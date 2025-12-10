package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDetectFromProcess(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		wantType string
		wantConf float64
	}{
		{"claude process", "claude --help", "claude", 0.95},
		{"claude-code process", "claude-code", "claude", 0.95},
		{"codex process", "codex-cli run", "codex", 0.95},
		{"gemini process", "gemini-cli", "gemini", 0.95},
		{"aider process", "aider-chat", "aider", 0.95},
		{"cursor process", "cursor", "cursor", 0.95},
		{"windsurf process", "windsurf", "windsurf", 0.95},
		{"unknown process", "vim", "unknown", 0.0},
		{"bash shell", "bash", "unknown", 0.0},
		{"zsh shell", "zsh", "unknown", 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection := detectFromProcess(tt.command)
			if detection.Type != tt.wantType {
				t.Errorf("detectFromProcess(%q) type = %v, want %v", tt.command, detection.Type, tt.wantType)
			}
			if detection.Confidence != tt.wantConf {
				t.Errorf("detectFromProcess(%q) confidence = %v, want %v", tt.command, detection.Confidence, tt.wantConf)
			}
			if tt.wantType != "unknown" && detection.Method != MethodProcess {
				t.Errorf("detectFromProcess(%q) method = %v, want %v", tt.command, detection.Method, MethodProcess)
			}
		})
	}
}

func TestDetectFromContent(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantType string
	}{
		{"claude mention", "Claude Code is ready", "claude"},
		{"claude prompt", "claude> help", "claude"},
		{"anthropic mention", "Powered by Anthropic", "claude"},
		{"codex prompt", "codex> generate", "codex"},
		{"openai codex", "OpenAI Codex CLI", "codex"},
		{"gemini prompt", "gemini> search", "gemini"},
		{"aider prompt", "aider> fix bug", "aider"},
		{"cursor ai", "Cursor AI ready", "cursor"},
		{"windsurf codeium", "Powered by Codeium", "windsurf"},
		{"plain shell", "$ ls -la\nREADME.md", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection := detectFromContent(tt.content)
			if detection.Type != tt.wantType {
				t.Errorf("detectFromContent(%q) type = %v, want %v", tt.content, detection.Type, tt.wantType)
			}
			if tt.wantType != "unknown" && detection.Method != MethodContent {
				t.Errorf("detectFromContent(%q) method = %v, want %v", tt.content, detection.Method, MethodContent)
			}
		})
	}
}

func TestDetectFromTitle(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		wantType string
	}{
		{"claude in title", "Claude Code - Editor", "claude"},
		{"codex in title", "Codex CLI Session", "codex"},
		{"gemini in title", "Gemini AI", "gemini"},
		{"aider in title", "Aider Chat", "aider"},
		{"plain title", "Terminal", "unknown"},
		{"mixed case", "CLAUDE Agent", "claude"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection := detectFromTitle(tt.title)
			if detection.Type != tt.wantType {
				t.Errorf("detectFromTitle(%q) type = %v, want %v", tt.title, detection.Type, tt.wantType)
			}
		})
	}
}

func TestDetectFromNTMTitle(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		wantType string
		wantConf float64
	}{
		{"NTM claude pane", "myproject__cc_1", "claude", 0.9},
		{"NTM codex pane", "myproject__cod_2", "codex", 0.9},
		{"NTM gemini pane", "session__gmi_1", "gemini", 0.9},
		{"non-NTM title", "Terminal", "unknown", 0.0},
		{"partial match", "code_cc", "unknown", 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection := detectFromNTMTitle(tt.title)
			if detection.Type != tt.wantType {
				t.Errorf("detectFromNTMTitle(%q) type = %v, want %v", tt.title, detection.Type, tt.wantType)
			}
			if detection.Confidence != tt.wantConf {
				t.Errorf("detectFromNTMTitle(%q) confidence = %v, want %v", tt.title, detection.Confidence, tt.wantConf)
			}
		})
	}
}

func TestDetectAgentTypeEnhanced(t *testing.T) {
	tests := []struct {
		name       string
		pane       tmux.Pane
		content    string
		wantType   string
		wantMethod DetectionMethod
	}{
		{
			name:       "process detection highest priority",
			pane:       tmux.Pane{Command: "claude-code", Title: "Terminal"},
			content:    "random output",
			wantType:   "claude",
			wantMethod: MethodProcess,
		},
		{
			name:       "content detection when no process match",
			pane:       tmux.Pane{Command: "bash", Title: "Terminal"},
			content:    "Claude Code is ready\n>",
			wantType:   "claude",
			wantMethod: MethodContent,
		},
		{
			name:       "NTM title detection",
			pane:       tmux.Pane{Command: "bash", Title: "project__cc_1"},
			content:    "",
			wantType:   "claude",
			wantMethod: MethodTitle,
		},
		{
			name:       "unknown when nothing matches",
			pane:       tmux.Pane{Command: "vim", Title: "editor"},
			content:    "editing file.txt",
			wantType:   "unknown",
			wantMethod: MethodUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detection := DetectAgentTypeEnhanced(tt.pane, tt.content)
			if detection.Type != tt.wantType {
				t.Errorf("DetectAgentTypeEnhanced() type = %v, want %v", detection.Type, tt.wantType)
			}
			if detection.Method != tt.wantMethod {
				t.Errorf("DetectAgentTypeEnhanced() method = %v, want %v", detection.Method, tt.wantMethod)
			}
		})
	}
}
