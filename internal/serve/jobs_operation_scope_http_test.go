package serve

import (
	"context"
	"errors"
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
