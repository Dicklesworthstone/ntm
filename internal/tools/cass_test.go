package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestCASSAdapter_ExtractKeyConcepts(t *testing.T) {
	t.Parallel()

	a := NewCASSAdapter()

	got := a.extractKeyConcepts("go to fix auth bug")
	want := []string{"fix", "auth", "bug"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractKeyConcepts() = %#v, want %#v", got, want)
	}
}

func TestCASSAdapter_BuildQueries(t *testing.T) {
	t.Parallel()

	a := NewCASSAdapter()

	if got := a.buildRelatedSessionQuery(nil, "sess"); got != "" {
		t.Fatalf("buildRelatedSessionQuery(nil) = %q, want \"\"", got)
	}
	if got := a.buildPatternQuery(nil); got != "" {
		t.Fatalf("buildPatternQuery(nil) = %q, want \"\"", got)
	}

	concepts := []string{"auth", "bug"}

	if got := a.buildRelatedSessionQuery(concepts, "sess"); got != "auth OR bug" {
		t.Fatalf("buildRelatedSessionQuery() = %q, want %q", got, "auth OR bug")
	}
	if got := a.buildPatternQuery(concepts); got != "auth AND bug" {
		t.Fatalf("buildPatternQuery() = %q, want %q", got, "auth AND bug")
	}
}

func TestCASSAdapter_EnhanceAndFilterPassthrough(t *testing.T) {
	t.Parallel()

	a := NewCASSAdapter()

	query := "hello world"
	if got := a.enhanceQueryForContext(query); got != query {
		t.Fatalf("enhanceQueryForContext() = %q, want %q", got, query)
	}

	raw := json.RawMessage(`{"hits":[1]}`)
	out, err := a.filterAndRankForContext(raw, 10)
	if err != nil {
		t.Fatalf("filterAndRankForContext() error: %v", err)
	}
	if string(out) != string(raw) {
		t.Fatalf("filterAndRankForContext() = %s, want %s", out, raw)
	}
	if !json.Valid(out) {
		t.Fatalf("filterAndRankForContext() returned invalid JSON: %s", out)
	}
}

func TestCASSAdapter_HasCapability(t *testing.T) {
	t.Parallel()

	a := NewCASSAdapter()
	ctx := context.Background()

	if !a.HasCapability(ctx, CapSearch) {
		t.Fatalf("expected CapSearch capability")
	}
	if a.HasCapability(ctx, Capability("nope")) {
		t.Fatalf("expected unknown capability to be false")
	}
}
