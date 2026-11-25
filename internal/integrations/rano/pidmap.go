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

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func pidmapLogger() *slog.Logger {
	return slog.Default().With("component", "integrations.rano.pidmap")
}

// PaneIdentity represents a pane's identity for attribution.
type PaneIdentity struct {
	PaneID      string         // Durable tmux identity; titles are not unique
	PanePID     int            // Root process; also retained on child attribution
	WindowIndex int            // Physical window containing PaneIndex
	Session     string         // Session name
	PaneIndex   int            // Pane index within session
	PaneTitle   string         // Full pane title (e.g., "myproject__cc_1")
	AgentType   tmux.AgentType // Parsed agent type
	NTMIndex    int            // NTM-specific index (e.g., 1 for cc_1)
}

// String prefers the durable tmux ID. Titles are a readable fallback for
// synthetic identities that have no tmux ID, never the production join key.
func (p PaneIdentity) String() string {
	if p.PaneID != "" {
		return p.PaneID
	}
	if p.PaneTitle != "" {
		return p.PaneTitle
	}
	if p.WindowIndex > 0 {
		return fmt.Sprintf("%s:%d.%d", p.Session, p.WindowIndex, p.PaneIndex)
	}
	return fmt.Sprintf("%s:%d", p.Session, p.PaneIndex)
}

// PIDMap attributes any process (a pane's shell or one of its children) to
// the pane it runs in.
type PIDMap struct {
	mu sync.RWMutex

	// pidToPane maps any PID (shell or child) to its pane identity
	pidToPane map[int]*PaneIdentity

	// session to watch (empty means all sessions)
	session string
}

// NewPIDMap creates a new PID map for the specified session.
// If session is empty, it tracks all NTM sessions.
func NewPIDMap(session string) *PIDMap {
	return &PIDMap{
		pidToPane: make(map[int]*PaneIdentity),
		session:   session,
	}
}

// RefreshContext rebuilds the PID mappings from tmux and /proc, with
// cancellation support. Call it before queries to keep attribution current.
func (m *PIDMap) RefreshContext(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear existing mappings
	m.pidToPane = make(map[int]*PaneIdentity)
	paneCount := 0

	var sessions []tmux.Session
	var err error

	if m.session != "" {
		// GetPanesContext below validates the named session. Avoid GetSession:
		// its context-free subprocess can outlive a one-shot reader's budget.
		sessions = []tmux.Session{{Name: m.session}}
	} else {
		// Get all sessions
		sessions, err = tmux.ListSessionsContext(ctx)
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
			return fmt.Errorf("failed to get panes for session %s: %w", sess.Name, err)
		}

		for _, pane := range panes {
			if pane.PID <= 0 || pane.Dead || pane.IsServicePane() {
				continue
			}

			identity := &PaneIdentity{
				PaneID:      pane.ID,
				PanePID:     pane.PID,
				WindowIndex: pane.WindowIndex,
				Session:     sess.Name,
				PaneIndex:   pane.Index,
				PaneTitle:   pane.Title,
				AgentType:   pane.Type,
				NTMIndex:    pane.NTMIndex,
			}

			// Map shell PID to pane
			m.pidToPane[pane.PID] = identity
			paneCount++

			// Discover and map child processes
			children, err := getChildPIDs(pane.PID)
			if err != nil {
				pidmapLogger().Debug("failed to get child PIDs",
					"shell_pid", pane.PID,
					"pane", pane.Title,
					"error", err,
				)
				continue
			}

			for _, childPID := range children {
				m.pidToPane[childPID] = identity
			}

			pidmapLogger().Debug("mapped pane",
				"pane", pane.Title,
				"shell_pid", pane.PID,
				"child_count", len(children),
			)
		}
	}

	// Routine per-poll success; the dashboard refreshes this every second.
	pidmapLogger().Debug("refreshed PID map",
		"pane_count", paneCount,
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

// GetPIDLabels returns a map of PID to label string for use with rano.
// Live mappings use durable pane IDs so retitled or same-titled panes stay distinct.
func (m *PIDMap) GetPIDLabels() map[int]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	labels := make(map[int]string, len(m.pidToPane))
	for pid, identity := range m.pidToPane {
		labels[pid] = identity.String()
	}
	return labels
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
