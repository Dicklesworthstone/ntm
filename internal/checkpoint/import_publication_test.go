package checkpoint

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

func publicationCheckpoint(generation string) *Checkpoint {
	return &Checkpoint{
		Version: 1, ID: "cp-import-publication", Name: generation,
		SessionName: "source", WorkingDir: "${WORKING_DIR}",
		CreatedAt: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		Session:   SessionState{Panes: []PaneState{}},
	}
}

func writePublicationArchive(t *testing.T, format ExportFormat, cp *Checkpoint, artifacts map[string][]byte) string {
	t.Helper()
	files := make(map[string][]byte)
	for name, data := range artifacts {
		files[name] = data
	}
	var err error
	files[MetadataFile], err = json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	files[SessionFile], err = json.Marshal(cp.Session)
	if err != nil {
		t.Fatal(err)
	}
	manifest := ExportManifest{Version: 1, SessionName: cp.SessionName, CheckpointID: cp.ID, CheckpointName: cp.Name, Checksums: make(map[string]string)}
	for name, data := range files {
		sum := sha256sum(data)
		manifest.Checksums[name] = sum
		manifest.Files = append(manifest.Files, ManifestEntry{Path: name, Size: int64(len(data)), Checksum: sum})
	}
	files["MANIFEST.json"], err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "checkpoint."+string(format))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	if format == FormatZip {
		zw := zip.NewWriter(f)
		for _, name := range names {
			if err := writeZipEntry(zw, name, files[name]); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gw := gzip.NewWriter(f)
		tw := tar.NewWriter(gw)
		for _, name := range names {
			if err := writeTarEntry(tw, name, files[name]); err != nil {
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
	return path
}

func TestImportPublishesCompleteArchiveWithOverrides(t *testing.T) {
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			storage := &Storage{BaseDir: t.TempDir()}
			cp := publicationCheckpoint("new")
			cp.Session.Panes = []PaneState{{ID: "%7", AgentType: "cc", ScrollbackFile: "panes/window/agent.txt"}}
			cp.PaneCount = 1
			cp.Git = GitState{PatchFile: "git.patch", StatusFile: "git-status.txt"}
			artifacts := map[string][]byte{"panes/window/agent.txt": []byte("saved context"), "git.patch": {0, 1, 2, 255}, "git-status.txt": []byte(" M file.go\n")}
			archive := writePublicationArchive(t, format, cp, artifacts)
			dir := t.TempDir()
			out, err := storage.Import(archive, ImportOptions{TargetSession: "destination", TargetDir: dir, VerifyChecksums: true})
			if err != nil || out == nil || out.SessionName != "destination" || out.WorkingDir != dir || out.PaneCount != 1 {
				t.Fatalf("import: %+v %v", out, err)
			}
			cpDir := filepath.Join(storage.BaseDir, out.SessionName, out.ID)
			files := readImportPublicationFiles(t, cpDir)
			if len(files) != len(artifacts)+2 {
				t.Fatalf("files=%v", files)
			}
			for name, want := range artifacts {
				if !reflect.DeepEqual(files[name], want) {
					t.Fatalf("%s: %v != %v", name, files[name], want)
				}
			}
			var saved Checkpoint
			if err := json.Unmarshal(files[MetadataFile], &saved); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*out, saved) {
				t.Fatal("returned checkpoint differs from installed metadata")
			}
			if err := validateImportedSessionState(files, &saved); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(storage.BaseDir, "source")); !os.IsNotExist(err) {
				t.Fatal("source namespace modified", err)
			}
			assertNoImportStaging(t, filepath.Dir(cpDir))
		})
	}
}

func TestImportLateFilesystemFailureDoesNotInstallPartialCheckpoint(t *testing.T) {
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		for _, overwrite := range []bool{false, true} {
			name := string(format) + "/new"
			if overwrite {
				name = string(format) + "/overwrite"
			}
			t.Run(name, func(t *testing.T) {
				storage := &Storage{BaseDir: t.TempDir()}
				cp := publicationCheckpoint("old")
				cpDir := filepath.Join(storage.BaseDir, cp.SessionName, cp.ID)
				var before map[string][]byte
				var original os.FileInfo
				if overwrite {
					archive := writePublicationArchive(t, format, cp, nil)
					if _, err := storage.Import(archive, ImportOptions{VerifyChecksums: true}); err != nil {
						t.Fatal(err)
					}
					before = readImportPublicationFiles(t, cpDir)
					original, _ = os.Stat(cpDir)
				}
				// Both names are valid archive entries and valid distinct git
				// references, but a regular file cannot also be a directory. This
				// reaches real filesystem staging, without an injected I/O failure.
				cp.Name = "new"
				cp.Git.PatchFile = "collision"
				cp.Git.StatusFile = "collision/status.txt"
				archive := writePublicationArchive(t, format, cp, map[string][]byte{"collision": []byte("patch"), "collision/status.txt": []byte("status")})
				out, err := storage.Import(archive, ImportOptions{AllowOverwrite: overwrite, VerifyChecksums: true})
				if out != nil || err == nil {
					t.Fatalf("invalid filesystem tree accepted: %+v %v", out, err)
				}
				if overwrite {
					if got := readImportPublicationFiles(t, cpDir); !reflect.DeepEqual(before, got) {
						t.Fatalf("failed overwrite changed old recovery files: %q != %q", got, before)
					}
					after, err := os.Stat(cpDir)
					if err != nil || !os.SameFile(original, after) {
						t.Fatal("failed import moved the old checkpoint", err)
					}
				} else if _, err := os.Stat(cpDir); !os.IsNotExist(err) {
					t.Fatal("failed import published a partial directory", err)
				}
				assertNoImportStaging(t, filepath.Dir(cpDir))
			})
		}
	}
}

func TestImportOverwriteUsesNewDirectoryGeneration(t *testing.T) {
	requireNativeImportExchange(t)
	for _, format := range []ExportFormat{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			storage := &Storage{BaseDir: t.TempDir()}
			cp := publicationCheckpoint("old")
			cp.Git.PatchFile = "git.patch"
			archive := writePublicationArchive(t, format, cp, map[string][]byte{"git.patch": []byte("old")})
			if _, err := storage.Import(archive, ImportOptions{VerifyChecksums: true}); err != nil {
				t.Fatal(err)
			}
			cpDir := filepath.Join(storage.BaseDir, cp.SessionName, cp.ID)
			original, err := os.Stat(cpDir)
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "linked-old-patch")
			if err := os.Link(filepath.Join(cpDir, "git.patch"), outside); err != nil {
				t.Fatal(err)
			}
			cp.Name = "new"
			archive = writePublicationArchive(t, format, cp, map[string][]byte{"git.patch": []byte("new")})
			if _, err := storage.Import(archive, ImportOptions{VerifyChecksums: true}); !errors.Is(err, os.ErrExist) {
				t.Fatalf("existing destination was not refused: %v", err)
			}
			out, err := storage.Import(archive, ImportOptions{AllowOverwrite: true, VerifyChecksums: true})
			if err != nil || out == nil || out.Name != "new" {
				t.Fatal(out, err)
			}
			after, _ := os.Stat(cpDir)
			if os.SameFile(original, after) {
				t.Fatal("import reused old directory and overwrote individual artifacts")
			}
			data, err := os.ReadFile(filepath.Join(cpDir, "git.patch"))
			if err != nil || string(data) != "new" {
				t.Fatal(string(data), err)
			}
			data, err = os.ReadFile(outside)
			if err != nil || string(data) != "old" {
				t.Fatal("existing hardlink target was changed", string(data), err)
			}
			assertNoImportStaging(t, filepath.Dir(cpDir))
		})
	}
}

func TestImportConcurrentArchiveCreateHasOneWinner(t *testing.T) {
	requireNativeImportExchange(t)
	storage := &Storage{BaseDir: t.TempDir()}
	cp := publicationCheckpoint("winner")
	archive := writePublicationArchive(t, FormatZip, cp, nil)
	const count = 16
	start := make(chan struct{})
	results := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := storage.Import(archive, ImportOptions{VerifyChecksums: true})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrCheckpointImportBusy) && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent successful imports=%d want=1", winners)
	}
	files := readImportPublicationFiles(t, filepath.Join(storage.BaseDir, cp.SessionName, cp.ID))
	if len(files) != 2 {
		t.Fatal("incomplete result", files)
	}
}
