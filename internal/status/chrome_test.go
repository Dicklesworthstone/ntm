package status

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

func TestAgentTranscriptPreservesTextAndExcludesChromeOnlyCapture(t *testing.T) {
	transcript := "prior answer\n\n  indented response\n"
	chrome := "╭──────────╮\n│ ❯       │\n╰──────────╯\ncustom status text"
	if got := AgentTranscript(transcript+chrome, "cc"); got != strings.TrimSuffix(transcript, "\n") {
		t.Fatalf("transcript text changed: got %q", got)
	}
	if got := AgentTranscript(chrome, "cc"); got != "" {
		t.Fatalf("chrome-only capture became response evidence: %q", got)
	}
	if got := AgentTranscript("plain\n  output\n", "unknown"); got != "plain\n  output\n" {
		t.Fatalf("plain output changed: %q", got)
	}
}

// claudePaneWithStatusLine reproduces the pane shape from ntm#322: a
// transcript, then Claude Code's bordered composer, then a two-line
// user-configured status line pinned at the very bottom. The status line text
// is the reporter's own, which is by itself longer than the 200-character
// preview window — so a blind tail truncation returns nothing but chrome.
const claudePaneWithStatusLine = `● I looked at internal/api/auth.go and the token refresh path never
  retries on a 401, so an expired token surfaces as a hard failure.

● Update(internal/api/auth.go)
  ⎿  Added retry-on-401 with a single refresh attempt.

● The auth refactor is done. Tests pass.

╭──────────────────────────────────────────────────────────────────────╮
│ ❯                                                                    │
╰──────────────────────────────────────────────────────────────────────╯
  velist-mvp ctx 173k/1000k (17%) | 5h 27% ↻1h50m | 7d 9% ↻6… jeff@...
  ▶▶ bypass permissions on (shift+tab to cycle) · ✓ Update installed · Restart to update`

// TestLastMeaningfulOutputSkipsClaudeStatusLine is the ntm#322 regression.
func TestLastMeaningfulOutputSkipsClaudeStatusLine(t *testing.T) {
	got := LastMeaningfulOutput(claudePaneWithStatusLine, "cc", 200)

	if got == "" {
		t.Fatal("preview is empty; the transcript above the composer must survive")
	}

	// Nothing from the status line or the composer box may appear.
	for _, chrome := range []string{
		"bypass permissions",
		"ctx 173k/1000k",
		"Restart to update",
		"╭", "╰", "│", "❯",
	} {
		if strings.Contains(got, chrome) {
			t.Errorf("preview still carries chrome %q:\n%s", chrome, got)
		}
	}

	// And it must be actual transcript.
	if !strings.Contains(got, "The auth refactor is done") {
		t.Errorf("preview lost the agent's last real output:\n%s", got)
	}
	if len(got) > 200 {
		t.Errorf("preview length %d exceeds the 200-char budget", len(got))
	}
}

// TestLastMeaningfulOutputMatchesOldTailOnPlainPanes guards against
// over-trimming: a pane with no composer box (a plain shell, an agent type we
// know nothing about) keeps returning its tail.
func TestLastMeaningfulOutputMatchesOldTailOnPlainPanes(t *testing.T) {
	plain := "make: entering directory\nbuilding...\nbuild finished in 4.2s"

	for _, agentType := range []string{"", "user", "unknown", "aider"} {
		got := LastMeaningfulOutput(plain, agentType, 200)
		if !strings.Contains(got, "build finished in 4.2s") {
			t.Errorf("agentType %q: preview lost the tail:\n%s", agentType, got)
		}
	}
}

// TestLastMeaningfulOutputKeepsTranscriptBoxes verifies that a boxed tool
// result inside the transcript is not mistaken for the composer: the cut
// anchors on the bottom-most composer marker, and content below a transcript
// box survives.
func TestLastMeaningfulOutputKeepsTranscriptBoxes(t *testing.T) {
	withTranscriptBox := `● Here is the diff:

┌──────────────────────┐
│ - old line           │
│ + new line           │
└──────────────────────┘

● Applied cleanly and the suite is green.`

	got := LastMeaningfulOutput(withTranscriptBox, "cc", 200)
	if !strings.Contains(got, "Applied cleanly") {
		t.Errorf("content below a transcript box was trimmed away:\n%s", got)
	}
}

// TestLastMeaningfulOutputKeepsBoxGluedToComposer covers the case with no
// blank line between a transcript box and the input box. The upward walk must
// stop at the transcript box's closing rule rather than absorbing it and the
// content inside it.
func TestLastMeaningfulOutputKeepsBoxGluedToComposer(t *testing.T) {
	glued := `● Here is the diff:
┌──────────────────────┐
│ - old line           │
│ + new KEEPME line    │
└──────────────────────┘
╭──────────────────────╮
│ ❯                    │
╰──────────────────────╯
  ctx 12% | bypass on`

	got := LastMeaningfulOutput(glued, "cc", 300)

	if !strings.Contains(got, "KEEPME") {
		t.Errorf("a transcript box glued to the composer was absorbed as chrome:\n%s", got)
	}
	if strings.Contains(got, "bypass on") || strings.Contains(got, "❯") {
		t.Errorf("composer/status chrome survived:\n%s", got)
	}
}

// TestLastMeaningfulOutputKeepsMarkdownBlockquotes guards the other
// over-trim: a markdown blockquote in the transcript starts with ">", and
// must not be mistaken for Claude's input box.
func TestLastMeaningfulOutputKeepsMarkdownBlockquotes(t *testing.T) {
	withQuote := `● The spec says:

> A token refresh must be attempted exactly once.

● So the retry loop is capped at one attempt.`

	got := LastMeaningfulOutput(withQuote, "cc", 200)
	if !strings.Contains(got, "retry loop is capped") {
		t.Errorf("content below a markdown blockquote was trimmed away:\n%s", got)
	}
}

// TestLastMeaningfulOutputComposerOnlyPaneFallsBack covers a pane that is all
// chrome (a freshly started agent with no transcript yet): trimming must not
// turn a sparse preview into an empty one.
func TestLastMeaningfulOutputComposerOnlyPaneFallsBack(t *testing.T) {
	composerOnly := `╭────────────────────────╮
│ ❯                      │
╰────────────────────────╯
  ready`

	if got := LastMeaningfulOutput(composerOnly, "cc", 200); got == "" {
		t.Error("a composer-only pane produced an empty preview instead of falling back to the raw tail")
	}
}

// TestAnalyzeAtStripsChromeFromPreview pins the dashboard's actual producer,
// not just the helper: AgentStatus.LastOutput is what "Recent Output" renders.
func TestAnalyzeAtStripsChromeFromPreview(t *testing.T) {
	d := NewDetector()

	got := d.AnalyzeAt("%1", "proj__cc_1", "cc", claudePaneWithStatusLine, time.Time{}, time.Now()).LastOutput

	if strings.Contains(got, "bypass permissions") || strings.Contains(got, "ctx 173k/1000k") {
		t.Errorf("AnalyzeAt preview still carries the status line:\n%s", got)
	}
	if !strings.Contains(got, "The auth refactor is done") {
		t.Errorf("AnalyzeAt preview lost the transcript:\n%s", got)
	}
}

// TestBoxRuleLinesNeverReachThePreview covers the belt-and-braces filter: a
// border row that escapes chrome trimming (a composer box taller than the
// scan window, for instance) must still not be shown as recent output.
func TestBoxRuleLinesNeverReachThePreview(t *testing.T) {
	// No composer marker anywhere, so trimAgentChrome returns the input
	// untouched and only the final filter can drop the rules.
	withBareRules := "● build finished\n╭──────────────╮\n╰──────────────╯"

	got := LastMeaningfulOutput(withBareRules, "cc", 200)
	for _, glyph := range []string{"╭", "╰", "─"} {
		if strings.Contains(got, glyph) {
			t.Errorf("box glyph %q reached the preview:\n%s", glyph, got)
		}
	}
	if !strings.Contains(got, "build finished") {
		t.Errorf("filtering the rules also dropped the content:\n%s", got)
	}
}

// TestLastMeaningfulOutputBudget covers the maxLen contract across the
// boundary where the ellipsis no longer fits.
func TestLastMeaningfulOutputBudget(t *testing.T) {
	long := strings.Repeat("abcdefghij", 40) // 400 chars, single line

	for _, maxLen := range []int{0, 1, 3, 4, 10, 200, 1000} {
		got := LastMeaningfulOutput(long, "cc", maxLen)
		if maxLen <= 0 {
			if got != "" {
				t.Errorf("maxLen %d returned %q, want empty", maxLen, got)
			}
			continue
		}
		if len(got) > maxLen {
			t.Errorf("maxLen %d: result length %d exceeds the budget", maxLen, len(got))
		}
	}
}

// TestLastMeaningfulOutputEdgeCases covers the inputs a live pane actually
// produces that a hand-written fixture tends to miss: CRLF endings, chrome
// wrapped in ANSI colour (a status line is almost always dim), multi-byte
// transcript text, and degenerate budgets.
func TestLastMeaningfulOutputEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		agent   string
		maxLen  int
		wantNot []string
		wantHas []string
	}{
		{name: "empty input", in: "", agent: "cc", maxLen: 200},
		{name: "only newlines", in: "\n\n\n\n", agent: "cc", maxLen: 200},
		{
			name:    "CRLF line endings",
			in:      "● done building\r\n╭────╮\r\n│ ❯  │\r\n╰────╯\r\n  ctx 5% | bypass on\r\n",
			agent:   "cc",
			maxLen:  200,
			wantNot: []string{"bypass on", "❯", "╭"},
			wantHas: []string{"done building"},
		},
		{
			name:    "ANSI-dimmed chrome is still chrome",
			in:      "● built ok\n\x1b[2m╭────╮\x1b[0m\n\x1b[2m│ ❯  │\x1b[0m\n\x1b[2m╰────╯\x1b[0m\n\x1b[2m ctx 9%\x1b[0m",
			agent:   "cc",
			maxLen:  200,
			wantNot: []string{"ctx 9%"},
			wantHas: []string{"built ok"},
		},
		{
			name:    "multi-byte transcript survives intact",
			in:      "● 完了しました — ünïcode ✓\n╭──╮\n│ ❯│\n╰──╯\n status",
			agent:   "cc",
			maxLen:  200,
			wantHas: []string{"完了しました", "ünïcode"},
		},
		{
			name:   "budget of one byte does not panic",
			in:     "● hello world this is long\n│ ❯ │\n status",
			agent:  "cc",
			maxLen: 1,
		},
		{
			name:    "unknown agent type falls back to the tail",
			in:      "line one\nline two",
			agent:   "totally-unknown",
			maxLen:  200,
			wantHas: []string{"line two"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LastMeaningfulOutput(tc.in, tc.agent, tc.maxLen)

			if tc.maxLen > 0 && len(got) > tc.maxLen {
				t.Errorf("length %d exceeds maxLen %d: %q", len(got), tc.maxLen, got)
			}
			// Truncation walks bytes, so a mid-rune cut is a live hazard.
			if !utf8.ValidString(got) {
				t.Errorf("preview is not valid UTF-8: %q", got)
			}
			for _, bad := range tc.wantNot {
				if strings.Contains(got, bad) {
					t.Errorf("preview still contains %q:\n%s", bad, got)
				}
			}
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("preview missing %q:\n%s", want, got)
				}
			}
		})
	}
}

// TestLastMeaningfulOutputCodexComposer covers the second bordered-composer
// agent, including codex's ultra-effort glyph swap.
func TestLastMeaningfulOutputCodexComposer(t *testing.T) {
	for _, marker := range []string{"›", "»"} {
		pane := "● codex finished the refactor.\n\n│ " + marker + "                    │\n  context left: 42%"

		got := LastMeaningfulOutput(pane, "cod", 200)
		if !strings.Contains(got, "codex finished the refactor") {
			t.Errorf("marker %q: lost the transcript:\n%s", marker, got)
		}
		if strings.Contains(got, "context left") {
			t.Errorf("marker %q: status line below the composer survived:\n%s", marker, got)
		}
	}
}

// These fixtures reproduce the default pi 1.3 composer/footer shape from
// ntm#361 and pi's Editor, CustomEditor and FooterComponent renderers. Unlike
// omp, pi has no vertical box edges or prompt glyph in its input area.
const piPaneRule = "────────────────────────────────────────────────────────────────"
const piPaneFooter = "~/projects/demo (main)\n↑4.4k ↓380 R2.1k $0.013 2.6%/1.0M (auto)  claude-sonnet-4"
const piIdleComposer = piPaneRule + "\n \n" + piPaneRule + "\n" + piPaneFooter

func TestPiTranscriptExcludesComposerAndFooter(t *testing.T) {
	const transcript = " Review the retry path.\n\n The refresh runs once.\n  Cancellation is preserved."
	workingRule := "── ⠹ Working " + strings.Repeat("─", 51)
	scrolledTop := strings.Repeat("─", 27) + " ↑ 2 more " + strings.Repeat("─", 27)
	scrolledBottom := strings.Repeat("─", 27) + " ↓ 3 more " + strings.Repeat("─", 27)
	for _, tc := range []struct {
		name     string
		composer string
	}{
		{"idle", piIdleComposer},
		{"working", workingRule + "\n \n" + piPaneRule + "\n" + piPaneFooter},
		{"multiline draft", piPaneRule + "\n SHIP-VERDICT\n this has not been submitted\n third draft line\n" + piPaneRule + "\n" + piPaneFooter},
		{"draft contains a rule", piPaneRule + "\n DRAFT-MUST-NOT-LEAK\n " + strings.Repeat("─", 62) + " \n more draft text\n" + piPaneRule + "\n" + piPaneFooter},
		{"tall draft", piPaneRule + "\n" + strings.Repeat(" draft row\n", 50) + piPaneRule + "\n" + piPaneFooter},
		{"scrolled draft", scrolledTop + "\n visible middle draft\n" + scrolledBottom + "\n" + piPaneFooter},
		{"autocomplete", piPaneRule + "\n /mod\n" + piPaneRule + "\n → /model    Select a model\n   /models   Configure available models\n   (1/2)\n" + piPaneFooter},
		{"empty autocomplete", piPaneRule + "\n /does-not-exist\n" + piPaneRule + "\n  No matching commands\n" + piPaneFooter},
		{"extension status", piIdleComposer + "\nchecks: SHIP-VERDICT (extension status)"},
		{"terminal padding", piIdleComposer + strings.Repeat("\n ", 40)},
		{"Windows cwd", piPaneRule + "\n \n" + piPaneRule + "\nC:\\work\\demo (main)\n0.0%/200k (auto)  no-model"},
		{"UNC cwd", piPaneRule + "\n \n" + piPaneRule + "\n\\\\server\\share\\demo\n0.0%/200k (auto)  no-model"},
		{"home cwd", piPaneRule + "\n \n" + piPaneRule + "\n~\n?/200k (auto)  no-model"},
		{"subscription usage", piPaneRule + "\n \n" + piPaneRule + "\n~/demo\n↑3.2k ↓143 $0.000 (sub) 1.2%/200k (auto)  kimi"},
		{"narrow truncated usage", strings.Repeat("─", 26) + "\n \n" + strings.Repeat("─", 26) + "\n~/demo (main)\n↑444k ↓36k R377k CH99..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, kind := range []string{"pi", " PI "} {
				got := AgentTranscript(transcript+"\n"+tc.composer, kind)
				if got != transcript {
					t.Fatalf("AgentTranscript(%s) = %q, want %q", kind, got, transcript)
				}
				if got, complete := AgentResponseTranscript(transcript+"\n"+tc.composer, kind); !complete || got != transcript {
					t.Fatalf("complete pi frame was not usable: got %q, %v", got, complete)
				}
			}
		})
	}
	if got := AgentTranscript(piIdleComposer, "pi"); got != "" {
		t.Fatalf("chrome-only pane became response evidence: %q", got)
	}
	if got, complete := AgentResponseTranscript(piIdleComposer, "pi"); !complete || got != "" {
		t.Fatalf("complete chrome-only frame = %q, %v; want empty, true", got, complete)
	}
	colored := "\x1b[2m" + strings.ReplaceAll(piIdleComposer, "\n", "\x1b[0m\n\x1b[2m") + "\x1b[0m"
	if got := AgentTranscript(transcript+"\n"+colored, "pi"); got != transcript {
		t.Fatalf("ANSI chrome leaked into the transcript: %q", got)
	}
}

func TestPiTranscriptRequiresCompleteRecognizableChrome(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane string
	}{
		{"empty", ""},
		{"plain output", "first line\nsecond line"},
		{"transcript horizontal rules", "tool output\n" + piPaneRule + "\nresult between rules\n" + piPaneRule},
		{"missing cwd", "answer\n" + piPaneRule + "\n \n" + piPaneRule + "\n0.0%/1.0M (auto)"},
		{"unknown custom footer", "answer\n" + piPaneRule + "\n \n" + piPaneRule + "\ncustom footer\nready"},
		{"prose below cwd", "answer\n" + piPaneRule + "\n \n" + piPaneRule + "\n~/demo\nA sentence mentioning 0.0%/1.0M"},
		{"mismatched rule widths", "answer\n────\n \n" + piPaneRule + "\n" + piPaneFooter},
		{"arbitrary top rule text", "answer\n── an ordinary output heading " + strings.Repeat("─", 34) + "\n \n" + piPaneRule + "\n" + piPaneFooter},
		{"unknown top after transcript rule", "answer\n" + piPaneRule + "\nkeep this result\n── arbitrary heading " + strings.Repeat("─", 43) + "\n \n" + piPaneRule + "\n" + piPaneFooter},
		{"missing top border", "answer\n draft\n" + piPaneRule + "\n" + piPaneFooter},
		{"missing input row", "answer\n" + piPaneRule + "\n" + piPaneRule + "\n" + piPaneFooter},
		{"incomplete redraw", "answer\n" + piPaneRule + "\n draft"},
		{"unexpected row before footer", "answer\n" + piPaneRule + "\n \n" + piPaneRule + "\nThis is transcript output\n" + piPaneFooter},
		{"exited to shell", "answer\n" + piIdleComposer + "\nuser@host:~/demo$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentTranscript(tc.pane, "pi"); got != tc.pane {
				t.Fatalf("unrecognized/incomplete pane was trimmed: got %q, want %q", got, tc.pane)
			}
			if got, complete := AgentResponseTranscript(tc.pane, "pi"); complete || got != "" {
				t.Fatalf("unrecognized/incomplete frame became response evidence: %q, %v", got, complete)
			}
		})
	}
	transcript := "First section\n" + piPaneRule + "\nSecond section"
	if got := AgentTranscript(transcript+"\n"+piIdleComposer, "pi"); got != transcript {
		t.Fatalf("real transcript rule was lost: %q", got)
	}
}

func TestPiResponseTranscriptRejectsDraftsDuringRepaint(t *testing.T) {
	const prompt = "Review the retry path and return SHIP-VERDICT."
	// The unique prompt can be present in the transcript while a following
	// unsubmitted draft contains the expected completion marker. Neither a
	// prompt hash match nor a stale idle observation makes that draft a reply.
	prefix := "Pi v1.3.0\nescape interrupt\n\n " + prompt + "\n" + piPaneRule + "\n SHIP-VERDICT"
	for _, tc := range []struct{ name, pane string }{
		{"cleared frame", "\n \n"},
		{"dangling top border", prefix},
		{"missing footer", prefix + "\n" + piPaneRule},
		{"cwd only", prefix + "\n" + piPaneRule + "\n~/projects/demo (main)"},
		{"half painted stats", prefix + "\n" + piPaneRule + "\n~/projects/demo (main)\n↑4.4k ↓"},
		{"custom footer", prefix + "\n" + piPaneRule + "\nWorkspace: demo\nReady"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, complete := AgentResponseTranscript(tc.pane, "pi"); complete || got != "" {
				t.Fatalf("partial repaint exposed draft evidence: %q, %v", got, complete)
			}
		})
	}
	full := prefix + "\n" + piPaneRule + "\n" + piPaneFooter
	want := "Pi v1.3.0\nescape interrupt\n\n " + prompt
	if got, complete := AgentResponseTranscript(full, "pi"); !complete || got != want {
		t.Fatalf("complete repaint retained draft or lost prompt: %q, %v", got, complete)
	}
}

func TestPiTranscriptPreservesStartupBoundary(t *testing.T) {
	for _, header := range []string{
		" ▀▀█  v1.3.0\n █▀ █ escape interrupt · ctrl+c/ctrl+d clear/exit",
		"Pi v1.3.0\nescape interrupt · ctrl+c/ctrl+d clear/exit",
	} {
		want := header + "\n\nThe startup transcript."
		before := "[user@host demo]$ cd '/home/user/projects/demo' && pi --model x\n\n" + want + "\n" + piIdleComposer
		after := "\n" + want + "\n" + piIdleComposer
		if got := AgentTranscript(before, "pi"); got != want {
			t.Fatalf("launch preamble remained: %q", got)
		}
		if got := AgentTranscript(after, "pi"); got != want {
			t.Fatalf("repaint changed the startup boundary: %q", got)
		}
		quoted := "Keep this answer before the quoted banner.\n" + want
		if got := AgentTranscript(quoted+"\n"+piIdleComposer, "pi"); got != quoted {
			t.Fatalf("a later quoted banner erased transcript text: %q", got)
		}
	}
	plain := "Header disabled; this is real output."
	if got := AgentTranscript(plain+"\n"+piIdleComposer, "pi"); got != plain {
		t.Fatalf("bannerless transcript changed: %q", got)
	}
}

func TestPiChromeDoesNotChangeOtherAgents(t *testing.T) {
	pi := "pi answer\n" + piIdleComposer
	for _, kind := range []string{"omp", "oh-my-pi", "cc", "cod", "unknown"} {
		if got := AgentTranscript(pi, kind); got != pi {
			t.Fatalf("pi chrome recognized for %s: %q", kind, got)
		}
		if got, complete := AgentResponseTranscript(pi, kind); !complete || got != pi {
			t.Fatalf("pi evidence restrictions changed %s: %q, %v", kind, got, complete)
		}
	}
	for _, fixture := range []string{"omp_nerd_idle_done.txt", "omp_unicode_working.txt", "omp_ascii_idle_done.txt"} {
		pane := loadOmpFixture(t, fixture)
		if got := AgentTranscript(pane, "pi"); got != StripANSI(pane) {
			t.Fatalf("omp fixture %s was treated as pi: %q", fixture, got)
		}
	}
}

func TestPiPaneReadinessAndPreviewUseRegisteredPlugin(t *testing.T) {
	// #362 already supplies plugin readiness to the shared classifiers. Pin
	// pi's real idle/working frames through that path rather than creating a
	// second pi-only readiness implementation.
	if err := agent.RegisterPlugin("pi", []string{`^─{4,}$`}, []string{`^─{2,}\s*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]`}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	working := " Reading the retry implementation.\n── ⠹ Working " + strings.Repeat("─", 51) + "\n \n" + piPaneRule + "\n" + piPaneFooter
	idle := "The retry preserves cancellation.\n" + piIdleComposer
	for _, tc := range []struct {
		name string
		pane string
		last time.Time
		want AgentState
	}{
		{"idle despite busy sibling", idle, now, StateIdle},
		{"working despite low velocity", working, now.Add(-time.Hour), StateWorking},
		{"historical spinner is not current work", "── ⠹ Working ──\n" + strings.Repeat("historical output\n", 30) + idle, now, StateIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewDetector().AnalyzeAt("%71", "demo__pi_1", "pi", tc.pane, tc.last, now)
			if got.State != tc.want {
				t.Fatalf("pi state = %s, want %s", got.State, tc.want)
			}
			if idle := DetectIdleFromOutput(tc.pane, "pi"); idle != (tc.want == StateIdle) {
				t.Fatalf("pi idle = %v, want %v", idle, tc.want == StateIdle)
			}
			if strings.Contains(got.LastOutput, "~/projects/demo") || strings.Contains(got.LastOutput, "2.6%/1.0M") {
				t.Fatalf("preview included pinned footer: %q", got.LastOutput)
			}
		})
	}
}
