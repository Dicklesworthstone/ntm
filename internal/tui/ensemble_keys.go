package tui

import "github.com/charmbracelet/bubbles/key"

// EnsembleKeyMap defines keyboard shortcuts for ensemble dashboard workflows.
type EnsembleKeyMap struct {
	Quit           key.Binding
	Help           key.Binding
	Refresh        key.Binding
	NextMode       key.Binding
	PrevMode       key.Binding
	ZoomWindow     key.Binding
	CycleFocus     key.Binding
	StartSynthesis key.Binding
	ForceSynthesis key.Binding
	Export         key.Binding
	Copy           key.Binding
	JumpWindow     []key.Binding
}

// DefaultEnsembleKeyMap returns the default key bindings for ensemble dashboards.
func DefaultEnsembleKeyMap() EnsembleKeyMap {
	return EnsembleKeyMap{
		Quit:           key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Refresh:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		NextMode:       key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "next mode")),
		PrevMode:       key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "prev mode")),
		ZoomWindow:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "zoom")),
		CycleFocus:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "cycle focus")),
		StartSynthesis: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "synthesize")),
		ForceSynthesis: key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "force synth")),
		Export:         key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export")),
		Copy:           key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
		JumpWindow:     jumpWindowBindings(),
	}
}

// ShortHelp returns a short list of keybindings for compact help bars.
func (k EnsembleKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		k.Help,
		k.Refresh,
		k.NextMode,
		k.PrevMode,
		k.StartSynthesis,
		k.ForceSynthesis,
		k.Export,
		k.Copy,
		k.Quit,
	}
}

// FullHelp returns grouped keybindings for the full help overlay.
func (k EnsembleKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.NextMode, k.PrevMode, k.ZoomWindow, k.CycleFocus},
		{k.StartSynthesis, k.ForceSynthesis, k.Export, k.Copy},
		append([]key.Binding{k.Help, k.Refresh, k.Quit}, k.JumpWindow...),
	}
}

func jumpWindowBindings() []key.Binding {
	bindings := make([]key.Binding, 0, 9)
	for i := 1; i <= 9; i++ {
		keyStr := string(rune('0' + i))
		bindings = append(bindings, key.NewBinding(key.WithKeys(keyStr), key.WithHelp(keyStr, "jump")))
	}
	return bindings
}
