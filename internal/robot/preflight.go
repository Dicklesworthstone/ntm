package robot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/lint"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

const (
	preflightSchemaID      = "ntm:robot:preflight:v1"
	preflightSchemaVersion = "1.0.0"
	maxPreflightPreviewLen = 500
)

// PreflightOptions configures ORI prompt preflight validation.
type PreflightOptions struct {
	Prompt string
	Strict bool
	Config *config.Config
}

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

// PreflightOutput represents the structured JSON envelope for --robot-preflight and prompt.preflight.
type PreflightOutput struct {
	RobotResponse
	SchemaID        string             `json:"schema_id"`
	SchemaVersion   string             `json:"schema_version"`
	PreviewHash     string             `json:"preview_hash"`
	PreviewLen      int                `json:"preview_len"`
	EstimatedTokens int                `json:"estimated_tokens"`
	Findings        []PreflightFinding `json:"findings"`
	ErrorCount      int                `json:"error_count"`
	WarningCount    int                `json:"warning_count"`
	InfoCount       int                `json:"info_count"`
	Preview         string             `json:"preview,omitempty"`
	DCGAvailable    bool               `json:"dcg_available"`
	Strict          bool               `json:"strict"`
	AgentHints      *AgentHints        `json:"_agent_hints,omitempty"`
}

func newPreflightErrorOutput(strict bool, err error, code, hint string) *PreflightOutput {
	return &PreflightOutput{
		RobotResponse: NewErrorResponse(err, code, hint),
		SchemaID:      preflightSchemaID,
		SchemaVersion: preflightSchemaVersion,
		Findings:      []PreflightFinding{},
		Strict:        strict,
	}
}

// GetPreflight validates and lints a prompt before sending, applying configured
// redaction policy, strict-mode escalation, preview truncation, and DCG availability.
func GetPreflight(ctx context.Context, opts PreflightOptions) (*PreflightOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newPreflightErrorOutput(opts.Strict, err, ErrCodeTimeout, "Retry --robot-preflight with an active context"), nil
	}
	if strings.TrimSpace(opts.Prompt) == "" {
		return newPreflightErrorOutput(
			opts.Strict,
			errors.New("prompt is required"),
			ErrCodeInvalidFlag,
			"Pass --robot-preflight='your prompt' or --robot-preflight --preflight-file=path/to/prompt.txt",
		), nil
	}

	outputPrompt := opts.Prompt

	var ruleSet *lint.RuleSet
	if opts.Strict {
		ruleSet = lint.StrictRuleSet()
	} else {
		ruleSet = lint.DefaultRuleSet()
	}

	linterOpts := []lint.Option{lint.WithRuleSet(ruleSet)}

	if opts.Config != nil {
		redactCfg := opts.Config.Redaction.ToRedactionLibConfig()

		switch redactCfg.Mode {
		case redaction.ModeOff:
			ruleSet.Disable(lint.RuleSecretDetected)
		case redaction.ModeWarn, redaction.ModeRedact:
			ruleSet.SetSeverity(lint.RuleSecretDetected, lint.SeverityWarning)
		case redaction.ModeBlock:
			ruleSet.SetSeverity(lint.RuleSecretDetected, lint.SeverityError)
		}

		if redactCfg.Mode == redaction.ModeRedact || redactCfg.Mode == redaction.ModeBlock {
			previewCfg := redactCfg
			previewCfg.Mode = redaction.ModeRedact
			outputPrompt = redaction.ScanAndRedact(opts.Prompt, previewCfg).Output
		}

		if rule, ok := ruleSet.Rules[lint.RuleSecretDetected]; ok && rule.Enabled {
			linterOpts = append(linterOpts, lint.WithRedactionConfig(&redactCfg))
		}
	}

	l := lint.New(linterOpts...)
	return buildPreflightOutput(ctx, l.Lint(opts.Prompt), outputPrompt, opts.Strict), nil
}

func buildPreflightOutput(ctx context.Context, lintResult *lint.Result, prompt string, strict bool) *PreflightOutput {
	hash := sha256.Sum256([]byte(prompt))
	hashStr := hex.EncodeToString(hash[:8])

	findings := make([]PreflightFinding, len(lintResult.Findings))
	var errCount, warnCount, infoCount int
	var hintWarnings []string
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
			hintWarnings = append(hintWarnings, fmt.Sprintf("[%s] %s", f.ID, f.Message))
		case lint.SeverityInfo:
			infoCount++
		}
	}

	dcgAdapter := tools.NewDCGAdapter()
	dcgAvailable := dcgAdapter.IsAvailable(ctx)

	preview := prompt
	if len(preview) > maxPreflightPreviewLen {
		preview = preview[:maxPreflightPreviewLen] + "..."
	}

	out := &PreflightOutput{
		RobotResponse:   NewRobotResponse(lintResult.Success),
		SchemaID:        preflightSchemaID,
		SchemaVersion:   preflightSchemaVersion,
		PreviewHash:     hashStr,
		PreviewLen:      len(prompt),
		EstimatedTokens: lintResult.Stats.TokenEstimate,
		Findings:        findings,
		ErrorCount:      errCount,
		WarningCount:    warnCount,
		InfoCount:       infoCount,
		Preview:         preview,
		DCGAvailable:    dcgAvailable,
		Strict:          strict,
	}

	summary := fmt.Sprintf("Preflight passed (%d bytes, ~%d tokens, %d warnings)", out.PreviewLen, out.EstimatedTokens, warnCount)
	var actions []RobotAction
	if !lintResult.Success {
		out.Error = "Prompt blocked due to lint errors"
		out.ErrorCode = "PREFLIGHT_BLOCKED"
		out.Hint = "Resolve blocking findings (secrets, oversized prompt, or strict-mode warnings) before sending"
		summary = fmt.Sprintf("Preflight blocked (%d errors, %d warnings)", errCount, warnCount)
		actions = append(actions, RobotAction{
			Action:   "remediate_prompt",
			Target:   "prompt",
			Reason:   fmt.Sprintf("%d blocking finding(s) detected", errCount),
			Priority: 1,
			Details:  "Inspect findings[].help and remove secrets or destructive commands before calling --robot-send",
		})
	} else if warnCount > 0 {
		actions = append(actions, RobotAction{
			Action:   "review_warnings",
			Target:   "prompt",
			Reason:   fmt.Sprintf("%d non-blocking warning(s) detected", warnCount),
			Priority: 2,
			Details:  "Pass --preflight-strict if warnings should block delivery",
		})
	}

	out.AgentHints = &AgentHints{
		Summary:          summary,
		Warnings:         hintWarnings,
		SuggestedActions: actions,
	}

	return out
}

// PrintPreflight executes GetPreflight and writes the terminal JSON/TOON response.
func PrintPreflight(ctx context.Context, opts PreflightOptions) error {
	out, err := GetPreflight(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot preflight failed")
}
