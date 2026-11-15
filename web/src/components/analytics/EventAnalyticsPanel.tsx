"use client";

/**
 * EventAnalyticsPanel
 *
 * Surfaces historical multi-harness event analytics from GET /api/v1/analytics
 * (backed by the canonical ORI robot.GetAnalytics engine):
 *   - Prompts sent, estimated tokens, character volume, crashes, restarts, errors
 *   - Per-harness spawn/prompt/token breakdown across all 11 coding harnesses
 *   - Error type distribution & historical session activity
 *   - On-demand Prometheus exposition preview (?format=prometheus)
 */

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  apiRequestSignal,
  formatApiErrorMessage,
  getAuthHeaders,
  getBaseUrl,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface AnalyticsAgentStats {
  count: number;
  prompts: number;
  chars_sent: number;
  tokens_estimated: number;
}

export interface AnalyticsSessionSummary {
  name: string;
  created_at: string;
  agent_count: number;
  prompt_count: number;
}

export interface EventAnalyticsResponse extends ApiEnvelope {
  schema_id?: string;
  schema_version?: string;
  period: string;
  since: string;
  days?: number;
  total_sessions: number;
  total_agents: number;
  total_prompts: number;
  total_chars_sent: number;
  total_tokens_estimated: number;
  agent_breakdown: Record<string, AnalyticsAgentStats>;
  sessions?: AnalyticsSessionSummary[];
  error_count: number;
  error_types: Record<string, number>;
  agent_crashes: number;
  agent_restarts: number;
}

const HARNESS_DISPLAY_LABELS: Record<string, string> = {
  claude: "Claude Code (claude)",
  codex: "OpenAI Codex (codex)",
  gemini: "Gemini CLI (gemini)",
  antigravity: "Antigravity (antigravity)",
  oc: "OpenCode (oc)",
  cursor: "Cursor (cursor)",
  windsurf: "Windsurf (windsurf)",
  aider: "Aider (aider)",
  ollama: "Ollama (ollama)",
  grok: "Grok (grok)",
  omp: "Oh My Pi (omp)",
  copilot: "GitHub Copilot (copilot)",
  pi: "Pi Agent (pi)",
};

function formatHarnessLabel(key: string): string {
  return HARNESS_DISPLAY_LABELS[key] || key;
}

export function EventAnalyticsPanel() {
  const [days, setDays] = useState<number>(30);
  const [sinceInput, setSinceInput] = useState<string>("");
  const [appliedSince, setAppliedSince] = useState<string>("");
  const [prometheusPreview, setPrometheusPreview] = useState<string | null>(
    null
  );
  const [prometheusLoading, setPrometheusLoading] = useState(false);
  const [prometheusError, setPrometheusError] = useState<string | null>(null);

  const queryPath = useMemo(() => {
    const params = new URLSearchParams();
    if (appliedSince) {
      params.set("since", appliedSince);
    } else {
      params.set("days", String(days));
    }
    params.set("sessions", "true");
    return `/api/v1/analytics?${params.toString()}`;
  }, [appliedSince, days]);

  const analyticsQuery = useQuery({
    queryKey: ["event-analytics", days, appliedSince],
    queryFn: () => apiFetch<EventAnalyticsResponse>(queryPath),
    refetchInterval: 30000,
  });

  const data = analyticsQuery.data;

  const sortedHarnesses = useMemo(() => {
    const entries = Object.entries(data?.agent_breakdown ?? {});
    return entries.sort((a, b) => {
      if (b[1].tokens_estimated !== a[1].tokens_estimated) {
        return b[1].tokens_estimated - a[1].tokens_estimated;
      }
      if (b[1].prompts !== a[1].prompts) {
        return b[1].prompts - a[1].prompts;
      }
      if (b[1].count !== a[1].count) {
        return b[1].count - a[1].count;
      }
      return a[0].localeCompare(b[0]);
    });
  }, [data?.agent_breakdown]);

  const sortedErrors = useMemo(() => {
    return Object.entries(data?.error_types ?? {}).sort(
      (a, b) => b[1] - a[1] || a[0].localeCompare(b[0])
    );
  }, [data?.error_types]);

  const handleLoadPrometheus = async () => {
    setPrometheusLoading(true);
    setPrometheusError(null);
    try {
      const baseUrl = getBaseUrl();
      const promParams = new URLSearchParams();
      if (appliedSince) {
        promParams.set("since", appliedSince);
      } else {
        promParams.set("days", String(days));
      }
      promParams.set("format", "prometheus");
      const headers: Record<string, string> = {
        ...getAuthHeaders(),
        Accept: "text/plain",
      };
      const res = await fetch(
        `${baseUrl}/api/v1/analytics?${promParams.toString()}`,
        {
          headers,
          signal: apiRequestSignal(),
        }
      );
      const text = await res.text();
      if (!res.ok) {
        throw new Error(text || `HTTP ${res.status}`);
      }
      setPrometheusPreview(text);
    } catch (err) {
      setPrometheusError(formatApiErrorMessage(err));
    } finally {
      setPrometheusLoading(false);
    }
  };

  return (
    <section
      aria-label="Event Telemetry & Multi-Harness Breakdown"
      className="bg-gray-800 rounded-lg border border-gray-700 p-5 space-y-5"
    >
      <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold text-gray-100">
            Event Telemetry & Multi-Harness Breakdown
          </h2>
          <p className="text-sm text-gray-400">
            Historical spawns, prompts, token estimates, crashes, and error
            rates from <code className="font-mono">/api/v1/analytics</code>
            {data?.period ? ` · ${data.period}` : ""}.
          </p>
        </div>

        <div className="flex flex-wrap items-end gap-2.5">
          <div>
            <label
              htmlFor="event-analytics-days"
              className="block text-xs text-gray-400 mb-1"
            >
              Window
            </label>
            <select
              id="event-analytics-days"
              value={days}
              disabled={Boolean(appliedSince)}
              onChange={(e) => setDays(Number(e.target.value))}
              className="rounded border border-gray-600 bg-gray-900 px-2.5 py-1.5 text-xs text-gray-100 disabled:opacity-50"
            >
              <option value={7}>Last 7 days</option>
              <option value={30}>Last 30 days</option>
              <option value={90}>Last 90 days</option>
            </select>
          </div>

          <div>
            <label
              htmlFor="event-analytics-since"
              className="block text-xs text-gray-400 mb-1"
            >
              Since (YYYY-MM-DD)
            </label>
            <div className="flex items-center gap-1.5">
              <input
                id="event-analytics-since"
                value={sinceInput}
                onChange={(e) => setSinceInput(e.target.value)}
                placeholder="2026-11-01"
                className="w-32 rounded border border-gray-600 bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-100"
              />
              <button
                type="button"
                onClick={() => setAppliedSince(sinceInput.trim())}
                className="rounded border border-gray-600 bg-gray-700 px-2.5 py-1.5 text-xs font-medium text-gray-100 hover:bg-gray-600"
              >
                Apply
              </button>
              {appliedSince && (
                <button
                  type="button"
                  onClick={() => {
                    setSinceInput("");
                    setAppliedSince("");
                  }}
                  className="text-xs text-gray-400 hover:text-gray-200 underline"
                >
                  Clear
                </button>
              )}
            </div>
          </div>

          <button
            type="button"
            disabled={prometheusLoading}
            onClick={() => void handleLoadPrometheus()}
            className="rounded border border-blue-500/40 bg-blue-500/10 px-3 py-1.5 text-xs font-medium text-blue-300 hover:bg-blue-500/20 disabled:opacity-50"
          >
            {prometheusLoading ? "Exporting…" : "Prometheus Metrics"}
          </button>
        </div>
      </div>

      {analyticsQuery.error && (
        <div className="rounded border border-red-800 bg-red-900/20 p-3 text-xs text-red-300">
          {formatApiErrorMessage(analyticsQuery.error)}
        </div>
      )}

      {/* Telemetry KPI cards */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <div className="rounded-lg border border-gray-700 bg-gray-900/60 p-3.5">
          <div className="text-xs text-gray-400">Prompts Sent</div>
          <div
            data-testid="analytics-total-prompts"
            className="mt-1 text-2xl font-bold text-gray-100"
          >
            {analyticsQuery.isLoading ? "…" : (data?.total_prompts ?? 0)}
          </div>
          <div className="mt-0.5 text-xs text-gray-500">
            {(data?.total_chars_sent ?? 0).toLocaleString()} chars sent
          </div>
        </div>

        <div className="rounded-lg border border-gray-700 bg-gray-900/60 p-3.5">
          <div className="text-xs text-gray-400">Estimated Tokens</div>
          <div
            data-testid="analytics-total-tokens"
            className="mt-1 text-2xl font-bold text-gray-100"
          >
            {analyticsQuery.isLoading
              ? "…"
              : (data?.total_tokens_estimated ?? 0).toLocaleString()}
          </div>
          <div className="mt-0.5 text-xs text-gray-500">
            {data?.period || "Selected window"}
          </div>
        </div>

        <div className="rounded-lg border border-gray-700 bg-gray-900/60 p-3.5">
          <div className="text-xs text-gray-400">Historical Spawns</div>
          <div
            data-testid="analytics-total-agents"
            className="mt-1 text-2xl font-bold text-gray-100"
          >
            {analyticsQuery.isLoading ? "…" : (data?.total_agents ?? 0)}
          </div>
          <div className="mt-0.5 text-xs text-gray-500">
            {data?.total_sessions ?? 0} session(s) created
          </div>
        </div>

        <div className="rounded-lg border border-gray-700 bg-gray-900/60 p-3.5">
          <div className="text-xs text-gray-400">Errors & Crashes</div>
          <div
            data-testid="analytics-error-count"
            className="mt-1 text-2xl font-bold text-gray-100"
          >
            {analyticsQuery.isLoading ? "…" : (data?.error_count ?? 0)}
          </div>
          <div className="mt-0.5 text-xs text-gray-500">
            {data?.agent_crashes ?? 0} crash(es) · {data?.agent_restarts ?? 0}{" "}
            restart(s)
          </div>
        </div>
      </div>

      {/* Per-Harness Breakdown */}
      <div className="space-y-2">
        <h3 className="text-sm font-semibold text-gray-200">
          Per-Harness Activity ({sortedHarnesses.length})
        </h3>
        {sortedHarnesses.length === 0 ? (
          <div className="rounded border border-dashed border-gray-700 py-6 text-center text-xs text-gray-500">
            No harness spawn or prompt events recorded in this period.
          </div>
        ) : (
          <div className="overflow-x-auto rounded border border-gray-700">
            <table className="min-w-full text-xs">
              <thead className="bg-gray-900/70 text-gray-400">
                <tr>
                  <th className="p-2.5 text-left font-medium">Harness</th>
                  <th className="p-2.5 text-right font-medium">Spawns</th>
                  <th className="p-2.5 text-right font-medium">Prompts</th>
                  <th className="p-2.5 text-right font-medium">Chars Sent</th>
                  <th className="p-2.5 text-right font-medium">Est. Tokens</th>
                </tr>
              </thead>
              <tbody>
                {sortedHarnesses.map(([harness, stats]) => (
                  <tr
                    key={harness}
                    data-testid={`harness-row-${harness}`}
                    className="border-t border-gray-700/80 hover:bg-gray-700/30"
                  >
                    <td className="p-2.5 font-medium text-gray-200">
                      {formatHarnessLabel(harness)}
                    </td>
                    <td className="p-2.5 text-right font-mono text-gray-300">
                      {stats.count}
                    </td>
                    <td className="p-2.5 text-right font-mono text-gray-300">
                      {stats.prompts}
                    </td>
                    <td className="p-2.5 text-right font-mono text-gray-300">
                      {stats.chars_sent.toLocaleString()}
                    </td>
                    <td className="p-2.5 text-right font-mono text-blue-300">
                      {stats.tokens_estimated.toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Historical Sessions & Error Types */}
      <div className="grid gap-4 lg:grid-cols-2">
        <div className="rounded border border-gray-700 bg-gray-900/40 p-3.5 space-y-2">
          <div className="text-xs font-semibold uppercase tracking-wide text-gray-400">
            Historical Session Activity ({data?.sessions?.length ?? 0})
          </div>
          {(data?.sessions ?? []).length === 0 ? (
            <div className="py-3 text-xs text-gray-500">
              No session creation events in this window.
            </div>
          ) : (
            <ul className="space-y-1.5 text-xs">
              {data?.sessions?.slice(0, 10).map((sess) => (
                <li
                  key={`${sess.name}-${sess.created_at}`}
                  className="flex items-center justify-between gap-2 rounded border border-gray-700/80 bg-gray-800/70 px-2.5 py-1.5"
                >
                  <span className="font-mono font-medium text-gray-200 truncate">
                    {sess.name}
                  </span>
                  <span className="shrink-0 text-gray-400 font-mono">
                    {sess.agent_count} agents · {sess.prompt_count} prompts
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="rounded border border-gray-700 bg-gray-900/40 p-3.5 space-y-2">
          <div className="text-xs font-semibold uppercase tracking-wide text-gray-400">
            Error Distribution ({sortedErrors.length})
          </div>
          {sortedErrors.length === 0 ? (
            <div className="py-3 text-xs text-gray-500">
              Zero error events recorded in this window.
            </div>
          ) : (
            <ul className="space-y-1.5 text-xs">
              {sortedErrors.map(([errType, count]) => (
                <li
                  key={errType}
                  className="flex items-center justify-between gap-2 rounded border border-red-900/50 bg-red-950/30 px-2.5 py-1.5"
                >
                  <span className="font-mono text-red-200">{errType}</span>
                  <span className="font-mono font-semibold text-red-300">
                    {count}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      {prometheusError && (
        <div className="rounded border border-red-800 bg-red-900/20 p-3 text-xs text-red-300">
          {prometheusError}
        </div>
      )}

      {prometheusPreview !== null && (
        <div className="rounded border border-gray-700 bg-gray-950 p-3 space-y-2">
          <div className="flex items-center justify-between text-xs">
            <span className="font-mono text-gray-400">
              Prometheus Exposition (/api/v1/analytics?format=prometheus)
            </span>
            <button
              type="button"
              onClick={() => setPrometheusPreview(null)}
              className="text-gray-400 hover:text-gray-200 underline"
            >
              Hide
            </button>
          </div>
          <pre
            data-testid="analytics-prometheus-preview"
            className="max-h-48 overflow-auto text-[11px] font-mono text-emerald-300 whitespace-pre-wrap"
          >
            {prometheusPreview}
          </pre>
        </div>
      )}
    </section>
  );
}
