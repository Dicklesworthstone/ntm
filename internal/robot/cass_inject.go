// Package robot provides machine-readable output for AI agents.
// cass_inject.go provides CASS (Cross-Agent Search) query functionality
// for injecting relevant historical context into agent prompts.
package robot

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// CASSConfig holds configuration for CASS queries.
type CASSConfig struct {
	// Enabled controls whether CASS queries are performed.
	Enabled bool `json:"enabled"`

	// MaxResults limits the number of CASS hits to return.
	MaxResults int `json:"max_results"`

	// MaxAgeDays filters results to those within this many days.
	MaxAgeDays int `json:"max_age_days"`

	// MinRelevance is the minimum relevance score (0.0-1.0) to include results.
	// Note: CASS doesn't currently return relevance scores, so this is for future use.
	MinRelevance float64 `json:"min_relevance"`

	// PreferSameProject gives preference to results from the current workspace.
	PreferSameProject bool `json:"prefer_same_project"`

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
	// SourcePath is the path to the session file.
	SourcePath string `json:"source_path"`

	// LineNumber is the line in the session file.
	LineNumber int `json:"line_number"`

	// Agent is the agent type (e.g., "claude", "codex", "gemini").
	Agent string `json:"agent"`

	// Content is the matched content snippet (if available).
	Content string `json:"content,omitempty"`

	// Score is the relevance score (if available from CASS).
	Score float64 `json:"score,omitempty"`
}

// CASSQueryResult holds the results of a CASS query.
type CASSQueryResult struct {
	// Success indicates whether the query completed successfully.
	Success bool `json:"success"`

	// Query is the search query that was executed.
	Query string `json:"query"`

	// Hits contains the matching results.
	Hits []CASSHit `json:"hits"`

	// TotalMatches is the total number of matches (may be > len(Hits) if limited).
	TotalMatches int `json:"total_matches"`

	// QueryTime is how long the query took.
	QueryTime time.Duration `json:"query_time_ms"`

	// Error contains any error message.
	Error string `json:"error,omitempty"`

	// Keywords are the extracted keywords from the original prompt.
	Keywords []string `json:"keywords,omitempty"`
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
// It extracts keywords from the prompt and searches for relevant past sessions.
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

	// Extract keywords from the prompt
	keywords := ExtractKeywords(prompt)
	result.Keywords = keywords

	if len(keywords) == 0 {
		result.Success = true
		result.Error = "no keywords extracted from prompt"
		return result
	}

	// Build the search query
	query := strings.Join(keywords, " ")
	result.Query = query

	// Check if CASS is available
	binPath := config.BinaryPath
	if binPath == "" {
		binPath = "cass"
	}

	if !isCASSAvailable(binPath) {
		result.Error = "cass command not found"
		return result
	}

	// Build the cass search command
	args := []string{"search", query, "--json"}

	// Add limit
	if config.MaxResults > 0 {
		args = append(args, "--limit", itoa(config.MaxResults))
	}

	// Add age filter
	if config.MaxAgeDays > 0 {
		args = append(args, "--days", itoa(config.MaxAgeDays))
	}

	// Add agent filter
	for _, agent := range config.AgentFilter {
		args = append(args, "--agent", agent)
	}

	// Execute CASS search
	cmd := exec.Command(binPath, args...)
	output, err := cmd.Output()
	result.QueryTime = time.Since(start)

	if err != nil {
		// Check if it's just no results vs actual error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Exit code 1 often means no results, which is fine
			result.Success = true
			return result
		}
		result.Error = err.Error()
		return result
	}

	// Parse the response
	var resp cassSearchResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		result.Error = "failed to parse CASS response: " + err.Error()
		return result
	}

	// Convert to our format
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

// ExtractKeywords extracts meaningful keywords from a prompt for CASS search.
// It filters out common stop words and short words, focusing on technical terms.
func ExtractKeywords(prompt string) []string {
	// Convert to lowercase for processing
	text := strings.ToLower(prompt)

	// Remove code blocks to avoid searching for code syntax
	text = removeCodeBlocks(text)

	// Tokenize: split on non-alphanumeric characters
	words := tokenize(text)

	// Filter words
	var keywords []string
	seen := make(map[string]bool)

	for _, word := range words {
		// Skip short words
		if len(word) < 3 {
			continue
		}

		// Skip stop words
		if isStopWord(word) {
			continue
		}

		// Skip if already seen (deduplicate)
		if seen[word] {
			continue
		}
		seen[word] = true

		keywords = append(keywords, word)
	}

	// Limit to most relevant keywords (first 10)
	if len(keywords) > 10 {
		keywords = keywords[:10]
	}

	return keywords
}

// tokenize splits text into words, handling code identifiers like snake_case.
func tokenize(text string) []string {
	// Split on whitespace and punctuation, but keep underscores in identifiers
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

// removeCodeBlocks removes markdown code blocks from text.
func removeCodeBlocks(text string) string {
	// Remove fenced code blocks
	re := regexp.MustCompile("(?s)```.*?```")
	text = re.ReplaceAllString(text, " ")

	// Remove inline code
	re = regexp.MustCompile("`[^`]+`")
	text = re.ReplaceAllString(text, " ")

	return text
}

// isStopWord returns true if the word is a common stop word.
func isStopWord(word string) bool {
	stopWords := map[string]bool{
		// Common English stop words
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

		// Common coding task words (too generic to search)
		"code": true, "file": true, "function": true, "method": true,
		"class": true, "variable": true, "add": true, "create": true,
		"update": true, "delete": true, "remove": true, "change": true,
		"fix": true, "bug": true, "error": true, "test": true, "write": true,
		"read": true, "run": true, "start": true, "stop": true,
	}

	return stopWords[word]
}

// isCASSAvailable checks if the cass command is available.
func isCASSAvailable(binPath string) bool {
	_, err := exec.LookPath(binPath)
	return err == nil
}

// itoa converts int to string (simple helper to avoid strconv import for small use).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var result []byte
	neg := i < 0
	if neg {
		i = -i
	}

	for i > 0 {
		result = append([]byte{byte('0' + i%10)}, result...)
		i /= 10
	}

	if neg {
		result = append([]byte{'-'}, result...)
	}

	return string(result)
}

// =============================================================================
// Topic Detection & Filtering
// =============================================================================

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
	// Enabled controls whether topic filtering is applied.
	Enabled bool `json:"enabled"`

	// MatchTopics requires results to match at least one detected topic.
	// If true, results with no topic overlap are penalized.
	MatchTopics bool `json:"match_topics"`

	// ExcludeTopics lists topics to completely filter out.
	ExcludeTopics []Topic `json:"exclude_topics,omitempty"`

	// SameTopicBoost is the multiplier for results matching prompt topics.
	// Default: 1.5
	SameTopicBoost float64 `json:"same_topic_boost"`

	// DifferentTopicPenalty is the multiplier for results not matching prompt topics.
	// Default: 0.5
	DifferentTopicPenalty float64 `json:"different_topic_penalty"`
}

// DefaultTopicFilterConfig returns sensible defaults for topic filtering.
func DefaultTopicFilterConfig() TopicFilterConfig {
	return TopicFilterConfig{
		Enabled:               false, // Off by default until stable
		MatchTopics:           true,
		ExcludeTopics:         nil,
		SameTopicBoost:        1.5,
		DifferentTopicPenalty: 0.5,
	}
}

// DetectTopics analyzes text and returns detected topics.
func DetectTopics(text string) []Topic {
	text = strings.ToLower(text)

	// Remove code blocks to avoid false positives from code syntax
	text = removeCodeBlocks(text)

	// Tokenize
	words := tokenize(text)
	wordSet := make(map[string]bool)
	for _, w := range words {
		wordSet[w] = true
	}

	// Count keyword matches per topic
	topicScores := make(map[Topic]int)
	for topic, keywords := range topicKeywords {
		for _, kw := range keywords {
			if wordSet[kw] {
				topicScores[topic]++
			}
		}
	}

	// Collect topics with at least 2 keyword matches (avoids false positives)
	var detected []Topic
	for topic, score := range topicScores {
		if score >= 2 {
			detected = append(detected, topic)
		}
	}

	// If no strong matches, check for single keyword matches
	if len(detected) == 0 {
		for topic, score := range topicScores {
			if score >= 1 {
				detected = append(detected, topic)
			}
		}
	}

	// If still nothing, return general
	if len(detected) == 0 {
		detected = []Topic{TopicGeneral}
	}

	return detected
}

// topicsOverlap checks if two topic slices have any common elements.
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

// containsTopic checks if a topic is in the slice.
func containsTopic(topics []Topic, target Topic) bool {
	for _, t := range topics {
		if t == target {
			return true
		}
	}
	return false
}

// =============================================================================
// Relevance Filtering
// =============================================================================

// FilterConfig holds configuration for relevance filtering.
type FilterConfig struct {
	// MinRelevance is the minimum relevance score (0.0-1.0) to include results.
	// Default: 0.7
	MinRelevance float64 `json:"min_relevance"`

	// MaxItems is the maximum number of items to return after filtering.
	MaxItems int `json:"max_items"`

	// PreferSameProject boosts scores for results from the same workspace.
	PreferSameProject bool `json:"prefer_same_project"`

	// CurrentWorkspace is the current project path for same-project preference.
	CurrentWorkspace string `json:"current_workspace,omitempty"`

	// MaxAgeDays filters out results older than this many days.
	// 0 means no age limit.
	MaxAgeDays int `json:"max_age_days"`

	// RecencyBoost is the weight given to recency (0.0-1.0).
	// Higher values favor newer results more strongly.
	// Default: 0.3
	RecencyBoost float64 `json:"recency_boost"`

	// TopicFilter configures topic-based filtering.
	TopicFilter TopicFilterConfig `json:"topic_filter"`

	// PromptTopics are the topics detected from the prompt (set by caller).
	// If empty and TopicFilter.Enabled, topics will be detected from hits.
	PromptTopics []Topic `json:"prompt_topics,omitempty"`
}

// DefaultFilterConfig returns sensible defaults for relevance filtering.
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		MinRelevance:      0.7,
		MaxItems:          5,
		PreferSameProject: true,
		MaxAgeDays:        30,
		RecencyBoost:      0.3,
	}
}

// ScoredHit wraps a CASSHit with a computed relevance score.
type ScoredHit struct {
	CASSHit
	// ComputedScore is the relevance score computed by filtering (0.0-1.0).
	ComputedScore float64 `json:"computed_score"`
	// ScoreDetail explains how the score was computed.
	ScoreDetail CASSScoreDetail `json:"score_detail"`
}

// CASSScoreDetail shows the components of a CASS relevance score.
type CASSScoreDetail struct {
	// BaseScore is the initial score from search result position.
	BaseScore float64 `json:"base_score"`
	// RecencyBonus is added for newer results.
	RecencyBonus float64 `json:"recency_bonus"`
	// ProjectBonus is added for same-project matches.
	ProjectBonus float64 `json:"project_bonus"`
	// AgePenalty is subtracted for older results.
	AgePenalty float64 `json:"age_penalty"`
	// TopicMultiplier is applied for topic match/mismatch (1.0 = neutral).
	TopicMultiplier float64 `json:"topic_multiplier,omitempty"`
	// DetectedTopics are the topics detected in this result.
	DetectedTopics []Topic `json:"detected_topics,omitempty"`
}

// FilterResult holds the results of filtering CASS hits.
type FilterResult struct {
	// Hits are the filtered and scored results.
	Hits []ScoredHit `json:"hits"`
	// OriginalCount is how many results were received before filtering.
	OriginalCount int `json:"original_count"`
	// FilteredCount is how many results passed the filter.
	FilteredCount int `json:"filtered_count"`
	// RemovedByScore is how many were removed for low relevance.
	RemovedByScore int `json:"removed_by_score"`
	// RemovedByAge is how many were removed for being too old.
	RemovedByAge int `json:"removed_by_age"`
	// RemovedByTopic is how many were removed for excluded topics.
	RemovedByTopic int `json:"removed_by_topic"`
	// PromptTopics are the topics detected in the prompt.
	PromptTopics []Topic `json:"prompt_topics,omitempty"`
}

// FilterResults filters and scores CASS hits based on relevance criteria.
// It applies recency preference, same-project preference, topic filtering,
// and minimum score thresholds.
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

	// Topic filtering setup
	topicEnabled := config.TopicFilter.Enabled
	promptTopics := config.PromptTopics

	// Score and filter each hit
	var scored []ScoredHit
	for i, hit := range hits {
		// Extract date from source path to compute age
		sessionDate := extractSessionDate(hit.SourcePath)

		// Apply age filter if configured
		if config.MaxAgeDays > 0 && !sessionDate.IsZero() && sessionDate.Before(maxAgeTime) {
			result.RemovedByAge++
			continue
		}

		// Compute score components
		breakdown := CASSScoreDetail{
			TopicMultiplier: 1.0, // Neutral default
		}

		// Detect topics from hit content if topic filtering is enabled
		var hitTopics []Topic
		excludedByTopic := false
		if topicEnabled && hit.Content != "" {
			hitTopics = DetectTopics(hit.Content)
			breakdown.DetectedTopics = hitTopics

			// Check for excluded topics
			for _, excluded := range config.TopicFilter.ExcludeTopics {
				if containsTopic(hitTopics, excluded) {
					result.RemovedByTopic++
					excludedByTopic = true
					break
				}
			}
			if excludedByTopic {
				continue
			}

			// Apply topic matching
			if config.TopicFilter.MatchTopics && len(promptTopics) > 0 {
				if topicsOverlap(promptTopics, hitTopics) {
					// Boost for matching topics
					breakdown.TopicMultiplier = config.TopicFilter.SameTopicBoost
				} else if !containsTopic(hitTopics, TopicGeneral) && !containsTopic(promptTopics, TopicGeneral) {
					// Penalize for non-matching topics (unless either is general)
					breakdown.TopicMultiplier = config.TopicFilter.DifferentTopicPenalty
				}
			}
		}

		// Base score from position (earlier = higher, normalized 0.5-1.0)
		// Position 0 gets 1.0, position N-1 gets 0.5
		if len(hits) > 1 {
			breakdown.BaseScore = 1.0 - (float64(i) * 0.5 / float64(len(hits)-1))
		} else {
			breakdown.BaseScore = 1.0
		}

		// If CASS returned a score, use it as base instead
		if hit.Score > 0 {
			breakdown.BaseScore = normalizeScore(hit.Score)
		}

		// Recency bonus (newer = higher)
		if !sessionDate.IsZero() {
			age := now.Sub(sessionDate)
			maxAge := time.Duration(config.MaxAgeDays) * 24 * time.Hour
			if maxAge > 0 && age < maxAge {
				// Recency bonus: 1.0 for today, 0.0 for max age
				recencyFactor := 1.0 - (float64(age) / float64(maxAge))
				breakdown.RecencyBonus = recencyFactor * config.RecencyBoost
			}
		}

		// Same-project bonus
		if config.PreferSameProject && config.CurrentWorkspace != "" {
			if isSameProject(hit.SourcePath, config.CurrentWorkspace) {
				breakdown.ProjectBonus = 0.15 // 15% bonus for same project
			}
		}

		// Compute final score
		// Start with base + bonuses - penalties
		finalScore := breakdown.BaseScore + breakdown.RecencyBonus + breakdown.ProjectBonus - breakdown.AgePenalty
		// Apply topic multiplier
		finalScore *= breakdown.TopicMultiplier
		// Cap at 1.0
		if finalScore > 1.0 {
			finalScore = 1.0
		}
		if finalScore < 0 {
			finalScore = 0
		}

		// Apply minimum relevance threshold
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

	// Sort by computed score (highest first)
	sortScoredHits(scored)

	// Apply max items limit
	if config.MaxItems > 0 && len(scored) > config.MaxItems {
		scored = scored[:config.MaxItems]
	}

	result.Hits = scored
	result.FilteredCount = len(scored)

	return result
}

// extractSessionDate attempts to extract a date from a CASS session file path.
// Session paths typically contain dates like: .../2025/12/05/session-....jsonl
func extractSessionDate(path string) time.Time {
	// Look for date patterns in the path
	// Common formats: /2025/12/05/ or /2025-12-05/ or session-2025-12-05
	datePatterns := []string{
		`/(\d{4})/(\d{2})/(\d{2})/`,       // /2025/12/05/
		`/(\d{4})-(\d{2})-(\d{2})/`,       // /2025-12-05/
		`session-(\d{4})-(\d{2})-(\d{2})`, // session-2025-12-05
		`(\d{4})-(\d{2})-(\d{2})T`,        // 2025-12-05T (ISO timestamp in filename)
	}

	for _, pattern := range datePatterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(path)
		if len(matches) >= 4 {
			year := parseIntOrZero(matches[1])
			month := parseIntOrZero(matches[2])
			day := parseIntOrZero(matches[3])
			if year > 0 && month >= 1 && month <= 12 && day >= 1 && day <= 31 {
				return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
			}
		}
	}

	return time.Time{} // Zero time if no date found
}

// parseIntOrZero parses a string as int, returning 0 on error.
func parseIntOrZero(s string) int {
	result := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		}
	}
	return result
}

// isSameProject checks if a session path is from the same project as the current workspace.
func isSameProject(sessionPath, currentWorkspace string) bool {
	// Normalize paths for comparison
	sessionPath = strings.ToLower(sessionPath)
	currentWorkspace = strings.ToLower(currentWorkspace)

	// Check if the session path contains the workspace directory name
	// This is a heuristic - session paths often include the project name
	if currentWorkspace == "" {
		return false
	}

	// Extract the last component of the workspace path (project name)
	parts := strings.Split(currentWorkspace, "/")
	if len(parts) > 0 {
		projectName := parts[len(parts)-1]
		if projectName != "" && strings.Contains(sessionPath, projectName) {
			return true
		}
	}

	return false
}

// normalizeScore normalizes a CASS score to 0.0-1.0 range.
// CASS scores can vary in range depending on the search algorithm.
func normalizeScore(score float64) float64 {
	// Assume CASS returns scores in 0-100 or 0-1 range
	if score > 1.0 {
		// Likely 0-100 scale, normalize
		return score / 100.0
	}
	return score
}

// sortScoredHits sorts hits by computed score in descending order (highest first).
func sortScoredHits(hits []ScoredHit) {
	// Simple insertion sort for small slices (typically <20 items)
	for i := 1; i < len(hits); i++ {
		key := hits[i]
		j := i - 1
		for j >= 0 && hits[j].ComputedScore < key.ComputedScore {
			hits[j+1] = hits[j]
			j--
		}
		hits[j+1] = key
	}
}

// QueryAndFilterCASS combines QueryCASS and FilterResults in one call.
// This is a convenience function for the common use case.
func QueryAndFilterCASS(prompt string, queryConfig CASSConfig, filterConfig FilterConfig) (CASSQueryResult, FilterResult) {
	queryResult := QueryCASS(prompt, queryConfig)

	if !queryResult.Success || len(queryResult.Hits) == 0 {
		return queryResult, FilterResult{OriginalCount: 0}
	}

	filterResult := FilterResults(queryResult.Hits, filterConfig)
	return queryResult, filterResult
}

// =============================================================================
// Context Injection
// =============================================================================

// InjectionFormat defines how context should be formatted for different agents.
type InjectionFormat string

const (
	// FormatMarkdown uses headers, bullets, and sections (best for Claude).
	FormatMarkdown InjectionFormat = "markdown"
	// FormatMinimal uses minimal prose, code-focused (best for Codex).
	FormatMinimal InjectionFormat = "minimal"
	// FormatStructured uses numbered lists and clear sections (best for Gemini).
	FormatStructured InjectionFormat = "structured"
)

// InjectConfig holds configuration for context injection.
type InjectConfig struct {
	// Format specifies how to format the injected context.
	// Default: FormatMarkdown
	Format InjectionFormat `json:"format"`

	// MaxTokens is the maximum number of tokens to use for injection.
	// Default: 500
	MaxTokens int `json:"max_tokens"`

	// SkipThreshold is the context usage percentage above which injection is skipped.
	// If current context is above this percentage, injection is skipped to preserve
	// space for the response. Default: 60 (60%)
	SkipThreshold int `json:"skip_threshold"`

	// CurrentContextPct is the current context window usage percentage (0-100).
	// Used to determine whether to skip injection.
	CurrentContextPct int `json:"current_context_pct,omitempty"`

	// IncludeMetadata includes injection metadata in the response.
	// Default: true
	IncludeMetadata bool `json:"include_metadata"`

	// DryRun shows what would be injected without actually modifying the prompt.
	// Default: false
	DryRun bool `json:"dry_run"`
}

// DefaultInjectConfig returns sensible defaults for context injection.
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
	// Enabled indicates whether injection was attempted.
	Enabled bool `json:"enabled"`
	// ItemsFound is how many CASS hits were found.
	ItemsFound int `json:"items_found"`
	// ItemsInjected is how many items were actually injected.
	ItemsInjected int `json:"items_injected"`
	// ItemsFiltered is how many were filtered out by relevance.
	ItemsFiltered int `json:"items_filtered"`
	// TokensAdded is the estimated token count of injected content.
	TokensAdded int `json:"tokens_added"`
	// FormatUsed is the format that was applied.
	FormatUsed InjectionFormat `json:"format_used"`
	// SkippedReason explains why injection was skipped, if applicable.
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// InjectionResult holds the result of context injection.
type InjectionResult struct {
	// Success indicates whether injection succeeded.
	Success bool `json:"success"`
	// ModifiedPrompt is the prompt with injected context prepended.
	ModifiedPrompt string `json:"modified_prompt"`
	// InjectedContext is the context that was (or would be) injected.
	InjectedContext string `json:"injected_context"`
	// Metadata contains details about the injection.
	Metadata InjectionMetadata `json:"metadata"`
	// Error contains any error message.
	Error string `json:"error,omitempty"`
}

// InjectContext prepends relevant CASS context to a prompt.
// It takes filtered CASS results and formats them appropriately for the agent type.
func InjectContext(prompt string, hits []ScoredHit, config InjectConfig) InjectionResult {
	result := InjectionResult{
		Success: false,
		Metadata: InjectionMetadata{
			Enabled:    true,
			ItemsFound: len(hits),
			FormatUsed: config.Format,
		},
	}

	// Check if injection should be skipped due to context usage
	if config.CurrentContextPct > 0 && config.CurrentContextPct >= config.SkipThreshold {
		result.Success = true
		result.ModifiedPrompt = prompt
		result.Metadata.SkippedReason = "context at " + itoa(config.CurrentContextPct) + "% (threshold: " + itoa(config.SkipThreshold) + "%)"
		return result
	}

	// If no hits, return original prompt
	if len(hits) == 0 {
		result.Success = true
		result.ModifiedPrompt = prompt
		result.Metadata.SkippedReason = "no relevant context found"
		return result
	}

	// Format the context based on agent type
	context := FormatContext(hits, config)

	// Estimate tokens (rough: ~4 chars per token)
	estimatedTokens := len(context) / 4
	result.Metadata.TokensAdded = estimatedTokens

	// Truncate if over budget
	if config.MaxTokens > 0 && estimatedTokens > config.MaxTokens {
		context = truncateToTokens(context, config.MaxTokens)
		result.Metadata.TokensAdded = config.MaxTokens
	}

	result.InjectedContext = context
	result.Metadata.ItemsInjected = countInjectedItems(context, config.Format)
	result.Metadata.ItemsFiltered = len(hits) - result.Metadata.ItemsInjected

	// Build the modified prompt
	if config.DryRun {
		result.Success = true
		result.ModifiedPrompt = prompt // Don't modify in dry run
		return result
	}

	result.Success = true
	result.ModifiedPrompt = context + "\n---\n\n" + prompt

	return result
}

// FormatContext formats CASS hits for injection based on the specified format.
func FormatContext(hits []ScoredHit, config InjectConfig) string {
	if len(hits) == 0 {
		return ""
	}

	switch config.Format {
	case FormatMinimal:
		return formatMinimal(hits)
	case FormatStructured:
		return formatStructured(hits)
	default: // FormatMarkdown
		return formatMarkdown(hits)
	}
}

// formatMarkdown formats context with headers, bullets, and sections.
// Best for Claude and similar markdown-friendly models.
func formatMarkdown(hits []ScoredHit) string {
	var b strings.Builder

	b.WriteString("## Relevant Context from Past Sessions\n\n")

	for i, hit := range hits {
		// Extract session info from path
		sessionName := extractSessionName(hit.SourcePath)
		age := formatAge(hit.SourcePath)
		relevance := int(hit.ComputedScore * 100)

		b.WriteString("### Session: ")
		b.WriteString(sessionName)
		b.WriteString(" (")
		b.WriteString(itoa(relevance))
		b.WriteString("% match")
		if age != "" {
			b.WriteString(", ")
			b.WriteString(age)
		}
		b.WriteString(")\n\n")

		if hit.Content != "" {
			// Clean up content for markdown
			content := cleanContentForMarkdown(hit.Content)
			b.WriteString(content)
			b.WriteString("\n")
		}

		if i < len(hits)-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// formatMinimal formats context with minimal prose, code-focused.
// Best for Codex and code-completion models.
func formatMinimal(hits []ScoredHit) string {
	var b strings.Builder

	b.WriteString("// Related context:\n")

	itemsWritten := 0
	for _, hit := range hits {
		if hit.Content != "" {
			// Extract just code snippets if present
			content := extractCodeSnippets(hit.Content)
			if content != "" {
				// Add a separator marker between items (used by countInjectedItems)
				if itemsWritten > 0 {
					b.WriteString("\n// ---\n")
				}
				b.WriteString(content)
				b.WriteString("\n")
				itemsWritten++
			}
		}
	}

	return b.String()
}

// formatStructured formats context with numbered lists and clear sections.
// Best for Gemini and structured-output models.
func formatStructured(hits []ScoredHit) string {
	var b strings.Builder

	b.WriteString("RELEVANT CONTEXT FROM PAST SESSIONS\n")
	b.WriteString("====================================\n\n")

	for i, hit := range hits {
		sessionName := extractSessionName(hit.SourcePath)
		age := formatAge(hit.SourcePath)
		relevance := int(hit.ComputedScore * 100)

		b.WriteString(itoa(i + 1))
		b.WriteString(". Session: ")
		b.WriteString(sessionName)
		b.WriteString("\n")
		b.WriteString("   Relevance: ")
		b.WriteString(itoa(relevance))
		b.WriteString("%\n")
		if age != "" {
			b.WriteString("   Age: ")
			b.WriteString(age)
			b.WriteString("\n")
		}

		if hit.Content != "" {
			b.WriteString("   Content:\n")
			// Clean and indent content lines (limit to 10 lines, 120 chars per line)
			content := cleanContentForMarkdown(hit.Content)
			lines := strings.Split(content, "\n")
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					b.WriteString("   | ")
					b.WriteString(line)
					b.WriteString("\n")
				}
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// extractSessionName extracts a readable session name from the file path.
func extractSessionName(path string) string {
	if path == "" {
		return ""
	}

	// Get the filename without extension
	parts := strings.Split(path, "/")
	filename := parts[len(parts)-1]

	// Handle empty filename (e.g., path ending with /)
	if filename == "" {
		return ""
	}

	// Remove common extensions
	filename = strings.TrimSuffix(filename, ".jsonl")
	filename = strings.TrimSuffix(filename, ".json")

	// Truncate if too long
	if len(filename) > 40 {
		filename = filename[:37] + "..."
	}

	return filename
}

// formatAge returns a human-readable age string based on the session date.
func formatAge(path string) string {
	sessionDate := extractSessionDate(path)
	if sessionDate.IsZero() {
		return ""
	}

	age := time.Since(sessionDate)

	days := int(age.Hours() / 24)
	if days == 0 {
		return "today"
	} else if days == 1 {
		return "1 day ago"
	} else if days < 7 {
		return itoa(days) + " days ago"
	} else if days < 30 {
		weeks := days / 7
		if weeks == 1 {
			return "1 week ago"
		}
		return itoa(weeks) + " weeks ago"
	}

	months := days / 30
	if months == 1 {
		return "1 month ago"
	}
	return itoa(months) + " months ago"
}

// cleanContentForMarkdown cleans content for markdown formatting.
func cleanContentForMarkdown(content string) string {
	// Trim whitespace
	content = strings.TrimSpace(content)

	// Limit line length for readability
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if len(line) > 120 {
			line = line[:117] + "..."
		}
		lines = append(lines, line)
	}

	// Limit total lines
	if len(lines) > 10 {
		lines = lines[:10]
		lines = append(lines, "...")
	}

	return strings.Join(lines, "\n")
}

// extractCodeSnippets extracts just code blocks from content.
func extractCodeSnippets(content string) string {
	// Look for fenced code blocks
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

	// If no code blocks, return cleaned content (might be inline code)
	content = strings.TrimSpace(content)
	if len(content) > 200 {
		content = content[:197] + "..."
	}
	return content
}

// truncateToTokens truncates content to approximately the given token count.
// Uses ~4 chars per token as a rough estimate.
func truncateToTokens(content string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(content) <= maxChars {
		return content
	}

	// Try to truncate at a line boundary
	truncated := content[:maxChars]
	lastNewline := strings.LastIndex(truncated, "\n")
	if lastNewline > maxChars/2 {
		truncated = truncated[:lastNewline]
	}

	return truncated + "\n[... truncated for token budget ...]"
}

// countInjectedItems counts how many items were included in the formatted context.
func countInjectedItems(context string, format InjectionFormat) int {
	switch format {
	case FormatMarkdown:
		// Count ### headers
		return strings.Count(context, "### Session:")
	case FormatStructured:
		// Count numbered items (1. Session:, 2. Session:, etc.)
		count := 0
		for i := 1; i <= 20; i++ {
			if strings.Contains(context, itoa(i)+". Session:") {
				count++
			}
		}
		return count
	default:
		// FormatMinimal uses "// ---" separators between items
		// Count separators + 1 (if there's content) to get item count
		if strings.TrimSpace(context) == "" || context == "// Related context:\n" {
			return 0
		}
		separators := strings.Count(context, "// ---")
		return separators + 1
	}
}

// InjectContextFromQuery is a convenience function that queries CASS, filters results,
// and injects the context in one call.
func InjectContextFromQuery(prompt string, queryConfig CASSConfig, filterConfig FilterConfig, injectConfig InjectConfig) (InjectionResult, CASSQueryResult, FilterResult) {
	// Query and filter
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

	// Inject context
	injectResult := InjectContext(prompt, filterResult.Hits, injectConfig)
	injectResult.Metadata.ItemsFound = filterResult.OriginalCount
	injectResult.Metadata.ItemsFiltered = filterResult.RemovedByScore + filterResult.RemovedByAge

	return injectResult, queryResult, filterResult
}

// FormatForAgent returns the appropriate InjectionFormat for an agent type.
func FormatForAgent(agentType string) InjectionFormat {
	switch strings.ToLower(agentType) {
	case "codex", "cod":
		return FormatMinimal
	case "gemini", "gmi":
		return FormatStructured
	default: // claude, cc, and others
		return FormatMarkdown
	}
}
