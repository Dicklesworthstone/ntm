package palette

// Keymap honesty tests (H3, bd-ws7-docs-ux-truth-tqh3l.3).
//
// The palette help bar used to advertise 1-9 quick-select, `q`, and `?` in the
// command phase even though the always-focused filter input swallowed every
// printable rune, so those keys were dead. The fix generates all help surfaces
// from the same per-phase keymaps the Update handlers match against; these
// tests pin the contract in BOTH directions per phase:
//
//	advertised ⊆ handled  — help never documents a key the phase ignores
//	handled ⊆ advertised  — every handled binding appears in some help surface
//
// plus reflection completeness (every binding field of each keymap struct is
// in its handled() list, so a newly added binding cannot dodge the table) and
// View()/Update() assertions for the previously dead keys.

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

// bindingID canonicalizes a binding by its key set for set membership checks.
func bindingID(b key.Binding) string {
	return strings.Join(b.Keys(), "|")
}

func bindingSet(bindings []key.Binding) map[string]bool {
	set := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		set[bindingID(b)] = true
	}
	return set
}

func entriesBindings(entryLists ...[]helpEntry) []key.Binding {
	var out []key.Binding
	for _, entries := range entryLists {
		for _, e := range entries {
			out = append(out, e.bindings...)
		}
	}
	return out
}

// reflectBindings returns every key.Binding field of a keymap struct.
func reflectBindings(t *testing.T, keymap any) []key.Binding {
	t.Helper()
	v := reflect.ValueOf(keymap)
	if v.Kind() != reflect.Struct {
		t.Fatalf("keymap must be a struct, got %T", keymap)
	}
	bindingType := reflect.TypeOf(key.Binding{})
	var out []key.Binding
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Type() != bindingType {
			t.Fatalf("keymap %T field %s is not a key.Binding", keymap, v.Type().Field(i).Name)
		}
		out = append(out, v.Field(i).Interface().(key.Binding))
	}
	return out
}

// phaseHonestyCase describes one phase's keymap struct and every help surface
// that advertises its keys. The handled set is derived by reflection over the
// keymap struct's binding fields — the same values the phase's Update handler
// matches against — so a newly added binding is in the table by construction.
type phaseHonestyCase struct {
	name       string
	keymap     any
	advertised [][]helpEntry
}

func phaseHonestyCases() []phaseHonestyCase {
	return []phaseHonestyCase{
		{
			name:   "command",
			keymap: commandKeys,
			advertised: [][]helpEntry{
				commandHelpEntries(layout.TierMega, true), // widest bar, scroll hint on
				commandOverlayEntries(),
			},
		},
		{
			name:   "target",
			keymap: targetKeys,
			advertised: [][]helpEntry{
				targetHelpEntries(),
				targetOverlayEntries(),
			},
		},
		{
			name:       "select-agents",
			keymap:     selectAgentsKeys,
			advertised: [][]helpEntry{selectAgentsHelpEntries()},
		},
		{
			name:       "edit",
			keymap:     editKeys,
			advertised: [][]helpEntry{editHelpEntries()},
		},
		{
			name:       "xf-search",
			keymap:     xfSearchKeys,
			advertised: [][]helpEntry{xfSearchHelpEntries()},
		},
		{
			name:       "xf-results",
			keymap:     xfResultsKeys,
			advertised: [][]helpEntry{xfResultsHelpEntries()},
		},
	}
}

// TestPaletteHelpHonesty is the both-direction honesty table:
// advertised ⊆ handled AND handled ⊆ advertised, per phase.
func TestPaletteHelpHonesty(t *testing.T) {
	for _, tc := range phaseHonestyCases() {
		t.Run(tc.name, func(t *testing.T) {
			handled := reflectBindings(t, tc.keymap)
			handledSet := bindingSet(handled)
			advertisedBindings := entriesBindings(tc.advertised...)
			advertisedSet := bindingSet(advertisedBindings)

			// Direction 1: advertised ⊆ handled.
			for _, b := range advertisedBindings {
				if !handledSet[bindingID(b)] {
					t.Errorf("help advertises binding %q (%s) that the %s phase does not handle",
						b.Help().Key, bindingID(b), tc.name)
				}
			}

			// Direction 2: handled ⊆ advertised.
			for _, b := range handled {
				if !advertisedSet[bindingID(b)] {
					t.Errorf("%s phase handles binding %q (%s) that no help surface advertises",
						tc.name, b.Help().Key, bindingID(b))
				}
			}
		})
	}
}

// TestCommandPhaseBindingsAvoidPrintableRunes: the filter input is permanently
// focused in the command phase, so any single printable rune bound there would
// be dead (swallowed by the search box). This is the guard that made the old
// 1-9/q/? advertisements lies.
func TestCommandPhaseBindingsAvoidPrintableRunes(t *testing.T) {
	printable := regexp.MustCompile(`^[\x20-\x7e]$`)
	for _, b := range reflectBindings(t, commandKeys) {
		for _, k := range b.Keys() {
			if printable.MatchString(k) {
				t.Errorf("command-phase binding %q includes printable key %q, which the focused filter swallows",
					b.Help().Key, k)
			}
		}
	}
}

// TestCommandHelpBarDropsDeadKeys asserts, on the rendered View(), that the
// dead advertisements are gone from the command phase and the live ones stay.
func TestCommandHelpBarDropsDeadKeys(t *testing.T) {
	m := New("test-session", testCommands)
	m.width = 400 // TierMega: widest help bar
	m.height = 40
	m.tier = layout.TierForWidth(m.width)
	m.syncListViewport()

	bar := stripANSI(m.renderHelpBar())

	for _, dead := range []string{"1-9", "quick select", "?"} {
		if strings.Contains(bar, dead) {
			t.Errorf("command help bar still advertises dead key %q: %s", dead, bar)
		}
	}
	// 'q' must not be advertised as a standalone key (ctrl+c is the quit key).
	if regexp.MustCompile(`(^|[^a-z+])q([^a-z]|$)`).MatchString(bar) {
		t.Errorf("command help bar still advertises 'q': %s", bar)
	}

	for _, live := range []string{"enter", "ctrl+n", "esc", "ctrl+p", "ctrl+f", "ctrl+c", "f1"} {
		if !strings.Contains(bar, live) {
			t.Errorf("command help bar missing live key %q: %s", live, bar)
		}
	}
}

// TestTargetHelpBarAdvertisesLiveKeys: in the target phase no text input is
// focused, so 1-6/e/?/q are genuinely live and must stay advertised.
func TestTargetHelpBarAdvertisesLiveKeys(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.width = 120
	m.height = 40

	view := stripANSI(m.View())
	for _, live := range []string{"1-5", "6", "e", "?", "esc", "q"} {
		if !strings.Contains(view, live) {
			t.Errorf("target phase view missing advertised key %q", live)
		}
	}
}

// TestHelpOverlayGeneratedFromKeymap asserts the ?/f1 overlay content comes
// from the keymaps: no dead 1-9 quick-select, and the previously undocumented
// half-page/home/end bindings are now listed.
func TestHelpOverlayGeneratedFromKeymap(t *testing.T) {
	m := New("test-session", testCommands)
	m.phase = PhaseTarget
	m.selected = &testCommands[0]
	m.width = 100
	m.height = 50

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = newModel.(Model)
	if !m.showHelp {
		t.Fatal("expected '?' to open the help overlay in the target phase")
	}

	view := stripANSI(m.View())
	if strings.Contains(view, "1-9") || strings.Contains(view, "Quick select") {
		t.Errorf("help overlay still advertises dead 1-9 quick-select:\n%s", view)
	}
	for _, want := range []string{"ctrl+u", "home/end", "ctrl+k", "1-5"} {
		if !strings.Contains(view, want) {
			t.Errorf("help overlay missing keymap-derived entry %q", want)
		}
	}
}

// TestQuestionMarkTypesIntoFilter: '?' is dead as a command-phase shortcut
// (the filter owns it); f1 opens the overlay instead.
func TestQuestionMarkTypesIntoFilter(t *testing.T) {
	m := New("test-session", testCommands)

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = newModel.(Model)

	if m.showHelp {
		t.Error("expected '?' NOT to open help in the command phase (filter owns runes)")
	}
	if m.filter.Value() != "?" {
		t.Errorf("expected '?' to type into the filter, got filter value %q", m.filter.Value())
	}

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyF1})
	m = newModel.(Model)
	if !m.showHelp {
		t.Error("expected f1 to open the help overlay in the command phase")
	}
}

// TestEditPhaseTypingQDoesNotQuit pins the fix for the old keys.Quit binding
// that included 'q' and was matched before the focused textarea: typing a
// prompt containing 'q' quit the palette.
func TestEditPhaseTypingQDoesNotQuit(t *testing.T) {
	m := New("test-session", testCommands)
	m.selected = &testCommands[0]
	m.editDraft = ""
	_ = m.enterEditPhase()

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = newModel.(Model)

	if m.quitting {
		t.Fatal("typing 'q' in the prompt editor must not quit the palette")
	}
	if !strings.Contains(m.editInput.Value(), "q") {
		t.Errorf("expected 'q' to be typed into the editor, got %q", m.editInput.Value())
	}
}

// TestXFSearchTypingQDoesNotQuit pins the same fix for the xf query input.
func TestXFSearchTypingQDoesNotQuit(t *testing.T) {
	m := New("test-session", testCommands)
	m.enterXFSearch()

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = newModel.(Model)

	if m.quitting {
		t.Fatal("typing 'q' in the xf query must not quit the palette")
	}
	if m.xfQuery.Value() != "q" {
		t.Errorf("expected 'q' in the xf query, got %q", m.xfQuery.Value())
	}
}

// TestHelpBarsRenderedFromEntries spot-checks that each phase's rendered bar
// contains exactly its generated entries' labels (View-level generation proof).
func TestHelpBarsRenderedFromEntries(t *testing.T) {
	m := New("test-session", testCommands)
	m.width = 400
	m.height = 40
	m.tier = layout.TierForWidth(m.width)
	m.syncListViewport()

	cases := []struct {
		name    string
		bar     string
		entries []helpEntry
	}{
		{"command", stripANSI(m.renderHelpBar()), commandHelpEntries(m.tier, m.listViewport.TotalLineCount() > m.listViewport.Height)},
		{"target", stripANSI(m.renderTargetHelpBar()), targetHelpEntries()},
		{"select-agents", stripANSI(m.renderSelectAgentsHelpBar()), selectAgentsHelpEntries()},
		{"edit", stripANSI(m.renderEditHelpBar()), editHelpEntries()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, e := range tc.entries {
				if !strings.Contains(tc.bar, e.label) || !strings.Contains(tc.bar, e.desc) {
					t.Errorf("%s bar missing generated entry %q %q: %s", tc.name, e.label, e.desc, tc.bar)
				}
			}
		})
	}
}

// TestHelpEntryLabelsDeriveFromBindings ensures entry() labels are the joined
// Help().Key values of the bindings they document.
func TestHelpEntryLabelsDeriveFromBindings(t *testing.T) {
	e := entry("quit", commandKeys.Quit)
	if e.label != commandKeys.Quit.Help().Key {
		t.Errorf("entry label %q != binding help key %q", e.label, commandKeys.Quit.Help().Key)
	}
	pair := entry("nav", xfResultsKeys.Up, xfResultsKeys.Down)
	want := fmt.Sprintf("%s/%s", xfResultsKeys.Up.Help().Key, xfResultsKeys.Down.Help().Key)
	if pair.label != want {
		t.Errorf("pair entry label %q != %q", pair.label, want)
	}
}
