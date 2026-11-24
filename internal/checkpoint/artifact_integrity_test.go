package checkpoint

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newArtifactIntegrityFixture(t *testing.T) (*Storage, *Checkpoint) {
	t.Helper()
	storage := &Storage{BaseDir: t.TempDir()}
	cp := &Checkpoint{
		Version: CurrentVersion, ID: "checksum-test", Name: "checksum-test",
		SessionName: "checksum-session", WorkingDir: t.TempDir(), CreatedAt: time.Now(),
		PaneCount: 1,
		Session:   SessionState{Panes: []PaneState{{ID: "%0", Width: 80, Height: 24, AgentType: "user"}}},
	}
	// Exercise the same initial-save / capture / final-save lifecycle as Create.
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	rel, err := storage.SaveScrollback(cp.SessionName, cp.ID, "%0", "abc")
	if err != nil {
		t.Fatal(err)
	}
	cp.Session.Panes[0].ScrollbackFile = rel
	if err := storage.SaveGitPatch(cp.SessionName, cp.ID, "patch"); err != nil {
		t.Fatal(err)
	}
	cp.Git.PatchFile = GitPatchFile
	if err := storage.SaveGitStatus(cp.SessionName, cp.ID, "status"); err != nil {
		t.Fatal(err)
	}
	cp.Git.StatusFile = GitStatusFile
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	return storage, cp
}

func TestArtifactIntegrityPersistsAllPayloads(t *testing.T) {
	t.Parallel()
	storage, cp := newArtifactIntegrityFixture(t)
	loaded, err := storage.Load(cp.SessionName, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ArtifactIntegrity == nil || len(loaded.ArtifactIntegrity.Files) != 3 {
		t.Fatalf("manifest not persisted: %+v", loaded.ArtifactIntegrity)
	}
	result := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID)
	if !result.Valid || result.Details["checksum_status"] != "verified" || result.Details["checksums_checked"] != "3" {
		t.Fatalf("unexpected verification: %+v", result)
	}
}

func TestArtifactIntegrityRejectsChangedPayloads(t *testing.T) {
	t.Parallel()
	for _, artifact := range []string{"scrollback", "patch", "status"} {
		artifact := artifact
		t.Run(artifact, func(t *testing.T) {
			t.Parallel()
			storage, cp := newArtifactIntegrityFixture(t)
			rel := cp.Session.Panes[0].ScrollbackFile
			if artifact == "patch" {
				rel = cp.Git.PatchFile
			} else if artifact == "status" {
				rel = cp.Git.StatusFile
			}
			path := filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), rel)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[0] ^= 1 // Same-size corruption must not escape detection.
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			result := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID)
			if result.Valid || result.ConsistencyValid || result.Details["checksum_status"] != "failed" {
				t.Fatalf("corruption accepted: %+v", result)
			}
			if !strings.Contains(strings.Join(result.Errors, "\n"), "SHA-256 mismatch") {
				t.Fatalf("missing checksum failure: %v", result.Errors)
			}
		})
	}
}

func TestArtifactIntegrityRejectsIncompleteOrInvalidManifest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*Checkpoint)
		want string
	}{
		{"missing_entry", func(cp *Checkpoint) { delete(cp.ArtifactIntegrity.Files, cp.Git.PatchFile) }, "missing checksum"},
		{"missing_map", func(cp *Checkpoint) { cp.ArtifactIntegrity.Files = nil }, "no files map"},
		{"future_version", func(cp *Checkpoint) { cp.ArtifactIntegrity.Version++ }, "unsupported artifact integrity version"},
		{"unexpected_path", func(cp *Checkpoint) { cp.ArtifactIntegrity.Files["../../outside"] = ArtifactChecksum{} }, "unreferenced artifact"},
		{"invalid_digest", func(cp *Checkpoint) {
			cp.ArtifactIntegrity.Files[cp.Git.PatchFile] = ArtifactChecksum{SHA256: "invalid"}
		}, "invalid SHA-256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage, cp := newArtifactIntegrityFixture(t)
			tc.edit(cp)
			// Write damaged metadata without invoking Save, which records checksums.
			if err := writeJSON(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), MetadataFile), cp); err != nil {
				t.Fatal(err)
			}
			result := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID)
			if result.Valid || !strings.Contains(strings.Join(result.Errors, "\n"), tc.want) {
				t.Fatalf("result = %+v, want %q", result, tc.want)
			}
		})
	}
}

func TestArtifactIntegrityLegacyCheckpointWarns(t *testing.T) {
	t.Parallel()
	storage, cp := newArtifactIntegrityFixture(t)
	cp.ArtifactIntegrity = nil
	if err := writeJSON(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), MetadataFile), cp); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Load(cp.SessionName, cp.ID); err != nil {
		t.Fatalf("legacy checkpoint no longer loads: %v", err)
	}
	result := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID)
	if !result.Valid || result.Details["checksum_status"] != "unavailable" || !strings.Contains(strings.Join(result.Warnings, "\n"), "legacy checkpoint") {
		t.Fatalf("legacy compatibility not explicit: %+v", result)
	}
}

func TestArtifactIntegrityResaveDoesNotBlessCorruption(t *testing.T) {
	t.Parallel()
	storage, cp := newArtifactIntegrityFixture(t)
	dir := storage.CheckpointDir(cp.SessionName, cp.ID)
	before, err := os.ReadFile(filepath.Join(dir, MetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cp.Git.PatchFile), []byte("bad!!"), 0600); err != nil {
		t.Fatal(err)
	}
	cp.Name = "renamed"
	if err := storage.Save(cp); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("metadata update blessed corruption: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, MetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed save modified persisted metadata")
	}
}

func TestArtifactIntegrityCompressedScrollbackAndPruning(t *testing.T) {
	t.Parallel()
	storage, cp := newArtifactIntegrityFixture(t)
	dir := storage.CheckpointDir(cp.SessionName, cp.ID)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte("compressed scrollback\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	oldRel := cp.Session.Panes[0].ScrollbackFile
	rel := oldRel + ".gz"
	if err := os.WriteFile(filepath.Join(dir, rel), compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	cp.Session.Panes[0].ScrollbackFile = rel
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	if _, ok := cp.ArtifactIntegrity.Files[oldRel]; ok {
		t.Fatal("manifest retained a removed reference")
	}
	if got := cp.ArtifactIntegrity.Files[rel].SizeBytes; got != int64(compressed.Len()) {
		t.Fatalf("stored size = %d, want compressed size %d", got, compressed.Len())
	}
	if result := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID); !result.Valid {
		t.Fatalf("compressed artifact rejected: %+v", result)
	}
}
