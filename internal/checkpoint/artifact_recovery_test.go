package checkpoint

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreArtifactIntegrityPrecedesMutation(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		for _, allowDegraded := range []bool{false, true} {
			for _, target := range []string{"", "checksum-restore-target"} {
				t.Run(fmt.Sprintf("dry_run=%t/allow_degraded=%t/target=%s", dryRun, allowDegraded, target), func(t *testing.T) {
					storage, cp := newArtifactIntegrityFixture(t)
					if err := os.WriteFile(storage.GitPatchPath(cp.SessionName, cp.ID), []byte("bad!!"), 0600); err != nil {
						t.Fatal(err)
					}
					// A missing directory also prevents a real restore if the
					// integrity guard regresses; dry runs never mutate tmux.
					cp.WorkingDir = filepath.Join(t.TempDir(), "missing")
					cp.Session.Panes[0].AgentType = "cc"
					source := cp.SessionName
					result, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{
						Force: true, AllowDegradedContext: allowDegraded, DryRun: dryRun, TargetSession: target, SkipGitCheck: true,
					})
					if !errors.Is(err, ErrArtifactIntegrity) || !strings.Contains(err.Error(), "SHA-256 mismatch") {
						t.Fatalf("restore did not reject source corruption in preflight: %v", err)
					}
					if result == nil || result.Stage != "validating" || result.PanesRestored != 0 || result.ContextInjected {
						t.Fatalf("restore progressed past integrity validation: %+v", result)
					}
					if cp.SessionName != source || result.SourceSession != source {
						t.Fatal("restore lost or mutated the source storage namespace")
					}
				})
			}
		}
	}
}

func TestArtifactPreflightSourceAndLegacy(t *testing.T) {
	storage, cp := newArtifactIntegrityFixture(t)
	source := cp.SessionName
	cp.SessionName = "renamed-target"
	if _, err := preflightCheckpointArtifacts(context.Background(), storage, cp, source, false); err != nil {
		t.Fatalf("valid source checkpoint rejected for renamed target: %v", err)
	}
	if _, err := preflightCheckpointArtifacts(context.Background(), nil, cp, source, false); !errors.Is(err, ErrArtifactIntegrity) {
		t.Fatalf("missing storage accepted: %v", err)
	}
	cp.ArtifactIntegrity = nil
	warnings, err := preflightCheckpointArtifacts(context.Background(), nil, cp, source, false)
	if err != nil || !strings.Contains(strings.Join(warnings, "\n"), "legacy checkpoint") {
		t.Fatalf("legacy preflight: warnings=%v err=%v", warnings, err)
	}
}

func TestArtifactPreflightAllowDegradedContextInvariants(t *testing.T) {
	t.Run("missing recorded scrollback allowed only with AllowDegradedContext", func(t *testing.T) {
		storage, cp := newArtifactIntegrityFixture(t)
		scrollbackPath := filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), cp.Session.Panes[0].ScrollbackFile)
		if err := os.Remove(scrollbackPath); err != nil {
			t.Fatal(err)
		}
		if _, err := preflightCheckpointArtifacts(context.Background(), storage, cp, "", false); !errors.Is(err, ErrArtifactIntegrity) {
			t.Fatalf("expected ErrArtifactIntegrity without AllowDegradedContext, got: %v", err)
		}
		warnings, err := preflightCheckpointArtifacts(context.Background(), storage, cp, "", true)
		if err != nil {
			t.Fatalf("expected missing recorded scrollback to pass under AllowDegradedContext, got: %v", err)
		}
		if !strings.Contains(strings.Join(warnings, "\n"), cp.Session.Panes[0].ScrollbackFile) {
			t.Fatalf("expected degraded context warning, got: %v", warnings)
		}
	})

	t.Run("tampered existing scrollback fails closed even with AllowDegradedContext", func(t *testing.T) {
		storage, cp := newArtifactIntegrityFixture(t)
		scrollbackPath := filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), cp.Session.Panes[0].ScrollbackFile)
		if err := os.WriteFile(scrollbackPath, []byte("tampered scrollback"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := preflightCheckpointArtifacts(context.Background(), storage, cp, "", true); !errors.Is(err, ErrArtifactIntegrity) {
			t.Fatalf("tampered existing scrollback bypassed AllowDegradedContext: %v", err)
		}
	})

	t.Run("existing unrecorded scrollback fails closed even with AllowDegradedContext", func(t *testing.T) {
		storage, cp := newArtifactIntegrityFixture(t)
		unrecorded := "panes/unrecorded.txt"
		if err := os.WriteFile(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), unrecorded), []byte("unverified"), 0600); err != nil {
			t.Fatal(err)
		}
		cp.Session.Panes = append(cp.Session.Panes, PaneState{ID: "%9", Index: 1, ScrollbackFile: unrecorded})
		if _, err := preflightCheckpointArtifacts(context.Background(), storage, cp, "", true); !errors.Is(err, ErrArtifactIntegrity) {
			t.Fatalf("existing unrecorded scrollback bypassed AllowDegradedContext: %v", err)
		}
	})

	t.Run("missing git patch fails closed even with AllowDegradedContext", func(t *testing.T) {
		storage, cp := newArtifactIntegrityFixture(t)
		if err := os.Remove(storage.GitPatchPath(cp.SessionName, cp.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := preflightCheckpointArtifacts(context.Background(), storage, cp, "", true); !errors.Is(err, ErrArtifactIntegrity) {
			t.Fatalf("missing git patch bypassed AllowDegradedContext: %v", err)
		}
	})

	t.Run("path traversal fails closed even with AllowDegradedContext", func(t *testing.T) {
		storage, cp := newArtifactIntegrityFixture(t)
		cp.Session.Panes[0].ScrollbackFile = "../escape.txt"
		if _, err := preflightCheckpointArtifacts(context.Background(), storage, cp, "", true); !errors.Is(err, ErrArtifactIntegrity) {
			t.Fatalf("path traversal bypassed AllowDegradedContext: %v", err)
		}
	})
}

func TestArtifactVerificationCancellation(t *testing.T) {
	storage, cp := newArtifactIntegrityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := preflightCheckpointArtifacts(ctx, storage, cp, "", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("preflight ignored cancellation: %v", err)
	}
	path := storage.GitPatchPath(cp.SessionName, cp.ID)
	if err := verifyArtifactFileContext(ctx, path, cp.ArtifactIntegrity.Files[cp.Git.PatchFile]); !errors.Is(err, context.Canceled) {
		t.Fatalf("file verification ignored cancellation: %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	reader := artifactContextReader{ctx: ctx, reader: &artifactCancellingReader{cancel: cancel}}
	if _, err := io.Copy(io.Discard, reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("streaming verification ignored cancellation between reads: %v", err)
	}
}

type artifactCancellingReader struct{ cancel context.CancelFunc }

func (r *artifactCancellingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = 'x'
	r.cancel()
	return 1, nil
}

func TestArtifactIntegrityArchiveRoundTrip(t *testing.T) {
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		for _, selection := range []struct{ scrollback, patch bool }{{true, true}, {false, true}, {true, false}, {false, false}} {
			t.Run(fmt.Sprintf("%s/scrollback=%t/patch=%t", format, selection.scrollback, selection.patch), func(t *testing.T) {
				storage, cp := newArtifactIntegrityFixture(t)
				archive := filepath.Join(t.TempDir(), "checkpoint."+string(format))
				opts := DefaultExportOptions()
				opts.Format, opts.IncludeScrollback, opts.IncludeGitPatch = format, selection.scrollback, selection.patch
				if _, err := storage.Export(cp.SessionName, cp.ID, archive, opts); err != nil {
					t.Fatal(err)
				}
				destination := &Storage{BaseDir: t.TempDir()}
				imported, err := destination.Import(archive, ImportOptions{VerifyChecksums: true, TargetSession: "imported", TargetDir: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				result := VerifyStoredCheckpoint(destination, imported.SessionName, imported.ID)
				if !result.Valid || result.Details["checksum_status"] != "verified" {
					t.Fatalf("imported checkpoint invalid: %+v", result)
				}
				want := 1
				if selection.scrollback {
					want++
				}
				if selection.patch {
					want++
				}
				if len(imported.ArtifactIntegrity.Files) != want {
					t.Fatalf("export retained omitted fingerprints: %+v", imported.ArtifactIntegrity)
				}
				if sourceResult := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID); !sourceResult.Valid {
					t.Fatalf("export altered source: %+v", sourceResult)
				}
			})
		}
	}
}

func TestArtifactIntegrityExportPreparedBytes(t *testing.T) {
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			storage, cp := newArtifactIntegrityFixture(t)
			opts := DefaultExportOptions()
			exported := rewriteCheckpointForExport(cp, opts)
			rel := cp.Session.Panes[0].ScrollbackFile
			original := cp.ArtifactIntegrity.Files[rel]
			// The redaction stage supplies transformed bytes. Integrity must
			// follow those exact bytes, without changing the source manifest.
			prepared := map[string][]byte{rel: []byte("[REDACTED] transcript\n")}
			dir := storage.CheckpointDir(cp.SessionName, cp.ID)
			if err := prepareExportArtifactIntegrity(dir, exported, prepared); err != nil {
				t.Fatal(err)
			}
			if exported.ArtifactIntegrity.Files[rel] != checksumArtifactBytes(prepared[rel]) || cp.ArtifactIntegrity.Files[rel] != original {
				t.Fatal("export checksums do not match prepared bytes or mutated the source")
			}
			var archive bytes.Buffer
			manifest := &ExportManifest{Version: 1, Checksums: make(map[string]string)}
			files := checkpointPayloadFiles(exported)
			var err error
			if format == FormatZip {
				err = storage.exportZip(&archive, dir, exported, files, opts, manifest, prepared)
			} else {
				err = storage.exportTarGz(&archive, dir, exported, files, opts, manifest, prepared)
			}
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "prepared."+string(format))
			if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			destination := &Storage{BaseDir: t.TempDir()}
			imported, err := destination.Import(path, ImportOptions{VerifyChecksums: true, TargetDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if result := VerifyStoredCheckpoint(destination, imported.SessionName, imported.ID); !result.Valid {
				t.Fatalf("transformed export failed verification: %+v", result)
			}
		})
	}
}

func TestArtifactIntegrityExportRefusesDamagedSource(t *testing.T) {
	storage, cp := newArtifactIntegrityFixture(t)
	if err := os.WriteFile(storage.GitPatchPath(cp.SessionName, cp.ID), []byte("bad!!"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "existing.zip")
	original := []byte("previous recovery archive")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultExportOptions()
	opts.Format = FormatZip
	if _, err := storage.Export(cp.SessionName, cp.ID, path, opts); !errors.Is(err, ErrArtifactIntegrity) {
		t.Fatalf("export blessed damaged source: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("failed export replaced destination: %q %v", data, err)
	}
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		var archive bytes.Buffer
		manifest := &ExportManifest{Checksums: make(map[string]string)}
		var err error
		if format == FormatZip {
			err = storage.exportZip(&archive, storage.CheckpointDir(cp.SessionName, cp.ID), cp, checkpointPayloadFiles(cp), opts, manifest, nil)
		} else {
			err = storage.exportTarGz(&archive, storage.CheckpointDir(cp.SessionName, cp.ID), cp, checkpointPayloadFiles(cp), opts, manifest, nil)
		}
		if !errors.Is(err, ErrArtifactIntegrity) {
			t.Fatalf("%s writer ignored changed payload: %v", format, err)
		}
	}
}

func TestArtifactIntegrityImportRejectsCorruptionBeforeOverwrite(t *testing.T) {
	for _, verifyOuter := range []bool{false, true} {
		t.Run(fmt.Sprintf("verify_outer=%t", verifyOuter), func(t *testing.T) {
			storage, cp := newArtifactIntegrityFixture(t)
			files := make(map[string][]byte)
			for rel := range expectedManifestFiles(cp) {
				data, err := os.ReadFile(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), rel))
				if err != nil {
					t.Fatal(err)
				}
				files[rel] = data
			}
			files[cp.Git.PatchFile] = []byte("bad!!")
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			manifest := &ExportManifest{Version: 1, Checksums: make(map[string]string)}
			for rel, data := range files {
				if err := writeZipEntry(writer, rel, data); err != nil {
					t.Fatal(err)
				}
				// An outer manifest matching the damaged bytes cannot override
				// the checkpoint's original embedded integrity evidence.
				manifest.Checksums[rel] = sha256sum(data)
			}
			if verifyOuter {
				data, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeZipEntry(writer, "MANIFEST.json", data); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "corrupt.zip")
			if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if imported, err := storage.Import(path, ImportOptions{VerifyChecksums: verifyOuter, AllowOverwrite: true}); imported != nil || !errors.Is(err, ErrArtifactIntegrity) {
				t.Fatalf("damaged import reached publication: cp=%+v err=%v", imported, err)
			}
			if result := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID); !result.Valid {
				t.Fatalf("failed import damaged existing checkpoint: %+v", result)
			}
		})
	}
}

func TestArtifactIntegrityRedactionRejectsChangedSource(t *testing.T) {
	storage, cp := newArtifactIntegrityFixture(t)
	dir := storage.CheckpointDir(cp.SessionName, cp.ID)
	if err := os.WriteFile(filepath.Join(dir, cp.Session.Panes[0].ScrollbackFile), []byte("xyz"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultExportOptions()
	opts.RedactSecrets = true
	// Simulate a source change after Export's initial preflight. Reject the
	// bytes read for transformation rather than blessing the redacted damage.
	if _, err := prepareRedactedScrollbackArtifacts(dir, cp, opts); !errors.Is(err, ErrArtifactIntegrity) {
		t.Fatalf("redaction accepted changed source: %v", err)
	}
}

func TestArtifactIntegrityRejectsReservedPathAliases(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		storage, cp := newArtifactIntegrityFixture(t)
		cp.Git.PatchFile = MetadataFile
		if legacy {
			cp.ArtifactIntegrity = nil
		}
		if _, err := preflightCheckpointArtifacts(context.Background(), storage, cp, "", false); !errors.Is(err, ErrArtifactIntegrity) {
			t.Fatalf("reserved metadata alias accepted (legacy=%t): %v", legacy, err)
		}
	}
}
