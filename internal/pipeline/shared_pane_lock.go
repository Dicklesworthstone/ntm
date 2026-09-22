package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// sharedPaneLockPath is independent of the project being run. A tmux pane is
// owned by the server, not by the working directory of a pipeline: two projects
// can target the very same pane. Project-local locks alone cannot exclude them.
//
// Use a persistent, per-user rendezvous, never a process-specific temporary
// directory. Pane IDs from different tmux servers deliberately share a lock:
// until the transport exposes a canonical server identity, conservative
// serialization is safer than guessing an identity and allowing overlapping
// dispatches. Lock files must never be removed while other processes may use
// them, since unlinking can give waiters and new owners different inodes.
func sharedPaneLockPath(paneID string) (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve pane lock state directory: %w", err)
		}
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("resolve pane lock state directory: home directory must be absolute")
		}
		root = filepath.Join(home, ".local", "state")
	}
	sum := sha256.Sum256([]byte(paneID))
	return filepath.Join(root, "ntm", "pane-locks", hex.EncodeToString(sum[:])+".lock"), nil
}

// acquireSharedPaneLock takes the user-wide pane lock followed by the optional
// legacy project lock. Keeping the latter protects against older NTM processes
// running in the same project during an upgrade. Every caller acquires locks in
// this order and releases them in reverse order. A single caller-supplied
// context bounds the entire acquisition, not each lock separately.
func acquireSharedPaneLock(ctx context.Context, paneID, legacyPath string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := sharedPaneLockPath(paneID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("prepare shared pane lock directory: %w", err)
	}

	shared, err := openLockedFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("acquire shared pane lock: %w", err)
	}
	var legacy *lockedFile
	var once sync.Once
	release := func() {
		once.Do(func() {
			if legacy != nil {
				legacy.unlockAndClose()
			}
			shared.unlockAndClose()
		})
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if legacyPath != "" {
		legacy, err = openLockedFile(ctx, legacyPath)
		if err != nil {
			release()
			return nil, fmt.Errorf("acquire project pane lock: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
