package swarm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
	// literal text "$(/bin/cat ...)" as its token.
	want := ClaudeOAuthTokenEnvVar + `="$(/bin/cat '` + tokenFile + `')"`
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
	if !strings.HasPrefix(got, ClaudeOAuthTokenEnvVar+`="$(/bin/cat '`) {
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
// hold a link to the backup; re-provisioning must take the links away while
// leaving what they point to, and the pane's other files, alone.
func TestProvisionPaneConfig_RemovesCredentialLinksFromAnExistingPaneDir(t *testing.T) {
	useFileCredentialStore(t)
	_, source, _ := claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	links := []string{".credentials.json.bak-20261003", ".credentials.json"}
	for _, name := range links {
		if err := os.Symlink(filepath.Join(source, name), filepath.Join(target, name)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(target, "pane-notes.txt"), "keep me")

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}

	for _, name := range links {
		if _, err := os.Lstat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Errorf("credential link %q survived re-provisioning (lstat err=%v)", name, err)
		}
		if data, err := os.ReadFile(filepath.Join(source, name)); err != nil || !strings.Contains(string(data), "rt-secret") {
			t.Errorf("removing the link disturbed the operator's %s: data=%q err=%v", name, data, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(target, "pane-notes.txt")); err != nil || string(data) != "keep me" {
		t.Errorf("a pane-created non-credential file was disturbed: data=%q err=%v", data, err)
	}
	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if !isolated {
		t.Error("CredentialIsolated = false after the credential links were removed")
	}
}

// A real credential file inside a pane dir is never ntm's: ntm only ever
// creates links. The usual source is a login inside the pane, which Claude
// Code writes to CLAUDE_CONFIG_DIR/.credentials.json with its own refresh
// token, so deleting it logs that pane out (and a copy the pane has already
// refreshed holds the only live token of its login). Re-provisioning keeps
// every such file byte for byte and still provisions the rest of the dir.
// Only the pane's own .credentials.json is accepted; every other
// credential-named file stops the launch with an error that names it.
func TestProvisionPaneConfig_KeepsAndReportsRealCredentialFiles(t *testing.T) {
	useFileCredentialStore(t)
	claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	ownLogin := map[string]string{".credentials.json": paneOwnLoginContent}
	reported := map[string]string{
		".credentials.json.tmp.99":   `{"claudeAiOauth":{"refreshToken":"pane-refresh-in-flight"}}`,
		".credentials.json.bak-1024": `{"claudeAiOauth":{"refreshToken":"pane-backup"}}`,
		"credential-helper.sh":       "#!/bin/sh\necho not a claude credential\n",
	}
	files := map[string]string{}
	for _, group := range []map[string]string{ownLogin, reported} {
		for name, content := range group {
			files[name] = content
			writeFile(t, filepath.Join(target, name), content)
		}
	}

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(target, name)); err != nil || string(got) != want {
			t.Errorf("re-provisioning changed the pane's %s: got %q, err=%v", name, got, err)
		}
	}
	if _, err := os.Readlink(filepath.Join(target, "settings.json")); err != nil {
		t.Errorf("provisioning stopped instead of linking the rest of the dir: %v", err)
	}

	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if isolated {
		t.Error("CredentialIsolated = true while real credential files are in the pane dir")
	}

	cfg := &config.Config{Agents: config.AgentConfig{ClaudeIsolateCredentials: true}}
	_, err = ProvisionClaudeIsolation(cfg, p.BaseDir, "proj", 1)
	if err == nil {
		t.Fatal("ProvisionClaudeIsolation launched a pane whose config dir holds real credential files")
	}
	for name := range reported {
		if !strings.Contains(err.Error(), name+" (file)") {
			t.Errorf("launch error does not name %s as a file: %v", name, err)
		}
	}
	for name := range ownLogin {
		if strings.Contains(err.Error(), name+" (file)") {
			t.Errorf("launch error reports the pane's own login %s as exposed: %v", name, err)
		}
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(target, name)); err != nil || string(got) != want {
			t.Errorf("the launch path changed the pane's %s: got %q, err=%v", name, got, err)
		}
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

func TestProvisionPaneConfig_KeepsAndReportsACredentialNamedDirectory(t *testing.T) {
	useFileCredentialStore(t)
	claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	credentialDir := filepath.Join(target, ".credentials.d")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(credentialDir, "token.json"), "{}")

	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(credentialDir, "token.json")); err != nil {
		t.Fatalf("the credential directory was modified: %v", err)
	}
	_, err := ProvisionClaudeIsolation(&config.Config{Agents: config.AgentConfig{ClaudeIsolateCredentials: true}}, p.BaseDir, "proj", 1)
	if err == nil || !strings.Contains(err.Error(), ".credentials.d (directory)") {
		t.Fatalf("ProvisionClaudeIsolation error = %v, want a refusal naming .credentials.d", err)
	}
	if _, err := os.Stat(filepath.Join(credentialDir, "token.json")); err != nil {
		t.Fatalf("the credential directory was modified by the launch path: %v", err)
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
	const backup = ".credentials.json.bak-20261003"
	if err := os.Symlink(filepath.Join(source, backup), filepath.Join(target, backup)); err != nil {
		t.Fatal(err)
	}
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
	if _, err := os.Lstat(filepath.Join(moved, backup)); !os.IsNotExist(err) {
		t.Errorf("the checked pane dir still holds its credential link (lstat err=%v)", err)
	}
	if dest, err := os.Readlink(filepath.Join(moved, "settings.json")); err != nil || dest != filepath.Join(source, "settings.json") {
		t.Errorf("the checked pane dir was not provisioned: settings.json -> %q, err=%v", dest, err)
	}
}

// A credential hard-linked into the pane dir is the operator's file under a
// second name. It is a regular file, so it is kept and reported like any
// other, and the operator's file stays whole.
func TestProvisionPaneConfig_KeepsAndReportsAHardLinkedCredential(t *testing.T) {
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
	if got, err := os.ReadFile(credential); err != nil || string(got) != string(want) {
		t.Errorf("the operator's credential changed: %q, err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".credentials.json")); err != nil {
		t.Errorf("the hard-linked credential was removed from the pane dir: %v", err)
	}
	isolated, err := p.CredentialIsolated("proj", "1")
	if err != nil {
		t.Fatalf("CredentialIsolated: %v", err)
	}
	if isolated {
		t.Error("CredentialIsolated = true while the operator's credential is hard-linked into the pane dir")
	}
}

// paneOwnLoginContent stands for what a login inside a pane writes: a
// credential whose refresh token appears nowhere in the operator's config dir.
const paneOwnLoginContent = `{"claudeAiOauth":{"refreshToken":"pane-own-login"}}`

func isolationConfig(tokenFile string) *config.Config {
	return &config.Config{Agents: config.AgentConfig{ClaudeIsolateCredentials: true, ClaudeTokenFile: tokenFile}}
}

// With no claude_token_file a pane authenticates by logging in inside it, and
// Claude Code writes that login to CLAUDE_CONFIG_DIR/.credentials.json. Its
// refresh-token chain is the pane's alone, so the pane is isolated. Every
// restart used to stop on it; it must survive re-provisioning byte for byte
// and the launch must go ahead, restart after restart.
func TestProvisionClaudeIsolation_LaunchesWithThePanesOwnLogin(t *testing.T) {
	useFileCredentialStore(t)
	claudeHomeWithCredentialCopies(t)
	project := t.TempDir()
	cfg := isolationConfig("")

	env, err := ProvisionClaudeIsolation(cfg, project, "proj", 1)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	configDir := env.Vars[ClaudeConfigDirEnvVar]
	login := filepath.Join(configDir, ".credentials.json")
	writeFile(t, login, paneOwnLoginContent) // the user logs in inside the pane

	for restart := 1; restart <= 2; restart++ {
		env, err := ProvisionClaudeIsolation(cfg, project, "proj", 1)
		if err != nil {
			t.Fatalf("restart %d refused the pane's own login: %v", restart, err)
		}
		if env.Vars[ClaudeConfigDirEnvVar] != configDir || len(env.SecretAssignments) != 0 {
			t.Fatalf("restart %d launch env = %+v, want CLAUDE_CONFIG_DIR=%s and no token", restart, env, configDir)
		}
		if got, err := os.ReadFile(login); err != nil || string(got) != paneOwnLoginContent {
			t.Fatalf("restart %d changed the pane's login: %q, err=%v", restart, got, err)
		}
	}
	if _, err := os.Readlink(filepath.Join(configDir, "settings.json")); err != nil {
		t.Errorf("settings.json is not linked next to the pane's login: %v", err)
	}
	isolated, err := NewClaudeConfigProvisioner(project).CredentialIsolated("proj", "1")
	if err != nil || !isolated {
		t.Errorf("CredentialIsolated = %v, %v for a pane holding only its own login", isolated, err)
	}
}

// Claude Code prefers a .credentials.json over CLAUDE_CODE_OAUTH_TOKEN, so
// with a setup token configured the pane's own login would silently replace
// the credential the operator chose. The launch stops and says which file
// overrides which setting, and nothing is deleted.
func TestProvisionClaudeIsolation_RefusesThePanesOwnLoginWhenATokenFileIsConfigured(t *testing.T) {
	useFileCredentialStore(t)
	home, _, _ := claudeHomeWithCredentialCopies(t)
	tokenFile := filepath.Join(home, "claude-setup.token")
	writeFile(t, tokenFile, "sk-ant-oat-test")
	project := t.TempDir()
	p := NewClaudeConfigProvisioner(project)
	configDir := p.ConfigPath("proj", "3")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	login := filepath.Join(configDir, ".credentials.json")
	writeFile(t, login, paneOwnLoginContent)

	_, err := ProvisionClaudeIsolation(isolationConfig(tokenFile), project, "proj", 3)
	if err == nil {
		t.Fatal("ProvisionClaudeIsolation launched with a setup token that the pane's own login overrides")
	}
	for _, want := range []string{"pane 3", "session proj", login, "claude_token_file", tokenFile} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("launch error does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "pane-own-login") || strings.Contains(err.Error(), "sk-ant-oat-test") {
		t.Errorf("launch error leaks credential contents: %v", err)
	}
	if got, err := os.ReadFile(login); err != nil || string(got) != paneOwnLoginContent {
		t.Errorf("the refused launch changed the pane's login: %q, err=%v", got, err)
	}
	isolated, err := p.CredentialIsolated("proj", "3")
	if err != nil || !isolated {
		t.Errorf("CredentialIsolated = %v, %v; the pane's own login is isolated even when the launch is refused", isolated, err)
	}
}

// A .credentials.json that is the operator's credential under another name
// shares its refresh token and rejoins the rotation race even though it is a
// regular file in the pane dir. A hard link is one file with two names; a copy
// holds the same token, whether byte for byte or reformatted; an operator
// credential entry that links to the pane's file makes the operator refresh it
// too; and a hard link or copy of another pane's login puts two panes on one
// refresh chain. Each is refused, named, kept, and never quoted.
func TestProvisionClaudeIsolation_RefusesACredentialsJSONSharedWithAnotherCredential(t *testing.T) {
	useFileCredentialStore(t)
	cases := []struct {
		name  string
		place func(t *testing.T, source string, p *ClaudeConfigProvisioner, login string)
	}{
		{"hard link to the operator credential", func(t *testing.T, source string, _ *ClaudeConfigProvisioner, login string) {
			if err := os.Link(filepath.Join(source, ".credentials.json"), login); err != nil {
				t.Skipf("hard links unsupported here: %v", err)
			}
		}},
		{"byte copy of the operator credential", func(t *testing.T, source string, _ *ClaudeConfigProvisioner, login string) {
			data, err := os.ReadFile(filepath.Join(source, ".credentials.json"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, login, string(data))
		}},
		{"reformatted copy of the operator credential", func(t *testing.T, _ string, _ *ClaudeConfigProvisioner, login string) {
			writeFile(t, login, "{\n  \"claudeAiOauth\": {\n    \"refreshToken\": \"rt-secret\"\n  }\n}\n")
		}},
		{"copy of the operator credential with a trailing newline", func(t *testing.T, source string, _ *ClaudeConfigProvisioner, login string) {
			data, err := os.ReadFile(filepath.Join(source, ".credentials.json"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, login, string(data)+"\n")
		}},
		{"byte copy of an operator credential backup", func(t *testing.T, source string, _ *ClaudeConfigProvisioner, login string) {
			const backup = `{"claudeAiOauth":{"refreshToken":"rt-backup-only"}}`
			writeFile(t, filepath.Join(source, ".credentials.json.bak-20261003"), backup)
			writeFile(t, login, backup)
		}},
		{"operator credential entry linked to the pane's file", func(t *testing.T, source string, _ *ClaudeConfigProvisioner, login string) {
			// One file, one link, but the operator reads and refreshes it
			// through ~/.claude too. The source entry must be followed.
			writeFile(t, login, `{"claudeAiOauth":{"refreshToken":"rt-shared-through-link"}}`)
			if err := os.Symlink(login, filepath.Join(source, ".credentials.json.pane")); err != nil {
				t.Fatal(err)
			}
		}},
		{"hard link to another pane's login", func(t *testing.T, _ string, p *ClaudeConfigProvisioner, login string) {
			other := p.ConfigPath("proj", "2")
			if err := os.MkdirAll(other, 0o700); err != nil {
				t.Fatal(err)
			}
			otherLogin := filepath.Join(other, ".credentials.json")
			writeFile(t, otherLogin, `{"claudeAiOauth":{"refreshToken":"rt-pane-two"}}`)
			if err := os.Link(otherLogin, login); err != nil {
				t.Skipf("hard links unsupported here: %v", err)
			}
		}},
		{"reformatted copy of another pane's login", func(t *testing.T, _ string, p *ClaudeConfigProvisioner, login string) {
			other := p.ConfigPath("other-session", "4")
			if err := os.MkdirAll(other, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(other, ".credentials.json"), `{"claudeAiOauth":{"refreshToken":"rt-pane-two"}}`)
			writeFile(t, login, `{ "claudeAiOauth": { "refreshToken": "rt-pane-two" } }`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, source, credentials := claudeHomeWithCredentialCopies(t)
			p := NewClaudeConfigProvisioner(t.TempDir())
			configDir := p.ConfigPath("proj", "1")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			login := filepath.Join(configDir, ".credentials.json")
			tc.place(t, source, p, login)
			paths := []string{login}
			for _, name := range credentials {
				paths = append(paths, filepath.Join(source, name))
			}
			before := map[string]string{}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = string(data)
			}

			_, err := ProvisionClaudeIsolation(isolationConfig(""), p.BaseDir, "proj", 1)
			if err == nil || !strings.Contains(err.Error(), ".credentials.json (file)") {
				t.Fatalf("ProvisionClaudeIsolation error = %v, want a refusal naming .credentials.json", err)
			}
			for _, secret := range []string{"rt-secret", "rt-backup-only", "rt-shared-through-link", "rt-pane-two"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("launch error leaks credential contents (%s): %v", secret, err)
				}
			}
			isolated, err := p.CredentialIsolated("proj", "1")
			if err != nil || isolated {
				t.Errorf("CredentialIsolated = %v, %v, want false", isolated, err)
			}
			for path, want := range before {
				if got, err := os.ReadFile(path); err != nil || string(got) != want {
					t.Errorf("%s changed: %q, err=%v", path, got, err)
				}
			}
		})
	}
}

// A login copied from one pane into another puts both on its refresh chain, so
// neither counts as isolated until one copy is moved out. A pane holding a
// login of its own is unaffected by the others.
func TestCredentialIsolated_ACopiedPaneLoginIsRefusedInBothPanes(t *testing.T) {
	useFileCredentialStore(t)
	claudeHomeWithCredentialCopies(t)
	p := NewClaudeConfigProvisioner(t.TempDir())
	for pane, login := range map[string]string{
		"1": `{"claudeAiOauth":{"refreshToken":"rt-copied"}}`,
		"2": "{\n  \"claudeAiOauth\": {\"refreshToken\": \"rt-copied\"}\n}\n",
		"3": paneOwnLoginContent,
	} {
		dir := p.ConfigPath("proj", pane)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, ".credentials.json"), login)
	}
	for pane, want := range map[string]bool{"1": false, "2": false, "3": true} {
		isolated, err := p.CredentialIsolated("proj", pane)
		if err != nil || isolated != want {
			t.Errorf("pane %s: CredentialIsolated = %v, %v, want %v", pane, isolated, err, want)
		}
	}
}

// When an operator credential cannot be read, ntm cannot tell whether the
// pane's file is a copy of it. Unverified is not isolated: the launch stops
// and names what could not be checked, and the pane's file is kept.
func TestProvisionClaudeIsolation_RefusesAnOwnLoginItCannotVerify(t *testing.T) {
	useFileCredentialStore(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of their mode")
	}
	_, source, _ := claudeHomeWithCredentialCopies(t)
	// Same size as the pane's file, so only its bytes could tell them apart.
	unreadable := filepath.Join(source, ".credentials.json.bak-locked")
	writeFile(t, unreadable, strings.Repeat("x", len(paneOwnLoginContent)))
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	p := NewClaudeConfigProvisioner(t.TempDir())
	configDir := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	login := filepath.Join(configDir, ".credentials.json")
	writeFile(t, login, paneOwnLoginContent)

	_, err := ProvisionClaudeIsolation(isolationConfig(""), p.BaseDir, "proj", 1)
	if err == nil {
		t.Fatal("ProvisionClaudeIsolation launched although the pane's login could not be compared with an operator credential")
	}
	for _, want := range []string{"own Claude login", unreadable} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("launch error does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "pane-own-login") {
		t.Errorf("launch error leaks credential contents: %v", err)
	}
	if got, err := os.ReadFile(login); err != nil || string(got) != paneOwnLoginContent {
		t.Errorf("the refused launch changed the pane's login: %q, err=%v", got, err)
	}
}

// .credentials.json as a directory or a symlink is not a login Claude Code
// wrote, and is handled as before: a directory is kept and refused, and a
// symlink counts as exposed until re-provisioning removes it (never its
// target). The link points at a file that would pass every check on its own,
// so judging the target instead of the entry would wrongly accept it.
func TestProvisionClaudeIsolation_CredentialsJSONDirectoryOrSymlinkIsNotALogin(t *testing.T) {
	useFileCredentialStore(t)

	t.Run("directory", func(t *testing.T) {
		claudeHomeWithCredentialCopies(t)
		p := NewClaudeConfigProvisioner(t.TempDir())
		credentialDir := filepath.Join(p.ConfigPath("proj", "1"), ".credentials.json")
		if err := os.MkdirAll(credentialDir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(credentialDir, "token.json"), paneOwnLoginContent)

		_, err := ProvisionClaudeIsolation(isolationConfig(""), p.BaseDir, "proj", 1)
		if err == nil || !strings.Contains(err.Error(), ".credentials.json (directory)") {
			t.Fatalf("ProvisionClaudeIsolation error = %v, want a refusal naming the .credentials.json directory", err)
		}
		if got, err := os.ReadFile(filepath.Join(credentialDir, "token.json")); err != nil || string(got) != paneOwnLoginContent {
			t.Errorf("the .credentials.json directory was modified: %q, err=%v", got, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		claudeHomeWithCredentialCopies(t)
		p := NewClaudeConfigProvisioner(t.TempDir())
		configDir := p.ConfigPath("proj", "1")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		elsewhere := filepath.Join(t.TempDir(), "login.json")
		writeFile(t, elsewhere, paneOwnLoginContent)
		link := filepath.Join(configDir, ".credentials.json")
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}

		isolated, err := p.CredentialIsolated("proj", "1")
		if err != nil || isolated {
			t.Fatalf("CredentialIsolated = %v, %v with a .credentials.json symlink in the pane dir, want false", isolated, err)
		}
		if _, err := ProvisionClaudeIsolation(isolationConfig(""), p.BaseDir, "proj", 1); err != nil {
			t.Fatalf("ProvisionClaudeIsolation: %v", err)
		}
		if _, err := os.Lstat(link); !os.IsNotExist(err) {
			t.Errorf("re-provisioning kept the .credentials.json symlink (lstat err=%v)", err)
		}
		if got, err := os.ReadFile(elsewhere); err != nil || string(got) != paneOwnLoginContent {
			t.Errorf("removing the link disturbed its target: %q, err=%v", got, err)
		}
	})
}

// A claude-homes/<session> link to a directory outside claude-homes makes that
// directory's numbered subdirectories pane dirs. With numbered Claude profile
// dirs there (one real login each, one of them the shell's CLAUDE_CONFIG_DIR),
// provisioning deleted the profile's real credential: the profile dir is not
// HOME, ~/.claude or above either, so the identity check let it through.
func TestProvisionPaneConfig_RefusesASessionLinkOutOfClaudeHomes(t *testing.T) {
	useFileCredentialStore(t)
	claudeHomeWithCredentialCopies(t)
	profiles := t.TempDir()
	profile := filepath.Join(profiles, "1")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(profile, ".credentials.json")
	writeFile(t, credential, `{"claudeAiOauth":{"refreshToken":"profile-1"}}`)
	t.Setenv(ClaudeConfigDirEnvVar, profile)

	p := NewClaudeConfigProvisioner(t.TempDir())
	sessionDir := filepath.Dir(p.ConfigPath("proj", "1"))
	if err := os.MkdirAll(filepath.Dir(sessionDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(profiles, sessionDir); err != nil {
		t.Fatal(err)
	}

	if _, err := p.ProvisionPaneConfig("proj", "1"); err == nil {
		t.Error("ProvisionPaneConfig accepted a pane dir reached through a link out of claude-homes")
	}
	if _, err := os.Lstat(credential); err != nil {
		t.Errorf("provisioning deleted the profile's credential: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(profiles, "2")); !os.IsNotExist(err) {
		t.Errorf("provisioning created directories through the link (lstat err=%v)", err)
	}
	if _, err := p.ProvisionPaneConfig("proj", "2"); err == nil {
		t.Error("ProvisionPaneConfig created a pane dir through a link out of claude-homes")
	}
	if _, err := os.Lstat(filepath.Join(profiles, "2")); !os.IsNotExist(err) {
		t.Errorf("provisioning created %s through the link (lstat err=%v)", filepath.Join(profiles, "2"), err)
	}
}

// A link that stays inside claude-homes (one session name aliased to another)
// leads only to ntm's own pane dirs and keeps working.
func TestProvisionPaneConfig_AllowsASessionLinkInsideClaudeHomes(t *testing.T) {
	useFileCredentialStore(t)
	claudeHomeWithCredentialCopies(t)

	p := NewClaudeConfigProvisioner(t.TempDir())
	if _, err := p.ProvisionPaneConfig("proj", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	if err := os.Symlink("proj", filepath.Dir(p.ConfigPath("alias", "1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProvisionPaneConfig("alias", "1"); err != nil {
		t.Fatalf("ProvisionPaneConfig through a session link inside claude-homes: %v", err)
	}
	if _, err := os.Readlink(filepath.Join(p.ConfigPath("proj", "1"), "settings.json")); err != nil {
		t.Errorf("settings.json is not linked in the aliased pane dir: %v", err)
	}
}

// The two HOME tests above reach HOME through a session link, which the
// claude-homes confinement now refuses before the identity check runs. This
// keeps the identity check itself covered: HOME is the pane dir, reached with
// no link at all and spelled in another case, so only comparing the
// directories themselves (not their names) catches it.
func TestProvisionPaneConfig_RefusesAPaneDirThatIsHomeByIdentityAlone(t *testing.T) {
	useFileCredentialStore(t)
	p := NewClaudeConfigProvisioner(t.TempDir())
	target := p.ConfigPath("proj", "1")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCredentials := filepath.Join(target, ".git-credentials")
	writeFile(t, gitCredentials, "https://user:token@example.com")
	upper := strings.ToUpper(target)
	if upper == target {
		t.Skip("temp dir has no letters to recase")
	}
	if _, err := os.Stat(upper); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	t.Setenv("HOME", upper)

	if _, err := p.ProvisionPaneConfig("proj", "1"); err == nil {
		t.Error("ProvisionPaneConfig accepted a pane dir that is HOME spelled in another case")
	}
	if _, err := os.Lstat(gitCredentials); err != nil {
		t.Errorf("provisioning deleted %s: %v", gitCredentials, err)
	}
}

// HOME may be relative (a service unit or a test harness can set it so).
// Symlink targets resolve against the link's own directory, so a source dir
// derived from a relative HOME must be made absolute like an explicit one, or
// every inherited entry in the pane dir is a broken link.
func TestProvisionPaneConfig_LinksAbsoluteTargetsWithARelativeHome(t *testing.T) {
	useFileCredentialStore(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.MkdirAll(filepath.Join("home", ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join("home", ".claude", "settings.json"), `{"theme":"dark"}`)
	t.Setenv("HOME", "home")

	p := NewClaudeConfigProvisioner(t.TempDir())
	target, err := p.ProvisionPaneConfig("proj", "1")
	if err != nil {
		t.Fatalf("ProvisionPaneConfig: %v", err)
	}
	dest, err := os.Readlink(filepath.Join(target, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json was not linked: %v", err)
	}
	if !filepath.IsAbs(dest) {
		t.Errorf("settings.json -> %q, want an absolute target", dest)
	}
	if data, err := os.ReadFile(filepath.Join(target, "settings.json")); err != nil || string(data) != `{"theme":"dark"}` {
		t.Errorf("settings.json does not resolve to the operator's file: data=%q err=%v", data, err)
	}
}

func TestEnvForPane_TokenReaderIgnoresRecoveryPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Claude token launch requires a POSIX shell")
	}
	const token = "test-setup-token-not-for-command-text"
	root := t.TempDir()
	tokenFile := filepath.Join(root, "setup token's file")
	writeFile(t, tokenFile, token+"\n")

	t.Setenv("NTM_EXPECTED_SETUP_TOKEN", token)
	p := NewClaudeConfigProvisioner(root)

	for _, hostileReader := range []bool{false, true} {
		name := "reader absent from PATH"
		if hostileReader {
			name = "different reader in PATH"
		}
		t.Run(name, func(t *testing.T) {
			replacement := t.TempDir()
			marker := filepath.Join(t.TempDir(), "replacement-reader-used")
			t.Setenv("NTM_TOKEN_READER_MARKER", marker)
			if hostileReader {
				const script = "#!/bin/sh\nprintf used > \"$NTM_TOKEN_READER_MARKER\"\nprintf wrong-token\n"
				if err := os.WriteFile(filepath.Join(replacement, "cat"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}

			// The replacement PATH is deliberately the only search directory.
			// Both preflight and execution must use the pinned reader.
			t.Setenv("PATH", replacement)
			resolved, err := ResolveClaudeSetupTokenFile(tokenFile)
			if err != nil {
				t.Fatalf("token preflight depended on recovery PATH: %v", err)
			}
			env := p.EnvForPane("recovered", "2", resolved)
			const verify = `[ "$CLAUDE_CODE_OAUTH_TOKEN" = "$NTM_EXPECTED_SETUP_TOKEN" ] || exit 41; printf token-ok`
			command := env.ApplyToCommand("/bin/sh -c " + tmux.ShellQuote(verify))
			if strings.Contains(command, token) {
				t.Fatal("token value leaked into launch command")
			}

			output, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", command).CombinedOutput()
			if err != nil {
				t.Fatalf("token launch failed under recovery PATH: %v", err)
			}
			if string(output) != "token-ok" {
				t.Fatal("agent did not receive the saved setup token")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("recovery PATH selected a different token reader")
			}
		})
	}
}
