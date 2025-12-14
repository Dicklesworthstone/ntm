package tokens

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hello world", 3},          // 11 chars * 10 / 35 = 3
		{"short", 1},                // 5 * 10 / 35 = 1
		{"longer sentence here", 5}, // 20 * 10 / 35 = 200 / 35 = 5 (integer division)
	}

	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestEstimateTokensWithLanguageHint(t *testing.T) {
	text := "function test() { return true; }" // 32 chars

	// Code: 32 / 2.8 = 11.4 -> 11
	if got := EstimateTokensWithLanguageHint(text, ContentCode); got != 11 {
		t.Errorf("ContentCode: got %d, want 11", got)
	}

	// Prose: 32 / 4.0 = 8
	if got := EstimateTokensWithLanguageHint(text, ContentProse); got != 8 {
		t.Errorf("ContentProse: got %d, want 8", got)
	}

	// Unknown: 32 / 3.5 = 9.1 -> 9
	if got := EstimateTokensWithLanguageHint(text, ContentUnknown); got != 9 {
		t.Errorf("ContentUnknown: got %d, want 9", got)
	}
}

func TestEstimateWithOverhead(t *testing.T) {
	text := "hello world" // 3 tokens

	// 3 * 2.0 = 6
	if got := EstimateWithOverhead(text, 2.0); got != 6 {
		t.Errorf("EstimateWithOverhead(2.0) = %d, want 6", got)
	}
}

func TestGetContextLimit(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-3-5-sonnet", 200000},
		{"gpt-4", 128000},
		{"gemini-pro", 1000000},
		{"unknown-model", 128000}, // Default
	}

	for _, tt := range tests {
		got := GetContextLimit(tt.model)
		if got != tt.want {
			t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestUsagePercentage(t *testing.T) {
	// gpt-4 limit 128k
	// 64k tokens = 50%
	got := UsagePercentage(64000, "gpt-4")
	if got != 50.0 {
		t.Errorf("UsagePercentage(64k, gpt-4) = %f, want 50.0", got)
	}

	// Unknown model (128k default)
	got = UsagePercentage(64000, "foo")
	if got != 50.0 {
		t.Errorf("UsagePercentage(64k, foo) = %f, want 50.0", got)
	}
}

func TestDetectContentType(t *testing.T) {
	tests := []struct {
		input string
		want  ContentType
	}{
		{`{"key": "value"}`, ContentJSON},
		{"# Markdown Title\n- Item", ContentMarkdown},
		{"func main() { fmt.Println() }", ContentCode},
		{"Just some regular text.", ContentProse},
		{"Short", ContentUnknown},
	}

	for _, tt := range tests {
		got := DetectContentType(tt.input)
		if got != tt.want {
			t.Errorf("DetectContentType(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestGetUsageInfo(t *testing.T) {
	info := GetUsageInfo("hello world", "gpt-4")
	if info.EstimatedTokens != 2 {
		t.Errorf("EstimatedTokens = %d, want 2", info.EstimatedTokens)
	}
	if info.ContextLimit != 128000 {
		t.Errorf("ContextLimit = %d, want 128000", info.ContextLimit)
	}
	if info.IsEstimate != true {
		t.Error("IsEstimate should be true")
	}
}

func TestSmartEstimate(t *testing.T) {
	code := "func main() {}" // 14 chars
	// Code (2.8): 14/2.8 = 5
	// Basic (3.5): 14/3.5 = 4

	got := SmartEstimate(code)
	if got != 5 {
		t.Errorf("SmartEstimate code = %d, want 5", got)
	}
}
