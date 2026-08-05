package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// ConfigPath returns the isolated CLAUDE_CONFIG_DIR for a (session, pane)
// pair without creating it. The path is keyed to the pane so `claude --resume`
// keeps finding the same conversation history across restarts.
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

	for _, entry := range entries {
		name := entry.Name()
		if name == claudeCredentialsFile {
			continue
		}
		linkPath := filepath.Join(target, name)
		sourcePath := filepath.Join(source, name)

		// Replace a stale link so a re-provision picks up a moved target,
		// but never clobber a real file the pane created itself.
		if info, err := os.Lstat(linkPath); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if err := os.Remove(linkPath); err != nil {
				return "", fmt.Errorf("replace stale link %s: %w", linkPath, err)
			}
		}
		if err := os.Symlink(sourcePath, linkPath); err != nil && !os.IsExist(err) {
			return "", fmt.Errorf("link %s -> %s: %w", linkPath, sourcePath, err)
		}
	}

	return target, nil
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

// EnvForPane returns the environment assignments for launching a Claude pane
// with isolated credentials. The setup token is included only when provided;
// without it the pane has an isolated config dir but no credential at all and
// will need an interactive login.
func (p *ClaudeConfigProvisioner) EnvForPane(session, pane, setupToken string) map[string]string {
	env := map[string]string{ClaudeConfigDirEnvVar: p.ConfigPath(session, pane)}
	if token := strings.TrimSpace(setupToken); token != "" {
		env[ClaudeOAuthTokenEnvVar] = token
	}
	return env
}

// CredentialIsolated reports whether the pane's config dir exists and has no
// reachable rotating credential — the property that actually prevents the
// cascade. Callers use it to verify isolation rather than assume it.
func (p *ClaudeConfigProvisioner) CredentialIsolated(session, pane string) (bool, error) {
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
