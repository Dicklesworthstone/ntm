package pipeline

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestEvaluateForeachFilterJSONNumbers(t *testing.T) {
	for _, tc := range []struct {
		expr  string
		value string
		want  bool
	}{
		{"item", "0", false},
		{"item", "-0", false},
		{"item", "0.0", false},
		{"item", "-0.000", false},
		{"item", "0e999999", false},
		{"item", "0e-999999", false},
		{"item", "-0.0E+999999", false},
		{"item", "1.0", true},
		{"item", "-1", true},
		{"item", "1e999999", true},
		{"item", "1e-999999", true},
		{"item", "-0.0001E-999999", true},
		{"item.enabled", `{"enabled":0}`, false},
		{"item.enabled", `{"enabled":0e999999}`, false},
		{"item.enabled", `{"enabled":1e-999999}`, true},
		{"item==9007199254740992", "9007199254740993", false},
		{"item!=9007199254740992", "9007199254740993", true},
		{"item==9007199254740993", "9007199254740993", true},
		{"item==1", "1.0", true},
		{"item==1", "1e0", true},
		{"item==1.0", "1", true},
		{"item==1e0", "1", true},
		{"item==10", "1e1", true},
		{"item==0.1", "1e-1", true},
		{"item==0.10", "0.1", true},
		{"item==-0", "0.0", true},
		{"item==0", "-0.0E+999999", true},
		{"item==0", "1e-999999", false},
		{"item==1e-999999", "10e-1000000", true},
		{"item==1e999999999999999999999", "10e999999999999999999998", true},
		{"item==1e999999999999999999999", "1e999999999999999999998", false},
		{"item==1e-999999999999999999999", "10e-1000000000000000000000", true},
		{"item==-0.125", "-125e-3", true},
		{"item==18446744073709551615", "18446744073709551615", true},
		{"item==18446744073709551614", "18446744073709551615", false},
	} {
		t.Run(tc.expr+"/"+tc.value, func(t *testing.T) {
			// Exercise the exact number types retained by API and job decoders,
			// including nested values, without rounding their representation.
			decoder := json.NewDecoder(strings.NewReader(tc.value))
			decoder.UseNumber()
			var item interface{}
			if err := decoder.Decode(&item); err != nil {
				t.Fatalf("decode JSON fixture: %v", err)
			}
			got, err := EvaluateForeachFilter(tc.expr, FilterContext{Item: item})
			if err != nil || got != tc.want {
				t.Fatalf("filter %q for JSON %s = %v, %v; want %v", tc.expr, tc.value, got, err, tc.want)
			}
		})
	}
}

func TestFilterNumericKindsStayExact(t *testing.T) {
	type signedID int64
	type unsignedID uint64
	for _, tc := range []struct {
		name        string
		left, right interface{}
		want        bool
	}{
		{"signed neighbors", int64(9007199254740993), int64(9007199254740992), false},
		{"unsigned neighbors", uint64(18446744073709551615), uint64(18446744073709551614), false},
		{"unsigned JSON maximum", uint64(18446744073709551615), json.Number("18446744073709551615"), true},
		{"signed alias", signedID(9007199254740993), json.Number("9007199254740993"), true},
		{"unsigned alias", unsignedID(18446744073709551615), json.Number("18446744073709551615"), true},
		{"numeric strings", "9007199254740993", "9007199254740992", false},
		{"equivalent decimal strings", "1.00", "10e-1", true},
		{"float32 decimal", float32(0.1), json.Number("0.1"), true},
		{"float64 decimal", float64(0.1), json.Number("0.1"), true},
		{"already rounded float", float64(9007199254740993), json.Number("9007199254740993"), false},
		{"float NaN", math.NaN(), math.NaN(), false},
		{"float32 NaN", float32(math.NaN()), "NaN", false},
		{"text NaN", "NaN", "NaN", false},
		{"mixed NaN", math.NaN(), "NaN", false},
		{"positive infinity spelling", "Inf", "+Inf", true},
		{"positive infinity float32", float32(math.Inf(1)), "Inf", true},
		{"negative infinity", math.Inf(-1), "-Inf", true},
		{"opposite infinities", math.Inf(-1), math.Inf(1), false},
		{"huge finite JSON and infinity", json.Number("1e999999999999"), math.Inf(1), false},
		{"huge finite string and infinity", "1e999999999999", "Inf", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := filterValuesEqual(tc.left, tc.right); got != tc.want {
				t.Fatalf("filterValuesEqual(%#v, %#v) = %v, want %v", tc.left, tc.right, got, tc.want)
			}
			if got := filterValuesEqual(tc.right, tc.left); got != tc.want {
				t.Fatalf("reversed filterValuesEqual(%#v, %#v) = %v, want %v", tc.right, tc.left, got, tc.want)
			}
		})
	}
}

func TestFilterNumericZeroKinds(t *testing.T) {
	type counter uint64
	for _, value := range []interface{}{
		int8(0), int16(0), int32(0), uint(0), uint8(0), uint16(0),
		uint32(0), uint64(0), uintptr(0), float32(0), counter(0),
	} {
		if filterTruthy(value) {
			t.Errorf("filterTruthy(%T(0)) = true", value)
		}
	}
	for _, value := range []interface{}{int8(-1), uint64(1), float32(0.1), counter(1)} {
		if !filterTruthy(value) {
			t.Errorf("filterTruthy(%#v) = false", value)
		}
	}
}

func TestEvaluateForeachFilterPaneRole(t *testing.T) {
	got, err := EvaluateForeachFilter("role==proposer", FilterContext{
		Pane: map[string]interface{}{"role": "proposer"},
	})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter() = false, want true")
	}
}

func TestEvaluateForeachFilterItemAnd(t *testing.T) {
	got, err := EvaluateForeachFilter("state==active && model!=cc", FilterContext{
		Item: map[string]interface{}{"state": "active", "model": "cod"},
	})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter() = false, want true")
	}
}

func TestEvaluateForeachFilterOr(t *testing.T) {
	got, err := EvaluateForeachFilter("role==proposer || role==investigator", FilterContext{
		Pane: map[string]interface{}{"role": "investigator"},
	})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter() = false, want true")
	}
}

func TestEvaluateForeachFilterParens(t *testing.T) {
	got, err := EvaluateForeachFilter("role==a && (state==x || state==y)", FilterContext{
		Item: map[string]interface{}{"state": "y"},
		Pane: map[string]interface{}{"role": "a"},
	})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter() = false, want true")
	}
}

func TestEvaluateForeachFilterUndefinedVariableErrors(t *testing.T) {
	_, err := EvaluateForeachFilter("missing==x", FilterContext{
		Item: map[string]interface{}{"state": "active"},
	})
	if err == nil {
		t.Fatal("EvaluateForeachFilter() error = nil, want undefined variable")
	}
	if !strings.Contains(err.Error(), "undefined filter variable") {
		t.Fatalf("EvaluateForeachFilter() error = %v, want undefined variable", err)
	}
}

func TestEvaluateForeachFilterScopedReferences(t *testing.T) {
	got, err := EvaluateForeachFilter("${item.state}==active && pane.role==reviewer", FilterContext{
		Item: map[string]interface{}{"state": "active"},
		Pane: map[string]interface{}{"role": "reviewer"},
	})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter() = false, want true")
	}
}

func TestEvaluateForeachFilterIntegerLiteral(t *testing.T) {
	got, err := EvaluateForeachFilter("priority==1", FilterContext{
		Item: map[string]interface{}{"priority": 1},
	})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter() = false, want true")
	}
}

// TestEvaluateForeachFilterScalarItem covers bd-8cx7e: foreach over a list of
// scalar items (e.g. ["a","b"]) must allow filters that reference the bare
// item value with `item==a` or `${item}==a`. Previously the path resolver
// fell through to pane lookup and surfaced an undefined-variable error.
func TestEvaluateForeachFilterScalarItem(t *testing.T) {
	for _, expr := range []string{"item==a", `${item}==a`, "item!=b"} {
		got, err := EvaluateForeachFilter(expr, FilterContext{Item: "a"})
		if err != nil {
			t.Fatalf("EvaluateForeachFilter(%q) error = %v", expr, err)
		}
		if !got {
			t.Fatalf("EvaluateForeachFilter(%q) = false, want true", expr)
		}
	}

	got, err := EvaluateForeachFilter("item==a", FilterContext{Item: "b"})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if got {
		t.Fatal("EvaluateForeachFilter(item==a) for Item=b = true, want false")
	}
}

func TestEvaluateForeachFilterBarePaneScalar(t *testing.T) {
	got, err := EvaluateForeachFilter("pane==main:1", FilterContext{Pane: "main:1"})
	if err != nil {
		t.Fatalf("EvaluateForeachFilter() error = %v", err)
	}
	if !got {
		t.Fatal("EvaluateForeachFilter(pane==main:1) for Pane='main:1' = false, want true")
	}
}

func TestSharedFilterGrammarQuotedValuesAndUnicode(t *testing.T) {
	ctx := FilterContext{Item: map[string]string{"title": `éclair || (a && b)`, "role": "reviewer"}}
	for _, expr := range []string{
		`title=="éclair || (a && b)" && role==reviewer`,
		"title\u2003==\u00a0\"éclair || (a && b)\"\u2003&&\u2003role==reviewer",
	} {
		got, err := EvaluateForeachFilter(expr, ctx)
		if err != nil || !got {
			t.Fatalf("EvaluateForeachFilter(%q) = %t, %v", expr, got, err)
		}
	}
	for _, expr := range []string{`title==unquoted"suffix"`, `role=="reviewer"suffix`, `role=="reviewer" == author`} {
		if _, err := EvaluateForeachFilter(expr, ctx); err == nil {
			t.Fatalf("EvaluateForeachFilter(%q) accepted an ambiguous literal", expr)
		}
	}
}

func TestSharedFilterGrammarBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		expr string
		want string
	}{
		{"bytes", `title=="` + strings.Repeat("x", maxFilterExpressionBytes) + `"`, "bytes"},
		{"tokens", strings.Repeat("role==a || ", maxFilterExpressionTokens/4) + "role==a", "tokens"},
		{"depth", strings.Repeat("(", maxFilterExpressionDepth+1) + "role==a" + strings.Repeat(")", maxFilterExpressionDepth+1), "levels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EvaluateForeachFilter(tc.expr, FilterContext{Item: map[string]string{"role": "a"}}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unbounded filter error = %v, want %q", err, tc.want)
			}
		})
	}
	expr := strings.Repeat("(", maxFilterExpressionDepth) + "role==a" + strings.Repeat(")", maxFilterExpressionDepth)
	if got, err := EvaluateForeachFilter(expr, FilterContext{Item: map[string]string{"role": "a"}}); err != nil || !got {
		t.Fatalf("maximum supported depth = %t, %v", got, err)
	}
}
