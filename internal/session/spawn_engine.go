package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/gemini"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

// ============================================================================
// Single Shared ORI Spawn Engine (bd-rc2-track-2-harness-ori-3ydfr.2)
//
// Consolidates the spawn lifecycle stages that must behave identically across
// `ntm spawn`, `ntm add`, `--robot-spawn`, REST `POST /api/v1/sessions/{id}/agents/spawn`,
// and async `swarm_spawn` jobs:
//   1. CAAM subscription seat selection (`[integrations.caam] seat_selection`)
//   2. Git worktree preflight, provisioning, revalidation, and AGENT_MAIL_PROJECT env
//   3. Persona & profile-set resolution, distribution validation, and prompt materialization
//   4. Pre-spawn and post-spawn lifecycle command hooks
//   5. Gemini Pro-model auto-selection post-spawn setup
// ============================================================================

// CAAMSeatResolver is the slice of the CAAM adapter that seat selection uses.
// RankedSeat pins the earliest-reset-with-headroom rank mode internally, so no
// caller can reach for `--best` (which ranks lowest utilization and would pick
// the reserve seat this feature preserves).
type CAAMSeatResolver interface {
	RankedSeat(ctx context.Context, provider, model string) (*tools.CAAMRankedProfile, error)
}

// CAAMSeatSelectionEnabled reports whether [integrations.caam] seat_selection
// is on. Nil-safe: an unloaded config means off.
func CAAMSeatSelectionEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Integrations.CAAM.SeatSelection
}

// CAAMSeatProviderForAgentType maps an ntm agent type (canonical or alias,
// e.g. "cc"/"claude", "cod"/"codex") onto the provider token `caam limits` can
// answer for, or "" when it cannot.
//
// Only Claude and Codex are subscription pools with per-seat allowance windows;
// `caam limits` rejects every other provider outright.
func CAAMSeatProviderForAgentType(agentType string) string {
	canonical := agent.AgentType(strings.TrimSpace(agentType)).Canonical()
	switch canonical {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	default:
		return ""
	}
}

// CAAMSeatCandidate is one pane of a spawn/add batch offered to seat selection.
type CAAMSeatCandidate struct {
	// Index is the pane's position in the batch's agent slice; it is the key
	// the resulting plan is read back by.
	Index int
	// Type is the pane's ntm agent type (canonical or alias).
	Type string
	// Pinned is true when the pane already carries a persona. Pinned panes are
	// skipped by the planner entirely so a pin can never be overridden.
	Pinned bool
}

// CAAMSeatDecision is the planner's answer for one pane.
type CAAMSeatDecision struct {
	Provider     string
	Profile      string
	Persona      string
	Registered   *persona.Persona
	TypeMismatch bool
	Skip         bool
	Reason       string
}

// LaunchCommand binds the entire live shell command to the selected account.
// PersonaName is template input, not an account boundary: default templates
// must honor selection even when they never mention that field. Persist the
// inner command and Profile separately so recovery does not nest wrappers.
func (d CAAMSeatDecision) LaunchCommand(binary, command string) (string, error) {
	if d.Skip {
		return "", fmt.Errorf("CAAM seat refused: %s", d.Reason)
	}
	return resilience.BindLaunchCommand(d.Provider, binary, &resilience.LaunchBinding{
		Provider: d.Provider, Launcher: "caam", Identifier: d.Profile,
	}, command)
}

// ValidateCAAMSeatCompatibility rejects Claude isolation when it would override
// an automatically selected or inherited account. Explicit persona pins retain
// their isolation policy only outside an inherited profile. Run before hooks or
// worktree/session mutations, including when automatic selection is disabled.
func ValidateCAAMSeatCompatibility(cfg *config.Config, candidates []CAAMSeatCandidate) error {
	if cfg == nil || !cfg.Agents.ClaudeIsolateCredentials {
		return nil
	}
	inherited := resilience.CaptureLaunchBinding("claude") != nil
	for _, candidate := range candidates {
		bound := inherited || CAAMSeatSelectionEnabled(cfg) && !candidate.Pinned
		if bound && CAAMSeatProviderForAgentType(candidate.Type) == "claude" {
			return resilience.ErrCAAMClaudeIsolationConflict
		}
	}
	return nil
}

// CAAMSeatSkip reports one pane that CAAM seat selection declined to launch,
// and why (ntm#319). Shared across CLI, Robot, REST, and swarm_spawn JSON
// envelopes so every surface emits an identical schema.
type CAAMSeatSkip struct {
	AgentType string `json:"agent_type"`
	Provider  string `json:"provider,omitempty"`
	Reason    string `json:"reason"`
}

// CAAMSeatPlan holds the per-pane decisions for one spawn/add batch. A nil plan
// is the disabled case and answers "no decision" for every index.
type CAAMSeatPlan struct {
	decisions map[int]CAAMSeatDecision
}

// For returns the decision recorded for a pane index, if any.
func (p *CAAMSeatPlan) For(index int) (CAAMSeatDecision, bool) {
	if p == nil {
		return CAAMSeatDecision{}, false
	}
	decision, ok := p.decisions[index]
	return decision, ok
}

// Seat returns the persona a pane should launch under, and false when the plan
// has nothing to say about it (disabled, pinned, skipped, or unsupported type).
func (p *CAAMSeatPlan) Seat(index int) (CAAMSeatDecision, bool) {
	decision, ok := p.For(index)
	if !ok || decision.Skip {
		return CAAMSeatDecision{}, false
	}
	return decision, true
}

// SkipReason returns why a pane must not launch, and false when it may.
func (p *CAAMSeatPlan) SkipReason(index int) (string, bool) {
	decision, ok := p.For(index)
	if !ok || !decision.Skip {
		return "", false
	}
	return decision.Reason, true
}

// PlanCAAMSeats resolves the seat every unpinned, CAAM-backed pane in a batch
// should launch on using the default CAAM adapter.
func PlanCAAMSeats(
	ctx context.Context,
	cfg *config.Config,
	projectDir string,
	candidates []CAAMSeatCandidate,
) *CAAMSeatPlan {
	if !CAAMSeatSelectionEnabled(cfg) {
		return nil
	}
	binary := strings.TrimSpace(cfg.Integrations.CAAM.BinaryPath)
	if binary == "" {
		binary = "caam"
	}
	resolver := &tools.CAAMAdapter{BaseAdapter: tools.NewBaseAdapter(tools.ToolCAAM, binary)}
	return PlanCAAMSeatsWithResolver(ctx, cfg, projectDir, candidates, resolver)
}

// PlanCAAMSeatsWithResolver resolves the seat every unpinned, CAAM-backed pane
// in a batch should launch on using the supplied resolver. Shells out to caam
// AT MOST ONCE PER PROVIDER in deterministic sorted order. Returns nil when the
// feature is off or no pane needs a seat.
func PlanCAAMSeatsWithResolver(
	ctx context.Context,
	cfg *config.Config,
	projectDir string,
	candidates []CAAMSeatCandidate,
	resolver CAAMSeatResolver,
) *CAAMSeatPlan {
	if !CAAMSeatSelectionEnabled(cfg) {
		return nil
	}

	providers := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.Pinned {
			continue
		}
		if provider := CAAMSeatProviderForAgentType(candidate.Type); provider != "" {
			providers[provider] = true
		}
	}
	if len(providers) == 0 {
		return nil
	}

	ordered := make([]string, 0, len(providers))
	for provider := range providers {
		ordered = append(ordered, provider)
	}
	sort.Strings(ordered)

	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		registry = nil
	}

	byProvider := make(map[string]CAAMSeatDecision, len(ordered))
	for _, provider := range ordered {
		if err := ValidateCAAMSeatCompatibility(cfg, []CAAMSeatCandidate{{Type: provider}}); err != nil {
			byProvider[provider] = CAAMSeatDecision{Provider: provider, Skip: true, Reason: err.Error()}
			continue
		}
		decision := resolveCAAMSeat(ctx, resolver, registry, provider)
		byProvider[provider] = decision
		if decision.Skip {
			slog.Warn("spawn seat selection skipped provider",
				"provider", provider,
				"reason", decision.Reason,
			)
		} else {
			slog.Info("spawn seat selection chose profile",
				"provider", provider,
				"profile", decision.Profile,
				"persona", decision.Persona,
			)
		}
	}

	plan := &CAAMSeatPlan{decisions: make(map[int]CAAMSeatDecision, len(candidates))}
	for _, candidate := range candidates {
		if candidate.Pinned {
			continue
		}
		provider := CAAMSeatProviderForAgentType(candidate.Type)
		if provider == "" {
			continue
		}
		decision := byProvider[provider]
		candidateCanonical := agent.AgentType(strings.TrimSpace(candidate.Type)).Canonical()
		if decision.Registered != nil {
			registeredCanonical := agent.AgentType(decision.Registered.AgentTypeFlag()).Canonical()
			if registeredCanonical != candidateCanonical {
				decision.Registered = nil
				decision.TypeMismatch = true
			}
		}
		plan.decisions[candidate.Index] = decision
	}
	return plan
}

func resolveCAAMSeat(
	ctx context.Context,
	resolver CAAMSeatResolver,
	registry *persona.Registry,
	provider string,
) CAAMSeatDecision {
	if resolver == nil {
		return CAAMSeatDecision{
			Provider: provider,
			Skip:     true,
			Reason:   fmt.Sprintf("no caam adapter available to rank the %s pool", provider),
		}
	}

	seat, err := resolver.RankedSeat(ctx, provider, "")
	if err != nil {
		return CAAMSeatDecision{
			Provider: provider,
			Skip:     true,
			Reason:   CAAMSeatSkipReason(provider, err),
		}
	}

	name := seat.Persona()
	if name == "" {
		return CAAMSeatDecision{
			Provider: provider,
			Skip:     true,
			Reason:   fmt.Sprintf("caam ranked the %s pool but returned no usable profile name", provider),
		}
	}

	decision := CAAMSeatDecision{
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

// CAAMSeatSkipReason turns a limits failure into an actionable sentence while
// preserving the distinction between transport errors and pool exhaustion.
func CAAMSeatSkipReason(provider string, err error) string {
	switch {
	case errors.Is(err, tools.ErrToolNotInstalled):
		return fmt.Sprintf("seat_selection is enabled but caam is not installed, so the %s seat cannot be chosen", provider)
	case errors.Is(err, tools.ErrCAAMNoSeatSelectable):
		return fmt.Sprintf("no %s seat has included headroom (%s)", provider, caamSeatErrorDetail(err))
	case errors.Is(err, tools.ErrTimeout):
		return fmt.Sprintf("caam limits timed out reading the %s pool", provider)
	default:
		return fmt.Sprintf("could not read caam limits for %s: %v", provider, err)
	}
}

func caamSeatErrorDetail(err error) string {
	text := err.Error()
	prefix := tools.ErrCAAMNoSeatSelectable.Error() + ": "
	if trimmed, ok := strings.CutPrefix(text, prefix); ok {
		return trimmed
	}
	return text
}

// CAAMSeatModelOverride reports the model a ranked seat's persona contributes
// to a pane when no explicit model was requested by the operator.
func CAAMSeatModelOverride(p *persona.Persona, modelRequested bool) (string, bool) {
	if p == nil || modelRequested {
		return "", false
	}
	model := strings.TrimSpace(p.Model)
	if model == "" {
		return "", false
	}
	return model, true
}

// CAAMSeatSkipMessage formats the operator-facing warning for a skipped pane.
func CAAMSeatSkipMessage(agentType, reason string) string {
	return fmt.Sprintf("seat selection skipped a %s pane: %s", agentType, reason)
}

// CAAMSeatTypeMismatchMessage warns that a persona of the seat's name exists
// but declares a different agent type, so only the seat name was pinned.
func CAAMSeatTypeMismatchMessage(agentType, personaName string) string {
	return fmt.Sprintf(
		"persona %q does not declare agent type %q (set agent_type on it); pinning the seat name only",
		personaName, agentType)
}

// ============================================================================
// Worktree Isolation (shared across CLI, Robot, REST & swarm_spawn)
// ============================================================================

// WorktreeAgentName builds the directory-name component for an agent's worktree
// (e.g. "cc_1", "cod_2"), using override when provided for single-agent spawns.
func WorktreeAgentName(agentType string, index int, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	canonical := agent.AgentType(strings.TrimSpace(agentType)).Canonical()
	short := string(canonical)
	if short == "" {
		short = strings.ToLower(strings.TrimSpace(agentType))
	}
	return fmt.Sprintf("%s_%d", short, index)
}

// ValidateSpawnWorktreeOptions rejects invalid worktree flag combinations
// before any external or filesystem side effects occur.
func ValidateSpawnWorktreeOptions(useWorktrees bool, worktreeName string, agentCount int) error {
	if !useWorktrees {
		return nil
	}
	if strings.TrimSpace(worktreeName) != "" && agentCount > 1 {
		return fmt.Errorf(
			"--worktree-name is only valid for single-agent spawns; got %d agents",
			agentCount,
		)
	}
	return nil
}

// PreflightWorktreeProject verifies that projectDir is an existing Git working
// tree with a valid HEAD commit before hooks or worktree creation run.
func PreflightWorktreeProject(ctx context.Context, projectDir string) error {
	if ctx == nil {
		return errors.New("worktree preflight requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("worktree preflight canceled: %w", err)
	}

	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return errors.New("worktree preflight requires a project directory")
	}
	info, err := os.Stat(projectDir)
	if err != nil {
		return fmt.Errorf("cannot create isolated agent worktrees: inspect project %s: %w", projectDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cannot create isolated agent worktrees: project path is not a directory: %s", projectDir)
	}

	runGit := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", projectDir}, args...)...)
		out, commandErr := cmd.CombinedOutput()
		trimmed := strings.TrimSpace(string(out))
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trimmed, fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		return trimmed, commandErr
	}

	insideWorktree, err := runGit("rev-parse", "--is-inside-work-tree")
	if err != nil || insideWorktree != "true" {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		detail := insideWorktree
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return fmt.Errorf(
			"cannot create isolated agent worktrees.\n\n"+
				"Project: %s\n"+
				"Problem: this path is not an initialized Git working tree (%s).\n\n"+
				"Initialize the repository and create its first commit, or rerun without --worktrees",
			projectDir, detail,
		)
	}

	headDetail, err := runGit("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		if headDetail == "" {
			headDetail = err.Error()
		}
		return fmt.Errorf(
			"cannot create isolated agent worktrees.\n\n"+
				"Project: %s\n"+
				"Problem: the Git repository has no valid HEAD commit (%s).\n\n"+
				"Create the initial commit, or rerun without --worktrees",
			projectDir, headDetail,
		)
	}

	return nil
}

// SpawnWorktreePlan tracks provisioned agent worktrees for a single spawn and
// revalidates each checkout at the launch boundary so isolation never silently
// falls back to the shared project root.
type SpawnWorktreePlan struct {
	manager *worktrees.WorktreeManager
	paths   map[string]string
}

// PreflightSpawnWorktrees validates the Git project and every requested agent
// worktree name before hooks or tmux mutations run.
func PreflightSpawnWorktrees(ctx context.Context, projectDir, sessionName string, agentNames []string) (*worktrees.WorktreeManager, error) {
	if err := PreflightWorktreeProject(ctx, projectDir); err != nil {
		return nil, err
	}
	mgr := worktrees.NewManager(projectDir, sessionName)
	for _, agentName := range agentNames {
		if err := mgr.PreflightForAgent(ctx, agentName); err != nil {
			return nil, fmt.Errorf("preflight worktree for agent %s: %w", agentName, err)
		}
	}
	return mgr, nil
}

// ProvisionSpawnWorktrees creates and revalidates isolated worktrees for every
// agent name in order. Returns the plan, any created paths (for partial
// mutation diagnostics even on error), and any error.
func ProvisionSpawnWorktrees(ctx context.Context, mgr *worktrees.WorktreeManager, agentNames []string) (*SpawnWorktreePlan, []string, error) {
	if mgr == nil {
		return nil, nil, errors.New("worktree manager is unavailable")
	}
	paths := make(map[string]string, len(agentNames))
	var affected []string
	for _, agentName := range agentNames {
		if err := ctx.Err(); err != nil {
			return nil, affected, fmt.Errorf("worktree creation canceled: %w", err)
		}
		info, createErr := mgr.CreateForAgent(ctx, agentName)
		if info != nil && info.Created && strings.TrimSpace(info.Path) != "" {
			affected = append(affected, info.Path)
		}
		if createErr != nil {
			return nil, affected, fmt.Errorf("failed to create worktree for agent %s: %w", agentName, createErr)
		}
		if info == nil || strings.TrimSpace(info.Path) == "" {
			return nil, affected, fmt.Errorf("failed to create worktree for agent %s: manager returned no path", agentName)
		}
		paths[agentName] = info.Path
	}

	plan := &SpawnWorktreePlan{manager: mgr, paths: paths}
	for _, agentName := range agentNames {
		if _, err := plan.ResolveLaunchDir(ctx, agentName); err != nil {
			return nil, affected, fmt.Errorf("validate provisioned worktree for agent %s: %w", agentName, err)
		}
	}
	return plan, affected, nil
}

// ResolveLaunchDir revalidates an agent's provisioned worktree right before
// command dispatch and returns its isolated checkout path.
func (p *SpawnWorktreePlan) ResolveLaunchDir(ctx context.Context, agentName string) (string, error) {
	if p == nil || p.manager == nil {
		return "", errors.New("worktree manager is unavailable")
	}
	expectedPath, provisioned := p.paths[agentName]
	if !provisioned || strings.TrimSpace(expectedPath) == "" {
		return "", fmt.Errorf("no provisioned worktree for agent %s", agentName)
	}
	info, err := p.manager.GetWorktreeForAgent(ctx, agentName)
	if err != nil {
		return "", fmt.Errorf("validate worktree for agent %s: %w", agentName, err)
	}
	if info == nil || !info.Created || info.Error != "" || info.Path != expectedPath {
		detail := "manager returned no worktree"
		if info != nil {
			detail = info.Error
			if detail == "" {
				detail = fmt.Sprintf("path %q does not match provisioned path %q", info.Path, expectedPath)
			}
		}
		return "", fmt.Errorf("worktree for agent %s is not launchable: %s", agentName, detail)
	}
	return expectedPath, nil
}

// ApplyWorktreeAgentMailProjectEnv injects AGENT_MAIL_PROJECT=projectDir when
// launching inside a worktree unless already set by plugin or user pane env.
func ApplyWorktreeAgentMailProjectEnv(envVars, userEnv map[string]string, projectDir string) (map[string]string, bool) {
	if _, fromPlugin := envVars["AGENT_MAIL_PROJECT"]; fromPlugin {
		return envVars, false
	}
	if _, fromUser := userEnv["AGENT_MAIL_PROJECT"]; fromUser {
		return envVars, false
	}
	out := make(map[string]string, len(envVars)+1)
	for k, v := range envVars {
		out[k] = v
	}
	out["AGENT_MAIL_PROJECT"] = projectDir
	return out, true
}

// ============================================================================
// Persona & Profile-Set Resolution (shared across CLI, Robot, REST & swarm_spawn)
// ============================================================================

// PersonaSpec represents a parsed persona specification ("name" or "name:count").
type PersonaSpec struct {
	Name  string
	Count int
}

// ResolvedPersonaSlot represents one concrete agent slot derived from a persona.
type ResolvedPersonaSlot struct {
	Persona         *persona.Persona
	AgentType       agent.AgentType
	Model           string
	ReasoningEffort string
	Index           int
}

// ParsePersonaSpec parses a single persona specification string ("name" or "name:count").
func ParsePersonaSpec(value string) (PersonaSpec, error) {
	spec := PersonaSpec{Count: 1}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return spec, fmt.Errorf("invalid persona spec: %q", value)
	}
	spec.Name = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		countStr := strings.TrimSpace(parts[1])
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return spec, fmt.Errorf("invalid count in persona spec %q: %w", value, err)
		}
		if count < 1 {
			return spec, fmt.Errorf("count must be at least 1 in persona spec: %q", value)
		}
		spec.Count = count
	}
	return spec, nil
}

// ResolveSpawnPersonas resolves persona specs against the project/user/builtin
// persona registry into concrete per-agent slots.
func ResolveSpawnPersonas(specs []PersonaSpec, projectDir string) ([]ResolvedPersonaSlot, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return nil, fmt.Errorf("loading persona registry: %w", err)
	}

	var slots []ResolvedPersonaSlot
	for _, spec := range specs {
		p, found := registry.Get(spec.Name)
		if !found {
			available := registry.List()
			names := make([]string, 0, len(available))
			for _, ap := range available {
				names = append(names, ap.Name)
			}
			return nil, fmt.Errorf("unknown persona %q (available: %s)", spec.Name, strings.Join(names, ", "))
		}
		agentType := agent.AgentType(p.AgentTypeFlag()).Canonical()
		for i := 1; i <= spec.Count; i++ {
			slots = append(slots, ResolvedPersonaSlot{
				Persona:         p,
				AgentType:       agentType,
				Model:           p.Model,
				ReasoningEffort: p.ReasoningEffort,
				Index:           i,
			})
		}
	}
	return slots, nil
}

// LoadSpawnProfileList loads the ordered personas for either a named profile
// set (--profile-set) or an explicit list of profile names (--profiles).
func LoadSpawnProfileList(projectDir, profileSet string, profileNames []string) ([]*persona.Persona, error) {
	profileSet = strings.TrimSpace(profileSet)
	if profileSet == "" && len(profileNames) == 0 {
		return nil, nil
	}
	if profileSet != "" && len(profileNames) > 0 {
		return nil, errors.New("cannot use both profiles and profile-set; pick one")
	}

	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return nil, fmt.Errorf("loading persona registry: %w", err)
	}

	names := profileNames
	if profileSet != "" {
		pset, ok := registry.GetSet(profileSet)
		if !ok {
			sets := registry.ListSets()
			available := make([]string, 0, len(sets))
			for _, s := range sets {
				available = append(available, s.Name)
			}
			return nil, fmt.Errorf("profile set %q not found; available: %s", profileSet, strings.Join(available, ", "))
		}
		names = pset.Personas
	}

	var out []*persona.Persona
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		p, ok := registry.Get(name)
		if !ok {
			return nil, fmt.Errorf("profile %q not found in registry", name)
		}
		out = append(out, p)
	}
	return out, nil
}

// ValidateProfileAgentDistribution fails closed when the per-type agent
// distribution implied by a persona set does not match explicitly requested counts.
func ValidateProfileAgentDistribution(personaCounts, requestedCounts map[agent.AgentType]int) error {
	typeSet := make(map[agent.AgentType]struct{})
	for t := range personaCounts {
		typeSet[t.Canonical()] = struct{}{}
	}
	for t := range requestedCounts {
		typeSet[t.Canonical()] = struct{}{}
	}
	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, string(t))
	}
	sort.Strings(types)

	normPersona := make(map[agent.AgentType]int, len(personaCounts))
	for t, c := range personaCounts {
		normPersona[t.Canonical()] += c
	}
	normRequested := make(map[agent.AgentType]int, len(requestedCounts))
	for t, c := range requestedCounts {
		normRequested[t.Canonical()] += c
	}

	var mismatches []string
	for _, ts := range types {
		t := agent.AgentType(ts)
		if normPersona[t] != normRequested[t] {
			mismatches = append(mismatches, fmt.Sprintf("%s: profile-set defines %d, you requested %d", ts, normPersona[t], normRequested[t]))
		}
	}
	if len(mismatches) > 0 {
		return fmt.Errorf(
			"profile-set agent distribution conflicts with the requested agent counts (%s); either drop the per-type counts and let --profile-set drive the spawn, or make the counts match the persona set exactly",
			strings.Join(mismatches, "; "),
		)
	}
	return nil
}

// PrepareRequiredPersonaSystemPrompt materializes a persona's rendered system
// prompt file and fails closed if writing fails.
func PrepareRequiredPersonaSystemPrompt(p *persona.Persona, projectDir string) (string, error) {
	if p == nil {
		return "", nil
	}
	promptFile, err := persona.PrepareSystemPrompt(p, projectDir)
	if err != nil {
		return "", fmt.Errorf("prepare system prompt for persona %s: %w", p.Name, err)
	}
	return promptFile, nil
}

// PersonaPromptSHA256 freezes the prepared prompt's identity for launch replay.
// Both CLI and automated launches use the same bounded, regular-file check.
func PersonaPromptSHA256(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open prepared persona prompt: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("prepared persona prompt must be a readable regular file")
	}
	hash := sha256.New()
	const maxPromptBytes = 1 << 20
	count, err := io.Copy(hash, io.LimitReader(file, maxPromptBytes+1))
	if err != nil {
		return "", fmt.Errorf("read prepared persona prompt: %w", err)
	}
	if count > maxPromptBytes {
		return "", errors.New("prepared persona prompt exceeds launch metadata verification limit")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// ============================================================================
// Pre-Spawn & Post-Spawn Hooks (shared across CLI, Robot, REST & swarm_spawn)
// ============================================================================

// SpawnHookCounts holds per-harness agent counts exported to spawn hooks.
type SpawnHookCounts struct {
	CC       int
	Cod      int
	Gmi      int
	Agy      int
	Grok     int
	Cursor   int
	Windsurf int
	Aider    int
	Opencode int
	Omp      int
	Ollama   int
	Total    int
}

// SpawnHookEnv builds the canonical NTM_AGENT_COUNT_* hook environment map.
func SpawnHookEnv(counts SpawnHookCounts) map[string]string {
	return map[string]string{
		"NTM_AGENT_COUNT_CC":       strconv.Itoa(counts.CC),
		"NTM_AGENT_COUNT_COD":      strconv.Itoa(counts.Cod),
		"NTM_AGENT_COUNT_GMI":      strconv.Itoa(counts.Gmi),
		"NTM_AGENT_COUNT_AGY":      strconv.Itoa(counts.Agy),
		"NTM_AGENT_COUNT_GROK":     strconv.Itoa(counts.Grok),
		"NTM_AGENT_COUNT_CURSOR":   strconv.Itoa(counts.Cursor),
		"NTM_AGENT_COUNT_WINDSURF": strconv.Itoa(counts.Windsurf),
		"NTM_AGENT_COUNT_AIDER":    strconv.Itoa(counts.Aider),
		"NTM_AGENT_COUNT_OC":       strconv.Itoa(counts.Opencode),
		"NTM_AGENT_COUNT_OMP":      strconv.Itoa(counts.Omp),
		"NTM_AGENT_COUNT_OLLAMA":   strconv.Itoa(counts.Ollama),
		"NTM_AGENT_COUNT_TOTAL":    strconv.Itoa(counts.Total),
	}
}

// LoadSpawnHooks loads the configured command hook executor unless disabled.
// If hook loading fails, returns an empty executor alongside the load error so
// callers can warn without crashing spawns that have no hooks configured.
func LoadSpawnHooks(disabled bool) (*hooks.Executor, error) {
	if disabled {
		return nil, nil
	}
	exec, err := hooks.NewExecutorFromConfig()
	if err != nil {
		return hooks.NewExecutor(nil), err
	}
	return exec, nil
}

// RunPreSpawnHooks executes configured EventPreSpawn hooks with a 5-minute
// timeout budget. Returns an error if any hook fails or context is canceled.
func RunPreSpawnHooks(ctx context.Context, exec *hooks.Executor, sessionName, projectDir string, extraEnv map[string]string) ([]hooks.ExecutionResult, error) {
	if exec == nil || !exec.HasHooksForEvent(hooks.EventPreSpawn) {
		return nil, nil
	}
	hookCtx := hooks.ExecutionContext{
		SessionName:   sessionName,
		ProjectDir:    projectDir,
		AdditionalEnv: cloneStringMap(extraEnv),
	}
	hookRunCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	results, err := exec.RunHooksForEvent(hookRunCtx, hooks.EventPreSpawn, hookCtx)
	if err != nil {
		return results, fmt.Errorf("pre-spawn hook failed: %w", err)
	}
	if hooks.AnyFailed(results) {
		return results, fmt.Errorf("pre-spawn hook failed: %w", hooks.AllErrors(results))
	}
	return results, nil
}

// RunPostSpawnHooks executes configured EventPostSpawn hooks with final pane
// metadata. Callers treat post-spawn hook errors as non-fatal warnings unless
// the parent context was canceled.
func RunPostSpawnHooks(ctx context.Context, exec *hooks.Executor, sessionName, projectDir string, extraEnv map[string]string, paneCount int, paneTitles []string) ([]hooks.ExecutionResult, error) {
	if exec == nil || !exec.HasHooksForEvent(hooks.EventPostSpawn) {
		return nil, nil
	}
	env := cloneStringMap(extraEnv)
	env["NTM_PANE_COUNT"] = strconv.Itoa(paneCount)
	env["NTM_PANE_TITLES"] = strings.Join(paneTitles, ",")
	env["NTM_SPAWN_SUCCESS"] = "true"
	hookCtx := hooks.ExecutionContext{
		SessionName:   sessionName,
		ProjectDir:    projectDir,
		AdditionalEnv: env,
	}
	hookRunCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	results, err := exec.RunHooksForEvent(hookRunCtx, hooks.EventPostSpawn, hookCtx)
	if err != nil {
		return results, err
	}
	if hooks.AnyFailed(results) {
		return results, hooks.AllErrors(results)
	}
	return results, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+3)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ============================================================================
// Gemini Post-Spawn Setup (shared across CLI, Add, Robot, REST & swarm_spawn)
// ============================================================================

// RunGeminiPostSpawnSetup performs Gemini Pro-model auto-selection for a
// freshly spawned Gemini pane when `[gemini_setup] auto_select_pro_model = true`.
// Returns nil immediately when disabled or cfg is nil.
func RunGeminiPostSpawnSetup(ctx context.Context, cfg *config.Config, paneID string) error {
	if cfg == nil || !cfg.GeminiSetup.AutoSelectProModel {
		return nil
	}
	geminiCfg := gemini.SetupConfig{
		AutoSelectProModel: cfg.GeminiSetup.AutoSelectProModel,
		ReadyTimeout:       time.Duration(cfg.GeminiSetup.ReadyTimeoutSeconds) * time.Second,
		ModelSelectTimeout: time.Duration(cfg.GeminiSetup.ModelSelectTimeoutSeconds) * time.Second,
		PollInterval:       500 * time.Millisecond,
		Verbose:            cfg.GeminiSetup.Verbose,
	}
	setupCtx, setupCancel := context.WithTimeout(ctx, geminiCfg.ReadyTimeout+geminiCfg.ModelSelectTimeout+10*time.Second)
	defer setupCancel()
	if err := gemini.PostSpawnSetup(setupCtx, paneID, geminiCfg); err != nil {
		slog.Warn("gemini post-spawn setup failed",
			"pane", paneID,
			"harness", string(agent.AgentTypeGemini),
			"error", err,
		)
		return err
	}
	slog.Info("gemini post-spawn setup configured Pro model",
		"pane", paneID,
		"harness", string(agent.AgentTypeGemini),
	)
	return nil
}
