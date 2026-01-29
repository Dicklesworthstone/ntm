package cass

import (
	"sort"
	"strings"
	"testing"
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
		got := truncateToTokens(input, 10)     // 10 * 4 = 40 chars max
		if len(got) > 100 {                    // 40 chars + truncation message
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
		name  string
		path  string
		want  string
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
