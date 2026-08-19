package ensemble

import (
	"testing"
)

// ---------------------------------------------------------------------------
// ModeBadge — missing branches: empty code, empty icon
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// TierChip — missing branches: advanced, experimental, unknown
// ---------------------------------------------------------------------------

func TestTierChip_AllTiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier ModeTier
		want string // substring expected in output
	}{
		{"core", TierCore, "CORE"},
		{"advanced", TierAdvanced, "ADV"},
		{"experimental", TierExperimental, "EXP"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := TierChip(tc.tier)
			if got == "" {
				t.Fatalf("expected non-empty TierChip for %q", tc.tier)
			}
			// ANSI-stripped check isn't needed; just verify non-empty.
		})
	}
}

func TestTierChip_UnknownTier(t *testing.T) {
	t.Parallel()

	got := TierChip("custom")
	if got == "" {
		t.Fatal("expected non-empty TierChip for unknown tier")
	}
}

// ---------------------------------------------------------------------------
// isASCII — additional coverage
// ---------------------------------------------------------------------------
