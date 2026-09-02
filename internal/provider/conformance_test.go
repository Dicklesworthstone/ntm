package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeRuntime struct {
	identityHash string
	completion   bool
	cancel       CancelObservation
	errors       []ErrorObservation
	launchErr    error
	errorsErr    error
	resume       ResumeObservation
}

func (f fakeRuntime) Launch(context.Context, Identity) (LaunchObservation, error) {
	return LaunchObservation{IdentityHash: f.identityHash, SessionID: "fake-session", NoWrites: true}, f.launchErr
}
func (f fakeRuntime) Deliver(_ context.Context, _ string, nonce string) (DeliveryObservation, error) {
	return DeliveryObservation{Submitted: true, AcknowledgedNonce: nonce, CompletionAuthoritative: f.completion}, nil
}
func (f fakeRuntime) Cancel(context.Context, string) (CancelObservation, error) { return f.cancel, nil }
func (f fakeRuntime) Recover(context.Context, string) (RecoveryObservation, error) {
	return RecoveryObservation{OutcomeUnknown: true, AutomaticReplayBlocked: true}, nil
}
func (f fakeRuntime) Resume(context.Context, string) (ResumeObservation, error) {
	return f.resume, nil
}
func (f fakeRuntime) Cleanup(context.Context, string) (CleanupObservation, error) {
	return CleanupObservation{ZeroResiduals: true}, nil
}
func (f fakeRuntime) ProviderErrors(context.Context) ([]ErrorObservation, error) {
	return f.errors, f.errorsErr
}

func conformanceIdentity(t *testing.T) Identity {
	t.Helper()
	id, err := NewIdentity("xai", "kevin", "grok-4", "https://api.x.ai", "grok", testConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func zaiConformanceIdentity(t *testing.T) Identity {
	t.Helper()
	id, err := NewIdentity("zai", "kevin", "glm-5.3-flash", "https://api.z.ai/api/anthropic", "claude-glm", testConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func conformanceFixture(id Identity) FixtureProvenance {
	return FixtureProvenance{FixtureID: "fixture-v1", CapturedAt: time.Now().UTC(), RuntimeVersion: "test", ProviderIdentityHash: id.Hash(), Source: "fake", Redacted: true}
}
func genericErrors() []ErrorObservation {
	return []ErrorObservation{{HTTPStatus: 429, Code: "rate_limit", Expected: ErrorRateLimited}, {HTTPStatus: 503, Code: "overloaded", Expected: ErrorOverloaded}}
}
func zaiErrors() []ErrorObservation {
	return []ErrorObservation{
		{429, "1302", ErrorRateLimited}, {429, "1305", ErrorOverloaded}, {429, "1308", ErrorLongPeriodQuota}, {429, "1309", ErrorPlanExpired},
		{429, "1311", ErrorUnsupportedModel}, {429, "1313", ErrorUsageRestricted}, {429, "1113", ErrorInsufficientBalance}, {401, "1000", ErrorAuthentication},
	}
}

func TestCapabilityMatrixPreservesEvidenceBoundaries(t *testing.T) {
	t.Parallel()
	matrix := CapabilityMatrix()
	if got := matrix["xai_acp"].Completion; got != EvidenceAuthoritative {
		t.Fatalf("xAI ACP completion = %q, want authoritative", got)
	}
	if got := matrix["xai_acp"].Resume; got != EvidenceUnavailable {
		t.Fatalf("xAI ACP resume = %q, want unavailable until production session/load is implemented", got)
	}
	if got := matrix["xai_grok_tui"]; got.Delivery != EvidenceSubmission || got.Completion != EvidenceUnavailable {
		t.Fatalf("Grok TUI matrix = %+v, want submission-only", got)
	}
	for transport, capabilities := range matrix {
		if capabilities.Cleanup != EvidenceSubmission {
			t.Fatalf("%s cleanup = %q, want submission until process-tree residuals are proven", transport, capabilities.Cleanup)
		}
	}
	if got := matrix["zai_claude_runtime"]; !got.IdentityProbeRequired || got.Delivery != EvidenceSubmission || got.IdentityEvidence != IdentityEvidenceProfileAttested || got.CapacityControlScope != CapacityControlScopeProcessLocal {
		t.Fatalf("Z.ai Claude-runtime matrix = %+v", got)
	}
}

func TestRunConformanceACPAuthoritativeCompletion(t *testing.T) {
	t.Parallel()
	id := conformanceIdentity(t)
	report := RunConformance(context.Background(), fakeRuntime{identityHash: id.Hash(), completion: true, cancel: CancelObservation{Attempted: true}, errors: genericErrors()}, "xai_acp", id, conformanceFixture(id), "nonce-1")
	if !report.Passed() {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunConformanceZAIRequiresEveryExactErrorClass(t *testing.T) {
	t.Parallel()
	id := zaiConformanceIdentity(t)
	report := RunConformance(context.Background(), fakeRuntime{identityHash: id.Hash(), cancel: CancelObservation{Attempted: true}, errors: zaiErrors()}, "zai_claude_runtime", id, conformanceFixture(id), "nonce-1")
	if !report.Passed() {
		t.Fatalf("report = %+v", report)
	}
	report = RunConformance(context.Background(), fakeRuntime{identityHash: id.Hash(), cancel: CancelObservation{Attempted: true}, errors: zaiErrors()[:7]}, "zai_claude_runtime", id, conformanceFixture(id), "nonce-1")
	if report.Passed() || checkByName(report, "provider_error_taxonomy").Passed {
		t.Fatalf("incomplete Z.ai taxonomy must fail: %+v", report)
	}
}

func TestRunConformanceRejectsUnsupportedAuthoritativeCancellation(t *testing.T) {
	t.Parallel()
	id := conformanceIdentity(t)
	report := RunConformance(context.Background(), fakeRuntime{identityHash: id.Hash(), cancel: CancelObservation{Attempted: true, Authoritative: true}, errors: genericErrors()}, "xai_grok_tui", id, conformanceFixture(id), "nonce-2")
	if report.Passed() || len(report.Discrepancies) == 0 {
		t.Fatalf("unsupported authoritative cancellation must be a discrepancy: %+v", report)
	}
}

func TestRunConformanceEarlyFailureHasFixedSevenCheckCoverage(t *testing.T) {
	t.Parallel()
	id := conformanceIdentity(t)
	report := RunConformance(context.Background(), fakeRuntime{identityHash: id.Hash(), launchErr: errors.New("token=super-secret")}, "xai_acp", id, conformanceFixture(id), "nonce")
	if report.Coverage.Required != 7 || len(report.Checks) != 7 || report.Coverage.Satisfied != 0 || report.Passed() {
		t.Fatalf("early failure coverage = %+v", report)
	}
}

func TestRunConformanceRejectsUnknownOutcomeAutomaticReplay(t *testing.T) {
	t.Parallel()
	id := conformanceIdentity(t)
	runtime := unsafeReplayRuntime{fakeRuntime: fakeRuntime{
		identityHash: id.Hash(),
		completion:   true,
		cancel:       CancelObservation{Attempted: true},
		errors:       genericErrors(),
	}}
	report := RunConformance(context.Background(), runtime, "xai_acp", id, conformanceFixture(id), "nonce-replay")
	if report.Passed() || checkByName(report, "crash_outcome_unknown_recovery").Passed {
		t.Fatalf("outcome-unknown recovery must block automatic replay: %+v", report)
	}
}

type unsafeReplayRuntime struct{ fakeRuntime }

func (unsafeReplayRuntime) Recover(context.Context, string) (RecoveryObservation, error) {
	return RecoveryObservation{OutcomeUnknown: true, AutomaticReplayBlocked: false}, nil
}

func TestConformanceReceiptNeverSerializesFixtureOrErrorSecrets(t *testing.T) {
	t.Parallel()
	id := conformanceIdentity(t)
	fixture := conformanceFixture(id)
	fixture.FixtureID, fixture.RuntimeVersion, fixture.Source = "fixture-secret-123", "runtime-secret-456", "source-secret-789"
	report := RunConformance(context.Background(), fakeRuntime{identityHash: id.Hash(), launchErr: errors.New("provider response token=error-secret-000")}, "xai_acp", id, fixture, "nonce")
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-secret-123", "runtime-secret-456", "source-secret-789", "error-secret-000"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("receipt leaked %q: %s", secret, encoded)
		}
	}
	if checkByName(report, "no_write_launch_identity").ErrorSHA256 == "" || report.FixtureReceiptSHA256 == "" {
		t.Fatalf("safe receipt hashes missing: %+v", report)
	}
}

func TestClassifyTransportErrorExact(t *testing.T) {
	t.Parallel()
	if got := ClassifyTransportError(429, "anything"); got != TransportRateLimited {
		t.Fatalf("429 = %q", got)
	}
	if got := ClassifyTransportError(400, "unsupported_model"); got != TransportUnsupportedModel {
		t.Fatalf("unsupported model = %q", got)
	}
}

func checkByName(report ConformanceReport, name string) CheckResult {
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	return CheckResult{}
}
