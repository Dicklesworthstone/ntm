package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestGetRedactionConfig_NilByDefault(t *testing.T) {
	// Not parallel: modifies package-level redactionConfig
	origCfg := GetRedactionConfig()
	SetRedactionConfig(nil)
	t.Cleanup(func() { SetRedactionConfig(origCfg) })

	got := GetRedactionConfig()
	if got != nil {
		t.Errorf("GetRedactionConfig() = %v, want nil when not configured", got)
	}
}

func TestGetRedactionConfig_ReturnsCopy(t *testing.T) {
	// Not parallel: modifies package-level redactionConfig
	origCfg := GetRedactionConfig()
	t.Cleanup(func() { SetRedactionConfig(origCfg) })

	cfg := &redaction.Config{Mode: redaction.ModeWarn}
	SetRedactionConfig(cfg)

	got := GetRedactionConfig()
	if got == nil {
		t.Fatal("GetRedactionConfig() = nil, want non-nil after SetRedactionConfig")
	}
	if got.Mode != redaction.ModeWarn {
		t.Errorf("GetRedactionConfig().Mode = %v, want %v", got.Mode, redaction.ModeWarn)
	}

	// Verify it returns a copy, not the original pointer
	got.Mode = redaction.ModeRedact
	got2 := GetRedactionConfig()
	if got2.Mode != redaction.ModeWarn {
		t.Error("GetRedactionConfig() should return a copy; mutation affected the original")
	}
}

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

func TestSavePromptConcurrentPreservesEveryEntry(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Not parallel: modifies package-level redactionConfig.
	originalConfig := GetRedactionConfig()
	SetRedactionConfig(nil)
	t.Cleanup(func() { SetRedactionConfig(originalConfig) })

	const promptCount = 64
	sessionName := "test-session-concurrent"
	start := make(chan struct{})
	errs := make(chan error, promptCount)
	var wg sync.WaitGroup

	for i := 0; i < promptCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- SavePrompt(PromptEntry{
				ID:      fmt.Sprintf("prompt-%d", i),
				Session: sessionName,
				Content: fmt.Sprintf("content-%d", i),
				Targets: []string{"1"},
				Source:  "cli",
			})
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SavePrompt failed: %v", err)
		}
	}

	history, err := LoadPromptHistory(sessionName)
	if err != nil {
		t.Fatalf("LoadPromptHistory failed: %v", err)
	}
	if len(history.Prompts) != promptCount {
		t.Fatalf("saved prompt count = %d, want %d", len(history.Prompts), promptCount)
	}

	seen := make(map[string]bool, promptCount)
	for _, prompt := range history.Prompts {
		seen[prompt.ID] = true
	}
	for i := 0; i < promptCount; i++ {
		id := fmt.Sprintf("prompt-%d", i)
		if !seen[id] {
			t.Errorf("missing concurrently saved prompt %q", id)
		}
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
