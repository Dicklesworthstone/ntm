// Package serve provides REST API endpoints for account management.
package serve

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/go-chi/chi/v5"
)

// AccountsConfig holds configuration for account management endpoints.
type AccountsConfig struct {
	// AutoRotateEnabled controls whether auto-rotation is enabled.
	AutoRotateEnabled bool `json:"auto_rotate_enabled"`

	// AutoRotateCooldownSeconds is the cooldown between auto-rotations.
	AutoRotateCooldownSeconds int `json:"auto_rotate_cooldown_seconds"`

	// AutoRotateOnRateLimit triggers rotation on rate limit detection.
	AutoRotateOnRateLimit bool `json:"auto_rotate_on_rate_limit"`
}

// AccountRotationEvent records a rotation event for history.
type AccountRotationEvent struct {
	Timestamp       string `json:"timestamp"`
	Provider        string `json:"provider"`
	PreviousAccount string `json:"previous_account,omitempty"`
	NewAccount      string `json:"new_account,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Automatic       bool   `json:"automatic"`
	Success         bool   `json:"success"`
	Error           string `json:"error,omitempty"`
}

// accountsState holds runtime state for accounts endpoints.
type accountsState struct {
	mu      sync.RWMutex
	config  AccountsConfig
	history []AccountRotationEvent
}

var accountState = &accountsState{
	config: AccountsConfig{
		AutoRotateEnabled:         false,
		AutoRotateCooldownSeconds: 300,
		AutoRotateOnRateLimit:     true,
	},
	history: make([]AccountRotationEvent, 0),
}

// registerAccountsRoutes registers account management routes.
func (s *Server) registerAccountsRoutes(r chi.Router) {
	r.Route("/accounts", func(r chi.Router) {
		// List all accounts
		r.With(s.RequirePermission(PermReadAccounts)).Get("/", s.handleListAccountsV1)

		// Get account status/quota summary
		r.With(s.RequirePermission(PermReadAccounts)).Get("/status", s.handleAccountStatusV1)

		// Get active account for each provider
		r.With(s.RequirePermission(PermReadAccounts)).Get("/active", s.handleActiveAccountsV1)

		// Get quota information
		r.With(s.RequirePermission(PermReadAccounts)).Get("/quota", s.handleAccountQuotaV1)

		// Auto-rotate configuration
		r.With(s.RequirePermission(PermReadAccounts)).Get("/auto-rotate", s.handleGetAutoRotateConfigV1)
		r.With(s.RequirePermission(PermWriteAccounts)).Patch("/auto-rotate", s.handlePatchAutoRotateConfigV1)

		// Rotation history
		r.With(s.RequirePermission(PermReadAccounts)).Get("/history", s.handleAccountHistoryV1)

		// Rotate/switch accounts
		r.With(s.RequirePermission(PermWriteAccounts)).Post("/rotate", s.handleRotateAccountV1)

		// Provider-specific endpoints
		r.With(s.RequirePermission(PermReadAccounts)).Get("/{provider}", s.handleListAccountsByProviderV1)
		r.With(s.RequirePermission(PermWriteAccounts)).Post("/{provider}/rotate", s.handleRotateProviderAccountV1)
	})
}

// handleListAccountsV1 returns all accounts across all providers.
// GET /api/v1/accounts
func (s *Server) handleListAccountsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	output, err := robot.GetAccountsList(robot.AccountsListOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Check if robot mode returned an error
	if !output.Success {
		statusCode := http.StatusInternalServerError
		if output.ErrorCode == robot.ErrCodeDependencyMissing {
			statusCode = http.StatusServiceUnavailable
		}
		writeErrorResponse(w, statusCode, output.ErrorCode, output.Error, map[string]interface{}{
			"hint": output.Hint,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"accounts": output.Accounts,
	}, reqID)
}

// handleAccountStatusV1 returns account status information per provider.
// GET /api/v1/accounts/status
func (s *Server) handleAccountStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	output, err := robot.GetAccountStatus(robot.AccountStatusOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	if !output.Success {
		statusCode := http.StatusInternalServerError
		if output.ErrorCode == robot.ErrCodeDependencyMissing {
			statusCode = http.StatusServiceUnavailable
		}
		writeErrorResponse(w, statusCode, output.ErrorCode, output.Error, map[string]interface{}{
			"hint": output.Hint,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"accounts": output.Accounts,
	}, reqID)
}

// handleActiveAccountsV1 returns the active account for each provider.
// GET /api/v1/accounts/active
func (s *Server) handleActiveAccountsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	output, err := robot.GetAccountsList(robot.AccountsListOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	if !output.Success {
		statusCode := http.StatusInternalServerError
		if output.ErrorCode == robot.ErrCodeDependencyMissing {
			statusCode = http.StatusServiceUnavailable
		}
		writeErrorResponse(w, statusCode, output.ErrorCode, output.Error, map[string]interface{}{
			"hint": output.Hint,
		}, reqID)
		return
	}

	// Filter to only active accounts
	activeAccounts := make(map[string]robot.AccountInfo)
	for _, acc := range output.Accounts {
		if acc.Current {
			activeAccounts[acc.Provider] = acc
		}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"active": activeAccounts,
	}, reqID)
}

// handleAccountQuotaV1 returns quota information for all providers.
// GET /api/v1/accounts/quota
func (s *Server) handleAccountQuotaV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	output, err := robot.GetAccountStatus(robot.AccountStatusOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	if !output.Success {
		statusCode := http.StatusInternalServerError
		if output.ErrorCode == robot.ErrCodeDependencyMissing {
			statusCode = http.StatusServiceUnavailable
		}
		writeErrorResponse(w, statusCode, output.ErrorCode, output.Error, map[string]interface{}{
			"hint": output.Hint,
		}, reqID)
		return
	}

	// Build quota summary
	type quotaInfo struct {
		Provider          string `json:"provider"`
		Current           string `json:"current"`
		UsagePercent      int    `json:"usage_percent"`
		LimitReset        string `json:"limit_reset,omitempty"`
		AvailableAccounts int    `json:"available_accounts"`
		RateLimited       bool   `json:"rate_limited"`
	}

	quotas := make([]quotaInfo, 0, len(output.Accounts))
	for provider, status := range output.Accounts {
		quotas = append(quotas, quotaInfo{
			Provider:          provider,
			Current:           status.Current,
			UsagePercent:      status.UsagePercent,
			LimitReset:        status.LimitReset,
			AvailableAccounts: status.AvailableAccounts,
			RateLimited:       status.RateLimited,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"quotas": quotas,
	}, reqID)
}

// handleGetAutoRotateConfigV1 returns auto-rotate configuration.
// GET /api/v1/accounts/auto-rotate
func (s *Server) handleGetAutoRotateConfigV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	accountState.mu.RLock()
	config := accountState.config
	accountState.mu.RUnlock()

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"config": config,
	}, reqID)
}

// handlePatchAutoRotateConfigV1 updates auto-rotate configuration.
// PATCH /api/v1/accounts/auto-rotate
func (s *Server) handlePatchAutoRotateConfigV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var patch struct {
		AutoRotateEnabled         *bool `json:"auto_rotate_enabled,omitempty"`
		AutoRotateCooldownSeconds *int  `json:"auto_rotate_cooldown_seconds,omitempty"`
		AutoRotateOnRateLimit     *bool `json:"auto_rotate_on_rate_limit,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON body", nil, reqID)
		return
	}

	accountState.mu.Lock()
	if patch.AutoRotateEnabled != nil {
		accountState.config.AutoRotateEnabled = *patch.AutoRotateEnabled
	}
	if patch.AutoRotateCooldownSeconds != nil {
		if *patch.AutoRotateCooldownSeconds < 60 {
			accountState.mu.Unlock()
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "auto_rotate_cooldown_seconds must be at least 60", nil, reqID)
			return
		}
		accountState.config.AutoRotateCooldownSeconds = *patch.AutoRotateCooldownSeconds
	}
	if patch.AutoRotateOnRateLimit != nil {
		accountState.config.AutoRotateOnRateLimit = *patch.AutoRotateOnRateLimit
	}
	config := accountState.config
	accountState.mu.Unlock()

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"config": config,
	}, reqID)
}

// handleAccountHistoryV1 returns account rotation history.
// GET /api/v1/accounts/history
func (s *Server) handleAccountHistoryV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Get optional limit query parameter
	limitStr := r.URL.Query().Get("limit")
	limit := 50 // default
	if limitStr != "" {
		if parsed, err := json.Number(limitStr).Int64(); err == nil && parsed > 0 {
			limit = int(parsed)
		}
	}

	accountState.mu.RLock()
	history := accountState.history
	accountState.mu.RUnlock()

	// Return most recent events first, limited
	if len(history) > limit {
		history = history[len(history)-limit:]
	}

	// Reverse to show most recent first
	reversed := make([]AccountRotationEvent, len(history))
	for i, e := range history {
		reversed[len(history)-1-i] = e
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"history": reversed,
		"total":   len(accountState.history),
	}, reqID)
}

// handleRotateAccountV1 performs an account rotation.
// POST /api/v1/accounts/rotate
func (s *Server) handleRotateAccountV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req struct {
		Provider  string `json:"provider"`
		AccountID string `json:"account_id,omitempty"`
		Reason    string `json:"reason,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON body", nil, reqID)
		return
	}

	if req.Provider == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "provider is required", nil, reqID)
		return
	}

	output, err := robot.GetSwitchAccount(robot.SwitchAccountOptions{
		Provider:  req.Provider,
		AccountID: req.AccountID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Record in history
	event := AccountRotationEvent{
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Provider:        req.Provider,
		PreviousAccount: output.Switch.PreviousAccount,
		NewAccount:      output.Switch.NewAccount,
		Reason:          req.Reason,
		Automatic:       false,
		Success:         output.Switch.Success,
		Error:           output.Switch.Error,
	}

	accountState.mu.Lock()
	accountState.history = append(accountState.history, event)
	// Keep only last 1000 events
	if len(accountState.history) > 1000 {
		accountState.history = accountState.history[len(accountState.history)-1000:]
	}
	accountState.mu.Unlock()

	// Publish WebSocket event for rotation
	if s.wsHub != nil {
		s.wsHub.Publish("accounts:"+req.Provider, "account.rotated", map[string]interface{}{
			"provider":         req.Provider,
			"previous_account": output.Switch.PreviousAccount,
			"new_account":      output.Switch.NewAccount,
			"success":          output.Switch.Success,
			"reason":           req.Reason,
			"automatic":        false,
		})
	}

	if !output.Success {
		statusCode := http.StatusInternalServerError
		if output.ErrorCode == robot.ErrCodeDependencyMissing {
			statusCode = http.StatusServiceUnavailable
		}
		writeErrorResponse(w, statusCode, output.ErrorCode, output.Error, map[string]interface{}{
			"switch": output.Switch,
			"hint":   output.Hint,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"switch": output.Switch,
	}, reqID)
}

// handleListAccountsByProviderV1 returns accounts for a specific provider.
// GET /api/v1/accounts/{provider}
func (s *Server) handleListAccountsByProviderV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	provider := chi.URLParam(r, "provider")

	if provider == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "provider is required", nil, reqID)
		return
	}

	output, err := robot.GetAccountsList(robot.AccountsListOptions{
		Provider: provider,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	if !output.Success {
		statusCode := http.StatusInternalServerError
		if output.ErrorCode == robot.ErrCodeDependencyMissing {
			statusCode = http.StatusServiceUnavailable
		}
		writeErrorResponse(w, statusCode, output.ErrorCode, output.Error, map[string]interface{}{
			"hint": output.Hint,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"provider": provider,
		"accounts": output.Accounts,
	}, reqID)
}

// handleRotateProviderAccountV1 rotates to the next account for a specific provider.
// POST /api/v1/accounts/{provider}/rotate
func (s *Server) handleRotateProviderAccountV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	provider := chi.URLParam(r, "provider")

	if provider == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "provider is required", nil, reqID)
		return
	}

	var req struct {
		AccountID string `json:"account_id,omitempty"`
		Reason    string `json:"reason,omitempty"`
	}

	// Request body is optional
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON body", nil, reqID)
			return
		}
	}

	output, err := robot.GetSwitchAccount(robot.SwitchAccountOptions{
		Provider:  provider,
		AccountID: req.AccountID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Record in history
	event := AccountRotationEvent{
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Provider:        provider,
		PreviousAccount: output.Switch.PreviousAccount,
		NewAccount:      output.Switch.NewAccount,
		Reason:          req.Reason,
		Automatic:       false,
		Success:         output.Switch.Success,
		Error:           output.Switch.Error,
	}

	accountState.mu.Lock()
	accountState.history = append(accountState.history, event)
	if len(accountState.history) > 1000 {
		accountState.history = accountState.history[len(accountState.history)-1000:]
	}
	accountState.mu.Unlock()

	// Publish WebSocket event for rotation
	if s.wsHub != nil {
		s.wsHub.Publish("accounts:"+provider, "account.rotated", map[string]interface{}{
			"provider":         provider,
			"previous_account": output.Switch.PreviousAccount,
			"new_account":      output.Switch.NewAccount,
			"success":          output.Switch.Success,
			"reason":           req.Reason,
			"automatic":        false,
		})
	}

	if !output.Success {
		statusCode := http.StatusInternalServerError
		if output.ErrorCode == robot.ErrCodeDependencyMissing {
			statusCode = http.StatusServiceUnavailable
		}
		writeErrorResponse(w, statusCode, output.ErrorCode, output.Error, map[string]interface{}{
			"switch": output.Switch,
			"hint":   output.Hint,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"switch": output.Switch,
	}, reqID)
}
