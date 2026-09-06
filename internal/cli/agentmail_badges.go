package cli

// Agent Mail pane identity badges (ntm#312): orchestration between the
// session agent registry (the display authority), the canonical identity
// files, and the tmux pane/window options that cache what the border shows.
//
// Reconciliation runs on the lifecycle paths that already touch the
// registry — spawn (before each launch, then once after), add, adopt and the
// restart/relaunch hook — and on explicit refresh via `ntm mapping`. Badges
// therefore describe the last reconciliation, not continuous liveness.
// Publication failures warn and never block a launch or alter an
// assignment; a disagreement is reported, never repaired by relabelling.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// paneBadgeTmux is the tmux surface badge reconciliation needs. It is an
// interface so tests can drive the orchestration without a tmux server.
type paneBadgeTmux interface {
	ListPanes(ctx context.Context, session string) ([]tmux.Pane, error)
	ListWindows(ctx context.Context, session string) ([]tmux.WindowBadgeInfo, error)
	Publish(ctx context.Context, paneID string, badge tmux.PaneBadge) (int, error)
	Clear(ctx context.Context, paneID string) error
	EnableWindowBorder(ctx context.Context, windowID string) (tmux.BorderChange, error)
	DisableWindowBorder(ctx context.Context, windowID string) (tmux.BorderChange, error)
}

type defaultPaneBadgeTmux struct{}

func (defaultPaneBadgeTmux) ListPanes(ctx context.Context, session string) ([]tmux.Pane, error) {
	return tmux.GetPanesContext(ctx, session)
}

func (defaultPaneBadgeTmux) ListWindows(ctx context.Context, session string) ([]tmux.WindowBadgeInfo, error) {
	return tmux.DefaultClient.ListWindowBadgeInfoContext(ctx, session)
}

func (defaultPaneBadgeTmux) Publish(ctx context.Context, paneID string, badge tmux.PaneBadge) (int, error) {
	return tmux.DefaultClient.PublishPaneBadgeContext(ctx, paneID, badge)
}

func (defaultPaneBadgeTmux) Clear(ctx context.Context, paneID string) error {
	return tmux.DefaultClient.ClearPaneBadgeContext(ctx, paneID)
}

func (defaultPaneBadgeTmux) EnableWindowBorder(ctx context.Context, windowID string) (tmux.BorderChange, error) {
	return tmux.DefaultClient.EnableWindowBadgeBorderContext(ctx, windowID)
}

func (defaultPaneBadgeTmux) DisableWindowBorder(ctx context.Context, windowID string) (tmux.BorderChange, error) {
	return tmux.DefaultClient.DisableWindowBadgeBorderContext(ctx, windowID)
}

// newPaneBadgeTmux builds the tmux surface; tests replace it.
var newPaneBadgeTmux = func() paneBadgeTmux { return defaultPaneBadgeTmux{} }

// paneBadgesEnabled reports whether the config asks for badges. Badges ride
// on identity assignment, so they need the same gate as registration
// (enabled + auto_register, #243) plus the pane_badges toggle.
func paneBadgesEnabled() bool {
	return agentMailRegistrationEnabled() && cfg.AgentMail.PaneBadgesOrDefault()
}

func paneBadgeTemplate() string {
	if cfg == nil {
		return agentmail.DefaultBadgeTemplate
	}
	return cfg.AgentMail.PaneBadgeFormatOrDefault()
}

// badgeReconcileOptions parameterise one reconciliation pass.
type badgeReconcileOptions struct {
	Session    string
	ProjectKey string
	// Registry is the in-memory registry of the running lifecycle command;
	// nil loads the persisted registry for the session.
	Registry *agentmail.SessionAgentRegistry
	// OnlyPanes restricts the pass to these panes, each with a lifecycle
	// override (pre-launch publication uses starting). nil reconciles every
	// pane in the session.
	OnlyPanes map[string]agentmail.PaneLifecycle
}

// badgeReconcileReport is the outcome of one pass.
type badgeReconcileReport struct {
	Session string `json:"session"`
	// Enabled is the config toggle; Published/Cleared count tmux writes.
	Enabled   bool `json:"enabled"`
	Published int  `json:"published"`
	Cleared   int  `json:"cleared"`
	// WindowsPrepared/WindowsRestored count border-format changes.
	WindowsPrepared int `json:"windows_prepared"`
	WindowsRestored int `json:"windows_restored"`
	// Records holds every reconciled pane, ordered by window then pane index.
	Records []agentmail.PaneBadgeRecord `json:"records"`
	// Discrepancies is the subset of Records that disagree in some way, plus
	// registry bindings whose pane no longer exists.
	Discrepancies []agentmail.PaneBadgeRecord `json:"discrepancies"`
	// Warnings carries diagnostics that did not stop the pass: linked
	// windows skipped, publication failures, session opt-out, store errors.
	Warnings []string `json:"warnings,omitempty"`
	// TmuxError is set when panes could not be listed; records then describe
	// unobservable assignments and nothing was published.
	TmuxError string `json:"tmux_error,omitempty"`
	// AttemptedAt is the pass timestamp (every record's last_attempt_at).
	AttemptedAt time.Time `json:"attempted_at"`
}

func (r *badgeReconcileReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// paneIsManagedAgent reports whether a pane should carry a badge: any pane
// with an NTM assignment, or an agent-typed pane (which then shows the
// unresolved indication). User panes and tagged service panes without an
// assignment are excluded; an explicit adoption (@ntm_agent_type) already
// outranks the service tag in the pane model.
func paneIsManagedAgent(pane tmux.Pane, registry *agentmail.SessionAgentRegistry) bool {
	if name, ok := registry.GetAgentByID(pane.ID); ok && strings.TrimSpace(name) != "" {
		return true
	}
	if pane.IsServicePane() {
		return false
	}
	agentType := pane.Type.Canonical()
	return agentType.IsValid() && agentType != tmux.AgentUser && agentType != tmux.AgentUnknown
}

// reconcileSessionIdentityBadges compares every managed pane's NTM assignment
// with its canonical identity file, records the outcome in the session's
// badge store, and — when badges are enabled and the session has not opted
// out — publishes the cached label/state to tmux and prepares each window's
// border format. When badges are disabled it withdraws NTM's pane options
// and restores the border formats it owns. The report is always returned;
// the error is set only when tmux could not be observed at all.
func reconcileSessionIdentityBadges(ctx context.Context, opts badgeReconcileOptions) (*badgeReconcileReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	report := &badgeReconcileReport{Session: opts.Session, Enabled: paneBadgesEnabled(), AttemptedAt: now}

	registry := opts.Registry
	if registry == nil {
		loaded, err := agentmail.LoadBestSessionAgentRegistry(opts.Session, opts.ProjectKey)
		if err != nil {
			return report, fmt.Errorf("loading agent registry: %w", err)
		}
		registry = loaded
	}
	projectKey := opts.ProjectKey
	if registry != nil && strings.TrimSpace(registry.ProjectKey) != "" {
		projectKey = registry.ProjectKey
	}
	hasRegistry := registry != nil
	if !hasRegistry {
		// Nothing NTM assigned: there is no display authority, so nothing is
		// published. The session is still listed so the caller can see its
		// unregistered agent panes, and anything an earlier pass cached is
		// withdrawn below like any other disabled pass.
		registry = agentmail.NewSessionAgentRegistry(opts.Session, projectKey)
		if report.Enabled {
			report.warnf("no agent registry for session %s; nothing to publish", opts.Session)
		}
	}

	store, err := agentmail.LoadPaneBadgeStore(opts.Session, projectKey)
	if err != nil {
		report.warnf("badge store unreadable, starting fresh: %v", err)
		store = &agentmail.PaneBadgeStore{SessionName: opts.Session, ProjectKey: projectKey, Panes: map[string]agentmail.PaneBadgeRecord{}}
	}
	persistStore := func() {
		if err := store.Save(); err != nil {
			report.warnf("badge store not saved: %v", err)
		}
	}

	tm := newPaneBadgeTmux()
	panes, err := tm.ListPanes(ctx, opts.Session)
	if err != nil {
		// tmux unavailable: advance every attempt, retain every success,
		// publish nothing, and say so.
		report.TmuxError = err.Error()
		for paneID, name := range registry.PaneIDMap {
			if opts.OnlyPanes != nil {
				if _, wanted := opts.OnlyPanes[paneID]; !wanted {
					continue
				}
			}
			rec := agentmail.UnobservableRecord(registry, paneID, name, now, store.Panes[paneID])
			rec.PublishError = "tmux unavailable: " + err.Error()
			store.Panes[paneID] = rec
			report.Records = append(report.Records, rec)
		}
		sortBadgeRecords(report.Records)
		report.Discrepancies = append(report.Discrepancies, report.Records...)
		persistStore()
		return report, fmt.Errorf("listing tmux panes: %w", err)
	}

	// Publication needs the toggle AND a display authority; a session with
	// no registry is treated like a disabled one so stale badges come off.
	publish := report.Enabled && hasRegistry
	windowsByIndex := map[int]tmux.WindowBadgeInfo{}
	socketPath := ""
	windows, werr := tm.ListWindows(ctx, opts.Session)
	if werr != nil {
		report.warnf("tmux window listing failed; badge publication skipped: %v", werr)
		publish = false
	}
	optedOut := false
	for _, w := range windows {
		windowsByIndex[w.Index] = w
		if w.SocketPath != "" {
			socketPath = w.SocketPath
		}
		if w.SessionOptOut {
			optedOut = true
		}
	}
	if optedOut {
		if report.Enabled {
			report.warnf("session %s opted out of pane badges (%s=off); nothing published", opts.Session, tmux.SessionOptionAgentMailBadges)
		}
		publish = false
	}

	projectKeys := identityPublishKeys(projectKey, "")
	template := paneBadgeTemplate()
	preparedWindows := map[string]bool{}
	skippedLinked := map[string]bool{}
	seenPanes := map[string]bool{}

	for _, pane := range panes {
		if pane.ID == "" {
			continue
		}
		if opts.OnlyPanes != nil {
			if _, wanted := opts.OnlyPanes[pane.ID]; !wanted {
				continue
			}
		}
		seenPanes[pane.ID] = true
		if !paneIsManagedAgent(pane, registry) {
			// A pane that stopped being an agent pane must not keep a stale
			// badge.
			if prev, had := store.Panes[pane.ID]; had {
				if prev.Cached && !optedOut {
					if cerr := tm.Clear(ctx, pane.ID); cerr != nil {
						report.warnf("could not clear badge on %s: %v", pane.ID, cerr)
						continue
					}
					report.Cleared++
				}
				delete(store.Panes, pane.ID)
			}
			continue
		}

		rec := agentmail.ReconcilePane(registry, agentmail.ReconcileInput{
			SessionName: opts.Session,
			ProjectKeys: projectKeys,
			SocketPath:  socketPath,
			Pane:        pane,
			Lifecycle:   opts.OnlyPanes[pane.ID],
			Template:    template,
			Now:         now,
		}, store.Panes[pane.ID])
		prev := store.Panes[pane.ID]
		// Until this pass says otherwise, whatever an earlier pass cached on
		// the pane is still there.
		rec.Cached = prev.Cached

		switch {
		case publish:
			window, ok := windowsByIndex[pane.WindowIndex]
			switch {
			case !ok:
				rec.PublishError = fmt.Sprintf("window %d not found in session listing", pane.WindowIndex)
			case window.Linked:
				rec.PublishError = "window " + window.ID + " is linked into multiple sessions; badge publication skipped"
				if !skippedLinked[window.ID] {
					skippedLinked[window.ID] = true
					report.warnf("window %s (index %d) is linked into multiple sessions; pane badges are not published there (window options would be shared)", window.ID, window.Index)
				}
			default:
				if !preparedWindows[window.ID] {
					preparedWindows[window.ID] = true
					change, berr := tm.EnableWindowBorder(ctx, window.ID)
					switch {
					case berr != nil:
						report.warnf("could not prepare pane-border-format on window %s: %v", window.ID, berr)
					case change.Changed:
						report.WindowsPrepared++
					case change.Skipped != "":
						report.warnf("window %s: %s", window.ID, change.Skipped)
					}
				}
				pid, perr := tm.Publish(ctx, pane.ID, tmux.PaneBadge{
					Name:      rec.AssignedName,
					State:     rec.State,
					Lifecycle: string(rec.Lifecycle),
					Label:     rec.Label,
				})
				switch {
				case perr != nil:
					rec.PublishError = perr.Error()
				case pid > 0 && pane.PID > 0 && pid != pane.PID:
					// The pane changed generation between observation and
					// publication (respawn keeps %N). What we wrote describes
					// the old generation: withdraw it and let the next
					// reconciliation judge the new one.
					if cerr := tm.Clear(ctx, pane.ID); cerr == nil {
						rec.Cached = false
					}
					rec.PublishError = fmt.Sprintf("pane generation changed during publication (pid %d -> %d); badge withdrawn", pane.PID, pid)
				default:
					rec.Published = true
					rec.Cached = true
					report.Published++
				}
			}
			if rec.PublishError != "" && !(ok && window.Linked) {
				// (Linked windows were warned about once above.)
				report.warnf("pane %s (%s): badge not published: %s", pane.ID, rec.AssignedName, rec.PublishError)
			}
		case (!report.Enabled || !hasRegistry) && werr == nil && !optedOut:
			// Disabled (or no display authority): withdraw anything an
			// earlier pass left on the pane.
			if prev.Cached {
				if cerr := tm.Clear(ctx, pane.ID); cerr != nil {
					report.warnf("could not clear badge on %s: %v", pane.ID, cerr)
				} else {
					rec.Cached = false
					report.Cleared++
				}
			}
		default:
			if prev.Cached {
				// Enabled but publication impossible this pass; the cached
				// pane option is whatever the last pass wrote.
				rec.PublishError = "publication skipped this pass"
			}
		}
		store.Panes[pane.ID] = rec
		report.Records = append(report.Records, rec)
	}

	// Registry bindings whose pane is gone are discrepancies too: they are
	// what `ntm add` may recover a name from, and they must never look
	// current.
	if opts.OnlyPanes == nil {
		for paneID, name := range registry.PaneIDMap {
			if seenPanes[paneID] {
				continue
			}
			prev := store.Panes[paneID]
			rec := agentmail.PaneBadgeRecord{
				PaneID:           paneID,
				PaneIndex:        prev.PaneIndex,
				WindowIndex:      prev.WindowIndex,
				RecordedPID:      registry.PanePID(paneID),
				AssignedName:     name,
				AssignmentState:  agentmail.PaneAssignmentStale,
				ObservationState: agentmail.PaneObservationSkipped,
				Lifecycle:        agentmail.PaneLifecycleExited,
				State:            "assignment-" + string(agentmail.PaneAssignmentStale),
				Problems:         []string{"pane-missing: registry binds " + paneID + " which no longer exists in session " + opts.Session},
				LastAttemptAt:    now,
				LastSuccessAt:    prev.LastSuccessAt,
			}
			report.Discrepancies = append(report.Discrepancies, rec)
			delete(store.Panes, paneID)
		}
	}

	// Disabled: restore every window border NTM owns. Windows carrying no
	// NTM marker are left untouched by the tmux layer.
	if (!report.Enabled || !hasRegistry) && werr == nil && !optedOut && opts.OnlyPanes == nil {
		for _, w := range windows {
			if w.Linked {
				continue
			}
			change, derr := tm.DisableWindowBorder(ctx, w.ID)
			if derr != nil {
				report.warnf("could not restore pane-border-format on window %s: %v", w.ID, derr)
				continue
			}
			if change.Changed {
				report.WindowsRestored++
			}
		}
	}

	sortBadgeRecords(report.Records)
	for _, rec := range report.Records {
		if rec.HasDiscrepancy() {
			report.Discrepancies = append(report.Discrepancies, rec)
		}
	}
	sortBadgeRecords(report.Discrepancies)
	persistStore()
	return report, nil
}

func sortBadgeRecords(records []agentmail.PaneBadgeRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].WindowIndex != records[j].WindowIndex {
			return records[i].WindowIndex < records[j].WindowIndex
		}
		if records[i].PaneIndex != records[j].PaneIndex {
			return records[i].PaneIndex < records[j].PaneIndex
		}
		return records[i].PaneID < records[j].PaneID
	})
}

// reportBadgeWarnings prints a pass's warnings for the human output paths of
// lifecycle commands. Badge problems are informational by contract: they
// never change an exit status.
func reportBadgeWarnings(report *badgeReconcileReport, err error) {
	if IsJSONOutput() {
		return
	}
	if err != nil {
		output.PrintWarningf("Pane badges not reconciled: %v", err)
	}
	if report == nil {
		return
	}
	for _, warning := range report.Warnings {
		output.PrintWarningf("Pane badges: %s", warning)
	}
	// Only live panes are worth a lifecycle-time warning: registry bindings
	// whose pane is gone are the normal dead-slot state between a kill and
	// a respawn, and a pane without an assignment was already reported by
	// registration itself. `ntm mapping` prints the full report.
	for _, rec := range report.Records {
		if !rec.HasDiscrepancy() || rec.AssignmentState == agentmail.PaneAssignmentUnregistered {
			continue
		}
		output.PrintWarningf("Pane badges: %s pane %d (%s): %s", rec.PaneID, rec.PaneIndex, rec.AssignedName, strings.Join(rec.Problems, "; "))
	}
}

// publishStartingBadge is the pre-launch publication for one pane: the
// assigned name with lifecycle=starting, written before the agent command is
// sent so the badge exists when the process comes up. Non-blocking by
// contract.
func (c *spawnIdentityCoordinator) publishStartingBadge(ctx context.Context, agent spawnedAgentInfo) {
	if !c.preLaunch || !paneBadgesEnabled() || c.registry == nil || agent.paneID == "" {
		return
	}
	report, err := reconcileSessionIdentityBadges(ctx, badgeReconcileOptions{
		Session:    c.sessionName,
		ProjectKey: c.workingDir,
		Registry:   c.registry,
		OnlyPanes:  map[string]agentmail.PaneLifecycle{agent.paneID: agentmail.PaneLifecycleStarting},
	})
	reportBadgeWarnings(report, err)
}

// reconcileBadges runs the post-launch pass over the whole session: every
// managed pane's lifecycle is re-derived from tmux, so a badge published as
// "starting" becomes plain once the agent is up. Also the single pass used
// by the batch (add/adopt/relaunch) entry point.
func (c *spawnIdentityCoordinator) reconcileBadges(ctx context.Context) *badgeReconcileReport {
	if !c.enabled || c.registry == nil {
		return nil
	}
	if !paneBadgesEnabled() {
		return nil
	}
	report, err := reconcileSessionIdentityBadges(ctx, badgeReconcileOptions{
		Session:    c.sessionName,
		ProjectKey: c.workingDir,
		Registry:   c.registry,
	})
	reportBadgeWarnings(report, err)
	return report
}
