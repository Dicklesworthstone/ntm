package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// OpenCode (sst/opencode 1.18.x) draws its composer as a run of rows prefixed
// with the heavy bar "┃", closed by a "╹▀▀▀…" rule:
//
//	┃
//	┃  [Pasted ~108 lines] typed tail
//	┃
//	┃  Build · Big Pickle OpenCode Zen
//	╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
//
// The bottom bar row is the agent/model line, not input. An empty composer
// shows an "Ask anything…" hint. Submitted prompts are echoed into the
// transcript with the same bar prefix, but always separated from the composer
// by a non-bar row, so only the contiguous run ending at the rule is the
// composer (all verified live, GH #333).
const opencodePasteToken = "[Pasted ~"

// opencodeComposerDraft extracts the text currently in an OpenCode composer
// from a visible-pane capture. found is false when no composer is on screen.
// The returned draft joins the input rows with newlines and is empty for an
// empty composer (including one showing only the hint text).
func opencodeComposerDraft(capture string) (draft string, found bool) {
	return agent.ParseOpencodeComposer(capture)
}

// OpencodeComposerVisible reports whether an OpenCode capture shows the
// composer (empty or not), i.e. the TUI is up and will accept a prompt.
func OpencodeComposerVisible(capture string) bool {
	_, found := opencodeComposerDraft(capture)
	return found
}

// opencodeComposerHoldsPayload reports whether an OpenCode capture shows the
// delivered message still sitting unsubmitted in the composer: either a
// collapsed "[Pasted ~N lines]" token (what OpenCode shows for the pasted
// payload itself) or the message's first non-empty line.
func opencodeComposerHoldsPayload(capture, message string) bool {
	draft, found := opencodeComposerDraft(capture)
	if !found || draft == "" {
		return false
	}
	if strings.Contains(draft, opencodePasteToken) {
		return true
	}
	snippet := ""
	for _, line := range strings.Split(message, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			if runes := []rune(line); len(runes) > 32 {
				line = string(runes[:32])
			}
			snippet = line
			break
		}
	}
	normalized := strings.Join(strings.Fields(draft), " ")
	return snippet != "" && strings.Contains(normalized, snippet)
}

const (
	// opencodeStagePollInterval and opencodeStageMaxPolls bound how long a
	// staged payload may take to appear in the composer after the protocol's
	// first delay before the delivery is declared dropped (~2s extra).
	opencodeStagePollInterval = 250 * time.Millisecond
	opencodeStageMaxPolls     = 8
)

// ErrOpencodePayloadNotStaged reports that an OpenCode composer was still
// empty after a prompt was typed or pasted into it, so no Enter was pressed.
var ErrOpencodePayloadNotStaged = errors.New("opencode composer is still empty after delivery: the TUI did not accept the payload, no Enter was sent")

// requireOpencodePayloadStaged is the pre-Enter half of OpenCode delivery
// verification. Once the payload has been sent it must be visible in the
// composer; a composer positively shown EMPTY means the TUI dropped it (the
// GH #333 failure, which used to be reported as a successful send). Fails
// open when no composer is recognizable or the capture fails, so a layout
// change can never block a delivery that worked.
func (c *Client) requireOpencodePayloadStaged(ctx context.Context, target string) error {
	for poll := 0; ; poll++ {
		capture, err := c.CapturePaneVisibleContext(ctx, target)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return nil
		}
		draft, found := opencodeComposerDraft(capture)
		if !found || draft != "" {
			return nil
		}
		if poll >= opencodeStageMaxPolls {
			return fmt.Errorf("pane %s: %w", target, ErrOpencodePayloadNotStaged)
		}
		if err := waitForSendDelay(ctx, opencodeStagePollInterval); err != nil {
			return err
		}
	}
}

const (
	// opencodeSubmitGracePolls x codexVerifyPollInterval is how long a payload
	// may sit in the composer after the protocol's Enters before a rescue
	// Enter is pressed. The FIRST prompt on a fresh OpenCode pane creates the
	// session and was measured to take up to ~7s to leave the composer; later
	// prompts clear in ~0.5s.
	opencodeSubmitGracePolls = 6
	// opencodeRescuePolls bounds the post-rescue confirmation window.
	opencodeRescuePolls = 12
)

// VerifyOpencodeSubmissionContext confirms that a prompt delivered to an
// OpenCode pane actually left the composer. Before GH #333 an OpenCode send
// was never read back, so a payload stranded in the composer was still
// reported as sent. A payload that is still visible after a grace window gets
// one more Enter (Enter into an empty OpenCode composer does nothing),
// followed by bounded polling. Returns (confirmed, rescued) like the other
// verifiers. The pre-Enter half (the payload must have landed at all) is
// requireOpencodePayloadStaged.
//
// paneWidth is accepted for signature parity; the composer is located by its
// closing rule, not by width.
func (c *Client) VerifyOpencodeSubmissionContext(ctx context.Context, target, message string, _ int) (bool, bool, error) {
	if err := waitForSendDelay(ctx, codexVerifyInitialDelay); err != nil {
		return false, false, err
	}
	holds := func() (bool, error) {
		capture, err := c.CapturePaneVisibleContext(ctx, target)
		if err != nil {
			return false, fmt.Errorf("capture opencode pane for submission verification: %w", err)
		}
		return opencodeComposerHoldsPayload(capture, message), nil
	}
	for poll := 0; ; poll++ {
		stranded, err := holds()
		if err != nil {
			return false, false, err
		}
		if !stranded {
			return true, false, nil
		}
		if poll >= opencodeSubmitGracePolls {
			break
		}
		if err := waitForSendDelay(ctx, codexVerifyPollInterval); err != nil {
			return false, false, err
		}
	}
	if err := c.RunSilentContext(ctx, "send-keys", "-t", ExactTarget(target), "Enter"); err != nil {
		return false, true, fmt.Errorf("send rescue Enter to opencode pane: %w", err)
	}
	for poll := 0; poll < opencodeRescuePolls; poll++ {
		if err := waitForSendDelay(ctx, codexVerifyPollInterval); err != nil {
			return false, true, err
		}
		stranded, err := holds()
		if err != nil {
			return false, true, err
		}
		if !stranded {
			return true, true, nil
		}
	}
	return false, true, nil
}

// VerifyOpencodeSubmissionContext verifies OpenCode prompt submission (default client).
func VerifyOpencodeSubmissionContext(ctx context.Context, target, message string, paneWidth int) (bool, bool, error) {
	return DefaultClient.VerifyOpencodeSubmissionContext(ctx, target, message, paneWidth)
}
