package kernel

import "testing"

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
