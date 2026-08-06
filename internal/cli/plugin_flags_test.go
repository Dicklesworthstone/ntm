package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/plugins"
)

// TestRegisterPluginAgentFlagsSkipsBuiltinCollision guards against the #200
// regression: an agent plugin whose name (or alias) collides with a built-in
// agent flag must not panic the command-tree build with pflag's
// "flag redefined: <name>". The most common trigger is a leftover "oc"
// (Opencode) plugin from before Opencode became a first-class agent type.
func TestRegisterPluginAgentFlagsSkipsBuiltinCollision(t *testing.T) {
	var specs AgentSpecs
	cmd := &cobra.Command{Use: "spawn"}
	// Built-in --oc, mirroring the static first-class Opencode registration.
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeOpencode, &specs), "oc", "Opencode agents (N or N:model)")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registerPluginAgentFlags panicked on built-in collision: %v", r)
		}
	}()

	// A leftover user plugin that also wants --oc (name) plus a free --opencode
	// (alias). The colliding --oc previously panicked the entire binary.
	registerPluginAgentFlags(cmd, plugins.AgentPlugin{
		Name:        "oc",
		Alias:       "opencode",
		Description: "user opencode plugin",
		Command:     "opencode",
	}, &specs)

	// Built-in --oc must survive unchanged.
	if cmd.Flags().Lookup("oc") == nil {
		t.Fatal("built-in --oc flag was lost")
	}
	// The non-colliding alias --opencode should still be registered.
	if cmd.Flags().Lookup("opencode") == nil {
		t.Fatal("non-colliding plugin alias --opencode should have been registered")
	}
}

// TestRegisterPluginAgentFlagsRegistersNonColliding verifies a normal plugin
// still gets both its name and alias flags.
func TestRegisterPluginAgentFlagsRegistersNonColliding(t *testing.T) {
	var specs AgentSpecs
	cmd := &cobra.Command{Use: "spawn"}
	registerPluginAgentFlags(cmd, plugins.AgentPlugin{
		Name:        "myagent",
		Alias:       "ma",
		Description: "custom agent",
		Command:     "myagent",
	}, &specs)

	if cmd.Flags().Lookup("myagent") == nil {
		t.Fatal("plugin flag --myagent should be registered")
	}
	if cmd.Flags().Lookup("ma") == nil {
		t.Fatal("plugin alias --ma should be registered")
	}
}

func TestPluginsListReportsPluginDirectoryReadErrors(t *testing.T) {
	originalCfgFile := cfgFile
	t.Cleanup(func() {
		cfgFile = originalCfgFile
	})

	tests := []struct {
		name        string
		directory   string
		errorPrefix string
	}{
		{
			name:        "agent plugins",
			directory:   "agents",
			errorPrefix: "load agent plugins",
		},
		{
			name:        "command plugins",
			directory:   "commands",
			errorPrefix: "load command plugins",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			cfgFile = filepath.Join(configDir, "config.toml")
			if err := os.WriteFile(filepath.Join(configDir, tt.directory), []byte("not a directory"), 0o600); err != nil {
				t.Fatalf("write invalid plugin directory: %v", err)
			}

			err := newPluginsListCmd().RunE(nil, nil)
			if err == nil {
				t.Fatal("newPluginsListCmd() error = nil, want plugin directory read error")
			}
			if !strings.Contains(err.Error(), tt.errorPrefix) {
				t.Fatalf("newPluginsListCmd() error = %q, want prefix %q", err, tt.errorPrefix)
			}
		})
	}
}
