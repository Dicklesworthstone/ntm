package ensemble

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// EnsembleLoader loads ensemble presets from multiple sources with precedence:
// embedded < user (~/.config/ntm/ensembles.toml) < project (.ntm/ensembles.toml).
type EnsembleLoader struct {
	// UserConfigDir is the user config directory (default: ~/.config/ntm).
	UserConfigDir string
	// ProjectDir is the project root (for .ntm/ensembles.toml).
	ProjectDir string
	// ModeCatalog is used to validate mode references.
	ModeCatalog *ModeCatalog
}

// ensemblesFile is the TOML structure for user/project ensemble files.
type ensemblesFile struct {
	Ensembles []EnsemblePreset `toml:"ensembles"`
}

// NewEnsembleLoader creates a loader with default paths.
func NewEnsembleLoader(catalog *ModeCatalog) *EnsembleLoader {
	return &EnsembleLoader{
		UserConfigDir: defaultModeConfigDir(),
		ProjectDir:    currentDir(),
		ModeCatalog:   catalog,
	}
}

// Load loads and merges ensembles from all sources, returning a validated list.
// Missing user/project files are not errors; invalid content is.
func (l *EnsembleLoader) Load() ([]EnsemblePreset, error) {
	// Start with embedded ensembles indexed by name
	merged := make(map[string]EnsemblePreset, len(EmbeddedEnsembles))
	for _, e := range EmbeddedEnsembles {
		preset := e
		preset.Source = "embedded"
		merged[e.Name] = preset
	}

	// Layer user ensembles
	userPath := filepath.Join(l.UserConfigDir, "ensembles.toml")
	if err := l.mergeFromFile(merged, userPath, "user"); err != nil {
		return nil, fmt.Errorf("user ensembles (%s): %w", userPath, err)
	}

	// Layer project ensembles (highest precedence)
	if l.ProjectDir != "" {
		projectPath := filepath.Join(l.ProjectDir, ".ntm", "ensembles.toml")
		if err := l.mergeFromFile(merged, projectPath, "project"); err != nil {
			return nil, fmt.Errorf("project ensembles (%s): %w", projectPath, err)
		}
	}

	// Collect into slice preserving embedded order, then user/project additions
	result := make([]EnsemblePreset, 0, len(merged))

	// Add embedded ensembles in their original order
	for _, e := range EmbeddedEnsembles {
		if preset, ok := merged[e.Name]; ok {
			result = append(result, preset)
			delete(merged, e.Name)
		}
	}

	// Add remaining (user/project defined) ensembles
	for _, e := range merged {
		result = append(result, e)
	}

	return result, nil
}

// mergeFromFile reads a TOML ensembles file and merges entries into the map.
// Missing files are silently skipped. Invalid content returns an error.
func (l *EnsembleLoader) mergeFromFile(merged map[string]EnsemblePreset, path, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // missing is fine
		}
		return fmt.Errorf("read file: %w", err)
	}

	var file ensemblesFile
	if _, err := toml.Decode(string(data), &file); err != nil {
		return fmt.Errorf("parse TOML: %w", err)
	}

	for i := range file.Ensembles {
		e := file.Ensembles[i]
		if e.Name == "" {
			return fmt.Errorf("ensembles[%d]: missing name", i)
		}

		// Validate mode refs if catalog is available
		if l.ModeCatalog != nil {
			if err := e.Validate(l.ModeCatalog); err != nil {
				return fmt.Errorf("ensembles[%d] (%s): %w", i, e.Name, err)
			}

			// Check tier constraints
			if !e.AllowAdvanced {
				for _, ref := range e.Modes {
					id, _ := ref.Resolve(l.ModeCatalog)
					mode := l.ModeCatalog.GetMode(id)
					if mode != nil && mode.Tier != TierCore {
						return fmt.Errorf("ensembles[%d] (%s): mode %q is tier %q but allow_advanced is false",
							i, e.Name, id, mode.Tier)
					}
				}
			}
		}

		e.Source = source
		merged[e.Name] = e
	}

	return nil
}

// LoadEnsembles is the convenience function for loading all ensembles
// with embedded + user + project sources merged.
func LoadEnsembles(catalog *ModeCatalog) ([]EnsemblePreset, error) {
	return NewEnsembleLoader(catalog).Load()
}

// EnsembleRegistry holds loaded ensembles indexed by name.
type EnsembleRegistry struct {
	ensembles []EnsemblePreset
	byName    map[string]*EnsemblePreset
	catalog   *ModeCatalog
}

// NewEnsembleRegistry creates a registry from loaded ensembles.
func NewEnsembleRegistry(ensembles []EnsemblePreset, catalog *ModeCatalog) *EnsembleRegistry {
	byName := make(map[string]*EnsemblePreset, len(ensembles))
	for i := range ensembles {
		byName[ensembles[i].Name] = &ensembles[i]
	}
	return &EnsembleRegistry{
		ensembles: ensembles,
		byName:    byName,
		catalog:   catalog,
	}
}

// Get returns an ensemble by name, or nil if not found.
func (r *EnsembleRegistry) Get(name string) *EnsemblePreset {
	return r.byName[name]
}

// List returns all ensembles.
func (r *EnsembleRegistry) List() []EnsemblePreset {
	return r.ensembles
}

// ListByTag returns ensembles that have the given tag.
func (r *EnsembleRegistry) ListByTag(tag string) []EnsemblePreset {
	var result []EnsemblePreset
	for _, e := range r.ensembles {
		for _, t := range e.Tags {
			if t == tag {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

// Count returns the number of ensembles.
func (r *EnsembleRegistry) Count() int {
	return len(r.ensembles)
}

// Global singleton for thread-safe ensemble registry access.
var (
	globalRegistry     *EnsembleRegistry
	globalRegistryOnce sync.Once
	globalRegistryErr  error
)

// GlobalEnsembleRegistry returns the shared ensemble registry, initializing it on first call.
// Thread-safe via sync.Once.
func GlobalEnsembleRegistry() (*EnsembleRegistry, error) {
	globalRegistryOnce.Do(func() {
		catalog, err := GlobalCatalog()
		if err != nil {
			globalRegistryErr = fmt.Errorf("load catalog: %w", err)
			return
		}
		ensembles, err := LoadEnsembles(catalog)
		if err != nil {
			globalRegistryErr = fmt.Errorf("load ensembles: %w", err)
			return
		}
		globalRegistry = NewEnsembleRegistry(ensembles, catalog)
	})
	return globalRegistry, globalRegistryErr
}

// ResetGlobalEnsembleRegistry clears the global registry singleton (for testing only).
func ResetGlobalEnsembleRegistry() {
	globalRegistryOnce = sync.Once{}
	globalRegistry = nil
	globalRegistryErr = nil
}
