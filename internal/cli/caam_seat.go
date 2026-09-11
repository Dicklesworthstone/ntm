package cli

// CAAM seat selection for spawn/add (ntm#319).
//
// The quota law this implements: on a pool of interchangeable subscription
// seats, spend the included allowance that refreshes SOONEST while it still
// has headroom, and PRESERVE the later-resetting seat. Before this, a pane
// launched without a persona pin carried an empty PersonaName into the agent
// command template, and a host template's else-branch pinned whatever static
// profile it named — on the reporting host, the reserve seat the law exists to
// protect.
//
// Three properties are load-bearing and each has a test:
//
//  1. OFF BY DEFAULT. With [integrations.caam] seat_selection unset or false,
//     planCAAMSeats returns nil before touching anything, no caam process is
//     started, and every launch path behaves exactly as it did before.
//  2. A PIN ALWAYS WINS. A pane that already carries a persona (--persona,
//     --profile-set, a recipe persona) is never re-ranked, and caam is never
//     even asked about it.
//  3. AN UNANSWERABLE POOL SKIPS THAT PANE, LOUDLY. When caam is missing,
//     times out, or ranks the pool and finds nothing eligible, the affected
//     panes do not launch and the reason is reported. They must never fall
//     through to a static pin — that fallthrough IS the bug. Siblings on a
//     provider that answered still launch: an `ntm add --cod=2 --cc=2` with an
//     exhausted Codex pool brings up the two Claude panes.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// caamSeatResolver is the slice of the CAAM adapter that seat selection uses.
// RankedSeat pins the earliest-reset-with-headroom rank mode internally, so no
// caller here can reach for `--best` (which ranks lowest utilization and would
// pick exactly the reserve seat this feature preserves).
type caamSeatResolver interface {
	RankedSeat(ctx context.Context, provider, model string) (*tools.CAAMRankedProfile, error)
}

// newCAAMSeatResolver builds the resolver a plan will use. A package variable
// so tests can substitute a stub without a caam binary; production code never
// reassigns it.
var newCAAMSeatResolver = func() caamSeatResolver { return tools.NewCAAMAdapter() }

// caamSeatSelectionEnabled reports whether [integrations.caam] seat_selection
// is on. Nil-safe: an unloaded config means off.
func caamSeatSelectionEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Integrations.CAAM.SeatSelection
}

// caamSeatProviderForAgentType maps an ntm agent type onto the provider token
// `caam limits` can answer for, or "" when it cannot.
//
// Only Claude and Codex are subscription pools with per-seat allowance
// windows; caam's limits command rejects every other provider outright. Panes
// of any other type are left completely alone, so enabling seat_selection on a
// host that also spawns gemini/grok/ollama panes changes nothing for those.
func caamSeatProviderForAgentType(agentType AgentType) string {
	switch agentType {
	case AgentTypeClaude:
		return "claude"
	case AgentTypeCodex:
		return "codex"
	default:
		return ""
	}
}

// caamSeatCandidate is one pane of a spawn/add batch offered to seat selection.
type caamSeatCandidate struct {
	// Index is the pane's position in the batch's agent slice; it is the key
	// the resulting plan is read back by.
	Index int
	// Type is the pane's ntm agent type.
	Type AgentType
	// Pinned is true when the pane already carries a persona. Pinned panes are
	// skipped by the planner entirely — no decision is recorded for them, so a
	// pin can never be overridden by a ranked seat.
	Pinned bool
}

// caamSeatDecision is the planner's answer for one pane.
type caamSeatDecision struct {
	// Provider is the caam provider token the decision was ranked on.
	Provider string
	// Profile is caam's own profile id for the selected seat.
	Profile string
	// Persona is the ntm persona name composed from provider+profile
	// ("codex-acme-net"). This is what a pane launches under.
	Persona string
	// Registered is the persona registry entry for Persona when one exists AND
	// declares the same agent type as the pane. Non-nil means the pane can
	// launch as a full persona (system prompt, model, reasoning effort); nil
	// means only the NAME is pinned, which is still enough for an agent
	// command template that maps PersonaName onto a caam profile.
	Registered *persona.Persona
	// TypeMismatch records that a registry persona of this name exists but
	// does not resolve to this pane's agent type — it declares another one, or
	// declares none and therefore defaults to Claude — so its settings were
	// deliberately not adopted. Worth a warning: it is a host
	// misconfiguration, not something ntm should paper over.
	TypeMismatch bool
	// Skip is set when this pane must not launch, with Reason saying why.
	Skip   bool
	Reason string
}

// caamSeatPlan holds the per-pane decisions for one spawn/add batch. A nil
// plan is the disabled case and answers "no decision" for every index, so
// call sites need no enabled-check of their own.
type caamSeatPlan struct {
	decisions map[int]caamSeatDecision
}

// For returns the decision recorded for a pane index, if any.
func (p *caamSeatPlan) For(index int) (caamSeatDecision, bool) {
	if p == nil {
		return caamSeatDecision{}, false
	}
	decision, ok := p.decisions[index]
	return decision, ok
}

// Seat returns the persona a pane should launch under, and false when the
// plan has nothing to say about it (disabled, pinned, or an unsupported
// agent type).
func (p *caamSeatPlan) Seat(index int) (caamSeatDecision, bool) {
	decision, ok := p.For(index)
	if !ok || decision.Skip {
		return caamSeatDecision{}, false
	}
	return decision, true
}

// SkipReason returns why a pane must not launch, and false when it may.
func (p *caamSeatPlan) SkipReason(index int) (string, bool) {
	decision, ok := p.For(index)
	if !ok || !decision.Skip {
		return "", false
	}
	return decision.Reason, true
}

// planCAAMSeats resolves the seat every unpinned, CAAM-backed pane in a batch
// should launch on.
//
// It shells out to caam AT MOST ONCE PER PROVIDER for the whole batch — the
// ranked seat is a property of the pool, not of the pane, so four Codex panes
// all belong on the same seat and asking four times would just be four network
// round trips to the same answer.
//
// Providers are queried sequentially, so the worst case for a batch spanning
// both is two CAAMLimitsTimeout waits. That bound is deliberate: caam itself
// allows its provider API fetches up to 60s, and a shorter one here would
// report a slow provider as an unreadable pool.
//
// Returns nil when the feature is off or no pane needs a seat, which is the
// signal for "behave exactly as before".
func planCAAMSeats(
	ctx context.Context,
	cfg *config.Config,
	projectDir string,
	candidates []caamSeatCandidate,
) *caamSeatPlan {
	if !caamSeatSelectionEnabled(cfg) {
		return nil
	}

	providers := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.Pinned {
			continue
		}
		if provider := caamSeatProviderForAgentType(candidate.Type); provider != "" {
			providers[provider] = true
		}
	}
	if len(providers) == 0 {
		return nil
	}

	// Deterministic order so a two-provider batch always queries caam in the
	// same sequence — the calls are independent, but a stable order keeps the
	// output (and any test that reads recorded argv) reproducible.
	ordered := make([]string, 0, len(providers))
	for provider := range providers {
		ordered = append(ordered, provider)
	}
	sort.Strings(ordered)

	resolver := newCAAMSeatResolver()
	registry := loadCAAMSeatPersonaRegistry(projectDir)

	byProvider := make(map[string]caamSeatDecision, len(ordered))
	for _, provider := range ordered {
		byProvider[provider] = resolveCAAMSeat(ctx, resolver, registry, provider)
	}

	plan := &caamSeatPlan{decisions: make(map[int]caamSeatDecision, len(candidates))}
	for _, candidate := range candidates {
		if candidate.Pinned {
			continue
		}
		provider := caamSeatProviderForAgentType(candidate.Type)
		if provider == "" {
			continue
		}
		decision := byProvider[provider]
		// A registry persona is adopted only when it resolves to the same agent
		// type as the pane. `codex-acme` carrying agent_type = "claude" would
		// otherwise inject a Claude model into a Codex pane — and so would one
		// carrying no agent_type at all, since AgentTypeFlag defaults to
		// Claude.
		if decision.Registered != nil && AgentType(decision.Registered.AgentTypeFlag()) != candidate.Type {
			decision.Registered = nil
			decision.TypeMismatch = true
		}
		plan.decisions[candidate.Index] = decision
	}
	return plan
}

// loadCAAMSeatPersonaRegistry loads the persona registry once per plan.
// A registry that fails to load is not fatal: seat selection still pins the
// composed persona NAME, which is the part an agent command template needs.
func loadCAAMSeatPersonaRegistry(projectDir string) *persona.Registry {
	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return nil
	}
	return registry
}

// resolveCAAMSeat asks caam for one provider's ranked seat.
func resolveCAAMSeat(
	ctx context.Context,
	resolver caamSeatResolver,
	registry *persona.Registry,
	provider string,
) caamSeatDecision {
	if resolver == nil {
		return caamSeatDecision{
			Provider: provider,
			Skip:     true,
			Reason:   fmt.Sprintf("no caam adapter available to rank the %s pool", provider),
		}
	}

	seat, err := resolver.RankedSeat(ctx, provider, "")
	if err != nil {
		return caamSeatDecision{
			Provider: provider,
			Skip:     true,
			Reason:   caamSeatSkipReason(provider, err),
		}
	}

	name := seat.Persona()
	if name == "" {
		return caamSeatDecision{
			Provider: provider,
			Skip:     true,
			Reason: fmt.Sprintf(
				"caam ranked the %s pool but returned no usable profile name", provider),
		}
	}

	decision := caamSeatDecision{
		Provider: provider,
		Profile:  seat.Profile,
		Persona:  name,
	}
	if registry != nil {
		if p, ok := registry.Get(name); ok && p != nil {
			decision.Registered = p
		}
	}
	return decision
}

// caamSeatSkipReason turns a limits failure into a sentence an operator can
// act on. The distinction between "caam could not answer" and "caam answered
// that nothing is eligible" is preserved: they call for different fixes, and
// collapsing them is how a broken pool starts looking like a healthy one.
func caamSeatSkipReason(provider string, err error) string {
	switch {
	case errors.Is(err, tools.ErrToolNotInstalled):
		return fmt.Sprintf(
			"seat_selection is enabled but caam is not installed, so the %s seat cannot be chosen", provider)
	case errors.Is(err, tools.ErrCAAMNoSeatSelectable):
		return fmt.Sprintf("no %s seat has included headroom (%s)", provider, caamSeatErrorDetail(err))
	case errors.Is(err, tools.ErrTimeout):
		return fmt.Sprintf("caam limits timed out reading the %s pool", provider)
	default:
		return fmt.Sprintf("could not read caam limits for %s: %v", provider, err)
	}
}

// caamSeatErrorDetail strips the ErrCAAMNoSeatSelectable sentinel prefix so
// caam's own reason is reported once rather than nested inside ntm's phrasing.
func caamSeatErrorDetail(err error) string {
	text := err.Error()
	prefix := tools.ErrCAAMNoSeatSelectable.Error() + ": "
	if trimmed, ok := strings.CutPrefix(text, prefix); ok {
		return trimmed
	}
	return text
}

// caamSeatModelOverride reports the model a ranked seat's persona contributes
// to a pane, and whether it contributes one at all.
//
// Seat selection chooses the ACCOUNT, not the model, so two things must hold
// and neither is automatic:
//
//   - An explicitly requested model wins. `ntm add --cod=1:gpt-5.1-codex-max`
//     carries no persona pin, so it IS eligible for a seat; taking the seat
//     persona's model as well would silently discard what the operator asked
//     for.
//   - A persona that declares no model contributes nothing. `model` is
//     optional on a persona (only `name` and `agent_type` are required), and
//     resolving an empty model falls through to the agent type's config
//     default — so an unguarded assignment would demote an otherwise
//     defaulted pane and, worse, do it invisibly.
func caamSeatModelOverride(p *persona.Persona, modelRequested bool) (string, bool) {
	if p == nil || modelRequested {
		return "", false
	}
	model := strings.TrimSpace(p.Model)
	if model == "" {
		return "", false
	}
	return model, true
}

// caamSeatSkipMessage is the one-line operator-facing report for a skipped
// pane. Kept here so spawn and add word it identically.
func caamSeatSkipMessage(agentType AgentType, reason string) string {
	return fmt.Sprintf("seat selection skipped a %s pane: %s", agentType, reason)
}

// caamSeatTypeMismatchMessage warns that a persona of the seat's name exists
// but is declared for a different agent type, so only the name was pinned.
func caamSeatTypeMismatchMessage(agentType AgentType, personaName string) string {
	return fmt.Sprintf(
		"persona %q does not declare agent type %q (set agent_type on it); pinning the seat name only",
		personaName, agentType)
}
