package startup

import (
	"os"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/profiler"
)

// configLoader manages lazy config loading
var configLoader = NewLazy[*config.Config]("config", func() (*config.Config, error) {
	span := profiler.StartWithPhase("config_load_inner", "deferred")
	defer span.End()

	// Use LoadMerged to include project-specific config
	cwd, _ := os.Getwd()
	cfg, err := config.LoadMerged(cwd, configFilePath)
	if err != nil {
		// If loading fails (e.g. project config invalid), return error
		// Note: LoadMerged handles global config missing by using defaults
		return nil, err
	}
	return cfg, nil
})

// configFilePath stores the custom config path if specified
var configFilePath string

// SetConfigPath sets the config file path for lazy loading and propagates the
// selected path to runtime helpers that consult NTM_CONFIG via DefaultPath().
func SetConfigPath(path string) {
	configFilePath = path
	if strings.TrimSpace(path) == "" {
		_ = os.Unsetenv("NTM_CONFIG")
		return
	}
	_ = os.Setenv("NTM_CONFIG", path)
}

// GetConfig returns the configuration, loading it lazily if needed
func GetConfig() (*config.Config, error) {
	return configLoader.Get()
}

// ResetConfig allows re-loading config. Test-only hook: retained because
// package tests across the repo (notably internal/cli) reset the lazy config
// loader between test cases; production code never calls it.
func ResetConfig() {
	configLoader.mu.Lock()
	defer configLoader.mu.Unlock()
	configLoader.done = false
	configLoader.initErr = nil
	configLoader.value = nil
}
