package agentsession

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGeminiUsageDiscoveryBound(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".gemini", "tmp", geminiProjectHash(cwd), "chats")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < geminiUsageDiscoveryLimit; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("session-%05d.jsonl", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := FindGeminiTranscriptForCwd(context.Background(), home, cwd, time.Time{}); !ok {
		t.Fatal("exact discovery budget should be admitted")
	}
	if err := os.WriteFile(filepath.Join(dir, "session-newest.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if path, ok := FindGeminiTranscriptForCwd(context.Background(), home, cwd, time.Time{}); ok || path != "" {
		t.Fatalf("partial scan claimed newest transcript: %q, %v", path, ok)
	}
}

func TestGeminiUsageDiscoveryUsesRegularFileMetadata(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".gemini", "tmp", geminiProjectHash(cwd), "chats")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Content is deliberately not JSON: discovery must not interpret accounting
	// or read headers. The bounded accounting reader owns format validation.
	a, b := filepath.Join(dir, "session-a.json"), filepath.Join(dir, "session-b.jsonl")
	for _, path := range []string{b, a} {
		if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "session-directory.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, ok := FindGeminiTranscriptForCwd(context.Background(), home, cwd, time.Time{}); !ok || got != a {
		t.Fatalf("stable tie break = %q, %v; want %q", got, ok, a)
	}
	if err := os.Chtimes(b, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, ok := FindGeminiTranscriptForCwd(context.Background(), home, cwd, now); !ok || got != b {
		t.Fatalf("newest metadata = %q, %v", got, ok)
	}
	if got, ok := FindGeminiTranscriptForCwd(context.Background(), home, cwd, now.Add(time.Second)); ok || got != "" {
		t.Fatalf("mtime bound was not strict: %q, %v", got, ok)
	}
}
