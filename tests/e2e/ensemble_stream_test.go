package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type streamChunk struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Index   int    `json:"index"`
}

func TestEnsembleSynthesizeStreamJSONL(t *testing.T) {
	testutil.E2ETestPrecheck(t)

	logger := testutil.NewTestLoggerStdout(t)
	streamHome(t)

	workDir := t.TempDir()
	session, paneID := createEnsembleStreamSession(t, logger, "stream")
	writeModeOutputToPane(t, paneID)
	assertPaneContains(t, paneID, "```yaml")

	state := buildEnsembleSessionState(session, paneID)
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("save ensemble session: %v", err)
	}

	stdout, stderr, err := runNTMStreamSynthesize(t, workDir, session, "", false)
	if err != nil {
		t.Fatalf("ntm ensemble synthesize failed: %v\nstderr:\n%s", err, stderr)
	}

	chunks := parseStreamChunks(t, stdout)
	if len(chunks) == 0 {
		t.Fatalf("expected streamed chunks, got none")
	}

	assertChunkIndexes(t, chunks)
	assertChunkTypes(t, chunks, []string{"status", "complete"})
}

func TestEnsembleSynthesizeStreamResumeJSONL(t *testing.T) {
	testutil.E2ETestPrecheck(t)

	logger := testutil.NewTestLoggerStdout(t)
	streamHome(t)

	workDir := t.TempDir()
	session, paneID := createEnsembleStreamSession(t, logger, "resume")
	writeModeOutputToPane(t, paneID)
	assertPaneContains(t, paneID, "```yaml")

	state := buildEnsembleSessionState(session, paneID)
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("save ensemble session: %v", err)
	}

	runID := fmt.Sprintf("%s-run", session)
	createSynthesisCheckpoint(t, workDir, state, runID, 2)

	stdout, stderr, err := runNTMStreamSynthesize(t, workDir, session, runID, true)
	if err != nil {
		t.Fatalf("ntm ensemble synthesize resume failed: %v\nstderr:\n%s", err, stderr)
	}

	chunks := parseStreamChunks(t, stdout)
	if len(chunks) == 0 {
		t.Fatalf("expected streamed chunks, got none")
	}

	for _, chunk := range chunks {
		if chunk.Index <= 2 {
			t.Fatalf("expected resume to skip index <=2, got %d", chunk.Index)
		}
	}
	assertChunkIndexes(t, chunks)
	assertChunkTypes(t, chunks, []string{"status", "complete"})
}

func TestEnsembleSynthesizeStreamWritesCheckpoint(t *testing.T) {
	testutil.E2ETestPrecheck(t)

	logger := testutil.NewTestLoggerStdout(t)
	streamHome(t)

	workDir := t.TempDir()
	session, paneID := createEnsembleStreamSession(t, logger, "checkpoint")
	writeModeOutputToPane(t, paneID)
	assertPaneContains(t, paneID, "```yaml")

	state := buildEnsembleSessionState(session, paneID)
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("save ensemble session: %v", err)
	}

	runID := fmt.Sprintf("%s-run", session)
	_, stderr, err := runNTMStreamSynthesize(t, workDir, session, runID, false)
	if err != nil {
		t.Fatalf("ntm ensemble synthesize failed: %v\nstderr:\n%s", err, stderr)
	}

	store, err := ensemble.NewCheckpointStore(filepath.Join(workDir, ".ntm"))
	if err != nil {
		t.Fatalf("open checkpoint store: %v", err)
	}
	checkpoint, err := store.LoadSynthesisCheckpoint(runID)
	if err != nil {
		t.Fatalf("load synthesis checkpoint: %v", err)
	}
	if checkpoint.LastIndex == 0 {
		t.Fatalf("expected LastIndex to be set")
	}
}

var (
	streamHomeOnce sync.Once
	streamHomeDir  string
)

func streamHome(t *testing.T) {
	t.Helper()

	streamHomeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ntm-e2e-home-")
		if err != nil {
			t.Fatalf("create temp home: %v", err)
		}
		streamHomeDir = dir
	})
	t.Setenv("HOME", streamHomeDir)
}

func createEnsembleStreamSession(t *testing.T, logger *testutil.TestLogger, label string) (string, string) {
	t.Helper()

	session := fmt.Sprintf("ntm_test_stream_%s_%d", label, time.Now().UnixNano())
	logger.LogSection("Create stream session")
	if err := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session).Run(); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	paneID := firstPaneID(t, session)
	logger.Log("Session %s pane: %s", session, paneID)
	return session, paneID
}

func firstPaneID(t *testing.T, session string) string {
	t.Helper()
	out, err := exec.Command(tmux.BinaryPath(), "list-panes", "-t", session, "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("list panes: %v", err)
	}
	panes := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(panes) == 0 || panes[0] == "" {
		t.Fatalf("no panes found for session %s", session)
	}
	return panes[0]
}

func writeModeOutputToPane(t *testing.T, paneID string) {
	t.Helper()
	lines := []string{
		"```yaml",
		`mode_id: "mode-a"`,
		`thesis: "Stream test synthesis"`,
		`confidence: 0.8`,
		"top_findings:",
		`  - finding: "Uses REST APIs"`,
		`    impact: "medium"`,
		`    confidence: 0.9`,
		"risks:",
		`  - risk: "Scaling concerns"`,
		`    impact: "high"`,
		`    likelihood: 0.6`,
		"recommendations:",
		`  - recommendation: "Add caching"`,
		`    priority: "high"`,
		"questions_for_user:",
		`  - question: "Which service is highest priority?"`,
		"```",
	}

	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", paneID, "cat <<'EOF'", "Enter").Run(); err != nil {
		t.Fatalf("send here-doc start: %v", err)
	}
	for _, line := range lines {
		if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", paneID, line, "Enter").Run(); err != nil {
			t.Fatalf("send line: %v", err)
		}
	}
	if err := exec.Command(tmux.BinaryPath(), "send-keys", "-t", paneID, "EOF", "Enter").Run(); err != nil {
		t.Fatalf("send here-doc end: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
}

func assertPaneContains(t *testing.T, paneID, needle string) {
	t.Helper()

	output, err := capturePaneOutput(paneID, 200)
	if err != nil {
		t.Fatalf("capture pane: %v", err)
	}
	if !strings.Contains(output, needle) {
		t.Fatalf("pane output missing %q\noutput:\n%s", needle, output)
	}
}

func capturePaneOutput(paneID string, lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	out, err := exec.Command(tmux.BinaryPath(), "capture-pane", "-t", paneID, "-p", "-S", fmt.Sprintf("-%d", lines)).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func buildEnsembleSessionState(session, paneID string) *ensemble.EnsembleSession {
	now := time.Now().UTC()
	return &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "Stream synthesis test",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyManual,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{
				ModeID:     "mode-a",
				PaneName:   paneID,
				AgentType:  "cod",
				Status:     ensemble.AssignmentDone,
				AssignedAt: now,
			},
		},
	}
}

func createSynthesisCheckpoint(t *testing.T, workDir string, state *ensemble.EnsembleSession, runID string, lastIndex int) {
	t.Helper()

	store, err := ensemble.NewCheckpointStore(filepath.Join(workDir, ".ntm"))
	if err != nil {
		t.Fatalf("open checkpoint store: %v", err)
	}

	meta := ensemble.CheckpointMetadata{
		SessionName:  state.SessionName,
		Question:     state.Question,
		RunID:        runID,
		Status:       state.Status,
		CreatedAt:    state.CreatedAt,
		CompletedIDs: []string{"mode-a"},
		PendingIDs:   []string{},
		TotalModes:   len(state.Assignments),
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("save checkpoint metadata: %v", err)
	}

	if err := store.SaveSynthesisCheckpoint(runID, ensemble.SynthesisCheckpoint{
		RunID:       runID,
		SessionName: state.SessionName,
		LastIndex:   lastIndex,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save synthesis checkpoint: %v", err)
	}
}

func runNTMStreamSynthesize(t *testing.T, workDir, session, runID string, resume bool) (string, string, error) {
	t.Helper()

	args := []string{"ensemble", "synthesize", session, "--stream", "--format", "json"}
	if resume {
		args = append(args, "--resume", "--run-id", runID)
	}

	cmd := exec.Command("ntm", args...)
	cmd.Dir = workDir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func parseStreamChunks(t *testing.T, output string) []streamChunk {
	t.Helper()
	raw := strings.TrimSpace(output)
	if raw == "" {
		return nil
	}

	lines := strings.Split(raw, "\n")
	chunks := make([]streamChunk, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("invalid JSONL chunk: %v\nline: %s", err, line)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func assertChunkIndexes(t *testing.T, chunks []streamChunk) {
	t.Helper()
	prev := 0
	for i, chunk := range chunks {
		if chunk.Index <= prev {
			t.Fatalf("chunk index %d = %d, want > %d", i, chunk.Index, prev)
		}
		prev = chunk.Index
	}
}

func assertChunkTypes(t *testing.T, chunks []streamChunk, required []string) {
	t.Helper()
	present := make(map[string]bool, len(chunks))
	for _, chunk := range chunks {
		present[chunk.Type] = true
	}
	for _, req := range required {
		if !present[req] {
			t.Fatalf("missing required chunk type %q", req)
		}
	}
}
