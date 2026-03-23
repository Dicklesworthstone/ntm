package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-isatty"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/palette"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	utilpkg "github.com/Dicklesworthstone/ntm/internal/util"
)

// parseEditorCommand splits the editor string into command and arguments.
// It handles simple spaces.
func parseEditorCommand(editor string) (string, []string) {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

// IsInteractive returns true when the writer is a terminal.
// The pane/session selectors rely on user input; in tests or piped execution they should not run.
func IsInteractive(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// HasAnyTag checks if any of the pane's tags match any of the filter tags.
// Comparison is case-insensitive.
func HasAnyTag(paneTags, filterTags []string) bool {
	for _, ft := range filterTags {
		for _, pt := range paneTags {
			if strings.EqualFold(pt, ft) {
				return true
			}
		}
	}
	return false
}

type SessionResolution struct {
	Session  string
	Reason   string
	Inferred bool // Session arg was omitted and we resolved automatically/with chooser.
	Prompted bool // User picked from a selector (may be canceled).
}

func (r SessionResolution) ExplainIfInferred(w io.Writer) {
	if !r.Inferred || r.Session == "" || IsJSONOutput() {
		return
	}
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "Using session %q (%s)\n", r.Session, r.Reason)
}

type SessionResolveOptions struct {
	// TreatAsJSON disables prompting for local-json subcommands that don't use the global flag.
	TreatAsJSON bool
}

// ResolveSession resolves an optional session argument using a shared algorithm:
// 1) Current tmux session (if inside tmux)
// 2) Best-effort inference from cwd/project
// 3) Single running session auto-pick
// 4) Interactive chooser (when allowed)
//
// If the user cancels the chooser, SessionResolution.Session is empty and error is nil.
func ResolveSession(session string, w io.Writer) (SessionResolution, error) {
	return ResolveSessionWithOptions(session, w, SessionResolveOptions{})
}

func ResolveSessionWithOptions(session string, w io.Writer, opts SessionResolveOptions) (SessionResolution, error) {
	if session != "" {
		if err := tmux.ValidateSessionName(session); err != nil {
			return SessionResolution{}, fmt.Errorf("invalid session name: %w", err)
		}
		sessionList, err := tmux.ListSessions()
		if err != nil {
			return SessionResolution{}, err
		}
		allowPrefix := !opts.TreatAsJSON && !IsJSONOutput()
		resolved, reason, err := resolveExplicitSessionName(session, sessionList, allowPrefix)
		if err != nil {
			return SessionResolution{}, err
		}
		return SessionResolution{Session: resolved, Reason: reason, Inferred: false}, nil
	}

	// Current tmux session is the most deterministic signal.
	if tmux.InTmux() {
		if current := tmux.GetCurrentSession(); current != "" {
			return SessionResolution{
				Session:  current,
				Reason:   "current tmux session",
				Inferred: true,
			}, nil
		}
	}

	sessionList, err := tmux.ListSessions()
	if err != nil {
		return SessionResolution{}, err
	}
	if len(sessionList) == 0 {
		return SessionResolution{}, fmt.Errorf("no tmux sessions found. Create one with: ntm spawn <name>")
	}

	if inferred, reason := inferSessionFromCWD(sessionList); inferred != "" {
		return SessionResolution{
			Session:  inferred,
			Reason:   reason,
			Inferred: true,
		}, nil
	}

	// If only one session exists, pick it.
	if len(sessionList) == 1 {
		return SessionResolution{
			Session:  sessionList[0].Name,
			Reason:   "only running session",
			Inferred: true,
		}, nil
	}

	// If we cannot prompt, provide a helpful error.
	allowPrompt := !opts.TreatAsJSON && !IsJSONOutput() && IsInteractive(w)
	if !allowPrompt {
		var names []string
		for _, s := range sessionList {
			names = append(names, s.Name)
		}
		sort.Strings(names)
		return SessionResolution{}, fmt.Errorf("session name required (multiple sessions: %s)", strings.Join(names, ", "))
	}

	// Order sessions so the "best" default is at the top of the selector.
	ordered := orderSessionsForSelection(sessionList)
	selected, err := palette.RunSessionSelector(ordered)
	if err != nil {
		return SessionResolution{}, err
	}
	if selected == "" {
		return SessionResolution{Session: "", Reason: "cancelled", Inferred: true, Prompted: true}, nil
	}

	return SessionResolution{
		Session:  selected,
		Reason:   "selected from list",
		Inferred: true,
		Prompted: true,
	}, nil
}

// normalizeExplicitLiveSessionName validates an explicit session name and resolves it
// to the canonical live tmux session when possible. If no live match exists, the
// validated raw session name is returned so callers can preserve existing "not found"
// behavior after normalization.
func normalizeExplicitLiveSessionName(session string, allowPrefix bool) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	sessionList, err := tmux.ListSessions()
	if err != nil {
		return "", err
	}
	resolved, _, err := resolveExplicitSessionName(session, sessionList, allowPrefix)
	if err == nil {
		return resolved, nil
	}
	var resolveErr *sessionPkg.ResolveExplicitSessionNameError
	if errors.As(err, &resolveErr) && resolveErr.Kind == sessionPkg.ResolveExplicitSessionNameErrorAmbiguous {
		return "", err
	}
	return session, nil
}

// normalizeProjectScopedSessionName normalizes an explicit session name for
// commands that operate on a project's working directory even when the session
// itself might be offline.
func normalizeProjectScopedSessionName(session string, allowPrefix bool) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", nil
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}

	resolved, err := normalizeExplicitLiveSessionName(session, allowPrefix)
	if err != nil {
		var resolveErr *sessionPkg.ResolveExplicitSessionNameError
		if !errors.As(err, &resolveErr) || resolveErr.Kind != sessionPkg.ResolveExplicitSessionNameErrorAmbiguous {
			return "", err
		}
	} else {
		session = resolved
	}

	if !allowPrefix {
		return session, nil
	}

	resolvedBase, err := normalizeConfiguredProjectBase(config.SessionBase(session), allowPrefix)
	if err != nil {
		return "", fmt.Errorf("session %q is ambiguous: %w", session, err)
	}
	if resolvedBase == "" || resolvedBase == config.SessionBase(session) {
		return session, nil
	}

	label := config.SessionLabel(session)
	if label == "" {
		return resolvedBase, nil
	}
	return config.FormatSessionName(resolvedBase, label), nil
}

func normalizeConfiguredProjectBase(base string, allowPrefix bool) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", nil
	}

	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}
	if activeCfg == nil {
		return base, nil
	}

	projectsBase := strings.TrimSpace(config.ExpandHome(activeCfg.ProjectsBase))
	if projectsBase == "" {
		return base, nil
	}

	entries, err := os.ReadDir(projectsBase)
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil
		}
		return "", err
	}

	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		if name == base {
			return base, nil
		}
		if allowPrefix && strings.HasPrefix(name, base) {
			matches = append(matches, name)
		}
	}

	if !allowPrefix || len(matches) == 0 {
		return base, nil
	}

	sort.Strings(matches)
	if len(matches) > 1 {
		return "", fmt.Errorf("matches configured project directories: %s", strings.Join(matches, ", "))
	}
	return matches[0], nil
}

func resolveExplicitSessionName(input string, sessions []tmux.Session, allowPrefix bool) (string, string, error) {
	return sessionPkg.ResolveExplicitSessionName(input, sessions, allowPrefix)
}

func inferSessionFromCWD(sessions []tmux.Session) (string, string) {
	// Avoid local-path heuristics when operating against a remote tmux server.
	if strings.TrimSpace(tmux.DefaultClient.Remote) != "" {
		return "", ""
	}

	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return "", ""
	}
	cwd = filepath.Clean(cwd)

	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}

	// Collect all sessions matching CWD, grouped by longest prefix (bd-3cu02.8)
	var bestMatches []tmux.Session
	bestLen := 0
	for _, s := range sessions {
		projectDir := filepath.Clean(activeCfg.GetProjectDir(s.Name))
		if projectDir == "" {
			continue
		}
		if cwd == projectDir || strings.HasPrefix(cwd, projectDir+string(os.PathSeparator)) {
			dirLen := len(projectDir)
			if dirLen > bestLen {
				bestMatches = []tmux.Session{s}
				bestLen = dirLen
			} else if dirLen == bestLen {
				bestMatches = append(bestMatches, s)
			}
		}
	}
	if len(bestMatches) == 1 {
		return bestMatches[0].Name, "current directory"
	}
	if len(bestMatches) > 1 {
		// Tier 1: prefer unlabeled (base) session if it exists
		for _, m := range bestMatches {
			if !config.HasLabel(m.Name) {
				return m.Name, "current directory (base session preferred)"
			}
		}
		// Tier 2: all labeled, ambiguous — pick first alphabetically for determinism
		sort.Slice(bestMatches, func(i, j int) bool {
			return bestMatches[i].Name < bestMatches[j].Name
		})
		return bestMatches[0].Name, "current directory (first labeled session)"
	}

	// Fallback heuristic: match session name to the current directory name.
	base := filepath.Base(cwd)
	if base == "" || base == "." || base == string(os.PathSeparator) {
		return "", ""
	}
	matches := 0
	matchName := ""
	for _, s := range sessions {
		if s.Name == base {
			matches++
			matchName = s.Name
		}
	}
	if matches == 1 {
		return matchName, "current directory name"
	}

	return "", ""
}

func orderSessionsForSelection(sessions []tmux.Session) []tmux.Session {
	ordered := make([]tmux.Session, len(sessions))
	copy(ordered, sessions)

	// Prefer attached sessions when outside tmux.
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Attached != ordered[j].Attached {
			return ordered[i].Attached
		}
		return ordered[i].Name < ordered[j].Name
	})

	return ordered
}

// SanitizeFilename removes/replaces characters that are invalid in filenames.
// It ensures the filename is safe for the filesystem and truncated correctly.
func SanitizeFilename(name string) string {
	// Remove or replace invalid characters
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
		"__", "_", // Collapse double underscores
	)

	result := replacer.Replace(name)

	// Remove leading/trailing underscores
	result = strings.Trim(result, "_")

	// Limit length while respecting UTF-8 boundaries
	if len(result) > 50 {
		// Find the last valid rune boundary within the limit
		for i := 50; i >= 0; i-- {
			if utf8.RuneStart(result[i]) {
				// We found the start of the character that crosses or is at the boundary.
				// If i == 50, result[:50] is valid.
				// If i < 50, result[:i] is valid.
				return result[:i]
			}
		}
		// Fallback for extremely weird cases
		return result[:50]
	}

	return result
}

// ResolveCassContext queries CASS for relevant past sessions based on a query string
// and returns a formatted markdown summary.
func ResolveCassContext(query, dir string) (string, error) {
	// Use active config or fall back to defaults
	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}

	var opts []cass.ClientOption
	if activeCfg.CASS.BinaryPath != "" {
		opts = append(opts, cass.WithBinaryPath(activeCfg.CASS.BinaryPath))
	}
	client := cass.NewClient(opts...)
	if !client.IsInstalled() {
		return "", fmt.Errorf("cass not installed")
	}

	// Search
	limit := activeCfg.CASS.Context.MaxSessions
	if limit <= 0 {
		limit = 3
	}

	since := fmt.Sprintf("%dd", activeCfg.CASS.Context.LookbackDays)
	if activeCfg.CASS.Context.LookbackDays <= 0 {
		since = "30d"
	}

	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query:     query,
		Workspace: dir,
		Limit:     limit,
		Since:     since,
	})
	if err != nil {
		return "", err
	}

	if len(resp.Hits) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("## Relevant Past Sessions (from CASS)\n\n")
	for _, hit := range resp.Hits {
		ts := ""
		if hit.CreatedAt != nil {
			ts = hit.CreatedAt.Format("2006-01-02")
		}
		sb.WriteString(fmt.Sprintf("- **%s** (%s, %s)\n", hit.Title, hit.Agent, ts))
		if hit.Snippet != "" {
			sb.WriteString(fmt.Sprintf("  %s\n", strings.TrimSpace(hit.Snippet)))
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// GetProjectRoot returns the git root of the current working directory,
// or the cwd itself if no git root is found or on error.
func GetProjectRoot() string {
	return utilpkg.ResolveProjectDir("")
}

// resolveProjectDirForSession chooses the project directory for a session-aware command.
// Explicit session arguments prefer the configured session directory, while inferred
// sessions prefer the current workspace so robot/TUI commands don't drift into
// projects_base/<session> when launched from a different checked-out repo.
func resolveProjectDirForSession(session string, preferSession bool) string {
	session = strings.TrimSpace(session)
	if session != "" {
		if err := tmux.ValidateSessionName(session); err != nil {
			return ""
		}
	}

	cwdProject := utilpkg.ResolveProjectDir("")

	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}

	sessionProject := ""
	if session != "" && activeCfg != nil {
		sessionProject = activeCfg.GetProjectDir(session)
	}

	if preferSession {
		if best := utilpkg.BestProjectDir(sessionProject, cwdProject); best != "" {
			return best
		}
	}

	if best := utilpkg.BestProjectDir(cwdProject, sessionProject); best != "" {
		return best
	}
	return ""
}
