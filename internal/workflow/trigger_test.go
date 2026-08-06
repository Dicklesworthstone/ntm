package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTriggerRegistryCreatesStandardTriggers(t *testing.T) {
	registry := NewTriggerRegistry()
	cases := []Trigger{
		{Type: TriggerFileCreated, Pattern: "*_test.go"},
		{Type: TriggerFileModified, Pattern: "*.go"},
		{Type: TriggerCommandSuccess, Command: "true"},
		{Type: TriggerCommandFailure, Command: "false"},
		{Type: TriggerAgentSays, Pattern: "done"},
		{Type: TriggerAllAgentsIdle, IdleMinutes: 1},
		{Type: TriggerManual},
		{Type: TriggerTimeElapsed, Minutes: 1},
	}
	for _, config := range cases {
		t.Run(string(config.Type), func(t *testing.T) {
			trigger, err := registry.Create(config)
			if err != nil {
				t.Fatalf("Create(%s): %v", config.Type, err)
			}
			if trigger.Type() != config.Type {
				t.Errorf("Type() = %q, want %q", trigger.Type(), config.Type)
			}
		})
	}
}

func TestTriggerRegistryRejectsMissingFactory(t *testing.T) {
	registry := NewTriggerRegistry()
	registry.Register(TriggerManual, nil)
	if _, err := registry.Create(Trigger{Type: TriggerManual}); err == nil {
		t.Fatal("Create() succeeded without a registered factory")
	}
}

func TestFileCreatedTrigger(t *testing.T) {
	dir := t.TempDir()
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerFileCreated, Pattern: "*_test.go"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	t.Cleanup(func() {
		if err := trigger.Stop(); err != nil {
			t.Errorf("Stop(): %v", err)
		}
	})
	ctx := &TriggerContext{ProjectRoot: dir}
	if err := trigger.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if fired, err := trigger.Check(ctx); err != nil || fired {
		t.Fatalf("Check before create = (%v, %v), want (false, nil)", fired, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "created_test.go"), []byte("package example\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	waitForTrigger(t, trigger, ctx)
}

func TestFileModifiedTrigger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.go")
	if err := os.WriteFile(path, []byte("package example\n"), 0o644); err != nil {
		t.Fatalf("initial WriteFile(): %v", err)
	}
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerFileModified, Pattern: "*.go"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	t.Cleanup(func() {
		if err := trigger.Stop(); err != nil {
			t.Errorf("Stop(): %v", err)
		}
	})
	ctx := &TriggerContext{ProjectRoot: dir}
	if err := trigger.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if err := os.WriteFile(path, []byte("package example\n// updated\n"), 0o644); err != nil {
		t.Fatalf("updated WriteFile(): %v", err)
	}
	waitForTrigger(t, trigger, ctx)
}

func TestFileCreatedTriggerWatchesExistingNestedDirectories(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "internal", "workflow")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerFileCreated, Pattern: "internal/**/*.go"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	t.Cleanup(func() {
		if err := trigger.Stop(); err != nil {
			t.Errorf("Stop(): %v", err)
		}
	})
	ctx := &TriggerContext{ProjectRoot: dir}
	if err := trigger.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "trigger.go"), []byte("package workflow\n"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	waitForTrigger(t, trigger, ctx)
}

func TestFileModifiedTriggerWatchesNewNestedDirectories(t *testing.T) {
	dir := t.TempDir()
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerFileModified, Pattern: "internal/**/*.go"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	t.Cleanup(func() {
		if err := trigger.Stop(); err != nil {
			t.Errorf("Stop(): %v", err)
		}
	})
	ctx := &TriggerContext{ProjectRoot: dir}
	if err := trigger.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	nested := filepath.Join(dir, "internal", "workflow")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	waitForDirectoryWatch(t, trigger, nested)
	path := filepath.Join(nested, "trigger.go")
	if err := os.WriteFile(path, []byte("package workflow\n"), 0o644); err != nil {
		t.Fatalf("create nested file: %v", err)
	}
	if err := os.WriteFile(path, []byte("package workflow\n// modified\n"), 0o644); err != nil {
		t.Fatalf("modify nested file: %v", err)
	}
	waitForTrigger(t, trigger, ctx)
}

func TestCommandTriggers(t *testing.T) {
	registry := NewTriggerRegistry()
	ctx := &TriggerContext{Context: context.Background(), ProjectRoot: t.TempDir()}
	success, err := registry.Create(Trigger{Type: TriggerCommandSuccess, Command: "true"})
	if err != nil {
		t.Fatalf("Create success: %v", err)
	}
	if fired, err := success.Check(ctx); err != nil || !fired {
		t.Fatalf("success trigger = (%v, %v), want (true, nil)", fired, err)
	}

	failure, err := registry.Create(Trigger{Type: TriggerCommandFailure, Command: "false"})
	if err != nil {
		t.Fatalf("Create failure: %v", err)
	}
	if fired, err := failure.Check(ctx); err != nil || !fired {
		t.Fatalf("failure trigger = (%v, %v), want (true, nil)", fired, err)
	}
	if fired, err := success.Check(&TriggerContext{ProjectRoot: ""}); err == nil || fired {
		t.Fatalf("missing project root = (%v, %v), want (false, error)", fired, err)
	}
}

func TestCommandFailureTriggerRejectsCommandLaunchErrors(t *testing.T) {
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerCommandFailure, Command: "ntm-command-that-does-not-exist"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	fired, err := trigger.Check(&TriggerContext{Context: context.Background(), ProjectRoot: t.TempDir()})
	if err == nil || fired {
		t.Fatalf("Check() for unavailable executable = (%v, %v), want (false, error)", fired, err)
	}
}

func TestAgentSaysTriggerHonorsRole(t *testing.T) {
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerAgentSays, Pattern: "(?i)approved", Role: "reviewer"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	ctx := &TriggerContext{Outputs: []AgentOutput{
		{Role: "author", Text: "approved"},
		{Role: "reviewer", Text: "needs work"},
	}}
	if fired, err := trigger.Check(ctx); err != nil || fired {
		t.Fatalf("Check non-matching role output = (%v, %v), want (false, nil)", fired, err)
	}
	ctx.Outputs[1].Text = "Approved after review"
	if fired, err := trigger.Check(ctx); err != nil || !fired {
		t.Fatalf("Check matching role output = (%v, %v), want (true, nil)", fired, err)
	}
}

func TestAllAgentsIdleTrigger(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerAllAgentsIdle, Role: "reviewer", IdleMinutes: 5})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	ctx := &TriggerContext{Now: func() time.Time { return now }, Activities: []AgentActivity{
		{Role: "reviewer", LastActivity: now.Add(-6 * time.Minute)},
		{Role: "reviewer", LastActivity: now.Add(-5 * time.Minute)},
		{Role: "author", LastActivity: now},
	}}
	if fired, err := trigger.Check(ctx); err != nil || !fired {
		t.Fatalf("all reviewers idle = (%v, %v), want (true, nil)", fired, err)
	}
	ctx.Activities[1].LastActivity = now.Add(-time.Minute)
	if fired, err := trigger.Check(ctx); err != nil || fired {
		t.Fatalf("active reviewer = (%v, %v), want (false, nil)", fired, err)
	}
}

func TestManualTriggerCoalescesAndConsumes(t *testing.T) {
	runtimeTrigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerManual, Label: "Approve"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	trigger, ok := runtimeTrigger.(*ManualTrigger)
	if !ok {
		t.Fatalf("manual trigger type = %T", runtimeTrigger)
	}
	if trigger.Label() != "Approve" {
		t.Errorf("Label() = %q, want Approve", trigger.Label())
	}
	if !trigger.Fire() || trigger.Fire() {
		t.Fatal("Fire() should enqueue one request and coalesce the next")
	}
	if fired, err := trigger.Check(nil); err != nil || !fired {
		t.Fatalf("first Check() = (%v, %v), want (true, nil)", fired, err)
	}
	if fired, err := trigger.Check(nil); err != nil || fired {
		t.Fatalf("second Check() = (%v, %v), want (false, nil)", fired, err)
	}
}

func TestTimeElapsedTrigger(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	trigger, err := NewTriggerRegistry().Create(Trigger{Type: TriggerTimeElapsed, Minutes: 2})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	ctx := &TriggerContext{Now: func() time.Time { return now }}
	if fired, err := trigger.Check(ctx); err == nil || fired {
		t.Fatalf("Check before Start() = (%v, %v), want (false, error)", fired, err)
	}
	if err := trigger.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	now = now.Add(2 * time.Minute)
	if fired, err := trigger.Check(ctx); err != nil || !fired {
		t.Fatalf("Check at duration = (%v, %v), want (true, nil)", fired, err)
	}
}

func waitForTrigger(t *testing.T, trigger RuntimeTrigger, ctx *TriggerContext) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fired, err := trigger.Check(ctx)
		if err != nil {
			t.Fatalf("Check(): %v", err)
		}
		if fired {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("trigger did not fire before deadline")
}

func waitForDirectoryWatch(t *testing.T, trigger RuntimeTrigger, dir string) {
	t.Helper()
	file, ok := trigger.(*fileTrigger)
	if !ok {
		t.Fatalf("trigger type = %T, want *fileTrigger", trigger)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		file.mu.Lock()
		watcher := file.watcher
		file.mu.Unlock()
		if watcher != nil {
			for _, watched := range watcher.WatchList() {
				if watched == dir {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("directory %q was not watched before deadline", dir)
}
