package provider

import "testing"

const testConfigHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestNewIdentityNormalizesAndDerivesStableScope(t *testing.T) {
	t.Parallel()
	one, err := NewIdentity(" ZAI ", "Kevin", "glm-5.3-flash", "HTTPS://API.Z.AI:443/api/anthropic/", "Claude-GLM", testConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewIdentity("zai", "kevin", "glm-5.3-flash", "https://api.z.ai/api/anthropic", "claude-glm", testConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := one.Endpoint(), "https://api.z.ai/api/anthropic"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
	if one.Hash() != two.Hash() || one.CapacityScope() != two.CapacityScope() {
		t.Fatalf("equivalent identities must share hash/scope: %q %q", one.Hash(), two.Hash())
	}
	if one.Hash() == "" || one.Hash() == one.Endpoint() {
		t.Fatalf("unsafe identity hash %q", one.Hash())
	}
	if got := one.EvidenceGrade(); got != IdentityEvidenceProfileAttested {
		t.Fatalf("identity evidence = %q, want profile_attested", got)
	}
}

func TestNewIdentityRejectsSecretBearingOrAmbiguousInput(t *testing.T) {
	t.Parallel()
	base := []string{"zai", "kevin", "glm-5.3-flash", "https://api.z.ai/api/anthropic", "claude-glm", testConfigHash}
	cases := []struct {
		name string
		edit func([]string)
	}{
		{"http", func(v []string) { v[3] = "http://api.z.ai" }},
		{"userinfo", func(v []string) { v[3] = "https://token@api.z.ai" }},
		{"query", func(v []string) { v[3] = "https://api.z.ai/api?token=secret" }},
		{"fragment", func(v []string) { v[3] = "https://api.z.ai/#secret" }},
		{"bad hash", func(v []string) { v[5] = "not-a-hash" }},
		{"blank alias", func(v []string) { v[1] = "" }},
		{"model control", func(v []string) { v[2] = "glm\nsecret" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := append([]string(nil), base...)
			tc.edit(v)
			if _, err := NewIdentity(v[0], v[1], v[2], v[3], v[4], v[5]); err == nil {
				t.Fatal("NewIdentity() error = nil")
			}
		})
	}
}

func TestZeroIdentityIsInvalidAndHasNoCapacityScope(t *testing.T) {
	var identity Identity
	if identity.Valid() || identity.Hash() != "" || identity.CapacityScope() != "" {
		t.Fatalf("zero identity must be unusable: hash=%q scope=%q", identity.Hash(), identity.CapacityScope())
	}
	if got := identity.EvidenceGrade(); got != IdentityEvidenceUnverified {
		t.Fatalf("zero identity evidence = %q, want unverified", got)
	}
}
