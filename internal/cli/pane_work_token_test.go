package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// withSemanticStamp temporarily sets the package-level cfg to enable/disable
// dispatch-time stamping, restoring the prior value afterward.
func withSemanticStamp(t *testing.T, enabled bool) {
	t.Helper()
	prev := cfg
	t.Cleanup(func() { cfg = prev })
	c := config.Default()
	c.Robot.Semantic.Stamp = enabled
	cfg = c
}

func TestStampMarchingOrders_NoOpWhenDisabled(t *testing.T) {
	withSemanticStamp(t, false)
	in := "do the work"
	got := stampMarchingOrders(in, "sess", 0, 1)
	if got != in {
		t.Fatalf("with stamping OFF the prompt must be byte-identical; got %q", got)
	}
}

func TestStampMarchingOrders_NilConfigIsNoOp(t *testing.T) {
	prev := cfg
	t.Cleanup(func() { cfg = prev })
	cfg = nil
	in := "do the work"
	if got := stampMarchingOrders(in, "sess", 0, 1); got != in {
		t.Fatalf("nil cfg must be a no-op; got %q", got)
	}
}

func TestStampMarchingOrders_AppendsTokenWhenEnabled(t *testing.T) {
	withSemanticStamp(t, true)
	in := "do the work"
	got := stampMarchingOrders(in, "sess", 1, 2)
	if !strings.HasPrefix(got, in) {
		t.Fatalf("stamp must preserve the original prompt as a prefix; got %q", got)
	}
	if !strings.Contains(got, "NTM-Pane: sess/1.2") {
		t.Fatalf("stamped prompt must contain the canonical token; got %q", got)
	}
}

func TestStampMarchingOrders_Idempotent(t *testing.T) {
	withSemanticStamp(t, true)
	once := stampMarchingOrders("do the work", "sess", 1, 2)
	twice := stampMarchingOrders(once, "sess", 1, 2)
	if once != twice {
		t.Fatalf("stamping must be idempotent; got a second copy:\n%q", twice)
	}
	if strings.Count(twice, "NTM-Pane: sess/1.2") != 1 {
		t.Fatalf("token must appear exactly once after double-stamp; got %d", strings.Count(twice, "NTM-Pane: sess/1.2"))
	}
}

func TestSemanticStampEnabledReflectsConfig(t *testing.T) {
	withSemanticStamp(t, false)
	if semanticStampEnabled() {
		t.Fatal("expected disabled")
	}
	withSemanticStamp(t, true)
	if !semanticStampEnabled() {
		t.Fatal("expected enabled")
	}
}
