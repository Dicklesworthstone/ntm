package serve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestJobSwarmSpawnDeliveryOptions(t *testing.T) {
	const prompt = "  Review the design.\nThen implement it — preserve this text.\n"
	for _, tc := range []struct {
		name   string
		params map[string]interface{}
		mode   string
		delay  time.Duration
	}{
		{"prompt", nil, "", 0},
		{"assignment_prefix", map[string]interface{}{"assign_work": true}, "", 0},
		{"preview", map[string]interface{}{"dry_run": true}, "", 0},
		{"fixed_default", map[string]interface{}{"stagger_mode": "fixed"}, "fixed", config.DefaultSpawnStaggerDelay},
		{"fixed_explicit", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "1250ms"}, "fixed", 1250 * time.Millisecond},
		{"fixed_zero", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "0s"}, "fixed", 0},
		{"fixed_maximum", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "5m"}, "fixed", config.MaxSpawnStaggerDelay},
		{"smart", map[string]interface{}{"stagger_mode": "smart"}, "smart", 0},
		{"explicit_none", map[string]interface{}{"stagger_mode": "none"}, "none", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{"session": "delivery", "cc_count": 1, "prompt": prompt}
			for key, value := range tc.params {
				params[key] = value
			}
			calls := 0
			srv := &Server{spawnAgents: func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls++
				if opts.Prompt != prompt || opts.StaggerMode != tc.mode || opts.StaggerDelay != tc.delay {
					t.Errorf("delivery = (%q, %q, %s), want (%q, %q, %s)", opts.Prompt, opts.StaggerMode, opts.StaggerDelay, prompt, tc.mode, tc.delay)
				}
				if opts.AssignWork != (params["assign_work"] == true) || opts.DryRun != (params["dry_run"] == true) {
					t.Error("delivery changed assignment or preview semantics")
				}
				return &robot.SpawnOutput{RobotResponse: robot.RobotResponse{Success: true}, Session: opts.Session}, nil
			}}
			if _, err := srv.jobSwarmSpawn(context.Background(), params); err != nil || calls != 1 {
				t.Fatalf("jobSwarmSpawn: calls=%d, err=%v", calls, err)
			}
		})
	}
}

func TestJobSwarmSpawnRejectsInvalidDeliveryBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]interface{}
		want   string
	}{
		{"unknown_mode", map[string]interface{}{"stagger_mode": "slow"}, "stagger_mode"},
		{"negative_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "-1s"}, "stagger_delay"},
		{"excessive_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "5m1ns"}, "stagger_delay"},
		{"overflow_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "999999999999999999999h"}, "stagger_delay"},
		{"empty_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": ""}, "stagger_delay"},
		{"numeric_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": 20}, "stagger_delay"},
		{"missing_mode", map[string]interface{}{"stagger_delay": "20s"}, "requires stagger_mode=fixed"},
		{"smart_fixed_delay", map[string]interface{}{"stagger_mode": "smart", "stagger_delay": "20s"}, "requires stagger_mode=fixed"},
		{"missing_work", map[string]interface{}{"stagger_mode": "fixed", "prompt": " \n"}, "requires a non-blank prompt"},
		{"numeric_prompt", map[string]interface{}{"prompt": 42}, "prompt"},
		{"unknown_prompt_option", map[string]interface{}{"prompt_file": "/tmp/private"}, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{"session": "delivery", "cc_count": 1, "prompt": "Do the work"}
			for key, value := range tc.params {
				params[key] = value
			}
			calls := 0
			srv := &Server{spawnAgents: func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls++
				return nil, errors.New("must not launch")
			}}
			_, err := srv.jobSwarmSpawn(context.Background(), params)
			if calls != 0 || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("calls=%d, err=%v, want preflight error containing %q", calls, err, tc.want)
			}
		})
	}
}

func TestJobSwarmSpawnAssignmentCanStaggerWithoutInitialPrompt(t *testing.T) {
	srv := &Server{spawnAgents: func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		if opts.Prompt != "" || !opts.AssignWork || opts.StaggerMode != "smart" {
			t.Errorf("assignment delivery options were lost: %+v", opts)
		}
		return &robot.SpawnOutput{RobotResponse: robot.RobotResponse{Success: true}}, nil
	}}
	_, err := srv.jobSwarmSpawn(context.Background(), map[string]interface{}{
		"session": "delivery", "cc_count": 1, "assign_work": true, "stagger_mode": "smart",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobSwarmSpawnDeliveryRetainsPartialFailure(t *testing.T) {
	failure := errors.New("second pane refused prompt delivery")
	srv := &Server{spawnAgents: func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		return &robot.SpawnOutput{
			RobotResponse: robot.RobotResponse{Success: false}, Session: opts.Session,
			Agents: []robot.SpawnedAgent{{Pane: "0.1", PaneID: "%17", Type: "cc"}},
		}, failure
	}}
	result, err := srv.jobSwarmSpawn(context.Background(), map[string]interface{}{
		"session": "delivery", "cc_count": 2, "prompt": "Do the work", "stagger_mode": "fixed",
	})
	if !errors.Is(err, failure) || result["session"] != "delivery" || result["success"] != false {
		t.Fatalf("lost partial failure: result=%v, err=%v", result, err)
	}
	agents, ok := result["agents"].([]interface{})
	if !ok || len(agents) != 1 || agents[0].(map[string]interface{})["pane_id"] != "%17" {
		t.Fatalf("lost created pane identity: %v", result)
	}
}

func TestJobSwarmSpawnDeliveryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := &Server{spawnAgents: func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
		t.Error("cancelled job launched agents")
		return nil, nil
	}}
	_, err := srv.jobSwarmSpawn(ctx, map[string]interface{}{"session": "delivery", "cc_count": 1, "prompt": "Do the work"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context cancellation", err)
	}
}
