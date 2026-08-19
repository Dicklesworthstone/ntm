package status

import (
	"testing"
	"time"
)

func TestDetectCompaction_ClaudeExactMatch(t *testing.T) {
	// This is THE critical test - "Conversation compacted" is the exact Claude Code message
	tests := []struct {
		name      string
		output    string
		agentType string
		wantMatch bool
		wantText  string
	}{
		{
			name:      "exact Claude Code compaction message",
			output:    "Some output\nConversation compacted\nMore output",
			agentType: "claude",
			wantMatch: true,
			wantText:  "Conversation compacted",
		},
		{
			name:      "exact match with cc alias",
			output:    "Conversation compacted",
			agentType: "cc",
			wantMatch: true,
			wantText:  "Conversation compacted",
		},
		{
			name:      "session continuation message",
			output:    "This session is being continued from a previous conversation that ran out of context",
			agentType: "claude",
			wantMatch: true,
		},
		{
			name:      "no compaction - normal output",
			output:    "def hello():\n    print('hello world')\n",
			agentType: "claude",
			wantMatch: false,
		},
		{
			name:      "no compaction - empty output",
			output:    "",
			agentType: "claude",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := DetectCompaction(tt.output, tt.agentType)
			if tt.wantMatch {
				if event == nil {
					t.Errorf("expected compaction to be detected, got nil")
					return
				}
				if tt.wantText != "" && event.MatchedText != tt.wantText {
					t.Errorf("matched text = %q, want %q", event.MatchedText, tt.wantText)
				}
			} else {
				if event != nil {
					t.Errorf("expected no compaction, got: %+v", event)
				}
			}
		})
	}
}

func TestDetectCompaction_AllAgentTypes(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		agentType string
		wantMatch bool
	}{
		// Claude patterns
		{"claude context compacted", "The context was compacted", "claude", true},
		{"claude ran out of context", "I ran out of context", "claude", true},
		{"claude session continued", "session is being continued from a previous", "cc", true},

		// Codex patterns
		{"codex context limit", "context limit reached", "codex", true},
		{"codex truncated", "conversation truncated", "cod", true},

		// Gemini patterns
		{"gemini context window", "context window exceeded", "gemini", true},
		{"gemini reset", "conversation reset due to limits", "gmi", true},

		// Cross-agent shouldn't match
		{"claude pattern on codex", "Conversation compacted", "codex", false},
		{"codex pattern on claude", "context limit reached", "claude", false},

		// Generic patterns match any
		{"generic continuing summary - claude", "continuing from summary", "claude", true},
		{"generic continuing summary - codex", "continuing from summary", "codex", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := DetectCompaction(tt.output, tt.agentType)
			if tt.wantMatch && event == nil {
				t.Errorf("expected compaction to be detected")
			}
			if !tt.wantMatch && event != nil {
				t.Errorf("expected no compaction, got: %+v", event)
			}
		})
	}
}

func TestDetectCompaction_NormalizesAliases(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		agentType string
	}{
		{"claude dash code", "Conversation compacted", "claude-code"},
		{"codex cli", "context limit reached", "codex-cli"},
		{"openai codex alias", "conversation truncated", " openai-codex "},
		{"google gemini alias", "context window exceeded", "Google-Gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if event := DetectCompaction(tt.output, tt.agentType); event == nil {
				t.Fatalf("expected compaction for agent type %q", tt.agentType)
			}
		})
	}
}

func TestDetectCompaction_NoFalsePositives(t *testing.T) {
	// Common output that should NOT trigger compaction detection
	normalOutputs := []string{
		"Running tests...\nAll 42 tests passed",
		"git status\nOn branch main\nnothing to commit",
		"npm install\ninstalled 1234 packages",
		"Building project...\nBuild complete",
		"Error: undefined is not a function", // Error but not compaction
		"fatal: not a git repository",
		"Connection refused",
		"Rate limit exceeded",                                   // Error but not compaction
		"def compact_data(x):\n    return x.strip()",            // Code containing 'compact'
		"class ConversationManager:\n    def reset(self): pass", // Code with similar words
	}

	for _, output := range normalOutputs {
		t.Run(output[:min(30, len(output))], func(t *testing.T) {
			for _, agentType := range []string{"claude", "codex", "gemini", "cc", "cod", "gmi"} {
				event := DetectCompaction(output, agentType)
				if event != nil {
					t.Errorf("false positive for agent %s: detected compaction in %q, matched: %q",
						agentType, output, event.MatchedText)
				}
			}
		})
	}
}

func TestDetectCompactionWithPaneID(t *testing.T) {
	event := DetectCompactionWithPaneID("Conversation compacted", "claude", "%5")
	if event == nil {
		t.Fatal("expected event")
	}
	if event.PaneID != "%5" {
		t.Errorf("PaneID = %q, want %q", event.PaneID, "%5")
	}
}

func TestCompactionDetector(t *testing.T) {
	detector := NewCompactionDetector(1 * time.Minute)

	// Check with no compaction
	event := detector.Check("normal output", "claude", "%0")
	if event != nil {
		t.Error("should not detect compaction in normal output")
	}

	// Check with compaction
	event = detector.Check("Conversation compacted", "claude", "%1")
	if event == nil {
		t.Fatal("should detect compaction")
	}
	if event.PaneID != "%1" {
		t.Errorf("PaneID = %q, want %q", event.PaneID, "%1")
	}

	// Check events list
	events := detector.Events()
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}

	// Check events for pane
	paneEvents := detector.EventsForPane("%1")
	if len(paneEvents) != 1 {
		t.Errorf("expected 1 event for pane %%1, got %d", len(paneEvents))
	}

	paneEvents = detector.EventsForPane("%0")
	if len(paneEvents) != 0 {
		t.Errorf("expected 0 events for pane %%0, got %d", len(paneEvents))
	}
}

func TestCompactionDetector_HasRecentCompaction(t *testing.T) {
	detector := NewCompactionDetector(1 * time.Minute)

	if detector.HasRecentCompaction("%1", time.Minute) {
		t.Error("should not have recent compaction before any events")
	}

	detector.Check("Conversation compacted", "claude", "%1")

	if !detector.HasRecentCompaction("%1", time.Minute) {
		t.Error("should have recent compaction after event")
	}

	if detector.HasRecentCompaction("%2", time.Minute) {
		t.Error("should not have recent compaction for different pane")
	}
}

func TestCompactionDetector_Clear(t *testing.T) {
	detector := NewCompactionDetector(1 * time.Minute)
	detector.Check("Conversation compacted", "claude", "%1")

	if len(detector.Events()) != 1 {
		t.Error("should have 1 event")
	}

	detector.Clear()

	if len(detector.Events()) != 0 {
		t.Error("should have 0 events after clear")
	}
}

func TestCompactionEvent_Fields(t *testing.T) {
	event := DetectCompaction("Conversation compacted", "claude")
	if event == nil {
		t.Fatal("expected event")
	}

	if event.AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", event.AgentType, "claude")
	}
	if event.MatchedText != "Conversation compacted" {
		t.Errorf("MatchedText = %q, want %q", event.MatchedText, "Conversation compacted")
	}
	if event.DetectedAt.IsZero() {
		t.Error("DetectedAt should not be zero")
	}
	if event.Pattern == "" {
		t.Error("Pattern should be set")
	}
}
