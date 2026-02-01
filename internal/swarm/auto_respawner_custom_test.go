package swarm

import (
	"testing"
)

func TestAutoRespawnerSpawnAgentWithCommandBuilder(t *testing.T) {
	mock := &mockTmuxClient{}
	builder := NewLaunchCommandBuilder().
		WithAgentPath("cc", "/custom/path/to/claude").
		WithFullPaths(true)

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithCommandBuilder(builder).
		WithProjectPathLookup(func(sessionPane string) string {
			return "/tmp/project"
		})

	t.Log("[TEST] Spawning agent with CommandBuilder configured")

	// Spawn agent "cc"
	err := r.spawnAgent("test:1.1", "cc")
	if err != nil {
		t.Fatalf("spawnAgent failed: %v", err)
	}

	if len(mock.sendKeysCalls) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(mock.sendKeysCalls))
	}

	call := mock.sendKeysCalls[0]
	// Expect full path since builder is configured with WithFullPaths(true) and a custom path
	expected := "/custom/path/to/claude --dangerously-skip-permissions"
	if call.text != expected {
		t.Errorf("expected command %q, got %q", expected, call.text)
	}
}

func TestAutoRespawnerSpawnAgentWithCommandBuilderAndArgs(t *testing.T) {
	mock := &mockTmuxClient{}
	builder := NewLaunchCommandBuilder().
		WithAgentPath("cod", "/opt/codex").
		WithAgentArgs("cod", []string{"--verbose", "--model=gpt-5"}).
		WithFullPaths(true)

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithCommandBuilder(builder)

	t.Log("[TEST] Spawning agent with CommandBuilder configured with args")

	// Spawn agent "cod"
	err := r.spawnAgent("test:1.1", "cod")
	if err != nil {
		t.Fatalf("spawnAgent failed: %v", err)
	}

	if len(mock.sendKeysCalls) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(mock.sendKeysCalls))
	}

	call := mock.sendKeysCalls[0]
	expected := "/opt/codex --verbose --model=gpt-5"
	if call.text != expected {
		t.Errorf("expected command %q, got %q", expected, call.text)
	}
}

func TestAutoRespawnerSpawnAgentFallbackWithoutBuilder(t *testing.T) {
	mock := &mockTmuxClient{}
	// No builder configured
	r := NewAutoRespawner().
		WithTmuxClient(mock)

	t.Log("[TEST] Spawning agent without CommandBuilder (fallback)")

	// Spawn agent "cc"
	err := r.spawnAgent("test:1.1", "cc")
	if err != nil {
		t.Fatalf("spawnAgent failed: %v", err)
	}

	if len(mock.sendKeysCalls) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(mock.sendKeysCalls))
	}

	call := mock.sendKeysCalls[0]
	// Expect default alias "cc"
	expected := "cc"
	if call.text != expected {
		t.Errorf("expected command %q, got %q", expected, call.text)
	}
}
