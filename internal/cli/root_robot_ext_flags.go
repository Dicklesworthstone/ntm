package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

var (
	// --robot-adopt flags
	robotAdopt         string
	robotAdoptCC       string
	robotAdoptCod      string
	robotAdoptGmi      string
	robotAdoptAgy      string
	robotAdoptGrok     string
	robotAdoptCursor   string
	robotAdoptWindsurf string
	robotAdoptAider    string
	robotAdoptOc       string
	robotAdoptOmp      string
	robotAdoptOllama   string
	robotAdoptUser     string
	robotAdoptByWindow string
	robotAdoptAutoName bool

	// --robot-redact flags
	robotRedact     string
	robotRedactFile string

	// --robot-hooks flags
	robotHooks              bool
	robotHooksAction        string
	robotHooksType          string
	robotHooksFailOnWarning bool

	// --robot-personas flags
	robotPersonas      bool
	robotPersonasAgent string
	robotPersonasTag   string

	// --robot-saved-sessions flags
	robotSavedSessions  bool
	robotSavedAction    string
	robotSavedName      string
	robotSavedSession   string
	robotSavedArchived  bool
	robotSavedOverwrite bool

	// --robot-mapping flag
	robotMapping string

	// --robot-workflows flags
	robotWorkflows     bool
	robotWorkflowsName string
	robotWorkflowsRole string

	// --robot-session-templates flags
	robotSessionTemplates bool
	robotTemplateName     string
)

func registerRobotExtFlags() {
	// Robot-adopt flags for adopting external tmux sessions across all 12 pane types
	rootCmd.Flags().StringVar(&robotAdopt, "robot-adopt", "", "Adopt an external tmux session into NTM management across all 11 agent harnesses plus user panes (JSON). Required: SESSION")
	rootCmd.Flags().StringVar(&robotAdoptCC, "adopt-cc", "", "Comma-separated pane specs (bare, range, or window.pane) for Claude (cc) agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptCod, "adopt-cod", "", "Comma-separated pane specs for Codex (cod) agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptGmi, "adopt-gmi", "", "Comma-separated pane specs for Gemini (gmi) agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptAgy, "adopt-agy", "", "Comma-separated pane specs for Antigravity (agy) agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptGrok, "adopt-grok", "", "Comma-separated pane specs for Grok Build (grok) agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptCursor, "adopt-cursor", "", "Comma-separated pane specs for Cursor agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptWindsurf, "adopt-windsurf", "", "Comma-separated pane specs for Windsurf agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptAider, "adopt-aider", "", "Comma-separated pane specs for Aider agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptOc, "adopt-oc", "", "Comma-separated pane specs for OpenCode (oc) agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptOmp, "adopt-omp", "", "Comma-separated pane specs for Oh My Pi (omp) agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptOllama, "adopt-ollama", "", "Comma-separated pane specs for Ollama agents (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptUser, "adopt-user", "", "Comma-separated pane specs for user shell panes (use with --robot-adopt)")
	rootCmd.Flags().StringVar(&robotAdoptByWindow, "adopt-by-window", "", "Map every window's sole pane to one harness type (e.g. cc, cod, agy; use with --robot-adopt)")
	rootCmd.Flags().BoolVar(&robotAdoptAutoName, "adopt-auto-name", true, "Automatically rename adopted panes to NTM title convention (default true, use with --robot-adopt)")

	// Robot-redact flags for secret/PII redaction preview without exposing raw secret bytes
	rootCmd.Flags().StringVar(&robotRedact, "robot-redact", "", "Preview secret/PII redaction findings and safe-redacted output without emitting raw secrets (JSON). Pass inline text or use --redact-file")
	rootCmd.Flags().Lookup("robot-redact").NoOptDefVal = "__present__"
	rootCmd.Flags().StringVar(&robotRedactFile, "redact-file", "", "Read text to scan/redact from file path (use with --robot-redact)")

	// Robot-hooks flags for repository git hook status and manual check execution
	rootCmd.Flags().BoolVar(&robotHooks, "robot-hooks", false, "Inspect installed repository git hooks or execute hook checks (JSON)")
	rootCmd.Flags().StringVar(&robotHooksAction, "hooks-action", "status", "Git hooks action: status (default) or run (use with --robot-hooks)")
	rootCmd.Flags().StringVar(&robotHooksType, "hooks-type", "pre-commit", "Hook type for --hooks-action=run: pre-commit (default), pre-push, commit-msg, post-commit, post-checkout")
	rootCmd.Flags().BoolVar(&robotHooksFailOnWarning, "hooks-fail-on-warning", true, "Treat UBS warnings as errors when running pre-commit (default true, use with --robot-hooks)")

	// Robot-personas flags for listing personas and profile sets across all 11 harnesses
	rootCmd.Flags().BoolVar(&robotPersonas, "robot-personas", false, "List built-in, user, and project agent personas and profile sets with 11-harness filtering (JSON)")
	rootCmd.Flags().StringVar(&robotPersonasAgent, "personas-agent", "", "Filter personas by harness alias (cc, cod, gmi, agy, grok, cursor, windsurf, aider, oc, omp, ollama)")
	rootCmd.Flags().StringVar(&robotPersonasTag, "personas-tag", "", "Filter personas by tag (use with --robot-personas)")

	// Robot-saved-sessions flags for saved session inspection, capture, archive, and unarchive
	rootCmd.Flags().BoolVar(&robotSavedSessions, "robot-saved-sessions", false, "List, inspect (with 11-harness counts and casr/native resume readiness), save, archive, or unarchive saved sessions (JSON)")
	rootCmd.Flags().StringVar(&robotSavedAction, "saved-action", "list", "Saved-session action: list (default), show, save, archive, or unarchive (use with --robot-saved-sessions)")
	rootCmd.Flags().StringVar(&robotSavedName, "saved-name", "", "Saved snapshot name for show/archive/unarchive, or custom save name (use with --robot-saved-sessions)")
	rootCmd.Flags().StringVar(&robotSavedSession, "saved-session", "", "Live tmux session name to capture when --saved-action=save (use with --robot-saved-sessions)")
	rootCmd.Flags().BoolVar(&robotSavedArchived, "saved-archived", false, "List archived saved sessions when --saved-action=list (use with --robot-saved-sessions)")
	rootCmd.Flags().BoolVar(&robotSavedOverwrite, "saved-overwrite", false, "Overwrite existing saved snapshot when --saved-action=save (use with --robot-saved-sessions)")

	// Robot-mapping flag for Agent Mail <-> tmux pane mapping & border badge reconciliation
	rootCmd.Flags().StringVar(&robotMapping, "robot-mapping", "", "Resolve Agent Mail names to live tmux panes, reconcile identity files, and publish/withdraw border badges (JSON). Required: SESSION")

	// Robot-workflows flags for workflow coordination templates and trigger validation
	rootCmd.Flags().BoolVar(&robotWorkflows, "robot-workflows", false, "List built-in, user, and project workflow coordination templates or inspect one with trigger validation (JSON)")
	rootCmd.Flags().StringVar(&robotWorkflowsName, "workflows-name", "", "Workflow template name to inspect in detail (use with --robot-workflows)")
	rootCmd.Flags().StringVar(&robotWorkflowsRole, "workflows-role", "", "Filter workflows or displayed agents by declared role (use with --robot-workflows)")

	// Robot-session-templates flags for multi-agent session templates
	rootCmd.Flags().BoolVar(&robotSessionTemplates, "robot-session-templates", false, "List built-in, user, and project session templates or inspect a template by name (JSON)")
	rootCmd.Flags().StringVar(&robotTemplateName, "template-name", "", "Session template name to inspect in detail (use with --robot-session-templates)")
}

func needsRobotExtConfigLoading() bool {
	return strings.Compare(robotAdopt, "") != 0 || (jsonArgumentRoot != nil && jsonArgumentRoot.Flags().Changed("robot-adopt")) ||
		strings.Compare(robotRedact, "") != 0 || (jsonArgumentRoot != nil && jsonArgumentRoot.Flags().Changed("robot-redact")) ||
		strings.Compare(robotMapping, "") != 0 || (jsonArgumentRoot != nil && jsonArgumentRoot.Flags().Changed("robot-mapping")) ||
		robotHooks ||
		robotPersonas ||
		robotSavedSessions ||
		robotWorkflows ||
		robotSessionTemplates
}

func dispatchRobotExtFlags(cmd *cobra.Command, robotDryRunEffective bool) bool {
	if strings.Compare(robotAdopt, "") != 0 || cmd.Flags().Changed("robot-adopt") {
		panes := make(map[string]string)
		adoptFlagSpecs := []struct {
			flagName string
			key      string
			value    string
		}{
			{"adopt-cc", "cc", robotAdoptCC},
			{"adopt-cod", "cod", robotAdoptCod},
			{"adopt-gmi", "gmi", robotAdoptGmi},
			{"adopt-agy", "agy", robotAdoptAgy},
			{"adopt-grok", "grok", robotAdoptGrok},
			{"adopt-cursor", "cursor", robotAdoptCursor},
			{"adopt-windsurf", "windsurf", robotAdoptWindsurf},
			{"adopt-aider", "aider", robotAdoptAider},
			{"adopt-oc", "oc", robotAdoptOc},
			{"adopt-omp", "omp", robotAdoptOmp},
			{"adopt-ollama", "ollama", robotAdoptOllama},
			{"adopt-user", "user", robotAdoptUser},
		}
		for _, spec := range adoptFlagSpecs {
			if cmd.Flags().Changed(spec.flagName) && strings.Compare(strings.TrimSpace(spec.value), "") != 0 {
				panes[spec.key] = spec.value
			}
		}
		autoName := robotAdoptAutoName
		byWindowType := strings.TrimSpace(robotAdoptByWindow)
		out, err := executeAdoptRobot(cmd.Context(), robot.AdoptInput{
			Session:      robotAdopt,
			Panes:        panes,
			ByWindow:     strings.Compare(byWindowType, "") != 0,
			ByWindowType: byWindowType,
			AutoName:     &autoName,
			DryRun:       robotDryRunEffective,
		})
		if err != nil {
			recordRobotProcessExit(err)
			return true
		}
		if err := robot.PrintAdoptOutput(out); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if strings.Compare(robotRedact, "") != 0 || cmd.Flags().Changed("robot-redact") {
		text := robotRedact
		if strings.Compare(text, "__present__") == 0 {
			text = ""
		}
		var redactCfg *redaction.Config
		if cfg != nil {
			rc := cfg.Redaction.ToRedactionLibConfig()
			rc.Mode = redaction.ModeRedact
			redactCfg = &rc
		}
		if err := robot.PrintRedactPreview(robot.RedactOptions{
			Text:      text,
			File:      robotRedactFile,
			RedactCfg: redactCfg,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if robotHooks {
		fow := robotHooksFailOnWarning
		if err := robot.PrintHooks(cmd.Context(), robot.HooksOptions{
			Action:        robotHooksAction,
			HookType:      robotHooksType,
			ProjectDir:    robotEnsembleProject,
			Verbose:       robotIsWorkingVerbose,
			FailOnWarning: &fow,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if robotPersonas {
		if err := robot.PrintPersonas(robot.PersonasOptions{
			ProjectDir: robotEnsembleProject,
			Agent:      robotPersonasAgent,
			Tag:        robotPersonasTag,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if robotSavedSessions {
		if err := robot.PrintSavedSessions(robot.SavedSessionsOptions{
			Action:    robotSavedAction,
			Name:      robotSavedName,
			Session:   robotSavedSession,
			Archived:  robotSavedArchived,
			Overwrite: robotSavedOverwrite,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if strings.Compare(robotMapping, "") != 0 || cmd.Flags().Changed("robot-mapping") {
		if err := robot.PrintAgentMapping(cmd.Context(), robot.AgentMappingOptions{
			Session:        robotMapping,
			ProjectDir:     robotEnsembleProject,
			Config:         cfg,
			BadgeTmux:      paneBadgeTmux,
			ReconcileFn:    reconcileSessionBadges,
			RequireSession: true,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if robotWorkflows {
		if err := robot.PrintWorkflows(robot.WorkflowsOptions{
			ProjectDir: robotEnsembleProject,
			Name:       robotWorkflowsName,
			Role:       robotWorkflowsRole,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if robotSessionTemplates {
		if err := robot.PrintSessionTemplates(robot.SessionTemplatesOptions{
			ProjectDir: robotEnsembleProject,
			Name:       robotTemplateName,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	return false
}
