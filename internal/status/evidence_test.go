package status

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// Expected visible text is derived from the default pi renderer at
// https://github.com/earendil-works/pi/blob/6c2fd73b0c545a4034672cb60e34d53e035dd751/packages/tui/src/components/markdown.ts
// and UserMessageComponent's preserveOrderedListMarkers/preserveBackslashEscapes
// options. ANSI styling and terminal width do not change these normalized forms.
func TestPiPromptEchoRenderingFixtures(t *testing.T) {
	for _, tc := range []struct{ name, prompt, want string }{
		{"plain", "Review the changes and report.", "Review the changes and report."},
		{"formatting", "# Review\n\nInspect **changes** and `tests`.\n\n## Deliver\n\n### Preserve this heading", "Review Inspect changes and tests. Deliver ### Preserve this heading"},
		{"ordered markers", "3) **Inspect** the source\n7) Run `go test`", "3) Inspect the source 7) Run go test"},
		{"unordered markers", "* Inspect **source**\n* Run `tests`\n\n+ Report findings", "* Inspect source * Run tests + Report findings"},
		{"nested list", "- Inspect\n  - **Nested** work\n- Report", "- Inspect - Nested work - Report"},
		{"task list", "- [X] Inspect **source**\n- [ ] Run `tests`", "- [x] Inspect source - [ ] Run tests"},
		{"code literals", "```go\nfmt.Println(\"**literal**\", \"$HOME\")\n```", "```go fmt.Println(\"**literal**\", \"$HOME\") ```"},
		{"indented code", "    **literal** $HOME\n", "``` **literal** $HOME ```"},
		{"tilde fence", "~~~sh\nprintf '**literal**'\n~~~", "```sh printf '**literal**' ```"},
		{"inline literal", "Use `**literal** $HOME` here.", "Use **literal** $HOME here."},
		{"escaped punctuation", `Keep \*literal\* and \#heading.`, `Keep \*literal\* and \#heading.`},
		{"single verdict", "**VERDICT**", "VERDICT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fingerprint, rendered, ok := PiPromptEcho(tc.prompt)
			wantHash := sha256.Sum256([]byte(tc.want))
			if !ok || fingerprint.Hash != hex.EncodeToString(wantHash[:]) || fingerprint.Length != len(tc.want) {
				t.Fatalf("pi rendering = %q (%+v, %v), want normalized %q", rendered, fingerprint, ok, tc.want)
			}
			if got := NormalizeAgentEcho(rendered); got != tc.want {
				t.Fatalf("rendered text = %q, want %q", got, tc.want)
			}
			if tc.name == "single verdict" && rendered != "VERDICT" {
				t.Fatalf("transient verdict text has invented surrounding rows: %q", rendered)
			}
		})
	}
}

func TestPiPromptEchoRefusesUnsupportedRendering(t *testing.T) {
	for _, prompt := range []string{
		"", "\n\t ", strings.Repeat("a", (1<<20)+1),
		"Review [the source](https://example.com).", "![diagram](diagram.png)",
		"| Name | Value |\n| --- | --- |\n| A | B |", "> Quoted task", "---", "Compute $x^2$.",
		`Compute \(x^2\).`, `Compute \[x^2\].`, "<div>custom markup</div>",
	} {
		if fingerprint, rendered, ok := PiPromptEcho(prompt); ok || fingerprint.Hash != "" || rendered != "" {
			t.Fatalf("unsupported prompt acquired trusted identity: %q -> %+v, %q", prompt, fingerprint, rendered)
		}
	}
}

func TestPiRepaintRecognizesRenderedMarkdownCollectively(t *testing.T) {
	const prompt = "# Review\n\nInspect **changes** and `tests`."
	const visible = "Review\n\nInspect changes and tests."
	raw := NormalizeAgentEcho(prompt)
	digest := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(digest[:])
	rendered, _, ok := PiPromptEcho(prompt)
	if !ok {
		t.Fatal("fixture prompt was not supported")
	}
	for _, tc := range []struct {
		name, before, after, want string
		ok                        bool
	}{
		{"rendered answer", "Old task", "Pi v1.3.0\n" + visible + "\nSHIP-VERDICT", visible + "\nSHIP-VERDICT", true},
		{"raw echo", "Old task", prompt, prompt, true},
		{"rendered echo", "Old task", visible, visible, true},
		{"raw and rendered duplicate", "Old task", prompt + "\nOld answer\n" + visible + "\nSHIP-VERDICT", "", false},
		{"stale rendered history", visible + "\nOld answer", prompt + "\nSHIP-VERDICT", "", false},
		{"stale raw history", prompt + "\nOld answer", visible + "\nSHIP-VERDICT", "", false},
		{"partial rendered prompt", "Old task", "Review\nInspect changes", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RecoverPiRepaint(tc.before, tc.after, hash, len(raw), rendered)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("recovered %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
			if ok && regexp.MustCompile(`^SHIP-VERDICT`).MatchString(got) {
				t.Fatal("recovery manufactured a response regex beginning by dropping the prompt")
			}
		})
	}
	for _, echo := range []string{prompt, visible, "\x1b[1m" + visible + "\x1b[0m"} {
		if !MatchesAgentEcho(echo, hash, len(raw), rendered) {
			t.Fatalf("echo-only text could count as an answer: %q", echo)
		}
	}
	for _, answer := range []string{visible + "\nActual answer", prompt + "\nActual answer", "Other task"} {
		if MatchesAgentEcho(answer, hash, len(raw), rendered) {
			t.Fatalf("non-echo text was discarded: %q", answer)
		}
	}
}

func TestPiRepaintDeduplicatesEquivalentFingerprints(t *testing.T) {
	const prompt = "Run the tests."
	fingerprint, _, ok := PiPromptEcho(prompt)
	if !ok {
		t.Fatal("plain prompt failed")
	}
	if got, ok := RecoverPiRepaint("Old screen", prompt+"\nDone", fingerprint.Hash, fingerprint.Length, fingerprint); !ok || got != prompt+"\nDone" {
		t.Fatalf("same raw/rendered fingerprint created ambiguity: %q, %v", got, ok)
	}
	wrongLength := fingerprint
	wrongLength.Length++
	if MatchesAgentEcho(prompt, "", 0, wrongLength) {
		t.Fatal("whole-text echo guard ignored pinned length")
	}
	if _, ok := RecoverPiRepaint("Old screen", prompt+"\nDone", fingerprint.Hash, fingerprint.Length, PromptEchoFingerprint{Hash: "bad", Length: 1}); ok {
		t.Fatal("invalid alternate fingerprint authorized recovery")
	}
}

func TestUniqueAgentEchoSpanPreservesRawOffsets(t *testing.T) {
	const prompt = "Review changes and run tests."
	digest := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(digest[:])
	const prefix = "Old view\r\n\r\n"
	const echo = "\x1b[1mReview changes\x1b[0m\r\nand run tests.\r\n"
	const output = prefix + echo + "\nACTUAL-VERDICT\r\n"
	start, end, ok := UniqueAgentEchoSpan(output, hash, len(prompt), PromptEchoFingerprint{Hash: hash, Length: len(prompt)})
	if !ok || start != len(prefix) || end != len(prefix)+len(echo) || output[start:end] != echo {
		t.Fatalf("raw span = (%d, %d, %v), want (%d, %d, true)", start, end, ok, len(prefix), len(prefix)+len(echo))
	}
	if match := regexp.MustCompile(`(?m)^and run tests\.`).FindStringIndex(output); match == nil || match[0] >= end {
		t.Fatal("wrapped prompt phrase escaped its echo span")
	}
	if match := regexp.MustCompile(`ACTUAL-VERDICT`).FindStringIndex(output); match == nil || match[0] < end {
		t.Fatal("actual response was included in the prompt span")
	}
}

func TestUniqueAgentEchoSpanRefusesAmbiguousOrMissingEvidence(t *testing.T) {
	const prompt = "Review changes\nRun tests."
	normalized := NormalizeAgentEcho(prompt)
	digest := sha256.Sum256([]byte(normalized))
	hash := hex.EncodeToString(digest[:])
	short := sha256.Sum256([]byte("Review changes"))
	for _, tc := range []struct {
		name, output string
		length       int
		alternatives []PromptEchoFingerprint
	}{
		{"duplicate", prompt + "\nAnswer\n" + prompt, len(normalized), nil},
		{"partial", "Review changes", len(normalized), nil},
		{"same start different end", prompt + "\nAnswer", len(normalized), []PromptEchoFingerprint{{Hash: hex.EncodeToString(short[:]), Length: len("Review changes")}}},
		{"absent", "Another task\nDone", len(normalized), nil},
		{"legacy length", prompt, 0, nil},
		{"invalid alternative", prompt, len(normalized), []PromptEchoFingerprint{{Hash: "bad", Length: 1}}},
		{"oversized", strings.Repeat("x", (1<<20)+1), len(normalized), nil},
		{"too many rows", prompt + strings.Repeat("\n", 4097), len(normalized), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if start, end, ok := UniqueAgentEchoSpan(tc.output, hash, tc.length, tc.alternatives...); ok || start != 0 || end != 0 {
				t.Fatalf("ambiguous or absent evidence acquired span (%d, %d, %v)", start, end, ok)
			}
		})
	}
}

func TestRecoverPiRepaintRequiresUniqueNewTurn(t *testing.T) {
	prompt := "Review the intake charter and registers.\nReport the next action."
	normalized := NormalizeAgentEcho(prompt)
	digest := sha256.Sum256([]byte(normalized))
	hash := hex.EncodeToString(digest[:])
	echo := "> Review the intake charter\nand registers.\n\nReport the next action."
	for _, tc := range []struct {
		name, before, after, want string
		ok                        bool
	}{
		{"same-frame answer", "Pi v1.3.0\nOld conversation", "Pi v1.3.0\n\n" + echo + "\n\nThe next action is to reconcile the register.", echo + "\n\nThe next action is to reconcile the register.", true},
		{"echo only", "Old conversation", echo, echo, true},
		{"ANSI prompt", "Old conversation", "\x1b[32m" + prompt + "\x1b[0m\nResponse", "\x1b[32m" + prompt + "\x1b[0m\nResponse", true},
		{"old prompt in history", prompt + "\nEarlier answer", echo + "\nWrong turn", "", false},
		{"repeated prompt", "Old conversation", echo + "\nEarlier answer\n" + echo + "\nLater answer", "", false},
		{"partial prompt", "Old conversation", "Review the intake charter", "", false},
		{"mid-line match", "Old conversation", "Quoted request: " + normalized + "\nAnswer", "", false},
		{"extra text on last line", "Old conversation", prompt + " and do something else", "", false},
		{"unrelated replacement", "Old conversation", "Another task\nAn unrelated answer", "", false},
		{"empty repaint", "Old conversation", "\n\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RecoverPiRepaint(tc.before, tc.after, hash, len(normalized))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("RecoverPiRepaint = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRecoverPiRepaintRefusesMissingOrExcessiveEvidence(t *testing.T) {
	const prompt = "Perform the requested review."
	digest := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, before, after, hash string
		length                    int
	}{
		{"legacy receipt", "Old text", prompt, hash, 0},
		{"negative length", "Old text", prompt, hash, -1},
		{"oversized length", "Old text", prompt, hash, (1 << 20) + 1},
		{"invalid hash", "Old text", prompt, "not-a-digest", len(prompt)},
		{"oversized baseline", strings.Repeat("x", (1<<20)+1), prompt, hash, len(prompt)},
		{"oversized replacement", "Old text", prompt + strings.Repeat("x", 1<<20), hash, len(prompt)},
		{"too many baseline rows", strings.Repeat("\n", 4097), prompt, hash, len(prompt)},
		{"too many replacement rows", "Old text", prompt + strings.Repeat("\n", 4097), hash, len(prompt)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := RecoverPiRepaint(tc.before, tc.after, tc.hash, tc.length); ok || got != "" {
				t.Fatalf("insufficient evidence recovered %q", got)
			}
		})
	}
}

func TestNormalizeAgentEchoRetainsExistingMarkerContract(t *testing.T) {
	for _, input := range []string{
		"review this\nthen report",
		"❯ review this\n› then report",
		"\x1b[32m> review   this\x1b[0m\n\n  » then\treport",
	} {
		if got := NormalizeAgentEcho(input); got != "review this then report" {
			t.Fatalf("normalization changed for %q: %q", input, got)
		}
	}
}
