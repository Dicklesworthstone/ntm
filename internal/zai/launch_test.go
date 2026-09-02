package zai

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/provider"
)

func TestRestrictedLaunchCommandIsNTMCompiledAndReadOnly(t *testing.T) {
	cmd, err := RestrictedLaunchCommand("/opt/Claude Code/claude", "https://api.z.ai/api/anthropic", "glm-5.3-flash")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"if [ -z", "NTM_ZAI_AUTH_REQUIRED", "exec env -i", "ANTHROPIC_AUTH_TOKEN=\"${ZAI_API_KEY:-${ANTHROPIC_AUTH_TOKEN:-}}\"", "ANTHROPIC_BASE_URL='https://api.z.ai/api/anthropic'", "--model 'glm-5.3-flash'", "--restricted", "--safe-mode", "--strict-mcp-config", "--disable-slash-commands", "--no-chrome", "--permission-mode dontAsk", "--tools 'Read,Glob,Grep,WebSearch'", "--allowedTools 'Read,Glob,Grep,WebSearch'", "--disallowedTools 'Bash,Edit,Write,NotebookEdit'"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("command %q missing %q", cmd, want)
		}
	}
}

func TestRestrictedLaunchCommandRejectsNonOfficialEndpoint(t *testing.T) {
	if _, err := RestrictedLaunchCommand("claude", "https://example.invalid/api/anthropic", "glm-5.3-flash"); err == nil {
		t.Fatal("non-official endpoint was accepted")
	}
}

func TestProbeArgsDisableEveryTool(t *testing.T) {
	args := probeArgs("nonce", "glm-5.3-flash")
	for _, flag := range []string{"--tools", "--allowedTools"} {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag {
				found = true
				if args[i+1] != "" {
					t.Fatalf("%s value = %q, want empty", flag, args[i+1])
				}
			}
		}
		if !found {
			t.Fatalf("missing %s", flag)
		}
	}
}

func TestStructuredErrorStatusUsesOnlyJSONCodeAndStatus(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want provider.ErrorClass
	}{
		{`{"status":429,"code":1302}`, provider.ErrorRateLimited},
		{`{"status":429,"code":"1309"}`, provider.ErrorPlanExpired},
		{`{"status":400,"error":{"code":1211}}`, provider.ErrorUnsupportedModel},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"{\\\"status\\\":429,\\\"code\\\":1302}"}]},"metadata":{"code":1302}}`, provider.ErrorUnknown},
		{`{"status":"429junk","code":"unmapped"}`, provider.ErrorUnknown},
		{`rate limit 1302`, provider.ErrorUnknown},
	} {
		status, code := structuredErrorStatus([]byte(test.raw))
		if got := provider.ClassifyProviderError(status, code); got != test.want {
			t.Fatalf("%q classified %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestBoundedBufferNeverRetainsMoreThanLimit(t *testing.T) {
	b := &boundedBuffer{limit: 4}
	if _, err := b.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "abcd" || !b.exceeded {
		t.Fatalf("bounded buffer=(%q,%v)", got, b.exceeded)
	}
}

func TestValidateExecutableRejectsShellFragment(t *testing.T) {
	for _, binary := range []string{"claude --model x", "claude; rm -rf x", "'claude'", "\nclaude"} {
		if err := ValidateExecutable(binary); err == nil {
			t.Fatalf("ValidateExecutable(%q) succeeded", binary)
		}
	}
}

func TestParseEvidenceRequiresNonceAndSessionScopedExactModel(t *testing.T) {
	nonce := "ntm-zai-test"
	good := []byte(`{"type":"system","subtype":"init","session_id":"s-1","model":"glm-5.3-flash"}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"session_id":"s-1","result":"ntm-zai-test"}`)
	seen, bound, session := parseEvidence(good, nonce, "glm-5.3-flash")
	if !seen || !bound || session != "s-1" {
		t.Fatalf("evidence=(%v,%v,%q)", seen, bound, session)
	}
	bad := []byte(`{"type":"system","subtype":"init","session_id":"s-1","model":"other"}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"session_id":"s-1","result":"ntm-zai-test"}`)
	seen, bound, _ = parseEvidence(bad, nonce, "glm-5.3-flash")
	if seen || bound {
		t.Fatalf("wrong-model evidence=(%v,%v)", seen, bound)
	}
	requestOnly := []byte(`{"type":"system","subtype":"init","session_id":"s-1","model":"glm-5.3-flash"}` + "\n" + `{"type":"user","message":"ntm-zai-test"}`)
	seen, bound, _ = parseEvidence(requestOnly, nonce, "glm-5.3-flash")
	if seen || !bound {
		t.Fatalf("request echo must not prove nonce: (%v,%v)", seen, bound)
	}
}

func TestParseEvidenceRejectsSplitAndNestedPromptEvidence(t *testing.T) {
	nonce := "ntm-zai-test"
	for _, raw := range []string{
		`{"type":"system","subtype":"init","session_id":"s-1","model":"other","nested":{"model":"glm-5.3-flash"}}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"session_id":"s-1","result":"ntm-zai-test"}`,
		`{"type":"system","subtype":"other","session_id":"s-1","model":"glm-5.3-flash"}` + "\n" + `{"type":"assistant","message":{"content":[{"type":"text","text":"ntm-zai-test"}],"request":{"prompt":"ntm-zai-test"}}}`,
		`{"type":"system","subtype":"init","session_id":"s-1","model":"glm-5.3-flash"}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"session_id":"s-1","result":"not-the-nonce","request":{"prompt":"ntm-zai-test"}}`,
		`{"type":"system","subtype":"init","session_id":"s-1","model":"glm-5.3-flash"}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"session_id":"other","result":"ntm-zai-test"}`,
		`{"type":"system","subtype":"init","session_id":"s-1","model":"glm-5.3-flash"}` + "\n" + `{"type":"system","subtype":"init","session_id":"other","model":"glm-5.3-flash"}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"session_id":"s-1","result":"ntm-zai-test"}`,
		`{"type":"system","subtype":"init","session_id":"wrong","model":"other"}` + "\n" + `{"type":"system","subtype":"init","session_id":"s-1","model":"glm-5.3-flash"}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"session_id":"s-1","result":"ntm-zai-test"}`,
	} {
		seen, bound, _ := parseEvidence([]byte(raw), nonce, "glm-5.3-flash")
		if seen && bound {
			t.Fatalf("adversarial input promoted evidence: %s", raw)
		}
	}
}

func TestProbeFailsBeforeExecWithoutExplicitAuth(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	receipt, err := Probe(t.Context(), ProbeSpec{Binary: "definitely-not-a-real-claude", Endpoint: OfficialEndpoint, Model: "glm-5.3-flash"})
	if err == nil || receipt.FailureClass != "authentication" || receipt.ProviderErrorClass != provider.ErrorAuthentication {
		t.Fatalf("missing-auth receipt=%+v err=%v", receipt, err)
	}
}

func TestMinimalEnvironmentScrubsUnrelatedCredentials(t *testing.T) {
	env := minimalEnvironment([]string{"PATH=/bin", "ZAI_API_KEY=zai-token", "ANTHROPIC_AUTH_TOKEN=other", "XAI_API_KEY=no", "AWS_SECRET_ACCESS_KEY=no", "GH_TOKEN=no"}, OfficialEndpoint, "glm-5.3-flash")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "ANTHROPIC_AUTH_TOKEN=zai-token") || strings.Contains(joined, "ZAI_API_KEY") || strings.Contains(joined, "XAI_API_KEY") || strings.Contains(joined, "AWS_SECRET") || strings.Contains(joined, "GH_TOKEN") {
		t.Fatalf("environment not scrubbed: %q", joined)
	}
}

func TestCanonicalAuthRequiresExplicitZAIOrAnthropicToken(t *testing.T) {
	if got := canonicalAuth([]string{"XAI_API_KEY=no", "GH_TOKEN=no"}); got != "" {
		t.Fatalf("unexpected auth=%q", got)
	}
	if got := canonicalAuth([]string{"ANTHROPIC_AUTH_TOKEN=anthropic", "ZAI_API_KEY=zai"}); got != "zai" {
		t.Fatalf("auth precedence=%q", got)
	}
}
