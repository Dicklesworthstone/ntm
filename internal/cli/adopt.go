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

Panes are specified by their pane index (0-based from tmux).
Use commas to specify multiple panes per agent type.

Examples:
  # Adopt session with panes 0-5 as Claude agents
  ntm adopt my_session --cc=0,1,2,3,4,5

  # Adopt with mixed agent types
  ntm adopt my_session --cc=0,1,2 --cod=3,4 --gmi=5 --cursor=6

  # Preview what would be changed
  ntm adopt my_session --windsurf=0 --aider=1 --ollama=2 --dry-run

  # Auto-rename panes based on agent type
  ntm adopt my_session --cc=0,1 --auto-name`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sessionName := args[0]
			assignments := make(map[agentpkg.AgentType][]int, len(supportedAdoptTypes))
			for _, spec := range supportedAdoptTypes {
				assignments[spec.AgentType] = parsePaneList(*paneFlags[spec.AgentType])
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

	return cmd
}

// AdoptOptions configures session adoption
type AdoptOptions struct {
	Session         string
	AutoName        bool
	DryRun          bool
	PaneAssignments map[agentpkg.AgentType][]int
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
	PaneID    string `json:"pane_id"`
	PaneIndex int    `json:"pane_index"`
	AgentType string `json:"agent_type"`
	OldTitle  string `json:"old_title,omitempty"`
	NewTitle  string `json:"new_title"`
	NTMIndex  int    `json:"ntm_index"`
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
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if !tmux.SessionExists(opts.Session) {
		result := &AdoptResult{
			Success: false,
			Session: opts.Session,
			Error:   fmt.Sprintf("session '%s' not found", opts.Session),
		}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		result := &AdoptResult{
			Success: false,
			Session: opts.Session,
			Error:   fmt.Sprintf("failed to get panes: %v", err),
		}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	paneByIndex := make(map[int]*tmux.Pane)
	for i := range panes {
		paneByIndex[panes[i].Index] = &panes[i]
	}

	if err := validateAdoptAssignments(opts.PaneAssignments); err != nil {
		result := &AdoptResult{Success: false, Session: opts.Session, Error: err.Error()}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	adoptedPanes := []AdoptedPaneInfo{}
	counts := AdoptedAgentCounts{}
	ntmIndex := make(map[agentpkg.AgentType]int, len(supportedAdoptTypes))
	for _, spec := range supportedAdoptTypes {
		ntmIndex[spec.AgentType] = 1
	}

	adoptType := func(paneIndices []int, agentType agentpkg.AgentType) error {
		canonicalType := agentType.Canonical()
		for _, idx := range paneIndices {
			pane, ok := paneByIndex[idx]
			if !ok {
				return fmt.Errorf("pane index %d not found in session", idx)
			}

			newTitle := tmux.FormatPaneName(opts.Session, canonicalType.String(), ntmIndex[canonicalType], "")
			info := AdoptedPaneInfo{
				PaneID:    pane.ID,
				PaneIndex: pane.Index,
				AgentType: canonicalType.String(),
				OldTitle:  pane.Title,
				NewTitle:  newTitle,
				NTMIndex:  ntmIndex[canonicalType],
			}

			if opts.AutoName && !opts.DryRun {
				if err := tmux.SetPaneTitle(pane.ID, newTitle); err != nil {
					return fmt.Errorf("failed to set title for pane %d: %v", idx, err)
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
			return output.New(output.WithJSON(jsonOutput)).Output(result)
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

func validateAdoptAssignments(assignments map[agentpkg.AgentType][]int) error {
	seen := make(map[int]agentpkg.AgentType)
	for _, spec := range supportedAdoptTypes {
		for _, idx := range assignments[spec.AgentType] {
			if idx < 0 {
				return fmt.Errorf("pane index %d is invalid; pane indices must be non-negative", idx)
			}
			if previous, ok := seen[idx]; ok {
				return fmt.Errorf("pane index %d assigned multiple times (%s and %s)", idx, previous, spec.AgentType)
			}
			seen[idx] = spec.AgentType
		}
	}
	if len(seen) == 0 {
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
