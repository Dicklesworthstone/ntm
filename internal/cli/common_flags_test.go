package cli

import (
	"testing"
	"time"
)

// TestCommonFlagsConsistency verifies that common flags are defined consistently
// across all ntm assign-related commands.
// Related bead: bd-7gyss

// TestNoColorFlagExists verifies the --no-color global flag exists
func TestNoColorFlagExists(t *testing.T) {
	cmd := rootCmd

	// Check that --no-color is a persistent flag on root command
	flag := cmd.PersistentFlags().Lookup("no-color")
	if flag == nil {
		t.Fatal("--no-color persistent flag not found on root command")
	}

	if flag.DefValue != "false" {
		t.Errorf("--no-color default value = %q, want %q", flag.DefValue, "false")
	}
}

// TestJSONFlagIsPersistent verifies --json is inherited by all commands
func TestJSONFlagIsPersistent(t *testing.T) {
	cmd := rootCmd

	// Check that --json is a persistent flag on root command
	flag := cmd.PersistentFlags().Lookup("json")
	if flag == nil {
		t.Fatal("--json persistent flag not found on root command")
	}

	if flag.DefValue != "false" {
		t.Errorf("--json default value = %q, want %q", flag.DefValue, "false")
	}
}

// TestAssignCommonFlags verifies that the assign command has all common flags
func TestAssignCommonFlags(t *testing.T) {
	cmd := newAssignCmd()

	tests := []struct {
		name     string
		flag     string
		defValue string
	}{
		{"verbose", "verbose", "false"},
		{"quiet", "quiet", "false"},
		{"timeout", "timeout", "30s"},
		{"agent type filter", "agent", ""},
		{"cc-only", "cc-only", "false"},
		{"cod-only", "cod-only", "false"},
		{"gmi-only", "gmi-only", "false"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flag := cmd.Flags().Lookup(tc.flag)
			if flag == nil {
				t.Fatalf("--%s flag not found on assign command", tc.flag)
			}
			if flag.DefValue != tc.defValue {
				t.Errorf("--%s default value = %q, want %q", tc.flag, flag.DefValue, tc.defValue)
			}
		})
	}
}

// TestSpawnAssignFlags verifies that spawn --assign has the necessary flags
func TestSpawnAssignFlags(t *testing.T) {
	cmd := newSpawnCmd()

	tests := []struct {
		name     string
		flag     string
		defValue string
	}{
		{"assign", "assign", "false"},
		{"strategy", "strategy", "balanced"},
		{"limit", "limit", "0"},
		{"ready-timeout", "ready-timeout", "1m0s"},
		{"assign-verbose", "assign-verbose", "false"},
		{"assign-quiet", "assign-quiet", "false"},
		{"assign-timeout", "assign-timeout", "30s"},
		{"assign-agent", "assign-agent", ""},
		{"assign-cc-only", "assign-cc-only", "false"},
		{"assign-cod-only", "assign-cod-only", "false"},
		{"assign-gmi-only", "assign-gmi-only", "false"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flag := cmd.Flags().Lookup(tc.flag)
			if flag == nil {
				t.Fatalf("--%s flag not found on spawn command", tc.flag)
			}
			if flag.DefValue != tc.defValue {
				t.Errorf("--%s default value = %q, want %q", tc.flag, flag.DefValue, tc.defValue)
			}
		})
	}
}

// TestAgentTypeFilterEquivalence verifies that both --cc-only and --agent=claude work
func TestAgentTypeFilterEquivalence(t *testing.T) {
	tests := []struct {
		ccOnly  bool
		codOnly bool
		gmiOnly bool
		agent   string
		want    string
	}{
		{false, false, false, "", ""},
		{true, false, false, "", "claude"},
		{false, true, false, "", "codex"},
		{false, false, true, "", "gemini"},
		{false, false, false, "claude", "claude"},
		{false, false, false, "CC", "claude"},
		{false, false, false, "codex", "codex"},
		{false, false, false, "codex-cli", "codex"},
		{false, false, false, "gemini", "gemini"},
		{false, false, false, " google-gemini ", "gemini"},
		// --agent flag takes precedence
		{true, false, false, "codex", "codex"},
	}

	for _, tc := range tests {
		// Save original values
		origCCOnly := assignCCOnly
		origCodOnly := assignCodOnly
		origGmiOnly := assignGmiOnly
		origAgentType := assignAgentType

		// Set test values
		assignCCOnly = tc.ccOnly
		assignCodOnly = tc.codOnly
		assignGmiOnly = tc.gmiOnly
		assignAgentType = tc.agent

		got := resolveAgentTypeFilter()
		if got != tc.want {
			t.Errorf("resolveAgentTypeFilter(cc=%v, cod=%v, gmi=%v, agent=%q) = %q, want %q",
				tc.ccOnly, tc.codOnly, tc.gmiOnly, tc.agent, got, tc.want)
		}

		// Restore original values
		assignCCOnly = origCCOnly
		assignCodOnly = origCodOnly
		assignGmiOnly = origGmiOnly
		assignAgentType = origAgentType
	}
}

// TestSpawnAssignAgentTypeAliases verifies spawn --assign agent filter aliases match assign behavior.
func TestSpawnAssignAgentTypeAliases(t *testing.T) {
	tests := []struct {
		ccOnly  bool
		codOnly bool
		gmiOnly bool
		agent   string
		want    string
	}{
		{false, false, false, "", ""},
		{true, false, false, "", "claude"},
		{false, true, false, "", "codex"},
		{false, false, true, "", "gemini"},
		{false, false, false, "claude", "claude"},
		{false, false, false, "CC", "claude"},
		{false, false, false, "Codex", "codex"},
		{false, false, false, "codex-cli", "codex"},
		{false, false, false, "GEMINI", "gemini"},
		{false, false, false, " google-gemini ", "gemini"},
		// --assign-agent should take precedence
		{true, false, false, "codex", "codex"},
	}

	for _, tc := range tests {
		got := resolveSpawnAssignAgentType(tc.agent, tc.ccOnly, tc.codOnly, tc.gmiOnly)
		if got != tc.want {
			t.Errorf("resolveSpawnAssignAgentType(agent=%q, cc=%v, cod=%v, gmi=%v) = %q, want %q",
				tc.agent, tc.ccOnly, tc.codOnly, tc.gmiOnly, got, tc.want)
		}
	}
}

// TestSpawnOptionsAssignCommonFields verifies that SpawnOptions has all common assignment fields
func TestSpawnOptionsAssignCommonFields(t *testing.T) {
	opts := SpawnOptions{
		Assign:             true,
		AssignStrategy:     "quality",
		AssignLimit:        5,
		AssignReadyTimeout: 2 * time.Minute,
		AssignVerbose:      true,
		AssignQuiet:        false,
		AssignTimeout:      45 * time.Second,
		AssignAgentType:    "claude",
	}

	// Verify all fields are set correctly
	if !opts.Assign {
		t.Error("Assign field not set")
	}
	if opts.AssignStrategy != "quality" {
		t.Errorf("AssignStrategy = %q, want %q", opts.AssignStrategy, "quality")
	}
	if opts.AssignLimit != 5 {
		t.Errorf("AssignLimit = %d, want %d", opts.AssignLimit, 5)
	}
	if opts.AssignReadyTimeout != 2*time.Minute {
		t.Errorf("AssignReadyTimeout = %v, want %v", opts.AssignReadyTimeout, 2*time.Minute)
	}
	if !opts.AssignVerbose {
		t.Error("AssignVerbose field not set")
	}
	if opts.AssignQuiet {
		t.Error("AssignQuiet should be false")
	}
	if opts.AssignTimeout != 45*time.Second {
		t.Errorf("AssignTimeout = %v, want %v", opts.AssignTimeout, 45*time.Second)
	}
	if opts.AssignAgentType != "claude" {
		t.Errorf("AssignAgentType = %q, want %q", opts.AssignAgentType, "claude")
	}
}
