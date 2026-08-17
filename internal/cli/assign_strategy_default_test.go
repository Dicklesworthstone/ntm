package cli

// Default pins for bd-ws2-wire-or-delete-ykmcz.4: the assignment strategy
// flags route through the real graph-aware planners, so their DEFAULT must
// stay "simple" — the honest name for the historical sequential pairing.
// Changing either default silently changes assignment output for users who
// never pass the flag; if you change one intentionally, update the CHANGELOG
// behavior-changes section in the same commit.

import "testing"

func TestDistStrategyFlagDefaultPinnedToSimple(t *testing.T) {
	flag := newSendCmd().Flags().Lookup("dist-strategy")
	if flag == nil {
		t.Fatal("--dist-strategy flag not registered on send command")
	}
	if flag.DefValue != "simple" {
		t.Fatalf("--dist-strategy default = %q, want %q (sequential pairing; see CHANGELOG before changing)", flag.DefValue, "simple")
	}
}

func TestRobotAssignStrategyFlagDefaultPinnedToSimple(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	flag := rootCmd.Flags().Lookup("strategy")
	if flag == nil {
		t.Fatal("--strategy flag not registered on root command")
	}
	if flag.DefValue != "simple" {
		t.Fatalf("--strategy default = %q, want %q (sequential pairing; see CHANGELOG before changing)", flag.DefValue, "simple")
	}
}
