package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// writeFakeCaamProfile creates a fake caam that returns a fixed auth JSON for the
// isolated-profile export primitives and a fixed account list. It records its
// invocations to a marker file.
func writeFakeCaamProfile(t *testing.T, authPayload string) (caamPath, markerPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake caam shell script requires a POSIX shell")
	}
	dir := t.TempDir()
	markerPath = filepath.Join(dir, "caam_invocations.log")
	caamPath = filepath.Join(dir, "caam")
	// The script branches on the first arg: "profile"/"creds" => emit auth;
	// "list" => emit two openai accounts; anything else => empty success.
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$1" in
  profile|creds)
    printf '%s'
    ;;
  list)
    printf '[{"id":"acctA","provider":"openai","active":true},{"id":"acctB","provider":"openai","active":false}]'
    ;;
  *)
    printf '{"success":true}'
    ;;
esac
`, markerPath, authPayload)
	if err := os.WriteFile(caamPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	return caamPath, markerPath
}

func TestCodexHome_HomePathIsolatedPerPane(t *testing.T) {
	p := NewCodexHomeProvisioner("/base")
	a := p.HomePath("swarm:1", "1.1")
	b := p.HomePath("swarm:1", "1.2")
	if a == b {
		t.Fatalf("expected distinct homes per pane, got %q == %q", a, b)
	}
	if want := filepath.Join("/base", ".ntm", "codex-homes", "swarm_1", "1_1"); a != want {
		t.Errorf("unexpected home path: got %q want %q", a, want)
	}
}

func TestCodexHome_ProvisionSeedsAuthFromProfile(t *testing.T) {
	caamPath, _ := writeFakeCaamProfile(t, `{"OPENAI_API_KEY":"sk-test"}`)
	base := t.TempDir()
	p := NewCodexHomeProvisioner(base).WithCaamPath(caamPath)

	home, err := p.ProvisionPaneHome(context.Background(), "swarm:1", "1.1", "acctA")
	if err != nil {
		t.Fatalf("ProvisionPaneHome: %v", err)
	}
	authPath := filepath.Join(home, "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("expected seeded auth.json: %v", err)
	}
	if string(data) != `{"OPENAI_API_KEY":"sk-test"}` {
		t.Errorf("unexpected seeded auth: %q", string(data))
	}
	// Isolation: the home must NOT be the global ~/.codex.
	if isGlobalCodexHome(home) {
		t.Errorf("provisioned home %q should not be considered global", home)
	}
	// Perms: auth.json should be 0600.
	if fi, err := os.Stat(authPath); err == nil {
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("expected auth.json perm 0600, got %v", fi.Mode().Perm())
		}
	}
}

func TestCodexHome_RepopulateRefreshesAuth(t *testing.T) {
	caamPath, marker := writeFakeCaamProfile(t, `{"OPENAI_API_KEY":"sk-new"}`)
	base := t.TempDir()
	p := NewCodexHomeProvisioner(base).WithCaamPath(caamPath)

	home, err := p.RepopulatePaneHome(context.Background(), "swarm:1", "1.1", "acctB")
	if err != nil {
		t.Fatalf("RepopulatePaneHome: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "auth.json"))
	if string(data) != `{"OPENAI_API_KEY":"sk-new"}` {
		t.Errorf("expected repopulated auth, got %q", string(data))
	}
	// caam must have been asked for the isolated profile auth, not a global switch.
	logData, _ := os.ReadFile(marker)
	if !contains(string(logData), "profile") && !contains(string(logData), "creds") {
		t.Errorf("expected caam isolated-profile invocation, got log: %q", string(logData))
	}
	if contains(string(logData), "switch") {
		t.Errorf("pane-local repopulate must NOT call caam switch; log: %q", string(logData))
	}
}

func TestCodexHome_RepopulateRequiresProfile(t *testing.T) {
	p := NewCodexHomeProvisioner(t.TempDir())
	if _, err := p.RepopulatePaneHome(context.Background(), "s", "p", ""); err == nil {
		t.Fatal("expected error when profile is empty")
	}
}

func TestIsGlobalCodexHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		in     string
		global bool
	}{
		{"", true},
		{filepath.Join(home, ".codex"), true},
		{"/home/u/.ntm/codex-homes/swarm/1", false},
		{"/tmp/iso", false},
		{".codex", true},
	}
	for _, c := range cases {
		if got := isGlobalCodexHome(c.in); got != c.global {
			t.Errorf("isGlobalCodexHome(%q)=%v want %v", c.in, got, c.global)
		}
	}
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

func (f fakeProbe) PaneCodexHome(target string) (string, bool, error) {
	set := f.setMap[target]
	return f.homes[target], set, nil
}

func TestInspector_DetectsGlobalVsIsolated(t *testing.T) {
	probe := fakeProbe{
		panes: []tmux.Pane{
			{ID: "%1", Index: 1, Type: tmux.AgentType("cod")},
			{ID: "%2", Index: 2, Type: tmux.AgentType("cod")},
			{ID: "%3", Index: 3, Type: tmux.AgentType("cc")}, // not codex, skipped
		},
		homes: map[string]string{
			"%1": "/home/u/.ntm/codex-homes/swarm/1", // isolated
			"%2": "",                                  // unset => global
		},
		setMap: map[string]bool{"%1": true, "%2": false},
	}
	inspector := newTmuxCodexHomeInspector("swarm", probe)
	panes, err := inspector()
	if err != nil {
		t.Fatalf("inspector: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("expected 2 codex panes (cc skipped), got %d", len(panes))
	}
	var isolated, global int
	for _, p := range panes {
		if p.IsIsolated() {
			isolated++
		} else {
			global++
		}
	}
	if isolated != 1 || global != 1 {
		t.Errorf("expected 1 isolated + 1 global, got isolated=%d global=%d", isolated, global)
	}
}

func TestInspector_GlobalCodexHomePathTreatedAsNotIsolated(t *testing.T) {
	home, _ := os.UserHomeDir()
	probe := fakeProbe{
		panes:  []tmux.Pane{{ID: "%1", Index: 1, Type: tmux.AgentType("cod")}},
		homes:  map[string]string{"%1": filepath.Join(home, ".codex")},
		setMap: map[string]bool{"%1": true},
	}
	inspector := newTmuxCodexHomeInspector("swarm", probe)
	panes, _ := inspector()
	if len(panes) != 1 || panes[0].IsIsolated() {
		t.Errorf("a CODEX_HOME pointing at global ~/.codex must be reported NOT isolated: %+v", panes)
	}
}

func TestInspector_PropagatesPaneListError(t *testing.T) {
	inspector := newTmuxCodexHomeInspector("swarm", fakeProbe{err: errors.New("tmux down")})
	if _, err := inspector(); err == nil {
		t.Fatal("expected error to propagate from GetPanes failure")
	}
}

func TestParseShowEnvironment(t *testing.T) {
	v, set, _ := parseShowEnvironment("CODEX_HOME=/x/y\n", "CODEX_HOME")
	if !set || v != "/x/y" {
		t.Errorf("expected set /x/y, got set=%v v=%q", set, v)
	}
	_, set2, _ := parseShowEnvironment("-CODEX_HOME\n", "CODEX_HOME")
	if set2 {
		t.Error("expected unset for -CODEX_HOME line")
	}
	_, set3, _ := parseShowEnvironment("OTHER=1\n", "CODEX_HOME")
	if set3 {
		t.Error("expected unset when var absent")
	}
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

// Acceptance: a global Codex rotation is refused when caam lacks safe-restore,
// even though all panes are isolated.
func TestGuard_GlobalRotationRefusedWithoutSafeRestore(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{{SessionPane: "swarm:1.1", CodexHome: "/iso/1"}}, nil
		}).
		WithCaamCapabilityProber(func(ctx context.Context) ([]string, error) {
			return []string{"robot"}, nil // NO safe-restore
		})

	_, err := rotator.OnLimitHit(codexLimitEvent())
	if err == nil {
		t.Fatal("expected refusal when caam lacks safe-restore")
	}
	if !errors.Is(err, ErrRotationBlocked) {
		t.Errorf("expected ErrRotationBlocked, got: %v", err)
	}
	if !contains(err.Error(), "safe-restore") {
		t.Errorf("expected safe-restore message, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("caam switch must NOT run when safe-restore is missing")
	}
}

// With safe-restore advertised, an isolated global rotation is permitted.
func TestGuard_GlobalRotationAllowedWithSafeRestore(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{{SessionPane: "swarm:1.1", CodexHome: "/iso/1"}}, nil
		}).
		WithCaamCapabilityProber(func(ctx context.Context) ([]string, error) {
			return []string{"safe-restore"}, nil
		})

	record, err := rotator.OnLimitHit(codexLimitEvent())
	if err != nil {
		t.Fatalf("expected rotation allowed with safe-restore, got: %v", err)
	}
	if record == nil {
		t.Fatal("expected a rotation record")
	}
	if !caamWasInvoked(t, marker) {
		t.Error("expected caam switch to be invoked")
	}
}

// Capability probe failure => fail closed.
func TestGuard_CapabilityProbeFailureRefusesRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{{SessionPane: "swarm:1.1", CodexHome: "/iso/1"}}, nil
		}).
		WithCaamCapabilityProber(func(ctx context.Context) ([]string, error) {
			return nil, fmt.Errorf("caam exploded")
		})

	_, err := rotator.OnLimitHit(codexLimitEvent())
	if err == nil || !errors.Is(err, ErrRotationBlocked) {
		t.Fatalf("expected ErrRotationBlocked on probe failure, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("caam switch must NOT run when capability probe fails")
	}
}

// ----- pane-local rotation chooses the isolated path, never global switch -----

func TestPaneLocalRotation_RepopulatesIsolatedHomeNotGlobal(t *testing.T) {
	caamPath, marker := writeFakeCaamProfile(t, `{"OPENAI_API_KEY":"sk-rotated"}`)
	base := t.TempDir()
	prov := NewCodexHomeProvisioner(base).WithCaamPath(caamPath)

	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeProvisioner(prov)

	event := LimitHitEvent{SessionPane: "swarm:1.1", AgentType: "cod", Pattern: "rate limit"}
	record, err := rotator.OnLimitHit(event)
	if err != nil {
		t.Fatalf("pane-local rotation failed: %v", err)
	}
	if record == nil || !record.PaneLocal {
		t.Fatalf("expected a pane-local rotation record, got %+v", record)
	}
	if record.CodexHome == "" {
		t.Error("expected CodexHome to be set on pane-local record")
	}
	// The isolated home must have been (re)written with fresh auth.
	data, err := os.ReadFile(filepath.Join(record.CodexHome, "auth.json"))
	if err != nil || string(data) != `{"OPENAI_API_KEY":"sk-rotated"}` {
		t.Errorf("expected isolated auth.json refreshed, got err=%v data=%q", err, string(data))
	}
	// caam switch (global clobber) must NEVER be invoked on the pane-local path.
	log, _ := os.ReadFile(marker)
	if contains(string(log), "switch") {
		t.Errorf("pane-local rotation must NOT call caam switch; log: %q", string(log))
	}
}

func TestPaneLocalRotation_HonorsPin(t *testing.T) {
	caamPath, marker := writeFakeCaamProfile(t, `{"OPENAI_API_KEY":"x"}`)
	prov := NewCodexHomeProvisioner(t.TempDir()).WithCaamPath(caamPath)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeProvisioner(prov)
	rotator.PinAccount("cod", "acctA")

	event := LimitHitEvent{SessionPane: "swarm:1.1", AgentType: "cod", Pattern: "rate limit"}
	_, err := rotator.OnLimitHit(event)
	if err == nil || !errors.Is(err, ErrRotationBlocked) {
		t.Fatalf("expected pin to block pane-local rotation, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("no caam call expected when pinned")
	}
}

func TestPaneLocalRotation_ForceOverridesPin(t *testing.T) {
	caamPath, _ := writeFakeCaamProfile(t, `{"OPENAI_API_KEY":"x"}`)
	prov := NewCodexHomeProvisioner(t.TempDir()).WithCaamPath(caamPath)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeProvisioner(prov).
		WithForceGlobalAuthClobber(true)
	rotator.PinAccount("cod", "acctA")

	event := LimitHitEvent{SessionPane: "swarm:1.1", AgentType: "cod", Pattern: "rate limit"}
	record, err := rotator.OnLimitHit(event)
	if err != nil {
		t.Fatalf("expected force to override pin for pane-local rotation, got: %v", err)
	}
	if record == nil || !record.PaneLocal {
		t.Fatalf("expected pane-local record under force, got %+v", record)
	}
}

func TestSplitSessionPane(t *testing.T) {
	cases := map[string][2]string{
		"swarm:1.1": {"swarm", "1.1"},
		"swarm":     {"swarm", "0"},
		"":          {"default", "0"},
		":x":        {"default", "x"},
	}
	for in, want := range cases {
		s, p := splitSessionPane(in)
		if s != want[0] || p != want[1] {
			t.Errorf("splitSessionPane(%q)=(%q,%q) want (%q,%q)", in, s, p, want[0], want[1])
		}
	}
}

// EnvForPane yields a CODEX_HOME assignment that points at the isolated path.
func TestCodexHome_EnvForPane(t *testing.T) {
	p := NewCodexHomeProvisioner("/base")
	env := p.EnvForPane("swarm:1", "1.1")
	got, ok := env[CodexHomeEnvVar]
	if !ok {
		t.Fatal("expected CODEX_HOME in env")
	}
	if got != p.HomePath("swarm:1", "1.1") {
		t.Errorf("CODEX_HOME=%q does not match HomePath", got)
	}
}

// Sanity: caam list parsing used by nextCodexProfile yields the right alt.
func TestNextCodexProfile_RoundRobinsPastCurrent(t *testing.T) {
	caamPath, _ := writeFakeCaamProfile(t, `{"k":"v"}`)
	rotator := NewAccountRotator().WithCaamPath(caamPath)
	next, err := rotator.nextCodexProfile(context.Background(), "openai", "acctA")
	if err != nil {
		t.Fatalf("nextCodexProfile: %v", err)
	}
	if next != "acctB" {
		t.Errorf("expected round-robin to acctB, got %q", next)
	}
}

// Ensure CodexPaneInfo JSON-marshals (defensive; struct is used in logs/tests).
func TestCodexPaneInfo_Marshalable(t *testing.T) {
	b, err := json.Marshal(CodexPaneInfo{SessionPane: "s:1.1", CodexHome: "/iso"})
	if err != nil || len(b) == 0 {
		t.Fatalf("marshal CodexPaneInfo: %v", err)
	}
}
