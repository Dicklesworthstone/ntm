package history

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSetEncryptionConfig(t *testing.T) {
	// Reset after test
	defer SetEncryptionConfig(nil)

	t.Run("nil disables", func(t *testing.T) {
		SetEncryptionConfig(nil)
		if encryptionEnabledForTest() {
			t.Error("expected disabled")
		}
	})

	t.Run("enabled with key", func(t *testing.T) {
		key := testKey(t)
		SetEncryptionConfig(&EncryptionConfig{
			Enabled:     true,
			EncryptKey:  key,
			DecryptKeys: [][]byte{key},
		})
		if !encryptionEnabledForTest() {
			t.Error("expected enabled")
		}
	})

	t.Run("enabled without key disables", func(t *testing.T) {
		SetEncryptionConfig(&EncryptionConfig{
			Enabled:    true,
			EncryptKey: nil,
		})
		if encryptionEnabledForTest() {
			t.Error("expected disabled when no key")
		}
	})
}

func TestEncryptedHistoryRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	// Write encrypted entries
	entry1 := &HistoryEntry{
		ID:        "enc-1",
		Session:   "test",
		Prompt:    "encrypted prompt alpha",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}
	entry2 := &HistoryEntry{
		ID:        "enc-2",
		Session:   "test",
		Prompt:    "encrypted prompt beta",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}

	if err := Append(entry1); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Append(entry2); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read them back
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Prompt != "encrypted prompt alpha" {
		t.Errorf("entry 0 prompt = %q, want %q", entries[0].Prompt, "encrypted prompt alpha")
	}
	if entries[1].Prompt != "encrypted prompt beta" {
		t.Errorf("entry 1 prompt = %q, want %q", entries[1].Prompt, "encrypted prompt beta")
	}
}

func TestEncryptedReadRecent(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	for i := 0; i < 5; i++ {
		e := &HistoryEntry{
			ID:        "r-" + string(rune('a'+i)),
			Session:   "test",
			Prompt:    "prompt " + string(rune('a'+i)),
			Timestamp: time.Now(),
			Source:    SourceCLI,
		}
		if err := Append(e); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := ReadRecent(2)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent, got %d", len(recent))
	}
}

func TestMixedPlaintextAndEncrypted(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Write plaintext entries first (no encryption)
	SetEncryptionConfig(nil)
	plain := &HistoryEntry{
		ID:        "plain-1",
		Session:   "test",
		Prompt:    "plaintext prompt",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}
	if err := Append(plain); err != nil {
		t.Fatal(err)
	}

	// Now enable encryption and write more
	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	enc := &HistoryEntry{
		ID:        "enc-1",
		Session:   "test",
		Prompt:    "encrypted prompt",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}
	if err := Append(enc); err != nil {
		t.Fatal(err)
	}

	// Both should be readable
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (mixed), got %d", len(entries))
	}
	if entries[0].ID != "plain-1" {
		t.Errorf("entry 0 ID = %q, want plain-1", entries[0].ID)
	}
	if entries[1].ID != "enc-1" {
		t.Errorf("entry 1 ID = %q, want enc-1", entries[1].ID)
	}
}

func TestKeyRotation(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	key1 := testKey(t)
	key2 := testKey(t)

	// Write with key1
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key1,
		DecryptKeys: [][]byte{key1},
	})

	if err := Append(&HistoryEntry{
		ID: "k1-entry", Session: "test", Prompt: "written with key1",
		Timestamp: time.Now(), Source: SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	// Rotate to key2, but keep key1 in the keyring for reading old entries
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key2,
		DecryptKeys: [][]byte{key2, key1},
	})
	defer SetEncryptionConfig(nil)

	if err := Append(&HistoryEntry{
		ID: "k2-entry", Session: "test", Prompt: "written with key2",
		Timestamp: time.Now(), Source: SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	// Both should be readable with the combined keyring
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != "k1-entry" {
		t.Errorf("entry 0 ID = %q, want k1-entry", entries[0].ID)
	}
	if entries[1].ID != "k2-entry" {
		t.Errorf("entry 1 ID = %q, want k2-entry", entries[1].ID)
	}
}

// encryptionEnabledForTest reports whether encryption is currently enabled.
func encryptionEnabledForTest() bool {
	encryptMu.RLock()
	defer encryptMu.RUnlock()
	return encryptionEnabled
}

// Pruning may replace the only copy of a prompt. Unlike best-effort reads,
// it must not turn an unavailable key or damaged row into permanent data loss.
func TestHistoryPrunePreservesUnreadableRecords(t *testing.T) {
	for _, operation := range []string{"count", "time"} {
		for _, failure := range []string{"malformed", "missing_key", "wrong_key"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", t.TempDir())
				SetEncryptionConfig(nil)
				t.Cleanup(func() { SetEncryptionConfig(nil) })
				opaque := []byte(`{"prompt":"sensitive-broken-prompt"`)
				if failure != "malformed" {
					var err error
					opaque, err = encryption.EncryptLine(testKey(t), []byte(`{"id":"hidden","prompt":"private"}`))
					if err != nil {
						t.Fatal(err)
					}
				}
				if failure == "wrong_key" {
					key := testKey(t)
					SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
				}
				original := []byte("{\"id\":\"old\",\"ts\":\"2026-01-01T00:00:00Z\"}\n" + string(opaque) +
					"\n{\"id\":\"recent\",\"ts\":\"2026-01-03T00:00:00Z\"}\n")
				writeHistoryFixture(t, original)
				// Browsing remains best-effort; mutation must not use this partial view.
				if entries, err := ReadAll(); err != nil || len(entries) != 2 {
					t.Fatalf("ReadAll() = %d entries, %v; want two readable entries", len(entries), err)
				}
				var removed int
				var err error
				if operation == "count" {
					removed, err = Prune(1)
				} else {
					removed, err = PruneByTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
				}
				if err == nil || removed != 0 {
					t.Errorf("prune = (%d, %v), want (0, error)", removed, err)
				} else if !strings.Contains(err.Error(), "line 2") || strings.Contains(err.Error(), "sensitive-broken-prompt") {
					t.Errorf("error must locate the unreadable row without exposing its payload: %v", err)
				}
				assertHistoryUnchanged(t, original)
			})
		}
	}
}

func TestHistoryPruneAbortsOnEncryptionFailure(t *testing.T) {
	for _, operation := range []string{"count", "time"} {
		t.Run(operation, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: []byte("invalid-key")})
			t.Cleanup(func() { SetEncryptionConfig(nil) })
			original := []byte("{\"id\":\"old\",\"ts\":\"2026-01-01T00:00:00Z\"}\n" +
				"{\"id\":\"recent\",\"ts\":\"2026-01-03T00:00:00Z\"}\n")
			writeHistoryFixture(t, original)
			var removed int
			var err error
			if operation == "count" {
				removed, err = Prune(1)
			} else {
				removed, err = PruneByTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
			}
			if err == nil || removed != 0 || !encryption.IsKind(err, encryption.ErrInvalidKey) {
				t.Errorf("prune = (%d, %v), want wrapped invalid-key failure", removed, err)
			}
			assertHistoryUnchanged(t, original)
		})
	}
}

func TestHistoryPruneEncryptedBoundaryAndValidation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
	t.Cleanup(func() { SetEncryptionConfig(nil) })
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, entry := range []*HistoryEntry{
		{ID: "old", Timestamp: cutoff.Add(-time.Second)},
		{ID: "boundary", Timestamp: cutoff},
		{ID: "recent", Timestamp: cutoff.Add(time.Second)},
	} {
		if err := Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("negative retention", func(t *testing.T) {
		original, err := os.ReadFile(StoragePath())
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if value := recover(); value != nil {
				t.Errorf("negative retention panicked: %v", value)
			}
			assertHistoryUnchanged(t, original)
		}()
		if removed, err := Prune(-1); err == nil || removed != 0 {
			t.Errorf("Prune(-1) = (%d, %v), want (0, error)", removed, err)
		}
	})
	if removed, err := PruneByTime(cutoff); err != nil || removed != 1 {
		t.Fatalf("PruneByTime = (%d, %v), want only the strictly older entry removed", removed, err)
	}
	entries, err := ReadAll()
	if err != nil || len(entries) != 2 || entries[0].ID != "boundary" || entries[1].ID != "recent" {
		t.Fatalf("retained entries = %+v, %v", entries, err)
	}
	if removed, err := Prune(0); err != nil || removed != 2 {
		t.Fatalf("Prune(0) = (%d, %v), want (2, nil)", removed, err)
	}
	assertHistoryUnchanged(t, []byte{})
}

func TestHistoryCountLargeEncryptedPrompt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
	t.Cleanup(func() { SetEncryptionConfig(nil) })
	if err := Append(&HistoryEntry{ID: "large", Prompt: strings.Repeat("x", 128*1024)}); err != nil {
		t.Fatal(err)
	}
	if entries, err := ReadAll(); err != nil || len(entries) != 1 {
		t.Fatalf("ReadAll() = %d entries, %v", len(entries), err)
	}
	if count, err := Count(); err != nil || count != 1 {
		t.Fatalf("Count() = (%d, %v), want (1, nil)", count, err)
	}
}

func TestHistoryPruneRejectsOversizedEncryptedRewrite(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	SetEncryptionConfig(nil)
	t.Cleanup(func() { SetEncryptionConfig(nil) })
	if err := Append(&HistoryEntry{ID: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(&HistoryEntry{ID: "large", Prompt: strings.Repeat("x", 4*1024*1024)}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(StoragePath())
	if err != nil {
		t.Fatal(err)
	}
	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
	if removed, err := Prune(1); err == nil || removed != 0 {
		t.Errorf("Prune() = (%d, %v), want rejection of an unreadable encoded row", removed, err)
	}
	assertHistoryUnchanged(t, original)
}

func TestHistoryAppendLineLimit(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	SetEncryptionConfig(nil)
	t.Cleanup(func() { SetEncryptionConfig(nil) })
	entry := &HistoryEntry{ID: "boundary"}
	empty, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	// The scanner's bound includes the newline appended on disk.
	entry.Prompt = strings.Repeat("x", 5*1024*1024-1-len(empty))
	if err := Append(entry); err != nil {
		t.Fatalf("largest readable plaintext entry rejected: %v", err)
	}
	if count, err := Count(); err != nil || count != 1 {
		t.Fatalf("boundary row is not readable: count=%d, err=%v", count, err)
	}
	original, err := os.ReadFile(StoragePath())
	if err != nil {
		t.Fatal(err)
	}
	entry.Prompt += "x"
	if err := Append(entry); err == nil {
		t.Error("oversized plaintext entry accepted")
	}
	assertHistoryUnchanged(t, original)
	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
	if err := Append(entry); err == nil {
		t.Error("oversized encrypted entry accepted")
	}
	assertHistoryUnchanged(t, original)
}

func TestHistoryExportPrivatePlaintext(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			key := testKey(t)
			SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
			t.Cleanup(func() { SetEncryptionConfig(nil) })
			if err := Append(&HistoryEntry{ID: "private", Prompt: "private prompt"}); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(StoragePath())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "export.jsonl")
			if existing {
				if err := os.WriteFile(path, []byte("previous export"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := ExportTo(path); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var entry HistoryEntry
			if err := json.Unmarshal(data, &entry); err != nil || entry.ID != "private" || entry.Prompt != "private prompt" {
				t.Fatalf("invalid plaintext export: %+v, %v", entry, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Errorf("plaintext export permissions = %o, want 600", info.Mode().Perm())
			}
			assertHistoryUnchanged(t, original)
		})
	}
}

func TestHistoryExportRejectsSourceAliases(t *testing.T) {
	for _, alias := range []string{"direct", "relative", "symlink", "hardlink"} {
		t.Run(alias, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			key := testKey(t)
			SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
			t.Cleanup(func() { SetEncryptionConfig(nil) })
			if err := Append(&HistoryEntry{ID: "private", Prompt: "private prompt"}); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(StoragePath())
			if err != nil {
				t.Fatal(err)
			}
			path := StoragePath()
			switch alias {
			case "relative":
				wd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				path, err = filepath.Rel(wd, path)
				if err != nil {
					t.Fatal(err)
				}
			case "symlink", "hardlink":
				path = filepath.Join(t.TempDir(), "alias.jsonl")
				link := os.Symlink
				if alias == "hardlink" {
					link = os.Link
				}
				if err := link(StoragePath(), path); err != nil {
					t.Skipf("cannot create %s: %v", alias, err)
				}
			}
			if err := ExportTo(path); err == nil {
				t.Error("export accepted an alias of its encrypted source")
			}
			assertHistoryUnchanged(t, original)
		})
	}
}

func TestHistoryExportRefusesIncompleteRead(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "missing_key"
		if malformed {
			name = "malformed"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			SetEncryptionConfig(nil)
			t.Cleanup(func() { SetEncryptionConfig(nil) })
			row := []byte(`{"prompt":"broken"`)
			if !malformed {
				var err error
				row, err = encryption.EncryptLine(testKey(t), []byte(`{"id":"hidden"}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			original := append([]byte("{\"id\":\"readable\"}\n"), row...)
			original = append(original, '\n')
			writeHistoryFixture(t, original)
			path := filepath.Join(t.TempDir(), "export.jsonl")
			previous := []byte("previous complete export")
			if err := os.WriteFile(path, previous, 0600); err != nil {
				t.Fatal(err)
			}
			if err := ExportTo(path); err == nil || !strings.Contains(err.Error(), "line 2") {
				t.Errorf("ExportTo() = %v, want unreadable line error", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, previous) {
				t.Errorf("incomplete export overwrote previous output: %v", err)
			}
			assertHistoryUnchanged(t, original)
		})
	}
}

func TestHistoryExportDoesNotCreateItsSource(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := ExportTo(StoragePath()); err == nil {
		t.Error("export accepted the source path when history is absent")
	}
	if _, err := os.Stat(StoragePath()); !os.IsNotExist(err) {
		t.Fatalf("export created the history source: %v", err)
	}
}

func TestHistoryExportEmptyAndWriteFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	SetEncryptionConfig(nil)
	t.Cleanup(func() { SetEncryptionConfig(nil) })
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := ExportTo(path); err != nil {
		t.Fatalf("export of absent history: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("empty export = %q, %v", data, err)
	}
	if err := Append(&HistoryEntry{ID: "retained"}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(StoragePath())
	if err != nil {
		t.Fatal(err)
	}
	// Renaming a prepared export over a directory must fail, without
	// damaging either the history source or existing destination contents.
	destination := t.TempDir()
	marker := filepath.Join(destination, "keep")
	if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExportTo(destination); err == nil {
		t.Error("export replaced a destination directory")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unchanged" {
		t.Fatalf("export damaged existing destination: %q, %v", data, err)
	}
	assertHistoryUnchanged(t, original)
}

func TestHistoryStrictReadAcceptsWhitespaceAndRejectsNonRecords(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
	t.Cleanup(func() { SetEncryptionConfig(nil) })
	// Whitespace around a valid plaintext row must not be mistaken for
	// ciphertext when encryption is enabled for newly appended records.
	writeHistoryFixture(t, []byte(" \t{\"id\":\"valid\"} \r\n\n"))
	path := filepath.Join(t.TempDir(), "export.jsonl")
	if err := ExportTo(path); err != nil {
		t.Fatalf("export rejected valid whitespace: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry HistoryEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.ID != "valid" {
		t.Fatalf("invalid exported record: %+v, %v", entry, err)
	}
	for _, row := range []string{"null", "[]"} {
		original := []byte("{\"id\":\"valid\"}\n" + row + "\n")
		writeHistoryFixture(t, original)
		if err := ExportTo(path); err == nil {
			t.Errorf("export accepted non-record %q", row)
		}
		if removed, err := Prune(0); err == nil || removed != 0 {
			t.Errorf("prune accepted non-record %q: (%d, %v)", row, removed, err)
		}
		assertHistoryUnchanged(t, original)
	}
}

func writeHistoryFixture(t *testing.T, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(StoragePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StoragePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertHistoryUnchanged(t *testing.T, original []byte) {
	t.Helper()
	got, err := os.ReadFile(StoragePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("history was unexpectedly rewritten: got %d bytes, want %d unchanged bytes", len(got), len(original))
	}
}
