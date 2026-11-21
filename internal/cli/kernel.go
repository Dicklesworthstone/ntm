package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/serve"
)

func registerKernelListKernel() {
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

// decodeKernelInput decodes a typed struct, pointer, or map[string]any payload
// passed to kernel.Run into T using JSON field tags.
func decodeKernelInput[T any](name string, input any) (T, error) {
	var opts T
	if input == nil {
		return opts, nil
	}
	switch value := input.(type) {
	case T:
		return value, nil
	case *T:
		if value != nil {
			return *value, nil
		}
		return opts, nil
	case map[string]any:
		raw, err := json.Marshal(value)
		if err != nil {
			return opts, fmt.Errorf("marshal %s input: %w", name, err)
		}
		if err := json.Unmarshal(raw, &opts); err != nil {
			return opts, fmt.Errorf("decode %s input: %w", name, err)
		}
		return opts, nil
	default:
		return opts, fmt.Errorf("unsupported %s input type %T", name, input)
	}
}

func newKernelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kernel",
		Short: "Inspect and execute commands in the command kernel registry",
		Long: `Inspect and execute commands in the command kernel registry used to drive CLI, TUI, and REST surfaces.

Commands with a registered in-process handler report executable=true (catalog_only=false)
and can be invoked directly with 'ntm kernel run'. Synthesized REST/robot metadata entries
report executable=false (catalog_only=true).

Examples:
  ntm kernel list                               # List all kernel commands with execution status
  ntm kernel list --executable --json           # List only executable commands as JSON
  ntm kernel run core.version                   # Execute a registered kernel handler
  ntm kernel run prompt.preflight --input '{"prompt":"hello"}'`,
	}

	cmd.AddCommand(newKernelListCmd(), newKernelRunCmd())
	return cmd
}

func newKernelListCmd() *cobra.Command {
	var executableOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered kernel commands",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKernelList(executableOnly)
		},
	}
	cmd.Flags().BoolVar(&executableOnly, "executable", false, "Only show commands with registered in-process handlers")
	return cmd
}

func newKernelRunCmd() *cobra.Command {
	var rawInput string
	cmd := &cobra.Command{
		Use:   "run <command-name>",
		Short: "Execute a registered executable kernel command handler",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKernelCommand(cmd.Context(), args[0], rawInput)
		},
	}
	cmd.Flags().StringVar(&rawInput, "input", "", "Optional JSON object input for the kernel handler")
	return cmd
}

func runKernelCommand(ctx context.Context, name, rawInput string) error {
	serve.EnsureKernelCatalogRegistered()
	name = strings.TrimSpace(name)
	meta, ok := kernel.Get(name)
	if !ok {
		return fmt.Errorf("kernel command %q not found; run 'ntm kernel list' to inspect available commands", name)
	}
	if meta.CatalogOnly || !meta.Executable {
		if meta.REST != nil && meta.REST.Path != "" {
			return fmt.Errorf("kernel command %q is catalog-only (no in-process handler registered); invoke via %s %s or the corresponding CLI flag", name, meta.REST.Method, meta.REST.Path)
		}
		return fmt.Errorf("kernel command %q is catalog-only (no in-process handler registered)", name)
	}

	var input any
	if trimmed := strings.TrimSpace(rawInput); trimmed != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return fmt.Errorf("parse --input JSON: %w", err)
		}
		input = parsed
	}

	if ctx == nil {
		ctx = context.Background()
	}
	result, err := kernel.Run(ctx, name, input)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func runKernelList(executableOnly bool) error {
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

	if executableOnly {
		filtered := make([]kernel.Command, 0, len(payload.Commands))
		for _, c := range payload.Commands {
			if c.Executable {
				filtered = append(filtered, c)
			}
		}
		payload.Commands = filtered
		payload.Count = len(filtered)
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(payload)
	}

	if len(payload.Commands) == 0 {
		fmt.Println("No kernel commands registered.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCATEGORY\tMODE\tREST\tDESCRIPTION")
	for _, cmd := range payload.Commands {
		mode := "catalog-only"
		if cmd.Executable {
			mode = "executable"
		}
		rest := ""
		if cmd.REST != nil {
			rest = fmt.Sprintf("%s %s", cmd.REST.Method, cmd.REST.Path)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", cmd.Name, cmd.Category, mode, rest, cmd.Description)
	}
	return w.Flush()
}
