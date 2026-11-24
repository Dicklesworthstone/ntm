package agent_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// TestHarnessConformanceMatrix12x15CrossSubsystem verifies all 12 built-in
// harnesses across all 15 operational capability dimensions by executing real
// cross-subsystem APIs (spawn/restore templates, alias normalization, title
// detection, composer gate, double-enter submission, graceful exit, parser
// readiness, rate-limit provider & detection, context window, compaction,
// automated relaunch, quota provider, CAAM seat selection, save/restore
// command resolution, and badge/palette/robot discovery), and asserts that
// undeclared/unknown harnesses fail closed.
func TestHarnessConformanceMatrix12x15CrossSubsystem(t *testing.T) {
	t.Parallel()

	harnesses := agent.AllHarnesses()
	if len(harnesses) != 12 {
		t.Fatalf("AllHarnesses() = %d, want 12", len(harnesses))
	}

	expectedTypes := []agent.AgentType{
		agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeAntigravity,
		agent.AgentTypeGrok,
		agent.AgentTypeOpencode,
		agent.AgentTypeOmp,
		agent.AgentTypePi,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOllama,
	}
	if len(expectedTypes) != len(harnesses) {
		t.Fatalf("expected %d canonical harnesses, got %d", len(expectedTypes), len(harnesses))
	}

	cfg := config.Default()
	state := &session.SessionState{Name: "matrix-sess", WorkDir: "/tmp/matrix-work"}
	restoreCmds := session.RestoreAgentCommands(state, cfg)
	parser := agent.NewParser()
	compactor := ntmctx.NewCompactor(nil, ntmctx.DefaultCompactorConfig())
	palette := theme.Current().Semantic()
	iconSet := icons.Current()

	capsOut, err := robot.GetCapabilities()
	if err != nil {
		t.Fatalf("robot.GetCapabilities() error = %v", err)
	}
	robotHarnessByType := make(map[string]robot.HarnessCapabilitySummary, len(capsOut.Harnesses))
	for _, rh := range capsOut.Harnesses {
		robotHarnessByType[rh.Type] = rh
	}
	if len(robotHarnessByType) != 12 {
		t.Fatalf("robot capabilities exposed %d harnesses, want 12", len(robotHarnessByType))
	}

	restoreCmdFor := func(typ agent.AgentType) string {
		switch typ {
		case agent.AgentTypeClaudeCode:
			return restoreCmds.Claude
		case agent.AgentTypeCodex:
			return restoreCmds.Codex
		case agent.AgentTypeGemini:
			return restoreCmds.Gemini
		case agent.AgentTypeAntigravity:
			return restoreCmds.Antigravity
		case agent.AgentTypeGrok:
			return restoreCmds.Grok
		case agent.AgentTypeOpencode:
			return restoreCmds.Opencode
		case agent.AgentTypeOmp:
			return restoreCmds.Omp
		case agent.AgentTypePi:
			return restoreCmds.Pi
		case agent.AgentTypeCursor:
			return restoreCmds.Cursor
		case agent.AgentTypeWindsurf:
			return restoreCmds.Windsurf
		case agent.AgentTypeAider:
			return restoreCmds.Aider
		case agent.AgentTypeOllama:
			return restoreCmds.Ollama
		default:
			return ""
		}
	}

	validExits := map[agent.GracefulExitStrategy]bool{
		agent.GracefulExitDoubleInterrupt: true,
		agent.GracefulExitSlashExit:       true,
		agent.GracefulExitEscapeInterrupt: true,
		agent.GracefulExitEscapeClearEOF:  true,
		agent.GracefulExitSingleInterrupt: true,
	}

	seenBadges := make(map[string]agent.AgentType, 12)
	for _, h := range harnesses {
		h := h
		if prev, dup := seenBadges[h.ShortBadge]; dup {
			t.Fatalf("ShortBadge %q collided between %q and %q", h.ShortBadge, prev, h.Type)
		}
		seenBadges[h.ShortBadge] = h.Type

		t.Run(string(h.Type), func(t *testing.T) {
			t.Parallel()

			// 1. Spawn / launch command template resolves non-vacuously.
			tmpl, resolvedType, ok := session.RestoreAgentTemplate(cfg, string(h.Type))
			if !ok || strings.TrimSpace(tmpl) == "" || resolvedType == "" {
				t.Errorf("[1:spawn] RestoreAgentTemplate(%q) = (%q, %q, %v), want non-empty template", h.Type, tmpl, resolvedType, ok)
			}

			// 2. Add/scale alias normalization agrees across agent, tmux, and robot.
			for _, alias := range h.Aliases {
				if got := agent.AgentType(alias).Canonical(); got != h.Type {
					t.Errorf("[2:alias] agent.Canonical(%q) = %q, want %q", alias, got, h.Type)
				}
				if got := tmux.AgentType(alias).Canonical(); string(got) != string(h.Type) {
					t.Errorf("[2:alias] tmux.Canonical(%q) = %q, want %q", alias, got, h.Type)
				}
				if got := robot.ResolveAgentType(alias); got == "" || got == "unknown" || got == "user" {
					t.Errorf("[2:alias] robot.ResolveAgentType(%q) = %q, want known harness", alias, got)
				}
			}

			// 3. Adopt / pane-title detection recognizes canonical title format.
			paneTitle := fmt.Sprintf("matrix__%s_1", h.Type)
			if detected := assign.DetectAgentTypeFromTitle(paneTitle); detected == "unknown" || detected == "user" {
				t.Errorf("[3:adopt] DetectAgentTypeFromTitle(%q) = %q, want known harness", paneTitle, detected)
			}

			// 4. Composer ready gate matches declared registry contract in robot discovery.
			rh, hasRobot := robotHarnessByType[string(h.Type)]
			if !hasRobot {
				t.Fatalf("[4:composer] harness %q missing from robot.GetCapabilities()", h.Type)
			}
			if rh.ComposerGated != h.ComposerGated {
				t.Errorf("[4:composer] robot ComposerGated=%v != registry=%v", rh.ComposerGated, h.ComposerGated)
			}

			// 5. Submission verification (NeedsDoubleEnter) agrees across agent, tmux, robot.
			if h.Type.NeedsDoubleEnter() != h.NeedsDoubleEnter || tmux.AgentType(h.Type).NeedsDoubleEnter() != h.NeedsDoubleEnter || rh.NeedsDoubleEnter != h.NeedsDoubleEnter {
				t.Errorf("[5:submit] NeedsDoubleEnter drift: registry=%v agent=%v tmux=%v robot=%v",
					h.NeedsDoubleEnter, h.Type.NeedsDoubleEnter(), tmux.AgentType(h.Type).NeedsDoubleEnter(), rh.NeedsDoubleEnter)
			}

			// 6. Interrupt / graceful exit strategy is valid and matches robot discovery.
			if !validExits[h.GracefulExit] || rh.GracefulExit != string(h.GracefulExit) {
				t.Errorf("[6:interrupt] invalid/drifted GracefulExit: registry=%q robot=%q", h.GracefulExit, rh.GracefulExit)
			}

			// 7. Activity / readiness parser executes non-vacuously with hint.
			parsed, err := parser.ParseWithHint("Ready.\n> \n", h.Type)
			if err != nil || parsed == nil || parsed.Type != h.Type {
				t.Errorf("[7:activity] ParseWithHint(%q) = (%+v, %v), want Type=%q", h.Type, parsed, err, h.Type)
			}

			// 8. Rate-limit provider normalization & detection work for this harness.
			if gotProv := ratelimit.NormalizeProvider(string(h.Type)); gotProv != h.Provider {
				t.Errorf("[8:rate-limit] NormalizeProvider(%q) = %q, want %q", h.Type, gotProv, h.Provider)
			}
			rl := ratelimit.DetectRateLimitForAgent("Error 429: rate limit exceeded. Please try again in 45 seconds.", string(h.Type))
			if !rl.RateLimited {
				t.Errorf("[8:rate-limit] DetectRateLimitForAgent(%q) failed to detect explicit 429 banner", h.Type)
			}

			// 9. Context usage window is positive and agrees with robot discovery.
			if h.DefaultContextWindow <= 0 || rh.DefaultContextWindow != h.DefaultContextWindow {
				t.Errorf("[9:context] DefaultContextWindow registry=%d robot=%d", h.DefaultContextWindow, rh.DefaultContextWindow)
			}

			// 10. Compaction capabilities & commands agree with internal/context.
			ctxCaps := ntmctx.GetAgentCapabilities(string(h.Type))
			if ctxCaps.SupportsBuiltinCompact != h.SupportsBuiltinCompact ||
				ctxCaps.BuiltinCompactCommand != h.BuiltinCompactCommand ||
				ctxCaps.SupportsHistoryClear != h.SupportsHistoryClear ||
				ctxCaps.HistoryClearCommand != h.HistoryClearCommand {
				t.Errorf("[10:compaction] context capabilities drift: got=%+v want=%+v", ctxCaps, h)
			}
			compactCmds := compactor.GetCompactionCommands(string(h.Type))
			if h.SupportsBuiltinCompact && len(compactCmds) == 0 {
				t.Errorf("[10:compaction] SupportsBuiltinCompact=true but GetCompactionCommands returned empty")
			}
			if !h.SupportsBuiltinCompact && len(compactCmds) != 0 {
				t.Errorf("[10:compaction] SupportsBuiltinCompact=false but GetCompactionCommands returned %+v", compactCmds)
			}

			// 11. Rotation & automated relaunch capability declared consistently.
			if !h.SupportsAutomatedRelaunch || !rh.SupportsAutomatedRelaunch || !h.SupportsAutomatedDelivery || !rh.SupportsAutomatedDelivery {
				t.Errorf("[11:rotation] automated delivery/relaunch drift: registry=(%v,%v) robot=(%v,%v)",
					h.SupportsAutomatedDelivery, h.SupportsAutomatedRelaunch, rh.SupportsAutomatedDelivery, rh.SupportsAutomatedRelaunch)
			}

			// 12. Quota / provider mapping is non-empty and canonical.
			if h.Provider == "" || ratelimit.NormalizeProvider(h.Provider) != h.Provider || rh.Provider != h.Provider {
				t.Errorf("[12:quota] Provider drift: registry=%q normalized=%q robot=%q", h.Provider, ratelimit.NormalizeProvider(h.Provider), rh.Provider)
			}

			// 13. CAAM seat selection fails closed for non-subscription harnesses and agrees on claude/codex.
			seatProv := session.CAAMSeatProviderForAgentType(string(h.Type))
			switch h.Type {
			case agent.AgentTypeClaudeCode:
				if seatProv != "claude" || h.CAAMTool != "claude" {
					t.Errorf("[13:caam] claude seatProv=%q CAAMTool=%q", seatProv, h.CAAMTool)
				}
			case agent.AgentTypeCodex:
				if seatProv != "codex" || h.CAAMTool != "codex" {
					t.Errorf("[13:caam] codex seatProv=%q CAAMTool=%q", seatProv, h.CAAMTool)
				}
			case agent.AgentTypeGemini, agent.AgentTypeAntigravity:
				if seatProv != "" || h.CAAMTool != "gemini" {
					t.Errorf("[13:caam] %s seatProv=%q CAAMTool=%q, want seatProv=\"\" CAAMTool=\"gemini\"", h.Type, seatProv, h.CAAMTool)
				}
			default:
				if seatProv != "" || h.CAAMTool != "" {
					t.Errorf("[13:caam] %s expected no CAAM binding, got seatProv=%q CAAMTool=%q", h.Type, seatProv, h.CAAMTool)
				}
			}

			// 14. Save / restore command resolution succeeds for every built-in harness.
			if cmd := strings.TrimSpace(restoreCmdFor(h.Type)); cmd == "" {
				t.Errorf("[14:save-restore] RestoreAgentCommands returned empty command for %q", h.Type)
			}

			// 15. Badge, semantic palette color, and icon are non-empty and distinct from AgentUnknown.
			if h.ShortBadge == "" {
				t.Errorf("[15:badge] empty ShortBadge for %q", h.Type)
			}
			if color := palette.AgentColor(string(h.Type)); color == "" || color == palette.AgentUnknown {
				t.Errorf("[15:palette] AgentColor(%q) = %q, fell through to AgentUnknown (%q)", h.Type, color, palette.AgentUnknown)
			}
			if icon := iconSet.AgentIcon(string(h.Type)); icon == "" {
				t.Errorf("[15:icon] AgentIcon(%q) returned empty", h.Type)
			}
		})
	}

	// Undeclared / unknown harness MUST fail closed across every capability gate.
	t.Run("undeclared_harness_fails_closed", func(t *testing.T) {
		t.Parallel()
		const bogus = agent.AgentType("nonexistent-harness-xyz")
		if _, ok := agent.LookupHarness(bogus); ok {
			t.Fatal("LookupHarness succeeded for undeclared harness")
		}
		if bogus.IsValid() {
			t.Fatal("undeclared harness reported IsValid()=true")
		}
		if got := bogus.DisplayName(); got != "Unknown" {
			t.Fatalf("undeclared DisplayName() = %q, want Unknown", got)
		}
		if resolved := robot.ResolveAgentType(string(bogus)); agent.AgentType(resolved).IsValid() {
			t.Fatalf("robot.ResolveAgentType(bogus) = %q unexpectedly resolved to valid built-in harness", resolved)
		}
		if resolved := robot.ResolveAgentType("pi-agent"); resolved != "pi" {
			t.Fatalf("robot.ResolveAgentType(\"pi-agent\") = %q, want \"pi\"", resolved)
		}
		if _, _, ok := session.RestoreAgentTemplate(cfg, string(bogus)); ok {
			t.Fatal("RestoreAgentTemplate succeeded for undeclared harness")
		}
		if seat := session.CAAMSeatProviderForAgentType(string(bogus)); seat != "" {
			t.Fatalf("CAAMSeatProviderForAgentType(bogus) = %q, want empty", seat)
		}
		if cmds := compactor.GetCompactionCommands(string(bogus)); len(cmds) != 0 {
			t.Fatalf("GetCompactionCommands(bogus) = %+v, want empty", cmds)
		}
		if caps := agent.CapabilitiesFor(bogus); caps.SupportsAutomatedDelivery || caps.SupportsAutomatedRelaunch || caps.ComposerGated {
			t.Fatalf("CapabilitiesFor(bogus) failed open: %+v", caps)
		}
		if color := palette.AgentColor(string(bogus)); color != palette.AgentUnknown {
			t.Fatalf("AgentColor(bogus) = %q, want AgentUnknown (%q)", color, palette.AgentUnknown)
		}
		if detected := assign.DetectAgentTypeFromTitle("matrix__nonexistent-harness-xyz_1"); detected != "unknown" {
			t.Fatalf("DetectAgentTypeFromTitle(bogus) = %q, want unknown", detected)
		}
	})
}
