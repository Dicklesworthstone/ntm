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

func TestCodexHome_ProvisionUsesCaamProfileHome(t *testing.T) {
	caamPath, _ := writeFakeCaamProfile(t, `{"OPENAI_API_KEY":"sk-test"}`)
	base := t.TempDir()
	p := NewCodexHomeProvisioner(base).WithCaamPath(caamPath)

	home, err := p.ProvisionPaneHome(context.Background(), "swarm:1", "1.1", "acctA")
	if err != nil {
		t.Fatalf("ProvisionPaneHome: %v", err)
	}
	wantHome := filepath.Join(filepath.Dir(caamPath), "profiles", "acctA", "codex_home")
	if home != wantHome {
		t.Errorf("ProvisionPaneHome() = %q, want CAAM-owned %q", home, wantHome)
	}
	// Isolation: the home must NOT be the global ~/.codex.
	if isGlobalCodexHome(home) {
		t.Errorf("provisioned home %q should not be considered global", home)
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
	wantHome := filepath.Join(filepath.Dir(caamPath), "profiles", "acctB", "codex_home")
	if home != wantHome {
		t.Errorf("RepopulatePaneHome() = %q, want CAAM-owned %q", home, wantHome)
	}
	// CAAM must have been asked for the named isolated profile, not a global switch.
	logData, _ := os.ReadFile(marker)
	if !contains(string(logData), "profile status codex acctB") {
		t.Errorf("expected CAAM profile-status invocation, got log: %q", string(logData))
	}
	if contains(string(logData), "creds") || contains(string(logData), "openai") || contains(string(logData), "switch") {
		t.Errorf("pane-local repopulate used an obsolete or global CAAM path: %q", string(logData))
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

func (f fakeProbe) PaneCodexHome(_ string, pane tmux.Pane) (string, bool, error) {
	set := f.setMap[pane.ID]
	return f.homes[pane.ID], set, nil
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
			"%2": "",                                 // unset => global
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

func TestCaamSupportsSafeRestoreNormalizesCapabilityNames(t *testing.T) {
	rotator := NewAccountRotator().WithCaamCapabilityProber(func(context.Context) ([]string, error) {
		return []string{" robot ", " SAFE-RESTORE "}, nil
	})

	ok, err := rotator.CaamSupportsSafeRestore(context.Background())
	if err != nil {
		t.Fatalf("CaamSupportsSafeRestore() error = %v", err)
	}
	if !ok {
		t.Fatal("CaamSupportsSafeRestore() = false, want true")
	}
}

func TestCaamSupportsSafeRestoreFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		probe func(context.Context) ([]string, error)
		want  error
	}{
		{
			name: "capability absent",
			probe: func(context.Context) ([]string, error) {
				return []string{"robot"}, nil
			},
		},
		{
			name: "probe error",
			probe: func(context.Context) ([]string, error) {
				return nil, errors.New("caam unavailable")
			},
			want: errors.New("caam unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rotator := NewAccountRotator().WithCaamCapabilityProber(tt.probe)
			ok, err := rotator.CaamSupportsSafeRestore(context.Background())
			if ok {
				t.Fatal("CaamSupportsSafeRestore() = true, want false")
			}
			if tt.want == nil {
				if err != nil {
					t.Fatalf("CaamSupportsSafeRestore() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want.Error() {
				t.Fatalf("CaamSupportsSafeRestore() error = %v, want %v", err, tt.want)
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
	wantHome := filepath.Join(filepath.Dir(caamPath), "profiles", "acctB", "codex_home")
	if record.CodexHome != wantHome {
		t.Errorf("pane-local CODEX_HOME = %q, want CAAM-owned %q", record.CodexHome, wantHome)
	}
	// CAAM's profile-status command, not a global switch or credential export,
	// must be the only isolated-profile operation on this path.
	log, _ := os.ReadFile(marker)
	if !contains(string(log), "profile status codex acctB") || contains(string(log), "switch") || contains(string(log), "creds") {
		t.Errorf("pane-local rotation used the wrong CAAM contract: %q", string(log))
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

// bd-91hy2: the inspector used `tmux show-environment -t <target> CODEX_HOME`,
// whose -t is a target-SESSION, not a target-pane. CODEX_HOME reaches a pane as
// a command-line assignment on the codex process, so it lives in that process's
// environment and is invisible to tmux. The inspector was therefore
// structurally incapable of ever returning an isolated pane, which would have
// made guardAutoRotation refuse EVERY automatic Codex rotation with
// "shared_global_codex_home".
//
// The acceptance criterion is exactly this: a provisioned pane must be
// reported isolated.
func TestProvisionedCodexProbe_ReportsProvisionedPaneAsIsolated(t *testing.T) {
	baseDir := t.TempDir()
	const session = "swarm"

	// The pane whose home we provision. Provision it through the SAME key the
	// rotation path would use, derived the same way: the limit detector reports
	// SessionPane as "session:<window>.<pane>", and pane-local rotation feeds
	// splitSessionPane's second half to the provisioner. Hardcoding a key here
	// would let the probe and the provisioner drift apart while the test still
	// passed — which is exactly how the original defect survived.
	livePane := tmux.Pane{ID: "%1", Index: 2, WindowIndex: 1, Type: tmux.AgentType("cod")}
	_, rotationPaneKey := splitSessionPane(codexPaneSessionTarget(session, livePane))

	provisioner := NewCodexHomeProvisioner(baseDir)
	home, err := provisioner.ProvisionPaneHome(context.Background(), session, rotationPaneKey, "")
	if err != nil {
		t.Fatalf("ProvisionPaneHome: %v", err)
	}

	probe := provisionedCodexProbe{baseDir: baseDir}

	t.Run("the inspector key matches the rotation key", func(t *testing.T) {
		if got := codexPaneKey(livePane); got != rotationPaneKey {
			t.Fatalf("codexPaneKey = %q but rotation provisions under %q; the inspector would stat the wrong directory and report every pane unisolated", got, rotationPaneKey)
		}
	})

	t.Run("a provisioned pane is isolated", func(t *testing.T) {
		got, set, err := probe.PaneCodexHome(session, livePane)
		if err != nil {
			t.Fatalf("PaneCodexHome: %v", err)
		}
		if !set {
			t.Fatal("a pane with a provisioned CODEX_HOME was reported as having none; the inspector can never approve a rotation")
		}
		if got != home {
			t.Fatalf("CodexHome = %q, want the provisioned %q", got, home)
		}
		if isGlobalCodexHome(got) {
			t.Fatalf("provisioned home %q was classified as the shared global ~/.codex", got)
		}
	})

	t.Run("an unprovisioned pane is not isolated", func(t *testing.T) {
		_, set, err := probe.PaneCodexHome(session, tmux.Pane{ID: "%9", Index: 9, Type: tmux.AgentType("cod")})
		if err != nil {
			t.Fatalf("PaneCodexHome: %v", err)
		}
		if set {
			t.Fatal("a pane that was never provisioned was reported isolated; the guard must fail closed")
		}
	})

	t.Run("the reported identity round-trips to the provisioned key", func(t *testing.T) {
		// The guard reads these infos while rotation keys the on-disk home
		// from a SessionPane of the same shape, so the identity the inspector
		// reports must parse back to the key the home lives under.
		target := codexPaneSessionTarget(session, livePane)
		gotSession, gotPane := splitSessionPane(target)
		if gotSession != session || gotPane != rotationPaneKey {
			t.Fatalf("splitSessionPane(%q) = (%q, %q), want (%q, %q)", target, gotSession, gotPane, session, rotationPaneKey)
		}
	})

	t.Run("the inspector reports the isolated pane end to end", func(t *testing.T) {
		unprovisioned := tmux.Pane{ID: "%2", Index: 3, WindowIndex: 1, Type: tmux.AgentType("cod")}
		inspector := newTmuxCodexHomeInspector(session, stubPaneProbe{
			probe: probe,
			panes: []tmux.Pane{livePane, unprovisioned},
		})
		infos, err := inspector()
		if err != nil {
			t.Fatalf("inspector: %v", err)
		}
		if len(infos) != 2 {
			t.Fatalf("got %d codex panes, want 2", len(infos))
		}
		if !infos[0].IsIsolated() {
			t.Fatal("the provisioned pane was not reported isolated")
		}
		if want := codexPaneSessionTarget(session, livePane); infos[0].SessionPane != want {
			t.Fatalf("SessionPane = %q, want %q (must round-trip to the provisioned key)", infos[0].SessionPane, want)
		}
		if infos[1].IsIsolated() {
			t.Fatal("the unprovisioned pane was reported isolated")
		}
	})
}

// stubPaneProbe supplies a fixed pane list while delegating the home lookup to
// the real provisioned-directory probe, so the end-to-end assertion exercises
// the actual isolation logic without a live tmux server.
type stubPaneProbe struct {
	probe provisionedCodexProbe
	panes []tmux.Pane
}

func (s stubPaneProbe) GetPanes(string) ([]tmux.Pane, error) { return s.panes, nil }

func (s stubPaneProbe) PaneCodexHome(session string, pane tmux.Pane) (string, bool, error) {
	return s.probe.PaneCodexHome(session, pane)
}
