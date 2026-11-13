package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/serve"
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
			Path:   "/api/v1/kernel/commands",
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
	kernel.MustRegisterHandler("kernel.list", func(ctx context.Context, _ any) (any, error) {
		serve.EnsureKernelCatalogRegistered()
		commands := kernel.List()
		return kernel.ListResponse{
			Commands: commands,
			Count:    len(commands),
		}, nil
	})
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
	result, err := kernel.Run(context.Background(), "kernel.list", nil)
	if err != nil {
		return err
	}

	var payload kernel.ListResponse
	switch value := result.(type) {
	case kernel.ListResponse:
		payload = value
	case *kernel.ListResponse:
		if value != nil {
			payload = *value
		}
	default:
		return fmt.Errorf("kernel.list returned unexpected type %T", result)
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload)
	}

	if len(payload.Commands) == 0 {
		fmt.Println("No kernel commands registered.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCATEGORY\tREST\tDESCRIPTION")
	for _, cmd := range payload.Commands {
		rest := ""
		if cmd.REST != nil {
			rest = fmt.Sprintf("%s %s", cmd.REST.Method, cmd.REST.Path)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", cmd.Name, cmd.Category, rest, cmd.Description)
	}
	return w.Flush()
}
