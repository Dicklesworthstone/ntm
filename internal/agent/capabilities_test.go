package agent

import (
	"strings"
	"testing"
)

// TestHarnessCapabilityConformanceMatrix12x15 verifies all 12 built-in agent
// harnesses against every required capability dimension so adding or modifying
// a harness cannot silently leave gaps in aliases, badges, providers, graceful
// exit strategies, compaction metadata, or automated delivery/relaunch flags.
func TestHarnessCapabilityConformanceMatrix12x15(t *testing.T) {
	t.Parallel()

	harnesses := AllHarnesses()
	if len(harnesses) != 12 {
		t.Fatalf("AllHarnesses() returned %d harnesses, want 12", len(harnesses))
	}

	seenTypes := make(map[AgentType]bool, len(harnesses))
	seenAliases := make(map[string]AgentType, len(harnesses)*4)
	seenBadges := make(map[string]AgentType, len(harnesses))
	validExits := map[GracefulExitStrategy]bool{
		GracefulExitDoubleInterrupt: true,
		GracefulExitSlashExit:       true,
		GracefulExitEscapeInterrupt: true,
		GracefulExitEscapeClearEOF:  true,
		GracefulExitSingleInterrupt: true,
	}

	for _, h := range harnesses {
		t.Run(string(h.Type), func(t *testing.T) {
			// 1. Canonical uniqueness
			if h.Type == "" {
				t.Fatal("empty canonical Type")
			}
			if seenTypes[h.Type] {
				t.Fatalf("duplicate canonical Type %q", h.Type)
			}
			seenTypes[h.Type] = true

			// 2. Canonical() round-trip
			if got := h.Type.Canonical(); got != h.Type {
				t.Errorf("Canonical(%q) = %q, want %q", h.Type, got, h.Type)
			}

			// 3. IsValid() agrees with registry
			if !h.Type.IsValid() {
				t.Errorf("%q.IsValid() = false, want true", h.Type)
			}

			// 4. DisplayName populated and agrees with method
			if h.DisplayName == "" || h.Type.DisplayName() != h.DisplayName {
				t.Errorf("DisplayName mismatch: registry=%q method=%q", h.DisplayName, h.Type.DisplayName())
			}

			// 5. ProfileName populated and agrees with method
			if h.ProfileName == "" || h.Type.ProfileName() != h.ProfileName {
				t.Errorf("ProfileName mismatch: registry=%q method=%q", h.ProfileName, h.Type.ProfileName())
			}

			// 6. Unique ShortBadge
			if h.ShortBadge == "" {
				t.Error("empty ShortBadge")
			} else if prev, dup := seenBadges[h.ShortBadge]; dup {
				t.Errorf("ShortBadge %q collides between %q and %q", h.ShortBadge, prev, h.Type)
			} else {
				seenBadges[h.ShortBadge] = h.Type
			}

			// 7. Provider populated
			if h.Provider == "" {
				t.Error("empty Provider")
			}

			// 8. NeedsDoubleEnter agrees with method
			if h.Type.NeedsDoubleEnter() != h.NeedsDoubleEnter {
				t.Errorf("NeedsDoubleEnter mismatch: registry=%v method=%v", h.NeedsDoubleEnter, h.Type.NeedsDoubleEnter())
			}

			// 9. GracefulExit is valid
			if !validExits[h.GracefulExit] {
				t.Errorf("invalid GracefulExit %q", h.GracefulExit)
			}

			// 10. Builtin compact command consistency
			if h.SupportsBuiltinCompact && !strings.HasPrefix(h.BuiltinCompactCommand, "/") {
				t.Errorf("SupportsBuiltinCompact=true but BuiltinCompactCommand=%q", h.BuiltinCompactCommand)
			}
			if !h.SupportsBuiltinCompact && h.BuiltinCompactCommand != "" {
				t.Errorf("SupportsBuiltinCompact=false but BuiltinCompactCommand=%q", h.BuiltinCompactCommand)
			}

			// 11. History clear command consistency
			if h.SupportsHistoryClear && !strings.HasPrefix(h.HistoryClearCommand, "/") {
				t.Errorf("SupportsHistoryClear=true but HistoryClearCommand=%q", h.HistoryClearCommand)
			}
			if !h.SupportsHistoryClear && h.HistoryClearCommand != "" {
				t.Errorf("SupportsHistoryClear=false but HistoryClearCommand=%q", h.HistoryClearCommand)
			}

			// 12. Positive DefaultContextWindow
			if h.DefaultContextWindow <= 0 {
				t.Errorf("DefaultContextWindow = %d, want > 0", h.DefaultContextWindow)
			}

			// 13. Automated delivery & relaunch capabilities
			if !h.SupportsAutomatedDelivery || !h.SupportsAutomatedRelaunch {
				t.Errorf("expected automated delivery & relaunch support for built-in harness %q", h.Type)
			}

			// 14. Alias round-trip & collision check
			if len(h.Aliases) == 0 {
				t.Error("expected at least 1 alias")
			}
			for _, alias := range h.Aliases {
				norm := strings.ToLower(strings.TrimSpace(alias))
				if prev, dup := seenAliases[norm]; dup && prev != h.Type {
					t.Errorf("alias %q collides between %q and %q", alias, prev, h.Type)
				}
				seenAliases[norm] = h.Type
				if got := AgentType(alias).Canonical(); got != h.Type {
					t.Errorf("AgentType(%q).Canonical() = %q, want %q", alias, got, h.Type)
				}
				if looked, ok := LookupHarness(AgentType(alias)); !ok || looked.Type != h.Type {
					t.Errorf("LookupHarness(%q) = (%q, %v), want (%q, true)", alias, looked.Type, ok, h.Type)
				}
			}

			// 15. LookupHarness returns defensive copy (mutating Aliases cannot poison registry)
			looked, ok := LookupHarness(h.Type)
			if !ok {
				t.Fatalf("LookupHarness(%q) returned false", h.Type)
			}
			if len(looked.Aliases) > 0 {
				looked.Aliases[0] = "MUTATED"
				fresh, _ := LookupHarness(h.Type)
				if fresh.Aliases[0] == "MUTATED" {
					t.Error("LookupHarness returned uncopied Aliases slice")
				}
			}
		})
	}

	// Spot-check key parity invariants from Track 2.1:
	// - Antigravity shares Google provider + Gemini CAAM tool + escape_interrupt exit + /clear
	agy := CapabilitiesFor(AgentTypeAntigravity)
	if agy.Provider != "google" || agy.CAAMTool != "gemini" || agy.GracefulExit != GracefulExitEscapeInterrupt || !agy.SupportsHistoryClear {
		t.Errorf("unexpected Antigravity capabilities: %+v", agy)
	}
	// - Pi is distinct from OMP and first-class in the 12-harness registry
	pi := CapabilitiesFor(AgentType("pi-agent"))
	if pi.Type != AgentTypePi || pi.Type == AgentTypeOmp || !pi.ComposerGated {
		t.Errorf("unexpected Pi capabilities: %+v", pi)
	}
	// - Grok has xai provider and automated delivery/relaunch enabled
	grok := CapabilitiesFor(AgentTypeGrok)
	if grok.Provider != "xai" || !grok.ComposerGated || !grok.SupportsAutomatedRelaunch {
		t.Errorf("unexpected Grok capabilities: %+v", grok)
	}
}
