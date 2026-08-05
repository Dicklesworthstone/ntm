// Package robot provides machine-readable output for AI agents.
// dialogs.go implements --robot-dialogs and --robot-answer-dialog
// (ntm-xcji): in-pane interactive dialogs (trust prompts, rate-limit
// options, usage overlays, paste limbo, destructive confirms) used to be
// resolved with blind raw keystrokes against memorized per-provider layouts
// ("option 2 on typical layout") — mispredict the layout and you press the
// wrong option, including accepting a destructive action. These verbs
// classify the dialog from the live capture, extract the actual option
// list, and answer by label with a hard policy: accept-side choices on a
// destructive confirm are refused outright.
package robot

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Dialog classes reported by the classifier.
const (
	DialogNone               = "none"
	DialogTrustPrompt        = "trust_prompt"
	DialogRateLimitOptions   = "rate_limit_options"
	DialogUsageOverlay       = "usage_overlay"
	DialogPasteLimbo         = "paste_limbo"
	DialogDestructiveConfirm = "destructive_confirm"
	DialogUnknown            = "unknown_dialog"
)

// DialogOption is one selectable entry extracted from a dialog.
type DialogOption struct {
	Number   int    `json:"number"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

// DialogState is the classification result for one pane.
type DialogState struct {
	Class    string         `json:"class"`
	Options  []DialogOption `json:"options,omitempty"`
	Evidence string         `json:"evidence,omitempty"`
}

// PaneDialog pairs a pane identity with its dialog state.
type PaneDialog struct {
	Pane      string      `json:"pane"`
	Target    string      `json:"target"`
	AgentType string      `json:"agent_type"`
	Dialog    DialogState `json:"dialog"`
}

// DialogsOutput is the structured output for --robot-dialogs.
type DialogsOutput struct {
	RobotResponse
	Session string       `json:"session"`
	Panes   []PaneDialog `json:"panes"`
}

// AnswerDialogOutput is the structured output for --robot-answer-dialog.
type AnswerDialogOutput struct {
	RobotResponse
	Session  string      `json:"session"`
	Pane     string      `json:"pane"`
	Target   string      `json:"target"`
	Before   DialogState `json:"before"`
	Choice   string      `json:"choice"`
	KeysSent []string    `json:"keys_sent,omitempty"`
	After    DialogState `json:"after"`
	// Resolved means the pane is CLEAR: no dialog is blocking it. It is not
	// "the dialog changed" — a destructive confirm that, on declining, presents
	// a follow-up usage overlay has changed class while the pane is still
	// blocked, and a caller told resolved:true proceeds to send work the new
	// dialog swallows.
	Resolved bool `json:"resolved"`
	// FollowUpDialog names the dialog class that REPLACED the one that was
	// answered, when answering surfaced a different dialog rather than
	// clearing the pane. It is the actionable signal a class change carries:
	// the caller can answer the next one.
	FollowUpDialog string `json:"follow_up_dialog,omitempty"`
}

// dialogSignature declares the substring patterns (case-insensitive, ALL
// required) that identify one dialog class for one agent family. The single
// table IS the provider dialog vocabulary — fixtures pin each row.
type dialogSignature struct {
	class       string
	agent       string // long agent name ("claude", "codex") or "*"
	patterns    []string
	needOptions bool // class requires extracted numbered options
}

var dialogSignatures = []dialogSignature{
	// Claude Code first-boot folder trust gate (live fixture 2026-08-04).
	{class: DialogTrustPrompt, agent: "*", patterns: []string{"trust"}, needOptions: true},
	// Claude rate-limit choice dialog (/rate-limit-options or auto-shown).
	{class: DialogRateLimitOptions, agent: "*", patterns: []string{"usage limit"}, needOptions: true},
	{class: DialogRateLimitOptions, agent: "*", patterns: []string{"rate limit"}, needOptions: true},
	// Usage overlay (claude /usage): a panel, not an option dialog.
	{class: DialogUsageOverlay, agent: "claude", patterns: []string{"current session", "usage"}},
	{class: DialogUsageOverlay, agent: "claude", patterns: []string{"esc to close", "usage"}},
}

// destructiveEvidence flags dialog bodies whose pending action is
// destructive; accept-side answers are refused for these.
var destructiveEvidence = regexp.MustCompile(`(?i)(rm -|rm\b.*-rf|force.?push|git push --force|delete|remove|reset --hard|drop table|overwrite)`)

// acceptSideLabel reports whether an option label accepts/proceeds.
func acceptSideLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, prefix := range []string{"yes", "allow", "proceed", "accept", "trust", "continue", "confirm", "run "} {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// declineSideLabel reports whether an option label declines/cancels.
func declineSideLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, prefix := range []string{"no", "don't", "dont", "cancel", "decline", "stop", "exit", "skip"} {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

var dialogOptionPattern = regexp.MustCompile(`^\s*(❯\s*)?(\d+)\.\s+(.+?)\s*$`)

// extractDialogOptions pulls numbered options (and the selected marker) from
// a capture. Only a compact trailing block of ≤9 options counts — transcript
// text with stray numbered lists is filtered by requiring the numbers to
// start at 1 and increment.
func extractDialogOptions(capture string) []DialogOption {
	var options []DialogOption
	for _, line := range strings.Split(capture, "\n") {
		m := dialogOptionPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		num := 0
		fmt.Sscanf(m[2], "%d", &num)
		options = append(options, DialogOption{
			Number:   num,
			Label:    strings.TrimSpace(m[3]),
			Selected: strings.TrimSpace(m[1]) != "",
		})
	}
	// Keep the LAST run of options numbered 1..n — the live dialog sits at
	// the bottom of the screen; anything before a numbering restart is
	// transcript history.
	start := 0
	for i, opt := range options {
		if opt.Number == 1 {
			start = i
		}
	}
	options = options[start:]
	for i, opt := range options {
		if opt.Number != i+1 {
			return nil
		}
	}
	if len(options) > 9 {
		return nil
	}
	return options
}

// classifyDialog inspects one pane capture and returns its dialog state.
func classifyDialog(capture string, agentType string) DialogState {
	lower := strings.ToLower(capture)
	options := extractDialogOptions(capture)

	// codex paste limbo: staged "[Pasted text ...]" sitting in the live
	// composer with no working footer — Enter was consumed by the paste.
	if agentType == "codex" && !strings.Contains(lower, "esc to interrupt") {
		if lines := strings.Split(capture, "\n"); len(lines) > 0 {
			for i := len(lines) - 1; i >= 0; i-- {
				idx := strings.Index(lines[i], "›")
				if idx < 0 {
					continue
				}
				if strings.Contains(lines[i][idx:], "[Pasted") {
					return DialogState{Class: DialogPasteLimbo, Evidence: strings.TrimSpace(lines[i])}
				}
				break
			}
		}
	}

	for _, sig := range dialogSignatures {
		if sig.agent != "*" && sig.agent != agentType {
			continue
		}
		matched := true
		for _, pattern := range sig.patterns {
			if !strings.Contains(lower, pattern) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if sig.needOptions && len(options) == 0 {
			continue
		}
		state := DialogState{Class: sig.class, Options: options, Evidence: firstDialogEvidenceLine(capture, sig.patterns)}
		// A trust/rate-limit style dialog whose body shows destructive
		// evidence is reclassified: the pending action dominates.
		if destructiveEvidence.MatchString(capture) && len(options) > 0 && sig.class != DialogUsageOverlay {
			state.Class = DialogDestructiveConfirm
		}
		return state
	}

	// Generic confirm dialog: numbered options with an accept/decline shape.
	if len(options) >= 2 {
		hasAccept, hasDecline := false, false
		for _, opt := range options {
			if acceptSideLabel(opt.Label) {
				hasAccept = true
			}
			if declineSideLabel(opt.Label) {
				hasDecline = true
			}
		}
		if hasAccept && hasDecline {
			if destructiveEvidence.MatchString(capture) {
				return DialogState{Class: DialogDestructiveConfirm, Options: options}
			}
			return DialogState{Class: DialogUnknown, Options: options}
		}
	}
	return DialogState{Class: DialogNone}
}

func firstDialogEvidenceLine(capture string, patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	for _, line := range strings.Split(capture, "\n") {
		if strings.Contains(strings.ToLower(line), patterns[0]) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// GetDialogs classifies the dialog state of every selected pane.
func GetDialogs(ctx context.Context, session string, selectors []string) (*DialogsOutput, error) {
	output := &DialogsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       strings.TrimSpace(session),
		Panes:         []PaneDialog{},
	}
	targets, multiWindow, failure := resolveLifecycleTargets(ctx, LifecycleOptions{Session: session, Panes: selectors}, "dialogs")
	if failure != nil {
		output.RobotResponse = *failure
		return output, nil
	}
	for _, pane := range targets {
		capture, err := tmux.CapturePaneVisibleContext(ctx, pane.ID)
		if err != nil {
			output.Panes = append(output.Panes, PaneDialog{
				Pane:      paneTargetKey(pane, multiWindow),
				Target:    pane.ID,
				AgentType: restartPaneAgentType(pane),
				Dialog:    DialogState{Class: DialogUnknown, Evidence: fmt.Sprintf("capture failed: %v", err)},
			})
			continue
		}
		output.Panes = append(output.Panes, PaneDialog{
			Pane:      paneTargetKey(pane, multiWindow),
			Target:    pane.ID,
			AgentType: restartPaneAgentType(pane),
			Dialog:    classifyDialog(capture, restartPaneAgentType(pane)),
		})
	}
	return output, nil
}

// AnswerDialogOptions configures --robot-answer-dialog.
type AnswerDialogOptions struct {
	Session string
	Panes   []string // must resolve to exactly one pane
	Choice  string   // decline | extra-usage | dismiss | option-K
}

var optionChoicePattern = regexp.MustCompile(`^option-(\d+)$`)

// resolveDialogAnswer maps a choice to concrete keys given the classified
// dialog, enforcing the destructive-confirm policy. Pure for testing.
func resolveDialogAnswer(state DialogState, choice string) ([]string, error) {
	choice = strings.TrimSpace(strings.ToLower(choice))
	if state.Class == DialogNone {
		return nil, fmt.Errorf("no dialog detected on the pane")
	}
	if m := optionChoicePattern.FindStringSubmatch(choice); m != nil {
		num := 0
		fmt.Sscanf(m[1], "%d", &num)
		var picked *DialogOption
		for i := range state.Options {
			if state.Options[i].Number == num {
				picked = &state.Options[i]
				break
			}
		}
		if picked == nil {
			return nil, fmt.Errorf("dialog has no option %d (options: %s)", num, describeOptions(state.Options))
		}
		if state.Class == DialogDestructiveConfirm && !declineSideLabel(picked.Label) {
			return nil, fmt.Errorf("POLICY_REFUSED: refusing to answer %q on a destructive confirm (option %d: %q); only decline-side answers are allowed", choice, num, picked.Label)
		}
		return []string{fmt.Sprintf("%d", num), "Enter"}, nil
	}
	switch choice {
	case "decline":
		for _, opt := range state.Options {
			if declineSideLabel(opt.Label) {
				return []string{fmt.Sprintf("%d", opt.Number), "Enter"}, nil
			}
		}
		if state.Class == DialogUsageOverlay || state.Class == DialogPasteLimbo {
			return []string{"Escape"}, nil
		}
		return nil, fmt.Errorf("no decline-side option found (options: %s)", describeOptions(state.Options))
	case "extra-usage":
		if state.Class != DialogRateLimitOptions {
			return nil, fmt.Errorf("extra-usage only applies to rate_limit_options dialogs (found %s)", state.Class)
		}
		for _, opt := range state.Options {
			if strings.Contains(strings.ToLower(opt.Label), "extra") {
				return []string{fmt.Sprintf("%d", opt.Number), "Enter"}, nil
			}
		}
		return nil, fmt.Errorf("no extra-usage option found (options: %s)", describeOptions(state.Options))
	case "dismiss":
		// Escape is the universal dismiss; never selects an option.
		return []string{"Escape"}, nil
	default:
		return nil, fmt.Errorf("unknown choice %q; use decline, extra-usage, dismiss, or option-K", choice)
	}
}

func describeOptions(options []DialogOption) string {
	if len(options) == 0 {
		return "none extracted"
	}
	parts := make([]string, 0, len(options))
	for _, opt := range options {
		parts = append(parts, fmt.Sprintf("%d=%q", opt.Number, opt.Label))
	}
	return strings.Join(parts, ", ")
}

// AnswerDialog classifies the pane, maps the choice under policy, sends the
// keys, and re-classifies to verify the dialog resolved.
func AnswerDialog(ctx context.Context, opts AnswerDialogOptions) (*AnswerDialogOutput, error) {
	output := &AnswerDialogOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       strings.TrimSpace(opts.Session),
		Choice:        strings.TrimSpace(opts.Choice),
	}
	if len(opts.Panes) == 0 {
		output.RobotResponse = NewErrorResponse(fmt.Errorf("--panes is required"), ErrCodeInvalidFlag,
			"Answering a dialog targets exactly one pane: --panes=SELECTOR")
		return output, nil
	}
	targets, multiWindow, failure := resolveLifecycleTargets(ctx, LifecycleOptions{Session: opts.Session, Panes: opts.Panes}, "answer-dialog")
	if failure != nil {
		output.RobotResponse = *failure
		return output, nil
	}
	if len(targets) != 1 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("--robot-answer-dialog requires exactly one target pane, resolved %d", len(targets)),
			ErrCodeInvalidFlag,
			"Narrow --panes to a single selector",
		)
		return output, nil
	}
	pane := targets[0]
	output.Pane = paneTargetKey(pane, multiWindow)
	output.Target = pane.ID

	capture, err := tmux.CapturePaneVisibleContext(ctx, pane.ID)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to capture the pane")
		return output, nil
	}
	output.Before = classifyDialog(capture, restartPaneAgentType(pane))

	keys, err := resolveDialogAnswer(output.Before, opts.Choice)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag,
			"Use --robot-dialogs to inspect the dialog class and extracted options first")
		return output, nil
	}
	for i, key := range keys {
		if i > 0 {
			select {
			case <-ctx.Done():
				output.RobotResponse = NewErrorResponse(ctx.Err(), ErrCodeTimeout, "Retry after the cancellation clears")
				return output, nil
			case <-time.After(150 * time.Millisecond):
			}
		}
		if err := tmux.DefaultClient.RunSilentContext(ctx, "send-keys", "-t", pane.ID, key); err != nil {
			output.RobotResponse = NewErrorResponse(fmt.Errorf("send key %q: %w", key, err), ErrCodeInternalError, "Check tmux pane state")
			return output, nil
		}
		output.KeysSent = append(output.KeysSent, key)
	}

	select {
	case <-ctx.Done():
	case <-time.After(700 * time.Millisecond):
	}
	if after, err := tmux.CapturePaneVisibleContext(ctx, pane.ID); err == nil {
		output.After = classifyDialog(after, restartPaneAgentType(pane))
	} else {
		output.After = DialogState{Class: DialogUnknown, Evidence: fmt.Sprintf("post-answer capture failed: %v", err)}
	}
	applyDialogResolution(output)
	return output, nil
}

// applyDialogResolution decides whether answering actually cleared the pane.
//
// Resolved means the pane is CLEAR. Accepting "the class merely changed"
// reported success while a modal was still blocking the pane: declining a
// destructive confirm can surface a follow-up dialog, and the caller — told
// resolved:true, success:true — then sent work that the new dialog swallowed.
// That is exactly the success-without-verified-effect failure the post-action
// verification contract (ntm-epu6) exists to eliminate.
//
// A class change is still useful information; it just is not resolution, so it
// is reported separately as the follow-up to answer next.
func applyDialogResolution(output *AnswerDialogOutput) {
	output.Resolved = output.After.Class == DialogNone
	if output.Resolved {
		return
	}

	output.Success = false
	output.ErrorCode = ErrCodeInternalError
	if output.After.Class != output.Before.Class {
		output.FollowUpDialog = output.After.Class
		output.Error = fmt.Sprintf("answering surfaced a follow-up dialog (%s); the pane is still blocked", output.After.Class)
		output.Hint = "Answer the follow-up dialog with --robot-answer-dialog --choice, then re-check with --robot-dialogs"
		return
	}
	output.Error = "dialog still present after the answer"
	output.Hint = "Re-run --robot-dialogs; the dialog may need a different choice"
}

// PrintDialogs prints the per-pane dialog classification.
func PrintDialogs(ctx context.Context, session string, selectors []string) error {
	output, err := GetDialogs(ctx, session, selectors)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot dialogs failed")
}

// PrintAnswerDialog answers a dialog and prints the structured result.
func PrintAnswerDialog(ctx context.Context, opts AnswerDialogOptions) error {
	output, err := AnswerDialog(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot answer-dialog failed")
}
