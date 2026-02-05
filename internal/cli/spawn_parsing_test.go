package cli

import (
	"testing"
	"time"
)

func TestOptionalDurationValueSet(t *testing.T) {
	t.Parallel()

	var dur time.Duration
	var enabled bool
	v := newOptionalDurationValue(5*time.Second, &dur, &enabled)

	if enabled {
		t.Fatal("enabled should be false initially")
	}
	if dur != 5*time.Second {
		t.Fatalf("default duration = %v, want 5s", dur)
	}
	if got := v.String(); got != "" {
		t.Fatalf("String() = %q, want empty when disabled", got)
	}

	if err := v.Set(""); err != nil {
		t.Fatalf("Set(\"\") error: %v", err)
	}
	if !enabled {
		t.Fatal("enabled should be true after empty Set")
	}
	if dur != 5*time.Second {
		t.Fatalf("duration after empty Set = %v, want 5s", dur)
	}

	if err := v.Set("0"); err != nil {
		t.Fatalf("Set(\"0\") error: %v", err)
	}
	if enabled {
		t.Fatal("enabled should be false after Set(\"0\")")
	}
	if dur != 0 {
		t.Fatalf("duration after Set(\"0\") = %v, want 0", dur)
	}

	if err := v.Set("10s"); err != nil {
		t.Fatalf("Set(\"10s\") error: %v", err)
	}
	if !enabled {
		t.Fatal("enabled should be true after Set(\"10s\")")
	}
	if dur != 10*time.Second {
		t.Fatalf("duration after Set(\"10s\") = %v, want 10s", dur)
	}

	if err := v.Set("-1s"); err == nil {
		t.Fatal("expected error for negative duration")
	}
}

func TestParseEnvDurationMs(t *testing.T) {
	t.Setenv("NTM_TEST_SPAWN_DELAY_MS", "")
	if got, err := parseEnvDurationMs("NTM_TEST_SPAWN_DELAY_MS"); err != nil || got != 0 {
		t.Fatalf("empty env: got %v err=%v, want 0 nil", got, err)
	}

	t.Setenv("NTM_TEST_SPAWN_DELAY_MS", "150")
	if got, err := parseEnvDurationMs("NTM_TEST_SPAWN_DELAY_MS"); err != nil || got != 150*time.Millisecond {
		t.Fatalf("150ms env: got %v err=%v, want 150ms nil", got, err)
	}

	t.Setenv("NTM_TEST_SPAWN_DELAY_MS", "abc")
	if _, err := parseEnvDurationMs("NTM_TEST_SPAWN_DELAY_MS"); err == nil {
		t.Fatal("expected error for non-integer env")
	}

	t.Setenv("NTM_TEST_SPAWN_DELAY_MS", "-5")
	if _, err := parseEnvDurationMs("NTM_TEST_SPAWN_DELAY_MS"); err == nil {
		t.Fatal("expected error for negative env")
	}
}

func TestResolveSpawnTestPacing(t *testing.T) {
	t.Setenv("NTM_TEST_MODE", "")
	t.Setenv("NTM_E2E", "")
	t.Setenv("NTM_TEST_SPAWN_DELAY_MS", "")
	t.Setenv("NTM_TEST_SPAWN_PANE_DELAY_MS", "")
	t.Setenv("NTM_TEST_SPAWN_AGENT_DELAY_MS", "")

	pacing, err := resolveSpawnTestPacing()
	if err != nil {
		t.Fatalf("resolveSpawnTestPacing error: %v", err)
	}
	if pacing.paneDelay != 0 || pacing.agentDelay != 0 {
		t.Fatalf("expected zero pacing when not in test mode, got %+v", pacing)
	}

	t.Setenv("NTM_TEST_MODE", "1")
	t.Setenv("NTM_TEST_SPAWN_DELAY_MS", "100")
	t.Setenv("NTM_TEST_SPAWN_PANE_DELAY_MS", "")
	t.Setenv("NTM_TEST_SPAWN_AGENT_DELAY_MS", "")

	pacing, err = resolveSpawnTestPacing()
	if err != nil {
		t.Fatalf("resolveSpawnTestPacing error: %v", err)
	}
	if pacing.paneDelay != 100*time.Millisecond || pacing.agentDelay != 100*time.Millisecond {
		t.Fatalf("expected defaults applied, got %+v", pacing)
	}

	t.Setenv("NTM_TEST_SPAWN_PANE_DELAY_MS", "50")
	t.Setenv("NTM_TEST_SPAWN_AGENT_DELAY_MS", "150")
	pacing, err = resolveSpawnTestPacing()
	if err != nil {
		t.Fatalf("resolveSpawnTestPacing error: %v", err)
	}
	if pacing.paneDelay != 50*time.Millisecond || pacing.agentDelay != 150*time.Millisecond {
		t.Fatalf("expected overrides applied, got %+v", pacing)
	}
}
