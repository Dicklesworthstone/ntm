package kernel

import (
	"context"
	"sync"
	"testing"
)

// =============================================================================
// Extended Registry Unit Tests
//
// These tests cover edge cases, concurrent access, and error conditions
// for the kernel registry following the bd-2gjig acceptance criteria.
// =============================================================================

// TestRegistryEmptyName verifies empty command names are rejected.
func TestRegistryEmptyName(t *testing.T) {
	reg := NewRegistry()

	cmd := Command{
		Name:        "",
		Description: "test",
		Category:    "test",
		Examples:    []Example{{Name: "ex1", Command: "test"}},
	}

	err := reg.Register(cmd)
	if err == nil {
		t.Fatal("expected error for empty command name")
	}
	t.Logf("input=empty_name error=%v", err)
}

// TestRegistryWhitespaceName verifies whitespace-only names are rejected.
func TestRegistryWhitespaceName(t *testing.T) {
	reg := NewRegistry()

	cmd := Command{
		Name:        "   ",
		Description: "test",
		Category:    "test",
		Examples:    []Example{{Name: "ex1", Command: "test"}},
	}

	err := reg.Register(cmd)
	if err == nil {
		t.Fatal("expected error for whitespace-only command name")
	}
	t.Logf("input=whitespace_name error=%v", err)
}

// TestRegistryMissingDescription verifies commands require descriptions.
func TestRegistryMissingDescription(t *testing.T) {
	reg := NewRegistry()

	cmd := Command{
		Name:        "test.cmd",
		Description: "",
		Category:    "test",
		Examples:    []Example{{Name: "ex1", Command: "test"}},
	}

	err := reg.Register(cmd)
	if err == nil {
		t.Fatal("expected error for missing description")
	}
	t.Logf("input=no_description error=%v", err)
}

// TestRegistryMissingCategory verifies commands require categories.
func TestRegistryMissingCategory(t *testing.T) {
	reg := NewRegistry()

	cmd := Command{
		Name:        "test.cmd",
		Description: "test description",
		Category:    "",
		Examples:    []Example{{Name: "ex1", Command: "test"}},
	}

	err := reg.Register(cmd)
	if err == nil {
		t.Fatal("expected error for missing category")
	}
	t.Logf("input=no_category error=%v", err)
}

// TestRegistryMissingExamples verifies commands require at least one example.
func TestRegistryMissingExamples(t *testing.T) {
	reg := NewRegistry()

	cmd := Command{
		Name:        "test.cmd",
		Description: "test description",
		Category:    "test",
		Examples:    []Example{},
	}

	err := reg.Register(cmd)
	if err == nil {
		t.Fatal("expected error for missing examples")
	}
	t.Logf("input=no_examples error=%v", err)
}

// TestRegistryRESTBindingMissingMethod verifies REST requires method.
func TestRegistryRESTBindingMissingMethod(t *testing.T) {
	reg := NewRegistry()

	cmd := testCommand("test.rest")
	cmd.REST = &RESTBinding{
		Method: "",
		Path:   "/api/test",
	}

	err := reg.Register(cmd)
	if err == nil {
		t.Fatal("expected error for REST binding missing method")
	}
	t.Logf("input=rest_no_method error=%v", err)
}

// TestRegistryRESTBindingMissingPath verifies REST requires path.
func TestRegistryRESTBindingMissingPath(t *testing.T) {
	reg := NewRegistry()

	cmd := testCommand("test.rest")
	cmd.REST = &RESTBinding{
		Method: "GET",
		Path:   "",
	}

	err := reg.Register(cmd)
	if err == nil {
		t.Fatal("expected error for REST binding missing path")
	}
	t.Logf("input=rest_no_path error=%v", err)
}

// TestRegistryRESTMethodCaseInsensitive verifies REST method conflicts are case-insensitive.
func TestRegistryRESTMethodCaseInsensitive(t *testing.T) {
	reg := NewRegistry()

	cmd1 := testCommand("test.first")
	cmd1.REST = &RESTBinding{Method: "GET", Path: "/api/test"}
	if err := reg.Register(cmd1); err != nil {
		t.Fatalf("first register failed: %v", err)
	}

	cmd2 := testCommand("test.second")
	cmd2.REST = &RESTBinding{Method: "get", Path: "/api/test"} // lowercase

	err := reg.Register(cmd2)
	if err == nil {
		t.Fatal("expected REST conflict for case-insensitive method match")
	}
	t.Logf("input=get_vs_GET error=%v", err)
}

// TestRegistryGetNotFound verifies Get returns false for unknown commands.
func TestRegistryGetNotFound(t *testing.T) {
	reg := NewRegistry()

	_, ok := reg.Get("nonexistent")
	if ok {
		t.Fatal("expected false for nonexistent command")
	}
	t.Logf("input=nonexistent found=%v", ok)
}

// TestRegistryListEmpty verifies List returns nil for empty registry.
func TestRegistryListEmpty(t *testing.T) {
	reg := NewRegistry()

	list := reg.List()
	if list != nil {
		t.Fatalf("expected nil for empty registry, got %v", list)
	}
	t.Logf("input=empty_registry list_len=%v", len(list))
}

// TestRegistryHandlerDuplicateRegistration verifies duplicate handler registration is rejected.
func TestRegistryHandlerDuplicateRegistration(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("test.handler")

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register command failed: %v", err)
	}

	handler := func(ctx context.Context, input any) (any, error) {
		return "ok", nil
	}

	if err := reg.RegisterHandler(cmd.Name, handler); err != nil {
		t.Fatalf("first handler registration failed: %v", err)
	}

	err := reg.RegisterHandler(cmd.Name, handler)
	if err == nil {
		t.Fatal("expected error for duplicate handler registration")
	}
	t.Logf("input=duplicate_handler error=%v", err)
}

// TestRegistryHandlerNil verifies nil handlers are rejected.
func TestRegistryHandlerNil(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("test.nil")

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register command failed: %v", err)
	}

	err := reg.RegisterHandler(cmd.Name, nil)
	if err == nil {
		t.Fatal("expected error for nil handler")
	}
	t.Logf("input=nil_handler error=%v", err)
}

// TestRegistryHandlerEmptyName verifies empty handler names are rejected.
func TestRegistryHandlerEmptyName(t *testing.T) {
	reg := NewRegistry()

	handler := func(ctx context.Context, input any) (any, error) {
		return "ok", nil
	}

	err := reg.RegisterHandler("", handler)
	if err == nil {
		t.Fatal("expected error for empty handler name")
	}
	t.Logf("input=empty_handler_name error=%v", err)
}

// TestRegistryRunEmptyName verifies Run rejects empty command names.
func TestRegistryRunEmptyName(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.Run(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error for empty command name in Run")
	}
	t.Logf("input=empty_run_name error=%v", err)
}

// TestRegistryRunNilContext verifies Run handles nil context gracefully.
func TestRegistryRunNilContext(t *testing.T) {
	reg := NewRegistry()
	cmd := testCommand("test.ctx")

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	if err := reg.RegisterHandler(cmd.Name, func(ctx context.Context, input any) (any, error) {
		if ctx == nil {
			t.Error("expected non-nil context from Run")
		}
		return "ok", nil
	}); err != nil {
		t.Fatalf("register handler failed: %v", err)
	}

	// Run with nil context should not panic
	out, err := reg.Run(nil, cmd.Name, nil)
	if err != nil {
		t.Fatalf("Run with nil context failed: %v", err)
	}
	t.Logf("input=nil_ctx output=%v", out)
}

// TestRegistryConcurrentAccess verifies registry is safe for concurrent use.
func TestRegistryConcurrentAccess(t *testing.T) {
	reg := NewRegistry()

	// Register base commands
	for i := 0; i < 10; i++ {
		cmd := testCommand("concurrent." + string(rune('a'+i)))
		if err := reg.Register(cmd); err != nil {
			t.Fatalf("register %d failed: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.List()
			_, _ = reg.Get("concurrent.a")
		}()
	}

	// Wait for completion
	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Errorf("concurrent operation failed: %v", err)
	}

	t.Logf("concurrent_reads=50 commands=%d", len(reg.List()))
}

// TestRegistrySafetyLevels verifies safety levels are preserved.
func TestRegistrySafetyLevels(t *testing.T) {
	reg := NewRegistry()

	tests := []struct {
		name  string
		level SafetyLevel
	}{
		{"test.safe", SafetySafe},
		{"test.caution", SafetyCaution},
		{"test.danger", SafetyDanger},
	}

	for _, tt := range tests {
		cmd := testCommand(tt.name)
		cmd.SafetyLevel = tt.level

		if err := reg.Register(cmd); err != nil {
			t.Fatalf("register %s failed: %v", tt.name, err)
		}

		got, ok := reg.Get(tt.name)
		if !ok {
			t.Fatalf("command %s not found", tt.name)
		}
		if got.SafetyLevel != tt.level {
			t.Errorf("command %s: safety_level=%v, want %v", tt.name, got.SafetyLevel, tt.level)
		}
		t.Logf("command=%s safety_level=%s", tt.name, got.SafetyLevel)
	}
}

// TestRegistryIdempotentFlag verifies idempotent flag is preserved.
func TestRegistryIdempotentFlag(t *testing.T) {
	reg := NewRegistry()

	cmd := testCommand("test.idempotent")
	cmd.Idempotent = true

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	got, ok := reg.Get(cmd.Name)
	if !ok {
		t.Fatal("command not found")
	}
	if !got.Idempotent {
		t.Error("idempotent flag not preserved")
	}
	t.Logf("command=%s idempotent=%v", cmd.Name, got.Idempotent)
}

// TestRegistryEmitsEvents verifies emits_events is preserved.
func TestRegistryEmitsEvents(t *testing.T) {
	reg := NewRegistry()

	cmd := testCommand("test.events")
	cmd.EmitsEvents = []string{"session.created", "pane.output"}

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	got, ok := reg.Get(cmd.Name)
	if !ok {
		t.Fatal("command not found")
	}
	if len(got.EmitsEvents) != 2 {
		t.Errorf("emits_events=%v, want 2 events", got.EmitsEvents)
	}
	t.Logf("command=%s emits_events=%v", cmd.Name, got.EmitsEvents)
}

// TestRegistrySchemaRefs verifies input/output schemas are preserved.
func TestRegistrySchemaRefs(t *testing.T) {
	reg := NewRegistry()

	cmd := testCommand("test.schemas")
	cmd.Input = &SchemaRef{
		Name:        "SpawnInput",
		Ref:         "#/components/schemas/SpawnInput",
		Description: "spawn request parameters",
	}
	cmd.Output = &SchemaRef{
		Name:        "SpawnOutput",
		Ref:         "#/components/schemas/SpawnOutput",
		Description: "spawn response",
	}

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	got, ok := reg.Get(cmd.Name)
	if !ok {
		t.Fatal("command not found")
	}

	if got.Input == nil {
		t.Error("input schema not preserved")
	} else if got.Input.Name != "SpawnInput" {
		t.Errorf("input.name=%s, want SpawnInput", got.Input.Name)
	}

	if got.Output == nil {
		t.Error("output schema not preserved")
	} else if got.Output.Name != "SpawnOutput" {
		t.Errorf("output.name=%s, want SpawnOutput", got.Output.Name)
	}

	t.Logf("command=%s input=%s output=%s", cmd.Name, got.Input.Name, got.Output.Name)
}

// TestRegistryMultipleExamples verifies multiple examples are preserved.
func TestRegistryMultipleExamples(t *testing.T) {
	reg := NewRegistry()

	cmd := Command{
		Name:        "test.examples",
		Description: "test multiple examples",
		Category:    "test",
		Examples: []Example{
			{Name: "basic", Command: "ntm test basic"},
			{Name: "advanced", Command: "ntm test --flag=value", Description: "with options"},
			{Name: "full", Command: "ntm test --all", Input: `{"full": true}`, Output: `{"ok": true}`},
		},
	}

	if err := reg.Register(cmd); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	got, ok := reg.Get(cmd.Name)
	if !ok {
		t.Fatal("command not found")
	}

	if len(got.Examples) != 3 {
		t.Errorf("examples count=%d, want 3", len(got.Examples))
	}

	// Verify example content
	if got.Examples[0].Name != "basic" {
		t.Errorf("examples[0].name=%s, want basic", got.Examples[0].Name)
	}
	if got.Examples[2].Output != `{"ok": true}` {
		t.Errorf("examples[2].output=%s, want JSON", got.Examples[2].Output)
	}

	t.Logf("command=%s example_count=%d", cmd.Name, len(got.Examples))
}

// TestRESTKeyFunction verifies restKey handles edge cases.
func TestRESTKeyFunction(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{"GET", "/api/test", "GET /api/test"},
		{"get", "/api/test", "GET /api/test"},
		{"  POST  ", "/api/test", "POST /api/test"},
		{"", "/api/test", ""},
		{"GET", "", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		got := restKey(tt.method, tt.path)
		if got != tt.want {
			t.Errorf("restKey(%q, %q) = %q, want %q", tt.method, tt.path, got, tt.want)
		}
	}
}

// TestValidateCommandFunction verifies validateCommand catches all errors.
func TestValidateCommandFunction(t *testing.T) {
	tests := []struct {
		name    string
		cmd     Command
		wantErr bool
	}{
		{
			name:    "valid",
			cmd:     testCommand("test.valid"),
			wantErr: false,
		},
		{
			name: "missing_name",
			cmd: Command{
				Description: "test",
				Category:    "test",
				Examples:    []Example{{Name: "ex", Command: "cmd"}},
			},
			wantErr: true,
		},
		{
			name: "missing_description",
			cmd: Command{
				Name:     "test.nodesc",
				Category: "test",
				Examples: []Example{{Name: "ex", Command: "cmd"}},
			},
			wantErr: true,
		},
		{
			name: "missing_category",
			cmd: Command{
				Name:        "test.nocat",
				Description: "test",
				Examples:    []Example{{Name: "ex", Command: "cmd"}},
			},
			wantErr: true,
		},
		{
			name: "missing_examples",
			cmd: Command{
				Name:        "test.noex",
				Description: "test",
				Category:    "test",
			},
			wantErr: true,
		},
		{
			name: "rest_missing_method",
			cmd: Command{
				Name:        "test.restnomethod",
				Description: "test",
				Category:    "test",
				Examples:    []Example{{Name: "ex", Command: "cmd"}},
				REST:        &RESTBinding{Path: "/api/test"},
			},
			wantErr: true,
		},
		{
			name: "rest_missing_path",
			cmd: Command{
				Name:        "test.restnopath",
				Description: "test",
				Category:    "test",
				Examples:    []Example{{Name: "ex", Command: "cmd"}},
				REST:        &RESTBinding{Method: "GET"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCommand(tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
			t.Logf("command=%s wantErr=%v gotErr=%v", tt.name, tt.wantErr, err != nil)
		})
	}
}

// =============================================================================
// Global wrapper function tests (defaultRegistry)
// =============================================================================

func TestRegister_Global(t *testing.T) {
	// Not parallel: modifies package-level defaultRegistry
	origRegistry := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = origRegistry })

	cmd := testCommand("global-test-cmd")
	if err := Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := Get("global-test-cmd")
	if !ok {
		t.Fatal("expected to find registered command")
	}
	if got.Name != "global-test-cmd" {
		t.Errorf("Name = %q, want global-test-cmd", got.Name)
	}
}

func TestMustRegister_Global(t *testing.T) {
	origRegistry := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = origRegistry })

	cmd := testCommand("must-register-cmd")
	// Should not panic
	MustRegister(cmd)

	_, ok := Get("must-register-cmd")
	if !ok {
		t.Error("expected to find must-registered command")
	}
}

func TestMustRegister_PanicsOnDuplicate(t *testing.T) {
	origRegistry := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = origRegistry })

	cmd := testCommand("dup-cmd")
	MustRegister(cmd)

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate MustRegister")
		}
	}()
	MustRegister(cmd) // Should panic
}

func TestList_Global(t *testing.T) {
	origRegistry := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = origRegistry })

	Register(testCommand("cmd-a"))
	Register(testCommand("cmd-b"))

	cmds := List()
	if len(cmds) != 2 {
		t.Errorf("List() returned %d commands, want 2", len(cmds))
	}
}

func TestRegisterHandler_Global(t *testing.T) {
	origRegistry := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = origRegistry })

	Register(testCommand("handler-cmd"))
	if err := RegisterHandler("handler-cmd", func(ctx context.Context, input any) (any, error) {
		return "result", nil
	}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}

	result, err := Run(context.Background(), "handler-cmd", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "result" {
		t.Errorf("Run result = %v, want 'result'", result)
	}
}

func TestMustRegisterHandler_Global(t *testing.T) {
	origRegistry := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = origRegistry })

	Register(testCommand("must-handler"))
	MustRegisterHandler("must-handler", func(ctx context.Context, input any) (any, error) {
		return "ok", nil
	})

	result, err := Run(context.Background(), "must-handler", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %v, want 'ok'", result)
	}
}

func TestRun_NotFound(t *testing.T) {
	origRegistry := defaultRegistry
	defaultRegistry = NewRegistry()
	t.Cleanup(func() { defaultRegistry = origRegistry })

	_, err := Run(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("expected error for non-existent command")
	}
}
