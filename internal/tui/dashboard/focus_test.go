package dashboard

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newTestFocusRing mirrors the removed NewFocusRing constructor.
func newTestFocusRing(targets []FocusTarget) FocusRing {
	var ring FocusRing
	ring.Rebuild(targets)
	return ring
}

func TestFocusRingNext(t *testing.T) {

	fr := newTestFocusRing([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "b", Visible: func() bool { return true }},
		{ID: "c", Visible: func() bool { return true }},
	})

	if fr.Current().ID != "a" {
		t.Fatalf("expected first focus target, got %q", fr.Current().ID)
	}

	fr.Next()
	if fr.Current().ID != "b" {
		t.Fatalf("expected focus to advance to b, got %q", fr.Current().ID)
	}

	fr.Next()
	fr.Next()
	if fr.Current().ID != "a" {
		t.Fatalf("expected focus to wrap to a, got %q", fr.Current().ID)
	}
}

func TestFocusRingSkipsHidden(t *testing.T) {

	fr := newTestFocusRing([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "b", Visible: func() bool { return false }},
		{ID: "c", Visible: func() bool { return true }},
	})

	fr.Next()
	if fr.Current().ID != "c" {
		t.Fatalf("expected hidden target to be skipped, got %q", fr.Current().ID)
	}
}

func TestFocusRingPrevWraps(t *testing.T) {

	fr := newTestFocusRing([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "b", Visible: func() bool { return true }},
	})

	fr.Prev()
	if fr.Current().ID != "b" {
		t.Fatalf("expected prev to wrap to b, got %q", fr.Current().ID)
	}
}

func TestFocusRingAllHiddenNoInfiniteLoop(t *testing.T) {

	fr := newTestFocusRing([]FocusTarget{
		{ID: "a", Visible: func() bool { return false }},
		{ID: "b", Visible: func() bool { return false }},
	})

	fr.Next()
	fr.Prev()
}

func TestFocusRingSetByID(t *testing.T) {

	fr := newTestFocusRing([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "b", Visible: func() bool { return true }},
	})

	if !fr.SetByID("b") {
		t.Fatal("expected SetByID to succeed for visible target")
	}
	if fr.Current().ID != "b" {
		t.Fatalf("expected focus on b, got %q", fr.Current().ID)
	}
	if fr.SetByID("missing") {
		t.Fatal("expected SetByID to fail for unknown target")
	}
}

func TestFocusRingRebuildPreservesFocus(t *testing.T) {

	fr := newTestFocusRing([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "b", Visible: func() bool { return true }},
	})

	if !fr.SetByID("b") {
		t.Fatal("expected to focus b")
	}

	fr.Rebuild([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "b", Visible: func() bool { return true }},
		{ID: "c", Visible: func() bool { return true }},
	})

	if fr.Current().ID != "b" {
		t.Fatalf("expected rebuild to preserve b, got %q", fr.Current().ID)
	}
}

func TestFocusRingRebuildCurrentRemoved(t *testing.T) {

	fr := newTestFocusRing([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "b", Visible: func() bool { return true }},
	})

	if !fr.SetByID("b") {
		t.Fatal("expected to focus b")
	}

	fr.Rebuild([]FocusTarget{
		{ID: "a", Visible: func() bool { return true }},
		{ID: "c", Visible: func() bool { return true }},
	})

	if fr.Current().ID != "a" {
		t.Fatalf("expected rebuild to fall back to a, got %q", fr.Current().ID)
	}
}

func TestFocusRingEmpty(t *testing.T) {

	fr := newTestFocusRing(nil)
	fr.Next()
	fr.Prev()
	if fr.Current().ID != "" {
		t.Fatalf("expected empty focus ring, got %q", fr.Current().ID)
	}
}

func TestMouseClickPaneListSetsFocus(t *testing.T) {

	m := newTestModel(120)
	m.focusedPanel = PanelDetail

	updated, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		X:      0,
		Y:      4,
	})
	m = updated.(Model)

	if m.focusedPanel != PanelPaneList {
		t.Fatalf("expected mouse click to focus pane list, got %v", m.focusedPanel)
	}
	if m.cursor != 0 {
		t.Fatalf("expected first pane to remain selected, got %d", m.cursor)
	}
}
