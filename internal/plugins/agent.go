package plugins

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// pluginNameRegex enforces allowed characters for plugin names (must match tmux pane regex)
var pluginNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// AgentPlugin defines a custom agent type loaded from config
type AgentPlugin struct {
	Name        string            `toml:"name"`
	Alias       string            `toml:"alias"`
	Command     string            `toml:"command"`
	Description string            `toml:"description"`
	Env         map[string]string `toml:"env"`
	Defaults    struct {
		// Model is the default model an agent of this plugin type spawns with
		// when the invocation omits an explicit model (e.g. bare `--hermes=1`).
		// Consumed by the CLI's model resolution as the lowest-precedence
		// fallback, below explicit specs and global config defaults.
		Model string   `toml:"model"`
		Tags  []string `toml:"tags"`
	} `toml:"defaults"`
	// Readiness lets a plugin declare how NTM should classify its panes, so
	// status, --robot-tail, --verify-boot, and safe dispatch treat it like a
	// built-in agent (ntm#260). Each entry is a Go regexp matched against
	// ANSI-stripped pane output:
	//   idle_patterns    — a match in the last few lines, with no working
	//                      match in the live tail, means the composer is
	//                      waiting for input;
	//   working_patterns — a match in the live tail means a turn is in
	//                      flight (vetoes idle), e.g. OMP's `⟨esc⟩` hint;
	//   error_patterns   — a match in the recent output marks an error.
	// With none declared the generic prompt heuristics apply as before.
	Readiness struct {
		IdlePatterns    []string `toml:"idle_patterns"`
		WorkingPatterns []string `toml:"working_patterns"`
		ErrorPatterns   []string `toml:"error_patterns"`
	} `toml:"readiness"`
}

// ProbeCommand returns the executable a plugin launches — the first token of
// its command template, before any argument or template action — so `ntm
// deps` can probe PATH for it. Empty when the template starts with a
// template action (the executable itself is dynamic).
func (p AgentPlugin) ProbeCommand() string {
	cmd := strings.TrimSpace(p.Command)
	if cmd == "" || strings.HasPrefix(cmd, "{{") {
		return ""
	}
	if idx := strings.Index(cmd, "{{"); idx >= 0 {
		cmd = cmd[:idx]
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	// Skip leading VAR=value assignments.
	for _, f := range fields {
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "=") {
			continue
		}
		return f
	}
	return ""
}

type agentConfigFile struct {
	Agent AgentPlugin `toml:"agent"`
}

// LoadAgentPlugins scans the given directory for .toml files and loads them.
func LoadAgentPlugins(dir string) ([]AgentPlugin, error) {
	var plugins []AgentPlugin

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".toml") {
			path := filepath.Join(dir, entry.Name())
			var cfg agentConfigFile
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				log.Printf("plugins: failed to parse plugin %s: %v", entry.Name(), err)
				continue
			}

			// Set defaults/validate
			if cfg.Agent.Name == "" {
				cfg.Agent.Name = strings.TrimSuffix(entry.Name(), ".toml")
			}

			if !pluginNameRegex.MatchString(cfg.Agent.Name) {
				log.Printf("plugins: plugin %s has invalid name %q (allowed: a-z, 0-9, _, -), skipping", entry.Name(), cfg.Agent.Name)
				continue
			}

			if cfg.Agent.Command == "" {
				log.Printf("plugins: plugin %s missing 'command' field", cfg.Agent.Name)
				continue
			}

			plugins = append(plugins, cfg.Agent)
		}
	}

	return plugins, nil
}
