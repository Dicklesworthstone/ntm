package serve

import (
	"context"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
)

type restoreJobPaneBinding struct {
	SourcePaneID string `json:"source_pane_id,omitempty"`
	WindowIndex  int    `json:"window_index"`
	PaneIndex    int    `json:"pane_index"`
	AgentType    string `json:"agent_type,omitempty"`
}

// restoreJobProgressObserver accumulates observed effects from the shared
// restorer, never a second execution model. Resolve the reporter from the
// execution context supplied with each event: admission precedes installation
// of the job and durable operation reporters. Capturing admission's context
// instead would silently bypass both journals.
func restoreJobProgressObserver() func(context.Context, checkpoint.RestoreProgress) error {
	var mu sync.Mutex
	var sessionCreated, previousStopped bool
	created, launched, started, injected := []string{}, []string{}, []string{}, []string{}
	bindings := make(map[string]restoreJobPaneBinding)
	return func(ctx context.Context, event checkpoint.RestoreProgress) error {
		mu.Lock()
		defer mu.Unlock()
		if event.Outcome == "succeeded" && (event.Phase == "after" || event.Phase == "observed") {
			switch event.Stage {
			case "stop_session":
				previousStopped = true
			case "create_session":
				sessionCreated = true
			case "create_pane", "pane_identified":
				if event.PaneID != "" {
					created = appendRestoreJobPaneID(created, event.PaneID)
					bindings[event.PaneID] = restoreJobPaneBinding{
						SourcePaneID: event.SourcePaneID, WindowIndex: event.WindowIndex,
						PaneIndex: event.PaneIndex, AgentType: event.AgentType,
					}
				}
			case "launch_agent":
				launched = appendRestoreJobPaneID(launched, event.PaneID)
			case "agent_started":
				started = appendRestoreJobPaneID(started, event.PaneID)
			case "inject_context":
				injected = appendRestoreJobPaneID(injected, event.PaneID)
			}
		}
		// Clone before publication: later events must not mutate either the
		// retained in-memory evidence or an operation receipt being serialized.
		snapshot, err := toJSONMap(map[string]interface{}{
			"session_name": event.Session, "source_session": event.SourceSession,
			"checkpoint_id": event.CheckpointID, "working_dir": event.WorkingDir,
			"restore_progress": map[string]interface{}{
				"last_event": event, "session_created": sessionCreated,
				"previous_session_stopped": previousStopped,
				"created_pane_ids":         created, "pane_bindings": bindings,
				"launch_accepted_pane_ids": launched, "startup_verified_pane_ids": started,
				"context_injected_pane_ids": injected,
			},
		})
		if err != nil {
			return err
		}
		// Recording a finished action still matters after cancellation. The
		// existing reporter owns terminal-state and durable-receipt policy.
		return reportJobProgress(ctx, snapshot)
	}
}

func appendRestoreJobPaneID(ids []string, id string) []string {
	if id == "" {
		return ids
	}
	for _, prior := range ids {
		if prior == id {
			return ids
		}
	}
	return append(ids, id)
}
