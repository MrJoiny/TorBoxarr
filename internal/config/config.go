package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDataRoot     = "/data"
	defaultDatabasePath = "/var/lib/torboxarr/torboxarr.db"
	defaultQBitUser     = "admin"
	defaultServerAddr   = ":8085"
)

type Config struct {
	Server struct {
		Address string
		BaseURL string
	}

	Logging struct {
		Level string
	}

	Database struct {
		Path        string
		BusyTimeout time.Duration
	}

	Data struct {
		Root      string
		Staging   string
		Completed string
		Payloads  string
	}

	TorBox struct {
		BaseURL               string
		APIToken              string
		RequestTimeout        time.Duration
		DownloadTimeout       time.Duration
		CreatePerMinute       int
		CreatePerHour         int
		PollPerMinute         int
		DownloadLinkPerMinute int
		UserAgent             string
	}

	Auth struct {
		QBitUsername string
		QBitPassword string
		SABAPIKey    string
		SABNZBKey    string
		SessionTTL   time.Duration
	}

	Compatibility struct {
		QBitVersion     string
		QBitWebAPI      string
		SABVersion      string
		DefaultCategory string
	}

	Workers struct {
		SubmitInterval   time.Duration
		PollInterval     time.Duration
		DownloadInterval time.Duration
		FinalizeInterval time.Duration
		RemoveInterval   time.Duration
		PruneInterval    time.Duration
		SubmitRetryMin   time.Duration
		SubmitRetryMax   time.Duration
		RemovedRetention time.Duration
		BatchSize        int
	}
}

func Load() (*Config, error) {
	cfg := defaultConfig()
	if err := loadDotEnv(".env"); err != nil {
		return nil, err
	}
	applyEnv(&cfg)
	cfg.applyDerived()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func defaultConfig() Config {
	var cfg Config
	cfg.Server.Address = defaultServerAddr
	cfg.Server.BaseURL = "http://localhost:8085"
	cfg.Logging.Level = "INFO"
	cfg.Database.Path = defaultDatabasePath
	cfg.Database.BusyTimeout = 5 * time.Second
	cfg.Data.Root = defaultDataRoot
	cfg.TorBox.BaseURL = "https://api.torbox.app/v1"
	cfg.TorBox.RequestTimeout = 45 * time.Second
	cfg.TorBox.DownloadTimeout = 0
	cfg.TorBox.CreatePerMinute = 10
	cfg.TorBox.CreatePerHour = 60
	cfg.TorBox.PollPerMinute = 300
	cfg.TorBox.DownloadLinkPerMinute = 300
	cfg.TorBox.UserAgent = "torboxarr/0.1"
	cfg.Auth.QBitUsername = defaultQBitUser
	cfg.Auth.SessionTTL = 24 * time.Hour
	cfg.Compatibility.QBitVersion = "5.0.0"
	cfg.Compatibility.QBitWebAPI = "2.11.3"
	cfg.Compatibility.SABVersion = "4.5.1"
	cfg.Compatibility.DefaultCategory = "torboxarr"
	cfg.Workers.SubmitInterval = 5 * time.Second
	cfg.Workers.PollInterval = 30 * time.Second
	cfg.Workers.DownloadInterval = 5 * time.Second
	cfg.Workers.FinalizeInterval = 3 * time.Second
	cfg.Workers.RemoveInterval = 5 * time.Second
	cfg.Workers.PruneInterval = 12 * time.Hour
	cfg.Workers.SubmitRetryMin = 15 * time.Second
	cfg.Workers.SubmitRetryMax = 15 * time.Minute
	cfg.Workers.RemovedRetention = 30 * 24 * time.Hour
	cfg.Workers.BatchSize = 25
	cfg.applyDerived()
	return cfg
}

func (c *Config) applyDerived() {
	root := strings.TrimSpace(c.Data.Root)
	if root == "" {
		root = defaultDataRoot
	}
	dbPath := strings.TrimSpace(c.Database.Path)
	if dbPath == "" {
		dbPath = defaultDatabasePath
	}
	c.Data.Root = root
	c.Database.Path = dbPath
	c.Data.Staging = filepath.Join(root, "staging")
	c.Data.Completed = filepath.Join(root, "completed")
	c.Data.Payloads = filepath.Join(root, "payloads")

	if strings.TrimSpace(c.Auth.QBitUsername) == "" {
		c.Auth.QBitUsername = defaultQBitUser
	}
	if strings.TrimSpace(c.Auth.SABNZBKey) == "" {
		c.Auth.SABNZBKey = c.Auth.SABAPIKey
	}
}

func applyEnv(cfg *Config) {
	setString := func(ptr *string, key string) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			*ptr = v
		}
	}

	setString(&cfg.Server.BaseURL, "TORBOXARR_SERVER_BASE_URL")
	setString(&cfg.Logging.Level, "TORBOXARR_LOG_LEVEL")
	setString(&cfg.Data.Root, "TORBOXARR_DATA_ROOT")
	setString(&cfg.Database.Path, "TORBOXARR_DATABASE_PATH")

	setString(&cfg.TorBox.APIToken, "TORBOXARR_TORBOX_API_TOKEN")
	setString(&cfg.Auth.QBitPassword, "TORBOXARR_QBIT_PASSWORD")
	setString(&cfg.Auth.SABAPIKey, "TORBOXARR_SAB_API_KEY")
	setString(&cfg.Auth.SABNZBKey, "TORBOXARR_SAB_NZB_KEY")
}

func (c *Config) Validate() error {
	switch {
	case c.Server.Address == "":
		return errors.New("server.address is required")
	case c.Server.BaseURL == "":
		return errors.New("server.base_url is required")
	case c.Database.Path == "":
		return errors.New("database.path is required")
	case c.Data.Root == "":
		return errors.New("data.root is required")
	case c.Data.Staging == "":
		return errors.New("data.staging is required")
	case c.Data.Completed == "":
		return errors.New("data.completed is required")
	case c.Auth.QBitUsername == "":
		return errors.New("auth.qbit_username is required")
	}
	if err := validateSecret("torbox.api_token", c.TorBox.APIToken); err != nil {
		return err
	}
	if err := validateSecret("auth.qbit_password", c.Auth.QBitPassword); err != nil {
		return err
	}
	if err := validateSecret("auth.sab_api_key", c.Auth.SABAPIKey); err != nil {
		return err
	}
	if err := validateSecret("auth.sab_nzb_key", c.Auth.SABNZBKey); err != nil {
		return err
	}
	return nil
}

func validateSecret(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if isEnvPlaceholder(value) {
		return fmt.Errorf("%s must be resolved before startup", field)
	}
	return nil
}

func isEnvPlaceholder(value string) bool {
	return strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}")
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("parse %s:%d: missing '='", path, lineNo)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return fmt.Errorf("parse %s:%d: empty key", path, lineNo)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}

		parsed, err := parseDotEnvValue(value)
		if err != nil {
			return fmt.Errorf("parse %s:%d: %w", path, lineNo, err)
		}
		if err := os.Setenv(key, parsed); err != nil {
			return fmt.Errorf("set %s from %s:%d: %w", key, path, lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	return nil
}

func parseDotEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "\"") {
		parsed, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value: %w", err)
		}
		return parsed, nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", errors.New("invalid single-quoted value")
		}
		return value[1 : len(value)-1], nil
	}
	return value, nil
}
