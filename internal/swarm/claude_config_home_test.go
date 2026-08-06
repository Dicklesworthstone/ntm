package swarm

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	requireIsolableCredentialStore(t)
	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if !isolated {
		t.Fatal("CredentialIsolated reported false for a freshly provisioned dir")
	}
}

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

// The whole value of CredentialIsolated is that it verifies rather than
// assumes. On a platform where the credential is not in the config dir at all,
// answering true would be the most dangerous possible response: it reports a
// protection that is not in force.
func TestCredentialIsolated_FailsClosedWhenTheStoreIsNotIsolable(t *testing.T) {
	storeErr := credentialStoreIsolable()
	if storeErr == nil {
		if runtime.GOOS == "darwin" {
			t.Log("no Claude Keychain item on this machine; the file mechanism is the one in play")
		}
		t.Skip("credential store is isolable here; nothing to fail closed about")
	}

	if !errors.Is(storeErr, ErrCredentialStoreNotIsolable) {
		t.Fatalf("store error = %v, want it to wrap ErrCredentialStoreNotIsolable", storeErr)
	}

	p := NewClaudeConfigProvisioner(t.TempDir()).WithSourceDir(t.TempDir())
	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	isolated, err := p.CredentialIsolated("proj", "1")
	if isolated {
		t.Fatal("CredentialIsolated reported true on a platform where isolation cannot be enforced")
	}
	if !errors.Is(err, ErrCredentialStoreNotIsolable) {
		t.Fatalf("error = %v, want ErrCredentialStoreNotIsolable so spawn refuses instead of claiming success", err)
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

	requireIsolableCredentialStore(t)
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

// The pane command is typed into a shell with send-keys, so anything in it
// lands in the pane's scrollback (which ntm captures and exports), in the argv
// of both the shell and the send-keys call (visible to any local process via
// ps), and in runLocalContext's error strings on a failed send. A long-lived,
// non-rotating OAuth token must therefore never appear in it by value.
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

// A token path containing a single quote must not be able to break out of the
// quoting and inject shell into the pane command.
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

// A relative source path is valid API input, but it must be made absolute
// before it becomes a symlink target. Otherwise each link resolves relative to
// the pane-private config directory rather than the caller's working directory.
func TestProvisionPaneConfig_RelativeSourceDirLinksToOperatorConfig(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)

	const source = "operator-claude"
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("mkdir source config: %v", err)
	}
	writeFile(t, filepath.Join(source, "settings.json"), `{"theme":"dark"}`)

	p := NewClaudeConfigProvisioner(t.TempDir()).WithSourceDir(source)
	configDir, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	settings := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("settings linked from relative source are unreadable: %v", err)
	}
	if string(data) != `{"theme":"dark"}` {
		t.Fatalf("settings content = %q", data)
	}
	linkTarget, err := os.Readlink(settings)
	if err != nil {
		t.Fatalf("settings is not a symlink: %v", err)
	}
	wantTarget := filepath.Join(workDir, source, "settings.json")
	if linkTarget != wantTarget {
		t.Fatalf("link target = %q, want absolute %q", linkTarget, wantTarget)
	}
}

// An entry removed from ~/.claude used to leave a broken symlink in every pane
// dir forever: nothing revisited it, so Claude Code saw a name that resolved to
// nothing rather than a name that was absent.
func TestProvisionPaneConfig_PrunesLinksForRemovedSourceEntries(t *testing.T) {
	source := t.TempDir()
	base := t.TempDir()
	writeFile(t, filepath.Join(source, "settings.json"), `{"theme":"dark"}`)
	writeFile(t, filepath.Join(source, "obsolete.json"), `{}`)

	p := NewClaudeConfigProvisioner(base).WithSourceDir(source)
	configDir, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(configDir, "obsolete.json")); err != nil {
		t.Fatalf("obsolete.json was not linked in the first place: %v", err)
	}

	// The operator removes the entry from their real config dir.
	if err := os.Remove(filepath.Join(source, "obsolete.json")); err != nil {
		t.Fatalf("remove source entry: %v", err)
	}
	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("re-provision: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(configDir, "obsolete.json")); !os.IsNotExist(err) {
		t.Fatalf("dangling link survived re-provision (err=%v)", err)
	}
	// The still-present entry must be untouched.
	if _, err := os.ReadFile(filepath.Join(configDir, "settings.json")); err != nil {
		t.Fatalf("live entry was pruned too: %v", err)
	}
}

// Files the pane created itself are not this provisioner's to reclaim.
func TestProvisionPaneConfig_PruneLeavesPaneOwnedFilesAlone(t *testing.T) {
	source := t.TempDir()
	base := t.TempDir()

	p := NewClaudeConfigProvisioner(base).WithSourceDir(source)
	configDir, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	paneOwned := filepath.Join(configDir, "pane-scratch.json")
	writeFile(t, paneOwned, `{"written":"by the pane"}`)
	// A symlink the pane made to somewhere else entirely.
	elsewhere := filepath.Join(t.TempDir(), "external.json")
	writeFile(t, elsewhere, `{}`)
	if err := os.Symlink(elsewhere, filepath.Join(configDir, "external.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("re-provision: %v", err)
	}

	if _, err := os.ReadFile(paneOwned); err != nil {
		t.Fatalf("pane-owned file was removed by prune: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(configDir, "external.json")); err != nil {
		t.Fatalf("pane-owned link to an unrelated target was pruned: %v", err)
	}
}

// A tool that writes atomically (tmp + rename) replaces the symlink with a real
// file, silently converting it into a point-in-time copy. Re-provisioning must
// not clobber the pane's file, but it must not pretend the link is still live
// either — the operator has to be able to find out.
func TestProvisionPaneConfig_KeepsDivergedEntryWithoutRelinking(t *testing.T) {
	source := t.TempDir()
	base := t.TempDir()
	writeFile(t, filepath.Join(source, "settings.json"), `{"theme":"dark"}`)

	p := NewClaudeConfigProvisioner(base).WithSourceDir(source)
	configDir, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	// Simulate the atomic rename: link becomes a regular file.
	diverged := filepath.Join(configDir, "settings.json")
	if err := os.Remove(diverged); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	writeFile(t, diverged, `{"theme":"pane-local"}`)

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("re-provision over a diverged entry: %v", err)
	}

	data, err := os.ReadFile(diverged)
	if err != nil {
		t.Fatalf("diverged entry unreadable: %v", err)
	}
	if string(data) != `{"theme":"pane-local"}` {
		t.Fatalf("pane-written content was clobbered: %q", data)
	}
	info, err := os.Lstat(diverged)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("diverged entry was silently relinked, discarding the pane's own file")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
