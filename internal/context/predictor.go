// Package context provides context window monitoring for AI agent orchestration.
// predictor.go implements context window exhaustion prediction based on token velocity.
package context

import (
	"sync"
	"time"
)

// PredictorConfig configures the ContextPredictor.
type PredictorConfig struct {
	Window         time.Duration // Velocity averaging window
	PollInterval   time.Duration // Polling interval
	MaxSamples     int           // Maximum samples to retain
	WarnMinutes    float64       // Minutes threshold for warning
	WarnUsage      float64       // Usage threshold for warning
	CompactMinutes float64       // Minutes threshold for compaction
	CompactUsage   float64       // Usage threshold for compaction
	MinSamples     int           // Minimum samples for valid prediction
}

// TokenSample represents a single token count sample at a point in time.
type TokenSample struct {
	Timestamp time.Time
	Tokens    int64
}

// Prediction represents the predicted context window exhaustion.
type Prediction struct {
	CurrentUsage        float64 // Current usage as percentage (0.0-1.0)
	CurrentTokens       int64   // Current token count
	ContextLimit        int64   // Model context limit
	TokenVelocity       float64 // Tokens per minute (positive = growing)
	MinutesToExhaustion float64 // Estimated minutes until exhaustion (0 if stable/decreasing)
	ShouldWarn          bool    // True if warning threshold met
	ShouldCompact       bool    // True if compaction threshold met
	SampleCount         int     // Number of samples used for prediction
	WindowDuration      float64 // Actual window duration in minutes
}

// ContextPredictor tracks token velocity and predicts context exhaustion.
type ContextPredictor struct {
	mu      sync.RWMutex
	samples []TokenSample // Ring buffer of samples
	head    int           // Next write position
	count   int           // Number of valid samples
	config  PredictorConfig
}

// DefaultPredictorConfig returns the default configuration for ContextPredictor.
// Test-only hook: retained because external test packages (tests/e2e) construct
// predictors directly; production receives predictors via wiring.
func DefaultPredictorConfig() PredictorConfig {
	return PredictorConfig{
		Window:         5 * time.Minute,  // Velocity averaging window
		PollInterval:   30 * time.Second, // How often to sample
		MaxSamples:     64,               // Ring buffer size
		WarnMinutes:    15.0,             // Warn when < 15 min to exhaustion
		WarnUsage:      0.70,             // Warn when > 70% usage
		CompactMinutes: 8.0,              // Compact when < 8 min to exhaustion
		CompactUsage:   0.75,             // Compact when > 75% usage
		MinSamples:     3,                // Minimum samples for valid prediction
	}
}

// NewContextPredictor creates a new predictor with the given configuration.
// Test-only hook: see DefaultPredictorConfig.
func NewContextPredictor(cfg PredictorConfig) *ContextPredictor {
	if cfg.MaxSamples <= 0 {
		cfg.MaxSamples = 64
	}
	if cfg.Window <= 0 {
		cfg.Window = 5 * time.Minute
	}
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = 3
	}

	return &ContextPredictor{
		samples: make([]TokenSample, cfg.MaxSamples),
		config:  cfg,
	}
}

// AddSample records a new token count sample.
// This is thread-safe for concurrent access.
func (p *ContextPredictor) AddSample(tokens int64) {
	p.AddSampleAt(tokens, time.Now())
}

// AddSampleAt records a token sample at a specific timestamp.
// Useful for testing and historical data replay.
func (p *ContextPredictor) AddSampleAt(tokens int64, timestamp time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.samples[p.head] = TokenSample{
		Timestamp: timestamp,
		Tokens:    tokens,
	}
	p.head = (p.head + 1) % len(p.samples)
	if p.count < len(p.samples) {
		p.count++
	}
}

// PredictExhaustion calculates the predicted time to context exhaustion.
// Returns nil if insufficient data is available for prediction.
func (p *ContextPredictor) PredictExhaustion(modelLimit int64) *Prediction {
	if modelLimit <= 0 {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.count < p.config.MinSamples {
		return nil
	}

	// Get samples within the window
	windowStart := time.Now().Add(-p.config.Window)
	samples := p.getSamplesInWindow(windowStart)

	if len(samples) < p.config.MinSamples {
		return nil
	}

	// Get latest sample
	latest := samples[len(samples)-1]
	oldest := samples[0]

	// Calculate velocity (tokens per minute)
	velocity := p.calculateVelocityFromSamples(oldest, latest)

	// Calculate usage
	currentUsage := float64(latest.Tokens) / float64(modelLimit)

	// Calculate minutes to exhaustion
	var minutesToExhaustion float64
	if velocity > 0 {
		remaining := modelLimit - latest.Tokens
		if remaining > 0 {
			minutesToExhaustion = float64(remaining) / velocity
		}
	}

	// Determine thresholds
	shouldWarn := minutesToExhaustion > 0 &&
		minutesToExhaustion < p.config.WarnMinutes &&
		currentUsage > p.config.WarnUsage

	shouldCompact := minutesToExhaustion > 0 &&
		minutesToExhaustion < p.config.CompactMinutes &&
		currentUsage > p.config.CompactUsage

	windowDuration := latest.Timestamp.Sub(oldest.Timestamp).Minutes()

	return &Prediction{
		CurrentUsage:        currentUsage,
		CurrentTokens:       latest.Tokens,
		ContextLimit:        modelLimit,
		TokenVelocity:       velocity,
		MinutesToExhaustion: minutesToExhaustion,
		ShouldWarn:          shouldWarn,
		ShouldCompact:       shouldCompact,
		SampleCount:         len(samples),
		WindowDuration:      windowDuration,
	}
}

// getSamplesInWindow returns samples within the time window, ordered oldest to newest.
func (p *ContextPredictor) getSamplesInWindow(windowStart time.Time) []TokenSample {
	result := make([]TokenSample, 0, p.count)

	for i := 0; i < p.count; i++ {
		// Calculate actual index in ring buffer
		idx := (p.head - p.count + i + len(p.samples)) % len(p.samples)
		sample := p.samples[idx]
		if !sample.Timestamp.Before(windowStart) {
			result = append(result, sample)
		}
	}

	return result
}

// calculateVelocityFromSamples calculates tokens per minute between two samples.
func (p *ContextPredictor) calculateVelocityFromSamples(oldest, latest TokenSample) float64 {
	duration := latest.Timestamp.Sub(oldest.Timestamp)
	if duration <= 0 {
		return 0
	}

	tokenDelta := latest.Tokens - oldest.Tokens
	minutes := duration.Minutes()

	if minutes <= 0 {
		return 0
	}

	return float64(tokenDelta) / minutes
}

// Reset clears all samples from the predictor.
func (p *ContextPredictor) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.head = 0
	p.count = 0
	// Zero out samples for GC
	for i := range p.samples {
		p.samples[i] = TokenSample{}
	}
}

// SampleCount returns the current number of samples stored.
func (p *ContextPredictor) SampleCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.count
}

// LatestSample returns the most recent sample, or nil if no samples exist.
func (p *ContextPredictor) LatestSample() *TokenSample {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.count == 0 {
		return nil
	}

	// Latest is at head-1 (with wraparound)
	idx := (p.head - 1 + len(p.samples)) % len(p.samples)
	sample := p.samples[idx]
	return &sample
}

// VelocityTrend returns the recent velocity trend.
// Returns velocity in tokens/minute and whether it's accelerating.
func (p *ContextPredictor) VelocityTrend() (velocity float64, accelerating bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.count < 4 {
		return 0, false
	}

	// Get samples for comparison
	windowStart := time.Now().Add(-p.config.Window)
	samples := p.getSamplesInWindow(windowStart)

	if len(samples) < 4 {
		return 0, false
	}

	// Split into first half and second half
	mid := len(samples) / 2
	firstHalf := samples[:mid]
	secondHalf := samples[mid:]

	// Calculate velocity for each half
	v1 := p.calculateVelocityFromSamples(firstHalf[0], firstHalf[len(firstHalf)-1])
	v2 := p.calculateVelocityFromSamples(secondHalf[0], secondHalf[len(secondHalf)-1])

	// Current velocity is from overall window
	velocity = p.calculateVelocityFromSamples(samples[0], samples[len(samples)-1])
	accelerating = v2 > v1

	return velocity, accelerating
}
