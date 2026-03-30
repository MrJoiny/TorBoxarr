package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRejectsPlaceholderSecrets(t *testing.T) {
	cfg := defaultConfig()
	cfg.TorBox.APIToken = "${TORBOXARR_TORBOX_API_TOKEN}"
	cfg.Auth.QBitPassword = "qbit-secret"
	cfg.Auth.SABAPIKey = "sab-secret"
	cfg.applyDerived()

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for unresolved placeholder secret")
	}
}

func TestApplyEnvUsesMinimalSurface(t *testing.T) {
	cfg := defaultConfig()

	t.Setenv("TORBOXARR_SERVER_BASE_URL", "https://torboxarr.example.com")
	t.Setenv("TORBOXARR_LOG_LEVEL", "DEBUG")
	t.Setenv("TORBOXARR_DATA_ROOT", "/srv/torboxarr")
	t.Setenv("TORBOXARR_TORBOX_API_TOKEN", "resolved-token")
	t.Setenv("TORBOXARR_QBIT_PASSWORD", "resolved-password")
	t.Setenv("TORBOXARR_SAB_API_KEY", "resolved-sab-api-key")

	applyEnv(&cfg)
	cfg.applyDerived()

	if cfg.Server.BaseURL != "https://torboxarr.example.com" {
		t.Fatalf("Server.BaseURL = %q, want env value", cfg.Server.BaseURL)
	}
	if cfg.Logging.Level != "DEBUG" {
		t.Fatalf("Logging.Level = %q, want DEBUG", cfg.Logging.Level)
	}
	if cfg.Data.Root != "/srv/torboxarr" {
		t.Fatalf("Data.Root = %q, want env value", cfg.Data.Root)
	}
	if cfg.Database.Path != filepath.Join("/srv/torboxarr", "torboxarr.db") {
		t.Fatalf("Database.Path = %q, want derived path", cfg.Database.Path)
	}
	if cfg.Auth.QBitUsername != defaultQBitUser {
		t.Fatalf("Auth.QBitUsername = %q, want %q", cfg.Auth.QBitUsername, defaultQBitUser)
	}
	if cfg.Auth.SABNZBKey != "resolved-sab-api-key" {
		t.Fatalf("Auth.SABNZBKey = %q, want SAB API key fallback", cfg.Auth.SABNZBKey)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestApplyEnvAllowsExplicitSABNZBKey(t *testing.T) {
	cfg := defaultConfig()

	t.Setenv("TORBOXARR_TORBOX_API_TOKEN", "resolved-token")
	t.Setenv("TORBOXARR_QBIT_PASSWORD", "resolved-password")
	t.Setenv("TORBOXARR_SAB_API_KEY", "resolved-sab-api-key")
	t.Setenv("TORBOXARR_SAB_NZB_KEY", "resolved-sab-nzb-key")

	applyEnv(&cfg)
	cfg.applyDerived()

	if cfg.Auth.SABNZBKey != "resolved-sab-nzb-key" {
		t.Fatalf("Auth.SABNZBKey = %q, want explicit override", cfg.Auth.SABNZBKey)
	}
}

func TestLoadDotEnvSetsUnsetVariablesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "TORBOXARR_TORBOX_API_TOKEN=from-dotenv\nTORBOXARR_QBIT_PASSWORD=\"quoted password\"\nTORBOXARR_SAB_API_KEY=from-dotenv-sab\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	t.Setenv("TORBOXARR_TORBOX_API_TOKEN", "from-env")

	if err := loadDotEnv(path); err != nil {
		t.Fatalf("loadDotEnv() = %v", err)
	}
	if got := os.Getenv("TORBOXARR_TORBOX_API_TOKEN"); got != "from-env" {
		t.Fatalf("TORBOXARR_TORBOX_API_TOKEN = %q, want existing env to win", got)
	}
	if got := os.Getenv("TORBOXARR_QBIT_PASSWORD"); got != "quoted password" {
		t.Fatalf("TORBOXARR_QBIT_PASSWORD = %q, want parsed quoted value", got)
	}
	if got := os.Getenv("TORBOXARR_SAB_API_KEY"); got != "from-dotenv-sab" {
		t.Fatalf("TORBOXARR_SAB_API_KEY = %q, want dotenv value", got)
	}
}
