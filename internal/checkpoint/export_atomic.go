package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// writeCheckpointExport publishes only a complete archive. The staging file
// lives beside the destination so publication is a same-filesystem rename;
// writers must finish their archive trailers before returning. Exported
// scrollback may contain credentials, so both staging and published files are
// private, including when replacing a previously world-readable archive.
func writeCheckpointExport(destPath, cpDir string, writeArchive func(io.Writer) error) (err error) {
	if writeArchive == nil {
		return errors.New("checkpoint export writer is required")
	}
	destPath, err = checkpointExportDestination(destPath, cpDir)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(destPath), ".ntm-export-*")
	if err != nil {
		return fmt.Errorf("creating export staging file: %w", err)
	}
	stagedPath := f.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := f.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("closing export staging file: %w", closeErr))
			}
		}
		if stagedPath != "" {
			if removeErr := os.Remove(stagedPath); removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, fmt.Errorf("removing incomplete export: %w", removeErr))
			}
		}
	}()

	if err := writeArchive(f); err != nil {
		return fmt.Errorf("writing checkpoint export: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing checkpoint export: %w", err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing checkpoint export: %w", err)
	}
	// Recheck after potentially slow archive construction. Never knowingly
	// replace a symlink or a checkpoint artifact introduced during the write.
	if _, err := checkpointExportDestination(destPath, cpDir); err != nil {
		return err
	}
	if err := os.Rename(stagedPath, destPath); err != nil {
		return fmt.Errorf("publishing checkpoint export: %w", err)
	}
	stagedPath = ""
	return nil
}

func checkpointExportDestination(destPath, cpDir string) (string, error) {
	if destPath == "" || cpDir == "" {
		return "", errors.New("checkpoint export requires destination and source paths")
	}
	destPath, err := filepath.Abs(destPath)
	if err != nil {
		return "", fmt.Errorf("resolving export destination: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destPath))
	if err != nil {
		return "", fmt.Errorf("resolving export destination directory: %w", err)
	}
	destPath = filepath.Join(parent, filepath.Base(destPath))
	cpDir, err = filepath.Abs(cpDir)
	if err != nil {
		return "", fmt.Errorf("resolving source checkpoint directory: %w", err)
	}
	cpDir, err = filepath.EvalSymlinks(cpDir)
	if err != nil {
		return "", fmt.Errorf("resolving source checkpoint directory: %w", err)
	}
	rel, err := filepath.Rel(cpDir, destPath)
	if err != nil {
		return "", fmt.Errorf("checking export destination: %w", err)
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return "", fmt.Errorf("export destination must be outside the source checkpoint directory: %s", destPath)
	}
	info, err := os.Lstat(destPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspecting export destination: %w", err)
	}
	if err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("export destination must be a regular file, not a symlink or special file: %s", destPath)
	}
	return destPath, nil
}
