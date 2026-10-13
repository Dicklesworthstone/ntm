package agent

import (
	"strings"
	"testing"
)

func TestDetectGeminiTurn(t *testing.T) {
	const prompt = ">   Type your message or @path/to/file"
	const footer = "workspace (/directory)   sandbox      /model        context\n~/project               no sandbox   gemini-pro    14% used"
	const quotaFooter = "workspace (/directory)   sandbox      /model        quota\n~/project               no sandbox   gemini-pro    15% used"
	for _, tc := range []struct {
		name   string
		output string
		want   GeminiTurnState
	}{
		{"empty startup", "", GeminiTurnUnknown},
		{"whitespace startup", " \n\n", GeminiTurnUnknown},
		{"legacy named prompt", "Task completed.\n\ngemini>", GeminiTurnIdle},
		{"legacy bare prompt", "Task completed.\n>", GeminiTurnIdle},
		{"current placeholder", prompt, GeminiTurnIdle},
		{"yolo placeholder", "*   Type your message or @path/to/file", GeminiTurnIdle},
		{"bordered placeholder", "╭────────────────────────────────────────────────╮\n│ > Type your message or @path/to/file            │\n╰────────────────────────────────────────────────╯", GeminiTurnIdle},
		{"bordered empty", "│ >                         │\n╰───────────────────────────╯", GeminiTurnIdle},
		{"bordered yolo empty", "│ *                         │\n╰───────────────────────────╯", GeminiTurnIdle},
		{"full footer", prompt + "\n" + footer, GeminiTurnIdle},
		{"usage footer", prompt + "\n" + quotaFooter, GeminiTurnIdle},
		{"exhausted footer", prompt + "\n/model quota\ngemini-pro limit reached", GeminiTurnRateLimited},
		{"exhaustion before composer", "RESOURCE_EXHAUSTED\n" + prompt, GeminiTurnRateLimited},
		{"rate limit during response", "Rate limit exceeded\n⠋ Retrying (esc to cancel, 12s)\n" + prompt, GeminiTurnRateLimited},
		{"sandbox only footer", prompt + "\nsandbox\nno sandbox", GeminiTurnIdle},
		{"model only footer", prompt + "\n/model\ngemini-pro", GeminiTurnIdle},
		{"legacy memory footer", prompt + "\ngemini-2.0-flash-preview /model | 396.8 MB", GeminiTurnIdle},
		{"shortcut footer", prompt + "\n? for shortcuts", GeminiTurnIdle},
		{"active before composer", "⠋ Thinking... (esc to cancel, 12s)\n" + prompt + "\n" + footer, GeminiTurnWorking},
		{"active after composer", prompt + "\n(esc to cancel, 12s)", GeminiTurnWorking},
		{"wrapped cancel", "(esc to\n cancel, 12s)\n" + prompt, GeminiTurnWorking},
		{"custom thought", "Examining the dependency graph (esc to cancel, 1m 2s)\n" + prompt, GeminiTurnWorking},
		{"spinner without timer", "⠹ Waiting for confirmation\n" + prompt, GeminiTurnWorking},
		{"prose is not work", "Finished thinking about and testing the implementation.\n" + prompt, GeminiTurnIdle},
		{"stale work marker", "⠋ Thinking... (esc to cancel, 12s)\n" + strings.Repeat("old transcript\n", geminiLiveTailLines) + prompt, GeminiTurnIdle},
		{"draft", "> please review the changes", GeminiTurnUnknown},
		{"bordered draft", "│ > please review the changes │", GeminiTurnUnknown},
		{"yolo bullet", "*", GeminiTurnUnknown},
		{"shell mode", "!   Type your shell command\n" + footer, GeminiTurnUnknown},
		{"shell prompt", "~/project $", GeminiTurnUnknown},
		{"quoted placeholder", "The placeholder is > Type your message or @path/to/file", GeminiTurnUnknown},
		{"fenced example", "```\n" + prompt + "\n```", GeminiTurnUnknown},
		{"new output below prompt", prompt + "\nMore response content is arriving.", GeminiTurnUnknown},
		{"footer alone", footer, GeminiTurnUnknown},
		{"blank padded live frame", prompt + "\n" + footer + strings.Repeat("\n", 40), GeminiTurnIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectGeminiTurn(tc.output); got != tc.want {
				t.Fatalf("DetectGeminiTurn(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestDetectGeminiTurnSpinnerFrames(t *testing.T) {
	for _, frame := range "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏" {
		output := string(frame) + " Examining implementation\n> Type your message or @path/to/file"
		if got := DetectGeminiTurn(output); got != GeminiTurnWorking {
			t.Errorf("spinner %c: got %v, want working", frame, got)
		}
	}
}

func TestGeminiRateLimitIndicators(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  string
		limited bool
	}{
		{"normal quota footer", "> Type your message or @path/to/file\nworkspace (/directory) sandbox /model quota\n~/project no sandbox gemini-pro 15% used", false},
		{"capacity discussion", "The cache has capacity for 1000 entries.\ngemini>", false},
		{"retry advice", "The implementation is ready; try again with the new flags.\ngemini>", false},
		{"quota discussion", "I added a quota field to the configuration.\ngemini>", false},
		{"quota exceeded", "Quota exceeded for quota metric requests.", true},
		{"google quota error", "You exceeded your current quota, please check your plan.", true},
		{"resource exhausted enum", "[API Error: RESOURCE_EXHAUSTED]", true},
		{"resource exhausted prose", "Resource exhausted. Retry later.", true},
		{"rate limit retry", "Rate limit reached, retrying in 30s", true},
		{"http error", "429 Too Many Requests", true},
		{"capacity exhaustion", "You have exhausted your capacity on this model.", true},
		{"exhausted footer", "/model quota\ngemini-pro limit reached", true},
		{"old quota error", "RESOURCE_EXHAUSTED\n" + strings.Repeat("completed output\n", 60) + "gemini>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(geminiRateLimitIndicators(tc.output)) > 0; got != tc.limited {
				t.Fatalf("limited=%v, want %v; indicators=%v", got, tc.limited, geminiRateLimitIndicators(tc.output))
			}
		})
	}
}

func TestGeminiContextUsage(t *testing.T) {
	const header = "workspace (/directory) sandbox /model context"
	for _, tc := range []struct {
		name   string
		output string
		used   float64
		ok     bool
	}{
		{"wide footer", header + "\n~/project no sandbox gemini-pro 14% used", 14, true},
		{"narrow footer", header + "\n.../project no sandbox gemini-pro 14%", 14, true},
		{"hidden workspace and sandbox", "/model context\ngemini-pro 90% used", 90, true},
		{"context only", "context\n42.5% used", 42.5, true},
		{"empty context", "context\n0% used", 0, true},
		{"full context", "context\n100% used", 100, true},
		{"unicode path with spaces", header + "\n~/日本語/my project no sandbox gemini-pro 93.25% used", 93.25, true},
		{"padded footer", header + "\n~/project no sandbox gemini-pro 77% used\n\n", 77, true},
		{"shortcut below footer", header + "\n~/project no sandbox gemini-pro 77% used\n? for shortcuts", 77, true},
		{"quota is not context", "workspace (/directory) sandbox /model quota\n~/project no sandbox gemini-pro 85% used", 0, false},
		{"old context then quota", "context\n98% used\n/model quota\ngemini-pro 15% used", 0, false},
		{"old context then new prompt", "context\n98% used\n> Type your message or @path/to/file", 0, false},
		{"transcript percentage", "The operation is 99% complete.", 0, false},
		{"memory is not context", "gemini-2.0-flash-preview /model | 396.8 MB", 0, false},
		{"out of range", "context\n101% used", 0, false},
		{"negative", "context\n-1% used", 0, false},
		{"nonfinite", "context\nNaN% used", 0, false},
		{"wrong direction", "context\n75% left", 0, false},
		{"missing value", "context", 0, false},
		{"missing unit", "context\n75", 0, false},
		{"malformed current footer", "context\n98% used\ncontext\nunknown", 0, false},
		{"new transcript below footer", "context\n98% used\nNew response content", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, ok := GeminiContextUsage(tc.output)
			if used != tc.used || ok != tc.ok {
				t.Fatalf("GeminiContextUsage(%q) = (%v, %v), want (%v, %v)", tc.output, used, ok, tc.used, tc.ok)
			}
		})
	}
}
