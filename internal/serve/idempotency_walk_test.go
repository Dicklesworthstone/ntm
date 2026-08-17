package serve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D4 (bd-ws3-contract-breadth-psvyu.4): the Idempotency-Key middleware is
// mounted ONCE at router level, so every mutating route supports replay by
// construction. This test proves it exhaustively over the SERVED router:
// chi.Walk enumerates the live route table, and every non-GET route must
// either be replay-verified here or appear in the committed opt-out golden
// list — nothing falls through unclassified, and stale golden entries fail.

const idempotencyGoldenPath = "testdata/idempotency_optout_golden.txt"

type idempotencyOptOut struct {
	Method        string
	Pattern       string
	Justification string
}

func loadIdempotencyGolden(t *testing.T) []idempotencyOptOut {
	t.Helper()
	f, err := os.Open(filepath.Clean(idempotencyGoldenPath))
	if err != nil {
		t.Fatalf("open idempotency opt-out golden list: %v", err)
	}
	defer f.Close()

	var entries []idempotencyOptOut
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) != 3 {
			t.Fatalf("%s:%d: malformed entry (want METHOD\\tpattern\\tjustification): %q", idempotencyGoldenPath, line, sc.Text())
		}
		if strings.TrimSpace(parts[2]) == "" {
			t.Fatalf("%s:%d: opt-out entry has no justification", idempotencyGoldenPath, line)
		}
		entries = append(entries, idempotencyOptOut{
			Method:        strings.ToUpper(strings.TrimSpace(parts[0])),
			Pattern:       strings.TrimSpace(parts[1]),
			Justification: strings.TrimSpace(parts[2]),
		})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read golden list: %v", err)
	}
	return entries
}

// concretePathForPattern turns a chi route pattern into a concrete request
// path by substituting URL parameters with dummy segments. Replay happens in
// middleware before any handler-side parameter validation runs, so the values
// only need to route.
func concretePathForPattern(pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, seg := range segs {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := strings.Trim(seg, "{}")
			if idx := strings.Index(name, ":"); idx >= 0 {
				name = name[:idx]
			}
			if strings.Contains(strings.ToLower(name), "idx") {
				segs[i] = "0"
			} else {
				segs[i] = "walkseg1"
			}
		} else if seg == "*" {
			segs[i] = "walkwild"
		}
	}
	return strings.Join(segs, "/")
}

// TestIdempotencyRouterCoverage replay-verifies every mutating route on the
// served router. For each route it seeds the idempotency store with a canned
// response under the exact scoped key the middleware will compute, then issues
// the request with that Idempotency-Key and asserts the canned response comes
// back with the replay marker — proving the middleware intercepted the route
// (the real handler can not produce the canned body). New routes are covered
// by construction; opting out requires a visible golden-list diff.
func TestIdempotencyRouterCoverage(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	routes, err := WalkServedRoutes(srv.Router())
	if err != nil {
		t.Fatalf("WalkServedRoutes: %v", err)
	}

	golden := loadIdempotencyGolden(t)
	goldenSet := make(map[string]idempotencyOptOut, len(golden))
	for _, e := range golden {
		goldenSet[e.Method+" "+e.Pattern] = e
	}

	served := make(map[string]bool)
	mutatingCount := 0
	verifiedCount := 0
	var failures []string

	for i, rt := range routes {
		served[rt.Method+" "+rt.Pattern] = true
		if !isMutatingMethod(rt.Method) {
			continue
		}
		mutatingCount++
		if _, optedOut := goldenSet[rt.Method+" "+rt.Pattern]; optedOut {
			continue
		}

		path := concretePathForPattern(rt.Pattern)
		key := fmt.Sprintf("walk-replay-%d", i)
		canned := []byte(fmt.Sprintf(`{"replay_probe":%d}`, i))

		// Compute the scoped key exactly as the middleware will see it: the
		// hermetic server runs auth-mode local, so rbacMiddleware attaches the
		// role/user derived from empty claims (RoleAdmin + the anonymous user
		// fallback). Derive both through the production helpers rather than
		// hardcoding so key-scoping changes keep this test honest.
		probe := httptest.NewRequest(rt.Method, path, nil)
		probe.Header.Set("Idempotency-Key", key)
		probe = probe.WithContext(withRoleContext(probe.Context(), &RoleContext{
			Role:   srv.extractRoleFromClaims(map[string]interface{}{}),
			UserID: extractUserIDFromClaims(map[string]interface{}{}),
		}))
		scoped := scopedIdempotencyKey(probe)
		if scoped == "" {
			t.Fatalf("scoped key empty for %s %s", rt.Method, rt.Pattern)
		}
		srv.idempotencyStore.Set(scoped, canned, http.StatusOK, http.Header{"Content-Type": {"application/json"}})

		req := httptest.NewRequest(rt.Method, path, nil)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)

		if rec.Header().Get("X-Idempotent-Replay") != "true" || !bytes.Equal(rec.Body.Bytes(), canned) {
			failures = append(failures, fmt.Sprintf("%s %s (status=%d replay_header=%q)",
				rt.Method, rt.Pattern, rec.Code, rec.Header().Get("X-Idempotent-Replay")))
			continue
		}
		verifiedCount++
	}

	if len(failures) > 0 {
		t.Errorf("%d mutating route(s) did not serve an idempotent replay and are not in %s:\n  %s",
			len(failures), idempotencyGoldenPath, strings.Join(failures, "\n  "))
	}

	// Both-direction ratchet: a golden entry that no longer resolves to a
	// served mutating route is stale and must be removed.
	for key, e := range goldenSet {
		if !served[key] {
			t.Errorf("stale opt-out golden entry (route no longer served): %s %s (%s)", e.Method, e.Pattern, e.Justification)
		}
		if !isMutatingMethod(e.Method) {
			t.Errorf("opt-out golden entry for non-mutating method: %s %s", e.Method, e.Pattern)
		}
	}

	if mutatingCount == 0 {
		t.Fatal("walk found zero mutating routes; the coverage test is vacuous")
	}
	t.Logf("idempotency replay coverage: %d/%d mutating routes replay-verified, %d opted out via golden list",
		verifiedCount, mutatingCount, len(golden))
}

// TestIdempotencyMiddlewareInertWithoutHeader is the D4 companion assertion:
// a mutating route called twice WITHOUT an Idempotency-Key header executes
// twice — the router-level mount must be a zero-behavior change for clients
// that never send the header.
func TestIdempotencyMiddlewareInertWithoutHeader(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	createJob := func() string {
		t.Helper()
		body := bytes.NewBufferString(`{"type":"pipeline_run","params":{"workflow_file":"missing.yaml","session":"walkjob1"}}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /api/v1/jobs = %d, want 202 (body=%s)", rec.Code, rec.Body.String())
		}
		var envelope struct {
			Job struct {
				ID string `json:"id"`
			} `json:"job"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode job envelope: %v (body=%s)", err, rec.Body.String())
		}
		if envelope.Job.ID == "" {
			t.Fatalf("job id missing in response: %s", rec.Body.String())
		}
		return envelope.Job.ID
	}

	first := createJob()
	second := createJob()
	if first == second {
		t.Fatalf("two no-header POSTs produced the same job (%s): request was deduplicated without an Idempotency-Key", first)
	}
}

// TestIdempotencyOrganicReplayThroughRouter exercises the organic replay path
// end to end (no store seeding): the same POST with the same key executes
// once, and the second response is a byte-identical replay.
func TestIdempotencyOrganicReplayThroughRouter(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	do := func() *httptest.ResponseRecorder {
		body := bytes.NewBufferString(`{"type":"pipeline_run","params":{"workflow_file":"missing.yaml","session":"walkjob1"}}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "organic-replay-1")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	first := do()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first POST = %d, want 202 (body=%s)", first.Code, first.Body.String())
	}
	second := do()
	if second.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("second POST with same key was not a replay (status=%d)", second.Code)
	}
	if second.Code != first.Code || !bytes.Equal(second.Body.Bytes(), first.Body.Bytes()) {
		t.Fatalf("replay differs from original: status %d vs %d, body %q vs %q",
			second.Code, first.Code, second.Body.String(), first.Body.String())
	}
}
