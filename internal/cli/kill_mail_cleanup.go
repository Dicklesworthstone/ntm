package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// agentMailKillCleanupTimeout bounds the whole best-effort Agent Mail cleanup
// on session kill. The kill itself must never wait on a slow/absent mail
// server longer than this.
const agentMailKillCleanupTimeout = 5 * time.Second

// cleanupAgentMailOnKill best-effort releases Agent Mail state held by a
// killed session's registered pane agents (bd-1bdvy).
//
// Field evidence (csd, 2026-08-04): after `ntm kill` + respawn, the dead
// prior-swarm agents' file reservations stayed active until a coordinator
// manually force-released them and broadcast the cleanup. The kill path now
// releases every active reservation of each agent registered for the session
// and asks the server to drop stale pane identity files.
//
// Guarantees:
//   - never returns an error and never blocks the kill beyond
//     agentMailKillCleanupTimeout;
//   - failures are logged and surfaced as an attention event, not fatal.
func cleanupAgentMailOnKill(ctx context.Context, session, projectDir string) {
	session = strings.TrimSpace(session)
	if session == "" {
		return
	}

	registry, err := agentmail.LoadBestSessionAgentRegistry(session, projectDir)
	if err != nil {
		slog.Debug("kill: no agent mail registry for session", "session", session, "error", err)
	}

	projectKey := strings.TrimSpace(projectDir)
	if registry != nil && strings.TrimSpace(registry.ProjectKey) != "" {
		projectKey = strings.TrimSpace(registry.ProjectKey)
	}

	agents := sessionRegisteredAgentNames(registry, session, projectKey)
	if len(agents) == 0 || projectKey == "" {
		// Nothing registered for this session — nothing to release.
		return
	}

	client := newAgentMailClient(projectKey)
	if registry != nil {
		registry.HydrateClientTokens(client)
	}
	released, failures := releaseAgentMailForKilledSession(ctx, client, projectKey, agents)
	if released > 0 {
		slog.Info("kill: released agent mail reservations", "session", session, "agents", len(agents), "released", released)
	}
	if len(failures) > 0 {
		recordKillMailCleanupFailure(session, projectKey, agents, failures)
	}
}

// sessionRegisteredAgentNames returns the deduplicated, sorted agent names
// registered for the session: the pane registry (title map + pane-ID backup
// map) plus the session-level coordinator agent, when present.
func sessionRegisteredAgentNames(registry *agentmail.SessionAgentRegistry, session, projectKey string) []string {
	seen := make(map[string]struct{})
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		seen[name] = struct{}{}
	}
	if registry != nil {
		for _, name := range registry.Agents {
			add(name)
		}
		for _, name := range registry.PaneIDMap {
			add(name)
		}
	}
	if info, err := agentmail.LoadBestSessionAgent(session, projectKey); err == nil && info != nil {
		add(info.AgentName)
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// releaseAgentMailForKilledSession releases all active reservations for each
// agent and triggers server-side stale pane-identity cleanup. Best-effort:
// per-agent failures are collected, not fatal, and the whole pass is bounded
// by agentMailKillCleanupTimeout.
func releaseAgentMailForKilledSession(ctx context.Context, client *agentmail.Client, projectKey string, agents []string) (released int, failures []string) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, agentMailKillCleanupTimeout)
	defer cancel()

	for _, agent := range agents {
		res, err := client.ReleaseReservations(ctx, projectKey, agent, nil, nil)
		if err != nil {
			failures = append(failures, fmt.Sprintf("release_file_reservations(%s): %v", agent, err))
			if ctx.Err() != nil {
				// Budget exhausted — don't burn a timeout per remaining agent.
				failures = append(failures, "agent mail cleanup timed out")
				return released, failures
			}
			continue
		}
		if res != nil {
			released += res.Released
		}
	}

	if _, err := client.CleanupPaneIdentities(ctx, projectKey); err != nil {
		failures = append(failures, fmt.Sprintf("cleanup_pane_identities: %v", err))
	}
	return released, failures
}

// recordKillMailCleanupFailure logs the failure and appends a warning-level
// attention event so dashboards/robot feeds surface the leaked reservations
// instead of them silently outliving the session (bd-1bdvy). Best-effort.
func recordKillMailCleanupFailure(session, projectKey string, agents, failures []string) {
	slog.Warn("kill: agent mail cleanup incomplete",
		"session", session,
		"project", projectKey,
		"failures", strings.Join(failures, "; "))

	store, err := state.Open("")
	if err != nil {
		return
	}
	defer func() { _ = store.Close() }()

	details, _ := json.Marshal(map[string]interface{}{
		"agents":   agents,
		"failures": failures,
	})
	if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            time.Now().UTC(),
		SessionName:   session,
		Category:      "alert",
		EventType:     "agent_mail_kill_cleanup_failed",
		Source:        "kill",
		Actionability: state.ActionabilityActionRequired,
		Severity:      state.SeverityWarning,
		ReasonCode:    "agent_mail_kill_cleanup_failed",
		Summary:       fmt.Sprintf("Agent Mail cleanup after killing '%s' failed for %d agent(s); reservations may need manual release (ntm unlock --all)", session, len(agents)),
		Details:       string(details),
		DedupKey:      "kill:agent_mail_cleanup:" + session,
	}); err != nil {
		slog.Debug("kill: recording agent mail cleanup attention event failed", "error", err)
	}
}
