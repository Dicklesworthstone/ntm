package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestRoundRobinAssignE2E(t *testing.T) {
	testutil.E2ETestPrecheck(t)

	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br not installed, skipping round-robin E2E test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed, skipping round-robin E2E test")
	}

	projectRoot := findProjectRoot(t)
	scriptPath := filepath.Join(projectRoot, "tests", "e2e", "test_round_robin_assign.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("round-robin E2E script not found at %s", scriptPath)
	}

	cmd := exec.Command(scriptPath)
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("round-robin E2E script failed with exit code %d", exitErr.ExitCode())
		}
		t.Fatalf("round-robin E2E script failed: %v", err)
	}
}
