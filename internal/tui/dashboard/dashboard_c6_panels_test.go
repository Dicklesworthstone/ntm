package dashboard

// Tests for the C6-wire dashboard panels: quota, ratelimit, accounts.
// [reality-bridge: bd-ws2-wire-or-delete-ykmcz.6]
//
// Convention: model View()/renderSidebar() output assertions with fixture
// data (see panels/metrics_test.go / TestDashboardWorkflowToggle...). Each
// test logs the panel's data-fetch result and rendered dimensions so a blank
// panel is diagnosable from the CI log.

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
)

func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestDashboardQuotaToggleShowsPanelWithFixtureData(t *testing.T) {
	oldFetch := dashboardFetchQuotaData
	defer func() { dashboardFetchQuotaData = oldFetch }()

	fixture := panels.QuotaData{
		Status: &tools.CautStatus{
			Running:      true,
			Tracking:     true,
			QuotaPercent: 82.5,
			TotalSpend:   12.34,
		},
		Usages: []tools.CautUsage{
			{Provider: "claude", RequestCount: 42, TokensIn: 9000, TokensOut: 6000, Cost: 4.2, Period: "day"},
		},
		Available: true,
	}
	dashboardFetchQuotaData = func() panels.QuotaData { return fixture }

	m := newTestModel(140)

	updated, cmd := m.Update(keyRune('$'))
	next := updated.(Model)
	if !next.showQuotaPanel {
		t.Fatal("expected '$' to show the quota panel")
	}
	if cmd == nil {
		t.Fatal("expected quota fetch command on toggle")
	}

	msg := cmd()
	quotaMsg, ok := msg.(QuotaStatusMsg)
	if !ok {
		t.Fatalf("fetch returned %T, want QuotaStatusMsg", msg)
	}
	t.Logf("quota data-fetch result: available=%v err=%v status=%+v usages=%d",
		quotaMsg.Data.Available, quotaMsg.Data.Error, quotaMsg.Data.Status, len(quotaMsg.Data.Usages))

	updated, _ = next.Update(quotaMsg)
	next = updated.(Model)

	rendered := status.StripANSI(next.renderSidebar(90, 30))
	t.Logf("quota panel rendered dimensions: %dx%d (sidebar 90x30)",
		lipgloss.Width(rendered), lipgloss.Height(rendered))

	for _, want := range []string{"Usage & Costs", "82.5% used", "$12.34 today", "claude", "15k tok", "$4.20"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("quota panel missing %q in sidebar:\n%s", want, rendered)
		}
	}

	// Toggling again hides the panel.
	updated, _ = next.Update(keyRune('$'))
	next = updated.(Model)
	if next.showQuotaPanel {
		t.Fatal("expected second '$' to hide the quota panel")
	}
	if got := status.StripANSI(next.renderSidebar(90, 30)); strings.Contains(got, "Usage & Costs") {
		t.Fatalf("quota panel still rendered after toggle off:\n%s", got)
	}
}

func TestDashboardRateLimitToggleShowsPanelWithFixtureData(t *testing.T) {
	oldFetch := dashboardFetchOAuthHealth
	defer func() { dashboardFetchOAuthHealth = oldFetch }()

	fixture := []robot.AgentOAuthHealth{
		{
			Pane: 1, AgentType: "claude", Provider: "claude",
			OAuthStatus: robot.OAuthValid, RateLimitStatus: robot.RateLimitOK,
			LastActivitySec: 30,
		},
		{
			Pane: 2, AgentType: "codex", Provider: "openai",
			OAuthStatus: robot.OAuthExpired, RateLimitStatus: robot.RateLimitLimited,
			RateLimitCount: 4, CooldownRemaining: 42, LastActivitySec: 120,
		},
	}
	var gotSession string
	dashboardFetchOAuthHealth = func(session string) ([]robot.AgentOAuthHealth, error) {
		gotSession = session
		return fixture, nil
	}

	m := newTestModel(140)

	updated, cmd := m.Update(keyRune('o'))
	next := updated.(Model)
	if !next.showRateLimitPanel {
		t.Fatal("expected 'o' to show the ratelimit panel")
	}
	if cmd == nil {
		t.Fatal("expected oauth health fetch command on toggle")
	}

	msg := cmd()
	healthMsg, ok := msg.(OAuthHealthMsg)
	if !ok {
		t.Fatalf("fetch returned %T, want OAuthHealthMsg", msg)
	}
	if gotSession != next.session {
		t.Fatalf("fetch used session %q, want %q", gotSession, next.session)
	}
	t.Logf("ratelimit data-fetch result: agents=%d err=%v", len(healthMsg.Agents), healthMsg.Err)

	updated, _ = next.Update(healthMsg)
	next = updated.(Model)

	rendered := status.StripANSI(next.renderSidebar(90, 30))
	t.Logf("ratelimit panel rendered dimensions: %dx%d (sidebar 90x30)",
		lipgloss.Width(rendered), lipgloss.Height(rendered))

	for _, want := range []string{"OAuth/Rate Status", "cc-1", "cod-2", "Last=2m", "(42s)", "(4 limits)"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("ratelimit panel missing %q in sidebar:\n%s", want, rendered)
		}
	}

	updated, _ = next.Update(keyRune('o'))
	next = updated.(Model)
	if next.showRateLimitPanel {
		t.Fatal("expected second 'o' to hide the ratelimit panel")
	}
}

func TestDashboardAccountsToggleShowsPanelWithFixtureData(t *testing.T) {
	oldFetch := dashboardFetchAccountsStatus
	defer func() { dashboardFetchAccountsStatus = oldFetch }()

	fixture := &tools.CAAMStatus{
		Available:     true,
		AccountsCount: 3,
		Providers:     []string{"claude", "openai"},
		Accounts: []tools.CAAMAccount{
			{ID: "acc-1", Provider: "claude", Email: "ops@example.com", Active: true},
			{ID: "acc-2", Provider: "claude", RateLimited: true},
			{ID: "acc-3", Provider: "openai", Name: "backup", Active: true},
		},
	}
	dashboardFetchAccountsStatus = func(ctx context.Context) (*tools.CAAMStatus, error) {
		return fixture, nil
	}

	m := newTestModel(140)

	updated, cmd := m.Update(keyRune('a'))
	next := updated.(Model)
	if !next.showAccountsPanel {
		t.Fatal("expected 'a' to show the accounts panel")
	}
	if cmd == nil {
		t.Fatal("expected accounts fetch command on toggle")
	}

	msg := cmd()
	accountsMsg, ok := msg.(AccountsStatusMsg)
	if !ok {
		t.Fatalf("fetch returned %T, want AccountsStatusMsg", msg)
	}
	t.Logf("accounts data-fetch result: available=%v err=%v accounts=%d",
		accountsMsg.Data.Available, accountsMsg.Data.Error, len(fixture.Accounts))

	updated, _ = next.Update(accountsMsg)
	next = updated.(Model)

	rendered := status.StripANSI(next.renderSidebar(90, 30))
	t.Logf("accounts panel rendered dimensions: %dx%d (sidebar 90x30)",
		lipgloss.Width(rendered), lipgloss.Height(rendered))

	for _, want := range []string{"Accounts", "3 accounts (2 providers)", "ops@example.com", "[1 cooldown]"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("accounts panel missing %q in sidebar:\n%s", want, rendered)
		}
	}

	updated, _ = next.Update(keyRune('a'))
	next = updated.(Model)
	if next.showAccountsPanel {
		t.Fatal("expected second 'a' to hide the accounts panel")
	}
}

func TestDashboardAccountsToggleYieldsToHistoryPanel(t *testing.T) {
	m := newTestModel(140)
	m.focusedPanel = PanelHistory

	updated, _ := m.Update(keyRune('a'))
	next := updated.(Model)
	if next.showAccountsPanel {
		t.Fatal("expected 'a' to be owned by the focused history panel, not the accounts toggle")
	}
}

func TestDashboardC6PanelsJoinSidebarNavigation(t *testing.T) {
	m := newTestModel(140)

	// Hidden panels must not appear in the sidebar focus cycle.
	for _, ref := range m.sidebarInteractivePanels() {
		switch ref.id {
		case m.quotaPanel.Config().ID, m.rateLimitPanel.Config().ID, m.accountsPanel.Config().ID:
			t.Fatalf("panel %q registered for navigation while hidden", ref.id)
		}
	}

	m.showQuotaPanel = true
	m.showRateLimitPanel = true
	m.showAccountsPanel = true
	m.focusedPanel = PanelSidebar

	refs := m.sidebarInteractivePanels()
	visited := map[string]bool{}
	next := m
	for range refs {
		updated, _ := next.Update(keyRune('J'))
		next = updated.(Model)
		visited[next.sidebarActivePanelID()] = true
	}
	for _, id := range []string{
		m.quotaPanel.Config().ID,
		m.rateLimitPanel.Config().ID,
		m.accountsPanel.Config().ID,
	} {
		if !visited[id] {
			t.Errorf("sidebar 'J' navigation never reached panel %q (visited %v)", id, visited)
		}
	}
}

func TestDashboardHelpListsC6PanelToggles(t *testing.T) {
	if got := dashKeys.QuotaToggle.Keys(); len(got) != 1 || got[0] != "$" {
		t.Fatalf("QuotaToggle bound to %v, want [$]", got)
	}
	if got := dashKeys.RateLimitToggle.Keys(); len(got) != 1 || got[0] != "o" {
		t.Fatalf("RateLimitToggle bound to %v, want [o]", got)
	}
	if got := dashKeys.AccountsToggle.Keys(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("AccountsToggle bound to %v, want [a]", got)
	}

	// Help-text consistency: every toggle must be listed in the full help
	// overlay with its bound key.
	wantHelp := map[string]string{
		"$": dashKeys.QuotaToggle.Help().Desc,
		"o": dashKeys.RateLimitToggle.Help().Desc,
		"a": dashKeys.AccountsToggle.Help().Desc,
	}
	listed := map[string]string{}
	for _, row := range dashKeys.FullHelp() {
		for _, binding := range row {
			listed[binding.Help().Key] = binding.Help().Desc
		}
	}
	for k, desc := range wantHelp {
		if listed[k] != desc {
			t.Errorf("full help missing toggle %q (%q); got %q", k, desc, listed[k])
		}
	}
}
