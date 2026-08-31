package agentmail

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectKeyOverrideEnv selects a canonical Agent Mail project identity for
// one NTM invocation. It is deliberately environment-scoped: callers that do
// not set it retain their physical checkout path as the Agent Mail key.
//
// This is useful when a checked-out upstream project must coordinate through a
// registered shared namespace, for example the public BBI project key.
const ProjectKeyOverrideEnv = "NTM_AGENT_MAIL_PROJECT_KEY"

// InvocationProjectKey returns the Agent Mail key to use for this process.
// An explicitly configured canonical key wins over a physical working
// directory; whitespace-only overrides are ignored so they cannot turn a
// normal invocation into an empty project key.
func InvocationProjectKey(projectKey string) string {
	if override := strings.TrimSpace(os.Getenv(ProjectKeyOverrideEnv)); override != "" {
		return override
	}
	return projectKey
}

// CanonicalProjectKey resolves a project key through symlinks to the real
// directory Agent Mail registers projects under.
//
// Agent Mail canonicalizes project paths server-side (e.g. a projects_base of
// /data/projects/X that is a symlink to /home/user/X is stored under
// /home/user/X), so any client-side read or release keyed by the raw
// symlinked path misses the project Agent Mail actually registered (GH#239).
// Falls back to the raw (trimmed) key when resolution fails, e.g. when the
// path does not exist.
func CanonicalProjectKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return trimmed
	}
	if resolved, err := filepath.EvalSymlinks(trimmed); err == nil && resolved != "" {
		return resolved
	}
	return trimmed
}

// ProjectKeysEquivalent reports whether two project keys identify the same
// directory. Agent Mail canonicalizes project paths through symlinks, so a
// literal string comparison rejects a correctly-bound project whenever the
// caller's key is a symlinked alias of the canonical path (GH#239). The
// fail-closed posture is preserved: when either side cannot be resolved and
// the strings differ, the keys are NOT considered equivalent.
func ProjectKeysEquivalent(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}
