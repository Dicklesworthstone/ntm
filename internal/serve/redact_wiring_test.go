package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// The rest of redact_test.go builds `&Server{redactionCfg: ...}` by hand, which
// exercises the middleware but says nothing about whether anything ever puts a
// config there. Nothing did: redactionMiddleware was mounted in b5d45e20 and the
// only writers of the field lived in tests, so the G1 dead-code gate deleted the
// Set/GetRedactionConfig setters in 670f6380 and REST/WS redaction was a no-op in
// every shipped build. These tests go through New() so that regression cannot
// return silently.

const wiringFakeSecret = "sk-proj-FAKEtestkey1234567890123456789012345678901234"

func newWiringTestServer(t *testing.T, rcfg *RedactionConfig) *Server {
	t.Helper()
	s := New(Config{Host: "127.0.0.1", Port: 0, Redaction: rcfg})
	t.Cleanup(s.Stop)
	return s
}

func TestNewWiresRedactionIntoRESTMiddleware(t *testing.T) {
	s := newWiringTestServer(t, &RedactionConfig{
		Enabled: true,
		Config:  redaction.Config{Mode: redaction.ModeRedact},
	})

	handler := s.redactionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"message":"` + wiringFakeSecret + `"}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/test", nil))

	if body := rr.Body.String(); strings.Contains(body, wiringFakeSecret) {
		t.Errorf("New did not wire Config.Redaction into redactionMiddleware; secret survived: %s", body)
	}
}

func TestNewWiresRedactionIntoWSHub(t *testing.T) {
	s := newWiringTestServer(t, &RedactionConfig{
		Enabled: true,
		Config:  redaction.Config{Mode: redaction.ModeRedact},
	})

	got := s.wsHub.getRedactionCfgForTest()
	if got == nil {
		t.Fatal("New did not wire Config.Redaction into the WebSocket hub; broadcastEvent would ship secrets to subscribers")
	}
	if !got.Enabled || got.Config.Mode != redaction.ModeRedact {
		t.Errorf("hub got enabled=%v mode=%v, want enabled=true mode=%v", got.Enabled, got.Config.Mode, redaction.ModeRedact)
	}
}

func TestNewWithoutRedactionLeavesBothConsumersInert(t *testing.T) {
	s := newWiringTestServer(t, nil)

	if s.redactionCfg != nil {
		t.Errorf("nil Config.Redaction must leave the middleware inert, got %+v", s.redactionCfg)
	}
	if got := s.wsHub.getRedactionCfgForTest(); got != nil {
		t.Errorf("nil Config.Redaction must leave the hub inert, got %+v", got)
	}
}

// New deep-copies so the caller's reference-typed fields cannot be mutated out
// from under either consumer, and so the two consumers cannot alias each other
// (the hazard bd-oekc2 fixed for the since-deleted setters in fa8045ea/0b0f0874).
func TestNewDeepCopiesRedactionConfig(t *testing.T) {
	caller := &RedactionConfig{
		Enabled: true,
		Config: redaction.Config{
			Mode:               redaction.ModeRedact,
			Allowlist:          []string{"keep-me"},
			ExtraPatterns:      map[redaction.Category][]string{"CUSTOM": {"pat"}},
			DisabledCategories: []redaction.Category{"JWT"},
		},
	}
	s := newWiringTestServer(t, caller)

	caller.Enabled = false
	caller.Config.Mode = redaction.ModeOff
	caller.Config.Allowlist[0] = "mutated"
	caller.Config.ExtraPatterns["CUSTOM"][0] = "mutated"
	caller.Config.DisabledCategories[0] = "MUTATED"

	hub := s.wsHub.getRedactionCfgForTest()
	if hub == nil {
		t.Fatal("hub config missing")
	}
	for name, got := range map[string]*RedactionConfig{"server": s.redactionCfg, "hub": hub} {
		if !got.Enabled || got.Config.Mode != redaction.ModeRedact {
			t.Errorf("%s: caller mutation leaked into stored config: enabled=%v mode=%v", name, got.Enabled, got.Config.Mode)
		}
		if got.Config.Allowlist[0] != "keep-me" {
			t.Errorf("%s: Allowlist aliases the caller's slice: %q", name, got.Config.Allowlist[0])
		}
		if got.Config.ExtraPatterns["CUSTOM"][0] != "pat" {
			t.Errorf("%s: ExtraPatterns aliases the caller's map: %q", name, got.Config.ExtraPatterns["CUSTOM"][0])
		}
		if got.Config.DisabledCategories[0] != "JWT" {
			t.Errorf("%s: DisabledCategories aliases the caller's slice: %q", name, got.Config.DisabledCategories[0])
		}
	}

	// The two consumers must not share backing arrays with each other either.
	s.redactionCfg.Config.Allowlist[0] = "server-only"
	if hub.Config.Allowlist[0] != "keep-me" {
		t.Errorf("server and hub configs alias the same Allowlist backing array: %q", hub.Config.Allowlist[0])
	}
}
