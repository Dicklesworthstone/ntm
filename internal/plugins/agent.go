package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// AgentPlugin defines a custom agent type loaded from config
type AgentPlugin struct {
	Name        string            `toml:"name"`
	Alias       string            `toml:"alias"`
	Command     string            `toml:"command"`
	Description string            `toml:"description"`
	Env         map[string]string `toml:"env"`
	Defaults    struct {
		Tags []string `toml:"tags"`
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
				return nil, fmt.Errorf("failed to parse %s: %w", path, err)
			}
			
			// Set defaults/validate
			if cfg.Agent.Name == "" {
				cfg.Agent.Name = strings.TrimSuffix(entry.Name(), ".toml")
			}
			
			plugins = append(plugins, cfg.Agent)
		}
	}

	return plugins, nil
}
