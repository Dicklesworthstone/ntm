package dashboard

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
)

var (
	dashboardNow = func() time.Time { return time.Now().UTC() }
	// dashboardZoomPane zooms the target pane using its physical tmux ID (%N)
	// when available so multi-window sessions zoom the exact pane across windows.
	dashboardZoomPane = func(session string, pane tmux.Pane) error {
		if strings.HasPrefix(pane.ID, "%") {
			return tmux.ZoomPaneByID(pane.ID)
		}
		return tmux.ZoomPane(session, pane.Index)
	}
	dashboardDisplayMessage  = tmux.DisplayMessage
	dashboardPublishBusEvent = ntmevents.PublishSync
)

func overlayZoomHint(cursor int64) string {
	hint := "F12 → dashboard overlay · prefix+z → unzoom"
	if cursor > 0 {
		return fmt.Sprintf("%s · cursor:%d", hint, cursor)
	}
	return hint
}

func overlayDismissDurationSeconds(openedAt, now time.Time) float64 {
	if openedAt.IsZero() || now.Before(openedAt) {
		return 0
	}
	return now.Sub(openedAt).Seconds()
}

func (m *Model) activatePopupMode(now time.Time) {
	m.popupMode = true
	if m.overlayOpenedAt.IsZero() {
		m.overlayOpenedAt = now
	}
}

func (m *Model) paneByID(paneID string) (tmux.Pane, bool) {
	for _, pane := range m.panes {
		if pane.ID != "" && pane.ID == paneID {
			return pane, true
		}
	}
	return tmux.Pane{}, false
}

func (m *Model) uniquePaneByIndex(paneIndex int) (tmux.Pane, bool, bool) {
	var matched tmux.Pane
	count := 0
	for _, pane := range m.panes {
		if pane.Index == paneIndex {
			matched = pane
			count++
		}
	}
	if count == 1 {
		return matched, true, false
	}
	return tmux.Pane{}, false, count > 1
}

func (m *Model) publishHumanZoomEvent(pane tmux.Pane, cursor int64) {
	details := map[string]string{
		"pane_index": strconv.Itoa(pane.Index),
	}
	if pane.Type != "" {
		details["agent_type"] = string(pane.Type)
	}
	if cursor > 0 {
		details["cursor"] = strconv.FormatInt(cursor, 10)
	}
	event := ntmevents.NewWebhookEvent(
		ntmevents.EventHumanZoom,
		m.session,
		strconv.Itoa(pane.Index),
		string(pane.Type),
		fmt.Sprintf("human zoomed pane %d", pane.Index),
		details,
	)
	dashboardPublishBusEvent(event)
}

func (m *Model) publishHumanOverlayDismiss(now time.Time) {
	cursor := robot.GetAttentionFeed().CurrentCursor()
	duration := overlayDismissDurationSeconds(m.overlayOpenedAt, now)
	details := map[string]string{
		"duration_seconds": strconv.FormatFloat(duration, 'f', 3, 64),
		"overlay_popup":    "true",
	}
	if !m.overlayOpenedAt.IsZero() {
		details["overlay_opened_at"] = m.overlayOpenedAt.Format(time.RFC3339Nano)
	}
	if cursor > 0 {
		details["cursor"] = strconv.FormatInt(cursor, 10)
	}
	event := ntmevents.NewWebhookEvent(
		ntmevents.EventHumanOverlayDismiss,
		m.session,
		"",
		"",
		"human dismissed dashboard overlay",
		details,
	)
	dashboardPublishBusEvent(event)
}

func (m *Model) exitPopupOverlay() tea.Cmd {
	m.postQuitAction = nil
	m.quitting = true
	m.publishHumanOverlayDismiss(dashboardNow())
	m.cleanup()
	return tea.Quit
}

func (m *Model) handlePaneZoomWithCursor(pane tmux.Pane, cursor int64) tea.Cmd {
	if err := dashboardZoomPane(m.session, pane); err != nil {
		m.healthMessage = fmt.Sprintf("Zoom failed: %v", err)
		return nil
	}
	if cursor <= 0 {
		cursor = robot.GetAttentionFeed().CurrentCursor()
	}
	if m.popupMode {
		m.publishHumanZoomEvent(pane, cursor)
		_ = dashboardDisplayMessage(m.session, overlayZoomHint(cursor), 4000)
		m.postQuitAction = nil
	} else {
		m.postQuitAction = &PostQuitAction{AttachSession: m.session}
	}
	m.quitting = true
	m.cleanup()
	return tea.Quit
}

func (m *Model) handlePaneZoom(pane tmux.Pane) tea.Cmd {
	return m.handlePaneZoomWithCursor(pane, 0)
}

// handleAttentionZoom zooms to the pane that generated the attention event.
// Refuses cross-session items, prefers physical pane IDs for multi-window
// precision, and displays a warning instead of zooming an ambiguous/missing pane.
func (m *Model) handleAttentionZoom(item panels.AttentionItem) tea.Cmd {
	itemSession := strings.TrimSpace(item.SourceSession)
	activeSession := strings.TrimSpace(m.session)
	if itemSession != "" && activeSession != "" && itemSession != activeSession {
		m.healthMessage = "Attention item belongs to another session"
		if m.toasts != nil {
			m.toasts.Push(components.Toast{
				ID:      fmt.Sprintf("attention-foreign-session-%d", item.Cursor),
				Message: "Attention item belongs to another session",
				Level:   components.ToastWarning,
			})
		}
		return nil
	}
	var (
		pane      tmux.Pane
		ok        bool
		ambiguous bool
	)
	if item.SourcePaneID != "" {
		pane, ok = m.paneByID(item.SourcePaneID)
	} else {
		pane, ok, ambiguous = m.uniquePaneByIndex(item.SourcePane)
	}
	if !ok {
		msg := "Source pane no longer available"
		if ambiguous {
			msg = "Source pane is ambiguous across windows"
		}
		m.healthMessage = msg
		if m.toasts != nil {
			m.toasts.Push(components.Toast{
				ID:      fmt.Sprintf("attention-missing-pane-%d", item.SourcePane),
				Message: msg,
				Level:   components.ToastWarning,
			})
		}
		return nil
	}
	m.setPaneListSelectionByPaneID(pane.ID)
	return m.handlePaneZoomWithCursor(pane, item.Cursor)
}

func (m *Model) requestAttentionCursor(cursor int64) {
	if cursor <= 0 {
		return
	}
	m.requestedAttentionCursor = cursor
	m.attentionCursorApplied = false
	m.focusRing.prevID = panelIDString(PanelAttention)
	_ = m.setFocusedPanel(PanelAttention)
}

func (m *Model) applyRequestedAttentionCursor() {
	if m.requestedAttentionCursor <= 0 || m.attentionCursorApplied || m.attentionPanel == nil || !m.attentionFeedOK {
		return
	}

	_ = m.setFocusedPanel(PanelAttention)
	if m.attentionPanel.SelectCursor(m.requestedAttentionCursor) {
		m.attentionCursorApplied = true
		return
	}
	if m.attentionPanel.SelectNearestCursor(m.requestedAttentionCursor) {
		m.attentionCursorApplied = true
		m.healthMessage = fmt.Sprintf(
			"Attention cursor %d no longer available; showing nearest surviving item",
			m.requestedAttentionCursor,
		)
		return
	}
	if m.attentionPanel.ItemCount() == 0 {
		m.attentionCursorApplied = true
		m.healthMessage = fmt.Sprintf(
			"Attention cursor %d unavailable; no attention items",
			m.requestedAttentionCursor,
		)
	}
}
