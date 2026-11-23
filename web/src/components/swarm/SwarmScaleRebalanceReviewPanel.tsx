"use client";

/**
 * SwarmScaleRebalanceReviewPanel
 *
 * Operator workbench on /swarm for:
 *   - 12-Harness Session Scaling (POST /api/v1/sessions/{id}/scale)
 *   - Workload Imbalance Analysis & Rebalancing (POST /api/v1/sessions/{id}/rebalance)
 *   - Cross-Agent Peer Review Queue (POST /api/v1/sessions/{id}/review-queue)
 *   - Session Coordinator Status, Digest & File Conflicts
 *     (GET /api/v1/sessions/{id}/coordinator,
 *      POST /api/v1/sessions/{id}/coordinator/digest,
 *      GET /api/v1/sessions/{id}/coordinator/conflicts)
 */

import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

const SCALE_HARNESSES = [
  { key: "cc", label: "Claude (cc)" },
  { key: "cod", label: "Codex (cod)" },
  { key: "gmi", label: "Gemini (gmi)" },
  { key: "agy", label: "Antigravity (agy)" },
  { key: "grok", label: "Grok (grok)" },
  { key: "omp", label: "Oh My Pi (omp)" },
  { key: "pi", label: "Pi (pi)" },
  { key: "oc", label: "OpenCode (oc)" },
  { key: "ollama", label: "Ollama" },
  { key: "cursor", label: "Cursor" },
  { key: "windsurf", label: "Windsurf" },
  { key: "aider", label: "Aider" },
] as const;

export interface ScaleActionItem {
  type: string;
  agent_type: string;
  count: number;
  agents: string[];
}

export interface SessionScaleResponse extends ApiEnvelope {
  session: string;
  before: Record<string, number>;
  after: Record<string, number>;
  actions: ScaleActionItem[];
  dry_run?: boolean;
  errors?: string[];
}

export interface RebalanceWorkloadItem {
  pane: number;
  pane_target: string;
  pane_id: string;
  agent_type: string;
  agent_name?: string;
  task_count: number;
  task_ids?: string[];
  is_healthy: boolean;
  is_idle: boolean;
}

export interface RebalanceTransferItem {
  bead_id: string;
  bead_title: string;
  from_pane: number;
  from_target: string;
  from_pane_id: string;
  from_agent: string;
  to_pane: number;
  to_target: string;
  to_pane_id: string;
  to_agent: string;
  reason: string;
}

export interface SessionRebalanceResponse extends ApiEnvelope {
  session: string;
  imbalance_score: number;
  recommendation: string;
  transfers: RebalanceTransferItem[];
  workloads: RebalanceWorkloadItem[];
  before: Record<string, number>;
  after: Record<string, number>;
  applied?: boolean;
  dry_run?: boolean;
}

export interface ReviewQueueIdleAgent {
  pane: number;
  agent_type: string;
  agent_name?: string;
  idle_duration?: string;
  last_task?: string;
}

export interface ReviewQueueSuggestion {
  agent: string;
  pane: number;
  agent_type: string;
  prompt: string;
  source: string;
  source_ref?: string;
}

export interface SessionReviewQueueResponse extends ApiEnvelope {
  session: string;
  idle_agents: ReviewQueueIdleAgent[];
  suggestions: ReviewQueueSuggestion[];
  dispatched?: boolean;
  sent?: number;
  skipped?: number;
}

export interface CoordinatorResponse extends ApiEnvelope {
  session: string;
  action: string;
  agent_count?: number;
  idle_count?: number;
  runtime?: {
    host?: string;
    state?: string;
    summary?: string;
    healthy?: boolean;
  };
  digest?: {
    session?: string;
    agent_count?: number;
    active_count?: number;
    idle_count?: number;
    error_count?: number;
    alerts?: string[];
  };
  digest_sent?: boolean;
  conflicts?: Array<{
    pattern: string;
    detected_at?: string;
    holders?: Array<{ agent_name: string; reason?: string }>;
  }>;
  conflict_count?: number;
}

export function SwarmScaleRebalanceReviewPanel() {
  const [sessionName, setSessionName] = useState("");

  // Scale state
  const [scaleTargets, setScaleTargets] = useState<Record<string, string>>({
    cc: "",
    cod: "",
    agy: "",
  });
  const [scaleDryRun, setScaleDryRun] = useState(true);
  const [scaleForce, setScaleForce] = useState(false);

  // Rebalance state
  const [rebalanceFilter, setRebalanceFilter] = useState("");
  const [rebalanceThreshold, setRebalanceThreshold] = useState("0");
  const [rebalanceGeometry, setRebalanceGeometry] = useState(false);

  // Review queue state
  const [reviewFilter, setReviewFilter] = useState("");
  const [reviewCommits, setReviewCommits] = useState("5");

  // Coordinator state
  const [sendDigestMail, setSendDigestMail] = useState(false);

  const [localError, setLocalError] = useState<string | null>(null);

  const trimmedSession = sessionName.trim();

  const scaleMutation = useMutation({
    mutationFn: (payload: {
      targets: Record<string, number>;
      dry_run: boolean;
      force: boolean;
    }) =>
      apiFetch<SessionScaleResponse>(
        `/api/v1/sessions/${encodeURIComponent(trimmedSession)}/scale`,
        {
          method: "POST",
          body: JSON.stringify(payload),
        }
      ),
    onError: (err) => setLocalError(formatApiErrorMessage(err)),
  });

  const rebalanceMutation = useMutation({
    mutationFn: (apply: boolean) =>
      apiFetch<SessionRebalanceResponse>(
        `/api/v1/sessions/${encodeURIComponent(trimmedSession)}/rebalance`,
        {
          method: "POST",
          body: JSON.stringify({
            dry_run: !apply,
            apply,
            filter: rebalanceFilter.trim() || undefined,
            threshold: Number(rebalanceThreshold) || 0,
            check_geometry: rebalanceGeometry,
          }),
        }
      ),
    onError: (err) => setLocalError(formatApiErrorMessage(err)),
  });

  const reviewQueueMutation = useMutation({
    mutationFn: (dispatch: boolean) =>
      apiFetch<SessionReviewQueueResponse>(
        `/api/v1/sessions/${encodeURIComponent(trimmedSession)}/review-queue`,
        {
          method: "POST",
          body: JSON.stringify({
            filter: reviewFilter.trim() || undefined,
            commits: Math.max(0, Number(reviewCommits) || 5),
            dispatch,
          }),
        }
      ),
    onError: (err) => setLocalError(formatApiErrorMessage(err)),
  });

  const coordinatorMutation = useMutation({
    mutationFn: (mode: "status" | "digest" | "conflicts") => {
      const encoded = encodeURIComponent(trimmedSession);
      if (mode === "status") {
        return apiFetch<CoordinatorResponse>(
          `/api/v1/sessions/${encoded}/coordinator`
        );
      }
      if (mode === "digest") {
        return apiFetch<CoordinatorResponse>(
          `/api/v1/sessions/${encoded}/coordinator/digest`,
          {
            method: "POST",
            body: JSON.stringify({ send_digest: sendDigestMail }),
          }
        );
      }
      return apiFetch<CoordinatorResponse>(
        `/api/v1/sessions/${encoded}/coordinator/conflicts`
      );
    },
    onError: (err) => setLocalError(formatApiErrorMessage(err)),
  });

  const handleScaleSubmit = (e: FormEvent) => {
    e.preventDefault();
    setLocalError(null);
    if (!trimmedSession) {
      setLocalError("Enter a target session name first.");
      return;
    }
    const parsedTargets: Record<string, number> = {};
    for (const [key, raw] of Object.entries(scaleTargets)) {
      const trimmed = raw.trim();
      if (!trimmed) continue;
      const num = Number(trimmed);
      if (!Number.isInteger(num) || num < 0) {
        setLocalError(`Target count for ${key} must be a non-negative integer.`);
        return;
      }
      parsedTargets[key] = num;
    }
    if (Object.keys(parsedTargets).length === 0) {
      setLocalError("Specify at least one harness target count to scale.");
      return;
    }
    scaleMutation.mutate({
      targets: parsedTargets,
      dry_run: scaleDryRun,
      force: scaleForce,
    });
  };

  return (
    <section
      aria-label="Swarm Scale, Rebalance, Review Queue & Coordinator"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-6"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Swarm Scale, Rebalance, Review Queue &amp; Coordinator
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Scale 12 agent harnesses to exact target counts, rebalance active
            Beads across idle panes, dispatch peer code reviews, and inspect the
            session coordinator.
          </p>
        </div>

        <div className="w-full sm:w-64">
          <label htmlFor="swarm-ops-session" className="sr-only">
            Target Swarm Session
          </label>
          <input
            id="swarm-ops-session"
            aria-label="Target Swarm Session"
            value={sessionName}
            onChange={(e) => setSessionName(e.target.value)}
            placeholder="Target session (e.g. myproject)..."
            className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
          />
        </div>
      </div>

      {localError && (
        <div
          role="alert"
          className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
        >
          {localError}
        </div>
      )}

      <div className="grid grid-cols-1 xl:grid-cols-2 gap-5">
        {/* 1. 12-Harness Session Scaler */}
        <form
          onSubmit={handleScaleSubmit}
          className="rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              12-Harness Session Scaler
            </h3>
            <span className="text-[11px] font-mono text-gray-500 dark:text-gray-400">
              POST /api/v1/sessions/&#123;id&#125;/scale
            </span>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
            {SCALE_HARNESSES.map((h) => (
              <label key={h.key} className="block text-xs">
                <span className="text-gray-600 dark:text-gray-400">
                  {h.label}
                </span>
                <input
                  type="number"
                  min={0}
                  max={64}
                  aria-label={`Scale target ${h.key}`}
                  value={scaleTargets[h.key] ?? ""}
                  onChange={(e) =>
                    setScaleTargets((prev) => ({
                      ...prev,
                      [h.key]: e.target.value,
                    }))
                  }
                  placeholder="—"
                  className="mt-0.5 w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
                />
              </label>
            ))}
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
            <div className="flex flex-wrap items-center gap-3 text-xs text-gray-700 dark:text-gray-300">
              <label className="inline-flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={scaleDryRun}
                  onChange={(e) => setScaleDryRun(e.target.checked)}
                />
                Dry-Run Preview
              </label>
              <label className="inline-flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={scaleForce}
                  onChange={(e) => setScaleForce(e.target.checked)}
                />
                Force Scale-Down
              </label>
            </div>

            <button
              type="submit"
              disabled={scaleMutation.isPending}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {scaleMutation.isPending
                ? "Scaling…"
                : scaleDryRun
                  ? "Preview Scale Plan"
                  : "Execute Scale"}
            </button>
          </div>

          {scaleMutation.data && (
            <div
              data-testid="scale-plan-result"
              className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-3 text-xs space-y-1.5"
            >
              <div className="font-semibold text-gray-900 dark:text-white">
                {scaleMutation.data.dry_run ? "Dry-Run Plan" : "Scale Result"}{" "}
                for{" "}
                <span className="font-mono">{scaleMutation.data.session}</span>{" "}
                ({scaleMutation.data.actions?.length ?? 0} action
                {(scaleMutation.data.actions?.length ?? 0) === 1 ? "" : "s"})
              </div>
              {(scaleMutation.data.actions ?? []).map((act, idx) => (
                <div
                  key={`${act.type}-${act.agent_type}-${idx}`}
                  className="font-mono text-gray-700 dark:text-gray-300"
                >
                  {act.type === "spawn" ? "+" : "-"} {act.type} {act.count}{" "}
                  {act.agent_type}
                  {act.agents?.length ? ` (${act.agents.join(", ")})` : ""}
                </div>
              ))}
            </div>
          )}
        </form>

        {/* 2. Workload Rebalancer */}
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Workload Rebalancer
            </h3>
            <span className="text-[11px] font-mono text-gray-500 dark:text-gray-400">
              POST /api/v1/sessions/&#123;id&#125;/rebalance
            </span>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 text-xs">
            <label className="block">
              <span className="text-gray-600 dark:text-gray-400">
                Agent Filter
              </span>
              <input
                aria-label="Rebalance agent filter"
                value={rebalanceFilter}
                onChange={(e) => setRebalanceFilter(e.target.value)}
                placeholder="cc, cod, agy…"
                className="mt-0.5 w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
              />
            </label>
            <label className="block">
              <span className="text-gray-600 dark:text-gray-400">
                Min Threshold
              </span>
              <input
                type="number"
                step="0.1"
                min={0}
                aria-label="Rebalance threshold"
                value={rebalanceThreshold}
                onChange={(e) => setRebalanceThreshold(e.target.value)}
                className="mt-0.5 w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
              />
            </label>
            <label className="inline-flex items-center gap-1.5 pt-4 text-gray-700 dark:text-gray-300">
              <input
                type="checkbox"
                checked={rebalanceGeometry}
                onChange={(e) => setRebalanceGeometry(e.target.checked)}
              />
              Check Geometry
            </label>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={rebalanceMutation.isPending}
              onClick={() => {
                setLocalError(null);
                if (!trimmedSession) {
                  setLocalError("Enter a target session name first.");
                  return;
                }
                rebalanceMutation.mutate(false);
              }}
              className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              Analyze Imbalance
            </button>
            <button
              type="button"
              disabled={rebalanceMutation.isPending}
              onClick={() => {
                setLocalError(null);
                if (!trimmedSession) {
                  setLocalError("Enter a target session name first.");
                  return;
                }
                rebalanceMutation.mutate(true);
              }}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
            >
              Apply Rebalance Transfers
            </button>
          </div>

          {rebalanceMutation.data && (
            <div
              data-testid="rebalance-plan-result"
              className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-3 text-xs space-y-1.5"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="font-semibold text-gray-900 dark:text-white">
                  Score: {rebalanceMutation.data.imbalance_score.toFixed(2)} (
                  {rebalanceMutation.data.recommendation})
                </span>
                <span className="font-mono text-gray-500 dark:text-gray-400">
                  {rebalanceMutation.data.transfers?.length ?? 0} transfer(s)
                  {rebalanceMutation.data.applied ? " · APPLIED" : ""}
                </span>
              </div>
              {(rebalanceMutation.data.transfers ?? []).map((tr) => (
                <div
                  key={`${tr.bead_id}-${tr.to_pane_id}`}
                  className="font-mono text-gray-700 dark:text-gray-300"
                >
                  [{tr.bead_id}] {tr.from_target} ({tr.from_agent}) →{" "}
                  {tr.to_target} ({tr.to_agent}) · {tr.reason}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* 3. Cross-Agent Peer Review Queue */}
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Peer Review Queue
            </h3>
            <span className="text-[11px] font-mono text-gray-500 dark:text-gray-400">
              POST /api/v1/sessions/&#123;id&#125;/review-queue
            </span>
          </div>

          <div className="grid grid-cols-2 gap-2 text-xs">
            <label className="block">
              <span className="text-gray-600 dark:text-gray-400">
                Reviewer Filter
              </span>
              <input
                aria-label="Review queue filter"
                value={reviewFilter}
                onChange={(e) => setReviewFilter(e.target.value)}
                placeholder="Optional harness filter…"
                className="mt-0.5 w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
              />
            </label>
            <label className="block">
              <span className="text-gray-600 dark:text-gray-400">
                Recent Commits
              </span>
              <input
                type="number"
                min={0}
                max={50}
                aria-label="Review queue commits"
                value={reviewCommits}
                onChange={(e) => setReviewCommits(e.target.value)}
                className="mt-0.5 w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
              />
            </label>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={reviewQueueMutation.isPending}
              onClick={() => {
                setLocalError(null);
                if (!trimmedSession) {
                  setLocalError("Enter a target session name first.");
                  return;
                }
                reviewQueueMutation.mutate(false);
              }}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              Preview Review Queue
            </button>
            <button
              type="button"
              disabled={reviewQueueMutation.isPending}
              onClick={() => {
                setLocalError(null);
                if (!trimmedSession) {
                  setLocalError("Enter a target session name first.");
                  return;
                }
                reviewQueueMutation.mutate(true);
              }}
              className="rounded-md bg-amber-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-amber-700 disabled:opacity-50"
            >
              Dispatch Review Prompts
            </button>
          </div>

          {reviewQueueMutation.data && (
            <div
              data-testid="review-queue-result"
              className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-3 text-xs space-y-1.5"
            >
              <div className="font-semibold text-gray-900 dark:text-white">
                Idle Agents: {reviewQueueMutation.data.idle_agents?.length ?? 0}{" "}
                · Suggestions:{" "}
                {reviewQueueMutation.data.suggestions?.length ?? 0}
                {typeof reviewQueueMutation.data.sent === "number"
                  ? ` · Sent: ${reviewQueueMutation.data.sent}`
                  : ""}
              </div>
              {(reviewQueueMutation.data.suggestions ?? []).map((s, idx) => (
                <div
                  key={`${s.pane}-${s.source_ref || idx}`}
                  className="text-gray-700 dark:text-gray-300"
                >
                  <span className="font-mono font-medium">{s.agent}</span> (pane{" "}
                  {s.pane}) ← {s.prompt}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* 4. Session Coordinator Status, Digest & Conflicts */}
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Session Coordinator
            </h3>
            <span className="text-[11px] font-mono text-gray-500 dark:text-gray-400">
              /api/v1/sessions/&#123;id&#125;/coordinator
            </span>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={coordinatorMutation.isPending}
              onClick={() => {
                setLocalError(null);
                if (!trimmedSession) {
                  setLocalError("Enter a target session name first.");
                  return;
                }
                coordinatorMutation.mutate("status");
              }}
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-800 dark:text-gray-200 hover:bg-gray-100 dark:hover:bg-gray-700"
            >
              Coordinator Status
            </button>
            <button
              type="button"
              disabled={coordinatorMutation.isPending}
              onClick={() => {
                setLocalError(null);
                if (!trimmedSession) {
                  setLocalError("Enter a target session name first.");
                  return;
                }
                coordinatorMutation.mutate("digest");
              }}
              className="rounded-md bg-purple-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-purple-700 disabled:opacity-50"
            >
              Generate Digest
            </button>
            <button
              type="button"
              disabled={coordinatorMutation.isPending}
              onClick={() => {
                setLocalError(null);
                if (!trimmedSession) {
                  setLocalError("Enter a target session name first.");
                  return;
                }
                coordinatorMutation.mutate("conflicts");
              }}
              className="rounded-md bg-rose-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-rose-700 disabled:opacity-50"
            >
              Check Conflicts
            </button>
            <label className="inline-flex items-center gap-1.5 text-xs text-gray-600 dark:text-gray-300">
              <input
                type="checkbox"
                checked={sendDigestMail}
                onChange={(e) => setSendDigestMail(e.target.checked)}
              />
              Send via Agent Mail
            </label>
          </div>

          {coordinatorMutation.data && (
            <div
              data-testid="coordinator-ops-result"
              className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-3 text-xs space-y-1.5"
            >
              <div className="font-semibold text-gray-900 dark:text-white">
                Coordinator ({coordinatorMutation.data.action}) —{" "}
                {coordinatorMutation.data.session}
              </div>
              {coordinatorMutation.data.runtime?.summary && (
                <div className="text-gray-700 dark:text-gray-300">
                  {coordinatorMutation.data.runtime.summary}
                </div>
              )}
              {coordinatorMutation.data.digest && (
                <div className="font-mono text-gray-700 dark:text-gray-300">
                  Agents: {coordinatorMutation.data.digest.agent_count ?? 0} ·
                  Active: {coordinatorMutation.data.digest.active_count ?? 0} ·
                  Idle: {coordinatorMutation.data.digest.idle_count ?? 0} ·
                  Errors: {coordinatorMutation.data.digest.error_count ?? 0}
                </div>
              )}
              {Array.isArray(coordinatorMutation.data.conflicts) && (
                <div className="text-gray-700 dark:text-gray-300">
                  Reservation Conflicts:{" "}
                  {coordinatorMutation.data.conflicts.length}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
