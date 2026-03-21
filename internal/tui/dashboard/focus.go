package dashboard

import (
	"log"
	"os"
)

// FocusTarget describes a focusable dashboard panel.
type FocusTarget struct {
	ID      string
	Panel   PanelID
	Visible func() bool
}

// FocusRing tracks panel focus while layouts change.
type FocusRing struct {
	targets []FocusTarget
	current int
	prevID  string
}

// NewFocusRing creates a focus ring and selects the first visible target.
func NewFocusRing(targets []FocusTarget) FocusRing {
	var ring FocusRing
	ring.Rebuild(targets)
	return ring
}

// Current returns the currently focused target, if any.
func (fr *FocusRing) Current() FocusTarget {
	if len(fr.targets) == 0 {
		return FocusTarget{}
	}
	if fr.current < 0 || fr.current >= len(fr.targets) {
		return FocusTarget{}
	}
	return fr.targets[fr.current]
}

// Next moves focus forward, skipping hidden targets.
func (fr *FocusRing) Next() {
	fr.step(1)
}

// Prev moves focus backward, skipping hidden targets.
func (fr *FocusRing) Prev() {
	fr.step(-1)
}

// SetByID focuses a target by ID when it is visible.
func (fr *FocusRing) SetByID(id string) bool {
	for i := range fr.targets {
		if fr.targets[i].ID == id && fr.isVisible(i) {
			fr.current = i
			fr.prevID = id
			return true
		}
	}
	return false
}

// Rebuild refreshes the target set while preserving focus by ID when possible.
func (fr *FocusRing) Rebuild(targets []FocusTarget) {
	currentID := fr.currentID()
	fr.targets = targets
	fr.current = 0

	if fr.prevID != "" && fr.SetByID(fr.prevID) {
		return
	}
	if currentID != "" && fr.SetByID(currentID) {
		return
	}
	if idx, ok := fr.firstVisibleIndex(); ok {
		fr.current = idx
		fr.prevID = fr.targets[idx].ID
		return
	}
	if len(fr.targets) == 0 {
		fr.prevID = ""
		return
	}
	fr.current = 0
	fr.prevID = fr.targets[0].ID
}

func (fr *FocusRing) step(dir int) {
	if len(fr.targets) == 0 {
		return
	}

	start := fr.current
	if start < 0 || start >= len(fr.targets) {
		start = 0
	}
	if !fr.isVisible(start) {
		if idx, ok := fr.firstVisibleIndex(); ok {
			start = idx
		}
	}

	idx := start
	for range fr.targets {
		idx = (idx + dir + len(fr.targets)) % len(fr.targets)
		if fr.isVisible(idx) {
			fr.current = idx
			fr.prevID = fr.targets[idx].ID
			return
		}
	}

	fr.current = start
	if start >= 0 && start < len(fr.targets) {
		fr.prevID = fr.targets[start].ID
	}
}

func (fr *FocusRing) isVisible(index int) bool {
	if index < 0 || index >= len(fr.targets) {
		return false
	}
	if fr.targets[index].Visible == nil {
		return true
	}
	return fr.targets[index].Visible()
}

func (fr *FocusRing) firstVisibleIndex() (int, bool) {
	for i := range fr.targets {
		if fr.isVisible(i) {
			return i, true
		}
	}
	return 0, false
}

func (fr *FocusRing) currentID() string {
	if len(fr.targets) == 0 {
		return fr.prevID
	}
	if fr.current < 0 || fr.current >= len(fr.targets) {
		return fr.prevID
	}
	return fr.targets[fr.current].ID
}

func panelIDString(id PanelID) string {
	switch id {
	case PanelPaneList:
		return "panes"
	case PanelDetail:
		return "detail"
	case PanelBeads:
		return "beads"
	case PanelAlerts:
		return "alerts"
	case PanelAttention:
		return "attention"
	case PanelConflicts:
		return "conflicts"
	case PanelMetrics:
		return "metrics"
	case PanelHistory:
		return "history"
	case PanelSidebar:
		return "sidebar"
	default:
		return ""
	}
}

func (m *Model) buildFocusTargets() []FocusTarget {
	visible := m.visiblePanelsForHelpVerbosity()
	targets := make([]FocusTarget, 0, len(visible))
	for _, panel := range visible {
		targets = append(targets, FocusTarget{
			ID:    panelIDString(panel),
			Panel: panel,
		})
	}
	return targets
}

func (m *Model) syncFocusRing() {
	m.rebuildFocusRing(true)
}

func (m *Model) refreshFocusRing() {
	m.rebuildFocusRing(false)
}

func (m *Model) rebuildFocusRing(logChanges bool) {
	targets := m.buildFocusTargets()
	previousID := panelIDString(m.focusedPanel)
	removed := previousID != "" && !focusTargetsContain(targets, previousID)

	m.focusRing.prevID = previousID
	m.focusRing.Rebuild(targets)

	current := m.focusRing.Current()
	if current.ID == "" {
		m.focusedPanel = PanelPaneList
		return
	}

	if logChanges {
		logFocusf("focus ring: rebuilt with %d targets (%d visible)", len(targets), countVisibleTargets(targets))
		if removed && previousID != current.ID {
			logFocusf("focus ring: %s removed, falling back to %s", previousID, current.ID)
		}
	}
	m.focusedPanel = current.Panel
}

func (m *Model) setFocusedPanel(panel PanelID) bool {
	targetID := panelIDString(panel)
	if targetID == "" {
		return false
	}
	m.refreshFocusRing()
	if !m.focusRing.SetByID(targetID) {
		return false
	}
	previous := m.focusedPanel
	m.focusedPanel = panel
	if previous != panel {
		logFocusf("focus: %s -> %s", panelIDString(previous), targetID)
	}
	return true
}

func countVisibleTargets(targets []FocusTarget) int {
	count := 0
	for i := range targets {
		if targets[i].Visible == nil || targets[i].Visible() {
			count++
		}
	}
	return count
}

func focusTargetsContain(targets []FocusTarget, id string) bool {
	for _, target := range targets {
		if target.ID == id {
			return true
		}
	}
	return false
}

func logFocusf(format string, args ...any) {
	if os.Getenv("NTM_DEBUG") == "1" {
		log.Printf(format, args...)
	}
}
