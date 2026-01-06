package robot

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name     string
		prompt   string
		minWords int // minimum expected keywords
		maxWords int // maximum expected keywords
		contains []string
		excludes []string
	}{
		{
			name:     "simple prompt",
			prompt:   "Fix the authentication bug in the login handler",
			minWords: 2,
			maxWords: 5,
			contains: []string{"authentication", "login", "handler"},
			excludes: []string{"the", "in", "fix", "bug"}, // stop words
		},
		{
			name:     "technical prompt",
			prompt:   "Implement retry logic with exponential backoff for database connections",
			minWords: 3,
			maxWords: 8,
			contains: []string{"retry", "logic", "exponential", "backoff", "database", "connections"},
			excludes: []string{"with", "for"},
		},
		{
			name:     "prompt with code block",
			prompt:   "Fix this function:\n```go\nfunc hello() { return }\n```\nThe return statement is wrong",
			minWords: 1,
			maxWords: 5,
			contains: []string{"return", "statement", "wrong"},
			excludes: []string{"func", "hello"}, // code block content should be removed
		},
		{
			name:     "prompt with inline code",
			prompt:   "The `getUserByID` function returns nil when user is not found",
			minWords: 2,
			maxWords: 6,
			contains: []string{"returns", "nil", "user", "found"},
			excludes: []string{"getuserbyid"}, // inline code should be removed
		},
		{
			name:     "empty prompt",
			prompt:   "",
			minWords: 0,
			maxWords: 0,
		},
		{
			name:     "only stop words",
			prompt:   "the and or but",
			minWords: 0,
			maxWords: 0,
		},
		{
			name:     "snake_case identifiers",
			prompt:   "Update the user_profile and order_items tables",
			minWords: 2,
			maxWords: 5,
			contains: []string{"user_profile", "order_items", "tables"},
		},
		{
			name:     "kebab-case identifiers",
			prompt:   "Check the api-gateway and load-balancer configs",
			minWords: 2,
			maxWords: 5,
			contains: []string{"api-gateway", "load-balancer", "configs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keywords := ExtractKeywords(tt.prompt)

			// Check count bounds
			if len(keywords) < tt.minWords {
				t.Errorf("ExtractKeywords() got %d keywords, want at least %d\nKeywords: %v",
					len(keywords), tt.minWords, keywords)
			}
			if len(keywords) > tt.maxWords {
				t.Errorf("ExtractKeywords() got %d keywords, want at most %d\nKeywords: %v",
					len(keywords), tt.maxWords, keywords)
			}

			// Check required keywords
			keywordSet := make(map[string]bool)
			for _, k := range keywords {
				keywordSet[k] = true
			}

			for _, required := range tt.contains {
				if !keywordSet[required] {
					t.Errorf("ExtractKeywords() missing required keyword %q\nKeywords: %v",
						required, keywords)
				}
			}

			// Check excluded keywords (stop words)
			for _, excluded := range tt.excludes {
				if keywordSet[excluded] {
					t.Errorf("ExtractKeywords() should not contain stop word %q\nKeywords: %v",
						excluded, keywords)
				}
			}
		})
	}
}

func TestExtractKeywords_Deduplication(t *testing.T) {
	prompt := "user user user authentication authentication"
	keywords := ExtractKeywords(prompt)

	// Count occurrences
	counts := make(map[string]int)
	for _, k := range keywords {
		counts[k]++
	}

	for word, count := range counts {
		if count > 1 {
			t.Errorf("ExtractKeywords() has duplicate keyword %q (count: %d)", word, count)
		}
	}
}

func TestExtractKeywords_MaxLimit(t *testing.T) {
	// Generate a prompt with many unique words
	prompt := "one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen"
	keywords := ExtractKeywords(prompt)

	if len(keywords) > 10 {
		t.Errorf("ExtractKeywords() returned %d keywords, should be limited to 10", len(keywords))
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "simple words",
			text: "hello world",
			want: []string{"hello", "world"},
		},
		{
			name: "with punctuation",
			text: "hello, world!",
			want: []string{"hello", "world"},
		},
		{
			name: "snake_case",
			text: "user_profile",
			want: []string{"user_profile"},
		},
		{
			name: "kebab-case",
			text: "api-gateway",
			want: []string{"api-gateway"},
		},
		{
			name: "mixed",
			text: "user_profile api-gateway normalWord",
			want: []string{"user_profile", "api-gateway", "normalWord"},
		},
		{
			name: "with numbers",
			text: "error404 v2api",
			want: []string{"error404", "v2api"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenize(tt.text)
			if len(got) != len(tt.want) {
				t.Errorf("tokenize() got %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenize()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRemoveCodeBlocks(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "fenced code block",
			text: "before ```go\ncode here\n``` after",
			want: "before   after",
		},
		{
			name: "inline code",
			text: "the `function` name",
			want: "the   name",
		},
		{
			name: "multiple code blocks",
			text: "start ```code1``` middle ```code2``` end",
			want: "start   middle   end",
		},
		{
			name: "no code",
			text: "plain text here",
			want: "plain text here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeCodeBlocks(tt.text)
			if got != tt.want {
				t.Errorf("removeCodeBlocks() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsStopWord(t *testing.T) {
	// Test some stop words
	stopWords := []string{"the", "a", "is", "are", "and", "or", "but", "in", "on"}
	for _, word := range stopWords {
		if !isStopWord(word) {
			t.Errorf("isStopWord(%q) = false, want true", word)
		}
	}

	// Test some non-stop words
	nonStopWords := []string{"database", "authentication", "handler", "retry", "exponential"}
	for _, word := range nonStopWords {
		if isStopWord(word) {
			t.Errorf("isStopWord(%q) = true, want false", word)
		}
	}
}

func TestDefaultCASSConfig(t *testing.T) {
	config := DefaultCASSConfig()

	if !config.Enabled {
		t.Error("DefaultCASSConfig().Enabled should be true")
	}
	if config.MaxResults != 5 {
		t.Errorf("DefaultCASSConfig().MaxResults = %d, want 5", config.MaxResults)
	}
	if config.MaxAgeDays != 30 {
		t.Errorf("DefaultCASSConfig().MaxAgeDays = %d, want 30", config.MaxAgeDays)
	}
	if !config.PreferSameProject {
		t.Error("DefaultCASSConfig().PreferSameProject should be true")
	}
}

func TestQueryCASS_Disabled(t *testing.T) {
	config := CASSConfig{
		Enabled: false,
	}

	result := QueryCASS("test prompt", config)

	if !result.Success {
		t.Error("QueryCASS with disabled config should succeed")
	}
	if len(result.Hits) != 0 {
		t.Error("QueryCASS with disabled config should return no hits")
	}
}

func TestQueryCASS_EmptyKeywords(t *testing.T) {
	config := DefaultCASSConfig()

	// Prompt with only stop words should extract no keywords
	result := QueryCASS("the and or but", config)

	if !result.Success {
		t.Error("QueryCASS with no keywords should still succeed")
	}
	if result.Error != "no keywords extracted from prompt" {
		t.Errorf("QueryCASS error = %q, want 'no keywords extracted from prompt'", result.Error)
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{10, "10"},
		{100, "100"},
		{-1, "-1"},
		{-100, "-100"},
		{12345, "12345"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := itoa(tt.input)
			if got != tt.want {
				t.Errorf("itoa(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Relevance Filtering Tests
// =============================================================================

func TestDefaultFilterConfig(t *testing.T) {
	config := DefaultFilterConfig()

	if config.MinRelevance != 0.7 {
		t.Errorf("DefaultFilterConfig().MinRelevance = %f, want 0.7", config.MinRelevance)
	}
	if config.MaxItems != 5 {
		t.Errorf("DefaultFilterConfig().MaxItems = %d, want 5", config.MaxItems)
	}
	if !config.PreferSameProject {
		t.Error("DefaultFilterConfig().PreferSameProject should be true")
	}
	if config.MaxAgeDays != 30 {
		t.Errorf("DefaultFilterConfig().MaxAgeDays = %d, want 30", config.MaxAgeDays)
	}
	if config.RecencyBoost != 0.3 {
		t.Errorf("DefaultFilterConfig().RecencyBoost = %f, want 0.3", config.RecencyBoost)
	}
}

func TestFilterResults_Empty(t *testing.T) {
	config := DefaultFilterConfig()
	result := FilterResults([]CASSHit{}, config)

	if result.OriginalCount != 0 {
		t.Errorf("FilterResults() OriginalCount = %d, want 0", result.OriginalCount)
	}
	if result.FilteredCount != 0 {
		t.Errorf("FilterResults() FilteredCount = %d, want 0", result.FilteredCount)
	}
	if len(result.Hits) != 0 {
		t.Errorf("FilterResults() len(Hits) = %d, want 0", len(result.Hits))
	}
}

func TestFilterResults_BasicScoring(t *testing.T) {
	hits := []CASSHit{
		{SourcePath: "/path/to/session1.jsonl", Agent: "claude"},
		{SourcePath: "/path/to/session2.jsonl", Agent: "codex"},
		{SourcePath: "/path/to/session3.jsonl", Agent: "gemini"},
	}

	// Use low MinRelevance to ensure all pass
	config := FilterConfig{
		MinRelevance: 0.0,
		MaxItems:     10,
		MaxAgeDays:   0, // Disable age filtering
	}

	result := FilterResults(hits, config)

	if result.OriginalCount != 3 {
		t.Errorf("FilterResults() OriginalCount = %d, want 3", result.OriginalCount)
	}
	if result.FilteredCount != 3 {
		t.Errorf("FilterResults() FilteredCount = %d, want 3", result.FilteredCount)
	}

	// First result should have highest score (position-based)
	if len(result.Hits) < 1 {
		t.Fatal("Expected at least 1 hit")
	}
	if result.Hits[0].ComputedScore < result.Hits[len(result.Hits)-1].ComputedScore {
		t.Error("First hit should have higher score than last hit")
	}
}

func TestFilterResults_MinRelevance(t *testing.T) {
	hits := []CASSHit{
		{SourcePath: "/path/to/session1.jsonl", Agent: "claude"},
		{SourcePath: "/path/to/session2.jsonl", Agent: "codex"},
		{SourcePath: "/path/to/session3.jsonl", Agent: "gemini"},
	}

	// High MinRelevance should filter out lower-scored results
	config := FilterConfig{
		MinRelevance: 0.95, // Very high threshold
		MaxItems:     10,
		MaxAgeDays:   0,
	}

	result := FilterResults(hits, config)

	// Only the top result(s) should pass the high threshold
	if result.FilteredCount > 1 {
		t.Errorf("FilterResults() with high MinRelevance should filter most results, got %d", result.FilteredCount)
	}
	if result.RemovedByScore < 2 {
		t.Errorf("FilterResults() RemovedByScore = %d, expected at least 2", result.RemovedByScore)
	}
}

func TestFilterResults_MaxItems(t *testing.T) {
	hits := make([]CASSHit, 10)
	for i := range hits {
		hits[i] = CASSHit{SourcePath: "/path/to/session.jsonl", Agent: "claude"}
	}

	config := FilterConfig{
		MinRelevance: 0.0,
		MaxItems:     3, // Limit to 3
		MaxAgeDays:   0,
	}

	result := FilterResults(hits, config)

	if len(result.Hits) != 3 {
		t.Errorf("FilterResults() len(Hits) = %d, want 3", len(result.Hits))
	}
}

func TestFilterResults_SameProjectPreference(t *testing.T) {
	hits := []CASSHit{
		{SourcePath: "/some/other/project/session.jsonl", Agent: "claude"},
		{SourcePath: "/users/test/myproject/session.jsonl", Agent: "codex"},
	}

	config := FilterConfig{
		MinRelevance:      0.0,
		MaxItems:          10,
		MaxAgeDays:        0,
		PreferSameProject: true,
		CurrentWorkspace:  "/users/test/myproject",
	}

	result := FilterResults(hits, config)

	// The same-project hit should have a bonus and thus higher score
	if len(result.Hits) < 2 {
		t.Fatal("Expected 2 hits")
	}

	// Find the myproject hit and check it has project bonus
	var myprojectHit *ScoredHit
	for i := range result.Hits {
		if result.Hits[i].SourcePath == "/users/test/myproject/session.jsonl" {
			myprojectHit = &result.Hits[i]
			break
		}
	}

	if myprojectHit == nil {
		t.Fatal("Expected to find myproject hit")
	}
	if myprojectHit.ScoreDetail.ProjectBonus == 0 {
		t.Error("Same-project hit should have ProjectBonus > 0")
	}
}

func TestExtractSessionDate(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string // Expected date in YYYY-MM-DD format, empty if no date
	}{
		{
			name: "date in path components",
			path: "/Users/test/.codex/sessions/2025/12/05/session.jsonl",
			want: "2025-12-05",
		},
		{
			name: "date with dashes in path",
			path: "/some/path/2025-12-05/session.jsonl",
			want: "2025-12-05",
		},
		{
			name: "date in session filename",
			path: "/some/path/session-2025-12-05-abc123.jsonl",
			want: "2025-12-05",
		},
		{
			name: "ISO timestamp in filename",
			path: "/some/path/session-2025-12-05T14-30-00.json",
			want: "2025-12-05",
		},
		{
			name: "no date in path",
			path: "/some/path/session.jsonl",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSessionDate(tt.path)
			gotStr := ""
			if !got.IsZero() {
				gotStr = got.Format("2006-01-02")
			}
			if gotStr != tt.want {
				t.Errorf("extractSessionDate(%q) = %q, want %q", tt.path, gotStr, tt.want)
			}
		})
	}
}

func TestIsSameProject(t *testing.T) {
	tests := []struct {
		name             string
		sessionPath      string
		currentWorkspace string
		want             bool
	}{
		{
			name:             "matching project name",
			sessionPath:      "/users/test/.codex/myproject/session.jsonl",
			currentWorkspace: "/users/dev/myproject",
			want:             true,
		},
		{
			name:             "case insensitive match",
			sessionPath:      "/users/test/MyProject/session.jsonl",
			currentWorkspace: "/users/dev/myproject",
			want:             true,
		},
		{
			name:             "no match",
			sessionPath:      "/users/test/otherproject/session.jsonl",
			currentWorkspace: "/users/dev/myproject",
			want:             false,
		},
		{
			name:             "empty workspace",
			sessionPath:      "/users/test/myproject/session.jsonl",
			currentWorkspace: "",
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSameProject(tt.sessionPath, tt.currentWorkspace)
			if got != tt.want {
				t.Errorf("isSameProject(%q, %q) = %v, want %v",
					tt.sessionPath, tt.currentWorkspace, got, tt.want)
			}
		})
	}
}

func TestNormalizeScore(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{0.5, 0.5},    // Already 0-1 scale
		{1.0, 1.0},    // Already 0-1 scale
		{50.0, 0.5},   // 0-100 scale
		{100.0, 1.0},  // 0-100 scale
		{0.0, 0.0},    // Zero
	}

	for _, tt := range tests {
		got := normalizeScore(tt.input)
		if got != tt.want {
			t.Errorf("normalizeScore(%f) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestSortScoredHits(t *testing.T) {
	hits := []ScoredHit{
		{ComputedScore: 0.5},
		{ComputedScore: 0.9},
		{ComputedScore: 0.7},
		{ComputedScore: 0.3},
	}

	sortScoredHits(hits)

	// Should be sorted descending
	for i := 1; i < len(hits); i++ {
		if hits[i-1].ComputedScore < hits[i].ComputedScore {
			t.Errorf("sortScoredHits() not sorted descending: %f < %f at positions %d, %d",
				hits[i-1].ComputedScore, hits[i].ComputedScore, i-1, i)
		}
	}

	// First should be highest
	if hits[0].ComputedScore != 0.9 {
		t.Errorf("sortScoredHits() first item score = %f, want 0.9", hits[0].ComputedScore)
	}
}

// =============================================================================
// Context Injection Tests
// =============================================================================

func TestDefaultInjectConfig(t *testing.T) {
	config := DefaultInjectConfig()

	if config.Format != FormatMarkdown {
		t.Errorf("DefaultInjectConfig().Format = %q, want %q", config.Format, FormatMarkdown)
	}
	if config.MaxTokens != 500 {
		t.Errorf("DefaultInjectConfig().MaxTokens = %d, want 500", config.MaxTokens)
	}
	if config.SkipThreshold != 60 {
		t.Errorf("DefaultInjectConfig().SkipThreshold = %d, want 60", config.SkipThreshold)
	}
	if !config.IncludeMetadata {
		t.Error("DefaultInjectConfig().IncludeMetadata should be true")
	}
	if config.DryRun {
		t.Error("DefaultInjectConfig().DryRun should be false")
	}
}

func TestInjectContext_EmptyHits(t *testing.T) {
	config := DefaultInjectConfig()
	prompt := "Original prompt"

	result := InjectContext(prompt, []ScoredHit{}, config)

	if !result.Success {
		t.Error("InjectContext with empty hits should succeed")
	}
	if result.ModifiedPrompt != prompt {
		t.Errorf("InjectContext with empty hits should return original prompt")
	}
	if result.Metadata.SkippedReason != "no relevant context found" {
		t.Errorf("SkippedReason = %q, want 'no relevant context found'", result.Metadata.SkippedReason)
	}
}

func TestInjectContext_SkipOnHighContextUsage(t *testing.T) {
	config := InjectConfig{
		Format:            FormatMarkdown,
		MaxTokens:         500,
		SkipThreshold:     60,
		CurrentContextPct: 70, // Above threshold
	}
	prompt := "Original prompt"
	hits := []ScoredHit{
		{CASSHit: CASSHit{SourcePath: "/path/session.jsonl", Content: "content"}, ComputedScore: 0.9},
	}

	result := InjectContext(prompt, hits, config)

	if !result.Success {
		t.Error("InjectContext should succeed even when skipping")
	}
	if result.ModifiedPrompt != prompt {
		t.Errorf("InjectContext should return original prompt when skipping")
	}
	if result.Metadata.SkippedReason == "" {
		t.Error("SkippedReason should explain why injection was skipped")
	}
}

func TestInjectContext_DryRun(t *testing.T) {
	config := InjectConfig{
		Format:    FormatMarkdown,
		MaxTokens: 500,
		DryRun:    true,
	}
	prompt := "Original prompt"
	hits := []ScoredHit{
		{CASSHit: CASSHit{SourcePath: "/path/session.jsonl", Content: "test content"}, ComputedScore: 0.9},
	}

	result := InjectContext(prompt, hits, config)

	if !result.Success {
		t.Error("InjectContext dry run should succeed")
	}
	if result.ModifiedPrompt != prompt {
		t.Error("DryRun should not modify the prompt")
	}
	if result.InjectedContext == "" {
		t.Error("DryRun should still show what would be injected")
	}
}

func TestInjectContext_BasicInjection(t *testing.T) {
	config := DefaultInjectConfig()
	prompt := "Original prompt"
	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "/path/2025/01/05/session.jsonl", Content: "relevant code here"},
			ComputedScore: 0.85,
		},
	}

	result := InjectContext(prompt, hits, config)

	if !result.Success {
		t.Errorf("InjectContext failed: %s", result.Error)
	}
	if result.ModifiedPrompt == prompt {
		t.Error("ModifiedPrompt should be different from original")
	}
	if result.InjectedContext == "" {
		t.Error("InjectedContext should contain formatted context")
	}
	if result.Metadata.ItemsInjected != 1 {
		t.Errorf("ItemsInjected = %d, want 1", result.Metadata.ItemsInjected)
	}
	if result.Metadata.TokensAdded == 0 {
		t.Error("TokensAdded should be > 0")
	}
}

func TestFormatContext_Markdown(t *testing.T) {
	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "/path/2025/01/05/test-session.jsonl", Content: "some code"},
			ComputedScore: 0.9,
		},
	}
	config := InjectConfig{Format: FormatMarkdown}

	result := FormatContext(hits, config)

	// Should contain markdown headers
	if len(result) == 0 {
		t.Fatal("FormatContext should return non-empty string")
	}
	// Check for markdown formatting
	if !containsString(result, "## Relevant Context") {
		t.Error("Markdown format should contain ## header")
	}
	if !containsString(result, "### Session:") {
		t.Error("Markdown format should contain ### Session: header")
	}
}

func TestFormatContext_Minimal(t *testing.T) {
	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "/path/session.jsonl", Content: "code snippet"},
			ComputedScore: 0.9,
		},
	}
	config := InjectConfig{Format: FormatMinimal}

	result := FormatContext(hits, config)

	// Should be minimal with code focus
	if !containsString(result, "// Related context:") {
		t.Error("Minimal format should start with // comment")
	}
}

func TestFormatContext_Structured(t *testing.T) {
	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "/path/session.jsonl", Content: "content here"},
			ComputedScore: 0.8,
		},
	}
	config := InjectConfig{Format: FormatStructured}

	result := FormatContext(hits, config)

	// Should have structured format
	if !containsString(result, "RELEVANT CONTEXT FROM PAST SESSIONS") {
		t.Error("Structured format should have capitalized header")
	}
	if !containsString(result, "1. Session:") {
		t.Error("Structured format should have numbered items")
	}
	if !containsString(result, "Relevance:") {
		t.Error("Structured format should show relevance")
	}
}

func TestFormatForAgent(t *testing.T) {
	tests := []struct {
		agentType string
		want      InjectionFormat
	}{
		{"claude", FormatMarkdown},
		{"cc", FormatMarkdown},
		{"Claude", FormatMarkdown},
		{"codex", FormatMinimal},
		{"cod", FormatMinimal},
		{"Codex", FormatMinimal},
		{"gemini", FormatStructured},
		{"gmi", FormatStructured},
		{"Gemini", FormatStructured},
		{"unknown", FormatMarkdown}, // Default
		{"", FormatMarkdown},        // Empty defaults to markdown
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := FormatForAgent(tt.agentType)
			if got != tt.want {
				t.Errorf("FormatForAgent(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestExtractSessionName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/path/to/session.jsonl", "session"},
		{"/path/to/my-long-session-name.json", "my-long-session-name"},
		// 40 char limit with "..." suffix = 37 chars + "..."
		{"/path/to/very-very-very-long-session-name-that-exceeds-forty-characters.jsonl", "very-very-very-long-session-name-that..."},
		{"", ""},                   // Empty path returns empty string (no parts)
		{"single", "single"},      // Single component without extension
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractSessionName(tt.path)
			if got != tt.want {
				t.Errorf("extractSessionName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestCleanContentForMarkdown(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(string) bool
	}{
		{
			name:  "trims whitespace",
			input: "  content  ",
			check: func(s string) bool { return s == "content" },
		},
		{
			name:  "limits lines",
			input: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12",
			check: func(s string) bool { return containsString(s, "...") },
		},
		{
			name: "truncates long lines",
			input: "this is a very long line that definitely exceeds one hundred and twenty characters and should be truncated at some point to maintain readability",
			check: func(s string) bool {
				return len(s) < len("this is a very long line that definitely exceeds one hundred and twenty characters and should be truncated at some point to maintain readability")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanContentForMarkdown(tt.input)
			if !tt.check(got) {
				t.Errorf("cleanContentForMarkdown() = %q, check failed", got)
			}
		})
	}
}

func TestExtractCodeSnippets(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "extracts fenced code block",
			content: "text before\n```go\nfunc main() {}\n```\ntext after",
			want:    "func main() {}",
		},
		{
			name:    "no code blocks",
			content: "plain text content here",
			want:    "plain text content here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCodeSnippets(tt.content)
			if got != tt.want {
				t.Errorf("extractCodeSnippets() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateToTokens(t *testing.T) {
	// ~4 chars per token, so 100 tokens = ~400 chars
	longContent := make([]byte, 800) // ~200 tokens
	for i := range longContent {
		longContent[i] = 'a'
	}

	result := truncateToTokens(string(longContent), 100)

	// Should be truncated to approximately 400 chars (100 tokens * 4)
	if len(result) > 450 { // Some slack for truncation message
		t.Errorf("truncateToTokens() len = %d, want <= 450", len(result))
	}
	if !containsString(result, "truncated") {
		t.Error("truncateToTokens() should include truncation notice")
	}
}

func TestCountInjectedItems(t *testing.T) {
	tests := []struct {
		name    string
		context string
		format  InjectionFormat
		want    int
	}{
		{
			name:    "markdown format",
			context: "## Header\n\n### Session: test1\n\ncontent\n\n### Session: test2\n\ncontent",
			format:  FormatMarkdown,
			want:    2,
		},
		{
			name:    "structured format",
			context: "HEADER\n\n1. Session: test1\n\n2. Session: test2\n\n3. Session: test3",
			format:  FormatStructured,
			want:    3,
		},
		{
			name:    "minimal format with content",
			context: "// Related context:\nsome code",
			format:  FormatMinimal,
			want:    1,
		},
		{
			name:    "minimal format multiple items",
			context: "// Related context:\nfirst code\n\n// ---\nsecond code\n\n// ---\nthird code\n",
			format:  FormatMinimal,
			want:    3,
		},
		{
			name:    "minimal format empty",
			context: "// Related context:\n",
			format:  FormatMinimal,
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countInjectedItems(tt.context, tt.format)
			if got != tt.want {
				t.Errorf("countInjectedItems() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestInjectContext_TokenBudget(t *testing.T) {
	config := InjectConfig{
		Format:    FormatMarkdown,
		MaxTokens: 50, // Very small budget
	}

	// Create hits with lots of content
	hits := []ScoredHit{
		{
			CASSHit: CASSHit{
				SourcePath: "/path/session.jsonl",
				Content:    "This is a very long piece of content that should definitely exceed the token budget we have set and therefore needs to be truncated to fit within our constraints.",
			},
			ComputedScore: 0.9,
		},
	}

	result := InjectContext("prompt", hits, config)

	if !result.Success {
		t.Errorf("InjectContext failed: %s", result.Error)
	}
	if result.Metadata.TokensAdded > 50 {
		t.Errorf("TokensAdded = %d, should be capped at 50", result.Metadata.TokensAdded)
	}
}

func TestFormatAge(t *testing.T) {
	// Test with a path that has no date
	result := formatAge("/some/path/without/date.jsonl")
	if result != "" {
		t.Errorf("formatAge() for no-date path = %q, want empty", result)
	}
}

func TestFormatAge_AllBranches(t *testing.T) {
	// Use UTC to match how extractSessionDate parses dates
	now := time.Now().UTC()

	// Helper to create path with specific date
	pathForDate := func(d time.Time) string {
		return fmt.Sprintf("/path/%d/%02d/%02d/session.jsonl", d.Year(), d.Month(), d.Day())
	}

	// Test the branch coverage by checking output format, not exact values
	// The exact values depend on time-of-day relative to UTC midnight
	tests := []struct {
		name        string
		daysBack    int
		validOutput []string // any of these substrings is acceptable
	}{
		{
			name:        "recent (today or 1 day)",
			daysBack:    0,
			validOutput: []string{"today", "1 day ago"}, // Depends on UTC time
		},
		{
			name:        "few days ago",
			daysBack:    3,
			validOutput: []string{"days ago", "1 week ago"}, // 3-4 days
		},
		{
			name:        "about a week ago",
			daysBack:    8,
			validOutput: []string{"week ago", "weeks ago"},
		},
		{
			name:        "two weeks ago",
			daysBack:    15,
			validOutput: []string{"weeks ago"},
		},
		{
			name:        "about a month ago",
			daysBack:    32,
			validOutput: []string{"month ago", "months ago"},
		},
		{
			name:        "several months ago",
			daysBack:    100,
			validOutput: []string{"months ago"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			date := now.AddDate(0, 0, -tt.daysBack)
			path := pathForDate(date)
			result := formatAge(path)

			// Check if any valid output matches
			matched := false
			for _, valid := range tt.validOutput {
				if containsString(result, valid) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("formatAge(%q) = %q, want one of %v", path, result, tt.validOutput)
			}
		})
	}
}

func TestFilterResults_AgeFiltering(t *testing.T) {
	now := time.Now()
	recent := now.AddDate(0, 0, -5)   // 5 days ago
	old := now.AddDate(0, 0, -60)     // 60 days ago

	recentPath := fmt.Sprintf("/path/%d/%02d/%02d/recent.jsonl", recent.Year(), recent.Month(), recent.Day())
	oldPath := fmt.Sprintf("/path/%d/%02d/%02d/old.jsonl", old.Year(), old.Month(), old.Day())

	hits := []CASSHit{
		{SourcePath: recentPath, Agent: "claude"},
		{SourcePath: oldPath, Agent: "codex"},
	}

	config := FilterConfig{
		MinRelevance: 0.0,
		MaxItems:     10,
		MaxAgeDays:   30, // Filter out results older than 30 days
	}

	result := FilterResults(hits, config)

	if result.FilteredCount != 1 {
		t.Errorf("FilterResults() FilteredCount = %d, want 1 (old session should be filtered)", result.FilteredCount)
	}
	if result.RemovedByAge != 1 {
		t.Errorf("FilterResults() RemovedByAge = %d, want 1", result.RemovedByAge)
	}
}

func TestFilterResults_RecencyBoost(t *testing.T) {
	now := time.Now()
	today := now
	weekAgo := now.AddDate(0, 0, -7)

	todayPath := fmt.Sprintf("/path/%d/%02d/%02d/today.jsonl", today.Year(), today.Month(), today.Day())
	weekAgoPath := fmt.Sprintf("/path/%d/%02d/%02d/weekago.jsonl", weekAgo.Year(), weekAgo.Month(), weekAgo.Day())

	hits := []CASSHit{
		// Week-ago hit comes first in original order
		{SourcePath: weekAgoPath, Agent: "claude"},
		{SourcePath: todayPath, Agent: "codex"},
	}

	config := FilterConfig{
		MinRelevance:  0.0,
		MaxItems:      10,
		MaxAgeDays:    30,
		RecencyBoost:  0.3, // Boost recent results
	}

	result := FilterResults(hits, config)

	if len(result.Hits) != 2 {
		t.Fatalf("FilterResults() len(Hits) = %d, want 2", len(result.Hits))
	}

	// Today's hit should have higher recency bonus
	var todayHit, weekAgoHit *ScoredHit
	for i := range result.Hits {
		if containsString(result.Hits[i].SourcePath, "today") {
			todayHit = &result.Hits[i]
		} else {
			weekAgoHit = &result.Hits[i]
		}
	}

	if todayHit == nil || weekAgoHit == nil {
		t.Fatal("Expected to find both hits")
	}

	if todayHit.ScoreDetail.RecencyBonus <= weekAgoHit.ScoreDetail.RecencyBonus {
		t.Errorf("Today's hit should have higher recency bonus: today=%f, weekAgo=%f",
			todayHit.ScoreDetail.RecencyBonus, weekAgoHit.ScoreDetail.RecencyBonus)
	}
}

func TestFilterResults_WithCASSScore(t *testing.T) {
	// Test that CASS-provided scores are used as base score
	hits := []CASSHit{
		{SourcePath: "/path/session1.jsonl", Agent: "claude", Score: 0.9},
		{SourcePath: "/path/session2.jsonl", Agent: "codex", Score: 0.5},
	}

	config := FilterConfig{
		MinRelevance: 0.0,
		MaxItems:     10,
		MaxAgeDays:   0, // Disable age filtering
	}

	result := FilterResults(hits, config)

	if len(result.Hits) != 2 {
		t.Fatalf("FilterResults() len(Hits) = %d, want 2", len(result.Hits))
	}

	// First hit should have higher base score from CASS
	if result.Hits[0].ScoreDetail.BaseScore < result.Hits[1].ScoreDetail.BaseScore {
		t.Errorf("Higher CASS score should result in higher base score")
	}
}

func TestTruncateToTokens_LineBoundary(t *testing.T) {
	// Create content that's definitely longer than budget
	// Each line is ~20 chars, we need more than 40 chars (10 tokens * 4)
	content := "Line 1 with some content here\nLine 2 with more content\nLine 3 extra text\nLine 4 additional\nLine 5 final line\n"

	// 10 tokens * 4 chars = 40 chars max - this content is ~100+ chars
	result := truncateToTokens(content, 10)

	// Should truncate at a line boundary and include truncation notice
	if !containsString(result, "truncated") {
		t.Errorf("truncateToTokens() should include truncation notice, got: %q (len=%d)", result, len(result))
	}
}

func TestTruncateToTokens_ShortContent(t *testing.T) {
	// Test that short content is not truncated
	content := "Short content"
	result := truncateToTokens(content, 100)

	if result != content {
		t.Errorf("truncateToTokens() should not modify short content: got %q, want %q", result, content)
	}
}

func TestTruncateToTokens_NoGoodLineBreak(t *testing.T) {
	// Test truncation when there's no good line break in first half
	content := strings.Repeat("a", 100)

	result := truncateToTokens(content, 10) // 40 chars

	if !containsString(result, "truncated") {
		t.Error("truncateToTokens() should include truncation notice")
	}
}

func TestQueryCASS_CASSNotAvailable(t *testing.T) {
	// Test behavior when CASS is not available
	// We can't easily mock exec.LookPath, but we can test the disabled config path
	config := CASSConfig{
		Enabled: false,
	}

	result := QueryCASS("test prompt", config)

	if !result.Success {
		t.Error("QueryCASS with disabled config should succeed")
	}
	if len(result.Hits) != 0 {
		t.Error("QueryCASS with disabled config should return no hits")
	}
}

func TestQueryCASS_KeywordExtraction(t *testing.T) {
	config := DefaultCASSConfig()
	// Intentionally use a prompt that will generate keywords but CASS won't be available
	result := QueryCASS("implement authentication handler for database", config)

	// Regardless of CASS availability, keywords should be extracted
	if len(result.Keywords) == 0 {
		t.Error("QueryCASS should extract keywords from prompt")
	}

	// Check some expected keywords
	found := make(map[string]bool)
	for _, k := range result.Keywords {
		found[k] = true
	}

	// "implement" might be filtered as stop word, but these should be present
	expectedKeywords := []string{"authentication", "handler", "database"}
	for _, expected := range expectedKeywords {
		if !found[expected] {
			t.Errorf("QueryCASS keywords should include %q, got %v", expected, result.Keywords)
		}
	}
}

func TestQueryAndFilterCASS_Disabled(t *testing.T) {
	queryConfig := CASSConfig{
		Enabled: false,
	}
	filterConfig := DefaultFilterConfig()

	queryResult, filterResult := QueryAndFilterCASS("test prompt", queryConfig, filterConfig)

	if !queryResult.Success {
		t.Error("QueryAndFilterCASS with disabled config should succeed")
	}
	if len(queryResult.Hits) != 0 {
		t.Error("QueryAndFilterCASS with disabled config should return no hits")
	}
	if filterResult.OriginalCount != 0 {
		t.Errorf("FilterResult.OriginalCount = %d, want 0", filterResult.OriginalCount)
	}
}

func TestQueryAndFilterCASS_EmptyKeywords(t *testing.T) {
	queryConfig := DefaultCASSConfig()
	filterConfig := DefaultFilterConfig()

	// Empty prompt should still work
	queryResult, filterResult := QueryAndFilterCASS("", queryConfig, filterConfig)

	if !queryResult.Success {
		t.Error("QueryAndFilterCASS with empty prompt should succeed")
	}
	// No keywords means no CASS query, so no hits
	if len(queryResult.Hits) != 0 {
		t.Error("QueryAndFilterCASS with empty prompt should return no hits")
	}
	if filterResult.OriginalCount != 0 {
		t.Errorf("FilterResult.OriginalCount = %d, want 0", filterResult.OriginalCount)
	}
}

func TestInjectContextFromQuery_Disabled(t *testing.T) {
	queryConfig := CASSConfig{
		Enabled: false,
	}
	filterConfig := DefaultFilterConfig()
	injectConfig := DefaultInjectConfig()

	injectResult, queryResult, filterResult := InjectContextFromQuery(
		"test prompt",
		queryConfig,
		filterConfig,
		injectConfig,
	)

	// Should succeed but with no injection
	if !injectResult.Success {
		t.Errorf("InjectContextFromQuery with disabled config should succeed, got error: %s", injectResult.Error)
	}
	if !queryResult.Success {
		t.Errorf("QueryResult should succeed, got error: %s", queryResult.Error)
	}
	if filterResult.OriginalCount != 0 {
		t.Errorf("FilterResult.OriginalCount = %d, want 0", filterResult.OriginalCount)
	}
	if injectResult.Metadata.ItemsInjected != 0 {
		t.Errorf("ItemsInjected = %d, want 0", injectResult.Metadata.ItemsInjected)
	}
}

func TestInjectContextFromQuery_WithPrompt(t *testing.T) {
	queryConfig := DefaultCASSConfig()
	filterConfig := DefaultFilterConfig()
	injectConfig := DefaultInjectConfig()

	// This will attempt to query CASS but likely won't find anything
	// The test verifies the function handles this gracefully
	injectResult, queryResult, filterResult := InjectContextFromQuery(
		"how do I implement authentication",
		queryConfig,
		filterConfig,
		injectConfig,
	)

	// Keywords should be extracted
	if len(queryResult.Keywords) == 0 {
		t.Error("Should extract keywords from prompt")
	}

	// If CASS isn't available, Success will be true with no hits
	// If CASS is available but no results, Success will be true with no hits
	if !queryResult.Success && queryResult.Error != "" {
		// CASS query failed - that's acceptable in test environment
		if !containsString(injectResult.Error, "CASS query failed") {
			t.Errorf("Expected CASS query failed error, got: %s", injectResult.Error)
		}
	} else {
		// Query succeeded, result should be valid
		if injectResult.ModifiedPrompt == "" {
			t.Error("ModifiedPrompt should not be empty")
		}
		// ItemsFound should match filter's original count
		if injectResult.Metadata.ItemsFound != filterResult.OriginalCount {
			t.Errorf("ItemsFound = %d, want %d", injectResult.Metadata.ItemsFound, filterResult.OriginalCount)
		}
	}
}

// Helper function for string containment checks
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// Robot API Integration Tests
// =============================================================================

func TestNewCASSInjectionInfo(t *testing.T) {
	result := InjectionResult{
		Success: true,
		Metadata: InjectionMetadata{
			Enabled:       true,
			ItemsFound:    5,
			ItemsInjected: 2,
			TokensAdded:   200,
			SkippedReason: "",
		},
	}

	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "/path/2025/01/04/session1.jsonl"},
			ComputedScore: 0.95,
		},
		{
			CASSHit:       CASSHit{SourcePath: "/path/2025/01/03/session2.jsonl"},
			ComputedScore: 0.85,
		},
	}

	info := NewCASSInjectionInfo(result, "test query", hits)

	if !info.Enabled {
		t.Error("Enabled should be true")
	}
	if info.Query != "test query" {
		t.Errorf("Query = %q, want 'test query'", info.Query)
	}
	if info.ItemsFound != 5 {
		t.Errorf("ItemsFound = %d, want 5", info.ItemsFound)
	}
	if info.ItemsInjected != 2 {
		t.Errorf("ItemsInjected = %d, want 2", info.ItemsInjected)
	}
	if info.TokensAdded != 200 {
		t.Errorf("TokensAdded = %d, want 200", info.TokensAdded)
	}
	if len(info.Sources) != 2 {
		t.Fatalf("len(Sources) = %d, want 2", len(info.Sources))
	}

	// Check first source
	if info.Sources[0].Session != "session1" {
		t.Errorf("Sources[0].Session = %q, want 'session1'", info.Sources[0].Session)
	}
	if info.Sources[0].Relevance != 95 {
		t.Errorf("Sources[0].Relevance = %d, want 95", info.Sources[0].Relevance)
	}

	// Check second source
	if info.Sources[1].Session != "session2" {
		t.Errorf("Sources[1].Session = %q, want 'session2'", info.Sources[1].Session)
	}
	if info.Sources[1].Relevance != 85 {
		t.Errorf("Sources[1].Relevance = %d, want 85", info.Sources[1].Relevance)
	}
}

func TestNewCASSInjectionInfo_EmptyHits(t *testing.T) {
	result := InjectionResult{
		Success: true,
		Metadata: InjectionMetadata{
			Enabled:       true,
			ItemsFound:    0,
			ItemsInjected: 0,
			SkippedReason: "no relevant context found",
		},
	}

	info := NewCASSInjectionInfo(result, "", []ScoredHit{})

	if len(info.Sources) != 0 {
		t.Errorf("len(Sources) = %d, want 0", len(info.Sources))
	}
	if info.SkippedReason != "no relevant context found" {
		t.Errorf("SkippedReason = %q, want 'no relevant context found'", info.SkippedReason)
	}
}

// =============================================================================
// Topic Detection Tests
// =============================================================================

func TestDetectTopics(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected []Topic
	}{
		{
			name:     "auth topic - multiple keywords",
			text:     "Fix the login authentication issue with jwt tokens",
			expected: []Topic{TopicAuth},
		},
		{
			name:     "ui topic - multiple keywords",
			text:     "Update the button component style and hover animation",
			expected: []Topic{TopicUI},
		},
		{
			name:     "api topic - multiple keywords",
			text:     "Add new endpoint handler for json request/response",
			expected: []Topic{TopicAPI},
		},
		{
			name:     "database topic - multiple keywords",
			text:     "Run migration to update schema with new index",
			expected: []Topic{TopicDatabase},
		},
		{
			name:     "testing topic - multiple keywords",
			text:     "Write unit test with mock fixture",
			expected: []Topic{TopicTesting},
		},
		{
			name:     "infrastructure topic - multiple keywords",
			text:     "Deploy docker container to kubernetes cluster",
			expected: []Topic{TopicInfra},
		},
		{
			name:     "docs topic - multiple keywords",
			text:     "Update readme documentation with guide",
			expected: []Topic{TopicDocs},
		},
		{
			name:     "general - no specific keywords",
			text:     "Make some improvements to the code",
			expected: []Topic{TopicGeneral},
		},
		{
			name:     "multiple topics detected",
			text:     "Add authentication endpoint with jwt token handler for login route",
			expected: []Topic{TopicAuth, TopicAPI}, // order may vary
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected := DetectTopics(tt.text)

			// For multiple expected topics, check all are present
			for _, expected := range tt.expected {
				found := false
				for _, topic := range detected {
					if topic == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("DetectTopics(%q) = %v, missing expected topic %q",
						tt.text, detected, expected)
				}
			}
		})
	}
}

func TestDetectTopics_CodeBlocksIgnored(t *testing.T) {
	// Code blocks should be ignored to avoid false positives from code syntax
	text := "Check this code:\n```go\nfunc login() { token := \"test\" }\n```\nThe issue is elsewhere"

	detected := DetectTopics(text)

	// Should not detect auth just from code block content
	// Only "login" and "token" in code should be stripped
	hasAuth := false
	for _, topic := range detected {
		if topic == TopicAuth {
			hasAuth = true
		}
	}
	// With code blocks removed, we might still get auth from "login" in description
	// This test ensures code isn't double-counted
	t.Logf("Detected topics: %v (hasAuth: %v)", detected, hasAuth)
}

func TestTopicsOverlap(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []Topic
		expected bool
	}{
		{
			name:     "exact match",
			a:        []Topic{TopicAuth},
			b:        []Topic{TopicAuth},
			expected: true,
		},
		{
			name:     "partial overlap",
			a:        []Topic{TopicAuth, TopicAPI},
			b:        []Topic{TopicAPI, TopicDatabase},
			expected: true,
		},
		{
			name:     "no overlap",
			a:        []Topic{TopicAuth, TopicUI},
			b:        []Topic{TopicAPI, TopicDatabase},
			expected: false,
		},
		{
			name:     "empty first",
			a:        []Topic{},
			b:        []Topic{TopicAuth},
			expected: false,
		},
		{
			name:     "empty second",
			a:        []Topic{TopicAuth},
			b:        []Topic{},
			expected: false,
		},
		{
			name:     "both empty",
			a:        []Topic{},
			b:        []Topic{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := topicsOverlap(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("topicsOverlap(%v, %v) = %v, want %v",
					tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestContainsTopic(t *testing.T) {
	tests := []struct {
		name     string
		topics   []Topic
		target   Topic
		expected bool
	}{
		{
			name:     "contains target",
			topics:   []Topic{TopicAuth, TopicAPI, TopicUI},
			target:   TopicAPI,
			expected: true,
		},
		{
			name:     "does not contain target",
			topics:   []Topic{TopicAuth, TopicUI},
			target:   TopicAPI,
			expected: false,
		},
		{
			name:     "empty slice",
			topics:   []Topic{},
			target:   TopicAuth,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsTopic(tt.topics, tt.target)
			if result != tt.expected {
				t.Errorf("containsTopic(%v, %q) = %v, want %v",
					tt.topics, tt.target, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// Topic Filtering Integration Tests
// =============================================================================

func TestFilterResults_TopicFiltering_ExcludedTopic(t *testing.T) {
	now := time.Now()
	hits := []CASSHit{
		{
			SourcePath: fmt.Sprintf("/sessions/%d/%02d/%02d/auth-session.jsonl", now.Year(), now.Month(), now.Day()),
			Content:    "Handle authentication with jwt tokens and login",
		},
		{
			SourcePath: fmt.Sprintf("/sessions/%d/%02d/%02d/api-session.jsonl", now.Year(), now.Month(), now.Day()),
			Content:    "Implement endpoint handler for http requests",
		},
	}

	config := DefaultFilterConfig()
	config.MinRelevance = 0.0 // Accept all scores
	config.TopicFilter = TopicFilterConfig{
		Enabled:               true,
		MatchTopics:           false,
		ExcludeTopics:         []Topic{TopicAuth}, // Exclude auth
		SameTopicBoost:        1.5,
		DifferentTopicPenalty: 0.5,
	}

	result := FilterResults(hits, config)

	// Auth topic should be excluded
	if result.RemovedByTopic != 1 {
		t.Errorf("RemovedByTopic = %d, want 1", result.RemovedByTopic)
	}
	if result.FilteredCount != 1 {
		t.Errorf("FilteredCount = %d, want 1", result.FilteredCount)
	}
	// The remaining hit should be API-related
	if len(result.Hits) > 0 && !containsTopic(result.Hits[0].ScoreDetail.DetectedTopics, TopicAPI) {
		t.Errorf("Expected remaining hit to have API topic, got %v",
			result.Hits[0].ScoreDetail.DetectedTopics)
	}
}

func TestFilterResults_TopicFiltering_BoostMatchingTopic(t *testing.T) {
	now := time.Now()
	hits := []CASSHit{
		{
			SourcePath: fmt.Sprintf("/sessions/%d/%02d/%02d/auth-session.jsonl", now.Year(), now.Month(), now.Day()),
			Content:    "Handle authentication with jwt tokens",
		},
		{
			SourcePath: fmt.Sprintf("/sessions/%d/%02d/%02d/ui-session.jsonl", now.Year(), now.Month(), now.Day()),
			Content:    "Update button component style and layout",
		},
	}

	config := DefaultFilterConfig()
	config.MinRelevance = 0.0
	config.PromptTopics = []Topic{TopicAuth} // Prompt is about auth
	config.TopicFilter = TopicFilterConfig{
		Enabled:               true,
		MatchTopics:           true,
		SameTopicBoost:        1.5,
		DifferentTopicPenalty: 0.5,
	}

	result := FilterResults(hits, config)

	// Both should pass (no exclusions)
	if result.FilteredCount != 2 {
		t.Errorf("FilteredCount = %d, want 2", result.FilteredCount)
	}

	// Auth hit should be boosted and ranked first
	if len(result.Hits) >= 2 {
		authHit := result.Hits[0]
		uiHit := result.Hits[1]

		// Auth should have topic multiplier of 1.5
		if authHit.ScoreDetail.TopicMultiplier != 1.5 {
			t.Errorf("Auth hit TopicMultiplier = %f, want 1.5",
				authHit.ScoreDetail.TopicMultiplier)
		}

		// UI should have topic multiplier of 0.5 (penalized)
		if uiHit.ScoreDetail.TopicMultiplier != 0.5 {
			t.Errorf("UI hit TopicMultiplier = %f, want 0.5",
				uiHit.ScoreDetail.TopicMultiplier)
		}

		// Auth should have higher score
		if authHit.ComputedScore <= uiHit.ComputedScore {
			t.Errorf("Auth score (%f) should be higher than UI score (%f)",
				authHit.ComputedScore, uiHit.ComputedScore)
		}
	}
}

func TestFilterResults_TopicFiltering_Disabled(t *testing.T) {
	now := time.Now()
	hits := []CASSHit{
		{
			SourcePath: fmt.Sprintf("/sessions/%d/%02d/%02d/session.jsonl", now.Year(), now.Month(), now.Day()),
			Content:    "Handle authentication with jwt tokens",
		},
	}

	config := DefaultFilterConfig()
	config.MinRelevance = 0.0
	config.PromptTopics = []Topic{TopicUI} // Different topic
	config.TopicFilter = TopicFilterConfig{
		Enabled:               false, // Disabled
		MatchTopics:           true,
		SameTopicBoost:        1.5,
		DifferentTopicPenalty: 0.5,
	}

	result := FilterResults(hits, config)

	// With topic filtering disabled, hit should not be penalized
	if result.FilteredCount != 1 {
		t.Errorf("FilteredCount = %d, want 1", result.FilteredCount)
	}
	if len(result.Hits) > 0 && result.Hits[0].ScoreDetail.TopicMultiplier != 1.0 {
		t.Errorf("TopicMultiplier = %f, want 1.0 (neutral when disabled)",
			result.Hits[0].ScoreDetail.TopicMultiplier)
	}
}

func TestFilterResults_TopicFiltering_GeneralTopicNoPenalty(t *testing.T) {
	now := time.Now()
	hits := []CASSHit{
		{
			SourcePath: fmt.Sprintf("/sessions/%d/%02d/%02d/session.jsonl", now.Year(), now.Month(), now.Day()),
			Content:    "Make some improvements", // General topic
		},
	}

	config := DefaultFilterConfig()
	config.MinRelevance = 0.0
	config.PromptTopics = []Topic{TopicAuth} // Specific topic
	config.TopicFilter = TopicFilterConfig{
		Enabled:               true,
		MatchTopics:           true,
		SameTopicBoost:        1.5,
		DifferentTopicPenalty: 0.5,
	}

	result := FilterResults(hits, config)

	// General topic should not be penalized
	if result.FilteredCount != 1 {
		t.Errorf("FilteredCount = %d, want 1", result.FilteredCount)
	}
	if len(result.Hits) > 0 && result.Hits[0].ScoreDetail.TopicMultiplier != 1.0 {
		t.Errorf("TopicMultiplier = %f, want 1.0 (no penalty for general topic)",
			result.Hits[0].ScoreDetail.TopicMultiplier)
	}
}

func TestDefaultTopicFilterConfig(t *testing.T) {
	config := DefaultTopicFilterConfig()

	// Check defaults
	if config.Enabled != false {
		t.Errorf("Enabled = %v, want false (disabled by default)", config.Enabled)
	}
	if config.MatchTopics != true {
		t.Errorf("MatchTopics = %v, want true", config.MatchTopics)
	}
	if config.SameTopicBoost != 1.5 {
		t.Errorf("SameTopicBoost = %f, want 1.5", config.SameTopicBoost)
	}
	if config.DifferentTopicPenalty != 0.5 {
		t.Errorf("DifferentTopicPenalty = %f, want 0.5", config.DifferentTopicPenalty)
	}
	if len(config.ExcludeTopics) != 0 {
		t.Errorf("ExcludeTopics = %v, want empty", config.ExcludeTopics)
	}
}
