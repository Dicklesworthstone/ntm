package coordinator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// --- fake captures -----------------------------------------------------------

// failoverIdleCapture is an idle pane with no rate-limit chatter.
const failoverIdleCapture = "● Done. All tests pass.\n\n❯ \n"

// limitedCapture trips the generic rate-limit detector with no parseable
// reset information (treated as beyond the horizon by documented rule).
const limitedCapture = "Error: rate limit exceeded. Please retry later.\n\n❯ \n"

// limitedSoonCapture carries a parseable wait (300s = 5 minutes), inside the
// default 30-minute horizon.
const limitedSoonCapture = "Error: rate limit exceeded. Try again in 300 s.\n\n❯ \n"

// limitedLongWaitCapture carries a parseable wait (7200s = 2 hours), beyond
// the default 30-minute horizon.
const limitedLongWaitCapture = "Error: rate limit exceeded. Try again in 7200 s.\n\n❯ \n"

// limitedClockCapture carries a clock-time reset hint ("resets at 3:00 am").
const limitedClockCapture = "You've hit your usage limit. Limit resets at 3:00 am (America/New_York).\n\n❯ \n"

// limitedWorkingCapture is a rate-limited pane whose agent is mid-turn.
const limitedWorkingCapture = "✻ Simmering… (esc to interrupt · 12s)\nError: rate limit exceeded. Please retry later.\n"

// --- test env ----------------------------------------------------------------

type failoverTestEnv struct {
	fc        *failoverChecker
	published []robot.ActuationRecord
	switched  []string // "provider:account" per switchAccount call
	queried   []string // provider per listAccounts call
	watermark map[string]time.Time
}

// newFailoverTestEnv wires a failoverChecker with fake collaborators. The
// production constructor is exercised separately; decision tests inject seams
// directly so no caam binary or tmux server is needed.
func newFailoverTestEnv(t *testing.T, providers []string, horizonMinutes int, panes []tmux.Pane, captures map[string]string) *failoverTestEnv {
	t.Helper()
	env := &failoverTestEnv{watermark: make(map[string]time.Time)}
	providerSet := make(map[string]bool)
	for _, p := range providers {
		if c := canonicalFailoverProvider(p); c != "" {
			providerSet[c] = true
		}
	}
	fc := &failoverChecker{
		session:   "fosess",
		horizon:   time.Duration(horizonMinutes) * time.Minute,
		providers: providerSet,
		getPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return panes, nil
		},
		capturePane: func(_ context.Context, paneID string, lines int) (string, error) {
			capture, ok := captures[paneID]
			if !ok {
				return "", errors.New("no capture for pane")
			}
			return capture, nil
		},
		caamAvailable:     func() bool { return true },
		guardSwitch:       func(context.Context, tmux.Pane, string) error { return nil },
		lockProvider:      func(context.Context, string) (func(), error) { return func() {}, nil },
		readProviderState: func(string) (accountProviderState, error) { return accountProviderState{}, nil },
		listAccounts: func(_ context.Context, provider string) ([]swarm.AccountInfo, error) {
			env.queried = append(env.queried, provider)
			return []swarm.AccountInfo{
				{Provider: provider, AccountName: "acct-active", IsActive: true},
				{Provider: provider, AccountName: "acct-alt"},
			}, nil
		},
		recoverPane: func(_ context.Context, _ tmux.Pane, provider string, request accountRecoveryRequest) (accountRecoveryOutcome, error) {
			if !env.fc.claimSwitch(request.Scope, provider, env.fc.now(), request.ObservedSwitch) {
				return accountRecoveryOutcome{}, &accountRecoveryDecline{reason: "cooldown"}
			}
			env.switched = append(env.switched, provider+":"+request.Account)
			return accountRecoveryOutcome{ActivationAttempted: true, AccountActivated: true,
				ConversationRecovered: true, PrevAccount: "acct-active", NativeSessionID: "native-session"}, nil
		},
		publish: func(record robot.ActuationRecord) {
			env.published = append(env.published, record)
		},
		now:           time.Now,
		memLastSwitch: make(map[string]time.Time),
		lastPublished: make(map[string]declineMark),
	}
	fc.lastSwitchAt = func(scope string) (time.Time, bool) {
		at, ok := env.watermark[scope]
		return at, ok
	}
	fc.claimSwitch = func(scope, provider string, at time.Time, _ *time.Time) bool {
		env.watermark[scope] = at
		return true
	}
	env.fc = fc
	return env
}

func foPane(id, title string) tmux.Pane {
	return tmux.Pane{ID: id, Title: title, Type: tmux.AgentClaude, Width: 120, PID: 1000, Index: 1}
}

// The monitor surface uses the real decision/recovery transaction with only
// external tmux, native-process and CAAM observations substituted. Robot's
// physical executor is covered separately through recording transports.
type accountRecoveryFixture struct {
	monitor                       *AccountFailoverMonitor
	panes                         map[string]tmux.Pane
	specs                         map[string]tmux.AgentLaunchSpec
	started                       map[string]int64
	captures                      map[string]string
	active                        string
	provider                      accountProviderState
	now                           time.Time
	activated, respawned, queried int
	queryHook                     func()
	restartHook                   func()
	activationHook                func()
	bindingError                  error
	activationError               error
	receiptError                  error
	wrongConversation             bool
}

func newAccountRecoveryFixture(t *testing.T, count int) *accountRecoveryFixture {
	t.Helper()
	dir := t.TempDir()
	f := &accountRecoveryFixture{panes: make(map[string]tmux.Pane), specs: make(map[string]tmux.AgentLaunchSpec),
		started: make(map[string]int64), captures: make(map[string]string), active: "acct-active", now: time.Now().UTC()}
	var panes []tmux.Pane
	for i := 1; i <= count; i++ {
		pane := foPane(fmt.Sprintf("%%%d", i), fmt.Sprintf("fosess__cc_%d", i))
		pane.PID += i
		pane.Index = i
		f.panes[pane.ID] = pane
		panes = append(panes, pane)
		f.started[pane.ID] = f.now.Add(-2 * time.Hour).UnixMilli()
		f.captures[pane.ID] = limitedCapture
		f.specs[pane.ID] = tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude,
			Command: "claude --model opus --dangerously-skip-permissions", Model: "opus"}
	}
	env := newFailoverTestEnv(t, []string{"claude"}, 30, panes, f.captures)
	fc := env.fc
	f.monitor = &AccountFailoverMonitor{checker: fc}
	fc.config = config.Default()
	fc.now = func() time.Time { return f.now }
	fc.getPanes = func(context.Context, string) ([]tmux.Pane, error) {
		var result []tmux.Pane
		for _, pane := range f.panes {
			result = append(result, pane)
		}
		return result, nil
	}
	fc.paneCwd = func(context.Context, string) (string, error) { return dir, nil }
	fc.readSpec = func(_ context.Context, id string) (*tmux.AgentLaunchSpec, error) {
		spec := f.specs[id]
		return &spec, nil
	}
	fc.readProviderState = func(string) (accountProviderState, error) { return f.provider, nil }
	fc.writeProviderWatermark = func(kind, provider, account string, at time.Time) error {
		if kind == watermarkTypeCaamProviderAttempt {
			f.provider.AttemptAt = at
		} else {
			if f.receiptError != nil {
				return f.receiptError
			}
			f.provider.ActivatedAt, f.provider.Account = at, account
		}
		return nil
	}
	fc.listAccounts = func(context.Context, string) ([]swarm.AccountInfo, error) {
		f.queried++
		if f.queryHook != nil {
			f.queryHook()
		}
		return []swarm.AccountInfo{{AccountName: "acct-active", IsActive: f.active == "acct-active"},
			{AccountName: "acct-alt", IsActive: f.active == "acct-alt"}}, nil
	}
	fc.activateAccount = func(ctx context.Context, _ string, _ string, preflight swarm.AccountActivationPreflight) (*swarm.RotationRecord, error) {
		if f.activationHook != nil {
			f.activationHook()
		}
		if err := preflight(ctx, &swarm.AccountInfo{AccountName: f.active, IsActive: true}); err != nil {
			return nil, err
		}
		f.activated++
		if f.activationError != nil {
			return nil, f.activationError
		}
		f.active = "acct-alt"
		return &swarm.RotationRecord{}, nil
	}
	fc.observeBinding = func(_ context.Context, _ string, _ string, pid int) (agentsession.GlobalCredentialBinding, error) {
		if f.bindingError != nil {
			return agentsession.GlobalCredentialBinding{}, f.bindingError
		}
		for id, pane := range f.panes {
			if pane.PID != pid {
				continue
			}
			nativeID := "11111111-1111-4111-8111-111111111111"
			if f.wrongConversation && pid > 2000 {
				nativeID = "22222222-2222-4222-8222-222222222222"
			}
			return agentsession.GlobalCredentialBinding{ProcessPID: pid + 10000, ProcessStartedAt: f.started[id], CredentialHome: "/home/test/.claude",
				Session: agentsession.BindingObservation{SessionID: nativeID, SourcePath: "/home/test/.claude/projects/session.jsonl"}}, nil
		}
		return agentsession.GlobalCredentialBinding{}, errors.New("process disappeared")
	}
	fc.restartPane = func(ctx context.Context, opts robot.RestartPaneOptions) (*robot.RestartPaneOutput, error) {
		if f.restartHook != nil {
			f.restartHook()
		}
		if err := opts.Recovery.BeforeRespawn(ctx); err != nil {
			return nil, err
		}
		pane := f.panes[opts.Panes[0]]
		beforePID := pane.PID
		pane.PID += 2000
		f.panes[pane.ID] = pane
		f.started[pane.ID] = f.now.Add(time.Millisecond).UnixMilli()
		f.respawned++
		spec := f.specs[pane.ID]
		var err error
		spec.Command, err = agentsession.ResumeLaunchCommand(agentsession.ResumeProvider(string(spec.AgentType)), opts.Recovery.NativeSessionID, spec.Command, agentsession.ResumeLaunchOptions{})
		if err != nil {
			return nil, err
		}
		f.specs[pane.ID] = spec
		key := strconv.Itoa(pane.Index)
		return &robot.RestartPaneOutput{RobotResponse: robot.NewRobotResponse(true), Restarted: []string{key},
			AgentRelaunched: map[string]bool{key: true}, PaneShellPIDs: map[string]robot.RestartPanePIDs{key: {Before: beforePID, After: pane.PID}}}, nil
	}
	fc.recoverPane = fc.recoverLimitedPane
	return f
}

func TestAccountFailoverMonitorRecoversSiblingsWithoutRepeatedGlobalActivation(t *testing.T) {
	f := newAccountRecoveryFixture(t, 2)
	decisions := f.monitor.RunOnce(t.Context())
	if len(decisions) != 2 || f.activated != 1 || f.respawned != 2 {
		t.Fatalf("decisions=%+v activated=%d respawned=%d", decisions, f.activated, f.respawned)
	}
	if !decisions[0].AccountActivated || !decisions[0].ConversationRecovered || decisions[1].AccountActivated ||
		decisions[1].AccountActivationAttempted || !decisions[1].ConversationRecovered {
		t.Fatalf("incorrect recovery receipts: %+v", decisions)
	}
	for _, spec := range f.specs {
		if !strings.Contains(spec.Command, "--model opus") || strings.Count(spec.Command, "--resume") != 1 {
			t.Fatalf("launch settings not preserved: %s", spec.Command)
		}
	}
}

func TestAccountFailoverMonitorDeclinesStaleAndUnprovenRecovery(t *testing.T) {
	for _, scenario := range []string{"pane_replaced_during_query", "working_after_query", "binding_changes_during_query", "binding_changes_before_action", "account_changes_before_action", "binding_changes_at_activation_boundary", "account_changes_at_activation_boundary", "profile", "omitted_environment", "receipt_storage_failure", "activation_failure", "provider_storage_failure", "new_process_after_receipt", "active_receipt_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccountRecoveryFixture(t, 1)
			switch scenario {
			case "pane_replaced_during_query":
				f.queryHook = func() {
					if f.queried == 2 {
						pane := f.panes["%1"]
						pane.PID++
						f.panes["%1"] = pane
					}
				}
			case "working_after_query":
				f.queryHook = func() {
					if f.queried == 2 {
						f.captures["%1"] = limitedWorkingCapture
					}
				}
			case "binding_changes_before_action":
				f.restartHook = func() { f.started["%1"]++ }
			case "binding_changes_during_query":
				f.queryHook = func() {
					if f.queried == 2 {
						f.started["%1"]++
					}
				}
			case "account_changes_before_action":
				f.restartHook = func() { f.active = "acct-alt" }
			case "binding_changes_at_activation_boundary":
				f.activationHook = func() { f.started["%1"]++ }
			case "account_changes_at_activation_boundary":
				f.activationHook = func() { f.active = "acct-alt" }
			case "profile":
				spec := f.specs["%1"]
				spec.CAAMProfile = "pinned-launch"
				f.specs["%1"] = spec
			case "omitted_environment":
				spec := f.specs["%1"]
				spec.OmittedEnv = []string{"SECRET"}
				f.specs["%1"] = spec
			case "receipt_storage_failure":
				f.receiptError = errors.New("read-only runtime store")
			case "activation_failure":
				f.activationError = errors.New("activation failed")
			case "provider_storage_failure":
				f.monitor.checker.readProviderState = func(string) (accountProviderState, error) { return accountProviderState{}, errors.New("unavailable") }
			case "new_process_after_receipt":
				f.active = "acct-alt"
				f.provider = accountProviderState{AttemptAt: f.now.Add(-time.Minute), ActivatedAt: f.now.Add(-time.Minute), Account: "acct-alt"}
				f.started["%1"] = f.now.UnixMilli()
			case "active_receipt_mismatch":
				f.provider = accountProviderState{AttemptAt: f.now.Add(-time.Minute), ActivatedAt: f.now.Add(-time.Minute), Account: "acct-alt"}
			}
			decisions := f.monitor.RunOnce(t.Context())
			if len(decisions) != 1 || decisions[0].ConversationRecovered || f.respawned != 0 {
				t.Fatalf("unproven recovery acted: %+v; respawned=%d", decisions, f.respawned)
			}
			wantActivations := 0
			if scenario == "activation_failure" || scenario == "receipt_storage_failure" {
				wantActivations = 1
			}
			if f.activated != wantActivations {
				t.Fatalf("activated=%d want=%d; decision=%+v", f.activated, wantActivations, decisions)
			}
			if f.provider.Account != "" && (scenario == "activation_failure" || scenario == "receipt_storage_failure") {
				t.Fatalf("failed action wrote successful receipt: %+v", f.provider)
			}
		})
	}
}

func TestAccountFailoverMonitorFailedActivationBlocksSiblingAttempts(t *testing.T) {
	f := newAccountRecoveryFixture(t, 2)
	f.activationError = errors.New("activation failed")
	decisions := f.monitor.RunOnce(t.Context())
	if len(decisions) != 2 || f.activated != 1 || f.respawned != 0 || decisions[1].DeclineReason != "provider_cooldown" {
		t.Fatalf("failed provider was retried: %+v activated=%d respawned=%d", decisions, f.activated, f.respawned)
	}
}

func TestAccountFailoverMonitorRetriesTransientProofWithoutBurningCooldown(t *testing.T) {
	f := newAccountRecoveryFixture(t, 1)
	f.bindingError = errors.New("main transcript is temporarily closed")
	decisions := f.monitor.RunOnce(t.Context())
	if len(decisions) != 1 || decisions[0].DeclineReason != "native_binding_unverified" || f.activated != 0 {
		t.Fatalf("unexpected initial refusal: %+v", decisions)
	}
	f.bindingError = nil
	decisions = f.monitor.RunOnce(t.Context())
	if len(decisions) != 1 || !decisions[0].ConversationRecovered || f.activated != 1 {
		t.Fatalf("transient proof failure burned the cooldown: %+v", decisions)
	}
}

func TestAccountFailoverMonitorActualFailedAttemptConsumesPaneCooldown(t *testing.T) {
	f := newAccountRecoveryFixture(t, 1)
	f.activationError = errors.New("activation failed")
	first := f.monitor.RunOnce(t.Context())
	if len(first) != 1 || !first[0].AccountActivationAttempted {
		t.Fatalf("attempt was not reported: %+v", first)
	}
	f.activationError = nil
	second := f.monitor.RunOnce(t.Context())
	if len(second) != 1 || second[0].DeclineReason != "cooldown" || f.activated != 1 {
		t.Fatalf("actual failure was retried: %+v activations=%d", second, f.activated)
	}
}

func TestAccountFailoverMonitorRechecksNativeBindingAfterFinalAccountQuery(t *testing.T) {
	f := newAccountRecoveryFixture(t, 1)
	f.queryHook = func() {
		if f.respawned > 0 {
			f.wrongConversation = true
		}
	}
	decisions := f.monitor.RunOnce(t.Context())
	if len(decisions) != 1 || decisions[0].ConversationRecovered || !decisions[0].AccountActivated || f.respawned != 1 {
		t.Fatalf("stale native proof reported recovery: %+v", decisions)
	}
}

func TestAccountFailoverMonitorRequiresNativeProofAfterReady(t *testing.T) {
	f := newAccountRecoveryFixture(t, 1)
	f.wrongConversation = true
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	decisions := f.monitor.RunOnce(ctx)
	if len(decisions) != 1 || !decisions[0].AccountActivated || decisions[0].ConversationRecovered || f.respawned != 1 {
		t.Fatalf("readiness was mistaken for native conversation recovery: %+v", decisions)
	}
}

func TestAccountFailoverProviderLockAndReceiptAreIndependentOfSelectedConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("global recovery uses Linux process proof")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := accountFailoverStatePath()
	if err != nil || path != filepath.Join(os.Getenv("HOME"), ".config", "ntm", "state.db") {
		t.Fatalf("global path=%q err=%v", path, err)
	}
	release, err := acquireFailoverProviderLock(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "different.toml"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if duplicate, err := acquireFailoverProviderLock(ctx, "claude"); err == nil {
		duplicate()
		t.Fatal("different config bypassed the global credential lock")
	}
	other, err := acquireFailoverProviderLock(t.Context(), "openai")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	again, err := acquireFailoverProviderLock(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestAccountFailoverMonitorReusesDurableReceiptAcrossSessions(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	attachStore := func(f *accountRecoveryFixture) {
		fc := f.monitor.checker
		fc.store = store
		fc.storeOnce.Do(func() {})
		fc.readProviderState = fc.storedProviderState
		fc.writeProviderWatermark = fc.storeProviderWatermark
	}
	first := newAccountRecoveryFixture(t, 1)
	attachStore(first)
	if decisions := first.monitor.RunOnce(t.Context()); len(decisions) != 1 || !decisions[0].ConversationRecovered || first.activated != 1 {
		t.Fatalf("initial activation failed: %+v", decisions)
	}
	second := newAccountRecoveryFixture(t, 1)
	second.monitor.checker.session = "another-session"
	second.active = "acct-alt"
	second.now = first.now.Add(time.Minute)
	attachStore(second)
	decisions := second.monitor.RunOnce(t.Context())
	if len(decisions) != 1 || !decisions[0].ConversationRecovered || decisions[0].AccountActivated || second.activated != 0 {
		t.Fatalf("new session did not reuse the durable successful activation: %+v", decisions)
	}
	attempt, err := store.GetWatermark(watermarkTypeCaamProviderAttempt, "claude")
	if err != nil || attempt == nil || attempt.LastTs == nil || !attempt.LastTs.Equal(first.now) {
		t.Fatalf("sibling moved the provider attempt cooldown: %+v err=%v", attempt, err)
	}
	receipt, err := store.GetWatermark(watermarkTypeCaamProviderActivated, "claude")
	if err != nil || receipt == nil || receipt.Consumer != "acct-alt" {
		t.Fatalf("successful activation receipt=%+v err=%v", receipt, err)
	}
}

// TestFailoverChecker_DecisionTable exercises the trigger over
// (limited x allow-list x horizon x working x cooldown x alternate-available)
// with fake captures and a stub caam responder.
func TestFailoverChecker_DecisionTable(t *testing.T) {
	// Fixed 'now' at 22:00 local: "resets at 3:00 am" is 5h away (beyond a
	// 30-minute horizon).
	nightNow := time.Date(2026, 8, 15, 22, 0, 0, 0, time.Local)
	// 02:45 local: "resets at 3:00 am" is 15 minutes away (within horizon).
	preDawnNow := time.Date(2026, 8, 15, 2, 45, 0, 0, time.Local)

	cases := []struct {
		name          string
		providers     []string
		horizonMin    int
		capture       string
		now           time.Time
		cooldownAgo   time.Duration // >0 seeds a prior switch this long ago
		noAlternate   bool
		listErr       error
		guardErr      error
		caamDown      bool
		switchFails   bool
		wantAction    string // "" = no decision at all
		wantDecline   string // prefix match
		wantPublished int
		wantSwitches  int
	}{
		{
			name: "not limited produces no decision", providers: []string{"claude"},
			horizonMin: 30, capture: failoverIdleCapture, now: nightNow,
			wantAction: "", wantPublished: 0,
		},
		{
			name: "provider not allow-listed declines", providers: []string{"openai"},
			horizonMin: 30, capture: limitedCapture, now: nightNow,
			wantAction: "declined", wantDecline: "provider_not_allowlisted", wantPublished: 1,
		},
		{
			name: "empty allow-list declines (doubly opt-in)", providers: nil,
			horizonMin: 30, capture: limitedCapture, now: nightNow,
			wantAction: "declined", wantDecline: "provider_not_allowlisted", wantPublished: 1,
		},
		{
			name: "working pane declines", providers: []string{"claude"},
			horizonMin: 30, capture: limitedWorkingCapture, now: nightNow,
			wantAction: "declined", wantDecline: "working", wantPublished: 1,
		},
		{
			name: "recent auto-switch declines (cooldown)", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow, cooldownAgo: 20 * time.Minute,
			wantAction: "declined", wantDecline: "cooldown", wantPublished: 1,
		},
		{
			name: "cooldown expired proceeds", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow, cooldownAgo: 2 * time.Hour,
			wantAction: "recovered", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "reset within horizon declines (wait seconds)", providers: []string{"claude"},
			horizonMin: 30, capture: limitedSoonCapture, now: nightNow,
			wantAction: "declined", wantDecline: "reset_within_horizon:", wantPublished: 1,
		},
		{
			name: "reset beyond horizon proceeds (wait seconds)", providers: []string{"claude"},
			horizonMin: 30, capture: limitedLongWaitCapture, now: nightNow,
			wantAction: "recovered", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "clock reset hint within horizon declines", providers: []string{"claude"},
			horizonMin: 30, capture: limitedClockCapture, now: preDawnNow,
			wantAction: "declined", wantDecline: "reset_within_horizon:", wantPublished: 1,
		},
		{
			name: "clock reset hint beyond horizon proceeds", providers: []string{"claude"},
			horizonMin: 30, capture: limitedClockCapture, now: nightNow,
			wantAction: "recovered", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "unparseable reset hint treated as beyond horizon", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow,
			wantAction: "recovered", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "horizon zero fails over on any detected limit", providers: []string{"claude"},
			horizonMin: 0, capture: limitedSoonCapture, now: nightNow,
			wantAction: "recovered", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "caam unavailable degrades silently", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow, caamDown: true,
			wantAction: "declined", wantDecline: "caam_unavailable", wantPublished: 0,
		},
		{
			name: "rotation safety guard refusal declines", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow,
			guardErr:   swarm.ErrRotationBlocked,
			wantAction: "declined", wantDecline: "rotation_blocked", wantPublished: 1,
		},
		{
			name: "caam query failure declines (fail closed)", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow, listErr: errors.New("caam exploded"),
			wantAction: "declined", wantDecline: "caam_query_failed", wantPublished: 1,
		},
		{
			name: "no alternate account declines", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow, noAlternate: true,
			wantAction: "declined", wantDecline: "no_alternate_account", wantPublished: 1,
		},
		{
			name: "switch failure is reported", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow, switchFails: true,
			wantAction: "recovery_failed", wantPublished: 1, wantSwitches: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane := foPane("%9", "fosess__cc_1")
			env := newFailoverTestEnv(t, tc.providers, tc.horizonMin,
				[]tmux.Pane{pane}, map[string]string{"%9": tc.capture})
			env.fc.now = func() time.Time { return tc.now }
			if tc.cooldownAgo > 0 {
				env.watermark["fosess:fosess__cc_1"] = tc.now.Add(-tc.cooldownAgo)
			}
			if tc.caamDown {
				env.fc.caamAvailable = func() bool { return false }
			}
			if tc.listErr != nil {
				env.fc.listAccounts = func(context.Context, string) ([]swarm.AccountInfo, error) { return nil, tc.listErr }
			}
			if tc.guardErr != nil {
				env.fc.guardSwitch = func(context.Context, tmux.Pane, string) error { return tc.guardErr }
			}
			if tc.noAlternate {
				env.fc.listAccounts = func(_ context.Context, provider string) ([]swarm.AccountInfo, error) {
					return []swarm.AccountInfo{
						{Provider: provider, AccountName: "acct-active", IsActive: true},
						{Provider: provider, AccountName: "acct-cool", RateLimited: true},
					}, nil
				}
			}
			if tc.switchFails {
				env.fc.recoverPane = func(_ context.Context, _ tmux.Pane, provider string, request accountRecoveryRequest) (accountRecoveryOutcome, error) {
					if !env.fc.claimSwitch(request.Scope, provider, env.fc.now(), request.ObservedSwitch) {
						return accountRecoveryOutcome{}, &accountRecoveryDecline{reason: "cooldown"}
					}
					env.switched = append(env.switched, provider+":"+request.Account)
					return accountRecoveryOutcome{ActivationAttempted: true}, errors.New("activate failed")
				}
			}

			decisions := env.fc.runOnce(t.Context())
			for _, d := range decisions {
				t.Logf("decision: %+v", d)
			}

			if tc.wantAction == "" {
				if len(decisions) != 0 {
					t.Fatalf("decisions = %+v, want none", decisions)
				}
				if len(env.published) != 0 {
					t.Fatalf("published = %+v, want none", env.published)
				}
				return
			}

			if len(decisions) != 1 {
				t.Fatalf("got %d decisions, want 1: %+v", len(decisions), decisions)
			}
			d := decisions[0]
			if d.Action != tc.wantAction {
				t.Fatalf("action = %q, want %q (decision %+v)", d.Action, tc.wantAction, d)
			}
			if tc.wantDecline != "" && !strings.HasPrefix(d.DeclineReason, tc.wantDecline) {
				t.Fatalf("decline reason = %q, want prefix %q", d.DeclineReason, tc.wantDecline)
			}
			if len(env.switched) != tc.wantSwitches {
				t.Fatalf("switch calls = %v, want %d", env.switched, tc.wantSwitches)
			}
			if len(env.published) != tc.wantPublished {
				t.Fatalf("published %d records, want %d: %+v", len(env.published), tc.wantPublished, env.published)
			}

			// Evidence invariants on every published record.
			for _, rec := range env.published {
				if rec.Action != "caam_failover" || rec.Session != "fosess" ||
					rec.Source != "coordinator.caam_failover" {
					t.Errorf("published record identity = %+v", rec)
				}
				if !strings.Contains(rec.MessagePreview, "provider=") ||
					!strings.Contains(rec.MessagePreview, "banner=") ||
					!strings.Contains(rec.MessagePreview, "reset_hint=") ||
					!strings.Contains(rec.MessagePreview, "account=") {
					t.Errorf("published evidence missing fields: %q", rec.MessagePreview)
				}
			}

			if tc.wantAction == "recovered" {
				if d.ChosenAccount != "acct-alt" {
					t.Errorf("chosen account = %q, want acct-alt", d.ChosenAccount)
				}
				if d.PrevAccount != "acct-active" {
					t.Errorf("previous account = %q, want acct-active", d.PrevAccount)
				}
				if d.Banner == "" {
					t.Error("banner evidence is empty on a switch")
				}
				// The attempt must start the per-pane cooldown.
				if at, ok := env.watermark["fosess:fosess__cc_1"]; !ok || !at.Equal(tc.now) {
					t.Errorf("cooldown watermark = %v (ok=%v), want %v", at, ok, tc.now)
				}
				if env.published[0].ReasonCode != "caam_failover_recovery" {
					t.Errorf("reason code = %q", env.published[0].ReasonCode)
				}
			}
			if tc.wantAction == "recovery_failed" {
				if env.published[0].Severity != robot.SeverityWarning {
					t.Errorf("switch failure severity = %q, want warning", env.published[0].Severity)
				}
				// Failed attempts start the cooldown too (no hammering caam).
				if _, ok := env.watermark["fosess:fosess__cc_1"]; !ok {
					t.Error("failed switch attempt did not start the cooldown")
				}
			}
			if tc.wantAction == "declined" {
				if len(env.published) == 1 {
					rec := env.published[0]
					if rec.ReasonCode != "caam_failover_declined" || !rec.Blocked {
						t.Errorf("decline record = %+v", rec)
					}
				}
				if len(env.switched) != 0 {
					t.Errorf("declined decision still switched: %v", env.switched)
				}
			}
		})
	}
}

// TestFailoverChecker_NeverSwitchesWithoutVerifiedAlternate pins the
// verification invariant: the switch seam must never fire when the caam
// query fails or returns no usable alternate.
func TestFailoverChecker_NeverSwitchesWithoutVerifiedAlternate(t *testing.T) {
	pane := foPane("%2", "fosess__cc_1")
	env := newFailoverTestEnv(t, []string{"claude"}, 30,
		[]tmux.Pane{pane}, map[string]string{"%2": limitedCapture})
	env.fc.listAccounts = func(context.Context, string) ([]swarm.AccountInfo, error) {
		return nil, nil // no accounts at all
	}
	decisions := env.fc.runOnce(t.Context())
	for _, d := range decisions {
		t.Logf("decision: %+v", d)
	}
	if len(env.switched) != 0 {
		t.Fatalf("switched without a verified alternate: %v", env.switched)
	}
	if len(decisions) != 1 || decisions[0].DeclineReason != "active_account_unverified" {
		t.Fatalf("decisions = %+v, want one active_account_unverified decline", decisions)
	}
}

// TestFailoverChecker_HonorsAccountPins exercises the PRODUCTION rotation
// safety guard: an operator pin persisted by `ntm rotate lock` (via
// swarm.SavePins) must make the auto-failover decline before ever querying
// for alternates or switching.
func TestFailoverChecker_HonorsAccountPins(t *testing.T) {
	dir := t.TempDir()
	pinner := swarm.NewAccountRotator()
	pinner.PinAccount("claude", "acct-active")
	if err := pinner.SavePins(dir); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultCAAMConfig()
	cfg.AutoFailover = true
	cfg.FailoverProviders = []string{"claude"}
	fc := newFailoverChecker("fosess", dir, cfg)
	if fc == nil {
		t.Fatal("checker not constructed")
	}

	pane := foPane("%7", "fosess__cc_1")
	var published []robot.ActuationRecord
	switched := 0
	fc.getPanes = func(context.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{pane}, nil }
	fc.capturePane = func(context.Context, string, int) (string, error) { return limitedCapture, nil }
	fc.paneCwd = func(context.Context, string) (string, error) { return dir, nil }
	fc.caamAvailable = func() bool { return true }
	fc.listAccounts = func(context.Context, string) ([]swarm.AccountInfo, error) {
		t.Error("alternate query ran despite a pinned provider")
		return nil, nil
	}
	fc.recoverPane = func(context.Context, tmux.Pane, string, accountRecoveryRequest) (accountRecoveryOutcome, error) {
		switched++
		return accountRecoveryOutcome{}, nil
	}
	fc.lastSwitchAt = func(string) (time.Time, bool) { return time.Time{}, false }
	fc.claimSwitch = func(string, string, time.Time, *time.Time) bool { return true }
	fc.publish = func(r robot.ActuationRecord) { published = append(published, r) }

	decisions := fc.runOnce(context.Background())
	for _, d := range decisions {
		t.Logf("decision: %+v", d)
	}
	if len(decisions) != 1 || decisions[0].DeclineReason != "rotation_blocked" {
		t.Fatalf("decisions = %+v, want one rotation_blocked decline", decisions)
	}
	if switched != 0 {
		t.Fatalf("switched %d times despite a pinned provider", switched)
	}
	if len(published) != 1 {
		t.Fatalf("published %d records, want 1", len(published))
	}
}

// TestFailoverChecker_DeclineRepublishSuppression verifies an unchanged
// decline reason is not re-published every tick.
func TestFailoverChecker_DeclineRepublishSuppression(t *testing.T) {
	pane := foPane("%4", "fosess__cc_1")
	env := newFailoverTestEnv(t, nil, 30,
		[]tmux.Pane{pane}, map[string]string{"%4": limitedCapture})

	base := time.Date(2026, 8, 15, 22, 0, 0, 0, time.Local)
	now := base
	env.fc.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		decisions := env.fc.runOnce(t.Context())
		for _, d := range decisions {
			t.Logf("tick %d decision: %+v", i, d)
		}
		if len(decisions) != 1 {
			t.Fatalf("tick %d: decisions = %+v, want 1", i, decisions)
		}
		now = now.Add(5 * time.Second)
	}
	if len(env.published) != 1 {
		t.Fatalf("published %d records over 3 ticks, want 1 (suppressed)", len(env.published))
	}

	// After the republish interval the same reason is published again.
	now = base.Add(failoverRepublishInterval + time.Minute)
	env.fc.runOnce(t.Context())
	if len(env.published) != 2 {
		t.Fatalf("published %d records after interval, want 2", len(env.published))
	}
}

// TestFailoverChecker_CooldownPersistsInRealStore exercises the production
// watermark path against a real temp runtime store: a switch recorded by one
// checker instance blocks a NEW checker instance (fresh process simulation)
// within the hour, and stops blocking after it.
func TestFailoverChecker_CooldownPersistsInRealStore(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	newChecker := func(now time.Time) *failoverTestEnv {
		pane := foPane("%5", "fosess__cc_1")
		env := newFailoverTestEnv(t, []string{"claude"}, 30,
			[]tmux.Pane{pane}, map[string]string{"%5": limitedCapture})
		env.fc.now = func() time.Time { return now }
		// Production watermark path against the injected real store.
		env.fc.store = store
		env.fc.storeOnce.Do(func() {})
		env.fc.lastSwitchAt = env.fc.storedLastSwitch
		env.fc.claimSwitch = env.fc.storeLastSwitch
		return env
	}

	t0 := time.Date(2026, 8, 15, 22, 0, 0, 0, time.UTC)

	first := newChecker(t0)
	decisions := first.runOnceLogged(t)
	if len(decisions) != 1 || decisions[0].Action != "recovered" {
		t.Fatalf("first checker decisions = %+v, want one switched", decisions)
	}

	// A brand-new checker (same store) within the hour must decline.
	second := newChecker(t0.Add(30 * time.Minute))
	decisions = second.runOnceLogged(t)
	if len(decisions) != 1 || decisions[0].DeclineReason != "cooldown" {
		t.Fatalf("second checker decisions = %+v, want one cooldown decline", decisions)
	}
	if len(second.switched) != 0 {
		t.Fatalf("second checker switched during cooldown: %v", second.switched)
	}

	// After the hour the persisted watermark no longer blocks.
	third := newChecker(t0.Add(failoverSwitchCooldown + time.Minute))
	decisions = third.runOnceLogged(t)
	if len(decisions) != 1 || decisions[0].Action != "recovered" {
		t.Fatalf("third checker decisions = %+v, want one switched", decisions)
	}
}

func (env *failoverTestEnv) runOnceLogged(t *testing.T) []failoverDecision {
	t.Helper()
	decisions := env.fc.runOnce(context.Background())
	for _, d := range decisions {
		t.Logf("decision: %+v", d)
	}
	return decisions
}

// TestMaybeCheckCaamFailover_DefaultOffBuildsNothing pins the default-off
// guarantee at the construction level: no checker object is ever created when
// auto_failover is unset or no NTM config is loaded.
func TestMaybeCheckCaamFailover_DefaultOffBuildsNothing(t *testing.T) {
	c := New("fo-off", t.TempDir(), nil, "Coordinator")
	c.maybeCheckCaamFailover(t.Context())
	if c.caamFailover != nil {
		t.Fatal("failover checker constructed despite nil ntm config")
	}

	cfg := config.Default()
	c.ntmConfig = cfg
	if cfg.Integrations.CAAM.AutoFailover {
		t.Fatal("auto_failover must default to false")
	}
	c.maybeCheckCaamFailover(t.Context())
	if c.caamFailover != nil {
		t.Fatal("failover checker constructed despite auto_failover=false")
	}
}

// TestRunCycle_CaamFailoverGating is the integration-shaped tick test: the
// coordinator cycle invokes the failover checker exactly when auto_failover
// is configured, and never touches it otherwise.
func TestRunCycle_CaamFailoverGating(t *testing.T) {
	origGetPanesWithActivity := getPanesWithActivity
	origCaptureForHealthCheckWithCtx := captureForHealthCheckWithCtx
	t.Cleanup(func() {
		getPanesWithActivity = origGetPanesWithActivity
		captureForHealthCheckWithCtx = origCaptureForHealthCheckWithCtx
	})
	getPanesWithActivity = func(session string) ([]tmux.PaneActivity, error) {
		return nil, nil
	}
	captureForHealthCheckWithCtx = func(_ context.Context, paneID string) (string, error) {
		return failoverIdleCapture, nil
	}

	c := New("fo-tick", t.TempDir(), nil, "Coordinator")
	c.monitor = NewAgentMonitor(c.session, nil, c.projectKey)

	calls := 0
	c.caamFailover = &failoverChecker{
		session: "fo-tick",
		getPanes: func(context.Context, string) ([]tmux.Pane, error) {
			calls++
			return nil, nil
		},
	}

	// auto_failover unset: the checker must never run.
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if calls != 0 {
		t.Fatalf("failover checker ran %d times with auto_failover off, want 0", calls)
	}

	// auto_failover configured: the tick runs the checker once per cycle.
	cfg := config.Default()
	cfg.Integrations.CAAM.AutoFailover = true
	c.ntmConfig = cfg
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if calls != 1 {
		t.Fatalf("failover checker ran %d times, want 1", calls)
	}
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if calls != 2 {
		t.Fatalf("failover checker ran %d times after two cycles, want 2", calls)
	}
}

// TestNewFailoverChecker_ConstructionGate verifies the production constructor
// honors the default-off gate and canonicalizes the provider allow-list.
func TestNewFailoverChecker_ConstructionGate(t *testing.T) {
	off := config.DefaultCAAMConfig()
	if fc := newFailoverChecker("s", t.TempDir(), off); fc != nil {
		t.Fatal("checker constructed with auto_failover=false")
	}

	on := config.DefaultCAAMConfig()
	on.AutoFailover = true
	on.ResetHorizonMinutes = 45
	on.FailoverProviders = []string{"Anthropic", "cod", "bogus"}
	fc := newFailoverChecker("s", t.TempDir(), on)
	if fc == nil {
		t.Fatal("checker not constructed with auto_failover=true")
	}
	if fc.guardSwitch == nil {
		t.Error("production checker missing the rotation safety guard seam")
	}
	if fc.horizon != 45*time.Minute {
		t.Errorf("horizon = %v, want 45m", fc.horizon)
	}
	if !fc.providers["claude"] || !fc.providers["openai"] {
		t.Errorf("providers = %v, want claude+openai", fc.providers)
	}
	if fc.providers["bogus"] || len(fc.providers) != 2 {
		t.Errorf("providers = %v, want exactly claude+openai", fc.providers)
	}
}

// TestResetBeyondHorizon covers the horizon decision helper, including the
// documented unparseable-hint rule.
func TestResetBeyondHorizon(t *testing.T) {
	now := time.Date(2026, 8, 15, 22, 0, 0, 0, time.Local)
	horizon := 30 * time.Minute
	mk := func(wait int, hint string) (bool, string) {
		det := detectionFor(wait, hint)
		return resetBeyondHorizon(det, now, horizon)
	}

	if beyond, detail := mk(300, ""); beyond {
		t.Errorf("wait 300s beyond 30m horizon = true (%s), want false", detail)
	}
	if beyond, detail := mk(7200, ""); !beyond {
		t.Errorf("wait 7200s beyond 30m horizon = false (%s), want true", detail)
	}
	if beyond, detail := mk(0, "try again at 10:15 PM"); beyond {
		t.Errorf("reset in 15m beyond 30m horizon = true (%s), want false", detail)
	}
	if beyond, detail := mk(0, "resets at 3am (America/New_York)"); !beyond {
		t.Errorf("reset at 3am from 22:00 beyond 30m horizon = false (%s), want true", detail)
	}
	if beyond, detail := mk(0, "please slow down"); !beyond || detail != "unparseable_reset_hint" {
		t.Errorf("unparseable hint = (%v, %s), want (true, unparseable_reset_hint)", beyond, detail)
	}
	if beyond, detail := mk(0, ""); !beyond {
		t.Errorf("absent hint = (false, %s), want true", detail)
	}
	if beyond, detail := resetBeyondHorizon(detectionFor(60, ""), now, 0); !beyond || detail != "horizon_disabled" {
		t.Errorf("horizon 0 = (%v, %s), want (true, horizon_disabled)", beyond, detail)
	}
	t.Logf("horizon decision helper behaves per the documented table")
}

// TestParseResetClock covers 12h/24h clock extraction and next-occurrence
// rollover.
func TestParseResetClock(t *testing.T) {
	now := time.Date(2026, 8, 15, 22, 0, 0, 0, time.Local)
	cases := []struct {
		hint     string
		ok       bool
		wantHour int
		wantMin  int
		nextDay  bool
	}{
		{"try again at 7:00 PM", true, 19, 0, true}, // 19:00 <= 22:00 -> tomorrow
		{"resets at 11:30 pm", true, 23, 30, false}, // later tonight
		{"resets at 3am", true, 3, 0, true},         // tomorrow morning
		{"resets 19:30", true, 19, 30, true},        // 24h form, tomorrow
		{"available again at 23:59", true, 23, 59, false},
		{"quota exceeded, please wait", false, 0, 0, false},
		{"", false, 0, 0, false},
	}
	for _, tc := range cases {
		at, ok := parseResetClock(tc.hint, now)
		t.Logf("parseResetClock(%q) = %v, %v", tc.hint, at, ok)
		if ok != tc.ok {
			t.Errorf("parseResetClock(%q) ok = %v, want %v", tc.hint, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if at.Hour() != tc.wantHour || at.Minute() != tc.wantMin {
			t.Errorf("parseResetClock(%q) = %v, want %02d:%02d", tc.hint, at, tc.wantHour, tc.wantMin)
		}
		if !at.After(now) {
			t.Errorf("parseResetClock(%q) = %v, not after now %v", tc.hint, at, now)
		}
		if gotNextDay := at.Day() != now.Day(); gotNextDay != tc.nextDay {
			t.Errorf("parseResetClock(%q) nextDay = %v, want %v", tc.hint, gotNextDay, tc.nextDay)
		}
	}
}

// TestMatchedBannerLine verifies best-effort banner evidence extraction.
func TestMatchedBannerLine(t *testing.T) {
	banner := matchedBannerLine(limitedCapture, "cc")
	t.Logf("banner: %q", banner)
	if !strings.Contains(banner, "rate limit exceeded") {
		t.Errorf("banner = %q, want the rate limit line", banner)
	}
	if got := matchedBannerLine(failoverIdleCapture, "cc"); got != "" {
		t.Errorf("banner for idle capture = %q, want empty", got)
	}
}

// detectionFor builds a synthetic detection for helper tests.
func detectionFor(wait int, hint string) ratelimit.RateLimitDetection {
	return ratelimit.RateLimitDetection{
		RateLimited: true,
		WaitSeconds: wait,
		ResetHint:   hint,
	}
}
