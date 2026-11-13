package robot

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/reservationpath"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// MailOptions configures the GetMail operation.
type MailOptions struct {
	Session    string
	ProjectKey string
}

// MailOutput represents the output for --robot-mail.
type MailOutput struct {
	RobotResponse
	GeneratedAt  time.Time                   `json:"generated_at"`
	Session      string                      `json:"session,omitempty"`
	ProjectKey   string                      `json:"project_key"`
	Available    bool                        `json:"available"`
	ServerURL    string                      `json:"server_url,omitempty"`
	SessionAgent *agentmail.SessionAgentInfo `json:"session_agent,omitempty"`
	// agents is a required array: it is always present, and empty means
	// "checked, found none" rather than "did not check". Never omitempty.
	Agents           []AgentMailAgent       `json:"agents"`
	UnmappedAgents   []AgentMailAgent       `json:"unmapped_agents,omitempty"`
	Messages         AgentMailMessageCounts `json:"messages,omitempty"`
	FileReservations []AgentMailReservation `json:"file_reservations,omitempty"`
	Conflicts        []AgentMailConflict    `json:"conflicts,omitempty"`
	Warnings         []string               `json:"warnings,omitempty"`
}

// GetMail returns detailed Agent Mail state for AI orchestrators.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetMail(opts MailOptions) (*MailOutput, error) {
	projectKey := opts.ProjectKey
	if cwdProject := util.ResolveProjectDir(""); cwdProject != "" {
		projectKey = util.BestProjectDir(projectKey, cwdProject)
	}

	sessionAgent, err := func() (*agentmail.SessionAgentInfo, error) {
		if opts.Session == "" {
			return nil, nil
		}
		return agentmail.LoadBestSessionAgent(opts.Session, projectKey)
	}()
	if err != nil {
		return nil, err
	}
	if sessionAgent != nil {
		resolvedProjectKey := util.BestProjectDir(projectKey, sessionAgent.ProjectKey)
		if resolvedProjectKey == "" {
			resolvedProjectKey = projectKey
		}
		if strings.TrimSpace(sessionAgent.ProjectKey) != "" && filepath.Clean(sessionAgent.ProjectKey) != filepath.Clean(resolvedProjectKey) {
			sessionAgent = nil
		}
		projectKey = resolvedProjectKey
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	serverURL := client.BaseURL()

	output := &MailOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Session:       opts.Session,
		ProjectKey:    projectKey,
		Available:     false,
		ServerURL:     serverURL,
		SessionAgent:  sessionAgent,
		// Initialized here so the unavailable-server return below still carries
		// the required agents array as [].
		Agents: []AgentMailAgent{},
	}

	if !client.IsAvailable() {
		return output, nil
	}
	output.Available = true

	// Ensure project exists
	if _, err := ensureProjectWithRetry(ctx, client, projectKey); err != nil {
		return mailReadFailed(output, "ensure_project", err), nil
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		return mailReadFailed(output, "list_agents", err), nil
	}

	agentByName := make(map[string]agentmail.Agent, len(agents))
	inboxByName := make(map[string]inboxTally, len(agents))

	// Gather per-agent mail counts (best-effort).
	for _, a := range agents {
		if a.Name == "HumanOverseer" {
			continue
		}
		agentByName[a.Name] = a
		tally := getInboxTally(ctx, client, projectKey, a.Name, 50)
		inboxByName[a.Name] = tally

		output.Messages.Total += tally.Total
		output.Messages.Unread += tally.Unread
		output.Messages.Urgent += tally.Urgent
		output.Messages.PendingAck += tally.PendingAck
	}

	// Best-effort pane mapping when a session is provided and tmux is available.
	assigned := make(map[string]bool)
	if opts.Session != "" && tmux.IsInstalled() && tmux.SessionExists(opts.Session) {
		if panes, err := tmux.GetPanes(opts.Session); err == nil {
			mapping := resolveAgentsForSession(panes, agents)
			paneInfos := parseNTMPanes(panes)

			// Collect and sort all pane types found
			var paneTypes []string
			for t := range paneInfos {
				paneTypes = append(paneTypes, t)
			}
			sort.Strings(paneTypes)

			for _, paneType := range paneTypes {
				for _, pane := range paneInfos[paneType] {
					entry := AgentMailAgent{Pane: pane.Label}
					if agentName, ok := mapping[pane.Label]; ok {
						assigned[agentName] = true
						a := agentByName[agentName]
						tally := inboxByName[agentName]
						entry.AgentName = agentName
						entry.Program = a.Program
						entry.Model = a.Model
						entry.UnreadCount = tally.Unread
						entry.UrgentCount = tally.Urgent
						entry.LastActiveTs = a.LastActiveTS.Time
					}
					output.Agents = append(output.Agents, entry)
				}
			}
		}
	}

	// If no panes were added (no session context), fall back to listing agents as-is.
	if len(output.Agents) == 0 {
		for _, a := range agents {
			if a.Name == "HumanOverseer" {
				continue
			}
			tally := inboxByName[a.Name]
			output.Agents = append(output.Agents, AgentMailAgent{
				AgentName:    a.Name,
				Program:      a.Program,
				Model:        a.Model,
				UnreadCount:  tally.Unread,
				UrgentCount:  tally.Urgent,
				LastActiveTs: a.LastActiveTS.Time,
			})
		}
	} else {
		// Include any registered agents that we couldn't map to panes.
		for _, a := range agents {
			if a.Name == "HumanOverseer" {
				continue
			}
			if a.Program == "ntm" || assigned[a.Name] {
				continue
			}
			tally := inboxByName[a.Name]
			output.UnmappedAgents = append(output.UnmappedAgents, AgentMailAgent{
				AgentName:    a.Name,
				Program:      a.Program,
				Model:        a.Model,
				UnreadCount:  tally.Unread,
				UrgentCount:  tally.Urgent,
				LastActiveTs: a.LastActiveTS.Time,
			})
		}
	}

	reservations, err := client.ListReservations(ctx, projectKey, "", true)
	if err == nil {
		output.FileReservations = summarizeReservations(reservations)
		output.Conflicts = detectReservationConflicts(reservations)
	} else {
		output.Warnings = append(output.Warnings, fmt.Sprintf("list_reservations failed: %v", err))
	}

	return output, nil
}

// mailReadFailed marks output failed when the project's agents could not be
// read: success with an empty agents array would claim the server has none.
func mailReadFailed(output *MailOutput, step string, err error) *MailOutput {
	code, hint := ErrCodeInternalError, "Check the Agent Mail server's health and logs"
	if agentmail.IsUnauthorized(err) {
		code, hint = ErrCodePermissionDenied, "Set [agent_mail] token or AGENT_MAIL_TOKEN to the bearer token the Agent Mail server requires"
	}
	output.RobotResponse = NewErrorResponse(fmt.Errorf("%s failed: %w", step, err), code, hint)
	return output
}

// PrintMail outputs detailed Agent Mail state for AI orchestrators.
// This is a thin wrapper around GetMail() for CLI output.
func PrintMail(sessionName, projectKey string) error {
	output, err := GetMail(MailOptions{
		Session:    sessionName,
		ProjectKey: projectKey,
	})
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot mail failed")
}

// AgentMailAgent is a per-agent view for --robot-mail.
type AgentMailAgent struct {
	Pane         string    `json:"pane,omitempty"`
	AgentName    string    `json:"agent_name,omitempty"`
	Program      string    `json:"program,omitempty"`
	Model        string    `json:"model,omitempty"`
	UnreadCount  int       `json:"unread_count,omitempty"`
	UrgentCount  int       `json:"urgent_count,omitempty"`
	LastActiveTs time.Time `json:"last_active_ts,omitempty"`
}

type AgentMailMessageCounts struct {
	Total      int `json:"total"`
	Unread     int `json:"unread"`
	Urgent     int `json:"urgent"`
	PendingAck int `json:"pending_ack"`
}

type AgentMailReservation struct {
	ID               int    `json:"id"`
	Pattern          string `json:"pattern"`
	Agent            string `json:"agent"`
	Exclusive        bool   `json:"exclusive"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Reason           string `json:"reason,omitempty"`
}

type AgentMailConflict struct {
	Pattern string   `json:"pattern"`
	Holders []string `json:"holders"`
}

type inboxTally struct {
	Total      int
	Unread     int
	Urgent     int
	PendingAck int
}

func getInboxTally(ctx context.Context, client *agentmail.Client, projectKey, agentName string, limit int) inboxTally {
	opts := agentmail.FetchInboxOptions{
		ProjectKey:    projectKey,
		AgentName:     agentName,
		UrgentOnly:    false,
		Limit:         limit,
		IncludeBodies: false,
	}
	msgs, err := client.FetchInbox(ctx, opts)
	if err != nil {
		return inboxTally{}
	}

	tally := inboxTally{Total: len(msgs)}
	for _, msg := range msgs {
		if msg.AckRequired {
			tally.PendingAck++
		}
		if !isUnreadInboxMessage(msg) {
			continue
		}
		tally.Unread++
		if strings.EqualFold(msg.Importance, "urgent") {
			tally.Urgent++
		}
	}
	return tally
}

type ntmPaneInfo struct {
	Key         string
	Label       string
	Type        string
	Index       int
	TmuxIndex   int
	WindowIndex int // tmux window index of the pane (for round-trippable W.P addresses, #172)
	Variant     string
}

func parseNTMPanes(panes []tmux.Pane) map[string][]ntmPaneInfo {
	out := make(map[string][]ntmPaneInfo)

	for _, p := range panes {
		// Use the NTM-specific index parsed by the tmux package
		// This avoids duplicate regex parsing and ensures consistency
		idx := p.NTMIndex
		if idx == 0 && p.Type == "user" {
			// Skip user pane or panes that didn't parse correctly
			// Note: parseAgentFromTitle returns 0 if no match.
			// But for valid NTM panes, index should be > 0 (e.g. cc_1)
			// User pane might be user_0 or just user?
			// Let's check tmux.parseAgentFromTitle logic: matches[2] is the index group.
			// "session__cc_1" -> index 1.
			// "session__user" -> no match for regex which expects _\d+
			// So if NTMIndex is 0, it's not a standard numbered agent pane.
			continue
		}

		// Convert AgentType to string for map key
		typ := string(p.Type)
		key := p.ID
		if key == "" {
			key = fmt.Sprintf("%s:%d:%d", typ, idx, p.Index)
		}

		out[typ] = append(out[typ], ntmPaneInfo{
			Key:         key,
			Label:       fmt.Sprintf("%s_%d", typ, idx),
			Type:        typ,
			Index:       idx,
			TmuxIndex:   p.Index,
			WindowIndex: p.WindowIndex,
			Variant:     p.Variant,
		})
	}

	for typ := range out {
		sort.SliceStable(out[typ], func(i, j int) bool { return out[typ][i].Index < out[typ][j].Index })
	}
	return out
}

func groupAgentsByType(agents []agentmail.Agent) map[string][]agentmail.Agent {
	out := make(map[string][]agentmail.Agent)

	for _, a := range agents {
		if a.Program == "" || a.Program == "ntm" {
			continue
		}
		typ := agentTypeFromProgram(a.Program)
		if typ == "" {
			continue
		}
		out[typ] = append(out[typ], a)
	}

	for typ := range out {
		sort.SliceStable(out[typ], func(i, j int) bool { return out[typ][i].InceptionTS.Before(out[typ][j].InceptionTS.Time) })
	}
	return out
}

func agentTypeFromProgram(program string) string {
	p := strings.ToLower(program)
	switch {
	case strings.Contains(p, "claude"):
		return "cc"
	case strings.Contains(p, "codex"):
		return "cod"
	case strings.Contains(p, "gemini"):
		return "gmi"
	case strings.Contains(p, "cursor"):
		return "cursor"
	case strings.Contains(p, "windsurf"):
		return "windsurf"
	case strings.Contains(p, "aider"):
		return "aider"
	default:
		return p
	}
}

func normalizedProgramType(program string) string {
	switch agentTypeFromProgram(program) {
	case "cc":
		return "claude"
	case "cod":
		return "codex"
	case "gmi":
		return "gemini"
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	case "aider":
		return "aider"
	case "ollama":
		return "ollama"
	case "user":
		return "user"
	default:
		return "unknown"
	}
}

func assignAgentsToPanes(panes []ntmPaneInfo, agents []agentmail.Agent) map[string]string {
	assigned := make(map[string]bool)
	mapping := make(map[string]string)

	// Create a copy of panes to sort without affecting the caller
	sortedPanes := make([]ntmPaneInfo, len(panes))
	copy(sortedPanes, panes)

	// Prioritize panes with variants (more specific requirements)
	sort.SliceStable(sortedPanes, func(i, j int) bool {
		hasVarI := sortedPanes[i].Variant != ""
		hasVarJ := sortedPanes[j].Variant != ""
		if hasVarI && !hasVarJ {
			return true
		}
		if !hasVarI && hasVarJ {
			return false
		}
		return sortedPanes[i].Index < sortedPanes[j].Index
	})

	for _, pane := range sortedPanes {
		bestIdx := -1
		bestScore := -1

		for i, a := range agents {
			if assigned[a.Name] {
				continue
			}
			score := 0
			if pane.Variant != "" {
				v := strings.ToLower(pane.Variant)
				if strings.Contains(strings.ToLower(a.Model), v) {
					score = 2
				} else if strings.Contains(strings.ToLower(a.TaskDescription), v) {
					score = 1
				}
			}
			if bestIdx == -1 || score > bestScore {
				bestIdx = i
				bestScore = score
			}
		}

		if bestIdx == -1 {
			continue
		}
		if bestIdx >= len(agents) {
			continue
		}

		chosen := agents[bestIdx]
		mapping[pane.Key] = chosen.Name
		assigned[chosen.Name] = true
	}

	return mapping
}

func summarizeReservations(reservations []agentmail.FileReservation) []AgentMailReservation {
	now := time.Now()
	out := make([]AgentMailReservation, 0, len(reservations))
	for _, r := range reservations {
		expiresIn := int(r.ExpiresTS.Sub(now).Seconds())
		if expiresIn < 0 {
			expiresIn = 0
		}
		out = append(out, AgentMailReservation{
			ID:               r.ID,
			Pattern:          r.PathPattern,
			Agent:            r.AgentName,
			Exclusive:        r.Exclusive,
			ExpiresInSeconds: expiresIn,
			Reason:           r.Reason,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out
}

func detectReservationConflicts(reservations []agentmail.FileReservation) []AgentMailConflict {
	type conflictState struct {
		holders map[string]bool
	}
	byPattern := make(map[string]*conflictState)
	for i := 0; i < len(reservations); i++ {
		for j := i + 1; j < len(reservations); j++ {
			a := reservations[i]
			b := reservations[j]
			if a.AgentName == b.AgentName {
				continue
			}
			if !a.Exclusive && !b.Exclusive {
				continue
			}
			if !reservationPatternsConflict(a.PathPattern, b.PathPattern) {
				continue
			}

			pattern := normalizeConflictPattern(a.PathPattern, b.PathPattern)
			state := byPattern[pattern]
			if state == nil {
				state = &conflictState{holders: make(map[string]bool)}
				byPattern[pattern] = state
			}
			state.holders[a.AgentName] = true
			state.holders[b.AgentName] = true
		}
	}

	var conflicts []AgentMailConflict
	for pattern, state := range byPattern {
		if len(state.holders) <= 1 {
			continue
		}
		var holders []string
		for name := range state.holders {
			holders = append(holders, name)
		}
		sort.Strings(holders)
		conflicts = append(conflicts, AgentMailConflict{Pattern: pattern, Holders: holders})
	}
	sort.SliceStable(conflicts, func(i, j int) bool { return conflicts[i].Pattern < conflicts[j].Pattern })
	return conflicts
}

func reservationPatternsConflict(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// Matching one glob against the other's literal spelling misses
	// intersections such as src/*/main.go and src/service/*.go. Compare their
	// path languages, as the coordinator does.
	return reservationpath.MayOverlap(a, b)
}

func normalizeConflictPattern(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return a
	}
	if a > b {
		a, b = b, a
	}
	return a + " <-> " + b
}

// fetchAgentMailData retrieves Agent Mail state for the project.
// Returns the summary, raw agent list, and per-agent stats map.
func fetchAgentMailData(projectKey string) (*SnapshotAgentMail, []agentmail.Agent, map[string]SnapshotAgentMailStats) {
	return fetchAgentMailDataContext(context.Background(), projectKey)
}

func fetchAgentMailDataContext(parent context.Context, projectKey string) (*SnapshotAgentMail, []agentmail.Agent, map[string]SnapshotAgentMailStats) {
	if parent == nil {
		parent = context.Background()
	}
	if parent.Err() != nil {
		return nil, nil, nil
	}
	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))

	if !client.IsAvailableContext(parent) {
		return nil, nil, nil
	}

	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	// Ensure project exists
	if _, err := ensureProjectWithRetry(ctx, client, projectKey); err != nil {
		if isAgentMailDBLockError(err) {
			return &SnapshotAgentMail{
				Available: true,
				Reason:    "temporarily unavailable: Agent Mail resource busy",
				Project:   projectKey,
			}, nil, nil
		}
		return &SnapshotAgentMail{
			Available: true,
			Reason:    fmt.Sprintf("ensure_project failed: %v", err),
			Project:   projectKey,
		}, nil, nil
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		if isAgentMailDBLockError(err) {
			return &SnapshotAgentMail{
				Available: true,
				Reason:    "temporarily unavailable: Agent Mail resource busy",
				Project:   projectKey,
			}, nil, nil
		}
		return &SnapshotAgentMail{
			Available: true,
			Reason:    fmt.Sprintf("list_agents failed: %v", err),
			Project:   projectKey,
		}, nil, nil
	}

	summary := &SnapshotAgentMail{
		Available: true,
		Project:   projectKey,
		Agents:    make(map[string]SnapshotAgentMailStats),
	}
	statsMap := make(map[string]SnapshotAgentMailStats)

	threadSet := make(map[string]struct{})
	for _, agent := range agents {
		if agent.Name == "HumanOverseer" {
			continue
		}

		inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    projectKey,
			AgentName:     agent.Name,
			Limit:         25,
			IncludeBodies: false,
		})
		if err != nil {
			continue
		}
		unread := 0
		pendingAck := 0
		for _, msg := range inbox {
			if msg.AckRequired {
				pendingAck++
			}
			if !isUnreadInboxMessage(msg) {
				continue
			}
			unread++
			threadKey := inboxThreadKey(msg)
			threadSet[threadKey] = struct{}{}
		}
		summary.TotalUnread += unread
		stats := SnapshotAgentMailStats{
			Unread:     unread,
			PendingAck: pendingAck,
		}
		summary.Agents[agent.Name] = stats
		statsMap[agent.Name] = stats
	}

	if len(threadSet) > 0 {
		summary.ThreadsKnown = len(threadSet)
	}

	return summary, agents, statsMap
}

// resolveAgentsForSession maps stable pane identifiers to agent names for a specific session.
func resolveAgentsForSession(panes []tmux.Pane, mailAgents []agentmail.Agent) map[string]string {
	if len(mailAgents) == 0 || len(panes) == 0 {
		return nil
	}

	paneInfos := parseNTMPanes(panes)
	agentsByType := groupAgentsByType(mailAgents)
	mapping := make(map[string]string)

	for paneType, info := range paneInfos {
		if agents, ok := agentsByType[paneType]; ok {
			typeMapping := assignAgentsToPanes(info, agents)
			for k, v := range typeMapping {
				mapping[k] = v
			}
		}
	}

	return mapping
}

// ensureProjectWithRetry wraps EnsureProject with a small retry window for
// transient SQLite lock contention on the Agent Mail server.
func ensureProjectWithRetry(ctx context.Context, client *agentmail.Client, projectKey string) (*agentmail.Project, error) {
	const maxAttempts = 4 // Canonical default: config.RetryConfig.DB.MaxAttempts
	backoff := 100 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		project, err := client.EnsureProject(ctx, projectKey)
		if err == nil {
			return project, nil
		}
		lastErr = err

		if !isAgentMailDBLockError(err) || attempt == maxAttempts {
			return nil, err
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}

		if backoff < time.Second {
			backoff *= 2
		}
	}

	return nil, lastErr
}

func isAgentMailDBLockError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database is busy") ||
		strings.Contains(msg, "resource busy")
}

func isUnreadInboxMessage(msg agentmail.InboxMessage) bool {
	return msg.ReadAt == nil
}

func inboxThreadKey(msg agentmail.InboxMessage) string {
	if msg.ThreadID != nil {
		if threadID := strings.TrimSpace(*msg.ThreadID); threadID != "" {
			return threadID
		}
	}
	return fmt.Sprintf("%d", msg.ID)
}
