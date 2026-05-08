package evidencebudget

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/faultharness"
)

// Surface fixtures used by the regression suite. Each one mirrors the
// shape of a real operator-visible JSON envelope so tests prove the
// budget contract holds for the surface as deployed, not just for an
// abstract pipeline.
//
// Compose functions are deliberately simple: they pass through
// per-provider raw payloads rather than reimplementing parsing logic
// the real adapters already do. The point of these tests is to prove
// the *evidence pipeline* meets the budget, not to re-test the
// adapters themselves.

// RobotSnapshotSurface mirrors `--robot-snapshot`: it merges mail
// reservations, bv triage, and pipeline state. Mail and bv are
// optional (degraded result still produces a snapshot); the surface
// has no Required providers.
func RobotSnapshotSurface(ctx context.Context, clock faultharness.Clock, mail, bv faultharness.Behavior) Surface {
	return Surface{
		Name:   "robot_snapshot",
		Budget: 5 * time.Second, // 5 simulated seconds
		Providers: []Provider{
			{
				Name: "mail",
				Fetch: func(ctx context.Context) faultharness.Result {
					return faultharness.MailReservationsFake(ctx, clock, mail)
				},
			},
			{
				Name: "bv",
				Fetch: func(ctx context.Context) faultharness.Result {
					return faultharness.BVTriageFake(ctx, clock, bv)
				},
			},
		},
		Compose: func(results map[string]faultharness.Result) (map[string]any, error) {
			env := map[string]any{
				"success":   true,
				"timestamp": clock.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
				"sources":   sourceListFromResults(results),
			}
			// Degrade gracefully: include whatever payload we got.
			if r, ok := results["mail"]; ok && len(r.Payload) > 0 {
				env["mail_reservations"] = json.RawMessage(r.Payload)
			}
			if r, ok := results["bv"]; ok && len(r.Payload) > 0 {
				env["bv_triage"] = json.RawMessage(r.Payload)
			}
			return env, nil
		},
	}
}

// CausalitySurface mirrors `--robot-causality`: merges mail, bv,
// and tmux pane capture. tmux is optional; even without it the
// surface still emits an envelope.
func CausalitySurface(ctx context.Context, clock faultharness.Clock, mail, bv, tmux faultharness.Behavior) Surface {
	return Surface{
		Name:   "robot_causality",
		Budget: 5 * time.Second,
		Providers: []Provider{
			{
				Name: "mail",
				Fetch: func(ctx context.Context) faultharness.Result {
					return faultharness.MailReservationsFake(ctx, clock, mail)
				},
			},
			{
				Name: "bv",
				Fetch: func(ctx context.Context) faultharness.Result {
					return faultharness.BVTriageFake(ctx, clock, bv)
				},
			},
			{
				Name: "tmux",
				Fetch: func(ctx context.Context) faultharness.Result {
					return faultharness.TmuxCaptureFake(ctx, clock, tmux)
				},
			},
		},
		Compose: func(results map[string]faultharness.Result) (map[string]any, error) {
			return map[string]any{
				"success":   true,
				"timestamp": clock.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
				"sources":   sourceListFromResults(results),
				"events":    []any{}, // a real implementation would merge timeline events here
			}, nil
		},
	}
}

// QueueDrySurface mirrors `ntm work queue-dry`: bv is the primary
// data source and Required (without it the diagnostic is meaningless),
// mail is optional.
func QueueDrySurface(ctx context.Context, clock faultharness.Clock, bv, mail faultharness.Behavior) Surface {
	return Surface{
		Name:   "queue_dry",
		Budget: 3 * time.Second, // tighter — interactive
		Providers: []Provider{
			{
				Name:     "bv",
				Required: true,
				Fetch: func(ctx context.Context) faultharness.Result {
					return faultharness.BVTriageFake(ctx, clock, bv)
				},
			},
			{
				Name: "mail",
				Fetch: func(ctx context.Context) faultharness.Result {
					return faultharness.MailReservationsFake(ctx, clock, mail)
				},
			},
		},
		Compose: func(results map[string]faultharness.Result) (map[string]any, error) {
			return map[string]any{
				"success":   true,
				"timestamp": clock.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
				"queue_dry": false,
				"sources":   sourceListFromResults(results),
			}, nil
		},
	}
}

func sourceListFromResults(results map[string]faultharness.Result) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for name, r := range results {
		entry := map[string]any{
			"name":      name,
			"available": r.Err == nil,
		}
		if r.Err != nil {
			entry["error"] = r.Err.Error()
		}
		out = append(out, entry)
	}
	return out
}
