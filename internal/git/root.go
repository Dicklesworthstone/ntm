package git

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// FindProjectRoot attempts to find the root of the git repository
// containing the given directory. Returns empty string if not found.
func FindProjectRoot(startDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = startDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
