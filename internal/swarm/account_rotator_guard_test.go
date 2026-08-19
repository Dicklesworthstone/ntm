package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeFakeCaam creates an executable fake caam that, when run, appends its
// arguments to a marker file and prints a successful JSON switch result. It
// returns the caam path and the marker path. The marker lets a test prove
// whether caam was actually invoked (i.e. the guard did NOT block).
func writeFakeCaam(t *testing.T) (caamPath, markerPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake caam shell script requires a POSIX shell")
	}
	dir := t.TempDir()
	markerPath = filepath.Join(dir, "caam_invocations.log")
	caamPath = filepath.Join(dir, "caam")
	// `robot status` advertises the safe-restore capability so the default
	// capability gate (caam #19) treats this fake caam as safe. The activate
	// path returns CAAM's current profile field names, not NTM's internal DTO
	// names, so this fixture catches a contract drift at the adapter boundary.
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
if [ "$1" = "robot" ] && [ "$2" = "status" ]; then
  printf '{"data":{"capabilities":["safe-restore","robot"]}}'
  exit 0
fi
printf '{"success":true,"previous_profile":"acctA","profile":"acctB"}'
`, markerPath)
	if err := os.WriteFile(caamPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	return caamPath, markerPath
}

func caamWasInvoked(t *testing.T, markerPath string) bool {
	t.Helper()
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		t.Fatalf("read marker: %v", err)
	}
	return len(data) > 0
}

func codexLimitEvent() LimitHitEvent {
	return LimitHitEvent{
		SessionPane: "swarm:1.1",
		AgentType:   "cod",
		Pattern:     "rate limit",
		DetectedAt:  time.Now(),
	}
}
