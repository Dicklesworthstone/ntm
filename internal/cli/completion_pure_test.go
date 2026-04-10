package cli

import (
	"testing"
)

// ---------------------------------------------------------------------------
// completeCommaSeparated — 0% → 100%
// ---------------------------------------------------------------------------

func TestCompleteCommaSeparated(t *testing.T) {

	options := []string{"cc", "cod", "gmi"}

	tests := []struct {
		name       string
		toComplete string
		wantLen    int
	}{
		{"empty", "", 3},
		{"prefix_c", "c", 2},
		{"after_comma", "cc,", 3},
		{"after_comma_prefix", "cc,g", 1},
		{"two_commas", "cc,cod,", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := completeCommaSeparated(options, tt.toComplete)
			if len(got) != tt.wantLen {
				t.Errorf("completeCommaSeparated(%v, %q) = %v (len %d), want len %d",
					options, tt.toComplete, got, len(got), tt.wantLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// prefixMatches — 0% → 100%
// ---------------------------------------------------------------------------

func TestPrefixMatches(t *testing.T) {

	options := []string{"alpha", "beta", "gamma"}

	tests := []struct {
		name    string
		prefix  string
		segment string
		wantLen int
	}{
		{"empty_segment", "pre:", "", 3},
		{"match_a", "", "a", 1},
		{"match_b", "x,", "b", 1},
		{"no_match", "", "z", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prefixMatches(options, tt.prefix, tt.segment)
			if len(got) != tt.wantLen {
				t.Errorf("prefixMatches(%v, %q, %q) = %v (len %d), want len %d",
					options, tt.prefix, tt.segment, got, len(got), tt.wantLen)
			}
			// Verify prefix is applied
			for _, item := range got {
				if tt.prefix != "" && len(item) > 0 && item[:len(tt.prefix)] != tt.prefix {
					t.Errorf("result %q missing prefix %q", item, tt.prefix)
				}
			}
		})
	}
}
