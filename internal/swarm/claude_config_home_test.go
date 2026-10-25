package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
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

// useFileCredentialStore makes the isolation checks run on every platform.
// On macOS the store probe would otherwise consult the real login Keychain;
// declaring the file store is what an operator who authenticates panes with a
// setup token does. Elsewhere the variable is ignored.
func useFileCredentialStore(t *testing.T) {
	t.Helper()
	t.Setenv(ClaudeCredentialStoreEnvVar, "file")
}

// claudeHomeWithCredentialCopies builds a temp HOME whose ~/.claude holds
// ordinary config next to the rotating credential and the kinds of copies
// that accumulate beside it (GH#366).
func claudeHomeWithCredentialCopies(t *testing.T) (home, source string, credentials []string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	source = filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(source, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "settings.json"), `{"theme":"dark"}`)
	writeFile(t, filepath.Join(source, "CLAUDE.md"), "# rules")
	credentials = []string{
		".credentials.json",
		".credentials.json.bak-20261003",
		".credentials.json.tmp.4242",
		"..credentials.json.swp",
		".credentials.json~",
		"#.credentials.json#",
		".Credentials.JSON.old",
	}
	for _, name := range credentials {
		writeFile(t, filepath.Join(source, name), `{"claudeAiOauth":{"refreshToken":"rt-secret"}}`)
	}
	return home, source, credentials
}

func TestIsClaudeCredentialEntry(t *testing.T) {
	for _, name := range []string{
		".credentials.json",
		".credentials.json.bak-20261003",
		".credentials.json.tmp.4242",
		"..credentials.json.swp",
		".#.credentials.json",
		"#.credentials.json#",
		".credentials.json~",
		"credentials.json",
		".Credentials.JSON.BAK",
		".credentials",
		"old-credentials-backup.json",
	} {
		if !isClaudeCredentialEntry(name) {
			t.Errorf("isClaudeCredentialEntry(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"settings.json",
		"settings.local.json",
		"CLAUDE.md",
		"projects",
		"skills",
		"plugins",
		"history.jsonl",
		"statsig",
		".claude.json",
	} {
		if isClaudeCredentialEntry(name) {
			t.Errorf("isClaudeCredentialEntry(%q) = true, want false", name)
		}
	}
}

func TestProvisionPaneConfig_LinksNoCredentialCopies(t *testing.T) {
	useFileCredentialStore(t)
	_, source, credentials := claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	for _, name := range credentials {
		if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Errorf("credential copy %q is reachable from the pane config dir (lstat err=%v)", name, err)
		}
	}
	for _, name := range []string{"settings.json", "CLAUDE.md", "skills"} {
		dest, err := os.Readlink(filepath.Join(target, name))
		if err != nil {
			t.Errorf("config entry %q was not linked: %v", name, err)
			continue
		}
		if want := filepath.Join(source, name); dest != want {
			t.Errorf("link %q -> %q, want %q", name, dest, want)
		}
	}

	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if !isolated {
		t.Fatal("CredentialIsolated = false for a dir provisioned without credential copies")
	}
}

// Pane dirs provisioned by an ntm that excluded only the exact name already
// hold a link to the backup; re-provisioning must take it away, along with any
// credential file the pane wrote itself, while leaving the pane's other files
// alone.
func TestProvisionPaneConfig_RemovesCredentialCopiesFromAnExistingPaneDir(t *testing.T) {
	useFileCredentialStore(t)
	_, source, _ := claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	staleLink := filepath.Join(target, ".credentials.json.bak-20261003")
	if err := os.Symlink(filepath.Join(source, ".credentials.json.bak-20261003"), staleLink); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, ".credentials.json.tmp.99"), `{"claudeAiOauth":{}}`)
	writeFile(t, filepath.Join(target, "pane-notes.txt"), "keep me")

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	for _, name := range []string{".credentials.json.bak-20261003", ".credentials.json.tmp.99"} {
		if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Errorf("%q survived re-provisioning (lstat err=%v)", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(target, "pane-notes.txt")); err != nil || string(data) != "keep me" {
		t.Errorf("a pane-created non-credential file was disturbed: data=%q err=%v", data, err)
	}
}

func TestCredentialIsolated_DetectsACredentialCopy(t *testing.T) {
	useFileCredentialStore(t)
	_, source, _ := claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	// The check used to look only for the exact name, so a linked backup
	// passed as "no credential file is reachable".
	if err := os.Symlink(filepath.Join(source, ".credentials.json.bak-20261003"),
		filepath.Join(target, ".credentials.json.bak-20261003")); err != nil {
		t.Fatal(err)
	}

	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if isolated {
		t.Fatal("CredentialIsolated = true while a credential backup is linked into the pane config dir")
	}
}

func TestProvisionPaneConfig_RefusesACredentialNamedDirectory(t *testing.T) {
	useFileCredentialStore(t)
	claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	credentialDir := filepath.Join(target, ".credentials.d")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(credentialDir, "token.json"), "{}")

	_, err := p.ProvisionPaneConfig("proj", "1")
	if err == nil || !strings.Contains(err.Error(), ".credentials.d") {
		t.Fatalf("ProvisionPaneConfig error = %v, want a refusal naming .credentials.d", err)
	}
	if _, err := os.Stat(filepath.Join(credentialDir, "token.json")); err != nil {
		t.Fatalf("the credential directory was modified instead of refused: %v", err)
	}
}

// End to end through the entry point every launch path uses, with the
// operator's config in a temp HOME.
func TestProvisionClaudeIsolation_KeepsCredentialCopiesOutOfThePane(t *testing.T) {
	useFileCredentialStore(t)
	home, _, credentials := claudeHomeWithCredentialCopies(t)
	tokenFile := filepath.Join(home, "claude-setup.token")
	writeFile(t, tokenFile, "sk-ant-oat-test")

	cfg := config.Default()
	cfg.Agents.ClaudeIsolateCredentials = true
	cfg.Agents.ClaudeTokenFile = tokenFile

	env, err := ProvisionClaudeIsolation(cfg, t.TempDir(), "proj", 2)
	if err != nil {
		t.Fatalf("ProvisionClaudeIsolation: %v", err)
	}
	configDir := env.Vars[ClaudeConfigDirEnvVar]
	if configDir == "" {
		t.Fatal("launch env has no CLAUDE_CONFIG_DIR")
	}
	for _, name := range credentials {
		if _, err := os.Lstat(filepath.Join(configDir, name)); !os.IsNotExist(err) {
			t.Errorf("credential copy %q is reachable from the pane config dir (lstat err=%v)", name, err)
		}
	}
	if _, err := os.Readlink(filepath.Join(configDir, "settings.json")); err != nil {
		t.Errorf("settings.json was not linked into the pane config dir: %v", err)
	}
	if len(env.SecretAssignments) != 1 {
		t.Errorf("secret assignments = %v, want the setup-token assignment", env.SecretAssignments)
	}
}

// operatorClaudeHome builds a temp HOME whose ~/.claude holds the rotating
// credential, a backup of it, and an entry that is itself a symlink into the
// operator's dotfiles, plus a credential file directly in HOME. It returns the
// paths that provisioning must never touch.
func operatorClaudeHome(t *testing.T) (home, source string, mustSurvive []string, dotfileLink string) {
	t.Helper()
	home, source, credentials := claudeHomeWithCredentialCopies(t)
	for _, name := range credentials {
		mustSurvive = append(mustSurvive, filepath.Join(source, name))
	}
	gitCredentials := filepath.Join(home, ".git-credentials")
	writeFile(t, gitCredentials, "https://user:token@example.com")
	mustSurvive = append(mustSurvive, gitCredentials)

	dotfiles := filepath.Join(home, "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dotfiles, "keybindings.json"), "[]")
	dotfileLink = filepath.Join(source, "keybindings.json")
	if err := os.Symlink(filepath.Join(dotfiles, "keybindings.json"), dotfileLink); err != nil {
		t.Fatal(err)
	}
	return home, source, mustSurvive, dotfileLink
}

func assertOperatorConfigUntouched(t *testing.T, mustSurvive []string, dotfileLink string) {
	t.Helper()
	for _, path := range mustSurvive {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("provisioning deleted the operator's %s: %v", path, err)
		}
	}
	dest, err := os.Readlink(dotfileLink)
	if err != nil || filepath.Base(filepath.Dir(dest)) != "dotfiles" {
		t.Errorf("provisioning rewrote the operator's own link %s (now -> %q, err=%v)", dotfileLink, dest, err)
	}
}

// Provisioning deletes credential entries inside the pane dir and replaces
// symlinks there. If the pane dir is a symlink, those deletions land wherever
// it points: pointed at ~/.claude (say, to let one pane share the login) it
// would delete the operator's real credential and its backups and replace the
// operator's own config links with self-referential ones. Pointed at HOME it
// would delete ~/.git-credentials. A symlinked pane dir is refused untouched.
func TestProvisionPaneConfig_RefusesASymlinkedPaneDir(t *testing.T) {
	useFileCredentialStore(t)
	for _, pointAt := range []string{"claude-config", "home"} {
		t.Run(pointAt, func(t *testing.T) {
			home, source, mustSurvive, dotfileLink := operatorClaudeHome(t)
			dest := source
			if pointAt == "home" {
				dest = home
			}

			p := NewClaudeConfigProvisioner(t.TempDir())
			target := p.ConfigPath("proj", "1")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dest, target); err != nil {
				t.Fatal(err)
			}

			_, err := p.ProvisionPaneConfig("proj", "1")
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Errorf("ProvisionPaneConfig error = %v, want a refusal of the symlinked pane dir", err)
			}
			assertOperatorConfigUntouched(t, mustSurvive, dotfileLink)

			_, err = ProvisionClaudeIsolation(&config.Config{Agents: config.AgentConfig{ClaudeIsolateCredentials: true}}, p.BaseDir, "proj", 1)
			if err == nil {
				t.Error("ProvisionClaudeIsolation accepted a pane dir symlinked to operator files")
			}
			assertOperatorConfigUntouched(t, mustSurvive, dotfileLink)
		})
	}
}

// The same holds when the pane dir is a real directory but the operator's
// config dir resolves to it (here ~/.claude is a symlink to the pane dir):
// the pane dir's entries are then the operator's own files.
func TestProvisionPaneConfig_RefusesAPaneDirThatIsTheOperatorConfig(t *testing.T) {
	useFileCredentialStore(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(target, ".credentials.json")
	writeFile(t, credential, `{"claudeAiOauth":{"refreshToken":"rt-secret"}}`)
	if err := os.Symlink(target, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}

	if _, err := p.ProvisionPaneConfig("proj", "1"); err == nil {
		t.Error("ProvisionPaneConfig accepted a pane dir that is the operator's config dir")
	}
	if _, err := os.Lstat(credential); err != nil {
		t.Errorf("provisioning deleted the operator's credential: %v", err)
	}
}

// A symlink higher up (a project whose .ntm is linked to shared storage) is
// ordinary and must keep working: only the pane dir itself, or a pane dir that
// resolves onto the operator's config, is refused.
func TestProvisionPaneConfig_AllowsASymlinkedNtmDir(t *testing.T) {
	useFileCredentialStore(t)
	_, _, credentials := claudeHomeWithCredentialCopies(t)

	project := t.TempDir()
	shared := t.TempDir()
	if err := os.Symlink(shared, filepath.Join(project, ".ntm")); err != nil {
		t.Fatal(err)
	}
	p := NewClaudeConfigProvisioner(project)
	target, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig through a symlinked .ntm: %v", err)
	}
	if _, err := os.Readlink(filepath.Join(shared, "claude-homes", "proj", "1", "settings.json")); err != nil {
		t.Errorf("settings.json was not linked into the pane dir under the shared .ntm: %v", err)
	}
	for _, name := range credentials {
		if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Errorf("credential copy %q is reachable from the pane config dir (lstat err=%v)", name, err)
		}
	}
}

// projectWhosePaneDirIsHome builds a temp HOME named like a pane index and a
// project whose claude-homes/<session> is a symlink to HOME's parent, so the
// pane dir is HOME itself while not being a symlink. HOME holds a
// .git-credentials file, and ~/.claude exists only if withClaudeDir is set.
func projectWhosePaneDirIsHome(t *testing.T, withClaudeDir bool) (p *ClaudeConfigProvisioner, homeParent, gitCredentials string) {
	t.Helper()
	homeParent = t.TempDir()
	home := filepath.Join(homeParent, "1")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	gitCredentials = filepath.Join(home, ".git-credentials")
	writeFile(t, gitCredentials, "https://user:token@example.com")
	if withClaudeDir {
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"refreshToken":"rt-secret"}}`)
	}
	p = NewClaudeConfigProvisioner(t.TempDir())
	sessionDir := filepath.Dir(p.ConfigPath("proj", "1"))
	if err := os.MkdirAll(filepath.Dir(sessionDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(homeParent, sessionDir); err != nil {
		t.Fatal(err)
	}
	return p, homeParent, gitCredentials
}

// The overlap check used to compare resolved path names and skipped itself
// when ~/.claude did not exist, so a pane dir that led to HOME through a
// symlinked parent lost ~/.git-credentials whenever ~/.claude was missing.
func TestProvisionPaneConfig_RefusesAPaneDirThatIsHomeWithoutAClaudeDir(t *testing.T) {
	useFileCredentialStore(t)
	p, _, gitCredentials := projectWhosePaneDirIsHome(t, false)

	if _, err := p.ProvisionPaneConfig("proj", "1"); err == nil {
		t.Error("ProvisionPaneConfig accepted a pane dir that is HOME")
	}
	if _, err := os.Lstat(gitCredentials); err != nil {
		t.Errorf("provisioning deleted %s: %v", gitCredentials, err)
	}
}

// On a case-insensitive filesystem HOME may be spelled in another case than
// the path the pane dir resolves to. Names then differ while the directories
// are the same, so the check must compare the directories themselves.
func TestProvisionPaneConfig_RefusesAPaneDirThatIsHomeSpelledInAnotherCase(t *testing.T) {
	useFileCredentialStore(t)
	p, homeParent, gitCredentials := projectWhosePaneDirIsHome(t, true)
	upper := strings.ToUpper(homeParent)
	if upper == homeParent {
		t.Skip("temp dir has no letters to recase")
	}
	if _, err := os.Stat(upper); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	t.Setenv("HOME", filepath.Join(upper, "1"))
	credential := filepath.Join(homeParent, "1", ".claude", ".credentials.json")

	if _, err := p.ProvisionPaneConfig("proj", "1"); err == nil {
		t.Error("ProvisionPaneConfig accepted a pane dir that is HOME spelled in another case")
	}
	for _, path := range []string{gitCredentials, credential} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("provisioning deleted %s: %v", path, err)
		}
	}
}

// Once the pane dir has been checked, swapping its path for a link to the
// operator's config must not redirect a single deletion or relink: they all
// act on the directory that was checked.
func TestProvisionPaneConfig_ActsOnTheCheckedDirEvenIfThePathIsSwapped(t *testing.T) {
	useFileCredentialStore(t)
	_, source, mustSurvive, dotfileLink := operatorClaudeHome(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, ".credentials.json"), `{"claudeAiOauth":{"refreshToken":"pane-login"}}`)
	moved := target + ".checked"

	testHookPaneConfigDirOpened = func(string) {
		if err := os.Rename(target, moved); err != nil {
			t.Errorf("move pane dir: %v", err)
			return
		}
		if err := os.Symlink(source, target); err != nil {
			t.Errorf("swap in link: %v", err)
		}
	}
	t.Cleanup(func() { testHookPaneConfigDirOpened = nil })

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	assertOperatorConfigUntouched(t, mustSurvive, dotfileLink)
	if _, err := os.Lstat(filepath.Join(moved, ".credentials.json")); !os.IsNotExist(err) {
		t.Errorf("the checked pane dir still holds its credential (lstat err=%v)", err)
	}
	if dest, err := os.Readlink(filepath.Join(moved, "settings.json")); err != nil || dest != filepath.Join(source, "settings.json") {
		t.Errorf("the checked pane dir was not provisioned: settings.json -> %q, err=%v", dest, err)
	}
}

// A credential hard-linked into the pane dir is the operator's file under a
// second name. Removing the pane's name must leave the operator's file whole.
func TestProvisionPaneConfig_UnlinksOnlyThePaneNameOfAHardLinkedCredential(t *testing.T) {
	useFileCredentialStore(t)
	_, source, _ := claudeHomeWithCredentialCopies(t)
	credential := filepath.Join(source, ".credentials.json")
	want, err := os.ReadFile(credential)
	if err != nil {
		t.Fatal(err)
	}

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(credential, filepath.Join(target, ".credentials.json")); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".credentials.json")); !os.IsNotExist(err) {
		t.Errorf("the hard-linked credential is still reachable from the pane dir (lstat err=%v)", err)
	}
	if got, err := os.ReadFile(credential); err != nil || string(got) != string(want) {
		t.Errorf("the operator's credential changed: %q, err=%v", got, err)
	}
}
