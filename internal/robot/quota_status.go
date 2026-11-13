package robot

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func init() {
	MustRegisterSchemaPagination("quota_status", SchemaPaginationFlag{
		Reason: "bounded: one entry per configured provider with capped recent rate-limit window",
	})
	MustRegisterSchemaPagination("quota_check", SchemaPaginationFlag{
		Reason: "bounded: single-provider quota detail with capped recent rate-limit window",
	})
}

// QuotaStatusOutput represents the response from --robot-quota-status
type QuotaStatusOutput struct {
	RobotResponse
	Quota QuotaInfo `json:"quota"`
}

// ProviderRateLimitStatus summarizes adaptive rate-limit and cooldown state for a provider.
type ProviderRateLimitStatus struct {
	InCooldown         bool                       `json:"in_cooldown"`
	CooldownRemainingS int                        `json:"cooldown_remaining_sec,omitempty"`
	CurrentDelayMs     int64                      `json:"current_delay_ms"`
	TotalRateLimits    int                        `json:"total_rate_limits"`
	TotalSuccesses     int                        `json:"total_successes"`
	LastRateLimit      string                     `json:"last_rate_limit,omitempty"`
	RecentEvents       []ratelimit.RateLimitEvent `json:"recent_events,omitempty"`
}

// QuotaInfo contains quota and usage information from caut
type QuotaInfo struct {
	LastUpdated    string                             `json:"last_updated"`
	CacheUpdates   int64                              `json:"cache_updates,omitempty"`
	CautAvailable  bool                               `json:"caut_available"`
	Providers      map[string]ProviderQuota           `json:"providers"`
	RateLimits     map[string]ProviderRateLimitStatus `json:"rate_limits,omitempty"`
	TotalCostToday float64                            `json:"total_cost_today_usd"`
	TotalCostMonth float64                            `json:"total_cost_month_usd,omitempty"`
	HasWarning     bool                               `json:"has_warning"`
	HasCritical    bool                               `json:"has_critical"`
}

// ProviderQuota contains quota information for a single provider
type ProviderQuota struct {
	UsagePercent     float64                  `json:"usage_percent"`
	RequestsUsed     int                      `json:"requests_used,omitempty"`
	RequestsLimit    int                      `json:"requests_limit,omitempty"`
	TokensUsed       int64                    `json:"tokens_used,omitempty"`
	TokensLimit      int64                    `json:"tokens_limit,omitempty"`
	CostUSD          float64                  `json:"cost_usd"`
	ResetAt          string                   `json:"reset_at,omitempty"`
	ResetDescription string                   `json:"reset_description,omitempty"`
	AccountEmail     string                   `json:"account_email,omitempty"`
	WindowMinutes    int                      `json:"window_minutes,omitempty"`
	HasUsageData     bool                     `json:"has_usage_data,omitempty"`
	RateLimited      bool                     `json:"rate_limited,omitempty"`
	RateLimit        *ProviderRateLimitStatus `json:"rate_limit,omitempty"`
	Status           string                   `json:"status"` // "ok", "warning", "critical"
}

// QuotaCheckOutput represents the response from --robot-quota-check
type QuotaCheckOutput struct {
	RobotResponse
	Provider string        `json:"provider"`
	Quota    ProviderQuota `json:"quota"`
}

// canonicalRobotProvider folds every spelling of a provider onto the one key
// the robot surfaces report it under.
//
// Codex is the case that bit ntm#319. ntm stores Codex accounts under the
// provider id "openai" (caamProviderForNTM), caam's own limits command calls
// it "codex", and the agent type is "cod" — so a controller filtering with
// --provider=codex matched nothing and got available_accounts=0 on a host with
// three healthy Codex seats. All of those spellings now resolve to the same
// key. "openai" stays the canonical value so existing consumers of the
// accounts map keep reading the key they already read.
func canonicalRobotProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic", "cc":
		return "claude"
	case "gemini", "gmi", "google", "google-ai", "google_gemini", "google-gemini":
		return "gemini"
	case "openai", "codex", "cod", "chatgpt", "openai-codex", "openai_codex":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func quotaLookupProvider(provider string) string {
	switch canonicalRobotProvider(provider) {
	case "claude":
		return "anthropic"
	default:
		return canonicalRobotProvider(provider)
	}
}

// cautQuotaRefreshTimeout bounds the caut read a quota query may trigger.
const cautQuotaRefreshTimeout = 25 * time.Second

// refreshCautQuotaCache refills caut's usage cache when it is stale, so quota
// queries report what caut measures instead of an empty cache.
func refreshCautQuotaCache(poller *caut.UsagePoller) {
	ctx, cancel := context.WithTimeout(context.Background(), cautQuotaRefreshTimeout)
	defer cancel()
	poller.RefreshIfStale(ctx, caut.MaxAge)
}

// GetQuotaStatus returns quota status information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetQuotaStatus() (*QuotaStatusOutput, error) {
	poller := caut.GetGlobalPoller()
	refreshCautQuotaCache(poller)
	cache := poller.GetCache()

	snap := cache.Snapshot()

	// Check if caut is available
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	adapter := tools.NewCautAdapter()
	available := adapter.IsAvailable(ctx)

	quotaInfo := QuotaInfo{
		LastUpdated:   FormatTimestamp(snap.LastUpdated),
		CacheUpdates:  cache.GetUpdateCount(),
		CautAvailable: available,
		Providers:     make(map[string]ProviderQuota),
	}

	// Get cached status
	status := snap.Status
	if status != nil {
		quotaInfo.TotalCostToday = status.TotalSpend

		// Check for warnings/critical based on overall quota
		if status.QuotaPercent >= 95.0 {
			quotaInfo.HasCritical = true
		} else if status.QuotaPercent >= 80.0 {
			quotaInfo.HasWarning = true
		}

		// Add per-provider quota info from status
		for _, p := range status.Providers {
			if !p.Enabled {
				continue
			}
			providerName := canonicalRobotProvider(p.Name)

			providerQuota := ProviderQuota{
				UsagePercent: p.QuotaUsed,
				Status:       getQuotaStatus(p.QuotaUsed),
			}

			quotaInfo.Providers[providerName] = providerQuota

			// Track warning/critical at provider level
			if p.QuotaUsed >= 95.0 {
				quotaInfo.HasCritical = true
			} else if p.QuotaUsed >= 80.0 {
				quotaInfo.HasWarning = true
			}
		}
	}

	// Merge rate-window and identity details captured from caut payloads
	for _, detail := range snap.ProviderDetails {
		providerName := canonicalRobotProvider(detail.Provider)
		providerQuota, exists := quotaInfo.Providers[providerName]
		if !exists {
			providerQuota = ProviderQuota{Status: "ok"}
		}
		applyCautProviderDetail(&providerQuota, detail)
		if detail.RateLimited {
			quotaInfo.HasCritical = true
		}
		quotaInfo.Providers[providerName] = providerQuota
	}

	// Add usage data from cache
	for _, usage := range snap.Usage {
		providerName := canonicalRobotProvider(usage.Provider)
		providerQuota, exists := quotaInfo.Providers[providerName]
		if !exists {
			providerQuota = ProviderQuota{
				Status: "ok",
			}
		}

		providerQuota.RequestsUsed = usage.RequestCount
		providerQuota.TokensUsed = usage.TokensIn + usage.TokensOut
		providerQuota.CostUSD = usage.Cost

		quotaInfo.Providers[providerName] = providerQuota
	}

	// Overlay CAAM's live subscription windows (ntm#319).
	//
	// Everything above reads only caut's poller cache, which covers API-key
	// spend. A subscription seat (Claude Max, ChatGPT Pro) has no caut usage
	// row at all, so on a host where `caam limits` answers happily this
	// surface still reported providers:{} — the controller could see
	// caut_available:true and conclude there were no windows to respect.
	// caut's numbers stay authoritative where it has them; caam only fills
	// what caut left blank.
	//
	// Deliberately NOT the ctx above: that one is a 5-second budget for a
	// local availability probe, while `caam limits` queries provider APIs and
	// caam allows itself 60s for them. Inheriting the 5s would make every
	// overlay call time out and the fix silently do nothing in production
	// while stubbed tests still passed.
	limitsCtx, limitsCancel := context.WithTimeout(context.Background(), caamLimitsOverlayTimeout)
	defer limitsCancel()
	applyLiveProviderQuota(limitsCtx, &quotaInfo)

	if wd, wdErr := os.Getwd(); wdErr == nil {
		applyRateLimitTrackerOverlay(wd, &quotaInfo)
	}

	// Check for cache errors
	if errTime, err := cache.GetLastError(); err != nil && !errTime.IsZero() {
		output := &QuotaStatusOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "caut polling error - data may be stale"),
			Quota:         quotaInfo,
		}
		// Still include the data even with error
		output.Success = true // Partial success
		return output, nil
	}

	return &QuotaStatusOutput{
		RobotResponse: NewRobotResponse(true),
		Quota:         quotaInfo,
	}, nil
}

// caamLimitsOverlayTimeout bounds the live-window overlay on the robot status
// surfaces. `caam limits` reaches provider APIs and caam allows itself 60s for
// them, so the budget has to be generous — but these are interactive robot
// commands, so it is not caam's full 75s ceiling either. Exceeding it is
// reported as an unreadable pool, never as a healthy one.
const caamLimitsOverlayTimeout = 30 * time.Second

// quotaStatusLimitsProviders are the providers whose subscription windows caam
// can read. Kept explicit so the overlay never shells out for a provider caam
// would reject.
var quotaStatusLimitsProviders = []string{"claude", "openai"}

// quotaStatusLimitsProbe reads one provider's ranked live quota. It is a
// variable so tests can supply a pool without a caam binary or credentials.
var quotaStatusLimitsProbe = func(ctx context.Context, provider string) (*tools.CAAMLimitsResult, error) {
	return tools.NewCAAMAdapter().Limits(ctx, tools.CAAMLimitsOptions{
		Provider: provider,
		Rank:     tools.CAAMRankEarliestResetHeadroom,
	})
}

// applyLiveProviderQuota merges CAAM's subscription windows into the quota
// map. It reports the worst-off eligible seat per provider — the binding
// constraint on new work — and never overwrites a figure caut already
// supplied.
func applyLiveProviderQuota(ctx context.Context, quotaInfo *QuotaInfo) {
	if quotaInfo == nil {
		return
	}
	if quotaInfo.Providers == nil {
		quotaInfo.Providers = make(map[string]ProviderQuota)
	}

	for _, provider := range quotaStatusLimitsProviders {
		applyLiveProviderQuotaFor(ctx, quotaInfo, provider)
	}
}

// applyLiveProviderQuotaFor merges one caam-readable provider's subscription
// windows into quotaInfo; see applyLiveProviderQuota.
func applyLiveProviderQuotaFor(ctx context.Context, quotaInfo *QuotaInfo, provider string) {
	if quotaInfo.Providers == nil {
		quotaInfo.Providers = make(map[string]ProviderQuota)
	}
	result, err := quotaStatusLimitsProbe(ctx, provider)
	if err != nil && result == nil {
		// Nothing readable for this provider. Silence here is correct:
		// most hosts have no caam seats at all, and an error row per
		// provider would make a normal install look broken.
		// --robot-account-status carries the per-provider limits_error for
		// operators who do use caam.
		return
	}
	if result == nil || len(result.Profiles) == 0 {
		return
	}

	// The seat that governs new work: caam's top-ranked eligible profile,
	// falling back to the highest utilization on record when nothing is
	// eligible, because "every seat is at its cap" is the single most
	// important thing this surface can report.
	var governing *tools.CAAMRankedProfile
	for i := range result.Profiles {
		p := &result.Profiles[i]
		if p.Eligible && p.Rank == 1 {
			governing = p
			break
		}
	}
	if governing == nil {
		for i := range result.Profiles {
			p := &result.Profiles[i]
			if governing == nil || p.UsedPercent > governing.UsedPercent {
				governing = p
			}
		}
	}
	if governing == nil {
		return
	}

	key := canonicalRobotProvider(provider)
	quota, exists := quotaInfo.Providers[key]
	if !exists {
		quota = ProviderQuota{}
	}
	// caut owns the number when it has one; a zero there means it had none.
	if quota.UsagePercent == 0 {
		quota.UsagePercent = float64(governing.UsedPercent)
	}
	if quota.ResetAt == "" && governing.ResetsAt != nil {
		quota.ResetAt = FormatTimestamp(*governing.ResetsAt)
	}
	quota.Status = getQuotaStatus(quota.UsagePercent)
	quotaInfo.Providers[key] = quota

	if quota.UsagePercent >= 95.0 {
		quotaInfo.HasCritical = true
	} else if quota.UsagePercent >= 80.0 {
		quotaInfo.HasWarning = true
	}
}

// PrintQuotaStatus handles the --robot-quota-status command.
// This is a thin wrapper around GetQuotaStatus() for CLI output.
func PrintQuotaStatus() error {
	output, err := GetQuotaStatus()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot quota status failed")
}

// GetQuotaCheck returns quota check for a specific provider.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetQuotaCheck(provider string) (*QuotaCheckOutput, error) {
	if provider == "" {
		return &QuotaCheckOutput{
			RobotResponse: NewErrorResponse(
				nil,
				ErrCodeInvalidFlag,
				"Specify a provider with --provider=<name> (alias: --quota-check-provider)",
			),
			Provider: provider,
		}, nil
	}
	if agent.AgentType(strings.TrimSpace(provider)).Canonical() == agent.AgentTypeOmp {
		// omp is a harness over arbitrary providers with no quota API; say so
		// (exit 2, skip gracefully) instead of "provider not found".
		return &QuotaCheckOutput{
			RobotResponse: NewErrorResponse(
				errors.New("omp exposes no quota API"),
				ErrCodeNotImplemented,
				"omp: "+quota.OmpQuotaUnsupported,
			),
			Provider: string(agent.AgentTypeOmp),
			Quota:    ProviderQuota{Status: "unsupported"},
		}, nil
	}
	canonicalProvider := canonicalRobotProvider(provider)
	lookupProvider := quotaLookupProvider(provider)

	// caut's cache first: usage counters, then its quota percentage.
	poller := caut.GetGlobalPoller()
	refreshCautQuotaCache(poller)
	cache := poller.GetCache()
	info := QuotaInfo{Providers: make(map[string]ProviderQuota)}
	if usage := cache.GetUsage(lookupProvider); usage != nil {
		info.Providers[canonicalProvider] = ProviderQuota{
			RequestsUsed: usage.RequestCount,
			TokensUsed:   usage.TokensIn + usage.TokensOut,
			CostUSD:      usage.Cost,
			Status:       "ok",
		}
	}
	if status := cache.GetStatus(); status != nil {
		for _, p := range status.Providers {
			if quotaLookupProvider(p.Name) == lookupProvider {
				quota := info.Providers[canonicalProvider]
				quota.UsagePercent = p.QuotaUsed
				quota.Status = getQuotaStatus(p.QuotaUsed)
				info.Providers[canonicalProvider] = quota
				break
			}
		}
	}
	for _, detail := range cache.GetProviderDetails() {
		if canonicalRobotProvider(detail.Provider) == canonicalProvider {
			pq, exists := info.Providers[canonicalProvider]
			if !exists {
				pq = ProviderQuota{Status: "ok"}
			}
			applyCautProviderDetail(&pq, detail)
			info.Providers[canonicalProvider] = pq
			break
		}
	}

	// Then the same CAAM subscription-window overlay --robot-quota-status
	// applies (ntm#319), for this provider only: a seat that status lists
	// must not be "not found" here.
	for _, limitsProvider := range quotaStatusLimitsProviders {
		if canonicalRobotProvider(limitsProvider) != canonicalProvider {
			continue
		}
		limitsCtx, limitsCancel := context.WithTimeout(context.Background(), caamLimitsOverlayTimeout)
		applyLiveProviderQuotaFor(limitsCtx, &info, limitsProvider)
		limitsCancel()
	}

	if wd, wdErr := os.Getwd(); wdErr == nil {
		applyRateLimitTrackerOverlay(wd, &info)
	}

	providerQuota, found := info.Providers[canonicalProvider]
	if !found {
		if rl, ok := info.RateLimits[canonicalProvider]; ok && rl.InCooldown {
			rlCopy := rl
			providerQuota = ProviderQuota{
				Status:    "warning",
				RateLimit: &rlCopy,
			}
			found = true
		}
	}
	if !found {
		return &QuotaCheckOutput{
			RobotResponse: NewErrorResponse(
				nil,
				ErrCodeNotFound,
				"No quota data for provider '"+canonicalProvider+"' from caut or caam. Use --robot-quota-status to see available providers.",
			),
			Provider: canonicalProvider,
		}, nil
	}
	return &QuotaCheckOutput{
		RobotResponse: NewRobotResponse(true),
		Provider:      canonicalProvider,
		Quota:         providerQuota,
	}, nil
}

// PrintQuotaCheck handles the --robot-quota-check command.
// This is a thin wrapper around GetQuotaCheck() for CLI output.
func PrintQuotaCheck(provider string) error {
	output, err := GetQuotaCheck(provider)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot quota check failed")
}

// getQuotaStatus returns the status string based on usage percentage
func getQuotaStatus(usagePercent float64) string {
	switch {
	case usagePercent >= 95.0:
		return "critical"
	case usagePercent >= 80.0:
		return "warning"
	default:
		return "ok"
	}
}

func applyCautProviderDetail(pq *ProviderQuota, detail caut.ProviderDetail) {
	if pq == nil {
		return
	}
	if detail.AccountEmail != "" {
		pq.AccountEmail = detail.AccountEmail
	}
	if detail.WindowMinutes > 0 {
		pq.WindowMinutes = detail.WindowMinutes
	}
	if detail.HasUsageData {
		pq.HasUsageData = true
	}
	if detail.RateLimited {
		pq.RateLimited = true
		pq.Status = "critical"
	}
	if pq.ResetAt == "" && detail.ResetAt != "" {
		pq.ResetAt = detail.ResetAt
	}
	if pq.ResetDescription == "" && detail.ResetDescription != "" {
		pq.ResetDescription = detail.ResetDescription
	}
}

func applyRateLimitTrackerOverlay(dir string, quotaInfo *QuotaInfo) {
	if quotaInfo == nil || strings.TrimSpace(dir) == "" {
		return
	}
	tracker := ratelimit.NewRateLimitTracker(dir)
	if err := tracker.LoadFromDir(dir); err != nil {
		return
	}
	providers := tracker.GetAllProviders()
	if len(providers) == 0 {
		return
	}
	sort.Strings(providers)
	if quotaInfo.RateLimits == nil {
		quotaInfo.RateLimits = make(map[string]ProviderRateLimitStatus, len(providers))
	}
	for _, provider := range providers {
		state := tracker.GetProviderState(provider)
		if state == nil {
			continue
		}
		inCooldown := tracker.IsInCooldown(provider)
		remaining := tracker.CooldownRemaining(provider)
		events := tracker.GetRecentEvents(provider, 5)
		rlStatus := ProviderRateLimitStatus{
			InCooldown:         inCooldown,
			CooldownRemainingS: int(remaining.Round(time.Second).Seconds()),
			CurrentDelayMs:     state.CurrentDelay.Milliseconds(),
			TotalRateLimits:    state.TotalRateLimits,
			TotalSuccesses:     state.TotalSuccesses,
			RecentEvents:       events,
		}
		if !state.LastRateLimit.IsZero() {
			rlStatus.LastRateLimit = FormatTimestamp(state.LastRateLimit)
		}
		key := canonicalRobotProvider(provider)
		quotaInfo.RateLimits[key] = rlStatus
		if inCooldown {
			quotaInfo.HasWarning = true
		}
		if pq, exists := quotaInfo.Providers[key]; exists {
			rlCopy := rlStatus
			pq.RateLimit = &rlCopy
			if inCooldown && pq.Status == "ok" {
				pq.Status = "warning"
			}
			quotaInfo.Providers[key] = pq
		}
	}
}
