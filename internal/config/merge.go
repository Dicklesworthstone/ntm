package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadMerged loads the global config and merges any project-specific config found starting from cwd.
func LoadMerged(cwd, globalPath string) (*Config, error) {
	// Load global
	cfg, err := Load(globalPath)
	if err != nil {
		return nil, fmt.Errorf("loading global config: %w", err)
	}

	// Find project config
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	projectDir, projectCfg, err := FindProjectConfig(cwd)
	if err != nil {
		// Return error if project config is invalid, so user knows
		return cfg, fmt.Errorf("loading project config: %w", err)
	}

	if projectCfg != nil {
		cfg = MergeConfig(cfg, projectCfg, projectDir)
	}

	return cfg, nil
}

// MergeConfig merges project config into global config.
func MergeConfig(global *Config, project *ProjectConfig, projectDir string) *Config {
	// Merge Agents - DISABLED for security
	// Project-level config should not be able to override agent execution commands
	// to prevent RCE from malicious repositories.
	/*
		if project.Agents.Claude != "" {
			global.Agents.Claude = project.Agents.Claude
		}
		if project.Agents.Codex != "" {
			global.Agents.Codex = project.Agents.Codex
		}
		if project.Agents.Gemini != "" {
			global.Agents.Gemini = project.Agents.Gemini
		}
	*/

	// Merge Defaults
	if len(project.Defaults.Agents) > 0 {
		global.ProjectDefaults = project.Defaults.Agents
	}

	// Merge Alerts
	if project.Alerts != nil {
		if project.Alerts.Enabled != nil {
			global.Alerts.Enabled = *project.Alerts.Enabled
		}
		if project.Alerts.AgentStuckMinutes != nil {
			global.Alerts.AgentStuckMinutes = *project.Alerts.AgentStuckMinutes
		}
		if project.Alerts.DiskLowThresholdGB != nil {
			global.Alerts.DiskLowThresholdGB = *project.Alerts.DiskLowThresholdGB
		}
		if project.Alerts.MailBacklogThreshold != nil {
			global.Alerts.MailBacklogThreshold = *project.Alerts.MailBacklogThreshold
		}
		if project.Alerts.BeadStaleHours != nil {
			global.Alerts.BeadStaleHours = *project.Alerts.BeadStaleHours
		}
		if project.Alerts.ContextWarningThreshold != nil {
			global.Alerts.ContextWarningThreshold = *project.Alerts.ContextWarningThreshold
		}
		if project.Alerts.ResolvedPruneMinutes != nil {
			global.Alerts.ResolvedPruneMinutes = *project.Alerts.ResolvedPruneMinutes
		}
	}

	// Merge Palette File
	if project.Palette.File != "" {
		// Prevent traversal
		cleanFile := filepath.Clean(project.Palette.File)
		if isUnsafeProjectRelativePath(cleanFile) {
			// Don't error, just ignore unsafe path
			fmt.Printf("Warning: ignoring unsafe project palette path: %s\n", project.Palette.File)
		} else {
			// Try .ntm/ first (legacy/convention)
			palettePath := filepath.Join(projectDir, ".ntm", cleanFile)
			if _, err := os.Stat(palettePath); os.IsNotExist(err) {
				// Try relative to project root
				palettePath = filepath.Join(projectDir, cleanFile)
			}

			if cmds, err := LoadPaletteFromMarkdown(palettePath); err == nil && len(cmds) > 0 {
				// Prepend project commands so they take precedence
				allCmds := append(cmds, global.Palette...)

				// Deduplicate by key
				seen := make(map[string]bool)
				unique := make([]PaletteCmd, 0, len(allCmds))
				for _, cmd := range allCmds {
					if !seen[cmd.Key] {
						seen[cmd.Key] = true
						unique = append(unique, cmd)
					}
				}
				global.Palette = unique
			}
		}
	}

	// Merge palette state (favorites/pins). Project entries come first.
	global.PaletteState.Pinned = mergeStringListPreferFirst(project.PaletteState.Pinned, global.PaletteState.Pinned)
	global.PaletteState.Favorites = mergeStringListPreferFirst(project.PaletteState.Favorites, global.PaletteState.Favorites)

	return global
}

func isUnsafeProjectRelativePath(cleanPath string) bool {
	if cleanPath == "" {
		return false
	}
	if filepath.IsAbs(cleanPath) {
		return true
	}
	parentPrefix := ".." + string(filepath.Separator)
	return cleanPath == ".." || strings.HasPrefix(cleanPath, parentPrefix)
}

func mergeStringListPreferFirst(primary, secondary []string) []string {
	if len(primary) == 0 && len(secondary) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(primary)+len(secondary))
	out := make([]string, 0, len(primary)+len(secondary))
	for _, v := range primary {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, v := range secondary {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
