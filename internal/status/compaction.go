package status

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// CompactionPattern defines patterns for detecting compaction events
type CompactionPattern struct {
	Agent    string           // "claude", "codex", "gemini", "*"
	Patterns []*regexp.Regexp // Compiled patterns
}

// CompactionEvent represents a detected compaction
type CompactionEvent struct {
	PaneID      string    `json:"pane_id"`
	AgentType   string    `json:"agent_type"`
	DetectedAt  time.Time `json:"detected_at"`
	MatchedText string    `json:"matched_text"`
	Pattern     string    `json:"pattern"` // Which pattern matched
}

var (
	compactionPatterns []CompactionPattern
	patternsOnce       sync.Once
)

// Claude Code compaction patterns - these are the CRITICAL ones
// "Conversation compacted" is the EXACT text Claude Code shows
var claudePatterns = []string{
	`Conversation compacted`,                                       // EXACT - primary signal
	`(?i)conversation.*summarized`,                                 // Alternate phrasing
	`(?i)context.*compacted`,                                       // Context management
	`(?i)continued from.*previous.*conversation`,                   // Recovery indication
	`(?i)ran out of context`,                                       // Explicit context limit
	`(?i)session is being continued`,                               // Session continuation
	`(?i)conversation.*truncated`,                                  // Truncation signal
	`(?i)previous.*context.*lost`,                                  // Context loss
	`This session is being continued from a previous conversation`, // Full phrase
}

// Codex compaction patterns
var codexPatterns = []string{
	`(?i)context limit reached`,
	`(?i)conversation truncated`,
	`(?i)history.*cleared`,
	`(?i)context.*reset`,
}

// Gemini compaction patterns
var geminiPatterns = []string{
	`(?i)context window exceeded`,
	`(?i)conversation reset`,
	`(?i)context.*limit`,
	`(?i)history.*truncated`,
}

// Generic patterns that apply to any agent
var genericPatterns = []string{
	`(?i)continuing.*from.*summary`,
	`(?i)previous.*session.*summarized`,
}

func initPatterns() {
	patternsOnce.Do(func() {
		compactionPatterns = []CompactionPattern{
			{
				Agent:    "cc",
				Patterns: compilePatterns(claudePatterns),
			},
			{
				Agent:    "cod",
				Patterns: compilePatterns(codexPatterns),
			},
			{
				Agent:    "gmi",
				Patterns: compilePatterns(geminiPatterns),
			},
			{
				Agent:    "*", // Generic patterns for all agents
				Patterns: compilePatterns(genericPatterns),
			},
		}
	})
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			result = append(result, re)
		}
	}
	return result
}

func normalizedCompactionAgentType(agentType string) string {
	switch canonical := agent.AgentType(agentType).Canonical(); canonical {
	case agent.AgentTypeClaudeCode:
		return "cc"
	case agent.AgentTypeCodex:
		return "cod"
	case agent.AgentTypeGemini:
		return "gmi"
	default:
		return strings.ToLower(strings.TrimSpace(agentType))
	}
}

// DetectCompaction checks output for compaction patterns.
// Returns nil if no compaction is detected.
// The agentType parameter may be any supported alias; matching is normalized
// through the shared agent type resolver before dispatch.
func DetectCompaction(output string, agentType string) *CompactionEvent {
	initPatterns()
	normalizedAgentType := normalizedCompactionAgentType(agentType)

	// Check agent-specific patterns and generic patterns (cp.Agent == "*")
	// The condition includes patterns where cp.Agent matches agentType OR is "*"
	for _, cp := range compactionPatterns {
		if cp.Agent != "*" && normalizedCompactionAgentType(cp.Agent) != normalizedAgentType {
			continue
		}
		for i, pattern := range cp.Patterns {
			if match := pattern.FindString(output); match != "" {
				// Get the pattern string for debugging
				var patternStr string
				switch normalizedCompactionAgentType(cp.Agent) {
				case "cc":
					if i < len(claudePatterns) {
						patternStr = claudePatterns[i]
					}
				case "cod":
					if i < len(codexPatterns) {
						patternStr = codexPatterns[i]
					}
				case "gmi":
					if i < len(geminiPatterns) {
						patternStr = geminiPatterns[i]
					}
				case "*":
					if i < len(genericPatterns) {
						patternStr = genericPatterns[i]
					}
				}

				return &CompactionEvent{
					AgentType:   agentType,
					DetectedAt:  time.Now(),
					MatchedText: match,
					Pattern:     patternStr,
				}
			}
		}
	}

	return nil
}

// DetectCompactionWithPaneID is a convenience wrapper that sets the pane ID
func DetectCompactionWithPaneID(output, agentType, paneID string) *CompactionEvent {
	event := DetectCompaction(output, agentType)
	if event != nil {
		event.PaneID = paneID
	}
	return event
}

// HasCompaction is a simple boolean check for compaction
func HasCompaction(output string, agentType string) bool {
	return DetectCompaction(output, agentType) != nil
}

// CompactionDetector provides a stateful detector that tracks compaction events
type CompactionDetector struct {
	mu     sync.Mutex
	events []CompactionEvent
	maxAge time.Duration
}

// NewCompactionDetector creates a new compaction detector
func NewCompactionDetector(maxAge time.Duration) *CompactionDetector {
	if maxAge == 0 {
		maxAge = 5 * time.Minute // Default: track events for 5 minutes
	}
	return &CompactionDetector{
		events: make([]CompactionEvent, 0),
		maxAge: maxAge,
	}
}

// Check checks for compaction and records any events
func (d *CompactionDetector) Check(output, agentType, paneID string) *CompactionEvent {
	event := DetectCompactionWithPaneID(output, agentType, paneID)
	if event != nil {
		d.mu.Lock()
		d.events = append(d.events, *event)
		d.prune()
		d.mu.Unlock()
	}
	return event
}

// Events returns all recent compaction events
func (d *CompactionDetector) Events() []CompactionEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	result := make([]CompactionEvent, len(d.events))
	copy(result, d.events)
	return result
}

// EventsForPane returns events for a specific pane
func (d *CompactionDetector) EventsForPane(paneID string) []CompactionEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	result := make([]CompactionEvent, 0)
	for _, e := range d.events {
		if e.PaneID == paneID {
			result = append(result, e)
		}
	}
	return result
}

// HasRecentCompaction checks if a pane had compaction within the given duration
func (d *CompactionDetector) HasRecentCompaction(paneID string, within time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := time.Now().Add(-within)
	for _, e := range d.events {
		if e.PaneID == paneID && e.DetectedAt.After(cutoff) {
			return true
		}
	}
	return false
}

// Clear removes all events
func (d *CompactionDetector) Clear() {
	d.mu.Lock()
	d.events = make([]CompactionEvent, 0)
	d.mu.Unlock()
}

// prune removes old events (must be called with lock held)
func (d *CompactionDetector) prune() {
	cutoff := time.Now().Add(-d.maxAge)
	kept := make([]CompactionEvent, 0, len(d.events))
	for _, e := range d.events {
		if e.DetectedAt.After(cutoff) {
			kept = append(kept, e)
		}
	}
	d.events = kept
}
