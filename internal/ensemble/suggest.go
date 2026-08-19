// Package ensemble provides types and utilities for multi-agent reasoning ensembles.
// suggest.go implements deterministic question→preset matching for ensemble suggestion.
package ensemble

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// SuggestionEngine provides deterministic question→preset matching.
// It analyzes question text and recommends the best ensemble presets.
type SuggestionEngine struct {
	presets     []EnsemblePreset
	keywords    map[string][]string
	compiled    []compiledPreset
	presetIndex map[string]int
	stopWords   map[string]bool
}

type compiledPreset struct {
	keywords         []compiledKeyword
	tagsLower        []string
	descriptionLower string
}

type compiledKeyword struct {
	original string
	lower    string
	tokens   []string
}

// SuggestionScore holds a preset with its match score.
type SuggestionScore struct {
	Preset     *EnsemblePreset `json:"preset"`
	PresetName string          `json:"preset_name"`
	Score      float64         `json:"score"`
	Reasons    []string        `json:"reasons"`
}

// SuggestionResult is the output of Suggest().
type SuggestionResult struct {
	Question      string            `json:"question"`
	Suggestions   []SuggestionScore `json:"suggestions"`
	TopPick       *SuggestionScore  `json:"top_pick,omitempty"`
	NoMatchReason string            `json:"no_match_reason,omitempty"`
}

// NewSuggestionEngine creates a new suggestion engine with embedded ensembles.
func NewSuggestionEngine() *SuggestionEngine {
	stopWords := buildStopWords()
	keywords := buildKeywordMap()
	engine := &SuggestionEngine{
		presets:   EmbeddedEnsembles,
		keywords:  keywords,
		stopWords: stopWords,
	}
	engine.compiled = buildCompiledPresets(engine.presets, keywords, stopWords)
	engine.presetIndex = buildPresetIndex(engine.presets)
	return engine
}

// buildKeywordMap creates keyword mappings for each preset based on their tags,
// description, and semantic analysis.
func buildKeywordMap() map[string][]string {
	return map[string][]string{
		"project-diagnosis": {
			"health", "diagnosis", "analyze", "assessment", "issues", "problems",
			"what's wrong", "check", "review", "audit", "status", "quality",
			"codebase", "project", "state", "condition", "overview",
		},
		"idea-forge": {
			"idea", "feature", "innovation", "brainstorm", "creative", "new",
			"suggest", "propose", "generate", "design", "concept", "next",
			"should we", "could we", "what if", "explore", "possibilities",
			"enhance", "improve", "add", "extend",
		},
		"spec-critique": {
			"spec", "specification", "requirements", "edge case", "edge-case",
			"corner case", "validate", "verify", "critique", "review",
			"ambiguity", "unclear", "missing", "gaps", "coverage", "complete",
			"prd", "design doc", "rfc", "proposal",
		},
		"safety-risk": {
			"security", "risk", "threat", "vulnerability", "attack", "exploit",
			"safety", "danger", "compliance", "audit", "penetration", "injection",
			"xss", "csrf", "auth", "authentication", "authorization", "secrets",
			"encryption", "tls", "ssl", "owasp", "cve", "malicious",
		},
		"architecture-review": {
			"architecture", "design", "structure", "pattern", "system",
			"component", "module", "layer", "coupling", "cohesion", "dependency",
			"scalability", "maintainability", "refactor", "monolith", "microservice",
			"api", "interface", "contract", "boundary",
		},
		"tech-debt-triage": {
			"tech debt", "technical debt", "refactor", "cleanup", "legacy",
			"deprecate", "obsolete", "maintenance", "priority", "triage",
			"backlog", "debt", "cost", "payoff", "investment", "todo",
			"fixme", "hack", "workaround",
		},
		"bug-hunt": {
			"bug", "error", "crash", "fail", "broken", "wrong", "incorrect",
			"unexpected", "regression", "defect", "issue", "problem", "fix",
			"debug", "trace", "reproduce", "stack", "exception", "panic",
		},
		"root-cause-analysis": {
			"root cause", "why", "reason", "understand", "investigate",
			"failure", "incident", "postmortem", "autopsy", "diagnosis",
			"happened", "caused", "origin", "source", "underlying", "deep",
			"5 whys", "five whys", "causal", "chain",
		},
		"strategic-planning": {
			"strategy", "plan", "roadmap", "future", "long-term", "vision",
			"goal", "objective", "milestone", "quarter", "year", "timeline",
			"prioritize", "resource", "allocate", "budget", "tradeoff",
			"decision", "direction", "next steps",
		},
	}
}

// buildStopWords returns common words to filter from scoring.
func buildStopWords() map[string]bool {
	words := []string{
		"the", "a", "an", "is", "are", "was", "were", "be", "been", "being",
		"have", "has", "had", "do", "does", "did", "will", "would", "could",
		"should", "may", "might", "can", "this", "that", "these", "those",
		"i", "you", "he", "she", "it", "we", "they", "my", "your", "his",
		"her", "its", "our", "their", "and", "or", "but", "if", "then",
		"else", "when", "where", "how", "what", "which", "who", "whom",
		"to", "of", "in", "for", "on", "with", "at", "by", "from", "as",
		"about", "into", "through", "during", "before", "after", "above",
		"below", "between", "under", "again", "further", "once", "here",
		"there", "all", "each", "few", "more", "most", "other", "some",
		"such", "no", "not", "only", "same", "so", "than", "too", "very",
		"just", "also", "now", "please", "help", "me", "us", "want", "need",
	}
	stopWords := make(map[string]bool, len(words))
	for _, w := range words {
		stopWords[w] = true
	}
	return stopWords
}

// wordRegex matches word boundaries for tokenization.
var wordRegex = regexp.MustCompile(`[a-zA-Z]+`)

func buildCompiledPresets(presets []EnsemblePreset, keywords map[string][]string, stopWords map[string]bool) []compiledPreset {
	compiled := make([]compiledPreset, len(presets))
	for i := range presets {
		preset := &presets[i]
		compiledKeywords := make([]compiledKeyword, 0, len(keywords[preset.Name]))
		for _, keyword := range keywords[preset.Name] {
			compiledKeywords = append(compiledKeywords, compiledKeyword{
				original: keyword,
				lower:    strings.ToLower(keyword),
				tokens:   tokenizeWithStopWords(keyword, stopWords),
			})
		}

		tagsLower := make([]string, len(preset.Tags))
		for j, tag := range preset.Tags {
			tagsLower[j] = strings.ToLower(tag)
		}

		compiled[i] = compiledPreset{
			keywords:         compiledKeywords,
			tagsLower:        tagsLower,
			descriptionLower: strings.ToLower(preset.Description),
		}
	}
	return compiled
}

func buildPresetIndex(presets []EnsemblePreset) map[string]int {
	index := make(map[string]int, len(presets))
	for i := range presets {
		index[presets[i].Name] = i
	}
	return index
}

// Suggest analyzes a question and returns ranked preset suggestions.
func (e *SuggestionEngine) Suggest(question string) SuggestionResult {
	result := SuggestionResult{
		Question:    question,
		Suggestions: []SuggestionScore{},
	}

	if strings.TrimSpace(question) == "" {
		result.NoMatchReason = "empty question"
		return result
	}

	// Tokenize and normalize question
	tokens := e.tokenize(question)
	if len(tokens) == 0 {
		result.NoMatchReason = "no meaningful tokens in question"
		return result
	}

	questionLower := strings.ToLower(question)
	tokenSet := makeTokenSet(tokens)

	// Score each preset
	scores := make([]SuggestionScore, 0, len(e.presets))
	for i := range e.presets {
		preset := &e.presets[i]
		score := e.scorePreset(preset, &e.compiled[i], tokens, tokenSet, questionLower)
		if score.Score > 0 {
			scores = append(scores, score)
		}
	}

	if len(scores) == 0 {
		result.NoMatchReason = "no preset matched the question keywords"
		return result
	}

	// Sort by score descending
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	result.Suggestions = scores
	if len(scores) > 0 {
		result.TopPick = &scores[0]
	}

	return result
}

// tokenize extracts meaningful words from the question.
func (e *SuggestionEngine) tokenize(text string) []string {
	return tokenizeWithStopWords(text, e.stopWords)
}

func tokenizeWithStopWords(text string, stopWords map[string]bool) []string {
	lower := strings.ToLower(text)
	matches := wordRegex.FindAllString(lower, -1)

	tokens := make([]string, 0, len(matches))
	for _, word := range matches {
		if !stopWords[word] && len(word) > 1 {
			tokens = append(tokens, word)
		}
	}

	return tokens
}

func makeTokenSet(tokens []string) map[string]struct{} {
	set := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		set[token] = struct{}{}
	}
	return set
}

// scorePreset calculates match score between a preset and question tokens.
func (e *SuggestionEngine) scorePreset(preset *EnsemblePreset, compiled *compiledPreset, tokens []string, tokenSet map[string]struct{}, questionLower string) SuggestionScore {
	score := SuggestionScore{
		Preset:     preset,
		PresetName: preset.Name,
		Score:      0,
		Reasons:    []string{},
	}

	if len(compiled.keywords) == 0 {
		return score
	}

	// Score based on keyword matches
	matchedKeywords := 0
	keywordScore := 0.0

	for _, keyword := range compiled.keywords {
		// Check for exact phrase match in original question (higher weight)
		if strings.Contains(questionLower, keyword.lower) {
			keywordScore += 2.0
			matchedKeywords++
			if len(score.Reasons) < 3 {
				score.Reasons = append(score.Reasons, "matches \""+keyword.original+"\"")
			}
			continue
		}

		// Check for token overlap
		for _, keywordToken := range keyword.tokens {
			if _, ok := tokenSet[keywordToken]; ok {
				keywordScore += 1.0
				matchedKeywords++
			}
		}
	}

	// Normalize by number of keywords to prevent bias toward presets with more keywords
	if len(compiled.keywords) > 0 {
		score.Score = keywordScore / float64(len(compiled.keywords))
	}

	// Bonus for tag matches
	for i, tagLower := range compiled.tagsLower {
		for _, token := range tokens {
			if strings.Contains(tagLower, token) || strings.Contains(token, tagLower) {
				score.Score += 0.3
				if len(score.Reasons) < 3 {
					score.Reasons = append(score.Reasons, "tag match: "+preset.Tags[i])
				}
				break
			}
		}
	}

	// Bonus for description matches
	for _, token := range tokens {
		if len(token) >= 4 && strings.Contains(compiled.descriptionLower, token) {
			score.Score += 0.1
		}
	}

	// Add match count to reasons
	if matchedKeywords > 0 && len(score.Reasons) == 0 {
		score.Reasons = append(score.Reasons, compiled.descriptionLower)
	}

	return score
}

// GetPreset returns a preset by name, or nil if not found.
func (e *SuggestionEngine) GetPreset(name string) *EnsemblePreset {
	index, ok := e.presetIndex[name]
	if !ok {
		return nil
	}
	return &e.presets[index]
}

// globalSuggestionEngine is a lazily-initialized global engine.
var (
	globalSuggestionEngine     *SuggestionEngine
	globalSuggestionEngineOnce sync.Once
)

// GlobalSuggestionEngine returns the shared suggestion engine instance.
// It is safe for concurrent use.
func GlobalSuggestionEngine() *SuggestionEngine {
	globalSuggestionEngineOnce.Do(func() {
		globalSuggestionEngine = NewSuggestionEngine()
	})
	return globalSuggestionEngine
}
