package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	_ "github.com/Dicklesworthstone/ntm/internal/cli"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/serve"
)

var routeParamPlaceholderRE = regexp.MustCompile(`\{[^{}]+\}`)

func normalizeServedRouteKey(method, pattern string) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	p := strings.TrimSpace(pattern)
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	p = routeParamPlaceholderRE.ReplaceAllString(p, "{}")
	return m + " " + p
}

// TestKernelRegisteredRESTBindingsMatchServedRouter imports BOTH internal/cli
// and internal/serve so every explicit kernel.MustRegister RESTBinding across
// the entire binary is verified against the real chi router in internal/serve.
func TestKernelRegisteredRESTBindingsMatchServedRouter(t *testing.T) {
	serve.EnsureKernelCatalogRegistered()
	srv := serve.NewHermeticServer("1.38.0-conformance")
	t.Cleanup(func() { srv.Stop() })

	routes, err := serve.WalkServedRoutes(srv.Router())
	if err != nil {
		t.Fatalf("WalkServedRoutes failed: %v", err)
	}

	servedSet := make(map[string]string, len(routes))
	for _, rt := range routes {
		servedSet[normalizeServedRouteKey(rt.Method, rt.Pattern)] = rt.Method + " " + rt.Pattern
	}

	commands := kernel.List()
	if len(commands) == 0 {
		t.Fatal("kernel.List() returned 0 commands")
	}

	explicitCLICommands := map[string]bool{
		"core.version":                   false,
		"core.deps":                      false,
		"kernel.list":                    false,
		"sessions.create":                false,
		"sessions.list":                  false,
		"sessions.status":                false,
		"sessions.attach":                false,
		"sessions.view":                  false,
		"sessions.zoom":                  false,
		"sessions.health":                false,
		"sessions.controller":            false,
		"sessions.interrupt":             false,
		"sessions.kill":                  false,
		"sessions.adopt":                 false,
		"sessions.scale":                 false,
		"sessions.rebalance":             false,
		"sessions.coordinator":           false,
		"sessions.coordinator_digest":    false,
		"sessions.coordinator_conflicts": false,
		"prompt.preflight":               false,
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, cmd := range commands {
		if _, tracked := explicitCLICommands[cmd.Name]; tracked {
			explicitCLICommands[cmd.Name] = true
			if _, runErr := kernel.Run(cancelledCtx, cmd.Name, "__probe_invalid_type__"); runErr != nil && strings.Contains(runErr.Error(), "not registered") {
				t.Errorf("explicit CLI kernel command %q is missing an in-process handler: %v", cmd.Name, runErr)
			}
			if cmd.REST == nil {
				t.Errorf("explicit CLI kernel command %q is missing a RESTBinding", cmd.Name)
				continue
			}
		}
		if cmd.REST == nil {
			continue
		}
		if !strings.HasPrefix(cmd.REST.Path, "/api/v1/") {
			t.Errorf("kernel command %q has non-canonical REST path %q (must start with /api/v1/)", cmd.Name, cmd.REST.Path)
		}
		key := normalizeServedRouteKey(cmd.REST.Method, cmd.REST.Path)
		if _, ok := servedSet[key]; !ok {
			t.Errorf("kernel command %q declares REST binding %s %s, which is NOT mounted on serve.Server.Router()", cmd.Name, cmd.REST.Method, cmd.REST.Path)
		}
	}

	for name, seen := range explicitCLICommands {
		if !seen {
			t.Errorf("expected explicit CLI kernel command %q to be registered in kernel.List()", name)
		}
	}

	// Exercise the newly wired session & preflight kernel-backed REST endpoints end-to-end
	// against real CLI kernel handlers to guarantee map[string]any decoding never regresses.
	router := srv.Router()

	pfReq := httptest.NewRequest(http.MethodPost, "/api/v1/prompt/preflight", strings.NewReader(`{"prompt":"Verify cross-package kernel router conformance"}`))
	pfReq.Header.Set("Content-Type", "application/json")
	pfRec := httptest.NewRecorder()
	router.ServeHTTP(pfRec, pfReq)
	if pfRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/prompt/preflight status = %d, want 200; body=%s", pfRec.Code, pfRec.Body.String())
	}

	for _, probe := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/sessions/nonexistent_conformance_sess/health"},
		{method: http.MethodPost, path: "/api/v1/sessions/nonexistent_conformance_sess/interrupt", body: `{}`},
		{method: http.MethodPost, path: "/api/v1/sessions/nonexistent_conformance_sess/controller", body: `{"agent_type":"cc","no_prompt":true}`},
		{method: http.MethodDelete, path: "/api/v1/sessions/nonexistent_conformance_sess?force=true"},
		{method: http.MethodPost, path: "/api/v1/sessions/nonexistent_conformance_sess/adopt", body: `{"panes":{"cc":"0,1","agy":"2"},"dry_run":true}`},
		{method: http.MethodPost, path: "/api/v1/sessions/nonexistent_conformance_sess/scale", body: `{"targets":{"cc":2,"ollama":1},"dry_run":true}`},
		{method: http.MethodPost, path: "/api/v1/sessions/nonexistent_conformance_sess/rebalance", body: `{"dry_run":true}`},
		{method: http.MethodGet, path: "/api/v1/sessions/nonexistent_conformance_sess/coordinator"},
		{method: http.MethodPost, path: "/api/v1/sessions/nonexistent_conformance_sess/coordinator/digest", body: `{"send_digest":false}`},
		{method: http.MethodGet, path: "/api/v1/sessions/nonexistent_conformance_sess/coordinator/conflicts"},
	} {
		req := httptest.NewRequest(probe.method, probe.path, strings.NewReader(probe.body))
		if probe.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusMethodNotAllowed || rec.Code == http.StatusNotImplemented {
			t.Fatalf("%s %s returned %d (unmounted or missing handler): %s", probe.method, probe.path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s returned non-JSON body (%d): %v", probe.method, probe.path, rec.Code, err)
		}
		if errStr, _ := body["error"].(string); strings.Contains(errStr, "unsupported input type") || errStr == "session is required" {
			t.Fatalf("%s %s failed map[string]any kernel decoding: %s", probe.method, probe.path, errStr)
		}
	}
}
