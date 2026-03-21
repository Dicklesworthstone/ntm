package e2e

import (
	"context"
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
