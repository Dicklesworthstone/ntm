"use client";

/**
 * Beads Page
 *
 * Issue tracking dashboard.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { apiRequestSignal, getAuthHeaders, getBaseUrl } from "@/lib/api/client";

type BeadStatus = "open" | "in_progress" | "closed";

/** A summary total that doubles as a filter over the matching beads. */
type BeadFilter = "open" | "in_progress" | "blocked" | "ready";

interface Bead {
  id: string;
  title: string;
  status: BeadStatus;
  priority?: number;
  issue_type?: string;
  labels?: string[];
  assignee?: string;
  updated_at?: string;
  dependency_count?: number;
  dependent_count?: number;
}

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface BeadsListResponse extends ApiEnvelope {
  beads: Bead[];
  count: number;
}

interface BeadLink {
  id: string;
  title?: string;
  status?: string;
  dependency_type?: string;
}

/** `br show --json` output for one bead. */
interface BeadDetail extends Bead {
  description?: string;
  acceptance_criteria?: string;
  notes?: string;
  design?: string;
  created_at?: string;
  dependencies?: BeadLink[];
  dependents?: BeadLink[];
}

interface BeadDetailResponse extends ApiEnvelope {
  bead: BeadDetail;
}

interface BeadsStatsResponse extends ApiEnvelope {
  stats: {
    summary: {
      total_issues: number;
      open_issues: number;
      in_progress_issues: number;
      closed_issues: number;
      blocked_issues: number;
      ready_issues: number;
    };
  };
}

interface Recommendation {
  id: string;
  title: string;
  priority: number;
  score: number;
  action: string;
  reasons: string[];
  unblocks_count: number;
  unblocks_ids?: string[];
  blocked_by_ids?: string[];
  is_actionable: boolean;
  estimated_size: "small" | "medium" | "large";
  tags?: string[];
}

interface TriageResponse extends ApiEnvelope {
  recommendations: Recommendation[];
  count: number;
}

interface NodeScore {
  ID: string;
  Value: number;
}

interface Cycle {
  nodes: string[];
}

interface InsightsResponse extends ApiEnvelope {
  insights: {
    Bottlenecks?: NodeScore[];
    Keystones?: NodeScore[];
    Hubs?: NodeScore[];
    Authorities?: NodeScore[];
    Cycles?: Cycle[];
  };
}

type Notice = { type: "success" | "error"; message: string };

const COLUMN_DEFS: { key: BeadStatus; label: string; helper: string }[] = [
  { key: "open", label: "Open", helper: "Ready or blocked work" },
  { key: "in_progress", label: "In Progress", helper: "Actively owned" },
  { key: "closed", label: "Closed", helper: "Completed or archived" },
];

const FILTER_DEFS: { key: BeadFilter; label: string; helper: string }[] = [
  { key: "open", label: "Open", helper: "Total open beads" },
  { key: "in_progress", label: "In Progress", helper: "Active work" },
  { key: "blocked", label: "Blocked", helper: "Needs unblocking" },
  { key: "ready", label: "Ready", helper: "Actionable now" },
];

const ASSIGNEE_STORAGE_KEY = "ntm-beads-assignee";

async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const baseUrl = getBaseUrl();
  const headers: HeadersInit = {
    "Content-Type": "application/json",
    ...getAuthHeaders(),
    ...(options.headers || {}),
  };

  const response = await fetch(`${baseUrl}${path}`, {
    ...options,
    signal: options.signal ?? apiRequestSignal(),
    headers,
  });

  const raw = await response.text();
  let data: unknown = null;
  if (raw) {
    try {
      data = JSON.parse(raw);
    } catch {
      throw new Error("Invalid response from server.");
    }
  }

  const envelope = data as ApiEnvelope | null;
  if (!response.ok || !envelope?.success) {
    const message =
      envelope?.error || `Request failed (${response.status})`;
    throw new Error(message);
  }

  return data as T;
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "Unexpected error";
}

export default function BeadsPage() {
  const queryClient = useQueryClient();
  const [assignee, setAssignee] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);
  const [dragOver, setDragOver] = useState<BeadStatus | null>(null);
  const [movingBeadId, setMovingBeadId] = useState<string | null>(null);
  const [activeFilter, setActiveFilter] = useState<BeadFilter | null>(null);
  const [selectedBeadId, setSelectedBeadId] = useState<string | null>(null);
  const noticeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const stored = localStorage.getItem(ASSIGNEE_STORAGE_KEY);
    if (stored) setAssignee(stored);
  }, []);

  useEffect(() => {
    if (typeof window === "undefined") return;
    if (!assignee) {
      localStorage.removeItem(ASSIGNEE_STORAGE_KEY);
      return;
    }
    localStorage.setItem(ASSIGNEE_STORAGE_KEY, assignee);
  }, [assignee]);

  const statsQuery = useQuery({
    queryKey: ["beads", "stats"],
    queryFn: () => apiFetch<BeadsStatsResponse>("/api/v1/beads/stats"),
    refetchInterval: 60000,
  });

  const openQuery = useQuery({
    queryKey: ["beads", "list", "open"],
    queryFn: () => apiFetch<BeadsListResponse>("/api/v1/beads?status=open"),
    refetchInterval: 30000,
  });

  const inProgressQuery = useQuery({
    queryKey: ["beads", "list", "in_progress"],
    queryFn: () => apiFetch<BeadsListResponse>("/api/v1/beads?status=in_progress"),
    refetchInterval: 30000,
  });

  const closedQuery = useQuery({
    queryKey: ["beads", "list", "closed"],
    queryFn: () => apiFetch<BeadsListResponse>("/api/v1/beads?status=closed"),
    refetchInterval: 60000,
  });

  const readyQuery = useQuery({
    queryKey: ["beads", "ready"],
    queryFn: () => apiFetch<BeadsListResponse>("/api/v1/beads/ready"),
    refetchInterval: 60000,
  });

  const blockedQuery = useQuery({
    queryKey: ["beads", "blocked"],
    queryFn: () => apiFetch<BeadsListResponse>("/api/v1/beads/blocked"),
    refetchInterval: 60000,
  });

  const detailQuery = useQuery({
    queryKey: ["beads", "detail", selectedBeadId],
    queryFn: () =>
      apiFetch<BeadDetailResponse>(
        `/api/v1/beads/${encodeURIComponent(selectedBeadId ?? "")}`
      ),
    enabled: selectedBeadId !== null,
  });

  const triageQuery = useQuery({
    queryKey: ["beads", "triage"],
    queryFn: () => apiFetch<TriageResponse>("/api/v1/beads/triage?limit=10"),
    refetchInterval: 120000,
  });

  const insightsQuery = useQuery({
    queryKey: ["beads", "insights"],
    queryFn: () => apiFetch<InsightsResponse>("/api/v1/beads/insights"),
    refetchInterval: 120000,
  });

  const readyIds = useMemo(
    () => new Set((readyQuery.data?.beads || []).map((bead) => bead.id)),
    [readyQuery.data]
  );

  const blockedIds = useMemo(
    () => new Set((blockedQuery.data?.beads || []).map((bead) => bead.id)),
    [blockedQuery.data]
  );

  const kanbanColumns = useMemo(() => {
    return {
      open: openQuery.data?.beads || [],
      in_progress: inProgressQuery.data?.beads || [],
      closed: closedQuery.data?.beads || [],
    } as Record<BeadStatus, Bead[]>;
  }, [openQuery.data, inProgressQuery.data, closedQuery.data]);

  const filterQueries: Record<BeadFilter, typeof openQuery> = {
    open: openQuery,
    in_progress: inProgressQuery,
    blocked: blockedQuery,
    ready: readyQuery,
  };
  const filterQuery = activeFilter ? filterQueries[activeFilter] : null;
  const filteredBeads = filterQuery?.data?.beads || [];
  const filterValues: Record<BeadFilter, number | undefined> = {
    open: statsQuery.data?.stats?.summary?.open_issues,
    in_progress: statsQuery.data?.stats?.summary?.in_progress_issues,
    blocked: statsQuery.data?.stats?.summary?.blocked_issues,
    ready: statsQuery.data?.stats?.summary?.ready_issues,
  };

  useEffect(() => {
    return () => {
      if (noticeTimeoutRef.current !== null) {
        clearTimeout(noticeTimeoutRef.current);
      }
    };
  }, []);

  const setStatusNotice = useCallback((next: Notice) => {
    if (noticeTimeoutRef.current !== null) {
      clearTimeout(noticeTimeoutRef.current);
    }
    setNotice(next);
    noticeTimeoutRef.current = setTimeout(() => {
      setNotice(null);
      noticeTimeoutRef.current = null;
    }, 5000);
  }, []);

  const moveBead = useCallback(
    async (bead: Bead, targetStatus: BeadStatus) => {
      if (bead.status === targetStatus) return;

      const allowedMoves: Record<BeadStatus, BeadStatus[]> = {
        open: ["in_progress", "closed"],
        in_progress: ["closed"],
        closed: [],
      };

      if (!allowedMoves[bead.status].includes(targetStatus)) {
        setStatusNotice({
          type: "error",
          message: `Cannot move ${bead.id} from ${bead.status} to ${targetStatus}.`,
        });
        return;
      }

      try {
        setMovingBeadId(bead.id);

        if (targetStatus === "in_progress") {
          const resolvedAssignee = assignee || bead.assignee;
          if (!resolvedAssignee) {
            setStatusNotice({
              type: "error",
              message: "Set an assignee before moving work to In Progress.",
            });
            return;
          }

          await apiFetch(`/api/v1/beads/${bead.id}/claim`, {
            method: "POST",
            body: JSON.stringify({ assignee: resolvedAssignee }),
          });
        }

        if (targetStatus === "closed") {
          await apiFetch(`/api/v1/beads/${bead.id}/close`, {
            method: "POST",
          });
        }

        if (process.env.NODE_ENV === "development") {
          console.log("[Beads] status change", {
            id: bead.id,
            from: bead.status,
            to: targetStatus,
          });
        }

        await queryClient.invalidateQueries({ queryKey: ["beads"] });

        setStatusNotice({
          type: "success",
          message: `Updated ${bead.id} to ${targetStatus.replace("_", " ")}.`,
        });
      } catch (error) {
        setStatusNotice({
          type: "error",
          message: getErrorMessage(error),
        });
      } finally {
        setMovingBeadId(null);
      }
    },
    [assignee, queryClient, setStatusNotice]
  );

  const triageRecs = triageQuery.data?.recommendations || [];
  const quickWins = triageRecs
    .filter((rec) => rec.is_actionable && rec.estimated_size === "small")
    .slice(0, 3);

  return (
    <div className="space-y-8">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Beads
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Kanban, triage signals, and dependency insights.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <label className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Assignee
          </label>
          <input
            value={assignee}
            onChange={(event) => setAssignee(event.target.value)}
            placeholder="Type a name"
            className="w-44 rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white shadow-sm focus:border-blue-500 focus:outline-none"
          />
        </div>
      </div>

      {notice && (
        <div
          className={`rounded-md border px-4 py-3 text-sm ${
            notice.type === "success"
              ? "border-green-200 bg-green-50 text-green-800 dark:border-green-700 dark:bg-green-900/20 dark:text-green-300"
              : "border-red-200 bg-red-50 text-red-800 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300"
          }`}
        >
          {notice.message}
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {FILTER_DEFS.map((def) => (
          <StatCard
            key={def.key}
            title={def.label}
            value={filterValues[def.key] ?? 0}
            helper={def.helper}
            selected={activeFilter === def.key}
            onClick={() =>
              setActiveFilter((current) => (current === def.key ? null : def.key))
            }
          />
        ))}
      </div>

      <div className="grid gap-6 lg:grid-cols-3">
        <section className="lg:col-span-2 space-y-4">
          {activeFilter && filterQuery ? (
            <FilteredBeadList
              label={FILTER_DEFS.find((def) => def.key === activeFilter)?.label ?? ""}
              beads={filteredBeads}
              isLoading={filterQuery.isLoading}
              error={filterQuery.error}
              blockedIds={blockedIds}
              readyIds={readyIds}
              onOpen={setSelectedBeadId}
              onBack={() => setActiveFilter(null)}
            />
          ) : (
          <>
          <div className="flex flex-col gap-1">
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Kanban Board
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Drag cards between columns to update status; click a card for details.
            </p>
          </div>

          {(openQuery.error || inProgressQuery.error || closedQuery.error) && (
            <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
              {getErrorMessage(
                openQuery.error || inProgressQuery.error || closedQuery.error
              )}
            </div>
          )}

          <div className="grid gap-4 md:grid-cols-3">
            {COLUMN_DEFS.map((column) => {
              const beads = kanbanColumns[column.key] || [];
              const isDragOver = dragOver === column.key;

              return (
                <div
                  key={column.key}
                  onDragOver={(event) => {
                    event.preventDefault();
                    setDragOver(column.key);
                  }}
                  onDragEnter={(event) => {
                    event.preventDefault();
                    setDragOver(column.key);
                  }}
                  onDragLeave={() => setDragOver(null)}
                  onDrop={(event) => {
                    event.preventDefault();
                    setDragOver(null);
                    const payload = event.dataTransfer.getData("application/ntm-bead");
                    if (!payload) return;
                    let parsed: { id?: string };
                    try {
                      parsed = JSON.parse(payload) as { id?: string };
                    } catch {
                      setStatusNotice({
                        type: "error",
                        message: "Invalid bead drag payload.",
                      });
                      return;
                    }

                    if (!parsed.id) {
                      setStatusNotice({
                        type: "error",
                        message: "Missing bead id in drag payload.",
                      });
                      return;
                    }

                    const source =
                      beads.find((item) => item.id === parsed.id) ||
                      openQuery.data?.beads?.find((item) => item.id === parsed.id) ||
                      inProgressQuery.data?.beads?.find((item) => item.id === parsed.id) ||
                      closedQuery.data?.beads?.find((item) => item.id === parsed.id);

                    if (!source) return;
                    moveBead(source, column.key);
                  }}
                  className={`rounded-lg border p-3 transition-colors ${
                    isDragOver
                      ? "border-blue-400 bg-blue-50/60 dark:border-blue-500 dark:bg-blue-900/10"
                      : "border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800"
                  }`}
                >
                  <div className="mb-3">
                    <div className="flex items-center justify-between">
                      <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                        {column.label}
                      </h3>
                      <span className="text-xs text-gray-500 dark:text-gray-400">
                        {beads.length}
                      </span>
                    </div>
                    <p className="text-xs text-gray-400 dark:text-gray-500">
                      {column.helper}
                    </p>
                  </div>

                  <div className="space-y-2">
                    {openQuery.isLoading || inProgressQuery.isLoading || closedQuery.isLoading ? (
                      <div className="flex h-24 items-center justify-center text-xs text-gray-400">
                        Loading beads...
                      </div>
                    ) : beads.length === 0 ? (
                      <div className="flex h-24 items-center justify-center text-xs text-gray-400">
                        No items
                      </div>
                    ) : (
                      beads.map((bead) => (
                        <BeadCard
                          key={bead.id}
                          bead={bead}
                          blocked={blockedIds.has(bead.id)}
                          ready={readyIds.has(bead.id)}
                          isMoving={movingBeadId === bead.id}
                          onOpen={() => setSelectedBeadId(bead.id)}
                        />
                      ))
                    )}
                  </div>
                </div>
              );
            })}
          </div>
          </>
          )}
        </section>

        <section className="space-y-4">
          <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Triage Signals
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Recommendations from bv triage.
            </p>

            {triageQuery.isLoading && (
              <div className="py-6 text-center text-sm text-gray-400">
                Loading triage...
              </div>
            )}

            {triageQuery.error && (
              <div className="mt-3 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
                {getErrorMessage(triageQuery.error)}
              </div>
            )}

            {!triageQuery.isLoading && triageRecs.length > 0 && (
              <div className="mt-4 space-y-3">
                {triageRecs.slice(0, 5).map((rec) => (
                  <div
                    key={rec.id}
                    className="rounded-md border border-gray-200 bg-gray-50 px-3 py-2 text-sm text-gray-700 dark:border-gray-700 dark:bg-gray-900/40 dark:text-gray-200"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="text-xs font-semibold text-gray-500">
                          {rec.id}
                        </span>
                        <span className="text-xs rounded-full bg-gray-200 px-2 py-0.5 text-gray-600 dark:bg-gray-700 dark:text-gray-300">
                          P{rec.priority}
                        </span>
                        {rec.is_actionable ? (
                          <span className="text-xs rounded-full bg-green-100 px-2 py-0.5 text-green-700 dark:bg-green-900/30 dark:text-green-300">
                            Actionable
                          </span>
                        ) : (
                          <span className="text-xs rounded-full bg-yellow-100 px-2 py-0.5 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300">
                            Blocked
                          </span>
                        )}
                      </div>
                      <span className="text-xs text-gray-400">
                        Score {rec.score.toFixed(2)}
                      </span>
                    </div>
                    <div className="mt-2 font-medium text-gray-900 dark:text-white">
                      {rec.title}
                    </div>
                    <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {rec.action}
                    </div>
                    {rec.reasons?.length > 0 && (
                      <div className="mt-2 text-xs text-gray-500 dark:text-gray-400">
                        {rec.reasons.slice(0, 2).join(" | ")}
                      </div>
                    )}
                    <div className="mt-2 flex flex-wrap gap-1">
                      <span className="rounded bg-gray-200 px-2 py-0.5 text-xs text-gray-600 dark:bg-gray-700 dark:text-gray-300">
                        {rec.estimated_size}
                      </span>
                      <span className="rounded bg-gray-200 px-2 py-0.5 text-xs text-gray-600 dark:bg-gray-700 dark:text-gray-300">
                        Unblocks {rec.unblocks_count}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            )}

            {!triageQuery.isLoading && triageRecs.length === 0 && (
              <div className="mt-4 text-sm text-gray-400">
                No triage recommendations available.
              </div>
            )}
          </div>

          <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
            <h3 className="text-base font-semibold text-gray-900 dark:text-white">
              Quick Wins
            </h3>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Small, actionable work from triage.
            </p>

            {triageQuery.isLoading && (
              <div className="py-4 text-center text-sm text-gray-400">
                Loading quick wins...
              </div>
            )}

            {!triageQuery.isLoading && quickWins.length === 0 && (
              <div className="mt-4 text-sm text-gray-400">
                No quick wins surfaced.
              </div>
            )}

            {!triageQuery.isLoading && quickWins.length > 0 && (
              <div className="mt-4 space-y-2">
                {quickWins.map((rec) => (
                  <div
                    key={rec.id}
                    className="rounded-md border border-gray-200 bg-gray-50 px-3 py-2 text-sm text-gray-700 dark:border-gray-700 dark:bg-gray-900/40 dark:text-gray-200"
                  >
                    <div className="flex items-center justify-between">
                      <span className="text-xs font-semibold text-gray-500">
                        {rec.id}
                      </span>
                      <span className="text-xs text-gray-400">
                        P{rec.priority}
                      </span>
                    </div>
                    <div className="mt-1 font-medium text-gray-900 dark:text-white">
                      {rec.title}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </section>
      </div>

      <GalaxyView
        insights={insightsQuery.data?.insights}
        isLoading={insightsQuery.isLoading}
        error={insightsQuery.error}
      />

      <BeadDetailDialog
        beadId={selectedBeadId}
        bead={detailQuery.data?.bead}
        isLoading={detailQuery.isLoading}
        error={detailQuery.error}
        onClose={() => setSelectedBeadId(null)}
      />
    </div>
  );
}

function StatCard({
  title,
  value,
  helper,
  selected,
  onClick,
}: {
  title: string;
  value: number;
  helper: string;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={selected}
      title={selected ? "Back to the Kanban board" : `Show ${title.toLowerCase()} beads`}
      className={`rounded-lg border bg-white p-4 text-left transition hover:border-blue-400 focus-visible:outline-2 focus-visible:outline-blue-500 dark:bg-gray-800 ${
        selected
          ? "border-blue-500 ring-2 ring-blue-200 dark:ring-blue-900"
          : "border-gray-200 dark:border-gray-700"
      }`}
    >
      <span className="block text-xs uppercase tracking-wide text-gray-500 dark:text-gray-400">
        {title}
      </span>
      <span className="mt-2 block text-2xl font-semibold text-gray-900 dark:text-white">
        {value}
      </span>
      <span className="mt-1 block text-xs text-gray-400 dark:text-gray-500">{helper}</span>
    </button>
  );
}

function FilteredBeadList({
  label,
  beads,
  isLoading,
  error,
  blockedIds,
  readyIds,
  onOpen,
  onBack,
}: {
  label: string;
  beads: Bead[];
  isLoading: boolean;
  error: unknown;
  blockedIds: Set<string>;
  readyIds: Set<string>;
  onOpen: (id: string) => void;
  onBack: () => void;
}) {
  return (
    <>
      <div className="flex items-center justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            {label} Beads
          </h2>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            {beads.length} bead{beads.length !== 1 ? "s" : ""} listed.
          </p>
        </div>
        <button
          type="button"
          onClick={onBack}
          className="text-sm text-blue-600 hover:underline dark:text-blue-400"
        >
          Back to Kanban
        </button>
      </div>
      {error ? (
        <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
          {getErrorMessage(error)}
        </div>
      ) : null}
      {isLoading ? (
        <div className="py-12 text-center text-sm text-gray-400">Loading beads...</div>
      ) : beads.length === 0 && !error ? (
        <div className="py-12 text-center text-sm text-gray-400">No beads in this view.</div>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {beads.map((bead) => (
            <BeadCard
              key={bead.id}
              bead={bead}
              blocked={blockedIds.has(bead.id)}
              ready={readyIds.has(bead.id)}
              isMoving={false}
              draggable={false}
              onOpen={() => onOpen(bead.id)}
            />
          ))}
        </div>
      )}
    </>
  );
}

function BeadDetailDialog({
  beadId,
  bead,
  isLoading,
  error,
  onClose,
}: {
  beadId: string | null;
  bead: BeadDetail | undefined;
  isLoading: boolean;
  error: unknown;
  onClose: () => void;
}) {
  const dialogRef = useRef<HTMLDialogElement | null>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (beadId && !dialog.open) dialog.showModal();
    if (!beadId && dialog.open) dialog.close();
  }, [beadId]);

  const sections: { heading: string; body?: string }[] = [
    { heading: "Description", body: bead?.description },
    { heading: "Acceptance Criteria", body: bead?.acceptance_criteria },
    { heading: "Design", body: bead?.design },
    { heading: "Notes", body: bead?.notes },
  ];
  const links: { heading: string; items?: BeadLink[] }[] = [
    { heading: "Depends on", items: bead?.dependencies },
    { heading: "Blocks", items: bead?.dependents },
  ];

  return (
    <dialog
      ref={dialogRef}
      onClose={onClose}
      aria-labelledby="bead-detail-title"
      className="m-auto max-h-[85vh] w-[min(92vw,48rem)] overflow-y-auto rounded-xl border border-gray-200 bg-white p-0 text-gray-900 shadow-2xl backdrop:bg-gray-950/60 dark:border-gray-700 dark:bg-gray-900 dark:text-white"
    >
      <div className="sticky top-0 flex items-start justify-between gap-4 border-b border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-900">
        <div className="min-w-0">
          <div className="text-xs font-semibold text-gray-500 dark:text-gray-400">{beadId}</div>
          <h2 id="bead-detail-title" className="mt-1 break-words text-lg font-semibold">
            {bead?.title || "Bead details"}
          </h2>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close bead details"
          className="rounded-md px-2 py-1 text-xl leading-none text-gray-500 hover:bg-gray-100 dark:hover:bg-gray-800"
        >
          ×
        </button>
      </div>
      <div className="space-y-5 p-5 text-sm">
        {isLoading && <p className="text-gray-500">Loading bead details...</p>}
        {error ? (
          <p className="text-red-700 dark:text-red-400">{getErrorMessage(error)}</p>
        ) : null}
        {bead && (
          <>
            <div className="flex flex-wrap gap-2 text-xs">
              {[
                bead.status?.replace("_", " "),
                typeof bead.priority === "number" ? `P${bead.priority}` : undefined,
                bead.issue_type,
                bead.assignee ? `Assigned to ${bead.assignee}` : undefined,
              ]
                .filter(Boolean)
                .map((chip) => (
                  <span key={chip} className="rounded bg-gray-100 px-2 py-1 dark:bg-gray-800">
                    {chip}
                  </span>
                ))}
            </div>
            {sections
              .filter((section) => section.body && section.body.trim() !== "")
              .map((section) => (
                <section key={section.heading}>
                  <h3 className="mb-2 font-semibold">{section.heading}</h3>
                  <div className="whitespace-pre-wrap break-words text-gray-700 dark:text-gray-300">
                    {section.body}
                  </div>
                </section>
              ))}
            {links
              .filter((link) => link.items && link.items.length > 0)
              .map((link) => (
                <section key={link.heading}>
                  <h3 className="mb-2 font-semibold">{link.heading}</h3>
                  <ul className="space-y-2">
                    {link.items!.map((item) => (
                      <li
                        key={item.id}
                        className="rounded border border-gray-200 p-2 dark:border-gray-700"
                      >
                        <span className="font-medium">{item.id}</span>
                        {item.title && <span> · {item.title}</span>}
                        {item.status && (
                          <span className="ml-2 text-xs text-gray-500 dark:text-gray-400">
                            {item.status.replace("_", " ")}
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
          </>
        )}
      </div>
    </dialog>
  );
}

function BeadCard({
  bead,
  blocked,
  ready,
  isMoving,
  onOpen,
  draggable = true,
}: {
  bead: Bead;
  blocked: boolean;
  ready: boolean;
  isMoving: boolean;
  onOpen: () => void;
  draggable?: boolean;
}) {
  const priorityLabel =
    typeof bead.priority === "number" ? `P${bead.priority}` : "P?";

  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`Open ${bead.id}: ${bead.title}`}
      onClick={onOpen}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onOpen();
        }
      }}
      draggable={draggable}
      onDragStart={(event) => {
        event.dataTransfer.setData(
          "application/ntm-bead",
          JSON.stringify({ id: bead.id })
        );
        event.dataTransfer.effectAllowed = "move";
      }}
      className={`cursor-pointer rounded-md border border-gray-200 bg-white p-3 text-sm text-gray-700 shadow-sm transition hover:border-blue-300 focus-visible:outline-2 focus-visible:outline-blue-500 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200 ${
        isMoving ? "opacity-60" : ""
      }`}
    >
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold text-gray-500">{bead.id}</span>
        <span className="text-xs rounded-full bg-gray-100 px-2 py-0.5 text-gray-600 dark:bg-gray-800 dark:text-gray-300">
          {priorityLabel}
        </span>
      </div>
      <div className="mt-2 font-medium text-gray-900 dark:text-white">
        {bead.title}
      </div>
      <div className="mt-2 flex flex-wrap gap-1 text-xs text-gray-500 dark:text-gray-400">
        {bead.issue_type && (
          <span className="rounded bg-gray-100 px-2 py-0.5 dark:bg-gray-800">
            {bead.issue_type}
          </span>
        )}
        {blocked && (
          <span className="rounded bg-red-100 px-2 py-0.5 text-red-700 dark:bg-red-900/30 dark:text-red-300">
            Blocked
          </span>
        )}
        {ready && (
          <span className="rounded bg-green-100 px-2 py-0.5 text-green-700 dark:bg-green-900/30 dark:text-green-300">
            Ready
          </span>
        )}
      </div>
      {bead.labels && bead.labels.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1">
          {bead.labels.slice(0, 3).map((label) => (
            <span
              key={label}
              className="rounded bg-gray-100 px-2 py-0.5 text-xs text-gray-500 dark:bg-gray-800 dark:text-gray-400"
            >
              {label}
            </span>
          ))}
        </div>
      )}
      <div className="mt-3 flex items-center justify-between text-xs text-gray-400">
        <span>
          Deps {bead.dependency_count ?? 0} | Unblocks{" "}
          {bead.dependent_count ?? 0}
        </span>
        {bead.assignee && <span>{bead.assignee}</span>}
      </div>
    </div>
  );
}

function GalaxyView({
  insights,
  isLoading,
  error,
}: {
  insights: InsightsResponse["insights"] | undefined;
  isLoading: boolean;
  error: unknown;
}) {
  const width = 900;
  const height = 420;

  const nodes = useMemo(() => {
    if (!insights) return [];

    const kindOrder = ["bottleneck", "keystone", "hub", "authority"] as const;
    const sources: Record<
      (typeof kindOrder)[number],
      NodeScore[] | undefined
    > = {
      bottleneck: insights.Bottlenecks,
      keystone: insights.Keystones,
      hub: insights.Hubs,
      authority: insights.Authorities,
    };

    const map = new Map<
      string,
      { id: string; score: number; kind: (typeof kindOrder)[number] }
    >();

    kindOrder.forEach((kind) => {
      (sources[kind] || []).forEach((node) => {
        if (!map.has(node.ID)) {
          map.set(node.ID, { id: node.ID, score: node.Value, kind });
        }
      });
    });

    return Array.from(map.values());
  }, [insights]);

  const positioned = useMemo(
    () => layoutGalaxy(nodes, width, height),
    [nodes, width, height]
  );

  const cycles = insights?.Cycles || [];
  const errorMessage = error ? getErrorMessage(error) : null;

  return (
    <section className="rounded-lg border border-gray-200 bg-gradient-to-br from-gray-50 to-white p-4 dark:border-gray-700 dark:from-gray-900 dark:to-gray-800">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Galaxy View
          </h2>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Force layout of high-signal nodes from bv insights.
          </p>
        </div>
        <div className="text-xs text-gray-400">
          Cycles detected: {cycles.length}
        </div>
      </div>

      {isLoading && (
        <div className="py-10 text-center text-sm text-gray-400">
          Loading insights...
        </div>
      )}

      {errorMessage && (
        <div className="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
          {errorMessage}
        </div>
      )}

      {!isLoading && !errorMessage && (
        <>
          {positioned.length === 0 ? (
            <div className="py-10 text-center text-sm text-gray-400">
              No insights available.
            </div>
          ) : (
            <div className="mt-4 overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900">
              <svg
                viewBox={`0 0 ${width} ${height}`}
                className="h-80 w-full"
                role="img"
                aria-label="Beads dependency galaxy view"
              >
                <rect width={width} height={height} fill="transparent" />
                {positioned.map((node) => (
                  <g key={node.id} transform={`translate(${node.x}, ${node.y})`}>
                    <circle
                      r={node.radius}
                      fill={node.color}
                      opacity={0.85}
                    />
                    <text
                      x={node.radius + 4}
                      y={4}
                      fontSize="10"
                      fill="currentColor"
                      className="text-gray-600 dark:text-gray-300"
                    >
                      {node.id}
                    </text>
                  </g>
                ))}
              </svg>
            </div>
          )}

          <div className="mt-4 flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
            <LegendItem label="Bottleneck" color="#ef4444" />
            <LegendItem label="Keystone" color="#f97316" />
            <LegendItem label="Hub" color="#22c55e" />
            <LegendItem label="Authority" color="#3b82f6" />
          </div>

          {cycles.length > 0 && (
            <div className="mt-4 text-xs text-gray-500 dark:text-gray-400">
              Sample cycles:{" "}
              {cycles.slice(0, 2).map((cycle, index) => (
                <span key={`${cycle.nodes.join("-")}-${index}`}>
                  {cycle.nodes.join(" -> ")}
                  {index < Math.min(cycles.length, 2) - 1 ? " | " : ""}
                </span>
              ))}
            </div>
          )}
        </>
      )}
    </section>
  );
}

function LegendItem({ label, color }: { label: string; color: string }) {
  return (
    <span className="flex items-center gap-2">
      <span className="h-2 w-2 rounded-full" style={{ backgroundColor: color }} />
      <span>{label}</span>
    </span>
  );
}

function layoutGalaxy(
  nodes: { id: string; score: number; kind: "bottleneck" | "keystone" | "hub" | "authority" }[],
  width: number,
  height: number
) {
  if (nodes.length === 0) return [];

  const centerX = width / 2;
  const centerY = height / 2;
  const minDimension = Math.min(width, height);
  const radii = {
    bottleneck: minDimension * 0.2,
    keystone: minDimension * 0.3,
    hub: minDimension * 0.4,
    authority: minDimension * 0.5,
  };

  const colors: Record<string, string> = {
    bottleneck: "#ef4444",
    keystone: "#f97316",
    hub: "#22c55e",
    authority: "#3b82f6",
  };

  const scores = nodes.map((node) => node.score);
  const minScore = Math.min(...scores);
  const maxScore = Math.max(...scores);

  const positioned = nodes.map((node, index) => {
    const seed = hashString(`${node.id}-${index}`);
    const angle = (seed % 360) * (Math.PI / 180);
    const radiusJitter = (seed % 40) - 20;
    const baseRadius = radii[node.kind] + radiusJitter;
    return {
      id: node.id,
      kind: node.kind,
      score: node.score,
      x: centerX + Math.cos(angle) * baseRadius,
      y: centerY + Math.sin(angle) * baseRadius,
      radius: normalizeScore(node.score, minScore, maxScore, 4, 11),
      color: colors[node.kind],
    };
  });

  const iterations = 40;
  for (let step = 0; step < iterations; step += 1) {
    for (let i = 0; i < positioned.length; i += 1) {
      let fx = 0;
      let fy = 0;
      const node = positioned[i];
      for (let j = 0; j < positioned.length; j += 1) {
        if (i === j) continue;
        const other = positioned[j];
        const dx = node.x - other.x;
        const dy = node.y - other.y;
        const distance = Math.sqrt(dx * dx + dy * dy) || 1;
        const force = 1200 / (distance * distance);
        fx += (dx / distance) * force;
        fy += (dy / distance) * force;
      }

      const dxCenter = node.x - centerX;
      const dyCenter = node.y - centerY;
      const distanceFromCenter = Math.sqrt(dxCenter * dxCenter + dyCenter * dyCenter) || 1;
      const targetRadius = radii[node.kind];
      const pull = (distanceFromCenter - targetRadius) * 0.02;
      fx -= (dxCenter / distanceFromCenter) * pull * distanceFromCenter;
      fy -= (dyCenter / distanceFromCenter) * pull * distanceFromCenter;

      node.x += fx * 0.02;
      node.y += fy * 0.02;

      node.x = clamp(node.x, 40, width - 40);
      node.y = clamp(node.y, 40, height - 40);
    }
  }

  return positioned;
}

function normalizeScore(value: number, min: number, max: number, low: number, high: number) {
  if (max === min) return (low + high) / 2;
  const normalized = (value - min) / (max - min);
  return low + normalized * (high - low);
}

function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}

function hashString(input: string) {
  let hash = 2166136261;
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i);
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}
