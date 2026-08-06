package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// CAAMAdapter provides integration with the CAAM (Coding Agent Account Manager) tool.
// CAAM manages multiple accounts for AI coding agents and handles automatic
// account rotation when rate limits are hit.
type CAAMAdapter struct {
	*BaseAdapter
}

// NewCAAMAdapter creates a new CAAM adapter
func NewCAAMAdapter() *CAAMAdapter {
	return &CAAMAdapter{
		BaseAdapter: NewBaseAdapter(ToolCAAM, "caam"),
	}
}

// Detect checks if caam is installed
func (a *CAAMAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed caam version.
//
// caam is a cobra-based Go CLI: it exposes a `version` SUBCOMMAND
// (`caam version` -> "caam 0.1.11 (7c604c4) built on ... with go1.26.2")
// and explicitly rejects a `--version` flag with "unknown flag: --version".
// ntm historically called `--version`, which always failed and made caam
// look like a 0.0.0 / "not responding" tool even when it was perfectly
// healthy (acfs#296). We invoke the `version` subcommand and stay tolerant
// of version skew: any output containing a parseable X.Y.Z is accepted,
// including when caam exits non-zero or prints to stderr.
func (a *CAAMAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "version")
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Prefer stdout; fall back to stderr (older/newer caam builds, or a
	// usage banner, may route the version string there). A parseable
	// version in either stream is good enough — don't hard-fail on a
	// non-zero exit if we still recovered a usable version.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = strings.TrimSpace(stderr.String())
	}

	if out != "" {
		if v, perr := ParseStandardVersion(out); perr == nil && (v.Major != 0 || v.Minor != 0 || v.Patch != 0) {
			return v, nil
		}
	}

	if err != nil {
		return Version{}, fmt.Errorf("failed to get caam version: %w", err)
	}

	// Command succeeded but we could not extract an X.Y.Z. Return whatever
	// raw text we have rather than a misleading 0.0.0 with no provenance.
	if out != "" {
		return ParseStandardVersion(out)
	}
	return Version{}, fmt.Errorf("caam version produced no output")
}

// Capabilities returns the list of caam capabilities
func (a *CAAMAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "help")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // Ignore error, just check output

	output := stdout.String()

	// Check for known capabilities
	if strings.Contains(output, "--json") || strings.Contains(output, "robot") {
		caps = append(caps, CapRobotMode)
	}

	return caps, nil
}

// Health checks if caam is functioning correctly
func (a *CAAMAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "caam not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check. caam responds to
	// `caam version`; a successful read proves the binary is alive.
	_, verr := a.Version(ctx)
	if verr == nil {
		return &HealthStatus{
			Healthy:     true,
			Message:     "caam is healthy",
			LastChecked: time.Now(),
			Latency:     time.Since(start),
		}, nil
	}

	// Version probe failed. Before declaring caam unresponsive, fall back
	// to a liveness check that does not depend on the exact version
	// command surface: if the binary runs at all and emits its usage
	// banner, it is responding (just version-skewed), so we must not
	// report a misleading "not responding" (acfs#296).
	if a.isResponsive(ctx) {
		return &HealthStatus{
			Healthy:     true,
			Message:     "caam is healthy (version unavailable)",
			LastChecked: time.Now(),
			Latency:     time.Since(start),
		}, nil
	}

	return &HealthStatus{
		Healthy:     false,
		Message:     fmt.Sprintf("caam at %s not responding", path),
		Error:       verr.Error(),
		LastChecked: time.Now(),
		Latency:     time.Since(start),
	}, nil
}

// isResponsive performs a version-agnostic liveness probe: it runs
// `caam --help` and treats any captured output (cobra prints its usage to
// stdout, but routes it to stderr on an error exit) as proof the binary is
// alive. This keeps the health verdict robust to changes in caam's version
// command surface.
func (a *CAAMAdapter) isResponsive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--help")
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // cobra may exit non-zero; we only care about output.

	if ctx.Err() == context.DeadlineExceeded {
		return false
	}
	return stdout.Len() > 0 || stderr.Len() > 0
}

// HasCapability checks if caam has a specific capability
func (a *CAAMAdapter) HasCapability(ctx context.Context, cap Capability) bool {
	caps, err := a.Capabilities(ctx)
	if err != nil {
		return false
	}
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Info returns complete caam tool information
func (a *CAAMAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// CAAM-specific types and methods

// CAAMAccount represents an account managed by CAAM
type CAAMAccount struct {
	ID            string    `json:"id"`
	Provider      string    `json:"provider"`
	Email         string    `json:"email,omitempty"`
	Name          string    `json:"name,omitempty"`
	Active        bool      `json:"active"`
	RateLimited   bool      `json:"rate_limited,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	CostCents     int       `json:"cost_cents,omitempty"`
}

// CAAMCostSession is CAAM's per-profile cost record. Costs are estimates
// supplied by CAAM, not API-provider invoices.
type CAAMCostSession struct {
	ID             int    `json:"id"`
	Provider       string `json:"provider"`
	Profile        string `json:"profile"`
	EstimatedCents int    `json:"estimated_cost_cents"`
}

// CAAMStatus represents the current CAAM status
type CAAMStatus struct {
	Available     bool          `json:"available"`
	Version       string        `json:"version,omitempty"`
	AccountsCount int           `json:"accounts_count"`
	Providers     []string      `json:"providers,omitempty"`
	ActiveAccount *CAAMAccount  `json:"active_account,omitempty"`
	Accounts      []CAAMAccount `json:"accounts,omitempty"`
}

// SwitchResult represents the response from caam switch --next --json
type SwitchResult struct {
	Success           bool      `json:"success"`
	Provider          string    `json:"provider"`
	PreviousAccount   string    `json:"previous_account"`
	NewAccount        string    `json:"new_account"`
	CooldownUntil     time.Time `json:"cooldown_until,omitempty"`
	AccountsRemaining int       `json:"accounts_remaining"`
	Error             string    `json:"error,omitempty"`
}

// CAAMCredentials represents credential information for an active account
type CAAMCredentials struct {
	Provider    string `json:"provider"`
	AccountID   string `json:"account_id"`
	APIKey      string `json:"api_key,omitempty"`
	TokenPath   string `json:"token_path,omitempty"`
	EnvVarName  string `json:"env_var_name"`
	ValidUntil  string `json:"valid_until,omitempty"`
	RateLimited bool   `json:"rate_limited"`
}

// ProviderEnvVars maps provider names to their API key environment variables
var ProviderEnvVars = map[string]string{
	"claude": "ANTHROPIC_API_KEY",
	"openai": "OPENAI_API_KEY",
	"gemini": "GOOGLE_API_KEY",
}

// GetCredentialEnvVar returns the environment variable name for a provider's API key
func GetCredentialEnvVar(provider string) string {
	if envVar, ok := ProviderEnvVars[provider]; ok {
		return envVar
	}
	// Default pattern for unknown providers
	return strings.ToUpper(provider) + "_API_KEY"
}

// Cached status to avoid repeated lookups
var (
	caamStatusOnce   sync.Once
	caamStatusCache  CAAMStatus
	caamStatusExpiry time.Time
	caamStatusMutex  sync.RWMutex
	caamCacheTTL     = 5 * time.Minute
)

// GetStatus returns the current CAAM status with caching
func (a *CAAMAdapter) GetStatus(ctx context.Context) (*CAAMStatus, error) {
	// Check cache first
	caamStatusMutex.RLock()
	if time.Now().Before(caamStatusExpiry) {
		status := cloneCAAMStatus(caamStatusCache)
		caamStatusMutex.RUnlock()
		return &status, nil
	}
	caamStatusMutex.RUnlock()

	// Fetch fresh status
	status, err := a.fetchStatus(ctx)
	if err != nil {
		return nil, err
	}

	// Update cache
	caamStatusMutex.Lock()
	caamStatusCache = cloneCAAMStatus(*status)
	caamStatusExpiry = time.Now().Add(caamCacheTTL)
	caamStatusMutex.Unlock()

	return status, nil
}

// cloneCAAMStatus returns an independent status snapshot. GetStatus exposes
// account slices and an active-account pointer to callers, so a shallow copy
// would let callers mutate the shared cache and race with other readers.
func cloneCAAMStatus(status CAAMStatus) CAAMStatus {
	cloned := status
	cloned.Providers = append([]string(nil), status.Providers...)
	cloned.Accounts = append([]CAAMAccount(nil), status.Accounts...)
	cloned.ActiveAccount = nil

	if status.ActiveAccount == nil {
		return cloned
	}
	for i := range status.Accounts {
		if status.ActiveAccount == &status.Accounts[i] {
			cloned.ActiveAccount = &cloned.Accounts[i]
			return cloned
		}
	}

	// Be defensive about detached active-account pointers from an unexpected
	// CAAM response: preserve the value without aliasing caller-owned memory.
	active := *status.ActiveAccount
	cloned.ActiveAccount = &active
	return cloned
}

// fetchStatus fetches fresh status from caam
func (a *CAAMAdapter) fetchStatus(ctx context.Context) (*CAAMStatus, error) {
	status := &CAAMStatus{}

	// Check if caam is installed
	path, installed := a.Detect()
	if !installed {
		status.Available = false
		return status, nil
	}
	status.Available = true

	// Get version
	version, err := a.Version(ctx)
	if err == nil {
		status.Version = version.String()
	}

	// CAAM's robot status endpoint is its stable machine-readable account
	// inventory. It returns profiles nested under providers rather than a
	// legacy flat `list --json` array.
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "robot", "status", "--compact")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// caam might not have accounts configured - that's ok
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// Return status without accounts
		return status, nil
	}

	output := stdout.Bytes()
	if len(output) == 0 || !json.Valid(output) {
		return status, nil
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Providers []struct {
				ID       string `json:"id"`
				LoggedIn bool   `json:"logged_in"`
				Profiles []struct {
					Name   string `json:"name"`
					Active bool   `json:"active"`
					System bool   `json:"system"`
					Health struct {
						Status string `json:"status"`
					} `json:"health"`
				} `json:"profiles"`
			} `json:"providers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &response); err != nil || !response.Success {
		return status, nil
	}

	accounts := make([]CAAMAccount, 0)
	for _, provider := range response.Data.Providers {
		for _, profile := range provider.Profiles {
			// caam reports synthetic bookkeeping profiles (_original,
			// _backup_<timestamp>) alongside real ones. Surfacing them as
			// accounts inflates the account count and makes them look like
			// valid rotation targets; activating one would restore a stale
			// auth snapshot rather than switch to another account.
			if profile.System {
				continue
			}
			accounts = append(accounts, CAAMAccount{
				ID:          profile.Name,
				Provider:    caamProviderForNTM(provider.ID),
				Name:        profile.Name,
				Active:      profile.Active,
				RateLimited: profile.Health.Status == "cooldown" || profile.Health.Status == "critical",
			})
		}
	}

	if sessions, err := a.GetCostSessions(ctx); err == nil {
		costs := make(map[string]int, len(sessions))
		for _, session := range sessions {
			costs[caamProviderForNTM(session.Provider)+"\x00"+session.Profile] += session.EstimatedCents
		}
		for i := range accounts {
			accounts[i].CostCents = costs[accounts[i].Provider+"\x00"+accounts[i].ID]
		}
	}
	status.applyAccounts(accounts)

	return status, nil
}

// GetCostSessions returns CAAM's cost records keyed by provider and profile.
// The adapter keeps account-level cost reporting read-only and degrades
// gracefully when a CAAM installation does not expose cost tracking.
func (a *CAAMAdapter) GetCostSessions(ctx context.Context) ([]CAAMCostSession, error) {
	path, installed := a.Detect()
	if !installed {
		return nil, ErrToolNotInstalled
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "cost", "sessions", "--json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("read caam cost sessions: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var response struct {
		Sessions []CAAMCostSession `json:"sessions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("%w: parse caam cost sessions: %v", ErrSchemaValidation, err)
	}
	return response.Sessions, nil
}

func caamProviderForNTM(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return "openai"
	}
	return strings.ToLower(strings.TrimSpace(provider))
}

func ntmProviderForCAAM(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), "openai") {
		return "codex"
	}
	return strings.ToLower(strings.TrimSpace(provider))
}

// applyAccounts populates account-derived status fields.
func (s *CAAMStatus) applyAccounts(accounts []CAAMAccount) {
	s.Accounts = accounts
	s.AccountsCount = len(accounts)
	s.ActiveAccount = nil
	s.Providers = s.Providers[:0]

	// Extract unique providers while preserving exact account identity for active account.
	providerSet := make(map[string]struct{})
	for i := range s.Accounts {
		acc := s.Accounts[i]
		if acc.Provider != "" {
			providerSet[acc.Provider] = struct{}{}
		}
		if acc.Active {
			s.ActiveAccount = &s.Accounts[i]
		}
	}
	for p := range providerSet {
		s.Providers = append(s.Providers, p)
	}
	sort.Strings(s.Providers)
}

// GetAccounts returns the list of configured accounts
func (a *CAAMAdapter) GetAccounts(ctx context.Context) ([]CAAMAccount, error) {
	status, err := a.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	return status.Accounts, nil
}

// GetActiveAccount returns the currently active account
func (a *CAAMAdapter) GetActiveAccount(ctx context.Context) (*CAAMAccount, error) {
	status, err := a.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	return status.ActiveAccount, nil
}

// GetCurrentCredentials returns credential information for the active account of a provider.
// CAAM does not expose credentials through the removed `creds` command. Its
// supported machine-readable source is `caam robot paths`, whose provider IDs
// use CAAM's names (notably "codex", not NTM's "openai").
func (a *CAAMAdapter) GetCurrentCredentials(ctx context.Context, provider string) (*CAAMCredentials, error) {
	path, installed := a.Detect()
	if !installed {
		return nil, ErrToolNotInstalled
	}
	ntmProvider := caamProviderForNTM(provider)
	caamProvider := ntmProviderForCAAM(ntmProvider)
	if caamProvider == "" {
		return nil, fmt.Errorf("provider is required")
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "robot", "paths")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("read caam credential paths: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Providers []struct {
				ID    string `json:"id"`
				Files []struct {
					Path     string `json:"path"`
					Exists   bool   `json:"exists"`
					Required bool   `json:"required"`
				} `json:"files"`
			} `json:"providers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("%w: parse caam credential paths: %v", ErrSchemaValidation, err)
	}
	if !response.Success {
		return nil, fmt.Errorf("%w: caam robot paths returned success=false", ErrSchemaValidation)
	}

	var tokenPath string
	for _, candidate := range response.Data.Providers {
		if !strings.EqualFold(candidate.ID, caamProvider) {
			continue
		}
		for _, file := range candidate.Files {
			if file.Required && file.Exists && strings.TrimSpace(file.Path) != "" {
				tokenPath = file.Path
				break
			}
		}
		if tokenPath == "" {
			for _, file := range candidate.Files {
				if file.Exists && strings.TrimSpace(file.Path) != "" {
					tokenPath = file.Path
					break
				}
			}
		}
		break
	}
	if tokenPath == "" {
		return nil, fmt.Errorf("%w: caam returned no credential path for provider %q", ErrSchemaValidation, caamProvider)
	}

	creds := &CAAMCredentials{
		Provider:   ntmProvider,
		EnvVarName: GetCredentialEnvVar(ntmProvider),
		TokenPath:  tokenPath,
	}
	accounts, err := a.GetAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if account.Provider == ntmProvider && account.Active {
			creds.AccountID = account.ID
			creds.RateLimited = account.RateLimited
			break
		}
	}

	return creds, nil
}

// SwitchAccount activates a named profile for a provider.
func (a *CAAMAdapter) SwitchAccount(ctx context.Context, provider, accountID string) error {
	path, installed := a.Detect()
	if !installed {
		return ErrToolNotInstalled
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(accountID) == "" {
		return fmt.Errorf("provider and account ID are required")
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "activate", ntmProviderForCAAM(provider), accountID, "--json")
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ErrTimeout
		}
		return fmt.Errorf("failed to switch account: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return fmt.Errorf("%w: parse caam activate response: %v", ErrSchemaValidation, err)
	}
	if !response.Success {
		if response.Error != "" {
			return fmt.Errorf("caam activate failed: %s", response.Error)
		}
		return fmt.Errorf("%w: caam activate returned success=false", ErrSchemaValidation)
	}

	// Invalidate cache after switch
	caamStatusMutex.Lock()
	caamStatusExpiry = time.Time{}
	caamStatusMutex.Unlock()

	return nil
}

// SwitchToNextAccount switches to the next available account for a provider.
// It calls CAAM's current `activate <provider> --auto --json` command.
// This is the preferred method for automatic account switching on rate limit.
func (a *CAAMAdapter) SwitchToNextAccount(ctx context.Context, provider string) (*SwitchResult, error) {
	path, installed := a.Detect()
	if !installed {
		return nil, ErrToolNotInstalled
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "activate", ntmProviderForCAAM(provider), "--auto", "--json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Parse JSON response even on error (CAAM emits structured error details).
	output := bytes.TrimSpace(stdout.Bytes())
	if len(output) == 0 {
		output = bytes.TrimSpace(stderr.Bytes())
	}

	var response struct {
		Success         bool   `json:"success"`
		Profile         string `json:"profile"`
		PreviousProfile string `json:"previous_profile"`
		Error           string `json:"error"`
	}
	if len(output) > 0 {
		if !json.Valid(output) {
			if err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return nil, ErrTimeout
				}
				return nil, fmt.Errorf("failed to switch account: %w: %s", err, stderr.String())
			}
			return nil, fmt.Errorf("%w: invalid JSON from caam switch", ErrSchemaValidation)
		}

		if jsonErr := json.Unmarshal(output, &response); jsonErr != nil {
			// If JSON parsing fails, wrap the original error
			if err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return nil, ErrTimeout
				}
				return nil, fmt.Errorf("failed to switch account: %w: %s", err, stderr.String())
			}
			return nil, fmt.Errorf("%w: failed to parse caam switch response: %v", ErrSchemaValidation, jsonErr)
		}
	} else if err != nil {
		// No output and command failed
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("failed to switch account: %w: %s", err, stderr.String())
	} else {
		// Command succeeded but returned no output. Treat this as invalid schema.
		return nil, fmt.Errorf("%w: empty response from caam switch", ErrSchemaValidation)
	}
	result := SwitchResult{
		Success:         response.Success,
		Provider:        caamProviderForNTM(provider),
		PreviousAccount: response.PreviousProfile,
		NewAccount:      response.Profile,
		Error:           response.Error,
	}

	// Invalidate cache after switch attempt
	caamStatusMutex.Lock()
	caamStatusExpiry = time.Time{}
	caamStatusMutex.Unlock()

	// Command failures should still surface as errors even if JSON was parseable.
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		if result.Error != "" {
			return &result, fmt.Errorf("caam switch failed: %s", result.Error)
		}
		return &result, fmt.Errorf("failed to switch account: %w", err)
	}

	// Return error if switch was not successful
	if !result.Success {
		if result.Error != "" {
			return &result, fmt.Errorf("caam switch failed: %s", result.Error)
		}
		return &result, fmt.Errorf("%w: caam switch returned success=false without error details", ErrSchemaValidation)
	}

	return &result, nil
}

// InvalidateCache forces the next GetStatus call to fetch fresh data
func (a *CAAMAdapter) InvalidateCache() {
	caamStatusMutex.Lock()
	caamStatusExpiry = time.Time{}
	caamStatusMutex.Unlock()
}

// IsAvailable is a convenience method that returns true if caam is installed
// and has at least one account configured
func (a *CAAMAdapter) IsAvailable(ctx context.Context) bool {
	status, err := a.GetStatus(ctx)
	if err != nil {
		return false
	}
	return status.Available && status.AccountsCount > 0
}

// HasMultipleAccounts returns true if caam has more than one account configured
func (a *CAAMAdapter) HasMultipleAccounts(ctx context.Context) bool {
	status, err := a.GetStatus(ctx)
	if err != nil {
		return false
	}
	return status.AccountsCount > 1
}

// RateLimitDetector monitors agent output for rate limit signals and triggers CAAM account switching.
// It uses pattern matching to detect rate limit messages from different AI providers.
type RateLimitDetector struct {
	adapter        *CAAMAdapter
	patterns       map[string][]*rateLimitPattern // provider -> patterns
	onLimitDetect  RateLimitCallback              // Called when rate limit is detected
	mu             sync.RWMutex
	lastDetection  map[string]time.Time // provider -> last detection time
	cooldownPeriod time.Duration        // Minimum time between detections per provider
}

// rateLimitPattern holds a compiled regex pattern and its metadata
type rateLimitPattern struct {
	pattern *regexp.Regexp
	name    string // Human-readable name for logging
}

// RateLimitCallback is called when a rate limit is detected
type RateLimitCallback func(provider string, paneID string, patterns []string)

// RateLimitEvent contains information about a detected rate limit
type RateLimitEvent struct {
	Provider      string    `json:"provider"`       // claude, openai, gemini
	PaneID        string    `json:"pane_id"`        // Pane where limit was detected
	DetectedAt    time.Time `json:"detected_at"`    // When limit was detected
	Patterns      []string  `json:"patterns"`       // Which patterns matched
	WaitSeconds   int       `json:"wait_seconds"`   // Suggested wait time (if detected)
	AccountBefore string    `json:"account_before"` // Account before switch (if any)
	AccountAfter  string    `json:"account_after"`  // Account after switch (if any)
	SwitchSuccess bool      `json:"switch_success"` // Whether account switch succeeded
}

// NewRateLimitDetector creates a new rate limit detector with default patterns
func NewRateLimitDetector(adapter *CAAMAdapter) *RateLimitDetector {
	d := &RateLimitDetector{
		adapter:        adapter,
		patterns:       make(map[string][]*rateLimitPattern),
		lastDetection:  make(map[string]time.Time),
		cooldownPeriod: 30 * time.Second, // Minimum 30 seconds between detections
	}

	// Initialize patterns for each provider
	d.initClaudePatterns()
	d.initOpenAIPatterns()
	d.initGeminiPatterns()

	return d
}

// initClaudePatterns sets up rate limit patterns for Claude
func (d *RateLimitDetector) initClaudePatterns() {
	patterns := []string{
		`(?i)you['']?ve\s+hit\s+your\s+limit`,
		`(?i)please\s+wait`,
		`(?i)try\s+again\s+later`,
		`(?i)too\s+many\s+requests`,
		`(?i)usage\s+limit`,
		`(?i)request\s+limit`,
		`(?i)limit.*exceeded`,
		`(?i)api\s+limit`,
		`(?i)anthropic.*limit`,
		`(?i)claude.*limit`,
	}
	d.patterns["claude"] = compilePatterns(patterns)
}

// initOpenAIPatterns sets up rate limit patterns for OpenAI/Codex
func (d *RateLimitDetector) initOpenAIPatterns() {
	patterns := []string{
		`(?i)you['']?ve\s+reached\s+your\s+usage\s+limit`,
		`(?i)rate[\s_-]*limit`,
		`(?i)quota\s+exceeded`,
		`(?i)capacity\s+reached`,
		`(?i)maximum\s+requests`,
		`(?i)429`,
		`(?i)tokens?\s+per\s+min`,
		`(?i)requests?\s+per\s+min`,
		`(?i)openai.*limit`,
		`(?i)gpt.*limit`,
		`(?i)codex.*limit`,
	}
	d.patterns["openai"] = compilePatterns(patterns)
}

// initGeminiPatterns sets up rate limit patterns for Gemini
func (d *RateLimitDetector) initGeminiPatterns() {
	patterns := []string{
		`(?i)resource[\s_-]*exhausted`,
		`(?i)RESOURCE_EXHAUSTED`,
		`(?i)limit\s+reached`,
		`(?i)gemini.*limit`,
		`(?i)google.*limit`,
		`(?i)bard.*limit`,
	}
	d.patterns["gemini"] = compilePatterns(patterns)
}

// compilePatterns compiles string patterns into regex
func compilePatterns(patterns []string) []*rateLimitPattern {
	result := make([]*rateLimitPattern, 0, len(patterns))
	for _, p := range patterns {
		compiled, err := regexp.Compile(p)
		if err != nil {
			slog.Warn("caam: invalid rate-limit pattern", "pattern", p, "error", err)
			continue
		}
		result = append(result, &rateLimitPattern{
			pattern: compiled,
			name:    p,
		})
	}
	return result
}

// SetCallback sets the callback function for rate limit detection
func (d *RateLimitDetector) SetCallback(cb RateLimitCallback) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onLimitDetect = cb
}

// SetCooldownPeriod sets the minimum time between detections per provider
func (d *RateLimitDetector) SetCooldownPeriod(period time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cooldownPeriod = period
}

// Check analyzes output for rate limit signals and triggers callback if found.
// It returns the detected event if a rate limit was found, nil otherwise.
// Providers are checked in order: claude, openai, gemini (deterministic).
func (d *RateLimitDetector) Check(output string, paneID string) *RateLimitEvent {
	// Check providers in deterministic order
	providerOrder := []string{"claude", "openai", "gemini"}
	for _, provider := range providerOrder {
		patterns := d.patterns[provider]
		if patterns == nil {
			continue
		}
		matched := d.matchPatterns(output, patterns)
		if len(matched) > 0 {
			// Check cooldown
			d.mu.RLock()
			lastTime := d.lastDetection[provider]
			cooldown := d.cooldownPeriod
			d.mu.RUnlock()

			if time.Since(lastTime) < cooldown {
				continue // Still in cooldown
			}

			// Update last detection time
			d.mu.Lock()
			d.lastDetection[provider] = time.Now()
			callback := d.onLimitDetect
			d.mu.Unlock()

			event := &RateLimitEvent{
				Provider:   provider,
				PaneID:     paneID,
				DetectedAt: time.Now(),
				Patterns:   matched,
			}

			// Parse wait time if present
			event.WaitSeconds = parseWaitTimeFromOutput(output)

			// Invoke callback if set
			if callback != nil {
				callback(provider, paneID, matched)
			}

			return event
		}
	}
	return nil
}

// matchPatterns returns names of all patterns that matched in the output
func (d *RateLimitDetector) matchPatterns(output string, patterns []*rateLimitPattern) []string {
	var matched []string
	for _, p := range patterns {
		if p.pattern.MatchString(output) {
			matched = append(matched, p.name)
		}
	}
	return matched
}

// waitTimePatterns compiled once at package level (called from hot path).
var waitTimePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)try\s+again\s+in\s+(\d+)\s*s`),
	regexp.MustCompile(`(?i)wait\s+(\d+)\s*(?:second|sec|s)`),
	regexp.MustCompile(`(?i)retry\s+(?:after|in)\s+(\d+)\s*(?:s|sec)`),
	regexp.MustCompile(`(?i)(\d+)\s*(?:second|sec)s?\s+(?:cooldown|delay|wait)`),
	regexp.MustCompile(`(?i)rate.?limit.*?(\d+)\s*s`),
}

// parseWaitTimeFromOutput extracts wait time from rate limit messages
func parseWaitTimeFromOutput(output string) int {
	for _, pattern := range waitTimePatterns {
		if matches := pattern.FindStringSubmatch(output); len(matches) > 1 {
			var seconds int
			if _, err := fmt.Sscanf(matches[1], "%d", &seconds); err == nil && seconds > 0 {
				return seconds
			}
		}
	}
	return 0
}

// TriggerAccountSwitch attempts to switch CAAM accounts when rate limit is detected.
// Uses the new SwitchToNextAccount method which calls `caam switch <provider> --next --json`.
// Returns the event with switch results populated.
func (d *RateLimitDetector) TriggerAccountSwitch(ctx context.Context, event *RateLimitEvent) *RateLimitEvent {
	if d.adapter == nil {
		return event
	}

	// Check if CAAM is available and has multiple accounts
	if !d.adapter.IsAvailable(ctx) {
		return event
	}
	if !d.adapter.HasMultipleAccounts(ctx) {
		return event
	}

	// Use the provider from the event, fallback to attempting detection
	provider := event.Provider
	if provider == "" || provider == "unknown" {
		return event // Can't switch without knowing the provider
	}

	// Use the new SwitchToNextAccount method
	result, err := d.adapter.SwitchToNextAccount(ctx, provider)
	if err != nil {
		// Switch failed - populate event with error info
		event.SwitchSuccess = false
		if result != nil {
			event.AccountBefore = result.PreviousAccount
		}
		return event
	}

	// Populate event with switch results
	event.AccountBefore = result.PreviousAccount
	event.AccountAfter = result.NewAccount
	event.SwitchSuccess = result.Success

	return event
}

// DetectProvider attempts to determine the AI provider from pane output
func DetectProvider(output string) string {
	outputLower := strings.ToLower(output)

	// Claude indicators
	if strings.Contains(outputLower, "claude") ||
		strings.Contains(outputLower, "anthropic") ||
		strings.Contains(outputLower, "sonnet") ||
		strings.Contains(outputLower, "opus") ||
		strings.Contains(outputLower, "haiku") {
		return "claude"
	}

	// OpenAI/Codex indicators
	if strings.Contains(outputLower, "openai") ||
		strings.Contains(outputLower, "codex") ||
		strings.Contains(outputLower, "gpt-") ||
		strings.Contains(outputLower, "gpt4") ||
		strings.Contains(outputLower, "gpt5") {
		return "openai"
	}

	// Gemini indicators
	if strings.Contains(outputLower, "gemini") ||
		strings.Contains(outputLower, "google") ||
		strings.Contains(outputLower, "bard") {
		return "gemini"
	}

	return "unknown"
}
