package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
)

const artifactIntegrityVersion = 1

// ArtifactIntegrity records a complete set of fingerprints for the payloads
// referenced by a checkpoint. A nil manifest denotes a legacy checkpoint.
// Metadata and session JSON are checked separately by the schema/consistency
// verifier; including metadata here would require a self-referential digest.
type ArtifactIntegrity struct {
	Version int                         `json:"version"`
	Files   map[string]ArtifactChecksum `json:"files"`
}

func checkpointPayloadFiles(cp *Checkpoint) []string {
	files := expectedManifestFiles(cp)
	delete(files, MetadataFile)
	delete(files, SessionFile)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// recordArtifactChecksums seals new artifacts but preserves existing evidence.
// In particular, renaming or re-saving a checkpoint must not silently replace
// the expected checksum of a damaged artifact with the checksum of the damage.
func (cp *Checkpoint) recordArtifactChecksums(dir string) error {
	if cp.ArtifactIntegrity != nil && cp.ArtifactIntegrity.Version != artifactIntegrityVersion {
		return fmt.Errorf("unsupported artifact integrity version: %d", cp.ArtifactIntegrity.Version)
	}
	manifest := &ArtifactIntegrity{
		Version: artifactIntegrityVersion,
		Files:   make(map[string]ArtifactChecksum),
	}
	for _, rel := range checkpointPayloadFiles(cp) {
		path, err := resolveExistingCheckpointArtifactPath(dir, rel)
		if err != nil {
			return err
		}
		if cp.ArtifactIntegrity != nil {
			if expected, ok := cp.ArtifactIntegrity.Files[rel]; ok {
				if err := verifyArtifactFile(path, expected); err != nil {
					return fmt.Errorf("artifact %q: %w", rel, err)
				}
				manifest.Files[rel] = expected
				continue
			}
		}
		checksum, err := checksumArtifactFile(path)
		if err != nil {
			return fmt.Errorf("checksumming artifact %q: %w", rel, err)
		}
		manifest.Files[rel] = checksum
	}
	// Do not mutate the checkpoint if any artifact failed validation.
	cp.ArtifactIntegrity = manifest
	return nil
}

func checkpointPaneScrollbackFiles(cp *Checkpoint) map[string]bool {
	files := make(map[string]bool, len(cp.Session.Panes))
	for _, pane := range cp.Session.Panes {
		if pane.ScrollbackFile != "" {
			files[pane.ScrollbackFile] = true
		}
	}
	return files
}

func (cp *Checkpoint) checkArtifactChecksums(dir string, result *IntegrityResult) {
	cp.checkArtifactChecksumsContext(context.Background(), dir, result, false)
}

func (cp *Checkpoint) checkArtifactChecksumsContext(ctx context.Context, dir string, result *IntegrityResult, allowDegradedContext bool) {
	var degradedPaneCheck func(string, bool) (string, bool, error)
	if allowDegradedContext {
		degradedPaneCheck = func(rel string, hasChecksum bool) (string, bool, error) {
			if err := ctx.Err(); err != nil {
				return "", false, err
			}
			_, err := resolveExistingCheckpointArtifactPath(dir, rel)
			if err == nil {
				// Existing regular files must still have a recorded checksum and pass verification.
				return "", false, nil
			}
			if errors.Is(err, os.ErrNotExist) {
				if !hasChecksum {
					return fmt.Sprintf("degraded context: missing pane scrollback artifact %q (unrecorded in integrity manifest)", rel), true, nil
				}
				return fmt.Sprintf("degraded context: missing pane scrollback artifact %q", rel), true, nil
			}
			return "", false, err
		}
	}
	cp.checkArtifactChecksumsUsingWithDegraded(result, func(rel string, expected ArtifactChecksum) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := resolveExistingCheckpointArtifactPath(dir, rel)
		if err != nil {
			return err
		}
		return verifyArtifactFileContext(ctx, path, expected)
	}, degradedPaneCheck)
}

// Keep manifest membership rules identical for on-disk checkpoints and
// archive entries, which must be validated before publication.
func (cp *Checkpoint) checkArtifactChecksumsUsing(result *IntegrityResult, verify func(string, ArtifactChecksum) error) {
	cp.checkArtifactChecksumsUsingWithDegraded(result, verify, nil)
}

func (cp *Checkpoint) checkArtifactChecksumsUsingWithDegraded(
	result *IntegrityResult,
	verify func(string, ArtifactChecksum) error,
	degradedPaneCheck func(rel string, hasChecksum bool) (string, bool, error),
) {
	paths := checkpointPayloadFiles(cp)
	result.Details["checksums_checked"] = "0"
	if err := validateCheckpointArtifactReferences(cp); err != nil {
		result.ConsistencyValid = false
		result.Details["checksum_status"] = "failed"
		result.Errors = append(result.Errors, err.Error())
		return
	}
	if cp.ArtifactIntegrity == nil {
		result.Details["checksum_status"] = "unavailable"
		if len(paths) > 0 {
			result.Warnings = append(result.Warnings, "checkpoint has no artifact checksums; payload corruption cannot be detected (legacy checkpoint)")
		}
		return
	}

	result.Details["checksum_status"] = "verified"
	fail := func(message string) {
		result.ConsistencyValid = false
		result.Details["checksum_status"] = "failed"
		result.Errors = append(result.Errors, message)
	}
	if cp.ArtifactIntegrity.Version != artifactIntegrityVersion {
		fail(fmt.Sprintf("unsupported artifact integrity version: %d", cp.ArtifactIntegrity.Version))
		return
	}
	if cp.ArtifactIntegrity.Files == nil {
		fail("artifact integrity manifest has no files map")
		return
	}

	// Reject unexpected entries without opening their paths. Every manifest
	// entry must refer to an artifact actually used by this checkpoint.
	wanted := make(map[string]bool, len(paths))
	for _, path := range paths {
		wanted[path] = true
	}
	var extra []string
	for path := range cp.ArtifactIntegrity.Files {
		if !wanted[path] {
			extra = append(extra, path)
		}
	}
	sort.Strings(extra)
	for _, path := range extra {
		fail(fmt.Sprintf("checksum references unreferenced artifact %q", path))
	}

	paneScrollbacks := checkpointPaneScrollbackFiles(cp)
	checked := 0
	for _, rel := range paths {
		expected, ok := cp.ArtifactIntegrity.Files[rel]
		if degradedPaneCheck != nil && paneScrollbacks[rel] {
			warning, skippedMissing, err := degradedPaneCheck(rel, ok)
			if err != nil {
				fail(fmt.Sprintf("artifact %q: %v", rel, err))
				continue
			}
			if skippedMissing {
				result.Warnings = append(result.Warnings, warning)
				if result.Details["checksum_status"] == "verified" {
					result.Details["checksum_status"] = "degraded"
				}
				continue
			}
		}
		if !ok {
			fail(fmt.Sprintf("missing checksum for artifact %q", rel))
			continue
		}
		if err := verify(rel, expected); err != nil {
			fail(fmt.Sprintf("artifact %q: %v", rel, err))
			continue
		}
		checked++
	}
	result.Details["checksums_checked"] = strconv.Itoa(checked)
}
