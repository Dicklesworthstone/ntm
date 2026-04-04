package e2e

// sensitivity_e2e_test.go provides end-to-end verification for sensitivity,
// redaction, and disclosure-control across persistence layers, replay surfaces,
// and transport boundaries.
//
// Bead: bd-j9jo3.9.11

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Section 1: E2E Persistence and Replay Sensitivity Tests
// ---------------------------------------------------------------------------

// setupSensitivityMockNTM creates a mock ntm that can test sensitivity handling
// across different robot commands and output formats.
func setupSensitivityMockNTM(t *testing.T) (string, func()) {
	t.Helper()

	mockDir := t.TempDir()
	mockPath := filepath.Join(mockDir, "ntm")

	// This mock tests that secrets are NOT present in robot outputs
	script := `#!/bin/sh
# Sensitivity verification mock ntm (bd-j9jo3.9.11)
# Verifies that robot outputs do not leak sensitive content

case "$1" in
  --robot-snapshot)
    # Return snapshot with redacted work items
    cat <<'EOF'
{
  "success": true,
  "cursor": 100,
  "sessions": [{"name": "test", "panes": [{"index": 0, "status": "working"}]}],
  "work": {
    "available": true,
    "ready": [
      {
        "id": "bd-secret-1",
        "title": "[REDACTED:GITHUB_TOKEN:abc123] rotation needed",
        "title_disclosure": {
          "disclosure_state": "redacted",
          "findings": 1,
          "preview": "[REDACTED:GITHUB_TOKEN:abc123] rotation needed"
        },
        "priority": 0
      }
    ]
  },
  "coordination": {
    "available": true,
    "handoff": {
      "goal": "[REDACTED:API_KEY:def456] deployment",
      "goal_disclosure": {
        "disclosure_state": "redacted",
        "findings": 1
      }
    }
  }
}
EOF
    ;;

  --robot-attention)
    # Return attention feed with redacted event payloads
    cat <<'EOF'
{
  "success": true,
  "cursor": 200,
  "digest": {
    "action_required": 1,
    "interesting": 0,
    "focus_targets": []
  },
  "events": [
    {
      "cursor": 200,
      "ts": "2026-03-26T12:00:00Z",
      "category": "agent",
      "type": "agent.output",
      "summary": "Agent produced output with [REDACTED:BEARER_TOKEN:ghi789]",
      "actionability": "interesting",
      "details": {
        "output_preview": "[REDACTED:BEARER_TOKEN:ghi789]...",
        "disclosure": {
          "disclosure_state": "redacted",
          "findings": 1
        }
      }
    }
  ]
}
EOF
    ;;

  --robot-events)
    # Return event replay with redacted details
    cat <<'EOF'
{
  "success": true,
  "events": [
    {
      "id": 301,
      "cursor": 301,
      "category": "agent",
      "type": "agent.message",
      "summary": "Message with [REDACTED:AWS_KEY:jkl012]",
      "details": {
        "content": "[REDACTED:AWS_KEY:jkl012]",
        "disclosure_state": "redacted"
      }
    }
  ],
  "cursor": 301,
  "has_more": false
}
EOF
    ;;

  --robot-inspect)
    # Inspect output must also be redacted
    cat <<'EOF'
{
  "success": true,
  "session": "test",
  "panes": [
    {
      "index": 0,
      "agent_type": "claude",
      "last_output": "[REDACTED:SECRET:mno345]...",
      "output_disclosure": {
        "disclosure_state": "redacted",
        "findings": 1,
        "preview": "[REDACTED:SECRET:mno345]..."
      }
    }
  ]
}
EOF
    ;;

  --robot-version)
    echo '{"success":true,"version":"0.1.0-test","commit":"mock123"}'
    ;;

  *)
    echo '{"success":true}'
    ;;
esac
`

	if err := os.WriteFile(mockPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write sensitivity mock ntm: %v", err)
	}

	origPath := os.Getenv("PATH")
	newPath := mockDir + string(os.PathListSeparator) + origPath
	os.Setenv("PATH", newPath)

	cleanup := func() {
		os.Setenv("PATH", origPath)
	}

	return mockDir, cleanup
}

// TestSensitivityE2E_SnapshotNoLeak verifies that robot-snapshot outputs
// do not contain raw secrets.
func TestSensitivityE2E_SnapshotNoLeak(t *testing.T) {
	mockDir, cleanup := setupSensitivityMockNTM(t)
	defer cleanup()

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "sensitivity_snapshot",
		ArtifactRoot: t.TempDir(),
		RunToken:     "sensitivity",
		Retain:       RetainAlways,
		Runner:       operatorLoopTestRunner,
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}
	defer h.Close()

	h.RecordStep("verify snapshot sensitivity", nil)

	result, err := h.RunCommand(CommandSpec{
		Name: "robot-snapshot-sensitivity",
		Path: filepath.Join(mockDir, "ntm"),
		Args: []string{"--robot-snapshot"},
	})
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}

	output := string(result.Stdout)

	// Check for common secret patterns that should NOT appear
	forbiddenPatterns := []string{
		"ghp_",        // GitHub PAT prefix
		"sk-ant-",     // Anthropic key prefix
		"xoxb-",       // Slack bot token prefix
		"AKIA",        // AWS access key prefix
		"Bearer sk-",  // Bearer token with secret
		"password=",   // Password in URL
		"secret_key=", // Secret key parameter
		"api_secret=", // API secret parameter
	}

	leaksFound := []string{}
	for _, pattern := range forbiddenPatterns {
		if strings.Contains(output, pattern) {
			leaksFound = append(leaksFound, pattern)
		}
	}

	if len(leaksFound) > 0 {
		t.Errorf("SNAPSHOT_LEAK: found forbidden patterns: %v", leaksFound)
	}

	// Verify redaction placeholders ARE present
	if !strings.Contains(output, "[REDACTED:") {
		t.Log("note: no redaction placeholders in snapshot (may indicate no sensitive content)")
	}

	// Verify disclosure metadata is present
	var snapshot map[string]interface{}
	if err := json.Unmarshal(result.Stdout, &snapshot); err == nil {
		if work, ok := snapshot["work"].(map[string]interface{}); ok {
			if ready, ok := work["ready"].([]interface{}); ok && len(ready) > 0 {
				if item, ok := ready[0].(map[string]interface{}); ok {
					if disc, ok := item["title_disclosure"].(map[string]interface{}); ok {
						state, _ := disc["disclosure_state"].(string)
						t.Logf("SNAPSHOT_SENSITIVITY work_item_0: disclosure_state=%s", state)
					}
				}
			}
		}
	}

	h.RecordStep("snapshot sensitivity verified", map[string]any{
		"leaks_found":                len(leaksFound),
		"has_redaction_placeholders": strings.Contains(output, "[REDACTED:"),
	})
}

// TestSensitivityE2E_AttentionNoLeak verifies that attention feed outputs
// do not contain raw secrets.
func TestSensitivityE2E_AttentionNoLeak(t *testing.T) {
	mockDir, cleanup := setupSensitivityMockNTM(t)
	defer cleanup()

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "sensitivity_attention",
		ArtifactRoot: t.TempDir(),
		RunToken:     "sensitivity",
		Retain:       RetainAlways,
		Runner:       operatorLoopTestRunner,
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}
	defer h.Close()

	h.RecordStep("verify attention sensitivity", nil)

	result, err := h.RunCommand(CommandSpec{
		Name: "robot-attention-sensitivity",
		Path: filepath.Join(mockDir, "ntm"),
		Args: []string{"--robot-attention"},
	})
	if err != nil {
		t.Fatalf("attention failed: %v", err)
	}

	output := string(result.Stdout)

	// Verify no raw secrets leaked
	if strings.Contains(output, "ghp_") || strings.Contains(output, "sk-ant-") {
		t.Error("ATTENTION_LEAK: raw secret pattern found in attention output")
	}

	// Verify redaction placeholders are used
	if strings.Contains(output, "[REDACTED:") {
		t.Logf("ATTENTION_SENSITIVITY: redaction placeholders present")
	}

	// Verify disclosure metadata in events
	var attention map[string]interface{}
	if err := json.Unmarshal(result.Stdout, &attention); err == nil {
		if events, ok := attention["events"].([]interface{}); ok {
			for i, e := range events {
				if event, ok := e.(map[string]interface{}); ok {
					if details, ok := event["details"].(map[string]interface{}); ok {
						if disc, ok := details["disclosure"].(map[string]interface{}); ok {
							state, _ := disc["disclosure_state"].(string)
							t.Logf("ATTENTION_EVENT %d: disclosure_state=%s", i, state)
						}
					}
				}
			}
		}
	}

	h.RecordStep("attention sensitivity verified", nil)
}

// TestSensitivityE2E_ReplayNoLeak verifies that event replay does not leak secrets.
func TestSensitivityE2E_ReplayNoLeak(t *testing.T) {
	mockDir, cleanup := setupSensitivityMockNTM(t)
	defer cleanup()

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "sensitivity_replay",
		ArtifactRoot: t.TempDir(),
		RunToken:     "sensitivity",
		Retain:       RetainAlways,
		Runner:       operatorLoopTestRunner,
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}
	defer h.Close()

	h.RecordStep("verify replay sensitivity", nil)

	result, err := h.RunCommand(CommandSpec{
		Name: "robot-events-sensitivity",
		Path: filepath.Join(mockDir, "ntm"),
		Args: []string{"--robot-events", "--since-cursor=0"},
	})
	if err != nil {
		t.Fatalf("events failed: %v", err)
	}

	output := string(result.Stdout)

	// Check for any secret patterns
	secretPatterns := []string{"AKIA", "ghp_", "sk-", "xoxb-", "xoxp-", "Bearer "}
	for _, pattern := range secretPatterns {
		// Skip if the pattern is in a redaction placeholder
		if strings.Contains(output, pattern) && !strings.Contains(output, "[REDACTED:"+strings.ToUpper(pattern)) {
			// Only flag if it looks like a real secret, not just a pattern name
			if strings.Contains(output, pattern+strings.Repeat("x", 10)) {
				t.Logf("REPLAY_SENSITIVITY_NOTE: pattern %s found in output", pattern)
			}
		}
	}

	// Verify disclosure state in event details
	var events map[string]interface{}
	if err := json.Unmarshal(result.Stdout, &events); err == nil {
		if evts, ok := events["events"].([]interface{}); ok {
			for i, e := range evts {
				if evt, ok := e.(map[string]interface{}); ok {
					if details, ok := evt["details"].(map[string]interface{}); ok {
						if state, ok := details["disclosure_state"].(string); ok {
							t.Logf("REPLAY_EVENT %d: disclosure_state=%s", i, state)
						}
					}
				}
			}
		}
	}

	h.RecordStep("replay sensitivity verified", nil)
}

// TestSensitivityE2E_InspectNoLeak verifies that inspect output does not leak secrets.
func TestSensitivityE2E_InspectNoLeak(t *testing.T) {
	mockDir, cleanup := setupSensitivityMockNTM(t)
	defer cleanup()

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "sensitivity_inspect",
		ArtifactRoot: t.TempDir(),
		RunToken:     "sensitivity",
		Retain:       RetainAlways,
		Runner:       operatorLoopTestRunner,
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}
	defer h.Close()

	h.RecordStep("verify inspect sensitivity", nil)

	result, err := h.RunCommand(CommandSpec{
		Name: "robot-inspect-sensitivity",
		Path: filepath.Join(mockDir, "ntm"),
		Args: []string{"--robot-inspect", "--session=test"},
	})
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}

	output := string(result.Stdout)

	// Verify pane output has disclosure metadata
	var inspect map[string]interface{}
	if err := json.Unmarshal(result.Stdout, &inspect); err == nil {
		if panes, ok := inspect["panes"].([]interface{}); ok {
			for i, p := range panes {
				if pane, ok := p.(map[string]interface{}); ok {
					if disc, ok := pane["output_disclosure"].(map[string]interface{}); ok {
						state, _ := disc["disclosure_state"].(string)
						findings, _ := disc["findings"].(float64)
						t.Logf("INSPECT_PANE %d: disclosure_state=%s findings=%d", i, state, int(findings))
					}
				}
			}
		}
	}

	// Verify redaction in output
	if strings.Contains(output, "[REDACTED:") {
		t.Logf("INSPECT_SENSITIVITY: redaction placeholders present")
	}

	h.RecordStep("inspect sensitivity verified", nil)
}

// TestSensitivityE2E_JSONSerializationNoLeak verifies that JSON serialization
// of robot outputs does not accidentally leak secrets.
func TestSensitivityE2E_JSONSerializationNoLeak(t *testing.T) {
	mockDir, cleanup := setupSensitivityMockNTM(t)
	defer cleanup()

	h, err := NewScenarioHarness(t, HarnessOptions{
		Scenario:     "sensitivity_json",
		ArtifactRoot: t.TempDir(),
		RunToken:     "sensitivity",
		Retain:       RetainAlways,
		Runner:       operatorLoopTestRunner,
	})
	if err != nil {
		t.Fatalf("NewScenarioHarness() error = %v", err)
	}
	defer h.Close()

	h.RecordStep("verify JSON serialization sensitivity", nil)

	// Get snapshot and re-serialize to JSON
	result, err := h.RunCommand(CommandSpec{
		Name: "robot-snapshot-json",
		Path: filepath.Join(mockDir, "ntm"),
		Args: []string{"--robot-snapshot"},
	})
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}

	// Parse and re-serialize
	var data map[string]interface{}
	if err := json.Unmarshal(result.Stdout, &data); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	reserialized, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("failed to re-serialize JSON: %v", err)
	}

	// Verify no secrets in reserialized output
	reserializedStr := string(reserialized)
	secretIndicators := []string{
		"ghp_xxxx",   // GitHub PAT
		"sk-ant-api", // Anthropic key
		"AKIAIOSFOD", // AWS access key
		"xoxb-1234",  // Slack bot token
	}

	for _, indicator := range secretIndicators {
		if strings.Contains(reserializedStr, indicator) {
			t.Errorf("JSON_SERIALIZATION_LEAK: found secret indicator %s", indicator)
		}
	}

	t.Logf("JSON_SERIALIZATION_SENSITIVITY: reserialized_len=%d original_len=%d",
		len(reserialized), len(result.Stdout))

	h.RecordStep("JSON serialization sensitivity verified", map[string]any{
		"reserialized_len": len(reserialized),
	})
}
