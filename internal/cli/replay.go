package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const replayErrCodeConfirmationRequired = "CONFIRMATION_REQUIRED"

// ReplayResponse is the stable JSON result for the replay command.
type ReplayResponse struct {
	Success   bool     `json:"success"`
	Timestamp string   `json:"timestamp"`
	Session   string   `json:"session"`
	Targets   []string `json:"targets"`
	Delivered int      `json:"delivered"`
	Failed    int      `json:"failed"`
	DryRun    bool     `json:"dry_run"`
	Warnings  []string `json:"warnings"`
	ErrorCode string   `json:"error_code,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func newReplayResponse(dryRun bool) ReplayResponse {
	return ReplayResponse{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Targets:   []string{},
		DryRun:    dryRun,
		Warnings:  []string{},
	}
}

func outputReplayFailure(response ReplayResponse, jsonOutput bool, code string, err error) error {
	if err == nil {
		err = errors.New("replay failed")
	}
	response.Success = false
	response.ErrorCode = code
	response.Error = err.Error()
	if response.Targets == nil {
		response.Targets = []string{}
	}
	if response.Warnings == nil {
		response.Warnings = []string{}
	}
	if jsonOutput {
		return emitJSONFailureEnvelope(response)
	}
	return err
}

func replayFailureCode(err error, fallback string) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return robot.ErrCodeTimeout
	}
	return fallback
}

func resolveReplaySession(entrySession, sessionOverride string) (string, error) {
	session := strings.TrimSpace(entrySession)
	if sessionOverride != "" {
		session = strings.TrimSpace(sessionOverride)
	}
	if session == "" {
		return "", fmt.Errorf("history entry session is empty")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	return session, nil
}

func selectReplayEntry(entries []history.HistoryEntry, arg string, last bool) (*history.HistoryEntry, error) {
	if len(entries) == 0 {
		return nil, errors.New("no history entries found")
	}
	if last || strings.TrimSpace(arg) == "" {
		return &entries[len(entries)-1], nil
	}

	if idx, err := strconv.Atoi(arg); err == nil && idx > 0 {
		if idx > len(entries) {
			return nil, fmt.Errorf("index %d out of range (have %d entries)", idx, len(entries))
		}
		return &entries[len(entries)-idx], nil
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if strings.HasPrefix(entries[i].ID, arg) {
			return &entries[i], nil
		}
	}
	return nil, fmt.Errorf("no entry found matching ID prefix %q", arg)
}

func selectReplayTargets(panes []tmux.Pane, filter AgentFilter) []tmux.Pane {
	targets := make([]tmux.Pane, 0, len(panes))
	noFilter := filter.IsEmpty()
	for _, pane := range panes {
		if pane.IsServicePane() {
			continue
		}
		if filter.All || (noFilter && pane.Type.Canonical() != tmux.AgentUser) {
			targets = append(targets, pane)
			continue
		}
		if noFilter {
			continue
		}
		if matchesLegacySendTypeFilter(pane, filter.Claude, filter.Codex, filter.Gemini) || filter.Matches(pane.Type) {
			targets = append(targets, pane)
		}
	}
	return targets
}

func replayTargetKeys(panes, targets []tmux.Pane) []string {
	keys := make([]string, 0, len(targets))
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	for _, pane := range targets {
		keys = append(keys, pane.Ref().Canonical(multiWindow))
	}
	return keys
}

func replayFailedCount(targetCount, delivered int) int {
	failed := targetCount - delivered
	if failed < 0 {
		return 0
	}
	return failed
}

func replayHistoryWarnings(noHistory bool, session string, targetNames, targetAgentTypes []string, prompt string, appendEntry func(*history.HistoryEntry) error) []string {
	warnings := []string{}
	if noHistory {
		return warnings
	}
	entry := history.NewEntry(session, targetNames, prompt, history.SourceReplay)
	entry.SetAgentTypes(targetAgentTypes)
	entry.SetSuccess()
	if err := appendEntry(entry); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to log replay: %v", err))
	}
	return warnings
}

func newReplayCmd() *cobra.Command {
	var (
		filter          AgentFilter
		sessionOverride string
		edit            bool
		dryRun          bool
		noHistory       bool
		last            bool
		yes             bool
	)

	cmd := &cobra.Command{
		Use:   "replay [index|id]",
		Short: "Replay a prompt from history",
		Long: `Replay a previously sent prompt from history.

Arguments:
  - Number (1-N): Index from most recent (1 = last prompt)
  - String: ID prefix to match

Examples:
  ntm replay 1                    # Replay most recent prompt
  ntm replay --last               # Same as above
  ntm replay 3                    # Replay 3rd most recent
  ntm replay 01HXYZ               # Replay by ID prefix
  ntm replay 1 --edit             # Edit prompt before sending
  ntm replay 1 --dry-run          # Preview without sending
  ntm replay 1 --yes              # Send without an interactive confirmation
  ntm replay 1 --session=other    # Send to different session
  ntm replay 1 --cc               # Send to Claude agents only`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode := IsJSONOutput()
			response := newReplayResponse(dryRun)
			fail := func(code string, err error) error {
				return outputReplayFailure(response, jsonMode, replayFailureCode(err, code), err)
			}

			if err := cmd.Context().Err(); err != nil {
				return fail(robot.ErrCodeInternalError, err)
			}

			entries, err := history.ReadAll()
			if err != nil {
				return fail(robot.ErrCodeInternalError, fmt.Errorf("reading history: %w", err))
			}

			entryArg := ""
			if len(args) > 0 {
				entryArg = args[0]
			}
			entry, err := selectReplayEntry(entries, entryArg, last)
			if err != nil {
				return fail(robot.ErrCodeInvalidFlag, err)
			}

			session, err := resolveReplaySession(entry.Session, sessionOverride)
			if err != nil {
				return fail(robot.ErrCodeInvalidFlag, err)
			}
			response.Session = session

			prompt := entry.Prompt
			if edit {
				if jsonMode {
					return fail(robot.ErrCodeInvalidFlag, errors.New("--edit cannot be used with --json"))
				}
				edited, err := editPrompt(prompt)
				if err != nil {
					return fail(robot.ErrCodeInternalError, fmt.Errorf("editing prompt: %w", err))
				}
				prompt = edited
			}

			if !jsonMode {
				fmt.Printf("Replaying prompt from %s\n", entry.Timestamp.Format("2006-01-02 15:04:05"))
				fmt.Printf("Session: %s\n", session)
				fmt.Printf("Prompt: %s\n", truncatePrompt(prompt, 80))
			}

			if !dryRun {
				if jsonMode && !yes {
					return fail(replayErrCodeConfirmationRequired, errors.New("replay execution requires --yes in JSON mode"))
				}
				if !jsonMode && !yes && !confirm("Send this prompt?") {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			exists, err := tmux.SessionExistsContext(cmd.Context(), session)
			if err != nil {
				return fail(robot.ErrCodeInternalError, fmt.Errorf("checking replay session: %w", err))
			}
			if !exists {
				return fail(robot.ErrCodeSessionNotFound, fmt.Errorf("session %q not found", session))
			}

			panes, err := tmux.GetPanesContext(cmd.Context(), session)
			if err != nil {
				return fail(robot.ErrCodeInternalError, fmt.Errorf("getting panes: %w", err))
			}

			targets := selectReplayTargets(panes, filter)
			response.Targets = replayTargetKeys(panes, targets)
			if len(targets) == 0 {
				return fail(robot.ErrCodePaneNotFound, errors.New("no matching panes found"))
			}

			if dryRun {
				response.Success = true
				if jsonMode {
					return output.PrintJSON(response)
				}
				fmt.Println("\n(dry-run mode - not sending)")
				return nil
			}

			promptService, err := dispatchsvc.NewService(dispatchsvc.Ports{
				Redactor:  shellFinalMessageRedactor(activeShellDispatchRedactionConfig()),
				Protocols: shellDispatchProtocolPlanner{},
				Deliverer: dispatchsvc.TMUXDeliverer{},
				Lifecycle: dispatchsvc.LifecycleHooks{
					AfterReceipt: func(_ context.Context, delivery dispatchsvc.Delivery, receipt dispatchsvc.Receipt) {
						if receipt.Status == dispatchsvc.ReceiptDelivered {
							addTimelinePromptMarker(session, delivery.Target.Pane, delivery.Message)
						}
					},
				},
			})
			if err != nil {
				return fail(robot.ErrCodeInternalError, fmt.Errorf("preparing replay dispatch: %w", err))
			}
			result, err := dispatchReplayPrompt(cmd.Context(), promptService, session, panes, targets, prompt)
			response.Delivered = result.Delivered
			response.Failed = replayFailedCount(len(targets), result.Delivered)
			if err != nil || result.Delivered != len(targets) {
				if err == nil {
					err = fmt.Errorf("replaying prompt delivered to %d of %d panes", result.Delivered, len(targets))
				}
				return fail(robot.ErrCodePromptSendFailed, fmt.Errorf("replaying prompt: %w", err))
			}

			targetAgentTypes := make([]string, 0, len(targets))
			for _, p := range targets {
				targetAgentTypes = append(targetAgentTypes, p.Type.String())
			}

			response.Warnings = replayHistoryWarnings(noHistory, session, response.Targets, targetAgentTypes, prompt, history.Append)

			response.Success = true
			if jsonMode {
				return output.PrintJSON(response)
			}
			for _, warning := range response.Warnings {
				fmt.Printf("Warning: %s\n", warning)
			}
			fmt.Printf("Sent to %d pane(s)\n", response.Delivered)
			return nil
		},
	}

	cmd.Flags().BoolVar(&last, "last", false, "replay most recent prompt")
	bindAgentFilterFlags(cmd, &filter, "send to %s agents only")
	cmd.Flags().BoolVar(&filter.All, "all", false, "send to all panes")
	cmd.Flags().StringVar(&sessionOverride, "session", "", "override target session")
	cmd.Flags().BoolVar(&edit, "edit", false, "edit prompt before sending")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be sent without sending")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "don't log this replay to history")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "send without interactive confirmation")

	return cmd
}

func dispatchReplayPrompt(ctx context.Context, service *dispatchsvc.Service, session string, panes, targets []tmux.Pane, prompt string) (dispatchsvc.Result, error) {
	return service.Execute(ctx, dispatchsvc.Request{
		Session:       session,
		Panes:         panes,
		Selectors:     shellDispatchSelectors(targets),
		IncludeUser:   true,
		Message:       prompt,
		Submit:        true,
		StopOnFailure: true,
	})
}

// editPrompt opens the prompt in an editor and returns the modified content.
func editPrompt(original string) (string, error) {
	// Create temp file
	f, err := os.CreateTemp("", "ntm-prompt-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())

	// Write original content
	if _, err := f.WriteString(original); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

	// Run editor
	cmd, err := buildEditorCommandWithFallback(f.Name(), "vim")
	if err != nil {
		return "", fmt.Errorf("configuring editor: %w", err)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor failed: %w", err)
	}

	// Read modified content
	modified, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(modified)), nil
}
