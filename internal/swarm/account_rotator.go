package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// AccountInfo describes a caam account.
type AccountInfo struct {
	Provider      string    `json:"provider"`
	AccountName   string    `json:"account_name"`
	Email         string    `json:"email,omitempty"`
	IsActive      bool      `json:"is_active"`
	RateLimited   bool      `json:"rate_limited,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	LastUsed      time.Time `json:"last_used,omitempty"`
}

// RotationRecord tracks an account rotation.
type RotationRecord struct {
	Provider       string        `json:"provider"`
	AgentType      string        `json:"agent_type,omitempty"`
	Project        string        `json:"project,omitempty"`
	FromAccount    string        `json:"from_account"`
	ToAccount      string        `json:"to_account"`
	RotatedAt      time.Time     `json:"rotated_at"`
	SessionPane    string        `json:"session_pane"`
	TriggeredBy    string        `json:"triggered_by"` // "limit_hit", "manual"
	TriggerPattern string        `json:"trigger_pattern,omitempty"`
	TimeSinceLast  time.Duration `json:"time_since_last,omitempty"`
	// PaneLocal is true when the rotation repopulated only this pane's isolated
	// CODEX_HOME (never the global ~/.codex/auth.json). The caller should restart
	// only this pane.
	PaneLocal bool `json:"pane_local,omitempty"`
	// CodexHome is the isolated CODEX_HOME directory that was repopulated for a
	// pane-local Codex rotation.
	CodexHome string `json:"codex_home,omitempty"`
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

type AccountRotationStats struct {
	AgentType        string         `json:"agent_type"`
	TotalRotations   int            `json:"total_rotations"`
	AvgTimeBetween   time.Duration  `json:"avg_time_between,omitempty"`
	AccountUsage     map[string]int `json:"account_usage,omitempty"`
	UniquePanes      int            `json:"unique_panes,omitempty"`
	UniquePanesByKey map[string]int `json:"-"`
}

type persistedRotationHistory struct {
	History map[string][]RotationRecord `json:"history,omitempty"`
}

// AccountRotationHistory tracks all account rotations with optional persistence.
// Persistence file: <dataDir>/.ntm/rotation_history.json
type AccountRotationHistory struct {
	mu      sync.RWMutex
	dataDir string
	history map[string][]RotationRecord // sessionPane -> records
	logger  *slog.Logger
}

func NewAccountRotationHistory(dataDir string, logger *slog.Logger) *AccountRotationHistory {
	return &AccountRotationHistory{
		dataDir: dataDir,
		history: make(map[string][]RotationRecord),
		logger:  logger,
	}
}

func (h *AccountRotationHistory) WithLogger(logger *slog.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logger = logger
}

func (h *AccountRotationHistory) SetDataDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dataDir = dir
}

func (h *AccountRotationHistory) RecordRotation(record RotationRecord) error {
	if record.SessionPane == "" {
		return nil
	}
	if record.RotatedAt.IsZero() {
		record.RotatedAt = time.Now()
	}

	h.mu.Lock()
	paneHistory := h.history[record.SessionPane]
	if record.TimeSinceLast == 0 && len(paneHistory) > 0 {
		last := paneHistory[len(paneHistory)-1]
		if !last.RotatedAt.IsZero() {
			record.TimeSinceLast = record.RotatedAt.Sub(last.RotatedAt)
		}
	}
	h.history[record.SessionPane] = append(paneHistory, record)
	total := len(h.history[record.SessionPane])
	logger := h.logger
	dataDir := h.dataDir
	h.mu.Unlock()

	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("[AccountRotationHistory] rotation_recorded",
		"session_pane", record.SessionPane,
		"agent_type", record.AgentType,
		"provider", record.Provider,
		"from_account", record.FromAccount,
		"to_account", record.ToAccount,
		"triggered_by", record.TriggeredBy,
		"trigger_pattern", record.TriggerPattern,
		"time_since_last", record.TimeSinceLast,
		"total_rotations_pane", total,
	)

	if dataDir == "" {
		return nil
	}
	return h.SaveToDir(dataDir)
}

func (h *AccountRotationHistory) RecordsForPane(sessionPane string, limit int) []RotationRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	records := h.history[sessionPane]
	if len(records) == 0 {
		return []RotationRecord{}
	}
	if limit <= 0 || limit > len(records) {
		limit = len(records)
	}
	start := len(records) - limit
	if start < 0 {
		start = 0
	}
	out := make([]RotationRecord, limit)
	copy(out, records[start:])
	return out
}

func (h *AccountRotationHistory) GetRotationStats(agentType string) AccountRotationStats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	stats := AccountRotationStats{
		AgentType:        agentType,
		AccountUsage:     make(map[string]int),
		UniquePanesByKey: make(map[string]int),
	}
	var totalBetween time.Duration
	var betweenCount int

	for pane, records := range h.history {
		seenThisPane := false
		for _, r := range records {
			if r.AgentType != agentType {
				continue
			}
			stats.TotalRotations++
			if r.ToAccount != "" {
				stats.AccountUsage[r.ToAccount]++
			}
			if r.TimeSinceLast > 0 {
				totalBetween += r.TimeSinceLast
				betweenCount++
			}
			seenThisPane = true
		}
		if seenThisPane {
			stats.UniquePanesByKey[pane] = 1
		}
	}
	stats.UniquePanes = len(stats.UniquePanesByKey)
	if betweenCount > 0 {
		stats.AvgTimeBetween = totalBetween / time.Duration(betweenCount)
	}
	return stats
}

func (h *AccountRotationHistory) LoadFromDir(dir string) error {
	if dir == "" {
		h.mu.RLock()
		dir = h.dataDir
		h.mu.RUnlock()
	}
	if dir == "" {
		return nil
	}

	path := filepath.Join(dir, ".ntm", "rotation_history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read rotation history: %w", err)
	}

	var pd persistedRotationHistory
	if err := json.Unmarshal(data, &pd); err != nil {
		return fmt.Errorf("parse rotation history: %w", err)
	}

	h.mu.Lock()
	if pd.History != nil {
		h.history = pd.History
	} else {
		h.history = make(map[string][]RotationRecord)
	}
	h.mu.Unlock()
	return nil
}

func (h *AccountRotationHistory) SaveToDir(dir string) error {
	if dir == "" {
		h.mu.RLock()
		dir = h.dataDir
		h.mu.RUnlock()
	}
	if dir == "" {
		return nil
	}

	h.mu.RLock()
	pd := persistedRotationHistory{
		History: make(map[string][]RotationRecord, len(h.history)),
	}
	for pane, records := range h.history {
		pd.History[pane] = append([]RotationRecord(nil), records...)
	}
	h.mu.RUnlock()

	ntmDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		return fmt.Errorf("create .ntm dir: %w", err)
	}

	data, err := json.MarshalIndent(pd, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rotation history: %w", err)
	}

	path := filepath.Join(ntmDir, "rotation_history.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write rotation history: %w", err)
	}
	return nil
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

	// rotationHistoryStore tracks per-pane rotation history with optional persistence.
	rotationHistoryStore *AccountRotationHistory

	// mu protects history and internal state.
	mu sync.Mutex

	// availabilityChecked tracks if we've checked caam availability.
	availabilityChecked bool
	availabilityResult  bool

	// pinnedAccounts maps a caam provider (e.g. "openai", "claude") to an
	// operator-pinned account name. While a provider is pinned, automatic
	// rotation (OnLimitHit) is refused unless ForceGlobalAuthClobber is set.
	// Manual operator-initiated switches (SwitchToAccount) are not blocked.
	pinnedAccounts map[string]string

	// codexHomeInspector, when set, reports the currently live Codex panes and
	// their CODEX_HOME isolation status. It lets the rotator refuse an automatic
	// *global* Codex rotation while one or more live Codex panes share the
	// default global ~/.codex/auth.json (no explicit per-pane CODEX_HOME).
	// When nil, the isolation state is unknown and, for safety, automatic global
	// Codex rotation is refused unless ForceGlobalAuthClobber is set.
	codexHomeInspector CodexHomeInspector

	// ForceGlobalAuthClobber is the explicit operator escape hatch that permits
	// automatic global Codex rotation even when live panes share global ~/.codex
	// or the isolation state is unknown, and bypasses pin enforcement. It maps to
	// the --force-global-auth-clobber operator intent. Off by default.
	ForceGlobalAuthClobber bool

	// codexHomes, when set, makes Codex rotation pane-local: instead of clobbering
	// the global ~/.codex/auth.json via `caam switch`, OnLimitHit repopulates the
	// affected pane's isolated CODEX_HOME from the next caam profile and lets the
	// caller restart only that pane. This is the safe path for Codex swarms (#194).
	codexHomes *CodexHomeProvisioner

	// caamCapProber probes caam for advertised capabilities (e.g. safe-restore).
	// Injected for testability; nil uses the default `caam robot status --json`.
	caamCapProber caamCapabilityProber

	// requireSafeRestore, when true, refuses a *global* caam switch unless caam
	// advertises the safe-restore capability (caam #19). Defaults to true so the
	// dangerous global clobber path is gated by default.
	requireSafeRestore bool
}

// CodexPaneInfo describes one live Codex pane for the auto-rotation safety guard.
type CodexPaneInfo struct {
	// SessionPane identifies the pane (e.g. "session:0.1"), for diagnostics.
	SessionPane string
	// CodexHome is the pane's effective CODEX_HOME. Empty means the pane uses
	// the default global ~/.codex (i.e. it is NOT isolated).
	CodexHome string
}

// IsIsolated reports whether the pane has an explicit per-pane CODEX_HOME and is
// therefore safe to rotate without clobbering the shared global ~/.codex/auth.json.
func (p CodexPaneInfo) IsIsolated() bool {
	return strings.TrimSpace(p.CodexHome) != ""
}

// CodexHomeInspector returns the live Codex panes and their CODEX_HOME isolation
// status. It is injected so the swarm package stays decoupled from tmux and the
// guard remains unit-testable. A nil error with an empty slice means "no live
// Codex panes" (rotation is then permitted by the shared-global guard).
type CodexHomeInspector func() ([]CodexPaneInfo, error)

// NewAccountRotator creates a new AccountRotator with default settings.
func NewAccountRotator() *AccountRotator {
	return &AccountRotator{
		caamPath:             "caam",
		Logger:               slog.Default(),
		CommandTimeout:       5 * time.Second,
		CooldownDuration:     60 * time.Second,
		rotationHistory:      make([]RotationRecord, 0),
		rotationStates:       make(map[string]*RotationState),
		rotationHistoryStore: NewAccountRotationHistory("", slog.Default()),
		pinnedAccounts:       make(map[string]string),
		requireSafeRestore:   true,
	}
}

// WithCodexHomeProvisioner installs a per-pane CODEX_HOME provisioner. When set,
// Codex limit-hits are rotated pane-locally (repopulate the pane's isolated
// CODEX_HOME from the next caam profile + restart only that pane) instead of
// clobbering the global ~/.codex/auth.json.
func (r *AccountRotator) WithCodexHomeProvisioner(p *CodexHomeProvisioner) *AccountRotator {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codexHomes = p
	return r
}

// WithCaamCapabilityProber installs a custom caam capability prober (testing).
func (r *AccountRotator) WithCaamCapabilityProber(p caamCapabilityProber) *AccountRotator {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caamCapProber = p
	return r
}

// WithRequireSafeRestore sets whether a global caam switch is gated on caam
// advertising the safe-restore capability (caam #19). Default true.
func (r *AccountRotator) WithRequireSafeRestore(require bool) *AccountRotator {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requireSafeRestore = require
	return r
}

// WithCodexHomeInspector installs a callback used by the auto-rotation safety
// guard to discover live Codex panes and whether they share the global ~/.codex.
func (r *AccountRotator) WithCodexHomeInspector(inspector CodexHomeInspector) *AccountRotator {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codexHomeInspector = inspector
	return r
}

// WithForceGlobalAuthClobber sets the operator escape hatch that permits unsafe
// automatic global Codex rotation (shared global ~/.codex or unknown isolation)
// and bypasses pin enforcement. Maps to --force-global-auth-clobber.
func (r *AccountRotator) WithForceGlobalAuthClobber(force bool) *AccountRotator {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ForceGlobalAuthClobber = force
	return r
}

// PinAccount pins a provider to a specific account so automatic rotation refuses
// to rotate away from it. agentType may be an agent type ("cod") or a caam
// provider ("openai"); it is normalized to the caam provider name.
func (r *AccountRotator) PinAccount(agentType, accountName string) {
	provider := normalizeProvider(agentType)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pinnedAccounts == nil {
		r.pinnedAccounts = make(map[string]string)
	}
	r.pinnedAccounts[provider] = accountName
	r.logger().Info("[AccountRotator] account_pinned",
		"provider", provider,
		"account", accountName)
}

// UnpinAccount removes any pin for the provider, re-enabling automatic rotation.
func (r *AccountRotator) UnpinAccount(agentType string) {
	provider := normalizeProvider(agentType)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pinnedAccounts, provider)
	r.logger().Info("[AccountRotator] account_unpinned",
		"provider", provider)
}

// PinnedAccount returns the pinned account for the provider and whether a pin is set.
func (r *AccountRotator) PinnedAccount(agentType string) (string, bool) {
	provider := normalizeProvider(agentType)
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.pinnedAccounts[provider]
	return name, ok
}

// PinnedAccounts returns a copy of all current pins (provider -> account).
func (r *AccountRotator) PinnedAccounts() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.pinnedAccounts))
	for k, v := range r.pinnedAccounts {
		out[k] = v
	}
	return out
}

// accountPinsFile is the on-disk location for shared account pins, relative to a
// data directory. The CLI (ntm rotate lock/unlock/status) and the running
// rotator both read/write this file so a pin set in one process is honored by
// the long-lived auto-rotation loop in another.
func accountPinsPath(dataDir string) string {
	return filepath.Join(dataDir, ".ntm", "account_pins.json")
}

type persistedAccountPins struct {
	Pins map[string]string `json:"pins"`
}

// LoadPins replaces the in-memory pins with those persisted under
// <dataDir>/.ntm/account_pins.json. A missing file is not an error.
func (r *AccountRotator) LoadPins(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(accountPinsPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read account pins: %w", err)
	}
	var pd persistedAccountPins
	if err := json.Unmarshal(data, &pd); err != nil {
		return fmt.Errorf("parse account pins: %w", err)
	}
	r.mu.Lock()
	if pd.Pins != nil {
		r.pinnedAccounts = pd.Pins
	} else {
		r.pinnedAccounts = make(map[string]string)
	}
	r.mu.Unlock()
	return nil
}

// SavePins persists the current pins to <dataDir>/.ntm/account_pins.json.
func (r *AccountRotator) SavePins(dataDir string) error {
	if dataDir == "" {
		return fmt.Errorf("dataDir cannot be empty")
	}
	pins := r.PinnedAccounts()
	ntmDir := filepath.Join(dataDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		return fmt.Errorf("create .ntm dir: %w", err)
	}
	data, err := json.MarshalIndent(persistedAccountPins{Pins: pins}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal account pins: %w", err)
	}
	if err := os.WriteFile(accountPinsPath(dataDir), data, 0o644); err != nil {
		return fmt.Errorf("write account pins: %w", err)
	}
	return nil
}

// WithCaamPath sets a custom caam binary path.
func (r *AccountRotator) WithCaamPath(path string) *AccountRotator {
	r.caamPath = path
	return r
}

// WithLogger sets a custom logger.
func (r *AccountRotator) WithLogger(logger *slog.Logger) *AccountRotator {
	r.Logger = logger
	if r.rotationHistoryStore != nil {
		r.rotationHistoryStore.WithLogger(logger)
	}
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

// EnableRotationHistory enables per-pane rotation history persistence using the given data directory.
// It loads any existing history from <dataDir>/.ntm/rotation_history.json.
func (r *AccountRotator) EnableRotationHistory(dataDir string) error {
	if dataDir == "" {
		return fmt.Errorf("dataDir cannot be empty")
	}
	r.mu.Lock()
	if r.rotationHistoryStore == nil {
		r.rotationHistoryStore = NewAccountRotationHistory(dataDir, r.logger())
	} else {
		r.rotationHistoryStore.SetDataDir(dataDir)
		r.rotationHistoryStore.WithLogger(r.logger())
	}
	store := r.rotationHistoryStore
	r.mu.Unlock()
	return store.LoadFromDir(dataDir)
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
	trimmed := strings.TrimSpace(agentType)
	switch agent.AgentType(trimmed).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "openai"
	case agent.AgentTypeGemini:
		return "google"
	default:
		if strings.EqualFold(trimmed, "anthropic") {
			return "claude"
		}
		return trimmed
	}
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

	stdout, stderr, err := r.runCaamCommand(ctx, "list", "--json")
	if err != nil {
		r.logger().Error("[AccountRotator] get_current_failed",
			"provider", provider,
			"error", err,
			"stderr", stderr,
		)
		return nil, fmt.Errorf("caam list failed: %w", err)
	}

	accounts, err := parseCAAMAccounts(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse caam list: %w", err)
	}

	for _, acc := range accounts {
		if acc.Provider != provider || !acc.Active {
			continue
		}

		info := &AccountInfo{
			Provider:      provider,
			AccountName:   acc.ID,
			Email:         acc.Email,
			IsActive:      true,
			RateLimited:   acc.RateLimited,
			CooldownUntil: acc.CooldownUntil,
		}

		r.logger().Info("[AccountRotator] get_current",
			"provider", provider,
			"account", info.AccountName,
		)

		return info, nil
	}

	return nil, fmt.Errorf("no active account found for provider %q", provider)
}

// ListAccounts returns all accounts for a provider/agent type.
func (r *AccountRotator) ListAccounts(agentType string) ([]AccountInfo, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("caam CLI not available")
	}

	provider := normalizeProvider(agentType)

	ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
	defer cancel()

	stdout, stderr, err := r.runCaamCommand(ctx, "list", "--json")
	if err != nil {
		r.logger().Error("[AccountRotator] list_accounts_failed",
			"provider", provider,
			"error", err,
			"stderr", stderr,
		)
		return nil, fmt.Errorf("caam list failed: %w", err)
	}

	accounts, err := parseCAAMAccounts(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse caam list: %w", err)
	}

	result := make([]AccountInfo, 0, len(accounts))
	for _, acc := range accounts {
		if acc.Provider != provider {
			continue
		}

		result = append(result, AccountInfo{
			Provider:      provider,
			AccountName:   acc.ID,
			Email:         acc.Email,
			IsActive:      acc.Active,
			RateLimited:   acc.RateLimited,
			CooldownUntil: acc.CooldownUntil,
		})
	}

	r.logger().Info("[AccountRotator] list_accounts",
		"provider", provider,
		"count", len(result))

	return result, nil
}

// ListAvailableAccounts returns non-rate-limited accounts for a provider/agent type.
func (r *AccountRotator) ListAvailableAccounts(agentType string) ([]AccountInfo, error) {
	accounts, err := r.ListAccounts(agentType)
	if err != nil {
		return nil, err
	}

	available := make([]AccountInfo, 0, len(accounts))
	for _, acc := range accounts {
		if acc.RateLimited {
			continue
		}
		available = append(available, acc)
	}
	return available, nil
}

func parseCAAMAccounts(output string) ([]tools.CAAMAccount, error) {
	data := []byte(output)
	if len(data) == 0 {
		return []tools.CAAMAccount{}, nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON")
	}

	var accounts []tools.CAAMAccount
	if err := json.Unmarshal(data, &accounts); err == nil {
		return accounts, nil
	}

	var wrapper struct {
		Accounts []tools.CAAMAccount `json:"accounts"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Accounts, nil
}

// SwitchAccount switches to the next available account.
// Returns the rotation record on success.
func (r *AccountRotator) SwitchAccount(agentType string) (*RotationRecord, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("caam CLI not available")
	}

	provider := normalizeProvider(agentType)

	r.logger().Info("[AccountRotator] switch_start",
		"provider", provider)

	ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
	defer cancel()

	start := time.Now()
	result, stdout, stderr, runErr := r.switchNext(ctx, provider)
	if runErr != nil {
		r.logger().Error("[AccountRotator] switch_failed",
			"provider", provider,
			"error", runErr,
			"stderr", stderr,
			"stdout", stdout,
		)
		return nil, fmt.Errorf("caam switch failed: %w", runErr)
	}

	duration := time.Since(start)

	record := &RotationRecord{
		Provider:    provider,
		FromAccount: result.PreviousAccount,
		ToAccount:   result.NewAccount,
		RotatedAt:   time.Now(),
		TriggeredBy: "limit_hit",
	}

	r.mu.Lock()
	r.rotationHistory = append(r.rotationHistory, *record)
	r.mu.Unlock()

	r.logger().Info("[AccountRotator] switch_complete",
		"provider", provider,
		"from", record.FromAccount,
		"to", record.ToAccount,
		"duration", duration,
		"accounts_remaining", result.AccountsRemaining,
	)

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
	_, stderr, err := r.runCaamCommand(ctx, "switch", accountName)
	if err != nil {
		r.logger().Error("[AccountRotator] switch_to_failed",
			"provider", provider,
			"account", accountName,
			"error", err,
			"stderr", stderr,
		)
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
	// Snapshot fields under lock to avoid reading while another goroutine writes
	currentAccount := state.CurrentAccount
	rotationCount := state.RotationCount
	lastRotation := state.LastRotation
	r.mu.Unlock()

	r.logger().Info("[AccountRotator] limit_hit_received",
		"session_pane", event.SessionPane,
		"agent_type", event.AgentType,
		"pattern", event.Pattern,
		"current_account", currentAccount,
		"rotation_count", rotationCount)

	// Check cooldown using snapshotted values (already read under lock)
	cooldownActive := rotationCount > 0 && time.Since(lastRotation) < r.CooldownDuration
	if cooldownActive {
		elapsed := time.Since(lastRotation)
		r.logger().Warn("[AccountRotator] cooldown_active",
			"session_pane", event.SessionPane,
			"last_rotation", lastRotation,
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

	provider := normalizeProvider(event.AgentType)

	// Pane-local rotation path (preferred for Codex swarms, #194): if a CODEX_HOME
	// provisioner is configured and this is a Codex pane, rotate by repopulating
	// ONLY this pane's isolated CODEX_HOME from the next caam profile and asking
	// the caller to restart only that pane — never the global ~/.codex/auth.json.
	// Pins are still honored (a pin means "stay on this account"); force bypasses.
	if r.codexHomes != nil && isCodexProvider(provider) {
		return r.rotatePaneLocal(event, provider, currentAccount)
	}

	// Safety guard: honor pins and refuse unsafe global Codex clobbering before
	// we ever shell out to caam. A deliberate refusal is wrapped in
	// ErrRotationBlocked so the caller can degrade gracefully.
	caamCommand := fmt.Sprintf("caam switch %s --next --json", provider)
	if err := r.guardAutoRotation(provider, currentAccount, caamCommand); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
	defer cancel()

	result, stdout, stderr, err := r.switchNext(ctx, provider)
	if err != nil {
		r.logger().Error("[AccountRotator] rotation_failed_on_limit_hit",
			"session_pane", event.SessionPane,
			"agent_type", event.AgentType,
			"error", err,
			"stderr", stderr,
			"stdout", stdout,
		)
		return nil, fmt.Errorf("rotation failed: %w", err)
	}

	record := &RotationRecord{
		Provider:       provider,
		AgentType:      event.AgentType,
		Project:        event.Project,
		FromAccount:    result.PreviousAccount,
		ToAccount:      result.NewAccount,
		RotatedAt:      time.Now(),
		SessionPane:    event.SessionPane,
		TriggeredBy:    "limit_hit",
		TriggerPattern: event.Pattern,
	}

	// Update per-pane state
	r.mu.Lock()
	timeSinceLast := time.Duration(0)
	if state.RotationCount > 0 && !state.LastRotation.IsZero() {
		timeSinceLast = record.RotatedAt.Sub(state.LastRotation)
	}
	record.TimeSinceLast = timeSinceLast

	prevAccount := state.CurrentAccount
	if prevAccount == "" && record.FromAccount != "" {
		prevAccount = record.FromAccount
	}
	if record.FromAccount == "" {
		record.FromAccount = prevAccount
	}
	if prevAccount != "" {
		state.PreviousAccounts = append(state.PreviousAccounts, prevAccount)
	}
	state.CurrentAccount = record.ToAccount
	state.RotationCount++
	state.LastRotation = record.RotatedAt
	r.rotationHistory = append(r.rotationHistory, *record)
	store := r.rotationHistoryStore
	r.mu.Unlock()

	if store != nil {
		if err := store.RecordRotation(*record); err != nil {
			r.logger().Warn("[AccountRotator] rotation_history_record_failed",
				"session_pane", record.SessionPane,
				"agent_type", record.AgentType,
				"error", err,
			)
		}
	}

	r.logger().Info("[AccountRotator] rotation_complete_on_limit_hit",
		"session_pane", event.SessionPane,
		"from_account", record.FromAccount,
		"to_account", record.ToAccount,
		"total_rotations", state.RotationCount)

	return record, nil
}

// ErrRotationBlocked is returned (wrapped) when the safety guard refuses an
// automatic rotation. Callers can use errors.Is to detect a deliberate refusal
// (as opposed to an operational failure) and degrade gracefully.
var ErrRotationBlocked = fmt.Errorf("rotation blocked by safety guard")

// rotatePaneLocal performs a Codex pane-local rotation: it repopulates only the
// affected pane's isolated CODEX_HOME from the next caam profile and returns a
// rotation record marked PaneLocal so the caller restarts just that pane. The
// global ~/.codex/auth.json is never touched. Pins are honored (a pin means the
// pane stays on its account); ForceGlobalAuthClobber bypasses the pin.
func (r *AccountRotator) rotatePaneLocal(event LimitHitEvent, provider, currentAccount string) (*RotationRecord, error) {
	r.mu.Lock()
	pinned, isPinned := r.pinnedAccounts[provider]
	force := r.ForceGlobalAuthClobber
	prov := r.codexHomes
	r.mu.Unlock()

	caamCommand := fmt.Sprintf("caam profile (pane-local, session=%s pane=%s)", event.SessionPane, event.SessionPane)

	// Honor an explicit pin: never rotate a pinned pane away from its account.
	if isPinned && !force {
		r.logger().Warn("[AccountRotator] rotation_blocked",
			"provider", provider, "reason", "account_pinned:"+pinned,
			"live_panes", 1, "caam_command", caamCommand, "from", currentAccount, "to", "")
		return nil, fmt.Errorf("%w: %s is pinned to %q; unpin (ntm rotate unlock) or pass --force-global-auth-clobber to override",
			ErrRotationBlocked, provider, pinned)
	}

	session, pane := splitSessionPane(event.SessionPane)

	ctx, cancel := context.WithTimeout(context.Background(), prov.CommandTimeout+r.CommandTimeout)
	defer cancel()

	// Choose the next profile to rotate this pane onto via caam's isolated
	// primitives (no global clobber).
	nextProfile, err := r.nextCodexProfile(ctx, provider, currentAccount)
	if err != nil {
		r.logger().Error("[AccountRotator] pane_local_next_profile_failed",
			"session_pane", event.SessionPane, "error", err)
		return nil, fmt.Errorf("pane-local rotation: choose next profile: %w", err)
	}

	home, err := prov.RepopulatePaneHome(ctx, session, pane, nextProfile)
	if err != nil {
		r.logger().Error("[AccountRotator] pane_local_repopulate_failed",
			"session_pane", event.SessionPane, "profile", nextProfile, "error", err)
		return nil, fmt.Errorf("pane-local rotation: repopulate %s: %w", event.SessionPane, err)
	}

	record := &RotationRecord{
		Provider:       provider,
		AgentType:      event.AgentType,
		Project:        event.Project,
		FromAccount:    currentAccount,
		ToAccount:      nextProfile,
		RotatedAt:      time.Now(),
		SessionPane:    event.SessionPane,
		TriggeredBy:    "limit_hit",
		TriggerPattern: event.Pattern,
		PaneLocal:      true,
		CodexHome:      home,
	}

	r.mu.Lock()
	state := r.getOrCreateState(event.SessionPane)
	if state.CurrentAccount != "" {
		state.PreviousAccounts = append(state.PreviousAccounts, state.CurrentAccount)
	}
	state.CurrentAccount = nextProfile
	state.RotationCount++
	state.LastRotation = record.RotatedAt
	r.rotationHistory = append(r.rotationHistory, *record)
	store := r.rotationHistoryStore
	r.mu.Unlock()

	if store != nil {
		_ = store.RecordRotation(*record)
	}

	r.logger().Info("[AccountRotator] rotation_allowed",
		"provider", provider, "reason", "pane_local_codex_home",
		"live_panes", 1, "caam_command", caamCommand,
		"from", currentAccount, "to", nextProfile)
	r.logger().Info("[AccountRotator] pane_local_rotation_complete",
		"session_pane", event.SessionPane, "from", currentAccount,
		"to", nextProfile, "codex_home", home)

	return record, nil
}

// nextCodexProfile picks the next non-current caam profile for the provider. It
// reads the account list via caam (isolated read, no clobber) and round-robins
// past the current account. Returns an error if no alternative exists.
func (r *AccountRotator) nextCodexProfile(ctx context.Context, provider, current string) (string, error) {
	stdout, stderr, err := r.runCaamCommand(ctx, "list", "--json")
	if err != nil {
		return "", fmt.Errorf("caam list: %w (%s)", err, strings.TrimSpace(stderr))
	}
	accounts, err := parseCAAMAccounts(stdout)
	if err != nil {
		return "", fmt.Errorf("parse caam list: %w", err)
	}
	var names []string
	for _, a := range accounts {
		if a.Provider != provider {
			continue
		}
		if a.RateLimited {
			continue
		}
		names = append(names, a.ID)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no available %s accounts to rotate to", provider)
	}
	// Round-robin past the current account.
	for i, n := range names {
		if n == current {
			return names[(i+1)%len(names)], nil
		}
	}
	// Current not found in list (or empty) — just take the first available.
	return names[0], nil
}

// splitSessionPane splits "session:window.pane" (or "session:pane") into a
// session segment and a pane segment for use as isolated CODEX_HOME path parts.
func splitSessionPane(sessionPane string) (session, pane string) {
	sessionPane = strings.TrimSpace(sessionPane)
	if sessionPane == "" {
		return "default", "0"
	}
	if idx := strings.IndexByte(sessionPane, ':'); idx >= 0 {
		session = sessionPane[:idx]
		pane = sessionPane[idx+1:]
	} else {
		session = sessionPane
		pane = "0"
	}
	if session == "" {
		session = "default"
	}
	if pane == "" {
		pane = "0"
	}
	return session, pane
}

// guardAutoRotation enforces the automatic-rotation safety guardrails for the
// caam-switch path:
//
//  1. Honor an explicit account pin: refuse to auto-rotate away from a pinned
//     provider unless ForceGlobalAuthClobber is set.
//  2. Refuse automatic *global* Codex rotation when one or more live Codex panes
//     use the default global ~/.codex (no explicit per-pane CODEX_HOME), or when
//     the isolation state is unknown — unless ForceGlobalAuthClobber is set.
//
// It logs every decision (allowed and blocked) with structured fields. The
// caamCommand argument is the caam invocation that would run if allowed.
// Returns nil to allow, or an error wrapping ErrRotationBlocked to refuse.
func (r *AccountRotator) guardAutoRotation(provider, from, caamCommand string) error {
	r.mu.Lock()
	pinned, isPinned := r.pinnedAccounts[provider]
	force := r.ForceGlobalAuthClobber
	inspector := r.codexHomeInspector
	r.mu.Unlock()

	logBlocked := func(reason string, livePanes int) {
		r.logger().Warn("[AccountRotator] rotation_blocked",
			"provider", provider,
			"reason", reason,
			"live_panes", livePanes,
			"caam_command", caamCommand,
			"from", from,
			"to", "")
	}
	logAllowed := func(reason string, livePanes int) {
		r.logger().Info("[AccountRotator] rotation_allowed",
			"provider", provider,
			"reason", reason,
			"live_panes", livePanes,
			"caam_command", caamCommand,
			"from", from,
			"to", "")
	}

	// Guardrail 2: honor an explicit pin. Checked first so a pin protects every
	// provider, not just Codex. Force overrides.
	if isPinned && !force {
		logBlocked("account_pinned:"+pinned, 0)
		return fmt.Errorf("%w: %s is pinned to %q; unpin (ntm rotate unlock) or pass --force-global-auth-clobber to override",
			ErrRotationBlocked, provider, pinned)
	}

	// Guardrail 1 only applies to Codex/global-auth clobbering. Non-Codex
	// providers (and forced rotations) skip the shared-global check.
	if !isCodexProvider(provider) {
		logAllowed("non_codex_provider", 0)
		return nil
	}
	if force {
		logAllowed("force_global_auth_clobber", 0)
		return nil
	}

	// Unknown isolation state (no inspector wired): refuse, since we cannot prove
	// no live pane shares the global ~/.codex/auth.json.
	if inspector == nil {
		logBlocked("codex_isolation_unknown", -1)
		return fmt.Errorf("%w: refusing to auto-rotate Codex account: live Codex pane isolation is unknown. "+
			"Use per-pane CODEX_HOME isolation, or pass --force-global-auth-clobber",
			ErrRotationBlocked)
	}

	panes, err := inspector()
	if err != nil {
		// Fail closed: if we cannot determine pane state, refuse the global clobber.
		logBlocked("codex_inspect_failed:"+err.Error(), -1)
		return fmt.Errorf("%w: refusing to auto-rotate Codex account: could not inspect live Codex panes: %v. "+
			"Use per-pane CODEX_HOME isolation, or pass --force-global-auth-clobber",
			ErrRotationBlocked, err)
	}

	sharedGlobal := 0
	for _, p := range panes {
		if !p.IsIsolated() {
			sharedGlobal++
		}
	}
	if sharedGlobal > 0 {
		logBlocked("shared_global_codex_home", sharedGlobal)
		return fmt.Errorf("%w: refusing to auto-rotate Codex account: %d live Codex pane(s) share global ~/.codex/auth.json. "+
			"Use per-pane CODEX_HOME isolation, or pass --force-global-auth-clobber",
			ErrRotationBlocked, sharedGlobal)
	}

	// Even when all live panes are isolated, a *global* caam switch still rewrites
	// ~/.codex/auth.json. Gate it on caam advertising the safe-restore capability
	// (caam #19) so we never reintroduce a consumed refresh_token. force bypasses.
	if r.requireSafeRestore {
		ctx, cancel := context.WithTimeout(context.Background(), r.CommandTimeout)
		defer cancel()
		ok, capErr := r.CaamSupportsSafeRestore(ctx)
		if capErr != nil {
			logBlocked("caam_capability_probe_failed:"+capErr.Error(), len(panes))
			return fmt.Errorf("%w: refusing global Codex rotation: could not verify caam safe-restore capability: %v. "+
				"Upgrade caam (#19) or pass --force-global-auth-clobber",
				ErrRotationBlocked, capErr)
		}
		if !ok {
			logBlocked("caam_lacks_safe_restore", len(panes))
			return fmt.Errorf("%w: refusing global Codex rotation: caam does not advertise the %q capability (caam #19). "+
				"Upgrade caam or pass --force-global-auth-clobber",
				ErrRotationBlocked, CapabilitySafeRestore)
		}
	}

	logAllowed("codex_panes_isolated_safe_restore", len(panes))
	return nil
}

func (r *AccountRotator) switchNext(ctx context.Context, provider string) (tools.SwitchResult, string, string, error) {
	stdout, stderr, runErr := r.runCaamCommand(ctx, "switch", provider, "--next", "--json")

	payload := stdout
	if payload == "" {
		payload = stderr
	}

	var result tools.SwitchResult
	if payload != "" && json.Valid([]byte(payload)) {
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return tools.SwitchResult{}, stdout, stderr, fmt.Errorf("parse caam switch output: %w", err)
		}
	}

	if runErr != nil {
		return result, stdout, stderr, runErr
	}

	if !result.Success && result.Error != "" {
		return result, stdout, stderr, fmt.Errorf("%s", result.Error)
	}

	return result, stdout, stderr, nil
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
func (r *AccountRotator) runCaamCommand(ctx context.Context, args ...string) (stdoutStr string, stderrStr string, err error) {
	cmd := exec.CommandContext(ctx, r.caamPath, args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.String(), stderr.String(), fmt.Errorf("caam %v: timeout", args)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return stdout.String(), stderr.String(), fmt.Errorf("caam %v: exit %d: %s", args, exitErr.ExitCode(), stderr.String())
		}
		return stdout.String(), stderr.String(), fmt.Errorf("caam %v: %w", args, err)
	}
	return stdout.String(), stderr.String(), nil
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
