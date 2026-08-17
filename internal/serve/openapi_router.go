// openapi_router.go generates the OpenAPI document from the SERVED chi router.
//
// This is the single-source-of-truth generator decided by the WS4 OpenAPI
// workstream (bd-ws4-openapi-parity-wpwck): instead of describing routes in a
// hand-maintained registry or a TypeScript script, the generator instantiates
// the same Server that `ntm serve` runs (hermetically — the listener is never
// bound), chi.Walk's its router, and emits one operation per registered
// route+method. Response shapes are joined from the robot schema registry
// (internal/robot/schema.go) where a handler verifiably serializes a robot
// output type; every other handler gets an honest generic envelope schema
// rather than an invented one.
package serve

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// RouterRoute is one route observed by walking the served chi router.
type RouterRoute struct {
	Method  string `json:"method"`
	Pattern string `json:"path"`
	// Handler is the short Go method name of the endpoint handler
	// (for example "handleRobotStatusV1"), or "" for anonymous closures.
	Handler string `json:"handler,omitempty"`
	// RobotSchema is the robot schema registry key joined to this route's
	// response shape, or "" when the response is not a registered robot type.
	RobotSchema string `json:"robot_schema,omitempty"`
}

// NewHermeticServer constructs the same Server `ntm serve` runs without
// binding a listener or starting any background loops. New() is documented
// side-effect-free, so the constructed router is exactly the served route
// table. Callers should Stop() the returned server when done.
func NewHermeticServer(version string) *Server {
	return New(Config{
		Port:     0,
		Version:  version,
		EventBus: events.NewEventBus(16),
	})
}

// WalkServedRoutes chi.Walk's the router and returns the served route table,
// sorted by (pattern, method) for deterministic output.
func WalkServedRoutes(router chi.Router) ([]RouterRoute, error) {
	var routes []RouterRoute
	err := chi.Walk(router, func(method, pattern string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		name := endpointHandlerName(handler)
		schemaType, _ := robotSchemaForHandler(name)
		routes = append(routes, RouterRoute{
			Method:      strings.ToUpper(method),
			Pattern:     pattern,
			Handler:     name,
			RobotSchema: schemaType,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk served router: %w", err)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern != routes[j].Pattern {
			return routes[i].Pattern < routes[j].Pattern
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

// endpointHandlerName resolves the short Go name of a route's endpoint
// handler. chi.Walk already unwraps ChainHandler middleware wrappers, so the
// handler here is the endpoint itself. Method-value handlers resolve to names
// like ".../serve.(*Server).handleRobotStatusV1-fm"; anonymous route closures
// resolve to ".../serve.(*Server).buildRouter.funcN" and are reported as "".
func endpointHandlerName(handler http.Handler) string {
	if ch, ok := handler.(*chi.ChainHandler); ok {
		handler = ch.Endpoint
	}
	v := reflect.ValueOf(handler)
	if v.Kind() != reflect.Func {
		return ""
	}
	fn := runtime.FuncForPC(v.Pointer())
	if fn == nil {
		return ""
	}
	name := fn.Name()
	name = strings.TrimSuffix(name, "-fm")
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	if !strings.HasPrefix(name, "handle") {
		return ""
	}
	return name
}

// verifiedRobotHandlers is the join table between served handlers and robot
// schema registry keys (the join heuristic decided under
// bd-ws4-openapi-parity-wpwck.2.1). Every entry was verified by reading the
// handler body: the handler serializes exactly that robot output type
// (flattened at the top level via toJSONMap/writeSuccessResponse, plus the
// envelope's request_id/success/timestamp fields).
//
// The table is deliberately explicit rather than name-derived, because names
// lie: handleRobotAttentionV1 actually serves robot.DigestOutput, and the
// legacy handleRobotHealth serves a stub note map, not robot.HealthOutput.
// Unlisted handlers get the honest generic envelope schema — honest generic
// beats invented specific. Add entries only after reading the handler body.
var verifiedRobotHandlers = map[string]string{
	// Robot read surface (thin adapters in server.go).
	"handleRobotStatusV1":    "status",
	"handleRobotHealthV1":    "health",
	"handleRobotSnapshotV1":  "snapshot",
	"handleRobotDigestV1":    "digest",
	"handleRobotAttentionV1": "digest", // serves GetDigest with an attention profile, NOT AttentionOutput
	"handleRobotDashboardV1": "dashboard",
	"handleRobotTerseV1":     "terse",
	"handleRobotTriageV1":    "triage",
	"handleRobotPlanV1":      "plan",
	"handleRobotGraphV1":     "graph",
	"handleRobotActivityV1":  "activity",
	"handleRobotAlertsV1":    "alerts",
	// Legacy /api/robot/status serves robot.StatusOutput directly.
	// (Legacy /api/robot/health serves a stub map and is intentionally absent.)
	"handleRobotStatus": "status",
	// System and agent-lifecycle handlers verified to serialize robot outputs.
	"handleCapabilitiesV1":   "capabilities",
	"handleAgentSpawnV1":     "spawn",
	"handleAgentSendV1":      "send",
	"handleAgentInterruptV1": "interrupt",
	"handleAgentWaitV1":      "wait",
}

// robotSchemaForHandler joins a handler name to a robot schema registry key
// via the verified join table.
func robotSchemaForHandler(handlerName string) (string, bool) {
	if handlerName == "" {
		return "", false
	}
	key, ok := verifiedRobotHandlers[handlerName]
	if !ok {
		return "", false
	}
	if _, bound := robot.GetRobotRegistry().SchemaBinding(key); bound {
		return key, true
	}
	return "", false
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParityMatrix is the generated CLI-vs-REST parity inventory: one row per
// served route, with the robot-schema join where verified. It replaces the
// hand-maintained docs/parity_matrix.json (bd-ws4-openapi-parity-wpwck.2.2)
// and is diff-gated in CI the same way as the OpenAPI document. No timestamps:
// output must be byte-reproducible.
type ParityMatrix struct {
	Source    string        `json:"source"`
	Count     int           `json:"count"`
	Endpoints []RouterRoute `json:"endpoints"`
}

// GenerateParityMatrixFromRouter builds the parity matrix by walking the same
// hermetically constructed served router the OpenAPI generator walks.
func GenerateParityMatrixFromRouter(version string) (*ParityMatrix, error) {
	srv := NewHermeticServer(version)
	defer srv.Stop()
	routes, err := WalkServedRoutes(srv.Router())
	if err != nil {
		return nil, err
	}
	return &ParityMatrix{
		Source:    "chi-router-walk",
		Count:     len(routes),
		Endpoints: routes,
	}, nil
}

// GenerateOpenAPISpecFromRouter builds the OpenAPI 3.1 document for the
// served router by instantiating the serve mux hermetically and walking it.
// Output is deterministic: routes are sorted, component maps marshal in key
// order, and no timestamps are embedded.
func GenerateOpenAPISpecFromRouter(version, serverURL string) (*OpenAPISpec, error) {
	srv := NewHermeticServer(version)
	defer srv.Stop()
	return buildRouterOpenAPISpec(srv.Router(), version, serverURL)
}

func buildRouterOpenAPISpec(router chi.Router, version, serverURL string) (*OpenAPISpec, error) {
	routes, err := WalkServedRoutes(router)
	if err != nil {
		return nil, err
	}

	spec := &OpenAPISpec{
		OpenAPI: "3.1.0",
		Info: OpenAPIInfo{
			Title:       "NTM REST API",
			Version:     version,
			Description: "REST API for NTM (Named Tmux Manager). Generated by walking the served chi router — the same mux `ntm serve` mounts — joined with the robot schema registry for response shapes.",
		},
		Servers: []OpenAPIServer{
			{URL: serverURL, Description: "NTM server"},
		},
		Paths: make(map[string]PathItem),
		Components: &OpenAPIComponents{
			Schemas: map[string]*Schema{
				"SuccessEnvelope": {
					Type:        "object",
					Description: "Generic success envelope. The handler's payload fields are merged at the top level alongside these envelope fields; this endpoint's payload shape is not registered in the robot schema registry, so only the envelope is documented.",
					Properties: map[string]*Schema{
						"success":    {Type: "boolean"},
						"timestamp":  {Type: "string", Format: "date-time"},
						"request_id": {Type: "string"},
					},
					Required:             []string{"success"},
					AdditionalProperties: true,
				},
				"ErrorResponse": {
					Type: "object",
					Properties: map[string]*Schema{
						"success":    {Type: "boolean"},
						"error":      {Type: "string"},
						"error_code": {Type: "string"},
						"timestamp":  {Type: "string", Format: "date-time"},
						"request_id": {Type: "string"},
						"details":    {Type: "object", AdditionalProperties: true},
						"hint":       {Type: "string"},
					},
					Required: []string{"success", "error", "error_code", "timestamp"},
				},
			},
			SecuritySchemes: map[string]*SecurityScheme{
				"bearerAuth": {
					Type:         "http",
					Scheme:       "bearer",
					BearerFormat: "JWT",
					Description:  "Bearer token authentication",
				},
				"apiKey": {
					Type:        "apiKey",
					Name:        "X-API-Key",
					In:          "header",
					Description: "API key authentication via X-API-Key header",
				},
			},
		},
	}

	tagSet := make(map[string]bool)
	for _, rt := range routes {
		op := buildRouterOperation(rt, spec.Components)
		if op.Tags != nil {
			for _, t := range op.Tags {
				tagSet[t] = true
			}
		}

		item := spec.Paths[rt.Pattern]
		switch rt.Method {
		case "GET":
			item.Get = op
		case "POST":
			item.Post = op
		case "PUT":
			item.Put = op
		case "PATCH":
			item.Patch = op
		case "DELETE":
			item.Delete = op
		default:
			// HEAD/OPTIONS/etc. are not part of the documented surface.
			continue
		}
		spec.Paths[rt.Pattern] = item
	}

	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		spec.Tags = append(spec.Tags, OpenAPITag{
			Name:        tag,
			Description: fmt.Sprintf("Operations related to %s", tag),
		})
	}

	return spec, nil
}

// buildRouterOperation builds the OpenAPI operation for one walked route.
func buildRouterOperation(rt RouterRoute, components *OpenAPIComponents) *Operation {
	responseRef := "#/components/schemas/SuccessEnvelope"
	joined := false
	if rt.RobotSchema != "" {
		if name, ok := registerRobotComponent(components, rt.RobotSchema); ok {
			responseRef = "#/components/schemas/" + name
			joined = true
		}
	}

	summary := routeSummary(rt)
	description := fmt.Sprintf("Served route `%s %s`.", rt.Method, rt.Pattern)
	if rt.Handler != "" {
		description += fmt.Sprintf(" Handler: `%s`.", rt.Handler)
	}
	if joined {
		description += fmt.Sprintf(" Response shape joined from robot schema registry type `%s`; the envelope fields success/timestamp/request_id are merged at the top level.", rt.RobotSchema)
	} else {
		description += " Response payload is not registered in the robot schema registry; the generic success envelope documents only the envelope fields."
	}

	op := &Operation{
		Tags:        []string{routeTag(rt.Pattern)},
		Summary:     summary,
		Description: description,
		OperationID: routeOperationID(rt),
		Parameters:  extractPathParams(rt.Pattern),
		Responses: map[string]Response{
			"200": {
				Description: "Successful operation",
				Content: map[string]MediaType{
					"application/json": {Schema: &Schema{Ref: responseRef}},
				},
			},
			"400": errorResponse("Bad request"),
			"401": errorResponse("Unauthorized"),
			"500": errorResponse("Internal server error"),
		},
	}

	if rt.Method == "POST" || rt.Method == "PUT" || rt.Method == "PATCH" {
		op.RequestBody = &RequestBody{
			Content: map[string]MediaType{
				"application/json": {
					Schema: &Schema{Type: "object", AdditionalProperties: true},
				},
			},
		}
	}
	return op
}

func errorResponse(description string) Response {
	return Response{
		Description: description,
		Content: map[string]MediaType{
			"application/json": {
				Schema: &Schema{Ref: "#/components/schemas/ErrorResponse"},
			},
		},
	}
}

// routeOperationID derives a stable operationId from method+pattern (handler
// names are not used: anonymous closures have none, and operationIds must be
// unique and deterministic).
func routeOperationID(rt RouterRoute) string {
	p := rt.Pattern
	// Distinguish trailing-slash patterns ("/docs" vs "/docs/") so ids stay unique.
	if strings.HasSuffix(p, "/") && p != "/" {
		p += "index"
	}
	p = strings.Trim(p, "/")
	p = strings.NewReplacer("{", "", "}", "", "/", "_", "-", "_", ".", "_", "*", "wildcard").Replace(p)
	if p == "" {
		p = "root"
	}
	return strings.ToLower(rt.Method) + "_" + p
}

// routeSummary derives a human-readable summary from the handler name when
// present, else from the route itself.
func routeSummary(rt RouterRoute) string {
	if rt.Handler != "" {
		name := strings.TrimPrefix(rt.Handler, "handle")
		name = strings.TrimSuffix(name, "V1")
		words := camelToSnake(name)
		words = strings.ReplaceAll(words, "_", " ")
		if words != "" {
			return strings.ToUpper(words[:1]) + words[1:]
		}
	}
	return fmt.Sprintf("%s %s", rt.Method, rt.Pattern)
}

// routeTag buckets a route by its first meaningful path segment.
func routeTag(pattern string) string {
	p := strings.TrimPrefix(pattern, "/api/v1")
	legacy := false
	if rest, ok := strings.CutPrefix(p, "/api"); ok && rest != p {
		p = rest
		legacy = true
	}
	seg := strings.Trim(p, "/")
	if idx := strings.Index(seg, "/"); idx >= 0 {
		seg = seg[:idx]
	}
	seg = strings.TrimSuffix(seg, ".json")
	if seg == "" || strings.HasPrefix(seg, "{") {
		seg = "system"
	}
	if legacy {
		return "legacy-" + seg
	}
	return seg
}

// registerRobotComponent converts the robot registry's JSON Schema for the
// given schema type into OpenAPI component schemas (rewriting #/definitions/
// refs to #/components/schemas/) and returns the top-level component name.
func registerRobotComponent(components *OpenAPIComponents, schemaType string) (string, bool) {
	binding, ok := robot.GetRobotRegistry().SchemaBinding(schemaType)
	if !ok {
		return "", false
	}
	t := reflect.TypeOf(binding)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := t.Name()
	if name == "" {
		return "", false
	}

	out, err := robot.GetSchema(schemaType)
	if err != nil || out == nil || out.Schema == nil {
		return "", false
	}
	js := out.Schema

	if components.Schemas == nil {
		components.Schemas = make(map[string]*Schema)
	}
	if _, exists := components.Schemas[name]; !exists {
		top := convertRobotSchema(&robot.JSONSchema{
			Description: js.Description,
			Type:        js.Type,
			Required:    js.Required,
			Properties:  js.Properties,
		})
		if top.Description == "" {
			top.Description = fmt.Sprintf("Robot schema registry type %s (robot schema key %q).", name, schemaType)
		}
		components.Schemas[name] = top
	}

	// Lift nested definitions into components in sorted (deterministic) order.
	defNames := make([]string, 0, len(js.Definitions))
	for defName := range js.Definitions {
		defNames = append(defNames, defName)
	}
	sort.Strings(defNames)
	for _, defName := range defNames {
		if _, exists := components.Schemas[defName]; exists {
			continue
		}
		components.Schemas[defName] = convertRobotSchema(js.Definitions[defName])
	}

	return name, true
}

// convertRobotSchema converts a robot JSONSchema node to an OpenAPI schema
// node, rewriting internal definition refs to component refs.
func convertRobotSchema(js *robot.JSONSchema) *Schema {
	if js == nil {
		return nil
	}
	s := &Schema{
		Type:        js.Type,
		Format:      js.Format,
		Description: js.Description,
		Required:    js.Required,
	}
	if js.Ref != "" {
		s.Ref = strings.Replace(js.Ref, "#/definitions/", "#/components/schemas/", 1)
	}
	if js.Items != nil {
		s.Items = convertRobotSchema(js.Items)
	}
	if len(js.Properties) > 0 {
		s.Properties = make(map[string]*Schema, len(js.Properties))
		for k, v := range js.Properties {
			s.Properties[k] = convertRobotSchema(v)
		}
	}
	if js.AdditionalProperties != nil {
		conv := convertRobotSchema(js.AdditionalProperties)
		// An empty schema means "any value"; encode as boolean true for brevity.
		if conv != nil && conv.Type == "" && conv.Ref == "" && len(conv.Properties) == 0 && conv.Items == nil {
			s.AdditionalProperties = true
		} else {
			s.AdditionalProperties = conv
		}
	}
	return s
}
