package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "Manage and list installed plugins",
		Long: `Manage and list installed plugins.

Agent plugins load from the "agents/" directory next to the selected config
file (default: ~/.config/ntm/agents/*.toml; with XDG_CONFIG_HOME set:
$XDG_CONFIG_HOME/ntm/agents/; with an explicit --config: the config file's
sibling agents/ directory). Command plugins load from "commands/" in the same
location. Copy a preset there to install it. Oh My Pi (omp) is a built-in
agent type and needs no preset; built-in agent types always take precedence
over a plugin of the same name. NTM never overwrites files in agents/.`,
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
			out, err := robot.GetPlugins(robot.PluginsOptions{ConfigDir: configDir})
			if err != nil {
				return err
			}
			if !out.Success {
				cause := fmt.Errorf("%s", out.Error)
				if IsJSONOutput() {
					return emitJSONFailureEnvelopeWithCause(output.NewErrorWithCode(out.ErrorCode, out.Error), cause)
				}
				return cause
			}

			if IsJSONOutput() {
				return output.PrintJSON(out)
			}

			if len(out.AgentPlugins) == 0 && len(out.CommandPlugins) == 0 {
				fmt.Println("No plugins installed.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)

			if len(out.AgentPlugins) > 0 {
				fmt.Println("Agent Plugins:")
				fmt.Fprintln(w, "NAME\tALIAS\tDESCRIPTION")
				for _, p := range out.AgentPlugins {
					alias := p.Alias
					if alias == "" {
						alias = "-"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, alias, p.Description)
				}
				w.Flush()
				fmt.Println()
			}

			if len(out.CommandPlugins) > 0 {
				fmt.Println("Command Plugins:")
				fmt.Fprintln(w, "NAME\tUSAGE\tDESCRIPTION")
				for _, p := range out.CommandPlugins {
					fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, p.Usage, p.Description)
				}
				w.Flush()
				fmt.Println()
			}

			return nil
		},
	}
}
