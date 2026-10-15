package cass

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLimitedOutputCopyCannotBypassBudget(t *testing.T) {
	calls := 0
	out := newLimitedOutput(4, func() { calls++ })
	// Hide Reader.WriteTo so io.Copy must also consider destination.ReadFrom.
	// Embedding bytes.Buffer would expose ReadFrom and silently bypass Write.
	src := struct{ io.Reader }{strings.NewReader("123456789")}
	n, err := io.Copy(out, src)
	if n != 4 || !errors.Is(err, ErrOutputLimit) || out.String() != "1234" || calls != 1 || !out.exceeded {
		t.Fatalf("copy exceeded bound: n=%d err=%v bytes=%q cancels=%d", n, err, out.String(), calls)
	}
	if n, err := out.Write([]byte("more")); n != 0 || !errors.Is(err, ErrOutputLimit) || calls != 1 {
		t.Fatal("an overflowed writer accepted more bytes or repeatedly cancelled")
	}
}

func TestLimitedOutputExactBoundary(t *testing.T) {
	out := newLimitedOutput(4, func() { t.Fatal("exact-boundary output cancelled") })
	for _, text := range []string{"12", "", "34", ""} {
		if n, err := out.Write([]byte(text)); n != len(text) || err != nil {
			t.Fatalf("valid output refused: %d %v", n, err)
		}
	}
	if out.exceeded || string(out.Bytes()) != "1234" {
		t.Fatal("exact-boundary output corrupted")
	}
}
