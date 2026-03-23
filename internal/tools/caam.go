package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

// Version returns the installed caam version
func (a *CAAMAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get caam version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
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

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("caam at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "caam is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
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

// GetCredentialPath returns the expected path where caam stores credentials for a provider.
// Returns the path in ~/.config/caam/current/<provider>.json format.
func GetCredentialPath(provider string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".config", "caam", "current", provider+".json")
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
		status := caamStatusCache
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
	caamStatusCache = *status
	caamStatusExpiry = time.Now().Add(caamCacheTTL)
	caamStatusMutex.Unlock()

	return status, nil
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

	// Get accounts list
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "list", "--json")
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

	// Parse accounts list
	var accounts []CAAMAccount
	if err := json.Unmarshal(output, &accounts); err != nil {
		// Try parsing as a status object instead
		var statusResp struct {
			Accounts []CAAMAccount `json:"accounts"`
		}
		if err := json.Unmarshal(output, &statusResp); err == nil {
			accounts = statusResp.Accounts
		}
	}

	status.applyAccounts(accounts)

	return status, nil
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
// This calls `caam creds <provider> --json` to get the current active credentials.
// The returned credentials include the environment variable name and optionally the token path.
func (a *CAAMAdapter) GetCurrentCredentials(ctx context.Context, provider string) (*CAAMCredentials, error) {
	path, installed := a.Detect()
	if !installed {
		return nil, ErrToolNotInstalled
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "creds", provider, "--json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// caam creds might not exist in older versions - fallback to constructed credentials
		return a.constructCredentials(ctx, provider)
	}

	output := stdout.Bytes()
	if len(output) == 0 || !json.Valid(output) {
		// Fallback to constructed credentials
		return a.constructCredentials(ctx, provider)
	}

	var creds CAAMCredentials
	if err := json.Unmarshal(output, &creds); err != nil {
		return a.constructCredentials(ctx, provider)
	}

	// Ensure env var name is set
	if creds.EnvVarName == "" {
		creds.EnvVarName = GetCredentialEnvVar(provider)
	}

	return &creds, nil
}

// constructCredentials builds credentials from available information when caam creds isn't available
func (a *CAAMAdapter) constructCredentials(ctx context.Context, provider string) (*CAAMCredentials, error) {
	// Get active account for the provider
	accounts, err := a.GetAccounts(ctx)
	if err != nil {
		return nil, err
	}

	var activeAccount *CAAMAccount
	for i := range accounts {
		if accounts[i].Provider == provider && accounts[i].Active {
			activeAccount = &accounts[i]
			break
		}
	}

	creds := &CAAMCredentials{
		Provider:   provider,
		EnvVarName: GetCredentialEnvVar(provider),
		TokenPath:  GetCredentialPath(provider),
	}

	if activeAccount != nil {
		creds.AccountID = activeAccount.ID
		creds.RateLimited = activeAccount.RateLimited
	}

	return creds, nil
}

// SwitchAccount switches to a different account
func (a *CAAMAdapter) SwitchAccount(ctx context.Context, accountID string) error {
	path, installed := a.Detect()
	if !installed {
		return ErrToolNotInstalled
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "switch", accountID)
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ErrTimeout
		}
		return fmt.Errorf("failed to switch account: %w: %s", err, stderr.String())
	}

	// Invalidate cache after switch
	caamStatusMutex.Lock()
	caamStatusExpiry = time.Time{}
	caamStatusMutex.Unlock()

	return nil
}

// SwitchToNextAccount switches to the next available account for a provider.
// It calls `caam switch <provider> --next --json` and returns the structured result.
// This is the preferred method for automatic account switching on rate limit.
func (a *CAAMAdapter) SwitchToNextAccount(ctx context.Context, provider string) (*SwitchResult, error) {
	path, installed := a.Detect()
	if !installed {
		return nil, ErrToolNotInstalled
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "switch", provider, "--next", "--json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Parse JSON response even on error (caam may return valid JSON with error details).
	output := bytes.TrimSpace(stdout.Bytes())
	if len(output) == 0 {
		output = bytes.TrimSpace(stderr.Bytes())
	}

	var result SwitchResult
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

		if jsonErr := json.Unmarshal(output, &result); jsonErr != nil {
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

// parseWaitTimeFromOutput extracts wait time from rate limit messages
func parseWaitTimeFromOutput(output string) int {
	waitPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)try\s+again\s+in\s+(\d+)\s*s`),
		regexp.MustCompile(`(?i)wait\s+(\d+)\s*(?:second|sec|s)`),
		regexp.MustCompile(`(?i)retry\s+(?:after|in)\s+(\d+)\s*(?:s|sec)`),
		regexp.MustCompile(`(?i)(\d+)\s*(?:second|sec)s?\s+(?:cooldown|delay|wait)`),
		regexp.MustCompile(`(?i)rate.?limit.*?(\d+)\s*s`),
	}

	for _, pattern := range waitPatterns {
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
