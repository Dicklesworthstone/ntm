package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

type adoptTypeSpec struct {
	Flag        string
	AgentType   agentpkg.AgentType
	Description string
	Example     string
}

// paneSpec identifies a pane requested for adoption. Pane addressing in a tmux
// session is fundamentally two-dimensional: a pane lives at (window, index).
// `GetPanes` lists panes session-wide (`list-panes -s`), so in a
// window-per-agent layout (N windows, each with a single pane at index 0) every
// pane shares the same window-local Index. A bare index therefore no longer
// uniquely identifies a pane.
//
// A paneSpec carries the window component when the user supplied `window.pane`
// syntax (e.g. `2.1`). When Window is paneWindowUnspecified the caller asked
// for a bare index and we must resolve it against the session — failing loud if
// that index is ambiguous across windows rather than silently adopting one pane
// and dropping the rest (#170).
type paneSpec struct {
	Window int // window index, or paneWindowUnspecified for bare-index requests
	Pane   int // window-local pane index
}

// paneWindowUnspecified marks a paneSpec parsed from a bare index (no
// `window.` prefix), distinguishing it from an explicit window 0.
const paneWindowUnspecified = -1

// HasWindow reports whether the spec was given with explicit window.pane syntax.
func (s paneSpec) HasWindow() bool { return s.Window != paneWindowUnspecified }

// String renders the spec the way a user would type it, for diagnostics.
func (s paneSpec) String() string {
	if s.HasWindow() {
		return fmt.Sprintf("%d.%d", s.Window, s.Pane)
	}
	return strconv.Itoa(s.Pane)
}

var supportedAdoptTypes = []adoptTypeSpec{
	{Flag: "cc", AgentType: agentpkg.AgentTypeClaudeCode, Description: "Claude agents", Example: "0,1,2"},
	{Flag: "cod", AgentType: agentpkg.AgentTypeCodex, Description: "Codex agents", Example: "3,4"},
	{Flag: "gmi", AgentType: agentpkg.AgentTypeGemini, Description: "Gemini agents", Example: "5"},
	{Flag: "cursor", AgentType: agentpkg.AgentTypeCursor, Description: "Cursor agents", Example: "6"},
	{Flag: "windsurf", AgentType: agentpkg.AgentTypeWindsurf, Description: "Windsurf agents", Example: "7"},
	{Flag: "aider", AgentType: agentpkg.AgentTypeAider, Description: "Aider agents", Example: "8"},
	{Flag: "oc", AgentType: agentpkg.AgentTypeOpencode, Description: "Opencode agents", Example: "9"},
	{Flag: "ollama", AgentType: agentpkg.AgentTypeOllama, Description: "Ollama agents", Example: "10"},
	{Flag: "user", AgentType: agentpkg.AgentTypeUser, Description: "User panes", Example: "10"},
}

func newAdoptCmd() *cobra.Command {
	var (
		autoName bool
		dryRun   bool
		byWindow bool
	)
	paneFlags := make(map[agentpkg.AgentType]*string, len(supportedAdoptTypes))
	for _, spec := range supportedAdoptTypes {
		paneFlags[spec.AgentType] = new(string)
	}

	cmd := &cobra.Command{
		Use:   "adopt <session-name>",
		Short: "Adopt an external tmux session for NTM management",
		Long: `Adopt an existing tmux session that was created outside of NTM.

This command takes an externally-created tmux session and configures it
for use with NTM by setting appropriate pane titles and registering
agent types. After adoption, all NTM commands (send, status, list, etc.)
will work with the session.

Panes are specified by their pane index (0-based from tmux). Use commas to
specify multiple panes per agent type. Ranges like "0-5" are supported.

For window-per-agent layouts (one pane per window, all sharing pane index 0),
address panes with "window.pane" syntax (e.g. "2.0") so each pane is uniquely
identified. A bare index that is ambiguous across windows is rejected with a
clear error rather than silently adopting one pane and dropping the rest.

Examples:
  # Adopt session with panes 0-5 as Claude agents
  ntm adopt my_session --cc=0,1,2,3,4,5

  # Adopt with mixed agent types
  ntm adopt my_session --cc=0,1,2 --cod=3,4 --gmi=5 --cursor=6

  # Window-per-agent layout: address each window's pane explicitly
  ntm adopt my_session --cc=1.0,2.0 --cod=3.0

  # Map each window's sole pane to a single agent type
  ntm adopt my_session --by-window --cc

  # Preview what would be changed
  ntm adopt my_session --windsurf=0 --aider=1 --ollama=2 --dry-run

  # Auto-rename panes based on agent type
  ntm adopt my_session --cc=0,1 --auto-name`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := args[0]

			if byWindow {
				// --by-window maps every window's sole pane to one agent type.
				// The agent type is identified by the single agent flag the
				// user passed (e.g. `--by-window --cc`); the flag's value is
				// ignored. Exactly one agent flag must be set.
				var selected agentpkg.AgentType
				selectedCount := 0
				for _, spec := range supportedAdoptTypes {
					if cmd.Flags().Changed(spec.Flag) {
						selected = spec.AgentType
						selectedCount++
					}
				}
				if selectedCount != 1 {
					return fmt.Errorf("--by-window requires exactly one agent type flag (e.g. --by-window --cc), got %d", selectedCount)
				}
				opts := AdoptOptions{
					Session:      sessionName,
					AutoName:     autoName,
					DryRun:       dryRun,
					ByWindow:     true,
					ByWindowType: selected,
				}
				return runAdopt(opts)
			}

			assignments := make(map[agentpkg.AgentType][]paneSpec, len(supportedAdoptTypes))
			for _, spec := range supportedAdoptTypes {
				specs, err := parsePaneSpecs(*paneFlags[spec.AgentType])
				if err != nil {
					return fmt.Errorf("--%s: %w", spec.Flag, err)
				}
				assignments[spec.AgentType] = specs
			}

			opts := AdoptOptions{
				Session:         sessionName,
				AutoName:        autoName,
				DryRun:          dryRun,
				PaneAssignments: assignments,
			}

			return runAdopt(opts)
		},
	}

	for _, spec := range supportedAdoptTypes {
		cmd.Flags().StringVar(
			paneFlags[spec.AgentType],
			spec.Flag,
			"",
			fmt.Sprintf("Comma-separated pane indices for %s (e.g., %s)", spec.Description, spec.Example),
		)
	}
	cmd.Flags().BoolVar(&autoName, "auto-name", true, "Automatically rename panes to NTM convention")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without applying them")
	cmd.Flags().BoolVar(&byWindow, "by-window", false, "Map each window's sole pane to the single agent type flag provided (e.g. --by-window --cc)")

	return cmd
}

// AdoptOptions configures session adoption
type AdoptOptions struct {
	Session  string
	AutoName bool
	DryRun   bool

	// PaneAssignments maps each agent type to the panes requested for it.
	// Each paneSpec is either a bare index (resolved against the session,
	// failing loud if ambiguous across windows) or an explicit window.pane.
	PaneAssignments map[agentpkg.AgentType][]paneSpec

	// ByWindow, when true, ignores PaneAssignments and instead maps every
	// window's sole pane to ByWindowType. This is the convenience path for
	// window-per-agent layouts.
	ByWindow     bool
	ByWindowType agentpkg.AgentType
}

// AdoptResult represents the result of an adopt operation
type AdoptResult struct {
	Success      bool               `json:"success"`
	Session      string             `json:"session"`
	AdoptedPanes []AdoptedPaneInfo  `json:"adopted_panes"`
	TotalPanes   int                `json:"total_panes"`
	Agents       AdoptedAgentCounts `json:"agents"`
	DryRun       bool               `json:"dry_run"`
	Error        string             `json:"error,omitempty"`
}

// AdoptedPaneInfo describes a pane that was adopted
type AdoptedPaneInfo struct {
	PaneID      string `json:"pane_id"`
	PaneIndex   int    `json:"pane_index"`
	WindowIndex int    `json:"window_index"`
	AgentType   string `json:"agent_type"`
	OldTitle    string `json:"old_title,omitempty"`
	NewTitle    string `json:"new_title"`
	NTMIndex    int    `json:"ntm_index"`
}

// AdoptedAgentCounts tracks agent counts by type
type AdoptedAgentCounts struct {
	CC       int `json:"cc"`
	Cod      int `json:"cod"`
	Gmi      int `json:"gmi"`
	Cursor   int `json:"cursor"`
	Windsurf int `json:"windsurf"`
	Aider    int `json:"aider"`
	Opencode int `json:"oc"`
	Ollama   int `json:"ollama"`
	User     int `json:"user"`
}

func (a AdoptedAgentCounts) Total() int {
	return a.CC + a.Cod + a.Gmi + a.Cursor + a.Windsurf + a.Aider + a.Opencode + a.Ollama + a.User
}

func (a AdoptedAgentCounts) Summary() string {
	parts := make([]string, 0, len(supportedAdoptTypes))
	for _, spec := range supportedAdoptTypes {
		count := a.countFor(spec.AgentType)
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", spec.AgentType, count))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func (a *AdoptedAgentCounts) increment(agentType agentpkg.AgentType) {
	switch agentType.Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		a.CC++
	case agentpkg.AgentTypeCodex:
		a.Cod++
	case agentpkg.AgentTypeGemini:
		a.Gmi++
	case agentpkg.AgentTypeCursor:
		a.Cursor++
	case agentpkg.AgentTypeWindsurf:
		a.Windsurf++
	case agentpkg.AgentTypeAider:
		a.Aider++
	case agentpkg.AgentTypeOpencode:
		a.Opencode++
	case agentpkg.AgentTypeOllama:
		a.Ollama++
	case agentpkg.AgentTypeUser:
		a.User++
	}
}

func (a AdoptedAgentCounts) countFor(agentType agentpkg.AgentType) int {
	switch agentType.Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		return a.CC
	case agentpkg.AgentTypeCodex:
		return a.Cod
	case agentpkg.AgentTypeGemini:
		return a.Gmi
	case agentpkg.AgentTypeCursor:
		return a.Cursor
	case agentpkg.AgentTypeWindsurf:
		return a.Windsurf
	case agentpkg.AgentTypeAider:
		return a.Aider
	case agentpkg.AgentTypeOpencode:
		return a.Opencode
	case agentpkg.AgentTypeOllama:
		return a.Ollama
	case agentpkg.AgentTypeUser:
		return a.User
	default:
		return 0
	}
}

func (r *AdoptResult) Text(w io.Writer) error {
	t := theme.Current()

	if !r.Success {
		fmt.Fprintf(w, "%s✗%s Failed to adopt session: %s\n",
			colorize(t.Red), colorize(t.Text), r.Error)
		return nil
	}

	if r.DryRun {
		fmt.Fprintf(w, "%s[DRY RUN]%s Would adopt session '%s'\n",
			colorize(t.Warning), colorize(t.Text), r.Session)
	} else {
		fmt.Fprintf(w, "%s✓%s Adopted session '%s'\n",
			colorize(t.Success), colorize(t.Text), r.Session)
	}

	fmt.Fprintf(w, "  Total panes: %d\n", r.TotalPanes)
	fmt.Fprintf(w, "  Adopted: %d (%s)\n", r.Agents.Total(), r.Agents.Summary())

	if len(r.AdoptedPanes) > 0 {
		fmt.Fprintf(w, "\n%sPanes:%s\n", colorize(t.Blue), colorize(t.Text))
		for _, p := range r.AdoptedPanes {
			fmt.Fprintf(w, "  [%d] %s → %s\n", p.PaneIndex, p.OldTitle, p.NewTitle)
		}
	}

	if r.DryRun {
		fmt.Fprintf(w, "\n%sNote:%s Use without --dry-run to apply changes.\n",
			colorize(t.Warning), colorize(t.Text))
	}

	return nil
}

func (r *AdoptResult) JSON() interface{} {
	return r
}

func runAdopt(opts AdoptOptions) error {
	// bd-usgfy: emitAdoptFailure writes the success:false JSON envelope and
	// signals non-zero exit via errJSONFailure so automation scripts gating
	// on `$?` don't treat the failure as success (parity with #125).
	// bd-ixy2t: hoisted above the tmux.EnsureInstalled() check so the
	// early-fail path also emits a parseable envelope when --json is set —
	// previously a missing tmux binary surfaced as a raw stderr error and
	// `ntm adopt --json | jq ...` parsed empty stdin.
	emitAdoptFailure := func(result *AdoptResult) error {
		if encErr := output.New(output.WithJSON(jsonOutput)).Output(result); encErr != nil {
			return encErr
		}
		return jsonFailureExit()
	}

	if err := tmux.EnsureInstalled(); err != nil {
		if jsonOutput {
			return emitAdoptFailure(&AdoptResult{
				Success: false,
				Session: opts.Session,
				Error:   err.Error(),
			})
		}
		return err
	}

	if !tmux.SessionExists(opts.Session) {
		return emitAdoptFailure(&AdoptResult{
			Success: false,
			Session: opts.Session,
			Error:   fmt.Sprintf("session '%s' not found", opts.Session),
		})
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return emitAdoptFailure(&AdoptResult{
			Success: false,
			Session: opts.Session,
			Error:   fmt.Sprintf("failed to get panes: %v", err),
		})
	}

	// --by-window expands to one paneSpec per window, each addressing that
	// window's sole pane. Reject the layout when any window holds more than one
	// pane: "the sole pane" is undefined there and silently picking one would
	// reintroduce the drop-panes bug we are fixing.
	if opts.ByWindow {
		specs, err := byWindowAssignments(panes)
		if err != nil {
			return emitAdoptFailure(&AdoptResult{Success: false, Session: opts.Session, Error: err.Error()})
		}
		opts.PaneAssignments = map[agentpkg.AgentType][]paneSpec{
			opts.ByWindowType.Canonical(): specs,
		}
	}

	// Build a composite-keyed index. tmux pane IDs (%N) are globally unique and
	// base-index-independent, so they are the canonical identity. We also build
	// an index keyed by (WindowIndex, Index) for explicit window.pane requests,
	// and a bare-index multimap so we can detect ambiguity (the #170
	// correctness bug: a non-unique bare index used to silently collapse N
	// panes to one map entry, last-write-wins).
	paneByWinPane := make(map[paneSpec]*tmux.Pane, len(panes))
	panesByBareIndex := make(map[int][]*tmux.Pane)
	for i := range panes {
		p := &panes[i]
		paneByWinPane[paneSpec{Window: p.WindowIndex, Pane: p.Index}] = p
		panesByBareIndex[p.Index] = append(panesByBareIndex[p.Index], p)
	}

	if err := validateAdoptAssignments(opts.PaneAssignments); err != nil {
		return emitAdoptFailure(&AdoptResult{Success: false, Session: opts.Session, Error: err.Error()})
	}

	adoptedPanes := []AdoptedPaneInfo{}
	counts := AdoptedAgentCounts{}
	ntmIndex := make(map[agentpkg.AgentType]int, len(supportedAdoptTypes))
	for _, spec := range supportedAdoptTypes {
		ntmIndex[spec.AgentType] = 1
	}

	adoptType := func(reqs []paneSpec, agentType agentpkg.AgentType) error {
		canonicalType := agentType.Canonical()
		for _, req := range reqs {
			pane, err := resolveAdoptPane(req, paneByWinPane, panesByBareIndex)
			if err != nil {
				return err
			}

			newTitle := tmux.FormatPaneName(opts.Session, canonicalType.String(), ntmIndex[canonicalType], "")
			info := AdoptedPaneInfo{
				PaneID:      pane.ID,
				PaneIndex:   pane.Index,
				WindowIndex: pane.WindowIndex,
				AgentType:   canonicalType.String(),
				OldTitle:    pane.Title,
				NewTitle:    newTitle,
				NTMIndex:    ntmIndex[canonicalType],
			}

			if opts.AutoName && !opts.DryRun {
				if err := tmux.SetPaneTitle(pane.ID, newTitle); err != nil {
					return fmt.Errorf("failed to set title for pane %s: %v", req, err)
				}
			}

			adoptedPanes = append(adoptedPanes, info)
			ntmIndex[canonicalType]++
			counts.increment(canonicalType)
		}
		return nil
	}

	for _, spec := range supportedAdoptTypes {
		if err := adoptType(opts.PaneAssignments[spec.AgentType], spec.AgentType); err != nil {
			result := &AdoptResult{Success: false, Session: opts.Session, Error: err.Error()}
			return emitAdoptFailure(result)
		}
	}

	result := &AdoptResult{
		Success:      true,
		Session:      opts.Session,
		AdoptedPanes: adoptedPanes,
		TotalPanes:   len(panes),
		Agents:       counts,
		DryRun:       opts.DryRun,
	}

	return output.New(output.WithJSON(jsonOutput)).Output(result)
}

// resolveAdoptPane maps a requested paneSpec to a concrete pane, failing loud
// (never silently dropping panes) when a bare index is ambiguous across
// windows. This is the heart of the #170 correctness fix: the previous code
// keyed a map solely on pane Index, so a window-per-agent layout collapsed N
// panes to one entry (last-write-wins) and either errored confusingly or, worse,
// adopted a single pane while silently dropping the rest.
//
//   - explicit window.pane → looked up by composite (window, index) identity.
//   - bare index, unique   → the single matching pane.
//   - bare index, ambiguous → error directing the user to window.pane addressing.
func resolveAdoptPane(req paneSpec, byWinPane map[paneSpec]*tmux.Pane, byBareIndex map[int][]*tmux.Pane) (*tmux.Pane, error) {
	if req.HasWindow() {
		pane, ok := byWinPane[req]
		if !ok {
			return nil, fmt.Errorf("pane %s not found in session (no pane at window %d index %d)", req, req.Window, req.Pane)
		}
		return pane, nil
	}
	matches := byBareIndex[req.Pane]
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("pane index %d not found in session", req.Pane)
	case 1:
		return matches[0], nil
	default:
		// Ambiguous: the same window-local index exists in multiple windows
		// (window-per-agent layout). Refuse rather than adopt one and drop the
		// rest. Surface the available window.pane addresses as remediation.
		return nil, fmt.Errorf("pane index %d is ambiguous: it exists in %d windows (%s); re-run with explicit window.pane addressing (e.g. --cc=%s)",
			req.Pane, len(matches), winPaneList(matches), winPaneList(matches))
	}
}

func validateAdoptAssignments(assignments map[agentpkg.AgentType][]paneSpec) error {
	// Track by spec identity (window.pane string) so the same explicit address
	// can't be claimed twice, and a bare index can't be reused. Bare-vs-window
	// collisions referencing the same physical pane are caught later at
	// resolution time (once the session layout is known); here we only need to
	// reject obvious duplicate specs.
	seen := make(map[string]agentpkg.AgentType)
	total := 0
	for _, spec := range supportedAdoptTypes {
		for _, req := range assignments[spec.AgentType] {
			total++
			if req.Pane < 0 {
				return fmt.Errorf("pane index %d is invalid; pane indices must be non-negative", req.Pane)
			}
			if req.HasWindow() && req.Window < 0 {
				return fmt.Errorf("window index %d is invalid; window indices must be non-negative", req.Window)
			}
			key := req.String()
			if previous, ok := seen[key]; ok {
				return fmt.Errorf("pane %s assigned multiple times (%s and %s)", key, previous, spec.AgentType)
			}
			seen[key] = spec.AgentType
		}
	}
	if total == 0 {
		return fmt.Errorf("no panes specified for adoption; use one or more of %s", adoptFlagList())
	}
	return nil
}

func adoptFlagList() string {
	parts := make([]string, 0, len(supportedAdoptTypes))
	for _, spec := range supportedAdoptTypes {
		parts = append(parts, "--"+spec.Flag)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", or " + parts[len(parts)-1]
}

// parsePaneList parses a comma-separated list of pane indices
func parsePaneList(s string) []int {
	if s == "" {
		return nil
	}

	var result []int
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		// Support ranges like "0-5"
		if strings.Contains(p, "-") {
			rangeParts := strings.Split(p, "-")
			if len(rangeParts) == 2 {
				start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err1 == nil && err2 == nil && start <= end {
					for i := start; i <= end; i++ {
						result = append(result, i)
					}
					continue
				}
			}
		}

		if idx, err := strconv.Atoi(p); err == nil {
			result = append(result, idx)
		}
	}

	return result
}

// parsePaneSpecs parses a comma-separated pane specification into paneSpecs.
//
// Accepted token forms:
//   - bare index:   "0", "3"          → paneSpec{Window: unspecified, Pane: n}
//   - range:        "0-5"             → bare specs for indices 0..5
//   - window.pane:  "2.1", "3.0"      → paneSpec{Window: w, Pane: p}
//
// Bare indices and ranges keep full backward compatibility with the original
// `--cc=1,2` usage. The window.pane form lets callers disambiguate panes in a
// window-per-agent layout where every window's pane shares index 0.
//
// Unlike the legacy parsePaneList (which silently skipped malformed tokens),
// this parser returns an error so a typo like "--cc=2.x" is surfaced loudly
// rather than dropping the pane.
func parsePaneSpecs(s string) ([]paneSpec, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}

	var result []paneSpec
	for _, raw := range strings.Split(s, ",") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}

		// window.pane form (exactly one dot separating two non-negative ints).
		if strings.Contains(tok, ".") {
			dotParts := strings.Split(tok, ".")
			if len(dotParts) != 2 {
				return nil, fmt.Errorf("invalid pane spec %q: expected window.pane (e.g. 2.1)", tok)
			}
			win, err1 := strconv.Atoi(strings.TrimSpace(dotParts[0]))
			pane, err2 := strconv.Atoi(strings.TrimSpace(dotParts[1]))
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid pane spec %q: window and pane must be integers", tok)
			}
			if win < 0 || pane < 0 {
				return nil, fmt.Errorf("invalid pane spec %q: window and pane must be non-negative", tok)
			}
			result = append(result, paneSpec{Window: win, Pane: pane})
			continue
		}

		// Range form like "0-5" (bare indices only). A leading '-' is not a
		// range separator but a negative sign, which falls through to the bare
		// branch below where it is rejected as non-negative.
		if i := strings.Index(tok, "-"); i > 0 {
			rangeParts := strings.Split(tok, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range %q: expected start-end", tok)
			}
			start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid range %q: bounds must be integers", tok)
			}
			if start < 0 || end < 0 {
				return nil, fmt.Errorf("invalid range %q: bounds must be non-negative", tok)
			}
			if start > end {
				return nil, fmt.Errorf("invalid range %q: start must be <= end", tok)
			}
			for i := start; i <= end; i++ {
				result = append(result, paneSpec{Window: paneWindowUnspecified, Pane: i})
			}
			continue
		}

		// Bare index.
		idx, err := strconv.Atoi(tok)
		if err != nil {
			return nil, fmt.Errorf("invalid pane index %q", tok)
		}
		if idx < 0 {
			return nil, fmt.Errorf("pane index %d must be non-negative", idx)
		}
		result = append(result, paneSpec{Window: paneWindowUnspecified, Pane: idx})
	}

	return result, nil
}

// byWindowAssignments builds one paneSpec per window, each addressing that
// window's sole pane. It fails loud when any window contains more than one
// pane, because "the window's sole pane" is undefined in that case and silently
// picking one would reintroduce the drop-panes bug (#170).
func byWindowAssignments(panes []tmux.Pane) ([]paneSpec, error) {
	panesPerWindow := make(map[int][]tmux.Pane)
	var windowOrder []int
	for _, p := range panes {
		if _, seen := panesPerWindow[p.WindowIndex]; !seen {
			windowOrder = append(windowOrder, p.WindowIndex)
		}
		panesPerWindow[p.WindowIndex] = append(panesPerWindow[p.WindowIndex], p)
	}
	if len(windowOrder) == 0 {
		return nil, fmt.Errorf("session has no panes to adopt")
	}

	specs := make([]paneSpec, 0, len(windowOrder))
	for _, win := range windowOrder {
		wp := panesPerWindow[win]
		if len(wp) != 1 {
			return nil, fmt.Errorf("--by-window requires exactly one pane per window, but window %d has %d panes; use explicit window.pane addressing instead", win, len(wp))
		}
		specs = append(specs, paneSpec{Window: win, Pane: wp[0].Index})
	}
	return specs, nil
}

// winPaneList renders a set of panes as a comma-separated list of window.pane
// addresses, for ambiguity-error remediation hints.
func winPaneList(panes []*tmux.Pane) string {
	parts := make([]string, 0, len(panes))
	for _, p := range panes {
		parts = append(parts, fmt.Sprintf("%d.%d", p.WindowIndex, p.Index))
	}
	return strings.Join(parts, ",")
}
