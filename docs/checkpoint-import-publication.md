# Complete checkpoint import publication

`ntm checkpoint import` now stages both `.tar.gz` and `.zip` imports before
publishing them. Archive validation, gzip trailer checks, input-stream closes,
metadata overrides, and all artifact writes finish before the destination
checkpoint becomes visible. A failed staging write leaves an existing checkpoint
unchanged and does not install a partial new checkpoint.

Staging directories are private siblings named `.ntm-import-*`; files are created
with mode `0600` and directories with mode `0700`. The existing checkpoint-ID
rules exclude these hidden directories from normal checkpoint selection.
Ordinary pre-publication failures clean up their private staging directory.
A process crash can leave staging behind, but it does not make that directory
an importable or automatically selected checkpoint.

## Replacing an existing checkpoint

`--overwrite` uses a native atomic directory exchange on Linux and macOS. The
old generation remains in staging until the containing directory has been
synced. An already-open old artifact is not modified by replacement. The
existing refusal to overwrite a checkpoint with stale extra artifacts is
retained, and symlinks or non-regular artifacts are refused.

Imports on Linux and macOS take a nonblocking lock on the session directory.
Competing imports return `ErrCheckpointImportBusy`; closing the descriptor or
process exit releases the lock without a stale lock file. Create-only imports
use native no-replace publication, so competing creators cannot replace a
checkpoint that another import just installed.

Linux/filesystem combinations lacking the required native rename operation fail
closed. Other operating systems support staged create-only imports but reject
overwrites with `ErrCheckpointImportAtomicUnavailable`. There is no destructive
remove-and-rename or file-by-file overwrite fallback. Directory syncing is
skipped on Windows; artifact files are still synced before publication.

## Publication errors require inspection

`Storage.Import` can return a non-nil checkpoint together with an
`ImportPublicationError` when the complete new directory was observed at its
target but finalization failed. The error retains `CheckpointDir`, `Published`,
`RetainedDir`, and the original cause. The existing CLI preserves this error and
its inspection guidance rather than reporting an ordinary success.

A rename error can be ambiguous, particularly on remote filesystems. The
importer retains staging and checks whether the new directory identity is at
the destination; it never deletes either possible generation on that error.
`Published: false` is not proof that no remote effect occurred. A retained path
may contain the new staging generation, the previous generation after exchange,
or cleanup remnants. Inspect the destination and reported path before retrying;
no interrupted import is replayed automatically.

## Scope

Publication is one directory-namespace operation, not a multi-file reader
transaction. A reader that opens different paths across the swap can observe
different generations. The import lock coordinates import writers only, not
capture, compaction, direct filesystem edits, or other storage mutations.
Process-exit and real Linux filesystem tests cover the publication boundary;
power-loss durability, remote filesystem behavior, and macOS runtime behavior
require separate validation. Newly created ancestor directories are not covered
by a recursive ancestor-sync guarantee.
