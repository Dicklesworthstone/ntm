package caut

import (
	"time"
)

// Response is the top-level caut JSON structure
type Response struct {
	SchemaVersion string   `json:"schema_version"` // "caut.v1"
	Command       string   `json:"command"`        // "usage"
	Timestamp     string   `json:"timestamp"`
	Data          Data     `json:"data"`
	Errors        []string `json:"errors"`
}

// Data contains the usage payloads
type Data struct {
	Payloads []ProviderPayload `json:"payloads"`
}

// ProviderPayload contains usage data for one provider
type ProviderPayload struct {
	Provider string        `json:"provider"`
	Account  *string       `json:"account,omitempty"`
	Source   string        `json:"source"` // "web", "cli", "api"
	Status   *StatusInfo   `json:"status,omitempty"`
	Usage    UsageSnapshot `json:"usage"`
}

// StatusInfo contains provider status page information
type StatusInfo struct {
	Operational bool    `json:"operational,omitempty"`
	Message     *string `json:"message,omitempty"`
	URL         *string `json:"url,omitempty"`
}

// UsageSnapshot contains rate window information
type UsageSnapshot struct {
	PrimaryRateWindow   *RateWindow `json:"primary_rate_window,omitempty"`
	SecondaryRateWindow *RateWindow `json:"secondary_rate_window,omitempty"`
	TertiaryRateWindow  *RateWindow `json:"tertiary_rate_window,omitempty"`
	Identity            *Identity   `json:"identity,omitempty"`
}

// RateWindow describes a usage rate limiting window
type RateWindow struct {
	UsedPercent      *float64   `json:"used_percent,omitempty"`
	WindowMinutes    *int       `json:"window_minutes,omitempty"`
	ResetsAt         *time.Time `json:"resets_at,omitempty"`
	ResetDescription *string    `json:"reset_description,omitempty"`
}

// Identity contains account information
type Identity struct {
	AccountEmail *string `json:"account_email,omitempty"`
	PlanName     *string `json:"plan_name,omitempty"`
}

// UsageResult is the processed result for NTM consumption
type UsageResult struct {
	SchemaVersion string
	Payloads      []ProviderPayload
	Errors        []string
	FetchedAt     time.Time
}

// IsRateLimited returns true if primary window usage > threshold
func (p *ProviderPayload) IsRateLimited(threshold float64) bool {
	if p.Usage.PrimaryRateWindow == nil || p.Usage.PrimaryRateWindow.UsedPercent == nil {
		return false
	}
	return *p.Usage.PrimaryRateWindow.UsedPercent >= threshold
}

// GetResetTime returns when the primary window resets
func (p *ProviderPayload) GetResetTime() *time.Time {
	if p.Usage.PrimaryRateWindow == nil {
		return nil
	}
	return p.Usage.PrimaryRateWindow.ResetsAt
}

// UsedPercent returns primary window usage percentage
func (p *ProviderPayload) UsedPercent() *float64 {
	if p.Usage.PrimaryRateWindow == nil {
		return nil
	}
	return p.Usage.PrimaryRateWindow.UsedPercent
}

// GetWindowMinutes returns the primary rate window duration in minutes
func (p *ProviderPayload) GetWindowMinutes() *int {
	if p.Usage.PrimaryRateWindow == nil {
		return nil
	}
	return p.Usage.PrimaryRateWindow.WindowMinutes
}

// GetResetDescription returns a human-readable description of when the window resets
func (p *ProviderPayload) GetResetDescription() string {
	if p.Usage.PrimaryRateWindow == nil || p.Usage.PrimaryRateWindow.ResetDescription == nil {
		return ""
	}
	return *p.Usage.PrimaryRateWindow.ResetDescription
}

// GetAccountEmail returns the account email if available
func (p *ProviderPayload) GetAccountEmail() string {
	if p.Usage.Identity == nil || p.Usage.Identity.AccountEmail == nil {
		return ""
	}
	return *p.Usage.Identity.AccountEmail
}

// GetPlanName returns the plan name if available
func (p *ProviderPayload) GetPlanName() string {
	if p.Usage.Identity == nil || p.Usage.Identity.PlanName == nil {
		return ""
	}
	return *p.Usage.Identity.PlanName
}

// HasUsageData returns true if the payload contains any usage data
func (p *ProviderPayload) HasUsageData() bool {
	return p.Usage.PrimaryRateWindow != nil ||
		p.Usage.SecondaryRateWindow != nil ||
		p.Usage.TertiaryRateWindow != nil
}

// IsOperational returns true if the provider status indicates it's operational
func (p *ProviderPayload) IsOperational() bool {
	if p.Status == nil {
		return true // Assume operational if no status
	}
	return p.Status.Operational
}

// GetPayloadByProvider returns the payload for a specific provider
func (r *UsageResult) GetPayloadByProvider(provider string) *ProviderPayload {
	for i := range r.Payloads {
		if r.Payloads[i].Provider == provider {
			return &r.Payloads[i]
		}
	}
	return nil
}

// HasErrors returns true if there were any errors during the fetch
func (r *UsageResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// AgentTypeToProvider maps NTM agent type to caut provider
func AgentTypeToProvider(agentType string) string {
	switch agentType {
	case "cc":
		return "claude"
	case "cod":
		return "codex"
	case "gmi":
		return "gemini"
	default:
		return ""
	}
}

// ProviderToAgentType maps caut provider to NTM agent type
func ProviderToAgentType(provider string) string {
	switch provider {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	default:
		return ""
	}
}

// SupportedProviders returns the list of providers supported by NTM
func SupportedProviders() []string {
	return []string{"claude", "codex", "gemini"}
}
