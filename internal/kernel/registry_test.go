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

	got, ok := reg.Get(cmd.Name)
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

func TestRegistryUnregisterRemovesCommandHandlerAndRESTBinding(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("cmd.one")
	cmd.REST = &RESTBinding{Method: "GET", Path: "/api/test"}
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := reg.RegisterHandler(cmd.Name, func(context.Context, any) (any, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("register handler failed: %v", err)
	}

	if !reg.Unregister(cmd.Name) {
		t.Fatal("Unregister returned false for registered command")
	}
	if _, ok := reg.Get(cmd.Name); ok {
		t.Fatal("unregistered command is still available")
	}
	if _, err := reg.Run(context.Background(), cmd.Name, nil); err == nil {
		t.Fatal("unregistered command handler is still available")
	}
	if reg.Unregister(cmd.Name) {
		t.Fatal("Unregister returned true for absent command")
	}

	replacement := testCommand("cmd.two")
	replacement.REST = &RESTBinding{Method: "GET", Path: "/api/test"}
	if err := reg.Register(replacement); err != nil {
		t.Fatalf("register replacement after unregister: %v", err)
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
