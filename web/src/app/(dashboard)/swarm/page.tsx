"use client";

/**
 * Swarm Backpressure Heatmap & Synthetic Load Lab
 *
 * Operator cockpit for live overload telemetry, load-shedding contracts,
 * per-session heatmaps, and deterministic in-memory synthetic swarm experiments:
 *   - GET  /api/v1/health/backpressure
 *   - GET  /api/v1/swarm/lab/scenarios
 *   - POST /api/v1/swarm/lab/run
 */

import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type components,
} from "@/lib/api/client";

type HealthBackpressureResponse =
  components["schemas"]["HealthBackpressureEnvelopeResponse"];
type SurfaceSnapshot = components["schemas"]["SurfaceSnapshot"];
type SwarmLabResponse = components["schemas"]["SwarmLabEnvelopeResponse"];
type SwarmLabRunRequest = components["schemas"]["SwarmLabRunRequest"];
type SyntheticExperimentScenario =
  components["schemas"]["SyntheticExperimentScenario"];
type SyntheticExperimentArtifact =
  components["schemas"]["SyntheticExperimentArtifact"];

export type PressureLevel = "normal" | "elevated" | "critical" | "overload";

function createIdempotencyKey(prefix: string): string {
  const randomPart =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  return `${prefix}-${randomPart}`;
}

export function mapDecisionToPressureLevel(decision?: string): PressureLevel {
  const normalized = (decision || "").trim().toLowerCase();
  switch (normalized) {
    case "degrade":
    case "overload":
      return "overload";
    case "defer":
    case "critical":
    case "warning":
      return "critical";
    case "coalesce":
    case "elevated":
    case "watch":
      return "elevated";
    default:
      return "normal";
  }
}

function pressureBadgeClass(level: PressureLevel): string {
  switch (level) {
    case "overload":
      return "bg-red-100 text-red-800 border-red-300 dark:bg-red-900/40 dark:text-red-200 dark:border-red-700";
    case "critical":
      return "bg-orange-100 text-orange-800 border-orange-300 dark:bg-orange-900/40 dark:text-orange-200 dark:border-orange-700";
    case "elevated":
      return "bg-amber-100 text-amber-800 border-amber-300 dark:bg-amber-900/40 dark:text-amber-200 dark:border-amber-700";
    default:
      return "bg-emerald-100 text-emerald-800 border-emerald-300 dark:bg-emerald-900/30 dark:text-emerald-300 dark:border-emerald-700";
  }
}

function severityBadgeClass(severity?: string): string {
  switch ((severity || "").toLowerCase()) {
    case "critical":
    case "degrade":
      return "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300";
    case "warning":
    case "defer":
      return "bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300";
    case "watch":
    case "coalesce":
      return "bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-300";
    default:
      return "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300";
  }
}

function verdictBadgeClass(result?: string): string {
  switch ((result || "").toLowerCase()) {
    case "pass":
      return "bg-emerald-100 text-emerald-800 border-emerald-300 dark:bg-emerald-900/30 dark:text-emerald-300 dark:border-emerald-700";
    case "warn":
      return "bg-amber-100 text-amber-800 border-amber-300 dark:bg-amber-900/30 dark:text-amber-300 dark:border-amber-700";
    case "fail":
      return "bg-red-100 text-red-800 border-red-300 dark:bg-red-900/30 dark:text-red-300 dark:border-red-700";
    default:
      return "bg-gray-100 text-gray-800 border-gray-300 dark:bg-gray-800 dark:text-gray-200 dark:border-gray-600";
  }
}

function formatBytes(bytes?: number): string {
  if (bytes === undefined || bytes === null) return "—";
  if (bytes === 0) return "0 B";
  const sign = bytes < 0 ? "-" : "";
  let val = Math.abs(bytes);
  const units = ["B", "KB", "MB", "GB"];
  let idx = 0;
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024;
    idx += 1;
  }
  return `${sign}${val.toFixed(idx === 0 ? 0 : 1)} ${units[idx]}`;
}

function findSurface(
  surfaces: SurfaceSnapshot[],
  surfaceName: string
): SurfaceSnapshot | undefined {
  return surfaces.find((s) => s.surface === surfaceName);
}

export default function SwarmPage() {
  const [sessionFilter, setSessionFilter] = useState("");
  const [selectedScenarioId, setSelectedScenarioId] = useState<string>("");
  const [evaluateGate, setEvaluateGate] = useState<boolean>(true);
  const [gateOverride, setGateOverride] = useState<string>("");
  const [labResult, setLabResult] = useState<SwarmLabResponse | null>(null);
  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const trimmedSession = sessionFilter.trim();
  const backpressurePath = useMemo(() => {
    const params = new URLSearchParams();
    if (trimmedSession) {
      params.set("session", trimmedSession);
    }
    const qs = params.toString();
    return `/api/v1/health/backpressure${qs ? `?${qs}` : ""}`;
  }, [trimmedSession]);

  const backpressureQuery = useQuery({
    queryKey: ["swarm", "backpressure", backpressurePath],
    queryFn: () => apiFetch<HealthBackpressureResponse>(backpressurePath),
    refetchInterval: 5_000,
  });

  const scenariosQuery = useQuery({
    queryKey: ["swarm", "lab-scenarios"],
    queryFn: () => apiFetch<SwarmLabResponse>("/api/v1/swarm/lab/scenarios"),
  });

  const scenarios = useMemo(
    () => scenariosQuery.data?.scenarios ?? [],
    [scenariosQuery.data?.scenarios]
  );

  const activeScenario: SyntheticExperimentScenario | undefined = useMemo(() => {
    if (scenarios.length === 0) return undefined;
    if (selectedScenarioId) {
      const found = scenarios.find((s) => s.id === selectedScenarioId);
      if (found) return found;
    }
    return scenarios[0];
  }, [scenarios, selectedScenarioId]);

  const effectiveScenarioId = activeScenario?.id ?? selectedScenarioId;
  const effectiveGate = gateOverride || activeScenario?.gate || "short";

  const runLabMutation = useMutation({
    mutationFn: (payload: SwarmLabRunRequest) =>
      apiFetch<SwarmLabResponse>("/api/v1/swarm/lab/run", {
        method: "POST",
        headers: {
          "Idempotency-Key": createIdempotencyKey("swarm-lab-run"),
        },
        body: JSON.stringify(payload),
      }),
    onSuccess: (res) => {
      setLabResult(res);
      const first = res.experiments?.[0];
      const verdict = first?.comparison?.result || (res.success ? "pass" : "completed");
      setNotice({
        kind: "success",
        text: `Synthetic lab run completed for "${first?.scenario_id || effectiveScenarioId}" — Gate verdict: ${verdict.toUpperCase()}.`,
      });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Synthetic lab run failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const bp = backpressureQuery.data?.backpressure;
  const loadShedding =
    backpressureQuery.data?.load_shedding ?? bp?.load_shedding;
  const heatmap = backpressureQuery.data?.heatmap ?? bp?.heatmap;
  const surfaces = useMemo(() => bp?.surfaces ?? [], [bp?.surfaces]);
  const heatmapBuckets = heatmap?.buckets ?? [];

  const pressureLevel = mapDecisionToPressureLevel(bp?.decision);
  const restSurface = findSurface(surfaces, "rest_handler");
  const sseSurface = findSurface(surfaces, "sse_stream");
  const wsSurface = findSurface(surfaces, "websocket");

  const maxQueueDepth = useMemo(() => {
    return surfaces.reduce(
      (max, s) => (s.queue_depth > max ? s.queue_depth : max),
      0
    );
  }, [surfaces]);

  const totalDrops = useMemo(() => {
    return surfaces.reduce((sum, s) => sum + (s.dropped_count || 0), 0);
  }, [surfaces]);

  const sseOccupancyPct =
    sseSurface?.queue_capacity && sseSurface.queue_capacity > 0
      ? Math.round((sseSurface.queue_depth / sseSurface.queue_capacity) * 100)
      : 0;

  const handleSelectScenario = (scenario: SyntheticExperimentScenario) => {
    setSelectedScenarioId(scenario.id);
    setGateOverride(scenario.gate || "short");
  };

  const handleRunScenario = () => {
    if (!effectiveScenarioId) {
      setNotice({
        kind: "error",
        text: "Select a synthetic scenario to execute.",
      });
      return;
    }

    const payload: SwarmLabRunRequest = {
      scenario: effectiveScenarioId,
      ...(evaluateGate ? { gate: effectiveGate } : {}),
    };
    runLabMutation.mutate(payload);
  };

  const latestArtifacts: SyntheticExperimentArtifact[] =
    labResult?.experiments ?? [];

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Swarm
          </h1>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            Live backpressure telemetry, load-shedding contracts, per-session
            overload heatmap, and synthetic swarm load lab.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void backpressureQuery.refetch()}
          className="self-start sm:self-auto px-3 py-1.5 text-sm font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700"
        >
          Refresh Telemetry
        </button>
      </div>

      {/* Feedback Banner */}
      {notice && (
        <div
          role={notice.kind === "error" ? "alert" : "status"}
          className={`rounded-lg border px-4 py-3 text-sm flex items-center justify-between gap-3 ${
            notice.kind === "error"
              ? "bg-red-50 border-red-200 text-red-800 dark:bg-red-900/20 dark:border-red-800 dark:text-red-200"
              : "bg-emerald-50 border-emerald-200 text-emerald-800 dark:bg-emerald-900/20 dark:border-emerald-800 dark:text-emerald-200"
          }`}
        >
          <span>{notice.text}</span>
          <button
            type="button"
            onClick={() => setNotice(null)}
            className="text-xs underline opacity-80 hover:opacity-100"
          >
            Dismiss
          </button>
        </div>
      )}

      {/* Live Backpressure KPI Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Overall Pressure Level
          </div>
          <div className="mt-2 flex items-center gap-2">
            <span
              data-testid="overall-pressure-badge"
              className={`inline-flex items-center rounded-full border px-2.5 py-0.5 text-sm font-bold uppercase ${pressureBadgeClass(pressureLevel)}`}
            >
              {pressureLevel}
            </span>
            {bp?.decision && (
              <span className="text-xs font-mono text-gray-500 dark:text-gray-400">
                ({bp.decision})
              </span>
            )}
          </div>
          <div className="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {surfaces.length} monitored surface{surfaces.length === 1 ? "" : "s"}
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Queue Depth
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {restSurface
              ? `${restSurface.queue_depth}${restSurface.queue_capacity ? ` / ${restSurface.queue_capacity}` : ""}`
              : maxQueueDepth}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Peak depth across surfaces: {maxQueueDepth}
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            SSE Occupancy
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {sseSurface
              ? `${sseSurface.queue_depth} / ${sseSurface.queue_capacity ?? 64}`
              : "0 / 64"}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Channel utilization: {sseOccupancyPct}%
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            WebSocket Buffer & Drops
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-gray-900 dark:text-white">
              {wsSurface
                ? `${wsSurface.queue_depth}${wsSurface.queue_capacity ? ` / ${wsSurface.queue_capacity}` : ""}`
                : "0"}
            </span>
            <span
              className={`text-xs font-semibold ${
                totalDrops > 0
                  ? "text-red-600 dark:text-red-400"
                  : "text-gray-500 dark:text-gray-400"
              }`}
            >
              · {totalDrops} dropped
            </span>
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            WS broadcast/client send buffer
          </div>
        </div>
      </div>

      {backpressureQuery.error && (
        <div
          role="alert"
          className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
        >
          Backpressure probe error: {formatApiErrorMessage(backpressureQuery.error)}
        </div>
      )}

      {/* Load-Shedding Contract & Per-Session Overload Heatmap */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Load-Shedding Contract Card */}
        <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Load-Shedding Contract
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Deterministic shedding guidance when subsystem queues saturate.
            </p>
          </div>

          <dl className="space-y-2.5 text-sm">
            <div className="flex items-center justify-between gap-2">
              <dt className="text-gray-500 dark:text-gray-400">Decision</dt>
              <dd className="font-mono font-semibold text-gray-900 dark:text-white">
                {loadShedding?.decision || "ok"}
              </dd>
            </div>
            <div className="flex items-center justify-between gap-2">
              <dt className="text-gray-500 dark:text-gray-400">
                Recommended Action
              </dt>
              <dd className="font-mono text-xs rounded bg-gray-100 dark:bg-gray-900 px-2 py-0.5 text-gray-800 dark:text-gray-200">
                {loadShedding?.recommended_action || "continue"}
              </dd>
            </div>
            <div className="flex items-center justify-between gap-2">
              <dt className="text-gray-500 dark:text-gray-400">
                Degraded Surface
              </dt>
              <dd className="font-mono text-xs text-gray-800 dark:text-gray-200">
                {loadShedding?.degraded_surface || "none"}
              </dd>
            </div>
            <div className="flex items-center justify-between gap-2">
              <dt className="text-gray-500 dark:text-gray-400">Retry After</dt>
              <dd className="font-mono text-xs text-gray-800 dark:text-gray-200">
                {loadShedding?.retry_after_ms
                  ? `${loadShedding.retry_after_ms} ms`
                  : "0 ms"}
              </dd>
            </div>
            <div className="flex items-center justify-between gap-2">
              <dt className="text-gray-500 dark:text-gray-400">Schema</dt>
              <dd className="font-mono text-xs text-gray-500 dark:text-gray-400">
                {loadShedding?.schema_version || "ntm.load_shedding.v1"}
              </dd>
            </div>
          </dl>

          {loadShedding?.reason_codes && loadShedding.reason_codes.length > 0 && (
            <div className="pt-2 border-t border-gray-200 dark:border-gray-700">
              <div className="text-xs font-medium text-gray-500 dark:text-gray-400 mb-1.5">
                Active Reason Codes
              </div>
              <div className="flex flex-wrap gap-1.5">
                {loadShedding.reason_codes.map((reason) => (
                  <span
                    key={reason}
                    className="rounded bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200 px-2 py-0.5 text-xs font-mono"
                  >
                    {reason}
                  </span>
                ))}
              </div>
            </div>
          )}

          {/* Subsystem Surface Rows */}
          {surfaces.length > 0 && (
            <div className="pt-2 border-t border-gray-200 dark:border-gray-700 space-y-2">
              <div className="text-xs font-medium text-gray-500 dark:text-gray-400">
                Subsystem Probes
              </div>
              <div className="space-y-1.5">
                {surfaces.map((surf, idx) => (
                  <div
                    key={`${surf.surface}-${surf.session || "global"}-${surf.pane || idx}`}
                    className="flex items-center justify-between rounded border border-gray-200 dark:border-gray-700 px-2.5 py-1.5 text-xs"
                  >
                    <div className="min-w-0">
                      <span className="font-mono font-medium text-gray-900 dark:text-white">
                        {surf.surface}
                      </span>
                      {surf.session && (
                        <span className="ml-1.5 text-gray-500 dark:text-gray-400">
                          ({surf.session})
                        </span>
                      )}
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      <span className="font-mono text-gray-600 dark:text-gray-300">
                        q={surf.queue_depth}
                        {surf.latency_ms ? ` · ${surf.latency_ms}ms` : ""}
                      </span>
                      <span
                        className={`rounded-full px-2 py-0.5 text-[11px] font-medium ${severityBadgeClass(surf.decision)}`}
                      >
                        {surf.decision}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>

        {/* Per-Session Overload Heatmap Table */}
        <section className="lg:col-span-2 rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4">
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Per-Session Overload Heatmap
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Urgency-ranked overload buckets across sessions, panes, and
                streaming surfaces.
              </p>
            </div>
            <div>
              <label htmlFor="swarm-heatmap-session" className="sr-only">
                Filter heatmap by session
              </label>
              <input
                id="swarm-heatmap-session"
                aria-label="Filter heatmap by session"
                value={sessionFilter}
                onChange={(e) => setSessionFilter(e.target.value)}
                placeholder="Filter by session..."
                className="w-full sm:w-56 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
              />
            </div>
          </div>

          {heatmapBuckets.length === 0 ? (
            <div className="rounded-md border border-dashed border-gray-200 dark:border-gray-700 py-10 text-center text-sm text-gray-500 dark:text-gray-400">
              No overloaded sessions or subsystems — all monitored surfaces are nominal.
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700 text-sm">
                <thead>
                  <tr className="text-left text-xs uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    <th className="py-2 pr-3">Session / Pane</th>
                    <th className="py-2 px-3">Surface</th>
                    <th className="py-2 px-3">Severity</th>
                    <th className="py-2 px-3">Bucket</th>
                    <th className="py-2 px-3">Reasons</th>
                    <th className="py-2 pl-3">Action</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100 dark:divide-gray-700/60">
                  {heatmapBuckets.map((bucket, idx) => (
                    <tr
                      key={`${bucket.surface}-${bucket.session || "host"}-${bucket.pane || idx}`}
                      className="text-gray-800 dark:text-gray-200"
                    >
                      <td className="py-2.5 pr-3 font-mono text-xs">
                        <span className="font-semibold">
                          {bucket.session || "host-wide"}
                        </span>
                        {bucket.pane ? ` · pane ${bucket.pane}` : ""}
                      </td>
                      <td className="py-2.5 px-3 font-mono text-xs">
                        {bucket.surface}
                      </td>
                      <td className="py-2.5 px-3">
                        <span
                          className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${severityBadgeClass(bucket.severity)}`}
                        >
                          {bucket.severity} ({bucket.score})
                        </span>
                      </td>
                      <td className="py-2.5 px-3 font-mono text-xs">
                        {bucket.heatmap_bucket}
                      </td>
                      <td className="py-2.5 px-3 font-mono text-xs">
                        {bucket.reason_codes?.length
                          ? bucket.reason_codes.join(", ")
                          : "—"}
                      </td>
                      <td className="py-2.5 pl-3 font-mono text-xs">
                        {bucket.recommended_action}
                        {bucket.retry_after_ms
                          ? ` (${bucket.retry_after_ms}ms)`
                          : ""}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      </div>

      {/* Interactive Synthetic Swarm Lab */}
      <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-5">
        <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-4">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Synthetic Swarm Lab
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Run deterministic in-memory multi-agent load scenarios and verify
              latency, memory, goroutine, and throughput SLO budgets.
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <label className="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
              <input
                type="checkbox"
                checked={evaluateGate}
                onChange={(e) => setEvaluateGate(e.target.checked)}
                className="h-4 w-4 rounded border-gray-300"
              />
              Evaluate SLO Gate
            </label>

            {evaluateGate && (
              <select
                aria-label="SLO Gate Profile"
                value={effectiveGate}
                onChange={(e) => setGateOverride(e.target.value)}
                className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-sm text-gray-900 dark:text-white"
              >
                <option value="short">Gate: short</option>
                <option value="benchmark">Gate: benchmark</option>
                <option value="load">Gate: load</option>
              </select>
            )}

            <button
              type="button"
              disabled={runLabMutation.isPending || !effectiveScenarioId}
              onClick={handleRunScenario}
              className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {runLabMutation.isPending ? "Running Scenario…" : "Run Scenario"}
            </button>
          </div>
        </div>

        {/* Scenario Catalog Picker */}
        {scenariosQuery.isLoading && (
          <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
            Loading synthetic scenario catalog…
          </div>
        )}

        {scenariosQuery.error && (
          <div
            role="alert"
            className="rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
          >
            Failed to load lab scenarios:{" "}
            {formatApiErrorMessage(scenariosQuery.error)}
          </div>
        )}

        {scenarios.length > 0 && (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {scenarios.map((scenario) => {
              const isSelected = scenario.id === effectiveScenarioId;
              const p95BudgetMs = (
                (scenario.budget?.max_latency_p95_micros ?? 0) / 1000
              ).toFixed(1);
              return (
                <button
                  key={scenario.id}
                  type="button"
                  onClick={() => handleSelectScenario(scenario)}
                  className={`text-left rounded-lg border p-4 transition space-y-2 ${
                    isSelected
                      ? "border-blue-500 bg-blue-50/70 dark:border-blue-400 dark:bg-blue-900/20"
                      : "border-gray-200 dark:border-gray-700 bg-gray-50/40 dark:bg-gray-900/30 hover:border-gray-300 dark:hover:border-gray-600"
                  }`}
                >
                  <div className="flex items-start justify-between gap-2">
                    <div className="font-semibold text-sm text-gray-900 dark:text-white">
                      {scenario.synthetic?.name || scenario.id}
                    </div>
                    <span className="shrink-0 rounded-full bg-gray-200 dark:bg-gray-700 px-2 py-0.5 text-[11px] font-mono text-gray-800 dark:text-gray-200">
                      {scenario.gate}
                    </span>
                  </div>

                  <div className="text-xs font-mono text-gray-500 dark:text-gray-400">
                    {scenario.id}
                  </div>

                  {scenario.description && (
                    <p className="text-xs text-gray-600 dark:text-gray-300">
                      {scenario.description}
                    </p>
                  )}

                  <div className="pt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs font-mono text-gray-600 dark:text-gray-400">
                    <span>Agents: {scenario.synthetic?.pane_count ?? 0}</span>
                    <span>
                      Cmds: {scenario.synthetic?.command_count ?? 0}
                    </span>
                    <span>
                      Budget: {scenario.budget?.name || scenario.gate} (p95 ≤{" "}
                      {p95BudgetMs}ms)
                    </span>
                  </div>
                </button>
              );
            })}
          </div>
        )}

        {/* Lab Execution Results Panel */}
        {latestArtifacts.length > 0 && (
          <div className="pt-4 border-t border-gray-200 dark:border-gray-700 space-y-4">
            {latestArtifacts.map((artifact) => {
              const peakLevel = mapDecisionToPressureLevel(
                artifact.backpressure?.decision
              );
              const verdict = artifact.comparison?.result || "unknown";
              const checks = artifact.comparison?.checks ?? [];
              const durationMs = (
                (artifact.metrics?.synthetic_duration_micros ?? 0) / 1000
              ).toFixed(1);

              return (
                <div
                  key={artifact.test_run_id || artifact.scenario_id}
                  className="rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-4 space-y-4"
                >
                  <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
                    <div>
                      <div className="flex flex-wrap items-center gap-2">
                        <h3 className="text-base font-semibold text-gray-900 dark:text-white">
                          Run Result: {artifact.scenario || artifact.scenario_id}
                        </h3>
                        <span
                          data-testid="lab-gate-verdict"
                          className={`inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-bold uppercase ${verdictBadgeClass(verdict)}`}
                        >
                          Gate: {verdict}
                        </span>
                        <span
                          className={`inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold uppercase ${pressureBadgeClass(peakLevel)}`}
                        >
                          Peak Pressure: {peakLevel} (
                          {artifact.backpressure?.decision || "ok"})
                        </span>
                      </div>
                      <div className="mt-1 text-xs font-mono text-gray-500 dark:text-gray-400">
                        Run ID: {artifact.test_run_id} · Gate Profile:{" "}
                        {artifact.gate}
                      </div>
                    </div>
                  </div>

                  {/* Metrics Grid */}
                  <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3">
                    <MetricTile
                      label="Agents / Cmds"
                      value={`${artifact.metrics.pane_count} / ${artifact.metrics.command_count}`}
                    />
                    <MetricTile
                      label="Throughput"
                      value={`${artifact.metrics.event_throughput_per_second.toFixed(1)} ev/s`}
                    />
                    <MetricTile
                      label="Latency P50 / P95"
                      value={`${artifact.metrics.latency_p50_ms.toFixed(2)} / ${artifact.metrics.latency_p95_ms.toFixed(2)} ms`}
                    />
                    <MetricTile
                      label="Memory Growth"
                      value={formatBytes(artifact.metrics.memory_growth_bytes)}
                    />
                    <MetricTile
                      label="Leaked Goroutines"
                      value={String(artifact.metrics.goroutines_leaked)}
                    />
                    <MetricTile label="Duration" value={`${durationMs} ms`} />
                  </div>

                  {/* SLO Gate Check Breakdown */}
                  {checks.length > 0 && (
                    <div className="overflow-x-auto">
                      <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700 text-xs">
                        <thead>
                          <tr className="text-left uppercase tracking-wide text-gray-500 dark:text-gray-400">
                            <th className="py-2 pr-3">SLO Metric</th>
                            <th className="py-2 px-3">Verdict</th>
                            <th className="py-2 px-3">Observed</th>
                            <th className="py-2 pl-3">Budget / Limit</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-gray-100 dark:divide-gray-700/60">
                          {checks.map((chk) => (
                            <tr
                              key={chk.metric}
                              className="text-gray-800 dark:text-gray-200"
                            >
                              <td className="py-2 pr-3 font-mono font-medium">
                                {chk.metric}
                              </td>
                              <td className="py-2 px-3">
                                <span
                                  className={`inline-flex rounded-full border px-2 py-0.5 text-[11px] font-semibold uppercase ${verdictBadgeClass(chk.result)}`}
                                >
                                  {chk.result}
                                </span>
                              </td>
                              <td className="py-2 px-3 font-mono">
                                {chk.current}
                              </td>
                              <td className="py-2 pl-3 font-mono text-gray-600 dark:text-gray-400">
                                {chk.limit}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}

                  {artifact.comparison?.hint && (
                    <div className="text-xs text-gray-600 dark:text-gray-300">
                      Hint: {artifact.comparison.hint}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </section>
    </div>
  );
}

function MetricTile({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-2.5">
      <div className="text-[11px] uppercase tracking-wide text-gray-500 dark:text-gray-400">
        {label}
      </div>
      <div className="mt-1 text-sm font-mono font-semibold text-gray-900 dark:text-white">
        {value}
      </div>
    </div>
  );
}
