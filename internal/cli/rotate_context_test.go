package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type fakeContextPaneSpawner struct {
	panes       []tmux.Pane
	spawnedID   string
	spawnCalls  int
	killedPanes []string
	sentKeys    []string
	sentBuffers []string
}

func (f *fakeContextPaneSpawner) SpawnAgent(session, agentType string, index int, variant string, workDir string) (string, error) {
	f.spawnCalls++
	if f.spawnedID == "" {
		f.spawnedID = "%2"
	}
	return f.spawnedID, nil
}

func (f *fakeContextPaneSpawner) KillPane(paneID string) error {
	f.killedPanes = append(f.killedPanes, paneID)
	return nil
}

func (f *fakeContextPaneSpawner) SendKeys(paneID, text string, enter bool) error {
	f.sentKeys = append(f.sentKeys, paneID+":"+text)
	return nil
}

func (f *fakeContextPaneSpawner) SendBuffer(paneID, text string, enter bool) error {
	f.sentBuffers = append(f.sentBuffers, paneID+":"+text)
	return nil
}

func (f *fakeContextPaneSpawner) GetPanes(session string) ([]tmux.Pane, error) {
	out := make([]tmux.Pane, len(f.panes))
	copy(out, f.panes)
	return out, nil
}

func isolateContextRotationStores(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("NTM_DIR", filepath.Join(root, ".ntm"))

	oldPending := ctxmon.DefaultPendingRotationStore
	oldHistory := ctxmon.DefaultRotationHistoryStore
	oldJSON := jsonOutput

	pendingPath := filepath.Join(root, "pending.jsonl")
	ctxmon.DefaultPendingRotationStore = ctxmon.NewPendingRotationStoreWithPath(pendingPath)
	ctxmon.DefaultRotationHistoryStore = ctxmon.NewRotationHistoryStore()
	jsonOutput = false

	t.Cleanup(func() {
		ctxmon.DefaultPendingRotationStore = oldPending
		ctxmon.DefaultRotationHistoryStore = oldHistory
		jsonOutput = oldJSON
	})

	return pendingPath
}

func TestRotateContextClearPending_ClearsAndRefusesUnresolved(t *testing.T) {
	pendingPath := isolateContextRotationStores(t)

	now := time.Now()
	if err := ctxmon.AddPendingRotation(&ctxmon.PendingRotation{
		AgentID:        "demo__cc_1",
		SessionName:    "demo",
		PaneID:         "%1",
		ContextPercent: 84.0,
		CreatedAt:      now,
		TimeoutAt:      now.Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("AddPendingRotation(demo__cc_1): %v", err)
	}
	if err := ctxmon.AddPendingRotation(&ctxmon.PendingRotation{
		AgentID:        "demo__cod_2",
		SessionName:    "demo",
		PaneID:         "%2",
		ContextPercent: 88.0,
		CreatedAt:      now,
		TimeoutAt:      now.Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("AddPendingRotation(demo__cod_2): %v", err)
	}

	var out bytes.Buffer
	cmd := newRotateContextCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"clear", "--pending", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("clear --pending --force failed: %v", err)
	}
	if !strings.Contains(out.String(), "Cleared pending rotation store (2 queued entries)") {
		t.Fatalf("unexpected clear output: %q", out.String())
	}
	if _, statErr := os.Stat(pendingPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected pending store file to be removed, stat err=%v", statErr)
	}

	// Now verify refusal when an in-progress confirmation exists.
	if err := ctxmon.AddPendingRotation(&ctxmon.PendingRotation{
		AgentID:        "demo__cc_3",
		SessionName:    "demo",
		PaneID:         "%3",
		ContextPercent: 91.0,
		CreatedAt:      now,
		TimeoutAt:      now.Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("AddPendingRotation(demo__cc_3): %v", err)
	}
	_, release, err := ctxmon.DefaultPendingRotationStore.BeginConfirmation(context.Background(), "demo__cc_3", ctxmon.ConfirmRotate, false)
	if err != nil {
		t.Fatalf("BeginConfirmation: %v", err)
	}
	release()

	out.Reset()
	refuseCmd := newRotateContextCmd()
	refuseCmd.SetOut(&out)
	refuseCmd.SetErr(&out)
	refuseCmd.SetArgs([]string{"clear", "--pending", "--force"})
	err = refuseCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cannot clear unresolved confirmation for demo__cc_3") {
		t.Fatalf("expected unresolved confirmation refusal, got err=%v", err)
	}
}

func TestRotateContextCancelCmd_CancelsQueuedAndReportsRemaining(t *testing.T) {
	isolateContextRotationStores(t)

	now := time.Now()
	for _, id := range []string{"demo__cc_1", "demo__cc_2"} {
		if err := ctxmon.AddPendingRotation(&ctxmon.PendingRotation{
			AgentID:        id,
			SessionName:    "demo",
			PaneID:         "%1",
			ContextPercent: 82.5,
			CreatedAt:      now,
			TimeoutAt:      now.Add(15 * time.Minute),
		}); err != nil {
			t.Fatalf("AddPendingRotation(%s): %v", id, err)
		}
	}

	jsonOutput = true
	var out bytes.Buffer
	cmd := newRotateContextCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cancel", "demo__cc_1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cancel demo__cc_1 failed: %v", err)
	}

	var parsed CancelRotationResult
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("decode CancelRotationResult: %v (raw=%q)", err, out.String())
	}
	if !parsed.Success || !parsed.Cancelled || parsed.RemainingPending != 1 {
		t.Fatalf("unexpected CancelRotationResult: %+v", parsed)
	}
	if gone, err := ctxmon.GetPendingRotationByID("demo__cc_1"); err != nil || gone != nil {
		t.Fatalf("expected demo__cc_1 removed from store, got entry=%+v err=%v", gone, err)
	}

	// Missing agent returns an error.
	jsonOutput = false
	out.Reset()
	missingCmd := newRotateContextCmd()
	missingCmd.SetOut(&out)
	missingCmd.SetErr(&out)
	missingCmd.SetArgs([]string{"cancel", "demo__cc_99"})
	if err := missingCmd.Execute(); err == nil || !strings.Contains(err.Error(), "no pending rotation found") {
		t.Fatalf("expected missing pending error, got %v", err)
	}

	// Claiming demo__cc_2 into in_progress state blocks naive cancel and directs operator to recover.
	_, release, err := ctxmon.DefaultPendingRotationStore.BeginConfirmation(context.Background(), "demo__cc_2", ctxmon.ConfirmRotate, false)
	if err != nil {
		t.Fatalf("BeginConfirmation(demo__cc_2): %v", err)
	}
	release()

	out.Reset()
	inFlightCmd := newRotateContextCmd()
	inFlightCmd.SetOut(&out)
	inFlightCmd.SetErr(&out)
	inFlightCmd.SetArgs([]string{"cancel", "demo__cc_2"})
	if err := inFlightCmd.Execute(); err == nil || !strings.Contains(err.Error(), "unresolved confirmation") {
		t.Fatalf("expected unresolved confirmation refusal, got %v", err)
	}
	if !strings.Contains(out.String(), "ntm rotate context recover") {
		t.Fatalf("expected recovery guidance in output, got %q", out.String())
	}
}

func TestRotateContextManualCmd_CompactionSkipAndFullManualRotate(t *testing.T) {
	isolateContextRotationStores(t)
	workDir := t.TempDir()

	t.Run("compaction_reduces_below_threshold_skips_rotation", func(t *testing.T) {
		spawner := &fakeContextPaneSpawner{
			panes: []tmux.Pane{
				{ID: "%1", Index: 1, Title: "demo__cc_1", Type: tmux.AgentClaude},
			},
		}
		deps := rotateContextManualDeps{
			resolve:    func(_ context.Context, name string) (string, error) { return name, nil },
			projectDir: func(_ context.Context, _ string) (string, error) { return workDir, nil },
			spawner:    spawner,
			seedMonitor: func(_ context.Context, monitor *ctxmon.ContextMonitor, session, agentID string) (string, string, error) {
				monitor.RegisterAgentWithTranscript(agentID, "%1", "claude-sonnet-4", "claude", session, "")
				monitor.UpdateFromRobotMode(agentID, `status line {"context_used": 170000, "context_limit": 200000}`)
				return "%1", "claude", nil
			},
			refreshAfterCompact: func(_ context.Context, monitor *ctxmon.ContextMonitor, _, agentID, _ string) {
				monitor.UpdateFromRobotMode(agentID, `status line {"context_used": 90000, "context_limit": 200000}`)
			},
		}

		jsonOutput = false
		var out bytes.Buffer
		cmd := newRotateContextManualCmdWithDeps(&deps)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"demo", "demo__cc_1", "--compact-first", "--if-needed"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("manual --compact-first --if-needed failed: %v", err)
		}

		rendered := out.String()
		for _, want := range []string{
			"✓ Compaction successful",
			"Usage: 85.0% → 45.0%",
			"Pre-Rotation Check: rotate=false",
			"Replacement rotation skipped for demo__cc_1",
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("expected output to contain %q, got:\n%s", want, rendered)
			}
		}
		if spawner.spawnCalls != 0 {
			t.Fatalf("expected 0 replacement spawns when compaction succeeded, got %d", spawner.spawnCalls)
		}
		if len(spawner.sentKeys) != 1 || spawner.sentKeys[0] != "%1:/compact" {
			t.Fatalf("expected builtin /compact sent to %%1, got %v", spawner.sentKeys)
		}
	})

	t.Run("full_manual_rotate_records_history_and_formats_display", func(t *testing.T) {
		spawner := &fakeContextPaneSpawner{
			panes: []tmux.Pane{
				{ID: "%1", Index: 1, Title: "demo__cc_1", Type: tmux.AgentClaude},
			},
			spawnedID: "%9",
		}
		deps := rotateContextManualDeps{
			resolve:    func(_ context.Context, name string) (string, error) { return name, nil },
			projectDir: func(_ context.Context, _ string) (string, error) { return workDir, nil },
			spawner:    spawner,
			seedMonitor: func(_ context.Context, monitor *ctxmon.ContextMonitor, session, agentID string) (string, string, error) {
				monitor.RegisterAgentWithTranscript(agentID, "%1", "claude-sonnet-4", "claude", session, "")
				monitor.UpdateFromRobotMode(agentID, `status line {"context_used": 176000, "context_limit": 200000}`)
				return "%1", "claude", nil
			},
		}

		jsonOutput = false
		var out bytes.Buffer
		cmd := newRotateContextManualCmdWithDeps(&deps)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"demo", "demo__cc_1"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("manual rotate failed: %v", err)
		}

		rendered := out.String()
		for _, want := range []string{
			"✓ Rotation completed",
			"Old Agent: demo__cc_1",
			"Method: manual",
			"State: completed",
			"Recorded history event: demo__cc_1 → demo__cc_1 (88.0% → 0.0%)",
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("expected output to contain %q, got:\n%s", want, rendered)
			}
		}
		if spawner.spawnCalls != 1 {
			t.Fatalf("expected 1 replacement spawn, got %d", spawner.spawnCalls)
		}
		if len(spawner.killedPanes) != 1 || spawner.killedPanes[0] != "%1" {
			t.Fatalf("expected old pane %%1 killed, got %v", spawner.killedPanes)
		}
		records, err := ctxmon.DefaultRotationHistoryStore.ReadRecent(10)
		if err != nil {
			t.Fatalf("ReadRecent: %v", err)
		}
		if len(records) != 1 || !records[0].Success || records[0].Method != ctxmon.RotationManual {
			t.Fatalf("expected 1 persisted manual rotation record, got %+v", records)
		}
	})
}
