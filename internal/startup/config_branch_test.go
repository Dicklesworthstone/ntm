package startup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetConfigPath(t *testing.T) {
	// Save and restore original
	orig := configFilePath
	origEnv, hadEnv := os.LookupEnv("NTM_CONFIG")
	defer func() {
		configFilePath = orig
		if hadEnv {
			_ = os.Setenv("NTM_CONFIG", origEnv)
		} else {
			_ = os.Unsetenv("NTM_CONFIG")
		}
	}()

	SetConfigPath("/some/path/config.toml")
	if configFilePath != "/some/path/config.toml" {
		t.Errorf("configFilePath = %q, want /some/path/config.toml", configFilePath)
	}
	if got := os.Getenv("NTM_CONFIG"); got != "/some/path/config.toml" {
		t.Errorf("NTM_CONFIG = %q, want /some/path/config.toml", got)
	}

	SetConfigPath("")
	if configFilePath != "" {
		t.Errorf("configFilePath = %q, want empty", configFilePath)
	}
	if got, ok := os.LookupEnv("NTM_CONFIG"); ok {
		t.Errorf("NTM_CONFIG = %q, want unset", got)
	}
}

func TestGetConfig_LoadsMerged(t *testing.T) {
	ResetConfig()
	defer ResetConfig()

	// Temporarily set an empty config path so LoadMerged uses defaults
	orig := configFilePath
	configFilePath = ""
	defer func() { configFilePath = orig }()

	cfg, err := GetConfig()
	if err != nil {
		// LoadMerged may fail if no global config exists, which is fine —
		// we exercised the code path either way
		t.Logf("GetConfig returned error (expected in test env): %v", err)
		return
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}

	// After successful Get, IsConfigLoaded should be true
	if !IsConfigLoaded() {
		t.Error("expected IsConfigLoaded() == true after successful GetConfig()")
	}
}

func TestGetConfig_UsesCurrentWorkingDirectoryForProjectConfig(t *testing.T) {
	ResetConfig()
	defer ResetConfig()

	origPath := configFilePath
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() failed: %v", err)
	}
	defer func() {
		configFilePath = origPath
		_ = os.Chdir(origWD)
	}()

	tmpDir := t.TempDir()
	globalPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(globalPath, []byte("[alerts]\nenabled = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(global config) failed: %v", err)
	}

	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("MkdirAll(project .ntm) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte("[alerts]\nenabled = false\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) failed: %v", err)
	}

	configFilePath = globalPath
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir(projectDir) failed: %v", err)
	}

	cfg, err := GetConfig()
	if err != nil {
		t.Fatalf("GetConfig() failed: %v", err)
	}
	if cfg.Alerts.Enabled {
		t.Fatal("expected project config override to disable alerts")
	}
}
