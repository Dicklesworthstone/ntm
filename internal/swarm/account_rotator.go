package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// agentToProvider maps agent type aliases to caam provider names.
var agentToProvider = map[string]string{
	"cc":          "claude",
	"claude":      "claude",
	"claude-code": "claude",
	"cod":         "openai",
	"codex":       "openai",
	"gmi":         "google",
	"gemini":      "google",
}

// AccountInfo describes a caam account.
type AccountInfo struct {
	Provider    string    `json:"provider"`
	AccountName string    `json:"account_name"`
	IsActive    bool      `json:"is_active"`
	LastUsed    time.Time `json:"last_used,omitempty"`
}

// RotationRecord tracks an account rotation.
type RotationRecord struct {
	Provider    string    `json:"provider"`
	FromAccount string    `json:"from_account"`
	ToAccount   string    `json:"to_account"`
	RotatedAt   time.Time `json:"rotated_at"`
	SessionPane string    `json:"session_pane"`
	TriggeredBy string    `json:"triggered_by"` // "limit_hit", "manual"
}

// caamStatus represents the JSON output from caam status command.
type caamStatus struct {
	Provider      string `json:"provider"`
	ActiveAccount string `json:"active_account"`
	AccountCount  int    `json:"account_count,omitempty"`
}

// caamAccount represents an account in caam list output.
type caamAccount struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// RotationState tracks per-pane account rotation state.
type RotationState struct {
	CurrentAccount   string    `json:"current_account"`
	PreviousAccounts []string  `json:"previous_accounts"`
	RotationCount    int       `json:"rotation_count"`
	LastRotation     time.Time `json:"last_rotation"`
}

// AccountRotator manages account rotation via caam CLI.
type AccountRotator struct {
	// caamPath is the path to caam binary (default: "caam").
	caamPath string

	// Logger for structured logging.
	Logger *slog.Logger

	// CommandTimeout is the timeout for caam commands (default: 5s).
	CommandTimeout time.Duration

	// CooldownDuration is the minimum time between rotations for a pane (default: 60s).
	CooldownDuration time.Duration

	// rotationHistory tracks rotations.
	rotationHistory []RotationRecord

	// rotationStates tracks per-pane rotation state.
	rotationStates map[string]*RotationState

	// mu protects history and internal state.
	mu sync.Mutex

	// availabilityChecked tracks if we've checked caam availability.
	availabilityChecked bool
	availabilityResult  bool
}

// NewAccountRotator creates a new AccountRotator with default settings.
func NewAccountRotator() *AccountRotator {
	return &AccountRotator{
		caamPath:         "caam",
		Logger:           slog.Default(),
		CommandTimeout:   5 * time.Second,
		CooldownDuration: 60 * time.Second,
		rotationHistory:  make([]RotationRecord, 0),
		rotationStates:   make(map[string]*RotationState),
	}
}

// WithCaamPath sets a custom caam binary path.
func (r *AccountRotator) WithCaamPath(path string) *AccountRotator {
	r.caamPath = path
	return r
}

// WithLogger sets a custom logger.
func (r *AccountRotator) WithLogger(logger *slog.Logger) *AccountRotator {
	r.Logger = logger
	return r
}

// WithCommandTimeout sets the command timeout.
func (r *AccountRotator) WithCommandTimeout(timeout time.Duration) *AccountRotator {
	r.CommandTimeout = timeout
	return r
}

// WithCooldown sets the minimum duration between rotations for a given pane.
func (r *AccountRotator) WithCooldown(d time.Duration) *AccountRotator {
	r.CooldownDuration = d
	return r
}

// logger returns the configured logger or the default logger.
func (r *AccountRotator) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// normalizeProvider converts agent type to caam provider name.
func normalizeProvider(agentType string) string {
	if provider, ok := agentToProvider[agentType]; ok {
		return provider
	}
	// Return as-is if not in map (might already be provider name)
	return agentType
}

// IsAvailable checks if caam CLI is installed and working.
func (r *AccountRotator) IsAvailable() bool {
	r.mu.Lock()
	if r.availabilityChecked {
		result := r.availabilityResult
		r.mu.Unlock()
		return result
	}
	r.mu.Unlock()

	// Check if caam binary exists
	path, err := exec.LookPath(r.caamPath)
	if err != nil {
		r.logger().Warn("[AccountRotator] caam_unavailable",
			"error", "caam binary not found",
			"path", r.caamPath)
		r.mu.Lock()
		r.availabilityChecked = true
		r.availabilityResult = false
		r.mu.Unlock()
		return false
	}

	r.logger().Debug("[AccountRotator] caam_found", "path", path)

	r.mu.Lock()
	r.availabilityChecked = true
	r.availabilityResult = true
	r.mu.Unlock()
	return true
}

// ResetAvailabilityCheck clears the cached availability check result.
func (r *AccountRotator) ResetAvailabilityCheck() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.availabilityChecked = false
	r.availabilityResult = false
}

// GetCurrentAccount returns the active account for a provider/agent type.
func (r *AccountRotator) GetCurrentAccount(agentType string) (*AccountInfo, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("caam CLI not available")
	}

	provider := normalizeProvider(agentType)

	ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
	defer cancel()

	output, err := r.runCaamCommand(ctx, "status", "--provider", provider, "--json")
	if err != nil {
		r.logger().Error("[AccountRotator] get_current_failed",
			"provider", provider,
			"error", err)
		return nil, fmt.Errorf("caam status failed: %w", err)
	}

	var status caamStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		return nil, fmt.Errorf("parse caam status: %w", err)
	}

	info := &AccountInfo{
		Provider:    status.Provider,
		AccountName: status.ActiveAccount,
		IsActive:    true,
	}

	r.logger().Info("[AccountRotator] get_current",
		"provider", provider,
		"account", status.ActiveAccount)

	return info, nil
}

// ListAccounts returns all accounts for a provider/agent type.
func (r *AccountRotator) ListAccounts(agentType string) ([]AccountInfo, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("caam CLI not available")
	}

	provider := normalizeProvider(agentType)

	ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
	defer cancel()

	output, err := r.runCaamCommand(ctx, "list", "--provider", provider, "--json")
	if err != nil {
		r.logger().Error("[AccountRotator] list_accounts_failed",
			"provider", provider,
			"error", err)
		return nil, fmt.Errorf("caam list failed: %w", err)
	}

	var accounts []caamAccount
	if err := json.Unmarshal([]byte(output), &accounts); err != nil {
		return nil, fmt.Errorf("parse caam list: %w", err)
	}

	result := make([]AccountInfo, len(accounts))
	for i, acc := range accounts {
		result[i] = AccountInfo{
			Provider:    provider,
			AccountName: acc.Name,
			IsActive:    acc.Active,
		}
	}

	r.logger().Info("[AccountRotator] list_accounts",
		"provider", provider,
		"count", len(result))

	return result, nil
}

// SwitchAccount switches to the next available account.
// Returns the rotation record on success.
func (r *AccountRotator) SwitchAccount(agentType string) (*RotationRecord, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("caam CLI not available")
	}

	provider := normalizeProvider(agentType)

	// Get current account before switch
	currentInfo, err := r.GetCurrentAccount(agentType)
	fromAccount := ""
	if err == nil && currentInfo != nil {
		fromAccount = currentInfo.AccountName
	}

	r.logger().Info("[AccountRotator] switch_start",
		"provider", provider,
		"from", fromAccount)

	ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
	defer cancel()

	start := time.Now()
	_, err = r.runCaamCommand(ctx, "switch", "--provider", provider, "--next")
	if err != nil {
		r.logger().Error("[AccountRotator] switch_failed",
			"provider", provider,
			"error", err)
		return nil, fmt.Errorf("caam switch failed: %w", err)
	}

	// Get new account after switch
	newInfo, err := r.GetCurrentAccount(agentType)
	toAccount := ""
	if err == nil && newInfo != nil {
		toAccount = newInfo.AccountName
	}

	duration := time.Since(start)

	record := &RotationRecord{
		Provider:    provider,
		FromAccount: fromAccount,
		ToAccount:   toAccount,
		RotatedAt:   time.Now(),
		TriggeredBy: "limit_hit",
	}

	r.mu.Lock()
	r.rotationHistory = append(r.rotationHistory, *record)
	r.mu.Unlock()

	r.logger().Info("[AccountRotator] switch_complete",
		"provider", provider,
		"from", fromAccount,
		"to", toAccount,
		"duration", duration)

	return record, nil
}

// SwitchToAccount switches to a specific account.
func (r *AccountRotator) SwitchToAccount(agentType, accountName string) (*RotationRecord, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("caam CLI not available")
	}

	provider := normalizeProvider(agentType)

	// Get current account before switch
	currentInfo, err := r.GetCurrentAccount(agentType)
	fromAccount := ""
	if err == nil && currentInfo != nil {
		fromAccount = currentInfo.AccountName
	}

	r.logger().Info("[AccountRotator] switch_to_start",
		"provider", provider,
		"from", fromAccount,
		"to", accountName)

	ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
	defer cancel()

	start := time.Now()
	_, err = r.runCaamCommand(ctx, "switch", "--provider", provider, "--account", accountName)
	if err != nil {
		r.logger().Error("[AccountRotator] switch_to_failed",
			"provider", provider,
			"account", accountName,
			"error", err)
		return nil, fmt.Errorf("caam switch failed: %w", err)
	}

	duration := time.Since(start)

	record := &RotationRecord{
		Provider:    provider,
		FromAccount: fromAccount,
		ToAccount:   accountName,
		RotatedAt:   time.Now(),
		TriggeredBy: "manual",
	}

	r.mu.Lock()
	r.rotationHistory = append(r.rotationHistory, *record)
	r.mu.Unlock()

	r.logger().Info("[AccountRotator] switch_to_complete",
		"provider", provider,
		"from", fromAccount,
		"to", accountName,
		"duration", duration)

	return record, nil
}

// GetRotationHistory returns recent rotation records.
func (r *AccountRotator) GetRotationHistory(limit int) []RotationRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	if limit <= 0 || limit > len(r.rotationHistory) {
		limit = len(r.rotationHistory)
	}

	// Return most recent records
	start := len(r.rotationHistory) - limit
	if start < 0 {
		start = 0
	}

	result := make([]RotationRecord, limit)
	copy(result, r.rotationHistory[start:])
	return result
}

// ClearRotationHistory clears all rotation history.
func (r *AccountRotator) ClearRotationHistory() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rotationHistory = make([]RotationRecord, 0)
}

// RotationCount returns the total number of rotations recorded.
func (r *AccountRotator) RotationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rotationHistory)
}

// OnLimitHit handles a limit detection event by rotating the account for the
// affected pane. It tracks per-pane rotation state and enforces cooldown to
// prevent rapid rotation loops. Returns the rotation record on success, or
// an error if rotation was skipped (cooldown active, caam unavailable, etc.).
func (r *AccountRotator) OnLimitHit(event LimitHitEvent) (*RotationRecord, error) {
	r.mu.Lock()
	state := r.getOrCreateState(event.SessionPane)
	r.mu.Unlock()

	r.logger().Info("[AccountRotator] limit_hit_received",
		"session_pane", event.SessionPane,
		"agent_type", event.AgentType,
		"pattern", event.Pattern,
		"current_account", state.CurrentAccount,
		"rotation_count", state.RotationCount)

	// Check cooldown
	if r.isCooldownActive(state) {
		elapsed := time.Since(state.LastRotation)
		r.logger().Warn("[AccountRotator] cooldown_active",
			"session_pane", event.SessionPane,
			"last_rotation", state.LastRotation,
			"elapsed", elapsed,
			"cooldown", r.CooldownDuration)
		return nil, fmt.Errorf("cooldown active for pane %s: %v remaining",
			event.SessionPane, r.CooldownDuration-elapsed)
	}

	if !r.IsAvailable() {
		r.logger().Error("[AccountRotator] caam_unavailable_on_limit_hit",
			"session_pane", event.SessionPane)
		return nil, fmt.Errorf("caam CLI not available")
	}

	// Perform the rotation
	record, err := r.SwitchAccount(event.AgentType)
	if err != nil {
		r.logger().Error("[AccountRotator] rotation_failed_on_limit_hit",
			"session_pane", event.SessionPane,
			"agent_type", event.AgentType,
			"error", err)
		return nil, fmt.Errorf("rotation failed: %w", err)
	}

	// Update per-pane state
	r.mu.Lock()
	record.SessionPane = event.SessionPane
	if state.CurrentAccount == "" && record.FromAccount != "" {
		state.CurrentAccount = record.FromAccount
	}
	if state.CurrentAccount != "" {
		state.PreviousAccounts = append(state.PreviousAccounts, state.CurrentAccount)
	}
	state.CurrentAccount = record.ToAccount
	state.RotationCount++
	state.LastRotation = time.Now()
	r.mu.Unlock()

	r.logger().Info("[AccountRotator] rotation_complete_on_limit_hit",
		"session_pane", event.SessionPane,
		"from_account", record.FromAccount,
		"to_account", record.ToAccount,
		"total_rotations", state.RotationCount)

	return record, nil
}

// GetPaneState returns the rotation state for a specific pane.
// Returns nil if no state exists for the pane.
func (r *AccountRotator) GetPaneState(sessionPane string) *RotationState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rotationStates[sessionPane]
}

// isCooldownActive checks whether the pane is within the cooldown window.
func (r *AccountRotator) isCooldownActive(state *RotationState) bool {
	if state.RotationCount == 0 {
		return false
	}
	return time.Since(state.LastRotation) < r.CooldownDuration
}

// getOrCreateState returns or initializes the rotation state for a pane.
// Caller must hold r.mu.
func (r *AccountRotator) getOrCreateState(sessionPane string) *RotationState {
	if state, ok := r.rotationStates[sessionPane]; ok {
		return state
	}
	state := &RotationState{
		PreviousAccounts: make([]string, 0),
	}
	r.rotationStates[sessionPane] = state
	return state
}

// runCaamCommand executes a caam command and returns its output.
func (r *AccountRotator) runCaamCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.caamPath, args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("caam %v: exit %d: %s", args, exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("caam %v: %w", args, err)
	}
	return string(output), nil
}

// RotateAccount implements the AccountRotator interface used by AutoRespawner.
// This is an alias for SwitchAccount that returns just the new account name.
func (r *AccountRotator) RotateAccount(agentType string) (newAccount string, err error) {
	record, err := r.SwitchAccount(agentType)
	if err != nil {
		return "", err
	}
	return record.ToAccount, nil
}

// CurrentAccount implements the AccountRotator interface used by AutoRespawner.
// Returns the current account name for the agent type.
func (r *AccountRotator) CurrentAccount(agentType string) string {
	info, err := r.GetCurrentAccount(agentType)
	if err != nil {
		return ""
	}
	return info.AccountName
}
