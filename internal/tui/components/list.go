package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ListItem represents an item in a list
type ListItem struct {
	ID          string
	Title       string
	Description string
	Icon        string
	Badge       string
	Disabled    bool
	Data        interface{} // Arbitrary data
}

// List is a styled list component
type List struct {
	Items        []ListItem
	Selected     int
	ShowIcons    bool
	ShowBadges   bool
	ShowNumbers  bool
	Cursor       string
	MaxVisible   int
	scrollOffset int
	Width        int

	// Styles
	ItemStyle     lipgloss.Style
	SelectedStyle lipgloss.Style
	CursorStyle   lipgloss.Style
	DisabledStyle lipgloss.Style
	DescStyle     lipgloss.Style
	BadgeStyle    lipgloss.Style
	NumberStyle   lipgloss.Style
}

// NewList creates a new list with defaults
func NewList(items []ListItem) *List {
	t := theme.Current()
	ic := icons.Current()

	return &List{
		Items:       items,
		ShowIcons:   true,
		ShowBadges:  true,
		ShowNumbers: false,
		Cursor:      ic.Pointer,
		MaxVisible:  10,
		Width:       60,

		ItemStyle: lipgloss.NewStyle().
			Foreground(t.Text).
			Padding(0, 1),

		SelectedStyle: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Pink).
			Background(t.Surface0).
			Padding(0, 1),

		CursorStyle: lipgloss.NewStyle().
			Foreground(t.Pink).
			Bold(true),

		DisabledStyle: lipgloss.NewStyle().
			Foreground(t.Overlay).
			Strikethrough(true).
			Padding(0, 1),

		DescStyle: lipgloss.NewStyle().
			Foreground(t.Subtext).
			Italic(true),

		BadgeStyle: lipgloss.NewStyle().
			Foreground(t.Base).
			Background(t.Mauve).
			Padding(0, 1),

		NumberStyle: lipgloss.NewStyle().
			Foreground(t.Overlay).
			Width(3),
	}
}

// WithCursor sets a custom cursor character
func (l *List) WithCursor(cursor string) *List {
	l.Cursor = cursor
	return l
}

// WithMaxVisible sets maximum visible items (for scrolling)
func (l *List) WithMaxVisible(max int) *List {
	l.MaxVisible = max
	return l
}

// WithWidth sets the list width
func (l *List) WithWidth(width int) *List {
	l.Width = width
	return l
}

// WithNumbers enables/disables number prefixes
func (l *List) WithNumbers(show bool) *List {
	l.ShowNumbers = show
	return l
}

// WithIcons enables/disables icons
func (l *List) WithIcons(show bool) *List {
	l.ShowIcons = show
	return l
}

// MoveUp moves selection up
func (l *List) MoveUp() {
	if l.Selected > 0 {
		l.Selected--
		// Adjust scroll offset if needed
		if l.Selected < l.scrollOffset {
			l.scrollOffset = l.Selected
		}
	}
}

// MoveDown moves selection down
func (l *List) MoveDown() {
	if l.Selected < len(l.Items)-1 {
		l.Selected++
		// Adjust scroll offset if needed
		if l.Selected >= l.scrollOffset+l.MaxVisible {
			l.scrollOffset = l.Selected - l.MaxVisible + 1
		}
	}
}

// SelectByNumber selects item by number (1-indexed)
func (l *List) SelectByNumber(n int) bool {
	idx := n - 1
	if idx >= 0 && idx < len(l.Items) && !l.Items[idx].Disabled {
		l.Selected = idx
		// Ensure item is visible
		if l.Selected < l.scrollOffset {
			l.scrollOffset = l.Selected
		} else if l.Selected >= l.scrollOffset+l.MaxVisible {
			l.scrollOffset = l.Selected - l.MaxVisible + 1
		}
		return true
	}
	return false
}

// SelectedItem returns the currently selected item
func (l *List) SelectedItem() *ListItem {
	if l.Selected >= 0 && l.Selected < len(l.Items) {
		return &l.Items[l.Selected]
	}
	return nil
}

// Render renders the list to a string
func (l *List) Render() string {
	if len(l.Items) == 0 {
		t := theme.Current()
		emptyStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
		return emptyStyle.Render("  No items")
	}

	var lines []string

	// Calculate visible range
	start := l.scrollOffset
	end := start + l.MaxVisible
	if end > len(l.Items) {
		end = len(l.Items)
	}

	// Show scroll indicator at top if needed
	if start > 0 {
		ic := icons.Current()
		t := theme.Current()
		scrollStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		lines = append(lines, scrollStyle.Render(fmt.Sprintf("  %s %d more above", ic.ArrowUp, start)))
	}

	for i := start; i < end; i++ {
		item := l.Items[i]
		isSelected := i == l.Selected

		var line strings.Builder

		// Cursor
		if isSelected {
			line.WriteString(l.CursorStyle.Render(l.Cursor + " "))
		} else {
			line.WriteString("  ")
		}

		// Number
		if l.ShowNumbers {
			num := fmt.Sprintf("%d", i+1)
			if i < 9 {
				line.WriteString(l.NumberStyle.Render(num + " "))
			} else {
				line.WriteString(l.NumberStyle.Render(num))
			}
		}

		// Icon
		if l.ShowIcons && item.Icon != "" {
			if isSelected {
				line.WriteString(l.SelectedStyle.Render(item.Icon + " "))
			} else if item.Disabled {
				line.WriteString(l.DisabledStyle.Render(item.Icon + " "))
			} else {
				line.WriteString(l.ItemStyle.Render(item.Icon + " "))
			}
		}

		// Title
		var titleStyle lipgloss.Style
		if isSelected {
			titleStyle = l.SelectedStyle
		} else if item.Disabled {
			titleStyle = l.DisabledStyle
		} else {
			titleStyle = l.ItemStyle
		}
		line.WriteString(titleStyle.Render(item.Title))

		// Badge
		if l.ShowBadges && item.Badge != "" {
			line.WriteString(" ")
			line.WriteString(l.BadgeStyle.Render(item.Badge))
		}

		lines = append(lines, line.String())

		// Description (if selected and has description)
		if isSelected && item.Description != "" {
			desc := l.DescStyle.Render("    " + item.Description)
			lines = append(lines, desc)
		}
	}

	// Show scroll indicator at bottom if needed
	remaining := len(l.Items) - end
	if remaining > 0 {
		ic := icons.Current()
		t := theme.Current()
		scrollStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		lines = append(lines, scrollStyle.Render(fmt.Sprintf("  %s %d more below", ic.ArrowDown, remaining)))
	}

	return strings.Join(lines, "\n")
}

// String implements fmt.Stringer
func (l *List) String() string {
	return l.Render()
}

// GroupedList represents a list with category groupings
type GroupedList struct {
	Groups       []ListGroup
	Selected     int
	flatItems    []groupedItem
	ShowIcons    bool
	ShowBadges   bool
	ShowNumbers  bool
	Cursor       string
	MaxVisible   int
	scrollOffset int
	Width        int

	// Styles
	GroupStyle    lipgloss.Style
	ItemStyle     lipgloss.Style
	SelectedStyle lipgloss.Style
	CursorStyle   lipgloss.Style
	DescStyle     lipgloss.Style
}

// ListGroup represents a category of items
type ListGroup struct {
	Name  string
	Icon  string
	Items []ListItem
}

type groupedItem struct {
	item       *ListItem
	groupIndex int
	itemIndex  int
}

// NewGroupedList creates a grouped list
func NewGroupedList(groups []ListGroup) *GroupedList {
	t := theme.Current()
	ic := icons.Current()

	gl := &GroupedList{
		Groups:      groups,
		ShowIcons:   true,
		ShowBadges:  true,
		ShowNumbers: true,
		Cursor:      ic.Pointer,
		MaxVisible:  15,
		Width:       60,

		GroupStyle: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Lavender).
			Padding(0, 0, 0, 1),

		ItemStyle: lipgloss.NewStyle().
			Foreground(t.Text).
			Padding(0, 1),

		SelectedStyle: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Pink).
			Background(t.Surface0).
			Padding(0, 1),

		CursorStyle: lipgloss.NewStyle().
			Foreground(t.Pink).
			Bold(true),

		DescStyle: lipgloss.NewStyle().
			Foreground(t.Subtext).
			Italic(true),
	}

	// Flatten items for easier navigation
	gl.flattenItems()
	return gl
}

func (gl *GroupedList) flattenItems() {
	gl.flatItems = nil
	for gi, group := range gl.Groups {
		for ii := range group.Items {
			gl.flatItems = append(gl.flatItems, groupedItem{
				item:       &gl.Groups[gi].Items[ii],
				groupIndex: gi,
				itemIndex:  ii,
			})
		}
	}
}

// MoveUp moves selection up
func (gl *GroupedList) MoveUp() {
	if gl.Selected > 0 {
		gl.Selected--
		if gl.Selected < gl.scrollOffset {
			gl.scrollOffset = gl.Selected
		}
	}
}

// MoveDown moves selection down
func (gl *GroupedList) MoveDown() {
	if gl.Selected < len(gl.flatItems)-1 {
		gl.Selected++
		if gl.Selected >= gl.scrollOffset+gl.MaxVisible {
			gl.scrollOffset = gl.Selected - gl.MaxVisible + 1
		}
	}
}

// SelectByNumber selects item by number (1-indexed)
func (gl *GroupedList) SelectByNumber(n int) bool {
	idx := n - 1
	if idx >= 0 && idx < len(gl.flatItems) {
		gl.Selected = idx
		if gl.Selected < gl.scrollOffset {
			gl.scrollOffset = gl.Selected
		} else if gl.Selected >= gl.scrollOffset+gl.MaxVisible {
			gl.scrollOffset = gl.Selected - gl.MaxVisible + 1
		}
		return true
	}
	return false
}

// SelectedItem returns the currently selected item
func (gl *GroupedList) SelectedItem() *ListItem {
	if gl.Selected >= 0 && gl.Selected < len(gl.flatItems) {
		return gl.flatItems[gl.Selected].item
	}
	return nil
}

// TotalItems returns the total number of selectable items
func (gl *GroupedList) TotalItems() int {
	return len(gl.flatItems)
}

// Render renders the grouped list
func (gl *GroupedList) Render() string {
	if len(gl.flatItems) == 0 {
		t := theme.Current()
		emptyStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
		return emptyStyle.Render("  No items")
	}

	var lines []string
	currentGroup := -1

	start := gl.scrollOffset
	end := start + gl.MaxVisible
	if end > len(gl.flatItems) {
		end = len(gl.flatItems)
	}

	if start > 0 {
		ic := icons.Current()
		t := theme.Current()
		scrollStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		lines = append(lines, scrollStyle.Render(fmt.Sprintf("  %s %d more above", ic.ArrowUp, start)))
	}

	for i := start; i < end; i++ {
		fi := gl.flatItems[i]
		// Add group header if entering new group
		if fi.groupIndex != currentGroup {
			currentGroup = fi.groupIndex
			group := gl.Groups[fi.groupIndex]

			if len(lines) > 0 && !strings.Contains(lines[len(lines)-1], "more above") {
				lines = append(lines, "") // Spacing between groups
			}

			header := group.Name
			if group.Icon != "" {
				header = group.Icon + " " + header
			}
			lines = append(lines, gl.GroupStyle.Render(header))
		}

		isSelected := i == gl.Selected
		item := fi.item
		itemNum := i + 1

		var line strings.Builder

		// Cursor
		if isSelected {
			line.WriteString(gl.CursorStyle.Render(gl.Cursor + " "))
		} else {
			line.WriteString("   ")
		}

		// Number (show 1-9 for quick selection)
		if gl.ShowNumbers && itemNum <= 9 {
			t := theme.Current()
			numStyle := lipgloss.NewStyle().Foreground(t.Overlay)
			line.WriteString(numStyle.Render(fmt.Sprintf("%d ", itemNum)))
		} else {
			line.WriteString("  ")
		}

		// Icon
		if gl.ShowIcons && item.Icon != "" {
			if isSelected {
				line.WriteString(gl.SelectedStyle.Render(item.Icon + " "))
			} else {
				line.WriteString(gl.ItemStyle.Render(item.Icon + " "))
			}
		}

		// Title
		if isSelected {
			line.WriteString(gl.SelectedStyle.Render(item.Title))
		} else {
			line.WriteString(gl.ItemStyle.Render(item.Title))
		}

		lines = append(lines, line.String())
	}

	remaining := len(gl.flatItems) - end
	if remaining > 0 {
		ic := icons.Current()
		t := theme.Current()
		scrollStyle := lipgloss.NewStyle().Foreground(t.Overlay)
		lines = append(lines, scrollStyle.Render(fmt.Sprintf("  %s %d more below", ic.ArrowDown, remaining)))
	}

	return strings.Join(lines, "\n")
}

// String implements fmt.Stringer
func (gl *GroupedList) String() string {
	return gl.Render()
}
