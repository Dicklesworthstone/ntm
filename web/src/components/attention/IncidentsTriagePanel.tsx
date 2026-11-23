"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  apiFetch,
  formatApiErrorMessage,
  type components,
} from "@/lib/api/client";

type Incident = components["schemas"]["Incident"];
type IncidentsListResponse =
  components["schemas"]["IncidentsListEnvelopeResponse"];
type IncidentDetailResponse =
  components["schemas"]["IncidentDetailEnvelopeResponse"];
type IncidentStatusResponse =
  components["schemas"]["IncidentStatusEnvelopeResponse"];

type StatusFilter = "all" | "open" | "investigating" | "resolved" | "muted";

function incidentSeverityClass(severity?: string): string {
  switch (severity?.toLowerCase()) {
    case "critical":
    case "error":
    case "p0":
      return "bg-red-100 text-red-800 border-red-200 dark:bg-red-900/30 dark:text-red-300 dark:border-red-800/60";
    case "warning":
    case "high":
    case "p1":
      return "bg-amber-100 text-amber-800 border-amber-200 dark:bg-amber-900/30 dark:text-amber-300 dark:border-amber-800/60";
    default:
      return "bg-blue-100 text-blue-800 border-blue-200 dark:bg-blue-900/30 dark:text-blue-300 dark:border-blue-800/60";
  }
}

function incidentStatusBadgeClass(status?: string): string {
  switch (status?.toLowerCase()) {
    case "open":
      return "bg-rose-100 text-rose-800 dark:bg-rose-900/40 dark:text-rose-200";
    case "investigating":
      return "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200";
    case "resolved":
      return "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200";
    case "muted":
      return "bg-gray-200 text-gray-700 dark:bg-gray-700 dark:text-gray-300";
    default:
      return "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300";
  }
}

function formatIncidentTime(ts?: string): string {
  if (!ts) return "—";
  const parsed = new Date(ts);
  if (Number.isNaN(parsed.getTime())) return ts;
  return parsed.toLocaleString();
}

export function IncidentsTriagePanel() {
  const queryClient = useQueryClient();
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [includeResolved, setIncludeResolved] = useState<boolean>(false);
  const [expandedIncidentId, setExpandedIncidentId] = useState<string | null>(
    null
  );
  const [triageNote, setTriageNote] = useState<string>("");
  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const effectiveIncludeResolved =
    includeResolved ||
    statusFilter === "resolved" ||
    statusFilter === "muted";

  const listPath = useMemo(() => {
    const params = new URLSearchParams();
    params.set("limit", "50");
    if (effectiveIncludeResolved) {
      params.set("include_resolved", "true");
    }
    if (statusFilter !== "all") {
      params.set("status", statusFilter);
    }
    return `/api/v1/incidents?${params.toString()}`;
  }, [effectiveIncludeResolved, statusFilter]);

  const incidentsQuery = useQuery({
    queryKey: ["incidents", "list", listPath],
    queryFn: () => apiFetch<IncidentsListResponse>(listPath),
    retry: false,
    refetchInterval: 15_000,
  });

  const detailQuery = useQuery({
    queryKey: ["incidents", "detail", expandedIncidentId],
    queryFn: () =>
      apiFetch<IncidentDetailResponse>(
        `/api/v1/incidents/${encodeURIComponent(expandedIncidentId || "")}?events_limit=20`
      ),
    enabled: Boolean(expandedIncidentId),
    retry: false,
  });

  const statusMutation = useMutation({
    mutationFn: async ({
      incidentId,
      targetStatus,
      reason,
    }: {
      incidentId: string;
      targetStatus: "open" | "investigating" | "muted";
      reason?: string;
    }) => {
      return apiFetch<IncidentStatusResponse>(
        `/api/v1/incidents/${encodeURIComponent(incidentId)}/status`,
        {
          method: "POST",
          body: JSON.stringify({
            status: targetStatus,
            ...(reason ? { reason } : {}),
          }),
        }
      );
    },
    onSuccess: (res) => {
      setNotice({
        kind: "success",
        text: `Incident ${res.incident_id} moved to "${res.status}" (${res.action}).`,
      });
      void queryClient.invalidateQueries({ queryKey: ["incidents"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Incident status update failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const resolveMutation = useMutation({
    mutationFn: async ({
      incidentId,
      note,
    }: {
      incidentId: string;
      note?: string;
    }) => {
      return apiFetch<IncidentStatusResponse>(
        `/api/v1/incidents/${encodeURIComponent(incidentId)}/resolve`,
        {
          method: "POST",
          body: JSON.stringify(note ? { note } : {}),
        }
      );
    },
    onSuccess: (res) => {
      setNotice({
        kind: "success",
        text: `Resolved incident ${res.incident_id}.`,
      });
      void queryClient.invalidateQueries({ queryKey: ["incidents"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Incident resolution failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const incidents: Incident[] = incidentsQuery.data?.incidents ?? [];
  const storeUnavailable =
    incidentsQuery.isError &&
    incidentsQuery.error instanceof ApiError &&
    incidentsQuery.error.status === 503;

  const trimmedNote = triageNote.trim();

  return (
    <section
      aria-label="Operator Incidents Triage"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4"
    >
      <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Durable Operator Incidents
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Escalated recurring alerts and stuck-agent fingerprints tracked in
            the runtime state store (`/api/v1/incidents`).
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          {(() => {
            const forcedResolved =
              statusFilter === "resolved" || statusFilter === "muted";
            return (
              <label
                className={`inline-flex items-center gap-2 text-xs ${
                  forcedResolved
                    ? "text-gray-400 dark:text-gray-500 cursor-not-allowed"
                    : "text-gray-700 dark:text-gray-300"
                }`}
                title={
                  forcedResolved
                    ? "Automatically enabled when filtering by resolved or muted status"
                    : undefined
                }
              >
                <input
                  type="checkbox"
                  checked={effectiveIncludeResolved}
                  disabled={forcedResolved}
                  onChange={(e) => setIncludeResolved(e.target.checked)}
                  className="rounded border-gray-300 dark:border-gray-600 disabled:opacity-60"
                />
                Include resolved &amp; muted
              </label>
            );
          })()}

          <select
            aria-label="Incident status filter"
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
          >
            <option value="all">All statuses</option>
            <option value="open">Open</option>
            <option value="investigating">Investigating</option>
            <option value="resolved">Resolved</option>
            <option value="muted">Muted</option>
          </select>

          <input
            type="text"
            aria-label="Incident triage note"
            value={triageNote}
            onChange={(e) => setTriageNote(e.target.value)}
            placeholder="Optional resolution / mute note..."
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white w-56"
          />
        </div>
      </div>

      {notice && (
        <div
          role={notice.kind === "error" ? "alert" : "status"}
          className={`rounded-md border px-3 py-2 text-xs flex items-center justify-between gap-2 ${
            notice.kind === "error"
              ? "border-red-200 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200"
              : "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
          }`}
        >
          <span>{notice.text}</span>
          <button
            type="button"
            onClick={() => setNotice(null)}
            className="underline opacity-80 hover:opacity-100"
          >
            Dismiss
          </button>
        </div>
      )}

      {storeUnavailable ? (
        <div className="rounded-md border border-amber-200 dark:border-amber-800/60 bg-amber-50/70 dark:bg-amber-900/20 p-3 text-xs text-amber-800 dark:text-amber-200">
          Durable incident state store is not initialized on this server
          instance yet.
        </div>
      ) : incidentsQuery.isError ? (
        <div
          role="alert"
          className="rounded-md border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 p-3 text-xs text-red-800 dark:text-red-200"
        >
          Failed to load incidents: {formatApiErrorMessage(incidentsQuery.error)}
        </div>
      ) : incidentsQuery.isLoading ? (
        <div className="py-4 text-center text-xs text-gray-500 dark:text-gray-400">
          Loading durable incidents...
        </div>
      ) : incidents.length === 0 ? (
        <div className="py-4 text-center text-xs text-gray-500 dark:text-gray-400">
          No active incidents match the current filters.
        </div>
      ) : (
        <div className="space-y-3">
          {incidents.map((inc) => {
            const isExpanded = expandedIncidentId === inc.id;
            const isMutating =
              (statusMutation.isPending &&
                statusMutation.variables?.incidentId === inc.id) ||
              (resolveMutation.isPending &&
                resolveMutation.variables?.incidentId === inc.id);

            return (
              <div
                key={inc.id}
                data-testid={`incident-card-${inc.id}`}
                className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/60 dark:bg-gray-900/40 p-3.5 space-y-2.5"
              >
                <div className="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-3">
                  <div className="space-y-1 min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-xs font-semibold text-gray-800 dark:text-gray-200">
                        {inc.id}
                      </span>
                      <span
                        className={`px-2 py-0.5 text-xs font-medium rounded border ${incidentSeverityClass(
                          inc.severity
                        )}`}
                      >
                        {inc.severity}
                      </span>
                      <span
                        className={`px-2 py-0.5 text-xs font-medium rounded ${incidentStatusBadgeClass(
                          inc.status
                        )}`}
                      >
                        {inc.status}
                      </span>
                      <span className="px-2 py-0.5 text-xs rounded bg-gray-200/80 dark:bg-gray-800 text-gray-700 dark:text-gray-300 font-mono">
                        {inc.category}/{inc.family}
                      </span>
                      {inc.session_names && (
                        <span className="text-xs font-mono text-blue-600 dark:text-blue-400">
                          {inc.session_names}
                        </span>
                      )}
                    </div>

                    <p className="text-sm font-medium text-gray-900 dark:text-white">
                      {inc.title}
                    </p>

                    <div className="flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
                      <span>Events: {inc.event_count}</span>
                      <span>Alerts: {inc.alert_count}</span>
                      <span>Started: {formatIncidentTime(inc.started_at)}</span>
                      <span>
                        Last event: {formatIncidentTime(inc.last_event_at)}
                      </span>
                    </div>

                    {(inc.notes || inc.muted_reason || inc.root_cause) && (
                      <p className="text-xs text-gray-600 dark:text-gray-300">
                        {inc.notes || inc.muted_reason || inc.root_cause}
                      </p>
                    )}
                  </div>

                  <div className="flex flex-wrap items-center gap-1.5 shrink-0">
                    <button
                      type="button"
                      onClick={() =>
                        setExpandedIncidentId(isExpanded ? null : inc.id)
                      }
                      aria-label={`Inspect incident ${inc.id}`}
                      className="px-2.5 py-1 text-xs font-medium rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
                    >
                      {isExpanded ? "Hide Timeline" : "Inspect"}
                    </button>

                    {inc.status !== "investigating" &&
                      inc.status !== "resolved" && (
                        <button
                          type="button"
                          disabled={isMutating}
                          aria-label={`Investigate incident ${inc.id}`}
                          onClick={() =>
                            statusMutation.mutate({
                              incidentId: inc.id,
                              targetStatus: "investigating",
                              reason: trimmedNote || undefined,
                            })
                          }
                          className="px-2.5 py-1 text-xs font-medium rounded border border-amber-300 dark:border-amber-700 text-amber-800 dark:text-amber-200 hover:bg-amber-50 dark:hover:bg-amber-900/30 disabled:opacity-50"
                        >
                          Investigate
                        </button>
                      )}

                    {inc.status !== "resolved" && (
                      <button
                        type="button"
                        disabled={isMutating}
                        aria-label={`Resolve incident ${inc.id}`}
                        onClick={() =>
                          resolveMutation.mutate({
                            incidentId: inc.id,
                            note: trimmedNote || undefined,
                          })
                        }
                        className="px-2.5 py-1 text-xs font-medium rounded bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50"
                      >
                        Resolve
                      </button>
                    )}

                    {inc.status !== "muted" && inc.status !== "resolved" && (
                      <button
                        type="button"
                        disabled={isMutating}
                        aria-label={`Mute incident ${inc.id}`}
                        onClick={() =>
                          statusMutation.mutate({
                            incidentId: inc.id,
                            targetStatus: "muted",
                            reason: trimmedNote || undefined,
                          })
                        }
                        className="px-2.5 py-1 text-xs font-medium rounded border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700 disabled:opacity-50"
                      >
                        Mute
                      </button>
                    )}

                    {(inc.status === "resolved" || inc.status === "muted") && (
                      <button
                        type="button"
                        disabled={isMutating}
                        aria-label={`Reopen incident ${inc.id}`}
                        onClick={() =>
                          statusMutation.mutate({
                            incidentId: inc.id,
                            targetStatus: "open",
                            reason: trimmedNote || undefined,
                          })
                        }
                        className="px-2.5 py-1 text-xs font-medium rounded border border-blue-300 dark:border-blue-700 text-blue-700 dark:text-blue-300 hover:bg-blue-50 dark:hover:bg-blue-900/30 disabled:opacity-50"
                      >
                        Reopen
                      </button>
                    )}
                  </div>
                </div>

                {isExpanded && (
                  <div className="pt-2.5 border-t border-gray-200 dark:border-gray-700 space-y-2 text-xs">
                    <div className="flex flex-wrap gap-3 font-mono text-gray-500 dark:text-gray-400">
                      <span>Fingerprint: {inc.fingerprint}</span>
                      {typeof inc.first_event_cursor === "number" && (
                        <span>
                          Cursors #{inc.first_event_cursor} &rarr; #
                          {inc.last_event_cursor ?? inc.first_event_cursor}
                        </span>
                      )}
                    </div>

                    {detailQuery.isLoading ? (
                      <div className="text-gray-500 dark:text-gray-400">
                        Loading linked incident timeline...
                      </div>
                    ) : detailQuery.isError ? (
                      <div className="text-red-600 dark:text-red-400">
                        {formatApiErrorMessage(detailQuery.error)}
                      </div>
                    ) : (detailQuery.data?.events?.length ?? 0) === 0 ? (
                      <div className="text-gray-500 dark:text-gray-400">
                        No linked attention events stored in current replay
                        window.
                      </div>
                    ) : (
                      <div className="space-y-1.5">
                        <div className="font-medium text-gray-700 dark:text-gray-300">
                          Linked Attention Events (
                          {detailQuery.data?.event_count ?? 0})
                        </div>
                        {detailQuery.data?.events.map((ev) => (
                          <div
                            key={ev.cursor}
                            className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-2.5 py-1.5 flex flex-wrap items-center justify-between gap-2"
                          >
                            <div className="flex flex-wrap items-center gap-2">
                              <span className="font-mono text-gray-500">
                                #{ev.cursor}
                              </span>
                              <span className="font-mono text-[11px] px-1.5 py-0.5 rounded bg-gray-100 dark:bg-gray-700 text-gray-700 dark:text-gray-200">
                                {ev.event_type}
                              </span>
                              <span className="text-gray-900 dark:text-gray-100">
                                {ev.summary}
                              </span>
                            </div>
                            <span className="text-gray-400">
                              {formatIncidentTime(ev.ts)}
                            </span>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
