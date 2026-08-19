// Package output provides unified output formatting for text and JSON output.
// All commands should use this package for consistent output across the CLI.
package output

import (
	"io"
	"os"
)

// Format represents the output format type
type Format int

const (
	// FormatText is human-readable formatted text (default)
	FormatText Format = iota
	// FormatJSON is machine-readable JSON output
	FormatJSON
)

// Formatter handles output formatting for commands
type Formatter struct {
	format Format
	writer io.Writer
	pretty bool // For JSON: whether to indent
}

// New creates a new Formatter with the given options
func New(opts ...Option) *Formatter {
	f := &Formatter{
		format: FormatText,
		writer: os.Stdout,
		pretty: true,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// Option is a functional option for Formatter
type Option func(*Formatter)

// WithJSON sets the output format to JSON
func WithJSON(enabled bool) Option {
	return func(f *Formatter) {
		if enabled {
			f.format = FormatJSON
		} else {
			f.format = FormatText
		}
	}
}

// IsJSON returns true if the output format is JSON
func (f *Formatter) IsJSON() bool {
	return f.format == FormatJSON
}
