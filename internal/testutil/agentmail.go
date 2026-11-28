package testutil

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ClosedAgentMailURL is an Agent Mail endpoint on a loopback port nothing
// listens on, so every request to it fails fast with "connection refused".
//
// Test environments set AGENT_MAIL_URL to this instead of removing the
// variable. With AGENT_MAIL_URL unset, ntm falls back to the default local
// server on port 8765, which on a developer machine is a real mailbox, and a
// loopback client also picks up the token `am` was set up with.
const ClosedAgentMailURL = "http://127.0.0.1:1/mcp/"

// AllowDefaultAgentMailEnv, set to "1", keeps an AGENT_MAIL_URL that names the
// default server port, for a deliberate run against a disposable server.
// internal/agentmail honours the same variable for in-process clients.
const AllowDefaultAgentMailEnv = "NTM_TEST_ALLOW_DEFAULT_AGENT_MAIL"

// defaultAgentMailPort is the port of the default Agent Mail server URL.
const defaultAgentMailPort = "8765"

// ClosedAgentMailEnv returns the KEY=value entries that point a child
// process's Agent Mail client at ClosedAgentMailURL with no token, for env
// builders that start from a scrubbed copy of os.Environ().
func ClosedAgentMailEnv() []string {
	return []string{"AGENT_MAIL_URL=" + ClosedAgentMailURL, "AGENT_MAIL_TOKEN="}
}

// IsolateAgentMailProcess points AGENT_MAIL_URL at ClosedAgentMailURL and
// clears AGENT_MAIL_TOKEN for the whole test process, so every ntm binary the
// suite launches with os.Environ() inherits an endpoint that refuses
// connections. Call it from TestMain before m.Run; tests that need a server
// pass their own stub URL.
//
// With keepExplicit, an AGENT_MAIL_URL the caller already set to another port
// is kept, for opt-in suites documented to run against a disposable server.
// One that names the default port is still replaced unless
// AllowDefaultAgentMailEnv is "1".
func IsolateAgentMailProcess(keepExplicit bool) error {
	if keepExplicit {
		current := strings.TrimSpace(os.Getenv("AGENT_MAIL_URL"))
		if current != "" && (!namesDefaultAgentMailPort(current) || os.Getenv(AllowDefaultAgentMailEnv) == "1") {
			return nil
		}
	}
	if err := os.Setenv("AGENT_MAIL_URL", ClosedAgentMailURL); err != nil {
		return fmt.Errorf("point AGENT_MAIL_URL at a closed port: %w", err)
	}
	if err := os.Setenv("AGENT_MAIL_TOKEN", ""); err != nil {
		return fmt.Errorf("clear AGENT_MAIL_TOKEN: %w", err)
	}
	return nil
}

// namesDefaultAgentMailPort reports whether raw addresses the default server
// port. A value that does not parse counts as the default, so it is replaced.
func namesDefaultAgentMailPort(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return true
	}
	return parsed.Port() == defaultAgentMailPort
}
