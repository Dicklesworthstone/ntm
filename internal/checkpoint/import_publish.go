package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

var (
	ErrCheckpointImportBusy              = errors.New("another checkpoint import is publishing in this session")
	ErrCheckpointImportAtomicUnavailable = errors.New("atomic checkpoint directory replacement is unavailable")
)

// ImportPublicationError retains evidence when publication or its finalization
// failed. Published means the complete new directory was observed at its target;
// it must not be interpreted as a rollback. RetainedDir is never auto-deleted on
// a publication error. It may contain the old checkpoint after an exchange, or
// the new staging directory when publication did not occur.
type ImportPublicationError struct {
	CheckpointDir string
	RetainedDir   string
	Published     bool
	Cause         error
}

func (e *ImportPublicationError) Error() string {
	state := "checkpoint publication failed; inspect before retrying"
	if e.Published {
		state = "checkpoint was published but finalization failed; do not blindly retry"
	}
	return fmt.Sprintf("%s: %s (retained directory: %s): %v", state, e.CheckpointDir, e.RetainedDir, e.Cause)
}

func (e *ImportPublicationError) Unwrap() error { return e.Cause }

// checkpointImportPublisher keeps fault injection local to a test invocation.
// Production always uses the real file writer, directory sync and native rename.
type checkpointImportPublisher struct {
	writeFile     func(string, []byte) error
	syncDirectory func(*os.File) error
	rename        func(*os.File, string, string, bool) error
}

func newCheckpointImportPublisher() checkpointImportPublisher {
	return checkpointImportPublisher{
		writeFile:     writeCheckpointImportFile,
		syncDirectory: syncCheckpointImportDirectory,
		rename:        renameCheckpointImport,
	}
}

// publishCheckpointImport never writes through the public checkpoint path.
// A complete private sibling directory becomes visible in a single rename. On
// supported systems an overwrite exchanges directories atomically, keeping the
// old version until the parent directory has been synced. There is deliberately
// no delete-and-rename or file-by-file overwrite fallback.
func publishCheckpointImport(cpDir string, files map[string][]byte, allowOverwrite bool) (bool, error) {
	return newCheckpointImportPublisher().publish(cpDir, files, allowOverwrite)
}

func (p checkpointImportPublisher) publish(cpDir string, files map[string][]byte, allowOverwrite bool) (published bool, err error) {
	if cpDir == "" {
		return false, errors.New("checkpoint import destination is required")
	}
	cpDir, err = filepath.Abs(cpDir)
	if err != nil {
		return false, err
	}
	if err := validateCheckpointID(filepath.Base(cpDir)); err != nil {
		return false, err
	}
	for _, required := range []string{MetadataFile, SessionFile} {
		if _, ok := files[required]; !ok {
			return false, fmt.Errorf("checkpoint import missing %s", required)
		}
	}
	// Validate every path before even creating the session directory. Sorted
	// writes also make prefix collisions fail reproducibly in private staging.
	names := make([]string, 0, len(files))
	for name := range files {
		if name == "MANIFEST.json" {
			continue
		}
		if err := validateImportEntryName(name); err != nil {
			return false, err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	parentPath := filepath.Dir(cpDir)
	if err := validateExistingDirectoryPath(parentPath, "session"); err != nil {
		return false, err
	}
	if err := os.MkdirAll(parentPath, 0700); err != nil {
		return false, fmt.Errorf("create checkpoint import parent: %w", err)
	}
	parentPath, err = filepath.EvalSymlinks(parentPath)
	if err != nil {
		return false, err
	}
	cpDir = filepath.Join(parentPath, filepath.Base(cpDir))
	parent, err := openCheckpointImportDirectory(parentPath)
	if err != nil {
		return false, err
	}
	retained := ""
	publicationAttempted := false
	defer func() {
		err = errors.Join(err, parent.Close()) // Closing also releases the import lock.
		if err != nil && publicationAttempted {
			err = &ImportPublicationError{CheckpointDir: cpDir, RetainedDir: retained, Published: published, Cause: err}
		}
	}()
	original, err := inspectCheckpointImportDestination(cpDir, files, allowOverwrite)
	if err != nil {
		return false, err
	}
	stage, err := os.MkdirTemp(parentPath, ".ntm-import-*")
	if err != nil {
		return false, fmt.Errorf("create checkpoint import staging directory: %w", err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		return false, fmt.Errorf("inspect checkpoint import staging directory: %w", err)
	}
	keepStage := false
	cleanupInfo := stageInfo
	defer func() {
		if keepStage {
			return
		}
		info, statErr := os.Lstat(stage)
		if os.IsNotExist(statErr) {
			retained = ""
			return
		}
		if statErr != nil || !os.SameFile(cleanupInfo, info) || !info.IsDir() {
			retained = stage
			err = errors.Join(err, fmt.Errorf("checkpoint staging identity changed; retained %s: %w", stage, errors.Join(statErr, ErrCheckpointImportBusy)))
			return
		}
		if removeErr := os.RemoveAll(stage); removeErr != nil {
			retained = stage
			err = errors.Join(err, fmt.Errorf("clean checkpoint import staging directory: %w", removeErr))
		} else {
			retained = ""
		}
	}()
	directories := map[string]bool{stage: true}
	for _, name := range names {
		dest := filepath.Join(stage, filepath.FromSlash(name))
		dir := filepath.Dir(dest)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return false, fmt.Errorf("stage checkpoint directory for %s: %w", name, err)
		}
		for at := dir; at != stage; at = filepath.Dir(at) {
			directories[at] = true
		}
		if err := p.writeFile(dest, files[name]); err != nil {
			return false, fmt.Errorf("stage checkpoint file %s: %w", name, err)
		}
	}
	// Sync children before parents so every staged directory entry is complete
	// before its enclosing directory can become the public checkpoint.
	dirs := make([]string, 0, len(directories))
	for dir := range directories {
		dirs = append(dirs, dir)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		f, err := os.Open(dir)
		if err != nil {
			return false, err
		}
		syncErr := p.syncDirectory(f)
		if err := errors.Join(syncErr, f.Close()); err != nil {
			return false, fmt.Errorf("sync checkpoint import staging directory: %w", err)
		}
	}
	// Keep path-based staging tied to the directory descriptor used by native
	// publication, and refuse a destination replaced while staging was built.
	openedParent, err := parent.Stat()
	if err != nil {
		return false, err
	}
	currentParent, err := os.Lstat(parentPath)
	if err != nil || !os.SameFile(openedParent, currentParent) || !currentParent.IsDir() {
		keepStage, retained = true, stage
		return false, fmt.Errorf("checkpoint import parent changed during staging: %w", errors.Join(err, ErrCheckpointImportBusy))
	}
	current, err := inspectCheckpointImportDestination(cpDir, files, allowOverwrite)
	if err != nil {
		return false, err
	}
	if (original == nil) != (current == nil) || (original != nil && !os.SameFile(original, current)) {
		return false, fmt.Errorf("checkpoint import destination changed during staging: %w", ErrCheckpointImportBusy)
	}
	currentStage, err := os.Lstat(stage)
	if err != nil || !os.SameFile(stageInfo, currentStage) || !currentStage.IsDir() {
		keepStage, retained = true, stage
		return false, fmt.Errorf("checkpoint import staging directory changed: %w", errors.Join(err, ErrCheckpointImportBusy))
	}
	publicationAttempted = true
	if err := p.rename(parent, filepath.Base(stage), filepath.Base(cpDir), original != nil); err != nil {
		// An interrupted remote-filesystem RPC can report failure after the
		// rename happened. Do not delete either possible generation on error.
		keepStage, retained = true, stage
		info, statErr := os.Lstat(cpDir)
		published = statErr == nil && os.SameFile(stageInfo, info)
		return published, fmt.Errorf("publish checkpoint directory: %w", err)
	}
	published = true
	if original != nil {
		retained = stage // After exchange, stage is the complete previous version.
		cleanupInfo = original
	}
	if err := p.syncDirectory(parent); err != nil {
		keepStage = true
		return true, fmt.Errorf("sync published checkpoint directory: %w", err)
	}
	return true, nil
}

func inspectCheckpointImportDestination(cpDir string, files map[string][]byte, allowOverwrite bool) (os.FileInfo, error) {
	info, err := os.Lstat(cpDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("checkpoint destination must be a directory, not a symlink or special file: %s", cpDir)
	}
	if !allowOverwrite {
		return nil, fmt.Errorf("checkpoint %s already exists (use AllowOverwrite to replace): %w", filepath.Base(cpDir), os.ErrExist)
	}
	if err := validateImportOverwrite(cpDir, files); err != nil {
		return nil, err
	}
	// Replacing a whole directory must not weaken the old importer’s refusal
	// to follow pre-existing symlinks or accept special artifact files.
	err = filepath.WalkDir(cpDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("checkpoint contains symlink or non-regular artifact: %s", path)
		}
		return nil
	})
	return info, err
}

func writeCheckpointImportFile(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return f.Sync()
}
