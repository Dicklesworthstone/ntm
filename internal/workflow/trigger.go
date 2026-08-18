package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// fileBaseline is the size/mtime of a just-created file, recorded so a write
// that the notification backend never reports can still be detected by stat.
type fileBaseline struct {
	size    int64
	modTime time.Time
}

type fileTrigger struct {
	kind    TriggerType
	pattern string
	op      fsnotify.Op

	mu      sync.Mutex
	root    string
	fired   bool
	err     error
	watcher *fsnotify.Watcher
	done    chan struct{}
	wg      sync.WaitGroup
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
	root, err := filepath.Abs(ctx.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create filesystem watcher: %w", err)
	}
	if err := addDirectoryTree(watcher, root); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("watch project tree: %w", err)
	}

	if err := t.Stop(); err != nil {
		_ = watcher.Close()
		return err
	}
	t.mu.Lock()
	t.root = root
	t.fired = false
	t.err = nil
	t.pending = make(map[string]fileBaseline)
	t.watcher = watcher
	t.done = make(chan struct{})
	done := t.done
	t.wg.Add(1)
	t.mu.Unlock()

	go t.run(watcher, done)
	return nil
}

func (t *fileTrigger) run(watcher *fsnotify.Watcher, done <-chan struct{}) {
	defer t.wg.Done()
	for {
		select {
		case <-done:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create != 0 {
				info, statErr := os.Stat(event.Name)
				if statErr == nil && info.IsDir() {
					if err := addDirectoryTree(watcher, event.Name); err != nil {
						t.mu.Lock()
						t.err = fmt.Errorf("watch created directory %q: %w", event.Name, err)
						t.mu.Unlock()
					}
				}
				// kqueue write-loss fallback: fsnotify registers a brand-new
				// file's own watch asynchronously after its Create event, so a
				// write landing immediately after creation can produce no Write
				// event at all. For write triggers, treat a matching file that
				// already has content as written, and remember empty ones so
				// Check can detect the write by stat.
				if t.op&fsnotify.Write != 0 && statErr == nil && info.Mode().IsRegular() && t.matches(event.Name) {
					t.mu.Lock()
					if info.Size() > 0 {
						t.fired = true
					} else if t.pending != nil {
						t.pending[event.Name] = fileBaseline{size: info.Size(), modTime: info.ModTime()}
					}
					t.mu.Unlock()
				}
			}
			if event.Op&t.op == 0 || !t.matches(event.Name) {
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
	t.mu.Unlock()
	rel, err := filepath.Rel(root, name)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	if matchPathPattern(pattern, rel) {
		return true
	}
	matched, _ := filepath.Match(pattern, filepath.Base(rel))
	return matched
}

// addDirectoryTree registers every existing directory beneath root. fsnotify
// watches directories individually, so watching only the project root misses
// events produced in nested source trees.
func addDirectoryTree(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			return fmt.Errorf("watch %q: %w", path, err)
		}
		return nil
	})
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
	// Stat fallback for writes whose notification was lost (see run): compare
	// each pending created file against its creation-time baseline.
	pending := make(map[string]fileBaseline, len(t.pending))
	for name, baseline := range t.pending {
		pending[name] = baseline
	}
	t.mu.Unlock()

	fired := false
	var settled []string
	for name, baseline := range pending {
		info, err := os.Stat(name)
		if err != nil {
			// Removed (or unreadable): a future rewrite produces a fresh
			// Create event, so stop tracking this instance.
			settled = append(settled, name)
			continue
		}
		if info.Size() != baseline.size || info.ModTime().After(baseline.modTime) {
			fired = true
			settled = append(settled, name)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if fired {
		t.fired = true
	}
	for _, name := range settled {
		delete(t.pending, name)
	}
	return t.fired, nil
}

func (t *fileTrigger) Stop() error {
	t.mu.Lock()
	watcher := t.watcher
	done := t.done
	t.watcher = nil
	t.done = nil
	t.mu.Unlock()
	if done != nil {
		close(done)
	}
	if watcher != nil {
		if err := watcher.Close(); err != nil {
			return fmt.Errorf("close filesystem watcher: %w", err)
		}
	}
	t.wg.Wait()
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
