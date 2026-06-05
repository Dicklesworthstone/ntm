package faultharness

import (
	"context"
	"encoding/json"
)

// Per-provider thin wrappers. Each function takes a Behavior and
// returns the same Result shape as Apply, but with a "healthy"
// payload pre-built in the canonical JSON shape that the matching
// real adapter would emit. Tests of operator surfaces (--robot-*,
// `ntm work *`, dashboard) can use these as drop-in fakes for the
// degraded-source paths without re-deriving the JSON each time.

// MailReservationsHealthy is the canonical "two reservations" healthy
// payload: shape matches []agentmail.FileReservation.
func MailReservationsHealthy() []byte {
	type res struct {
		ID          int    `json:"id"`
		PathPattern string `json:"path_pattern"`
		AgentName   string `json:"agent_name"`
		Exclusive   bool   `json:"exclusive"`
	}
	body, _ := json.Marshal([]res{
		{ID: 1, PathPattern: "internal/auth/**", AgentName: "Alice", Exclusive: true},
		{ID: 2, PathPattern: "internal/billing/**", AgentName: "Bob", Exclusive: false},
	})
	return body
}

// MailReservationsFake simulates an agentmail.ListReservations call
// under the configured Behavior.
func MailReservationsFake(ctx context.Context, clock Clock, b Behavior) Result {
	return Apply(ctx, clock, b, MailReservationsHealthy())
}

// BVTriageHealthy is the canonical "two recommendations" payload
// shaped like a bv --robot-triage response.
func BVTriageHealthy() []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"quick_ref": map[string]int{
			"open_count":        2,
			"actionable_count":  2,
			"blocked_count":     0,
			"in_progress_count": 0,
		},
		"recommendations": []map[string]string{
			{"id": "bd-1", "summary": "first"},
			{"id": "bd-2", "summary": "second"},
		},
	})
	return body
}

// BVTriageFake simulates a bv triage call under Behavior.
func BVTriageFake(ctx context.Context, clock Clock, b Behavior) Result {
	return Apply(ctx, clock, b, BVTriageHealthy())
}

// CASSSearchHealthy is the canonical "one hit" cass search payload.
func CASSSearchHealthy() []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"hits": []map[string]interface{}{
			{
				"score":       0.92,
				"source_path": "/data/projects/ntm/internal/foo.go",
				"line_number": 42,
				"snippet":     "TODO: handle nil case",
				"session_id":  "cc-2026-05-08",
			},
		},
	})
	return body
}

// CASSSearchFake simulates a cass search under Behavior.
func CASSSearchFake(ctx context.Context, clock Clock, b Behavior) Result {
	return Apply(ctx, clock, b, CASSSearchHealthy())
}

// TmuxCaptureHealthy is the canonical multi-line tmux capture-pane
// output (raw text, not JSON — the tmux provider returns bytes).
func TmuxCaptureHealthy() []byte {
	return []byte("alice@host:~/proj$ ls\nfile1.go file2.go\nclaude>\n")
}

// TmuxCaptureFake simulates a tmux capture-pane call under Behavior.
func TmuxCaptureFake(ctx context.Context, clock Clock, b Behavior) Result {
	return Apply(ctx, clock, b, TmuxCaptureHealthy())
}
