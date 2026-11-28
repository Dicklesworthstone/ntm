package testutil

import (
	"os"
	"testing"
)

func TestHermeticEnvPointsUnsetAgentMailURLAtClosedPort(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "")
	if err := os.Unsetenv("AGENT_MAIL_URL"); err != nil {
		t.Fatalf("unset AGENT_MAIL_URL: %v", err)
	}

	HermeticEnv(t)

	if got := os.Getenv("AGENT_MAIL_URL"); got != ClosedAgentMailURL {
		t.Fatalf("AGENT_MAIL_URL = %q, want %q when the ambient environment had none", got, ClosedAgentMailURL)
	}
}

func TestHermeticEnvKeepsPreservedAgentMailURL(t *testing.T) {
	const stub = "http://127.0.0.1:41234/mcp/"
	t.Setenv("AGENT_MAIL_URL", stub)

	HermeticEnv(t, WithPreserveKeys("AGENT_MAIL_URL"))

	if got := os.Getenv("AGENT_MAIL_URL"); got != stub {
		t.Fatalf("AGENT_MAIL_URL = %q, want the preserved %q", got, stub)
	}
}

func TestClosedAgentMailEnvNamesClosedPortAndNoToken(t *testing.T) {
	got := ClosedAgentMailEnv()
	want := []string{"AGENT_MAIL_URL=" + ClosedAgentMailURL, "AGENT_MAIL_TOKEN="}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ClosedAgentMailEnv() = %q, want %q", got, want)
	}
}

func TestIsolateAgentMailProcess(t *testing.T) {
	cases := []struct {
		name         string
		ambientURL   string
		allowDefault string
		keepExplicit bool
		wantURL      string
		wantToken    string
	}{
		{name: "unset, hermetic", ambientURL: "", wantURL: ClosedAgentMailURL},
		{name: "explicit stub, hermetic", ambientURL: "http://127.0.0.1:41234/mcp/", wantURL: ClosedAgentMailURL},
		{name: "default server, hermetic", ambientURL: "http://127.0.0.1:8765/mcp/", wantURL: ClosedAgentMailURL},
		{name: "unset, keep explicit", ambientURL: "", keepExplicit: true, wantURL: ClosedAgentMailURL},
		{name: "default server, keep explicit", ambientURL: "http://127.0.0.1:8765/mcp/", keepExplicit: true, wantURL: ClosedAgentMailURL},
		{name: "remote default port, keep explicit", ambientURL: "http://mail.example.invalid:8765/mcp/", keepExplicit: true, wantURL: ClosedAgentMailURL},
		{name: "unparseable, keep explicit", ambientURL: "127.0.0.1:8765/mcp/", keepExplicit: true, wantURL: ClosedAgentMailURL},
		{name: "disposable server, keep explicit", ambientURL: "http://127.0.0.1:41234/mcp/", keepExplicit: true, wantURL: "http://127.0.0.1:41234/mcp/", wantToken: "ambient-token"},
		{name: "default server opted in, keep explicit", ambientURL: "http://127.0.0.1:8765/mcp/", allowDefault: "1", keepExplicit: true, wantURL: "http://127.0.0.1:8765/mcp/", wantToken: "ambient-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_MAIL_URL", tc.ambientURL)
			t.Setenv("AGENT_MAIL_TOKEN", "ambient-token")
			t.Setenv(AllowDefaultAgentMailEnv, tc.allowDefault)

			if err := IsolateAgentMailProcess(tc.keepExplicit); err != nil {
				t.Fatalf("IsolateAgentMailProcess(%v): %v", tc.keepExplicit, err)
			}

			if got := os.Getenv("AGENT_MAIL_URL"); got != tc.wantURL {
				t.Errorf("AGENT_MAIL_URL = %q, want %q", got, tc.wantURL)
			}
			if got := os.Getenv("AGENT_MAIL_TOKEN"); got != tc.wantToken {
				t.Errorf("AGENT_MAIL_TOKEN = %q, want %q", got, tc.wantToken)
			}
		})
	}
}
