package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestCoordinatorFeatureKeys(t *testing.T) {
	tests := []struct {
		name     string
		feature  string
		enable   bool
		interval string
		want     [][2]string
		wantErr  string
	}{
		{name: "enable auto assign", feature: "auto-assign", enable: true, want: [][2]string{{"auto_assign", "true"}}},
		{name: "disable auto assign", feature: "auto-assign", want: [][2]string{{"auto_assign", "false"}}},
		{name: "enable digest", feature: "digest", enable: true, want: [][2]string{{"send_digests", "true"}}},
		{name: "enable digest interval", feature: "digest", enable: true, interval: "30m", want: [][2]string{{"send_digests", "true"}, {"digest_interval", `"30m"`}}},
		{name: "disable digest ignores no interval", feature: "digest", want: [][2]string{{"send_digests", "false"}}},
		{name: "enable conflict notify", feature: "conflict-notify", enable: true, want: [][2]string{{"conflict_notify", "true"}}},
		{name: "disable conflict negotiate", feature: "conflict-negotiate", want: [][2]string{{"conflict_negotiate", "false"}}},
		{name: "enable mail nudge", feature: "mail-nudge", enable: true, want: [][2]string{{"mail_nudge", "true"}}},
		{name: "invalid duration", feature: "digest", enable: true, interval: "later", wantErr: "invalid --interval"},
		{name: "zero duration", feature: "digest", enable: true, interval: "0s", wantErr: "must be at least"},
		{name: "negative duration", feature: "digest", enable: true, interval: "-1s", wantErr: "must be at least"},
		{name: "below runtime minimum", feature: "digest", enable: true, interval: (coordinator.MinDigestInterval - time.Second).String(), wantErr: "must be at least"},
		{name: "interval on wrong feature", feature: "auto-assign", enable: true, interval: "30m", wantErr: "only valid with the digest"},
		{name: "unknown feature", feature: "missing", enable: true, wantErr: "unknown feature"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := coordinatorFeatureKeys(tt.feature, tt.enable, tt.interval)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("coordinatorFeatureKeys() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("coordinatorFeatureKeys() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("coordinatorFeatureKeys() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRunCoordinatorToggleValidationClassifiesInvalidFlag(t *testing.T) {
	tests := []struct {
		name     string
		feature  string
		interval string
	}{
		{name: "unknown feature", feature: "missing"},
		{name: "invalid digest interval", feature: "digest", interval: "later"},
		{name: "interval below runtime minimum", feature: "digest", interval: "1s"},
		{name: "interval on wrong feature", feature: "auto-assign", interval: "30m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCoordinatorToggle(nil, []string{tt.feature}, true, tt.interval)
			if err == nil {
				t.Fatal("runCoordinatorToggle() unexpectedly succeeded")
			}
			if !errors.Is(err, errCLIInvalidInput) {
				t.Fatalf("runCoordinatorToggle() error = %v, want errCLIInvalidInput", err)
			}
			code, _ := classifyRobotExecuteError(err)
			if code != robot.ErrCodeInvalidFlag {
				t.Fatalf("classifyRobotExecuteError() code = %q, want %q", code, robot.ErrCodeInvalidFlag)
			}
		})
	}
}

func TestRunCoordinatorTogglePersistsSelectedConfig(t *testing.T) {
	previousConfigFile := cfgFile
	previousJSON := jsonOutput
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		jsonOutput = previousJSON
	})

	path := filepath.Join(t.TempDir(), "selected.toml")
	original := "# retained\n[coordinator]\nsend_digests = false # retained comment\nauto_assign = false\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write selected config: %v", err)
	}
	cfgFile = path
	jsonOutput = true
	output, err := captureStdout(t, func() error {
		return runCoordinatorToggle(nil, []string{"digest"}, true, "30m")
	})
	if err != nil {
		t.Fatalf("runCoordinatorToggle: %v", err)
	}
	var envelope struct {
		Feature    string            `json:"feature"`
		Enabled    bool              `json:"enabled"`
		Persisted  bool              `json:"persisted"`
		ConfigPath string            `json:"config_path"`
		Written    map[string]string `json:"written"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, output)
	}
	wantWritten := map[string]string{"send_digests": "true", "digest_interval": `"30m"`}
	if envelope.Feature != "digest" || !envelope.Enabled || !envelope.Persisted || envelope.ConfigPath != path || !reflect.DeepEqual(envelope.Written, wantWritten) {
		t.Fatalf("toggle envelope = %+v, want path=%s written=%v", envelope, path, wantWritten)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read selected config: %v", err)
	}
	if !strings.Contains(string(data), "# retained") || !strings.Contains(string(data), "send_digests = true # retained comment") {
		t.Fatalf("selected config was not surgically updated:\n%s", data)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Coordinator.SendDigests || loaded.Coordinator.DigestInterval != 30*time.Minute || loaded.Coordinator.AutoAssign {
		t.Fatalf("persisted coordinator config = %+v", loaded.Coordinator)
	}
}

func TestCoordinatorRunCommandExposesDeterministicOnceMode(t *testing.T) {
	cmd := newCoordinatorRunCmd()
	if cmd.Use != "run [session]" {
		t.Fatalf("Use=%q", cmd.Use)
	}
	if flag := cmd.Flags().Lookup("once"); flag == nil || flag.DefValue != "false" {
		t.Fatalf("--once flag = %+v", flag)
	}
}

func TestCoordinatorBootstrapImportsOnlyExactInProjectPaneIdentities(t *testing.T) {
	projectDir := t.TempDir()
	otherDir := t.TempDir()
	previousPath := coordinatorPaneCurrentDir
	coordinatorPaneCurrentDir = func(paneID string) (string, error) {
		if paneID == "%66" {
			return otherDir, nil
		}
		return projectDir, nil
	}
	t.Cleanup(func() { coordinatorPaneCurrentDir = previousPath })
	panes := []tmux.Pane{
		{ID: "%44", PID: 4400}, {ID: "%45", PID: 4500}, {ID: "%46", PID: 4600},
		{ID: "%51", PID: 5100}, {ID: "%66", PID: 6600}, {ID: "%129", PID: 12900},
	}
	specs := []string{"%44=AzureCreek", "%45=TopazAnchor", "%46=CrimsonOak", "%51=IvoryTern", "%129=CoralFox"}
	bindings, public, err := validateCoordinatorBootstrapBindings(t.Context(), projectDir, panes, specs)
	if err != nil {
		t.Fatalf("validateCoordinatorBootstrapBindings: %v", err)
	}
	if len(bindings) != 5 || len(public) != 5 {
		t.Fatalf("bindings = %+v, want exact five", public)
	}
	if _, ok := bindings["%66"]; ok {
		t.Fatal("unbound LMS pane %66 was imported")
	}
	if bindings["%44"].AgentName != "AzureCreek" || bindings["%129"].AgentName != "CoralFox" {
		t.Fatalf("exact identities changed: %+v", public)
	}
	_, _, err = validateCoordinatorBootstrapBindings(t.Context(), projectDir, panes, append(specs, "%66=WrongProject"))
	if err == nil || !strings.Contains(err.Error(), "cross-project") {
		t.Fatalf("cross-project pane binding error = %v", err)
	}
}

func TestCoordinatorBootstrapPanesFindsExplicitBindingInDetachedSession(t *testing.T) {
	previousAll := coordinatorGetAllPanes
	t.Cleanup(func() { coordinatorGetAllPanes = previousAll })
	coordinatorGetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{
			"bbi-infrastructure":                 {{ID: "%44", PID: 4400}},
			"bbi-infrastructure--lms-argo-wedge": {{ID: "%155", PID: 15500}},
			"mereka-lms":                         {{ID: "%66", PID: 6600}},
		}, nil
	}

	panes, err := coordinatorBootstrapPanes(t.Context(), []tmux.Pane{{ID: "%44", PID: 4400}}, []string{"%44=AzureCreek", "%155=YellowOx"})
	if err != nil {
		t.Fatalf("coordinatorBootstrapPanes: %v", err)
	}
	got := make(map[string]int, len(panes))
	for _, pane := range panes {
		got[pane.ID] = pane.PID
	}
	if got["%44"] != 4400 || got["%155"] != 15500 {
		t.Fatalf("expanded panes = %+v, want primary and explicitly named detached pane available", got)
	}
}

func TestLoadBootstrappedCoordinatorFailsClosedWithoutIdentity(t *testing.T) {
	isolateSessionAgentStorage(t)
	projectDir := t.TempDir()
	coord, state, err := loadBootstrappedCoordinator("missing-coordinator", projectDir)
	if err == nil || coord != nil || state != nil || !strings.Contains(err.Error(), "not bootstrapped") {
		t.Fatalf("loadBootstrappedCoordinator = (%v, %+v, %v), want fail closed", coord, state, err)
	}
}

func TestLoadBootstrappedCoordinatorHydratesExactPersistedSender(t *testing.T) {
	isolateSessionAgentStorage(t)
	projectDir := t.TempDir()
	state := &agentmail.CoordinatorSessionState{
		Version: 1, SessionName: "persisted-coordinator", ProjectDir: projectDir,
		MailProjectKey: "/repos/github.com/biji-biji-initiative/bbi-infrastructure",
		Coordinator: agentmail.CoordinatorIdentity{
			AgentName: "SilverHarbor", RegistrationToken: "coordinator-token", Program: "ntm", Model: "coordinator",
		},
		PaneBindings: map[string]agentmail.CoordinatorPaneBinding{
			"%44": {AgentName: "AzureCreek", PanePID: 4400, ProjectDir: projectDir},
		},
	}
	if err := agentmail.SaveCoordinatorSessionState(state); err != nil {
		t.Fatal(err)
	}
	coord, loaded, err := loadBootstrappedCoordinator(state.SessionName, projectDir)
	if err != nil {
		t.Fatalf("loadBootstrappedCoordinator: %v", err)
	}
	if coord == nil || loaded.MailProjectKey == loaded.ProjectDir || loaded.Coordinator.AgentName != "SilverHarbor" {
		t.Fatalf("loaded coordinator scope = %+v", loaded)
	}
}

func stubCoordinatorLiveTopology(t *testing.T, panes []tmux.Pane, paths map[string]string) {
	t.Helper()
	previousExists := coordinatorSessionExists
	previousPanes := coordinatorGetPanes
	previousPath := coordinatorPaneCurrentDir
	coordinatorSessionExists = func(string) bool { return true }
	coordinatorGetPanes = func(string) ([]tmux.Pane, error) { return append([]tmux.Pane(nil), panes...), nil }
	coordinatorPaneCurrentDir = func(paneID string) (string, error) {
		path, ok := paths[paneID]
		if !ok {
			return "", errors.New("missing pane path")
		}
		return path, nil
	}
	t.Cleanup(func() {
		coordinatorSessionExists = previousExists
		coordinatorGetPanes = previousPanes
		coordinatorPaneCurrentDir = previousPath
	})
}

func TestCoordinatorRunFailureIncludesAssignmentFailures(t *testing.T) {
	tests := []struct {
		name        string
		assignments []coordinator.AssignmentResult
		cycleErr    error
		wantError   bool
	}{
		{name: "empty success"},
		{name: "assignment success", assignments: []coordinator.AssignmentResult{{Success: true}}},
		{name: "pressure deferral", assignments: []coordinator.AssignmentResult{{Deferred: true, ReasonCode: "critical_pressure"}}},
		{name: "assignment failure", assignments: []coordinator.AssignmentResult{{Success: false}}, wantError: true},
		{name: "cycle failure", cycleErr: errors.New("observe failed"), wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := coordinatorRunFailure(tc.assignments, tc.cycleErr); (got != nil) != tc.wantError {
				t.Fatalf("coordinatorRunFailure()=%v, wantError=%t", got, tc.wantError)
			}
		})
	}
}

func TestResolveCoordinatorProjectKeyPreservesExactResolvedSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	originalConfig := cfg
	t.Cleanup(func() { cfg = originalConfig })

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}
	session := "coordinator-exact-session"
	competing := filepath.Join(projectsBase, session+"-prefixed")
	if err := os.MkdirAll(filepath.Join(competing, ".git"), 0o755); err != nil {
		t.Fatalf("create competing project: %v", err)
	}
	authoritative := t.TempDir()
	if err := os.MkdirAll(filepath.Join(authoritative, ".git"), 0o755); err != nil {
		t.Fatalf("create authoritative project marker: %v", err)
	}
	saveSessionAgentForTest(t, session, authoritative, "GreenCastle")

	got, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveCoordinatorProjectKey: %v", err)
	}
	if got != authoritative {
		t.Fatalf("resolved project = %q, want exact session project %q (not %q)", got, authoritative, competing)
	}
}

func TestResolveCoordinatorProjectKeyPrefersCommonLivePaneRoot(t *testing.T) {
	isolateSessionAgentStorage(t)
	const session = "coordinator-live-authority"
	staleProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staleProject, ".git"), 0o755); err != nil {
		t.Fatalf("create stale project marker: %v", err)
	}
	liveProject := t.TempDir()
	for _, dir := range []string{filepath.Join(liveProject, ".git"), filepath.Join(liveProject, ".beads"), filepath.Join(liveProject, "internal", "cli"), filepath.Join(liveProject, "internal", "coordinator")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create live project path: %v", err)
		}
	}
	saveSessionAgentForTest(t, session, staleProject, "GreenCastle")
	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%70"}, {ID: "%71"}}, map[string]string{
		"%70": filepath.Join(liveProject, "internal", "cli"),
		"%71": filepath.Join(liveProject, "internal", "coordinator"),
	})

	got, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveCoordinatorProjectKey: %v", err)
	}
	if got != liveProject {
		t.Fatalf("resolved project = %q, want live root %q instead of stale registry %q", got, liveProject, staleProject)
	}
}

func TestResolveCoordinatorProjectKeyRejectsMixedLiveRoots(t *testing.T) {
	const session = "coordinator-mixed-roots"
	first := t.TempDir()
	second := t.TempDir()
	for _, project := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
			t.Fatalf("create project marker: %v", err)
		}
	}
	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%80"}, {ID: "%81"}}, map[string]string{
		"%80": first,
		"%81": second,
	})

	_, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err == nil || !strings.Contains(err.Error(), "multiple project roots") {
		t.Fatalf("mixed live roots error = %v", err)
	}
}

// TestResolveCoordinatorProjectKeyAcceptsLinkedWorktrees — issue #252: an
// `ntm spawn --worktrees` session puts the controller in the base checkout and
// workers in linked worktrees of the same repository. That is one physical
// repo (one git common directory), so coordinator status must resolve it to
// the base checkout instead of rejecting it as multiple project roots.
func TestResolveCoordinatorProjectKeyAcceptsLinkedWorktrees(t *testing.T) {
	isolateSessionAgentStorage(t)
	const session = "coordinator-worktrees"
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	gitCommand := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = base
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	gitCommand("init", "-b", "main")
	gitCommand("config", "user.email", "test@test.com")
	gitCommand("config", "user.name", "Test")
	gitCommand("commit", "--allow-empty", "-m", "init")
	worktree := filepath.Join(base, ".ntm", "worktrees", session, "cod_1")
	gitCommand("worktree", "add", "-b", "cod_1", worktree)

	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%90"}, {ID: "%91"}}, map[string]string{
		"%90": base,
		"%91": worktree,
	})

	got, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveCoordinatorProjectKey rejected linked worktrees: %v", err)
	}
	if got != base {
		t.Fatalf("resolved project = %q, want base checkout %q", got, base)
	}
}

// TestCoordinatorConfigFromTOMLPropagatesValues — translator passes through
// every field when the TOML carries explicit values. Anchors the contract
// `coordinator status --json` relies on.
func TestCoordinatorConfigFromTOMLPropagatesValues(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:      30 * time.Second,
		DigestInterval:    30 * time.Minute,
		AutoAssign:        true,
		IdleThreshold:     300,
		AssignOnlyIdle:    false,
		ConflictNotify:    false,
		ConflictNegotiate: true,
		SendDigests:       true,
		HumanAgent:        "Operator",
		MailNudge:         true,
	}
	got := coordinatorConfigFromTOML(toml, coordinator.DefaultCoordinatorConfig())
	if got.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", got.PollInterval)
	}
	if got.DigestInterval != 30*time.Minute {
		t.Errorf("DigestInterval = %s, want 30m", got.DigestInterval)
	}
	if !got.AutoAssign {
		t.Errorf("AutoAssign = false, want true")
	}
	if got.IdleThreshold != 300 {
		t.Errorf("IdleThreshold = %v, want 300", got.IdleThreshold)
	}
	if got.AssignOnlyIdle {
		t.Errorf("AssignOnlyIdle = true, want false")
	}
	if got.ConflictNotify {
		t.Errorf("ConflictNotify = true, want false")
	}
	if !got.ConflictNegotiate {
		t.Errorf("ConflictNegotiate = false, want true")
	}
	if !got.SendDigests {
		t.Errorf("SendDigests = false, want true")
	}
	if got.HumanAgent != "Operator" {
		t.Errorf("HumanAgent = %q, want %q", got.HumanAgent, "Operator")
	}
	if !got.MailNudge {
		t.Error("MailNudge = false, want true")
	}
}

// TestCoordinatorConfigFromTOMLClampsBelowMinimumDurations — anything below
// the runtime minimums (which would otherwise panic time.NewTicker) is clamped
// up. This matches the validation inside SessionCoordinator.Start.
func TestCoordinatorConfigFromTOMLClampsBelowMinimumDurations(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:   1 * time.Millisecond, // below MinPollInterval
		DigestInterval: 1 * time.Second,      // below MinDigestInterval
		HumanAgent:     "X",
	}
	got := coordinatorConfigFromTOML(toml, coordinator.DefaultCoordinatorConfig())
	if got.PollInterval != coordinator.MinPollInterval {
		t.Errorf("PollInterval = %s, want clamped to %s", got.PollInterval, coordinator.MinPollInterval)
	}
	if got.DigestInterval != coordinator.MinDigestInterval {
		t.Errorf("DigestInterval = %s, want clamped to %s", got.DigestInterval, coordinator.MinDigestInterval)
	}
}

// TestCoordinatorConfigFromTOMLEmptyHumanAgentFallsBack — explicitly empty
// `human_agent = ""` in TOML must fall back to the runtime default. Otherwise
// digest delivery would silently target an empty agent name.
func TestCoordinatorConfigFromTOMLEmptyHumanAgentFallsBack(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:   coordinator.MinPollInterval,
		DigestInterval: coordinator.MinDigestInterval,
		HumanAgent:     "  ", // whitespace-only also counts as empty
	}
	defaults := coordinator.DefaultCoordinatorConfig()
	got := coordinatorConfigFromTOML(toml, defaults)
	if got.HumanAgent != defaults.HumanAgent {
		t.Errorf("HumanAgent = %q, want fallback %q", got.HumanAgent, defaults.HumanAgent)
	}
}

// TestCoordinatorMirrorMatchesRuntime — config.DefaultCoordinatorConfig() must
// stay in lock-step with coordinator.DefaultCoordinatorConfig(). Drift here
// means a user with no [coordinator] TOML section sees one set of "defaults"
// reflected in `am config validate`, and a different set actually enforced at
// runtime — exactly the symptom #111 was filed for. This test lives in the cli
// package because it can import both internal/config and internal/coordinator
// without forming a cycle (config → coordinator → robot → config).
func TestCoordinatorMirrorMatchesRuntime(t *testing.T) {
	mirror := config.DefaultCoordinatorConfig()
	runtime := coordinator.DefaultCoordinatorConfig()

	if mirror.PollInterval != runtime.PollInterval {
		t.Errorf("PollInterval drift: mirror=%s runtime=%s", mirror.PollInterval, runtime.PollInterval)
	}
	if mirror.DigestInterval != runtime.DigestInterval {
		t.Errorf("DigestInterval drift: mirror=%s runtime=%s", mirror.DigestInterval, runtime.DigestInterval)
	}
	if mirror.AutoAssign != runtime.AutoAssign {
		t.Errorf("AutoAssign drift: mirror=%v runtime=%v", mirror.AutoAssign, runtime.AutoAssign)
	}
	if mirror.IdleThreshold != runtime.IdleThreshold {
		t.Errorf("IdleThreshold drift: mirror=%v runtime=%v", mirror.IdleThreshold, runtime.IdleThreshold)
	}
	if mirror.AssignOnlyIdle != runtime.AssignOnlyIdle {
		t.Errorf("AssignOnlyIdle drift: mirror=%v runtime=%v", mirror.AssignOnlyIdle, runtime.AssignOnlyIdle)
	}
	if mirror.ConflictNotify != runtime.ConflictNotify {
		t.Errorf("ConflictNotify drift: mirror=%v runtime=%v", mirror.ConflictNotify, runtime.ConflictNotify)
	}
	if mirror.ConflictNegotiate != runtime.ConflictNegotiate {
		t.Errorf("ConflictNegotiate drift: mirror=%v runtime=%v", mirror.ConflictNegotiate, runtime.ConflictNegotiate)
	}
	if mirror.SendDigests != runtime.SendDigests {
		t.Errorf("SendDigests drift: mirror=%v runtime=%v", mirror.SendDigests, runtime.SendDigests)
	}
	if mirror.HumanAgent != runtime.HumanAgent {
		t.Errorf("HumanAgent drift: mirror=%q runtime=%q", mirror.HumanAgent, runtime.HumanAgent)
	}
}

func TestFormatIdleDuration(t *testing.T) {

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		// Less than 1 minute - seconds
		{name: "0 seconds", duration: 0, expected: "0s"},
		{name: "1 second", duration: 1 * time.Second, expected: "1s"},
		{name: "30 seconds", duration: 30 * time.Second, expected: "30s"},
		{name: "59 seconds", duration: 59 * time.Second, expected: "59s"},

		// 1 minute to less than 1 hour - minutes
		{name: "1 minute", duration: 1 * time.Minute, expected: "1m"},
		{name: "5 minutes", duration: 5 * time.Minute, expected: "5m"},
		{name: "30 minutes", duration: 30 * time.Minute, expected: "30m"},
		{name: "59 minutes", duration: 59 * time.Minute, expected: "59m"},
		{name: "59 min 59 sec", duration: 59*time.Minute + 59*time.Second, expected: "59m"},

		// 1+ hours - hours and minutes
		{name: "1 hour", duration: 1 * time.Hour, expected: "1h0m"},
		{name: "1 hour 30 min", duration: 1*time.Hour + 30*time.Minute, expected: "1h30m"},
		{name: "2 hours", duration: 2 * time.Hour, expected: "2h0m"},
		{name: "2 hours 15 min", duration: 2*time.Hour + 15*time.Minute, expected: "2h15m"},
		{name: "24 hours", duration: 24 * time.Hour, expected: "24h0m"},
		{name: "48 hours", duration: 48 * time.Hour, expected: "48h0m"},
		{name: "100 hours 45 min", duration: 100*time.Hour + 45*time.Minute, expected: "100h45m"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatIdleDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatIdleDuration(%v) = %q; want %q", tc.duration, result, tc.expected)
			}
		})
	}
}
