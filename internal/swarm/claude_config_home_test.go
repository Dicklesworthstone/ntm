package swarm

import (
	"os"
	"path/filepath"
	"testing"
)

// GH#237: the isolated config dir must inherit settings/MCP/skills while the
// rotating credential stays structurally unreachable — that combination is
// what stops N panes from invalidating each other's refresh token.
func TestProvisionPaneConfig_LinksConfigButNotCredentials(t *testing.T) {
	source := t.TempDir()
	base := t.TempDir()

	writeFile(t, filepath.Join(source, "settings.json"), `{"theme":"dark"}`)
	writeFile(t, filepath.Join(source, ".credentials.json"), `{"refresh_token":"rotating"}`)
	if err := os.MkdirAll(filepath.Join(source, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	writeFile(t, filepath.Join(source, "skills", "demo.md"), "skill")

	p := NewClaudeConfigProvisioner(base).WithSourceDir(source)
	configDir, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	// Settings and skills are readable through the isolated dir.
	if data, err := os.ReadFile(filepath.Join(configDir, "settings.json")); err != nil {
		t.Fatalf("settings.json unreadable from isolated dir: %v", err)
	} else if string(data) != `{"theme":"dark"}` {
		t.Fatalf("settings.json content = %q", data)
	}
	if _, err := os.ReadFile(filepath.Join(configDir, "skills", "demo.md")); err != nil {
		t.Fatalf("skills unreadable from isolated dir: %v", err)
	}

	// The rotating credential must not be reachable by ANY path.
	if _, err := os.Lstat(filepath.Join(configDir, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("rotating credential is reachable from the isolated config dir (err=%v)", err)
	}
	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if !isolated {
		t.Fatal("CredentialIsolated reported false for a freshly provisioned dir")
	}
}

// Panes must not share a directory, or they would share whatever credential
// state Claude Code writes into it.
func TestProvisionPaneConfig_PerPaneDirectories(t *testing.T) {
	source := t.TempDir()
	base := t.TempDir()
	p := NewClaudeConfigProvisioner(base).WithSourceDir(source)

	one, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("provision pane 1: %v", err)
	}
	two, err := p.ProvisionPaneConfig("proj", "2")
	if err != nil {
		t.Fatalf("provision pane 2: %v", err)
	}
	if one == two {
		t.Fatalf("panes share a config dir: %s", one)
	}
	// Keyed to the pane so --resume keeps finding the same history.
	if again, err := p.ProvisionPaneConfig("proj", "1"); err != nil || again != one {
		t.Fatalf("re-provision pane 1 = (%s, %v), want stable %s", again, err, one)
	}
}

// A credential appearing inside the isolated dir defeats the whole mechanism:
// Claude Code prefers the file over the static token and rejoins the race.
// Re-provisioning must remove it.
func TestProvisionPaneConfig_RemovesCredentialThatAppearedLater(t *testing.T) {
	source := t.TempDir()
	base := t.TempDir()
	p := NewClaudeConfigProvisioner(base).WithSourceDir(source)

	configDir, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	writeFile(t, filepath.Join(configDir, ".credentials.json"), `{"refresh_token":"leaked"}`)

	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if isolated {
		t.Fatal("CredentialIsolated reported true with a credential present")
	}

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	isolated, err = p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated after re-provision: %v", err)
	}
	if !isolated {
		t.Fatal("re-provisioning did not remove the credential")
	}
}

func TestEnvForPane_IncludesTokenOnlyWhenProvided(t *testing.T) {
	p := NewClaudeConfigProvisioner(t.TempDir())

	env := p.EnvForPane("proj", "1", "")
	if _, ok := env[ClaudeOAuthTokenEnvVar]; ok {
		t.Fatal("env carries an OAuth token variable with no token configured")
	}
	if env[ClaudeConfigDirEnvVar] == "" {
		t.Fatal("env is missing CLAUDE_CONFIG_DIR")
	}

	env = p.EnvForPane("proj", "1", "  sk-ant-oat-test  ")
	if got := env[ClaudeOAuthTokenEnvVar]; got != "sk-ant-oat-test" {
		t.Fatalf("token = %q, want the trimmed setup token", got)
	}
}

// A source config dir that does not exist yet is normal on a fresh machine and
// must still yield a valid, credential-free config dir.
func TestProvisionPaneConfig_MissingSourceIsNotAnError(t *testing.T) {
	base := t.TempDir()
	p := NewClaudeConfigProvisioner(base).WithSourceDir(filepath.Join(base, "absent"))

	configDir, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig with missing source: %v", err)
	}
	if _, err := os.Stat(configDir); err != nil {
		t.Fatalf("config dir not created: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
