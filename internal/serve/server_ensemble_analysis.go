package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const ensembleEstimateNearBudgetThreshold = 0.85

// =============================================================================
// Ensemble Analytical Request & Response Shapes (Reflected into OpenAPI 3.1)
// =============================================================================

// EnsembleEstimateRequest is the JSON request body for POST /api/v1/ensemble/estimate.
type EnsembleEstimateRequest struct {
	Preset            string           `json:"preset,omitempty"`
	Modes             EnsembleModeList `json:"modes,omitempty"`
	Question          string           `json:"question,omitempty"`
	AllowAdvanced     *bool            `json:"allow_advanced,omitempty"`
	BudgetTotal       int              `json:"budget_total,omitempty"`
	BudgetPerAgent    int              `json:"budget_per_agent,omitempty"`
	BudgetSynthesis   int              `json:"budget_synthesis,omitempty"`
	BudgetContextPack int              `json:"budget_context_pack,omitempty"`
	ProjectDir        string           `json:"project_dir,omitempty"`
}

// EnsembleEstimateModeRow describes a single mode's token and value estimate.
type EnsembleEstimateModeRow struct {
	ModeID        string  `json:"mode_id"`
	ModeCode      string  `json:"mode_code,omitempty"`
	ModeName      string  `json:"mode_name,omitempty"`
	Category      string  `json:"category,omitempty"`
	Tier          string  `json:"tier,omitempty"`
	TokenEstimate int     `json:"token_estimate"`
	ValueScore    float64 `json:"value_score,omitempty"`
	ValuePerToken float64 `json:"value_per_token,omitempty"`
}

// EnsembleEstimateBudgetSummary summarizes the computed budget for an ensemble run.
type EnsembleEstimateBudgetSummary struct {
	MaxTokensPerMode       int `json:"max_tokens_per_mode"`
	MaxTotalTokens         int `json:"max_total_tokens"`
	SynthesisReserveTokens int `json:"synthesis_reserve_tokens,omitempty"`
	ContextReserveTokens   int `json:"context_reserve_tokens,omitempty"`
	EstimatedModeTokens    int `json:"estimated_mode_tokens"`
	EstimatedTotalTokens   int `json:"estimated_total_tokens"`
	ModeCount              int `json:"mode_count"`
	BudgetOverride         int `json:"budget_override,omitempty"`
}

// EnsembleEstimateSuggestion recommends a lower-token mode substitution when over budget.
type EnsembleEstimateSuggestion struct {
	ReplaceModeID     string `json:"replace_mode_id"`
	ReplaceModeName   string `json:"replace_mode_name,omitempty"`
	ReplaceTokens     int    `json:"replace_tokens"`
	SuggestedModeID   string `json:"suggested_mode_id"`
	SuggestedModeName string `json:"suggested_mode_name,omitempty"`
	SuggestedTokens   int    `json:"suggested_tokens"`
	SavingsTokens     int    `json:"savings_tokens"`
	Reason            string `json:"reason,omitempty"`
}

// EnsembleEstimateEnvelopeResponse is the REST envelope for POST /api/v1/ensemble/estimate.
type EnsembleEstimateEnvelopeResponse struct {
	APIResponse
	GeneratedAt time.Time                     `json:"generated_at"`
	PresetName  string                        `json:"preset_name,omitempty"`
	PresetLabel string                        `json:"preset_label,omitempty"`
	OverBudget  bool                          `json:"over_budget"`
	Modes       []EnsembleEstimateModeRow     `json:"modes"`
	Budget      EnsembleEstimateBudgetSummary `json:"budget"`
	Warnings    []string                      `json:"warnings,omitempty"`
	Suggestions []EnsembleEstimateSuggestion  `json:"suggestions,omitempty"`
}

// EnsembleRunSummary describes a saved ensemble session or checkpoint run.
type EnsembleRunSummary struct {
	RunID             string    `json:"run_id"`
	SessionName       string    `json:"session_name,omitempty"`
	Source            string    `json:"source"` // "session" or "checkpoint"
	Question          string    `json:"question,omitempty"`
	PresetUsed        string    `json:"preset_used,omitempty"`
	Status            string    `json:"status"`
	SynthesisStrategy string    `json:"synthesis_strategy,omitempty"`
	TotalModes        int       `json:"total_modes"`
	CompletedModes    int       `json:"completed_modes"`
	PendingModes      int       `json:"pending_modes"`
	ErrorModes        int       `json:"error_modes"`
	HasSynthesis      bool      `json:"has_synthesis"`
	Live              bool      `json:"live,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

// EnsembleRunsEnvelopeResponse is the REST envelope for GET /api/v1/ensemble/runs.
type EnsembleRunsEnvelopeResponse struct {
	APIResponse
	Runs  []EnsembleRunSummary `json:"runs"`
	Count int                  `json:"count"`
}

// EnsembleProvenanceEnvelopeResponse is the REST envelope for GET /api/v1/ensemble/sessions/{session}/provenance.
type EnsembleProvenanceEnvelopeResponse struct {
	APIResponse
	Session          string                      `json:"session"`
	FindingFilter    string                      `json:"finding_filter,omitempty"`
	ContextHash      string                      `json:"context_hash,omitempty"`
	Stats            ensemble.ProvenanceStats    `json:"stats"`
	Chains           []*ensemble.ProvenanceChain `json:"chains"`
	Chain            *ensemble.ProvenanceChain   `json:"chain,omitempty"`
	Count            int                         `json:"count"`
	InvalidCount     int                         `json:"invalid_count,omitempty"`
	ValidationErrors []string                    `json:"validation_errors,omitempty"`
}

// EnsembleCompareRequest is the JSON request body for POST /api/v1/ensemble/compare.
type EnsembleCompareRequest struct {
	RunA       string `json:"run_a"`
	RunB       string `json:"run_b"`
	Verbose    bool   `json:"verbose,omitempty"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// EnsembleCompareEnvelopeResponse is the REST envelope for POST /api/v1/ensemble/compare.
type EnsembleCompareEnvelopeResponse struct {
	APIResponse
	GeneratedAt     time.Time                  `json:"generated_at"`
	RunA            string                     `json:"run_a"`
	RunB            string                     `json:"run_b"`
	Summary         string                     `json:"summary"`
	Unchanged       bool                       `json:"unchanged"`
	ChangedSections []string                   `json:"changed_sections,omitempty"`
	Result          *ensemble.ComparisonResult `json:"result"`
}

// =============================================================================
// Handlers
// =============================================================================

// handleEnsembleEstimateV1 handles POST /api/v1/ensemble/estimate.
func (s *Server) handleEnsembleEstimateV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req EnsembleEstimateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}

	presetName := strings.TrimSpace(req.Preset)
	if presetName == "" && len(req.Modes) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "either preset or modes is required", nil, reqID)
		return
	}
	if presetName != "" && len(req.Modes) > 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "preset and modes are mutually exclusive", nil, reqID)
		return
	}
	if req.BudgetTotal < 0 || req.BudgetPerAgent < 0 || req.BudgetSynthesis < 0 || req.BudgetContextPack < 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "budget overrides must be non-negative", nil, reqID)
		return
	}

	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, req.ProjectDir, reqID)
	if !ok {
		return
	}

	catalog, err := ensemble.GlobalCatalog()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, fmt.Sprintf("load mode catalog: %v", err), nil, reqID)
		return
	}

	var (
		modeIDs       []string
		presetUsed    string
		presetLabel   string
		question      = strings.TrimSpace(req.Question)
		allowAdvanced = true
		cacheCfg      ensemble.CacheConfig
		budget        = ensemble.DefaultBudgetConfig()
	)

	if presetName != "" {
		registry, regErr := ensemble.GlobalEnsembleRegistry()
		if regErr != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, fmt.Sprintf("load ensemble registry: %v", regErr), nil, reqID)
			return
		}
		preset := registry.Get(presetName)
		if preset == nil {
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("ensemble preset %q not found", presetName), nil, reqID)
			return
		}
		presetUsed = preset.Name
		presetLabel = preset.DisplayName
		if presetLabel == "" {
			presetLabel = preset.Name
		}
		modeIDs, err = preset.ResolveIDs(catalog)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("resolve preset modes: %v", err), nil, reqID)
			return
		}
		budget = mergeEnsembleBudgetDefaults(preset.Budget, budget)
		cacheCfg = preset.Cache
		allowAdvanced = preset.AllowAdvanced
		if question == "" {
			question = preset.Description
		}
	} else {
		modeIDs, err = resolveEnsembleEstimateModeIDs(req.Modes, catalog)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
			return
		}
	}
	if req.AllowAdvanced != nil {
		allowAdvanced = *req.AllowAdvanced
	}

	cfg := s.loadEnsembleConfig(projectDir)
	budget = ensemble.BudgetConfigFromTOML(budget, cfg.Ensemble.Budget)
	if req.BudgetTotal > 0 {
		budget.MaxTotalTokens = req.BudgetTotal
	}
	if req.BudgetPerAgent > 0 {
		budget.MaxTokensPerMode = req.BudgetPerAgent
	}
	if req.BudgetSynthesis > 0 {
		budget.SynthesisReserveTokens = req.BudgetSynthesis
	}
	if req.BudgetContextPack > 0 {
		budget.ContextReserveTokens = req.BudgetContextPack
	}

	estimator := ensemble.NewEstimator(catalog, slog.Default())
	est, err := estimator.Estimate(r.Context(), ensemble.EstimateInput{
		ModeIDs:       modeIDs,
		Question:      question,
		ProjectDir:    projectDir,
		Budget:        budget,
		Cache:         cacheCfg,
		AllowAdvanced: allowAdvanced,
	}, ensemble.EstimateOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	rows := make([]EnsembleEstimateModeRow, 0, len(est.Modes))
	modeTokens := 0
	for _, m := range est.Modes {
		rows = append(rows, EnsembleEstimateModeRow{
			ModeID:        m.ID,
			ModeCode:      m.Code,
			ModeName:      m.Name,
			Category:      m.Category,
			Tier:          m.Tier,
			TokenEstimate: m.TotalTokens,
			ValueScore:    m.ValueScore,
			ValuePerToken: m.ValuePerToken,
		})
		modeTokens += m.TotalTokens
	}

	warnings := append([]string(nil), est.Warnings...)
	if est.Budget.MaxTotalTokens > 0 && !est.OverBudget {
		if float64(est.EstimatedTotalTokens) >= ensembleEstimateNearBudgetThreshold*float64(est.Budget.MaxTotalTokens) {
			warnings = append(warnings, fmt.Sprintf("estimated tokens (%d) are near budget (%d)", est.EstimatedTotalTokens, est.Budget.MaxTotalTokens))
		}
	}

	var suggestions []EnsembleEstimateSuggestion
	if est.OverBudget {
		sortedModes := make([]ensemble.ModeEstimate, len(est.Modes))
		copy(sortedModes, est.Modes)
		sort.Slice(sortedModes, func(i, j int) bool {
			return sortedModes[i].TotalTokens > sortedModes[j].TotalTokens
		})
		for _, m := range sortedModes {
			if len(m.Alternatives) == 0 {
				continue
			}
			alt := m.Alternatives[0]
			suggestions = append(suggestions, EnsembleEstimateSuggestion{
				ReplaceModeID:     m.ID,
				ReplaceModeName:   m.Name,
				ReplaceTokens:     m.TotalTokens,
				SuggestedModeID:   alt.ID,
				SuggestedModeName: alt.Name,
				SuggestedTokens:   alt.EstimatedTokens,
				SavingsTokens:     alt.Savings,
				Reason:            alt.Reason,
			})
			if len(suggestions) >= 3 {
				break
			}
		}
	}

	resp := EnsembleEstimateEnvelopeResponse{
		GeneratedAt: est.GeneratedAt,
		PresetName:  presetUsed,
		PresetLabel: presetLabel,
		OverBudget:  est.OverBudget,
		Modes:       rows,
		Budget: EnsembleEstimateBudgetSummary{
			MaxTokensPerMode:       est.Budget.MaxTokensPerMode,
			MaxTotalTokens:         est.Budget.MaxTotalTokens,
			SynthesisReserveTokens: est.Budget.SynthesisReserveTokens,
			ContextReserveTokens:   est.Budget.ContextReserveTokens,
			EstimatedModeTokens:    modeTokens,
			EstimatedTotalTokens:   est.EstimatedTotalTokens,
			ModeCount:              len(rows),
			BudgetOverride:         req.BudgetTotal,
		},
		Warnings:    warnings,
		Suggestions: suggestions,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleEnsembleRunsV1 handles GET /api/v1/ensemble/runs.
func (s *Server) handleEnsembleRunsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	rawDir := strings.TrimSpace(r.URL.Query().Get("project_dir"))
	if rawDir == "" {
		rawDir = strings.TrimSpace(r.URL.Query().Get("project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	seen := make(map[string]struct{})
	runs := make([]EnsembleRunSummary, 0, 16)

	sessions := s.listPersistedEnsembleSessions()
	for i := range sessions {
		sess := &sessions[i]
		name := strings.TrimSpace(sess.SessionName)
		if name == "" {
			continue
		}
		seen["session:"+name] = struct{}{}
		runs = append(runs, summarizeEnsembleSessionRun(sess))
	}

	for _, candidateDir := range s.ensembleCheckpointProjectCandidates(projectDir) {
		cpRoot := filepath.Join(candidateDir, ".ntm", "ensemble-checkpoints")
		info, err := os.Stat(cpRoot)
		if err != nil || !info.IsDir() {
			continue
		}
		store, err := ensemble.NewCheckpointStore(filepath.Join(candidateDir, ".ntm"))
		if err != nil {
			continue
		}
		cpRuns, err := store.ListRuns()
		if err != nil {
			continue
		}
		for _, meta := range cpRuns {
			if meta.RunID == "" {
				continue
			}
			key := "checkpoint:" + meta.RunID
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			hasSynth := false
			if _, synthErr := store.LoadSynthesisCheckpoint(meta.RunID); synthErr == nil {
				hasSynth = true
			}
			total := meta.TotalModes
			if computed := len(meta.CompletedIDs) + len(meta.PendingIDs) + len(meta.ErrorIDs); computed > total {
				total = computed
			}
			status := strings.TrimSpace(string(meta.Status))
			if status == "" {
				if len(meta.ErrorIDs) > 0 && len(meta.CompletedIDs) == 0 {
					status = "error"
				} else if len(meta.PendingIDs) == 0 && len(meta.CompletedIDs) > 0 {
					status = "ready"
				} else {
					status = "checkpoint"
				}
			}
			runs = append(runs, EnsembleRunSummary{
				RunID:          meta.RunID,
				SessionName:    meta.SessionName,
				Source:         "checkpoint",
				Question:       meta.Question,
				Status:         status,
				TotalModes:     total,
				CompletedModes: len(meta.CompletedIDs),
				PendingModes:   len(meta.PendingIDs),
				ErrorModes:     len(meta.ErrorIDs),
				HasSynthesis:   hasSynth,
				CreatedAt:      meta.CreatedAt,
				UpdatedAt:      meta.UpdatedAt,
			})
		}
	}

	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})

	resp := EnsembleRunsEnvelopeResponse{
		Runs:  runs,
		Count: len(runs),
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleEnsembleProvenanceV1 handles GET /api/v1/ensemble/sessions/{session}/provenance.
func (s *Server) handleEnsembleProvenanceV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	runID := strings.TrimSpace(chi.URLParam(r, "session"))
	if runID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "session or run identifier is required", nil, reqID)
		return
	}
	if err := validateEnsembleRunIdentifier(runID); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	rawDir := strings.TrimSpace(r.URL.Query().Get("project_dir"))
	if rawDir == "" {
		rawDir = strings.TrimSpace(r.URL.Query().Get("project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	input, err := s.loadEnsembleCompareOrProvenanceInput(r.Context(), runID, projectDir)
	if err != nil {
		var execErr *ensembleExecError
		if errors.As(err, &execErr) {
			writeEnsembleOperationError(w, execErr, reqID)
			return
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	tracker := input.Provenance
	if tracker == nil {
		tracker = ensemble.NewProvenanceTracker("", input.ModeIDs)
	}
	allChains := tracker.ListChains()
	if allChains == nil {
		allChains = []*ensemble.ProvenanceChain{}
	}
	invalidCount, valErrs := validateServedProvenanceChains(allChains)
	stats := tracker.Stats()
	if stats.ModeBreakdown == nil {
		stats.ModeBreakdown = map[string]int{}
	}
	contextHash := ""
	if len(allChains) > 0 && allChains[0] != nil {
		contextHash = allChains[0].ContextHash
	}

	findingFilter := strings.TrimSpace(r.URL.Query().Get("finding"))
	filteredChains := allChains
	var matchedChain *ensemble.ProvenanceChain
	if findingFilter != "" {
		if exact, found := tracker.GetChain(findingFilter); found {
			matchedChain = exact
			filteredChains = []*ensemble.ProvenanceChain{exact}
		} else {
			matches := make([]*ensemble.ProvenanceChain, 0, 2)
			for _, c := range allChains {
				if c != nil && strings.HasPrefix(c.FindingID, findingFilter) {
					matches = append(matches, c)
				}
			}
			if len(matches) == 0 {
				writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("finding %q not found in provenance chains for %q", findingFilter, runID), nil, reqID)
				return
			}
			matchedChain = matches[0]
			filteredChains = matches
		}
	}

	resp := EnsembleProvenanceEnvelopeResponse{
		Session:          runID,
		FindingFilter:    findingFilter,
		ContextHash:      contextHash,
		Stats:            stats,
		Chains:           filteredChains,
		Chain:            matchedChain,
		Count:            len(filteredChains),
		InvalidCount:     invalidCount,
		ValidationErrors: valErrs,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleEnsembleCompareV1 handles POST /api/v1/ensemble/compare.
func (s *Server) handleEnsembleCompareV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req EnsembleCompareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}
	runA := strings.TrimSpace(req.RunA)
	runB := strings.TrimSpace(req.RunB)
	if runA == "" || runB == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "run_a and run_b are required", nil, reqID)
		return
	}
	if err := validateEnsembleRunIdentifier(runA); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("invalid run_a: %v", err), nil, reqID)
		return
	}
	if err := validateEnsembleRunIdentifier(runB); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("invalid run_b: %v", err), nil, reqID)
		return
	}

	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, req.ProjectDir, reqID)
	if !ok {
		return
	}

	inputA, err := s.loadEnsembleCompareOrProvenanceInput(r.Context(), runA, projectDir)
	if err != nil {
		writeEnsembleOperationError(w, err, reqID)
		return
	}
	inputB, err := s.loadEnsembleCompareOrProvenanceInput(r.Context(), runB, projectDir)
	if err != nil {
		writeEnsembleOperationError(w, err, reqID)
		return
	}

	result := ensemble.Compare(*inputA, *inputB)
	if !req.Verbose && result != nil {
		result.FindingsDiff.Unchanged = nil
	}

	var sections []string
	unchanged := true
	summary := ""
	genAt := time.Now().UTC()
	if result != nil {
		unchanged = result.IsEmpty()
		summary = result.Summary
		genAt = result.GeneratedAt
		if !unchanged {
			if result.HasModeChanges() {
				sections = append(sections, "modes")
			}
			if result.HasFindingChanges() {
				sections = append(sections, "findings")
			}
			if result.HasConclusionChanges() {
				sections = append(sections, "conclusions")
			}
			if result.HasContributionChanges() {
				sections = append(sections, "contributions")
			}
		}
	}

	resp := EnsembleCompareEnvelopeResponse{
		GeneratedAt:     genAt,
		RunA:            runA,
		RunB:            runB,
		Summary:         summary,
		Unchanged:       unchanged,
		ChangedSections: sections,
		Result:          result,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// =============================================================================
// Internal Analysis Helpers
// =============================================================================

func (s *Server) resolveOptionalEnsembleAnalysisDir(w http.ResponseWriter, raw string, reqID string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		if !filepath.IsAbs(trimmed) {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, fmt.Sprintf("project directory must be an absolute path: %q", trimmed), nil, reqID)
			return "", false
		}
		resolved, err := ResolveProjectDir(trimmed)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
			return "", false
		}
		return resolved, true
	}
	if snap := strings.TrimSpace(s.projectDirSnapshot()); snap != "" {
		if resolved, err := ResolveProjectDir(snap); err == nil {
			return resolved, true
		}
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return cwd, true
	}
	return ".", true
}

func validateEnsembleRunIdentifier(id string) error {
	if _, err := ensemble.NormalizeCheckpointRunID(id); err == nil {
		return nil
	}
	if err := tmux.ValidateSessionName(id); err == nil {
		return nil
	}
	return fmt.Errorf("invalid ensemble session or run ID %q", id)
}

func (s *Server) listPersistedEnsembleSessions() []ensemble.EnsembleSession {
	seen := make(map[string]struct{})
	var merged []ensemble.EnsembleSession

	if s.stateStore != nil {
		if wrapped := ensemble.NewStateStoreFromDB(s.stateStore); wrapped != nil {
			if items, err := wrapped.List(); err == nil {
				for _, item := range items {
					if item.SessionName != "" {
						seen[item.SessionName] = struct{}{}
						merged = append(merged, item)
					}
				}
			}
		}
	}
	if items, err := ensemble.ListSessions(); err == nil {
		for _, item := range items {
			if item.SessionName == "" {
				continue
			}
			if _, exists := seen[item.SessionName]; exists {
				continue
			}
			seen[item.SessionName] = struct{}{}
			merged = append(merged, item)
		}
	}
	return merged
}

func (s *Server) loadPersistedEnsembleSession(name string) (*ensemble.EnsembleSession, error) {
	if s.stateStore != nil {
		if wrapped := ensemble.NewStateStoreFromDB(s.stateStore); wrapped != nil {
			sess, err := wrapped.Load(name)
			if err == nil && sess != nil {
				return sess, nil
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	return ensemble.LoadSession(name)
}

func summarizeEnsembleSessionRun(sess *ensemble.EnsembleSession) EnsembleRunSummary {
	var completed, pending, errCount int
	var latest time.Time
	if sess.CreatedAt.After(latest) {
		latest = sess.CreatedAt
	}
	if sess.SynthesizedAt != nil && sess.SynthesizedAt.After(latest) {
		latest = *sess.SynthesizedAt
	}
	for _, a := range sess.Assignments {
		switch a.Status {
		case ensemble.AssignmentDone:
			completed++
		case ensemble.AssignmentError:
			errCount++
		default:
			pending++
		}
		if a.CompletedAt != nil && a.CompletedAt.After(latest) {
			latest = *a.CompletedAt
		} else if a.AssignedAt.After(latest) {
			latest = a.AssignedAt
		}
	}
	live := tmux.IsInstalled() && tmux.SessionExists(sess.SessionName)
	return EnsembleRunSummary{
		RunID:             sess.SessionName,
		SessionName:       sess.SessionName,
		Source:            "session",
		Question:          sess.Question,
		PresetUsed:        sess.PresetUsed,
		Status:            sess.Status.String(),
		SynthesisStrategy: sess.SynthesisStrategy.String(),
		TotalModes:        len(sess.Assignments),
		CompletedModes:    completed,
		PendingModes:      pending,
		ErrorModes:        errCount,
		HasSynthesis:      strings.TrimSpace(sess.SynthesisOutput) != "" || sess.SynthesizedAt != nil,
		Live:              live,
		CreatedAt:         sess.CreatedAt,
		UpdatedAt:         latest,
	}
}

func (s *Server) ensembleCheckpointProjectCandidates(explicitProjectDir string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	addDir := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if abs, err := filepath.Abs(config.ExpandHome(raw)); err == nil {
			raw = abs
		}
		raw = filepath.Clean(raw)
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}

	addDir(explicitProjectDir)
	addDir(s.projectDirSnapshot())
	if cwd, err := os.Getwd(); err == nil {
		addDir(cwd)
	}

	cfg := s.loadEnsembleConfig(explicitProjectDir)
	if cfg != nil {
		projectsBase := strings.TrimSpace(config.ExpandHome(cfg.ProjectsBase))
		if projectsBase != "" {
			if entries, err := os.ReadDir(projectsBase); err == nil {
				for _, entry := range entries {
					if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
						addDir(filepath.Join(projectsBase, entry.Name()))
					}
				}
			}
		}
	}
	return out
}

func (s *Server) resolveServedCheckpointStoreForRunID(runID, explicitProjectDir string) (*ensemble.CheckpointStore, bool, error) {
	normalized, err := ensemble.NormalizeCheckpointRunID(runID)
	if err != nil {
		return nil, false, nil
	}
	matches := make([]string, 0, 2)
	for _, dir := range s.ensembleCheckpointProjectCandidates(explicitProjectDir) {
		metaPath := filepath.Join(dir, ".ntm", "ensemble-checkpoints", normalized, "_meta.json")
		if info, statErr := os.Stat(metaPath); statErr == nil && !info.IsDir() {
			matches = append(matches, dir)
		}
	}
	switch len(matches) {
	case 0:
		return nil, false, nil
	case 1:
		store, storeErr := ensemble.NewCheckpointStore(filepath.Join(matches[0], ".ntm"))
		if storeErr != nil {
			return nil, false, storeErr
		}
		return store, store.RunExists(normalized), nil
	default:
		sort.Strings(matches)
		return nil, false, &ensembleExecError{
			status:  http.StatusConflict,
			code:    ErrCodeConflict,
			message: fmt.Sprintf("checkpoint run %q is ambiguous across projects: %s", normalized, strings.Join(matches, ", ")),
		}
	}
}

func (s *Server) loadEnsembleCompareOrProvenanceInput(ctx interface{ Err() error }, runID, explicitProjectDir string) (*ensemble.CompareInput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cpStore, cpExists, cpErr := s.resolveServedCheckpointStoreForRunID(runID, explicitProjectDir)
	if cpErr != nil {
		return nil, cpErr
	}

	sessState, sessErr := s.loadPersistedEnsembleSession(runID)
	sessExists := sessErr == nil && sessState != nil
	if sessErr != nil && !errors.Is(sessErr, os.ErrNotExist) {
		return nil, &ensembleExecError{
			status:  http.StatusInternalServerError,
			code:    ErrCodeInternalError,
			message: fmt.Sprintf("load ensemble session %q: %v", runID, sessErr),
		}
	}

	if cpExists && sessExists {
		return nil, &ensembleExecError{
			status:  http.StatusConflict,
			code:    ErrCodeConflict,
			message: fmt.Sprintf("identifier %q is ambiguous: both persisted ensemble session state and checkpoint run exist", runID),
		}
	}

	if cpExists && cpStore != nil {
		meta, err := cpStore.LoadMetadata(runID)
		if err != nil {
			return nil, &ensembleExecError{
				status:  http.StatusInternalServerError,
				code:    ErrCodeInternalError,
				message: fmt.Sprintf("load checkpoint metadata for %q: %v", runID, err),
			}
		}
		outs, err := cpStore.GetCompletedOutputs(runID)
		if err != nil {
			return nil, &ensembleExecError{
				status:  http.StatusInternalServerError,
				code:    ErrCodeInternalError,
				message: fmt.Sprintf("load checkpoint outputs for %q: %v", runID, err),
			}
		}
		modeSet := make(map[string]struct{})
		for _, m := range meta.CompletedIDs {
			modeSet[m] = struct{}{}
		}
		for _, m := range meta.PendingIDs {
			modeSet[m] = struct{}{}
		}
		for _, m := range meta.ErrorIDs {
			modeSet[m] = struct{}{}
		}
		outputs := make([]ensemble.ModeOutput, 0, len(outs))
		for _, o := range outs {
			if o == nil {
				continue
			}
			modeSet[o.ModeID] = struct{}{}
			outputs = append(outputs, *o)
		}
		modeIDs := make([]string, 0, len(modeSet))
		for m := range modeSet {
			modeIDs = append(modeIDs, m)
		}
		sort.Strings(modeIDs)
		return buildServedCompareInput(runID, meta.Question, modeIDs, outputs, ""), nil
	}

	if sessExists {
		modeIDs := make([]string, 0, len(sessState.Assignments))
		for _, a := range sessState.Assignments {
			modeIDs = append(modeIDs, a.ModeID)
		}
		sort.Strings(modeIDs)
		collected, err := ensemble.CollectModeOutputs(sessState, nil)
		if err != nil {
			return nil, &ensembleExecError{
				status:  http.StatusInternalServerError,
				code:    ErrCodeInternalError,
				message: fmt.Sprintf("collect mode outputs for %q: %v", runID, err),
			}
		}
		return buildServedCompareInput(runID, sessState.Question, modeIDs, collected.Outputs, sessState.SynthesisOutput), nil
	}

	return nil, &ensembleExecError{
		status:  http.StatusNotFound,
		code:    ErrCodeNotFound,
		message: fmt.Sprintf("ensemble session or checkpoint run %q not found", runID),
	}
}

func buildServedCompareInput(runID, question string, modeIDs []string, outputs []ensemble.ModeOutput, persistedSynthesis string) *ensemble.CompareInput {
	provenance := ensemble.NewProvenanceTracker(question, modeIDs)
	var contributions *ensemble.ContributionReport
	synthesisOutput := persistedSynthesis
	if len(outputs) > 0 {
		if synth, err := ensemble.NewSynthesizer(ensemble.DefaultSynthesisConfig()); err == nil {
			if result, synthErr := synth.Synthesize(&ensemble.SynthesisInput{
				Outputs:          outputs,
				OriginalQuestion: question,
				Config:           synth.Config,
				Provenance:       provenance,
			}); synthErr == nil && result != nil {
				if synthesisOutput == "" {
					synthesisOutput = result.Summary
				}
				contributions = result.Contributions
			}
		}
	}
	return &ensemble.CompareInput{
		RunID:           runID,
		ModeIDs:         modeIDs,
		Outputs:         outputs,
		Provenance:      provenance,
		Contributions:   contributions,
		SynthesisOutput: synthesisOutput,
	}
}

func validateServedProvenanceChains(chains []*ensemble.ProvenanceChain) (int, []string) {
	invalid := 0
	var errs []string
	for _, c := range chains {
		if c == nil {
			invalid++
			errs = append(errs, "nil provenance chain")
			continue
		}
		if err := c.Validate(); err != nil {
			invalid++
			id := c.FindingID
			if id == "" {
				id = "<missing-id>"
			}
			errs = append(errs, fmt.Sprintf("%s: %v", id, err))
		}
	}
	return invalid, errs
}

func resolveEnsembleEstimateModeIDs(inputs []string, catalog *ensemble.ModeCatalog) ([]string, error) {
	if catalog == nil {
		return nil, errors.New("mode catalog is nil")
	}
	seen := make(map[string]bool, len(inputs))
	out := make([]string, 0, len(inputs))
	for _, raw := range inputs {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		var resolved *ensemble.ReasoningMode
		if mode := catalog.GetMode(token); mode != nil {
			resolved = mode
		} else if mode := catalog.GetModeByCode(strings.ToUpper(token)); mode != nil {
			resolved = mode
		}
		if resolved == nil {
			return nil, fmt.Errorf("mode %q not found (id or code)", token)
		}
		if seen[resolved.ID] {
			return nil, fmt.Errorf("duplicate mode %q", resolved.ID)
		}
		seen[resolved.ID] = true
		out = append(out, resolved.ID)
	}
	if len(out) == 0 {
		return nil, errors.New("no valid modes provided")
	}
	return out, nil
}

func mergeEnsembleBudgetDefaults(current, defaults ensemble.BudgetConfig) ensemble.BudgetConfig {
	if current.MaxTokensPerMode == 0 {
		current.MaxTokensPerMode = defaults.MaxTokensPerMode
	}
	if current.MaxTotalTokens == 0 {
		current.MaxTotalTokens = defaults.MaxTotalTokens
	}
	if current.SynthesisReserveTokens == 0 {
		current.SynthesisReserveTokens = defaults.SynthesisReserveTokens
	}
	if current.ContextReserveTokens == 0 {
		current.ContextReserveTokens = defaults.ContextReserveTokens
	}
	if current.TimeoutPerMode == 0 {
		current.TimeoutPerMode = defaults.TimeoutPerMode
	}
	if current.TotalTimeout == 0 {
		current.TotalTimeout = defaults.TotalTimeout
	}
	if current.MaxRetries == 0 {
		current.MaxRetries = defaults.MaxRetries
	}
	return current
}
