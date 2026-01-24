package robot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tokens"
)

// TestToonVsJSONTokenEfficiency evaluates TOON vs JSON token counts on representative payloads.
// This test documents the findings for bead bd-rmnfk.
func TestToonVsJSONTokenEfficiency(t *testing.T) {
	requireToonBinary(t)

	payloads := []struct {
		name    string
		payload any
	}{
		{
			name:    "StatusOutput (3 sessions, 9 agents)",
			payload: createSampleStatusOutput(),
		},
		{
			name:    "SessionInfo array (uniform objects)",
			payload: createSampleSessionArray(),
		},
		{
			name:    "TokenBreakdown array (tabular)",
			payload: createSampleTokenBreakdown(),
		},
		{
			name:    "PlanActions array (recommendations)",
			payload: createSamplePlanActions(),
		},
		{
			name:    "AgentInfo array (pane data)",
			payload: createSampleAgentInfoArray(),
		},
		{
			name:    "Simple RobotResponse",
			payload: NewRobotResponse(true),
		},
		{
			name:    "Error response",
			payload: NewErrorResponse(fmt.Errorf("session not found"), ErrCodeSessionNotFound, "Use 'ntm list' to see sessions"),
		},
	}

	t.Log("=" + "==========================================================================")
	t.Log("TOON vs JSON Token Efficiency Evaluation (bd-rmnfk)")
	t.Log("=" + "==========================================================================")
	t.Log("")

	var totalJSONBytes, totalTOONBytes int
	var totalJSONTokens, totalTOONTokens int

	for _, tc := range payloads {
		// JSON encoding
		var jsonBuf bytes.Buffer
		jsonEnc := json.NewEncoder(&jsonBuf)
		jsonEnc.SetIndent("", "  ")
		if err := jsonEnc.Encode(tc.payload); err != nil {
			t.Fatalf("JSON encode failed for %s: %v", tc.name, err)
		}
		jsonOutput := jsonBuf.String()

		// TOON encoding
		toonOutput, err := toonEncode(tc.payload, "\t")
		if err != nil {
			t.Logf("TOON encode failed for %s: %v (will use JSON)", tc.name, err)
			toonOutput = jsonOutput // fallback
		}

		// Calculate sizes
		jsonBytes := len(jsonOutput)
		toonBytes := len(toonOutput)

		// Estimate tokens (using ~3.5 chars per token as industry standard)
		jsonTokens := tokens.EstimateTokens(jsonOutput)
		toonTokens := tokens.EstimateTokens(toonOutput)

		// Calculate savings
		byteSavings := float64(jsonBytes-toonBytes) / float64(jsonBytes) * 100
		tokenSavings := float64(jsonTokens-toonTokens) / float64(jsonTokens) * 100

		t.Logf("Payload: %s", tc.name)
		t.Logf("  JSON:  %d bytes, ~%d tokens", jsonBytes, jsonTokens)
		t.Logf("  TOON:  %d bytes, ~%d tokens", toonBytes, toonTokens)
		t.Logf("  Savings: %.1f%% bytes, %.1f%% tokens", byteSavings, tokenSavings)
		t.Log("")

		totalJSONBytes += jsonBytes
		totalTOONBytes += toonBytes
		totalJSONTokens += jsonTokens
		totalTOONTokens += toonTokens
	}

	// Summary
	overallByteSavings := float64(totalJSONBytes-totalTOONBytes) / float64(totalJSONBytes) * 100
	overallTokenSavings := float64(totalJSONTokens-totalTOONTokens) / float64(totalJSONTokens) * 100

	t.Log("=" + "==========================================================================")
	t.Log("SUMMARY")
	t.Log("=" + "==========================================================================")
	t.Logf("Total JSON:  %d bytes, ~%d tokens", totalJSONBytes, totalJSONTokens)
	t.Logf("Total TOON:  %d bytes, ~%d tokens", totalTOONBytes, totalTOONTokens)
	t.Logf("Overall savings: %.1f%% bytes, %.1f%% tokens", overallByteSavings, overallTokenSavings)
	t.Log("")

	// Decision criteria from bead: ≥25% token savings = allow TOON default
	if overallTokenSavings >= 25 {
		t.Log("RECOMMENDATION: TOON saves ≥25% tokens. Allow TOON as default via config or 'auto' mode.")
	} else if overallTokenSavings >= 10 {
		t.Log("RECOMMENDATION: TOON saves 10-25% tokens. Keep JSON default but document TOON for LLM-only usage.")
	} else {
		t.Log("RECOMMENDATION: TOON savings marginal (<10%). Keep JSON as default.")
	}
}

// Sample payload generators

func createSampleStatusOutput() map[string]any {
	return map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"system": map[string]any{
			"version":        "1.2.3",
			"os":             "linux",
			"tmux_available": true,
			"tmux_version":   "3.4",
		},
		"sessions": createSampleSessionArray(),
		"summary": map[string]any{
			"total_sessions": 3,
			"total_agents":   9,
			"attached_count": 1,
			"claude_count":   4,
			"codex_count":    3,
			"gemini_count":   2,
		},
		"beads": map[string]any{
			"total":       42,
			"open":        15,
			"in_progress": 8,
			"closed":      19,
		},
	}
}

func createSampleSessionArray() []map[string]any {
	return []map[string]any{
		{
			"name":        "myproject",
			"attached":    true,
			"pane_count":  4,
			"agent_count": 3,
			"agents": []map[string]any{
				{"pane": 1, "type": "user", "model": ""},
				{"pane": 2, "type": "claude", "model": "opus-4.5"},
				{"pane": 3, "type": "codex", "model": "o3"},
				{"pane": 4, "type": "gemini", "model": "2.5-pro"},
			},
		},
		{
			"name":        "backend",
			"attached":    false,
			"pane_count":  3,
			"agent_count": 3,
			"agents": []map[string]any{
				{"pane": 1, "type": "claude", "model": "opus-4.5"},
				{"pane": 2, "type": "claude", "model": "opus-4.5"},
				{"pane": 3, "type": "codex", "model": "o3"},
			},
		},
		{
			"name":        "frontend",
			"attached":    false,
			"pane_count":  4,
			"agent_count": 3,
			"agents": []map[string]any{
				{"pane": 1, "type": "user", "model": ""},
				{"pane": 2, "type": "claude", "model": "sonnet-4"},
				{"pane": 3, "type": "codex", "model": "o3"},
				{"pane": 4, "type": "gemini", "model": "2.5-flash"},
			},
		},
	}
}

func createSampleTokenBreakdown() []map[string]any {
	return []map[string]any{
		{"key": "claude", "tokens": 45000, "prompts": 120, "characters": 157500, "percentage": 45.0},
		{"key": "codex", "tokens": 35000, "prompts": 95, "characters": 122500, "percentage": 35.0},
		{"key": "gemini", "tokens": 20000, "prompts": 55, "characters": 70000, "percentage": 20.0},
	}
}

func createSamplePlanActions() []map[string]any {
	return []map[string]any{
		{
			"priority":    1,
			"command":     "ntm send myproject --type=claude --msg='Continue work on auth refactor'",
			"description": "Resume work on high-priority task",
			"args":        []string{"--type=claude", "--msg='...'"},
		},
		{
			"priority":    2,
			"command":     "ntm spawn backend --cc=2",
			"description": "Add capacity for backend work",
			"args":        []string{"--cc=2"},
		},
		{
			"priority":    3,
			"command":     "bd update bd-xyz --status in_progress",
			"description": "Claim next available bead",
			"args":        []string{"--status", "in_progress"},
		},
	}
}

func createSampleAgentInfoArray() []map[string]any {
	return []map[string]any{
		{
			"pane":                2,
			"type":                "claude",
			"model":               "opus-4.5",
			"state":               "working",
			"context_usage":       0.45,
			"last_activity":       time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339),
			"prompt_count":        15,
			"assigned_bead":       "bd-abc123",
			"assigned_bead_title": "Implement auth flow",
		},
		{
			"pane":                3,
			"type":                "codex",
			"model":               "o3",
			"state":               "idle",
			"context_usage":       0.22,
			"last_activity":       time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
			"prompt_count":        8,
			"assigned_bead":       "",
			"assigned_bead_title": "",
		},
		{
			"pane":                4,
			"type":                "gemini",
			"model":               "2.5-pro",
			"state":               "working",
			"context_usage":       0.67,
			"last_activity":       time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339),
			"prompt_count":        22,
			"assigned_bead":       "bd-def456",
			"assigned_bead_title": "Fix database migration",
		},
	}
}

// TestToonOutputFormats shows what TOON output looks like for different payloads.
func TestToonOutputFormats(t *testing.T) {
	requireToonBinary(t)

	payloads := []struct {
		name    string
		payload any
	}{
		{
			name:    "Uniform array (tabular)",
			payload: createSampleTokenBreakdown(),
		},
		{
			name:    "Simple object",
			payload: NewRobotResponse(true),
		},
		{
			name:    "Nested object with array",
			payload: createSampleStatusOutput(),
		},
	}

	for _, tc := range payloads {
		t.Logf("\n=== %s ===\n", tc.name)

		toonOutput, err := toonEncode(tc.payload, "\t")
		if err != nil {
			t.Logf("TOON error: %v", err)
			continue
		}

		t.Logf("TOON output:\n%s", toonOutput)
	}
}
