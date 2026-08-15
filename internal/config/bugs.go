package config

import "time"

// BugsConfig holds settings for UBS bug-finding push routing
// (`ntm bugs watch`). Routing is opt-in: with PushRouting false the watch
// command refuses to run (unless forced) so agents are never nudged by a
// scanner the operator did not explicitly enable.
//
// BurntSushi/toml decodes time.Duration fields from either nanosecond
// integers or duration strings (e.g. "30s", "5m"), matching the precedent
// set by CoordinatorConfig.
type BugsConfig struct {
	// PushRouting enables routing NEW UBS findings to the agent holding
	// the affected file's reservation. Default false (opt-in).
	PushRouting bool `toml:"push_routing"`

	// Interval is how often `ntm bugs watch` rescans. Default 5m.
	Interval time.Duration `toml:"interval"`

	// CooldownMinutes is the minimum number of minutes between bug nudges
	// delivered to the same pane. Default 10.
	CooldownMinutes int `toml:"cooldown_minutes"`
}

// DefaultBugsConfig returns the built-in [bugs] defaults.
func DefaultBugsConfig() BugsConfig {
	return BugsConfig{
		PushRouting:     false,
		Interval:        5 * time.Minute,
		CooldownMinutes: 10,
	}
}

// EffectiveInterval returns the configured watch interval, falling back to
// the 5m default when the value is unset or non-positive.
func (b BugsConfig) EffectiveInterval() time.Duration {
	if b.Interval <= 0 {
		return 5 * time.Minute
	}
	return b.Interval
}

// EffectiveCooldown returns the per-pane nudge cooldown, falling back to the
// 10m default when the value is unset or non-positive.
func (b BugsConfig) EffectiveCooldown() time.Duration {
	if b.CooldownMinutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(b.CooldownMinutes) * time.Minute
}
