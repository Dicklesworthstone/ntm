package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/cli/tiers"
)

// =============================================================================
// stripANSI tests
// =============================================================================

func TestStripANSI_NoEscapes(t *testing.T) {
	t.Parallel()
	got := stripANSI("hello world")
	if got != "hello world" {
		t.Errorf("stripANSI(plain) = %q, want %q", got, "hello world")
	}
}

func TestStripANSI_WithColors(t *testing.T) {
	t.Parallel()
	input := "\033[31mred\033[0m text"
	got := stripANSI(input)
	if got != "red text" {
		t.Errorf("stripANSI(colored) = %q, want %q", got, "red text")
	}
}

func TestStripANSI_MultipleCodes(t *testing.T) {
	t.Parallel()
	input := "\033[1m\033[32mbold green\033[0m normal"
	got := stripANSI(input)
	if got != "bold green normal" {
		t.Errorf("stripANSI(multi) = %q, want %q", got, "bold green normal")
	}
}

func TestStripANSI_Empty(t *testing.T) {
	t.Parallel()
	got := stripANSI("")
	if got != "" {
		t.Errorf("stripANSI(\"\") = %q, want \"\"", got)
	}
}

// =============================================================================
// padRight tests
// =============================================================================

func TestPadRight_ShorterString(t *testing.T) {
	t.Parallel()
	got := padRight("hi", 5)
	if len(got) < 5 {
		t.Errorf("padRight(\"hi\", 5) = %q, want width >= 5", got)
	}
}

func TestPadRight_ExactWidth(t *testing.T) {
	t.Parallel()
	got := padRight("hello", 5)
	// Should not add padding
	if got != "hello" {
		t.Errorf("padRight(\"hello\", 5) = %q, want \"hello\"", got)
	}
}

func TestPadRight_LongerString(t *testing.T) {
	t.Parallel()
	got := padRight("hello world", 5)
	if got != "hello world" {
		t.Errorf("padRight(longer, 5) = %q, want \"hello world\"", got)
	}
}

func TestPadRight_ZeroWidth(t *testing.T) {
	t.Parallel()
	got := padRight("hi", 0)
	if got != "hi" {
		t.Errorf("padRight(\"hi\", 0) = %q, want \"hi\"", got)
	}
}

// =============================================================================
// Styled text helpers (SectionHeader, SectionDivider, etc.)
// =============================================================================

func TestSectionHeader_ContainsTitle(t *testing.T) {
	t.Parallel()
	got := SectionHeader("Status")
	plain := stripANSI(got)
	if !strings.Contains(plain, "Status") {
		t.Errorf("SectionHeader(\"Status\") stripped = %q, want to contain \"Status\"", plain)
	}
}

func TestSectionDivider_CorrectLength(t *testing.T) {
	t.Parallel()
	got := SectionDivider(20)
	plain := stripANSI(got)
	// Each "─" is 3 bytes in UTF-8 but 1 rune
	runes := []rune(plain)
	if len(runes) != 20 {
		t.Errorf("SectionDivider(20) rune count = %d, want 20", len(runes))
	}
}

func TestKeyValue_Format(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	got := ErrorMessage("failed")
	plain := stripANSI(got)
	if !strings.Contains(plain, "✗") {
		t.Errorf("ErrorMessage() = %q, should contain ✗", plain)
	}
}

func TestWarningMessage_ContainsIcon(t *testing.T) {
	t.Parallel()
	got := WarningMessage("caution")
	plain := stripANSI(got)
	if !strings.Contains(plain, "⚠") {
		t.Errorf("WarningMessage() = %q, should contain ⚠", plain)
	}
}

func TestInfoMessage_ContainsIcon(t *testing.T) {
	t.Parallel()
	got := InfoMessage("note")
	plain := stripANSI(got)
	if !strings.Contains(plain, "ℹ") {
		t.Errorf("InfoMessage() = %q, should contain ℹ", plain)
	}
}

func TestSubtleText_NonEmpty(t *testing.T) {
	t.Parallel()
	got := SubtleText("muted")
	plain := stripANSI(got)
	if plain != "muted" {
		t.Errorf("SubtleText() stripped = %q, want \"muted\"", plain)
	}
}

func TestBoldText_NonEmpty(t *testing.T) {
	t.Parallel()
	got := BoldText("important")
	plain := stripANSI(got)
	if plain != "important" {
		t.Errorf("BoldText() stripped = %q, want \"important\"", plain)
	}
}

func TestAccentText_NonEmpty(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	got := truncateString("hi", 10)
	if got != "hi" {
		t.Errorf("truncateString(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateString_Exact(t *testing.T) {
	t.Parallel()
	got := truncateString("hello", 5)
	if got != "hello" {
		t.Errorf("truncateString(exact) = %q, want \"hello\"", got)
	}
}

func TestTruncateString_Long(t *testing.T) {
	t.Parallel()
	got := truncateString("hello world", 5)
	if len([]rune(got)) > 5 {
		t.Errorf("truncateString(long) = %q, rune count should be <= 5", got)
	}
}

func TestTruncateString_MaxOne(t *testing.T) {
	t.Parallel()
	got := truncateString("hello", 1)
	if got != "h" {
		t.Errorf("truncateString(maxLen=1) = %q, want \"h\"", got)
	}
}

// =============================================================================
// truncateStr (checkpoint.go) tests
// =============================================================================

func TestTruncateStr_Short(t *testing.T) {
	t.Parallel()
	got := truncateStr("hi", 10)
	if got != "hi" {
		t.Errorf("truncateStr(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateStr_Long(t *testing.T) {
	t.Parallel()
	got := truncateStr("hello world this is long", 10)
	if len(got) > 10 {
		t.Errorf("truncateStr(long) len = %d, want <= 10", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncateStr(long) = %q, should end with '...'", got)
	}
}

func TestTruncateStr_MaxThree(t *testing.T) {
	t.Parallel()
	got := truncateStr("hello", 3)
	if got != "..." {
		t.Errorf("truncateStr(maxLen=3) = %q, want \"...\"", got)
	}
}

func TestTruncateStr_MaxTwo(t *testing.T) {
	t.Parallel()
	got := truncateStr("hello", 2)
	if got != ".." {
		t.Errorf("truncateStr(maxLen=2) = %q, want \"..\"", got)
	}
}

func TestTruncateStr_MaxZero(t *testing.T) {
	t.Parallel()
	got := truncateStr("hello", 0)
	if got != "" {
		t.Errorf("truncateStr(maxLen=0) = %q, want \"\"", got)
	}
}

// =============================================================================
// truncate (ensemble_suggest.go) tests
// =============================================================================

func TestTruncate_Short(t *testing.T) {
	t.Parallel()
	got := truncate("hi", 10)
	if got != "hi" {
		t.Errorf("truncate(short) = %q, want \"hi\"", got)
	}
}

func TestTruncate_Long(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	got := truncateWithEllipsis("hi", 10)
	if got != "hi" {
		t.Errorf("truncateWithEllipsis(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateWithEllipsis_Long(t *testing.T) {
	t.Parallel()
	got := truncateWithEllipsis("hello world", 8)
	if len(got) > 8 {
		t.Errorf("truncateWithEllipsis(long) len = %d, want <= 8", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncateWithEllipsis(long) = %q, should end with '...'", got)
	}
}

func TestTruncateWithEllipsis_MaxZero(t *testing.T) {
	t.Parallel()
	got := truncateWithEllipsis("hello", 0)
	if got != "" {
		t.Errorf("truncateWithEllipsis(maxLen=0) = %q, want \"\"", got)
	}
}

func TestTruncateWithEllipsis_MaxThree(t *testing.T) {
	t.Parallel()
	got := truncateWithEllipsis("hello", 3)
	if len(got) > 3 {
		t.Errorf("truncateWithEllipsis(maxLen=3) len = %d, want <= 3", len(got))
	}
}

// =============================================================================
// truncateSubject (mail.go) tests
// =============================================================================

func TestTruncateSubject_Short(t *testing.T) {
	t.Parallel()
	got := truncateSubject("Hello", 50)
	if got != "Hello" {
		t.Errorf("truncateSubject(short) = %q, want \"Hello\"", got)
	}
}

func TestTruncateSubject_Long(t *testing.T) {
	t.Parallel()
	got := truncateSubject("This is a very long subject line that should be truncated", 20)
	if len(got) > 20 {
		t.Errorf("truncateSubject(long) len = %d, want <= 20", len(got))
	}
}

func TestTruncateSubject_MultiLine(t *testing.T) {
	t.Parallel()
	got := truncateSubject("First line\nSecond line", 50)
	if got != "First line" {
		t.Errorf("truncateSubject(multiline) = %q, want \"First line\"", got)
	}
}

func TestTruncateSubject_MarkdownHeading(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	got := truncateForDisplay("hi", 10)
	if got != "hi" {
		t.Errorf("truncateForDisplay(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateForDisplay_Long(t *testing.T) {
	t.Parallel()
	got := truncateForDisplay("hello world this is long", 10)
	if len(got) > 10 {
		t.Errorf("truncateForDisplay(long) len = %d, want <= 10", len(got))
	}
}

func TestTruncateForDisplay_MaxTwo(t *testing.T) {
	t.Parallel()
	got := truncateForDisplay("hello", 2)
	if len(got) > 2 {
		t.Errorf("truncateForDisplay(maxLen=2) len = %d, want <= 2", len(got))
	}
}

// =============================================================================
// truncateForPreview (send.go) tests
// =============================================================================

func TestTruncateForPreview_Short(t *testing.T) {
	t.Parallel()
	got := truncateForPreview("hi", 10)
	if got != "hi" {
		t.Errorf("truncateForPreview(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateForPreview_Long(t *testing.T) {
	t.Parallel()
	got := truncateForPreview("hello world this is a very long string", 15)
	if len(got) > 15 {
		t.Errorf("truncateForPreview(long) len = %d, want <= 15", len(got))
	}
}

func TestTruncateForPreview_WithNewlines(t *testing.T) {
	t.Parallel()
	got := truncateForPreview("line1\nline2\nline3", 50)
	if strings.Contains(got, "\n") {
		t.Errorf("truncateForPreview should replace newlines, got %q", got)
	}
}

func TestTruncateForPreview_WhitespaceStripped(t *testing.T) {
	t.Parallel()
	got := truncateForPreview("  hello  ", 50)
	if got != "hello" {
		t.Errorf("truncateForPreview(whitespace) = %q, want \"hello\"", got)
	}
}

// =============================================================================
// truncatePrompt (send.go) tests
// =============================================================================

func TestTruncatePrompt_Short(t *testing.T) {
	t.Parallel()
	got := truncatePrompt("hi", 10)
	if got != "hi" {
		t.Errorf("truncatePrompt(short) = %q, want \"hi\"", got)
	}
}

func TestTruncatePrompt_MaxZero(t *testing.T) {
	t.Parallel()
	got := truncatePrompt("hello", 0)
	if got != "" {
		t.Errorf("truncatePrompt(0) = %q, want \"\"", got)
	}
}

func TestTruncatePrompt_MaxTwo(t *testing.T) {
	t.Parallel()
	got := truncatePrompt("hello", 2)
	if len(got) > 2 {
		t.Errorf("truncatePrompt(2) len = %d, want <= 2", len(got))
	}
}

// =============================================================================
// truncateCassText (cass.go) tests
// =============================================================================

func TestTruncateCassText_Short(t *testing.T) {
	t.Parallel()
	got := truncateCassText("hi", 10)
	if got != "hi" {
		t.Errorf("truncateCassText(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateCassText_ReplacesNewlines(t *testing.T) {
	t.Parallel()
	got := truncateCassText("line1\nline2", 50)
	if strings.Contains(got, "\n") {
		t.Errorf("truncateCassText should replace newlines, got %q", got)
	}
}

func TestTruncateCassText_MaxZero(t *testing.T) {
	t.Parallel()
	got := truncateCassText("hello", 0)
	if got != "" {
		t.Errorf("truncateCassText(0) = %q, want \"\"", got)
	}
}

// =============================================================================
// truncateHistoryStr (history.go) tests
// =============================================================================

func TestTruncateHistoryStr_Short(t *testing.T) {
	t.Parallel()
	got := truncateHistoryStr("hi", 10)
	if got != "hi" {
		t.Errorf("truncateHistoryStr(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateHistoryStr_ReplacesNewlines(t *testing.T) {
	t.Parallel()
	got := truncateHistoryStr("line1\nline2\rline3", 50)
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("truncateHistoryStr should replace newlines, got %q", got)
	}
}

// =============================================================================
// truncateRunes (personas.go) tests
// =============================================================================

func TestTruncateRunes_Short(t *testing.T) {
	t.Parallel()
	got := truncateRunes("hi", 10, "...")
	if got != "hi" {
		t.Errorf("truncateRunes(short) = %q, want \"hi\"", got)
	}
}

func TestTruncateRunes_Long(t *testing.T) {
	t.Parallel()
	got := truncateRunes("hello world", 5, "...")
	if len([]rune(got)) > 8 { // 5 runes + "..." suffix
		t.Errorf("truncateRunes(long) = %q, too long", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncateRunes(long) = %q, should end with '...'", got)
	}
}

func TestTruncateRunes_Unicode(t *testing.T) {
	t.Parallel()
	got := truncateRunes("日本語テスト", 3, "…")
	if len([]rune(got)) > 4 { // 3 runes + "…"
		t.Errorf("truncateRunes(unicode) rune count = %d, want <= 4", len([]rune(got)))
	}
}

// =============================================================================
// splitAndTrim (handoff.go) tests
// =============================================================================

func TestSplitAndTrim_Basic(t *testing.T) {
	t.Parallel()
	got := splitAndTrim("a, b, c", ",")
	if len(got) != 3 {
		t.Errorf("splitAndTrim basic len = %d, want 3", len(got))
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitAndTrim basic = %v", got)
	}
}

func TestSplitAndTrim_EmptyParts(t *testing.T) {
	t.Parallel()
	got := splitAndTrim("a, , b, ,c", ",")
	if len(got) != 3 {
		t.Errorf("splitAndTrim with empties len = %d, want 3", len(got))
	}
}

func TestSplitAndTrim_AllEmpty(t *testing.T) {
	t.Parallel()
	got := splitAndTrim(", , ,", ",")
	if len(got) != 0 {
		t.Errorf("splitAndTrim all empty len = %d, want 0", len(got))
	}
}

func TestSplitAndTrim_SingleValue(t *testing.T) {
	t.Parallel()
	got := splitAndTrim("hello", ",")
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("splitAndTrim single = %v", got)
	}
}

// =============================================================================
// getUnlocksDescription (level.go) tests
// =============================================================================

func TestGetUnlocksDescription_Journeyman(t *testing.T) {
	t.Parallel()
	got := getUnlocksDescription(tiers.TierJourneyman)
	if got == "" {
		t.Error("getUnlocksDescription(Journeyman) should not be empty")
	}
	if !strings.Contains(got, "dashboard") {
		t.Errorf("getUnlocksDescription(Journeyman) = %q, should mention dashboard", got)
	}
}

func TestGetUnlocksDescription_Master(t *testing.T) {
	t.Parallel()
	got := getUnlocksDescription(tiers.TierMaster)
	if got == "" {
		t.Error("getUnlocksDescription(Master) should not be empty")
	}
	if !strings.Contains(got, "robot") {
		t.Errorf("getUnlocksDescription(Master) = %q, should mention robot", got)
	}
}

func TestGetUnlocksDescription_Apprentice(t *testing.T) {
	t.Parallel()
	got := getUnlocksDescription(tiers.TierApprentice)
	if got != "" {
		t.Errorf("getUnlocksDescription(Apprentice) = %q, want empty", got)
	}
}

func TestGetUnlocksDescription_Unknown(t *testing.T) {
	t.Parallel()
	got := getUnlocksDescription(tiers.Tier(99))
	if got != "" {
		t.Errorf("getUnlocksDescription(99) = %q, want empty", got)
	}
}

// =============================================================================
// AgentSpawnContext.AnnotatePrompt tests
// =============================================================================

func TestAnnotatePrompt_WithAnnotation(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	sc := &SpawnContext{BatchID: "b", TotalAgents: 2}
	asc := sc.ForAgent(1, 0)
	got := asc.AnnotatePrompt("prompt", false)
	if got != "prompt" {
		t.Errorf("AnnotatePrompt(false) = %q, want \"prompt\"", got)
	}
}

func TestAnnotatePrompt_EmptyPrompt(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	got := runeWidth("hello")
	if got != 5 {
		t.Errorf("runeWidth(\"hello\") = %d, want 5", got)
	}
}

func TestRuneWidth_Empty(t *testing.T) {
	t.Parallel()
	got := runeWidth("")
	if got != 0 {
		t.Errorf("runeWidth(\"\") = %d, want 0", got)
	}
}
