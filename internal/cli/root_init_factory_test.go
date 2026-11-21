package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
)

// TestCLIExplicitFactoryInitialization proves that all CLI subcommands,
// schemas, and kernel commands previously wired via 32 scattered init()
// functions are deterministically registered through initRootFlagsAndSubcommands().
func TestCLIExplicitFactoryInitialization(t *testing.T) {
	// Every previously init()-registered subcommand must exist on rootCmd.
	wantSubcommands := []string{
		"attention",
		"work-snapshot",
		"webhook-outbox",
		"profiles",
		"extract",
		"level",
	}
	subIndex := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		subIndex[cmd.Name()] = true
	}
	for _, name := range wantSubcommands {
		if !subIndex[name] {
			t.Errorf("rootCmd missing expected subcommand %q", name)
		}
	}

	// Sequence flag must be registered explicitly on rootCmd.
	if rootCmd.Flags().Lookup("sequence-expected-position") == nil {
		t.Error("rootCmd missing --sequence-expected-position flag")
	}

	// All 16 CLI-owned kernel registrations must be present in the kernel catalog.
	wantKernel := []string{
		"core.version",
		"core.deps",
		"kernel.list",
		"openapi.generate",
		"sessions.create",
		"sessions.list",
		"sessions.adopt",
		"sessions.health",
		"sessions.view",
		"sessions.zoom",
		"sessions.scale",
		"sessions.rebalance",
		"sessions.coordinator",
		"sessions.controller",
		"sessions.interrupt",
		"prompt.preflight",
	}
	kernelIndex := make(map[string]bool)
	for _, cmd := range kernel.List() {
		kernelIndex[cmd.Name] = true
	}
	for _, name := range wantKernel {
		if !kernelIndex[name] {
			t.Errorf("kernel catalog missing expected command %q", name)
		}
	}
}
