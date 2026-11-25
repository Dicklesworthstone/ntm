package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestOptionalDurationValue_Set(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		input        string
		wantDuration time.Duration
		wantEnabled  bool
		wantErr      bool
	}{
		{
			name:         "empty string uses default",
			input:        "",
			wantDuration: 90 * time.Second,
			wantEnabled:  true,
		},
		{
			name:         "explicit duration",
			input:        "2m",
			wantDuration: 2 * time.Minute,
			wantEnabled:  true,
		},
		{
			name:         "zero disables",
			input:        "0",
			wantDuration: 0,
			wantEnabled:  false,
		},
		{
			name:         "30 seconds",
			input:        "30s",
			wantDuration: 30 * time.Second,
			wantEnabled:  true,
		},
		{
			name:         "5 minutes",
			input:        "5m",
			wantDuration: 5 * time.Minute,
			wantEnabled:  true,
		},
		{
			name:    "over maximum duration rejected",
			input:   "5m1s",
			wantErr: true,
		},
		{
			name:    "invalid duration",
			input:   "invalid",
			wantErr: true,
		},
		{
			name:    "negative duration rejected",
			input:   "-1m",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var duration time.Duration
			var enabled bool
			v := newOptionalDurationValue(90*time.Second, &duration, &enabled)

			err := v.Set(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Set(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if duration != tt.wantDuration {
				t.Errorf("Set(%q) duration = %v, want %v", tt.input, duration, tt.wantDuration)
			}
			if enabled != tt.wantEnabled {
				t.Errorf("Set(%q) enabled = %v, want %v", tt.input, enabled, tt.wantEnabled)
			}
		})
	}
}

func TestOptionalDurationValue_String(t *testing.T) {
	t.Parallel()
	var duration time.Duration
	var enabled bool

	v := newOptionalDurationValue(90*time.Second, &duration, &enabled)

	// Before Set, should return empty
	if got := v.String(); got != "" {
		t.Errorf("String() before Set = %q, want empty", got)
	}

	// After Set, should return the duration
	_ = v.Set("2m")
	if got := v.String(); got != "2m0s" {
		t.Errorf("String() after Set = %q, want %q", got, "2m0s")
	}
}

func TestOptionalDurationValue_NoOptDefVal(t *testing.T) {
	t.Parallel()
	var duration time.Duration
	var enabled bool

	v := newOptionalDurationValue(90*time.Second, &duration, &enabled)

	if got := v.NoOptDefVal(); got != "1m30s" {
		t.Errorf("NoOptDefVal() = %q, want %q", got, "1m30s")
	}
}

func TestSpawnStaggerFlagUsesDocumentedDefault(t *testing.T) {
	t.Parallel()
	flag := newSpawnCmd().Flags().Lookup("stagger")
	if flag == nil {
		t.Fatal("--stagger flag is missing")
	}
	if got, want := flag.NoOptDefVal, "1m30s"; got != want {
		t.Fatalf("--stagger NoOptDefVal = %q, want %q", got, want)
	}
}

func TestValidateSpawnStaggerOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opts    SpawnOptions
		wantErr string
	}{
		{
			name: "legacy zero is disabled",
			opts: SpawnOptions{Stagger: 0},
		},
		{
			name: "legacy maximum is accepted",
			opts: SpawnOptions{Stagger: config.MaxSpawnStaggerDelay},
		},
		{
			name:    "legacy interval above maximum is rejected",
			opts:    SpawnOptions{Stagger: config.MaxSpawnStaggerDelay + time.Second},
			wantErr: "--stagger must be between 0 and 5m0s",
		},
		{
			name:    "fixed interval below zero is rejected",
			opts:    SpawnOptions{StaggerMode: "fixed", StaggerDelay: -time.Second},
			wantErr: "--stagger-delay must be between 0 and 5m0s",
		},
		{
			name: "fixed maximum is accepted",
			opts: SpawnOptions{StaggerMode: "fixed", StaggerDelay: config.MaxSpawnStaggerDelay},
		},
		{
			name: "smart mode is accepted",
			opts: SpawnOptions{StaggerMode: "smart"},
		},
		{
			name:    "unsupported mode is rejected",
			opts:    SpawnOptions{StaggerMode: "adaptive"},
			wantErr: "--stagger-mode must be one of none, fixed, or smart; got \"adaptive\"",
		},
		{
			name:    "fixed interval above maximum is rejected",
			opts:    SpawnOptions{StaggerMode: "fixed", StaggerDelay: config.MaxSpawnStaggerDelay + time.Second},
			wantErr: "--stagger-delay must be between 0 and 5m0s",
		},
		{
			name:    "out-of-range delay is rejected in any mode, like --robot-spawn",
			opts:    SpawnOptions{StaggerMode: "smart", StaggerDelay: -time.Second},
			wantErr: "--stagger-delay must be between 0 and 5m0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSpawnStaggerOptions(tt.opts)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSpawnStaggerOptions() error = %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("validateSpawnStaggerOptions() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestSpawnRejectsInvalidStaggerBeforeLifecycleValidation(t *testing.T) {
	t.Parallel()
	err := spawnSessionLogicComposable(context.Background(), SpawnOptions{
		Session: "invalid session name",
		Stagger: config.MaxSpawnStaggerDelay + time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "--stagger must be between 0 and 5m0s") {
		t.Fatalf("spawnSessionLogicComposable() error = %v, want stagger validation error", err)
	}
}

func TestSpawnRejectsUnsupportedStaggerModeBeforeLifecycleValidation(t *testing.T) {
	t.Parallel()
	err := spawnSessionLogicComposable(context.Background(), SpawnOptions{
		Session:     "invalid session name",
		StaggerMode: "adaptive",
	})
	if err == nil || !strings.Contains(err.Error(), "--stagger-mode must be one of none, fixed, or smart") {
		t.Fatalf("spawnSessionLogicComposable() error = %v, want stagger mode validation error", err)
	}
}

func TestOptionalDurationValue_Type(t *testing.T) {
	t.Parallel()
	var duration time.Duration
	var enabled bool

	v := newOptionalDurationValue(90*time.Second, &duration, &enabled)

	if got := v.Type(); got != "duration" {
		t.Errorf("Type() = %q, want %q", got, "duration")
	}
}

func TestStaggerDelayCalculation(t *testing.T) {
	t.Parallel()
	// Test the stagger delay calculation logic
	stagger := 90 * time.Second

	tests := []struct {
		agentIdx int
		want     time.Duration
	}{
		{0, 0},                 // First agent: no delay
		{1, 90 * time.Second},  // Second: 90s
		{2, 180 * time.Second}, // Third: 180s (3m)
		{3, 270 * time.Second}, // Fourth: 270s (4.5m)
		{4, 360 * time.Second}, // Fifth: 360s (6m)
	}

	for _, tt := range tests {
		got := time.Duration(tt.agentIdx) * stagger
		if got != tt.want {
			t.Errorf("agent %d delay = %v, want %v", tt.agentIdx, got, tt.want)
		}
	}
}

func TestStaggerDelayCalculation_SingleAgent(t *testing.T) {
	t.Parallel()
	// Edge case: single agent should have zero delay
	stagger := 90 * time.Second
	agentIdx := 0 // Only agent

	delay := time.Duration(agentIdx) * stagger
	if delay != 0 {
		t.Errorf("single agent delay = %v, want 0", delay)
	}
}

func TestStaggerDelayCalculation_ZeroStagger(t *testing.T) {
	t.Parallel()
	// Edge case: zero stagger means all agents start immediately
	stagger := time.Duration(0)

	for agentIdx := 0; agentIdx < 10; agentIdx++ {
		delay := time.Duration(agentIdx) * stagger
		if delay != 0 {
			t.Errorf("agent %d with zero stagger delay = %v, want 0", agentIdx, delay)
		}
	}
}

func TestStaggerMaxDelayCalculation(t *testing.T) {
	t.Parallel()
	// Test calculation of maximum delay (for progress display)
	stagger := 90 * time.Second

	tests := []struct {
		numAgents    int
		wantMaxDelay time.Duration
	}{
		{1, 0},                  // Single agent: no delay
		{2, 90 * time.Second},   // 2 agents: last agent (idx 1) at 90s
		{3, 180 * time.Second},  // 3 agents: last agent (idx 2) at 180s
		{5, 360 * time.Second},  // 5 agents: last agent (idx 4) at 360s
		{10, 810 * time.Second}, // 10 agents: last agent (idx 9) at 810s (13.5m)
	}

	for _, tt := range tests {
		var maxDelay time.Duration
		for agentIdx := 0; agentIdx < tt.numAgents; agentIdx++ {
			delay := time.Duration(agentIdx) * stagger
			if delay > maxDelay {
				maxDelay = delay
			}
		}
		if maxDelay != tt.wantMaxDelay {
			t.Errorf("%d agents: maxDelay = %v, want %v", tt.numAgents, maxDelay, tt.wantMaxDelay)
		}
	}
}

func TestStaggerDelayCalculation_CustomIntervals(t *testing.T) {
	t.Parallel()
	// Test various stagger intervals
	tests := []struct {
		stagger  time.Duration
		agentIdx int
		want     time.Duration
	}{
		{30 * time.Second, 3, 90 * time.Second},              // 30s stagger, 4th agent
		{2 * time.Minute, 2, 4 * time.Minute},                // 2m stagger, 3rd agent
		{500 * time.Millisecond, 5, 2500 * time.Millisecond}, // 500ms stagger, 6th agent
		{1 * time.Hour, 1, 1 * time.Hour},                    // 1h stagger, 2nd agent
	}

	for _, tt := range tests {
		got := time.Duration(tt.agentIdx) * tt.stagger
		if got != tt.want {
			t.Errorf("stagger=%v agent=%d: delay = %v, want %v",
				tt.stagger, tt.agentIdx, got, tt.want)
		}
	}
}

func TestOptionalDurationValue_IsBoolFlag(t *testing.T) {
	t.Parallel()
	var duration time.Duration
	var enabled bool

	v := newOptionalDurationValue(90*time.Second, &duration, &enabled)

	if !v.IsBoolFlag() {
		t.Error("IsBoolFlag() = false, want true so --stagger can use NoOptDefVal")
	}
}

func TestOptionalDurationValue_SetMultipleTimes(t *testing.T) {
	t.Parallel()
	var duration time.Duration
	var enabled bool

	v := newOptionalDurationValue(90*time.Second, &duration, &enabled)

	// Set first value
	if err := v.Set("1m"); err != nil {
		t.Fatalf("Set(1m) failed: %v", err)
	}
	if duration != time.Minute {
		t.Errorf("after Set(1m), duration = %v, want 1m", duration)
	}

	// Override with second value
	if err := v.Set("2m"); err != nil {
		t.Fatalf("Set(2m) failed: %v", err)
	}
	if duration != 2*time.Minute {
		t.Errorf("after Set(2m), duration = %v, want 2m", duration)
	}

	// Disable with 0
	if err := v.Set("0"); err != nil {
		t.Fatalf("Set(0) failed: %v", err)
	}
	if enabled {
		t.Error("after Set(0), enabled = true, want false")
	}
}

func TestOptionalDurationValue_StringAfterDisable(t *testing.T) {
	t.Parallel()
	var duration time.Duration
	var enabled bool

	v := newOptionalDurationValue(90*time.Second, &duration, &enabled)

	// Enable then disable
	_ = v.Set("2m")
	_ = v.Set("0")

	// String should be empty when disabled
	if got := v.String(); got != "" {
		t.Errorf("String() after disable = %q, want empty", got)
	}
}

func TestStaggerSpawnOptionsStruct(t *testing.T) {
	t.Parallel()
	// Test that SpawnOptions correctly holds stagger configuration
	opts := SpawnOptions{
		Session:        "test",
		Stagger:        90 * time.Second,
		StaggerEnabled: true,
	}

	if opts.Stagger != 90*time.Second {
		t.Errorf("Stagger = %v, want 90s", opts.Stagger)
	}
	if !opts.StaggerEnabled {
		t.Error("StaggerEnabled = false, want true")
	}

	// Test disabled stagger
	opts2 := SpawnOptions{
		Session:        "test",
		Stagger:        0,
		StaggerEnabled: false,
	}

	if opts2.Stagger != 0 {
		t.Errorf("Stagger = %v, want 0", opts2.Stagger)
	}
	if opts2.StaggerEnabled {
		t.Error("StaggerEnabled = true, want false")
	}
}

func TestStaggerPromptDelayAssignment(t *testing.T) {
	t.Parallel()
	// Simulate the prompt delay assignment logic from spawnSessionLogic
	stagger := 90 * time.Second
	staggerEnabled := true

	type agent struct {
		idx         int
		promptDelay time.Duration
	}

	agents := make([]agent, 5)
	for i := range agents {
		agents[i].idx = i
		if staggerEnabled && stagger > 0 {
			agents[i].promptDelay = time.Duration(i) * stagger
		}
	}

	// Verify delays
	expected := []time.Duration{0, 90 * time.Second, 180 * time.Second, 270 * time.Second, 360 * time.Second}
	for i, a := range agents {
		if a.promptDelay != expected[i] {
			t.Errorf("agent %d: promptDelay = %v, want %v", i, a.promptDelay, expected[i])
		}
	}
}

func TestStaggerDisabledNoDelay(t *testing.T) {
	t.Parallel()
	// When stagger is disabled, all agents should have zero delay
	stagger := 90 * time.Second
	staggerEnabled := false

	for agentIdx := 0; agentIdx < 5; agentIdx++ {
		var promptDelay time.Duration
		if staggerEnabled && stagger > 0 {
			promptDelay = time.Duration(agentIdx) * stagger
		}

		if promptDelay != 0 {
			t.Errorf("agent %d with stagger disabled: delay = %v, want 0", agentIdx, promptDelay)
		}
	}
}

// TestSpawnStaggerRequestResolvesThroughSharedPlanner pins how `ntm spawn`'s
// flags map onto the stagger planner it shares with --robot-spawn: an
// explicit --stagger-mode wins over the legacy --stagger, and the legacy
// interval only applies when it was actually enabled with a positive value.
func TestSpawnStaggerRequestResolvesThroughSharedPlanner(t *testing.T) {
	t.Parallel()
	tracker := ratelimit.NewRateLimitTracker("")
	tracker.RecordRateLimit("anthropic", "spawn")
	learned := tracker.GetOptimalDelay("anthropic")
	claude := []FlatAgent{{Type: AgentTypeClaude, Index: 1}, {Type: AgentTypeClaude, Index: 2}}

	tests := []struct {
		name         string
		opts         SpawnOptions
		wantMode     string
		wantInterval time.Duration
	}{
		{
			name:     "explicit smart overrides legacy flags",
			opts:     SpawnOptions{StaggerMode: "smart", StaggerEnabled: true, Stagger: 90 * time.Second, Agents: claude},
			wantMode: "smart", wantInterval: learned,
		},
		{
			name:     "explicit fixed mode uses the fixed delay",
			opts:     SpawnOptions{StaggerMode: "fixed", StaggerDelay: 20 * time.Second, StaggerEnabled: true, Stagger: 90 * time.Second},
			wantMode: "fixed", wantInterval: 20 * time.Second,
		},
		{
			name:     "legacy fallback when mode none and legacy enabled",
			opts:     SpawnOptions{StaggerMode: "none", StaggerEnabled: true, Stagger: 90 * time.Second},
			wantMode: "legacy", wantInterval: 90 * time.Second,
		},
		{
			name:     "legacy fallback when mode empty and legacy enabled",
			opts:     SpawnOptions{StaggerEnabled: true, Stagger: 45 * time.Second},
			wantMode: "legacy", wantInterval: 45 * time.Second,
		},
		{
			name:     "flag default duration without --stagger stays unpaced",
			opts:     SpawnOptions{StaggerMode: "none", Stagger: 90 * time.Second},
			wantMode: "none",
		},
		{
			name:     "--stagger=0 stays unpaced",
			opts:     SpawnOptions{StaggerMode: "none", StaggerEnabled: true},
			wantMode: "none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := robot.ResolveSpawnStagger(spawnStaggerRequest(tt.opts), spawnStaggerAgentTypes(tt.opts.Agents), tracker)
			if got.Mode != tt.wantMode || got.Interval != tt.wantInterval {
				t.Fatalf("resolved stagger = %+v, want mode %q interval %v", got, tt.wantMode, tt.wantInterval)
			}
			if got.Delay(3) != 3*tt.wantInterval {
				t.Fatalf("4th agent delay = %v, want %v", got.Delay(3), 3*tt.wantInterval)
			}
		})
	}
}

// TestSpawnStaggerAgentTypesFeedSmartProviderSelection pins that the CLI
// hands its concrete agent list to the shared planner, so an omp-only spawn
// uses omp's own bucket while a mixed cc+omp spawn keeps the strictest one.
func TestSpawnStaggerAgentTypesFeedSmartProviderSelection(t *testing.T) {
	t.Parallel()
	tracker := ratelimit.NewRateLimitTracker("")
	for i := 0; i < 3; i++ {
		tracker.RecordRateLimit("anthropic", "spawn")
	}
	anthropic, omp := tracker.GetOptimalDelay("anthropic"), tracker.GetOptimalDelay("omp")
	if anthropic == omp {
		t.Fatalf("control: anthropic backoff (%v) must differ from the omp bucket (%v)", anthropic, omp)
	}
	smart := robot.SpawnStaggerRequest{Mode: "smart"}

	ompOnly := []FlatAgent{{Type: AgentTypeOmp, Index: 1}, {Type: AgentTypeOmp, Index: 2}}
	if got := robot.ResolveSpawnStagger(smart, spawnStaggerAgentTypes(ompOnly), tracker); got.Interval != omp || got.Provider != "omp" {
		t.Fatalf("omp-only smart stagger = %+v, want the omp bucket %v", got, omp)
	}
	mixed := []FlatAgent{{Type: AgentTypeClaude, Index: 1}, {Type: AgentTypeOmp, Index: 1}}
	if got := robot.ResolveSpawnStagger(smart, spawnStaggerAgentTypes(mixed), tracker); got.Interval != anthropic || got.Provider != "anthropic" {
		t.Fatalf("cc+omp smart stagger = %+v, want strictest anthropic %v", got, anthropic)
	}
}

// TestResolveSpawnStaggerDefaultsFlagsWinOverSpawnConfig pins the [spawn]
// precedence both `ntm spawn` and `--robot-spawn` resolve through: an explicit
// flag wins, [spawn] fills what was not given, and an invalid config value is
// reported against its config key.
func TestResolveSpawnStaggerDefaultsFlagsWinOverSpawnConfig(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Spawn.StaggerMode = config.SpawnStaggerFixed
	cfg.Spawn.StaggerDelay = 45 * time.Second

	mode, delay, err := resolveSpawnStaggerDefaults(cfg, "none", false, config.DefaultSpawnStaggerDelay, false)
	if err != nil || mode != "fixed" || delay != 45*time.Second {
		t.Fatalf("config defaults = (%q, %v, %v), want (fixed, 45s, nil)", mode, delay, err)
	}
	mode, delay, err = resolveSpawnStaggerDefaults(cfg, "smart", true, 10*time.Second, true)
	if err != nil || mode != "smart" || delay != 10*time.Second {
		t.Fatalf("explicit flags = (%q, %v, %v), want (smart, 10s, nil)", mode, delay, err)
	}
	mode, delay, err = resolveSpawnStaggerDefaults(cfg, "none", true, config.DefaultSpawnStaggerDelay, false)
	if err != nil || mode != "none" || delay != 45*time.Second {
		t.Fatalf("explicit mode with config delay = (%q, %v, %v), want (none, 45s, nil)", mode, delay, err)
	}
	if mode, delay, err = resolveSpawnStaggerDefaults(nil, "none", false, 30*time.Second, false); err != nil || mode != "none" || delay != 30*time.Second {
		t.Fatalf("nil config = (%q, %v, %v), want flag values unchanged", mode, delay, err)
	}

	bad := config.Default()
	bad.Spawn.StaggerMode = "adaptive"
	if _, _, err := resolveSpawnStaggerDefaults(bad, "none", false, 0, true); err == nil || !strings.Contains(err.Error(), "[spawn] stagger_mode must be one of none, fixed, or smart") {
		t.Fatalf("invalid config mode error = %v", err)
	}
	if _, _, err := resolveSpawnStaggerDefaults(bad, "fixed", true, 0, true); err != nil {
		t.Fatalf("explicit flags must not consult an invalid config value: %v", err)
	}
	bad = config.Default()
	bad.Spawn.StaggerDelay = config.MaxSpawnStaggerDelay + time.Second
	if _, _, err := resolveSpawnStaggerDefaults(bad, "fixed", true, 0, false); err == nil || !strings.Contains(err.Error(), "[spawn] stagger_delay must be between 0 and 5m0s") {
		t.Fatalf("invalid config delay error = %v", err)
	}
}

func TestCodexCooldownRemaining_Once(t *testing.T) {
	t.Parallel()
	tracker := ratelimit.NewRateLimitTracker("")
	tracker.RecordRateLimitWithCooldown("openai", "spawn", 30)

	cooldown, waited := codexCooldownRemaining(tracker, false)
	if cooldown <= 0 {
		t.Fatalf("expected positive cooldown, got %v", cooldown)
	}
	if !waited {
		t.Fatal("expected waited=true after first check")
	}

	cooldown, waited = codexCooldownRemaining(tracker, waited)
	if cooldown != 0 {
		t.Fatalf("expected cooldown=0 after already waited, got %v", cooldown)
	}
	if !waited {
		t.Fatal("expected waited to remain true")
	}
}
