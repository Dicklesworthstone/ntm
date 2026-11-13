package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func runRootCommand(cmd *cobra.Command, args []string) {
	// Resolve robot output format and verbosity: CLI flag > env var > config > default.
	// Invalid values remain warnings for human use, but robot commands fail
	// before dispatch so stdout contains exactly one JSON error document.
	formatErr := resolveRobotFormat(cfg)
	verbosityErr := resolveRobotVerbosity(cfg)
	if robotInvocation, robotCommand := robotInvocationFromArgs(os.Args[1:]); robotInvocation {
		if cmd.Flags().Changed("restart-env-file") && robotCommand != "robot-restart-pane" {
			failRobotCommand(errors.New("--restart-env-file is only supported with --robot-restart-pane"), robot.ErrCodeInvalidFlag, "Select one agent pane with --robot-restart-pane and --panes", robotCommand)
			return
		}
		if formatErr != nil {
			failRobotCommand(formatErr, robot.ErrCodeInvalidFlag, "Use json, toon, or auto", robotCommand)
			return
		}
		if verbosityErr != nil {
			failRobotCommand(verbosityErr, robot.ErrCodeInvalidFlag, "Use terse, default, or debug", robotCommand)
			return
		}
		// An explicit pane selector the surface does not read must
		// fail before any actuation instead of silently broadening
		// the target to the whole session (ntm#308).
		if err := validateRobotPaneSelectorFlags(cmd, robotCommand); err != nil {
			var selectorErr *robotPaneSelectorFlagError
			hint := "Check the pane selector flags this robot command accepts"
			if errors.As(err, &selectorErr) {
				hint = selectorErr.hint
			}
			failRobotCommand(err, robot.ErrCodeInvalidFlag, hint, robotCommand)
			return
		}
	} else {
		if formatErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v, using default (auto)\n", formatErr)
		}
		if verbosityErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v, using default verbosity\n", verbosityErr)
		}
	}
	robotDryRunEffective := robotDryRun || robotRestoreDry

	if dispatchRobotInspectionAndOps(cmd, args) {
		return
	}
	if dispatchRobotActionsAndParity(cmd, robotDryRunEffective) {
		return
	}

	// Show help with appropriate verbosity when run without subcommand
	showMinimal := helpMinimal
	if !helpMinimal && helpFull {
		showMinimal = false
	}
	if !helpMinimal && !helpFull && cfg != nil {
		// Optional config default (bd-352n): help_verbosity = minimal|full
		if strings.EqualFold(strings.TrimSpace(cfg.HelpVerbosity), "minimal") {
			showMinimal = true
		}
	}

	if showMinimal {
		PrintMinimalHelp(cmd.OutOrStdout())
	} else {
		// Default to full help (current stunning help)
		PrintStunningHelp(cmd.OutOrStdout())
	}
}

func dispatchRobotInspectionAndOps(cmd *cobra.Command, args []string) bool {
	// Handle robot flags for AI agent integration
	if robotHelp {
		robot.PrintHelp()
		return true
	}
	if robotStatus {
		pagination, err := resolveRobotPagination(cmd)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Pagination values must be zero or greater", "robot-status")
			return true
		}
		if err := robot.PrintStatusWithOptions(pagination); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotVersion {
		if err := robot.PrintVersion(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotCapabilities {
		opts := robot.CapabilitiesOptions{
			Command:  robotCapabilitiesCommand,
			Category: robotCapabilitiesCategory,
			Query:    robotCapabilitiesSearch,
			Compact:  robotCapabilitiesCompact,
		}
		if err := robot.PrintCapabilitiesWithOptions(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if cmd.Flags().Changed("robot-docs") {
		if err := robot.PrintDocs(robotDocs); err != nil {
			recordRobotProcessExit(err)
			return true
		}
		return true
	}
	if robotPlan {
		if err := robot.PrintPlan(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSnapshot {
		// Set bead limit from flag
		if robotBeadLimit > 0 {
			robot.BeadLimit = robotBeadLimit
		}
		pagination, err := resolveRobotPagination(cmd)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Pagination values must be zero or greater", "robot-snapshot")
			return true
		}
		if robotSince != "" {
			// Parse the since timestamp
			sinceTime, parseErr := time.Parse(time.RFC3339, robotSince)
			if parseErr != nil {
				err = fmt.Errorf("invalid --since timestamp (expected ISO8601/RFC3339 format): %w", parseErr)
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use an ISO8601/RFC3339 timestamp", "robot-snapshot")
				return true
			}
			err = robot.PrintSnapshotDelta(sinceTime)
		} else {
			err = robot.PrintSnapshotWithOptions(cfg, pagination)
		}
		if err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotEvents {
		session, err := resolveOptionalRobotSessionFilter(cmd.Context(), robotEventsSession)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-events")
			return true
		}
		opts := robot.EventsOptions{
			SinceCursor: robotEventsSinceCursor,
			Limit:       robotEventsLimit,
			IncidentID:  robotEventsIncident,
			Session:     session,
			Profile:     robotProfile,
		}
		if robotEventsAsOf != "" {
			asOf, err := time.Parse(time.RFC3339, robotEventsAsOf)
			if err != nil {
				err = fmt.Errorf("invalid --events-as-of timestamp (expected RFC3339): %w", err)
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use an RFC3339 timestamp", "robot-events")
				return true
			}
			opts.AsOf = asOf
		}
		if robotEventsWindowBefore != "" {
			windowBefore, err := time.ParseDuration(robotEventsWindowBefore)
			if err != nil {
				err = fmt.Errorf("invalid --events-window-before duration: %w", err)
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a Go duration such as 5m or 30s", "robot-events")
				return true
			}
			opts.WindowBefore = windowBefore
		}
		if robotEventsWindowAfter != "" {
			windowAfter, err := time.ParseDuration(robotEventsWindowAfter)
			if err != nil {
				err = fmt.Errorf("invalid --events-window-after duration: %w", err)
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a Go duration such as 1m or 30s", "robot-events")
				return true
			}
			opts.WindowAfter = windowAfter
		}
		if robotEventsCategory != "" {
			opts.CategoryFilter = []string{robotEventsCategory}
		}
		if robotEventsActionability != "" {
			opts.ActionabilityFilter = []string{robotEventsActionability}
		}
		if err := robot.PrintEvents(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotAttention {
		session, err := resolveOptionalRobotSessionFilter(cmd.Context(), robotAttentionSession)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-attention")
			return true
		}
		timeout, err := time.ParseDuration(robotAttentionTimeout)
		if err != nil {
			err = fmt.Errorf("invalid attention timeout %q: %w", robotAttentionTimeout, err)
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a Go duration such as 5m or 30s", "robot-attention")
			return true
		}
		poll, err := time.ParseDuration(robotAttentionPoll)
		if err != nil {
			err = fmt.Errorf("invalid attention poll %q: %w", robotAttentionPoll, err)
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a Go duration such as 1s or 500ms", "robot-attention")
			return true
		}
		sinceCursor := robotAttentionSinceCursor
		if sinceCursor == 0 {
			sinceCursor = robotEventsSinceCursor
		}
		opts := robot.AttentionOptions{
			SinceCursor:  sinceCursor,
			Session:      session,
			Timeout:      timeout,
			PollInterval: poll,
			Condition:    robotAttentionCondition,
			Profile:      robotProfile,
		}
		exitCode := robot.PrintAttention(opts)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotDigest {
		session, err := resolveOptionalRobotSessionFilter(cmd.Context(), robotAttentionSession)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-digest")
			return true
		}
		sinceCursor := robotAttentionSinceCursor
		if sinceCursor == 0 {
			sinceCursor = robotEventsSinceCursor
		}
		opts := robot.DigestOptions{
			SinceCursor: sinceCursor,
			Session:     session,
			Profile:     robotProfile,
		}
		if err := robot.PrintDigest(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotGraph {
		if err := robot.PrintGraph(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotTriage {
		if err := robot.PrintTriage(robot.TriageOptions{Limit: resolveRobotSharedLimit(cmd, "triage-limit", robotTriageLimit)}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotForecast != "" {
		if err := robot.PrintForecast(robotForecast); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSuggest {
		if err := robot.PrintSuggest(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotImpact != "" {
		if err := robot.PrintImpact(robotImpact); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSearch != "" {
		searchLimit := 0
		if cmd.Flags().Changed("limit") {
			searchLimit = cassLimit
		}
		if err := robot.PrintSearch(robotSearch, searchLimit); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotLabelAttention {
		opts := robot.LabelAttentionOptions{Limit: resolveRobotSharedLimit(cmd, "attention-limit", robotAttentionLimit)}
		if err := robot.PrintLabelAttention(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotLabelFlow {
		if err := robot.PrintLabelFlow(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotLabelHealth {
		if err := robot.PrintLabelHealth(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotFileBeads != "" {
		opts := robot.FileBeadsOptions{FilePath: robotFileBeads, Limit: resolveRobotSharedLimit(cmd, "file-beads-limit", robotFileBeadsLimit)}
		if err := robot.PrintFileBeads(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotFileHotspots {
		opts := robot.FileHotspotsOptions{Limit: resolveRobotSharedLimit(cmd, "hotspots-limit", robotHotspotsLimit)}
		if err := robot.PrintFileHotspots(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotFileRelations != "" {
		opts := robot.FileRelationsOptions{FilePath: robotFileRelations, Limit: resolveRobotSharedLimit(cmd, "relations-limit", robotRelationsLimit), Threshold: robotRelationsThreshold}
		if err := robot.PrintFileRelations(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotDashboard {
		if err := robot.PrintDashboard(jsonOutput); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotContext != "" {
		session, _, err := resolveRobotSessionProjectScope(cmd.Context(), robotContext)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-context")
			return true
		}
		// Use --lines flag for scrollback (default 20, or as specified)
		scrollbackLines := robotLines
		if !cmd.Flags().Changed("lines") {
			scrollbackLines = 1000 // Default to capturing more for context estimation
		} else if scrollbackLines <= 0 {
			scrollbackLines = 1000 // Safety fallback
		}
		if err := robot.PrintContext(session, scrollbackLines); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotEnsembleModesList {
		pagination, err := resolveRobotPagination(cmd)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Pagination values must be zero or greater", "robot-ensemble-modes")
			return true
		}
		opts := robot.EnsembleModesOptions{
			Category: strings.TrimSpace(jfpCategory),
			Tier:     strings.TrimSpace(robotEnsembleTier),
			Limit:    pagination.Limit,
			Offset:   pagination.Offset,
		}
		if err := robot.PrintEnsembleModes(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotEnsemblePresetsList {
		if err := robot.PrintEnsemblePresets(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotEnsembleSpawn != "" {
		applyRobotEnsembleConfigDefaults(cmd, cfg)
		opts := robot.EnsembleSpawnOptions{
			Session:       robotEnsembleSpawn,
			Preset:        robotEnsemblePreset,
			Modes:         robotEnsembleModes,
			Question:      robotEnsembleQuestion,
			Agents:        robotEnsembleAgents,
			Assignment:    robotEnsembleAssignment,
			AllowAdvanced: robotEnsembleAllowAdvanced,
			BudgetTotal:   robotEnsembleBudgetTotal,
			BudgetPerMode: robotEnsembleBudgetPerMode,
			NoCache:       robotEnsembleNoCache,
			ProjectDir:    robotEnsembleProject,
		}
		if err := robot.PrintEnsembleSpawn(opts, cfg); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotEnsemble != "" {
		session, err := resolveRobotOfflineCapableSession(cmd.Context(), robotEnsemble)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-ensemble")
			return true
		}
		if err := robot.PrintEnsemble(session); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotEnsembleSuggest != "" {
		if err := robot.PrintEnsembleSuggest(robotEnsembleSuggest, robotEnsembleSuggestIDOnly); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotEnsembleStop != "" {
		session, err := resolveRobotOfflineCapableSession(cmd.Context(), robotEnsembleStop)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-ensemble-stop")
			return true
		}
		opts := robot.EnsembleStopOptions{
			Force:     robotEnsembleStopForce,
			NoCollect: robotEnsembleStopNoCollect,
		}
		if err := robot.PrintEnsembleStop(session, opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotOverlay {
		session, err := resolveOptionalRobotLiveSession(cmd.Context(), robotOverlaySession)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-overlay")
			return true
		}
		opts := robot.OverlayOptions{
			Session: session,
			Cursor:  robotOverlayCursor,
			NoWait:  robotOverlayNoWait,
		}
		if err := robot.PrintOverlay(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotTools {
		if err := robot.PrintTools(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotACFSStatus || robotSetup {
		if err := robot.PrintACFSStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSLBPending {
		if err := robot.PrintSLBPending(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSLBApprove != "" {
		if err := robot.PrintSLBApprove(robotSLBApprove); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSLBDeny != "" {
		if err := robot.PrintSLBDeny(robotSLBDeny, slbReason); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotRUSync {
		opts := robot.RUSyncOptions{
			DryRun: robotDryRun,
		}
		if err := robot.PrintRUSync(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotGiilFetch != "" {
		if err := robot.PrintGIILFetch(robotGiilFetch); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotMail {
		projectKey := GetProjectRoot()
		sessionName := ""
		sessionExplicit := len(args) > 0
		if len(args) > 0 {
			sessionName = args[0]
		} else if tmux.IsInstalled() {
			// Best-effort: infer a session when running inside tmux or when cwd matches
			// a project dir. Robot mode must never prompt.
			if res, err := ResolveSessionWithOptions("", cmd.OutOrStdout(), SessionResolveOptions{TreatAsJSON: true}); err == nil && res.Session != "" {
				sessionName = res.Session
			}
		}

		if sessionName != "" {
			resolvedSession, resolvedProjectKey, err := resolveAgentMailScopeWithPreference(cmd.Context(), sessionName, sessionExplicit)
			if err != nil {
				failRobotCommand(err, robot.ErrCodeSessionNotFound, "Verify the session and Agent Mail project scope", "robot-mail")
				return true
			}
			sessionName = resolvedSession
			projectKey = resolvedProjectKey
		}

		if err := robot.PrintMail(sessionName, projectKey); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotCassStatus {
		if err := robot.PrintCASSStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotCassSearch != "" {
		if err := robot.PrintCASSSearch(robotCassSearch, cassAgent, cassWorkspace, resolveRobotCASSSince(cmd), cassLimit); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotCassInsights {
		if err := robot.PrintCASSInsights(resolveRobotCASSSince(cmd)); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotCassContext != "" {
		if err := robot.PrintCASSContext(robotCassContext); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	// JFP (JeffreysPrompts) robot handlers
	if robotJFPStatus {
		if err := robot.PrintJFPStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPList {
		if err := robot.PrintJFPList(jfpCategory, jfpTag); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPSearch != "" {
		if err := robot.PrintJFPSearch(robotJFPSearch); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPShow != "" {
		if err := robot.PrintJFPShow(robotJFPShow); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPSuggest != "" {
		if err := robot.PrintJFPSuggest(robotJFPSuggest); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPExport != "" {
		if err := robot.PrintJFPExport(robotJFPExport, jfpFormat); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPUpdate {
		if err := robot.PrintJFPUpdate(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPCategories {
		if err := robot.PrintJFPCategories(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPTags {
		if err := robot.PrintJFPTags(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotJFPBundles {
		if err := robot.PrintJFPBundles(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	// MS (Meta Skill) robot handlers
	if robotMSSearch != "" {
		if err := robot.PrintMSSearch(robotMSSearch); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotMSShow != "" {
		if err := robot.PrintMSShow(robotMSShow); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	// XF (X Find) robot handlers
	if robotXFSearch != "" {
		opts := robot.XFSearchOptions{
			Query: robotXFSearch,
			Limit: xfLimit,
			Mode:  xfMode,
			Sort:  xfSort,
		}
		if err := robot.PrintXFSearch(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotXFStatus {
		if err := robot.PrintXFStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotDefaultPrompts {
		if err := printDefaultPrompts(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotProfileList {
		if err := printSessionProfileList(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotProfileShow != "" {
		if err := printSessionProfileShow(robotProfileShow); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotTokens {
		session, err := resolveOptionalRobotSessionFilter(cmd.Context(), resolveRobotTokensSession(cmd))
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-tokens")
			return true
		}
		opts := robot.TokensOptions{
			Days:      robotTokensDays,
			Since:     resolveRobotTokensSince(cmd),
			GroupBy:   robotTokensGroupBy,
			Session:   session,
			AgentType: resolveRobotSharedFlag(cmd, "tokens-agent", robotTokensAgent, "agent", cassAgent),
		}
		if err := robot.PrintTokens(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotHistory != "" {
		session, err := resolveRobotSessionFilter(cmd.Context(), robotHistory)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-history")
			return true
		}
		pagination, err := resolveRobotPagination(cmd)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Pagination values must be zero or greater", "robot-history")
			return true
		}
		opts := robot.HistoryOptions{
			Session:   session,
			Pane:      robotHistoryPane,
			AgentType: resolveRobotHistoryType(cmd),
			Last:      robotHistoryLast,
			Since:     resolveRobotHistorySince(cmd),
			Stats:     robotHistoryStats,
			Limit:     pagination.Limit,
			Offset:    pagination.Offset,
		}
		if err := robot.PrintHistory(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotCausality != "" {
		session, err := resolveRobotOfflineCapableSession(cmd.Context(), robotCausality)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-causality")
			return true
		}
		projectDir := strings.TrimSpace(robotCausalityProject)
		if projectDir == "" {
			if resolvedProject, resolveErr := resolveExplicitProjectDirForSessionContext(cmd.Context(), session); resolveErr == nil {
				projectDir = strings.TrimSpace(resolvedProject)
			}
		}
		if projectDir == "" {
			if cwd, cwdErr := os.Getwd(); cwdErr == nil {
				projectDir = cwd
			}
		}
		opts := robot.CausalityOptions{
			Session:   session,
			Project:   projectDir,
			AgentName: strings.TrimSpace(robotCausalityAgent),
			BeadID:    strings.TrimSpace(robotCausalityBead),
			Pane:      strings.TrimSpace(robotCausalityPane),
			Type:      strings.TrimSpace(robotCausalityType),
			Chain:     strings.TrimSpace(robotCausalityChain),
			Since:     strings.TrimSpace(robotCausalitySince),
			Until:     strings.TrimSpace(robotCausalityUntil),
			Limit:     robotCausalityLimit,
		}
		if err := robot.PrintCausality(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotActivity != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotActivity)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-activity")
			return true
		}
		paneFilter, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-activity")
			return true
		}
		// Parse agent types
		var agentTypes []string
		if activityType := resolveRobotSharedFlag(cmd, "activity-type", robotActivityType, "type", robotSendType); activityType != "" {
			agentTypes = strings.Split(activityType, ",")
		}
		opts := robot.ActivityOptions{
			Session:    session,
			Panes:      paneFilter,
			AgentTypes: agentTypes,
		}
		if err := robot.PrintActivity(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotProductivity != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotProductivity)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-productivity")
			return true
		}
		window, err := time.ParseDuration(robotProductivityWindow)
		if err != nil || window <= 0 {
			if err == nil {
				err = fmt.Errorf("must be greater than zero")
			}
			failRobotCommand(fmt.Errorf("invalid productivity window %q: %w", robotProductivityWindow, err), robot.ErrCodeInvalidFlag, "Use a positive Go duration such as 30m or 1h", "robot-productivity")
			return true
		}
		exitCode := robot.PrintProductivity(robot.ProductivityOptions{Session: session, Window: window})
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotWaitCancel != "" {
		exitCode := robot.PrintWaitCancel(robotWaitCancel)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotWait != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotWait)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-wait")
			return true
		}
		// Parse timeout and poll interval
		timeout, err := time.ParseDuration(robotWaitTimeout)
		if err != nil {
			err = fmt.Errorf("invalid timeout %q: %w", robotWaitTimeout, err)
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a Go duration such as 5m or 30s", "robot-wait")
			return true
		}
		poll, err := time.ParseDuration(robotWaitPoll)
		if err != nil {
			err = fmt.Errorf("invalid poll interval %q: %w", robotWaitPoll, err)
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a Go duration such as 1s or 500ms", "robot-wait")
			return true
		}
		productivityWindow, err := time.ParseDuration(robotProductivityWindow)
		if err != nil || productivityWindow <= 0 {
			if err == nil {
				err = fmt.Errorf("must be greater than zero")
			}
			failRobotCommand(fmt.Errorf("invalid productivity window %q: %w", robotProductivityWindow, err), robot.ErrCodeInvalidFlag, "Use a positive Go duration such as 30m or 1h", "robot-wait")
			return true
		}
		if robotConvergedStreak <= 0 {
			failRobotCommand(fmt.Errorf("invalid converged streak %d", robotConvergedStreak), robot.ErrCodeInvalidFlag, "Use a positive --converged-streak such as 3", "robot-wait")
			return true
		}
		// Parse pane selectors; resolution happens against the live topology.
		waitPanes := resolveRobotWaitPanes(cmd)
		paneSelectors, err := robot.ParsePaneSelectorsArg(waitPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-wait")
			return true
		}
		sinceCursorSet := cmd.Flags().Changed("attention-cursor")
		sinceCursor := robotAttentionSinceCursor
		if !sinceCursorSet && sinceCursor == 0 {
			sinceCursor = robotEventsSinceCursor
			sinceCursorSet = cmd.Flags().Changed("since-cursor")
		}
		opts := robot.WaitOptions{
			Session:            session,
			Condition:          robotWaitUntil,
			Timeout:            timeout,
			PollInterval:       poll,
			PaneSelectors:      paneSelectors,
			AgentType:          resolveRobotWaitType(cmd),
			WaitForAny:         robotWaitAny,
			ExitOnError:        robotWaitOnError,
			RequireTransition:  robotWaitTransition,
			SinceCursor:        sinceCursor,
			SinceCursorSet:     sinceCursorSet,
			Profile:            robotProfile,
			WaitID:             robotWaitID,
			ProductivityWindow: productivityWindow,
			ConvergedStreak:    robotConvergedStreak,
		}
		exitCode := robot.PrintWaitContext(cmd.Context(), opts)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotRoute != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotRoute)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-route")
			return true
		}
		// Parse exclude panes
		excludePanes, err := robot.ParseExcludePanes(resolveRobotRouteExclude(cmd))
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated numeric pane indices", "robot-route")
			return true
		}
		// The message being routed (--msg/--msg-file) feeds reservation
		// affinity: the affinity strategy ranks by it, and the affinity
		// bonus scores with it under every strategy when enabled.
		routedMsg, err := loadRobotMessageInput(robotSendMsg, robotSendMsgFile, "--msg", "--msg-file")
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidArgs, "Provide a readable --msg or --msg-file", "robot-route")
			return true
		}
		opts := robot.RouteOptions{
			Session:      session,
			Strategy:     robot.StrategyName(resolveRobotRouteStrategy(cmd)),
			AgentType:    resolveRobotRouteType(cmd),
			ExcludePanes: excludePanes,
			Prompt:       routedMsg,
			LastAgent:    strings.TrimSpace(robotRouteLastAgent),
			Config:       loadSelectedConfigOrDefault(),
			Relax:        robotAssignRelax,
		}
		exitCode := robot.PrintRoute(opts)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotSequence != "" {
		projectDir, err := os.Getwd()
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInternalError, "Run the command from the project whose .ntm state should own the sequence", "robot-sequence")
			return true
		}
		if err := printRobotSequence(projectDir, robotSequence, robotSequenceAction, robotSequencePane, robotSequenceSteps); err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a JSON prompt array for create and provide --sequence-pane for next or advance", "robot-sequence")
		}
		return true
	}
	// Robot-pipeline commands
	if robotPipelineRun != "" {
		vars, err := pipeline.ParsePipelineVars(robotPipelineVars)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use KEY=VALUE pipeline variables", "robot-pipeline-run")
			return true
		}
		session, projectDir, err := resolveRobotSessionProjectScope(cmd.Context(), resolveRobotPipelineSession(cmd))
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Provide a valid pipeline session or project scope", "robot-pipeline-run")
			return true
		}
		// bd-9296v: --pipeline-from-state requires --pipeline-start-from
		// (mirrors the `ntm pipeline run --from-state` validation in
		// internal/cli/pipeline.go) so an operator does not silently
		// reuse prior step state without telling the run where to
		// start.
		if robotPipelineFromState != "" && robotPipelineStartFrom == "" {
			err := errors.New("--pipeline-from-state requires --pipeline-start-from")
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Add --pipeline-start-from or remove --pipeline-from-state", "robot-pipeline-run")
			return true
		}
		opts := pipeline.PipelineRunOptions{
			WorkflowFile:  robotPipelineRun,
			Session:       session,
			ProjectDir:    projectDir,
			Variables:     vars,
			DryRun:        resolveRobotPipelineDryRun(cmd),
			Background:    robotPipelineBG,
			StartFromStep: robotPipelineStartFrom,
			FromState:     robotPipelineFromState,
		}
		exitCode := pipeline.PrintPipelineRun(opts)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotPipelineStatus != "" {
		exitCode := pipeline.PrintPipelineStatus(robotPipelineStatus)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotPipelineList {
		exitCode := pipeline.PrintPipelineList()
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotPipelineCancel != "" {
		exitCode := pipeline.PrintPipelineCancel(robotPipelineCancel)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotTail != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotTail)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-tail")
			return true
		}
		paneFilter, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-tail")
			return true
		}
		if err := robot.PrintTail(robot.TailOptions{
			Session:    session,
			Lines:      robotLines,
			PaneFilter: paneFilter,
			Fresh:      robotTailFresh,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotWatchBead != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotWatchBead)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-watch-bead")
			return true
		}
		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-watch-bead")
			return true
		}

		interval := 30 * time.Second
		if strings.TrimSpace(robotMonitorInterval) != "" {
			interval, err = util.ParseDurationWithDefault(robotMonitorInterval, time.Millisecond, "interval")
			if err != nil {
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 30s or 500ms", "robot-watch-bead")
				return true
			}
		}

		lines := robotLines
		if !cmd.Flags().Changed("lines") {
			lines = 200
		}

		opts := robot.WatchBeadOptions{
			Session:       session,
			BeadID:        robotWatchBeadID,
			PaneSelectors: paneSelectors,
			Lines:         lines,
			Interval:      interval,
		}
		if err := robot.PrintWatchBead(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotErrors != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotErrors)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-errors")
			return true
		}
		paneFilter, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-errors")
			return true
		}
		// Parse agent type filter
		agentType := ""
		if robotSendType != "" {
			agentType = robotSendType
		}
		lines := robotLines
		if !cmd.Flags().Changed("lines") {
			lines = 1000 // Default to 1000 lines for error scanning
		}
		opts := robot.ErrorsOptions{
			Session:   session,
			Panes:     paneFilter,
			Lines:     lines,
			AgentType: agentType,
		}
		if err := robot.PrintErrors(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotIsWorking != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotIsWorking)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-is-working")
			return true
		}
		// Parse pane filter
		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-is-working")
			return true
		}
		opts := robot.IsWorkingOptions{
			Session:       session,
			PaneSelectors: paneSelectors,
			LinesCaptured: robotIsWorkingLines(cmd),
			Verbose:       robotIsWorkingVerbose,
			Semantic:      robotSemantic,
		}
		if robotSemantic {
			win, werr := resolveSemanticWindow(robotSemanticWindow)
			if werr != nil {
				failRobotCommand(werr, robot.ErrCodeInvalidFlag, "Use a valid semantic sampling window", "robot-is-working")
				return true
			}
			opts.SemanticWindow = win
		}
		if err := robot.PrintIsWorking(cmd.Context(), opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotAgentHealth != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotAgentHealth)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-agent-health")
			return true
		}
		// Parse pane filter
		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-agent-health")
			return true
		}
		opts := robot.AgentHealthOptions{
			Session:       session,
			PaneSelectors: paneSelectors,
			LinesCaptured: robotAgentHealthLines(cmd),
			IncludeCaut:   !robotAgentHealthNoCaut,
			Verbose:       resolveRobotAgentHealthVerbose(cmd),
		}
		// PrintAgentHealth emits the JSON envelope and returns the process
		// exit code; a success:false result must exit nonzero (ntm#207).
		recordLegacyRobotExit(robot.PrintAgentHealth(opts))
		return true
	}
	if robotSmartRestart != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotSmartRestart)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-smart-restart")
			return true
		}
		// Parse pane filter
		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-smart-restart")
			return true
		}
		opts := robot.SmartRestartOptions{
			Session:       session,
			PaneSelectors: paneSelectors,
			Force:         robotSmartRestartForce,
			DryRun:        resolveRobotSmartRestartDryRun(cmd),
			Prompt:        robotSmartRestartPrompt,
			LinesCaptured: robotSmartRestartLines(cmd),
			Verbose:       resolveRobotSmartRestartVerbose(cmd),
			HardKill:      robotSmartRestartHardKill,
			HardKillOnly:  robotSmartRestartHardKillOnly,
		}
		if err := robot.PrintSmartRestart(cmd.Context(), opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotMonitor != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotMonitor)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-monitor")
			return true
		}
		// Parse pane filter
		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-monitor")
			return true
		}
		// Parse interval
		interval, err := robot.ParseIntervalArg(robotMonitorInterval)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 30s or 500ms", "robot-monitor")
			return true
		}
		// Parse thresholds
		warnThresh, err := robot.ParseThresholdArg(robotMonitorWarn, 25.0)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a numeric percentage", "robot-monitor")
			return true
		}
		critThresh, err := robot.ParseThresholdArg(robotMonitorCrit, 15.0)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a numeric percentage", "robot-monitor")
			return true
		}
		infoThresh, err := robot.ParseThresholdArg(robotMonitorInfo, 40.0)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a numeric percentage", "robot-monitor")
			return true
		}
		alertThresh, err := robot.ParseThresholdArg(robotMonitorAlert, 80.0)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a numeric percentage", "robot-monitor")
			return true
		}
		config := robot.MonitorConfig{
			Session:        session,
			PaneSelectors:  paneSelectors,
			Interval:       interval,
			InfoThreshold:  infoThresh,
			WarnThreshold:  warnThresh,
			CritThreshold:  critThresh,
			AlertThreshold: alertThresh,
			IncludeCaut:    robotMonitorIncludeCaut,
			CautInterval:   robot.DefaultMonitorConfig().CautInterval,
			OutputFile:     robotMonitorOutput,
			LinesCaptured:  robotMonitorLines(cmd),
		}
		if err := robot.PrintMonitor(config); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSupportBundle != "" || cmd.Flags().Changed("robot-support-bundle") {
		session, err := resolveOptionalRobotLiveSession(cmd.Context(), robotSupportBundle)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-support-bundle")
			return true
		}
		opts := robot.SupportBundleOptions{
			Session:      session,
			OutputPath:   robotSupportBundleOutput,
			Format:       robotSupportBundleFormat,
			Since:        robotSupportBundleSince,
			Lines:        robotSupportBundleLines,
			MaxSizeMB:    robotSupportBundleMax,
			RedactMode:   robotSupportBundleRedact,
			AllSessions:  robotSendAll, // Reuse --all flag
			AllowPersist: allowSecret,  // Reuse --allow-secret for persist override
			NTMVersion:   Version,
		}
		if err := robot.PrintSupportBundle(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	return false
}

func resolveRobotPagination(cmd *cobra.Command) (robot.PaginationOptions, error) {
	opts := robot.PaginationOptions{}

	if cmd.Flags().Changed("robot-limit") {
		opts.Limit = robotLimit
	} else if cmd.Flags().Changed("limit") {
		opts.Limit = cassLimit
	}

	if cmd.Flags().Changed("robot-offset") || cmd.Flags().Changed("offset") {
		opts.Offset = robotOffset
	}

	if opts.Limit < 0 || opts.Offset < 0 {
		return opts, fmt.Errorf("pagination values must be >= 0")
	}

	return opts, nil
}

func resolveRobotLiveSession(ctx context.Context, sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", fmt.Errorf("session is required")
	}
	return normalizeExplicitLiveSessionName(ctx, sessionName, !IsJSONOutput())
}

func resolveRobotOfflineCapableSession(ctx context.Context, sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", fmt.Errorf("session is required")
	}
	return normalizeProjectScopedSessionName(ctx, sessionName, !IsJSONOutput())
}

func resolveOptionalRobotLiveSession(ctx context.Context, sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", nil
	}
	return resolveRobotLiveSession(ctx, sessionName)
}

func resolveRobotSessionFilter(ctx context.Context, sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", fmt.Errorf("session is required")
	}
	return resolveOptionalRobotSessionFilter(ctx, sessionName)
}

func resolveOptionalRobotSessionFilter(ctx context.Context, sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", nil
	}
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}

	resolved, err := normalizeExplicitLiveSessionName(ctx, sessionName, !IsJSONOutput())
	if err == nil {
		return resolved, nil
	}

	var resolveErr *session.ResolveExplicitSessionNameError
	if errors.As(err, &resolveErr) && resolveErr.Kind == session.ResolveExplicitSessionNameErrorAmbiguous {
		return "", err
	}
	return sessionName, nil
}

func resolveRobotSessionProjectScope(ctx context.Context, sessionName string) (string, string, error) {
	resolved, err := resolveRobotLiveSession(ctx, sessionName)
	if err != nil {
		return "", "", err
	}

	projectDir, err := resolveExplicitProjectDirForSessionContext(ctx, resolved)
	if err != nil {
		return "", "", err
	}

	return resolved, projectDir, nil
}

// DefaultPromptsOutput is the robot response for --robot-default-prompts.
type DefaultPromptsOutput struct {
	Success    bool   `json:"success"`
	CCDefault  string `json:"cc_default"`
	CodDefault string `json:"cod_default"`
	GmiDefault string `json:"gmi_default"`
	AgyDefault string `json:"agy_default"`
}

// printDefaultPrompts outputs per-agent-type default prompts as JSON (bd-2ywo).
func printDefaultPrompts() error {
	r := DefaultPromptsOutput{Success: true}
	if cfg != nil {
		p := cfg.Prompts
		if v, err := p.ResolveForType("cc"); err == nil {
			r.CCDefault = v
		}
		if v, err := p.ResolveForType("cod"); err == nil {
			r.CodDefault = v
		}
		if v, err := p.ResolveForType("gmi"); err == nil {
			r.GmiDefault = v
		}
		if v, err := p.ResolveForType("agy"); err == nil {
			r.AgyDefault = v
		}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// resolveRobotFormat determines the robot output format from CLI flag, env var, config, or default.
// Priority: --robot-format flag > NTM_ROBOT_FORMAT > NTM_OUTPUT_FORMAT > TOON_DEFAULT_FORMAT > config > auto
func resolveRobotFormat(cfg *config.Config) error {
	formatStr := robotFormat

	// Fall back to environment variable if flag not set
	if formatStr == "" {
		formatStr = os.Getenv("NTM_ROBOT_FORMAT")
	}
	if formatStr == "" {
		formatStr = os.Getenv("NTM_OUTPUT_FORMAT")
	}
	if formatStr == "" {
		formatStr = os.Getenv("TOON_DEFAULT_FORMAT")
	}

	// Fall back to config if available
	if formatStr == "" && cfg != nil {
		formatStr = cfg.Robot.Output.Format
	}

	// Parse and set the format
	if formatStr != "" {
		format, err := robot.ParseRobotFormat(formatStr)
		if err != nil {
			robot.SetOutputFormat(robot.FormatAuto)
			return err
		}
		robot.SetOutputFormat(format)
	} else {
		robot.SetOutputFormat(robot.FormatAuto)
	}
	return nil
}

func robotLinesOrDefault(cmd *cobra.Command, fallback int) int {
	if cmd == nil {
		return fallback
	}
	if !cmd.Flags().Changed("lines") {
		return fallback
	}
	return robotLines
}

func robotIsWorkingLines(cmd *cobra.Command) int {
	return robotLinesOrDefault(cmd, robot.DefaultIsWorkingOptions().LinesCaptured)
}

// resolveSemanticWindow resolves the --semantic look-back window (#199).
// Priority: --semantic-window flag (Go duration) > [robot.semantic].window_minutes
// config > conservative 30m default. A non-positive resolved window falls back
// to the default so the reader never receives a zero/negative window.
func resolveSemanticWindow(flagVal string) (time.Duration, error) {
	if strings.TrimSpace(flagVal) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(flagVal))
		if err != nil {
			return 0, err
		}
		if d <= 0 {
			return 0, fmt.Errorf("must be positive, got %s", flagVal)
		}
		return d, nil
	}
	if cfg != nil && cfg.Robot.Semantic.WindowMinutes > 0 {
		return time.Duration(cfg.Robot.Semantic.WindowMinutes) * time.Minute, nil
	}
	return 30 * time.Minute, nil
}

func robotAgentHealthLines(cmd *cobra.Command) int {
	return robotLinesOrDefault(cmd, robot.DefaultAgentHealthOptions().LinesCaptured)
}

func robotSmartRestartLines(cmd *cobra.Command) int {
	return robotLinesOrDefault(cmd, robot.DefaultSmartRestartOptions().LinesCaptured)
}

func robotMonitorLines(cmd *cobra.Command) int {
	return robotLinesOrDefault(cmd, robot.DefaultMonitorConfig().LinesCaptured)
}

// resolveRobotVerbosity determines the robot verbosity profile from CLI flag, env var, or config.
// Priority: --robot-verbosity flag > NTM_ROBOT_VERBOSITY env var > config.robot.verbosity > default
func resolveRobotVerbosity(cfg *config.Config) error {
	verbosityStr := robotVerbosity

	// Fall back to environment variable if flag not set
	if verbosityStr == "" {
		verbosityStr = os.Getenv("NTM_ROBOT_VERBOSITY")
	}

	// Fall back to config if available
	if verbosityStr == "" && cfg != nil {
		verbosityStr = cfg.Robot.Verbosity
	}

	if verbosityStr == "" {
		robot.SetOutputVerbosity(robot.VerbosityDefault)
		return nil
	}

	verbosity, err := robot.ParseRobotVerbosity(verbosityStr)
	if err != nil {
		robot.SetOutputVerbosity(robot.VerbosityDefault)
		return err
	}
	robot.SetOutputVerbosity(verbosity)
	return nil
}

func applyRobotEnsembleConfigDefaults(cmd *cobra.Command, cfg *config.Config) {
	if cmd == nil {
		return
	}
	if cfg == nil {
		cfg = config.Default()
	}

	ensCfg := cfg.Ensemble
	flags := cmd.Flags()

	if robotEnsemblePreset == "" && strings.TrimSpace(robotEnsembleModes) == "" && strings.TrimSpace(ensCfg.DefaultEnsemble) != "" {
		robotEnsemblePreset = ensCfg.DefaultEnsemble
	}
	if !flags.Changed("agents") && strings.TrimSpace(robotEnsembleAgents) == "" && strings.TrimSpace(ensCfg.AgentMix) != "" {
		robotEnsembleAgents = ensCfg.AgentMix
	}
	if !flags.Changed("assignment") && strings.TrimSpace(ensCfg.Assignment) != "" {
		robotEnsembleAssignment = ensCfg.Assignment
	}
	if !flags.Changed("allow-advanced") {
		allow := ensCfg.AllowAdvanced
		switch strings.ToLower(strings.TrimSpace(ensCfg.ModeTierDefault)) {
		case "advanced", "experimental":
			allow = true
		}
		robotEnsembleAllowAdvanced = allow
	}
	if !flags.Changed("budget-total") && robotEnsembleBudgetTotal == 0 && ensCfg.Budget.Total > 0 {
		robotEnsembleBudgetTotal = ensCfg.Budget.Total
	}
	if !flags.Changed("budget-per-agent") && robotEnsembleBudgetPerMode == 0 && ensCfg.Budget.PerAgent > 0 {
		robotEnsembleBudgetPerMode = ensCfg.Budget.PerAgent
	}
	if !flags.Changed("no-cache") && !ensCfg.Cache.Enabled {
		robotEnsembleNoCache = true
	}
}
