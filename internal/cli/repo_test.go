package cli

import (
	"strings"
	"testing"
)

func TestRunRepoSyncMissingRU(t *testing.T) {
	t.Setenv("PATH", "")

	err := runRepoSync([]string{})
	if err == nil {
		t.Fatal("expected error when ru is not installed")
	}
	if !strings.Contains(err.Error(), "ru not installed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
