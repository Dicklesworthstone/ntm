package testutil

import (
	"os"
	"testing"
)

func TestIsolationAuthorizationCannotBeForged(t *testing.T) {
	t.Setenv(isolatedTmuxMarker, "1")
	t.Setenv("TMUX_TMPDIR", "/tmp")
	t.Setenv("TMUX", "")
	if isolatedTmuxReady() {
		t.Fatal("environment-only isolation authorization must be rejected")
	}
}

func TestIsolationRejectsRoutingChangeAfterSetup(t *testing.T) {
	cleanup, err := SetupIsolatedTmuxTestProcess()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})

	if !isolatedTmuxReady() {
		t.Fatal("process-owned isolation should be ready after setup")
	}
	originalRoot := os.Getenv("TMUX_TMPDIR")
	t.Setenv("TMUX_TMPDIR", "/tmp")
	if isolatedTmuxReady() {
		t.Fatal("routing change after setup must revoke mutation authorization")
	}
	t.Setenv("TMUX_TMPDIR", originalRoot)
	if !isolatedTmuxReady() {
		t.Fatal("restoring the exact process-owned root should restore authorization")
	}
}
