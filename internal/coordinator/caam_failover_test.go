package coordinator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		getPanes: func(string) ([]tmux.Pane, error) {
			return panes, nil
		},
		capturePane: func(paneID string, lines int) (string, error) {
			capture, ok := captures[paneID]
			if !ok {
				return "", errors.New("no capture for pane")
			}
			return capture, nil
		},
		caamAvailable: func() bool { return true },
		listAccounts: func(provider string) ([]swarm.AccountInfo, error) {
			env.queried = append(env.queried, provider)
			return []swarm.AccountInfo{
				{Provider: provider, AccountName: "acct-active", IsActive: true},
				{Provider: provider, AccountName: "acct-alt"},
			}, nil
		},
		switchAccount: func(provider, accountID string) (*robot.SwitchAccountOutput, error) {
			env.switched = append(env.switched, provider+":"+accountID)
			return &robot.SwitchAccountOutput{
				Switch: robot.SwitchAccountResult{
					Success:         true,
					Provider:        provider,
					PreviousAccount: "acct-active",
					NewAccount:      accountID,
				},
			}, nil
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
	fc.recordSwitch = func(scope, provider string, at time.Time) {
		env.watermark[scope] = at
	}
	env.fc = fc
	return env
}

func foPane(id, title string) tmux.Pane {
	return tmux.Pane{ID: id, Title: title, Type: tmux.AgentClaude, Width: 120}
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
			wantAction: "switched", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "reset within horizon declines (wait seconds)", providers: []string{"claude"},
			horizonMin: 30, capture: limitedSoonCapture, now: nightNow,
			wantAction: "declined", wantDecline: "reset_within_horizon:", wantPublished: 1,
		},
		{
			name: "reset beyond horizon proceeds (wait seconds)", providers: []string{"claude"},
			horizonMin: 30, capture: limitedLongWaitCapture, now: nightNow,
			wantAction: "switched", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "clock reset hint within horizon declines", providers: []string{"claude"},
			horizonMin: 30, capture: limitedClockCapture, now: preDawnNow,
			wantAction: "declined", wantDecline: "reset_within_horizon:", wantPublished: 1,
		},
		{
			name: "clock reset hint beyond horizon proceeds", providers: []string{"claude"},
			horizonMin: 30, capture: limitedClockCapture, now: nightNow,
			wantAction: "switched", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "unparseable reset hint treated as beyond horizon", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow,
			wantAction: "switched", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "horizon zero fails over on any detected limit", providers: []string{"claude"},
			horizonMin: 0, capture: limitedSoonCapture, now: nightNow,
			wantAction: "switched", wantPublished: 1, wantSwitches: 1,
		},
		{
			name: "caam unavailable degrades silently", providers: []string{"claude"},
			horizonMin: 30, capture: limitedCapture, now: nightNow, caamDown: true,
			wantAction: "declined", wantDecline: "caam_unavailable", wantPublished: 0,
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
			wantAction: "switch_failed", wantPublished: 1, wantSwitches: 1,
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
				env.fc.listAccounts = func(string) ([]swarm.AccountInfo, error) { return nil, tc.listErr }
			}
			if tc.noAlternate {
				env.fc.listAccounts = func(provider string) ([]swarm.AccountInfo, error) {
					return []swarm.AccountInfo{
						{Provider: provider, AccountName: "acct-active", IsActive: true},
						{Provider: provider, AccountName: "acct-cool", RateLimited: true},
					}, nil
				}
			}
			if tc.switchFails {
				env.fc.switchAccount = func(provider, accountID string) (*robot.SwitchAccountOutput, error) {
					env.switched = append(env.switched, provider+":"+accountID)
					return &robot.SwitchAccountOutput{
						Switch: robot.SwitchAccountResult{Success: false, Provider: provider, Error: "activate failed"},
					}, nil
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

			if tc.wantAction == "switched" {
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
				if env.published[0].ReasonCode != "caam_failover_switch" {
					t.Errorf("reason code = %q", env.published[0].ReasonCode)
				}
			}
			if tc.wantAction == "switch_failed" {
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
	env.fc.listAccounts = func(string) ([]swarm.AccountInfo, error) {
		return nil, nil // no accounts at all
	}
	decisions := env.fc.runOnce(t.Context())
	for _, d := range decisions {
		t.Logf("decision: %+v", d)
	}
	if len(env.switched) != 0 {
		t.Fatalf("switched without a verified alternate: %v", env.switched)
	}
	if len(decisions) != 1 || decisions[0].DeclineReason != "no_alternate_account" {
		t.Fatalf("decisions = %+v, want one no_alternate_account decline", decisions)
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
		env.fc.recordSwitch = env.fc.storeLastSwitch
		return env
	}

	t0 := time.Date(2026, 8, 15, 22, 0, 0, 0, time.UTC)

	first := newChecker(t0)
	decisions := first.runOnceLogged(t)
	if len(decisions) != 1 || decisions[0].Action != "switched" {
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
	if len(decisions) != 1 || decisions[0].Action != "switched" {
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
		getPanes: func(string) ([]tmux.Pane, error) {
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
	if fc := newFailoverChecker("s", off); fc != nil {
		t.Fatal("checker constructed with auto_failover=false")
	}

	on := config.DefaultCAAMConfig()
	on.AutoFailover = true
	on.ResetHorizonMinutes = 45
	on.FailoverProviders = []string{"Anthropic", "cod", "bogus"}
	fc := newFailoverChecker("s", on)
	if fc == nil {
		t.Fatal("checker not constructed with auto_failover=true")
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
