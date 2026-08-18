package cass

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestTokenize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"simple words", "hello world", []string{"hello", "world"}},
		{"with punctuation", "hello, world!", []string{"hello", "world"}},
		{"underscores kept", "my_var_name", []string{"my_var_name"}},
		{"hyphens kept", "my-var-name", []string{"my-var-name"}},
		{"digits", "foo123 bar456", []string{"foo123", "bar456"}},
		{"empty string", "", nil},
		{"only separators", "   ,,, ", nil},
		{"mixed", "Fix bug #42 in auth-flow", []string{"Fix", "bug", "42", "in", "auth-flow"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tokenize(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("tokenize(%q) = %v (len %d), want %v (len %d)", tc.input, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("tokenize(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestIsStopWord(t *testing.T) {
	t.Parallel()

	stopWords := []string{"the", "a", "an", "and", "or", "is", "was", "for", "of", "with", "code", "test", "fix"}
	nonStopWords := []string{"authentication", "golang", "database", "refactor", "pagination", "websocket"}

	for _, w := range stopWords {
		t.Run("stop_"+w, func(t *testing.T) {
			t.Parallel()
			if !isStopWord(w) {
				t.Errorf("isStopWord(%q) = false, want true", w)
			}
		})
	}

	for _, w := range nonStopWords {
		t.Run("nonstop_"+w, func(t *testing.T) {
			t.Parallel()
			if isStopWord(w) {
				t.Errorf("isStopWord(%q) = true, want false", w)
			}
		})
	}
}

func TestRemoveCodeBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no code blocks", "hello world", "hello world"},
		{"inline code", "use `fmt.Println` here", "use   here"},
		{"fenced code block", "before\n```go\nfmt.Println()\n```\nafter", "before\n \nafter"},
		{"empty string", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := removeCodeBlocks(tc.input)
			if got != tc.want {
				t.Errorf("removeCodeBlocks(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormalizeScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input float64
		want  float64
	}{
		{"zero", 0, 0},
		{"0.5", 0.5, 0.5},
		{"1.0", 1.0, 1.0},
		{"percentage 50", 50.0, 0.5},
		{"percentage 100", 100.0, 1.0},
		{"negative stays", -0.5, -0.5},
		{"1.1 is above 1.0", 1.1, 0.011},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeScore(tc.input)
			diff := got - tc.want
			if diff < -0.001 || diff > 0.001 {
				t.Errorf("normalizeScore(%v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsSameProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		sessionPath      string
		currentWorkspace string
		want             bool
	}{
		{"matching project name", "/sessions/myproject/log", "/home/user/myproject", true},
		{"no match", "/sessions/other/log", "/home/user/myproject", false},
		{"empty workspace", "/sessions/myproject/log", "", false},
		{"empty session path", "", "/home/user/myproject", false},
		{"partial name match", "/sessions/myproject-extra/log", "/home/user/myproject", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isSameProject(tc.sessionPath, tc.currentWorkspace)
			if got != tc.want {
				t.Errorf("isSameProject(%q, %q) = %v, want %v", tc.sessionPath, tc.currentWorkspace, got, tc.want)
			}
		})
	}
}

func TestTopicsOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    []Topic
		b    []Topic
		want bool
	}{
		{"overlap", []Topic{"go", "rust"}, []Topic{"rust", "python"}, true},
		{"no overlap", []Topic{"go", "rust"}, []Topic{"python", "java"}, false},
		{"empty a", nil, []Topic{"go"}, false},
		{"empty b", []Topic{"go"}, nil, false},
		{"both empty", nil, nil, false},
		{"same topics", []Topic{"go"}, []Topic{"go"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := topicsOverlap(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("topicsOverlap(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestContainsTopic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		topics []Topic
		target Topic
		want   bool
	}{
		{"found", []Topic{"go", "rust", "python"}, "rust", true},
		{"not found", []Topic{"go", "rust"}, "python", false},
		{"empty list", nil, "go", false},
		{"empty target", []Topic{"go"}, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := containsTopic(tc.topics, tc.target)
			if got != tc.want {
				t.Errorf("containsTopic(%v, %q) = %v, want %v", tc.topics, tc.target, got, tc.want)
			}
		})
	}
}

func TestCleanContentForMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"short content unchanged", "hello world"},
		{"trims whitespace", "  hello  "},
		{"empty string", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cleanContentForMarkdown(tc.input)
			if got == "" && tc.input != "" && strings.TrimSpace(tc.input) != "" {
				t.Errorf("cleanContentForMarkdown(%q) returned empty", tc.input)
			}
		})
	}

	t.Run("truncates long lines", func(t *testing.T) {
		longLine := strings.Repeat("a", 200)
		got := cleanContentForMarkdown(longLine)
		if len(got) > 125 { // 117 + "..."
			t.Errorf("long line not truncated: len=%d", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Error("truncated line should end with ...")
		}
	})

	t.Run("limits to 10 lines", func(t *testing.T) {
		lines := make([]string, 20)
		for i := range lines {
			lines[i] = "line"
		}
		input := strings.Join(lines, "\n")
		got := cleanContentForMarkdown(input)
		gotLines := strings.Split(got, "\n")
		if len(gotLines) > 11 { // 10 lines + "..."
			t.Errorf("expected at most 11 lines, got %d", len(gotLines))
		}
	})
}

func TestTruncateToTokensCass(t *testing.T) {
	t.Parallel()

	t.Run("short content unchanged", func(t *testing.T) {
		t.Parallel()
		input := "short text"
		got := truncateToTokens(input, 100)
		if got != input {
			t.Errorf("truncateToTokens(%q, 100) = %q, want unchanged", input, got)
		}
	})

	t.Run("long content truncated", func(t *testing.T) {
		t.Parallel()
		input := strings.Repeat("word ", 500) // ~2500 chars
		got := truncateToTokens(input, 10)    // 10 * 4 = 40 chars max
		if len(got) > 100 {                   // 40 chars + truncation message
			t.Errorf("truncateToTokens should truncate, got len=%d", len(got))
		}
		if !strings.Contains(got, "truncated for token budget") {
			t.Error("truncated content should contain budget message")
		}
	})

	t.Run("zero max tokens", func(t *testing.T) {
		t.Parallel()
		got := truncateToTokens("hello", 0)
		if !strings.Contains(got, "truncated") {
			t.Errorf("zero max tokens should truncate: got %q", got)
		}
	})
}

func TestExtractSessionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"simple path", "sessions/my-session.jsonl", "my-session"},
		{"nested path", "/data/2026/01/29/my-project.jsonl", "my-project"},
		{"json extension", "path/to/data.json", "data"},
		{"no extension", "path/to/session", "session"},
		{"empty path", "", ""},
		{"long name truncated", "sessions/" + strings.Repeat("a", 50) + ".jsonl", strings.Repeat("a", 37) + "..."},
		{"trailing slash", "sessions/", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractSessionName(tc.path)
			if got != tc.want {
				t.Errorf("ExtractSessionName(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestExtractCodeSnippets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"with code block", "text\n```go\nfmt.Println(\"hello\")\n```\nmore text"},
		{"no code block short", "just plain text"},
		{"no code block long", strings.Repeat("a ", 200)},
		{"empty string", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractCodeSnippets(tc.input)
			if tc.input != "" && got == "" {
				t.Errorf("extractCodeSnippets(%q) returned empty", tc.input)
			}
		})
	}

	t.Run("extracts code from fenced block", func(t *testing.T) {
		t.Parallel()
		input := "Here is code:\n```go\nfmt.Println(\"hello\")\n```\nEnd."
		got := extractCodeSnippets(input)
		if !strings.Contains(got, "fmt.Println") {
			t.Errorf("expected code snippet, got %q", got)
		}
	})

	t.Run("truncates long content without code blocks", func(t *testing.T) {
		t.Parallel()
		input := strings.Repeat("word ", 100)
		got := extractCodeSnippets(input)
		if len(got) > 210 {
			t.Errorf("should truncate long content: len=%d", len(got))
		}
	})
}

func TestSortScoredHits(t *testing.T) {
	t.Parallel()

	hits := []ScoredHit{
		{ComputedScore: 0.3},
		{ComputedScore: 0.9},
		{ComputedScore: 0.5},
		{ComputedScore: 0.7},
	}

	sortScoredHits(hits)

	if !sort.SliceIsSorted(hits, func(i, j int) bool {
		return hits[i].ComputedScore > hits[j].ComputedScore
	}) {
		t.Errorf("sortScoredHits did not sort descending: scores = %v",
			[]float64{hits[0].ComputedScore, hits[1].ComputedScore, hits[2].ComputedScore, hits[3].ComputedScore})
	}
}

func TestFormatMarkdown(t *testing.T) {
	t.Parallel()

	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "sessions/2026/01/15/my-session.jsonl", Content: "Some context here"},
			ComputedScore: 0.85,
		},
	}

	got := formatMarkdown(hits)

	if !strings.Contains(got, "## Relevant Context") {
		t.Error("formatMarkdown should contain header")
	}
	if !strings.Contains(got, "### Session:") {
		t.Error("formatMarkdown should contain session header")
	}
	if !strings.Contains(got, "85% match") {
		t.Error("formatMarkdown should contain relevance percentage")
	}
	if !strings.Contains(got, "Some context here") {
		t.Error("formatMarkdown should contain hit content")
	}
}

func TestFormatMinimal(t *testing.T) {
	t.Parallel()

	t.Run("with content", func(t *testing.T) {
		t.Parallel()
		hits := []ScoredHit{
			{CASSHit: CASSHit{Content: "func hello() {}"}},
			{CASSHit: CASSHit{Content: "func world() {}"}},
		}
		got := formatMinimal(hits)
		if !strings.Contains(got, "// Related context:") {
			t.Error("should start with comment header")
		}
		if !strings.Contains(got, "// ---") {
			t.Error("should contain separator between items")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		t.Parallel()
		hits := []ScoredHit{
			{CASSHit: CASSHit{Content: ""}},
		}
		got := formatMinimal(hits)
		if !strings.Contains(got, "// Related context:") {
			t.Error("should contain header even with empty content")
		}
	})
}

func TestFormatStructured(t *testing.T) {
	t.Parallel()

	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "sessions/proj.jsonl", Content: "func main() {}"},
			ComputedScore: 0.72,
		},
	}

	got := formatStructured(hits)

	if !strings.Contains(got, "RELEVANT CONTEXT") {
		t.Error("should contain header")
	}
	if !strings.Contains(got, "1. Session:") {
		t.Error("should contain numbered item")
	}
	if !strings.Contains(got, "72%") {
		t.Error("should contain relevance percentage")
	}
}

func TestFilterResults_EmptyHits(t *testing.T) {
	t.Parallel()

	result := FilterResults(nil, FilterConfig{MaxItems: 10})
	if result.OriginalCount != 0 {
		t.Errorf("OriginalCount = %d, want 0", result.OriginalCount)
	}
	if len(result.Hits) != 0 {
		t.Errorf("Hits should be empty, got %d", len(result.Hits))
	}
}

func TestCountInjectedItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ctx    string
		format InjectionFormat
		want   int
	}{
		{"markdown zero", "no items here", FormatMarkdown, 0},
		{"markdown two", "### Session: A\n### Session: B\n", FormatMarkdown, 2},
		{"minimal non-empty", "no items", FormatMinimal, 1},
		{"minimal empty", "", FormatMinimal, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := countInjectedItems(tc.ctx, tc.format)
			if got != tc.want {
				t.Errorf("countInjectedItems(%q, %q) = %d, want %d", tc.ctx, tc.format, got, tc.want)
			}
		})
	}
}

// =============================================================================
// ExtractKeywords
// =============================================================================

func TestExtractKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty prompt", "", nil},
		{"only stop words", "the and for with this that", nil},
		{"short words filtered", "go is an ok", nil},
		{"extracts meaningful words", "authentication database migration golang", []string{"authentication", "database", "migration", "golang"}},
		{"deduplicates", "golang golang golang", []string{"golang"}},
		{"lowercases", "Authentication DATABASE", []string{"authentication", "database"}},
		{"removes code blocks", "check `fmt.Println` and authentication", []string{"check", "authentication"}},
		{"caps at 10", "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima",
			[]string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractKeywords(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("ExtractKeywords(%q) = %v (len %d), want %v (len %d)", tc.input, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ExtractKeywords(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// =============================================================================
// DetectTopics
// =============================================================================

func TestDetectTopics(t *testing.T) {
	t.Parallel()

	t.Run("auth topic detected", func(t *testing.T) {
		t.Parallel()
		topics := DetectTopics("implement login and password authentication")
		found := false
		for _, tp := range topics {
			if tp == TopicAuth {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected TopicAuth, got %v", topics)
		}
	})

	t.Run("database topic detected", func(t *testing.T) {
		t.Parallel()
		topics := DetectTopics("write a SQL query to select from the table")
		found := false
		for _, tp := range topics {
			if tp == TopicDatabase {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected TopicDatabase, got %v", topics)
		}
	})

	t.Run("testing topic detected", func(t *testing.T) {
		t.Parallel()
		topics := DetectTopics("write unit test with mock and assert")
		found := false
		for _, tp := range topics {
			if tp == TopicTesting {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected TopicTesting, got %v", topics)
		}
	})

	t.Run("general fallback for no keywords", func(t *testing.T) {
		t.Parallel()
		topics := DetectTopics("something completely unrelated")
		if len(topics) != 1 || topics[0] != TopicGeneral {
			t.Errorf("expected [general], got %v", topics)
		}
	})

	t.Run("empty text returns general", func(t *testing.T) {
		t.Parallel()
		topics := DetectTopics("")
		if len(topics) != 1 || topics[0] != TopicGeneral {
			t.Errorf("expected [general], got %v", topics)
		}
	})

	t.Run("single keyword match falls back to score>=1 path", func(t *testing.T) {
		t.Parallel()
		// "deploy" is a single keyword for TopicInfra; score=1, not >=2
		// so first pass finds nothing, second pass (>=1) picks it up
		topics := DetectTopics("deploy the application")
		found := false
		for _, tp := range topics {
			if tp == TopicInfra {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected TopicInfra from single keyword, got %v", topics)
		}
	})

	t.Run("code blocks removed before detection", func(t *testing.T) {
		t.Parallel()
		// "login" and "password" are inside code block, should be stripped
		topics := DetectTopics("```\nlogin password\n```\nsomething unrelated")
		for _, tp := range topics {
			if tp == TopicAuth {
				t.Errorf("should not detect auth from code block content, got %v", topics)
			}
		}
	})
}

// =============================================================================
// ExtractSessionDate
// =============================================================================

func TestExtractSessionDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		wantZero bool
		wantYear int
		wantDay  int
	}{
		{"standard path", "sessions/2026/01/15/my-session.jsonl", false, 2026, 15},
		{"nested date", "/data/cass/2025/12/25/holiday.json", false, 2025, 25},
		{"no date", "sessions/my-session.jsonl", true, 0, 0},
		{"empty path", "", true, 0, 0},
		{"partial date", "sessions/2026/01/session.jsonl", true, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractSessionDate(tc.path)
			if tc.wantZero {
				if !got.IsZero() {
					t.Errorf("ExtractSessionDate(%q) = %v, want zero time", tc.path, got)
				}
			} else {
				if got.IsZero() {
					t.Fatalf("ExtractSessionDate(%q) returned zero time", tc.path)
				}
				if got.Year() != tc.wantYear {
					t.Errorf("year = %d, want %d", got.Year(), tc.wantYear)
				}
				if got.Day() != tc.wantDay {
					t.Errorf("day = %d, want %d", got.Day(), tc.wantDay)
				}
				if got.Location() != time.UTC {
					t.Errorf("location = %v, want UTC", got.Location())
				}
			}
		})
	}
}

// =============================================================================
// DefaultInjectConfig
// =============================================================================

func TestDefaultInjectConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultInjectConfig()

	if cfg.Format != FormatMarkdown {
		t.Errorf("Format = %q, want %q", cfg.Format, FormatMarkdown)
	}
	if cfg.MaxTokens != 500 {
		t.Errorf("MaxTokens = %d, want 500", cfg.MaxTokens)
	}
	if cfg.SkipThreshold != 60 {
		t.Errorf("SkipThreshold = %d, want 60", cfg.SkipThreshold)
	}
	if !cfg.IncludeMetadata {
		t.Error("IncludeMetadata should be true")
	}
	if cfg.DryRun {
		t.Error("DryRun should be false")
	}
}

// =============================================================================
// DefaultCASSConfig
// =============================================================================

func TestDefaultCASSConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultCASSConfig()

	if !cfg.Enabled {
		t.Error("Enabled should be true")
	}
	if cfg.MaxResults != 5 {
		t.Errorf("MaxResults = %d, want 5", cfg.MaxResults)
	}
	if cfg.MaxAgeDays != 30 {
		t.Errorf("MaxAgeDays = %d, want 30", cfg.MaxAgeDays)
	}
	if cfg.MinRelevance != 0.0 {
		t.Errorf("MinRelevance = %f, want 0.0", cfg.MinRelevance)
	}
	if !cfg.PreferSameProject {
		t.Error("PreferSameProject should be true")
	}
	if cfg.AgentFilter != nil {
		t.Errorf("AgentFilter = %v, want nil", cfg.AgentFilter)
	}
}

// =============================================================================
// DefaultTopicFilterConfig
// =============================================================================

func TestDefaultTopicFilterConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultTopicFilterConfig()

	if cfg.Enabled {
		t.Error("Enabled should be false")
	}
	if !cfg.MatchTopics {
		t.Error("MatchTopics should be true")
	}
	if cfg.ExcludeTopics != nil {
		t.Errorf("ExcludeTopics = %v, want nil", cfg.ExcludeTopics)
	}
	if cfg.SameTopicBoost != 1.5 {
		t.Errorf("SameTopicBoost = %f, want 1.5", cfg.SameTopicBoost)
	}
	if cfg.DifferentTopicPenalty != 0.5 {
		t.Errorf("DifferentTopicPenalty = %f, want 0.5", cfg.DifferentTopicPenalty)
	}
}

// =============================================================================
// FormatForAgent
// =============================================================================

func TestFormatForAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		want      InjectionFormat
	}{
		{"codex", "codex", FormatMinimal},
		{"cod", "cod", FormatMinimal},
		{"openai codex alias", "openai-codex", FormatMinimal},
		{"gemini", "gemini", FormatStructured},
		{"gmi", "gmi", FormatStructured},
		{"google gemini alias", "google-gemini", FormatStructured},
		{"claude defaults to markdown", "claude", FormatMarkdown},
		{"claude code alias defaults to markdown", "claude_code", FormatMarkdown},
		{"empty defaults to markdown", "", FormatMarkdown},
		{"unknown defaults to markdown", "aider", FormatMarkdown},
		{"case insensitive CODEX", "CODEX", FormatMinimal},
		{"case insensitive Gemini", "Gemini", FormatStructured},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FormatForAgent(tc.agentType)
			if got != tc.want {
				t.Errorf("FormatForAgent(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

// =============================================================================
// FormatContext
// =============================================================================

func TestFormatContext(t *testing.T) {
	t.Parallel()

	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "sessions/proj.jsonl", Content: "some context"},
			ComputedScore: 0.8,
		},
	}

	t.Run("empty hits", func(t *testing.T) {
		t.Parallel()
		got := FormatContext(nil, InjectConfig{Format: FormatMarkdown})
		if got != "" {
			t.Errorf("FormatContext(nil) = %q, want empty", got)
		}
	})

	t.Run("markdown format", func(t *testing.T) {
		t.Parallel()
		got := FormatContext(hits, InjectConfig{Format: FormatMarkdown})
		if !strings.Contains(got, "## Relevant Context") {
			t.Errorf("expected markdown header, got %q", got)
		}
	})

	t.Run("minimal format", func(t *testing.T) {
		t.Parallel()
		got := FormatContext(hits, InjectConfig{Format: FormatMinimal})
		if !strings.Contains(got, "// Related context:") {
			t.Errorf("expected minimal header, got %q", got)
		}
	})

	t.Run("structured format", func(t *testing.T) {
		t.Parallel()
		got := FormatContext(hits, InjectConfig{Format: FormatStructured})
		if !strings.Contains(got, "RELEVANT CONTEXT") {
			t.Errorf("expected structured header, got %q", got)
		}
	})

	t.Run("unknown format defaults to markdown", func(t *testing.T) {
		t.Parallel()
		got := FormatContext(hits, InjectConfig{Format: "unknown"})
		if !strings.Contains(got, "## Relevant Context") {
			t.Errorf("expected markdown header for unknown format, got %q", got)
		}
	})
}

// =============================================================================
// InjectContext
// =============================================================================

// [cass] enabled=false with an explicit --with-cass records the honest skip
// reason instead of the zero-hits path's "no relevant context found".
func TestInjectContextFromQueryDisabledRecordsHonestSkip(t *testing.T) {
	t.Parallel()
	queryConfig := DefaultCASSConfig()
	queryConfig.Enabled = false
	injectResult, queryResult, _ := InjectContextFromQuery(
		"test prompt", queryConfig, FilterConfig{}, DefaultInjectConfig())
	if !injectResult.Success || !queryResult.Success {
		t.Fatalf("disabled cass must degrade to a successful skip, got inject=%+v query=%+v", injectResult, queryResult)
	}
	if !strings.Contains(injectResult.Metadata.SkippedReason, "cass disabled") {
		t.Errorf("SkippedReason = %q, want a 'cass disabled' reason", injectResult.Metadata.SkippedReason)
	}
	if injectResult.Metadata.Enabled {
		t.Error("Metadata.Enabled = true, want false when cass is config-disabled")
	}
	if injectResult.ModifiedPrompt != "test prompt" {
		t.Errorf("ModifiedPrompt = %q, want the prompt unchanged", injectResult.ModifiedPrompt)
	}
}

func TestInjectContext(t *testing.T) {
	t.Parallel()

	hits := []ScoredHit{
		{
			CASSHit:       CASSHit{SourcePath: "sessions/proj.jsonl", Content: "relevant context"},
			ComputedScore: 0.85,
		},
	}

	t.Run("no hits returns prompt unchanged", func(t *testing.T) {
		t.Parallel()
		result := InjectContext("my prompt", nil, DefaultInjectConfig())
		if !result.Success {
			t.Error("expected success")
		}
		if result.ModifiedPrompt != "my prompt" {
			t.Errorf("prompt should be unchanged, got %q", result.ModifiedPrompt)
		}
		if result.Metadata.SkippedReason != "no relevant context found" {
			t.Errorf("SkippedReason = %q", result.Metadata.SkippedReason)
		}
	})

	t.Run("skip when context pct >= threshold", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultInjectConfig()
		cfg.CurrentContextPct = 80
		cfg.SkipThreshold = 60
		result := InjectContext("my prompt", hits, cfg)
		if !result.Success {
			t.Error("expected success")
		}
		if result.ModifiedPrompt != "my prompt" {
			t.Errorf("prompt should be unchanged when skipped, got %q", result.ModifiedPrompt)
		}
		if !strings.Contains(result.Metadata.SkippedReason, "context at 80%") {
			t.Errorf("SkippedReason = %q", result.Metadata.SkippedReason)
		}
	})

	t.Run("injects context into prompt", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultInjectConfig()
		result := InjectContext("my prompt", hits, cfg)
		if !result.Success {
			t.Error("expected success")
		}
		if !strings.Contains(result.ModifiedPrompt, "my prompt") {
			t.Error("modified prompt should contain original prompt")
		}
		if !strings.Contains(result.ModifiedPrompt, "---") {
			t.Error("modified prompt should contain separator")
		}
		if result.InjectedContext == "" {
			t.Error("InjectedContext should not be empty")
		}
		if result.Metadata.ItemsInjected == 0 {
			t.Error("ItemsInjected should be > 0")
		}
	})

	t.Run("dry run does not modify prompt", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultInjectConfig()
		cfg.DryRun = true
		result := InjectContext("my prompt", hits, cfg)
		if !result.Success {
			t.Error("expected success")
		}
		if result.ModifiedPrompt != "my prompt" {
			t.Errorf("dry run should not modify prompt, got %q", result.ModifiedPrompt)
		}
		if result.InjectedContext == "" {
			t.Error("dry run should still populate InjectedContext")
		}
	})

	t.Run("metadata tracks items found", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultInjectConfig()
		result := InjectContext("my prompt", hits, cfg)
		if result.Metadata.ItemsFound != 1 {
			t.Errorf("ItemsFound = %d, want 1", result.Metadata.ItemsFound)
		}
		if result.Metadata.FormatUsed != FormatMarkdown {
			t.Errorf("FormatUsed = %q, want %q", result.Metadata.FormatUsed, FormatMarkdown)
		}
		if !result.Metadata.Enabled {
			t.Error("Enabled should be true")
		}
	})

	t.Run("truncates when exceeding max tokens", func(t *testing.T) {
		t.Parallel()
		bigHits := []ScoredHit{
			{
				CASSHit:       CASSHit{SourcePath: "a.jsonl", Content: strings.Repeat("word ", 500)},
				ComputedScore: 0.9,
			},
		}
		cfg := DefaultInjectConfig()
		cfg.MaxTokens = 10
		result := InjectContext("my prompt", bigHits, cfg)
		if !result.Success {
			t.Error("expected success")
		}
		if result.Metadata.TokensAdded != 10 {
			t.Errorf("TokensAdded = %d, want 10", result.Metadata.TokensAdded)
		}
	})
}

// =============================================================================
// NewClient and ClientOptions
// =============================================================================

func TestNewClientOptions(t *testing.T) {
	t.Parallel()

	t.Run("default client", func(t *testing.T) {
		t.Parallel()
		c := NewClient()
		if c == nil {
			t.Fatal("NewClient() returned nil")
		}
		if c.timeout != 30*time.Second {
			t.Errorf("timeout = %v, want 30s", c.timeout)
		}
		exec, ok := c.executor.(*DefaultExecutor)
		if !ok {
			t.Fatal("executor should be *DefaultExecutor")
		}
		if exec.BinaryPath != "cass" {
			t.Errorf("BinaryPath = %q, want %q", exec.BinaryPath, "cass")
		}
	})

	t.Run("with timeout", func(t *testing.T) {
		t.Parallel()
		c := NewClient(WithTimeout(5 * time.Minute))
		if c.timeout != 5*time.Minute {
			t.Errorf("timeout = %v, want 5m", c.timeout)
		}
	})

	t.Run("with binary path", func(t *testing.T) {
		t.Parallel()
		c := NewClient(WithBinaryPath("/usr/local/bin/cass"))
		exec, ok := c.executor.(*DefaultExecutor)
		if !ok {
			t.Fatal("executor should be *DefaultExecutor")
		}
		if exec.BinaryPath != "/usr/local/bin/cass" {
			t.Errorf("BinaryPath = %q, want %q", exec.BinaryPath, "/usr/local/bin/cass")
		}
	})

	t.Run("with empty binary path is no-op", func(t *testing.T) {
		t.Parallel()
		c := NewClient(WithBinaryPath(""))
		exec, ok := c.executor.(*DefaultExecutor)
		if !ok {
			t.Fatal("executor should be *DefaultExecutor")
		}
		if exec.BinaryPath != "cass" {
			t.Errorf("BinaryPath = %q, want %q (empty path should be no-op)", exec.BinaryPath, "cass")
		}
	})

	t.Run("with custom executor", func(t *testing.T) {
		t.Parallel()
		custom := &DefaultExecutor{BinaryPath: "custom-cass"}
		c := NewClient(WithExecutor(custom))
		if c.executor != custom {
			t.Error("executor should be the custom one")
		}
	})

	t.Run("multiple options applied in order", func(t *testing.T) {
		t.Parallel()
		c := NewClient(
			WithTimeout(10*time.Second),
			WithBinaryPath("/opt/cass"),
		)
		if c.timeout != 10*time.Second {
			t.Errorf("timeout = %v, want 10s", c.timeout)
		}
		exec, ok := c.executor.(*DefaultExecutor)
		if !ok {
			t.Fatal("executor should be *DefaultExecutor")
		}
		if exec.BinaryPath != "/opt/cass" {
			t.Errorf("BinaryPath = %q, want %q", exec.BinaryPath, "/opt/cass")
		}
	})
}
