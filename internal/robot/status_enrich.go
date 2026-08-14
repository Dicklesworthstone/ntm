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

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// Output tracking state
var (
	outputStateMu sync.RWMutex
	paneStates    = make(map[string]*paneState)
)

type paneState struct {
	lastHash      string
	lastTS        time.Time
	lastLineCount int
}

// Rate limit patterns
var rateLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)you've hit your limit`),
	regexp.MustCompile(`(?i)rate limit`),
	regexp.MustCompile(`(?i)too many requests`),
	regexp.MustCompile(`RESOURCE_EXHAUSTED`),
	regexp.MustCompile(`resets \d+[ap]m`),
	// Provider usage-limit banners ("hit your usage limit", "plan limit
	// reached", quota exhaustion) are NOT duplicated here: detectRateLimit
	// falls through to ratelimit.DetectRateLimitForAgent, whose canonical
	// usageLimitBannerPatterns vocabulary covers them for every agent type
	// (bd-wtm0w). Add new banner phrasings there, once.
}

var codexRateLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)openai.*rate.?limit`),
	regexp.MustCompile(`(?i)codex.*rate.?limit`),
	regexp.MustCompile(`(?i)insufficient_quota`),
	regexp.MustCompile(`(?i)billing.*limit|limit.*billing`),
	regexp.MustCompile(`(?i)usage.*(?:cap|limit).*reached`),
	regexp.MustCompile(`(?i)tokens?\s+per\s+min`),
	regexp.MustCompile(`(?i)requests?\s+per\s+min`),
	regexp.MustCompile(`(?i)RateLimitError`),
}

func enrichAgentStatus(agent *Agent, sessionName, modelName string, content string) {
	// 1. PID is already populated from tmux
	if agent.PID == 0 {
		return // Cannot do much without PID
	}

	// 2. Get Child PID (delegated to shared process package)

	childPID := process.GetChildPID(agent.PID)
	if childPID > 0 {
		agent.ChildPID = childPID
	}

	// 3. Process State
	targetPID := agent.ChildPID
	if targetPID == 0 {
		targetPID = agent.PID
	}
	state, stateName, err := process.GetProcessState(targetPID)
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
	// We use the content passed in from the caller (already captured once)
	if content != "" {
		// Rate limit
		detected, match := detectRateLimit(content, agent.Type)
		agent.RateLimitDetected = detected
		agent.RateLimitMatch = match

		// Output activity
		lastOutputTS, linesDelta := updateActivity(agent.Pane, content)
		agent.LastOutputTS = lastOutputTS
		agent.OutputLinesSinceLast = linesDelta

		if !agent.LastOutputTS.IsZero() {
			agent.SecondsSinceOutput = int(time.Since(agent.LastOutputTS).Seconds())
		}

		// Durable output-change sequence (#246). agent.Pane carries the tmux
		// pane ID on this path, matching the scope used by --robot-activity.
		agent.OutputSequence = observeOutputSequence(sessionName, agent.Pane, content, time.Now().UTC())

		// Composer visibility (bd-v8dqd).
		composer := tmux.InspectComposer(content, tmux.AgentType(agent.Type))
		agent.UnsubmittedInput = composer.HoldsText
		agent.QueuedMessages = composer.QueuedMessages

		if modelName != "" {
			agent.ContextModel = modelName
			usage := tokens.GetUsageInfo(content, modelName)
			if usage != nil {
				agent.ContextTokens = usage.EstimatedTokens
				agent.ContextLimit = usage.ContextLimit
				agent.ContextPercent = usage.UsagePercent
				agent.ContextModel = usage.Model
			}
		}
	}
}

func getProcessMemoryMB(pid int) (int, error) {
	// Try /proc first (Linux)
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
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
	}

	// Fallback to ps (macOS and Linux)
	// 'rss' is resident set size in kilobytes
	cmd := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err == nil {
		kbStr := strings.TrimSpace(string(out))
		if kbStr != "" {
			kb, _ := strconv.Atoi(kbStr)
			return kb / 1024, nil
		}
	}

	return 0, fmt.Errorf("failed to get memory for pid %d", pid)
}

func detectRateLimit(content string, agentType string) (bool, string) {
	cleaned := strings.TrimSpace(content)
	for _, pattern := range rateLimitPatterns {
		if match := pattern.FindString(cleaned); match != "" {
			return true, match
		}
	}
	if normalizeAgentType(agentType) == "codex" {
		for _, pattern := range codexRateLimitPatterns {
			if match := pattern.FindString(cleaned); match != "" {
				return true, match
			}
		}
	}
	if detection := ratelimit.DetectRateLimitForAgent(cleaned, agentType); detection.RateLimited {
		if detection.ExitCode == 429 {
			return true, "429"
		}
		return true, "rate limit"
	}
	return false, ""
}

func updateActivity(paneID, content string) (time.Time, int) {
	outputStateMu.Lock()
	defer outputStateMu.Unlock()

	currentLines := util.CountNonEmptyLines(content)
	state, ok := paneStates[paneID]
	if !ok {
		state = &paneState{
			lastTS:        time.Now(), // Initialize with current time
			lastHash:      content,
			lastLineCount: currentLines,
		}
		paneStates[paneID] = state
		return state.lastTS, currentLines
	}

	linesDelta := currentLines - state.lastLineCount
	if linesDelta < 0 {
		// Buffer wrap or clear - treat as reset
		linesDelta = currentLines
	} else if linesDelta == 0 && state.lastHash != content {
		// Output changed but line count stayed flat (window shift). Signal activity.
		linesDelta = 1
	}

	if state.lastHash != content {
		state.lastTS = time.Now()
		state.lastHash = content
	}
	state.lastLineCount = currentLines

	return state.lastTS, linesDelta
}

func getLastOutput(paneID string) time.Time {
	outputStateMu.RLock()
	defer outputStateMu.RUnlock()
	if state, ok := paneStates[paneID]; ok {
		return state.lastTS
	}
	return time.Time{}
}
