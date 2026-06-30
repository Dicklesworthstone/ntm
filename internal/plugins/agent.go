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
		Model string   `toml:"model"`
		Tags  []string `toml:"tags"`
	} `toml:"defaults"`
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
