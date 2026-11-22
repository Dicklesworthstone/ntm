// Package spawnidentity provisions the Agent Mail identity of every agent
// pane NTM launches, immediately before the agent process starts, and keeps
// the tmux pane badges that display those identities reconciled.
//
// It is the single implementation shared by every launch surface (ORI):
// `ntm spawn`, `ntm add`, `ntm adopt` and the relaunch hook (internal/cli),
// and `--robot-spawn` plus the REST spawn endpoints, which call the robot
// spawn engine (internal/robot, internal/serve). Configuration arrives as
// Options rather than package globals, so each surface binds its own
// configuration and diagnostics sink.
package spawnidentity

import (
	"context"
	"errors"
	"strings"
	"time"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AgentMailSpawnStatus represents Agent Mail registration status for a spawn
// operation. It is the agent_mail object of `ntm spawn --json`, `ntm add
// --json`, `--robot-spawn` and the REST spawn endpoints.
type AgentMailSpawnStatus struct {
	Available         bool              `json:"available"`
	ProjectRegistered bool              `json:"project_registered"`
	AgentsRegistered  int               `json:"agents_registered"`
	AgentsFailed      int               `json:"agents_failed"`
	AgentMap          map[string]string `json:"agent_map,omitempty"` // stable %pane_id -> agent name
	// SessionAgent is the session-level identity `ntm lock` and the session
	// coordinator act as (RegisterSession); absent when it was not registered.
	SessionAgent string `json:"session_agent,omitempty"`
}

// Agent describes one agent pane whose identity is prepared.
type Agent struct {
	PaneIndex     int
	PaneID        string
	PaneTitle     string
	AgentType     string
	Model         string
	ResolvedModel string
	// PaneDir is the directory the agent process is launched in when it
	// differs from the session's project key (linked worktrees). Empty for
	// ordinary panes. The identity is additionally published under this key
	// so cwd-derived resolution finds it (ntm#257).
	PaneDir string
}

// Reporter receives the coordinator's human-facing diagnostics. A nil
// Reporter discards them (machine-readable surfaces report through the
// returned status instead).
type Reporter interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}

// Options binds one coordinator to a surface's configuration and ports.
type Options struct {
	// Enabled is the registration gate: [agent_mail] enabled AND
	// auto_register (#243). When false the coordinator never contacts Agent
	// Mail and reports a nil status.
	Enabled bool
	// ClientOptions configure the Agent Mail client (endpoint, bearer token).
	ClientOptions []agentmail.Option
	// PaneBadges enables tmux pane badge publication (ntm#312); it only
	// takes effect when Enabled is also set.
	PaneBadges bool
	// BadgeTemplate is the badge label template; empty selects
	// agentmail.DefaultBadgeTemplate.
	BadgeTemplate string
	// PreLaunch is true on the spawn paths, where PrepareAgent runs before
	// the agent command is sent: each pane's identity badge is then
	// published with lifecycle=starting right after its identity (ntm#312).
	// The batch path (add/adopt/relaunch) reconciles once at the end
	// instead, because its agents are already running.
	PreLaunch bool
	// Reporter receives diagnostics; nil discards them.
	Reporter Reporter
	// ListPanes lists a session's panes for liveness judgement; nil uses
	// tmux.GetPanesContext.
	ListPanes func(context.Context, string) ([]tmux.Pane, error)
	// BadgeTmux is the tmux surface badge publication writes through; nil
	// uses the default tmux client.
	BadgeTmux BadgeTmux
}

// RegistrationEnabled reports whether a launch may contact Agent Mail to
// register identities. It fails closed: a missing config never authorizes a
// network-facing side effect, and both the top-level toggle and the
// auto_register preference must be on. This is a user-preference and privacy
// boundary (#243): registration sends the absolute project path, session
// metadata, and any configured bearer token to the configured endpoint.
func RegistrationEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.AgentMail.Enabled && cfg.AgentMail.AutoRegister
}

// BadgesEnabled reports whether the config asks for pane badges. Badges ride
// on identity assignment, so they need the same gate as registration
// (enabled + auto_register, #243) plus the pane_badges toggle.
func BadgesEnabled(cfg *config.Config) bool {
	return RegistrationEnabled(cfg) && cfg.AgentMail.PaneBadgesOrDefault()
}

// BadgeTemplate returns the configured badge label template.
func BadgeTemplate(cfg *config.Config) string {
	if cfg == nil {
		return agentmail.DefaultBadgeTemplate
	}
	return cfg.AgentMail.PaneBadgeFormatOrDefault()
}

// ConfigOptions derives the configuration-bound Options (gate, client
// endpoint and token, badge toggle and template) from cfg. Callers add the
// surface-specific fields (PreLaunch, Reporter, ports).
func ConfigOptions(cfg *config.Config) Options {
	opts := Options{
		Enabled:       RegistrationEnabled(cfg),
		PaneBadges:    BadgesEnabled(cfg),
		BadgeTemplate: BadgeTemplate(cfg),
	}
	if cfg != nil {
		opts.ClientOptions = agentmail.ConfigOptions(cfg.AgentMail.URL, cfg.AgentMail.Token)
	}
	return opts
}

// sessionRegistrationTimeout bounds RegisterSession's Agent Mail calls.
const sessionRegistrationTimeout = 15 * time.Second

// RegisterSession registers (or refreshes) the session-level coordinator
// identity: the agent.json sender that `ntm lock`/`unlock`/`locks` and the
// session coordinator act as. Every spawn surface calls it once per launch,
// so a session spawned by the robot API or REST is as lockable as one
// spawned by `ntm spawn`.
//
// It returns the identity name, or "" when registration is disabled or
// failed. Failures go to the Reporter and never fail the caller.
func RegisterSession(ctx context.Context, projectKey, session string, opts Options) string {
	if !opts.Enabled {
		return ""
	}
	if ctx == nil {
		if opts.Reporter != nil {
			opts.Reporter.Warnf("Agent Mail registration skipped: missing command context")
		}
		return ""
	}
	regCtx, cancel := context.WithTimeout(ctx, sessionRegistrationTimeout)
	defer cancel()
	info, err := agentmail.NewClient(opts.ClientOptions...).RegisterSessionAgent(regCtx, session, projectKey)
	if err != nil {
		if opts.Reporter != nil {
			opts.Reporter.Warnf("Agent Mail registration failed: %v", err)
		}
		return ""
	}
	if info == nil || info.AgentName == "" {
		return ""
	}
	if opts.Reporter != nil {
		opts.Reporter.Infof("Registered with Agent Mail as %s", info.AgentName)
	}
	return info.AgentName
}

// RegisterBatch registers already-running agents with Agent Mail and returns
// the aggregate status. It is the post-launch entry point used by add, adopt,
// and relaunch, where the agent processes already exist; the spawn paths use
// a Coordinator directly so each pane's identity is published BEFORE its
// agent command is sent (gh#255). Both share the same per-pane logic.
//
// Graceful degradation: Agent Mail unavailability never fails the caller.
// The status is nil when registration is disabled.
func RegisterBatch(ctx context.Context, projectKey, session string, agents []Agent, opts Options) *AgentMailSpawnStatus {
	if ctx == nil {
		return &AgentMailSpawnStatus{AgentsFailed: len(agents)}
	}
	opts.PreLaunch = false
	coordinator := New(projectKey, session, opts)
	for _, agent := range agents {
		coordinator.PrepareAgent(ctx, agent)
	}
	// The agents are already running: reconcile their pane badges in one
	// pass (ntm#312). Best-effort, never affects the registration status.
	coordinator.ReconcileBadges(ctx)
	return coordinator.Status()
}

// Coordinator owns one Agent Mail client, availability probe, project
// registration, session registry, and transient-busy reconciliation set for
// the lifetime of a spawn (or batch registration). Initialization is lazy:
// nothing contacts Agent Mail until the first PrepareAgent call, so a spawn
// with zero agents (or with registration disabled) pays no cost.
//
// The coordinator exists to fix a startup race (gh#255): identities used to
// be created and published only after every agent had been launched, so an
// agent that resolved its pane identity during boot could read a previous
// occupant's name — or none. PrepareAgent is designed to run immediately
// before the launch keystrokes for each pane, making the identity file and
// registry entry durable before the agent process starts.
//
// Failure policy: Agent Mail being disabled, unavailable, or failing
// per-pane never blocks the launch (graceful degradation); failures are
// counted and warned, not fatal.
type Coordinator struct {
	projectKey string
	session    string
	opts       Options

	initialized bool
	enabled     bool
	client      *agentmail.Client
	available   bool
	projectOK   bool
	registry    *agentmail.SessionAgentRegistry
	// reconciledIDs tracks agent IDs already claimed by transient-busy
	// reconciliation so two panes never match the same server-side agent.
	reconciledIDs map[int]bool
	status        *AgentMailSpawnStatus
	// livePanes maps pane id -> #{pane_pid} for the session as observed when
	// the coordinator initialized (refreshed on a miss); liveness derives from
	// it. Both are nil when the topology could not be read, which makes every
	// recorded holder count as live — the fail-safe is a fresh identity,
	// never a shared one (ntm#256).
	livePanes map[string]int
	liveness  agentmail.PaneLiveness
}

// New returns a lazily initialized coordinator for one session whose
// identities register under projectKey.
func New(projectKey, session string, opts Options) *Coordinator {
	return &Coordinator{
		projectKey:    projectKey,
		session:       session,
		opts:          opts,
		reconciledIDs: make(map[int]bool),
	}
}

func (c *Coordinator) infof(format string, args ...any) {
	if c.opts.Reporter != nil {
		c.opts.Reporter.Infof(format, args...)
	}
}

func (c *Coordinator) warnf(format string, args ...any) {
	if c.opts.Reporter != nil {
		c.opts.Reporter.Warnf(format, args...)
	}
}

func (c *Coordinator) listPanes(ctx context.Context) ([]tmux.Pane, error) {
	if c.opts.ListPanes != nil {
		return c.opts.ListPanes(ctx, c.session)
	}
	return tmux.GetPanesContext(ctx, c.session)
}

// ensureInit performs the one-time spawn-scoped setup: config gate, client
// construction, availability probe, registry load, and project registration.
func (c *Coordinator) ensureInit(parentCtx context.Context) {
	if c.initialized {
		return
	}
	c.initialized = true

	// Fail closed: no config never authorizes contacting Agent Mail, and both
	// enabled and auto_register must be on (#243).
	if !c.opts.Enabled {
		return
	}
	c.enabled = true

	c.client = agentmail.NewClient(c.opts.ClientOptions...)

	// Do not gate spawn registration on the full MCP health_check probe. Its
	// availability budget is deliberately small, while a healthy Agent Mail
	// server under database pressure may need longer to assemble the
	// diagnostic snapshot; gating on it made a loaded-but-healthy server
	// suppress every pane identity. ensure_project below is the operation
	// spawn actually needs and is a sufficient (and lighter) reachability
	// check — treat its response as authoritative.
	c.status = &AgentMailSpawnStatus{
		Available: false,
		AgentMap:  make(map[string]string),
	}

	// Load existing registry to reuse identities on respawn (#69),
	// falling back to a fresh registry if none exists.
	c.registry, _ = agentmail.LoadSessionAgentRegistry(c.session, c.projectKey)
	if c.registry == nil {
		c.registry = agentmail.NewSessionAgentRegistry(c.session, c.projectKey)
	}
	c.observeLivePanes(parentCtx)

	// Ensure project exists; success also marks Agent Mail available.
	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()
	if _, err := c.client.EnsureProject(ctx, c.projectKey); err != nil {
		c.warnf("Agent Mail project registration failed: %v", err)
		return
	}
	c.available = true
	c.status.Available = true
	c.projectOK = true
	c.status.ProjectRegistered = true
}

// PrepareAgent reuses or creates the Agent Mail identity for one pane and
// publishes it (canonical identity file, legacy compat file, session
// registry) so the identity is resolvable before the agent process starts.
// All failures degrade gracefully: they are counted in the aggregate status
// and never block the pane's launch.
func (c *Coordinator) PrepareAgent(parentCtx context.Context, agent Agent) {
	if parentCtx == nil {
		return
	}
	c.ensureInit(parentCtx)
	if !c.enabled {
		return
	}
	if !c.available || !c.projectOK {
		c.status.AgentsFailed++
		return
	}
	if parentCtx.Err() != nil {
		c.status.AgentsFailed++
		return
	}

	// Reuse the identity of a prior occupant of this slot (#69) — but only a
	// DEAD one. The pane id is the primary key; a matching title whose
	// recorded pane is still live means the slot is occupied and the running
	// agent keeps its name, so this pane gets a fresh identity (ntm#256).
	if existingName, recoveredFrom, ok := c.registry.ResolveForPane(agent.PaneTitle, agent.PaneID, c.liveness); ok && existingName != "" {
		// Best-effort: re-register the reused name bound to THIS pane so the
		// server's pane-binding generation receipt follows the new pane
		// instead of the dead one it recovered from. Reuse deliberately does
		// not depend on the server (#69: a same-session respawn gets its name
		// back even offline), so a failed or timed-out re-registration only
		// means the binding refresh waits for the next opportunity.
		reuseProgram := agentTypeToProgram(agent.AgentType)
		reuseModel := agent.ResolvedModel
		if reuseModel == "" {
			reuseModel = agent.Model
		}
		if strings.TrimSpace(reuseModel) == "" {
			reuseModel = delegatedModelPlaceholder(reuseProgram)
		}
		// Re-claiming an existing name on mcp-agent-mail >=2.13 needs its
		// registration token; prime the client's cache from the registry.
		c.registry.HydrateClientTokens(c.client)
		reregCtx, reregCancel := context.WithTimeout(parentCtx, 15*time.Second)
		reregistered, reregErr := c.client.RegisterAgent(reregCtx, agentmail.RegisterAgentOptions{
			ProjectKey: c.projectKey,
			Program:    reuseProgram,
			Model:      reuseModel,
			Name:       existingName,
			PaneID:     agent.PaneID,
		})
		reregCancel()
		// Re-registration can ROTATE this identity's registration token:
		// mcp-agent-mail >=2.13 may issue a fresh credential when it re-binds
		// an existing name to a new pane, and the previous one then stops
		// authenticating the agent. Persisting the replacement in the session
		// registry BEFORE the agent process starts is what makes a restarted
		// worker inherit a credential the server still accepts (ntm#321).
		//
		// Deliberately conservative: a failed or timed-out re-registration,
		// or a response carrying no token, leaves the recorded token
		// untouched — reuse must survive an offline server (#69), and
		// SetRegistrationToken("") would DELETE the entry. A response naming
		// a different identity is not ours to record against this name.
		if reregErr == nil && reregistered != nil &&
			reregistered.RegistrationToken != "" &&
			(reregistered.Name == "" || reregistered.Name == existingName) {
			c.registry.SetRegistrationToken(existingName, reregistered.RegistrationToken)
		}

		c.status.AgentsRegistered++
		c.status.AgentMap[agent.PaneID] = existingName
		if recoveredFrom != "" {
			c.infof("Reused existing identity for pane %d: %s (recovered from pane %s, no longer live)", agent.PaneIndex, existingName, recoveredFrom)
		} else {
			c.infof("Reused existing identity for pane %d: %s", agent.PaneIndex, existingName)
		}
		c.publishIdentity(agent, existingName)
		c.registry.AddAgent(agent.PaneTitle, agent.PaneID, existingName)
		c.recordPanePID(parentCtx, agent.PaneID)
		c.persistRegistry()
		c.publishStartingBadge(parentCtx, agent)
		return
	}

	// Map agent type to program name
	program := agentTypeToProgram(agent.AgentType)
	model := agent.ResolvedModel
	if model == "" {
		model = agent.Model
	}
	// Agent Mail rejects an empty model, but several agent types legitimately
	// delegate model selection to the CLI's own config (bare --oc=N, --grok=N,
	// plugins without a default). Register with a stable, clearly-delegated
	// delegation marker instead of failing the pane's identity (ntm#261).
	if strings.TrimSpace(model) == "" {
		model = delegatedModelPlaceholder(program)
		if !modelDelegationIsBuiltInDefault(agent.AgentType) {
			c.infof("No model resolved for pane %d (%s); registering with Agent Mail as %q — set models.default_%s or pass an explicit model to name it",
				agent.PaneIndex, agent.AgentType, model, modelDefaultKeyForType(agent.AgentType))
		}
	}

	regCtx, regCancel := context.WithTimeout(parentCtx, 15*time.Second)
	registered, err := c.client.CreateAgentIdentity(regCtx, agentmail.RegisterAgentOptions{
		ProjectKey: c.projectKey,
		Program:    program,
		Model:      model,
		PaneID:     agent.PaneID,
	})
	regCancel()

	if err != nil {
		// On transient busy errors, the agent may have been created server-side
		// despite the error. Reconcile by listing agents and checking.
		if errors.Is(err, agentmail.ErrTransientBusy) {
			reconcileCtx, reconcileCancel := context.WithTimeout(parentCtx, 5*time.Second)
			allAgents, listErr := c.client.ListAgents(reconcileCtx, c.projectKey)
			reconcileCancel()
			if listErr == nil {
				// Look for a recently-created agent matching our program/model
				// that hasn't already been claimed by a prior pane.
				var found *agentmail.Agent
				for i := range allAgents {
					if allAgents[i].Program == program && allAgents[i].Model == model {
						if !c.reconciledIDs[allAgents[i].ID] {
							if found == nil || allAgents[i].ID > found.ID {
								found = &allAgents[i]
							}
						}
					}
				}
				if found != nil {
					// Agent was actually created — treat as success
					c.reconciledIDs[found.ID] = true
					registered = found
					err = nil
					c.infof("Reconciled busy response for pane %d: agent %s exists", agent.PaneIndex, found.Name)
				}
			}
		}
		if err != nil {
			c.status.AgentsFailed++
			c.warnf("Agent Mail registration failed for pane %d: %v", agent.PaneIndex, err)
			return
		}
	}

	// Write the per-pane identity file(s) so Agent Mail and notify hooks can
	// resolve AGENT_MAIL_AGENT before the agent process starts.
	c.publishIdentity(agent, registered.Name)

	c.status.AgentsRegistered++
	c.status.AgentMap[agent.PaneID] = registered.Name

	// Add to registry for persistence
	c.registry.AddAgent(agent.PaneTitle, agent.PaneID, registered.Name)
	c.recordPanePID(parentCtx, agent.PaneID)
	// Persist the registration_token alongside the agent name so
	// later ntm processes can re-authenticate as this agent on
	// mcp-agent-mail >=2.13 (ntm#146).
	if registered.RegistrationToken != "" {
		c.registry.SetRegistrationToken(registered.Name, registered.RegistrationToken)
	}
	c.persistRegistry()
	c.publishStartingBadge(parentCtx, agent)

	c.infof("Registered agent pane %d as %s", agent.PaneIndex, registered.Name)
}

// persistRegistry saves the session registry after each mutation so the
// pane-to-name mapping is durable before the pane's agent process launches.
func (c *Coordinator) persistRegistry() {
	if c.registry == nil || c.registry.Count() == 0 {
		return
	}
	if err := agentmail.SaveSessionAgentRegistry(c.registry); err != nil {
		c.warnf("Failed to persist agent registry: %v", err)
	}
}

// Status returns the aggregate registration status for output assembly. It
// returns nil when registration is disabled or no agent was ever prepared.
// The pointer is live: later PrepareAgent calls keep updating it.
func (c *Coordinator) Status() *AgentMailSpawnStatus {
	return c.status
}

// observeLivePanes snapshots the session's pane ids and pids so registry
// bindings can be judged live or dead (ntm#256). When the topology cannot be
// read the snapshot is nil and ResolveForPane treats every recorded holder as
// live: titles then never trigger reuse, pane ids still do.
func (c *Coordinator) observeLivePanes(ctx context.Context) {
	panes, err := c.listPanes(ctx)
	if err != nil {
		c.livePanes = nil
		c.liveness = nil
		return
	}
	live := make(map[string]int, len(panes))
	for _, p := range panes {
		if p.ID != "" {
			live[p.ID] = p.PID
		}
	}
	c.livePanes = live
	c.liveness = LivenessFromPanes(panes)
}

// recordPanePID stores the registering pane's current #{pane_pid} beside its
// binding so a later process can tell this incarnation from a recycled %N.
// A pane absent from the init snapshot (created after it) triggers one
// refresh; an unknown pid is simply left unrecorded.
func (c *Coordinator) recordPanePID(ctx context.Context, paneID string) {
	if paneID == "" || c.registry == nil {
		return
	}
	pid, ok := c.livePanes[paneID]
	if !ok {
		c.observeLivePanes(ctx)
		pid = c.livePanes[paneID]
	}
	c.registry.SetPanePID(paneID, pid)
}

// publishIdentity writes the canonical identity file (XDG-compliant, atomic,
// Agent-Mail-compatible; see agentmail.CanonicalIdentityPath and the
// mcp-agent-mail Rust reference in pane_identity.rs) plus the legacy /tmp
// compat file still read by older hooks, under every project key the pane may
// resolve its identity through: the session key, its symlink-resolved form,
// and the pane's worktree directory (ntm#257). The extra keys are best-effort;
// only a failure on the session key itself is reported.
func (c *Coordinator) publishIdentity(agent Agent, name string) {
	// When registration carried a pane binding, the Agent Mail server has
	// already written a structured generation receipt (name + pane + PID +
	// socket) at the canonical session-key path. That receipt is strictly
	// richer than a plain name — its liveness facts back the server's
	// pane-identity reuse — so never clobber it: keep it in place and mirror
	// its exact bytes into the alternate project namespaces. Without a
	// matching receipt (older server, unreachable filesystem, or a stale
	// receipt for a different identity), fall back to plain-name writes.
	receipt, receiptName, hasReceipt := agentmail.ReadPaneIdentityReceipt(c.projectKey, agent.PaneID)
	if hasReceipt && receiptName != name {
		hasReceipt = false
	}
	for i, key := range publishKeys(c.projectKey, agent.PaneDir) {
		switch {
		case hasReceipt && key == c.projectKey:
			// Canonical receipt already in place; leave it untouched.
		case hasReceipt:
			if _, writeErr := agentmail.MirrorPaneIdentityReceipt(key, agent.PaneID, receipt); writeErr != nil && i == 0 {
				c.warnf("Failed to mirror identity receipt for pane %d: %v", agent.PaneIndex, writeErr)
			}
		default:
			if _, writeErr := agentmail.WriteIdentity(key, agent.PaneID, name); writeErr != nil && i == 0 {
				c.warnf("Failed to write identity file for pane %d: %v", agent.PaneIndex, writeErr)
			}
		}
		_ = agentmail.WriteLegacyCompatIdentity(key, agent.PaneID, name)
	}
}

// LivenessFromPanes derives an agentmail.PaneLiveness from a tmux pane listing.
// A recorded binding is live when its pane id is present in the listing and,
// if a pid was recorded at registration time, the pane still carries that pid
// (tmux reuses %N across server restarts, so existence alone is not proof of
// the same incarnation). Missing pids on either side fall back to existence.
func LivenessFromPanes(panes []tmux.Pane) agentmail.PaneLiveness {
	pids := make(map[string]int, len(panes))
	for _, p := range panes {
		if p.ID == "" {
			continue
		}
		pids[p.ID] = p.PID
	}
	return func(paneID string, recordedPID int) bool {
		pid, ok := pids[paneID]
		if !ok {
			return false
		}
		if recordedPID > 0 && pid > 0 && pid != recordedPID {
			return false
		}
		return true
	}
}

// publishKeys returns the distinct project keys a pane's Agent Mail identity
// file must be written under, session key first:
//
//   - the session key itself (what NTM registered the project as);
//   - its symlink-resolved form, so an agent that canonicalizes its cwd still
//     finds the name (GH#239 class);
//   - for a pane launched in a linked worktree, the pane's own directory and
//     its resolved form, because the agent's tooling derives the key from its
//     cwd and hashes it into a different identity directory (ntm#257).
//
// Empty inputs and duplicates are dropped, so a plain spawn yields exactly the
// session key (plus its resolved form when the path is a symlink).
func publishKeys(sessionKey, paneDir string) []string {
	var keys []string
	seen := make(map[string]struct{}, 4)
	add := func(key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	add(sessionKey)
	add(agentmail.CanonicalProjectKey(sessionKey))
	add(paneDir)
	add(agentmail.CanonicalProjectKey(paneDir))
	return keys
}

// delegatedModelPlaceholder is the model identifier NTM registers with Agent
// Mail when the agent type delegates model selection to its own CLI config
// and no explicit model was given. It is stable per program, so the same
// pane re-registers identically, and unmistakably not a real model name.
func delegatedModelPlaceholder(program string) string {
	program = strings.TrimSpace(program)
	if program == "" {
		program = "agent"
	}
	return program + "/cli-default"
}

// modelDelegationIsBuiltInDefault reports whether leaving the launch model to
// the agent's own CLI is NTM's built-in default for this agent type: a known
// type whose compiled-in [models] default is empty (claude since ntm#334, and
// grok, opencode, omp), or one with no [models] key at all. Such a pane
// resolving no model is the intended configuration, so the delegation notice
// — which suggests setting models.default_<type> — would advise undoing the
// default on every spawn. Plugin types and a user-blanked non-empty default
// (codex, gemini, ollama) still get the notice.
func modelDelegationIsBuiltInDefault(agentType string) bool {
	canonical := agentpkg.AgentType(agentType).Canonical()
	if !canonical.IsValid() || canonical == agentpkg.AgentTypeUser {
		return false
	}
	defaults := config.DefaultModels()
	return strings.TrimSpace(defaults.GetModelName(string(canonical), "")) == ""
}

// modelDefaultKeyForType names the [models] key a user would set to give the
// type a real default model, for the delegation notice.
func modelDefaultKeyForType(agentType string) string {
	switch agentpkg.AgentType(agentType).Canonical() {
	case agentpkg.AgentTypeOpencode:
		return "opencode"
	case agentpkg.AgentTypeGrok:
		return "grok"
	case agentpkg.AgentTypeOmp:
		return "omp"
	case agentpkg.AgentTypeClaudeCode:
		return "claude"
	case agentpkg.AgentTypeCodex:
		return "codex"
	case agentpkg.AgentTypeGemini:
		return "gemini"
	case agentpkg.AgentTypeOllama:
		return "ollama"
	default:
		return strings.ToLower(strings.TrimSpace(agentType))
	}
}

// agentTypeToProgram maps NTM agent types to Agent Mail program names.
func agentTypeToProgram(agentType string) string {
	switch agentpkg.AgentType(agentType).Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		return "claude-code"
	case agentpkg.AgentTypeCodex:
		return "codex-cli"
	case agentpkg.AgentTypeGemini:
		return "gemini-cli"
	case agentpkg.AgentTypeAntigravity:
		return "antigravity"
	case agentpkg.AgentTypeGrok:
		return "grok"
	case agentpkg.AgentTypeOmp:
		return "omp"
	case agentpkg.AgentTypePi:
		return "pi"
	case agentpkg.AgentTypeCursor:
		return "cursor"
	case agentpkg.AgentTypeWindsurf:
		return "windsurf"
	case agentpkg.AgentTypeAider:
		return "aider"
	case agentpkg.AgentTypeOpencode:
		return "opencode"
	case agentpkg.AgentTypeOllama:
		return "ollama"
	default:
		return strings.ToLower(strings.TrimSpace(agentType))
	}
}
