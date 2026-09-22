package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	restoreContextReadyTimeout = 30 * time.Second
	restoreContextReadyPoll    = 200 * time.Millisecond
)

// ErrRestoreContextNotReady means saved context was withheld. Waiting never
// clears a draft, answers a dialog, interrupts a turn, or relaunches a pane.
var ErrRestoreContextNotReady = errors.New("restored agent is not ready for context")

// waitForRestoreContextReady uses the shared observer/classifier rather than
// treating process startup as permission to submit input. Capture only the
// exact restored pane and only its visible screen; unrelated panes and old
// scrollback must not determine whether this recipient is ready.
func waitForRestoreContextReady(ctx context.Context, session, paneID string, expected tmux.AgentType) error {
	client := tmux.DefaultClient
	observer := status.NewSessionObserverWithDependencies(nil, status.SessionObserverConfig{}, status.SessionObserverDependencies{
		ListPanes: func(ctx context.Context, session string) ([]tmux.PaneActivity, error) {
			panes, err := client.GetPanesContext(ctx, session)
			if err != nil {
				return nil, err
			}
			var selected []tmux.PaneActivity
			for _, pane := range panes {
				if pane.ID == paneID {
					// Start conservatively without borrowing a sibling's window
					// activity. The shared observer refines repeated captures
					// using its pane-local content-change history.
					selected = append(selected, tmux.PaneActivity{Pane: pane, LastActivity: time.Now()})
				}
			}
			return selected, nil
		},
		CapturePane: func(ctx context.Context, target string, _ int) (string, error) {
			return client.CapturePaneVisibleContext(ctx, target)
		},
	})
	return waitForRestoreReadyObservation(ctx, session, paneID, expected,
		restoreContextReadyTimeout, restoreContextReadyPoll, observer.Observe, time.Now)
}

// Two separately collected ready observations prevent a transient startup
// frame from authorizing delivery. A refused observation breaks the streak.
// The caller performs its final topology check AFTER this wait and immediately
// before sending. No previously captured output is submitted as an answer.
func waitForRestoreReadyObservation(ctx context.Context, session, paneID string, expected tmux.AgentType,
	timeout, interval time.Duration,
	observe func(context.Context, string) (status.SessionObservation, error), now func() time.Time,
) error {
	if ctx == nil || observe == nil || now == nil || timeout <= 0 || interval <= 0 || session == "" || !strings.HasPrefix(paneID, "%") {
		return fmt.Errorf("%w: invalid readiness request", ErrRestoreContextNotReady)
	}
	expected = expected.Canonical()
	if expected == "" || expected == tmux.AgentUser || expected == tmux.AgentUnknown {
		return fmt.Errorf("%w: target is not an agent", ErrRestoreContextNotReady)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var previous time.Time
	var previousCommand string
	reason := "no current observation"
	stopped := func(err error) error {
		return fmt.Errorf("%w for pane %s (%s); context was not sent: %w", ErrRestoreContextNotReady, paneID, reason, err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return stopped(errors.Join(err, context.Cause(ctx)))
		}
		snapshot, err := observe(ctx, session)
		if ctx.Err() != nil {
			return stopped(errors.Join(err, ctx.Err(), context.Cause(ctx)))
		}
		if err != nil {
			return stopped(fmt.Errorf("observe restored session: %w", err))
		}
		if snapshot.Session != session {
			return stopped(errors.New("readiness observation belongs to a different session"))
		}
		pane, found := snapshot.PaneByID(paneID)
		if !found {
			return stopped(errors.New("restored pane is missing or ambiguous"))
		}
		ready, refusal, err := restoreContextReadiness(pane, paneID, expected, now())
		reason = refusal
		if err != nil {
			return stopped(err)
		}
		if ready {
			if !previous.IsZero() && pane.Current.ObservedAt.After(previous) && pane.Metadata.Command == previousCommand {
				return nil
			}
			previous = pane.Current.ObservedAt
			previousCommand = pane.Metadata.Command
			reason = "waiting for a second ready observation"
		} else {
			previous = time.Time{}
			previousCommand = ""
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return stopped(errors.Join(ctx.Err(), context.Cause(ctx)))
		case <-timer.C:
		}
	}
}

// restoreContextReadiness never promotes last-known, display-only heuristic,
// or failed captures into dispatch permission. Composer inspection adds draft
// and modal checks that process health and idle classification alone lack.
func restoreContextReadiness(pane status.PaneObservation, paneID string, expected tmux.AgentType, now time.Time) (bool, string, error) {
	metadata := pane.Metadata
	if metadata.ID != paneID || metadata.Type.Canonical() != expected.Canonical() ||
		metadata.Dead || metadata.Service != "" || metadata.IdleShell() ||
		strings.TrimSpace(metadata.Command) == "" || tmux.PaneCommandIsStarting(metadata.Command) {
		return false, "target identity changed", errors.New("restored target is no longer the expected live agent")
	}
	if pane.Current.Error != "" {
		return false, "observation failed", errors.New("current pane capture failed; inspect the agent before retrying")
	}
	if !status.DispatchObservationIsCurrent(pane.Current.ObservedAt, now) {
		return false, "observation is stale or undated", nil
	}
	if !pane.SafeToDispatch() {
		return false, string(pane.DispatchRefusal()), nil
	}
	if strings.TrimSpace(pane.RawOutput) == "" {
		return false, "agent has not drawn its input UI", nil
	}
	if _, blocked := agent.DetectInteractiveGate(pane.RawOutput, metadata.Width); blocked {
		return false, "interactive gate requires operator attention", nil
	}
	composer := tmux.InspectComposer(pane.RawOutput, expected)
	if composer.HoldsText || composer.QueuedMessages {
		return false, "agent has existing draft or queued input", nil
	}
	switch expected.Canonical() {
	case tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGrok, tmux.AgentOmp:
		if !composer.MarkerVisible {
			return false, "agent composer is not visible", nil
		}
	}
	if expected.Canonical() == tmux.AgentOmp && agent.ParseOmpComposer(pane.RawOutput).RowsBelow != 0 {
		return false, "agent composer has an open completion list", nil
	}
	return true, "", nil
}
