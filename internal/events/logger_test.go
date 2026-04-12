package events

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestNewLogger(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	if logger.file == nil {
		t.Error("Expected file to be opened")
	}
}

func TestNewLogger_Disabled(t *testing.T) {
	logger, err := NewLogger(LoggerOptions{
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	if logger.file != nil {
		t.Error("Expected file to be nil when disabled")
	}

	// Logging should be a no-op
	err = logger.Log(NewEvent(EventSessionCreate, "test", nil))
	if err != nil {
		t.Errorf("Log on disabled logger should not error: %v", err)
	}
}

func TestLogger_Log(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:    logPath,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Log an event
	event := NewEvent(EventSessionCreate, "myproject", map[string]interface{}{
		"claude_count": 2,
		"codex_count":  1,
	})
	if err := logger.Log(event); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	// Close and read the file
	logger.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// Parse the logged event
	var logged Event
	if err := json.Unmarshal(data, &logged); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if logged.Type != EventSessionCreate {
		t.Errorf("Type = %q, want %q", logged.Type, EventSessionCreate)
	}

	if logged.Session != "myproject" {
		t.Errorf("Session = %q, want %q", logged.Session, "myproject")
	}
}

func TestLogger_Log_RedactsSecretsForStorage(t *testing.T) {
	// Ensure clean state
	SetRedactionConfig(nil)
	defer SetRedactionConfig(nil)

	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeWarn})

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:    logPath,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	secret := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	event := NewEvent(EventError, "test-session", map[string]interface{}{
		"message": "failed with key: " + secret,
	})
	if err := logger.Log(event); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	logger.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if bytes.Contains(data, []byte(secret)) {
		t.Fatalf("event log on disk contains raw secret; want redacted")
	}
	if !bytes.Contains(data, []byte("[REDACTED:")) {
		t.Fatalf("expected redaction marker in persisted event log")
	}
}

func TestLogger_LogEvent(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:    logPath,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Log using convenience method
	err = logger.LogEvent(EventPromptSend, "test-session", PromptSendData{
		TargetCount:  3,
		PromptLength: 100,
		Template:     "code_review",
	})
	if err != nil {
		t.Fatalf("LogEvent failed: %v", err)
	}

	logger.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	var logged Event
	if err := json.Unmarshal(data, &logged); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if logged.Type != EventPromptSend {
		t.Errorf("Type = %q, want %q", logged.Type, EventPromptSend)
	}

	if tc, ok := logged.Data["target_count"].(float64); !ok || int(tc) != 3 {
		t.Errorf("target_count = %v, want 3", logged.Data["target_count"])
	}
}

func TestLogger_MultipleEvents(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:    logPath,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Log multiple events
	for i := 0; i < 5; i++ {
		logger.LogEvent(EventSessionCreate, "session-"+string(rune('a'+i)), nil)
	}
	logger.Close()

	// Read and count lines
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := bytes.Split(data, []byte("\n"))
	nonEmpty := 0
	for _, line := range lines {
		if len(line) > 0 {
			nonEmpty++
		}
	}

	if nonEmpty != 5 {
		t.Errorf("Got %d events, want 5", nonEmpty)
	}
}

func TestLogger_CloseDoesNotReopenAfterQueuedRotation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	old := time.Now().UTC().AddDate(0, 0, -(DefaultRetentionDays + 5))
	payload := strings.Repeat("x", 2048)
	var data bytes.Buffer
	for i := 0; i < 8000; i++ {
		line, err := json.Marshal(Event{
			Timestamp: old,
			Type:      EventSessionCreate,
			Session:   "old",
			Data: map[string]interface{}{
				"payload": payload,
				"index":   i,
			},
		})
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}
		data.Write(line)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(logPath, data.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: DefaultRetentionDays,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.lastRotation = time.Time{}
	logger.rotationWg.Add(1)
	go func() {
		defer logger.rotationWg.Done()
		logger.maybeRotate()
	}()

	if err := logger.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	time.Sleep(250 * time.Millisecond)

	logger.mu.Lock()
	reopened := logger.file != nil
	logger.mu.Unlock()
	if reopened {
		t.Fatal("logger reopened file after Close")
	}

	if err := logger.Log(NewEvent(EventSessionCreate, "after-close", nil)); err != nil {
		t.Fatalf("Log after Close failed: %v", err)
	}
	logger.mu.Lock()
	reopened = logger.file != nil
	logger.mu.Unlock()
	if reopened {
		t.Fatal("logger reopened file after Log called after Close")
	}
}

func TestRotateOldEntries(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	// Create file with old and new entries
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -35)   // 35 days ago
	recent := now.AddDate(0, 0, -5) // 5 days ago

	entries := []Event{
		{Timestamp: old, Type: EventSessionCreate, Session: "old"},
		{Timestamp: recent, Type: EventSessionCreate, Session: "recent"},
		{Timestamp: now, Type: EventSessionCreate, Session: "now"},
	}

	var data []byte
	for _, e := range entries {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}

	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create logger and trigger rotation
	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Force rotation
	logger.lastRotation = time.Time{} // Reset to trigger rotation
	if err := logger.rotateOldEntries(); err != nil {
		t.Fatalf("rotateOldEntries failed: %v", err)
	}
	logger.Close()

	// Read and verify
	data, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := bytes.Split(data, []byte("\n"))
	nonEmpty := 0
	for _, line := range lines {
		if len(line) > 0 {
			nonEmpty++
			var e Event
			json.Unmarshal(line, &e)
			if e.Session == "old" {
				t.Error("Old entry should have been rotated out")
			}
		}
	}

	if nonEmpty != 2 {
		t.Errorf("Got %d entries after rotation, want 2", nonEmpty)
	}
}

func TestExpandPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := util.ExpandPath(tt.input)
		if got != tt.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}

	// Test ~ expansion (can't test exact value since it depends on user)
	expanded := util.ExpandPath("~/test")
	if expanded == "~/test" {
		t.Error("ExpandPath should have expanded ~")
	}
}

func TestToMap(t *testing.T) {
	data := SessionCreateData{
		ClaudeCount: 2,
		CodexCount:  1,
		CursorCount: 1,
		OllamaCount: 1,
		WorkDir:     "/path",
	}

	m := ToMap(data)
	if m["claude_count"] != 2 || m["codex_count"] != 1 || m["cursor_count"] != 1 || m["ollama_count"] != 1 || m["work_dir"] != "/path" {
		t.Errorf("ToMap(SessionCreateData) produced unexpected map: %v", m)
	}
}

func TestToMap_AllTypes(t *testing.T) {
	t.Parallel()

	t.Run("SessionCreateData", func(t *testing.T) {
		t.Parallel()
		m := ToMap(SessionCreateData{
			ClaudeCount:   3,
			CodexCount:    2,
			GeminiCount:   1,
			CursorCount:   1,
			WindsurfCount: 1,
			AiderCount:    1,
			OllamaCount:   2,
			WorkDir:       "/work",
			Recipe:        "code-review",
		})
		if m["claude_count"] != 3 || m["codex_count"] != 2 || m["gemini_count"] != 1 || m["cursor_count"] != 1 || m["windsurf_count"] != 1 || m["aider_count"] != 1 || m["ollama_count"] != 2 {
			t.Errorf("unexpected counts: %v", m)
		}
		if m["work_dir"] != "/work" || m["recipe"] != "code-review" {
			t.Errorf("unexpected context: %v", m)
		}
	})

	t.Run("AgentSpawnData", func(t *testing.T) {
		t.Parallel()
		m := ToMap(AgentSpawnData{
			AgentType: "claude",
			Model:     "opus-4.5",
			Variant:   "standard",
			PaneIndex: 2,
		})
		if m == nil {
			t.Fatal("ToMap returned nil")
		}
		if m["agent_type"] != "claude" {
			t.Errorf("agent_type = %v, want claude", m["agent_type"])
		}
		if m["model"] != "opus-4.5" {
			t.Errorf("model = %v, want opus-4.5", m["model"])
		}
		if m["variant"] != "standard" {
			t.Errorf("variant = %v, want standard", m["variant"])
		}
		if m["pane_index"] != 2 {
			t.Errorf("pane_index = %v, want 2", m["pane_index"])
		}
	})

	t.Run("PromptSendData", func(t *testing.T) {
		t.Parallel()
		m := ToMap(PromptSendData{
			TargetCount:     5,
			PromptLength:    1000,
			Template:        "debug",
			HasContext:      true,
			TargetTypes:     "claude,codex",
			EstimatedTokens: 500,
		})
		if m == nil {
			t.Fatal("ToMap returned nil")
		}
		if m["target_count"] != 5 {
			t.Errorf("target_count = %v, want 5", m["target_count"])
		}
		if m["prompt_length"] != 1000 {
			t.Errorf("prompt_length = %v, want 1000", m["prompt_length"])
		}
		if m["has_context"] != true {
			t.Errorf("has_context = %v, want true", m["has_context"])
		}
		if m["estimated_tokens"] != 500 {
			t.Errorf("estimated_tokens = %v, want 500", m["estimated_tokens"])
		}
	})

	t.Run("CheckpointData", func(t *testing.T) {
		t.Parallel()
		m := ToMap(CheckpointData{
			CheckpointID: "cp-123",
			Description:  "before refactor",
			IncludesGit:  true,
		})
		if m == nil {
			t.Fatal("ToMap returned nil")
		}
		if m["checkpoint_id"] != "cp-123" {
			t.Errorf("checkpoint_id = %v, want cp-123", m["checkpoint_id"])
		}
		if m["description"] != "before refactor" {
			t.Errorf("description = %v, want before refactor", m["description"])
		}
		if m["includes_git"] != true {
			t.Errorf("includes_git = %v, want true", m["includes_git"])
		}
	})

	t.Run("ErrorData", func(t *testing.T) {
		t.Parallel()
		m := ToMap(ErrorData{
			ErrorType: "rate_limit",
			Message:   "429 Too Many Requests",
			Stack:     "goroutine 1 [running]:\nmain.go:42",
		})
		if m == nil {
			t.Fatal("ToMap returned nil")
		}
		if m["error_type"] != "rate_limit" {
			t.Errorf("error_type = %v, want rate_limit", m["error_type"])
		}
		if m["message"] != "429 Too Many Requests" {
			t.Errorf("message = %v, want 429 Too Many Requests", m["message"])
		}
		if m["stack"] != "goroutine 1 [running]:\nmain.go:42" {
			t.Errorf("stack = %v", m["stack"])
		}
	})

	t.Run("map passthrough", func(t *testing.T) {
		t.Parallel()
		input := map[string]interface{}{"key": "value", "num": 42}
		m := ToMap(input)
		if m["key"] != "value" {
			t.Errorf("key = %v, want value", m["key"])
		}
		if m["num"] != 42 {
			t.Errorf("num = %v, want 42", m["num"])
		}
	})

	t.Run("unknown type returns nil", func(t *testing.T) {
		t.Parallel()
		m := ToMap("string value")
		if m != nil {
			t.Errorf("ToMap(string) = %v, want nil", m)
		}
	})

	t.Run("nil returns nil", func(t *testing.T) {
		t.Parallel()
		m := ToMap(nil)
		if m != nil {
			t.Errorf("ToMap(nil) = %v, want nil", m)
		}
	})
}

func TestNewEventWithCorrelation(t *testing.T) {
	t.Parallel()

	before := time.Now()
	event := NewEventWithCorrelation(EventAgentSpawn, "sess", "agent1", "corr-123",
		map[string]interface{}{"key": "val"})
	after := time.Now()

	if event.Type != EventAgentSpawn {
		t.Errorf("Type = %q, want %q", event.Type, EventAgentSpawn)
	}
	if event.Session != "sess" {
		t.Errorf("Session = %q, want %q", event.Session, "sess")
	}
	if event.AgentName != "agent1" {
		t.Errorf("AgentName = %q, want %q", event.AgentName, "agent1")
	}
	if event.CorrelationID != "corr-123" {
		t.Errorf("CorrelationID = %q, want %q", event.CorrelationID, "corr-123")
	}
	if event.Timestamp.Before(before) || event.Timestamp.After(after) {
		t.Error("Timestamp should be between before and after")
	}
	if event.Data["key"] != "val" {
		t.Errorf("Data[key] = %v, want val", event.Data["key"])
	}
}

func TestNewEvent(t *testing.T) {
	before := time.Now()
	event := NewEvent(EventSessionCreate, "test", map[string]interface{}{"key": "value"})
	after := time.Now()

	if event.Type != EventSessionCreate {
		t.Errorf("Type = %q, want %q", event.Type, EventSessionCreate)
	}

	if event.Session != "test" {
		t.Errorf("Session = %q, want %q", event.Session, "test")
	}

	if event.Timestamp.Before(before) || event.Timestamp.After(after) {
		t.Error("Timestamp should be between before and after")
	}
}

func TestNewEvent_NilData(t *testing.T) {
	t.Parallel()

	event := NewEvent(EventSessionKill, "s", nil)
	if event.Type != EventSessionKill {
		t.Errorf("Type = %q, want %q", event.Type, EventSessionKill)
	}
	if event.Data != nil {
		t.Errorf("Data = %v, want nil", event.Data)
	}
}

func TestEventTypeConstants(t *testing.T) {
	t.Parallel()

	// Verify all event type constants are non-empty and unique
	types := []EventType{
		EventSessionCreate, EventSessionKill, EventSessionAttach,
		EventAgentSpawn, EventAgentAdd, EventAgentCrash, EventAgentRestart,
		EventPromptSend, EventPromptBroadcast, EventInterrupt,
		EventCheckpointCreate, EventCheckpointRestore, EventSessionSave, EventSessionRestore,
		EventTemplateUse, EventError,
	}

	seen := make(map[EventType]bool)
	for _, et := range types {
		if string(et) == "" {
			t.Errorf("EventType constant is empty")
		}
		if seen[et] {
			t.Errorf("Duplicate EventType: %q", et)
		}
		seen[et] = true
	}
}

func TestDefaultOptions(t *testing.T) {
	t.Parallel()

	opts := DefaultOptions()

	if opts.RetentionDays != DefaultRetentionDays {
		t.Errorf("RetentionDays = %d, want %d", opts.RetentionDays, DefaultRetentionDays)
	}
	if !opts.Enabled {
		t.Error("Enabled should be true by default")
	}
	if opts.Path == "" {
		t.Error("Path should not be empty")
	}
	// Path should have ~ expanded
	if opts.Path[0] == '~' {
		t.Error("Path should have ~ expanded")
	}
}

func TestLogger_Since(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	// Create events at different times
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-30 * time.Minute)

	entries := []Event{
		{Timestamp: old, Type: EventSessionCreate, Session: "old"},
		{Timestamp: recent, Type: EventAgentSpawn, Session: "recent"},
		{Timestamp: now, Type: EventPromptSend, Session: "now"},
	}

	var data []byte
	for _, e := range entries {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Get events since 1 hour ago
	cutoff := now.Add(-1 * time.Hour)
	events, err := logger.Since(cutoff)
	if err != nil {
		t.Fatalf("Since failed: %v", err)
	}

	if len(events) != 2 {
		t.Errorf("got %d events, want 2", len(events))
	}

	// Verify the old event was filtered out
	for _, e := range events {
		if e.Session == "old" {
			t.Error("old event should have been filtered out")
		}
	}
}

func TestLogger_Since_DisabledLogger(t *testing.T) {
	t.Parallel()

	logger, err := NewLogger(LoggerOptions{Enabled: false})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	events, err := logger.Since(time.Now().Add(-1 * time.Hour))
	if err != nil {
		t.Fatalf("Since failed: %v", err)
	}

	if len(events) != 0 {
		t.Errorf("got %d events, want 0 for disabled logger", len(events))
	}
}

func TestLogger_SinceByType(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	now := time.Now().UTC()
	entries := []Event{
		{Timestamp: now.Add(-1 * time.Hour), Type: EventSessionCreate, Session: "s1"},
		{Timestamp: now.Add(-30 * time.Minute), Type: EventAgentSpawn, Session: "s2"},
		{Timestamp: now.Add(-15 * time.Minute), Type: EventSessionCreate, Session: "s3"},
		{Timestamp: now, Type: EventPromptSend, Session: "s4"},
	}

	var data []byte
	for _, e := range entries {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Get only SessionCreate events since 2 hours ago
	events, err := logger.SinceByType(EventSessionCreate, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("SinceByType failed: %v", err)
	}

	if len(events) != 2 {
		t.Errorf("got %d events, want 2", len(events))
	}

	for _, e := range events {
		if e.Type != EventSessionCreate {
			t.Errorf("got event type %q, want %q", e.Type, EventSessionCreate)
		}
	}
}

func TestLogger_EventCount(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	now := time.Now().UTC()
	entries := []Event{
		{Timestamp: now.Add(-2 * time.Hour), Type: EventSessionCreate, Session: "s1"},
		{Timestamp: now.Add(-1 * time.Hour), Type: EventAgentSpawn, Session: "s2"},
		{Timestamp: now.Add(-30 * time.Minute), Type: EventPromptSend, Session: "s3"},
		{Timestamp: now, Type: EventError, Session: "s4"},
	}

	var data []byte
	for _, e := range entries {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	tests := []struct {
		name  string
		since time.Duration
		want  int
	}{
		{"all events", -3 * time.Hour, 4},
		{"last 90 minutes", -90 * time.Minute, 3},
		{"last 45 minutes", -45 * time.Minute, 2},
		{"last 15 minutes", -15 * time.Minute, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			count, err := logger.EventCount(now.Add(tc.since))
			if err != nil {
				t.Fatalf("EventCount failed: %v", err)
			}
			if count != tc.want {
				t.Errorf("count = %d, want %d", count, tc.want)
			}
		})
	}
}

func TestLogger_LastEvent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	now := time.Now().UTC()
	entries := []Event{
		{Timestamp: now.Add(-2 * time.Hour), Type: EventSessionCreate, Session: "first"},
		{Timestamp: now.Add(-1 * time.Hour), Type: EventAgentSpawn, Session: "middle"},
		{Timestamp: now, Type: EventPromptSend, Session: "last"},
	}

	var data []byte
	for _, e := range entries {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	event, err := logger.LastEvent()
	if err != nil {
		t.Fatalf("LastEvent failed: %v", err)
	}

	if event == nil {
		t.Fatal("LastEvent returned nil")
	}
	if event.Session != "last" {
		t.Errorf("Session = %q, want %q", event.Session, "last")
	}
	if event.Type != EventPromptSend {
		t.Errorf("Type = %q, want %q", event.Type, EventPromptSend)
	}
}

func TestLogger_LastEvent_EmptyFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	// Create empty file
	if err := os.WriteFile(logPath, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	event, err := logger.LastEvent()
	if err != nil {
		t.Fatalf("LastEvent failed: %v", err)
	}

	if event != nil {
		t.Errorf("LastEvent = %v, want nil for empty file", event)
	}
}

func TestLogger_LastEvent_NoFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "nonexistent.jsonl")

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Delete the file that was created by NewLogger
	os.Remove(logPath)

	event, err := logger.LastEvent()
	if err != nil {
		t.Fatalf("LastEvent failed: %v", err)
	}

	if event != nil {
		t.Errorf("LastEvent = %v, want nil for nonexistent file", event)
	}
}

func TestLogger_LastEvent_Disabled(t *testing.T) {
	t.Parallel()

	logger, err := NewLogger(LoggerOptions{Enabled: false})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	event, err := logger.LastEvent()
	if err != nil {
		t.Fatalf("LastEvent failed: %v", err)
	}

	if event != nil {
		t.Errorf("LastEvent = %v, want nil for disabled logger", event)
	}
}

func TestLogger_Replay(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	now := time.Now().UTC()
	entries := []Event{
		{Timestamp: now.Add(-2 * time.Hour), Type: EventSessionCreate, Session: "s1"},
		{Timestamp: now.Add(-1 * time.Hour), Type: EventAgentSpawn, Session: "s2"},
		{Timestamp: now, Type: EventPromptSend, Session: "s3"},
	}

	var data []byte
	for _, e := range entries {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	ch, err := logger.Replay(now.Add(-90 * time.Minute))
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	var received []*Event
	for e := range ch {
		received = append(received, e)
	}

	if len(received) != 2 {
		t.Errorf("received %d events, want 2", len(received))
	}
}

func TestLogger_ReplaySession(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	now := time.Now().UTC()
	entries := []Event{
		{Timestamp: now.Add(-2 * time.Hour), Type: EventSessionCreate, Session: "target"},
		{Timestamp: now.Add(-1 * time.Hour), Type: EventAgentSpawn, Session: "other"},
		{Timestamp: now.Add(-30 * time.Minute), Type: EventPromptSend, Session: "target"},
		{Timestamp: now, Type: EventError, Session: "target"},
	}

	var data []byte
	for _, e := range entries {
		line, _ := json.Marshal(e)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	ch, err := logger.ReplaySession("target", now.Add(-3*time.Hour))
	if err != nil {
		t.Fatalf("ReplaySession failed: %v", err)
	}

	var received []*Event
	for e := range ch {
		received = append(received, e)
	}

	if len(received) != 3 {
		t.Errorf("received %d events, want 3", len(received))
	}

	for _, e := range received {
		if e.Session != "target" {
			t.Errorf("Session = %q, want %q", e.Session, "target")
		}
	}
}

func TestLogger_LastEvent_MalformedEntries(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	now := time.Now().UTC()
	validEvent := Event{Timestamp: now, Type: EventSessionCreate, Session: "valid"}
	validLine, _ := json.Marshal(validEvent)

	// Write valid event followed by malformed line followed by another valid event
	data := append(validLine, '\n')
	data = append(data, []byte("not valid json\n")...)
	lastEvent := Event{Timestamp: now.Add(time.Hour), Type: EventPromptSend, Session: "last"}
	lastLine, _ := json.Marshal(lastEvent)
	data = append(data, lastLine...)
	data = append(data, '\n')

	if err := os.WriteFile(logPath, data, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	logger, err := NewLogger(LoggerOptions{Path: logPath, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	event, err := logger.LastEvent()
	if err != nil {
		t.Fatalf("LastEvent failed: %v", err)
	}

	if event == nil {
		t.Fatal("LastEvent returned nil")
	}
	if event.Session != "last" {
		t.Errorf("Session = %q, want %q (should skip malformed entries)", event.Session, "last")
	}
}
