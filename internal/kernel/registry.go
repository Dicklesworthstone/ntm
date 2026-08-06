package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// HandlerFunc executes a command with the provided input.
type HandlerFunc func(ctx context.Context, input any) (any, error)

// Registry stores command metadata for CLI/TUI/REST surfaces.
type Registry struct {
	mu        sync.RWMutex
	commands  map[string]Command
	restIndex map[string]string
	handlers  map[string]HandlerFunc
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		commands:  make(map[string]Command),
		restIndex: make(map[string]string),
		handlers:  make(map[string]HandlerFunc),
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

// Unregister removes a command and its associated handler from the registry.
// It returns false when the command was not registered.
func (r *Registry) Unregister(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	cmd, exists := r.commands[name]
	if !exists {
		return false
	}

	delete(r.commands, name)
	delete(r.handlers, name)
	if cmd.REST != nil {
		key := restKey(cmd.REST.Method, cmd.REST.Path)
		if r.restIndex[key] == name {
			delete(r.restIndex, key)
		}
	}

	return true
}

// RegisterHandler associates a handler with a registered command.
func (r *Registry) RegisterHandler(name string, handler HandlerFunc) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("command name is required")
	}
	if handler == nil {
		return fmt.Errorf("handler for %q is nil", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.commands[name]; !exists {
		err := fmt.Errorf("command %q not registered", name)
		slog.Error("kernel handler registration failed", "command", name, "error", err)
		return err
	}
	if _, exists := r.handlers[name]; exists {
		err := fmt.Errorf("handler for %q already registered", name)
		slog.Error("kernel handler registration failed", "command", name, "error", err)
		return err
	}

	r.handlers[name] = handler
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

// Run executes a registered command handler.
func (r *Registry) Run(ctx context.Context, name string, input any) (any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("command name is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.RLock()
	handler, ok := r.handlers[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("handler for %q not registered", name)
	}

	return handler(ctx, input)
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

// Unregister removes a command from the default registry.
// It returns false when the command was not registered.
func Unregister(name string) bool {
	return defaultRegistry.Unregister(name)
}

// MustRegister registers a command or panics on failure.
func MustRegister(cmd Command) {
	if err := Register(cmd); err != nil {
		panic(err)
	}
}

// RegisterHandler registers a handler for a command in the default registry.
func RegisterHandler(name string, handler HandlerFunc) error {
	return defaultRegistry.RegisterHandler(name, handler)
}

// MustRegisterHandler registers a handler or panics on failure.
func MustRegisterHandler(name string, handler HandlerFunc) {
	if err := RegisterHandler(name, handler); err != nil {
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

// Run executes a handler in the default registry.
func Run(ctx context.Context, name string, input any) (any, error) {
	return defaultRegistry.Run(ctx, name, input)
}
