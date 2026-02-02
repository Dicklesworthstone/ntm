package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestSaveAndLoadPromptHistory(t *testing.T) {
	// Create temp dir for test
	tmpDir, err := os.MkdirTemp("", "ntm-prompts-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "test-session"

	// Save a prompt
	entry := PromptEntry{
		Session:  sessionName,
		Content:  "fix the tests",
		Targets:  []string{"1", "2"},
		Source:   "cli",
		Template: "",
	}

	err = SavePrompt(entry)
	if err != nil {
		t.Fatalf("SavePrompt failed: %v", err)
	}

	// Load and verify
	history, err := LoadPromptHistory(sessionName)
	if err != nil {
		t.Fatalf("LoadPromptHistory failed: %v", err)
	}

	if len(history.Prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(history.Prompts))
	}

	if history.Prompts[0].Content != "fix the tests" {
		t.Errorf("expected content 'fix the tests', got '%s'", history.Prompts[0].Content)
	}

	if history.Session != sessionName {
		t.Errorf("expected session '%s', got '%s'", sessionName, history.Session)
	}
}

func TestSavePrompt_RedactsOnWriteWhenConfigured(t *testing.T) {
	// Create temp dir for test
	tmpDir, err := os.MkdirTemp("", "ntm-prompts-redact-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeWarn})
	t.Cleanup(func() { SetRedactionConfig(nil) })

	secret := "sk-" + strings.Repeat("A", 48)
	sessionName := "test-session-redact"

	entry := PromptEntry{
		Session: sessionName,
		Content: "token=" + secret,
		Targets: []string{"1"},
		Source:  "cli",
	}
	if err := SavePrompt(entry); err != nil {
		t.Fatalf("SavePrompt failed: %v", err)
	}

	history, err := LoadPromptHistory(sessionName)
	if err != nil {
		t.Fatalf("LoadPromptHistory failed: %v", err)
	}
	if len(history.Prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(history.Prompts))
	}

	got := history.Prompts[0].Content
	if strings.Contains(got, secret) {
		t.Fatalf("expected persisted prompt to be redacted, but it still contained the secret")
	}
	if !strings.Contains(got, "[REDACTED:") {
		t.Fatalf("expected persisted prompt to contain a redaction placeholder, got: %q", got)
	}
}

func TestSaveMultiplePrompts(t *testing.T) {
	// Create temp dir for test
	tmpDir, err := os.MkdirTemp("", "ntm-prompts-multi-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "test-session-multi"

	// Save multiple prompts
	prompts := []string{"prompt 1", "prompt 2", "prompt 3"}
	for _, p := range prompts {
		entry := PromptEntry{
			Session: sessionName,
			Content: p,
			Targets: []string{"1"},
			Source:  "cli",
		}
		err := SavePrompt(entry)
		if err != nil {
			t.Fatalf("SavePrompt failed: %v", err)
		}
	}

	// Load and verify
	history, err := LoadPromptHistory(sessionName)
	if err != nil {
		t.Fatalf("LoadPromptHistory failed: %v", err)
	}

	if len(history.Prompts) != 3 {
		t.Fatalf("expected 3 prompts, got %d", len(history.Prompts))
	}

	for i, p := range prompts {
		if history.Prompts[i].Content != p {
			t.Errorf("prompt %d: expected '%s', got '%s'", i, p, history.Prompts[i].Content)
		}
	}
}

func TestGetLatestPrompts(t *testing.T) {
	// Create temp dir for test
	tmpDir, err := os.MkdirTemp("", "ntm-prompts-latest-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "test-session-latest"

	// Save multiple prompts with different timestamps
	for i := 0; i < 5; i++ {
		entry := PromptEntry{
			Session:   sessionName,
			Content:   "prompt",
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Targets:   []string{"1"},
			Source:    "cli",
		}
		entry.ID = "" // Let SavePrompt generate ID
		err := SavePrompt(entry)
		if err != nil {
			t.Fatalf("SavePrompt failed: %v", err)
		}
	}

	// Get latest 2
	latest, err := GetLatestPrompts(sessionName, 2)
	if err != nil {
		t.Fatalf("GetLatestPrompts failed: %v", err)
	}

	if len(latest) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(latest))
	}

	// Verify they're sorted newest first
	if latest[0].Timestamp.Before(latest[1].Timestamp) {
		t.Error("prompts not sorted newest first")
	}
}

func TestClearPromptHistory(t *testing.T) {
	// Create temp dir for test
	tmpDir, err := os.MkdirTemp("", "ntm-prompts-clear-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "test-session-clear"

	// Save a prompt
	entry := PromptEntry{
		Session: sessionName,
		Content: "test prompt",
		Targets: []string{"1"},
		Source:  "cli",
	}
	err = SavePrompt(entry)
	if err != nil {
		t.Fatalf("SavePrompt failed: %v", err)
	}

	// Clear history
	err = ClearPromptHistory(sessionName)
	if err != nil {
		t.Fatalf("ClearPromptHistory failed: %v", err)
	}

	// Verify it's cleared (LoadPromptHistory returns empty history)
	history, err := LoadPromptHistory(sessionName)
	if err != nil {
		t.Fatalf("LoadPromptHistory failed: %v", err)
	}

	if len(history.Prompts) != 0 {
		t.Errorf("expected 0 prompts after clear, got %d", len(history.Prompts))
	}
}

func TestSessionDir(t *testing.T) {
	// Create temp dir for test
	tmpDir, err := os.MkdirTemp("", "ntm-session-dir-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	sessionName := "my-project"
	dir, err := SessionDir(sessionName)
	if err != nil {
		t.Fatalf("SessionDir failed: %v", err)
	}

	// Verify the directory was created
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("SessionDir did not create directory")
	}

	// Verify it's in the expected location
	expected := filepath.Join(tmpDir, ".ntm", "sessions", sessionName)
	if dir != expected {
		t.Errorf("expected '%s', got '%s'", expected, dir)
	}
}

func TestSavePromptRequiresSession(t *testing.T) {
	entry := PromptEntry{
		Session: "", // Empty session name
		Content: "test",
		Targets: []string{"1"},
		Source:  "cli",
	}

	err := SavePrompt(entry)
	if err == nil {
		t.Error("expected error for empty session name")
	}
}

func TestListSessionDirs(t *testing.T) {
	// Create temp dir for test
	tmpDir, err := os.MkdirTemp("", "ntm-list-sessions-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Create prompts for two sessions
	sessions := []string{"project-a", "project-b"}
	for _, s := range sessions {
		entry := PromptEntry{
			Session: s,
			Content: "test prompt",
			Targets: []string{"1"},
			Source:  "cli",
		}
		if err := SavePrompt(entry); err != nil {
			t.Fatalf("SavePrompt for %s failed: %v", s, err)
		}
	}

	// List sessions
	listed, err := ListSessionDirs()
	if err != nil {
		t.Fatalf("ListSessionDirs failed: %v", err)
	}

	if len(listed) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %v", len(listed), listed)
	}

	// Verify both sessions are present
	found := make(map[string]bool)
	for _, s := range listed {
		found[s] = true
	}
	for _, s := range sessions {
		if !found[s] {
			t.Errorf("session '%s' not found in list", s)
		}
	}
}
