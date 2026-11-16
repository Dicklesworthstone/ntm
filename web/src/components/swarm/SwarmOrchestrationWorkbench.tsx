"use client";

/**
 * SwarmOrchestrationWorkbench
 *
 * Interactive multi-agent task assignment planner, prompt router, and
 * pre-dispatch prompt safety/lint preflight workbench on /swarm:
 *   - POST /api/v1/assign (bead-to-agent assignment recommendations)
 *   - POST /api/v1/route (optimal target pane routing recommendation)
 *   - POST /api/v1/prompt/preflight (lint, token estimate, secrets & DCG check)
 */

import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface AssignRecommendationItem {
  pane_id: string;
  pane_target: string;
  agent_type: string;
  model?: string;
  assign_bead: string;
  bead_title: string;
  priority: string;
  confidence: number;
  reasoning: string;
}

export interface AssignPlanResponse extends ApiEnvelope {
  session: string;
  strategy: string;
  relaxed: boolean;
  recommendations: AssignRecommendationItem[];
  summary: {
    total_agents: number;
    idle_agents: number;
    working_agents: number;
    ready_beads: number;
    blocked_beads: number;
    recommendations: number;
  };
}

export interface RouteCandidateItem {
  pane_id: string;
  pane_index: number;
  agent_type: string;
  state: string;
  score: number;
  reason?: string;
}

export interface RoutePlanResponse extends ApiEnvelope {
  session: string;
  strategy: string;
  recommendation?: RouteCandidateItem | null;
  candidates: RouteCandidateItem[];
}

export interface PreflightFindingItem {
  id: string;
  severity: string;
  message: string;
  help?: string;
  line?: number;
}

export interface PromptPreflightResponse extends ApiEnvelope {
  preview_hash: string;
  preview_len: number;
  estimated_tokens: number;
  findings: PreflightFindingItem[];
  error_count: number;
  warning_count: number;
  info_count: number;
  dcg_available: boolean;
  strict?: boolean;
  allowed?: boolean;
}

export function SwarmOrchestrationWorkbench({
  defaultSession = "",
}: {
  defaultSession?: string;
}) {
  // 1. Task Assignment Planner (POST /api/v1/assign)
  const [assignSession, setAssignSession] = useState(defaultSession);
  const [assignStrategy, setAssignStrategy] = useState<
    "balanced" | "speed" | "quality" | "dependency" | "simple"
  >("balanced");
  const [assignBeadsCSV, setAssignBeadsCSV] = useState("");
  const [assignRelax, setAssignRelax] = useState(false);
  const [assignResult, setAssignResult] = useState<AssignPlanResponse | null>(
    null
  );
  const [assignError, setAssignError] = useState<string | null>(null);

  const assignMutation = useMutation({
    mutationFn: () => {
      const beads = assignBeadsCSV
        .split(",")
        .map((b) => b.trim())
        .filter(Boolean);
      return apiFetch<AssignPlanResponse>("/api/v1/assign", {
        method: "POST",
        body: JSON.stringify({
          session: assignSession.trim(),
          strategy: assignStrategy,
          beads: beads.length > 0 ? beads : undefined,
          relax: assignRelax,
        }),
      });
    },
    onSuccess: (res) => {
      setAssignResult(res);
      setAssignError(null);
    },
    onError: (err) => {
      setAssignResult(null);
      setAssignError(formatApiErrorMessage(err));
    },
  });

  // 2. Prompt Preflight & Agent Router (POST /api/v1/prompt/preflight + POST /api/v1/route)
  const [promptDraft, setPromptDraft] = useState("");
  const [strictPreflight, setStrictPreflight] = useState(false);
  const [routeSession, setRouteSession] = useState(defaultSession);
  const [routeStrategy, setRouteStrategy] = useState<
    "least-loaded" | "affinity" | "round-robin" | "sticky" | "first-available"
  >("least-loaded");
  const [routeAgentType, setRouteAgentType] = useState("");
  const [preflightResult, setPreflightResult] =
    useState<PromptPreflightResponse | null>(null);
  const [preflightError, setPreflightError] = useState<string | null>(null);
  const [routeResult, setRouteResult] = useState<RoutePlanResponse | null>(null);
  const [routeError, setRouteError] = useState<string | null>(null);

  const preflightMutation = useMutation({
    mutationFn: () =>
      apiFetch<PromptPreflightResponse>("/api/v1/prompt/preflight", {
        method: "POST",
        body: JSON.stringify({
          prompt: promptDraft,
          strict: strictPreflight,
        }),
      }),
    onSuccess: (res) => {
      setPreflightResult(res);
      setPreflightError(null);
    },
    onError: (err) => {
      setPreflightResult(null);
      setPreflightError(formatApiErrorMessage(err));
    },
  });

  const routeMutation = useMutation({
    mutationFn: () =>
      apiFetch<RoutePlanResponse>("/api/v1/route", {
        method: "POST",
        body: JSON.stringify({
          session: routeSession.trim(),
          strategy: routeStrategy,
          type: routeAgentType.trim() || undefined,
          prompt: promptDraft.trim() || undefined,
        }),
      }),
    onSuccess: (res) => {
      setRouteResult(res);
      setRouteError(null);
    },
    onError: (err) => {
      setRouteResult(null);
      setRouteError(formatApiErrorMessage(err));
    },
  });

  const handleAssignSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!assignSession.trim()) {
      setAssignError("Session name is required to compute assignment plan.");
      return;
    }
    assignMutation.mutate();
  };

  return (
    <section
      aria-label="Swarm Task Assignment, Routing & Prompt Preflight"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-5"
    >
      <div>
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
          Task Assignment Planner, Prompt Router &amp; Safety Preflight
        </h2>
        <p className="text-xs text-gray-500 dark:text-gray-400">
          Compute capability-scored bead assignments (<code className="font-mono">/api/v1/assign</code>), lint prompts for secrets/destructive commands (<code className="font-mono">/api/v1/prompt/preflight</code>), and pick the optimal target pane (<code className="font-mono">/api/v1/route</code>).
        </p>
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        {/* Left: Task Assignment Planner */}
        <form
          onSubmit={handleAssignSubmit}
          aria-label="Task Assignment Planner"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
            Bead-to-Agent Assignment Planner
          </h3>

          <div className="grid gap-2 sm:grid-cols-2">
            <div>
              <label
                htmlFor="assign-session-input"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Session
              </label>
              <input
                id="assign-session-input"
                aria-label="Assign Session"
                value={assignSession}
                onChange={(e) => setAssignSession(e.target.value)}
                placeholder="e.g. ntm-swarm"
                className="w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
              />
            </div>

            <div>
              <label
                htmlFor="assign-strategy-select"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Strategy
              </label>
              <select
                id="assign-strategy-select"
                aria-label="Assignment Strategy"
                value={assignStrategy}
                onChange={(e) =>
                  setAssignStrategy(
                    e.target.value as
                      | "balanced"
                      | "speed"
                      | "quality"
                      | "dependency"
                      | "simple"
                  )
                }
                className="w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
              >
                <option value="balanced">balanced (capability + load)</option>
                <option value="dependency">dependency (unblock graph)</option>
                <option value="quality">quality (highest model affinity)</option>
                <option value="speed">speed (fastest throughput)</option>
                <option value="simple">simple (sequential idle pairing)</option>
              </select>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <input
              aria-label="Optional Bead IDs"
              value={assignBeadsCSV}
              onChange={(e) => setAssignBeadsCSV(e.target.value)}
              placeholder="Optional bead IDs (bd-1, bd-2)"
              className="flex-1 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <label className="flex items-center gap-1.5 text-xs text-gray-700 dark:text-gray-300 cursor-pointer">
              <input
                type="checkbox"
                checked={assignRelax}
                onChange={(e) => setAssignRelax(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              Relax score floor
            </label>
            <button
              type="submit"
              disabled={assignMutation.isPending}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {assignMutation.isPending
                ? "Planning…"
                : "Recommend Assignments"}
            </button>
          </div>

          {assignError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {assignError}
            </div>
          )}

          {assignResult && (
            <div
              data-testid="assign-plan-results"
              className="space-y-2 pt-2 border-t border-gray-200 dark:border-gray-700 text-xs"
            >
              <div className="flex flex-wrap gap-2 text-gray-600 dark:text-gray-300">
                <span>
                  Idle agents: <strong>{assignResult.summary.idle_agents}</strong>
                </span>
                <span>·</span>
                <span>
                  Ready beads: <strong>{assignResult.summary.ready_beads}</strong>
                </span>
                <span>·</span>
                <span>
                  Recommendations:{" "}
                  <strong>{assignResult.recommendations.length}</strong>
                </span>
              </div>

              {assignResult.recommendations.map((rec) => (
                <div
                  key={`${rec.pane_id}-${rec.assign_bead}`}
                  className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-2.5 space-y-1"
                >
                  <div className="flex items-center justify-between gap-2 font-mono">
                    <span className="font-semibold text-gray-900 dark:text-white">
                      {rec.assign_bead} → {rec.pane_target} ({rec.agent_type})
                    </span>
                    <span className="text-emerald-700 dark:text-emerald-300">
                      {Math.round(rec.confidence * 100)}%
                    </span>
                  </div>
                  <div className="text-gray-800 dark:text-gray-200">
                    {rec.bead_title}
                  </div>
                  <div className="text-[11px] text-gray-500 dark:text-gray-400">
                    {rec.reasoning}
                  </div>
                </div>
              ))}
            </div>
          )}
        </form>

        {/* Right: Prompt Preflight & Optimal Agent Router */}
        <div
          role="region"
          aria-label="Prompt Preflight & Agent Router"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
            Prompt Safety Preflight &amp; Target Pane Router
          </h3>

          <textarea
            aria-label="Candidate Prompt"
            rows={3}
            value={promptDraft}
            onChange={(e) => setPromptDraft(e.target.value)}
            placeholder="Paste prompt to lint for secrets, PII, token budget, and destructive commands…"
            className="w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 p-2.5 text-xs font-mono text-gray-900 dark:text-white"
          />

          <div className="flex flex-wrap items-center gap-2">
            <label className="flex items-center gap-1.5 text-xs text-gray-700 dark:text-gray-300 cursor-pointer">
              <input
                type="checkbox"
                checked={strictPreflight}
                onChange={(e) => setStrictPreflight(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              Strict mode
            </label>
            <button
              type="button"
              disabled={!promptDraft.trim() || preflightMutation.isPending}
              onClick={() => preflightMutation.mutate()}
              className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {preflightMutation.isPending ? "Linting…" : "Run Prompt Preflight"}
            </button>
          </div>

          <div className="grid gap-2 sm:grid-cols-3 pt-1">
            <input
              aria-label="Route Session"
              value={routeSession}
              onChange={(e) => setRouteSession(e.target.value)}
              placeholder="Route session"
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <select
              aria-label="Route Strategy"
              value={routeStrategy}
              onChange={(e) =>
                setRouteStrategy(
                  e.target.value as
                    | "least-loaded"
                    | "affinity"
                    | "round-robin"
                    | "sticky"
                    | "first-available"
                )
              }
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            >
              <option value="least-loaded">least-loaded</option>
              <option value="affinity">affinity (prompt match)</option>
              <option value="round-robin">round-robin</option>
              <option value="sticky">sticky</option>
              <option value="first-available">first-available</option>
            </select>
            <div className="flex gap-1.5">
              <input
                aria-label="Route Harness Filter"
                value={routeAgentType}
                onChange={(e) => setRouteAgentType(e.target.value)}
                placeholder="cc/cod/agy"
                className="w-20 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
              />
              <button
                type="button"
                disabled={!routeSession.trim() || routeMutation.isPending}
                onClick={() => routeMutation.mutate()}
                className="flex-1 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-2.5 py-1.5 text-xs font-medium text-gray-800 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
              >
                Route
              </button>
            </div>
          </div>

          {preflightError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {preflightError}
            </div>
          )}

          {preflightResult && (
            <div
              data-testid="prompt-preflight-result"
              className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-2.5 text-xs space-y-1.5"
            >
              <div className="flex flex-wrap items-center justify-between gap-2 font-mono">
                <span>
                  ~{preflightResult.estimated_tokens} tokens (
                  {preflightResult.preview_len} B · #{preflightResult.preview_hash}
                  )
                </span>
                <span
                  className={
                    preflightResult.error_count > 0
                      ? "font-semibold text-red-600 dark:text-red-400"
                      : "font-semibold text-emerald-700 dark:text-emerald-300"
                  }
                >
                  {preflightResult.error_count > 0
                    ? `BLOCKED (${preflightResult.error_count} errors)`
                    : `PASSED (${preflightResult.warning_count} warnings)`}
                </span>
              </div>
              {preflightResult.findings.map((f, idx) => (
                <div
                  key={`${f.id}-${idx}`}
                  className="text-[11px] text-gray-700 dark:text-gray-300"
                >
                  <strong>[{f.severity.toUpperCase()}]</strong>{" "}
                  <code className="font-mono">{f.id}</code>: {f.message}
                </div>
              ))}
            </div>
          )}

          {routeError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {routeError}
            </div>
          )}

          {routeResult && (
            <div
              data-testid="prompt-route-result"
              className="rounded border border-emerald-200 dark:border-emerald-800 bg-emerald-50/60 dark:bg-emerald-900/20 p-2.5 text-xs text-emerald-900 dark:text-emerald-200"
            >
              {routeResult.recommendation ? (
                <span>
                  Recommended pane:{" "}
                  <strong className="font-mono">
                    {routeResult.recommendation.pane_id} (pane{" "}
                    {routeResult.recommendation.pane_index} ·{" "}
                    {routeResult.recommendation.agent_type})
                  </strong>{" "}
                  — score {routeResult.recommendation.score.toFixed(1)}
                </span>
              ) : (
                <span>No eligible idle agent candidates found.</span>
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
