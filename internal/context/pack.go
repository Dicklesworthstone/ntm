// Package context provides context pack building for agent task assignment.
package context

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// TokenBudgets defines context limits per agent type
var TokenBudgets = map[string]int{
	"cc":      180000, // Claude Opus 4.5
	"cod":     120000, // OpenAI Codex
	"gmi":     100000, // Google Gemini
	"default": 100000,
}

// BudgetAllocation defines percentage allocation per component
type BudgetAllocation struct {
	Triage int // 10%
	CM     int // 5%
	CASS   int // 15%
	S2P    int // 70%
}

// DefaultBudgetAllocation returns the standard allocation
func DefaultBudgetAllocation() BudgetAllocation {
	return BudgetAllocation{
		Triage: 10,
		CM:     5,
		CASS:   15,
		S2P:    70,
	}
}

// PackComponent represents a component of the context pack
type PackComponent struct {
	Type       string          `json:"type"`
	Data       json.RawMessage `json:"data,omitempty"`
	TokenCount int             `json:"token_count"`
	Error      string          `json:"error,omitempty"`
}

// ContextPackFull extends the basic state.ContextPack with component data
type ContextPackFull struct {
	state.ContextPack
	Components map[string]*PackComponent `json:"components,omitempty"`
}

// BuildOptions configures how a context pack is built
type BuildOptions struct {
	BeadID        string
	AgentType     string // cc, cod, gmi
	RepoRev       string
	Task          string   // Task description for CM context
	Files         []string // Files for S2P context
	CorrelationID string
	ProjectDir    string
	SessionID     string // For CM client connection
}

// Package-level cache shared across all builders
var (
	globalCacheMu sync.RWMutex
	globalCache   = make(map[string]*ContextPackFull)
)

// ContextPackBuilder builds context packs from multiple sources
type ContextPackBuilder struct {
	bvAdapter   *tools.BVAdapter
	cmAdapter   *tools.CMAdapter
	cassAdapter *tools.CASSAdapter
	s2pAdapter  *tools.S2PAdapter
	store       *state.Store
	allocation  BudgetAllocation
}

// NewContextPackBuilder creates a new context pack builder
func NewContextPackBuilder(store *state.Store) *ContextPackBuilder {
	return &ContextPackBuilder{
		bvAdapter:   tools.NewBVAdapter(),
		cmAdapter:   tools.NewCMAdapter(),
		cassAdapter: tools.NewCASSAdapter(),
		s2pAdapter:  tools.NewS2PAdapter(),
		store:       store,
		allocation:  DefaultBudgetAllocation(),
	}
}

// SetAllocation overrides the default budget allocation
func (b *ContextPackBuilder) SetAllocation(alloc BudgetAllocation) {
	b.allocation = alloc
}

// cacheKey generates a cache key from build options
func cacheKey(opts BuildOptions) string {
	h := sha256.New()
	h.Write([]byte(opts.RepoRev))
	h.Write([]byte(opts.BeadID))
	h.Write([]byte(opts.AgentType))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// Build constructs a context pack for a task
func (b *ContextPackBuilder) Build(ctx context.Context, opts BuildOptions) (*ContextPackFull, error) {
	// Check cache
	key := cacheKey(opts)
	globalCacheMu.RLock()
	if cached, ok := globalCache[key]; ok {
		globalCacheMu.RUnlock()
		return cached, nil
	}
	globalCacheMu.RUnlock()

	// Determine budget
	budget := TokenBudgets[opts.AgentType]
	if budget == 0 {
		budget = TokenBudgets["default"]
	}

	// Initialize pack
	pack := &ContextPackFull{
		ContextPack: state.ContextPack{
			ID:            generatePackID(),
			BeadID:        opts.BeadID,
			AgentType:     state.AgentType(opts.AgentType),
			RepoRev:       opts.RepoRev,
			CorrelationID: opts.CorrelationID,
			CreatedAt:     time.Now(),
		},
		Components: make(map[string]*PackComponent),
	}

	// Build components in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex

	// BV Triage (10%)
	wg.Add(1)
	go func() {
		defer wg.Done()
		component := b.buildTriageComponent(ctx, opts.ProjectDir, budget*b.allocation.Triage/100)
		mu.Lock()
		pack.Components["triage"] = component
		mu.Unlock()
	}()

	// CM Rules (5%)
	wg.Add(1)
	go func() {
		defer wg.Done()
		component := b.buildCMComponent(ctx, opts, budget*b.allocation.CM/100)
		mu.Lock()
		pack.Components["cm"] = component
		mu.Unlock()
	}()

	// CASS History (15%)
	wg.Add(1)
	go func() {
		defer wg.Done()
		component := b.buildCASSComponent(ctx, opts.Task, budget*b.allocation.CASS/100)
		mu.Lock()
		pack.Components["cass"] = component
		mu.Unlock()
	}()

	// S2P Context (70%)
	wg.Add(1)
	go func() {
		defer wg.Done()
		component := b.buildS2PComponent(ctx, opts.ProjectDir, opts.Files, budget*b.allocation.S2P/100)
		mu.Lock()
		pack.Components["s2p"] = component
		mu.Unlock()
	}()

	wg.Wait()

	// Render to agent-specific format
	pack.RenderedPrompt = b.render(pack)
	pack.TokenCount = estimateTokens(pack.RenderedPrompt)

	// Final overflow check
	if pack.TokenCount > budget {
		pack = b.truncateOverflow(pack, budget)
	}

	// Cache
	globalCacheMu.Lock()
	globalCache[key] = pack
	globalCacheMu.Unlock()

	// Store in database if store is available
	if b.store != nil {
		_ = b.store.CreateContextPack(&pack.ContextPack)
	}

	return pack, nil
}

// buildTriageComponent fetches BV triage data
func (b *ContextPackBuilder) buildTriageComponent(ctx context.Context, dir string, tokenBudget int) *PackComponent {
	component := &PackComponent{Type: "triage"}

	_, installed := b.bvAdapter.Detect()
	if !installed {
		component.Error = "bv not installed"
		return component
	}

	data, err := b.bvAdapter.GetTriage(ctx, dir)
	if err != nil {
		component.Error = err.Error()
		return component
	}

	component.Data = truncateJSON(data, tokenBudget)
	component.TokenCount = estimateTokens(string(component.Data))
	return component
}

// buildCMComponent fetches CM context data
func (b *ContextPackBuilder) buildCMComponent(ctx context.Context, opts BuildOptions, tokenBudget int) *PackComponent {
	component := &PackComponent{Type: "cm"}

	_, installed := b.cmAdapter.Detect()
	if !installed {
		component.Error = "cm not installed"
		return component
	}

	// Try to connect if we have session info
	if opts.ProjectDir != "" && opts.SessionID != "" {
		_ = b.cmAdapter.Connect(opts.ProjectDir, opts.SessionID)
	}

	data, err := b.cmAdapter.GetContext(ctx, opts.Task)
	if err != nil {
		component.Error = err.Error()
		return component
	}

	component.Data = truncateJSON(data, tokenBudget)
	component.TokenCount = estimateTokens(string(component.Data))
	return component
}

// buildCASSComponent fetches CASS search results
func (b *ContextPackBuilder) buildCASSComponent(ctx context.Context, query string, tokenBudget int) *PackComponent {
	component := &PackComponent{Type: "cass"}

	_, installed := b.cassAdapter.Detect()
	if !installed {
		component.Error = "cass not installed"
		return component
	}

	if query == "" {
		component.Error = "no query provided"
		return component
	}

	// Estimate limit based on token budget (rough: ~100 tokens per result)
	limit := tokenBudget / 100
	if limit < 5 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}

	data, err := b.cassAdapter.Search(ctx, query, limit)
	if err != nil {
		component.Error = err.Error()
		return component
	}

	component.Data = truncateJSON(data, tokenBudget)
	component.TokenCount = estimateTokens(string(component.Data))
	return component
}

// buildS2PComponent generates S2P context with agent-aware budget enforcement
func (b *ContextPackBuilder) buildS2PComponent(ctx context.Context, dir string, files []string, tokenBudget int) *PackComponent {
	component := &PackComponent{Type: "s2p"}

	_, installed := b.s2pAdapter.Detect()
	if !installed {
		component.Error = "s2p not installed"
		return component
	}

	if len(files) == 0 {
		component.Error = "no files specified"
		return component
	}

	// Apply agent-specific file limits and processing strategies
	optimizedFiles := b.optimizeFilesForBudget(files, tokenBudget)
	format := b.selectS2PFormat(tokenBudget)

	data, err := b.s2pAdapter.GenerateContext(ctx, dir, optimizedFiles, format)
	if err != nil {
		component.Error = err.Error()
		return component
	}

	// Apply intelligent truncation that preserves structure
	truncated := b.intelligentTruncate(string(data), tokenBudget)
	component.Data = json.RawMessage(fmt.Sprintf("%q", truncated))
	component.TokenCount = estimateTokens(truncated)
	return component
}

// render creates the final prompt in agent-appropriate format
func (b *ContextPackBuilder) render(pack *ContextPackFull) string {
	switch pack.AgentType {
	case state.AgentTypeClaude:
		return b.renderXML(pack)
	default:
		return b.renderMarkdown(pack)
	}
}

// renderXML creates XML-formatted output for Claude
func (b *ContextPackBuilder) renderXML(pack *ContextPackFull) string {
	var sb strings.Builder

	sb.WriteString("<context_pack>\n")
	sb.WriteString(fmt.Sprintf("  <id>%s</id>\n", pack.ID))
	sb.WriteString(fmt.Sprintf("  <bead_id>%s</bead_id>\n", pack.BeadID))
	sb.WriteString(fmt.Sprintf("  <repo_rev>%s</repo_rev>\n", pack.RepoRev))

	// Use consistent ordering (same as renderMarkdown)
	order := []string{"triage", "cm", "cass", "s2p"}
	for _, name := range order {
		comp, ok := pack.Components[name]
		if !ok {
			continue
		}
		if comp.Error != "" {
			sb.WriteString(fmt.Sprintf("  <%s unavailable=\"true\">%s</%s>\n", name, comp.Error, name))
			continue
		}
		if len(comp.Data) > 0 {
			sb.WriteString(fmt.Sprintf("  <%s>\n", name))
			sb.WriteString(fmt.Sprintf("    %s\n", string(comp.Data)))
			sb.WriteString(fmt.Sprintf("  </%s>\n", name))
		}
	}

	sb.WriteString("</context_pack>")
	return sb.String()
}

// renderMarkdown creates Markdown-formatted output for Codex/Gemini
func (b *ContextPackBuilder) renderMarkdown(pack *ContextPackFull) string {
	var sb strings.Builder

	sb.WriteString("# Context Pack\n\n")
	sb.WriteString(fmt.Sprintf("- **ID**: %s\n", pack.ID))
	sb.WriteString(fmt.Sprintf("- **Bead**: %s\n", pack.BeadID))
	sb.WriteString(fmt.Sprintf("- **Repo Rev**: %s\n\n", pack.RepoRev))

	order := []string{"triage", "cm", "cass", "s2p"}
	for _, name := range order {
		comp, ok := pack.Components[name]
		if !ok {
			continue
		}

		title := componentTitle(name)
		sb.WriteString(fmt.Sprintf("## %s\n\n", title))

		if comp.Error != "" {
			sb.WriteString(fmt.Sprintf("*Unavailable: %s*\n\n", comp.Error))
			continue
		}

		if len(comp.Data) > 0 {
			// For JSON data, format as code block
			if name == "s2p" {
				// S2P is quoted text, unquote it
				var text string
				if err := json.Unmarshal(comp.Data, &text); err == nil {
					sb.WriteString(text)
				} else {
					sb.WriteString(string(comp.Data))
				}
			} else {
				sb.WriteString("```json\n")
				sb.WriteString(string(comp.Data))
				sb.WriteString("\n```\n")
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// componentTitle returns a human-readable title for a component
func componentTitle(name string) string {
	switch name {
	case "triage":
		return "BV Triage (Priority & Planning)"
	case "cm":
		return "CM Rules (Learned Guidelines)"
	case "cass":
		return "CASS History (Prior Solutions)"
	case "s2p":
		return "File Context"
	default:
		// Title case the first letter (replacement for deprecated strings.Title)
		if len(name) == 0 {
			return name
		}
		return strings.ToUpper(name[:1]) + name[1:]
	}
}

// truncateOverflow trims the pack to fit within budget
func (b *ContextPackBuilder) truncateOverflow(pack *ContextPackFull, budget int) *ContextPackFull {
	// Re-render with reduced content
	// Priority: keep triage and s2p, reduce cass and cm first
	if cass, ok := pack.Components["cass"]; ok && cass.Data != nil {
		cass.Data = truncateJSON(cass.Data, len(cass.Data)/2)
		cass.TokenCount = estimateTokens(string(cass.Data))
	}

	pack.RenderedPrompt = b.render(pack)
	pack.TokenCount = estimateTokens(pack.RenderedPrompt)
	return pack
}

// ClearCache clears the context pack cache
func (b *ContextPackBuilder) ClearCache() {
	globalCacheMu.Lock()
	globalCache = make(map[string]*ContextPackFull)
	globalCacheMu.Unlock()
}

// CacheStats returns cache statistics
func (b *ContextPackBuilder) CacheStats() (size int, keys []string) {
	globalCacheMu.RLock()
	defer globalCacheMu.RUnlock()

	size = len(globalCache)
	for k := range globalCache {
		keys = append(keys, k)
	}
	return
}

// Helper functions

// generatePackID creates a unique pack ID
func generatePackID() string {
	return fmt.Sprintf("pack-%d", time.Now().UnixNano())
}

// estimateTokens estimates token count (rough: 4 chars per token)
func estimateTokens(s string) int {
	return len(s) / 4
}

// truncateJSON truncates JSON to fit within token budget while keeping it valid
func truncateJSON(data json.RawMessage, tokenBudget int) json.RawMessage {
	charBudget := tokenBudget * 4 // rough conversion
	if len(data) <= charBudget {
		return data
	}

	// Try to parse as array and truncate elements
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err == nil {
		// Binary search for max elements that fit
		lo, hi := 0, len(arr)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			truncated := arr[:mid]
			result, _ := json.Marshal(truncated)
			if len(result) <= charBudget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		if lo > 0 {
			result, _ := json.Marshal(arr[:lo])
			return result
		}
	}

	// Try to parse as object and include a truncation indicator
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err == nil {
		// Create truncated version with indicator
		truncated := map[string]interface{}{
			"_truncated":     true,
			"_original_size": len(data),
		}
		// Add fields until we hit budget
		for k, v := range obj {
			truncated[k] = v
			result, _ := json.Marshal(truncated)
			if len(result) > charBudget {
				delete(truncated, k)
				break
			}
		}
		result, _ := json.Marshal(truncated)
		return result
	}

	// Fallback: wrap raw bytes in a truncation indicator object
	// This ensures we return valid JSON even for edge cases
	fallback := map[string]interface{}{
		"_truncated": true,
		"_message":   "data too large to include",
	}
	result, _ := json.Marshal(fallback)
	return result
}

// truncateText truncates text to fit within token budget
func truncateText(text string, tokenBudget int) string {
	charBudget := tokenBudget * 4
	if len(text) <= charBudget {
		return text
	}
	return text[:charBudget] + "\n...[truncated]"
}

// optimizeFilesForBudget applies agent-specific file selection strategies
func (b *ContextPackBuilder) optimizeFilesForBudget(files []string, tokenBudget int) []string {
	// Estimate files per token budget - smaller budgets get fewer files
	maxFiles := tokenBudget / 2000 // Roughly 2000 tokens per file
	if maxFiles < 3 {
		maxFiles = 3 // Minimum files for context
	}
	if maxFiles > 20 {
		maxFiles = 20 // Maximum to prevent overwhelm
	}

	if len(files) <= maxFiles {
		return files
	}

	// Prioritize files by importance heuristics
	prioritized := b.prioritizeFilesByImportance(files)
	return prioritized[:maxFiles]
}

// prioritizeFilesByImportance sorts files by likely importance for context
func (b *ContextPackBuilder) prioritizeFilesByImportance(files []string) []string {
	type fileWithPriority struct {
		path     string
		priority int
	}

	priorityFiles := make([]fileWithPriority, 0, len(files))

	for _, file := range files {
		priority := b.calculateFilePriority(file)
		priorityFiles = append(priorityFiles, fileWithPriority{file, priority})
	}

	// Sort by priority (higher number = higher priority)
	for i := 0; i < len(priorityFiles)-1; i++ {
		for j := i + 1; j < len(priorityFiles); j++ {
			if priorityFiles[i].priority < priorityFiles[j].priority {
				priorityFiles[i], priorityFiles[j] = priorityFiles[j], priorityFiles[i]
			}
		}
	}

	result := make([]string, len(priorityFiles))
	for i, fp := range priorityFiles {
		result[i] = fp.path
	}
	return result
}

// calculateFilePriority assigns priority scores to files
func (b *ContextPackBuilder) calculateFilePriority(file string) int {
	priority := 0
	lower := strings.ToLower(file)

	// High priority: Main entry points
	if strings.Contains(lower, "main.") ||
	   strings.Contains(lower, "index.") ||
	   strings.Contains(lower, "app.") {
		priority += 100
	}

	// Medium-high priority: Core logic files
	if strings.Contains(lower, "core") ||
	   strings.Contains(lower, "service") ||
	   strings.Contains(lower, "controller") ||
	   strings.Contains(lower, "handler") {
		priority += 50
	}

	// Medium priority: Configuration and important modules
	if strings.Contains(lower, "config") ||
	   strings.Contains(lower, "router") ||
	   strings.Contains(lower, "middleware") {
		priority += 30
	}

	// Lower priority: Tests, docs, examples
	if strings.Contains(lower, "test") ||
	   strings.Contains(lower, "spec") ||
	   strings.Contains(lower, "example") ||
	   strings.Contains(lower, "demo") {
		priority -= 20
	}

	// Bonus for shorter paths (likely more important)
	if strings.Count(file, "/") <= 2 {
		priority += 10
	}

	return priority
}

// selectS2PFormat chooses optimal s2p format based on token budget
func (b *ContextPackBuilder) selectS2PFormat(tokenBudget int) string {
	// Use more compact formats for smaller budgets
	if tokenBudget < 30000 { // Less than ~30k tokens
		return "compact" // More concise output if s2p supports it
	}
	return "" // Default format
}

// intelligentTruncate preserves important content structure when truncating
func (b *ContextPackBuilder) intelligentTruncate(text string, tokenBudget int) string {
	charBudget := tokenBudget * 4
	if len(text) <= charBudget {
		return text
	}

	lines := strings.Split(text, "\n")
	var result strings.Builder
	var currentSize int

	// Phase 1: Include file headers and important structural elements
	for i, line := range lines {
		lineSize := len(line) + 1 // +1 for newline

		// Always include file boundaries and headers
		if strings.HasPrefix(line, "=== ") ||
		   strings.HasPrefix(line, "# ") ||
		   strings.Contains(line, "File: ") ||
		   i < 3 { // First few lines often contain metadata
			if currentSize + lineSize <= charBudget {
				result.WriteString(line + "\n")
				currentSize += lineSize
				continue
			}
		}

		// For regular content, check budget
		if currentSize + lineSize > charBudget {
			result.WriteString("\n...[truncated - content exceeded budget]\n")
			break
		}

		result.WriteString(line + "\n")
		currentSize += lineSize
	}

	return result.String()
}
