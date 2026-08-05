package swarm

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ClaudeConfigDirEnvVar is the environment variable Claude Code honors to
// locate its configuration directory. When set, Claude Code reads settings,
// MCP config, skills, and — critically — .credentials.json from there instead
// of the default ~/.claude.
const ClaudeConfigDirEnvVar = "CLAUDE_CONFIG_DIR"

// ClaudeOAuthTokenEnvVar carries a non-rotating setup token (sk-ant-oat…)
// minted once per account with `claude setup-token`. It is only effective when
// no .credentials.json is reachable in the config dir: with one present,
// interactive Claude Code prefers the file credential and keeps racing.
const ClaudeOAuthTokenEnvVar = "CLAUDE_CODE_OAUTH_TOKEN"

// claudeCredentialsFile is the rotating credential that must NOT be reachable
// from an isolated config dir.
const claudeCredentialsFile = ".credentials.json"

// ClaudeCredentialStoreEnvVar lets an operator declare which store Claude Code
// uses on this machine: "file" (the credential lives in the config dir, so
// per-pane isolation applies) or "keychain" (it does not).
//
// It matters only on macOS, where the credential is normally Keychain-held and
// isolation therefore cannot be enforced. An operator who has removed the
// Keychain item and authenticates with a setup token sets this to "file". It
// does not skip any isolation check; it only supplies the one fact the probe
// cannot infer on its own.
const ClaudeCredentialStoreEnvVar = "NTM_CLAUDE_CREDENTIAL_STORE"

// ClaudeConfigProvisioner provisions per-pane isolated CLAUDE_CONFIG_DIRs so
// that Claude panes in a swarm never share the rotating
// ~/.claude/.credentials.json (GH#237).
//
// Anthropic OAuth uses single-use rotating refresh tokens and interactive
// Claude Code rewrites the shared credential file on every refresh. With N
// panes on one subscription, whichever pane refreshes first invalidates the
// refresh token every other pane holds, and they 401 in cascade — including
// the operator's own interactive session on the same machine. It presents as
// intermittent, spawn-count-dependent "account trouble": panes frozen on an
// auth-error frame while health checks still read them as alive.
//
// ntm cannot fix Claude Code's rotation, only shield it. Each pane gets a
// config dir that symlinks every entry of the real ~/.claude EXCEPT the
// rotating credential, so settings/MCP/skills survive while the credential is
// structurally unreachable. Combined with a non-rotating setup token in
// CLAUDE_CODE_OAUTH_TOKEN, the panes become stateless readers of one static
// token with no refresh cycle to race.
type ClaudeConfigProvisioner struct {
	// BaseDir roots the per-pane directories.
	BaseDir string
	// SourceDir is the operator's real Claude config dir. Empty means
	// ~/.claude.
	SourceDir string
}

// NewClaudeConfigProvisioner creates a provisioner rooted at baseDir.
func NewClaudeConfigProvisioner(baseDir string) *ClaudeConfigProvisioner {
	return &ClaudeConfigProvisioner{BaseDir: baseDir}
}

// WithSourceDir overrides the config dir that entries are linked from.
func (p *ClaudeConfigProvisioner) WithSourceDir(dir string) *ClaudeConfigProvisioner {
	p.SourceDir = dir
	return p
}

func (p *ClaudeConfigProvisioner) sourceDir() (string, error) {
	if strings.TrimSpace(p.SourceDir) != "" {
		return p.SourceDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

// ConfigPath returns the isolated CLAUDE_CONFIG_DIR for a (session, pane) pair
// without creating it. The path is keyed to the pane so the dir a given pane
// gets is stable across restarts.
//
// Note that conversation history is NOT per-pane today: projects/, sessions/,
// todos/ and history.jsonl are symlinked back to the operator's ~/.claude, so
// every pane and the operator's own interactive session read and write the same
// files. Only the rotating credential is isolated. Privatizing the writable
// state is tracked separately (bd-ox0su) because it changes what `--resume`
// sees and is the operator's call, not a silent one.
func (p *ClaudeConfigProvisioner) ConfigPath(session, pane string) string {
	return filepath.Join(p.BaseDir, ".ntm", "claude-homes", sanitizeSegment(session), sanitizeSegment(pane))
}

// ProvisionPaneConfig creates the pane's isolated config dir, linking every
// entry of the source config dir except the rotating credential. It is
// idempotent: re-provisioning refreshes links for entries added since, and
// removes a credential file that appeared inside the isolated dir.
func (p *ClaudeConfigProvisioner) ProvisionPaneConfig(session, pane string) (string, error) {
	if strings.TrimSpace(p.BaseDir) == "" {
		return "", fmt.Errorf("ClaudeConfigProvisioner: BaseDir is required")
	}
	source, err := p.sourceDir()
	if err != nil {
		return "", err
	}
	target := p.ConfigPath(session, pane)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", fmt.Errorf("create claude config dir %s: %w", target, err)
	}

	// A credential inside the isolated dir would defeat the whole point: it
	// makes Claude Code prefer the file over the static token and rejoins the
	// rotation race.
	if err := removeIsolatedCredential(target); err != nil {
		return "", err
	}

	entries, err := os.ReadDir(source)
	if err != nil {
		if os.IsNotExist(err) {
			// No operator config to inherit; an empty isolated dir is still a
			// valid, credential-free config dir.
			return target, nil
		}
		return "", fmt.Errorf("read claude config dir %s: %w", source, err)
	}

	linked := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == claudeCredentialsFile {
			continue
		}
		linked[name] = struct{}{}
		linkPath := filepath.Join(target, name)
		sourcePath := filepath.Join(source, name)

		// Replace a stale link so a re-provision picks up a moved target,
		// but never clobber a real file the pane created itself.
		if info, err := os.Lstat(linkPath); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				// The link was replaced by a real file or directory. The usual
				// cause is a tool that writes atomically (tmp file + rename
				// over settings.json), which silently converts the link into a
				// point-in-time COPY: this pane stops tracking the operator's
				// config forever, and later edits to ~/.claude never reach it.
				// Clobbering would discard whatever the pane wrote, so report
				// the divergence instead of silently choosing for the operator.
				slog.Warn("claude config entry diverged from the operator config",
					"session", session,
					"pane", pane,
					"entry", name,
					"path", linkPath,
					"source", sourcePath,
					"detail", "no longer a symlink; this pane will not see further changes to the source entry",
				)
				continue
			}
			if err := os.Remove(linkPath); err != nil {
				// A concurrent provision of the same pane may have removed it
				// first; that is the state we wanted, not a failure.
				if !os.IsNotExist(err) {
					return "", fmt.Errorf("replace stale link %s: %w", linkPath, err)
				}
			}
		}
		if err := os.Symlink(sourcePath, linkPath); err != nil && !os.IsExist(err) {
			return "", fmt.Errorf("link %s -> %s: %w", linkPath, sourcePath, err)
		}
	}

	if err := pruneDanglingLinks(target, source, linked); err != nil {
		return "", err
	}

	return target, nil
}

// pruneDanglingLinks removes symlinks in the isolated dir that point back into
// the source config dir but no longer correspond to an entry there.
//
// Without this, an entry deleted or renamed in ~/.claude leaves a broken
// symlink in every pane dir permanently: nothing ever revisits it, and Claude
// Code sees a name that resolves to nothing rather than a name that is absent.
// Only links into the source are pruned — anything the pane created itself is
// left strictly alone.
func pruneDanglingLinks(target, source string, linked map[string]struct{}) error {
	existing, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("read isolated config dir %s: %w", target, err)
	}
	for _, entry := range existing {
		name := entry.Name()
		if _, current := linked[name]; current {
			continue
		}
		linkPath := filepath.Join(target, name)
		info, err := os.Lstat(linkPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue // not ours to remove
		}
		dest, err := os.Readlink(linkPath)
		if err != nil {
			continue
		}
		// Only reclaim links this provisioner would have created.
		if filepath.Dir(dest) != filepath.Clean(source) {
			continue
		}
		if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune stale link %s: %w", linkPath, err)
		}
	}
	return nil
}

// removeIsolatedCredential deletes a .credentials.json inside the isolated
// config dir, whether it is a real file or a symlink to the shared one.
func removeIsolatedCredential(target string) error {
	credential := filepath.Join(target, claudeCredentialsFile)
	if _, err := os.Lstat(credential); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", credential, err)
	}
	if err := os.Remove(credential); err != nil {
		return fmt.Errorf("remove reachable credential %s: %w", credential, err)
	}
	return nil
}

// ProvisionClaudeIsolation builds a pane-private CLAUDE_CONFIG_DIR and returns
// the environment that launches the pane against it (GH#237). It returns the
// zero value and no error when the feature is not enabled, so callers can
// apply it unconditionally.
//
// This lives here, rather than in the spawn command, because EVERY path that
// renders a Claude launch command must apply it. It previously had a single
// caller in `ntm spawn`, and the env was a command-line prefix that dies with
// the process — so `ntm add`, the auth/limit auto-restarter, the controller,
// and saved-session restore all silently relaunched panes back onto the shared
// rotating credential. Isolation degraded away precisely at the moment the 401
// it prevents occurred, with nothing logged about the downgrade.
func ProvisionClaudeIsolation(cfg *config.Config, projectDir, session string, paneIndex int) (ClaudeLaunchEnv, error) {
	if cfg == nil || !cfg.Agents.ClaudeIsolateCredentials {
		return ClaudeLaunchEnv{}, nil
	}

	provisioner := NewClaudeConfigProvisioner(projectDir)
	pane := strconv.Itoa(paneIndex)

	configDir, err := provisioner.ProvisionPaneConfig(session, pane)
	if err != nil {
		return ClaudeLaunchEnv{}, err
	}

	// Verify the property that actually prevents the cascade instead of
	// assuming the provision worked. This also refuses platforms where the
	// credential lives outside the config dir (macOS Keychain), where the
	// check would otherwise pass vacuously.
	isolated, err := provisioner.CredentialIsolated(session, pane)
	if err != nil {
		return ClaudeLaunchEnv{}, err
	}
	if !isolated {
		return ClaudeLaunchEnv{}, fmt.Errorf("config dir %s still exposes a rotating credential", configDir)
	}

	tokenFile, err := ResolveClaudeSetupTokenFile(cfg.Agents.ClaudeTokenFile)
	if err != nil {
		return ClaudeLaunchEnv{}, err
	}
	return provisioner.EnvForPane(session, pane, tokenFile), nil
}

// ResolveClaudeSetupTokenFile validates the non-rotating setup token file and
// returns its absolute path. An unset path is allowed (the pane gets an
// isolated but uncredentialed config dir and will prompt for login); a
// set-but-unusable one is an error, because silently continuing would leave the
// operator believing isolation is credentialed.
//
// It deliberately returns the PATH rather than the token. The value is read
// here only to prove the file is usable at launch time — failing now with a
// clear message beats a pane that starts and immediately cannot authenticate —
// and is then discarded so it cannot be interpolated into a command, a log
// line, or an error string. The pane's shell reads the file itself at launch.
func ResolveClaudeSetupTokenFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for claude_token_file: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read claude_token_file %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("claude_token_file %s is empty", path)
	}
	// A multi-line token file would break `VAR="$(cat file)"` in ways that are
	// confusing at the pane rather than here.
	if strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("claude_token_file %s must contain a single-line token", path)
	}
	return path, nil
}

// ApplyToCommand prefixes cmd with this launch environment.
//
// Literal vars are shell-quoted; secret assignments are already complete,
// quoted shell text carrying only a PATH to the secret and are emitted
// verbatim. Re-quoting them would turn the command substitution into a literal
// string and hand the agent the text "$(cat …)" as its token.
func (e ClaudeLaunchEnv) ApplyToCommand(cmd string) string {
	if len(e.Vars) == 0 && len(e.SecretAssignments) == 0 {
		return cmd
	}
	// Deterministic order keeps rendered commands stable across runs.
	names := make([]string, 0, len(e.Vars))
	for name := range e.Vars {
		names = append(names, name)
	}
	sort.Strings(names)

	var prefix strings.Builder
	for _, assignment := range e.SecretAssignments {
		prefix.WriteString(assignment)
		prefix.WriteByte(' ')
	}
	for _, name := range names {
		prefix.WriteString(name)
		prefix.WriteByte('=')
		prefix.WriteString(tmux.ShellQuote(e.Vars[name]))
		prefix.WriteByte(' ')
	}
	return prefix.String() + cmd
}

// ClaudeLaunchEnv describes how to launch a Claude pane with isolated
// credentials, keeping the two kinds of value strictly apart.
type ClaudeLaunchEnv struct {
	// Vars are ordinary literal assignments. They are not secret and may be
	// displayed, logged, and quoted normally.
	Vars map[string]string

	// SecretAssignments are COMPLETE, already-quoted shell assignments whose
	// values must never appear in the command text. Callers prepend them to
	// the command verbatim and must not re-quote or log them.
	SecretAssignments []string
}

// EnvForPane returns the launch environment for a Claude pane with isolated
// credentials. tokenFile is the path to the operator's non-rotating setup
// token; an empty path means the pane has an isolated config dir but no
// credential at all and will need an interactive login.
//
// The token is passed BY REFERENCE, never by value. The pane command is typed
// into a shell with `tmux send-keys`, so a literal `CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat…`
// prefix put a long-lived, non-rotating secret into three places at once:
//   - the pane's scrollback, which ntm itself captures and exports (grep, copy,
//     extract, get-all-session-text, and the robot tail/capture surfaces),
//   - the argv of both the pane shell and the send-keys invocation, readable by
//     any local process via ps,
//   - runLocalContext's error strings, which embed the full argv and flow into
//     logs and JSON output on a failed send.
//
// Emitting `VAR="$(cat <path>)"` instead means the shell expands the file at
// launch: the command text, and therefore all three sinks above, carry only the
// path. The value reaches the process environment, which is where Claude Code
// reads it from and is the same exposure any exported credential has.
func (p *ClaudeConfigProvisioner) EnvForPane(session, pane, tokenFile string) ClaudeLaunchEnv {
	env := ClaudeLaunchEnv{
		Vars: map[string]string{ClaudeConfigDirEnvVar: p.ConfigPath(session, pane)},
	}
	if path := strings.TrimSpace(tokenFile); path != "" {
		env.SecretAssignments = append(env.SecretAssignments,
			fmt.Sprintf(`%s="$(cat %s)"`, ClaudeOAuthTokenEnvVar, tmux.ShellQuote(path)))
	}
	return env
}

// ErrCredentialStoreNotIsolable reports that the platform keeps Claude Code's
// credential somewhere this mechanism cannot reach, so isolation cannot be
// achieved — let alone verified — by excluding a file from a config dir.
var ErrCredentialStoreNotIsolable = errors.New("claude credential is held outside the config dir; per-pane isolation cannot be enforced")

// CredentialIsolated reports whether the pane's config dir exists and has no
// reachable rotating credential — the property that actually prevents the
// cascade. Callers use it to verify isolation rather than assume it.
//
// It first checks that the platform stores the credential in the config dir at
// all. On macOS, Claude Code keeps its OAuth credential in the login Keychain
// rather than ~/.claude/.credentials.json, so the file this provisioner
// excludes does not exist: every check trivially passed and callers were told
// "isolation verified" for panes that all still share and refresh one Keychain
// credential, leaving the GH#237 cascade completely unmitigated. A guard whose
// whole purpose is to verify rather than assume must fail closed there.
func (p *ClaudeConfigProvisioner) CredentialIsolated(session, pane string) (bool, error) {
	if err := credentialStoreIsolable(); err != nil {
		return false, err
	}

	target := p.ConfigPath(session, pane)
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect %s: %w", target, err)
	}
	credential := filepath.Join(target, claudeCredentialsFile)
	if _, err := os.Lstat(credential); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("inspect %s: %w", credential, err)
	}
	return false, nil
}
