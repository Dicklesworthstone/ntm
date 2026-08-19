package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSpawnLifecycleContextCancellationShortCircuitsOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := preflightOllamaSpawnContext(ctx, SpawnOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ollama preflight error = %v, want cancellation", err)
	}
	if err := preflightCodexAccountSupportContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Codex preflight error = %v, want cancellation", err)
	}
	if _, err := ResolveCassContextWithContext(ctx, "query", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("CASS context error = %v, want cancellation", err)
	}
	if err := spawnSessionLogicContext(ctx, SpawnOptions{Session: "canceled-spawn"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn lifecycle error = %v, want cancellation before side effects", err)
	}
}

func TestSpawnLifecycleComposableCancellationDoesNotEmit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	stdout, err := captureStdout(t, func() error {
		return spawnSessionLogicComposable(ctx, SpawnOptions{Session: "silent-canceled-spawn"})
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("composable spawn error = %v, want cancellation", err)
	}
	if stdout != "" {
		t.Fatalf("composable spawn wrote stdout %q", stdout)
	}
}

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

func TestAgentSpawnContextEnvVarPrefix(t *testing.T) {
	ctx := NewSpawnContext(3)
	agentCtx := ctx.ForAgent(1, 0)

	prefix := agentCtx.EnvVarPrefix()

	if !strings.HasPrefix(prefix, "NTM_SPAWN_ORDER=1 ") {
		t.Errorf("expected prefix to start with 'NTM_SPAWN_ORDER=1 ', got %s", prefix)
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
	if strings.Contains(prefix, ";") {
		t.Errorf("expected prefix not to contain ';' (to preserve `cd && cmd` semantics), got %s", prefix)
	}
	if !strings.HasSuffix(prefix, " ") {
		t.Errorf("expected prefix to end with a space, got %s", prefix)
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

func TestBuildSpawnPromptSequencePreservesContextOrdering(t *testing.T) {
	t.Run("user prompt combines cass after recovery", func(t *testing.T) {
		steps := buildSpawnPromptSequence("cass", "recovery", "user", 250*time.Millisecond)
		if len(steps) != 2 {
			t.Fatalf("steps = %#v, want recovery and combined user prompt", steps)
		}
		if steps[0].Kind != "recovery_context" || steps[0].Message != "recovery" {
			t.Fatalf("first step = %#v, want recovery", steps[0])
		}
		if steps[1].Kind != "user_prompt" || steps[1].Message != "cass\n\nuser" || steps[1].Delay != 250*time.Millisecond {
			t.Fatalf("second step = %#v, want delayed cass+user", steps[1])
		}
	})

	t.Run("context-only delivery keeps cass before recovery", func(t *testing.T) {
		steps := buildSpawnPromptSequence("cass", "recovery", "", time.Second)
		if len(steps) != 2 || steps[0].Kind != "cass_context" || steps[1].Kind != "recovery_context" {
			t.Fatalf("steps = %#v, want cass then recovery", steps)
		}
		if steps[0].Delay != 0 || steps[1].Delay != 0 {
			t.Fatalf("context-only delays = %v/%v, want zero", steps[0].Delay, steps[1].Delay)
		}
	})
}
