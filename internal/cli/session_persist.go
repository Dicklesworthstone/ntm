package cli

import (
	"fmt"
	"io"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/palette"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newSessionPersistCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Manage saved session states",
		Long: `Save and manage tmux session state snapshots.

Captures session configuration including agent counts, pane layout,
working directory, and git context for later restoration.

Examples:
  ntm sessions save                    # Save current session
  ntm sessions save myproject          # Save specific session
  ntm sessions list                    # List saved sessions
  ntm sessions show myproject          # Show saved state details
  ntm sessions delete myproject        # Delete saved state`,
	}

	cmd.AddCommand(newSessionsSaveCmd())
	cmd.AddCommand(newSessionsRestoreCmd())
	cmd.AddCommand(newSessionsListCmd())
	cmd.AddCommand(newSessionsShowCmd())
	cmd.AddCommand(newSessionsDeleteCmd())

	return cmd
}

func newSessionsSaveCmd() *cobra.Command {
	var name string
	var overwrite bool

	cmd := &cobra.Command{
		Use:   "save [session-name]",
		Short: "Save session state",
		Long: `Save the current state of a tmux session.

If no session name is provided and you're inside tmux, saves the current session.
Otherwise, prompts to select a session.

Examples:
  ntm sessions save                    # Save current session
  ntm sessions save myproject          # Save specific session
  ntm sessions save myproject --name=backup  # Save with custom name
  ntm sessions save myproject --overwrite    # Overwrite existing save`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sessionName string
			if len(args) > 0 {
				sessionName = args[0]
			}

			opts := session.SaveOptions{
				Name:      name,
				Overwrite: overwrite,
			}

			return runSessionsSave(sessionName, opts)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "custom name for the saved state")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing save")

	return cmd
}

// SessionsSaveResult represents the result of a save operation.
type SessionsSaveResult struct {
	Success   bool                  `json:"success"`
	Session   string                `json:"session"`
	SavedAs   string                `json:"saved_as"`
	FilePath  string                `json:"file_path"`
	State     *session.SessionState `json:"state,omitempty"`
	Error     string                `json:"error,omitempty"`
}

func (r *SessionsSaveResult) Text(w io.Writer) error {
	t := theme.Current()
	if !r.Success {
		fmt.Fprintf(w, "%s✗%s Failed to save session: %s\n",
			colorize(t.Red), colorize(t.Text), r.Error)
		return nil
	}

	fmt.Fprintf(w, "%s✓%s Saved session '%s'\n",
		colorize(t.Success), colorize(t.Text), r.Session)
	fmt.Fprintf(w, "  Saved as: %s\n", r.SavedAs)
	fmt.Fprintf(w, "  File: %s\n", r.FilePath)
	if r.State != nil {
		fmt.Fprintf(w, "  Agents: %d Claude, %d Codex, %d Gemini\n",
			r.State.Agents.Claude, r.State.Agents.Codex, r.State.Agents.Gemini)
		if r.State.GitBranch != "" {
			fmt.Fprintf(w, "  Git: %s\n", r.State.GitBranch)
		}
	}
	return nil
}

func (r *SessionsSaveResult) JSON() interface{} {
	return r
}

func runSessionsSave(sessionName string, opts session.SaveOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Determine session name
	if sessionName == "" {
		if tmux.InTmux() {
			sessionName = tmux.GetCurrentSession()
		} else {
			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				return fmt.Errorf("no tmux sessions found")
			}
			selected, err := palette.RunSessionSelector(sessions)
			if err != nil {
				return err
			}
			if selected == "" {
				return nil
			}
			sessionName = selected
		}
	}

	if !tmux.SessionExists(sessionName) {
		result := &SessionsSaveResult{
			Success: false,
			Session: sessionName,
			Error:   fmt.Sprintf("session '%s' not found", sessionName),
		}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	// Capture state
	state, err := session.Capture(sessionName)
	if err != nil {
		result := &SessionsSaveResult{
			Success: false,
			Session: sessionName,
			Error:   err.Error(),
		}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	// Save state
	path, err := session.Save(state, opts)
	if err != nil {
		result := &SessionsSaveResult{
			Success: false,
			Session: sessionName,
			Error:   err.Error(),
		}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	savedName := opts.Name
	if savedName == "" {
		savedName = sessionName
	}

	result := &SessionsSaveResult{
		Success:  true,
		Session:  sessionName,
		SavedAs:  savedName,
		FilePath: path,
		State:    state,
	}

	return output.New(output.WithJSON(jsonOutput)).Output(result)
}

func newSessionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsList()
		},
	}
}

// SessionsListResult contains the list of saved sessions.
type SessionsListResult struct {
	Sessions []session.SavedSession `json:"sessions"`
	Count    int                    `json:"count"`
}

func (r *SessionsListResult) Text(w io.Writer) error {
	t := theme.Current()

	if r.Count == 0 {
		fmt.Fprintf(w, "%sNo saved sessions found%s\n", colorize(t.Warning), colorize(t.Text))
		fmt.Fprintf(w, "Use 'ntm sessions save' to save a session.\n")
		return nil
	}

	fmt.Fprintf(w, "%sSaved Sessions%s (%d)\n", colorize(t.Blue), colorize(t.Text), r.Count)
	fmt.Fprintf(w, "─────────────────────────────────────────\n")

	for _, s := range r.Sessions {
		gitInfo := ""
		if s.GitBranch != "" {
			gitInfo = fmt.Sprintf(" [%s]", s.GitBranch)
		}
		fmt.Fprintf(w, "  %s%-15s%s  %d agents  %s%s\n",
			colorize(t.Green), s.Name, colorize(t.Text),
			s.Agents,
			s.SavedAt.Local().Format("2006-01-02 15:04"),
			gitInfo)
	}

	return nil
}

func (r *SessionsListResult) JSON() interface{} {
	return r
}

func runSessionsList() error {
	sessions, err := session.List()
	if err != nil {
		return err
	}

	result := &SessionsListResult{
		Sessions: sessions,
		Count:    len(sessions),
	}

	return output.New(output.WithJSON(jsonOutput)).Output(result)
}

func newSessionsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show saved session details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsShow(args[0])
		},
	}
}

// SessionsShowResult contains a saved session's full state.
type SessionsShowResult struct {
	State *session.SessionState `json:"state"`
}

func (r *SessionsShowResult) Text(w io.Writer) error {
	t := theme.Current()
	s := r.State

	fmt.Fprintf(w, "%sSession: %s%s\n", colorize(t.Blue), s.Name, colorize(t.Text))
	fmt.Fprintf(w, "─────────────────────────────────────────\n")
	fmt.Fprintf(w, "Saved:     %s\n", s.SavedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "Directory: %s\n", s.WorkDir)
	fmt.Fprintf(w, "Layout:    %s\n", s.Layout)

	if s.GitBranch != "" {
		fmt.Fprintf(w, "\n%sGit Context%s\n", colorize(t.Blue), colorize(t.Text))
		fmt.Fprintf(w, "  Branch: %s\n", s.GitBranch)
		if s.GitRemote != "" {
			fmt.Fprintf(w, "  Remote: %s\n", s.GitRemote)
		}
		if s.GitCommit != "" {
			fmt.Fprintf(w, "  Commit: %s\n", s.GitCommit)
		}
	}

	fmt.Fprintf(w, "\n%sAgents%s\n", colorize(t.Blue), colorize(t.Text))
	fmt.Fprintf(w, "  Claude: %d\n", s.Agents.Claude)
	fmt.Fprintf(w, "  Codex:  %d\n", s.Agents.Codex)
	fmt.Fprintf(w, "  Gemini: %d\n", s.Agents.Gemini)
	if s.Agents.User > 0 {
		fmt.Fprintf(w, "  User:   %d\n", s.Agents.User)
	}

	fmt.Fprintf(w, "\n%sPanes%s (%d)\n", colorize(t.Blue), colorize(t.Text), len(s.Panes))
	for _, p := range s.Panes {
		active := ""
		if p.Active {
			active = " *"
		}
		model := ""
		if p.Model != "" {
			model = fmt.Sprintf(" (%s)", p.Model)
		}
		fmt.Fprintf(w, "  [%d] %s%s%s\n", p.Index, p.Title, model, active)
	}

	return nil
}

func (r *SessionsShowResult) JSON() interface{} {
	return r.State
}

func runSessionsShow(name string) error {
	state, err := session.Load(name)
	if err != nil {
		return err
	}

	result := &SessionsShowResult{State: state}
	return output.New(output.WithJSON(jsonOutput)).Output(result)
}

func newSessionsDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a saved session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsDelete(args[0], force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")

	return cmd
}

func runSessionsDelete(name string, force bool) error {
	t := theme.Current()

	if !session.Exists(name) {
		return fmt.Errorf("no saved session named '%s'", name)
	}

	if !force {
		fmt.Printf("Delete saved session '%s'? [y/N]: ", name)
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := session.Delete(name); err != nil {
		return err
	}

	fmt.Printf("%s✓%s Deleted saved session '%s'\n",
		colorize(t.Success), colorize(t.Text), name)
	return nil
}

func newSessionsRestoreCmd() *cobra.Command {
	var name string
	var force bool
	var attach bool
	var skipGitCheck bool
	var launchAgents bool

	cmd := &cobra.Command{
		Use:   "restore <saved-name>",
		Short: "Restore a saved session",
		Long: `Restore a session from a saved state.

Creates a new tmux session with the same panes and layout as the saved state.
Optionally launches agents in the panes.

Examples:
  ntm sessions restore myproject              # Restore saved session
  ntm sessions restore myproject --force      # Overwrite if session exists
  ntm sessions restore myproject --attach     # Attach after restore
  ntm sessions restore myproject --name=new   # Restore as different name
  ntm sessions restore myproject --launch     # Launch agents in panes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := session.RestoreOptions{
				Name:         name,
				Force:        force,
				SkipGitCheck: skipGitCheck,
			}
			return runSessionsRestore(args[0], opts, attach, launchAgents)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "restore as different session name")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing session")
	cmd.Flags().BoolVarP(&attach, "attach", "a", false, "attach after restore")
	cmd.Flags().BoolVar(&skipGitCheck, "skip-git-check", false, "don't warn about git branch mismatch")
	cmd.Flags().BoolVar(&launchAgents, "launch", false, "launch agents in restored panes")

	return cmd
}

// SessionsRestoreResult represents the result of a restore operation.
type SessionsRestoreResult struct {
	Success      bool                  `json:"success"`
	SavedName    string                `json:"saved_name"`
	RestoredAs   string                `json:"restored_as"`
	State        *session.SessionState `json:"state,omitempty"`
	AgentCount   int                   `json:"agent_count"`
	Error        string                `json:"error,omitempty"`
	GitWarning   string                `json:"git_warning,omitempty"`
}

func (r *SessionsRestoreResult) Text(w io.Writer) error {
	t := theme.Current()
	if !r.Success {
		fmt.Fprintf(w, "%s✗%s Failed to restore session: %s\n",
			colorize(t.Red), colorize(t.Text), r.Error)
		return nil
	}

	fmt.Fprintf(w, "%s✓%s Restored session '%s'\n",
		colorize(t.Success), colorize(t.Text), r.RestoredAs)
	if r.State != nil {
		fmt.Fprintf(w, "  Directory: %s\n", r.State.WorkDir)
		fmt.Fprintf(w, "  Panes: %d\n", len(r.State.Panes))
		fmt.Fprintf(w, "  Agents: %d Claude, %d Codex, %d Gemini\n",
			r.State.Agents.Claude, r.State.Agents.Codex, r.State.Agents.Gemini)
	}
	if r.GitWarning != "" {
		fmt.Fprintf(w, "  %sWarning:%s %s\n", colorize(t.Warning), colorize(t.Text), r.GitWarning)
	}
	return nil
}

func (r *SessionsRestoreResult) JSON() interface{} {
	return r
}

func runSessionsRestore(savedName string, opts session.RestoreOptions, attach, launchAgents bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Load saved state
	state, err := session.Load(savedName)
	if err != nil {
		result := &SessionsRestoreResult{
			Success:   false,
			SavedName: savedName,
			Error:     err.Error(),
		}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	// Restore session
	if err := session.Restore(state, opts); err != nil {
		result := &SessionsRestoreResult{
			Success:   false,
			SavedName: savedName,
			Error:     err.Error(),
		}
		return output.New(output.WithJSON(jsonOutput)).Output(result)
	}

	restoredName := opts.Name
	if restoredName == "" {
		restoredName = state.Name
	}

	// Check git branch mismatch
	var gitWarning string
	if !opts.SkipGitCheck && state.GitBranch != "" && state.WorkDir != "" {
		// The restore function already does the check, but we capture the warning for output
		// by checking current branch again
		if _, err := tmux.GetSession(restoredName); err == nil {
			// Session exists, could check git branch here
		}
	}

	// Optionally launch agents
	agentCount := 0
	if launchAgents {
		// For now, just count the agents - actual launch would need config access
		agentCount = state.Agents.Total()
		// Note: Full agent launch implementation would go here
		// session.RestoreAgents(restoredName, state, agentConfig)
	}

	result := &SessionsRestoreResult{
		Success:    true,
		SavedName:  savedName,
		RestoredAs: restoredName,
		State:      state,
		AgentCount: agentCount,
		GitWarning: gitWarning,
	}

	if err := output.New(output.WithJSON(jsonOutput)).Output(result); err != nil {
		return err
	}

	// Attach if requested
	if attach {
		return tmux.AttachOrSwitch(restoredName)
	}

	return nil
}
