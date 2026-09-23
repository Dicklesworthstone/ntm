package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func importPublicationFiles(generation string) map[string][]byte {
	return map[string][]byte{
		MetadataFile:             []byte("metadata:" + generation),
		SessionFile:              []byte("session:" + generation),
		"panes/agent/output.txt": []byte("context:" + generation),
		"git.patch":              []byte("patch:" + generation),
	}
}

func readImportPublicationFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func seedImportPublication(t *testing.T, dir, generation string) {
	t.Helper()
	published, err := publishCheckpointImport(dir, importPublicationFiles(generation), false)
	if err != nil || !published {
		t.Fatalf("seed: published=%v err=%v", published, err)
	}
}

func assertImportPublicationFiles(t *testing.T, dir, generation string) {
	t.Helper()
	if got := readImportPublicationFiles(t, dir); !reflect.DeepEqual(got, importPublicationFiles(generation)) {
		t.Fatalf("mixed or partial checkpoint: got %q, want generation %q", got, generation)
	}
}

func assertNoImportStaging(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ntm-import-") {
			t.Fatalf("staging leaked: %s", entry.Name())
		}
	}
}

func requireNativeImportExchange(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native directory exchange requires Linux or macOS")
	}
}

func TestCheckpointImportStagesBeforeVisibility(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session", "cp")
	p := newCheckpointImportPublisher()
	writes := 0
	p.writeFile = func(path string, data []byte) error {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("checkpoint visible before all writes: %v", err)
		}
		writes++
		return writeCheckpointImportFile(path, data)
	}
	files := importPublicationFiles("new")
	files["MANIFEST.json"] = []byte("not installed")
	published, err := p.publish(dir, files, false)
	if err != nil || !published || writes != 4 {
		t.Fatalf("published=%v writes=%d err=%v", published, writes, err)
	}
	assertImportPublicationFiles(t, dir, "new")
	assertNoImportStaging(t, filepath.Dir(dir))
	if runtime.GOOS != "windows" {
		if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			want := os.FileMode(0600)
			if d.IsDir() {
				want = 0700
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode=%o want=%o", path, info.Mode().Perm(), want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckpointImportLateWriteFailurePreservesRecovery(t *testing.T) {
	cause := errors.New("disk full after earlier staged writes")
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprint(overwrite), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session", "cp")
			if overwrite {
				seedImportPublication(t, dir, "old")
			}
			original, _ := os.Stat(dir)
			p := newCheckpointImportPublisher()
			writes := 0
			p.writeFile = func(path string, data []byte) error {
				writes++
				if filepath.Base(path) == SessionFile {
					return cause
				}
				return writeCheckpointImportFile(path, data)
			}
			published, err := p.publish(dir, importPublicationFiles("new"), overwrite)
			if published || !errors.Is(err, cause) || writes != 4 {
				t.Fatalf("published=%v writes=%d err=%v", published, writes, err)
			}
			if overwrite {
				assertImportPublicationFiles(t, dir, "old")
				after, err := os.Stat(dir)
				if err != nil || !os.SameFile(original, after) {
					t.Fatal("old checkpoint was moved on staging failure")
				}
			} else if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("partial new checkpoint exists: %v", err)
			}
			assertNoImportStaging(t, filepath.Dir(dir))
		})
	}
}

func TestCheckpointImportRejectsPathsBeforeCreatingParent(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "bad\\path", "C:drive", "panes/../escape", "./alias", ""} {
		t.Run(name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "not-created")
			files := importPublicationFiles("new")
			files[name] = []byte("bad")
			if published, err := publishCheckpointImport(filepath.Join(parent, "cp"), files, false); err == nil || published {
				t.Fatal("invalid path accepted")
			}
			if _, err := os.Stat(parent); !os.IsNotExist(err) {
				t.Fatalf("invalid path created parent: %v", err)
			}
		})
	}
}

func TestCheckpointImportStageSyncFailureLeavesOldCheckpoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	seedImportPublication(t, dir, "old")
	cause := errors.New("staging fsync failed")
	p := newCheckpointImportPublisher()
	p.syncDirectory = func(*os.File) error { return cause }
	published, err := p.publish(dir, importPublicationFiles("new"), true)
	if published || !errors.Is(err, cause) {
		t.Fatalf("published=%v err=%v", published, err)
	}
	assertImportPublicationFiles(t, dir, "old")
	assertNoImportStaging(t, filepath.Dir(dir))
}

func TestCheckpointImportAtomicSwapRetainsOldOpenFile(t *testing.T) {
	requireNativeImportExchange(t)
	dir := filepath.Join(t.TempDir(), "cp")
	seedImportPublication(t, dir, "old")
	old, err := os.Open(filepath.Join(dir, MetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	oldDir, _ := os.Stat(dir)
	p := newCheckpointImportPublisher()
	p.rename = func(parent *os.File, stage, target string, replace bool) error {
		assertImportPublicationFiles(t, dir, "old")
		if !replace {
			t.Fatal("overwrite did not use exchange")
		}
		if err := renameCheckpointImport(parent, stage, target, replace); err != nil {
			return err
		}
		assertImportPublicationFiles(t, dir, "new")
		assertImportPublicationFiles(t, filepath.Join(parent.Name(), stage), "old")
		return nil
	}
	published, err := p.publish(dir, importPublicationFiles("new"), true)
	if err != nil || !published {
		t.Fatalf("published=%v err=%v", published, err)
	}
	newDir, _ := os.Stat(dir)
	if os.SameFile(oldDir, newDir) {
		t.Fatal("overwrite modified the old directory instead of publishing a new generation")
	}
	data := make([]byte, 64)
	n, err := old.Read(data)
	if err != nil || string(data[:n]) != "metadata:old" {
		t.Fatalf("old open artifact was modified: %q %v", data[:n], err)
	}
	assertNoImportStaging(t, filepath.Dir(dir))
}

func TestCheckpointImportPublicationErrorRetainsEvidence(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(fmt.Sprint(afterRename), func(t *testing.T) {
			requireNativeImportExchange(t)
			dir := filepath.Join(t.TempDir(), "cp")
			seedImportPublication(t, dir, "old")
			p := newCheckpointImportPublisher()
			cause := errors.New("rename acknowledgement failed")
			p.rename = func(parent *os.File, stage, target string, replace bool) error {
				if afterRename {
					if err := renameCheckpointImport(parent, stage, target, replace); err != nil {
						return err
					}
				}
				return cause
			}
			published, err := p.publish(dir, importPublicationFiles("new"), true)
			var receipt *ImportPublicationError
			if published != afterRename || !errors.Is(err, cause) || !errors.As(err, &receipt) || receipt.Published != afterRename {
				t.Fatalf("published=%v receipt=%+v err=%v", published, receipt, err)
			}
			wantCurrent, wantRetained := "old", "new"
			if afterRename {
				wantCurrent, wantRetained = "new", "old"
			}
			assertImportPublicationFiles(t, dir, wantCurrent)
			assertImportPublicationFiles(t, receipt.RetainedDir, wantRetained)
		})
	}
}

func TestCheckpointImportPostPublicationSyncFailureKeepsBothVersions(t *testing.T) {
	requireNativeImportExchange(t)
	dir := filepath.Join(t.TempDir(), "cp")
	seedImportPublication(t, dir, "old")
	p := newCheckpointImportPublisher()
	cause := errors.New("parent sync failed after exchange")
	parentPath, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	p.syncDirectory = func(f *os.File) error {
		if f.Name() == parentPath {
			return cause
		}
		return syncCheckpointImportDirectory(f)
	}
	published, err := p.publish(dir, importPublicationFiles("new"), true)
	var receipt *ImportPublicationError
	if !published || !errors.Is(err, cause) || !errors.As(err, &receipt) || !receipt.Published {
		t.Fatalf("published=%v err=%v", published, err)
	}
	assertImportPublicationFiles(t, dir, "new")
	assertImportPublicationFiles(t, receipt.RetainedDir, "old")
	// Even an error releases the directory lock; the retained old generation
	// cannot be mistaken for the destination of another explicit import.
	if ok, err := publishCheckpointImport(dir, importPublicationFiles("next"), true); !ok || err != nil {
		t.Fatal(ok, err)
	}
	assertImportPublicationFiles(t, dir, "next")
	assertImportPublicationFiles(t, receipt.RetainedDir, "old")
}

func TestCheckpointImportRefusesExistingArtifactsAndSymlinks(t *testing.T) {
	for _, kind := range []string{"no-overwrite", "stale-file", "artifact-symlink", "destination-symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cp")
			seedImportPublication(t, dir, "old")
			original := dir
			switch kind {
			case "stale-file":
				if err := os.WriteFile(filepath.Join(dir, "unrelated"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "artifact-symlink":
				path := filepath.Join(dir, "git.patch")
				outside := filepath.Join(t.TempDir(), "patch")
				if err := os.Rename(path, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Skip(err)
				}
			case "destination-symlink":
				original = dir + "-saved"
				if err := os.Rename(dir, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, dir); err != nil {
					t.Skip(err)
				}
			}
			before := readImportPublicationFiles(t, original)
			published, err := publishCheckpointImport(dir, importPublicationFiles("new"), kind != "no-overwrite")
			if published || err == nil {
				t.Fatal("unsafe overwrite accepted")
			}
			if after := readImportPublicationFiles(t, original); !reflect.DeepEqual(before, after) {
				t.Fatal("refused import changed previous files")
			}
			assertNoImportStaging(t, filepath.Dir(dir))
		})
	}
}

func TestCheckpointImportConcurrentCreateHasOneWinner(t *testing.T) {
	requireNativeImportExchange(t)
	dir := filepath.Join(t.TempDir(), "cp")
	const workers = 16
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := publishCheckpointImport(dir, importPublicationFiles("winner"), false)
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
			continue
		}
		if !errors.Is(err, ErrCheckpointImportBusy) && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d want=1", winners)
	}
	assertImportPublicationFiles(t, dir, "winner")
	assertNoImportStaging(t, filepath.Dir(dir))
}

func TestCheckpointImportCrashLeavesWholeGeneration(t *testing.T) {
	requireNativeImportExchange(t)
	if phase := os.Getenv("NTM_TEST_IMPORT_CRASH_PHASE"); phase != "" {
		dir := os.Getenv("NTM_TEST_IMPORT_CRASH_DIR")
		p := newCheckpointImportPublisher()
		p.rename = func(parent *os.File, stage, target string, replace bool) error {
			if phase == "before" {
				os.Exit(73)
			}
			if err := renameCheckpointImport(parent, stage, target, replace); err != nil {
				return err
			}
			os.Exit(73)
			return nil
		}
		_, err := p.publish(dir, importPublicationFiles("new"), true)
		t.Fatalf("child failed to reach crash point: %v", err)
	}
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cp")
			seedImportPublication(t, dir, "old")
			cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointImportCrashLeavesWholeGeneration$")
			cmd.Env = append(os.Environ(), "NTM_TEST_IMPORT_CRASH_PHASE="+phase, "NTM_TEST_IMPORT_CRASH_DIR="+dir)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("child: %v %s", err, output)
			}
			want := "old"
			if phase == "after" {
				want = "new"
			}
			assertImportPublicationFiles(t, dir, want)
			// A crashed writer leaves no stale lock ownership. A new explicit
			// overwrite publishes normally, without replaying the interrupted one.
			if ok, err := publishCheckpointImport(dir, importPublicationFiles("recovered"), true); !ok || err != nil {
				t.Fatal(ok, err)
			}
			assertImportPublicationFiles(t, dir, "recovered")
		})
	}
}
