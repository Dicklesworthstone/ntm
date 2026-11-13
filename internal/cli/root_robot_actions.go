package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func dispatchRobotActionsAndParity(cmd *cobra.Command, robotDryRunEffective bool) bool {
	if robotSendReceipt != "" {
		if robotSend != "" || robotInterrupt != "" {
			// The receipt branch would otherwise return before the send
			// and interrupt blocks, silently skipping the actuation with
			// a success-looking receipt response.
			failRobotCommand(errors.New("--robot-send-receipt cannot be combined with --robot-send or --robot-interrupt"), robot.ErrCodeInvalidFlag, "Run the send or interrupt first, then query the receipt in a separate invocation", "robot-send-receipt")
			return true
		}
		if err := robot.PrintSendReceipt(robotSendReceipt); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotActionStatus != "" {
		if robotSend != "" || robotInterrupt != "" {
			failRobotCommand(errors.New("--robot-action-status cannot be combined with --robot-send or --robot-interrupt"), robot.ErrCodeInvalidFlag, "Inspect request status in a separate invocation", "robot-action-status")
			return true
		}
		reqID := strings.TrimSpace(robotActionStatus)
		if reqID == "__present__" {
			reqID = strings.TrimSpace(robotRequestID)
		} else if trimmed := strings.TrimSpace(robotRequestID); trimmed != "" && trimmed != reqID {
			failRobotCommand(errors.New("conflicting request IDs supplied to --robot-action-status and --request-id"), robot.ErrCodeInvalidFlag, "Pass either --robot-action-status=ID or --robot-action-status --request-id=ID", "robot-action-status")
			return true
		}
		if err := robot.PrintActionStatus(reqID); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotWaitRequest != "" {
		if robotSend != "" || robotInterrupt != "" {
			failRobotCommand(errors.New("--robot-wait-request cannot be combined with --robot-send or --robot-interrupt"), robot.ErrCodeInvalidFlag, "Wait on the request in a separate invocation", "robot-wait-request")
			return true
		}
		reqID := strings.TrimSpace(robotWaitRequest)
		if reqID == "__present__" {
			reqID = strings.TrimSpace(robotRequestID)
		} else if trimmed := strings.TrimSpace(robotRequestID); trimmed != "" && trimmed != reqID {
			failRobotCommand(errors.New("conflicting request IDs supplied to --robot-wait-request and --request-id"), robot.ErrCodeInvalidFlag, "Pass either --robot-wait-request=ID or --robot-wait-request --request-id=ID", "robot-wait-request")
			return true
		}
		waitTimeout := 30 * time.Second
		if cmd.Flags().Changed("timeout") || cmd.Flags().Changed("wait-timeout") {
			parsed, err := util.ParseDurationWithDefault(robotWaitTimeout, time.Second, "timeout")
			if err != nil || parsed <= 0 {
				failRobotCommand(fmt.Errorf("invalid --timeout %q", robotWaitTimeout), robot.ErrCodeInvalidFlag, "Provide a positive duration like 30s or 500ms", "robot-wait-request")
				return true
			}
			waitTimeout = parsed
		}
		waitPoll := 100 * time.Millisecond
		if cmd.Flags().Changed("poll") || cmd.Flags().Changed("wait-poll") {
			parsed, err := util.ParseDurationWithDefault(robotWaitPoll, time.Millisecond, "poll")
			if err != nil || parsed <= 0 {
				failRobotCommand(fmt.Errorf("invalid --poll %q", robotWaitPoll), robot.ErrCodeInvalidFlag, "Provide a positive duration like 100ms or 1s", "robot-wait-request")
				return true
			}
			waitPoll = parsed
		}
		if err := robot.PrintWaitRequest(cmd.Context(), robot.WaitRequestOptions{
			RequestID:    reqID,
			Timeout:      waitTimeout,
			PollInterval: waitPoll,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotCancelRequest != "" {
		if robotSend != "" || robotInterrupt != "" {
			failRobotCommand(errors.New("--robot-cancel-request cannot be combined with --robot-send or --robot-interrupt"), robot.ErrCodeInvalidFlag, "Cancel the request in a separate invocation", "robot-cancel-request")
			return true
		}
		reqID := strings.TrimSpace(robotCancelRequest)
		if reqID == "__present__" {
			reqID = strings.TrimSpace(robotRequestID)
		} else if trimmed := strings.TrimSpace(robotRequestID); trimmed != "" && trimmed != reqID {
			failRobotCommand(errors.New("conflicting request IDs supplied to --robot-cancel-request and --request-id"), robot.ErrCodeInvalidFlag, "Pass either --robot-cancel-request=ID or --robot-cancel-request --request-id=ID", "robot-cancel-request")
			return true
		}
		if err := robot.PrintCancelRequest(reqID); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	if robotSend != "" {
		if flag := changedRobotRouteModifier(cmd); flag != "" {
			failRobotCommand(
				fmt.Errorf("%s is a --robot-route modifier; --robot-send does not route, so it would be ignored and the message sent to every matching pane", flag),
				robot.ErrCodeInvalidFlag,
				"Pick a pane with ntm --robot-route=SESSION --strategy=NAME --msg='...', then send to it with --robot-send=SESSION --pane=%N",
				"robot-send",
			)
			return true
		}
		singularPane := ""
		if cmd.Flags().Changed("pane") {
			singularPane = strings.TrimSpace(robotHistoryPane)
			if singularPane == "" {
				failRobotCommand(errors.New("--pane requires one N, W.P, or %N selector with --robot-send"), robot.ErrCodeInvalidFlag, "Use --pane=2, --pane=1.0, or --pane=%3", "robot-send")
				return true
			}
		}
		if singularPane != "" && strings.TrimSpace(robotPanes) != "" {
			failRobotCommand(errors.New("--pane and --panes are mutually exclusive with --robot-send"), robot.ErrCodeInvalidFlag, "Use --pane for exactly one physical pane or --panes for a set", "robot-send")
			return true
		}
		session, err := resolveRobotLiveSession(cmd.Context(), robotSend)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-send")
			return true
		}
		// Load message from --msg or --msg-file
		msg, err := loadRobotMessageInput(robotSendMsg, robotSendMsgFile, "--msg", "--msg-file")
		if err != nil {
			// Missing/empty message content is an argument problem, not a
			// malformed flag (INVALID_ARGS per the robot taxonomy).
			failRobotCommand(err, robot.ErrCodeInvalidArgs, "Provide a readable non-empty --msg or --msg-file", "robot-send")
			return true
		}
		robotSendMsg = msg

		// Validate message is provided
		if robotSendMsg == "" {
			failRobotCommand(errors.New("--msg or --msg-file is required with --robot-send"), robot.ErrCodeInvalidArgs, "Provide --msg or --msg-file", "robot-send")
			return true
		}
		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-send")
			return true
		}
		// Parse exclude list
		var excludeList []string
		if robotSendExclude != "" {
			excludeList = strings.Split(robotSendExclude, ",")
		}
		// Parse agent types
		var agentTypes []string
		if robotSendType != "" {
			agentTypes = strings.Split(robotSendType, ",")
		}
		// Determine enter behavior (default true unless explicitly overridden)
		var enterOverride *bool
		if cmd.Flags().Changed("enter") || cmd.Flags().Changed("submit") {
			enterOverride = &robotSendEnter
		}

		// Check if --track flag is set for combined send+ack mode
		if robotAckTrack {
			// The tracked path performs its own inline dispatch that does
			// not (yet) participate in the durable claim/replay protocol.
			// Failing loudly beats accepting --op-id and providing no
			// idempotency.
			if strings.TrimSpace(robotOpID) != "" {
				failRobotCommand(errors.New("--op-id is not supported with --track"), robot.ErrCodeInvalidFlag, "Send with --op-id first, then poll with --robot-ack separately", "robot-send")
				return true
			}
			// Canonical modifiers for ack behavior are --timeout and --poll.
			// --ack-timeout/--ack-poll remain as deprecated aliases for backward compatibility.
			ackTimeoutStr := robotAckTimeout
			if cmd.Flags().Changed("timeout") {
				ackTimeoutStr = robotWaitTimeout
			}
			ackTimeout, err := util.ParseDurationWithDefault(ackTimeoutStr, time.Millisecond, "timeout")
			if err != nil {
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 30s or 500ms", "robot-send")
				return true
			}

			ackPollMs := robotAckPoll
			if cmd.Flags().Changed("poll") {
				pollDur, err := util.ParseDurationWithDefault(robotWaitPoll, time.Millisecond, "poll")
				if err != nil {
					failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 1s or 500ms", "robot-send")
					return true
				}
				ackPollMs = int(pollDur.Milliseconds())
			}
			withMemory, memoryInject := robotSendMemoryOptions(robotSendWithMemory, cfg)
			withCASS, cassQuery, cassFilter, cassInject := robotSendCASSOptions(robotSendWithCASS, robotSendNoCASS, cfg)
			opts := robot.SendAndAckOptions{
				SendOptions: robot.SendOptions{
					Context:      cmd.Context(),
					Session:      session,
					Message:      robotSendMsg,
					All:          robotSendAll,
					Pane:         singularPane,
					Panes:        paneSelectors,
					AgentTypes:   agentTypes,
					Exclude:      excludeList,
					DelayMs:      robotSendDelay,
					Enter:        enterOverride,
					DryRun:       robotDryRunEffective,
					ClearInput:   robotSendClearInput,
					VerifyRender: robotSendVerifyRender,
					WithCASS:     withCASS,
					CASSConfig:   cassQuery,
					FilterConfig: cassFilter,
					InjectConfig: cassInject,
					WithMemory:   withMemory,
					MemoryInject: memoryInject,
				},
				AckTimeoutMs: int(ackTimeout.Milliseconds()),
				AckPollMs:    ackPollMs,
			}
			if cfg != nil {
				opts.Redaction = cfg.Redaction.ToRedactionLibConfig()
			}
			if err := robot.PrintSendAndAck(opts); err != nil {
				recordRobotProcessExit(err)
			}
			return true
		}

		withMemory, memoryInject := robotSendMemoryOptions(robotSendWithMemory, cfg)
		withCASS, cassQuery, cassFilter, cassInject := robotSendCASSOptions(robotSendWithCASS, robotSendNoCASS, cfg)
		opts := robot.SendOptions{
			Context:        cmd.Context(),
			Session:        session,
			Message:        robotSendMsg,
			All:            robotSendAll,
			Pane:           singularPane,
			Panes:          paneSelectors,
			AgentTypes:     agentTypes,
			Exclude:        excludeList,
			DelayMs:        robotSendDelay,
			Enter:          enterOverride,
			DryRun:         robotDryRunEffective,
			ClearInput:     robotSendClearInput,
			VerifyRender:   robotSendVerifyRender,
			IdempotencyKey: strings.TrimSpace(robotOpID),
			WithCASS:       withCASS,
			CASSConfig:     cassQuery,
			FilterConfig:   cassFilter,
			InjectConfig:   cassInject,
			WithMemory:     withMemory,
			MemoryInject:   memoryInject,
		}
		if cfg != nil {
			opts.Redaction = cfg.Redaction.ToRedactionLibConfig()
		}
		if err := robot.PrintSend(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if cmd.Flags().Changed("robot-health") {
		if robotHealth == "__present__" {
			if err := robot.PrintHealth(); err != nil {
				recordRobotProcessExit(err)
			}
			return true
		}
		if robotHealth == "" {
			failRobotCommand(errors.New("--robot-health requires a session name or no value for project health"), robot.ErrCodeInvalidFlag, "Use --robot-health for project health or --robot-health=SESSION for session health", "robot-health")
			return true
		}
		session, err := resolveRobotLiveSession(cmd.Context(), robotHealth)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-health")
			return true
		}
		if err := robot.PrintSessionHealth(session); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotHealthOAuth != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotHealthOAuth)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-health-oauth")
			return true
		}
		// PrintHealthOAuth emits the JSON envelope and returns the process
		// exit code; a success:false result must exit nonzero (ntm#207).
		recordLegacyRobotExit(robot.PrintHealthOAuth(session))
		return true
	}
	if robotHealthRestartStuck != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotHealthRestartStuck)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-health-restart-stuck")
			return true
		}
		threshold, err := robot.ParseStuckThreshold(robotStuckThreshold)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a valid stuck-duration threshold", "robot-health-restart-stuck")
			return true
		}
		opts := autoRestartStuckOptions(session, threshold, robotDryRunEffective, cfg)
		if err := robot.PrintAutoRestartStuck(cmd.Context(), opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotLogs != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotLogs)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-logs")
			return true
		}
		var panes []int
		if robotLogsPanes != "" {
			var err error
			panes, err = robot.ParsePanesArg(robotLogsPanes)
			if err != nil {
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated numeric pane indices", "robot-logs")
				return true
			}
		}
		opts := robot.LogsOptions{
			Session: session,
			Panes:   panes,
			Limit:   robotLogsLimit,
		}
		if err := robot.PrintLogs(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotDiagnose != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotDiagnose)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-diagnose")
			return true
		}
		if robotDiagnoseBrief {
			// The brief summary covers the whole session and never
			// repairs; refuse rather than silently drop --fix/--diagnose-pane.
			if robotDiagnoseFix || robotDiagnosePane >= 0 {
				failRobotCommand(errors.New("--brief cannot be combined with --fix or --diagnose-pane"), robot.ErrCodeInvalidFlag, "Drop --brief to auto-fix or to diagnose one pane", "robot-diagnose")
				return true
			}
			if err := robot.PrintDiagnoseBrief(cmd.Context(), session); err != nil {
				recordRobotProcessExit(err)
			}
		} else {
			opts := robot.DiagnoseOptions{
				Session: session,
				Pane:    robotDiagnosePane,
				Fix:     robotDiagnoseFix,
			}
			if err := robot.PrintDiagnose(cmd.Context(), opts); err != nil {
				recordRobotProcessExit(err)
			}
		}
		return true
	}
	if robotRecipes {
		if err := robot.PrintRecipes(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSchema != "" {
		if err := robot.PrintSchema(robotSchema); err != nil {
			recordRobotProcessExit(err)
			return true
		}
		return true
	}
	if robotAck != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotAck)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-ack")
			return true
		}
		// Load message from --msg or --msg-file (reuse logic from robot-send)
		msg, err := loadRobotMessageInput(robotSendMsg, robotSendMsgFile, "--msg", "--msg-file")
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidArgs, "Provide a readable non-empty --msg or --msg-file", "robot-ack")
			return true
		}
		robotSendMsg = msg

		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-ack")
			return true
		}
		// Canonical modifiers for ack behavior are --timeout and --poll.
		// --ack-timeout/--ack-poll remain as deprecated aliases for backward compatibility.
		ackTimeoutStr := robotAckTimeout
		if cmd.Flags().Changed("timeout") {
			ackTimeoutStr = robotWaitTimeout
		}
		ackTimeout, err := util.ParseDurationWithDefault(ackTimeoutStr, time.Millisecond, "timeout")
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 30s or 500ms", "robot-ack")
			return true
		}

		ackPollMs := robotAckPoll
		if cmd.Flags().Changed("poll") {
			pollDur, err := util.ParseDurationWithDefault(robotWaitPoll, time.Millisecond, "poll")
			if err != nil {
				failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 1s or 500ms", "robot-ack")
				return true
			}
			ackPollMs = int(pollDur.Milliseconds())
		}
		var ackAgentTypes []string
		if robotSendType != "" {
			ackAgentTypes = strings.Split(robotSendType, ",")
		}
		opts := robot.AckOptions{
			Context:    cmd.Context(),
			Session:    session,
			Message:    robotSendMsg, // Reuse --msg flag for echo detection
			Panes:      paneSelectors,
			AgentTypes: ackAgentTypes,
			TimeoutMs:  int(ackTimeout.Milliseconds()),
			PollMs:     ackPollMs,
		}
		if err := robot.PrintAck(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotAssign != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotAssign)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-assign")
			return true
		}
		projectDir, err := resolveExplicitProjectDirForSessionContext(cmd.Context(), session)
		if err != nil {
			code := robot.ErrCodeInternalError
			hint := "Ensure the target session has a resolvable project directory"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
				hint = "Retry after the cancellation or timeout condition clears"
			}
			failRobotCommand(err, code, hint, "robot-assign")
			return true
		}
		if err := configureAuthoritativeAssignmentPolicy(projectDir); err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Fix the selected global config and the target project's .ntm/config.toml", "robot-assign")
			return true
		}
		var beads []string
		if robotAssignBeads != "" {
			beads = strings.Split(robotAssignBeads, ",")
		}
		opts := robot.AssignOptions{
			Session:    session,
			ProjectDir: projectDir,
			Beads:      beads,
			Strategy:   robotAssignStrategy,
			Relax:      robotAssignRelax,
		}
		if err := robot.PrintAssign(cmd.Context(), opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotBulkAssign != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotBulkAssign)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-bulk-assign")
			return true
		}
		skipSelectors, err := robot.ParsePaneSelectorsArg(robotBulkAssignSkip)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-bulk-assign")
			return true
		}
		reservationPaths, err := parseRobotReservationPathsArg(robotReservationPaths)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated non-empty project-relative path globs", "robot-bulk-assign")
			return true
		}
		if robotRequireReservation && !robotReserveFiles {
			failRobotCommand(errors.New("--require-reservation conflicts with --reserve-files=false"), robot.ErrCodeInvalidFlag, "Bulk assignment reserves files by default; drop one of the two flags", "robot-bulk-assign")
			return true
		}
		strategy := resolveRobotBulkAssignStrategy(cmd)
		opts := robot.BulkAssignOptions{
			Session:            session,
			ConfigPath:         selectedConfigPath(),
			RequireConfig:      selectedConfigIsExplicit(),
			FromBV:             robotBulkAssignFromBV,
			Strategy:           strategy,
			AllocationJSON:     robotBulkAssignAlloc,
			DryRun:             robotDryRunEffective,
			Parallel:           robotBulkAssignParallel,
			Stagger:            robotBulkAssignStagger,
			SkipPaneSelectors:  skipSelectors,
			PromptTemplatePath: robotBulkAssignTemplate,
			ReserveFiles:       robotReserveFiles,
			ReservationPaths:   reservationPaths,
			PromptContext:      resolvePromptContextOptions(robotSendWithCASS, robotSendNoCASS, robotSendWithMemory, cfg),
		}
		// Project/user-level default dispatch template (#153). A per-invocation
		// --bulk-assign-template still wins; these only fill the gap the built-in
		// const used to occupy.
		if cfg != nil {
			opts.DefaultTemplate = cfg.Assign.PromptTemplate
			opts.DefaultTemplatePath = cfg.Assign.PromptTemplateFile
		}
		if err := robot.PrintBulkAssign(cmd.Context(), opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSpawn != "" {
		// Preserve the parsed duration so subsecond readiness deadlines remain exact.
		spawnTimeout, err := util.ParseDurationWithDefault(resolveRobotSpawnTimeout(cmd), time.Second, "timeout")
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 30s or 500ms", "robot-spawn")
			return true
		}
		reservationPaths, err := parseRobotReservationPathsArg(robotReservationPaths)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated non-empty project-relative path globs", "robot-spawn")
			return true
		}
		opts, err := robotSpawnOptionsFromFlags(cmd, spawnTimeout, reservationPaths, robotDryRunEffective)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use count[:model[:effort]] (effort also as model@effort), e.g. --spawn-cod=2 or --spawn-cod=8:gpt-5.3-codex:high", "robot-spawn")
			return true
		}
		if err := applyRobotSpawnPromptFlags(cmd, cfg, &opts); err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use one of --spawn-prompt/--spawn-prompt-file, --spawn-stagger-mode=none|fixed|smart, and a --spawn-stagger-delay such as 20s (0-5m); fix any invalid [spawn] config value", "robot-spawn")
			return true
		}
		if err := robot.PrintSpawn(cmd.Context(), opts, cfg); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotAgentNames != "" {
		customNames := robot.ParseCustomNames(robotSpawnNames)
		// PrintAgentNames emits the JSON envelope and returns the process
		// exit code; success:false (e.g. SESSION_NOT_FOUND) must exit
		// nonzero per the robot contract (ntm#215).
		recordLegacyRobotExit(robot.PrintAgentNames(robotAgentNames, customNames))
		return true
	}
	if robotControllerSpawn != "" {
		opts := ControllerInput{
			Session:    robotControllerSpawn,
			AgentType:  robotControllerAgentType,
			PromptFile: robotControllerPrompt,
			NoPrompt:   robotControllerNoPrompt,
		}
		resp, err := buildControllerResponse(cmd.Context(), opts)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-controller-spawn")
			return true
		}
		if err := output.PrintJSON(resp); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInterrupt != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotInterrupt)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-interrupt")
			return true
		}
		// Parse pane filter (reuse --panes flag)
		var paneFilter []string
		if robotPanes != "" {
			paneFilter = strings.Split(robotPanes, ",")
		}
		// Parse agent type filter (shared --type flag)
		var interruptAgentTypes []string
		if robotSendType != "" {
			interruptAgentTypes = strings.Split(robotSendType, ",")
		}
		// Parse interrupt timeout duration
		interruptTimeout, err := util.ParseDurationWithDefault(resolveRobotInterruptTimeout(cmd), time.Millisecond, "timeout")
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration such as 30s or 500ms", "robot-interrupt")
			return true
		}
		opts := robot.InterruptOptions{
			Session:        session,
			Message:        resolveRobotInterruptMessage(cmd),
			Panes:          paneFilter,
			AgentTypes:     interruptAgentTypes,
			All:            resolveRobotInterruptAll(cmd),
			Force:          resolveRobotInterruptForce(cmd),
			NoWait:         robotInterruptNoWait,
			TimeoutMs:      int(interruptTimeout.Milliseconds()),
			DryRun:         robotDryRunEffective,
			IdempotencyKey: strings.TrimSpace(robotOpID),
		}
		if cfg != nil {
			opts.Redaction = cfg.Redaction.ToRedactionLibConfig()
		}
		if err := robot.PrintInterrupt(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	for _, lifecycleFlag := range []struct {
		name  string
		value string
	}{
		{"robot-exit-cli", robotExitCLI},
		{"robot-kill-agent", robotKillAgent},
		{"robot-kill-pane", robotKillPane},
		{"robot-dialogs", robotDialogs},
		{"robot-answer-dialog", robotAnswerDialog},
	} {
		if lifecycleFlag.value == "" && cmd.Flags().Changed(lifecycleFlag.name) {
			failRobotCommand(fmt.Errorf("--%s requires a session name", lifecycleFlag.name), robot.ErrCodeInvalidFlag, fmt.Sprintf("Use --%s=SESSION; 'ntm list' shows available sessions", lifecycleFlag.name), lifecycleFlag.name)
			return true
		}
	}
	if robotDialogs != "" || robotAnswerDialog != "" {
		verb, flagValue, flagName := "dialogs", robotDialogs, "robot-dialogs"
		if robotAnswerDialog != "" {
			verb, flagValue, flagName = "answer-dialog", robotAnswerDialog, "robot-answer-dialog"
		}
		session, err := resolveRobotLiveSession(cmd.Context(), flagValue)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", flagName)
			return true
		}
		var paneFilter []string
		if robotPanes != "" {
			paneFilter = strings.Split(robotPanes, ",")
		}
		if verb == "dialogs" {
			err = robot.PrintDialogs(cmd.Context(), session, paneFilter)
		} else {
			err = robot.PrintAnswerDialog(cmd.Context(), robot.AnswerDialogOptions{
				Session: session,
				Panes:   paneFilter,
				Choice:  robotDialogChoice,
			})
		}
		if err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotKillPane != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotKillPane)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-kill-pane")
			return true
		}
		var paneFilter []string
		if robotPanes != "" {
			paneFilter = strings.Split(robotPanes, ",")
		}
		if err := robot.PrintKillPane(cmd.Context(), robot.KillPaneOptions{
			Session: session,
			Panes:   paneFilter,
			Force:   robotSmartRestartForce,
		}); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotExitCLI != "" || robotKillAgent != "" {
		verb, flagValue, flagName := "exit-cli", robotExitCLI, "robot-exit-cli"
		if robotKillAgent != "" {
			verb, flagValue, flagName = "kill-agent", robotKillAgent, "robot-kill-agent"
		}
		session, err := resolveRobotLiveSession(cmd.Context(), flagValue)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", flagName)
			return true
		}
		var paneFilter []string
		if robotPanes != "" {
			paneFilter = strings.Split(robotPanes, ",")
		}
		opts := robot.LifecycleOptions{
			Session:        session,
			Panes:          paneFilter,
			Relaunch:       robotLifecycleRelaunch,
			RespawnShell:   robotExitCLIRespawnShell,
			AllowPaneClose: robotExitCLIAllowPaneClose,
		}
		if verb == "kill-agent" && (robotExitCLIRespawnShell || robotExitCLIAllowPaneClose) {
			failRobotCommand(fmt.Errorf("--respawn-shell and --allow-pane-close apply only to --robot-exit-cli"), robot.ErrCodeInvalidFlag, "Use --robot-exit-cli=SESSION with --respawn-shell or --allow-pane-close", flagName)
			return true
		}
		if verb == "exit-cli" {
			err = robot.PrintExitCLI(cmd.Context(), opts)
		} else {
			err = robot.PrintKillAgent(cmd.Context(), opts)
		}
		if err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotRestartPane != "" {
		environment, err := readRestartEnvironment(robotRestartEnvironmentFile)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use --restart-env-file with a private JSON object of required environment names and string values", "robot-restart-pane")
			return true
		}
		session, err := resolveRobotLiveSession(cmd.Context(), robotRestartPane)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-restart-pane")
			return true
		}
		// Parse pane filter (reuse --panes flag). The restart engine
		// validates each selector against the shared N / W.P / %N grammar.
		var paneFilter []string
		if robotPanes != "" {
			paneFilter = strings.Split(robotPanes, ",")
		}
		projectDir := ""
		if strings.TrimSpace(robotRestartPaneBead) != "" {
			projectDir, err = resolveExplicitProjectDirForSessionContext(cmd.Context(), session)
			if err != nil {
				code := robot.ErrCodeInternalError
				hint := "Ensure the target session has one authoritative project directory"
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					code = robot.ErrCodeTimeout
					hint = "Retry after the cancellation or timeout condition clears"
				}
				failRobotCommand(err, code, hint, "robot-restart-pane")
				return true
			}
		}
		opts := robot.RestartPaneOptions{
			Session:       session,
			Panes:         paneFilter,
			Type:          robotSendType,
			All:           robotSendAll,
			DryRun:        robotDryRunEffective,
			Bead:          robotRestartPaneBead,
			Prompt:        robotRestartPanePrompt,
			Model:         robotRestartPaneModel,
			AgentArgs:     robotRestartPaneArgs,
			Environment:   environment,
			Config:        cfg,
			ProjectDir:    projectDir,
			ConfigPath:    selectedConfigPath(),
			RequireConfig: selectedConfigIsExplicit(),
		}
		if err := robot.PrintRestartPaneContext(cmd.Context(), opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotPaneAddress != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotPaneAddress)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-pane-address")
			return true
		}
		if err := robot.PrintPaneAddresses(session); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotProbe != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotProbe)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-probe")
			return true
		}
		paneSelectors, err := robot.ParsePaneSelectorsArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors", "robot-probe")
			return true
		}
		flags, err := robot.ParseProbeFlags(robotProbeMethod, robotProbeTimeout, robotProbeAggressive)
		if err != nil {
			recordRobotProcessExit(robot.PrintProbeFlagError(err))
			return true
		}
		opts := robot.ProbeSessionOptions{
			Session:       session,
			PaneSelectors: paneSelectors,
			Flags:         *flags,
		}
		exitCode := robot.PrintProbeSession(opts)
		recordLegacyRobotExit(exitCode)
		return true
	}
	if robotTerse {
		if err := robot.PrintTerse(cfg); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotMarkdown {
		session, err := resolveOptionalRobotSessionFilter(cmd.Context(), resolveRobotSharedFlag(cmd, "md-session", robotMarkdownSession, "session", robotSharedSession))
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-markdown")
			return true
		}
		opts := robot.DefaultMarkdownOptions()
		opts.Compact = robotMarkdownCompact
		opts.Session = session
		if robotMarkdownSections != "" {
			parts := strings.Split(robotMarkdownSections, ",")
			var sections []string
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					sections = append(sections, p)
				}
			}
			if len(sections) > 0 {
				opts.IncludeSections = sections
			}
		}
		if robotMarkdownMaxBeads > 0 {
			opts.MaxBeads = robotMarkdownMaxBeads
		}
		if robotMarkdownMaxAlerts > 0 {
			opts.MaxAlerts = robotMarkdownMaxAlerts
		}
		if err := robot.PrintMarkdown(cfg, opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSave != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotSave)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-save")
			return true
		}
		opts := robot.SaveOptions{
			Session:    session,
			OutputFile: resolveRobotSaveOutput(cmd),
		}
		if err := robot.PrintSave(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotRestore != "" {
		opts := robot.RestoreOptions{
			SavedName: robotRestore,
			DryRun:    robotDryRunEffective,
		}
		if err := robot.PrintRestore(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// TUI Parity robot handlers - expose TUI functionality to AI agents
	if cmd.Flags().Changed("robot-files") {
		sessionName := robotFiles
		if sessionName == "__present__" {
			sessionName = ""
		}
		session, err := resolveOptionalRobotSessionFilter(cmd.Context(), sessionName)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-files")
			return true
		}
		opts := robot.FilesOptions{
			Session:    session,
			TimeWindow: robotFilesWindow,
			Limit:      resolveRobotSharedLimit(cmd, "files-limit", robotFilesLimit),
		}
		if err := robot.PrintFiles(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInspectPane != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotInspectPane)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-inspect-pane")
			return true
		}
		opts := robot.InspectPaneOptions{
			Session:     session,
			PaneIndex:   robotInspectIndex,
			Lines:       resolveRobotSharedLines(cmd, "inspect-lines", robotInspectLines),
			IncludeCode: robotInspectCode,
		}
		if err := robot.PrintInspectPane(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInspectSession != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotInspectSession)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-inspect-session")
			return true
		}
		opts := robot.InspectSessionOptions{
			Session: session,
		}
		if err := robot.PrintInspectSession(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInspectAgent != "" {
		opts := robot.InspectAgentOptions{
			AgentID: robotInspectAgent,
		}
		if err := robot.PrintInspectAgent(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInspectWork != "" {
		opts := robot.InspectWorkOptions{
			BeadID: robotInspectWork,
		}
		if err := robot.PrintInspectWork(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInspectCoord != "" {
		opts := robot.InspectCoordinationOptions{
			AgentName: robotInspectCoord,
		}
		if err := robot.PrintInspectCoordination(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInspectQuota != "" {
		opts := robot.InspectQuotaOptions{
			QuotaID: robotInspectQuota,
		}
		if err := robot.PrintInspectQuota(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotInspectIncident != "" {
		opts := robot.InspectIncidentOptions{
			IncidentID: robotInspectIncident,
		}
		if err := robot.PrintInspectIncident(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotIncidentResolve == "" && cmd.Flags().Changed("robot-incident-resolve") {
		failRobotCommand(errors.New("--robot-incident-resolve requires an incident id"), robot.ErrCodeInvalidFlag, "Use --robot-incident-resolve=INCIDENT_ID; list ids via --robot-snapshot or --robot-attention", "robot-incident-resolve")
		return true
	}
	if robotIncidentResolve != "" {
		if err := robot.PrintIncidentResolve(robotIncidentResolve, robotIncidentNote); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotContextInject != "" {
		session, projectDir, err := resolveRobotSessionProjectScope(cmd.Context(), robotContextInject)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-context-inject")
			return true
		}
		files := defaultContextFiles()
		if robotContextInjectFiles != "" {
			files = strings.Split(robotContextInjectFiles, ",")
			for i := range files {
				files[i] = strings.TrimSpace(files[i])
			}
		}
		content, injected, truncated, err := formatContextInjectContent(projectDir, files, robotContextInjectMax)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use readable project-relative context files", "robot-context-inject")
			return true
		}
		result := contextInjectionResult(session, injected, len(content), truncated, robotContextInjectDry, nil, nil)
		if len(injected) > 0 {
			panes, pErr := tmux.GetPanes(session)
			if pErr != nil {
				failRobotCommand(fmt.Errorf("get panes: %w", pErr), robot.ErrCodeInternalError, "Check tmux session health", "robot-context-inject")
				return true
			}
			targets, targetErr := selectContextInjectTargetPanes(panes, robotContextInjectPane, robotContextInjectAll, session)
			if targetErr != nil {
				failRobotCommand(targetErr, robot.ErrCodePaneNotFound, "Inspect canonical pane addresses with --robot-is-working", "robot-context-inject")
				return true
			}
			result, err = injectContextIntoPanes(cmd.Context(), session, targets, content, robotContextInjectMax, robotContextInjectDry, sendContextToPane)
			result.InjectedFiles, result.Truncated = injected, truncated
		}
		result.Mode = "files"
		if result.InjectedFiles == nil {
			result.InjectedFiles = []string{}
		}
		if result.PanesInjected == nil {
			result.PanesInjected = []int{}
		}
		if printErr := output.PrintJSON(result); printErr != nil {
			recordRobotProcessExit(printErr)
		} else if err != nil {
			recordRobotProcessExit(robot.ExitResultForResponse(robot.NewErrorResponse(err, result.ErrorCode, result.Hint), err, true))
		}
		return true
	}
	if cmd.Flags().Changed("robot-metrics") {
		sessionName := robotMetrics
		if sessionName == "__present__" {
			sessionName = ""
		}
		session, err := resolveOptionalRobotLiveSession(cmd.Context(), sessionName)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-metrics")
			return true
		}
		opts := robot.MetricsOptions{
			Session:         session,
			Period:          robotMetricsPeriod,
			DiskAttribution: robotDiskAttribution,
		}
		if err := robot.PrintMetrics(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotReplay != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotReplay)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-replay")
			return true
		}
		opts := robot.ReplayOptions{
			Session:   session,
			HistoryID: robotReplayID,
			DryRun:    resolveRobotReplayDryRun(cmd),
		}
		if err := robot.PrintReplay(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotPaletteInfo {
		paletteSession, err := resolveOptionalRobotSessionFilter(cmd.Context(), resolveRobotPaletteSession(cmd))
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-palette")
			return true
		}
		opts := robot.PaletteOptions{
			Session:     paletteSession,
			Category:    resolveRobotSharedFlag(cmd, "palette-category", robotPaletteCategory, "category", jfpCategory),
			SearchQuery: robotPaletteSearch,
		}
		if err := robot.PrintPalette(cfg, opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotDismissAlert != "" {
		dismissSession, err := resolveOptionalRobotSessionFilter(cmd.Context(), resolveRobotSharedFlag(cmd, "dismiss-session", robotDismissSession, "session", robotSharedSession))
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-dismiss-alert")
			return true
		}
		dismissAlertID := robotDismissAlert
		if dismissAlertID == "__present__" {
			dismissAlertID = ""
		}
		opts := robot.DismissAlertOptions{
			AlertID:    dismissAlertID,
			Session:    dismissSession,
			DismissAll: resolveRobotSharedBool(cmd, "dismiss-all", robotDismissAll, "all", robotSendAll),
		}
		if err := robot.PrintDismissAlert(cfg, opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-diff handler for comparing agent activity (synthesis)
	if robotDiff != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotDiff)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-diff")
			return true
		}
		since, err := parseRobotSinceWindow(resolveRobotDiffSince(cmd), time.Minute, "since")
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration or supported timestamp", "robot-diff")
			return true
		}
		opts := robot.DiffOptions{
			Session: session,
			Since:   since,
		}
		if err := robot.PrintDiff(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-alerts handler for alert listing (TUI parity)
	if robotAlerts {
		session, err := resolveOptionalRobotSessionFilter(cmd.Context(), resolveRobotAlertsSession(cmd))
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-alerts")
			return true
		}
		opts := robot.TUIAlertsOptions{
			Session:  session,
			Severity: robotAlertsSeverity,
			Type:     robotAlertsType,
		}
		if err := robot.PrintAlertsTUI(cfg, opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-beads-list handler for bead listing (TUI parity)
	if robotBeadsList {
		opts := robot.BeadsListOptions{
			Status:   robotBeadsStatus,
			Priority: robotBeadsPriority,
			Assignee: robotBeadsAssignee,
			Type:     robotBeadsType,
			Limit:    resolveRobotSharedLimit(cmd, "beads-limit", robotBeadsLimit),
		}
		if err := robot.PrintBeadsList(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-bead handlers for bead management
	if robotBeadClaim != "" {
		opts := robot.BeadClaimOptions{
			BeadID:   robotBeadClaim,
			Assignee: beadAssignee,
		}
		if err := robot.PrintBeadClaim(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotBeadCreate {
		var labels, dependsOn []string
		if beadLabels != "" {
			labels = strings.Split(beadLabels, ",")
		}
		if beadDependsOn != "" {
			dependsOn = strings.Split(beadDependsOn, ",")
		}
		opts := robot.BeadCreateOptions{
			Title:       beadTitle,
			Type:        beadType,
			Priority:    beadPriority,
			Description: beadDescription,
			Labels:      labels,
			DependsOn:   dependsOn,
		}
		if err := robot.PrintBeadCreate(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotBeadShow != "" {
		opts := robot.BeadShowOptions{
			BeadID: robotBeadShow,
		}
		if err := robot.PrintBeadShow(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotBeadClose != "" {
		opts := robot.BeadCloseOptions{
			BeadID: robotBeadClose,
			Reason: beadCloseReason,
		}
		if err := robot.PrintBeadClose(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-summary handler for session activity summary
	if robotSummary != "" {
		session, err := resolveRobotLiveSession(cmd.Context(), robotSummary)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions", "robot-summary")
			return true
		}
		since, err := parseRobotSinceWindow(resolveRobotSummarySince(cmd), time.Minute, "since")
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use a duration or supported timestamp", "robot-summary")
			return true
		}
		opts := robot.SummaryOptions{
			Session: session,
			Since:   since,
		}
		if err := robot.PrintSummary(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-account-status handler for CAAM account status
	if robotAccountStatus {
		opts := robot.AccountStatusOptions{
			Provider: resolveRobotProvider(cmd, "account-status-provider", robotAccountStatusProvider),
		}
		if err := robot.PrintAccountStatus(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-accounts-list handler for CAAM accounts list
	if robotAccountsList {
		opts := robot.AccountsListOptions{
			Provider: resolveRobotProvider(cmd, "accounts-list-provider", robotAccountsListProvider),
		}
		if err := robot.PrintAccountsList(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-switch-account handler for CAAM account switching
	if robotSwitchAccount != "" {
		opts := robot.ParseSwitchAccountArg(robotSwitchAccount)
		if robotHistoryPane != "" {
			opts.Pane = robotHistoryPane
		}
		if err := robot.PrintSwitchAccount(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-dcg-status handler for DCG status
	if robotDCGStatus {
		if err := robot.PrintDCGStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	// Robot-dcg-check / robot-guard handler for DCG command preflight
	if robotDCGCheck {
		opts := robot.DCGCheckOptions{
			Command: robotDCGCmd,
			Context: robotDCGContext,
			CWD:     robotDCGCwd,
		}
		if err := robot.PrintDCGCheckWithOptions(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotSafetySimulate {
		opts := robot.SafetySimulationOptions{
			Command: robotDCGCmd,
			Steps:   robotSafetySteps,
		}
		if err := robot.PrintSafetySimulation(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotPolicyDecisions {
		limit := 0
		if cmd.Flags().Changed("limit") {
			limit = cassLimit
		}
		opts := robot.PolicyDecisionsOptions{
			EntityType: robotPolicyEntityType,
			EntityID:   robotPolicyEntityID,
			Limit:      limit,
		}
		if err := robot.PrintPolicyDecisions(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	if robotUBSScan != "" {
		scanPath := strings.TrimSpace(robotScanPath)
		if robotUBSScan != "__present__" {
			inlinePath := strings.TrimSpace(robotUBSScan)
			if scanPath != "" && inlinePath != "" && scanPath != inlinePath {
				failRobotCommand(
					errors.New("cannot specify different paths via --robot-ubs-scan=PATH and --scan-path"),
					robot.ErrCodeInvalidFlag,
					"Specify the target directory once via --robot-ubs-scan=PATH or --scan-path=PATH.",
					"robot-ubs-scan",
				)
				return true
			}
			if scanPath == "" {
				scanPath = inlinePath
			}
		}
		limit := 0
		if cmd.Flags().Changed("limit") {
			limit = cassLimit
		}
		opts := robot.UBSScanOptions{
			Path:             scanPath,
			File:             robotScanFile,
			Staged:           robotScanStaged,
			Diff:             robotScanDiff,
			Languages:        robotScanOnly,
			ExcludeLanguages: robotScanExclude,
			Timeout:          robotScanTimeout,
			FailOnWarning:    robotScanFailOnWarning,
			Limit:            limit,
		}
		if err := robot.PrintUBSScan(cmd.Context(), opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-quota-status handler for caut quota status
	if robotQuotaStatus {
		if err := robot.PrintQuotaStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-quota-check handler for caut quota check (single provider)
	if robotQuotaCheck {
		if err := robot.PrintQuotaCheck(resolveRobotProvider(cmd, "quota-check-provider", robotQuotaCheckProvider)); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-env handler for environment info (bd-18gwh)
	if robotEnv != "" {
		if err := robot.PrintEnv(robotEnv); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-rano-stats handler for per-agent network stats
	if robotRanoStats {
		panes, err := robot.ParsePanesArg(robotPanes)
		if err != nil {
			failRobotCommand(err, robot.ErrCodeInvalidFlag, "Use comma-separated numeric pane indices", "robot-rano-stats")
			return true
		}
		opts := robot.RanoStatsOptions{
			Panes:  panes,
			Window: robotRanoWindow,
		}
		if cfg != nil {
			opts.Database = cfg.Integrations.Rano.SQLitePath
		}
		if err := robot.PrintRanoStats(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-rch-status handler for RCH status
	if robotRCHStatus {
		if err := robot.PrintRCHStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}
	// Robot-proxy-status handler for rust_proxy status
	if robotProxyStatus {
		if err := robot.PrintProxyStatus(); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-rch-workers handler for RCH workers
	if robotRCHWorkers {
		opts := robot.RCHWorkersOptions{
			Worker: robotRCHWorker,
		}
		if err := robot.PrintRCHWorkers(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	// Robot-mail-check handler for Agent Mail inbox (bd-adgv)
	if robotMailCheck {
		opts := robot.MailCheckOptions{
			Client:        newAgentMailClient(mailProject),
			Project:       mailProject,
			Agent:         mailAgent,
			Thread:        mailThread,
			Status:        mailStatus,
			IncludeBodies: mailIncludeBodies,
			UrgentOnly:    mailUrgentOnly,
			Verbose:       mailVerbose,
			Limit:         resolveRobotMailCheckLimit(cmd),
			Offset:        mailOffset, // Pagination offset
			Since:         resolveRobotCASSSince(cmd),
			Until:         mailUntil, // Date filter
		}
		if err := robot.PrintMailCheck(opts); err != nil {
			recordRobotProcessExit(err)
		}
		return true
	}

	return false
}

// loadRobotMessageInput returns a robot prompt given inline (msgFlag) or via a
// file or stdin (fileFlag, "-" for stdin). The flag names only label errors;
// --robot-send (--msg/--msg-file) and --robot-spawn
// (--spawn-prompt/--spawn-prompt-file) share this reader.
func loadRobotMessageInput(msg, msgFile, msgFlag, fileFlag string) (string, error) {
	if msg != "" && msgFile != "" {
		return "", fmt.Errorf("use either %s or %s, not both", msgFlag, fileFlag)
	}
	if msgFile == "" {
		return msg, nil
	}

	// "--msg-file=-" reads the message from stdin (ntm-kd2s): command-safety
	// filters (dcg PreToolUse) match the full command string and cannot tell
	// executable text from message content, so a prompt that merely MENTIONS
	// a destructive command (e.g. instructions about `git reset --hard`)
	// blocked the whole dispatch. Passing the payload via stdin or a file
	// keeps it out of the scanned command line entirely.
	var f *os.File
	if msgFile == "-" {
		f = os.Stdin
	} else {
		var err error
		f, err = os.Open(msgFile)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", fileFlag, err)
		}
	}

	// Read up to 10MB + 1 byte to detect truncation
	limit := int64(10 * 1024 * 1024)
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		if f != os.Stdin {
			_ = f.Close()
		}
		return "", fmt.Errorf("read %s: %w", fileFlag, err)
	}
	if f != os.Stdin {
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close %s: %w", fileFlag, err)
		}
	}

	if int64(len(data)) > limit {
		return "", fmt.Errorf("%s content too large (max 10MB)", fileFlag)
	}

	if len(data) == 0 || strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("%s content is empty", fileFlag)
	}
	return string(data), nil
}

func resolveRobotSpawnTimeout(cmd *cobra.Command) string {
	if cmd != nil && (cmd.Flags().Changed("spawn-timeout") || cmd.Flags().Changed("ready-timeout")) {
		return robotSpawnTimeout
	}
	if cmd != nil && cmd.Flags().Changed("timeout") {
		return robotWaitTimeout
	}
	return robotSpawnTimeout
}

func resolveRobotSpawnStrategy(cmd *cobra.Command) string {
	return resolveRobotSharedFlag(cmd, "spawn-assign-strategy", robotSpawnStrategy, "strategy", robotAssignStrategy)
}

// parseRobotSpawnAgentFlag parses one robot spawn agent flag value using the
// same `count[:model[:effort]]` grammar as `ntm spawn --cc/--cod/...`
// (bd-rr8gn), including the `count:model@effort` shorthand for the agent types
// with a reasoning-effort knob. An empty value means "flag not set" (count 0).
// Plain integers keep their historical IntVar semantics: "0" (and negative
// values, rejected downstream by robot validation with the exact pre-existing
// message) remain bare counts.
func parseRobotSpawnAgentFlag(flagName, raw string, agentType AgentType) (AgentSpec, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return AgentSpec{Type: agentType}, nil
	}
	if count, err := strconv.Atoi(value); err == nil {
		return AgentSpec{Type: agentType, Count: count}, nil
	}
	spec, err := parseTypedAgentSpec(value, agentType)
	if err != nil {
		return AgentSpec{}, fmt.Errorf("invalid %s value %q: %w", flagName, raw, err)
	}
	return spec, nil
}

// parseRobotSpawnAgentSpecs parses every per-type robot spawn flag, returning
// the first grammar error it hits.
func parseRobotSpawnAgentSpecs() (map[AgentType]AgentSpec, error) {
	flags := []struct {
		name      string
		value     string
		agentType AgentType
	}{
		{name: "--spawn-cc", value: robotSpawnCC, agentType: AgentTypeClaude},
		{name: "--spawn-cod", value: robotSpawnCod, agentType: AgentTypeCodex},
		{name: "--spawn-gmi", value: robotSpawnGmi, agentType: AgentTypeGemini},
		{name: "--spawn-agy", value: robotSpawnAgy, agentType: AgentTypeAntigravity},
		{name: "--spawn-grok", value: robotSpawnGrok, agentType: AgentTypeGrok},
		{name: "--spawn-omp", value: robotSpawnOmp, agentType: AgentTypeOmp},
		{name: "--spawn-oc", value: robotSpawnOc, agentType: AgentTypeOpencode},
		{name: "--spawn-ollama", value: robotSpawnOllama, agentType: AgentTypeOllama},
		{name: "--spawn-aider", value: robotSpawnAider, agentType: AgentTypeAider},
		{name: "--spawn-cursor", value: robotSpawnCursor, agentType: AgentTypeCursor},
		{name: "--spawn-windsurf", value: robotSpawnWindsurf, agentType: AgentTypeWindsurf},
	}
	names := make([]string, 0, len(robotSpawnPlugins))
	for name := range robotSpawnPlugins {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if value := robotSpawnPlugins[name]; value != nil {
			flags = append(flags, struct {
				name      string
				value     string
				agentType AgentType
			}{"--spawn-" + name, *value, AgentType(name)})
		}
	}
	specs := make(map[AgentType]AgentSpec, len(flags))
	for _, flag := range flags {
		spec, err := parseRobotSpawnAgentFlag(flag.name, flag.value, flag.agentType)
		if err != nil {
			return nil, err
		}
		specs[flag.agentType] = spec
	}
	return specs, nil
}

func robotSpawnOptionsFromFlags(cmd *cobra.Command, readyTimeout time.Duration, reservationPaths []string, dryRun bool) (robot.SpawnOptions, error) {
	specs, err := parseRobotSpawnAgentSpecs()
	if err != nil {
		return robot.SpawnOptions{}, err
	}
	opts := robot.SpawnOptions{
		Session:       robotSpawn,
		Label:         robotSpawnLabel,
		ConfigPath:    selectedConfigPath(),
		RequireConfig: selectedConfigIsExplicit(),
		CCCount:       specs[AgentTypeClaude].Count,
		CodCount:      specs[AgentTypeCodex].Count,
		GmiCount:      specs[AgentTypeGemini].Count,
		AgyCount:      specs[AgentTypeAntigravity].Count,
		GrokCount:     specs[AgentTypeGrok].Count,
		OmpCount:      specs[AgentTypeOmp].Count,
		OcCount:       specs[AgentTypeOpencode].Count,
		OllamaCount:   specs[AgentTypeOllama].Count,
		AiderCount:    specs[AgentTypeAider].Count,
		CursorCount:   specs[AgentTypeCursor].Count,
		WindsurfCount: specs[AgentTypeWindsurf].Count,
		// Model/effort overrides from the count[:model[:effort]] specs. agy is
		// intentionally absent: its model is hard-pinned by config, and parsing
		// rejects any --spawn-agy model override before options are built.
		// Efforts flow only for the types whose
		// launch command consumes them (cc/cod/grok/omp), mirroring the CLI spawn
		// path where other types (gmi, oc) drop the hint at template-render time.
		CCModel:             specs[AgentTypeClaude].Model,
		CCReasoningEffort:   specs[AgentTypeClaude].ReasoningEffort,
		CodModel:            specs[AgentTypeCodex].Model,
		CodReasoningEffort:  specs[AgentTypeCodex].ReasoningEffort,
		GmiModel:            specs[AgentTypeGemini].Model,
		GrokModel:           specs[AgentTypeGrok].Model,
		GrokReasoningEffort: specs[AgentTypeGrok].ReasoningEffort,
		OmpModel:            specs[AgentTypeOmp].Model,
		OmpReasoningEffort:  specs[AgentTypeOmp].ReasoningEffort,
		OcModel:             specs[AgentTypeOpencode].Model,
		OllamaModel:         specs[AgentTypeOllama].Model,
		AiderModel:          specs[AgentTypeAider].Model,
		CursorModel:         specs[AgentTypeCursor].Model,
		WindsurfModel:       specs[AgentTypeWindsurf].Model,
		Preset:              robotSpawnPreset,
		NoUserPane:          robotSpawnNoUser,
		WorkingDir:          robotSpawnDir,
		WaitReady:           robotSpawnWait,
		ReadyTimeout:        readyTimeout,
		DryRun:              dryRun,
		Safety:              robotSpawnSafety,
		AssignWork:          robotSpawnAssignWork,
		AssignStrategy:      resolveRobotSpawnStrategy(cmd),
		CustomNames:         robot.ParseCustomNames(robotSpawnNames),
		RequireReservation:  robotRequireReservation,
		ReservationPaths:    append([]string(nil), reservationPaths...),
	}
	for name, raw := range robotSpawnPlugins {
		if raw == nil || strings.TrimSpace(*raw) == "" {
			continue
		}
		if opts.PluginAgents == nil {
			opts.PluginAgents = make(map[string]robot.PluginSpawnSpec)
		}
		spec := specs[AgentType(name)]
		opts.PluginAgents[name] = robot.PluginSpawnSpec{Count: spec.Count, Model: spec.Model, ReasoningEffort: spec.ReasoningEffort}
	}
	return opts, nil
}

// applyRobotSpawnPromptFlags resolves the initial prompt (--spawn-prompt or
// --spawn-prompt-file) and the prompt stagger (--spawn-stagger-mode and
// --spawn-stagger-delay over the [spawn] config defaults, flags winning) onto
// opts. Range and vocabulary checks stay in robot.GetSpawn so every caller of
// the spawn engine is held to the same contract.
func applyRobotSpawnPromptFlags(cmd *cobra.Command, cfg *config.Config, opts *robot.SpawnOptions) error {
	prompt, err := loadRobotMessageInput(robotSpawnPrompt, robotSpawnPromptFile, "--spawn-prompt", "--spawn-prompt-file")
	if err != nil {
		return err
	}
	modeSet := cmd != nil && cmd.Flags().Changed("spawn-stagger-mode")
	delaySet := cmd != nil && cmd.Flags().Changed("spawn-stagger-delay")
	delay := config.DefaultSpawnStaggerDelay
	if delaySet {
		delay, err = time.ParseDuration(strings.TrimSpace(robotSpawnStaggerDelay))
		if err != nil {
			return fmt.Errorf("invalid --spawn-stagger-delay %q: %w", robotSpawnStaggerDelay, err)
		}
	}
	mode, delay, err := resolveSpawnStaggerDefaults(cfg, strings.TrimSpace(robotSpawnStaggerMode), modeSet, delay, delaySet)
	if err != nil {
		return err
	}
	opts.Prompt = prompt
	opts.StaggerMode = mode
	opts.StaggerDelay = delay
	// --spawn-assign-work prompts are enriched like --robot-bulk-assign's:
	// --with-cass/--no-cass/--with-memory over [cass.context]/[memory].
	if opts.AssignWork {
		opts.PromptContext = resolvePromptContextOptions(robotSendWithCASS, robotSendNoCASS, robotSendWithMemory, cfg)
	}
	return nil
}

func parseRobotReservationPathsArg(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	paths := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for index, part := range parts {
		path := strings.TrimSpace(part)
		if path == "" {
			return nil, fmt.Errorf("invalid reservation path list %q: empty path at position %d", raw, index+1)
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths, nil
}
