package serve

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// Exercise the actual POST/GET jobs surface, admission's project binding,
// dispatch, and durable receipts; only the external agent launcher is replaced.
func TestJobOperationHTTPProjectScopeAcrossRestart(t *testing.T) {
	for _, workingDir := range []string{"", "child"} {
		t.Run("working_dir="+workingDir, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			projectA, projectB := t.TempDir(), t.TempDir()
			srv, closeServer := newJournalHTTPServer(t, path)
			srv.projectDir = projectA
			var calls atomic.Int32
			srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				if err := os.MkdirAll(opts.WorkingDir, 0o700); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(opts.WorkingDir, "effect"), []byte("once"), 0o600); err != nil {
					return nil, err
				}
				out := &robot.SpawnOutput{Session: opts.Session, WorkingDir: opts.WorkingDir}
				out.Success = true
				return out, nil
			}
			body := `{"type":"swarm_spawn","params":{"operation_id":"same-operation","session":"same-name","cc_count":1,"working_dir":"` + workingDir + `"}}`
			first := postJob(t, srv, body)
			original := pollJobTerminal(t, srv, first.Job.ID)
			if original.Job.Status != string(JobStatusCompleted) || calls.Load() != 1 {
				t.Fatalf("original operation failed: %+v", original.Job)
			}
			closeServer()
			restarted, _ := newJournalHTTPServer(t, path)
			restarted.projectDir = projectB
			restarted.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				return nil, errors.New("retry must not launch an agent")
			}
			retry := postJob(t, restarted, body)
			conflict := pollJobTerminal(t, restarted, retry.Job.ID)
			if conflict.Job.Status != string(JobStatusFailed) || !strings.Contains(conflict.Job.Error, "execution project") ||
				operationMetadata(t, conflict.Job.Result)["status"] != "conflict" || calls.Load() != 1 {
				t.Fatalf("HTTP replay ignored changed admitted project: %+v calls=%d", conflict.Job, calls.Load())
			}
			if conflict.Job.Result["working_dir"] != nil || conflict.Job.Result["session"] != nil {
				t.Fatalf("cross-project conflict replayed the old payload: %+v", conflict.Job.Result)
			}
			restarted.mu.Lock()
			restarted.projectDir = projectA
			restarted.mu.Unlock()
			retry = postJob(t, restarted, body)
			replayed := pollJobTerminal(t, restarted, retry.Job.ID)
			meta := operationMetadata(t, replayed.Job.Result)
			if replayed.Job.Status != string(JobStatusCompleted) || meta["replayed"] != true || meta["original_job_id"] != first.Job.ID || calls.Load() != 1 {
				t.Fatalf("returning to original project did not replay safely: %+v", replayed.Job)
			}
			effect, err := os.ReadFile(filepath.Join(projectA, workingDir, "effect"))
			if err != nil || string(effect) != "once" {
				t.Fatalf("original effect was lost or repeated: %q %v", effect, err)
			}
			if _, err := os.Stat(filepath.Join(projectB, workingDir, "effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cross-project retry performed work")
			}
		})
	}
}

func TestJobHTTPProjectScopeTransportReplayCannotHideOperationConflict(t *testing.T) {
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
	projectA, projectB := t.TempDir(), t.TempDir()
	jobScopeHTTPPatchProject(t, srv, projectA)
	var calls atomic.Int32
	srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		calls.Add(1)
		if err := os.WriteFile(filepath.Join(opts.WorkingDir, "effect"), []byte("once"), 0600); err != nil {
			return nil, err
		}
		out := &robot.SpawnOutput{Session: opts.Session, WorkingDir: opts.WorkingDir}
		out.Success = true
		return out, nil
	}
	const body = `{"type":"swarm_spawn","params":{"operation_id":"scope-over-cache","session":"scoped-operation","cc_count":1}}`
	const key = "same-durable-transport-key"
	first := jobScopeHTTPReceipt(t, jobScopeHTTPPost(srv, body, key))
	original := pollJobTerminal(t, srv, first.ID)
	if original.Job.Status != string(JobStatusCompleted) || calls.Load() != 1 {
		t.Fatalf("original durable operation failed: %+v calls=%d", original.Job, calls.Load())
	}
	dir, err := srv.jobJournalDir()
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := jobOperationPath(dir, "scope-over-cache")
	before, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	jobScopeHTTPPatchProject(t, srv, projectB)
	rec := jobScopeHTTPPost(srv, body, key)
	second := jobScopeHTTPReceipt(t, rec)
	if second.ID == first.ID || second.ProjectDir != projectB || rec.Header().Get("X-Idempotent-Replay") != "" {
		t.Fatalf("transport replay hid the new admitted project: %+v %v", second, rec.Header())
	}
	conflict := pollJobTerminal(t, srv, second.ID)
	if conflict.Job.Status != string(JobStatusFailed) || !strings.Contains(conflict.Job.Error, "execution project") || operationMetadata(t, conflict.Job.Result)["status"] != "conflict" || calls.Load() != 1 {
		t.Fatalf("cross-project durable retry bypassed its conflict: %+v calls=%d", conflict.Job, calls.Load())
	}
	if conflict.Job.Result["working_dir"] != nil || conflict.Job.Result["session"] != nil {
		t.Fatalf("conflicting job exposed the original operation's result: %+v", conflict.Job.Result)
	}
	if _, err := os.Stat(filepath.Join(projectB, "effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-project durable retry performed work: %v", err)
	}
	jobScopeHTTPPatchProject(t, srv, projectA)
	rec = jobScopeHTTPPost(srv, body, key)
	replay := jobScopeHTTPReceipt(t, rec)
	if rec.Code != http.StatusAccepted || replay.ID != first.ID || rec.Header().Get("X-Idempotent-Replay") != "true" || calls.Load() != 1 || len(srv.jobStore.List()) != 2 {
		t.Fatalf("original project lost its safe HTTP replay: %+v %v calls=%d", replay, rec.Header(), calls.Load())
	}
	if after, err := os.ReadFile(receiptPath); err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP scope conflict rewrote durable operation evidence")
	}
}
