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
	// Injected for testability; nil uses the default `caam robot status`.
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

// logger returns the configured logger or the default logger.
func (r *AccountRotator) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// caamToolName converts NTM's internal provider name to the tool name caam
// actually accepts on the command line. caam names its tools "codex", "claude"
// and "gemini"; NTM has long used "openai" and "google" internally, and passing
// those straight through makes caam fail with `unknown tool: openai`. Internal
// names stay unchanged so rate-limit tracking and pinned-account map keys keep
// working; only the caam boundary is translated. Mirrors ntmProviderForCAAM in
// internal/tools/caam.go.
func caamToolName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return "codex"
	case "google":
		return "gemini"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// ntmProviderName converts a caam tool name back to NTM's internal provider
// name. Inverse of caamToolName; used when reading caam output so the result
// can be compared against normalizeProvider values.
func ntmProviderName(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "codex":
		return "openai"
	case "gemini":
		return "google"
	default:
		return strings.ToLower(strings.TrimSpace(tool))
	}
}

// normalizeProvider converts agent type to NTM's internal provider name. Use
// caamToolName on the result before passing it to the caam CLI.
func normalizeProvider(agentType string) string {
	trimmed := strings.TrimSpace(agentType)
	switch agent.AgentType(trimmed).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "openai"
	case agent.AgentTypeGemini, agent.AgentTypeAntigravity:
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

	// Current caam emits {"profiles":[{tool,name,active,health:{status}}]}.
	// This must be tried before the {"accounts":[...]} wrapper below, because
	// that wrapper unmarshals cleanly from ANY JSON object and would silently
	// yield zero accounts with no error -- which read as "no accounts to rotate
	// to" rather than as a parse failure.
	var profileWrapper struct {
		Profiles []struct {
			Tool   string `json:"tool"`
			Name   string `json:"name"`
			Active bool   `json:"active"`
			System bool   `json:"system"`
			Health struct {
				Status string `json:"status"`
			} `json:"health"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &profileWrapper); err == nil && len(profileWrapper.Profiles) > 0 {
		converted := make([]tools.CAAMAccount, 0, len(profileWrapper.Profiles))
		for _, profile := range profileWrapper.Profiles {
			// caam's synthetic _backup_*/_original entries are not real
			// accounts and must never be rotation targets.
			if profile.System {
				continue
			}
			converted = append(converted, tools.CAAMAccount{
				ID:          profile.Name,
				Name:        profile.Name,
				Provider:    ntmProviderName(profile.Tool),
				Active:      profile.Active,
				RateLimited: profile.Health.Status == "cooldown" || profile.Health.Status == "critical",
			})
		}
		return converted, nil
	}

	var wrapper struct {
		Accounts []tools.CAAMAccount `json:"accounts"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Accounts, nil
}

// validateCaamAccountOperand refuses account/profile names that could not be
// passed safely as a caam positional argument. Names come from `caam list
// --json` output (or operator input); a hostile or corrupted listing must not
// be able to inject caam flags — a profile "named" `--auto` would otherwise
// land in the activate argv as an option, not an operand. Control bytes are
// refused outright (they can only be argv smuggling or log forgery).
func validateCaamAccountOperand(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("account name is empty")
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("account name %q begins with '-'; refusing flag-shaped operand", trimmed)
	}
	// Scan the RAW name, not the trimmed copy: callers pass the original
	// string to exec, so a control byte hiding in leading/trailing
	// whitespace ("foo\n", "\tfoo") must be refused too — matching
	// CAAMAdapter.SwitchAccount, which scans the full operand.
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("account name contains control character 0x%02x", r)
		}
	}
	return nil
}

// SwitchToAccount switches to a specific account.
func (r *AccountRotator) SwitchToAccount(agentType, accountName string) (*RotationRecord, error) {
	if !r.IsAvailable() {
		return nil, fmt.Errorf("caam CLI not available")
	}
	if err := validateCaamAccountOperand(accountName); err != nil {
		return nil, fmt.Errorf("refusing account switch: %w", err)
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
	// caam's signature is `activate <tool> [profile-name]`. Passing only the
	// account name made caam read it as the tool name and fail.
	_, stderr, err := r.runCaamCommand(ctx, "activate", caamToolName(provider), accountName)
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

// ErrRotationBlocked is returned (wrapped) when the safety guard refuses an
// automatic rotation. Callers can use errors.Is to detect a deliberate refusal
// (as opposed to an operational failure) and degrade gracefully.
var ErrRotationBlocked = fmt.Errorf("rotation blocked by safety guard")

// GuardAutoSwitch enforces the automatic-rotation safety guardrails
// (guardAutoRotation) for an UNATTENDED targeted switch initiated outside
// OnLimitHit — e.g. the coordinator's CAAM auto-failover (bd-um3uy). It honors
// operator account pins for every provider and the global-Codex-auth
// protections (live-pane isolation proof plus the caam safe-restore
// capability gate). provider accepts an agent type ("cod") or an NTM provider
// name ("openai"). Returns nil to allow, or an error wrapping
// ErrRotationBlocked to refuse.
func (r *AccountRotator) GuardAutoSwitch(provider string) error {
	p := normalizeProvider(provider)
	return r.guardAutoRotation(p, "", fmt.Sprintf("caam activate %s <account>", caamToolName(p)))
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
