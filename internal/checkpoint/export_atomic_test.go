package checkpoint

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckpointExportPublication(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			name := "new"
			if existing {
				name = "replace"
			}
			if fail {
				name += "-failure"
			}
			t.Run(name, func(t *testing.T) {
				dir, source := t.TempDir(), t.TempDir()
				dest := filepath.Join(dir, "backup.zip")
				old := "previous complete backup"
				if existing {
					if err := os.WriteFile(dest, []byte(old), 0644); err != nil {
						t.Fatal(err)
					}
				}
				writeErr := errors.New("artifact read failed")
				err := writeCheckpointExport(dest, source, func(w io.Writer) error {
					data, err := os.ReadFile(dest)
					if existing && (err != nil || string(data) != old) {
						t.Fatalf("old backup changed during write: %q, %v", data, err)
					}
					if !existing && !os.IsNotExist(err) {
						t.Fatalf("incomplete archive became visible: %v", err)
					}
					if _, err := io.WriteString(w, "new complete backup"); err != nil {
						return err
					}
					if fail {
						return writeErr
					}
					return nil
				})
				if fail && !errors.Is(err, writeErr) {
					t.Fatalf("error = %v, want original write failure", err)
				}
				if !fail && err != nil {
					t.Fatal(err)
				}
				data, readErr := os.ReadFile(dest)
				switch {
				case fail && !existing:
					if !os.IsNotExist(readErr) {
						t.Fatalf("failed export published output: %v", readErr)
					}
				case fail:
					if readErr != nil || string(data) != old {
						t.Fatalf("failed export destroyed backup: %q, %v", data, readErr)
					}
				default:
					if readErr != nil || string(data) != "new complete backup" {
						t.Fatalf("published archive = %q, %v", data, readErr)
					}
					info, err := os.Stat(dest)
					if err != nil {
						t.Fatal(err)
					}
					if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
						t.Fatalf("archive permissions = %o, want 600", info.Mode().Perm())
					}
				}
				assertNoExportStagingFiles(t, dir)
			})
		}
	}
}

func TestCheckpointExportRejectsUnsafeDestinations(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "checkpoint")
	if err := os.MkdirAll(filepath.Join(source, "panes"), 0700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(source, "metadata.json")
	if err := os.WriteFile(artifact, []byte("source checkpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "backup.zip")
	if err := os.Symlink(artifact, symlink); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "source-alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(root, "dangling.zip")
	if err := os.Symlink(filepath.Join(root, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{source, artifact, filepath.Join(source, "panes", "new.zip"), symlink, dangling, filepath.Join(alias, "metadata.json"), filepath.Join(root, "absent", "backup.zip")} {
		t.Run(filepath.Base(dest), func(t *testing.T) {
			called := false
			err := writeCheckpointExport(dest, source, func(io.Writer) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("unsafe destination accepted: %q, called=%v, err=%v", dest, called, err)
			}
		})
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "source checkpoint" {
		t.Fatalf("source corrupted: %q, %v", data, err)
	}
	assertNoExportStagingFiles(t, root)
}

func TestCheckpointExportAllowsSiblingAndHardlink(t *testing.T) {
	root := t.TempDir()
	source, sibling := filepath.Join(root, "checkpoint"), filepath.Join(root, "checkpoint-backups")
	for _, dir := range []string{source, sibling} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	artifact := filepath.Join(source, "metadata.json")
	if err := os.WriteFile(artifact, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(sibling, "backup.zip")
	if err := os.Link(artifact, dest); err != nil {
		t.Fatal(err)
	}
	if err := writeCheckpointExport(dest, source, func(w io.Writer) error { _, err := io.WriteString(w, "archive"); return err }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "source" {
		t.Fatalf("hardlinked source changed: %q, %v", data, err)
	}
}

func TestCheckpointExportRechecksDestinationBeforePublication(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	victim := filepath.Join(root, "other-backup.zip")
	if err := os.WriteFile(victim, []byte("other backup"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "backup.zip")
	err := writeCheckpointExport(dest, source, func(w io.Writer) error {
		if _, err := io.WriteString(w, "archive"); err != nil {
			return err
		}
		return os.Symlink(victim, dest)
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("destination replacement error = %v", err)
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "other backup" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	assertNoExportStagingFiles(t, root)
}

func TestCheckpointExportSyncFailurePreservesBackup(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "backup.zip")
	if err := os.WriteFile(dest, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	err := writeCheckpointExport(dest, t.TempDir(), func(w io.Writer) error { return w.(io.Closer).Close() })
	if err == nil || !strings.Contains(err.Error(), "syncing") {
		t.Fatalf("sync failure = %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "old" {
		t.Fatalf("old backup changed: %q, %v", data, err)
	}
	assertNoExportStagingFiles(t, root)
}

func assertNoExportStagingFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ntm-export-") {
			t.Errorf("staging file leaked: %s", entry.Name())
		}
	}
}
