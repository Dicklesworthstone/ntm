package robot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	normalizedProjectionStaleAfter   = 45 * time.Second
	normalizedAttentionDedupWindow   = 2 * time.Minute
	normalizedProjectionEventSource  = "adapter.work_coordination"
	normalizedProjectionWorkStatus   = "open"
	normalizedProjectionInProgStatus = "in_progress"
)

// RefreshNormalizedProjection collects normalized work/coordination state,
// persists the current runtime projection slice atomically, and publishes
// durable attention events derived from the normalized result.
func RefreshNormalizedProjection(ctx context.Context, store *state.Store, projectDir, sessionName string) error {
	if store == nil {
		return fmt.Errorf("refresh normalized projection: nil state store")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resolvedProjectDir, err := resolveNormalizedProjectDir(projectDir)
	if err != nil {
		return err
	}

	aggregator := adapters.NewSignalAggregator(0)
	aggregator.RegisterAdapter(adapters.NewWorkCoordinationAdapter(
		adapters.DefaultWorkCoordinationAdapterConfig(resolvedProjectDir),
	))

	// The work (bv/br) and tmux (capture, process) collections are
	// independent and each takes seconds on a busy host; run them together.
	var (
		tmuxSnapshot *NormalizedSnapshot
		tmuxErr      error
		tmuxDone     = make(chan struct{})
	)
	go func() {
		defer close(tmuxDone)
		tmuxSnapshot, tmuxErr = collectNormalizedTmuxProjectionContext(ctx, resolvedProjectDir, normalizedProjectionStaleAfter)
	}()
	signals, err := aggregator.Collect(ctx)
	<-tmuxDone
	if err != nil {
		return fmt.Errorf("collect normalized projection: %w", err)
	}
	if signals == nil {
		return nil
	}
	if tmuxErr != nil {
		return fmt.Errorf("collect tmux projection: %w", tmuxErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := persistNormalizedProjection(store, signals, tmuxSnapshot, normalizedProjectionStaleAfter); err != nil {
		return err
	}

	publishNormalizedAttentionSignals(GetAttentionFeed(), store, sessionName, signals)

	if err := ctx.Err(); err != nil {
		return err
	}

	// The work just collected is source-verified. Publish it as the project's
	// durable observation so the snapshot and status that follow read it back
	// (re-verified) instead of collecting again. RuntimeWork rows carry no
	// source identity and are never served as work (GH #283). Unavailable work
	// (no tracker here, br failing) is published as a marker and reported by
	// the readers; it is not a refresh failure.
	err = adapters.PublishDurableWork(ctx, store, adapters.DefaultWorkCoordinationAdapterConfig(resolvedProjectDir), signals.Work)
	if err != nil && !errors.Is(err, adapters.ErrWorkSnapshotUnavailable) {
		return fmt.Errorf("publish durable work: %w", err)
	}
	return nil
}

func resolveNormalizedProjectDir(projectDir string) (string, error) {
	if path := strings.TrimSpace(projectDir); path != "" {
		return path, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve normalized projection directory: %w", err)
	}
	if root, err := git.FindProjectRoot(cwd); err == nil && strings.TrimSpace(root) != "" {
		return root, nil
	}
	return cwd, nil
}

func collectNormalizedTmuxProjection(projectDir string, staleAfter time.Duration) (*NormalizedSnapshot, error) {
	return collectNormalizedTmuxProjectionContext(context.Background(), projectDir, staleAfter)
}

func collectNormalizedTmuxProjectionContext(ctx context.Context, projectDir string, staleAfter time.Duration) (*NormalizedSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := config.LoadMerged(projectDir, config.DefaultPath())
	if err != nil {
		cfg = config.Default()
	}

	_, mailAgents, mailStats := fetchAgentMailDataContext(ctx, projectDir)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sessions, err := tmux.ListSessionsContext(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return &NormalizedSnapshot{
			Sessions:    []state.RuntimeSession{},
			Agents:      []state.RuntimeAgent{},
			CollectedAt: time.Now().UTC(),
		}, nil
	}

	// Optimization: Get all panes across all sessions in one tmux call
	allPanes, err := tmux.GetAllPanesContext(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// Fallback: create empty map if failed
		allPanes = make(map[string][]tmux.Pane)
	}

	adapterConfig := DefaultTmuxAdapterConfig()
	if staleAfter > 0 {
		adapterConfig.SessionStaleness = staleAfter
		adapterConfig.AgentStaleness = staleAfter
	}
	adapter := NewTmuxAdapter(adapterConfig)

	agentsBySession := make(map[string][]Agent, len(sessions))
	outputTailsBySession := make(map[string]map[string]string, len(sessions))

	totalPaneCount := 0
	for _, panes := range allPanes {
		totalPaneCount += len(panes)
	}
	captureJobs := make([]normalizedTmuxCaptureJob, 0, totalPaneCount)
	for _, sess := range sessions {
		panes := allPanes[sess.Name]
		for _, pane := range panes {
			// Service panes are dropped from the agent projection below, so
			// capturing their scrollback would be pure waste (ntm#305).
			if pane.IsServicePane() {
				continue
			}
			captureJobs = append(captureJobs, normalizedTmuxCaptureJob{
				paneID:    pane.ID,
				modelName: modelNameForPane(pane, cfg),
			})
		}
	}
	allCapturedContent, err := captureNormalizedTmuxPanes(
		ctx,
		captureJobs,
		10,
		tmux.CaptureForStatusDetectionContext,
		tmux.CaptureForFullContextContext,
	)
	if err != nil {
		return nil, err
	}

	for i := range sessions {
		panes := allPanes[sessions[i].Name]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		paneTranscripts, err := resolvePaneTranscriptsContext(ctx, panes)
		if err != nil {
			return nil, err
		}
		// Ensure panes are associated with the session for the adapter
		sessions[i].Panes = append([]tmux.Pane(nil), panes...)

		agentMapping := resolveAgentsForSession(panes, mailAgents)
		agents := make([]Agent, 0, len(panes))
		outputTails := make(map[string]string, len(panes))

		for _, pane := range panes {
			// Tagged service panes never enter the agent projection: they are
			// not agents, so counting them made a quiet service look like a
			// failed agent (ntm#305).
			if pane.IsServicePane() {
				continue
			}
			agent := Agent{
				Pane:     pane.ID,
				Window:   pane.WindowIndex,
				PaneIdx:  pane.Index,
				IsActive: pane.Active,
				Variant:  pane.Variant,
				PID:      pane.PID,
			}

			agent.Type = paneAgentType(pane)

			if mappedName, ok := agentMapping[pane.ID]; ok {
				agent.Name = mappedName
			}

			captured := allCapturedContent[pane.ID]
			content := captured.content
			agent.CaptureError = captured.captureErr
			modelName := paneLaunchModelContext(ctx, pane.ID)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if modelName == "" {
				modelName = detectModel(agent.Type, pane.Title)
			}
			// Context is enriched once below with attributed evidence, rather
			// than estimating it here and discarding it during normalization.
			enrichAgentStatus(&agent, sessions[i].Name, "", content)
			enrichAgentContext(&agent, modelName, content, paneTranscripts[pane.ID])
			if content != "" {
				outputTails[pane.ID] = content
			}

			agents = append(agents, agent)
		}

		agentsBySession[sessions[i].Name] = agents
		outputTailsBySession[sessions[i].Name] = outputTails
	}

	snapshot := adapter.NormalizeSnapshot(sessions, agentsBySession, outputTailsBySession)
	if snapshot == nil {
		snapshot = &NormalizedSnapshot{
			Sessions:    []state.RuntimeSession{},
			Agents:      []state.RuntimeAgent{},
			CollectedAt: time.Now().UTC(),
		}
	}

	collectedAt := snapshot.CollectedAt.UTC()
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	if staleAfter <= 0 {
		staleAfter = normalizedProjectionStaleAfter
	}
	expiresAt := collectedAt.Add(staleAfter)

	for i := range snapshot.Sessions {
		snapshot.Sessions[i].CollectedAt = collectedAt
		snapshot.Sessions[i].StaleAfter = expiresAt
	}
	for i := range snapshot.Agents {
		snapshot.Agents[i].CollectedAt = collectedAt
		snapshot.Agents[i].StaleAfter = expiresAt
		if stats, ok := mailStats[snapshot.Agents[i].AgentMailName]; ok {
			snapshot.Agents[i].PendingMail = stats.Unread
		}
	}

	return snapshot, nil
}

type normalizedTmuxCaptureJob struct {
	paneID    string
	modelName string
}

type normalizedTmuxCaptureResult struct {
	paneID     string
	content    string
	captureErr string
}

type normalizedTmuxCaptureFunc func(context.Context, string) (string, error)

func captureNormalizedTmuxPanes(
	ctx context.Context,
	captureJobs []normalizedTmuxCaptureJob,
	workerLimit int,
	captureStatus normalizedTmuxCaptureFunc,
	captureFull normalizedTmuxCaptureFunc,
) (map[string]normalizedTmuxCaptureResult, error) {
	if ctx == nil {
		return nil, errors.New("normalized tmux capture context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if captureStatus == nil || captureFull == nil {
		return nil, errors.New("normalized tmux capture functions are required")
	}
	if len(captureJobs) == 0 {
		return map[string]normalizedTmuxCaptureResult{}, nil
	}
	if workerLimit <= 0 || workerLimit > len(captureJobs) {
		workerLimit = len(captureJobs)
	}

	jobs := make(chan normalizedTmuxCaptureJob, len(captureJobs))
	results := make(chan normalizedTmuxCaptureResult, len(captureJobs))
	for _, job := range captureJobs {
		select {
		case jobs <- job:
		case <-ctx.Done():
			close(jobs)
			return nil, ctx.Err()
		}
	}
	close(jobs)

	var workers sync.WaitGroup
	workers.Add(workerLimit)
	for range workerLimit {
		go func() {
			defer workers.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				capture := captureStatus
				if job.modelName != "" {
					capture = captureFull
				}
				content, err := capture(ctx, job.paneID)
				captureErr := ""
				if err != nil {
					content = ""
					captureErr = err.Error()
				}
				select {
				case results <- normalizedTmuxCaptureResult{paneID: job.paneID, content: content, captureErr: captureErr}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	workers.Wait()
	close(results)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	captured := make(map[string]normalizedTmuxCaptureResult, len(results))
	for result := range results {
		captured[result.paneID] = result
	}
	return captured, nil
}

func persistNormalizedProjection(store *state.Store, signals *adapters.AggregatedSignals, tmuxSnapshot *NormalizedSnapshot, staleAfter time.Duration) error {
	if store == nil || signals == nil {
		return nil
	}

	collectedAt := signals.CollectedAt.UTC()
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	if staleAfter <= 0 {
		staleAfter = normalizedProjectionStaleAfter
	}
	expiresAt := collectedAt.Add(staleAfter)

	workRows := buildRuntimeWorkRows(signals.Work, collectedAt, expiresAt)
	coordinationRows := buildRuntimeCoordinationRows(signals.Coordination, collectedAt, expiresAt)
	handoffRow := buildRuntimeHandoffRow(signals.Coordination, collectedAt, expiresAt)
	quotaRows := buildRuntimeQuotaRows(signals.Quota, collectedAt, expiresAt)
	healthRows := buildSourceHealthRows(signals.Health, collectedAt)
	sessionRows := buildRuntimeSessionRows(tmuxSnapshot, collectedAt, expiresAt)
	agentRows := buildRuntimeAgentRows(tmuxSnapshot, collectedAt, expiresAt)

	existingWork, err := store.ListFreshRuntimeWork("", 0)
	if err != nil {
		return fmt.Errorf("list runtime work: %w", err)
	}
	existingSessions, err := store.GetFreshRuntimeSessions()
	if err != nil {
		return fmt.Errorf("list runtime sessions: %w", err)
	}
	existingCoordination, err := store.ListFreshRuntimeCoordination("")
	if err != nil {
		return fmt.Errorf("list runtime coordination: %w", err)
	}
	existingQuota, err := store.ListFreshRuntimeQuota("")
	if err != nil {
		return fmt.Errorf("list runtime quota: %w", err)
	}
	existingHealth, err := store.GetAllSourceHealth()
	if err != nil {
		return fmt.Errorf("list source health: %w", err)
	}
	existingAgentsBySession := make(map[string][]state.RuntimeAgent, len(existingSessions))
	for _, item := range existingSessions {
		agents, err := store.GetRuntimeAgentsBySession(item.Name)
		if err != nil {
			return fmt.Errorf("list runtime agents for %s: %w", item.Name, err)
		}
		existingAgentsBySession[item.Name] = agents
	}
	if err := persistNormalizedIncidents(store, signals); err != nil {
		return err
	}

	return store.Transaction(func(tx *state.Tx) error {
		for _, item := range existingWork {
			if _, ok := workRows[item.BeadID]; ok {
				continue
			}
			if err := tx.DeleteRuntimeWork(item.BeadID); err != nil {
				return err
			}
		}
		for _, item := range existingCoordination {
			if _, ok := coordinationRows[item.AgentName]; ok {
				continue
			}
			if err := tx.DeleteRuntimeCoordination(item.AgentName); err != nil {
				return err
			}
		}
		for _, item := range existingQuota {
			key := runtimeQuotaKey(item.Provider, item.Account)
			if _, ok := quotaRows[key]; ok {
				continue
			}
			if err := tx.DeleteRuntimeQuota(item.Provider, item.Account); err != nil {
				return err
			}
		}
		for _, item := range existingHealth {
			if _, ok := healthRows[item.SourceName]; ok {
				continue
			}
			if err := tx.DeleteSourceHealth(item.SourceName); err != nil {
				return err
			}
		}
		for _, item := range existingSessions {
			if _, ok := sessionRows[item.Name]; ok {
				continue
			}
			if err := tx.DeleteRuntimeSession(item.Name); err != nil {
				return err
			}
		}
		for _, sessionKey := range sortedMapKeys(existingAgentsBySession) {
			if _, ok := sessionRows[sessionKey]; !ok {
				continue
			}
			for _, agent := range existingAgentsBySession[sessionKey] {
				if _, ok := agentRows[agent.ID]; ok {
					continue
				}
				if err := tx.DeleteRuntimeAgent(agent.ID); err != nil {
					return err
				}
			}
		}

		for _, key := range sortedMapKeys(workRows) {
			if err := tx.UpsertRuntimeWork(workRows[key]); err != nil {
				return err
			}
		}
		for _, key := range sortedMapKeys(sessionRows) {
			if err := tx.UpsertRuntimeSession(sessionRows[key]); err != nil {
				return err
			}
		}
		for _, key := range sortedMapKeys(agentRows) {
			if err := tx.UpsertRuntimeAgent(agentRows[key]); err != nil {
				return err
			}
		}
		for _, key := range sortedMapKeys(coordinationRows) {
			if err := tx.UpsertRuntimeCoordination(coordinationRows[key]); err != nil {
				return err
			}
		}
		for _, key := range sortedMapKeys(quotaRows) {
			if err := tx.UpsertRuntimeQuota(quotaRows[key]); err != nil {
				return err
			}
		}
		for _, key := range sortedMapKeys(healthRows) {
			if err := tx.UpsertSourceHealth(healthRows[key]); err != nil {
				return err
			}
		}
		switch {
		case handoffRow != nil:
			if err := tx.UpsertRuntimeHandoff(handoffRow); err != nil {
				return err
			}
		case coordinationSectionIsAuthoritative(signals.Coordination):
			// Coordination reported successfully and carries no handoff, so the
			// projection is authoritatively empty for every scope and the stale
			// rows should go.
			if err := tx.DeleteRuntimeHandoff(); err != nil {
				return err
			}
		default:
			// The coordination source was unavailable this tick, so a nil handoff
			// row means "unknown", not "none". Leave the existing projection in
			// place and let stale_after expire it, rather than blanking every
			// session's handoff surface on a transient Agent Mail outage.
		}
		return nil
	})
}

func persistNormalizedIncidents(store *state.Store, signals *adapters.AggregatedSignals) error {
	if store == nil || signals == nil || len(signals.Incidents) == 0 {
		return reconcileNormalizedIncidents(store, nil)
	}

	activeFingerprints := make(map[string]struct{}, len(signals.Incidents))
	for i := range signals.Incidents {
		draft := runtimeIncidentFromAdapter(signals.Incidents[i])
		if draft.Fingerprint != "" {
			activeFingerprints[draft.Fingerprint] = struct{}{}
		}
		persisted, err := store.CreateOrUpdateIncident(draft)
		if err != nil {
			return fmt.Errorf("upsert incident %s: %w", robotFirstNonEmpty(signals.Incidents[i].ID, signals.Incidents[i].Title), err)
		}
		signals.Incidents[i] = adapterIncidentFromState(signals.Incidents[i], persisted)
	}
	return reconcileNormalizedIncidents(store, activeFingerprints)
}

func runtimeIncidentFromAdapter(item adapters.IncidentItem) *state.Incident {
	now := time.Now().UTC()
	startedAt := parseRobotTimestamp(item.CreatedAt)
	if startedAt == nil || startedAt.IsZero() {
		startedAt = &now
	}
	lastEventAt := parseRobotTimestamp(item.UpdatedAt)
	if lastEventAt == nil || lastEventAt.IsZero() {
		lastEventAt = startedAt
	}

	alertCount := item.AlertCount
	if alertCount <= 0 {
		alertCount = 1
	}

	return &state.Incident{
		ID:           strings.TrimSpace(item.ID),
		Title:        robotFirstNonEmpty(strings.TrimSpace(item.Title), strings.TrimSpace(item.Type)),
		Fingerprint:  normalizedIncidentFingerprint(item),
		Family:       robotFirstNonEmpty(strings.TrimSpace(item.Type), "incident"),
		Category:     robotFirstNonEmpty(strings.TrimSpace(item.Type), "incident"),
		Status:       normalizedIncidentStatus(item.Status),
		Severity:     normalizedIncidentStoreSeverity(item.Severity),
		SessionNames: jsonStringOrEmpty(sortedCompactStrings([]string{strings.TrimSpace(item.Session)})),
		AgentIDs:     jsonStringOrEmpty(sortedCompactStrings(item.Agents)),
		AlertCount:   alertCount,
		EventCount:   item.EventCount,
		StartedAt:    startedAt.UTC(),
		LastEventAt:  lastEventAt.UTC(),
		ResolvedAt:   parseRobotTimestamp(item.ResolvedAt),
		RootCause:    strings.TrimSpace(item.RootCause),
		Resolution:   strings.TrimSpace(item.Resolution),
		Notes:        strings.TrimSpace(item.Description),
	}
}

func adapterIncidentFromState(base adapters.IncidentItem, incident *state.Incident) adapters.IncidentItem {
	if incident == nil {
		return base
	}

	updated := base
	updated.ID = strings.TrimSpace(incident.ID)
	updated.Title = robotFirstNonEmpty(strings.TrimSpace(incident.Title), updated.Title)
	updated.Type = robotFirstNonEmpty(strings.TrimSpace(incident.Category), updated.Type)
	updated.Status = strings.TrimSpace(string(incident.Status))
	updated.Severity = strings.TrimSpace(string(incident.Severity))
	updated.AlertCount = incident.AlertCount
	updated.EventCount = incident.EventCount
	updated.CreatedAt = FormatTimestamp(incident.StartedAt)
	updated.UpdatedAt = FormatTimestamp(incident.LastEventAt)
	updated.ResolvedAt = FormatTimestampPtr(incident.ResolvedAt)
	if updated.Session == "" {
		sessions := decodeStringList(incident.SessionNames)
		if len(sessions) > 0 {
			updated.Session = sessions[0]
		}
	}
	if len(updated.Agents) == 0 {
		updated.Agents = decodeStringList(incident.AgentIDs)
	}
	return updated
}

func normalizedIncidentFingerprint(item adapters.IncidentItem) string {
	payload := map[string]any{
		"type":          strings.TrimSpace(item.Type),
		"title":         strings.TrimSpace(item.Title),
		"session":       strings.TrimSpace(item.Session),
		"panes":         sortedCompactStrings(item.Panes),
		"agents":        sortedCompactStrings(item.Agents),
		"related_beads": sortedCompactStrings(item.RelatedBeads),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Fallback to deterministic hash from key fields when marshal fails
		fallback := fmt.Sprintf("%s:%s:%s", item.Type, item.Title, item.Session)
		sum := sha256.Sum256([]byte(fallback))
		return fmt.Sprintf("incident-%x", sum[:8])
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("incident-%x", sum[:8])
}

func normalizedIncidentStatus(raw string) state.IncidentStatus {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(state.IncidentStatusResolved):
		return state.IncidentStatusResolved
	case string(state.IncidentStatusMuted):
		return state.IncidentStatusMuted
	case string(state.IncidentStatusInvestigating):
		return state.IncidentStatusInvestigating
	default:
		return state.IncidentStatusOpen
	}
}

func normalizedIncidentStoreSeverity(raw string) state.Severity {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "p0", string(SeverityCritical):
		return state.SeverityCritical
	case "p1", string(SeverityError):
		return state.SeverityError
	case "p2", string(SeverityWarning):
		return state.SeverityWarning
	case string(SeverityInfo):
		return state.SeverityInfo
	default:
		return state.SeverityWarning
	}
}

func reconcileNormalizedIncidents(store *state.Store, activeFingerprints map[string]struct{}) error {
	if store == nil {
		return nil
	}

	openIncidents, err := store.ListOpenIncidents()
	if err != nil {
		return fmt.Errorf("list open incidents for reconciliation: %w", err)
	}

	for _, incident := range openIncidents {
		if !shouldAutoResolveNormalizedIncident(incident) {
			continue
		}
		if _, ok := activeFingerprints[incident.Fingerprint]; ok {
			continue
		}
		if err := store.UpdateIncidentStatus(
			incident.ID,
			state.IncidentStatusResolved,
			"system",
			"auto-resolved: promoted source alert no longer active in normalized projection",
		); err != nil {
			return fmt.Errorf("auto-resolve incident %s: %w", incident.ID, err)
		}
	}

	return nil
}

func shouldAutoResolveNormalizedIncident(incident state.Incident) bool {
	if strings.TrimSpace(incident.Fingerprint) == "" {
		return false
	}
	return strings.Contains(incident.Notes, "Promoted from alert ")
}

func buildRuntimeSessionRows(snapshot *NormalizedSnapshot, collectedAt, staleAfter time.Time) map[string]*state.RuntimeSession {
	rows := make(map[string]*state.RuntimeSession)
	if snapshot == nil {
		return rows
	}

	for _, item := range snapshot.Sessions {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}

		row := item
		row.Name = name
		row.Label = strings.TrimSpace(row.Label)
		row.ProjectPath = strings.TrimSpace(row.ProjectPath)
		row.HealthReason = strings.TrimSpace(row.HealthReason)
		row.CollectedAt = collectedAt
		row.StaleAfter = staleAfter
		rows[name] = &row
	}

	return rows
}

func buildRuntimeAgentRows(snapshot *NormalizedSnapshot, collectedAt, staleAfter time.Time) map[string]*state.RuntimeAgent {
	rows := make(map[string]*state.RuntimeAgent)
	if snapshot == nil {
		return rows
	}

	for _, item := range snapshot.Agents {
		sessionName := strings.TrimSpace(item.SessionName)
		pane := strings.TrimSpace(item.Pane)
		agentID := strings.TrimSpace(item.ID)
		if agentID == "" && sessionName != "" && pane != "" {
			agentID = fmt.Sprintf("%s:%s", sessionName, pane)
		}
		if agentID == "" || sessionName == "" || pane == "" {
			continue
		}

		row := item
		row.ID = agentID
		row.SessionName = sessionName
		row.Pane = pane
		row.AgentType = strings.TrimSpace(row.AgentType)
		row.Variant = strings.TrimSpace(row.Variant)
		row.TypeMethod = strings.TrimSpace(row.TypeMethod)
		row.StateReason = strings.TrimSpace(row.StateReason)
		row.PreviousState = strings.TrimSpace(row.PreviousState)
		row.CurrentBead = strings.TrimSpace(row.CurrentBead)
		row.AgentMailName = strings.TrimSpace(row.AgentMailName)
		row.HealthReason = strings.TrimSpace(row.HealthReason)
		row.CollectedAt = collectedAt
		row.StaleAfter = staleAfter
		rows[agentID] = &row
	}

	return rows
}

func buildRuntimeWorkRows(section *adapters.WorkSection, collectedAt, staleAfter time.Time) map[string]*state.RuntimeWork {
	rows := make(map[string]*state.RuntimeWork)
	if section == nil {
		return rows
	}

	topRecommendationID := ""
	topRecommendationReason := ""
	if section.Triage != nil && section.Triage.TopRecommendation != nil {
		topRecommendationID = strings.TrimSpace(section.Triage.TopRecommendation.ID)
		topRecommendationReason = strings.Join(section.Triage.TopRecommendation.Reasons, "; ")
	}

	assign := func(item adapters.WorkItem, status string) {
		beadID := strings.TrimSpace(item.ID)
		if beadID == "" {
			return
		}

		row := &state.RuntimeWork{
			BeadID:          beadID,
			Title:           robotFirstNonEmpty(item.Title, beadID),
			TitleDisclosure: jsonStringOrEmpty(item.TitleDisclosure),
			Status:          status,
			Priority:        item.Priority,
			BeadType:        strings.TrimSpace(item.Type),
			Assignee:        strings.TrimSpace(item.Assignee),
			BlockedByCount:  len(item.BlockedBy),
			UnblocksCount:   item.Unblocks,
			Labels:          jsonStringOrEmpty(item.Labels),
			CollectedAt:     collectedAt,
			StaleAfter:      staleAfter,
		}
		if item.Score != nil {
			score := *item.Score
			row.Score = &score
		}
		if item.Assignee != "" && item.UpdatedAt != "" {
			row.ClaimedAt = parseRobotTimestamp(item.UpdatedAt)
		}
		if beadID == topRecommendationID {
			row.ScoreReason = strings.TrimSpace(topRecommendationReason)
		}

		existing := rows[beadID]
		if existing != nil && runtimeWorkStatusPrecedence(existing.Status) > runtimeWorkStatusPrecedence(status) {
			return
		}
		rows[beadID] = row
	}

	for _, item := range section.Ready {
		assign(item, normalizedProjectionWorkStatus)
	}
	for _, item := range section.Blocked {
		assign(item, normalizedProjectionWorkStatus)
		// A blocked bead's blocker IDs come from triage, which may not list
		// it. It is still blocked: stored as an open row with no blockers, it
		// would read back as ready (GH #283 field case a).
		if row := rows[strings.TrimSpace(item.ID)]; row != nil && row.Status == normalizedProjectionWorkStatus && row.BlockedByCount == 0 {
			row.BlockedByCount = 1
		}
	}
	for _, item := range section.InProgress {
		assign(item, normalizedProjectionInProgStatus)
	}

	return rows
}

func buildRuntimeCoordinationRows(section *adapters.CoordinationSection, collectedAt, staleAfter time.Time) map[string]*state.RuntimeCoordination {
	rows := make(map[string]*state.RuntimeCoordination)
	if section == nil || section.Mail == nil {
		return rows
	}

	latestMessageAt := parseRobotTimestamp(section.Mail.LatestMessage)
	for agentName, stats := range section.Mail.ByAgent {
		trimmedName := strings.TrimSpace(agentName)
		if trimmedName == "" {
			continue
		}
		agentLatestMessageAt := parseRobotTimestamp(stats.LatestMessage)
		if agentLatestMessageAt == nil {
			agentLatestMessageAt = latestMessageAt
		}
		rows[trimmedName] = &state.RuntimeCoordination{
			AgentName:                    trimmedName,
			SessionName:                  "",
			Pane:                         strings.TrimSpace(stats.Pane),
			UnreadCount:                  stats.Unread,
			PendingAckCount:              stats.Pending,
			UrgentCount:                  stats.Urgent,
			LastMessageSubject:           strings.TrimSpace(stats.LatestSubject),
			LastMessageSubjectDisclosure: jsonStringOrEmpty(stats.LatestSubjectDisclosure),
			LastMessagePreview:           strings.TrimSpace(stats.LatestPreview),
			LastMessagePreviewDisclosure: jsonStringOrEmpty(stats.LatestPreviewDisclosure),
			LastMessageAt:                agentLatestMessageAt,
			LastReceivedAt:               agentLatestMessageAt,
			CollectedAt:                  collectedAt,
			StaleAfter:                   staleAfter,
		}
	}

	return rows
}

// coordinationSectionIsAuthoritative reports whether the coordination signals
// for this tick are trustworthy enough to treat an absent handoff as "there is
// no handoff" rather than "we could not tell".
//
// The section carries its own Available flag precisely because the underlying
// sources (Agent Mail, threads, reservations) can be unreachable. Reconciling
// durable projections against an unavailable section deletes rows on a transient
// outage.
func coordinationSectionIsAuthoritative(section *adapters.CoordinationSection) bool {
	return section != nil && section.Available
}

func buildRuntimeHandoffRow(section *adapters.CoordinationSection, collectedAt, staleAfter time.Time) *state.RuntimeHandoff {
	if section == nil || section.Handoff == nil {
		return nil
	}
	handoff := section.Handoff
	if strings.TrimSpace(handoff.Session) == "" &&
		strings.TrimSpace(handoff.Goal) == "" &&
		strings.TrimSpace(handoff.Now) == "" &&
		strings.TrimSpace(handoff.Status) == "" {
		return nil
	}
	return &state.RuntimeHandoff{
		SessionName:        strings.TrimSpace(handoff.Session),
		Status:             strings.TrimSpace(handoff.Status),
		Goal:               strings.TrimSpace(handoff.Goal),
		GoalDisclosure:     jsonStringOrEmpty(handoff.GoalDisclosure),
		NowText:            strings.TrimSpace(handoff.Now),
		NowDisclosure:      jsonStringOrEmpty(handoff.NowDisclosure),
		UpdatedAt:          parseRobotTimestamp(handoff.UpdatedAt),
		ActiveBeads:        jsonStringOrEmpty(handoff.ActiveBeads),
		AgentMailThreads:   jsonStringOrEmpty(handoff.AgentMailThreads),
		Blockers:           jsonStringOrEmpty(handoff.Blockers),
		BlockerDisclosures: jsonStringOrEmpty(handoff.BlockerDisclosures),
		Files:              jsonStringOrEmpty(handoff.Files),
		CollectedAt:        collectedAt,
		StaleAfter:         staleAfter,
	}
}

func buildRuntimeQuotaRows(section *adapters.QuotaSection, collectedAt, staleAfter time.Time) map[string]*state.RuntimeQuota {
	rows := make(map[string]*state.RuntimeQuota)
	if section == nil {
		return rows
	}

	for _, account := range section.Accounts {
		provider := strings.TrimSpace(account.Provider)
		id := robotFirstNonEmpty(strings.TrimSpace(account.ID), provider)
		if provider == "" || id == "" {
			continue
		}

		usedPct, usedPctKnown, usedPctSource := quotaUsedPercent(account)
		healthy := !strings.EqualFold(account.Status, "critical") && !strings.EqualFold(account.Status, "exceeded")
		row := &state.RuntimeQuota{
			Provider:      provider,
			Account:       id,
			LimitHit:      strings.EqualFold(account.Status, "exceeded"),
			UsedPct:       usedPct,
			UsedPctKnown:  usedPctKnown,
			UsedPctSource: usedPctSource,
			ResetsAt:      parseRobotTimestamp(account.ResetAt),
			IsActive:      account.IsActive,
			Healthy:       healthy,
			HealthReason:  strings.TrimSpace(string(account.ReasonCode)),
			CollectedAt:   collectedAt,
			StaleAfter:    staleAfter,
		}
		rows[runtimeQuotaKey(provider, id)] = row
	}

	return rows
}

func buildSourceHealthRows(section *adapters.SourceHealthSection, collectedAt time.Time) map[string]*state.SourceHealth {
	rows := make(map[string]*state.SourceHealth)
	if section == nil {
		return rows
	}

	for sourceName, info := range section.Sources {
		trimmedSource := strings.TrimSpace(sourceName)
		if trimmedSource == "" {
			continue
		}

		healthy := info.Available && info.Fresh && !info.Degraded
		row := &state.SourceHealth{
			SourceName:          trimmedSource,
			Available:           info.Available,
			Healthy:             healthy,
			Reason:              strings.TrimSpace(robotFirstNonEmpty(info.DegradedReason, string(info.ReasonCode))),
			LastSuccessAt:       parseRobotTimestamp(info.UpdatedAt),
			LastFailureAt:       parseRobotTimestamp(info.DegradedSince),
			LastCheckAt:         collectedAt,
			ConsecutiveFailures: 0,
			LastError:           strings.TrimSpace(info.LastError),
			LastErrorCode:       strings.TrimSpace(string(info.ReasonCode)),
		}
		if !healthy {
			row.ConsecutiveFailures = 1
		}
		if healthy && row.Reason == string(adapters.ReasonHealthOK) {
			row.Reason = ""
		}
		rows[trimmedSource] = row
	}

	return rows
}

func publishNormalizedAttentionSignals(feed *AttentionFeed, store *state.Store, sessionName string, signals *adapters.AggregatedSignals) {
	if feed == nil || signals == nil {
		return
	}
	for _, event := range normalizedAttentionEvents(sessionName, signals) {
		appended, ok := feed.AppendDeduplicated(event, normalizedAttentionDedupWindow)
		if ok {
			linkIncidentAttentionEvent(store, appended)
		}
	}
}

func linkIncidentAttentionEvent(store *state.Store, event AttentionEvent) {
	if store == nil || event.Category != EventCategoryIncident || event.Cursor <= 0 {
		return
	}
	incidentID := attentionStringDetail(event.Details, "incident_id")
	if incidentID == "" {
		return
	}
	_ = store.LinkEventToIncident(incidentID, event.Cursor)
}

func normalizedAttentionEvents(sessionName string, signals *adapters.AggregatedSignals) []AttentionEvent {
	if signals == nil {
		return nil
	}

	session := normalizedProjectionSession(sessionName, signals)
	ts := signals.CollectedAt.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	events := make([]AttentionEvent, 0, 8)
	if event := normalizedTopRecommendationEvent(ts, session, signals.Work); event != nil {
		events = append(events, *event)
	}
	events = append(events, normalizedIncidentEvents(ts, session, signals.Incidents)...)
	events = append(events, normalizedSourceHealthEvents(ts, session, signals.Health)...)
	events = append(events, normalizedCoordinationProblemEvents(ts, session, signals.Coordination)...)
	return events
}

func normalizedIncidentEvents(ts time.Time, session string, incidents []adapters.IncidentItem) []AttentionEvent {
	if len(incidents) == 0 {
		return nil
	}

	events := make([]AttentionEvent, 0, len(incidents))
	for _, incident := range incidents {
		event, ok := normalizedIncidentEvent(ts, session, incident)
		if !ok {
			continue
		}
		events = append(events, event)
	}
	return events
}

func normalizedIncidentEvent(ts time.Time, session string, incident adapters.IncidentItem) (AttentionEvent, bool) {
	incidentID := strings.TrimSpace(incident.ID)
	if incidentID == "" {
		return AttentionEvent{}, false
	}

	eventType := normalizedIncidentEventType(incident)
	severity := normalizedIncidentEventSeverity(incident.Severity)
	actionability := normalizedIncidentActionability(eventType, severity)
	summaryVerb := "incident promoted"
	switch eventType {
	case EventTypeIncidentOpened:
		summaryVerb = "incident opened"
	case EventTypeIncidentRecurred:
		summaryVerb = "incident recurred"
	case EventTypeIncidentResolved:
		summaryVerb = "incident resolved"
	case EventTypeIncidentMuted:
		summaryVerb = "incident muted"
	}

	details := map[string]any{
		"incident_id": incidentID,
		"type":        strings.TrimSpace(incident.Type),
		"status":      strings.TrimSpace(incident.Status),
		"alert_count": incident.AlertCount,
		"event_count": incident.EventCount,
	}
	if detectedBy := strings.TrimSpace(incident.DetectedBy); detectedBy != "" {
		details["detected_by"] = detectedBy
	}
	if len(incident.Panes) > 0 {
		details["panes"] = append([]string(nil), incident.Panes...)
	}
	if len(incident.Agents) > 0 {
		details["agents"] = append([]string(nil), incident.Agents...)
	}
	if len(incident.RelatedAlerts) > 0 {
		details["related_alerts"] = append([]string(nil), incident.RelatedAlerts...)
	}
	if len(incident.RelatedBeads) > 0 {
		details["related_beads"] = append([]string(nil), incident.RelatedBeads...)
		details["bead_id"] = incident.RelatedBeads[0]
	}

	nextActions := []NextAction{
		{
			Action: "robot-snapshot",
			Args:   "--robot-snapshot",
			Reason: "Inspect the current active incidents and related runtime state",
		},
	}
	if len(incident.RelatedBeads) > 0 {
		nextActions = append(nextActions, attentionBeadActions(incident.RelatedBeads[0], "Inspect the related bead")...)
	}

	event := annotateAttentionSignal(AttentionEvent{
		Ts:            ts.Format(time.RFC3339Nano),
		Session:       robotFirstNonEmpty(strings.TrimSpace(incident.Session), session),
		Category:      EventCategoryIncident,
		Type:          eventType,
		Source:        normalizedProjectionEventSource,
		Actionability: actionability,
		Severity:      severity,
		ReasonCode:    normalizedIncidentReasonCode(eventType),
		Summary:       attentionSummary(robotFirstNonEmpty(strings.TrimSpace(incident.Session), session), "", fmt.Sprintf("%s: %s", summaryVerb, robotFirstNonEmpty(strings.TrimSpace(incident.Title), incidentID))),
		Details:       details,
		NextActions:   nextActions,
		DedupKey:      normalizedDedupKey("incident", incidentID, strings.ToLower(strings.TrimSpace(incident.Status)), string(eventType)),
	})
	return event, true
}

func normalizedIncidentEventType(incident adapters.IncidentItem) EventType {
	switch strings.ToLower(strings.TrimSpace(incident.Status)) {
	case string(state.IncidentStatusResolved):
		return EventTypeIncidentResolved
	case string(state.IncidentStatusMuted):
		return EventTypeIncidentMuted
	}
	if incident.AlertCount > 1 || incident.EventCount > 1 {
		return EventTypeIncidentRecurred
	}
	if strings.EqualFold(strings.TrimSpace(incident.DetectedBy), "alert_promotion") {
		return EventTypeIncidentPromoted
	}
	return EventTypeIncidentOpened
}

func normalizedIncidentEventSeverity(raw string) Severity {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "p0", string(SeverityCritical):
		return SeverityCritical
	case "p1", string(SeverityError):
		return SeverityError
	case "p2", string(SeverityWarning):
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

func normalizedIncidentActionability(eventType EventType, severity Severity) Actionability {
	switch eventType {
	case EventTypeIncidentResolved, EventTypeIncidentMuted:
		return ActionabilityBackground
	}
	if attentionSeverityRank(severity) >= attentionSeverityRank(SeverityError) {
		return ActionabilityActionRequired
	}
	return ActionabilityInteresting
}

func normalizedIncidentReasonCode(eventType EventType) string {
	switch eventType {
	case EventTypeIncidentOpened:
		return "incident:opened"
	case EventTypeIncidentRecurred:
		return "incident:recurred"
	case EventTypeIncidentResolved:
		return "incident:resolved"
	case EventTypeIncidentMuted:
		return "incident:muted"
	default:
		return "incident:promoted"
	}
}

func normalizedTopRecommendationEvent(ts time.Time, session string, section *adapters.WorkSection) *AttentionEvent {
	if section == nil || section.Triage == nil || section.Triage.TopRecommendation == nil {
		return nil
	}
	rec := section.Triage.TopRecommendation
	beadID := strings.TrimSpace(rec.ID)
	if beadID == "" {
		return nil
	}

	details := map[string]any{
		"bead_id":  beadID,
		"title":    strings.TrimSpace(rec.Title),
		"priority": rec.Priority,
		"score":    rec.Score,
		"unblocks": rec.Unblocks,
		"source":   "top_recommendation",
	}
	if len(rec.Reasons) > 0 {
		details["reasons"] = append([]string(nil), rec.Reasons...)
	}

	event := AttentionEvent{
		Ts:            ts.Format(time.RFC3339Nano),
		Session:       session,
		Category:      EventCategoryBead,
		Type:          EventTypeBeadUpdated,
		Source:        normalizedProjectionEventSource,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		ReasonCode:    string(adapters.ReasonWorkReadyTopRecommendation),
		Summary:       attentionSummary(session, "", fmt.Sprintf("top ready bead %s: %s", beadID, robotFirstNonEmpty(rec.Title, beadID))),
		Details:       details,
		NextActions:   attentionBeadActions(beadID, "Inspect the top ready bead"),
		DedupKey:      normalizedDedupKey("work", "top_recommendation", beadID),
	}

	annotated := annotateAttentionSignal(event)
	return &annotated
}

func normalizedSourceHealthEvents(ts time.Time, session string, section *adapters.SourceHealthSection) []AttentionEvent {
	if section == nil || len(section.Sources) == 0 {
		return nil
	}

	events := make([]AttentionEvent, 0, len(section.Sources))
	for _, sourceName := range sortedMapKeys(section.Sources) {
		info := section.Sources[sourceName]
		reasonCode := info.ReasonCode
		severity := Severity(adapters.ReasonToSeverity(reasonCode))
		actionability := Actionability(adapters.SeverityToActionability(adapters.ReasonToSeverity(reasonCode), false))
		summary := fmt.Sprintf("source %s healthy", sourceName)
		if info.Degraded || !info.Available || !info.Fresh {
			summary = fmt.Sprintf("source %s degraded", sourceName)
			if reason := strings.TrimSpace(info.DegradedReason); reason != "" {
				summary = fmt.Sprintf("source %s degraded: %s", sourceName, reason)
			}
		}

		details := map[string]any{
			"source_name": sourceName,
			"available":   info.Available,
			"fresh":       info.Fresh,
		}
		if info.UpdatedAt != "" {
			details["updated_at"] = info.UpdatedAt
		}
		if info.AgeMs > 0 {
			details["age_ms"] = info.AgeMs
		}
		if info.DegradedReason != "" {
			details["degraded_reason"] = info.DegradedReason
		}
		if info.DegradedSince != "" {
			details["degraded_since"] = info.DegradedSince
		}
		if info.LastError != "" {
			details["last_error"] = info.LastError
		}

		event := annotateAttentionSignal(AttentionEvent{
			Ts:            ts.Format(time.RFC3339Nano),
			Session:       session,
			Category:      EventCategoryHealth,
			Type:          EventTypeHealthChange,
			Source:        normalizedProjectionEventSource,
			Actionability: actionability,
			Severity:      severity,
			ReasonCode:    string(reasonCode),
			Summary:       attentionSummary(session, "", summary),
			Details:       details,
			NextActions:   []NextAction{attentionStatusNextAction("Inspect normalized source health")},
			DedupKey:      normalizedDedupKey("health", sourceName, string(reasonCode)),
		})
		events = append(events, event)
	}

	return events
}

func normalizedCoordinationProblemEvents(ts time.Time, session string, section *adapters.CoordinationSection) []AttentionEvent {
	if section == nil || len(section.Problems) == 0 {
		return nil
	}

	events := make([]AttentionEvent, 0, len(section.Problems))
	for _, problem := range section.Problems {
		event, ok := normalizedCoordinationProblemEvent(ts, session, problem)
		if !ok {
			continue
		}
		events = append(events, event)
	}
	return events
}

func normalizedCoordinationProblemEvent(ts time.Time, session string, problem adapters.CoordinationProblem) (AttentionEvent, bool) {
	var (
		reasonCode adapters.ReasonCode
		category   EventCategory
		eventType  EventType
		actions    []NextAction
		details    = map[string]any{
			"problem_kind": strings.TrimSpace(problem.Kind),
		}
	)

	agents := sortedCompactStrings(problem.Agents)
	paths := sortedCompactStrings(problem.Paths)
	threadIDs := sortedCompactStrings(problem.ThreadIDs)
	if len(agents) > 0 {
		details["agents"] = agents
	}
	if len(paths) > 0 {
		details["paths"] = paths
		details["path"] = paths[0]
	}
	if len(threadIDs) > 0 {
		details["thread_ids"] = threadIDs
	}

	switch strings.TrimSpace(problem.Kind) {
	case "urgent_mail":
		reasonCode = adapters.ReasonCoordinationUrgentMail
		category = EventCategoryMail
		eventType = EventTypeMailUnread
		actions = []NextAction{attentionStatusNextAction("Inspect urgent unread mail")}
	case "pending_ack":
		reasonCode = adapters.ReasonCoordinationPendingAck
		category = EventCategoryMail
		eventType = EventTypeMailAckRequired
		actions = []NextAction{attentionStatusNextAction("Inspect pending mail acknowledgements")}
	case "mail_backlog":
		reasonCode = adapters.ReasonCoordinationMailBacklog
		category = EventCategoryMail
		eventType = EventTypeMailUnread
		actions = []NextAction{attentionStatusNextAction("Inspect swarm mail backlog")}
	case "reservation_conflict":
		reasonCode = adapters.ReasonCoordinationReservationConflict
		category = EventCategoryFile
		eventType = EventTypeFileConflict
		if len(agents) > 0 {
			details["holders"] = agents
		}
		actions = attentionReservationConflictActions(session, details, "Inspect the conflicting reservation state")
	case "file_conflict":
		reasonCode = adapters.ReasonCoordinationFileConflict
		category = EventCategoryFile
		eventType = EventTypeFileConflict
		if len(agents) > 0 {
			details["agents"] = agents
			details["change_count"] = len(agents)
		}
		if strings.TrimSpace(problem.Severity) != "" {
			details["tracker_severity"] = strings.TrimSpace(problem.Severity)
		}
		actions = attentionConflictActions(session, attentionStringDetail(details, "path"), "Compare conflicting file edits")
	case "handoff_blocked":
		reasonCode = adapters.ReasonCoordinationHandoffBlocked
		category = EventCategoryAlert
		eventType = EventTypeAlertWarning
		actions = []NextAction{attentionStatusNextAction("Inspect blocked handoff state")}
	default:
		return AttentionEvent{}, false
	}

	severity := normalizedCoordinationProblemSeverity(problem.Severity, reasonCode)
	actionability := normalizedCoordinationProblemActionability(strings.TrimSpace(problem.Kind), severity)

	event := annotateAttentionSignal(AttentionEvent{
		Ts:            ts.Format(time.RFC3339Nano),
		Session:       session,
		Category:      category,
		Type:          eventType,
		Source:        normalizedProjectionEventSource,
		Actionability: actionability,
		Severity:      severity,
		ReasonCode:    string(reasonCode),
		Summary:       attentionSummary(session, "", robotFirstNonEmpty(strings.TrimSpace(problem.Summary), strings.TrimSpace(problem.Kind))),
		Details:       details,
		NextActions:   actions,
		DedupKey:      normalizedDedupKey(append([]string{"coordination", problem.Kind}, append(agents, append(paths, threadIDs...)...)...)...),
	})
	return event, true
}

func normalizedCoordinationProblemSeverity(raw string, reasonCode adapters.ReasonCode) Severity {
	switch strings.TrimSpace(raw) {
	case string(adapters.SeverityCritical):
		return SeverityCritical
	case string(adapters.SeverityError):
		return SeverityError
	case string(adapters.SeverityWarning):
		return SeverityWarning
	case string(adapters.SeverityDebug):
		return SeverityDebug
	case string(adapters.SeverityInfo):
		return SeverityInfo
	default:
		return Severity(adapters.ReasonToSeverity(reasonCode))
	}
}

func normalizedCoordinationProblemActionability(kind string, severity Severity) Actionability {
	switch kind {
	case "pending_ack", "reservation_conflict", "file_conflict", "handoff_blocked":
		return ActionabilityActionRequired
	}
	return Actionability(adapters.SeverityToActionability(adapters.Severity(severity), false))
}

func normalizedProjectionSession(sessionName string, signals *adapters.AggregatedSignals) string {
	if session := strings.TrimSpace(sessionName); session != "" {
		return session
	}
	if signals != nil && signals.Coordination != nil && signals.Coordination.Handoff != nil {
		if session := strings.TrimSpace(signals.Coordination.Handoff.Session); session != "" {
			return session
		}
	}
	return ""
}

func runtimeWorkStatusPrecedence(status string) int {
	switch status {
	case normalizedProjectionInProgStatus:
		return 2
	case normalizedProjectionWorkStatus:
		return 1
	default:
		return 0
	}
}

func runtimeQuotaKey(provider, account string) string {
	return strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(account)
}

func quotaUsedPercent(account adapters.AccountQuota) (float64, bool, state.RuntimeQuotaUsedPctSource) {
	if account.UsagePercent != nil {
		return *account.UsagePercent, true, state.RuntimeQuotaUsedPctSourceProvider
	}
	if account.TokensLimit > 0 {
		return float64(account.TokensUsed) / float64(account.TokensLimit) * 100, true, state.RuntimeQuotaUsedPctSourceTokens
	}
	if account.RequestsLimit > 0 {
		return float64(account.RequestsUsed) / float64(account.RequestsLimit) * 100, true, state.RuntimeQuotaUsedPctSourceRequests
	}
	return 0, false, state.RuntimeQuotaUsedPctSourceUnknown
}

func sortedMapKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedCompactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func normalizedDedupKey(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	return strings.Join(filtered, "|")
}

func jsonStringOrEmpty(value any) string {
	data, err := json.Marshal(value)
	if err != nil || string(data) == "null" {
		return ""
	}
	return string(data)
}

func decodeDisclosureMetadata(value string) *adapters.DisclosureMetadata {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	var disclosure adapters.DisclosureMetadata
	if err := json.Unmarshal([]byte(trimmed), &disclosure); err != nil {
		return nil
	}
	return &disclosure
}

func decodeDisclosureMetadataList(value string) []adapters.DisclosureMetadata {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	var disclosures []adapters.DisclosureMetadata
	if err := json.Unmarshal([]byte(trimmed), &disclosures); err != nil {
		return nil
	}
	return disclosures
}

func decodeStringList(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	var items []string
	if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
		return nil
	}
	return items
}

func statusHandoffFromRuntime(row *state.RuntimeHandoff) *HandoffSummary {
	if row == nil {
		return nil
	}
	summary := &HandoffSummary{
		Session:            strings.TrimSpace(row.SessionName),
		Goal:               strings.TrimSpace(row.Goal),
		GoalDisclosure:     decodeDisclosureMetadata(row.GoalDisclosure),
		Now:                strings.TrimSpace(row.NowText),
		NowDisclosure:      decodeDisclosureMetadata(row.NowDisclosure),
		Status:             strings.TrimSpace(row.Status),
		ActiveBeads:        decodeStringList(row.ActiveBeads),
		AgentMailThreads:   decodeStringList(row.AgentMailThreads),
		Blockers:           decodeStringList(row.Blockers),
		BlockerDisclosures: decodeDisclosureMetadataList(row.BlockerDisclosures),
		Files:              decodeStringList(row.Files),
	}
	if row.UpdatedAt != nil && !row.UpdatedAt.IsZero() {
		summary.UpdatedAt = row.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return summary
}

func statusHandoffFromAdapter(handoff *adapters.HandoffSummary) *HandoffSummary {
	if handoff == nil {
		return nil
	}
	summary := &HandoffSummary{
		Session:          strings.TrimSpace(handoff.Session),
		Goal:             strings.TrimSpace(handoff.Goal),
		Now:              strings.TrimSpace(handoff.Now),
		Status:           strings.TrimSpace(handoff.Status),
		UpdatedAt:        strings.TrimSpace(handoff.UpdatedAt),
		ActiveBeads:      append([]string(nil), handoff.ActiveBeads...),
		AgentMailThreads: append([]string(nil), handoff.AgentMailThreads...),
		Blockers:         append([]string(nil), handoff.Blockers...),
		Files:            append([]string(nil), handoff.Files...),
	}
	if handoff.GoalDisclosure != nil {
		disclosure := *handoff.GoalDisclosure
		summary.GoalDisclosure = &disclosure
	}
	if handoff.NowDisclosure != nil {
		disclosure := *handoff.NowDisclosure
		summary.NowDisclosure = &disclosure
	}
	if len(handoff.BlockerDisclosures) > 0 {
		summary.BlockerDisclosures = append([]adapters.DisclosureMetadata(nil), handoff.BlockerDisclosures...)
	}
	return summary
}

func parseRobotTimestamp(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		ts, err := time.Parse(layout, trimmed)
		if err == nil {
			utc := ts.UTC()
			return &utc
		}
	}
	return nil
}

func robotFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
