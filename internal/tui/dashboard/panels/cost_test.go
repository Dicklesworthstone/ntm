package panels

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/cost"
)

func TestNewCostPanel(t *testing.T) {
	panel := NewCostPanel()
	if panel == nil {
		t.Fatal("NewCostPanel returned nil")
	}

	cfg := panel.Config()
	if cfg.ID != "cost" {
		t.Errorf("Expected ID 'cost', got %q", cfg.ID)
	}
	// The title must not claim tracking: the figures are a token estimate over
	// scraped pane output priced from a static table, never provider billing.
	if cfg.Title != "Cost (estimated)" {
		t.Errorf("Expected Title 'Cost (estimated)', got %q", cfg.Title)
	}
}

func TestCostPanel_SetSize(t *testing.T) {
	panel := NewCostPanel()
	panel.SetSize(80, 24)

	if panel.Width() != 80 {
		t.Errorf("Expected Width 80, got %d", panel.Width())
	}
	if panel.Height() != 24 {
		t.Errorf("Expected Height 24, got %d", panel.Height())
	}
}

func TestCostPanel_FocusBlur(t *testing.T) {
	panel := NewCostPanel()
	if panel.IsFocused() {
		t.Error("Panel should not be focused initially")
	}

	panel.Focus()
	if !panel.IsFocused() {
		t.Error("Panel should be focused after Focus()")
	}

	panel.Blur()
	if panel.IsFocused() {
		t.Error("Panel should not be focused after Blur()")
	}
}

func TestCostPanel_SetData_Sorts(t *testing.T) {
	panel := NewCostPanel()
	panel.SetSize(60, 12)

	panel.SetData(CostPanelData{
		Agents: []CostAgentRow{
			{PaneTitle: "proj__cc_2", InputTokens: 1000, OutputTokens: 1000, CostUSD: 1.0, Trend: CostTrendUp},
			{PaneTitle: "proj__cc_1", InputTokens: 1000, OutputTokens: 1000, CostUSD: 2.0, Trend: CostTrendUp},
		},
		SessionTotalUSD: 3.0,
		LastHourUSD:     1.2,
		DailyBudgetUSD:  10,
		BudgetUsedUSD:   3.0,
	}, nil)

	if panel.data.Agents[0].PaneTitle != "proj__cc_1" {
		t.Fatalf("expected highest cost agent first, got %q", panel.data.Agents[0].PaneTitle)
	}

	view := panel.View()
	if view == "" {
		t.Fatal("expected non-empty View output")
	}
}

func TestCostPanel_HasData(t *testing.T) {
	panel := NewCostPanel()
	if panel.HasData() {
		t.Fatal("expected HasData=false initially")
	}

	panel.SetData(CostPanelData{DailyBudgetUSD: 50, BudgetUsedUSD: 1}, nil)
	if !panel.HasData() {
		t.Fatal("expected HasData=true when budget is set")
	}
}

func TestCostPanelHandlesOwnHeight(t *testing.T) {
	panel := NewCostPanel()
	if !panel.HandlesOwnHeight() {
		t.Fatal("expected cost panel to manage its own height")
	}
}

func TestCostPanelViewShowsScrollIndicatorWhenOverflowing(t *testing.T) {
	panel := NewCostPanel()
	panel.SetSize(52, 10)

	agents := make([]CostAgentRow, 0, 12)
	for i := 0; i < 12; i++ {
		agents = append(agents, CostAgentRow{
			PaneTitle:    "proj__cc_agent",
			InputTokens:  1000 + i,
			OutputTokens: 500 + i,
			CostUSD:      float64(12 - i),
			Trend:        CostTrendUp,
		})
	}

	panel.SetData(CostPanelData{
		Agents:          agents,
		SessionTotalUSD: 42,
		DailyBudgetUSD:  100,
		BudgetUsedUSD:   42,
	}, nil)

	view := panel.View()
	if !strings.Contains(view, "%") {
		t.Fatalf("expected overflowing cost panel to show percent badge, got %q", view)
	}
}

// The cost column has to hold the widest cell the panel can produce. The "~"
// prefix and the confidence marker each add a column, and a truncated currency
// figure reads as precise while being the wrong number.
func TestCostColumnFitsTheWidestCell(t *testing.T) {
	panel := NewCostPanel()
	cols := panel.costTableColumns(80)

	var costWidth int
	for _, col := range cols {
		if col.Title == "Cost" {
			costWidth = col.Width
		}
	}
	if costWidth == 0 {
		t.Fatal("no Cost column")
	}

	// Longest producible cell: a four-figure estimate priced from the default
	// row, which carries the "?" marker.
	widest := cost.FormatCostEstimate(1234.5) + "?"
	if len(widest) > costWidth {
		t.Errorf("widest cost cell %q is %d columns, but the column is %d — it will be truncated",
			widest, len(widest), costWidth)
	}
}

// GH #331: at table widths 36-43 the "In" column is dropped while "Out" stays,
// and rows used to keep an input-token cell anyway. bubbles/table indexes its
// columns by cell position, so the fifth cell panicked with "index out of range
// [4] with length 4" once three or more agents made the rows visible.
func TestCostPanel_RowsMatchColumnsAtEveryWidth(t *testing.T) {
	data := CostPanelData{Agents: []CostAgentRow{
		{PaneTitle: "cc_1", InputTokens: 12000, OutputTokens: 3400, CostUSD: 0.30},
		{PaneTitle: "cc_2", InputTokens: 9000, OutputTokens: 2100, CostUSD: 0.20},
		{PaneTitle: "cod_1", InputTokens: 5000, OutputTokens: 1500, CostUSD: 0.10},
		{PaneTitle: "gmi_1", InputTokens: 100, OutputTokens: 50, CostUSD: 0.01},
	}}

	for tableWidth := 0; tableWidth <= 80; tableWidth++ {
		panel := NewCostPanel()
		panel.SetData(data, nil)
		cols := panel.costTableColumns(tableWidth)
		rows := panel.costTableRows(cols, len(data.Agents))
		if len(rows) != len(data.Agents) {
			t.Fatalf("tableWidth %d: got %d rows, want %d", tableWidth, len(rows), len(data.Agents))
		}
		for i, row := range rows {
			if len(row) != len(cols) {
				t.Fatalf("tableWidth %d row %d: %d cells for %d columns", tableWidth, i, len(row), len(cols))
			}
			for j, col := range cols {
				var want string
				switch col.Title {
				case "In":
					want = formatTokenShort(data.Agents[i].InputTokens)
				case "Out":
					want = formatTokenShort(data.Agents[i].OutputTokens)
				default:
					continue
				}
				if row[j] != want {
					t.Errorf("tableWidth %d row %d column %q = %q, want %q", tableWidth, i, col.Title, row[j], want)
				}
			}
		}
	}
}

func TestCostPanel_ViewDoesNotPanicAtAnyWidth(t *testing.T) {
	data := CostPanelData{Agents: []CostAgentRow{
		{PaneTitle: "cc_1", CostUSD: 0.30},
		{PaneTitle: "cc_2", CostUSD: 0.20},
		{PaneTitle: "cod_1", CostUSD: 0.10},
	}}
	for width := 10; width <= 90; width++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panel width %d: View panicked: %v", width, r)
				}
			}()
			panel := NewCostPanel()
			panel.SetSize(width, 20)
			panel.SetData(data, nil)
			_ = panel.View()
		}()
	}
}
