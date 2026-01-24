package kernel

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// Registry stores command metadata for CLI/TUI/REST surfaces.
type Registry struct {
	mu        sync.RWMutex
	commands  map[string]Command
	restIndex map[string]string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		commands:  make(map[string]Command),
		restIndex: make(map[string]string),
	}
}

// Register adds a command to the registry with validation.
func (r *Registry) Register(cmd Command) error {
	if err := validateCommand(cmd); err != nil {
		logRegisterError(cmd, err)
		return err
	}

	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		err := fmt.Errorf("command name cannot be empty")
		logRegisterError(cmd, err)
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.commands[name]; exists {
		err := fmt.Errorf("command %q already registered", name)
		logRegisterError(cmd, err)
		return err
	}

	if cmd.REST != nil {
		key := restKey(cmd.REST.Method, cmd.REST.Path)
		if key == "" {
			err := fmt.Errorf("REST binding requires method and path")
			logRegisterError(cmd, err)
			return err
		}
		if existing, exists := r.restIndex[key]; exists {
			err := fmt.Errorf("REST binding conflict: %s already used by %s", key, existing)
			logRegisterError(cmd, err)
			return err
		}
		r.restIndex[key] = name
	}

	r.commands[name] = cmd
	return nil
}

// Get returns a command by name.
func (r *Registry) Get(name string) (Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cmd, ok := r.commands[name]
	return cmd, ok
}

// List returns all commands in deterministic order.
func (r *Registry) List() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.commands) == 0 {
		return nil
	}

	names := make([]string, 0, len(r.commands))
	for name := range r.commands {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Command, 0, len(names))
	for _, name := range names {
		out = append(out, r.commands[name])
	}
	return out
}

func validateCommand(cmd Command) error {
	if strings.TrimSpace(cmd.Name) == "" {
		return fmt.Errorf("command name is required")
	}
	if strings.TrimSpace(cmd.Description) == "" {
		return fmt.Errorf("command description is required")
	}
	if strings.TrimSpace(cmd.Category) == "" {
		return fmt.Errorf("command category is required")
	}
	if len(cmd.Examples) == 0 {
		return fmt.Errorf("at least one example is required")
	}
	if cmd.REST != nil {
		if strings.TrimSpace(cmd.REST.Method) == "" || strings.TrimSpace(cmd.REST.Path) == "" {
			return fmt.Errorf("REST binding requires method and path")
		}
	}
	return nil
}

func restKey(method, path string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	if method == "" || path == "" {
		return ""
	}
	return method + " " + path
}

func logRegisterError(cmd Command, err error) {
	method := ""
	path := ""
	if cmd.REST != nil {
		method = cmd.REST.Method
		path = cmd.REST.Path
	}
	slog.Error("kernel command registration failed",
		"command", cmd.Name,
		"method", method,
		"path", path,
		"error", err,
	)
}

var defaultRegistry = NewRegistry()

// Register adds a command to the default registry.
func Register(cmd Command) error {
	return defaultRegistry.Register(cmd)
}

// MustRegister registers a command or panics on failure.
func MustRegister(cmd Command) {
	if err := Register(cmd); err != nil {
		panic(err)
	}
}

// Get returns a command from the default registry.
func Get(name string) (Command, bool) {
	return defaultRegistry.Get(name)
}

// List returns all commands from the default registry.
func List() []Command {
	return defaultRegistry.List()
}
