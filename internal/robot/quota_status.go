package robot

import (
	"context"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// QuotaStatusOutput represents the response from --robot-quota-status
type QuotaStatusOutput struct {
	RobotResponse
	Quota QuotaInfo `json:"quota"`
}

// QuotaInfo contains quota and usage information from caut
type QuotaInfo struct {
	LastUpdated    string                   `json:"last_updated"`
	CautAvailable  bool                     `json:"caut_available"`
	Providers      map[string]ProviderQuota `json:"providers"`
	TotalCostToday float64                  `json:"total_cost_today_usd"`
	TotalCostMonth float64                  `json:"total_cost_month_usd,omitempty"`
	HasWarning     bool                     `json:"has_warning"`
	HasCritical    bool                     `json:"has_critical"`
}

// ProviderQuota contains quota information for a single provider
type ProviderQuota struct {
	UsagePercent  float64 `json:"usage_percent"`
	RequestsUsed  int     `json:"requests_used,omitempty"`
	RequestsLimit int     `json:"requests_limit,omitempty"`
	TokensUsed    int64   `json:"tokens_used,omitempty"`
	TokensLimit   int64   `json:"tokens_limit,omitempty"`
	CostUSD       float64 `json:"cost_usd"`
	ResetAt       string  `json:"reset_at,omitempty"`
	Status        string  `json:"status"` // "ok", "warning", "critical"
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

// GetQuotaStatus returns quota status information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetQuotaStatus() (*QuotaStatusOutput, error) {
	poller := caut.GetGlobalPoller()
	cache := poller.GetCache()

	// Check if caut is available
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	adapter := tools.NewCautAdapter()
	available := adapter.IsAvailable(ctx)

	quotaInfo := QuotaInfo{
		LastUpdated:   FormatTimestamp(cache.GetLastUpdated()),
		CautAvailable: available,
		Providers:     make(map[string]ProviderQuota),
	}

	// Get cached status
	status := cache.GetStatus()
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

	// Add usage data from cache
	usages := cache.GetAllUsage()
	for _, usage := range usages {
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
		result, err := quotaStatusLimitsProbe(ctx, provider)
		if err != nil && result == nil {
			// Nothing readable for this provider. Silence here is correct:
			// most hosts have no caam seats at all, and an error row per
			// provider would make a normal install look broken.
			// --robot-account-status carries the per-provider limits_error for
			// operators who do use caam.
			continue
		}
		if result == nil || len(result.Profiles) == 0 {
			continue
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
			continue
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
	canonicalProvider := canonicalRobotProvider(provider)
	lookupProvider := quotaLookupProvider(provider)

	poller := caut.GetGlobalPoller()
	cache := poller.GetCache()

	// Get provider-specific usage
	usage := cache.GetUsage(lookupProvider)
	if usage == nil {
		// Try to get from status providers
		status := cache.GetStatus()
		if status != nil {
			for _, p := range status.Providers {
				if quotaLookupProvider(p.Name) == lookupProvider {
					return &QuotaCheckOutput{
						RobotResponse: NewRobotResponse(true),
						Provider:      canonicalProvider,
						Quota: ProviderQuota{
							UsagePercent: p.QuotaUsed,
							Status:       getQuotaStatus(p.QuotaUsed),
						},
					}, nil
				}
			}
		}

		return &QuotaCheckOutput{
			RobotResponse: NewErrorResponse(
				nil,
				ErrCodePaneNotFound, // Reusing as "not found"
				"Provider '"+canonicalProvider+"' not found. Use --robot-quota-status to see available providers.",
			),
			Provider: canonicalProvider,
		}, nil
	}

	// Build provider quota from usage data
	providerQuota := ProviderQuota{
		RequestsUsed: usage.RequestCount,
		TokensUsed:   usage.TokensIn + usage.TokensOut,
		CostUSD:      usage.Cost,
		Status:       "ok",
	}

	// Check status for quota percentage
	status := cache.GetStatus()
	if status != nil {
		for _, p := range status.Providers {
			if quotaLookupProvider(p.Name) == lookupProvider {
				providerQuota.UsagePercent = p.QuotaUsed
				providerQuota.Status = getQuotaStatus(p.QuotaUsed)
				break
			}
		}
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
