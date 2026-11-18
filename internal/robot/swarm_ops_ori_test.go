package robot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

func initTempGitRepoWithCommit(t *testing.T, subject string) string {
	t.Helper()
	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Wave13Test",
			"GIT_AUTHOR_EMAIL=wave13@example.com",
			"GIT_COMMITTER_NAME=Wave13Test",
			"GIT_COMMITTER_EMAIL=wave13@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Wave 13\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", subject)
	return repoDir
}

func TestReviewQueue_GetReviewQueueAndDispatch(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	const sessionName = "wave13-review-session"
	repoDir := initTempGitRepoWithCommit(t, "feat(scale): add 11-harness robot parity")

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-done-1", "Implement robot scale", 1, "claude", "BlueLake", ""); err != nil {
		t.Fatalf("Assign completed bead: %v", err)
	}
	if err := store.MarkCompleted("bd-done-1"); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	if _, err := store.Assign("bd-busy-2", "Active work on pane 3", 3, "codex", "RedStone", ""); err != nil {
		t.Fatalf("Assign active bead: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("Save store: %v", err)
	}

	origSessionExists := reviewQueueSessionExists
	origGetPanes := reviewQueueGetPanes
	t.Cleanup(func() {
		reviewQueueSessionExists = origSessionExists
		reviewQueueGetPanes = origGetPanes
	})
	reviewQueueSessionExists = func(name string) bool {
		return name == sessionName
	}
	reviewQueueGetPanes = func(name string) ([]tmux.Pane, error) {
		return []tmux.Pane{
			{Index: 0, Title: "user", Type: tmux.AgentUser, Active: true},
			{Index: 1, Title: "__cc_1", Type: tmux.AgentClaude, Active: true},
			{Index: 2, Title: "__cod_1", Type: tmux.AgentCodex, Active: true},
			{Index: 3, Title: "__cod_2", Type: tmux.AgentCodex, Active: true},
		}, nil
	}

	// Validation: empty session -> INVALID_FLAG
	emptyOut, err := GetReviewQueue(context.Background(), ReviewQueueOptions{})
	if err != nil {
		t.Fatalf("GetReviewQueue empty session returned error: %v", err)
	}
	if emptyOut.Success || emptyOut.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for empty session, got %+v", emptyOut)
	}

	// Validation: invalid filter -> INVALID_FLAG
	badFilterOut, err := GetReviewQueue(context.Background(), ReviewQueueOptions{
		Session: sessionName,
		Filter:  "not-a-harness",
	})
	if err != nil {
		t.Fatalf("GetReviewQueue invalid filter returned error: %v", err)
	}
	if badFilterOut.Success || strings.Compare(badFilterOut.ErrorCode, ErrCodeInvalidFlag) != 0 {
		t.Fatalf("expected INVALID_FLAG for invalid filter, got %+v", badFilterOut)
	}

	// Validation: missing session -> SESSION_NOT_FOUND
	missingOut, err := GetReviewQueue(context.Background(), ReviewQueueOptions{
		Session: "missing-session",
	})
	if err != nil {
		t.Fatalf("GetReviewQueue missing session returned error: %v", err)
	}
	if missingOut.Success || missingOut.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND for missing session, got %+v", missingOut)
	}

	// Inspection + Dispatch with IdleThreshold=0 (so newly completed bead qualifies) and ProjectDir override
	type deliveredPrompt struct {
		target string
		prompt string
		enter  bool
	}
	var delivered []deliveredPrompt

	out, err := GetReviewQueue(context.Background(), ReviewQueueOptions{
		Session:          sessionName,
		ProjectDir:       repoDir,
		IdleThreshold:    0,
		IdleThresholdSet: true,
		CommitLimit:      5,
		CommitLimitSet:   true,
		Dispatch:         true,
		Sender: func(target, prompt string, enter bool) error {
			delivered = append(delivered, deliveredPrompt{target: target, prompt: prompt, enter: enter})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("GetReviewQueue dispatch returned error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected GetReviewQueue success, got error=%q code=%q", out.Error, out.ErrorCode)
	}
	if strings.Compare(out.SchemaID, reviewQueueSchemaID) != 0 {
		t.Fatalf("schema_id = %q, want %q", out.SchemaID, reviewQueueSchemaID)
	}
	// Pane 0 is user, Pane 3 has active assignment -> Panes 1 and 2 must be idle.
	if len(out.IdleAgents) != 2 || out.IdleAgents[0].Pane != 1 || out.IdleAgents[1].Pane != 2 {
		t.Fatalf("unexpected idle agents: %+v", out.IdleAgents)
	}
	// Suggestions must include both the completed bead ("bd-done-1") and the commit from repoDir.
	if len(out.Suggestions) != 2 {
		t.Fatalf("expected 2 suggestions (1 bead + 1 commit), got %+v", out.Suggestions)
	}
	foundBead := false
	foundCommit := false
	for _, s := range out.Suggestions {
		if strings.Compare(s.Source, "agent_work") == 0 && strings.Compare(s.SourceRef, "bd-done-1") == 0 {
			foundBead = true
		}
		if strings.Compare(s.Source, "git_commit") == 0 && strings.Contains(s.Prompt, "feat(scale): add 11-harness robot parity") {
			foundCommit = true
		}
	}
	if !foundBead || !foundCommit {
		t.Fatalf("missing expected bead/commit suggestions: %+v", out.Suggestions)
	}
	if !out.Dispatched || out.Sent != 2 || out.Skipped != 0 || len(delivered) != 2 {
		t.Fatalf("expected dispatched=true sent=2 skipped=0 delivered=2, got out=%+v delivered=%+v", out, delivered)
	}
	if delivered[0].target != sessionName+":.1" || !delivered[0].enter {
		t.Fatalf("unexpected first delivery target: %+v", delivered[0])
	}
	if delivered[1].target != sessionName+":.2" || !delivered[1].enter {
		t.Fatalf("unexpected second delivery target: %+v", delivered[1])
	}
}

func TestWebhookOutbox_ListRetryArchiveAndValidation(t *testing.T) {
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	t.Setenv("NTM_WEBHOOK_OUTBOX_DIR", outboxDir)
	projectDir := t.TempDir()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	cfgYAML := "webhooks:\n  - name: wave13-hook\n    url: " + server.URL + "\n    events: [agent.error]\n    formatter: json\n    retry:\n      max_attempts: 1\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	redactCfg := &redaction.Config{Mode: redaction.ModeOff}
	mgr, err := webhook.NewProjectWebhookManager(projectDir, redactCfg)
	if err != nil {
		t.Fatalf("NewProjectWebhookManager: %v", err)
	}
	if err := mgr.Start(); err != nil {
		t.Fatalf("mgr.Start: %v", err)
	}
	if err := mgr.Dispatch(webhook.Event{
		ID:      "evt-wave13-1",
		Type:    "agent.error",
		Session: "wave13-proj",
		Message: "simulated agent crash",
	}); err != nil {
		t.Fatalf("mgr.Dispatch: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err := mgr.ListOutbox()
		if err == nil && len(snap.Entries) == 1 && strings.EqualFold(snap.Entries[0].State, "failed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for failed outbox entry; last=%+v err=%v", snap, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := mgr.Stop(); err != nil {
		t.Fatalf("mgr.Stop: %v", err)
	}

	// Unknown action -> INVALID_FLAG
	badAction, err := GetWebhookOutbox(context.Background(), WebhookOutboxOptions{
		Action:     "purge",
		ProjectDir: projectDir,
		RedactCfg:  redactCfg,
	})
	if err != nil {
		t.Fatalf("GetWebhookOutbox unknown action returned error: %v", err)
	}
	if badAction.Success || strings.Compare(badAction.ErrorCode, ErrCodeInvalidFlag) != 0 {
		t.Fatalf("expected INVALID_FLAG for unknown action, got %+v", badAction)
	}

	// Missing record/revision on retry -> INVALID_FLAG
	missingParams, err := GetWebhookOutbox(context.Background(), WebhookOutboxOptions{
		Action:     "retry",
		ProjectDir: projectDir,
		RedactCfg:  redactCfg,
	})
	if err != nil {
		t.Fatalf("GetWebhookOutbox missing params returned error: %v", err)
	}
	if missingParams.Success || strings.Compare(missingParams.ErrorCode, ErrCodeInvalidFlag) != 0 {
		t.Fatalf("expected INVALID_FLAG for missing record/revision, got %+v", missingParams)
	}

	// List outbox -> returns failed receipt with revision
	listed, err := GetWebhookOutbox(context.Background(), WebhookOutboxOptions{
		Action:     "list",
		ProjectDir: projectDir,
		RedactCfg:  redactCfg,
	})
	if err != nil {
		t.Fatalf("GetWebhookOutbox list returned error: %v", err)
	}
	if !listed.Success || listed.Snapshot == nil || len(listed.Snapshot.Entries) != 1 {
		t.Fatalf("expected 1 outbox entry on list, got %+v", listed)
	}
	entry := listed.Snapshot.Entries[0]
	if entry.Record == "" || len(entry.Revision) != 64 {
		t.Fatalf("expected non-empty record and 64-hex revision, got %+v", entry)
	}

	// Archive terminal failed receipt using exact revision
	archived, err := GetWebhookOutbox(context.Background(), WebhookOutboxOptions{
		Action:     "archive",
		ProjectDir: projectDir,
		Record:     entry.Record,
		Revision:   entry.Revision,
		RedactCfg:  redactCfg,
	})
	if err != nil {
		t.Fatalf("GetWebhookOutbox archive returned error: %v", err)
	}
	if !archived.Success || archived.Archive == nil || strings.Compare(archived.Archive.Record, entry.Record) != 0 {
		t.Fatalf("expected archive success, got %+v", archived)
	}
	if _, statErr := os.Stat(archived.Archive.Path); statErr != nil {
		t.Fatalf("archived receipt missing at %s: %v", archived.Archive.Path, statErr)
	}

	// Subsequent list must show 0 active entries
	afterArchive, err := GetWebhookOutbox(context.Background(), WebhookOutboxOptions{
		Action:     "list",
		ProjectDir: projectDir,
		RedactCfg:  redactCfg,
	})
	if err != nil || !afterArchive.Success || len(afterArchive.Snapshot.Entries) != 0 {
		t.Fatalf("expected 0 active entries after archive, got %+v (err=%v)", afterArchive, err)
	}
}

func TestSummary_StoredSummariesListReadAndMissingSession(t *testing.T) {
	projectDir := t.TempDir()
	summariesDir := filepath.Join(projectDir, ".ntm", "summaries")
	if err := os.MkdirAll(summariesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	older := summary.SessionSummary{
		Session:     "alpha",
		GeneratedAt: time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC),
		Format:      summary.FormatBrief,
		Text:        "Alpha older summary",
	}
	newer := summary.SessionSummary{
		Session:     "my-app--frontend",
		GeneratedAt: time.Date(2026, 11, 16, 10, 15, 30, 0, time.UTC),
		Format:      summary.FormatDetailed,
		Text:        "Frontend newer summary",
	}
	writeSummary := func(name string, s summary.SessionSummary) string {
		t.Helper()
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(summariesDir, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	olderPath := writeSummary("alpha-20261115-093000.json", older)
	newerPath := writeSummary("my-app--frontend-20261116-101530.json", newer)
	// Noise file that should be ignored by ListStoredSummaries
	if err := os.WriteFile(filepath.Join(summariesDir, "notes.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := ListStoredSummaries(projectDir)
	if err != nil {
		t.Fatalf("ListStoredSummaries: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 stored summaries, got %+v", files)
	}
	if strings.Compare(files[0].Session, "my-app--frontend") != 0 || strings.Compare(files[0].Path, newerPath) != 0 {
		t.Fatalf("expected newest summary first, got %+v", files[0])
	}
	if strings.Compare(files[1].Session, "alpha") != 0 || strings.Compare(files[1].Path, olderPath) != 0 {
		t.Fatalf("expected older summary second, got %+v", files[1])
	}

	readBack, err := ReadStoredSummary(newerPath)
	if err != nil {
		t.Fatalf("ReadStoredSummary: %v", err)
	}
	if strings.Compare(readBack.Session, "my-app--frontend") != 0 || strings.Compare(readBack.Text, "Frontend newer summary") != 0 {
		t.Fatalf("unexpected ReadStoredSummary payload: %+v", readBack)
	}

	// GetSummary validation on empty and nonexistent sessions
	emptyResp, err := GetSummary(SummaryOptions{})
	if err != nil {
		t.Fatalf("GetSummary empty returned error: %v", err)
	}
	if emptyResp.Success || emptyResp.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for empty session, got %+v", emptyResp)
	}

	missingResp, err := GetSummary(SummaryOptions{
		Session: "__nonexistent_wave13_summary_session__",
	})
	if err != nil {
		t.Fatalf("GetSummary missing session returned error: %v", err)
	}
	if missingResp.Success || missingResp.ErrorCode != ErrCodeSessionNotFound {
		t.Fatalf("expected SESSION_NOT_FOUND for nonexistent session, got %+v", missingResp)
	}
}
