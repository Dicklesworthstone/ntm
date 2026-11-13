package checkpoint

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

func mustRandomAESKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read key: %v", err)
	}
	return key
}

func mustGzipBytes(t *testing.T, plain string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(plain)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func initTempGitRepoWithDirtyPatch(t *testing.T) (repoDir, commit, patch, status string) {
	t.Helper()
	repoDir = t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=NTM Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=NTM Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
		return string(out)
	}

	runGit("init", "-b", "main")
	tracked := filepath.Join(repoDir, "secret.txt")
	if err := os.WriteFile(tracked, []byte("baseline\n"), 0o600); err != nil {
		t.Fatalf("write tracked: %v", err)
	}
	runGit("add", "secret.txt")
	runGit("commit", "-m", "initial")
	commit = strings.TrimSpace(runGit("rev-parse", "HEAD"))

	if err := os.WriteFile(tracked, []byte("baseline\npatched-secret-token=sk-live-1234567890abcdef\n"), 0o600); err != nil {
		t.Fatalf("modify tracked: %v", err)
	}
	patch = runGit("diff")
	status = runGit("status", "--porcelain")
	return repoDir, commit, patch, status
}

func TestCheckpointEncryption_AtRestTamperMissingKeyAndRotation(t *testing.T) {
	keyA := mustRandomAESKey(t)
	keyB := mustRandomAESKey(t)
	t.Cleanup(func() { SetEncryptionConfig(nil) })

	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  keyA,
		DecryptKeys: [][]byte{keyA},
	})

	repoDir, commitSHA, gitPatch, gitStatus := initTempGitRepoWithDirtyPatch(t)
	storage := NewStorageWithDir(t.TempDir())
	sessionName := "enc-session"
	checkpointID := "20261112-210000.000-0123456789abcdef-enc"

	rawScrollback := "pane0 secret API_KEY=sk-ant-api03-supersecretvalue123456789\n"
	gzScrollbackPlain := "pane1 compressed secret BEARER_TOKEN=eyJhbGciOiJIUzI1NiJ9.secret\n"

	scrollRel0, err := storage.SaveScrollback(sessionName, checkpointID, "%0", rawScrollback)
	if err != nil {
		t.Fatalf("SaveScrollback: %v", err)
	}
	scrollRel1, err := storage.SaveCompressedScrollback(sessionName, checkpointID, "%1", mustGzipBytes(t, gzScrollbackPlain))
	if err != nil {
		t.Fatalf("SaveCompressedScrollback: %v", err)
	}
	if err := storage.SaveGitPatch(sessionName, checkpointID, gitPatch); err != nil {
		t.Fatalf("SaveGitPatch: %v", err)
	}
	if err := storage.SaveGitStatus(sessionName, checkpointID, gitStatus); err != nil {
		t.Fatalf("SaveGitStatus: %v", err)
	}

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		Name:        "encrypted-test",
		Description: "confidential-checkpoint-marker",
		SessionName: sessionName,
		WorkingDir:  repoDir,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, Width: 120, Height: 40, ScrollbackFile: scrollRel0, ScrollbackLines: 1},
				{ID: "%1", Index: 1, Width: 120, Height: 40, ScrollbackFile: scrollRel1, ScrollbackLines: 1},
			},
			ActivePaneIndex: 0,
		},
		Git: GitState{
			Branch:     "main",
			Commit:     commitSHA,
			IsDirty:    true,
			PatchFile:  GitPatchFile,
			StatusFile: GitStatusFile,
		},
		PaneCount: 2,
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("storage.Save: %v", err)
	}

	// 1. Every persisted artifact on disk MUST carry NTMENC1\n magic and hide plaintext markers.
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	for _, rel := range []string{
		MetadataFile,
		SessionFile,
		scrollRel0,
		scrollRel1,
		GitPatchFile,
		GitStatusFile,
	} {
		rawBytes, readErr := os.ReadFile(filepath.Join(cpDir, rel))
		if readErr != nil {
			t.Fatalf("read raw %s: %v", rel, readErr)
		}
		if !encryption.IsEncryptedArtifact(rawBytes) {
			t.Fatalf("expected %s to start with NTMENC1 magic, got prefix %q", rel, string(rawBytes[:min(len(rawBytes), 16)]))
		}
		for _, leaked := range []string{
			"confidential-checkpoint-marker",
			"supersecretvalue123456789",
			"eyJhbGciOiJIUzI1NiJ9",
			"patched-secret-token",
		} {
			if bytes.Contains(rawBytes, []byte(leaked)) {
				t.Fatalf("plaintext secret %q leaked into encrypted artifact %s", leaked, rel)
			}
		}
	}

	// 2. Integrity check, Load, scrollback reads, and Git rollback preflight all succeed with Key A.
	integrity := VerifyStoredCheckpoint(storage, sessionName, checkpointID)
	if !integrity.Valid {
		t.Fatalf("VerifyStoredCheckpoint failed: %+v", integrity)
	}

	loaded, err := storage.Load(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("storage.Load: %v", err)
	}
	if loaded.Description != cp.Description || len(loaded.Session.Panes) != 2 {
		t.Fatalf("loaded checkpoint mismatch: %+v", loaded)
	}

	gotPane0, err := storage.LoadScrollback(sessionName, checkpointID, "%0")
	if err != nil || gotPane0 != rawScrollback {
		t.Fatalf("LoadScrollback = (%q, %v), want %q", gotPane0, err, rawScrollback)
	}
	gotPane1, err := storage.LoadCompressedScrollback(sessionName, checkpointID, "%1")
	if err != nil || gotPane1 != gzScrollbackPlain {
		t.Fatalf("LoadCompressedScrollback = (%q, %v), want %q", gotPane1, err, gzScrollbackPlain)
	}

	plan, err := PrepareGitRollback(context.Background(), storage, loaded, repoDir)
	if err != nil {
		t.Fatalf("PrepareGitRollback: %v", err)
	}
	if plan.Patch != gitPatch {
		t.Fatalf("PrepareGitRollback patch mismatch:\ngot:  %q\nwant: %q", plan.Patch, gitPatch)
	}

	// 3. Export both .tar.gz and .zip under Key A -> verify archives are sealed on disk.
	tarGzPath := filepath.Join(t.TempDir(), "bundle.tar.gz")
	tarOpts := DefaultExportOptions()
	tarOpts.Format = FormatTarGz
	if _, err := storage.Export(sessionName, checkpointID, tarGzPath, tarOpts); err != nil {
		t.Fatalf("Export tar.gz: %v", err)
	}
	tarBytes, err := os.ReadFile(tarGzPath)
	if err != nil || !encryption.IsEncryptedArtifact(tarBytes) {
		t.Fatalf("expected exported tar.gz to be NTMENC1-encrypted, err=%v", err)
	}

	zipPath := filepath.Join(t.TempDir(), "bundle.zip")
	zipOpts := DefaultExportOptions()
	zipOpts.Format = FormatZip
	if _, err := storage.Export(sessionName, checkpointID, zipPath, zipOpts); err != nil {
		t.Fatalf("Export zip: %v", err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil || !encryption.IsEncryptedArtifact(zipBytes) {
		t.Fatalf("expected exported zip to be NTMENC1-encrypted, err=%v", err)
	}

	// 4. Missing-key refusal: clearing encryption config fails closed across Load, Verify, Scrollback, Import.
	SetEncryptionConfig(nil)
	if _, err := storage.Load(sessionName, checkpointID); err == nil {
		t.Fatal("expected storage.Load to fail closed when encryption key is missing")
	}
	if res := VerifyStoredCheckpoint(storage, sessionName, checkpointID); res.Valid {
		t.Fatal("expected VerifyStoredCheckpoint to fail closed when encryption key is missing")
	}
	if _, err := storage.LoadScrollback(sessionName, checkpointID, "%0"); err == nil {
		t.Fatal("expected LoadScrollback to fail closed when encryption key is missing")
	}
	importDestNoKey := NewStorageWithDir(t.TempDir())
	if _, err := importDestNoKey.Import(tarGzPath, DefaultImportOptions()); err == nil {
		t.Fatal("expected Import(tar.gz) to fail closed when encryption key is missing")
	}

	// 5. Keyring rotation: active key = Key B, keyring = [Key B, Key A].
	// Historical Key-A checkpoints and archives still load/import/export-with-redaction,
	// and newly written outputs are encrypted under Key B!
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  keyB,
		DecryptKeys: [][]byte{keyB, keyA},
	})

	if _, err := storage.Load(sessionName, checkpointID); err != nil {
		t.Fatalf("Load under rotated keyring [B, A] failed: %v", err)
	}

	redactedTarPath := filepath.Join(t.TempDir(), "redacted-rotated.tar.gz")
	redactOpts := DefaultExportOptions()
	redactOpts.Format = FormatTarGz
	redactOpts.RedactSecrets = true
	if _, err := storage.Export(sessionName, checkpointID, redactedTarPath, redactOpts); err != nil {
		t.Fatalf("Export with RedactSecrets under rotated keyring failed: %v", err)
	}

	importStorage := NewStorageWithDir(t.TempDir())
	impOpts := DefaultImportOptions()
	impOpts.TargetSession = "imported-tar"
	importedTar, err := importStorage.Import(redactedTarPath, impOpts)
	if err != nil {
		t.Fatalf("Import(redacted rotated tar.gz) failed: %v", err)
	}

	impZipOpts := DefaultImportOptions()
	impZipOpts.TargetSession = "imported-zip"
	if _, err := importStorage.Import(zipPath, impZipOpts); err != nil {
		t.Fatalf("Import(Key-A zip under [B, A] keyring) failed: %v", err)
	}

	// Drop Key A so ONLY Key B remains: newly imported/re-exported artifacts MUST still load under Key B alone!
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  keyB,
		DecryptKeys: [][]byte{keyB},
	})
	if _, err := importStorage.Load("imported-tar", importedTar.ID); err != nil {
		t.Fatalf("expected imported checkpoint re-encrypted under Key B to load with Key B only: %v", err)
	}
	redactedPane0, err := importStorage.LoadScrollback("imported-tar", importedTar.ID, "%0")
	if err != nil {
		t.Fatalf("LoadScrollback on Key-B imported checkpoint failed: %v", err)
	}
	if strings.Contains(redactedPane0, "supersecretvalue123456789") {
		t.Fatalf("expected RedactSecrets export to scrub secret from scrollback, got: %q", redactedPane0)
	}
	// Meanwhile, original Key-A checkpoint must refuse decryption when Key A is removed from keyring.
	if _, err := storage.Load(sessionName, checkpointID); err == nil {
		t.Fatal("expected Key-A checkpoint to fail decryption once Key A was removed from keyring")
	}

	// 6. Tamper refusal: flip 1 byte in ciphertext of imported Key-B scrollback and metadata -> fail closed!
	tamperScrollPath := filepath.Join(importStorage.CheckpointDir("imported-tar", importedTar.ID), scrollRel0)
	tamperBytes, err := os.ReadFile(tamperScrollPath)
	if err != nil {
		t.Fatalf("read tamper target: %v", err)
	}
	tamperBytes[len(tamperBytes)-1] ^= 0xff
	if err := os.WriteFile(tamperScrollPath, tamperBytes, 0o600); err != nil {
		t.Fatalf("write tampered scrollback: %v", err)
	}
	if _, err := importStorage.LoadScrollback("imported-tar", importedTar.ID, "%0"); err == nil {
		t.Fatal("expected tampered scrollback ciphertext to fail GCM authentication")
	}
	if res := VerifyStoredCheckpoint(importStorage, "imported-tar", importedTar.ID); res.Valid {
		t.Fatal("expected VerifyStoredCheckpoint to fail on tampered scrollback ciphertext")
	}

	tamperMetaPath := filepath.Join(importStorage.CheckpointDir("imported-tar", importedTar.ID), MetadataFile)
	metaCipher, err := os.ReadFile(tamperMetaPath)
	if err != nil {
		t.Fatalf("read metadata ciphertext: %v", err)
	}
	metaCipher[len(metaCipher)-1] ^= 0xff
	if err := os.WriteFile(tamperMetaPath, metaCipher, 0o600); err != nil {
		t.Fatalf("write tampered metadata: %v", err)
	}
	if _, err := importStorage.Load("imported-tar", importedTar.ID); err == nil {
		t.Fatal("expected tampered metadata ciphertext to fail GCM authentication")
	}
}
