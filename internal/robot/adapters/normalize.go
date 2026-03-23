package adapters

import (
	"context"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
)

// SignalAdapter normalizes a signal source to projection sections
type SignalAdapter interface {
	// Name returns the adapter identifier
	Name() string

	// Available returns true if the signal source is reachable
	Available(ctx context.Context) bool

	// Collect gathers current signal state
	Collect(ctx context.Context) (*SignalBatch, error)

	// LastError returns the most recent collection error
	LastError() error
}

// SignalBatch contains normalized signals from a source
type SignalBatch struct {
	Source       string
	CollectedAt  time.Time
	Work         *WorkSection
	Coordination *CoordinationSection
	Quota        *QuotaSection
	Alerts       *AlertsSection
	Health       *SourceHealthSection
}

// AggregatedSignals contains merged signals from all adapters
type AggregatedSignals struct {
	CollectedAt      time.Time
	Work             *WorkSection
	Coordination     *CoordinationSection
	Quota            *QuotaSection
	Alerts           *AlertsSection
	Health           *SourceHealthSection
	DegradedFeatures []DegradedFeature
	CollectionErrors []error
}

// QuotaSection normalized quota information
type QuotaSection struct {
	Accounts  []AccountQuota `json:"accounts"`
	Summary   *QuotaSummary  `json:"summary,omitempty"`
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
}

// AccountQuota per-provider quota state
type AccountQuota struct {
	ID            string     `json:"id"`
	Provider      string     `json:"provider"`
	Model         string     `json:"model,omitempty"`
	UsagePercent  *float64   `json:"usage_percent,omitempty"`
	TokensUsed    int64      `json:"tokens_used"`
	TokensLimit   int64      `json:"tokens_limit,omitempty"`
	RequestsUsed  int        `json:"requests_used"`
	RequestsLimit int        `json:"requests_limit,omitempty"`
	CostUSD       float64    `json:"cost_usd,omitempty"`
	Status        string     `json:"status"` // ok, warning, exceeded, suspended
	ReasonCode    ReasonCode `json:"reason_code"`
	ResetAt       string     `json:"reset_at,omitempty"`
	IsActive      bool       `json:"is_active"`
	IsPrimary     bool       `json:"is_primary,omitempty"`
}

// QuotaSummary aggregate quota state
type QuotaSummary struct {
	TotalAccounts         int    `json:"total_accounts"`
	HealthyAccounts       int    `json:"healthy_accounts"`
	WarningAccounts       int    `json:"warning_accounts"`
	ExceededAccounts      int    `json:"exceeded_accounts"`
	LowestTokensRemaining int64  `json:"lowest_tokens_remaining,omitempty"`
	NextReset             string `json:"next_reset,omitempty"`
}

// AlertsSection normalized alerts
type AlertsSection struct {
	Active          []AlertItem    `json:"active"`
	Summary         *AlertsSummary `json:"summary,omitempty"`
	RecentlyCleared []string       `json:"recently_cleared,omitempty"`
}

// AlertItem normalized alert
type AlertItem struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Severity    string                 `json:"severity"`
	Message     string                 `json:"message"`
	Session     string                 `json:"session,omitempty"`
	Pane        string                 `json:"pane,omitempty"`
	Agent       string                 `json:"agent,omitempty"`
	BeadID      string                 `json:"bead_id,omitempty"`
	ReasonCode  ReasonCode             `json:"reason_code"`
	Details     map[string]interface{} `json:"details,omitempty"`
	Count       int                    `json:"count"`
	CreatedAt   string                 `json:"created_at"`
	UpdatedAt   string                 `json:"updated_at"`
	DurationMs  int64                  `json:"duration_ms"`
	AutoClears  bool                   `json:"auto_clears"`
	Dismissable bool                   `json:"dismissable"`
}

// AlertsSummary aggregate alert state
type AlertsSummary struct {
	TotalActive int            `json:"total_active"`
	BySeverity  map[string]int `json:"by_severity"`
	ByType      map[string]int `json:"by_type"`
	OldestAlert string         `json:"oldest_alert,omitempty"`
}

// SourceHealthSection normalized source health
type SourceHealthSection struct {
	Sources  map[string]SourceInfo `json:"sources"`
	Degraded []string              `json:"degraded,omitempty"`
	AllFresh bool                  `json:"all_fresh"`
}

// SourceInfo per-source health state
type SourceInfo struct {
	Name           string     `json:"name"`
	Available      bool       `json:"available"`
	Fresh          bool       `json:"fresh"`
	ReasonCode     ReasonCode `json:"reason_code"`
	AgeMs          int64      `json:"age_ms,omitempty"`
	UpdatedAt      string     `json:"updated_at,omitempty"`
	Degraded       bool       `json:"degraded,omitempty"`
	DegradedSince  string     `json:"degraded_since,omitempty"`
	DegradedReason string     `json:"degraded_reason,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	RetryingAt     string     `json:"retrying_at,omitempty"`
}

// HealthSource represents a source's health state for normalization
type HealthSource struct {
	Available  bool
	Fresh      bool
	LastUpdate *time.Time
	DegradedAt time.Time
	Reason     string
	LastError  string
}

// SourceHealthConfig controls staleness and degradation thresholds
type SourceHealthConfig struct {
	// StaleAfter is how long before collected data is considered stale
	StaleAfter time.Duration
	// DegradedAfter is how long an error state persists before marking degraded
	DegradedAfter time.Duration
}

// DefaultSourceHealthConfig returns conservative defaults for source health
func DefaultSourceHealthConfig() SourceHealthConfig {
	return SourceHealthConfig{
		StaleAfter:    30 * time.Second,
		DegradedAfter: 60 * time.Second,
	}
}

// AdapterResult captures the outcome of an adapter collection for health computation
type AdapterResult struct {
	Name        string
	Available   bool
	CollectedAt time.Time
	Error       error
	Batch       *SignalBatch
}

// ComputeSourceHealth computes deterministic source health from adapter results.
// This is the canonical source of freshness and degradation semantics.
func ComputeSourceHealth(results []AdapterResult, config SourceHealthConfig, now time.Time) *SourceHealthSection {
	sources := make(map[string]HealthSource, len(results))

	for _, r := range results {
		source := HealthSource{
			Available: r.Available && r.Error == nil,
			Fresh:     true,
		}

		// Compute freshness from collection timestamp
		if !r.CollectedAt.IsZero() {
			source.LastUpdate = &r.CollectedAt
			age := now.Sub(r.CollectedAt)
			if age > config.StaleAfter {
				source.Fresh = false
				source.Reason = "stale: " + age.Round(time.Second).String() + " since last collection"
			}
		} else if r.Available {
			// Available but no collection timestamp means first collection pending
			source.Fresh = false
			source.Reason = "awaiting first collection"
		}

		// Handle collection errors
		if r.Error != nil {
			source.Available = false
			source.Fresh = false
			source.LastError = r.Error.Error()
			source.Reason = "collection failed"
			source.DegradedAt = now
		}

		// Handle unavailable sources
		if !r.Available {
			source.Fresh = false
			if source.Reason == "" {
				source.Reason = "source unavailable"
			}
			if source.DegradedAt.IsZero() {
				source.DegradedAt = now
			}
		}

		sources[r.Name] = source
	}

	return NormalizeHealth(sources)
}

// DegradedFeature describes a feature that is degraded due to source issues
type DegradedFeature struct {
	Feature       string   `json:"feature"`
	Reason        string   `json:"reason"`
	AffectedBy    []string `json:"affected_by"`
	Severity      string   `json:"severity"` // warning, error
	Actionability string   `json:"actionability"`
}

// ComputeDegradedFeatures determines which features are degraded based on source health.
// This provides actionable guidance about what won't work correctly.
func ComputeDegradedFeatures(health *SourceHealthSection) []DegradedFeature {
	if health == nil || health.AllFresh {
		return nil
	}

	var features []DegradedFeature

	// Map sources to dependent features
	featureDeps := map[string][]string{
		"work_coordination": {"work_section", "coordination_section", "bead_triage"},
		"tmux":              {"session_list", "agent_detection", "pane_output"},
		"caut":              {"quota_status", "account_rotation"},
		"beads":             {"work_section", "bead_triage", "dependency_graph"},
		"mail":              {"coordination_section", "agent_mail_status"},
		"alerts":            {"alerts_section", "incident_detection"},
	}

	affectedFeatures := make(map[string][]string) // feature -> affected sources

	for _, sourceName := range health.Degraded {
		if features, ok := featureDeps[sourceName]; ok {
			for _, feature := range features {
				affectedFeatures[feature] = append(affectedFeatures[feature], sourceName)
			}
		}
	}

	for feature, sources := range affectedFeatures {
		severity := "warning"
		actionability := "background"

		// Escalate if multiple sources affect the same feature
		if len(sources) > 1 {
			severity = "error"
			actionability = "action_required"
		}

		// Escalate for critical features
		switch feature {
		case "session_list", "agent_detection":
			severity = "error"
			actionability = "action_required"
		}

		features = append(features, DegradedFeature{
			Feature:       feature,
			Reason:        "dependent source(s) degraded",
			AffectedBy:    sources,
			Severity:      severity,
			Actionability: actionability,
		})
	}

	return features
}

// FormatTimestamp formats time as RFC3339
func FormatTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// matchesPattern checks if value matches pattern using glob semantics.
// Supports * for any sequence and ? for single character.
func matchesPattern(pattern, value string) bool {
	if pattern == "" || value == "" {
		return false
	}
	if pattern == value {
		return true
	}
	if pattern == "*" {
		return true
	}

	// Simple glob matching
	pIdx, vIdx := 0, 0
	starIdx, matchIdx := -1, 0

	for vIdx < len(value) {
		if pIdx < len(pattern) && (pattern[pIdx] == '?' || pattern[pIdx] == value[vIdx]) {
			pIdx++
			vIdx++
		} else if pIdx < len(pattern) && pattern[pIdx] == '*' {
			starIdx = pIdx
			matchIdx = vIdx
			pIdx++
		} else if starIdx != -1 {
			pIdx = starIdx + 1
			matchIdx++
			vIdx = matchIdx
		} else {
			return false
		}
	}

	for pIdx < len(pattern) && pattern[pIdx] == '*' {
		pIdx++
	}

	return pIdx == len(pattern)
}

// NormalizeQuota transforms caut cache data to QuotaSection
func NormalizeQuota(poller *caut.UsagePoller) *QuotaSection {
	section := &QuotaSection{
		Accounts:  make([]AccountQuota, 0),
		Available: poller != nil,
	}

	if poller == nil {
		section.Reason = "caut unavailable"
		return section
	}

	cache := poller.GetCache()
	if cache == nil {
		section.Reason = "cache empty"
		return section
	}

	status := cache.GetStatus()
	usages := cache.GetAllUsage()

	for _, usage := range usages {
		account := AccountQuota{
			ID:           usage.Provider,
			Provider:     usage.Provider,
			TokensUsed:   usage.TokensIn + usage.TokensOut,
			RequestsUsed: usage.RequestCount,
			CostUSD:      usage.Cost,
			Status:       "ok",
			ReasonCode:   ReasonQuotaOK,
			IsActive:     true,
		}

		// Compute status from usage if we have status info
		if status != nil {
			for _, p := range status.Providers {
				if p.Name == usage.Provider {
					quotaUsed := p.QuotaUsed
					account.UsagePercent = &quotaUsed
					account.ReasonCode = computeQuotaReasonCode(p.QuotaUsed)
					account.Status = reasonToStatus(account.ReasonCode)
					break
				}
			}
		}

		section.Accounts = append(section.Accounts, account)
	}

	section.Summary = computeQuotaSummary(section.Accounts)
	return section
}

func computeQuotaReasonCode(usagePercent float64) ReasonCode {
	switch {
	case usagePercent >= 100.0:
		return ReasonQuotaExceededTokens
	case usagePercent >= 95.0:
		return ReasonQuotaCriticalTokens
	case usagePercent >= 80.0:
		return ReasonQuotaWarningTokens
	default:
		return ReasonQuotaOK
	}
}

func reasonToStatus(code ReasonCode) string {
	switch code {
	case ReasonQuotaExceededTokens, ReasonQuotaExceededRequests,
		ReasonQuotaExceededCost, ReasonQuotaSuspended:
		return "exceeded"
	case ReasonQuotaCriticalTokens, ReasonQuotaCriticalRequests:
		return "critical"
	case ReasonQuotaWarningTokens, ReasonQuotaWarningRequests,
		ReasonQuotaWarningRateLimit:
		return "warning"
	default:
		return "ok"
	}
}

func computeQuotaSummary(accounts []AccountQuota) *QuotaSummary {
	summary := &QuotaSummary{
		TotalAccounts: len(accounts),
	}

	for _, a := range accounts {
		switch a.Status {
		case "ok":
			summary.HealthyAccounts++
		case "warning", "critical":
			summary.WarningAccounts++
		case "exceeded":
			summary.ExceededAccounts++
		}
	}

	return summary
}

// NormalizeAlerts transforms alerts.Alert slice to AlertsSection
func NormalizeAlerts(alertList []alerts.Alert) *AlertsSection {
	section := &AlertsSection{
		Active: make([]AlertItem, 0, len(alertList)),
		Summary: &AlertsSummary{
			BySeverity: make(map[string]int),
			ByType:     make(map[string]int),
		},
	}

	var oldest time.Time

	for _, a := range alertList {
		if a.IsResolved() {
			continue
		}

		item := AlertItem{
			ID:          a.ID,
			Type:        mapAlertType(a.Type),
			Severity:    string(a.Severity),
			Message:     a.Message,
			Session:     a.Session,
			Pane:        a.Pane,
			BeadID:      a.BeadID,
			ReasonCode:  computeAlertReasonCode(a.Type),
			Details:     a.Context,
			Count:       a.Count,
			CreatedAt:   FormatTimestamp(a.CreatedAt),
			UpdatedAt:   FormatTimestamp(a.LastSeenAt),
			DurationMs:  a.Duration().Milliseconds(),
			AutoClears:  alertAutoClears(a.Type),
			Dismissable: true,
		}

		section.Active = append(section.Active, item)
		section.Summary.TotalActive++
		section.Summary.BySeverity[item.Severity]++
		section.Summary.ByType[item.Type]++

		if oldest.IsZero() || a.CreatedAt.Before(oldest) {
			oldest = a.CreatedAt
		}
	}

	if !oldest.IsZero() {
		section.Summary.OldestAlert = FormatTimestamp(oldest)
	}

	return section
}

func mapAlertType(t alerts.AlertType) string {
	switch t {
	case alerts.AlertAgentStuck:
		return "agent_stuck"
	case alerts.AlertAgentCrashed:
		return "agent_crashed"
	case alerts.AlertAgentError:
		return "agent_error"
	case alerts.AlertHighCPU:
		return "system_cpu"
	case alerts.AlertDiskLow:
		return "system_disk"
	case alerts.AlertBeadStale:
		return "bead_stale"
	case alerts.AlertMailBacklog:
		return "mail_backlog"
	case alerts.AlertDependencyCycle:
		return "bead_cycle"
	case alerts.AlertRateLimit:
		return "rate_limit"
	case alerts.AlertFileConflict:
		return "file_conflict"
	case alerts.AlertContextWarning:
		return "context_warning"
	case alerts.AlertRotationStarted:
		return "rotation_started"
	case alerts.AlertRotationComplete:
		return "rotation_complete"
	case alerts.AlertRotationFailed:
		return "rotation_failed"
	case alerts.AlertCompactionTriggered:
		return "compaction_triggered"
	case alerts.AlertCompactionComplete:
		return "compaction_complete"
	case alerts.AlertCompactionFailed:
		return "compaction_failed"
	case alerts.AlertQuotaWarning:
		return "quota_warning"
	case alerts.AlertQuotaCritical:
		return "quota_critical"
	default:
		return string(t)
	}
}

func computeAlertReasonCode(t alerts.AlertType) ReasonCode {
	switch t {
	case alerts.AlertAgentStuck:
		return ReasonAlertAgentStuck
	case alerts.AlertAgentCrashed:
		return ReasonAlertAgentCrashed
	case alerts.AlertAgentError:
		return ReasonAlertAgentError
	case alerts.AlertDiskLow:
		return ReasonAlertSystemDiskLow
	case alerts.AlertHighCPU:
		return ReasonAlertSystemCPUHigh
	case alerts.AlertBeadStale:
		return ReasonAlertBeadStale
	case alerts.AlertMailBacklog:
		return ReasonAlertMailBacklog
	case alerts.AlertFileConflict:
		return ReasonAlertConflictFile
	case alerts.AlertRateLimit:
		return ReasonAlertAgentRateLimited
	case alerts.AlertContextWarning:
		return ReasonAlertAgentContext
	case alerts.AlertRotationStarted:
		return ReasonAlertRotationStarted
	case alerts.AlertRotationComplete:
		return ReasonAlertRotationComplete
	case alerts.AlertRotationFailed:
		return ReasonAlertRotationFailed
	case alerts.AlertCompactionTriggered:
		return ReasonAlertCompactionTriggered
	case alerts.AlertCompactionComplete:
		return ReasonAlertCompactionComplete
	case alerts.AlertCompactionFailed:
		return ReasonAlertCompactionFailed
	case alerts.AlertQuotaWarning:
		return ReasonQuotaWarningTokens
	case alerts.AlertQuotaCritical:
		return ReasonQuotaCriticalTokens
	default:
		return ReasonAlertAgentError
	}
}

func alertAutoClears(t alerts.AlertType) bool {
	switch t {
	case alerts.AlertAgentCrashed, alerts.AlertRotationFailed,
		alerts.AlertCompactionFailed:
		return false
	default:
		return true
	}
}

// NormalizeHealth transforms health sources to SourceHealthSection
func NormalizeHealth(sources map[string]HealthSource) *SourceHealthSection {
	section := &SourceHealthSection{
		Sources:  make(map[string]SourceInfo),
		Degraded: []string{},
		AllFresh: true,
	}

	for name, source := range sources {
		info := SourceInfo{
			Name:       name,
			Available:  source.Available,
			Fresh:      source.Fresh,
			ReasonCode: computeHealthReasonCode(source),
		}

		if source.LastUpdate != nil {
			info.AgeMs = time.Since(*source.LastUpdate).Milliseconds()
			info.UpdatedAt = FormatTimestamp(*source.LastUpdate)
		}

		if !source.Available || !source.Fresh {
			section.AllFresh = false
			info.Degraded = true
			info.DegradedSince = FormatTimestamp(source.DegradedAt)
			info.DegradedReason = source.Reason
			section.Degraded = append(section.Degraded, name)
		}

		if source.LastError != "" {
			info.LastError = source.LastError
		}

		section.Sources[name] = info
	}

	return section
}

func computeHealthReasonCode(source HealthSource) ReasonCode {
	if !source.Available {
		return ReasonHealthSourceUnavailable
	}
	if !source.Fresh {
		return ReasonHealthSourceStale
	}
	if source.Reason != "" {
		return ReasonHealthSourceDegraded
	}
	return ReasonHealthOK
}

// SignalAggregator combines signals from multiple adapters
type SignalAggregator struct {
	adapters     []SignalAdapter
	mu           sync.RWMutex
	cache        map[string]*SignalBatch
	cacheTTL     time.Duration
	healthConfig SourceHealthConfig
}

// NewSignalAggregator creates a new aggregator
func NewSignalAggregator(ttl time.Duration) *SignalAggregator {
	return &SignalAggregator{
		adapters:     make([]SignalAdapter, 0),
		cache:        make(map[string]*SignalBatch),
		cacheTTL:     ttl,
		healthConfig: DefaultSourceHealthConfig(),
	}
}

// SetHealthConfig configures staleness and degradation thresholds
func (a *SignalAggregator) SetHealthConfig(config SourceHealthConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.healthConfig = config
}

// RegisterAdapter adds an adapter to the aggregator
func (a *SignalAggregator) RegisterAdapter(adapter SignalAdapter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.adapters = append(a.adapters, adapter)
}

// Collect aggregates signals from all adapters and computes deterministic source health
func (a *SignalAggregator) Collect(ctx context.Context) (*AggregatedSignals, error) {
	now := time.Now()
	result := &AggregatedSignals{
		CollectedAt:  now,
		Work:         NewWorkSection(),
		Coordination: NewCoordinationSection(),
		Quota:        &QuotaSection{Accounts: []AccountQuota{}},
		Alerts:       &AlertsSection{Active: []AlertItem{}, Summary: &AlertsSummary{BySeverity: make(map[string]int), ByType: make(map[string]int)}},
		Health:       &SourceHealthSection{Sources: make(map[string]SourceInfo)},
	}

	a.mu.RLock()
	adapters := a.adapters
	healthConfig := a.healthConfig
	a.mu.RUnlock()

	// Collect from all adapters in parallel, capturing results for health computation
	type adapterOutcome struct {
		adapter SignalAdapter
		batch   *SignalBatch
		err     error
	}

	outcomes := make([]adapterOutcome, len(adapters))
	var wg sync.WaitGroup

	for i, adapter := range adapters {
		wg.Add(1)
		go func(idx int, ad SignalAdapter) {
			defer wg.Done()
			batch, err := ad.Collect(ctx)
			outcomes[idx] = adapterOutcome{adapter: ad, batch: batch, err: err}
		}(i, adapter)
	}

	wg.Wait()

	// Build adapter results for health computation and merge batches
	adapterResults := make([]AdapterResult, 0, len(outcomes))
	var errs []error

	for _, outcome := range outcomes {
		// Build health result
		ar := AdapterResult{
			Name:      outcome.adapter.Name(),
			Available: outcome.adapter.Available(ctx),
			Error:     outcome.err,
			Batch:     outcome.batch,
		}
		if outcome.batch != nil {
			ar.CollectedAt = outcome.batch.CollectedAt
		}
		adapterResults = append(adapterResults, ar)

		// Track errors
		if outcome.err != nil {
			errs = append(errs, outcome.err)
			continue
		}

		// Merge batch data
		mergeSignalBatch(result, outcome.batch)
	}

	// Compute deterministic source health from adapter results
	result.Health = ComputeSourceHealth(adapterResults, healthConfig, now)
	result.DegradedFeatures = ComputeDegradedFeatures(result.Health)

	if len(errs) > 0 {
		result.CollectionErrors = errs
	}

	return result, nil
}

func mergeSignalBatch(target *AggregatedSignals, batch *SignalBatch) {
	if batch == nil {
		return
	}

	if batch.Work != nil {
		target.Work = batch.Work
	}

	if batch.Coordination != nil {
		target.Coordination = batch.Coordination
	}

	if batch.Quota != nil {
		target.Quota.Accounts = append(target.Quota.Accounts, batch.Quota.Accounts...)
		if batch.Quota.Summary != nil && target.Quota.Summary == nil {
			target.Quota.Summary = batch.Quota.Summary
		}
	}

	if batch.Alerts != nil {
		target.Alerts.Active = append(target.Alerts.Active, batch.Alerts.Active...)
		if batch.Alerts.Summary != nil {
			target.Alerts.Summary.TotalActive += batch.Alerts.Summary.TotalActive
			for k, v := range batch.Alerts.Summary.BySeverity {
				target.Alerts.Summary.BySeverity[k] += v
			}
			for k, v := range batch.Alerts.Summary.ByType {
				target.Alerts.Summary.ByType[k] += v
			}
		}
	}

	if batch.Health != nil {
		for k, v := range batch.Health.Sources {
			target.Health.Sources[k] = v
		}
		target.Health.Degraded = append(target.Health.Degraded, batch.Health.Degraded...)
		if !batch.Health.AllFresh {
			target.Health.AllFresh = false
		}
	}
}
