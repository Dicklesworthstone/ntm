package pipeline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestArrayLikeItemsAcceptsAllSliceFlavors(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want []interface{}
	}{
		{"interface_slice", []interface{}{"a", 1}, []interface{}{"a", 1}},
		{"string_slice", []string{"a", "b"}, []interface{}{"a", "b"}},
		{"int_slice", []int{1, 2}, []interface{}{1, 2}},
		{"int64_slice", []int64{int64(1), int64(2)}, []interface{}{int64(1), int64(2)}},
		{"float64_slice", []float64{1.5, 2.25}, []interface{}{1.5, 2.25}},
		{"bool_slice", []bool{true, false}, []interface{}{true, false}},
		{"empty_interface_slice", []interface{}{}, []interface{}{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := arrayLikeItems(tc.in)
			if err != nil {
				t.Fatalf("arrayLikeItems(%v) unexpected err: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("arrayLikeItems(%v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestArrayLikeItemsRejectsNonSlice(t *testing.T) {
	rejects := []interface{}{
		"a string",
		42,
		3.14,
		true,
		map[string]interface{}{"k": "v"},
		nil,
		[]byte("blob"),
	}
	for _, in := range rejects {
		_, err := arrayLikeItems(in)
		if err == nil {
			t.Fatalf("arrayLikeItems(%T) want error, got nil", in)
		}
		if !strings.Contains(err.Error(), "expected array-like") {
			t.Errorf("arrayLikeItems(%T) error = %q, want 'expected array-like'", in, err)
		}
	}
}

// bd-682z9: typed slices produced by step parsed output (bead_query stores
// ParsedData as []BeadRecord, future structured outputs will use other
// concrete slice types) must fan out per element rather than being wrapped
// as one iteration item.
func TestArrayLikeItemsAcceptsTypedSlices(t *testing.T) {
	beadRecords := []BeadRecord{
		{ID: "bd-1", Title: "first"},
		{ID: "bd-2", Title: "second"},
	}
	got, err := arrayLikeItems(beadRecords)
	if err != nil {
		t.Fatalf("arrayLikeItems([]BeadRecord) err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2 — typed slice was wrapped instead of fanned out", len(got))
	}
	first, ok := got[0].(BeadRecord)
	if !ok {
		t.Fatalf("got[0] = %T, want BeadRecord — element type lost", got[0])
	}
	if first.ID != "bd-1" {
		t.Fatalf("got[0].ID = %q, want bd-1", first.ID)
	}

	type widget struct{ N int }
	widgets := []widget{{N: 7}, {N: 9}, {N: 11}}
	got, err = arrayLikeItems(widgets)
	if err != nil {
		t.Fatalf("arrayLikeItems([]widget) err = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}

	// Pointer slice: each element should be the original *widget, not
	// dereferenced.
	pwidgets := []*widget{{N: 1}, {N: 2}}
	got, err = arrayLikeItems(pwidgets)
	if err != nil {
		t.Fatalf("arrayLikeItems([]*widget) err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if _, ok := got[0].(*widget); !ok {
		t.Fatalf("got[0] = %T, want *widget", got[0])
	}

	// Fixed-size array support — same code path via reflect.
	arr := [3]string{"a", "b", "c"}
	got, err = arrayLikeItems(arr)
	if err != nil {
		t.Fatalf("arrayLikeItems([3]string) err = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
}

func TestScalarOrArrayItemsWrapsScalarAsSingleItem(t *testing.T) {
	got, err := scalarOrArrayItems("solo")
	if err != nil {
		t.Fatalf("scalarOrArrayItems(scalar) err: %v", err)
	}
	if !reflect.DeepEqual(got, []interface{}{"solo"}) {
		t.Fatalf("scalar wrap = %#v, want [\"solo\"]", got)
	}

	got, err = scalarOrArrayItems(map[string]interface{}{"id": "x"})
	if err != nil {
		t.Fatalf("scalarOrArrayItems(map) err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("map wrap len = %d, want 1", len(got))
	}
}

func TestScalarOrArrayItemsPassesArraysThrough(t *testing.T) {
	got, err := scalarOrArrayItems([]string{"a", "b"})
	if err != nil {
		t.Fatalf("scalarOrArrayItems(arr) err: %v", err)
	}
	if !reflect.DeepEqual(got, []interface{}{"a", "b"}) {
		t.Fatalf("array passthrough = %#v, want [a b]", got)
	}
}

func TestParseItemsJSONLiteralRejectsNonArray(t *testing.T) {
	cases := []string{
		`{"key":"value"}`,
		`"string"`,
		`42`,
		`true`,
		`null`,
	}
	for _, in := range cases {
		_, err := parseItemsJSONLiteral(in)
		if err == nil {
			t.Errorf("parseItemsJSONLiteral(%q) want error, got nil", in)
			continue
		}
		if !strings.Contains(err.Error(), "expected JSON array") {
			t.Errorf("parseItemsJSONLiteral(%q) error = %q, want 'expected JSON array'", in, err)
		}
	}
}

func TestParseItemsJSONLiteralRejectsTrailingTokens(t *testing.T) {
	_, err := parseItemsJSONLiteral(`[1,2,3] [4,5,6]`)
	if err == nil {
		t.Fatal("trailing tokens want error, got nil")
	}
	if !strings.Contains(err.Error(), "trailing JSON tokens") {
		t.Errorf("trailing tokens error = %q, want 'trailing JSON tokens'", err)
	}
}

func TestParseItemsJSONLiteralRejectsMalformedJSON(t *testing.T) {
	_, err := parseItemsJSONLiteral(`[1,2,`)
	if err == nil {
		t.Fatal("malformed JSON want error, got nil")
	}
}

func TestParseItemsJSONLiteralNormalizesIntegers(t *testing.T) {
	got, err := parseItemsJSONLiteral(`[1, 2, 3]`)
	if err != nil {
		t.Fatalf("parseItemsJSONLiteral err: %v", err)
	}
	for i, item := range got {
		if _, ok := item.(int); !ok {
			t.Errorf("item %d type = %T, want int", i, item)
		}
	}
}

func TestExactVariableReferenceRecognizesValidForms(t *testing.T) {
	cases := map[string]string{
		"${vars.x}":        "vars.x",
		"${session_id}":    "session_id",
		"${ trimmed_ref }": "trimmed_ref",
		"${steps.a.out}":   "steps.a.out",
	}
	for in, want := range cases {
		got, ok := exactVariableReference(in)
		if !ok {
			t.Errorf("exactVariableReference(%q) ok=false, want true", in)
			continue
		}
		if got != want {
			t.Errorf("exactVariableReference(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExactVariableReferenceRejectsMalformed(t *testing.T) {
	rejects := []string{
		"plain string",
		"$vars.x",
		"${unclosed",
		"unopened}",
		"${}",
		"${  }",
		"text${vars.x}suffix",
	}
	for _, in := range rejects {
		if _, ok := exactVariableReference(in); ok {
			t.Errorf("exactVariableReference(%q) ok=true, want false", in)
		}
	}
}

func TestLookupItemsVariableNilVarsErrors(t *testing.T) {
	_, err := lookupItemsVariable("anything", nil)
	if err == nil {
		t.Fatal("nil vars want error, got nil")
	}
	if !strings.Contains(err.Error(), "no variables available") {
		t.Errorf("nil vars error = %q, want 'no variables available'", err)
	}
}

func TestLookupItemsVariableUnknownKeyErrors(t *testing.T) {
	vars := map[string]interface{}{"known": "v"}
	_, err := lookupItemsVariable("missing", vars)
	if err == nil {
		t.Fatal("unknown key want error, got nil")
	}
	if !strings.Contains(err.Error(), "undefined variable") {
		t.Errorf("unknown key error = %q, want 'undefined variable'", err)
	}
}

func TestLookupItemsVariableStripsVarsPrefix(t *testing.T) {
	vars := map[string]interface{}{"hypotheses": []string{"h1", "h2"}}
	got, err := lookupItemsVariable("vars.hypotheses", vars)
	if err != nil {
		t.Fatalf("vars.prefix lookup err: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"h1", "h2"}) {
		t.Errorf("got %#v, want [h1 h2]", got)
	}
}

func TestParseModelShellOutputStripsQuotesAndBlanks(t *testing.T) {
	out := []byte("  cc  \n\"cod\"\n'gmi'\n\n  \nclaude\n")
	got := parseModelShellOutput(out)
	want := []string{"cc", "cod", "gmi", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseModelShellOutput = %#v, want %#v", got, want)
	}
}

func TestParseModelShellOutputEmptyInput(t *testing.T) {
	got := parseModelShellOutput([]byte("   \n   "))
	if len(got) != 0 {
		t.Errorf("empty input: got %#v, want []", got)
	}
}

func TestNormalizeJSONNumbersRecursesIntoMaps(t *testing.T) {
	raw := json.Number("42")
	in := map[string]interface{}{
		"int":   raw,
		"float": json.Number("3.14"),
		"nested": map[string]interface{}{
			"deep": json.Number("7"),
		},
		"list": []interface{}{json.Number("1"), json.Number("2")},
		"str":  "leave-me",
	}
	got := normalizeJSONNumbers(in).(map[string]interface{})

	if got["int"] != 42 {
		t.Errorf("int = %v(%T), want 42(int)", got["int"], got["int"])
	}
	if got["float"] != 3.14 {
		t.Errorf("float = %v(%T), want 3.14(float64)", got["float"], got["float"])
	}
	nested := got["nested"].(map[string]interface{})
	if nested["deep"] != 7 {
		t.Errorf("nested.deep = %v(%T), want 7(int)", nested["deep"], nested["deep"])
	}
	list := got["list"].([]interface{})
	if list[0] != 1 || list[1] != 2 {
		t.Errorf("list = %#v, want [1 2]", list)
	}
	if got["str"] != "leave-me" {
		t.Errorf("str = %v, want 'leave-me'", got["str"])
	}
}

func TestNormalizeJSONNumbersHandlesScientificNotation(t *testing.T) {
	got := normalizeJSONNumbers(json.Number("1e10"))
	if got != float64(1e10) {
		t.Errorf("scientific = %v(%T), want 1e10(float64)", got, got)
	}
}

func TestNormalizeJSONNumbersHandlesNonNumberPassthrough(t *testing.T) {
	for _, in := range []interface{}{"hello", true, nil, 42} {
		got := normalizeJSONNumbers(in)
		if !reflect.DeepEqual(got, in) {
			t.Errorf("passthrough(%v) = %v, want unchanged", in, got)
		}
	}
}

