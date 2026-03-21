//go:build e2e
// +build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArtifactManagerCreatesLayoutAndManifest(t *testing.T) {
	root := t.TempDir()
	cfg := ArtifactConfig{
		BaseDir:            root,
		SuiteName:          "attention feed harness",
		Retention:          RetainAlways,
		MaxArtifactAgeDays: 1,
	}

	am, err := NewArtifactManager("operator loop smoke", cfg)
	if err != nil {
		t.Fatalf("NewArtifactManager() error = %v", err)
	}
	t.Logf("artifact root = %s", am.Dir())

	for _, rel := range []string{
		".",
		"stdout",
		"stderr",
		"cursors",
		"events",
		"summaries",
		"transport",
		"screenshots",
	} {
		path := filepath.Join(am.Dir(), rel)
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if !info.IsDir() && rel == "." {
			t.Fatalf("artifact root is not a directory: %s", path)
		}
	}

	manifestData, err := os.ReadFile(filepath.Join(am.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if got := manifest["status"]; got != "running" {
		t.Fatalf("manifest status = %v, want running", got)
	}

	timelineData, err := os.ReadFile(filepath.Join(am.Dir(), "timeline.jsonl"))
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if !strings.Contains(string(timelineData), "scenario started") {
		t.Fatalf("timeline missing scenario-start entry: %s", string(timelineData))
	}
}

func TestArtifactManagerPrunesOldArtifacts(t *testing.T) {
	root := t.TempDir()
	suiteDir := filepath.Join(root, "attention_suite")
	if err := os.MkdirAll(filepath.Join(suiteDir, "stale_case"), 0o755); err != nil {
		t.Fatalf("mkdir stale artifact: %v", err)
	}
	staleFile := filepath.Join(suiteDir, "stale_case", "old.txt")
	if err := os.WriteFile(staleFile, []byte("old"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	oldTime := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(filepath.Join(suiteDir, "stale_case"), oldTime, oldTime); err != nil {
		t.Fatalf("chtimes stale dir: %v", err)
	}
	if err := os.Chtimes(staleFile, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes stale file: %v", err)
	}

	_, err := NewArtifactManager("fresh_case", ArtifactConfig{
		BaseDir:            root,
		SuiteName:          "attention_suite",
		Retention:          RetainAlways,
		MaxArtifactAgeDays: 1,
	})
	if err != nil {
		t.Fatalf("NewArtifactManager() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(suiteDir, "stale_case")); !os.IsNotExist(err) {
		t.Fatalf("stale artifact directory still exists, err=%v", err)
	}
}

func TestArtifactManagerFinalizeRetention(t *testing.T) {
	cases := []struct {
		name     string
		retain   ArtifactRetention
		failed   bool
		exists   bool
	}{
		{name: "retain_on_failure_pass", retain: RetainOnFailure, failed: false, exists: false},
		{name: "retain_on_failure_fail", retain: RetainOnFailure, failed: true, exists: true},
		{name: "retain_always", retain: RetainAlways, failed: false, exists: true},
		{name: "retain_never", retain: RetainNever, failed: true, exists: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am, err := NewArtifactManager(tc.name, ArtifactConfig{
				BaseDir:   t.TempDir(),
				SuiteName: "retention_suite",
				Retention: tc.retain,
			})
			if err != nil {
				t.Fatalf("NewArtifactManager() error = %v", err)
			}
			dir := am.Dir()
			if err := am.Finalize(tc.failed); err != nil {
				t.Fatalf("Finalize() error = %v", err)
			}
			_, err = os.Stat(dir)
			if tc.exists && err != nil {
				t.Fatalf("expected artifact dir to exist, stat err=%v", err)
			}
			if !tc.exists && !os.IsNotExist(err) {
				t.Fatalf("expected artifact dir to be deleted, stat err=%v", err)
			}
		})
	}
}

func TestScenarioHarnessRunRobotSmoke(t *testing.T) {
	pathDir := t.TempDir()
	script := filepath.Join(pathDir, "ntm")
	content := `#!/bin/sh
case "$1" in
  --robot-status)
    echo '{"success":true,"wake_reason":"mail","focus_targets":["pane-2"],"_degraded":false,"cursor":"41","nested":{"cursor":"42"}}'
    ;;
  --robot-fail)
    echo '{"success":false,"error_code":"NOT_IMPLEMENTED"}'
    echo 'simulated stderr' 1>&2
    exit 2
    ;;
  *)
    echo '{"success":true}'
    ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake ntm: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+origPath)

	cfg := ScenarioConfig{
		Name: "robot smoke",
		ArtifactConfig: ArtifactConfig{
			BaseDir:   t.TempDir(),
			SuiteName: "scenario_harness",
			Retention: RetainAlways,
		},
		Timeout: 5 * time.Second,
	}
	h, err := NewScenarioHarness(t, cfg)
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}

	success := h.RunRobot("--robot-status")
	h.AssertSuccess(success)
	h.AssertWakeReason(success, "wake_reason", "mail")
	h.AssertFocusTargetsPresent(success, "focus_targets")
	h.AssertDegradedMarker(success, "_degraded", false)

	cursor, err := ParseCursor(success, "nested.cursor")
	if err != nil {
		t.Fatalf("ParseCursor() error = %v", err)
	}
	if cursor == nil || *cursor != 42 {
		t.Fatalf("cursor = %v, want 42", cursor)
	}

	if err := h.CaptureTransport("ws_messages", []byte("{\"event\":\"hello\"}\n")); err != nil {
		t.Fatalf("CaptureTransport() error = %v", err)
	}
	in := int64(41)
	out := int64(42)
	h.TrackCursor("robot-status", &in, &out, 1, "mail", "smoke cursor", 12*time.Millisecond, nil)
	h.AnnotateStep("rendered digest", map[string]interface{}{"focus_target_count": 1})

	failure := h.RunRobot("--robot-fail")
	h.AssertExitCode(failure, 2)
	h.AssertErrorCode(failure, "NOT_IMPLEMENTED")

	h.Cleanup()

	t.Logf("scenario artifacts retained at %s", h.Artifacts().Dir())
	manifestData, err := os.ReadFile(filepath.Join(h.Artifacts().Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read final manifest: %v", err)
	}
	if !strings.Contains(string(manifestData), `"status": "passed"`) {
		t.Fatalf("manifest missing passed status: %s", string(manifestData))
	}

	timelineData, err := os.ReadFile(filepath.Join(h.Artifacts().Dir(), "timeline.jsonl"))
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	for _, want := range []string{"robot-status", "ws_messages", "rendered digest", "scenario finalized"} {
		if !strings.Contains(string(timelineData), want) {
			t.Fatalf("timeline missing %q entry: %s", want, string(timelineData))
		}
	}

	cursorTrace, err := os.ReadFile(filepath.Join(h.Artifacts().Dir(), "cursors", "cursor_trace.jsonl"))
	if err != nil {
		t.Fatalf("read cursor trace: %v", err)
	}
	if !strings.Contains(string(cursorTrace), `"wake_reason":"mail"`) {
		t.Fatalf("cursor trace missing wake_reason: %s", string(cursorTrace))
	}
}

func TestScenarioHarnessCleanupOrder(t *testing.T) {
	h, err := NewScenarioHarness(t, ScenarioConfig{
		Name: "cleanup order",
		ArtifactConfig: ArtifactConfig{
			BaseDir:   t.TempDir(),
			SuiteName: "cleanup_suite",
			Retention: RetainAlways,
		},
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}

	var order []string
	h.RegisterCleanup("first", func() error {
		order = append(order, "first")
		return nil
	})
	h.RegisterCleanup("second", func() error {
		order = append(order, "second")
		return fmt.Errorf("simulated cleanup error")
	})
	h.RegisterCleanup("third", func() error {
		order = append(order, "third")
		return nil
	})

	h.Cleanup()

	got := strings.Join(order, ",")
	want := "third,second,first"
	if got != want {
		t.Fatalf("cleanup order = %q, want %q", got, want)
	}
}
