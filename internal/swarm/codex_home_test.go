package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// writeFakeCaamProfile creates a fake caam that exposes the supported isolated
// profile-status surface and a fixed account list. It records its invocations to
// a marker file.
func writeFakeCaamProfile(t *testing.T, authPayload string) (caamPath, markerPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake caam shell script requires a POSIX shell")
	}
	dir := t.TempDir()
	markerPath = filepath.Join(dir, "caam_invocations.log")
	caamPath = filepath.Join(dir, "caam")
	// The script returns profile status for CAAM's supported isolated-profile
	// inspection command, plus two accounts for pane-local rotation selection.
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$1 $2" in
  "profile status")
    printf 'Profile: codex/%%s\n  Path: %s/profiles/%%s\n  Auth mode: oauth\n  Logged in: true\n' "$4" "$4"
    ;;
  "list --json")
    printf '[{"id":"acctA","provider":"openai","active":true},{"id":"acctB","provider":"openai","active":false}]'
    ;;
  *)
    printf '{"success":true}'
    ;;
esac
`, markerPath, filepath.Dir(caamPath))
	if err := os.WriteFile(caamPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	return caamPath, markerPath
}

// fakeProbe implements codexHomeProbe for inspector tests.
type fakeProbe struct {
	panes  []tmux.Pane
	homes  map[string]string // target -> CODEX_HOME ("" => unset)
	setMap map[string]bool   // target -> whether CODEX_HOME is set
	err    error
}

func (f fakeProbe) GetPanes(session string) ([]tmux.Pane, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.panes, nil
}

func (f fakeProbe) PaneCodexHome(_ string, pane tmux.Pane) (string, bool, error) {
	set := f.setMap[pane.ID]
	return f.homes[pane.ID], set, nil
}

// ----- caam capability probe gating -----

func TestCaamCapability_ParsesDataCapabilities(t *testing.T) {
	caps, err := parseCaamCapabilities(`{"data":{"capabilities":["safe-restore","robot"]}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasCapability(caps, CapabilitySafeRestore) {
		t.Errorf("expected safe-restore capability, got %v", caps)
	}
}

func TestCaamCapability_TopLevelFallback(t *testing.T) {
	caps, err := parseCaamCapabilities(`{"capabilities":["safe-restore"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasCapability(caps, CapabilitySafeRestore) {
		t.Errorf("expected safe-restore via top-level, got %v", caps)
	}
}

func TestParseCaamCapabilitiesRejectsUnusableOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
	}{
		{name: "empty", out: ""},
		{name: "whitespace", out: " \n\t "},
		{name: "not JSON", out: "safe-restore"},
		{name: "wrong capability type", out: `{"data":{"capabilities":"safe-restore"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseCaamCapabilities(tt.out); err == nil {
				t.Fatalf("parseCaamCapabilities(%q) succeeded, want error", tt.out)
			}
		})
	}
}

func TestDefaultCaamCapabilityProberUsesRobotStatusJSONContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake caam shell script requires a POSIX shell")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "args")
	caamPath := filepath.Join(dir, "caam")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" > %q
if [ "$#" -ne 2 ] || [ "$1" != "robot" ] || [ "$2" != "status" ]; then
  printf 'unexpected arguments: %%s\n' "$*" >&2
  exit 64
fi
printf '{"data":{"capabilities":["safe-restore"]}}'
`, marker)
	if err := os.WriteFile(caamPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}

	caps, err := defaultCaamCapabilityProber(caamPath, time.Second)(context.Background())
	if err != nil {
		t.Fatalf("probe caam capabilities: %v", err)
	}
	if !hasCapability(caps, CapabilitySafeRestore) {
		t.Fatalf("capabilities = %v, want %q", caps, CapabilitySafeRestore)
	}
	args, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read caam arguments: %v", err)
	}
	if got := string(args); got != "robot status\n" {
		t.Fatalf("caam arguments = %q, want robot status", got)
	}
}

// ----- pane-local rotation chooses the isolated path, never global switch -----

// Ensure CodexPaneInfo JSON-marshals (defensive; struct is used in logs/tests).
func TestCodexPaneInfo_Marshalable(t *testing.T) {
	b, err := json.Marshal(CodexPaneInfo{SessionPane: "s:1.1", CodexHome: "/iso"})
	if err != nil || len(b) == 0 {
		t.Fatalf("marshal CodexPaneInfo: %v", err)
	}
}

// stubPaneProbe supplies a fixed pane list while delegating the home lookup to
// the real provisioned-directory probe, so the end-to-end assertion exercises
// the actual isolation logic without a live tmux server.
type stubPaneProbe struct {
	probe provisionedCodexProbe
	panes []tmux.Pane
}

func (s stubPaneProbe) GetPanes(string) ([]tmux.Pane, error) { return s.panes, nil }
