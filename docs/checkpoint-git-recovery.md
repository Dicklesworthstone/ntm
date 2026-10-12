# Recoverable Git checkpoints

`ntm checkpoint save SESSION` records a binary-capable patch of tracked
working-tree changes against the captured commit. Git's display preferences,
external diff drivers, text conversions, colors, and relative-path settings do
not replace that patch with a human-only preview. Capturing from a pane in a
subdirectory still saves the repository-wide tracked delta.

## Preserve newly created files explicitly

```sh
ntm checkpoint save myproject --include-untracked -m "Before refactor"
```

The corresponding create-checkpoint REST request accepts
`{"include_untracked":true}`. `--no-git` and `--include-untracked` are incompatible;
the REST equivalent is `capture_git:false` with `include_untracked:true`.

Untracked capture is off by default. It includes non-ignored regular files and
symlinks, including empty and executable files. Symlinks are saved as link text,
not by reading their targets. Git's ignore rules continue to exclude files such
as ignored local credentials. **Non-ignored sensitive files are included when
this option is requested.** Review the file selection before making portable
archives; this flag is not a secret scanner.

The metadata and save responses expose `untracked_count` and
`untracked_captured`. Counts describe files, not collapsed directory entries.
A nested repository, special file, changing selection, unreadable path,
timeout, or size failure makes explicitly requested capture fail visibly. The
session checkpoint may still be retained, with `capture_failed` Git metadata;
CLI errors name its ID and REST error details include its identity. Do not treat
that partial checkpoint as a successful code backup.

The patch uses the existing `git.patch` artifact. It participates in existing
artifact hashing and archive transfer. Excluding the Git patch from an export
also clears `untracked_captured`, while preserving the original omitted count.

## Rollback validates first

Both `ntm rollback` and the REST rollback handler use a shared preflight before
interrupting agents (CLI), stashing work, or checking out the saved commit:

- Resolve the saved commit and the actual repository root.
- Verify available checkpoint artifact fingerprints, load a bounded patch, and
  verify the exact loaded bytes again.
- Check patch applicability to the saved commit using a temporary Git index,
  without modifying the working tree, real staging index, or references.

A dry run performs this read-only preflight too. Checkpoints without recorded
artifact fingerprints remain usable with an explicit integrity-unavailable
warning; applicability checks do not establish authenticity.

The REST handler rechecks its project-directory boundary against the actual
repository root. Permission to a subdirectory does not authorize mutations to
its parent repository.

Normal rollback stashes current tracked and untracked work unless the operator
has explicitly disabled stashing. Checkout refuses to overwrite ignored files.
The already validated patch bytes are then applied at the repository root.
Patch failures are errors, not successful rollbacks with a warning. Failure
responses retain available stash information; stashes are not automatically
popped on failure. Inspect a partial recovery before retrying it.

## Scope and limits

A baseline commit is required. This captures the combined final working-tree
state, not the original staged/unstaged division. It is not a backup of Git's
object database, LFS objects, ignored files, or dirty submodule contents.
Repositories nested inside the untracked selection require separate capture.

Capture leaves the real staging index, references, and files unchanged. It
bounds patch output to the existing 10 MiB Git-output limit and untracked
selection output to 4 MiB. Preflight and patch operations honor their contexts.
The surrounding legacy interrupt/stash/checkout operations are not a single
transaction or an entirely cancellable workflow. A hook, agent, or concurrent
writer can still cause a failure after checkout; progress must be inspected.

Filesystem changes are not globally frozen during capture. Path metadata and
selection changes are detected, but this is not an atomic filesystem snapshot.
