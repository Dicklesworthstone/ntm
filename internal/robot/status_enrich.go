package robot

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Output tracking state
var (
	outputStateMu sync.RWMutex
	paneStates    = make(map[string]*paneState)
)

type paneState struct {
	lastHash string
	lastTS   time.Time
}

// Rate limit patterns
var rateLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)you've hit your limit`),
	regexp.MustCompile(`(?i)rate limit`),
	regexp.MustCompile(`(?i)too many requests`),
	regexp.MustCompile(`RESOURCE_EXHAUSTED`),
	regexp.MustCompile(`resets \d+[ap]m`),
}

func enrichAgentStatus(agent *Agent, sessionName string) {
	// 1. PID is already populated from tmux
	if agent.PID == 0 {
		return // Cannot do much without PID
	}

	// 2. Get Child PID

	childPID, err := getChildPID(agent.PID)
	if err == nil {
		agent.ChildPID = childPID
	} else {
		// Fallback: if no child, maybe the shell IS the process?
		// But usually agent is a child of the shell.
	}

	// 3. Process State
	targetPID := agent.ChildPID
	if targetPID == 0 {
		targetPID = agent.PID
	}
	state, stateName, err := getProcessState(targetPID)
	if err == nil {
		agent.ProcessState = state
		agent.ProcessStateName = stateName
	}

	// 4. Memory
	mem, err := getProcessMemoryMB(targetPID)
	if err == nil {
		agent.MemoryMB = mem
	}

	// 5. Output analysis
	// Capture last 50 lines for rate limit detection and activity
	// We use agent.Pane which is the pane ID (e.g. %3)
	content, err := tmux.CapturePaneOutput(agent.Pane, 50)
	if err == nil {
		// Rate limit
		detected, match := detectRateLimit(content)
		agent.RateLimitDetected = detected
		agent.RateLimitMatch = match

		// Output activity
		updateActivity(agent.Pane, content)
		agent.LastOutputTS = getLastOutput(agent.Pane)
		
		// For output_lines_since_last, we would need total line count which is expensive.
		// We'll skip precise line counting for now to avoid performance hit.
		
		if !agent.LastOutputTS.IsZero() {
			agent.SecondsSinceOutput = int(time.Since(agent.LastOutputTS).Seconds())
		}
	}
}

func getChildPID(shellPID int) (int, error) {
	// Try /proc first (Linux)
	taskPath := fmt.Sprintf("/proc/%d/task/%d/children", shellPID, shellPID)
	data, err := os.ReadFile(taskPath)
	if err == nil {
		parts := strings.Fields(string(data))
		if len(parts) > 0 {
			return strconv.Atoi(parts[0])
		}
	}

	// Fallback to pgrep
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shellPID)).Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 {
			return strconv.Atoi(lines[0])
		}
	}

	return 0, fmt.Errorf("no child process found")
}

func getProcessState(pid int) (string, string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "", "", err
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			// Format: "State:  S (sleeping)"
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				code := parts[1]
				name := strings.Trim(parts[2], "()")
				return code, name, nil
			}
		}
	}
	return "unknown", "unknown", nil
}

func getProcessMemoryMB(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			// Format: "VmRSS:    123456 kB"
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				kb, _ := strconv.Atoi(parts[1])
				return kb / 1024, nil
			}
		}
	}
	return 0, nil
}

func detectRateLimit(content string) (bool, string) {
	for _, pattern := range rateLimitPatterns {
		if match := pattern.FindString(content); match != "" {
			return true, match
		}
	}
	return false, ""
}

func updateActivity(paneID, content string) {
	outputStateMu.Lock()
	defer outputStateMu.Unlock()

	state, ok := paneStates[paneID]
	if !ok {
		state = &paneState{
			lastTS: time.Now(), // Initialize with current time
		}
		paneStates[paneID] = state
	}

	if state.lastHash != content {
		state.lastTS = time.Now()
		state.lastHash = content
	}
}

func getLastOutput(paneID string) time.Time {
	outputStateMu.RLock()
	defer outputStateMu.RUnlock()
	if state, ok := paneStates[paneID]; ok {
		return state.lastTS
	}
	return time.Time{}
}
