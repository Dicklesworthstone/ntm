package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// Exercise the real admission/dispatch route, not a scheduler assembled only
// by the test. The spawn seam replaces tmux, but request binding, job receipts,
// queued cancellation, cross-operation resources, and HTTP inspection are real.
func TestJobResourceAdmissionHTTPSerializesActualSessionTargets(t *testing.T) {
	for _, tc := range []struct {
		name, target, queuedBody string
	}{
		{"same session", "project", `{"type":"swarm_spawn","session":"project","params":{"cc_count":1}}`},
		{"labelled spawn", "project--dev", `{"type":"swarm_spawn","params":{"session":"project","label":"dev","cc_count":1}}`},
		{"restore target", "project", `{"type":"checkpoint_restore","params":{"session":"archive-source","target_session":"project","checkpoint_id":"saved","force":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewHermeticServer("resource-admission-test")
			t.Cleanup(func() { srv.Stop() })
			srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 3, 4
			gate, _ := resourceGate(t)
			events := make(chan string, 8)
			srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				target := opts.Session
				if opts.Label != "" {
					target += "--" + opts.Label
				}
				events <- target
				out := &robot.SpawnOutput{Session: target}
				select {
				case <-ctx.Done():
					return out, ctx.Err()
				case <-gate:
					out.Success = true
					return out, nil
				}
			}
			body, err := json.Marshal(map[string]interface{}{
				"type": JobTypeSwarmSpawn, "params": map[string]interface{}{"session": tc.target, "cc_count": 1},
			})
			if err != nil {
				t.Fatal(err)
			}
			admissionAcceptedJob(t, srv, string(body))
			resourceWait(t, events, tc.target)
			queued := admissionAcceptedJob(t, srv, tc.queuedBody)
			admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"independent","cc_count":1}}`)
			resourceWait(t, events, "independent")
			if job := srv.jobStore.Get(queued.ID); job == nil || job.Status != JobStatusPending {
				t.Fatalf("conflicting operation reached dispatch: %+v", job)
			}
			rec := admissionRequest(srv, http.MethodGet, "/api/v1/jobs", "")
			var listing struct {
				Execution JobExecutionStatus `json:"execution"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listing) != nil {
				t.Fatalf("list jobs: %d %s", rec.Code, rec.Body.String())
			}
			if listing.Execution.Running != 2 || listing.Execution.Queued != 1 || listing.Execution.WaitingForResources != 1 {
				t.Fatalf("resource wait hidden from HTTP clients: %+v", listing.Execution)
			}
			rec = admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("cancel resource waiter: %d %s", rec.Code, rec.Body.String())
			}
			if got := srv.jobStore.Get(queued.ID); got == nil || got.Status != JobStatusCancelled {
				t.Fatalf("resource waiter not cancelled: %+v", got)
			}
		})
	}
}

func TestJobResourceAdmissionHTTPRejectsConflictingSessionBinding(t *testing.T) {
	srv := NewHermeticServer("resource-admission-test")
	defer srv.Stop()
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs", `{"type":"swarm_spawn","session":"outer","params":{"session":"inner","cc_count":1}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous resource binding accepted: %d %s", rec.Code, rec.Body.String())
	}
	if len(srv.jobStore.List()) != 0 {
		t.Fatal("ambiguous resource binding created a receipt")
	}
}
