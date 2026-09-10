package cli

// Tests for CAAM seat selection on the spawn/add launch path (ntm#319).
//
// The behavior under test is the maintainer decision on that issue: opt-in
// behind [integrations.caam] seat_selection, with skip-unpinned semantics.
// Five properties are named in the decision and each has a test here:
//
//	off                       -> nothing changes, caam is never invoked
//	on + unpinned             -> the pane launches on caam's ranked seat
//	on + pinned               -> the pin wins, no ranking happens for it
//	on + caam unavailable     -> that pane is skipped, siblings still placed
//	on + no seat with headroom-> the same skip path, with caam's own reason

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// stubSeatResolver answers RankedSeat from a fixture and records every
// provider it was asked about, so a test can assert both the answer and the
// number of round trips.
type stubSeatResolver struct {
	mu    sync.Mutex
	calls []string
	seats map[string]*tools.CAAMRankedProfile
	errs  map[string]error
}

func (s *stubSeatResolver) RankedSeat(_ context.Context, provider, _ string) (*tools.CAAMRankedProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, provider)
	if err, ok := s.errs[provider]; ok {
		return nil, err
	}
	if seat, ok := s.seats[provider]; ok {
		return seat, nil
	}
	return nil, fmt.Errorf("stub seat resolver has no fixture for provider %q", provider)
}

func (s *stubSeatResolver) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// withStubSeatResolver swaps the resolver constructor for the duration of a
// test. Nothing in production reassigns it.
func withStubSeatResolver(t *testing.T, stub caamSeatResolver) {
	t.Helper()
	previous := newCAAMSeatResolver
	newCAAMSeatResolver = func() caamSeatResolver { return stub }
	t.Cleanup(func() { newCAAMSeatResolver = previous })
}

// isolatePersonaRegistry points every user-persona lookup at an empty temp
// directory so a test never reads (or is perturbed by) the developer's real
// ~/.config/ntm/personas.toml.
func isolatePersonaRegistry(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("NTM_CONFIG", filepath.Join(home, ".config", "ntm", "config.toml"))
	return t.TempDir() // project dir, deliberately distinct and empty
}

// writeProjectPersonas drops a .ntm/personas.toml into a project directory.
func writeProjectPersonas(t *testing.T, projectDir, contents string) {
	t.Helper()
	dir := filepath.Join(projectDir, ".ntm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project persona dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "personas.toml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write project personas: %v", err)
	}
}

func seatSelectionConfig(enabled bool) *config.Config {
	cfg := config.Default()
	cfg.Integrations.CAAM.SeatSelection = enabled
	return cfg
}

func rankedSeat(provider, profile string, usedPercent int) *tools.CAAMRankedProfile {
	return &tools.CAAMRankedProfile{
		Provider:        provider,
		Profile:         profile,
		Rank:            1,
		Eligible:        true,
		Tier:            "included_headroom",
		Reason:          "included allowance refreshes soonest",
		UsedPercent:     usedPercent,
		HeadroomPercent: 100 - usedPercent,
		GoverningWindow: "weekly",
	}
}

// TestSeatSelectionDefaultsOff is the guarantee every other host depends on:
// a fresh config does not opt in.
func TestSeatSelectionDefaultsOff(t *testing.T) {
	if config.DefaultCAAMConfig().SeatSelection {
		t.Fatal("seat_selection must default to false; enabling it by default puts a provider API call on the critical path of every spawn")
	}
	if caamSeatSelectionEnabled(config.Default()) {
		t.Fatal("caamSeatSelectionEnabled must be false for a default config")
	}
	if caamSeatSelectionEnabled(nil) {
		t.Fatal("caamSeatSelectionEnabled must be false for a nil config")
	}
}

// TestSeatSelectionOffChangesNothing: with the key off the planner returns the
// nil plan (every call site then behaves exactly as before) and caam is never
// asked anything.
func TestSeatSelectionOffChangesNothing(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": rankedSeat("codex", "spend-me", 34),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(false), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
		{Index: 1, Type: AgentTypeClaude},
	})

	if plan != nil {
		t.Fatalf("expected a nil plan when seat_selection is off, got %+v", plan)
	}
	if calls := stub.recorded(); len(calls) != 0 {
		t.Fatalf("seat selection is off but caam was queried: %v", calls)
	}
	// The nil plan must answer "no opinion" for every index, so no call site
	// needs its own enabled-check.
	if _, ok := plan.For(0); ok {
		t.Fatal("nil plan returned a decision")
	}
	if _, ok := plan.Seat(0); ok {
		t.Fatal("nil plan returned a seat")
	}
	if _, skipped := plan.SkipReason(0); skipped {
		t.Fatal("nil plan skipped a pane")
	}
}

// TestSeatSelectionPlacesUnpinnedPaneOnRankedSeat is the core fix.
func TestSeatSelectionPlacesUnpinnedPaneOnRankedSeat(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": rankedSeat("codex", "spend-me", 34),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	seat, ok := plan.Seat(0)
	if !ok {
		t.Fatal("expected a seat for the unpinned codex pane")
	}
	if seat.Persona != "codex-spend-me" {
		t.Fatalf("persona = %q, want %q", seat.Persona, "codex-spend-me")
	}
	if seat.Profile != "spend-me" {
		t.Fatalf("profile = %q, want %q", seat.Profile, "spend-me")
	}
	if seat.Provider != "codex" {
		t.Fatalf("provider = %q, want %q", seat.Provider, "codex")
	}
	if _, skipped := plan.SkipReason(0); skipped {
		t.Fatal("a pane with a resolved seat must not be skipped")
	}
}

// TestSeatSelectionNeverOverridesAPin: a pinned pane is not even offered to
// caam, so a --persona pin cannot be re-ranked away.
func TestSeatSelectionNeverOverridesAPin(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": rankedSeat("codex", "spend-me", 34),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex, Pinned: true},
	})

	if plan != nil {
		if _, ok := plan.For(0); ok {
			t.Fatal("a pinned pane must carry no seat decision")
		}
	}
	if calls := stub.recorded(); len(calls) != 0 {
		t.Fatalf("a batch of only pinned panes must not query caam, got %v", calls)
	}
}

// TestSeatSelectionPinWinsWhileSiblingIsRanked: the mixed case — one pinned
// pane and one bare pane in the same batch.
func TestSeatSelectionPinWinsWhileSiblingIsRanked(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": rankedSeat("codex", "spend-me", 34),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex, Pinned: true},
		{Index: 1, Type: AgentTypeCodex},
	})

	if _, ok := plan.For(0); ok {
		t.Fatal("pinned pane 0 must carry no decision")
	}
	seat, ok := plan.Seat(1)
	if !ok || seat.Persona != "codex-spend-me" {
		t.Fatalf("unpinned pane 1 seat = %+v, ok = %v; want codex-spend-me", seat, ok)
	}
}

// TestSeatSelectionSkipsWhenCAAMUnavailableAndPlacesSiblings is the
// skip-unpinned half of the decision: an unreadable Codex pool refuses the
// Codex panes and leaves the Claude panes alone.
func TestSeatSelectionSkipsWhenCAAMUnavailableAndPlacesSiblings(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{
		seats: map[string]*tools.CAAMRankedProfile{
			"claude": rankedSeat("claude", "spend-me", 12),
		},
		errs: map[string]error{"codex": tools.ErrToolNotInstalled},
	}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
		{Index: 1, Type: AgentTypeCodex},
		{Index: 2, Type: AgentTypeClaude},
		{Index: 3, Type: AgentTypeClaude},
	})

	for _, index := range []int{0, 1} {
		reason, skipped := plan.SkipReason(index)
		if !skipped {
			t.Fatalf("codex pane %d should have been skipped", index)
		}
		if !strings.Contains(reason, "caam is not installed") {
			t.Fatalf("pane %d skip reason = %q, want it to name the missing caam binary", index, reason)
		}
		if _, ok := plan.Seat(index); ok {
			t.Fatalf("skipped pane %d must not also carry a seat", index)
		}
	}
	for _, index := range []int{2, 3} {
		seat, ok := plan.Seat(index)
		if !ok {
			t.Fatalf("claude pane %d should still be placed when only the codex pool is unreadable", index)
		}
		if seat.Persona != "claude-spend-me" {
			t.Fatalf("claude pane %d persona = %q, want claude-spend-me", index, seat.Persona)
		}
	}
}

// TestSeatSelectionSkipsWhenNoSeatHasHeadroom: caam ANSWERED — the pool is
// exhausted. That is not a transport failure and must not be reported as one,
// and it must never fall through to a static pin.
func TestSeatSelectionSkipsWhenNoSeatHasHeadroom(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	exhausted := fmt.Errorf("%w: every codex profile is at its included cap", tools.ErrCAAMNoSeatSelectable)
	stub := &stubSeatResolver{errs: map[string]error{"codex": exhausted}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	reason, skipped := plan.SkipReason(0)
	if !skipped {
		t.Fatal("an exhausted pool must skip the pane, not place it")
	}
	if !strings.Contains(reason, "no codex seat has included headroom") {
		t.Fatalf("skip reason = %q, want it to say the pool has no headroom", reason)
	}
	if !strings.Contains(reason, "every codex profile is at its included cap") {
		t.Fatalf("skip reason = %q, want caam's own reason carried through", reason)
	}
	// caam's reason must appear exactly once, not nested inside the sentinel
	// text as well.
	if strings.Contains(reason, tools.ErrCAAMNoSeatSelectable.Error()) {
		t.Fatalf("skip reason = %q, want the sentinel prefix stripped", reason)
	}
	if _, ok := plan.Seat(0); ok {
		t.Fatal("an exhausted pool must not yield a seat")
	}
}

// TestSeatSelectionDistinguishesTimeoutFromExhaustion keeps the two failure
// classes from collapsing into one message: they call for different fixes.
func TestSeatSelectionDistinguishesTimeoutFromExhaustion(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{errs: map[string]error{"codex": tools.ErrTimeout}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	reason, skipped := plan.SkipReason(0)
	if !skipped {
		t.Fatal("a timed-out limits call must skip the pane")
	}
	if !strings.Contains(reason, "timed out") {
		t.Fatalf("skip reason = %q, want it to name the timeout", reason)
	}
	if strings.Contains(reason, "headroom") {
		t.Fatalf("skip reason = %q, must not describe a transport failure as an exhausted pool", reason)
	}
}

// TestSeatSelectionQueriesCAAMOncePerProvider: the ranked seat is a property
// of the pool, not of the pane, so a four-pane add is one round trip per
// provider — not four.
func TestSeatSelectionQueriesCAAMOncePerProvider(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex":  rankedSeat("codex", "spend-me", 34),
		"claude": rankedSeat("claude", "also-me", 20),
	}}
	withStubSeatResolver(t, stub)

	candidates := make([]caamSeatCandidate, 0, 8)
	for i := 0; i < 4; i++ {
		candidates = append(candidates, caamSeatCandidate{Index: i, Type: AgentTypeCodex})
	}
	for i := 4; i < 8; i++ {
		candidates = append(candidates, caamSeatCandidate{Index: i, Type: AgentTypeClaude})
	}

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, candidates)

	calls := stub.recorded()
	if len(calls) != 2 {
		t.Fatalf("expected exactly one query per provider, got %d: %v", len(calls), calls)
	}
	// Deterministic order keeps output reproducible across runs.
	if calls[0] != "claude" || calls[1] != "codex" {
		t.Fatalf("provider query order = %v, want [claude codex]", calls)
	}
	for i := 0; i < 4; i++ {
		if seat, ok := plan.Seat(i); !ok || seat.Persona != "codex-spend-me" {
			t.Fatalf("codex pane %d seat = %+v ok=%v", i, seat, ok)
		}
	}
	for i := 4; i < 8; i++ {
		if seat, ok := plan.Seat(i); !ok || seat.Persona != "claude-also-me" {
			t.Fatalf("claude pane %d seat = %+v ok=%v", i, seat, ok)
		}
	}
}

// TestSeatSelectionIgnoresUnsupportedProviders: `caam limits` answers only for
// claude and codex, so a gemini/ollama pane is left completely alone even with
// the feature on.
func TestSeatSelectionIgnoresUnsupportedProviders(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeGemini},
		{Index: 1, Type: AgentTypeOllama},
		{Index: 2, Type: AgentTypeGrok},
	})

	if plan != nil {
		t.Fatalf("a batch with no CAAM-backed pane must produce no plan, got %+v", plan)
	}
	if calls := stub.recorded(); len(calls) != 0 {
		t.Fatalf("unsupported providers must not reach caam, got %v", calls)
	}
}

// TestSeatSelectionAdoptsRegisteredPersonaForMatchingAgentType: when the
// composed seat name IS a configured persona of the same agent type, the pane
// launches as that full persona.
func TestSeatSelectionAdoptsRegisteredPersonaForMatchingAgentType(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	writeProjectPersonas(t, projectDir, `
[[personas]]
name = "codex-spend-me"
description = "shallow codex seat"
agent_type = "codex"
model = "gpt-5.1-codex-max"
system_prompt = "You are a codex seat."
`)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": rankedSeat("codex", "spend-me", 34),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	seat, ok := plan.Seat(0)
	if !ok {
		t.Fatal("expected a seat")
	}
	if seat.Registered == nil {
		t.Fatal("a configured persona of the same agent type must be adopted")
	}
	if seat.Registered.Model != "gpt-5.1-codex-max" {
		t.Fatalf("adopted persona model = %q", seat.Registered.Model)
	}
	if seat.TypeMismatch {
		t.Fatal("a matching agent type must not report a mismatch")
	}
}

// TestSeatSelectionRefusesAPersonaDeclaredForAnotherAgentType: a persona named
// codex-* but declared agent_type = "claude" would otherwise inject a Claude
// model into a Codex pane. Pin the NAME (which is what a command template
// needs) and adopt nothing else.
func TestSeatSelectionRefusesAPersonaDeclaredForAnotherAgentType(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	writeProjectPersonas(t, projectDir, `
[[personas]]
name = "codex-spend-me"
description = "misconfigured"
agent_type = "claude"
model = "opus"
system_prompt = "Wrong agent type."
`)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": rankedSeat("codex", "spend-me", 34),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	seat, ok := plan.Seat(0)
	if !ok {
		t.Fatal("the pane must still be placed on the ranked seat name")
	}
	if seat.Persona != "codex-spend-me" {
		t.Fatalf("persona = %q, want the composed seat name", seat.Persona)
	}
	if seat.Registered != nil {
		t.Fatal("a persona declared for another agent type must not be adopted")
	}
	if !seat.TypeMismatch {
		t.Fatal("the mismatch must be recorded so it can be warned about")
	}
}

// TestSeatSelectionSurvivesAnUnloadablePersonaRegistry: an invalid
// personas.toml (here, one missing the required agent_type) makes the whole
// registry fail to load. That must not take the seat with it — the composed
// name is what an agent command template maps onto a caam profile, and pinning
// it is the whole point. Only the optional enrichment (system prompt, model,
// reasoning effort) is lost.
func TestSeatSelectionSurvivesAnUnloadablePersonaRegistry(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	writeProjectPersonas(t, projectDir, `
[[personas]]
name = "codex-spend-me"
description = "invalid: agent_type is required"
model = "opus"
system_prompt = "Undeclared agent type."
`)
	if _, err := persona.LoadRegistry(projectDir); err == nil {
		t.Fatal("fixture precondition: this personas.toml must fail to load")
	}

	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": rankedSeat("codex", "spend-me", 34),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	seat, ok := plan.Seat(0)
	if !ok || seat.Persona != "codex-spend-me" {
		t.Fatalf("seat = %+v ok = %v, want the composed seat name still pinned", seat, ok)
	}
	if seat.Registered != nil {
		t.Fatal("nothing can be adopted from a registry that did not load")
	}
	if seat.TypeMismatch {
		t.Fatal("a registry that did not load is not an agent-type mismatch")
	}
}

// TestSeatSelectionAdoptsAPersonaForAClaudePane is the counterpart: a persona
// with no declared agent_type DOES resolve to Claude, so a Claude pane adopts
// it — the default is only wrong for the other providers.
func TestSeatSelectionAdoptsAPersonaForAClaudePane(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	writeProjectPersonas(t, projectDir, `
[[personas]]
name = "claude-spend-me"
description = "shallow claude seat"
agent_type = "claude"
model = "opus"
reasoning_effort = "high"
system_prompt = "You are a claude seat."
`)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"claude": rankedSeat("claude", "spend-me", 20),
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeClaude},
	})

	seat, ok := plan.Seat(0)
	if !ok {
		t.Fatal("expected a seat for the unpinned claude pane")
	}
	if seat.Registered == nil {
		t.Fatal("a matching claude persona must be adopted")
	}
	if seat.Registered.ReasoningEffort != "high" {
		t.Fatalf("adopted reasoning effort = %q, want high", seat.Registered.ReasoningEffort)
	}
	if seat.TypeMismatch {
		t.Fatal("a claude persona on a claude pane is not a mismatch")
	}
}

// TestSeatSelectionSkipsWhenCAAMNamesNoProfile guards a malformed answer: a
// selected row with an empty profile composes no persona, and pinning
// "codex-" would be worse than not launching.
func TestSeatSelectionSkipsWhenCAAMNamesNoProfile(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	stub := &stubSeatResolver{seats: map[string]*tools.CAAMRankedProfile{
		"codex": {Provider: "codex", Profile: "", Rank: 1, Eligible: true},
	}}
	withStubSeatResolver(t, stub)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	reason, skipped := plan.SkipReason(0)
	if !skipped {
		t.Fatal("a seat with no profile name must be skipped, not pinned as \"codex-\"")
	}
	if !strings.Contains(reason, "no usable profile name") {
		t.Fatalf("skip reason = %q", reason)
	}
}

// TestSeatSelectionUsesTheRankModeAndNeverBest runs the real CAAM adapter
// against a scripted caam binary, so the argv ntm actually produces is
// pinned. `--best` ranks LOWEST UTILIZATION, which picks the idle reserve
// seat — the exact opposite of this feature's policy.
func TestSeatSelectionUsesTheRankModeAndNeverBest(t *testing.T) {
	projectDir := isolatePersonaRegistry(t)
	argvPath := writeScriptedCAAMForSeatTests(t, `{
  "rank": "earliest-reset-headroom",
  "provider": "codex",
  "headroom_ceiling_percent": 95,
  "generated_at": "2026-09-10T12:00:00Z",
  "selected": {
    "provider": "codex",
    "profile": "spend-me",
    "rank": 1,
    "eligible": true,
    "tier": "included_headroom",
    "used_percent": 34,
    "headroom_percent": 66,
    "governing_window": "weekly"
  },
  "profiles": []
}`, "", 0)

	plan := planCAAMSeats(context.Background(), seatSelectionConfig(true), projectDir, []caamSeatCandidate{
		{Index: 0, Type: AgentTypeCodex},
	})

	seat, ok := plan.Seat(0)
	if !ok || seat.Persona != "codex-spend-me" {
		t.Fatalf("seat = %+v ok = %v, want codex-spend-me", seat, ok)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	recorded := string(argv)
	if !strings.Contains(recorded, "--rank "+tools.CAAMRankEarliestResetHeadroom) {
		t.Fatalf("argv %q must pin the earliest-reset-headroom rank mode", recorded)
	}
	if strings.Contains(recorded, "--best") {
		t.Fatalf("argv %q must never pass --best: it ranks lowest utilization and picks the reserve seat", recorded)
	}
	// ntm stores Codex accounts under the provider id "openai"; caam limits
	// rejects that spelling outright, so the adapter must translate.
	if !strings.Contains(recorded, "limits codex") {
		t.Fatalf("argv %q must reach caam as the codex provider", recorded)
	}
}

// writeScriptedCAAMForSeatTests installs a fake `caam` on PATH that records
// its argv and replies to `limits` with the given payload.
func writeScriptedCAAMForSeatTests(t *testing.T, stdout, stderr string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")

	stdoutFile := filepath.Join(dir, "stdout.json")
	if err := os.WriteFile(stdoutFile, []byte(stdout), 0o644); err != nil {
		t.Fatalf("write fake stdout: %v", err)
	}
	stderrFile := filepath.Join(dir, "stderr.txt")
	if err := os.WriteFile(stderrFile, []byte(stderr), 0o644); err != nil {
		t.Fatalf("write fake stderr: %v", err)
	}

	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + argvPath + "\n" +
		"if [ \"$1\" = \"limits\" ]; then\n" +
		"  cat " + stdoutFile + "\n" +
		"  cat " + stderrFile + " 1>&2\n" +
		"  exit " + strconv.Itoa(exitCode) + "\n" +
		"fi\n" +
		"echo 'unexpected subcommand' 1>&2\n" +
		"exit 1\n"

	fake := filepath.Join(dir, "caam")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tools.InvalidateCAAMLimitsCache()
	t.Cleanup(tools.InvalidateCAAMLimitsCache)
	return argvPath
}

// TestCAAMSeatSkipReasonClassifiesErrors pins the wording contract each
// failure class carries, since these strings are what an operator acts on.
func TestCAAMSeatSkipReasonClassifiesErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		contains string
		absent   string
	}{
		{
			name:     "not installed",
			err:      tools.ErrToolNotInstalled,
			contains: "caam is not installed",
			absent:   "headroom",
		},
		{
			name:     "no seat selectable",
			err:      fmt.Errorf("%w: pool exhausted", tools.ErrCAAMNoSeatSelectable),
			contains: "no codex seat has included headroom (pool exhausted)",
		},
		{
			name:     "timeout",
			err:      tools.ErrTimeout,
			contains: "timed out",
			absent:   "not installed",
		},
		{
			name:     "unclassified",
			err:      errors.New("boom"),
			contains: "could not read caam limits for codex: boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := caamSeatSkipReason("codex", tc.err)
			if !strings.Contains(got, tc.contains) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.contains)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Fatalf("reason = %q, must not contain %q", got, tc.absent)
			}
		})
	}
}

// TestCAAMSeatProviderForAgentType pins the agent-type -> provider mapping.
func TestCAAMSeatProviderForAgentType(t *testing.T) {
	cases := map[AgentType]string{
		AgentTypeClaude:      "claude",
		AgentTypeCodex:       "codex",
		AgentTypeGemini:      "",
		AgentTypeGrok:        "",
		AgentTypeOllama:      "",
		AgentTypeAntigravity: "",
	}
	for agentType, want := range cases {
		if got := caamSeatProviderForAgentType(agentType); got != want {
			t.Errorf("caamSeatProviderForAgentType(%q) = %q, want %q", agentType, got, want)
		}
	}
}
