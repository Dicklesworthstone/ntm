package tokens

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},                    // 1 char -> 1 token (min 1)
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

	// Minimum 1 token check
	if got := EstimateTokensWithLanguageHint("a", ContentCode); got != 1 {
		t.Errorf("ContentCode min 1: got %d, want 1", got)
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
		{"gemini-pro", 32000},
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
		{"\u00a0{\"key\": \"value\"}\u00a0", ContentJSON},
		{"# Markdown Title\n- Item", ContentMarkdown},
		{"func main() { fmt.Println() }", ContentCode},
		{"Just some regular text.", ContentUnknown}, // Now unknown, as it's not clearly code/json/md
		{"Short", ContentUnknown},
		{"Some ambiguous content that is not clearly any specific type but is longer than 10 chars.", ContentUnknown},
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
	if info.EstimatedTokens != 3 {
		t.Errorf("EstimatedTokens = %d, want 3", info.EstimatedTokens)
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

// =============================================================================
// bd-25o0: Additional Token Estimation Tests
// =============================================================================

func TestTokenCountingAccuracy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		text        string
		contentType ContentType
		minTokens   int
		maxTokens   int
	}{
		{
			name:        "empty string",
			text:        "",
			contentType: ContentUnknown,
			minTokens:   0,
			maxTokens:   0,
		},
		{
			name:        "single character",
			text:        "a",
			contentType: ContentUnknown,
			minTokens:   1,
			maxTokens:   1,
		},
		{
			name:        "typical code snippet",
			text:        `func main() { fmt.Println("Hello, World!") }`,
			contentType: ContentCode,
			minTokens:   10,
			maxTokens:   20,
		},
		{
			name:        "prose paragraph",
			text:        "This is a typical English paragraph that contains multiple sentences. It should tokenize according to the prose heuristics.",
			contentType: ContentProse,
			minTokens:   20,
			maxTokens:   40,
		},
		{
			name:        "JSON object",
			text:        `{"name": "test", "value": 123, "nested": {"key": "val"}}`,
			contentType: ContentJSON,
			minTokens:   10,
			maxTokens:   25,
		},
		{
			name:        "markdown document",
			text:        "# Header\n\n## Subheader\n\n- List item 1\n- List item 2\n\n```go\nfunc test() {}\n```",
			contentType: ContentMarkdown,
			minTokens:   15,
			maxTokens:   30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens := EstimateTokensWithLanguageHint(tt.text, tt.contentType)

			t.Logf("COST_TEST: %s | Tokens=%d | ContentType=%d | TextLen=%d",
				tt.name, tokens, tt.contentType, len(tt.text))

			if tokens < tt.minTokens || tokens > tt.maxTokens {
				t.Errorf("EstimateTokensWithLanguageHint() = %d, want between %d and %d",
					tokens, tt.minTokens, tt.maxTokens)
			}
		})
	}
}

func TestMultiModelContextLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model     string
		wantLimit int
	}{
		// Claude models
		{"claude-opus-4", 200000},
		{"claude-3-opus", 200000},
		{"claude-3-5-sonnet", 200000},
		{"claude-sonnet", 200000},
		{"claude-haiku", 200000},
		{"opus", 200000},
		{"sonnet", 200000},
		{"haiku", 200000},

		// OpenAI models
		{"gpt-4", 128000},
		{"gpt-4o", 128000},
		{"gpt4-turbo", 128000},
		{"o1", 128000},
		{"o1-mini", 128000},
		{"codex", 256000},

		// Google models
		{"gemini", 1000000},
		{"gemini-pro", 32000},
		{"gemini-flash", 1000000},
		{"gemini-ultra", 1000000},

		// Claude with "3.5" variant
		{"claude-3.5-sonnet", 200000},

		// Unknown models fallback
		{"unknown-model-123", 128000},
		{"some-new-model", 128000},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()

			limit := GetContextLimit(tt.model)

			t.Logf("COST_TEST: Model=%s | ContextLimit=%d", tt.model, limit)

			if limit != tt.wantLimit {
				t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, limit, tt.wantLimit)
			}
		})
	}
}

func TestInputOutputTokenDistinction(t *testing.T) {
	t.Parallel()

	// Simulate input/output token counting
	inputPrompt := "Write a function that sorts an array"
	outputResponse := "Here's a function that sorts an array using the quicksort algorithm:\n\n```go\nfunc quickSort(arr []int) []int {\n    if len(arr) <= 1 {\n        return arr\n    }\n    pivot := arr[len(arr)/2]\n    var left, right, equal []int\n    for _, v := range arr {\n        switch {\n        case v < pivot:\n            left = append(left, v)\n        case v > pivot:\n            right = append(right, v)\n        default:\n            equal = append(equal, v)\n        }\n    }\n    return append(append(quickSort(left), equal...), quickSort(right)...)\n}\n```"

	inputTokens := EstimateTokens(inputPrompt)
	outputTokens := EstimateTokens(outputResponse)

	t.Logf("COST_TEST: InputOutput | InputTokens=%d | OutputTokens=%d | Ratio=%.2f",
		inputTokens, outputTokens, float64(outputTokens)/float64(inputTokens))

	// Output should typically be longer than input for generation tasks
	if outputTokens <= inputTokens {
		t.Logf("Note: Output (%d) not longer than input (%d) - this is expected for some cases",
			outputTokens, inputTokens)
	}

	// Both should be positive
	if inputTokens <= 0 {
		t.Errorf("Input tokens should be positive, got %d", inputTokens)
	}
	if outputTokens <= 0 {
		t.Errorf("Output tokens should be positive, got %d", outputTokens)
	}
}

func TestStreamingTokenAccumulation(t *testing.T) {
	t.Parallel()

	// Simulate streaming chunks
	chunks := []string{
		"Here's ",
		"a ",
		"response ",
		"that ",
		"comes ",
		"in ",
		"streaming ",
		"chunks. ",
		"Each chunk adds to the total token count.",
	}

	var accumulatedTokens int
	for _, chunk := range chunks {
		accumulatedTokens += EstimateTokens(chunk)
	}

	// Full text for comparison
	fullText := ""
	for _, chunk := range chunks {
		fullText += chunk
	}
	fullTokens := EstimateTokens(fullText)

	t.Logf("COST_TEST: StreamingAccumulation | ChunkedTokens=%d | FullTokens=%d | Diff=%d",
		accumulatedTokens, fullTokens, accumulatedTokens-fullTokens)

	// Chunked estimation may differ from full text due to minimum 1 token per chunk
	// The difference should be bounded
	diff := accumulatedTokens - fullTokens
	maxDiff := len(chunks) // At most 1 extra token per chunk due to minimum
	if diff > maxDiff {
		t.Errorf("Token accumulation difference too large: got %d, max expected %d",
			diff, maxDiff)
	}
}

func TestContentTypeDetectionEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		expected ContentType
	}{
		{
			name:     "array JSON",
			text:     `[1, 2, 3, "four", {"nested": true}]`,
			expected: ContentJSON,
		},
		{
			name:     "markdown with code fence",
			text:     "Here's some code:\n```python\nprint('hello')\n```\nEnd.",
			expected: ContentMarkdown,
		},
		{
			name:     "code with many special chars",
			text:     "if (x > 0) { y = f(x); z = g(y); return z; }",
			expected: ContentCode,
		},
		{
			name:     "very short text",
			text:     "Hi",
			expected: ContentUnknown,
		},
		{
			name:     "markdown list",
			text:     "# Title\n- [ ] Task 1\n- [x] Task 2\n- [ ] Task 3",
			expected: ContentMarkdown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			detected := DetectContentType(tt.text)

			t.Logf("COST_TEST: ContentDetection | Case=%s | Detected=%d | Expected=%d",
				tt.name, detected, tt.expected)

			if detected != tt.expected {
				t.Errorf("DetectContentType() = %v, want %v", detected, tt.expected)
			}
		})
	}
}

func TestUsageInfoComprehensive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		model    string
		minUsage float64
		maxUsage float64
	}{
		{
			name:     "small text on Claude",
			text:     "Hello world",
			model:    "claude-opus",
			minUsage: 0.0,
			maxUsage: 0.01, // 3 tokens / 200k = 0.0015%
		},
		{
			name:     "medium text on GPT-4",
			text:     string(make([]byte, 10000)), // 10k chars ~= 2.8k tokens
			model:    "gpt-4",
			minUsage: 1.0,
			maxUsage: 5.0, // ~2.8k / 128k = ~2.2%
		},
		{
			name:     "text on Gemini (large context)",
			text:     string(make([]byte, 10000)),
			model:    "gemini-2.0-flash",
			minUsage: 0.1,
			maxUsage: 1.0, // ~2.8k / 1M = ~0.28%
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := GetUsageInfo(tt.text, tt.model)

			t.Logf("COST_TEST: UsageInfo | Case=%s | Tokens=%d | Model=%s | Usage=%.4f%%",
				tt.name, info.EstimatedTokens, info.Model, info.UsagePercent)

			if info.UsagePercent < tt.minUsage || info.UsagePercent > tt.maxUsage {
				t.Errorf("UsagePercent = %f, want between %f and %f",
					info.UsagePercent, tt.minUsage, tt.maxUsage)
			}

			if !info.IsEstimate {
				t.Error("IsEstimate should always be true")
			}
		})
	}
}
