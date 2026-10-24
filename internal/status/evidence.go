package status

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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
func RecoverPiRepaint(before, after, echoHash string, echoLength int) (string, bool) {
	const maxCaptureBytes = 1 << 20
	if echoLength <= 0 || echoLength > maxCaptureBytes || len(before) > maxCaptureBytes || len(after) > maxCaptureBytes {
		return "", false
	}
	digest, err := hex.DecodeString(echoHash)
	if err != nil || len(digest) != sha256.Size {
		return "", false
	}
	var want [sha256.Size]byte
	copy(want[:], digest)
	// Bound hashing even for adversarial captures with thousands of plausible
	// windows. Exceeding the budget never changes the existing boundary.
	budget := 64 << 20
	_, count, complete := piEchoWindow(before, want, echoLength, &budget)
	if !complete || count != 0 {
		return "", false
	}
	start, count, complete := piEchoWindow(after, want, echoLength, &budget)
	if !complete || count != 1 {
		return "", false
	}
	return after[start:], true
}

// piEchoWindow considers only whole nonempty lines, preserving raw offsets
// while normalizing whitespace in a separate comparison buffer. Blank rows
// cannot create duplicate matches of the same prompt.
func piEchoWindow(output string, want [sha256.Size]byte, length int, budget *int) (int, int, bool) {
	if strings.Count(output, "\n") > 4096 {
		return 0, 0, false
	}
	type boundary struct{ raw, normalized int }
	var starts []boundary
	ends := make(map[int]struct{})
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
			ends[normalized.Len()] = struct{}{}
		}
		rawOffset += len(line)
	}
	text := normalized.String()
	match, count := 0, 0
	for _, start := range starts {
		end := start.normalized + length
		if end > len(text) {
			break
		}
		if _, completeLine := ends[end]; !completeLine {
			continue
		}
		if length > *budget {
			return 0, 0, false
		}
		*budget -= length
		if sha256.Sum256([]byte(text[start.normalized:end])) == want {
			match, count = start.raw, count+1
			if count > 1 {
				return match, count, true
			}
		}
	}
	return match, count, true
}
