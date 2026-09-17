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

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
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
// Number is the one-based DISPLAY ORDER, including on unnumbered menus. It
// identifies option-K; it is not necessarily a key the provider accepts.
type DialogOption struct {
	Number   int    `json:"number"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	Key      string `json:"key,omitempty"` // explicitly displayed hotkey, if any
}

// DialogState is the classification result for one pane.
type DialogState struct {
	Class     string         `json:"class"`
	Options   []DialogOption `json:"options,omitempty"`
	Evidence  string         `json:"evidence,omitempty"`
	InputMode string         `json:"input_mode,omitempty"` // numbered | cursor | hotkey
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
	needOptions bool // class requires extracted selectable options
}

var dialogSignatures = []dialogSignature{
	// Claude and Antigravity folder trust gates, numbered or cursor-driven.
	{class: DialogTrustPrompt, agent: "*", patterns: []string{"trust"}, needOptions: true},
	// Grok's workspace gate does not contain the word "trust" (GH#325).
	{class: DialogTrustPrompt, agent: "grok", patterns: []string{"may run or modify contents", "security risks"}, needOptions: true},
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

var (
	dialogOptionPattern = regexp.MustCompile(`^\s*([❯>]\s*)?(\d+)\.\s+(.+?)\s*$`)
	// A hotkey must be a separate column, not the last letter of a label.
	dialogHotkeyPattern = regexp.MustCompile(`^(.+?\S)[\t ]{2,}([a-zA-Z])\s*$`)
	dialogPlainLabel    = regexp.MustCompile(`(?i)^(yes\b|no\b|allow\b|proceed\b|accept\b|trust\b|continue\b|confirm\b|run\b|don't\b|dont\b|cancel\b|decline\b|stop\b|exit\b|skip\b|wait for\b|use extra\b|upgrade\b)`)
)

// extractDialogOptions accepts only the LAST compact option block. Requiring
// a live footer (or a displayed hotkey pair) for unnumbered menus prevents
// arbitrary yes/no prose, quoted dialogs, and old menus above a live composer
// from being treated as actionable. A numbered block must still run 1..n.
func extractDialogOptions(capture string) ([]DialogOption, string) {
	lines := strings.Split(strings.TrimRight(capture, "\r\n \t"), "\n")
	var options, candidate []DialogOption
	mode, candidateMode := "", ""
	end := -1
	for i, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		var option DialogOption
		kind := ""
		if match := dialogOptionPattern.FindStringSubmatch(line); match != nil {
			fmt.Sscanf(match[2], "%d", &option.Number)
			option.Label = strings.TrimSpace(match[3])
			option.Selected = strings.TrimSpace(match[1]) != ""
			kind = "numbered"
		} else {
			for _, marker := range []string{"❯", ">"} {
				if strings.HasPrefix(text, marker+" ") || strings.HasPrefix(text, marker+"\t") {
					option.Selected = true
					text = strings.TrimSpace(strings.TrimPrefix(text, marker))
					break
				}
			}
			option.Label = text
			kind = "cursor"
			if match := dialogHotkeyPattern.FindStringSubmatch(text); match != nil {
				option.Label = strings.TrimSpace(match[1])
				option.Key = match[2]
				kind = "hotkey"
			}
			if !dialogPlainLabel.MatchString(option.Label) {
				kind = ""
			}
		}
		if kind == "" {
			candidate = nil
			candidateMode = ""
			continue
		}
		if candidateMode != kind || (kind == "numbered" && option.Number == 1) {
			candidate = nil
		}
		candidateMode = kind
		if kind != "numbered" {
			option.Number = len(candidate) + 1
		}
		candidate = append(candidate, option)
		options, mode, end = candidate, kind, i
	}
	if len(options) == 0 || len(options) > 9 {
		return nil, ""
	}
	for i, option := range options {
		if option.Number != i+1 {
			return nil, ""
		}
	}
	// Only known dialog chrome may follow a menu. In particular, a new
	// composer, agent response, or closing Markdown fence invalidates it.
	footer := false
	for _, line := range lines[end+1:] {
		text := strings.ToLower(strings.TrimSpace(line))
		if text == "" {
			continue
		}
		if (strings.Contains(text, "enter") && strings.Contains(text, "confirm")) ||
			(strings.Contains(text, "esc") && strings.Contains(text, "cancel")) ||
			(strings.Contains(text, "↑/↓") && strings.Contains(text, "navigate")) {
			footer = true
			continue
		}
		return nil, ""
	}
	if mode == "numbered" {
		return options, mode
	}
	if len(options) < 2 || (mode == "cursor" && !footer) {
		return nil, ""
	}
	if mode == "hotkey" {
		seen := make(map[string]bool, len(options))
		for _, option := range options {
			key := strings.ToLower(option.Key)
			if seen[key] || option.Selected {
				return nil, ""
			}
			seen[key] = true
		}
	}
	return options, mode
}

// ompSelectorFooterRe matches the key-hint footer of an omp selector overlay
// (the /model picker renders "Enter assign roles · ↑/↓ providers · … · Esc
// close" as its last bordered row, and the overlay replaces the composer).
var ompSelectorFooterRe = regexp.MustCompile(`(?i)\besc\s+(?:close|cancel|back)\b`)

// ompSelectorScanLines bounds the footer search to the overlay's bottom rows.
const ompSelectorScanLines = 6

// classifyOmpDialog recognises omp-specific blocking states from a live
// capture (omp v18.2.3): a collapsed bracketed paste ("#N" token with its
// "+N lines" preview box) sitting in an idle composer, and a selector overlay
// that has replaced the composer. It reports ok=false when neither applies so
// the shared classifier runs.
func classifyOmpDialog(capture string) (DialogState, bool) {
	composer := agentpkg.ParseOmpComposer(capture)
	if composer.Found {
		if composer.PasteToken && !composer.Working() {
			return DialogState{Class: DialogPasteLimbo, Evidence: composer.Draft}, true
		}
		return DialogState{}, false
	}
	lines := strings.Split(capture, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < ompSelectorScanLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if ompSelectorFooterRe.MatchString(line) {
			return DialogState{Class: DialogUnknown, Evidence: strings.Trim(line, " │┃|")}, true
		}
	}
	return DialogState{}, false
}

// adaptDialogKeysForAgent rewrites a resolved answer for TUIs whose keys
// differ from the shared vocabulary. omp does not drop a staged paste on
// Escape (a single Escape with a draft is a verified no-op); one Ctrl+C
// clears the whole draft, paste token included.
func adaptDialogKeysForAgent(keys []string, state DialogState, agentType string) []string {
	if agentType == "omp" && state.Class == DialogPasteLimbo && len(keys) == 1 && keys[0] == "Escape" {
		return []string{"C-c"}
	}
	return keys
}

// classifyDialog inspects one pane capture and returns its dialog state.
func classifyDialog(capture string, agentType string) DialogState {
	if agentType == "omp" {
		if state, ok := classifyOmpDialog(capture); ok {
			return state
		}
	}
	lower := strings.ToLower(capture)
	options, inputMode := extractDialogOptions(capture)

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
		state := DialogState{Class: sig.class, Options: options, InputMode: inputMode, Evidence: firstDialogEvidenceLine(capture, sig.patterns)}
		// A trust/rate-limit style dialog whose body shows destructive
		// evidence is reclassified: the pending action dominates.
		if destructiveEvidence.MatchString(capture) && len(options) > 0 && sig.class != DialogUsageOverlay {
			state.Class = DialogDestructiveConfirm
		}
		return state
	}

	// Generic confirm dialog: options with an accept/decline shape.
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
				return DialogState{Class: DialogDestructiveConfirm, Options: options, InputMode: inputMode}
			}
			return DialogState{Class: DialogUnknown, Options: options, InputMode: inputMode}
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
	Choice  string   // decline | extra-usage | dismiss | option-K (display order)
}

var optionChoicePattern = regexp.MustCompile(`^option-(\d+)$`)

// dialogOptionKeys translates a displayed option into the provider's input
// protocol. Never guess a cursor's initial position or follow an immediate
// hotkey with Enter: that Enter could act on an entirely different screen.
func dialogOptionKeys(state DialogState, number int) ([]string, error) {
	picked, selected := -1, -1
	for i, option := range state.Options {
		if option.Number == number {
			picked = i
		}
		if option.Selected {
			if selected != -1 {
				return nil, fmt.Errorf("ambiguous dialog selection; re-inspect the pane")
			}
			selected = i
		}
	}
	if picked == -1 {
		return nil, fmt.Errorf("dialog has no option %d (options: %s)", number, describeOptions(state.Options))
	}
	option := state.Options[picked]
	if state.Class == DialogDestructiveConfirm && !declineSideLabel(option.Label) {
		return nil, fmt.Errorf("POLICY_REFUSED: refusing option %d: %q on a destructive confirm; only decline-side answers are allowed", number, option.Label)
	}
	switch state.InputMode {
	case "", "numbered":
		return []string{fmt.Sprintf("%d", number), "Enter"}, nil
	case "hotkey":
		if len(option.Key) != 1 || !strings.ContainsAny(option.Key, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return nil, fmt.Errorf("option %d has no usable displayed hotkey", number)
		}
		for i, other := range state.Options {
			if i != picked && strings.EqualFold(other.Key, option.Key) {
				return nil, fmt.Errorf("ambiguous dialog hotkey %q", option.Key)
			}
		}
		return []string{option.Key}, nil
	case "cursor":
		if selected == -1 {
			return nil, fmt.Errorf("dialog cursor is not visible; re-inspect before answering")
		}
		var keys []string
		for selected > picked {
			keys = append(keys, "Up")
			selected--
		}
		for selected < picked {
			keys = append(keys, "Down")
			selected++
		}
		return append(keys, "Enter"), nil
	default:
		return nil, fmt.Errorf("unsupported dialog input mode %q", state.InputMode)
	}
}

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
		return dialogOptionKeys(state, num)
	}
	switch choice {
	case "decline":
		for _, opt := range state.Options {
			if declineSideLabel(opt.Label) {
				return dialogOptionKeys(state, opt.Number)
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
				return dialogOptionKeys(state, opt.Number)
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

// verifyCursorDialogSelection is the final precondition for Enter on a cursor
// menu. Navigation keys can be dropped or a different modal can replace the
// original; neither case permits accepting whatever happens to be selected.
func verifyCursorDialogSelection(before, current DialogState, choice string) error {
	if before.Class != current.Class || before.InputMode != current.InputMode ||
		before.Evidence != current.Evidence || len(before.Options) != len(current.Options) {
		return fmt.Errorf("dialog changed while navigating; re-inspect before answering")
	}
	for i, option := range before.Options {
		other := current.Options[i]
		if option.Number != other.Number || option.Label != other.Label || option.Key != other.Key {
			return fmt.Errorf("dialog options changed while navigating; re-inspect before answering")
		}
	}
	keys, err := resolveDialogAnswer(current, choice)
	if err != nil {
		return err
	}
	if len(keys) != 1 || keys[0] != "Enter" {
		return fmt.Errorf("requested option is not selected; refusing Enter on an unverified cursor")
	}
	return nil
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
	keys = adaptDialogKeysForAgent(keys, output.Before, restartPaneAgentType(pane))
	for i, key := range keys {
		if i > 0 {
			select {
			case <-ctx.Done():
				output.RobotResponse = NewErrorResponse(ctx.Err(), ErrCodeTimeout, "Retry after the cancellation clears")
				return output, nil
			case <-time.After(150 * time.Millisecond):
			}
		}
		if output.Before.InputMode == "cursor" && key == "Enter" {
			current, captureErr := tmux.CapturePaneVisibleContext(ctx, pane.ID)
			if captureErr != nil {
				output.RobotResponse = NewErrorResponse(captureErr, ErrCodeInternalError, "Could not verify the selected option; Enter was not sent")
				return output, nil
			}
			output.After = classifyDialog(current, restartPaneAgentType(pane))
			if err := verifyCursorDialogSelection(output.Before, output.After, opts.Choice); err != nil {
				output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Enter was not sent; re-run --robot-dialogs before retrying")
				return output, nil
			}
		}
		if err := tmux.DefaultClient.RunSilentContext(ctx, "send-keys", "-t", tmux.ExactTarget(pane.ID), key); err != nil {
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
// resolved:true, success:true — then sent work the new dialog swallowed.
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
