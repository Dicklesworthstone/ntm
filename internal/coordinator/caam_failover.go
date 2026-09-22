// The coordinator and resident swarm monitor share this account-recovery
// engine. It is opt-in, provider-allowlisted, and limited to Linux Claude/Codex
// panes with proven global credentials and an opened native conversation.
// Fresh pane captures must show a rate limit beyond the wait horizon without
// active work. Saved launch settings, physical identity, worktree, conversation
// and operator pins are rechecked before the shared robot restart executor acts.
//
// Provider locks and durable attempt cooldowns prevent sessions from racing to
// overwrite shared credentials. A successful activation receipt lets sibling
// panes resume on that account without activating another account. A ready
// process counts as recovered only after its native conversation is independently
// confirmed. Account activation and conversation recovery have separate receipts.
// Identical declines are published at most every failoverRepublishInterval.
package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// failoverCaptureLines is how much fresh pane tail is captured for detection
// and the fire-time safety gates (same bound rotation.go uses).
const failoverCaptureLines = 100

// failoverSwitchCooldown is the mandatory minimum spacing between automatic
// switches for one pane. Fixed by design, not configurable.
const failoverSwitchCooldown = time.Hour

// failoverRepublishInterval bounds attention-feed noise: an unchanged decline
// reason for a pane is re-published at most this often (every occurrence is
// still logged via slog).
const failoverRepublishInterval = 10 * time.Minute

// watermarkTypeCaamFailover is the runtime-store watermark type persisting
// the last auto-switch attempt per pane, following the disk_sample /
// output_seq precedent of documenting per-type column reuse instead of
// widening the schema. For rows of this type:
//
//	Scope      — "<session>:<agent ID>" (pane title; pane ID when untitled)
//	LastTs     — when the last auto-switch attempt fired
//	Consumer   — the caam provider that was switched, informational only
const watermarkTypeCaamFailover = "caam_failover"

// Provider rows share the user's global credential scope, across all sessions
// and selected NTM configuration files. Consumer contains an opaque CAAM
// account identifier, never a credential. A success receipt is written only
// after both activation and a fresh active-account query succeed.
const watermarkTypeCaamProviderAttempt = "caam_provider_attempt"
const watermarkTypeCaamProviderActivated = "caam_provider_activated"

// failoverDecision records one failover decision for logging and tests.
type AccountFailoverDecision struct {
	PaneID                     string // tmux pane ID (%N)
	AgentID                    string // canonical agent ID (pane title, e.g. sess__cc_1)
	Provider                   string // caam provider ("claude", "openai", "gemini")
	Action                     string // "switched", "switch_failed", "declined"
	DeclineReason              string // populated when Action == "declined"
	Banner                     string // evidence: matched rate-limit banner line (best-effort)
	ResetHint                  string // evidence: human-readable reset phrase, if any
	WaitSeconds                int    // evidence: parsed wait seconds, if any
	ChosenAccount              string // evidence: alternate account selected (switch attempts)
	PrevAccount                string // evidence: account switched away from (successful switches)
	AccountActivationAttempted bool
	AccountActivated           bool
	AccountActivationUnknown   bool
	ConversationRecovered      bool
	NativeSessionID            string
	Error                      string
}

type failoverDecision = AccountFailoverDecision

// AccountFailoverTarget restricts a resident swarm monitor to a successfully
// launched physical pane and its original project, excluding later additions.
type AccountFailoverTarget struct {
	AgentType  string
	ProjectDir string
}

type AccountFailoverOptions struct {
	Config                 *config.Config
	Targets                map[string]AccountFailoverTarget
	ForceGlobalAuthClobber bool
}

// AccountFailoverMonitor exposes the coordinator's existing decision engine to
// the resident internal monitor. It does not start assignments or other daemons.
type AccountFailoverMonitor struct{ checker *failoverChecker }

func NewAccountFailoverMonitor(session string, opts AccountFailoverOptions) (*AccountFailoverMonitor, error) {
	if err := tmux.ValidateSessionName(session); err != nil {
		return nil, err
	}
	if opts.Config == nil || !opts.Config.Integrations.CAAM.AutoFailover || len(opts.Targets) == 0 {
		return nil, errors.New("account failover requires enabled configuration and explicit pane targets")
	}
	fc := newFailoverChecker(session, "", opts.Config.Integrations.CAAM)
	fc.config = opts.Config
	fc.forceGlobal = opts.ForceGlobalAuthClobber
	fc.targets = make(map[string]AccountFailoverTarget, len(opts.Targets))
	for paneID, target := range opts.Targets {
		if !regexp.MustCompile(`^%[0-9]+$`).MatchString(paneID) || !filepath.IsAbs(target.ProjectDir) ||
			!hasWorkingDetector(agent.AgentType(target.AgentType).Canonical()) {
			return nil, fmt.Errorf("invalid account failover target %q", paneID)
		}
		fc.targets[paneID] = target
	}
	return &AccountFailoverMonitor{checker: fc}, nil
}

func (m *AccountFailoverMonitor) RunOnce(ctx context.Context) []AccountFailoverDecision {
	if m == nil || m.checker == nil {
		return nil
	}
	return m.checker.runOnce(ctx)
}

// Close releases runtime-store resources after the caller cancels and joins
// its monitor loop. It does not stop a loop owned by another process.
func (m *AccountFailoverMonitor) Close() error {
	if m == nil || m.checker == nil || m.checker.store == nil {
		return nil
	}
	return m.checker.store.Close()
}

// PreflightAccountRotation rejects unavailable account infrastructure before a
// swarm creates sessions. Live credential scope, pins, and native conversation
// proof are intentionally checked again against each actual pane at fire time.
func PreflightAccountRotation(ctx context.Context, cfg config.CAAMConfig) error {
	if ctx == nil {
		return errors.New("account rotation preflight requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("automatic global account recovery requires Linux process and credential proof")
	}
	if len(cfg.FailoverProviders) == 0 {
		return errors.New("account rotation requires an explicit provider allowlist")
	}
	r := swarm.NewAccountRotator()
	if strings.TrimSpace(cfg.BinaryPath) != "" {
		r = r.WithCaamPath(cfg.BinaryPath)
	}
	for _, requested := range cfg.FailoverProviders {
		provider := canonicalFailoverProvider(requested)
		if provider != "claude" && provider != "openai" {
			return fmt.Errorf("automatic account recovery does not support provider %q", requested)
		}
		accounts, err := r.ListAccountsContext(ctx, provider)
		if err != nil {
			return fmt.Errorf("account rotation preflight for %s: %w", provider, err)
		}
		_, alternate, err := verifiedFailoverAccounts(accounts, "", time.Now())
		if err != nil {
			return fmt.Errorf("account rotation preflight for %s: %w", provider, err)
		}
		if alternate == nil {
			return fmt.Errorf("account rotation preflight for %s: no alternate account with headroom", provider)
		}
	}
	return nil
}

// failoverChecker performs the per-tick rate-limit failover check. All
// collaborators are injectable for tests; production wiring is installed by
// newFailoverChecker.
type failoverChecker struct {
	session     string
	workDir     string
	config      *config.Config
	forceGlobal bool
	targets     map[string]AccountFailoverTarget
	horizon     time.Duration
	providers   map[string]bool // canonical caam provider allow-list

	// Seams (default to real implementations).
	getPanes               func(context.Context, string) ([]tmux.Pane, error)
	capturePane            func(context.Context, string, int) (string, error)
	caamAvailable          func() bool
	guardSwitch            func(context.Context, tmux.Pane, string) error
	listAccounts           func(context.Context, string) ([]swarm.AccountInfo, error)
	recoverPane            func(context.Context, tmux.Pane, string, accountRecoveryRequest) (accountRecoveryOutcome, error)
	activateAccount        func(context.Context, string, string, swarm.AccountActivationPreflight) (*swarm.RotationRecord, error)
	paneCwd                func(context.Context, string) (string, error)
	readSpec               func(context.Context, string) (*tmux.AgentLaunchSpec, error)
	observeBinding         func(context.Context, string, string, int) (agentsession.GlobalCredentialBinding, error)
	restartPane            func(context.Context, robot.RestartPaneOptions) (*robot.RestartPaneOutput, error)
	lockProvider           func(context.Context, string) (func(), error)
	readProviderState      func(string) (accountProviderState, error)
	writeProviderWatermark func(string, string, string, time.Time) error
	lastSwitchAt           func(scope string) (time.Time, bool)
	// claimSwitch atomically takes the per-pane cooldown slot and reports
	// whether this process won it; a false return must decline the switch.
	claimSwitch func(scope, provider string, at time.Time, expected *time.Time) bool
	publish     func(record robot.ActuationRecord)
	now         func() time.Time

	// Cooldown fallback when the runtime store is unavailable, and decline
	// republish bookkeeping. Guarded by mu (runOnce may share the checker
	// with future callers, and the store seams close over it).
	mu            sync.Mutex
	memLastSwitch map[string]time.Time
	lastPublished map[string]declineMark // pane ID -> last published decline

	storeOnce sync.Once
	store     *state.Store
}

// declineMark tracks the last published decline for republish suppression.
type declineMark struct {
	reason string
	at     time.Time
}

// newFailoverChecker builds a production checker, or nil when the feature is
// disabled (auto_failover false). An empty provider allow-list still returns
// a checker — rate-limited panes then get an explicit, published
// "provider_not_allowlisted" decline, which is the operator's cue that the
// second opt-in is missing.
func newFailoverChecker(session, workDir string, caamCfg config.CAAMConfig) *failoverChecker {
	if !caamCfg.AutoFailover {
		return nil
	}

	providers := make(map[string]bool, len(caamCfg.FailoverProviders))
	for _, p := range caamCfg.FailoverProviders {
		if canonical := canonicalFailoverProvider(p); canonical != "" {
			providers[canonical] = true
		}
	}

	horizon := time.Duration(caamCfg.ResetHorizonMinutes) * time.Minute
	if horizon < 0 {
		horizon = 0
	}

	// Reuse swarm's caam machinery: availability probing, the fail-closed
	// `caam list --json` account query (ntm-9mt8.2 heritage), and the
	// automatic-rotation safety guard (pins; Codex global-auth protections).
	customPath := strings.TrimSpace(caamCfg.BinaryPath) != ""
	rotator := swarm.NewAccountRotator()
	if customPath {
		rotator = rotator.WithCaamPath(caamCfg.BinaryPath)
	}
	fullConfig := config.Default()
	fullConfig.Integrations.CAAM = caamCfg
	fc := &failoverChecker{
		session:       session,
		workDir:       workDir,
		config:        fullConfig,
		horizon:       horizon,
		providers:     providers,
		getPanes:      tmux.GetPanesContext,
		capturePane:   tmux.CapturePaneOutputContext,
		caamAvailable: rotator.IsAvailable,
		listAccounts:  rotator.ListAccountsContext,
		activateAccount: func(ctx context.Context, provider, account string, preflight swarm.AccountActivationPreflight) (*swarm.RotationRecord, error) {
			return rotator.SwitchToAccountContext(ctx, provider, account, preflight)
		},
		paneCwd: func(ctx context.Context, paneID string) (string, error) {
			out, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(paneID), "#{pane_current_path}")
			return strings.TrimSuffix(out, "\n"), err
		},
		readSpec:       tmux.ReadPaneLaunchSpecContext,
		observeBinding: agentsession.ObserveGlobalCredentialBinding,
		restartPane:    robot.GetRestartPaneContext,
		lockProvider:   acquireFailoverProviderLock,
		publish: func(record robot.ActuationRecord) {
			robot.GetAttentionFeed().PublishActuation(record)
		},
		now:           time.Now,
		memLastSwitch: make(map[string]time.Time),
		lastPublished: make(map[string]declineMark),
	}
	fc.lastSwitchAt = fc.storedLastSwitch
	fc.claimSwitch = fc.storeLastSwitch
	fc.guardSwitch = fc.guardPaneSwitch
	fc.recoverPane = fc.recoverLimitedPane
	fc.readProviderState = fc.storedProviderState
	fc.writeProviderWatermark = fc.storeProviderWatermark
	return fc
}

// runtimeStore lazily opens the shared runtime store. nil means the store is
// unavailable; the in-memory cooldown map still bounds this process.
func (fc *failoverChecker) runtimeStore() *state.Store {
	fc.storeOnce.Do(func() {
		path, err := accountFailoverStatePath()
		if err != nil {
			return
		}
		store, err := state.Open(path)
		if err != nil {
			slog.Debug("caam failover: runtime store unavailable; global recovery is blocked",
				"session", fc.session, "error", err)
			return
		}
		// The watermark tables live in the runtime migrations; applying them
		// is idempotent and is what every other store consumer does on open.
		if err := store.Migrate(); err != nil {
			slog.Debug("caam failover: runtime store migration failed; global recovery is blocked",
				"session", fc.session, "error", err)
			_ = store.Close()
			return
		}
		fc.store = store
	})
	return fc.store
}

// storedLastSwitch reads the per-pane switch watermark, falling back to the
// in-memory map when the store is unavailable.
func (fc *failoverChecker) storedLastSwitch(scope string) (time.Time, bool) {
	if store := fc.runtimeStore(); store != nil {
		wm, err := store.GetWatermark(watermarkTypeCaamFailover, scope)
		if err == nil && wm != nil && wm.LastTs != nil {
			return *wm.LastTs, true
		}
		if err != nil {
			slog.Debug("caam failover: watermark read failed", "scope", scope, "error", err)
		}
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	at, ok := fc.memLastSwitch[scope]
	return at, ok
}

// storeLastSwitch claims the per-pane switch slot, reporting whether THIS
// process won it (and always recording in memory so cooldown survives a store
// outage within this process).
//
// A claim rather than a write because the cooldown gates an action, not a
// report. checkPane reads the watermark, then runs the remaining gates —
// including a real `caam list --json` subprocess with a multi-second budget —
// and only then records. Nothing prevents two `ntm coordinator run` processes
// from watching one session (no pid file, no lock, no DB singleton), so both
// could read "not recently switched", both pass every gate, and both rotate
// the same pane seconds apart, breaking the one-hour invariant this gate
// documents and potentially leaving the pane on an unexpected account.
//
// A false return means a peer claimed the slot first; the caller must decline
// instead of switching.
func (fc *failoverChecker) storeLastSwitch(scope, provider string, at time.Time, expected *time.Time) bool {
	fc.mu.Lock()
	if prev, ok := fc.memLastSwitch[scope]; ok && at.Sub(prev) < failoverSwitchCooldown {
		// Another goroutine in this process already claimed the slot.
		fc.mu.Unlock()
		return false
	}
	fc.memLastSwitch[scope] = at
	fc.mu.Unlock()

	store := fc.runtimeStore()
	if store == nil {
		// No shared store: the in-memory claim above is the only guard
		// available, and it is what this process had before.
		return true
	}
	ts := at.UTC()
	claimed, err := store.ClaimWatermark(&state.OutputWatermark{
		WatermarkType: watermarkTypeCaamFailover,
		Scope:         scope,
		LastTs:        &ts,
		Consumer:      provider,
		CreatedAt:     ts,
		UpdatedAt:     ts,
	}, expected)
	if err != nil {
		slog.Debug("caam failover: watermark claim failed", "scope", scope, "error", err)
		// The store could not answer. Fall back to the in-memory claim rather
		// than declining a legitimate failover on a storage hiccup.
		return true
	}
	return claimed
}

type accountProviderState struct {
	AttemptAt   time.Time
	ActivatedAt time.Time
	Account     string
}

type accountRecoveryRequest struct {
	Account        string
	Previous       string
	Reuse          bool
	ActivatedAt    time.Time
	Scope          string
	ObservedSwitch *time.Time
}

type accountRecoveryOutcome struct {
	ActivationAttempted   bool
	AccountActivated      bool
	ActivationUnknown     bool
	ConversationRecovered bool
	NativeSessionID       string
	PrevAccount           string
}

type accountRecoveryDecline struct{ reason string }

func (e *accountRecoveryDecline) Error() string { return e.reason }

func accountFailoverStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("global account recovery requires an absolute credential HOME")
	}
	// Selected config paths and XDG overrides cannot split ownership of the
	// same ~/.claude or ~/.codex credentials between independent monitors.
	return filepath.Join(home, ".config", "ntm", "state.db"), nil
}

func (fc *failoverChecker) storedProviderState(provider string) (accountProviderState, error) {
	var result accountProviderState
	store := fc.runtimeStore()
	if store == nil {
		return result, errors.New("provider cooldown store is unavailable")
	}
	for _, kind := range []string{watermarkTypeCaamProviderAttempt, watermarkTypeCaamProviderActivated} {
		wm, err := store.GetWatermark(kind, provider)
		if err != nil {
			return result, err
		}
		if wm == nil {
			continue
		}
		if wm.LastTs == nil || wm.LastTs.IsZero() || wm.Consumer == "" {
			return result, errors.New("provider recovery watermark is incomplete")
		}
		if kind == watermarkTypeCaamProviderAttempt {
			result.AttemptAt = *wm.LastTs
		} else {
			result.ActivatedAt, result.Account = *wm.LastTs, wm.Consumer
		}
	}
	return result, nil
}

func (fc *failoverChecker) storeProviderWatermark(kind, provider, account string, at time.Time) error {
	store := fc.runtimeStore()
	if store == nil {
		return errors.New("provider cooldown store is unavailable")
	}
	ts := at.UTC()
	return store.SetWatermark(&state.OutputWatermark{WatermarkType: kind, Scope: provider,
		LastTs: &ts, Consumer: account, CreatedAt: ts, UpdatedAt: ts})
}

// Inventory is usable only with one unambiguous active account. A duplicate
// account ID or multiple active flags must never select a global credential.
func verifiedFailoverAccounts(accounts []swarm.AccountInfo, required string, now time.Time) (*swarm.AccountInfo, *swarm.AccountInfo, error) {
	var active, alternate *swarm.AccountInfo
	seen := make(map[string]bool)
	for i := range accounts {
		account := &accounts[i]
		if strings.TrimSpace(account.AccountName) == "" || seen[account.AccountName] {
			return nil, nil, errors.New("active_account_unverified")
		}
		seen[account.AccountName] = true
		if account.IsActive {
			if active != nil {
				return nil, nil, errors.New("active_account_unverified")
			}
			active = account
		} else if !account.RateLimited && !account.CooldownUntil.After(now) &&
			(required == "" || account.AccountName == required) && alternate == nil {
			alternate = account
		}
	}
	if active == nil {
		return nil, nil, errors.New("active_account_unverified")
	}
	return active, alternate, nil
}

func selectAccountRecovery(accounts []swarm.AccountInfo, state accountProviderState, now time.Time) (accountRecoveryRequest, error) {
	var result accountRecoveryRequest
	active, alternate, err := verifiedFailoverAccounts(accounts, "", now)
	if err != nil {
		return result, err
	}
	result.Previous = active.AccountName
	if !state.AttemptAt.IsZero() && now.Sub(state.AttemptAt) < failoverSwitchCooldown {
		if state.Account != active.AccountName || state.ActivatedAt.IsZero() ||
			state.ActivatedAt.Before(state.AttemptAt) || state.ActivatedAt.After(now) ||
			active.RateLimited || active.CooldownUntil.After(now) {
			return result, errors.New("provider_cooldown")
		}
		result.Account, result.Reuse, result.ActivatedAt = state.Account, true, state.ActivatedAt
		return result, nil
	}
	if alternate == nil {
		return result, errors.New("no_alternate_account")
	}
	result.Account = alternate.AccountName
	return result, nil
}

func (fc *failoverChecker) guardPaneSwitch(ctx context.Context, pane tmux.Pane, provider string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := fc.paneCwd(ctx, pane.ID)
	if err != nil || !filepath.IsAbs(dir) {
		return errors.New("pane workspace is unavailable")
	}
	if target, ok := fc.targets[pane.ID]; ok && filepath.Clean(dir) != filepath.Clean(target.ProjectDir) {
		return errors.New("pane left its monitored project")
	}
	rotator := swarm.NewAccountRotator()
	// A new rotator on every check ensures removing a pin really removes it,
	// and malformed/unreadable policy never falls back to stale cached pins.
	if err := rotator.LoadPins(dir); err != nil {
		return err
	}
	rotator.ForceGlobalAuthClobber = fc.forceGlobal
	return rotator.GuardAutoSwitch(provider)
}

func sameRecoveryPane(expected, actual tmux.Pane) bool {
	return expected.ID != "" && expected.PID > 0 && expected.ID == actual.ID && expected.PID == actual.PID &&
		expected.Title == actual.Title && expected.Type.Canonical() == actual.Type.Canonical() &&
		expected.Index == actual.Index && expected.WindowIndex == actual.WindowIndex && expected.NTMIndex == actual.NTMIndex &&
		!actual.Dead && !actual.IsServicePane()
}

func (fc *failoverChecker) observeRecoveryPane(ctx context.Context, expected tmux.Pane, saved *tmux.AgentLaunchSpec, cwd string) (tmux.AgentLaunchSpec, string, error) {
	var empty tmux.AgentLaunchSpec
	panes, err := fc.getPanes(ctx, fc.session)
	if err != nil {
		return empty, "", err
	}
	found := false
	for _, actual := range panes {
		if actual.ID != expected.ID {
			continue
		}
		if !sameRecoveryPane(expected, actual) {
			return empty, "", errors.New("pane identity changed")
		}
		found = true
	}
	if !found {
		return empty, "", errors.New("pane is no longer in the monitored session")
	}
	spec, err := fc.readSpec(ctx, expected.ID)
	if err != nil {
		return empty, "", err
	}
	if spec == nil {
		return empty, "", errors.New("pane has no saved launch specification")
	}
	if err := spec.ValidateReplay(expected.Type); err != nil {
		return empty, "", err
	}
	if spec.CAAMProfile != "" || spec.ClaudeIsolateCredentials || spec.ClaudeTokenFile != "" || len(spec.OmittedEnv) != 0 {
		return empty, "", errors.New("pane does not use replayable global credentials")
	}
	if err := agentsession.ValidateGlobalCredentialLaunchCommand(string(expected.Type), spec.Command, agentsession.ResumeLaunchOptions{SystemPromptFile: spec.SystemPromptFile}); err != nil {
		return empty, "", err
	}
	if saved != nil && !reflect.DeepEqual(*saved, *spec) {
		return empty, "", errors.New("pane launch specification changed")
	}
	dir, err := fc.paneCwd(ctx, expected.ID)
	if err != nil || !filepath.IsAbs(dir) || (cwd != "" && cwd != dir) {
		return empty, "", errors.New("pane workspace changed or is unavailable")
	}
	if target, ok := fc.targets[expected.ID]; ok && filepath.Clean(dir) != filepath.Clean(target.ProjectDir) {
		return empty, "", errors.New("pane left its monitored project")
	}
	captured, err := fc.capturePane(ctx, expected.ID, failoverCaptureLines)
	if err != nil {
		return empty, "", err
	}
	detection := ratelimit.DetectRateLimitForAgent(captured, string(expected.Type.Canonical()))
	if !detection.RateLimited || agentWorking(expected.Type.Canonical(), captured, expected.Width) {
		return empty, "", errors.New("pane is no longer stalled at a rate limit")
	}
	if beyond, _ := resetBeyondHorizon(detection, fc.now(), fc.horizon); !beyond {
		return empty, "", errors.New("pane rate limit now resets within the wait horizon")
	}
	if err := fc.guardSwitch(ctx, expected, providerForAgentType(expected.Type.Canonical())); err != nil {
		return empty, "", err
	}
	if err := ctx.Err(); err != nil {
		return empty, "", err
	}
	return *spec, dir, nil
}

func sameRecoveryBinding(a, b agentsession.GlobalCredentialBinding) bool {
	return a.ProcessPID > 0 && a.ProcessPID == b.ProcessPID && a.ProcessStartedAt > 0 && a.ProcessStartedAt == b.ProcessStartedAt &&
		a.CredentialHome != "" && a.CredentialHome == b.CredentialHome && a.Session.SessionID != "" && a.Session.SessionID == b.Session.SessionID &&
		a.Session.SourcePath != "" && a.Session.SourcePath == b.Session.SourcePath
}

// recoverLimitedPane runs while the caller owns the provider lock. Its typed
// restart callback is reached only after the ordinary replay preflight, and
// rechecks live conversation/account/policy evidence immediately before the
// activation. The same robot executor then owns the physical respawn.
func (fc *failoverChecker) recoverLimitedPane(ctx context.Context, pane tmux.Pane, provider string, request accountRecoveryRequest) (accountRecoveryOutcome, error) {
	out := accountRecoveryOutcome{PrevAccount: request.Previous}
	spec, dir, err := fc.observeRecoveryPane(ctx, pane, nil, "")
	if err != nil {
		return out, &accountRecoveryDecline{reason: "pane_recovery_unverified"}
	}
	before, err := fc.observeBinding(ctx, string(pane.Type), dir, pane.PID)
	if err != nil {
		return out, &accountRecoveryDecline{reason: "native_binding_unverified"}
	}
	if !sameRecoveryBinding(before, before) {
		return out, &accountRecoveryDecline{reason: "native_binding_unverified"}
	}
	out.NativeSessionID = before.Session.SessionID
	if request.Reuse && before.ProcessStartedAt >= request.ActivatedAt.UnixMilli() {
		return out, &accountRecoveryDecline{reason: "pane_already_uses_current_account"}
	}
	command, err := agentsession.ResumeLaunchCommand(agentsession.ResumeProvider(string(pane.Type)), out.NativeSessionID, spec.Command, agentsession.ResumeLaunchOptions{SystemPromptFile: spec.SystemPromptFile})
	if err != nil {
		return out, &accountRecoveryDecline{reason: "native_resume_unsupported"}
	}
	resumedSpec := spec
	resumedSpec.Command = command
	checkBefore := func(checkCtx context.Context) error {
		binding, err := fc.observeBinding(checkCtx, string(pane.Type), dir, pane.PID)
		if err != nil || !sameRecoveryBinding(before, binding) {
			return errors.New("native conversation changed before recovery")
		}
		_, _, err = fc.observeRecoveryPane(checkCtx, pane, &spec, dir)
		return err
	}
	requestRecovery := &robot.NativeSessionRecovery{ExpectedPane: pane, ExpectedSpec: spec,
		WorkingDir: dir, NativeSessionID: out.NativeSessionID}
	claimed := false
	claim := func() error {
		if !claimed {
			if !fc.claimSwitch(request.Scope, provider, fc.now(), request.ObservedSwitch) {
				return &accountRecoveryDecline{reason: "cooldown"}
			}
			claimed = true
		}
		return nil
	}
	requestRecovery.BeforeRespawn = func(actionCtx context.Context) error {
		if err := checkBefore(actionCtx); err != nil {
			return err
		}
		accounts, err := fc.listAccounts(actionCtx, provider)
		if err != nil {
			return err
		}
		active, alternate, err := verifiedFailoverAccounts(accounts, request.Account, fc.now())
		if err != nil {
			return err
		}
		if request.Reuse {
			state, err := fc.readProviderState(provider)
			if err != nil || state.Account != request.Account || !state.ActivatedAt.Equal(request.ActivatedAt) || state.ActivatedAt.Before(state.AttemptAt) ||
				active.AccountName != request.Account || active.RateLimited || active.CooldownUntil.After(fc.now()) {
				return errors.New("active account no longer matches the successful activation receipt")
			}
		} else if active.AccountName != request.Previous || alternate == nil {
			return errors.New("account inventory changed before activation")
		}
		// Inventory queries can take seconds. Recheck the physical generation,
		// saved command, workspace, policy and stalled state after they finish.
		if err := checkBefore(actionCtx); err != nil {
			return err
		}
		if !request.Reuse {
			_, activationErr := fc.activateAccount(actionCtx, provider, request.Account, func(finalCtx context.Context, current *swarm.AccountInfo) error {
				if current == nil || current.AccountName != request.Previous {
					return errors.New("active account changed at the activation boundary")
				}
				if err := checkBefore(finalCtx); err != nil {
					return err
				}
				if err := claim(); err != nil {
					return err
				}
				if err := fc.writeProviderWatermark(watermarkTypeCaamProviderAttempt, provider, request.Account, fc.now()); err != nil {
					return err
				}
				if err := finalCtx.Err(); err != nil {
					return err
				}
				out.ActivationAttempted = true
				return nil
			})
			if !out.ActivationAttempted {
				if activationErr == nil {
					activationErr = errors.New("account executor did not confirm the activation boundary")
				}
				return activationErr
			}
			// Cancellation may have interrupted an activation after its side
			// effect. Report uncertainty; never retry or write a success receipt.
			if actionCtx.Err() != nil {
				out.ActivationUnknown = true
				return actionCtx.Err()
			}
			accounts, queryErr := fc.listAccounts(actionCtx, provider)
			if queryErr != nil {
				out.ActivationUnknown = true
				return queryErr
			}
			current, _, queryErr := verifiedFailoverAccounts(accounts, "", fc.now())
			if queryErr != nil {
				out.ActivationUnknown = true
				return queryErr
			}
			out.AccountActivated = current.AccountName == request.Account
			if activationErr != nil {
				return activationErr
			}
			if !out.AccountActivated {
				return errors.New("CAAM activation did not select the requested account")
			}
			if err := fc.writeProviderWatermark(watermarkTypeCaamProviderActivated, provider, request.Account, fc.now()); err != nil {
				return err
			}
		}
		if err := checkBefore(actionCtx); err != nil {
			return err
		}
		if request.Reuse {
			return claim()
		}
		return nil
	}
	restarted, err := fc.restartPane(ctx, robot.RestartPaneOptions{Session: fc.session, Panes: []string{pane.ID},
		Config: fc.config, ProjectDir: dir, Recovery: requestRecovery})
	if err != nil {
		return out, err
	}
	if restarted == nil || !restarted.Success || len(restarted.Restarted) != 1 || len(restarted.Failed) != 0 {
		return out, errors.New("native pane restart did not complete")
	}
	key := restarted.Restarted[0]
	pids := restarted.PaneShellPIDs[key]
	if !restarted.AgentRelaunched[key] || pids.Before != pane.PID || pids.After <= 0 || pids.After == pane.PID {
		return out, errors.New("native pane restart has no verified ready replacement process")
	}
	// Startup readiness can precede opening the transcript. Wait a bounded
	// interval for independent native-session proof, retaining the provider
	// lock so a sibling cannot activate another account during verification.
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		panes, err := fc.getPanes(verifyCtx, fc.session)
		if err != nil {
			return out, err
		}
		var current tmux.Pane
		for _, candidate := range panes {
			if candidate.ID == pane.ID {
				current = candidate
				break
			}
		}
		expected := pane
		expected.PID = pids.After
		if !sameRecoveryPane(expected, current) {
			return out, errors.New("replacement pane identity changed")
		}
		liveSpec, err := fc.readSpec(verifyCtx, pane.ID)
		if err != nil || liveSpec == nil || !reflect.DeepEqual(*liveSpec, resumedSpec) {
			return out, errors.New("replacement launch settings changed")
		}
		liveDir, err := fc.paneCwd(verifyCtx, pane.ID)
		if err != nil || liveDir != dir {
			return out, errors.New("replacement workspace changed")
		}
		after, bindingErr := fc.observeBinding(verifyCtx, string(pane.Type), dir, pids.After)
		if bindingErr == nil && after.ProcessPID > 0 && after.ProcessPID != before.ProcessPID && after.ProcessStartedAt > before.ProcessStartedAt &&
			after.CredentialHome == before.CredentialHome && after.Session.SessionID == before.Session.SessionID &&
			after.Session.SourcePath == before.Session.SourcePath {
			accounts, err := fc.listAccounts(verifyCtx, provider)
			if err != nil {
				return out, err
			}
			active, _, err := verifiedFailoverAccounts(accounts, "", fc.now())
			if err != nil || active.AccountName != request.Account {
				return out, errors.New("active account changed while confirming recovery")
			}
			finalSpec, err := fc.readSpec(verifyCtx, pane.ID)
			if err != nil || finalSpec == nil || !reflect.DeepEqual(*finalSpec, resumedSpec) {
				return out, errors.New("replacement launch settings changed during confirmation")
			}
			finalDir, err := fc.paneCwd(verifyCtx, pane.ID)
			if err != nil || finalDir != dir {
				return out, errors.New("replacement workspace changed during confirmation")
			}
			finalBinding, err := fc.observeBinding(verifyCtx, string(pane.Type), dir, pids.After)
			if err != nil || !sameRecoveryBinding(after, finalBinding) {
				return out, errors.New("replacement native conversation changed during account confirmation")
			}
			finalPanes, err := fc.getPanes(verifyCtx, fc.session)
			if err != nil {
				return out, err
			}
			confirmed := false
			for _, final := range finalPanes {
				if sameRecoveryPane(expected, final) {
					confirmed = true
					break
				}
			}
			if !confirmed {
				return out, errors.New("replacement pane changed during account confirmation")
			}
			out.ConversationRecovered = true
			return out, nil
		}
		select {
		case <-verifyCtx.Done():
			return out, errors.New("replacement native conversation could not be confirmed")
		case <-ticker.C:
		}
	}
}

// runOnce executes one failover check pass and returns the decisions made.
// Panes without a detected rate limit produce no decision at all.
func (fc *failoverChecker) runOnce(ctx context.Context) []failoverDecision {
	if fc == nil {
		return nil
	}
	if ctx == nil || ctx.Err() != nil {
		return nil
	}

	panes, err := fc.getPanes(ctx, fc.session)
	if err != nil {
		slog.Warn("caam failover check could not list panes",
			"session", fc.session, "error", err)
		return nil
	}

	// Deterministic order for logs and tests.
	ordered := append([]tmux.Pane(nil), panes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	var decisions []failoverDecision
	for _, pane := range ordered {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if fc.targets != nil {
			target, selected := fc.targets[pane.ID]
			if !selected || agent.AgentType(target.AgentType).Canonical() != pane.Type.Canonical() {
				continue
			}
		}
		if decision, acted := fc.checkPane(ctx, pane); acted {
			decisions = append(decisions, decision)
		}
	}
	return decisions
}

// checkPane detects a rate limit on one pane's fresh capture and, when
// detected, runs the mandatory safety gates and executes/declines the
// failover. The bool result reports whether a decision was made.
//
// Gate order (documented invariant, mirrored by the tests): detection →
// allow-list → working → cooldown → reset horizon → caam availability →
// rotation safety guard → verified alternate → switch.
func (fc *failoverChecker) checkPane(ctx context.Context, pane tmux.Pane) (failoverDecision, bool) {
	now := fc.now()
	canonical := pane.Type.Canonical()

	// A pane we cannot observe is a pane whose rate-limit state is unknown:
	// nothing to decide (detection never happened).
	if pane.IsServicePane() || pane.Dead {
		return failoverDecision{}, false
	}
	captured, err := fc.capturePane(ctx, pane.ID, failoverCaptureLines)
	if err != nil {
		slog.Debug("caam failover: capture failed; pane skipped",
			"session", fc.session, "pane", pane.ID, "error", err)
		return failoverDecision{}, false
	}

	det := ratelimit.DetectRateLimitForAgent(captured, string(canonical))
	if !det.RateLimited {
		return failoverDecision{}, false
	}

	agentID := strings.TrimSpace(pane.Title)
	decision := failoverDecision{
		PaneID:      pane.ID,
		AgentID:     agentID,
		Provider:    providerForAgentType(canonical),
		ResetHint:   det.ResetHint,
		WaitSeconds: det.WaitSeconds,
		Banner:      matchedBannerLine(captured, string(canonical)),
	}

	// Gate: only agent types with BOTH a provider mapping and a working
	// detector are supported; anything else is refused outright (fail
	// closed) because the "never while working" gate cannot be verified.
	if decision.Provider == "" || !hasWorkingDetector(canonical) {
		return fc.decline(decision, "unsupported_agent_type", true), true
	}

	// Gate: provider allow-list (the second opt-in).
	if !fc.providers[decision.Provider] {
		return fc.decline(decision, "provider_not_allowlisted", true), true
	}

	// Gate: never while the pane is working (agent working detectors on the
	// same fresh capture the detection ran on).
	if agentWorking(canonical, captured, pane.Width) {
		return fc.decline(decision, "working", true), true
	}

	// Gate: never within failoverSwitchCooldown of the last auto-switch
	// attempt for this pane.
	scope := fc.cooldownScope(pane)
	// observedSwitch is what the cooldown decision below is made from; the
	// claim at fire time is guarded on it still being current, because the
	// gates in between do real work (a caam subprocess) and a peer coordinator
	// can take the slot during that window.
	var observedSwitch *time.Time
	if last, ok := fc.lastSwitchAt(scope); ok {
		if now.Sub(last) < failoverSwitchCooldown {
			return fc.decline(decision, "cooldown", true), true
		}
		observed := last.UTC()
		observedSwitch = &observed
	}

	// Gate: only fail over when the detected reset is further away than the
	// horizon. An unparseable (or absent) reset hint counts as beyond.
	if beyond, detail := resetBeyondHorizon(det, now, fc.horizon); !beyond {
		return fc.decline(decision, "reset_within_horizon:"+detail, true), true
	}

	// Gate: when caam is unavailable the feature degrades SILENTLY to off —
	// debug log only, no attention-feed record.
	if !fc.caamAvailable() {
		slog.Debug("caam failover: caam unavailable; degrading to off",
			"session", fc.session, "pane", pane.ID, "agent", agentID,
			"provider", decision.Provider)
		return fc.decline(decision, "caam_unavailable", false), true
	}

	// Gate: the automatic-rotation safety guard — the same guardrails
	// swarm's OnLimitHit rotation passes (operator pins from `ntm rotate
	// lock`; refusal of an unattended global Codex auth clobber without
	// proven pane isolation and caam safe-restore). An unattended
	// coordinator switch must never bypass it.
	if fc.guardSwitch != nil {
		if err := fc.guardSwitch(ctx, pane, decision.Provider); err != nil {
			slog.Info("caam failover blocked by rotation safety guard",
				"session", fc.session, "pane", pane.ID, "agent", agentID,
				"provider", decision.Provider, "error", err)
			return fc.decline(decision, "rotation_blocked", true), true
		}
	}

	// Global credentials are shared by panes in every session. Keep selection,
	// activation, native restart, and confirmation under one provider lock.
	unlock, err := fc.lockProvider(ctx, decision.Provider)
	if err != nil {
		return fc.decline(decision, "provider_lock_unavailable", true), true
	}
	defer unlock()
	providerState, err := fc.readProviderState(decision.Provider)
	if err != nil {
		return fc.decline(decision, "provider_state_unavailable", true), true
	}
	accounts, err := fc.listAccounts(ctx, decision.Provider)
	if err != nil {
		return fc.decline(decision, "caam_query_failed", true), true
	}
	now = fc.now()
	request, err := selectAccountRecovery(accounts, providerState, now)
	if err != nil {
		return fc.decline(decision, err.Error(), true), true
	}
	decision.ChosenAccount = request.Account

	// The final eligible actuation boundary claims the per-pane cooldown.
	// A temporarily unopened transcript or failed replay preflight can then
	// be observed again next tick without suppressing recovery for an hour.
	request.Scope, request.ObservedSwitch = scope, observedSwitch

	out, err := fc.recoverPane(ctx, pane, decision.Provider, request)
	decision.AccountActivationAttempted = out.ActivationAttempted
	decision.AccountActivated = out.AccountActivated
	decision.AccountActivationUnknown = out.ActivationUnknown
	decision.ConversationRecovered = out.ConversationRecovered
	decision.NativeSessionID = out.NativeSessionID
	decision.PrevAccount = out.PrevAccount
	var declined *accountRecoveryDecline
	if errors.As(err, &declined) && !out.ActivationAttempted && !out.AccountActivated {
		return fc.decline(decision, declined.reason, true), true
	}
	success := err == nil && out.ConversationRecovered
	if success {
		decision.Action = "recovered"
		slog.Info("caam failover recovered native conversation",
			"session", fc.session, "pane", pane.ID, "agent", agentID,
			"provider", decision.Provider,
			"previous_account", decision.PrevAccount,
			"new_account", request.Account,
			"banner", decision.Banner, "reset_hint", decision.ResetHint)
	} else {
		decision.Action = "recovery_failed"
		if out.ActivationUnknown {
			decision.Action = "activation_unknown"
		}
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		decision.Error = errText
		slog.Warn("caam failover recovery failed",
			"session", fc.session, "pane", pane.ID, "agent", agentID,
			"provider", decision.Provider, "account", request.Account, "error", errText,
			"account_activated", out.AccountActivated, "activation_unknown", out.ActivationUnknown)
	}

	severity := robot.SeverityInfo
	resultWord := "completed"
	if !success {
		severity = robot.SeverityWarning
		resultWord = "failed"
	}
	fc.publishDecision(robot.ActuationRecord{
		Stage:          robot.ActuationStageOutcome,
		Targets:        []string{fc.declineTarget(decision)},
		Summary:        fmt.Sprintf("caam auto-failover %s for %s: provider %s -> account %s; account_activated=%t conversation_recovered=%t", resultWord, fc.declineTarget(decision), decision.Provider, request.Account, out.AccountActivated, out.ConversationRecovered),
		ReasonCode:     "caam_failover_recovery",
		MessagePreview: failoverEvidence(decision),
		Result:         resultWord,
		Severity:       severity,
	})
	return decision, true
}

// decline finalizes a declined decision: always logged, and published to the
// attention feed unless silent (caam unavailable degrades silently) or the
// identical reason was already published for this pane within
// failoverRepublishInterval.
func (fc *failoverChecker) decline(decision failoverDecision, reason string, publish bool) failoverDecision {
	decision.Action = "declined"
	decision.DeclineReason = reason
	slog.Info("caam failover declined",
		"session", fc.session, "pane", decision.PaneID, "agent", decision.AgentID,
		"provider", decision.Provider, "reason", reason,
		"banner", decision.Banner, "reset_hint", decision.ResetHint,
		"wait_seconds", decision.WaitSeconds)
	if !publish || !fc.shouldPublishDecline(decision.PaneID, reason) {
		return decision
	}
	fc.publishDecision(robot.ActuationRecord{
		Stage:          robot.ActuationStageOutcome,
		Targets:        []string{fc.declineTarget(decision)},
		Summary:        fmt.Sprintf("caam auto-failover for %s declined: %s", fc.declineTarget(decision), reason),
		ReasonCode:     "caam_failover_declined",
		MessagePreview: failoverEvidence(decision),
		Blocked:        true,
		Severity:       robot.SeverityInfo,
	})
	return decision
}

// shouldPublishDecline suppresses attention-feed spam: an unchanged decline
// reason for a pane is re-published only after failoverRepublishInterval.
func (fc *failoverChecker) shouldPublishDecline(paneID, reason string) bool {
	now := fc.now()
	fc.mu.Lock()
	defer fc.mu.Unlock()
	mark, ok := fc.lastPublished[paneID]
	if ok && mark.reason == reason && now.Sub(mark.at) < failoverRepublishInterval {
		return false
	}
	fc.lastPublished[paneID] = declineMark{reason: reason, at: now}
	return true
}

// declineTarget picks the attention-feed target: agent ID when titled, pane
// ID otherwise.
func (fc *failoverChecker) declineTarget(decision failoverDecision) string {
	if decision.AgentID != "" {
		return decision.AgentID
	}
	return decision.PaneID
}

// cooldownScope is the watermark scope for a pane's switch cooldown.
func (fc *failoverChecker) cooldownScope(pane tmux.Pane) string {
	id := strings.TrimSpace(pane.Title)
	if id == "" {
		id = pane.ID
	}
	return fc.session + ":" + id
}

// publishDecision fills the shared actuation-record fields and publishes to
// the attention feed.
func (fc *failoverChecker) publishDecision(record robot.ActuationRecord) {
	if fc.publish == nil {
		return
	}
	record.Session = fc.session
	record.Action = "caam_failover"
	record.Source = "coordinator.caam_failover"
	record.Method = "rate_limit_banner"
	record.Actionability = robot.ActionabilityInteresting
	fc.publish(record)
}

// failoverEvidence renders the decision evidence (provider, matched banner,
// reset hint, chosen account) for attention-feed consumers.
func failoverEvidence(d failoverDecision) string {
	return fmt.Sprintf("provider=%s banner=%q reset_hint=%q wait_s=%d account=%s",
		d.Provider, d.Banner, d.ResetHint, d.WaitSeconds, d.ChosenAccount)
}

// providerForAgentType maps a canonical agent type to its caam provider.
// Empty means the type has no provider mapping and can never fail over.
func providerForAgentType(t agent.AgentType) string {
	switch t {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "openai"
	case agent.AgentTypeGemini:
		return "gemini"
	default:
		return ""
	}
}

// canonicalFailoverProvider normalizes an allow-list entry to a caam provider
// name; unrecognized entries yield "" and never match.
func canonicalFailoverProvider(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "claude", "anthropic", "cc":
		return "claude"
	case "openai", "codex", "cod", "gpt":
		return "openai"
	case "gemini", "gmi", "google":
		return "gemini"
	default:
		return ""
	}
}

// hasWorkingDetector reports whether the "never while working" gate can be
// verified for this agent type. Only Claude Code and Codex have working
// detectors today; every other type fails closed.
func hasWorkingDetector(t agent.AgentType) bool {
	return t == agent.AgentTypeClaudeCode || t == agent.AgentTypeCodex
}

// agentWorking applies the per-type agent working detector.
func agentWorking(t agent.AgentType, captured string, paneWidth int) bool {
	switch t {
	case agent.AgentTypeClaudeCode:
		return agent.ClaudeActivelyWorking(captured, paneWidth)
	case agent.AgentTypeCodex:
		return agent.CodexActivelyWorking(captured, paneWidth)
	default:
		// Unreachable behind hasWorkingDetector; refuse anyway.
		return true
	}
}

// matchedBannerLine returns the first capture line that alone trips the
// rate-limit detector — best-effort evidence of WHICH banner fired. Multi-line
// banners may yield "" (the whole-capture detection still stands).
func matchedBannerLine(captured, agentType string) string {
	for _, line := range strings.Split(captured, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if ratelimit.DetectRateLimitForAgent(trimmed, agentType).RateLimited {
			if len(trimmed) > 120 {
				cut := trimmed[:120]
				// Never split a multi-byte rune: published evidence must stay
				// valid UTF-8.
				for len(cut) > 0 && !utf8.ValidString(cut) {
					cut = cut[:len(cut)-1]
				}
				trimmed = strings.TrimSpace(cut)
			}
			return trimmed
		}
	}
	return ""
}

// failoverClockPatterns parse a clock time out of a human reset hint, e.g.
// "try again at 7:00 PM", "resets at 3am (America/New_York)", "resets 19:30".
var failoverClockPatterns = []*regexp.Regexp{
	// 12-hour: "7 PM", "7:00pm", "3 a.m."
	regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*([ap])\.?m\.?\b`),
	// 24-hour: "19:30"
	regexp.MustCompile(`\b(\d{1,2}):(\d{2})\b`),
}

// parseResetClock extracts a wall-clock reset time from a human hint and
// returns the NEXT occurrence of that clock time after now (in now's
// location). ok=false when no clock time can be parsed.
func parseResetClock(hint string, now time.Time) (time.Time, bool) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return time.Time{}, false
	}
	for i, pat := range failoverClockPatterns {
		m := pat.FindStringSubmatch(hint)
		if m == nil {
			continue
		}
		hour, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		minute := 0
		if m[2] != "" {
			if minute, err = strconv.Atoi(m[2]); err != nil {
				continue
			}
		}
		if i == 0 { // 12-hour pattern
			if hour < 1 || hour > 12 {
				continue
			}
			hour = hour % 12
			if strings.EqualFold(m[3], "p") {
				hour += 12
			}
		} else if hour > 23 {
			continue
		}
		if minute > 59 {
			continue
		}
		candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !candidate.After(now) {
			candidate = candidate.Add(24 * time.Hour)
		}
		return candidate, true
	}
	return time.Time{}, false
}

// resetBeyondHorizon decides whether the detected reset lies beyond the
// configured horizon. Detail is a machine-readable explanation for decline
// reasons and logs.
//
// Documented rule: a reset hint that cannot be parsed (or a detection with no
// reset information at all) is treated as BEYOND the horizon — the failover
// may proceed. Long-lived usage limits are the common case for unparseable
// phrasing, and the remaining gates (verified alternate, per-pane hourly
// cooldown) bound the cost of a wrong guess.
func resetBeyondHorizon(det ratelimit.RateLimitDetection, now time.Time, horizon time.Duration) (bool, string) {
	if horizon <= 0 {
		return true, "horizon_disabled"
	}
	if det.WaitSeconds > 0 {
		wait := time.Duration(det.WaitSeconds) * time.Second
		return wait > horizon, fmt.Sprintf("wait_%ds", det.WaitSeconds)
	}
	if at, ok := parseResetClock(det.ResetHint, now); ok {
		return at.Sub(now) > horizon, "reset_at_" + at.Format("15:04")
	}
	return true, "unparseable_reset_hint"
}
