package robot

import (
	"context"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// AccountStatusOutput represents the response from --robot-account-status
type AccountStatusOutput struct {
	RobotResponse
	Accounts map[string]ProviderStatus `json:"accounts"`
}

// ProviderStatus contains status information for a single provider
type ProviderStatus struct {
	Current           string `json:"current"`
	UsagePercent      int    `json:"usage_percent,omitempty"`
	LimitReset        string `json:"limit_reset,omitempty"`
	AvailableAccounts int    `json:"available_accounts"`
	RateLimited       bool   `json:"rate_limited,omitempty"`

	// Live quota, read from `caam limits <provider> --rank
	// earliest-reset-headroom` (ntm#319). Before this the robot surface
	// reported account NAMES and nothing about their windows, so a controller
	// could not see which seat was near its cap or when quota came back.
	//
	// UsagePercent, ResetsAt, GoverningWindow and Tier describe the
	// RecommendedSeat — the seat the earliest-reset-with-headroom policy would
	// hand new work — not the currently active one. Seats lists every profile
	// with its own numbers.
	Tier               string              `json:"tier,omitempty"`
	ResetsAt           string              `json:"resets_at,omitempty"`
	GoverningWindow    string              `json:"governing_window,omitempty"`
	RecommendedSeat    string              `json:"recommended_seat,omitempty"`
	RecommendedPersona string              `json:"recommended_persona,omitempty"`
	Seats              []ProviderSeatQuota `json:"seats,omitempty"`

	// LimitsError explains why the live windows are absent. It is populated
	// whenever they could not be read — an unreadable pool must be visible,
	// never silently indistinguishable from a healthy one.
	LimitsError string `json:"limits_error,omitempty"`
}

// ProviderSeatQuota is one profile's live quota window, as caam ranked it.
type ProviderSeatQuota struct {
	Profile string `json:"profile"`
	// Persona is the ntm persona name for this seat (codex-<profile> /
	// claude-<profile>).
	Persona string `json:"persona,omitempty"`
	// Rank is 1-based among ELIGIBLE seats; ineligible seats carry 0.
	Rank            int    `json:"rank"`
	Eligible        bool   `json:"eligible"`
	Tier            string `json:"tier,omitempty"`
	UsedPercent     int    `json:"used_percent"`
	HeadroomPercent int    `json:"headroom_percent"`
	BindingWindow   string `json:"binding_window,omitempty"`
	GoverningWindow string `json:"governing_window,omitempty"`
	ResetsAt        string `json:"resets_at,omitempty"`
	ResetsInSeconds *int64 `json:"resets_in_seconds,omitempty"`
	HasCredits      bool   `json:"has_credits,omitempty"`
	PlanType        string `json:"plan_type,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// AccountStatusOptions contains options for the account status command
type AccountStatusOptions struct {
	Provider string // Optional filter for a specific provider (claude, openai, gemini)
}

// GetAccountStatus returns account status information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetAccountStatus(opts AccountStatusOptions) (*AccountStatusOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := tools.NewCAAMAdapter()

	// Check if CAAM is available
	if _, installed := adapter.Detect(); !installed {
		output := &AccountStatusOutput{
			RobotResponse: NewErrorResponse(nil, ErrCodeDependencyMissing, "Install caam to manage coding agent accounts"),
			Accounts:      make(map[string]ProviderStatus),
		}
		output.Error = "caam not installed"
		return output, nil
	}

	// Get all accounts from CAAM
	status, err := adapter.GetStatus(ctx)
	if err != nil {
		return &AccountStatusOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Check if caam is configured correctly"),
			Accounts:      make(map[string]ProviderStatus),
		}, nil
	}

	// Build per-provider status map
	providerAccounts := make(map[string][]tools.CAAMAccount)
	for _, acc := range status.Accounts {
		provider := canonicalRobotProvider(acc.Provider)
		providerAccounts[provider] = append(providerAccounts[provider], acc)
	}
	opts.Provider = canonicalRobotProvider(opts.Provider)

	// Build output
	output := &AccountStatusOutput{
		RobotResponse: NewRobotResponse(true),
		Accounts:      make(map[string]ProviderStatus),
	}

	for provider, accounts := range providerAccounts {
		// Filter by provider if specified
		if opts.Provider != "" && provider != opts.Provider {
			continue
		}

		provStatus := ProviderStatus{
			AvailableAccounts: len(accounts),
		}

		// Find the active/current account for this provider
		for _, acc := range accounts {
			if acc.Active {
				provStatus.Current = acc.Email
				if provStatus.Current == "" {
					provStatus.Current = acc.Name
				}
				if provStatus.Current == "" {
					provStatus.Current = acc.ID
				}
				provStatus.RateLimited = acc.RateLimited
				if !acc.CooldownUntil.IsZero() {
					provStatus.LimitReset = FormatTimestamp(acc.CooldownUntil)
				}
			}
		}

		output.Accounts[provider] = provStatus
	}

	// If a specific provider was requested but not found, still include it with zero values
	if opts.Provider != "" {
		if _, exists := output.Accounts[opts.Provider]; !exists {
			output.Accounts[opts.Provider] = ProviderStatus{
				AvailableAccounts: 0,
			}
		}
	}

	// Overlay the live quota windows (ntm#319). Best-effort per provider: a
	// pool whose limits cannot be read reports why in limits_error rather than
	// vanishing, because "unreadable" and "healthy" must never look alike to a
	// controller deciding where to put work.
	//
	// The overlay gets its own budget rather than sharing the 30s above:
	// that one already covers the caam status and cost calls, while `caam
	// limits` queries provider APIs on top of them.
	limitsCtx, limitsCancel := context.WithTimeout(context.Background(), caamLimitsOverlayTimeout)
	defer limitsCancel()
	for provider, provStatus := range output.Accounts {
		if !tools.CAAMLimitsProviderSupported(provider) {
			continue
		}
		applyLiveSeatQuota(limitsCtx, provider, &provStatus)
		output.Accounts[provider] = provStatus
	}

	return output, nil
}

// accountStatusLimitsProbe reads one provider's ranked live quota. It is a
// variable so tests can supply a pool without a caam binary or credentials.
var accountStatusLimitsProbe = func(ctx context.Context, provider string) (*tools.CAAMLimitsResult, error) {
	return tools.NewCAAMAdapter().Limits(ctx, tools.CAAMLimitsOptions{
		Provider: provider,
		Rank:     tools.CAAMRankEarliestResetHeadroom,
	})
}

// applyLiveSeatQuota fills a provider's live window fields from caam.
//
// The rank mode is earliest-reset-with-headroom, never `--best`: `--best`
// ranks lowest utilization, which on a pool of interchangeable seats names the
// idle reserve rather than the seat whose quota is about to expire unused
// (caam#105).
func applyLiveSeatQuota(ctx context.Context, provider string, provStatus *ProviderStatus) {
	result, err := accountStatusLimitsProbe(ctx, provider)

	if result != nil {
		provStatus.Seats = make([]ProviderSeatQuota, 0, len(result.Profiles))
		for i := range result.Profiles {
			p := result.Profiles[i]
			seat := ProviderSeatQuota{
				Profile:         p.Profile,
				Persona:         p.Persona(),
				Rank:            p.Rank,
				Eligible:        p.Eligible,
				Tier:            p.Tier,
				UsedPercent:     p.UsedPercent,
				HeadroomPercent: p.HeadroomPercent,
				BindingWindow:   p.BindingWindow,
				GoverningWindow: p.GoverningWindow,
				ResetsInSeconds: p.ResetsInSeconds,
				HasCredits:      p.HasCredits,
				PlanType:        p.PlanType,
				Reason:          p.Reason,
			}
			if p.ResetsAt != nil {
				seat.ResetsAt = FormatTimestamp(*p.ResetsAt)
			}
			provStatus.Seats = append(provStatus.Seats, seat)
		}
	}

	if err != nil {
		provStatus.LimitsError = err.Error()
		return
	}
	if result == nil || result.Selected == nil {
		provStatus.LimitsError = "caam returned no ranked selection"
		return
	}

	selected := result.Selected
	provStatus.RecommendedSeat = selected.Provider + "/" + selected.Profile
	provStatus.RecommendedPersona = selected.Persona()
	provStatus.Tier = selected.Tier
	provStatus.GoverningWindow = selected.GoverningWindow
	provStatus.UsagePercent = selected.UsedPercent
	if selected.ResetsAt != nil {
		provStatus.ResetsAt = FormatTimestamp(*selected.ResetsAt)
	}
}

// PrintAccountStatus handles the --robot-account-status command.
// This is a thin wrapper around GetAccountStatus() for CLI output.
func PrintAccountStatus(opts AccountStatusOptions) error {
	output, err := GetAccountStatus(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot account status failed")
}
