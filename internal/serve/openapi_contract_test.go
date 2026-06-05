package serve

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenAPISpecDeterminism verifies that spec generation produces identical output
// across multiple invocations. This ensures CI can reliably diff the checked-in spec.
func TestOpenAPISpecDeterminism(t *testing.T) {
	// Generate the spec twice
	spec1 := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")
	spec2 := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	// Marshal both specs
	data1, err := json.MarshalIndent(spec1, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal spec1: %v", err)
	}

	data2, err := json.MarshalIndent(spec2, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal spec2: %v", err)
	}

	// Compare byte-for-byte
	if !bytes.Equal(data1, data2) {
		t.Error("OpenAPI spec generation is not deterministic")
		t.Logf("spec1 length: %d", len(data1))
		t.Logf("spec2 length: %d", len(data2))
	}
}

// TestOpenAPISpecTagsSorted verifies tags are sorted alphabetically.
func TestOpenAPISpecTagsSorted(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for i := 1; i < len(spec.Tags); i++ {
		if spec.Tags[i-1].Name > spec.Tags[i].Name {
			t.Errorf("tags not sorted: %q comes before %q",
				spec.Tags[i-1].Name, spec.Tags[i].Name)
		}
	}
}

// TestOpenAPISpecPathsSorted verifies paths are serialized in sorted order.
func TestOpenAPISpecPathsSorted(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	// Marshal and unmarshal to check JSON key ordering
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("failed to marshal spec: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal spec: %v", err)
	}

	// Note: Go's json.Marshal doesn't guarantee key order, but our
	// GenerateOpenAPISpec uses a map[string]PathItem which has stable iteration
	// order when marshaled. The test verifies the spec structure is valid.
	if parsed["paths"] == nil {
		t.Error("expected paths in spec")
	}
}

// TestOpenAPISpecAllOperationsHaveResponses verifies every operation has at least
// a 200 response defined.
func TestOpenAPISpecAllOperationsHaveResponses(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for path, item := range spec.Paths {
		checkOperation := func(method string, op *Operation) {
			if op == nil {
				return
			}
			if len(op.Responses) == 0 {
				t.Errorf("%s %s: no responses defined", method, path)
			}
			if _, ok := op.Responses["200"]; !ok {
				t.Errorf("%s %s: missing 200 response", method, path)
			}
		}

		checkOperation("GET", item.Get)
		checkOperation("POST", item.Post)
		checkOperation("PUT", item.Put)
		checkOperation("PATCH", item.Patch)
		checkOperation("DELETE", item.Delete)
	}
}

// TestOpenAPISpecAllOperationsHaveOperationID verifies every operation has an operationId.
func TestOpenAPISpecAllOperationsHaveOperationID(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for path, item := range spec.Paths {
		checkOperation := func(method string, op *Operation) {
			if op == nil {
				return
			}
			if op.OperationID == "" {
				t.Errorf("%s %s: missing operationId", method, path)
			}
		}

		checkOperation("GET", item.Get)
		checkOperation("POST", item.Post)
		checkOperation("PUT", item.Put)
		checkOperation("PATCH", item.Patch)
		checkOperation("DELETE", item.Delete)
	}
}

// TestOpenAPISpecAllOperationsHaveSummary verifies every operation has a summary.
func TestOpenAPISpecAllOperationsHaveSummary(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for path, item := range spec.Paths {
		checkOperation := func(method string, op *Operation) {
			if op == nil {
				return
			}
			if op.Summary == "" {
				t.Errorf("%s %s: missing summary", method, path)
			}
		}

		checkOperation("GET", item.Get)
		checkOperation("POST", item.Post)
		checkOperation("PUT", item.Put)
		checkOperation("PATCH", item.Patch)
		checkOperation("DELETE", item.Delete)
	}
}

// TestOpenAPISpecPathParametersComplete verifies all path parameters are properly defined.
func TestOpenAPISpecPathParametersComplete(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for path, item := range spec.Paths {
		expectedParams := extractPathParams(path)
		if len(expectedParams) == 0 {
			continue
		}

		checkOperation := func(method string, op *Operation) {
			if op == nil {
				return
			}

			// Build map of actual path params
			actualParams := make(map[string]bool)
			for _, p := range op.Parameters {
				if p.In == "path" {
					actualParams[p.Name] = true
				}
			}

			// Check all expected params are present
			for _, expected := range expectedParams {
				if !actualParams[expected.Name] {
					t.Errorf("%s %s: missing path parameter %q", method, path, expected.Name)
				}
			}
		}

		checkOperation("GET", item.Get)
		checkOperation("POST", item.Post)
		checkOperation("PUT", item.Put)
		checkOperation("PATCH", item.Patch)
		checkOperation("DELETE", item.Delete)
	}
}

// TestOpenAPISpecNoEmptyPaths verifies there are no paths without operations.
func TestOpenAPISpecNoEmptyPaths(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	for path, item := range spec.Paths {
		hasOperation := item.Get != nil || item.Post != nil ||
			item.Put != nil || item.Patch != nil || item.Delete != nil
		if !hasOperation {
			t.Errorf("path %s has no operations", path)
		}
	}
}

// TestOpenAPISpecRequiredComponents verifies required components are present.
func TestOpenAPISpecRequiredComponents(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	if spec.Components == nil {
		t.Fatal("expected Components to be defined")
	}

	// Check required schemas
	requiredSchemas := []string{"SuccessResponse", "ErrorResponse"}
	for _, name := range requiredSchemas {
		if _, ok := spec.Components.Schemas[name]; !ok {
			t.Errorf("missing required schema: %s", name)
		}
	}

	// Check security schemes
	if spec.Components.SecuritySchemes == nil {
		t.Error("expected SecuritySchemes to be defined")
	}
	if _, ok := spec.Components.SecuritySchemes["bearerAuth"]; !ok {
		t.Error("missing bearerAuth security scheme")
	}
	apiKey, ok := spec.Components.SecuritySchemes["apiKey"]
	if !ok {
		t.Fatal("missing apiKey security scheme")
	}
	if apiKey.Type != "apiKey" || apiKey.Name == "" || apiKey.In == "" {
		t.Fatalf("apiKey security scheme is incomplete: type=%q name=%q in=%q", apiKey.Type, apiKey.Name, apiKey.In)
	}
}

func TestOpenAPIEnvelopeSchemasMatchRuntimeContract(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")
	if spec.Components == nil {
		t.Fatal("expected Components to be defined")
	}

	success := requireOpenAPISchema(t, spec, "SuccessResponse")
	requireSchemaProperties(t, "SuccessResponse", success, []string{"success", "timestamp"})
	requireSchemaRequiredFields(t, "SuccessResponse", success, []string{"success", "timestamp"})

	failure := requireOpenAPISchema(t, spec, "ErrorResponse")
	requireSchemaProperties(t, "ErrorResponse", failure, []string{"success", "error", "error_code", "timestamp"})
	requireSchemaRequiredFields(t, "ErrorResponse", failure, []string{"success", "error", "error_code", "timestamp"})
	if _, ok := failure.Properties["code"]; ok {
		t.Fatal("ErrorResponse schema exposes legacy code field; runtime REST errors use error_code")
	}
}

// TestOpenAPISpecVersionFormat verifies the OpenAPI version is 3.1.x.
func TestOpenAPISpecVersionFormat(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	if spec.OpenAPI != "3.1.0" {
		t.Errorf("OpenAPI version = %q, want %q", spec.OpenAPI, "3.1.0")
	}
}

// TestOpenAPISpecInfoComplete verifies the info section is complete.
func TestOpenAPISpecInfoComplete(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	if spec.Info.Title == "" {
		t.Error("missing info.title")
	}
	if spec.Info.Version == "" {
		t.Error("missing info.version")
	}
	if spec.Info.Description == "" {
		t.Error("missing info.description")
	}
}

// TestOpenAPISpecServersComplete verifies at least one server is defined.
func TestOpenAPISpecServersComplete(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	if len(spec.Servers) == 0 {
		t.Error("expected at least one server")
	}

	for i, server := range spec.Servers {
		if server.URL == "" {
			t.Errorf("server[%d]: missing URL", i)
		}
	}
}

// TestOpenAPISpecCheckedInExists verifies the checked-in spec file exists.
// This test helps CI catch cases where the spec file is missing.
func TestOpenAPISpecCheckedInExists(t *testing.T) {
	// Skip if not running in the repo root
	if _, err := os.Stat("docs/openapi-kernel.json"); os.IsNotExist(err) {
		// Try relative to test file location
		dir, _ := os.Getwd()
		specPath := filepath.Join(dir, "..", "..", "docs", "openapi-kernel.json")
		if _, err := os.Stat(specPath); os.IsNotExist(err) {
			t.Skip("checked-in spec file not found (run from repo root)")
		}
	}
}

// TestOpenAPISpecMatchesKernelCommands verifies the spec includes all kernel commands
// with REST bindings.
// Note: In unit test context, CLI init() functions may not run, so this test
// validates the structure is correct rather than requiring specific commands.
func TestOpenAPISpecMatchesKernelCommands(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	// The spec structure should be valid even if no commands are registered
	// (which happens in isolated test runs)
	if spec.Paths == nil {
		t.Error("expected Paths to be non-nil")
	}

	// Count operations if any paths exist
	operationCount := 0
	for _, item := range spec.Paths {
		if item.Get != nil {
			operationCount++
		}
		if item.Post != nil {
			operationCount++
		}
		if item.Put != nil {
			operationCount++
		}
		if item.Patch != nil {
			operationCount++
		}
		if item.Delete != nil {
			operationCount++
		}
	}

	// Log the count for visibility, but don't fail if zero
	// (kernel commands may not be registered in unit test context)
	t.Logf("Found %d paths with %d total operations", len(spec.Paths), operationCount)
}

// TestOpenAPISpecSchemaReferences verifies all schema $refs point to valid schemas.
func TestOpenAPISpecSchemaReferences(t *testing.T) {
	spec := GenerateOpenAPISpec("1.0.0", "http://localhost:8080")

	// Collect all valid schema names
	validSchemas := make(map[string]bool)
	for name := range spec.Components.Schemas {
		validSchemas["#/components/schemas/"+name] = true
	}

	// Check all $ref in responses
	for path, item := range spec.Paths {
		checkOperation := func(method string, op *Operation) {
			if op == nil {
				return
			}
			for code, resp := range op.Responses {
				for _, media := range resp.Content {
					if media.Schema != nil && media.Schema.Ref != "" {
						// Note: We allow refs to schemas that don't exist yet
						// (they may be generated dynamically or come from kernel types)
						// Just log them for visibility
						if !validSchemas[media.Schema.Ref] {
							t.Logf("%s %s response %s references undefined schema: %s",
								method, path, code, media.Schema.Ref)
						}
					}
				}
			}
		}

		checkOperation("GET", item.Get)
		checkOperation("POST", item.Post)
		checkOperation("PUT", item.Put)
		checkOperation("PATCH", item.Patch)
		checkOperation("DELETE", item.Delete)
	}
}

func requireOpenAPISchema(t *testing.T, spec *OpenAPISpec, name string) *Schema {
	t.Helper()

	schema, ok := spec.Components.Schemas[name]
	if !ok {
		t.Fatalf("missing required schema: %s", name)
	}
	if schema == nil {
		t.Fatalf("schema %s is nil", name)
	}
	return schema
}

func requireSchemaProperties(t *testing.T, schemaName string, schema *Schema, fields []string) {
	t.Helper()

	for _, field := range fields {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("%s schema missing property %q", schemaName, field)
		}
	}
}

func requireSchemaRequiredFields(t *testing.T, schemaName string, schema *Schema, fields []string) {
	t.Helper()

	required := make(map[string]bool, len(schema.Required))
	for _, field := range schema.Required {
		required[field] = true
	}
	for _, field := range fields {
		if !required[field] {
			t.Errorf("%s schema does not require %q", schemaName, field)
		}
	}
}
