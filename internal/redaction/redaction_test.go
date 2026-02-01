package redaction

import (
	"regexp"
	"strings"
	"testing"
)

func TestAllowlistSuppressesOverlappingLowerPriorityMatches(t *testing.T) {
	ResetPatterns()

	// Construct a key-shaped value at runtime to avoid embedding secret-looking
	// literals in the repo (which can trigger push-protection scanners).
	prefix := "s" + "k" + "-" + "proj" + "-"
	key := prefix + strings.Repeat("a", 40)
	input := "token=" + key

	// Sanity check: input should be detected without allowlist.
	if got := Scan(input, Config{}); len(got) == 0 {
		t.Fatalf("expected findings without allowlist, got none")
	}

	cfg := Config{
		Allowlist: []string{"^" + regexp.QuoteMeta(key) + "$"},
	}
	if got := Scan(input, cfg); len(got) != 0 {
		t.Fatalf("expected no findings with allowlist, got %d: %#v", len(got), got)
	}
}
