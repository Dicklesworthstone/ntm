"use client";

/**
 * SessionRollbackAndArchivePanel
 *
 * Surfaces the three previously unwired session recovery & portable archive
 * endpoints on the /sessions detail cockpit:
 *   POST /api/v1/sessions/{sessionName}/rollback
 *   POST /api/v1/sessions/{sessionName}/checkpoints/{checkpointId}/export
 *   POST /api/v1/sessions/{sessionName}/checkpoints/import
 */

import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
  type components,
} from "@/lib/api/client";

type CheckpointSummary = components["schemas"]["CheckpointResponse"];

export interface SessionRollbackResponse extends ApiEnvelope {
  checkpoint_id: string;
  checkpoint_name: string;
  git_restored: boolean;
  dry_run: boolean;
  stash_created?: boolean;
  stash_ref?: string;
  warnings?: string[];
}

export interface CheckpointArchiveExportResponse extends ApiEnvelope {
  filename: string;
  size: number;
  content_type: string;
  data: string;
}

export interface CheckpointArchiveImportResponse extends ApiEnvelope {
  imported: boolean;
  checkpoint: CheckpointSummary;
}

function createRecoveryIdempotencyKey(prefix: string): string {
  const randomPart =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  return `${prefix}-${randomPart}`;
}

function formatArchiveBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let idx = 0;
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024;
    idx += 1;
  }
  return `${value.toFixed(idx === 0 ? 0 : 1)} ${units[idx]}`;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunkSize = 0x8000;
  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunkSize));
  }
  return btoa(binary);
}

export interface SessionRollbackAndArchivePanelProps {
  sessionId: string;
  encodedSessionId: string;
  checkpoints: CheckpointSummary[];
  onNotice?: (notice: { type: "success" | "error"; message: string }) => void;
}

export function SessionRollbackAndArchivePanel({
  sessionId,
  encodedSessionId,
  checkpoints,
  onNotice,
}: SessionRollbackAndArchivePanelProps) {
  const queryClient = useQueryClient();

  // 1. Git-Aware Rollback state
  const [rollbackRef, setRollbackRef] = useState("");
  const [noStash, setNoStash] = useState(false);
  const [noGit, setNoGit] = useState(false);
  const [confirmExecuteRollback, setConfirmExecuteRollback] = useState(false);
  const [rollbackResult, setRollbackResult] =
    useState<SessionRollbackResponse | null>(null);
  const [rollbackError, setRollbackError] = useState<string | null>(null);

  // 2. Checkpoint Archive Export state
  const [exportCheckpointId, setExportCheckpointId] = useState("");
  const [exportFormat, setExportFormat] = useState<"tar.gz" | "zip">("tar.gz");
  const [redactSecrets, setRedactSecrets] = useState(true);
  const [rewritePaths, setRewritePaths] = useState(true);
  const [exportedArchive, setExportedArchive] =
    useState<CheckpointArchiveExportResponse | null>(null);
  const [exportError, setExportError] = useState<string | null>(null);

  // 3. Checkpoint Archive Import state
  const [importBase64, setImportBase64] = useState("");
  const [importFilename, setImportFilename] = useState<string | null>(null);
  const [importTargetDir, setImportTargetDir] = useState("");
  const [verifyChecksums, setVerifyChecksums] = useState(true);
  const [allowOverwrite, setAllowOverwrite] = useState(false);
  const [importedCheckpoint, setImportedCheckpoint] =
    useState<CheckpointSummary | null>(null);
  const [importError, setImportError] = useState<string | null>(null);

  useEffect(() => {
    if (checkpoints.length === 0) {
      if (exportCheckpointId !== "") {
        setExportCheckpointId("");
      }
      return;
    }
    if (!checkpoints.some((cp) => cp.id === exportCheckpointId)) {
      setExportCheckpointId(checkpoints[0].id);
    }
  }, [checkpoints, exportCheckpointId]);

  const rollbackMutation = useMutation({
    mutationFn: (payload: {
      checkpoint_ref?: string;
      no_stash: boolean;
      no_git: boolean;
      dry_run: boolean;
    }) =>
      apiFetch<SessionRollbackResponse>(
        `/api/v1/sessions/${encodedSessionId}/rollback`,
        {
          method: "POST",
          headers: payload.dry_run
            ? undefined
            : {
                "Idempotency-Key": createRecoveryIdempotencyKey(
                  "session-rollback"
                ),
              },
          body: JSON.stringify(payload),
        }
      ),
    onSuccess: (res) => {
      setRollbackError(null);
      setRollbackResult(res);
      setConfirmExecuteRollback(false);
      if (!res.dry_run) {
        onNotice?.({
          type: "success",
          message: `Rolled back session to checkpoint "${res.checkpoint_name || res.checkpoint_id}".`,
        });
        void queryClient.invalidateQueries({
          queryKey: ["sessions", sessionId],
        });
        void queryClient.invalidateQueries({
          queryKey: ["session-checkpoints", sessionId],
        });
      }
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err);
      setRollbackError(msg);
      setConfirmExecuteRollback(false);
      onNotice?.({
        type: "error",
        message: `Rollback failed: ${msg}`,
      });
    },
  });

  const exportMutation = useMutation({
    mutationFn: (payload: {
      checkpointId: string;
      format: "tar.gz" | "zip";
      redact_secrets: boolean;
      rewrite_paths: boolean;
    }) =>
      apiFetch<CheckpointArchiveExportResponse>(
        `/api/v1/sessions/${encodedSessionId}/checkpoints/${encodeURIComponent(
          payload.checkpointId
        )}/export`,
        {
          method: "POST",
          body: JSON.stringify({
            format: payload.format,
            redact_secrets: payload.redact_secrets,
            rewrite_paths: payload.rewrite_paths,
          }),
        }
      ),
    onSuccess: (res) => {
      setExportError(null);
      setExportedArchive(res);
      onNotice?.({
        type: "success",
        message: `Exported checkpoint archive "${res.filename}" (${formatArchiveBytes(
          res.size
        )}).`,
      });
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err);
      setExportError(msg);
      onNotice?.({
        type: "error",
        message: `Checkpoint export failed: ${msg}`,
      });
    },
  });

  const importMutation = useMutation({
    mutationFn: (payload: {
      data: string;
      target_dir?: string;
      verify_checksums: boolean;
      allow_overwrite: boolean;
    }) =>
      apiFetch<CheckpointArchiveImportResponse>(
        `/api/v1/sessions/${encodedSessionId}/checkpoints/import`,
        {
          method: "POST",
          headers: {
            "Idempotency-Key": createRecoveryIdempotencyKey("cp-import"),
          },
          body: JSON.stringify(payload),
        }
      ),
    onSuccess: (res) => {
      setImportError(null);
      setImportedCheckpoint(res.checkpoint);
      setImportBase64("");
      setImportFilename(null);
      onNotice?.({
        type: "success",
        message: `Imported checkpoint "${res.checkpoint.name || res.checkpoint.id}".`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["session-checkpoints", sessionId],
      });
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err);
      setImportError(msg);
      onNotice?.({
        type: "error",
        message: `Checkpoint import failed: ${msg}`,
      });
    },
  });

  const triggerRollback = (dryRun: boolean) => {
    const trimmedRef = rollbackRef.trim();
    rollbackMutation.mutate({
      ...(trimmedRef ? { checkpoint_ref: trimmedRef } : {}),
      no_stash: noStash,
      no_git: noGit,
      dry_run: dryRun,
    });
  };

  const handleExportSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!exportCheckpointId) {
      setExportError("Select a saved checkpoint to export.");
      return;
    }
    exportMutation.mutate({
      checkpointId: exportCheckpointId,
      format: exportFormat,
      redact_secrets: redactSecrets,
      rewrite_paths: rewritePaths,
    });
  };

  const handleImportFileChange = async (
    event: React.ChangeEvent<HTMLInputElement>
  ) => {
    const file = event.target.files?.[0];
    if (!file) return;
    try {
      const buf = await file.arrayBuffer();
      setImportBase64(bytesToBase64(new Uint8Array(buf)));
      setImportFilename(file.name);
      setImportError(null);
    } catch (err) {
      setImportError(
        err instanceof Error ? err.message : "Failed to read archive file"
      );
    }
  };

  const handleImportSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    const cleanedData = importBase64.replace(/\s+/g, "");
    if (!cleanedData) {
      setImportError(
        "Choose a .tar.gz/.zip archive file or paste base64 archive data."
      );
      return;
    }
    importMutation.mutate({
      data: cleanedData,
      ...(importTargetDir.trim() ? { target_dir: importTargetDir.trim() } : {}),
      verify_checksums: verifyChecksums,
      allow_overwrite: allowOverwrite,
    });
  };

  return (
    <section
      aria-label="Git Rollback & Checkpoint Archives"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-5"
    >
      <div>
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
          Git Rollback & Portable Checkpoint Archives
        </h2>
        <p className="text-sm text-gray-500 dark:text-gray-400">
          Preview or execute Git-aware rollback to a saved checkpoint, or export
          and import redacted portable checkpoint bundles.
        </p>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Left column: Git-Aware Session Rollback */}
        <div
          role="region"
          aria-label="Git-Aware Session Rollback"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Git-Aware Session Rollback
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Stashes dirty working-tree changes by default before checking out
              and applying the checkpoint&apos;s Git state.
            </p>
          </div>

          <div>
            <label
              htmlFor="rollback-checkpoint-ref"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Checkpoint Ref / ID (blank = latest ~1)
            </label>
            <input
              id="rollback-checkpoint-ref"
              value={rollbackRef}
              onChange={(e) => setRollbackRef(e.target.value)}
              placeholder="~1, checkpoint ID, or name"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm font-mono text-gray-900 dark:text-white"
            />
            {checkpoints.length > 0 && (
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {checkpoints.slice(0, 4).map((cp) => (
                  <button
                    key={cp.id}
                    type="button"
                    onClick={() => setRollbackRef(cp.id)}
                    className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-2 py-0.5 text-[11px] font-mono text-gray-700 dark:text-gray-200 hover:border-blue-400"
                  >
                    Use {cp.id}
                  </button>
                ))}
              </div>
            )}
          </div>

          <div className="flex flex-wrap gap-4 text-xs text-gray-700 dark:text-gray-300">
            <label
              htmlFor="rollback-no-stash"
              className="flex items-center gap-1.5 cursor-pointer"
            >
              <input
                id="rollback-no-stash"
                type="checkbox"
                checked={noStash}
                onChange={(e) => setNoStash(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              Skip Git stash (no_stash)
            </label>
            <label
              htmlFor="rollback-no-git"
              className="flex items-center gap-1.5 cursor-pointer"
            >
              <input
                id="rollback-no-git"
                type="checkbox"
                checked={noGit}
                onChange={(e) => setNoGit(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              Skip Git recovery (no_git)
            </label>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={rollbackMutation.isPending}
              onClick={() => triggerRollback(true)}
              className="rounded-md border border-blue-300 dark:border-blue-700 bg-blue-50 dark:bg-blue-900/20 px-3 py-1.5 text-xs font-medium text-blue-800 dark:text-blue-200 hover:bg-blue-100 dark:hover:bg-blue-900/40 disabled:opacity-50"
            >
              {rollbackMutation.isPending && rollbackMutation.variables?.dry_run
                ? "Previewing…"
                : "Preview Rollback (Dry Run)"}
            </button>

            {!confirmExecuteRollback ? (
              <button
                type="button"
                disabled={rollbackMutation.isPending}
                onClick={() => setConfirmExecuteRollback(true)}
                className="rounded-md bg-amber-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-amber-700 disabled:opacity-50"
              >
                Execute Rollback
              </button>
            ) : (
              <div className="flex items-center gap-2 rounded-md border border-amber-300 dark:border-amber-700 bg-amber-50 dark:bg-amber-900/20 px-2.5 py-1">
                <button
                  type="button"
                  disabled={rollbackMutation.isPending}
                  onClick={() => triggerRollback(false)}
                  className="rounded bg-amber-600 px-2.5 py-0.5 text-xs font-medium text-white hover:bg-amber-700 disabled:opacity-50"
                >
                  {rollbackMutation.isPending &&
                  !rollbackMutation.variables?.dry_run
                    ? "Rolling back…"
                    : "Confirm Git Rollback"}
                </button>
                <button
                  type="button"
                  onClick={() => setConfirmExecuteRollback(false)}
                  className="text-xs text-gray-600 dark:text-gray-300 hover:underline"
                >
                  Cancel
                </button>
              </div>
            )}
          </div>

          {rollbackError && (
            <div className="rounded border border-red-200 bg-red-50 p-2.5 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
              {rollbackError}
            </div>
          )}

          {rollbackResult && (
            <div
              data-testid="session-rollback-result"
              className={`rounded border p-3 text-xs space-y-1.5 ${
                rollbackResult.dry_run
                  ? "border-blue-200 bg-blue-50 text-blue-900 dark:border-blue-800 dark:bg-blue-900/20 dark:text-blue-200"
                  : "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
              }`}
            >
              <div className="font-semibold">
                {rollbackResult.dry_run
                  ? `Dry-Run Preview: ${rollbackResult.checkpoint_name || rollbackResult.checkpoint_id}`
                  : `Rollback Complete: ${rollbackResult.checkpoint_name || rollbackResult.checkpoint_id}`}
              </div>
              <div className="font-mono text-[11px]">
                checkpoint_id={rollbackResult.checkpoint_id} · git_restored=
                {String(rollbackResult.git_restored)}
                {rollbackResult.stash_created && rollbackResult.stash_ref
                  ? ` · stash=${rollbackResult.stash_ref}`
                  : ""}
              </div>
              {(rollbackResult.warnings ?? []).length > 0 && (
                <ul className="list-disc pl-4 space-y-0.5">
                  {rollbackResult.warnings?.map((warn, i) => (
                    <li key={`${i}-${warn}`}>{warn}</li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>

        {/* Right column: Export + Import Portable Checkpoint Archives */}
        <div
          role="region"
          aria-label="Portable Checkpoint Archives"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-4"
        >
          {/* Export form */}
          <form onSubmit={handleExportSubmit} className="space-y-3">
            <div>
              <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                Export Checkpoint Archive
              </h3>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Bundle scrollback and Git patches as a disclosure-scanned{" "}
                <code className="font-mono">.tar.gz</code> or{" "}
                <code className="font-mono">.zip</code> archive.
              </p>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-2.5">
              <div className="sm:col-span-2">
                <label
                  htmlFor="export-checkpoint-id"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Checkpoint to Export
                </label>
                <select
                  id="export-checkpoint-id"
                  value={exportCheckpointId}
                  onChange={(e) => setExportCheckpointId(e.target.value)}
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
                >
                  {checkpoints.length === 0 ? (
                    <option value="">No saved checkpoints</option>
                  ) : (
                    checkpoints.map((cp) => (
                      <option key={cp.id} value={cp.id}>
                        {cp.name ? `${cp.name} (${cp.id})` : cp.id}
                      </option>
                    ))
                  )}
                </select>
              </div>
              <div>
                <label
                  htmlFor="export-checkpoint-format"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Archive Format
                </label>
                <select
                  id="export-checkpoint-format"
                  value={exportFormat}
                  onChange={(e) =>
                    setExportFormat(e.target.value as "tar.gz" | "zip")
                  }
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
                >
                  <option value="tar.gz">tar.gz</option>
                  <option value="zip">zip</option>
                </select>
              </div>
            </div>

            <div className="flex flex-wrap gap-4 text-xs text-gray-700 dark:text-gray-300">
              <label
                htmlFor="export-redact-secrets"
                className="flex items-center gap-1.5 cursor-pointer"
              >
                <input
                  id="export-redact-secrets"
                  type="checkbox"
                  checked={redactSecrets}
                  onChange={(e) => setRedactSecrets(e.target.checked)}
                  className="h-3.5 w-3.5"
                />
                Redact scrollback secrets
              </label>
              <label
                htmlFor="export-rewrite-paths"
                className="flex items-center gap-1.5 cursor-pointer"
              >
                <input
                  id="export-rewrite-paths"
                  type="checkbox"
                  checked={rewritePaths}
                  onChange={(e) => setRewritePaths(e.target.checked)}
                  className="h-3.5 w-3.5"
                />
                Rewrite absolute paths
              </label>
            </div>

            <button
              type="submit"
              disabled={exportMutation.isPending || checkpoints.length === 0}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {exportMutation.isPending ? "Exporting…" : "Export Archive"}
            </button>

            {exportError && (
              <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
                {exportError}
              </div>
            )}

            {exportedArchive && (
              <div
                data-testid="checkpoint-export-ready"
                className="rounded border border-emerald-200 bg-emerald-50 dark:border-emerald-800 dark:bg-emerald-900/20 p-2.5 text-xs space-y-1.5"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-mono font-semibold text-emerald-900 dark:text-emerald-200">
                    {exportedArchive.filename}
                  </span>
                  <span className="text-emerald-700 dark:text-emerald-300">
                    {formatArchiveBytes(exportedArchive.size)} ·{" "}
                    {exportedArchive.content_type}
                  </span>
                </div>
                <div className="flex items-center gap-3">
                  <a
                    href={`data:${exportedArchive.content_type};base64,${exportedArchive.data}`}
                    download={exportedArchive.filename}
                    className="font-medium text-blue-700 dark:text-blue-300 underline"
                  >
                    Download {exportedArchive.filename}
                  </a>
                </div>
              </div>
            )}
          </form>

          {/* Import form */}
          <form
            onSubmit={handleImportSubmit}
            className="pt-3 border-t border-gray-200 dark:border-gray-700 space-y-2.5"
          >
            <div>
              <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                Import Checkpoint Archive
              </h3>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Upload an archive file or paste base64 bundle data to register a
                checkpoint on this session.
              </p>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
              <div>
                <label
                  htmlFor="import-archive-file"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Archive File (.tar.gz / .zip)
                </label>
                <input
                  id="import-archive-file"
                  type="file"
                  accept=".tar.gz,.tgz,.zip,application/gzip,application/zip"
                  onChange={(e) => void handleImportFileChange(e)}
                  className="w-full text-xs text-gray-700 dark:text-gray-300"
                />
                {importFilename && (
                  <div className="mt-1 text-[11px] font-mono text-emerald-700 dark:text-emerald-300">
                    Loaded: {importFilename}
                  </div>
                )}
              </div>
              <div>
                <label
                  htmlFor="import-target-dir"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Target Working Dir (optional)
                </label>
                <input
                  id="import-target-dir"
                  value={importTargetDir}
                  onChange={(e) => setImportTargetDir(e.target.value)}
                  placeholder="Project-relative or within workspace"
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
                />
              </div>
            </div>

            <div>
              <label
                htmlFor="import-archive-base64"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Base64 Archive Data
              </label>
              <textarea
                id="import-archive-base64"
                rows={2}
                value={importBase64}
                onChange={(e) => setImportBase64(e.target.value)}
                placeholder="Paste base64-encoded .tar.gz or .zip archive bytes..."
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
              />
            </div>

            <div className="flex flex-wrap gap-4 text-xs text-gray-700 dark:text-gray-300">
              <label
                htmlFor="import-verify-checksums"
                className="flex items-center gap-1.5 cursor-pointer"
              >
                <input
                  id="import-verify-checksums"
                  type="checkbox"
                  checked={verifyChecksums}
                  onChange={(e) => setVerifyChecksums(e.target.checked)}
                  className="h-3.5 w-3.5"
                />
                Verify SHA256 checksums
              </label>
              <label
                htmlFor="import-allow-overwrite"
                className="flex items-center gap-1.5 cursor-pointer"
              >
                <input
                  id="import-allow-overwrite"
                  type="checkbox"
                  checked={allowOverwrite}
                  onChange={(e) => setAllowOverwrite(e.target.checked)}
                  className="h-3.5 w-3.5"
                />
                Allow overwrite existing ID
              </label>
            </div>

            <button
              type="submit"
              disabled={importMutation.isPending}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
            >
              {importMutation.isPending
                ? "Importing…"
                : "Import Checkpoint Archive"}
            </button>

            {importError && (
              <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
                {importError}
              </div>
            )}

            {importedCheckpoint && (
              <div
                data-testid="checkpoint-import-confirmation"
                className="rounded border border-emerald-200 bg-emerald-50 dark:border-emerald-800 dark:bg-emerald-900/20 p-2.5 text-xs text-emerald-900 dark:text-emerald-200"
              >
                Imported checkpoint{" "}
                <span className="font-mono font-semibold">
                  {importedCheckpoint.name || importedCheckpoint.id}
                </span>{" "}
                ({importedCheckpoint.pane_count} pane
                {importedCheckpoint.pane_count === 1 ? "" : "s"}).
              </div>
            )}
          </form>
        </div>
      </div>
    </section>
  );
}
