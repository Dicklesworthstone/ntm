package output

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// StepStatus represents the outcome of a step.
type StepStatus int

const (
	StepPending StepStatus = iota
	StepRunning
	StepSuccess
	StepWarning
	StepFailed
	StepSkipped
)

// Step represents a single step in a multi-step operation.
// Use for progress indication during long CLI operations.
type Step struct {
	name   string
	w      io.Writer
	status StepStatus
}

// Steps manages a sequence of steps for long operations.
// Provides step-oriented progress output with consistent styling.
type Steps struct {
	w         io.Writer
	current   *Step
	completed int
	total     int
	useColor  bool
	indent    string
}

// NewSteps creates a new step tracker for stdout.
// Use for long operations like spawn, upgrade, quick.
func NewSteps() *Steps {
	return NewStepsWriter(os.Stdout)
}

// NewStepsWriter creates a step tracker writing to a specific writer.
func NewStepsWriter(w io.Writer) *Steps {
	useColor := false
	if f, ok := w.(*os.File); ok {
		useColor = term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
	}
	return &Steps{
		w:        w,
		useColor: useColor,
		indent:   "  ",
	}
}

// Start begins a new named step, auto-completing any running step.
func (s *Steps) Start(name string) *Steps {
	// Auto-complete previous step if still running
	if s.current != nil && s.current.status == StepRunning {
		s.Done()
	}

	s.current = &Step{name: name, w: s.w, status: StepRunning}

	// Build step prefix
	prefix := s.indent
	if s.total > 0 {
		s.completed++
		prefix += fmt.Sprintf("[%d/%d] ", s.completed, s.total)
	}

	// Print step start
	fmt.Fprintf(s.w, "%s%s... ", prefix, name)
	return s
}

// Done marks the current step as successful.
// Prints "✓" (or "[OK]" without color).
func (s *Steps) Done() *Steps {
	if s.current == nil {
		return s
	}
	s.current.status = StepSuccess
	s.printStatus("✓", "OK", s.successStyle())
	return s
}

// Fail marks the current step as failed.
// Prints "✗" (or "[FAIL]" without color).
func (s *Steps) Fail() *Steps {
	if s.current == nil {
		return s
	}
	s.current.status = StepFailed
	s.printStatus("✗", "FAIL", s.errorStyle())
	return s
}

// Warn marks the current step as completed with warnings.
// Prints "⚠" (or "[WARN]" without color).
func (s *Steps) Warn() *Steps {
	if s.current == nil {
		return s
	}
	s.current.status = StepWarning
	s.printStatus("⚠", "WARN", s.warnStyle())
	return s
}

func (s *Steps) printStatus(icon, text string, style lipgloss.Style) {
	if s.useColor {
		fmt.Fprintln(s.w, style.Render(icon))
	} else {
		fmt.Fprintf(s.w, "[%s]\n", text)
	}
}

// Style helpers
func (s *Steps) successStyle() lipgloss.Style {
	t := theme.Current()
	return lipgloss.NewStyle().Foreground(t.Success)
}

func (s *Steps) errorStyle() lipgloss.Style {
	t := theme.Current()
	return lipgloss.NewStyle().Foreground(t.Error)
}

func (s *Steps) warnStyle() lipgloss.Style {
	t := theme.Current()
	return lipgloss.NewStyle().Foreground(t.Warning)
}

// ===========================================================================
// One-shot progress messages
// ===========================================================================

// ProgressMsg prints a status message with consistent styling.
// Use for reporting progress without step tracking.
type ProgressMsg struct {
	w        io.Writer
	useColor bool
	indent   string
}

// Progress returns a ProgressMsg for stdout.
func Progress() *ProgressMsg {
	return ProgressWriter(os.Stdout)
}

// ProgressWriter returns a ProgressMsg for a specific writer.
func ProgressWriter(w io.Writer) *ProgressMsg {
	useColor := false
	if f, ok := w.(*os.File); ok {
		useColor = term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
	}
	return &ProgressMsg{w: w, useColor: useColor, indent: ""}
}

// Success prints "✓ message".
func (p *ProgressMsg) Success(msg string) {
	p.printWithIcon("✓", msg, p.successStyle())
}

// Successf prints "✓ formatted message".
func (p *ProgressMsg) Successf(format string, args ...any) {
	p.Success(fmt.Sprintf(format, args...))
}

// Warning prints "⚠ message".
func (p *ProgressMsg) Warning(msg string) {
	p.printWithIcon("⚠", msg, p.warnStyle())
}

// Warningf prints "⚠ formatted message".
func (p *ProgressMsg) Warningf(format string, args ...any) {
	p.Warning(fmt.Sprintf(format, args...))
}

// Info prints "ℹ message".
func (p *ProgressMsg) Info(msg string) {
	p.printWithIcon("ℹ", msg, p.infoStyle())
}

// Infof prints "ℹ formatted message".
func (p *ProgressMsg) Infof(format string, args ...any) {
	p.Info(fmt.Sprintf(format, args...))
}

func (p *ProgressMsg) printWithIcon(icon, msg string, style lipgloss.Style) {
	if p.useColor {
		fmt.Fprintf(p.w, "%s%s %s\n", p.indent, style.Render(icon), msg)
	} else {
		fmt.Fprintf(p.w, "%s%s %s\n", p.indent, icon, msg)
	}
}

func (p *ProgressMsg) successStyle() lipgloss.Style {
	t := theme.Current()
	return lipgloss.NewStyle().Foreground(t.Success)
}

func (p *ProgressMsg) warnStyle() lipgloss.Style {
	t := theme.Current()
	return lipgloss.NewStyle().Foreground(t.Warning)
}

func (p *ProgressMsg) infoStyle() lipgloss.Style {
	t := theme.Current()
	return lipgloss.NewStyle().Foreground(t.Info)
}

// ===========================================================================
// Convenience functions (use default Progress())
// ===========================================================================

// PrintSuccess prints "✓ message" to stdout.
func PrintSuccess(msg string) {
	Progress().Success(msg)
}

// PrintSuccessf prints "✓ formatted message" to stdout.
func PrintSuccessf(format string, args ...any) {
	Progress().Successf(format, args...)
}

// PrintWarning prints "⚠ message" to stdout.
func PrintWarning(msg string) {
	Progress().Warning(msg)
}

// PrintWarningf prints "⚠ formatted message" to stdout.
func PrintWarningf(format string, args ...any) {
	Progress().Warningf(format, args...)
}

// PrintInfo prints "ℹ message" to stdout.
func PrintInfo(msg string) {
	Progress().Info(msg)
}

// PrintInfof prints "ℹ formatted message" to stdout.
func PrintInfof(format string, args ...any) {
	Progress().Infof(format, args...)
}

// ===========================================================================
// Multi-step operation helpers
// ===========================================================================
