package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

func TestBuildEnsembleAssignmentsCounts(t *testing.T) {
	state := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "m1", AgentType: "cc", Status: ensemble.AssignmentPending},
			{ModeID: "m2", AgentType: "cod", Status: ensemble.AssignmentInjecting},
			{ModeID: "m3", AgentType: "gmi", Status: ensemble.AssignmentActive},
			{ModeID: "m4", AgentType: "cc", Status: ensemble.AssignmentDone},
			{ModeID: "m5", AgentType: "cod", Status: ensemble.AssignmentError},
		},
	}

	rows, counts := buildEnsembleAssignments(state, nil, 1234)

	if len(rows) != 5 {
		t.Fatalf("expected 5 assignments, got %d", len(rows))
	}
	if counts.Pending != 2 {
		t.Errorf("pending count = %d, want 2", counts.Pending)
	}
	if counts.Working != 1 {
		t.Errorf("working count = %d, want 1", counts.Working)
	}
	if counts.Done != 1 {
		t.Errorf("done count = %d, want 1", counts.Done)
	}
	if counts.Error != 1 {
		t.Errorf("error count = %d, want 1", counts.Error)
	}
	if rows[0].TokenEstimate != 1234 {
		t.Errorf("token estimate = %d, want 1234", rows[0].TokenEstimate)
	}
}

func TestMergeBudgetDefaults(t *testing.T) {
	defaults := ensemble.BudgetConfig{
		MaxTokensPerMode: 4000,
		MaxTotalTokens:   50000,
		TimeoutPerMode:   5 * time.Minute,
		TotalTimeout:     30 * time.Minute,
		MaxRetries:       2,
	}

	current := ensemble.BudgetConfig{
		MaxTotalTokens: 12345,
	}

	merged := mergeBudgetDefaults(current, defaults)

	if merged.MaxTokensPerMode != defaults.MaxTokensPerMode {
		t.Errorf("MaxTokensPerMode = %d, want %d", merged.MaxTokensPerMode, defaults.MaxTokensPerMode)
	}
	if merged.MaxTotalTokens != current.MaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", merged.MaxTotalTokens, current.MaxTotalTokens)
	}
	if merged.TimeoutPerMode != defaults.TimeoutPerMode {
		t.Errorf("TimeoutPerMode = %s, want %s", merged.TimeoutPerMode, defaults.TimeoutPerMode)
	}
	if merged.TotalTimeout != defaults.TotalTimeout {
		t.Errorf("TotalTimeout = %s, want %s", merged.TotalTimeout, defaults.TotalTimeout)
	}
	if merged.MaxRetries != defaults.MaxRetries {
		t.Errorf("MaxRetries = %d, want %d", merged.MaxRetries, defaults.MaxRetries)
	}
}

func TestRenderEnsembleStatusNoSession(t *testing.T) {
	var buf bytes.Buffer
	err := renderEnsembleStatus(&buf, ensembleStatusOutput{
		Session: "demo",
		Exists:  false,
	}, "table")
	if err != nil {
		t.Fatalf("renderEnsembleStatus error: %v", err)
	}
	if !strings.Contains(buf.String(), "No ensemble running") {
		t.Errorf("expected no-ensemble message, got %q", buf.String())
	}
}

func TestImpactToBeadPriority(t *testing.T) {
	tests := []struct {
		name   string
		impact ensemble.ImpactLevel
		want   int
	}{
		{"critical", ensemble.ImpactCritical, 0},
		{"high", ensemble.ImpactHigh, 1},
		{"medium", ensemble.ImpactMedium, 2},
		{"low", ensemble.ImpactLow, 3},
		{"unknown", ensemble.ImpactLevel("unknown"), 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := impactToBeadPriority(tt.impact); got != tt.want {
				t.Errorf("impactToBeadPriority(%s) = %d, want %d", tt.impact, got, tt.want)
			}
		})
	}
}

func TestNormalizeEnsembleAgentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"cc", "cc"},
		{"claude", "cc"},
		{"claude-code", "cc"},
		{"claude_code", "cc"},
		{"cod", "cod"},
		{"codex", "cod"},
		{"codex-cli", "cod"},
		{"openai-codex", "cod"},
		{"gmi", "gmi"},
		{"gemini", "gmi"},
		{"gemini_cli", "gmi"},
		{"google-gemini", "gmi"},
		{"cursor", "cursor"},
		{"ws", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
		{"unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := normalizeEnsembleAgentType(tt.input); got != tt.want {
				t.Fatalf("normalizeEnsembleAgentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRunBrCreateParsesID(t *testing.T) {
	prev := runBrCommand
	t.Cleanup(func() { runBrCommand = prev })

	called := false
	runBrCommand = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		called = true
		if dir == "" {
			t.Fatalf("expected dir to be set")
		}
		if len(args) == 0 || args[0] != "create" {
			t.Fatalf("expected br create args, got %v", args)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--title") || !strings.Contains(joined, "--priority") || !strings.Contains(joined, "--description") {
			t.Fatalf("missing expected args: %v", args)
		}
		return []byte(`[{"id":"bd-123"}]`), nil
	}

	spec := beadSpec{
		Title:       "Test finding",
		Type:        "task",
		Priority:    1,
		Description: "Body",
	}

	id, err := runBrCreate(context.Background(), t.TempDir(), spec)
	if err != nil {
		t.Fatalf("runBrCreate error: %v", err)
	}
	if id != "bd-123" {
		t.Fatalf("runBrCreate id = %q, want bd-123", id)
	}
	if !called {
		t.Fatalf("expected runBrCommand to be called")
	}
}

func TestApplyResumeIndex(t *testing.T) {
	tests := []struct {
		name        string
		resumeIndex int
		chunkIndex  int
		wantOK      bool
		wantIndex   int
	}{
		{name: "no resume", resumeIndex: 0, chunkIndex: 1, wantOK: true, wantIndex: 1},
		{name: "skip earlier", resumeIndex: 2, chunkIndex: 1, wantOK: false, wantIndex: 1},
		{name: "skip equal", resumeIndex: 2, chunkIndex: 2, wantOK: false, wantIndex: 2},
		{name: "emit after", resumeIndex: 2, chunkIndex: 3, wantOK: true, wantIndex: 3},
		{name: "negative resume", resumeIndex: -1, chunkIndex: 4, wantOK: true, wantIndex: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunk, ok := applyResumeIndex(ensemble.SynthesisChunk{Index: tt.chunkIndex}, tt.resumeIndex)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if chunk.Index != tt.wantIndex {
				t.Fatalf("index = %d, want %d", chunk.Index, tt.wantIndex)
			}
		})
	}
}

func TestFormatBeadDescriptionIncludesProvenance(t *testing.T) {
	ctx := &exportFindingsContext{
		Question: "What are the security risks?",
		Session:  "test-session",
		RunID:    "run-abc123",
	}

	f := exportFinding{
		ID: "finding-01",
		Finding: ensemble.Finding{
			Finding:         "SQL injection vulnerability detected",
			Impact:          ensemble.ImpactCritical,
			Confidence:      0.95,
			EvidencePointer: "api/handler.go:42",
			Reasoning:       "Unsanitized user input passed to query builder",
		},
		SourceModes: []string{"mode-a", "mode-b"},
		Provenance: &ensemble.ProvenanceChain{
			FindingID:    "prov-001",
			SourceMode:   "mode-a",
			ContextHash:  "abc123def456",
			OriginalText: "SQL injection found",
			Steps: []ensemble.ProvenanceStep{
				{
					Stage:     "discovery",
					Action:    "mode-output",
					Timestamp: time.Now(),
					Details:   "Found by security mode",
				},
			},
		},
	}

	desc := formatBeadDescription(ctx, f)

	// Check that all required sections are present
	requiredSections := []string{
		"## Finding",
		"SQL injection vulnerability detected",
		"## Details",
		"**Impact:** critical",
		"**Confidence:** 95%",
		"**Evidence:** `api/handler.go:42`",
		"**Reasoning:**",
		"**Source Modes:** mode-a, mode-b",
		"**Question:** What are the security risks?",
		"**Session:** test-session",
		"**Run ID:** run-abc123",
		"## Provenance",
		"**Provenance ID:** `prov-001`",
		"**Source Mode:** mode-a",
		"**Context Hash:** `abc123def456`",
		"**Lifecycle:**",
		"discovery/mode-output",
	}

	for _, section := range requiredSections {
		if !strings.Contains(desc, section) {
			t.Errorf("formatBeadDescription missing required section: %q", section)
		}
	}
}

func TestFormatBeadDescriptionMinimal(t *testing.T) {
	ctx := &exportFindingsContext{}

	f := exportFinding{
		ID: "finding-01",
		Finding: ensemble.Finding{
			Finding:    "Simple finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.7,
		},
		SourceModes: nil,
		Provenance:  nil,
	}

	desc := formatBeadDescription(ctx, f)

	// Should still have basic structure
	if !strings.Contains(desc, "## Finding") {
		t.Error("missing Finding section")
	}
	if !strings.Contains(desc, "Simple finding") {
		t.Error("missing finding text")
	}
	if !strings.Contains(desc, "**Impact:** medium") {
		t.Error("missing impact")
	}

	// Should NOT have provenance section when nil
	if strings.Contains(desc, "## Provenance") {
		t.Error("should not have Provenance section when provenance is nil")
	}
}

func TestParseBrCreateID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantID  string
		wantErr bool
	}{
		{
			name:   "array format",
			input:  `[{"id":"bd-abc123"}]`,
			wantID: "bd-abc123",
		},
		{
			name:   "object format",
			input:  `{"id":"bd-xyz789"}`,
			wantID: "bd-xyz789",
		},
		{
			name:   "array with extra fields",
			input:  `[{"id":"bd-test","title":"Test","status":"open"}]`,
			wantID: "bd-test",
		},
		{
			name:    "empty array",
			input:   `[]`,
			wantErr: true,
		},
		{
			name:    "array with empty id",
			input:   `[{"id":""}]`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := parseBrCreateID([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got id=%q", id)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if id != tt.wantID {
				t.Errorf("parseBrCreateID = %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestSelectFindingsByIDs(t *testing.T) {
	findings := []exportFinding{
		{ID: "finding-01", Finding: ensemble.Finding{Finding: "First"}},
		{ID: "finding-02", Finding: ensemble.Finding{Finding: "Second"}},
		{ID: "finding-03", Finding: ensemble.Finding{Finding: "Third"}},
	}

	tests := []struct {
		name    string
		ids     []string
		wantLen int
		wantErr bool
	}{
		{
			name:    "single valid id",
			ids:     []string{"finding-01"},
			wantLen: 1,
		},
		{
			name:    "multiple valid ids",
			ids:     []string{"finding-01", "finding-03"},
			wantLen: 2,
		},
		{
			name:    "invalid id",
			ids:     []string{"finding-99"},
			wantErr: true,
		},
		{
			name:    "mixed valid and invalid",
			ids:     []string{"finding-01", "finding-99"},
			wantErr: true,
		},
		{
			name:    "empty ids",
			ids:     []string{},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectFindingsByIDs(findings, tt.ids)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %d findings", len(selected))
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if len(selected) != tt.wantLen {
				t.Errorf("selectFindingsByIDs returned %d findings, want %d", len(selected), tt.wantLen)
			}
		})
	}
}

func TestParseSelectionIndices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		max     int
		want    []int
		wantErr bool
	}{
		{"single value", "1", 5, []int{1}, false},
		{"comma separated", "1,3,5", 5, []int{1, 3, 5}, false},
		{"range", "2-4", 5, []int{2, 3, 4}, false},
		{"all keyword", "all", 3, []int{1, 2, 3}, false},
		{"ALL uppercase", "ALL", 3, []int{1, 2, 3}, false},
		{"mixed", "1,3-5", 5, []int{1, 3, 4, 5}, false},
		{"reversed range", "4-2", 5, []int{2, 3, 4}, false},
		{"with spaces", " 1 , 3 ", 5, []int{1, 3}, false},
		{"deduplicates", "1,1,2", 5, []int{1, 2}, false},
		{"empty string", "", 5, nil, true},
		{"out of range high", "6", 5, nil, true},
		{"out of range zero", "0", 5, nil, true},
		{"invalid text", "abc", 5, nil, true},
		{"invalid range start", "a-3", 5, nil, true},
		{"invalid range end", "1-b", 5, nil, true},
		{"range out of bounds", "1-10", 5, nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseSelectionIndices(tc.input, tc.max)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseSelectionIndices(%q, %d) = %v, want error", tc.input, tc.max, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSelectionIndices(%q, %d) error: %v", tc.input, tc.max, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseSelectionIndices(%q, %d) = %v (len %d), want %v (len %d)",
					tc.input, tc.max, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseSelectionIndices(%q, %d)[%d] = %d, want %d",
						tc.input, tc.max, i, got[i], tc.want[i])
				}
			}
		})
	}
}
