package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// LoadMerged loads the global config and merges any project-specific config found starting from cwd.
//
// If the project overlay (.ntm/config.toml) fails to parse/validate, the global
// config that loaded successfully is preserved and the bad overlay is skipped
// after printing a clear stderr warning that names the offending file and the
// parse error. This avoids silently reverting every global setting to built-in
// defaults just because a project overlay has a typo or stale schema (issue #162).
//
// A genuinely broken global config still returns an error so the caller can
// surface the real cause.
func LoadMerged(cwd, globalPath string) (*Config, error) {
	// Load global
	cfg, err := loadWithCWD(globalPath, cwd)
	if err != nil {
		return nil, fmt.Errorf("loading global config: %w", err)
	}

	// Find project config
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	projectDir, projectCfg, err := FindProjectConfig(cwd)
	if err != nil {
		// The project overlay is invalid. Keep the global config that loaded
		// fine and skip only the bad overlay, warning loudly on stderr so the
		// user sees the real cause (offending file + parse error) instead of
		// silently reverting to built-in defaults.
		projectConfigPath := "project .ntm/config.toml"
		if projectDir != "" {
			projectConfigPath = filepath.Join(projectDir, ".ntm", "config.toml")
		}
		fmt.Fprintf(os.Stderr,
			"ntm: warning: ignoring invalid project config %s: %v (continuing with global config)\n",
			projectConfigPath, err)
		return cfg, nil
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

	// Merge project-scoped integration toggles that have direct runtime equivalents.
	if project.Integrations.AgentMail != nil {
		global.AgentMail.Enabled = *project.Integrations.AgentMail
	}
	if project.Integrations.AgentMailProjectKey != "" {
		if global.AgentMail.ProjectKeys == nil {
			global.AgentMail.ProjectKeys = make(map[string]string)
		}
		global.AgentMail.ProjectKeys[projectDir] = project.Integrations.AgentMailProjectKey
	}
	if project.Integrations.CASS != nil {
		global.CASS.Enabled = *project.Integrations.CASS
	}
	if project.Integrations.CM != nil {
		global.Memory.Enabled = *project.Integrations.CM
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
		palettePath, err := ResolveProjectPalettePath(projectDir, project)
		if err != nil {
			// Don't error, just ignore unsafe path. Log to stderr so robot/json
			// stdout streams remain machine-readable.
			log.Printf("warning: ignoring unsafe project palette path: %s", project.Palette.File)
		} else {
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
