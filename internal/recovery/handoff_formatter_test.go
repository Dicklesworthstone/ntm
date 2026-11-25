package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestLoadHandoffRetainsProjectIdentityAndReadableFallback(t *testing.T) {
	project, unrelated := t.TempDir(), t.TempDir()
	const session = "resume-work"
	var selected string
	for _, source := range []struct{ dir, goal string }{
		{project, "Finish the target project migration"},
		{unrelated, "Unrelated project work"},
	} {
		h := handoff.New(session).WithGoalAndNow(source.goal, "Verify the interrupted migration")
		h.Decisions = map[string]string{"storage": "retain existing data"}
		path, err := handoff.NewWriter(source.dir).Write(h, "continuity")
		if err != nil {
			t.Fatal(err)
		}
		if source.dir == project {
			selected = path
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(selected), "99999999-unreadable.yaml"), []byte("[invalid YAML"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHandoff(context.Background(), session, project)
	if err != nil || h == nil {
		t.Fatalf("load target handoff: context=%+v error=%v", h, err)
	}
	resolved, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	if h.Path != resolved || h.Goal != "Finish the target project migration" ||
		h.Now != "Verify the interrupted migration" || h.Decisions["storage"] != "retain existing data" {
		t.Fatalf("handoff lost its source identity or recovery context: %+v", h)
	}
	prompt := TruncateHandoffPrompt(GetInjectionForType(SessionFreshSpawn, h, nil), MaxHandoffTokens*4)
	if !strings.Contains(prompt, h.Now) || !strings.Contains(prompt, h.Goal) || strings.Contains(prompt, "Unrelated project work") {
		t.Fatalf("selected context was not retained in the recovery prompt: %q", prompt)
	}
}

func TestLoadHandoffRefusesInvalidSources(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "malformed", "wrong-session", "outside-project"} {
		t.Run(kind, func(t *testing.T) {
			project := t.TempDir()
			if kind == "outside-project" {
				external := t.TempDir()
				h := handoff.New("resume-work").WithGoalAndNow("External project", "Do not inject")
				if _, err := handoff.NewWriter(external).Write(h, "external"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(external, ".ntm"), filepath.Join(project, ".ntm")); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
			} else if kind != "missing" {
				dir := filepath.Join(project, ".ntm", "handoffs", "resume-work")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				content := "session: resume-work\ngoal: ''\nnow: ''\n"
				switch kind {
				case "malformed":
					content = "[invalid YAML"
				case "wrong-session":
					content = "session: other-work\ngoal: Another session\nnow: Do not inject\n"
				}
				if err := os.WriteFile(filepath.Join(dir, "20260101-context.yaml"), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			h, err := LoadHandoff(context.Background(), "resume-work", project)
			if h != nil {
				t.Fatalf("unusable source produced recovery context: %+v", h)
			}
			wantError := kind != "missing" && kind != "empty"
			if (err != nil) != wantError {
				t.Fatalf("source failure=%v, wantError=%t", err, wantError)
			}
		})
	}
}

func TestLoadHandoffPreflightAndRemoteIsolation(t *testing.T) {
	project := t.TempDir()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		session string
		project string
	}{
		{"nil context", nil, "resume-work", project},
		{"canceled", canceled, "resume-work", project},
		{"invalid session", context.Background(), "../other", project},
		{"relative project", context.Background(), "resume-work", "relative"},
		{"missing project", context.Background(), "resume-work", filepath.Join(project, "missing")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := LoadHandoff(tc.ctx, tc.session, tc.project)
			if err == nil || h != nil {
				t.Fatalf("invalid source accepted: handoff=%+v error=%v", h, err)
			}
			if tc.name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("context cancellation lost: %v", err)
			}
		})
	}
	oldClient := tmux.DefaultClient
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	tmux.DefaultClient = tmux.NewClient("user@remote")
	h, err := LoadHandoff(context.Background(), "resume-work", project)
	if h != nil || err == nil || !strings.Contains(err.Error(), "remote tmux") {
		t.Fatalf("local context crossed a remote boundary: handoff=%+v error=%v", h, err)
	}
}

func TestTruncateHandoffPromptPreservesPrefixAndUTF8(t *testing.T) {
	prompt := "Resume the migration first\n" + strings.Repeat("界", MaxHandoffTokens*4)
	for _, budget := range []int{-1, 0, 1, 40, 100, 101, 102, MaxHandoffTokens * 4, len(prompt)} {
		got := TruncateHandoffPrompt(prompt, budget)
		if !utf8.ValidString(got) || (budget > 0 && len(got) > budget) {
			t.Fatalf("budget %d corrupted or exceeded the prompt: length=%d valid=%t", budget, len(got), utf8.ValidString(got))
		}
		if budget <= 40 && got != "" {
			t.Fatalf("budget %d retained unusable context: %q", budget, got)
		}
		if budget >= 100 && !strings.HasPrefix(got, "Resume the migration first") {
			t.Fatalf("budget %d discarded the immediate task: %q", budget, got)
		}
		if budget >= 100 && budget < len(prompt) && !strings.Contains(got, "[Handoff truncated to fit recovery budget.]") {
			t.Fatalf("budget %d silently truncated recovery: %q", budget, got)
		}
		if budget == len(prompt) && got != prompt {
			t.Fatal("a fitting handoff was changed")
		}
	}
}

func TestHandoffContextFromHandoff(t *testing.T) {
	t.Log("RECOVERY_TEST: HandoffContextFromHandoff | Testing conversion from Handoff to HandoffContext")

	tests := []struct {
		name     string
		handoff  *handoff.Handoff
		path     string
		wantNil  bool
		wantGoal string
		wantNow  string
	}{
		{
			name:    "nil handoff returns nil",
			handoff: nil,
			path:    "",
			wantNil: true,
		},
		{
			name: "converts basic handoff",
			handoff: &handoff.Handoff{
				Goal:      "Implemented feature X",
				Now:       "Write tests for feature X",
				Status:    "complete",
				Outcome:   "SUCCEEDED",
				CreatedAt: time.Now().Add(-1 * time.Hour),
			},
			path:     "/test/path/handoff.yaml",
			wantNil:  false,
			wantGoal: "Implemented feature X",
			wantNow:  "Write tests for feature X",
		},
		{
			name: "converts handoff with all fields",
			handoff: &handoff.Handoff{
				Goal:             "Completed refactoring",
				Now:              "Run test suite",
				Blockers:         []string{"CI failing", "Missing deps"},
				Decisions:        map[string]string{"arch": "microservices"},
				Findings:         map[string]string{"perf": "10x faster"},
				Next:             []string{"Deploy", "Monitor"},
				Status:           "partial",
				Outcome:          "PARTIAL_PLUS",
				ActiveBeads:      []string{"bd-123"},
				AgentMailThreads: []string{"thread-1"},
				CMMemories:       []string{"memory-1"},
				CreatedAt:        time.Now().Add(-30 * time.Minute),
			},
			path:     "/test/handoff.yaml",
			wantNil:  false,
			wantGoal: "Completed refactoring",
			wantNow:  "Run test suite",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("RECOVERY_TEST: HandoffContextFromHandoff | Case=%s", tt.name)

			result := HandoffContextFromHandoff(tt.handoff, tt.path)

			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil result")
			}

			if result.Goal != tt.wantGoal {
				t.Errorf("Goal = %q, want %q", result.Goal, tt.wantGoal)
			}

			if result.Now != tt.wantNow {
				t.Errorf("Now = %q, want %q", result.Now, tt.wantNow)
			}

			if result.Path != tt.path {
				t.Errorf("Path = %q, want %q", result.Path, tt.path)
			}

			t.Logf("RECOVERY_TEST: HandoffContextFromHandoff | Success | Goal=%s Now=%s", result.Goal, result.Now)
		})
	}
}

func TestFormatHandoffContext(t *testing.T) {
	t.Log("RECOVERY_TEST: FormatHandoffContext | Testing handoff formatting")

	tests := []struct {
		name            string
		context         *HandoffContext
		wantEmpty       bool
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:      "nil context returns empty",
			context:   nil,
			wantEmpty: true,
		},
		{
			name: "formats basic context",
			context: &HandoffContext{
				Goal: "Built the API",
				Now:  "Add authentication",
			},
			wantEmpty: false,
			wantContains: []string{
				"## Previous Session Context",
				"**Your immediate task:** Add authentication",
				"**Last session achieved:** Built the API",
			},
		},
		{
			name: "includes next steps",
			context: &HandoffContext{
				Goal: "Refactored module",
				Now:  "Test the changes",
				Next: []string{"Run unit tests", "Update docs", "Deploy"},
			},
			wantEmpty: false,
			wantContains: []string{
				"**Suggested next steps:**",
				"1. Run unit tests",
				"2. Update docs",
				"3. Deploy",
			},
		},
		{
			name: "includes decisions",
			context: &HandoffContext{
				Goal:      "Made architecture decision",
				Now:       "Implement the design",
				Decisions: map[string]string{"database": "postgres", "cache": "redis"},
			},
			wantEmpty: false,
			wantContains: []string{
				"**Key decisions made:**",
			},
		},
		{
			name: "includes findings",
			context: &HandoffContext{
				Goal:     "Investigated performance",
				Now:      "Apply optimizations",
				Findings: map[string]string{"bottleneck": "database queries"},
			},
			wantEmpty: false,
			wantContains: []string{
				"**Key findings:**",
				"bottleneck",
			},
		},
		{
			name: "includes blockers",
			context: &HandoffContext{
				Goal:     "Started implementation",
				Now:      "Resolve blockers first",
				Blockers: []string{"Missing API key", "CI is red"},
			},
			wantEmpty: false,
			wantContains: []string{
				"**Known blockers:**",
				"Missing API key",
			},
		},
		{
			name: "prioritizes now over goal",
			context: &HandoffContext{
				Goal: "This is the goal",
				Now:  "This is the immediate task",
			},
			wantEmpty: false,
			wantContains: []string{
				"**Your immediate task:** This is the immediate task",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("RECOVERY_TEST: FormatHandoffContext | Case=%s", tt.name)

			result := FormatHandoffContext(tt.context)

			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
				return
			}

			if result == "" {
				t.Error("expected non-empty string")
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("result should contain %q, got:\n%s", want, result)
				}
			}

			for _, notWant := range tt.wantNotContains {
				if strings.Contains(result, notWant) {
					t.Errorf("result should not contain %q, got:\n%s", notWant, result)
				}
			}

			// Verify token budget
			estimatedTokens := len(result) / 4
			if estimatedTokens > MaxHandoffTokens+50 { // allow small buffer
				t.Errorf("output exceeds token budget: %d tokens (max %d)", estimatedTokens, MaxHandoffTokens)
			}

			t.Logf("RECOVERY_TEST: FormatHandoffContext | Success | OutputLen=%d EstTokens=%d", len(result), estimatedTokens)
		})
	}
}

func TestFormatMinimalHandoff(t *testing.T) {
	t.Log("RECOVERY_TEST: FormatMinimalHandoff | Testing minimal format")

	tests := []struct {
		name         string
		context      *HandoffContext
		wantEmpty    bool
		wantContains []string
	}{
		{
			name:      "nil context returns empty",
			context:   nil,
			wantEmpty: true,
		},
		{
			name: "formats goal and now",
			context: &HandoffContext{
				Goal: "Completed task",
				Now:  "Next task",
			},
			wantEmpty: false,
			wantContains: []string{
				"Last: Completed task",
				"Now: Next task",
				"|", // separator
			},
		},
		{
			name: "only goal",
			context: &HandoffContext{
				Goal: "Completed task",
			},
			wantEmpty:    false,
			wantContains: []string{"Last: Completed task"},
		},
		{
			name: "only now",
			context: &HandoffContext{
				Now: "Next task",
			},
			wantEmpty:    false,
			wantContains: []string{"Now: Next task"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("RECOVERY_TEST: FormatMinimalHandoff | Case=%s", tt.name)

			result := FormatMinimalHandoff(tt.context)

			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("result should contain %q, got %q", want, result)
				}
			}

			t.Logf("RECOVERY_TEST: FormatMinimalHandoff | Success | Result=%s", result)
		})
	}
}

func TestGetInjectionForType(t *testing.T) {
	t.Log("RECOVERY_TEST: GetInjectionForType | Testing session type handling")

	ctx := &HandoffContext{
		Goal: "Completed task",
		Now:  "Continue with next",
		Next: []string{"Step 1", "Step 2"},
	}
	memories := []string{"Memory 1", "Memory 2", "Memory 3", "Memory 4"}

	tests := []struct {
		name         string
		sessionType  SessionType
		wantContains []string
		wantMinimal  bool
	}{
		{
			name:        "fresh spawn gets full context",
			sessionType: SessionFreshSpawn,
			wantContains: []string{
				"## Previous Session Context",
				"**Your immediate task:**",
				"**Last session achieved:**",
			},
			wantMinimal: false,
		},
		{
			name:        "after clear gets handoff plus memories",
			sessionType: SessionAfterClear,
			wantContains: []string{
				"## Previous Session Context",
				"**Relevant memories:**",
			},
			wantMinimal: false,
		},
		{
			name:        "after compact gets minimal",
			sessionType: SessionAfterCompact,
			wantContains: []string{
				"Last:",
				"Now:",
			},
			wantMinimal: true,
		},
		{
			name:        "unknown session type gets full context",
			sessionType: SessionType(99),
			wantContains: []string{
				"## Previous Session Context",
				"**Your immediate task:**",
			},
			wantMinimal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("RECOVERY_TEST: GetInjectionForType | SessionType=%d Case=%s", tt.sessionType, tt.name)

			result := GetInjectionForType(tt.sessionType, ctx, memories)

			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("result should contain %q, got:\n%s", want, result)
				}
			}

			if tt.wantMinimal {
				// Minimal should not have markdown headers
				if strings.Contains(result, "##") {
					t.Errorf("minimal format should not contain markdown headers, got:\n%s", result)
				}
			}

			t.Logf("RECOVERY_TEST: GetInjectionForType | Success | OutputLen=%d", len(result))
		})
	}
}

func TestHumanizeDuration(t *testing.T) {
	t.Log("RECOVERY_TEST: HumanizeDuration | Testing duration formatting")

	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30s ago"},
		{5 * time.Minute, "5m ago"},
		{2 * time.Hour, "2h ago"},
		{48 * time.Hour, "2d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			result := HumanizeDuration(tt.duration)
			if result != tt.want {
				t.Errorf("HumanizeDuration(%v) = %q, want %q", tt.duration, result, tt.want)
			}
		})
	}
}

func TestTruncateToTokens(t *testing.T) {
	t.Log("RECOVERY_TEST: truncateToTokens | Testing token truncation")

	tests := []struct {
		input     string
		maxTokens int
		wantLen   int
		wantEnd   string
	}{
		{"short", 10, 5, "short"},
		{"this is a longer string that should be truncated", 5, 20, "..."},
		{"exactly twenty chars", 5, 20, "ars"},
	}

	for _, tt := range tests {
		t.Run(tt.input[:min(10, len(tt.input))], func(t *testing.T) {
			result := truncateToTokens(tt.input, tt.maxTokens)
			if len(result) > tt.maxTokens*4 {
				t.Errorf("result too long: %d bytes (max %d)", len(result), tt.maxTokens*4)
			}
			if !strings.HasSuffix(result, tt.wantEnd) {
				t.Errorf("result should end with %q, got %q", tt.wantEnd, result)
			}
		})
	}
}

func TestTokenBudgetEnforcement(t *testing.T) {
	t.Log("RECOVERY_TEST: TokenBudgetEnforcement | Testing budget stays under limit")

	// Create a context with lots of data
	ctx := &HandoffContext{
		Goal:     strings.Repeat("goal text ", 100),
		Now:      strings.Repeat("now text ", 100),
		Next:     []string{strings.Repeat("step ", 50), strings.Repeat("step ", 50), strings.Repeat("step ", 50), strings.Repeat("step ", 50)},
		Blockers: []string{strings.Repeat("blocker ", 50), strings.Repeat("blocker ", 50)},
		Decisions: map[string]string{
			"key1": strings.Repeat("value ", 50),
			"key2": strings.Repeat("value ", 50),
			"key3": strings.Repeat("value ", 50),
		},
		Findings: map[string]string{
			"finding1": strings.Repeat("data ", 50),
			"finding2": strings.Repeat("data ", 50),
		},
	}

	result := FormatHandoffContext(ctx)
	estimatedTokens := len(result) / 4

	t.Logf("RECOVERY_TEST: TokenBudgetEnforcement | OutputLen=%d EstTokens=%d MaxTokens=%d",
		len(result), estimatedTokens, MaxHandoffTokens)

	// Allow 20% buffer for formatting overhead
	maxAllowed := int(float64(MaxHandoffTokens) * 1.2)
	if estimatedTokens > maxAllowed {
		t.Errorf("output exceeds token budget: %d tokens (max allowed %d)", estimatedTokens, maxAllowed)
	}
}

func TestFormatHandoffContext_DeterministicMapOrder(t *testing.T) {
	t.Log("RECOVERY_TEST: FormatHandoffContext_DeterministicMapOrder | Testing sorted map output")

	ctx := &HandoffContext{
		Goal: "Completed audit",
		Now:  "Apply fixes",
		Decisions: map[string]string{
			"zeta":  "last",
			"alpha": "first",
			"mid":   "middle",
		},
		Findings: map[string]string{
			"omega": "later",
			"beta":  "earlier",
		},
	}

	first := FormatHandoffContext(ctx)
	second := FormatHandoffContext(ctx)

	if first != second {
		t.Fatalf("FormatHandoffContext() should be deterministic across calls")
	}

	alphaIndex := strings.Index(first, "- alpha: first")
	midIndex := strings.Index(first, "- mid: middle")
	zetaIndex := strings.Index(first, "- zeta: last")
	betaIndex := strings.Index(first, "- beta: earlier")
	omegaIndex := strings.Index(first, "- omega: later")

	if alphaIndex == -1 || midIndex == -1 || zetaIndex == -1 {
		t.Fatalf("decision lines missing from output:\n%s", first)
	}
	if betaIndex == -1 || omegaIndex == -1 {
		t.Fatalf("finding lines missing from output:\n%s", first)
	}
	if !(alphaIndex < midIndex && midIndex < zetaIndex) {
		t.Fatalf("decisions not sorted lexicographically:\n%s", first)
	}
	if !(betaIndex < omegaIndex) {
		t.Fatalf("findings not sorted lexicographically:\n%s", first)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
