package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireIsolableCredentialStore skips assertions about the file-based
// isolation property on platforms where the credential lives somewhere this
// mechanism cannot reach (the macOS Keychain). Those platforms are covered by
// TestCredentialIsolated_FailsClosedWhenTheStoreIsNotIsolable instead — the
// point being that CredentialIsolated must NOT answer true there.
func requireIsolableCredentialStore(t *testing.T) {
	t.Helper()
	if err := credentialStoreIsolable(); err != nil {
		t.Skipf("credential store is not isolable on this platform: %v", err)
	}
}

// The pane shell runs from the project directory, which may differ from the
// directory that was current when ntm loaded its configuration. A relative
// token-file setting must therefore be resolved before it becomes command text.
func TestResolveClaudeSetupTokenFile_NormalizesRelativePath(t *testing.T) {
	configDir := t.TempDir()
	t.Chdir(configDir)
	writeFile(t, "claude.token", "sk-ant-oat-test")

	got, err := ResolveClaudeSetupTokenFile("claude.token")
	if err != nil {
		t.Fatalf("ResolveClaudeSetupTokenFile: %v", err)
	}
	want := filepath.Join(configDir, "claude.token")
	if got != want {
		t.Fatalf("token path = %q, want absolute %q", got, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestEnvForPane_IncludesTokenOnlyWhenProvided(t *testing.T) {
	p := NewClaudeConfigProvisioner(t.TempDir())

	env := p.EnvForPane("proj", "1", "")
	if len(env.SecretAssignments) != 0 {
		t.Fatalf("env carries a token assignment with no token file configured: %v", env.SecretAssignments)
	}
	if env.Vars[ClaudeConfigDirEnvVar] == "" {
		t.Fatal("env is missing CLAUDE_CONFIG_DIR")
	}

	env = p.EnvForPane("proj", "1", "/secrets/claude.token")
	if len(env.SecretAssignments) != 1 {
		t.Fatalf("secret assignments = %v, want exactly one token assignment", env.SecretAssignments)
	}
	if _, ok := env.Vars[ClaudeOAuthTokenEnvVar]; ok {
		t.Fatal("the OAuth token must never be a literal Vars entry; those are quoted into the visible command")
	}
}

func TestEnvForPane_NeverPutsTheTokenValueInTheCommandText(t *testing.T) {
	const tokenValue = "sk-ant-oat-super-secret-value"
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "claude.token")
	writeFile(t, tokenFile, tokenValue)

	env := NewClaudeConfigProvisioner(dir).EnvForPane("proj", "1", tokenFile)

	rendered := strings.Join(env.SecretAssignments, " ")
	for k, v := range env.Vars {
		rendered += " " + k + "=" + v
	}

	if strings.Contains(rendered, tokenValue) {
		t.Fatalf("the token VALUE reached the command text: %q", rendered)
	}
	if !strings.Contains(rendered, tokenFile) {
		t.Fatalf("the command text does not reference the token file: %q", rendered)
	}
	// It must be a shell command substitution, or the pane receives the
	// literal text "$(cat ...)" as its token.
	want := ClaudeOAuthTokenEnvVar + `="$(cat '` + tokenFile + `')"`
	if env.SecretAssignments[0] != want {
		t.Fatalf("assignment = %q, want %q", env.SecretAssignments[0], want)
	}
}

func TestEnvForPane_QuotesAdversarialTokenPaths(t *testing.T) {
	p := NewClaudeConfigProvisioner(t.TempDir())
	env := p.EnvForPane("proj", "1", `/tmp/x'; touch /tmp/pwned; echo '`)

	got := env.SecretAssignments[0]
	if strings.Contains(got, "touch /tmp/pwned;") && !strings.Contains(got, `'\''`) {
		t.Fatalf("token path was not shell-quoted: %q", got)
	}
	if !strings.HasPrefix(got, ClaudeOAuthTokenEnvVar+`="$(cat '`) {
		t.Fatalf("assignment lost its quoting: %q", got)
	}
}
