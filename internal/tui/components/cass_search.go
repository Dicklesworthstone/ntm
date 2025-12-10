package components

import (
	"context"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CassSearchModel manages the CASS search UI
type CassSearchModel struct {
	textInput textinput.Model
	list      list.Model
	client    *cass.Client
	width     int
	height    int
	searching bool
	err       error
	onSelect  func(cass.SearchHit) tea.Cmd
	
	// Debounce
	searchID int
}

type searchItem struct {
	hit cass.SearchHit
}

func (i searchItem) Title() string       { return i.hit.Title }
func (i searchItem) Description() string { return fmt.Sprintf("%s • %s", i.hit.Agent, i.hit.SourcePath) }
func (i searchItem) FilterValue() string { return i.hit.Title }

type searchResultMsg struct {
	id      int
	results *cass.SearchResponse
	err     error
}

type performSearchMsg struct {
	id    int
	query string
}

// NewCassSearch creates a new search model
func NewCassSearch(onSelect func(cass.SearchHit) tea.Cmd) CassSearchModel {
	ti := textinput.New()
	ti.Placeholder = "Search past sessions..."
	ti.Focus()
	ti.Prompt = "🔍 "
	ti.Width = 40

	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.SetShowHelp(false)
	l.SetShowTitle(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)
	l.SetShowStatusBar(false)

	return CassSearchModel{
		textInput: ti,
		list:      l,
		client:    cass.NewClient(),
		onSelect:  onSelect,
	}
}

// Init initializes the model
func (m CassSearchModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update updates the model
func (m CassSearchModel) Update(msg tea.Msg) (CassSearchModel, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if i, ok := m.list.SelectedItem().(searchItem); ok {
				if m.onSelect != nil {
					return m, m.onSelect(i.hit)
				}
			}
		case "down", "j":
			m.list, cmd = m.list.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		case "up", "k":
			m.list, cmd = m.list.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

	case performSearchMsg:
		if msg.id != m.searchID {
			break // Stale
		}
		if msg.query == "" {
			m.list.SetItems(nil)
			break
		}
		m.searching = true
		cmds = append(cmds, m.performSearch(msg.id, msg.query))

	case searchResultMsg:
		if msg.id != m.searchID {
			break // Stale
		}
		m.searching = false
		if msg.err != nil {
			m.err = msg.err
			// Maybe show error in list?
			break
		}
		items := make([]list.Item, len(msg.results.Hits))
		for i, hit := range msg.results.Hits {
			items[i] = searchItem{hit: hit}
		}
		m.list.SetItems(items)
	}

	// Handle input changes
	oldValue := m.textInput.Value()
	m.textInput, cmd = m.textInput.Update(msg)
	cmds = append(cmds, cmd)

	if m.textInput.Value() != oldValue {
		m.searchID++
		id := m.searchID
		query := m.textInput.Value()
		// Debounce
		cmds = append(cmds, tea.Tick(300*time.Millisecond, func(t time.Time) tea.Msg {
			return performSearchMsg{id: id, query: query}
		}))
	}

	return m, tea.Batch(cmds...)
}

func (m CassSearchModel) performSearch(id int, query string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Search(context.Background(), cass.SearchOptions{
			Query: query,
			Limit: 20,
		})
		return searchResultMsg{id: id, results: resp, err: err}
	}
}

// SetSize sets the size of the component
func (m *CassSearchModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.list.SetSize(width, height-3) // Reserve space for input
	m.textInput.Width = width - 4
}

// View renders the component
func (m CassSearchModel) View() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		m.textInput.View(),
		m.list.View(),
	)
}
