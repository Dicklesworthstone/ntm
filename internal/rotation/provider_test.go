package rotation

import (
	"testing"
)

func TestGetProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		wantName  string
		wantNil   bool
	}{
		{name: "claude short", agentType: "cc", wantName: "Claude"},
		{name: "claude long", agentType: "claude", wantName: "Claude"},
		{name: "claude shared alias", agentType: "claude-code", wantName: "Claude"},
		{name: "claude provider alias", agentType: "anthropic", wantName: "Claude"},
		{name: "codex short", agentType: "cod", wantName: "Codex"},
		{name: "codex long", agentType: "codex", wantName: "Codex"},
		{name: "codex cli alias", agentType: "codex-cli", wantName: "Codex"},
		{name: "codex provider alias", agentType: "openai", wantName: "Codex"},
		{name: "codex shared alias", agentType: "openai-codex", wantName: "Codex"},
		{name: "gemini short", agentType: "gmi", wantName: "Gemini"},
		{name: "gemini long", agentType: "gemini", wantName: "Gemini"},
		{name: "gemini cli alias", agentType: "gemini-cli", wantName: "Gemini"},
		{name: "gemini provider alias", agentType: "google", wantName: "Gemini"},
		{name: "gemini shared alias", agentType: "google-gemini", wantName: "Gemini"},
		{name: "gemini google ai alias", agentType: "google-ai", wantName: "Gemini"},
		{name: "unknown type", agentType: "unknown", wantNil: true},
		{name: "empty type", agentType: "", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := GetProvider(tt.agentType)

			if tt.wantNil {
				if provider != nil {
					t.Errorf("GetProvider(%q) = %v, want nil", tt.agentType, provider)
				}
				return
			}

			if provider == nil {
				t.Fatalf("GetProvider(%q) = nil, want non-nil", tt.agentType)
			}

			if got := provider.Name(); got != tt.wantName {
				t.Errorf("provider.Name() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestClaudeProvider(t *testing.T) {
	t.Parallel()

	p := &ClaudeProvider{}

	t.Run("Name", func(t *testing.T) {
		if got := p.Name(); got != "Claude" {
			t.Errorf("Name() = %q, want %q", got, "Claude")
		}
	})

	t.Run("LoginCommand", func(t *testing.T) {
		if got := p.LoginCommand(); got != "/login" {
			t.Errorf("LoginCommand() = %q, want %q", got, "/login")
		}
	})

	t.Run("ExitCommand", func(t *testing.T) {
		if got := p.ExitCommand(); got != "/exit" {
			t.Errorf("ExitCommand() = %q, want %q", got, "/exit")
		}
	})

	t.Run("AuthSuccessPatterns", func(t *testing.T) {
		patterns := p.AuthSuccessPatterns()
		if len(patterns) != 2 {
			t.Errorf("AuthSuccessPatterns() returned %d patterns, want 2", len(patterns))
		}
		// Check expected patterns exist
		wantPatterns := map[string]bool{
			"successfully logged in": true,
			"authenticated as":       true,
		}
		for _, pat := range patterns {
			if !wantPatterns[pat] {
				t.Errorf("unexpected pattern: %q", pat)
			}
		}
	})

	t.Run("ContinuationPrompt", func(t *testing.T) {
		if got := p.ContinuationPrompt(); got != "continue. Use ultrathink" {
			t.Errorf("ContinuationPrompt() = %q, want %q", got, "continue. Use ultrathink")
		}
	})

	t.Run("SupportsReauth", func(t *testing.T) {
		if got := p.SupportsReauth(); got != true {
			t.Errorf("SupportsReauth() = %v, want true", got)
		}
	})
}

func TestCodexProvider(t *testing.T) {
	t.Parallel()

	p := &CodexProvider{}

	t.Run("Name", func(t *testing.T) {
		if got := p.Name(); got != "Codex" {
			t.Errorf("Name() = %q, want %q", got, "Codex")
		}
	})

	t.Run("LoginCommand", func(t *testing.T) {
		// Codex uses /logout because it needs restart
		if got := p.LoginCommand(); got != "/logout" {
			t.Errorf("LoginCommand() = %q, want %q", got, "/logout")
		}
	})

	t.Run("ExitCommand", func(t *testing.T) {
		if got := p.ExitCommand(); got != "/exit" {
			t.Errorf("ExitCommand() = %q, want %q", got, "/exit")
		}
	})

	t.Run("AuthSuccessPatterns", func(t *testing.T) {
		patterns := p.AuthSuccessPatterns()
		// Codex doesn't use in-pane auth, so patterns should be nil
		if patterns != nil {
			t.Errorf("AuthSuccessPatterns() = %v, want nil", patterns)
		}
	})

	t.Run("ContinuationPrompt", func(t *testing.T) {
		if got := p.ContinuationPrompt(); got != "" {
			t.Errorf("ContinuationPrompt() = %q, want empty string", got)
		}
	})

	t.Run("SupportsReauth", func(t *testing.T) {
		if got := p.SupportsReauth(); got != false {
			t.Errorf("SupportsReauth() = %v, want false", got)
		}
	})
}

func TestGeminiProvider(t *testing.T) {
	t.Parallel()

	p := &GeminiProvider{}

	t.Run("Name", func(t *testing.T) {
		if got := p.Name(); got != "Gemini" {
			t.Errorf("Name() = %q, want %q", got, "Gemini")
		}
	})

	t.Run("LoginCommand", func(t *testing.T) {
		if got := p.LoginCommand(); got != "/auth" {
			t.Errorf("LoginCommand() = %q, want %q", got, "/auth")
		}
	})

	t.Run("ExitCommand", func(t *testing.T) {
		if got := p.ExitCommand(); got != "/exit" {
			t.Errorf("ExitCommand() = %q, want %q", got, "/exit")
		}
	})

	t.Run("AuthSuccessPatterns", func(t *testing.T) {
		patterns := p.AuthSuccessPatterns()
		if len(patterns) != 2 {
			t.Errorf("AuthSuccessPatterns() returned %d patterns, want 2", len(patterns))
		}
		wantPatterns := map[string]bool{
			"authenticated": true,
			"logged in":     true,
		}
		for _, pat := range patterns {
			if !wantPatterns[pat] {
				t.Errorf("unexpected pattern: %q", pat)
			}
		}
	})

	t.Run("ContinuationPrompt", func(t *testing.T) {
		if got := p.ContinuationPrompt(); got != "continue" {
			t.Errorf("ContinuationPrompt() = %q, want %q", got, "continue")
		}
	})

	t.Run("SupportsReauth", func(t *testing.T) {
		if got := p.SupportsReauth(); got != false {
			t.Errorf("SupportsReauth() = %v, want false", got)
		}
	})
}

func TestProviderInterface(t *testing.T) {
	t.Parallel()

	// Test that all providers implement the Provider interface
	providers := []Provider{
		&ClaudeProvider{},
		&CodexProvider{},
		&GeminiProvider{},
	}

	for _, p := range providers {
		t.Run(p.Name(), func(t *testing.T) {
			// Just calling all methods to verify interface implementation
			_ = p.Name()
			_ = p.LoginCommand()
			_ = p.ExitCommand()
			_ = p.AuthSuccessPatterns()
			_ = p.ContinuationPrompt()
			_ = p.SupportsReauth()
		})
	}
}

func TestProviderAuthPatternsConsistency(t *testing.T) {
	t.Parallel()

	// Providers that support reauth should have auth patterns
	// Providers that don't support reauth may or may not have patterns

	claude := &ClaudeProvider{}
	if claude.SupportsReauth() && len(claude.AuthSuccessPatterns()) == 0 {
		t.Error("Claude supports reauth but has no auth patterns")
	}

	codex := &CodexProvider{}
	if codex.SupportsReauth() && len(codex.AuthSuccessPatterns()) == 0 {
		t.Error("Codex supports reauth but has no auth patterns")
	}

	// Gemini doesn't support reauth, but still has patterns for initial auth detection
	gemini := &GeminiProvider{}
	_ = gemini.AuthSuccessPatterns() // Just verify it doesn't panic
}

func TestMatchesAuthSuccess(t *testing.T) {
	t.Parallel()

	claude := GetProvider("claude")
	if matched, ok := MatchesAuthSuccess(claude, "Welcome back! Authenticated as alice@example.com"); !ok || matched != "authenticated as" {
		t.Fatalf("MatchesAuthSuccess(claude) = (%q, %v), want (authenticated as, true)", matched, ok)
	}
	if _, ok := MatchesAuthSuccess(claude, "Waiting for browser login..."); ok {
		t.Fatal("MatchesAuthSuccess(claude) matched unrelated output")
	}

	gemini := GetProvider("gemini")
	if matched, ok := MatchesAuthSuccess(gemini, "User successfully LOGGED IN via OAuth"); !ok || matched != "logged in" {
		t.Fatalf("MatchesAuthSuccess(gemini) = (%q, %v), want (logged in, true)", matched, ok)
	}

	codex := GetProvider("codex")
	if _, ok := MatchesAuthSuccess(codex, "Authenticated as user"); ok {
		t.Fatal("MatchesAuthSuccess(codex) should return false when provider has no in-pane auth patterns")
	}
	if _, ok := MatchesAuthSuccess(nil, "Successfully logged in"); ok {
		t.Fatal("MatchesAuthSuccess(nil) should return false")
	}
}
