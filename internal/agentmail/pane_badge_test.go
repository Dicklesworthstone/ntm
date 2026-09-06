package agentmail

// Regression tests for ntm#312: pane identity badges reconcile NTM's
// assigned name against the canonical Agent Mail identity file, and the
// outcome table in the issue is the contract.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func isolateBadgeDirs(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	return home
}

func writeStructuredIdentity(t *testing.T, projectKey, paneID string, record PaneIdentityRecord) string {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	path, err := MirrorPaneIdentityReceipt(projectKey, paneID, data)
	if err != nil {
		t.Fatalf("write structured identity: %v", err)
	}
	return path
}

func badgeRegistry(session, projectKey, paneID, name string, pid int) *SessionAgentRegistry {
	registry := NewSessionAgentRegistry(session, projectKey)
	registry.AddAgent(session+"__cc_1", paneID, name)
	registry.SetPanePID(paneID, pid)
	return registry
}

func reconcile(t *testing.T, registry *SessionAgentRegistry, projectKey string, pane tmux.Pane, lifecycle PaneLifecycle) PaneBadgeRecord {
	t.Helper()
	return ReconcilePane(registry, ReconcileInput{
		SessionName: registry.SessionName,
		ProjectKeys: []string{projectKey},
		SocketPath:  "/tmp/tmux-1000/default",
		Pane:        pane,
		Lifecycle:   lifecycle,
	}, PaneBadgeRecord{})
}

// TestReconcilePane_OutcomeTable pins the display/diagnostic table from
// ntm#312 row by row.
func TestReconcilePane_OutcomeTable(t *testing.T) {
	isolateBadgeDirs(t)
	projectKey := t.TempDir()
	const session = "badge_table"
	runningPane := tmux.Pane{ID: "%7", Index: 1, PID: 4242, Command: "claude", Type: tmux.AgentClaude}
	verified := PaneIdentityRecord{Name: "BlueLake", SessionName: session, PaneID: "%7", PanePID: 4242, SocketPath: "/tmp/tmux-1000/default"}

	cases := []struct {
		name         string
		registry     func() *SessionAgentRegistry
		file         func(t *testing.T)
		pane         tmux.Pane
		wantAssign   PaneAssignmentState
		wantObserve  PaneObservationState
		wantVerified bool
		wantLabel    string
		wantState    string
	}{
		{
			name:         "current assignment, verified matching binding",
			registry:     func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 4242) },
			file:         func(t *testing.T) { writeStructuredIdentity(t, projectKey, "%7", verified) },
			pane:         runningPane,
			wantAssign:   PaneAssignmentCurrent,
			wantObserve:  PaneObservationMatched,
			wantVerified: true,
			wantLabel:    "[BlueLake]",
			wantState:    "matched",
		},
		{
			name:     "current assignment, matching legacy name",
			registry: func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 4242) },
			file: func(t *testing.T) {
				if _, err := WriteIdentity(projectKey, "%7", "BlueLake"); err != nil {
					t.Fatal(err)
				}
			},
			pane:        runningPane,
			wantAssign:  PaneAssignmentCurrent,
			wantObserve: PaneObservationLegacyUnverified,
			wantLabel:   "[BlueLake]",
			wantState:   "legacy-unverified",
		},
		{
			name:     "current assignment, different name in file",
			registry: func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 4242) },
			file: func(t *testing.T) {
				rec := verified
				rec.Name = "RedFox"
				writeStructuredIdentity(t, projectKey, "%7", rec)
			},
			pane:        runningPane,
			wantAssign:  PaneAssignmentCurrent,
			wantObserve: PaneObservationNameDisagreement,
			// Binding verification is reported separately from name
			// agreement: the file's binding facts do describe this pane —
			// for the other name.
			wantVerified: true,
			wantLabel:    "[BlueLake!]",
			wantState:    "name-disagreement",
		},
		{
			name:        "current assignment, file missing",
			registry:    func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 4242) },
			file:        func(*testing.T) {},
			pane:        runningPane,
			wantAssign:  PaneAssignmentCurrent,
			wantObserve: PaneObservationMissingFile,
			wantLabel:   "[BlueLake!]",
			wantState:   "missing-file",
		},
		{
			name:     "current assignment, malformed file",
			registry: func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 4242) },
			file: func(t *testing.T) {
				if _, err := MirrorPaneIdentityReceipt(projectKey, "%7", []byte("{not json")); err != nil {
					t.Fatal(err)
				}
			},
			pane:        runningPane,
			wantAssign:  PaneAssignmentCurrent,
			wantObserve: PaneObservationInvalidFile,
			wantLabel:   "[BlueLake!]",
			wantState:   "invalid-file",
		},
		{
			name:     "current assignment, structured record lacks binding fields",
			registry: func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 4242) },
			file: func(t *testing.T) {
				writeStructuredIdentity(t, projectKey, "%7", PaneIdentityRecord{Name: "BlueLake"})
			},
			pane:        runningPane,
			wantAssign:  PaneAssignmentCurrent,
			wantObserve: PaneObservationBindingUnverifiable,
			wantLabel:   "[BlueLake!]",
			wantState:   "binding-unverifiable",
		},
		{
			name:     "current assignment, structured record identifies another generation",
			registry: func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 4242) },
			file: func(t *testing.T) {
				rec := verified
				rec.PanePID = 1
				writeStructuredIdentity(t, projectKey, "%7", rec)
			},
			pane:        runningPane,
			wantAssign:  PaneAssignmentCurrent,
			wantObserve: PaneObservationBindingStale,
			wantLabel:   "[BlueLake!]",
			wantState:   "binding-stale",
		},
		{
			name:         "stale assignment is never displayed as current",
			registry:     func() *SessionAgentRegistry { return badgeRegistry(session, projectKey, "%7", "BlueLake", 999) },
			file:         func(t *testing.T) { writeStructuredIdentity(t, projectKey, "%7", verified) },
			pane:         runningPane,
			wantAssign:   PaneAssignmentStale,
			wantObserve:  PaneObservationMatched,
			wantVerified: true,
			wantLabel:    "[?!]",
			wantState:    "assignment-stale",
		},
		{
			name:        "unregistered agent pane shows the unknown indication",
			registry:    func() *SessionAgentRegistry { return NewSessionAgentRegistry(session, projectKey) },
			file:        func(*testing.T) {},
			pane:        runningPane,
			wantAssign:  PaneAssignmentUnregistered,
			wantObserve: PaneObservationSkipped,
			wantLabel:   "[?!]",
			wantState:   "assignment-unregistered",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(CanonicalIdentityPath(projectKey, "%7"))
			tc.file(t)
			rec := reconcile(t, tc.registry(), projectKey, tc.pane, "")
			if rec.AssignmentState != tc.wantAssign {
				t.Errorf("assignment = %q, want %q (problems %v)", rec.AssignmentState, tc.wantAssign, rec.Problems)
			}
			if rec.ObservationState != tc.wantObserve {
				t.Errorf("observation = %q, want %q (problems %v)", rec.ObservationState, tc.wantObserve, rec.Problems)
			}
			if rec.BindingVerified != tc.wantVerified {
				t.Errorf("binding_verified = %v, want %v", rec.BindingVerified, tc.wantVerified)
			}
			if rec.Label != tc.wantLabel {
				t.Errorf("label = %q, want %q", rec.Label, tc.wantLabel)
			}
			if rec.State != tc.wantState {
				t.Errorf("state = %q, want %q", rec.State, tc.wantState)
			}
			if rec.Lifecycle != PaneLifecycleRunning {
				t.Errorf("lifecycle = %q, want running", rec.Lifecycle)
			}
			if tc.wantAssign != PaneAssignmentUnregistered && rec.AssignedName != "BlueLake" {
				t.Errorf("assigned name %q was relabelled", rec.AssignedName)
			}
		})
	}
}

// TestReconcilePane_DisagreementRetainsAssignmentAndKeepsBothProblems: a
// different name AND a stale binding are both reported; the assignment is
// untouched and the registry is never mutated.
func TestReconcilePane_DisagreementRetainsAssignmentAndKeepsBothProblems(t *testing.T) {
	isolateBadgeDirs(t)
	projectKey := t.TempDir()
	const session = "badge_multi"
	registry := badgeRegistry(session, projectKey, "%3", "BlueLake", 100)
	before, _ := json.Marshal(registry)
	writeStructuredIdentity(t, projectKey, "%3", PaneIdentityRecord{Name: "RedFox", SessionName: session, PaneID: "%3", PanePID: 555})

	rec := reconcile(t, registry, projectKey, tmux.Pane{ID: "%3", Index: 2, PID: 100, Command: "codex", Type: tmux.AgentCodex}, "")
	if rec.ObservationState != PaneObservationNameDisagreement {
		t.Fatalf("observation = %q, want name-disagreement", rec.ObservationState)
	}
	if rec.AssignedName != "BlueLake" || rec.ResolvedName != "RedFox" {
		t.Fatalf("assigned/resolved = %q/%q", rec.AssignedName, rec.ResolvedName)
	}
	joined := strings.Join(rec.Problems, "\n")
	if !strings.Contains(joined, "name-disagreement") || !strings.Contains(joined, "binding-stale") {
		t.Fatalf("problems must keep both issues, got %v", rec.Problems)
	}
	if rec.Label != "[BlueLake!]" {
		t.Fatalf("label = %q", rec.Label)
	}
	after, _ := json.Marshal(registry)
	if string(before) != string(after) {
		t.Fatal("reconciliation mutated the registry")
	}
	if rec.LastSuccessAt == nil {
		t.Fatal("a successfully observed disagreement must advance last_success_at")
	}
}

// TestReconcilePane_LifecycleMarkers: lifecycle supplements the badge and
// stays visible when the assignment is unresolved.
func TestReconcilePane_LifecycleMarkers(t *testing.T) {
	isolateBadgeDirs(t)
	projectKey := t.TempDir()
	const session = "badge_lifecycle"
	registry := badgeRegistry(session, projectKey, "%1", "BlueLake", 10)
	if _, err := WriteIdentity(projectKey, "%1", "BlueLake"); err != nil {
		t.Fatal(err)
	}
	base := tmux.Pane{ID: "%1", Index: 1, PID: 10, Command: "claude", Type: tmux.AgentClaude}

	if got := reconcile(t, registry, projectKey, base, PaneLifecycleStarting).Label; got != "[BlueLake] (starting)" {
		t.Errorf("starting label = %q", got)
	}
	dead := base
	dead.Dead = true
	if got := reconcile(t, registry, projectKey, dead, "").Label; got != "[BlueLake] (exited)" {
		t.Errorf("dead pane label = %q", got)
	}
	shell := base
	shell.Command = "zsh"
	shell.PID = 0 // no process tree: command alone decides
	rec := reconcile(t, registry, projectKey, shell, "")
	if rec.Lifecycle != PaneLifecycleExited {
		t.Errorf("agent back at a shell: lifecycle = %q, want exited", rec.Lifecycle)
	}
	unknown := base
	unknown.Command = ""
	if got := reconcile(t, registry, projectKey, unknown, "").Label; got != "[BlueLake] (unknown)" {
		t.Errorf("unobservable command label = %q (must not claim exited)", got)
	}
	stale := badgeRegistry(session, projectKey, "%1", "BlueLake", 99)
	if got := reconcile(t, stale, projectKey, dead, "").Label; got != "[?!] (exited)" {
		t.Errorf("stale + exited label = %q", got)
	}
}

// TestReconcilePane_TimestampsDistinguishFailedAttempts: last_attempt_at
// advances on every pass; last_success_at only when the observation
// completed.
func TestReconcilePane_TimestampsDistinguishFailedAttempts(t *testing.T) {
	isolateBadgeDirs(t)
	projectKey := t.TempDir()
	const session = "badge_ts"
	registry := badgeRegistry(session, projectKey, "%2", "BlueLake", 20)
	pane := tmux.Pane{ID: "%2", Index: 1, PID: 20, Command: "claude", Type: tmux.AgentClaude}
	t1 := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Minute)

	if _, err := WriteIdentity(projectKey, "%2", "BlueLake"); err != nil {
		t.Fatal(err)
	}
	first := ReconcilePane(registry, ReconcileInput{SessionName: session, ProjectKeys: []string{projectKey}, Pane: pane, Now: t1}, PaneBadgeRecord{})
	if !first.LastAttemptAt.Equal(t1) || first.LastSuccessAt == nil || !first.LastSuccessAt.Equal(t1) {
		t.Fatalf("first pass timestamps = %v / %v", first.LastAttemptAt, first.LastSuccessAt)
	}

	if err := os.Remove(CanonicalIdentityPath(projectKey, "%2")); err != nil {
		t.Fatal(err)
	}
	second := ReconcilePane(registry, ReconcileInput{SessionName: session, ProjectKeys: []string{projectKey}, Pane: pane, Now: t2}, first)
	if second.ObservationState != PaneObservationMissingFile {
		t.Fatalf("second observation = %q", second.ObservationState)
	}
	if !second.LastAttemptAt.Equal(t2) {
		t.Errorf("failed attempt must advance last_attempt_at: %v", second.LastAttemptAt)
	}
	if second.LastSuccessAt == nil || !second.LastSuccessAt.Equal(t1) {
		t.Errorf("failed observation must retain last_success_at=%v, got %v", t1, second.LastSuccessAt)
	}

	unobservable := UnobservableRecord(registry, "%2", "BlueLake", t3, second)
	if unobservable.AssignmentState != PaneAssignmentUnobservable || !unobservable.LastAttemptAt.Equal(t3) {
		t.Fatalf("unobservable = %+v", unobservable)
	}
	if unobservable.LastSuccessAt == nil || !unobservable.LastSuccessAt.Equal(t1) {
		t.Errorf("tmux outage must retain last_success_at, got %v", unobservable.LastSuccessAt)
	}
	if unobservable.Label != "[?!] (unknown)" {
		t.Errorf("unobservable label = %q", unobservable.Label)
	}
}

func TestObservePaneIdentity_DistinguishesFailures(t *testing.T) {
	isolateBadgeDirs(t)
	projectKey := t.TempDir()

	if obs := ObservePaneIdentity([]string{projectKey}, "%9"); obs.Failure != PaneObservationMissingFile {
		t.Errorf("missing: %+v", obs)
	}

	path := CanonicalIdentityPath(projectKey, "%9")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if obs := ObservePaneIdentity([]string{projectKey}, "%9"); obs.Failure != PaneObservationInvalidFile {
		t.Errorf("empty: %+v", obs)
	}
	if err := os.WriteFile(path, []byte(`{"pane_id":"%9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if obs := ObservePaneIdentity([]string{projectKey}, "%9"); obs.Failure != PaneObservationInvalidFile {
		t.Errorf("nameless record: %+v", obs)
	}
	if err := os.WriteFile(path, []byte("BlueLake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if obs := ObservePaneIdentity([]string{projectKey}, "%9"); obs.Failure != "" || obs.Structured || obs.Name != "BlueLake" {
		t.Errorf("legacy: %+v", obs)
	}
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		if obs := ObservePaneIdentity([]string{projectKey}, "%9"); obs.Failure != PaneObservationUnreadableFile {
			t.Errorf("unreadable: %+v", obs)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A symlink at the canonical path is never trusted.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("Impostor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if obs := ObservePaneIdentity([]string{projectKey}, "%9"); obs.Failure != PaneObservationInvalidFile {
		t.Errorf("symlink: %+v", obs)
	}
}

// TestObservePaneIdentity_AliasPrecedence: the session key wins when it has a
// file; a published alias is consulted only when the session key has none.
func TestObservePaneIdentity_AliasPrecedence(t *testing.T) {
	isolateBadgeDirs(t)
	sessionKey := t.TempDir()
	aliasKey := t.TempDir()
	if _, err := WriteIdentity(aliasKey, "%4", "AliasName"); err != nil {
		t.Fatal(err)
	}
	if obs := ObservePaneIdentity([]string{sessionKey, aliasKey}, "%4"); obs.Name != "AliasName" {
		t.Fatalf("alias fallback: %+v", obs)
	}
	if _, err := WriteIdentity(sessionKey, "%4", "SessionName"); err != nil {
		t.Fatal(err)
	}
	if obs := ObservePaneIdentity([]string{sessionKey, aliasKey}, "%4"); obs.Name != "SessionName" {
		t.Fatalf("session key precedence: %+v", obs)
	}
}

func TestCompare_BindingFacts(t *testing.T) {
	facts := PaneBindingFacts{SessionName: "s", PaneID: "%5", PanePID: 50, SocketPath: "/tmp/tmux-1/default"}
	structured := func(rec PaneIdentityRecord) IdentityObservation {
		return IdentityObservation{Name: rec.Name, Path: "p", Structured: true, Record: rec}
	}
	full := PaneIdentityRecord{Name: "BlueLake", SessionName: "s", PaneID: "%5", PanePID: 50, SocketPath: "/tmp/tmux-1/default"}

	cases := []struct {
		name  string
		obs   IdentityObservation
		facts PaneBindingFacts
		want  PaneObservationState
	}{
		{"matched", structured(full), facts, PaneObservationMatched},
		{"other pane", structured(func() PaneIdentityRecord { r := full; r.PaneID = "%6"; return r }()), facts, PaneObservationBindingStale},
		{"other session", structured(func() PaneIdentityRecord { r := full; r.SessionName = "other"; return r }()), facts, PaneObservationBindingStale},
		{"other socket", structured(func() PaneIdentityRecord { r := full; r.SocketPath = "/tmp/tmux-1/other"; return r }()), facts, PaneObservationBindingStale},
		{"no socket recorded", structured(func() PaneIdentityRecord { r := full; r.SocketPath = ""; return r }()), facts, PaneObservationMatched},
		{"tmux pid unknown", structured(full), PaneBindingFacts{SessionName: "s", PaneID: "%5"}, PaneObservationBindingUnverifiable},
		{"legacy", IdentityObservation{Name: "BlueLake", Path: "p"}, facts, PaneObservationLegacyUnverified},
		{"legacy other name", IdentityObservation{Name: "RedFox", Path: "p"}, facts, PaneObservationNameDisagreement},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, verified, _ := tc.obs.Compare("BlueLake", tc.facts)
			if state != tc.want {
				t.Fatalf("state = %q, want %q", state, tc.want)
			}
			if verified != (tc.want == PaneObservationMatched) {
				t.Fatalf("verified = %v for %q", verified, state)
			}
		})
	}
}

func TestBadgeLabel_TemplateAndSanitisation(t *testing.T) {
	current := PaneBadgeRecord{AssignedName: "BlueLake", AssignmentState: PaneAssignmentCurrent, ObservationState: PaneObservationMatched, Lifecycle: PaneLifecycleRunning}
	if got := BadgeLabel(current, "<{name}{drift}>{lifecycle}"); got != "<BlueLake>" {
		t.Errorf("custom template = %q", got)
	}
	if got := BadgeLabel(current, ""); got != "[BlueLake]" {
		t.Errorf("default template = %q", got)
	}
	unsafe := current
	unsafe.AssignedName = "Blue#{pane_id}Lake"
	if got := BadgeLabel(unsafe, ""); got != "[?!]" {
		t.Errorf("unsafe name must render unresolved, got %q", got)
	}
	injected := BadgeLabel(current, "#[fg=red]{name};kill-server")
	if strings.ContainsAny(injected, "#{};") {
		t.Errorf("format syntax survived sanitisation: %q", injected)
	}
	for _, tmpl := range []string{"", "[{name}{drift}]", "{name} {lifecycle}"} {
		if err := ValidateBadgeTemplate(tmpl); err != nil {
			t.Errorf("ValidateBadgeTemplate(%q) = %v", tmpl, err)
		}
	}
	for _, tmpl := range []string{"{drift}", "{name} {bogus}", "#[fg=red]{name}", "{name};{drift}", strings.Repeat("x", 100) + "{name}"} {
		if err := ValidateBadgeTemplate(tmpl); err == nil {
			t.Errorf("ValidateBadgeTemplate(%q) accepted", tmpl)
		}
	}
}

func TestPaneBadgeStore_RoundTrip(t *testing.T) {
	isolateBadgeDirs(t)
	projectKey := t.TempDir()
	const session = "badge_store"
	store, err := LoadPaneBadgeStore(session, projectKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Panes) != 0 {
		t.Fatalf("fresh store not empty: %+v", store.Panes)
	}
	success := time.Now().UTC().Truncate(time.Second)
	store.Panes["%1"] = PaneBadgeRecord{PaneID: "%1", AssignedName: "BlueLake", AssignmentState: PaneAssignmentCurrent, ObservationState: PaneObservationMatched, LastAttemptAt: success, LastSuccessAt: &success, Published: true}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadPaneBadgeStore(session, projectKey)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := reloaded.Panes["%1"]
	if !ok || rec.AssignedName != "BlueLake" || !rec.Published || rec.LastSuccessAt == nil || !rec.LastSuccessAt.Equal(success) {
		t.Fatalf("reloaded = %+v", rec)
	}
	// The store lives beside the registry, never inside it.
	if _, err := os.Stat(filepath.Join(filepath.Dir(registryPath(session, projectKey)), "agent_badges.json")); err != nil {
		t.Fatalf("store path: %v", err)
	}
	if _, err := LoadPaneBadgeStore("../escape", projectKey); err == nil {
		t.Fatal("invalid session name accepted")
	}
}
