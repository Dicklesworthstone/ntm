package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrArtifactIntegrity means recorded checkpoint payloads failed verification.
// Force restoration and archive overwrite options never bypass this check.
var ErrArtifactIntegrity = errors.New("checkpoint artifact integrity failed")

func artifactIntegrityError(result *IntegrityResult) error {
	if len(result.Errors) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrArtifactIntegrity, strings.Join(result.Errors, "; "))
}

func preflightCheckpointArtifacts(ctx context.Context, storage *Storage, cp *Checkpoint, sourceSession string, allowDegradedContext bool) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Legacy in-memory checkpoints need no storage lookup. Their lack of
	// checksums is surfaced as a warning rather than invented assurance.
	dir := ""
	if cp.ArtifactIntegrity != nil {
		if storage == nil {
			return nil, fmt.Errorf("%w: checkpoint storage is required", ErrArtifactIntegrity)
		}
		if sourceSession == "" {
			sourceSession = cp.SessionName
		}
		var err error
		dir, err = storage.safeCheckpointDir(sourceSession, cp.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrArtifactIntegrity, err)
		}
	}
	result := newIntegrityResult()
	cp.checkArtifactChecksumsContext(ctx, dir, result, allowDegradedContext)
	if err := ctx.Err(); err != nil {
		return result.Warnings, err
	}
	return result.Warnings, artifactIntegrityError(result)
}
