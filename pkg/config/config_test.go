package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatedHome points HOME/USERPROFILE at a throwaway directory so the config
// package's os.UserHomeDir()-based paths resolve inside a temp dir.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestConfigPathNewLocation(t *testing.T) {
	home := isolatedHome(t)

	if want := filepath.Join(home, ".go-rag", "config.yml"); ConfigPath() != want {
		t.Errorf("ConfigPath() = %q, want %q", ConfigPath(), want)
	}
	if dir := filepath.Dir(ConfigPath()); dir != ConfigDir() {
		t.Errorf("ConfigPath not inside ConfigDir: %q vs %q", dir, ConfigDir())
	}
	if _, err := os.Stat(ConfigPath()); !os.IsNotExist(err) {
		t.Errorf("ConfigPath should not exist in a fresh home, stat err = %v", err)
	}
}

func TestLoadDefaultsWhenNoConfig(t *testing.T) {
	isolatedHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Chunking.MaxTokens != 512 || cfg.Chunking.Overlap != 100 {
		t.Errorf("unexpected default chunking: %+v", cfg.Chunking)
	}
}

func TestLoadMigratesLegacyConfig(t *testing.T) {
	home := isolatedHome(t)

	legacyDir := filepath.Join(home, "AppData", "Roaming", "go-rag")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("create legacy dir: %v", err)
	}
	legacyYAML := "embedding:\n" +
		"  url: https://example.test/v1\n" +
		"  api_key: legacy-key\n" +
		"  model: legacy-model\n"
	legacyPath := filepath.Join(legacyDir, "config.yaml")
	if err := os.WriteFile(legacyPath, []byte(legacyYAML), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Embedding.APIKey != "legacy-key" {
		t.Errorf("APIKey = %q, want legacy-key", cfg.Embedding.APIKey)
	}

	// The config now lives at the new location and the legacy file is kept
	// untouched as a backup.
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if !strings.Contains(string(data), "legacy-key") {
		t.Errorf("migrated config does not contain legacy key:\n%s", data)
	}
	kept, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("legacy config must be preserved: %v", err)
	}
	if string(kept) != legacyYAML {
		t.Errorf("legacy file was modified:\n%s", kept)
	}
}

func TestLoadMigratesLegacyDashKeys(t *testing.T) {
	// Migration copies the legacy bytes verbatim, so dash-style keys written
	// by hand stay dash-style at the new location. The struct tags use
	// underscores, so those keys are still silently ignored (use
	// `go-rag config set` to rewrite them properly).
	isolatedHome(t)

	legacyDir := filepath.Join(homeDir(), ".config", "go-rag")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("create legacy dir: %v", err)
	}
	legacyPath := filepath.Join(legacyDir, "config.yaml")
	if err := os.WriteFile(legacyPath, []byte("embedding:\n  api-key: dash\n"), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Embedding.APIKey != "" {
		t.Errorf("dash key should still be ignored, APIKey = %q", cfg.Embedding.APIKey)
	}
}
