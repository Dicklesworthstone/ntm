package ensemble

import "testing"

func TestOutputCapture_CaptureAll_NilCapture(t *testing.T) {
	var capture *OutputCapture
	_, err := capture.CaptureAll(&EnsembleSession{})
	if err == nil {
		t.Error("expected error for nil capture")
	}
}

func TestOutputCapture_CaptureAll_NilSession(t *testing.T) {
	capture := NewOutputCapture(nil)
	_, err := capture.CaptureAll(nil)
	if err == nil {
		t.Error("expected error for nil session")
	}
}

func TestOutputCapture_ExtractYAML_CodeBlock(t *testing.T) {
	capture := NewOutputCapture(nil)

	tests := []struct {
		name     string
		input    string
		wantYAML bool
		contains string
	}{
		{
			name:     "yaml code block",
			input:    "Some text\n```yaml\nthesis: Test thesis\nconfidence: 0.8\n```\nMore text",
			wantYAML: true,
			contains: "thesis: Test thesis",
		},
		{
			name:     "thesis line fallback",
			input:    "Some output\nthesis: Found thesis here\nconfidence: 0.9",
			wantYAML: true,
			contains: "thesis: Found thesis here",
		},
		{
			name:     "no yaml content",
			input:    "Just plain text without any YAML",
			wantYAML: false,
			contains: "",
		},
		{
			name:     "empty input",
			input:    "",
			wantYAML: false,
			contains: "",
		},
		{
			name:     "multiple yaml blocks picks valid",
			input:    "```yaml\ninvalid: [broken\n```\n```yaml\nthesis: Valid block\nconfidence: 0.5\n```",
			wantYAML: true,
			contains: "thesis: Valid block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml, found := capture.extractYAML(tt.input)
			if found != tt.wantYAML {
				t.Errorf("extractYAML found = %v, want %v", found, tt.wantYAML)
			}
			if tt.contains != "" && !captureContains(yaml, tt.contains) {
				t.Errorf("extractYAML yaml = %q, want contains %q", yaml, tt.contains)
			}
		})
	}
}

func TestOutputCapture_CapturePane_EmptyPane(t *testing.T) {
	capture := NewOutputCapture(nil)
	_, err := capture.capturePane("")
	if err == nil {
		t.Error("expected error for empty pane")
	}
}

func TestCapturedOutput_Fields(t *testing.T) {
	captured := CapturedOutput{
		ModeID:        "test-mode",
		PaneName:      "test-pane",
		RawOutput:     "test output",
		LineCount:     5,
		TokenEstimate: 100,
	}

	if captured.ModeID != "test-mode" {
		t.Errorf("ModeID = %q, want test-mode", captured.ModeID)
	}
	if captured.PaneName != "test-pane" {
		t.Errorf("PaneName = %q, want test-pane", captured.PaneName)
	}
	if captured.LineCount != 5 {
		t.Errorf("LineCount = %d, want 5", captured.LineCount)
	}
	if captured.TokenEstimate != 100 {
		t.Errorf("TokenEstimate = %d, want 100", captured.TokenEstimate)
	}
}

func TestCountLines_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"a\nb\nc", 3},
		{"\n", 1},   // Single newline = one empty line
		{"\n\n", 2}, // Two newlines = two empty lines
	}

	for _, tt := range tests {
		got := countLines(tt.input)
		if got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestEstimateModeOutputTokens_Coverage(t *testing.T) {
	tests := []struct {
		name   string
		output *ModeOutput
		wantGt int // want greater than
	}{
		{
			name:   "nil output",
			output: nil,
			wantGt: -1, // 0
		},
		{
			name: "with raw output",
			output: &ModeOutput{
				RawOutput: "This is raw output text for token estimation",
			},
			wantGt: 0,
		},
		{
			name: "with thesis only",
			output: &ModeOutput{
				Thesis: "This is a thesis statement",
			},
			wantGt: 0,
		},
		{
			name: "with findings",
			output: &ModeOutput{
				Thesis: "Thesis",
				TopFindings: []Finding{
					{Finding: "Finding one with details"},
					{Finding: "Finding two with more details"},
				},
			},
			wantGt: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateModeOutputTokens(tt.output)
			if got <= tt.wantGt {
				t.Errorf("EstimateModeOutputTokens() = %d, want > %d", got, tt.wantGt)
			}
		})
	}
}

// captureContains is a helper to check substring
func captureContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestOutputCapture_DefaultsAndLineCount(t *testing.T) {
	capture := &OutputCapture{}
	capture.ensureDefaults()
	if capture.tmuxClient == nil {
		t.Fatal("expected tmux client to be set")
	}
	if capture.validator == nil {
		t.Fatal("expected validator to be set")
	}
	if capture.maxLines != defaultCaptureLines {
		t.Fatalf("maxLines = %d, want %d", capture.maxLines, defaultCaptureLines)
	}

	if countLines("") != 0 {
		t.Fatal("countLines should be 0 for empty string")
	}
	if countLines("a\n") != 1 {
		t.Fatal("countLines should ignore trailing newline")
	}
	if countLines("a\nb") != 2 {
		t.Fatal("countLines should count lines")
	}
}
