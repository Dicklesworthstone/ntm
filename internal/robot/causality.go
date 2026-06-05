package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

var causalityBeadPattern = regexp.MustCompile(`\b(?:bd|br)-[A-Za-z0-9][A-Za-z0-9.-]*\b`)

const maxCausalityPipelineStateBytes int64 = 16 * 1024 * 1024

// CausalityOptions configures --robot-causality.
type CausalityOptions struct {
	Session   string
	Project   string
	AgentName string

	BeadID string
	Pane   string
	Type   string
	Chain  string

	Since string
	Until string
	Limit int
}

// CausalityQuery captures normalized query inputs in the response.
type CausalityQuery struct {
	Session   string `json:"session,omitempty"`
	Project   string `json:"project,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	BeadID    string `json:"bead_id,omitempty"`
	Pane      string `json:"pane,omitempty"`
	Type      string `json:"type,omitempty"`
	Chain     string `json:"chain,omitempty"`
	Since     string `json:"since,omitempty"`
	Until     string `json:"until,omitempty"`
	Limit     int    `json:"limit"`
}

// CausalityEvent is a normalized event from one of several coordination sources.
type CausalityEvent struct {
	ID        string                 `json:"id"`
	Source    string                 `json:"source"`
	Type      string                 `json:"type"`
	Timestamp string                 `json:"timestamp"`
	Session   string                 `json:"session,omitempty"`
	Pane      string                 `json:"pane,omitempty"`
	Agent     string                 `json:"agent,omitempty"`
	BeadID    string                 `json:"bead_id,omitempty"`
	ChainID   string                 `json:"chain_id,omitempty"`
	RunID     string                 `json:"run_id,omitempty"`
	Summary   string                 `json:"summary,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`

	ts time.Time
}

// CausalitySourceStatus reports whether each source was available.
type CausalitySourceStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Events    int    `json:"events"`
	Error     string `json:"error,omitempty"`
}

// CausalityOutput is the structured response for --robot-causality.
type CausalityOutput struct {
	RobotResponse
	Query     CausalityQuery          `json:"query"`
	Events    []CausalityEvent        `json:"events"`
	Sources   []CausalitySourceStatus `json:"sources"`
	Total     int                     `json:"total"`
	Available int                     `json:"available"`
	Filtered  int                     `json:"filtered"`
	Truncated bool                    `json:"truncated,omitempty"`
	Warnings  []string                `json:"warnings,omitempty"`
}

type causalityLoaders struct {
	audit    func(CausalityOptions, *time.Time, *time.Time) ([]CausalityEvent, error)
	mail     func(CausalityOptions, *time.Time, *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string)
	session  func(CausalityOptions, *time.Time, *time.Time) ([]CausalityEvent, error)
	pipeline func(CausalityOptions, *time.Time, *time.Time) ([]CausalityEvent, []string, error)
}

func defaultCausalityLoaders() causalityLoaders {
	return causalityLoaders{
		audit:    loadAuditCausalityEvents,
		mail:     loadAgentMailCausalityEvents,
		session:  loadSessionTimelineCausalityEvents,
		pipeline: loadPipelineCausalityEvents,
	}
}

// PrintCausality emits merged, replayable causality events as JSON.
func PrintCausality(opts CausalityOptions) error {
	return printCausality(opts, defaultCausalityLoaders())
}

func printCausality(opts CausalityOptions, loaders causalityLoaders) error {
	output := buildCausalityOutput(opts, loaders)
	return encodeJSON(output)
}

func buildCausalityOutput(opts CausalityOptions, loaders causalityLoaders) CausalityOutput {
	if loaders.audit == nil || loaders.mail == nil || loaders.session == nil || loaders.pipeline == nil {
		return CausalityOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("causality loaders are not initialized"),
				ErrCodeInternalError,
				"Re-run with a complete binary build",
			),
			Events:  []CausalityEvent{},
			Sources: []CausalitySourceStatus{},
		}
	}

	if strings.TrimSpace(opts.Session) == "" {
		return CausalityOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session is required"),
				ErrCodeInvalidFlag,
				"Provide a session name: ntm --robot-causality=myproject",
			),
			Events:  []CausalityEvent{},
			Sources: []CausalitySourceStatus{},
		}
	}

	if strings.TrimSpace(opts.Project) == "" {
		if cwd, err := os.Getwd(); err == nil {
			opts.Project = cwd
		}
	}

	since, until, parseErr := parseCausalityWindow(opts.Since, opts.Until)
	if parseErr != nil {
		return CausalityOutput{
			RobotResponse: NewErrorResponse(
				parseErr,
				ErrCodeInvalidFlag,
				"Use --causality-since/--causality-until with duration (1h) or RFC3339",
			),
			Events:  []CausalityEvent{},
			Sources: []CausalitySourceStatus{},
		}
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}

	output := CausalityOutput{
		RobotResponse: NewRobotResponse(true),
		Query: CausalityQuery{
			Session:   opts.Session,
			Project:   opts.Project,
			AgentName: opts.AgentName,
			BeadID:    strings.TrimSpace(opts.BeadID),
			Pane:      strings.TrimSpace(opts.Pane),
			Type:      strings.TrimSpace(opts.Type),
			Chain:     strings.TrimSpace(opts.Chain),
			Limit:     limit,
		},
		Events:  []CausalityEvent{},
		Sources: []CausalitySourceStatus{},
	}
	if since != nil {
		output.Query.Since = since.UTC().Format(time.RFC3339)
	}
	if until != nil {
		output.Query.Until = until.UTC().Format(time.RFC3339)
	}

	all := make([]CausalityEvent, 0, 512)

	auditEvents, err := loaders.audit(opts, since, until)
	if err != nil {
		output.Sources = append(output.Sources, CausalitySourceStatus{Name: "robot_audit", Available: false, Error: err.Error()})
		output.Warnings = append(output.Warnings, "robot_audit unavailable: "+err.Error())
	} else {
		all = append(all, auditEvents...)
		output.Sources = append(output.Sources, CausalitySourceStatus{Name: "robot_audit", Available: true, Events: len(auditEvents)})
	}

	mailEvents, mailStatuses, mailWarnings := loaders.mail(opts, since, until)
	all = append(all, mailEvents...)
	output.Sources = append(output.Sources, mailStatuses...)
	output.Warnings = append(output.Warnings, mailWarnings...)

	sessionEvents, err := loaders.session(opts, since, until)
	if err != nil {
		output.Sources = append(output.Sources, CausalitySourceStatus{Name: "session_timeline", Available: false, Error: err.Error()})
		output.Warnings = append(output.Warnings, "session_timeline unavailable: "+err.Error())
	} else {
		all = append(all, sessionEvents...)
		output.Sources = append(output.Sources, CausalitySourceStatus{Name: "session_timeline", Available: true, Events: len(sessionEvents)})
	}

	pipelineEvents, pipelineWarnings, err := loaders.pipeline(opts, since, until)
	if err != nil {
		output.Sources = append(output.Sources, CausalitySourceStatus{Name: "pipeline_state", Available: false, Error: err.Error()})
		output.Warnings = append(output.Warnings, "pipeline_state unavailable: "+err.Error())
	} else {
		all = append(all, pipelineEvents...)
		output.Sources = append(output.Sources, CausalitySourceStatus{Name: "pipeline_state", Available: true, Events: len(pipelineEvents)})
		output.Warnings = append(output.Warnings, pipelineWarnings...)
	}

	all = dedupeCausalityEvents(all)
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].ts.Equal(all[j].ts) {
			return all[i].ts.Before(all[j].ts)
		}
		if all[i].Source != all[j].Source {
			return all[i].Source < all[j].Source
		}
		return all[i].ID < all[j].ID
	})

	output.Total = len(all)

	filtered := filterCausalityEvents(all, opts, since, until)
	output.Available = len(filtered)
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	output.Filtered = len(filtered)
	output.Truncated = output.Available > output.Filtered
	output.Events = filtered

	for i := range output.Events {
		if output.Events[i].Timestamp == "" && !output.Events[i].ts.IsZero() {
			output.Events[i].Timestamp = output.Events[i].ts.UTC().Format(time.RFC3339Nano)
		}
	}

	return output
}

func parseCausalityWindow(sinceRaw, untilRaw string) (*time.Time, *time.Time, error) {
	sinceRaw = strings.TrimSpace(sinceRaw)
	untilRaw = strings.TrimSpace(untilRaw)
	var since *time.Time
	var until *time.Time

	if sinceRaw != "" {
		parsed, err := parseSinceTime(sinceRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid since: %w", err)
		}
		t := parsed.UTC()
		since = &t
	}
	if untilRaw != "" {
		parsed, err := parseSinceTime(untilRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid until: %w", err)
		}
		t := parsed.UTC()
		until = &t
	}
	if since != nil && until != nil && since.After(*until) {
		return nil, nil, fmt.Errorf("since must be <= until")
	}

	return since, until, nil
}

func filterCausalityEvents(events []CausalityEvent, opts CausalityOptions, since, until *time.Time) []CausalityEvent {
	beadFilter := strings.TrimSpace(opts.BeadID)
	paneFilter := strings.TrimSpace(opts.Pane)
	typeFilter := strings.TrimSpace(opts.Type)
	chainFilter := strings.TrimSpace(opts.Chain)
	sessionFilter := strings.TrimSpace(opts.Session)

	out := make([]CausalityEvent, 0, len(events))
	for _, ev := range events {
		if since != nil && ev.ts.Before(*since) {
			continue
		}
		if until != nil && ev.ts.After(*until) {
			continue
		}
		if sessionFilter != "" {
			if strings.TrimSpace(ev.Session) != "" && ev.Session != sessionFilter {
				continue
			}
		}
		if beadFilter != "" && !strings.EqualFold(ev.BeadID, beadFilter) {
			continue
		}
		if paneFilter != "" && ev.Pane != paneFilter {
			continue
		}
		if typeFilter != "" && !strings.EqualFold(ev.Type, typeFilter) {
			continue
		}
		if chainFilter != "" && !strings.EqualFold(ev.ChainID, chainFilter) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func dedupeCausalityEvents(events []CausalityEvent) []CausalityEvent {
	seen := make(map[string]struct{}, len(events))
	out := make([]CausalityEvent, 0, len(events))
	for _, ev := range events {
		key := strings.Join([]string{
			ev.Source,
			ev.ID,
			ev.Type,
			ev.ts.UTC().Format(time.RFC3339Nano),
			ev.Session,
			ev.Pane,
			ev.BeadID,
			ev.ChainID,
		}, "|")
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ev)
	}
	return out
}

func loadAuditCausalityEvents(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
	searcher, err := audit.NewSearcher()
	if err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}

	q := audit.Query{
		Sessions: []string{opts.Session},
		Limit:    limit,
		Timeout:  4 * time.Second,
	}
	if since != nil {
		q.Since = since
	}
	if until != nil {
		q.Until = until
	}

	result, err := searcher.Search(q)
	if err != nil {
		return nil, err
	}

	events := make([]CausalityEvent, 0, len(result.Entries))
	for _, entry := range result.Entries {
		pane := causalityFirstNonEmpty(
			valueString(entry.Payload, "pane"),
			valueString(entry.Payload, "pane_id"),
			valueString(entry.Payload, "pane_index"),
		)
		if pane == "" {
			pane = valueString(entry.Metadata, "pane")
		}
		chain := causalityFirstNonEmpty(
			valueString(entry.Payload, "correlation_id"),
			valueString(entry.Metadata, "correlation_id"),
			valueString(entry.Payload, "run_id"),
		)
		runID := causalityFirstNonEmpty(valueString(entry.Payload, "run_id"), valueString(entry.Metadata, "run_id"))
		bead := findBeadID(
			valueString(entry.Payload, "bead_id"),
			valueString(entry.Metadata, "bead_id"),
			valueString(entry.Payload, "reason"),
			entry.Target,
		)

		summary := fmt.Sprintf("%s %s", entry.EventType, entry.Target)
		events = append(events, CausalityEvent{
			ID:        fmt.Sprintf("audit:%s:%d", entry.SessionID, entry.SequenceNum),
			Source:    "robot_audit",
			Type:      string(entry.EventType),
			Timestamp: entry.Timestamp.UTC().Format(time.RFC3339Nano),
			Session:   entry.SessionID,
			Pane:      pane,
			Agent:     string(entry.Actor),
			BeadID:    bead,
			ChainID:   chain,
			RunID:     runID,
			Summary:   strings.TrimSpace(summary),
			Details: map[string]interface{}{
				"target": entry.Target,
				"actor":  entry.Actor,
			},
			ts: entry.Timestamp.UTC(),
		})
	}

	return events, nil
}

func loadAgentMailCausalityEvents(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []CausalitySourceStatus, []string) {
	statuses := make([]CausalitySourceStatus, 0, 2)
	warnings := make([]string, 0, 2)
	events := make([]CausalityEvent, 0, 128)

	project := strings.TrimSpace(opts.Project)
	if project == "" {
		statuses = append(statuses,
			CausalitySourceStatus{Name: "agentmail_reservations", Available: false, Error: "project path not set"},
			CausalitySourceStatus{Name: "agentmail_inbox", Available: false, Error: "project path not set"},
		)
		warnings = append(warnings, "agentmail unavailable: project path not set")
		return events, statuses, warnings
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(project))
	reservations, err := client.ListReservations(ctx, project, "", true)
	if err != nil {
		statuses = append(statuses, CausalitySourceStatus{Name: "agentmail_reservations", Available: false, Error: err.Error()})
		warnings = append(warnings, "agentmail_reservations unavailable: "+err.Error())
	} else {
		kept := 0
		for _, res := range reservations {
			ts := res.CreatedTS.UTC()
			if !withinCausalityWindow(ts, since, until) {
				continue
			}
			kept++
			reason := strings.TrimSpace(res.Reason)
			bead := findBeadID(reason, res.PathPattern)
			events = append(events, CausalityEvent{
				ID:        fmt.Sprintf("am-res:%d", res.ID),
				Source:    "agentmail_reservations",
				Type:      "reservation_active",
				Timestamp: ts.Format(time.RFC3339Nano),
				Agent:     res.AgentName,
				BeadID:    bead,
				Summary:   fmt.Sprintf("%s reserved %s", res.AgentName, res.PathPattern),
				Details: map[string]interface{}{
					"path_pattern": res.PathPattern,
					"exclusive":    res.Exclusive,
					"reason":       reason,
					"expires_at":   res.ExpiresTS.Time.UTC().Format(time.RFC3339Nano),
				},
				ts: ts,
			})
		}
		statuses = append(statuses, CausalitySourceStatus{Name: "agentmail_reservations", Available: true, Events: kept})
	}

	agentName := strings.TrimSpace(opts.AgentName)
	if agentName == "" {
		statuses = append(statuses, CausalitySourceStatus{Name: "agentmail_inbox", Available: false, Error: "agent name not provided"})
		warnings = append(warnings, "agentmail_inbox skipped: pass --causality-agent=AGENT_NAME")
		return events, statuses, warnings
	}

	inboxLimit := opts.Limit
	if inboxLimit <= 0 {
		inboxLimit = 200
	}
	if inboxLimit > 2000 {
		inboxLimit = 2000
	}
	inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey:    project,
		AgentName:     agentName,
		Limit:         inboxLimit,
		IncludeBodies: false,
	})
	if err != nil {
		statuses = append(statuses, CausalitySourceStatus{Name: "agentmail_inbox", Available: false, Error: err.Error()})
		warnings = append(warnings, "agentmail_inbox unavailable: "+err.Error())
		return events, statuses, warnings
	}

	kept := 0
	for _, msg := range inbox {
		ts := msg.CreatedTS.UTC()
		if !withinCausalityWindow(ts, since, until) {
			continue
		}
		kept++
		thread := ""
		if msg.ThreadID != nil {
			thread = strings.TrimSpace(*msg.ThreadID)
		}
		bead := findBeadID(thread, msg.Subject, msg.BodyMD)
		events = append(events, CausalityEvent{
			ID:        fmt.Sprintf("am-msg:%d", msg.ID),
			Source:    "agentmail_inbox",
			Type:      "message",
			Timestamp: ts.Format(time.RFC3339Nano),
			Agent:     msg.From,
			BeadID:    bead,
			ChainID:   thread,
			Summary:   msg.Subject,
			Details: map[string]interface{}{
				"importance":   msg.Importance,
				"ack_required": msg.AckRequired,
				"kind":         msg.Kind,
			},
			ts: ts,
		})
	}
	statuses = append(statuses, CausalitySourceStatus{Name: "agentmail_inbox", Available: true, Events: kept})

	return events, statuses, warnings
}

func loadSessionTimelineCausalityEvents(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, error) {
	persister, err := state.GetDefaultTimelinePersister()
	if err != nil {
		return nil, err
	}

	events, err := persister.LoadTimeline(opts.Session)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return []CausalityEvent{}, nil
	}

	out := make([]CausalityEvent, 0, len(events))
	for i, ev := range events {
		ts := ev.Timestamp.UTC()
		if !withinCausalityWindow(ts, since, until) {
			continue
		}
		pane := causalityFirstNonEmpty(valueStringFromStringMap(ev.Details, "pane"), valueStringFromStringMap(ev.Details, "pane_id"), valueStringFromStringMap(ev.Details, "pane_index"))
		bead := findBeadID(valueStringFromStringMap(ev.Details, "bead_id"), ev.Trigger, valueStringFromStringMap(ev.Details, "reason"))
		chain := causalityFirstNonEmpty(valueStringFromStringMap(ev.Details, "correlation_id"), valueStringFromStringMap(ev.Details, "run_id"))
		runID := valueStringFromStringMap(ev.Details, "run_id")

		summary := fmt.Sprintf("%s -> %s", ev.AgentID, ev.State)
		out = append(out, CausalityEvent{
			ID:        fmt.Sprintf("session:%s:%d", opts.Session, i),
			Source:    "session_timeline",
			Type:      string(ev.State),
			Timestamp: ts.Format(time.RFC3339Nano),
			Session:   causalityFirstNonEmpty(ev.SessionID, opts.Session),
			Pane:      pane,
			Agent:     ev.AgentID,
			BeadID:    bead,
			ChainID:   chain,
			RunID:     runID,
			Summary:   summary,
			Details: map[string]interface{}{
				"agent_type": ev.AgentType,
				"trigger":    ev.Trigger,
			},
			ts: ts,
		})
	}

	return out, nil
}

func loadPipelineCausalityEvents(opts CausalityOptions, since, until *time.Time) ([]CausalityEvent, []string, error) {
	project := strings.TrimSpace(opts.Project)
	if project == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		project = cwd
	}

	dir := filepath.Join(project, ".ntm", "pipelines")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CausalityEvent{}, nil, nil
		}
		return nil, nil, err
	}

	out := make([]CausalityEvent, 0, len(entries)*3)
	var warnings []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		runID := strings.TrimSuffix(entry.Name(), ".json")
		if runID == "" {
			continue
		}

		st, err := readPipelineStateFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			// Surface the skip so operators don't silently lose runs
			// (especially the bd-w3ft1 16MB cap or corrupt-JSON cases).
			warnings = append(warnings, fmt.Sprintf("pipeline_state %s skipped: %s", entry.Name(), err.Error()))
			continue
		}
		if opts.Session != "" && st.Session != "" && st.Session != opts.Session {
			continue
		}
		// Skip the entire run when none of its timestamps fall in the
		// caller's window — saves us from emitting events the
		// post-merge filter would discard anyway.
		if !pipelineRunIntersectsWindow(st, since, until) {
			continue
		}

		bead := findBeadID(valueString(st.Variables, "bead_id"), valueString(st.Variables, "bead"), valueString(st.Variables, "thread_id"))
		sessionName := causalityFirstNonEmpty(st.Session, opts.Session)

		if !st.StartedAt.IsZero() && withinCausalityWindow(st.StartedAt.UTC(), since, until) {
			out = append(out, CausalityEvent{
				ID:        fmt.Sprintf("pipeline:%s:start", runID),
				Source:    "pipeline_state",
				Type:      "pipeline_started",
				Timestamp: st.StartedAt.UTC().Format(time.RFC3339Nano),
				Session:   sessionName,
				BeadID:    bead,
				ChainID:   runID,
				RunID:     runID,
				Summary:   fmt.Sprintf("pipeline run started (%s)", st.WorkflowID),
				Details: map[string]interface{}{
					"workflow_id": st.WorkflowID,
					"status":      st.Status,
				},
				ts: st.StartedAt.UTC(),
			})
		}

		if !st.UpdatedAt.IsZero() && !st.UpdatedAt.Equal(st.StartedAt) && withinCausalityWindow(st.UpdatedAt.UTC(), since, until) {
			out = append(out, CausalityEvent{
				ID:        fmt.Sprintf("pipeline:%s:update", runID),
				Source:    "pipeline_state",
				Type:      "pipeline_updated",
				Timestamp: st.UpdatedAt.UTC().Format(time.RFC3339Nano),
				Session:   sessionName,
				BeadID:    bead,
				ChainID:   runID,
				RunID:     runID,
				Summary:   fmt.Sprintf("pipeline run updated (%s)", st.Status),
				Details: map[string]interface{}{
					"workflow_id": st.WorkflowID,
					"status":      st.Status,
				},
				ts: st.UpdatedAt.UTC(),
			})
		}

		endAt, endType := pipelineRunEnd(st)
		if !endAt.IsZero() && withinCausalityWindow(endAt, since, until) {
			out = append(out, CausalityEvent{
				ID:        fmt.Sprintf("pipeline:%s:end", runID),
				Source:    "pipeline_state",
				Type:      endType,
				Timestamp: endAt.UTC().Format(time.RFC3339Nano),
				Session:   sessionName,
				BeadID:    bead,
				ChainID:   runID,
				RunID:     runID,
				Summary:   fmt.Sprintf("pipeline run %s", st.Status),
				Details: map[string]interface{}{
					"workflow_id": st.WorkflowID,
					"status":      st.Status,
				},
				ts: endAt.UTC(),
			})
		}
	}

	return out, warnings, nil
}

type causalityPipelineState struct {
	RunID       string                 `json:"run_id"`
	WorkflowID  string                 `json:"workflow_id"`
	Session     string                 `json:"session"`
	Status      string                 `json:"status"`
	StartedAt   time.Time              `json:"started_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	FinishedAt  time.Time              `json:"finished_at"`
	CancelledAt *time.Time             `json:"cancelled_at,omitempty"`
	Variables   map[string]interface{} `json:"variables,omitempty"`
}

func readPipelineStateFile(path string) (*causalityPipelineState, error) {
	return readPipelineStateFileWithLimit(path, maxCausalityPipelineStateBytes)
}

func readPipelineStateFileWithLimit(path string, maxBytes int64) (*causalityPipelineState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var data []byte
	if maxBytes > 0 {
		// Read at most maxBytes+1 from the open FD so we can detect overflow
		// without trusting a separate Stat() call. Bounds the actual read,
		// so a file growing or a symlink swap between stat-and-read can't
		// bypass the cap.
		data, err = io.ReadAll(io.LimitReader(f, maxBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("pipeline state file exceeds limit: %s (> %d bytes)", path, maxBytes)
		}
	} else {
		data, err = io.ReadAll(f)
		if err != nil {
			return nil, err
		}
	}

	var st causalityPipelineState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func pipelineRunEnd(st *causalityPipelineState) (time.Time, string) {
	if st == nil {
		return time.Time{}, ""
	}
	if st.CancelledAt != nil && !st.CancelledAt.IsZero() {
		return st.CancelledAt.UTC(), "pipeline_cancelled"
	}
	if !st.FinishedAt.IsZero() {
		if strings.EqualFold(string(st.Status), "failed") {
			return st.FinishedAt.UTC(), "pipeline_failed"
		}
		return st.FinishedAt.UTC(), "pipeline_finished"
	}
	return time.Time{}, ""
}

// withinCausalityWindow returns true when ts falls inside [since, until].
// Either bound may be nil to mean "open ended". A zero ts (which the
// underlying source rendered as missing) is treated as in-window so we
// do not silently drop events that lack a timestamp; downstream
// filtering can still reject them by other criteria.
func withinCausalityWindow(ts time.Time, since, until *time.Time) bool {
	if ts.IsZero() {
		return true
	}
	if since != nil && ts.Before(*since) {
		return false
	}
	if until != nil && ts.After(*until) {
		return false
	}
	return true
}

// pipelineRunIntersectsWindow returns true when any of the run's
// recorded timestamps (started/updated/finished/cancelled) lies inside
// the caller's window. Used by loadPipelineCausalityEvents to skip
// reading an entire run early when none of its events would survive.
func pipelineRunIntersectsWindow(st *causalityPipelineState, since, until *time.Time) bool {
	if st == nil {
		return false
	}
	if since == nil && until == nil {
		return true
	}
	candidates := []time.Time{st.StartedAt, st.UpdatedAt, st.FinishedAt}
	if st.CancelledAt != nil {
		candidates = append(candidates, *st.CancelledAt)
	}
	for _, t := range candidates {
		if t.IsZero() {
			continue
		}
		if withinCausalityWindow(t.UTC(), since, until) {
			return true
		}
	}
	return false
}

func valueString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", x))
	}
}

func causalityFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func valueStringFromStringMap(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[key])
}

func findBeadID(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		match := causalityBeadPattern.FindString(value)
		if match != "" {
			return match
		}
	}
	return ""
}
