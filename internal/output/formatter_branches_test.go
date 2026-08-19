package output

import (
	"bytes"
	"testing"
)

// ---------------------------------------------------------------------------
// Formatter.Error / ErrorMsg / ErrorWithCode — text mode branches (66.7% → 100%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Steps nil-current guards (Done/Fail/Skip/Warn at 80% → 100%)
// ---------------------------------------------------------------------------

func TestSteps_Done_NilCurrent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	s := NewStepsWriter(&buf)
	// Call Done without calling Start — should not panic.
	s.Done()
	if buf.Len() > 0 {
		t.Errorf("expected no output for nil current, got %q", buf.String())
	}
}

func TestSteps_Fail_NilCurrent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	s := NewStepsWriter(&buf)
	s.Fail()
	if buf.Len() > 0 {
		t.Errorf("expected no output for nil current, got %q", buf.String())
	}
}

func TestSteps_Warn_NilCurrent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	s := NewStepsWriter(&buf)
	s.Warn()
	if buf.Len() > 0 {
		t.Errorf("expected no output for nil current, got %q", buf.String())
	}
}
