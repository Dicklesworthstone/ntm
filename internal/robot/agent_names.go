package robot

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// NATOAlphabet is the NATO phonetic alphabet used for generating agent names.
var NATOAlphabet = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot",
	"golf", "hotel", "india", "juliet", "kilo", "lima",
	"mike", "november", "oscar", "papa", "quebec", "romeo",
	"sierra", "tango", "uniform", "victor", "whiskey", "xray",
	"yankee", "zulu",
}

// agentTypePrefix maps agent type strings to their name prefixes.
var agentTypePrefix = map[string]string{
	"claude": "claude",
	"codex":  "codex",
	"gemini": "gemini",
	"user":   "user",
}

// AgentNameMap stores the mapping from agent names to pane references and back.
// It is safe for concurrent use.
type AgentNameMap struct {
	mu           sync.RWMutex
	nameToPane   map[string]string // name -> pane ref (e.g., "0.1")
	paneToName   map[string]string // pane ref -> name
	nameToType   map[string]string // name -> agent type
	sessionName  string
	natoIndex    int // next NATO alphabet index to use
	customNames  []string
	customOffset int // how many custom names have been consumed
}

// NewAgentNameMap creates a new empty AgentNameMap for the given session.
func NewAgentNameMap(sessionName string) *AgentNameMap {
	return &AgentNameMap{
		nameToPane:  make(map[string]string),
		paneToName:  make(map[string]string),
		nameToType:  make(map[string]string),
		sessionName: sessionName,
	}
}

// NewAgentNameMapWithCustomNames creates an AgentNameMap with user-supplied custom names.
// Custom names are used in order as agents are assigned names.
// If more agents exist than custom names, the NATO alphabet is used for the remainder.
func NewAgentNameMapWithCustomNames(sessionName string, customNames []string) *AgentNameMap {
	m := NewAgentNameMap(sessionName)
	m.customNames = customNames
	return m
}

// GenerateName creates a name for an agent based on its type and position.
// The name format is "{prefix}-{nato_word}" (e.g., "claude-alpha", "codex-bravo").
// If custom names are configured and available, those are used instead.
func (m *AgentNameMap) GenerateName(agentType string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.generateNameLocked(agentType)
}

// generateNameLocked is the lock-free version of GenerateName for internal use.
// Caller must hold m.mu.
func (m *AgentNameMap) generateNameLocked(agentType string) string {
	// Check if there are custom names left to use
	if m.customOffset < len(m.customNames) {
		name := m.customNames[m.customOffset]
		m.customOffset++
		return name
	}

	// Use NATO phonetic alphabet with agent type prefix
	prefix, ok := agentTypePrefix[agentType]
	if !ok {
		prefix = agentType
	}

	var natoWord string
	if m.natoIndex < len(NATOAlphabet) {
		natoWord = NATOAlphabet[m.natoIndex]
	} else {
		// Wrap around with numeric suffix for >26 agents
		idx := m.natoIndex % len(NATOAlphabet)
		cycle := m.natoIndex / len(NATOAlphabet)
		natoWord = fmt.Sprintf("%s-%d", NATOAlphabet[idx], cycle+1)
	}
	m.natoIndex++

	return fmt.Sprintf("%s-%s", prefix, natoWord)
}

// Assign associates a name with a pane reference and agent type.
func (m *AgentNameMap) Assign(name, paneRef, agentType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nameToPane[name] = paneRef
	m.paneToName[paneRef] = name
	m.nameToType[name] = agentType
}

// AssignNew generates a name for the agent type and assigns it to the pane reference.
// Returns the generated name.
func (m *AgentNameMap) AssignNew(agentType, paneRef string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := m.generateNameLocked(agentType)
	m.nameToPane[name] = paneRef
	m.paneToName[paneRef] = name
	m.nameToType[name] = agentType
	return name
}

// NameForPane returns the name assigned to a pane reference.
// Returns empty string and false if no name is assigned.
func (m *AgentNameMap) NameForPane(paneRef string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	name, ok := m.paneToName[paneRef]
	return name, ok
}

// PaneForName returns the pane reference for a given agent name.
// Returns empty string and false if the name is not found.
func (m *AgentNameMap) PaneForName(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pane, ok := m.nameToPane[name]
	return pane, ok
}

// TypeForName returns the agent type for a given agent name.
func (m *AgentNameMap) TypeForName(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.nameToType[name]
	return t, ok
}

// AllNames returns all assigned names sorted by pane reference.
func (m *AgentNameMap) AllNames() []AgentNameEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries := make([]AgentNameEntry, 0, len(m.nameToPane))
	for name, pane := range m.nameToPane {
		entries = append(entries, AgentNameEntry{
			Name:      name,
			Pane:      pane,
			AgentType: m.nameToType[name],
		})
	}

	// Sort by pane reference for deterministic output
	sortAgentNameEntries(entries)
	return entries
}

// Count returns the number of named agents.
func (m *AgentNameMap) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.nameToPane)
}

// AgentNameEntry represents a single name-to-pane mapping.
type AgentNameEntry struct {
	Name      string `json:"name"`
	Pane      string `json:"pane"`
	AgentType string `json:"agent_type"`
}

// AgentNamesOutput is the structured JSON output for --robot-agent-names.
type AgentNamesOutput struct {
	RobotResponse
	Session string           `json:"session"`
	Agents  []AgentNameEntry `json:"agents"`
	Count   int              `json:"count"`
}

// GetAgentNames returns the agent name mapping for a session.
// It inspects the active tmux session and generates names for all agents found.
func GetAgentNames(sessionName string, customNames []string) (*AgentNamesOutput, error) {
	output := &AgentNamesOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       sessionName,
		Agents:        []AgentNameEntry{}, // Always non-nil per envelope spec
	}

	if !tmuxInstalledFn() {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("tmux is not installed"),
			ErrCodeDependencyMissing,
			"Install tmux to use agent naming",
		)
		return output, nil
	}

	if !tmuxSessionExistsFn(sessionName) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", sessionName),
			ErrCodeSessionNotFound,
			"Use 'ntm --robot-status' to see available sessions",
		)
		return output, nil
	}

	// Build name map from current session state
	nameMap := BuildNameMapFromSession(sessionName, customNames)
	output.Agents = nameMap.AllNames()
	output.Count = len(output.Agents)

	return output, nil
}

// PrintAgentNames outputs agent names as JSON.
func PrintAgentNames(sessionName string, customNames []string) error {
	output, err := GetAgentNames(sessionName, customNames)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// BuildNameMapFromSession inspects a tmux session and generates names for each agent pane.
func BuildNameMapFromSession(sessionName string, customNames []string) *AgentNameMap {
	var nameMap *AgentNameMap
	if len(customNames) > 0 {
		nameMap = NewAgentNameMapWithCustomNames(sessionName, customNames)
	} else {
		nameMap = NewAgentNameMap(sessionName)
	}

	// Get panes from the session
	panes := tmuxGetPanesFn(sessionName)
	if panes == nil {
		return nameMap
	}

	for _, pane := range panes {
		agentType := resolveAgentTypeForNaming(pane)
		paneRef := fmt.Sprintf("0.%d", pane.Index)
		nameMap.AssignNew(agentType, paneRef)
	}

	return nameMap
}

func resolveAgentTypeForNaming(pane tmuxPaneInfo) string {
	if resolved := ResolveAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	if agentType := detectAgentTypeFromTitle(pane.Title); agentType != "" {
		return agentType
	}
	return "user"
}

// detectAgentTypeFromTitle extracts the agent type from a tmux pane title.
// Pane titles follow the format "SESSION__TYPE_NUM" (e.g., "myproj__cc_1").
func detectAgentTypeFromTitle(title string) string {
	lower := strings.ToLower(title)

	if idx := strings.LastIndex(lower, "__"); idx >= 0 && idx+2 < len(lower) {
		typePart := lower[idx+2:]
		if underscore := strings.IndexByte(typePart, '_'); underscore >= 0 {
			typePart = typePart[:underscore]
		}
		if resolved := ResolveAgentType(typePart); resolved != "" && resolved != "unknown" {
			return resolved
		}
	}

	if detected := DetectAgentType(lower); detected != "unknown" {
		return detected
	}
	if strings.Contains(lower, "user") {
		return "user"
	}
	return ""
}

// ParseCustomNames parses a comma-separated list of custom agent names.
func ParseCustomNames(namesList string) []string {
	if namesList == "" {
		return nil
	}
	parts := strings.Split(namesList, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			names = append(names, trimmed)
		}
	}
	return names
}

// sortAgentNameEntries sorts entries by pane reference for deterministic output.
func sortAgentNameEntries(entries []AgentNameEntry) {
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Pane > entries[j].Pane {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}

// tmux wrapper function variables - testable via swapping.
// In production these point to real tmux implementations.
// Tests can override them to avoid requiring a real tmux instance.
var tmuxInstalledFn = func() bool { return tmuxIsInstalledReal() }
var tmuxSessionExistsFn = func(name string) bool { return tmuxSessionExistsReal(name) }
var tmuxGetPanesFn = func(session string) []tmuxPaneInfo { return tmuxGetPanesReal(session) }

// tmuxPaneInfo is a lightweight pane representation for naming purposes.
type tmuxPaneInfo struct {
	Index int
	Title string
	Type  tmux.AgentType
}
