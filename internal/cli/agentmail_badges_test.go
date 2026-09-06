package cli

// Regression tests for ntm#312 (Agent Mail pane identity badges): the
// orchestration between registry, identity files and the tmux pane/window
// options, driven through a fake tmux surface.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// fakeBadgeTmux records every badge write and serves a scripted topology.
type fakeBadgeTmux struct {
	panes    []tmux.Pane
	panesErr error
	windows  []tmux.WindowBadgeInfo
	winErr   error
	// publishPID overrides the pid reported after publishing (0 = the pane's
	// listed pid); publishErr fails every publish.
	publishPID map[string]int
	publishErr error

	published []publishedBadge
	cleared   []string
	enabled   []string
	disabled  []string
	// owned windows for DisableWindowBorder to report a change on.
	owned map[string]bool
}

type publishedBadge struct {
	paneID string
	badge  tmux.PaneBadge
}

func (f *fakeBadgeTmux) ListPanes(context.Context, string) ([]tmux.Pane, error) {
	return f.panes, f.panesErr
}

func (f *fakeBadgeTmux) ListWindows(context.Context, string) ([]tmux.WindowBadgeInfo, error) {
	return f.windows, f.winErr
}

func (f *fakeBadgeTmux) Publish(_ context.Context, paneID string, badge tmux.PaneBadge) (int, error) {
	if f.publishErr != nil {
		return 0, f.publishErr
	}
	f.published = append(f.published, publishedBadge{paneID: paneID, badge: badge})
	if pid, ok := f.publishPID[paneID]; ok {
		return pid, nil
	}
	for _, p := range f.panes {
		if p.ID == paneID {
			return p.PID, nil
		}
	}
	return 0, fmt.Errorf("no such pane: %s", paneID)
}

func (f *fakeBadgeTmux) Clear(_ context.Context, paneID string) error {
	f.cleared = append(f.cleared, paneID)
	return nil
}

func (f *fakeBadgeTmux) EnableWindowBorder(_ context.Context, windowID string) (tmux.BorderChange, error) {
	f.enabled = append(f.enabled, windowID)
	if f.owned == nil {
		f.owned = map[string]bool{}
	}
	f.owned[windowID] = true
	return tmux.BorderChange{Changed: true}, nil
}

func (f *fakeBadgeTmux) DisableWindowBorder(_ context.Context, windowID string) (tmux.BorderChange, error) {
	f.disabled = append(f.disabled, windowID)
	if f.owned[windowID] {
		delete(f.owned, windowID)
		return tmux.BorderChange{Changed: true}, nil
	}
	return tmux.BorderChange{Skipped: "not owned"}, nil
}

func (f *fakeBadgeTmux) badgeFor(paneID string) (tmux.PaneBadge, bool) {
	for i := len(f.published) - 1; i >= 0; i-- {
		if f.published[i].paneID == paneID {
			return f.published[i].badge, true
		}
	}
	return tmux.PaneBadge{}, false
}

func installFakeBadgeTmux(t *testing.T, fake *fakeBadgeTmux) {
	t.Helper()
	old := newPaneBadgeTmux
	newPaneBadgeTmux = func() paneBadgeTmux { return fake }
	t.Cleanup(func() { newPaneBadgeTmux = old })
}

func setBadgeConfig(t *testing.T, enabled bool) {
	t.Helper()
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Default()
	cfg.AgentMail.Enabled = true
	cfg.AgentMail.AutoRegister = true
	cfg.AgentMail.PaneBadges = &enabled
}

func singleWindow() []tmux.WindowBadgeInfo {
	return []tmux.WindowBadgeInfo{{ID: "@1", Index: 0, SocketPath: "/tmp/tmux-1000/default"}}
}

// badgeFixture seeds a registry with BlueLake on %1 (legacy file agrees) and
// RedFox on %2 (file says GreenTree), plus a user pane and a service pane.
func badgeFixture(t *testing.T, session, projectKey string) *agentmail.SessionAgentRegistry {
	t.Helper()
	registry := agentmail.NewSessionAgentRegistry(session, projectKey)
	registry.AddAgent(session+"__cc_1", "%1", "BlueLake")
	registry.SetPanePID("%1", 101)
	registry.AddAgent(session+"__cod_1", "%2", "RedFox")
	registry.SetPanePID("%2", 102)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := agentmail.WriteIdentity(projectKey, "%1", "BlueLake"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentmail.WriteIdentity(projectKey, "%2", "GreenTree"); err != nil {
		t.Fatal(err)
	}
	return registry
}

func fixturePanes(session string) []tmux.Pane {
	return []tmux.Pane{
		{ID: "%0", Index: 0, PID: 100, Command: "zsh", Title: session + "__user", Type: tmux.AgentUser},
		{ID: "%1", Index: 1, PID: 101, Command: "claude", Title: session + "__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, PID: 102, Command: "codex", Title: session + "__cod_1", Type: tmux.AgentCodex},
		{ID: "%3", Index: 3, PID: 103, Command: "cass", Title: "cass", Service: "cass", ServiceManager: "acfs"},
	}
}

func TestReconcileSessionIdentityBadges_PublishesAndReportsDrift(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_publish"
	badgeFixture(t, session, projectKey)
	fake := &fakeBadgeTmux{panes: fixturePanes(session), windows: singleWindow()}
	installFakeBadgeTmux(t, fake)

	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Published != 2 || report.WindowsPrepared != 1 || len(fake.enabled) != 1 {
		t.Fatalf("report = published %d, windows %d (enabled %v)", report.Published, report.WindowsPrepared, fake.enabled)
	}
	blue, ok := fake.badgeFor("%1")
	if !ok || blue.Label != "[BlueLake]" || blue.State != "legacy-unverified" || blue.Lifecycle != "running" || blue.Name != "BlueLake" {
		t.Fatalf("%%1 badge = %+v", blue)
	}
	red, ok := fake.badgeFor("%2")
	if !ok || red.Label != "[RedFox!]" || red.State != "name-disagreement" || red.Name != "RedFox" {
		t.Fatalf("%%2 badge = %+v (assigned name must be retained, never relabelled)", red)
	}
	for _, p := range fake.published {
		if p.paneID == "%0" || p.paneID == "%3" {
			t.Fatalf("user/service pane %s received a badge", p.paneID)
		}
	}
	if len(report.Discrepancies) != 1 || report.Discrepancies[0].PaneID != "%2" || report.Discrepancies[0].ResolvedName != "GreenTree" {
		t.Fatalf("discrepancies = %+v", report.Discrepancies)
	}

	// The registry is untouched and the store recorded both panes.
	registry, err := agentmail.LoadSessionAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		t.Fatal(err)
	}
	if name, _ := registry.GetAgentByID("%2"); name != "RedFox" {
		t.Fatalf("registry relabelled %%2 to %q", name)
	}
	store, err := agentmail.LoadPaneBadgeStore(session, projectKey)
	if err != nil {
		t.Fatal(err)
	}
	if rec := store.Panes["%2"]; !rec.Published || rec.ObservationState != agentmail.PaneObservationNameDisagreement || rec.LastSuccessAt == nil {
		t.Fatalf("store %%2 = %+v", rec)
	}
}

func TestReconcileSessionIdentityBadges_UnregisteredAgentPaneShowsUnknown(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_unregistered"
	badgeFixture(t, session, projectKey)
	panes := append(fixturePanes(session), tmux.Pane{ID: "%9", Index: 4, PID: 109, Command: "gemini", Title: session + "__gmi_1", Type: tmux.AgentGemini})
	fake := &fakeBadgeTmux{panes: panes, windows: singleWindow()}
	installFakeBadgeTmux(t, fake)

	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	badge, ok := fake.badgeFor("%9")
	if !ok || badge.Label != "[?!]" || badge.Name != "" || badge.State != "assignment-unregistered" {
		t.Fatalf("unregistered pane badge = %+v", badge)
	}
	found := false
	for _, rec := range report.Discrepancies {
		if rec.PaneID == "%9" && rec.AssignmentState == agentmail.PaneAssignmentUnregistered {
			found = true
		}
	}
	if !found {
		t.Fatalf("unregistered pane missing from discrepancies: %+v", report.Discrepancies)
	}
}

func TestReconcileSessionIdentityBadges_DisabledWithdrawsAndRestores(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_disable"
	badgeFixture(t, session, projectKey)
	fake := &fakeBadgeTmux{panes: fixturePanes(session), windows: singleWindow()}
	installFakeBadgeTmux(t, fake)
	if _, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session}); err != nil {
		t.Fatal(err)
	}

	setBadgeConfig(t, false)
	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if report.Enabled || report.Published != 0 {
		t.Fatalf("disabled pass published: %+v", report)
	}
	if report.Cleared != 2 || len(fake.cleared) != 2 {
		t.Fatalf("cleared = %d (%v), want both agent panes", report.Cleared, fake.cleared)
	}
	if report.WindowsRestored != 1 || len(fake.disabled) != 1 {
		t.Fatalf("windows restored = %d (%v)", report.WindowsRestored, fake.disabled)
	}
	// Reconciliation still ran: the disagreement is reported even with
	// badges off.
	if len(report.Discrepancies) != 1 || report.Discrepancies[0].PaneID != "%2" {
		t.Fatalf("discrepancies = %+v", report.Discrepancies)
	}
	store, _ := agentmail.LoadPaneBadgeStore(session, projectKey)
	if rec := store.Panes["%1"]; rec.Published || rec.Cached {
		t.Fatalf("store still claims %%1 published/cached after withdrawal: %+v", rec)
	}
	// A second disabled pass has nothing left to withdraw.
	fake.cleared, fake.disabled = nil, nil
	report, _ = reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if report.Cleared != 0 || report.WindowsRestored != 0 {
		t.Fatalf("second disabled pass = cleared %d, restored %d", report.Cleared, report.WindowsRestored)
	}
}

func TestReconcileSessionIdentityBadges_TopologyAndOptOut(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_topology"
	registry := agentmail.NewSessionAgentRegistry(session, projectKey)
	// Two windows with duplicate pane indices: identity is keyed by pane id.
	registry.AddAgent(session+"__cc_1", "%1", "BlueLake")
	registry.SetPanePID("%1", 101)
	registry.AddAgent(session+"__cc_2", "%5", "GoldHawk")
	registry.SetPanePID("%5", 105)
	registry.AddAgent(session+"__cc_3", "%8", "IronWolf")
	registry.SetPanePID("%8", 108)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatal(err)
	}
	for pane, name := range map[string]string{"%1": "BlueLake", "%5": "GoldHawk", "%8": "IronWolf"} {
		if _, err := agentmail.WriteIdentity(projectKey, pane, name); err != nil {
			t.Fatal(err)
		}
	}
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, WindowIndex: 0, PID: 101, Command: "claude", Type: tmux.AgentClaude},
		{ID: "%5", Index: 1, WindowIndex: 1, PID: 105, Command: "claude", Type: tmux.AgentClaude},
		{ID: "%8", Index: 1, WindowIndex: 2, PID: 108, Command: "claude", Type: tmux.AgentClaude},
	}
	windows := []tmux.WindowBadgeInfo{
		{ID: "@1", Index: 0},
		{ID: "@2", Index: 1},
		{ID: "@3", Index: 2, Linked: true},
	}
	fake := &fakeBadgeTmux{panes: panes, windows: windows}
	installFakeBadgeTmux(t, fake)

	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if report.Published != 2 || len(fake.enabled) != 2 {
		t.Fatalf("published %d, windows enabled %v", report.Published, fake.enabled)
	}
	if b, ok := fake.badgeFor("%1"); !ok || b.Label != "[BlueLake]" {
		t.Fatalf("window 0 pane 1 = %+v", b)
	}
	if b, ok := fake.badgeFor("%5"); !ok || b.Label != "[GoldHawk]" {
		t.Fatalf("window 1 pane 1 = %+v", b)
	}
	if _, ok := fake.badgeFor("%8"); ok {
		t.Fatal("pane in a linked window must not be published")
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "@3") || !strings.Contains(strings.Join(report.Warnings, "\n"), "linked") {
		t.Fatalf("linked window needs an explicit diagnostic, got %v", report.Warnings)
	}
	for _, id := range fake.enabled {
		if id == "@3" {
			t.Fatal("linked window border was modified")
		}
	}
	var linkedRec *agentmail.PaneBadgeRecord
	for i := range report.Records {
		if report.Records[i].PaneID == "%8" {
			linkedRec = &report.Records[i]
		}
	}
	if linkedRec == nil || linkedRec.Published || linkedRec.PublishError == "" || linkedRec.ObservationState != agentmail.PaneObservationLegacyUnverified {
		t.Fatalf("linked pane record = %+v (reconciled but not published)", linkedRec)
	}

	// Session opt-out: nothing is written, nothing restored, still reported.
	for i := range windows {
		windows[i].SessionOptOut = true
	}
	fake = &fakeBadgeTmux{panes: panes, windows: windows}
	installFakeBadgeTmux(t, fake)
	report, err = reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.published) != 0 || len(fake.enabled) != 0 || len(fake.disabled) != 0 || len(fake.cleared) != 0 {
		t.Fatalf("opted-out session was touched: %+v", fake)
	}
	if len(report.Records) != 3 || !strings.Contains(strings.Join(report.Warnings, "\n"), "opted out") {
		t.Fatalf("opt-out report = %+v", report)
	}
}

func TestReconcileSessionIdentityBadges_TmuxUnavailableRetainsSuccess(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_outage"
	badgeFixture(t, session, projectKey)
	fake := &fakeBadgeTmux{panes: fixturePanes(session), windows: singleWindow()}
	installFakeBadgeTmux(t, fake)
	if _, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session}); err != nil {
		t.Fatal(err)
	}
	before, _ := agentmail.LoadPaneBadgeStore(session, projectKey)
	firstSuccess := before.Panes["%1"].LastSuccessAt
	if firstSuccess == nil {
		t.Fatal("first pass recorded no success")
	}

	fake.panesErr = errors.New("no server running")
	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err == nil || report.TmuxError == "" {
		t.Fatalf("tmux outage must be reported: err=%v report=%+v", err, report)
	}
	if len(report.Records) != 2 || report.Published != 0 {
		t.Fatalf("outage records = %+v", report.Records)
	}
	after, _ := agentmail.LoadPaneBadgeStore(session, projectKey)
	rec := after.Panes["%1"]
	if rec.AssignmentState != agentmail.PaneAssignmentUnobservable {
		t.Fatalf("outage record = %+v", rec)
	}
	if !rec.LastAttemptAt.After(before.Panes["%1"].LastAttemptAt) {
		t.Fatal("last_attempt_at did not advance on a failed attempt")
	}
	if rec.LastSuccessAt == nil || !rec.LastSuccessAt.Equal(*firstSuccess) {
		t.Fatalf("last_success_at changed on a failed attempt: %v -> %v", firstSuccess, rec.LastSuccessAt)
	}
	if rec.Label != "[?!] (unknown)" || rec.Published {
		t.Fatalf("outage record must not claim a published current badge: %+v", rec)
	}
}

func TestReconcileSessionIdentityBadges_GenerationRaceWithdrawsBadge(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_race"
	badgeFixture(t, session, projectKey)
	// %1 was respawned between listing and publication: tmux reports a new
	// pid for the same %N.
	fake := &fakeBadgeTmux{panes: fixturePanes(session), windows: singleWindow(), publishPID: map[string]int{"%1": 9999}}
	installFakeBadgeTmux(t, fake)

	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if report.Published != 1 {
		t.Fatalf("published = %d, want only %%2", report.Published)
	}
	if len(fake.cleared) != 1 || fake.cleared[0] != "%1" {
		t.Fatalf("raced pane badge not withdrawn: cleared %v", fake.cleared)
	}
	var raced agentmail.PaneBadgeRecord
	for _, rec := range report.Records {
		if rec.PaneID == "%1" {
			raced = rec
		}
	}
	if raced.Published || !strings.Contains(raced.PublishError, "generation changed") {
		t.Fatalf("raced record = %+v", raced)
	}
	// Publication failure is separate from reconciliation success.
	if raced.LastSuccessAt == nil {
		t.Fatal("publication failure must not erase a completed observation")
	}
}

func TestReconcileSessionIdentityBadges_PublishFailureWarnsOnly(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_pubfail"
	badgeFixture(t, session, projectKey)
	fake := &fakeBadgeTmux{panes: fixturePanes(session), windows: singleWindow(), publishErr: errors.New("set-option: permission denied")}
	installFakeBadgeTmux(t, fake)

	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatalf("publication failures must not fail the pass: %v", err)
	}
	if report.Published != 0 || len(report.Warnings) < 2 {
		t.Fatalf("report = %+v", report)
	}
	registry, _ := agentmail.LoadSessionAgentRegistry(session, projectKey)
	if name, _ := registry.GetAgentByID("%1"); name != "BlueLake" {
		t.Fatal("publication failure altered the assignment")
	}
}

func TestReconcileSessionIdentityBadges_MissingPaneIsADiscrepancy(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_gone"
	badgeFixture(t, session, projectKey)
	panes := fixturePanes(session)[:2] // %2 (RedFox) is gone
	fake := &fakeBadgeTmux{panes: panes, windows: singleWindow()}
	installFakeBadgeTmux(t, fake)

	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	var gone *agentmail.PaneBadgeRecord
	for i := range report.Discrepancies {
		if report.Discrepancies[i].PaneID == "%2" {
			gone = &report.Discrepancies[i]
		}
	}
	if gone == nil || gone.AssignmentState != agentmail.PaneAssignmentStale || !strings.Contains(strings.Join(gone.Problems, ""), "pane-missing") {
		t.Fatalf("missing pane discrepancy = %+v", gone)
	}
	if _, ok := fake.badgeFor("%2"); ok {
		t.Fatal("published to a pane that does not exist")
	}
}

func TestReconcileSessionIdentityBadges_StalePaneNotShownAsCurrent(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	projectKey := t.TempDir()
	const session = "badge_stale"
	badgeFixture(t, session, projectKey)
	panes := fixturePanes(session)
	panes[1].PID = 555 // %1 respawned since registration; dead pane retained
	panes[1].Dead = true
	fake := &fakeBadgeTmux{panes: panes, windows: singleWindow()}
	installFakeBadgeTmux(t, fake)

	if _, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session}); err != nil {
		t.Fatal(err)
	}
	badge, ok := fake.badgeFor("%1")
	if !ok || badge.Label != "[?!] (exited)" || badge.State != "assignment-stale" || badge.Lifecycle != "exited" {
		t.Fatalf("stale dead pane badge = %+v", badge)
	}
}

// TestSpawnIdentityCoordinator_PublishesStartingBadgeBeforeLaunch: on the
// spawn path the badge is written as part of prepareAgent (which runs before
// send-keys) with lifecycle=starting; the batch path publishes nothing per
// pane and reconciles once at the end with the observed lifecycle.
func TestSpawnIdentityCoordinator_PublishesStartingBadgeBeforeLaunch(t *testing.T) {
	isolateIdentityDirs(t)
	srv, _ := fakeSpawnMailServer(t, "BraveFalcon")
	enableFakeAgentMail(t, srv.URL)
	enabled := true
	cfg.AgentMail.PaneBadges = &enabled

	projectKey := t.TempDir()
	const session = "badge_prelaunch"
	panes := []tmux.Pane{
		{ID: "%0", Index: 0, PID: 100, Command: "zsh", Type: tmux.AgentUser},
		{ID: "%7", Index: 1, PID: 107, Command: "zsh", Title: session + "__cc_1", Type: tmux.AgentClaude},
	}
	stubPaneProbe(t, panes, nil)
	fake := &fakeBadgeTmux{panes: panes, windows: singleWindow()}
	installFakeBadgeTmux(t, fake)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.preLaunch = true
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%7", paneTitle: session + "__cc_1", agentType: "cc", model: "opus",
	})
	badge, ok := fake.badgeFor("%7")
	if !ok {
		t.Fatal("prepareAgent published no badge before launch")
	}
	if badge.Name != "BraveFalcon" || badge.Lifecycle != "starting" || badge.Label != "[BraveFalcon] (starting)" {
		t.Fatalf("pre-launch badge = %+v", badge)
	}
	if len(fake.enabled) != 1 {
		t.Fatalf("window border not prepared before launch: %v", fake.enabled)
	}

	// After launch the pane runs the agent: the full pass drops "starting".
	fake.panes[1].Command = "claude"
	fake.published = nil
	if report := coordinator.reconcileBadges(context.Background()); report == nil || report.Published != 1 {
		t.Fatalf("post-launch reconcile = %+v", report)
	}
	badge, _ = fake.badgeFor("%7")
	if badge.Lifecycle != "running" || badge.Label != "[BraveFalcon]" {
		t.Fatalf("post-launch badge = %+v", badge)
	}

	// Batch path: no per-pane publication inside prepareAgent.
	batch := newSpawnIdentityCoordinator(projectKey, session)
	fake.published = nil
	batch.prepareAgent(context.Background(), spawnedAgentInfo{
		paneIndex: 1, paneID: "%7", paneTitle: session + "__cc_1", agentType: "cc", model: "opus",
	})
	if len(fake.published) != 0 {
		t.Fatalf("batch prepareAgent published %+v; the batch path reconciles once at the end", fake.published)
	}
}

// TestSpawnIdentityCoordinator_BadgesOffPublishesNothing: the default
// configuration leaves tmux untouched.
func TestSpawnIdentityCoordinator_BadgesOffPublishesNothing(t *testing.T) {
	isolateIdentityDirs(t)
	srv, _ := fakeSpawnMailServer(t, "BraveFalcon")
	enableFakeAgentMail(t, srv.URL)
	if cfg.AgentMail.PaneBadgesOrDefault() {
		t.Fatal("pane badges must default to off")
	}
	projectKey := t.TempDir()
	const session = "badge_off"
	panes := []tmux.Pane{{ID: "%7", Index: 1, PID: 107, Command: "zsh", Title: session + "__cc_1", Type: tmux.AgentClaude}}
	stubPaneProbe(t, panes, nil)
	fake := &fakeBadgeTmux{panes: panes, windows: singleWindow()}
	installFakeBadgeTmux(t, fake)

	coordinator := newSpawnIdentityCoordinator(projectKey, session)
	coordinator.preLaunch = true
	coordinator.prepareAgent(context.Background(), spawnedAgentInfo{paneIndex: 1, paneID: "%7", paneTitle: session + "__cc_1", agentType: "cc", model: "opus"})
	if report := coordinator.reconcileBadges(context.Background()); report != nil {
		t.Fatalf("reconcileBadges ran with badges off: %+v", report)
	}
	if len(fake.published) != 0 || len(fake.enabled) != 0 || len(fake.cleared) != 0 || len(fake.disabled) != 0 {
		t.Fatalf("tmux touched with badges off: %+v", fake)
	}
	if status := coordinator.finalStatus(); status == nil || status.AgentsRegistered != 1 {
		t.Fatalf("registration unaffected? %+v", status)
	}
}

func TestPaneBadgeConfig(t *testing.T) {
	c := config.Default()
	if c.AgentMail.PaneBadgesOrDefault() {
		t.Fatal("default must be off")
	}
	on := true
	c.AgentMail.PaneBadges = &on
	if !c.AgentMail.PaneBadgesOrDefault() {
		t.Fatal("explicit true ignored")
	}
	c.AgentMail.Enabled = false
	if c.AgentMail.PaneBadgesOrDefault() {
		t.Fatal("badges require agent_mail.enabled")
	}
	c.AgentMail.Enabled = true
	if got := c.AgentMail.PaneBadgeFormatOrDefault(); got != agentmail.DefaultBadgeTemplate {
		t.Fatalf("default template = %q", got)
	}
	c.AgentMail.PaneBadgeFormat = "<{name}{drift}>"
	if errs := config.Validate(c); len(errs) != 0 {
		t.Fatalf("valid template rejected: %v", errs)
	}
	c.AgentMail.PaneBadgeFormat = "#[fg=red]{name}"
	errs := config.Validate(c)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "agent_mail: pane_badge_format") {
			found = true
		}
	}
	if !found {
		t.Fatalf("format syntax in template accepted: %v", errs)
	}
}

// TestReconcileSessionIdentityBadges_NoRegistryPublishesNothing: without a
// registry there is no display authority, so an enabled session gets no
// badges — and anything an earlier pass cached is withdrawn.
func TestReconcileSessionIdentityBadges_NoRegistryPublishesNothing(t *testing.T) {
	isolateIdentityDirs(t)
	setBadgeConfig(t, true)
	const session = "badge_noregistry"
	panes := []tmux.Pane{{ID: "%1", Index: 1, PID: 101, Command: "claude", Title: session + "__cc_1", Type: tmux.AgentClaude}}
	fake := &fakeBadgeTmux{panes: panes, windows: singleWindow()}
	installFakeBadgeTmux(t, fake)

	report, err := reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if report.Published != 0 || len(fake.published) != 0 || len(fake.enabled) != 0 {
		t.Fatalf("published without a registry: %+v", fake)
	}
	if len(report.Discrepancies) != 1 || report.Discrepancies[0].AssignmentState != agentmail.PaneAssignmentUnregistered {
		t.Fatalf("discrepancies = %+v", report.Discrepancies)
	}

	// A badge cached earlier (registry since removed) is withdrawn.
	store, err := agentmail.LoadPaneBadgeStore(session, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := store.Panes["%1"]
	rec.Cached = true
	store.Panes["%1"] = rec
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	fake.owned = map[string]bool{"@1": true}
	report, _ = reconcileSessionIdentityBadges(context.Background(), badgeReconcileOptions{Session: session})
	if report.Cleared != 1 || len(fake.cleared) != 1 || report.WindowsRestored != 1 {
		t.Fatalf("stale badge not withdrawn: %+v (fake %+v)", report, fake)
	}
}
