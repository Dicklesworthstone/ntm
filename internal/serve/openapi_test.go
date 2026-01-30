package serve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
)

func TestGenerateOpenAPISpec(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	if spec.OpenAPI != "3.1.0" {
		t.Errorf("OpenAPI version = %q, want %q", spec.OpenAPI, "3.1.0")
	}

	if spec.Info.Title != "NTM REST API" {
		t.Errorf("Info.Title = %q, want %q", spec.Info.Title, "NTM REST API")
	}

	if spec.Info.Version != "1.0.0" {
		t.Errorf("Info.Version = %q, want %q", spec.Info.Version, "1.0.0")
	}

	if len(spec.Servers) == 0 {
		t.Error("expected at least one server")
	} else if spec.Servers[0].URL != "http://localhost:8080" {
		t.Errorf("Server URL = %q, want %q", spec.Servers[0].URL, "http://localhost:8080")
	}

	if spec.Components == nil {
		t.Error("expected Components to be non-nil")
	}

	if spec.Components.Schemas == nil {
		t.Error("expected Schemas to be non-nil")
	}

	if _, ok := spec.Components.Schemas["SuccessResponse"]; !ok {
		t.Error("expected SuccessResponse schema")
	}

	if _, ok := spec.Components.Schemas["ErrorResponse"]; !ok {
		t.Error("expected ErrorResponse schema")
	}
}

func TestGenerateOpenAPISpecJSON(t *testing.T) {
	spec := GenerateOpenAPISpec("dev", "http://localhost:8080")

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("failed to marshal spec: %v", err)
	}

	if len(data) == 0 {
		t.Error("expected non-empty JSON")
	}

	// Verify it's valid JSON by unmarshalling
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal spec: %v", err)
	}

	if parsed["openapi"] != "3.1.0" {
		t.Errorf("parsed openapi = %v, want %q", parsed["openapi"], "3.1.0")
	}
}

func TestNormalizePathForOpenAPI(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/sessions", "/api/v1/sessions"},
		{"/sessions/{sessionId}", "/api/v1/sessions/{sessionId}"},
		{"/api/kernel/commands", "/api/kernel/commands"},
		{"/api/v1/health", "/api/v1/health"},
	}

	for _, tt := range tests {
		got := normalizePathForOpenAPI(tt.input)
		if got != tt.expected {
			t.Errorf("normalizePathForOpenAPI(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExtractPathParams(t *testing.T) {
	tests := []struct {
		path     string
		expected []string
	}{
		{"/sessions", nil},
		{"/sessions/{sessionId}", []string{"sessionId"}},
		{"/sessions/{sessionId}/panes/{paneIdx}", []string{"sessionId", "paneIdx"}},
	}

	for _, tt := range tests {
		params := extractPathParams(tt.path)
		if len(params) != len(tt.expected) {
			t.Errorf("extractPathParams(%q) returned %d params, want %d", tt.path, len(params), len(tt.expected))
			continue
		}
		for i, p := range params {
			if p.Name != tt.expected[i] {
				t.Errorf("extractPathParams(%q)[%d].Name = %q, want %q", tt.path, i, p.Name, tt.expected[i])
			}
			if p.In != "path" {
				t.Errorf("extractPathParams(%q)[%d].In = %q, want %q", tt.path, i, p.In, "path")
			}
			if !p.Required {
				t.Errorf("extractPathParams(%q)[%d].Required = false, want true", tt.path, i)
			}
		}
	}
}

func TestBuildDescription(t *testing.T) {
	tests := []struct {
		name        string
		description string
		safetyLevel string
		idempotent  bool
		emits       []string
		wantParts   []string
	}{
		{
			name:        "basic",
			description: "Test command",
			wantParts:   []string{"Test command"},
		},
		{
			name:        "with-safety",
			description: "Test command",
			safetyLevel: "safe",
			wantParts:   []string{"Test command", "Safety Level:", "safe"},
		},
		{
			name:        "with-idempotent",
			description: "Test command",
			idempotent:  true,
			wantParts:   []string{"Test command", "Idempotent:", "safe to retry"},
		},
		{
			name:        "with-events",
			description: "Test command",
			emits:       []string{"agent.started", "agent.stopped"},
			wantParts:   []string{"Test command", "Emits Events:", "agent.started", "agent.stopped"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := kernel.Command{
				Name:        "test.command",
				Description: tt.description,
				SafetyLevel: kernel.SafetyLevel(tt.safetyLevel),
				Idempotent:  tt.idempotent,
				EmitsEvents: tt.emits,
			}
			desc := buildDescription(cmd)
			for _, part := range tt.wantParts {
				if !strings.Contains(desc, part) {
					t.Errorf("description missing %q in %q", part, desc)
				}
			}
		})
	}
}

func TestBuildResponseSchema(t *testing.T) {
	tests := []struct {
		name      string
		command   kernel.Command
		expectRef string
	}{
		{
			name: "default-success",
			command: kernel.Command{
				Name: "robot.status",
			},
			expectRef: "#/components/schemas/SuccessResponse",
		},
		{
			name: "output-ref-with-name",
			command: kernel.Command{
				Name:   "robot.status",
				Output: &kernel.SchemaRef{Name: "RobotStatus", Ref: "#/schemas/RobotStatus"},
			},
			expectRef: "#/components/schemas/RobotStatus",
		},
		{
			name: "output-ref-without-name",
			command: kernel.Command{
				Name:   "robot.status",
				Output: &kernel.SchemaRef{Ref: "#/schemas/RobotStatus"},
			},
			expectRef: "#/components/schemas/robot_status_Response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := buildResponseSchema(tt.command)
			if schema == nil {
				t.Fatalf("expected schema, got nil")
			}
			if schema.Ref != tt.expectRef {
				t.Errorf("schema.Ref = %q, want %q", schema.Ref, tt.expectRef)
			}
		})
	}
}

func TestBuildInputSchema(t *testing.T) {
	tests := []struct {
		name      string
		command   kernel.Command
		expectRef string
		wantType  string
	}{
		{
			name: "default-object",
			command: kernel.Command{
				Name: "robot.status",
			},
			wantType: "object",
		},
		{
			name: "input-ref-with-name",
			command: kernel.Command{
				Name:  "robot.status",
				Input: &kernel.SchemaRef{Name: "RobotStatusRequest", Ref: "#/schemas/RobotStatusRequest"},
			},
			expectRef: "#/components/schemas/RobotStatusRequest",
		},
		{
			name: "input-ref-without-name",
			command: kernel.Command{
				Name:  "robot.status",
				Input: &kernel.SchemaRef{Ref: "#/schemas/RobotStatusRequest"},
			},
			expectRef: "#/components/schemas/robot_status_Request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := buildInputSchema(tt.command)
			if schema == nil {
				t.Fatalf("expected schema, got nil")
			}
			if tt.expectRef != "" {
				if schema.Ref != tt.expectRef {
					t.Errorf("schema.Ref = %q, want %q", schema.Ref, tt.expectRef)
				}
				return
			}
			if schema.Type != tt.wantType {
				t.Errorf("schema.Type = %q, want %q", schema.Type, tt.wantType)
			}
			if schema.AdditionalProperties != true {
				t.Errorf("schema.AdditionalProperties = %v, want true", schema.AdditionalProperties)
			}
		})
	}
}

func TestOpenAPISpecHasRequiredFields(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://test:8080")

	// Must have openapi field
	if spec.OpenAPI == "" {
		t.Error("OpenAPI field is required")
	}

	// Must have info
	if spec.Info.Title == "" {
		t.Error("Info.Title is required")
	}
	if spec.Info.Version == "" {
		t.Error("Info.Version is required")
	}

	// Must have paths
	if spec.Paths == nil {
		t.Error("Paths is required")
	}

	// Tags should be sorted
	for i := 1; i < len(spec.Tags); i++ {
		if spec.Tags[i-1].Name > spec.Tags[i].Name {
			t.Errorf("Tags not sorted: %s > %s", spec.Tags[i-1].Name, spec.Tags[i].Name)
		}
	}

	// Verify all operations have responses
	for path, item := range spec.Paths {
		ops := []*Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete}
		for _, op := range ops {
			if op == nil {
				continue
			}
			if len(op.Responses) == 0 {
				t.Errorf("Operation at %s has no responses", path)
			}
			if _, ok := op.Responses["200"]; !ok {
				t.Errorf("Operation at %s missing 200 response", path)
			}
		}
	}
}

func TestSecuritySchemes(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	if spec.Components == nil || spec.Components.SecuritySchemes == nil {
		t.Fatal("expected SecuritySchemes to be defined")
	}

	bearer, ok := spec.Components.SecuritySchemes["bearerAuth"]
	if !ok {
		t.Error("expected bearerAuth security scheme")
	} else {
		if bearer.Type != "http" {
			t.Errorf("bearerAuth.Type = %q, want %q", bearer.Type, "http")
		}
		if bearer.Scheme != "bearer" {
			t.Errorf("bearerAuth.Scheme = %q, want %q", bearer.Scheme, "bearer")
		}
	}
}

func TestPathItemMethodsAreExclusive(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for path, item := range spec.Paths {
		// Count non-nil operations
		count := 0
		if item.Get != nil {
			count++
		}
		if item.Post != nil {
			count++
		}
		if item.Put != nil {
			count++
		}
		if item.Patch != nil {
			count++
		}
		if item.Delete != nil {
			count++
		}

		if count == 0 {
			t.Errorf("Path %s has no operations", path)
		}
	}
}

func TestOperationIDsAreUnique(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	ids := make(map[string]string)
	for path, item := range spec.Paths {
		ops := map[string]*Operation{
			"GET":    item.Get,
			"POST":   item.Post,
			"PUT":    item.Put,
			"PATCH":  item.Patch,
			"DELETE": item.Delete,
		}
		for method, op := range ops {
			if op == nil {
				continue
			}
			if op.OperationID == "" {
				t.Errorf("%s %s has no operationId", method, path)
				continue
			}
			if existing, ok := ids[op.OperationID]; ok {
				t.Errorf("duplicate operationId %q: %s and %s %s", op.OperationID, existing, method, path)
			}
			ids[op.OperationID] = method + " " + path
		}
	}
}

func TestOperationIDFormat(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for path, item := range spec.Paths {
		ops := []*Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete}
		for _, op := range ops {
			if op == nil {
				continue
			}
			// Operation IDs should not contain dots (converted from command names)
			if strings.Contains(op.OperationID, ".") {
				t.Errorf("operationId %q at %s contains dots", op.OperationID, path)
			}
		}
	}
}
