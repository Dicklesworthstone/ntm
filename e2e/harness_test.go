package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewScenarioHarnessCreatesExpectedLayout(t *testing.T) {
	base := t.TempDir()
	fixed := time.Date(2026, 3, 21, 2, 45, 6, 123000000, time.UTC)

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "Attention Feed Harness",
		ArtifactRoot: base,
		RunToken:     "smoke",
		Retain:       RetainAlways,
		Clock:        func() time.Time { return fixed },
		FailureState: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness failed: %v", err)
	}
	t.Cleanup(h.Close)

	wantRoot := filepath.Join(base, "attention-feed-harness", "20260321T024506.123Z-smoke")
	if h.Root() != wantRoot {
		t.Fatalf("root mismatch: got %q want %q", h.Root(), wantRoot)
	}
	if got := h.SessionName(); got != "ntm-e2e-attention-feed-harness-20260321T024506Z-smoke" {
		t.Fatalf("session mismatch: got %q", got)
	}

	for _, kind := range allArtifactKinds {
		info, err := os.Stat(h.Dir(kind))
		if err != nil {
			t.Fatalf("stat %s dir: %v", kind, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s path is not a dir", kind)
		}
	}
}

func TestScenarioHarnessWritesCommandArtifactsAndTimeline(t *testing.T) {
	base := t.TempDir()
	fixed := time.Date(2026, 3, 21, 3, 0, 0, 0, time.UTC)

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "command capture",
		ArtifactRoot: base,
		RunToken:     "case",
		Retain:       RetainAlways,
		Clock:        func() time.Time { return fixed },
		FailureState: func() bool { return false },
		Runner: func(_ context.Context, spec CommandSpec) (CommandResult, error) {
			return CommandResult{
				StartedAt:   fixed,
				CompletedAt: fixed.Add(1500 * time.Millisecond),
				Duration:    1500 * time.Millisecond,
				ExitCode:    0,
				Stdout:      []byte("hello stdout\n"),
				Stderr:      []byte("hello stderr\n"),
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness failed: %v", err)
	}

	result, err := h.RunCommand(CommandSpec{
		Name: "robot-status",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-status"},
	})
	if err != nil {
		t.Fatalf("RunCommand failed: %v", err)
	}
	if result.StdoutPath == "" || result.StderrPath == "" || result.MetadataPath == "" {
		t.Fatalf("expected command artifact paths, got %+v", result)
	}

	if _, err := h.WriteCursorTrace("cursor-trace.json", []byte(`{"cursor":"c-1"}`)); err != nil {
		t.Fatalf("WriteCursorTrace failed: %v", err)
	}
	if _, err := h.WriteTransportCapture("transport.ndjson", []byte("{}\n")); err != nil {
		t.Fatalf("WriteTransportCapture failed: %v", err)
	}
	if _, err := h.WriteRenderedSummary("digest.md", []byte("# Summary\n")); err != nil {
		t.Fatalf("WriteRenderedSummary failed: %v", err)
	}
	if err := h.RecordStep("after-command", map[string]any{"cursor": "c-1", "wake_reason": "manual"}); err != nil {
		t.Fatalf("RecordStep failed: %v", err)
	}
	h.Close()

	stdoutBytes, err := os.ReadFile(result.StdoutPath)
	if err != nil {
		t.Fatalf("read stdout artifact: %v", err)
	}
	if string(stdoutBytes) != "hello stdout\n" {
		t.Fatalf("stdout artifact mismatch: %q", string(stdoutBytes))
	}

	timelineBytes, err := os.ReadFile(filepath.Join(h.Root(), "timeline.jsonl"))
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	timeline := string(timelineBytes)
	if !strings.Contains(timeline, "\"name\":\"robot-status\"") {
		t.Fatalf("timeline missing command entry: %s", timeline)
	}
	if !strings.Contains(timeline, "\"name\":\"after-command\"") {
		t.Fatalf("timeline missing custom step entry: %s", timeline)
	}
	if _, err := os.Stat(filepath.Join(h.Dir(ArtifactTraces), "004-cursor-trace.json")); err != nil {
		t.Fatalf("expected cursor trace artifact: %v", err)
	}
}

func TestScenarioHarnessRetentionPolicy(t *testing.T) {
	fixed := time.Date(2026, 3, 21, 3, 30, 0, 0, time.UTC)

	t.Run("removes successful runs when policy is never", func(t *testing.T) {
		base := t.TempDir()
		h, err := NewScenarioHarness(t, HarnessOptions{
			Scenario:     "cleanup",
			ArtifactRoot: base,
			RunToken:     "gone",
			Retain:       RetainNever,
			Clock:        func() time.Time { return fixed },
			FailureState: func() bool { return false },
		})
		if err != nil {
			t.Fatalf("NewScenarioHarness failed: %v", err)
		}

		root := h.Root()
		h.Close()
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected root to be removed, stat err=%v", err)
		}
	})

	t.Run("retains failed runs on on_failure policy", func(t *testing.T) {
		base := t.TempDir()
		h, err := NewScenarioHarness(t, HarnessOptions{
			Scenario:     "cleanup",
			ArtifactRoot: base,
			RunToken:     "kept",
			Retain:       RetainOnFailure,
			Clock:        func() time.Time { return fixed },
			FailureState: func() bool { return true },
		})
		if err != nil {
			t.Fatalf("NewScenarioHarness failed: %v", err)
		}

		root := h.Root()
		h.Close()
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("expected retained root, stat err=%v", err)
		}
	})
}

func TestAssertOperatorLoopStateWritesDiagnostics(t *testing.T) {
	base := t.TempDir()
	fixed := time.Date(2026, 3, 21, 4, 0, 0, 0, time.UTC)

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "operator loop",
		ArtifactRoot: base,
		RunToken:     "diag",
		Retain:       RetainAlways,
		Clock:        func() time.Time { return fixed },
		FailureState: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness failed: %v", err)
	}
	defer h.Close()

	err = h.AssertOperatorLoopState("missing-fields", OperatorLoopState{
		Degraded: true,
	}, OperatorLoopExpectation{
		RequireCursor:      true,
		RequireWakeReason:  true,
		RequireFocusTarget: true,
		AllowDegraded:      false,
	})
	if err == nil {
		t.Fatal("expected invariant failure")
	}
	msg := err.Error()
	for _, want := range []string{"cursor is required", "wake reason is required", "focus target is required", "degraded marker was not expected"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in error %q", want, msg)
		}
	}

	files, err := filepath.Glob(filepath.Join(h.Dir(ArtifactSummaries), "*-operator-loop.json"))
	if err != nil {
		t.Fatalf("glob summaries: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one operator loop summary, got %d", len(files))
	}
}

func TestSetupTmuxSessionTreatsMissingCleanupAsBenign(t *testing.T) {
	base := t.TempDir()
	fixed := time.Date(2026, 3, 21, 4, 30, 0, 0, time.UTC)
	var seen []string

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "tmux setup",
		ArtifactRoot: base,
		RunToken:     "tmux",
		Retain:       RetainAlways,
		Clock:        func() time.Time { return fixed },
		FailureState: func() bool { return false },
		LookPath: func(file string) (string, error) {
			if file != "tmux" {
				return "", errors.New("unexpected binary")
			}
			return "/usr/bin/tmux", nil
		},
		Runner: func(_ context.Context, spec CommandSpec) (CommandResult, error) {
			seen = append(seen, strings.Join(append([]string{spec.Path}, spec.Args...), " "))
			switch spec.Name {
			case "tmux-new-session":
				return CommandResult{ExitCode: 0}, nil
			case "tmux-kill-session":
				return CommandResult{
					ExitCode: 1,
					Stderr:   []byte("can't find session: already-gone"),
				}, errors.New("exit status 1")
			default:
				return CommandResult{}, errors.New("unexpected command")
			}
		},
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness failed: %v", err)
	}

	if err := h.SetupTmuxSession(TmuxSessionOptions{Width: 120, Height: 40}); err != nil {
		t.Fatalf("SetupTmuxSession failed: %v", err)
	}
	h.Close()

	if len(seen) != 2 {
		t.Fatalf("expected create+cleanup tmux commands, got %d: %v", len(seen), seen)
	}
}

func TestScenarioHarnessOperatorLoopTranscriptArtifacts(t *testing.T) {
	base := t.TempDir()
	fixed := time.Date(2026, 3, 21, 5, 0, 0, 0, time.UTC)

	mustJSON := func(v map[string]any) []byte {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal fake command payload: %v", err)
		}
		return data
	}

	decodeJSON := func(result CommandResult) map[string]any {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal(result.Stdout, &payload); err != nil {
			t.Fatalf("decode %s stdout as JSON: %v\nstdout=%s", result.Name, err, string(result.Stdout))
		}
		return payload
	}

	var snapshotCalls int
	var dashboardCalls int
	var terseCalls int

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "operator loop transcript",
		ArtifactRoot: base,
		RunToken:     "transcript",
		Retain:       RetainAlways,
		Clock:        func() time.Time { return fixed },
		FailureState: func() bool { return false },
		Runner: func(_ context.Context, spec CommandSpec) (CommandResult, error) {
			result := CommandResult{
				StartedAt:   fixed,
				CompletedAt: fixed.Add(25 * time.Millisecond),
				Duration:    25 * time.Millisecond,
				ExitCode:    0,
			}

			command := strings.Join(spec.Args, " ")
			switch {
			case command == "--robot-snapshot":
				snapshotCalls++
				latestCursor := int64(42)
				if snapshotCalls > 1 {
					latestCursor = 44
				}
				result.Stdout = mustJSON(map[string]any{
					"success":       true,
					"latest_cursor": latestCursor,
					"replay_window": map[string]any{
						"oldest_cursor":  40,
						"latest_cursor":  latestCursor,
						"resync_command": "ntm --robot-snapshot",
					},
				})
				return result, nil
			case command == "--robot-dashboard":
				dashboardCalls++
				if dashboardCalls == 1 {
					result.Stdout = []byte("# Dashboard\n## Attention\n_Feed is clear._\n### Suggested Next Steps\n- `ntm --robot-attention --since-cursor=42`\n")
				} else {
					result.Stdout = []byte("# Dashboard\n## Attention\n| Key | Value |\n|---|---|\n| Action Required | 1 |\n### Suggested Next Steps\n- `ntm --robot-tail=proj --panes=2 --lines=50`\n")
				}
				return result, nil
			case command == "--robot-terse":
				terseCalls++
				if terseCalls == 1 {
					result.Stdout = []byte("S:proj|A:1/1|W:0|I:1|E:0|C:0%|B:R1/I0/B0|M:0|^:0|!:0\n")
				} else {
					result.Stdout = []byte("S:proj|A:1/1|W:1|I:0|E:0|C:0%|B:R1/I0/B0|M:1|^:1a|!:1c\n")
				}
				return result, nil
			case command == "--robot-events --since-cursor=42 --limit=20":
				result.Stdout = mustJSON(map[string]any{
					"success": true,
					"events": []any{
						map[string]any{
							"cursor":        43,
							"summary":       "mail requires ack",
							"actionability": "action_required",
						},
					},
					"next_cursor": 43,
				})
				return result, nil
			case command == "--robot-attention --since-cursor=43 --profile=operator":
				result.Stdout = mustJSON(map[string]any{
					"success":       true,
					"wake_reason":   "action_required",
					"focus_targets": []any{"proj:2"},
					"cursor_info": map[string]any{
						"start_cursor": 43,
						"end_cursor":   44,
					},
					"digest": map[string]any{
						"event_count": 1,
						"summary":     "1 action-required event",
					},
				})
				return result, nil
			case command == "--robot-attention --since-cursor=43 --profile=debug":
				result.Stdout = mustJSON(map[string]any{
					"success":       true,
					"wake_reason":   "action_required",
					"wake_reasons":  []any{"action_required", "mail_pending"},
					"focus_targets": []any{"proj:2"},
					"profile":       "debug",
					"cursor_info": map[string]any{
						"start_cursor": 43,
						"end_cursor":   44,
					},
					"digest": map[string]any{
						"event_count": 2,
						"summary":     "2 events including interesting context",
					},
				})
				return result, nil
			case command == "--robot-capabilities":
				result.Stdout = mustJSON(map[string]any{
					"success": true,
					"attention": map[string]any{
						"contract_version": "1.0.0",
						"default_profile":  "operator",
						"features": map[string]any{
							"cursor_replay": map[string]any{
								"status": "available",
								"note":   "Replay uses monotonic cursors with explicit resync instructions on expiration.",
							},
							"operator_boundary": map[string]any{
								"status": "available",
								"note":   "Attention feed remains a sensing/actuation surface only; it does not assign work, infer intent, or replace beads, bv, or Agent Mail.",
							},
						},
					},
				})
				return result, nil
			case command == "--robot-help":
				result.Stdout = []byte("Attention Feed (Operator Loop):\n  --robot-attention      Wait-then-digest (the one obvious tending command)\nIf cursor expires: re-run --robot-snapshot to resync.\nTips for AI Agents:\n- Attention feed is a sensing/actuation surface, not a planner: it does not assign beads, infer intent, or replace beads, bv, or Agent Mail.\n")
				return result, nil
			case command == "--robot-events --since-cursor=1 --limit=20":
				result.ExitCode = 1
				result.Stdout = mustJSON(map[string]any{
					"success":    false,
					"error_code": "CURSOR_EXPIRED",
					"details": map[string]any{
						"requested_cursor": 1,
						"earliest_cursor":  40,
						"resync_command":   "ntm --robot-snapshot",
					},
				})
				return result, errors.New("exit status 1")
			default:
				return CommandResult{}, errors.New("unexpected command: " + command)
			}
		},
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness failed: %v", err)
	}
	defer h.Close()

	bootstrap, err := h.RunCommand(CommandSpec{
		Name: "robot-snapshot-bootstrap",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-snapshot"},
	})
	if err != nil {
		t.Fatalf("bootstrap snapshot failed: %v", err)
	}
	bootstrapPayload := decodeJSON(bootstrap)
	if got := int64(bootstrapPayload["latest_cursor"].(float64)); got != 42 {
		t.Fatalf("bootstrap latest_cursor = %d, want 42", got)
	}
	if err := h.AssertOperatorLoopState("bootstrap", OperatorLoopState{
		Cursor:      "42",
		FocusTarget: "snapshot-bootstrap",
		Details: map[string]any{
			"replay_window": bootstrapPayload["replay_window"],
		},
	}, OperatorLoopExpectation{
		RequireCursor:      true,
		RequireFocusTarget: true,
		AllowDegraded:      true,
	}); err != nil {
		t.Fatalf("bootstrap operator-loop assertion failed: %v", err)
	}
	if err := h.RecordStep("bootstrap_snapshot", map[string]any{
		"latest_cursor": 42,
		"oldest_cursor": 40,
	}); err != nil {
		t.Fatalf("RecordStep bootstrap_snapshot failed: %v", err)
	}

	dashboardQuiet, err := h.RunCommand(CommandSpec{
		Name: "robot-dashboard-quiet",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-dashboard"},
	})
	if err != nil {
		t.Fatalf("quiet dashboard failed: %v", err)
	}
	if _, err := h.WriteRenderedSummary("dashboard-quiet.md", dashboardQuiet.Stdout); err != nil {
		t.Fatalf("WriteRenderedSummary dashboard quiet failed: %v", err)
	}
	if !strings.Contains(string(dashboardQuiet.Stdout), "_Feed is clear._") {
		t.Fatalf("quiet dashboard missing clear marker: %s", string(dashboardQuiet.Stdout))
	}

	terseQuiet, err := h.RunCommand(CommandSpec{
		Name: "robot-terse-quiet",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-terse"},
	})
	if err != nil {
		t.Fatalf("quiet terse failed: %v", err)
	}
	if _, err := h.WriteRenderedSummary("terse-quiet.txt", terseQuiet.Stdout); err != nil {
		t.Fatalf("WriteRenderedSummary terse quiet failed: %v", err)
	}
	if !strings.Contains(string(terseQuiet.Stdout), "|^:0|") {
		t.Fatalf("quiet terse missing attention-clear encoding: %s", string(terseQuiet.Stdout))
	}

	replay, err := h.RunCommand(CommandSpec{
		Name: "robot-events-replay",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-events", "--since-cursor=42", "--limit=20"},
	})
	if err != nil {
		t.Fatalf("events replay failed: %v", err)
	}
	replayPayload := decodeJSON(replay)
	if got := int64(replayPayload["next_cursor"].(float64)); got != 43 {
		t.Fatalf("replay next_cursor = %d, want 43", got)
	}
	if err := h.RecordStep("raw_replay", map[string]any{
		"next_cursor": 43,
		"event_count": len(replayPayload["events"].([]any)),
	}); err != nil {
		t.Fatalf("RecordStep raw_replay failed: %v", err)
	}

	operatorAttention, err := h.RunCommand(CommandSpec{
		Name: "robot-attention-operator",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-attention", "--since-cursor=43", "--profile=operator"},
	})
	if err != nil {
		t.Fatalf("operator attention failed: %v", err)
	}
	operatorPayload := decodeJSON(operatorAttention)
	operatorDigest := operatorPayload["digest"].(map[string]any)
	operatorCursor := operatorPayload["cursor_info"].(map[string]any)
	if got := int64(operatorCursor["end_cursor"].(float64)); got != 44 {
		t.Fatalf("operator end_cursor = %d, want 44", got)
	}
	if err := h.AssertOperatorLoopState("operator-profile", OperatorLoopState{
		Cursor:      "44",
		WakeReason:  "action_required",
		FocusTarget: "proj:2",
		Details: map[string]any{
			"profile": "operator",
			"digest":  operatorDigest,
		},
	}, OperatorLoopExpectation{
		RequireCursor:      true,
		RequireWakeReason:  true,
		RequireFocusTarget: true,
	}); err != nil {
		t.Fatalf("operator profile assertion failed: %v", err)
	}

	debugAttention, err := h.RunCommand(CommandSpec{
		Name: "robot-attention-debug",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-attention", "--since-cursor=43", "--profile=debug"},
	})
	if err != nil {
		t.Fatalf("debug attention failed: %v", err)
	}
	debugPayload := decodeJSON(debugAttention)
	debugDigest := debugPayload["digest"].(map[string]any)
	if debugDigest["event_count"].(float64) <= operatorDigest["event_count"].(float64) {
		t.Fatalf("debug digest should contain more context than operator digest: operator=%v debug=%v", operatorDigest, debugDigest)
	}
	if err := h.RecordStep("profile_compare", map[string]any{
		"operator_event_count": operatorDigest["event_count"],
		"debug_event_count":    debugDigest["event_count"],
		"wake_reasons":         debugPayload["wake_reasons"],
	}); err != nil {
		t.Fatalf("RecordStep profile_compare failed: %v", err)
	}

	dashboardBusy, err := h.RunCommand(CommandSpec{
		Name: "robot-dashboard-busy",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-dashboard"},
	})
	if err != nil {
		t.Fatalf("busy dashboard failed: %v", err)
	}
	if _, err := h.WriteRenderedSummary("dashboard-busy.md", dashboardBusy.Stdout); err != nil {
		t.Fatalf("WriteRenderedSummary dashboard busy failed: %v", err)
	}
	if !strings.Contains(string(dashboardBusy.Stdout), "| Action Required | 1 |") {
		t.Fatalf("busy dashboard missing action-required summary: %s", string(dashboardBusy.Stdout))
	}

	terseBusy, err := h.RunCommand(CommandSpec{
		Name: "robot-terse-busy",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-terse"},
	})
	if err != nil {
		t.Fatalf("busy terse failed: %v", err)
	}
	if _, err := h.WriteRenderedSummary("terse-busy.txt", terseBusy.Stdout); err != nil {
		t.Fatalf("WriteRenderedSummary terse busy failed: %v", err)
	}
	if !strings.Contains(string(terseBusy.Stdout), "|^:1a|") {
		t.Fatalf("busy terse missing action-required encoding: %s", string(terseBusy.Stdout))
	}

	expired, err := h.RunCommand(CommandSpec{
		Name:         "robot-events-expired",
		Path:         "/usr/bin/ntm",
		Args:         []string{"--robot-events", "--since-cursor=1", "--limit=20"},
		AllowFailure: true,
	})
	if err != nil {
		t.Fatalf("expired cursor replay should be allowed, got error: %v", err)
	}
	expiredPayload := decodeJSON(expired)
	if expired.ExitCode != 1 {
		t.Fatalf("expired cursor exit_code = %d, want 1", expired.ExitCode)
	}
	if got := expiredPayload["error_code"]; got != "CURSOR_EXPIRED" {
		t.Fatalf("expired cursor error_code = %v, want CURSOR_EXPIRED", got)
	}
	if err := h.RecordStep("cursor_resync_required", map[string]any{
		"requested_cursor": 1,
		"resync_command":   "ntm --robot-snapshot",
	}); err != nil {
		t.Fatalf("RecordStep cursor_resync_required failed: %v", err)
	}

	tracePayload, err := json.MarshalIndent([]map[string]any{
		{"step": "bootstrap", "cursor": 42},
		{"step": "replay", "cursor": 43},
		{"step": "operator_attention", "cursor": 44, "wake_reason": "action_required"},
		{"step": "expired_cursor", "requested_cursor": 1, "resync_command": "ntm --robot-snapshot"},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal cursor trace: %v", err)
	}
	if _, err := h.WriteCursorTrace("operator-loop-trace.json", append(tracePayload, '\n')); err != nil {
		t.Fatalf("WriteCursorTrace failed: %v", err)
	}
	if _, err := h.WriteTransportCapture("attention-feed.ndjson", []byte("{\"cursor\":43,\"summary\":\"mail requires ack\"}\n")); err != nil {
		t.Fatalf("WriteTransportCapture failed: %v", err)
	}

	resync, err := h.RunCommand(CommandSpec{
		Name: "robot-snapshot-resync",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-snapshot"},
	})
	if err != nil {
		t.Fatalf("resync snapshot failed: %v", err)
	}
	resyncPayload := decodeJSON(resync)
	if got := int64(resyncPayload["latest_cursor"].(float64)); got != 44 {
		t.Fatalf("resync latest_cursor = %d, want 44", got)
	}

	capabilities, err := h.RunCommand(CommandSpec{
		Name: "robot-capabilities",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-capabilities"},
	})
	if err != nil {
		t.Fatalf("capabilities command failed: %v", err)
	}
	if _, err := h.WriteRenderedSummary("capabilities-surface.json", capabilities.Stdout); err != nil {
		t.Fatalf("WriteRenderedSummary capabilities failed: %v", err)
	}
	capsPayload := decodeJSON(capabilities)
	attentionCaps := capsPayload["attention"].(map[string]any)
	features := attentionCaps["features"].(map[string]any)
	if _, ok := features["operator_boundary"]; !ok {
		t.Fatalf("capabilities surface missing operator_boundary feature: %+v", attentionCaps)
	}
	if err := h.RecordStep("capabilities_surface", map[string]any{
		"default_profile": attentionCaps["default_profile"],
		"features":        []string{"cursor_replay", "operator_boundary"},
	}); err != nil {
		t.Fatalf("RecordStep capabilities_surface failed: %v", err)
	}

	helpOut, err := h.RunCommand(CommandSpec{
		Name: "robot-help",
		Path: "/usr/bin/ntm",
		Args: []string{"--robot-help"},
	})
	if err != nil {
		t.Fatalf("help command failed: %v", err)
	}
	if _, err := h.WriteRenderedSummary("robot-help.txt", helpOut.Stdout); err != nil {
		t.Fatalf("WriteRenderedSummary robot-help failed: %v", err)
	}
	helpText := string(helpOut.Stdout)
	for _, want := range []string{
		"Wait-then-digest (the one obvious tending command)",
		"re-run --robot-snapshot to resync",
		"sensing/actuation surface, not a planner",
	} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help output missing %q: %s", want, helpText)
		}
	}
	if err := h.RecordStep("help_surface", map[string]any{
		"guardrail": "not a planner",
		"resync":    true,
	}); err != nil {
		t.Fatalf("RecordStep help_surface failed: %v", err)
	}

	h.Close()

	timelineBytes, err := os.ReadFile(filepath.Join(h.Root(), "timeline.jsonl"))
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	timeline := string(timelineBytes)
	for _, want := range []string{"bootstrap_snapshot", "raw_replay", "profile_compare", "cursor_resync_required", "capabilities_surface", "help_surface"} {
		if !strings.Contains(timeline, want) {
			t.Fatalf("timeline missing %q entry: %s", want, timeline)
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(h.Root(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(manifestBytes), `"failed": false`) {
		t.Fatalf("manifest should record a passing transcript run: %s", string(manifestBytes))
	}

	cursorTraceFiles, err := filepath.Glob(filepath.Join(h.Dir(ArtifactTraces), "*-operator-loop-trace.json"))
	if err != nil {
		t.Fatalf("glob cursor trace artifacts: %v", err)
	}
	if len(cursorTraceFiles) != 1 {
		t.Fatalf("expected one operator-loop trace artifact, got %d", len(cursorTraceFiles))
	}
	cursorTraceBytes, err := os.ReadFile(cursorTraceFiles[0])
	if err != nil {
		t.Fatalf("read cursor trace artifact: %v", err)
	}
	if !strings.Contains(string(cursorTraceBytes), "\"resync_command\": \"ntm --robot-snapshot\"") {
		t.Fatalf("cursor trace missing resync command: %s", string(cursorTraceBytes))
	}

	busyDashboardFiles, err := filepath.Glob(filepath.Join(h.Dir(ArtifactSummaries), "*-dashboard-busy.md"))
	if err != nil {
		t.Fatalf("glob busy dashboard artifacts: %v", err)
	}
	if len(busyDashboardFiles) != 1 {
		t.Fatalf("expected one busy dashboard artifact, got %d", len(busyDashboardFiles))
	}
	busyDashboardBytes, err := os.ReadFile(busyDashboardFiles[0])
	if err != nil {
		t.Fatalf("read busy dashboard artifact: %v", err)
	}
	if !strings.Contains(string(busyDashboardBytes), "| Action Required | 1 |") {
		t.Fatalf("busy dashboard artifact missing attention summary: %s", string(busyDashboardBytes))
	}

	helpFiles, err := filepath.Glob(filepath.Join(h.Dir(ArtifactSummaries), "*-robot-help.txt"))
	if err != nil {
		t.Fatalf("glob help artifacts: %v", err)
	}
	if len(helpFiles) != 1 {
		t.Fatalf("expected one robot-help artifact, got %d", len(helpFiles))
	}
	helpBytes, err := os.ReadFile(helpFiles[0])
	if err != nil {
		t.Fatalf("read help artifact: %v", err)
	}
	if !strings.Contains(string(helpBytes), "not a planner") {
		t.Fatalf("help artifact missing operator boundary guardrail: %s", string(helpBytes))
	}

	capsFiles, err := filepath.Glob(filepath.Join(h.Dir(ArtifactSummaries), "*-capabilities-surface.json"))
	if err != nil {
		t.Fatalf("glob capabilities artifacts: %v", err)
	}
	if len(capsFiles) != 1 {
		t.Fatalf("expected one capabilities artifact, got %d", len(capsFiles))
	}
	capsBytes, err := os.ReadFile(capsFiles[0])
	if err != nil {
		t.Fatalf("read capabilities artifact: %v", err)
	}
	if !strings.Contains(string(capsBytes), "\"operator_boundary\"") {
		t.Fatalf("capabilities artifact missing operator boundary feature: %s", string(capsBytes))
	}
}
