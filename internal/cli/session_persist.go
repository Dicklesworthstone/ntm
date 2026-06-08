package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newSessionPersistCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Manage saved session states",
		Long: `Save, archive, and resume tmux session state snapshots.

Captures session topology including windows, panes, splits/layout,
working directory, git context, agent counts, and (best-effort) the
per-pane agent CLI session id so the session can be resumed.

Resume reconstructs the tmux topology (ntm-owned) and relaunches each
pane's agent, delegating per-pane agent-session resume to casr
(Cross Agent Session Resumer) or the agent's native --resume <id>.

Examples:
  ntm sessions save                    # Save current session
  ntm sessions save myproject          # Save specific session
  ntm sessions list                    # List saved sessions
  ntm sessions list --archived         # List archived sessions
  ntm sessions show myproject          # Show saved state details
  ntm sessions resume myproject        # Rebuild topology + resume agents
  ntm sessions restore myproject       # Rebuild topology (fresh agents)
  ntm sessions archive myproject       # Move a saved session to archive
  ntm sessions unarchive myproject     # Restore an archived session
  ntm sessions delete myproject        # Delete saved state`,
	}

	cmd.AddCommand(newSessionsSaveCmd())
	cmd.AddCommand(newSessionsRestoreCmd())
	cmd.AddCommand(newSessionsResumeCmd())
	cmd.AddCommand(newSessionsListCmd())
	cmd.AddCommand(newSessionsShowCmd())
	cmd.AddCommand(newSessionsDeleteCmd())
	cmd.AddCommand(newSessionsArchiveCmd())
	cmd.AddCommand(newSessionsUnarchiveCmd())

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
	Success  bool                  `json:"success"`
	Session  string                `json:"session"`
	SavedAs  string                `json:"saved_as"`
	FilePath string                `json:"file_path"`
	State    *session.SessionState `json:"state,omitempty"`
	Error    string                `json:"error,omitempty"`
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
	// bd-oqwmf: emitSaveFailure writes the success:false envelope and
	// signals non-zero exit so `ntm sessions save --json` automation
	// gating on `$?` no longer treats failure as success.
	// bd-1yws7: hoisted above the tmux.EnsureInstalled() check so the
	// early-fail path also emits a parseable envelope when --json is set.
	emitSaveFailure := func(result *SessionsSaveResult) error {
		if encErr := output.New(output.WithJSON(jsonOutput)).Output(result); encErr != nil {
			return encErr
		}
		return jsonFailureExit()
	}

	if err := tmux.EnsureInstalled(); err != nil {
		if jsonOutput {
			return emitSaveFailure(&SessionsSaveResult{
				Success: false,
				Session: sessionName,
				Error:   err.Error(),
			})
		}
		return err
	}

	res, err := ResolveSessionWithOptions(sessionName, os.Stdout, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
	if err != nil {
		if jsonOutput {
			return emitSaveFailure(&SessionsSaveResult{
				Success: false,
				Session: sessionName,
				Error:   err.Error(),
			})
		}
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(os.Stderr)
	sessionName = res.Session

	if !tmux.SessionExists(sessionName) {
		return emitSaveFailure(&SessionsSaveResult{
			Success: false,
			Session: sessionName,
			Error:   fmt.Sprintf("session '%s' not found", sessionName),
		})
	}

	// Capture state
	state, err := session.Capture(sessionName)
	if err != nil {
		return emitSaveFailure(&SessionsSaveResult{
			Success: false,
			Session: sessionName,
			Error:   err.Error(),
		})
	}

	// Save state
	path, err := session.Save(state, opts)
	if err != nil {
		return emitSaveFailure(&SessionsSaveResult{
			Success: false,
			Session: sessionName,
			Error:   err.Error(),
		})
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
	var archived bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsList(archived)
		},
	}
	cmd.Flags().BoolVar(&archived, "archived", false, "list archived sessions instead of active ones")
	return cmd
}

// SessionsListResult contains the list of saved sessions.
type SessionsListResult struct {
	Sessions []session.SavedSession `json:"sessions"`
	Count    int                    `json:"count"`
	Archived bool                   `json:"archived"`
}

func (r *SessionsListResult) Text(w io.Writer) error {
	t := theme.Current()

	label := "Saved Sessions"
	if r.Archived {
		label = "Archived Sessions"
	}

	if r.Count == 0 {
		if r.Archived {
			fmt.Fprintf(w, "%sNo archived sessions found%s\n", colorize(t.Warning), colorize(t.Text))
			fmt.Fprintf(w, "Use 'ntm sessions archive <name>' to archive a session.\n")
			return nil
		}
		fmt.Fprintf(w, "%sNo saved sessions found%s\n", colorize(t.Warning), colorize(t.Text))
		fmt.Fprintf(w, "Use 'ntm sessions save' to save a session.\n")
		return nil
	}

	fmt.Fprintf(w, "%s%s%s (%d)\n", colorize(t.Blue), label, colorize(t.Text), r.Count)
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

func runSessionsList(archived bool) error {
	listFn := session.List
	if archived {
		listFn = session.ListArchived
	}
	sessions, err := listFn()
	if err != nil {
		if jsonOutput {
			// bd-1yws7: route the read failure through a JSON envelope so
			// `ntm sessions list --json | jq ...` automation sees a
			// parseable error instead of stderr text + exit 0.
			return emitJSONFailureEnvelope(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
		}
		return err
	}

	result := &SessionsListResult{
		Sessions: sessions,
		Count:    len(sessions),
		Archived: archived,
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
		if jsonOutput {
			// bd-1yws7: same envelope routing as runSessionsList — a
			// missing/corrupt saved-session file under --json should be
			// parseable on stdout and signal non-zero exit.
			return emitJSONFailureEnvelope(map[string]interface{}{
				"success": false,
				"session": name,
				"error":   err.Error(),
			})
		}
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

	// bd-1yws7: every failure-emitting path under --json goes through
	// emitJSONFailureEnvelope so automation gates on `$?` no longer treat
	// missing-saved-state, missing-confirmation, or session.Delete failures
	// as success. Pre-bd-1yws7 the missing-name/Delete-failure paths
	// returned raw errors (bypassing --json) and the missing-confirmation
	// path used output.PrintJSON which always returns nil → exit 0.
	emitDeleteFailure := func(errMsg string) error {
		return emitJSONFailureEnvelope(map[string]interface{}{
			"success": false,
			"session": name,
			"error":   errMsg,
		})
	}

	if !session.Exists(name) {
		if jsonOutput {
			return emitDeleteFailure(fmt.Sprintf("no saved session named '%s'", name))
		}
		return fmt.Errorf("no saved session named '%s'", name)
	}

	if !force {
		if jsonOutput {
			return emitDeleteFailure("confirmation required (use --force)")
		}
		fmt.Printf("Delete saved session '%s'? [y/N]: ", name)
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := session.Delete(name); err != nil {
		if jsonOutput {
			return emitDeleteFailure(err.Error())
		}
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
	Success    bool                  `json:"success"`
	SavedName  string                `json:"saved_name"`
	RestoredAs string                `json:"restored_as"`
	State      *session.SessionState `json:"state,omitempty"`
	AgentCount int                   `json:"agent_count"`
	Error      string                `json:"error,omitempty"`
	GitWarning string                `json:"git_warning,omitempty"`
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
	// bd-oqwmf: emitRestoreFailure writes the success:false envelope and
	// signals non-zero exit (parity with bd-usgfy).
	// bd-1yws7: hoisted above the tmux.EnsureInstalled() check so the
	// early-fail path also emits a parseable envelope when --json is set.
	emitRestoreFailure := func(result *SessionsRestoreResult) error {
		if encErr := output.New(output.WithJSON(jsonOutput)).Output(result); encErr != nil {
			return encErr
		}
		return jsonFailureExit()
	}

	if err := tmux.EnsureInstalled(); err != nil {
		if jsonOutput {
			return emitRestoreFailure(&SessionsRestoreResult{
				Success:   false,
				SavedName: savedName,
				Error:     err.Error(),
			})
		}
		return err
	}

	// Load saved state
	state, err := session.Load(savedName)
	if err != nil {
		return emitRestoreFailure(&SessionsRestoreResult{
			Success:   false,
			SavedName: savedName,
			Error:     err.Error(),
		})
	}

	// Restore session
	if err := session.Restore(state, opts); err != nil {
		return emitRestoreFailure(&SessionsRestoreResult{
			Success:   false,
			SavedName: savedName,
			Error:     err.Error(),
		})
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
	var launchErr error
	agentCount := 0
	if launchAgents {
		if cfg != nil {
			// Render the agent command templates before launching: the raw
			// cfg.Agents.* values are Go templates and must not be sent into a
			// pane verbatim (a literal `{{memLimitPrefix}} claude ...` would fail
			// to exec and the agent would never launch).
			cmds := buildAgentCommands(state)
			launchErr = session.RestoreAgents(restoredName, state, cmds)
		}
		agentCount = state.Agents.Total()
	}

	result := &SessionsRestoreResult{
		Success:    true,
		SavedName:  savedName,
		RestoredAs: restoredName,
		State:      state,
		AgentCount: agentCount,
		GitWarning: gitWarning,
	}

	if launchErr != nil {
		result.Error = fmt.Sprintf("restored session but failed to launch agents: %v", launchErr)
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

func newSessionsResumeCmd() *cobra.Command {
	var (
		name       string
		force      bool
		attach     bool
		nativeFlag bool
	)

	cmd := &cobra.Command{
		Use:   "resume <saved-name>",
		Short: "Resume a saved session (rebuild topology + resume agents)",
		Long: `Resume a saved session.

Reconstructs the tmux topology (windows, panes, splits, cwd, layout) and
relaunches each pane's agent. Panes that captured a provider session id at
save time are resumed via casr (Cross Agent Session Resumer) when available,
or the agent's native --resume <id>. Panes without a captured id are launched
fresh. ntm owns the topology restore; per-pane agent-session resume is
delegated to casr, not reimplemented here.

Examples:
  ntm sessions resume myproject            # Resume topology + agents
  ntm sessions resume myproject --force    # Overwrite if session exists
  ntm sessions resume myproject --native   # Use native --resume, skip casr
  ntm sessions resume myproject --attach    # Attach after resume`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsResume(args[0], name, force, attach, !nativeFlag)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "resume as different session name")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing session")
	cmd.Flags().BoolVarP(&attach, "attach", "a", false, "attach after resume")
	cmd.Flags().BoolVar(&nativeFlag, "native", false, "use the agent's native --resume instead of casr")

	return cmd
}

// SessionsResumeResult reports the outcome of a resume operation.
type SessionsResumeResult struct {
	Success   bool                 `json:"success"`
	SavedName string               `json:"saved_name"`
	ResumedAs string               `json:"resumed_as"`
	Resumed   int                  `json:"resumed"`
	Launched  int                  `json:"launched"`
	Skipped   int                  `json:"skipped"`
	Panes     []session.ResumePane `json:"panes,omitempty"`
	Error     string               `json:"error,omitempty"`
}

func (r *SessionsResumeResult) Text(w io.Writer) error {
	t := theme.Current()
	if !r.Success {
		fmt.Fprintf(w, "%s✗%s Failed to resume session: %s\n",
			colorize(t.Red), colorize(t.Text), r.Error)
		return nil
	}

	fmt.Fprintf(w, "%s✓%s Resumed session '%s'\n",
		colorize(t.Success), colorize(t.Text), r.ResumedAs)
	fmt.Fprintf(w, "  %d agent(s) resumed, %d launched fresh, %d skipped\n",
		r.Resumed, r.Launched, r.Skipped)
	for _, p := range r.Panes {
		marker := "·"
		switch p.Action {
		case "resumed":
			marker = "↻"
		case "launched":
			marker = "+"
		}
		idInfo := ""
		if p.SessionID != "" {
			idInfo = fmt.Sprintf(" [%s:%s]", p.Provider, p.SessionID)
		}
		fmt.Fprintf(w, "  %s [%d] %s (%s)%s\n", marker, p.Index, p.Title, p.Action, idInfo)
	}
	return nil
}

func (r *SessionsResumeResult) JSON() interface{} { return r }

// buildAgentCommands renders each configured agent command template into a
// concrete, shell-ready command for the given saved session. The config values
// (cfg.Agents.*) are Go text/template strings (e.g. `{{memLimitPrefix}} claude
// --dangerously-skip-permissions{{if .Model}} --model {{shellQuote .Model}}{{end}}`).
// They MUST be rendered before being sent into a pane via send-keys — otherwise
// the shell tries to exec a literal command named `{{memLimitPrefix}}` and the
// agent never launches. This mirrors the spawn paths (controller.go, add.go,
// init.go, auth/restart.go) which all render via config.GenerateAgentCommand.
//
// Rendering uses session-level template vars (session name + project dir); the
// fresh-launch fallback does not request a specific model, so the no-model
// branch of each template is taken. A render failure for any single agent is
// non-fatal: that agent's command is left empty so the restore/resume path
// simply skips launching it rather than sending a broken template into a pane.
func buildAgentCommands(state *session.SessionState) session.AgentCommands {
	if cfg == nil || state == nil {
		return session.AgentCommands{}
	}

	vars := config.AgentTemplateVars{
		SessionName: state.Name,
		ProjectDir:  state.WorkDir,
		PaneIndex:   1,
	}

	render := func(tmpl, agentType string) string {
		if tmpl == "" {
			return ""
		}
		v := vars
		v.AgentType = agentType
		rendered, err := config.GenerateAgentCommand(tmpl, v)
		if err != nil {
			// Leave empty so the launch path skips this agent rather than
			// sending an unrendered template into the pane.
			return ""
		}
		return rendered
	}

	return session.AgentCommands{
		Claude:   render(cfg.Agents.Claude, "cc"),
		Codex:    render(cfg.Agents.Codex, "cod"),
		Gemini:   render(cfg.Agents.Gemini, "gmi"),
		Cursor:   render(cfg.Agents.Cursor, "cursor"),
		Windsurf: render(cfg.Agents.Windsurf, "windsurf"),
		Aider:    render(cfg.Agents.Aider, "aider"),
		Opencode: render(cfg.Agents.Opencode, "opencode"),
		Ollama:   render(cfg.Agents.Ollama, "ollama"),
	}
}

func runSessionsResume(savedName, name string, force, attach, preferCASR bool) error {
	emitFailure := func(result *SessionsResumeResult) error {
		if encErr := output.New(output.WithJSON(jsonOutput)).Output(result); encErr != nil {
			return encErr
		}
		return jsonFailureExit()
	}

	if err := tmux.EnsureInstalled(); err != nil {
		if jsonOutput {
			return emitFailure(&SessionsResumeResult{Success: false, SavedName: savedName, Error: err.Error()})
		}
		return err
	}

	state, err := session.Load(savedName)
	if err != nil {
		return emitFailure(&SessionsResumeResult{Success: false, SavedName: savedName, Error: err.Error()})
	}

	// Render the agent command templates before launching: the raw cfg.Agents.*
	// values are Go templates and must not be sent into a pane verbatim.
	cmds := buildAgentCommands(state)

	res, err := session.Resume(state, cmds, session.ResumeOptions{
		Name:       name,
		Force:      force,
		PreferCASR: preferCASR,
	})
	if err != nil {
		return emitFailure(&SessionsResumeResult{Success: false, SavedName: savedName, Error: err.Error()})
	}

	result := &SessionsResumeResult{
		Success:   true,
		SavedName: savedName,
		ResumedAs: res.Session,
		Resumed:   res.Resumed,
		Launched:  res.Launched,
		Skipped:   res.Skipped,
		Panes:     res.Panes,
	}

	if err := output.New(output.WithJSON(jsonOutput)).Output(result); err != nil {
		return err
	}

	if attach {
		return tmux.AttachOrSwitch(res.Session)
	}
	return nil
}

func newSessionsArchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive <name>",
		Short: "Archive a saved session",
		Long: `Move a saved session into the archive.

Archived sessions no longer appear in 'ntm sessions list' but remain fully
restorable via 'ntm sessions unarchive <name>'. Use 'ntm sessions list
--archived' to view them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsArchive(args[0])
		},
	}
	return cmd
}

// SessionsArchiveResult reports the outcome of an archive/unarchive operation.
type SessionsArchiveResult struct {
	Success  bool   `json:"success"`
	Session  string `json:"session"`
	Action   string `json:"action"`
	FilePath string `json:"file_path,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (r *SessionsArchiveResult) Text(w io.Writer) error {
	t := theme.Current()
	if !r.Success {
		fmt.Fprintf(w, "%s✗%s Failed to %s session: %s\n",
			colorize(t.Red), colorize(t.Text), r.Action, r.Error)
		return nil
	}
	verb := "Archived"
	if r.Action == "unarchive" {
		verb = "Unarchived"
	}
	fmt.Fprintf(w, "%s✓%s %s session '%s'\n",
		colorize(t.Success), colorize(t.Text), verb, r.Session)
	if r.FilePath != "" {
		fmt.Fprintf(w, "  File: %s\n", r.FilePath)
	}
	return nil
}

func (r *SessionsArchiveResult) JSON() interface{} { return r }

func runSessionsArchive(name string) error {
	emitFailure := func(errMsg string) error {
		result := &SessionsArchiveResult{Success: false, Session: name, Action: "archive", Error: errMsg}
		if jsonOutput {
			if encErr := output.New(output.WithJSON(jsonOutput)).Output(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("%s", errMsg)
	}

	path, err := session.Archive(name)
	if err != nil {
		return emitFailure(err.Error())
	}

	result := &SessionsArchiveResult{Success: true, Session: name, Action: "archive", FilePath: path}
	return output.New(output.WithJSON(jsonOutput)).Output(result)
}

func newSessionsUnarchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "unarchive <name>",
		Aliases: []string{"restore-archived"},
		Short:   "Restore an archived session",
		Long: `Move an archived session back into the active store.

The session reappears in 'ntm sessions list' and can be resumed/restored
normally.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsUnarchive(args[0])
		},
	}
	return cmd
}

func runSessionsUnarchive(name string) error {
	emitFailure := func(errMsg string) error {
		result := &SessionsArchiveResult{Success: false, Session: name, Action: "unarchive", Error: errMsg}
		if jsonOutput {
			if encErr := output.New(output.WithJSON(jsonOutput)).Output(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("%s", errMsg)
	}

	path, err := session.Unarchive(name)
	if err != nil {
		return emitFailure(err.Error())
	}

	result := &SessionsArchiveResult{Success: true, Session: name, Action: "unarchive", FilePath: path}
	return output.New(output.WithJSON(jsonOutput)).Output(result)
}
