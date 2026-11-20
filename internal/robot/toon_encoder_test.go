package robot

import (
	"reflect"
	"strings"
	"testing"
)

// =============================================================================
// toonEncoder.encodeValue tests
// =============================================================================

func TestEncodeValue_Primitives(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	tests := []struct {
		name string
		val  any
		want string
	}{
		{"string identifier", "hello", "hello"},
		{"string with space", "hello world", `"hello world"`},
		{"string empty", "", `""`},
		{"string keyword true", "true", `"true"`},
		{"string keyword false", "false", `"false"`},
		{"string keyword null", "null", `"null"`},
		{"string with newline", "line1\nline2", `"line1\nline2"`},
		{"string with tab", "col1\tcol2", `"col1\tcol2"`},
		{"string with quote", `say "hi"`, `"say \"hi\""`},
		{"string with backslash", `path\to`, `"path\\to"`},
		{"string with carriage return", "cr\rhere", `"cr\rhere"`},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int positive", 42, "42"},
		{"int negative", -7, "-7"},
		{"int zero", 0, "0"},
		{"uint", uint(100), "100"},
		{"float simple", 3.14, "3.14"},
		{"float whole", 2.0, "2"},
		{"float trailing zeros", 1.50, "1.5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := enc.encodeValue(reflect.ValueOf(tc.val))
			if err != nil {
				t.Fatalf("encodeValue(%v) error: %v", tc.val, err)
			}
			if got != tc.want {
				t.Errorf("encodeValue(%v) = %q, want %q", tc.val, got, tc.want)
			}
		})
	}
}

func TestEncodeValue_Invalid(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.encodeValue(reflect.Value{})
	if err != nil {
		t.Fatalf("encodeValue(invalid) error: %v", err)
	}
	if got != "null" {
		t.Errorf("encodeValue(invalid) = %q, want %q", got, "null")
	}
}

func TestEncodeValue_NilPointer(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	var p *int
	got, err := enc.encodeValue(reflect.ValueOf(p))
	if err != nil {
		t.Fatalf("encodeValue(nil ptr) error: %v", err)
	}
	if got != "null" {
		t.Errorf("encodeValue(nil ptr) = %q, want %q", got, "null")
	}
}

func TestEncodeValue_NonNilPointer(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	val := 42
	got, err := enc.encodeValue(reflect.ValueOf(&val))
	if err != nil {
		t.Fatalf("encodeValue(ptr to 42) error: %v", err)
	}
	if got != "42" {
		t.Errorf("encodeValue(ptr to 42) = %q, want %q", got, "42")
	}
}

func TestEncodeValue_UnsupportedType(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	ch := make(chan int)
	_, err := enc.encodeValue(reflect.ValueOf(ch))
	if err == nil {
		t.Fatal("expected error for chan type, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported value type") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// toonEncoder.encodeString tests
// =============================================================================

func TestEncodeString(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple identifier", "hello", "hello"},
		{"underscore start", "_private", "_private"},
		{"alphanumeric", "item42", "item42"},
		{"with hyphen needs quote", "my-item", `"my-item"`},
		{"with dot needs quote", "file.txt", `"file.txt"`},
		{"starts with digit needs quote", "42abc", `"42abc"`},
		{"space needs quote", "hello world", `"hello world"`},
		{"empty needs quote", "", `""`},
		{"keyword true quoted", "true", `"true"`},
		{"keyword false quoted", "false", `"false"`},
		{"keyword null quoted", "null", `"null"`},
		{"special chars escaped", "a\"b\\c\nd\re\tf", `"a\"b\\c\nd\re\tf"`},
		{"all uppercase", "ABC", "ABC"},
		{"mixed case", "camelCase", "camelCase"},
		{"underscore only", "_", "_"},
		{"underscore and digits", "_123", "_123"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := enc.encodeString(tc.input)
			if got != tc.want {
				t.Errorf("encodeString(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// toonEncoder.formatFloat tests
// =============================================================================

func TestToonFormatFloat(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	tests := []struct {
		name  string
		input float64
		want  string
	}{
		{"whole number", 42.0, "42"},
		{"one decimal", 3.5, "3.5"},
		{"two decimals", 3.14, "3.14"},
		{"trailing zeros removed", 1.50, "1.5"},
		{"many trailing zeros", 2.100000, "2.1"},
		{"zero", 0.0, "0"},
		{"negative", -7.25, "-7.25"},
		{"negative whole", -100.0, "-100"},
		{"small decimal", 0.001, "0.001"},
		{"large number", 1234567.89, "1234567.89"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := enc.formatFloat(tc.input)
			if got != tc.want {
				t.Errorf("formatFloat(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// toonEncoder.extractFields tests
// =============================================================================

func TestExtractFields_Map(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]int{"beta": 2, "alpha": 1, "gamma": 3}
	fields, err := enc.extractFields(reflect.ValueOf(m))
	if err != nil {
		t.Fatalf("extractFields error: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("extractFields(map) = %v, want %v", fields, want)
	}
}

func TestExtractFields_Struct(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Sample struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		internal string //nolint:unused
		Ignored  string `json:"-"`
		OmitTag  int    `json:"omit_tag,omitempty"`
		NoTag    string
	}

	fields, err := enc.extractFields(reflect.ValueOf(Sample{}))
	if err != nil {
		t.Fatalf("extractFields error: %v", err)
	}
	want := []string{"id", "name", "omit_tag", "NoTag"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("extractFields(struct) = %v, want %v", fields, want)
	}
}

func TestExtractFields_EmptyMap(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]int{}
	fields, err := enc.extractFields(reflect.ValueOf(m))
	if err != nil {
		t.Fatalf("extractFields error: %v", err)
	}
	if len(fields) != 0 {
		t.Errorf("expected 0 fields for empty map, got %d", len(fields))
	}
}

func TestExtractFields_NilPointer(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	var p *map[string]int
	fields, err := enc.extractFields(reflect.ValueOf(p))
	if err != nil {
		t.Fatalf("extractFields error: %v", err)
	}
	if fields != nil {
		t.Errorf("expected nil fields for nil pointer, got %v", fields)
	}
}

func TestExtractFields_NonStringMapKey(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[int]string{1: "a", 2: "b"}
	_, err := enc.extractFields(reflect.ValueOf(m))
	if err == nil {
		t.Fatal("expected error for non-string map key, got nil")
	}
	if !strings.Contains(err.Error(), "non-string map key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExtractFields_InvalidKind(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	_, err := enc.extractFields(reflect.ValueOf(42))
	if err == nil {
		t.Fatal("expected error for int kind, got nil")
	}
	if !strings.Contains(err.Error(), "expected map or struct") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// toonEncoder.getFieldValue tests
// =============================================================================

func TestGetFieldValue_Map(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]int{"alpha": 1, "beta": 2}
	val, err := enc.getFieldValue(reflect.ValueOf(m), "alpha")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if val.Int() != 1 {
		t.Errorf("getFieldValue(map, alpha) = %d, want 1", val.Int())
	}
}

func TestGetFieldValue_MapMissingKey(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]int{"alpha": 1}
	val, err := enc.getFieldValue(reflect.ValueOf(m), "missing")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if val.IsValid() {
		t.Errorf("expected invalid value for missing key, got %v", val)
	}
}

// Regression for #209: a map keyed by a named string type (e.g.
// robot.EventCategory, `type EventCategory string`) must not panic in
// reflect.Value.MapIndex — the string field name has to be converted to the
// map's actual key type first.
func TestGetFieldValue_MapNamedStringKey(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[EventCategory]int{EventCategory("alpha"): 1, EventCategory("beta"): 2}
	val, err := enc.getFieldValue(reflect.ValueOf(m), "alpha")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if !val.IsValid() || val.Int() != 1 {
		t.Errorf("getFieldValue(EventCategory-keyed map, alpha) = %v, want 1", val)
	}

	// A missing named-string key returns an invalid (absent) value, not a panic.
	missing, err := enc.getFieldValue(reflect.ValueOf(m), "missing")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if missing.IsValid() {
		t.Errorf("expected invalid value for missing EventCategory key, got %v", missing)
	}
}

// A map whose key is not a string type has no string field to look up; the
// lookup must return an absent value rather than panicking.
func TestGetFieldValue_MapNonStringKey(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[int]string{1: "one"}
	val, err := enc.getFieldValue(reflect.ValueOf(m), "1")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if val.IsValid() {
		t.Errorf("expected invalid value for non-string-keyed map, got %v", val)
	}
}

func TestGetFieldValue_Struct(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Item struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}

	item := Item{ID: 42, Name: "test"}
	val, err := enc.getFieldValue(reflect.ValueOf(item), "id")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if val.Int() != 42 {
		t.Errorf("getFieldValue(struct, id) = %d, want 42", val.Int())
	}
}

func TestGetFieldValue_StructFallbackToFieldName(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Item struct {
		NoTag string
	}

	item := Item{NoTag: "value"}
	val, err := enc.getFieldValue(reflect.ValueOf(item), "NoTag")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if val.String() != "value" {
		t.Errorf("getFieldValue(struct, NoTag) = %q, want %q", val.String(), "value")
	}
}

func TestGetFieldValue_StructMissingField(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Item struct {
		ID int `json:"id"`
	}

	item := Item{ID: 1}
	_, err := enc.getFieldValue(reflect.ValueOf(item), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing struct field, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGetFieldValue_NilPointer(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	var p *map[string]int
	val, err := enc.getFieldValue(reflect.ValueOf(p), "key")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if val.IsValid() {
		t.Errorf("expected invalid value for nil pointer, got %v", val)
	}
}

// =============================================================================
// toonEncoder.isTabSafe tests
// =============================================================================

func TestIsTabSafe_SafeValues(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]string{
		{"name": "Alice", "city": "London"},
		{"name": "Bob", "city": "Paris"},
	}
	v := reflect.ValueOf(data)
	fields := []string{"name", "city"}

	if !enc.isTabSafe(v, fields) {
		t.Error("expected tab-safe for values without tabs/newlines")
	}
}

func TestIsTabSafe_UnsafeTab(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]string{
		{"name": "Alice", "desc": "has\ttab"},
		{"name": "Bob", "desc": "normal"},
	}
	v := reflect.ValueOf(data)
	fields := []string{"name", "desc"}

	if enc.isTabSafe(v, fields) {
		t.Error("expected tab-unsafe for values containing tab characters")
	}
}

func TestIsTabSafe_UnsafeNewline(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]string{
		{"name": "Alice", "bio": "line1\nline2"},
	}
	v := reflect.ValueOf(data)
	fields := []string{"name", "bio"}

	if enc.isTabSafe(v, fields) {
		t.Error("expected tab-unsafe for values containing newline characters")
	}
}

func TestIsTabSafe_UnsafeCarriageReturn(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]string{
		{"val": "has\rreturn"},
	}
	v := reflect.ValueOf(data)
	fields := []string{"val"}

	if enc.isTabSafe(v, fields) {
		t.Error("expected tab-unsafe for values containing carriage return")
	}
}

func TestIsTabSafe_NonStringFieldsIgnored(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]int{
		{"count": 42},
		{"count": 99},
	}
	v := reflect.ValueOf(data)
	fields := []string{"count"}

	if !enc.isTabSafe(v, fields) {
		t.Error("expected tab-safe for non-string fields")
	}
}

// =============================================================================
// toonEncoder.renderArray tests
// =============================================================================

func TestRenderArray_EmptySlice(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.renderArray(reflect.ValueOf([]int{}))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[]\n" {
		t.Errorf("renderArray([]) = %q, want %q", got, "[]\n")
	}
}

func TestRenderArray_PrimitiveInts(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.renderArray(reflect.ValueOf([]int{1, 2, 3}))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[3]:1,2,3\n" {
		t.Errorf("renderArray([1,2,3]) = %q, want %q", got, "[3]:1,2,3\n")
	}
}

func TestRenderArray_PrimitiveStrings(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.renderArray(reflect.ValueOf([]string{"hello", "world"}))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[2]:hello,world\n" {
		t.Errorf("renderArray([hello,world]) = %q, want %q", got, "[2]:hello,world\n")
	}
}

func TestRenderArray_PrimitiveStringWithSpaces(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.renderArray(reflect.ValueOf([]string{"hello world", "foo"}))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != `[2]:"hello world",foo`+"\n" {
		t.Errorf("renderArray result = %q", got)
	}
}

func TestRenderArray_PrimitiveBools(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.renderArray(reflect.ValueOf([]bool{true, false, true}))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[3]:true,false,true\n" {
		t.Errorf("renderArray result = %q, want %q", got, "[3]:true,false,true\n")
	}
}

func TestRenderArray_SingleElement(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.renderArray(reflect.ValueOf([]int{42}))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[1]:42\n" {
		t.Errorf("renderArray result = %q, want %q", got, "[1]:42\n")
	}
}

func TestRenderArray_ObjectsDelegatesToTabular(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	data := []map[string]int{
		{"a": 1},
		{"a": 2},
	}
	got, err := enc.renderArray(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	// Should contain tabular header
	if !strings.Contains(got, "[2]{a}:") {
		t.Errorf("expected tabular format header, got %q", got)
	}
}

func TestRenderArray_TabDelimiter(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	got, err := enc.renderArray(reflect.ValueOf([]int{10, 20, 30}))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[3\t]:10\t20\t30\n" {
		t.Errorf("renderArray with tab = %q, want %q", got, "[3\t]:10\t20\t30\n")
	}
}

// =============================================================================
// toonEncoder.renderTabular tests
// =============================================================================

func TestRenderTabular_EmptySlice(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	got, err := enc.renderTabular(reflect.ValueOf([]map[string]int{}))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}
	if got != "[]\n" {
		t.Errorf("renderTabular([]) = %q, want %q", got, "[]\n")
	}
}

func TestRenderTabular_UniformMaps(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	data := []map[string]any{
		{"id": 1, "name": "Alice"},
		{"id": 2, "name": "Bob"},
	}

	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}

	// Fields should be sorted: id, name
	if !strings.HasPrefix(got, "[2]{id,name}:\n") {
		t.Errorf("expected header [2]{id,name}:, got %q", got)
	}

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 { // header + 2 rows
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}

	// Row 1: " 1,Alice"
	if lines[1] != " 1,Alice" {
		t.Errorf("row 1 = %q, want %q", lines[1], " 1,Alice")
	}
	// Row 2: " 2,Bob"
	if lines[2] != " 2,Bob" {
		t.Errorf("row 2 = %q, want %q", lines[2], " 2,Bob")
	}
}

func TestRenderTabular_UniformStructs(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Agent struct {
		Type  string `json:"type"`
		Pane  int    `json:"pane"`
		Ready bool   `json:"ready"`
	}

	data := []Agent{
		{Type: "claude", Pane: 1, Ready: true},
		{Type: "codex", Pane: 2, Ready: false},
	}

	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}

	// Structs preserve declaration/json field order.
	if !strings.HasPrefix(got, "[2]{type,pane,ready}:\n") {
		t.Errorf("expected declaration-order header, got %q", got)
	}

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[1] != " claude,1,true" {
		t.Errorf("row 1 = %q, want %q", lines[1], " claude,1,true")
	}
	if lines[2] != " codex,2,false" {
		t.Errorf("row 2 = %q, want %q", lines[2], " codex,2,false")
	}
}

func TestRenderTabular_SingleRow(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	data := []map[string]string{
		{"key": "value"},
	}

	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}
	if !strings.HasPrefix(got, "[1]{key}:\n") {
		t.Errorf("expected [1]{key}: header, got %q", got)
	}
	if !strings.Contains(got, " value\n") {
		t.Errorf("expected row with value, got %q", got)
	}
}

func TestRenderTabular_TabDelimiterSafeFallback(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]string{
		{"name": "Alice", "desc": "has\ttab"},
		{"name": "Bob", "desc": "normal"},
	}

	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}

	// When tab-unsafe, should fall back to comma delimiter in rows
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	for _, line := range lines[1:] {
		if strings.Contains(line, "\t") {
			t.Errorf("expected comma fallback when tab-unsafe, got line with tab: %q", line)
		}
	}
}

func TestRenderTabular_TabDelimiterSafe(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]string{
		{"name": "Alice"},
		{"name": "Bob"},
	}

	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}

	// Tab-safe, so tabs should be used in rows
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	// With single field, no delimiter between fields (only one field)
	if lines[1] != " Alice" {
		t.Errorf("row 1 = %q, want %q", lines[1], " Alice")
	}
}

// =============================================================================
// toonEncoder.renderObject tests
// =============================================================================

func TestRenderObject_SimpleMap(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]int{"count": 42, "value": 100}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}

	// Fields sorted alphabetically: count, value
	if !strings.Contains(got, "count: 42\n") {
		t.Errorf("expected 'count: 42', got %q", got)
	}
	if !strings.Contains(got, "value: 100\n") {
		t.Errorf("expected 'value: 100', got %q", got)
	}
}

func TestRenderObject_SimpleStruct(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Config struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Enabled bool   `json:"enabled"`
	}

	cfg := Config{Host: "localhost", Port: 8080, Enabled: true}
	got, err := enc.renderObject(reflect.ValueOf(cfg), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}

	hostIdx := strings.Index(got, "host: localhost\n")
	portIdx := strings.Index(got, "port: 8080\n")
	enabledIdx := strings.Index(got, "enabled: true\n")
	if hostIdx == -1 || portIdx == -1 || enabledIdx == -1 {
		t.Fatalf("missing expected fields in %q", got)
	}
	if !(hostIdx < portIdx && portIdx < enabledIdx) {
		t.Errorf("expected struct field order host->port->enabled, got %q", got)
	}
}

func TestRenderObject_PreservesProjectionSectionOrder(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Projection struct {
		Summary  string `json:"summary"`
		Sessions string `json:"sessions"`
		Alerts   string `json:"alerts"`
	}

	got, err := enc.renderObject(reflect.ValueOf(Projection{
		Summary:  "summary",
		Sessions: "sessions",
		Alerts:   "alerts",
	}), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}

	summaryIdx := strings.Index(got, "summary: summary\n")
	sessionsIdx := strings.Index(got, "sessions: sessions\n")
	alertsIdx := strings.Index(got, "alerts: alerts\n")
	if summaryIdx == -1 || sessionsIdx == -1 || alertsIdx == -1 {
		t.Fatalf("missing expected fields in %q", got)
	}
	if !(summaryIdx < sessionsIdx && sessionsIdx < alertsIdx) {
		t.Fatalf("expected projection order summary->sessions->alerts, got %q", got)
	}
}

func TestRenderObject_EmptyMap(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]int{}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}
	if got != "{}\n" {
		t.Errorf("renderObject({}) = %q, want %q", got, "{}\n")
	}
}

func TestRenderObject_WithIndent(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]string{"name": "test"}
	got, err := enc.renderObject(reflect.ValueOf(m), 2)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}
	// Indent of 2 = 4 spaces (2 * "  ")
	if !strings.HasPrefix(got, "    name: test\n") {
		t.Errorf("expected indented output, got %q", got)
	}
}

func TestRenderObject_NilMapValue(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]any{"key": nil}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}
	if !strings.Contains(got, "key: null\n") {
		t.Errorf("expected 'key: null', got %q", got)
	}
}

func TestRenderObject_NestedMap(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]any{
		"name": "test",
		"meta": map[string]any{
			"version": "1.0",
		},
	}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}

	if !strings.Contains(got, "meta:\n") {
		t.Errorf("expected nested object header 'meta:', got %q", got)
	}
	if !strings.Contains(got, "  version: \"1.0\"\n") {
		t.Errorf("expected indented nested field, got %q", got)
	}
	if !strings.Contains(got, "name: test\n") {
		t.Errorf("expected 'name: test', got %q", got)
	}
}

func TestRenderObject_WithArrayField(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]any{
		"tags": []string{"go", "rust"},
	}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}

	if !strings.Contains(got, "tags[2]:go,rust\n") {
		t.Errorf("expected array field in output, got %q", got)
	}
}

func TestRenderObject_PointerField(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	val := "hello"
	m := map[string]any{
		"ptr": &val,
	}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}
	if !strings.Contains(got, "ptr: hello\n") {
		t.Errorf("expected pointer dereferenced, got %q", got)
	}
}

func TestRenderObject_NilPointerField(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	var p *string
	m := map[string]any{
		"ptr": p,
	}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}
	if !strings.Contains(got, "ptr: null\n") {
		t.Errorf("expected nil pointer rendered as null, got %q", got)
	}
}

// =============================================================================
// Integration: Full encoding pipeline (without tru binary)
// =============================================================================

func TestRenderObject_RobotResponseShape(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	resp := map[string]any{
		"success":   true,
		"timestamp": "2026-01-15T10:30:00Z",
		"data": map[string]any{
			"sessions": 3,
			"agents":   9,
		},
	}

	got, err := enc.renderObject(reflect.ValueOf(resp), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}

	if !strings.Contains(got, "success: true\n") {
		t.Errorf("expected 'success: true', got %q", got)
	}
	if !strings.Contains(got, "timestamp: \"2026-01-15T10:30:00Z\"\n") {
		t.Errorf("expected timestamp field, got %q", got)
	}
	if !strings.Contains(got, "data:\n") {
		t.Errorf("expected nested data field, got %q", got)
	}
}

func TestRenderTabular_MultipleFieldTypes(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	data := []map[string]any{
		{"name": "session1", "panes": 4, "active": true, "usage": 0.45},
		{"name": "session2", "panes": 3, "active": false, "usage": 0.22},
	}

	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}

	// Fields sorted: active, name, panes, usage
	if !strings.HasPrefix(got, "[2]{active,name,panes,usage}:\n") {
		t.Errorf("expected sorted header, got first line: %q", strings.SplitN(got, "\n", 2)[0])
	}

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[1] != " true,session1,4,0.45" {
		t.Errorf("row 1 = %q, want %q", lines[1], " true,session1,4,0.45")
	}
	if lines[2] != " false,session2,3,0.22" {
		t.Errorf("row 2 = %q, want %q", lines[2], " false,session2,3,0.22")
	}
}

func TestRenderArray_LargeSlice(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	data := make([]int, 100)
	for i := range data {
		data[i] = i
	}

	got, err := enc.renderArray(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}

	if !strings.HasPrefix(got, "[100]:") {
		t.Errorf("expected [100]: prefix, got %q", got[:20])
	}
	if !strings.Contains(got, "0,1,2,3") {
		t.Errorf("expected sequential values in output")
	}
}

func TestEncodeValue_NestedComplexFallsBackToJSON(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	nested := map[string]int{"a": 1}
	got, err := enc.encodeValue(reflect.ValueOf(nested))
	if err != nil {
		t.Fatalf("encodeValue error: %v", err)
	}
	// Complex types should fall back to JSON inline (quoted)
	if !strings.Contains(got, "a") {
		t.Errorf("expected JSON inline for complex value, got %q", got)
	}
}

// =============================================================================
// Edge case tests for remaining coverage gaps
// =============================================================================

func TestRenderArray_StructElements(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Item struct {
		X int `json:"x"`
	}
	data := []Item{{X: 10}, {X: 20}}
	got, err := enc.renderArray(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	// Struct elements should delegate to tabular
	if !strings.Contains(got, "[2]{x}:") {
		t.Errorf("expected tabular format for struct array, got %q", got)
	}
}

func TestRenderArray_PointerElements(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	a, b := 10, 20
	data := []*int{&a, &b}
	got, err := enc.renderArray(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[2]:10,20\n" {
		t.Errorf("renderArray pointers = %q, want %q", got, "[2]:10,20\n")
	}
}

func TestRenderArray_InterfaceElements(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	data := []any{1, 2, 3}
	got, err := enc.renderArray(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderArray error: %v", err)
	}
	if got != "[3]:1,2,3\n" {
		t.Errorf("renderArray interfaces = %q, want %q", got, "[3]:1,2,3\n")
	}
}

func TestRenderTabular_PointerElements(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Row struct {
		Val int `json:"val"`
	}
	r1, r2 := Row{Val: 10}, Row{Val: 20}
	data := []*Row{&r1, &r2}
	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}
	if !strings.Contains(got, "[2]{val}:") {
		t.Errorf("expected header, got %q", got)
	}
	if !strings.Contains(got, " 10\n") {
		t.Errorf("expected row with 10, got %q", got)
	}
}

func TestRenderTabular_ManyFields(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	data := []map[string]any{
		{"a": 1, "b": "two", "c": true, "d": 3.14, "e": "five"},
	}
	got, err := enc.renderTabular(reflect.ValueOf(data))
	if err != nil {
		t.Fatalf("renderTabular error: %v", err)
	}
	// Fields sorted: a,b,c,d,e
	if !strings.HasPrefix(got, "[1]{a,b,c,d,e}:\n") {
		t.Errorf("expected [1]{a,b,c,d,e}: header, got %q", strings.SplitN(got, "\n", 2)[0])
	}
}

func TestRenderObject_DeeplyNested(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]any{
		"level1": map[string]any{
			"level2": map[string]any{
				"value": 42,
			},
		},
	}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}
	if !strings.Contains(got, "level1:\n") {
		t.Errorf("missing level1 header in %q", got)
	}
	if !strings.Contains(got, "  level2:\n") {
		t.Errorf("missing level2 header in %q", got)
	}
	if !strings.Contains(got, "    value: 42\n") {
		t.Errorf("missing deeply nested value in %q", got)
	}
}

func TestRenderObject_EmptyArrayField(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]any{
		"items": []int{},
	}
	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}
	if !strings.Contains(got, "items[]\n") {
		t.Errorf("expected empty array field, got %q", got)
	}
}

func TestGetFieldValue_StructWithOmitempty(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	type Item struct {
		Name  string `json:"name,omitempty"`
		Count int    `json:"count,omitempty"`
	}

	item := Item{Name: "test", Count: 5}
	val, err := enc.getFieldValue(reflect.ValueOf(item), "name")
	if err != nil {
		t.Fatalf("getFieldValue error: %v", err)
	}
	if val.String() != "test" {
		t.Errorf("getFieldValue(struct, name) = %q, want %q", val.String(), "test")
	}
}

func TestGetFieldValue_InvalidKind(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	_, err := enc.getFieldValue(reflect.ValueOf(42), "key")
	if err == nil {
		t.Fatal("expected error for int kind, got nil")
	}
	if !strings.Contains(err.Error(), "expected map or struct") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIsTabSafe_EmptySlice(t *testing.T) {
	enc := &toonEncoder{delimiter: "\t"}

	data := []map[string]string{}
	v := reflect.ValueOf(data)
	fields := []string{"name"}

	if !enc.isTabSafe(v, fields) {
		t.Error("expected tab-safe for empty slice")
	}
}

func TestEncodeValue_AllIntTypes(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	tests := []struct {
		name string
		val  any
		want string
	}{
		{"int8", int8(127), "127"},
		{"int16", int16(-1000), "-1000"},
		{"int32", int32(42), "42"},
		{"int64", int64(9999999), "9999999"},
		{"uint8", uint8(255), "255"},
		{"uint16", uint16(1000), "1000"},
		{"uint32", uint32(42), "42"},
		{"uint64", uint64(9999999), "9999999"},
		{"float32", float32(1.5), "1.5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := enc.encodeValue(reflect.ValueOf(tc.val))
			if err != nil {
				t.Fatalf("encodeValue error: %v", err)
			}
			if got != tc.want {
				t.Errorf("encodeValue(%v) = %q, want %q", tc.val, got, tc.want)
			}
		})
	}
}

func TestRenderObject_MixedScalarAndComplex(t *testing.T) {
	enc := &toonEncoder{delimiter: ","}

	m := map[string]any{
		"name":  "project",
		"count": 5,
		"tags":  []int{1, 2, 3},
		"meta": map[string]any{
			"author": "test",
		},
	}

	got, err := enc.renderObject(reflect.ValueOf(m), 0)
	if err != nil {
		t.Fatalf("renderObject error: %v", err)
	}

	// Verify scalar fields
	if !strings.Contains(got, "count: 5\n") {
		t.Errorf("missing 'count: 5' in %q", got)
	}
	if !strings.Contains(got, "name: project\n") {
		t.Errorf("missing 'name: project' in %q", got)
	}

	// Verify array field
	if !strings.Contains(got, "tags[3]:1,2,3\n") {
		t.Errorf("missing tags array in %q", got)
	}

	// Verify nested object
	if !strings.Contains(got, "meta:\n") {
		t.Errorf("missing 'meta:' header in %q", got)
	}
	if !strings.Contains(got, "  author: test\n") {
		t.Errorf("missing indented author field in %q", got)
	}
}
