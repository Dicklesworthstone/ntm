package serve

// E2E for the embedded web UI (F1b, bd-ws5-ship-or-cut-jv0rc.1.2): a
// hermetic `ntm web` server (same construction, WebUI mounted) must answer
// real HTTP GETs on ALL exported routes with 200 and each route's POSITIVE
// content marker — absence-of-stub alone is gameable, so every route asserts
// its real heading and the absence of the coming-soon marker. The route list
// is derived by globbing web/src/app/**/page.tsx so the inventory cannot
// drift from the source tree (R2 DoD: 12/12 non-stub).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
	"/attention": "Attention",
	"/beads":     "Beads",
	"/connect":   "Server URL",
	"/ensemble":  "Ensemble",
	"/mail":      "Mail",
	"/memory":    "Memory",
	"/pipelines": "Pipelines",
	"/safety":    "Safety",
	"/scanner":   "Scanner",
	"/sessions":  "Session Detail",
	"/swarm":     "Swarm",
	"/worktrees": "Worktrees",
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
	if len(routes) != 16 {
		t.Errorf("expected the 16 shipped routes (R2 DoD + /ensemble + /attention + /swarm + /worktrees), glob found %d: %v", len(routes), routes)
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

func lookupChromeBinary() string {
	for _, candidate := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if bin, err := exec.LookPath(candidate); err == nil && bin != "" {
			return bin
		}
	}
	return ""
}

func reserveQualificationPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release reserved local port: %v", err)
	}
	return port
}

func installQualificationTmuxFixture(t *testing.T, paneLines []string) {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	socketRoot, err := os.MkdirTemp("", "ntmbq")
	if err != nil {
		t.Fatalf("create tmux socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	t.Setenv("TMUX_TMPDIR", socketRoot)

	panesPath := filepath.Join(t.TempDir(), "panes.txt")
	if err := os.WriteFile(panesPath, []byte(strings.Join(paneLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write pane fixture: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "tmux")
	// list-panes returns our deterministic pane roster; list-sessions exits 1
	// with a non-"no server" error so startup ReconcileSessions treats tmux as
	// skipped and preserves our seeded active session in state.Store.
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  list-panes) cat %q ;;\n  list-sessions) echo \"hermetic fixture\" >&2; exit 1 ;;\n  *) : ;;\nesac\n", panesPath)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	prevClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = prevClient })
}

func formatConsoleArgs(args []*cdpruntime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == nil {
			continue
		}
		if len(arg.Value) > 0 {
			parts = append(parts, string(arg.Value))
		} else if arg.Description != "" {
			parts = append(parts, arg.Description)
		} else {
			parts = append(parts, string(arg.Type))
		}
	}
	return strings.Join(parts, " ")
}

func waitForDOMCondition(ctx context.Context, desc, expr string) error {
	deadline := time.Now().Add(8 * time.Second)
	for {
		var ok bool
		err := chromedp.Run(ctx, chromedp.Evaluate(expr, &ok))
		if err == nil && ok {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("context done waiting for %s: %w (last err: %v)", desc, ctx.Err(), err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s (last err: %v)", desc, err)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func TestWebUIHeadlessChromeBrowserQualification(t *testing.T) {
	chromeBin := lookupChromeBinary()
	if chromeBin == "" {
		t.Skip("no google-chrome/chromium/chromium-browser binary found in PATH")
	}
	t.Logf("qualifying embedded Web UI with headless browser: %s", chromeBin)

	const (
		seededSession = "qual-browser-session"
		seededAgent   = seededSession + "__cc_1"
		seededSummary = "Browser qualification attention item"
	)

	installQualificationTmuxFixture(t, []string{
		paneLine("%0", 0, "shell", "user", 100, false),
		paneLine("%1", 1, seededAgent, "cc", 4242, false),
	})

	projectDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "webui-browser-qual.db")
	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatalf("store.Migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       64,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, robot.WithAttentionStore(store))
	oldFeed := robot.GetAttentionFeed()
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	now := time.Now().UTC()
	staleAfter := now.Add(15 * time.Minute)
	if err := store.CreateSession(&state.Session{
		ID:          seededSession,
		Name:        seededSession,
		ProjectPath: projectDir,
		CreatedAt:   now,
		Status:      state.SessionActive,
	}); err != nil {
		t.Fatalf("seed Session: %v", err)
	}
	if err := store.UpsertRuntimeSession(&state.RuntimeSession{
		Name:         seededSession,
		ProjectPath:  projectDir,
		Attached:     true,
		WindowCount:  1,
		PaneCount:    2,
		AgentCount:   1,
		ActiveAgents: 1,
		HealthStatus: state.HealthStatusHealthy,
		CreatedAt:    &now,
		CollectedAt:  now,
		StaleAfter:   staleAfter,
	}); err != nil {
		t.Fatalf("seed RuntimeSession: %v", err)
	}
	if err := store.UpsertRuntimeAgent(&state.RuntimeAgent{
		ID:             seededSession + ":%1",
		SessionName:    seededSession,
		Pane:           "%1",
		AgentType:      "cc",
		TypeConfidence: 1.0,
		TypeMethod:     "title",
		State:          state.AgentStateActive,
		HealthStatus:   state.HealthStatusHealthy,
		LastOutputAt:   &now,
		CollectedAt:    now,
		StaleAfter:     staleAfter,
	}); err != nil {
		t.Fatalf("seed RuntimeAgent: %v", err)
	}
	seededAttn := feed.Append(robot.AttentionEvent{
		Ts:            now.Format(time.RFC3339Nano),
		Session:       seededSession,
		Pane:          1,
		Category:      robot.EventCategoryAgent,
		Type:          robot.EventTypeAgentStateChange,
		Source:        "browser_qualification",
		Actionability: robot.ActionabilityActionRequired,
		Severity:      robot.SeverityWarning,
		Summary:       seededSummary,
	})
	if err := store.UpsertAttentionItemState(&state.AttentionItemState{
		ItemKey:  fmt.Sprintf("cursor:%d", seededAttn.Cursor),
		State:    state.AttentionStateNew,
		Pinned:   true,
		PinnedAt: &now,
		PinnedBy: "browser-qual",
	}); err != nil {
		t.Fatalf("seed AttentionItemState: %v", err)
	}

	ui, err := webui.FS()
	if err != nil {
		t.Fatalf("webui.FS(): %v", err)
	}

	port := reserveQualificationPort(t)
	srv := New(Config{
		Host:       "127.0.0.1",
		Port:       port,
		ProjectDir: projectDir,
		Version:    "qual-test",
		EventBus:   events.NewEventBus(32),
		StateStore: store,
		WebUI:      ui,
	})
	srv.mailClient = deadAgentMailClient(t)
	srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
		return nil, errServeTestAgentSpawnDisabled
	}
	srv.listLiveSessions = func(context.Context) ([]tmux.Session, error) {
		return []tmux.Session{{Name: seededSession, Windows: 1, Attached: true}}, nil
	}

	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Start(srvCtx)
	}()
	// Registered BEFORE browser cleanup so LIFO teardown closes Chrome first,
	// avoiding spurious WebSocket disconnect console errors during shutdown.
	t.Cleanup(func() {
		srvCancel()
		select {
		case err := <-srvErrCh:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for server shutdown")
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	readyDeadline := time.Now().Add(5 * time.Second)
	for {
		resp, reqErr := http.Get(baseURL + "/api/v1/health")
		if reqErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("server at %s did not become healthy in time (last err: %v)", baseURL, reqErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromeBin),
		chromedp.UserDataDir(t.TempDir()),
		chromedp.Flag("headless", "new"),
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	timeoutCtx, timeoutCancel := context.WithTimeout(browserCtx, 20*time.Second)
	t.Cleanup(func() {
		timeoutCancel()
		browserCancel()
		allocCancel()
	})

	var (
		diagMu             sync.Mutex
		uncaughtExceptions []string
		consoleErrors      []string
	)
	chromedp.ListenTarget(timeoutCtx, func(ev interface{}) {
		switch e := ev.(type) {
		case *cdpruntime.EventExceptionThrown:
			msg := "uncaught exception"
			if e.ExceptionDetails != nil {
				msg = e.ExceptionDetails.Error()
			}
			diagMu.Lock()
			uncaughtExceptions = append(uncaughtExceptions, msg)
			diagMu.Unlock()
		case *cdpruntime.EventConsoleAPICalled:
			if e.Type == cdpruntime.APITypeError {
				diagMu.Lock()
				consoleErrors = append(consoleErrors, formatConsoleArgs(e.Args))
				diagMu.Unlock()
			}
		}
	})

	connJSON, err := json.Marshal(map[string]string{"baseUrl": baseURL})
	if err != nil {
		t.Fatalf("marshal connection config: %v", err)
	}
	preloadScript := fmt.Sprintf(`try { window.localStorage.setItem("ntm-connection", %q); } catch (_) {}`, string(connJSON))

	// 1. Navigate to "/" and verify hydrated Next.js Sessions dashboard + seeded session + live API fetch.
	var homeHeading string
	if err := chromedp.Run(timeoutCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, addErr := page.AddScriptToEvaluateOnNewDocument(preloadScript).Do(ctx)
			return addErr
		}),
		chromedp.Navigate(baseURL+"/"),
		chromedp.WaitVisible("h1", chromedp.ByQuery),
		chromedp.Text("h1", &homeHeading, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("navigate and hydrate /: %v", err)
	}
	if strings.TrimSpace(homeHeading) != "Sessions" {
		t.Fatalf("GET / hydrated h1 = %q, want %q", homeHeading, "Sessions")
	}
	if err := waitForDOMCondition(timeoutCtx, "seeded session card on /",
		fmt.Sprintf(`document.body && document.body.innerText.includes(%q)`, seededSession),
	); err != nil {
		t.Fatalf("seeded session %q did not render on /: %v", seededSession, err)
	}

	// Verify in-browser live fetch against 127.0.0.1:<port> succeeds with zero CORS/CSP errors
	// and returns our seeded attention item.
	var attentionSummaryOK bool
	if err := chromedp.Run(timeoutCtx, chromedp.Evaluate(
		fmt.Sprintf(`fetch(%q).then(r => r.ok ? r.text() : Promise.reject("status "+r.status)).then(t => t.includes(%q))`,
			baseURL+"/api/v1/attention/events", seededSummary),
		&attentionSummaryOK,
		func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
			return p.WithAwaitPromise(true)
		},
	)); err != nil {
		t.Fatalf("in-browser fetch /api/v1/attention/events failed: %v", err)
	}
	if !attentionSummaryOK {
		t.Fatalf("in-browser fetch /api/v1/attention/events did not contain seeded summary %q", seededSummary)
	}
	t.Logf("verified / hydrated heading=%q seeded_session=%q attention_fetch=ok", strings.TrimSpace(homeHeading), seededSession)

	// 2. Navigate across key routes (/agents, /beads, /scanner, /analytics) via hydrated SPA nav
	// and assert real DOM headings without "coming soon".
	routeChecks := []struct {
		route       string
		wantHeading string
		extraText   string
	}{
		{route: "/agents", wantHeading: "Agents", extraText: seededAgent},
		{route: "/ensemble", wantHeading: "Ensemble", extraText: "project-diagnosis"},
		{route: "/attention", wantHeading: "Attention", extraText: seededSummary},
		{route: "/swarm", wantHeading: "Swarm", extraText: "short_smoke"},
		{route: "/worktrees", wantHeading: "Worktrees"},
		{route: "/beads", wantHeading: "Beads"},
		{route: "/scanner", wantHeading: "Scanner"},
		{route: "/analytics", wantHeading: "Analytics", extraText: seededSession},
	}

	for _, rc := range routeChecks {
		navSelector := fmt.Sprintf(`nav[aria-label="Main navigation"] a[href=%q], nav[aria-label="Main navigation"] a[href=%q]`, rc.route+"/", rc.route)
		if err := chromedp.Run(timeoutCtx,
			chromedp.WaitVisible(navSelector, chromedp.ByQuery),
			chromedp.Click(navSelector, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("click nav link %s: %v", rc.route, err)
		}
		if err := waitForDOMCondition(timeoutCtx, "heading "+rc.wantHeading+" on "+rc.route, fmt.Sprintf(`(() => {
			const h1 = document.querySelector("h1");
			return Boolean(h1 && h1.textContent && h1.textContent.trim() === %q && window.location.pathname.replace(/\/+$/, "") === %q);
		})()`, rc.wantHeading, rc.route)); err != nil {
			t.Fatalf("route %s did not hydrate heading %q: %v", rc.route, rc.wantHeading, err)
		}
		if rc.extraText != "" {
			if err := waitForDOMCondition(timeoutCtx, "seeded content "+rc.extraText+" on "+rc.route,
				fmt.Sprintf(`document.body && document.body.innerText.includes(%q)`, rc.extraText),
			); err != nil {
				t.Fatalf("route %s missing expected live data %q: %v", rc.route, rc.extraText, err)
			}
		}
		var bodyText string
		if err := chromedp.Run(timeoutCtx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &bodyText)); err != nil {
			t.Fatalf("read body text on %s: %v", rc.route, err)
		}
		if strings.Contains(strings.ToLower(bodyText), comingSoonMarker) {
			t.Fatalf("route %s rendered stub marker %q", rc.route, comingSoonMarker)
		}
		t.Logf("verified route %s hydrated heading=%q (coming-soon absent)", rc.route, rc.wantHeading)
	}

	// 3. Trigger Ctrl+K and verify the Command Palette dialog opens in the real browser DOM
	// and loads kernel commands from /api/v1/kernel/commands.
	const paletteSelector = `[role="dialog"][aria-label="Command Palette"]`
	if err := chromedp.Run(timeoutCtx,
		input.DispatchKeyEvent(input.KeyRawDown).
			WithModifiers(input.ModifierCtrl).
			WithKey("k").
			WithCode("KeyK").
			WithWindowsVirtualKeyCode(75),
		input.DispatchKeyEvent(input.KeyUp).
			WithModifiers(input.ModifierCtrl).
			WithKey("k").
			WithCode("KeyK").
			WithWindowsVirtualKeyCode(75),
		chromedp.WaitVisible(paletteSelector, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Ctrl+K failed to open Command Palette dialog: %v", err)
	}
	if err := waitForDOMCondition(timeoutCtx, "Command Palette kernel commands loaded",
		`document.querySelectorAll('[role="dialog"][aria-label="Command Palette"] button').length >= 2`,
	); err != nil {
		t.Fatalf("Command Palette did not populate kernel commands: %v", err)
	}
	var paletteButtonCount int
	if err := chromedp.Run(timeoutCtx, chromedp.Evaluate(
		`document.querySelectorAll('[role="dialog"][aria-label="Command Palette"] button').length`,
		&paletteButtonCount,
	)); err != nil {
		t.Fatalf("count Command Palette buttons: %v", err)
	}
	t.Logf("verified Ctrl+K opened Command Palette dialog (rendered %d interactive buttons)", paletteButtonCount)

	// 4. Verify zero uncaught JS exceptions and zero console errors.
	diagMu.Lock()
	gotExceptions := append([]string(nil), uncaughtExceptions...)
	gotConsoleErrors := append([]string(nil), consoleErrors...)
	diagMu.Unlock()

	if len(gotExceptions) > 0 {
		t.Fatalf("unexpected uncaught browser JS exceptions (%d): %v", len(gotExceptions), gotExceptions)
	}
	if len(gotConsoleErrors) > 0 {
		t.Fatalf("unexpected browser console.error calls (%d): %v", len(gotConsoleErrors), gotConsoleErrors)
	}
}
