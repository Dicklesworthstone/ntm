package cli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
)

// Exercise the existing Cobra import surface, not a second import engine.
// The archive passes metadata/path validation but fails on an actual file/dir
// collision. Neither a fresh destination nor --overwrite may publish half of it.
func TestCheckpointImportCommandLateFailurePreservesRecovery(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		for _, overwrite := range []bool{false, true} {
			name := format + "/new"
			if overwrite {
				name = format + "/overwrite"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("USERPROFILE", home)
				oldJSON := jsonOutput
				jsonOutput = false
				t.Cleanup(func() { jsonOutput = oldJSON })
				storage := checkpoint.NewStorage()
				cp := &checkpoint.Checkpoint{Version: 1, ID: "cp-cli-publication", Name: "old", SessionName: "source", Session: checkpoint.SessionState{Panes: []checkpoint.PaneState{}}}
				cpDir := storage.CheckpointDir("destination", cp.ID)
				var before map[string][]byte
				if overwrite {
					cp.SessionName = "destination"
					if err := storage.Save(cp); err != nil {
						t.Fatal(err)
					}
					before = readCLIImportFiles(t, cpDir)
					cp.SessionName = "source"
				}
				cp.Name = "new"
				cp.Git.PatchFile = "collision"
				cp.Git.StatusFile = "collision/status.txt"
				metadata, err := json.Marshal(cp)
				if err != nil {
					t.Fatal(err)
				}
				session, err := json.Marshal(cp.Session)
				if err != nil {
					t.Fatal(err)
				}
				files := map[string][]byte{"metadata.json": metadata, "session.json": session, "collision": []byte("patch"), "collision/status.txt": []byte("status")}
				archive := filepath.Join(t.TempDir(), "checkpoint."+format)
				f, err := os.Create(archive)
				if err != nil {
					t.Fatal(err)
				}
				if format == "zip" {
					zw := zip.NewWriter(f)
					for name, data := range files {
						w, err := zw.Create(name)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := w.Write(data); err != nil {
							t.Fatal(err)
						}
					}
					if err := zw.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					gw := gzip.NewWriter(f)
					tw := tar.NewWriter(gw)
					for name, data := range files {
						if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
							t.Fatal(err)
						}
						if _, err := tw.Write(data); err != nil {
							t.Fatal(err)
						}
					}
					if err := tw.Close(); err != nil {
						t.Fatal(err)
					}
					if err := gw.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				cmd := newCheckpointImportCmd()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				args := []string{archive, "--session=destination", "--skip-verify"}
				if overwrite {
					args = append(args, "--overwrite")
				}
				cmd.SetArgs(args)
				if err := cmd.Execute(); err == nil {
					t.Fatal("filesystem collision reported successful import")
				}
				if overwrite {
					if after := readCLIImportFiles(t, cpDir); !reflect.DeepEqual(before, after) {
						t.Fatalf("failed CLI overwrite changed recovery files: %q != %q", after, before)
					}
				} else if _, err := os.Stat(cpDir); !os.IsNotExist(err) {
					t.Fatal("failed CLI import left a partial checkpoint", err)
				}
			})
		}
	}
}

func readCLIImportFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel], err = os.ReadFile(path)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
