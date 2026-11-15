package ensemble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const defaultCaptureLines = 1000

// CapturedOutput holds raw and parsed output for an ensemble assignment.
type CapturedOutput struct {
	ModeID        string
	PaneName      string
	RawOutput     string
	OutputPath    string // Durable path to the raw transcript for this capture.
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

// CaptureAll captures output from all assignments in the session.
func (c *OutputCapture) CaptureAll(session *EnsembleSession) ([]CapturedOutput, error) {
	return c.CaptureAllContext(context.Background(), session)
}

// CaptureAllContext captures assignment output while honoring caller cancellation.
func (c *OutputCapture) CaptureAllContext(ctx context.Context, session *EnsembleSession) ([]CapturedOutput, error) {
	if c == nil {
		return nil, errors.New("output capture is nil")
	}
	if session == nil {
		return nil, errors.New("ensemble session is nil")
	}

	if ctx == nil {
		return nil, errors.New("output capture context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.ensureDefaults()

	panes, err := c.tmuxClient.GetPanesContext(ctx, session.SessionName)
	if err != nil {
		return nil, fmt.Errorf("get panes: %w", err)
	}

	paneIDs := make(map[string][]string, len(panes)*2)
	for _, pane := range panes {
		if pane.ID == "" {
			continue
		}
		paneIDs[pane.ID] = append(paneIDs[pane.ID], pane.ID)
		if pane.Title != "" && pane.Title != pane.ID {
			paneIDs[pane.Title] = append(paneIDs[pane.Title], pane.ID)
		}
	}

	outputs := make([]CapturedOutput, 0, len(session.Assignments))
	var captureErrs []error

	for _, assignment := range session.Assignments {
		if err := ctx.Err(); err != nil {
			return outputs, err
		}
		var target, raw string
		var captureErr error
		matches := paneIDs[assignment.PaneName]
		if len(matches) != 1 {
			captureErr = fmt.Errorf("assignment pane %q has %d matches in session %q", assignment.PaneName, len(matches), session.SessionName)
		} else {
			target = matches[0]
			raw, captureErr = c.capturePane(ctx, target)
		}
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
				if parsed != nil && parsed.ModeID != assignment.ModeID {
					captured.ParseErrors = append(captured.ParseErrors,
						fmt.Errorf("output mode %q does not match assignment %q", parsed.ModeID, assignment.ModeID))
				}
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

	return outputs, errors.Join(append(captureErrs, ctx.Err())...)
}

// CaptureAndSave captures every assignment and persists the outputs before
// callers interrupt or destroy the session. Parse failures retain raw output;
// only schema-valid outputs mark an assignment complete.
func (c *OutputCapture) CaptureAndSave(session *EnsembleSession) ([]CapturedOutput, error) {
	return c.CaptureAndSaveContext(context.Background(), session)
}

// CaptureAndSaveContext persists a complete capture unless the caller cancels.
func (c *OutputCapture) CaptureAndSaveContext(ctx context.Context, session *EnsembleSession) ([]CapturedOutput, error) {
	captured, err := c.CaptureAllContext(ctx, session)
	if err != nil {
		return captured, err
	}
	if err := ctx.Err(); err != nil {
		return captured, err
	}
	if err := saveCapturedOutputs(session, captured); err != nil {
		return captured, err
	}
	return captured, nil
}

// CaptureForSynthesis reconciles finished live assignments before readiness is
// checked. A valid response must also end at an idle prompt: a partial response
// or an echoed schema must not authorize dispatching a synthesis prompt. Queued
// assignments cannot complete from scrollback left by an earlier task.
func (c *OutputCapture) CaptureForSynthesis(ctx context.Context, session *EnsembleSession) ([]CapturedOutput, error) {
	captured, err := c.CaptureAllContext(ctx, session)
	if err != nil {
		return captured, err
	}
	needsSave := false
	for i := range captured {
		output := &captured[i]
		assignment := session.Assignments[i]
		switch assignment.Status {
		case AssignmentActive:
			if !status.DetectIdleFromOutput(output.RawOutput, assignment.AgentType) {
				output.ParseErrors = append(output.ParseErrors, errors.New("mode agent has not returned to an idle prompt"))
			}
		case AssignmentDone:
			// Saved results remain usable if the live pane has since moved on.
		default:
			output.ParseErrors = append(output.ParseErrors, fmt.Errorf("mode assignment is %s", assignment.Status))
		}
		if output.Parsed != nil && len(output.ParseErrors) == 0 &&
			(assignment.Status != AssignmentDone || assignment.OutputPath == "") {
			needsSave = true
		}
	}
	if needsSave {
		if err := ctx.Err(); err != nil {
			return captured, err
		}
		if err := saveCapturedOutputs(session, captured); err != nil {
			return captured, err
		}
	}
	return captured, nil
}

func saveCapturedOutputs(session *EnsembleSession, captured []CapturedOutput) error {
	if session == nil {
		return errors.New("ensemble session is nil")
	}
	sessionName, err := NormalizeCheckpointRunID(session.SessionName)
	if err != nil {
		return fmt.Errorf("invalid output session name: %w", err)
	}
	if sessionName != session.SessionName {
		return errors.New("output session name must not contain surrounding whitespace")
	}
	if len(captured) != len(session.Assignments) {
		return fmt.Errorf("captured %d outputs for %d assignments", len(captured), len(session.Assignments))
	}
	for i, output := range captured {
		assignment := session.Assignments[i]
		if output.ModeID != assignment.ModeID || output.PaneName != assignment.PaneName {
			return fmt.Errorf("capture %d does not match assignment %s in pane %s", i, assignment.ModeID, assignment.PaneName)
		}
	}
	if len(captured) == 0 {
		return nil
	}

	store, err := defaultSQLiteStore()
	if err != nil {
		return err
	}
	stateDir, err := filepath.Abs(filepath.Dir(store.store.Path()))
	if err != nil {
		return fmt.Errorf("resolve ensemble output directory: %w", err)
	}
	outputRoot := filepath.Join(stateDir, "ensemble-outputs")
	sessionDir := filepath.Join(outputRoot, sessionName)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return fmt.Errorf("create ensemble output directory: %w", err)
	}
	// Every attempt has its own directory, so a failed retry cannot overwrite
	// the last recoverable snapshot.
	captureDir, err := os.MkdirTemp(sessionDir, "capture-")
	if err != nil {
		return fmt.Errorf("create ensemble capture directory: %w", err)
	}

	next := *session
	next.Assignments = append([]ModeAssignment(nil), session.Assignments...)
	validator := NewSchemaValidator()
	for i := range captured {
		output := &captured[i]
		assignment := &next.Assignments[i]
		rawPath := filepath.Join(captureDir, fmt.Sprintf("%03d.raw.txt", i))
		if err := util.AtomicWriteFile(rawPath, []byte(output.RawOutput), 0o600); err != nil {
			return fmt.Errorf("save raw output for %s: %w", output.ModeID, err)
		}
		output.OutputPath = rawPath

		// Preserve an earlier valid structured result if the latest pane
		// contains an incomplete response. Its new raw transcript is still saved.
		if assignment.Status != AssignmentDone || assignment.OutputPath == "" {
			assignment.OutputPath = rawPath
		}
		if output.Parsed == nil || len(output.ParseErrors) != 0 ||
			output.Parsed.ModeID != assignment.ModeID || len(validator.Validate(output.Parsed)) != 0 {
			continue
		}

		parsed := *output.Parsed
		parsed.RawOutput = output.RawOutput
		data, err := json.MarshalIndent(parsed, "", "  ")
		if err != nil {
			return fmt.Errorf("encode output for %s: %w", output.ModeID, err)
		}
		outputPath := filepath.Join(captureDir, fmt.Sprintf("%03d.json", i))
		if err := util.AtomicWriteFile(outputPath, data, 0o600); err != nil {
			return fmt.Errorf("save structured output for %s: %w", output.ModeID, err)
		}
		completedAt := output.CapturedAt
		if completedAt.IsZero() {
			completedAt = time.Now().UTC()
		}
		assignment.OutputPath = outputPath
		assignment.Status = AssignmentDone
		assignment.CompletedAt = &completedAt
		assignment.Error = ""
	}

	// AtomicWriteFile syncs each file and captureDir. Sync the new directory
	// entries before publishing paths in SQLite.
	for _, dir := range []string{sessionDir, outputRoot, stateDir} {
		if err := util.SyncDirectory(dir); err != nil {
			return fmt.Errorf("sync ensemble output directory: %w", err)
		}
	}
	if err := store.Save(&next); err != nil {
		return fmt.Errorf("save captured ensemble state: %w", err)
	}
	*session = next
	return nil
}

func (c *OutputCapture) capturePane(ctx context.Context, pane string) (string, error) {
	if pane == "" {
		return "", errors.New("pane is empty")
	}
	lines := c.maxLines
	if lines <= 0 {
		lines = defaultCaptureLines
	}
	return c.tmuxClient.CapturePaneOutputContext(ctx, pane, lines)
}

func (c *OutputCapture) extractYAML(raw string) (string, bool) {
	clean := status.StripANSI(raw)
	parser := codeblock.NewParser().WithLanguageFilter([]string{"yaml", "yml", "json"})
	blocks := parser.Parse(clean)

	if len(blocks) > 0 {
		// Prefer the latest schema-parseable block that is not an echoed prompt template.
		for i := len(blocks) - 1; i >= 0; i-- {
			content := blocks[i].Content
			if strings.Contains(content, "{{mode_id}}") {
				continue
			}
			if parsed, errs, err := c.validator.ParseNormalizeAndValidate(content, ""); err == nil && len(errs) == 0 && parsed != nil {
				return content, true
			}
		}

		best := blocks[len(blocks)-1].Content
		bestValid := ""
		bestValidLen := -1

		for _, block := range blocks {
			if strings.Contains(block.Content, "{{mode_id}}") {
				continue
			}
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
			candidate := strings.Join(lines[i:], "\n")
			if !strings.Contains(candidate, "Your one-sentence main insight or conclusion") {
				return candidate, true
			}
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
