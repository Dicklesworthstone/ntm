package handoff

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	goalRegex   = regexp.MustCompile(`(?m)^goal:\s*["']?(.+?)["']?\s*$`)
	nowRegex    = regexp.MustCompile(`(?m)^now:\s*["']?(.+?)["']?\s*$`)
	statusRegex = regexp.MustCompile(`(?m)^status:\s*(\w+)`)
)

// Reader handles reading handoff files from disk with caching.
type Reader struct {
	baseDir string
	logger  *slog.Logger

	// Cache for ExtractGoalNow (most frequent operation)
	cacheMu      sync.RWMutex
	goalNowCache map[string]goalNowEntry
	cacheExpiry  time.Duration
}

type goalNowEntry struct {
	goal      string
	now       string
	fetchedAt time.Time
	modTime   time.Time // file mod time when fetched
}

// HandoffMeta provides summary information about a handoff file.
type HandoffMeta struct {
	Path    string
	Session string
	Date    time.Time
	Status  string
	Goal    string // For quick display
	IsAuto  bool
}

func discoverHandoffYAMLPaths(dir string, entries []os.DirEntry) []string {
	var paths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || e.Type()&os.ModeSymlink != 0 {
			continue
		}

		path := filepath.Join(dir, e.Name())
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		paths = append(paths, path)
	}

	sort.Slice(paths, func(i, j int) bool {
		return filepath.Base(paths[i]) > filepath.Base(paths[j])
	})
	return paths
}

// NewReader creates a Reader for the given project directory.
func NewReader(projectDir string) *Reader {
	return &Reader{
		baseDir:      filepath.Join(projectDir, ".ntm", "handoffs"),
		logger:       slog.Default().With("component", "handoff.reader"),
		goalNowCache: make(map[string]goalNowEntry),
		cacheExpiry:  30 * time.Second,
	}
}

// NewReaderWithOptions creates a Reader with custom options.
func NewReaderWithOptions(projectDir string, cacheExpiry time.Duration, logger *slog.Logger) *Reader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reader{
		baseDir:      filepath.Join(projectDir, ".ntm", "handoffs"),
		logger:       logger.With("component", "handoff.reader"),
		goalNowCache: make(map[string]goalNowEntry),
		cacheExpiry:  cacheExpiry,
	}
}

// FindLatest returns the most recent handoff for a session.
// Returns (nil, "", nil) if no handoffs exist (not an error).
func (r *Reader) FindLatest(sessionName string) (*Handoff, string, error) {
	r.logger.Debug("finding latest handoff", "session", sessionName)

	dir := filepath.Join(r.baseDir, sessionName)

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		r.logger.Debug("no handoff directory", "session", sessionName)
		return nil, "", nil // No directory = no handoffs, not an error
	}
	if err != nil {
		r.logger.Error("failed to read handoff directory",
			"dir", dir,
			"error", err,
		)
		return nil, "", fmt.Errorf("readdir failed: %w", err)
	}

	yamlPaths := discoverHandoffYAMLPaths(dir, entries)
	if len(yamlPaths) == 0 {
		r.logger.Debug("no handoff files found", "session", sessionName)
		return nil, "", nil
	}

	var lastErr error
	for _, path := range yamlPaths {
		h, err := r.Read(path)
		if err != nil {
			lastErr = err
			r.logger.Warn("skipping unreadable handoff while finding latest",
				"path", path,
				"error", err,
			)
			continue
		}

		r.logger.Debug("found latest handoff",
			"session", sessionName,
			"path", path,
			"goal", truncateForLog(h.Goal, 30),
		)
		return h, path, nil
	}

	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", nil
}

// FindLatestAny returns the most recent handoff across all sessions.
func (r *Reader) FindLatestAny() (*Handoff, string, error) {
	r.logger.Debug("finding latest handoff across all sessions")

	entries, err := os.ReadDir(r.baseDir)
	if os.IsNotExist(err) {
		r.logger.Debug("no handoffs directory exists")
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("readdir base failed: %w", err)
	}

	var newest *Handoff
	var newestPath string
	var newestTime time.Time

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		h, path, err := r.FindLatest(e.Name())
		if err != nil || h == nil {
			continue
		}
		if h.CreatedAt.After(newestTime) {
			newest = h
			newestPath = path
			newestTime = h.CreatedAt
		}
	}

	if newest != nil {
		r.logger.Debug("found latest handoff across all",
			"path", newestPath,
			"session", newest.Session,
		)
	}

	return newest, newestPath, nil
}

// Read parses a specific handoff file.
func (r *Reader) Read(path string) (*Handoff, error) {
	r.logger.Debug("reading handoff", "path", path)

	data, err := os.ReadFile(path)
	if err != nil {
		r.logger.Error("failed to read handoff file",
			"path", path,
			"error", err,
		)
		return nil, fmt.Errorf("read failed: %w", err)
	}

	var h Handoff
	if err := yaml.Unmarshal(data, &h); err != nil {
		r.logger.Error("failed to parse handoff YAML",
			"path", path,
			"error", err,
			"size", len(data),
		)
		return nil, fmt.Errorf("unmarshal failed: %w", err)
	}

	// Validate on read
	if errs := h.Validate(); len(errs) > 0 {
		r.logger.Warn("handoff has validation issues",
			"path", path,
			"error_count", len(errs),
			"first_error", errs[0].Error(),
		)
		// Continue anyway - allow reading partial/old handoffs
	}

	r.logger.Debug("handoff read successfully",
		"path", path,
		"session", h.Session,
		"has_goal", h.Goal != "",
		"has_now", h.Now != "",
	)

	return &h, nil
}

// ExtractGoalNow extracts just goal and now fields efficiently.
// For status line use - uses regex and caching for speed.
func (r *Reader) ExtractGoalNow(sessionName string) (goal, now string, err error) {
	// Find latest file path first
	dir := filepath.Join(r.baseDir, sessionName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}

	yamlPaths := discoverHandoffYAMLPaths(dir, entries)
	if len(yamlPaths) == 0 {
		return "", "", nil
	}

	var lastErr error
	for _, path := range yamlPaths {
		// Check cache
		r.cacheMu.RLock()
		entry, ok := r.goalNowCache[path]
		r.cacheMu.RUnlock()

		if ok {
			// Check if cache is still valid
			info, err := os.Stat(path)
			if err == nil && info.ModTime().Equal(entry.modTime) && time.Since(entry.fetchedAt) < r.cacheExpiry {
				r.logger.Debug("cache hit for goal/now",
					"path", path,
					"age_ms", time.Since(entry.fetchedAt).Milliseconds(),
				)
				return entry.goal, entry.now, nil
			}
		}

		r.logger.Debug("cache miss for goal/now, reading file", "path", path)

		content, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}

		goal, now = extractGoalNowFromContent(content)

		info, _ := os.Stat(path)
		modTime := time.Time{}
		if info != nil {
			modTime = info.ModTime()
		}

		r.cacheMu.Lock()
		r.goalNowCache[path] = goalNowEntry{
			goal:      goal,
			now:       now,
			fetchedAt: time.Now(),
			modTime:   modTime,
		}
		r.cacheMu.Unlock()

		r.logger.Debug("extracted goal/now",
			"path", path,
			"goal_len", len(goal),
			"now_len", len(now),
		)

		return goal, now, nil
	}

	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", nil
}

// InvalidateCache clears the goal/now cache.
// Call when you know handoffs have been written.
func (r *Reader) InvalidateCache() {
	r.cacheMu.Lock()
	r.goalNowCache = make(map[string]goalNowEntry)
	r.cacheMu.Unlock()
	r.logger.Debug("cache invalidated")
}

// ListHandoffs returns all handoffs for a session, sorted by date descending.
func (r *Reader) ListHandoffs(sessionName string) ([]HandoffMeta, error) {
	r.logger.Debug("listing handoffs", "session", sessionName)

	dir := filepath.Join(r.baseDir, sessionName)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("readdir failed: %w", err)
	}

	var metas []HandoffMeta
	for _, path := range discoverHandoffYAMLPaths(dir, entries) {
		info, _ := os.Stat(path)

		// Quick extract goal/now without full parse
		goal, _, err := r.extractGoalNowDirect(path)
		if err != nil {
			continue
		}

		meta := HandoffMeta{
			Path:    path,
			Session: sessionName,
			Goal:    goal,
			IsAuto:  strings.HasPrefix(filepath.Base(path), "auto-handoff-"),
		}

		if info != nil {
			meta.Date = info.ModTime()
		}

		// Extract status from file (quick regex)
		if content, err := os.ReadFile(path); err == nil {
			if match := statusRegex.FindSubmatch(content); match != nil {
				meta.Status = string(match[1])
			}
		}

		metas = append(metas, meta)
	}

	// Sort by date descending
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].Date.After(metas[j].Date)
	})

	r.logger.Debug("listed handoffs",
		"session", sessionName,
		"count", len(metas),
	)

	return metas, nil
}

// ListSessions returns all sessions that have handoffs.
func (r *Reader) ListSessions() ([]string, error) {
	r.logger.Debug("listing sessions")

	entries, err := os.ReadDir(r.baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("readdir base failed: %w", err)
	}

	var sessions []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		h, _, err := r.FindLatest(e.Name())
		if err != nil || h == nil {
			continue
		}
		sessions = append(sessions, e.Name())
	}

	sort.Strings(sessions)
	r.logger.Debug("listed sessions", "count", len(sessions))

	return sessions, nil
}

// BaseDir returns the base directory where handoffs are stored.
func (r *Reader) BaseDir() string {
	return r.baseDir
}

// extractGoalNowDirect does regex extraction without caching.
func (r *Reader) extractGoalNowDirect(path string) (goal, now string, err error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	goal, now = extractGoalNowFromContent(content)
	return goal, now, nil
}

// extractGoalNowFromContent extracts goal and now from raw YAML content.
func extractGoalNowFromContent(content []byte) (goal, now string) {
	if match := goalRegex.FindSubmatch(content); match != nil {
		goal = strings.TrimSpace(string(match[1]))
		// Remove trailing quotes if present
		goal = strings.Trim(goal, `"'`)
	}
	if match := nowRegex.FindSubmatch(content); match != nil {
		now = strings.TrimSpace(string(match[1]))
		// Remove trailing quotes if present
		now = strings.Trim(now, `"'`)
	}
	return goal, now
}

// truncateForLog shortens a string for logging purposes.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
