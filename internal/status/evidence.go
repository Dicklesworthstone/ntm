package status

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// NormalizeAgentEcho ignores input-row markers and terminal wrapping when
// comparing a submitted prompt with its visible echo. It must not be used to
// rewrite response evidence: matching and saved output retain their raw text.
func NormalizeAgentEcho(output string) string {
	lines := strings.Split(StripANSI(output), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		for _, marker := range []string{"❯", "›", "»", ">"} {
			if strings.HasPrefix(line, marker) {
				line = strings.TrimSpace(strings.TrimPrefix(line, marker))
				break
			}
		}
		lines[i] = line
	}
	return strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")
}

// PromptEchoFingerprint identifies one complete, normalized visible prompt.
// A pi delivery retains its raw fingerprint and the default Markdown
// renderer's fingerprint as alternatives for the same send.
type PromptEchoFingerprint struct {
	Hash   string
	Length int
}

var piListMarker = regexp.MustCompile(`^(?:[0-9]{1,9}[.)]|[-+*])(?:[ \t]+|\r?\n|$)`)

// PiPromptEcho fingerprints the supported, deterministic part of pi's default
// user-message Markdown renderer. It renders only the delivered prompt; reply
// text is never parsed as Markdown or stripped of punctuation for matching.
//
// Pi preserves list markers and backslash escapes, removes inline style
// delimiters and level-one/two heading markers, and retains deeper heading
// markers and code fences. Width-dependent tables, rules and quotes,
// terminal-dependent links, math, images and unsupported extensions refuse a
// fingerprint. Response-waiting callers must refuse those prompts before
// sending so an unknown transformed echo cannot be mistaken for an answer.
// The returned text is transient and is not stored in delivery receipts.
func PiPromptEcho(prompt string) (PromptEchoFingerprint, string, bool) {
	const maxPromptBytes = 1 << 20
	if len(prompt) == 0 || len(prompt) > maxPromptBytes {
		return PromptEchoFingerprint{}, "", false
	}
	source := []byte(StripANSI(prompt))
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()
	document := parser.Parse(text.NewReader(source))
	var rendered strings.Builder
	budget := 8192
	if !renderPiPromptNode(document, source, &rendered, 0, &budget) {
		return PromptEchoFingerprint{}, "", false
	}
	visible := strings.TrimSpace(rendered.String())
	echo := NormalizeAgentEcho(visible)
	if echo == "" || len(echo) > maxPromptBytes {
		return PromptEchoFingerprint{}, "", false
	}
	digest := sha256.Sum256([]byte(echo))
	return PromptEchoFingerprint{Hash: hex.EncodeToString(digest[:]), Length: len(echo)}, visible, true
}

func renderPiPromptNode(node ast.Node, source []byte, out *strings.Builder, depth int, budget *int) bool {
	if depth > 64 || *budget <= 0 {
		return false
	}
	*budget--
	children := func() bool {
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			if !renderPiPromptNode(child, source, out, depth+1, budget) {
				return false
			}
		}
		return true
	}
	block := func() bool {
		if !children() {
			return false
		}
		out.WriteByte('\n')
		return true
	}
	switch value := node.(type) {
	case *ast.Document, *ast.List:
		return block()
	case *ast.Paragraph, *ast.TextBlock:
		return block()
	case *ast.Heading:
		if value.Level >= 3 {
			out.WriteString(strings.Repeat("#", value.Level) + " ")
		}
		return block()
	case *ast.ListItem:
		start := value.Pos()
		if start < 0 || start >= len(source) {
			return false
		}
		marker := piListMarker.Find(source[start:])
		if marker == nil {
			return false
		}
		out.Write(bytes.TrimSpace(marker))
		out.WriteByte(' ')
		return block()
	case *ast.CodeBlock, *ast.FencedCodeBlock:
		out.WriteString("```")
		if fenced, ok := value.(*ast.FencedCodeBlock); ok && fenced.Info != nil {
			out.Write(bytes.TrimSpace(fenced.Info.Value(source)))
		}
		out.WriteByte('\n')
		out.Write(node.Lines().Value(source))
		out.WriteString("\n```\n")
		return true
	case *ast.CodeSpan:
		for child := value.FirstChild(); child != nil; child = child.NextSibling() {
			literal, ok := child.(*ast.Text)
			if !ok {
				return false
			}
			out.Write(bytes.ReplaceAll(literal.Value(source), []byte{'\n'}, []byte{' '}))
		}
		return true
	case *ast.Emphasis:
		return children()
	case *ast.Text:
		literal := value.Value(source)
		// Pi recognizes LaTeX separately from CommonMark. Refuse unknown
		// transformations outside code rather than discard their meaning.
		if bytes.ContainsAny(literal, "$") || bytes.Contains(literal, []byte(`\(`)) || bytes.Contains(literal, []byte(`\[`)) {
			return false
		}
		out.Write(literal)
		if value.SoftLineBreak() || value.HardLineBreak() {
			out.WriteByte('\n')
		}
		return true
	case *extast.TaskCheckBox:
		if value.IsChecked {
			out.WriteString("[x] ")
		} else {
			out.WriteString("[ ] ")
		}
		return true
	default:
		return false
	}
}

// MatchesAgentEcho recognizes an echo-only capture across a delivery's raw
// and rendered alternatives. Legacy raw fingerprints without a length retain
// their existing whole-text echo guard; repaint recovery still needs a length.
func MatchesAgentEcho(output, echoHash string, echoLength int, alternatives ...PromptEchoFingerprint) bool {
	echo := NormalizeAgentEcho(output)
	digest := sha256.Sum256([]byte(echo))
	hash := hex.EncodeToString(digest[:])
	for _, candidate := range append([]PromptEchoFingerprint{{Hash: echoHash, Length: echoLength}}, alternatives...) {
		if candidate.Hash == hash && (candidate.Length == 0 || candidate.Length == len(echo)) {
			return true
		}
	}
	return false
}

// RecoverPiRepaint finds a new turn's prompt in a pi transcript after a
// full-pane repaint has removed the previous capture boundary. Both inputs
// must first have recognized composer chrome removed with a successful
// AgentResponseTranscript call; incomplete frames must retain the old boundary.
// Recovery requires exactly one complete-line echo in after and none in
// before. The returned raw suffix includes the prompt, so an echo by itself
// cannot become response evidence or manufacture a new regex beginning.
//
// A hash and normalized byte count suffice on durable resume; no copy of the
// prompt is needed. Missing legacy lengths, ambiguous echoes, unsupported
// rendering and exhausted scan budgets all refuse recovery. Callers must
// separately retain pane identity, submission and idle checks, and must not
// replace already-collected response text with a newly anchored screen.
func RecoverPiRepaint(before, after, echoHash string, echoLength int, alternatives ...PromptEchoFingerprint) (string, bool) {
	// Bound hashing even for adversarial captures with thousands of plausible
	// windows. Exceeding the budget never changes the existing boundary.
	budget := 64 << 20
	old, complete := agentEchoSpans(before, echoHash, echoLength, &budget, alternatives...)
	if !complete || len(old) != 0 {
		return "", false
	}
	fresh, complete := agentEchoSpans(after, echoHash, echoLength, &budget, alternatives...)
	if !complete || len(fresh) != 1 {
		return "", false
	}
	return after[fresh[0].start:], true
}

// UniqueAgentEchoSpan locates exactly one complete prompt echo across all
// pinned representations. Its offsets refer to unchanged output; the end
// includes the last prompt line's newline, but no following response text.
// Callers can require a response after the echo without moving regex anchors.
// Missing, duplicate or unbounded evidence refuses a span.
func UniqueAgentEchoSpan(output, echoHash string, echoLength int, alternatives ...PromptEchoFingerprint) (start, end int, ok bool) {
	budget := 64 << 20
	spans, complete := agentEchoSpans(output, echoHash, echoLength, &budget, alternatives...)
	if !complete || len(spans) != 1 {
		return 0, 0, false
	}
	return spans[0].start, spans[0].end, true
}

func agentEchoSpans(output, echoHash string, echoLength int, budget *int, alternatives ...PromptEchoFingerprint) ([]piEchoRange, bool) {
	const maxCaptureBytes = 1 << 20
	if len(output) > maxCaptureBytes {
		return nil, false
	}
	matches := make(map[piEchoRange]struct{})
	seen := make(map[PromptEchoFingerprint]struct{})
	for _, candidate := range append([]PromptEchoFingerprint{{Hash: echoHash, Length: echoLength}}, alternatives...) {
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		digest, err := hex.DecodeString(candidate.Hash)
		if err != nil || len(digest) != sha256.Size || candidate.Length <= 0 || candidate.Length > maxCaptureBytes {
			return nil, false
		}
		var want [sha256.Size]byte
		copy(want[:], digest)
		fresh, complete := piEchoWindow(output, want, candidate.Length, budget)
		if !complete {
			return nil, false
		}
		for _, match := range fresh {
			matches[match] = struct{}{}
		}
		if len(matches) > 1 {
			return nil, false
		}
	}
	spans := make([]piEchoRange, 0, len(matches))
	for match := range matches {
		spans = append(spans, match)
	}
	return spans, true
}

type piEchoRange struct{ start, end int }

// piEchoWindow considers only whole nonempty lines, preserving raw offsets
// while normalizing whitespace in a separate comparison buffer. Blank rows
// cannot create duplicate matches of the same prompt.
func piEchoWindow(output string, want [sha256.Size]byte, length int, budget *int) ([]piEchoRange, bool) {
	if strings.Count(output, "\n") > 4096 {
		return nil, false
	}
	type boundary struct{ raw, normalized int }
	var starts []boundary
	ends := make(map[int]int)
	var normalized strings.Builder
	rawOffset := 0
	for _, line := range strings.SplitAfter(output, "\n") {
		text := NormalizeAgentEcho(line)
		if text != "" {
			if normalized.Len() != 0 {
				normalized.WriteByte(' ')
			}
			starts = append(starts, boundary{rawOffset, normalized.Len()})
			normalized.WriteString(text)
			ends[normalized.Len()] = rawOffset + len(line)
		}
		rawOffset += len(line)
	}
	text := normalized.String()
	var matches []piEchoRange
	for _, start := range starts {
		end := start.normalized + length
		if end > len(text) {
			break
		}
		rawEnd, completeLine := ends[end]
		if !completeLine {
			continue
		}
		if length > *budget {
			return nil, false
		}
		*budget -= length
		if sha256.Sum256([]byte(text[start.normalized:end])) == want {
			matches = append(matches, piEchoRange{start: start.raw, end: rawEnd})
			if len(matches) > 1 {
				return matches, true
			}
		}
	}
	return matches, true
}
