package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// =============================================================================
// Agents API Handlers
// =============================================================================

// handleListAgentsV1 handles GET /api/v1/sessions/{sessionId}/agents.
func (s *Server) handleListAgentsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	roster, err := s.listSessionAgents(r.Context(), sessionID)
	if err != nil {
		status, code, message := sessionAgentsFailure(err)
		writeErrorResponse(w, status, code, message, nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, roster.payload(sessionID), reqID)
}

// sessionAgentRoster is what the agents endpoints report for one session.
type sessionAgentRoster struct {
	agents   []map[string]interface{}
	warnings []string
}

// payload is the roster's response body, without the envelope fields.
func (roster sessionAgentRoster) payload(sessionID string) map[string]interface{} {
	agents := roster.agents
	if agents == nil {
		agents = []map[string]interface{}{}
	}
	body := map[string]interface{}{
		"session_id": sessionID,
		"agents":     agents,
		"count":      len(agents),
	}
	if len(roster.warnings) > 0 {
		body["warnings"] = roster.warnings
	}
	return body
}

var (
	// errSessionAgentsNotFound reports a session that tmux is not running and
	// the state store does not record.
	errSessionAgentsNotFound = errors.New("session not found")
	// errSessionAgentsLiveListing wraps a failed live pane listing that no
	// projection row could stand in for.
	errSessionAgentsLiveListing = errors.New("listing live tmux panes failed")
)

// sessionAgentsFailure maps a listSessionAgents error to an HTTP status, an
// error code and a message.
func sessionAgentsFailure(err error) (int, string, string) {
	switch {
	case errors.Is(err, errSessionAgentsNotFound):
		return http.StatusNotFound, ErrCodeNotFound, "session not found"
	case errors.Is(err, errSessionAgentsLiveListing) && errors.Is(err, context.DeadlineExceeded):
		// The bounded tmux listing ran out of time: tmux, not the server,
		// failed.
		return http.StatusGatewayTimeout, ErrCodeTimeout, err.Error()
	case errors.Is(err, errSessionAgentsLiveListing):
		return http.StatusServiceUnavailable, ErrCodeServiceUnavail, err.Error()
	default:
		return http.StatusInternalServerError, ErrCodeInternalError, err.Error()
	}
}

// listSessionAgents reports the agents of one session for
// GET /api/v1/sessions/{id}/agents and the legacy /api/sessions/{id}/agents.
// The legacy `agents` table those endpoints used to read (Store.ListAgents)
// has no production writer, so it only ever answered "no agents".
//
// Live tmux panes are the roster: they say which agents exist right now. The
// runtime projection that `ntm serve` refreshes every few seconds
// (robot.RefreshNormalizedProjection) adds what a pane listing cannot observe:
// the agent's state, when it last produced output, its health and its current
// bead. A live pane with no fresh projection row (right after start, or with
// no state store) is still reported, just without that state.
//
// When tmux answers that the session does not exist, the session has no
// agents, whatever an unexpired projection row still says: a session the
// state store records is reported empty, any other is
// errSessionAgentsNotFound. When tmux fails in any other way (a wedged server
// past the bound, an open circuit breaker), the fresh projection rows are
// reported on their own with a warning; with none, the failure is returned.
func (s *Server) listSessionAgents(ctx context.Context, sessionID string) (sessionAgentRoster, error) {
	var roster sessionAgentRoster
	var projected []state.RuntimeAgent
	if s.stateStore != nil {
		rows, err := s.stateStore.GetRuntimeAgentsBySession(sessionID)
		if err != nil {
			slog.Warn("reading runtime agent projection failed", "session", sessionID, "error", err)
			roster.warnings = append(roster.warnings, "runtime agent projection unavailable: "+err.Error())
		} else {
			projected = rows
		}
	}

	paneCtx, cancel := context.WithTimeout(ctx, s.liveTmuxTimeout())
	panes, err := tmux.GetPanesContext(paneCtx, sessionID)
	cancel()
	if err == nil {
		roster.agents = s.liveAgentRecords(sessionID, panes, projected)
		return roster, nil
	}

	switch tmux.ClassifyCommandError(err).Kind {
	case tmux.CommandErrorSessionNotFound, tmux.CommandErrorNoServer:
		if s.stateStore != nil {
			stored, storeErr := s.stateStore.GetSession(sessionID)
			if storeErr != nil {
				return sessionAgentRoster{}, storeErr
			}
			if stored != nil {
				roster.agents = []map[string]interface{}{}
				return roster, nil
			}
		}
		return sessionAgentRoster{}, errSessionAgentsNotFound
	}

	agents := projectedAgentRecords(sessionID, projected)
	if len(agents) == 0 {
		return sessionAgentRoster{}, fmt.Errorf("%w: %w", errSessionAgentsLiveListing, err)
	}
	slog.Warn("listing live tmux panes failed; reporting projected agents", "session", sessionID, "error", err)
	roster.agents = agents
	roster.warnings = append(roster.warnings, fmt.Sprintf("%v (%v); agents are reported from the runtime projection", errSessionAgentsLiveListing, err))
	return roster, nil
}

// liveAgentRecords turns the agent panes of a running session into agent
// records, adding each pane's fresh projection row when there is one.
func (s *Server) liveAgentRecords(sessionID string, panes []tmux.Pane, projected []state.RuntimeAgent) []map[string]interface{} {
	byPane := make(map[string]state.RuntimeAgent, len(projected))
	for _, row := range projected {
		byPane[row.Pane] = row
	}

	// Agent Mail names ("GreenLake") are recorded per pane. Only trust a
	// mapping whose recorded pane PID still matches: tmux reuses pane IDs
	// after a server restart, and a stale registry must not rename a new pane.
	registry, err := agentmail.LoadBestSessionAgentRegistry(sessionID, s.projectDirSnapshot())
	if err != nil {
		slog.Debug("loading agent registry failed", "session", sessionID, "error", err)
		registry = nil
	}

	agents := make([]map[string]interface{}, 0, len(panes))
	for _, p := range panes {
		// User shells, unrecognized panes and tagged service panes (cm,
		// cass, ...) are not agents; the projection leaves them out too.
		agentType := servedAgentType(string(p.Type))
		if agentType == "" || p.IsServicePane() {
			continue
		}
		name := p.Title
		agentMailName := ""
		if registry != nil && p.PID != 0 && registry.PanePID(p.ID) == p.PID {
			if registered, ok := registry.GetAgentByID(p.ID); ok && registered != "" {
				agentMailName = registered
				name = registered
			}
		}
		agent := map[string]interface{}{
			// Agent-record fields (id, session_id, name, type, tmux_pane_id,
			// status, last_seen, current_task_id) that clients such as the
			// web dashboard's Agents page render.
			"id":           p.ID,
			"session_id":   sessionID,
			"name":         name,
			"type":         agentType,
			"tmux_pane_id": p.ID,
			// Pane-specific fields.
			"pane_index": p.Index,
			"pane_id":    p.ID,
			"agent_type": agentType,
			"title":      p.Title,
			"variant":    p.Variant,
			"tags":       p.Tags,
			"active":     p.Active,
			"dead":       p.Dead,
		}
		if agentMailName != "" {
			agent["agent_mail_name"] = agentMailName
		}
		if row, ok := byPane[p.ID]; ok {
			addProjectedAgentState(agent, row)
		}
		if p.Dead {
			// tmux reports the pane's process has exited, which outranks
			// whatever state the projection last classified.
			agent["status"] = "dead"
		}
		agents = append(agents, agent)
	}
	return agents
}

// projectedAgentRecords turns fresh projection rows into agent records, for
// when tmux cannot be listed. The projection records no pane index or title.
// Its Agent Mail name is reported as is: without a live pane PID there is
// nothing to verify it against, and it is the name --robot-status reports.
func projectedAgentRecords(sessionID string, rows []state.RuntimeAgent) []map[string]interface{} {
	agents := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		agentType := servedAgentType(row.AgentType)
		if agentType == "" || row.Pane == "" {
			continue
		}
		name := row.Pane
		if row.AgentMailName != "" {
			name = row.AgentMailName
		}
		agent := map[string]interface{}{
			"id":           row.Pane,
			"session_id":   sessionID,
			"name":         name,
			"type":         agentType,
			"tmux_pane_id": row.Pane,
			"pane_id":      row.Pane,
			"agent_type":   agentType,
		}
		if row.Variant != "" {
			agent["variant"] = row.Variant
		}
		if row.AgentMailName != "" {
			agent["agent_mail_name"] = row.AgentMailName
		}
		addProjectedAgentState(agent, row)
		agents = append(agents, agent)
	}
	return agents
}

// addProjectedAgentState copies what the runtime projection observed about an
// agent into its record: its state (idle, active, busy, error, compacting or
// unknown, the --robot-status vocabulary), when it last produced output,
// health, current bead, unread mail, and when the projection collected it.
func addProjectedAgentState(agent map[string]interface{}, row state.RuntimeAgent) {
	if row.State != "" {
		agent["status"] = string(row.State)
	}
	if row.StateReason != "" {
		agent["state_reason"] = row.StateReason
	}
	if row.LastOutputAt != nil && !row.LastOutputAt.IsZero() {
		agent["last_seen"] = row.LastOutputAt.UTC()
	}
	if row.HealthStatus != "" {
		agent["health_status"] = string(row.HealthStatus)
	}
	if row.HealthReason != "" {
		agent["health_reason"] = row.HealthReason
	}
	if row.CurrentBead != "" {
		agent["current_task_id"] = row.CurrentBead
	}
	if row.PendingMail > 0 {
		agent["pending_mail"] = row.PendingMail
	}
	if !row.CollectedAt.IsZero() {
		agent["projected_at"] = row.CollectedAt.UTC()
	}
}

// servedAgentType returns the short agent type ("cc", "cod", ...) the agents
// endpoints report, or "" for a pane that is not an agent. Live panes carry
// the short form already; projection rows carry the robot form ("claude",
// "codex").
func servedAgentType(raw string) string {
	switch canonical := tmux.AgentType(raw).Canonical(); canonical {
	case "", tmux.AgentUser, tmux.AgentUnknown:
		return ""
	default:
		return string(canonical)
	}
}

// AgentSpawnRequest is the request body for POST /sessions/{id}/agents/spawn.
type AgentSpawnRequest struct {
	CCCount   int    `json:"cc_count,omitempty"`
	CodCount  int    `json:"cod_count,omitempty"`
	GmiCount  int    `json:"gmi_count,omitempty"`
	AgyCount  int    `json:"agy_count,omitempty"`
	GrokCount int    `json:"grok_count,omitempty"`
	OmpCount  int    `json:"omp_count,omitempty"`
	OcCount   int    `json:"oc_count,omitempty"`
	Preset    string `json:"preset,omitempty"`
	WaitReady bool   `json:"wait_ready,omitempty"`
	Label     string `json:"label,omitempty"` // Goal label for multi-session support
}

// hasAgentCountOrPreset reports whether the request names at least one agent
// to spawn (by count for any supported type, or through a preset).
func (req AgentSpawnRequest) hasAgentCountOrPreset() bool {
	return req.CCCount != 0 || req.CodCount != 0 || req.GmiCount != 0 || req.AgyCount != 0 ||
		req.GrokCount != 0 || req.OmpCount != 0 || req.OcCount != 0 || req.Preset != ""
}

// agentSpawnCountRequiredMessage is the shared validation error for a spawn
// request that names no agents.
const agentSpawnCountRequiredMessage = "at least one agent count (cc_count, cod_count, gmi_count, agy_count, grok_count, omp_count, oc_count) or preset required"

// handleAgentSpawnV1 handles POST /api/v1/sessions/{sessionId}/agents/spawn.
func (s *Server) handleAgentSpawnV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// At least one agent count or preset must be specified
	if !req.hasAgentCountOrPreset() {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, agentSpawnCountRequiredMessage, nil, reqID)
		return
	}

	opts := robot.SpawnOptions{
		Session:   sessionID,
		Label:     req.Label,
		CCCount:   req.CCCount,
		CodCount:  req.CodCount,
		GmiCount:  req.GmiCount,
		AgyCount:  req.AgyCount,
		GrokCount: req.GrokCount,
		OmpCount:  req.OmpCount,
		OcCount:   req.OcCount,
		Preset:    req.Preset,
		WaitReady: req.WaitReady,
	}

	if s.spawnAgents == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "agent spawn service unavailable", nil, reqID)
		return
	}

	result, err := s.spawnAgents(r.Context(), opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if result == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "agent spawn returned no result", nil, reqID)
		return
	}
	if !result.Success {
		details := map[string]interface{}{}
		if result.Hint != "" {
			details["hint"] = result.Hint
		}
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, details, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AgentSendRequest is the request body for POST /sessions/{id}/agents/send.
type AgentSendRequest struct {
	Panes      []string `json:"panes,omitempty"`
	AgentTypes []string `json:"agent_types,omitempty"`
	Message    string   `json:"message"`
	All        bool     `json:"all,omitempty"`
}

var errPaneProjectIdentityChanged = errors.New("pane identity changed while checking project safety policy")

// loadPaneProjectPolicy resolves the actual target pane, including a project
// root above its current subdirectory. Server selection and daemon cwd cannot
// authorize input into another project's agent.
func loadPaneProjectPolicy(ctx context.Context, pane tmux.Pane) (*policy.Policy, error) {
	if tmux.DefaultClient.Remote != "" {
		return nil, errors.New("cannot verify project safety policy for a remote tmux pane")
	}
	selector, err := tmux.ParsePaneSelector(pane.ID)
	if err != nil || selector.Kind != tmux.PaneSelectorID || pane.PID <= 0 || pane.Dead || pane.IsServicePane() {
		return nil, errors.New("cannot verify project safety policy for a pane without a live physical identity")
	}
	// Read lifetime and project in one tmux observation. A path by itself
	// could belong to a replacement process under the same physical pane ID.
	format := strings.Join([]string{"#{pane_id}", "#{pane_pid}", "#{pane_dead}", "#{pane_current_command}", "#{@ntm_agent_type}", "#{@acfs_service}", "#{@ntm_service}", "#{pane_current_path}"}, tmux.FieldSeparator)
	observation, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(pane.ID), format)
	if err != nil {
		return nil, fmt.Errorf("resolve pane project: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(observation), tmux.FieldSeparator, 8)
	if len(parts) != 8 {
		return nil, errors.New("pane did not report a complete project and lifetime observation")
	}
	pid, err := strconv.Atoi(parts[1])
	recordedType := tmux.ParsePaneAgentTypeOption(parts[4])
	if err != nil || parts[0] != pane.ID || pid != pane.PID || parts[2] != "0" || parts[3] != pane.Command ||
		recordedType != tmux.AgentUnknown && recordedType.Canonical() != pane.Type.Canonical() ||
		recordedType == tmux.AgentUnknown && (tmux.ParsePaneServiceOption(parts[5]) != "" || tmux.ParsePaneServiceOption(parts[6]) != "") {
		return nil, errPaneProjectIdentityChanged
	}
	path := parts[7]
	if !filepath.IsAbs(path) {
		return nil, errors.New("pane did not report an absolute local project path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, errors.New("pane project path is not an accessible local directory")
	}
	projectDir, err := util.ResolveProjectDirContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("resolve pane project root: %w", err)
	}
	p, err := policy.LoadForProject(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load pane project safety policy: %w", err)
	}
	return p, nil
}

type agentMessagePolicyError struct {
	status int
	code   string
	err    error
}

func (e *agentMessagePolicyError) Error() string { return e.err.Error() }
func (e *agentMessagePolicyError) Unwrap() error { return e.err }

// agentMessageGuard runs inside the robot's canonical target selection, so
// every affected pane is checked before the first send or interrupt. Keeping
// this per request avoids global policy races between concurrently served
// projects, while leaving durable selector bindings unchanged.
func (s *Server) agentMessageGuard(requestCtx context.Context, refusal *error) func(context.Context, string, []tmux.Pane) error {
	return func(_ context.Context, message string, panes []tmux.Pane) error {
		fail := func(status int, code string, err error) error {
			failure := &agentMessagePolicyError{status, code, err}
			if *refusal == nil {
				*refusal = failure
			}
			return failure
		}
		budget := s.paneInputTimeout
		if budget <= 0 {
			budget = defaultPaneInputTimeout
		}
		ctx, cancel := context.WithTimeout(requestCtx, budget)
		defer cancel()
		for _, pane := range panes {
			p, err := loadPaneProjectPolicy(ctx, pane)
			if err != nil {
				if ctx.Err() != nil {
					return fail(http.StatusGatewayTimeout, ErrCodeTimeout, ctx.Err())
				}
				if errors.Is(err, errPaneProjectIdentityChanged) {
					return fail(http.StatusConflict, ErrCodeConflict, err)
				}
				return fail(http.StatusServiceUnavailable, ErrCodeServiceUnavail, err)
			}
			if match := p.Check(message); match != nil && match.Action == policy.ActionBlock {
				return fail(http.StatusForbidden, ErrCodeForbidden,
					fmt.Errorf("blocked by safety policy for pane %s: %s", pane.ID, match.Reason))
			}
		}
		return nil
	}
}

func writeAgentMessagePolicyError(w http.ResponseWriter, reqID string, err error, result interface{}) bool {
	var refusal *agentMessagePolicyError
	if !errors.As(err, &refusal) {
		return false
	}
	var details map[string]interface{}
	switch output := result.(type) {
	case *robot.SendOutput:
		if output != nil {
			details = map[string]interface{}{"actuation": output}
			if output.Operation != nil {
				details["operation"] = output.Operation
			}
		}
	case *robot.InterruptOutput:
		if output != nil {
			details = map[string]interface{}{"actuation": output}
			if output.Operation != nil {
				details["operation"] = output.Operation
			}
		}
	}
	writeErrorResponse(w, refusal.status, refusal.code, refusal.Error(), details, reqID)
	return true
}

// handleAgentSendV1 handles POST /api/v1/sessions/{sessionId}/agents/send.
func (s *Server) handleAgentSendV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Message == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "message required", nil, reqID)
		return
	}

	var policyRefusal error
	opts := robot.SendOptions{
		Context:        r.Context(),
		Session:        sessionID,
		Message:        req.Message,
		Panes:          req.Panes,
		AgentTypes:     req.AgentTypes,
		All:            req.All,
		RequestID:      reqID,
		CorrelationID:  reqID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		MessageGuard:   s.agentMessageGuard(r.Context(), &policyRefusal),
		Store:          s.stateStore,
		ExplicitStore:  true,
	}

	sendAgents := robot.GetSend
	if s.sendAgents != nil {
		sendAgents = s.sendAgents
	}
	result, err := sendAgents(opts)
	if writeAgentMessagePolicyError(w, reqID, policyRefusal, result) {
		return
	}
	if err != nil {
		if writeAgentMessagePolicyError(w, reqID, err, result) {
			return
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if result == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "agent send returned no result", nil, reqID)
		return
	}
	if !result.Success {
		details := map[string]interface{}{}
		if result.Hint != "" {
			details["hint"] = result.Hint
		}
		if result.Operation != nil {
			details["operation"] = result.Operation
		}
		if len(result.Warnings) > 0 {
			details["warnings"] = result.Warnings
		}
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, details, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AgentInterruptRequest is the request body for POST /sessions/{id}/agents/interrupt.
type AgentInterruptRequest struct {
	Panes      []string `json:"panes,omitempty"`
	AgentTypes []string `json:"agent_types,omitempty"`
	Message    string   `json:"message,omitempty"`
	Force      bool     `json:"force,omitempty"`
	NoWait     bool     `json:"no_wait,omitempty"`
}

// handleAgentInterruptV1 handles POST /api/v1/sessions/{sessionId}/agents/interrupt.
func (s *Server) handleAgentInterruptV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentInterruptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	var policyRefusal error
	opts := robot.InterruptOptions{
		Session:        sessionID,
		Panes:          req.Panes,
		AgentTypes:     req.AgentTypes,
		Message:        req.Message,
		Force:          req.Force,
		NoWait:         req.NoWait,
		RequestID:      reqID,
		CorrelationID:  reqID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		MessageGuard:   s.agentMessageGuard(r.Context(), &policyRefusal),
		Store:          s.stateStore,
		ExplicitStore:  true,
	}

	interruptAgents := robot.GetInterrupt
	if s.interruptAgents != nil {
		interruptAgents = s.interruptAgents
	}
	result, err := interruptAgents(opts)
	if writeAgentMessagePolicyError(w, reqID, policyRefusal, result) {
		return
	}
	if err != nil {
		if writeAgentMessagePolicyError(w, reqID, err, result) {
			return
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if result == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "agent interrupt returned no result", nil, reqID)
		return
	}
	if !result.Success {
		details := map[string]interface{}{}
		if result.Hint != "" {
			details["hint"] = result.Hint
		}
		if result.Operation != nil {
			details["operation"] = result.Operation
		}
		if len(result.Warnings) > 0 {
			details["warnings"] = result.Warnings
		}
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, details, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AgentWaitRequest is the request body for POST /sessions/{id}/agents/wait.
type AgentWaitRequest struct {
	Condition   string `json:"condition"`
	TimeoutMs   int    `json:"timeout_ms,omitempty"`
	PollMs      int    `json:"poll_ms,omitempty"`
	Panes       []int  `json:"panes,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
	WaitForAny  bool   `json:"wait_for_any,omitempty"`
	ExitOnError bool   `json:"exit_on_error,omitempty"`
}

// handleAgentWaitV1 handles POST /api/v1/sessions/{sessionId}/agents/wait.
// maxAgentWaitTimeout caps the client-supplied wait so one request cannot pin
// a server goroutine indefinitely. Long orchestration waits belong in the
// caller's own polling loop, not in a single HTTP request.
const maxAgentWaitTimeout = 30 * time.Minute

func (s *Server) handleAgentWaitV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentWaitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Condition == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "condition required", nil, reqID)
		return
	}

	// Bound the client-supplied wait so an unbounded timeout_ms cannot pin
	// one server-side goroutine (and its tmux polling) indefinitely. Every
	// other client-supplied bound in this package is clamped; these two were
	// oversights.
	timeout := 30 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	if timeout > maxAgentWaitTimeout {
		timeout = maxAgentWaitTimeout
	}
	pollInterval := 500 * time.Millisecond
	if req.PollMs > 0 {
		pollInterval = time.Duration(req.PollMs) * time.Millisecond
	}
	if pollInterval > timeout {
		pollInterval = timeout
	}

	opts := robot.WaitOptions{
		Session:           sessionID,
		Condition:         req.Condition,
		Timeout:           timeout,
		PollInterval:      pollInterval,
		PaneIndices:       req.Panes,
		AgentType:         req.AgentType,
		WaitForAny:        req.WaitForAny,
		ExitOnError:       req.ExitOnError,
		CountN:            1,
		RequireTransition: false,
	}

	waitAgents := s.waitAgents
	if waitAgents == nil {
		waitAgents = robot.GetWaitContext
	}
	result, exitCode := waitAgents(r.Context(), opts)
	if exitCode != 0 && !result.Success && result.ErrorCode != robot.ErrCodeTimeout {
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	// A wait that times out is a successful observation of "the condition did
	// not become true in the window", and the payload already says so via
	// timed_out. It used to be returned as HTTP 408 through
	// writeSuccessResponse, which unconditionally injects "success": true —
	// so the two halves of one response contradicted each other, and 408
	// (which means the CLIENT was too slow, and which proxies and Go's
	// transport treat as connection-close-and-retry) was wrong on its own
	// terms. Only a genuine internal failure is an error status now.
	if exitCode >= 2 {
		writeErrorResponse(w, http.StatusInternalServerError, result.ErrorCode,
			result.Error, data, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}
