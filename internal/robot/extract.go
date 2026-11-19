package robot

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	extractSchemaID      = "ntm:robot:extract:v1"
	extractSchemaVersion = "1.0.0"

	ExtractSchemaID      = extractSchemaID
	ExtractSchemaVersion = extractSchemaVersion
)

// ExtractOptions configures GetExtract for fenced code-block parsing from pane scrollback.
type ExtractOptions struct {
	Session     string
	Pane        string
	Language    string
	LastPane    bool
	Lines       int
	SelectBlock int
}

// ExtractOutput is the canonical robot envelope for --robot-extract and GET /api/v1/sessions/{id}/extract.
type ExtractOutput struct {
	RobotResponse
	SchemaID      string                `json:"schema_id"`
	SchemaVersion string                `json:"schema_version"`
	Session       string                `json:"session"`
	Source        string                `json:"source"`
	Language      string                `json:"language,omitempty"`
	Blocks        []codeblock.CodeBlock `json:"blocks"`
	TotalFound    int                   `json:"total_found"`
	PanesScanned  int                   `json:"panes_scanned"`
}

// GetExtract parses fenced Markdown code blocks and file-path hints from session pane scrollback.
func GetExtract(opts ExtractOptions) (*ExtractOutput, error) {
	sessionName := strings.TrimSpace(opts.Session)
	paneSpec := strings.TrimSpace(opts.Pane)
	lang := strings.TrimSpace(opts.Language)

	source := sessionName
	if paneSpec != "" && sessionName != "" {
		source = fmt.Sprintf("%s:%s", sessionName, paneSpec)
	}

	out := &ExtractOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      extractSchemaID,
		SchemaVersion: extractSchemaVersion,
		Session:       sessionName,
		Source:        source,
		Language:      lang,
		Blocks:        []codeblock.CodeBlock{},
	}

	if sessionName == "" {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide session name: ntm --robot-extract=myproject",
		)
		return out, nil
	}
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("invalid session name: %w", err),
			ErrCodeInvalidFlag,
			"Pass a valid tmux session name",
		)
		return out, nil
	}
	if !tmux.SessionExists(sessionName) {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' does not exist", sessionName),
			ErrCodeSessionNotFound,
			"Use ntm --robot-status to inspect active sessions",
		)
		return out, nil
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Verify tmux session health",
		)
		return out, nil
	}

	var targetPanes []tmux.Pane
	switch {
	case opts.LastPane:
		for _, p := range panes {
			if p.Active {
				targetPanes = []tmux.Pane{p}
				break
			}
		}
		if len(targetPanes) == 0 && len(panes) > 0 {
			targetPanes = []tmux.Pane{panes[len(panes)-1]}
		}
	case paneSpec != "":
		for _, p := range panes {
			paneIdxStr := fmt.Sprintf("%d", p.Index)
			if paneIdxStr == paneSpec || p.ID == paneSpec || p.Title == paneSpec || strings.Contains(p.Title, paneSpec) {
				targetPanes = []tmux.Pane{p}
				break
			}
		}
		if len(targetPanes) == 0 {
			out.RobotResponse = NewErrorResponse(
				fmt.Errorf("pane '%s' not found in session '%s'", paneSpec, sessionName),
				ErrCodePaneNotFound,
				"Pass a valid pane index, %pane_id, or pane title substring",
			)
			return out, nil
		}
	default:
		targetPanes = panes
	}

	lines := opts.Lines
	if lines <= 0 {
		lines = 500
	}

	parser := codeblock.NewParser()
	if lang != "" {
		parser = parser.WithLanguageFilter([]string{lang})
	}

	for _, pane := range targetPanes {
		captured, err := tmux.CapturePaneOutput(pane.ID, lines)
		if err != nil {
			continue
		}
		out.PanesScanned++
		blocks := parser.Parse(captured)
		for i := range blocks {
			blocks[i].SourcePane = fmt.Sprintf("%s:.%d", sessionName, pane.Index)
		}
		out.Blocks = append(out.Blocks, blocks...)
	}

	if opts.SelectBlock > 0 {
		if opts.SelectBlock > len(out.Blocks) {
			out.RobotResponse = NewErrorResponse(
				fmt.Errorf("block %d does not exist (found %d blocks)", opts.SelectBlock, len(out.Blocks)),
				"INVALID_SELECTION",
				"Adjust --extract-select or omit it to inspect all extracted blocks",
			)
			out.TotalFound = len(out.Blocks)
			return out, nil
		}
		out.Blocks = []codeblock.CodeBlock{out.Blocks[opts.SelectBlock-1]}
	}

	out.TotalFound = len(out.Blocks)
	return out, nil
}

// PrintExtract writes the terminal JSON/TOON response for --robot-extract.
func PrintExtract(opts ExtractOptions) error {
	out, err := GetExtract(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot extract failed")
}

func extractCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "extract",
		Flag:        "--robot-extract",
		Category:    "state",
		Summary:     "Extract fenced code blocks from pane output.",
		Description: "Parse fenced Markdown code blocks, language tags, and inferred target file paths from captured session pane scrollback.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--robot-extract", Type: "string", Required: true, Description: "Target tmux session name"},
			{Name: "extract-pane", Flag: "--extract-pane", Type: "string", Required: false, Description: "Filter to a specific pane index, %pane_id, or title substring"},
			{Name: "extract-lang", Flag: "--extract-lang", Type: "string", Required: false, Description: "Filter extracted blocks by language (e.g. go, python, typescript, bash)"},
			{Name: "extract-last", Flag: "--extract-last", Type: "bool", Required: false, Default: "false", Description: "Extract only from the currently active pane"},
			{Name: "extract-select", Flag: "--extract-select", Type: "int", Required: false, Default: "0", Description: "Select a single extracted block by 1-based index"},
			{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "500", Description: "Number of scrollback lines captured per pane"},
		},
		Examples: []string{
			"ntm --robot-extract=myproject",
			"ntm --robot-extract=myproject --extract-lang=go --lines=1000",
			"ntm --robot-extract=myproject --extract-pane=cc_1 --extract-select=1",
		},
	}
}
