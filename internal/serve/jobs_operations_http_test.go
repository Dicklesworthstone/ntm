package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestJobOperationHTTPReplayAcrossServerRestart(t *testing.T) {
	t.Parallel()
	for _, fails := range []bool{false, true} {
		t.Run(fmt.Sprintf("fails=%v", fails), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "state.db")
			srv, closeServer := newJournalHTTPServer(t, path)
			var calls atomic.Int32
			srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				out := &robot.SpawnOutput{Session: opts.Session, Agents: []robot.SpawnedAgent{{Pane: "%7", Type: "claude"}}}
				out.Success = !fails
				if fails {
					return out, errors.New("second pane failed")
				}
				return out, nil
			}
			body := `{"type":"swarm_spawn","params":{"operation_id":"deploy-42","session":"durable","cc_count":2}}`
			first := postJob(t, srv, body)
			original := pollJobTerminal(t, srv, first.Job.ID)
			if original.Job.Result["session"] != "durable" || calls.Load() != 1 {
				t.Fatalf("first operation did not run: %+v", original.Job)
			}
			closeServer()
			restarted, _ := newJournalHTTPServer(t, path)
			restarted.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				return nil, errors.New("retry must not spawn")
			}
			retry := postJob(t, restarted, body)
			got := pollJobTerminal(t, restarted, retry.Job.ID)
			meta := operationMetadata(t, got.Job.Result)
			if got.Job.ID == first.Job.ID || calls.Load() != 1 || got.Job.Status != original.Job.Status || got.Job.Error != original.Job.Error ||
				got.Job.Result["session"] != "durable" || meta["original_job_id"] != first.Job.ID || meta["replayed"] != true {
				t.Fatalf("restart replay lost or repeated the operation: %+v calls=%d", got.Job, calls.Load())
			}
			conflict := postJob(t, restarted, `{"type":"swarm_spawn","params":{"operation_id":"deploy-42","session":"different","cc_count":2}}`)
			got = pollJobTerminal(t, restarted, conflict.Job.ID)
			if got.Job.Status != string(JobStatusFailed) || !strings.Contains(got.Job.Error, "conflicts") || calls.Load() != 1 {
				t.Fatalf("changed request reused a durable operation ID: %+v", got.Job)
			}
		})
	}
}

func TestJobOperationHTTPCancellationIsDurable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
	srv, closeServer := newJournalHTTPServer(t, path)
	started := make(chan struct{})
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		close(started)
		<-ctx.Done()
		return &robot.SpawnOutput{Session: opts.Session, Agents: []robot.SpawnedAgent{{Pane: "%9"}}}, ctx.Err()
	}
	body := `{"type":"swarm_spawn","params":{"operation_id":"cancel-once","session":"partial","cc_count":2}}`
	first := postJob(t, srv, body)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not reach engine")
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+first.Job.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel failed: %d %s", rec.Code, rec.Body.String())
	}
	closeServer()
	restarted, _ := newJournalHTTPServer(t, path)
	var calls atomic.Int32
	restarted.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) { calls.Add(1); return nil, nil }
	retry := postJob(t, restarted, body)
	got := pollJobTerminal(t, restarted, retry.Job.ID)
	if got.Job.Status != string(JobStatusCancelled) || got.Job.Result["session"] != "partial" || calls.Load() != 0 ||
		operationMetadata(t, got.Job.Result)["replayed"] != true || !strings.Contains(got.Job.Error, "context canceled") {
		t.Fatalf("cancellation was lost or re-executed: %+v", got.Job)
	}
}

func TestJobOperationHTTPRequiresDurabilityAndValidIdentity(t *testing.T) {
	t.Parallel()
	srv := NewHermeticServer("test")
	defer srv.Stop()
	var calls atomic.Int32
	srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) { calls.Add(1); return nil, nil }
	for _, id := range []string{`"durable-required"`, `null`, `true`, `3`, `""`, `"has spaces"`} {
		env := postJob(t, srv, fmt.Sprintf(`{"type":"swarm_spawn","params":{"operation_id":%s,"session":"guarded","cc_count":1}}`, id))
		got := pollJobTerminal(t, srv, env.Job.ID)
		if got.Job.Status != string(JobStatusFailed) || got.Job.Error == "" || calls.Load() != 0 {
			t.Fatalf("unsafe operation_id reached engine: %+v calls=%d", got.Job, calls.Load())
		}
	}
}

func TestJobOperationHTTPKeepsStrictParametersForEveryJobType(t *testing.T) {
	t.Parallel()
	for _, kind := range implementedJobTypes {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
			body := fmt.Sprintf(`{"type":%q,"params":{"operation_id":%q,"dry_rnu":true}}`, kind, "strict-"+kind)
			for attempt := 0; attempt < 2; attempt++ {
				env := postJob(t, srv, body)
				got := pollJobTerminal(t, srv, env.Job.ID)
				if got.Job.Status != string(JobStatusFailed) || !strings.Contains(got.Job.Error, `unknown field "dry_rnu"`) {
					t.Fatalf("operation guard bypassed strict %s validation: %+v", kind, got.Job)
				}
				if operationMetadata(t, got.Job.Result)["replayed"] != (attempt == 1) {
					t.Fatalf("failure receipt was not replayed: %+v", got.Job)
				}
			}
		})
	}
}

func TestJobSessionEnvelopeTargetsSpawnHTTP(t *testing.T) {
	t.Parallel()
	srv := NewHermeticServer("test")
	defer srv.Stop()
	opts := make(chan robot.SpawnOptions, 1)
	srv.spawnAgents = func(_ context.Context, got robot.SpawnOptions) (*robot.SpawnOutput, error) {
		opts <- got
		out := &robot.SpawnOutput{Session: got.Session, DryRun: got.DryRun}
		out.Success = true
		return out, nil
	}
	env := postJob(t, srv, `{"type":"swarm_spawn","session":"envelope-target","params":{"cc_count":2,"dry_run":true,"label":"lane"}}`)
	got := pollJobTerminal(t, srv, env.Job.ID)
	if got.Job.Status != string(JobStatusCompleted) || got.Job.Result["session"] != "envelope-target" || got.Job.Result["dry_run"] != true {
		t.Fatalf("job envelope session did not reach spawn: %+v", got.Job)
	}
	select {
	case options := <-opts:
		if options.Session != "envelope-target" || options.CCCount != 2 || !options.DryRun || options.Label != "lane" {
			t.Fatalf("normalization lost launch controls: %+v", options)
		}
	default:
		t.Fatal("job completed without invoking the spawn service")
	}
}

// Conflicting explicit sessions fail admission (docs/jobs-admission.md,
// "Session-aware execution"): the POST is rejected with 400 before a job,
// receipt, or engine call exists, rather than locking the wrong session.
func TestJobSessionConflictFailsBeforeEveryEngineHTTP(t *testing.T) {
	t.Parallel()
	srv := NewHermeticServer("test")
	defer srv.Stop()
	var calls atomic.Int32
	srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) { calls.Add(1); return nil, nil }
	for _, kind := range implementedJobTypes {
		for _, session := range []string{`"different"`, `""`, `null`, `true`, `42`} {
			body := fmt.Sprintf(`{"type":%q,"session":"requested","params":{"session":%s}}`, kind, session)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			want := "session conflict"
			if session == `null` || session == `true` || session == `42` {
				want = "params.session must be a string"
			}
			var resp struct {
				Error     string `json:"error"`
				ErrorCode string `json:"error_code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("%s session=%s: decode response: %v (body=%s)", kind, session, err, rec.Body.String())
			}
			if rec.Code != http.StatusBadRequest || resp.ErrorCode != ErrCodeBadRequest || !strings.Contains(resp.Error, want) {
				t.Fatalf("%s session=%s: POST = %d %s, want 400 %q", kind, session, rec.Code, rec.Body.String(), want)
			}
			if calls.Load() != 0 {
				t.Fatalf("%s reached its engine before resolving the target", kind)
			}
		}
	}
	if jobs := srv.jobStore.List(); len(jobs) != 0 {
		t.Fatalf("rejected session conflicts still created %d job(s): %#v", len(jobs), jobs)
	}
}
