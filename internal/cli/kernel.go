package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
)

func init() {
	kernel.MustRegister(kernel.Command{
		Name:        "kernel.list",
		Description: "List registered kernel commands",
		Category:    "kernel",
		Output: &kernel.SchemaRef{
			Name: "KernelListResponse",
			Ref:  "kernel.ListResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/api/kernel/commands",
		},
		Examples: []kernel.Example{
			{
				Name:        "list",
				Description: "List all registered kernel commands",
				Command:     "ntm kernel list",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
}

// KernelListResult is the JSON output for kernel list.
type KernelListResult struct {
	Commands []kernel.Command `json:"commands"`
	Count    int              `json:"count"`
}

func newKernelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kernel",
		Short: "Inspect the command kernel registry",
		Long: `Inspect the command kernel registry used to drive CLI, TUI, and REST surfaces.

Examples:
  ntm kernel list          # List registered commands
  ntm kernel list --json   # JSON output`,
	}

	cmd.AddCommand(newKernelListCmd())
	return cmd
}

func newKernelListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered kernel commands",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKernelList()
		},
	}

	return cmd
}

func runKernelList() error {
	commands := kernel.List()
	result := KernelListResult{
		Commands: commands,
		Count:    len(commands),
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	if len(commands) == 0 {
		fmt.Println("No kernel commands registered.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCATEGORY\tREST\tDESCRIPTION")
	for _, cmd := range commands {
		rest := ""
		if cmd.REST != nil {
			rest = fmt.Sprintf("%s %s", cmd.REST.Method, cmd.REST.Path)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", cmd.Name, cmd.Category, rest, cmd.Description)
	}
	return w.Flush()
}
