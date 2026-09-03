package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/process"
)

// bufferSeq is a monotonic counter that ensures unique tmux buffer names
// even when multiple goroutines call SendBufferWithDelay concurrently
// within the same nanosecond.
var bufferSeq atomic.Uint64

// paneNameRegex matches the NTM pane naming convention:
// session__type_index or session__type_index_variant, optionally with tags [tag1,tag2]
// Examples:
//
//	session__cc_1
//	session__cc_1[frontend]
//	session__cc_1_opus[backend,api]
var paneNameRegex = regexp.MustCompile(`^.+__([\w-]+)_(\d+)(?:_([A-Za-z0-9._/@:+-]+))?(?:\[([^\]]*)\])?$`)

// sessionNameRegex validates session names (allowed: a-z, A-Z, 0-9, _, -)
var sessionNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// AgentType represents the type of AI agent
type AgentType = agent.AgentType

const (
	AgentClaude      = agent.AgentTypeClaudeCode
	AgentCodex       = agent.AgentTypeCodex
	AgentGemini      = agent.AgentTypeGemini
	AgentAntigravity = agent.AgentTypeAntigravity
	AgentGrok        = agent.AgentTypeGrok
	AgentCursor      = agent.AgentTypeCursor
	AgentWindsurf    = agent.AgentTypeWindsurf
	AgentAider       = agent.AgentTypeAider
	AgentOpencode    = agent.AgentTypeOpencode
	AgentOllama      = agent.AgentTypeOllama
	AgentUser        = agent.AgentTypeUser
	AgentUnknown     = agent.AgentTypeUnknown
)

// FieldSeparator is used to separate fields in tmux format strings.
const FieldSeparator = "_NTM_SEP_"

// Pane represents a tmux pane
type Pane struct {
	ID          string
	Index       int
	WindowIndex int // The window index (0-based)
	NTMIndex    int // The NTM-specific index parsed from the title (e.g., 1 for cc_1)
	Title       string
	Type        AgentType
	Variant     string   // Model alias or persona name (from pane title)
	Tags        []string // User-defined tags (from pane title, e.g., [frontend,api])
	Command     string
	Width       int
	Height      int
	Active      bool
	PID         int // Shell PID
}

// PaneRef is the stable physical identity and topology address of a pane.
// ID is the tmux-native identity; WindowIndex and PaneIndex identify its
// current physical location. NTMIndex is a logical label only and is never
// used to address a pane.
type PaneRef struct {
	ID          string `json:"pane_id,omitempty"`
	WindowIndex int    `json:"window_index"`
	PaneIndex   int    `json:"pane_index"`
	NTMIndex    int    `json:"ntm_index,omitempty"`
}

// Ref returns the pane's stable identity and current topology address.
func (p Pane) Ref() PaneRef {
	return PaneRef{
		ID:          p.ID,
		WindowIndex: p.WindowIndex,
		PaneIndex:   p.Index,
		NTMIndex:    p.NTMIndex,
	}
}

// Physical returns the explicit window.pane address.
func (r PaneRef) Physical() string {
	return fmt.Sprintf("%d.%d", r.WindowIndex, r.PaneIndex)
}

// Canonical returns the shortest unambiguous address for the session
// topology. A multi-window session requires window.pane; a single-window
// session retains the familiar bare pane index.
func (r PaneRef) Canonical(multiWindow bool) string {
	if multiWindow {
		return r.Physical()
	}
	return strconv.Itoa(r.PaneIndex)
}

// StableKey returns a process-stable identity suitable for deduplication.
// Synthetic panes without a tmux ID fall back to their physical address.
func (r PaneRef) StableKey() string {
	if r.ID != "" {
		return r.ID
	}
	return r.Physical()
}

// PaneSelectorKind identifies one syntax in the shared pane selector grammar.
type PaneSelectorKind uint8

const (
	PaneSelectorPaneIndex PaneSelectorKind = iota + 1
	PaneSelectorWindowPane
	PaneSelectorID
)

// PaneSelector is a validated selector. Bare N means pane index in a
// single-window session and window index in a multi-window session. W.P and
// %N always name one physical pane.
type PaneSelector struct {
	Raw         string
	Kind        PaneSelectorKind
	Index       int
	WindowIndex int
	PaneIndex   int
	PaneID      string
}

// PaneSelectorErrorKind identifies why a pane selector could not be resolved.
// Command surfaces use this classification to produce stable machine-readable
// error codes without parsing human-readable error strings.
type PaneSelectorErrorKind string

const (
	PaneSelectorInvalid   PaneSelectorErrorKind = "invalid"
	PaneSelectorMissing   PaneSelectorErrorKind = "missing"
	PaneSelectorNotFound  PaneSelectorErrorKind = "not_found"
	PaneSelectorAmbiguous PaneSelectorErrorKind = "ambiguous"
)

// PaneSelectorError describes a pane-selector validation or resolution error.
type PaneSelectorError struct {
	Kind      PaneSelectorErrorKind
	Selector  string
	Matches   int
	Available []string
}

func (e *PaneSelectorError) Error() string {
	if e == nil {
		return "pane selector error"
	}
	switch e.Kind {
	case PaneSelectorMissing:
		return "at least one pane selector is required"
	case PaneSelectorNotFound:
		return fmt.Sprintf("pane selector %q not found; available: %s", e.Selector, strings.Join(e.Available, ", "))
	case PaneSelectorAmbiguous:
		if e.Selector != "" {
			return fmt.Sprintf("pane selector %q matched %d panes (%s); use explicit W.P or %%N", e.Selector, e.Matches, strings.Join(e.Available, ", "))
		}
		return fmt.Sprintf("pane selector resolved to %d panes; use exactly one W.P or %%N selector", e.Matches)
	default:
		return fmt.Sprintf("invalid pane selector %q: expected N, W.P, or %%N", e.Selector)
	}
}

func parsePaneSelectorNumber(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil
}

// ParsePaneSelector validates and parses N, W.P, or %N syntax.
func ParsePaneSelector(input string) (PaneSelector, error) {
	raw := strings.TrimSpace(input)
	invalid := func() (PaneSelector, error) {
		return PaneSelector{}, &PaneSelectorError{Kind: PaneSelectorInvalid, Selector: input}
	}
	if raw == "" {
		return invalid()
	}
	if strings.HasPrefix(raw, "%") {
		index, ok := parsePaneSelectorNumber(strings.TrimPrefix(raw, "%"))
		if !ok {
			return invalid()
		}
		return PaneSelector{Raw: raw, Kind: PaneSelectorID, Index: index, PaneID: raw}, nil
	}
	if strings.Contains(raw, ".") {
		window, pane, ok := strings.Cut(raw, ".")
		windowIndex, windowOK := parsePaneSelectorNumber(window)
		paneIndex, paneOK := parsePaneSelectorNumber(pane)
		if !ok || !windowOK || !paneOK {
			return invalid()
		}
		return PaneSelector{
			Raw:         raw,
			Kind:        PaneSelectorWindowPane,
			WindowIndex: windowIndex,
			PaneIndex:   paneIndex,
		}, nil
	}
	index, ok := parsePaneSelectorNumber(raw)
	if !ok {
		return invalid()
	}
	return PaneSelector{Raw: raw, Kind: PaneSelectorPaneIndex, Index: index}, nil
}

// Matches reports whether the validated selector addresses pane in the given
// topology.
func (s PaneSelector) Matches(pane Pane, multiWindow bool) bool {
	switch s.Kind {
	case PaneSelectorID:
		return pane.ID == s.PaneID
	case PaneSelectorWindowPane:
		return pane.WindowIndex == s.WindowIndex && pane.Index == s.PaneIndex
	case PaneSelectorPaneIndex:
		if multiWindow {
			return pane.WindowIndex == s.Index
		}
		return pane.Index == s.Index
	default:
		return false
	}
}

// PanesSpanMultipleWindows reports whether panes belong to more than one tmux
// window. Pane.Index is only unique within a window, so callers must switch to
// topology-aware addresses when this returns true.
func PanesSpanMultipleWindows(panes []Pane) bool {
	if len(panes) == 0 {
		return false
	}
	first := panes[0].WindowIndex
	for _, pane := range panes[1:] {
		if pane.WindowIndex != first {
			return true
		}
	}
	return false
}

// PaneTargetKey returns the shortest unambiguous pane address for the supplied
// session topology. Single-window sessions use the familiar bare pane index;
// multi-window sessions use window.pane.
func PaneTargetKey(pane Pane, multiWindow bool) string {
	return pane.Ref().Canonical(multiWindow)
}

// PaneMatchesSelector applies the pane-selector grammar shared by robot mode
// and shell send: %N is an exact tmux pane ID, W.P is an exact window/pane
// address, and a bare N selects pane N in a single-window session or window N
// in a multi-window session. Syntax validation belongs at the command boundary;
// malformed selectors simply do not match here.
func PaneMatchesSelector(pane Pane, selector string, multiWindow bool) bool {
	parsed, err := ParsePaneSelector(selector)
	if err != nil {
		return false
	}
	return parsed.Matches(pane, multiWindow)
}

// SortPanesByTopology returns a deterministic window, pane, then ID ordering.
func SortPanesByTopology(panes []Pane) []Pane {
	ordered := append([]Pane(nil), panes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].WindowIndex != ordered[j].WindowIndex {
			return ordered[i].WindowIndex < ordered[j].WindowIndex
		}
		if ordered[i].Index != ordered[j].Index {
			return ordered[i].Index < ordered[j].Index
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

func paneSelectorRefs(panes []Pane, multiWindow bool) []string {
	refs := make([]string, 0, len(panes))
	for _, pane := range panes {
		ref := pane.Ref().Canonical(multiWindow)
		if pane.ID != "" {
			ref += " (" + pane.ID + ")"
		}
		refs = append(refs, ref)
	}
	return refs
}

// ResolvePaneSelectors resolves every selector, rejects missing or ambiguous
// singular matches, deduplicates aliases by physical pane identity, and
// returns panes in deterministic topology order.
func ResolvePaneSelectors(panes []Pane, selectors []string, requireSingle bool) ([]Pane, error) {
	if len(selectors) == 0 {
		return nil, &PaneSelectorError{Kind: PaneSelectorMissing}
	}
	ordered := SortPanesByTopology(panes)
	multiWindow := PanesSpanMultipleWindows(ordered)
	selected := make(map[string]struct{})

	for _, raw := range selectors {
		selector, err := ParsePaneSelector(raw)
		if err != nil {
			return nil, err
		}
		matches := make([]Pane, 0, 1)
		for _, pane := range ordered {
			if selector.Matches(pane, multiWindow) {
				matches = append(matches, pane)
			}
		}
		if len(matches) == 0 {
			return nil, &PaneSelectorError{
				Kind:      PaneSelectorNotFound,
				Selector:  raw,
				Available: paneSelectorRefs(ordered, multiWindow),
			}
		}
		if requireSingle && len(matches) != 1 {
			return nil, &PaneSelectorError{
				Kind:      PaneSelectorAmbiguous,
				Selector:  raw,
				Matches:   len(matches),
				Available: paneSelectorRefs(matches, true),
			}
		}
		for _, pane := range matches {
			selected[pane.Ref().StableKey()] = struct{}{}
		}
	}

	resolved := make([]Pane, 0, len(selected))
	for _, pane := range ordered {
		if _, ok := selected[pane.Ref().StableKey()]; ok {
			resolved = append(resolved, pane)
		}
	}
	if requireSingle && len(resolved) != 1 {
		return nil, &PaneSelectorError{Kind: PaneSelectorAmbiguous, Matches: len(resolved)}
	}
	return resolved, nil
}

// Session represents a tmux session
type Session struct {
	Name      string
	Directory string
	Windows   int
	Panes     []Pane
	Attached  bool
	Created   string
}

// parseAgentFromTitle extracts agent type, index, variant, and tags from a pane title.
// Title format: {session}__{type}_{index}[tags] or {session}__{type}_{index}_{variant}[tags]
// Returns AgentUser, 0, empty variant, and nil tags if title doesn't match NTM format.
func parseAgentFromTitle(title string) (AgentType, int, string, []string) {
	matches := paneNameRegex.FindStringSubmatch(title)
	if matches == nil {
		// Not an NTM-formatted title, default to user
		return AgentUser, 0, "", nil
	}

	// matches[1] = type (cc, cod, gmi, cursor, etc.)
	// matches[2] = index (1, 2, 3...)
	// matches[3] = variant (may be empty)
	// matches[4] = tags string (may be empty, may be absent if regex didn't capture)
	agentType := AgentType(matches[1])
	idx, _ := strconv.Atoi(matches[2])
	variant := matches[3]
	var tags []string
	if len(matches) >= 5 {
		tags = parseTags(matches[4])
	}

	// Allow any non-empty agent type that matched the regex
	if agentType != "" {
		return agentType, idx, variant, tags
	}
	return AgentUser, 0, "", nil
}

// parseTags parses a comma-separated tag string into a slice.
// Returns nil for empty input.
func parseTags(tagStr string) []string {
	if tagStr == "" {
		return nil
	}
	parts := strings.Split(tagStr, ",")
	var tags []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			tags = append(tags, p)
		}
	}
	return tags
}

// FormatTags formats tags as a bracket-enclosed string for pane titles.
// Returns empty string if no tags.
func FormatTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, ",") + "]"
}

// detectAgentFromCommand attempts to identify the agent type from the process command.
// This is a fallback when the pane title doesn't match the NTM format (e.g., when
// shell prompts or tmux hooks change the title dynamically).
func detectAgentFromCommand(command string) AgentType {
	cmd := strings.ToLower(strings.TrimSpace(command))

	// Grok Build deliberately uses the generic executable name "grok", which is
	// also present in unrelated project names and command arguments. Recognize it
	// only when the command's executable token has the exact basename "grok";
	// substring matching here would misclassify tools such as ngrok,
	// grok-exporter, and `rg grok`. The process-tree path applies the same rule to
	// argv[0] before considering legacy agent heuristics.
	if commandExecutableIs(cmd, "grok") {
		return AgentGrok
	}

	// Antigravity (agy) — checked before Gemini so an agy launch command whose
	// pinned model name contains "Gemini" ("Gemini 3.8 Flash (High)") is never
	// misread as a gemini pane. Like grok, "agy" is a short generic token, so
	// it must be the command's executable basename, never a coincidental
	// argument. Real launch shapes: `agy`, `agy-locked` (the un-aliased
	// wrapper binary spawn actually execs — see config.agyBinary), and the
	// spelled-out `antigravity` binary.
	if commandExecutableIs(cmd, "agy") ||
		commandExecutableIs(cmd, "agy-locked") ||
		commandExecutableIs(cmd, "antigravity") {
		return AgentAntigravity
	}

	// Helper to check if a command matches an agent
	isAgent := func(name string) bool {
		return cmd == name ||
			strings.HasPrefix(cmd, name+" ") ||
			strings.Contains(cmd, "/"+name+" ") ||
			strings.Contains(cmd, "/"+name+"-") ||
			strings.HasSuffix(cmd, "/"+name)
	}

	// Claude Code variants
	if isAgent("claude") || strings.Contains(cmd, "claude-code") {
		return AgentClaude
	}

	// Codex CLI
	if isAgent("codex") {
		return AgentCodex
	}

	// Gemini CLI
	if isAgent("gemini") {
		return AgentGemini
	}

	// Cursor
	if isAgent("cursor") {
		return AgentCursor
	}

	// Windsurf
	if isAgent("windsurf") {
		return AgentWindsurf
	}

	// Aider
	if isAgent("aider") {
		return AgentAider
	}

	// Ollama
	if isAgent("ollama") {
		return AgentOllama
	}

	// OpenCode (https://opencode.ai) ships as a single `opencode` binary, so
	// tmux reports it directly as the pane's current command (ntm#266).
	if isAgent("opencode") {
		return AgentOpencode
	}

	return AgentUser
}

// commandExecutableIs reports whether command starts with an exact executable
// basename. It intentionally does not interpret shell syntax or search later
// arguments: process discovery supplies the real argv separately, and treating
// arbitrary arguments as executables creates false positives for a generic name
// such as "grok".
func commandExecutableIs(command, executable string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return false
	}
	first := strings.Trim(fields[0], "'\"")
	if i := strings.LastIndexAny(first, "/\\"); i >= 0 {
		first = first[i+1:]
	}
	first = strings.TrimSuffix(first, ".exe")
	return strings.EqualFold(first, executable)
}

func detectAgentFromArgv(argv []string) AgentType {
	if len(argv) == 0 {
		return AgentUser
	}

	// Grok Build must be the process executable, never a coincidental argument.
	if commandExecutableIs(argv[0], "grok") {
		return AgentGrok
	}

	// Antigravity: same executable-only rule as grok. "agy" is a short
	// generic token that shows up in unrelated argv elements (`rg agy`), so
	// it may only classify via argv[0]; the fallback loops below therefore
	// exclude AgentAntigravity the same way they exclude AgentGrok.
	if commandExecutableIs(argv[0], "agy") ||
		commandExecutableIs(argv[0], "agy-locked") ||
		commandExecutableIs(argv[0], "antigravity") {
		return AgentAntigravity
	}

	joined := strings.Join(argv, " ")
	if t := detectAgentFromCommand(joined); t != AgentUser && t != AgentGrok && t != AgentAntigravity {
		return t
	}
	for _, arg := range argv {
		if t := detectAgentFromCommand(arg); t != AgentUser && t != AgentGrok && t != AgentAntigravity {
			return t
		}
	}
	return AgentUser
}

// agentWrapperCommands lists shell-bin names that frequently appear as
// `pane_current_command` even when the pane is actually running an
// agent under them. tmux only reports the immediate process name, so a
// pane running `bun /home/.../codex ...` reports `bun` and gets
// classified as a user pane unless we look one level deeper. See
// acfs#267.
var agentWrapperCommands = map[string]struct{}{
	"bun":     {},
	"node":    {},
	"npx":     {},
	"deno":    {},
	"python":  {},
	"python3": {},
	"sh":      {},
	"bash":    {},
	"zsh":     {},
}

// bareShellCommands are foreground commands that prove no agent CLI is
// running in the pane: the shell itself is the active process. Deliberately
// conservative — wrappers (bun/node), unknown binaries, and agent-named
// commands are all treated as "possibly the agent" so a live pane can never
// be false-refused (ntm-0g0b).
var bareShellCommands = map[string]struct{}{
	"zsh": {}, "bash": {}, "sh": {}, "fish": {}, "dash": {}, "ksh": {}, "tcsh": {},
}

// RunsBareShell reports whether the pane's foreground process is a bare login
// shell — the only state in which typing a launch command into the pane is
// guaranteed to reach a shell rather than a running program. Conservative by
// construction: wrappers and unknown binaries all report false.
func (p Pane) RunsBareShell() bool {
	base := strings.ToLower(strings.TrimSpace(p.Command))
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexAny(base, " \t"); i >= 0 {
		base = base[:i]
	}
	_, isShell := bareShellCommands[base]
	return isShell
}

// AgentCLIDead reports whether an agent-typed pane's CLI has exited back to a
// bare shell — the pane looks like an agent (title) but its foreground
// process is the login shell, so anything typed into it lands in zsh, not an
// agent ("zsh: command not found" is one of the most-documented swarm failure
// modes). User/unknown panes are never "dead": a shell is their normal state.
func (p Pane) AgentCLIDead() bool {
	agentType := p.Type.Canonical()
	if !agentType.IsValid() || agentType == AgentUser || agentType == AgentUnknown {
		return false
	}
	base := strings.ToLower(strings.TrimSpace(p.Command))
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexAny(base, " \t"); i >= 0 {
		base = base[:i]
	}
	_, isShell := bareShellCommands[base]
	return isShell
}

func isAgentWrapperCommand(command string) bool {
	base := strings.ToLower(strings.TrimSpace(command))
	if base == "" {
		return false
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexAny(base, " \t"); i >= 0 {
		base = base[:i]
	}
	_, ok := agentWrapperCommands[base]
	return ok
}

// detectAgentFromProcessTree walks up to `maxDepth` levels of
// descendants under shellPID and returns the first agent type that
// matches either an argv element or a child process command.
//
// This handles the common Bun-wrapper case (acfs#267): the immediate
// child of zsh is `bun /home/.../codex ...`, whose own child is the
// real codex binary. Without this walk, NTM mis-classifies the pane
// as user.
//
// Bounded by `maxDepth` (default 4) and a child-fanout limit
// (`process.GetChildPIDs(_, 8)`) to keep the per-pane cost bounded
// even on busy hosts.
func detectAgentFromProcessTree(shellPID int, maxDepth int) AgentType {
	if shellPID <= 0 {
		return AgentUser
	}
	if maxDepth <= 0 {
		maxDepth = 4
	}

	type frame struct {
		pid   int
		depth int
	}
	stack := []frame{{pid: shellPID, depth: 0}}
	visited := map[int]bool{shellPID: true}

	for len(stack) > 0 {
		// Pop. Pre-order traversal — check argv before descending.
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		argv := process.GetCmdline(top.pid)
		// Detect on the joined argv as well as on each individual element
		// so wrappers like `bun /home/.../codex ...` get caught: the
		// agent name appears as a path component of one of argv's
		// arguments rather than as the immediate command.
		if t := detectAgentFromArgv(argv); t != AgentUser {
			return t
		}

		if top.depth >= maxDepth {
			continue
		}
		for _, child := range process.GetChildPIDs(top.pid, 8) {
			if visited[child] {
				continue
			}
			visited[child] = true
			stack = append(stack, frame{pid: child, depth: top.depth + 1})
		}
	}
	return AgentUser
}

// IsInstalled checks if tmux is available
func IsInstalled() bool {
	return DefaultClient.IsInstalled()
}

// EnsureInstalled returns an error if tmux is not installed
func EnsureInstalled() error {
	if !IsInstalled() {
		return errors.New("tmux is not installed. Install it with: brew install tmux (macOS) or apt install tmux (Linux)")
	}
	return nil
}

// InTmux returns true if currently inside a tmux session
func InTmux() bool {
	return os.Getenv("TMUX") != ""
}

// SessionExists checks if a session exists
func (c *Client) SessionExists(name string) bool {
	exists, _ := c.SessionExistsContext(context.Background(), name)
	return exists
}

// SessionExistsContext checks whether a session exists with caller cancellation.
func (c *Client) SessionExistsContext(ctx context.Context, name string) (bool, error) {
	return classifySessionExistsResult(c.RunSilentContext(ctx, "has-session", "-t", TargetSession(name)))
}

func classifySessionExistsResult(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if exitCode, ok := commandExitCode(err); ok && exitCode == 1 && isExpectedSessionAbsence(err) {
		// has-session uses exit status 1 both for a missing target and when the
		// first session has not created a server socket yet. Accept only those
		// recognized stderr shapes; permission, stale-socket, and command failures
		// must remain observable to callers.
		return false, nil
	}
	return false, err
}

func isExpectedSessionAbsence(err error) bool {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "can't find session") ||
		strings.Contains(message, "no such session") ||
		strings.Contains(message, "session not found") ||
		strings.Contains(message, "no server running") {
		return true
	}
	return strings.Contains(message, "error connecting to") &&
		(strings.Contains(message, "no such file or directory") || strings.Contains(message, "does not exist"))
}

// SessionExists checks if a session exists (default client)
func SessionExists(name string) bool {
	return DefaultClient.SessionExists(name)
}

// SessionExistsContext checks whether a session exists with caller cancellation (default client).
func SessionExistsContext(ctx context.Context, name string) (bool, error) {
	return DefaultClient.SessionExistsContext(ctx, name)
}

// ListSessions returns all tmux sessions
func (c *Client) ListSessions() ([]Session, error) {
	return c.ListSessionsContext(context.Background())
}

// ListSessionsContext returns all tmux sessions with caller cancellation.
func (c *Client) ListSessionsContext(ctx context.Context) ([]Session, error) {
	sep := FieldSeparator
	format := fmt.Sprintf("#{session_name}%[1]s#{session_windows}%[1]s#{session_attached}%[1]s#{session_created_string}", sep)
	output, err := c.RunContext(ctx, "list-sessions", "-F", format)
	if err != nil {
		// No sessions is not an error - handle various tmux error messages
		errMsg := err.Error()
		if strings.Contains(errMsg, "no server running") ||
			strings.Contains(errMsg, "no sessions") ||
			strings.Contains(errMsg, "No such file or directory") ||
			strings.Contains(errMsg, "error connecting to") {
			return nil, nil
		}
		return nil, err
	}

	if output == "" {
		return nil, nil
	}

	var sessions []Session
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(line, sep)
		if len(parts) < 4 {
			continue
		}

		windows, _ := strconv.Atoi(parts[1])
		attached := parts[2] == "1"

		sessions = append(sessions, Session{
			Name:     parts[0],
			Windows:  windows,
			Attached: attached,
			Created:  parts[3],
		})
	}

	return sessions, nil
}

// ListSessions returns all tmux sessions (default client)
func ListSessions() ([]Session, error) {
	return DefaultClient.ListSessions()
}

// ListSessionsContext returns all tmux sessions with caller cancellation (default client).
func ListSessionsContext(ctx context.Context) ([]Session, error) {
	return DefaultClient.ListSessionsContext(ctx)
}

// GetSession returns detailed info about a session
func (c *Client) GetSession(name string) (*Session, error) {
	// Validate session name to prevent tmux format string injection in the
	// -f filter below, which embeds the name into a #{==:...} expression.
	if err := ValidateSessionName(name); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}
	if !c.SessionExists(name) {
		return nil, fmt.Errorf("session '%s' not found", name)
	}

	// Get session info
	sep := FieldSeparator
	format := fmt.Sprintf("#{session_name}%[1]s#{session_windows}%[1]s#{session_attached}", sep)
	output, err := c.Run("list-sessions", "-F", format, "-f", fmt.Sprintf("#{==:#{session_name},%s}", name))
	if err != nil {
		return nil, err
	}

	parts := strings.Split(output, sep)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected session format")
	}

	windows, _ := strconv.Atoi(parts[1])
	attached := parts[2] == "1"

	session := &Session{
		Name:     name,
		Windows:  windows,
		Attached: attached,
	}

	// Get panes
	panes, err := c.GetPanes(name)
	if err != nil {
		return nil, err
	}
	session.Panes = panes

	return session, nil
}

// GetSession returns detailed info about a session (default client)
func GetSession(name string) (*Session, error) {
	return DefaultClient.GetSession(name)
}

// DefaultHistoryLimit is the default scrollback buffer size for new sessions.
// tmux's built-in default is only 2000 lines, which is far too small for AI agent
// output. 50000 lines ensures that scrollback is accessible when reviewing
// previous output in panes created via the palette or spawn commands.
const DefaultHistoryLimit = 50000

// CreateSession creates a new tmux session with a generous scrollback buffer.
// The history-limit is set to DefaultHistoryLimit (50000 lines) to ensure pane
// scrollback is accessible. Use CreateSessionWithHistoryLimit for a custom value.
func (c *Client) CreateSession(name, directory string) error {
	return c.CreateSessionContext(context.Background(), name, directory)
}

// CreateSessionContext creates a new tmux session with cancellation support.
func (c *Client) CreateSessionContext(ctx context.Context, name, directory string) error {
	return c.CreateSessionWithHistoryLimitContext(ctx, name, directory, DefaultHistoryLimit)
}

// CreateSessionWithHistoryLimit creates a new tmux session and sets the
// history-limit (scrollback buffer size) for the session. A value of 0 skips
// setting history-limit, leaving tmux's default (2000 lines).
func (c *Client) CreateSessionWithHistoryLimit(name, directory string, historyLimit int) error {
	return c.CreateSessionWithHistoryLimitContext(context.Background(), name, directory, historyLimit)
}

// CreateSessionWithHistoryLimitContext creates a new tmux session with a
// custom scrollback limit and cancellation support.
func (c *Client) CreateSessionWithHistoryLimitContext(ctx context.Context, name, directory string, historyLimit int) error {
	if ctx == nil {
		return errors.New("tmux session creation context is required")
	}
	if err := ValidateSessionName(name); err != nil {
		return fmt.Errorf("invalid session name: %w", err)
	}
	if err := c.RunSilentContext(ctx, "new-session", "-d", "-s", name, "-c", directory); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tmux session %q was created before cancellation: %w", name, err)
	}
	if historyLimit > 0 {
		// Set history-limit on the session so all panes (including those created
		// later via split-window) inherit the larger scrollback buffer.
		if err := c.RunSilentContext(ctx, "set-option", "-t", SessionOptionTarget(name), "history-limit", fmt.Sprintf("%d", historyLimit)); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("tmux session %q was created before cancellation: %w", name, ctxErr)
			}
			return fmt.Errorf("tmux session %q was created but setting history limit failed: %w", name, err)
		}
	}
	return nil
}

// CreateSession creates a new tmux session (default client)
func CreateSession(name, directory string) error {
	return DefaultClient.CreateSession(name, directory)
}

// CreateSessionContext creates a new tmux session with cancellation support
// using the default client.
func CreateSessionContext(ctx context.Context, name, directory string) error {
	return DefaultClient.CreateSessionContext(ctx, name, directory)
}

// CreateSessionWithHistoryLimit creates a new tmux session with a custom
// history-limit (default client).
func CreateSessionWithHistoryLimit(name, directory string, historyLimit int) error {
	return DefaultClient.CreateSessionWithHistoryLimit(name, directory, historyLimit)
}

// CreateSessionWithHistoryLimitContext creates a new tmux session with a
// custom scrollback limit and cancellation support using the default client.
func CreateSessionWithHistoryLimitContext(ctx context.Context, name, directory string, historyLimit int) error {
	return DefaultClient.CreateSessionWithHistoryLimitContext(ctx, name, directory, historyLimit)
}

// GetPanes returns all panes in a session
func (c *Client) GetPanes(session string) ([]Pane, error) {
	return c.GetPanesContext(context.Background(), session)
}

// GetPanesContext returns all panes in a session with cancellation support.
func (c *Client) GetPanesContext(ctx context.Context, session string) ([]Pane, error) {
	sep := FieldSeparator
	format := fmt.Sprintf("#{pane_id}%[1]s#{pane_index}%[1]s#{pane_title}%[1]s#{pane_current_command}%[1]s#{pane_width}%[1]s#{pane_height}%[1]s#{pane_active}%[1]s#{pane_pid}%[1]s#{window_index}%[1]s#{@ntm_agent_type}", sep)
	output, err := c.RunContext(ctx, "list-panes", "-s", "-t", TargetSession(session), "-F", format)
	if err != nil {
		return nil, err
	}

	var panes []Pane
	for lineNumber, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		p, err := parsePaneLine(line, sep)
		if err != nil {
			return nil, fmt.Errorf("parse pane %d from session %q: %w", lineNumber+1, session, err)
		}
		panes = append(panes, *p)
	}

	return panes, nil
}

// GetPanes returns all panes in a session (default client)
func GetPanes(session string) ([]Pane, error) {
	return DefaultClient.GetPanes(session)
}

// GetPanesContext returns all panes in a session with cancellation support (default client).
func GetPanesContext(ctx context.Context, session string) ([]Pane, error) {
	return DefaultClient.GetPanesContext(ctx, session)
}

const (
	defaultPaneProcessStartTimeout = 10 * time.Second
	defaultPaneProcessStartPoll    = 100 * time.Millisecond
	paneProcessStableObservations  = 2
)

// PaneCommandIsShell reports whether tmux still observes an interactive shell
// rather than the process sent to the pane.
func PaneCommandIsShell(command string) bool {
	command = strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	switch command {
	case "sh", "bash", "ash", "dash", "zsh", "fish", "ksh", "mksh", "pdksh", "csh", "tcsh",
		"yash", "xonsh", "elvish", "ion", "nu", "oil", "osh", "cmd", "cmd.exe",
		"pwsh", "pwsh.exe", "powershell", "powershell.exe":
		return true
	default:
		return false
	}
}

// ErrPaneOccupied is wrapped by ValidatePaneLaunchBaseline when a launch
// target is already running a non-shell process.
var ErrPaneOccupied = errors.New("pane is occupied by a non-shell process")

// ValidatePaneLaunchBaseline rejects a pane that was already occupied before
// an agent launch. The decision is made from tmux's own `pane_current_command`
// (the foreground process on the pane's tty), not from titles or output
// heuristics: a pane whose foreground process is an interactive shell is idle,
// anything else (an agent CLI, `cat`, `vim`, a build) would receive the launch
// line as keystrokes — `tmux send-keys` succeeds either way, so without this
// preflight the launch command would be typed into the running process and
// spawn would report success. A pane with an empty observation (process died
// instantly under remain-on-exit) is left to the post-launch stability wait.
func ValidatePaneLaunchBaseline(pane Pane) error {
	command := strings.TrimSpace(pane.Command)
	if command == "" || PaneCommandIsShell(command) {
		return nil
	}

	paneID := strings.TrimSpace(pane.ID)
	if paneID == "" {
		paneID = pane.Ref().Physical()
	}
	return fmt.Errorf(
		"pane %s pre-launch current command %q is already a non-shell process (%w); "+
			"refusing to type the launch command into it — free the pane (exit that process) "+
			"or add the agent in a fresh pane instead of reusing this one",
		paneID,
		command,
		ErrPaneOccupied,
	)
}

// ValidatePaneLaunchBaselineLiveContext re-reads the exact pane from tmux
// immediately before a launch and applies ValidatePaneLaunchBaseline to the
// fresh observation. It closes the gap between a batch preflight and the
// send-keys call: identity registration and command preparation can take long
// enough for something else to occupy the pane. A pane that can no longer be
// found, or a tmux read failure, is a refusal — the launch is never retargeted.
func ValidatePaneLaunchBaselineLiveContext(ctx context.Context, session, paneID string) error {
	return validatePaneLaunchBaselineLiveContext(ctx, session, paneID, GetPanesContext)
}

func validatePaneLaunchBaselineLiveContext(ctx context.Context, session, paneID string, listPanes paneProcessLister) error {
	if ctx == nil {
		return errors.New("pane launch preflight requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	session = strings.TrimSpace(session)
	paneID = strings.TrimSpace(paneID)
	if session == "" || paneID == "" {
		return errors.New("pane launch preflight requires a session and an exact pane ID")
	}
	if listPanes == nil {
		return errors.New("pane launch preflight requires a pane lister")
	}
	panes, err := listPanes(ctx, session)
	if err != nil {
		return fmt.Errorf("re-reading pane %s before launch: %w", paneID, err)
	}
	for _, pane := range panes {
		if strings.TrimSpace(pane.ID) != paneID {
			continue
		}
		return ValidatePaneLaunchBaseline(pane)
	}
	return fmt.Errorf("pane %s disappeared from session %s before launch", paneID, session)
}

type paneProcessLister func(context.Context, string) ([]Pane, error)

// WaitForPaneProcessStartContext waits until the same non-shell process is
// observed in the pane twice in succession. The stability requirement prevents
// a command that exits immediately from being reported as a successful launch.
func WaitForPaneProcessStartContext(ctx context.Context, session, paneID string) (Pane, error) {
	return waitForPaneProcessStartContext(
		ctx,
		session,
		paneID,
		defaultPaneProcessStartTimeout,
		defaultPaneProcessStartPoll,
		paneProcessStableObservations,
		GetPanesContext,
	)
}

func waitForPaneProcessStartContext(
	ctx context.Context,
	session, paneID string,
	timeout, pollInterval time.Duration,
	requiredStableObservations int,
	listPanes paneProcessLister,
) (Pane, error) {
	if ctx == nil {
		return Pane{}, errors.New("waiting for pane process start requires a context")
	}
	if session == "" || paneID == "" {
		return Pane{}, errors.New("waiting for pane process start requires a session and pane ID")
	}
	if listPanes == nil {
		return Pane{}, errors.New("waiting for pane process start requires a pane lister")
	}
	if timeout <= 0 {
		timeout = defaultPaneProcessStartTimeout
	}
	if pollInterval <= 0 {
		pollInterval = defaultPaneProcessStartPoll
	}
	if requiredStableObservations < 1 {
		requiredStableObservations = paneProcessStableObservations
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := waitCtx.Deadline()
	stableCommand := ""
	stableObservations := 0
	lastCommand := ""
	foundPane := false
	var lastErr error

	for {
		panes, err := listPanes(waitCtx, session)
		if err != nil {
			lastErr = err
			stableCommand = ""
			stableObservations = 0
		} else {
			lastErr = nil
			foundPane = false
			for _, pane := range panes {
				if pane.ID != paneID {
					continue
				}
				foundPane = true
				lastCommand = strings.TrimSpace(pane.Command)
				// Test the RAW command for emptiness before taking its
				// basename: filepath.Base("") returns ".", not "", so the
				// emptiness check had to happen here or it was dead code.
				// tmux reports an empty pane_current_command for a pane whose
				// process died instantly (missing binary, bad cd) and is held
				// open by remain-on-exit — exactly the case this function
				// exists to catch. Basenaming first turned that into the
				// "stable non-shell process" ".", so two polls later this
				// returned success and callers reported the agent as
				// launched. The empty-command failure detail below was
				// likewise unreachable.
				if lastCommand == "" || PaneCommandIsShell(strings.ToLower(filepath.Base(lastCommand))) {
					stableCommand = ""
					stableObservations = 0
					break
				}
				observedCommand := strings.ToLower(filepath.Base(lastCommand))
				if observedCommand != stableCommand {
					stableCommand = observedCommand
					stableObservations = 1
				} else {
					stableObservations++
				}
				if stableObservations >= requiredStableObservations {
					return pane, nil
				}
				break
			}
			if !foundPane {
				stableCommand = ""
				stableObservations = 0
			}
		}
		if err := ctx.Err(); err != nil {
			return Pane{}, fmt.Errorf("waiting for pane %s process start: %w", paneID, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if pollInterval < remaining {
			remaining = pollInterval
		}
		timer := time.NewTimer(remaining)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if ctx.Err() != nil {
				return Pane{}, fmt.Errorf("waiting for pane %s process start: %w", paneID, ctx.Err())
			}
			remaining = 0
		case <-timer.C:
		}
		if remaining <= 0 {
			break
		}
	}

	detail := "pane was not found"
	if lastErr != nil {
		detail = "last pane observation failed: " + lastErr.Error()
	} else if foundPane && lastCommand == "" {
		detail = "tmux reported an empty current command"
	} else if foundPane {
		detail = fmt.Sprintf("tmux still reported current command %q", lastCommand)
	}
	return Pane{}, fmt.Errorf(
		"pane %s did not keep a non-shell process running for %s: %s; the pane may still exist",
		paneID, timeout, detail,
	)
}

// GetAllPanesContext returns all panes from all sessions, grouped by session name.
func (c *Client) GetAllPanesContext(ctx context.Context) (map[string][]Pane, error) {
	sep := FieldSeparator
	// Add session_name at the beginning
	format := fmt.Sprintf("#{session_name}%[1]s#{pane_id}%[1]s#{pane_index}%[1]s#{pane_title}%[1]s#{pane_current_command}%[1]s#{pane_width}%[1]s#{pane_height}%[1]s#{pane_active}%[1]s#{pane_pid}%[1]s#{window_index}%[1]s#{@ntm_agent_type}", sep)
	output, err := c.RunContext(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		// No server/no sessions is not an error; treat as empty result.
		errMsg := err.Error()
		if strings.Contains(errMsg, "no server running") ||
			strings.Contains(errMsg, "no sessions") ||
			strings.Contains(errMsg, "No such file or directory") ||
			strings.Contains(errMsg, "error connecting to") {
			return map[string][]Pane{}, nil
		}
		return nil, err
	}

	panesBySession := make(map[string][]Pane)

	for lineNumber, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		parts := strings.Split(line, sep)
		if len(parts) != 11 {
			return nil, fmt.Errorf("parse pane %d: expected 11 fields, got %d", lineNumber+1, len(parts))
		}

		sessionName := parts[0]
		// parts[1:] contains: id, index, title, command, width, height, active, pid, window_index, @ntm_agent_type
		// parts[1:8] = id(0), index(1), title(2), command(3), width(4), height(5), active(6)
		// parts[8:] = pid(0), window_index(1), @ntm_agent_type(2)
		p, err := parsePaneFromParts(parts[1:8], parts[8:])
		if err != nil {
			return nil, fmt.Errorf("parse pane %d: %w", lineNumber+1, err)
		}

		panesBySession[sessionName] = append(panesBySession[sessionName], *p)
	}

	return panesBySession, nil
}

// GetAllPanes returns all panes from all sessions (default client)
func GetAllPanes() (map[string][]Pane, error) {
	return DefaultClient.GetAllPanesContext(context.Background())
}

// GetAllPanesContext returns all panes from all sessions while honoring caller
// cancellation (default client).
func GetAllPanesContext(ctx context.Context) (map[string][]Pane, error) {
	return DefaultClient.GetAllPanesContext(ctx)
}

// GetFirstWindow returns the first window index for a session
func (c *Client) GetFirstWindow(session string) (int, error) {
	return c.GetFirstWindowContext(context.Background(), session)
}

// GetFirstWindowContext returns the first window index for a session with
// cancellation support.
func (c *Client) GetFirstWindowContext(ctx context.Context, session string) (int, error) {
	output, err := c.RunContext(ctx, "list-windows", "-t", TargetSession(session), "-F", "#{window_index}")
	if err != nil {
		return 0, err
	}

	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return 0, errors.New("no windows found")
	}

	return strconv.Atoi(lines[0])
}

// GetFirstWindow returns the first window index for a session (default client)
func GetFirstWindow(session string) (int, error) {
	return DefaultClient.GetFirstWindow(session)
}

// GetFirstWindowContext returns the first window index for a session with
// cancellation support using the default client.
func GetFirstWindowContext(ctx context.Context, session string) (int, error) {
	return DefaultClient.GetFirstWindowContext(ctx, session)
}

// SplitWindow creates a new pane in the session
func (c *Client) SplitWindow(session string, directory string) (string, error) {
	return c.SplitWindowContext(context.Background(), session, directory)
}

// SplitWindowContext creates a new pane in the session with cancellation
// support across window discovery, pane creation, and layout selection.
func (c *Client) SplitWindowContext(ctx context.Context, session string, directory string) (string, error) {
	if ctx == nil {
		return "", errors.New("tmux split window context is required")
	}
	firstWin, err := c.GetFirstWindowContext(ctx, session)
	if err != nil {
		return "", err
	}

	target := fmt.Sprintf("%s:%d", session, firstWin)

	// Split and get the new pane ID
	paneID, err := c.RunContext(ctx, "split-window", "-t", ExactTarget(target), "-c", directory, "-P", "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return paneID, fmt.Errorf("tmux pane %q was created before cancellation: %w", paneID, err)
	}

	// Apply tiled layout
	if err := c.RunSilentContext(ctx, "select-layout", "-t", ExactTarget(target), "tiled"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return paneID, fmt.Errorf("tmux pane %q was created before cancellation: %w", paneID, ctxErr)
		}
		return paneID, fmt.Errorf("tmux pane %q was created but applying tiled layout failed: %w", paneID, err)
	}

	return paneID, nil
}

// SplitWindow creates a new pane in the session (default client)
func SplitWindow(session string, directory string) (string, error) {
	return DefaultClient.SplitWindow(session, directory)
}

// SplitWindowContext creates a new pane with cancellation support using the
// default client.
func SplitWindowContext(ctx context.Context, session string, directory string) (string, error) {
	return DefaultClient.SplitWindowContext(ctx, session, directory)
}

// SetPaneTitle sets the title of a pane and disables title changes by programs
// to prevent shells/processes from overwriting NTM's pane naming convention.
func (c *Client) SetPaneTitle(paneID, title string) error {
	return c.SetPaneTitleContext(context.Background(), paneID, title)
}

// SetPaneTitleContext sets a pane title with cancellation covering the title
// mutation, transient retries, and the best-effort allow-set-title mutation.
func (c *Client) SetPaneTitleContext(ctx context.Context, paneID, title string) error {
	if ctx == nil {
		return errors.New("tmux pane title context is required")
	}
	if strings.Contains(title, FieldSeparator) {
		return fmt.Errorf("pane title contains reserved field separator %q", FieldSeparator)
	}
	for _, r := range title {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("pane title contains disallowed control character 0x%02x", r)
		}
	}
	selectErr := c.RunSilentContext(ctx, "select-pane", "-t", ExactTarget(paneID), "-T", title)
	if selectErr != nil && ClassifyCommandError(selectErr).Kind == CommandErrorPaneNotFound {
		// On busy tmux servers, newly-created panes can transiently fail to resolve by ID.
		// Retry briefly to reduce flakiness (especially under `go test`).
		const attempts = 5
		for i := 0; i < attempts && selectErr != nil; i++ {
			if err := waitForSendDelay(ctx, 50*time.Millisecond); err != nil {
				return err
			}
			selectErr = c.RunSilentContext(ctx, "select-pane", "-t", ExactTarget(paneID), "-T", title)
			if selectErr != nil && ClassifyCommandError(selectErr).Kind != CommandErrorPaneNotFound {
				break
			}
		}
	}
	if selectErr != nil {
		return selectErr
	}
	// Disable allow-set-title to prevent programs (shells, node, etc.) from
	// overwriting the pane title via terminal escape sequences (OSC 0/2).
	// This is a per-pane option (requires tmux 3.0+).
	// Errors are non-fatal - the title is already set, and older tmux versions
	// may not support this option.
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = c.RunSilentContext(ctx, "set-option", "-p", "-t", ExactTarget(paneID), "allow-set-title", "off")
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// SetPaneTitle sets the title of a pane (default client)
func SetPaneTitle(paneID, title string) error {
	return DefaultClient.SetPaneTitle(paneID, title)
}

// SetPaneTitleContext sets a pane title with caller cancellation (default client).
func SetPaneTitleContext(ctx context.Context, paneID, title string) error {
	return DefaultClient.SetPaneTitleContext(ctx, paneID, title)
}

// SetPaneAgentIdentityContext records both pieces of pane identity that ntm
// owns: the human-readable title and the durable agent-type pane option. The
// option is authoritative when wrappers or TUIs replace the visible command or
// title, so lifecycle paths that already know the agent type must use this
// helper before launching the agent.
func (c *Client) SetPaneAgentIdentityContext(ctx context.Context, paneID, title string, agentType AgentType) error {
	if ParsePaneAgentTypeOption(string(agentType)) == AgentUnknown {
		return fmt.Errorf("agent type %q is not a known agent type", agentType)
	}
	if title != "" {
		if err := c.SetPaneTitleContext(ctx, paneID, title); err != nil {
			return err
		}
	}
	return c.SetPaneAgentTypeContext(ctx, paneID, agentType)
}

// SetPaneAgentIdentityContext records pane title and durable agent type using
// the default client.
func SetPaneAgentIdentityContext(ctx context.Context, paneID, title string, agentType AgentType) error {
	return DefaultClient.SetPaneAgentIdentityContext(ctx, paneID, title, agentType)
}

// SetPaneAgentIdentity records pane title and durable agent type using the
// default client.
func SetPaneAgentIdentity(paneID, title string, agentType AgentType) error {
	return DefaultClient.SetPaneAgentIdentityContext(context.Background(), paneID, title, agentType)
}

// GetPaneTitle returns the title of a pane
func (c *Client) GetPaneTitle(paneID string) (string, error) {
	return c.Run("display-message", "-p", "-t", ExactTarget(paneID), "#{pane_title}")
}

// GetPaneTitle returns the title of a pane (default client)
func GetPaneTitle(paneID string) (string, error) {
	return DefaultClient.GetPaneTitle(paneID)
}

// PaneTitleSuffix returns the portion of an NTM pane title after the final
// session separator "__". It preserves any variant or tag suffixes.
func PaneTitleSuffix(title string) string {
	title = strings.TrimSpace(title)
	if idx := strings.LastIndex(title, "__"); idx >= 0 && idx+2 < len(title) {
		return title[idx+2:]
	}
	return ""
}

// PaneTitleSession returns the session portion of an NTM pane title before the
// final session separator "__".
func PaneTitleSession(title string) string {
	title = strings.TrimSpace(title)
	if idx := strings.LastIndex(title, "__"); idx >= 0 {
		return title[:idx]
	}
	return ""
}

// Default delays before sending Enter key (milliseconds)
const (
	// DefaultEnterDelay is for AI agent TUIs (Claude, Codex, Gemini) which have
	// their own input buffering and process pasted text quickly.
	//
	// Note: In practice, even "agent" panes may run a plain shell (e.g. tests that
	// set claude/codex/gemini commands to bash). A slightly higher default helps
	// avoid flaky "lost Enter" behavior under load.
	DefaultEnterDelay = 100 * time.Millisecond

	// ShellEnterDelay is for shell panes (bash, zsh, etc.) which may need more
	// time to process pasted text before receiving Enter. Shell input handling
	// can vary based on readline, prompt configuration, and system load.
	ShellEnterDelay = 150 * time.Millisecond
)

// SendKeys sends keys to a pane with the default Enter delay.
func (c *Client) SendKeys(target, keys string, enter bool) error {
	return c.SendKeysContext(context.Background(), target, keys, enter)
}

// SendKeysContext sends keys to a pane with caller cancellation.
func (c *Client) SendKeysContext(ctx context.Context, target, keys string, enter bool) error {
	return c.SendKeysWithDelayContext(ctx, target, keys, enter, DefaultEnterDelay)
}

// escapeTrailingSemicolon protects a payload whose last byte is an unescaped
// semicolon from tmux's command parser.
//
// tmux's cmd_parse_from_arguments treats any argument ending in an unescaped ";"
// as a command terminator, and "--" does not protect it. Verified on tmux 3.5a:
// `send-keys -l -- 'A=x;'` delivers "A=x", `'C=y;;'` delivers "C=y;", and a
// payload of exactly ";" sends nothing at all — while `'D=z\;'` correctly
// delivers "D=z;". Any prompt or command ending in a semicolon was therefore
// silently truncated, and because long payloads are chunked, a chunk boundary
// landing right after a semicolon dropped that byte from the middle of a prompt.
//
// Only a trailing semicolon needs escaping; interior semicolons are delivered
// verbatim. The backslash is tmux parser syntax, not caller data: even when a
// payload already ends in one or more literal backslashes, another backslash
// must be added so tmux consumes the added escape and preserves the original
// bytes. Leaving an apparently "already escaped" payload alone drops one
// caller backslash before delivery.
func escapeTrailingSemicolon(payload string) string {
	if !strings.HasSuffix(payload, ";") {
		return payload
	}
	return payload[:len(payload)-1] + `\;`
}

// SendKeysWithDelayContext sends keys with caller cancellation covering every
// tmux subprocess and the delay before Enter.
func (c *Client) SendKeysWithDelayContext(ctx context.Context, target, keys string, enter bool, enterDelay time.Duration) error {
	if ctx == nil {
		return errors.New("tmux send context is required")
	}
	if enterDelay < 0 {
		return errors.New("tmux Enter delay cannot be negative")
	}
	// Send large payloads in chunks to avoid ARG_MAX limits or tmux buffer issues
	const chunkSize = 4096

	if len(keys) <= chunkSize {
		if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "-l", "--", escapeTrailingSemicolon(keys)); err != nil {
			return err
		}
	} else {
		start := 0
		for start < len(keys) {
			end := start + chunkSize
			if end >= len(keys) {
				end = len(keys)
			} else {
				// Backtrack end until it hits a rune start to avoid splitting multi-byte characters
				for end > start && !utf8.RuneStart(keys[end]) {
					end--
				}
				// If we backtracked all the way (single char > chunkSize?), just split at chunk size
				if end == start {
					end = start + chunkSize
				}
			}

			chunk := keys[start:end]
			if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "-l", "--", escapeTrailingSemicolon(chunk)); err != nil {
				return err
			}
			start = end
		}
	}

	if enter {
		// Delay before Enter to ensure the target has time to process the pasted text.
		// Without this, Enter can be lost due to input buffering.
		// Agent TUIs (Codex, Gemini) need ~50ms; shells may need 150ms or more.
		if err := waitForSendDelay(ctx, enterDelay); err != nil {
			return err
		}
		// Use "Enter" instead of "C-m" (Ctrl+M) because some TUIs (e.g., Codex)
		// distinguish between the Enter key and the carriage return control character.
		return c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter")
	}
	return nil
}

func waitForSendDelay(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// FormatPaneVariant encodes a pane's model and reasoning effort into the single
// variant field of an NTM pane title.
//
// The pane title is the only durable record of what a pane was launched with
// that survives into a respawn — tmux keeps it, and GetPanes parses it back
// into Pane.Variant. Encoding only the model meant a respawn could recover the
// model but never the reasoning effort, so a session spawned with
// `--cod=8:gpt-5.6-terra:high` came back on the config DEFAULT effort after any
// recovery, silently and with no operator signal (bd-qs6rj).
//
// The `model@effort` shape is not new syntax: it is exactly the shorthand
// ParseAgentSpec already accepts for `--cod=N:model@effort`, and '@' is already
// inside the variant charset of paneNameRegex, so no title format or regex
// change is required.
func FormatPaneVariant(model, reasoningEffort string) string {
	model = strings.TrimSpace(model)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if model == "" || reasoningEffort == "" {
		// An effort with no model has nothing to hang off in `model@effort`
		// form, and would parse back as a model named after the effort.
		return model
	}
	return model + "@" + reasoningEffort
}

// ParsePaneVariant splits a variant produced by FormatPaneVariant back into its
// model and reasoning effort. A variant with no '@' is a bare model, which is
// what every pane titled before this encoding existed looks like.
func ParsePaneVariant(variant string) (model, reasoningEffort string) {
	variant = strings.TrimSpace(variant)
	at := strings.LastIndex(variant, "@")
	if at < 0 {
		return variant, ""
	}
	model = strings.TrimSpace(variant[:at])
	reasoningEffort = strings.TrimSpace(variant[at+1:])
	if model == "" || reasoningEffort == "" {
		// Not a model@effort pair — treat the whole thing as a model so a
		// persona or model name that legitimately contains '@' is preserved.
		return variant, ""
	}
	return model, reasoningEffort
}

// FormatPaneName formats a pane title according to NTM convention
func FormatPaneName(session string, agentType string, index int, variant string) string {
	normalizedType := strings.TrimSpace(agentType)
	if canonical := AgentType(normalizedType).Canonical(); canonical.IsValid() {
		normalizedType = string(canonical)
	}

	base := fmt.Sprintf("%s__%s_%d", session, normalizedType, index)
	if variant != "" {
		return fmt.Sprintf("%s_%s", base, variant)
	}
	return base
}

// SendKeys sends keys to a pane (default client)
func SendKeys(target, keys string, enter bool) error {
	return DefaultClient.SendKeys(target, keys, enter)
}

// SendKeysContext sends keys with caller cancellation (default client).
func SendKeysContext(ctx context.Context, target, keys string, enter bool) error {
	return DefaultClient.SendKeysContext(ctx, target, keys, enter)
}

// PasteKeys pastes content to a pane. It delegates to SendKeys, whose literal
// (-l) chunked delivery already handles multi-line content safely; a tmux
// paste-buffer implementation would be an optimization, not a behavior change.
func (c *Client) PasteKeys(target, content string, enter bool) error {
	return c.PasteKeysContext(context.Background(), target, content, enter)
}

// PasteKeysContext pastes content with caller cancellation.
func (c *Client) PasteKeysContext(ctx context.Context, target, content string, enter bool) error {
	return c.SendKeysContext(ctx, target, content, enter)
}

// PasteKeysWithDelay pastes content to a pane with a configurable delay before Enter.
func (c *Client) PasteKeysWithDelay(target, content string, enter bool, enterDelay time.Duration) error {
	return c.PasteKeysWithDelayContext(context.Background(), target, content, enter, enterDelay)
}

// PasteKeysWithDelayContext pastes content with a cancellable Enter delay.
func (c *Client) PasteKeysWithDelayContext(ctx context.Context, target, content string, enter bool, enterDelay time.Duration) error {
	return c.SendKeysWithDelayContext(ctx, target, content, enter, enterDelay)
}

// PasteKeys pastes content to a pane (default client)
func PasteKeys(target, content string, enter bool) error {
	return DefaultClient.PasteKeys(target, content, enter)
}

// PasteKeysWithDelay pastes content to a pane with a configurable delay (default client)
func PasteKeysWithDelay(target, content string, enter bool, enterDelay time.Duration) error {
	return DefaultClient.PasteKeysWithDelay(target, content, enter, enterDelay)
}

// PasteKeysWithDelayContext pastes content with a cancellable delay (default client).
func PasteKeysWithDelayContext(ctx context.Context, target, content string, enter bool, enterDelay time.Duration) error {
	return DefaultClient.PasteKeysWithDelayContext(ctx, target, content, enter, enterDelay)
}

// SendBuffer sends content to a pane using tmux's load-buffer + paste-buffer mechanism.
// This is the correct way to send multi-line content to agents like Gemini that interpret
// newlines in send-keys as actual Enter key presses (causing "quote mode" or similar issues).
//
// Unlike SendKeys which uses send-keys -l (literal mode), this method:
// 1. Loads the content into a tmux buffer
// 2. Pastes the buffer into the target pane
// 3. Optionally sends Enter after the paste
//
// This preserves newlines as data rather than as key presses, which is essential for
// multi-line prompts in Gemini's TUI.
func (c *Client) SendBuffer(target, content string, enter bool) error {
	return c.SendBufferContext(context.Background(), target, content, enter)
}

// SendBufferContext sends buffer content with caller cancellation.
func (c *Client) SendBufferContext(ctx context.Context, target, content string, enter bool) error {
	return c.SendBufferWithDelayContext(ctx, target, content, enter, DefaultEnterDelay)
}

// SendBufferWithDelayContext sends content using the buffer mechanism with
// cancellation covering load, paste, delay, and Enter.
func (c *Client) SendBufferWithDelayContext(ctx context.Context, target, content string, enter bool, enterDelay time.Duration) error {
	if ctx == nil {
		return errors.New("tmux buffer send context is required")
	}
	if enterDelay < 0 {
		return errors.New("tmux Enter delay cannot be negative")
	}
	bufferName := newSendBufferName()

	// Load content into a tmux buffer
	// We use 'load-buffer' with stdin to handle arbitrary content including special characters
	if c.Remote == "" {
		// Local: use load-buffer with a pipe
		if err := c.loadBufferLocalContext(ctx, bufferName, content); err != nil {
			c.cleanupBuffer(bufferName)
			return fmt.Errorf("load buffer: %w", err)
		}
	} else {
		// Remote: need to escape content for ssh
		if err := c.loadBufferRemoteContext(ctx, bufferName, content); err != nil {
			c.cleanupBuffer(bufferName)
			return fmt.Errorf("load buffer (remote): %w", err)
		}
	}

	// Paste the buffer into the target pane
	// -p = paste from buffer, -d = delete buffer after pasting, -b = buffer name
	if err := c.RunSilentContext(ctx, "paste-buffer", "-p", "-d", "-b", bufferName, "-t", ExactTarget(target)); err != nil {
		// Cleanup uses its own short context because the delivery context may be
		// canceled. It can only delete the private buffer; it never submits Enter.
		c.cleanupBuffer(bufferName)
		return fmt.Errorf("paste buffer: %w", err)
	}

	if enter {
		if err := waitForSendDelay(ctx, enterDelay); err != nil {
			return err
		}
		return c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter")
	}
	return nil
}

// newSendBufferName returns a tmux buffer name that is unique per send.
// The PID makes the name unique across concurrent ntm processes sharing one
// tmux server (each process starts its bufferSeq at 1, so two processes'
// first sends landing in the same nanosecond could otherwise collide and
// paste-buffer -d one another's buffers); the timestamp plus the atomic
// counter make it unique across goroutines within a process. Every send —
// including a caller-level retry of a failed send — allocates a fresh name,
// so no retry can race a prior attempt's delete-after-paste.
func newSendBufferName() string {
	return fmt.Sprintf("ntm-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), bufferSeq.Add(1))
}

func (c *Client) cleanupBuffer(bufferName string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.RunSilentContext(cleanupCtx, "delete-buffer", "-b", bufferName)
}

// loadBufferLocalContext loads content into a tmux buffer using stdin.
func (c *Client) loadBufferLocalContext(ctx context.Context, bufferName, content string) error {
	binary := BinaryPath()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "load-buffer", "-b", bufferName, "-")
	cmd.Stdin = strings.NewReader(content)
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%s load-buffer: %w: %s", binary, err, stderr.String())
	}
	return nil
}

// loadBufferRemoteContext loads content into a remote tmux buffer.
func (c *Client) loadBufferRemoteContext(ctx context.Context, bufferName, content string) error {
	// For remote, we need to pipe the content through ssh's stdin
	// instead of passing it on the command line to avoid ARG_MAX limits.
	remoteCmd := fmt.Sprintf("tmux load-buffer -b %s -", ShellQuote(bufferName))
	sshArgs := []string{"--", c.Remote, "/bin/sh", "-c", ShellQuote(remoteCmd)}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = strings.NewReader(content)
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("ssh load-buffer: %w: %s", err, stderr.String())
	}
	return nil
}

// SendBuffer sends content using the buffer mechanism (default client)
func SendBuffer(target, content string, enter bool) error {
	return DefaultClient.SendBuffer(target, content, enter)
}

// SendKeysForAgent sends keys to a pane using the appropriate method for the agent type.
// It uses buffer-based paste for agents/content combinations that misbehave with raw
// send-keys (for example multiline Claude/Gemini prompts or large/multiline Codex prompts).
// Other combinations use the standard send-keys path.
func (c *Client) SendKeysForAgent(target, keys string, enter bool, agentType AgentType) error {
	return c.SendKeysForAgentContext(context.Background(), target, keys, enter, agentType)
}

// SendKeysForAgentContext sends keys using the agent-appropriate mechanism
// with caller cancellation.
func (c *Client) SendKeysForAgentContext(ctx context.Context, target, keys string, enter bool, agentType AgentType) error {
	return c.SendKeysForAgentWithDelayContext(ctx, target, keys, enter, DefaultEnterDelay, agentType)
}

// SendKeysForAgentWithDelayContext sends keys using the appropriate mechanism
// with cancellation covering all subprocesses and delays.
func (c *Client) SendKeysForAgentWithDelayContext(ctx context.Context, target, keys string, enter bool, enterDelay time.Duration, agentType AgentType) error {
	// Use buffer-based paste when the agent/content combination needs it.
	// This avoids newline interpretation issues and large-paste truncation quirks.
	if needsBufferSend(agentType, keys) {
		return c.SendBufferWithDelayContext(ctx, target, keys, enter, enterDelay)
	}
	return c.SendKeysWithDelayContext(ctx, target, keys, enter, enterDelay)
}

// canonicalAgentType folds user-facing aliases into the canonical short codes used
// throughout pane metadata and agent-specific send behavior.
func canonicalAgentType(agentType AgentType) AgentType {
	return AgentType(agent.AgentType(agentType).Canonical())
}

// claudeFilenamePattern matches a bare "name.ext" filename token (e.g. README.md,
// config.yaml) anywhere in the content. The token must be bounded by non-word
// characters so that prose like "the end. Then..." or version strings embedded in
// words do not match. Both sides of the dot must be alphanumeric/underscore/hyphen
// runs to look like an actual filename rather than ordinary punctuation.
var claudeFilenamePattern = regexp.MustCompile(`(^|[^\w.])[\w-]+\.[\w-]+`)

// claudeAutocompleteRisk reports whether a single-line Claude prompt contains a
// token that can trigger Claude Code's TUI file/@-mention autocomplete picker:
//   - "/"  — a path separator (e.g. ".orch-dispatch/x.txt", src/main.go)
//   - "@"  — an @-mention / @file reference
//   - "name.ext" — a bare filename (e.g. README.md)
//
// When any of these are typed char-by-char via send-keys, the picker can pop up
// mid-token and steal the trailing Enter, so the prompt never submits. Such
// prompts are routed through the atomic buffer (bracketed-paste) path instead.
func claudeAutocompleteRisk(content string) bool {
	if strings.ContainsAny(content, "/@") {
		return true
	}
	return claudeFilenamePattern.MatchString(content)
}

// needsBufferSend returns true if the content should be sent via buffer mechanism
// rather than send-keys, based on agent type and content.
func needsBufferSend(agentType AgentType, content string) bool {
	// Gemini and Codex need buffer-based sending for multi-line content or large prompts.
	// Gemini's TUI interprets newlines in send-keys as actual Enter presses.
	// Codex uses bracketed paste mode and shows "[Pasted Content N chars]" instead of
	// actual content when receiving large send-keys input, and may not auto-execute.
	switch canonicalAgentType(agentType) {
	case AgentClaude:
		// Use buffer if content contains newlines — send-keys -l silently strips
		// newlines (tmux 3.6+), while paste-buffer converts them to CR which
		// Claude Code interprets as Enter, enabling multi-line prompt submission.
		//
		// Also use buffer for single-line prompts that contain autocomplete-
		// triggering tokens (a path separator, an @-mention, or a name.ext
		// filename pattern). Sending these char-by-char via send-keys lets
		// Claude Code's TUI file/@-mention picker pop up mid-token, so the
		// trailing Enter selects a menu entry instead of submitting the prompt
		// and the dispatched work silently never starts. Bracketed paste
		// delivers the whole prompt atomically, avoiding the picker race. (#198)
		return strings.Contains(content, "\n") || claudeAutocompleteRisk(content)
	case AgentGemini, AgentAntigravity:
		// agy (Antigravity) reuses Gemini's TUI send behavior: use buffer if
		// content contains newlines.
		return strings.Contains(content, "\n")
	case AgentCodex:
		// Use buffer for Codex when content contains newlines or is large (>512 chars)
		// This avoids the "[Pasted Content N chars]" truncation and auto-execute issues
		return strings.Contains(content, "\n") || len(content) > 512
	default:
		return false
	}
}

// SendKeysForAgent sends keys using the appropriate method for the agent type (default client)
func SendKeysForAgent(target, keys string, enter bool, agentType AgentType) error {
	return DefaultClient.SendKeysForAgent(target, keys, enter, agentType)
}

// SendKeysForAgentContext sends keys with caller cancellation (default client).
func SendKeysForAgentContext(ctx context.Context, target, keys string, enter bool, agentType AgentType) error {
	return DefaultClient.SendKeysForAgentContext(ctx, target, keys, enter, agentType)
}

// SendKeysForAgentWithDelayContext sends keys with a cancellable delay (default client).
func SendKeysForAgentWithDelayContext(ctx context.Context, target, keys string, enter bool, enterDelay time.Duration, agentType AgentType) error {
	return DefaultClient.SendKeysForAgentWithDelayContext(ctx, target, keys, enter, enterDelay, agentType)
}

const (
	// DoubleEnterFirstDelay is the pause after sending text before the first Enter.
	DoubleEnterFirstDelay = 1 * time.Second
	// DoubleEnterSecondDelay is the pause between the first and second Enter.
	DoubleEnterSecondDelay = 500 * time.Millisecond
)

// SendKeysForAgentDoubleEnter sends text to an agent pane using the double-Enter
// submission protocol: send text (no enter), wait 1s, Enter, wait 500ms, Enter.
// This is the reliable way to submit prompts to CLI agents (Claude, Codex, Gemini)
// that need the double-Enter to confirm submission.
func SendKeysForAgentDoubleEnter(target, keys string, agentType AgentType) error {
	return SendKeysForAgentDoubleEnterContext(context.Background(), target, keys, agentType)
}

// SendKeysForAgentDoubleEnterContext runs the double-Enter submission protocol
// with cancellation checks between staging and both Enter presses.
func SendKeysForAgentDoubleEnterContext(ctx context.Context, target, keys string, agentType AgentType) error {
	return DefaultClient.SendKeysForAgentDoubleEnterContext(ctx, target, keys, agentType)
}

// SendKeysForAgentDoubleEnterContext runs the double-Enter submission protocol
// on this client with caller cancellation.
func (c *Client) SendKeysForAgentDoubleEnterContext(ctx context.Context, target, keys string, agentType AgentType) error {
	// Send the text without pressing Enter
	if err := c.SendKeysForAgentWithDelayContext(ctx, target, keys, false, 0, agentType); err != nil {
		return err
	}
	if err := waitForSendDelay(ctx, DoubleEnterFirstDelay); err != nil {
		return err
	}
	// First Enter
	if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter"); err != nil {
		return err
	}
	if err := waitForSendDelay(ctx, DoubleEnterSecondDelay); err != nil {
		return err
	}
	// Second Enter
	if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter"); err != nil {
		return err
	}
	return nil
}

const (
	// codexVerifyInitialDelay is the pause after the double-Enter protocol
	// before the first submission check, giving codex time to either enter
	// its working state or re-render the composer with the unsubmitted text.
	codexVerifyInitialDelay = 700 * time.Millisecond
	// codexVerifyPollInterval spaces the post-rescue submission checks.
	codexVerifyPollInterval = 500 * time.Millisecond
	// codexVerifyMaxPolls bounds the post-rescue verification loop.
	codexVerifyMaxPolls = 6
)

// codexComposerHoldsPayload reports whether a codex pane capture shows the
// delivered message still sitting unsubmitted in the TUI composer. The
// composer renders as a line starting with the "›" prompt marker; an
// unsubmitted delivery shows either the message text itself or codex's
// "[Pasted ..." stand-in there. Idle suggestion hints also render after "›",
// so composer text alone is not evidence — the line must carry the payload
// snippet or a paste marker.
// Composer-clear choreography (ntm-5p0b). AP-16 doctrine — "always Escape
// Escape Escape C-u before sending fresh prompts into any codex pane that had
// a prior interrupt" — was operator muscle memory driven with raw tmux
// send-keys. The sequence is per-agent because the same keys mean different
// things per TUI: codex tolerates the triple-Escape ritual; Claude Code maps
// double-Escape to history jump-back, so it gets a single Escape; unknown
// TUIs get only the line-kill.
const (
	composerClearKeyGap     = 120 * time.Millisecond
	composerClearVerifyWait = 300 * time.Millisecond
)

// ComposerClearKeys returns the pre-send composer clear sequence for an agent
// type. Exported for sequence-composition tests.
func ComposerClearKeys(agentType AgentType) []string {
	switch agentType.Canonical() {
	case AgentCodex:
		return []string{"Escape", "Escape", "Escape", "C-u"}
	case AgentClaude:
		return []string{"Escape", "C-u"}
	default:
		return []string{"C-u"}
	}
}

// composerMarkersForAgent returns the live-composer marker glyphs used to
// verify emptiness, or nil when the TUI has no known marker. Codex has TWO
// markers: "›" (U+203A) at every reasoning effort except ultra, and "»"
// (U+00BB) at effort ultra — codex's EffortTier::prompt_glyph swaps the
// composer glyph with the effort (GH#273), so a gate that only knows "›"
// refuses every delivery to an ultra pane.
func composerMarkersForAgent(agentType AgentType) []string {
	switch agentType.Canonical() {
	case AgentCodex:
		return []string{"›", "»"}
	case AgentClaude:
		return []string{"❯"}
	case AgentGrok:
		// Grok Build renders a bordered composer ("│ ❯ <text>  │"); the same
		// heavy chevron is the live-composer marker. Lines are trimmed of the
		// trailing box border before emptiness checks (see composerLineEmpty).
		return []string{"❯"}
	default:
		return nil
	}
}

// indexAnyMarker returns the byte index and glyph of the leftmost occurrence
// of any of the markers in line, or (-1, "") when none appears.
func indexAnyMarker(line string, markers []string) (int, string) {
	best, bestMarker := -1, ""
	for _, m := range markers {
		if m == "" {
			continue
		}
		if j := strings.Index(line, m); j >= 0 && (best < 0 || j < best) {
			best, bestMarker = j, m
		}
	}
	return best, bestMarker
}

// composerPlaceholderPrefixes lists per-agent hint text the TUI renders in
// an EMPTY composer (dim, but indistinguishable from real text in a plain
// capture). Text matching a prefix counts as empty.
func composerPlaceholderPrefixes(agentType AgentType) []string {
	switch agentType.Canonical() {
	case AgentClaude:
		return []string{`Try "`}
	case AgentCodex:
		return []string{"Ask Codex"}
	default:
		return nil
	}
}

// composerLineEmpty reports whether the bottom-most marker line in the
// capture carries no composer text. found=false means no marker line was
// visible at all (verification impossible). Hint text the TUI renders in an
// empty composer counts as empty.
func composerLineEmpty(capture string, markers []string, placeholderPrefixes []string) (found, empty bool) {
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		idx, marker := indexAnyMarker(lines[i], markers)
		if idx < 0 {
			continue
		}
		text := strings.TrimSpace(lines[i][idx+len(marker):])
		// Grok Build's composer is a bordered box, so the marker line ends
		// with the box's right border ("│ ❯ <text>   │"). Strip trailing
		// border glyphs before the emptiness check; harmless for TUIs whose
		// composer line has no right border.
		text = strings.TrimSpace(strings.TrimRight(text, "│"))
		if text == "" {
			return true, true
		}
		for _, prefix := range placeholderPrefixes {
			if strings.HasPrefix(text, prefix) {
				return true, true
			}
		}
		return true, false
	}
	return false, false
}

// ComposerState is the message-independent view of an agent pane's input
// box, derived from a pane capture (bd-v8dqd). It lets observability
// surfaces report "this pane holds an unsubmitted prompt" or "this pane has
// queued messages" without knowing what was sent — the blind spot behind
// panes that are simultaneously idle, blocking a swarm, and holding orders
// unread.
type ComposerState struct {
	// MarkerVisible: the agent's composer marker (❯ for Claude, › for
	// codex) appears in the capture. False for agent types without a known
	// marker, or when the pane shows a splash/dialog instead of the input
	// box.
	MarkerVisible bool `json:"marker_visible"`
	// HoldsText: the bottom-most marker line carries real text (TUI hint
	// text counts as empty) — a prompt is sitting unsubmitted in the box.
	HoldsText bool `json:"holds_text"`
	// QueuedMessages: the TUI reports queued unsubmitted messages (Claude's
	// "Press up to edit queued messages" footer).
	QueuedMessages bool `json:"queued_messages"`
}

// queuedMessagesMarkers are TUI phrases (lowercase) that appear on the
// LIVE footer when the agent is holding queued, not-yet-processed messages.
// Only screen-verbatim phrases belong here: matching a generic substring
// like "queued messages" would latch the flag whenever the agent's recent
// output merely mentions the phrase (editing this file, discussing the
// feature), and orchestrators withhold work from panes that appear to hold
// queued input.
var queuedMessagesMarkers = []string{
	"press up to edit queued messages",
}

// queuedMarkerTailLines bounds the queued-footer scan to the rows where the
// live footer actually renders. Callers pass scrollback-inclusive captures
// (50-500 lines), so an unanchored scan would resurrect a footer that
// scrolled away hours ago.
const queuedMarkerTailLines = 10

// InspectComposer derives the message-independent composer state from a
// pane capture. The composer is bottom-pinned in both Claude Code and
// codex, so the bottom-most marker line is the live input box even in a
// capture that includes scrollback.
func InspectComposer(capture string, agentType AgentType) ComposerState {
	var state ComposerState
	tailLines := strings.Split(capture, "\n")
	if len(tailLines) > queuedMarkerTailLines {
		tailLines = tailLines[len(tailLines)-queuedMarkerTailLines:]
	}
	lower := strings.ToLower(strings.Join(tailLines, "\n"))
	for _, marker := range queuedMessagesMarkers {
		if strings.Contains(lower, marker) {
			state.QueuedMessages = true
			break
		}
	}
	markers := composerMarkersForAgent(agentType)
	if len(markers) == 0 {
		return state
	}
	found, empty := composerLineEmpty(capture, markers, composerPlaceholderPrefixes(agentType))
	state.MarkerVisible = found
	state.HoldsText = found && !empty
	return state
}

// ComposerReadyForDelivery reports whether an agent pane is in a state that
// can accept a typed prompt (bd-dp9oy). Sends fired while a Claude/codex
// TUI is still initializing — or while it is showing a trust dialog or menu
// instead of its input box — are silently swallowed: the keystrokes land
// nowhere recoverable and the send still reports success. Refusing up front
// turns that silent drop into a typed failure the caller can retry.
//
// The check is deliberately conservative: it only reports not-ready when a
// capture POSITIVELY shows neither the composer marker nor working-state
// chrome for an agent type whose marker we know. Unknown agent types, empty
// captures, and capture errors all return ready=true (fail open) so a
// detection gap can never block deliveries that used to work.
//
// paneWidth is the real tmux pane width used by the width-adaptive working
// detectors (bd-eeifh); pass 0 when unknown.
func (c *Client) ComposerReadyForDelivery(ctx context.Context, target string, agentType AgentType, paneWidth int) (ready bool, reason string) {
	canonical := agentType.Canonical()
	markers := composerMarkersForAgent(canonical)
	if len(markers) == 0 {
		return true, ""
	}
	capture, err := c.CapturePaneVisibleContext(ctx, target)
	if err != nil || strings.TrimSpace(capture) == "" {
		return true, ""
	}
	if composerVisibleForDelivery(capture, markers) {
		return true, ""
	}
	switch canonical {
	case AgentClaude:
		if agent.ClaudeActivelyWorking(capture, paneWidth) {
			return true, ""
		}
	case AgentCodex:
		if codexLooksWorking(capture) {
			return true, ""
		}
	case AgentGrok:
		if grokLooksWorking(capture) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("%s composer not visible: pane appears to be initializing or showing a dialog; a typed prompt would be swallowed", canonical)
}

// ComposerReadyForDelivery checks delivery readiness (default client).
func ComposerReadyForDelivery(ctx context.Context, target string, agentType AgentType, paneWidth int) (bool, string) {
	return DefaultClient.ComposerReadyForDelivery(ctx, target, agentType, paneWidth)
}

// dialogOptionLineRe matches the text of a numbered dialog option row after
// the marker glyph has been stripped ("1. Update now (runs `bun install -g
// @openai/codex`)"). Codex renders its update dialog's selected option with
// the same "›" cursor glyph as the composer prompt, so a whole-screen
// substring match accepted the dialog as a ready composer and a robot send's
// Enter selected "Update now" — an unintended self-update (GH#273 root cause
// 3; same class as the Claude-menu half of GH#241).
var dialogOptionLineRe = regexp.MustCompile(`^\d{1,2}[.)]\s`)

// dialogChromeScanLines bounds how far below the marker line the delivery
// gate looks for dialog chrome like "Press enter to continue". Dialogs render
// that hint directly under their option list, so a short window is enough and
// keeps transcript text far above the composer from vetoing a live pane.
const dialogChromeScanLines = 6

// composerVisibleForDelivery reports whether the capture POSITIVELY shows a
// live composer rather than dialog chrome reusing the marker glyph. Only the
// BOTTOM-MOST marker line counts — the composer is bottom-pinned in every
// supported TUI, and transcript echoes render the marker higher up. A marker
// line whose text is a numbered dialog option, or whose frame shows "Press
// enter to continue" at or just below the marker, is a selection cursor, not
// an input box (GH#273 root cause 3).
func composerVisibleForDelivery(capture string, markers []string) bool {
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		idx, marker := indexAnyMarker(lines[i], markers)
		if idx < 0 {
			continue
		}
		text := strings.TrimSpace(lines[i][idx+len(marker):])
		if dialogOptionLineRe.MatchString(text) {
			return false
		}
		for j := i; j < len(lines) && j <= i+dialogChromeScanLines; j++ {
			if strings.Contains(strings.ToLower(lines[j]), "press enter to continue") {
				return false
			}
		}
		return true
	}
	return false
}

// ClearComposerContext performs the per-agent pre-send composer clear and
// verifies the composer is empty where the TUI exposes a marker. Returns
// cleared=false only when verification POSITIVELY shows leftover text;
// verified=false means the sequence was sent but emptiness could not be
// confirmed (no marker visible / unknown TUI).
func (c *Client) ClearComposerContext(ctx context.Context, target string, agentType AgentType) (cleared, verified bool, err error) {
	for i, key := range ComposerClearKeys(agentType) {
		if i > 0 {
			if err := waitForSendDelay(ctx, composerClearKeyGap); err != nil {
				return false, false, err
			}
		}
		if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), key); err != nil {
			return false, false, fmt.Errorf("send composer clear key %q: %w", key, err)
		}
	}
	markers := composerMarkersForAgent(agentType)
	if len(markers) == 0 {
		return true, false, nil
	}
	if err := waitForSendDelay(ctx, composerClearVerifyWait); err != nil {
		return false, false, err
	}
	capture, err := c.CapturePaneVisibleContext(ctx, target)
	if err != nil {
		return false, false, fmt.Errorf("capture pane for composer clear verification: %w", err)
	}
	found, empty := composerLineEmpty(capture, markers, composerPlaceholderPrefixes(agentType))
	if !found {
		return true, false, nil
	}
	return empty, true, nil
}

// ClearComposerContext clears the composer using the default client.
func ClearComposerContext(ctx context.Context, target string, agentType AgentType) (bool, bool, error) {
	return DefaultClient.ClearComposerContext(ctx, target, agentType)
}

func codexComposerHoldsPayload(capture, message string) bool {
	snippet := ""
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 32 {
				line = line[:32]
			}
			snippet = line
			break
		}
	}
	// Only the BOTTOM-MOST marker line is the live composer. Codex also echoes
	// submitted user messages into the transcript with a "›" prefix, so any
	// earlier match is history — treating it as composer content would make a
	// successfully submitted prompt look permanently stuck and fail the
	// delivery falsely. Both codex glyphs count: "›" everywhere, "»" at
	// reasoning effort ultra (GH#273).
	markers := composerMarkersForAgent(AgentCodex)
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		idx, marker := indexAnyMarker(line, markers)
		if idx < 0 {
			continue
		}
		composerText := strings.TrimSpace(line[idx+len(marker):])
		if composerText == "" {
			return false
		}
		if strings.Contains(composerText, "[Pasted") {
			return true
		}
		return snippet != "" && strings.Contains(line, snippet)
	}
	return false
}

// codexLooksWorking reports whether the capture shows codex's working footer.
func codexLooksWorking(capture string) bool {
	return strings.Contains(strings.ToLower(capture), "esc to interrupt")
}

// grokActivityLineRe matches Grok Build's live activity line — a braille
// spinner frame plus one of the three observed phase verbs ("⠹ Waiting for
// response… 0.7s" / "⠸ Thinking… 0.0s" / "⠙ Responding… 1.7s"). Rendered only
// while a turn is in flight (verified live against grok 1.0.5, GH#251).
var grokActivityLineRe = regexp.MustCompile(`(?m)^\s*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*(?:Waiting for response|Thinking|Responding)…`)

// grokLooksWorking reports whether the capture shows Grok Build's in-flight
// chrome: the spinner activity line or the "Esc:cancel" footer hint, both of
// which disappear at idle.
func grokLooksWorking(capture string) bool {
	return grokActivityLineRe.MatchString(capture) || strings.Contains(capture, "Esc:cancel")
}

// grokComposerHoldsPayload reports whether a Grok Build pane capture shows the
// delivered message still sitting unsubmitted in the bordered composer. Grok
// pins its "│ ❯ <text> │" composer to the bottom of the screen and echoes
// submitted prompts into the transcript with a plain "❯" prefix, so only the
// BOTTOM-MOST marker line is live-composer evidence; the trailing box border
// is stripped before evaluation.
func grokComposerHoldsPayload(capture, message string) bool {
	snippet := ""
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 32 {
				line = line[:32]
			}
			snippet = line
			break
		}
	}
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		marker := strings.Index(line, "❯")
		if marker < 0 {
			continue
		}
		composerText := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[marker+len("❯"):]), "│"))
		if composerText == "" {
			return false
		}
		if strings.Contains(composerText, "[Pasted") {
			return true
		}
		return snippet != "" && strings.Contains(line, snippet)
	}
	return false
}

// VerifyGrokSubmissionContext confirms that a prompt delivered to a Grok Build
// pane actually submitted (GH#251 phase 2). Mirrors the codex verifier: when
// the payload is still visibly sitting in the composer after the double-Enter
// protocol, the rescue is one extra Enter, then bounded polling. Returns
// (confirmed, rescued); confirmed=false means the payload is still visibly
// unsubmitted and callers must not report the send as delivered.
//
// paneWidth is accepted for signature parity with the other verifiers; grok's
// working check is a full-capture match and does not depend on it.
func (c *Client) VerifyGrokSubmissionContext(ctx context.Context, target, message string, paneWidth int) (bool, bool, error) {
	if err := waitForSendDelay(ctx, codexVerifyInitialDelay); err != nil {
		return false, false, err
	}
	capture, err := c.CapturePaneVisibleContext(ctx, target)
	if err != nil {
		return false, false, fmt.Errorf("capture grok pane for submission verification: %w", err)
	}
	if grokLooksWorking(capture) || !grokComposerHoldsPayload(capture, message) {
		return true, false, nil
	}

	// The composer still holds the payload: finish the job with one bare
	// Enter, then poll for the submission to take effect. An extra Enter into
	// an already-submitted grok composer is a no-op (verified live).
	if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter"); err != nil {
		return false, true, fmt.Errorf("send rescue Enter to grok pane: %w", err)
	}
	for poll := 0; poll < codexVerifyMaxPolls; poll++ {
		if err := waitForSendDelay(ctx, codexVerifyPollInterval); err != nil {
			return false, true, err
		}
		capture, err = c.CapturePaneVisibleContext(ctx, target)
		if err != nil {
			return false, true, fmt.Errorf("capture grok pane after rescue Enter: %w", err)
		}
		if grokLooksWorking(capture) || !grokComposerHoldsPayload(capture, message) {
			return true, true, nil
		}
	}
	return false, true, nil
}

// VerifyGrokSubmissionContext verifies grok prompt submission (default client).
func VerifyGrokSubmissionContext(ctx context.Context, target, message string, paneWidth int) (bool, bool, error) {
	return DefaultClient.VerifyGrokSubmissionContext(ctx, target, message, paneWidth)
}

// VerifyCodexSubmissionContext confirms that a prompt delivered to a codex
// pane actually submitted, rescuing it with one extra Enter when the TUI
// consumed the protocol's Enter as part of the bracketed paste (ntm-8ubn).
// Returns (confirmed, rescued): confirmed=false means the payload is still
// visibly sitting in the composer after the rescue attempt, so callers must
// not report the send as delivered. A capture failure returns an error and
// makes no claim either way.
//
// paneWidth is accepted for signature parity with the Claude verifier so
// callers can thread one real pane width (bd-eeifh); codex's working check
// is a full-capture substring match and does not depend on it.
func (c *Client) VerifyCodexSubmissionContext(ctx context.Context, target, message string, paneWidth int) (bool, bool, error) {
	if err := waitForSendDelay(ctx, codexVerifyInitialDelay); err != nil {
		return false, false, err
	}
	// Visible-screen capture only: a scrollback capture can resurrect a stale
	// "esc to interrupt" footer and misclassify an unsubmitted composer as
	// working (see CapturePaneVisible).
	capture, err := c.CapturePaneVisibleContext(ctx, target)
	if err != nil {
		return false, false, fmt.Errorf("capture codex pane for submission verification: %w", err)
	}
	if codexLooksWorking(capture) || !codexComposerHoldsPayload(capture, message) {
		return true, false, nil
	}

	// The composer still holds the payload: finish the job with one bare
	// Enter, then poll for the submission to take effect.
	if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter"); err != nil {
		return false, true, fmt.Errorf("send rescue Enter to codex pane: %w", err)
	}
	for poll := 0; poll < codexVerifyMaxPolls; poll++ {
		if err := waitForSendDelay(ctx, codexVerifyPollInterval); err != nil {
			return false, true, err
		}
		capture, err = c.CapturePaneVisibleContext(ctx, target)
		if err != nil {
			return false, true, fmt.Errorf("capture codex pane after rescue Enter: %w", err)
		}
		if codexLooksWorking(capture) || !codexComposerHoldsPayload(capture, message) {
			return true, true, nil
		}
	}
	return false, true, nil
}

// VerifyCodexSubmissionContext verifies codex prompt submission (default client).
func VerifyCodexSubmissionContext(ctx context.Context, target, message string, paneWidth int) (bool, bool, error) {
	return DefaultClient.VerifyCodexSubmissionContext(ctx, target, message, paneWidth)
}

// claudeComposerHoldsPayload reports whether a Claude Code pane capture shows
// the delivered message still sitting unsubmitted in the input box. Claude
// Code pins its live composer — the "❯" prompt — to the bottom of the screen;
// past user turns render with a plain ">" prefix, so only the bottom-most "❯"
// line is composer evidence. As with codex, composer text alone is not
// payload: the line must carry the message snippet or a "[Pasted" stand-in.
func claudeComposerHoldsPayload(capture, message string) bool {
	snippet := ""
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 32 {
				line = line[:32]
			}
			snippet = line
			break
		}
	}
	lines := strings.Split(capture, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		marker := strings.Index(line, "❯")
		if marker < 0 {
			continue
		}
		composerText := strings.TrimSpace(line[marker+len("❯"):])
		if composerText == "" {
			return false
		}
		if strings.Contains(composerText, "[Pasted") {
			return true
		}
		return snippet != "" && strings.Contains(line, snippet)
	}
	return false
}

// VerifyClaudeSubmissionContext confirms that a prompt delivered to a Claude
// Code pane actually submitted (GH#241 / ntm-utq6). Claude's autocomplete /
// @-mention picker can swallow the protocol's Enter as a menu selection,
// stranding the prompt in the composer while the send reports success. When
// the composer still holds the payload, the rescue is the documented human
// recipe: one Escape (dismisses an open picker; inert against plain composer
// text) followed by Enter, then bounded polling. Returns (confirmed, rescued);
// confirmed=false means the payload is still visibly unsubmitted and callers
// must not report the send as delivered.
//
// paneWidth is the real tmux pane width used by the width-adaptive Claude
// working detector (bd-eeifh); pass 0 when unknown.
func (c *Client) VerifyClaudeSubmissionContext(ctx context.Context, target, message string, paneWidth int) (bool, bool, error) {
	if err := waitForSendDelay(ctx, codexVerifyInitialDelay); err != nil {
		return false, false, err
	}
	capture, err := c.CapturePaneVisibleContext(ctx, target)
	if err != nil {
		return false, false, fmt.Errorf("capture claude pane for submission verification: %w", err)
	}
	if agent.ClaudeActivelyWorking(capture, paneWidth) || !claudeComposerHoldsPayload(capture, message) {
		return true, false, nil
	}

	// Confirm the stranded state on a second capture before rescuing: unlike
	// codex's benign extra Enter, Escape against a Claude pane that started
	// working in the gap since the first capture would interrupt the running
	// turn. Two consecutive stranded observations shrink that race window to
	// effectively zero.
	if err := waitForSendDelay(ctx, 300*time.Millisecond); err != nil {
		return false, false, err
	}
	capture, err = c.CapturePaneVisibleContext(ctx, target)
	if err != nil {
		return false, false, fmt.Errorf("re-capture claude pane before rescue: %w", err)
	}
	if agent.ClaudeActivelyWorking(capture, paneWidth) || !claudeComposerHoldsPayload(capture, message) {
		return true, false, nil
	}

	// Dismiss a possible picker, then finish the submission. A single Escape
	// is deliberate: double-Escape opens Claude Code's message-history jump.
	if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Escape"); err != nil {
		return false, true, fmt.Errorf("send rescue Escape to claude pane: %w", err)
	}
	if err := waitForSendDelay(ctx, 300*time.Millisecond); err != nil {
		return false, true, err
	}
	if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter"); err != nil {
		return false, true, fmt.Errorf("send rescue Enter to claude pane: %w", err)
	}
	for poll := 0; poll < codexVerifyMaxPolls; poll++ {
		if err := waitForSendDelay(ctx, codexVerifyPollInterval); err != nil {
			return false, true, err
		}
		capture, err = c.CapturePaneVisibleContext(ctx, target)
		if err != nil {
			return false, true, fmt.Errorf("capture claude pane after rescue: %w", err)
		}
		if agent.ClaudeActivelyWorking(capture, paneWidth) || !claudeComposerHoldsPayload(capture, message) {
			return true, true, nil
		}
	}
	return false, true, nil
}

// VerifyClaudeSubmissionContext verifies Claude prompt submission (default client).
func VerifyClaudeSubmissionContext(ctx context.Context, target, message string, paneWidth int) (bool, bool, error) {
	return DefaultClient.VerifyClaudeSubmissionContext(ctx, target, message, paneWidth)
}

// SendKeyName sends a tmux KEY NAME (BSpace, Escape, Enter, C-u, Up, ...)
// rather than literal text.
//
// SendKeys sends with `-l`, so passing a key name to it types the name as
// characters: SendKeys(target, "BSpace") put the six letters "BSpace" into the
// pane instead of erasing anything. That is not a cosmetic difference — in an
// agent composer it leaves text that the operator's next Enter submits.
// Callers that mean a keystroke must use this.
func (c *Client) SendKeyName(target, keyName string) error {
	return c.SendKeyNameContext(context.Background(), target, keyName)
}

// SendKeyNameContext sends a tmux key name with caller cancellation.
//
// The "--" stops a key name that begins with a dash from being parsed by tmux
// as a flag instead of a key.
func (c *Client) SendKeyNameContext(ctx context.Context, target, keyName string) error {
	return c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "--", keyName)
}

// SendKeyName sends a tmux key name using the default client.
func SendKeyName(target, keyName string) error {
	return DefaultClient.SendKeyName(target, keyName)
}

// SendInterrupt sends Ctrl+C to a pane
func (c *Client) SendInterrupt(target string) error {
	return c.RunSilent("send-keys", "-t", ExactTarget(target), "C-c")
}

// SendInterrupt sends Ctrl+C to a pane (default client)
func SendInterrupt(target string) error {
	return DefaultClient.SendInterrupt(target)
}

// SendEOF sends Ctrl+D (EOF) to a pane
func (c *Client) SendEOF(target string) error {
	return c.RunSilent("send-keys", "-t", ExactTarget(target), "C-d")
}

// DisplayMessage shows a message in the tmux status line.
// The "--" prevents msg from being interpreted as a tmux flag.
func (c *Client) DisplayMessage(session, msg string, durationMs int) error {
	return c.RunSilent("display-message", "-t", TargetSession(session), "-d", fmt.Sprintf("%d", durationMs), "--", msg)
}

// DisplayMessage shows a message in the tmux status line (default client)
func DisplayMessage(session, msg string, durationMs int) error {
	return DefaultClient.DisplayMessage(session, msg, durationMs)
}

// SanitizePaneCommand rejects control characters that could inject unintended
// key sequences (e.g., newlines, carriage returns, escapes) when sending
// commands into tmux panes.
func SanitizePaneCommand(cmd string) (string, error) {
	for _, r := range cmd {
		switch {
		case r == '\n', r == '\r', r == 0:
			return "", fmt.Errorf("command contains disallowed control characters")
		case r < 0x20 && r != '\t':
			return "", fmt.Errorf("command contains disallowed control character 0x%02x", r)
		}
	}
	return cmd, nil
}

// BuildPaneCommand constructs a safe cd+command string for execution inside a
// tmux pane, rejecting commands with unsafe control characters.
func BuildPaneCommand(projectDir, agentCommand string) (string, error) {
	safeCommand, err := SanitizePaneCommand(agentCommand)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("cd %s && %s", ShellQuote(projectDir), safeCommand), nil
}

// AttachOrSwitch attaches to a session or switches if already in tmux
func (c *Client) AttachOrSwitch(session string) error {
	if c.Remote == "" {
		if InTmux() {
			return c.RunSilent("switch-client", "-t", TargetSession(session))
		}
		// Interactive attach needs stdin/stdout, so use exec directly for local
		cmd := exec.Command(BinaryPath(), "attach", "-t", TargetSession(session))
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Remote attach
	// ssh -t user@host tmux attach -t session
	remoteCmd := buildRemoteShellCommand("tmux", "attach", "-t", TargetSession(session))
	// Use "--" to prevent Remote from being parsed as an ssh option.
	sshArgs := []string{"-t", "--", c.Remote, remoteCmd}
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// AttachOrSwitch attaches to a session or switches if already in tmux (default client)
func AttachOrSwitch(session string) error {
	return DefaultClient.AttachOrSwitch(session)
}

// KillSession kills a tmux session
func (c *Client) KillSession(session string) error {
	return c.RunSilent("kill-session", "-t", TargetSession(session))
}

// KillSession kills a tmux session (default client)
func KillSession(session string) error {
	return DefaultClient.KillSession(session)
}

// KillPane kills a tmux pane
func (c *Client) KillPane(paneID string) error {
	return c.KillPaneContext(context.Background(), paneID)
}

// KillPaneContext kills a tmux pane with caller cancellation.
func (c *Client) KillPaneContext(ctx context.Context, paneID string) error {
	if ctx == nil {
		return errors.New("tmux kill pane context is required")
	}
	return c.RunSilentContext(ctx, "kill-pane", "-t", ExactTarget(paneID))
}

// KillPane kills a tmux pane (default client)
func KillPane(paneID string) error {
	return DefaultClient.KillPane(paneID)
}

// KillPaneContext kills a tmux pane with caller cancellation (default client).
func KillPaneContext(ctx context.Context, paneID string) error {
	return DefaultClient.KillPaneContext(ctx, paneID)
}

// ApplyTiledLayout applies tiled layout to all windows
func (c *Client) ApplyTiledLayout(session string) error {
	return c.ApplyTiledLayoutContext(context.Background(), session)
}

// ApplyTiledLayoutContext applies tiled layout to all windows with
// cancellation support across discovery and each window mutation.
func (c *Client) ApplyTiledLayoutContext(ctx context.Context, session string) error {
	if ctx == nil {
		return errors.New("tmux layout context is required")
	}
	output, err := c.RunContext(ctx, "list-windows", "-t", TargetSession(session), "-F", "#{window_index}")
	if err != nil {
		return err
	}

	for _, winIdx := range strings.Split(output, "\n") {
		if winIdx == "" {
			continue
		}

		target := fmt.Sprintf("%s:%s", session, winIdx)

		// Unzoom if zoomed
		zoomed, displayErr := c.RunContext(ctx, "display-message", "-t", ExactTarget(target), "-p", "#{window_zoomed_flag}")
		if displayErr != nil {
			return fmt.Errorf("inspect window %s zoom state: %w", target, displayErr)
		}
		if zoomed == "1" {
			if err := c.RunSilentContext(ctx, "resize-pane", "-t", ExactTarget(target), "-Z"); err != nil {
				return fmt.Errorf("unzoom window %s: %w", target, err)
			}
		}

		// Apply tiled layout
		if err := c.RunSilentContext(ctx, "select-layout", "-t", ExactTarget(target), "tiled"); err != nil {
			return fmt.Errorf("apply tiled layout to window %s: %w", target, err)
		}
	}

	return nil
}

// ApplyTiledLayout applies tiled layout to all windows (default client)
func ApplyTiledLayout(session string) error {
	return DefaultClient.ApplyTiledLayout(session)
}

// ApplyTiledLayoutContext applies tiled layout with cancellation support using
// the default client.
func ApplyTiledLayoutContext(ctx context.Context, session string) error {
	return DefaultClient.ApplyTiledLayoutContext(ctx, session)
}

// ZoomPane zooms a specific pane
func (c *Client) ZoomPane(session string, paneIndex int) error {
	firstWin, err := c.GetFirstWindow(session)
	if err != nil {
		return err
	}

	target := fmt.Sprintf("%s:%d.%d", session, firstWin, paneIndex)

	if err := c.RunSilent("select-pane", "-t", ExactTarget(target)); err != nil {
		return err
	}

	return c.RunSilent("resize-pane", "-t", ExactTarget(target), "-Z")
}

// ZoomPane zooms a specific pane (default client)
func ZoomPane(session string, paneIndex int) error {
	return DefaultClient.ZoomPane(session, paneIndex)
}

// CapturePaneOutput captures the output of a pane with a default timeout to avoid hangs.
func (c *Client) CapturePaneOutput(target string, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultCommandTimeout)
	defer cancel()
	return c.CapturePaneOutputContext(ctx, target, lines)
}

// CapturePaneOutputContext captures the output of a pane with cancellation support.
func (c *Client) CapturePaneOutputContext(ctx context.Context, target string, lines int) (string, error) {
	if lines < 0 {
		lines = -lines
	}
	started := time.Now()
	output, err := c.RunContext(ctx, "capture-pane", "-t", ExactTarget(target), "-p", "-S", fmt.Sprintf("-%d", lines))
	c.recordCaptureBackpressure(target, lines, time.Since(started), err)
	return output, err
}

// CapturePaneOutput captures the output of a pane (default client)
func CapturePaneOutput(target string, lines int) (string, error) {
	return DefaultClient.CapturePaneOutput(target, lines)
}

// CapturePaneVisible captures ONLY the currently-visible screen of a pane (no
// scrollback history). This is the right capture for classifying transient TUI
// state — a live status bar / working footer is always on the visible screen,
// whereas a deep scrollback capture can resurrect stale footers (e.g. an MCP
// "esc to interrupt" startup line) that no longer reflect the pane's real state.
// It uses `capture-pane -S 0` so the start row is the top of the visible region.
func (c *Client) CapturePaneVisible(target string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultCommandTimeout)
	defer cancel()
	return c.CapturePaneVisibleContext(ctx, target)
}

// CapturePaneVisibleContext captures only the visible screen while honoring
// caller cancellation.
func (c *Client) CapturePaneVisibleContext(ctx context.Context, target string) (string, error) {
	started := time.Now()
	output, err := c.RunContext(ctx, "capture-pane", "-t", ExactTarget(target), "-p", "-S", "0")
	c.recordCaptureBackpressure(target, 0, time.Since(started), err)
	return output, err
}

// CapturePaneVisible captures only the visible screen of a pane (default client).
func CapturePaneVisible(target string) (string, error) {
	return DefaultClient.CapturePaneVisible(target)
}

// CapturePaneVisibleContext captures only the visible screen with caller cancellation.
func CapturePaneVisibleContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CapturePaneVisibleContext(ctx, target)
}

// CapturePaneOutputContext captures the output of a pane with cancellation support (default client).
func CapturePaneOutputContext(ctx context.Context, target string, lines int) (string, error) {
	return DefaultClient.CapturePaneOutputContext(ctx, target, lines)
}

// GetCurrentSession returns the current session name (if in tmux)
func (c *Client) GetCurrentSession() string {
	current, _ := c.GetCurrentSessionContext(context.Background())
	return current
}

// GetCurrentSessionContext returns the current session name with caller
// cancellation. Ordinary lookup failures are returned so context-aware
// callers can distinguish an absent tmux environment from a failed lookup.
func (c *Client) GetCurrentSessionContext(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("tmux current session context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.Remote == "" {
		if !InTmux() {
			return "", nil
		}
	} else {
		// A remote client is never "inside" the remote tmux server, so a
		// current-session lookup is not applicable; report no session.
		return "", nil
	}
	output, err := c.RunContext(ctx, "display-message", "-p", "#{session_name}")
	if err == nil {
		return output, nil
	}
	paneTarget := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneTarget == "" {
		return "", err
	}
	output, err = c.RunContext(ctx, "display-message", "-p", "-t", ExactTarget(paneTarget), "#{session_name}")
	if err != nil {
		return "", err
	}
	return output, nil
}

// GetCurrentSession returns the current session name (default client)
func GetCurrentSession() string {
	return DefaultClient.GetCurrentSession()
}

// GetCurrentSessionContext returns the current session name with caller
// cancellation (default client).
func GetCurrentSessionContext(ctx context.Context) (string, error) {
	return DefaultClient.GetCurrentSessionContext(ctx)
}

// ValidateSessionName checks if a session name is valid.
// It enforces a strict character set to prevent shell injection risks when used in templates.
// Allowed: Alphanumeric, underscore (_), dash (-).
func ValidateSessionName(name string) error {
	if name == "" {
		return errors.New("session name cannot be empty")
	}

	// Provide targeted errors for common confusion cases so callers can surface
	// clear remediation (tmux uses ':' as a target separator; '.' conflicts with
	// NTM's pane reference format like "0.1").
	if strings.Contains(name, ":") {
		return errors.New("session name cannot contain ':'")
	}
	if strings.Contains(name, ".") {
		return errors.New("session name cannot contain '.'")
	}

	// Check for invalid characters
	if !sessionNameRegex.MatchString(name) {
		return fmt.Errorf("session name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", name)
	}
	return nil
}

// GetPaneActivity returns the last activity time for the pane's window. tmux
// does not expose a pane-level activity timestamp; window_activity is the
// narrowest supported signal and is conservative when a window has many panes.
func (c *Client) GetPaneActivity(paneID string) (time.Time, error) {
	output, err := c.Run("display-message", "-p", "-t", ExactTarget(paneID), "#{window_activity}")
	if err != nil {
		return time.Time{}, err
	}

	activity, err := parsePaneActivityTimestamp(output, time.Now())
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse pane activity timestamp: %w", err)
	}
	return activity, nil
}

// GetPaneActivity returns the last activity time for a pane (default client)
func GetPaneActivity(paneID string) (time.Time, error) {
	return DefaultClient.GetPaneActivity(paneID)
}

// PaneActivity contains pane info with activity timestamp
type PaneActivity struct {
	Pane         Pane
	LastActivity time.Time
}

// GetPanesWithActivityContext returns all panes in a session with their activity times with cancellation support.
func (c *Client) GetPanesWithActivityContext(ctx context.Context, session string) ([]PaneActivity, error) {
	sep := FieldSeparator
	format := fmt.Sprintf("#{pane_id}%[1]s#{pane_index}%[1]s#{pane_title}%[1]s#{pane_current_command}%[1]s#{pane_width}%[1]s#{pane_height}%[1]s#{pane_active}%[1]s#{window_activity}%[1]s#{pane_pid}%[1]s#{window_index}%[1]s#{@ntm_agent_type}", sep)
	output, err := c.RunContext(ctx, "list-panes", "-s", "-t", TargetSession(session), "-F", format)
	if err != nil {
		return nil, err
	}

	var panes []PaneActivity
	for lineNumber, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		parts := strings.Split(line, sep)
		if len(parts) != 11 {
			return nil, fmt.Errorf("parse pane activity %d from session %q: expected 11 fields, got %d", lineNumber+1, session, len(parts))
		}

		// Format: id(0), index(1), title(2), command(3), width(4), height(5), active(6), last_activity(7), pid(8), window_index(9), @ntm_agent_type(10)
		// parts[:7] = id..active
		// parts[8:] = pid, window_index, @ntm_agent_type
		p, err := parsePaneFromParts(parts[:7], parts[8:])
		if err != nil {
			return nil, fmt.Errorf("parse pane activity %d from session %q: %w", lineNumber+1, session, err)
		}

		rawTimestamp := strings.TrimSpace(parts[7])
		now := time.Now()
		lastActivity, err := parsePaneActivityTimestamp(rawTimestamp, now)
		if err != nil {
			// Unparseable timestamps should not produce huge idle durations.
			lastActivity = now
		}

		panes = append(panes, PaneActivity{
			Pane:         *p,
			LastActivity: lastActivity,
		})
	}

	return panes, nil
}

// parsePaneLine parses a single line from list-panes format into a Pane.
func parsePaneLine(line, sep string) (*Pane, error) {
	parts := strings.Split(line, sep)
	if len(parts) != 10 {
		return nil, fmt.Errorf("expected 10 fields, got %d", len(parts))
	}
	// For standard GetPanes: id, index, title, command, width, height, active, pid, window_index, @ntm_agent_type
	return parsePaneFromParts(parts[:7], parts[7:])
}

// parsePaneFromParts constructs a Pane from pre-split parts.
// parts1: id, index, title, command, width, height, active
// parts2: pid, window_index, and optionally the pane's @ntm_agent_type user
// option (empty when unset or when the tmux server predates pane options).
func parsePaneFromParts(parts1, parts2 []string) (*Pane, error) {
	if len(parts1) != 7 || (len(parts2) != 2 && len(parts2) != 3) {
		return nil, fmt.Errorf("expected 7 primary fields and 2 or 3 secondary fields, got %d and %d", len(parts1), len(parts2))
	}
	recordedType := ""
	if len(parts2) == 3 {
		recordedType = parts2[2]
	}

	index, err := strconv.Atoi(parts1[1])
	if err != nil {
		return nil, fmt.Errorf("parse pane index %q: %w", parts1[1], err)
	}
	width, err := strconv.Atoi(parts1[4])
	if err != nil {
		return nil, fmt.Errorf("parse pane width %q: %w", parts1[4], err)
	}
	height, err := strconv.Atoi(parts1[5])
	if err != nil {
		return nil, fmt.Errorf("parse pane height %q: %w", parts1[5], err)
	}
	active := parts1[6] == "1"
	pid, err := strconv.Atoi(parts2[0])
	if err != nil {
		return nil, fmt.Errorf("parse pane PID %q: %w", parts2[0], err)
	}
	windowIndex, err := strconv.Atoi(parts2[1])
	if err != nil {
		return nil, fmt.Errorf("parse window index %q: %w", parts2[1], err)
	}

	pane := &Pane{
		ID:          parts1[0],
		Index:       index,
		WindowIndex: windowIndex,
		Title:       parts1[2],
		Command:     parts1[3],
		Width:       width,
		Height:      height,
		Active:      active,
		PID:         pid,
	}

	// Parse pane title using regex to extract type, index, variant, and tags
	pane.Type, pane.NTMIndex, pane.Variant, pane.Tags = parseAgentFromTitle(pane.Title)

	// A type recorded on the pane by ntm itself (adopt sets the
	// @ntm_agent_type pane option) is authoritative: unlike the title it
	// cannot be overwritten by the program running in the pane. OpenCode, for
	// example, re-asserts its own "opencode | <dir>" title continuously, so an
	// adopted pane's NTM title survives only seconds (ntm#266). The title is
	// still consulted above for the NTM index, variant and tags.
	if recorded := ParsePaneAgentTypeOption(recordedType); recorded != AgentUnknown {
		pane.Type = recorded
		return pane, nil
	}

	// Fallback chain:
	//  1. Title-based parse (NTM-formatted titles).
	//  2. Immediate command name (`claude`, `codex`, etc.).
	//  3. A title the agent set for itself (e.g. OpenCode's "opencode | dir").
	//  4. Process tree walk — required when the agent runs under a
	//     wrapper that shows up in tmux's `pane_current_command` (e.g.
	//     Bun-launched Codex shows `bun`, not `codex`). See acfs#267.
	if pane.Type == AgentUser && pane.Command != "" {
		if detected := detectAgentFromCommand(pane.Command); detected != AgentUser {
			pane.Type = detected
		} else if detected := detectAgentFromSelfTitle(pane.Title); detected != AgentUser {
			pane.Type = detected
		} else if isAgentWrapperCommand(pane.Command) && pane.PID > 0 {
			if detected := detectAgentFromProcessTree(pane.PID, 4); detected != AgentUser {
				pane.Type = detected
			}
		}
	}

	return pane, nil
}

// PaneAgentTypeOption is the tmux pane user option in which ntm records the
// agent type it assigned to a pane. Programs running in the pane can rewrite
// the pane title (OSC 0/2 escapes, or tmux commands of their own) but never
// this option, so it survives agents that manage their own titles (ntm#266).
// Pane-scoped options need tmux 3.0+; on older servers the option is simply
// absent and the title/command heuristics apply.
const PaneAgentTypeOption = "@ntm_agent_type"

// ParsePaneAgentTypeOption converts a raw @ntm_agent_type value into an agent
// type. It returns AgentUnknown when the option is unset or names a type ntm
// does not know (a built-in type or a registered plugin), so a stale or
// hand-edited value can never masquerade as a real agent.
func ParsePaneAgentTypeOption(raw string) AgentType {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AgentUnknown
	}
	canonical := AgentType(raw).Canonical()
	if canonical.IsValid() {
		return canonical
	}
	if agent.IsPluginType(canonical) {
		return canonical
	}
	return AgentUnknown
}

// SetPaneAgentType records agentType on the pane as the @ntm_agent_type user
// option (see PaneAgentTypeOption). Unlike SetPaneTitle it never touches the
// title, so it is safe to call for panes whose program owns its own title.
func (c *Client) SetPaneAgentType(paneID string, agentType AgentType) error {
	return c.SetPaneAgentTypeContext(context.Background(), paneID, agentType)
}

// SetPaneAgentTypeContext is SetPaneAgentType with caller cancellation.
func (c *Client) SetPaneAgentTypeContext(ctx context.Context, paneID string, agentType AgentType) error {
	if ctx == nil {
		return errors.New("tmux pane agent type context is required")
	}
	if strings.TrimSpace(paneID) == "" {
		return errors.New("pane ID is required")
	}
	recorded := ParsePaneAgentTypeOption(string(agentType))
	if recorded == AgentUnknown {
		return fmt.Errorf("agent type %q is not a known agent type", agentType)
	}
	return c.RunSilentContext(ctx, "set-option", "-p", "-t", ExactTarget(paneID), PaneAgentTypeOption, string(recorded))
}

// SetPaneAgentType records the agent type on a pane (default client).
func SetPaneAgentType(paneID string, agentType AgentType) error {
	return DefaultClient.SetPaneAgentType(paneID, agentType)
}

// SetPaneAgentTypeContext records the agent type on a pane with caller
// cancellation (default client).
func SetPaneAgentTypeContext(ctx context.Context, paneID string, agentType AgentType) error {
	return DefaultClient.SetPaneAgentTypeContext(ctx, paneID, agentType)
}

// detectAgentFromSelfTitle recognises pane titles that an agent CLI sets for
// itself, as opposed to the NTM "session__type_n" convention. Only shapes
// that are unambiguous are listed: OpenCode renders "opencode | <workdir>"
// (ntm#266). Free-form titles such as Claude Code's summary line are not
// matched here; the command and process-tree checks cover those.
func detectAgentFromSelfTitle(title string) AgentType {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return AgentUser
	}
	head := trimmed
	if i := strings.Index(trimmed, "|"); i >= 0 {
		head = trimmed[:i]
	}
	if strings.EqualFold(strings.TrimSpace(head), "opencode") {
		return AgentOpencode
	}
	return AgentUser
}

func parsePaneActivityTimestamp(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return now, nil
	}

	timestamp, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	if timestamp <= 0 {
		// Some tmux versions return 0 for fresh panes; treat as current time.
		return now, nil
	}
	return time.Unix(timestamp, 0), nil
}

// GetPanesWithActivity returns all panes in a session with their activity times
func (c *Client) GetPanesWithActivity(session string) ([]PaneActivity, error) {
	return c.GetPanesWithActivityContext(context.Background(), session)
}

// GetPanesWithActivity returns all panes in a session with their activity times (default client)
func GetPanesWithActivity(session string) ([]PaneActivity, error) {
	return DefaultClient.GetPanesWithActivity(session)
}

// GetPanesWithActivityContext returns all panes in a session with their activity times with cancellation support (default client).
func GetPanesWithActivityContext(ctx context.Context, session string) ([]PaneActivity, error) {
	return DefaultClient.GetPanesWithActivityContext(ctx, session)
}
