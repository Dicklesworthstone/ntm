// Package agentsession discovers the per-pane agent CLI session ID for a given
// working directory and agent type. This is used by `ntm sessions save` to
// record which provider session ran in each pane so it can later be resumed via
// casr (Cross Agent Session Resumer) or the agent's native `--resume <id>`.
//
// ntm owns the tmux topology capture/restore; per-pane provider-session resume
// is intentionally delegated to casr / native --resume rather than
// reimplementing provider-specific session formats here. This package only
// needs to find the *id* of the most-recent session for a directory; casr does
// the rest.
package agentsession

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Info describes a discovered agent CLI session for a pane.
type Info struct {
	// AgentType is the canonical ntm agent type ("cc", "cod", "gmi").
	AgentType string `json:"agent_type"`
	// SessionID is the provider session id (e.g. the Claude Code UUID).
	SessionID string `json:"session_id"`
	// Provider is the casr/native provider name ("claude", "codex", "gemini").
	Provider string `json:"provider"`
	// SourcePath is the on-disk session file the id was discovered from.
	SourcePath string `json:"source_path,omitempty"`
	// UpdatedAt is the modification time of the session file.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// ResumeProvider maps an ntm agent type to the casr/provider name used by
// `casr <provider> resume` and `casr -<flag>`. Returns "" for agent types that
// have no provider-session resume path (user panes, editor agents, etc.).
func ResumeProvider(agentType string) string {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "cc", "claude", "claude-code", "claudecode":
		return "claude"
	case "cod", "codex":
		return "codex"
	case "gmi", "gemini":
		return "gemini"
	default:
		return ""
	}
}

// homeDir is overridable in tests.
var homeDir = os.UserHomeDir

// Discover finds the most-recently-active agent CLI session for the given
// working directory and agent type. It returns nil (no error) when no session
// can be located, since session capture must remain best-effort: a pane without
// a discoverable id is still recorded topologically, just without a resume id.
func Discover(agentType, workDir string) *Info {
	provider := ResumeProvider(agentType)
	if provider == "" || workDir == "" {
		return nil
	}
	home, err := homeDir()
	if err != nil || home == "" {
		return nil
	}

	switch provider {
	case "claude":
		return discoverClaude(home, workDir)
	case "codex":
		return discoverCodex(home, workDir)
	case "gemini":
		return discoverGemini(home, workDir)
	default:
		return nil
	}
}

// encodeClaudeProjectDir reproduces Claude Code's project-directory encoding:
// the absolute cwd with path separators and dots replaced by '-'. Underscores
// are PRESERVED (Claude Code does not transform them), e.g.
//
//	/data/projects/ntm              -> -data-projects-ntm
//	/data/projects/mcp_agent_mail   -> -data-projects-mcp_agent_mail
//	/data/projects/jeffreys-skills.md -> -data-projects-jeffreys-skills-md
//
// Collapsing '_' to '-' (as an earlier version did) made discoverClaude look in
// the wrong directory and, when a hyphen-variant of another project existed,
// could resolve to a DIFFERENT real project's dir — resuming the wrong session.
func encodeClaudeProjectDir(workDir string) string {
	cleaned := filepath.Clean(workDir)
	replacer := strings.NewReplacer("/", "-", ".", "-")
	return replacer.Replace(cleaned)
}

// discoverClaude locates the newest *.jsonl under
// ~/.claude/projects/<encoded-cwd>/ and treats the filename stem as the id.
func discoverClaude(home, workDir string) *Info {
	projDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(workDir))
	path, mod := newestFileWithExt(projDir, ".jsonl")
	if path == "" {
		return nil
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "cc",
		SessionID:  id,
		Provider:   "claude",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

// discoverCodex scans ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl for the
// newest rollout whose recorded cwd matches workDir. Codex does not key its
// session store by directory, so we match on the embedded cwd line.
func discoverCodex(home, workDir string) *Info {
	root := filepath.Join(home, ".codex", "sessions")
	path, mod := newestCodexRolloutForCwd(root, filepath.Clean(workDir))
	if path == "" {
		return nil
	}
	id := codexSessionID(path)
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "cod",
		SessionID:  id,
		Provider:   "codex",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

// discoverGemini scans ~/.gemini/tmp/<hash>/chats/session-*.json. Gemini hashes
// the workspace path, so we cannot reverse it cheaply; we fall back to the
// newest session whose chat file references the workDir.
func discoverGemini(home, workDir string) *Info {
	root := filepath.Join(home, ".gemini", "tmp")
	path, mod := newestGeminiSessionForCwd(root, filepath.Clean(workDir))
	if path == "" {
		return nil
	}
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	id := strings.TrimPrefix(base, "session-")
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "gmi",
		SessionID:  id,
		Provider:   "gemini",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

// newestFileWithExt returns the path and modtime of the most-recently-modified
// file with the given extension directly inside dir.
func newestFileWithExt(dir, ext string) (string, time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}
	}
	var bestPath string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestMod) {
			bestPath = filepath.Join(dir, e.Name())
			bestMod = info.ModTime()
		}
	}
	return bestPath, bestMod
}

// fileMentionsCwd reports whether the first scanLimit bytes of the file contain
// the workDir string. Used as a cheap cwd-affinity check for providers that do
// not key their store by directory.
func fileMentionsCwd(path, workDir string, scanLimit int) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, scanLimit)
	n, _ := f.Read(buf)
	return n > 0 && strings.Contains(string(buf[:n]), workDir)
}

// newestCodexRolloutForCwd walks the date-sharded codex session tree and
// returns the newest rollout file referencing workDir.
func newestCodexRolloutForCwd(root, workDir string) (string, time.Time) {
	var bestPath string
	var bestMod time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort scan
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		if bestPath != "" && !info.ModTime().After(bestMod) {
			return nil
		}
		if !fileMentionsCwd(path, workDir, 64*1024) {
			return nil
		}
		bestPath = path
		bestMod = info.ModTime()
		return nil
	})
	return bestPath, bestMod
}

// codexSessionID derives the casr-resumable id for a codex rollout. casr accepts
// the rollout filename stem (rollout-<id>) as the lookup key; we return the
// portion after the "rollout-" prefix.
func codexSessionID(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return strings.TrimPrefix(base, "rollout-")
}

// newestGeminiSessionForCwd walks ~/.gemini/tmp/<hash>/chats and returns the
// newest session-*.json referencing workDir.
func newestGeminiSessionForCwd(root, workDir string) (string, time.Time) {
	var bestPath string
	var bestMod time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort scan
		}
		name := d.Name()
		if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".json") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "chats" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		if bestPath != "" && !info.ModTime().After(bestMod) {
			return nil
		}
		if !fileMentionsCwd(path, workDir, 64*1024) {
			return nil
		}
		bestPath = path
		bestMod = info.ModTime()
		return nil
	})
	return bestPath, bestMod
}
