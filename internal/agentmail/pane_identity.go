// Package agentmail — pane_identity.go
//
// Canonical per-pane agent identity file contract, compatible with the
// mcp-agent-mail Rust reference implementation in
// `crates/mcp-agent-mail-core/src/pane_identity.rs`.
//
// Canonical path:
//
//	~/.config/agent-mail/identity/<sha1(project_key)[:12]>/<sanitized_pane_id>
//
// Pane IDs are sanitized the same way the reference implementation sanitizes
// them: the leading `%` is stripped; ASCII alphanumerics, `-`, and `_` are
// preserved; `:` is replaced with `-` (so composite keys like
// `main:0:2` become `main-0-2`); all other characters become `_`. An empty
// result becomes `unknown`.
//
// Reads also check a small set of legacy locations for backwards
// compatibility with identity files written by older `ntm` versions:
//
//   - Old NTM canonical (pre-issue-#107): `<XDG_STATE_HOME or ~/.local/state>/agent-mail/identity/<sha256(project)[:12]>/<raw_pane_id>`
//   - Legacy NTM tmp (sha1-hashed):       `/tmp/agent-mail-name.<sha1(project)[:12]>.<sanitized_pane_id>`
//   - Legacy NTM tmp (sha256-hashed):     `/tmp/agent-mail-name.<sha256(project)[:12]>.<raw_pane_id>`
//   - Legacy Claude Code:                 `~/.claude/agent-mail/identity.<sanitized_pane_id>`
//
// When ResolveIdentity finds an identity in a legacy location, the returned
// value is still the agent name; the caller can opt-in to a one-shot
// migration by calling WriteIdentity with the same arguments to move the
// value into the canonical path.
package agentmail

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// identityDirName is the sub-path under the user's config dir used to
	// store per-pane identity files.
	identityDirName = "agent-mail/identity"

	// projectHashLen is the number of hex characters of the project hash to
	// use in the directory name. Matches the Rust reference implementation.
	projectHashLen = 12
)

// CanonicalIdentityPath returns the canonical identity file path for a given
// project and tmux pane. This matches the path format used by the
// mcp-agent-mail Rust `pane_identity::canonical_identity_path` helper.
func CanonicalIdentityPath(projectKey, paneID string) string {
	base := configBaseDir()
	hash := projectSha1Short(projectKey)
	sanitized := sanitizePaneID(paneID)
	return filepath.Join(base, identityDirName, hash, sanitized)
}

// WriteIdentity writes the agent name to the canonical identity file for a
// pane. The write is atomic (write-then-rename). Parent directories are
// created with mode 0o700.
//
// A newline is appended to the agent name so the file is easy to read with
// shell tooling; ResolveIdentity trims whitespace when reading.
func WriteIdentity(projectKey, paneID, agentName string) (string, error) {
	trimmed := strings.TrimSpace(agentName)
	if trimmed == "" {
		return "", fmt.Errorf("agent name must not be empty")
	}
	path := CanonicalIdentityPath(projectKey, paneID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create identity dir %s: %w", dir, err)
	}
	// Atomic write: temp file in the same directory, then rename.
	tmp, err := os.CreateTemp(dir, ".identity-*")
	if err != nil {
		return "", fmt.Errorf("create temp identity file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	content := trimmed + "\n"
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("write temp identity file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("chmod temp identity file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("sync temp identity file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("close temp identity file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return "", fmt.Errorf("rename temp identity file: %w", err)
	}
	return path, nil
}

// ResolveIdentity returns the agent name stored for the given project/pane,
// checking the canonical path first and then a prioritized list of legacy
// locations. Returns the agent name and the path it was read from, or empty
// strings if no identity file was found.
//
// Legacy lookups are intentionally narrow: each legacy format is checked
// exactly once with the precise inputs that a prior version of ntm would have
// written.
func ResolveIdentity(projectKey, paneID string) (name string, path string) {
	// 1. Canonical path.
	canonical := CanonicalIdentityPath(projectKey, paneID)
	if v, ok := readIdentityFile(canonical); ok {
		return v, canonical
	}

	// 2. Old NTM canonical (pre-#107): XDG_STATE_HOME with sha256(project).
	if p := oldNtmStatePath(projectKey, paneID); p != "" {
		if v, ok := readIdentityFile(p); ok {
			return v, p
		}
	}

	// 3. Legacy NTM tmp (sha1).
	sha1Tmp := fmt.Sprintf("/tmp/agent-mail-name.%s.%s", projectSha1Short(projectKey), sanitizePaneID(paneID))
	if v, ok := readIdentityFile(sha1Tmp); ok {
		return v, sha1Tmp
	}

	// 4. Legacy NTM tmp (sha256) — what ntm <=1.13 actually wrote.
	sha256Tmp := fmt.Sprintf("/tmp/agent-mail-name.%s.%s", projectSha256Short(projectKey), paneID)
	if v, ok := readIdentityFile(sha256Tmp); ok {
		return v, sha256Tmp
	}

	// 5. Legacy Claude Code.
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".claude", "agent-mail", "identity."+sanitizePaneID(paneID))
		if v, ok := readIdentityFile(p); ok {
			return v, p
		}
	}

	return "", ""
}

// MigrateLegacyIdentityIfNeeded resolves the identity via the legacy paths and
// migrates it to the canonical path if (a) a legacy file exists and (b) the
// canonical file does not. This is a best-effort helper: errors are silently
// swallowed because a migration failure should never break a caller.
// Returns the resolved agent name (empty if none found).
func MigrateLegacyIdentityIfNeeded(projectKey, paneID string) string {
	canonical := CanonicalIdentityPath(projectKey, paneID)
	if _, err := os.Stat(canonical); err == nil {
		// Canonical already exists; nothing to do.
		if v, ok := readIdentityFile(canonical); ok {
			return v
		}
		return ""
	}
	name, found := ResolveIdentity(projectKey, paneID)
	if found == "" || name == "" {
		return ""
	}
	// Migrate into canonical (best-effort).
	_, _ = WriteIdentity(projectKey, paneID, name)
	return name
}

// WriteLegacyCompatIdentity writes the legacy `/tmp/agent-mail-name.<sha1>.<sanitized_pane>`
// file in addition to the canonical path so that older consumers keep working
// during the deprecation window. Returns the path written to (or empty on
// error). Errors are not returned because failing to write the legacy file
// must never abort a spawn.
func WriteLegacyCompatIdentity(projectKey, paneID, agentName string) string {
	name := strings.TrimSpace(agentName)
	if name == "" {
		return ""
	}
	p := fmt.Sprintf("/tmp/agent-mail-name.%s.%s", projectSha1Short(projectKey), sanitizePaneID(paneID))
	if err := os.WriteFile(p, []byte(name+"\n"), 0o600); err != nil {
		return ""
	}
	return p
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// configBaseDir returns the XDG-compatible config base directory (~/.config on
// Linux; ~/Library/Application Support on macOS via UserConfigDir). Falls back
// to ~/.config when UserConfigDir is unavailable (should be effectively
// impossible on any supported platform).
func configBaseDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config")
	}
	return "/tmp/.config"
}

// projectSha1Short returns the lowercase hex SHA-1 of the project key,
// truncated to projectHashLen characters. Matches the Rust implementation.
func projectSha1Short(projectKey string) string {
	h := sha1.Sum([]byte(projectKey)) // nolint:gosec // Not cryptographic; path namespace only.
	full := hex.EncodeToString(h[:])
	if len(full) < projectHashLen {
		return full
	}
	return full[:projectHashLen]
}

// projectSha256Short returns the lowercase hex SHA-256 of the project key,
// truncated to projectHashLen characters. Used only for reading legacy NTM
// identity files written before issue #107.
func projectSha256Short(projectKey string) string {
	h := sha256.Sum256([]byte(projectKey))
	full := hex.EncodeToString(h[:])
	if len(full) < projectHashLen {
		return full
	}
	return full[:projectHashLen]
}

// sanitizePaneID strips the leading `%` and replaces characters that are
// unsafe in filenames. Must match the reference implementation exactly:
//   - `:`            -> `-`  (for composite pane keys)
//   - ascii alnum, `-`, `_` -> unchanged
//   - everything else -> `_`
//
// An empty result returns "unknown" (matching Rust behaviour).
func sanitizePaneID(paneID string) string {
	stripped := strings.TrimPrefix(paneID, "%")
	var b strings.Builder
	b.Grow(len(stripped))
	for _, ch := range stripped {
		switch {
		case ch == ':':
			b.WriteRune('-')
		case ch == '-' || ch == '_':
			b.WriteRune(ch)
		case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9'):
			b.WriteRune(ch)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// readIdentityFile reads the identity file at path and returns the trimmed
// contents and true, or ("", false) if the file does not exist or the content
// is empty after trimming.
func readIdentityFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

// oldNtmStatePath returns the pre-#107 NTM identity file path (sha256 hashed,
// stored under XDG_STATE_HOME) or "" if XDG_STATE_HOME and ~/.local/state
// cannot be determined. The returned path is NOT created.
func oldNtmStatePath(projectKey, paneID string) string {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	hash := projectSha256Short(projectKey)
	// Old code stored the raw pane ID (e.g. "%0"), so pass through unchanged.
	return filepath.Join(stateDir, "agent-mail", "identity", hash, paneID)
}
