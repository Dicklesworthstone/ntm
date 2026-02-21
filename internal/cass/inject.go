// Package cass provides CASS integration including context injection.
package cass

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// CASSConfig holds configuration for CASS queries.
type CASSConfig struct {
	Enabled           bool     `json:"enabled"`
	MaxResults        int      `json:"max_results"`
	MaxAgeDays        int      `json:"max_age_days"`
	MinRelevance      float64  `json:"min_relevance"`
	PreferSameProject bool     `json:"prefer_same_project"`
		// AgentFilter limits results to specific agent types (e.g., "claude", "codex").
		// Empty means all agents.
		AgentFilter []string `json:"agent_filter,omitempty"`
	
		// BinaryPath is the path to the cass binary.
		BinaryPath string `json:"binary_path,omitempty"`
	}
	
	// DefaultCASSConfig returns sensible defaults for CASS queries.
	func DefaultCASSConfig() CASSConfig {
		return CASSConfig{
			Enabled:           true,
			MaxResults:        5,
			MaxAgeDays:        30,
			MinRelevance:      0.0,
			PreferSameProject: true,
			AgentFilter:       nil,
			BinaryPath:        "",
		}
	}
	
	// CASSHit represents a single search result from CASS.
	type CASSHit struct {
		SourcePath string  `json:"source_path"`
		LineNumber int     `json:"line_number"`
		Agent      string  `json:"agent"`
		Content    string  `json:"content,omitempty"`
		Score      float64 `json:"score,omitempty"`
	}
	
	// CASSQueryResult holds the results of a CASS query.
	type CASSQueryResult struct {
		Success      bool          `json:"success"`
		Query        string        `json:"query"`
		Hits         []CASSHit     `json:"hits"`
		TotalMatches int           `json:"total_matches"`
		QueryTime    time.Duration `json:"query_time_ms"`
		Error        string        `json:"error,omitempty"`
		Keywords     []string      `json:"keywords,omitempty"`
	}
	
	// cassSearchResponse matches the JSON structure returned by `cass search --json`.
	type cassSearchResponse struct {
		Query        string `json:"query"`
		TotalMatches int    `json:"total_matches"`
		Hits         []struct {
			SourcePath string  `json:"source_path"`
			LineNumber int     `json:"line_number"`
			Agent      string  `json:"agent"`
			Content    string  `json:"content,omitempty"`
			Score      float64 `json:"score,omitempty"`
		} `json:"hits"`
	}
	
	// QueryCASS queries CASS for relevant historical context based on the prompt.
	func QueryCASS(prompt string, config CASSConfig) CASSQueryResult {
		start := time.Now()
		result := CASSQueryResult{
			Success: false,
			Query:   "",
			Hits:    []CASSHit{},
		}
	
		if !config.Enabled {
			result.Success = true
			return result
		}
	
		keywords := ExtractKeywords(prompt)
		result.Keywords = keywords
	
		if len(keywords) == 0 {
			result.Success = true
			result.Error = "no keywords extracted from prompt"
			return result
		}
	
		query := strings.Join(keywords, " ")
		result.Query = query
	
		binPath := config.BinaryPath
		if binPath == "" {
			binPath = "cass"
		}
	
		if !isCASSAvailable(binPath) {
			result.Error = "cass command not found"
			return result
		}
	
		args := []string{"search", query, "--json"}
		if config.MaxResults > 0 {
			args = append(args, "--limit", strconv.Itoa(config.MaxResults))
		}
		if config.MaxAgeDays > 0 {
			args = append(args, "--days", strconv.Itoa(config.MaxAgeDays))
		}
		for _, agent := range config.AgentFilter {
			args = append(args, "--agent", agent)
		}
	
		cmd := exec.Command(binPath, args...)
	output, err := cmd.Output()
	result.QueryTime = time.Since(start)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			result.Success = true
			return result
		}
		result.Error = err.Error()
		return result
	}

	var resp cassSearchResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		result.Error = "failed to parse CASS response: " + err.Error()
		return result
	}

	result.TotalMatches = resp.TotalMatches
	for _, hit := range resp.Hits {
		result.Hits = append(result.Hits, CASSHit{
			SourcePath: hit.SourcePath,
			LineNumber: hit.LineNumber,
			Agent:      hit.Agent,
			Content:    hit.Content,
			Score:      hit.Score,
		})
	}

	result.Success = true
	return result
}

// ExtractKeywords extracts meaningful keywords from a prompt.
func ExtractKeywords(prompt string) []string {
	text := strings.ToLower(prompt)
	text = removeCodeBlocks(text)
	words := tokenize(text)

	var keywords []string
	seen := make(map[string]bool)

	for _, word := range words {
		if len(word) < 3 {
			continue
		}
		if isStopWord(word) {
			continue
		}
		if seen[word] {
			continue
		}
		seen[word] = true
		keywords = append(keywords, word)
	}

	if len(keywords) > 10 {
		keywords = keywords[:10]
	}

	return keywords
}

func tokenize(text string) []string {
	var words []string
	var current strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

func removeCodeBlocks(text string) string {
	re := regexp.MustCompile("(?s)```.*?```")
	text = re.ReplaceAllString(text, " ")
	re = regexp.MustCompile("`[^`]+`")
	text = re.ReplaceAllString(text, " ")
	return text
}

func isStopWord(word string) bool {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "by": true, "from": true,
		"as": true, "is": true, "was": true, "are": true, "were": true,
		"been": true, "be": true, "have": true, "has": true, "had": true,
		"do": true, "does": true, "did": true, "will": true, "would": true,
		"could": true, "should": true, "may": true, "might": true,
		"this": true, "that": true, "these": true, "those": true,
		"it": true, "its": true, "they": true, "them": true, "their": true,
		"we": true, "you": true, "your": true, "our": true, "my": true,
		"me": true, "him": true, "her": true, "his": true, "she": true,
		"he": true, "i": true, "all": true, "each": true, "every": true,
		"both": true, "few": true, "more": true, "most": true, "other": true,
		"some": true, "such": true, "no": true, "nor": true, "not": true,
		"only": true, "own": true, "same": true, "so": true, "than": true,
		"too": true, "very": true, "just": true, "also": true, "now": true,
		"can": true, "get": true, "got": true, "how": true, "what": true,
		"when": true, "where": true, "which": true, "who": true, "why": true,
		"new": true, "use": true, "used": true, "using": true,
		"make": true, "made": true, "like": true, "want": true, "need": true,
		"please": true, "help": true, "here": true, "there": true,
		"code": true, "file": true, "function": true, "method": true,
		"class": true, "variable": true, "add": true, "create": true,
		"update": true, "delete": true, "remove": true, "change": true,
		"fix": true, "bug": true, "error": true, "test": true, "write": true,
		"read": true, "run": true, "start": true, "stop": true,
	}
	return stopWords[word]
}

func isCASSAvailable(binPath string) bool {
	_, err := exec.LookPath(binPath)
	return err == nil
}

// InjectionFormat defines how context should be formatted.
type InjectionFormat string

const (
	FormatMarkdown   InjectionFormat = "markdown"
	FormatMinimal    InjectionFormat = "minimal"
	FormatStructured InjectionFormat = "structured"
)

// InjectConfig holds configuration for context injection.
type InjectConfig struct {
	Format            InjectionFormat `json:"format"`
	MaxTokens         int             `json:"max_tokens"`
	SkipThreshold     int             `json:"skip_threshold"`
	CurrentContextPct int             `json:"current_context_pct,omitempty"`
	IncludeMetadata   bool            `json:"include_metadata"`
	DryRun            bool            `json:"dry_run"`
}

// DefaultInjectConfig returns sensible defaults.
func DefaultInjectConfig() InjectConfig {
	return InjectConfig{
		Format:          FormatMarkdown,
		MaxTokens:       500,
		SkipThreshold:   60,
		IncludeMetadata: true,
		DryRun:          false,
	}
}

// InjectionMetadata tracks details about what was injected.
type InjectionMetadata struct {
	Enabled       bool            `json:"enabled"`
	ItemsFound    int             `json:"items_found"`
	ItemsInjected int             `json:"items_injected"`
	ItemsFiltered int             `json:"items_filtered"`
	TokensAdded   int             `json:"tokens_added"`
	FormatUsed    InjectionFormat `json:"format_used"`
	SkippedReason string          `json:"skipped_reason,omitempty"`
}

// InjectionResult holds the result of context injection.
type InjectionResult struct {
	Success         bool              `json:"success"`
	ModifiedPrompt  string            `json:"modified_prompt"`
	InjectedContext string            `json:"injected_context"`
	Metadata        InjectionMetadata `json:"metadata"`
	Error           string            `json:"error,omitempty"`
}

// InjectContext prepends relevant CASS context to a prompt.
func InjectContext(prompt string, hits []ScoredHit, config InjectConfig) InjectionResult {
	result := InjectionResult{
		Success: false,
		Metadata: InjectionMetadata{
			Enabled:    true,
			ItemsFound: len(hits),
			FormatUsed: config.Format,
		},
	}

	if config.CurrentContextPct > 0 && config.CurrentContextPct >= config.SkipThreshold {
		result.Success = true
		result.ModifiedPrompt = prompt
		result.Metadata.SkippedReason = "context at " + strconv.Itoa(config.CurrentContextPct) + "%"
		return result
	}

	if len(hits) == 0 {
		result.Success = true
		result.ModifiedPrompt = prompt
		result.Metadata.SkippedReason = "no relevant context found"
		return result
	}

	context := FormatContext(hits, config)
	estimatedTokens := len(context) / 4
	result.Metadata.TokensAdded = estimatedTokens

	if config.MaxTokens > 0 && estimatedTokens > config.MaxTokens {
		context = truncateToTokens(context, config.MaxTokens)
		result.Metadata.TokensAdded = config.MaxTokens
	}

	result.InjectedContext = context
	result.Metadata.ItemsInjected = countInjectedItems(context, config.Format)
	result.Metadata.ItemsFiltered = len(hits) - result.Metadata.ItemsInjected

	if config.DryRun {
		result.Success = true
		result.ModifiedPrompt = prompt
		return result
	}

	result.Success = true
	result.ModifiedPrompt = context + "\n---\n\n" + prompt
	return result
}

// FormatContext formats CASS hits for injection.
func FormatContext(hits []ScoredHit, config InjectConfig) string {
	if len(hits) == 0 {
		return ""
	}
	switch config.Format {
	case FormatMinimal:
		return formatMinimal(hits)
	case FormatStructured:
		return formatStructured(hits)
	default:
		return formatMarkdown(hits)
	}
}

func formatMarkdown(hits []ScoredHit) string {
	var b strings.Builder
	b.WriteString("## Relevant Context from Past Sessions\n\n")
	for i, hit := range hits {
		sessionName := ExtractSessionName(hit.SourcePath)
		age := formatAge(hit.SourcePath)
		relevance := int(hit.ComputedScore * 100)
		b.WriteString("### Session: " + sessionName + " (" + strconv.Itoa(relevance) + "% match")
		if age != "" {
			b.WriteString(", " + age)
		}
		b.WriteString(")\n\n")
		if hit.Content != "" {
			content := cleanContentForMarkdown(hit.Content)
			b.WriteString(content + "\n")
		}
		if i < len(hits)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatMinimal(hits []ScoredHit) string {
	var b strings.Builder
	b.WriteString("// Related context:\n")
	itemsWritten := 0
	for _, hit := range hits {
		if hit.Content != "" {
			content := extractCodeSnippets(hit.Content)
			if content != "" {
				if itemsWritten > 0 {
					b.WriteString("\n// ---\n")
				}
				b.WriteString(content + "\n")
				itemsWritten++
			}
		}
	}
	return b.String()
}

func formatStructured(hits []ScoredHit) string {
	var b strings.Builder
	b.WriteString("RELEVANT CONTEXT FROM PAST SESSIONS\n====================================\n\n")
	for i, hit := range hits {
		sessionName := ExtractSessionName(hit.SourcePath)
		relevance := int(hit.ComputedScore * 100)
		b.WriteString(fmt.Sprintf("%d. Session: %s\n", i+1, sessionName))
		b.WriteString(fmt.Sprintf("   Relevance: %d%%\n", relevance))
		if hit.Content != "" {
			b.WriteString("   Content:\n")
			content := cleanContentForMarkdown(hit.Content)
			lines := strings.Split(content, "\n")
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					b.WriteString("   | " + line + "\n")
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ExtractSessionName extracts a readable session name from the file path.
func ExtractSessionName(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	filename := parts[len(parts)-1]
	if filename == "" {
		return ""
	}
	filename = strings.TrimSuffix(filename, ".jsonl")
	filename = strings.TrimSuffix(filename, ".json")
	if len(filename) > 40 {
		filename = filename[:37] + "..."
	}
	return filename
}

func formatAge(path string) string {
	date := ExtractSessionDate(path)
	if date.IsZero() {
		return ""
	}

	// Compare by calendar day in UTC (CASS paths encode YYYY/MM/DD without timezone).
	// Using UTC avoids off-by-one issues when the local timezone is behind/ahead of UTC.
	now := time.Now().UTC()
	ny, nm, nd := now.Date()
	dy, dm, dd := date.Date()
	nowDay := time.Date(ny, nm, nd, 0, 0, 0, 0, time.UTC)
	dateDay := time.Date(dy, dm, dd, 0, 0, 0, 0, time.UTC)

	days := int(nowDay.Sub(dateDay).Hours() / 24)
	if days <= 0 {
		return "today"
	}
	if days == 1 {
		return "yesterday"
	}
	return fmt.Sprintf("%d days ago", days)
}

func cleanContentForMarkdown(content string) string {
	content = strings.TrimSpace(content)
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if len(line) > 120 {
			line = line[:117] + "..."
		}
		lines = append(lines, line)
	}
	if len(lines) > 10 {
		lines = lines[:10]
		lines = append(lines, "...")
	}
	return strings.Join(lines, "\n")
}

func extractCodeSnippets(content string) string {
	codePattern := regexp.MustCompile("(?s)```[a-z]*\n(.*?)```")
	matches := codePattern.FindAllStringSubmatch(content, -1)
	var snippets []string
	for _, match := range matches {
		if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			snippets = append(snippets, strings.TrimSpace(match[1]))
		}
	}
	if len(snippets) > 0 {
		return strings.Join(snippets, "\n\n")
	}
	content = strings.TrimSpace(content)
	if len(content) > 200 {
		content = content[:197] + "..."
	}
	return content
}

func truncateToTokens(content string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(content) <= maxChars {
		return content
	}
	truncated := content[:maxChars]
	lastNewline := strings.LastIndex(truncated, "\n")
	if lastNewline > maxChars/2 {
		truncated = truncated[:lastNewline]
	}
	return truncated + "\n[... truncated for token budget ...]"
}

func countInjectedItems(context string, format InjectionFormat) int {
	switch format {
	case FormatMarkdown:
		return strings.Count(context, "### Session:")
	case FormatStructured:
		count := 0
		for i := 1; i <= 20; i++ {
			if strings.Contains(context, strconv.Itoa(i)+". Session:") {
				count++
			}
		}
		return count
	default:
		if strings.TrimSpace(context) == "" {
			return 0
		}
		return strings.Count(context, "// ---") + 1
	}
}

// ExtractSessionDate attempts to extract a date from a CASS session file path.
func ExtractSessionDate(path string) time.Time {
	re := regexp.MustCompile(`(\d{4})/(\d{2})/(\d{2})`)
	matches := re.FindStringSubmatch(path)
	if len(matches) >= 4 {
		year, _ := strconv.Atoi(matches[1])
		month, _ := strconv.Atoi(matches[2])
		day, _ := strconv.Atoi(matches[3])
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	return time.Time{}
}

func isSameProject(sessionPath, currentWorkspace string) bool {
	if currentWorkspace == "" {
		return false
	}
	parts := strings.Split(filepath.ToSlash(currentWorkspace), "/")
	if len(parts) == 0 {
		return false
	}
	projName := parts[len(parts)-1]
	return strings.Contains(sessionPath, projName)
}

func normalizeScore(score float64) float64 {
	if score > 1.0 {
		return score / 100.0
	}
	return score
}

func sortScoredHits(hits []ScoredHit) {
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].ComputedScore > hits[j].ComputedScore
	})
}

// FilterConfig, FilterResult, ScoredHit structs...
type FilterConfig struct {
	MinRelevance      float64           `json:"min_relevance"`
	MaxItems          int               `json:"max_items"`
	PreferSameProject bool              `json:"prefer_same_project"`
	CurrentWorkspace  string            `json:"current_workspace,omitempty"`
	MaxAgeDays        int               `json:"max_age_days"`
	RecencyBoost      float64           `json:"recency_boost"`
	TopicFilter       TopicFilterConfig `json:"topic_filter"`
	PromptTopics      []Topic           `json:"prompt_topics,omitempty"`
}

type ScoredHit struct {
	CASSHit
	ComputedScore float64         `json:"computed_score"`
	ScoreDetail   CASSScoreDetail `json:"score_detail"`
}

type CASSScoreDetail struct {
	BaseScore       float64 `json:"base_score"`
	RecencyBonus    float64 `json:"recency_bonus"`
	ProjectBonus    float64 `json:"project_bonus"`
	AgePenalty      float64 `json:"age_penalty"`
	TopicMultiplier float64 `json:"topic_multiplier,omitempty"`
	DetectedTopics  []Topic `json:"detected_topics,omitempty"`
}

type FilterResult struct {
	Hits           []ScoredHit `json:"hits"`
	OriginalCount  int         `json:"original_count"`
	FilteredCount  int         `json:"filtered_count"`
	RemovedByScore int         `json:"removed_by_score"`
	RemovedByAge   int         `json:"removed_by_age"`
	RemovedByTopic int         `json:"removed_by_topic"`
	PromptTopics   []Topic     `json:"prompt_topics,omitempty"`
}

func FilterResults(hits []CASSHit, config FilterConfig) FilterResult {
	result := FilterResult{
		Hits:          []ScoredHit{},
		OriginalCount: len(hits),
		PromptTopics:  config.PromptTopics,
	}
	if len(hits) == 0 {
		return result
	}
	now := time.Now()
	maxAgeTime := now.AddDate(0, 0, -config.MaxAgeDays)
	var scored []ScoredHit
	for i, hit := range hits {
		sessionDate := ExtractSessionDate(hit.SourcePath)
		if config.MaxAgeDays > 0 && !sessionDate.IsZero() && sessionDate.Before(maxAgeTime) {
			result.RemovedByAge++
			continue
		}
		breakdown := CASSScoreDetail{TopicMultiplier: 1.0}
		if len(hits) > 1 {
			breakdown.BaseScore = 1.0 - (float64(i) * 0.5 / float64(len(hits)-1))
		} else {
			breakdown.BaseScore = 1.0
		}
		if hit.Score > 0 {
			breakdown.BaseScore = normalizeScore(hit.Score)
		}
		if !sessionDate.IsZero() {
			age := now.Sub(sessionDate)
			maxAge := time.Duration(config.MaxAgeDays) * 24 * time.Hour
			if maxAge > 0 && age < maxAge {
				recencyFactor := 1.0 - (float64(age) / float64(maxAge))
				breakdown.RecencyBonus = recencyFactor * config.RecencyBoost
			}
		}
		if config.PreferSameProject && config.CurrentWorkspace != "" {
			if isSameProject(hit.SourcePath, config.CurrentWorkspace) {
				breakdown.ProjectBonus = 0.15
			}
		}
		finalScore := breakdown.BaseScore + breakdown.RecencyBonus + breakdown.ProjectBonus - breakdown.AgePenalty
		finalScore *= breakdown.TopicMultiplier
		if finalScore > 1.0 {
			finalScore = 1.0
		}
		if finalScore < 0 {
			finalScore = 0
		}
		if finalScore < config.MinRelevance {
			result.RemovedByScore++
			continue
		}
		scored = append(scored, ScoredHit{
			CASSHit:       hit,
			ComputedScore: finalScore,
			ScoreDetail:   breakdown,
		})
	}
	sortScoredHits(scored)
	if config.MaxItems > 0 && len(scored) > config.MaxItems {
		scored = scored[:config.MaxItems]
	}
	result.Hits = scored
	result.FilteredCount = len(scored)
	return result
}

func QueryAndFilterCASS(prompt string, queryConfig CASSConfig, filterConfig FilterConfig) (CASSQueryResult, FilterResult) {
	queryResult := QueryCASS(prompt, queryConfig)
	if !queryResult.Success || len(queryResult.Hits) == 0 {
		return queryResult, FilterResult{OriginalCount: 0}
	}
	filterResult := FilterResults(queryResult.Hits, filterConfig)
	return queryResult, filterResult
}

func InjectContextFromQuery(prompt string, queryConfig CASSConfig, filterConfig FilterConfig, injectConfig InjectConfig) (InjectionResult, CASSQueryResult, FilterResult) {
	queryResult, filterResult := QueryAndFilterCASS(prompt, queryConfig, filterConfig)
	if !queryResult.Success {
		return InjectionResult{
			Success: false,
			Error:   "CASS query failed: " + queryResult.Error,
			Metadata: InjectionMetadata{
				Enabled: true,
			},
		}, queryResult, filterResult
	}
	injectResult := InjectContext(prompt, filterResult.Hits, injectConfig)
	injectResult.Metadata.ItemsFound = filterResult.OriginalCount
	injectResult.Metadata.ItemsFiltered = filterResult.RemovedByScore + filterResult.RemovedByAge
	return injectResult, queryResult, filterResult
}

func FormatForAgent(agentType string) InjectionFormat {
	switch strings.ToLower(agentType) {
	case "codex", "cod":
		return FormatMinimal
	case "gemini", "gmi":
		return FormatStructured
	default:
		return FormatMarkdown
	}
}

// Topic represents a category of code/task.
type Topic string

const (
	TopicAuth     Topic = "auth"
	TopicUI       Topic = "ui"
	TopicAPI      Topic = "api"
	TopicDatabase Topic = "database"
	TopicTesting  Topic = "testing"
	TopicInfra    Topic = "infrastructure"
	TopicDocs     Topic = "docs"
	TopicGeneral  Topic = "general"
)

// topicKeywords maps topics to their indicator keywords.
var topicKeywords = map[Topic][]string{
	TopicAuth: {
		"auth", "login", "logout", "password", "jwt", "token", "session",
		"oauth", "sso", "credential", "authenticate", "authorization",
		"permission", "role", "user", "signup", "signin", "register",
	},
	TopicUI: {
		"ui", "component", "css", "style", "button", "form", "input",
		"modal", "dialog", "layout", "responsive", "theme", "design",
		"frontend", "html", "render", "display", "view", "template",
		"animation", "transition", "hover", "click",
	},
	TopicAPI: {
		"endpoint", "route", "request", "response", "rest", "graphql",
		"http", "api", "handler", "controller", "middleware", "cors",
		"json", "payload", "body", "header", "status", "method",
	},
	TopicDatabase: {
		"query", "schema", "migration", "model", "database", "sql",
		"table", "column", "index", "foreign", "primary", "key",
		"join", "select", "insert", "update", "delete", "transaction",
		"postgres", "mysql", "sqlite", "mongo", "redis", "orm",
	},
	TopicTesting: {
		"test", "spec", "mock", "fixture", "assert", "expect", "describe",
		"unit", "integration", "e2e", "coverage", "benchmark", "stub",
		"fake", "spy", "suite", "runner", "jest", "pytest", "gotest",
	},
	TopicInfra: {
		"deploy", "docker", "kubernetes", "k8s", "ci", "cd", "pipeline",
		"terraform", "aws", "gcp", "azure", "cloud", "server", "container",
		"helm", "ansible", "nginx", "loadbalancer", "ssl", "dns",
	},
	TopicDocs: {
		"readme", "documentation", "docs", "comment", "docstring", "jsdoc",
		"godoc", "markdown", "wiki", "changelog", "guide", "tutorial",
	},
}

// TopicFilterConfig holds configuration for topic-based filtering.
type TopicFilterConfig struct {
	Enabled               bool    `json:"enabled"`
	MatchTopics           bool    `json:"match_topics"`
	ExcludeTopics         []Topic `json:"exclude_topics,omitempty"`
	SameTopicBoost        float64 `json:"same_topic_boost"`
	DifferentTopicPenalty float64 `json:"different_topic_penalty"`
}

// DefaultTopicFilterConfig returns sensible defaults for topic filtering.
func DefaultTopicFilterConfig() TopicFilterConfig {
	return TopicFilterConfig{
		Enabled:               false,
		MatchTopics:           true,
		ExcludeTopics:         nil,
		SameTopicBoost:        1.5,
		DifferentTopicPenalty: 0.5,
	}
}

// DetectTopics analyzes text and returns detected topics.
func DetectTopics(text string) []Topic {
	text = strings.ToLower(text)
	text = removeCodeBlocks(text)
	words := tokenize(text)
	wordSet := make(map[string]bool)
	for _, w := range words {
		wordSet[w] = true
	}

	topicScores := make(map[Topic]int)
	for topic, keywords := range topicKeywords {
		for _, kw := range keywords {
			if wordSet[kw] {
				topicScores[topic]++
			}
		}
	}

	var detected []Topic
	for topic, score := range topicScores {
		if score >= 2 {
			detected = append(detected, topic)
		}
	}

	if len(detected) == 0 {
		for topic, score := range topicScores {
			if score >= 1 {
				detected = append(detected, topic)
			}
		}
	}

	if len(detected) == 0 {
		detected = []Topic{TopicGeneral}
	}

	return detected
}

func topicsOverlap(a, b []Topic) bool {
	setA := make(map[Topic]bool)
	for _, t := range a {
		setA[t] = true
	}
	for _, t := range b {
		if setA[t] {
			return true
		}
	}
	return false
}

func containsTopic(topics []Topic, target Topic) bool {
	for _, t := range topics {
		if t == target {
			return true
		}
	}
	return false
}
