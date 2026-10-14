package status

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// The parser drives restart recommendations; DetectIdleFromOutput is also
// called directly by health checks and swarm startup. A persistent composer
// must not make either consumer authorize new work during a live turn.
func TestCodexLiveReadinessAgreesAcrossConsumers(t *testing.T) {
	const composer = "› Ask Codex to do anything\n47% context left\n"
	cases := []struct {
		name, output string
		working      bool
	}{
		{"working above composer", "• Working (12s • esc to interrupt)\n" + composer, true},
		{"thinking above composer", "◐ Thinking\n" + composer, true},
		{"background task", "• Waiting for background terminal\n" + composer, true},
		{"interrupt without spinner", "Investigating alternatives (esc to interrupt)\n" + composer, true},
		{"narrow unicode hint", "Investigating (esc to inte…)\n" + composer, true},
		{"narrow ascii hint", "Investigating (esc to inte...)\n" + composer, true},
		{"wrapped hint", "Investigating (esc to\ninterrupt)\n" + composer, true},
		{"marker below composer", composer + "• Working (esc to interrupt)\n", true},
		{"ultra chevron", "• Working\n» Ask Codex to do anything\n47% context left\n", true},
		{"ansi", "\x1b[32m• Thinking\x1b[0m\n" + composer, true},
		{"repairing tool error", "Error: command failed:\n• Working (esc to interrupt)\n" + composer, true},
		{"idle", composer, false},
		{"finished transcript verbs", "Finished reading and patching the files.\n" + composer, false},
		{"old spinner", "• Working (esc to interrupt)\n" + strings.Repeat("historical output\n", 20) + composer, false},
	}
	for _, tc := range cases {
		for _, hint := range []agent.AgentType{"cod", "codex", " CODEX-CLI ", "openai_codex"} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, hint), func(t *testing.T) {
				parsed, err := agent.NewParser().ParseWithHint(tc.output, hint)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.IsWorking != tc.working || parsed.IsIdle == tc.working || parsed.IsRateLimited || parsed.IsInError {
					t.Fatalf("incorrect live-turn flags: %+v", parsed)
				}
				if idle := DetectIdleFromOutput(tc.output, string(hint)); idle == tc.working {
					t.Fatalf("direct idle=%v, parser working=%v", idle, parsed.IsWorking)
				}
				want := agent.RecommendSafeToRestart
				if tc.working {
					want = agent.RecommendDoNotInterrupt
					found := false
					for _, indicator := range parsed.WorkIndicators {
						found = found || indicator == "codex_live_activity"
					}
					if !found {
						t.Fatalf("live work has no diagnostic indicator: %v", parsed.WorkIndicators)
					}
				}
				if got := parsed.GetRecommendation(); got != want {
					t.Fatalf("recommendation=%s, want %s", got, want)
				}
				if parsed.ContextRemaining == nil || *parsed.ContextRemaining != 47 {
					t.Fatal("readiness lost the existing context metric")
				}
			})
		}
	}
}

func TestAntigravityLiveWorkVetoesDirectIdle(t *testing.T) {
	const prompt = ">\n? for shortcuts\n"
	for _, hint := range []agent.AgentType{"agy", "antigravity", " ANTIGRAVITY-CLI "} {
		for _, tc := range []struct {
			name, output string
			working      bool
		}{
			{"above composer", "Reviewing alternatives (esc to cancel)\n" + prompt, true},
			{"below composer", prompt + "esc to cancel\n", true},
			{"ansi", "\x1b[32mesc to cancel\x1b[0m\n" + prompt, true},
			{"wrapped", "esc to\ncancel\n" + prompt, true},
			{"idle", prompt, false},
			{"stale hint", "esc to cancel\n" + strings.Repeat("historical output\n", 20) + prompt, false},
		} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, hint), func(t *testing.T) {
				parsed, err := agent.NewParser().ParseWithHint(tc.output, hint)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.IsWorking != tc.working || parsed.IsIdle == tc.working {
					t.Fatalf("incorrect parser flags: %+v", parsed)
				}
				if idle := DetectIdleFromOutput(tc.output, string(hint)); idle == tc.working {
					t.Fatalf("direct idle=%v during working=%v", idle, tc.working)
				}
			})
		}
	}
}

func TestCodexLiveReadinessPreservesBlockingStates(t *testing.T) {
	for _, hint := range []agent.AgentType{"cod", "codex"} {
		for _, banner := range []string{"You've hit your usage limit", "Rate limit exceeded", "Too many requests"} {
			parsed, err := agent.NewParser().ParseWithHint(banner+"\n• Working (esc to interrupt)\n›\n47% context left\n", hint)
			if err != nil {
				t.Fatal(err)
			}
			if !parsed.IsRateLimited || parsed.IsIdle || parsed.IsWorking || parsed.GetRecommendation() != agent.RecommendRateLimitedWait {
				t.Fatalf("live markers overrode blocking limit %q: %+v", banner, parsed)
			}
		}
		parsed, err := agent.NewParser().ParseWithHint("• Working (esc to interrupt)\n›\n7% context left\n", hint)
		if err != nil {
			t.Fatal(err)
		}
		if !parsed.IsWorking || parsed.IsIdle || parsed.GetRecommendation() != agent.RecommendContextLowContinue {
			t.Fatalf("low-context work must finish before restart: %+v", parsed)
		}
		parsed, err = agent.NewParser().ParseWithHint("Error: unable to connect\n", hint)
		if err != nil || !parsed.IsInError || parsed.IsWorking || parsed.GetRecommendation() != agent.RecommendErrorState {
			t.Fatalf("real error without live work lost: state=%+v err=%v", parsed, err)
		}
	}
}
