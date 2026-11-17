"use client";

/**
 * MetricsExportAndStreamingPanel
 *
 * Analytics operator panel for:
 *   - Session Metrics Export (GET /api/v1/metrics/export?format=json|csv|md|prometheus&session=...)
 *   - Live Pane Streaming Stats (GET /api/v1/streaming/stats)
 */

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  getAuthHeaders,
  getBaseUrl,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface StreamingStatsResponse extends ApiEnvelope {
  active_streams?: number;
  total_chunks?: number;
  total_bytes?: number;
  dropped_chunks?: number;
  active_targets?: string[];
  [key: string]: unknown;
}

export function MetricsExportAndStreamingPanel({
  selectedSession = "",
}: {
  selectedSession?: string;
}) {
  const [exportFormat, setExportFormat] = useState<
    "json" | "csv" | "md" | "prometheus"
  >("json");
  const [exportSession, setExportSession] = useState(selectedSession);
  const [exportOutput, setExportOutput] = useState<string | null>(null);
  const [exportError, setExportError] = useState<string | null>(null);

  const [streamingRequested, setStreamingRequested] = useState(false);

  const streamingQuery = useQuery({
    queryKey: ["analytics", "streaming-stats"],
    queryFn: () => apiFetch<StreamingStatsResponse>("/api/v1/streaming/stats"),
    enabled: streamingRequested,
    retry: false,
  });

  const exportMutation = useMutation({
    mutationFn: async () => {
      const params = new URLSearchParams({ format: exportFormat });
      const targetSession = (exportSession || selectedSession).trim();
      if (targetSession) {
        params.set("session", targetSession);
      }
      const path = `/api/v1/metrics/export?${params.toString()}`;
      if (exportFormat === "json") {
        const jsonRes = await apiFetch<Record<string, unknown>>(path);
        return JSON.stringify(jsonRes, null, 2);
      }
      const resp = await fetch(`${getBaseUrl()}${path}`, {
        headers: getAuthHeaders(),
      });
      const text = await resp.text();
      if (!resp.ok) {
        throw new Error(text || `HTTP ${resp.status}`);
      }
      return text;
    },
    onSuccess: (body) => {
      setExportOutput(body);
      setExportError(null);
    },
    onError: (err) => {
      setExportOutput(null);
      setExportError(formatApiErrorMessage(err));
    },
  });

  const activeTargets = Array.isArray(streamingQuery.data?.active_targets)
    ? streamingQuery.data.active_targets
    : [];

  return (
    <section
      aria-label="Metrics Export & Pane Streaming Stats"
      className="bg-gray-800 rounded-lg border border-gray-700 p-4 space-y-4"
    >
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-5">
        {/* 1. Metrics Export */}
        <div className="space-y-3">
          <div>
            <h2 className="text-base font-semibold text-gray-100">
              Session Metrics Exporter
            </h2>
            <p className="text-xs text-gray-400">
              Export per-session operational metrics as JSON, CSV, Markdown, or
              Prometheus exposition (<code className="font-mono">/api/v1/metrics/export</code>).
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <input
              aria-label="Metrics export session"
              value={exportSession}
              onChange={(e) => setExportSession(e.target.value)}
              placeholder={selectedSession || "Session (default)..."}
              className="rounded bg-gray-900 border border-gray-600 px-2.5 py-1.5 text-xs text-gray-100"
            />
            <select
              aria-label="Metrics export format"
              value={exportFormat}
              onChange={(e) =>
                setExportFormat(
                  e.target.value as "json" | "csv" | "md" | "prometheus"
                )
              }
              className="rounded bg-gray-900 border border-gray-600 px-2.5 py-1.5 text-xs text-gray-100"
            >
              <option value="json">JSON</option>
              <option value="csv">CSV</option>
              <option value="md">Markdown</option>
              <option value="prometheus">Prometheus</option>
            </select>
            <button
              type="button"
              disabled={exportMutation.isPending}
              onClick={() => exportMutation.mutate()}
              className="rounded bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {exportMutation.isPending ? "Exporting…" : "Export Metrics"}
            </button>
          </div>

          {exportError && (
            <div
              role="alert"
              className="rounded border border-red-800 bg-red-900/20 p-2 text-xs text-red-300"
            >
              {exportError}
            </div>
          )}

          {exportOutput !== null && (
            <pre
              data-testid="metrics-export-preview"
              className="max-h-48 overflow-auto rounded border border-gray-700 bg-gray-950 p-2.5 text-[11px] font-mono text-gray-200 whitespace-pre-wrap"
            >
              {exportOutput}
            </pre>
          )}
        </div>

        {/* 2. Live Pane Streaming Stats */}
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-2">
            <div>
              <h2 className="text-base font-semibold text-gray-100">
                Pane Streaming Telemetry
              </h2>
              <p className="text-xs text-gray-400">
                Inspect active WebSocket/SSE pane stream workers and active
                targets (<code className="font-mono">/api/v1/streaming/stats</code>).
              </p>
            </div>
            <button
              type="button"
              disabled={streamingQuery.isFetching}
              onClick={() => {
                if (streamingRequested) {
                  void streamingQuery.refetch();
                } else {
                  setStreamingRequested(true);
                }
              }}
              className="rounded border border-gray-600 bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-200 hover:bg-gray-700 disabled:opacity-50"
            >
              {streamingQuery.isFetching
                ? "Refreshing…"
                : streamingRequested
                  ? "Refresh Stream Stats"
                  : "Inspect Stream Stats"}
            </button>
          </div>

          {streamingQuery.error && (
            <div
              role="alert"
              className="rounded border border-red-800 bg-red-900/20 p-2 text-xs text-red-300"
            >
              {formatApiErrorMessage(streamingQuery.error)}
            </div>
          )}

          {streamingQuery.data && (
            <div
              data-testid="streaming-stats-card"
              className="rounded border border-gray-700 bg-gray-900/70 p-3 text-xs text-gray-200 space-y-1.5"
            >
              <div className="flex flex-wrap items-center gap-4 font-mono">
                <span>
                  Active Targets: <strong>{activeTargets.length}</strong>
                </span>
                {typeof streamingQuery.data.active_streams === "number" && (
                  <span>
                    Streams: <strong>{streamingQuery.data.active_streams}</strong>
                  </span>
                )}
                {typeof streamingQuery.data.dropped_chunks === "number" && (
                  <span>
                    Dropped: <strong>{streamingQuery.data.dropped_chunks}</strong>
                  </span>
                )}
              </div>
              {activeTargets.length > 0 && (
                <div className="text-[11px] font-mono text-gray-400">
                  Targets: {activeTargets.join(", ")}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
