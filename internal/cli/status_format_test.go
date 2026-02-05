package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestFormatDuration(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			if got := formatDuration(tc.in); got != tc.want {
				t.Errorf("formatDuration(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPaneLabel(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentClaude}); got != "claude-sonnet-4-20250514" {
		t.Errorf("default claude model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentCodex}); got != "gpt-4" {
		t.Errorf("default codex model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentGemini}); got != "gemini-2.0-flash" {
		t.Errorf("default gemini model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentUser}); got != "" {
		t.Errorf("default user model = %q, want empty", got)
	}

	cfg = config.Default()
	cfg.Models.DefaultClaude = "claude-test"
	cfg.Models.DefaultCodex = "codex-test"
	cfg.Models.DefaultGemini = "gemini-test"

	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentClaude}); got != "claude-test" {
		t.Errorf("cfg claude model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentCodex}); got != "codex-test" {
		t.Errorf("cfg codex model = %q", got)
	}
	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentGemini}); got != "gemini-test" {
		t.Errorf("cfg gemini model = %q", got)
	}

	if got := modelNameForPane(tmux.Pane{Type: tmux.AgentClaude, Variant: "custom-variant"}); got != "custom-variant" {
		t.Errorf("variant override = %q", got)
	}
}
