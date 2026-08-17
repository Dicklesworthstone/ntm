package serve

// E2E for the embedded web UI (F1b, bd-ws5-ship-or-cut-jv0rc.1.2): a
// hermetic `ntm web` server (same construction, WebUI mounted) must answer
// real HTTP GETs on ALL exported routes with 200 and each route's POSITIVE
// content marker — absence-of-stub alone is gameable, so every route asserts
// its real heading and the absence of the coming-soon marker. The route list
// is derived by globbing web/src/app/**/page.tsx so the inventory cannot
// drift from the source tree (R2 DoD: 12/12 non-stub).

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/webui"
)

// webUIRouteMarkers maps each app route to the positive content marker its
// exported HTML must carry (the route's real heading). The route KEYS are
// verified against the web/src/app glob in both directions, so adding or
// removing a page without updating the marker table fails the test.
var webUIRouteMarkers = map[string]string{
	"/":          "Sessions",
	"/accounts":  "Accounts",
	"/agents":    "Agents",
	"/analytics": "Analytics",
	"/beads":     "Beads",
	"/connect":   "Server URL",
	"/mail":      "Mail",
	"/memory":    "Memory",
	"/pipelines": "Pipelines",
	"/safety":    "Safety",
	"/scanner":   "Scanner",
	"/sessions":  "Session Detail",
}

const comingSoonMarker = "coming soon"

// webAppRoutes derives the route list from the web/src/app source tree.
func webAppRoutes(t *testing.T) []string {
	t.Helper()

	appDir := filepath.Join("..", "..", "web", "src", "app")
	var routes []string
	err := filepath.WalkDir(appDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "page.tsx" {
			return nil
		}
		rel, err := filepath.Rel(appDir, filepath.Dir(path))
		if err != nil {
			return err
		}
		segments := []string{}
		if rel != "." {
			for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
				if strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")") {
					continue
				}
				if strings.HasPrefix(seg, "[") {
					t.Fatalf("dynamic route segment %q in %s breaks the static export", seg, path)
				}
				segments = append(segments, seg)
			}
		}
		routes = append(routes, "/"+strings.Join(segments, "/"))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", appDir, err)
	}
	sort.Strings(routes)
	return routes
}

func newWebUITestServer(t *testing.T) *httptest.Server {
	t.Helper()

	ui, err := webui.FS()
	if err != nil {
		t.Fatalf("webui.FS(): %v", err)
	}
	srv := New(Config{
		Host:     "127.0.0.1",
		Port:     0,
		Version:  "test",
		EventBus: events.NewEventBus(16),
		WebUI:    ui,
	})
	t.Cleanup(func() { srv.Stop() })

	ts := httptest.NewServer(srv.router)
	t.Cleanup(ts.Close)
	return ts
}

func TestWebUIAllRoutesNonStub(t *testing.T) {
	routes := webAppRoutes(t)

	// Marker table and glob-derived inventory must agree in both directions.
	routeSet := make(map[string]bool, len(routes))
	for _, route := range routes {
		routeSet[route] = true
		if _, ok := webUIRouteMarkers[route]; !ok {
			t.Errorf("route %s exists under web/src/app but has no positive marker registered in webUIRouteMarkers", route)
		}
	}
	for route := range webUIRouteMarkers {
		if !routeSet[route] {
			t.Errorf("webUIRouteMarkers lists %s but no page.tsx exists for it", route)
		}
	}
	if len(routes) != 12 {
		t.Errorf("expected the 12 shipped routes (R2 DoD), glob found %d: %v", len(routes), routes)
	}

	ts := newWebUITestServer(t)

	for _, route := range routes {
		marker := webUIRouteMarkers[route]
		for _, path := range []string{route, strings.TrimSuffix(route, "/") + "/"} {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("GET %s: read body: %v", path, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s: status %d, want 200", path, resp.StatusCode)
				continue
			}
			html := string(body)
			if !strings.Contains(html, marker) {
				t.Errorf("GET %s: missing positive content marker %q", path, marker)
			}
			if strings.Contains(strings.ToLower(html), comingSoonMarker) {
				t.Errorf("GET %s: contains stub marker %q", path, comingSoonMarker)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("GET %s: Content-Type %q, want text/html", path, ct)
			}
		}
	}
}

func TestWebUIDoesNotShadowAPIOr404(t *testing.T) {
	ts := newWebUITestServer(t)

	// API namespaces keep JSON semantics.
	resp, err := http.Get(ts.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET /api/v1/health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/health: status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("GET /api/v1/health: Content-Type %q, want application/json", resp.Header.Get("Content-Type"))
	}
	if strings.Contains(strings.ToLower(string(body)), "<html") {
		t.Error("GET /api/v1/health returned HTML — the web UI mount shadowed the API")
	}

	// Unknown API path stays a JSON 404, not the exported 404 page.
	resp, err = http.Get(ts.URL + "/api/v1/definitely-not-a-route")
	if err != nil {
		t.Fatalf("GET unknown api route: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown api route: status %d, want 404", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("GET unknown api route: Content-Type %q, want application/json", resp.Header.Get("Content-Type"))
	}

	// Unknown UI path serves the exported 404 page with a 404 status.
	resp, err = http.Get(ts.URL + "/definitely-not-a-page")
	if err != nil {
		t.Fatalf("GET unknown ui route: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown ui route: status %d, want 404", resp.StatusCode)
	}
	if !strings.Contains(string(body), "404") {
		t.Error("GET unknown ui route: expected the exported 404 page")
	}

	// Static assets are served with long-lived immutable caching.
	respIdx, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	io.Copy(io.Discard, respIdx.Body)
	respIdx.Body.Close()
	if cc := respIdx.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET /: Cache-Control %q, want no-cache", cc)
	}
}

func TestWebUINotMountedByDefault(t *testing.T) {
	srv := New(Config{
		Host:     "127.0.0.1",
		Port:     0,
		Version:  "test",
		EventBus: events.NewEventBus(16),
	})
	t.Cleanup(func() { srv.Stop() })
	ts := httptest.NewServer(srv.router)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/agents")
	if err != nil {
		t.Fatalf("GET /agents: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("plain serve (no --web) should 404 UI routes, got %d", resp.StatusCode)
	}
}
