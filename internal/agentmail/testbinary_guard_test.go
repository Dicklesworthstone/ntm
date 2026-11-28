package agentmail

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingTransport answers every request itself and records its host, so a
// test can tell whether the guard forwarded a request or stopped it.
type recordingTransport struct {
	hosts []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.hosts = append(r.hosts, req.URL.Host)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// statusOf sends req and returns the response status, closing the body.
func statusOf(client *http.Client, req *http.Request) (int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// TestScrubbedEnvironmentCannotReachDefaultServer proves that a test whose
// environment dropped AGENT_MAIL_URL and AGENT_MAIL_TOKEN, the way an env
// scrubber that deletes the variables does, still resolves to the default URL
// but cannot send that server a single request.
func TestScrubbedEnvironmentCannotReachDefaultServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	t.Setenv(AllowDefaultServerInTestsEnv, "")
	prevURL, prevToken := configuredEndpointDefaults()
	UseConfiguredEndpoint("", "")
	t.Cleanup(func() { UseConfiguredEndpoint(prevURL, prevToken) })

	c := NewClient()
	if got := c.BaseURL(); got != DefaultBaseURL {
		t.Fatalf("BaseURL() = %q, want the default %q for a scrubbed environment", got, DefaultBaseURL)
	}
	if _, ok := c.httpClient.Transport.(defaultServerRefusingTransport); !ok {
		t.Fatalf("client transport in a test binary = %T, want defaultServerRefusingTransport", c.httpClient.Transport)
	}
	if _, ok := c.overseerHTTPClient().Transport.(defaultServerRefusingTransport); !ok {
		t.Fatalf("overseer transport in a test binary = %T, want defaultServerRefusingTransport", c.overseerHTTPClient().Transport)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultBaseURL, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	status, err := statusOf(c.httpClient, req)
	if err == nil {
		t.Fatalf("request to the default server got a response (status %d)", status)
	}
	if !errors.Is(err, ErrDefaultServerInTest) {
		t.Fatalf("request to the default server: err = %v, want ErrDefaultServerInTest", err)
	}

	if c.IsAvailableContext(ctx) {
		t.Fatal("IsAvailableContext() = true for the default server inside a test binary")
	}
}

func TestDefaultServerRefusingTransportBlocksOnlyTheDefaultPort(t *testing.T) {
	t.Setenv(AllowDefaultServerInTestsEnv, "")
	if defaultServerPort() == "" {
		t.Fatalf("no port in DefaultBaseURL %q: the guard would never refuse anything", DefaultBaseURL)
	}

	cases := []struct {
		target  string
		refused bool
	}{
		{target: "http://127.0.0.1:8765/mcp/", refused: true},
		{target: "http://localhost:8765/mcp/", refused: true},
		{target: "http://[::1]:8765/mcp/", refused: true},
		{target: "http://mail.example.invalid:8765/mcp/", refused: true},
		{target: "http://127.0.0.1:1/mcp/", refused: false},
		{target: "http://127.0.0.1:18765/mcp/", refused: false},
		{target: "http://localhost/mcp/", refused: false},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			rec := &recordingTransport{}
			transport := defaultServerRefusingTransport{base: rec}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := transport.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tc.refused {
				if !errors.Is(err, ErrDefaultServerInTest) {
					t.Fatalf("err = %v, want ErrDefaultServerInTest", err)
				}
				if len(rec.hosts) != 0 {
					t.Fatalf("refused request still reached the base transport: %v", rec.hosts)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want the request forwarded", err)
			}
			if len(rec.hosts) != 1 {
				t.Fatalf("base transport saw %v, want exactly one forwarded request", rec.hosts)
			}
		})
	}
}

func TestDefaultServerRefusingTransportHonoursOptIn(t *testing.T) {
	t.Setenv(AllowDefaultServerInTestsEnv, "1")
	rec := &recordingTransport{}
	transport := defaultServerRefusingTransport{base: rec}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, DefaultBaseURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("err = %v, want the request forwarded with %s=1", err, AllowDefaultServerInTestsEnv)
	}
	_ = resp.Body.Close()
	if len(rec.hosts) != 1 {
		t.Fatalf("base transport saw %v, want the opted-in request forwarded", rec.hosts)
	}
}
