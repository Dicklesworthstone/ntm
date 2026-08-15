// Package coordinator: caam_failover.go wires automatic CAAM account failover
// into the coordinator tick (bd-um3uy), mirroring rotation.go's structure.
//
// When [integrations.caam] auto_failover = true AND failover_providers is
// non-empty (the feature is doubly opt-in), every coordinator cycle inspects
// each agent pane's fresh capture with the same detector is-working uses
// (ratelimit.DetectRateLimitForAgent). A pane that shows a banner-verified
// rate limit for an allow-listed provider, whose detected reset lies beyond
// [integrations.caam] reset_horizon_minutes, triggers an account switch
// through the same machinery --robot-switch-account uses — but ONLY after a
// verified alternate caam account with headroom is found.
//
// Safety gates, all mandatory and checked at fire time on a fresh capture:
//
//  1. provider allow-list      — only providers in failover_providers
//  2. not working              — the agent working detectors (Claude/Codex);
//     agent types without a working detector are refused outright (fail
//     closed, like rotation.go's unsupported_agent_type)
//  3. per-pane switch cooldown — never within 1 hour of the last auto-switch
//     for that pane, persisted in the runtime store as a 'caam_failover'
//     watermark (disk_sample/output_seq precedent); switch ATTEMPTS start
//     the cooldown too, so a failing caam is never hammered every tick
//  4. reset horizon            — limits that reset within the horizon are
//     waited out; an UNPARSEABLE reset hint is treated as beyond the horizon
//     (long-lived limits are the common case for odd phrasing, and gates
//     1-3 plus the verified-alternate check bound the blast radius)
//  5. caam availability        — when caam is not installed/responsive the
//     checker degrades SILENTLY to off (debug log only, no attention noise)
//  6. verified alternate       — caam is queried (swarm's `caam list --json`
//     fail-closed parse path) and the switch fires only when a non-active,
//     non-rate-limited account exists
//
// Every decision — switch, switch failure, and every decline with its reason
// — is logged and published to the attention feed with evidence (provider,
// matched banner, reset hint, chosen account). Identical consecutive decline
// reasons for a pane are re-published only every failoverRepublishInterval so
// a pane that stays rate-limited for an hour does not flood the feed on every
// 5-second tick (each decision is still slog-logged).
package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
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

// failoverDecision records one failover decision for logging and tests.
type failoverDecision struct {
	PaneID        string // tmux pane ID (%N)
	AgentID       string // canonical agent ID (pane title, e.g. sess__cc_1)
	Provider      string // caam provider ("claude", "openai", "gemini")
	Action        string // "switched", "switch_failed", "declined"
	DeclineReason string // populated when Action == "declined"
	Banner        string // evidence: matched rate-limit banner line (best-effort)
	ResetHint     string // evidence: human-readable reset phrase, if any
	WaitSeconds   int    // evidence: parsed wait seconds, if any
	ChosenAccount string // evidence: alternate account selected (switch attempts)
	PrevAccount   string // evidence: account switched away from (successful switches)
}

// failoverChecker performs the per-tick rate-limit failover check. All
// collaborators are injectable for tests; production wiring is installed by
// newFailoverChecker.
type failoverChecker struct {
	session   string
	horizon   time.Duration
	providers map[string]bool // canonical caam provider allow-list

	// Seams (default to real implementations).
	getPanes      func(session string) ([]tmux.Pane, error)
	capturePane   func(paneID string, lines int) (string, error)
	caamAvailable func() bool
	listAccounts  func(provider string) ([]swarm.AccountInfo, error)
	switchAccount func(provider, accountID string) (*robot.SwitchAccountOutput, error)
	lastSwitchAt  func(scope string) (time.Time, bool)
	recordSwitch  func(scope, provider string, at time.Time)
	publish       func(record robot.ActuationRecord)
	now           func() time.Time

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
func newFailoverChecker(session string, caamCfg config.CAAMConfig) *failoverChecker {
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

	// Reuse swarm's caam machinery: availability probing and the fail-closed
	// `caam list --json` account query (ntm-9mt8.2 heritage).
	rotator := swarm.NewAccountRotator()
	if strings.TrimSpace(caamCfg.BinaryPath) != "" {
		rotator = rotator.WithCaamPath(caamCfg.BinaryPath)
	}

	fc := &failoverChecker{
		session:       session,
		horizon:       horizon,
		providers:     providers,
		getPanes:      tmux.GetPanes,
		capturePane:   tmux.CapturePaneOutput,
		caamAvailable: rotator.IsAvailable,
		listAccounts:  rotator.ListAvailableAccounts,
		switchAccount: func(provider, accountID string) (*robot.SwitchAccountOutput, error) {
			// The exact machinery --robot-switch-account uses.
			return robot.GetSwitchAccount(robot.SwitchAccountOptions{
				Provider:  provider,
				AccountID: accountID,
			})
		},
		publish: func(record robot.ActuationRecord) {
			robot.GetAttentionFeed().PublishActuation(record)
		},
		now:           time.Now,
		memLastSwitch: make(map[string]time.Time),
		lastPublished: make(map[string]declineMark),
	}
	fc.lastSwitchAt = fc.storedLastSwitch
	fc.recordSwitch = fc.storeLastSwitch
	return fc
}

// runtimeStore lazily opens the shared runtime store. nil means the store is
// unavailable; the in-memory cooldown map still bounds this process.
func (fc *failoverChecker) runtimeStore() *state.Store {
	fc.storeOnce.Do(func() {
		store, err := state.Open("")
		if err != nil {
			slog.Debug("caam failover: runtime store unavailable; cooldown is process-local",
				"session", fc.session, "error", err)
			return
		}
		// The watermark tables live in the runtime migrations; applying them
		// is idempotent and is what every other store consumer does on open.
		if err := store.Migrate(); err != nil {
			slog.Debug("caam failover: runtime store migration failed; cooldown is process-local",
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

// storeLastSwitch persists the per-pane switch watermark (and always records
// it in memory so cooldown survives a store outage within this process).
func (fc *failoverChecker) storeLastSwitch(scope, provider string, at time.Time) {
	fc.mu.Lock()
	fc.memLastSwitch[scope] = at
	fc.mu.Unlock()

	store := fc.runtimeStore()
	if store == nil {
		return
	}
	ts := at.UTC()
	if err := store.SetWatermark(&state.OutputWatermark{
		WatermarkType: watermarkTypeCaamFailover,
		Scope:         scope,
		LastTs:        &ts,
		Consumer:      provider,
		CreatedAt:     ts,
		UpdatedAt:     ts,
	}); err != nil {
		slog.Debug("caam failover: watermark write failed", "scope", scope, "error", err)
	}
}

// runOnce executes one failover check pass and returns the decisions made.
// Panes without a detected rate limit produce no decision at all.
func (fc *failoverChecker) runOnce(ctx context.Context) []failoverDecision {
	if fc == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return nil
	}

	panes, err := fc.getPanes(fc.session)
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
		if decision, acted := fc.checkPane(pane); acted {
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
// verified alternate → switch.
func (fc *failoverChecker) checkPane(pane tmux.Pane) (failoverDecision, bool) {
	now := fc.now()
	canonical := pane.Type.Canonical()

	// A pane we cannot observe is a pane whose rate-limit state is unknown:
	// nothing to decide (detection never happened).
	captured, err := fc.capturePane(pane.ID, failoverCaptureLines)
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
	if last, ok := fc.lastSwitchAt(scope); ok && now.Sub(last) < failoverSwitchCooldown {
		return fc.decline(decision, "cooldown", true), true
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

	// Gate: NEVER switch without verifying an alternate exists. Reuses
	// swarm's fail-closed `caam list --json` query; a query error is a
	// decline, not a best-effort switch.
	accounts, err := fc.listAccounts(decision.Provider)
	if err != nil {
		return fc.decline(decision, "caam_query_failed", true), true
	}
	alternate := ""
	for _, acc := range accounts {
		// ListAvailableAccounts already excludes rate-limited/cooldown
		// accounts; also exclude the active account (switching to itself is
		// not a failover) and respect any still-running cooldown timestamp.
		if acc.IsActive || acc.RateLimited {
			continue
		}
		if !acc.CooldownUntil.IsZero() && acc.CooldownUntil.After(now) {
			continue
		}
		alternate = acc.AccountName
		break
	}
	if alternate == "" {
		return fc.decline(decision, "no_alternate_account", true), true
	}
	decision.ChosenAccount = alternate

	// Fire. Record the attempt FIRST so even a failing switch starts the
	// per-pane cooldown (a broken caam must not be retried every tick).
	fc.recordSwitch(scope, decision.Provider, now)

	out, err := fc.switchAccount(decision.Provider, alternate)
	success := err == nil && out != nil && out.Switch.Success
	if success {
		decision.Action = "switched"
		decision.PrevAccount = out.Switch.PreviousAccount
		slog.Info("caam failover switched account",
			"session", fc.session, "pane", pane.ID, "agent", agentID,
			"provider", decision.Provider,
			"previous_account", decision.PrevAccount,
			"new_account", out.Switch.NewAccount,
			"banner", decision.Banner, "reset_hint", decision.ResetHint)
	} else {
		decision.Action = "switch_failed"
		errText := ""
		if err != nil {
			errText = err.Error()
		} else if out != nil {
			errText = out.Switch.Error
		}
		slog.Warn("caam failover switch failed",
			"session", fc.session, "pane", pane.ID, "agent", agentID,
			"provider", decision.Provider, "account", alternate, "error", errText)
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
		Summary:        fmt.Sprintf("caam auto-failover %s for %s: provider %s -> account %s", resultWord, fc.declineTarget(decision), decision.Provider, alternate),
		ReasonCode:     "caam_failover_switch",
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
				trimmed = strings.TrimSpace(trimmed[:120])
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
