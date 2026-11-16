"use client";

/**
 * SessionTimelinePanel
 *
 * Surfaces persisted agent state-transition timelines on /sessions:
 *   - GET /api/v1/timelines/{sessionId}?events=true
 *   - GET /api/v1/timelines/stats (global timeline store overview)
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface TimelineInfoSummary {
  session_id: string;
  path?: string;
  event_count: number;
  agent_count: number;
  size_bytes: number;
  compressed: boolean;
  created_at?: string;
  modified_at?: string;
}

export interface TimelineAgentEvent {
  agent_id: string;
  state: string;
  timestamp: string;
  duration?: number;
  details?: string;
}

export interface TimelineEventStats {
  total_events: number;
  unique_agents: number;
  agent_breakdown: Record<string, number>;
  state_breakdown: Record<string, number>;
  first_event?: string;
  last_event?: string;
}

export interface SessionTimelineResponse extends ApiEnvelope {
  action?: string;
  session?: string;
  info?: TimelineInfoSummary;
  events?: TimelineAgentEvent[];
  session_stats?: TimelineEventStats;
}

export interface TimelineStoreStatsResponse extends ApiEnvelope {
  total_count: number;
  timelines?: TimelineInfoSummary[];
  aggregate_stats?: {
    total_timelines: number;
    total_events: number;
    total_agents: number;
    total_size_bytes: number;
    compressed_count: number;
  };
}

export function SessionTimelinePanel({ sessionId }: { sessionId: string }) {
  const [timelineRequested, setTimelineRequested] = useState(false);
  const [storeStatsRequested, setStoreStatsRequested] = useState(false);

  const encodedSession = encodeURIComponent(sessionId);

  const timelineQuery = useQuery({
    queryKey: ["session-timeline", sessionId],
    queryFn: () =>
      apiFetch<SessionTimelineResponse>(
        `/api/v1/timelines/${encodedSession}?events=true`
      ),
    enabled: timelineRequested && Boolean(sessionId),
    retry: false,
  });

  const storeStatsQuery = useQuery({
    queryKey: ["timelines", "stats"],
    queryFn: () =>
      apiFetch<TimelineStoreStatsResponse>("/api/v1/timelines/stats"),
    enabled: storeStatsRequested,
    retry: false,
  });

  const stats = timelineQuery.data?.session_stats;
  const events = timelineQuery.data?.events ?? [];
  const stateBreakdown = Object.entries(stats?.state_breakdown ?? {});

  return (
    <section
      aria-label="Session State Timeline"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Session State Timeline
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Inspect persisted agent state transitions (<code className="font-mono">idle</code>, <code className="font-mono">working</code>, <code className="font-mono">waiting</code>, <code className="font-mono">error</code>) from <code className="font-mono">/api/v1/timelines</code>.
          </p>
        </div>

        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            disabled={!sessionId || timelineQuery.isFetching}
            onClick={() => {
              if (timelineRequested) {
                void timelineQuery.refetch();
              } else {
                setTimelineRequested(true);
              }
            }}
            className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {timelineQuery.isFetching
              ? "Loading Timeline…"
              : timelineRequested
                ? "Refresh Timeline"
                : "Load Session Timeline"}
          </button>

          <button
            type="button"
            disabled={storeStatsQuery.isFetching}
            onClick={() => {
              if (storeStatsRequested) {
                void storeStatsQuery.refetch();
              } else {
                setStoreStatsRequested(true);
              }
            }}
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
          >
            {storeStatsQuery.isFetching
              ? "Loading Stats…"
              : "Load Timeline Store Stats"}
          </button>
        </div>
      </div>

      {storeStatsRequested && storeStatsQuery.data && (
        <div
          data-testid="timeline-store-stats"
          className="rounded-md border border-blue-200 dark:border-blue-800 bg-blue-50/70 dark:bg-blue-900/20 p-3 text-xs text-blue-900 dark:text-blue-200 flex flex-wrap items-center justify-between gap-2"
        >
          <span>
            Saved Timelines:{" "}
            <strong>
              {storeStatsQuery.data.aggregate_stats?.total_timelines ??
                storeStatsQuery.data.total_count}
            </strong>
          </span>
          <span>
            Total Events:{" "}
            <strong>
              {storeStatsQuery.data.aggregate_stats?.total_events ?? 0}
            </strong>{" "}
            across{" "}
            {storeStatsQuery.data.aggregate_stats?.total_agents ?? 0} agents
          </span>
        </div>
      )}

      {timelineRequested && (
        <div data-testid="session-timeline-detail" className="space-y-3 text-xs">
          {timelineQuery.isFetching && !timelineQuery.data ? (
            <div className="text-gray-500 dark:text-gray-400">
              Loading session timeline…
            </div>
          ) : timelineQuery.error ? (
            <div className="rounded border border-amber-200 bg-amber-50 p-3 text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
              {formatApiErrorMessage(timelineQuery.error)}
            </div>
          ) : !timelineQuery.data?.info ? (
            <div className="text-gray-500 dark:text-gray-400">
              No persisted timeline transitions recorded for {sessionId} yet.
            </div>
          ) : (
            <>
              <div className="flex flex-wrap gap-3 text-gray-700 dark:text-gray-200">
                <span className="rounded bg-gray-100 dark:bg-gray-900 px-2.5 py-1 font-mono">
                  events={timelineQuery.data.info.event_count}
                </span>
                <span className="rounded bg-gray-100 dark:bg-gray-900 px-2.5 py-1 font-mono">
                  agents={timelineQuery.data.info.agent_count}
                </span>
                {stateBreakdown.map(([st, cnt]) => (
                  <span
                    key={st}
                    className="rounded bg-indigo-50 dark:bg-indigo-900/30 px-2.5 py-1 font-mono text-indigo-800 dark:text-indigo-200"
                  >
                    {st}={cnt}
                  </span>
                ))}
              </div>

              {events.length > 0 && (
                <div className="max-h-52 overflow-y-auto rounded border border-gray-200 dark:border-gray-700">
                  <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
                    <thead className="bg-gray-50 dark:bg-gray-900 text-gray-500 dark:text-gray-400">
                      <tr>
                        <th className="px-2.5 py-1.5 text-left">Time</th>
                        <th className="px-2.5 py-1.5 text-left">Agent</th>
                        <th className="px-2.5 py-1.5 text-left">State</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-gray-200 dark:divide-gray-700 bg-white dark:bg-gray-800">
                      {events.slice(-20).map((ev, idx) => (
                        <tr key={`${idx}-${ev.timestamp}-${ev.agent_id}`}>
                          <td className="px-2.5 py-1 font-mono text-[11px] text-gray-500 dark:text-gray-400">
                            {new Date(ev.timestamp).toLocaleTimeString()}
                          </td>
                          <td className="px-2.5 py-1 font-mono text-gray-900 dark:text-white">
                            {ev.agent_id}
                          </td>
                          <td className="px-2.5 py-1 font-mono font-medium text-gray-700 dark:text-gray-200">
                            {ev.state}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </div>
      )}
    </section>
  );
}
