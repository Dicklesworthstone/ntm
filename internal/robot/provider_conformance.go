package robot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/provider"
)

// ProviderConformanceOutput is an operator-runnable, synthetic/offline report.
// It validates NTM's lifecycle contract and error taxonomy without Beads,
// Agent Mail, a credential, a provider call, or a writable working tree.
type ProviderConformanceOutput struct {
	RobotResponse
	Mode   string                     `json:"mode"`
	Passed bool                       `json:"passed"`
	Report provider.ConformanceReport `json:"report"`
}

func GetProviderConformance(ctx context.Context, cfg *config.Config, profileTarget, transport string) (*ProviderConformanceOutput, error) {
	if ctx == nil {
		return nil, errors.New("provider conformance context is required")
	}
	if cfg == nil {
		return nil, errors.New("provider conformance requires a loaded exact provider profile")
	}
	profileTarget = strings.TrimSpace(profileTarget)
	transport = strings.TrimSpace(transport)
	if profileTarget == "" || transport == "" {
		return nil, errors.New("provider conformance requires --provider-profile and --provider-transport")
	}
	profile, err := cfg.ProviderProfile(profileTarget)
	if err != nil {
		return nil, err
	}
	identity, err := profile.Identity()
	if err != nil {
		return nil, err
	}
	if err := validateConformanceTransportIdentity(transport, identity); err != nil {
		return nil, err
	}

	fixture := provider.FixtureProvenance{
		FixtureID:            "builtin-redacted-v1:" + transport,
		CapturedAt:           time.Now().UTC(),
		RuntimeVersion:       "ntm-synthetic-v1",
		ProviderIdentityHash: identity.Hash(),
		Source:               "compiled synthetic lifecycle fixture",
		Redacted:             true,
	}
	runtime := syntheticProviderRuntime{identity: identity, transport: transport}
	report := provider.RunConformance(ctx, runtime, transport, identity, fixture, "NTM_ACK_0123456789abcdef0123456789abcdef")
	output := &ProviderConformanceOutput{
		RobotResponse: NewRobotResponse(report.Passed()),
		Mode:          "synthetic_offline",
		Passed:        report.Passed(),
		Report:        report,
	}
	if !report.Passed() {
		output.RobotResponse = NewErrorResponse(errors.New("provider conformance contract failed"), ErrCodeInternalError, "Inspect failed checks; this command made no provider calls")
	}
	return output, nil
}

func PrintProviderConformance(ctx context.Context, cfg *config.Config, profileTarget, transport string) error {
	output, err := GetProviderConformance(ctx, cfg, profileTarget, transport)
	if err != nil {
		return EncodeErrorJSON(err, ErrCodeInvalidFlag, "Select an exact profile and matching declared transport", "robot-provider-conformance")
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot provider conformance failed")
}

func validateConformanceTransportIdentity(transport string, identity provider.Identity) error {
	switch transport {
	case "xai_acp", "xai_grok_tui":
		if identity.Provider() != "xai" {
			return fmt.Errorf("transport %q requires an xAI provider identity", transport)
		}
	case "zai_claude_runtime":
		if identity.Provider() != "zai" {
			return fmt.Errorf("transport %q requires a Z.ai provider identity", transport)
		}
	default:
		return fmt.Errorf("unknown provider transport %q", transport)
	}
	return nil
}

type syntheticProviderRuntime struct {
	identity  provider.Identity
	transport string
}

func (r syntheticProviderRuntime) Launch(_ context.Context, identity provider.Identity) (provider.LaunchObservation, error) {
	if identity.Hash() != r.identity.Hash() {
		return provider.LaunchObservation{}, errors.New("synthetic identity mismatch")
	}
	return provider.LaunchObservation{IdentityHash: identity.Hash(), SessionID: "synthetic-" + identity.Hash()[:16], NoWrites: true}, nil
}

func (r syntheticProviderRuntime) Deliver(_ context.Context, _ string, nonce string) (provider.DeliveryObservation, error) {
	return provider.DeliveryObservation{Submitted: true, AcknowledgedNonce: nonce, CompletionAuthoritative: r.transport == "xai_acp"}, nil
}

func (syntheticProviderRuntime) Cancel(context.Context, string) (provider.CancelObservation, error) {
	return provider.CancelObservation{Attempted: true, Authoritative: false}, nil
}

func (syntheticProviderRuntime) Recover(context.Context, string) (provider.RecoveryObservation, error) {
	return provider.RecoveryObservation{OutcomeUnknown: true, AutomaticReplayBlocked: true}, nil
}

func (syntheticProviderRuntime) Resume(context.Context, string) (provider.ResumeObservation, error) {
	return provider.ResumeObservation{}, nil
}

func (syntheticProviderRuntime) Cleanup(context.Context, string) (provider.CleanupObservation, error) {
	return provider.CleanupObservation{ZeroResiduals: true}, nil
}

func (r syntheticProviderRuntime) ProviderErrors(context.Context) ([]provider.ErrorObservation, error) {
	if r.identity.Provider() != "zai" {
		return []provider.ErrorObservation{{HTTPStatus: 429, Code: "rate_limit", Expected: provider.ErrorRateLimited}}, nil
	}
	return []provider.ErrorObservation{
		{HTTPStatus: 429, Code: "1302", Expected: provider.ErrorRateLimited},
		{HTTPStatus: 429, Code: "1305", Expected: provider.ErrorOverloaded},
		{HTTPStatus: 429, Code: "1308", Expected: provider.ErrorLongPeriodQuota},
		{HTTPStatus: 403, Code: "1309", Expected: provider.ErrorPlanExpired},
		{HTTPStatus: 400, Code: "1311", Expected: provider.ErrorUnsupportedModel},
		{HTTPStatus: 403, Code: "1313", Expected: provider.ErrorUsageRestricted},
		{HTTPStatus: 429, Code: "1113", Expected: provider.ErrorInsufficientBalance},
		{HTTPStatus: 401, Code: "1000", Expected: provider.ErrorAuthentication},
	}, nil
}
