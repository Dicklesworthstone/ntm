package robot

import (
	"encoding/json"
	"strings"
	"testing"
)

// =============================================================================
// parseJSONTag Tests
// =============================================================================

func TestParseJSONTag(t *testing.T) {
	tests := []struct {
		tag       string
		wantName  string
		wantOmit  bool
	}{
		{"", "", false},
		{"name", "name", false},
		{"name,omitempty", "name", true},
		{"-", "-", false},
		{"field_name,omitempty", "field_name", true},
		{"name,string", "name", false},
		{"name,omitempty,string", "name", true},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			name, omitempty := parseJSONTag(tt.tag)
			if name != tt.wantName {
				t.Errorf("parseJSONTag(%q) name = %q, want %q", tt.tag, name, tt.wantName)
			}
			if omitempty != tt.wantOmit {
				t.Errorf("parseJSONTag(%q) omitempty = %v, want %v", tt.tag, omitempty, tt.wantOmit)
			}
		})
	}
}

// =============================================================================
// generateDescription Tests
// =============================================================================

func TestGenerateDescription(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Name", "Name"},
		{"AgentType", "Agent type"},
		{"IsWorking", "Is working"},
		{"ContextRemaining", "Context remaining"},
		{"A", "A"},
		{"", ""},
		{"HTTPStatus", "H t t p status"}, // CamelCase splits on each uppercase
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateDescription(tt.name)
			if got != tt.want {
				t.Errorf("generateDescription(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// =============================================================================
// typeToSchema Tests
// =============================================================================

func TestTypeToSchema_Primitives(t *testing.T) {
	type TestPrimitives struct {
		BoolField   bool    `json:"bool_field"`
		IntField    int     `json:"int_field"`
		FloatField  float64 `json:"float_field"`
		StringField string  `json:"string_field"`
	}

	schema := generateSchema(TestPrimitives{}, "test_primitives")

	if schema.Type != "object" {
		t.Errorf("schema.Type = %q, want %q", schema.Type, "object")
	}

	checks := map[string]string{
		"bool_field":   "boolean",
		"int_field":    "integer",
		"float_field":  "number",
		"string_field": "string",
	}

	for field, wantType := range checks {
		prop, ok := schema.Properties[field]
		if !ok {
			t.Errorf("missing property %q", field)
			continue
		}
		if prop.Type != wantType {
			t.Errorf("property %q type = %q, want %q", field, prop.Type, wantType)
		}
	}
}

func TestTypeToSchema_Slice(t *testing.T) {
	type TestSlice struct {
		Items []string `json:"items"`
	}

	schema := generateSchema(TestSlice{}, "test_slice")

	prop, ok := schema.Properties["items"]
	if !ok {
		t.Fatal("missing property 'items'")
	}
	if prop.Type != "array" {
		t.Errorf("items type = %q, want %q", prop.Type, "array")
	}
	if prop.Items == nil {
		t.Fatal("items.Items should not be nil")
	}
	if prop.Items.Type != "string" {
		t.Errorf("items.Items.Type = %q, want %q", prop.Items.Type, "string")
	}
}

func TestTypeToSchema_Map(t *testing.T) {
	type TestMap struct {
		Data map[string]int `json:"data"`
	}

	schema := generateSchema(TestMap{}, "test_map")

	prop, ok := schema.Properties["data"]
	if !ok {
		t.Fatal("missing property 'data'")
	}
	if prop.Type != "object" {
		t.Errorf("data type = %q, want %q", prop.Type, "object")
	}
	if prop.AdditionalProperties == nil {
		t.Fatal("data.AdditionalProperties should not be nil")
	}
	if prop.AdditionalProperties.Type != "integer" {
		t.Errorf("data.AdditionalProperties.Type = %q, want %q", prop.AdditionalProperties.Type, "integer")
	}
}

func TestTypeToSchema_Pointer(t *testing.T) {
	type TestPointer struct {
		Value *float64 `json:"value,omitempty"`
	}

	schema := generateSchema(TestPointer{}, "test_pointer")

	prop, ok := schema.Properties["value"]
	if !ok {
		t.Fatal("missing property 'value'")
	}
	if prop.Type != "number" {
		t.Errorf("value type = %q, want %q", prop.Type, "number")
	}
}

func TestTypeToSchema_OmitemptyRequired(t *testing.T) {
	type TestRequired struct {
		Required string `json:"required"`
		Optional string `json:"optional,omitempty"`
	}

	schema := generateSchema(TestRequired{}, "test_required")

	foundRequired := false
	foundOptional := false
	for _, req := range schema.Required {
		if req == "required" {
			foundRequired = true
		}
		if req == "optional" {
			foundOptional = true
		}
	}

	if !foundRequired {
		t.Error("'required' field should be in Required list")
	}
	if foundOptional {
		t.Error("'optional' field should NOT be in Required list")
	}
}

func TestTypeToSchema_SkipUnexported(t *testing.T) {
	type TestUnexported struct {
		Public  string `json:"public"`
		private string //nolint:unused
	}

	schema := generateSchema(TestUnexported{}, "test_unexported")

	if _, ok := schema.Properties["public"]; !ok {
		t.Error("exported field 'public' should be present")
	}
	// private field should not appear
	for key := range schema.Properties {
		if key == "private" || key == "Private" {
			t.Error("unexported field should not be present")
		}
	}
}

func TestTypeToSchema_JSONDash(t *testing.T) {
	type TestDash struct {
		Visible string `json:"visible"`
		Hidden  string `json:"-"`
	}

	schema := generateSchema(TestDash{}, "test_dash")

	if _, ok := schema.Properties["visible"]; !ok {
		t.Error("'visible' field should be present")
	}
	if _, ok := schema.Properties["-"]; ok {
		t.Error("json:\"-\" field should be skipped")
	}
	if _, ok := schema.Properties["Hidden"]; ok {
		t.Error("json:\"-\" field should be skipped")
	}
}

func TestTypeToSchema_EmbeddedStruct(t *testing.T) {
	// RobotResponse is an embedded struct used in all outputs
	schema := generateSchema(StatusOutput{}, "status")

	// Should have fields from the embedded RobotResponse
	if _, ok := schema.Properties["success"]; !ok {
		t.Error("embedded RobotResponse field 'success' should be present")
	}
}

// =============================================================================
// generateSchema Tests
// =============================================================================

func TestGenerateSchema_Metadata(t *testing.T) {
	schema := generateSchema(StatusOutput{}, "status")

	if schema.Schema != "http://json-schema.org/draft-07/schema#" {
		t.Errorf("Schema = %q, want draft-07", schema.Schema)
	}
	if !strings.Contains(schema.Title, "Status") {
		t.Errorf("Title = %q, should contain 'Status'", schema.Title)
	}
	if schema.Type != "object" {
		t.Errorf("Type = %q, want 'object'", schema.Type)
	}
}

func TestGenerateSchema_ValidJSON(t *testing.T) {
	schema := generateSchema(StatusOutput{}, "status")

	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("json.Marshal schema: %v", err)
	}
	if len(data) == 0 {
		t.Error("schema JSON should not be empty")
	}

	// Verify it roundtrips
	var roundtrip JSONSchema
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("json.Unmarshal roundtrip: %v", err)
	}
	if roundtrip.Type != "object" {
		t.Errorf("roundtrip Type = %q, want %q", roundtrip.Type, "object")
	}
}

// =============================================================================
// GetSchema Tests
// =============================================================================

func TestGetSchema_SingleType(t *testing.T) {
	output, err := GetSchema("status")
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}

	if !output.Success {
		t.Error("output.Success should be true")
	}
	if output.SchemaType != "status" {
		t.Errorf("SchemaType = %q, want %q", output.SchemaType, "status")
	}
	if output.Schema == nil {
		t.Fatal("Schema should not be nil for valid type")
	}
	if output.Schema.Type != "object" {
		t.Errorf("Schema.Type = %q, want %q", output.Schema.Type, "object")
	}
}

func TestGetSchema_AllTypes(t *testing.T) {
	output, err := GetSchema("all")
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}

	if !output.Success {
		t.Error("output.Success should be true")
	}
	if output.SchemaType != "all" {
		t.Errorf("SchemaType = %q, want %q", output.SchemaType, "all")
	}
	if len(output.Schemas) == 0 {
		t.Fatal("Schemas should not be empty for 'all'")
	}
	if len(output.Schemas) != len(SchemaCommand) {
		t.Errorf("got %d schemas, want %d", len(output.Schemas), len(SchemaCommand))
	}
}

func TestGetSchema_UnknownType(t *testing.T) {
	output, err := GetSchema("nonexistent")
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}

	if output.Success {
		t.Error("output.Success should be false for unknown type")
	}
	if output.ErrorCode == "" {
		t.Error("ErrorCode should be set for unknown type")
	}
}

// =============================================================================
// getSchemaTypes Tests
// =============================================================================

func TestGetSchemaTypes(t *testing.T) {
	types := getSchemaTypes()

	if len(types) == 0 {
		t.Fatal("schema types should not be empty")
	}
	if len(types) != len(SchemaCommand) {
		t.Errorf("got %d types, want %d", len(types), len(SchemaCommand))
	}

	// Verify some expected types
	typeSet := make(map[string]bool)
	for _, tp := range types {
		typeSet[tp] = true
	}

	expected := []string{"status", "spawn", "health", "assign"}
	for _, exp := range expected {
		if !typeSet[exp] {
			t.Errorf("expected type %q not found in schema types", exp)
		}
	}
}

// =============================================================================
// SchemaCommand Map Tests
// =============================================================================

func TestSchemaCommand_AllEntriesGenerateValidSchemas(t *testing.T) {
	for name, typ := range SchemaCommand {
		t.Run(name, func(t *testing.T) {
			schema := generateSchema(typ, name)
			if schema == nil {
				t.Fatal("schema should not be nil")
			}
			if schema.Type != "object" {
				t.Errorf("schema.Type = %q, want %q", schema.Type, "object")
			}
			if schema.Title == "" {
				t.Error("schema.Title should not be empty")
			}

			// Verify it serializes to valid JSON
			data, err := json.Marshal(schema)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if len(data) < 10 {
				t.Error("schema JSON suspiciously short")
			}
		})
	}
}
