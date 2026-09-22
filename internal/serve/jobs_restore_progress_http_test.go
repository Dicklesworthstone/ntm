package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRestoreJobProgressHTTPPersistsBeforeActuationAndSurvivesCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	ready, calls := filepath.Join(dir, "ready"), filepath.Join(dir, "calls")
	t.Setenv("NTM_RESTORE_PROGRESS_READY", ready)
	t.Setenv("NTM_RESTORE_PROGRESS_CALLS", calls)
	bin := filepath.Join(dir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$1" >> "$NTM_RESTORE_PROGRESS_CALLS"
case "$1" in
  has-session) echo "can't find session: recovery-target" >&2; exit 1 ;;
  new-session) echo entered > "$NTM_RESTORE_PROGRESS_READY"; exec sleep 30 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	old := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = old })
	cp := &checkpoint.Checkpoint{
		Version: checkpoint.CurrentVersion, ID: "progress-source", SessionName: "recovery-source",
		WorkingDir: t.TempDir(), CreatedAt: time.Now(), PaneCount: 1,
		Session: checkpoint.SessionState{Panes: []checkpoint.PaneState{{ID: "%100", AgentType: "user", Command: "bash"}}},
	}
	if err := checkpoint.NewStorage().Save(cp); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.db")
	srv, closeServer := newJournalHTTPServer(t, path)
	body := `{"type":"checkpoint_restore","params":{"session":"recovery-source","checkpoint_id":"progress-source","target_session":"recovery-target","operation_id":"restore-progress-once","skip_git_check":true}}`
	job := admissionAcceptedJob(t, srv, body)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restore did not enter tmux: %+v", srv.jobStore.Get(job.ID))
		}
		time.Sleep(5 * time.Millisecond)
	}
	response := admissionRequest(srv, http.MethodGet, "/api/v1/jobs/"+job.ID, "")
	var live struct {
		Job *Job `json:"job"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &live) != nil || live.Job == nil {
		t.Fatalf("live job unavailable: %d %s", response.Code, response.Body.String())
	}
	assertRestoreJobBeforeCreate(t, live.Job.Result)
	journalDir, err := srv.jobJournalDir()
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := (&jobJournal{dir: journalDir}).load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, saved := range persisted {
		if saved.ID == job.ID {
			found = true
			assertRestoreJobBeforeCreate(t, saved.Result)
		}
	}
	if !found {
		t.Fatal("actuation preceded durable job evidence")
	}
	operation, err := readJobOperation(jobOperationPath(journalDir, "restore-progress-once"))
	if err != nil || operation == nil {
		t.Fatalf("operation receipt missing: %+v %v", operation, err)
	}
	assertRestoreJobBeforeCreate(t, operation.Result)
	response = admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+job.ID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel failed: %d %s", response.Code, response.Body.String())
	}
	closeServer() // Joins the interrupted subprocess and both final journal writes.
	restarted, _ := newJournalHTTPServer(t, path)
	terminal := pollJobTerminal(t, restarted, job.ID)
	if terminal.Job.Status != string(JobStatusCancelled) || terminal.Job.Result["session_name"] != "recovery-target" || terminal.Job.Result["source_session"] != "recovery-source" {
		t.Fatalf("cancellation/restart lost recovery target: %+v", terminal.Job)
	}
	progress, ok := terminal.Job.Result["restore_progress"].(map[string]interface{})
	if !ok {
		t.Fatalf("late restore evidence was discarded: %+v", terminal.Job.Result)
	}
	last := progress["last_event"].(map[string]interface{})
	if last["stage"] != "create_session" || last["phase"] != "after" || last["outcome"] != "uncertain" || progress["session_created"] != false {
		t.Fatalf("interrupted create was fabricated as completed: %+v", progress)
	}
	// Replaying the same operation returns its recorded result; it must not
	// launch another recovery or erase the nested evidence after restart.
	replayed := admissionAcceptedJob(t, restarted, body)
	got := pollJobTerminal(t, restarted, replayed.ID)
	if got.Job.Status != string(JobStatusCancelled) || got.Job.Result["restore_progress"] == nil {
		t.Fatalf("operation replay lost cancelled recovery evidence: %+v", got.Job)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "new-session") != 1 || strings.Contains(string(data), "kill-session") {
		t.Fatalf("cancel/replay re-executed or destroyed recovery: %s", data)
	}
}

func assertRestoreJobBeforeCreate(t *testing.T, result map[string]interface{}) {
	t.Helper()
	if result["session_name"] != "recovery-target" || result["source_session"] != "recovery-source" || result["checkpoint_id"] != "progress-source" || result["_execution_in_progress"] != true {
		t.Fatalf("missing live recovery identities: %+v", result)
	}
	progress, ok := result["restore_progress"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing restore progress: %+v", result)
	}
	last, ok := progress["last_event"].(map[string]interface{})
	if !ok || last["stage"] != "create_session" || last["phase"] != "before" || last["outcome"] != "uncertain" || progress["session_created"] != false {
		t.Fatalf("missing pre-actuation checkpoint: %+v", progress)
	}
}
