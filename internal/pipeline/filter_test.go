package pipeline

import (
	"encoding/json"
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
