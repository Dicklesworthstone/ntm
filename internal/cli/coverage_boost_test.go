package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/cli/tiers"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// =============================================================================
// stripANSI tests
// =============================================================================

func TestStripANSI_NoEscapes(t *testing.T) {
	got := stripANSI("hello world")
	if got != "hello world" {
		t.Errorf("stripANSI(plain) = %q, want %q", got, "hello world")
	}
}

func TestStripANSI_WithColors(t *testing.T) {
	input := "\033[31mred\033[0m text"
	got := stripANSI(input)
	if got != "red text" {
		t.Errorf("stripANSI(colored) = %q, want %q", got, "red text")
	}
}

func TestStripANSI_MultipleCodes(t *testing.T) {
	input := "\033[1m\033[32mbold green\033[0m normal"
	got := stripANSI(input)
	if got != "bold green normal" {
		t.Errorf("stripANSI(multi) = %q, want %q", got, "bold green normal")
	}
}

func TestStripANSI_Empty(t *testing.T) {
	got := stripANSI("")
	if got != "" {
		t.Errorf("stripANSI(\"\") = %q, want \"\"", got)
	}
}

// =============================================================================
// padRight tests
// =============================================================================

func TestPadRight_ShorterString(t *testing.T) {
	got := padRight("hi", 5)
	if len(got) < 5 {
		t.Errorf("padRight(\"hi\", 5) = %q, want width >= 5", got)
	}
}

func TestPadRight_ExactWidth(t *testing.T) {
	got := padRight("hello", 5)
	// Should not add padding
	if got != "hello" {
		t.Errorf("padRight(\"hello\", 5) = %q, want \"hello\"", got)
	}
}

func TestPadRight_LongerString(t *testing.T) {
	got := padRight("hello world", 5)
	if got != "hello world" {
		t.Errorf("padRight(longer, 5) = %q, want \"hello world\"", got)
	}
}

func TestPadRight_ZeroWidth(t *testing.T) {
	got := padRight("hi", 0)
	if got != "hi" {
		t.Errorf("padRight(\"hi\", 0) = %q, want \"hi\"", got)
	}
}

// =============================================================================
// Styled text helpers (SectionHeader, SectionDivider, etc.)
// =============================================================================

func TestSectionHeader_ContainsTitle(t *testing.T) {
	got := SectionHeader("Status")
	plain := stripANSI(got)
	if !strings.Contains(plain, "Status") {
		t.Errorf("SectionHeader(\"Status\") stripped = %q, want to contain \"Status\"", plain)
	}
}

func TestSectionDivider_CorrectLength(t *testing.T) {
	got := SectionDivider(20)
	plain := stripANSI(got)
	// Each "─" is 3 bytes in UTF-8 but 1 rune
	runes := []rune(plain)
	if len(runes) != 20 {
		t.Errorf("SectionDivider(20) rune count = %d, want 20", len(runes))
	}
}

func TestKeyValue_Format(t *testing.T) {
	got := KeyValue("Name", "NTM", 10)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Name:") {
		t.Errorf("KeyValue() stripped = %q, should contain 'Name:'", plain)
	}
	if !strings.Contains(plain, "NTM") {
		t.Errorf("KeyValue() stripped = %q, should contain 'NTM'", plain)
	}
}

func TestSuccessMessage_ContainsIcon(t *testing.T) {
	got := SuccessMessage("done")
	plain := stripANSI(got)
	if !strings.Contains(plain, "✓") {
		t.Errorf("SuccessMessage() = %q, should contain ✓", plain)
	}
	if !strings.Contains(plain, "done") {
		t.Errorf("SuccessMessage() = %q, should contain 'done'", plain)
	}
}

func TestErrorMessage_ContainsIcon(t *testing.T) {
	got := ErrorMessage("failed")
	plain := stripANSI(got)
	if !strings.Contains(plain, "✗") {
		t.Errorf("ErrorMessage() = %q, should contain ✗", plain)
	}
}

func TestWarningMessage_ContainsIcon(t *testing.T) {
	got := WarningMessage("caution")
	plain := stripANSI(got)
	if !strings.Contains(plain, "⚠") {
		t.Errorf("WarningMessage() = %q, should contain ⚠", plain)
	}
}

func TestInfoMessage_ContainsIcon(t *testing.T) {
	got := InfoMessage("note")
	plain := stripANSI(got)
	if !strings.Contains(plain, "ℹ") {
		t.Errorf("InfoMessage() = %q, should contain ℹ", plain)
	}
}

func TestSubtleText_NonEmpty(t *testing.T) {
	got := SubtleText("muted")
	plain := stripANSI(got)
	if plain != "muted" {
		t.Errorf("SubtleText() stripped = %q, want \"muted\"", plain)
	}
}

func TestBoldText_NonEmpty(t *testing.T) {
	got := BoldText("important")
	plain := stripANSI(got)
	if plain != "important" {
		t.Errorf("BoldText() stripped = %q, want \"important\"", plain)
	}
}

func TestAccentText_NonEmpty(t *testing.T) {
	got := AccentText("highlighted")
	plain := stripANSI(got)
	if plain != "highlighted" {
		t.Errorf("AccentText() stripped = %q, want \"highlighted\"", plain)
	}
}

// =============================================================================
// truncateString (health.go) tests
// =============================================================================

func TestTruncateString_Short(t *testing.T) {
	got := truncateString("hi", 10)
	if got != "hi" {
		t.Errorf("truncateString(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateString_Exact(t *testing.T) {
	got := truncateString("hello", 5)
	if got != "hello" {
		t.Errorf("truncateString(exact) = %q, want \"hello\"", got)
	}
}

func TestTruncateString_Long(t *testing.T) {
	got := truncateString("hello world", 5)
	if len([]rune(got)) > 5 {
		t.Errorf("truncateString(long) = %q, rune count should be <= 5", got)
	}
}

func TestTruncateString_MaxOne(t *testing.T) {
	got := truncateString("hello", 1)
	if got != "h" {
		t.Errorf("truncateString(maxLen=1) = %q, want \"h\"", got)
	}
}

// =============================================================================
// truncateStr (checkpoint.go) tests
// =============================================================================

func TestTruncateStr_Short(t *testing.T) {
	got := truncateStr("hi", 10)
	if got != "hi" {
		t.Errorf("truncateStr(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateStr_Long(t *testing.T) {
	got := truncateStr("hello world this is long", 10)
	if len(got) > 10 {
		t.Errorf("truncateStr(long) len = %d, want <= 10", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncateStr(long) = %q, should end with '...'", got)
	}
}

func TestTruncateStr_MaxThree(t *testing.T) {
	got := truncateStr("hello", 3)
	if got != "..." {
		t.Errorf("truncateStr(maxLen=3) = %q, want \"...\"", got)
	}
}

func TestTruncateStr_MaxTwo(t *testing.T) {
	got := truncateStr("hello", 2)
	if got != ".." {
		t.Errorf("truncateStr(maxLen=2) = %q, want \"..\"", got)
	}
}

func TestTruncateStr_MaxZero(t *testing.T) {
	got := truncateStr("hello", 0)
	if got != "" {
		t.Errorf("truncateStr(maxLen=0) = %q, want \"\"", got)
	}
}

// =============================================================================
// truncate (ensemble_suggest.go) tests
// =============================================================================

func TestTruncate_Short(t *testing.T) {
	got := truncate("hi", 10)
	if got != "hi" {
		t.Errorf("truncate(short) = %q, want \"hi\"", got)
	}
}

func TestTruncate_Long(t *testing.T) {
	got := truncate("hello world this is long", 10)
	if len(got) != 10 {
		t.Errorf("truncate(long) len = %d, want 10", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncate(long) = %q, should end with '...'", got)
	}
}

// =============================================================================
// truncateWithEllipsis (ensemble.go) tests
// =============================================================================

func TestTruncateWithEllipsis_Short(t *testing.T) {
	got := truncateWithEllipsis("hi", 10)
	if got != "hi" {
		t.Errorf("truncateWithEllipsis(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateWithEllipsis_Long(t *testing.T) {
	got := truncateWithEllipsis("hello world", 8)
	if len(got) > 8 {
		t.Errorf("truncateWithEllipsis(long) len = %d, want <= 8", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncateWithEllipsis(long) = %q, should end with '...'", got)
	}
}

func TestTruncateWithEllipsis_MaxZero(t *testing.T) {
	got := truncateWithEllipsis("hello", 0)
	if got != "" {
		t.Errorf("truncateWithEllipsis(maxLen=0) = %q, want \"\"", got)
	}
}

func TestTruncateWithEllipsis_MaxThree(t *testing.T) {
	got := truncateWithEllipsis("hello", 3)
	if len(got) > 3 {
		t.Errorf("truncateWithEllipsis(maxLen=3) len = %d, want <= 3", len(got))
	}
}

// =============================================================================
// truncateSubject (mail.go) tests
// =============================================================================

func TestTruncateSubject_Short(t *testing.T) {
	got := truncateSubject("Hello", 50)
	if got != "Hello" {
		t.Errorf("truncateSubject(short) = %q, want \"Hello\"", got)
	}
}

func TestTruncateSubject_Long(t *testing.T) {
	got := truncateSubject("This is a very long subject line that should be truncated", 20)
	if len(got) > 20 {
		t.Errorf("truncateSubject(long) len = %d, want <= 20", len(got))
	}
}

func TestTruncateSubject_MultiLine(t *testing.T) {
	got := truncateSubject("First line\nSecond line", 50)
	if got != "First line" {
		t.Errorf("truncateSubject(multiline) = %q, want \"First line\"", got)
	}
}

func TestTruncateSubject_MarkdownHeading(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"# Title", "Title"},
		{"## Subtitle", "Subtitle"},
		{"### Section", "Section"},
	}
	for _, tt := range tests {
		got := truncateSubject(tt.input, 50)
		if got != tt.want {
			t.Errorf("truncateSubject(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// =============================================================================
// truncateForDisplay (handoff.go) tests
// =============================================================================

func TestTruncateForDisplay_Short(t *testing.T) {
	got := truncateForDisplay("hi", 10)
	if got != "hi" {
		t.Errorf("truncateForDisplay(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateForDisplay_Long(t *testing.T) {
	got := truncateForDisplay("hello world this is long", 10)
	if len(got) > 10 {
		t.Errorf("truncateForDisplay(long) len = %d, want <= 10", len(got))
	}
}

func TestTruncateForDisplay_MaxTwo(t *testing.T) {
	got := truncateForDisplay("hello", 2)
	if len(got) > 2 {
		t.Errorf("truncateForDisplay(maxLen=2) len = %d, want <= 2", len(got))
	}
}

// =============================================================================
// truncateForPreview (send.go) tests
// =============================================================================

func TestTruncateForPreview_Short(t *testing.T) {
	got := truncateForPreview("hi", 10)
	if got != "hi" {
		t.Errorf("truncateForPreview(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateForPreview_Long(t *testing.T) {
	got := truncateForPreview("hello world this is a very long string", 15)
	if len(got) > 15 {
		t.Errorf("truncateForPreview(long) len = %d, want <= 15", len(got))
	}
}

func TestTruncateForPreview_WithNewlines(t *testing.T) {
	got := truncateForPreview("line1\nline2\nline3", 50)
	if strings.Contains(got, "\n") {
		t.Errorf("truncateForPreview should replace newlines, got %q", got)
	}
}

func TestTruncateForPreview_WhitespaceStripped(t *testing.T) {
	got := truncateForPreview("  hello  ", 50)
	if got != "hello" {
		t.Errorf("truncateForPreview(whitespace) = %q, want \"hello\"", got)
	}
}

// =============================================================================
// truncatePrompt (send.go) tests
// =============================================================================

func TestTruncatePrompt_Short(t *testing.T) {
	got := truncatePrompt("hi", 10)
	if got != "hi" {
		t.Errorf("truncatePrompt(short) = %q, want \"hi\"", got)
	}
}

func TestTruncatePrompt_MaxZero(t *testing.T) {
	got := truncatePrompt("hello", 0)
	if got != "" {
		t.Errorf("truncatePrompt(0) = %q, want \"\"", got)
	}
}

func TestTruncatePrompt_MaxTwo(t *testing.T) {
	got := truncatePrompt("hello", 2)
	if len(got) > 2 {
		t.Errorf("truncatePrompt(2) len = %d, want <= 2", len(got))
	}
}

// =============================================================================
// truncateCassText (cass.go) tests
// =============================================================================

func TestTruncateCassText_Short(t *testing.T) {
	got := truncateCassText("hi", 10)
	if got != "hi" {
		t.Errorf("truncateCassText(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateCassText_ReplacesNewlines(t *testing.T) {
	got := truncateCassText("line1\nline2", 50)
	if strings.Contains(got, "\n") {
		t.Errorf("truncateCassText should replace newlines, got %q", got)
	}
}

func TestTruncateCassText_MaxZero(t *testing.T) {
	got := truncateCassText("hello", 0)
	if got != "" {
		t.Errorf("truncateCassText(0) = %q, want \"\"", got)
	}
}

// =============================================================================
// truncateHistoryStr (history.go) tests
// =============================================================================

func TestTruncateHistoryStr_Short(t *testing.T) {
	got := truncateHistoryStr("hi", 10)
	if got != "hi" {
		t.Errorf("truncateHistoryStr(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateHistoryStr_ReplacesNewlines(t *testing.T) {
	got := truncateHistoryStr("line1\nline2\rline3", 50)
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("truncateHistoryStr should replace newlines, got %q", got)
	}
}

// =============================================================================
// truncateRunes (personas.go) tests
// =============================================================================

func TestTruncateRunes_Short(t *testing.T) {
	got := truncateRunes("hi", 10, "...")
	if got != "hi" {
		t.Errorf("truncateRunes(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateRunes_Long(t *testing.T) {
	got := truncateRunes("hello world", 5, "...")
	if len([]rune(got)) > 8 { // 5 runes + "..." suffix
		t.Errorf("truncateRunes(long) = %q, too long", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncateRunes(long) = %q, should end with '...'", got)
	}
}

func TestTruncateRunes_Unicode(t *testing.T) {
	got := truncateRunes("日本語テスト", 3, "…")
	if len([]rune(got)) > 4 { // 3 runes + "…"
		t.Errorf("truncateRunes(unicode) rune count = %d, want <= 4", len([]rune(got)))
	}
}

// =============================================================================
// splitAndTrim (handoff.go) tests
// =============================================================================

func TestSplitAndTrim_Basic(t *testing.T) {
	got := splitAndTrim("a, b, c", ",")
	if len(got) != 3 {
		t.Errorf("splitAndTrim basic len = %d, want 3", len(got))
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitAndTrim basic = %v", got)
	}
}

func TestSplitAndTrim_EmptyParts(t *testing.T) {
	got := splitAndTrim("a, , b, ,c", ",")
	if len(got) != 3 {
		t.Errorf("splitAndTrim with empties len = %d, want 3", len(got))
	}
}

func TestSplitAndTrim_AllEmpty(t *testing.T) {
	got := splitAndTrim(", , ,", ",")
	if len(got) != 0 {
		t.Errorf("splitAndTrim all empty len = %d, want 0", len(got))
	}
}

func TestSplitAndTrim_SingleValue(t *testing.T) {
	got := splitAndTrim("hello", ",")
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("splitAndTrim single = %v", got)
	}
}

// =============================================================================
// getUnlocksDescription (level.go) tests
// =============================================================================

func TestGetUnlocksDescription_Journeyman(t *testing.T) {
	got := getUnlocksDescription(tiers.TierJourneyman)
	if got == "" {
		t.Error("getUnlocksDescription(Journeyman) should not be empty")
	}
	if !strings.Contains(got, "dashboard") {
		t.Errorf("getUnlocksDescription(Journeyman) = %q, should mention dashboard", got)
	}
}

func TestGetUnlocksDescription_Master(t *testing.T) {
	got := getUnlocksDescription(tiers.TierMaster)
	if got == "" {
		t.Error("getUnlocksDescription(Master) should not be empty")
	}
	if !strings.Contains(got, "robot") {
		t.Errorf("getUnlocksDescription(Master) = %q, should mention robot", got)
	}
}

func TestGetUnlocksDescription_Apprentice(t *testing.T) {
	got := getUnlocksDescription(tiers.TierApprentice)
	if got != "" {
		t.Errorf("getUnlocksDescription(Apprentice) = %q, want empty", got)
	}
}

func TestGetUnlocksDescription_Unknown(t *testing.T) {
	got := getUnlocksDescription(tiers.Tier(99))
	if got != "" {
		t.Errorf("getUnlocksDescription(99) = %q, want empty", got)
	}
}

// =============================================================================
// AgentSpawnContext.AnnotatePrompt tests
// =============================================================================

func TestAnnotatePrompt_WithAnnotation(t *testing.T) {
	sc := &SpawnContext{BatchID: "test-batch", TotalAgents: 4}
	asc := sc.ForAgent(2, 0)
	got := asc.AnnotatePrompt("do something", true)
	if !strings.Contains(got, "Agent 2/4") {
		t.Errorf("AnnotatePrompt() = %q, should contain spawn context", got)
	}
	if !strings.Contains(got, "do something") {
		t.Errorf("AnnotatePrompt() = %q, should contain original prompt", got)
	}
}

func TestAnnotatePrompt_WithoutAnnotation(t *testing.T) {
	sc := &SpawnContext{BatchID: "b", TotalAgents: 2}
	asc := sc.ForAgent(1, 0)
	got := asc.AnnotatePrompt("prompt", false)
	if got != "prompt" {
		t.Errorf("AnnotatePrompt(false) = %q, want \"prompt\"", got)
	}
}

func TestAnnotatePrompt_EmptyPrompt(t *testing.T) {
	sc := &SpawnContext{BatchID: "b", TotalAgents: 1}
	asc := sc.ForAgent(1, 0)
	got := asc.AnnotatePrompt("", true)
	if got != "" {
		t.Errorf("AnnotatePrompt(empty) = %q, want \"\"", got)
	}
}

// =============================================================================
// runeWidth tests
// =============================================================================

func TestRuneWidth_ASCII(t *testing.T) {
	got := runeWidth("hello")
	if got != 5 {
		t.Errorf("runeWidth(\"hello\") = %d, want 5", got)
	}
}

func TestRuneWidth_Empty(t *testing.T) {
	got := runeWidth("")
	if got != 0 {
		t.Errorf("runeWidth(\"\") = %d, want 0", got)
	}
}

// =============================================================================
// calculateMatchConfidence tests
// =============================================================================

func TestCalculateMatchConfidence_ClaudeAnalysis(t *testing.T) {
	bead := bv.BeadPreview{ID: "b1", Title: "Analyze performance bottleneck", Priority: "P1"}
	got := calculateMatchConfidence("claude", bead, "balanced")
	if got < 0.8 {
		t.Errorf("claude+analysis confidence = %.2f, want >= 0.8", got)
	}
}

func TestCalculateMatchConfidence_CodexFeature(t *testing.T) {
	bead := bv.BeadPreview{ID: "b2", Title: "Implement user login feature", Priority: "P1"}
	got := calculateMatchConfidence("codex", bead, "balanced")
	if got < 0.8 {
		t.Errorf("codex+feature confidence = %.2f, want >= 0.8", got)
	}
}

func TestCalculateMatchConfidence_GeminiDocs(t *testing.T) {
	bead := bv.BeadPreview{ID: "b3", Title: "Update documentation for API", Priority: "P2"}
	got := calculateMatchConfidence("gemini", bead, "balanced")
	if got < 0.8 {
		t.Errorf("gemini+docs confidence = %.2f, want >= 0.8", got)
	}
}

func TestCalculateMatchConfidence_SpeedStrategy(t *testing.T) {
	bead := bv.BeadPreview{ID: "b4", Title: "Generic task", Priority: "P2"}
	got := calculateMatchConfidence("claude", bead, "speed")
	if got < 0.7 {
		t.Errorf("speed strategy confidence = %.2f, want >= 0.7", got)
	}
}

func TestCalculateMatchConfidence_DependencyHighPriority(t *testing.T) {
	bead := bv.BeadPreview{ID: "b5", Title: "Generic task", Priority: "P0"}
	got := calculateMatchConfidence("claude", bead, "dependency")
	if got < 0.7 {
		t.Errorf("dependency+P0 confidence = %.2f, want >= 0.7", got)
	}
}

func TestCalculateMatchConfidence_UnknownAgent(t *testing.T) {
	bead := bv.BeadPreview{ID: "b6", Title: "Some task", Priority: "P2"}
	got := calculateMatchConfidence("unknown_agent", bead, "balanced")
	if got < 0.5 || got > 0.8 {
		t.Errorf("unknown agent confidence = %.2f, want ~0.7 (base)", got)
	}
}

func TestCalculateMatchConfidence_BugTask(t *testing.T) {
	bead := bv.BeadPreview{ID: "b7", Title: "Fix broken login", Priority: "P1"}
	got := calculateMatchConfidence("codex", bead, "balanced")
	if got < 0.7 {
		t.Errorf("codex+bug confidence = %.2f, want >= 0.7", got)
	}
}

func TestCalculateMatchConfidence_TestingTask(t *testing.T) {
	bead := bv.BeadPreview{ID: "b8", Title: "Add test coverage", Priority: "P2"}
	got := calculateMatchConfidence("claude", bead, "balanced")
	// "test" → testing, claude has no specific testing strength → base
	if got < 0.5 {
		t.Errorf("claude+testing confidence = %.2f, want >= 0.5", got)
	}
}

// parsePriorityString already tested in assign_test.go

// =============================================================================
// buildReasoning tests
// =============================================================================

func TestBuildReasoning_ClaudeRefactor(t *testing.T) {
	bead := bv.BeadPreview{ID: "r1", Title: "Refactor authentication module", Priority: "P0"}
	got := buildReasoning("claude", bead, "balanced")
	if !strings.Contains(got, "Claude excels") {
		t.Errorf("buildReasoning(claude+refactor) = %q, want Claude excels mention", got)
	}
	if !strings.Contains(got, "critical priority") {
		t.Errorf("buildReasoning(P0) = %q, want critical priority", got)
	}
}

func TestBuildReasoning_CodexImplement(t *testing.T) {
	bead := bv.BeadPreview{ID: "r2", Title: "Implement new feature", Priority: "P1"}
	got := buildReasoning("codex", bead, "speed")
	if !strings.Contains(got, "Codex excels") {
		t.Errorf("buildReasoning(codex+implement) = %q, want Codex excels", got)
	}
	if !strings.Contains(got, "speed") {
		t.Errorf("buildReasoning(speed) = %q, want speed mention", got)
	}
}

func TestBuildReasoning_GeminiDoc(t *testing.T) {
	bead := bv.BeadPreview{ID: "r3", Title: "Update docs", Priority: "P2"}
	got := buildReasoning("gemini", bead, "quality")
	if !strings.Contains(got, "Gemini excels") {
		t.Errorf("buildReasoning(gemini+doc) = %q, want Gemini excels", got)
	}
}

func TestBuildReasoning_NoMatch(t *testing.T) {
	bead := bv.BeadPreview{ID: "r4", Title: "Generic task", Priority: "P3"}
	got := buildReasoning("unknown", bead, "round_robin")
	if got != "available agent matched to available work" {
		t.Errorf("buildReasoning(no match) = %q", got)
	}
}

func TestBuildReasoning_DependencyStrategy(t *testing.T) {
	bead := bv.BeadPreview{ID: "r5", Title: "Generic work", Priority: "P2"}
	got := buildReasoning("claude", bead, "dependency")
	if !strings.Contains(got, "unblocks") {
		t.Errorf("buildReasoning(dependency) = %q, want unblocks mention", got)
	}
}

// inferTaskTypeFromBead already tested in helpers_batch3_test.go

// IsBeadInCycle already tested in assign_pure_test.go

// assignmentAgentName already tested in cli_helpers_test.go

// formatTokenCount already tested in assign_detect_test.go

// =============================================================================
// summarizeAssignmentCounts tests
// =============================================================================

func TestSummarizeAssignmentCounts_Empty(t *testing.T) {
	got := summarizeAssignmentCounts(nil)
	if got.total != 0 || got.working != 0 || got.assigned != 0 || got.failed != 0 {
		t.Errorf("summarizeAssignmentCounts(nil) = %+v, want all zeros", got)
	}
}

func TestSummarizeAssignmentCounts_Mixed(t *testing.T) {
	assignments := []checkpoint.AssignmentSnapshot{
		{Status: "working"},
		{Status: "working"},
		{Status: "assigned"},
		{Status: "failed"},
		{Status: "unknown"},
	}
	got := summarizeAssignmentCounts(assignments)
	if got.total != 5 {
		t.Errorf("total = %d, want 5", got.total)
	}
	if got.working != 2 {
		t.Errorf("working = %d, want 2", got.working)
	}
	if got.assigned != 1 {
		t.Errorf("assigned = %d, want 1", got.assigned)
	}
	if got.failed != 1 {
		t.Errorf("failed = %d, want 1", got.failed)
	}
}

// =============================================================================
// generateAssignmentsEnhanced tests — all 5 strategy branches
// =============================================================================

// helpers to build test agents/beads
func makeTestAgent(paneIndex int, agentType string) assignAgentInfo {
	return assignAgentInfo{
		pane:      tmux.Pane{Index: paneIndex},
		agentType: agentType,
		model:     "test-model",
		state:     "idle",
	}
}

func makeTestBead(id, title, priority string) bv.BeadPreview {
	return bv.BeadPreview{ID: id, Title: title, Priority: priority}
}

// --- Round-Robin strategy ---

func TestGenerateAssignmentsEnhanced_RoundRobin_Basic(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
		makeTestAgent(2, "gemini"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P1"),
		makeTestBead("b3", "Task 3", "P2"),
		makeTestBead("b4", "Task 4", "P2"),
		makeTestBead("b5", "Task 5", "P3"),
	}
	opts := &AssignCommandOptions{Strategy: "round-robin"}
	got := generateAssignmentsEnhanced(agents, beads, opts)

	if len(got) != 5 {
		t.Fatalf("round-robin: got %d assignments, want 5", len(got))
	}
	// Verify round-robin pattern: b1→agent0, b2→agent1, b3→agent2, b4→agent0, b5→agent1
	expectedPanes := []int{0, 1, 2, 0, 1}
	for i, a := range got {
		if a.Pane != expectedPanes[i] {
			t.Errorf("assignment[%d].Pane = %d, want %d", i, a.Pane, expectedPanes[i])
		}
		if a.Score != 1.0 {
			t.Errorf("assignment[%d].Score = %.2f, want 1.0", i, a.Score)
		}
		if a.BeadID != beads[i].ID {
			t.Errorf("assignment[%d].BeadID = %q, want %q", i, a.BeadID, beads[i].ID)
		}
	}
}

func TestGenerateAssignmentsEnhanced_RoundRobin_NoAgents(t *testing.T) {
	beads := []bv.BeadPreview{makeTestBead("b1", "Task 1", "P1")}
	opts := &AssignCommandOptions{Strategy: "round-robin"}
	got := generateAssignmentsEnhanced(nil, beads, opts)
	if len(got) != 0 {
		t.Errorf("round-robin with no agents: got %d assignments, want 0", len(got))
	}
}

func TestGenerateAssignmentsEnhanced_RoundRobin_NoBeads(t *testing.T) {
	agents := []assignAgentInfo{makeTestAgent(0, "claude")}
	opts := &AssignCommandOptions{Strategy: "round-robin"}
	got := generateAssignmentsEnhanced(agents, nil, opts)
	if len(got) != 0 {
		t.Errorf("round-robin with no beads: got %d assignments, want 0", len(got))
	}
}

func TestGenerateAssignmentsEnhanced_RoundRobin_SingleAgent(t *testing.T) {
	agents := []assignAgentInfo{makeTestAgent(0, "claude")}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
		makeTestBead("b3", "Task 3", "P3"),
	}
	opts := &AssignCommandOptions{Strategy: "round-robin"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 3 {
		t.Fatalf("single agent round-robin: got %d, want 3", len(got))
	}
	for i, a := range got {
		if a.Pane != 0 {
			t.Errorf("assignment[%d].Pane = %d, want 0", i, a.Pane)
		}
	}
}

// --- Quality strategy ---

func TestGenerateAssignmentsEnhanced_Quality_BestMatch(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Implement new API endpoint", "P1"), // implementation → codex should score well
	}
	opts := &AssignCommandOptions{Strategy: "quality"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 1 {
		t.Fatalf("quality: got %d assignments, want 1", len(got))
	}
	// Just verify it picks one agent and sets reasoning
	if got[0].Score <= 0 {
		t.Errorf("quality: score = %.2f, want > 0", got[0].Score)
	}
	if got[0].Reasoning == "" {
		t.Error("quality: reasoning should not be empty")
	}
	if got[0].Status != "assigned" {
		t.Errorf("quality: status = %q, want 'assigned'", got[0].Status)
	}
}

func TestGenerateAssignmentsEnhanced_Quality_MoreBeadsThanAgents(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Analyze code", "P1"),
		makeTestBead("b2", "Implement feature", "P1"),
		makeTestBead("b3", "Write docs", "P2"), // No agent left
	}
	opts := &AssignCommandOptions{Strategy: "quality"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	// Quality uses usedAgents map — each agent can only be assigned once
	if len(got) != 2 {
		t.Errorf("quality with 3 beads/2 agents: got %d assignments, want 2", len(got))
	}
	// Verify no duplicate pane assignments
	panes := make(map[int]bool)
	for _, a := range got {
		if panes[a.Pane] {
			t.Errorf("quality: duplicate pane assignment %d", a.Pane)
		}
		panes[a.Pane] = true
	}
}

func TestGenerateAssignmentsEnhanced_Quality_NoAgents(t *testing.T) {
	beads := []bv.BeadPreview{makeTestBead("b1", "Task", "P1")}
	opts := &AssignCommandOptions{Strategy: "quality"}
	got := generateAssignmentsEnhanced(nil, beads, opts)
	if len(got) != 0 {
		t.Errorf("quality with no agents: got %d, want 0", len(got))
	}
}

// --- Speed strategy ---

func TestGenerateAssignmentsEnhanced_Speed_FirstAvailable(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
		makeTestAgent(2, "gemini"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
		makeTestBead("b3", "Task 3", "P2"),
	}
	opts := &AssignCommandOptions{Strategy: "speed"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 3 {
		t.Fatalf("speed: got %d assignments, want 3", len(got))
	}
	// Speed assigns each bead to first available → b1→agent0, b2→agent1, b3→agent2
	for i, a := range got {
		if a.Pane != i {
			t.Errorf("speed assignment[%d].Pane = %d, want %d", i, a.Pane, i)
		}
	}
	// Speed score is (calculateMatchConfidence + 0.9) / 2 → always > 0.45
	for i, a := range got {
		if a.Score < 0.45 {
			t.Errorf("speed assignment[%d].Score = %.2f, want >= 0.45", i, a.Score)
		}
	}
}

func TestGenerateAssignmentsEnhanced_Speed_MoreBeadsThanAgents(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P1"),
		makeTestBead("b3", "Task 3", "P2"),
	}
	opts := &AssignCommandOptions{Strategy: "speed"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	// Speed uses usedAgents — only 1 agent available
	if len(got) != 1 {
		t.Errorf("speed with 3 beads/1 agent: got %d, want 1", len(got))
	}
}

// --- Dependency strategy ---

func TestGenerateAssignmentsEnhanced_Dependency_PriorityBoost(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Critical fix", "P0"),
	}
	opts := &AssignCommandOptions{Strategy: "dependency"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 1 {
		t.Fatalf("dependency: got %d assignments, want 1", len(got))
	}
	// P0 gets a +0.1 boost (capped at 0.95)
	if got[0].Score < 0.7 {
		t.Errorf("dependency+P0: score = %.2f, want >= 0.7", got[0].Score)
	}
	if !strings.Contains(got[0].Reasoning, "unblocks") {
		t.Errorf("dependency: reasoning = %q, want 'unblocks' mention", got[0].Reasoning)
	}
}

func TestGenerateAssignmentsEnhanced_Dependency_MoreBeadsThanAgents(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
	}
	opts := &AssignCommandOptions{Strategy: "dependency"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	// Only 1 agent, so only 1 assignment
	if len(got) != 1 {
		t.Errorf("dependency 2 beads/1 agent: got %d, want 1", len(got))
	}
}

func TestGenerateAssignmentsEnhanced_Dependency_LowPriority(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Low priority task", "P3"),
	}
	opts := &AssignCommandOptions{Strategy: "dependency"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 1 {
		t.Fatalf("dependency P3: got %d, want 1", len(got))
	}
	// P3 → parsePriorityString returns 3, no boost (priority > 1)
	if got[0].Score > 0.95 {
		t.Errorf("dependency P3: score = %.2f, should not be boosted to max", got[0].Score)
	}
}

// --- Balanced (default) strategy ---

func TestGenerateAssignmentsEnhanced_Balanced_EvenSpread(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
		makeTestAgent(2, "gemini"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Analyze code", "P1"),
		makeTestBead("b2", "Implement feature", "P1"),
		makeTestBead("b3", "Write docs", "P2"),
		makeTestBead("b4", "Fix bug", "P1"),
		makeTestBead("b5", "Review PR", "P2"),
		makeTestBead("b6", "Deploy service", "P3"),
	}
	// Empty session → skips LoadStore
	opts := &AssignCommandOptions{Strategy: "balanced", Session: ""}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 6 {
		t.Fatalf("balanced: got %d assignments, want 6", len(got))
	}
	// Balanced should spread work: count per agent
	paneCounts := make(map[int]int)
	for _, a := range got {
		paneCounts[a.Pane]++
	}
	// With 6 beads and 3 agents, each should get 2
	for pane, count := range paneCounts {
		if count != 2 {
			t.Errorf("balanced: pane %d got %d beads, want 2", pane, count)
		}
	}
}

func TestGenerateAssignmentsEnhanced_Balanced_SingleAgent(t *testing.T) {
	agents := []assignAgentInfo{makeTestAgent(0, "claude")}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
	}
	opts := &AssignCommandOptions{Strategy: "balanced"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 2 {
		t.Fatalf("balanced single agent: got %d, want 2", len(got))
	}
	for i, a := range got {
		if a.Pane != 0 {
			t.Errorf("assignment[%d].Pane = %d, want 0", i, a.Pane)
		}
	}
}

func TestGenerateAssignmentsEnhanced_Balanced_NoAgents(t *testing.T) {
	beads := []bv.BeadPreview{makeTestBead("b1", "Task", "P1")}
	opts := &AssignCommandOptions{Strategy: "balanced"}
	got := generateAssignmentsEnhanced(nil, beads, opts)
	if len(got) != 0 {
		t.Errorf("balanced with no agents: got %d, want 0", len(got))
	}
}

func TestGenerateAssignmentsEnhanced_DefaultIsBalanced(t *testing.T) {
	agents := []assignAgentInfo{
		makeTestAgent(0, "claude"),
		makeTestAgent(1, "codex"),
	}
	beads := []bv.BeadPreview{
		makeTestBead("b1", "Task 1", "P1"),
		makeTestBead("b2", "Task 2", "P2"),
	}
	// Unknown strategy falls through to default (balanced)
	opts := &AssignCommandOptions{Strategy: "unknown_strategy"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 2 {
		t.Fatalf("default strategy: got %d, want 2", len(got))
	}
	// Both agents should be used (balanced spread)
	panes := make(map[int]bool)
	for _, a := range got {
		panes[a.Pane] = true
	}
	if len(panes) != 2 {
		t.Errorf("default strategy: used %d agents, want 2", len(panes))
	}
}

// --- Common field verification ---

func TestGenerateAssignmentsEnhanced_CommonFields(t *testing.T) {
	agents := []assignAgentInfo{makeTestAgent(5, "claude")}
	beads := []bv.BeadPreview{makeTestBead("bd-abc", "My Task", "P1")}
	opts := &AssignCommandOptions{Strategy: "round-robin", Session: "test-session"}
	got := generateAssignmentsEnhanced(agents, beads, opts)
	if len(got) != 1 {
		t.Fatalf("common fields: got %d, want 1", len(got))
	}
	a := got[0]
	if a.BeadID != "bd-abc" {
		t.Errorf("BeadID = %q, want 'bd-abc'", a.BeadID)
	}
	if a.BeadTitle != "My Task" {
		t.Errorf("BeadTitle = %q, want 'My Task'", a.BeadTitle)
	}
	if a.Pane != 5 {
		t.Errorf("Pane = %d, want 5", a.Pane)
	}
	if a.AgentType != "claude" {
		t.Errorf("AgentType = %q, want 'claude'", a.AgentType)
	}
	if a.AgentName != "test-session_claude_5" {
		t.Errorf("AgentName = %q, want 'test-session_claude_5'", a.AgentName)
	}
	if a.Status != "assigned" {
		t.Errorf("Status = %q, want 'assigned'", a.Status)
	}
	if a.PromptSent {
		t.Error("PromptSent should be false")
	}
	if a.AssignedAt == "" {
		t.Error("AssignedAt should not be empty")
	}
}

// =============================================================================
// looksLikeAgentName tests (mail.go)
// =============================================================================

func TestLooksLikeAgentName_Valid(t *testing.T) {
	valid := []string{"BlueLake", "GreenCastle", "RedStone", "AB"}
	for _, name := range valid {
		if !looksLikeAgentName(name) {
			t.Errorf("looksLikeAgentName(%q) = false, want true", name)
		}
	}
}

func TestLooksLikeAgentName_Invalid(t *testing.T) {
	invalid := []string{
		"",          // empty
		"bluelake",  // no uppercase
		"Blue Lake", // space
		"Blue_Lake", // underscore
		"Blue-Lake", // hyphen
		"B",         // single char, no second uppercase
		"blueLake",  // lowercase start
		"1BlueLake", // digit start
	}
	for _, name := range invalid {
		if looksLikeAgentName(name) {
			t.Errorf("looksLikeAgentName(%q) = true, want false", name)
		}
	}
}

// =============================================================================
// parseMessageIDs tests (mail.go)
// =============================================================================

func TestParseMessageIDs_Empty(t *testing.T) {
	ids, err := parseMessageIDs(nil)
	if err != nil {
		t.Errorf("nil input: unexpected error: %v", err)
	}
	if ids != nil {
		t.Errorf("nil input: got %v, want nil", ids)
	}
}

func TestParseMessageIDs_Valid(t *testing.T) {
	ids, err := parseMessageIDs([]string{"1", "2", "42"})
	if err != nil {
		t.Fatalf("valid input: unexpected error: %v", err)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 42 {
		t.Errorf("valid input: got %v, want [1 2 42]", ids)
	}
}

func TestParseMessageIDs_Invalid(t *testing.T) {
	_, err := parseMessageIDs([]string{"1", "abc", "3"})
	if err == nil {
		t.Error("invalid input: expected error for non-numeric ID")
	}
	if !strings.Contains(err.Error(), "abc") {
		t.Errorf("error should mention invalid value, got: %v", err)
	}
}

func TestParseMessageIDs_EmptySlice(t *testing.T) {
	ids, err := parseMessageIDs([]string{})
	if err != nil {
		t.Errorf("empty slice: unexpected error: %v", err)
	}
	if ids != nil {
		t.Errorf("empty slice: got %v, want nil", ids)
	}
}

// =============================================================================
// renderTempBar tests (personas.go)
// =============================================================================

func TestRenderTempBar_Focused(t *testing.T) {
	th := theme.Default
	got := renderTempBar(0.2, th)
	plain := stripANSI(got)
	if !strings.Contains(plain, "focused") {
		t.Errorf("renderTempBar(0.2) = %q, want 'focused'", plain)
	}
}

func TestRenderTempBar_Balanced(t *testing.T) {
	th := theme.Default
	got := renderTempBar(0.5, th)
	plain := stripANSI(got)
	if !strings.Contains(plain, "balanced") {
		t.Errorf("renderTempBar(0.5) = %q, want 'balanced'", plain)
	}
}

func TestRenderTempBar_Creative(t *testing.T) {
	th := theme.Default
	got := renderTempBar(0.8, th)
	plain := stripANSI(got)
	if !strings.Contains(plain, "creative") {
		t.Errorf("renderTempBar(0.8) = %q, want 'creative'", plain)
	}
}

func TestRenderTempBar_Wild(t *testing.T) {
	th := theme.Default
	got := renderTempBar(1.5, th)
	plain := stripANSI(got)
	if !strings.Contains(plain, "wild") {
		t.Errorf("renderTempBar(1.5) = %q, want 'wild'", plain)
	}
}

func TestRenderTempBar_Boundaries(t *testing.T) {
	th := theme.Default
	// Exact boundary values
	if plain := stripANSI(renderTempBar(0.3, th)); !strings.Contains(plain, "focused") {
		t.Errorf("renderTempBar(0.3) = %q, want 'focused'", plain)
	}
	if plain := stripANSI(renderTempBar(0.7, th)); !strings.Contains(plain, "balanced") {
		t.Errorf("renderTempBar(0.7) = %q, want 'balanced'", plain)
	}
	if plain := stripANSI(renderTempBar(1.0, th)); !strings.Contains(plain, "creative") {
		t.Errorf("renderTempBar(1.0) = %q, want 'creative'", plain)
	}
}

// =============================================================================
// renderTags tests (personas.go)
// =============================================================================

func TestRenderTags_Multiple(t *testing.T) {
	th := theme.Default
	got := renderTags([]string{"frontend", "api"}, th)
	plain := stripANSI(got)
	if !strings.Contains(plain, "#frontend") {
		t.Errorf("renderTags should contain #frontend, got %q", plain)
	}
	if !strings.Contains(plain, "#api") {
		t.Errorf("renderTags should contain #api, got %q", plain)
	}
}

func TestRenderTags_Empty(t *testing.T) {
	th := theme.Default
	got := renderTags(nil, th)
	if got != "" {
		t.Errorf("renderTags(nil) = %q, want empty", got)
	}
}

func TestRenderTags_Single(t *testing.T) {
	th := theme.Default
	got := renderTags([]string{"backend"}, th)
	plain := stripANSI(got)
	if !strings.Contains(plain, "#backend") {
		t.Errorf("renderTags single = %q, want #backend", plain)
	}
}

// =============================================================================
// valueOrDefault tests (personas.go)
// =============================================================================

func TestValueOrDefault_NonEmpty(t *testing.T) {
	got := valueOrDefault("hello", "default")
	if got != "hello" {
		t.Errorf("valueOrDefault(\"hello\", \"default\") = %q, want \"hello\"", got)
	}
}

func TestValueOrDefault_Empty(t *testing.T) {
	got := valueOrDefault("", "default")
	if got != "default" {
		t.Errorf("valueOrDefault(\"\", \"default\") = %q, want \"default\"", got)
	}
}

// =============================================================================
// formatHandoffMarkdown tests (handoff.go)
// =============================================================================

func TestFormatHandoffMarkdown_Minimal(t *testing.T) {
	h := &handoff.Handoff{
		Session: "test-session",
		Status:  "complete",
		Outcome: "SUCCEEDED",
		Goal:    "Do something great",
		Now:     "Write tests",
	}
	got := formatHandoffMarkdown(h)
	if !strings.Contains(got, "# Handoff: test-session") {
		t.Error("should contain session header")
	}
	if !strings.Contains(got, "complete") {
		t.Error("should contain status")
	}
	if !strings.Contains(got, "Do something great") {
		t.Error("should contain goal")
	}
	if !strings.Contains(got, "Write tests") {
		t.Error("should contain now")
	}
}

func TestFormatHandoffMarkdown_Full(t *testing.T) {
	h := &handoff.Handoff{
		Session: "full-session",
		Status:  "partial",
		Outcome: "PARTIAL_PLUS",
		Goal:    "Build feature",
		Now:     "Continue implementation",
		DoneThisSession: []handoff.TaskRecord{
			{Task: "Created handler"},
			{Task: "Added tests"},
		},
		Next:     []string{"Deploy", "Monitor"},
		Blockers: []string{"API not ready"},
		Decisions: map[string]string{
			"approach": "TDD",
		},
		Files: handoff.FileChanges{
			Created:  []string{"handler.go"},
			Modified: []string{"main.go"},
			Deleted:  []string{"old.go"},
		},
	}
	got := formatHandoffMarkdown(h)
	if !strings.Contains(got, "## Done This Session") {
		t.Error("should contain done section")
	}
	if !strings.Contains(got, "Created handler") {
		t.Error("should contain done task")
	}
	if !strings.Contains(got, "## Next Steps") {
		t.Error("should contain next section")
	}
	if !strings.Contains(got, "Deploy") {
		t.Error("should contain next step")
	}
	if !strings.Contains(got, "## Blockers") {
		t.Error("should contain blockers section")
	}
	if !strings.Contains(got, "API not ready") {
		t.Error("should contain blocker text")
	}
	if !strings.Contains(got, "## Key Decisions") {
		t.Error("should contain decisions section")
	}
	if !strings.Contains(got, "**approach:** TDD") {
		t.Error("should contain decision")
	}
	if !strings.Contains(got, "## File Changes") {
		t.Error("should contain file changes section")
	}
	if !strings.Contains(got, "handler.go") {
		t.Error("should contain created file")
	}
	if !strings.Contains(got, "old.go") {
		t.Error("should contain deleted file")
	}
}

// =============================================================================
// resolveAgentName tests (mail.go)
// =============================================================================

func TestResolveAgentName_AgentNameTitle(t *testing.T) {
	p := tmux.Pane{Title: "BlueLake", Type: tmux.AgentClaude, Index: 1}
	got := resolveAgentName(p)
	if got != "BlueLake" {
		t.Errorf("resolveAgentName with agent title = %q, want 'BlueLake'", got)
	}
}

func TestResolveAgentName_ClaudeFallback(t *testing.T) {
	p := tmux.Pane{Title: "", Type: tmux.AgentClaude, Index: 3}
	got := resolveAgentName(p)
	if got != "ClaudeAgent3" {
		t.Errorf("resolveAgentName claude fallback = %q, want 'ClaudeAgent3'", got)
	}
}

func TestResolveAgentName_CodexFallback(t *testing.T) {
	p := tmux.Pane{Title: "pane_1", Type: tmux.AgentCodex, Index: 2}
	got := resolveAgentName(p)
	if got != "CodexAgent2" {
		t.Errorf("resolveAgentName codex = %q, want 'CodexAgent2'", got)
	}
}

func TestResolveAgentName_GeminiFallback(t *testing.T) {
	p := tmux.Pane{Title: "", Type: tmux.AgentGemini, Index: 0}
	got := resolveAgentName(p)
	if got != "GeminiAgent0" {
		t.Errorf("resolveAgentName gemini = %q, want 'GeminiAgent0'", got)
	}
}

func TestResolveAgentName_NewAgentFallbacks(t *testing.T) {
	tests := []struct {
		name string
		pane tmux.Pane
		want string
	}{
		{name: "cursor", pane: tmux.Pane{Title: "", Type: tmux.AgentCursor, Index: 1}, want: "CursorAgent1"},
		{name: "windsurf alias canonicalized", pane: tmux.Pane{Title: "pane_2", Type: tmux.AgentType("ws"), Index: 2}, want: "WindsurfAgent2"},
		{name: "aider", pane: tmux.Pane{Title: "", Type: tmux.AgentAider, Index: 3}, want: "AiderAgent3"},
		{name: "ollama", pane: tmux.Pane{Title: "", Type: tmux.AgentOllama, Index: 4}, want: "OllamaAgent4"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveAgentName(tc.pane); got != tc.want {
				t.Fatalf("resolveAgentName(%+v) = %q, want %q", tc.pane, got, tc.want)
			}
		})
	}
}

func TestResolveAgentName_UnknownType(t *testing.T) {
	p := tmux.Pane{Title: "", Type: "unknown", Index: 1}
	got := resolveAgentName(p)
	if got != "" {
		t.Errorf("resolveAgentName unknown = %q, want empty", got)
	}
}

func TestResolveAgentName_NonAgentTitle(t *testing.T) {
	// Title that doesn't look like an agent name → fallback
	p := tmux.Pane{Title: "bash", Type: tmux.AgentClaude, Index: 5}
	got := resolveAgentName(p)
	if got != "ClaudeAgent5" {
		t.Errorf("resolveAgentName non-agent title = %q, want 'ClaudeAgent5'", got)
	}
}

func TestFormatHandoffMarkdown_NoOptionalSections(t *testing.T) {
	h := &handoff.Handoff{
		Session: "s",
		Status:  "complete",
		Outcome: "SUCCEEDED",
		Goal:    "g",
		Now:     "n",
	}
	got := formatHandoffMarkdown(h)
	if strings.Contains(got, "## Done This Session") {
		t.Error("should not contain empty done section")
	}
	if strings.Contains(got, "## Next Steps") {
		t.Error("should not contain empty next section")
	}
	if strings.Contains(got, "## Blockers") {
		t.Error("should not contain empty blockers section")
	}
	if strings.Contains(got, "## Key Decisions") {
		t.Error("should not contain empty decisions section")
	}
	if strings.Contains(got, "## File Changes") {
		t.Error("should not contain empty file changes section")
	}
}
