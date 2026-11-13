package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

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
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{session}", s.handleEnsembleSessionV1)
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

func writeEnsembleRobotFailure(w http.ResponseWriter, resp robot.RobotResponse, reqID string) {
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
	writeErrorResponse(w, ensembleRobotErrorStatus(code), code, msg, details, reqID)
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

// handleEnsembleSpawnV1 handles POST /api/v1/ensemble/spawn.
func (s *Server) handleEnsembleSpawnV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req EnsembleSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}

	session := strings.TrimSpace(req.Session)
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "session is required", nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("invalid session name: %v", err), nil, reqID)
		return
	}

	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "question is required", nil, reqID)
		return
	}

	preset := strings.TrimSpace(req.Preset)
	if preset != "" && len(req.Modes) > 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "preset and modes are mutually exclusive", nil, reqID)
		return
	}
	if preset == "" && len(req.Modes) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "either preset or modes is required", nil, reqID)
		return
	}
	if req.BudgetTotal < 0 || req.BudgetPerAgent < 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "budget_total and budget_per_agent must be non-negative", nil, reqID)
		return
	}

	rawProjectDir := strings.TrimSpace(req.ProjectDir)
	if rawProjectDir == "" {
		rawProjectDir = s.projectDirSnapshot()
	}
	if rawProjectDir == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest,
			"project_dir is required when server has no default project directory configured", nil, reqID)
		return
	}
	projectDir, err := ResolveProjectDir(rawProjectDir)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	baseCfg := s.loadEnsembleConfig(projectDir)
	cfgCopy := *baseCfg
	if strings.TrimSpace(req.SynthesisStrategy) != "" {
		strategy, stratErr := ensemble.ValidateOrMigrateStrategy(strings.TrimSpace(req.SynthesisStrategy))
		if stratErr != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, stratErr.Error(), nil, reqID)
			return
		}
		cfgCopy.Ensemble.Synthesis.Strategy = string(strategy)
	}

	SetAuditSession(r, session, "", "")
	SetAuditResource(r, "ensemble", session)

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

	out, err := robot.GetEnsembleSpawn(opts, &cfgCopy)
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

	formatName := strings.ToLower(strings.TrimSpace(req.OutputFormat))
	if formatName == "" {
		formatName = "markdown"
	}
	var outputFormat ensemble.OutputFormat
	switch formatName {
	case "markdown", "md":
		formatName = "markdown"
		outputFormat = ensemble.FormatMarkdown
	case "json":
		outputFormat = ensemble.FormatJSON
	case "yaml", "yml":
		formatName = "yaml"
		outputFormat = ensemble.FormatYAML
	default:
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported output_format %q (use markdown, json, or yaml)", req.OutputFormat), nil, reqID)
		return
	}

	var explicitStrategy ensemble.SynthesisStrategy
	if strings.TrimSpace(req.Strategy) != "" {
		validated, stratErr := ensemble.ValidateOrMigrateStrategy(strings.TrimSpace(req.Strategy))
		if stratErr != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, stratErr.Error(), nil, reqID)
			return
		}
		explicitStrategy = validated
	}

	SetAuditSession(r, session, "", "")
	SetAuditResource(r, "ensemble", session)

	sessionLive := tmux.IsInstalled() && tmux.SessionExists(session)
	state, err := ensemble.LoadSession(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				writeErrorResponse(w, http.StatusNotFound, robot.ErrCodeSessionNotFound,
					fmt.Sprintf("session %q not found", session), nil, reqID)
				return
			}
			writeErrorResponse(w, http.StatusNotFound, robot.ErrCodeEnsembleNotFound,
				fmt.Sprintf("no ensemble state found in session %q", session), nil, reqID)
			return
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	ready, pending, working := countEnsembleAgentStates(state)
	if !req.Force && (pending > 0 || working > 0) {
		writeErrorResponse(w, http.StatusConflict, robot.ErrCodeSynthesisNotReady,
			fmt.Sprintf("synthesis not ready: %d pending, %d working", pending, working),
			map[string]interface{}{"hint": "Set force=true to synthesize available outputs before all modes finish"},
			reqID)
		return
	}
	if ready == 0 && !req.Force {
		writeErrorResponse(w, http.StatusConflict, robot.ErrCodeSynthesisNotReady,
			"no completed mode outputs to synthesize",
			map[string]interface{}{"hint": "Wait for reasoning modes to complete or pass force=true if saved outputs exist"},
			reqID)
		return
	}

	var captured []ensemble.CapturedOutput
	if sessionLive {
		capture := ensemble.NewOutputCapture(tmux.DefaultClient)
		var capErr error
		captured, capErr = capture.CaptureAll(state)
		if capErr != nil {
			slog.Warn("ensemble pane output capture failed; using saved outputs",
				"session", session, "error", capErr)
		}
	}

	availableOutputs, err := ensemble.CollectModeOutputs(state, captured)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			fmt.Sprintf("collect mode outputs: %v", err), nil, reqID)
		return
	}

	collector := ensemble.NewOutputCollector(ensemble.DefaultOutputCollectorConfig())
	for _, modeOutput := range availableOutputs.Outputs {
		if addErr := collector.Add(modeOutput); addErr != nil {
			writeErrorResponse(w, http.StatusUnprocessableEntity, robot.ErrCodeOutputSchemaInvalid,
				fmt.Sprintf("invalid output from mode %s: %v", modeOutput.ModeID, addErr), nil, reqID)
			return
		}
	}
	for modeID, errs := range availableOutputs.ValidationErrors {
		if _, exists := collector.ValidationErrors[modeID]; !exists {
			collector.ValidationErrors[modeID] = errs
		}
	}

	if collector.Count() == 0 {
		if collector.ErrorCount() > 0 {
			writeErrorResponse(w, http.StatusUnprocessableEntity, robot.ErrCodeOutputSchemaInvalid,
				fmt.Sprintf("no valid mode outputs collected (%d validation errors)", collector.ErrorCount()),
				map[string]interface{}{"validation_errors": collector.ValidationErrors}, reqID)
			return
		}
		writeErrorResponse(w, http.StatusConflict, robot.ErrCodeSynthesisNotReady,
			"no mode outputs available for synthesis", nil, reqID)
		return
	}

	cfg := s.loadEnsembleConfig(s.projectDirSnapshot())
	budgetCfg := ensemble.BudgetConfigFromTOML(ensemble.DefaultBudgetConfig(), cfg.Ensemble.Budget)
	baseSynth := ensemble.SynthesisConfig{
		Strategy:      state.SynthesisStrategy,
		MaxFindings:   20,
		MinConfidence: 0.3,
	}
	synthConfig := ensemble.SynthesisConfigFromTOML(baseSynth, cfg.Ensemble.Synthesis)
	if state.SynthesisStrategy != "" && explicitStrategy == "" {
		synthConfig.Strategy = state.SynthesisStrategy
	}
	if explicitStrategy != "" {
		synthConfig.Strategy = explicitStrategy
	}
	if budgetCfg.SynthesisReserveTokens > 0 {
		synthConfig.MaxSynthesisTokens = budgetCfg.SynthesisReserveTokens
	}

	synth, err := ensemble.NewSynthesizer(synthConfig)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	input, err := collector.BuildSynthesisInput(state.Question, nil, synthConfig)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			fmt.Sprintf("build synthesis input: %v", err), nil, reqID)
		return
	}

	result, err := synth.SynthesizeContext(r.Context(), input)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			fmt.Sprintf("synthesis failed: %v", err), nil, reqID)
		return
	}

	catalog, _ := ensemble.GlobalCatalog()
	redundancy := result.Redundancy
	if redundancy == nil {
		redundancy = ensemble.AnalyzeRedundancy(input.Outputs, catalog)
		result.Redundancy = redundancy
	}

	var rendered bytes.Buffer
	formatter := ensemble.NewSynthesisFormatter(outputFormat)
	formatter.IncludeRaw = synthConfig.IncludeRawOutputs
	formatter.IncludeAudit = true
	if err := formatter.FormatResult(&rendered, result, input.AuditReport); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			fmt.Sprintf("format synthesis output: %v", err), nil, reqID)
		return
	}

	generatedAt := result.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	report := &robot.SynthesisReport{
		Summary:              result.Summary,
		Strategy:             string(synth.Strategy.Name),
		Format:               formatName,
		FindingsCount:        len(result.Findings),
		RecommendationsCount: len(result.Recommendations),
		RisksCount:           len(result.Risks),
		QuestionsCount:       len(result.QuestionsForUser),
		Confidence:           float64(result.Confidence),
		GeneratedAt:          generatedAt.UTC().Format(time.RFC3339),
		SynthesizedBy:        result.SynthesizedBy,
		FallbackReason:       result.FallbackReason,
		Redundancy:           redundancy,
	}

	auditSummary := summarizeSynthesisAudit(input.AuditReport)

	respObj := EnsembleSynthesizeEnvelopeResponse{
		Action:          "ensemble_synthesize",
		Session:         session,
		Status:          "complete",
		Report:          report,
		Audit:           auditSummary,
		Result:          result,
		AuditReport:     input.AuditReport,
		Redundancy:      redundancy,
		FormattedOutput: rendered.String(),
	}

	payload, err := toJSONMap(respObj)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
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

func countEnsembleAgentStates(state *ensemble.EnsembleSession) (ready, pending, working int) {
	if state == nil {
		return 0, 0, 0
	}
	for _, a := range state.Assignments {
		switch a.Status {
		case ensemble.AssignmentDone:
			ready++
		case ensemble.AssignmentPending, ensemble.AssignmentInjecting:
			pending++
		case ensemble.AssignmentActive:
			working++
		}
	}
	return ready, pending, working
}

func summarizeSynthesisAudit(auditReport *ensemble.AuditReport) *robot.SynthesisAudit {
	summary := &robot.SynthesisAudit{
		HighConflictPairs: []string{},
	}
	if auditReport == nil {
		return summary
	}
	summary.ConflictCount = len(auditReport.Conflicts)
	pairSet := make(map[string]struct{})
	for _, c := range auditReport.Conflicts {
		if strings.TrimSpace(c.ResolutionPath) == "" {
			summary.UnresolvedCount++
		}
		if c.Severity == ensemble.ConflictHigh && len(c.Positions) >= 2 {
			modes := make([]string, 0, len(c.Positions))
			for _, pos := range c.Positions {
				if m := strings.TrimSpace(pos.ModeID); m != "" {
					modes = append(modes, m)
				}
			}
			sort.Strings(modes)
			if len(modes) >= 2 {
				pairSet[modes[0]+" vs "+modes[1]] = struct{}{}
			}
		}
	}
	for pair := range auditReport.ModeDisagreements {
		if len(auditReport.ModeDisagreements[pair]) >= 2 {
			pairSet[pair] = struct{}{}
		}
	}
	for pair := range pairSet {
		summary.HighConflictPairs = append(summary.HighConflictPairs, pair)
	}
	sort.Strings(summary.HighConflictPairs)
	return summary
}
