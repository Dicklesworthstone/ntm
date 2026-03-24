// Package config provides scanner configuration types and loading.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ScannerConfig holds UBS scanner configuration
type ScannerConfig struct {
	// UBSPath is the path to the UBS executable (auto-detected if empty)
	UBSPath string `toml:"ubs_path" yaml:"ubs_path"`

	// Defaults are the default scan settings
	Defaults ScannerDefaults `toml:"defaults" yaml:"defaults"`

	// Thresholds for different contexts
	Thresholds ScannerThresholds `toml:"thresholds" yaml:"thresholds"`

	// Tools configures which UBS tools to enable/disable
	Tools ScannerTools `toml:"tools" yaml:"tools"`

	// Beads configures auto-bead creation from findings
	Beads ScannerBeads `toml:"beads" yaml:"beads"`

	// Notifications for scan events
	Notifications ScannerNotifications `toml:"notifications" yaml:"notifications"`
}

// ScannerDefaults holds default scan settings
type ScannerDefaults struct {
	// Timeout for scans (e.g., "60s", "2m")
	Timeout string `toml:"timeout" yaml:"timeout"`

	// Parallel enables parallel scanning
	Parallel bool `toml:"parallel" yaml:"parallel"`

	// Exclude patterns for files/directories to skip
	Exclude []string `toml:"exclude" yaml:"exclude"`

	// Languages to scan (empty = auto-detect)
	Languages []string `toml:"languages" yaml:"languages"`
}

// ScannerThresholds holds severity thresholds for different contexts
type ScannerThresholds struct {
	PreCommit   ThresholdConfig `toml:"pre_commit" yaml:"pre_commit"`
	CI          ThresholdConfig `toml:"ci" yaml:"ci"`
	Dashboard   ThresholdConfig `toml:"dashboard" yaml:"dashboard"`
	Interactive ThresholdConfig `toml:"interactive" yaml:"interactive"`
}

// ThresholdConfig defines what should fail/block in a given context
type ThresholdConfig struct {
	// BlockCritical blocks if any critical issues found
	BlockCritical bool `toml:"block_critical" yaml:"block_critical"`

	// FailCritical fails (non-zero exit) if any critical issues found
	FailCritical bool `toml:"fail_critical" yaml:"fail_critical"`

	// BlockErrors blocks if >= this many errors (0 = disabled)
	BlockErrors int `toml:"block_errors" yaml:"block_errors"`

	// FailErrors fails if > this many errors (0 = any error fails, -1 = disabled)
	FailErrors int `toml:"fail_errors" yaml:"fail_errors"`

	// ShowWarnings includes warnings in output
	ShowWarnings bool `toml:"show_warnings" yaml:"show_warnings"`

	// ShowInfo includes info-level findings in output
	ShowInfo bool `toml:"show_info" yaml:"show_info"`
}

// ScannerTools configures which UBS tools to run
type ScannerTools struct {
	// Enabled lists tools to explicitly enable
	Enabled []string `toml:"enabled" yaml:"enabled"`

	// Disabled lists tools to explicitly disable
	Disabled []string `toml:"disabled" yaml:"disabled"`
}

// ScannerBeads configures automatic bead creation from findings
type ScannerBeads struct {
	// AutoCreate enables automatic bead creation
	AutoCreate bool `toml:"auto_create" yaml:"auto_create"`

	// MinSeverity is the minimum severity for auto-creating beads
	// Valid values: "critical", "error", "warning", "info"
	MinSeverity string `toml:"min_severity" yaml:"min_severity"`

	// AutoClose closes beads when findings are fixed
	AutoClose bool `toml:"auto_close" yaml:"auto_close"`

	// Labels to add to auto-created beads
	Labels []string `toml:"labels" yaml:"labels"`
}

// ScannerNotifications configures notifications for scan events
type ScannerNotifications struct {
	// Enabled enables notifications
	Enabled bool `toml:"enabled" yaml:"enabled"`

	// OnNewCritical notifies when new critical issues are found
	OnNewCritical bool `toml:"on_new_critical" yaml:"on_new_critical"`

	// SummaryAfterScan shows summary notification after scans
	SummaryAfterScan bool `toml:"summary_after_scan" yaml:"summary_after_scan"`
}

// DefaultScannerConfig returns sensible scanner defaults
func DefaultScannerConfig() ScannerConfig {
	return ScannerConfig{
		UBSPath: "", // Auto-detect
		Defaults: ScannerDefaults{
			Timeout:  "120s",
			Parallel: true,
			Exclude: []string{
				"vendor/**",
				"node_modules/**",
				".git/**",
				"*.min.js",
				"*.min.css",
			},
			Languages: nil, // Auto-detect
		},
		Thresholds: ScannerThresholds{
			PreCommit: ThresholdConfig{
				BlockCritical: true,
				BlockErrors:   5,
				ShowWarnings:  true,
				ShowInfo:      false,
			},
			CI: ThresholdConfig{
				FailCritical: true,
				FailErrors:   0, // Any error fails
				ShowWarnings: true,
				ShowInfo:     false,
			},
			Dashboard: ThresholdConfig{
				ShowWarnings: true,
				ShowInfo:     false,
			},
			Interactive: ThresholdConfig{
				ShowWarnings: true,
				ShowInfo:     true,
			},
		},
		Tools: ScannerTools{
			Enabled:  nil, // Use UBS defaults
			Disabled: nil,
		},
		Beads: ScannerBeads{
			AutoCreate:  false, // Opt-in
			MinSeverity: "warning",
			AutoClose:   true,
			Labels:      []string{"ubs-scan"},
		},
		Notifications: ScannerNotifications{
			Enabled:          true,
			OnNewCritical:    true,
			SummaryAfterScan: false,
		},
	}
}

// ValidateScannerConfig validates UBS scanner configuration.
func ValidateScannerConfig(cfg *ScannerConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.Defaults.Timeout != "" {
		dur, err := time.ParseDuration(cfg.Defaults.Timeout)
		if err != nil {
			return fmt.Errorf("defaults.timeout: %w", err)
		}
		if dur <= 0 {
			return fmt.Errorf("defaults.timeout: must be greater than zero, got %q", cfg.Defaults.Timeout)
		}
	}

	validateThreshold := func(path string, threshold ThresholdConfig) error {
		if threshold.BlockErrors < 0 {
			return fmt.Errorf("%s.block_errors: must be non-negative, got %d", path, threshold.BlockErrors)
		}
		if threshold.FailErrors < -1 {
			return fmt.Errorf("%s.fail_errors: must be -1 or greater, got %d", path, threshold.FailErrors)
		}
		return nil
	}
	if err := validateThreshold("thresholds.pre_commit", cfg.Thresholds.PreCommit); err != nil {
		return err
	}
	if err := validateThreshold("thresholds.ci", cfg.Thresholds.CI); err != nil {
		return err
	}
	if err := validateThreshold("thresholds.dashboard", cfg.Thresholds.Dashboard); err != nil {
		return err
	}
	if err := validateThreshold("thresholds.interactive", cfg.Thresholds.Interactive); err != nil {
		return err
	}

	allowedSeverity := map[string]bool{
		"critical": true,
		"error":    true,
		"warning":  true,
		"info":     true,
	}
	if cfg.Beads.MinSeverity != "" && !allowedSeverity[strings.ToLower(strings.TrimSpace(cfg.Beads.MinSeverity))] {
		return fmt.Errorf("beads.min_severity: must be one of critical, error, warning, info; got %q", cfg.Beads.MinSeverity)
	}

	disabled := make(map[string]struct{}, len(cfg.Tools.Disabled))
	for _, tool := range cfg.Tools.Disabled {
		disabled[tool] = struct{}{}
	}
	for _, tool := range cfg.Tools.Enabled {
		if _, ok := disabled[tool]; ok {
			return fmt.Errorf("tools: %q cannot be both enabled and disabled", tool)
		}
	}

	return nil
}

// GetTimeout returns the timeout as a time.Duration
func (d *ScannerDefaults) GetTimeout() time.Duration {
	if d.Timeout == "" {
		return 120 * time.Second
	}
	dur, err := time.ParseDuration(d.Timeout)
	if err != nil {
		return 120 * time.Second
	}
	return dur
}

// ProjectScannerConfig represents a project-level scanner configuration
// typically stored in .ntm.yaml at the project root
type ProjectScannerConfig struct {
	Scanner ScannerConfig `yaml:"scanner"`
}

// LoadProjectScannerConfig loads scanner config from a project directory.
// It searches for:
// 1. .ntm.yaml in the project root
// 2. .ntm.toml in the project root
// 3. Returns defaults if not found
func LoadProjectScannerConfig(projectDir string) (*ScannerConfig, error) {
	// Try .ntm.yaml first
	yamlPath := filepath.Join(projectDir, ".ntm.yaml")
	if data, err := os.ReadFile(yamlPath); err == nil {
		var projCfg ProjectScannerConfig
		if err := yaml.Unmarshal(data, &projCfg); err != nil {
			return nil, err
		}
		cfg := mergeWithDefaults(projCfg.Scanner)
		applyEnvOverrides(&cfg)
		return &cfg, nil
	}

	// Try .ntm.yml
	ymlPath := filepath.Join(projectDir, ".ntm.yml")
	if data, err := os.ReadFile(ymlPath); err == nil {
		var projCfg ProjectScannerConfig
		if err := yaml.Unmarshal(data, &projCfg); err != nil {
			return nil, err
		}
		cfg := mergeWithDefaults(projCfg.Scanner)
		applyEnvOverrides(&cfg)
		return &cfg, nil
	}

	// Return defaults
	cfg := DefaultScannerConfig()
	applyEnvOverrides(&cfg)
	return &cfg, nil
}

// mergeWithDefaults merges user config with defaults
func mergeWithDefaults(user ScannerConfig) ScannerConfig {
	defaults := DefaultScannerConfig()

	// Merge defaults where user hasn't specified
	if user.Defaults.Timeout == "" {
		user.Defaults.Timeout = defaults.Defaults.Timeout
	}
	if user.Defaults.Exclude == nil {
		user.Defaults.Exclude = defaults.Defaults.Exclude
	}
	if user.Beads.MinSeverity == "" {
		user.Beads.MinSeverity = defaults.Beads.MinSeverity
	}
	if user.Beads.Labels == nil {
		user.Beads.Labels = defaults.Beads.Labels
	}

	return user
}

// applyEnvOverrides applies environment variable overrides to scanner config
func applyEnvOverrides(cfg *ScannerConfig) {
	// UBS_PATH overrides ubs_path
	if path := os.Getenv("UBS_PATH"); path != "" {
		cfg.UBSPath = path
	}

	// NTM_SCANNER_TIMEOUT overrides timeout
	if timeout := os.Getenv("NTM_SCANNER_TIMEOUT"); timeout != "" {
		cfg.Defaults.Timeout = timeout
	}

	// NTM_SCANNER_AUTO_BEADS enables auto-bead creation
	if autoBeads := os.Getenv("NTM_SCANNER_AUTO_BEADS"); autoBeads != "" {
		cfg.Beads.AutoCreate = autoBeads == "1" || strings.ToLower(autoBeads) == "true"
	}

	// NTM_SCANNER_MIN_SEVERITY sets minimum severity for beads
	if minSev := os.Getenv("NTM_SCANNER_MIN_SEVERITY"); minSev != "" {
		cfg.Beads.MinSeverity = minSev
	}

	// NTM_SCANNER_BLOCK_CRITICAL enables blocking on critical in pre-commit
	if block := os.Getenv("NTM_SCANNER_BLOCK_CRITICAL"); block != "" {
		cfg.Thresholds.PreCommit.BlockCritical = block == "1" || strings.ToLower(block) == "true"
	}

	// NTM_SCANNER_FAIL_ERRORS sets error threshold for CI
	if failErrors := os.Getenv("NTM_SCANNER_FAIL_ERRORS"); failErrors != "" {
		if n, err := strconv.Atoi(failErrors); err == nil {
			cfg.Thresholds.CI.FailErrors = n
		}
	}
}

// IsToolEnabled checks if a tool should be run
func (t *ScannerTools) IsToolEnabled(toolName string) bool {
	// Check disabled list first
	for _, d := range t.Disabled {
		if strings.EqualFold(d, toolName) {
			return false
		}
	}

	// If enabled list is empty, tool is enabled
	if len(t.Enabled) == 0 {
		return true
	}

	// Check enabled list
	for _, e := range t.Enabled {
		if strings.EqualFold(e, toolName) {
			return true
		}
	}

	return false
}

// ShouldBlock returns true if findings should block based on threshold config
func (t *ThresholdConfig) ShouldBlock(criticalCount, errorCount int) bool {
	if t.BlockCritical && criticalCount > 0 {
		return true
	}
	if t.BlockErrors > 0 && errorCount >= t.BlockErrors {
		return true
	}
	return false
}

// ShouldFail returns true if findings should cause a non-zero exit
func (t *ThresholdConfig) ShouldFail(criticalCount, errorCount int) bool {
	if t.FailCritical && criticalCount > 0 {
		return true
	}
	// FailErrors < 0 disables this check; >= 0 fails if errorCount exceeds threshold
	if t.FailErrors >= 0 && errorCount > t.FailErrors {
		return true
	}
	return false
}
