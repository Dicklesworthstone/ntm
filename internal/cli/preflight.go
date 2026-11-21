package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// PreflightFinding aliases the canonical ORI preflight finding struct in internal/robot.
type PreflightFinding = robot.PreflightFinding

// PreflightResult aliases the canonical ORI preflight output struct in internal/robot.
type PreflightResult = robot.PreflightOutput

// PreflightInput is the input for the kernel command.
type PreflightInput struct {
	Prompt string `json:"prompt"`
	Strict bool   `json:"strict,omitempty"`
}

func registerPreflightKernel() {
	// Register preflight kernel command
	kernel.MustRegister(kernel.Command{
		Name:        "prompt.preflight",
		Description: "Lint and validate a prompt before sending",
		Category:    "prompt",
		Input: &kernel.SchemaRef{
			Name: "PreflightInput",
			Ref:  "cli.PreflightInput",
		},
		Output: &kernel.SchemaRef{
			Name: "PreflightResult",
			Ref:  "cli.PreflightResult",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/api/v1/prompt/preflight",
		},
		Examples: []kernel.Example{
			{
				Name:        "preflight-basic",
				Description: "Check a prompt for issues",
				Command:     `ntm preflight "rm -rf /"`,
			},
			{
				Name:        "preflight-strict",
				Description: "Check with strict mode (all warnings become errors)",
				Command:     `ntm preflight --strict "my prompt"`,
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("prompt.preflight", handlePreflight)
}

func handlePreflight(ctx context.Context, input any) (any, error) {
	opts, err := decodeKernelInput[PreflightInput]("prompt.preflight", input)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(opts.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	return robot.GetPreflight(ctx, robot.PreflightOptions{
		Prompt: opts.Prompt,
		Strict: opts.Strict,
		Config: cfg,
	})
}

// runPreflight performs the preflight check on a prompt via the canonical robot engine.
func runPreflight(prompt string, strict bool) (*PreflightResult, error) {
	return robot.GetPreflight(context.Background(), robot.PreflightOptions{
		Prompt: prompt,
		Strict: strict,
		Config: cfg,
	})
}

func newPreflightCmd() *cobra.Command {
	var strict bool
	var showPreview bool

	cmd := &cobra.Command{
		Use:   "preflight <prompt>",
		Short: "Validate a prompt before sending",
		Long: `Check a prompt for potential issues before sending to agents.

Detects:
  - Oversized prompts (bytes and estimated tokens)
  - Secrets and API keys
  - Destructive commands (rm -rf, git reset --hard, etc.)
  - PII (emails, phone numbers, SSNs)
  - Missing context markers (optional, configurable)

Use --strict for high-security environments where all warnings become errors.

Examples:
  ntm preflight "fix the bug in auth.go"
  ntm preflight --strict "rm -rf /tmp/cache"
  ntm preflight --json "Deploy to production"
  echo "my prompt" | ntm preflight -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var prompt string

			// Get prompt from args or stdin
			if len(args) > 0 && args[0] != "-" {
				prompt = args[0]
			} else {
				// Read from stdin
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("failed to read from stdin: %w", err)
				}
				prompt = string(data)
			}

			if strings.TrimSpace(prompt) == "" {
				return fmt.Errorf("prompt is required")
			}

			effectiveStrict := strict
			if cfg != nil && !cmd.Flags().Changed("strict") {
				effectiveStrict = cfg.Preflight.Strict
			}

			result, err := runPreflight(prompt, effectiveStrict)
			if err != nil {
				return err
			}

			machineJSON := IsJSONOutput()
			if machineJSON {
				if !result.Success {
					return emitJSONFailureEnvelopeWithCause(result, preflightError{result: result})
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Human-readable output
			fmt.Printf("Preflight Check\n")
			fmt.Printf("===============\n\n")
			fmt.Printf("Preview Hash: %s\n", result.PreviewHash)
			fmt.Printf("Size: %d bytes, ~%d tokens\n", result.PreviewLen, result.EstimatedTokens)
			fmt.Printf("DCG Available: %v\n\n", result.DCGAvailable)

			if len(result.Findings) == 0 {
				fmt.Printf("✓ No issues found\n")
			} else {
				fmt.Printf("Findings: %d errors, %d warnings, %d info\n\n",
					result.ErrorCount, result.WarningCount, result.InfoCount)

				for _, f := range result.Findings {
					icon := "ℹ"
					switch f.Severity {
					case "error":
						icon = "✗"
					case "warning":
						icon = "⚠"
					}
					fmt.Printf("%s [%s] %s\n", icon, f.ID, f.Message)
					if f.Help != "" {
						fmt.Printf("  💡 %s\n", f.Help)
					}
				}
			}

			if showPreview && result.Preview != "" {
				fmt.Printf("\nPreview:\n%s\n", result.Preview)
			}

			if result.Success {
				fmt.Printf("\n✓ Preflight passed\n")
				return nil
			}

			fmt.Printf("\n✗ Preflight blocked - resolve errors before sending\n")
			return preflightError{result: result}
		},
	}

	cmd.Flags().BoolVar(&strict, "strict", false, "Use strict mode (all warnings become errors)")
	cmd.Flags().BoolVar(&showPreview, "preview", false, "Show prompt preview")

	return cmd
}

// preflightError is returned when preflight blocks a send.
type preflightError struct {
	result *PreflightResult
}

func (e preflightError) Error() string {
	return fmt.Sprintf("preflight blocked: %d errors found", e.result.ErrorCount)
}
