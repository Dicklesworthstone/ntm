package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const defaultCommandTriggerTimeout = 30 * time.Second

// RuntimeTrigger evaluates a workflow transition condition. Start is only
// needed by triggers that consume a continuous source, such as filesystem
// notifications; all implementations are safe to Stop more than once.
type RuntimeTrigger interface {
	Type() TriggerType
	Check(*TriggerContext) (bool, error)
	Start(*TriggerContext) error
	Stop() error
}

// AgentOutput is a captured agent response available to agent_says triggers.
// Role is the workflow role, rather than the underlying pane or agent ID.
type AgentOutput struct {
	Role string
	Text string
}

// AgentActivity is the last observed activity for an agent in a workflow role.
type AgentActivity struct {
	Role         string
	LastActivity time.Time
}

// TriggerContext supplies runtime observations without coupling workflow
// templates to tmux or a particular session coordinator.
type TriggerContext struct {
	Context     context.Context
	ProjectRoot string
	Session     string
	CurrentRole string
	Outputs     []AgentOutput
	Activities  []AgentActivity
	Now         func() time.Time

	// TransitionEvidence contains verified agent_says receipts for the current
	// stage visit, keyed by the template's complete Flow.Transitions indexes.
	// A nonnil map replaces raw-output matching for those transitions, allowing
	// durable receipts to survive scrollback rollover and runner restarts.
	// Nil preserves ordinary trigger evaluation against Outputs.
	TransitionEvidence map[int]bool

	// FileTriggerStates is the complete filesystem evidence set for this
	// stage visit, keyed by the template's complete transition indexes. At
	// startup it restores the original file baselines; at evaluation it only
	// authorizes transitions whose Fired evidence the caller has saved. Nil
	// starts fresh baselines and evaluates live filesystem observations.
	FileTriggerStates map[int]FileTriggerState
	// FileTriggerExcludes names project-relative state directories whose
	// writes are made by the runner itself, never by the workflow's work.
	FileTriggerExcludes []string
}

func (c *TriggerContext) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// TriggerFactory builds a runtime trigger from a validated template trigger.
type TriggerFactory func(Trigger) (RuntimeTrigger, error)

// TriggerRegistry maps template trigger types to runtime implementations.
type TriggerRegistry struct {
	mu        sync.RWMutex
	factories map[TriggerType]TriggerFactory
}

// NewTriggerRegistry returns the standard trigger registry.
func NewTriggerRegistry() *TriggerRegistry {
	r := &TriggerRegistry{factories: make(map[TriggerType]TriggerFactory)}
	r.Register(TriggerFileCreated, newFileCreatedTrigger)
	r.Register(TriggerFileModified, newFileModifiedTrigger)
	r.Register(TriggerCommandSuccess, newCommandSuccessTrigger)
	r.Register(TriggerCommandFailure, newCommandFailureTrigger)
	r.Register(TriggerAgentSays, newAgentSaysTrigger)
	r.Register(TriggerAllAgentsIdle, newAllAgentsIdleTrigger)
	r.Register(TriggerManual, newManualTrigger)
	r.Register(TriggerTimeElapsed, newTimeElapsedTrigger)
	return r
}

// Register installs a factory for a trigger type. Supplying a nil factory
// removes a factory, which is useful when tests or an embedding application
// intentionally disable a trigger type.
func (r *TriggerRegistry) Register(kind TriggerType, factory TriggerFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if factory == nil {
		delete(r.factories, kind)
		return
	}
	r.factories[kind] = factory
}

// Create validates config and constructs its runtime trigger.
func (r *TriggerRegistry) Create(config Trigger) (RuntimeTrigger, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	factory := r.factories[config.Type]
	r.mu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("no runtime trigger registered for %q", config.Type)
	}
	return factory(config)
}

const (
	fileTriggerStateVersion = 1
	maxFileTriggerFiles     = 16384
	maxFileTriggerBytes     = 256 << 20
	maxFileTriggerPaths     = 4 << 20
	maxFileTriggerEntries   = 262144
)

// FileTriggerStamp records a regular file's original content and metadata.
// Hashes detect offline replacements even when size and modification time
// are preserved; file contents are never copied into the checkpoint.
type FileTriggerStamp struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	SHA256  string    `json:"sha256"`
}

// FileTriggerState belongs to one outgoing transition in a stage visit. The
// runner additionally binds the complete set to its stage visit and delivery
// round. Baseline is immutable for that attempt; Fired is sticky evidence,
// including a notification for a file that has since been removed.
type FileTriggerState struct {
	Version         int                         `json:"version"`
	ProjectRoot     string                      `json:"project_root"`
	Stage           string                      `json:"stage"`
	TransitionIndex int                         `json:"transition_index"`
	Kind            TriggerType                 `json:"kind"`
	Pattern         string                      `json:"pattern"`
	Excludes        []string                    `json:"excludes,omitempty"`
	Baseline        map[string]FileTriggerStamp `json:"baseline"`
	Fired           bool                        `json:"fired"`
}

type fileDigestCache struct {
	info  os.FileInfo
	stamp FileTriggerStamp
}

// fileBaseline tracks a newly created empty file until it is written.
type fileBaseline struct {
	size    int64
	modTime time.Time
}

type fileTrigger struct {
	kind    TriggerType
	pattern string
	op      fsnotify.Op

	// scanMu excludes scans from Stop, which closes the rooted filesystem.
	// Notification handling never takes scanMu and continues during scans.
	scanMu          sync.Mutex
	mu              sync.Mutex
	root            string
	excludes        []string
	directory       *os.Root
	stage           string
	transitionIndex int
	restore         *FileTriggerState
	baseline        map[string]FileTriggerStamp
	cache           map[string]fileDigestCache
	fired           bool
	err             error
	watcher         *fsnotify.Watcher
	done            chan struct{}
	wg              sync.WaitGroup
	// pending tracks pattern-matching files whose Create event has been seen
	// but whose first Write event may never arrive: on kqueue platforms
	// (macOS/BSD) fsnotify registers the new file's own watch asynchronously
	// after the Create event, so a create-then-write-once sequence can lose
	// the write. Check falls back to stat-ing these files.
	pending map[string]fileBaseline
}

func newFileCreatedTrigger(config Trigger) (RuntimeTrigger, error) {
	return &fileTrigger{kind: TriggerFileCreated, pattern: config.Pattern, op: fsnotify.Create}, nil
}

func newFileModifiedTrigger(config Trigger) (RuntimeTrigger, error) {
	return &fileTrigger{kind: TriggerFileModified, pattern: config.Pattern, op: fsnotify.Write}, nil
}

func (t *fileTrigger) Type() TriggerType { return t.kind }

func (t *fileTrigger) Start(ctx *TriggerContext) error {
	if ctx == nil || strings.TrimSpace(ctx.ProjectRoot) == "" {
		return errors.New("file trigger requires a project root")
	}
	root, err := canonicalFileTriggerRoot(ctx.ProjectRoot)
	if err != nil {
		return err
	}
	excludes, err := normalizeFileTriggerExcludes(ctx.FileTriggerExcludes)
	if err != nil {
		return err
	}
	if t.restore != nil {
		if err := validateFileTriggerState(*t.restore, root, t.stage, t.transitionIndex, t.kind, t.pattern, excludes); err != nil {
			return err
		}
	}
	if err := t.Stop(); err != nil {
		return err
	}
	t.scanMu.Lock()
	defer t.scanMu.Unlock()
	directory, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open workflow project root: %w", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		_ = directory.Close()
		return fmt.Errorf("create filesystem watcher: %w", err)
	}
	t.mu.Lock()
	t.root = root
	t.excludes = excludes
	t.directory = directory
	t.fired = false
	t.err = nil
	t.baseline = nil
	t.cache = nil
	t.pending = make(map[string]fileBaseline)
	t.watcher = watcher
	t.done = make(chan struct{})
	done := t.done
	t.wg.Add(1)
	t.mu.Unlock()

	// Drain notifications while installing watches and taking the baseline.
	// Each directory is watched before its children are listed, so a change
	// during the initial scan is either in the baseline or latched by run.
	go t.run(watcher, directory, root, done)
	if t.restore != nil && t.restore.Fired {
		// A saved notification is already proof. Later deletion or an
		// unrelated unreadable subtree must not erase that completed work.
		t.mu.Lock()
		t.baseline = cloneFileTriggerBaseline(t.restore.Baseline)
		t.fired = true
		t.mu.Unlock()
		return nil
	}
	current, cache, err := snapshotFileTriggerTree(directory, root, t.pattern, excludes, watcher, nil)
	if err != nil {
		return errors.Join(err, t.stopLocked())
	}
	t.mu.Lock()
	t.cache = cache
	if t.restore == nil {
		t.baseline = current
	} else {
		t.baseline = cloneFileTriggerBaseline(t.restore.Baseline)
		t.fired = t.fired || t.restore.Fired || fileTriggerChanged(t.kind, t.baseline, current)
	}
	t.mu.Unlock()
	return nil
}

func (t *fileTrigger) run(watcher *fsnotify.Watcher, directory *os.Root, root string, done <-chan struct{}) {
	defer t.wg.Done()
	for {
		select {
		case <-done:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			rel, relErr := filepath.Rel(root, event.Name)
			if relErr != nil || !filepath.IsLocal(rel) {
				continue
			}
			if fileTriggerExcluded(t.excludes, filepath.ToSlash(rel)) {
				continue
			}
			// Rooted reads cannot follow a replaced directory or symlink out
			// of the project. Symlink and directory events are not file work.
			info, statErr := directory.Lstat(rel)
			if statErr == nil && info.IsDir() && event.Op&fsnotify.Create != 0 {
				if err := watchFileTriggerDirectories(directory, root, filepath.ToSlash(rel), t.excludes, watcher); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.mu.Lock()
					t.err = fmt.Errorf("watch created directory %q: %w", rel, err)
					t.mu.Unlock()
				}
			}
			if statErr != nil || !info.Mode().IsRegular() || !t.matches(event.Name) {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				// kqueue write-loss fallback: fsnotify registers a brand-new
				// file's own watch asynchronously after its Create event, so a
				// write landing immediately after creation can produce no Write
				// event at all. For write triggers, treat a matching file that
				// already has content as written, and remember empty ones so
				// Check can detect the write by stat.
				if t.op&fsnotify.Write != 0 {
					t.mu.Lock()
					if info.Size() > 0 {
						t.fired = true
					} else if t.pending != nil {
						if len(t.pending) >= maxFileTriggerFiles {
							t.err = fmt.Errorf("file trigger exceeds %d pending files; narrow its pattern", maxFileTriggerFiles)
						} else {
							t.pending[event.Name] = fileBaseline{size: info.Size(), modTime: info.ModTime()}
						}
					}
					t.mu.Unlock()
				}
			}
			if event.Op&t.op == 0 {
				continue
			}
			t.mu.Lock()
			t.fired = true
			t.mu.Unlock()
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			t.mu.Lock()
			t.err = err
			t.mu.Unlock()
		}
	}
}

func (t *fileTrigger) matches(name string) bool {
	t.mu.Lock()
	root := t.root
	pattern := t.pattern
	excludes := t.excludes
	t.mu.Unlock()
	rel, err := filepath.Rel(root, name)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return false
	}
	rel = filepath.ToSlash(rel)
	return !fileTriggerExcluded(excludes, rel) && fileTriggerMatches(pattern, rel)
}

func fileTriggerMatches(pattern, relative string) bool {
	if matchPathPattern(pattern, relative) {
		return true
	}
	matched, _ := filepath.Match(pattern, filepath.Base(relative))
	return matched
}

func canonicalFileTriggerRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workflow project root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workflow project root: %w", err)
	}
	return filepath.Clean(canonical), nil
}

func cloneFileTriggerBaseline(source map[string]FileTriggerStamp) map[string]FileTriggerStamp {
	if source == nil {
		return nil
	}
	copy := make(map[string]FileTriggerStamp, len(source))
	for path, stamp := range source {
		copy[path] = stamp
	}
	return copy
}

func cloneFileTriggerStates(source map[int]FileTriggerState) map[int]FileTriggerState {
	if source == nil {
		return nil
	}
	copy := make(map[int]FileTriggerState, len(source))
	for index, state := range source {
		state.Baseline = cloneFileTriggerBaseline(state.Baseline)
		state.Excludes = slices.Clone(state.Excludes)
		copy[index] = state
	}
	return copy
}

func normalizeFileTriggerExcludes(excludes []string) ([]string, error) {
	if len(excludes) > 128 {
		return nil, errors.New("too many workflow file trigger exclusions")
	}
	normalized := make([]string, 0, len(excludes))
	paths := 0
	for _, path := range excludes {
		if !filepath.IsLocal(filepath.FromSlash(path)) || strings.IndexByte(path, 0) >= 0 {
			return nil, fmt.Errorf("invalid workflow file trigger exclusion %q", path)
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if clean == "." || !fs.ValidPath(clean) {
			return nil, fmt.Errorf("invalid workflow file trigger exclusion %q", path)
		}
		paths += len(clean)
		if paths > maxFileTriggerPaths {
			return nil, errors.New("workflow file trigger exclusions are oversized")
		}
		normalized = append(normalized, clean)
	}
	sort.Strings(normalized)
	return slices.Compact(normalized), nil
}

func fileTriggerExcluded(excludes []string, path string) bool {
	for _, excluded := range excludes {
		if path == excluded || strings.HasPrefix(path, excluded+"/") {
			return true
		}
	}
	return false
}

func validateFileTriggerState(state FileTriggerState, root, stage string, index int, kind TriggerType, pattern string, excludes []string) error {
	if state.Version != fileTriggerStateVersion || state.ProjectRoot != root || state.Stage != stage || state.TransitionIndex != index || state.Kind != kind || state.Pattern != pattern {
		return fmt.Errorf("file trigger checkpoint does not match project, stage or transition %d", index)
	}
	if !slices.Equal(state.Excludes, excludes) {
		return fmt.Errorf("file trigger %d checkpoint has different state-directory exclusions", index)
	}
	if state.Baseline == nil || len(state.Baseline) > maxFileTriggerFiles {
		return fmt.Errorf("file trigger %d has missing or oversized baseline", index)
	}
	var bytes int64
	paths := 0
	for name, stamp := range state.Baseline {
		if name == "." || !fs.ValidPath(name) || strings.IndexByte(name, 0) >= 0 || !filepath.IsLocal(filepath.FromSlash(name)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) != name || fileTriggerExcluded(excludes, name) || !fileTriggerMatches(pattern, name) {
			return fmt.Errorf("file trigger %d has invalid baseline path %q", index, name)
		}
		paths += len(name)
		if paths > maxFileTriggerPaths || stamp.Size < 0 || stamp.Size > maxFileTriggerBytes || stamp.ModTime.IsZero() {
			return fmt.Errorf("file trigger %d has invalid or oversized baseline metadata", index)
		}
		bytes += stamp.Size
		if bytes > maxFileTriggerBytes {
			return fmt.Errorf("file trigger %d baseline exceeds %d bytes", index, maxFileTriggerBytes)
		}
		digest, err := hex.DecodeString(stamp.SHA256)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != stamp.SHA256 {
			return fmt.Errorf("file trigger %d has invalid baseline digest for %q", index, name)
		}
	}
	return nil
}

// watchFileTriggerDirectories repairs watches for a new directory subtree.
// Its children may already exist before fsnotify reports the parent create;
// Check reconciles those files without requiring another write notification.
func watchFileTriggerDirectories(directory *os.Root, root, relative string, excludes []string, watcher *fsnotify.Watcher) error {
	entries := 0
	return fs.WalkDir(directory.FS(), relative, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fileTriggerExcluded(excludes, name) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if entries > maxFileTriggerEntries {
			return fmt.Errorf("file trigger project tree exceeds %d entries", maxFileTriggerEntries)
		}
		if !entry.IsDir() {
			return nil
		}
		return watcher.Add(filepath.Join(root, filepath.FromSlash(name)))
	})
}

// snapshotFileTriggerTree never follows symlink entries. All reads use os.Root
// so a directory swapped for an escaping symlink cannot disclose file contents.
// Metadata and file identity let steady-state polls reuse digests; restoration
// deliberately supplies no cache and hashes every matching file again.
func snapshotFileTriggerTree(directory *os.Root, root, pattern string, excludes []string, watcher *fsnotify.Watcher, cache map[string]fileDigestCache) (map[string]FileTriggerStamp, map[string]fileDigestCache, error) {
	liveRoot, err := os.Stat(root)
	if err != nil {
		return nil, nil, fmt.Errorf("stat workflow project root: %w", err)
	}
	openedRoot, err := directory.Stat(".")
	if err != nil {
		return nil, nil, fmt.Errorf("stat open workflow project root: %w", err)
	}
	if !os.SameFile(liveRoot, openedRoot) {
		return nil, nil, errors.New("workflow project root changed while watching files")
	}
	files := make(map[string]FileTriggerStamp)
	nextCache := make(map[string]fileDigestCache)
	watched := make(map[string]struct{})
	for _, path := range watcher.WatchList() {
		watched[path] = struct{}{}
	}
	entries, paths := 0, 0
	var bytes int64
	err = fs.WalkDir(directory.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A vanished path cannot satisfy either file predicate. Other
			// read failures must not certify an incomplete baseline as empty.
			if errors.Is(walkErr, os.ErrNotExist) && name != "." {
				return nil
			}
			return walkErr
		}
		if fileTriggerExcluded(excludes, name) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if entries > maxFileTriggerEntries {
			return fmt.Errorf("file trigger project tree exceeds %d entries; use a smaller project root", maxFileTriggerEntries)
		}
		if entry.IsDir() {
			path := filepath.Join(root, filepath.FromSlash(name))
			if _, exists := watched[path]; !exists {
				if err := watcher.Add(path); err != nil {
					if errors.Is(err, os.ErrNotExist) && name != "." {
						return fs.SkipDir
					}
					return fmt.Errorf("watch workflow directory %q: %w", name, err)
				}
				watched[path] = struct{}{}
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !fileTriggerMatches(pattern, name) {
			return nil
		}
		info, err := directory.Lstat(filepath.FromSlash(name))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat workflow file %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		paths += len(name)
		if len(files) >= maxFileTriggerFiles || paths > maxFileTriggerPaths {
			return fmt.Errorf("file trigger baseline exceeds %d files or %d path bytes; narrow its pattern", maxFileTriggerFiles, maxFileTriggerPaths)
		}
		if info.Size() < 0 || info.Size() > maxFileTriggerBytes-bytes {
			return fmt.Errorf("file trigger baseline exceeds %d content bytes; narrow its pattern", maxFileTriggerBytes)
		}
		bytes += info.Size()
		if cached, ok := cache[name]; ok && os.SameFile(info, cached.info) && info.Size() == cached.stamp.Size && info.ModTime().Equal(cached.stamp.ModTime) {
			files[name], nextCache[name] = cached.stamp, cached
			return nil
		}
		stamp, err := readFileTriggerStamp(directory, name, info)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		files[name] = stamp
		nextCache[name] = fileDigestCache{info: info, stamp: stamp}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot workflow files: %w", err)
	}
	return files, nextCache, nil
}

func readFileTriggerStamp(directory *os.Root, name string, observed os.FileInfo) (FileTriggerStamp, error) {
	file, err := directory.Open(filepath.FromSlash(name))
	if err != nil {
		return FileTriggerStamp{}, fmt.Errorf("open workflow file %q: %w", name, err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return FileTriggerStamp{}, fmt.Errorf("stat open workflow file %q: %w", name, err)
	}
	if !before.Mode().IsRegular() || !os.SameFile(observed, before) || before.Size() != observed.Size() || !before.ModTime().Equal(observed.ModTime()) {
		return FileTriggerStamp{}, fmt.Errorf("workflow file %q changed while taking its baseline; retry when the file is stable", name)
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(file, before.Size()+1))
	if readErr != nil {
		return FileTriggerStamp{}, fmt.Errorf("hash workflow file %q: %w", name, readErr)
	}
	after, err := file.Stat()
	if err != nil {
		return FileTriggerStamp{}, fmt.Errorf("stat hashed workflow file %q: %w", name, err)
	}
	if n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return FileTriggerStamp{}, fmt.Errorf("workflow file %q changed while hashing its baseline; retry when the file is stable", name)
	}
	return FileTriggerStamp{Size: n, ModTime: after.ModTime().UTC(), SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func fileTriggerChanged(kind TriggerType, baseline, current map[string]FileTriggerStamp) bool {
	for name, after := range current {
		before, existed := baseline[name]
		if kind == TriggerFileCreated {
			if !existed {
				return true
			}
		} else if (!existed && after.Size > 0) || (existed && (before.Size != after.Size || !before.ModTime.Equal(after.ModTime) || before.SHA256 != after.SHA256)) {
			return true
		}
	}
	return false
}

// matchPathPattern matches slash-separated relative paths and gives ** its
// conventional recursive meaning. filepath.Match deliberately does not let
// * cross a path separator, so it cannot express workflow patterns such as
// internal/**/*.go by itself.
func matchPathPattern(pattern, rel string) bool {
	patternParts := strings.Split(filepath.ToSlash(pattern), "/")
	pathParts := strings.Split(filepath.ToSlash(rel), "/")
	var match func(int, int) bool
	match = func(patternIndex, pathIndex int) bool {
		if patternIndex == len(patternParts) {
			return pathIndex == len(pathParts)
		}
		if patternParts[patternIndex] == "**" {
			return match(patternIndex+1, pathIndex) ||
				(pathIndex < len(pathParts) && match(patternIndex, pathIndex+1))
		}
		if pathIndex >= len(pathParts) {
			return false
		}
		matched, err := filepath.Match(patternParts[patternIndex], pathParts[pathIndex])
		return err == nil && matched && match(patternIndex+1, pathIndex+1)
	}
	return match(0, 0)
}

func (t *fileTrigger) Check(_ *TriggerContext) (bool, error) {
	t.scanMu.Lock()
	defer t.scanMu.Unlock()
	t.mu.Lock()
	if t.err != nil {
		err := t.err
		t.mu.Unlock()
		return false, fmt.Errorf("watch filesystem: %w", err)
	}
	if t.fired {
		t.mu.Unlock()
		return true, nil
	}
	if t.directory == nil || t.watcher == nil || t.baseline == nil {
		t.mu.Unlock()
		return false, errors.New("file trigger has not been started")
	}
	directory, root, watcher := t.directory, t.root, t.watcher
	baseline, cache := t.baseline, t.cache
	// Snapshot pending empty creates as well: even a zero-byte write can be
	// recovered by its modification time after notification loss.
	pending := make(map[string]fileBaseline, len(t.pending))
	for name, baseline := range t.pending {
		pending[name] = baseline
	}
	t.mu.Unlock()

	current, nextCache, err := snapshotFileTriggerTree(directory, root, t.pattern, t.excludes, watcher, cache)
	if err != nil {
		return false, err
	}
	fired := fileTriggerChanged(t.kind, baseline, current)
	for name, baseline := range pending {
		rel, err := filepath.Rel(root, name)
		stamp, exists := current[filepath.ToSlash(rel)]
		if err == nil && exists && (stamp.Size != baseline.size || !stamp.ModTime.Equal(baseline.modTime)) {
			fired = true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return false, fmt.Errorf("watch filesystem: %w", t.err)
	}
	t.cache = nextCache
	t.fired = t.fired || fired
	for name, before := range pending {
		rel, err := filepath.Rel(root, name)
		if _, exists := current[filepath.ToSlash(rel)]; err == nil && !exists && t.pending[name] == before {
			delete(t.pending, name)
		}
	}
	return t.fired, nil
}

// fileState is also usable after Stop when rolling a failed transition back;
// stopping a watcher does not erase the last observed evidence.
func (t *fileTrigger) fileState() FileTriggerState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return FileTriggerState{
		Version: fileTriggerStateVersion, ProjectRoot: t.root, Stage: t.stage,
		TransitionIndex: t.transitionIndex, Kind: t.kind, Pattern: t.pattern,
		Excludes: slices.Clone(t.excludes),
		Baseline: cloneFileTriggerBaseline(t.baseline), Fired: t.fired,
	}
}

func (t *fileTrigger) Stop() error {
	t.scanMu.Lock()
	defer t.scanMu.Unlock()
	return t.stopLocked()
}

func (t *fileTrigger) stopLocked() error {
	t.mu.Lock()
	watcher := t.watcher
	done := t.done
	directory := t.directory
	t.watcher = nil
	t.done = nil
	t.directory = nil
	t.mu.Unlock()
	if done != nil {
		close(done)
	}
	var err error
	if watcher != nil {
		err = watcher.Close()
	}
	t.wg.Wait()
	if directory != nil {
		err = errors.Join(err, directory.Close())
	}
	if err != nil {
		return fmt.Errorf("close filesystem trigger: %w", err)
	}
	return nil
}

type commandTrigger struct {
	kind        TriggerType
	command     string
	timeout     time.Duration
	wantSuccess bool
}

func newCommandSuccessTrigger(config Trigger) (RuntimeTrigger, error) {
	return &commandTrigger{kind: TriggerCommandSuccess, command: config.Command, wantSuccess: true}, nil
}

func newCommandFailureTrigger(config Trigger) (RuntimeTrigger, error) {
	return &commandTrigger{kind: TriggerCommandFailure, command: config.Command}, nil
}

func (t *commandTrigger) Type() TriggerType { return t.kind }

func (t *commandTrigger) Start(_ *TriggerContext) error { return nil }

func (t *commandTrigger) Stop() error { return nil }

func (t *commandTrigger) Check(ctx *TriggerContext) (bool, error) {
	if ctx == nil || strings.TrimSpace(ctx.ProjectRoot) == "" {
		return false, errors.New("command trigger requires a project root")
	}
	timeout := t.timeout
	if timeout <= 0 {
		timeout = defaultCommandTriggerTimeout
	}
	parent := ctx.Context
	if parent == nil {
		parent = context.Background()
	}
	runCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	args := strings.Fields(t.command)
	if len(args) == 0 {
		return false, errors.New("command trigger requires a command")
	}
	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	cmd.Dir = ctx.ProjectRoot
	err := cmd.Run()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return false, fmt.Errorf("command trigger timed out after %s", timeout)
	}
	if runCtx.Err() != nil {
		return false, fmt.Errorf("run command trigger: %w", runCtx.Err())
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false, fmt.Errorf("run command trigger %q: %w", t.command, err)
		}
	}
	succeeded := err == nil
	return succeeded == t.wantSuccess, nil
}

// SetTimeout changes the command deadline. It is primarily useful to keep
// tests fast; production callers normally use the default timeout.
func (t *commandTrigger) SetTimeout(timeout time.Duration) { t.timeout = timeout }

type agentSaysTrigger struct {
	pattern *regexp.Regexp
	role    string
}

func newAgentSaysTrigger(config Trigger) (RuntimeTrigger, error) {
	pattern, err := regexp.Compile(config.Pattern)
	if err != nil {
		return nil, fmt.Errorf("compile agent_says pattern: %w", err)
	}
	return &agentSaysTrigger{pattern: pattern, role: config.Role}, nil
}

func (t *agentSaysTrigger) Type() TriggerType { return TriggerAgentSays }

func (t *agentSaysTrigger) Start(_ *TriggerContext) error { return nil }

func (t *agentSaysTrigger) Stop() error { return nil }

func (t *agentSaysTrigger) Check(ctx *TriggerContext) (bool, error) {
	if ctx == nil {
		return false, errors.New("agent_says trigger requires context")
	}
	for _, output := range ctx.Outputs {
		if t.role != "" && output.Role != t.role {
			continue
		}
		if t.pattern.MatchString(output.Text) {
			return true, nil
		}
	}
	return false, nil
}

type allAgentsIdleTrigger struct {
	role string
	idle time.Duration
}

func newAllAgentsIdleTrigger(config Trigger) (RuntimeTrigger, error) {
	return &allAgentsIdleTrigger{role: config.Role, idle: time.Duration(config.IdleMinutes) * time.Minute}, nil
}

func (t *allAgentsIdleTrigger) Type() TriggerType { return TriggerAllAgentsIdle }

func (t *allAgentsIdleTrigger) Start(_ *TriggerContext) error { return nil }

func (t *allAgentsIdleTrigger) Stop() error { return nil }

func (t *allAgentsIdleTrigger) Check(ctx *TriggerContext) (bool, error) {
	if ctx == nil {
		return false, errors.New("all_agents_idle trigger requires context")
	}
	matched := 0
	for _, activity := range ctx.Activities {
		if t.role != "" && activity.Role != t.role {
			continue
		}
		matched++
		if activity.LastActivity.IsZero() || ctx.now().Sub(activity.LastActivity) < t.idle {
			return false, nil
		}
	}
	return matched > 0, nil
}

// ManualTrigger moves to true after Fire. Fire is non-blocking and coalesces
// repeated requests so an operator can safely retry a manual transition.
type ManualTrigger struct {
	label string
	fired chan struct{}
}

func newManualTrigger(config Trigger) (RuntimeTrigger, error) {
	return &ManualTrigger{label: config.Label, fired: make(chan struct{}, 1)}, nil
}

func (t *ManualTrigger) Type() TriggerType { return TriggerManual }

func (t *ManualTrigger) Start(_ *TriggerContext) error { return nil }

func (t *ManualTrigger) Stop() error { return nil }

// Label identifies this manual control in a user interface.
func (t *ManualTrigger) Label() string { return t.label }

// Fire requests the transition and returns whether this call added a request.
func (t *ManualTrigger) Fire() bool {
	select {
	case t.fired <- struct{}{}:
		return true
	default:
		return false
	}
}

func (t *ManualTrigger) Check(_ *TriggerContext) (bool, error) {
	select {
	case <-t.fired:
		return true, nil
	default:
		return false, nil
	}
}

type timeElapsedTrigger struct {
	duration time.Duration
	mu       sync.Mutex
	started  time.Time
}

func newTimeElapsedTrigger(config Trigger) (RuntimeTrigger, error) {
	return &timeElapsedTrigger{duration: time.Duration(config.Minutes) * time.Minute}, nil
}

func (t *timeElapsedTrigger) Type() TriggerType { return TriggerTimeElapsed }

func (t *timeElapsedTrigger) Start(ctx *TriggerContext) error {
	t.mu.Lock()
	t.started = ctx.now()
	t.mu.Unlock()
	return nil
}

func (t *timeElapsedTrigger) Stop() error { return nil }

func (t *timeElapsedTrigger) Check(ctx *TriggerContext) (bool, error) {
	t.mu.Lock()
	started := t.started
	t.mu.Unlock()
	if started.IsZero() {
		return false, errors.New("time_elapsed trigger has not been started")
	}
	return !ctx.now().Before(started.Add(t.duration)), nil
}
