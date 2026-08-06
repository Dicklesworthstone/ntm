package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/serve"
)

func init() {
	kernel.MustRegister(kernel.Command{
		Name:        "openapi.generate",
		Description: "Generate OpenAPI 3.1 specification from kernel registry",
		Category:    "openapi",
		Input: &kernel.SchemaRef{
			Name: "OpenAPIGenerateInput",
			Ref:  "cli.OpenAPIGenerateInput",
		},
		Output: &kernel.SchemaRef{
			Name: "OpenAPIGenerateResponse",
			Ref:  "cli.OpenAPIGenerateResponse",
		},
		Examples: []kernel.Example{
			{
				Name:        "generate",
				Description: "Generate OpenAPI spec to docs/openapi.json",
				Command:     "ntm openapi generate",
			},
			{
				Name:        "generate-stdout",
				Description: "Print OpenAPI spec to stdout",
				Command:     "ntm openapi generate --stdout",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("openapi.generate", handleOpenAPIGenerate)
}

// OpenAPIGenerateInput holds input parameters for OpenAPI generation.
type OpenAPIGenerateInput struct {
	Output    string `json:"output"`
	Version   string `json:"version"`
	ServerURL string `json:"server_url"`
	Stdout    bool   `json:"stdout"`
}

// OpenAPIGenerateResponse holds the result of OpenAPI generation.
type OpenAPIGenerateResponse struct {
	Success    bool            `json:"success"`
	OutputPath string          `json:"output_path,omitempty"`
	Message    string          `json:"message,omitempty"`
	Spec       json.RawMessage `json:"spec,omitempty"`
}

func handleOpenAPIGenerate(ctx context.Context, input any) (any, error) {
	var params OpenAPIGenerateInput
	if input != nil {
		switch v := input.(type) {
		case OpenAPIGenerateInput:
			params = v
		case *OpenAPIGenerateInput:
			if v != nil {
				params = *v
			}
		case map[string]any:
			if o, ok := v["output"].(string); ok {
				params.Output = o
			}
			if ver, ok := v["version"].(string); ok {
				params.Version = ver
			}
			if url, ok := v["server_url"].(string); ok {
				params.ServerURL = url
			}
			if stdout, ok := v["stdout"].(bool); ok {
				params.Stdout = stdout
			}
		}
	}

	// Set defaults
	if params.Output == "" {
		params.Output = "docs/openapi.json"
	}
	if params.Version == "" {
		params.Version = "dev"
	}
	if params.ServerURL == "" {
		params.ServerURL = "http://localhost:8080"
	}

	spec := serve.GenerateOpenAPISpec(params.Version, params.ServerURL)

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}

	if params.Stdout {
		return OpenAPIGenerateResponse{
			Success: true,
			Message: "OpenAPI spec printed to stdout",
			Spec:    data,
		}, nil
	}

	if err := os.WriteFile(params.Output, append(data, '\n'), 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return OpenAPIGenerateResponse{
		Success:    true,
		OutputPath: params.Output,
		Message:    fmt.Sprintf("Wrote OpenAPI spec to %s", params.Output),
	}, nil
}

func newOpenAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "OpenAPI specification management",
		Long: `Manage OpenAPI specification generation from the kernel registry.

Examples:
  ntm openapi generate              # Generate docs/openapi.json
  ntm openapi generate --stdout     # Print to stdout
  ntm openapi generate -o api.json  # Custom output path`,
	}

	cmd.AddCommand(newOpenAPIGenerateCmd())
	return cmd
}

func newOpenAPIGenerateCmd() *cobra.Command {
	var (
		output    string
		version   string
		serverURL string
		stdout    bool
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate OpenAPI 3.1 specification",
		Args:  cobra.NoArgs,
		Long: `Generate an OpenAPI 3.1 specification from the kernel command registry.

The specification is generated dynamically from registered kernel commands,
including their REST bindings, input/output schemas, and examples.

Examples:
  ntm openapi generate                    # Write to docs/openapi.json
  ntm openapi generate --stdout           # Print to stdout
  ntm openapi generate -o api.json        # Custom output path
  ntm openapi generate --version 1.0.0    # Set API version`,
		RunE: func(cmd *cobra.Command, args []string) error {
			input := OpenAPIGenerateInput{
				Output:    output,
				Version:   version,
				ServerURL: serverURL,
				Stdout:    stdout,
			}

			result, err := kernel.Run(cmd.Context(), "openapi.generate", input)
			if err != nil {
				return err
			}
			response, ok := result.(OpenAPIGenerateResponse)
			if !ok {
				return fmt.Errorf("unexpected openapi.generate result type %T", result)
			}

			if stdout {
				if len(response.Spec) == 0 {
					return fmt.Errorf("openapi.generate returned no specification for --stdout")
				}
				if _, err := cmd.OutOrStdout().Write(response.Spec); err != nil {
					return fmt.Errorf("write OpenAPI spec to stdout: %w", err)
				}
				if _, err := cmd.OutOrStdout().Write([]byte("\n")); err != nil {
					return fmt.Errorf("terminate OpenAPI spec output: %w", err)
				}
				return nil
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(result)
			}

			fmt.Println(response.Message)

			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "docs/openapi.json", "Output file path")
	cmd.Flags().StringVar(&version, "version", "dev", "API version for the spec")
	cmd.Flags().StringVar(&serverURL, "server-url", "http://localhost:8080", "Server URL for the spec")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "Print to stdout instead of file")

	return cmd
}
