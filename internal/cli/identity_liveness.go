package cli

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// livenessFromPanes derives an agentmail.PaneLiveness from a tmux pane listing.
// A recorded binding is live when its pane id is present in the listing and,
// if a pid was recorded at registration time, the pane still carries that pid
// (tmux reuses %N across server restarts, so existence alone is not proof of
// the same incarnation). Missing pids on either side fall back to existence.
func livenessFromPanes(panes []tmux.Pane) agentmail.PaneLiveness {
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

// nextPaneIndices returns the highest NTM pane index currently in use per agent
// type, so `ntm add` can mint the next free slot. Two sources are folded in:
//
//  1. every live pane whose title still parses as session__<type>_<N>;
//  2. every registry title whose bound pane is still live, even though the
//     pane's current title no longer parses because the agent process or the
//     user overwrote it (tmux's allow-set-title guard is best-effort only).
//
// Without (2) a retitled live pane contributed nothing to the scan, its slot
// number was re-issued, the composed title collided with the registry entry,
// and the new pane inherited the running agent's Agent Mail identity (ntm#256).
func nextPaneIndices(panes []tmux.Pane, registry *agentmail.SessionAgentRegistry) map[string]int {
	maxIndices := make(map[string]int)
	fold := func(title string) {
		typeStr, num, ok := paneTitleTypeAndIndex(title)
		if ok && num > maxIndices[typeStr] {
			maxIndices[typeStr] = num
		}
	}
	for _, p := range panes {
		fold(p.Title)
	}
	if registry != nil {
		for _, title := range registry.OccupiedTitles(livenessFromPanes(panes)) {
			fold(title)
		}
	}
	return maxIndices
}

// identityPublishKeys returns the distinct project keys a pane's Agent Mail
// identity file must be written under, session key first:
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
func identityPublishKeys(sessionKey, paneDir string) []string {
	if override := agentmail.InvocationProjectKey(""); override != "" {
		// The explicit invocation scope is authoritative. Do not also publish
		// identities under the physical checkout/worktree keys: a public Agent
		// Mail deployment resolves the pane through the canonical key only.
		return []string{override}
	}
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
