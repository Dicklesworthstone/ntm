package ratelimit

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/provider"
)

// ErrorClass is shared with provider conformance so every launch boundary
// applies the same exact Z.ai taxonomy. Only transient categories are retried.
type ErrorClass = provider.ErrorClass

const (
	ErrorRateLimited         = provider.ErrorRateLimited
	ErrorOverloaded          = provider.ErrorOverloaded
	ErrorLongPeriodQuota     = provider.ErrorLongPeriodQuota
	ErrorPlanExpired         = provider.ErrorPlanExpired
	ErrorUnsupportedModel    = provider.ErrorUnsupportedModel
	ErrorUsageRestricted     = provider.ErrorUsageRestricted
	ErrorInsufficientBalance = provider.ErrorInsufficientBalance
	ErrorAuthentication      = provider.ErrorAuthentication
	ErrorIdentityMismatch    = provider.ErrorIdentityMismatch
	ErrorUnknown             = provider.ErrorUnknown
)

// ClassifyProviderError performs exact classification from HTTP status and a
// normalized provider error code. Z.ai business codes are evaluated before an
// HTTP status because a 429 can represent a non-retryable long-period quota or
// account/policy condition. It intentionally does not guess from prose.
func ClassifyProviderError(httpStatus int, code string) ErrorClass {
	return provider.ClassifyProviderError(httpStatus, code)
}

// AdmissionConfig is an identity-local capacity policy. Token refill is
// expressed as tokens per second. MaxConcurrent and TokenCapacity must be
// positive; a zero retry-after uses exponential backoff with full jitter.
type AdmissionConfig struct {
	MaxConcurrent    int
	TokenCapacity    float64
	TokensPerSecond  float64
	BaseBackoff      time.Duration
	MaxBackoff       time.Duration
	CircuitThreshold int
	CircuitOpenFor   time.Duration
}

func DefaultAdmissionConfig() AdmissionConfig {
	return AdmissionConfig{
		MaxConcurrent:    1,
		TokenCapacity:    1,
		TokensPerSecond:  1.0 / 30.0,
		BaseBackoff:      5 * time.Second,
		MaxBackoff:       5 * time.Minute,
		CircuitThreshold: 3,
		CircuitOpenFor:   1 * time.Minute,
	}
}

func (c AdmissionConfig) validate() error {
	if c.MaxConcurrent < 1 || c.TokenCapacity <= 0 || c.TokensPerSecond <= 0 {
		return fmt.Errorf("admission capacity values must be positive")
	}
	if c.BaseBackoff <= 0 || c.MaxBackoff < c.BaseBackoff || c.CircuitThreshold < 1 || c.CircuitOpenFor <= 0 {
		return fmt.Errorf("admission backoff and circuit values are invalid")
	}
	return nil
}

// Decision is the complete, non-failover admission result. RetryAt is set for
// a temporary denial; permanent provider errors have RetryAt=nil.
type Decision struct {
	Allowed    bool
	Reason     ErrorClass
	RetryAt    *time.Time
	NoFailover bool
}

type admissionState struct {
	running             int
	tokens              float64
	lastRefill          time.Time
	consecutiveFailures int
	nextRetry           time.Time
	circuitOpenUntil    time.Time
	halfOpenInFlight    bool
	terminalReason      ErrorClass
}

// AdmissionController isolates budgets by provider.Identity.CapacityScope.
// It intentionally has no fallback target or provider-selection API.
type AdmissionController struct {
	mu     sync.Mutex
	config AdmissionConfig
	now    func() time.Time
	rand   func() float64
	states map[provider.CapacityScope]*admissionState
}

// NewAdmissionController uses injected clock/random functions when supplied;
// they make retry and circuit behavior deterministic in tests. randFn must
// return a value in [0,1); values outside that range are clamped defensively.
func NewAdmissionController(config AdmissionConfig, now func() time.Time, randFn func() float64) (*AdmissionController, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	if randFn == nil {
		randFn = func() float64 { return 0.5 }
	}
	return &AdmissionController{config: config, now: now, rand: randFn, states: make(map[provider.CapacityScope]*admissionState)}, nil
}

// Acquire consumes one identity-local concurrency slot and token. A successful
// acquire must be paired with Release. This method never retries or selects a
// different identity on the caller's behalf.
func (c *AdmissionController) Acquire(identity provider.Identity) Decision {
	if !validIdentity(identity) {
		return Decision{Reason: ErrorIdentityMismatch, NoFailover: true}
	}
	scope := identity.CapacityScope()
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.stateLocked(scope, now)
	c.refillLocked(state, now)
	if state.terminalReason != "" {
		return Decision{Reason: state.terminalReason, NoFailover: true}
	}

	if !state.circuitOpenUntil.IsZero() && now.Before(state.circuitOpenUntil) {
		return retryDecision(ErrorOverloaded, state.circuitOpenUntil)
	}
	if !state.nextRetry.IsZero() && now.Before(state.nextRetry) {
		return retryDecision(ErrorRateLimited, state.nextRetry)
	}
	if !state.circuitOpenUntil.IsZero() && state.halfOpenInFlight {
		return retryDecision(ErrorOverloaded, now.Add(c.config.BaseBackoff))
	}
	if state.running >= c.config.MaxConcurrent {
		return Decision{Reason: ErrorRateLimited, NoFailover: true}
	}
	if state.tokens < 1 {
		wait := time.Duration(math.Ceil((1 - state.tokens) / c.config.TokensPerSecond * float64(time.Second)))
		return retryDecision(ErrorRateLimited, now.Add(wait))
	}
	// An expired circuit admits exactly one probe. Mark it only after all
	// ordinary admission checks pass so a capacity/token denial cannot leave
	// the circuit permanently half-open with no request in flight.
	if !state.circuitOpenUntil.IsZero() {
		state.halfOpenInFlight = true
	}
	state.running++
	state.tokens--
	return Decision{Allowed: true, NoFailover: true}
}

// Release returns only the concurrency slot. Tokens represent actual request
// admission and are deliberately not refunded after a process starts.
func (c *AdmissionController) Release(identity provider.Identity) {
	if !validIdentity(identity) {
		return
	}
	scope := identity.CapacityScope()
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.states[scope]; state != nil && state.running > 0 {
		state.running--
	}
}

// RecordResult records the outcome of one acquired request. retryAfter is
// authoritative when supplied for a rate limit; otherwise exponential backoff
// uses full jitter. Permanent failures never create a retry schedule.
func (c *AdmissionController) RecordResult(identity provider.Identity, class ErrorClass, retryAfter time.Duration) Decision {
	if !validIdentity(identity) {
		return Decision{Reason: ErrorIdentityMismatch, NoFailover: true}
	}
	scope := identity.CapacityScope()
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.stateLocked(scope, now)
	if class == "" {
		class = ErrorUnknown
	}
	// Only an explicitly classified rate limit or temporary overload may retry.
	// Long-period quota, plan/balance, policy, model, identity, and auth errors
	// require deliberate operator remediation under the same identity.
	if class != ErrorRateLimited && class != ErrorOverloaded {
		state.terminalReason = class
		state.consecutiveFailures = 0
		state.nextRetry = time.Time{}
		state.circuitOpenUntil = time.Time{}
		state.halfOpenInFlight = false
		return Decision{Reason: class, NoFailover: true}
	}

	state.consecutiveFailures++
	state.halfOpenInFlight = false
	if retryAfter > 0 && class == ErrorRateLimited {
		state.nextRetry = now.Add(retryAfter)
	} else {
		state.nextRetry = now.Add(c.jitteredBackoffLocked(state.consecutiveFailures))
	}
	if state.consecutiveFailures >= c.config.CircuitThreshold {
		state.circuitOpenUntil = now.Add(c.config.CircuitOpenFor)
		if state.nextRetry.After(state.circuitOpenUntil) {
			state.circuitOpenUntil = state.nextRetry
		}
	}
	return retryDecision(class, state.nextRetry)
}

// RecordSuccess closes a half-open circuit and clears transient backoff only
// for this exact identity scope. Permanent provider classifications require an
// explicit Reset so a stray or misattributed success cannot reopen the lane.
func (c *AdmissionController) RecordSuccess(identity provider.Identity) {
	if !validIdentity(identity) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.states[identity.CapacityScope()]; state != nil {
		state.consecutiveFailures = 0
		state.nextRetry = time.Time{}
		state.circuitOpenUntil = time.Time{}
		state.halfOpenInFlight = false
	}
}

// Reset clears result-driven blocks for one exact identity after an operator
// has remediated its quota, plan, policy, model, or credentials. It never
// releases a running slot or refunds tokens; callers still pair Acquire with
// Release.
func (c *AdmissionController) Reset(identity provider.Identity) {
	if !validIdentity(identity) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.states[identity.CapacityScope()]; state != nil {
		state.terminalReason = ""
		state.consecutiveFailures = 0
		state.nextRetry = time.Time{}
		state.circuitOpenUntil = time.Time{}
		state.halfOpenInFlight = false
	}
}

func validIdentity(identity provider.Identity) bool {
	// Identity values are normally created by provider.NewIdentity. The zero
	// value remains constructible by callers, so reject it before it can merge
	// unrelated work into the shared "provider:" capacity scope.
	return identity.Hash() != ""
}

func (c *AdmissionController) stateLocked(scope provider.CapacityScope, now time.Time) *admissionState {
	if state := c.states[scope]; state != nil {
		return state
	}
	state := &admissionState{tokens: c.config.TokenCapacity, lastRefill: now}
	c.states[scope] = state
	return state
}

func (c *AdmissionController) refillLocked(state *admissionState, now time.Time) {
	if now.Before(state.lastRefill) {
		return
	}
	state.tokens = math.Min(c.config.TokenCapacity, state.tokens+now.Sub(state.lastRefill).Seconds()*c.config.TokensPerSecond)
	state.lastRefill = now
}

func (c *AdmissionController) jitteredBackoffLocked(failures int) time.Duration {
	capDelay := c.config.BaseBackoff
	for i := 1; i < failures && capDelay < c.config.MaxBackoff; i++ {
		if capDelay > c.config.MaxBackoff/2 {
			capDelay = c.config.MaxBackoff
			break
		}
		capDelay *= 2
	}
	r := c.rand()
	if r < 0 {
		r = 0
	} else if r >= 1 {
		r = math.Nextafter(1, 0)
	}
	return time.Duration(r * float64(capDelay))
}

func retryDecision(reason ErrorClass, retryAt time.Time) Decision {
	retryAt = retryAt.UTC()
	return Decision{Reason: reason, RetryAt: &retryAt, NoFailover: true}
}
