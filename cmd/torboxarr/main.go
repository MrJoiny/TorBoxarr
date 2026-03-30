package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mrjoiny/torboxarr/internal/api"
	"github.com/mrjoiny/torboxarr/internal/auth"
	"github.com/mrjoiny/torboxarr/internal/config"
	"github.com/mrjoiny/torboxarr/internal/files"
	"github.com/mrjoiny/torboxarr/internal/store"
	"github.com/mrjoiny/torboxarr/internal/torbox"
	"github.com/mrjoiny/torboxarr/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Logging.Level)
	logger.Info("configuration loaded",
		"log_level", cfg.Logging.Level,
		"server_address", cfg.Server.Address,
		"server_base_url", cfg.Server.BaseURL,
		"db_path", cfg.Database.Path,
		"data_root", cfg.Data.Root,
		"staging_path", cfg.Data.Staging,
		"completed_path", cfg.Data.Completed,
		"payloads_path", cfg.Data.Payloads,
		"torbox_base_url", cfg.TorBox.BaseURL,
	)
	if !strings.Contains(cfg.Server.Address, "127.0.0.1") &&
		!strings.Contains(cfg.Server.Address, "localhost") {
		logger.Warn("server is binding to all interfaces",
			"address", cfg.Server.Address,
			"hint", "set server.address to 127.0.0.1:8085 if not intended for network access")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	layout := files.NewLayout(cfg.Data.Root, cfg.Data.Staging, cfg.Data.Completed, cfg.Data.Payloads)
	logger.Debug("ensuring filesystem layout")
	if err := layout.Ensure(); err != nil {
		return err
	}
	logger.Info("filesystem layout ready")

	logger.Debug("opening database", "path", cfg.Database.Path, "busy_timeout", cfg.Database.BusyTimeout.String())
	db, err := store.Open(ctx, cfg.Database.Path, cfg.Database.BusyTimeout)
	if err != nil {
		return err
	}
	defer db.Close()

	logger.Debug("running database migrations (embedded)")
	if err := store.RunMigrationsFS(db, store.EmbeddedMigrations); err != nil {
		return err
	}
	logger.Info("database ready")
	st := store.New(db)

	createLimiter := torbox.NewMultiLimiter(
		torbox.NewTokenBucket(cfg.TorBox.CreatePerMinute, time.Minute),
		torbox.NewTokenBucket(cfg.TorBox.CreatePerHour, time.Hour),
	)
	pollLimiter := torbox.NewTokenBucket(cfg.TorBox.PollPerMinute, time.Minute)
	dlLimiter := torbox.NewTokenBucket(cfg.TorBox.DownloadLinkPerMinute, time.Minute)
	tbClient := torbox.NewHTTPClient(logger, cfg.TorBox.BaseURL, cfg.TorBox.APIToken, cfg.TorBox.UserAgent, cfg.TorBox.RequestTimeout, createLimiter, pollLimiter, dlLimiter)
	downloader := files.NewRangeDownloader(logger, cfg.TorBox.DownloadTimeout)

	qbitAuth := auth.NewQBitSessionManager(st, cfg.Auth.QBitUsername, cfg.Auth.QBitPassword, cfg.Auth.SessionTTL)
	sabAuth := auth.NewSABAuth(cfg.Auth.SABAPIKey, cfg.Auth.SABNZBKey)

	apiServer := api.NewServer(cfg, logger, st, layout, qbitAuth, sabAuth)
	orchestrator := worker.NewOrchestrator(cfg, logger, st, layout, downloader, tbClient)
	logger.Info("starting background workers",
		"submit_interval", cfg.Workers.SubmitInterval.String(),
		"poll_interval", cfg.Workers.PollInterval.String(),
		"download_interval", cfg.Workers.DownloadInterval.String(),
		"finalize_interval", cfg.Workers.FinalizeInterval.String(),
		"remove_interval", cfg.Workers.RemoveInterval.String(),
		"prune_interval", cfg.Workers.PruneInterval.String(),
		"batch_size", cfg.Workers.BatchSize,
	)
	if err := orchestrator.Start(ctx); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           apiServer.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("server listening", "address", cfg.Server.Address)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server exited", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown requested")
	orchestrator.Wait()
	logger.Info("all workers stopped")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		slogLevel = slog.LevelDebug
	case "WARN":
		slogLevel = slog.LevelWarn
	case "ERROR":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel})
	return slog.New(handler)
}
