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
 */

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
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
  const [restoreTarget, setRestoreTarget] = useState<string | null>(null);
  const [restoreTargetName, setRestoreTargetName] = useState("");
  const [restoreForce, setRestoreForce] = useState(false);
  const [restoreLaunch, setRestoreLaunch] = useState(false);
  const [feedback, setFeedback] = useState<string | null>(null);

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
    mutationFn: (payload: {
      name: string;
      target_name?: string;
      force: boolean;
      launch: boolean;
    }) =>
      typedPost("/api/v1/saved-sessions/{name}/restore", {
        params: { path: { name: payload.name } },
        body: {
          ...(payload.target_name ? { target_name: payload.target_name } : {}),
          force: payload.force,
          launch: payload.launch,
        },
      }),
    onSuccess: (res, vars) => {
      setFeedback(
        `Restored saved session "${vars.name}" into tmux session "${res.session || vars.target_name || vars.name}".`
      );
      setRestoreTarget(null);
      setRestoreTargetName("");
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (err) => {
      setFeedback(`Restore failed: ${formatApiErrorMessage(err)}`);
    },
  });

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
            Saved Sessions &amp; casr Resume Readiness
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Capture live tmux topologies, inspect 11-harness counts &amp; per-pane resume bindings, and manage active vs. archived snapshots.
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
        <div className="rounded-md border border-blue-200 dark:border-blue-800 bg-blue-50 dark:bg-blue-950/30 px-3 py-2 text-xs text-blue-800 dark:text-blue-200">
          {feedback}
        </div>
      )}

      {/* Saved Sessions Table */}
      {listQuery.isLoading ? (
        <p className="text-xs text-gray-500 dark:text-gray-400">
          Loading saved session snapshots…
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
                      onClick={() => {
                        setRestoreTarget(item.name);
                        setRestoreTargetName("");
                      }}
                      className="rounded border border-blue-300 dark:border-blue-700 px-2.5 py-1 text-xs font-medium text-blue-700 dark:text-blue-300 hover:bg-blue-50 dark:hover:bg-blue-950/30"
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

      {/* Restore Confirmation Drawer */}
      {restoreTarget && (
        <div
          data-testid="saved-session-restore-confirm"
          className="rounded-lg border border-blue-200 dark:border-blue-800 bg-blue-50/50 dark:bg-blue-950/30 p-4 space-y-3"
        >
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Confirm Restore: <span className="font-mono">{restoreTarget}</span>
            </h3>
            <button
              type="button"
              onClick={() => setRestoreTarget(null)}
              className="text-xs text-gray-500 hover:text-gray-800 dark:hover:text-gray-200"
            >
              Cancel
            </button>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3 items-center">
            <input
              type="text"
              aria-label="Restore Target Session Name"
              value={restoreTargetName}
              onChange={(e) => setRestoreTargetName(e.target.value)}
              placeholder="Target session name (optional)"
              className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <label className="flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
              <input
                type="checkbox"
                checked={restoreForce}
                onChange={(e) => setRestoreForce(e.target.checked)}
              />
              Force replace existing session
            </label>
            <label className="flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
              <input
                type="checkbox"
                checked={restoreLaunch}
                onChange={(e) => setRestoreLaunch(e.target.checked)}
              />
              Launch / resume agents
            </label>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              disabled={restoreMutation.isPending}
              onClick={() =>
                restoreMutation.mutate({
                  name: restoreTarget,
                  ...(restoreTargetName.trim()
                    ? { target_name: restoreTargetName.trim() }
                    : {}),
                  force: restoreForce,
                  launch: restoreLaunch,
                })
              }
              className="rounded bg-blue-600 hover:bg-blue-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
            >
              {restoreMutation.isPending ? "Restoring…" : "Confirm Restore"}
            </button>
          </div>
        </div>
      )}

      {/* Detailed Snapshot & Resume Binding Inspection */}
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
