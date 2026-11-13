"use client";

/**
 * Attention Triage Inbox
 *
 * Operator-facing triage surface backed by:
 *   - GET  /api/v1/attention/digest
 *   - GET  /api/v1/attention/events
 *   - POST /api/v1/attention/items/{cursor}/state
 */

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type components,
} from "@/lib/api/client";

type AttentionDigest = components["schemas"]["AttentionDigest"];
type AttentionEventsResponse =
  components["schemas"]["AttentionEventsEnvelopeResponse"];
type AttentionEvent = components["schemas"]["AttentionEvent"];
type AttentionItemStateRequest =
  components["schemas"]["AttentionItemStateRequest"];
type AttentionItemStateResponse =
  components["schemas"]["AttentionItemStateEnvelopeResponse"];
type AttentionItemState = components["schemas"]["AttentionItemState"];

type ActionabilityFilter =
  | "all"
  | "action_required"
  | "interesting"
  | "background";

const CATEGORIES = [
  "all",
  "agent",
  "session",
  "pane",
  "bead",
  "mail",
  "alert",
  "actuation",
  "incident",
  "health",
  "system",
] as const;

function severityBadgeClass(severity?: string): string {
  switch (severity) {
    case "critical":
    case "error":
      return "bg-red-100 text-red-800 border-red-200 dark:bg-red-900/30 dark:text-red-300 dark:border-red-800/60";
    case "warning":
      return "bg-yellow-100 text-yellow-800 border-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300 dark:border-yellow-800/60";
    case "info":
      return "bg-blue-100 text-blue-800 border-blue-200 dark:bg-blue-900/30 dark:text-blue-300 dark:border-blue-800/60";
    default:
      return "bg-gray-100 text-gray-700 border-gray-200 dark:bg-gray-800 dark:text-gray-300 dark:border-gray-700";
  }
}

function actionabilityBadgeClass(actionability?: string): string {
  switch (actionability) {
    case "action_required":
      return "bg-rose-100 text-rose-800 dark:bg-rose-900/40 dark:text-rose-200";
    case "interesting":
      return "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200";
    default:
      return "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300";
  }
}

function stateBadgeClass(state?: string): string {
  switch (state) {
    case "acknowledged":
      return "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300";
    case "snoozed":
      return "bg-indigo-100 text-indigo-800 dark:bg-indigo-900/30 dark:text-indigo-300";
    case "dismissed":
      return "bg-gray-200 text-gray-600 dark:bg-gray-700 dark:text-gray-400";
    default:
      return "bg-sky-100 text-sky-800 dark:bg-sky-900/30 dark:text-sky-300";
  }
}

function formatTimestamp(ts?: string): string {
  if (!ts) return "—";
  const parsed = new Date(ts);
  if (Number.isNaN(parsed.getTime())) return ts;
  return parsed.toLocaleString();
}

function readEventState(
  event: AttentionEvent,
  overrides: Record<number, AttentionItemState>
): { state: string; pinned: boolean; snoozedUntil?: string } {
  const override = overrides[event.cursor];
  if (override) {
    return {
      state: override.state || "new",
      pinned: Boolean(override.pinned),
      snoozedUntil: override.snoozed_until,
    };
  }
  const details = (event.details ?? {}) as Record<string, unknown>;
  const detailState =
    typeof details.attention_state === "string" ? details.attention_state : "";
  const detailPinned = Boolean(details.attention_pinned);
  const detailSnoozed =
    typeof details.attention_snoozed_until === "string"
      ? details.attention_snoozed_until
      : undefined;
  return {
    state: detailState || "new",
    pinned: detailPinned,
    snoozedUntil: detailSnoozed,
  };
}

export default function AttentionPage() {
  const queryClient = useQueryClient();
  const [actionabilityFilter, setActionabilityFilter] =
    useState<ActionabilityFilter>("all");
  const [categoryFilter, setCategoryFilter] = useState<string>("all");
  const [sessionFilter, setSessionFilter] = useState<string>("");
  const [sinceCursor, setSinceCursor] = useState<number>(0);
  const [hideDismissed, setHideDismissed] = useState<boolean>(true);
  const [stateOverrides, setStateOverrides] = useState<
    Record<number, AttentionItemState>
  >({});
  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const trimmedSession = sessionFilter.trim();

  const digestPath = useMemo(() => {
    const params = new URLSearchParams();
    if (sinceCursor > 0) params.set("since_cursor", String(sinceCursor));
    if (categoryFilter !== "all") params.set("category", categoryFilter);
    if (trimmedSession) params.set("session", trimmedSession);
    const qs = params.toString();
    return `/api/v1/attention/digest${qs ? `?${qs}` : ""}`;
  }, [sinceCursor, categoryFilter, trimmedSession]);

  const eventsPath = useMemo(() => {
    const params = new URLSearchParams();
    params.set("limit", "100");
    if (sinceCursor > 0) params.set("since_cursor", String(sinceCursor));
    if (actionabilityFilter !== "all") {
      params.set("actionability", actionabilityFilter);
    }
    if (categoryFilter !== "all") params.set("category", categoryFilter);
    if (trimmedSession) params.set("session", trimmedSession);
    return `/api/v1/attention/events?${params.toString()}`;
  }, [sinceCursor, actionabilityFilter, categoryFilter, trimmedSession]);

  const digestQuery = useQuery({
    queryKey: ["attention", "digest", digestPath],
    queryFn: () => apiFetch<AttentionDigest>(digestPath),
    refetchInterval: 10_000,
  });

  const eventsQuery = useQuery({
    queryKey: ["attention", "events", eventsPath],
    queryFn: () => apiFetch<AttentionEventsResponse>(eventsPath),
    refetchInterval: 10_000,
  });

  const stateMutation = useMutation({
    mutationFn: async ({
      cursor,
      body,
    }: {
      cursor: number;
      body: AttentionItemStateRequest;
    }) => {
      return apiFetch<AttentionItemStateResponse>(
        `/api/v1/attention/items/${encodeURIComponent(String(cursor))}/state`,
        {
          method: "POST",
          body: JSON.stringify(body),
        }
      );
    },
    onSuccess: (response, variables) => {
      const updated = response.result?.attention_state;
      if (updated) {
        setStateOverrides((prev) => ({
          ...prev,
          [variables.cursor]: updated,
        }));
      }
      setNotice({
        kind: "success",
        text: `Applied "${variables.body.action}" to attention item #${variables.cursor} (state: ${response.result?.new_state ?? "updated"}).`,
      });
      void queryClient.invalidateQueries({ queryKey: ["attention"] });
    },
    onError: (err, variables) => {
      setNotice({
        kind: "error",
        text: `Failed to ${variables.body.action} item #${variables.cursor}: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const digest = digestQuery.data;
  const actionRequiredCount =
    digest?.buckets?.action_required?.length ??
    digest?.by_actionability?.action_required ??
    0;
  const interestingCount =
    digest?.buckets?.interesting?.length ??
    digest?.by_actionability?.interesting ??
    0;
  const suppressedTotal = digest?.suppressed?.total ?? 0;

  const visibleEvents = useMemo(() => {
    const raw = eventsQuery.data?.events ?? [];
    const ordered = [...raw].sort((a, b) => b.cursor - a.cursor);
    if (!hideDismissed) return ordered;
    return ordered.filter(
      (ev) => readEventState(ev, stateOverrides).state !== "dismissed"
    );
  }, [eventsQuery.data?.events, hideDismissed, stateOverrides]);

  const handleRefresh = () => {
    void digestQuery.refetch();
    void eventsQuery.refetch();
  };

  const triggerAction = (cursor: number, body: AttentionItemStateRequest) => {
    stateMutation.mutate({ cursor, body });
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Attention
          </h1>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            Operator triage inbox across sessions, agents, beads, mail, and
            alerts with durable cursor state.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {sinceCursor > 0 && (
            <button
              type="button"
              onClick={() => setSinceCursor(0)}
              className="px-3 py-1.5 text-sm font-medium rounded-md border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
            >
              Reset Cursor (#0)
            </button>
          )}
          <button
            type="button"
            onClick={handleRefresh}
            className="px-3 py-1.5 text-sm font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700"
          >
            Refresh Feed
          </button>
        </div>
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
            Dismiss message
          </button>
        </div>
      )}

      {/* Digest Summary Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Action Required
          </div>
          <div
            className={`mt-2 text-2xl font-bold ${
              actionRequiredCount > 0
                ? "text-rose-600 dark:text-rose-400"
                : "text-gray-900 dark:text-white"
            }`}
          >
            {actionRequiredCount}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Items awaiting operator intervention
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Interesting Signals
          </div>
          <div className="mt-2 text-2xl font-bold text-amber-600 dark:text-amber-400">
            {interestingCount}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Notable state transitions &amp; advisories
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Window Events
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {digest?.event_count ?? eventsQuery.data?.event_count ?? 0}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 font-mono">
            Cursor #{eventsQuery.data?.oldest_cursor ?? 0} &rarr; #
            {eventsQuery.data?.newest_cursor ?? digest?.cursor_end ?? 0}
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Digest Noise Suppressed
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {suppressedTotal}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 truncate">
            {digest?.summary || "Coalesced heartbeats and burst output"}
          </div>
        </div>
      </div>

      {/* Filters & Cursor Controls */}
      <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
        <div className="grid grid-cols-1 md:grid-cols-4 gap-3">
          <div>
            <label
              htmlFor="attention-actionability-filter"
              className="block text-xs font-medium text-gray-600 dark:text-gray-300 mb-1"
            >
              Actionability
            </label>
            <select
              id="attention-actionability-filter"
              value={actionabilityFilter}
              onChange={(e) =>
                setActionabilityFilter(e.target.value as ActionabilityFilter)
              }
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
            >
              <option value="all">All urgencies</option>
              <option value="action_required">Action required</option>
              <option value="interesting">Interesting</option>
              <option value="background">Background</option>
            </select>
          </div>

          <div>
            <label
              htmlFor="attention-category-filter"
              className="block text-xs font-medium text-gray-600 dark:text-gray-300 mb-1"
            >
              Category
            </label>
            <select
              id="attention-category-filter"
              value={categoryFilter}
              onChange={(e) => setCategoryFilter(e.target.value)}
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
            >
              {CATEGORIES.map((cat) => (
                <option key={cat} value={cat}>
                  {cat === "all" ? "All categories" : cat}
                </option>
              ))}
            </select>
          </div>

          <div>
            <label
              htmlFor="attention-session-filter"
              className="block text-xs font-medium text-gray-600 dark:text-gray-300 mb-1"
            >
              Session
            </label>
            <input
              id="attention-session-filter"
              type="text"
              value={sessionFilter}
              onChange={(e) => setSessionFilter(e.target.value)}
              placeholder="Filter by session name..."
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
            />
          </div>

          <div className="flex items-end justify-between gap-2">
            <label className="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300 py-1.5">
              <input
                type="checkbox"
                checked={hideDismissed}
                onChange={(e) => setHideDismissed(e.target.checked)}
                className="rounded border-gray-300 dark:border-gray-600"
              />
              Hide dismissed
            </label>
            {typeof eventsQuery.data?.next_cursor === "number" &&
              eventsQuery.data.next_cursor > sinceCursor && (
                <button
                  type="button"
                  onClick={() =>
                    setSinceCursor(eventsQuery.data?.next_cursor ?? 0)
                  }
                  className="px-2.5 py-1.5 text-xs font-medium rounded border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
                >
                  Advance Cursor (#{eventsQuery.data.next_cursor})
                </button>
              )}
          </div>
        </div>
      </div>

      {/* Error State */}
      {(eventsQuery.isError || digestQuery.isError) && (
        <div
          role="alert"
          className="rounded-lg border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 p-4 text-sm text-red-800 dark:text-red-200"
        >
          Failed to load attention feed:{" "}
          {formatApiErrorMessage(eventsQuery.error ?? digestQuery.error)}
        </div>
      )}

      {/* Event Feed */}
      <div className="space-y-3">
        {eventsQuery.isLoading ? (
          <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-8 text-center text-sm text-gray-500 dark:text-gray-400">
            Loading attention events...
          </div>
        ) : visibleEvents.length === 0 ? (
          <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-8 text-center text-sm text-gray-500 dark:text-gray-400">
            No attention events match the active filters.
          </div>
        ) : (
          visibleEvents.map((ev) => {
            const itemState = readEventState(ev, stateOverrides);
            const busyForCursor =
              stateMutation.isPending &&
              stateMutation.variables?.cursor === ev.cursor;

            return (
              <div
                key={ev.cursor}
                data-testid={`attention-item-${ev.cursor}`}
                className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3"
              >
                <div className="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-3">
                  <div className="space-y-1.5 min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-xs text-gray-500 dark:text-gray-400">
                        #{ev.cursor}
                      </span>
                      <span
                        className={`px-2 py-0.5 text-xs font-medium rounded border ${severityBadgeClass(
                          ev.severity
                        )}`}
                      >
                        {ev.severity}
                      </span>
                      <span
                        className={`px-2 py-0.5 text-xs font-medium rounded ${actionabilityBadgeClass(
                          ev.actionability
                        )}`}
                      >
                        {ev.actionability}
                      </span>
                      <span className="px-2 py-0.5 text-xs rounded bg-gray-100 dark:bg-gray-700 text-gray-700 dark:text-gray-200 font-mono">
                        {ev.type || ev.category}
                      </span>
                      <span
                        className={`px-2 py-0.5 text-xs font-medium rounded ${stateBadgeClass(
                          itemState.state
                        )}`}
                      >
                        {itemState.state}
                      </span>
                      {itemState.pinned && (
                        <span className="px-2 py-0.5 text-xs font-medium rounded bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200">
                          pinned
                        </span>
                      )}
                      {ev.session && (
                        <span className="text-xs font-mono text-blue-600 dark:text-blue-400">
                          {ev.session}
                          {typeof ev.pane === "number" && ev.pane >= 0
                            ? `:%${ev.pane}`
                            : ""}
                        </span>
                      )}
                    </div>

                    <p className="text-sm font-medium text-gray-900 dark:text-white">
                      {ev.summary}
                    </p>

                    <div className="flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
                      <span>{formatTimestamp(ev.ts)}</span>
                      {ev.source && <span>Source: {ev.source}</span>}
                      {itemState.snoozedUntil && (
                        <span>
                          Snoozed until {formatTimestamp(itemState.snoozedUntil)}
                        </span>
                      )}
                    </div>
                  </div>

                  {/* Operator Actions */}
                  <div className="flex flex-wrap items-center gap-1.5 shrink-0">
                    <button
                      type="button"
                      disabled={busyForCursor}
                      onClick={() =>
                        triggerAction(ev.cursor, { action: "ack" })
                      }
                      aria-label={`Acknowledge item ${ev.cursor}`}
                      className="px-2.5 py-1 text-xs font-medium rounded border border-emerald-300 dark:border-emerald-700 text-emerald-700 dark:text-emerald-300 hover:bg-emerald-50 dark:hover:bg-emerald-900/30 disabled:opacity-50"
                    >
                      Acknowledge
                    </button>
                    <button
                      type="button"
                      disabled={busyForCursor}
                      onClick={() =>
                        triggerAction(ev.cursor, {
                          action: itemState.pinned ? "unpin" : "pin",
                        })
                      }
                      aria-label={`${itemState.pinned ? "Unpin" : "Pin"} item ${ev.cursor}`}
                      className="px-2.5 py-1 text-xs font-medium rounded border border-amber-300 dark:border-amber-700 text-amber-700 dark:text-amber-300 hover:bg-amber-50 dark:hover:bg-amber-900/30 disabled:opacity-50"
                    >
                      {itemState.pinned ? "Unpin" : "Pin"}
                    </button>
                    <button
                      type="button"
                      disabled={busyForCursor}
                      onClick={() =>
                        triggerAction(ev.cursor, {
                          action: "snooze",
                          until: new Date(
                            Date.now() + 30 * 60 * 1000
                          ).toISOString(),
                        })
                      }
                      aria-label={`Snooze item ${ev.cursor}`}
                      className="px-2.5 py-1 text-xs font-medium rounded border border-indigo-300 dark:border-indigo-700 text-indigo-700 dark:text-indigo-300 hover:bg-indigo-50 dark:hover:bg-indigo-900/30 disabled:opacity-50"
                    >
                      Snooze 30m
                    </button>
                    <button
                      type="button"
                      disabled={busyForCursor}
                      onClick={() =>
                        triggerAction(ev.cursor, { action: "dismiss" })
                      }
                      aria-label={`Dismiss item ${ev.cursor}`}
                      className="px-2.5 py-1 text-xs font-medium rounded border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700 disabled:opacity-50"
                    >
                      Dismiss
                    </button>
                  </div>
                </div>

                {ev.next_actions && ev.next_actions.length > 0 && (
                  <div className="pt-2 border-t border-gray-100 dark:border-gray-700/70 space-y-1">
                    <div className="text-xs font-medium text-gray-500 dark:text-gray-400">
                      Suggested Robot Commands
                    </div>
                    <div className="flex flex-wrap gap-2">
                      {ev.next_actions.map((na, idx) => (
                        <code
                          key={`${ev.cursor}-na-${idx}`}
                          title={na.reason || na.action}
                          className="px-2 py-1 text-xs rounded bg-gray-100 dark:bg-gray-900 text-gray-800 dark:text-gray-200 font-mono"
                        >
                          ntm {na.args}
                        </code>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}
