package ensemble

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// EnsembleLoader loads ensemble presets from multiple sources with precedence:
// embedded < imported (~/.config/ntm/ensembles.imported.toml) < user (~/.config/ntm/ensembles.toml) < project (.ntm/ensembles.toml).
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

const importedEnsemblesFilename = "ensembles.imported.toml"

func undecodedEnsembleFields(md toml.MetaData) []string {
	fields := md.Undecoded()
	if len(fields) == 0 {
		return nil
	}
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.String())
	}
	sort.Strings(names)
	return names
}

// ImportedEnsemblesPath returns the default path for imported ensembles.
func ImportedEnsemblesPath(userConfigDir string) string {
	if strings.TrimSpace(userConfigDir) == "" {
		userConfigDir = defaultModeConfigDir()
	}
	return filepath.Join(userConfigDir, importedEnsemblesFilename)
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

	// Layer imported ensembles (user-level imports, lower precedence than user-defined)
	importedPath := ImportedEnsemblesPath(l.UserConfigDir)
	if err := l.mergeFromFile(merged, importedPath, "imported"); err != nil {
		return nil, fmt.Errorf("imported ensembles (%s): %w", importedPath, err)
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

	// Add remaining (user/project defined) ensembles in a stable order. Map
	// iteration would otherwise make the robot/CLI preset list vary between
	// equivalent runs.
	remainingNames := make([]string, 0, len(merged))
	for name := range merged {
		remainingNames = append(remainingNames, name)
	}
	sort.Strings(remainingNames)
	for _, name := range remainingNames {
		result = append(result, merged[name])
	}

	if l.ModeCatalog != nil {
		report := ValidateEnsemblePresets(result, l.ModeCatalog)
		if err := report.Error(); err != nil {
			return nil, err
		}
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
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return fmt.Errorf("parse TOML: %w", err)
	}
	if unknown := undecodedEnsembleFields(md); len(unknown) > 0 {
		return fmt.Errorf("parse TOML: unknown field(s): %s", strings.Join(unknown, ", "))
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
		}

		e.Source = source
		merged[e.Name] = e
	}

	return nil
}

// LoadEnsemblesFile loads ensembles from a TOML file.
// Missing files return an empty slice and no error.
func LoadEnsemblesFile(path string) ([]EnsemblePreset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []EnsemblePreset{}, nil
		}
		return nil, fmt.Errorf("read ensembles file: %w", err)
	}

	var file ensemblesFile
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return nil, fmt.Errorf("parse TOML: %w", err)
	}
	if unknown := undecodedEnsembleFields(md); len(unknown) > 0 {
		return nil, fmt.Errorf("parse TOML: unknown field(s): %s", strings.Join(unknown, ", "))
	}
	if file.Ensembles == nil {
		return []EnsemblePreset{}, nil
	}
	return file.Ensembles, nil
}

// SaveEnsemblesFile writes ensembles to a TOML file, creating parent directories if needed.
func SaveEnsemblesFile(path string, presets []EnsemblePreset) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create ensembles dir: %w", err)
	}
	file := ensemblesFile{Ensembles: presets}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create ensembles file: %w", err)
	}
	defer f.Close()

	if err := toml.NewEncoder(f).Encode(file); err != nil {
		return fmt.Errorf("encode TOML: %w", err)
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
