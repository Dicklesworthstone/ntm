package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestRouterSpecDeterminism verifies two independent generations are
// byte-identical — the property the openapi-drift CI job depends on.
func TestRouterSpecDeterminism(t *testing.T) {
	spec1, err := GenerateOpenAPISpecFromRouter("1.0.0", "http://localhost:8080")
	if err != nil {
		t.Fatalf("generate spec1: %v", err)
	}
	spec2, err := GenerateOpenAPISpecFromRouter("1.0.0", "http://localhost:8080")
	if err != nil {
		t.Fatalf("generate spec2: %v", err)
	}

	data1, err := json.MarshalIndent(spec1, "", "  ")
	if err != nil {
		t.Fatalf("marshal spec1: %v", err)
	}
	data2, err := json.MarshalIndent(spec2, "", "  ")
	if err != nil {
		t.Fatalf("marshal spec2: %v", err)
	}
	if !bytes.Equal(data1, data2) {
		t.Fatal("router-walk OpenAPI generation is not deterministic")
	}
}

// TestRouterSpecCoversEveryServedRoute asserts the generated operation count
// equals the chi.Walk route count of the same served router: the document is
// the router, not a subset. It also pins a floor so a silently empty router
// cannot pass.
//
// NOTE on the workstream's "~263 handlers" figure: that count came from
// grepping `.Get(`/`.Post(`-style call sites, which also matched
// `r.URL.Query().Get(...)` parameter reads. The served router registers 178
// routes (verified by chi.Walk); the honest assertion is exact equality with
// the walk plus a >150 floor.
func TestRouterSpecCoversEveryServedRoute(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	walkCount := 0
	err := chi.Walk(srv.Router(), func(method, route string, handler http.Handler, mws ...func(http.Handler) http.Handler) error {
		walkCount++
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	spec, err := buildRouterOpenAPISpec(srv.Router(), "test", "http://localhost:8080")
	if err != nil {
		t.Fatalf("generate spec: %v", err)
	}

	opCount := 0
	for _, item := range spec.Paths {
		for _, op := range []*Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				opCount++
			}
		}
	}

	if opCount != walkCount {
		t.Errorf("generated operation count %d != chi.Walk route count %d", opCount, walkCount)
	}
	if opCount <= 150 {
		t.Errorf("generated operation count %d suspiciously low (kernel registry had 13; served router has ~178)", opCount)
	}
	if len(spec.Paths) <= 13 {
		t.Errorf("path count %d looks like the retired kernel-registry generator (13 paths)", len(spec.Paths))
	}
}

// TestRouterSpecRobotSchemaJoin spot-checks the schema join for five known
// endpoints: the 200 response must reference the robot registry component,
// and the component must expose a known field of the real Go type.
func TestRouterSpecRobotSchemaJoin(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("test", "http://localhost:8080")
	if err != nil {
		t.Fatalf("generate spec: %v", err)
	}

	cases := []struct {
		method    string
		path      string
		component string
		field     string
	}{
		{"GET", "/api/v1/robot/status", "StatusOutput", "sessions"},
		{"GET", "/api/v1/robot/health", "HealthOutput", "checked_at"},
		{"GET", "/api/v1/robot/snapshot", "SnapshotOutput", "sessions"},
		{"GET", "/api/v1/capabilities", "CapabilitiesOutput", "commands"},
		{"POST", "/api/v1/sessions/{sessionId}/agents/spawn", "SpawnOutput", "working_dir"},
	}

	for _, tc := range cases {
		item, ok := spec.Paths[tc.path]
		if !ok {
			t.Errorf("%s %s: path missing from spec", tc.method, tc.path)
			continue
		}
		var op *Operation
		switch tc.method {
		case "GET":
			op = item.Get
		case "POST":
			op = item.Post
		}
		if op == nil {
			t.Errorf("%s %s: operation missing", tc.method, tc.path)
			continue
		}
		resp, ok := op.Responses["200"]
		if !ok {
			t.Errorf("%s %s: no 200 response", tc.method, tc.path)
			continue
		}
		schema := resp.Content["application/json"].Schema
		wantRef := "#/components/schemas/" + tc.component
		if schema == nil || schema.Ref != wantRef {
			got := "<nil>"
			if schema != nil {
				got = schema.Ref
			}
			t.Errorf("%s %s: 200 schema ref = %q, want %q", tc.method, tc.path, got, wantRef)
			continue
		}
		comp, ok := spec.Components.Schemas[tc.component]
		if !ok {
			t.Errorf("%s %s: component %s not registered", tc.method, tc.path, tc.component)
			continue
		}
		if _, ok := comp.Properties[tc.field]; !ok {
			t.Errorf("%s %s: component %s missing expected field %q", tc.method, tc.path, tc.component, tc.field)
		}
	}
}

// TestRouterSpecUnjoinedRoutesGetHonestGenerics verifies unmapped handlers
// reference the generic envelope, never a fabricated specific schema.
func TestRouterSpecUnjoinedRoutesGetHonestGenerics(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("test", "http://localhost:8080")
	if err != nil {
		t.Fatalf("generate spec: %v", err)
	}

	// /api/v1/version returns a hand-built map, NOT robot.VersionOutput, so it
	// must NOT join to the robot "version" schema despite the name similarity.
	item, ok := spec.Paths["/api/v1/version"]
	if !ok || item.Get == nil {
		t.Fatal("GET /api/v1/version missing from spec")
	}
	ref := item.Get.Responses["200"].Content["application/json"].Schema.Ref
	if ref != "#/components/schemas/SuccessEnvelope" {
		t.Errorf("GET /api/v1/version 200 ref = %q, want generic SuccessEnvelope (handler builds an ad hoc map)", ref)
	}

	// Legacy /api/robot/health serves a stub note map, not robot.HealthOutput,
	// so it must stay generic even though its V1 sibling joins.
	item, ok = spec.Paths["/api/robot/health"]
	if !ok || item.Get == nil {
		t.Fatal("GET /api/robot/health missing from spec")
	}
	ref = item.Get.Responses["200"].Content["application/json"].Schema.Ref
	if ref != "#/components/schemas/SuccessEnvelope" {
		t.Errorf("GET /api/robot/health 200 ref = %q, want generic SuccessEnvelope (legacy stub handler)", ref)
	}

	// /api/v1/robot/attention serves GetDigest output, so it must join to
	// DigestOutput, not AttentionOutput.
	item, ok = spec.Paths["/api/v1/robot/attention"]
	if !ok || item.Get == nil {
		t.Fatal("GET /api/v1/robot/attention missing from spec")
	}
	ref = item.Get.Responses["200"].Content["application/json"].Schema.Ref
	if ref != "#/components/schemas/DigestOutput" {
		t.Errorf("GET /api/v1/robot/attention 200 ref = %q, want DigestOutput (handler serves GetDigest)", ref)
	}

	// Same for /api/v1/health: {"status":"healthy"} envelope, not robot.HealthOutput.
	item, ok = spec.Paths["/api/v1/health"]
	if !ok || item.Get == nil {
		t.Fatal("GET /api/v1/health missing from spec")
	}
	ref = item.Get.Responses["200"].Content["application/json"].Schema.Ref
	if ref != "#/components/schemas/SuccessEnvelope" {
		t.Errorf("GET /api/v1/health 200 ref = %q, want generic SuccessEnvelope", ref)
	}
}

// TestRouterSpecAllRefsResolve verifies every $ref in the document points at
// a defined component, so validators and client generators never break.
func TestRouterSpecAllRefsResolve(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("test", "http://localhost:8080")
	if err != nil {
		t.Fatalf("generate spec: %v", err)
	}

	valid := make(map[string]bool)
	for name := range spec.Components.Schemas {
		valid["#/components/schemas/"+name] = true
	}

	var check func(where string, s *Schema)
	check = func(where string, s *Schema) {
		if s == nil {
			return
		}
		if s.Ref != "" && !valid[s.Ref] {
			t.Errorf("%s: unresolved $ref %s", where, s.Ref)
		}
		check(where+".items", s.Items)
		for k, v := range s.Properties {
			check(where+"."+k, v)
		}
		if ap, ok := s.AdditionalProperties.(*Schema); ok {
			check(where+".additionalProperties", ap)
		}
	}

	for name, comp := range spec.Components.Schemas {
		check("components."+name, comp)
	}
	for path, item := range spec.Paths {
		for _, op := range []*Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			for code, resp := range op.Responses {
				for _, media := range resp.Content {
					check(path+" "+code, media.Schema)
				}
			}
			if op.RequestBody != nil {
				for _, media := range op.RequestBody.Content {
					check(path+" requestBody", media.Schema)
				}
			}
		}
	}
}

// TestParityMatrixGeneratedFromRouter verifies the generated parity matrix
// (bd-ws4-openapi-parity-wpwck.2.2): deterministic byte output, count equal
// to the chi.Walk route count, sorted rows, no timestamps, and the verified
// robot-schema joins present.
func TestParityMatrixGeneratedFromRouter(t *testing.T) {
	m1, err := GenerateParityMatrixFromRouter("test")
	if err != nil {
		t.Fatalf("generate matrix: %v", err)
	}
	m2, err := GenerateParityMatrixFromRouter("test")
	if err != nil {
		t.Fatalf("generate matrix again: %v", err)
	}
	d1, err := json.MarshalIndent(m1, "", "  ")
	if err != nil {
		t.Fatalf("marshal m1: %v", err)
	}
	d2, err := json.MarshalIndent(m2, "", "  ")
	if err != nil {
		t.Fatalf("marshal m2: %v", err)
	}
	if !bytes.Equal(d1, d2) {
		t.Fatal("parity matrix generation is not deterministic")
	}
	if bytes.Contains(d1, []byte("generated_at")) {
		t.Fatal("parity matrix embeds a timestamp; output must be reproducible")
	}

	srv := NewHermeticServer("test")
	defer srv.Stop()
	walkCount := 0
	err = chi.Walk(srv.Router(), func(method, route string, handler http.Handler, mws ...func(http.Handler) http.Handler) error {
		walkCount++
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if m1.Count != walkCount || len(m1.Endpoints) != walkCount {
		t.Errorf("matrix count=%d endpoints=%d, want chi.Walk count %d", m1.Count, len(m1.Endpoints), walkCount)
	}
	if m1.Source != "chi-router-walk" {
		t.Errorf("matrix source=%q, want chi-router-walk", m1.Source)
	}

	for i := 1; i < len(m1.Endpoints); i++ {
		a, b := m1.Endpoints[i-1], m1.Endpoints[i]
		if a.Pattern > b.Pattern || (a.Pattern == b.Pattern && a.Method > b.Method) {
			t.Fatalf("endpoints not sorted at %d: %v %v then %v %v", i, a.Method, a.Pattern, b.Method, b.Pattern)
		}
	}

	joins := make(map[string]string)
	for _, ep := range m1.Endpoints {
		if ep.RobotSchema != "" {
			joins[ep.Method+" "+ep.Pattern] = ep.RobotSchema
		}
	}
	for route, want := range map[string]string{
		"GET /api/v1/robot/status":                       "status",
		"GET /api/v1/robot/attention":                    "digest",
		"POST /api/v1/sessions/{sessionId}/agents/spawn": "spawn",
	} {
		if got := joins[route]; got != want {
			t.Errorf("matrix join for %s = %q, want %q", route, got, want)
		}
	}
	if len(joins) < 15 {
		t.Errorf("matrix has %d robot-schema joins, want >=15 (verified join table)", len(joins))
	}
}

// TestRouterSpecOperationBasics verifies every operation carries an
// operationId, summary, path parameters, and a 200 response, and that
// operationIds are unique.
func TestRouterSpecOperationBasics(t *testing.T) {
	spec, err := GenerateOpenAPISpecFromRouter("test", "http://localhost:8080")
	if err != nil {
		t.Fatalf("generate spec: %v", err)
	}

	seen := make(map[string]string)
	for path, item := range spec.Paths {
		for method, op := range map[string]*Operation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put,
			"PATCH": item.Patch, "DELETE": item.Delete,
		} {
			if op == nil {
				continue
			}
			key := method + " " + path
			if op.OperationID == "" {
				t.Errorf("%s: missing operationId", key)
			}
			if prev, dup := seen[op.OperationID]; dup {
				t.Errorf("%s: operationId %q duplicates %s", key, op.OperationID, prev)
			}
			seen[op.OperationID] = key
			if op.Summary == "" {
				t.Errorf("%s: missing summary", key)
			}
			if _, ok := op.Responses["200"]; !ok {
				if _, accepted := op.Responses["202"]; !accepted {
					t.Errorf("%s: missing success (200/202) response", key)
				}
			}

			declared := make(map[string]bool)
			for _, p := range op.Parameters {
				if p.In == "path" {
					declared[p.Name] = true
				}
			}
			for _, part := range strings.Split(path, "/") {
				if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
					name := strings.Trim(part, "{}")
					if !declared[name] {
						t.Errorf("%s: path parameter %q not declared", key, name)
					}
				}
			}
		}
	}
}
