package agentmail

import (
	"strings"
	"testing"
)

// TestConfigOptionsEnvironmentOverridesConfig pins the precedence rule every
// Agent Mail call site now shares: NewClient reads AGENT_MAIL_URL /
// AGENT_MAIL_TOKEN before applying options, so a configured value is yielded
// only when the matching variable is unset. Copies of this rule had drifted
// across call sites before ntm#316 consolidated them here.
func TestConfigOptionsEnvironmentOverridesConfig(t *testing.T) {
	const (
		cfgURL   = "http://config.test:9000/mcp/"
		cfgToken = "config-token"
		envURL   = "http://env.test:9100/mcp/"
		envToken = "env-token"
	)

	cases := []struct {
		name      string
		envURL    string
		envToken  string
		wantURL   string
		wantToken string
	}{
		{
			name:      "no environment: config wins",
			wantURL:   cfgURL,
			wantToken: cfgToken,
		},
		{
			name:      "AGENT_MAIL_URL set: environment URL wins, config token still applies",
			envURL:    envURL,
			wantURL:   envURL,
			wantToken: cfgToken,
		},
		{
			name:      "AGENT_MAIL_TOKEN set: environment token wins, config URL still applies",
			envToken:  envToken,
			wantURL:   cfgURL,
			wantToken: envToken,
		},
		{
			name:      "both set: environment wins outright",
			envURL:    envURL,
			envToken:  envToken,
			wantURL:   envURL,
			wantToken: envToken,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_MAIL_URL", tc.envURL)
			t.Setenv("AGENT_MAIL_TOKEN", tc.envToken)

			c := NewClient(ConfigOptions(cfgURL, cfgToken)...)

			if got := strings.TrimSuffix(c.BaseURL(), "/"); got != strings.TrimSuffix(tc.wantURL, "/") {
				t.Errorf("BaseURL() = %q, want %q", got, tc.wantURL)
			}
			if c.bearerToken != tc.wantToken {
				t.Errorf("bearer token = %q, want %q", c.bearerToken, tc.wantToken)
			}
		})
	}
}

// TestConfigOptionsEmptyConfigKeepsDefaults verifies that an unconfigured
// endpoint yields no options at all, leaving the client on its default base
// URL with no bearer.
func TestConfigOptionsEmptyConfigKeepsDefaults(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	if opts := ConfigOptions("", ""); len(opts) != 0 {
		t.Fatalf("ConfigOptions(\"\", \"\") returned %d options, want 0", len(opts))
	}

	c := NewClient(ConfigOptions("", "")...)
	if c.BaseURL() != DefaultBaseURL {
		t.Errorf("BaseURL() = %q, want the default %q", c.BaseURL(), DefaultBaseURL)
	}
	if c.bearerToken != "" {
		t.Errorf("bearer token = %q, want empty", c.bearerToken)
	}
}
