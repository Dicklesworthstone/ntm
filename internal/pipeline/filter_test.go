package pipeline

import (
	"strings"
	"testing"
)

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
