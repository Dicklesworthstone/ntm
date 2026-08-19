package gemini

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIsGeminiReady(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "simple gemini prompt",
			output:   "Welcome to Gemini CLI\ngemini>",
			expected: true,
		},
		{
			name:     "gemini prompt with space",
			output:   "Some output\ngemini> ",
			expected: true,
		},
		{
			name:     "uppercase GEMINI prompt",
			output:   "Starting...\nGEMINI>",
			expected: true,
		},
		{
			name:     "gemini prompt in the middle (last non-empty line)",
			output:   "Loading...\ngemini>\n\n",
			expected: true,
		},
		{
			name:     "no gemini prompt",
			output:   "Loading...\nPlease wait...",
			expected: false,
		},
		{
			name:     "loading state",
			output:   "Initializing Gemini CLI...\n█",
			expected: false,
		},
		{
			name:     "empty output",
			output:   "",
			expected: false,
		},
		{
			name:     "only whitespace",
			output:   "   \n\n  ",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isGeminiReady(tt.output)
			if got != tt.expected {
				t.Errorf("isGeminiReady(%q) = %v, want %v", tt.output, got, tt.expected)
			}
		})
	}
}

func TestIsModelMenuVisible(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "select model text",
			output:   "Please select a model:\n1. Flash\n2. Pro",
			expected: true,
		},
		{
			name:     "available models text",
			output:   "Available models:\n- Flash\n- Pro",
			expected: true,
		},
		{
			name:     "numbered list",
			output:   "1. Flash (default)\n2. Pro\n3. Ultra",
			expected: true,
		},
		{
			name:     "model names present",
			output:   "Choose: Flash or Pro",
			expected: true,
		},
		{
			name:     "bracket selection",
			output:   "[1] Flash  [2] Pro",
			expected: true,
		},
		{
			name:     "no model menu",
			output:   "gemini> Hello, how can I help?",
			expected: false,
		},
		{
			name:     "empty output",
			output:   "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isModelMenuVisible(tt.output)
			if got != tt.expected {
				t.Errorf("isModelMenuVisible(%q) = %v, want %v", tt.output, got, tt.expected)
			}
		})
	}
}

func TestIsProModelSelected(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "pro selected message",
			output:   "Pro model selected. Ready to assist.",
			expected: true,
		},
		{
			name:     "selected pro message",
			output:   "You have selected Pro mode.",
			expected: true,
		},
		{
			name:     "using pro model",
			output:   "Using Pro model for this session.",
			expected: true,
		},
		{
			name:     "model colon pro",
			output:   "Model: Gemini Pro\ngemini>",
			expected: true,
		},
		{
			name:     "gemini-2.5-pro",
			output:   "Current model: gemini-2.5-pro",
			expected: true,
		},
		{
			name:     "gemini 3 (pro)",
			output:   "You are now using Gemini 3.",
			expected: true,
		},
		{
			name:     "flash model",
			output:   "Using Flash model.",
			expected: false,
		},
		{
			name:     "no model info",
			output:   "gemini> Hello!",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isProModelSelected(tt.output)
			if got != tt.expected {
				t.Errorf("isProModelSelected(%q) = %v, want %v", tt.output, got, tt.expected)
			}
		})
	}
}

func TestGeminiPromptPatternMatches(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"gemini>", true},
		{"gemini> ", true},
		{"GEMINI>", true},
		{"Gemini>", true},
		{"gemini", false},
		{"geminipro>", false},
		{">", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := geminiPromptPattern.MatchString(strings.TrimSpace(tt.input))
			if got != tt.expected {
				t.Errorf("geminiPromptPattern.MatchString(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIsGeminiReady_StripsANSI(t *testing.T) {
	output := "Booting...\n\x1b[31mGEMINI>\x1b[0m"
	if !isGeminiReady(output) {
		t.Fatalf("expected isGeminiReady to be true for ANSI-colored prompt")
	}
}

func TestIsModelMenuVisible_StripsANSI(t *testing.T) {
	output := "\x1b[32mSelect a model:\x1b[0m\n1. Flash\n2. Pro"
	if !isModelMenuVisible(output) {
		t.Fatalf("expected isModelMenuVisible to be true for ANSI-colored menu")
	}
}

func TestIsProModelSelected_StripsANSI(t *testing.T) {
	output := "Model: \x1b[36mGemini Pro\x1b[0m"
	if !isProModelSelected(output) {
		t.Fatalf("expected isProModelSelected to be true for ANSI-colored model line")
	}
}

func TestWaitForReady_TimeoutMessage(t *testing.T) {
	err := WaitForReady(context.Background(), "%1", 0, time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout after 0s waiting for Gemini prompt") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "consider increasing gemini_setup.ready_timeout_seconds") {
		t.Fatalf("missing config hint in error: %v", err)
	}
}

func TestWaitForModelMenu_TimeoutMessage(t *testing.T) {
	err := waitForModelMenu(context.Background(), "%1", 0, time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout after 0s waiting for model menu") {
		t.Fatalf("unexpected error: %v", err)
	}
}
