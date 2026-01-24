package ensemble

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
)

const defaultCaptureLines = 1000

// CapturedOutput holds raw and parsed output for an ensemble assignment.
type CapturedOutput struct {
	ModeID        string
	PaneName      string
	RawOutput     string
	Parsed        *ModeOutput
	ParseErrors   []error
	CapturedAt    time.Time
	LineCount     int
	TokenEstimate int
}

// OutputCapture captures and parses ensemble agent output.
type OutputCapture struct {
	tmuxClient *tmux.Client
	maxLines   int
	validator  *SchemaValidator
}

// NewOutputCapture creates a new OutputCapture with defaults.
func NewOutputCapture(client *tmux.Client) *OutputCapture {
	if client == nil {
		client = tmux.DefaultClient
	}
	return &OutputCapture{
		tmuxClient: client,
		maxLines:   defaultCaptureLines,
		validator:  NewSchemaValidator(),
	}
}

// SetMaxLines configures how many lines to capture per pane.
func (c *OutputCapture) SetMaxLines(lines int) {
	if lines > 0 {
		c.maxLines = lines
	}
}

// CaptureAll captures output from all assignments in the session.
func (c *OutputCapture) CaptureAll(session *EnsembleSession) ([]CapturedOutput, error) {
	if c == nil {
		return nil, errors.New("output capture is nil")
	}
	if session == nil {
		return nil, errors.New("ensemble session is nil")
	}

	c.ensureDefaults()

	panes, err := c.tmuxClient.GetPanes(session.SessionName)
	if err != nil {
		return nil, fmt.Errorf("get panes: %w", err)
	}

	paneIDs := make(map[string]string, len(panes)*2)
	for _, pane := range panes {
		if pane.Title != "" {
			paneIDs[pane.Title] = pane.ID
		}
		if pane.ID != "" {
			paneIDs[pane.ID] = pane.ID
		}
	}

	outputs := make([]CapturedOutput, 0, len(session.Assignments))
	var captureErrs []error

	for _, assignment := range session.Assignments {
		target := paneIDs[assignment.PaneName]
		if target == "" {
			target = assignment.PaneName
			slog.Warn("ensemble output capture pane not found by title",
				"pane_name", assignment.PaneName,
				"session", session.SessionName,
			)
		}

		raw, captureErr := c.capturePane(target)
		captured := CapturedOutput{
			ModeID:     assignment.ModeID,
			PaneName:   assignment.PaneName,
			RawOutput:  raw,
			CapturedAt: time.Now().UTC(),
		}

		if captureErr != nil {
			captured.ParseErrors = append(captured.ParseErrors, captureErr)
			captureErrs = append(captureErrs, fmt.Errorf("%s: %w", assignment.PaneName, captureErr))
			slog.Error("ensemble output capture failed",
				"mode_id", assignment.ModeID,
				"pane", assignment.PaneName,
				"pane_id", target,
				"error", captureErr,
			)
			outputs = append(outputs, captured)
			continue
		}

		captured.LineCount = countLines(raw)
		clean := status.StripANSI(raw)

		if yamlBlock, ok := c.extractYAML(clean); ok && strings.TrimSpace(yamlBlock) != "" {
			parsed, validationErrs, parseErr := c.validator.ParseNormalizeAndValidate(yamlBlock, assignment.ModeID)
			if parseErr != nil {
				captured.ParseErrors = append(captured.ParseErrors, parseErr)
			} else {
				captured.Parsed = parsed
			}
			for i := range validationErrs {
				captured.ParseErrors = append(captured.ParseErrors, validationErrs[i])
			}
		}

		if captured.Parsed != nil {
			captured.TokenEstimate = EstimateModeOutputTokens(captured.Parsed)
		} else {
			captured.TokenEstimate = tokenpkg.EstimateTokensWithLanguageHint(clean, tokenpkg.ContentMarkdown)
		}

		slog.Info("ensemble output captured",
			"mode_id", assignment.ModeID,
			"pane", assignment.PaneName,
			"pane_id", target,
			"lines", captured.LineCount,
			"tokens", captured.TokenEstimate,
			"parsed", captured.Parsed != nil,
			"errors", len(captured.ParseErrors),
		)

		if len(captured.ParseErrors) > 0 {
			slog.Warn("ensemble output parse issues",
				"mode_id", assignment.ModeID,
				"pane", assignment.PaneName,
				"pane_id", target,
				"errors", len(captured.ParseErrors),
			)
		}

		outputs = append(outputs, captured)
	}

	return outputs, errors.Join(captureErrs...)
}

func (c *OutputCapture) capturePane(pane string) (string, error) {
	if pane == "" {
		return "", errors.New("pane is empty")
	}
	lines := c.maxLines
	if lines <= 0 {
		lines = defaultCaptureLines
	}
	return c.tmuxClient.CapturePaneOutput(pane, lines)
}

func (c *OutputCapture) extractYAML(raw string) (string, bool) {
	clean := status.StripANSI(raw)
	parser := codeblock.NewParser().WithLanguageFilter([]string{"yaml"})
	blocks := parser.Parse(clean)

	if len(blocks) > 0 {
		best := blocks[0].Content
		bestValid := ""
		bestValidLen := -1

		for _, block := range blocks {
			if _, err := c.validator.ParseYAML(block.Content); err == nil {
				if len(block.Content) > bestValidLen {
					bestValid = block.Content
					bestValidLen = len(block.Content)
				}
			}
		}

		if bestValidLen >= 0 {
			return bestValid, true
		}
		return best, true
	}

	lines := strings.Split(clean, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "thesis:") {
			return strings.Join(lines[i:], "\n"), true
		}
	}

	return "", false
}

func (c *OutputCapture) ensureDefaults() {
	if c.tmuxClient == nil {
		c.tmuxClient = tmux.DefaultClient
	}
	if c.maxLines <= 0 {
		c.maxLines = defaultCaptureLines
	}
	if c.validator == nil {
		c.validator = NewSchemaValidator()
	}
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines)
}
