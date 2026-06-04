package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/codex"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// newCodexCmd creates the "codex" command group. It hosts Codex-specific
// pane-control subcommands. The foundational subcommand is "palette-state",
// which reads and classifies the state of a Codex pane's slash-command / goal
// palette (NTM issue #166). Other Codex pane-control subcommands (#165, #167-169)
// build on this classifier.
func newCodexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Codex agent pane-control commands",
		Long: `Commands for inspecting and controlling Codex (cod) agent panes.

Codex presents transient TUI overlays — a slash-command palette, a goal/plan
prime indicator, and modal dialogs. These commands let agents and tooling read
that overlay state before driving the pane with keystrokes.

Examples:
  ntm codex palette-state --session myproject --pane 1
  ntm codex palette-state --session myproject --pane 1 --json
  ntm codex preflight --session myproject --pane 1 --json`,
	}

	cmd.AddCommand(newCodexPaletteStateCmd())
	cmd.AddCommand(newCodexPreflightCmd())
	cmd.AddCommand(newCodexReplaceGoalCmd())
	cmd.AddCommand(newCodexWaitGoalEngagedCmd())

	return cmd
}

// resolveCodexPane resolves (session, pane) to a live tmux pane and captures its
// VISIBLE screen, returning the pane plus a fresh capture. It centralizes the
// session/pane validation shared by every goal-lifecycle codex subcommand.
//
// It deliberately captures only the visible screen (not scrollback): transient
// TUI state — the live status bar, the "Working (...esc to interrupt)" footer,
// the replace-goal modal — is always on-screen, whereas a deep scrollback
// capture can resurrect stale footers (e.g. an MCP "esc to interrupt" startup
// line) that no longer reflect the pane's real state. The `lines` parameter is
// accepted for signature symmetry with preflight/palette-state but, when <= 0 or
// equal to the full-context default, the visible-screen capture is used.
//
// A non-nil error is already wrapped as a robot error (its JSON envelope emitted).
func resolveCodexPane(session string, pane, lines int) (*tmux.Pane, string, error) {
	if session == "" {
		return nil, "", robot.RobotError(
			fmt.Errorf("session is required"),
			robot.ErrCodeInvalidFlag,
			"Pass the session positionally or via --session; use 'ntm list' to see sessions",
		)
	}
	if pane < 0 {
		return nil, "", robot.RobotError(
			fmt.Errorf("--pane must be >= 0, got %d", pane),
			robot.ErrCodeInvalidFlag,
			"Pass a non-negative pane index, e.g. --pane 1",
		)
	}
	if !tmux.SessionExists(session) {
		return nil, "", robot.RobotError(
			fmt.Errorf("session '%s' not found", session),
			robot.ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, "", robot.RobotError(err, robot.ErrCodeInternalError, "Could not enumerate panes for the session")
	}
	var target *tmux.Pane
	for i := range panes {
		if panes[i].Index == pane {
			target = &panes[i]
			break
		}
	}
	if target == nil {
		return nil, "", robot.RobotError(
			fmt.Errorf("pane %d not found in session '%s'", pane, session),
			robot.ErrCodePaneNotFound,
			"Use 'ntm status <session>' to see available pane indices",
		)
	}
	var content string
	if lines <= 0 || lines >= tmux.LinesFullContext {
		content, err = tmux.CapturePaneVisible(target.ID)
	} else {
		content, err = tmux.CapturePaneOutput(target.ID, lines)
	}
	if err != nil {
		return nil, "", robot.RobotError(err, robot.ErrCodeInternalError, "Failed to capture pane content")
	}
	return target, content, nil
}

// CodexPreflightResult is the JSON output for "ntm codex preflight".
//
// It embeds the standard robot envelope and adds the goal-readiness verdict
// (state + recommended action + reason) plus the same capture provenance shape
// as PaletteStateResult, so the two codex robot subcommands are consistent.
type CodexPreflightResult struct {
	robot.RobotResponse

	// State is the classified preflight state (see internal/codex.PreflightState).
	State string `json:"state"`

	// RecommendedAction is the closed-set action mapped from State
	// (proceed / respawn / alternate_pane / wait / refuse).
	RecommendedAction string `json:"recommended_action"`

	// Reason is a human-readable explanation of the verdict.
	Reason string `json:"reason"`

	// Session is the resolved tmux session name.
	Session string `json:"session"`

	// Pane is the pane index that was inspected.
	Pane int `json:"pane"`

	// ProvenanceHash is the sha256 (hex) of the exact captured content classified.
	ProvenanceHash string `json:"provenance_hash"`

	// CapturedLines is the number of lines in the captured content.
	CapturedLines int `json:"captured_lines"`

	// MarkersMatched lists the marker substrings that selected the state.
	// Always present (empty array, not null).
	MarkersMatched []string `json:"markers_matched"`

	// TimestampSource documents how the response timestamp was derived.
	TimestampSource string `json:"timestamp_source"`
}

func newCodexPreflightCmd() *cobra.Command {
	var (
		session    string
		pane       int
		jsonOutput bool
		lines      int
	)

	cmd := &cobra.Command{
		Use:   "preflight [session]",
		Short: "Classify a Codex pane's readiness for goal work (goal-lifecycle preflight)",
		Long: `Capture a Codex agent pane and classify its readiness to receive goal work.

This is the first layer of the Codex goal-lifecycle cluster (NTM #167). It is a
SUPERSET of 'ntm codex palette-state': where palette-state answers "which overlay
is open" for keystroke routing, preflight answers "is it safe to drive this pane
toward a goal, and if not, what to do instead".

The pane content is captured, hashed (sha256 for provenance), and classified into
one of:

  codex-live               Live Codex at a quiescent prompt          -> proceed
  goal-completed           Codex finished a goal, back at the prompt  -> proceed
  goal-in-progress         Codex is actively working a task          -> wait
  background-terminal-wait Blocked on a background/long command      -> wait
  replace-goal-dialog      A replace/overwrite-goal modal is open    -> refuse
  usage-limit              Account hit a usage/rate/quota limit      -> respawn
  shell-no-codex           Bare shell, Codex not running here        -> alternate_pane
  stale-scrollback         Empty/trivial capture, nothing to trust   -> refuse
  unknown                  No known marker matched                   -> refuse

The session may be passed positionally ('ntm codex preflight myproject') or via
--session, matching the other codex subcommands.

Examples:
  ntm codex preflight --session myproject --pane 1
  ntm codex preflight myproject --pane 1 --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			emitJSON := jsonOutput || IsJSONOutput()

			// Accept session positionally (consistent with other ntm commands)
			// while still honoring --session. An explicit positional arg wins.
			if len(args) == 1 && args[0] != "" {
				session = args[0]
			}

			if session == "" {
				return robot.RobotError(
					fmt.Errorf("session is required"),
					robot.ErrCodeInvalidFlag,
					"Pass the session positionally ('ntm codex preflight <session>') or via --session; use 'ntm list' to see sessions",
				)
			}
			if pane < 0 {
				return robot.RobotError(
					fmt.Errorf("--pane must be >= 0, got %d", pane),
					robot.ErrCodeInvalidFlag,
					"Pass a non-negative pane index, e.g. --pane 1",
				)
			}

			if !tmux.SessionExists(session) {
				return robot.RobotError(
					fmt.Errorf("session '%s' not found", session),
					robot.ErrCodeSessionNotFound,
					"Use 'ntm list' to see available sessions",
				)
			}

			panes, err := tmux.GetPanes(session)
			if err != nil {
				return robot.RobotError(err, robot.ErrCodeInternalError, "Could not enumerate panes for the session")
			}

			var target *tmux.Pane
			for i := range panes {
				if panes[i].Index == pane {
					target = &panes[i]
					break
				}
			}
			if target == nil {
				return robot.RobotError(
					fmt.Errorf("pane %d not found in session '%s'", pane, session),
					robot.ErrCodePaneNotFound,
					"Use 'ntm status <session>' to see available pane indices",
				)
			}

			content, err := tmux.CapturePaneOutput(target.ID, lines)
			if err != nil {
				return robot.RobotError(err, robot.ErrCodeInternalError, "Failed to capture pane content")
			}

			// Provenance: hash the exact bytes we classify (same pattern as
			// palette-state, so output is reproducible/auditable).
			sum := sha256.Sum256([]byte(content))
			provenanceHash := hex.EncodeToString(sum[:])

			verdict := codex.Preflight(content)

			markers := verdict.MarkersMatched
			if markers == nil {
				markers = []string{}
			}

			result := CodexPreflightResult{
				RobotResponse:     robot.NewRobotResponse(true),
				State:             verdict.State.String(),
				RecommendedAction: verdict.Action.String(),
				Reason:            verdict.Reason,
				Session:           session,
				Pane:              pane,
				ProvenanceHash:    provenanceHash,
				CapturedLines:     countCapturedLines(content),
				MarkersMatched:    markers,
				TimestampSource:   "capture_walltime",
			}

			if emitJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("Codex Preflight\n")
			fmt.Printf("===============\n\n")
			fmt.Printf("Session: %s\n", result.Session)
			fmt.Printf("Pane:    %d\n", result.Pane)
			fmt.Printf("State:   %s\n", result.State)
			fmt.Printf("Action:  %s\n", result.RecommendedAction)
			fmt.Printf("Reason:  %s\n", result.Reason)
			fmt.Printf("Captured: %d lines (sha256 %s)\n", result.CapturedLines, result.ProvenanceHash[:16])
			if len(result.MarkersMatched) > 0 {
				fmt.Printf("Markers: %s\n", strings.Join(result.MarkersMatched, ", "))
			} else {
				fmt.Printf("Markers: (none)\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Target tmux session name (or pass positionally)")
	cmd.Flags().IntVar(&pane, "pane", 0, "Target pane index within the session")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().IntVar(&lines, "lines", tmux.LinesFullContext, "Number of pane lines to capture for classification")

	return cmd
}

// PaletteStateResult is the JSON output for "ntm codex palette-state".
//
// It embeds the standard robot envelope (success/timestamp/version/error) and
// adds the classifier result plus capture provenance so agents can both trust
// and reproduce the classification.
type PaletteStateResult struct {
	robot.RobotResponse

	// State is the classified Codex pane state (see internal/codex.PaletteState).
	State string `json:"state"`

	// Session is the resolved tmux session name.
	Session string `json:"session"`

	// Pane is the pane index that was inspected.
	Pane int `json:"pane"`

	// ProvenanceHash is the sha256 (hex) of the exact captured content that was
	// classified. It lets a caller verify the classification was made over the
	// same bytes they captured.
	ProvenanceHash string `json:"provenance_hash"`

	// CapturedLines is the number of lines in the captured content.
	CapturedLines int `json:"captured_lines"`

	// MarkersMatched lists the marker substrings that selected the state.
	// Always present (empty array, not null) so agents can iterate safely.
	MarkersMatched []string `json:"markers_matched"`

	// TimestampSource documents how the response timestamp was derived. For this
	// command the classification is point-in-time over a fresh capture, so the
	// timestamp reflects the local wall clock at capture time.
	TimestampSource string `json:"timestamp_source"`
}

func newCodexPaletteStateCmd() *cobra.Command {
	var (
		session    string
		pane       int
		jsonOutput bool
		lines      int
	)

	cmd := &cobra.Command{
		Use:   "palette-state",
		Short: "Classify the state of a Codex pane's slash/goal palette",
		Long: `Capture a Codex agent pane and classify its command/goal palette state.

The pane content is captured, hashed (sha256 for provenance), and matched
against a centralized table of Codex TUI markers to produce one of:

  idle                 Quiescent Codex prompt, no overlay open
  slash_palette_open   The "/" slash-command palette is open
  goal_palette_primed  A goal/plan prompt is primed but not submitted
  dialog_open          A modal dialog (approval/confirmation) is open
  unknown              No known marker matched

This is the foundational read used by the other Codex pane-control commands.

Examples:
  ntm codex palette-state --session myproject --pane 1
  ntm codex palette-state --session myproject --pane 1 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			emitJSON := jsonOutput || IsJSONOutput()

			if session == "" {
				return robot.RobotError(
					fmt.Errorf("--session is required"),
					robot.ErrCodeInvalidFlag,
					"Pass --session <name>; use 'ntm list' to see available sessions",
				)
			}
			if pane < 0 {
				return robot.RobotError(
					fmt.Errorf("--pane must be >= 0, got %d", pane),
					robot.ErrCodeInvalidFlag,
					"Pass a non-negative pane index, e.g. --pane 1",
				)
			}

			if !tmux.SessionExists(session) {
				return robot.RobotError(
					fmt.Errorf("session '%s' not found", session),
					robot.ErrCodeSessionNotFound,
					"Use 'ntm list' to see available sessions",
				)
			}

			panes, err := tmux.GetPanes(session)
			if err != nil {
				return robot.RobotError(err, robot.ErrCodeInternalError, "Could not enumerate panes for the session")
			}

			var target *tmux.Pane
			for i := range panes {
				if panes[i].Index == pane {
					target = &panes[i]
					break
				}
			}
			if target == nil {
				return robot.RobotError(
					fmt.Errorf("pane %d not found in session '%s'", pane, session),
					robot.ErrCodePaneNotFound,
					"Use 'ntm status <session>' to see available pane indices",
				)
			}

			content, err := tmux.CapturePaneOutput(target.ID, lines)
			if err != nil {
				return robot.RobotError(err, robot.ErrCodeInternalError, "Failed to capture pane content")
			}

			// Provenance: hash the exact bytes we classify.
			sum := sha256.Sum256([]byte(content))
			provenanceHash := hex.EncodeToString(sum[:])

			classification := codex.Classify(content)

			markers := classification.MarkersMatched
			if markers == nil {
				markers = []string{}
			}

			result := PaletteStateResult{
				RobotResponse:   robot.NewRobotResponse(true),
				State:           classification.State.String(),
				Session:         session,
				Pane:            pane,
				ProvenanceHash:  provenanceHash,
				CapturedLines:   countCapturedLines(content),
				MarkersMatched:  markers,
				TimestampSource: "capture_walltime",
			}

			if emitJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Human-readable output (dual-mode, matching other commands).
			fmt.Printf("Codex Palette State\n")
			fmt.Printf("===================\n\n")
			fmt.Printf("Session: %s\n", result.Session)
			fmt.Printf("Pane:    %d\n", result.Pane)
			fmt.Printf("State:   %s\n", result.State)
			fmt.Printf("Captured: %d lines (sha256 %s)\n", result.CapturedLines, result.ProvenanceHash[:16])
			if len(result.MarkersMatched) > 0 {
				fmt.Printf("Markers: %s\n", strings.Join(result.MarkersMatched, ", "))
			} else {
				fmt.Printf("Markers: (none)\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Target tmux session name (required)")
	cmd.Flags().IntVar(&pane, "pane", 0, "Target pane index within the session")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().IntVar(&lines, "lines", tmux.LinesFullContext, "Number of pane lines to capture for classification")

	return cmd
}

// CodexReplaceGoalResult is the JSON receipt for `ntm codex replace-goal` (#168).
type CodexReplaceGoalResult struct {
	robot.RobotResponse

	Session string `json:"session"`
	Pane    int    `json:"pane"`

	// SelectedOption is the option actually selected: "replace", "cancel", or
	// "none" when the command refused to act.
	SelectedOption string `json:"selected_option"`
	// DialogBefore is the detected dialog state before acting.
	DialogBefore string `json:"dialog_before"`
	// DialogAfter is the detected dialog state after acting (or after refusal).
	DialogAfter string `json:"dialog_after"`
	// RefusalReason is set (non-empty) when the command refused to act; empty on
	// a successful selection.
	RefusalReason string `json:"refusal_reason"`
	// OldGoalClosedProof records whether the #168 old-goal-closed proof held.
	OldGoalClosedProof bool `json:"old_goal_closed_proof"`
	// NewObjective is the parsed "New objective:" text from the modal, if any.
	NewObjective string `json:"new_objective,omitempty"`
	// MarkersMatched lists the replace-goal markers that fired.
	MarkersMatched []string `json:"markers_matched"`
	// ProvenanceHash is the sha256 of the pre-action capture.
	ProvenanceHash string `json:"provenance_hash"`
}

func newCodexReplaceGoalCmd() *cobra.Command {
	var (
		session    string
		pane       int
		selectArg  string
		jsonOutput bool
		lines      int
	)

	cmd := &cobra.Command{
		Use:   "replace-goal [session]",
		Short: "Safely resolve the Codex \"Replace goal?\" dialog (#168)",
		Long: `Detect and resolve Codex's "Replace goal?" modal with provenance and safety gates.

When you set a new /goal while one is already active, Codex opens a modal:

  Replace goal?
  New objective: <text>
  › 1. Replace current goal  Set the new objective and start it now
    2. Cancel                Keep the current goal
  Press enter to confirm or esc to go back

This command reuses the grounded replace-goal classifier (PreflightReplaceGoalDialog)
to detect that modal, then:

  --select replace   Selects option 1 (Replace) — ONLY when the modal is fully
                     rendered AND there is affirmative old-goal-closed proof
                     (the modal offers to "keep the current goal", which only
                     renders when a current goal exists). Refuses otherwise.
  --select cancel    Selects option 2 (Cancel) — keep the current goal (Esc).

It NEVER sends blind keystrokes at an ambiguous/absent dialog; it refuses with a
clear JSON reason instead.

Examples:
  ntm codex replace-goal myproject --pane 1 --select replace --json
  ntm codex replace-goal myproject --pane 1 --select cancel --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}
			emitJSON := jsonOutput || IsJSONOutput()
			if len(args) == 1 && args[0] != "" {
				session = args[0]
			}

			sel := codex.ReplaceGoalSelection(strings.ToLower(strings.TrimSpace(selectArg)))
			if sel != codex.ReplaceGoalReplace && sel != codex.ReplaceGoalCancel {
				return robot.RobotError(
					fmt.Errorf("--select must be 'replace' or 'cancel', got %q", selectArg),
					robot.ErrCodeInvalidFlag,
					"Pass --select replace or --select cancel",
				)
			}

			target, content, err := resolveCodexPane(session, pane, lines)
			if err != nil {
				return err
			}

			dlg := codex.DetectReplaceGoalDialog(content)
			sum := sha256.Sum256([]byte(content))
			markers := dlg.MarkersMatched
			if markers == nil {
				markers = []string{}
			}

			res := CodexReplaceGoalResult{
				RobotResponse:      robot.NewRobotResponse(false),
				Session:            session,
				Pane:               pane,
				SelectedOption:     "none",
				DialogBefore:       replaceDialogStateLabel(dlg),
				DialogAfter:        replaceDialogStateLabel(dlg),
				OldGoalClosedProof: dlg.OldGoalClosed,
				NewObjective:       dlg.NewObjective,
				MarkersMatched:     markers,
				ProvenanceHash:     hex.EncodeToString(sum[:]),
			}

			// emit prints the single canonical receipt. On a refusal/failure it
			// records error_code/hint/error on the embedded envelope and returns
			// errJSONFailure (JSON mode) so Execute exits non-zero WITHOUT printing
			// a second envelope. The RefusalReason field is set by the caller.
			emit := func(failErr error, code, hint string) error {
				if failErr != nil {
					res.Success = false
					res.Error = failErr.Error()
					res.ErrorCode = code
					res.Hint = hint
				}
				if emitJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					_ = enc.Encode(res)
					if failErr != nil {
						return errors.Join(errJSONFailure, failErr)
					}
					return nil
				}
				fmt.Printf("Codex Replace-Goal\n==================\n\n")
				fmt.Printf("Session: %s  Pane: %d\n", res.Session, res.Pane)
				fmt.Printf("Dialog before: %s\n", res.DialogBefore)
				fmt.Printf("Selected: %s\n", res.SelectedOption)
				fmt.Printf("Dialog after: %s\n", res.DialogAfter)
				if res.RefusalReason != "" {
					fmt.Printf("Refused: %s\n", res.RefusalReason)
				}
				return failErr
			}

			// Refuse if no replace-goal modal is present.
			if !dlg.Present {
				res.RefusalReason = "no replace-goal dialog detected on the pane; refusing to send blind keystrokes"
				return emit(
					fmt.Errorf("replace-goal dialog not present"),
					robot.ErrCodeResourceBusy,
					"Only run this when Codex is showing the 'Replace goal?' modal",
				)
			}
			// Refuse if the modal is not fully rendered/interactive.
			if !dlg.Interactive {
				res.RefusalReason = "replace-goal modal is present but not yet interactive (no confirm affordance); re-capture and retry"
				return emit(
					fmt.Errorf("replace-goal dialog ambiguous/not interactive"),
					robot.ErrCodeResourceBusy,
					"Wait for the 'Press enter to confirm' line to render before selecting",
				)
			}

			if sel == codex.ReplaceGoalCancel {
				// Cancel == keep the current goal == Esc (sent as a key, not literal).
				if err := tmux.SendNamedKey(target.ID, "Escape"); err != nil {
					res.RefusalReason = fmt.Sprintf("failed to send Escape: %v", err)
					return emit(err, robot.ErrCodePromptSendFailed, "tmux send-keys Escape failed")
				}
				res.SelectedOption = "cancel"
				res.Success = true
				res.DialogAfter = replaceDialogStateLabel(codex.DetectReplaceGoalDialog(recapturePane(target.ID, lines)))
				return emit(nil, "", "")
			}

			// --select replace: require old-goal-closed proof.
			if !dlg.OldGoalClosed {
				res.RefusalReason = "no old-goal-closed proof (modal did not offer to keep a current goal); refusing to Replace"
				return emit(
					fmt.Errorf("old-goal-closed proof absent"),
					robot.ErrCodeResourceBusy,
					"Replace is only selected when there is affirmative proof a current goal is being replaced",
				)
			}

			// Option 1 is the default highlighted choice; Enter confirms Replace.
			// Send "1" to be explicit about the selection, then Enter to confirm.
			if err := tmux.SendKeys(target.ID, "1", false); err != nil {
				res.RefusalReason = fmt.Sprintf("failed to select option 1: %v", err)
				return emit(err, robot.ErrCodePromptSendFailed, "tmux send-keys '1' failed")
			}
			time.Sleep(tmux.DefaultEnterDelay)
			if err := tmux.SendKeys(target.ID, "", true); err != nil {
				res.RefusalReason = fmt.Sprintf("failed to confirm Replace: %v", err)
				return emit(err, robot.ErrCodePromptSendFailed, "tmux send-keys Enter (confirm) failed")
			}
			res.SelectedOption = "replace"
			res.Success = true
			res.DialogAfter = replaceDialogStateLabel(codex.DetectReplaceGoalDialog(recapturePane(target.ID, lines)))
			return emit(nil, "", "")
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Target tmux session name (or pass positionally)")
	cmd.Flags().IntVar(&pane, "pane", 0, "Target pane index within the session")
	cmd.Flags().StringVar(&selectArg, "select", "replace", "Dialog choice: 'replace' (option 1) or 'cancel' (option 2)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().IntVar(&lines, "lines", tmux.LinesFullContext, "Number of pane lines to capture for classification")

	return cmd
}

// replaceDialogStateLabel renders a compact label for a replace-goal dialog view.
func replaceDialogStateLabel(d codex.ReplaceGoalDialog) string {
	if !d.Present {
		return "no-dialog"
	}
	if d.Interactive {
		return "replace-goal-dialog-interactive"
	}
	return "replace-goal-dialog-rendering"
}

// recapturePane re-captures a pane's visible screen after a short settle delay,
// returning the content (or empty string on error). Used to populate dialog_after.
func recapturePane(paneID string, _ int) string {
	time.Sleep(500 * time.Millisecond)
	content, err := tmux.CapturePaneVisible(paneID)
	if err != nil {
		return ""
	}
	return content
}

// CodexWaitGoalEngagedResult is the JSON receipt for `ntm codex wait-goal-engaged` (#169).
type CodexWaitGoalEngagedResult struct {
	robot.RobotResponse

	Session string `json:"session"`
	Pane    int    `json:"pane"`

	// Outcome is the engagement outcome: engaged / engaging / dialog_stuck /
	// unconfirmed / respawn_required.
	Outcome string `json:"outcome"`
	// Reason explains the outcome.
	Reason string `json:"reason"`
	// CounterInit is the first observed pursuing-goal counter (-1 if never seen).
	CounterInit int `json:"counter_init"`
	// CounterSamples is the ordered list of observed pursuing-goal counters.
	CounterSamples []int `json:"counter_samples"`
	// CumulativeDelta is counter_last-counter_init when both were seen.
	CumulativeDelta int `json:"cumulative_delta"`
	// HookInit is true when a hook/Agent-Mail init signal was observed.
	HookInit bool `json:"hook_init"`
	// UsageLimitMixedState is true when usage-limit co-occurred with live/goal evidence.
	UsageLimitMixedState bool `json:"usage_limit_mixed_state"`
	// Samples is the number of polls performed.
	Samples int `json:"samples"`
	// TimedOut records whether the wait exhausted its budget.
	TimedOut bool `json:"timed_out"`
}

func newCodexWaitGoalEngagedCmd() *cobra.Command {
	var (
		session      string
		pane         int
		jsonOutput   bool
		lines        int
		timeoutSec   float64
		intervalMs   int
		counterDelta bool
	)

	cmd := &cobra.Command{
		Use:   "wait-goal-engaged [session]",
		Short: "Bounded wait for Codex goal engagement, classified (#169)",
		Long: `Poll a Codex pane until its goal visibly engages, with a bounded timeout.

Samples the grounded "Pursuing goal (Ns)" counter, the "Goal active" banner, and
hook-init (Agent Mail macro_start_session) signals on each poll, then classifies
the window into one of:

  engaged           A pursuing-goal counter advanced (or counter + hook-init) -> success
  engaging          Goal/working/banner present, no advancing counter yet     -> success
  dialog_stuck      A replace-goal modal is capturing input                   -> non-zero exit
  unconfirmed       Timed out with no engagement signal                       -> non-zero exit
  respawn_required  Usage limit hit, or Codex no longer live in this pane     -> non-zero exit

Emits a deterministic JSON receipt with counter_init, counter_samples,
cumulative_delta, hook_init, usage_limit_mixed_state, and outcome. Exits non-zero
for terminal non-engagement (dialog_stuck / unconfirmed / respawn_required).

Examples:
  ntm codex wait-goal-engaged myproject --pane 1 --json
  ntm codex wait-goal-engaged myproject --pane 1 --timeout 15 --counter-delta --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}
			emitJSON := jsonOutput || IsJSONOutput()
			_ = counterDelta // counter deltas are always computed; flag documents intent
			if len(args) == 1 && args[0] != "" {
				session = args[0]
			}
			if timeoutSec <= 0 {
				timeoutSec = 10
			}
			if intervalMs <= 0 {
				intervalMs = 500
			}

			// Resolve once up front to validate session/pane (also the first sample).
			target, firstContent, err := resolveCodexPane(session, pane, lines)
			if err != nil {
				return err
			}

			interval := time.Duration(intervalMs) * time.Millisecond
			deadline := time.Now().Add(time.Duration(timeoutSec * float64(time.Second)))

			var samples []codex.EngagementSample
			var counterSamples []int

			record := func(content string) (terminal bool) {
				s := codex.SampleEngagement(content)
				samples = append(samples, s)
				if s.PursuingPresent {
					counterSamples = append(counterSamples, s.PursuingCounter)
				}
				// Early-exit on terminal conditions so we don't burn the full budget.
				return s.ReplaceDialog || s.UsageLimit || (!s.CodexLive)
			}

			terminal := record(firstContent)
			for !terminal && time.Now().Before(deadline) {
				time.Sleep(interval)
				content, capErr := tmux.CapturePaneVisible(target.ID)
				if capErr != nil {
					continue
				}
				terminal = record(content)
			}

			timedOut := !terminal && !time.Now().Before(deadline)
			cls := codex.ClassifyEngagement(samples, timedOut)
			if counterSamples == nil {
				counterSamples = []int{}
			}

			nonEngaged := codex.EngagementExitNonZero(cls.Outcome)
			res := CodexWaitGoalEngagedResult{
				RobotResponse:        robot.NewRobotResponse(!nonEngaged),
				Session:              session,
				Pane:                 pane,
				Outcome:              cls.Outcome.String(),
				Reason:               cls.Reason,
				CounterInit:          cls.CounterInit,
				CounterSamples:       counterSamples,
				CumulativeDelta:      cls.CumulativeDelta,
				HookInit:             cls.HookInit,
				UsageLimitMixedState: cls.UsageLimitMixedState,
				Samples:              len(samples),
				TimedOut:             cls.TimedOut,
			}
			// Terminal non-engagement records the error on the single receipt and
			// exits non-zero without printing a second envelope.
			var nonEngagedErr error
			if nonEngaged {
				nonEngagedErr = fmt.Errorf("goal not engaged (outcome=%s)", cls.Outcome)
				res.Error = nonEngagedErr.Error()
				res.ErrorCode = robot.ErrCodeTimeout
				res.Hint = "Outcome is terminal non-engagement; see reason and outcome fields"
			}

			if emitJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(res)
				if nonEngaged {
					return errors.Join(errJSONFailure, nonEngagedErr)
				}
				return nil
			}

			fmt.Printf("Codex Wait-Goal-Engaged\n=======================\n\n")
			fmt.Printf("Session: %s  Pane: %d\n", res.Session, res.Pane)
			fmt.Printf("Outcome: %s\n", res.Outcome)
			fmt.Printf("Counters: init=%d samples=%v delta=%d\n", res.CounterInit, res.CounterSamples, res.CumulativeDelta)
			fmt.Printf("Hook init: %v  Usage-limit mixed: %v  Timed out: %v\n", res.HookInit, res.UsageLimitMixedState, res.TimedOut)
			fmt.Printf("Reason:  %s\n", res.Reason)
			return nonEngagedErr
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Target tmux session name (or pass positionally)")
	cmd.Flags().IntVar(&pane, "pane", 0, "Target pane index within the session")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().IntVar(&lines, "lines", tmux.LinesFullContext, "Number of pane lines to capture for classification")
	cmd.Flags().Float64Var(&timeoutSec, "timeout", 10, "Bounded wait timeout in seconds")
	cmd.Flags().IntVar(&intervalMs, "interval-ms", 500, "Poll interval in milliseconds")
	cmd.Flags().BoolVar(&counterDelta, "counter-delta", false, "Report pursuing-goal counter deltas (always computed; documents intent)")

	return cmd
}

// countCapturedLines counts the lines in captured pane content, ignoring a
// single trailing newline so a final blank line is not over-counted.
func countCapturedLines(content string) int {
	if content == "" {
		return 0
	}
	trimmed := strings.TrimSuffix(content, "\n")
	return strings.Count(trimmed, "\n") + 1
}
