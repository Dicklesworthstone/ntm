package robot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// SwitchAccountOutput represents the response from --robot-switch-account
type SwitchAccountOutput struct {
	RobotResponse
	Switch SwitchAccountResult `json:"switch"`
}

// SwitchAccountResult contains the switch operation result
type SwitchAccountResult struct {
	Success         bool     `json:"success"`
	Provider        string   `json:"provider"`
	PreviousAccount string   `json:"previous_account,omitempty"`
	NewAccount      string   `json:"new_account,omitempty"`
	PanesAffected   []string `json:"panes_affected,omitempty"`
	CooldownSeconds int      `json:"cooldown_seconds,omitempty"`
	Error           string   `json:"error,omitempty"`
}

// SwitchAccountOptions contains options for the switch account command
type SwitchAccountOptions struct {
	Provider  string // claude, openai, gemini
	AccountID string // Optional specific account to switch to
	Pane      string // Optional pane filter
}

// GetSwitchAccount performs the account switch and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
// Usage:
//
//	ntm --robot-switch-account claude         # Switch to next Claude account
//	ntm --robot-switch-account openai:acc123  # Switch to specific account
func GetSwitchAccount(opts SwitchAccountOptions) (*SwitchAccountOutput, error) {
	canonicalProvider, providerErr := validateRobotAccountProvider(opts.Provider)
	if providerErr != nil {
		return &SwitchAccountOutput{
			RobotResponse: NewErrorResponse(
				providerErr,
				ErrCodeInvalidFlag,
				"Specify a valid provider (claude, openai, gemini)",
			),
			Switch: SwitchAccountResult{
				Success:  false,
				Provider: strings.TrimSpace(opts.Provider),
				Error:    providerErr.Error(),
			},
		}, nil
	}
	if canonicalProvider == "" {
		return &SwitchAccountOutput{
			RobotResponse: NewErrorResponse(
				nil,
				ErrCodeInvalidFlag,
				"Specify a provider with --robot-switch-account=PROVIDER or --robot-switch-account=PROVIDER:ACCOUNT",
			),
			Switch: SwitchAccountResult{
				Success: false,
				Error:   "provider required",
			},
		}, nil
	}
	if opts.AccountID != "" {
		trimmedAccount := strings.TrimSpace(opts.AccountID)
		var acctErr error
		if trimmedAccount == "" {
			acctErr = fmt.Errorf("invalid account_id %q: must not be whitespace-only", opts.AccountID)
		} else if strings.HasPrefix(trimmedAccount, "-") {
			acctErr = fmt.Errorf("invalid account_id %q: must not begin with '-'", opts.AccountID)
		} else {
			for _, r := range opts.AccountID {
				if r < 0x20 || r == 0x7f {
					acctErr = fmt.Errorf("invalid account_id: contains control character 0x%02x", r)
					break
				}
			}
		}
		if acctErr != nil {
			return &SwitchAccountOutput{
				RobotResponse: NewErrorResponse(
					acctErr,
					ErrCodeInvalidFlag,
					"Provide a valid non-empty account ID without leading '-' or control characters",
				),
				Switch: SwitchAccountResult{
					Success:  false,
					Provider: canonicalProvider,
					Error:    acctErr.Error(),
				},
			}, nil
		}
		opts.AccountID = trimmedAccount
	}
	if strings.TrimSpace(opts.Pane) != "" {
		return &SwitchAccountOutput{
			RobotResponse: NewErrorResponse(
				nil,
				ErrCodeInvalidFlag,
				"Account switching is global per provider; pane targeting is not supported for --robot-switch-account",
			),
			Switch: SwitchAccountResult{
				Success:  false,
				Provider: canonicalProvider,
				Error:    "pane targeting unsupported",
			},
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := tools.NewCAAMAdapter()

	// Check if CAAM is available
	if _, installed := adapter.Detect(); !installed {
		return &SwitchAccountOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("caam not installed"),
				ErrCodeDependencyMissing,
				"Install caam to enable account switching",
			),
			Switch: SwitchAccountResult{
				Success:  false,
				Provider: canonicalProvider,
				Error:    "caam not installed",
			},
		}, nil
	}

	// Get current account before switch (for comparison)
	creds, _ := adapter.GetCurrentCredentials(ctx, canonicalProvider)
	previousAccount := ""
	if creds != nil {
		previousAccount = creds.AccountID
	}

	var result *tools.SwitchResult
	var err error

	if opts.AccountID != "" {
		// Switch to specific account
		err = adapter.SwitchAccount(ctx, canonicalProvider, opts.AccountID)
		if err == nil {
			result = &tools.SwitchResult{
				Success:         true,
				Provider:        canonicalProvider,
				PreviousAccount: previousAccount,
				NewAccount:      opts.AccountID,
			}
		}
	} else {
		// Switch to next available account
		result, err = adapter.SwitchToNextAccount(ctx, canonicalProvider)
	}

	output := &SwitchAccountOutput{
		RobotResponse: NewRobotResponse(true),
		Switch: SwitchAccountResult{
			Provider:        canonicalProvider,
			PreviousAccount: previousAccount,
		},
	}

	if err != nil {
		output.Switch.Success = false
		output.Switch.Error = err.Error()
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check caam configuration and availability",
		)
		return output, nil
	}

	if result != nil {
		output.Switch.Success = result.Success
		output.Switch.NewAccount = result.NewAccount
		if result.PreviousAccount != "" {
			output.Switch.PreviousAccount = result.PreviousAccount
		}
		output.Switch.CooldownSeconds = cooldownSeconds(result.CooldownUntil)
	}
	return output, nil
}

// PrintSwitchAccount handles the --robot-switch-account command.
// This is a thin wrapper around GetSwitchAccount() for CLI output.
func PrintSwitchAccount(opts SwitchAccountOptions) error {
	output, err := GetSwitchAccount(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot switch account failed")
}

// cooldownSeconds calculates seconds until cooldown expires
func cooldownSeconds(cooldownUntil time.Time) int {
	if cooldownUntil.IsZero() {
		return 0
	}
	remaining := time.Until(cooldownUntil)
	if remaining <= 0 {
		return 0
	}
	return int(remaining.Seconds())
}

// ParseSwitchAccountArg parses the argument format "provider" or "provider:account"
func ParseSwitchAccountArg(arg string) SwitchAccountOptions {
	opts := SwitchAccountOptions{}
	trimmed := strings.TrimSpace(arg)

	// Handle "provider:account" format
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == ':' {
			opts.Provider = canonicalRobotProvider(strings.TrimSpace(trimmed[:i]))
			opts.AccountID = strings.TrimSpace(trimmed[i+1:])
			return opts
		}
	}

	// Just provider
	opts.Provider = canonicalRobotProvider(trimmed)
	return opts
}
