package agentmail

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
)

// ErrDefaultServerInTest is returned when code running inside a `go test`
// binary sends a request to the default Agent Mail port. On a developer
// machine that port is a real mailbox, and a loopback client also carries the
// token `am` was set up with, so a test that falls back to the default URL
// (for example because its environment dropped AGENT_MAIL_URL) would register
// agents, reserve files or send mail for real. Tests point AGENT_MAIL_URL at
// a stub server or at a port nothing listens on.
var ErrDefaultServerInTest = errors.New("agent mail: test binaries may not send requests to the default server port")

// AllowDefaultServerInTestsEnv, set to "1", lets a test binary reach the
// default port anyway, for a deliberate run against a disposable server.
const AllowDefaultServerInTestsEnv = "NTM_TEST_ALLOW_DEFAULT_AGENT_MAIL"

// guardTestBinaryTransport wraps base so that, in a test binary only, requests
// to the default server port fail before any connection is opened. Production
// binaries get base back unchanged.
func guardTestBinaryTransport(base http.RoundTripper) http.RoundTripper {
	if !testing.Testing() {
		return base
	}
	return defaultServerRefusingTransport{base: base}
}

// defaultServerRefusingTransport refuses requests addressed to the default
// Agent Mail port on any host and forwards everything else to base.
type defaultServerRefusingTransport struct {
	base http.RoundTripper
}

func (t defaultServerRefusingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := refuseDefaultServerURL(req.URL); err != nil {
		// A RoundTripper must close the request body, even on error.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach base.
func (t defaultServerRefusingTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func refuseDefaultServerURL(target *url.URL) error {
	if target == nil || os.Getenv(AllowDefaultServerInTestsEnv) == "1" {
		return nil
	}
	port := defaultServerPort()
	if port == "" || target.Port() != port {
		return nil
	}
	return fmt.Errorf("%w (%s): point AGENT_MAIL_URL at a stub or a closed port, or set %s=1",
		ErrDefaultServerInTest, target.Host, AllowDefaultServerInTestsEnv)
}

// defaultServerPort is the port of DefaultBaseURL.
func defaultServerPort() string {
	parsed, err := url.Parse(DefaultBaseURL)
	if err != nil {
		return ""
	}
	return parsed.Port()
}
