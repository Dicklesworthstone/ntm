package ensemble

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// ModeLoader loads reasoning modes from multiple sources with precedence:
// embedded < user (~/.config/ntm/modes.toml) < project (.ntm/modes.toml).
type ModeLoader struct {
	// UserConfigDir is the user config directory (default: ~/.config/ntm).
	UserConfigDir string
	// ProjectDir is the project root (for .ntm/modes.toml).
	ProjectDir string
}

// modesFile is the TOML structure for user/project mode files.
type modesFile struct {
	Modes []ReasoningMode `toml:"modes"`
}

// NewModeLoader creates a loader with default paths.
func NewModeLoader() *ModeLoader {
	return &ModeLoader{
		UserConfigDir: defaultModeConfigDir(),
		ProjectDir:    currentDir(),
	}
}

// defaultModeConfigDir returns the user config directory for NTM.
func defaultModeConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm")
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".config", "ntm")
}

// currentDir returns the current working directory or empty string.
func currentDir() string {
	dir, _ := os.Getwd()
	return dir
}

// Load loads and merges modes from all sources, returning a validated catalog.
// Missing user/project files are not errors; invalid content is.
func (l *ModeLoader) Load() (*ModeCatalog, error) {
	// Start with embedded modes indexed by ID
	merged := make(map[string]ReasoningMode, len(EmbeddedModes))
	for _, m := range EmbeddedModes {
		merged[m.ID] = m
	}

	// Layer user modes
	userPath := filepath.Join(l.UserConfigDir, "modes.toml")
	if err := l.mergeFromFile(merged, userPath, "user"); err != nil {
		return nil, fmt.Errorf("user modes (%s): %w", userPath, err)
	}

	// Layer project modes (highest precedence)
	if l.ProjectDir != "" {
		projectPath := filepath.Join(l.ProjectDir, ".ntm", "modes.toml")
		if err := l.mergeFromFile(merged, projectPath, "project"); err != nil {
			return nil, fmt.Errorf("project modes (%s): %w", projectPath, err)
		}
	}

	// Collect into slice
	modes := make([]ReasoningMode, 0, len(merged))
	for _, m := range merged {
		modes = append(modes, m)
	}

	return NewModeCatalog(modes, CatalogVersion)
}

// mergeFromFile reads a TOML modes file and merges entries into the map.
// Missing files are silently skipped. Invalid content returns an error.
func (l *ModeLoader) mergeFromFile(merged map[string]ReasoningMode, path, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // missing is fine
		}
		return fmt.Errorf("read file: %w", err)
	}

	var file modesFile
	if _, err := toml.Decode(string(data), &file); err != nil {
		return fmt.Errorf("parse TOML: %w", err)
	}

	for i := range file.Modes {
		m := file.Modes[i]
		if m.ID == "" {
			return fmt.Errorf("modes[%d]: missing id", i)
		}
		// Default tier to advanced for user/project modes
		if m.Tier == "" {
			m.Tier = TierAdvanced
		}
		m.Source = source
		merged[m.ID] = m
	}

	return nil
}

// LoadModeCatalog is the convenience function for loading the full catalog
// with embedded + user + project sources merged.
func LoadModeCatalog() (*ModeCatalog, error) {
	return NewModeLoader().Load()
}

// Global singleton for thread-safe catalog access.
var (
	globalCatalog     *ModeCatalog
	globalCatalogOnce sync.Once
	globalCatalogErr  error
)

// GlobalCatalog returns the shared mode catalog, initializing it on first call.
// Thread-safe via sync.Once. Returns the cached catalog on subsequent calls.
func GlobalCatalog() (*ModeCatalog, error) {
	globalCatalogOnce.Do(func() {
		globalCatalog, globalCatalogErr = LoadModeCatalog()
	})
	return globalCatalog, globalCatalogErr
}

// ResetGlobalCatalog clears the global catalog singleton (for testing only).
func ResetGlobalCatalog() {
	globalCatalogOnce = sync.Once{}
	globalCatalog = nil
	globalCatalogErr = nil
}
