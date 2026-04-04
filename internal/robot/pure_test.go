package robot

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
)

func TestParseJFPIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty string", "", nil},
		{"single id", "abc123", []string{"abc123"}},
		{"comma separated", "a,b,c", []string{"a", "b", "c"}},
		{"space separated", "a b c", []string{"a", "b", "c"}},
		{"newline separated", "a\nb\nc", []string{"a", "b", "c"}},
		{"tab separated", "a\tb\tc", []string{"a", "b", "c"}},
		{"mixed delimiters", "a, b\tc\nd", []string{"a", "b", "c", "d"}},
		{"extra whitespace", "  a , b , c  ", []string{"a", "b", "c"}},
		{"empty between delimiters", "a,,b", []string{"a", "b"}},
		{"only whitespace", "   ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseJFPIDs(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseJFPIDs(%q) = %v (len %d), want %v (len %d)", tt.raw, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseJFPIDs(%q)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestNormalizedProgramType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		program string
		want    string
	}{
		{"claude lowercase", "claude", "claude"},
		{"claude mixed case", "Claude-Code", "claude"},
		{"claude with suffix", "claude-code-v2", "claude"},
		{"codex lowercase", "codex", "codex"},
		{"codex mixed case", "Codex-CLI", "codex"},
		{"gemini lowercase", "gemini", "gemini"},
		{"gemini mixed case", "Gemini-Pro", "gemini"},
		{"unknown program", "vim", "unknown"},
		{"empty string", "", "unknown"},
		{"random string", "foobarbaz", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizedProgramType(tt.program)
			if got != tt.want {
				t.Errorf("normalizedProgramType(%q) = %q, want %q", tt.program, got, tt.want)
			}
		})
	}
}

func TestEscapeQuotes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no quotes", "hello world", "hello world"},
		{"single quote", `say "hi"`, `say \"hi\"`},
		{"multiple quotes", `"a" and "b"`, `\"a\" and \"b\"`},
		{"empty string", "", ""},
		{"only quotes", `""`, `\"\"`},
		{"backslash before quote", `say \"hi\"`, `say \\"hi\\"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := escapeQuotes(tt.input)
			if got != tt.want {
				t.Errorf("escapeQuotes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		session string
		pane    int
		want    string
	}{
		{"basic", "mysession", 0, "mysession:1.0"},
		{"pane 5", "test", 5, "test:1.5"},
		{"complex session name", "my-ai-session", 12, "my-ai-session:1.12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatTarget(tt.session, tt.pane)
			if got != tt.want {
				t.Errorf("formatTarget(%q, %d) = %q, want %q", tt.session, tt.pane, got, tt.want)
			}
		})
	}
}

func TestSummarizeReservations(t *testing.T) {
	t.Parallel()

	now := time.Now()
	future := now.Add(300 * time.Second)

	reservations := []agentmail.FileReservation{
		{
			ID:          1,
			PathPattern: "internal/robot/*.go",
			AgentName:   "BlueLake",
			Exclusive:   true,
			Reason:      "editing",
			ExpiresTS:   agentmail.FlexTime{Time: future},
		},
		{
			ID:          2,
			PathPattern: "internal/cli/*.go",
			AgentName:   "AmberMill",
			Exclusive:   false,
			Reason:      "reading",
			ExpiresTS:   agentmail.FlexTime{Time: future},
		},
	}

	got := summarizeReservations(reservations)

	if len(got) != 2 {
		t.Fatalf("summarizeReservations: got %d results, want 2", len(got))
	}

	// Should be sorted by agent name first
	if got[0].Agent != "AmberMill" {
		t.Errorf("expected first agent to be AmberMill (sorted), got %q", got[0].Agent)
	}
	if got[1].Agent != "BlueLake" {
		t.Errorf("expected second agent to be BlueLake (sorted), got %q", got[1].Agent)
	}

	// Check fields
	if got[0].Pattern != "internal/cli/*.go" {
		t.Errorf("got[0].Pattern = %q, want %q", got[0].Pattern, "internal/cli/*.go")
	}
	if got[0].Exclusive {
		t.Error("got[0].Exclusive = true, want false")
	}
	if got[0].ExpiresInSeconds <= 0 {
		t.Errorf("got[0].ExpiresInSeconds = %d, want > 0", got[0].ExpiresInSeconds)
	}
}

func TestSummarizeReservations_Empty(t *testing.T) {
	t.Parallel()
	got := summarizeReservations(nil)
	if len(got) != 0 {
		t.Errorf("summarizeReservations(nil) returned %d items, want 0", len(got))
	}
}

func TestSummarizeReservations_ExpiredClampedToZero(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-60 * time.Second)
	reservations := []agentmail.FileReservation{
		{
			ID:          1,
			PathPattern: "*.go",
			AgentName:   "Agent",
			ExpiresTS:   agentmail.FlexTime{Time: past},
		},
	}
	got := summarizeReservations(reservations)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].ExpiresInSeconds != 0 {
		t.Errorf("expired reservation ExpiresInSeconds = %d, want 0", got[0].ExpiresInSeconds)
	}
}

func TestDetectReservationConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		reservations []agentmail.FileReservation
		wantCount    int
	}{
		{
			name:         "no reservations",
			reservations: nil,
			wantCount:    0,
		},
		{
			name: "no conflicts single agent",
			reservations: []agentmail.FileReservation{
				{PathPattern: "*.go", AgentName: "Agent1", Exclusive: true},
			},
			wantCount: 0,
		},
		{
			name: "no conflict shared reservations",
			reservations: []agentmail.FileReservation{
				{PathPattern: "*.go", AgentName: "Agent1", Exclusive: false},
				{PathPattern: "*.go", AgentName: "Agent2", Exclusive: false},
			},
			wantCount: 0,
		},
		{
			name: "conflict exclusive with multiple agents",
			reservations: []agentmail.FileReservation{
				{PathPattern: "*.go", AgentName: "Agent1", Exclusive: true},
				{PathPattern: "*.go", AgentName: "Agent2", Exclusive: false},
			},
			wantCount: 1,
		},
		{
			name: "multiple conflicts",
			reservations: []agentmail.FileReservation{
				{PathPattern: "a.go", AgentName: "X", Exclusive: true},
				{PathPattern: "a.go", AgentName: "Y", Exclusive: false},
				{PathPattern: "b.go", AgentName: "P", Exclusive: true},
				{PathPattern: "b.go", AgentName: "Q", Exclusive: false},
			},
			wantCount: 2,
		},
		{
			name: "same pattern different agents both exclusive",
			reservations: []agentmail.FileReservation{
				{PathPattern: "src/*.go", AgentName: "Alpha", Exclusive: true},
				{PathPattern: "src/*.go", AgentName: "Beta", Exclusive: true},
			},
			wantCount: 1,
		},
		{
			name: "overlapping patterns conflict when one reservation is exclusive",
			reservations: []agentmail.FileReservation{
				{PathPattern: "internal/**", AgentName: "Alpha", Exclusive: true},
				{PathPattern: "internal/cli/*.go", AgentName: "Beta", Exclusive: false},
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := detectReservationConflicts(tt.reservations)
			if len(got) != tt.wantCount {
				t.Fatalf("detectReservationConflicts: got %d conflicts, want %d; conflicts=%+v", len(got), tt.wantCount, got)
			}
		})
	}
}

func TestDetectReservationConflicts_HoldersSorted(t *testing.T) {
	t.Parallel()
	reservations := []agentmail.FileReservation{
		{PathPattern: "*.go", AgentName: "Zulu", Exclusive: true},
		{PathPattern: "*.go", AgentName: "Alpha", Exclusive: false},
		{PathPattern: "*.go", AgentName: "Mike", Exclusive: false},
	}
	got := detectReservationConflicts(reservations)
	if len(got) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(got))
	}
	if len(got[0].Holders) != 3 {
		t.Fatalf("expected 3 holders, got %d", len(got[0].Holders))
	}
	if got[0].Holders[0] != "Alpha" || got[0].Holders[1] != "Mike" || got[0].Holders[2] != "Zulu" {
		t.Errorf("holders not sorted: %v", got[0].Holders)
	}
}

func TestMergeBudgetConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		base     ensemble.BudgetConfig
		override ensemble.BudgetConfig
		check    func(t *testing.T, result ensemble.BudgetConfig)
	}{
		{
			name: "empty override keeps base",
			base: ensemble.BudgetConfig{
				MaxTokensPerMode:       1000,
				MaxTotalTokens:         5000,
				SynthesisReserveTokens: 200,
				ContextReserveTokens:   100,
				TimeoutPerMode:         30 * time.Second,
				TotalTimeout:           5 * time.Minute,
				MaxRetries:             3,
			},
			override: ensemble.BudgetConfig{},
			check: func(t *testing.T, r ensemble.BudgetConfig) {
				if r.MaxTokensPerMode != 1000 {
					t.Errorf("MaxTokensPerMode = %d, want 1000", r.MaxTokensPerMode)
				}
				if r.MaxTotalTokens != 5000 {
					t.Errorf("MaxTotalTokens = %d, want 5000", r.MaxTotalTokens)
				}
				if r.MaxRetries != 3 {
					t.Errorf("MaxRetries = %d, want 3", r.MaxRetries)
				}
			},
		},
		{
			name: "override replaces nonzero fields",
			base: ensemble.BudgetConfig{
				MaxTokensPerMode: 1000,
				MaxTotalTokens:   5000,
				MaxRetries:       3,
			},
			override: ensemble.BudgetConfig{
				MaxTokensPerMode: 2000,
				MaxRetries:       5,
			},
			check: func(t *testing.T, r ensemble.BudgetConfig) {
				if r.MaxTokensPerMode != 2000 {
					t.Errorf("MaxTokensPerMode = %d, want 2000", r.MaxTokensPerMode)
				}
				if r.MaxTotalTokens != 5000 {
					t.Errorf("MaxTotalTokens = %d, want 5000 (unchanged)", r.MaxTotalTokens)
				}
				if r.MaxRetries != 5 {
					t.Errorf("MaxRetries = %d, want 5", r.MaxRetries)
				}
			},
		},
		{
			name: "all fields overridden",
			base: ensemble.BudgetConfig{
				MaxTokensPerMode:       100,
				MaxTotalTokens:         500,
				SynthesisReserveTokens: 50,
				ContextReserveTokens:   25,
				TimeoutPerMode:         10 * time.Second,
				TotalTimeout:           1 * time.Minute,
				MaxRetries:             1,
			},
			override: ensemble.BudgetConfig{
				MaxTokensPerMode:       200,
				MaxTotalTokens:         1000,
				SynthesisReserveTokens: 100,
				ContextReserveTokens:   50,
				TimeoutPerMode:         20 * time.Second,
				TotalTimeout:           2 * time.Minute,
				MaxRetries:             2,
			},
			check: func(t *testing.T, r ensemble.BudgetConfig) {
				if r.MaxTokensPerMode != 200 {
					t.Errorf("MaxTokensPerMode = %d, want 200", r.MaxTokensPerMode)
				}
				if r.MaxTotalTokens != 1000 {
					t.Errorf("MaxTotalTokens = %d, want 1000", r.MaxTotalTokens)
				}
				if r.SynthesisReserveTokens != 100 {
					t.Errorf("SynthesisReserveTokens = %d, want 100", r.SynthesisReserveTokens)
				}
				if r.ContextReserveTokens != 50 {
					t.Errorf("ContextReserveTokens = %d, want 50", r.ContextReserveTokens)
				}
				if r.TimeoutPerMode != 20*time.Second {
					t.Errorf("TimeoutPerMode = %v, want 20s", r.TimeoutPerMode)
				}
				if r.TotalTimeout != 2*time.Minute {
					t.Errorf("TotalTimeout = %v, want 2m", r.TotalTimeout)
				}
				if r.MaxRetries != 2 {
					t.Errorf("MaxRetries = %d, want 2", r.MaxRetries)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := mergeBudgetConfig(tt.base, tt.override)
			tt.check(t, result)
		})
	}
}

func TestBuildEnsembleSuggestHints(t *testing.T) {
	t.Parallel()

	t.Run("nil top pick returns nil", func(t *testing.T) {
		t.Parallel()
		output := EnsembleSuggestOutput{TopPick: nil}
		got := buildEnsembleSuggestHints(output)
		if got != nil {
			t.Errorf("expected nil hints for nil TopPick, got %+v", got)
		}
	})

	t.Run("single suggestion", func(t *testing.T) {
		t.Parallel()
		output := EnsembleSuggestOutput{
			TopPick: &EnsembleSuggestion{
				PresetName:  "research",
				DisplayName: "Research Mode",
				SpawnCmd:    "ntm ensemble spawn research",
			},
			Suggestions: []EnsembleSuggestion{
				{PresetName: "research", DisplayName: "Research Mode"},
			},
		}
		got := buildEnsembleSuggestHints(output)
		if got == nil {
			t.Fatal("expected non-nil hints")
		}
		if got.Summary != "Best match: Research Mode" {
			t.Errorf("Summary = %q, want %q", got.Summary, "Best match: Research Mode")
		}
		if got.SpawnCommand != "ntm ensemble spawn research" {
			t.Errorf("SpawnCommand = %q, want %q", got.SpawnCommand, "ntm ensemble spawn research")
		}
		if len(got.SuggestedActions) != 1 {
			t.Fatalf("SuggestedActions length = %d, want 1", len(got.SuggestedActions))
		}
		if got.SuggestedActions[0].Action != "spawn-ensemble" {
			t.Errorf("action = %q, want spawn-ensemble", got.SuggestedActions[0].Action)
		}
	})

	t.Run("multiple suggestions adds alternative", func(t *testing.T) {
		t.Parallel()
		output := EnsembleSuggestOutput{
			TopPick: &EnsembleSuggestion{
				PresetName:  "research",
				DisplayName: "Research Mode",
				SpawnCmd:    "ntm ensemble spawn research",
			},
			Suggestions: []EnsembleSuggestion{
				{PresetName: "research", DisplayName: "Research Mode"},
				{PresetName: "debug", DisplayName: "Debug Mode"},
			},
		}
		got := buildEnsembleSuggestHints(output)
		if got == nil {
			t.Fatal("expected non-nil hints")
		}
		if len(got.SuggestedActions) != 2 {
			t.Fatalf("SuggestedActions length = %d, want 2", len(got.SuggestedActions))
		}
		if got.SuggestedActions[1].Action != "consider-alternatives" {
			t.Errorf("second action = %q, want consider-alternatives", got.SuggestedActions[1].Action)
		}
		if got.SuggestedActions[1].Priority != 2 {
			t.Errorf("second action priority = %d, want 2", got.SuggestedActions[1].Priority)
		}
	})
}

func TestBuildEnsembleHints(t *testing.T) {
	t.Parallel()

	t.Run("modes pending suggests wait", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary: EnsembleSummary{TotalModes: 3, Completed: 1, Working: 1, Pending: 1},
			Ensemble: EnsembleState{
				Modes:     []EnsembleMode{},
				Synthesis: EnsembleSynthesis{Status: "waiting"},
			},
		}
		got := buildEnsembleHints(output)
		if got == nil {
			t.Fatal("expected non-nil hints")
		}
		foundWait := false
		for _, a := range got.SuggestedActions {
			if a.Action == "wait" {
				foundWait = true
			}
		}
		if !foundWait {
			t.Error("expected 'wait' action when modes are pending")
		}
	})

	t.Run("all complete synthesis ready suggests synthesize", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary: EnsembleSummary{TotalModes: 3, Completed: 3, Working: 0, Pending: 0},
			Ensemble: EnsembleState{
				Modes:     []EnsembleMode{},
				Synthesis: EnsembleSynthesis{Status: "ready"},
			},
		}
		got := buildEnsembleHints(output)
		if got == nil {
			t.Fatal("expected non-nil hints")
		}
		foundSynthesize := false
		for _, a := range got.SuggestedActions {
			if a.Action == "synthesize" {
				foundSynthesize = true
			}
		}
		if !foundSynthesize {
			t.Error("expected 'synthesize' action when all modes complete and synthesis ready")
		}
	})

	t.Run("error mode adds warning", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary: EnsembleSummary{TotalModes: 2, Completed: 1, Errors: 1},
			Ensemble: EnsembleState{
				Modes: []EnsembleMode{
					{ID: "m1", Status: "done"},
					{ID: "m2", Status: string(ensemble.AssignmentError)},
				},
				Synthesis: EnsembleSynthesis{Status: "ready"},
			},
		}
		got := buildEnsembleHints(output)
		if got == nil {
			t.Fatal("expected non-nil hints")
		}
		foundErrorWarning := false
		for _, w := range got.Warnings {
			if w == "one or more modes reported errors; review pane output" {
				foundErrorWarning = true
			}
		}
		if !foundErrorWarning {
			t.Errorf("expected error warning, got warnings: %v", got.Warnings)
		}

		foundReview := false
		for _, a := range got.SuggestedActions {
			if a.Action == "review" {
				foundReview = true
			}
			if a.Action == "wait" {
				t.Fatalf("did not expect 'wait' action when only errors remain: %+v", got.SuggestedActions)
			}
		}
		if !foundReview {
			t.Errorf("expected review action, got actions: %+v", got.SuggestedActions)
		}
	})

	t.Run("summary format", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary:  EnsembleSummary{TotalModes: 4, Completed: 2, Working: 1, Pending: 1},
			Ensemble: EnsembleState{Modes: []EnsembleMode{}, Synthesis: EnsembleSynthesis{}},
		}
		got := buildEnsembleHints(output)
		if got == nil {
			t.Fatal("expected non-nil hints")
		}
		want := "2/4 modes complete, 1 working, 1 pending"
		if got.Summary != want {
			t.Errorf("Summary = %q, want %q", got.Summary, want)
		}
	})

	t.Run("summary format includes errors", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary:  EnsembleSummary{TotalModes: 3, Completed: 1, Working: 0, Pending: 0, Errors: 2},
			Ensemble: EnsembleState{Modes: []EnsembleMode{}, Synthesis: EnsembleSynthesis{Status: "error"}},
		}
		got := buildEnsembleHints(output)
		if got == nil {
			t.Fatal("expected non-nil hints")
		}
		want := "1/3 modes complete, 0 working, 0 pending, 2 errors"
		if got.Summary != want {
			t.Errorf("Summary = %q, want %q", got.Summary, want)
		}
	})
}

func TestConvertPTState(t *testing.T) {
	t.Parallel()

	t.Run("nil state returns nil", func(t *testing.T) {
		t.Parallel()
		got := convertPTState(nil, false)
		if got != nil {
			t.Errorf("expected nil for nil state, got %+v", got)
		}
	})

	t.Run("basic state conversion", func(t *testing.T) {
		t.Parallel()
		since := time.Now().Add(-30 * time.Second)
		state := &pt.AgentState{
			Pane:           "test__cc_1",
			Classification: pt.ClassUseful,
			Confidence:     0.95,
			Since:          since,
		}
		got := convertPTState(state, true)
		if got == nil {
			t.Fatal("expected non-nil result")
		}
		if got.Classification != "useful" {
			t.Errorf("Classification = %q, want %q", got.Classification, "useful")
		}
		if got.Confidence != 0.95 {
			t.Errorf("Confidence = %f, want 0.95", got.Confidence)
		}
		if got.DurationSeconds < 29 {
			t.Errorf("DurationSeconds = %d, want >= 29", got.DurationSeconds)
		}
		if got.Since == "" {
			t.Error("expected Since to be non-empty")
		}
	})

	t.Run("zero since omits since field", func(t *testing.T) {
		t.Parallel()
		state := &pt.AgentState{
			Classification: pt.ClassIdle,
			Confidence:     0.5,
		}
		got := convertPTState(state, false)
		if got == nil {
			t.Fatal("expected non-nil result")
		}
		if got.Since != "" {
			t.Errorf("expected empty Since for zero time, got %q", got.Since)
		}
	})

	t.Run("with history populates signals", func(t *testing.T) {
		t.Parallel()
		since := time.Now().Add(-10 * time.Second)
		state := &pt.AgentState{
			Pane:           "test__cc_1",
			Classification: pt.ClassWaiting,
			Confidence:     0.8,
			Since:          since,
			History: []pt.ClassificationEvent{
				{
					Classification: pt.ClassWaiting,
					Confidence:     0.8,
					Timestamp:      time.Now(),
					Reason:         "waiting for API",
					NetworkActive:  true,
				},
			},
		}
		got := convertPTState(state, true)
		if got == nil {
			t.Fatal("expected non-nil result")
		}
		if got.Reason != "waiting for API" {
			t.Errorf("Reason = %q, want %q", got.Reason, "waiting for API")
		}
		if got.Signals == nil {
			t.Fatal("expected Signals to be non-nil with history")
		}
		if !got.Signals.NetworkActive {
			t.Error("expected NetworkActive = true")
		}
		if !got.Signals.OutputRecent {
			t.Error("expected OutputRecent = true (isWorking=true)")
		}
	})

	t.Run("isWorking false sets output recent false", func(t *testing.T) {
		t.Parallel()
		state := &pt.AgentState{
			Classification: pt.ClassStuck,
			Confidence:     0.9,
			Since:          time.Now(),
			History: []pt.ClassificationEvent{
				{Classification: pt.ClassStuck, Reason: "stuck"},
			},
		}
		got := convertPTState(state, false)
		if got == nil {
			t.Fatal("expected non-nil result")
		}
		if got.Signals == nil {
			t.Fatal("expected signals")
		}
		if got.Signals.OutputRecent {
			t.Error("expected OutputRecent = false when isWorking=false")
		}
	})
}

func TestUpdatePTSummary(t *testing.T) {
	t.Parallel()

	t.Run("nil summary is safe", func(t *testing.T) {
		t.Parallel()
		// Should not panic
		updatePTSummary(nil, pt.ClassUseful)
	})

	tests := []struct {
		name           string
		classification pt.Classification
		checkField     string
	}{
		{"useful", pt.ClassUseful, "Useful"},
		{"waiting", pt.ClassWaiting, "Waiting"},
		{"idle", pt.ClassIdle, "Idle"},
		{"stuck", pt.ClassStuck, "Stuck"},
		{"zombie", pt.ClassZombie, "Zombie"},
		{"unknown", pt.ClassUnknown, "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			summary := &PTHealthSummary{}
			updatePTSummary(summary, tt.classification)

			got := map[string]int{
				"Useful":  summary.Useful,
				"Waiting": summary.Waiting,
				"Idle":    summary.Idle,
				"Stuck":   summary.Stuck,
				"Zombie":  summary.Zombie,
				"Unknown": summary.Unknown,
			}
			if got[tt.checkField] != 1 {
				t.Errorf("expected %s = 1, got %d", tt.checkField, got[tt.checkField])
			}
			// Verify all other fields are still 0
			for field, val := range got {
				if field != tt.checkField && val != 0 {
					t.Errorf("expected %s = 0, got %d", field, val)
				}
			}
		})
	}
}

func TestUpdatePTSummary_Accumulates(t *testing.T) {
	t.Parallel()
	summary := &PTHealthSummary{}
	updatePTSummary(summary, pt.ClassUseful)
	updatePTSummary(summary, pt.ClassUseful)
	updatePTSummary(summary, pt.ClassStuck)
	if summary.Useful != 2 {
		t.Errorf("Useful = %d, want 2", summary.Useful)
	}
	if summary.Stuck != 1 {
		t.Errorf("Stuck = %d, want 1", summary.Stuck)
	}
}

func TestUpdatePTSummary_UnrecognizedClassification(t *testing.T) {
	t.Parallel()
	summary := &PTHealthSummary{}
	updatePTSummary(summary, pt.Classification("bogus"))
	if summary.Unknown != 1 {
		t.Errorf("Unknown = %d, want 1 for unrecognized classification", summary.Unknown)
	}
}
