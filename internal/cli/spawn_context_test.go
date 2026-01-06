package cli

import (
	"strings"
	"testing"
	"time"
)

func TestNewSpawnContext(t *testing.T) {
	ctx := NewSpawnContext(4)

	if ctx.TotalAgents != 4 {
		t.Errorf("expected TotalAgents=4, got %d", ctx.TotalAgents)
	}
	if ctx.BatchID == "" {
		t.Error("expected non-empty BatchID")
	}
	if !strings.HasPrefix(ctx.BatchID, "spawn-") {
		t.Errorf("expected BatchID to start with 'spawn-', got %s", ctx.BatchID)
	}
	if ctx.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestSpawnContextForAgent(t *testing.T) {
	ctx := NewSpawnContext(3)

	agentCtx := ctx.ForAgent(2, 5*time.Second)

	if agentCtx.Order != 2 {
		t.Errorf("expected Order=2, got %d", agentCtx.Order)
	}
	if agentCtx.StaggerDelay != 5*time.Second {
		t.Errorf("expected StaggerDelay=5s, got %v", agentCtx.StaggerDelay)
	}
	if agentCtx.TotalAgents != 3 {
		t.Errorf("expected TotalAgents=3, got %d", agentCtx.TotalAgents)
	}
	if agentCtx.BatchID != ctx.BatchID {
		t.Errorf("expected inherited BatchID %s, got %s", ctx.BatchID, agentCtx.BatchID)
	}
}

func TestAgentSpawnContextEnvVars(t *testing.T) {
	ctx := NewSpawnContext(4)
	agentCtx := ctx.ForAgent(2, time.Second)

	envVars := agentCtx.EnvVars()

	if envVars["NTM_SPAWN_ORDER"] != "2" {
		t.Errorf("expected NTM_SPAWN_ORDER=2, got %s", envVars["NTM_SPAWN_ORDER"])
	}
	if envVars["NTM_SPAWN_TOTAL"] != "4" {
		t.Errorf("expected NTM_SPAWN_TOTAL=4, got %s", envVars["NTM_SPAWN_TOTAL"])
	}
	if envVars["NTM_SPAWN_BATCH_ID"] != ctx.BatchID {
		t.Errorf("expected NTM_SPAWN_BATCH_ID=%s, got %s", ctx.BatchID, envVars["NTM_SPAWN_BATCH_ID"])
	}
}

func TestAgentSpawnContextEnvVarPrefix(t *testing.T) {
	ctx := NewSpawnContext(3)
	agentCtx := ctx.ForAgent(1, 0)

	prefix := agentCtx.EnvVarPrefix()

	if !strings.HasPrefix(prefix, "export ") {
		t.Errorf("expected prefix to start with 'export ', got %s", prefix)
	}
	if !strings.Contains(prefix, "NTM_SPAWN_ORDER=1") {
		t.Errorf("expected prefix to contain NTM_SPAWN_ORDER=1, got %s", prefix)
	}
	if !strings.Contains(prefix, "NTM_SPAWN_TOTAL=3") {
		t.Errorf("expected prefix to contain NTM_SPAWN_TOTAL=3, got %s", prefix)
	}
	if !strings.Contains(prefix, "NTM_SPAWN_BATCH_ID=") {
		t.Errorf("expected prefix to contain NTM_SPAWN_BATCH_ID=, got %s", prefix)
	}
	if !strings.HasSuffix(prefix, "; ") {
		t.Errorf("expected prefix to end with '; ', got %s", prefix)
	}
}

func TestAgentSpawnContextPromptAnnotation(t *testing.T) {
	ctx := NewSpawnContext(4)
	agentCtx := ctx.ForAgent(2, time.Second)

	annotation := agentCtx.PromptAnnotation()

	if !strings.HasPrefix(annotation, "[Spawn context:") {
		t.Errorf("expected annotation to start with '[Spawn context:', got %s", annotation)
	}
	if !strings.Contains(annotation, "Agent 2/4") {
		t.Errorf("expected annotation to contain 'Agent 2/4', got %s", annotation)
	}
	if !strings.Contains(annotation, ctx.BatchID) {
		t.Errorf("expected annotation to contain batch ID %s, got %s", ctx.BatchID, annotation)
	}
}

func TestAnnotatePrompt(t *testing.T) {
	ctx := NewSpawnContext(2)
	agentCtx := ctx.ForAgent(1, 0)

	tests := []struct {
		name              string
		prompt            string
		includeAnnotation bool
		expectAnnotation  bool
	}{
		{
			name:              "with annotation",
			prompt:            "Do the task",
			includeAnnotation: true,
			expectAnnotation:  true,
		},
		{
			name:              "without annotation",
			prompt:            "Do the task",
			includeAnnotation: false,
			expectAnnotation:  false,
		},
		{
			name:              "empty prompt with annotation",
			prompt:            "",
			includeAnnotation: true,
			expectAnnotation:  false, // Empty prompt returns empty
		},
		{
			name:              "empty prompt without annotation",
			prompt:            "",
			includeAnnotation: false,
			expectAnnotation:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agentCtx.AnnotatePrompt(tt.prompt, tt.includeAnnotation)

			hasAnnotation := strings.Contains(result, "[Spawn context:")

			if hasAnnotation != tt.expectAnnotation {
				t.Errorf("expected annotation=%v, got %v (result: %s)", tt.expectAnnotation, hasAnnotation, result)
			}

			if tt.prompt != "" && tt.includeAnnotation {
				if !strings.Contains(result, tt.prompt) {
					t.Errorf("expected result to contain original prompt %q, got %s", tt.prompt, result)
				}
			}
		})
	}
}

func TestAnnotatePromptPreservesOriginal(t *testing.T) {
	ctx := NewSpawnContext(3)
	agentCtx := ctx.ForAgent(2, time.Second)

	original := "Implement the feature\nwith multiple lines"
	result := agentCtx.AnnotatePrompt(original, true)

	if !strings.Contains(result, original) {
		t.Errorf("expected result to contain original prompt, got %s", result)
	}
	if !strings.HasPrefix(result, "[Spawn context:") {
		t.Errorf("expected result to start with annotation, got %s", result)
	}
}

func TestGenerateBatchID(t *testing.T) {
	id1 := generateBatchID()
	id2 := generateBatchID()

	if id1 == id2 {
		t.Error("expected unique batch IDs, got duplicates")
	}

	if !strings.HasPrefix(id1, "spawn-") {
		t.Errorf("expected batch ID to start with 'spawn-', got %s", id1)
	}

	// Check format: spawn-YYYYMMDD-HHMMSS-XXXX
	parts := strings.Split(id1, "-")
	if len(parts) < 4 {
		t.Errorf("expected batch ID format spawn-date-time-random, got %s", id1)
	}
}

func TestSpawnContextFirstAndLastAgent(t *testing.T) {
	ctx := NewSpawnContext(5)

	// First agent
	first := ctx.ForAgent(1, 0)
	if first.Order != 1 {
		t.Errorf("expected first agent Order=1, got %d", first.Order)
	}

	// Last agent
	last := ctx.ForAgent(5, 4*time.Second)
	if last.Order != 5 {
		t.Errorf("expected last agent Order=5, got %d", last.Order)
	}

	// Both should have same batch ID
	if first.BatchID != last.BatchID {
		t.Errorf("expected same BatchID for all agents, got %s vs %s", first.BatchID, last.BatchID)
	}
}

func TestEnvVarsMapContent(t *testing.T) {
	ctx := NewSpawnContext(10)
	agentCtx := ctx.ForAgent(7, 6*time.Second)

	envVars := agentCtx.EnvVars()

	// Should have exactly 3 environment variables
	if len(envVars) != 3 {
		t.Errorf("expected 3 env vars, got %d", len(envVars))
	}

	expectedKeys := []string{"NTM_SPAWN_ORDER", "NTM_SPAWN_TOTAL", "NTM_SPAWN_BATCH_ID"}
	for _, key := range expectedKeys {
		if _, ok := envVars[key]; !ok {
			t.Errorf("expected env var %s to be present", key)
		}
	}
}
