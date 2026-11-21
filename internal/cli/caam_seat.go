package cli

// CAAM seat selection adapters delegating to the shared ORI spawn engine in
// internal/session/spawn_engine.go (ntm#319, bd-rc2-track-2-harness-ori-3ydfr.2).
//
// Keeping these package-local aliases/wrappers ensures CLI spawn, CLI add,
// Robot spawn, REST /agents/spawn, and swarm_spawn execute ONE canonical seat
// planner while preserving internal/cli test bindings.

import (
	"context"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/session"
)

type caamSeatResolver = session.CAAMSeatResolver
type caamSeatDecision = session.CAAMSeatDecision
type caamSeatPlan = session.CAAMSeatPlan

// newCAAMSeatResolver builds the resolver a CLI plan will use. A package
// variable so unit tests can substitute a stub without a caam binary.
var newCAAMSeatResolver func() caamSeatResolver

type caamSeatCandidate struct {
	Index  int
	Type   AgentType
	Pinned bool
}

func planCAAMSeats(
	ctx context.Context,
	cfg *config.Config,
	projectDir string,
	candidates []caamSeatCandidate,
) *caamSeatPlan {
	shared := canonicalCAAMSeatCandidates(candidates)
	if newCAAMSeatResolver == nil {
		return session.PlanCAAMSeats(ctx, cfg, projectDir, shared)
	}
	return session.PlanCAAMSeatsWithResolver(ctx, cfg, projectDir, shared, newCAAMSeatResolver())
}

func validateCAAMSeatCompatibility(cfg *config.Config, candidates []caamSeatCandidate) error {
	return session.ValidateCAAMSeatCompatibility(cfg, canonicalCAAMSeatCandidates(candidates))
}

func canonicalCAAMSeatCandidates(candidates []caamSeatCandidate) []session.CAAMSeatCandidate {
	shared := make([]session.CAAMSeatCandidate, len(candidates))
	for i, c := range candidates {
		shared[i] = session.CAAMSeatCandidate{
			Index:  c.Index,
			Type:   string(c.Type),
			Pinned: c.Pinned,
		}
	}
	return shared
}

func caamSeatModelOverride(p *persona.Persona, modelRequested bool) (string, bool) {
	return session.CAAMSeatModelOverride(p, modelRequested)
}

func caamSeatSkipMessage(agentType AgentType, reason string) string {
	return session.CAAMSeatSkipMessage(string(agentType), reason)
}

func caamSeatTypeMismatchMessage(agentType AgentType, personaName string) string {
	return session.CAAMSeatTypeMismatchMessage(string(agentType), personaName)
}
