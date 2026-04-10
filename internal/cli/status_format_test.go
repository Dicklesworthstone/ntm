package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestFormatDuration(t *testing.T) {

	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "seconds", in: 45 * time.Second, want: "45s"},
		{name: "minutes", in: 5 * time.Minute, want: "5m"},
		{name: "hours+minutes", in: 2*time.Hour + 15*time.Minute, want: "2h15m"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := formatDuration(tc.in); got != tc.want {
				t.Errorf("formatDuration(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPaneLabel(t *testing.T) {

	session := "myproject"
	pane := tmux.Pane{Index: 2, Title: "myproject__cc_2"}
	if got := paneLabel(session, pane); got != "cc_2" {
		t.Errorf("paneLabel trimmed prefix = %q, want %q", got, "cc_2")
	}

	pane.Title = "custom-title"
	if got := paneLabel(session, pane); got != "custom-title" {
		t.Errorf("paneLabel custom title = %q, want %q", got, "custom-title")
	}

	pane.Title = "   "
	if got := paneLabel(session, pane); got != "pane 2" {
		t.Errorf("paneLabel empty title = %q, want %q", got, "pane 2")
	}
}

func TestRenderProgressBar(t *testing.T) {

	if got := renderProgressBar(50, 0); got != "" {
		t.Errorf("renderProgressBar width 0 = %q, want empty", got)
	}

	bar := renderProgressBar(-10, 4)
	if bar != "[----]" {
		t.Errorf("renderProgressBar negative percent = %q, want %q", bar, "[----]")
	}

	bar = renderProgressBar(150, 3)
	if bar != "[===]" {
		t.Errorf("renderProgressBar >100 percent = %q, want %q", bar, "[===]")
	}

	bar = renderProgressBar(50, 4)
	if bar != "[==--]" {
		t.Errorf("renderProgressBar 50%% = %q, want %q", bar, "[==--]")
	}
}

func TestModelNameForPane(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	cfg = nil

	defaults := config.DefaultModels()
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentClaude}); got != defaults.DefaultClaude {
		t.Errorf("default claude model = %q, want %q", got, defaults.DefaultClaude)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentCodex}); got != defaults.DefaultCodex {
		t.Errorf("default codex model = %q, want %q", got, defaults.DefaultCodex)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentGemini}); got != defaults.DefaultGemini {
		t.Errorf("default gemini model = %q, want %q", got, defaults.DefaultGemini)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentOllama}); got != defaults.DefaultOllama {
		t.Errorf("default ollama model = %q, want %q", got, defaults.DefaultOllama)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentUser}); got != "" {
		t.Errorf("default user model = %q, want empty", got)
	}

	cfg = config.Default()
	cfg.Models.DefaultClaude = "claude-test"
	cfg.Models.DefaultCodex = "codex-test"
	cfg.Models.DefaultGemini = "gemini-test"
	cfg.Models.DefaultOllama = "ollama-test"

	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentClaude}); got != "claude-test" {
		t.Errorf("cfg claude model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentCodex}); got != "codex-test" {
		t.Errorf("cfg codex model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentGemini}); got != "gemini-test" {
		t.Errorf("cfg gemini model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentOllama}); got != "ollama-test" {
		t.Errorf("cfg ollama model = %q", got)
	}

	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentClaude, Variant: "custom-variant"}); got != "custom-variant" {
		t.Errorf("variant override = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentType("ollama"), Variant: "mistral"}); got != "mistral" {
		t.Errorf("ollama variant override = %q", got)
	}
}

func TestSessionPanePresentationCanonicalizesAliases(t *testing.T) {

	th := theme.CatppuccinMocha
	ic := icons.ASCII

	tests := []struct {
		name      string
		agentType tmux.AgentType
		wantColor string
		wantIcon  string
	}{
		{name: "claude alias", agentType: tmux.AgentType("claude_code"), wantColor: string(th.Claude), wantIcon: ic.Claude},
		{name: "codex alias", agentType: tmux.AgentType("openai-codex"), wantColor: string(th.Codex), wantIcon: ic.Codex},
		{name: "gemini alias", agentType: tmux.AgentType("google-gemini"), wantColor: string(th.Gemini), wantIcon: ic.Gemini},
		{name: "cursor", agentType: tmux.AgentCursor, wantColor: string(th.Cursor), wantIcon: ic.Cursor},
		{name: "windsurf alias", agentType: tmux.AgentType("ws"), wantColor: string(th.Windsurf), wantIcon: ic.Windsurf},
		{name: "aider", agentType: tmux.AgentAider, wantColor: string(th.Aider), wantIcon: ic.Aider},
		{name: "ollama", agentType: tmux.AgentOllama, wantColor: string(th.Ollama), wantIcon: ic.Ollama},
		{name: "user", agentType: tmux.AgentUser, wantColor: string(th.User), wantIcon: ic.Terminal},
		{name: "unknown", agentType: tmux.AgentType("mystery"), wantColor: string(th.Overlay), wantIcon: ic.Robot},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotColor, gotIcon := sessionPanePresentation(tmux.Pane{Type: tc.agentType}, th, ic)
			if gotColor != tc.wantColor {
				t.Fatalf("sessionPanePresentation(%q) color = %q, want %q", tc.agentType, gotColor, tc.wantColor)
			}
			if gotIcon != tc.wantIcon {
				t.Fatalf("sessionPanePresentation(%q) icon = %q, want %q", tc.agentType, gotIcon, tc.wantIcon)
			}
		})
	}
}

func TestModelNameForPaneCanonicalizesAliases(t *testing.T) {

	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	aliasDefaults := config.DefaultModels()
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentType("claude_code")}); got != aliasDefaults.DefaultClaude {
		t.Fatalf("claude alias model = %q, want %q", got, aliasDefaults.DefaultClaude)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentType("openai-codex")}); got != aliasDefaults.DefaultCodex {
		t.Fatalf("codex alias model = %q, want %q", got, aliasDefaults.DefaultCodex)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentType("google-gemini")}); got != aliasDefaults.DefaultGemini {
		t.Fatalf("gemini alias model = %q, want %q", got, aliasDefaults.DefaultGemini)
	}
}

func TestZoomPanePresentationCanonicalizesAliases(t *testing.T) {

	th := theme.CatppuccinMocha
	ic := icons.ASCII

	tests := []struct {
		name      string
		agentType tmux.AgentType
		wantColor string
		wantIcon  string
	}{
		{name: "codex alias", agentType: tmux.AgentType("openai-codex"), wantColor: colorize(string(th.Codex)), wantIcon: ic.Codex},
		{name: "cursor", agentType: tmux.AgentCursor, wantColor: colorize(string(th.Cursor)), wantIcon: ic.Cursor},
		{name: "windsurf alias", agentType: tmux.AgentType("ws"), wantColor: colorize(string(th.Windsurf)), wantIcon: ic.Windsurf},
		{name: "aider", agentType: tmux.AgentAider, wantColor: colorize(string(th.Aider)), wantIcon: ic.Aider},
		{name: "user", agentType: tmux.AgentUser, wantColor: colorize(string(th.User)), wantIcon: ic.Terminal},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotColor, gotIcon := zoomPanePresentation(tmux.Pane{Type: tc.agentType}, th, ic)
			if gotColor != tc.wantColor {
				t.Fatalf("zoomPanePresentation(%q) color = %q, want %q", tc.agentType, gotColor, tc.wantColor)
			}
			if gotIcon != tc.wantIcon {
				t.Fatalf("zoomPanePresentation(%q) icon = %q, want %q", tc.agentType, gotIcon, tc.wantIcon)
			}
		})
	}
}
