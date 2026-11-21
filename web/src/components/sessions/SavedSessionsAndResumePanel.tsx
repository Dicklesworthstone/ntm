"use client";

/**
 * SavedSessionsAndResumePanel
 *
 * Surfaces saved session snapshots, 11-harness topology inspection, casr/native
 * per-pane resume binding readiness, and non-destructive archive/unarchive lifecycle:
 *   - GET  /api/v1/saved-sessions?archived=false|true
 *   - POST /api/v1/saved-sessions
 *   - GET  /api/v1/saved-sessions/{name}
 *   - POST /api/v1/saved-sessions/{name}/archive
 *   - POST /api/v1/saved-sessions/{name}/unarchive
 *   - POST /api/v1/saved-sessions/{name}/restore
 */

import { useEffect, useId, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  apiRequestSignal,
  formatApiErrorMessage,
  typedGet,
  typedPost,
  type components,
} from "@/lib/api/client";

export type SavedSessionsEnvelope =
  components["schemas"]["SavedSessionsEnvelopeResponse"];
export type SavedSessionSummaryItem = NonNullable<
  SavedSessionsEnvelope["sessions"]
>[number];
export type SavedSessionStatePayload = NonNullable<
  SavedSessionsEnvelope["state"]
>;
export type SavedSessionPaneItem = NonNullable<
  SavedSessionStatePayload["panes"]
>[number];

type RestoreMode = "topology" | "launch" | "resume";
type SavedSessionRestoreOutcome = Pick<
  components["schemas"]["SavedSessionRestoreEnvelopeResponse"],
  "session" | "resume"
>;

interface RestoreAttempt {
  savedName: string;
  key: string;
  payload: {
    target_name?: string;
    force: boolean;
    mode: RestoreMode;
    native: boolean;
  };
}

function RestoreOutcome({ outcome }: { outcome: SavedSessionRestoreOutcome | null }) {
  if (!outcome?.resume) return null;
  return (
    <div className="rounded-md border border-gray-200 dark:border-gray-700 p-3 text-xs space-y-2">
      <p className="font-medium text-gray-900 dark:text-white">
        Session {outcome.session}: {outcome.resume.resumed} resumed,
        {" "}{outcome.resume.launched} started, {outcome.resume.skipped} skipped,
        {" "}{outcome.resume.failed} failed.
      </p>
      {(outcome.resume.panes ?? []).filter((pane) => pane.error).map((pane) => (
        <p key={`${pane.window_index}:${pane.index}`} className="text-red-700 dark:text-red-300">
          Pane {pane.window_index}:{pane.index} ({pane.agent_type}): {pane.error}
        </p>
      ))}
    </div>
  );
}

const HARNESS_LABELS: Array<{
  key: keyof SavedSessionStatePayload["agents"];
  label: string;
}> = [
  { key: "cc", label: "Claude (cc)" },
  { key: "cod", label: "Codex (cod)" },
  { key: "gmi", label: "Gemini (gmi)" },
  { key: "agy", label: "Antigravity (agy)" },
  { key: "grok", label: "Grok" },
  { key: "cursor", label: "Cursor" },
  { key: "windsurf", label: "Windsurf" },
  { key: "aider", label: "Aider" },
  { key: "oc", label: "OpenCode (oc)" },
  { key: "omp", label: "Oh My Pi (omp)" },
  { key: "pi", label: "Pi (pi)" },
  { key: "ollama", label: "Ollama" },
  { key: "user", label: "User" },
];

export function SavedSessionsAndResumePanel({
  sessionId,
}: {
  sessionId: string;
}) {
  const queryClient = useQueryClient();
  const [archived, setArchived] = useState(false);
  const [selectedName, setSelectedName] = useState("");
  const [captureSession, setCaptureSession] = useState(sessionId);
  const [captureName, setCaptureName] = useState("");
  const [overwrite, setOverwrite] = useState(true);
  const [feedback, setFeedback] = useState<string | null>(null);
  const [restoreDraft, setRestoreDraft] = useState<{
    savedName: string;
    targetName: string;
    mode: RestoreMode;
    force: boolean;
    native: boolean;
  } | null>(null);
  const [restoreError, setRestoreError] = useState<string | null>(null);
  const [restoreResult, setRestoreResult] = useState<SavedSessionRestoreOutcome | null>(null);
  const restoreAttemptRef = useRef<RestoreAttempt | null>(null);
  const restoreDialogRef = useRef<HTMLDialogElement | null>(null);
  const restoreTitleId = useId();

  useEffect(() => {
    const dialog = restoreDialogRef.current;
    if (!dialog) return;
    if (restoreDraft && !dialog.open) dialog.showModal();
    if (!restoreDraft && dialog.open) dialog.close();
  }, [restoreDraft]);

  useEffect(() => {
    if (sessionId) {
      setCaptureSession(sessionId);
    }
  }, [sessionId]);

  const listQuery = useQuery({
    queryKey: ["saved-sessions", archived ? "archived" : "active"],
    queryFn: () =>
      typedGet("/api/v1/saved-sessions", {
        params: { query: { archived } },
      }),
  });

  const detailQuery = useQuery({
    queryKey: ["saved-session-detail", selectedName],
    queryFn: () =>
      typedGet("/api/v1/saved-sessions/{name}", {
        params: { path: { name: selectedName } },
      }),
    enabled: Boolean(selectedName) && !archived,
  });

  const saveMutation = useMutation({
    mutationFn: (payload: { session: string; name?: string; overwrite: boolean }) =>
      typedPost("/api/v1/saved-sessions", { body: payload }),
    onSuccess: (res) => {
      setFeedback(`Captured snapshot "${res.saved_as || captureSession}".`);
      void queryClient.invalidateQueries({ queryKey: ["saved-sessions"] });
      if (res.saved_as && !archived) {
        setSelectedName(res.saved_as);
      }
    },
    onError: (err) => {
      setFeedback(`Save failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const archiveMutation = useMutation({
    mutationFn: (name: string) =>
      typedPost("/api/v1/saved-sessions/{name}/archive", {
        params: { path: { name } },
      }),
    onSuccess: (_, name) => {
      setFeedback(`Archived snapshot "${name}".`);
      if (selectedName === name) {
        setSelectedName("");
      }
      void queryClient.invalidateQueries({ queryKey: ["saved-sessions"] });
    },
    onError: (err) => {
      setFeedback(`Archive failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const unarchiveMutation = useMutation({
    mutationFn: (name: string) =>
      typedPost("/api/v1/saved-sessions/{name}/unarchive", {
        params: { path: { name } },
      }),
    onSuccess: (_, name) => {
      setFeedback(`Restored snapshot "${name}" to active saved sessions.`);
      void queryClient.invalidateQueries({ queryKey: ["saved-sessions"] });
    },
    onError: (err) => {
      setFeedback(`Unarchive failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const restoreMutation = useMutation({
    mutationFn: (attempt: RestoreAttempt) =>
      typedPost("/api/v1/saved-sessions/{name}/restore", {
        params: { path: { name: attempt.savedName } },
        headers: { "Idempotency-Key": attempt.key },
        body: attempt.payload,
        // The recovery handler allows two minutes for a multi-agent launch.
        signal: apiRequestSignal(135_000),
      }),
    retry: false,
    onSuccess: (res, attempt) => {
      setRestoreResult(res);
      setRestoreError(null);
      setRestoreDraft(null);
      const action = attempt.payload.mode === "topology"
        ? "Pane layout restored; agents have not been started."
        : attempt.payload.mode === "launch"
          ? "Fresh agent launch completed."
          : "Agent resume completed.";
      setFeedback(`Restored session "${res.session}". ${action}`);
    },
    onError: (err) => {
      setRestoreError(formatApiErrorMessage(err));
      if (err instanceof ApiError && err.details && typeof err.details === "object") {
        const partial = (err.details as { details?: SavedSessionRestoreOutcome }).details;
        if (partial?.resume) setRestoreResult(partial);
      }
    },
    onSettled: (res, _, attempt) => {
      // A failed launch or lost response can still leave restored panes behind.
      const targetName = res?.session || attempt.payload.target_name;
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
      void queryClient.invalidateQueries({
        queryKey: targetName ? ["session-panes", targetName] : ["session-panes"],
      });
      void queryClient.invalidateQueries({
        queryKey: ["saved-session-detail", attempt.savedName],
      });
    },
  });

  function openRestore(savedName: string, targetName = "") {
    restoreAttemptRef.current = null;
    setFeedback(null);
    setRestoreError(null);
    setRestoreResult(null);
    setRestoreDraft({ savedName, targetName, mode: "topology", force: false, native: false });
  }

  function confirmRestore() {
    if (!restoreDraft || restoreMutation.isPending) return;
    const targetName = restoreDraft.targetName.trim();
    const payload: RestoreAttempt["payload"] = {
      ...(targetName ? { target_name: targetName } : {}),
      force: restoreDraft.force,
      mode: restoreDraft.mode,
      native: restoreDraft.mode === "resume" && restoreDraft.native,
    };
    const previous = restoreAttemptRef.current;
    const attempt = previous && previous.savedName === restoreDraft.savedName &&
      JSON.stringify(previous.payload) === JSON.stringify(payload)
      ? previous
      : {
          savedName: restoreDraft.savedName,
          payload,
          key: `saved-restore-${typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
            ? crypto.randomUUID()
            : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`}`,
        };
    // Preserve the operation key if the operator retries after a lost response.
    restoreAttemptRef.current = attempt;
    setRestoreError(null);
    setRestoreResult(null);
    restoreMutation.mutate(attempt);
  }

  const sessions = listQuery.data?.sessions ?? [];
  const detailState = detailQuery.data?.state;

  return (
    <section
      data-testid="saved-sessions-resume-panel"
      className="rounded-xl border border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-900 p-5 space-y-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-gray-900 dark:text-white">
            Saved Sessions &amp; Resume
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Capture sessions, inspect saved agents, and restore layouts or resume work.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => {
              setArchived(false);
              setSelectedName("");
            }}
            className={`rounded-md px-3 py-1.5 text-xs font-medium ${
              !archived
                ? "bg-blue-600 text-white"
                : "border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-300"
            }`}
          >
            Active Snapshots
          </button>
          <button
            type="button"
            onClick={() => {
              setArchived(true);
              setSelectedName("");
            }}
            className={`rounded-md px-3 py-1.5 text-xs font-medium ${
              archived
                ? "bg-blue-600 text-white"
                : "border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-300"
            }`}
          >
            Archived Snapshots
          </button>
        </div>
      </div>

      {/* Capture Live Session Form */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-3 items-end rounded-lg border border-gray-100 dark:border-gray-800 bg-gray-50/60 dark:bg-gray-950/40 p-3">
        <div>
          <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
            Live Session Name
          </label>
          <input
            type="text"
            aria-label="Live Session Name"
            value={captureSession}
            onChange={(e) => setCaptureSession(e.target.value)}
            placeholder="myproject"
            className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
            Save Snapshot As (optional)
          </label>
          <input
            type="text"
            aria-label="Save Snapshot As"
            value={captureName}
            onChange={(e) => setCaptureName(e.target.value)}
            placeholder="pre-migration-snap"
            className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
          />
        </div>
        <label className="flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300 py-1.5">
          <input
            type="checkbox"
            checked={overwrite}
            onChange={(e) => setOverwrite(e.target.checked)}
            className="rounded border-gray-300 dark:border-gray-600"
          />
          Overwrite existing snapshot
        </label>
        <button
          type="button"
          disabled={!captureSession.trim() || saveMutation.isPending}
          onClick={() =>
            saveMutation.mutate({
              session: captureSession.trim(),
              ...(captureName.trim() ? { name: captureName.trim() } : {}),
              overwrite,
            })
          }
          className="rounded-md bg-emerald-600 hover:bg-emerald-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
        >
          {saveMutation.isPending ? "Saving…" : "Capture Snapshot"}
        </button>
      </div>

      {feedback && (
        <div role="status" className="rounded-md border border-blue-200 dark:border-blue-800 bg-blue-50 dark:bg-blue-950/30 px-3 py-2 text-xs text-blue-800 dark:text-blue-200">
          {feedback}
        </div>
      )}

      {!restoreDraft && <RestoreOutcome outcome={restoreResult} />}

      {/* Saved Sessions Table */}
      {listQuery.isLoading ? (
        <p className="text-xs text-gray-500 dark:text-gray-400">
          Loading saved session snapshots…
        </p>
      ) : listQuery.isError ? (
        <p role="alert" className="text-xs text-red-700 dark:text-red-300">
          Could not load saved sessions: {formatApiErrorMessage(listQuery.error)}
        </p>
      ) : sessions.length === 0 ? (
        <p className="text-xs text-gray-500 dark:text-gray-400">
          No {archived ? "archived" : "active"} saved session snapshots found.
        </p>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          {sessions.map((item) => (
            <div
              key={item.name}
              className="rounded-lg border border-gray-200 dark:border-gray-700 p-3 flex items-center justify-between gap-3"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-xs font-semibold text-gray-900 dark:text-white truncate">
                    {item.name}
                  </span>
                  <span className="rounded bg-gray-100 dark:bg-gray-800 px-1.5 py-0.5 text-[11px] text-gray-700 dark:text-gray-300">
                    {item.agents} panes
                  </span>
                  {item.git_branch && (
                    <span className="rounded bg-purple-100 dark:bg-purple-900/30 px-1.5 py-0.5 text-[11px] font-mono text-purple-700 dark:text-purple-300">
                      {item.git_branch}
                    </span>
                  )}
                </div>
                <div className="text-[11px] text-gray-500 dark:text-gray-400 truncate mt-0.5">
                  {item.cwd}
                </div>
              </div>

              <div className="flex items-center gap-1.5 shrink-0">
                {!archived ? (
                  <>
                    <button
                      type="button"
                      onClick={() => setSelectedName(item.name)}
                      className="rounded border border-gray-200 dark:border-gray-700 px-2.5 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-800"
                    >
                      Inspect
                    </button>
                    <button
                      type="button"
                      disabled={restoreMutation.isPending}
                      onClick={() => openRestore(
                        item.name, selectedName === item.name ? detailState?.name : undefined
                      )}
                      className="rounded border border-blue-300 dark:border-blue-700 disabled:opacity-50 px-2.5 py-1 text-xs font-medium text-blue-700 dark:text-blue-300 hover:bg-blue-50 dark:hover:bg-blue-950/30"
                    >
                      Restore
                    </button>
                    <button
                      type="button"
                      onClick={() => archiveMutation.mutate(item.name)}
                      disabled={archiveMutation.isPending}
                      className="rounded border border-amber-300 dark:border-amber-700 px-2.5 py-1 text-xs font-medium text-amber-700 dark:text-amber-300 hover:bg-amber-50 dark:hover:bg-amber-950/30"
                    >
                      Archive
                    </button>
                  </>
                ) : (
                  <button
                    type="button"
                    onClick={() => unarchiveMutation.mutate(item.name)}
                    disabled={unarchiveMutation.isPending}
                    className="rounded border border-emerald-300 dark:border-emerald-700 px-2.5 py-1 text-xs font-medium text-emerald-700 dark:text-emerald-300 hover:bg-emerald-50 dark:hover:bg-emerald-950/30"
                  >
                    Unarchive
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      <dialog
        ref={restoreDialogRef}
        data-testid="saved-session-restore-confirm"
        aria-labelledby={restoreTitleId}
        onCancel={(event) => {
          event.preventDefault();
          if (!restoreMutation.isPending) setRestoreDraft(null);
        }}
        className="m-auto max-h-[85vh] w-[min(92vw,32rem)] overflow-y-auto rounded-xl border border-gray-200 bg-white p-5 text-gray-900 shadow-2xl backdrop:bg-gray-950/60 dark:border-gray-700 dark:bg-gray-900 dark:text-white"
      >
        {restoreDraft && (
          <form onSubmit={(event) => { event.preventDefault(); confirmRestore(); }} className="space-y-4">
            <h3 id={restoreTitleId} className="text-base font-semibold">Restore saved session</h3>
            <p className="text-sm text-gray-600 dark:text-gray-300">
              Restore snapshot <strong>{restoreDraft.savedName}</strong> into a tmux session.
            </p>
            <fieldset disabled={restoreMutation.isPending} className="space-y-4 disabled:opacity-60">
              <label className="block text-sm font-medium">
                Target session name (optional)
                <input
                  aria-label="Target session name"
                  value={restoreDraft.targetName}
                  onChange={(event) => setRestoreDraft({ ...restoreDraft, targetName: event.target.value })}
                  placeholder="Saved session name"
                  className="mt-1 w-full rounded-md border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-950 px-3 py-2 font-mono"
                />
              </label>
              <p className="text-xs text-gray-600 dark:text-gray-400">
                Leave blank to use the session name recorded in the snapshot.
              </p>
              <label className="block text-sm font-medium">
                Restore mode
                <select
                  value={restoreDraft.mode}
                  onChange={(event) => setRestoreDraft({ ...restoreDraft, mode: event.target.value as RestoreMode, native: false })}
                  className="mt-1 w-full rounded-md border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-950 px-3 py-2"
                >
                  <option value="topology">Layout only</option>
                  <option value="launch">Start fresh agents</option>
                  <option value="resume">Resume saved agents</option>
                </select>
              </label>
              <p className="text-xs text-gray-600 dark:text-gray-400">
                {restoreDraft.mode === "topology"
                  ? "Restore panes and layout without starting agent processes."
                  : restoreDraft.mode === "launch"
                    ? "Start new agent conversations using the saved configuration."
                    : "Continue saved conversations where bindings are available; other agent panes start fresh."}
              </p>
              {restoreDraft.mode === "resume" && (
                <label className="flex items-start gap-2 text-sm">
                  <input type="checkbox" checked={restoreDraft.native}
                    onChange={(event) => setRestoreDraft({ ...restoreDraft, native: event.target.checked })} />
                  Use native resume commands (bypass casr)
                </label>
              )}
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" checked={restoreDraft.force}
                  onChange={(event) => setRestoreDraft({ ...restoreDraft, force: event.target.checked })} />
                Replace an existing target session
              </label>
              {restoreDraft.force && (
                <p className="rounded-md bg-amber-50 dark:bg-amber-950/30 p-3 text-sm text-amber-900 dark:text-amber-200">
                  This will stop {restoreDraft.targetName.trim()
                    ? `the running session "${restoreDraft.targetName.trim()}"`
                    : "the running session with the name recorded in this snapshot"}
                  {" "}and its processes before restoring. Unsaved work in those processes may be lost.
                </p>
              )}
            </fieldset>
            {restoreError && (
              <p role="alert" className="text-sm text-red-700 dark:text-red-300">
                Restore failed: {restoreError} Check the session before changing options;
                retrying unchanged options reuses the same request.
              </p>
            )}
            <RestoreOutcome outcome={restoreResult} />
            <div className="flex justify-end gap-3">
              <button type="button" disabled={restoreMutation.isPending}
                onClick={() => setRestoreDraft(null)}
                className="rounded-md border border-gray-300 dark:border-gray-700 px-3 py-2 text-sm disabled:opacity-50">
                Cancel
              </button>
              <button type="submit" disabled={restoreMutation.isPending}
                className="rounded-md bg-blue-600 hover:bg-blue-700 px-3 py-2 text-sm font-medium text-white disabled:opacity-50">
                {restoreMutation.isPending ? "Restoring…" : restoreError ? "Retry Restore" : "Confirm Restore"}
              </button>
            </div>
          </form>
        )}
      </dialog>

      {/* Detailed Snapshot & Resume Binding Inspection */}
      {selectedName && detailQuery.isError && (
        <p role="alert" className="text-xs text-red-700 dark:text-red-300">
          Could not inspect snapshot: {formatApiErrorMessage(detailQuery.error)}
        </p>
      )}
      {selectedName && detailState && (
        <div
          data-testid="saved-session-detail-card"
          className="rounded-lg border border-indigo-200 dark:border-indigo-800 bg-indigo-50/40 dark:bg-indigo-950/20 p-4 space-y-3"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h3 className="text-sm font-semibold text-gray-900 dark:text-white font-mono">
                Snapshot: {selectedName}
              </h3>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Layout: {detailState.layout} · Resumable panes:{" "}
                <strong className="text-emerald-700 dark:text-emerald-300">
                  {detailQuery.data?.resumable_panes ?? 0}
                </strong>{" "}
                / {detailState.panes?.length ?? 0}
              </p>
            </div>
            <button
              type="button"
              disabled={restoreMutation.isPending}
              onClick={() => openRestore(selectedName, detailState.name)}
              className="rounded-md bg-blue-600 hover:bg-blue-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
            >
              Restore Session
            </button>
          </div>

          <div className="flex flex-wrap gap-1.5">
            {HARNESS_LABELS.filter(
              (h) => (detailState.agents?.[h.key] ?? 0) > 0
            ).map((h) => (
              <span
                key={h.key}
                className="rounded-full bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 px-2.5 py-0.5 text-xs text-gray-800 dark:text-gray-200"
              >
                {h.label}: {detailState.agents[h.key]}
              </span>
            ))}
          </div>

          {detailState.panes && detailState.panes.length > 0 && (
            <div className="space-y-1">
              {detailState.panes.map((pane) => (
                <div
                  key={`${pane.index}-${pane.title}`}
                  className="flex flex-wrap items-center justify-between gap-2 rounded bg-white/80 dark:bg-gray-900/70 px-3 py-1.5 text-xs"
                >
                  <span className="font-mono text-gray-900 dark:text-white">
                    [{pane.index}] {pane.title} ({pane.agent_type})
                  </span>
                  <span className="text-gray-600 dark:text-gray-300">
                    {pane.session_id
                      ? `${pane.session_provider || "provider"}:${pane.session_id} (${pane.session_freshness || "unknown"})`
                      : "no resume binding"}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
