package status

import (
	"strings"
	"testing"
	"time"
)

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
