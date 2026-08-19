package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/lint"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// PreflightFinding represents a single lint finding in preflight output.
type PreflightFinding struct {
	ID       string         `json:"id"`
	Severity string         `json:"severity"`
	Message  string         `json:"message"`
	Help     string         `json:"help"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Start    int            `json:"start,omitempty"`
	End      int            `json:"end,omitempty"`
	Line     int            `json:"line,omitempty"`
}

// PreflightResult is the JSON output for the preflight command.
type PreflightResult struct {
	robot.RobotResponse

	// Core results
	PreviewHash     string             `json:"preview_hash"`
	PreviewLen      int                `json:"preview_len"`
	EstimatedTokens int                `json:"estimated_tokens"`
	Findings        []PreflightFinding `json:"findings"`

	// Summary counts
	ErrorCount   int `json:"error_count"`
	WarningCount int `json:"warning_count"`
	InfoCount    int `json:"info_count"`

	// Prompt preview (truncated if long)
	Preview string `json:"preview,omitempty"`

	// DCG integration status
	DCGAvailable bool `json:"dcg_available"`
}

// PreflightInput is the input for the kernel command.
type PreflightInput struct {
	Prompt string `json:"prompt"`
	Strict bool   `json:"strict,omitempty"`
}

func init() {
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
			Path:   "/prompt/preflight",
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
	opts := PreflightInput{}
	switch v := input.(type) {
	case PreflightInput:
		opts = v
	case *PreflightInput:
		if v != nil {
			opts = *v
		}
	case map[string]any:
		if p, ok := v["prompt"].(string); ok {
			opts.Prompt = p
		}
		if s, ok := v["strict"].(bool); ok {
			opts.Strict = s
		}
	}

	if opts.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	return runPreflight(opts.Prompt, opts.Strict)
}

// runPreflight performs the preflight check on a prompt.
func runPreflight(prompt string, strict bool) (*PreflightResult, error) {
	outputPrompt := prompt

	// Set up lint rules
	var ruleSet *lint.RuleSet
	if strict {
		ruleSet = lint.StrictRuleSet()
	} else {
		ruleSet = lint.DefaultRuleSet()
	}

	var (
		linterOpts []lint.Option
	)
	linterOpts = append(linterOpts, lint.WithRuleSet(ruleSet))

	// Configure from app config if available
	if cfg != nil {
		redactCfg := cfg.Redaction.ToRedactionLibConfig()

		switch redactCfg.Mode {
		case redaction.ModeOff:
			// Redaction mode off explicitly disables scanning; keep lint in sync.
			ruleSet.Disable(lint.RuleSecretDetected)
		case redaction.ModeWarn, redaction.ModeRedact:
			// Warn + redact modes should not block sends on secrets.
			ruleSet.SetSeverity(lint.RuleSecretDetected, lint.SeverityWarning)
		case redaction.ModeBlock:
			ruleSet.SetSeverity(lint.RuleSecretDetected, lint.SeverityError)
		}

		// Avoid leaking raw secrets in preflight output when redaction is active.
		// In redact mode, this matches what will actually be sent.
		// In block mode, this matches the safe preview behavior used elsewhere (e.g. send.go).
		if redactCfg.Mode == redaction.ModeRedact || redactCfg.Mode == redaction.ModeBlock {
			previewCfg := redactCfg
			previewCfg.Mode = redaction.ModeRedact
			outputPrompt = redaction.ScanAndRedact(prompt, previewCfg).Output
		}

		if rule, ok := ruleSet.Rules[lint.RuleSecretDetected]; ok && rule.Enabled {
			linterOpts = append(linterOpts, lint.WithRedactionConfig(&redactCfg))
		}
	}

	l := lint.New(linterOpts...)
	return buildPreflightResult(l.Lint(prompt), outputPrompt)
}

// buildPreflightResult converts lint.Result to PreflightResult.
func buildPreflightResult(lintResult *lint.Result, prompt string) (*PreflightResult, error) {
	// Compute hash
	hash := sha256.Sum256([]byte(prompt))
	hashStr := hex.EncodeToString(hash[:8]) // First 8 bytes = 16 hex chars

	// Convert findings
	findings := make([]PreflightFinding, len(lintResult.Findings))
	var errCount, warnCount, infoCount int
	for i, f := range lintResult.Findings {
		findings[i] = PreflightFinding{
			ID:       string(f.ID),
			Severity: string(f.Severity),
			Message:  f.Message,
			Help:     f.Help,
			Metadata: f.Metadata,
			Start:    f.Start,
			End:      f.End,
			Line:     f.Line,
		}
		switch f.Severity {
		case lint.SeverityError:
			errCount++
		case lint.SeverityWarning:
			warnCount++
		case lint.SeverityInfo:
			infoCount++
		}
	}

	// Check DCG availability
	dcgAdapter := tools.NewDCGAdapter()
	dcgAvailable := dcgAdapter.IsAvailable(context.Background())

	// Build preview (truncate if too long)
	preview := prompt
	const maxPreview = 500
	if len(preview) > maxPreview {
		preview = preview[:maxPreview] + "..."
	}

	result := &PreflightResult{
		RobotResponse:   robot.NewRobotResponse(lintResult.Success),
		PreviewHash:     hashStr,
		PreviewLen:      len(prompt),
		EstimatedTokens: lintResult.Stats.TokenEstimate,
		Findings:        findings,
		ErrorCount:      errCount,
		WarningCount:    warnCount,
		InfoCount:       infoCount,
		Preview:         preview,
		DCGAvailable:    dcgAvailable,
	}

	// If not successful, add error info
	if !lintResult.Success {
		result.Error = "Prompt blocked due to lint errors"
		result.ErrorCode = "PREFLIGHT_BLOCKED"
	}

	return result, nil
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

			if prompt == "" {
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
