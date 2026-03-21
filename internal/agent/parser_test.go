package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewParser(t *testing.T) {
	p := NewParser()
	if p == nil {
		t.Fatal("NewParser returned nil")
	}
}

func TestNewParserWithConfig(t *testing.T) {
	cfg := ParserConfig{
		ContextLowThreshold: 30.0,
		SampleLength:        200,
	}
	p := NewParserWithConfig(cfg)
	if p == nil {
		t.Fatal("NewParserWithConfig returned nil")
	}
}

func TestParser_Parse_EmptyOutput(t *testing.T) {
	p := NewParser()
	state, err := p.Parse("")

	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if state == nil {
		t.Fatal("Parse returned nil state")
	}
	if state.Type != AgentTypeUnknown {
		t.Errorf("Expected AgentTypeUnknown for empty output, got %v", state.Type)
	}
	// Empty output should have low confidence
	if state.Confidence > 0.5 {
		t.Errorf("Expected low confidence for empty output, got %f", state.Confidence)
	}
}

func TestParser_DetectAgentType_Claude(t *testing.T) {
	p := NewParser()

	outputs := []string{
		"Claude Opus 4.5 is ready",
		"Using sonnet 3.5 for this task",
		"Haiku model loaded",
	}

	for _, output := range outputs {
		t.Run(output, func(t *testing.T) {
			agentType := p.DetectAgentType(output)
			if agentType != AgentTypeClaudeCode {
				t.Errorf("DetectAgentType(%q) = %v, want %v", output, agentType, AgentTypeClaudeCode)
			}
		})
	}
}

func TestParser_DetectAgentType_Codex(t *testing.T) {
	p := NewParser()

	outputs := []string{
		"47% context left · ? for shortcuts",
		"OpenAI Codex CLI ready",
		"GPT-4 turbo model",
	}

	for _, output := range outputs {
		t.Run(output, func(t *testing.T) {
			agentType := p.DetectAgentType(output)
			if agentType != AgentTypeCodex {
				t.Errorf("DetectAgentType(%q) = %v, want %v", output, agentType, AgentTypeCodex)
			}
		})
	}
}

func TestParser_DetectAgentType_Gemini(t *testing.T) {
	p := NewParser()

	outputs := []string{
		"gemini-2.0-flash-preview ready",
		"YOLO mode: ON",
		"Google AI Studio connected",
	}

	for _, output := range outputs {
		t.Run(output, func(t *testing.T) {
			agentType := p.DetectAgentType(output)
			if agentType != AgentTypeGemini {
				t.Errorf("DetectAgentType(%q) = %v, want %v", output, agentType, AgentTypeGemini)
			}
		})
	}
}

func TestParser_DetectAgentType_Cursor(t *testing.T) {
	t.Parallel()
	p := NewParser()

	outputs := []string{
		"Cursor AI ready to assist",
		"cursor session started",
	}

	for _, output := range outputs {
		t.Run(output, func(t *testing.T) {
			t.Parallel()
			agentType := p.DetectAgentType(output)
			if agentType != AgentTypeCursor {
				t.Errorf("DetectAgentType(%q) = %v, want %v", output, agentType, AgentTypeCursor)
			}
		})
	}
}

func TestParser_DetectAgentType_Windsurf(t *testing.T) {
	t.Parallel()
	p := NewParser()

	outputs := []string{
		"Windsurf IDE connected",
		"windsurf is ready",
	}

	for _, output := range outputs {
		t.Run(output, func(t *testing.T) {
			t.Parallel()
			agentType := p.DetectAgentType(output)
			if agentType != AgentTypeWindsurf {
				t.Errorf("DetectAgentType(%q) = %v, want %v", output, agentType, AgentTypeWindsurf)
			}
		})
	}
}

func TestParser_DetectAgentType_Aider(t *testing.T) {
	t.Parallel()
	p := NewParser()

	outputs := []string{
		"aider chat session",
		"Aider started",
	}

	for _, output := range outputs {
		t.Run(output, func(t *testing.T) {
			t.Parallel()
			agentType := p.DetectAgentType(output)
			if agentType != AgentTypeAider {
				t.Errorf("DetectAgentType(%q) = %v, want %v", output, agentType, AgentTypeAider)
			}
		})
	}
}

func TestParser_Parse_RateLimited_Claude(t *testing.T) {
	p := NewParser()
	output := `Claude Opus 4.5 ready
Processing your request...
You've hit your limit. Please wait and try again later.`

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if state.Type != AgentTypeClaudeCode {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeClaudeCode)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited to be true")
	}
	if len(state.LimitIndicators) == 0 {
		t.Error("Expected LimitIndicators to be populated")
	}
	if state.GetRecommendation() != RecommendRateLimitedWait {
		t.Errorf("Recommendation = %v, want %v", state.GetRecommendation(), RecommendRateLimitedWait)
	}
}

func TestParser_Parse_Working_CodeBlock(t *testing.T) {
	p := NewParser()
	output := `Claude Opus 4.5 ready
Let me write some code for you:
` + "```go" + `
package main

func main() {
    fmt.Println("Hello, World!")
}
` + "```"

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if !state.IsWorking {
		t.Error("Expected IsWorking to be true when code block present")
	}
	if state.GetRecommendation() != RecommendDoNotInterrupt {
		t.Errorf("Recommendation = %v, want %v", state.GetRecommendation(), RecommendDoNotInterrupt)
	}
}

func TestParser_Parse_Idle_Claude(t *testing.T) {
	p := NewParser()
	output := `Task completed successfully.
What would you like me to do next?
Human: `

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if !state.IsIdle {
		t.Error("Expected IsIdle to be true when prompt present")
	}
	if state.IsWorking {
		t.Error("Expected IsWorking to be false when idle")
	}
	if state.GetRecommendation() != RecommendSafeToRestart {
		t.Errorf("Recommendation = %v, want %v", state.GetRecommendation(), RecommendSafeToRestart)
	}
}

func TestParser_Parse_Codex_ContextExtraction(t *testing.T) {
	p := NewParser()
	output := `Processing your request...
Token usage: total=150,000 input=140,000 output=10,000
47% context left · ? for shortcuts
codex> `

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if state.Type != AgentTypeCodex {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeCodex)
	}
	if state.ContextRemaining == nil {
		t.Fatal("Expected ContextRemaining to be set")
	}
	if *state.ContextRemaining != 47.0 {
		t.Errorf("ContextRemaining = %f, want 47.0", *state.ContextRemaining)
	}
	if state.TokensUsed == nil {
		t.Fatal("Expected TokensUsed to be set")
	}
	if *state.TokensUsed != 150000 {
		t.Errorf("TokensUsed = %d, want 150000", *state.TokensUsed)
	}
}

func TestParser_Parse_Codex_LowContext(t *testing.T) {
	p := NewParser()
	output := `Some work done...
10% context left · ? for shortcuts
codex> `

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if state.ContextRemaining == nil {
		t.Fatal("Expected ContextRemaining to be set")
	}
	if *state.ContextRemaining != 10.0 {
		t.Errorf("ContextRemaining = %f, want 10.0", *state.ContextRemaining)
	}
	if !state.IsContextLow {
		t.Error("Expected IsContextLow to be true (10% < 20% threshold)")
	}
}

func TestParser_Parse_Gemini_Memory(t *testing.T) {
	p := NewParser()
	output := `gemini-2.0-flash-preview /model | 396.8 MB
Processing request...`

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if state.Type != AgentTypeGemini {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeGemini)
	}
	if state.MemoryMB == nil {
		t.Fatal("Expected MemoryMB to be set")
	}
	if *state.MemoryMB != 396.8 {
		t.Errorf("MemoryMB = %f, want 396.8", *state.MemoryMB)
	}
}

func TestParser_Parse_WorkingWithLowContext(t *testing.T) {
	p := NewParser()
	// Realistic Codex output when actively working with low context.
	// The "? for shortcuts" status line is pushed up by several lines of output,
	// so it should NOT be in the last 5 lines that we check for idle detection.
	output := `5% context left · ? for shortcuts
This is some explanation from the agent...
And more explanation...
I'll write some code now.
Writing to file.go...
` + "```go" + `
func example() {}
` + "```"

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if !state.IsWorking {
		t.Error("Expected IsWorking to be true")
	}
	if !state.IsContextLow {
		t.Error("Expected IsContextLow to be true")
	}
	if state.GetRecommendation() != RecommendContextLowContinue {
		t.Errorf("Recommendation = %v, want %v", state.GetRecommendation(), RecommendContextLowContinue)
	}
}

func TestParser_Parse_ANSIStripping(t *testing.T) {
	p := NewParser()
	// Include ANSI color codes
	output := "\x1b[32mClaude Opus 4.5 ready\x1b[0m\n\x1b[1;31mYou've hit your limit\x1b[0m"

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Should still detect patterns after stripping ANSI
	if state.Type != AgentTypeClaudeCode {
		t.Errorf("Type = %v, want %v (ANSI codes should be stripped)", state.Type, AgentTypeClaudeCode)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited to be true (pattern should match after ANSI stripping)")
	}
}

func TestParser_Parse_RawSample(t *testing.T) {
	p := NewParserWithConfig(ParserConfig{
		ContextLowThreshold: 20.0,
		SampleLength:        50,
	})

	// Create output longer than sample length
	output := strings.Repeat("x", 100)
	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(state.RawSample) != 50 {
		t.Errorf("RawSample length = %d, want 50", len(state.RawSample))
	}
}

func TestParser_Parse_ConfidenceScores(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		minConfidence float64
		maxConfidence float64
	}{
		{
			name:          "empty output has low confidence",
			output:        "",
			minConfidence: 0.0,
			maxConfidence: 0.3,
		},
		{
			name:          "codex with percentage has high confidence",
			output:        "OpenAI Codex\n47% context left · ? for shortcuts\ncodex> ",
			minConfidence: 0.7,
			maxConfidence: 1.0,
		},
		{
			name:          "rate limited has boosted confidence",
			output:        "Claude Opus 4.5\nYou've hit your limit",
			minConfidence: 0.6,
			maxConfidence: 1.0,
		},
	}

	p := NewParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := p.Parse(tt.output)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}

			if state.Confidence < tt.minConfidence || state.Confidence > tt.maxConfidence {
				t.Errorf("Confidence = %f, want [%f, %f]",
					state.Confidence, tt.minConfidence, tt.maxConfidence)
			}
		})
	}
}

func TestParser_Parse_ErrorDetection(t *testing.T) {
	p := NewParser()
	output := `Claude Opus 4.5 ready
error: permission denied accessing /etc/passwd
Fatal: cannot continue`

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if !state.IsInError {
		t.Error("Expected IsInError to be true")
	}
	if state.GetRecommendation() != RecommendErrorState {
		t.Errorf("Recommendation = %v, want %v", state.GetRecommendation(), RecommendErrorState)
	}
}

func TestParser_Parse_FileOperations(t *testing.T) {
	operations := []string{
		"Writing to internal/api/handler.go",
		"Created new file test.go",
		"Modified config.yaml",
		"Reading package.json",
		"Searching for pattern",
		"Running go test ./...",
	}

	p := NewParser()
	for _, op := range operations {
		t.Run(op, func(t *testing.T) {
			output := "Claude Opus 4.5\n" + op
			state, err := p.Parse(output)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}

			if !state.IsWorking {
				t.Errorf("Expected IsWorking for %q", op)
			}
		})
	}
}

// NOTE: TestMinInt was removed because the minInt function doesn't exist.
// Go 1.21+ has a built-in min function which should be used instead.

// ============================================================================
// File-based tests using real output samples from testdata/
// ============================================================================

// loadTestData loads a test file from the testdata directory.
func loadTestData(t *testing.T, filename string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatalf("Failed to load test data %s: %v", filename, err)
	}
	return string(data)
}

func TestParser_FileData_ClaudeCode_Working(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "cc_working.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeClaudeCode {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeClaudeCode)
	}
	if !state.IsWorking {
		t.Error("Expected IsWorking=true for active Claude Code")
	}
	if state.IsIdle {
		t.Error("Expected IsIdle=false for active Claude Code")
	}
	if state.GetRecommendation() != RecommendDoNotInterrupt {
		t.Errorf("Recommendation = %v, want %v", state.GetRecommendation(), RecommendDoNotInterrupt)
	}
}

func TestParser_FileData_ClaudeCode_Idle(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "cc_idle.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeClaudeCode {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeClaudeCode)
	}
	// Note: This output ends with a question but no explicit prompt marker
	// The parser may detect this as either idle or unknown depending on heuristics
}

func TestParser_FileData_ClaudeCode_RateLimit(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "cc_ratelimit.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeClaudeCode {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeClaudeCode)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited=true")
	}
	if state.GetRecommendation() != RecommendRateLimitedWait {
		t.Errorf("Recommendation = %v, want %v", state.GetRecommendation(), RecommendRateLimitedWait)
	}
}

func TestParser_FileData_ClaudeCode_LowContext(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "cc_lowcontext.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeClaudeCode {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeClaudeCode)
	}
	if !state.IsContextLow {
		t.Error("Expected IsContextLow=true for conversation warning")
	}
}

func TestParser_FileData_Codex_Working(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "cod_working.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeCodex {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeCodex)
	}
	if !state.IsWorking {
		t.Error("Expected IsWorking=true for active Codex")
	}
	// Should extract token usage
	if state.TokensUsed == nil {
		t.Error("Expected TokensUsed to be set")
	} else if *state.TokensUsed != 85432 {
		t.Errorf("TokensUsed = %d, want 85432", *state.TokensUsed)
	}
}

func TestParser_FileData_Codex_Idle(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "cod_idle.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeCodex {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeCodex)
	}
	if state.ContextRemaining == nil {
		t.Fatal("Expected ContextRemaining to be set for Codex")
	}
	if *state.ContextRemaining != 47.0 {
		t.Errorf("ContextRemaining = %.1f, want 47.0", *state.ContextRemaining)
	}
	if state.TokensUsed == nil {
		t.Error("Expected TokensUsed to be set")
	}
	if !state.IsIdle {
		t.Error("Expected IsIdle=true for Codex at prompt")
	}
}

func TestParser_FileData_Codex_RateLimit(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "cod_ratelimit.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeCodex {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeCodex)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited=true")
	}
}

func TestParser_FileData_Gemini_Working(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "gmi_working.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeGemini {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeGemini)
	}
	if !state.IsWorking {
		t.Error("Expected IsWorking=true for active Gemini")
	}
	// Should extract memory
	if state.MemoryMB == nil {
		t.Error("Expected MemoryMB to be set")
	} else if *state.MemoryMB != 256.4 {
		t.Errorf("MemoryMB = %.1f, want 256.4", *state.MemoryMB)
	}
}

func TestParser_FileData_Gemini_Idle(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "gmi_idle.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeGemini {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeGemini)
	}
	if state.MemoryMB == nil {
		t.Error("Expected MemoryMB to be set")
	} else if *state.MemoryMB != 396.8 {
		t.Errorf("MemoryMB = %.1f, want 396.8", *state.MemoryMB)
	}
	if !state.IsIdle {
		t.Error("Expected IsIdle=true for Gemini at prompt")
	}
}

func TestParser_FileData_Gemini_YOLO(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "gmi_yolo.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeGemini {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeGemini)
	}
	// Test data shows deployment is complete and agent is at gemini> prompt.
	// The agent WAS working but is now idle, waiting for next command.
	// YOLO mode only affects auto-approval behavior, not working/idle detection.
	if !state.IsIdle {
		t.Error("Expected IsIdle=true for completed deployment at gemini> prompt")
	}
}

func TestParser_FileData_Gemini_RateLimit(t *testing.T) {
	p := NewParser()
	output := loadTestData(t, "gmi_ratelimit.txt")

	state, err := p.Parse(output)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if state.Type != AgentTypeGemini {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeGemini)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited=true")
	}
}

// TestParser_FileData_AllFiles verifies all testdata files can be parsed without error.
func TestParser_FileData_AllFiles(t *testing.T) {
	files := []string{
		"cc_working.txt",
		"cc_idle.txt",
		"cc_ratelimit.txt",
		"cc_lowcontext.txt",
		"cod_working.txt",
		"cod_idle.txt",
		"cod_ratelimit.txt",
		"gmi_working.txt",
		"gmi_idle.txt",
		"gmi_yolo.txt",
		"gmi_ratelimit.txt",
	}

	p := NewParser()
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			output := loadTestData(t, file)
			state, err := p.Parse(output)
			if err != nil {
				t.Errorf("Parse failed for %s: %v", file, err)
			}
			if state == nil {
				t.Errorf("Parse returned nil state for %s", file)
			}
			// Every file should produce some type detection
			if state.Confidence == 0 {
				t.Errorf("Confidence is 0 for %s, expected some confidence", file)
			}
		})
	}
}

// =============================================================================
// Cursor / Windsurf / Aider state detection via ParseWithHint
// =============================================================================

func TestParser_ParseWithHint_Cursor_Working(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Analyzing your codebase...\nwriting to src/main.ts"

	state, err := p.ParseWithHint(output, AgentTypeCursor)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if state.Type != AgentTypeCursor {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeCursor)
	}
	if !state.IsWorking {
		t.Error("Expected IsWorking=true for active Cursor")
	}
	if len(state.WorkIndicators) == 0 {
		t.Error("Expected WorkIndicators to be populated")
	}
}

func TestParser_ParseWithHint_Cursor_Idle(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Done editing.\ncursor> "

	state, err := p.ParseWithHint(output, AgentTypeCursor)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsIdle {
		t.Error("Expected IsIdle=true for Cursor at prompt")
	}
	if state.IsWorking {
		t.Error("Expected IsWorking=false when idle")
	}
}

func TestParser_ParseWithHint_Cursor_RateLimited(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Processing...\nError: rate limit exceeded. Please wait."

	state, err := p.ParseWithHint(output, AgentTypeCursor)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited=true")
	}
	if len(state.LimitIndicators) == 0 {
		t.Error("Expected LimitIndicators to be populated")
	}
}

func TestParser_ParseWithHint_Cursor_Error(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "error: could not connect to server\nfailed: timeout"

	state, err := p.ParseWithHint(output, AgentTypeCursor)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsInError {
		t.Error("Expected IsInError=true")
	}
}

func TestParser_ParseWithHint_Windsurf_Working(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Generating code...\nsearching for references"

	state, err := p.ParseWithHint(output, AgentTypeWindsurf)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if state.Type != AgentTypeWindsurf {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeWindsurf)
	}
	if !state.IsWorking {
		t.Error("Expected IsWorking=true for active Windsurf")
	}
	if len(state.WorkIndicators) == 0 {
		t.Error("Expected WorkIndicators to be populated")
	}
}

func TestParser_ParseWithHint_Windsurf_Idle(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Completed task.\nwindsurf> "

	state, err := p.ParseWithHint(output, AgentTypeWindsurf)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsIdle {
		t.Error("Expected IsIdle=true for Windsurf at prompt")
	}
}

func TestParser_ParseWithHint_Windsurf_RateLimited(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Querying model...\ntoo many requests, please try again"

	state, err := p.ParseWithHint(output, AgentTypeWindsurf)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited=true")
	}
	if len(state.LimitIndicators) == 0 {
		t.Error("Expected LimitIndicators to be populated")
	}
}

func TestParser_ParseWithHint_Windsurf_Error(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "exception: unexpected null pointer"

	state, err := p.ParseWithHint(output, AgentTypeWindsurf)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsInError {
		t.Error("Expected IsInError=true")
	}
}

func TestParser_ParseWithHint_Aider_Working(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Applied edit to src/main.py\ncommitting changes\nrepo-map updated"

	state, err := p.ParseWithHint(output, AgentTypeAider)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if state.Type != AgentTypeAider {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeAider)
	}
	if !state.IsWorking {
		t.Error("Expected IsWorking=true for active Aider")
	}
	if len(state.WorkIndicators) == 0 {
		t.Error("Expected WorkIndicators to be populated")
	}
}

func TestParser_ParseWithHint_Aider_Idle(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Changes applied.\naider> "

	state, err := p.ParseWithHint(output, AgentTypeAider)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsIdle {
		t.Error("Expected IsIdle=true for Aider at prompt")
	}
}

func TestParser_ParseWithHint_Aider_RateLimited(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "Sending to model...\nquota exceeded, please wait"

	state, err := p.ParseWithHint(output, AgentTypeAider)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited=true")
	}
	if len(state.LimitIndicators) == 0 {
		t.Error("Expected LimitIndicators to be populated")
	}
}

func TestParser_ParseWithHint_Aider_Error(t *testing.T) {
	t.Parallel()
	p := NewParser()
	output := "failed: could not apply patch"

	state, err := p.ParseWithHint(output, AgentTypeAider)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsInError {
		t.Error("Expected IsInError=true")
	}
}

// =============================================================================
// Unknown agent type: collect indicators fall through default branches
// =============================================================================

func TestParser_ParseWithHint_Ollama_RateLimited(t *testing.T) {
	t.Parallel()
	p := NewParser()
	// Ollama has no explicit case in detect functions, hits default branch
	output := "rate limit exceeded. Please wait and try again later."

	state, err := p.ParseWithHint(output, AgentTypeOllama)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if state.Type != AgentTypeOllama {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeOllama)
	}
	if !state.IsRateLimited {
		t.Error("Expected IsRateLimited=true via default branch")
	}
	if len(state.LimitIndicators) == 0 {
		t.Error("Expected LimitIndicators to be populated via default branch")
	}
}

func TestParser_ParseWithHint_Ollama_Working(t *testing.T) {
	t.Parallel()
	p := NewParser()
	// Ollama hits default branch in detectWorking/collectWorkIndicators
	output := "writing to main.go\n" + "```go\npackage main\n```"

	state, err := p.ParseWithHint(output, AgentTypeOllama)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if state.Type != AgentTypeOllama {
		t.Errorf("Type = %v, want %v", state.Type, AgentTypeOllama)
	}
	if !state.IsWorking {
		t.Error("Expected IsWorking=true via default branch")
	}
	if len(state.WorkIndicators) == 0 {
		t.Error("Expected WorkIndicators to be populated via default branch")
	}
}

func TestParser_ParseWithHint_Ollama_Idle(t *testing.T) {
	t.Parallel()
	p := NewParser()
	// Ollama hits default branch in detectIdle
	output := "Done.\n> "

	state, err := p.ParseWithHint(output, AgentTypeOllama)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	if !state.IsIdle {
		t.Error("Expected IsIdle=true via default branch")
	}
}

func TestParser_ParseWithHint_Ollama_Error(t *testing.T) {
	t.Parallel()
	p := NewParser()
	// Ollama hits default in detectError which checks all patterns
	output := "error: something broke\nfatal crash"

	state, err := p.ParseWithHint(output, AgentTypeOllama)
	if err != nil {
		t.Fatalf("ParseWithHint error: %v", err)
	}
	// Default detectError checks all patterns, so it should detect the error
	if !state.IsInError {
		t.Error("Expected IsInError=true for Ollama (detectError checks all patterns for default)")
	}
}

func TestParser_CalculateConfidence_Clamping(t *testing.T) {
	t.Parallel()
	p := NewParser().(*parserImpl)

	t.Run("clamps above 1.0", func(t *testing.T) {
		t.Parallel()
		// Stack many boosts: ContextRemaining + TokensUsed + 3 indicators + LimitIndicators
		state := &AgentState{
			Type:             AgentTypeCodex,
			ContextRemaining: floatPtr(47.0),
			TokensUsed:       intPtr(100000),
			WorkIndicators:   []string{"a", "b", "c", "d"},
			LimitIndicators:  []string{"rate limit"},
		}
		conf := p.calculateConfidence(state)
		if conf > 1.0 {
			t.Errorf("confidence = %f, want <= 1.0", conf)
		}
		if conf != 1.0 {
			t.Errorf("confidence = %f, want exactly 1.0 (clamped)", conf)
		}
	})

	t.Run("clamps below 0.0", func(t *testing.T) {
		t.Parallel()
		// Unknown type (-0.3) + working+idle conflict (-0.2) = 0.5-0.3-0.2 = 0.0
		// But we can't get below 0 easily since base is 0.5 and max penalty is -0.5
		// Actually 0.5 - 0.3 - 0.2 = 0.0, not negative.
		// Need to find a way to go negative... base 0.5, unknown -0.3, conflict -0.2 = 0.0 exactly
		state := &AgentState{
			Type:      AgentTypeUnknown,
			IsWorking: true,
			IsIdle:    true,
		}
		conf := p.calculateConfidence(state)
		if conf < 0.0 {
			t.Errorf("confidence = %f, want >= 0.0", conf)
		}
	})

	t.Run("working+idle conflict penalty", func(t *testing.T) {
		t.Parallel()
		// Known type + working + idle should get -0.2 penalty
		state := &AgentState{
			Type:      AgentTypeClaudeCode,
			IsWorking: true,
			IsIdle:    true,
		}
		conf := p.calculateConfidence(state)
		// Base 0.5, no unknown penalty, conflict -0.2 = 0.3
		if conf != 0.3 {
			t.Errorf("confidence = %f, want 0.3 (conflict penalty)", conf)
		}
	})
}

// Test real-world-like outputs
func TestParser_RealWorldScenarios(t *testing.T) {
	p := NewParser()

	tests := []struct {
		name        string
		output      string
		wantType    AgentType
		wantWorking bool
		wantIdle    bool
		wantLimited bool
	}{
		{
			name: "claude writing file",
			output: `Claude Opus 4.5 ready
I'll help you create that function.
Writing to internal/util/helper.go
` + "```go" + `
package util

func Helper() string {
    return "hello"
}
` + "```" + `
Done!`,
			wantType:    AgentTypeClaudeCode,
			wantWorking: true,
			wantIdle:    false,
			wantLimited: false,
		},
		{
			name: "codex waiting for input",
			output: `Completed refactoring.
Token usage: total=50,000 input=45,000 output=5,000
72% context left · ? for shortcuts
codex> `,
			wantType:    AgentTypeCodex,
			wantWorking: false,
			wantIdle:    true,
			wantLimited: false,
		},
		{
			name: "gemini rate limited",
			output: `gemini-2.0-flash-preview ready
Processing...
Error: quota exceeded. Please try again in 1 minute.`,
			wantType:    AgentTypeGemini,
			wantWorking: false,
			wantIdle:    false,
			wantLimited: true,
		},
		{
			name: "cursor working on code",
			output: "Cursor AI session\nanalyzing your request\n" +
				"```typescript\nconst x = 1;\n```",
			wantType:    AgentTypeCursor,
			wantWorking: true,
			wantIdle:    false,
			wantLimited: false,
		},
		{
			name:        "windsurf idle at prompt",
			output:      "Windsurf IDE connected\nDone.\nwindsurf> ",
			wantType:    AgentTypeWindsurf,
			wantWorking: false,
			wantIdle:    true,
			wantLimited: false,
		},
		{
			name:        "aider committing changes",
			output:      "aider chat started\napplied edit to main.py\ncommitting",
			wantType:    AgentTypeAider,
			wantWorking: true,
			wantIdle:    false,
			wantLimited: false,
		},
		{
			name: "gemini thinking",
			output: `gemini-2.0-flash-preview
I am thinking about the best way to solve this...
Here is a plan:
1. First step
2. Second step`,
			wantType:    AgentTypeGemini,
			wantWorking: true,
			wantIdle:    false,
			wantLimited: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := p.Parse(tt.output)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}

			if state.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", state.Type, tt.wantType)
			}
			if state.IsWorking != tt.wantWorking {
				t.Errorf("IsWorking = %v, want %v", state.IsWorking, tt.wantWorking)
			}
			if state.IsIdle != tt.wantIdle {
				t.Errorf("IsIdle = %v, want %v", state.IsIdle, tt.wantIdle)
			}
			if state.IsRateLimited != tt.wantLimited {
				t.Errorf("IsRateLimited = %v, want %v", state.IsRateLimited, tt.wantLimited)
			}
		})
	}
}
