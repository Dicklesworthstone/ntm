package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

func dashboardReservationScopeConfig(url, token string) *config.Config {
	settings := &config.Config{}
	settings.AgentMail.Enabled = true
	settings.AgentMail.URL = url
	settings.AgentMail.Token = token
	settings.FileReservation.Enabled = true
	settings.FileReservation.AutoReserve = true
	settings.FileReservation.NotifyOnConflict = true
	settings.FileReservation.ExtendOnActivity = true
	return settings
}

// Refuse readiness after recording the real MCP request. These scope tests
// must never start tmux polling or mutate a reservation on the test host.
func dashboardScopeProbeServer(t *testing.T, requests chan<- string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode probe: %v", err)
		}
		if r.Method != http.MethodPost || request.Method != "tools/call" || request.Params.Name != "health_check" {
			t.Errorf("startup used an unexpected operation: %s %+v", r.Method, request)
		}
		select {
		case requests <- r.Header.Get("Authorization"):
		default:
			t.Error("unexpected extra startup probe")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"error": map[string]any{"code": -32603, "message": "scope probe intentionally not ready"},
		})
	}))
}

func TestDashboardReservationUsesTargetSettingsAndEnvironment(t *testing.T) {
	for _, environment := range []bool{false, true} {
		name := "target project"
		if environment {
			name = "environment override"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AGENT_MAIL_URL", "")
			t.Setenv("AGENT_MAIL_TOKEN", "")
			requests := make(chan string, 4)
			server := dashboardScopeProbeServer(t, requests)
			defer server.Close()
			settings := dashboardReservationScopeConfig(server.URL+"/mcp/", "target-token")
			wrong := dashboardReservationScopeConfig("http://127.0.0.1:1/mcp/", "wrong-token")
			wrong.FileReservation.Enabled = false
			previous := cfg
			cfg = wrong
			t.Cleanup(func() { cfg = previous })
			wantToken := "Bearer target-token"
			if environment {
				settings.AgentMail.URL = "http://127.0.0.1:1/mcp/"
				t.Setenv("AGENT_MAIL_URL", server.URL+"/mcp/")
				t.Setenv("AGENT_MAIL_TOKEN", "environment-token")
				wantToken = "Bearer environment-token"
			}
			stop := startDashboardReservationWatcher(context.Background(), "demo", t.TempDir(), settings)
			if stop == nil {
				t.Fatal("launch-directory settings overrode enabled target project")
			}
			defer stop()
			select {
			case token := <-requests:
				if token != wantToken {
					t.Errorf("probe used wrong credential source")
				}
			case <-time.After(time.Second):
				t.Fatal("probe did not reach the selected endpoint")
			}
		})
	}
}

func TestDashboardReservationRefusesMissingAuthority(t *testing.T) {
	for _, mode := range []string{"nil config", "reservations disabled", "mail disabled", "nil context", "cancelled context", "empty session", "empty project"} {
		t.Run(mode, func(t *testing.T) {
			settings := dashboardReservationScopeConfig("http://127.0.0.1:1/mcp/", "unused-token")
			previous := cfg
			cfg = dashboardReservationScopeConfig("http://127.0.0.1:1/mcp/", "unrelated-token")
			t.Cleanup(func() { cfg = previous })
			ctx := context.Background()
			session, project := "demo", t.TempDir()
			switch mode {
			case "nil config":
				settings = nil
			case "reservations disabled":
				settings.FileReservation.Enabled = false
			case "mail disabled":
				settings.AgentMail.Enabled = false
			case "nil context":
				ctx = nil
			case "cancelled context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "empty session":
				session = " "
			case "empty project":
				project = " "
			}
			if stop := startDashboardReservationWatcher(ctx, session, project, settings); stop != nil {
				stop()
				t.Fatal("missing authority still started reservation dependency work")
			}
		})
	}
}

func TestDashboardReservationOwnerCancellationReachesProbe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	entered, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the server can observe a disconnected HTTP client.
		var request json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&request)
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settings := dashboardReservationScopeConfig(server.URL+"/mcp/", "target-token")
	previous := cfg
	cfg = settings
	t.Cleanup(func() { cfg = previous })
	stop := startDashboardReservationWatcher(ctx, "demo", t.TempDir(), settings)
	if stop == nil {
		t.Fatal("enabled scoped watcher was not started")
	}
	defer stop()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe never started")
	}
	cancel() // No Stop call: the dashboard's own context must reach the HTTP I/O.
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not reach the in-flight startup probe")
	}
}

func TestDashboardReservationRetryKeepsItsSettingsSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	requests := make(chan string, 4)
	server := dashboardScopeProbeServer(t, requests)
	defer server.Close()
	settings := dashboardReservationScopeConfig(server.URL+"/mcp/", "target-token")
	stop := startDashboardReservationWatcher(context.Background(), "demo", t.TempDir(), settings)
	if stop == nil {
		t.Fatal("enabled scoped watcher was not started")
	}
	defer stop()
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("first probe never started")
	}
	// Replacing the caller's settings while startup backs off must not retarget
	// an already-owned dashboard lifecycle or introduce concurrent cfg reads.
	*settings = *dashboardReservationScopeConfig("http://127.0.0.1:1/mcp/", "replacement-token")
	select {
	case token := <-requests:
		if token != "Bearer target-token" {
			t.Error("retry switched to replacement credentials")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not retain the original endpoint")
	}
}

func TestDashboardReservationEmptyTokenDoesNotInheritProcessCredential(t *testing.T) {
	const helper = "NTM_TEST_DASHBOARD_RESERVATION_SCOPE_CHILD"
	if os.Getenv(helper) != "1" {
		// Use a separate process so changing Agent Mail's process defaults
		// cannot leak into other native CLI tests.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDashboardReservationEmptyTokenDoesNotInheritProcessCredential$")
		cmd.Env = append(os.Environ(), helper+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated credential-scope probe failed: %v\n%s", err, output)
		}
		return
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	agentmail.UseConfiguredEndpoint("http://127.0.0.1:1/mcp/", "unrelated-process-token")
	requests := make(chan string, 4)
	server := dashboardScopeProbeServer(t, requests)
	defer server.Close()
	settings := dashboardReservationScopeConfig(server.URL+"/mcp/", "")
	cfg = settings // Isolated child: reproduce the original empty-token fallback.
	stop := startDashboardReservationWatcher(context.Background(), "demo", t.TempDir(), settings)
	if stop == nil {
		t.Fatal("enabled target did not start")
	}
	defer stop()
	select {
	case token := <-requests:
		if token != "" {
			t.Fatal("empty target token inherited a different project's credential")
		}
	case <-time.After(time.Second):
		t.Fatal("target endpoint was not selected")
	}
}
