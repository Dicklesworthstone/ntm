package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/plugins"
)

func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "Manage and list installed plugins",
	}

	cmd.AddCommand(newPluginsListCmd())
	return cmd
}

func newPluginsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed agent and command plugins",
		RunE: func(cmd *cobra.Command, args []string) error {
			configDir := filepath.Dir(selectedConfigPath())

			// Load Agent Plugins
			agentsDir := filepath.Join(configDir, "agents")
			agentPlugins, err := plugins.LoadAgentPlugins(agentsDir)
			if err != nil {
				return fmt.Errorf("load agent plugins from %s: %w", agentsDir, err)
			}

			// Load Command Plugins
			cmdDir := filepath.Join(configDir, "commands")
			commandPlugins, err := plugins.LoadCommandPlugins(cmdDir)
			if err != nil {
				return fmt.Errorf("load command plugins from %s: %w", cmdDir, err)
			}

			if len(agentPlugins) == 0 && len(commandPlugins) == 0 {
				fmt.Println("No plugins installed.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)

			if len(agentPlugins) > 0 {
				fmt.Println("Agent Plugins:")
				fmt.Fprintln(w, "NAME\tALIAS\tDESCRIPTION")
				for _, p := range agentPlugins {
					alias := p.Alias
					if alias == "" {
						alias = "-"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, alias, p.Description)
				}
				w.Flush()
				fmt.Println()
			}

			if len(commandPlugins) > 0 {
				fmt.Println("Command Plugins:")
				fmt.Fprintln(w, "NAME\tUSAGE\tDESCRIPTION")
				for _, p := range commandPlugins {
					fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, p.Usage, p.Description)
				}
				w.Flush()
				fmt.Println()
			}

			return nil
		},
	}
}
