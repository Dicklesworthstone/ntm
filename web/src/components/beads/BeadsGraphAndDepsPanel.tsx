"use client";

/**
 * BeadsGraphAndDepsPanel
 *
 * Surfaces BV graph-priority recommendations and an interactive bead
 * dependency linker on /beads:
 *   - GET    /api/v1/beads/priority
 *   - GET    /api/v1/beads/recipes
 *   - GET    /api/v1/beads/{id}/deps
 *   - POST   /api/v1/beads/{id}/deps (body: { blocked_by })
 *   - DELETE /api/v1/beads/{id}/deps/{depId}
 */

import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

interface PriorityRecommendation {
  issue_id?: string;
  id?: string;
  title?: string;
  current_priority?: number | string;
  suggested_priority?: number | string;
  confidence?: number;
  reasoning?: string[];
  reason?: string;
}

interface BeadsPriorityResponse extends ApiEnvelope {
  priority?: {
    recommendations?: PriorityRecommendation[];
  };
}

interface BeadDepEntry {
  id: string;
  title?: string;
  status?: string;
  dependency_type?: string;
}

interface BeadDepsListResponse extends ApiEnvelope {
  bead_id: string;
  dependencies?: BeadDepEntry[];
}

export function BeadsGraphAndDepsPanel({
  onNotice,
  onSelectBead,
}: {
  onNotice?: (next: { type: "success" | "error"; message: string }) => void;
  onSelectBead?: (beadId: string) => void;
}) {
  const queryClient = useQueryClient();

  // 1. BV Priority Audit (GET /api/v1/beads/priority)
  const [priorityRequested, setPriorityRequested] = useState(false);

  const priorityQuery = useQuery({
    queryKey: ["beads", "bv-priority"],
    queryFn: () => apiFetch<BeadsPriorityResponse>("/api/v1/beads/priority"),
    enabled: priorityRequested,
    retry: false,
  });

  // 2. Interactive Bead Dependency Editor (GET/POST/DELETE /api/v1/beads/{id}/deps)
  const [targetBeadInput, setTargetBeadInput] = useState("");
  const [inspectedBeadId, setInspectedBeadId] = useState("");
  const [blockedByInput, setBlockedByInput] = useState("");

  const depsQuery = useQuery({
    queryKey: ["beads", "deps", inspectedBeadId],
    queryFn: () =>
      apiFetch<BeadDepsListResponse>(
        `/api/v1/beads/${encodeURIComponent(inspectedBeadId)}/deps`
      ),
    enabled: Boolean(inspectedBeadId),
    retry: false,
  });

  const addDepMutation = useMutation({
    mutationFn: ({
      beadId,
      blockedBy,
    }: {
      beadId: string;
      blockedBy: string;
    }) =>
      apiFetch<ApiEnvelope>(
        `/api/v1/beads/${encodeURIComponent(beadId)}/deps`,
        {
          method: "POST",
          body: JSON.stringify({ blocked_by: blockedBy }),
        }
      ),
    onSuccess: (_res, vars) => {
      setBlockedByInput("");
      onNotice?.({
        type: "success",
        message: `Linked dependency: ${vars.beadId} blocked by ${vars.blockedBy}.`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["beads", "deps", vars.beadId],
      });
      void queryClient.invalidateQueries({ queryKey: ["beads"] });
    },
    onError: (err) => {
      onNotice?.({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const removeDepMutation = useMutation({
    mutationFn: ({ beadId, depId }: { beadId: string; depId: string }) =>
      apiFetch<ApiEnvelope>(
        `/api/v1/beads/${encodeURIComponent(beadId)}/deps/${encodeURIComponent(
          depId
        )}`,
        {
          method: "DELETE",
        }
      ),
    onSuccess: (_res, vars) => {
      onNotice?.({
        type: "success",
        message: `Removed dependency ${vars.depId} from ${vars.beadId}.`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["beads", "deps", vars.beadId],
      });
      void queryClient.invalidateQueries({ queryKey: ["beads"] });
    },
    onError: (err) => {
      onNotice?.({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const handleInspectSubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = targetBeadInput.trim();
    if (!trimmed) return;
    if (trimmed === inspectedBeadId) {
      void depsQuery.refetch();
    } else {
      setInspectedBeadId(trimmed);
    }
  };

  const handleAddDepSubmit = (e: FormEvent) => {
    e.preventDefault();
    const beadId = (inspectedBeadId || targetBeadInput).trim();
    const blockedBy = blockedByInput.trim();
    if (!beadId || !blockedBy) return;
    if (beadId !== inspectedBeadId) {
      setInspectedBeadId(beadId);
    }
    addDepMutation.mutate({ beadId, blockedBy });
  };

  const priorityRecs = priorityQuery.data?.priority?.recommendations ?? [];
  const depsList = Array.isArray(depsQuery.data?.dependencies)
    ? depsQuery.data.dependencies
    : [];

  return (
    <section
      aria-label="BV Priority Calibration & Dependency Editor"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4"
    >
      <div className="grid gap-5 lg:grid-cols-2">
        {/* Left: BV Graph Priority Recommendations */}
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-2">
            <div>
              <h2 className="text-sm font-semibold text-gray-900 dark:text-white">
                BV Graph Priority Calibration
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Detect mis-prioritized bottlenecks via <code className="font-mono">/api/v1/beads/priority</code>.
              </p>
            </div>
            <button
              type="button"
              disabled={priorityQuery.isFetching}
              onClick={() => {
                if (priorityRequested) {
                  void priorityQuery.refetch();
                } else {
                  setPriorityRequested(true);
                }
              }}
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {priorityQuery.isFetching
                ? "Analyzing…"
                : priorityRequested
                  ? "Refresh Priority Audit"
                  : "Audit Graph Priorities"}
            </button>
          </div>

          {priorityQuery.isError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {formatApiErrorMessage(priorityQuery.error)}
            </div>
          )}

          {priorityRequested && !priorityQuery.isFetching && (
            <div className="space-y-2 text-xs">
              {priorityRecs.length === 0 ? (
                <p className="text-gray-500 dark:text-gray-400">
                  All bead priorities are aligned with dependency graph impact.
                </p>
              ) : (
                priorityRecs.slice(0, 6).map((rec, idx) => {
                  const id = rec.issue_id || rec.id || `rec-${idx}`;
                  return (
                    <div
                      key={id}
                      className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-2.5 flex items-start justify-between gap-2"
                    >
                      <div className="space-y-0.5">
                        <button
                          type="button"
                          onClick={() => onSelectBead?.(id)}
                          className="font-mono font-semibold text-blue-600 dark:text-blue-400 hover:underline text-left"
                        >
                          {id}
                        </button>
                        {rec.title && (
                          <div className="text-gray-800 dark:text-gray-200">
                            {rec.title}
                          </div>
                        )}
                      </div>
                      <span className="font-mono text-[11px] rounded bg-amber-100 dark:bg-amber-900/30 px-2 py-0.5 text-amber-800 dark:text-amber-300 shrink-0">
                        P{String(rec.current_priority ?? "?")} → P
                        {String(rec.suggested_priority ?? "?")}
                      </span>
                    </div>
                  );
                })
              )}
            </div>
          )}
        </div>

        {/* Right: Interactive Dependency Linker */}
        <div className="space-y-3">
          <div>
            <h2 className="text-sm font-semibold text-gray-900 dark:text-white">
              Bead Dependency Linker
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Inspect, link, or unlink blocker edges (<code className="font-mono">/api/v1/beads/&#123;id&#125;/deps</code>).
            </p>
          </div>

          <form onSubmit={handleInspectSubmit} className="flex gap-2">
            <input
              aria-label="Target Bead ID"
              value={targetBeadInput}
              onChange={(e) => setTargetBeadInput(e.target.value)}
              placeholder="Bead ID (e.g. bd-n5y0l.2)"
              className="flex-1 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <button
              type="submit"
              disabled={!targetBeadInput.trim() || depsQuery.isFetching}
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              Inspect Deps
            </button>
          </form>

          <form onSubmit={handleAddDepSubmit} className="flex gap-2">
            <input
              aria-label="Blocked By Bead ID"
              value={blockedByInput}
              onChange={(e) => setBlockedByInput(e.target.value)}
              placeholder="Blocker ID (e.g. bd-n5y0l.1)"
              className="flex-1 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <button
              type="submit"
              disabled={
                !(inspectedBeadId || targetBeadInput.trim()) ||
                !blockedByInput.trim() ||
                addDepMutation.isPending
              }
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              Add Blocker
            </button>
          </form>

          {depsQuery.isError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {formatApiErrorMessage(depsQuery.error)}
            </div>
          )}

          {inspectedBeadId && !depsQuery.isFetching && !depsQuery.isError && (
            <div
              data-testid="bead-deps-list"
              className="space-y-1.5 pt-1 text-xs"
            >
              {depsList.length === 0 ? (
                <p className="text-gray-500 dark:text-gray-400">
                  No blocking dependencies on{" "}
                  <code className="font-mono">{inspectedBeadId}</code>.
                </p>
              ) : (
                depsList.map((dep) => (
                  <div
                    key={dep.id}
                    className="flex items-center justify-between gap-2 rounded border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 px-2.5 py-1.5"
                  >
                    <span className="font-mono text-gray-900 dark:text-white">
                      {dep.id}
                      {dep.title ? ` — ${dep.title}` : ""}
                    </span>
                    <button
                      type="button"
                      disabled={removeDepMutation.isPending}
                      onClick={() =>
                        removeDepMutation.mutate({
                          beadId: inspectedBeadId,
                          depId: dep.id,
                        })
                      }
                      className="rounded border border-red-200 dark:border-red-800 px-2 py-0.5 text-[11px] text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
                    >
                      Unlink
                    </button>
                  </div>
                ))
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
