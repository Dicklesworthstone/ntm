package kernel

import (
	"context"
	"testing"
)

func testCommand(name string) Command {
	return Command{
		Name:        name,
		Description: "test command",
		Category:    "test",
		Examples: []Example{
			{
				Name:    "basic",
				Command: "ntm test",
			},
		},
	}
}

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("kernel.list")

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	got, ok := registryGet(reg, cmd.Name)
	if !ok {
		t.Fatalf("expected command to be found")
	}
	if got.Name != cmd.Name {
		t.Fatalf("expected name %q, got %q", cmd.Name, got.Name)
	}
}

func TestRegistryDuplicateName(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("dup")

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := reg.Register(cmd); err == nil {
		t.Fatalf("expected duplicate name error")
	}
}

func TestRegistryRestConflict(t *testing.T) {
	reg := NewRegistry()

	first := testCommand("cmd.one")
	first.REST = &RESTBinding{Method: "GET", Path: "/api/test"}
	if err := reg.Register(first); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	second := testCommand("cmd.two")
	second.REST = &RESTBinding{Method: "GET", Path: "/api/test"}
	if err := reg.Register(second); err == nil {
		t.Fatalf("expected REST conflict error")
	}
}

func TestRegistryListDeterministic(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(testCommand("bravo")); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := reg.Register(testCommand("alpha")); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(list))
	}
	if list[0].Name != "alpha" || list[1].Name != "bravo" {
		t.Fatalf("expected deterministic ordering, got %q then %q", list[0].Name, list[1].Name)
	}
}

func TestRegistryValidation(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Command{}); err == nil {
		t.Fatalf("expected validation error for empty command")
	}
}

func TestRegistryRegisterHandlerUnknownCommand(t *testing.T) {
	reg := NewRegistry()
	err := reg.RegisterHandler("missing", func(context.Context, any) (any, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatalf("expected error registering handler for unknown command")
	}
}

func TestRegistryRunHandler(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("kernel.list")

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	if err := reg.RegisterHandler(cmd.Name, func(ctx context.Context, input any) (any, error) {
		if input != "ping" {
			t.Fatalf("expected input 'ping', got %v", input)
		}
		return "ok", nil
	}); err != nil {
		t.Fatalf("register handler failed: %v", err)
	}

	out, err := reg.Run(context.Background(), cmd.Name, "ping")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("expected output ok, got %v", out)
	}
}

func TestRegistryRunMissingHandler(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("kernel.list")

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	if _, err := reg.Run(context.Background(), cmd.Name, nil); err == nil {
		t.Fatalf("expected error for missing handler")
	}
}

// registryGet looks up a command by name through the live List API.
func registryGet(r *Registry, name string) (Command, bool) {
	for _, cmd := range r.List() {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return Command{}, false
}

// globalGet looks up a command in the default registry through the live List API.
func globalGet(name string) (Command, bool) {
	for _, cmd := range List() {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return Command{}, false
}
