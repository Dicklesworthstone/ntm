// Package rano provides integration with the rano network observer for per-agent API tracking.
// It bridges NTM's pane identities with process PIDs so rano can attribute network activity
// to specific agents.
package rano

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var pidmapLogger = slog.Default().With("component", "integrations.rano.pidmap")

// PaneIdentity represents a pane's identity for attribution.
type PaneIdentity struct {
	Session   string         // Session name
	PaneIndex int            // Pane index within session
	PaneTitle string         // Full pane title (e.g., "myproject__cc_1")
	AgentType tmux.AgentType // Parsed agent type
	NTMIndex  int            // NTM-specific index (e.g., 1 for cc_1)
}

// String returns a readable representation of the pane identity.
func (p PaneIdentity) String() string {
	if p.PaneTitle != "" {
		return p.PaneTitle
	}
	return fmt.Sprintf("%s:.%d", p.Session, p.PaneIndex)
}

// PIDMap maintains bidirectional mappings between PIDs and pane identities.
// It tracks both shell PIDs and their child processes to enable attribution
// of any process to its originating pane.
type PIDMap struct {
	mu sync.RWMutex

	// paneToShellPID maps pane identity to its shell PID
	paneToShellPID map[string]int // paneTitle -> shell PID

	// pidToPane maps any PID (shell or child) to its pane identity
	pidToPane map[int]*PaneIdentity

	// shellToChildren maps shell PIDs to their child PIDs
	shellToChildren map[int][]int

	// session to watch (empty means all sessions)
	session string

	// lastRefresh records when the map was last updated
	lastRefresh time.Time
}

// NewPIDMap creates a new PID map for the specified session.
// If session is empty, it tracks all NTM sessions.
func NewPIDMap(session string) *PIDMap {
	return &PIDMap{
		paneToShellPID:  make(map[string]int),
		pidToPane:       make(map[int]*PaneIdentity),
		shellToChildren: make(map[int][]int),
		session:         session,
	}
}

// Refresh updates all PID mappings by querying tmux and /proc.
// This should be called periodically or before queries to ensure accuracy.
func (m *PIDMap) Refresh() error {
	return m.RefreshContext(context.Background())
}

// RefreshContext updates all PID mappings with cancellation support.
func (m *PIDMap) RefreshContext(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear existing mappings
	m.paneToShellPID = make(map[string]int)
	m.pidToPane = make(map[int]*PaneIdentity)
	m.shellToChildren = make(map[int][]int)

	var sessions []tmux.Session
	var err error

	if m.session != "" {
		// Get specific session
		sess, err := tmux.GetSession(m.session)
		if err != nil {
			return fmt.Errorf("failed to get session %s: %w", m.session, err)
		}
		sessions = []tmux.Session{*sess}
	} else {
		// Get all sessions
		sessions, err = tmux.ListSessions()
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}
	}

	for _, sess := range sessions {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		panes, err := tmux.GetPanesContext(ctx, sess.Name)
		if err != nil {
			pidmapLogger.Warn("failed to get panes for session",
				"session", sess.Name,
				"error", err,
			)
			continue
		}

		for _, pane := range panes {
			if pane.PID <= 0 {
				continue
			}

			identity := &PaneIdentity{
				Session:   sess.Name,
				PaneIndex: pane.Index,
				PaneTitle: pane.Title,
				AgentType: pane.Type,
				NTMIndex:  pane.NTMIndex,
			}

			// Map pane to shell PID
			m.paneToShellPID[pane.Title] = pane.PID

			// Map shell PID to pane
			m.pidToPane[pane.PID] = identity

			// Discover and map child processes
			children, err := getChildPIDs(pane.PID)
			if err != nil {
				pidmapLogger.Debug("failed to get child PIDs",
					"shell_pid", pane.PID,
					"pane", pane.Title,
					"error", err,
				)
				continue
			}

			m.shellToChildren[pane.PID] = children
			for _, childPID := range children {
				m.pidToPane[childPID] = identity
			}

			pidmapLogger.Debug("mapped pane",
				"pane", pane.Title,
				"shell_pid", pane.PID,
				"child_count", len(children),
			)
		}
	}

	m.lastRefresh = time.Now()
	pidmapLogger.Info("refreshed PID map",
		"pane_count", len(m.paneToShellPID),
		"total_pids", len(m.pidToPane),
	)

	return nil
}

// GetPaneForPID returns the pane identity for any PID (shell or child).
// Returns nil if the PID is not known.
func (m *PIDMap) GetPaneForPID(pid int) *PaneIdentity {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pidToPane[pid]
}

// GetShellPID returns the shell PID for a pane title.
// Returns 0 if the pane is not known.
func (m *PIDMap) GetShellPID(paneTitle string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.paneToShellPID[paneTitle]
}

// GetAllPIDsForPane returns all PIDs (shell + children) for a pane.
func (m *PIDMap) GetAllPIDsForPane(paneTitle string) []int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	shellPID, ok := m.paneToShellPID[paneTitle]
	if !ok {
		return nil
	}

	result := []int{shellPID}
	result = append(result, m.shellToChildren[shellPID]...)
	return result
}

// GetPIDLabels returns a map of PID to label string for use with rano.
// The label format is: "session:paneTitle" or just "paneTitle" if unambiguous.
func (m *PIDMap) GetPIDLabels() map[int]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	labels := make(map[int]string, len(m.pidToPane))
	for pid, identity := range m.pidToPane {
		labels[pid] = identity.String()
	}
	return labels
}

// LastRefresh returns when the map was last refreshed.
func (m *PIDMap) LastRefresh() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastRefresh
}

// Stats returns statistics about the current PID map.
type Stats struct {
	PaneCount     int            `json:"pane_count"`
	TotalPIDCount int            `json:"total_pid_count"`
	ShellPIDCount int            `json:"shell_pid_count"`
	ChildPIDCount int            `json:"child_pid_count"`
	LastRefresh   time.Time      `json:"last_refresh"`
	Session       string         `json:"session,omitempty"`
	ByAgentType   map[string]int `json:"by_agent_type,omitempty"`
}

// GetStats returns statistics about the current PID map.
func (m *PIDMap) GetStats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	byAgentType := make(map[string]int)
	for _, identity := range m.pidToPane {
		if identity.AgentType != "" {
			byAgentType[string(identity.AgentType)]++
		}
	}

	shellCount := len(m.paneToShellPID)
	childCount := 0
	for _, children := range m.shellToChildren {
		childCount += len(children)
	}

	return Stats{
		PaneCount:     len(m.paneToShellPID),
		TotalPIDCount: len(m.pidToPane),
		ShellPIDCount: shellCount,
		ChildPIDCount: childCount,
		LastRefresh:   m.lastRefresh,
		Session:       m.session,
		ByAgentType:   byAgentType,
	}
}

// getChildPIDs returns all child PIDs for a given parent PID.
// It uses /proc filesystem on Linux for efficiency.
func getChildPIDs(parentPID int) ([]int, error) {
	children := []int{}

	// Try /proc/[pid]/task/[tid]/children first (Linux 3.5+)
	childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", parentPID, parentPID)
	if data, err := os.ReadFile(childrenPath); err == nil {
		fields := strings.Fields(string(data))
		for _, field := range fields {
			if pid, err := strconv.Atoi(field); err == nil && pid > 0 {
				children = append(children, pid)
				// Recursively get grandchildren
				grandchildren, _ := getChildPIDs(pid)
				children = append(children, grandchildren...)
			}
		}
		return children, nil
	}

	// Fallback: scan /proc for processes with matching PPID
	procDir, err := os.Open("/proc")
	if err != nil {
		return nil, fmt.Errorf("failed to open /proc: %w", err)
	}
	defer procDir.Close()

	entries, err := procDir.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("failed to read /proc: %w", err)
	}

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry)
		if err != nil {
			continue // Not a PID directory
		}

		ppid, err := getParentPID(pid)
		if err != nil {
			continue
		}

		if ppid == parentPID {
			children = append(children, pid)
			// Recursively get grandchildren
			grandchildren, _ := getChildPIDs(pid)
			children = append(children, grandchildren...)
		}
	}

	return children, nil
}

// getParentPID returns the parent PID for a process.
func getParentPID(pid int) (int, error) {
	statPath := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	file, err := os.Open(statPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return 0, fmt.Errorf("empty stat file")
	}

	// /proc/[pid]/stat format: pid (comm) state ppid ...
	// We need to handle comm which may contain spaces and parentheses
	line := scanner.Text()

	// Find the last ')' which marks the end of comm
	lastParen := strings.LastIndex(line, ")")
	if lastParen == -1 {
		return 0, fmt.Errorf("malformed stat line")
	}

	// Fields after comm: state ppid ...
	fields := strings.Fields(line[lastParen+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("not enough fields after comm")
	}

	// fields[0] is state, fields[1] is ppid
	return strconv.Atoi(fields[1])
}

// Global PID map instance for convenience

var (
	globalPIDMap     *PIDMap
	globalPIDMapOnce sync.Once
	globalPIDMapMu   sync.RWMutex
)

// GetGlobalPIDMap returns the global PID map singleton.
// It tracks all sessions by default.
func GetGlobalPIDMap() *PIDMap {
	globalPIDMapOnce.Do(func() {
		globalPIDMap = NewPIDMap("")
	})
	return globalPIDMap
}

// GetGlobalPIDMapForSession returns or creates a global PID map for a specific session.
func GetGlobalPIDMapForSession(session string) *PIDMap {
	globalPIDMapMu.Lock()
	defer globalPIDMapMu.Unlock()

	if globalPIDMap != nil && globalPIDMap.session == session {
		return globalPIDMap
	}

	globalPIDMap = NewPIDMap(session)
	return globalPIDMap
}
