"use client";

/**
 * SessionAdoptContextDiffPanel
 *
 * Surfaces external session adoption across all 12 pane types plus live context
 * window gauges, pane activity/conflict diffing, and file change attribution on /sessions:
 *   - POST /api/v1/sessions/{id}/adopt
 *   - GET  /api/v1/sessions/{id}/context?lines=200
 *   - GET  /api/v1/sessions/{id}/diff?since=15m
 *   - GET  /api/v1/sessions/{id}/files?window=15m&limit=50
 */

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

const ADOPT_HARNESS_OPTIONS = [
  { key: "cc", label: "Claude (cc)" },
  { key: "cod", label: "Codex (cod)" },
  { key: "gmi", label: "Gemini (gmi)" },
  { key: "agy", label: "Antigravity (agy)" },
  { key: "grok", label: "Grok (grok)" },
  { key: "cursor", label: "Cursor" },
  { key: "windsurf", label: "Windsurf" },
  { key: "aider", label: "Aider" },
  { key: "oc", label: "OpenCode (oc)" },
  { key: "omp", label: "Oh My Pi (omp)" },
  { key: "pi", label: "Pi Agent (pi)" },
  { key: "ollama", label: "Ollama" },
  { key: "user", label: "User Shell" },
] as const;

export interface SessionAdoptResponse extends ApiEnvelope {
  session: string;
  total_panes: number;
  dry_run?: boolean;
  adopted_panes: Array<{
    pane_id: string;
    pane_index: number;
    window_index: number;
    agent_type: string;
    old_title?: string;
    new_title: string;
  }>;
  agents: Record<string, number>;
}

export interface SessionContextResponse extends ApiEnvelope {
  session: string;
  agents: Array<{
    pane: string;
    pane_id: string;
    agent_type: string;
    model?: string;
    usage_percent: number;
    estimated_tokens?: number;
    context_limit?: number;
    state?: string;
  }>;
}

export interface SessionDiffResponse extends ApiEnvelope {
  session: string;
  timeframe?: {
    since?: string;
    captured_at?: string;
  };
  files?: {
    modified?: string[];
    potential_conflicts?: Array<{
      file: string;
      likely_modifiers: string[];
      reason: string;
    }>;
  };
  agent_activity?: Array<{
    pane: string;
    agent_type: string;
    state: string;
    output_lines: number;
  }>;
}

export interface SessionFilesResponse extends ApiEnvelope {
  session?: string;
  time_window: string;
  count: number;
  changes: Array<{
    timestamp: string;
    path: string;
    operation: string;
    agents: string[];
  }>;
}

export function SessionAdoptContextDiffPanel({
  sessionId,
}: {
  sessionId: string;
}) {
  const [targetSessionInput, setTargetSessionInput] = useState(sessionId);
  const effectiveSession = (sessionId || targetSessionInput).trim();
  const encodedSession = encodeURIComponent(effectiveSession);

  const [adoptHarness, setAdoptHarness] = useState("cc");
  const [adoptPanesSpec, setAdoptPanesSpec] = useState("0,1");
  const [adoptByWindow, setAdoptByWindow] = useState(false);
  const [adoptAutoName, setAdoptAutoName] = useState(true);
  const [adoptDryRun, setAdoptDryRun] = useState(true);
  const [adoptResult, setAdoptResult] = useState<SessionAdoptResponse | null>(
    null
  );

  const [contextLines, setContextLines] = useState("200");
  const [contextRequested, setContextRequested] = useState(false);
  const [diffSince, setDiffSince] = useState("15m");
  const [diffRequested, setDiffRequested] = useState(false);
  const [filesWindow, setFilesWindow] = useState("15m");
  const [filesRequested, setFilesRequested] = useState(false);

  const adoptMutation = useMutation({
    mutationFn: async () => {
      if (!effectiveSession) {
        throw new Error("Target tmux session name is required");
      }
      const body = adoptByWindow
        ? {
            by_window: true,
            by_window_type: adoptHarness,
            auto_name: adoptAutoName,
            dry_run: adoptDryRun,
          }
        : {
            panes: { [adoptHarness]: adoptPanesSpec.trim() },
            auto_name: adoptAutoName,
            dry_run: adoptDryRun,
          };
      return apiFetch<SessionAdoptResponse>(
        `/api/v1/sessions/${encodeURIComponent(effectiveSession)}/adopt`,
        {
          method: "POST",
          body: JSON.stringify(body),
        }
      );
    },
    onSuccess: (data) => setAdoptResult(data),
  });

  const contextQuery = useQuery({
    queryKey: ["sessions", effectiveSession, "context", contextLines],
    queryFn: () =>
      apiFetch<SessionContextResponse>(
        `/api/v1/sessions/${encodedSession}/context?lines=${encodeURIComponent(
          contextLines.trim() || "200"
        )}`
      ),
    enabled: contextRequested && Boolean(effectiveSession),
    retry: false,
  });

  const diffQuery = useQuery({
    queryKey: ["sessions", effectiveSession, "diff", diffSince],
    queryFn: () =>
      apiFetch<SessionDiffResponse>(
        `/api/v1/sessions/${encodedSession}/diff?since=${encodeURIComponent(
          diffSince.trim() || "15m"
        )}`
      ),
    enabled: diffRequested && Boolean(effectiveSession),
    retry: false,
  });

  const filesQuery = useQuery({
    queryKey: ["sessions", effectiveSession, "files", filesWindow],
    queryFn: () =>
      apiFetch<SessionFilesResponse>(
        `/api/v1/sessions/${encodedSession}/files?window=${encodeURIComponent(
          filesWindow
        )}&limit=50`
      ),
    enabled: filesRequested && Boolean(effectiveSession),
    retry: false,
  });

  return (
    <section
      aria-label="Session Adopt, Context, Diff and File Attribution"
      className="rounded-lg border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800 space-y-5"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Adopt External Session, Context Gauges, Activity Diff &amp; File Attribution
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Adopt external tmux sessions across all 13 pane types and inspect context usage, cross-pane diffs, and file edits.
          </p>
        </div>
        {!sessionId && (
          <input
            aria-label="Target Session Name"
            type="text"
            value={targetSessionInput}
            onChange={(e) => setTargetSessionInput(e.target.value)}
            placeholder="external_session"
            className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs text-gray-900 dark:text-white"
          />
        )}
      </div>

      {/* Adopt External Session */}
      <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-3">
        <div className="text-sm font-medium text-gray-900 dark:text-white">
          12-Type External Session Adoption
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-4 gap-3">
          <label className="text-xs text-gray-600 dark:text-gray-300 space-y-1">
            <span>Harness / Pane Type</span>
            <select
              aria-label="Adopt Harness"
              value={adoptHarness}
              onChange={(e) => setAdoptHarness(e.target.value)}
              className="w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            >
              {ADOPT_HARNESS_OPTIONS.map((opt) => (
                <option key={opt.key} value={opt.key}>
                  {opt.label}
                </option>
              ))}
            </select>
          </label>
          <label className="text-xs text-gray-600 dark:text-gray-300 space-y-1">
            <span>Pane Indices / window.pane</span>
            <input
              aria-label="Adopt Pane Specs"
              type="text"
              disabled={adoptByWindow}
              value={adoptPanesSpec}
              onChange={(e) => setAdoptPanesSpec(e.target.value)}
              placeholder="0,1 or 1.0,2.0"
              className="w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white disabled:opacity-50"
            />
          </label>
          <div className="flex flex-col justify-end gap-1.5 text-xs text-gray-700 dark:text-gray-300">
            <label className="inline-flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={adoptByWindow}
                onChange={(e) => setAdoptByWindow(e.target.checked)}
              />
              Map by window (--by-window)
            </label>
            <label className="inline-flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={adoptAutoName}
                onChange={(e) => setAdoptAutoName(e.target.checked)}
              />
              Auto-name NTM titles
            </label>
          </div>
          <div className="flex flex-col justify-end gap-1.5">
            <label className="inline-flex items-center gap-1.5 text-xs text-gray-700 dark:text-gray-300">
              <input
                type="checkbox"
                checked={adoptDryRun}
                onChange={(e) => setAdoptDryRun(e.target.checked)}
              />
              Dry run preview
            </label>
            <button
              type="button"
              disabled={!effectiveSession || adoptMutation.isPending}
              onClick={() => adoptMutation.mutate()}
              className="rounded bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {adoptDryRun ? "Preview Adopt" : "Adopt Session"}
            </button>
          </div>
        </div>
        {adoptMutation.isError && (
          <div className="text-xs text-red-600 dark:text-red-400">
            {formatApiErrorMessage(adoptMutation.error)}
          </div>
        )}
        {adoptResult && (
          <div className="rounded bg-gray-50 dark:bg-gray-900/50 p-3 text-xs text-gray-800 dark:text-gray-200 space-y-1">
            <div>
              {adoptResult.dry_run ? "[DRY RUN] Would adopt" : "Adopted"}{" "}
              <span className="font-semibold">
                {adoptResult.adopted_panes?.length ?? 0}
              </span>{" "}
              of {adoptResult.total_panes} pane(s) in{" "}
              <span className="font-mono">{adoptResult.session}</span>
            </div>
            {(adoptResult.adopted_panes ?? []).map((p) => (
              <div key={p.pane_id} className="font-mono text-[11px]">
                {p.pane_id} ({p.window_index}.{p.pane_index}) [{p.agent_type}] →{" "}
                {p.new_title}
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Context, Diff & Files Inspector */}
      {effectiveSession && (
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
          {/* Context Window Usage */}
          <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-2">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-semibold text-gray-900 dark:text-white">
                Context Window Gauges
              </span>
              <div className="flex items-center gap-1.5">
                <input
                  aria-label="Context Lines"
                  type="number"
                  min={20}
                  max={10000}
                  value={contextLines}
                  onChange={(e) => setContextLines(e.target.value)}
                  className="w-16 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
                />
                <button
                  type="button"
                  onClick={() => {
                    setContextRequested(true);
                    void contextQuery.refetch();
                  }}
                  className="rounded border border-gray-300 dark:border-gray-600 px-2.5 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
                >
                  Load Context
                </button>
              </div>
            </div>
            {contextQuery.isError && (
              <div className="text-xs text-red-600 dark:text-red-400">
                {formatApiErrorMessage(contextQuery.error)}
              </div>
            )}
            {contextQuery.data && (
              <div className="space-y-1.5 text-xs">
                {(contextQuery.data.agents ?? []).length === 0 ? (
                  <div className="text-gray-500">No agent panes measured.</div>
                ) : (
                  contextQuery.data.agents.map((a) => (
                    <div
                      key={a.pane_id}
                      className="flex items-center justify-between rounded bg-gray-50 dark:bg-gray-900/50 px-2 py-1"
                    >
                      <span className="font-mono">
                        {a.pane} ({a.agent_type})
                      </span>
                      <span>{Math.round(a.usage_percent ?? 0)}%</span>
                    </div>
                  ))
                )}
              </div>
            )}
          </div>

          {/* Activity & Conflict Diff */}
          <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-2">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-semibold text-gray-900 dark:text-white">
                Activity &amp; Conflict Diff
              </span>
              <div className="flex items-center gap-1.5">
                <input
                  aria-label="Diff Since"
                  type="text"
                  value={diffSince}
                  onChange={(e) => setDiffSince(e.target.value)}
                  className="w-16 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
                />
                <button
                  type="button"
                  onClick={() => {
                    setDiffRequested(true);
                    void diffQuery.refetch();
                  }}
                  className="rounded border border-gray-300 dark:border-gray-600 px-2.5 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
                >
                  Load Diff
                </button>
              </div>
            </div>
            {diffQuery.isError && (
              <div className="text-xs text-red-600 dark:text-red-400">
                {formatApiErrorMessage(diffQuery.error)}
              </div>
            )}
            {diffQuery.data && (
              <div className="space-y-1 text-xs text-gray-700 dark:text-gray-300">
                <div>
                  Modified files:{" "}
                  {diffQuery.data.files?.modified?.length ?? 0} · Potential
                  conflicts:{" "}
                  {diffQuery.data.files?.potential_conflicts?.length ?? 0}
                </div>
                {(diffQuery.data.agent_activity ?? []).map((act) => (
                  <div key={act.pane} className="font-mono text-[11px]">
                    {act.pane} [{act.agent_type}] {act.state} (
                    {act.output_lines} lines)
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Tracked File Changes */}
          <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-2">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-semibold text-gray-900 dark:text-white">
                Tracked File Changes
              </span>
              <div className="flex items-center gap-1.5">
                <select
                  aria-label="Files Window"
                  value={filesWindow}
                  onChange={(e) => setFilesWindow(e.target.value)}
                  className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
                >
                  <option value="5m">5m</option>
                  <option value="15m">15m</option>
                  <option value="1h">1h</option>
                  <option value="all">all</option>
                </select>
                <button
                  type="button"
                  onClick={() => {
                    setFilesRequested(true);
                    void filesQuery.refetch();
                  }}
                  className="rounded border border-gray-300 dark:border-gray-600 px-2.5 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
                >
                  Load Files
                </button>
              </div>
            </div>
            {filesQuery.isError && (
              <div className="text-xs text-red-600 dark:text-red-400">
                {formatApiErrorMessage(filesQuery.error)}
              </div>
            )}
            {filesQuery.data && (
              <div className="space-y-1 text-xs text-gray-700 dark:text-gray-300">
                <div>{filesQuery.data.count ?? 0} tracked file change(s)</div>
                {(filesQuery.data.changes ?? []).slice(0, 6).map((ch, idx) => (
                  <div
                    key={`${ch.path}-${idx}`}
                    className="font-mono text-[11px] truncate"
                  >
                    [{ch.operation}] {ch.path} ({(ch.agents ?? []).join(", ")})
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
