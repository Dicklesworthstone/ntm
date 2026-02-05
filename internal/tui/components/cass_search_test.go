package components

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"

	"github.com/Dicklesworthstone/ntm/internal/cass"
)

type stubCassExecutor struct {
	output []byte
	err    error
	called bool
	args   []string
}

func (s *stubCassExecutor) Run(_ context.Context, args ...string) ([]byte, error) {
	s.called = true
	s.args = append([]string(nil), args...)
	return s.output, s.err
}

func TestCassSearchSetSize(t *testing.T) {
	model := NewCassSearch(nil)
	model.SetSize(80, 24)

	if model.width != 80 || model.height != 24 {
		t.Fatalf("expected size 80x24, got %dx%d", model.width, model.height)
	}
	if model.list.Width() != 80 {
		t.Fatalf("expected list width 80, got %d", model.list.Width())
	}
	if model.list.Height() != 21 {
		t.Fatalf("expected list height 21, got %d", model.list.Height())
	}
	if model.textInput.Width != 76 {
		t.Fatalf("expected input width 76, got %d", model.textInput.Width)
	}
}

func TestCassSearchViewNotInstalled(t *testing.T) {
	model := NewCassSearch(nil)
	model.SetSize(60, 10)
	model.err = cass.ErrNotInstalled

	view := model.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
	if !contains(view, "CASS is not installed") {
		t.Fatalf("expected missing cass message, got: %q", view)
	}
	if !contains(view, "brew install cass") {
		t.Fatalf("expected install hint, got: %q", view)
	}
}

func TestCassSearchUpdateSearchResults(t *testing.T) {
	exec := &stubCassExecutor{
		output: []byte(`{"query":"foo","limit":20,"offset":0,"count":1,"total_matches":1,"hits":[{"source_path":"path/to/session","agent":"cod","workspace":"ws","title":"Hit title","score":1.0,"snippet":"snippet","match_type":"summary"}]}`),
	}

	model := NewCassSearch(nil)
	model.client = cass.NewClient(cass.WithExecutor(exec))
	model.searchID = 1

	updated, _ := model.Update(performSearchMsg{id: 1, query: "foo"})
	if !updated.searching {
		t.Fatal("expected searching to be true after performSearchMsg")
	}

	msg := updated.performSearch(1, "foo")()
	updated, _ = updated.Update(msg)
	if updated.searching {
		t.Fatal("expected searching to be false after results")
	}
	if len(updated.list.Items()) != 1 {
		t.Fatalf("expected 1 item, got %d", len(updated.list.Items()))
	}

	item, ok := updated.list.Items()[0].(searchItem)
	if !ok {
		t.Fatal("expected searchItem type")
	}
	if item.hit.Title != "Hit title" {
		t.Fatalf("expected title, got %q", item.hit.Title)
	}
	if !exec.called {
		t.Fatal("expected cass executor to be called")
	}
}

func TestCassSearchClearsOnEmptyQuery(t *testing.T) {
	model := NewCassSearch(nil)
	model.searchID = 1
	model.list.SetItems([]list.Item{listItemStub{}})

	updated, _ := model.Update(performSearchMsg{id: 1, query: ""})
	if len(updated.list.Items()) != 0 {
		t.Fatalf("expected items to be cleared, got %d", len(updated.list.Items()))
	}
}

func TestCassSearchInitReturnsCmd(t *testing.T) {
	model := NewCassSearch(nil)
	if cmd := model.Init(); cmd == nil {
		t.Fatal("expected Init to return a command")
	}
}

func TestSearchItemFields(t *testing.T) {
	item := searchItem{hit: cass.SearchHit{
		Title:      "Hit title",
		Agent:      "cod",
		SourcePath: "path/to/session",
	}}

	if item.Title() != "Hit title" {
		t.Fatalf("expected Title to be %q, got %q", "Hit title", item.Title())
	}
	if item.FilterValue() != "Hit title" {
		t.Fatalf("expected FilterValue to be %q, got %q", "Hit title", item.FilterValue())
	}
	desc := item.Description()
	if !strings.Contains(desc, "cod") || !strings.Contains(desc, "path/to/session") {
		t.Fatalf("unexpected Description output: %q", desc)
	}
	if !strings.Contains(desc, "•") {
		t.Fatalf("expected Description to include separator, got %q", desc)
	}
}

type listItemStub struct{}

func (listItemStub) Title() string       { return "stub" }
func (listItemStub) Description() string { return "" }
func (listItemStub) FilterValue() string { return "stub" }

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
