package worktrees

import "path/filepath"

// worktreePath returns the on-disk path for an agent's worktree (test-only
// helper replicating the removed production accessor).
func (m *WorktreeManager) worktreePath(agentName string) string {
	return filepath.Join(m.sessionRoot(), agentName)
}
