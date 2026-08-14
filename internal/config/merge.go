package config

import (
	"fmt"
	"log"
	"log/slog"
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
	return loadMerged(cwd, globalPath, false)
}

// LoadMergedStrict loads global and project configuration like LoadMerged, but
// treats an invalid project overlay as a fatal error. Safety-sensitive callers
// use this variant when silently dropping project policy would fail open.
func LoadMergedStrict(cwd, globalPath string) (*Config, error) {
	return loadMerged(cwd, globalPath, true)
}

// LoadAssignmentPolicyStrict loads the global and authoritative project
// configuration used by automated assignment. An explicitly selected global
// path must already exist and resolve to a regular file; default-path callers
// may continue to use the built-in defaults when that file is absent.
func LoadAssignmentPolicyStrict(projectDir, globalPath string, requireGlobal bool) (*Config, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, fmt.Errorf("assignment safety policy requires an authoritative project directory")
	}
	globalPath = strings.TrimSpace(globalPath)
	if globalPath == "" {
		return nil, fmt.Errorf("assignment safety policy requires a global config path")
	}
	if requireGlobal {
		info, err := os.Stat(globalPath)
		if err != nil {
			return nil, fmt.Errorf("load explicitly selected config %s: %w", globalPath, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("explicitly selected config %s is not a regular file", globalPath)
		}
	}
	return LoadMergedStrict(projectDir, globalPath)
}

func loadMerged(cwd, globalPath string, strictProject bool) (*Config, error) {
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
		projectConfigPath := "project .ntm/config.toml"
		if projectDir != "" {
			projectConfigPath = filepath.Join(projectDir, ".ntm", "config.toml")
		}
		if strictProject {
			return nil, fmt.Errorf("loading project config %s: %w", projectConfigPath, err)
		}
		// The project overlay is invalid. Keep the global config that loaded
		// fine and skip only the bad overlay, warning loudly on stderr so the
		// user sees the real cause (offending file + parse error) instead of
		// silently reverting to built-in defaults.
		fmt.Fprintf(log.Writer(),
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

	// Project operator gates extend the global list. Treating the overlay as an
	// override would let a repository erase the user's safety gates, while an
	// absent or explicitly empty project list should leave global policy intact.
	if len(project.Assign.OperatorGatedLabels) > 0 {
		global.Assign.OperatorGatedLabels = mergeOperatorGatedLabels(
			project.Assign.OperatorGatedLabels,
			global.Assign.OperatorGatedLabels,
		)
	}

	// Project-scoped integration toggles are a one-way ratchet: a repository
	// overlay may DISABLE an integration for its own project, but it can never
	// ENABLE one the user turned off globally. Letting the overlay flip a
	// disabled integration on would allow repository content to opt the user
	// into network-facing behavior (Agent Mail registration, CASS, CM) without
	// any reapproval — the same reasoning that keeps operator gates
	// additive-only above and the Agents command merge disabled entirely.
	global.AgentMail.Enabled = ratchetIntegrationToggle(
		"agent_mail", global.AgentMail.Enabled, project.Integrations.AgentMail)
	global.CASS.Enabled = ratchetIntegrationToggle(
		"cass", global.CASS.Enabled, project.Integrations.CASS)
	global.Memory.Enabled = ratchetIntegrationToggle(
		"cm", global.Memory.Enabled, project.Integrations.CM)

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

// ratchetIntegrationToggle applies a project integration toggle to the global
// value with enable-suppression: false always wins, true only holds when the
// user already enabled the integration globally. An ignored enable attempt is
// logged to stderr so the operator can see why the project setting had no
// effect (and can enable it globally if that is what they actually want).
func ratchetIntegrationToggle(name string, global bool, project *bool) bool {
	if project == nil {
		return global
	}
	if !*project {
		return false
	}
	if !global {
		// Debug, not warning: the default project template enables every
		// integration, so a user with a global disable would otherwise see
		// this on every command in every default-templated project.
		slog.Debug("project config cannot enable a globally disabled integration; ignoring",
			"integration", name)
	}
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

func mergeOperatorGatedLabels(primary, secondary []string) []string {
	if len(primary) == 0 && len(secondary) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(primary)+len(secondary))
	out := make([]string, 0, len(primary)+len(secondary))
	for _, values := range [][]string{primary, secondary} {
		for _, value := range values {
			value = strings.TrimSpace(value)
			normalized := strings.ToLower(value)
			if normalized == "" || seen[normalized] {
				continue
			}
			seen[normalized] = true
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
