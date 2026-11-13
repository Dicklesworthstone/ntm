package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/startup"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func initRootFlagsAndSubcommands() {
	robot.MustRegisterSchemaCommand("default_prompts", DefaultPromptsOutput{})
	jsonArgumentRoot = rootCmd

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/ntm/config.toml)")

	// Pre-register cobra's built-in version flag so it gets the -V shorthand
	// (fleet probes reach for `ntm --version`/`-V` before discovering the
	// `version` subcommand; -v is left free for any future verbose flag).
	// Output is pinned to the `ntm version` headline via the template below.
	rootCmd.Flags().BoolP("version", "V", false, "Print version information (same as 'ntm version')")
	rootCmd.SetVersionTemplate("ntm version {{.Version}}\n")

	// Subcommand flag errors get a did-you-mean over the failing command's
	// own flags, plus curated hints for the fleet's top mis-guessed flags
	// (--session, --pane, --project-dir, --timeout, --message). Root flag
	// errors keep their existing robot-path classification, which already
	// runs the same suggester.
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		if err == nil || cmd == nil || cmd == rootCmd {
			return err
		}
		unknown := unknownFlagFromError(err.Error())
		if unknown == "" {
			return err
		}
		// Curated hints win over the edit-distance suggester: for the known
		// mis-guesses the nearest registered flag is usually a red herring
		// (--session's nearest match on 'ntm send' is --json). But a real
		// flag one edit away wins over a fuzzy curated match, so commands
		// that genuinely have --session still get "did you mean --session?"
		// for --sesion (see flagErrorGuidance).
		curated, suggestion := flagErrorGuidance(unknown, cmd.Flags(), cmd.InheritedFlags())
		if curated != "" {
			return fmt.Errorf("%w\nHint: %s", err, curated)
		}
		if suggestion != "" {
			return fmt.Errorf("%w (did you mean --%s?)", err, suggestion)
		}
		return err
	})

	// Global JSON output flag - applies to all commands
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (machine-readable)")
	rootCmd.PersistentFlags().StringVar(&sshHost, "ssh", "", "Remote host for SSH execution (e.g. user@host)")

	// Global no-color flag - disables colored output (respects NO_COLOR env var standard)
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable colored output")

	// Global redaction flags - secrets/PII redaction control
	rootCmd.PersistentFlags().StringVar(&redactMode, "redact", "", "Redaction mode override: off, warn, redact, block")
	rootCmd.PersistentFlags().BoolVar(&allowSecret, "allow-secret", false, "Bypass 'block' mode for this invocation (use with caution)")

	// Profiling flag for startup timing analysis
	rootCmd.PersistentFlags().BoolVar(&profileStartup, "profile-startup", false, "Enable startup profiling (outputs timing data)")

	registerRobotFlags()

	// Sync version info with robot package
	robot.Version = Version
	robot.Commit = Commit
	robot.Date = Date
	robot.BuiltBy = BuiltBy

	// Add all subcommands
	rootCmd.AddCommand(
		// Session creation
		newCreateCmd(),
		newSpawnCmd(),
		newQuickCmd(),
		newAdoptCmd(),
		newSwarmCmd(),

		// Agent management
		newAddCmd(),
		newSendCmd(),
		newPreflightCmd(),
		newReplayCmd(),
		newInterruptCmd(),
		newRotateCmd(),
		newQuotaCmd(),
		newPipelineCmd(),
		newWaitCmd(),
		newMailCmd(),
		newPluginsCmd(),
		newAgentsCmd(),
		newModelsCmd(),
		newAssignCmd(),
		newRebalanceCmd(),
		newReviewQueueCmd(),
		newScaleCmd(),
		newControllerCmd(),
		newCodexCmd(),

		// Session navigation
		newAttachCmd(),
		newListCmd(),
		newStatusCmd(),
		newViewCmd(),
		newZoomCmd(),
		newDashboardCmd(),
		newWatchCmd(),
		newGetAllSessionTextCmd(),

		// Output management
		newCopyCmd(),
		newSaveCmd(),
		newGrepCmd(),
		newSearchCmd(),
		newErrorsCmd(),
		newExtractCmd(),
		newDiffCmd(),
		newChangesCmd(),
		newConflictsCmd(),
		newSummaryCmd(),
		newLogsCmd(),

		// Session persistence
		newCheckpointCmd(),
		newRollbackCmd(),
		newSessionPersistCmd(),
		newHandoffCmd(),
		newResumeCmd(),
		newTimelineCmd(),

		// Utilities
		newOverlayCmd(),
		newPaletteCmd(),
		newBindCmd(),
		newDepsCmd(),
		newKillCmd(),
		newRespawnCmd(),
		newScanCmd(),
		newScrubCmd(),
		newRedactCmd(),
		newBugsCmd(),
		newCassCmd(),
		newAuditCmd(),
		newHooksCmd(),
		newHealthCmd(),
		newDoctorCmd(),
		newCleanupCmd(),
		newClaimCmd(),
		newSupportBundleCmd(),
		newSafetyCmd(),
		newPolicyCmd(),
		newKernelCmd(),
		newOpenAPICmd(),
		newGuardsCmd(),
		newApproveCmd(),
		newServeCmd(),
		newWebCmd(),
		newSetupCmd(),
		newActivityCmd(),
		newHistoryCmd(),
		newAnalyticsCmd(),
		newMetricsCmd(),
		newWorkCmd(),
		newEnsembleCmd(),
		newModesCmd(),

		// Agent-name navigation (ntm#139)
		newSwitchAgentCmd(),
		newMappingCmd(),

		// Internal commands
		newMonitorCmd(),

		// Memory integration
		newMemoryCmd(),

		// Context pack building
		newContextCmd(),

		// Beads daemon management
		newBeadsCmd(),

		// Project initialization + shell integration
		newInitCmd(),
		newShellCmd(),
		newCompletionCmd(),
		newVersionCmd(),
		newConfigCmd(),
		newUpgradeCmd(),
		newLevelCmd(),

		// Tutorial
		newTutorialCmd(),

		// Agent Mail & File Reservations
		newLockCmd(),
		newUnlockCmd(),
		newLocksCmd(),
		newMessageCmd(),     // Unified messaging
		newCoordinatorCmd(), // Multi-agent coordination

		// Git coordination
		newGitCmd(),
		newRepoCmd(),
		newWorktreesCmd(),

		// Configuration management
		newRecipesCmd(),
		newWorkflowsCmd(),
		newPersonasCmd(),
		newTemplateCmd(),
		newSessionTemplatesCmd(),
		newSessionProfileCmd(), // bd-29kr: session profiles
	)

	// Load command plugins
	cmdDir := pluginConfigDirForArgs(os.Args[1:])
	cmds, _ := plugins.LoadCommandPlugins(cmdDir)

	for _, p := range cmds {
		plugin := p // Capture for closure
		cmd := &cobra.Command{
			Use:                plugin.Name,
			Short:              plugin.Description,
			Long:               plugin.Description + "\n\nUsage: " + plugin.Usage,
			DisableFlagParsing: true,
			RunE: func(c *cobra.Command, args []string) error {
				// Prepare env
				env := map[string]string{
					"NTM_CONFIG_PATH": selectedConfigPath(),
					"NTM_VERSION":     Version,
				}
				if jsonOutput {
					env["NTM_JSON"] = "1"
				}
				if s := tmux.GetCurrentSession(); s != "" {
					env["NTM_SESSION"] = s
				}

				return plugin.Execute(args, env)
			},
		}
		rootCmd.AddCommand(cmd)
	}
	registerConfiguredRobotPluginSpawnFlags()
}

func newVersionCmd() *cobra.Command {
	var short bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVersion(short)
		},
	}
	cmd.Flags().BoolVarP(&short, "short", "s", false, "Print only version number")
	return cmd
}

func registerCoreVersionKernel() {
	kernel.MustRegister(kernel.Command{
		Name:        "core.version",
		Description: "Return NTM version and build info",
		Category:    "core",
		Input: &kernel.SchemaRef{
			Name: "VersionInput",
			Ref:  "cli.VersionInput",
		},
		Output: &kernel.SchemaRef{
			Name: "VersionResponse",
			Ref:  "output.VersionResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/version",
		},
		Examples: []kernel.Example{
			{
				Name:        "version",
				Description: "Show version info",
				Command:     "ntm version",
			},
			{
				Name:        "version-short",
				Description: "Show only the version number",
				Command:     "ntm version --short",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("core.version", func(ctx context.Context, _ any) (any, error) {
		return buildVersionResponse(), nil
	})
}

func runVersion(short bool) error {
	result, err := kernel.Run(context.Background(), "core.version", VersionInput{Short: short})
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
		}
		return err
	}

	resp, err := coerceVersionResponse(result)
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
		}
		return err
	}

	if IsJSONOutput() {
		return output.PrintJSON(resp)
	}

	if short {
		fmt.Println(resp.Version)
		return nil
	}
	fmt.Printf("ntm version %s\n", resp.Version)
	fmt.Printf("  commit:    %s\n", resp.Commit)
	fmt.Printf("  built:     %s\n", resp.BuiltAt)
	fmt.Printf("  builder:   %s\n", resp.BuiltBy)
	fmt.Printf("  go:        %s\n", resp.GoVersion)
	fmt.Printf("  platform:  %s\n", resp.Platform)
	return nil
}

func coerceVersionResponse(result any) (output.VersionResponse, error) {
	switch value := result.(type) {
	case output.VersionResponse:
		return value, nil
	case *output.VersionResponse:
		if value != nil {
			return *value, nil
		}
		return output.VersionResponse{}, fmt.Errorf("core.version returned nil response")
	default:
		return output.VersionResponse{}, fmt.Errorf("core.version returned unexpected type %T", result)
	}
}

func buildVersionResponse() output.VersionResponse {
	return output.VersionResponse{
		TimestampedResponse: output.NewTimestamped(),
		Version:             Version,
		Commit:              Commit,
		BuiltAt:             Date,
		BuiltBy:             BuiltBy,
		GoVersion:           goVersion(),
		Platform:            goPlatform(),
	}
}

func selectedConfigPath() string {
	if strings.TrimSpace(cfgFile) != "" {
		return cfgFile
	}
	return configPathFromArgs(os.Args[1:])
}

func selectedConfigIsExplicit() bool {
	return strings.TrimSpace(cfgFile) != "" || strings.TrimSpace(os.Getenv("NTM_CONFIG")) != ""
}

func configureStartupConfigPath() {
	if selectedConfigIsExplicit() {
		startup.SetConfigPath(selectedConfigPath())
		return
	}
	startup.SetConfigPath("")
}

func selectedConfigAllowsProjectionRefresh() bool {
	if !selectedConfigIsExplicit() {
		return true
	}
	info, err := os.Stat(selectedConfigPath())
	return err == nil && info.Mode().IsRegular()
}

func robotAssignmentCommandRequiresPolicyPreflight() bool {
	if strings.TrimSpace(robotAssign) != "" || strings.TrimSpace(robotBulkAssign) != "" {
		return true
	}
	if strings.TrimSpace(robotSpawn) != "" && robotSpawnAssignWork {
		return true
	}
	return strings.TrimSpace(robotRestartPane) != "" && strings.TrimSpace(robotRestartPaneBead) != ""
}

func selectedConfigDir() string {
	return filepath.Dir(selectedConfigPath())
}

func configPathFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--config=") {
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
			if value != "" {
				return value
			}
			break
		}
		if arg == "--config" {
			if i+1 < len(args) {
				value := strings.TrimSpace(args[i+1])
				if value != "" {
					return value
				}
			}
			break
		}
	}
	return config.DefaultPath()
}

func pluginConfigDirForArgs(args []string) string {
	return filepath.Join(filepath.Dir(configPathFromArgs(args)), "commands")
}

func pluginAgentsDirForArgs(args []string) string {
	return filepath.Join(filepath.Dir(configPathFromArgs(args)), "agents")
}

// recordInteractiveUsage feeds `ntm level`: each successful interactive
// command counts once, and session-creating and prompt-sending commands also
// count as such. Cobra skips post-run hooks for failed commands. Machine
// (robot/JSON) invocations are automation, not the operator's proficiency;
// hidden commands (shell completion's __complete runs on every TAB, plus
// internal ones like internal-monitor) are not usage either; and test
// binaries must not write the developer's stats.
func recordInteractiveUsage(cmd *cobra.Command) {
	if !isInteractiveUsage(cmd) || flag.Lookup("test.v") != nil {
		return
	}
	recordProficiencyUsage(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" "))
}

// isInteractiveUsage reports whether cmd is a user-facing subcommand.
func isInteractiveUsage(cmd *cobra.Command) bool {
	return cmd != nil && cmd.HasParent() && !cmd.Hidden && !strings.HasPrefix(cmd.Name(), "__")
}

func recordProficiencyUsage(command string) {
	prof, err := config.LoadProficiency()
	if err != nil || prof == nil {
		return
	}
	_ = prof.RecordCommand(command)
	switch command {
	case "spawn", "create", "quick":
		_ = prof.IncrementSession()
	case "send":
		_ = prof.IncrementPrompt()
	}
}

// tmuxSessionPrivacy reads the privacy settings `ntm spawn --privacy` /
// `--allow-persist` recorded on a session, for the privacy manager.
func tmuxSessionPrivacy(session string) (*privacy.SessionState, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	privacyMode, allowPersist, recorded, err := tmux.SessionPrivacyContext(ctx, session)
	if err != nil || !recorded {
		return nil, false
	}
	return &privacy.SessionState{PrivacyMode: privacyMode, AllowPersist: allowPersist}, true
}

func loadSelectedConfigOrDefault() *config.Config {
	if cfg != nil {
		return cfg
	}
	cwd, _ := os.Getwd()
	loaded, err := config.LoadMerged(cwd, selectedConfigPath())
	if err != nil {
		loaded = config.Default()
		bv.ConfigureOperatorGatedLabels(loaded.Assign.OperatorGatedLabels)
		bv.ConfigureWorkSourcePolicy(loaded.Assign.WorkSource.Policy())
		bv.ConfigureCommandTimeout(loaded.Integrations.BV.TimeoutSeconds)
		return loaded
	}
	// Callers on this path bypassed the PersistentPreRun config application,
	// so install config-driven package state here as well (#223).
	bv.ConfigureOperatorGatedLabels(loaded.Assign.OperatorGatedLabels)
	bv.ConfigureWorkSourcePolicy(loaded.Assign.WorkSource.Policy())
	bv.ConfigureCommandTimeout(loaded.Integrations.BV.TimeoutSeconds)
	return loaded
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Create default configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			auditStart := time.Now()
			_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.init", map[string]interface{}{
				"phase":          "start",
				"correlation_id": auditCorrelationID,
			}, nil)
			path, err := config.CreateDefault(selectedConfigPath())
			if err != nil {
				_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.init", map[string]interface{}{
					"phase":          "finish",
					"success":        false,
					"error":          err.Error(),
					"duration_ms":    time.Since(auditStart).Milliseconds(),
					"correlation_id": auditCorrelationID,
				}, nil)
				return err
			}
			_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.init", map[string]interface{}{
				"phase":          "finish",
				"success":        true,
				"path":           path,
				"duration_ms":    time.Since(auditStart).Milliseconds(),
				"correlation_id": auditCorrelationID,
			}, nil)
			fmt.Printf("Created config file: %s\n", path)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print configuration file path",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(selectedConfigPath())
		},
	})

	// Add 'set' subcommand for easy configuration
	setCmd := &cobra.Command{
		Use:   "set",
		Short: "Set configuration values",
	}

	setCmd.AddCommand(&cobra.Command{
		Use:   "projects-base <path>",
		Short: "Set the base directory for projects",
		Long: `Set the base directory where ntm creates project folders.

Examples:
  ntm config set projects-base ~/projects
  ntm config set projects-base /data/projects
  ntm config set projects-base ~/Developer`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			auditStart := time.Now()
			_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.set_projects_base", map[string]interface{}{
				"phase":          "start",
				"path":           path,
				"correlation_id": auditCorrelationID,
			}, nil)
			if err := config.SetProjectsBase(selectedConfigPath(), path); err != nil {
				_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.set_projects_base", map[string]interface{}{
					"phase":          "finish",
					"path":           path,
					"success":        false,
					"error":          err.Error(),
					"duration_ms":    time.Since(auditStart).Milliseconds(),
					"correlation_id": auditCorrelationID,
				}, nil)
				return err
			}
			expanded := config.ExpandHome(path)
			fmt.Printf("Projects base set to: %s\n", expanded)
			fmt.Printf("Config saved to: %s\n", selectedConfigPath())
			_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.set_projects_base", map[string]interface{}{
				"phase":          "finish",
				"path":           path,
				"expanded_path":  expanded,
				"success":        true,
				"duration_ms":    time.Since(auditStart).Milliseconds(),
				"correlation_id": auditCorrelationID,
			}, nil)
			return nil
		},
	})

	cmd.AddCommand(setCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveCfg := loadSelectedConfigOrDefault()

			if IsJSONOutput() {
				palette := make([]map[string]interface{}, 0, len(effectiveCfg.Palette))
				for _, pal := range effectiveCfg.Palette {
					palette = append(palette, map[string]interface{}{
						"key":      pal.Key,
						"label":    pal.Label,
						"prompt":   pal.Prompt,
						"category": pal.Category,
						"tags":     pal.Tags,
					})
				}

				return output.PrintJSON(map[string]interface{}{
					"projects_base": effectiveCfg.ProjectsBase,
					"theme":         effectiveCfg.Theme,
					"palette_file":  effectiveCfg.PaletteFile,
					"agents": map[string]string{
						"claude":      effectiveCfg.Agents.Claude,
						"codex":       effectiveCfg.Agents.Codex,
						"antigravity": effectiveCfg.Agents.Antigravity,
						"gemini":      effectiveCfg.Agents.Gemini,
						"grok":        effectiveCfg.Agents.Grok,
						"omp":         config.OmpCommandOrDefault(effectiveCfg.Agents.Omp),
						"cursor":      effectiveCfg.Agents.Cursor,
						"windsurf":    effectiveCfg.Agents.Windsurf,
						"aider":       effectiveCfg.Agents.Aider,
						"oc":          effectiveCfg.Agents.Opencode,
					},
					"tmux": map[string]interface{}{
						"default_panes":      effectiveCfg.Tmux.DefaultPanes,
						"pane_init_delay_ms": effectiveCfg.Tmux.PaneInitDelayMs,
					},
					"checkpoints": map[string]interface{}{
						"enabled":              effectiveCfg.Checkpoints.Enabled,
						"before_broadcast":     effectiveCfg.Checkpoints.BeforeBroadcast,
						"before_add_agents":    effectiveCfg.Checkpoints.BeforeAddAgents,
						"max_auto_checkpoints": effectiveCfg.Checkpoints.MaxAutoCheckpoints,
						"scrollback_lines":     effectiveCfg.Checkpoints.ScrollbackLines,
						"include_git":          effectiveCfg.Checkpoints.IncludeGit,
					},
					"alerts": map[string]interface{}{
						"enabled":                 effectiveCfg.Alerts.Enabled,
						"agent_stuck_minutes":     effectiveCfg.Alerts.AgentStuckMinutes,
						"disk_low_threshold_gb":   effectiveCfg.Alerts.DiskLowThresholdGB,
						"disk_full_horizon_hours": effectiveCfg.Alerts.DiskFullHorizonHours,
						"mail_backlog_threshold":  effectiveCfg.Alerts.MailBacklogThreshold,
						"bead_stale_hours":        effectiveCfg.Alerts.BeadStaleHours,
						"resolved_prune_minutes":  effectiveCfg.Alerts.ResolvedPruneMinutes,
					},
					"safety": map[string]interface{}{
						"profile": effectiveCfg.Safety.Profile,
						"preflight": map[string]interface{}{
							"strict": effectiveCfg.Preflight.Strict,
						},
						"redaction": map[string]interface{}{
							"mode": effectiveCfg.Redaction.Mode,
						},
						"privacy": map[string]interface{}{
							"enabled": effectiveCfg.Privacy.Enabled,
						},
					},
					"integrations": map[string]interface{}{
						"dcg": map[string]interface{}{
							"enabled":        effectiveCfg.Integrations.DCG.Enabled,
							"binary_path":    effectiveCfg.Integrations.DCG.BinaryPath,
							"allow_override": effectiveCfg.Integrations.DCG.AllowOverride,
						},
						"caam": map[string]interface{}{
							"auto_failover":         effectiveCfg.Integrations.CAAM.AutoFailover,
							"reset_horizon_minutes": effectiveCfg.Integrations.CAAM.ResetHorizonMinutes,
							"failover_providers":    effectiveCfg.Integrations.CAAM.FailoverProviders,
							"seat_selection":        effectiveCfg.Integrations.CAAM.SeatSelection,
						},
						"rch": map[string]interface{}{
							"enabled": effectiveCfg.Integrations.RCH.Enabled,
						},
						"process_triage": map[string]interface{}{
							"enabled": effectiveCfg.Integrations.ProcessTriage.Enabled,
						},
						"rano": map[string]interface{}{
							"enabled": effectiveCfg.Integrations.Rano.Enabled,
						},
						"xf": map[string]interface{}{
							"enabled": effectiveCfg.Integrations.XF.Enabled,
						},
					},
					"resilience": map[string]interface{}{
						"auto_restart":         effectiveCfg.Resilience.AutoRestart,
						"max_restarts":         effectiveCfg.Resilience.MaxRestarts,
						"health_check_seconds": effectiveCfg.Resilience.HealthCheckSeconds,
					},
					"assign": map[string]interface{}{
						"strategy":              effectiveCfg.Assign.Strategy,
						"prompt_template":       effectiveCfg.Assign.PromptTemplate,
						"prompt_template_file":  effectiveCfg.Assign.PromptTemplateFile,
						"operator_gated_labels": effectiveCfg.Assign.OperatorGatedLabels,
					},
					"scanner": map[string]interface{}{
						"ubs_path": effectiveCfg.Scanner.UBSPath,
					},
					"cass": map[string]interface{}{
						"enabled": effectiveCfg.CASS.Enabled,
						"timeout": effectiveCfg.CASS.Timeout,
					},
					"memory": map[string]interface{}{
						"enabled":               effectiveCfg.Memory.Enabled,
						"query_timeout_seconds": effectiveCfg.Memory.QueryTimeoutSeconds,
						"send_injection":        effectiveCfg.Memory.SendInjection,
						"send_max_rules":        effectiveCfg.Memory.SendMaxRules,
						"send_budget_tokens":    effectiveCfg.Memory.SendBudgetTokens,
					},
					"bugs": map[string]interface{}{
						"push_routing":     effectiveCfg.Bugs.PushRouting,
						"interval":         effectiveCfg.Bugs.EffectiveInterval().String(),
						"cooldown_minutes": effectiveCfg.Bugs.CooldownMinutes,
					},
					"rotation": map[string]interface{}{
						"enabled":                 effectiveCfg.Rotation.Enabled,
						"auto_trigger":            effectiveCfg.Rotation.AutoTrigger,
						"auto_initiate":           effectiveCfg.Rotation.AutoInitiate,
						"usage_percent_threshold": effectiveCfg.Rotation.UsagePercentThreshold,
						"auto_confirm":            effectiveCfg.Rotation.AutoConfirm,
					},
					"gemini_setup": map[string]interface{}{
						"auto_select_pro_model": effectiveCfg.GeminiSetup.AutoSelectProModel,
					},
					"spawn": map[string]interface{}{
						"stagger_mode":  effectiveCfg.Spawn.StaggerMode,
						"stagger_delay": effectiveCfg.Spawn.StaggerDelay.String(),
					},
					"context_rotation": map[string]interface{}{
						"rotate_threshold":  effectiveCfg.ContextRotation.RotateThreshold,
						"try_compact_first": effectiveCfg.ContextRotation.TryCompactFirst,
					},
					"agent_mail": map[string]interface{}{
						"enabled":            effectiveCfg.AgentMail.Enabled,
						"url":                effectiveCfg.AgentMail.URL,
						"auto_register":      effectiveCfg.AgentMail.AutoRegister,
						"supervisor_enabled": effectiveCfg.AgentMail.SupervisorEnabledOrDefault(),
					},
					"ensemble": map[string]interface{}{
						"default_ensemble": effectiveCfg.Ensemble.DefaultEnsemble,
						"agent_mix":        effectiveCfg.Ensemble.AgentMix,
					},
					"palette": palette,
				})
			}

			return config.Print(effectiveCfg, os.Stdout)
		},
	})

	// Add diff subcommand
	cmd.AddCommand(&cobra.Command{
		Use:   "diff",
		Short: "Show configuration differences from defaults",
		Long:  `Shows all configuration values that differ from the built-in defaults.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveCfg := loadSelectedConfigOrDefault()

			diffs := config.Diff(effectiveCfg)

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"count": len(diffs),
					"diffs": diffs,
				})
			}

			if len(diffs) == 0 {
				fmt.Println("No differences from defaults")
				return nil
			}

			fmt.Printf("Configuration differences (%d):\n\n", len(diffs))
			for _, d := range diffs {
				fmt.Printf("  %s\n", d.Path)
				fmt.Printf("    default: %v\n", d.Default)
				fmt.Printf("    current: %v\n", d.Current)
				fmt.Println()
			}
			return nil
		},
	})

	// Add validate subcommand (comprehensive validation from validate.go)
	cmd.AddCommand(newConfigValidateCmd())

	// Add migrate subcommand: surgical removal of removed/deprecated keys
	// (bd-config-migrate-warning-wall-151x2).
	cmd.AddCommand(newConfigMigrateCmd())

	// Add get subcommand
	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Long: `Retrieves a configuration value by its dotted path.

Examples:
  ntm config get projects_base
  ntm config get alerts.enabled
  ntm config get context_rotation.rotate_threshold`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveCfg := loadSelectedConfigOrDefault()

			value, err := config.GetValue(effectiveCfg, args[0])
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"key":   args[0],
					"value": value,
				})
			}

			fmt.Printf("%v\n", value)
			return nil
		},
	})

	// Add edit subcommand
	cmd.AddCommand(&cobra.Command{
		Use:   "edit",
		Short: "Open configuration file in editor",
		Long:  `Opens the configuration file in your default editor ($EDITOR or vi).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := selectedConfigPath()

			// Ensure config exists
			if _, err := os.Stat(path); os.IsNotExist(err) {
				if _, err := config.CreateDefault(path); err != nil {
					return fmt.Errorf("creating config: %w", err)
				}
			}

			editorCmd, err := buildEditorCommand(path)
			if err != nil {
				return err
			}
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = os.Stdout
			editorCmd.Stderr = os.Stderr
			return editorCmd.Run()
		},
	})

	// Add reset subcommand
	var resetConfirm bool
	resetCmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset configuration to defaults",
		Long:  `Removes the current configuration file and creates a new one with defaults.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !resetConfirm {
				return fmt.Errorf("use --confirm to reset configuration (this will delete your current config)")
			}

			if err := config.Reset(selectedConfigPath()); err != nil {
				return err
			}

			fmt.Printf("Configuration reset to defaults: %s\n", selectedConfigPath())
			return nil
		},
	}
	resetCmd.Flags().BoolVar(&resetConfirm, "confirm", false, "confirm reset operation")
	cmd.AddCommand(resetCmd)

	projectCmd := &cobra.Command{
		Use:   "project",
		Short: "Manage project-specific configuration",
	}

	var projectInitForce bool
	projectInitCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize .ntm configuration for current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.InitProjectConfig(projectInitForce); err != nil {
				return err
			}
			if cwd, cwdErr := os.Getwd(); cwdErr == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Initialized project config in %s\n", filepath.Join(cwd, ".ntm"))
			}

			projectPath, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}

			configPath := filepath.Join(projectPath, ".ntm", "config.toml")
			registered, warning, err := registerAgentMailProject(projectPath, configPath)
			if err != nil {
				return err
			}
			if warning != "" {
				output.PrintWarningf("Agent Mail: %s", warning)
			} else if registered {
				output.PrintSuccess("Registered project with Agent Mail")
			}

			return nil
		},
	}
	projectInitCmd.Flags().BoolVar(&projectInitForce, "force", false, "overwrite .ntm/config.toml if it already exists")
	projectCmd.AddCommand(projectInitCmd)

	cmd.AddCommand(projectCmd)

	return cmd
}

func buildEditorCommand(path string) (*exec.Cmd, error) {
	return buildEditorCommandWithFallback(path, "vi")
}

func buildEditorCommandWithFallback(path, fallback string) (*exec.Cmd, error) {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editor == "" {
		editor = fallback
	}

	editorCmd, editorArgs := parseEditorCommand(editor)
	if editorCmd == "" || !editorTokensSafe(append([]string{editorCmd}, editorArgs...)) {
		editorCmd, editorArgs = parseEditorCommand(fallback)
	}
	if editorCmd == "" {
		return nil, fmt.Errorf("editor not configured")
	}

	cmdPath, err := exec.LookPath(editorCmd)
	if err != nil {
		return nil, fmt.Errorf("editor not found: %w", err)
	}

	args := append([]string{cmdPath}, editorArgs...)
	args = append(args, path)
	return &exec.Cmd{Path: cmdPath, Args: args}, nil
}

func editorTokensSafe(tokens []string) bool {
	for _, token := range tokens {
		if strings.ContainsAny(token, ";&|<>`$\n\r") {
			return false
		}
	}
	return true
}

// IsJSONOutput returns true if JSON output is enabled
func IsJSONOutput() bool {
	return jsonOutput
}

// applyRedactionFlagOverrides applies CLI flag overrides to the redaction config.
// Priority: --allow-secret > --redact > config > default
func applyRedactionFlagOverrides(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}

	var overrideErr error
	// --redact flag overrides config mode
	if redactMode != "" {
		switch redactMode {
		case "off", "warn", "redact", "block":
			cfg.Redaction.Mode = redactMode
		default:
			overrideErr = fmt.Errorf("invalid --redact value %q", redactMode)
		}
	}

	// --allow-secret downgrades "block" to "warn" (still detects, but doesn't fail)
	// This allows the operation to proceed while still logging any findings
	if allowSecret && cfg.Redaction.Mode == "block" {
		cfg.Redaction.Mode = "warn"
	}
	return overrideErr
}

// canSkipConfigLoading returns true if we can skip Phase 2 config loading.
// This checks both subcommand names and robot flags for Phase 1 only operations.
func canSkipConfigLoading(cmdName string) bool {
	// Check subcommand first
	if startup.CanSkipConfig(cmdName) {
		return true
	}

	// Check robot flags that don't need config
	// These flags are processed in the root command's Run function
	if cmdName == "ntm" || cmdName == "" {
		if robotHelp || robotVersion || robotCapabilities {
			return true
		}
	}

	return false
}

// needsConfigLoading returns true if config should be loaded for this command.
// This checks both subcommand names and robot flags.
func needsConfigLoading(cmdName string) bool {
	// Check subcommand first
	if startup.NeedsConfig(cmdName) {
		return true
	}

	// Check robot flags that need config
	if cmdName == "ntm" || cmdName == "" {
		// robot-recipes needs config but not full startup
		if robotRecipes {
			return true
		}
		// Most other robot flags need full config
		if robotStatus || robotPlan || robotSnapshot || robotTail != "" || robotWatchBead != "" ||
			robotSend != "" || robotAck != "" || robotSpawn != "" ||
			robotInterrupt != "" || robotRestartPane != "" || robotExitCLI != "" || robotKillAgent != "" || robotKillPane != "" || robotDialogs != "" || robotAnswerDialog != "" || robotIncidentResolve != "" || robotProbe != "" || robotGraph || robotMail || robotHealth != "" ||
			robotHealthOAuth != "" || robotHealthRestartStuck != "" || robotLogs != "" || robotDiagnose != "" || robotTerse || robotMarkdown || robotSave != "" || robotRestore != "" ||
			robotContext != "" || robotEnsemble != "" || robotEnsembleSpawn != "" || robotEnsembleSuggest != "" || robotEnsembleStop != "" || robotAlerts || robotIsWorking != "" || robotAgentHealth != "" ||
			robotSmartRestart != "" || robotMonitor != "" || robotEnv != "" || robotSupportBundle != "" ||
			robotActivity != "" || robotProductivity != "" || robotSendReceipt != "" ||
			robotActionStatus != "" || robotWaitRequest != "" || robotCancelRequest != "" {
			return true
		}
	}

	return false
}
