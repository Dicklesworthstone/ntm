package robot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	redactSchemaID      = "ntm:robot:redact:v1"
	redactSchemaVersion = "1.0.0"

	RedactSchemaID      = redactSchemaID
	RedactSchemaVersion = redactSchemaVersion
)

// RedactPreviewFinding describes a single secret/PII match without exposing raw secret bytes.
type RedactPreviewFinding struct {
	Category redaction.Category `json:"category"`
	Redacted string             `json:"redacted"`
	Start    int                `json:"start"`
	End      int                `json:"end"`
	Line     int                `json:"line,omitempty"`
	Column   int                `json:"column,omitempty"`
}

// RedactOptions configures GetRedactPreview.
type RedactOptions struct {
	Text      string
	File      string
	RedactCfg *redaction.Config
}

// RedactOutput is the canonical robot envelope for --robot-redact and POST /api/v1/redact/preview.
type RedactOutput struct {
	RobotResponse
	SchemaID      string                 `json:"schema_id"`
	SchemaVersion string                 `json:"schema_version"`
	Source        string                 `json:"source,omitempty"`
	Path          string                 `json:"path,omitempty"`
	InputLen      int                    `json:"input_len"`
	FindingCount  int                    `json:"finding_count"`
	Findings      []RedactPreviewFinding `json:"findings"`
	Output        string                 `json:"output"`
	AgentHints    *AgentHints            `json:"_agent_hints,omitempty"`
}

// GetRedactPreview scans input text or a file and returns safe-redacted output and line-annotated findings.
// Raw matched secrets are never returned, even when user configuration sets redaction mode to off or warn.
func GetRedactPreview(opts RedactOptions) (*RedactOutput, error) {
	hasText := strings.Compare(opts.Text, "") != 0
	trimmedFile := strings.TrimSpace(opts.File)
	hasFile := strings.Compare(trimmedFile, "") != 0

	if !hasText && !hasFile {
		return &RedactOutput{
			RobotResponse: NewErrorResponse(
				errors.New("must provide either inline text or --redact-file"),
				ErrCodeInvalidFlag,
				"Pass --robot-redact='text to scan' or --robot-redact --redact-file=path/to/file",
			),
			SchemaID:      redactSchemaID,
			SchemaVersion: redactSchemaVersion,
			Findings:      []RedactPreviewFinding{},
		}, nil
	}
	if hasText && hasFile {
		return &RedactOutput{
			RobotResponse: NewErrorResponse(
				errors.New("cannot specify both inline text and --redact-file"),
				ErrCodeInvalidFlag,
				"Pass either inline text or --redact-file, not both",
			),
			SchemaID:      redactSchemaID,
			SchemaVersion: redactSchemaVersion,
			Findings:      []RedactPreviewFinding{},
		}, nil
	}

	source := "text"
	absPath := ""
	input := opts.Text
	if hasFile {
		source = "file"
		expanded := util.ExpandPath(trimmedFile)
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return &RedactOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("resolve --redact-file %q: %w", trimmedFile, err),
					ErrCodeInvalidFlag,
					"Provide a valid readable file path",
				),
				SchemaID:      redactSchemaID,
				SchemaVersion: redactSchemaVersion,
				Findings:      []RedactPreviewFinding{},
			}, nil
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return &RedactOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("read %q: %w", abs, err),
					ErrCodeInvalidFlag,
					"Ensure --redact-file points to an existing readable file",
				),
				SchemaID:      redactSchemaID,
				SchemaVersion: redactSchemaVersion,
				Findings:      []RedactPreviewFinding{},
			}, nil
		}
		absPath = abs
		input = string(raw)
	}

	var libCfg redaction.Config
	if opts.RedactCfg != nil {
		libCfg = *opts.RedactCfg
	} else {
		libCfg = config.Default().Redaction.ToRedactionLibConfig()
	}
	// Always force ModeRedact in preview so raw secret matches never leak in responses.
	libCfg.Mode = redaction.ModeRedact

	res := redaction.ScanAndRedact(input, libCfg)
	redaction.AddLineInfo(input, res.Findings)

	findings := make([]RedactPreviewFinding, 0, len(res.Findings))
	for _, f := range res.Findings {
		findings = append(findings, RedactPreviewFinding{
			Category: f.Category,
			Redacted: f.Redacted,
			Start:    f.Start,
			End:      f.End,
			Line:     f.Line,
			Column:   f.Column,
		})
	}

	summary := "No secrets or sensitive tokens detected."
	if len(findings) > 0 {
		summary = fmt.Sprintf("Redacted %d sensitive finding(s) across %d bytes of input.", len(findings), len(input))
	}

	return &RedactOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      redactSchemaID,
		SchemaVersion: redactSchemaVersion,
		Source:        source,
		Path:          absPath,
		InputLen:      len(input),
		FindingCount:  len(findings),
		Findings:      findings,
		Output:        res.Output,
		AgentHints: &AgentHints{
			Summary: summary,
		},
	}, nil
}

// PrintRedactPreview writes the terminal JSON/TOON response for --robot-redact.
func PrintRedactPreview(opts RedactOptions) error {
	out, err := GetRedactPreview(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot redact failed")
}

func redactCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "redact",
		Flag:        "--robot-redact",
		Category:    "utility",
		Summary:     "Preview secret and PII redaction.",
		Description: "Scan text or a file for secrets/PII and return safe-redacted output with line/column finding metadata without ever emitting raw matched secrets.",
		Parameters: []RobotParameter{
			{Name: "text", Flag: "--robot-redact", Type: "string", Required: false, Description: "Inline text to scan and redact (or omit value and pass --redact-file)"},
			{Name: "redact-file", Flag: "--redact-file", Type: "string", Required: false, Description: "File path to scan and redact (mutually exclusive with inline --robot-redact=<text>)"},
		},
		Examples: []string{
			"ntm --robot-redact='Authorization: Bearer sk-live-1234567890abcdef1234567890'",
			"ntm --robot-redact --redact-file=./notes.txt",
		},
	}
}
