package robot

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeEnsembleAgentType_Aliases(t *testing.T) {

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
		{"openai", "cod"},
		{"openai-codex", "cod"},
		{"codex-cli", "cod"},
		{"codex_cli", "cod"},
		{"gmi", "gmi"},
		{"gemini", "gmi"},
		{"google", "gmi"},
		{"google-ai", "gmi"},
		{"google-gemini", "gmi"},
		{"gemini-cli", "gmi"},
		{"gemini_cli", "gmi"},
		{"cursor", "cursor"},
		{"ws", "windsurf"},
		{"windsurf", "windsurf"},
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

func TestParseEnsembleAgentMix_AcceptsSharedAliases(t *testing.T) {

	mix, err := parseEnsembleAgentMix("openai-codex=2, google-gemini=1, ws=1")
	if err != nil {
		t.Fatalf("parseEnsembleAgentMix error: %v", err)
	}

	if mix["cod"] != 2 {
		t.Fatalf("cod count = %d, want 2", mix["cod"])
	}
	if mix["gmi"] != 1 {
		t.Fatalf("gmi count = %d, want 1", mix["gmi"])
	}
	if mix["windsurf"] != 1 {
		t.Fatalf("windsurf count = %d, want 1", mix["windsurf"])
	}
}

func TestGetEnsembleSpawn_CancelledBeforeTmuxInspection(t *testing.T) {
	t.Setenv("NTM_TMUX_BINARY", t.TempDir()+"/missing-tmux")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	output, err := GetEnsembleSpawn(ctx, EnsembleSpawnOptions{
		Session:  "cancelled-ensemble",
		Question: "What should we analyze?",
		Modes:    "deductive,abductive",
	}, nil)
	if err != nil {
		t.Fatalf("GetEnsembleSpawn returned Go error: %v", err)
	}
	if output == nil {
		t.Fatal("expected cancellation envelope")
	}
	if output.Success || output.ErrorCode != ErrCodeTimeout || !strings.Contains(output.Error, context.Canceled.Error()) {
		t.Fatalf("expected cancellation envelope, got %+v", output)
	}
	if output.Modes == nil || len(output.Modes) != 0 || output.Status != "" {
		t.Fatalf("cancelled spawn reported created panes: %+v", output)
	}
}
