// Package ratelimit provides rate limit tracking and adaptive delay management for AI agents.
package ratelimit

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "strconv"
    "sync"
    "time"

    "github.com/Dicklesworthstone/ntm/internal/status"
)

// Default delays per provider (initial values before learning).
const (
	DefaultDelayAnthropic = 15 * time.Second
	DefaultDelayOpenAI    = 10 * time.Second
	DefaultDelayGoogle    = 8 * time.Second

	MinDelayAnthropic = 5 * time.Second
	MinDelayOpenAI    = 3 * time.Second
	MinDelayGoogle    = 2 * time.Second

	// MaxLearnedDelay caps the adaptive delay to prevent overflow.
	// With delayIncreaseRate=1.5, uncapped growth overflows int64
	// nanoseconds after ~61 consecutive rate limits (ntm-45).
	MaxLearnedDelay = 10 * time.Minute

	// Learning parameters
	delayIncreaseRate       = 1.5 // Increase by 50% on rate limit
	delayDecreaseRate       = 0.9 // Decrease by 10% on consecutive successes
	successesBeforeDecrease = 10  // Number of consecutive successes before decreasing delay
)

// RateLimitEvent represents a rate limit occurrence.
type RateLimitEvent struct {
	Time     time.Time `json:"time"`
	Provider string    `json:"provider"`
	Action   string    `json:"action"` // "spawn" or "send"
}

// ProviderState tracks the current state for a provider.
type ProviderState struct {
    CurrentDelay       time.Duration `json:"current_delay"`
    ConsecutiveSuccess int           `json:"consecutive_success"`
    LastRateLimit      time.Time     `json:"last_rate_limit,omitempty"`
    CooldownUntil      time.Time     `json:"cooldown_until,omitempty"`
    TotalRateLimits    int           `json:"total_rate_limits"`
    TotalSuccesses     int           `json:"total_successes"`
}

// RateLimitTracker tracks rate limit events and learns optimal spawn/send timing.
type RateLimitTracker struct {
	mu      sync.RWMutex
	history map[string][]RateLimitEvent // provider -> recent events
	state   map[string]*ProviderState   // provider -> current state
	dataDir string
}

// persistedData is the JSON structure for persistence.
type persistedData struct {
	State   map[string]*ProviderState   `json:"state"`
	History map[string][]RateLimitEvent `json:"history,omitempty"`
}

// NewRateLimitTracker creates a new RateLimitTracker instance.
// If dataDir is empty, persistence is disabled.
func NewRateLimitTracker(dataDir string) *RateLimitTracker {
	return &RateLimitTracker{
		history: make(map[string][]RateLimitEvent),
		state:   make(map[string]*ProviderState),
		dataDir: dataDir,
	}
}

// getDefaultDelay returns the default delay for a provider.
func getDefaultDelay(provider string) time.Duration {
	switch provider {
	case "anthropic", "claude":
		return DefaultDelayAnthropic
	case "openai", "gpt":
		return DefaultDelayOpenAI
	case "google", "gemini":
		return DefaultDelayGoogle
	default:
		return DefaultDelayOpenAI // Default to OpenAI timing
	}
}

// getMinDelay returns the minimum delay for a provider.
func getMinDelay(provider string) time.Duration {
	switch provider {
	case "anthropic", "claude":
		return MinDelayAnthropic
	case "openai", "gpt":
		return MinDelayOpenAI
	case "google", "gemini":
		return MinDelayGoogle
	default:
		return MinDelayOpenAI
	}
}

// getOrCreateState returns the provider state, creating it if needed.
func (t *RateLimitTracker) getOrCreateState(provider string) *ProviderState {
	if s, ok := t.state[provider]; ok {
		return s
	}
	s := &ProviderState{
		CurrentDelay: getDefaultDelay(provider),
	}
	t.state[provider] = s
	return s
}

// RecordRateLimit records a rate limit event and adjusts delays.
func (t *RateLimitTracker) RecordRateLimit(provider, action string) {
    provider = NormalizeProvider(provider)
    t.mu.Lock()
    defer t.mu.Unlock()

    now := time.Now()
    t.recordRateLimitLocked(provider, action, now)
}

// RecordRateLimitWithCooldown records a rate limit event and sets a cooldown window.
// If waitSeconds is <= 0, the current adaptive delay is used as the cooldown duration.
// Returns the applied cooldown duration.
func (t *RateLimitTracker) RecordRateLimitWithCooldown(provider, action string, waitSeconds int) time.Duration {
    provider = NormalizeProvider(provider)
    t.mu.Lock()
    defer t.mu.Unlock()

    now := time.Now()
    state := t.recordRateLimitLocked(provider, action, now)

    // Cap waitSeconds to prevent absurdly long cooldowns from misparse
    // (e.g., ParseWaitSeconds extracting a Unix timestamp from output).
    const maxCooldownSeconds = 600 // 10 minutes
    if waitSeconds > maxCooldownSeconds {
        waitSeconds = maxCooldownSeconds
    }
    cooldown := time.Duration(waitSeconds) * time.Second
    if waitSeconds <= 0 {
        cooldown = state.CurrentDelay
    }

    cooldownUntil := now.Add(cooldown)
    if cooldownUntil.After(state.CooldownUntil) {
        state.CooldownUntil = cooldownUntil
    }

    return cooldown
}

func (t *RateLimitTracker) recordRateLimitLocked(provider, action string, now time.Time) *ProviderState {
    event := RateLimitEvent{
        Time:     now,
        Provider: provider,
        Action:   action,
    }

    // Add to history (keep last 100 events per provider)
    t.history[provider] = append(t.history[provider], event)
    if len(t.history[provider]) > 100 {
        t.history[provider] = t.history[provider][len(t.history[provider])-100:]
    }

    // Update state
    state := t.getOrCreateState(provider)
    state.LastRateLimit = now
    state.TotalRateLimits++
    state.ConsecutiveSuccess = 0 // Reset consecutive successes

    // Increase delay by 50%, capping to prevent int64 overflow (ntm-45).
    newDelayF := float64(state.CurrentDelay) * delayIncreaseRate
    if newDelayF > float64(MaxLearnedDelay) {
        state.CurrentDelay = MaxLearnedDelay
    } else {
        state.CurrentDelay = time.Duration(newDelayF)
    }
    return state
}

// RecordSuccess records a successful request.
func (t *RateLimitTracker) RecordSuccess(provider string) {
    provider = NormalizeProvider(provider)
    t.mu.Lock()
    defer t.mu.Unlock()

	state := t.getOrCreateState(provider)
	state.TotalSuccesses++
	state.ConsecutiveSuccess++

	// After 10 consecutive successes, decrease delay by 10%
	if state.ConsecutiveSuccess >= successesBeforeDecrease {
		minDelay := getMinDelay(provider)
		newDelay := time.Duration(float64(state.CurrentDelay) * delayDecreaseRate)
		if newDelay < minDelay {
			newDelay = minDelay
		}
		state.CurrentDelay = newDelay
		state.ConsecutiveSuccess = 0 // Reset counter
	}
}

// CooldownRemaining returns how much cooldown time remains for a provider.
// Returns 0 if no cooldown is active or the provider is unknown.
func (t *RateLimitTracker) CooldownRemaining(provider string) time.Duration {
    provider = NormalizeProvider(provider)
    t.mu.RLock()
    defer t.mu.RUnlock()

    state, ok := t.state[provider]
    if !ok {
        return 0
    }
    remaining := time.Until(state.CooldownUntil)
    if remaining < 0 {
        return 0
    }
    return remaining
}

// IsInCooldown reports whether the provider is currently in a cooldown window.
func (t *RateLimitTracker) IsInCooldown(provider string) bool {
    return t.CooldownRemaining(provider) > 0
}

// ClearCooldown clears any active cooldown for a provider.
func (t *RateLimitTracker) ClearCooldown(provider string) {
    provider = NormalizeProvider(provider)
    t.mu.Lock()
    defer t.mu.Unlock()

    if state, ok := t.state[provider]; ok {
        state.CooldownUntil = time.Time{}
    }
}

// GetOptimalDelay returns the current optimal delay for a provider.
func (t *RateLimitTracker) GetOptimalDelay(provider string) time.Duration {
	provider = NormalizeProvider(provider)
	t.mu.RLock()
	defer t.mu.RUnlock()

	if state, ok := t.state[provider]; ok {
		return state.CurrentDelay
	}
	return getDefaultDelay(provider)
}

// GetProviderState returns a copy of the state for a provider.
func (t *RateLimitTracker) GetProviderState(provider string) *ProviderState {
	provider = NormalizeProvider(provider)
	t.mu.RLock()
	defer t.mu.RUnlock()

	state, ok := t.state[provider]
	if !ok {
		return nil
	}
	// Return a copy
	copy := *state
	return &copy
}

// GetAllProviders returns all tracked providers.
func (t *RateLimitTracker) GetAllProviders() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	providers := make([]string, 0, len(t.state))
	for p := range t.state {
		providers = append(providers, p)
	}
	return providers
}

// GetRecentEvents returns recent rate limit events for a provider.
func (t *RateLimitTracker) GetRecentEvents(provider string, limit int) []RateLimitEvent {
	provider = NormalizeProvider(provider)
	t.mu.RLock()
	defer t.mu.RUnlock()

	events := t.history[provider]
	if len(events) == 0 {
		return nil
	}

	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}

	// Return the most recent events
	result := make([]RateLimitEvent, limit)
	copy(result, events[len(events)-limit:])
	return result
}

// Reset resets the state for a provider to defaults.
func (t *RateLimitTracker) Reset(provider string) {
	provider = NormalizeProvider(provider)
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.state, provider)
	delete(t.history, provider)
}

// ResetAll resets all provider states.
func (t *RateLimitTracker) ResetAll() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.state = make(map[string]*ProviderState)
	t.history = make(map[string][]RateLimitEvent)
}

// LoadFromDir loads rate limit data from the .ntm directory.
func (t *RateLimitTracker) LoadFromDir(dir string) error {
	if dir == "" {
		dir = t.dataDir
	}
	if dir == "" {
		return nil // persistence disabled
	}

	path := filepath.Join(dir, ".ntm", "rate_limits.json")
	// Read without lock (IO)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No file yet, that's fine
		}
		return fmt.Errorf("read rate limits file: %w", err)
	}

	var pd persistedData
	if err := json.Unmarshal(data, &pd); err != nil {
		return fmt.Errorf("parse rate limits file: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if pd.State != nil {
		// Sanitize loaded state: reset corrupted delays and cooldowns
		// that may have been persisted from a previous overflow bug.
		for _, ps := range pd.State {
			if ps.CurrentDelay < 0 || ps.CurrentDelay > time.Hour {
				ps.CurrentDelay = 0
			}
			if !ps.CooldownUntil.IsZero() && ps.CooldownUntil.After(time.Now().Add(time.Hour)) {
				ps.CooldownUntil = time.Time{}
			}
		}
		t.state = pd.State
	}
	if pd.History != nil {
		t.history = pd.History
	}

	return nil
}

// SaveToDir saves rate limit data to the .ntm directory.
func (t *RateLimitTracker) SaveToDir(dir string) error {
	if dir == "" {
		dir = t.dataDir
	}
	if dir == "" {
		return nil // persistence disabled
	}

	t.mu.RLock()
	pd := persistedData{
		State:   make(map[string]*ProviderState),
		History: make(map[string][]RateLimitEvent),
	}
	// Deep copy to release lock early
	for k, v := range t.state {
		val := *v
		pd.State[k] = &val
	}
	for k, v := range t.history {
		pd.History[k] = append([]RateLimitEvent(nil), v...)
	}
	t.mu.RUnlock()

	ntmDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		return fmt.Errorf("create .ntm dir: %w", err)
	}

	data, err := json.MarshalIndent(pd, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rate limits: %w", err)
	}

	path := filepath.Join(ntmDir, "rate_limits.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write rate limits file: %w", err)
	}

	return nil
}

// FormatDelay formats a duration as a human-readable string.
func FormatDelay(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

// NormalizeProvider normalizes provider names to canonical forms.
func NormalizeProvider(provider string) string {
	switch provider {
	case "anthropic", "claude", "claude-code", "cc":
		return "anthropic"
	case "openai", "gpt", "chatgpt", "codex", "cod":
		return "openai"
	case "google", "gemini", "gmi":
		return "google"
	default:
		return provider
	}
}

// RateLimitDetection captures a detected rate limit signal from output.
type RateLimitDetection struct {
    RateLimited bool
    WaitSeconds int
    ExitCode    int
    Source      string // "output" or "exit_code"
    AgentType   string // Agent type hint (e.g., "cod") when available
}

const (
    detectionSourceOutput   = "output"
    detectionSourceExitCode = "exit_code"
)

type waitPattern struct {
    re         *regexp.Regexp
    multiplier int
}

var waitTimePatterns = []waitPattern{
    {regexp.MustCompile(`(?i)retry-after[:=]\s*(\d+)`), 1},
    {regexp.MustCompile(`(?i)try\s+again\s+in\s+(\d+)\s*s`), 1},
    {regexp.MustCompile(`(?i)wait\s+(\d+)\s*(?:second|sec|s)`), 1},
    {regexp.MustCompile(`(?i)retry\s+(?:after|in)\s+(\d+)\s*(?:s|sec)`), 1},
    {regexp.MustCompile(`(?i)retry\s+(?:after|in)\s+(\d+)\s*(?:m|min|minute|minutes)`), 60},
    {regexp.MustCompile(`(?i)(\d+)\s*(?:second|sec)s?\s+(?:cooldown|delay|wait)`), 1},
    {regexp.MustCompile(`(?i)rate.?limit.*?(\d+)\s*s`), 1},
}

var exitCodePatterns = []*regexp.Regexp{
    regexp.MustCompile(`(?i)exit(?:\s+status|\s+code)?[:\s]+(\d+)`),
    regexp.MustCompile(`(?i)exited\s+with\s+code\s+(\d+)`),
    regexp.MustCompile(`(?i)status[:\s]+(\d+)`),
}

// ParseWaitSeconds extracts a suggested wait time in seconds from output.
// Returns 0 if no wait time is found.
func ParseWaitSeconds(output string) int {
    if output == "" {
        return 0
    }
    output = status.StripANSI(output)

    for _, pattern := range waitTimePatterns {
        if matches := pattern.re.FindStringSubmatch(output); len(matches) > 1 {
            seconds, err := strconv.Atoi(matches[1])
            if err == nil && seconds > 0 {
                return seconds * pattern.multiplier
            }
        }
    }
    return 0
}

// ParseExitCode tries to extract an exit code from output.
func ParseExitCode(output string) (int, bool) {
    if output == "" {
        return 0, false
    }
    output = status.StripANSI(output)

    for _, pattern := range exitCodePatterns {
        if matches := pattern.FindStringSubmatch(output); len(matches) > 1 {
            code, err := strconv.Atoi(matches[1])
            if err == nil {
                return code, true
            }
        }
    }
    return 0, false
}

// DetectRateLimit inspects output for rate limit signals, including exit code 429.
func DetectRateLimit(output string) RateLimitDetection {
    detection := RateLimitDetection{WaitSeconds: ParseWaitSeconds(output)}

    if code, ok := ParseExitCode(output); ok {
        detection.ExitCode = code
        if code == 429 {
            detection.RateLimited = true
            detection.Source = detectionSourceExitCode
            return detection
        }
    }

    if status.DetectErrorInOutput(output) == status.ErrorRateLimit {
        detection.RateLimited = true
        detection.Source = detectionSourceOutput
        return detection
    }

    for _, et := range status.DetectAllErrorsInOutput(output) {
        if et == status.ErrorRateLimit {
            detection.RateLimited = true
            detection.Source = detectionSourceOutput
            return detection
        }
    }

    return detection
}

// DetectRateLimitForAgent inspects output for rate limit signals with agent type context.
// When agentType is "cod", additional Codex-specific patterns are checked.
func DetectRateLimitForAgent(output string, agentType string) RateLimitDetection {
    detection := DetectRateLimit(output)
    detection.AgentType = agentType

    // Already detected by generic logic
    if detection.RateLimited {
        return detection
    }

    // Codex-specific patterns (only check for cod agents)
    if agentType == "cod" && DetectCodexRateLimit(output) {
        detection.RateLimited = true
        detection.Source = detectionSourceOutput
        return detection
    }

    return detection
}

// codexRateLimitPatterns contains patterns specific to Codex CLI rate-limit messages.
var codexRateLimitPatterns = []*regexp.Regexp{
    regexp.MustCompile(`(?i)openai.*rate.?limit`),
    regexp.MustCompile(`(?i)codex.*rate.?limit`),
    regexp.MustCompile(`(?i)tokens?\s+per\s+min`),
    regexp.MustCompile(`(?i)requests?\s+per\s+min`),
    regexp.MustCompile(`(?i)api\.openai\.com.*429`),
    regexp.MustCompile(`(?i)insufficient_quota`),
    regexp.MustCompile(`(?i)billing.*limit|limit.*billing`),
    regexp.MustCompile(`(?i)exceeded.*(?:token|request).*(?:limit|quota)`),
    regexp.MustCompile(`(?i)(?:token|request).*(?:limit|quota).*exceeded`),
    regexp.MustCompile(`(?i)please\s+try\s+again`),
    regexp.MustCompile(`(?i)usage.*(?:cap|limit).*reached`),
    regexp.MustCompile(`(?i)RateLimitError`),
}

// DetectCodexRateLimit checks output for Codex-specific rate limit patterns.
// This is a pure function suitable for unit testing.
func DetectCodexRateLimit(output string) bool {
    if output == "" {
        return false
    }
    cleaned := status.StripANSI(output)
    for _, pat := range codexRateLimitPatterns {
        if pat.MatchString(cleaned) {
            return true
        }
    }
    return false
}

// =============================================================================
// CodexThrottle: AIMD-based adaptive throttling for Codex launches (bd-3qoly)
// =============================================================================

// ThrottlePhase describes the current throttle state.
type ThrottlePhase string

const (
    // ThrottleNormal means no throttling is active.
    ThrottleNormal ThrottlePhase = "normal"
    // ThrottlePaused means new cod launches are paused due to rate limiting.
    ThrottlePaused ThrottlePhase = "paused"
    // ThrottleRecovering means throttle is gradually allowing launches again (AIMD additive increase).
    ThrottleRecovering ThrottlePhase = "recovering"
)

// AIMD parameters for Codex throttle.
const (
    // AIMDMultiplicativeDecrease halves the allowed rate on rate-limit detection.
    AIMDMultiplicativeDecrease = 0.5
    // AIMDAdditiveIncrease adds one allowed launch per recovery interval.
    AIMDAdditiveIncrease = 1
    // DefaultCooldownWindow is the minimum pause after a rate-limit event.
    DefaultCooldownWindow = 30 * time.Second
    // MaxCooldownWindow caps the cooldown to prevent indefinite pausing.
    MaxCooldownWindow = 5 * time.Minute
    // RecoveryCheckInterval is how often to re-evaluate recovery during additive increase.
    RecoveryCheckInterval = 15 * time.Second
)

// CodexThrottle implements AIMD-based adaptive throttling for Codex (cod) agent launches.
// It is safe for concurrent use.
type CodexThrottle struct {
    mu sync.RWMutex

    phase         ThrottlePhase
    cooldownUntil time.Time
    cooldownDur   time.Duration // Current cooldown window (grows on repeated rate limits)

    // AIMD state
    allowedConcurrent int // Current allowed concurrent cod launches (AIMD window size)
    maxConcurrent     int // Maximum allowed (ceiling for additive increase)

    // Bookkeeping
    rateLimitCount   int       // Total rate-limit events observed
    lastRateLimit    time.Time // When last rate-limit was observed
    lastRecoveryStep time.Time // When last additive increase happened
    affectedPanes    []string  // Pane IDs that were rate-limited

    // nowFn allows test time injection.
    nowFn func() time.Time
}

// CodexThrottleStatus is a read-only snapshot of the throttle state.
type CodexThrottleStatus struct {
    Phase             ThrottlePhase `json:"phase"`
    CooldownRemaining time.Duration `json:"cooldown_remaining"`
    AllowedConcurrent int           `json:"allowed_concurrent"`
    MaxConcurrent     int           `json:"max_concurrent"`
    RateLimitCount    int           `json:"rate_limit_count"`
    AffectedPanes     []string      `json:"affected_panes,omitempty"`
    Guidance          string        `json:"guidance"`
}

// NewCodexThrottle creates a CodexThrottle with the given max concurrency ceiling.
func NewCodexThrottle(maxConcurrent int) *CodexThrottle {
    if maxConcurrent < 1 {
        maxConcurrent = 3
    }
    return &CodexThrottle{
        phase:             ThrottleNormal,
        allowedConcurrent: maxConcurrent,
        maxConcurrent:     maxConcurrent,
        nowFn:             time.Now,
    }
}

// now returns the current time, using the injectable nowFn.
func (ct *CodexThrottle) now() time.Time {
    if ct.nowFn != nil {
        return ct.nowFn()
    }
    return time.Now()
}

// RecordRateLimit is called when a Codex rate-limit event is detected on the given pane.
// It triggers multiplicative decrease and sets a cooldown window.
func (ct *CodexThrottle) RecordRateLimit(paneID string, waitSeconds int) {
    ct.mu.Lock()
    defer ct.mu.Unlock()

    now := ct.now()
    ct.rateLimitCount++
    ct.lastRateLimit = now

    // Track affected pane
    ct.addAffectedPane(paneID)

    // AIMD multiplicative decrease
    newAllowed := int(float64(ct.allowedConcurrent) * AIMDMultiplicativeDecrease)
    if newAllowed < 0 {
        newAllowed = 0
    }
    ct.allowedConcurrent = newAllowed

    // Calculate cooldown window
    cooldown := DefaultCooldownWindow
    if waitSeconds > 0 {
        cooldown = time.Duration(waitSeconds) * time.Second
    }
    // Scale up cooldown on repeated rate limits (multiplicative backoff)
    if ct.rateLimitCount > 1 {
        cooldown = time.Duration(float64(cooldown) * 1.5)
    }
    if cooldown > MaxCooldownWindow {
        cooldown = MaxCooldownWindow
    }
    ct.cooldownDur = cooldown

    cooldownUntil := now.Add(cooldown)
    if cooldownUntil.After(ct.cooldownUntil) {
        ct.cooldownUntil = cooldownUntil
    }

    ct.phase = ThrottlePaused
}

// addAffectedPane adds a pane ID to the affected list if not already present.
func (ct *CodexThrottle) addAffectedPane(paneID string) {
    if paneID == "" {
        return
    }
    for _, p := range ct.affectedPanes {
        if p == paneID {
            return
        }
    }
    ct.affectedPanes = append(ct.affectedPanes, paneID)
}

// MayLaunch reports whether a new cod launch is allowed right now.
// It also advances the AIMD state (checks cooldown expiry, additive increase).
func (ct *CodexThrottle) MayLaunch(currentRunning int) bool {
    ct.mu.Lock()
    defer ct.mu.Unlock()

    ct.advanceStateLocked()

    switch ct.phase {
    case ThrottleNormal:
        return true
    case ThrottlePaused:
        return false
    case ThrottleRecovering:
        return currentRunning < ct.allowedConcurrent
    }
    return true
}

// advanceStateLocked transitions state based on elapsed time. Caller must hold ct.mu.
func (ct *CodexThrottle) advanceStateLocked() {
    now := ct.now()

    if ct.phase == ThrottlePaused {
        if now.After(ct.cooldownUntil) {
            // Cooldown expired: enter recovery with additive increase
            ct.phase = ThrottleRecovering
            ct.lastRecoveryStep = now
            // Start recovery with at least 1 allowed concurrent
            if ct.allowedConcurrent < 1 {
                ct.allowedConcurrent = 1
            }
        }
        return
    }

    if ct.phase == ThrottleRecovering {
        // AIMD additive increase: add 1 per recovery interval
        if now.Sub(ct.lastRecoveryStep) >= RecoveryCheckInterval {
            steps := int(now.Sub(ct.lastRecoveryStep) / RecoveryCheckInterval)
            ct.allowedConcurrent += steps * AIMDAdditiveIncrease
            if ct.allowedConcurrent >= ct.maxConcurrent {
                ct.allowedConcurrent = ct.maxConcurrent
                ct.phase = ThrottleNormal
                ct.affectedPanes = nil // Clear affected panes on full recovery
            }
            ct.lastRecoveryStep = now
        }
    }
}

// RecordSuccess is called when a cod agent completes work successfully.
// This does NOT advance recovery -- only time does. But it resets the
// rate-limit counter so that future cooldowns are not over-penalized.
func (ct *CodexThrottle) RecordSuccess() {
    ct.mu.Lock()
    defer ct.mu.Unlock()

    // Reset consecutive penalty count on success so next
    // rate-limit event does not immediately escalate.
    ct.rateLimitCount = 0
}

// Status returns a read-only snapshot of the current throttle state.
func (ct *CodexThrottle) Status() CodexThrottleStatus {
    ct.mu.Lock()
    ct.advanceStateLocked()
    defer ct.mu.Unlock()

    var remaining time.Duration
    if ct.phase == ThrottlePaused {
        remaining = ct.cooldownUntil.Sub(ct.now())
        if remaining < 0 {
            remaining = 0
        }
    }

    panes := make([]string, len(ct.affectedPanes))
    copy(panes, ct.affectedPanes)

    return CodexThrottleStatus{
        Phase:             ct.phase,
        CooldownRemaining: remaining,
        AllowedConcurrent: ct.allowedConcurrent,
        MaxConcurrent:     ct.maxConcurrent,
        RateLimitCount:    ct.rateLimitCount,
        AffectedPanes:     panes,
        Guidance:          ct.guidanceLocked(),
    }
}

// guidanceLocked returns human-readable remediation guidance. Caller must hold ct.mu.
func (ct *CodexThrottle) guidanceLocked() string {
    switch ct.phase {
    case ThrottlePaused:
        remaining := ct.cooldownUntil.Sub(ct.now())
        if remaining < 0 {
            remaining = 0
        }
        return fmt.Sprintf(
            "Codex rate-limited: new cod launches paused for %s. "+
                "Claude and Gemini agents are unaffected. "+
                "Consider reducing concurrent cod panes or waiting for quota reset.",
            FormatDelay(remaining),
        )
    case ThrottleRecovering:
        return fmt.Sprintf(
            "Codex recovering: allowing %d/%d concurrent cod launches. "+
                "Rate will increase gradually. Other agent types are unaffected.",
            ct.allowedConcurrent, ct.maxConcurrent,
        )
    default:
        return "Codex throttle inactive: all launches permitted."
    }
}

// ClearAffectedPane removes a pane from the affected list (e.g., on pane restart).
func (ct *CodexThrottle) ClearAffectedPane(paneID string) {
    ct.mu.Lock()
    defer ct.mu.Unlock()

    for i, p := range ct.affectedPanes {
        if p == paneID {
            ct.affectedPanes = append(ct.affectedPanes[:i], ct.affectedPanes[i+1:]...)
            return
        }
    }
}

// Reset restores the throttle to a clean normal state.
func (ct *CodexThrottle) Reset() {
    ct.mu.Lock()
    defer ct.mu.Unlock()

    ct.phase = ThrottleNormal
    ct.cooldownUntil = time.Time{}
    ct.cooldownDur = 0
    ct.allowedConcurrent = ct.maxConcurrent
    ct.rateLimitCount = 0
    ct.lastRateLimit = time.Time{}
    ct.lastRecoveryStep = time.Time{}
    ct.affectedPanes = nil
}
