package palette

import "github.com/Dicklesworthstone/ntm/internal/config"

// New creates a palette model with default options (test-only convenience
// constructor; production uses NewWithOptions).
func New(session string, commands []config.PaletteCmd) Model {
	return NewWithOptions(session, commands, Options{})
}
