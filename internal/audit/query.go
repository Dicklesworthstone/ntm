package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Query represents filter criteria for searching audit logs
type Query struct {
	// Time range filters
	Since *time.Time `json:"since,omitempty"`
	Until *time.Time `json:"until,omitempty"`

	// Event filters
	EventTypes []EventType `json:"event_types,omitempty"`
	Actors     []Actor     `json:"actors,omitempty"`

	// Target filter (supports glob patterns with * and ?)
	TargetPattern string `json:"target_pattern,omitempty"`

	// Session filter
	Sessions []string `json:"sessions,omitempty"`

	// Full-text search pattern (regex)
	GrepPattern string `json:"grep_pattern,omitempty"`

	// Pagination
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`

	// Timeout for long-running queries
	Timeout time.Duration `json:"timeout,omitempty"`
}

// QueryResult contains search results with metadata
type QueryResult struct {
	Entries    []AuditEntry  `json:"entries"`
	TotalCount int           `json:"total_count"`
	Scanned    int           `json:"scanned"`
	Duration   time.Duration `json:"duration"`
	Truncated  bool          `json:"truncated"`
}

// StreamResult wraps an entry or error for streaming
type StreamResult struct {
	Entry *AuditEntry
	Err   error
}

// Index provides in-memory indexing for fast queries
type Index struct {
	mu sync.RWMutex

	// Indexed entries by various keys
	byTime      []indexEntry
	byEventType map[EventType][]int
	byActor     map[Actor][]int
	bySession   map[string][]int

	// Index metadata
	lastModTime time.Time
	entryCount  int
	indexedPath string
}

type indexEntry struct {
	offset    int64
	timestamp time.Time
	eventType EventType
	actor     Actor
	session   string
}

// Searcher provides query capabilities for audit logs
type Searcher struct {
	auditDir string
	index    *Index
	mu       sync.RWMutex
}

// NewSearcher creates a new audit log searcher
func NewSearcher() (*Searcher, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	auditDir := filepath.Join(homeDir, ".local", "share", "ntm", "audit")

	return &Searcher{
		auditDir: auditDir,
		index:    newIndex(),
	}, nil
}

// NewSearcherWithPath creates a searcher for a specific audit directory
func NewSearcherWithPath(auditDir string) *Searcher {
	return &Searcher{
		auditDir: auditDir,
		index:    newIndex(),
	}
}

func newIndex() *Index {
	return &Index{
		byEventType: make(map[EventType][]int),
		byActor:     make(map[Actor][]int),
		bySession:   make(map[string][]int),
	}
}

// Search executes a query and returns matching entries
func (s *Searcher) Search(query Query) (*QueryResult, error) {
	ctx := context.Background()
	if query.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, query.Timeout)
		defer cancel()
	}

	return s.SearchContext(ctx, query)
}

// SearchContext executes a query with context for cancellation/timeout
func (s *Searcher) SearchContext(ctx context.Context, query Query) (*QueryResult, error) {
	startTime := time.Now()
	result := &QueryResult{
		Entries: make([]AuditEntry, 0),
	}

	// Get list of log files to search
	files, err := s.getLogFiles(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get log files: %w", err)
	}

	// Compile patterns once
	var targetRegex *regexp.Regexp
	if query.TargetPattern != "" {
		pattern := globToRegex(query.TargetPattern)
		targetRegex, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid target pattern: %w", err)
		}
	}

	var grepRegex *regexp.Regexp
	if query.GrepPattern != "" {
		grepRegex, err = regexp.Compile(query.GrepPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid grep pattern: %w", err)
		}
	}

	// Build filter function
	filter := s.buildFilter(query, targetRegex)

	skipped := 0
	collected := 0
	limit := query.Limit
	if limit <= 0 {
		limit = 10000 // Default limit to prevent unbounded memory usage
	}

	// Search each file
	for _, filePath := range files {
		select {
		case <-ctx.Done():
			result.Truncated = true
			result.Duration = time.Since(startTime)
			return result, ctx.Err()
		default:
		}

		err := s.searchFile(ctx, filePath, filter, grepRegex, func(entry AuditEntry) bool {
			result.Scanned++

			// Handle offset (skip entries)
			if query.Offset > 0 && skipped < query.Offset {
				skipped++
				return true // Continue scanning
			}

			// Check limit
			if collected >= limit {
				result.Truncated = true
				return false // Stop scanning
			}

			result.Entries = append(result.Entries, entry)
			collected++
			return true
		})

		if err != nil {
			// Log but don't fail on individual file errors
			continue
		}

		if result.Truncated {
			break
		}
	}

	result.TotalCount = len(result.Entries)
	result.Duration = time.Since(startTime)
	return result, nil
}

// StreamSearch returns a channel of matching entries for memory efficiency
func (s *Searcher) StreamSearch(ctx context.Context, query Query) (<-chan StreamResult, error) {
	results := make(chan StreamResult, 100)

	// Get list of log files to search
	files, err := s.getLogFiles(query)
	if err != nil {
		close(results)
		return results, fmt.Errorf("failed to get log files: %w", err)
	}

	// Compile patterns once
	var targetRegex *regexp.Regexp
	if query.TargetPattern != "" {
		pattern := globToRegex(query.TargetPattern)
		targetRegex, err = regexp.Compile(pattern)
		if err != nil {
			close(results)
			return results, fmt.Errorf("invalid target pattern: %w", err)
		}
	}

	var grepRegex *regexp.Regexp
	if query.GrepPattern != "" {
		grepRegex, err = regexp.Compile(query.GrepPattern)
		if err != nil {
			close(results)
			return results, fmt.Errorf("invalid grep pattern: %w", err)
		}
	}

	// Build filter function
	filter := s.buildFilter(query, targetRegex)

	go func() {
		defer close(results)

		skipped := 0
		sent := 0
		limit := query.Limit
		if limit <= 0 {
			limit = 10000
		}

		for _, filePath := range files {
			select {
			case <-ctx.Done():
				results <- StreamResult{Err: ctx.Err()}
				return
			default:
			}

			err := s.searchFile(ctx, filePath, filter, grepRegex, func(entry AuditEntry) bool {
				// Handle offset
				if query.Offset > 0 && skipped < query.Offset {
					skipped++
					return true
				}

				// Check limit
				if sent >= limit {
					return false
				}

				select {
				case <-ctx.Done():
					return false
				case results <- StreamResult{Entry: &entry}:
					sent++
					return true
				}
			})

			if err != nil {
				results <- StreamResult{Err: err}
				return
			}

			if sent >= limit {
				break
			}
		}
	}()

	return results, nil
}

// Count returns the number of entries matching the query
func (s *Searcher) Count(query Query) (int, error) {
	ctx := context.Background()
	if query.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, query.Timeout)
		defer cancel()
	}

	return s.CountContext(ctx, query)
}

// CountContext returns the count with context for cancellation
func (s *Searcher) CountContext(ctx context.Context, query Query) (int, error) {
	// Get list of log files to search
	files, err := s.getLogFiles(query)
	if err != nil {
		return 0, fmt.Errorf("failed to get log files: %w", err)
	}

	// Compile patterns once
	var targetRegex *regexp.Regexp
	if query.TargetPattern != "" {
		pattern := globToRegex(query.TargetPattern)
		targetRegex, err = regexp.Compile(pattern)
		if err != nil {
			return 0, fmt.Errorf("invalid target pattern: %w", err)
		}
	}

	var grepRegex *regexp.Regexp
	if query.GrepPattern != "" {
		grepRegex, err = regexp.Compile(query.GrepPattern)
		if err != nil {
			return 0, fmt.Errorf("invalid grep pattern: %w", err)
		}
	}

	// Build filter function
	filter := s.buildFilter(query, targetRegex)

	count := 0

	for _, filePath := range files {
		select {
		case <-ctx.Done():
			return count, ctx.Err()
		default:
		}

		err := s.searchFile(ctx, filePath, filter, grepRegex, func(entry AuditEntry) bool {
			count++
			return true
		})

		if err != nil {
			continue
		}
	}

	return count, nil
}

// getLogFiles returns audit log files that could contain matching entries
func (s *Searcher) getLogFiles(query Query) ([]string, error) {
	entries, err := os.ReadDir(s.auditDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No audit directory yet
		}
		return nil, fmt.Errorf("failed to read audit directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		// Filter by session if specified
		if len(query.Sessions) > 0 {
			matched := false
			for _, session := range query.Sessions {
				if strings.HasPrefix(name, session+"-") {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		// Filter by date if time range specified.
		//
		// Filenames are stamped with the LOCAL date (logger.go), while entry
		// timestamps are UTC. Parsing the filename as UTC therefore shifted the
		// file's day by the UTC offset and skipped files that did contain matching
		// entries: on a UTC-4 host, entries written at local 21:00 on the 22nd
		// carry UTC timestamps on the 23rd, so a --since of the 23rd computed a
		// file date of the 22nd, saw it as Before, and dropped the file.
		//
		// The bound is also a whole day wide now. A file named for day D holds
		// entries anywhere within that local day, so it can only be excluded when
		// the entire day precedes Since or the day starts after Until.
		if query.Since != nil || query.Until != nil {
			// Extract date from filename: session-YYYY-MM-DD.jsonl
			datePart := extractDateFromFilename(name)
			if datePart != "" {
				fileDate, err := time.ParseInLocation("2006-01-02", datePart, time.Local)
				if err == nil {
					if query.Since != nil && fileDate.Add(24*time.Hour).Before(*query.Since) {
						continue
					}
					if query.Until != nil && fileDate.After(*query.Until) {
						continue
					}
				}
			}
		}

		files = append(files, filepath.Join(s.auditDir, name))
	}

	return files, nil
}

// buildFilter creates a filter function based on query criteria
func (s *Searcher) buildFilter(query Query, targetRegex *regexp.Regexp) func(AuditEntry) bool {
	return func(entry AuditEntry) bool {
		// Time range filter
		if query.Since != nil && entry.Timestamp.Before(*query.Since) {
			return false
		}
		if query.Until != nil && entry.Timestamp.After(*query.Until) {
			return false
		}

		// Event type filter
		if len(query.EventTypes) > 0 {
			found := false
			for _, et := range query.EventTypes {
				if entry.EventType == et {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}

		// Actor filter
		if len(query.Actors) > 0 {
			found := false
			for _, a := range query.Actors {
				if entry.Actor == a {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}

		// Session filter
		if len(query.Sessions) > 0 {
			found := false
			for _, sess := range query.Sessions {
				if entry.SessionID == sess {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}

		// Target pattern filter
		if targetRegex != nil && !targetRegex.MatchString(entry.Target) {
			return false
		}

		return true
	}
}

// searchFile scans a single log file and calls the callback for matching entries
func (s *Searcher) searchFile(ctx context.Context, filePath string, filter func(AuditEntry) bool, grepRegex *regexp.Regexp, callback func(AuditEntry) bool) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Set max line size for large audit payloads (10MB), start with 64KB
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Full-text grep filter (before parsing JSON for efficiency)
		if grepRegex != nil && !grepRegex.Match(line) {
			continue
		}

		var entry AuditEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip malformed entries
			continue
		}

		// Apply structured filters
		if !filter(entry) {
			continue
		}

		// Call callback; if it returns false, stop scanning
		if !callback(entry) {
			return nil
		}
	}

	return scanner.Err()
}

// BuildIndex builds or updates the in-memory index for faster queries
func (s *Searcher) BuildIndex(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get all log files
	files, err := s.getLogFiles(Query{})
	if err != nil {
		return fmt.Errorf("failed to get log files: %w", err)
	}

	newIndex := newIndex()
	entryIdx := 0

	for _, filePath := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		file, err := os.Open(filePath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var entry AuditEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				continue
			}

			// Add to index
			ie := indexEntry{
				timestamp: entry.Timestamp,
				eventType: entry.EventType,
				actor:     entry.Actor,
				session:   entry.SessionID,
			}
			newIndex.byTime = append(newIndex.byTime, ie)

			// Index by event type
			newIndex.byEventType[entry.EventType] = append(newIndex.byEventType[entry.EventType], entryIdx)

			// Index by actor
			newIndex.byActor[entry.Actor] = append(newIndex.byActor[entry.Actor], entryIdx)

			// Index by session
			newIndex.bySession[entry.SessionID] = append(newIndex.bySession[entry.SessionID], entryIdx)

			entryIdx++
		}

		file.Close()
	}

	newIndex.entryCount = entryIdx
	newIndex.lastModTime = time.Now()
	s.index = newIndex

	return nil
}

// GetIndex returns the current index (may be nil if not built)
func (s *Searcher) GetIndex() *Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.index
}

// IndexStats returns statistics about the current index
func (s *Searcher) IndexStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.index == nil || s.index.entryCount == 0 {
		return map[string]interface{}{
			"indexed":     false,
			"entry_count": 0,
		}
	}

	eventTypeCounts := make(map[string]int)
	for et, indices := range s.index.byEventType {
		eventTypeCounts[string(et)] = len(indices)
	}

	actorCounts := make(map[string]int)
	for a, indices := range s.index.byActor {
		actorCounts[string(a)] = len(indices)
	}

	return map[string]interface{}{
		"indexed":           true,
		"entry_count":       s.index.entryCount,
		"last_indexed":      s.index.lastModTime,
		"session_count":     len(s.index.bySession),
		"event_type_counts": eventTypeCounts,
		"actor_counts":      actorCounts,
	}
}

// globToRegex converts a glob pattern to a regex pattern
func globToRegex(glob string) string {
	// Escape regex special characters except * and ?
	var result strings.Builder
	result.WriteString("^")

	for _, ch := range glob {
		switch ch {
		case '*':
			result.WriteString(".*")
		case '?':
			result.WriteString(".")
		case '.', '+', '^', '$', '(', ')', '[', ']', '{', '}', '|', '\\':
			result.WriteString("\\")
			result.WriteRune(ch)
		default:
			result.WriteRune(ch)
		}
	}

	result.WriteString("$")
	return result.String()
}

// extractDateFromFilename extracts the date portion from a log filename
// Expected format: session-YYYY-MM-DD.jsonl
func extractDateFromFilename(filename string) string {
	// Remove .jsonl suffix
	name := strings.TrimSuffix(filename, ".jsonl")

	// The date is always the last 10 characters in YYYY-MM-DD format
	if len(name) < 10 {
		return ""
	}

	datePart := name[len(name)-10:]
	// Validate YYYY-MM-DD format
	parts := strings.Split(datePart, "-")
	if len(parts) == 3 && len(parts[0]) == 4 && len(parts[1]) == 2 && len(parts[2]) == 2 {
		return datePart
	}

	return ""
}

// AuditDir returns the path to the audit directory
func (s *Searcher) AuditDir() string {
	return s.auditDir
}
