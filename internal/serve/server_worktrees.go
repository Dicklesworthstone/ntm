package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

// =============================================================================
// Worktrees Request & Response Shapes (Reflected into OpenAPI 3.1)
// =============================================================================

// WorktreesListEnvelopeResponse is the REST envelope for GET /api/v1/worktrees.
type WorktreesListEnvelopeResponse struct {
	APIResponse
	ProjectDir string                   `json:"project_dir"`
	Session    string                   `json:"session,omitempty"`
	Worktrees  []worktrees.WorktreeInfo `json:"worktrees"`
	Count      int                      `json:"count"`
}

// CreateWorktreeRequest is the JSON request body for POST /api/v1/worktrees.
type CreateWorktreeRequest struct {
	Session    string `json:"session"`
	Agent      string `json:"agent"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// WorktreeCreateEnvelopeResponse is the REST envelope for POST /api/v1/worktrees.
type WorktreeCreateEnvelopeResponse struct {
	APIResponse
	ProjectDir string                 `json:"project_dir"`
	Session    string                 `json:"session"`
	Worktree   worktrees.WorktreeInfo `json:"worktree"`
}

// MergeWorktreeRequest is the JSON request body for POST /api/v1/worktrees/{agent}/merge.
type MergeWorktreeRequest struct {
	Session        string `json:"session,omitempty"`
	Preview        bool   `json:"preview,omitempty"`
	ExpectedSource string `json:"expected_source,omitempty"`
	ExpectedTarget string `json:"expected_target,omitempty"`
	ProjectDir     string `json:"project_dir,omitempty"`
}

// WorktreeMergeEnvelopeResponse is the REST envelope for POST /api/v1/worktrees/{agent}/merge.
type WorktreeMergeEnvelopeResponse struct {
	APIResponse
	ProjectDir   string                  `json:"project_dir"`
	Session      string                  `json:"session"`
	AgentName    string                  `json:"agent_name"`
	Merged       bool                    `json:"merged"`
	SourceBranch string                  `json:"source_branch,omitempty"`
	Preview      *worktrees.MergePreview `json:"preview,omitempty"`
}

// WorktreeDeleteEnvelopeResponse is the REST envelope for DELETE /api/v1/worktrees/{agent}.
type WorktreeDeleteEnvelopeResponse struct {
	APIResponse
	ProjectDir string `json:"project_dir"`
	Session    string `json:"session"`
	AgentName  string `json:"agent_name"`
	Path       string `json:"path,omitempty"`
	BranchName string `json:"branch_name,omitempty"`
	Removed    bool   `json:"removed"`
}

// =============================================================================
// Route Registration & Handlers
// =============================================================================

// registerWorktreeRoutes mounts the /api/v1/worktrees/* REST endpoints.
func (s *Server) registerWorktreeRoutes(r chi.Router) {
	r.With(s.RequirePermission(PermReadSessions)).Get("/worktrees", s.handleListWorktreesV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/worktrees", s.handleCreateWorktreeV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/worktrees/{agent}/merge", s.handleMergeWorktreeV1)
	r.With(s.RequirePermission(PermWriteSessions)).Delete("/worktrees/{agent}", s.handleDeleteWorktreeV1)
}

func (s *Server) resolveWorktreesProjectDir(explicitDir, queryDir string) (string, error) {
	candidate := strings.TrimSpace(explicitDir)
	if candidate == "" {
		candidate = strings.TrimSpace(queryDir)
	}
	if candidate != "" {
		if !filepath.IsAbs(candidate) {
			return "", fmt.Errorf("project directory must be an absolute path: %q", candidate)
		}
		resolved, err := ResolveProjectDir(candidate)
		if err != nil {
			return "", err
		}
		return resolved, nil
	}

	fallback := strings.TrimSpace(s.projectDirSnapshot())
	if fallback == "" {
		return "", errors.New("no project directory configured; pass ?project=/abs/path or project_dir in request body")
	}
	return ResolveProjectDir(fallback)
}

func validateSafeWorktreeIdentifier(kind, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s is required", kind)
	}
	if filepath.IsAbs(trimmed) || trimmed == "." || trimmed == ".." || strings.ContainsAny(trimmed, `/\`) {
		return "", fmt.Errorf("invalid %s %q: must be a single path-safe identifier", kind, value)
	}
	return trimmed, nil
}

// resolveAgentWorktreeSession locates the owning session when session is omitted.
// If explicitSession is provided, it validates and returns it directly.
func resolveAgentWorktreeSession(ctx context.Context, projectDir, explicitSession, agentName string) (string, int, error) {
	if strings.TrimSpace(explicitSession) != "" {
		validSession, err := validateSafeWorktreeIdentifier("session", explicitSession)
		if err != nil {
			return "", http.StatusBadRequest, err
		}
		return validSession, 0, nil
	}

	root := filepath.Join(projectDir, ".ntm", "worktrees")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", http.StatusNotFound, fmt.Errorf("worktree for agent %q not found", agentName)
		}
		return "", http.StatusInternalServerError, fmt.Errorf("read worktrees directory: %w", err)
	}

	var matchedSessions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessName := entry.Name()
		if _, vErr := validateSafeWorktreeIdentifier("session", sessName); vErr != nil {
			continue
		}
		mgr := worktrees.NewManager(projectDir, sessName)
		info, gErr := mgr.GetWorktreeForAgent(ctx, agentName)
		if gErr == nil && info != nil && info.Created {
			matchedSessions = append(matchedSessions, sessName)
		}
	}
	sort.Strings(matchedSessions)

	switch len(matchedSessions) {
	case 0:
		return "", http.StatusNotFound, fmt.Errorf("worktree for agent %q not found", agentName)
	case 1:
		return matchedSessions[0], 0, nil
	default:
		return "", http.StatusConflict, fmt.Errorf(
			"agent %q exists in multiple sessions (%s); specify session explicitly",
			agentName, strings.Join(matchedSessions, ", "),
		)
	}
}

func worktreeQueryProject(r *http.Request) string {
	if p := strings.TrimSpace(r.URL.Query().Get("project")); p != "" {
		return p
	}
	return strings.TrimSpace(r.URL.Query().Get("project_dir"))
}

// handleListWorktreesV1 handles GET /api/v1/worktrees.
func (s *Server) handleListWorktreesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	projectDir, err := s.resolveWorktreesProjectDir("", worktreeQueryProject(r))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	sessionFilter := strings.TrimSpace(r.URL.Query().Get("session"))
	items := make([]worktrees.WorktreeInfo, 0)

	if sessionFilter != "" {
		validSession, vErr := validateSafeWorktreeIdentifier("session", sessionFilter)
		if vErr != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, vErr.Error(), nil, reqID)
			return
		}
		sessionFilter = validSession
		mgr := worktrees.NewManager(projectDir, sessionFilter)
		list, lErr := mgr.ListWorktrees(r.Context())
		if lErr != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, lErr.Error(), nil, reqID)
			return
		}
		for _, wt := range list {
			if wt != nil {
				items = append(items, *wt)
			}
		}
	} else {
		root := filepath.Join(projectDir, ".ntm", "worktrees")
		entries, rErr := os.ReadDir(root)
		if rErr != nil && !errors.Is(rErr, os.ErrNotExist) {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, rErr.Error(), nil, reqID)
			return
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessName := entry.Name()
			if _, vErr := validateSafeWorktreeIdentifier("session", sessName); vErr != nil {
				continue
			}
			mgr := worktrees.NewManager(projectDir, sessName)
			list, lErr := mgr.ListWorktrees(r.Context())
			if lErr != nil {
				writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, lErr.Error(), nil, reqID)
				return
			}
			for _, wt := range list {
				if wt != nil {
					items = append(items, *wt)
				}
			}
		}
	}

	resp := WorktreesListEnvelopeResponse{
		ProjectDir: projectDir,
		Session:    sessionFilter,
		Worktrees:  items,
		Count:      len(items),
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleCreateWorktreeV1 handles POST /api/v1/worktrees.
func (s *Server) handleCreateWorktreeV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req CreateWorktreeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}

	session, err := validateSafeWorktreeIdentifier("session", req.Session)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}
	agentName, err := validateSafeWorktreeIdentifier("agent", req.Agent)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	projectDir, err := s.resolveWorktreesProjectDir(req.ProjectDir, worktreeQueryProject(r))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	SetAuditSession(r, session, "", agentName)
	SetAuditResource(r, "worktree", session+"/"+agentName)

	mgr := worktrees.NewManager(projectDir, session)
	info, err := mgr.CreateForAgent(r.Context(), agentName)
	if err != nil {
		status := http.StatusInternalServerError
		code := ErrCodeInternalError
		if strings.Contains(err.Error(), "already exists on branch") || strings.Contains(err.Error(), "detached HEAD") {
			status = http.StatusConflict
			code = ErrCodeConflict
		} else if strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "invalid worktree") {
			status = http.StatusBadRequest
			code = ErrCodeInvalidRequest
		}
		writeErrorResponse(w, status, code, err.Error(), nil, reqID)
		return
	}

	resp := WorktreeCreateEnvelopeResponse{
		ProjectDir: projectDir,
		Session:    session,
		Worktree:   *info,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleMergeWorktreeV1 handles POST /api/v1/worktrees/{agent}/merge.
func (s *Server) handleMergeWorktreeV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	agentName, err := validateSafeWorktreeIdentifier("agent", chi.URLParam(r, "agent"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	var req MergeWorktreeRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
		return
	}

	projectDir, err := s.resolveWorktreesProjectDir(req.ProjectDir, worktreeQueryProject(r))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	explicitSession := strings.TrimSpace(req.Session)
	if explicitSession == "" {
		explicitSession = strings.TrimSpace(r.URL.Query().Get("session"))
	}
	session, status, err := resolveAgentWorktreeSession(r.Context(), projectDir, explicitSession, agentName)
	if err != nil {
		code := ErrCodeInvalidRequest
		if status == http.StatusNotFound {
			code = ErrCodeNotFound
		} else if status == http.StatusConflict {
			code = ErrCodeConflict
		} else if status >= 500 {
			code = ErrCodeInternalError
		}
		writeErrorResponse(w, status, code, err.Error(), nil, reqID)
		return
	}

	SetAuditSession(r, session, "", agentName)
	SetAuditResource(r, "worktree", session+"/"+agentName)

	mgr := worktrees.NewManager(projectDir, session)
	info, err := mgr.GetWorktreeForAgent(r.Context(), agentName)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if info == nil || !info.Created {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("worktree for agent %q in session %q not found", agentName, session), nil, reqID)
		return
	}

	preview, err := mgr.PreviewMerge(r.Context(), agentName)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	if req.Preview {
		resp := WorktreeMergeEnvelopeResponse{
			ProjectDir:   projectDir,
			Session:      session,
			AgentName:    agentName,
			Merged:       false,
			SourceBranch: info.BranchName,
			Preview:      preview,
		}
		payload, mErr := toJSONMap(resp)
		if mErr != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, mErr.Error(), nil, reqID)
			return
		}
		writeSuccessResponse(w, http.StatusOK, payload, reqID)
		return
	}

	var expectations []worktrees.MergeOptions
	if strings.TrimSpace(req.ExpectedSource) != "" || strings.TrimSpace(req.ExpectedTarget) != "" {
		expectations = append(expectations, worktrees.MergeOptions{
			ExpectedSource: strings.TrimSpace(req.ExpectedSource),
			ExpectedTarget: strings.TrimSpace(req.ExpectedTarget),
		})
	} else if preview != nil && preview.SourceCommit != "" && preview.TargetCommit != "" {
		expectations = append(expectations, worktrees.MergeOptions{
			ExpectedSource: preview.SourceCommit,
			ExpectedTarget: preview.TargetCommit,
		})
	}

	if err := mgr.MergeBack(r.Context(), agentName, expectations...); err != nil {
		httpStatus := http.StatusInternalServerError
		errCode := ErrCodeInternalError
		switch {
		case errors.Is(err, worktrees.ErrMergeUnsafe),
			errors.Is(err, worktrees.ErrMergeSnapshotChanged),
			errors.Is(err, worktrees.ErrMergeBusy):
			httpStatus = http.StatusConflict
			errCode = ErrCodeConflict
		case strings.Contains(err.Error(), "expected source and target must both be full commit IDs"):
			httpStatus = http.StatusBadRequest
			errCode = ErrCodeInvalidRequest
		}
		writeErrorResponse(w, httpStatus, errCode, err.Error(), nil, reqID)
		return
	}

	resp := WorktreeMergeEnvelopeResponse{
		ProjectDir:   projectDir,
		Session:      session,
		AgentName:    agentName,
		Merged:       true,
		SourceBranch: info.BranchName,
		Preview:      preview,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleDeleteWorktreeV1 handles DELETE /api/v1/worktrees/{agent}.
func (s *Server) handleDeleteWorktreeV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	agentName, err := validateSafeWorktreeIdentifier("agent", chi.URLParam(r, "agent"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	projectDir, err := s.resolveWorktreesProjectDir("", worktreeQueryProject(r))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error(), nil, reqID)
		return
	}

	force := false
	if rawForce := strings.TrimSpace(r.URL.Query().Get("force")); rawForce != "" {
		parsedForce, pErr := strconv.ParseBool(rawForce)
		if pErr != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest,
				fmt.Sprintf("invalid force query parameter %q: expected boolean", rawForce), nil, reqID)
			return
		}
		force = parsedForce
	}

	explicitSession := strings.TrimSpace(r.URL.Query().Get("session"))
	session, status, err := resolveAgentWorktreeSession(r.Context(), projectDir, explicitSession, agentName)
	if err != nil {
		code := ErrCodeInvalidRequest
		if status == http.StatusNotFound {
			code = ErrCodeNotFound
		} else if status == http.StatusConflict {
			code = ErrCodeConflict
		} else if status >= 500 {
			code = ErrCodeInternalError
		}
		writeErrorResponse(w, status, code, err.Error(), nil, reqID)
		return
	}

	SetAuditSession(r, session, "", agentName)
	SetAuditResource(r, "worktree", session+"/"+agentName)

	mgr := worktrees.NewManager(projectDir, session)
	info, err := mgr.GetWorktreeForAgent(r.Context(), agentName)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if info == nil || !info.Created {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("worktree for agent %q in session %q not found", agentName, session), nil, reqID)
		return
	}

	if !force {
		risk, riskErr := mgr.AssessRemovalRisk(r.Context(), agentName)
		if riskErr != nil {
			writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
				fmt.Sprintf("cannot verify worktree cleanliness for %q: %v", agentName, riskErr),
				map[string]interface{}{"hint": "Pass ?force=true to force worktree removal"}, reqID)
			return
		}
		if risk.HasWork() {
			writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
				fmt.Sprintf("worktree %q has uncommitted or unmerged work", agentName),
				map[string]interface{}{
					"uncommitted_files": risk.UncommittedFiles,
					"unmerged_commits":  risk.UnmergedCommits,
					"hint":              "Merge work first or pass ?force=true to discard unmerged worktree changes",
				}, reqID)
			return
		}
	}

	if err := mgr.RemoveWorktree(r.Context(), agentName); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	resp := WorktreeDeleteEnvelopeResponse{
		ProjectDir: projectDir,
		Session:    session,
		AgentName:  agentName,
		Path:       info.Path,
		BranchName: info.BranchName,
		Removed:    true,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}
