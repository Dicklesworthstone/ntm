package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CAAMRankEarliestResetHeadroom is caam's rank mode for handing a seat to NEW
// work: among seats whose included allowance is still under the headroom
// ceiling it prefers the one that refreshes SOONEST, so quota about to be lost
// is spent first and a later-resetting seat is preserved.
//
// It is deliberately NOT `--best`. `--best` ranks lowest utilization, which on
// a pool of interchangeable seats picks the idle reserve — the opposite of the
// policy (caam#105, ntm#319). Do not swap these.
const CAAMRankEarliestResetHeadroom = "earliest-reset-headroom"

// CAAMLimitsTimeout bounds one `caam limits` call. caam itself allows its
// provider API fetches up to 60s, so a shorter bound here would turn a slow
// provider into a spurious "limits unreadable". Callers that need to answer
// faster should pass their own context deadline.
const CAAMLimitsTimeout = 75 * time.Second

// ErrCAAMNoSeatSelectable reports that caam ranked the pool and found nothing
// eligible. It is a real answer, not a transport failure: every seat is
// exhausted, on paid credits only, or unreadable. Callers must not fall
// through to a static pin on it — that fallthrough is the failure ntm#319 is
// about.
var ErrCAAMNoSeatSelectable = errors.New("caam: no seat selectable")

// CAAMRankedProfile is one profile in a `caam limits --rank` result.
//
// Field set mirrors caam's RankedProfile. Unknown fields are ignored, so a
// newer caam that adds columns stays compatible.
type CAAMRankedProfile struct {
	Provider string `json:"provider"`
	Profile  string `json:"profile"`

	// Rank is 1-based among ELIGIBLE profiles; ineligible ones carry 0.
	Rank     int  `json:"rank"`
	Eligible bool `json:"eligible"`

	// Tier is included_headroom | paid_credits | exhausted | unknown.
	Tier   string `json:"tier"`
	Reason string `json:"reason"`

	UsedPercent     int    `json:"used_percent"`
	BindingWindow   string `json:"binding_window,omitempty"`
	HeadroomPercent int    `json:"headroom_percent"`

	// GoverningWindow names the window whose reset time the rank sorted on:
	// the LONGEST allowance the seat reports (its weekly cap), because that is
	// the quota actually at risk of expiring unused.
	GoverningWindow string     `json:"governing_window,omitempty"`
	ResetsAt        *time.Time `json:"resets_at,omitempty"`
	ResetsInSeconds *int64     `json:"resets_in_seconds,omitempty"`

	AvailabilityScore int    `json:"availability_score"`
	HasCredits        bool   `json:"has_credits"`
	PlanType          string `json:"plan_type,omitempty"`
	Error             string `json:"error,omitempty"`
}

// CAAMLimitsResult is the parsed payload of `caam limits <provider> --rank
// <mode> --format json`.
//
// Selected is the answer a caller wants; caam guarantees Error is non-empty
// exactly when Selected is nil, and exits non-zero in that case while keeping
// stdout pure JSON.
type CAAMLimitsResult struct {
	Rank            string              `json:"rank"`
	Provider        string              `json:"provider,omitempty"`
	Model           string              `json:"model,omitempty"`
	HeadroomCeiling int                 `json:"headroom_ceiling_percent"`
	GeneratedAt     time.Time           `json:"generated_at"`
	Selected        *CAAMRankedProfile  `json:"selected"`
	Profiles        []CAAMRankedProfile `json:"profiles"`
	Error           string              `json:"error,omitempty"`
}

// CAAMLimitsOptions parameterizes a limits query.
type CAAMLimitsOptions struct {
	// Provider is an ntm-side or caam-side provider token; it is normalized
	// with caamLimitsProvider before use.
	Provider string

	// Rank selects a caam rank mode. Empty defaults to
	// CAAMRankEarliestResetHeadroom: the parsed result type IS caam's rank
	// payload, and an unranked `caam limits` answers with a different shape
	// that would unmarshal into an empty struct and read as "no seats".
	Rank string

	// Model narrows scores and eligibility to one model's own allowance,
	// which is what makes a Claude Fable launch honor the weekly_scoped row.
	Model string

	// HeadroomCeiling overrides caam's percent-used ceiling for one call.
	// Zero leaves caam's configured default (95) in place.
	HeadroomCeiling int
}

// caamLimitsProvider maps a provider token onto the vocabulary `caam limits`
// actually accepts.
//
// This is the trap behind ntm#319 item 4: ntm stores Codex accounts under the
// provider id "openai" (see caamProviderForNTM), while caam's limits command
// accepts only "claude" and "codex" and rejects anything else outright. Every
// caller therefore has to translate, and the two vocabularies must never be
// mixed at a call site.
//
// Returns "" for a provider caam limits cannot answer for, so callers can skip
// it instead of shelling out to be told no.
func caamLimitsProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic", "cc":
		return "claude"
	case "codex", "openai", "cod", "chatgpt", "openai-codex":
		return "codex"
	default:
		return ""
	}
}

// CAAMLimitsProviderSupported reports whether `caam limits` can answer for
// this provider token, in either vocabulary.
func CAAMLimitsProviderSupported(provider string) bool {
	return caamLimitsProvider(provider) != ""
}

// CAAMSeatPersona composes the ntm persona name for a caam seat.
//
// caam deliberately keeps ntm's persona vocabulary out of caam: it answers
// with a provider and a profile, and ntm composes the name. The convention is
// "<caam provider>-<profile>", e.g. codex-acme-net / claude-acme-net.
//
// Returns "" when either half is missing, so a caller never builds a
// half-formed persona like "codex-".
func CAAMSeatPersona(provider, profile string) string {
	p := caamLimitsProvider(provider)
	profile = strings.TrimSpace(profile)
	if p == "" || profile == "" {
		return ""
	}
	return p + "-" + profile
}

// Persona is the ntm persona name for this ranked seat.
func (p *CAAMRankedProfile) Persona() string {
	if p == nil {
		return ""
	}
	return CAAMSeatPersona(p.Provider, p.Profile)
}

// caamLimitsCacheEntry is one memoized limits answer.
type caamLimitsCacheEntry struct {
	result *CAAMLimitsResult
	err    error
	expiry time.Time
}

var (
	caamLimitsMutex sync.Mutex
	caamLimitsCache = map[string]caamLimitsCacheEntry{}
)

// caamLimitsCacheTTL keeps one multi-pane `ntm add` from shelling out to caam
// once per pane. Provider quota windows move on the order of hours; a few
// seconds of staleness cannot change which seat is ranked first, and the call
// crosses the network.
const caamLimitsCacheTTL = 20 * time.Second

// InvalidateCAAMLimitsCache drops every memoized limits answer. Tests and
// callers that just changed account state use it.
func InvalidateCAAMLimitsCache() {
	caamLimitsMutex.Lock()
	defer caamLimitsMutex.Unlock()
	caamLimitsCache = map[string]caamLimitsCacheEntry{}
}

func caamLimitsCacheKey(opts CAAMLimitsOptions) string {
	return strings.Join([]string{
		caamLimitsProvider(opts.Provider),
		strings.ToLower(strings.TrimSpace(opts.Rank)),
		strings.ToLower(strings.TrimSpace(opts.Model)),
		fmt.Sprintf("%d", opts.HeadroomCeiling),
	}, "\x00")
}

// Limits runs `caam limits` and returns the parsed result.
//
// Error policy, in the order a caller cares about:
//   - caam not installed            -> ErrToolNotInstalled
//   - provider caam cannot answer   -> a plain error naming the provider
//   - context deadline              -> ErrTimeout
//   - ranked, but nothing eligible  -> ErrCAAMNoSeatSelectable, WITH the
//     result, so the caller can report caam's own reason per profile
//   - unparseable stdout            -> ErrSchemaValidation
//
// The distinction that matters for ntm#319: a non-zero exit with a valid
// payload naming why nothing was selectable is an ANSWER. Silently treating it
// as "caam unavailable" and continuing with a static pin is exactly the
// behavior the rank mode exists to prevent.
func (a *CAAMAdapter) Limits(ctx context.Context, opts CAAMLimitsOptions) (*CAAMLimitsResult, error) {
	provider := caamLimitsProvider(opts.Provider)
	if provider == "" {
		return nil, fmt.Errorf("caam limits does not support provider %q (supported: claude, codex)", opts.Provider)
	}
	if strings.TrimSpace(opts.Rank) == "" {
		opts.Rank = CAAMRankEarliestResetHeadroom
	}

	key := caamLimitsCacheKey(opts)
	caamLimitsMutex.Lock()
	if entry, ok := caamLimitsCache[key]; ok && time.Now().Before(entry.expiry) {
		caamLimitsMutex.Unlock()
		// Hand out a copy: the cache is shared, and a caller that mutated a
		// profile row would corrupt every later reader (same reason
		// cloneCAAMStatus exists).
		return cloneCAAMLimitsResult(entry.result), entry.err
	}
	caamLimitsMutex.Unlock()

	result, err := a.fetchLimits(ctx, provider, opts)

	// A cancelled or timed-out call says nothing about the pool, so caching it
	// would poison every later pane in the same add.
	if !errors.Is(err, ErrTimeout) && ctx.Err() == nil {
		caamLimitsMutex.Lock()
		caamLimitsCache[key] = caamLimitsCacheEntry{
			result: result,
			err:    err,
			expiry: time.Now().Add(caamLimitsCacheTTL),
		}
		caamLimitsMutex.Unlock()
	}
	return result, err
}

// cloneCAAMLimitsResult returns an independent snapshot, including the
// Selected row and the pointer fields inside every profile.
func cloneCAAMLimitsResult(result *CAAMLimitsResult) *CAAMLimitsResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.Profiles = make([]CAAMRankedProfile, len(result.Profiles))
	for i := range result.Profiles {
		cloned.Profiles[i] = cloneCAAMRankedProfile(result.Profiles[i])
	}
	if result.Selected != nil {
		selected := cloneCAAMRankedProfile(*result.Selected)
		cloned.Selected = &selected
	}
	return &cloned
}

func cloneCAAMRankedProfile(profile CAAMRankedProfile) CAAMRankedProfile {
	cloned := profile
	if profile.ResetsAt != nil {
		resetsAt := *profile.ResetsAt
		cloned.ResetsAt = &resetsAt
	}
	if profile.ResetsInSeconds != nil {
		seconds := *profile.ResetsInSeconds
		cloned.ResetsInSeconds = &seconds
	}
	return cloned
}

func (a *CAAMAdapter) fetchLimits(ctx context.Context, provider string, opts CAAMLimitsOptions) (*CAAMLimitsResult, error) {
	path, installed := a.Detect()
	if !installed {
		return nil, ErrToolNotInstalled
	}

	args := []string{"limits", provider, "--format", "json"}
	if rank := strings.TrimSpace(opts.Rank); rank != "" {
		args = append(args, "--rank", rank)
	}
	if model := strings.TrimSpace(opts.Model); model != "" {
		args = append(args, "--model", model)
	}
	if opts.HeadroomCeiling > 0 {
		args = append(args, "--headroom", fmt.Sprintf("%d", opts.HeadroomCeiling))
	}

	ctx, cancel := context.WithTimeout(ctx, CAAMLimitsTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, ErrTimeout
	}

	// caam keeps stdout pure JSON even when it exits non-zero — the human
	// message goes to stderr — so parse before judging the exit code.
	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		if runErr != nil {
			return nil, fmt.Errorf("read caam limits: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("%w: caam limits returned no output", ErrSchemaValidation)
	}

	var result CAAMLimitsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		if runErr != nil {
			// Non-zero exit AND unparseable output: the exit is the better
			// diagnosis (unknown flag, unsupported provider, older caam).
			return nil, fmt.Errorf("read caam limits: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("%w: parse caam limits: %v", ErrSchemaValidation, err)
	}

	// Ranked with nothing eligible: a real, reportable answer.
	if strings.TrimSpace(opts.Rank) != "" && result.Selected == nil {
		reason := strings.TrimSpace(result.Error)
		if reason == "" {
			reason = strings.TrimSpace(stderr.String())
		}
		if reason == "" {
			reason = "caam selected no profile and gave no reason"
		}
		return &result, fmt.Errorf("%w: %s", ErrCAAMNoSeatSelectable, reason)
	}

	if runErr != nil && result.Selected == nil && len(result.Profiles) == 0 {
		return nil, fmt.Errorf("read caam limits: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}

	return &result, nil
}

// RankedSeat returns the profile caam ranks first for new work on this
// provider, using the earliest-reset-with-headroom policy.
//
// It is the single entry point ntm's seat selection should use: it pins the
// rank mode (never `--best`) and translates the provider vocabulary, so no
// call site can get either wrong.
func (a *CAAMAdapter) RankedSeat(ctx context.Context, provider, model string) (*CAAMRankedProfile, error) {
	result, err := a.Limits(ctx, CAAMLimitsOptions{
		Provider: provider,
		Rank:     CAAMRankEarliestResetHeadroom,
		Model:    model,
	})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Selected == nil {
		return nil, fmt.Errorf("%w: caam returned no selection", ErrCAAMNoSeatSelectable)
	}
	return result.Selected, nil
}
