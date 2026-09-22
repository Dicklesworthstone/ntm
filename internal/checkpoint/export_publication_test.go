package checkpoint

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExportFormatsPublishRestorableArchives(t *testing.T) {
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			storage := NewStorageWithDir(t.TempDir())
			cp := &Checkpoint{
				Version: CurrentVersion, ID: "20260922-120000-atomic",
				SessionName: "atomic-export", WorkingDir: t.TempDir(), CreatedAt: time.Now(),
				Session: SessionState{Panes: []PaneState{{ID: "%0", Index: 0}}}, PaneCount: 1,
			}
			if err := storage.Save(cp); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "backup."+string(format))
			if err := os.WriteFile(dest, []byte("previous backup"), 0644); err != nil {
				t.Fatal(err)
			}
			opts := DefaultExportOptions()
			opts.Format = format
			manifest, err := storage.Export(cp.SessionName, cp.ID, dest, opts)
			if err != nil || manifest == nil {
				t.Fatalf("Export = %v, %v", manifest, err)
			}
			imported, err := NewStorageWithDir(t.TempDir()).Import(dest, ImportOptions{VerifyChecksums: true, TargetDir: cp.WorkingDir})
			if err != nil || imported == nil || imported.ID != cp.ID {
				t.Fatalf("published archive is not restorable: %v, %v", imported, err)
			}
			cpDir, err := storage.safeCheckpointDir(cp.SessionName, cp.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := storage.Export(cp.SessionName, cp.ID, filepath.Join(cpDir, MetadataFile), opts); err == nil {
				t.Fatal("Export accepted its own checkpoint metadata as the destination")
			}
			if _, err := storage.Load(cp.SessionName, cp.ID); err != nil {
				t.Fatalf("source checkpoint damaged: %v", err)
			}
			assertNoExportStagingFiles(t, filepath.Dir(dest))
		})
	}
}

func TestExportFormatsPreserveBackupOnLateArtifactFailure(t *testing.T) {
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			dir, source := t.TempDir(), t.TempDir()
			dest := filepath.Join(dir, "backup."+string(format))
			if err := os.WriteFile(dest, []byte("previous backup"), 0600); err != nil {
				t.Fatal(err)
			}
			// Metadata and session bytes are written before this missing artifact
			// is encountered. This reproduces failure after archive creation.
			cp := &Checkpoint{ID: "atomic", SessionName: "session"}
			manifest := &ExportManifest{Checksums: make(map[string]string)}
			storage := &Storage{}
			err := writeCheckpointExport(dest, source, func(w io.Writer) error {
				files := []string{MetadataFile, SessionFile, "panes/missing.txt"}
				if format == FormatTarGz {
					return storage.exportTarGz(w, source, cp, files, ExportOptions{}, manifest, nil)
				}
				return storage.exportZip(w, source, cp, files, ExportOptions{}, manifest, nil)
			})
			if err == nil {
				t.Fatal("missing artifact did not fail the export")
			}
			data, err := os.ReadFile(dest)
			if err != nil || string(data) != "previous backup" {
				t.Fatalf("failed export replaced old backup: %q, %v", data, err)
			}
			assertNoExportStagingFiles(t, dir)
		})
	}
}

type failingCheckpointExportWriter struct{ err error }

func (w failingCheckpointExportWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExportFormatsPropagateWriterFailure(t *testing.T) {
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			writeErr := errors.New("export device full")
			writer := failingCheckpointExportWriter{err: writeErr}
			manifest := &ExportManifest{Checksums: make(map[string]string)}
			storage := &Storage{}
			var err error
			if format == FormatTarGz {
				err = storage.exportTarGz(writer, t.TempDir(), &Checkpoint{}, nil, ExportOptions{}, manifest, nil)
			} else {
				err = storage.exportZip(writer, t.TempDir(), &Checkpoint{}, nil, ExportOptions{}, manifest, nil)
			}
			if !errors.Is(err, writeErr) {
				t.Fatalf("export error = %v, want underlying write failure", err)
			}
		})
	}
}
