// Package dashboard provides a stunning visual session dashboard
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/clipboard"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/cost"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/integrations/rano"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/state"
	status "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	synthtui "github.com/Dicklesworthstone/ntm/internal/tui/synthesizer"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

func (m *Model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	prevWidth := m.width
	prevTier := m.tier

	width := msg.Width
	height := msg.Height
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	m.width = width
	m.height = height
	m.tier = layout.TierForWidthWithHysteresis(width, prevTier)

	m.cycleFocus(0)

	_, detailWidth := layout.SplitProportions(width)
	contentWidth := detailWidth - 4
	if contentWidth < 20 {
		contentWidth = 20
	}
	if m.renderer != nil {
		m.initRenderer(contentWidth)
	}

	if prevWidth != m.width {
		m.renderedOutputCache = make(map[string]string)
		// Update help model width for responsive rendering
		m.helpModel.Width = width - 4
	}

	searchW := int(float64(width) * 0.6)
	searchH := int(float64(height) * 0.6)
	if searchW < 20 {
		searchW = 20
	}
	if searchH < 10 {
		searchH = 10
	}
	m.cassSearch.SetSize(searchW, searchH)

	modesW := m.width - 10
	modesH := m.height - 6
	if modesW < 30 {
		modesW = 30
	}
	if modesH < 10 {
		modesH = 10
	}
	m.ensembleModes.SetSize(modesW, modesH)

	m.resizePanelsForLayout()

	if dashboardDebugEnabled(m) {
		contentHeight := contentHeightFor(m.height)
		log.Printf("[dashboard] resize width=%d height=%d contentHeight=%d tier=%s",
			m.width, m.height, contentHeight, tierLabel(m.tier))
		log.Printf("[dashboard] panels %s %s %s %s %s %s %s %s",
			logPanelSize("beads", m.beadsPanel),
			logPanelSize("alerts", m.alertsPanel),
			logPanelSize("metrics", m.metricsPanel),
			logPanelSize("history", m.historyPanel),
			logPanelSize("files", m.filesPanel),
			logPanelSize("timeline", m.timelinePanel),
			logPanelSize("cass", m.cassPanel),
			logPanelSize("spawn", m.spawnPanel),
		)
	}

	if prevTier != m.tier && dashboardDebugEnabled(m) {
		log.Printf("[dashboard] tier transition %s -> %s (width=%d height=%d)",
			tierLabel(prevTier), tierLabel(m.tier), m.width, m.height)
	}

	return *m, nil
}

func (m Model) subscribeToConfig() tea.Cmd {
	return func() tea.Msg {
		if m.configSub == nil {
			return nil
		}
		cfg := <-m.configSub
		return ConfigReloadMsg{Config: cfg}
	}
}

func (m Model) tick() tea.Cmd {
	interval := m.getTickInterval()
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return DashboardTickMsg(t)
	})
}

// getTickInterval returns the appropriate tick interval based on activity state
func (m Model) getTickInterval() time.Duration {
	baseTick := m.baseTick
	if baseTick == 0 {
		baseTick = 100 * time.Millisecond
	}
	idleTick := m.idleTick
	if idleTick == 0 {
		idleTick = 500 * time.Millisecond
	}

	// Use fast tick during spring-driven animations for smooth motion.
	if (m.toasts != nil && m.toasts.IsAnimating()) ||
		(m.dashboardSprings != nil && m.dashboardSprings.IsAnimating()) ||
		m.localPerfAnimating() {
		// ~60 FPS for smooth spring animation
		return 16 * time.Millisecond
	}

	switch m.activityState {
	case StateIdle:
		return idleTick
	default:
		return baseTick
	}
}

// fetchHealthStatus performs the health check via bv
func (m Model) fetchHealthStatus() tea.Cmd {
	return func() tea.Msg {
		if !bv.IsInstalled() {
			return HealthCheckMsg{
				Status:  "unavailable",
				Message: "bv not installed",
			}
		}

		result := bv.CheckDrift(m.projectDir)
		var status string
		switch result.Status {
		case bv.DriftOK:
			status = "ok"
		case bv.DriftWarning:
			status = "warning"
		case bv.DriftCritical:
			status = "critical"
		case bv.DriftNoBaseline:
			status = "no_baseline"
		default:
			status = "unknown"
		}

		return HealthCheckMsg{
			Status:  status,
			Message: result.Message,
		}
	}
}

func (m *Model) fetchScanStatusWithContext(ctx context.Context) tea.Cmd {
	gen := m.nextGen(refreshScan)
	return func() tea.Msg {
		if !scanner.IsAvailable() {
			return ScanStatusMsg{Status: "unavailable", Gen: gen}
		}

		if ctx == nil {
			ctx = context.Background()
		}

		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		opts := scanner.ScanOptions{
			DiffOnly: true,
			Timeout:  15 * time.Second,
		}

		start := time.Now()
		result, err := scanner.QuickScanWithOptions(ctx, ".", opts)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				if errors.Is(ctxErr, context.Canceled) {
					return ScanStatusMsg{Err: ctxErr, Gen: gen}
				}
				return ScanStatusMsg{Status: "error", Err: ctxErr, Gen: gen}
			}
			if errors.Is(err, context.Canceled) {
				return ScanStatusMsg{Err: err, Gen: gen}
			}
			return ScanStatusMsg{Status: "error", Err: err, Gen: gen}
		}
		if result == nil {
			return ScanStatusMsg{Status: "unavailable", Gen: gen}
		}

		status := "clean"
		switch {
		case result.Totals.Critical > 0:
			status = "critical"
		case result.Totals.Warning > 0:
			status = "warning"
		}

		dur := result.Duration
		if dur == 0 {
			dur = time.Since(start)
		}

		return ScanStatusMsg{
			Status:   status,
			Totals:   result.Totals,
			Duration: dur,
			Gen:      gen,
		}
	}
}

// fetchRanoNetworkStats fetches per-agent network activity from rano (best-effort).
func (m *Model) fetchRanoNetworkStats() tea.Cmd {
	gen := m.nextGen(refreshRanoNetwork)
	cfg := m.cfg
	session := m.session

	return func() tea.Msg {
		data := panels.RanoNetworkPanelData{
			Loaded: true,
		}

		enabled := true
		pollInterval := 1 * time.Second
		if cfg != nil {
			enabled = cfg.Integrations.Rano.Enabled
			if cfg.Integrations.Rano.PollIntervalMs > 0 {
				pollInterval = time.Duration(cfg.Integrations.Rano.PollIntervalMs) * time.Millisecond
			}
		}
		data.Enabled = enabled
		data.PollInterval = pollInterval

		if !enabled {
			return RanoNetworkUpdateMsg{Data: data, Gen: gen}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		adapter := tools.NewRanoAdapter()
		availability, err := adapter.GetAvailability(ctx)
		if err != nil {
			data.Error = err
			return RanoNetworkUpdateMsg{Data: data, Gen: gen}
		}

		if availability != nil {
			data.Available = availability.Available && availability.Compatible && availability.HasCapability && availability.CanReadProc
			if availability.Version.Raw != "" {
				data.Version = availability.Version.String()
			}
		}

		if !data.Available {
			return RanoNetworkUpdateMsg{Data: data, Gen: gen}
		}

		// Refresh PID mapping so we can attribute process stats to panes/agents.
		pidMap := rano.NewPIDMap(session)
		if err := pidMap.RefreshContext(ctx); err != nil {
			data.Error = err
			return RanoNetworkUpdateMsg{Data: data, Gen: gen}
		}

		allStats, err := adapter.GetAllProcessStats(ctx)
		if err != nil {
			data.Error = err
			return RanoNetworkUpdateMsg{Data: data, Gen: gen}
		}

		byPane := make(map[string]*panels.RanoNetworkRow)
		for i := range allStats {
			st := allStats[i]

			identity := pidMap.GetPaneForPID(st.PID)
			if identity == nil {
				continue
			}
			label := identity.PaneTitle
			if label == "" {
				label = identity.String()
			}

			row := byPane[label]
			if row == nil {
				row = &panels.RanoNetworkRow{
					Label:     label,
					AgentType: string(identity.AgentType),
				}
				byPane[label] = row
			}

			row.RequestCount += st.RequestCount
			row.BytesOut += st.BytesOut
			row.BytesIn += st.BytesIn

			if ts := parseRanoTimestamp(st.LastRequest); !ts.IsZero() && ts.After(row.LastRequest) {
				row.LastRequest = ts
			}
		}

		for _, row := range byPane {
			data.Rows = append(data.Rows, *row)
			data.TotalRequests += row.RequestCount
			data.TotalBytesOut += row.BytesOut
			data.TotalBytesIn += row.BytesIn
		}

		// Stable ordering by label (the panel will still prioritize recency in View).
		sort.Slice(data.Rows, func(i, j int) bool {
			return data.Rows[i].Label < data.Rows[j].Label
		})

		return RanoNetworkUpdateMsg{Data: data, Gen: gen}
	}
}

func parseRanoTimestamp(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts
	}
	return time.Time{}
}

// fetchRCHStatus fetches the current RCH status.
func (m *Model) fetchRCHStatus() tea.Cmd {
	gen := m.nextGen(refreshRCH)
	cfg := m.cfg

	return func() tea.Msg {
		data := panels.RCHPanelData{
			Loaded: true,
		}

		enabled := true
		if cfg != nil {
			enabled = cfg.Integrations.RCH.Enabled
		}
		data.Enabled = enabled

		if !enabled {
			return RCHStatusUpdateMsg{Data: data, Gen: gen}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()

		adapter := tools.NewRCHAdapter()
		availability, err := adapter.GetAvailability(ctx)
		if err != nil {
			data.Error = err
			return RCHStatusUpdateMsg{Data: data, Gen: gen}
		}

		if availability != nil {
			data.Available = availability.Available && availability.Compatible
			if availability.Version.Raw != "" {
				data.Version = availability.Version.String()
			}
		}

		if !data.Available {
			return RCHStatusUpdateMsg{Data: data, Gen: gen}
		}

		status, err := adapter.GetStatus(ctx)
		if err != nil {
			data.Error = err
			return RCHStatusUpdateMsg{Data: data, Gen: gen}
		}
		data.Status = status

		return RCHStatusUpdateMsg{Data: data, Gen: gen}
	}
}

// fetchDCGStatus fetches the current DCG status
func (m *Model) fetchDCGStatus() tea.Cmd {
	gen := m.nextGen(refreshDCG)
	cfg := m.cfg

	return func() tea.Msg {
		// Check if DCG is enabled in config
		enabled := false
		if cfg != nil {
			enabled = cfg.Integrations.DCG.Enabled
		}

		// Get availability from the DCG adapter
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		adapter := tools.NewDCGAdapter()
		availability, err := adapter.GetAvailability(ctx)

		msg := DCGStatusUpdateMsg{
			Enabled: enabled,
			Gen:     gen,
		}

		if err != nil {
			msg.Err = err
			return msg
		}

		if availability != nil {
			msg.Available = availability.Available && availability.Compatible
			if availability.Version.Major > 0 || availability.Version.Minor > 0 || availability.Version.Patch > 0 {
				msg.Version = availability.Version.String()
			}
		}

		// TODO: Read blocked count from audit log when available
		// For now, we just report availability status

		return msg
	}
}

// fetchPendingRotations fetches pending rotation confirmations for the session
func (m *Model) fetchPendingRotations() tea.Cmd {
	gen := m.nextGen(refreshPendingRotations)
	session := m.session
	return func() tea.Msg {
		pending, err := ctxmon.GetPendingRotationsForSession(session)
		return PendingRotationsUpdateMsg{
			Pending: pending,
			Err:     err,
			Gen:     gen,
		}
	}
}

func (m *Model) newAgentMailClient(projectKey string) *agentmail.Client {
	if projectKey == "" {
		return nil
	}
	var opts []agentmail.Option
	opts = append(opts, agentmail.WithProjectKey(projectKey))
	if m.cfg != nil {
		if !m.cfg.AgentMail.Enabled {
			return nil
		}
		if m.cfg.AgentMail.URL != "" {
			opts = append(opts, agentmail.WithBaseURL(m.cfg.AgentMail.URL))
		}
		if m.cfg.AgentMail.Token != "" {
			opts = append(opts, agentmail.WithToken(m.cfg.AgentMail.Token))
		}
	}
	return agentmail.NewClient(opts...)
}

// fetchAgentMailStatus fetches Agent Mail data (locks, connection status)
func (m *Model) fetchAgentMailStatus() tea.Cmd {
	gen := m.nextGen(refreshAgentMail)
	projectKey := m.projectDir
	return func() tea.Msg {
		if projectKey == "" {
			return AgentMailUpdateMsg{Available: false, Gen: gen}
		}

		client := m.newAgentMailClient(projectKey)
		if client == nil {
			return AgentMailUpdateMsg{Available: false, Gen: gen}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Check availability via HTTP
		if !client.IsAvailable() {
			// Fallback: check if archive directory exists
			// This detects Agent Mail running via MCP stdio protocol (not HTTP)
			archiveFound := agentmail.HasArchiveForProject(projectKey)
			return AgentMailUpdateMsg{
				Available:    false,
				ArchiveFound: archiveFound,
				Gen:          gen,
			}
		}

		// Ensure project exists
		_, err := client.EnsureProject(ctx, projectKey)
		if err != nil {
			return AgentMailUpdateMsg{Available: true, Connected: false, Gen: gen}
		}

		// Fetch file reservations
		var lockInfo []AgentMailLockInfo
		reservations, err := client.ListReservations(ctx, projectKey, "", true)
		if err == nil {
			for _, r := range reservations {
				expiresIn := ""
				if !r.ExpiresTS.IsZero() {
					remaining := time.Until(r.ExpiresTS.Time)
					if remaining > 0 {
						if remaining < time.Minute {
							expiresIn = fmt.Sprintf("%ds", int(remaining.Seconds()))
						} else if remaining < time.Hour {
							expiresIn = fmt.Sprintf("%dm", int(remaining.Minutes()))
						} else {
							expiresIn = fmt.Sprintf("%dh%dm", int(remaining.Hours()), int(remaining.Minutes())%60)
						}
					} else {
						expiresIn = "expired"
					}
				}
				lockInfo = append(lockInfo, AgentMailLockInfo{
					PathPattern: r.PathPattern,
					AgentName:   r.AgentName,
					Exclusive:   r.Exclusive,
					ExpiresIn:   expiresIn,
				})
			}
		}

		return AgentMailUpdateMsg{
			Available: true,
			Connected: true,
			Locks:     len(lockInfo),
			LockInfo:  lockInfo,
			Gen:       gen,
		}
	}
}

// fetchAgentMailInboxes polls inbox summaries for all registered agents in this session.
func (m *Model) fetchAgentMailInboxes() tea.Cmd {
	gen := m.nextGen(refreshAgentMailInbox)
	projectKey := m.projectDir
	sessionName := m.session
	panes := append([]tmux.Pane(nil), m.panes...)

	return func() tea.Msg {
		if projectKey == "" || sessionName == "" {
			return AgentMailInboxSummaryMsg{Gen: gen}
		}

		client := m.newAgentMailClient(projectKey)
		if client == nil || !client.IsAvailable() {
			return AgentMailInboxSummaryMsg{Gen: gen}
		}

		registry, err := agentmail.LoadSessionAgentRegistry(sessionName, projectKey)
		if err != nil {
			return AgentMailInboxSummaryMsg{Err: err, Gen: gen}
		}
		if registry == nil {
			return AgentMailInboxSummaryMsg{AgentMap: map[string]string{}, Gen: gen}
		}

		agentMap := make(map[string]string)
		for _, pane := range panes {
			if pane.Type == tmux.AgentUser {
				continue
			}
			if name, ok := registry.GetAgent(pane.Title, pane.ID); ok {
				agentMap[pane.ID] = name
			}
		}

		if len(agentMap) == 0 {
			return AgentMailInboxSummaryMsg{AgentMap: agentMap, Gen: gen}
		}

		type job struct {
			paneID    string
			agentName string
		}
		type result struct {
			paneID string
			inbox  []agentmail.InboxMessage
			err    error
		}

		inboxes := make(map[string][]agentmail.InboxMessage, len(agentMap))
		var firstErr error
		var mu sync.Mutex
		jobs := make(chan job)
		results := make(chan result, len(agentMap))
		workerCount := 4
		if len(agentMap) < workerCount {
			workerCount = len(agentMap)
		}

		var wg sync.WaitGroup
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
						ProjectKey:    projectKey,
						AgentName:     j.agentName,
						UrgentOnly:    false,
						Limit:         5,
						IncludeBodies: false,
					})
					cancel()
					results <- result{paneID: j.paneID, inbox: msgs, err: err}
				}
			}()
		}

		go func() {
			for paneID, agentName := range agentMap {
				jobs <- job{paneID: paneID, agentName: agentName}
			}
			close(jobs)
			wg.Wait()
			close(results)
		}()

		for res := range results {
			if res.err != nil {
				if firstErr == nil {
					firstErr = res.err
				}
				continue
			}
			mu.Lock()
			inboxes[res.paneID] = res.inbox
			mu.Unlock()
		}

		return AgentMailInboxSummaryMsg{
			Inboxes:  inboxes,
			AgentMap: agentMap,
			Err:      firstErr,
			Gen:      gen,
		}
	}
}

// fetchAgentMailInboxDetails fetches message bodies for a single agent.
func (m *Model) fetchAgentMailInboxDetails(pane tmux.Pane) tea.Cmd {
	gen := m.nextGen(refreshAgentMailInbox)
	projectKey := m.projectDir
	sessionName := m.session
	paneID := pane.ID
	paneTitle := pane.Title

	return func() tea.Msg {
		if projectKey == "" || sessionName == "" {
			return AgentMailInboxDetailMsg{PaneID: paneID, Gen: gen}
		}

		client := m.newAgentMailClient(projectKey)
		if client == nil || !client.IsAvailable() {
			return AgentMailInboxDetailMsg{PaneID: paneID, Gen: gen}
		}

		registry, err := agentmail.LoadSessionAgentRegistry(sessionName, projectKey)
		if err != nil || registry == nil {
			return AgentMailInboxDetailMsg{PaneID: paneID, Err: err, Gen: gen}
		}

		agentName, ok := registry.GetAgent(paneTitle, paneID)
		if !ok {
			return AgentMailInboxDetailMsg{PaneID: paneID, Gen: gen}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    projectKey,
			AgentName:     agentName,
			UrgentOnly:    false,
			Limit:         5,
			IncludeBodies: true,
		})
		cancel()

		return AgentMailInboxDetailMsg{
			PaneID:   paneID,
			Messages: msgs,
			Err:      err,
			Gen:      gen,
		}
	}
}

// fetchCheckpointStatus fetches checkpoint status for the session
func (m *Model) fetchCheckpointStatus() tea.Cmd {
	gen := m.nextGen(refreshCheckpoint)
	session := m.session
	return func() tea.Msg {
		storage := checkpoint.NewStorage()
		checkpoints, err := storage.List(session)
		if err != nil {
			return CheckpointUpdateMsg{
				Status: "none",
				Err:    err,
				Gen:    gen,
			}
		}

		if len(checkpoints) == 0 {
			return CheckpointUpdateMsg{
				Count:  0,
				Status: "none",
				Gen:    gen,
			}
		}

		// Latest is first (sorted by creation time, newest first)
		latest := checkpoints[0]
		age := latest.Age()

		// Determine status based on age
		var status string
		switch {
		case age < 30*time.Minute:
			status = "recent"
		case age < 1*time.Hour:
			status = "stale"
		default:
			status = "old"
		}

		return CheckpointUpdateMsg{
			Count:     len(checkpoints),
			Latest:    latest,
			LatestAge: age,
			Status:    status,
			Gen:       gen,
		}
	}
}

// createCheckpointCmd creates a new checkpoint for the session
func (m Model) createCheckpointCmd() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		capturer := checkpoint.NewCapturer()
		cp, err := capturer.Create(session, "dashboard")
		if err != nil {
			return CheckpointCreatedMsg{Err: err}
		}
		return CheckpointCreatedMsg{Checkpoint: cp}
	}
}

// RotationConfirmResultMsg is sent after a rotation confirmation action completes.
type RotationConfirmResultMsg struct {
	AgentID string
	Action  ctxmon.ConfirmAction
	Success bool
	Message string
	Err     error
}

// executeRotationConfirmAction executes a rotation confirmation action.
func (m Model) executeRotationConfirmAction(agentID string, action ctxmon.ConfirmAction) tea.Cmd {
	return func() tea.Msg {
		// Get the pending rotation
		pending, err := ctxmon.GetPendingRotationByID(agentID)
		if err != nil {
			return RotationConfirmResultMsg{
				AgentID: agentID,
				Action:  action,
				Success: false,
				Err:     err,
			}
		}
		if pending == nil {
			return RotationConfirmResultMsg{
				AgentID: agentID,
				Action:  action,
				Success: false,
				Message: fmt.Sprintf("No pending rotation found for agent %s", agentID),
			}
		}

		var resultMsg string
		switch action {
		case ctxmon.ConfirmRotate:
			// Remove pending and mark for rotation on next check
			if err := ctxmon.RemovePendingRotation(agentID); err != nil {
				return RotationConfirmResultMsg{AgentID: agentID, Action: action, Success: false, Err: err}
			}
			resultMsg = fmt.Sprintf("Rotation confirmed for %s", agentID)

		case ctxmon.ConfirmCompact:
			// Remove pending and mark for compaction
			if err := ctxmon.RemovePendingRotation(agentID); err != nil {
				return RotationConfirmResultMsg{AgentID: agentID, Action: action, Success: false, Err: err}
			}
			resultMsg = fmt.Sprintf("Compaction requested for %s", agentID)

		case ctxmon.ConfirmIgnore:
			// Simply remove the pending rotation
			if err := ctxmon.RemovePendingRotation(agentID); err != nil {
				return RotationConfirmResultMsg{AgentID: agentID, Action: action, Success: false, Err: err}
			}
			resultMsg = fmt.Sprintf("Rotation cancelled for %s", agentID)

		case ctxmon.ConfirmPostpone:
			// Extend the timeout by 30 minutes
			pending.TimeoutAt = pending.TimeoutAt.Add(30 * time.Minute)
			if err := ctxmon.AddPendingRotation(pending); err != nil {
				return RotationConfirmResultMsg{AgentID: agentID, Action: action, Success: false, Err: err}
			}
			resultMsg = fmt.Sprintf("Rotation postponed 30 minutes for %s", agentID)
		}

		return RotationConfirmResultMsg{
			AgentID: agentID,
			Action:  action,
			Success: true,
			Message: resultMsg,
		}
	}
}

// Helper struct to carry output data
type PaneOutputData struct {
	PaneID       string
	PaneIndex    int
	LastActivity time.Time
	Output       string
	AgentType    string
}

type SessionDataWithOutputMsg struct {
	Panes             []tmux.Pane
	Outputs           []PaneOutputData
	Duration          time.Duration
	NextCaptureCursor int
	MetadataOnly      bool
	FocusedOnly       bool
	Err               error
	Gen               uint64
}

func (m *Model) fetchSessionDataWithOutputs() tea.Cmd {
	return m.fetchSessionDataWithOutputsCtx(context.Background())
}

func (m *Model) requestSessionFetch(cancelInFlight bool) tea.Cmd {
	m.sessionFetchPending = true

	if m.fetchingSession {
		if cancelInFlight && m.sessionFetchCancel != nil {
			m.sessionFetchCancel()
		}
		return nil
	}

	return m.startSessionFetch()
}

func (m *Model) startSessionFetch() tea.Cmd {
	if m.fetchingSession || !m.sessionFetchPending {
		return nil
	}

	m.sessionFetchPending = false
	m.fetchingSession = true
	m.lastPaneFetch = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	m.sessionFetchCancel = cancel

	cmd := m.fetchSessionDataWithOutputsCtx(ctx)
	if cmd == nil {
		cancel()
		return nil
	}
	return func() tea.Msg {
		defer cancel()
		return cmd()
	}
}

func (m *Model) finishSessionFetch() tea.Cmd {
	m.fetchingSession = false
	if m.sessionFetchCancel != nil {
		m.sessionFetchCancel()
		m.sessionFetchCancel = nil
	}

	return m.startSessionFetch()
}

func (m *Model) requestStatusesFetch() tea.Cmd {
	m.contextFetchPending = true

	if m.fetchingContext {
		return nil
	}

	return m.startStatusesFetch()
}

func (m *Model) startStatusesFetch() tea.Cmd {
	if m.fetchingContext || !m.contextFetchPending {
		return nil
	}

	m.contextFetchPending = false
	m.fetchingContext = true
	m.lastContextFetch = time.Now()

	return m.fetchStatuses()
}

func (m *Model) finishStatusesFetch() tea.Cmd {
	m.fetchingContext = false
	return m.startStatusesFetch()
}

func (m *Model) requestScanFetch(cancelInFlight bool) tea.Cmd {
	m.scanFetchPending = true

	if m.fetchingScan {
		if cancelInFlight && m.scanFetchCancel != nil {
			m.scanFetchCancel()
		}
		return nil
	}

	return m.startScanFetch()
}

func (m *Model) startScanFetch() tea.Cmd {
	if m.fetchingScan || !m.scanFetchPending {
		return nil
	}

	m.scanFetchPending = false
	m.fetchingScan = true

	ctx, cancel := context.WithCancel(context.Background())
	m.scanFetchCancel = cancel

	cmd := m.fetchScanStatusWithContext(ctx)
	if cmd == nil {
		cancel()
		return nil
	}
	return func() tea.Msg {
		defer cancel()
		return cmd()
	}
}

func (m *Model) finishScanFetch() tea.Cmd {
	m.fetchingScan = false
	if m.scanFetchCancel != nil {
		m.scanFetchCancel()
		m.scanFetchCancel = nil
	}

	return m.startScanFetch()
}

func (m *Model) fullRefresh(cancelInFlight bool) []tea.Cmd {
	var cmds []tea.Cmd
	now := time.Now()

	if cmd := m.requestSessionFetch(cancelInFlight); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.requestStatusesFetch(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.requestScanFetch(cancelInFlight); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if !m.fetchingBeads {
		m.fetchingBeads = true
		m.lastBeadsFetch = now
		cmds = append(cmds, m.fetchBeadsCmd())
	}
	if !m.fetchingAlerts {
		m.fetchingAlerts = true
		m.lastAlertsFetch = now
		cmds = append(cmds, m.fetchAlertsCmd())
	}
	if !m.fetchingAttention {
		m.fetchingAttention = true
		m.lastAttentionFetch = now
		cmds = append(cmds, m.fetchAttentionCmd())
	}
	if !m.fetchingMetrics {
		m.fetchingMetrics = true
		cmds = append(cmds, m.fetchMetricsCmd())
	}
	if !m.fetchingRouting {
		m.fetchingRouting = true
		cmds = append(cmds, m.fetchRoutingCmd())
	}
	if !m.fetchingHistory {
		m.fetchingHistory = true
		cmds = append(cmds, m.fetchHistoryCmd())
	}
	if !m.fetchingFileChanges {
		m.fetchingFileChanges = true
		cmds = append(cmds, m.fetchFileChangesCmd())
	}
	if !m.fetchingCassContext {
		m.fetchingCassContext = true
		m.lastCassContextFetch = now
		cmds = append(cmds, m.fetchCASSContextCmd())
	}
	if !m.fetchingCheckpoint {
		m.fetchingCheckpoint = true
		m.lastCheckpointFetch = now
		cmds = append(cmds, m.fetchCheckpointStatus())
	}
	if !m.fetchingHandoff {
		m.fetchingHandoff = true
		m.lastHandoffFetch = now
		cmds = append(cmds, m.fetchHandoffCmd())
	}
	if !m.fetchingSpawn {
		m.fetchingSpawn = true
		m.lastSpawnFetch = now
		cmds = append(cmds, m.fetchSpawnStateCmd())
	}
	if !m.fetchingPTHealth {
		m.fetchingPTHealth = true
		cmds = append(cmds, m.fetchPTHealthStatesCmd())
	}
	if !m.fetchingMailInbox {
		m.fetchingMailInbox = true
		m.lastMailInboxFetch = now
		cmds = append(cmds, m.fetchAgentMailInboxes())
	}
	if !m.fetchingPendingRot {
		m.fetchingPendingRot = true
		m.lastPendingFetch = now
		cmds = append(cmds, m.fetchPendingRotations())
	}

	// Agent mail status is light enough to refresh on demand.
	cmds = append(cmds, m.fetchAgentMailStatus())

	return cmds
}

func (m *Model) fetchSessionDataWithOutputsCtx(ctx context.Context) tea.Cmd {
	gen := m.nextGen(refreshSession)
	outputLines := m.paneOutputLines
	budget := m.paneOutputCaptureBudget
	startCursor := m.paneOutputCaptureCursor
	lastCaptured := copyTimeMap(m.paneOutputLastCaptured)
	metadataOnly := !m.initialPaneSnapshotDone
	focusedOnly := m.initialPaneSnapshotDone && !m.initialFocusedPaneHydrationDone
	if metadataOnly {
		// The first visible frame only needs pane metadata. Defer capture-pane work
		// until immediately after first paint to minimize startup latency.
		budget = 0
	} else if focusedOnly {
		// Hydrate only the selected pane before kicking off broader warmup so the
		// first meaningful detail view is not competing with background probes.
		budget = 1
	}

	selectedPaneID := ""
	if m.cursor >= 0 && m.cursor < len(m.panes) {
		selectedPaneID = m.panes[m.cursor].ID
	}

	session := m.session

	return func() tea.Msg {
		start := time.Now()
		if ctx == nil {
			ctx = context.Background()
		}

		panesWithActivity, err := tmux.GetPanesWithActivityContext(ctx, session)
		if err != nil {
			return SessionDataWithOutputMsg{Err: err, Duration: time.Since(start), Gen: gen}
		}

		panes := make([]tmux.Pane, 0, len(panesWithActivity))
		for _, pane := range panesWithActivity {
			panes = append(panes, pane.Pane)
		}

		plan := planPaneCaptures(panesWithActivity, selectedPaneID, lastCaptured, budget, startCursor)

		// Parallelize output capture
		type captureResult struct {
			pane   tmux.PaneActivity
			output string
			err    error
		}

		resultsCh := make(chan captureResult, len(plan.Targets))
		for _, pane := range plan.Targets {
			go func(p tmux.PaneActivity) {
				capCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()

				out, err := tmux.CapturePaneOutputContext(capCtx, p.Pane.ID, outputLines)
				resultsCh <- captureResult{pane: p, output: out, err: err}
			}(pane)
		}

		var outputs []PaneOutputData
		for i := 0; i < len(plan.Targets); i++ {
			select {
			case res := <-resultsCh:
				if res.err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return SessionDataWithOutputMsg{Err: ctxErr, Duration: time.Since(start), Gen: gen}
					}
					continue
				}

				outputs = append(outputs, PaneOutputData{
					PaneID:       res.pane.Pane.ID,
					PaneIndex:    res.pane.Pane.Index,
					LastActivity: res.pane.LastActivity,
					Output:       res.output,
					AgentType:    string(res.pane.Pane.Type), // Simplified mapping
				})
			case <-ctx.Done():
				return SessionDataWithOutputMsg{Err: ctx.Err(), Duration: time.Since(start), Gen: gen}
			}
		}

		if err := ctx.Err(); err != nil {
			return SessionDataWithOutputMsg{Err: err, Duration: time.Since(start), Gen: gen}
		}

		return SessionDataWithOutputMsg{
			Panes:             panes,
			Outputs:           outputs,
			Duration:          time.Since(start),
			NextCaptureCursor: plan.NextCursor,
			MetadataOnly:      metadataOnly,
			FocusedOnly:       focusedOnly,
			Gen:               gen,
		}
	}
}

type paneCapturePlan struct {
	Targets    []tmux.PaneActivity
	NextCursor int
}

func planPaneCaptures(panes []tmux.PaneActivity, selectedPaneID string, lastCaptured map[string]time.Time, budget int, startCursor int) paneCapturePlan {
	var candidates []tmux.PaneActivity
	for _, pane := range panes {
		if pane.Pane.Type == tmux.AgentUser {
			continue
		}
		candidates = append(candidates, pane)
	}

	if budget <= 0 || len(candidates) == 0 {
		next := 0
		if len(candidates) > 0 {
			next = startCursor % len(candidates)
			if next < 0 {
				next = 0
			}
		}
		return paneCapturePlan{Targets: nil, NextCursor: next}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Pane.Index < candidates[j].Pane.Index
	})

	if startCursor < 0 {
		startCursor = 0
	}
	startCursor = startCursor % len(candidates)

	selected := make(map[string]struct{}, budget)
	var targets []tmux.PaneActivity

	if selectedPaneID != "" {
		for _, pane := range candidates {
			if pane.Pane.ID == selectedPaneID {
				selected[pane.Pane.ID] = struct{}{}
				targets = append(targets, pane)
				budget--
				break
			}
		}
	}

	type captureCandidate struct {
		pane tmux.PaneActivity
	}

	var needs []captureCandidate
	if budget > 0 {
		for _, pane := range candidates {
			if _, ok := selected[pane.Pane.ID]; ok {
				continue
			}

			last, ok := lastCaptured[pane.Pane.ID]
			if !ok || pane.LastActivity.After(last) {
				needs = append(needs, captureCandidate{pane: pane})
			}
		}
	}

	sort.Slice(needs, func(i, j int) bool {
		if needs[i].pane.LastActivity.Equal(needs[j].pane.LastActivity) {
			return needs[i].pane.Pane.Index < needs[j].pane.Pane.Index
		}
		return needs[i].pane.LastActivity.After(needs[j].pane.LastActivity)
	})

	for _, c := range needs {
		if budget <= 0 {
			break
		}
		if _, ok := selected[c.pane.Pane.ID]; ok {
			continue
		}
		selected[c.pane.Pane.ID] = struct{}{}
		targets = append(targets, c.pane)
		budget--
	}

	rrSteps := 0
	for budget > 0 && rrSteps < len(candidates) {
		idx := (startCursor + rrSteps) % len(candidates)
		pane := candidates[idx]
		rrSteps++
		if _, ok := selected[pane.Pane.ID]; ok {
			continue
		}
		selected[pane.Pane.ID] = struct{}{}
		targets = append(targets, pane)
		budget--
	}

	nextCursor := startCursor
	if rrSteps > 0 {
		nextCursor = (startCursor + rrSteps) % len(candidates)
	}

	return paneCapturePlan{Targets: targets, NextCursor: nextCursor}
}

func copyTimeMap(src map[string]time.Time) map[string]time.Time {
	if len(src) == 0 {
		return nil
	}

	copied := make(map[string]time.Time, len(src))
	for k, v := range src {
		copied[k] = v
	}
	return copied
}

// fetchStatuses runs unified status detection across all panes
func (m *Model) fetchStatuses() tea.Cmd {
	gen := m.nextGen(refreshStatus)
	return func() tea.Msg {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()

		statuses, err := m.detector.DetectAllContext(ctx, m.session)
		duration := time.Since(start)
		if err != nil {
			// Keep UI responsive even if detection fails
			return StatusUpdateMsg{Statuses: nil, Time: time.Now(), Duration: duration, Err: err, Gen: gen}
		}
		return StatusUpdateMsg{Statuses: statuses, Time: time.Now(), Duration: duration, Gen: gen}
	}
}

// fetchHealthCmd fetches health status for all agents in the session
func (m Model) fetchHealthCmd() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Get health check from health package
		sessionHealth, err := health.CheckSession(ctx, session)
		if err != nil {
			return HealthUpdateMsg{Health: nil, Err: err}
		}

		// Get health tracker for uptime/restart data
		tracker := robot.GetHealthTracker(session)

		// Build health info map
		healthMap := make(map[string]PaneHealthInfo)
		for _, agent := range sessionHealth.Agents {
			info := PaneHealthInfo{
				Status: string(agent.Status),
			}

			// Collect issues
			for _, issue := range agent.Issues {
				info.Issues = append(info.Issues, issue.Message)
			}

			// Get uptime and restart count from tracker
			info.Uptime = int(tracker.GetUptime(agent.PaneID).Seconds())
			info.RestartCount = tracker.GetRestartsInWindow(agent.PaneID)

			healthMap[agent.PaneID] = info
		}

		return HealthUpdateMsg{Health: healthMap, Err: nil}
	}
}

func (m Model) fetchEnsembleModesData() tea.Cmd {
	sessionName := m.session
	panes := make([]tmux.Pane, len(m.panes))
	copy(panes, m.panes)

	return func() tea.Msg {
		sess, err := ensemble.LoadSession(sessionName)
		if errors.Is(err, os.ErrNotExist) {
			sess = nil
			err = nil
		}

		catalog, catErr := ensemble.GlobalCatalog()
		if catErr != nil {
			catalog = nil
		}

		return EnsembleModesDataMsg{
			SessionName: sessionName,
			Session:     sess,
			Catalog:     catalog,
			Panes:       panes,
			Err:         err,
		}
	}
}

// Update implements tea.Model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// If the ensemble modes overlay is active, consume keyboard input there and skip global shortcuts.
	if m.showEnsembleModes {
		if _, ok := msg.(tea.KeyMsg); ok {
			var cmd tea.Cmd
			m.ensembleModes, cmd = m.ensembleModes.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
	}

	// Handle CASS search updates
	passToSearch := true
	if _, ok := msg.(tea.KeyMsg); ok && !m.showCassSearch {
		passToSearch = false
	}

	if passToSearch {
		var cmd tea.Cmd
		m.cassSearch, cmd = m.cassSearch.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	switch msg := msg.(type) {
	case CassSelectMsg:
		m.showCassSearch = false
		m.healthMessage = fmt.Sprintf("Selected: %s", msg.Hit.Title)
		return m, tea.Batch(cmds...)

	case synthtui.CloseMsg:
		m.showEnsembleModes = false
		return m, nil

	case synthtui.RefreshMsg:
		cmds = append(cmds, m.fetchEnsembleModesData())
		return m, tea.Batch(cmds...)

	case synthtui.ZoomMsg:
		pane, ok := m.paneByIndex(msg.PaneIndex)
		if !ok {
			pane = tmux.Pane{Index: msg.PaneIndex}
		}
		return m, m.handlePaneZoom(pane)

	case EnsembleModesDataMsg:
		m.ensembleModes.SetData(msg.SessionName, msg.Session, msg.Catalog, msg.Panes, msg.Err)
		return m, nil

	case panels.ReplayMsg:
		// Handle replay request from history panel
		return m, m.executeReplay(msg.Entry)

	case panels.CopyMsg:
		clip, err := clipboard.New()
		if err != nil {
			m.healthMessage = fmt.Sprintf("Clipboard unavailable: %v", err)
			return m, nil
		}
		if err := clip.Copy(msg.Text); err != nil {
			m.healthMessage = fmt.Sprintf("Copy failed: %v", err)
			return m, nil
		}
		m.healthMessage = "Copied prompt to clipboard"
		return m, nil

	case BeadsUpdateMsg:
		if !m.acceptUpdate(refreshBeads, msg.Gen) {
			return m, nil
		}
		m.fetchingBeads = false
		m.beadsError = msg.Err
		if msg.Err == nil {
			m.beadsSummary = msg.Summary
			m.beadsReady = msg.Ready
			m.markUpdated(refreshBeads, time.Now())
		}
		m.beadsPanel.SetData(m.beadsSummary, m.beadsReady, m.beadsError)
		return m, nil

	case AlertsUpdateMsg:
		if !m.acceptUpdate(refreshAlerts, msg.Gen) {
			return m, nil
		}
		m.fetchingAlerts = false
		m.alertsError = msg.Err
		if msg.Err == nil {
			// Build set of previous alert IDs to detect truly new alerts
			prevIDs := make(map[string]struct{}, len(m.activeAlerts))
			for _, a := range m.activeAlerts {
				prevIDs[a.ID] = struct{}{}
			}
			m.activeAlerts = msg.Alerts
			m.markUpdated(refreshAlerts, time.Now())
			// Push toasts only for alerts not seen in the previous update
			if m.toasts != nil {
				for _, a := range msg.Alerts {
					if _, existed := prevIDs[a.ID]; existed {
						continue
					}
					level := components.ToastInfo
					switch a.Severity {
					case alerts.SeverityCritical:
						level = components.ToastError
					case alerts.SeverityWarning:
						level = components.ToastWarning
					}
					m.toasts.Push(components.Toast{
						ID:      "alert-" + a.ID,
						Message: a.Message,
						Level:   level,
					})
				}
			}
		}
		m.alertsPanel.SetData(m.activeAlerts, m.alertsError)
		return m, nil

	case AttentionUpdateMsg:
		if !m.acceptUpdate(refreshAttention, msg.Gen) {
			return m, nil
		}
		m.fetchingAttention = false
		m.attentionItems = msg.Items
		m.attentionFeedOK = msg.FeedAvailable
		m.markUpdated(refreshAttention, time.Now())
		m.attentionPanel.SetData(m.attentionItems, m.attentionFeedOK)
		m.applyRequestedAttentionCursor()
		return m, nil

	case FileConflictMsg:
		// Add the conflict to the conflicts panel
		m.conflictsPanel.AddConflict(msg.Conflict)
		if m.toasts != nil {
			m.toasts.Push(components.Toast{
				ID:      "conflict-" + msg.Conflict.Path,
				Message: "File conflict: " + msg.Conflict.Path,
				Level:   components.ToastWarning,
			})
		}
		return m, nil

	case panels.ConflictActionResultMsg:
		// Handle conflict action result
		if msg.Err != nil {
			// Log error but don't block
			log.Printf("[Dashboard] Conflict action error: %v", msg.Err)
		}
		// Remove the conflict from the panel if action was successful or dismissed
		if msg.Err == nil {
			m.conflictsPanel.RemoveConflict(msg.Conflict.Path, msg.Conflict.RequestorAgent)
		}
		return m, nil

	case SpawnUpdateMsg:
		if !m.acceptUpdate(refreshSpawn, msg.Gen) {
			return m, nil
		}
		m.fetchingSpawn = false
		m.spawnPanel.SetData(msg.Data)
		m.markUpdated(refreshSpawn, time.Now())

		// Adaptive polling: faster when spawn is active for smooth countdown display,
		// slower when idle to reduce CPU/render churn
		wasActive := m.spawnActive
		m.spawnActive = msg.Data.Active && !msg.Data.IsComplete()

		// Only adjust interval if not overridden by env var (check if it's one of the defaults)
		if m.spawnRefreshInterval == SpawnIdleRefreshInterval || m.spawnRefreshInterval == SpawnActiveRefreshInterval {
			if m.spawnActive {
				m.spawnRefreshInterval = SpawnActiveRefreshInterval
			} else if wasActive && !m.spawnActive {
				// Spawn just completed - switch back to idle rate
				m.spawnRefreshInterval = SpawnIdleRefreshInterval
			}
		}
		return m, nil

	case PTHealthStatesMsg:
		if !m.acceptUpdate(refreshPTHealth, msg.Gen) {
			return m, nil
		}
		m.fetchingPTHealth = false
		if msg.States != nil {
			m.healthStates = msg.States
			m.markUpdated(refreshPTHealth, time.Now())
		}
		return m, nil

	case MetricsUpdateMsg:
		if !m.acceptUpdate(refreshMetrics, msg.Gen) {
			return m, nil
		}
		m.fetchingMetrics = false
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		m.metricsError = msg.Err
		if msg.Err == nil {
			m.metricsData = msg.Data
			m.markUpdated(refreshMetrics, time.Now())
		}
		m.metricsPanel.SetData(m.metricsData, m.metricsError)
		return m, nil

	case HistoryUpdateMsg:
		if !m.acceptUpdate(refreshHistory, msg.Gen) {
			return m, nil
		}
		m.fetchingHistory = false
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		m.historyError = msg.Err
		if msg.Err == nil {
			m.cmdHistory = msg.Entries
			m.markUpdated(refreshHistory, time.Now())
		}
		m.historyPanel.SetEntries(m.cmdHistory, m.historyError)
		return m, nil

	case FileChangeMsg:
		if !m.acceptUpdate(refreshFiles, msg.Gen) {
			return m, nil
		}
		m.fetchingFileChanges = false
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		m.fileChangesError = msg.Err
		if msg.Err == nil {
			m.fileChanges = msg.Changes
			m.markUpdated(refreshFiles, time.Now())
		}
		if m.filesPanel != nil {
			m.filesPanel.SetData(m.fileChanges, m.fileChangesError)
		}
		return m, nil

	case CASSContextMsg:
		if !m.acceptUpdate(refreshCass, msg.Gen) {
			return m, nil
		}
		m.fetchingCassContext = false
		m.cassError = msg.Err
		m.cassContext = msg.Hits
		if m.cassPanel != nil {
			m.cassPanel.SetData(m.cassContext, m.cassError)
		}
		if msg.Err == nil {
			m.markUpdated(refreshCass, time.Now())
		}
		return m, nil

	case TimelineLoadMsg:
		if msg.Err != nil {
			if m.timelinePanel != nil {
				m.timelinePanel.SetData(panels.TimelineData{}, msg.Err)
			}
			return m, nil
		}
		if len(msg.Events) == 0 || m.session == "" {
			return m, nil
		}
		tracker := state.GetGlobalTimelineTracker()
		if len(tracker.GetEventsForSession(m.session, time.Time{})) == 0 {
			for _, event := range msg.Events {
				tracker.RecordEvent(event)
			}
		}
		m.refreshTimelinePanel()
		return m, nil

	case RoutingUpdateMsg:
		if !m.acceptUpdate(refreshRouting, msg.Gen) {
			return m, nil
		}
		m.fetchingRouting = false
		m.routingError = msg.Err
		if msg.Err == nil && msg.Scores != nil {
			m.routingScores = msg.Scores
			m.markUpdated(refreshRouting, time.Now())
		}
		return m, nil

	case tea.WindowSizeMsg:
		updated, cmd := m.handleWindowSize(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return updated, tea.Batch(cmds...)

	case DashboardTickMsg:
		// Only increment animation tick when not in reduce motion mode
		if !m.reduceMotion {
			m.animTick++
		}

		if m.dashboardSprings != nil {
			m.dashboardSprings.Tick()
		}
		m.tickLocalPerfAnimations()

		// Check for idle state transition (fixes #32 - reduce tick rate when idle)
		now := time.Now()
		idleTimeout := m.idleTimeout
		if idleTimeout == 0 {
			idleTimeout = 5 * time.Second
		}
		if !m.lastActivity.IsZero() && time.Since(m.lastActivity) > idleTimeout {
			m.activityState = StateIdle
		}

		// Update ticker panel with current data and animation tick
		m.updateTickerData()

		// Prune expired toasts
		if m.toasts != nil {
			m.toasts.Tick()
		}

		// Drive staggered refreshes on the animation ticker to avoid a single heavy burst.
		if !m.refreshPaused {
			cmds = append(cmds, m.scheduleRefreshes(now)...)
		}

		cmds = append(cmds, m.tick())
		return m, tea.Batch(cmds...)

	case RefreshMsg:
		// Trigger a coordinated refresh across subsystems (coalesced to avoid pile-up).
		return m, tea.Batch(m.fullRefresh(false)...)

	case SessionDataWithOutputMsg:
		if !m.acceptUpdate(refreshSession, msg.Gen) {
			return m, nil
		}
		followUp := m.finishSessionFetch()
		m.sessionFetchLatency = msg.Duration

		if msg.Err != nil {
			// Ignore coalescing cancellations; we'll immediately re-fetch if pending.
			if !errors.Is(msg.Err, context.Canceled) {
				m.err = msg.Err
			}
			return m, followUp
		}

		// Track activity when new pane output arrives (fixes #32)
		if len(msg.Outputs) > 0 {
			m.lastActivity = time.Now()
			m.activityState = StateActive
		}

		m.err = nil
		m.lastRefresh = time.Now()
		m.markUpdated(refreshSession, time.Now())

		{
			prevSelectedID := ""
			if m.cursor >= 0 && m.cursor < len(m.panes) {
				prevSelectedID = m.panes[m.cursor].ID
			}

			// Build old pane ID to index lookup BEFORE updating panes
			// This is used to migrate paneStatus entries to new indices
			oldPaneIDToIdx := make(map[string]int, len(m.panes))
			for _, p := range m.panes {
				oldPaneIDToIdx[p.ID] = p.Index
			}

			sort.Slice(msg.Panes, func(i, j int) bool {
				return msg.Panes[i].Index < msg.Panes[j].Index
			})

			m.panes = msg.Panes
			if m.historyPanel != nil {
				m.historyPanel.SetPanes(m.panes)
			}

			// Migrate paneStatus entries from old indices to new indices by pane ID
			// This prevents stale data when pane indices change (add/remove/reorder)
			newPaneIDToIdx := make(map[string]int, len(m.panes))
			for _, p := range m.panes {
				newPaneIDToIdx[p.ID] = p.Index
			}
			newPaneStatus := make(map[int]PaneStatus, len(m.panes))
			for paneID, newIdx := range newPaneIDToIdx {
				if oldIdx, exists := oldPaneIDToIdx[paneID]; exists {
					// Pane still exists - migrate its status to new index
					if ps, ok := m.paneStatus[oldIdx]; ok {
						newPaneStatus[newIdx] = ps
					}
				}
			}
			m.paneStatus = newPaneStatus

			m.updateStats()
			m.paneOutputCaptureCursor = msg.NextCaptureCursor

			if prevSelectedID != "" {
				for i := range m.panes {
					if m.panes[i].ID == prevSelectedID {
						m.cursor = i
						break
					}
				}
			}
			if len(m.panes) > 0 && m.cursor >= len(m.panes) {
				m.cursor = len(m.panes) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}

			if m.paneOutputCache == nil {
				m.paneOutputCache = make(map[string]string)
			}
			if m.paneOutputLastCaptured == nil {
				m.paneOutputLastCaptured = make(map[string]time.Time)
			}
			if m.renderedOutputCache == nil {
				m.renderedOutputCache = make(map[string]string)
			}

			// Cleanup caches for stale panes
			validPaneIDs := make(map[string]bool, len(m.panes))
			for _, p := range m.panes {
				validPaneIDs[p.ID] = true
			}
			for id := range m.paneOutputCache {
				if !validPaneIDs[id] {
					delete(m.paneOutputCache, id)
				}
			}
			for id := range m.paneOutputLastCaptured {
				if !validPaneIDs[id] {
					delete(m.paneOutputLastCaptured, id)
				}
			}
			for id := range m.renderedOutputCache {
				if !validPaneIDs[id] {
					delete(m.renderedOutputCache, id)
				}
			}

			// Process compaction checks, context tracking, AND live status updates
			timelineUpdated := false
			for _, data := range msg.Outputs {
				prevOutput := ""
				if data.PaneID != "" {
					prevOutput = m.paneOutputCache[data.PaneID]
				}

				// Find the pane to get the variant
				var currentPane tmux.Pane
				found := false
				for _, p := range m.panes {
					if p.ID == data.PaneID {
						currentPane = p
						found = true
						break
					}
				}

				// Map type string to model name for context limits
				statusAgentType := data.AgentType
				modelName := ""
				switch data.AgentType {
				case string(tmux.AgentClaude):
					if m.cfg != nil {
						modelName = m.cfg.Models.DefaultClaude
					} else {
						modelName = "claude-sonnet-4-6"
					}
				case string(tmux.AgentCodex):
					if m.cfg != nil {
						modelName = m.cfg.Models.DefaultCodex
					} else {
						modelName = "gpt-4"
					}
				case string(tmux.AgentGemini):
					if m.cfg != nil {
						modelName = m.cfg.Models.DefaultGemini
					} else {
						modelName = "gemini-2.0-flash"
					}
				case string(tmux.AgentCursor):
					modelName = "cursor"
				case string(tmux.AgentWindsurf):
					modelName = "windsurf"
				case string(tmux.AgentAider):
					modelName = "aider"
				}

				// Use variant if available
				if found && currentPane.Variant != "" {
					modelName = currentPane.Variant
				}

				// Compute delta once (used for cost + local perf).
				delta := ""
				deltaTokens := 0
				if data.PaneID != "" && data.Output != "" {
					delta = tailDelta(prevOutput, data.Output)
					if delta != "" {
						deltaTokens = cost.EstimateTokens(delta)
					}
				}

				// Record estimated output delta tokens for cost tracking BEFORE updating caches.
				if data.PaneID != "" && data.Output != "" {
					m.recordCostOutputDelta(data.PaneID, modelName, prevOutput, data.Output)
				}

				// Update output caches after cost tracking
				if data.PaneID != "" {
					m.paneOutputCache[data.PaneID] = data.Output
					if !data.LastActivity.IsZero() {
						m.paneOutputLastCaptured[data.PaneID] = data.LastActivity
					}
				}

				// Get or create pane status
				ps := m.paneStatus[data.PaneIndex]

				// Local agent performance (Ollama): best-effort token rate + latency tracking.
				// This is based on output deltas and prompt history timestamps, not on Ollama's
				// API token counters, because local panes are typically launched via `ollama run`.
				if isLocalAgentType(statusAgentType) && data.PaneID != "" {
					tr := m.ensureLocalPerfTracker(data.PaneID)
					if tr != nil && deltaTokens > 0 && !data.LastActivity.IsZero() {
						tr.addOutputDelta(data.LastActivity, deltaTokens)
					}
					if tr != nil {
						tps, total, lastLat, avgLat := tr.snapshot()
						ps.LocalTokensPerSecond = tps
						ps.LocalTotalTokens = total
						ps.LocalLastLatency = lastLat
						ps.LocalAvgLatency = avgLat
						ps.LocalTPSHistory = tr.animatedTPSHistory()
					}

					// Refresh /api/ps memory occasionally and map by model name (pane variant).
					if found && currentPane.Variant != "" {
						m.refreshOllamaPSIfNeeded(time.Now())
						if m.ollamaModelMemory != nil {
							if mem, ok := m.ollamaModelMemory[currentPane.Variant]; ok {
								ps.LocalMemoryBytes = mem
							}
						}
					}
				}

				// Update LIVE STATUS using local analysis (avoid waiting for slow full fetch)
				st := m.detector.Analyze(data.PaneID, currentPane.Title, statusAgentType, data.Output, data.LastActivity)

				state := string(st.State)
				// Rate limit check
				if st.State == status.StateError && st.ErrorType == status.ErrorRateLimit {
					state = "rate_limited"
				} else if ps.LastCompaction != nil && state != string(status.StateError) {
					state = "compacted"
				}
				ps.State = state
				ps.TokenVelocity = tokenVelocityFromStatus(st)
				m.agentStatuses[st.PaneID] = st
				if m.recordTimelineStatus(currentPane, st) {
					timelineUpdated = true
				}

				// Calculate context usage
				if data.Output != "" && modelName != "" {
					contextInfo := tokens.GetUsageInfo(data.Output, modelName)
					ps.ContextTokens = contextInfo.EstimatedTokens
					ps.ContextLimit = contextInfo.ContextLimit
					ps.ContextPercent = contextInfo.UsagePercent
					ps.ContextModel = modelName
				}

				// Compaction check
				event, recoverySent, _ := m.compaction.CheckAndRecover(data.Output, statusAgentType, m.session, data.PaneIndex)

				if event != nil {
					now := time.Now()
					ps.LastCompaction = &now
					ps.RecoverySent = recoverySent
					ps.State = "compacted"
				}

				m.paneStatus[data.PaneIndex] = ps
			}
			if timelineUpdated {
				m.refreshTimelinePanel()
			}

			// Refresh cost panel from prompt history + accumulated output deltas.
			now := time.Now()
			m.updateCostFromPrompts(now)
			m.refreshCostPanel(now)
		}

		warmupCmds := []tea.Cmd{followUp}
		if msg.MetadataOnly {
			m.initialPaneSnapshotDone = true
			if cmd := m.requestSessionFetch(false); cmd != nil {
				warmupCmds = append(warmupCmds, cmd)
			}
			if cmd := m.startRendererInit(); cmd != nil {
				warmupCmds = append(warmupCmds, cmd)
			}
			if cmd := m.startConfigWatcher(); cmd != nil {
				warmupCmds = append(warmupCmds, cmd)
			}
			return m, tea.Batch(warmupCmds...)
		}

		if msg.FocusedOnly {
			m.initialFocusedPaneHydrationDone = true
			if cmd := m.requestSessionFetch(false); cmd != nil {
				warmupCmds = append(warmupCmds, cmd)
			}
		}

		if m.startupWarmupDone {
			return m, tea.Batch(warmupCmds...)
		}
		m.startupWarmupDone = true

		if cmd := m.startRendererInit(); cmd != nil {
			warmupCmds = append(warmupCmds, cmd)
		}
		if cmd := m.startConfigWatcher(); cmd != nil {
			warmupCmds = append(warmupCmds, cmd)
		}
		if cmd := m.requestStatusesFetch(); cmd != nil {
			warmupCmds = append(warmupCmds, cmd)
		}
		warmupCmds = append(warmupCmds, m.fetchTimelineCmd())
		if m.healthStatus == "unknown" && m.healthMessage == "" {
			warmupCmds = append(warmupCmds, m.fetchHealthStatus())
		}
		warmupCmds = append(warmupCmds, m.fetchAgentMailStatus())
		if !m.fetchingMailInbox {
			m.fetchingMailInbox = true
			m.lastMailInboxFetch = time.Now()
			warmupCmds = append(warmupCmds, m.fetchAgentMailInboxes())
		}
		if !m.fetchingPTHealth {
			m.fetchingPTHealth = true
			warmupCmds = append(warmupCmds, m.fetchPTHealthStatesCmd())
		}
		if !m.fetchingPendingRot {
			m.fetchingPendingRot = true
			m.lastPendingFetch = time.Now()
			warmupCmds = append(warmupCmds, m.fetchPendingRotations())
		}
		if !m.fetchingCheckpoint {
			m.fetchingCheckpoint = true
			m.lastCheckpointFetch = time.Now()
			warmupCmds = append(warmupCmds, m.fetchCheckpointStatus())
		}
		if !m.fetchingHandoff {
			m.fetchingHandoff = true
			m.lastHandoffFetch = time.Now()
			warmupCmds = append(warmupCmds, m.fetchHandoffCmd())
		}
		if !m.fetchingSpawn && m.spawnPanel != nil {
			m.fetchingSpawn = true
			m.lastSpawnFetch = time.Now()
			warmupCmds = append(warmupCmds, m.fetchSpawnStateCmd())
		}

		return m, tea.Batch(warmupCmds...)

	case StatusUpdateMsg:
		if !m.acceptUpdate(refreshStatus, msg.Gen) {
			return m, nil
		}
		followUp := m.finishStatusesFetch()
		m.statusFetchLatency = msg.Duration
		m.statusFetchErr = msg.Err
		// Build index lookup for current panes
		paneIndexByID := make(map[string]int)
		paneByID := make(map[string]tmux.Pane)
		for _, p := range m.panes {
			paneIndexByID[p.ID] = p.Index
			paneByID[p.ID] = p
		}

		// Best-effort refresh of Ollama /api/ps, only if we have any local panes.
		hasOllama := false
		for _, p := range m.panes {
			if string(p.Type) == "ollama" {
				hasOllama = true
				break
			}
		}
		if hasOllama {
			m.refreshOllamaPSIfNeeded(msg.Time)
		}

		timelineUpdated := false
		for _, st := range msg.Statuses {
			idx, ok := paneIndexByID[st.PaneID]
			if !ok {
				continue
			}

			ps := m.paneStatus[idx]
			state := string(st.State)

			// Rate limit should be shown with special indicator
			if st.State == status.StateError && st.ErrorType == status.ErrorRateLimit {
				state = "rate_limited"
			} else if ps.LastCompaction != nil && state != string(status.StateError) {
				// Compaction warning should override idle/working but not errors
				state = "compacted"
			}
			ps.State = state

			// Pre-calculate token velocity
			ps.TokenVelocity = tokenVelocityFromStatus(st)

			// Local perf snapshot + memory enrichment (Ollama panes only).
			if pane, ok := paneByID[st.PaneID]; ok && string(pane.Type) == "ollama" {
				if tr := m.ensureLocalPerfTracker(st.PaneID); tr != nil {
					tps, total, lastLat, avgLat := tr.snapshot()
					ps.LocalTokensPerSecond = tps
					ps.LocalTotalTokens = total
					ps.LocalLastLatency = lastLat
					ps.LocalAvgLatency = avgLat
					ps.LocalTPSHistory = tr.animatedTPSHistory()
				}
				if hasOllama && m.ollamaModelMemory != nil && pane.Variant != "" {
					if mem, ok := m.ollamaModelMemory[pane.Variant]; ok {
						ps.LocalMemoryBytes = mem
					}
				}
			}

			m.paneStatus[idx] = ps
			m.agentStatuses[st.PaneID] = st
			if m.recordTimelineStatus(paneByID[st.PaneID], st) {
				timelineUpdated = true
			}

			// Cache expensive markdown rendering
			if st.LastOutput != "" && m.renderer != nil {
				rendered, err := m.renderer.Render(st.LastOutput)
				if err == nil {
					if m.renderedOutputCache == nil {
						m.renderedOutputCache = make(map[string]string)
					}
					m.renderedOutputCache[st.PaneID] = rendered
				}
			}
		}
		if timelineUpdated {
			m.refreshTimelinePanel()
		}
		if msg.Err == nil {
			m.markUpdated(refreshStatus, msg.Time)
		}
		m.lastRefresh = msg.Time
		// Also refresh health data after status update
		return m, tea.Batch(followUp, m.fetchHealthCmd())

	case HealthUpdateMsg:
		if msg.Err == nil && msg.Health != nil {
			// Build pane ID to index lookup
			paneIndexByID := make(map[string]int)
			for _, p := range m.panes {
				paneIndexByID[p.ID] = p.Index
			}

			// Update pane status with health info
			for paneID, healthInfo := range msg.Health {
				idx, ok := paneIndexByID[paneID]
				if !ok {
					continue
				}

				ps := m.paneStatus[idx]
				ps.HealthStatus = healthInfo.Status
				ps.HealthIssues = healthInfo.Issues
				ps.RestartCount = healthInfo.RestartCount
				ps.UptimeSeconds = healthInfo.Uptime
				m.paneStatus[idx] = ps
			}
		}
		return m, nil

	case ConfigReloadMsg:
		if msg.Config != nil {
			m.cfg = msg.Config
			m.helpVerbosity = normalizedHelpVerbosity(msg.Config.HelpVerbosity)
			// Update theme
			m.theme = theme.FromName(msg.Config.Theme)
			// Reload icons (if dependent on config in future, pass cfg)
			m.icons = icons.Current()

			// Follow integrations.rano.poll_interval_ms (default 1000ms).
			if msg.Config.Integrations.Rano.PollIntervalMs > 0 {
				m.ranoNetworkRefreshInterval = time.Duration(msg.Config.Integrations.Rano.PollIntervalMs) * time.Millisecond
			}

			// Re-initialize renderer with new theme colors
			_, detailWidth := layout.SplitProportions(m.width)
			contentWidth := detailWidth - 4
			if contentWidth < 20 {
				contentWidth = 20
			}
			m.initRenderer(contentWidth)

			// If help verbosity changed, ensure focused panel remains valid.
			m.cycleFocus(0)
		}
		return m, m.subscribeToConfig()

	case ConfigWatcherReadyMsg:
		if msg.Err == nil && msg.Closer != nil && m.configCloser == nil {
			m.configCloser = msg.Closer
			return m, m.subscribeToConfig()
		}
		return m, nil

	case RendererReadyMsg:
		if msg.Renderer != nil {
			m.renderer = msg.Renderer
			m.renderedOutputCache = make(map[string]string)
		}
		return m, nil

	case HealthCheckMsg:
		m.healthStatus = msg.Status
		m.healthMessage = msg.Message
		return m, nil

	case ScanStatusMsg:
		if !m.acceptUpdate(refreshScan, msg.Gen) {
			return m, nil
		}
		followUp := m.finishScanFetch()
		if msg.Err != nil && errors.Is(msg.Err, context.Canceled) {
			return m, followUp
		}

		// Only update badge state if the scan actually produced a new result.
		if msg.Status != "" {
			m.scanStatus = msg.Status
			m.scanTotals = msg.Totals
			m.scanDuration = msg.Duration
			m.markUpdated(refreshScan, time.Now())
		}
		return m, followUp

	case AgentMailUpdateMsg:
		if !m.acceptUpdate(refreshAgentMail, msg.Gen) {
			return m, nil
		}
		m.agentMailAvailable = msg.Available
		m.agentMailConnected = msg.Connected
		m.agentMailArchiveFound = msg.ArchiveFound
		m.agentMailLocks = msg.Locks
		m.agentMailLockInfo = msg.LockInfo
		m.markUpdated(refreshAgentMail, time.Now())
		return m, nil

	case AgentMailInboxSummaryMsg:
		if !m.acceptUpdate(refreshAgentMailInbox, msg.Gen) {
			return m, nil
		}
		m.fetchingMailInbox = false
		m.lastMailInboxFetch = time.Now()

		if msg.Err == nil {
			m.agentMailInbox = msg.Inboxes
			m.agentMailAgents = msg.AgentMap

			// Build index lookup for current panes
			paneIndexByID := make(map[string]int)
			for _, p := range m.panes {
				paneIndexByID[p.ID] = p.Index
			}

			// Calculate totals and update per-pane status
			unread := 0
			urgent := 0
			attentionFeed := robot.GetAttentionFeed()
			for paneID, msgs := range msg.Inboxes {
				paneUnread := len(msgs)
				paneUrgent := 0
				toAgent := msg.AgentMap[paneID]
				for _, mm := range msgs {
					if strings.EqualFold(mm.Importance, "urgent") {
						paneUrgent++
					}
					// Emit attention signals for new unread messages
					if _, seen := m.seenMailAttentionIDs[mm.ID]; !seen {
						m.seenMailAttentionIDs[mm.ID] = struct{}{}
						threadID := ""
						if mm.ThreadID != nil {
							threadID = *mm.ThreadID
						}
						if mm.AckRequired {
							attentionFeed.PublishMailAckRequired(mm.From, toAgent, mm.Subject, mm.ID, threadID)
						} else {
							attentionFeed.PublishMailPending(mm.From, toAgent, mm.Subject, mm.ID, threadID)
						}
					}
				}
				unread += paneUnread
				urgent += paneUrgent

				// Update pane status
				if idx, ok := paneIndexByID[paneID]; ok {
					ps := m.paneStatus[idx]
					ps.MailUnread = paneUnread
					ps.MailUrgent = paneUrgent
					m.paneStatus[idx] = ps
				}
			}
			m.agentMailUnread = unread
			m.agentMailUrgent = urgent
			m.markUpdated(refreshAgentMailInbox, time.Now())
		}
		return m, nil

	case AgentMailInboxDetailMsg:
		if !m.acceptUpdate(refreshAgentMailInbox, msg.Gen) {
			return m, nil
		}
		if msg.Err != nil {
			m.agentMailInboxErrors[msg.PaneID] = msg.Err
		} else {
			delete(m.agentMailInboxErrors, msg.PaneID)
			m.agentMailInbox[msg.PaneID] = msg.Messages
		}
		return m, nil

	case CheckpointUpdateMsg:
		if !m.acceptUpdate(refreshCheckpoint, msg.Gen) {
			return m, nil
		}
		m.fetchingCheckpoint = false
		m.lastCheckpointFetch = time.Now()
		if msg.Err != nil {
			m.checkpointError = msg.Err
			// Clear stale data on error
			m.checkpointCount = 0
			m.checkpointStatus = "none"
			m.latestCheckpoint = nil
		} else {
			m.checkpointCount = msg.Count
			m.latestCheckpoint = msg.Latest
			m.checkpointStatus = msg.Status
			m.checkpointError = nil
			m.markUpdated(refreshCheckpoint, time.Now())
		}
		return m, nil

	case RanoNetworkUpdateMsg:
		if !m.acceptUpdate(refreshRanoNetwork, msg.Gen) {
			return m, nil
		}
		m.fetchingRanoNetwork = false
		m.lastRanoNetworkFetch = time.Now()
		if m.ranoNetworkPanel != nil {
			m.ranoNetworkPanel.SetData(msg.Data)
		}
		if msg.Data.Error == nil {
			m.markUpdated(refreshRanoNetwork, time.Now())
		}
		return m, nil

	case RCHStatusUpdateMsg:
		if !m.acceptUpdate(refreshRCH, msg.Gen) {
			return m, nil
		}
		m.fetchingRCH = false
		m.lastRCHFetch = time.Now()
		if m.rchPanel != nil {
			m.rchPanel.SetData(msg.Data)
		}
		if msg.Data.Error == nil {
			m.markUpdated(refreshRCH, time.Now())
		}
		wasActive := m.rchActive
		m.rchActive = rchStatusActive(msg.Data.Status)
		if m.rchRefreshInterval == RCHIdleRefreshInterval || m.rchRefreshInterval == RCHActiveRefreshInterval {
			if m.rchActive {
				m.rchRefreshInterval = RCHActiveRefreshInterval
			} else if wasActive && !m.rchActive {
				m.rchRefreshInterval = RCHIdleRefreshInterval
			}
		}
		return m, nil

	case DCGStatusUpdateMsg:
		if !m.acceptUpdate(refreshDCG, msg.Gen) {
			return m, nil
		}
		m.fetchingDCG = false
		m.lastDCGFetch = time.Now()
		if msg.Err != nil {
			m.dcgError = msg.Err
		} else {
			m.dcgEnabled = msg.Enabled
			m.dcgAvailable = msg.Available
			m.dcgVersion = msg.Version
			m.dcgBlocked = msg.Blocked
			m.dcgLastBlocked = msg.LastBlocked
			m.dcgError = nil
			m.markUpdated(refreshDCG, time.Now())
		}
		return m, nil

	case PendingRotationsUpdateMsg:
		if !m.acceptUpdate(refreshPendingRotations, msg.Gen) {
			return m, nil
		}
		m.fetchingPendingRot = false
		m.lastPendingFetch = time.Now()
		m.pendingRotationsErr = msg.Err
		if msg.Err == nil {
			m.pendingRotations = msg.Pending
			m.markUpdated(refreshPendingRotations, time.Now())
		}
		// Update the panel with the new data
		if m.rotationConfirmPanel != nil {
			m.rotationConfirmPanel.SetData(m.pendingRotations, m.pendingRotationsErr)
		}
		return m, nil

	case panels.RotationConfirmActionMsg:
		// Handle rotation confirmation action from the panel
		return m, m.executeRotationConfirmAction(msg.AgentID, msg.Action)

	case RotationConfirmResultMsg:
		// Handle the result of a rotation confirmation action
		if msg.Err != nil {
			m.healthMessage = fmt.Sprintf("Rotation action failed: %v", msg.Err)
		} else if !msg.Success {
			m.healthMessage = msg.Message
		} else {
			m.healthMessage = msg.Message
		}
		// Refresh the pending rotations list
		return m, m.fetchPendingRotations()

	case HandoffUpdateMsg:
		if !m.acceptUpdate(refreshHandoff, msg.Gen) {
			return m, nil
		}
		m.fetchingHandoff = false
		m.lastHandoffFetch = time.Now()
		m.handoffError = msg.Err
		m.handoffGoal = msg.Goal
		m.handoffNow = msg.Now
		m.handoffAge = msg.Age
		m.handoffPath = msg.Path
		m.handoffStatus = msg.Status
		if msg.Err == nil {
			m.markUpdated(refreshHandoff, time.Now())
		}
		return m, nil

	case CheckpointCreatedMsg:
		if msg.Err != nil {
			m.checkpointError = msg.Err
		} else {
			// Refresh checkpoint status after creation
			m.latestCheckpoint = msg.Checkpoint
			m.checkpointCount++
			m.checkpointStatus = "recent"
			m.checkpointError = nil
		}
		return m, nil

	case tea.MouseMsg:
		// Track activity for adaptive tick rate
		m.lastActivity = time.Now()
		m.activityState = StateActive

		// Log mouse events in debug mode
		if os.Getenv("NTM_DEBUG") == "1" {
			log.Printf("mouse: button=%d x=%d y=%d action=%v", msg.Button, msg.X, msg.Y, msg.Action)
		}

		// Handle mouse wheel for scrolling pane selection
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.cursor > 0 {
				m.cursor--
				m.paneList.Select(m.cursor)
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			if m.cursor < len(m.panes)-1 {
				m.cursor++
				m.paneList.Select(m.cursor)
			}
			return m, nil
		case tea.MouseButtonLeft:
			if msg.Action == tea.MouseActionPress {
				// Click to dismiss toasts (individual toast hit-testing)
				if m.toasts != nil && m.toasts.Count() > 0 {
					// Toast area is positioned at top-right
					toastWidth := m.width / 3
					if toastWidth > 60 {
						toastWidth = 60
					}
					toastStackHeight := m.toasts.ToastStackHeight()
					// Check if click is within toast area bounds
					if msg.X > m.width-toastWidth-5 && msg.Y < toastStackHeight+1 {
						// Find which toast was clicked based on Y position
						toastID := m.toasts.ToastAtPosition(msg.Y)
						if toastID != "" {
							m.toasts.Dismiss(toastID)
						} else {
							// Click in toast area but not on a specific toast - dismiss all
							m.toasts.DismissAll()
						}
						return m, nil
					}
				}
				// Click on pane list to select (left side of dashboard)
				if msg.X < m.width/4 && msg.Y > 3 {
					// Map Y position to pane index (rough heuristic)
					paneIndex := (msg.Y - 4) / 2
					if paneIndex >= 0 && paneIndex < len(m.panes) {
						m.setFocusedPanel(PanelPaneList)
						m.cursor = paneIndex
						m.paneList.Select(m.cursor)
						return m, nil
					}
				}
			}
		}
		return m, nil

	case tea.KeyMsg:
		// Track activity for adaptive tick rate (fixes #32)
		m.lastActivity = time.Now()
		m.activityState = StateActive
		paneListKeyHandled := false

		// Handle help overlay: Esc or ? closes it
		if m.showHelp {
			if msg.String() == "esc" || msg.String() == "?" {
				m.showHelp = false
			}
			return m, nil
		}

		if m.showCassSearch {
			if msg.String() == "esc" {
				m.showCassSearch = false
			}
			return m, tea.Batch(cmds...)
		}

		// Popup mode: Escape closes the overlay (exits cleanly)
		if m.popupMode && msg.String() == "esc" {
			return m, m.exitPopupOverlay()
		}

		if m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.paneList, cmd = m.paneList.Update(msg)
			m.syncCursorFromPaneList()
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, dashKeys.NextPanel):
			m.cycleFocus(1)
			return m, nil

		case key.Matches(msg, dashKeys.PrevPanel):
			m.cycleFocus(-1)
			return m, nil
		case key.Matches(msg, dashKeys.CassSearch):
			m.showCassSearch = true
			searchW := int(float64(m.width) * 0.6)
			searchH := int(float64(m.height) * 0.6)
			m.cassSearch.SetSize(searchW, searchH)
			cmds = append(cmds, m.cassSearch.Init())
			return m, tea.Batch(cmds...)

		case key.Matches(msg, dashKeys.EnsembleModes):
			m.showEnsembleModes = true
			m.showCassSearch = false
			m.showHelp = false

			modesW := m.width - 10
			modesH := m.height - 6
			if modesW < 30 {
				modesW = 30
			}
			if modesH < 10 {
				modesH = 10
			}
			m.ensembleModes.SetSize(modesW, modesH)

			cmds = append(cmds, m.fetchEnsembleModesData())
			return m, tea.Batch(cmds...)

		case key.Matches(msg, dashKeys.Help):
			m.showHelp = !m.showHelp
			return m, nil
		case key.Matches(msg, dashKeys.Diagnostics):
			m.showDiagnostics = !m.showDiagnostics
			return m, nil
		case key.Matches(msg, dashKeys.ScanToggle):
			m.scanDisabled = !m.scanDisabled
			return m, nil

		case key.Matches(msg, dashKeys.Quit):
			m.quitting = true
			// In popup mode, ensure no post-quit action (clean close).
			if m.popupMode {
				m.postQuitAction = nil
			}
			m.cleanup()
			return m, tea.Quit

		case key.Matches(msg, dashKeys.Up):
			if m.focusedPanel == PanelPaneList || m.focusedPanel == PanelDetail {
				paneListKeyHandled = m.focusedPanel == PanelPaneList
				if m.cursor > 0 {
					m.cursor--
					m.paneList.Select(m.cursor)
				}
			}

		case key.Matches(msg, dashKeys.Down):
			if m.focusedPanel == PanelPaneList || m.focusedPanel == PanelDetail {
				paneListKeyHandled = m.focusedPanel == PanelPaneList
				if m.cursor < len(m.panes)-1 {
					m.cursor++
					m.paneList.Select(m.cursor)
				}
			}

		case key.Matches(msg, dashKeys.Refresh):
			// Manual refresh (coalesced; cancels in-flight where supported)
			return m, tea.Batch(m.fullRefresh(true)...)

		case key.Matches(msg, dashKeys.ContextRefresh):
			// Force context refresh (same as regular refresh but with user intent to see context)
			return m, tea.Batch(m.fullRefresh(true)...)

		case key.Matches(msg, dashKeys.MailRefresh):
			// Refresh Agent Mail data
			return m, m.fetchAgentMailStatus()

		case key.Matches(msg, dashKeys.InboxToggle):
			if m.cursor >= 0 && m.cursor < len(m.panes) {
				p := m.panes[m.cursor]
				return m, m.fetchAgentMailInboxDetails(p)
			}

		case key.Matches(msg, dashKeys.Checkpoint):
			// Create a new checkpoint for the session
			return m, m.createCheckpointCmd()

		case key.Matches(msg, dashKeys.Zoom):
			if (m.focusedPanel == PanelPaneList || m.focusedPanel == PanelDetail) && len(m.panes) > 0 && m.cursor < len(m.panes) {
				return m, m.handlePaneZoom(m.panes[m.cursor])
			}
			// Zoom-to-source for attention panel items
			if m.focusedPanel == PanelAttention && m.attentionPanel != nil {
				if item := m.attentionPanel.SelectedItem(); item != nil {
					return m, m.handleAttentionZoom(item.SourcePane, item.Cursor)
				}
			}
			return m, nil

		// Number quick-select
		case key.Matches(msg, dashKeys.Num1):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(1)
		case key.Matches(msg, dashKeys.Num2):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(2)
		case key.Matches(msg, dashKeys.Num3):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(3)
		case key.Matches(msg, dashKeys.Num4):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(4)
		case key.Matches(msg, dashKeys.Num5):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(5)
		case key.Matches(msg, dashKeys.Num6):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(6)
		case key.Matches(msg, dashKeys.Num7):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(7)
		case key.Matches(msg, dashKeys.Num8):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(8)
		case key.Matches(msg, dashKeys.Num9):
			paneListKeyHandled = m.focusedPanel == PanelPaneList && m.paneList.FilterState() == list.Filtering
			m.selectByNumber(9)
		}
		switch m.focusedPanel {
		case PanelPaneList:
			if !paneListKeyHandled {
				var cmd tea.Cmd
				m.paneList, cmd = m.paneList.Update(msg)
				m.syncCursorFromPaneList()
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}

		case PanelHistory:
			if m.historyPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.historyPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelSidebar:
			// For sidebar, forward to the focused sub-panel
			if m.timelinePanel != nil && m.timelinePanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.timelinePanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.historyPanel != nil && m.historyPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.historyPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.filesPanel != nil && m.filesPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.filesPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.cassPanel != nil && m.cassPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.cassPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.costPanel != nil && m.costPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.costPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.metricsPanel != nil && m.metricsPanel.IsFocused() {
				var cmd tea.Cmd
				_, cmd = m.metricsPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelBeads:
			if m.beadsPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.beadsPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelAlerts:
			if m.alertsPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.alertsPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelAttention:
			if m.attentionPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.attentionPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		case PanelConflicts:
			if m.conflictsPanel != nil {
				var cmd tea.Cmd
				_, cmd = m.conflictsPanel.Update(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}

		// Also forward to rotation confirm panel when it has pending rotations
		if m.rotationConfirmPanel != nil && m.rotationConfirmPanel.HasPending() && m.rotationConfirmPanel.IsFocused() {
			var cmd tea.Cmd
			_, cmd = m.rotationConfirmPanel.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	if len(cmds) > 0 {
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m *Model) selectByNumber(n int) {
	idx := n - 1
	if idx >= 0 && idx < len(m.panes) {
		m.cursor = idx
	}
}

func (m *Model) cycleFocus(dir int) {
	if dir == 0 {
		m.syncFocusRing()
		return
	}

	m.refreshFocusRing()

	previous := m.focusedPanel
	if dir > 0 {
		m.focusRing.Next()
	} else {
		m.focusRing.Prev()
	}

	current := m.focusRing.Current()
	if current.ID == "" {
		return
	}

	m.focusedPanel = current.Panel
	m.syncFocusAnimations()
	if previous != m.focusedPanel {
		logFocusf("focus: %s -> %s", panelIDString(previous), current.ID)
	}
}

func (m *Model) updateStats() {
	m.claudeCount = 0
	m.codexCount = 0
	m.geminiCount = 0
	m.cursorCount = 0
	m.windsurfCount = 0
	m.aiderCount = 0
	m.userCount = 0

	for _, p := range m.panes {
		switch p.Type {
		case tmux.AgentClaude:
			m.claudeCount++
		case tmux.AgentCodex:
			m.codexCount++
		case tmux.AgentGemini:
			m.geminiCount++
		case tmux.AgentCursor:
			m.cursorCount++
		case tmux.AgentWindsurf:
			m.windsurfCount++
		case tmux.AgentAider:
			m.aiderCount++
		default:
			m.userCount++
		}
	}
}

func (m *Model) seedInitialPanes(panesWithActivity []tmux.PaneActivity) {
	if len(panesWithActivity) == 0 {
		return
	}

	seeded := make([]tmux.Pane, 0, len(panesWithActivity))
	for _, pane := range panesWithActivity {
		seeded = append(seeded, pane.Pane)
	}
	sort.Slice(seeded, func(i, j int) bool {
		return seeded[i].Index < seeded[j].Index
	})

	m.panes = seeded
	m.initialPaneSnapshotDone = true
	m.updateStats()
	_ = m.rebuildPaneList()
	m.lastRefresh = time.Now()
}

// updateTickerData updates the ticker panel with current dashboard data
func (m *Model) updateTickerData() {
	// Count active agents (those with any known status, not just "working")
	// "Active" means status has been determined - could be working, idle, error, or compacted
	// This gives a more accurate picture than only counting actively working agents
	activeAgents := 0
	for _, ps := range m.paneStatus {
		// Count as active if state is known (non-empty)
		// Empty or missing state means status detection hasn't run yet
		if ps.State != "" {
			activeAgents++
		}
	}
	// Fallback: if no panes have determined status yet but we have panes, count agent panes
	// (excludes user panes which are type "user" or empty)
	// Note: We check activeAgents==0 rather than len(paneStatus)==0 because paneStatus
	// may have entries with empty State when status detection is still pending
	if activeAgents == 0 && len(m.panes) > 0 {
		// Status detection hasn't populated yet; show total agents as placeholder
		// This prevents showing "0/17" when we simply haven't fetched status yet
		activeAgents = m.claudeCount + m.codexCount + m.geminiCount + m.cursorCount + m.windsurfCount + m.aiderCount
	}

	// Count alerts by severity
	var critAlerts, warnAlerts, infoAlerts int
	for _, a := range m.activeAlerts {
		switch a.Severity {
		case alerts.SeverityCritical:
			critAlerts++
		case alerts.SeverityWarning:
			warnAlerts++
		default:
			infoAlerts++
		}
	}

	// Build ticker data from dashboard state
	data := panels.TickerData{
		TotalAgents:      len(m.panes),
		ActiveAgents:     activeAgents,
		ClaudeCount:      m.claudeCount,
		CodexCount:       m.codexCount,
		GeminiCount:      m.geminiCount,
		CursorCount:      m.cursorCount,
		WindsurfCount:    m.windsurfCount,
		AiderCount:       m.aiderCount,
		UserCount:        m.userCount,
		CriticalAlerts:   critAlerts,
		WarningAlerts:    warnAlerts,
		InfoAlerts:       infoAlerts,
		ReadyBeads:       m.beadsSummary.Ready,
		InProgressBeads:  m.beadsSummary.InProgress,
		BlockedBeads:     m.beadsSummary.Blocked,
		UnreadMessages:   m.agentMailUnread,
		ActiveLocks:      m.agentMailLocks,
		MailConnected:    m.agentMailConnected,
		MailAvailable:    m.agentMailAvailable,
		MailArchiveFound: m.agentMailArchiveFound,
		CheckpointCount:  m.checkpointCount,
		CheckpointStatus: m.checkpointStatus,
		BugsCritical:     m.bugsCritical,
		BugsWarning:      m.bugsWarning,
		BugsInfo:         m.bugsInfo,
		BugsScanned:      m.bugsScanned,
	}

	m.tickerPanel.SetData(data)
	m.tickerPanel.SetAnimTick(m.animTick)
}

func (m Model) renderPaneGrid() string {
	t := m.theme
	ic := m.icons

	var lines []string

	// Calculate adaptive card dimensions based on terminal width
	// Uses beads_viewer-inspired algorithm with min/max constraints
	const (
		minCardWidth = 22 // Minimum usable card width
		maxCardWidth = 45 // Maximum card width for readability
		cardGap      = 2  // Gap between cards
	)

	availableWidth := m.width - 4 // Account for margins
	cardWidth, cardsPerRow := styles.AdaptiveCardDimensions(availableWidth, minCardWidth, maxCardWidth, cardGap)

	// In grid mode (used below Split threshold), show more detail when card width allows it.
	showExtendedInfo := cardWidth >= 24

	rows := BuildPaneTableRows(m.panes, m.agentStatuses, m.paneStatus, &m.beadsSummary, m.fileChanges, m.healthStates, m.animTick, t)
	if summary := activitySummaryLine(rows, t); summary != "" {
		lines = append(lines, "  "+summary)
	}
	contextRanks := m.computeContextRanks()

	// Compact grids often render many panes that share identical visual fragments
	// (status badges, model badges, context bars, token labels). Cache those pure
	// sub-render results for this pass so we only pay the lipgloss/style cost once
	// per unique input tuple.
	type badgeCacheKey struct {
		text       string
		bg         string
		fg         string
		style      styles.BadgeStyle
		bold       bool
		showIcon   bool
		fixedWidth int
	}
	badgeCache := make(map[badgeCacheKey]string, 16)
	cachedTextBadge := func(text string, bgColor, fgColor lipgloss.Color, opt styles.BadgeOptions) string {
		key := badgeCacheKey{
			text:       text,
			bg:         string(bgColor),
			fg:         string(fgColor),
			style:      opt.Style,
			bold:       opt.Bold,
			showIcon:   opt.ShowIcon,
			fixedWidth: opt.FixedWidth,
		}
		if rendered, ok := badgeCache[key]; ok {
			return rendered
		}
		rendered := styles.TextBadge(text, bgColor, fgColor, opt)
		badgeCache[key] = rendered
		return rendered
	}

	type styledTextCacheKey struct {
		text   string
		fg     string
		bold   bool
		italic bool
	}
	type styledTextStyleKey struct {
		fg     string
		bold   bool
		italic bool
	}
	styledTextCache := make(map[styledTextCacheKey]string, 24)
	styledTextStyleCache := make(map[styledTextStyleKey]lipgloss.Style, 8)
	styledTextStyle := func(fg lipgloss.Color, bold, italic bool) lipgloss.Style {
		key := styledTextStyleKey{
			fg:     string(fg),
			bold:   bold,
			italic: italic,
		}
		if style, ok := styledTextStyleCache[key]; ok {
			return style
		}
		style := lipgloss.NewStyle().Foreground(fg)
		if bold {
			style = style.Bold(true)
		}
		if italic {
			style = style.Italic(true)
		}
		styledTextStyleCache[key] = style
		return style
	}
	cachedStyledText := func(text string, fg lipgloss.Color, bold, italic bool) string {
		key := styledTextCacheKey{
			text:   text,
			fg:     string(fg),
			bold:   bold,
			italic: italic,
		}
		if rendered, ok := styledTextCache[key]; ok {
			return rendered
		}
		rendered := styledTextStyle(fg, bold, italic).Render(text)
		styledTextCache[key] = rendered
		return rendered
	}

	type contextBarCacheKey struct {
		percent float64
		width   int
	}
	contextBarCache := make(map[contextBarCacheKey]string, 8)
	cachedContextBar := func(percent float64, width int) string {
		key := contextBarCacheKey{percent: percent, width: width}
		if rendered, ok := contextBarCache[key]; ok {
			return rendered
		}
		rendered := m.renderContextBar(percent, width)
		contextBarCache[key] = rendered
		return rendered
	}

	type cardStyleKey struct {
		borderColor string
		selected    bool
	}
	cardBaseStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(cardWidth).
		Padding(0, 1)
	cardStyleCache := make(map[cardStyleKey]lipgloss.Style, 8)
	cachedCardStyle := func(borderColor lipgloss.Color, selected bool) lipgloss.Style {
		key := cardStyleKey{
			borderColor: string(borderColor),
			selected:    selected,
		}
		if style, ok := cardStyleCache[key]; ok {
			return style
		}
		style := cardBaseStyle.BorderForeground(borderColor)
		if selected {
			style = style.Background(t.Surface0)
		}
		cardStyleCache[key] = style
		return style
	}

	activityBadgeCache := make(map[string]string, 8)
	cachedActivityBadge := func(state string) string {
		if badge, ok := activityBadgeCache[state]; ok {
			return badge
		}
		label, color := activityLabelAndColor(state, t)
		if label == "" {
			activityBadgeCache[state] = ""
			return ""
		}
		badge := cachedTextBadge(label, color, t.Base, styles.BadgeOptions{
			Style:    styles.BadgeStyleCompact,
			Bold:     true,
			ShowIcon: false,
		})
		activityBadgeCache[state] = badge
		return badge
	}

	var cards []string

	for i, p := range m.panes {
		row := rows[i]
		isSelected := i == m.cursor
		ps, hasPaneStatus := m.paneStatus[p.Index]

		// Determine card colors based on agent type
		var borderColor, iconColor lipgloss.Color
		var agentIcon string

		switch p.Type {
		case tmux.AgentClaude:
			borderColor = t.Claude
			iconColor = t.Claude
			agentIcon = ic.Claude
		case tmux.AgentCodex:
			borderColor = t.Codex
			iconColor = t.Codex
			agentIcon = ic.Codex
		case tmux.AgentGemini:
			borderColor = t.Gemini
			iconColor = t.Gemini
			agentIcon = ic.Gemini
		default:
			borderColor = t.Green
			iconColor = t.Green
			agentIcon = ic.User
		}

		// Selection highlight
		if isSelected {
			borderColor = t.Pink
		}

		// Pulse border for active/working panes (if not selected)
		if !isSelected && row.Status == "working" && m.animTick > 0 {
			borderColor = styles.Pulse(string(borderColor), m.animTick)
		}

		// Build card content
		var cardContent strings.Builder

		// Header line with icon and title
		statusIcon := "•"
		statusColor := t.Overlay
		switch row.Status {
		case "working":
			statusIcon = WorkingSpinnerFrame(m.animTick)
			statusColor = t.Green
		case "idle":
			statusIcon = "○"
			statusColor = t.Yellow
		case "error":
			statusIcon = "✗"
			statusColor = t.Red
		case "compacted":
			statusIcon = "⚠"
			statusColor = t.Peach
		case "rate_limited":
			statusIcon = "⏳"
			statusColor = t.Maroon
		}
		statusStyled := cachedStyledText(statusIcon, statusColor, true, false)

		iconStyled := cachedStyledText(agentIcon, iconColor, true, false)
		// Show profile name as primary identifier
		profileName := p.Type.ProfileName()
		profileStyled := cachedStyledText(profileName, t.Text, true, false)
		cardContent.WriteString(statusStyled + " " + iconStyled + " " + profileStyled + "\n")

		// Index badge + compact badges
		numBadge := cachedStyledText(fmt.Sprintf("#%d", p.Index), t.Overlay, false, false)
		cardContent.WriteString(numBadge)
		if p.Variant != "" {
			label := layout.TruncateWidthDefault(p.Variant, 12)
			modelBadge := cachedTextBadge(label, iconColor, t.Base, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: false,
			})
			cardContent.WriteByte(' ')
			cardContent.WriteString(modelBadge)
		}
		if showExtendedInfo {
			if rank, ok := contextRanks[p.Index]; ok && rank > 0 {
				rankBadge := cachedTextBadge(fmt.Sprintf("rank%d", rank), t.Mauve, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     false,
					ShowIcon: false,
				})
				cardContent.WriteByte(' ')
				cardContent.WriteString(rankBadge)
			}
		}
		cardContent.WriteByte('\n')

		// Bead + activity badges (best-effort)
		if row.CurrentBead != "" {
			beadID := row.CurrentBead
			if parts := strings.SplitN(row.CurrentBead, ": ", 2); len(parts) > 0 {
				beadID = parts[0]
			}
			beadBadge := cachedTextBadge(beadID, t.Mauve, t.Base, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: false,
			})
			cardContent.WriteString(beadBadge + "\n")
		}

		wroteActivityBadge := false
		if badge := cachedActivityBadge(row.Status); badge != "" {
			cardContent.WriteString(badge)
			wroteActivityBadge = true
		}
		if row.FileChanges > 0 {
			if wroteActivityBadge {
				cardContent.WriteByte(' ')
			}
			cardContent.WriteString(cachedTextBadge(fmt.Sprintf("Δ%d", row.FileChanges), t.Blue, t.Base, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: false,
			}))
			wroteActivityBadge = true
		}
		if row.TokenVelocity > 0 && showExtendedInfo {
			if wroteActivityBadge {
				cardContent.WriteByte(' ')
			}
			cardContent.WriteString(styles.TokenVelocityBadge(row.TokenVelocity, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: true,
			}))
			wroteActivityBadge = true
		}
		if wroteActivityBadge {
			cardContent.WriteByte('\n')
		}

		// Mail badges
		if hasPaneStatus && ps.MailUnread > 0 {
			label := fmt.Sprintf("✉ %d new", ps.MailUnread)
			if ps.MailUrgent > 0 {
				label = fmt.Sprintf("✉ %d new (%d urgent)", ps.MailUnread, ps.MailUrgent)
			}

			style := t.Green
			if ps.MailUrgent > 0 {
				style = t.Red
			}

			mailBadge := cachedTextBadge(label, style, t.Base, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     ps.MailUrgent > 0,
				ShowIcon: false,
			})
			cardContent.WriteString(mailBadge + "\n")
		}

		// Health badges - show warning/error status and restart count
		if hasPaneStatus {
			// Health status badge
			switch ps.HealthStatus {
			case "warning":
				healthBadge := cachedTextBadge("⚠ WARN", t.Yellow, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     true,
					ShowIcon: false,
				})
				cardContent.WriteString(healthBadge + "\n")
			case "error":
				healthBadge := cachedTextBadge("✗ ERR", t.Red, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     true,
					ShowIcon: false,
				})
				cardContent.WriteString(healthBadge + "\n")
			}

			// Restart count badge
			if ps.RestartCount > 0 {
				restartBadge := cachedTextBadge(fmt.Sprintf("↻%d", ps.RestartCount), t.Peach, t.Base, styles.BadgeOptions{
					Style:    styles.BadgeStyleCompact,
					Bold:     false,
					ShowIcon: false,
				})
				cardContent.WriteString(restartBadge + "\n")
			}

			// Show first health issue as tooltip
			if len(ps.HealthIssues) > 0 && showExtendedInfo {
				issue := layout.TruncateWidthDefault(ps.HealthIssues[0], maxInt(cardWidth-4, 10))
				healthBadge := cachedStyledText(issue, t.Overlay, false, true)
				cardContent.WriteString(healthBadge + "\n")
			}
		}

		// Size info - on wide displays show more detail
		if showExtendedInfo {
			cardContent.WriteString(cachedStyledText(fmt.Sprintf("%dx%d cols×rows", p.Width, p.Height), t.Subtext, false, false) + "\n")
		} else {
			cardContent.WriteString(cachedStyledText(fmt.Sprintf("%dx%d", p.Width, p.Height), t.Subtext, false, false) + "\n")
		}

		// Command running (if any) - only when there is room
		if p.Command != "" && showExtendedInfo {
			cmd := layout.TruncateWidthDefault(p.Command, maxInt(cardWidth-4, 8))
			cardContent.WriteString(cachedStyledText(cmd, t.Overlay, false, true))
		}

		// Context usage bar (best-effort; show in grid when available)
		if hasPaneStatus && ps.ContextLimit > 0 {
			cardContent.WriteString("\n")
			// Show token counts in extended view (e.g., "142.5K / 200K")
			if showExtendedInfo && ps.ContextTokens > 0 {
				tokenInfo := formatTokenDisplay(ps.ContextTokens, ps.ContextLimit)
				cardContent.WriteString(cachedStyledText(tokenInfo, t.Subtext, false, false) + "\n")
			}
			contextBar := cachedContextBar(ps.ContextPercent, cardWidth-4)
			cardContent.WriteString(contextBar)
		}

		// Rotation in-progress indicator
		if hasPaneStatus && ps.IsRotating {
			cardContent.WriteString("\n")
			rotateIcon := styles.Shimmer("🔄", m.animTick, string(t.Blue), string(t.Sapphire), string(t.Blue))
			cardContent.WriteString(rotateIcon + cachedStyledText(" Rotating...", t.Blue, true, false))
		} else if hasPaneStatus && ps.RotatedAt != nil {
			// Show "rotated Xm ago" indicator for recently rotated agents
			elapsed := time.Since(*ps.RotatedAt)
			if elapsed < 5*time.Minute {
				cardContent.WriteString("\n")
				cardContent.WriteString(cachedStyledText(fmt.Sprintf("↻ rotated %s ago", formatRelativeTime(elapsed)), t.Overlay, false, true))
			}
		}

		// Compaction indicator
		if hasPaneStatus && ps.LastCompaction != nil {
			cardContent.WriteString("\n")
			indicator := "⚠ compacted"
			if ps.RecoverySent {
				indicator = "↻ recovering"
			}
			cardContent.WriteString(cachedStyledText(indicator, t.Warning, true, false))
		}

		// Create card box
		cards = append(cards, cachedCardStyle(borderColor, isSelected).Render(cardContent.String()))
	}

	// Arrange cards in rows
	for i := 0; i < len(cards); i += cardsPerRow {
		end := i + cardsPerRow
		if end > len(cards) {
			end = len(cards)
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, cards[i:end]...)
		lines = append(lines, "  "+row)
	}

	return strings.Join(lines, "\n")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func contentHeightFor(total int) int {
	contentHeight := total - 14
	if contentHeight < 5 {
		contentHeight = 5
	}
	return contentHeight
}

func (m Model) focusedPanelHandlesOwnHeight() bool {
	switch m.focusedPanel {
	case PanelBeads:
		return m.beadsPanel != nil && m.beadsPanel.HandlesOwnHeight()
	case PanelAlerts:
		return m.alertsPanel != nil && m.alertsPanel.HandlesOwnHeight()
	case PanelAttention:
		return m.attentionPanel != nil && m.attentionPanel.HandlesOwnHeight()
	default:
		return false
	}
}

// truncateToHeight truncates content to fit within maxLines.
// If the content has more lines than maxLines, it truncates and optionally
// shows a "more" indicator. This is needed because lipgloss's Height/MaxHeight
// don't actually truncate content - they're CSS-like properties for layout.
func truncateToHeight(content string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	// Truncate to maxLines
	return strings.Join(lines[:maxLines], "\n")
}

func dashboardDebugEnabled(m *Model) bool {
	if m != nil && m.showDiagnostics {
		return true
	}
	// Check NTM_TUI_DEBUG (preferred), NTM_DEBUG (global), or NTM_DASH_DEBUG (legacy alias)
	for _, envVar := range []string{"NTM_TUI_DEBUG", "NTM_DEBUG", "NTM_DASH_DEBUG"} {
		value := strings.TrimSpace(os.Getenv(envVar))
		if value == "" {
			continue
		}
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func (m Model) dashboardHelpOptions() components.DashboardHelpOptions {
	opts := components.DashboardHelpOptionsFrom(m.helpVerbosity, dashboardDebugEnabled(&m))
	opts.PopupMode = m.popupMode
	return opts
}

func (m *Model) visiblePanelsForHelpVerbosity() []PanelID {
	opts := m.dashboardHelpOptions()
	if opts.Verbosity == components.DashboardHelpVerbosityMinimal {
		// Minimal: core panels only (activity/status).
		if m.tier >= layout.TierSplit {
			return []PanelID{PanelPaneList, PanelDetail}
		}
		return []PanelID{PanelPaneList}
	}

	// Full/debug: responsive panel set based on tier.
	switch {
	case m.tier >= layout.TierMega:
		// Use PanelConflicts instead of PanelAlerts when conflicts are present.
		// Include PanelAttention between alerts and sidebar.
		if m.conflictsPanel.HasConflicts() {
			return []PanelID{PanelPaneList, PanelDetail, PanelBeads, PanelConflicts, PanelAttention, PanelSidebar}
		}
		return []PanelID{PanelPaneList, PanelDetail, PanelBeads, PanelAlerts, PanelAttention, PanelSidebar}
	case m.tier >= layout.TierUltra:
		// Include PanelAttention at TierUltra and above.
		return []PanelID{PanelPaneList, PanelDetail, PanelAttention, PanelSidebar}
	case m.tier >= layout.TierWide:
		// Show attention panel at TierWide (visible between split and ultra).
		return []PanelID{PanelPaneList, PanelDetail, PanelAttention}
	case m.tier >= layout.TierSplit:
		return []PanelID{PanelPaneList, PanelDetail, PanelAttention}
	default:
		return []PanelID{PanelPaneList}
	}
}

func normalizedHelpVerbosity(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "minimal":
		return "minimal"
	default:
		return "full"
	}
}

type sizedPanel interface {
	Width() int
	Height() int
}

func logPanelSize(name string, panel sizedPanel) string {
	if panel == nil {
		return fmt.Sprintf("%s=0x0", name)
	}
	return fmt.Sprintf("%s=%dx%d", name, panel.Width(), panel.Height())
}

func tierLabel(tier layout.Tier) string {
	switch tier {
	case layout.TierNarrow:
		return "narrow"
	case layout.TierSplit:
		return "split"
	case layout.TierWide:
		return "wide"
	case layout.TierUltra:
		return "ultra"
	case layout.TierMega:
		return "mega"
	default:
		return fmt.Sprintf("tier-%d", int(tier))
	}
}

func (m *Model) resizeSidebarPanels(width, height int) {
	if m.spawnPanel != nil {
		m.spawnPanel.SetSize(width, height)
	}
	if m.costPanel != nil {
		m.costPanel.SetSize(width, height)
	}
	if m.rchPanel != nil {
		m.rchPanel.SetSize(width, height)
	}
	if m.metricsPanel != nil {
		m.metricsPanel.SetSize(width, height)
	}
	if m.historyPanel != nil {
		m.historyPanel.SetSize(width, height)
	}
	if m.filesPanel != nil {
		m.filesPanel.SetSize(width, height)
	}
	if m.timelinePanel != nil {
		m.timelinePanel.SetSize(width, height)
	}
	if m.cassPanel != nil {
		m.cassPanel.SetSize(width, height)
	}
}

func (m *Model) resizePanelsForLayout() {
	contentHeight := contentHeightFor(m.height)
	panelHeight := maxInt(contentHeight-2, 0)

	switch {
	case m.tier >= layout.TierMega:
		_, _, p3, p4, p5, p6 := layout.MegaProportions6(m.width)
		p3Inner := maxInt(p3-4, 0)
		p4Inner := maxInt(p4-4, 0)
		p5Inner := maxInt(p5-4, 0)
		p6Inner := maxInt(p6-4, 0)

		if m.beadsPanel != nil {
			m.beadsPanel.SetSize(p3Inner, panelHeight)
		}
		if m.alertsPanel != nil {
			m.alertsPanel.SetSize(p4Inner, panelHeight)
		}
		if m.attentionPanel != nil {
			m.attentionPanel.SetSize(p5Inner, panelHeight)
		}
		m.resizeSidebarPanels(p6Inner, panelHeight)

	case m.tier >= layout.TierUltra:
		_, _, rightWidth := layout.UltraProportions(m.width)
		rightWidth = maxInt(rightWidth-2, 0)
		sidebarWidth := maxInt(rightWidth-4, 0)
		if m.attentionPanel != nil {
			m.attentionPanel.SetSize(sidebarWidth, minInt(maxInt(panelHeight/3, m.attentionPanel.Config().MinHeight), 10))
		}
		m.resizeSidebarPanels(sidebarWidth, panelHeight)
		if m.beadsPanel != nil {
			m.beadsPanel.SetSize(0, 0)
		}
		if m.alertsPanel != nil {
			m.alertsPanel.SetSize(0, 0)
		}

	default:
		m.resizeSidebarPanels(0, 0)
		if m.beadsPanel != nil {
			m.beadsPanel.SetSize(0, 0)
		}
		if m.alertsPanel != nil {
			m.alertsPanel.SetSize(0, 0)
		}
		if m.attentionPanel != nil {
			if m.tier >= layout.TierSplit {
				_, rightWidth := layout.SplitProportions(m.width)
				m.attentionPanel.SetSize(maxInt(rightWidth-4, 0), panelHeight)
			} else {
				m.attentionPanel.SetSize(0, 0)
			}
		}
	}

	if m.tickerPanel != nil {
		m.tickerPanel.SetSize(maxInt(m.width-4, 0), 1)
	}
}

func refreshDue(last time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	if last.IsZero() {
		return true
	}
	return time.Since(last) >= interval
}

func tailDelta(prev, current string) string {
	if current == "" {
		return ""
	}
	if prev == "" {
		curLines := strings.Split(current, "\n")
		if len(curLines) > 0 && curLines[len(curLines)-1] == "" {
			curLines = curLines[:len(curLines)-1]
		}
		if len(curLines) == 0 {
			return ""
		}
		return strings.Join(curLines, "\n")
	}
	if prev == current {
		return ""
	}

	prevLines := strings.Split(prev, "\n")
	curLines := strings.Split(current, "\n")
	if len(prevLines) > 0 && prevLines[len(prevLines)-1] == "" {
		prevLines = prevLines[:len(prevLines)-1]
	}
	if len(curLines) > 0 && curLines[len(curLines)-1] == "" {
		curLines = curLines[:len(curLines)-1]
	}
	if len(curLines) == 0 {
		return ""
	}

	maxOverlap := len(prevLines)
	if len(curLines) < maxOverlap {
		maxOverlap = len(curLines)
	}

	overlap := 0
	for k := maxOverlap; k > 0; k-- {
		if slicesEqual(prevLines[len(prevLines)-k:], curLines[:k]) {
			overlap = k
			break
		}
	}

	deltaLines := curLines[overlap:]
	if len(deltaLines) == 0 {
		return ""
	}
	return strings.Join(deltaLines, "\n")
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *Model) recordCostOutputDelta(paneID, modelName, prevOutput, currentOutput string) {
	if paneID == "" || currentOutput == "" {
		return
	}
	if m.costOutputTokens == nil {
		m.costOutputTokens = make(map[string]int)
	}
	if m.costModels == nil {
		m.costModels = make(map[string]string)
	}

	delta := tailDelta(prevOutput, currentOutput)
	if delta == "" {
		return
	}

	deltaTokens := cost.EstimateTokens(delta)
	if deltaTokens <= 0 {
		return
	}
	m.costOutputTokens[paneID] += deltaTokens
	if modelName != "" {
		m.costModels[paneID] = modelName
	}
}

func (m *Model) updateCostFromPrompts(now time.Time) {
	if m.session == "" {
		return
	}
	if !m.costLastPromptRead.IsZero() && now.Sub(m.costLastPromptRead) < CostPromptRefreshInterval {
		return
	}
	m.costLastPromptRead = now

	if m.costInputTokens == nil {
		m.costInputTokens = make(map[string]int)
	}

	history, err := sessionPkg.LoadPromptHistory(m.session)
	if err != nil {
		m.costError = err
		return
	}

	// Index panes by index for history targets.
	paneByIndex := make(map[int]tmux.Pane, len(m.panes))
	for _, p := range m.panes {
		paneByIndex[p.Index] = p
	}

	lastTs := m.costLastPromptTimestamp
	maxTs := lastTs

	for _, entry := range history.Prompts {
		if !entry.Timestamp.After(lastTs) {
			continue
		}

		promptTokens := cost.EstimateTokens(entry.Content)
		if promptTokens <= 0 {
			if entry.Timestamp.After(maxTs) {
				maxTs = entry.Timestamp
			}
			continue
		}

		for _, target := range entry.Targets {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}

			if strings.EqualFold(target, "all") {
				for _, p := range m.panes {
					if p.Type == tmux.AgentUser {
						continue
					}
					m.costInputTokens[p.ID] += promptTokens
					if string(p.Type) == "ollama" {
						if tr := m.ensureLocalPerfTracker(p.ID); tr != nil {
							tr.addPrompt(entry.Timestamp)
						}
					}
				}
				continue
			}

			idx, err := strconv.Atoi(target)
			if err != nil {
				continue
			}
			pane, ok := paneByIndex[idx]
			if !ok || pane.Type == tmux.AgentUser {
				continue
			}
			m.costInputTokens[pane.ID] += promptTokens
			if string(pane.Type) == "ollama" {
				if tr := m.ensureLocalPerfTracker(pane.ID); tr != nil {
					tr.addPrompt(entry.Timestamp)
				}
			}
		}

		if entry.Timestamp.After(maxTs) {
			maxTs = entry.Timestamp
		}
	}

	if maxTs.After(lastTs) {
		m.costLastPromptTimestamp = maxTs
	}
	m.costError = nil
}

func (m *Model) resolveCostModelForPane(pane tmux.Pane) string {
	if pane.Variant != "" {
		return pane.Variant
	}

	switch pane.Type {
	case tmux.AgentClaude:
		if m.cfg != nil && m.cfg.Models.DefaultClaude != "" {
			return m.cfg.Models.DefaultClaude
		}
		return "claude-sonnet-4-6"
	case tmux.AgentCodex:
		if m.cfg != nil && m.cfg.Models.DefaultCodex != "" {
			return m.cfg.Models.DefaultCodex
		}
		return "gpt-4"
	case tmux.AgentGemini:
		if m.cfg != nil && m.cfg.Models.DefaultGemini != "" {
			return m.cfg.Models.DefaultGemini
		}
		return "gemini-2.0-flash"
	default:
		return ""
	}
}

func (m *Model) refreshCostPanel(now time.Time) {
	if m.costPanel == nil {
		return
	}
	if m.costInputTokens == nil {
		m.costInputTokens = make(map[string]int)
	}
	if m.costOutputTokens == nil {
		m.costOutputTokens = make(map[string]int)
	}
	if m.costModels == nil {
		m.costModels = make(map[string]string)
	}
	if m.costLastCosts == nil {
		m.costLastCosts = make(map[string]float64)
	}

	var rows []panels.CostAgentRow
	var total float64

	for _, p := range m.panes {
		if p.Type == tmux.AgentUser {
			continue
		}

		modelName := m.costModels[p.ID]
		if modelName == "" {
			modelName = m.resolveCostModelForPane(p)
			if modelName != "" {
				m.costModels[p.ID] = modelName
			}
		}

		inputTokens := m.costInputTokens[p.ID]
		outputTokens := m.costOutputTokens[p.ID]

		pricing := cost.GetModelPricing(modelName)
		costUSD := (float64(inputTokens)/1000.0)*pricing.InputPer1K + (float64(outputTokens)/1000.0)*pricing.OutputPer1K
		total += costUSD

		prevCost := m.costLastCosts[p.ID]
		delta := costUSD - prevCost
		trend := panels.CostTrendFlat
		if delta > 0.001 {
			trend = panels.CostTrendUp
		} else if delta < -0.001 {
			trend = panels.CostTrendDown
		}
		m.costLastCosts[p.ID] = costUSD

		rows = append(rows, panels.CostAgentRow{
			PaneTitle:    p.Title,
			Model:        modelName,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      costUSD,
			Trend:        trend,
		})
	}

	lastHour := m.updateCostSnapshots(now, total)

	data := panels.CostPanelData{
		Agents:          rows,
		SessionTotalUSD: total,
		LastHourUSD:     lastHour,
		DailyBudgetUSD:  m.costDailyBudgetUSD,
		BudgetUsedUSD:   total,
	}

	m.costData = data
	m.costPanel.SetData(data, m.costError)
}

func (m *Model) updateCostSnapshots(now time.Time, total float64) float64 {
	if m.costSnapshots == nil {
		m.costSnapshots = make([]costSnapshot, 0, 128)
	}

	if len(m.costSnapshots) == 0 || total != m.costSnapshots[len(m.costSnapshots)-1].TotalUSD {
		m.costSnapshots = append(m.costSnapshots, costSnapshot{At: now, TotalUSD: total})
	}

	cutoff := now.Add(-1 * time.Hour)
	pruneIdx := 0
	for pruneIdx < len(m.costSnapshots) && m.costSnapshots[pruneIdx].At.Before(cutoff) {
		pruneIdx++
	}
	if pruneIdx > 0 {
		m.costSnapshots = m.costSnapshots[pruneIdx:]
	}

	if len(m.costSnapshots) == 0 {
		return 0
	}

	delta := total - m.costSnapshots[0].TotalUSD
	if delta < 0 {
		return 0
	}
	return delta
}

func (m *Model) recordTimelineStatus(pane tmux.Pane, st status.AgentStatus) bool {
	if m.timelinePanel == nil || m.session == "" {
		return false
	}

	agentType := timelineAgentType(pane, st.AgentType)
	if agentType == tmux.AgentUnknown || agentType == tmux.AgentUser {
		return false
	}

	agentID := timelineAgentID(pane, st.AgentType, st.PaneID)
	if agentID == "" {
		return false
	}

	nextState := timelineStateFromStatus(st)
	tracker := state.GetGlobalTimelineTracker()
	currentState := tracker.GetCurrentState(agentID)
	if currentState == nextState {
		return false
	}

	event := state.AgentEvent{
		AgentID:   agentID,
		AgentType: state.AgentType(agentType),
		SessionID: m.session,
		State:     nextState,
		Timestamp: st.UpdatedAt,
	}
	recorded := tracker.RecordEvent(event)

	if currentState == "" {
		tracker.AddMarker(state.TimelineMarker{
			AgentID:   agentID,
			SessionID: m.session,
			Type:      state.MarkerStart,
			Timestamp: recorded.Timestamp,
		})
	}

	if currentState == state.TimelineWorking && nextState == state.TimelineIdle {
		tracker.AddMarker(state.TimelineMarker{
			AgentID:   agentID,
			SessionID: m.session,
			Type:      state.MarkerCompletion,
			Timestamp: recorded.Timestamp,
		})
	}

	if nextState == state.TimelineError {
		errMsg := ""
		if st.ErrorType != "" {
			errMsg = st.ErrorType.String()
		}
		tracker.AddMarker(state.TimelineMarker{
			AgentID:   agentID,
			SessionID: m.session,
			Type:      state.MarkerError,
			Timestamp: recorded.Timestamp,
			Message:   errMsg,
		})
	}

	return true
}

func (m *Model) refreshTimelinePanel() {
	if m.timelinePanel == nil || m.session == "" {
		return
	}

	tracker := state.GetGlobalTimelineTracker()
	events := tracker.GetEventsForSession(m.session, time.Time{})
	markers := tracker.GetMarkersForSession(m.session, time.Time{}, time.Time{})
	data := panels.TimelineData{
		Events:  events,
		Markers: markers,
		Stats:   tracker.Stats(),
	}
	m.timelinePanel.SetData(data, nil)
}

func timelineStateFromStatus(st status.AgentStatus) state.TimelineState {
	switch st.State {
	case status.StateWorking:
		return state.TimelineWorking
	case status.StateError:
		return state.TimelineError
	case status.StateIdle:
		return state.TimelineIdle
	default:
		return state.TimelineIdle
	}
}

func timelineAgentID(pane tmux.Pane, fallbackType, fallbackID string) string {
	if pane.NTMIndex > 0 && pane.Type != tmux.AgentUnknown && pane.Type != tmux.AgentUser {
		return fmt.Sprintf("%s_%d", pane.Type, pane.NTMIndex)
	}

	if pane.Title != "" {
		if parts := strings.SplitN(pane.Title, "__", 2); len(parts) == 2 && parts[1] != "" {
			return parts[1]
		}
		return pane.Title
	}

	if fallbackType != "" {
		suffix := strings.TrimPrefix(fallbackID, "%")
		if suffix == "" {
			suffix = "0"
		}
		return fmt.Sprintf("%s_%s", fallbackType, suffix)
	}

	return fallbackID
}

func timelineAgentType(pane tmux.Pane, fallbackType string) tmux.AgentType {
	if pane.Type != tmux.AgentUnknown && pane.Type != tmux.AgentUser && pane.Type != "" {
		return pane.Type
	}
	if fallbackType == "" {
		return tmux.AgentUnknown
	}
	t := tmux.AgentType(fallbackType)
	if t.IsValid() && t != tmux.AgentUser {
		return t
	}
	return tmux.AgentUnknown
}

func (m *Model) scheduleRefreshes(now time.Time) []tea.Cmd {
	var cmds []tea.Cmd

	paneDue := refreshDue(m.lastPaneFetch, m.paneRefreshInterval)
	contextDue := refreshDue(m.lastContextFetch, m.contextRefreshInterval)

	if paneDue {
		if cmd := m.requestSessionFetch(false); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if contextDue {
		if cmd := m.requestStatusesFetch(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if contextDue && !m.fetchingContext {
		if !m.fetchingMetrics {
			m.fetchingMetrics = true
			cmds = append(cmds, m.fetchMetricsCmd())
		}
		if !m.fetchingRouting {
			m.fetchingRouting = true
			cmds = append(cmds, m.fetchRoutingCmd())
		}
		if !m.fetchingHistory {
			m.fetchingHistory = true
			cmds = append(cmds, m.fetchHistoryCmd())
		}
		if !m.fetchingFileChanges {
			m.fetchingFileChanges = true
			cmds = append(cmds, m.fetchFileChangesCmd())
		}
		// Refresh Agent Mail status along with context updates.
		cmds = append(cmds, m.fetchAgentMailStatus())
	}

	if !m.scanDisabled && refreshDue(m.lastScanFetch, m.scanRefreshInterval) && !m.fetchingScan {
		m.lastScanFetch = now
		if cmd := m.requestScanFetch(false); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if refreshDue(m.lastMailInboxFetch, m.mailInboxRefreshInterval) && !m.fetchingMailInbox {
		m.fetchingMailInbox = true
		m.lastMailInboxFetch = now
		cmds = append(cmds, m.fetchAgentMailInboxes())
	}

	if refreshDue(m.lastAlertsFetch, m.alertsRefreshInterval) && !m.fetchingAlerts {
		m.fetchingAlerts = true
		m.lastAlertsFetch = now
		cmds = append(cmds, m.fetchAlertsCmd())
	}

	if refreshDue(m.lastAttentionFetch, m.attentionRefreshInterval) && !m.fetchingAttention {
		m.fetchingAttention = true
		m.lastAttentionFetch = now
		cmds = append(cmds, m.fetchAttentionCmd())
	}

	if refreshDue(m.lastBeadsFetch, m.beadsRefreshInterval) && !m.fetchingBeads {
		m.fetchingBeads = true
		m.lastBeadsFetch = now
		cmds = append(cmds, m.fetchBeadsCmd())
	}

	if refreshDue(m.lastCassContextFetch, m.cassContextRefreshInterval) && !m.fetchingCassContext {
		m.fetchingCassContext = true
		m.lastCassContextFetch = now
		cmds = append(cmds, m.fetchCASSContextCmd())
	}

	if refreshDue(m.lastCheckpointFetch, m.checkpointRefreshInterval) && !m.fetchingCheckpoint {
		m.fetchingCheckpoint = true
		m.lastCheckpointFetch = now
		cmds = append(cmds, m.fetchCheckpointStatus())
	}

	if refreshDue(m.lastHandoffFetch, m.handoffRefreshInterval) && !m.fetchingHandoff {
		m.fetchingHandoff = true
		m.lastHandoffFetch = now
		cmds = append(cmds, m.fetchHandoffCmd())
	}

	if refreshDue(m.lastRanoNetworkFetch, m.ranoNetworkRefreshInterval) && !m.fetchingRanoNetwork {
		m.fetchingRanoNetwork = true
		m.lastRanoNetworkFetch = now
		cmds = append(cmds, m.fetchRanoNetworkStats())
	}

	if refreshDue(m.lastRCHFetch, m.rchRefreshInterval) && !m.fetchingRCH {
		m.fetchingRCH = true
		m.lastRCHFetch = now
		cmds = append(cmds, m.fetchRCHStatus())
	}

	if refreshDue(m.lastDCGFetch, m.dcgRefreshInterval) && !m.fetchingDCG {
		m.fetchingDCG = true
		m.lastDCGFetch = now
		cmds = append(cmds, m.fetchDCGStatus())
	}

	return cmds
}

func (m *Model) scheduleSpawnRefresh(now time.Time) tea.Cmd {
	if refreshDue(m.lastSpawnFetch, m.spawnRefreshInterval) && !m.fetchingSpawn {
		m.fetchingSpawn = true
		m.lastSpawnFetch = now
		return m.fetchSpawnStateCmd()
	}
	return nil
}

func rchStatusActive(status *tools.RCHStatus) bool {
	if status == nil {
		return false
	}
	for _, worker := range status.Workers {
		if strings.TrimSpace(worker.CurrentBuild) != "" {
			return true
		}
		if worker.Queue > 0 {
			return true
		}
	}
	return false
}

// ═══════════════════════════════════════════════════════════════════════════
// SPLIT VIEW RENDERING (for wide terminals ≥110 cols)
// Inspired by beads_viewer's responsive layout patterns
// ═══════════════════════════════════════════════════════════════════════════

// renderSplitView renders a two-panel layout: pane list (left) + detail (right)
func (m Model) renderSplitView() string {
	t := m.theme
	leftWidth, rightWidth := layout.SplitProportions(m.width)
	// The dashboard UI uses a left margin (2) plus inter-panel gap (1);
	// trim the rightmost panel so the total rendered width stays within the terminal.
	rightWidth = maxInt(rightWidth-3, 0)

	// Calculate content height (leave room for header/footer)
	contentHeight := contentHeightFor(m.height)

	listBorder := m.focusBorderColor(t.Surface1, PanelPaneList)
	detailBorder := m.focusBorderColor(t.Pink, PanelDetail, PanelAttention)

	// Build left panel (pane list)
	listContent := m.renderPaneList(leftWidth - 4) // -4 for borders/padding
	listPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(listBorder).
		Width(leftWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(listContent)

	// Build right panel (tab bar + focused content)
	tabBar := m.renderPanelTabBar(rightWidth - 4)
	detailContent := m.renderSplitPrimaryPanel(rightWidth-4, contentHeight-2)
	rightContent := tabBar + "\n" + detailContent
	detailPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(detailBorder). // Accent color for detail
		Width(rightWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(rightContent)

	// Join panels horizontally with gap
	gap := panelGap(contentHeight)
	return indentDashboardLayout(lipgloss.JoinHorizontal(lipgloss.Top, listPanel, gap, detailPanel))
}

// renderUltraLayout renders a three-panel layout: Agents | Detail | Sidebar
func (m Model) renderUltraLayout() string {
	t := m.theme
	leftWidth, centerWidth, rightWidth := layout.UltraProportions(m.width)
	// The dashboard UI uses a left margin (2) plus inter-panel gaps (2×1=2);
	// trim the rightmost panel so the total rendered width stays within the terminal.
	rightWidth = maxInt(rightWidth-4, 0)

	contentHeight := contentHeightFor(m.height)

	listBorder := m.focusBorderColor(t.Surface1, PanelPaneList)
	detailBorder := m.focusBorderColor(t.Pink, PanelDetail)
	sidebarBorder := m.focusBorderColor(t.Lavender, PanelSidebar, PanelAttention)

	listContent := m.renderPaneList(leftWidth - 4)
	listPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(listBorder).
		Width(leftWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(listContent)

	tabBar := m.renderPanelTabBar(centerWidth - 4)
	detailContent := m.renderPaneDetail(centerWidth - 4)
	centerContent := tabBar + "\n" + detailContent
	detailPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(detailBorder).
		Width(centerWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(centerContent)

	sidebarContent := m.renderSidebar(rightWidth-4, contentHeight-2)
	sidebarPanel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(sidebarBorder).
		Width(rightWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Padding(0, 1).
		Render(sidebarContent)

	gap := panelGap(contentHeight)
	return indentDashboardLayout(lipgloss.JoinHorizontal(lipgloss.Top, listPanel, gap, detailPanel, gap, sidebarPanel))
}

func (m Model) renderSidebar(width, height int) string {
	t := m.theme
	var lines []string

	if width <= 0 {
		return ""
	}

	// Keep the sidebar compact by only embedding the attention panel when it is
	// actively relevant. Split/wide layouts render attention in the primary
	// detail slot when focused; ultra layouts surface it here when focused or
	// when actionable items exist. Mega has a dedicated attention column.
	if m.tier < layout.TierMega &&
		m.attentionPanel != nil &&
		height >= m.attentionPanel.Config().MinHeight &&
		(m.focusedPanel == PanelAttention || m.attentionPanel.HasItems()) {
		panelHeight := m.attentionPanel.Config().MinHeight
		if m.focusedPanel == PanelAttention || m.attentionPanel.HasItems() {
			panelHeight = maxInt(panelHeight, height/3)
			if panelHeight > 10 {
				panelHeight = 10
			}
		}
		if panelHeight > height {
			panelHeight = height
		}
		lines = append(lines, m.renderAttentionPanel(width, panelHeight), "")
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1).
		Width(width).
		Padding(0, 1)

	lines = append(lines, headerStyle.Render("Activity & Locks"))
	lines = append(lines, "")

	// Spawn progress (only show if active)
	if m.spawnPanel != nil && m.spawnPanel.IsActive() {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		panelHeight := 8 // Fixed height for spawn panel
		if height-used > panelHeight {
			m.spawnPanel.SetSize(width, panelHeight)
			lines = append(lines, m.spawnPanel.View())
			lines = append(lines, "")
		}
	}

	if len(m.agentMailLockInfo) > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Lavender).Bold(true).Render("Active Locks"))
		for _, lock := range m.agentMailLockInfo {
			lines = append(lines, fmt.Sprintf("🔒 %s", layout.TruncateWidthDefault(lock.PathPattern, width-4)))
			lines = append(lines, lipgloss.NewStyle().Foreground(t.Subtext).Render(fmt.Sprintf("  by %s (%s)", lock.AgentName, lock.ExpiresIn)))
		}
		lines = append(lines, "")
	} else {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Overlay).Italic(true).Render("No active locks"))
		lines = append(lines, "")
	}

	// Scan status
	if m.scanStatus != "" && m.scanStatus != "unavailable" {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Blue).Bold(true).Render("Scan Status"))
		lines = append(lines, m.renderScanBadge())
	}

	// Rano network activity (best-effort, height-gated)
	if m.ranoNetworkPanel != nil && height > 0 && m.ranoNetworkPanel.HasData() {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.ranoNetworkPanel.Config().MinHeight {
			if panelHeight > 16 {
				panelHeight = 16
			}

			if m.focusedPanel == PanelSidebar {
				m.ranoNetworkPanel.Focus()
			} else {
				m.ranoNetworkPanel.Blur()
			}
			m.ranoNetworkPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.ranoNetworkPanel.View())
		}
	}

	// RCH build offload status (best-effort, height-gated)
	if m.rchPanel != nil && height > 0 && m.rchPanel.HasData() {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.rchPanel.Config().MinHeight {
			if panelHeight > 18 {
				panelHeight = 18
			}

			if m.focusedPanel == PanelSidebar {
				m.rchPanel.Focus()
			} else {
				m.rchPanel.Blur()
			}
			m.rchPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.rchPanel.View())
		}
	}

	// Cost tracking (best-effort, height-gated)
	if m.costPanel != nil && height > 0 && (m.costPanel.HasData() || m.costDailyBudgetUSD > 0) {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.costPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.costPanel.Focus()
			} else {
				m.costPanel.Blur()
			}
			m.costPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.costPanel.View())
		}
	}

	// Metrics (best-effort, height-gated)
	if m.metricsPanel != nil && height > 0 && (m.metricsError != nil || hasMetricsData(m.metricsData)) {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.metricsPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.metricsPanel.Focus()
			} else {
				m.metricsPanel.Blur()
			}
			m.metricsPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.metricsPanel.View())
		}
	}

	// Command history (best-effort, height-gated)
	if m.historyPanel != nil && height > 0 && (len(m.cmdHistory) > 0 || m.historyError != nil) {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.historyPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.historyPanel.Focus()
			} else {
				m.historyPanel.Blur()
			}
			m.historyPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.historyPanel.View())
		}
	}

	// File activity (best-effort, height-gated)
	if m.filesPanel != nil && height > 0 && (len(m.fileChanges) > 0 || m.fileChangesError != nil) {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.filesPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.filesPanel.Focus()
			} else {
				m.filesPanel.Blur()
			}
			m.filesPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.filesPanel.View())
		}
	}

	// CASS context (best-effort, height-gated)
	if m.cassPanel != nil && height > 0 {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.cassPanel.Config().MinHeight {
			if panelHeight > 14 {
				panelHeight = 14
			}

			if m.focusedPanel == PanelSidebar {
				m.cassPanel.Focus()
			} else {
				m.cassPanel.Blur()
			}
			m.cassPanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.cassPanel.View())
		}
	}

	// Timeline panel (best-effort, height-gated; appended last to avoid crowding)
	if m.timelinePanel != nil && height > 0 {
		used := lipgloss.Height(strings.Join(lines, "\n"))
		spacer := 1
		panelHeight := height - used - spacer
		if panelHeight >= m.timelinePanel.Config().MinHeight {
			if panelHeight > 16 {
				panelHeight = 16
			}

			if m.focusedPanel == PanelSidebar {
				m.timelinePanel.Focus()
			} else {
				m.timelinePanel.Blur()
			}
			m.timelinePanel.SetSize(width, panelHeight)
			lines = append(lines, "", m.timelinePanel.View())
		}
	}

	// Ensure stable height by padding the sidebar to fill allocated space
	content := strings.Join(lines, "\n")
	return panels.FitToHeight(content, height)
}

// renderMegaLayout renders a six-panel layout: Agents | Detail | Beads | Alerts | Attention | Activity
func (m Model) renderMegaLayout() string {
	t := m.theme
	p1, p2, p3, p4, p5, p6 := layout.MegaProportions6(m.width)
	// The dashboard UI uses a left margin (2) plus inter-panel gaps (5×1=5);
	// trim the rightmost panel so the total rendered width stays within the terminal.
	p6 = maxInt(p6-7, 0)
	p1Inner := maxInt(p1-4, 0)
	p2Inner := maxInt(p2-4, 0)
	p3Inner := maxInt(p3-4, 0)
	p4Inner := maxInt(p4-4, 0)
	p5Inner := maxInt(p5-4, 0)
	p6Inner := maxInt(p6-4, 0)

	contentHeight := contentHeightFor(m.height)

	listBorder := m.focusBorderColor(t.Surface1, PanelPaneList)
	detailBorder := m.focusBorderColor(t.Pink, PanelDetail)
	beadsBorder := m.focusBorderColor(t.Green, PanelBeads)
	alertsBorder := m.focusBorderColor(t.Red, PanelAlerts, PanelConflicts)
	conflictsBorder := m.focusBorderColor(t.Red, PanelConflicts)
	attentionBorder := m.focusBorderColor(t.Yellow, PanelAttention)
	sidebarBorder := m.focusBorderColor(t.Lavender, PanelSidebar)

	panel1 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(listBorder).
		Width(p1).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderPaneList(p1Inner))

	megaTabBar := m.renderPanelTabBar(p2Inner)
	panel2 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(detailBorder).
		Width(p2).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(megaTabBar + "\n" + m.renderPaneDetail(p2Inner))

	panel3 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(beadsBorder).
		Width(p3).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderBeadsPanel(p3Inner, contentHeight-2))

	// Panel 4: Alerts or Conflicts (conflicts take priority when present)
	var panel4 string
	if m.conflictsPanel.HasConflicts() {
		panel4 = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(conflictsBorder).
			Width(p4).Height(contentHeight).MaxHeight(contentHeight).
			Padding(0, 1).
			Render(m.renderConflictsPanel(p4Inner, contentHeight-2))
	} else {
		panel4 = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(alertsBorder).
			Width(p4).Height(contentHeight).MaxHeight(contentHeight).
			Padding(0, 1).
			Render(m.renderAlertsPanel(p4Inner, contentHeight-2))
	}

	// Panel 5: Attention feed (always visible in mega layout)
	panel5 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(attentionBorder).
		Width(p5).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderAttentionPanel(p5Inner, contentHeight-2))

	// Panel 6: Sidebar (activity, locks, metrics, history)
	panel6 := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(sidebarBorder).
		Width(p6).Height(contentHeight).MaxHeight(contentHeight).
		Padding(0, 1).
		Render(m.renderSidebar(p6Inner, contentHeight-2))

	gap := panelGap(contentHeight)
	return indentDashboardLayout(lipgloss.JoinHorizontal(lipgloss.Top, panel1, gap, panel2, gap, panel3, gap, panel4, gap, panel5, gap, panel6))
}

// panelGap returns a single-character-wide vertical spacer between dashboard panels.
func panelGap(height int) string {
	if height <= 0 {
		return " "
	}
	lines := make([]string, height+2) // +2 for border top/bottom
	for i := range lines {
		lines[i] = " "
	}
	return strings.Join(lines, "\n")
}

func indentDashboardLayout(content string) string {
	if content == "" {
		return ""
	}
	return lipgloss.NewStyle().PaddingLeft(2).Render(content)
}

func (m Model) renderBeadsPanel(width, height int) string {
	m.beadsPanel.SetSize(width, height)
	if m.focusedPanel == PanelBeads {
		m.beadsPanel.Focus()
	} else {
		m.beadsPanel.Blur()
	}
	return m.beadsPanel.View()
}

func (m Model) renderAlertsPanel(width, height int) string {
	m.alertsPanel.SetSize(width, height)
	if m.focusedPanel == PanelAlerts {
		m.alertsPanel.Focus()
	} else {
		m.alertsPanel.Blur()
	}
	return m.alertsPanel.View()
}

func (m Model) renderAttentionPanel(width, height int) string {
	m.attentionPanel.SetSize(width, height)
	if m.focusedPanel == PanelAttention {
		m.attentionPanel.Focus()
	} else {
		m.attentionPanel.Blur()
	}
	return m.attentionPanel.View()
}

func (m Model) renderSplitPrimaryPanel(width, height int) string {
	if m.focusedPanel == PanelAttention && m.attentionPanel != nil {
		return m.renderAttentionPanel(width, height)
	}
	return m.renderPaneDetail(width)
}

func (m Model) renderConflictsPanel(width, height int) string {
	m.conflictsPanel.SetSize(width, height)
	if m.focusedPanel == PanelConflicts {
		m.conflictsPanel.Focus()
	} else {
		m.conflictsPanel.Blur()
	}
	return m.conflictsPanel.View()
}

func (m Model) renderSpawnPanel(width, height int) string {
	m.spawnPanel.SetSize(width, height)
	return m.spawnPanel.View()
}

func (m Model) renderMetricsPanel(width, height int) string {
	m.metricsPanel.SetSize(width, height)
	if m.focusedPanel == PanelMetrics {
		m.metricsPanel.Focus()
	} else {
		m.metricsPanel.Blur()
	}
	return m.metricsPanel.View()
}

func hasMetricsData(data panels.MetricsData) bool {
	return data.Coverage != nil || data.Redundancy != nil || data.Velocity != nil || data.Conflicts != nil
}

func (m Model) renderHistoryPanel(width, height int) string {
	m.historyPanel.SetSize(width, height)
	if m.focusedPanel == PanelHistory {
		m.historyPanel.Focus()
	} else {
		m.historyPanel.Blur()
	}
	return m.historyPanel.View()
}

// renderPaneList renders a compact list of panes with status indicators.
// Uses bubbles/list for rendering with fuzzy filtering support.
func (m *Model) renderPaneList(width int) string {
	t := m.theme
	var lines []string

	// Calculate layout dimensions
	dims := CalculateLayout(width, 1)

	// Header row
	lines = append(lines, RenderTableHeader(dims, t))

	// Activity summary line (computed from items for consistency)
	rows := BuildPaneTableRows(m.panes, m.agentStatuses, m.paneStatus, &m.beadsSummary, m.fileChanges, m.healthStates, m.animTick, t)
	if summary := activitySummaryLine(rows, t); summary != "" {
		lines = append(lines, " "+summary)
	}

	// Update delegate dimensions and tick for this render
	m.paneDelegate.SetDims(dims)
	m.paneDelegate.SetTick(m.animTick)
	m.paneList.SetDelegate(m.paneDelegate)

	// Set list dimensions to match the rendering context
	listHeight := len(m.panes)
	if listHeight < 1 {
		listHeight = 1
	}
	m.paneList.SetSize(width, listHeight)

	// Sync cursor with list selection
	if m.paneList.Index() != m.cursor && m.cursor < len(m.panes) {
		m.paneList.Select(m.cursor)
	}

	// Use bubbles/list View() for the pane rows
	lines = append(lines, m.paneList.View())

	return strings.Join(lines, "\n")
}

// computeContextRanks returns a 1-based rank per pane index based on context usage (desc).
// Ties share the same rank.
func (m Model) computeContextRanks() map[int]int {
	type pair struct {
		idx int
		pct float64
	}

	var pairs []pair
	for _, p := range m.panes {
		if ps, ok := m.paneStatus[p.Index]; ok {
			pairs = append(pairs, pair{idx: p.Index, pct: ps.ContextPercent})
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].pct > pairs[j].pct
	})

	ranks := make(map[int]int, len(pairs))
	prevPct := -1.0
	currentRank := 0
	for i, pr := range pairs {
		if prevPct < 0 || pr.pct < prevPct {
			currentRank = i + 1
			prevPct = pr.pct
		}
		ranks[pr.idx] = currentRank
	}
	return ranks
}

// spinnerDot returns a one-cell dot spinner frame based on the animation tick.
func spinnerDot(tick int) string {
	frames := []string{".", "·", "•", "·"}
	return frames[tick%len(frames)]
}

// renderPaneDetail renders detailed info for the selected pane
func (m Model) renderPaneDetail(width int) string {
	t := m.theme
	ic := m.icons

	if len(m.panes) == 0 || m.cursor >= len(m.panes) {
		emptyStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
		return emptyStyle.Render("No pane selected")
	}

	p := m.panes[m.cursor]
	ps := m.paneStatus[p.Index]
	var lines []string

	// Header with profile name as primary identifier
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1).
		Width(width-2).
		Padding(0, 1)
	lines = append(lines, headerStyle.Render(p.Type.ProfileName()))
	lines = append(lines, "")

	// Info grid
	labelStyle := lipgloss.NewStyle().Foreground(t.Subtext).Width(12)
	valueStyle := lipgloss.NewStyle().Foreground(t.Text)

	// Type badge
	var typeColor lipgloss.Color
	var typeIcon string
	switch p.Type {
	case tmux.AgentClaude:
		typeColor = t.Claude
		typeIcon = ic.Claude
	case tmux.AgentCodex:
		typeColor = t.Codex
		typeIcon = ic.Codex
	case tmux.AgentGemini:
		typeColor = t.Gemini
		typeIcon = ic.Gemini
	default:
		typeColor = t.Green
		typeIcon = ic.User
	}
	typeBadge := lipgloss.NewStyle().
		Background(typeColor).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 1).
		Render(typeIcon + " " + p.Type.ProfileName())
	lines = append(lines, labelStyle.Render("Profile:")+typeBadge)

	// Index
	lines = append(lines, labelStyle.Render("Index:")+valueStyle.Render(fmt.Sprintf("%d", p.Index)))

	// Pane ID (secondary identifier)
	lines = append(lines, labelStyle.Render("Pane ID:")+valueStyle.Render(p.Title))

	// Dimensions
	lines = append(lines, labelStyle.Render("Size:")+valueStyle.Render(fmt.Sprintf("%d × %d", p.Width, p.Height)))

	// Variant/Model
	if p.Variant != "" {
		variantBadge := lipgloss.NewStyle().
			Background(t.Surface1).
			Foreground(t.Text).
			Padding(0, 1).
			Render(p.Variant)
		lines = append(lines, labelStyle.Render("Model:")+variantBadge)
	}

	lines = append(lines, "")

	// Context usage section
	if ps.ContextLimit > 0 {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Context Usage"))
		lines = append(lines, "")

		// Large context bar
		barWidth := width - 10
		if barWidth < 10 {
			barWidth = 10
		} else if barWidth > 50 {
			barWidth = 50
		}
		contextBar := m.renderContextBar(ps.ContextPercent, barWidth)
		lines = append(lines, "  "+contextBar)

		// Stats
		statsStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		lines = append(lines, statsStyle.Render(fmt.Sprintf(
			"  %d / %d tokens (%.1f%%)",
			ps.ContextTokens, ps.ContextLimit, ps.ContextPercent,
		)))
		lines = append(lines, "")

		// Legend for thresholds (kept compact and ASCII-safe)
		legend := lipgloss.JoinHorizontal(
			lipgloss.Top,
			lipgloss.NewStyle().Foreground(t.Green).Render("green<40%"),
			lipgloss.NewStyle().Foreground(t.Blue).Render("  blue<60%"),
			lipgloss.NewStyle().Foreground(t.Yellow).Render("  yellow<80%"),
			lipgloss.NewStyle().Foreground(t.Red).Render("  red≥80%"),
		)
		lines = append(lines, "  "+legend)
		lines = append(lines, "")
	}

	// Status section
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Status"))
	lines = append(lines, "")

	statusState := ps.State
	if statusState == "" || statusState == "unknown" {
		if st, ok := m.agentStatuses[p.ID]; ok && st.State != status.StateUnknown {
			statusState = st.State.String()
		}
	}
	statusText := statusState
	if statusText == "" {
		statusText = "unknown"
		statusState = statusText
	}
	var statusColor lipgloss.Color
	var statusIcon string
	switch statusState {
	case "working":
		// Animated spinner for working state
		statusIcon = WorkingSpinnerFrame(m.animTick)
		statusColor = t.Green
	case "idle":
		statusIcon = "○"
		statusColor = t.Yellow
	case "error":
		statusIcon = "✗"
		statusColor = t.Red
	case "compacted":
		statusIcon = "⚠"
		statusColor = t.Peach
	default:
		statusIcon = "•"
		statusColor = t.Overlay
	}
	lines = append(lines, "  "+lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon+" "+statusText))

	// Project Health (if warning/critical)
	if m.healthStatus == "warning" || m.healthStatus == "critical" {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Yellow).Render("Project Health"))
		lines = append(lines, "")
		msg := wordwrap.String(m.healthMessage, width-4)
		lines = append(lines, "  "+lipgloss.NewStyle().Foreground(t.Warning).Render(msg))
	}

	// Global Locks (TierWide+)
	if m.tier >= layout.TierWide && len(m.agentMailLockInfo) > 0 {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Active Locks"))
		lines = append(lines, "")
		for i, lock := range m.agentMailLockInfo {
			if i >= 5 {
				lines = append(lines, fmt.Sprintf("  ...and %d more", len(m.agentMailLockInfo)-5))
				break
			}
			lines = append(lines, fmt.Sprintf("  🔒 %s (%s)", layout.TruncateWidthDefault(lock.PathPattern, 20), lock.AgentName))
		}
	}

	// Compaction warning
	if ps.LastCompaction != nil {
		lines = append(lines, "")
		warnStyle := lipgloss.NewStyle().Foreground(t.Peach).Bold(true)
		lines = append(lines, warnStyle.Render("  ⚠ Context compaction detected"))
		if ps.RecoverySent {
			lines = append(lines, lipgloss.NewStyle().Foreground(t.Green).Render("    ↻ Recovery prompt sent"))
		}
	}

	// Command (if running)
	if p.Command != "" {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Command"))
		lines = append(lines, "")
		cmdStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true).
			Width(width - 6)
		lines = append(lines, "  "+cmdStyle.Render(p.Command))
	}

	// Recent Output (rendered with glamour)
	if st, ok := m.agentStatuses[p.ID]; ok && st.LastOutput != "" && m.renderer != nil {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Recent Output"))
		lines = append(lines, "")

		// Use cached rendering if available (cache is populated in Update, not here)
		if cached, ok := m.renderedOutputCache[p.ID]; ok {
			lines = append(lines, cached)
		} else {
			// Fallback: render on demand but don't cache here (View must be pure)
			// The Update handler will populate the cache on the next status update
			rendered, err := m.renderer.Render(st.LastOutput)
			if err == nil {
				lines = append(lines, rendered)
			} else {
				lines = append(lines, layout.TruncateWidthDefault(st.LastOutput, 500))
			}
		}
	}

	// Inbox
	if msgs, ok := m.agentMailInbox[p.ID]; ok && len(msgs) > 0 {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Inbox"))
		lines = append(lines, "")

		count := 0
		for _, msg := range msgs {
			if count >= 5 {
				break
			}
			icon := "•"
			style := t.Text
			if strings.EqualFold(msg.Importance, "urgent") {
				icon = "!"
				style = t.Red
			} else if msg.ReadAt == nil {
				icon = "*"
				style = t.Green
			}

			subject := layout.TruncateWidthDefault(msg.Subject, width-4)
			lines = append(lines, lipgloss.NewStyle().Foreground(style).Render(fmt.Sprintf("  %s %s", icon, subject)))
			count++
		}
		if len(msgs) > 5 {
			lines = append(lines, lipgloss.NewStyle().Foreground(t.Subtext).Render(fmt.Sprintf("  ...and %d more", len(msgs)-5)))
		}
	}

	return strings.Join(lines, "\n")
}

func activitySummaryLine(rows []PaneTableRow, t theme.Theme) string {
	if len(rows) == 0 {
		return ""
	}

	counts := make(map[string]int)
	for _, row := range rows {
		state := row.Status
		if state == "" {
			state = "unknown"
		}
		counts[state]++
	}

	var badges []string
	badges = append(badges, activityCountBadge("working", counts["working"], t))
	badges = append(badges, activityCountBadge("idle", counts["idle"], t))
	badges = append(badges, activityCountBadge("error", counts["error"], t))
	badges = append(badges, activityCountBadge("compacted", counts["compacted"], t))
	badges = append(badges, activityCountBadge("rate_limited", counts["rate_limited"], t))
	badges = append(badges, activityCountBadge("unknown", counts["unknown"], t))

	var compactBadges []string
	for _, badge := range badges {
		if badge != "" {
			compactBadges = append(compactBadges, badge)
		}
	}
	if len(compactBadges) == 0 {
		return ""
	}

	label := lipgloss.NewStyle().Foreground(t.Subtext).Bold(true)
	return label.Render("Activity:") + " " + strings.Join(compactBadges, " ")
}

func (m *Model) ensureDashboardConflictAgent(ctx context.Context, client *agentmail.Client, projectKey string) string {
	agentName := m.session + "_dashboard"
	_, err := client.RegisterAgent(ctx, agentmail.RegisterAgentOptions{
		ProjectKey:      projectKey,
		Program:         "ntm-dashboard",
		Model:           "local",
		Name:            agentName,
		TaskDescription: "Dashboard conflict resolution",
	})
	if err != nil {
		log.Printf("[ConflictAction] Warning: could not register dashboard agent %s: %v", agentName, err)
		// Continue anyway - the agent may already be registered or the server may accept the operation.
	}
	return agentName
}

// handleConflictAction handles user actions on file reservation conflicts.
// It integrates with Agent Mail to send messages or force-release reservations.
func (m *Model) handleConflictAction(conflict watcher.FileConflict, action watcher.ConflictAction) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get project key from project directory or current working directory
	projectKey := m.projectDir
	if projectKey == "" {
		var err error
		projectKey, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get project directory: %w", err)
		}
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))

	switch action {
	case watcher.ConflictActionWait:
		// Wait action: nothing to do, user will wait for reservation to expire
		log.Printf("[ConflictAction] Waiting for reservation to expire: %s (held by %v)", conflict.Path, conflict.Holders)
		return nil

	case watcher.ConflictActionRequest:
		// Request action: send a message to the holder requesting handoff
		if len(conflict.Holders) == 0 {
			return fmt.Errorf("no holders to request handoff from")
		}

		agentName := m.ensureDashboardConflictAgent(ctx, client, projectKey)

		// Send handoff request to each holder
		for _, holder := range conflict.Holders {
			subject := fmt.Sprintf("Handoff Request: %s", conflict.Path)
			body := fmt.Sprintf("**File Handoff Request**\n\n"+
				"Agent `%s` needs to edit the file:\n"+
				"```\n%s\n```\n\n"+
				"You currently hold the reservation for this file.\n"+
				"Please release the reservation when you're done editing, or confirm you're still actively working on it.\n\n"+
				"*Sent via NTM Dashboard conflict resolution*",
				conflict.RequestorAgent, conflict.Path)

			_, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
				ProjectKey:  projectKey,
				SenderName:  agentName,
				To:          []string{holder},
				Subject:     subject,
				BodyMD:      body,
				Importance:  "high",
				AckRequired: true,
			})
			if err != nil {
				log.Printf("[ConflictAction] Failed to send handoff request to %s: %v", holder, err)
				return fmt.Errorf("failed to send handoff request to %s: %w", holder, err)
			}
			log.Printf("[ConflictAction] Sent handoff request to %s for %s", holder, conflict.Path)
		}
		return nil

	case watcher.ConflictActionForce:
		// Force action: force-release the reservation via Agent Mail
		if len(conflict.HolderReservationIDs) == 0 {
			return fmt.Errorf("no reservation IDs available for force-release")
		}
		agentName := m.ensureDashboardConflictAgent(ctx, client, projectKey)

		// Force-release each reservation
		for _, reservationID := range conflict.HolderReservationIDs {
			result, err := client.ForceReleaseReservation(ctx, agentmail.ForceReleaseOptions{
				ProjectKey:     projectKey,
				AgentName:      agentName,
				ReservationID:  reservationID,
				Note:           fmt.Sprintf("Force-released by %s via NTM dashboard", conflict.RequestorAgent),
				NotifyPrevious: true,
			})
			if err != nil {
				log.Printf("[ConflictAction] Failed to force-release reservation %d: %v", reservationID, err)
				return fmt.Errorf("failed to force-release reservation %d: %w", reservationID, err)
			}
			log.Printf("[ConflictAction] Force-released reservation %d: success=%v", reservationID, result.Success)
		}
		return nil

	case watcher.ConflictActionDismiss:
		// Dismiss action: just remove the notification (handled by the panel)
		log.Printf("[ConflictAction] Dismissed conflict notification: %s", conflict.Path)
		return nil

	default:
		return fmt.Errorf("unknown conflict action: %v", action)
	}
}

// executeReplay executes a replay of a history entry
func (m Model) executeReplay(entry history.HistoryEntry) tea.Cmd {
	return func() tea.Msg {
		// Create a new history entry for the replay
		replayEntry := history.NewEntry(m.session, entry.Targets, entry.Prompt, history.SourceReplay)

		// Set template if the original entry used one
		if entry.Template != "" {
			replayEntry.Template = entry.Template
		}

		// Append to history
		if err := history.Append(replayEntry); err != nil {
			log.Printf("[Replay] Failed to append replay entry to history: %v", err)
			replayEntry.SetError(err)
		} else {
			// Execute the replay using tmux client
			client := tmux.DefaultClient

			// Parse targets - for now, replay to the same targets as the original entry
			// In the future, we could show a dialog to let user choose new targets
			for _, targetStr := range entry.Targets {
				target := fmt.Sprintf("%s:%s", m.session, targetStr)
				if err := client.SendKeys(target, entry.Prompt, true); err != nil {
					log.Printf("[Replay] Failed to send to target %s: %v", targetStr, err)
					replayEntry.SetError(fmt.Errorf("failed to send to target %s: %w", targetStr, err))
				}
			}

			// If no errors occurred, mark as successful
			if replayEntry.Error == "" {
				replayEntry.SetSuccess()
			}
		}

		// Return a message to update the status
		return struct {
			Entry   history.HistoryEntry
			Success bool
		}{
			Entry:   *replayEntry,
			Success: replayEntry.Success,
		}
	}
}

// Run, RunPopup, RunWithOptions, RunOptions, mouseEnabled moved to run.go
