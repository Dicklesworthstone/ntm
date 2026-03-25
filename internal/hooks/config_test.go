package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAllCommandEvents(t *testing.T) {
	events := AllCommandEvents()
	if len(events) != 12 {
		t.Errorf("expected 12 command events, got %d", len(events))
	}

	// Check that expected events are present
	expected := []CommandEvent{
		EventPreSpawn, EventPostSpawn,
		EventPreSend, EventPostSend,
		EventPreAdd, EventPostAdd,
		EventPreCreate, EventPostCreate,
		EventPreKill, EventPostKill,
		EventPreShutdown, EventPostShutdown,
	}
	for _, e := range expected {
		found := false
		for _, actual := range events {
			if e == actual {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing expected event: %s", e)
		}
	}
}

func TestIsValidCommandEvent(t *testing.T) {
	tests := []struct {
		event string
		want  bool
	}{
		{"pre-spawn", true},
		{"post-spawn", true},
		{"pre-send", true},
		{"post-send", true},
		{"pre-add", true},
		{"post-add", true},
		{"pre-create", true},
		{"post-create", true},
		{"pre-kill", true},
		{"post-kill", true},
		{"pre-shutdown", true},
		{"post-shutdown", true},
		{"invalid", false},
		{"pre-commit", false}, // This is a git hook, not a command hook
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got := IsValidCommandEvent(tt.event)
			if got != tt.want {
				t.Errorf("IsValidCommandEvent(%q) = %v, want %v", tt.event, got, tt.want)
			}
		})
	}
}

func TestCommandHookValidate(t *testing.T) {
	tests := []struct {
		name    string
		hook    CommandHook
		wantErr bool
	}{
		{
			name: "valid hook",
			hook: CommandHook{
				Event:   EventPreSpawn,
				Command: "echo hello",
			},
			wantErr: false,
		},
		{
			name: "empty command",
			hook: CommandHook{
				Event:   EventPreSpawn,
				Command: "",
			},
			wantErr: true,
		},
		{
			name: "whitespace command",
			hook: CommandHook{
				Event:   EventPreSpawn,
				Command: "   \t  ",
			},
			wantErr: true,
		},
		{
			name: "invalid event",
			hook: CommandHook{
				Event:   CommandEvent("invalid-event"),
				Command: "echo hello",
			},
			wantErr: true,
		},
		{
			name: "negative timeout treated as default",
			hook: CommandHook{
				Event:   EventPreSpawn,
				Command: "echo hello",
				Timeout: Duration(-1 * time.Second),
			},
			wantErr: false, // Negative is treated as "use default"
		},
		{
			name: "excessive timeout",
			hook: CommandHook{
				Event:   EventPreSpawn,
				Command: "echo hello",
				Timeout: Duration(15 * time.Minute),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.hook.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCommandHookGetTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout Duration
		want    time.Duration
	}{
		{"zero uses default", 0, CommandHookDefaults.Timeout},
		{"custom timeout", Duration(60 * time.Second), 60 * time.Second},
		{"negative uses default", Duration(-1 * time.Second), CommandHookDefaults.Timeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := CommandHook{Timeout: tt.timeout}
			got := h.GetTimeout()
			if got != tt.want {
				t.Errorf("GetTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCommandHookIsEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{"nil uses default (true)", nil, true},
		{"explicitly true", &trueVal, true},
		{"explicitly false", &falseVal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := CommandHook{Enabled: tt.enabled}
			got := h.IsEnabled()
			if got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCommandHooksConfigGetHooksForEvent(t *testing.T) {
	trueVal := true
	falseVal := false

	cfg := &CommandHooksConfig{
		Hooks: []CommandHook{
			{Event: EventPreSpawn, Command: "echo 1", Enabled: &trueVal},
			{Event: EventPreSpawn, Command: "echo 2", Enabled: &trueVal},
			{Event: EventPreSpawn, Command: "echo disabled", Enabled: &falseVal},
			{Event: EventPostSpawn, Command: "echo post", Enabled: &trueVal},
		},
	}

	hooks := cfg.GetHooksForEvent(EventPreSpawn)
	if len(hooks) != 2 {
		t.Errorf("GetHooksForEvent(EventPreSpawn) returned %d hooks, want 2", len(hooks))
	}

	hooks = cfg.GetHooksForEvent(EventPostSpawn)
	if len(hooks) != 1 {
		t.Errorf("GetHooksForEvent(EventPostSpawn) returned %d hooks, want 1", len(hooks))
	}

	hooks = cfg.GetHooksForEvent(EventPreSend)
	if len(hooks) != 0 {
		t.Errorf("GetHooksForEvent(EventPreSend) returned %d hooks, want 0", len(hooks))
	}
}

func TestCommandHooksConfigHasHooksForEvent(t *testing.T) {
	trueVal := true
	falseVal := false

	cfg := &CommandHooksConfig{
		Hooks: []CommandHook{
			{Event: EventPreSpawn, Command: "echo 1", Enabled: &trueVal},
			{Event: EventPostSpawn, Command: "echo disabled", Enabled: &falseVal},
		},
	}

	if !cfg.HasHooksForEvent(EventPreSpawn) {
		t.Error("HasHooksForEvent(EventPreSpawn) should return true")
	}
	if cfg.HasHooksForEvent(EventPostSpawn) {
		t.Error("HasHooksForEvent(EventPostSpawn) should return false (disabled)")
	}
	if cfg.HasHooksForEvent(EventPreSend) {
		t.Error("HasHooksForEvent(EventPreSend) should return false (no hooks)")
	}
}

func TestLoadCommandHooksFromTOML(t *testing.T) {
	toml := `
[[command_hooks]]
event = "pre-spawn"
command = "git stash"
timeout = "30s"
description = "Stash changes before spawning"

[[command_hooks]]
event = "post-send"
command = "notify-send 'Message sent'"
continue_on_error = true
`

	cfg, err := LoadCommandHooksFromTOML(toml)
	if err != nil {
		t.Fatalf("LoadCommandHooksFromTOML() error = %v", err)
	}

	if len(cfg.Hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(cfg.Hooks))
	}

	// Check first hook
	h1 := cfg.Hooks[0]
	if h1.Event != EventPreSpawn {
		t.Errorf("hook[0].Event = %s, want %s", h1.Event, EventPreSpawn)
	}
	if h1.Command != "git stash" {
		t.Errorf("hook[0].Command = %s, want 'git stash'", h1.Command)
	}
	if h1.GetTimeout() != 30*time.Second {
		t.Errorf("hook[0].GetTimeout() = %v, want 30s", h1.GetTimeout())
	}

	// Check second hook
	h2 := cfg.Hooks[1]
	if h2.Event != EventPostSend {
		t.Errorf("hook[1].Event = %s, want %s", h2.Event, EventPostSend)
	}
	if !h2.ContinueOnError {
		t.Error("hook[1].ContinueOnError should be true")
	}
}

func TestLoadCommandHooksFromTOMLInvalid(t *testing.T) {
	tests := []struct {
		name string
		toml string
	}{
		{
			name: "invalid event",
			toml: `[[command_hooks]]
event = "invalid-event"
command = "echo hello"`,
		},
		{
			name: "empty command",
			toml: `[[command_hooks]]
event = "pre-spawn"
command = ""`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadCommandHooksFromTOML(tt.toml)
			if err == nil {
				t.Error("LoadCommandHooksFromTOML() should return error")
			}
		})
	}
}

func TestLoadCommandHooksNonExistentFile(t *testing.T) {
	cfg, err := LoadCommandHooks("/nonexistent/path/hooks.toml")
	if err != nil {
		t.Fatalf("LoadCommandHooks() should not error for nonexistent file: %v", err)
	}
	if len(cfg.Hooks) != 0 {
		t.Errorf("expected empty config, got %d hooks", len(cfg.Hooks))
	}
}

func TestLoadCommandHooksFromFile(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	hooksPath := filepath.Join(tmpDir, "hooks.toml")

	content := `
[[command_hooks]]
event = "pre-spawn"
command = "echo 'preparing to spawn'"
timeout = "10s"
`
	if err := os.WriteFile(hooksPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := LoadCommandHooks(hooksPath)
	if err != nil {
		t.Fatalf("LoadCommandHooks() error = %v", err)
	}

	if len(cfg.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.Hooks))
	}

	h := cfg.Hooks[0]
	if h.Event != EventPreSpawn {
		t.Errorf("hook.Event = %s, want %s", h.Event, EventPreSpawn)
	}
	if h.GetTimeout() != 10*time.Second {
		t.Errorf("hook.GetTimeout() = %v, want 10s", h.GetTimeout())
	}
}

func TestCommandHookExpandWorkDir(t *testing.T) {
	tests := []struct {
		name        string
		workDir     string
		sessionName string
		projectDir  string
		wantSuffix  string // Check that result ends with this
	}{
		{
			name:       "empty uses project dir",
			workDir:    "",
			projectDir: "/home/user/project",
			wantSuffix: "/home/user/project",
		},
		{
			name:        "session variable",
			workDir:     "/projects/${SESSION}",
			sessionName: "myproject",
			wantSuffix:  "/projects/myproject",
		},
		{
			name:       "project variable",
			workDir:    "${PROJECT}/subdir",
			projectDir: "/home/user/proj",
			wantSuffix: "/home/user/proj/subdir",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := CommandHook{WorkDir: tt.workDir}
			got := h.ExpandWorkDir(tt.sessionName, tt.projectDir)
			if got != tt.wantSuffix {
				t.Errorf("ExpandWorkDir() = %s, want %s", got, tt.wantSuffix)
			}
		})
	}
}

func TestDurationUnmarshalText(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"30s", 30 * time.Second},
		{"1m", 1 * time.Minute},
		{"5m30s", 5*time.Minute + 30*time.Second},
		{"1h", 1 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalText([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalText(%q) error = %v", tt.input, err)
			}
			if d.Duration() != tt.want {
				t.Errorf("UnmarshalText(%q) = %v, want %v", tt.input, d.Duration(), tt.want)
			}
		})
	}
}

func TestEmptyCommandHooksConfig(t *testing.T) {
	cfg := EmptyCommandHooksConfig()
	if cfg == nil {
		t.Fatal("EmptyCommandHooksConfig() returned nil")
	}
	if len(cfg.Hooks) != 0 {
		t.Errorf("expected empty hooks, got %d", len(cfg.Hooks))
	}
}

func TestDurationUnmarshalTextInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"invalid format", "not-a-duration"},
		{"missing unit", "30"},
		{"invalid unit", "30x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalText([]byte(tt.input))
			if err == nil {
				t.Errorf("UnmarshalText(%q) should return error", tt.input)
			}
		})
	}
}
