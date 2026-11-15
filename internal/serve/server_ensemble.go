package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ErrCodeInvalidRequest identifies invalid request payloads or parameters.
const ErrCodeInvalidRequest = "INVALID_REQUEST"

// EnsembleModeList decodes either a JSON string array (["deductive","bayesian"])
// or a comma-separated string ("deductive,bayesian") while reflecting as []string
// in OpenAPI 3.1 schemas.
type EnsembleModeList []string

// UnmarshalJSON implements json.Unmarshaler for EnsembleModeList.
func (m *EnsembleModeList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*m = nil
		return nil
	}
	if trimmed[0] == '[' {
		var items []string
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return err
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			for _, part := range strings.Split(item, ",") {
				if v := strings.TrimSpace(part); v != "" {
					out = append(out, v)
				}
			}
		}
		*m = out
		return nil
	}
	var raw string
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return fmt.Errorf("modes must be a string array or comma-separated string: %w", err)
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	*m = out
	return nil
}

// FlexibleAgentMix decodes either an agent-mix string ("cc=2,cod=1") or a JSON
// object ({"cc":2,"cod":1}) while reflecting as string in OpenAPI 3.1 schemas.
type FlexibleAgentMix string

// UnmarshalJSON implements json.Unmarshaler for FlexibleAgentMix.
func (a *FlexibleAgentMix) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*a = ""
		return nil
	}
	if trimmed[0] == '{' {
		var mix map[string]int
		if err := json.Unmarshal(trimmed, &mix); err != nil {
			return err
		}
		keys := make([]string, 0, len(mix))
		for k := range mix {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", strings.TrimSpace(k), mix[k]))
		}
		*a = FlexibleAgentMix(strings.Join(parts, ","))
		return nil
	}
	var raw string
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return fmt.Errorf("agents must be an agent-mix string or agent count map: %w", err)
	}
	*a = FlexibleAgentMix(strings.TrimSpace(raw))
	return nil
}

// =============================================================================
// Ensemble Request & Response Shapes (Reflected into OpenAPI 3.1)
// =============================================================================

// EnsembleModesEnvelopeResponse is the REST envelope for GET /api/v1/ensemble/modes.
type EnsembleModesEnvelopeResponse struct {
	robot.EnsembleModesOutput
	RequestID string `json:"request_id,omitempty"`
}

// EnsemblePresetsEnvelopeResponse is the REST envelope for GET /api/v1/ensemble/presets.
type EnsemblePresetsEnvelopeResponse struct {
	robot.EnsemblePresetsOutput
	RequestID string `json:"request_id,omitempty"`
}

// EnsembleSuggestRequest is the JSON request body for POST /api/v1/ensemble/suggest.
type EnsembleSuggestRequest struct {
	Question string `json:"question"`
	IDOnly   bool   `json:"id_only,omitempty"`
}

// EnsembleSuggestEnvelopeResponse is the REST envelope for POST /api/v1/ensemble/suggest.
type EnsembleSuggestEnvelopeResponse struct {
	APIResponse
	Question    string                           `json:"question"`
	TopPick     *robot.EnsembleSuggestion        `json:"top_pick,omitempty"`
	Suggestions []robot.EnsembleSuggestion       `json:"suggestions,omitempty"`
	PresetName  string                           `json:"preset_name,omitempty"`
	SpawnCmd    string                           `json:"spawn_cmd,omitempty"`
	AgentHints  *robot.EnsembleSuggestAgentHints `json:"_agent_hints,omitempty"`
}

// EnsembleSessionEnvelopeResponse is the REST envelope for GET /api/v1/ensemble/sessions/{session}.
type EnsembleSessionEnvelopeResponse struct {
	robot.EnsembleOutput
	RequestID string `json:"request_id,omitempty"`
}

// EnsembleSpawnRequest is the JSON request body for POST /api/v1/ensemble/spawn.
type EnsembleSpawnRequest struct {
	Session           string           `json:"session"`
	Preset            string           `json:"preset,omitempty"`
	Modes             EnsembleModeList `json:"modes,omitempty"`
	Question          string           `json:"question"`
	Agents            FlexibleAgentMix `json:"agents,omitempty"`
	Assignment        string           `json:"assignment,omitempty"`
	AllowAdvanced     bool             `json:"allow_advanced,omitempty"`
	BudgetTotal       int              `json:"budget_total,omitempty"`
	BudgetPerAgent    int              `json:"budget_per_agent,omitempty"`
	NoCache           bool             `json:"no_cache,omitempty"`
	SynthesisStrategy string           `json:"synthesis_strategy,omitempty"`
	ProjectDir        string           `json:"project_dir,omitempty"`
}

// EnsembleSpawnEnvelopeResponse is the REST envelope for POST /api/v1/ensemble/spawn.
type EnsembleSpawnEnvelopeResponse struct {
	robot.EnsembleSpawnOutput
	RequestID string `json:"request_id,omitempty"`
}

// EnsembleSynthesizeRequest is the optional JSON body for POST /api/v1/ensemble/sessions/{session}/synthesize.
type EnsembleSynthesizeRequest struct {
	Strategy     string `json:"strategy,omitempty"`
	OutputFormat string `json:"output_format,omitempty"`
	Force        bool   `json:"force,omitempty"`
}

// EnsembleSynthesizeEnvelopeResponse is the REST envelope for POST /api/v1/ensemble/sessions/{session}/synthesize.
type EnsembleSynthesizeEnvelopeResponse struct {
	APIResponse
	Action          string                       `json:"action"`
	Session         string                       `json:"session"`
	Status          string                       `json:"status"`
	Report          *robot.SynthesisReport       `json:"report,omitempty"`
	Audit           *robot.SynthesisAudit        `json:"audit,omitempty"`
	Result          *ensemble.SynthesisResult    `json:"result,omitempty"`
	AuditReport     *ensemble.AuditReport        `json:"audit_report,omitempty"`
	Redundancy      *ensemble.RedundancyAnalysis `json:"redundancy,omitempty"`
	FormattedOutput string                       `json:"formatted_output,omitempty"`
	AgentHints      *robot.AgentHints            `json:"_agent_hints,omitempty"`
}

// EnsembleStopRequest is the optional JSON body for POST /api/v1/ensemble/sessions/{session}/stop.
type EnsembleStopRequest struct {
	Force     bool `json:"force,omitempty"`
	NoCollect bool `json:"no_collect,omitempty"`
}

// EnsembleStopEnvelopeResponse is the REST envelope for POST /api/v1/ensemble/sessions/{session}/stop.
type EnsembleStopEnvelopeResponse struct {
	robot.EnsembleStopOutput
	RequestID string `json:"request_id,omitempty"`
}

// =============================================================================
// Route Registration & Handlers
// =============================================================================

// registerEnsembleRoutes mounts the /api/v1/ensemble/* REST endpoints.
func (s *Server) registerEnsembleRoutes(r chi.Router) {
	r.Route("/ensemble", func(r chi.Router) {
		r.With(s.RequirePermission(PermReadSessions)).Get("/modes", s.handleEnsembleModesV1)
		r.With(s.RequirePermission(PermReadSessions)).Get("/presets", s.handleEnsemblePresetsV1)
		r.With(s.RequirePermission(PermReadSessions)).Post("/suggest", s.handleEnsembleSuggestV1)
		r.With(s.RequirePermission(PermReadSessions)).Post("/estimate", s.handleEnsembleEstimateV1)
		r.With(s.RequirePermission(PermReadSessions)).Get("/runs", s.handleEnsembleRunsV1)
		r.With(s.RequirePermission(PermReadSessions)).Post("/compare", s.handleEnsembleCompareV1)
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{session}", s.handleEnsembleSessionV1)
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{session}/provenance", s.handleEnsembleProvenanceV1)
		r.With(s.RequirePermission(PermWriteSessions), s.RequirePermission(PermWriteAgents)).Post("/spawn", s.handleEnsembleSpawnV1)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{session}/synthesize", s.handleEnsembleSynthesizeV1)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{session}/stop", s.handleEnsembleStopV1)
	})
}

func (s *Server) loadEnsembleConfig(projectDir string) *config.Config {
	if strings.TrimSpace(projectDir) != "" {
		if merged, err := config.LoadMerged(projectDir, ""); err == nil && merged != nil {
			return merged
		}
	}
	if loaded, err := config.Load(""); err == nil && loaded != nil {
		return loaded
	}
	return config.Default()
}

func ensembleRobotErrorStatus(code string) int {
	switch code {
	case robot.ErrCodeEnsembleNotFound, robot.ErrCodeSessionNotFound, robot.ErrCodePaneNotFound, robot.ErrCodeNotFound:
		return http.StatusNotFound
	case robot.ErrCodeInvalidFlag, ErrCodeInvalidRequest, ErrCodeBadRequest:
		return http.StatusBadRequest
	case robot.ErrCodeSynthesisNotReady:
		return http.StatusConflict
	case robot.ErrCodeOutputSchemaInvalid:
		return http.StatusUnprocessableEntity
	default:
		return robotErrorHTTPStatus(code)
	}
}

type ensembleExecError struct {
	status  int
	code    string
	message string
	details map[string]interface{}
	cause   error
}

func (e *ensembleExecError) Error() string {
	if e == nil {
		return ""
	}
	if e.code != "" {
		return fmt.Sprintf("ensemble operation failed [%s]: %s", e.code, e.message)
	}
	return e.message
}

func (e *ensembleExecError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func ensembleExecErrorFromRobot(resp robot.RobotResponse) *ensembleExecError {
	code := strings.TrimSpace(resp.ErrorCode)
	if code == "" {
		code = ErrCodeInternalError
	}
	msg := strings.TrimSpace(resp.Error)
	if msg == "" {
		msg = "ensemble operation failed"
	}
	var details map[string]interface{}
	if strings.TrimSpace(resp.Hint) != "" {
		details = map[string]interface{}{"hint": resp.Hint}
	}
	status := ensembleRobotErrorStatus(code)
	var cause error
	if status == http.StatusBadRequest {
		cause = errInvalidJobRequest
	}
	return &ensembleExecError{
		status:  status,
		code:    code,
		message: msg,
		details: details,
		cause:   cause,
	}
}

func writeEnsembleOperationError(w http.ResponseWriter, err error, reqID string) {
	var execErr *ensembleExecError
	if errors.As(err, &execErr) && execErr != nil {
		status := execErr.status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		code := execErr.code
		if code == "" {
			code = ErrCodeInternalError
		}
		writeErrorResponse(w, status, code, execErr.message, execErr.details, reqID)
		return
	}
	writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
}

func writeEnsembleRobotFailure(w http.ResponseWriter, resp robot.RobotResponse, reqID string) {
	writeEnsembleOperationError(w, ensembleExecErrorFromRobot(resp), reqID)
}

func parseNonNegativeQueryInt(r *http.Request, name string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("query parameter %q must be a non-negative integer", name)
	}
	return v, nil
}

// handleEnsembleModesV1 handles GET /api/v1/ensemble/modes.
func (s *Server) handleEnsembleModesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	limit, err := parseNonNegativeQueryInt(r, "limit")
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}
	offset, err := parseNonNegativeQueryInt(r, "offset")
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	opts := robot.EnsembleModesOptions{
		Category: strings.TrimSpace(r.URL.Query().Get("category")),
		Tier:     strings.TrimSpace(r.URL.Query().Get("tier")),
		Search:   strings.TrimSpace(r.URL.Query().Get("search")),
		Limit:    limit,
		Offset:   offset,
	}

	out, err := robot.GetEnsembleModes(opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeEnsembleRobotFailure(w, out.RobotResponse, reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleEnsemblePresetsV1 handles GET /api/v1/ensemble/presets.
func (s *Server) handleEnsemblePresetsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	out, err := robot.GetEnsemblePresets()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeEnsembleRobotFailure(w, out.RobotResponse, reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleEnsembleSuggestV1 handles POST /api/v1/ensemble/suggest.
func (s *Server) handleEnsembleSuggestV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req EnsembleSuggestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "question is required", map[string]interface{}{
			"hint": "Provide a non-empty question string in the JSON request body",
		}, reqID)
		return
	}

	out, err := robot.GetEnsembleSuggest(question)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeEnsembleRobotFailure(w, out.RobotResponse, reqID)
		return
	}

	if req.IDOnly {
		idResp := map[string]interface{}{
			"question":    out.Question,
			"preset_name": "",
			"spawn_cmd":   "",
		}
		if out.TopPick != nil {
			idResp["preset_name"] = out.TopPick.PresetName
			idResp["spawn_cmd"] = out.TopPick.SpawnCmd
		}
		writeSuccessResponse(w, http.StatusOK, idResp, reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleEnsembleSessionV1 handles GET /api/v1/ensemble/sessions/{session}.
func (s *Server) handleEnsembleSessionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	session := strings.TrimSpace(chi.URLParam(r, "session"))
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "session name is required", nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("invalid session name: %v", err), nil, reqID)
		return
	}

	out, err := robot.GetEnsemble(session)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeEnsembleRobotFailure(w, out.RobotResponse, reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// executeEnsembleSpawn validates and executes an ensemble spawn operation.
// Shared by POST /api/v1/ensemble/spawn and async job type ensemble_spawn.
func (s *Server) executeEnsembleSpawn(ctx context.Context, req EnsembleSpawnRequest) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	session := strings.TrimSpace(req.Session)
	if session == "" {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: "session is required",
			cause:   errInvalidJobRequest,
		}
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: fmt.Sprintf("invalid session name: %v", err),
			cause:   errInvalidJobRequest,
		}
	}

	question := strings.TrimSpace(req.Question)
	if question == "" {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: "question is required",
			cause:   errInvalidJobRequest,
		}
	}

	preset := strings.TrimSpace(req.Preset)
	if preset != "" && len(req.Modes) > 0 {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: "preset and modes are mutually exclusive",
			cause:   errInvalidJobRequest,
		}
	}
	if preset == "" && len(req.Modes) == 0 {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: "either preset or modes is required",
			cause:   errInvalidJobRequest,
		}
	}
	if req.BudgetTotal < 0 || req.BudgetPerAgent < 0 {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: "budget_total and budget_per_agent must be non-negative",
			cause:   errInvalidJobRequest,
		}
	}

	rawProjectDir := strings.TrimSpace(req.ProjectDir)
	if rawProjectDir == "" {
		rawProjectDir = s.projectDirSnapshot()
	}
	if rawProjectDir == "" {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: "project_dir is required when server has no default project directory configured",
			cause:   errInvalidJobRequest,
		}
	}
	projectDir, err := ResolveProjectDir(rawProjectDir)
	if err != nil {
		return nil, &ensembleExecError{
			status:  http.StatusBadRequest,
			code:    ErrCodeInvalidRequest,
			message: err.Error(),
			cause:   errInvalidJobRequest,
		}
	}

	baseCfg := s.loadEnsembleConfig(projectDir)
	cfgCopy := *baseCfg
	if strings.TrimSpace(req.SynthesisStrategy) != "" {
		strategy, stratErr := ensemble.ValidateOrMigrateStrategy(strings.TrimSpace(req.SynthesisStrategy))
		if stratErr != nil {
			return nil, &ensembleExecError{
				status:  http.StatusBadRequest,
				code:    ErrCodeInvalidRequest,
				message: stratErr.Error(),
				cause:   errInvalidJobRequest,
			}
		}
		cfgCopy.Ensemble.Synthesis.Strategy = string(strategy)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := reportJobProgress(ctx, map[string]interface{}{
		"action":      "ensemble_spawn",
		"session":     session,
		"project_dir": projectDir,
		"status":      "spawning",
	}); err != nil {
		return nil, err
	}

	opts := robot.EnsembleSpawnOptions{
		Session:       session,
		Preset:        preset,
		Modes:         strings.Join([]string(req.Modes), ","),
		Question:      question,
		Agents:        string(req.Agents),
		Assignment:    strings.TrimSpace(req.Assignment),
		AllowAdvanced: req.AllowAdvanced,
		BudgetTotal:   req.BudgetTotal,
		BudgetPerMode: req.BudgetPerAgent,
		NoCache:       req.NoCache,
		ProjectDir:    projectDir,
	}

	out, spawnErr := robot.GetEnsembleSpawn(ctx, opts, &cfgCopy)
	payload, encodeErr := toJSONMap(out)
	if encodeErr != nil {
		return nil, &ensembleExecError{
			status:  http.StatusInternalServerError,
			code:    ErrCodeInternalError,
			message: encodeErr.Error(),
			cause:   encodeErr,
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return payload, errors.Join(spawnErr, ctxErr)
	}
	if spawnErr != nil {
		return payload, &ensembleExecError{
			status:  http.StatusInternalServerError,
			code:    ErrCodeInternalError,
			message: spawnErr.Error(),
			cause:   spawnErr,
		}
	}
	if !out.Success {
		return payload, ensembleExecErrorFromRobot(out.RobotResponse)
	}
	return payload, nil
}

// handleEnsembleSpawnV1 handles POST /api/v1/ensemble/spawn.
func (s *Server) handleEnsembleSpawnV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req EnsembleSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}

	payload, err := s.executeEnsembleSpawn(r.Context(), req)
	if !errors.Is(err, errInvalidJobRequest) {
		if session := strings.TrimSpace(req.Session); session != "" {
			SetAuditSession(r, session, "", "")
			SetAuditResource(r, "ensemble", session)
		}
	}
	if err != nil {
		writeEnsembleOperationError(w, err, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// executeEnsembleSynthesize validates and executes synthesis over an ensemble session
// by delegating to the canonical robot.GetEnsembleSynthesize engine (ORI).
// Shared by POST /api/v1/ensemble/sessions/{session}/synthesize and async job type ensemble_synthesize.
func (s *Server) executeEnsembleSynthesize(ctx context.Context, session string, req EnsembleSynthesizeRequest) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	session = strings.TrimSpace(session)
	cfg := s.loadEnsembleConfig(s.projectDirSnapshot())

	out, err := robot.GetEnsembleSynthesize(ctx, robot.EnsembleSynthesizeOptions{
		Session:  session,
		Strategy: req.Strategy,
		Format:   req.OutputFormat,
		Force:    req.Force,
		OnProgress: func(stage string) error {
			return reportJobProgress(ctx, map[string]interface{}{
				"action":  "ensemble_synthesize",
				"session": session,
				"status":  stage,
			})
		},
	}, cfg)
	if err != nil {
		return nil, err
	}
	if !out.Success {
		execErr := ensembleExecErrorFromRobot(out.RobotResponse)
		if out.ErrorCode == robot.ErrCodeInvalidFlag {
			execErr.code = ErrCodeInvalidRequest
			execErr.status = http.StatusBadRequest
			execErr.cause = errInvalidJobRequest
		}
		if len(out.ValidationErrors) > 0 {
			if execErr.details == nil {
				execErr.details = make(map[string]interface{})
			}
			execErr.details["validation_errors"] = out.ValidationErrors
		}
		return nil, execErr
	}

	respObj := EnsembleSynthesizeEnvelopeResponse{
		Action:          out.Action,
		Session:         out.Session,
		Status:          out.Status,
		Report:          out.Report,
		Audit:           out.Audit,
		Result:          out.Result,
		AuditReport:     out.AuditReport,
		Redundancy:      out.Redundancy,
		FormattedOutput: out.FormattedOutput,
		AgentHints:      out.AgentHints,
	}

	payload, err := toJSONMap(respObj)
	if err != nil {
		return nil, &ensembleExecError{
			status:  http.StatusInternalServerError,
			code:    ErrCodeInternalError,
			message: err.Error(),
			cause:   err,
		}
	}
	if err := ctx.Err(); err != nil {
		return payload, err
	}
	return payload, nil
}

// handleEnsembleSynthesizeV1 handles POST /api/v1/ensemble/sessions/{session}/synthesize.
func (s *Server) handleEnsembleSynthesizeV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	session := strings.TrimSpace(chi.URLParam(r, "session"))
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "session name is required", nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("invalid session name: %v", err), nil, reqID)
		return
	}

	var req EnsembleSynthesizeRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}

	payload, err := s.executeEnsembleSynthesize(r.Context(), session, req)
	if !errors.Is(err, errInvalidJobRequest) {
		SetAuditSession(r, session, "", "")
		SetAuditResource(r, "ensemble", session)
	}
	if err != nil {
		writeEnsembleOperationError(w, err, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleEnsembleStopV1 handles POST /api/v1/ensemble/sessions/{session}/stop.
func (s *Server) handleEnsembleStopV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	session := strings.TrimSpace(chi.URLParam(r, "session"))
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "session name is required", nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("invalid session name: %v", err), nil, reqID)
		return
	}

	var req EnsembleStopRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}

	SetAuditSession(r, session, "", "")
	SetAuditResource(r, "ensemble", session)

	out, err := robot.GetEnsembleStop(session, robot.EnsembleStopOptions{
		Force:     req.Force,
		NoCollect: req.NoCollect,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeEnsembleRobotFailure(w, out.RobotResponse, reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}
