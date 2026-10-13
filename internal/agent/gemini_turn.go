package agent

import (
	"regexp"
	"strings"
)

// GeminiTurnState describes the current Gemini CLI input/response frame, not
// keywords in its transcript. Unknown is deliberately not permission to send.
type GeminiTurnState uint8

const (
	GeminiTurnUnknown GeminiTurnState = iota
	GeminiTurnWorking
	GeminiTurnIdle
	GeminiTurnRateLimited
)

const geminiLiveTailLines = 20

var (
	// LoadingIndicator.tsx renders this while StreamingState.Responding. In
	// narrow terminals the hint moves onto its own line and can wrap.
	geminiLiveCancelRe = regexp.MustCompile(`(?i)\besc\s+to\s+cancel\b`)
	// GeminiRespondingSpinner defaults to cli-spinners' dots animation. A
	// waiting-for-confirmation spinner also blocks new work, even without a
	// cancel hint. Do not infer work from bare prose such as "thinking".
	geminiLiveSpinnerRe = regexp.MustCompile(`(?m)^[ \t]*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏][ \t]+\S`)
	// InputPrompt.tsx uses > normally and * in YOLO mode. Only an empty
	// input or the exact empty-input placeholder is readiness evidence; a
	// draft, shell-mode ! prompt, search UI or quoted prose is not.
	geminiEmptyPromptRe = regexp.MustCompile(`^(?:gemini>[ \t]*|>[ \t]*|[>*][ \t]+Type your message or @path/to/file[ \t]*|[│┃][ \t]*[>*][ \t]*(?:Type your message or @path/to/file[ \t]*)?[│┃])$`)
	geminiRuleRe        = regexp.MustCompile(`^[╭╮╰╯┌┐└┘─━│┃ \t]+$`)
	// Footer.tsx emits an optional header/value pair. Its columns can be
	// disabled independently; "quota" is a column label, not an API error.
	// Match the whole header, in order, rather than arbitrary model mentions.
	geminiFooterHeaderRe = regexp.MustCompile(`^(?:workspace \(/directory\)(?:[ \t]+|$))?(?:sandbox(?:[ \t]+|$))?(?:/model(?:[ \t]+|$))?(?:(?:context|quota)[ \t]*)?$`)
	geminiLegacyFooterRe = regexp.MustCompile(`^gemini-\S+[ \t]+/model[ \t]*\|[ \t]*[0-9.]+[ \t]+MB[ \t]*$`)
)

// DetectGeminiTurn classifies ANSI-stripped output. Both the agent parser and
// status detector strip ANSI before calling it, so every consumer uses the
// same live-work veto and composer recognition. The markers are grounded in
// Gemini CLI's LoadingIndicator, InputPrompt and Footer renderers. This is not
// a guarantee that an interactive approval/authentication gate is clear;
// callers must still perform the normal dispatch gate preflight.
func DetectGeminiTurn(cleanOutput string) GeminiTurnState {
	if len(geminiRateLimitIndicators(cleanOutput)) > 0 {
		return GeminiTurnRateLimited
	}
	lines := strings.Split(strings.TrimSpace(cleanOutput), "\n")
	if len(lines) > geminiLiveTailLines {
		lines = lines[len(lines)-geminiLiveTailLines:]
	}
	tail := strings.Join(lines, "\n")
	if geminiLiveCancelRe.MatchString(tail) || geminiLiveSpinnerRe.MatchString(tail) {
		return GeminiTurnWorking
	}

	// Walk only the current bottom frame. A prompt above new transcript text
	// is stale, so unlike an unbounded prompt search, unknown rows stop us.
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || geminiRuleRe.MatchString(line) {
			continue
		}
		if i > 0 {
			header := strings.TrimSpace(lines[i-1])
			if header != "" && geminiFooterHeaderRe.MatchString(header) {
				i-- // Consume the footer's value and header together.
				continue
			}
		}
		if geminiLegacyFooterRe.MatchString(line) || line == "? for shortcuts" {
			continue
		}
		if geminiEmptyPromptRe.MatchString(line) {
			return GeminiTurnIdle
		}
		return GeminiTurnUnknown
	}
	return GeminiTurnUnknown
}

// geminiRateLimitIndicators deliberately excludes the words "quota",
// "capacity" and "try again" on their own. Gemini's ordinary usage footer
// contains "quota", and successful answers can contain the other two. Keep
// explicit exhaustion/retry signals, and use the same matches for both the
// verdict and its diagnostic indicators.
func geminiRateLimitIndicators(cleanOutput string) []string {
	lines := strings.Split(strings.TrimSpace(cleanOutput), "\n")
	if len(lines) > 50 {
		lines = lines[len(lines)-50:]
	}
	recent := strings.ToLower(strings.Join(lines, "\n"))
	var indicators []string
	for _, pattern := range []string{
		"quota exceeded",
		"quota has been exceeded",
		"exceeded your current quota",
		"limit reached",
		"rate limit",
		"rate_limit_exceeded",
		"too many requests",
		"resource exhausted",
		"resource_exhausted",
		"exhausted your capacity",
		"hit your usage limit",
	} {
		if strings.Contains(recent, pattern) {
			indicators = append(indicators, pattern)
		}
	}
	return indicators
}
