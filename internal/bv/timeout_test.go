package bv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetConfiguredTimeout snapshots and restores the process-global configured
// timeout so precedence tests cannot leak into other tests in the package.
func resetConfiguredTimeout(t *testing.T) {
	t.Helper()
	prev := configuredTimeoutSeconds.Load()
	t.Cleanup(func() { configuredTimeoutSeconds.Store(prev) })
	configuredTimeoutSeconds.Store(0)
}

func TestCommandTimeoutPrecedence(t *testing.T) {
	resetConfiguredTimeout(t)
	t.Setenv(BVTimeoutEnvVar, "")

	// Default: nothing configured, no env.
	if got := CommandTimeout(); got != DefaultTimeout {
		t.Fatalf("default CommandTimeout() = %v, want %v", got, DefaultTimeout)
	}

	// Config value applies.
	ConfigureCommandTimeout(7)
	if got := CommandTimeout(); got != 7*time.Second {
		t.Fatalf("configured CommandTimeout() = %v, want 7s", got)
	}

	// Non-positive config values are ignored.
	ConfigureCommandTimeout(0)
	ConfigureCommandTimeout(-5)
	if got := CommandTimeout(); got != 7*time.Second {
		t.Fatalf("CommandTimeout() after invalid ConfigureCommandTimeout = %v, want 7s", got)
	}

	// Env wins over config.
	t.Setenv(BVTimeoutEnvVar, "3")
	if got := CommandTimeout(); got != 3*time.Second {
		t.Fatalf("env-overridden CommandTimeout() = %v, want 3s", got)
	}

	// Invalid env values fall back to the configured value.
	for _, raw := range []string{"abc", "0", "-2", "2.5"} {
		t.Setenv(BVTimeoutEnvVar, raw)
		if got := CommandTimeout(); got != 7*time.Second {
			t.Fatalf("CommandTimeout() with NTM_BV_TIMEOUT=%q = %v, want configured 7s", raw, got)
		}
	}
}

func TestGetInsightsHonorsEnvTimeoutWithSlowBV(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow-stub timeout test in -short mode")
	}
	resetConfiguredTimeout(t)

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\nexec /bin/sleep 10\n"
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake bv: %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv(BVTimeoutEnvVar, "1")

	start := time.Now()
	_, err := GetInsights(dir)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("GetInsights with slow bv stub: expected timeout error, got nil")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("GetInsights error = %v, want ErrTimeout in chain", err)
	}
	if !strings.Contains(err.Error(), "bv timed out after 1s") {
		t.Fatalf("GetInsights error = %q, want mention of 1s timeout", err)
	}
	// The historical hard-coded cap was 30s; the env override must cut the
	// stall to ~1s (generous ceiling for CI jitter).
	if elapsed > 8*time.Second {
		t.Fatalf("GetInsights took %v; env timeout of 1s did not bound the subprocess", elapsed)
	}
}

func TestBVChildInheritsCallerBVEnvVars(t *testing.T) {
	resetConfiguredTimeout(t)

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	capture := filepath.Join(t.TempDir(), "captured-env")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf 'BV_NO_CACHE=%s\\nBV_ROBOT_HISTORY_TIMEOUT_MS=%s\\n' \"$BV_NO_CACHE\" \"$BV_ROBOT_HISTORY_TIMEOUT_MS\" > " + capture + "\n" +
		"printf '{}\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake bv: %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_ROBOT_HISTORY_TIMEOUT_MS", "2000")

	if _, err := GetInsights(dir); err != nil {
		t.Fatalf("GetInsights with env-capturing stub: %v", err)
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured env: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "BV_NO_CACHE=1") {
		t.Errorf("bv child did not inherit BV_NO_CACHE=1; captured: %q", got)
	}
	if !strings.Contains(got, "BV_ROBOT_HISTORY_TIMEOUT_MS=2000") {
		t.Errorf("bv child did not inherit BV_ROBOT_HISTORY_TIMEOUT_MS=2000; captured: %q", got)
	}
}
