// Package serve provides REST API handlers for safety and policy management.
package serve

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/policy"
)

// registerSafetyRoutes registers all safety and policy related routes.
func (s *Server) registerSafetyRoutes(r chi.Router) {
	// Safety API - destructive command protection
	r.Route("/safety", func(r chi.Router) {
		r.With(s.RequirePermission(PermReadHealth)).Get("/status", s.handleSafetyStatusV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/blocked", s.handleSafetyBlockedV1)
		r.With(s.RequirePermission(PermReadHealth)).Post("/check", s.handleSafetyCheckV1)
		r.With(s.RequirePermission(PermDangerousOps)).Post("/install", s.handleSafetyInstallV1)
		r.With(s.RequirePermission(PermDangerousOps)).Post("/uninstall", s.handleSafetyUninstallV1)
	})

	// Policy API - policy management
	r.Route("/policy", func(r chi.Router) {
		r.With(s.RequirePermission(PermReadHealth)).Get("/", s.handlePolicyGetV1)
		r.With(s.RequirePermission(PermSystemConfig)).Put("/", s.handlePolicyUpdateV1)
		r.With(s.RequirePermission(PermReadHealth)).Post("/validate", s.handlePolicyValidateV1)
		r.With(s.RequirePermission(PermSystemConfig)).Post("/reset", s.handlePolicyResetV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/automation", s.handlePolicyAutomationGetV1)
		r.With(s.RequirePermission(PermSystemConfig)).Put("/automation", s.handlePolicyAutomationUpdateV1)
	})

	// Approvals API - dangerous action gating
	r.Route("/approvals", func(r chi.Router) {
		r.With(s.RequirePermission(PermReadApprovals)).Get("/", s.handleApprovalsListV1)
		r.With(s.RequirePermission(PermReadApprovals)).Get("/history", s.handleApprovalsHistoryV1)
		r.With(s.RequirePermission(PermReadApprovals)).Get("/{id}", s.handleApprovalGetV1)
		r.With(s.RequirePermission(PermApproveRequests)).Post("/{id}/approve", s.handleApprovalApproveV1)
		r.With(s.RequirePermission(PermApproveRequests)).Post("/{id}/deny", s.handleApprovalDenyV1)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/request", s.handleApprovalRequestV1)
	})
}

// SafetyStatusResponse is the REST response for safety status.
type SafetyStatusResponse struct {
	Installed     bool   `json:"installed"`
	PolicyPath    string `json:"policy_path,omitempty"`
	BlockedCount  int    `json:"blocked_rules"`
	ApprovalCount int    `json:"approval_rules"`
	AllowedCount  int    `json:"allowed_rules"`
	WrapperPath   string `json:"wrapper_path,omitempty"`
	HookInstalled bool   `json:"hook_installed"`
}

func (s *Server) handleSafetyStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	home, err := os.UserHomeDir()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to get home directory", nil, reqID)
		return
	}

	ntmDir := filepath.Join(home, ".ntm")
	wrapperDir := filepath.Join(ntmDir, "bin")

	// Check if wrappers are installed
	gitWrapper := filepath.Join(wrapperDir, "git")
	wrapperInstalled := safetyFileExists(gitWrapper)

	// Check if Claude Code hook is installed
	hookPath := filepath.Join(home, ".claude", "hooks", "PreToolUse", "ntm-safety.sh")
	hookInstalled := safetyFileExists(hookPath)

	// Load policy
	p, err := policy.LoadOrDefault()
	var blocked, approval, allowed int
	var policyPath string
	if err == nil {
		blocked, approval, allowed = p.Stats()
		customPath := filepath.Join(ntmDir, "policy.yaml")
		if safetyFileExists(customPath) {
			policyPath = customPath
		}
	}

	resp := SafetyStatusResponse{
		Installed:     wrapperInstalled || hookInstalled,
		PolicyPath:    policyPath,
		BlockedCount:  blocked,
		ApprovalCount: approval,
		AllowedCount:  allowed,
		WrapperPath:   wrapperDir,
		HookInstalled: hookInstalled,
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// SafetyBlockedResponse is the REST response for blocked commands.
type SafetyBlockedResponse struct {
	Entries []policy.BlockedEntry `json:"entries"`
	Count   int                   `json:"count"`
}

func (s *Server) handleSafetyBlockedV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Parse query params
	hours := 24
	limit := 100

	if h := r.URL.Query().Get("hours"); h != "" {
		if v, err := strconv.Atoi(h); err == nil && v > 0 {
			hours = v
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	entries, err := policy.RecentBlocked("", hours)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to read blocked log", nil, reqID)
		return
	}

	// Limit entries
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}

	resp := SafetyBlockedResponse{
		Entries: entries,
		Count:   len(entries),
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// SafetyCheckRequest is the request to check a command against policy.
type SafetyCheckRequest struct {
	Command string `json:"command"`
}

// SafetyCheckResponse is the REST response for safety check.
type SafetyCheckResponse struct {
	Command string `json:"command"`
	Action  string `json:"action"` // allow, block, approve
	Pattern string `json:"pattern,omitempty"`
	Reason  string `json:"reason,omitempty"`
	SLB     bool   `json:"slb,omitempty"` // Requires SLB two-person approval
}

func (s *Server) handleSafetyCheckV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req SafetyCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	if req.Command == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"command is required", nil, reqID)
		return
	}

	p, err := policy.LoadOrDefault()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to load policy", nil, reqID)
		return
	}

	match := p.Check(req.Command)

	resp := SafetyCheckResponse{
		Command: req.Command,
		Action:  "allow",
	}

	if match != nil {
		resp.Action = string(match.Action)
		resp.Pattern = match.Pattern
		resp.Reason = match.Reason
		resp.SLB = match.SLB
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// SafetyInstallRequest configures safety installation options.
type SafetyInstallRequest struct {
	Force bool `json:"force"`
}

// SafetyInstallResponse is the REST response for safety install.
type SafetyInstallResponse struct {
	GitWrapper string `json:"git_wrapper"`
	RmWrapper  string `json:"rm_wrapper"`
	Hook       string `json:"hook"`
	Policy     string `json:"policy"`
}

func (s *Server) handleSafetyInstallV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req SafetyInstallRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				"invalid request body", nil, reqID)
			return
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to get home directory", nil, reqID)
		return
	}

	ntmDir := filepath.Join(home, ".ntm")
	binDir := filepath.Join(ntmDir, "bin")
	logsDir := filepath.Join(ntmDir, "logs")

	// Create directories
	for _, dir := range []string{ntmDir, binDir, logsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				fmt.Sprintf("failed to create directory %s", dir), nil, reqID)
			return
		}
	}

	// Install git wrapper
	gitWrapper := filepath.Join(binDir, "git")
	if err := installWrapperFile(gitWrapper, gitWrapperScript, req.Force); err != nil {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		return
	}

	// Install rm wrapper
	rmWrapper := filepath.Join(binDir, "rm")
	if err := installWrapperFile(rmWrapper, rmWrapperScript, req.Force); err != nil {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		return
	}

	// Install Claude Code hook
	hookDir := filepath.Join(home, ".claude", "hooks", "PreToolUse")
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create hook directory", nil, reqID)
		return
	}

	hookPath := filepath.Join(hookDir, "ntm-safety.sh")
	if err := installWrapperFile(hookPath, claudeHookScript, req.Force); err != nil {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		return
	}

	// Create default policy file if it doesn't exist
	policyPath := filepath.Join(ntmDir, "policy.yaml")
	if !safetyFileExists(policyPath) || req.Force {
		if err := writeDefaultPolicyFile(policyPath); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to write policy file", nil, reqID)
			return
		}
	}

	resp := SafetyInstallResponse{
		GitWrapper: gitWrapper,
		RmWrapper:  rmWrapper,
		Hook:       hookPath,
		Policy:     policyPath,
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// SafetyUninstallResponse is the REST response for safety uninstall.
type SafetyUninstallResponse struct {
	Removed []string `json:"removed"`
}

func (s *Server) handleSafetyUninstallV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	home, err := os.UserHomeDir()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to get home directory", nil, reqID)
		return
	}

	var removed []string

	// Remove wrappers
	binDir := filepath.Join(home, ".ntm", "bin")
	for _, name := range []string{"git", "rm"} {
		path := filepath.Join(binDir, name)
		if safetyFileExists(path) {
			if err := os.Remove(path); err != nil {
				writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
					fmt.Sprintf("failed to remove %s", path), nil, reqID)
				return
			}
			removed = append(removed, path)
		}
	}

	// Remove hook
	hookPath := filepath.Join(home, ".claude", "hooks", "PreToolUse", "ntm-safety.sh")
	if safetyFileExists(hookPath) {
		if err := os.Remove(hookPath); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to remove hook", nil, reqID)
			return
		}
		removed = append(removed, hookPath)
	}

	resp := SafetyUninstallResponse{
		Removed: removed,
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// PolicyGetResponse is the REST response for policy get.
type PolicyGetResponse struct {
	Version    int                     `json:"version"`
	PolicyPath string                  `json:"policy_path,omitempty"`
	IsDefault  bool                    `json:"is_default"`
	Stats      PolicyStatsResponse     `json:"stats"`
	Automation policy.AutomationConfig `json:"automation"`
	Rules      *PolicyRulesResponse    `json:"rules,omitempty"`
}

// PolicyStatsResponse contains rule counts.
type PolicyStatsResponse struct {
	Blocked  int `json:"blocked"`
	Approval int `json:"approval"`
	Allowed  int `json:"allowed"`
	SLBRules int `json:"slb_rules"`
}

// PolicyRulesResponse contains detailed rule information.
type PolicyRulesResponse struct {
	Blocked          []PolicyRuleSummary `json:"blocked,omitempty"`
	ApprovalRequired []PolicyRuleSummary `json:"approval_required,omitempty"`
	Allowed          []PolicyRuleSummary `json:"allowed,omitempty"`
}

// PolicyRuleSummary is a simplified rule representation.
type PolicyRuleSummary struct {
	Pattern string `json:"pattern"`
	Reason  string `json:"reason,omitempty"`
	SLB     bool   `json:"slb,omitempty"`
}

func (s *Server) handlePolicyGetV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Check if full rules are requested
	includeRules := r.URL.Query().Get("rules") == "true"

	home, err := os.UserHomeDir()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to get home directory", nil, reqID)
		return
	}
	policyPath := filepath.Join(home, ".ntm", "policy.yaml")

	p, err := policy.LoadOrDefault()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to load policy", nil, reqID)
		return
	}

	isDefault := !safetyFileExists(policyPath)
	blocked, approval, allowed := p.Stats()

	// Count SLB rules
	slbCount := 0
	for _, r := range p.ApprovalRequired {
		if r.SLB {
			slbCount++
		}
	}

	resp := PolicyGetResponse{
		Version:    p.Version,
		IsDefault:  isDefault,
		Automation: p.Automation,
		Stats: PolicyStatsResponse{
			Blocked:  blocked,
			Approval: approval,
			Allowed:  allowed,
			SLBRules: slbCount,
		},
	}

	if !isDefault {
		resp.PolicyPath = policyPath
	}

	if includeRules {
		resp.Rules = &PolicyRulesResponse{
			Blocked:          toPolicyRuleSummaries(p.Blocked),
			ApprovalRequired: toPolicyRuleSummaries(p.ApprovalRequired),
			Allowed:          toPolicyRuleSummaries(p.Allowed),
		}
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

func toPolicyRuleSummaries(rules []policy.Rule) []PolicyRuleSummary {
	result := make([]PolicyRuleSummary, len(rules))
	for i, r := range rules {
		result[i] = PolicyRuleSummary{
			Pattern: r.Pattern,
			Reason:  r.Reason,
			SLB:     r.SLB,
		}
	}
	return result
}

// PolicyUpdateRequest is the request to update the policy.
type PolicyUpdateRequest struct {
	Content string `json:"content"` // YAML content
}

// PolicyUpdateResponse is the REST response for policy update.
type PolicyUpdateResponse struct {
	PolicyPath string              `json:"policy_path"`
	Stats      PolicyStatsResponse `json:"stats"`
}

func (s *Server) handlePolicyUpdateV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req PolicyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	if req.Content == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"content is required", nil, reqID)
		return
	}

	// Validate the YAML
	var p policy.Policy
	if err := yaml.Unmarshal([]byte(req.Content), &p); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid YAML: %v", err), nil, reqID)
		return
	}

	if err := p.Validate(); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("validation failed: %v", err), nil, reqID)
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to get home directory", nil, reqID)
		return
	}

	policyPath := filepath.Join(home, ".ntm", "policy.yaml")

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(policyPath), 0755); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create directory", nil, reqID)
		return
	}

	if err := os.WriteFile(policyPath, []byte(req.Content), 0644); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to write policy file", nil, reqID)
		return
	}

	blocked, approval, allowed := p.Stats()

	resp := PolicyUpdateResponse{
		PolicyPath: policyPath,
		Stats: PolicyStatsResponse{
			Blocked:  blocked,
			Approval: approval,
			Allowed:  allowed,
		},
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// PolicyValidateRequest is the request to validate a policy.
type PolicyValidateRequest struct {
	Content string `json:"content,omitempty"` // YAML content to validate (optional, uses file if not provided)
}

// PolicyValidateResponse is the REST response for policy validation.
type PolicyValidateResponse struct {
	Valid      bool     `json:"valid"`
	PolicyPath string   `json:"policy_path,omitempty"`
	Errors     []string `json:"errors,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

func (s *Server) handlePolicyValidateV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req PolicyValidateRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				"invalid request body", nil, reqID)
			return
		}
	}

	var errors []string
	var warnings []string
	var policyPath string

	if req.Content != "" {
		// Validate provided content
		var p policy.Policy
		if err := yaml.Unmarshal([]byte(req.Content), &p); err != nil {
			errors = append(errors, fmt.Sprintf("invalid YAML: %v", err))
			data, _ := toJSONMap(PolicyValidateResponse{
				Valid:    false,
				Errors:   errors,
				Warnings: warnings,
			})
			writeSuccessResponse(w, http.StatusOK, data, reqID)
			return
		}

		if p.Version == 0 {
			warnings = append(warnings, "no version specified, defaulting to 1")
		}

		if err := p.Validate(); err != nil {
			errors = append(errors, err.Error())
		}

		blocked, approval, allowed := p.Stats()
		if blocked == 0 && approval == 0 && allowed == 0 {
			warnings = append(warnings, "policy has no rules defined")
		}

		data, _ := toJSONMap(PolicyValidateResponse{
			Valid:    len(errors) == 0,
			Errors:   errors,
			Warnings: warnings,
		})
		writeSuccessResponse(w, http.StatusOK, data, reqID)
		return
	}

	// Validate file-based policy
	home, err := os.UserHomeDir()
	if err != nil {
		errors = append(errors, fmt.Sprintf("failed to get home directory: %v", err))
		data, _ := toJSONMap(PolicyValidateResponse{
			Valid:  false,
			Errors: errors,
		})
		writeSuccessResponse(w, http.StatusOK, data, reqID)
		return
	}

	policyPath = filepath.Join(home, ".ntm", "policy.yaml")

	if !safetyFileExists(policyPath) {
		errors = append(errors, "policy file does not exist")
		data, _ := toJSONMap(PolicyValidateResponse{
			Valid:      false,
			PolicyPath: policyPath,
			Errors:     errors,
		})
		writeSuccessResponse(w, http.StatusOK, data, reqID)
		return
	}

	fileData, err := os.ReadFile(policyPath)
	if err != nil {
		errors = append(errors, fmt.Sprintf("cannot read file: %v", err))
		data, _ := toJSONMap(PolicyValidateResponse{
			Valid:      false,
			PolicyPath: policyPath,
			Errors:     errors,
		})
		writeSuccessResponse(w, http.StatusOK, data, reqID)
		return
	}

	var p policy.Policy
	if err := yaml.Unmarshal(fileData, &p); err != nil {
		errors = append(errors, fmt.Sprintf("invalid YAML: %v", err))
		data, _ := toJSONMap(PolicyValidateResponse{
			Valid:      false,
			PolicyPath: policyPath,
			Errors:     errors,
		})
		writeSuccessResponse(w, http.StatusOK, data, reqID)
		return
	}

	if p.Version == 0 {
		warnings = append(warnings, "no version specified, defaulting to 1")
	}

	if err := p.Validate(); err != nil {
		errors = append(errors, err.Error())
	}

	blocked, approval, allowed := p.Stats()
	if blocked == 0 && approval == 0 && allowed == 0 {
		warnings = append(warnings, "policy has no rules defined")
	}

	data, _ := toJSONMap(PolicyValidateResponse{
		Valid:      len(errors) == 0,
		PolicyPath: policyPath,
		Errors:     errors,
		Warnings:   warnings,
	})
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// PolicyResetResponse is the REST response for policy reset.
type PolicyResetResponse struct {
	PolicyPath string `json:"policy_path"`
	Action     string `json:"action"`
}

func (s *Server) handlePolicyResetV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	home, err := os.UserHomeDir()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to get home directory", nil, reqID)
		return
	}

	policyPath := filepath.Join(home, ".ntm", "policy.yaml")

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(policyPath), 0755); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create directory", nil, reqID)
		return
	}

	// Write default policy
	if err := writeDefaultPolicyFile(policyPath); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to write policy file", nil, reqID)
		return
	}

	resp := PolicyResetResponse{
		PolicyPath: policyPath,
		Action:     "reset",
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AutomationGetResponse is the REST response for automation settings.
type AutomationGetResponse struct {
	AutoCommit   bool   `json:"auto_commit"`
	AutoPush     bool   `json:"auto_push"`
	ForceRelease string `json:"force_release"`
}

func (s *Server) handlePolicyAutomationGetV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	p, err := policy.LoadOrDefault()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to load policy", nil, reqID)
		return
	}

	resp := AutomationGetResponse{
		AutoCommit:   p.Automation.AutoCommit,
		AutoPush:     p.Automation.AutoPush,
		ForceRelease: p.ForceReleasePolicy(),
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AutomationUpdateRequest is the request to update automation settings.
type AutomationUpdateRequest struct {
	AutoCommit   *bool   `json:"auto_commit,omitempty"`
	AutoPush     *bool   `json:"auto_push,omitempty"`
	ForceRelease *string `json:"force_release,omitempty"`
}

// AutomationUpdateResponse is the REST response for automation update.
type AutomationUpdateResponse struct {
	AutoCommit   bool   `json:"auto_commit"`
	AutoPush     bool   `json:"auto_push"`
	ForceRelease string `json:"force_release"`
	Modified     bool   `json:"modified"`
}

func (s *Server) handlePolicyAutomationUpdateV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req AutomationUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to get home directory", nil, reqID)
		return
	}
	policyPath := filepath.Join(home, ".ntm", "policy.yaml")

	// Load existing or create default
	var p *policy.Policy
	if safetyFileExists(policyPath) {
		p, err = policy.Load(policyPath)
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to load policy", nil, reqID)
			return
		}
	} else {
		p = policy.DefaultPolicy()
	}

	// Apply changes
	modified := false
	if req.AutoCommit != nil && *req.AutoCommit != p.Automation.AutoCommit {
		p.Automation.AutoCommit = *req.AutoCommit
		modified = true
	}
	if req.AutoPush != nil && *req.AutoPush != p.Automation.AutoPush {
		p.Automation.AutoPush = *req.AutoPush
		modified = true
	}
	if req.ForceRelease != nil {
		switch *req.ForceRelease {
		case "never", "approval", "auto":
			if *req.ForceRelease != p.Automation.ForceRelease {
				p.Automation.ForceRelease = *req.ForceRelease
				modified = true
			}
		default:
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				fmt.Sprintf("invalid force_release value: %q (must be never, approval, or auto)", *req.ForceRelease), nil, reqID)
			return
		}
	}

	if modified {
		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(policyPath), 0755); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to create directory", nil, reqID)
			return
		}

		// Generate and write policy
		content := generatePolicyYAMLFromPolicy(p)
		if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to write policy", nil, reqID)
			return
		}
	}

	resp := AutomationUpdateResponse{
		AutoCommit:   p.Automation.AutoCommit,
		AutoPush:     p.Automation.AutoPush,
		ForceRelease: p.ForceReleasePolicy(),
		Modified:     modified,
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// Approval represents a pending approval request.
type Approval struct {
	ID          string    `json:"id"`
	Action      string    `json:"action"`       // The action requiring approval
	Resource    string    `json:"resource"`     // The resource being acted on
	Requestor   string    `json:"requestor"`    // Who requested the action
	Reason      string    `json:"reason"`       // Why approval is needed
	SLBRequired bool      `json:"slb_required"` // Whether SLB two-person approval is needed
	Status      string    `json:"status"`       // pending, approved, denied, expired
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	ApprovedBy  string    `json:"approved_by,omitempty"`
	ApprovedAt  time.Time `json:"approved_at,omitempty"`
}

// In-memory approval store (in production, this would be persisted)
var (
	approvals     = make(map[string]*Approval)
	approvalsLock sync.RWMutex
	approvalIDSeq int64
)

// ApprovalsListResponse is the REST response for approvals list.
type ApprovalsListResponse struct {
	Approvals []Approval `json:"approvals"`
	Count     int        `json:"count"`
}

func (s *Server) handleApprovalsListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Filter by status
	status := r.URL.Query().Get("status")

	approvalsLock.Lock()
	defer approvalsLock.Unlock()

	var result []Approval
	now := time.Now()

	for _, a := range approvals {
		// Check if expired (now safe to modify since we hold the write lock)
		if a.Status == "pending" && now.After(a.ExpiresAt) {
			a.Status = "expired"
		}

		// Filter by status
		if status != "" && a.Status != status {
			continue
		}

		result = append(result, *a)
	}

	resp := ApprovalsListResponse{
		Approvals: result,
		Count:     len(result),
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

func (s *Server) handleApprovalsHistoryV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Parse limit
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	approvalsLock.RLock()
	defer approvalsLock.RUnlock()

	var result []Approval
	for _, a := range approvals {
		if a.Status != "pending" {
			result = append(result, *a)
		}
	}

	// Sort by CreatedAt descending (most recent first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	// Limit results (take the first N, which are the most recent)
	if len(result) > limit {
		result = result[:limit]
	}

	resp := ApprovalsListResponse{
		Approvals: result,
		Count:     len(result),
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

func (s *Server) handleApprovalGetV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	id := chi.URLParam(r, "id")
	if id == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"approval ID is required", nil, reqID)
		return
	}

	approvalsLock.Lock()
	approval, ok := approvals[id]
	if !ok {
		approvalsLock.Unlock()
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("approval '%s' not found", id), nil, reqID)
		return
	}

	// Check if expired (safe to modify since we hold the write lock)
	if approval.Status == "pending" && time.Now().After(approval.ExpiresAt) {
		approval.Status = "expired"
	}
	// Copy the approval data while holding the lock
	approvalCopy := *approval
	approvalsLock.Unlock()

	data, err := toJSONMap(approvalCopy)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// ApprovalDecisionResponse is the REST response for approval decision.
type ApprovalDecisionResponse struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Decision string `json:"decision"`
}

func (s *Server) handleApprovalApproveV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	id := chi.URLParam(r, "id")
	if id == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"approval ID is required", nil, reqID)
		return
	}

	// Get approver identity from RBAC context
	rc := RoleFromContext(r.Context())
	approver := "unknown"
	if rc != nil {
		approver = rc.UserID
	}

	approvalsLock.Lock()
	approval, ok := approvals[id]
	if !ok {
		approvalsLock.Unlock()
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("approval '%s' not found", id), nil, reqID)
		return
	}

	// Check if expired
	if time.Now().After(approval.ExpiresAt) {
		approval.Status = "expired"
		approvalsLock.Unlock()
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
			"approval has expired", nil, reqID)
		return
	}

	if approval.Status != "pending" {
		approvalsLock.Unlock()
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
			fmt.Sprintf("approval is not pending (status: %s)", approval.Status), nil, reqID)
		return
	}

	// Check SLB requirement (approver can't be the requestor)
	if approval.SLBRequired && approver == approval.Requestor {
		approvalsLock.Unlock()
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
			"SLB two-person approval required: approver cannot be the requestor", nil, reqID)
		return
	}

	approval.Status = "approved"
	approval.ApprovedBy = approver
	approval.ApprovedAt = time.Now()
	approvalsLock.Unlock()

	log.Printf("Approval %s approved by %s", id, approver)
	s.publishApprovalEvent("approval.resolved", map[string]interface{}{
		"approval_id": id,
		"decision":    "approved",
		"approved_by": approver,
	})

	resp := ApprovalDecisionResponse{
		ID:       id,
		Status:   approval.Status,
		Decision: "approved",
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

func (s *Server) handleApprovalDenyV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	id := chi.URLParam(r, "id")
	if id == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"approval ID is required", nil, reqID)
		return
	}

	// Get denier identity from RBAC context
	rc := RoleFromContext(r.Context())
	denier := "unknown"
	if rc != nil {
		denier = rc.UserID
	}

	approvalsLock.Lock()
	approval, ok := approvals[id]
	if !ok {
		approvalsLock.Unlock()
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("approval '%s' not found", id), nil, reqID)
		return
	}

	if approval.Status != "pending" {
		approvalsLock.Unlock()
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
			fmt.Sprintf("approval is not pending (status: %s)", approval.Status), nil, reqID)
		return
	}

	approval.Status = "denied"
	approval.ApprovedBy = denier
	approval.ApprovedAt = time.Now()
	approvalsLock.Unlock()

	log.Printf("Approval %s denied by %s", id, denier)
	s.publishApprovalEvent("approval.resolved", map[string]interface{}{
		"approval_id": id,
		"decision":    "denied",
		"approved_by": denier,
	})

	resp := ApprovalDecisionResponse{
		ID:       id,
		Status:   approval.Status,
		Decision: "denied",
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// ApprovalRequestRequest is the request to create a new approval request.
type ApprovalRequestRequest struct {
	Action     string `json:"action"`
	Resource   string `json:"resource"`
	Reason     string `json:"reason,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"` // Default 3600 (1 hour)
}

// ApprovalRequestResponse is the REST response for creating an approval request.
type ApprovalRequestResponse struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	ExpiresAt   time.Time `json:"expires_at"`
	SLBRequired bool      `json:"slb_required"`
}

func (s *Server) handleApprovalRequestV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req ApprovalRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	if req.Action == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"action is required", nil, reqID)
		return
	}

	// Get requestor identity from RBAC context
	rc := RoleFromContext(r.Context())
	requestor := "unknown"
	if rc != nil {
		requestor = rc.UserID
	}

	// Check if this action requires SLB approval
	p, _ := policy.LoadOrDefault()
	slbRequired := p.NeedsSLBApproval(req.Action)

	// Default TTL
	ttl := 3600
	if req.TTLSeconds > 0 {
		ttl = req.TTLSeconds
	}

	approvalsLock.Lock()
	approvalIDSeq++
	id := fmt.Sprintf("apr-%d", approvalIDSeq)

	approval := &Approval{
		ID:          id,
		Action:      req.Action,
		Resource:    req.Resource,
		Requestor:   requestor,
		Reason:      req.Reason,
		SLBRequired: slbRequired,
		Status:      "pending",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(time.Duration(ttl) * time.Second),
	}
	approvals[id] = approval
	approvalsLock.Unlock()

	log.Printf("Approval request %s created by %s for action '%s'", id, requestor, req.Action)
	s.publishApprovalEvent("approval.requested", map[string]interface{}{
		"approval_id":  id,
		"action":       approval.Action,
		"resource":     approval.Resource,
		"requestor":    approval.Requestor,
		"reason":       approval.Reason,
		"slb_required": approval.SLBRequired,
		"status":       approval.Status,
		"expires_at":   approval.ExpiresAt.Format(time.RFC3339Nano),
		"requested_at": approval.CreatedAt.Format(time.RFC3339Nano),
	})

	resp := ApprovalRequestResponse{
		ID:          id,
		Status:      approval.Status,
		ExpiresAt:   approval.ExpiresAt,
		SLBRequired: slbRequired,
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

func (s *Server) publishApprovalEvent(eventType string, payload map[string]interface{}) {
	if s.wsHub == nil {
		return
	}
	s.wsHub.Publish("approvals:*", eventType, payload)
}

// Helper functions

func safetyFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func installWrapperFile(path, content string, force bool) error {
	if safetyFileExists(path) && !force {
		return fmt.Errorf("%s already exists (use force=true to overwrite)", path)
	}

	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

func writeDefaultPolicyFile(path string) error {
	content := `# NTM Policy Configuration
# Version 1 - with automation settings and SLB support
version: 1

# Automation settings
automation:
  auto_commit: true        # Allow automatic git commits
  auto_push: false         # Require explicit git push
  force_release: approval  # "never", "approval", or "auto" for file reservation force-release

# Explicitly allowed patterns (checked first - highest priority)
allowed:
  - pattern: 'git\s+push\s+.*--force-with-lease'
    reason: "Safe force push with lease protection"
  - pattern: 'git\s+reset\s+--soft'
    reason: "Soft reset preserves changes"
  - pattern: 'git\s+reset\s+HEAD~?\d*$'
    reason: "Mixed reset preserves working directory"

# Blocked patterns (dangerous operations)
blocked:
  - pattern: 'git\s+reset\s+--hard'
    reason: "Hard reset loses uncommitted changes"
  - pattern: 'git\s+clean\s+-fd'
    reason: "Removes untracked files permanently"
  - pattern: 'git\s+push\s+.*--force'
    reason: "Force push can overwrite remote history"
  - pattern: 'git\s+push\s+.*\s-f(\s|$)'
    reason: "Force push can overwrite remote history"
  - pattern: 'git\s+push\s+-f(\s|$)'
    reason: "Force push can overwrite remote history"
  - pattern: 'rm\s+-rf\s+/$'
    reason: "Recursive delete of root is catastrophic"
  - pattern: 'rm\s+-rf\s+~'
    reason: "Recursive delete of home directory"
  - pattern: 'rm\s+-rf\s+\*'
    reason: "Recursive delete of everything in current directory"
  - pattern: 'git\s+branch\s+-D'
    reason: "Force delete branch loses unmerged work"
  - pattern: 'git\s+stash\s+drop'
    reason: "Dropping stash loses saved work"
  - pattern: 'git\s+stash\s+clear'
    reason: "Clearing all stashes loses saved work"

# Approval required patterns (need confirmation)
approval_required:
  - pattern: 'git\s+rebase\s+-i'
    reason: "Interactive rebase rewrites history"
  - pattern: 'git\s+commit\s+--amend'
    reason: "Amending rewrites history"
  - pattern: 'rm\s+-rf\s+\S'
    reason: "Recursive force delete"
  - pattern: 'force_release'
    reason: "Force release another agent's reservation"
    slb: true  # Requires two-person approval
`
	return os.WriteFile(path, []byte(content), 0644)
}

func generatePolicyYAMLFromPolicy(p *policy.Policy) string {
	var sb strings.Builder

	sb.WriteString("# NTM Policy Configuration\n")
	sb.WriteString(fmt.Sprintf("version: %d\n\n", p.Version))

	sb.WriteString("automation:\n")
	sb.WriteString(fmt.Sprintf("  auto_commit: %v\n", p.Automation.AutoCommit))
	sb.WriteString(fmt.Sprintf("  auto_push: %v\n", p.Automation.AutoPush))
	sb.WriteString(fmt.Sprintf("  force_release: %s\n\n", p.ForceReleasePolicy()))

	if len(p.Allowed) > 0 {
		sb.WriteString("allowed:\n")
		for _, r := range p.Allowed {
			sb.WriteString(fmt.Sprintf("  - pattern: '%s'\n", safetyEscapeYAMLSingleQuote(r.Pattern)))
			if r.Reason != "" {
				sb.WriteString(fmt.Sprintf("    reason: \"%s\"\n", safetyEscapeYAMLDoubleQuote(r.Reason)))
			}
		}
		sb.WriteString("\n")
	}

	if len(p.Blocked) > 0 {
		sb.WriteString("blocked:\n")
		for _, r := range p.Blocked {
			sb.WriteString(fmt.Sprintf("  - pattern: '%s'\n", safetyEscapeYAMLSingleQuote(r.Pattern)))
			if r.Reason != "" {
				sb.WriteString(fmt.Sprintf("    reason: \"%s\"\n", safetyEscapeYAMLDoubleQuote(r.Reason)))
			}
		}
		sb.WriteString("\n")
	}

	if len(p.ApprovalRequired) > 0 {
		sb.WriteString("approval_required:\n")
		for _, r := range p.ApprovalRequired {
			sb.WriteString(fmt.Sprintf("  - pattern: '%s'\n", safetyEscapeYAMLSingleQuote(r.Pattern)))
			if r.Reason != "" {
				sb.WriteString(fmt.Sprintf("    reason: \"%s\"\n", safetyEscapeYAMLDoubleQuote(r.Reason)))
			}
			if r.SLB {
				sb.WriteString("    slb: true\n")
			}
		}
	}

	return sb.String()
}

func safetyEscapeYAMLSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func safetyEscapeYAMLDoubleQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

// Wrapper scripts (same as CLI)

const gitWrapperScript = `#!/bin/bash
# NTM Safety Wrapper for git
# Intercepts destructive git commands

REAL_GIT=$(which -a git | grep -v "$HOME/.ntm/bin" | head -1)
if [ -z "$REAL_GIT" ]; then
    REAL_GIT="/usr/bin/git"
fi

# Check command against policy (include "git" in the command string)
check_result=$(ntm safety check "git $*" --json 2>&1)
exit_code=$?

# ntm safety check exits 0 for allow/approve, 1 for block
if [ $exit_code -eq 1 ]; then
    # Command was blocked
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)
    echo "NTM Safety: Command blocked" >&2
    echo "  Reason: $reason" >&2
    echo "  Command: git $*" >&2

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg cmd "git $*" \
              --arg reason "${reason:-Policy violation}" \
              '{timestamp: $ts, command: $cmd, reason: $reason, action: "block"}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"block\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
    fi

    exit 1
fi

# Pass through to real git
exec "$REAL_GIT" "$@"
`

const rmWrapperScript = `#!/bin/bash
# NTM Safety Wrapper for rm
# Intercepts destructive rm commands

REAL_RM=$(which -a rm | grep -v "$HOME/.ntm/bin" | head -1)
if [ -z "$REAL_RM" ]; then
    REAL_RM="/bin/rm"
fi

# Check command against policy
check_result=$(ntm safety check "rm $*" --json 2>&1)
exit_code=$?

# ntm safety check exits 0 for allow/approve, 1 for block
if [ $exit_code -eq 1 ]; then
    # Command was blocked
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)
    echo "NTM Safety: Command blocked" >&2
    echo "  Reason: $reason" >&2
    echo "  Command: rm $*" >&2

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg cmd "rm $*" \
              --arg reason "${reason:-Policy violation}" \
              '{timestamp: $ts, command: $cmd, reason: $reason, action: "block"}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"block\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
    fi

    exit 1
fi

# Pass through to real rm
exec "$REAL_RM" "$@"
`

const claudeHookScript = `#!/bin/bash
# NTM Safety Hook for Claude Code
# PreToolUse hook that validates Bash commands

# Only process Bash tool calls
TOOL_NAME="${CLAUDE_TOOL_NAME:-}"
if [ "$TOOL_NAME" != "Bash" ]; then
    exit 0
fi

# Get the command from the tool input
COMMAND="${CLAUDE_TOOL_INPUT_command:-}"
if [ -z "$COMMAND" ]; then
    exit 0
fi

# Check against policy
check_result=$(ntm safety check "$COMMAND" --json 2>&1)
exit_code=$?

# ntm safety check exits 0 for allow/approve, 1 for block
if [ $exit_code -eq 1 ]; then
    # Command was blocked
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    session="${NTM_SESSION:-unknown}"
    agent="${CLAUDE_AGENT_TYPE:-claude}"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg session "$session" \
              --arg agent "$agent" \
              --arg cmd "$COMMAND" \
              --arg reason "${reason:-Policy violation}" \
              '{timestamp: $ts, session: $session, agent: $agent, command: $cmd, reason: $reason, action: "block"}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"block\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
    fi

    # Return error to Claude Code
    echo "BLOCKED: $reason"
    exit 1
fi

exit 0
`
