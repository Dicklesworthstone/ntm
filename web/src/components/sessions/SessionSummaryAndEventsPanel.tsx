"use client";

/**
 * SessionSummaryAndEventsPanel
 *
 * Surfaces session-scoped and stored activity summaries, latest Goal/Now handoff
 * snapshot, and durable session events on /sessions:
 *   - GET /api/v1/sessions/{id}/summary?since=30m
 *   - GET /api/v1/summaries
 *   - GET /api/v1/handoffs/latest?session=...
 *   - GET /api/v1/sessions/{id}/events
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface SessionSummaryAgentRow {
  pane_id?: string;
  pane_title?: string;
  agent_type?: string;
  state?: string;
  lines_captured?: number;
  files_modified?: string[];
  key_actions?: string[];
  error_count?: number;
}

export interface SessionSummaryResponse extends ApiEnvelope {
  session?: string;
  summary?: {
    session?: string;
    total_agents?: number;
    active_agents?: number;
    total_files?: number;
    total_output_lines?: number;
    total_errors?: number;
    conflicts?: string[];
    agents?: SessionSummaryAgentRow[];
    text?: string;
  };
  agents?: SessionSummaryAgentRow[];
  text?: string;
}

export interface StoredSummariesResponse extends ApiEnvelope {
  summaries: Array<{
    session: string;
    timestamp: string;
    path: string;
  }>;
  count: number;
}

export interface LatestHandoffRecord {
  session?: string;
  goal?: string;
  now?: string;
  status?: string;
  updated_at?: string;
}

export interface LatestHandoffResponse extends ApiEnvelope {
  session: string;
  found?: boolean;
  path?: string;
  latest_path?: string;
  latest?: LatestHandoffRecord;
  handoff?: LatestHandoffRecord;
  quality?: {
    score?: number;
    status?: string;
    grade?: string;
  };
}

export interface SessionEventsResponse extends ApiEnvelope {
  session?: string;
  session_id?: string;
  events?: Array<{
    id?: string | number;
    type?: string;
    event_type?: string;
    timestamp?: string;
    summary?: string;
    message?: string;
  }>;
  count?: number;
}

export function SessionSummaryAndEventsPanel({
  sessionId,
}: {
  sessionId: string;
}) {
  const encodedSession = encodeURIComponent(sessionId);
  const [sinceWindow, setSinceWindow] = useState("30m");
  const [summaryRequested, setSummaryRequested] = useState(false);
  const [storedRequested, setStoredRequested] = useState(false);
  const [latestHandoffRequested, setLatestHandoffRequested] = useState(false);
  const [eventsRequested, setEventsRequested] = useState(false);

  const summaryQuery = useQuery({
    queryKey: ["sessions", sessionId, "summary", sinceWindow],
    queryFn: () =>
      apiFetch<SessionSummaryResponse>(
        `/api/v1/sessions/${encodedSession}/summary?since=${encodeURIComponent(
          sinceWindow.trim() || "30m"
        )}`
      ),
    enabled: summaryRequested && Boolean(sessionId),
    retry: false,
  });

  const storedQuery = useQuery({
    queryKey: ["sessions", "stored-summaries"],
    queryFn: () => apiFetch<StoredSummariesResponse>("/api/v1/summaries"),
    enabled: storedRequested,
    retry: false,
  });

  const latestHandoffQuery = useQuery({
    queryKey: ["sessions", sessionId, "latest-handoff"],
    queryFn: () =>
      apiFetch<LatestHandoffResponse>(
        `/api/v1/handoffs/latest?session=${encodedSession}`
      ),
    enabled: latestHandoffRequested && Boolean(sessionId),
    retry: false,
  });

  const eventsQuery = useQuery({
    queryKey: ["sessions", sessionId, "events"],
    queryFn: () =>
      apiFetch<SessionEventsResponse>(
        `/api/v1/sessions/${encodedSession}/events`
      ),
    enabled: eventsRequested && Boolean(sessionId),
    retry: false,
  });

  const summaryAgents =
    summaryQuery.data?.summary?.agents ?? summaryQuery.data?.agents ?? [];
  const storedList = storedQuery.data?.summaries ?? [];
  const eventRows = eventsQuery.data?.events ?? [];

  return (
    <section
      aria-label="Session Activity Summary & Event Log"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Activity Summary, Latest Handoff &amp; Session Events
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Generate per-agent accomplishment summaries, inspect archived
            summaries, check the newest Goal/Now handoff, and view session
            events.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <input
            aria-label="Summary lookback window"
            value={sinceWindow}
            onChange={(e) => setSinceWindow(e.target.value)}
            placeholder="30m"
            className="w-20 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
          />
          <button
            type="button"
            onClick={() =>
              summaryRequested
                ? void summaryQuery.refetch()
                : setSummaryRequested(true)
            }
            className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700"
          >
            Generate Activity Summary
          </button>
          <button
            type="button"
            onClick={() =>
              storedRequested
                ? void storedQuery.refetch()
                : setStoredRequested(true)
            }
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
          >
            Stored Summaries
          </button>
          <button
            type="button"
            onClick={() =>
              latestHandoffRequested
                ? void latestHandoffQuery.refetch()
                : setLatestHandoffRequested(true)
            }
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
          >
            Latest Handoff
          </button>
          <button
            type="button"
            onClick={() =>
              eventsRequested
                ? void eventsQuery.refetch()
                : setEventsRequested(true)
            }
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
          >
            Load Session Events
          </button>
        </div>
      </div>

      {summaryQuery.error && (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          {formatApiErrorMessage(summaryQuery.error)}
        </div>
      )}
      {storedQuery.error && (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          Stored summaries: {formatApiErrorMessage(storedQuery.error)}
        </div>
      )}
      {latestHandoffQuery.error && (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          Latest handoff: {formatApiErrorMessage(latestHandoffQuery.error)}
        </div>
      )}
      {eventsQuery.error && (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          Session events: {formatApiErrorMessage(eventsQuery.error)}
        </div>
      )}

      {summaryQuery.data && (
        <div
          data-testid="session-activity-summary"
          className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/40 p-3 text-xs space-y-2"
        >
          <div className="font-semibold text-gray-900 dark:text-white">
            Session Summary ({sessionId}) — {summaryAgents.length} agent pane(s)
            {typeof summaryQuery.data.summary?.total_files === "number"
              ? ` · ${summaryQuery.data.summary.total_files} file(s)`
              : ""}
            {typeof summaryQuery.data.summary?.total_output_lines === "number"
              ? ` · ${summaryQuery.data.summary.total_output_lines} line(s)`
              : ""}
          </div>
          {summaryAgents.length > 0 && (
            <div className="space-y-1">
              {summaryAgents.map((agent, idx) => (
                <div
                  key={agent.pane_id ?? agent.pane_title ?? idx}
                  className="font-mono text-[11px] text-gray-700 dark:text-gray-300"
                >
                  {agent.pane_title || agent.pane_id || `pane-${idx}`}
                  {agent.agent_type ? ` (${agent.agent_type})` : ""}
                  {agent.state ? ` · ${agent.state}` : ""}
                  {agent.files_modified && agent.files_modified.length > 0
                    ? ` · files: ${agent.files_modified.join(", ")}`
                    : ""}
                  {agent.key_actions && agent.key_actions.length > 0
                    ? ` · ${agent.key_actions.slice(0, 2).join("; ")}`
                    : ""}
                </div>
              ))}
            </div>
          )}
          {summaryQuery.data.summary?.conflicts &&
            summaryQuery.data.summary.conflicts.length > 0 && (
              <div className="text-[11px] text-amber-700 dark:text-amber-300">
                Conflicts: {summaryQuery.data.summary.conflicts.join(", ")}
              </div>
            )}
          {(summaryQuery.data.summary?.text || summaryQuery.data.text) && (
            <pre className="whitespace-pre-wrap font-mono text-[11px] text-gray-700 dark:text-gray-300">
              {summaryQuery.data.summary?.text || summaryQuery.data.text}
            </pre>
          )}
        </div>
      )}

      {storedQuery.data && (
        <div
          data-testid="stored-summaries-list"
          className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/40 p-3 text-xs space-y-1.5"
        >
          <div className="font-semibold text-gray-900 dark:text-white">
            Archived Summaries ({storedList.length})
          </div>
          {storedList.map((f) => (
            <div
              key={f.path}
              className="font-mono text-[11px] text-gray-700 dark:text-gray-300"
            >
              {f.session} · {f.timestamp} · {f.path}
            </div>
          ))}
        </div>
      )}

      {latestHandoffQuery.data && (() => {
        const latestHandoff =
          latestHandoffQuery.data.latest ?? latestHandoffQuery.data.handoff;
        const latestPath =
          latestHandoffQuery.data.latest_path ?? latestHandoffQuery.data.path;
        const qualityLabel =
          latestHandoffQuery.data.quality?.status ??
          latestHandoffQuery.data.quality?.grade;
        return (
          <div
            data-testid="latest-handoff-snapshot"
            className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/40 p-3 text-xs space-y-1"
          >
            <div className="font-semibold text-gray-900 dark:text-white">
              Latest Handoff ({latestHandoffQuery.data.session})
              {latestHandoff?.status ? ` · ${latestHandoff.status}` : ""}
              {qualityLabel
                ? ` · Quality ${qualityLabel}${
                    typeof latestHandoffQuery.data.quality?.score === "number"
                      ? ` (${latestHandoffQuery.data.quality.score})`
                      : ""
                  }`
                : ""}
            </div>
            {latestHandoff ? (
              <div className="space-y-1 text-gray-700 dark:text-gray-300">
                <div>
                  <strong>Goal:</strong> {latestHandoff.goal || "—"} ·{" "}
                  <strong>Now:</strong> {latestHandoff.now || "—"}
                </div>
                {latestPath && (
                  <div className="font-mono text-[11px] text-gray-500 dark:text-gray-400">
                    {latestPath}
                  </div>
                )}
              </div>
            ) : (
              <div className="text-gray-500 dark:text-gray-400">
                No saved handoff found for this session.
              </div>
            )}
          </div>
        );
      })()}

      {eventsQuery.data && (
        <div
          data-testid="session-events-log"
          className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/40 p-3 text-xs space-y-1.5"
        >
          <div className="font-semibold text-gray-900 dark:text-white">
            Session Events ({eventRows.length})
          </div>
          {eventRows.slice(0, 10).map((ev, idx) => (
            <div
              key={`${ev.id ?? idx}`}
              className="font-mono text-[11px] text-gray-700 dark:text-gray-300"
            >
              [{ev.type || ev.event_type || "event"}]{" "}
              {ev.summary || ev.message || ev.timestamp || "—"}
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
