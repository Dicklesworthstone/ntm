package ensemble

import (
	"strings"
	"testing"
)

func TestNewSuggestionEngine(t *testing.T) {
	engine := NewSuggestionEngine()
	if engine == nil {
		t.Fatal("NewSuggestionEngine returned nil")
	}
	if len(engine.presets) == 0 {
		t.Error("engine has no presets")
	}
	if len(engine.keywords) == 0 {
		t.Error("engine has no keywords")
	}
	if len(engine.stopWords) == 0 {
		t.Error("engine has no stop words")
	}
	t.Logf("Engine initialized with %d presets, %d keyword maps, %d stop words",
		len(engine.presets), len(engine.keywords), len(engine.stopWords))
}

func TestSuggestionEngine_Suggest_EmptyQuestion(t *testing.T) {
	engine := NewSuggestionEngine()

	result := engine.Suggest("")
	if result.NoMatchReason != "empty question" {
		t.Errorf("expected 'empty question', got %q", result.NoMatchReason)
	}
	if result.TopPick != nil {
		t.Error("TopPick should be nil for empty question")
	}
	t.Logf("Empty question correctly returns: %s", result.NoMatchReason)
}

func TestSuggestionEngine_Suggest_StopWordsOnly(t *testing.T) {
	engine := NewSuggestionEngine()

	result := engine.Suggest("the a an is are was were")
	if result.NoMatchReason == "" {
		t.Error("expected no match reason for stop words only")
	}
	t.Logf("Stop words only returns: %s", result.NoMatchReason)
}

func TestSuggestionEngine_Suggest_SecurityQuestion(t *testing.T) {
	engine := NewSuggestionEngine()

	questions := []string{
		"What security vulnerabilities does this codebase have?",
		"Check for XSS and SQL injection risks",
		"Analyze the authentication flow for threats",
		"Is this API secure?",
	}

	for _, q := range questions {
		t.Run(q, func(t *testing.T) {
			result := engine.Suggest(q)
			if result.TopPick == nil {
				t.Fatalf("no suggestion for security question: %s", q)
			}
			t.Logf("Question: %q", q)
			t.Logf("Top pick: %s (score: %.2f)", result.TopPick.PresetName, result.TopPick.Score)
			for _, r := range result.TopPick.Reasons {
				t.Logf("  Reason: %s", r)
			}
			// Safety-risk should be the top pick for security questions
			if result.TopPick.PresetName != "safety-risk" {
				t.Logf("Warning: expected safety-risk, got %s", result.TopPick.PresetName)
			}
		})
	}
}

func TestSuggestionEngine_Suggest_BugQuestion(t *testing.T) {
	engine := NewSuggestionEngine()

	questions := []string{
		"Why is this function throwing an error?",
		"Debug the crash in the login flow",
		"Find bugs in this code",
		"The tests are failing, help me fix them",
	}

	for _, q := range questions {
		t.Run(q, func(t *testing.T) {
			result := engine.Suggest(q)
			if result.TopPick == nil {
				t.Fatalf("no suggestion for bug question: %s", q)
			}
			t.Logf("Question: %q", q)
			t.Logf("Top pick: %s (score: %.2f)", result.TopPick.PresetName, result.TopPick.Score)
			// Bug-hunt should rank high for bug questions
			if result.TopPick.PresetName == "bug-hunt" || result.TopPick.PresetName == "root-cause-analysis" {
				t.Logf("Correctly matched debugging preset")
			}
		})
	}
}

func TestSuggestionEngine_Suggest_IdeaQuestion(t *testing.T) {
	engine := NewSuggestionEngine()

	questions := []string{
		"What features should we add next?",
		"Brainstorm improvements for the UI",
		"Generate new ideas for the dashboard",
		"What if we added real-time notifications?",
	}

	for _, q := range questions {
		t.Run(q, func(t *testing.T) {
			result := engine.Suggest(q)
			if result.TopPick == nil {
				t.Fatalf("no suggestion for idea question: %s", q)
			}
			t.Logf("Question: %q", q)
			t.Logf("Top pick: %s (score: %.2f)", result.TopPick.PresetName, result.TopPick.Score)
			if result.TopPick.PresetName == "idea-forge" {
				t.Logf("Correctly matched idea-forge")
			}
		})
	}
}

func TestSuggestionEngine_Suggest_ArchitectureQuestion(t *testing.T) {
	engine := NewSuggestionEngine()

	questions := []string{
		"Review the system architecture",
		"Is this design scalable?",
		"Analyze the component dependencies",
		"Should we refactor to microservices?",
	}

	for _, q := range questions {
		t.Run(q, func(t *testing.T) {
			result := engine.Suggest(q)
			if result.TopPick == nil {
				t.Fatalf("no suggestion for architecture question: %s", q)
			}
			t.Logf("Question: %q", q)
			t.Logf("Top pick: %s (score: %.2f)", result.TopPick.PresetName, result.TopPick.Score)
		})
	}
}

func TestSuggestionEngine_Suggest_RootCauseQuestion(t *testing.T) {
	engine := NewSuggestionEngine()

	questions := []string{
		"Why did the server crash yesterday?",
		"What caused this failure?",
		"Investigate the root cause of the outage",
		"5 whys analysis on the incident",
	}

	for _, q := range questions {
		t.Run(q, func(t *testing.T) {
			result := engine.Suggest(q)
			if result.TopPick == nil {
				t.Fatalf("no suggestion for root cause question: %s", q)
			}
			t.Logf("Question: %q", q)
			t.Logf("Top pick: %s (score: %.2f)", result.TopPick.PresetName, result.TopPick.Score)
		})
	}
}

func TestSuggest_SecurityKeywords(t *testing.T) {
	engine := NewSuggestionEngine()
	input := "Check for security vulnerabilities and XSS risk"
	logTestStartSuggest(t, input)

	result := engine.Suggest(input)
	logTestResultSuggest(t, result.TopPick)
	assertTrueSuggest(t, "top pick present", result.TopPick != nil)
	assertEqualSuggest(t, "top pick preset", result.TopPick.PresetName, "safety-risk")
}

func TestSuggest_BugKeywords(t *testing.T) {
	engine := NewSuggestionEngine()
	input := "Debug the crash and fix the bug"
	logTestStartSuggest(t, input)

	result := engine.Suggest(input)
	logTestResultSuggest(t, result.TopPick)
	assertTrueSuggest(t, "top pick present", result.TopPick != nil)
	assertTrueSuggest(t, "bug-related preset", result.TopPick.PresetName == "bug-hunt" || result.TopPick.PresetName == "root-cause-analysis")
}

func TestSuggest_ArchitectureKeywords(t *testing.T) {
	engine := NewSuggestionEngine()
	input := "Review the architecture and system design"
	logTestStartSuggest(t, input)

	result := engine.Suggest(input)
	logTestResultSuggest(t, result.TopPick)
	assertTrueSuggest(t, "top pick present", result.TopPick != nil)
	assertEqualSuggest(t, "architecture preset", result.TopPick.PresetName, "architecture-review")
}

func TestSuggest_AmbiguousQuestion(t *testing.T) {
	engine := NewSuggestionEngine()
	input := "We need a security review and architecture assessment"
	logTestStartSuggest(t, input)

	result := engine.Suggest(input)
	logTestResultSuggest(t, result.Suggestions)
	assertTrueSuggest(t, "has suggestions", len(result.Suggestions) > 0)
	assertTrueSuggest(t, "top pick present", result.TopPick != nil)
}

func TestSuggest_EmptyQuestion(t *testing.T) {
	engine := NewSuggestionEngine()
	input := ""
	logTestStartSuggest(t, input)

	result := engine.Suggest(input)
	logTestResultSuggest(t, result.NoMatchReason)
	assertEqualSuggest(t, "empty question reason", result.NoMatchReason, "empty question")
}

func TestSuggest_IDOnly(t *testing.T) {
	engine := NewSuggestionEngine()
	input := "safety-risk"
	logTestStartSuggest(t, input)

	result := engine.Suggest(input)
	logTestResultSuggest(t, result.TopPick)
	assertTrueSuggest(t, "top pick present", result.TopPick != nil)
	assertTrueSuggest(t, "preset name matches input", strings.Contains(result.TopPick.PresetName, "safety"))
}

func logTestStartSuggest(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultSuggest(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertTrueSuggest(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualSuggest(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}

func TestSuggestionEngine_GetPreset(t *testing.T) {
	engine := NewSuggestionEngine()

	preset := engine.GetPreset("safety-risk")
	if preset == nil {
		t.Fatal("GetPreset(safety-risk) returned nil")
	}
	if preset.Name != "safety-risk" {
		t.Errorf("expected name 'safety-risk', got %q", preset.Name)
	}
	t.Logf("Found preset: %s - %s", preset.Name, preset.Description)

	// Test non-existent preset
	notFound := engine.GetPreset("not-a-preset")
	if notFound != nil {
		t.Error("expected nil for non-existent preset")
	}
}

func TestSuggestionEngine_MultipleSuggestions(t *testing.T) {
	engine := NewSuggestionEngine()

	// A question that could match multiple presets
	result := engine.Suggest("Analyze the codebase for security issues and bugs")

	if len(result.Suggestions) == 0 {
		t.Fatal("expected at least one suggestion")
	}

	t.Logf("Question: %q", result.Question)
	t.Logf("Number of suggestions: %d", len(result.Suggestions))
	for i, s := range result.Suggestions {
		t.Logf("  %d. %s (score: %.2f) - %v", i+1, s.PresetName, s.Score, s.Reasons)
	}

	// Verify suggestions are sorted by score descending
	for i := 1; i < len(result.Suggestions); i++ {
		if result.Suggestions[i].Score > result.Suggestions[i-1].Score {
			t.Error("suggestions not sorted by score descending")
		}
	}
}

func TestSuggestionEngine_TieBreaker(t *testing.T) {
	engine := NewSuggestionEngine()

	// Questions with potentially equal scores should still produce a deterministic result
	result1 := engine.Suggest("analyze the code")
	result2 := engine.Suggest("analyze the code")

	if result1.TopPick == nil || result2.TopPick == nil {
		t.Skip("no suggestions returned")
	}

	if result1.TopPick.PresetName != result2.TopPick.PresetName {
		t.Errorf("non-deterministic: got %q and %q", result1.TopPick.PresetName, result2.TopPick.PresetName)
	}
	t.Logf("Deterministic result: %s", result1.TopPick.PresetName)
}

func TestGlobalSuggestionEngine(t *testing.T) {
	engine1 := GlobalSuggestionEngine()
	engine2 := GlobalSuggestionEngine()

	if engine1 != engine2 {
		t.Error("GlobalSuggestionEngine should return the same instance")
	}
	t.Log("GlobalSuggestionEngine correctly returns singleton")
}

func BenchmarkSuggest(b *testing.B) {
	engine := NewSuggestionEngine()
	question := "What security vulnerabilities exist in this codebase?"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Suggest(question)
	}
}
