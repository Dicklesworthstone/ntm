package ratelimit

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/provider"
)

const admissionConfigHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func admissionIdentity(t *testing.T, account, model string) provider.Identity {
	t.Helper()
	id, err := provider.NewIdentity("zai", account, model, "https://api.z.ai/api/anthropic", "claude-glm", admissionConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func testAdmissionController(t *testing.T, now *time.Time, cfg AdmissionConfig) *AdmissionController {
	t.Helper()
	c, err := NewAdmissionController(cfg, func() time.Time { return *now }, func() float64 { return 0.5 })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAdmissionIsolatedByFullIdentityScope(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cfg := DefaultAdmissionConfig()
	cfg.MaxConcurrent, cfg.TokenCapacity, cfg.TokensPerSecond = 1, 1, 1
	c := testAdmissionController(t, &now, cfg)
	one := admissionIdentity(t, "kevin", "glm-5.3-flash")
	two := admissionIdentity(t, "other", "glm-5.3-flash")
	if got := c.Acquire(one); !got.Allowed || !got.NoFailover {
		t.Fatalf("first identity decision = %+v", got)
	}
	if got := c.Acquire(one); got.Allowed || !got.NoFailover {
		t.Fatalf("same identity must be capacity-limited: %+v", got)
	}
	if got := c.Acquire(two); !got.Allowed {
		t.Fatalf("distinct account must have an independent budget: %+v", got)
	}
}

func TestAdmissionRetryAfterAndCircuitBreaker(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cfg := DefaultAdmissionConfig()
	cfg.CircuitThreshold, cfg.CircuitOpenFor = 2, 10*time.Second
	c := testAdmissionController(t, &now, cfg)
	id := admissionIdentity(t, "kevin", "glm-5.3-flash")
	first := c.RecordResult(id, ErrorRateLimited, 45*time.Second)
	if first.RetryAt == nil || !first.RetryAt.Equal(now.Add(45*time.Second)) {
		t.Fatalf("explicit retry-after must be exact: %+v", first)
	}
	now = now.Add(45 * time.Second)
	c.RecordResult(id, ErrorOverloaded, 0)
	blocked := c.Acquire(id)
	if blocked.Allowed || blocked.RetryAt == nil || blocked.Reason != ErrorOverloaded {
		t.Fatalf("circuit must deny a second transient failure scope: %+v", blocked)
	}
	c.RecordSuccess(id)
	if got := c.Acquire(id); !got.Allowed {
		t.Fatalf("success should close the exact identity circuit: %+v", got)
	}
}

func TestAdmissionHalfOpenAllowsOnlyOneProbeAcrossAvailableCapacity(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cfg := DefaultAdmissionConfig()
	cfg.MaxConcurrent, cfg.TokenCapacity, cfg.TokensPerSecond = 2, 2, 1
	cfg.CircuitThreshold, cfg.CircuitOpenFor = 1, 10*time.Second
	c := testAdmissionController(t, &now, cfg)
	id := admissionIdentity(t, "kevin", "glm-5.3-flash")

	c.RecordResult(id, ErrorOverloaded, 0)
	now = now.Add(10 * time.Second)
	first := c.Acquire(id)
	if !first.Allowed {
		t.Fatalf("first half-open probe = %+v, want allowed", first)
	}
	second := c.Acquire(id)
	if second.Allowed || second.Reason != ErrorOverloaded || second.RetryAt == nil || !second.NoFailover {
		t.Fatalf("second half-open probe = %+v, want temporary no-failover denial", second)
	}
	c.Release(id)
	c.RecordSuccess(id)
	if afterSuccess := c.Acquire(id); !afterSuccess.Allowed {
		t.Fatalf("closed circuit acquire = %+v, want allowed", afterSuccess)
	}
}

func TestAdmissionRejectsZeroIdentityWithoutCreatingSharedScope(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := testAdmissionController(t, &now, DefaultAdmissionConfig())
	var zero provider.Identity
	if got := c.Acquire(zero); got.Allowed || got.Reason != ErrorIdentityMismatch || !got.NoFailover || got.RetryAt != nil {
		t.Fatalf("zero identity acquire = %+v, want identity mismatch denial", got)
	}
	if got := c.RecordResult(zero, ErrorRateLimited, time.Second); got.Allowed || got.Reason != ErrorIdentityMismatch || !got.NoFailover {
		t.Fatalf("zero identity result = %+v, want identity mismatch denial", got)
	}
	c.Release(zero)
	c.RecordSuccess(zero)
	if len(c.states) != 0 {
		t.Fatalf("zero identity created capacity state: %+v", c.states)
	}
}

func TestAdmissionFullJitterAndPermanentErrorsDoNotRetry(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cfg := DefaultAdmissionConfig()
	cfg.BaseBackoff, cfg.MaxBackoff = 10*time.Second, 40*time.Second
	c, err := NewAdmissionController(cfg, func() time.Time { return now }, func() float64 { return 0.25 })
	if err != nil {
		t.Fatal(err)
	}
	id := admissionIdentity(t, "kevin", "glm-5.3-flash")
	transient := c.RecordResult(id, ErrorOverloaded, 0)
	if transient.RetryAt == nil || !transient.RetryAt.Equal(now.Add(2500*time.Millisecond)) {
		t.Fatalf("full-jitter retry = %+v, want 2.5s", transient)
	}
	permanent := c.RecordResult(id, ErrorUnsupportedModel, 0)
	if permanent.RetryAt != nil || permanent.NoFailover != true {
		t.Fatalf("permanent failure must not retry or fail over: %+v", permanent)
	}
}

func TestClassifyProviderErrorExactMappings(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   ErrorClass
	}{
		{429, "", ErrorRateLimited}, {503, "", ErrorOverloaded}, {400, "unsupported_model", ErrorUnsupportedModel},
		{400, "plan_expired", ErrorPlanExpired}, {401, "", ErrorAuthentication}, {400, "other", ErrorUnknown},
		// Z.ai business codes take precedence over generic HTTP 429 semantics.
		{429, "1302", ErrorRateLimited}, {429, "1305", ErrorOverloaded},
		{429, "1308", ErrorLongPeriodQuota}, {429, "1310", ErrorLongPeriodQuota}, {429, "1316", ErrorLongPeriodQuota},
		{429, "1309", ErrorPlanExpired}, {429, "1314", ErrorPlanExpired},
		{429, "1311", ErrorUnsupportedModel}, {429, "1313", ErrorUsageRestricted},
		{429, "1113", ErrorInsufficientBalance}, {429, "1000", ErrorAuthentication}, {401, "1005", ErrorAuthentication}, {429, "1211", ErrorUnsupportedModel},
		{400, "1303", ErrorUnknown}, {400, "1312", ErrorUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyProviderError(tc.status, tc.code); got != tc.want {
			t.Errorf("ClassifyProviderError(%d, %q) = %q, want %q", tc.status, tc.code, got, tc.want)
		}
	}
}

func TestAdmissionPermanentZaiBusinessCodesDoNotRetry(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := testAdmissionController(t, &now, DefaultAdmissionConfig())
	id := admissionIdentity(t, "kevin", "glm-5.3-flash")
	for _, code := range []string{"1308", "1316", "1309", "1314", "1311", "1313", "1113", "1000"} {
		class := ClassifyProviderError(429, code)
		decision := c.RecordResult(id, class, 30*time.Second)
		if decision.RetryAt != nil || !decision.NoFailover {
			t.Fatalf("code %s class %q decision = %+v, want terminal no-failover", code, class, decision)
		}
	}
}

func TestAdmissionPermanentClassificationPersistsUntilExplicitReset(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cfg := DefaultAdmissionConfig()
	cfg.TokenCapacity, cfg.TokensPerSecond = 1, 1
	c := testAdmissionController(t, &now, cfg)
	id := admissionIdentity(t, "kevin", "glm-5.3-flash")

	first := c.RecordResult(id, ErrorUnsupportedModel, 0)
	if first.Allowed || first.Reason != ErrorUnsupportedModel || first.RetryAt != nil || !first.NoFailover {
		t.Fatalf("initial permanent decision = %+v", first)
	}
	if blocked := c.Acquire(id); blocked.Allowed || blocked.Reason != ErrorUnsupportedModel || blocked.RetryAt != nil || !blocked.NoFailover {
		t.Fatalf("permanent classification did not persist: %+v", blocked)
	}
	c.RecordSuccess(id)
	if stillBlocked := c.Acquire(id); stillBlocked.Allowed || stillBlocked.Reason != ErrorUnsupportedModel {
		t.Fatalf("RecordSuccess reopened permanent classification: %+v", stillBlocked)
	}
	c.Reset(id)
	if afterReset := c.Acquire(id); !afterReset.Allowed || !afterReset.NoFailover {
		t.Fatalf("explicit reset did not reopen identity: %+v", afterReset)
	}
}
