package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRotateCmdValidation(t *testing.T) {
	tests := []struct {
		name                     string
		args                     []string
		flags                    map[string]string
		wantError                string
		wantErrorAny             []string
		skipIfAutoSelectPossible bool // Skip if exactly one session is running (auto-select applies)
	}{
		{
			name:                     "missing session and not in tmux",
			args:                     []string{},
			wantError:                "session",
			skipIfAutoSelectPossible: true, // Session auto-selected when only one exists
		},
		{
			name: "missing pane index",
			args: []string{"mysession"},
			wantErrorAny: []string{
				"pane index required",
				"session", // session may not exist in shared tmux environment
			},
		},
		{
			name: "dry run requires valid session/pane",
			args: []string{"mysession"},
			flags: map[string]string{
				"pane":    "0",
				"dry-run": "true",
			},
			// Dry run still needs to look up pane info, which fails without tmux
			wantErrorAny: []string{
				"getting panes",
				"session", // session may not exist in shared tmux environment
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Change to a temp dir to prevent CWD-based session inference
			tmpDir := t.TempDir()
			oldWd, _ := os.Getwd()
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("chdir failed: %v", err)
			}
			defer os.Chdir(oldWd)

			// Unset TMUX env var to prevent auto-detection from environment
			oldTmux := os.Getenv("TMUX")
			os.Unsetenv("TMUX")
			defer os.Setenv("TMUX", oldTmux)

			if tt.skipIfAutoSelectPossible && sessionAutoSelectPossible() {
				t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
			}

			cmd := newRotateCmd()
			// Redirect output to buffer to ensure non-interactive mode
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			// Set args
			if len(tt.args) > 0 {
				cmd.SetArgs(tt.args)
			} else {
				cmd.SetArgs([]string{})
			}

			// Set flags
			for k, v := range tt.flags {
				_ = cmd.Flags().Set(k, v)
			}

			// Execute
			err := cmd.Execute()

			if tt.wantError != "" || len(tt.wantErrorAny) > 0 {
				if err == nil {
					if tt.wantError != "" {
						t.Errorf("expected error containing %q, got nil", tt.wantError)
					} else {
						t.Errorf("expected error containing one of %q, got nil", tt.wantErrorAny)
					}
				} else if !errorMatchesAny(err.Error(), append(tt.wantErrorAny, tt.wantError)) {
					if tt.wantError != "" {
						t.Errorf("expected error containing %q, got %q", tt.wantError, err.Error())
					} else {
						t.Errorf("expected error containing one of %q, got %q", tt.wantErrorAny, err.Error())
					}
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func errorMatchesAny(err string, matches []string) bool {
	for _, match := range matches {
		if match == "" {
			continue
		}
		if strings.Contains(err, match) {
			return true
		}
	}
	return false
}

func TestQuotaProviderForAgentType_CanonicalizesAliases(t *testing.T) {

	tests := []struct {
		name      string
		agentType tmux.AgentType
		want      quota.Provider
		ok        bool
	}{
		{name: "claude alias", agentType: tmux.AgentType("claude_code"), want: quota.ProviderClaude, ok: true},
		{name: "codex alias", agentType: tmux.AgentType("openai-codex"), want: quota.ProviderCodex, ok: true},
		{name: "gemini alias", agentType: tmux.AgentType("google-gemini"), want: quota.ProviderGemini, ok: true},
		{name: "unsupported cursor", agentType: tmux.AgentCursor, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := quotaProviderForAgentType(tt.agentType)
			if ok != tt.ok {
				t.Fatalf("quotaProviderForAgentType(%q) ok = %v, want %v", tt.agentType, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("quotaProviderForAgentType(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestNormalizedProviderName_CanonicalizesFallbacks(t *testing.T) {

	tests := []struct {
		name      string
		agentType tmux.AgentType
		want      string
	}{
		{name: "claude alias", agentType: tmux.AgentType("claude_code"), want: "claude"},
		{name: "codex alias", agentType: tmux.AgentType("openai-codex"), want: "codex"},
		{name: "gemini alias", agentType: tmux.AgentType("google-gemini"), want: "gemini"},
		{name: "windsurf short alias", agentType: tmux.AgentType("ws"), want: "windsurf"},
		{name: "unknown falls back raw", agentType: tmux.AgentType("mystery"), want: "mystery"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedProviderName(tt.agentType); got != tt.want {
				t.Fatalf("normalizedProviderName(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestResolveRotationProjectDirRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	_, err := resolveRotationProjectDir("ntm", false)
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveRotationProjectDirAllowsWorkspaceFallbackForInferredSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	got, err := resolveRotationProjectDir("ntm", true)
	if err != nil {
		t.Fatalf("resolveRotationProjectDir() error = %v", err)
	}
	if got != cwdRepo {
		t.Fatalf("resolveRotationProjectDir() = %q, want %q", got, cwdRepo)
	}
}
