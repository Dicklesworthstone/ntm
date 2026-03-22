"use client";

/**
 * Mail Page
 *
 * Agent mail coordination.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getAuthHeaders, getBaseUrl } from "@/lib/api/client";

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface Agent {
  id: number;
  name: string;
  program?: string;
  model?: string;
  task_description?: string;
  last_active_ts?: string;
}

interface MailAgentsResponse extends ApiEnvelope {
  agents: Agent[];
  count: number;
}

interface InboxMessage {
  id: number;
  subject: string;
  from: string;
  created_ts: string;
  thread_id?: string | null;
  importance: string;
  ack_required: boolean;
  kind?: string;
  body_md?: string;
  read_at?: string | null;
}

interface MailInboxResponse extends ApiEnvelope {
  messages: InboxMessage[];
  count: number;
}

interface ThreadSummary {
  thread_id: string;
  participants: string[];
  key_points: string[];
  action_items: string[];
}

interface ThreadSummaryResponse extends ApiEnvelope {
  summary: ThreadSummary;
}

interface Reservation {
  id: number;
  path_pattern: string;
  agent_name: string;
  exclusive: boolean;
  reason?: string;
  created_ts?: string;
  expires_ts?: string;
  released_ts?: string | null;
}

interface ReservationConflict {
  path: string;
  holders: string[];
}

interface ReservationsResponse extends ApiEnvelope {
  reservations: Reservation[];
  count: number;
}

interface ReservationConflictsResponse extends ApiEnvelope {
  conflicts: ReservationConflict[];
  has_conflicts: boolean;
}

type Notice = { type: "success" | "error"; message: string };

type ImportanceFilter = "all" | "normal" | "high" | "urgent";

const AGENT_STORAGE_KEY = "ntm-mail-agent";

async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const baseUrl = getBaseUrl();
  const headers: HeadersInit = {
    "Content-Type": "application/json",
    ...getAuthHeaders(),
    ...(options.headers || {}),
  };

  const response = await fetch(`${baseUrl}${path}`, {
    ...options,
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
    const message = envelope?.error || `Request failed (${response.status})`;
    throw new Error(message);
  }

  return data as T;
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "Unexpected error";
}

function formatTimestamp(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatShortDate(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleDateString();
}

function importanceBadge(importance: string): string {
  switch (importance) {
    case "urgent":
      return "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300";
    case "high":
      return "bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-300";
    default:
      return "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300";
  }
}

export default function MailPage() {
  const queryClient = useQueryClient();
  const [agentName, setAgentName] = useState("");
  const [importanceFilter, setImportanceFilter] =
    useState<ImportanceFilter>("all");
  const [ackOnly, setAckOnly] = useState(false);
  const [selectedMessageId, setSelectedMessageId] = useState<number | null>(null);
  const [replyBody, setReplyBody] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);
  const noticeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [actionBusy, setActionBusy] = useState({
    ack: false,
    read: false,
    reply: false,
  });

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

  useEffect(() => {
    if (typeof window === "undefined") return;
    const stored = localStorage.getItem(AGENT_STORAGE_KEY);
    if (stored) setAgentName(stored);
  }, []);

  useEffect(() => {
    if (typeof window === "undefined") return;
    if (!agentName) {
      localStorage.removeItem(AGENT_STORAGE_KEY);
      return;
    }
    localStorage.setItem(AGENT_STORAGE_KEY, agentName);
  }, [agentName]);

  const agentsQuery = useQuery({
    queryKey: ["mail", "agents"],
    queryFn: () => apiFetch<MailAgentsResponse>("/api/v1/mail/agents"),
    refetchInterval: 60000,
  });

  useEffect(() => {
    if (agentName) return;
    const firstAgent = agentsQuery.data?.agents?.[0]?.name;
    if (firstAgent) {
      setAgentName(firstAgent);
    }
  }, [agentName, agentsQuery.data]);

  const inboxQuery = useQuery({
    queryKey: ["mail", "inbox", agentName],
    queryFn: () =>
      apiFetch<MailInboxResponse>(
        `/api/v1/mail/inbox?agent_name=${encodeURIComponent(
          agentName
        )}&limit=200&include_bodies=true`
      ),
    enabled: agentName.length > 0,
    refetchInterval: 10000,
  });

  const reservationsQuery = useQuery({
    queryKey: ["reservations"],
    queryFn: () => apiFetch<ReservationsResponse>("/api/v1/reservations"),
    refetchInterval: 15000,
  });

  const reservationPaths = useMemo(() => {
    const reservations = reservationsQuery.data?.reservations ?? [];
    const paths = new Set<string>();
    reservations.forEach((reservation) => {
      if (reservation.path_pattern) {
        paths.add(reservation.path_pattern);
      }
    });
    return Array.from(paths);
  }, [reservationsQuery.data]);

  const conflictsQuery = useQuery({
    queryKey: ["reservations", "conflicts", reservationPaths],
    queryFn: () => {
      const query = reservationPaths
        .slice(0, 200)
        .map((path) => `paths=${encodeURIComponent(path)}`)
        .join("&");
      return apiFetch<ReservationConflictsResponse>(
        `/api/v1/reservations/conflicts?${query}`
      );
    },
    enabled: reservationPaths.length > 0,
    refetchInterval: 20000,
  });

  const messageList = inboxQuery.data?.messages ?? [];

  const filteredMessages = useMemo(() => {
    const sorted = [...messageList].sort((a, b) => {
      const aTime = new Date(a.created_ts).getTime();
      const bTime = new Date(b.created_ts).getTime();
      return bTime - aTime;
    });

    return sorted.filter((message) => {
      if (importanceFilter !== "all" && message.importance !== importanceFilter) {
        return false;
      }
      if (ackOnly && !message.ack_required) {
        return false;
      }
      return true;
    });
  }, [messageList, importanceFilter, ackOnly]);

  useEffect(() => {
    if (selectedMessageId === null && filteredMessages.length > 0) {
      setSelectedMessageId(filteredMessages[0].id);
      return;
    }
    if (
      selectedMessageId !== null &&
      !filteredMessages.some((message) => message.id === selectedMessageId)
    ) {
      setSelectedMessageId(filteredMessages[0]?.id ?? null);
    }
  }, [filteredMessages, selectedMessageId]);

  const selectedMessage = useMemo(() => {
    if (selectedMessageId === null) return null;
    return messageList.find((message) => message.id === selectedMessageId) ?? null;
  }, [messageList, selectedMessageId]);

  const threadSummaryQuery = useQuery({
    queryKey: ["mail", "thread", selectedMessage?.thread_id],
    queryFn: () =>
      apiFetch<ThreadSummaryResponse>(
        `/api/v1/mail/threads/${selectedMessage?.thread_id}/summary`
      ),
    enabled: Boolean(selectedMessage?.thread_id),
  });

  const inboxUnreadCount = useMemo(() => {
    return messageList.filter((message) => !message.read_at).length;
  }, [messageList]);

  const ackRequiredCount = useMemo(() => {
    return messageList.filter((message) => message.ack_required).length;
  }, [messageList]);

  const reservationGroups = useMemo(() => {
    const reservations = reservationsQuery.data?.reservations ?? [];
    const grouped = new Map<string, Reservation[]>();
    reservations.forEach((reservation) => {
      const key = reservation.path_pattern || "(unknown)";
      const list = grouped.get(key) ?? [];
      list.push(reservation);
      grouped.set(key, list);
    });

    return Array.from(grouped.entries()).map(([path, entries]) => {
      const agentSet = new Set(entries.map((entry) => entry.agent_name));
      const exclusive = entries.some((entry) => entry.exclusive);
      const earliestExpiry = entries
        .map((entry) => entry.expires_ts)
        .filter(Boolean)
        .sort()[0];
      return {
        path,
        entries,
        agents: Array.from(agentSet),
        exclusive,
        earliestExpiry,
      };
    });
  }, [reservationsQuery.data]);

  const conflictMap = useMemo(() => {
    const conflicts = conflictsQuery.data?.conflicts ?? [];
    const map = new Map<string, ReservationConflict>();
    conflicts.forEach((conflict) => {
      map.set(conflict.path, conflict);
    });
    return map;
  }, [conflictsQuery.data]);

  const handleMarkRead = useCallback(async () => {
    if (!selectedMessageId || !agentName) return;
    setActionBusy((prev) => ({ ...prev, read: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Mail] Mark read", { selectedMessageId, agentName });
      }
      await apiFetch<ApiEnvelope>(
        `/api/v1/mail/messages/${selectedMessageId}/read?agent_name=${encodeURIComponent(
          agentName
        )}`,
        { method: "POST" }
      );
      queryClient.invalidateQueries({ queryKey: ["mail", "inbox"] });
      setStatusNotice({ type: "success", message: "Marked as read." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, read: false }));
    }
  }, [agentName, queryClient, selectedMessageId, setStatusNotice]);

  const handleAck = useCallback(async () => {
    if (!selectedMessageId || !agentName) return;
    setActionBusy((prev) => ({ ...prev, ack: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Mail] Acknowledge", { selectedMessageId, agentName });
      }
      await apiFetch<ApiEnvelope>(
        `/api/v1/mail/messages/${selectedMessageId}/ack?agent_name=${encodeURIComponent(
          agentName
        )}`,
        { method: "POST" }
      );
      queryClient.invalidateQueries({ queryKey: ["mail", "inbox"] });
      setStatusNotice({ type: "success", message: "Acknowledged." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, ack: false }));
    }
  }, [agentName, queryClient, selectedMessageId, setStatusNotice]);

  const handleReply = useCallback(async () => {
    if (!selectedMessageId || !agentName) return;
    if (!replyBody.trim()) {
      setStatusNotice({ type: "error", message: "Reply body is empty." });
      return;
    }

    setActionBusy((prev) => ({ ...prev, reply: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Mail] Reply", { selectedMessageId, agentName });
      }
      await apiFetch<ApiEnvelope>(
        `/api/v1/mail/messages/${selectedMessageId}/reply`,
        {
          method: "POST",
          body: JSON.stringify({
            sender_name: agentName,
            body_md: replyBody,
          }),
        }
      );
      setReplyBody("");
      queryClient.invalidateQueries({ queryKey: ["mail", "inbox"] });
      setStatusNotice({ type: "success", message: "Reply sent." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, reply: false }));
    }
  }, [
    agentName,
    queryClient,
    replyBody,
    selectedMessageId,
    setStatusNotice,
  ]);

  const connectionError =
    agentsQuery.error ??
    inboxQuery.error ??
    reservationsQuery.error ??
    conflictsQuery.error ??
    threadSummaryQuery.error;

  return (
    <div className="space-y-8">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Mail
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Inbox, threads, and file reservations for Agent Mail.
          </p>
        </div>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
          <div>
            <label className="block text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
              Active Agent
            </label>
            <input
              list="mail-agents"
              value={agentName}
              onChange={(event) => setAgentName(event.target.value)}
              placeholder="Select agent..."
              className="mt-1 w-56 rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
            />
            <datalist id="mail-agents">
              {(agentsQuery.data?.agents ?? []).map((agent) => (
                <option key={agent.id} value={agent.name} />
              ))}
            </datalist>
          </div>
          <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-3 py-2 text-xs text-gray-500 dark:text-gray-400">
            <div>
              {messageList.length} messages · {inboxUnreadCount} unread
            </div>
            <div>{ackRequiredCount} require ack</div>
          </div>
        </div>
      </div>

      {connectionError && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md">
          <p className="text-sm text-red-700 dark:text-red-400">
            Mail error: {getErrorMessage(connectionError)}
          </p>
        </div>
      )}

      {notice && (
        <div
          className={`p-3 rounded-md border text-sm ${
            notice.type === "success"
              ? "bg-green-50 border-green-200 text-green-700 dark:bg-green-900/20 dark:border-green-800 dark:text-green-300"
              : "bg-red-50 border-red-200 text-red-700 dark:bg-red-900/20 dark:border-red-800 dark:text-red-300"
          }`}
        >
          {notice.message}
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)]">
        <section className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Inbox
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Filter by importance or ack requirements.
              </p>
            </div>
            <div className="flex items-center gap-2">
              <select
                value={importanceFilter}
                onChange={(event) =>
                  setImportanceFilter(event.target.value as ImportanceFilter)
                }
                className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-700 dark:text-gray-200"
              >
                <option value="all">All</option>
                <option value="normal">Normal</option>
                <option value="high">High</option>
                <option value="urgent">Urgent</option>
              </select>
              <label className="flex items-center gap-2 text-xs text-gray-600 dark:text-gray-300">
                <input
                  type="checkbox"
                  checked={ackOnly}
                  onChange={(event) => setAckOnly(event.target.checked)}
                />
                Ack only
              </label>
            </div>
          </div>

          {!agentName && (
            <div className="p-4 rounded-md border border-dashed border-gray-300 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              Select an agent to load their inbox.
            </div>
          )}

          {agentName && inboxQuery.isLoading && (
            <div className="flex items-center justify-center h-40">
              <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
            </div>
          )}

          {agentName && !inboxQuery.isLoading && filteredMessages.length === 0 && (
            <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              No messages match the current filters.
            </div>
          )}

          {filteredMessages.length > 0 && (
            <div className="space-y-2">
              {filteredMessages.map((message) => {
                const isSelected = message.id === selectedMessageId;
                const unread = !message.read_at;
                return (
                  <button
                    key={message.id}
                    type="button"
                    onClick={() => setSelectedMessageId(message.id)}
                    className={`w-full text-left rounded-md border px-3 py-3 transition-colors ${
                      isSelected
                        ? "border-blue-400 bg-blue-50 dark:bg-blue-900/20"
                        : "border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 hover:border-blue-300"
                    }`}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <div
                          className={`text-sm ${
                            unread
                              ? "font-semibold text-gray-900 dark:text-white"
                              : "text-gray-700 dark:text-gray-200"
                          }`}
                        >
                          {message.subject}
                        </div>
                        <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                          From {message.from} · {formatShortDate(message.created_ts)}
                        </div>
                      </div>
                      <div className="flex flex-col items-end gap-2">
                        <span
                          className={`px-2 py-0.5 rounded-full text-xs ${importanceBadge(
                            message.importance
                          )}`}
                        >
                          {message.importance}
                        </span>
                        {message.ack_required && (
                          <span className="text-[10px] uppercase text-red-500">
                            Ack required
                          </span>
                        )}
                      </div>
                    </div>
                  </button>
                );
              })}
            </div>
          )}
        </section>

        <section className="space-y-4">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Thread
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Message detail and reply controls.
            </p>
          </div>

          {!selectedMessage && (
            <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              Select a message to view details.
            </div>
          )}

          {selectedMessage && (
            <div className="space-y-4">
              <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
                <div className="flex items-start justify-between gap-4">
                  <div>
                    <h3 className="text-lg font-semibold text-gray-900 dark:text-white">
                      {selectedMessage.subject}
                    </h3>
                    <p className="text-xs text-gray-500 dark:text-gray-400">
                      From {selectedMessage.from} · {formatTimestamp(selectedMessage.created_ts)}
                    </p>
                  </div>
                  <div className="flex flex-col items-end gap-2">
                    <span
                      className={`px-2 py-0.5 rounded-full text-xs ${importanceBadge(
                        selectedMessage.importance
                      )}`}
                    >
                      {selectedMessage.importance}
                    </span>
                    {selectedMessage.ack_required && (
                      <span className="text-[10px] uppercase text-red-500">
                        Ack required
                      </span>
                    )}
                  </div>
                </div>

                <div className="text-xs text-gray-500 dark:text-gray-400 space-y-1">
                  <div>Recipients: Available in message threads and replies.</div>
                  <div>Thread: {selectedMessage.thread_id || "—"}</div>
                  <div>
                    Read: {selectedMessage.read_at ? formatTimestamp(selectedMessage.read_at) : "Unread"}
                  </div>
                </div>

                <div className="flex flex-wrap gap-2">
                  <button
                    type="button"
                    onClick={handleMarkRead}
                    disabled={!agentName || actionBusy.read}
                    className="px-3 py-1.5 rounded-md text-xs font-medium border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
                  >
                    Mark read
                  </button>
                  <button
                    type="button"
                    onClick={handleAck}
                    disabled={!agentName || actionBusy.ack || !selectedMessage.ack_required}
                    className="px-3 py-1.5 rounded-md text-xs font-medium border border-red-200 text-red-600 dark:border-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
                  >
                    Acknowledge
                  </button>
                </div>

                <div className="border-t border-gray-200 dark:border-gray-700 pt-3">
                  <div className="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase">
                    Body
                  </div>
                  <div className="mt-2 whitespace-pre-wrap text-sm text-gray-800 dark:text-gray-100">
                    {selectedMessage.body_md ||
                      "No body content available."}
                  </div>
                </div>
              </div>

              <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
                <div className="text-sm font-semibold text-gray-900 dark:text-white">
                  Reply
                </div>
                <textarea
                  value={replyBody}
                  onChange={(event) => setReplyBody(event.target.value)}
                  rows={4}
                  placeholder="Write a reply..."
                  className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
                />
                <div className="flex items-center justify-between">
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    Reply will go to original sender unless you specify recipients
                    in Agent Mail.
                  </span>
                  <button
                    type="button"
                    onClick={handleReply}
                    disabled={!agentName || actionBusy.reply}
                    className="px-3 py-1.5 rounded-md text-xs font-medium bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
                  >
                    Send Reply
                  </button>
                </div>
              </div>

              <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-2">
                <div className="text-sm font-semibold text-gray-900 dark:text-white">
                  Thread Summary
                </div>
                {selectedMessage.thread_id ? (
                  threadSummaryQuery.isLoading ? (
                    <div className="text-sm text-gray-500 dark:text-gray-400">
                      Loading summary...
                    </div>
                  ) : threadSummaryQuery.data?.summary ? (
                    <div className="space-y-2 text-sm text-gray-700 dark:text-gray-200">
                      <div>
                        <span className="font-semibold">Participants:</span>{" "}
                        {threadSummaryQuery.data.summary.participants.join(", ") ||
                          "—"}
                      </div>
                      <div>
                        <span className="font-semibold">Key points:</span>{" "}
                        {threadSummaryQuery.data.summary.key_points.length > 0
                          ? threadSummaryQuery.data.summary.key_points.join(
                              " · "
                            )
                          : "—"}
                      </div>
                      <div>
                        <span className="font-semibold">Action items:</span>{" "}
                        {threadSummaryQuery.data.summary.action_items.length > 0
                          ? threadSummaryQuery.data.summary.action_items.join(
                              " · "
                            )
                          : "—"}
                      </div>
                    </div>
                  ) : (
                    <div className="text-sm text-gray-500 dark:text-gray-400">
                      No summary available yet.
                    </div>
                  )
                ) : (
                  <div className="text-sm text-gray-500 dark:text-gray-400">
                    This message is not part of a thread.
                  </div>
                )}
              </div>
            </div>
          )}
        </section>
      </div>

      <section className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Reservation Map
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Active file reservations grouped by path.
            </p>
          </div>
          <div className="text-xs text-gray-500 dark:text-gray-400">
            {reservationsQuery.data?.count ?? 0} active reservations
          </div>
        </div>

        {reservationsQuery.isLoading && (
          <div className="flex items-center justify-center h-32">
            <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
          </div>
        )}

        {!reservationsQuery.isLoading && reservationGroups.length === 0 && (
          <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
            No active reservations.
          </div>
        )}

        {reservationGroups.length > 0 && (
          <div className="grid gap-3 lg:grid-cols-2">
            {reservationGroups.map((group) => {
              const conflict = conflictMap.get(group.path);
              const multiHolder = group.agents.length > 1;
              const highlight = Boolean(conflict);

              return (
                <div
                  key={group.path}
                  className={`rounded-md border p-4 space-y-2 ${
                    highlight
                      ? "border-red-300 bg-red-50 dark:border-red-700 dark:bg-red-900/20"
                      : "border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800"
                  }`}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <div className="font-medium text-sm text-gray-900 dark:text-white">
                        {group.path}
                      </div>
                      <div className="text-xs text-gray-500 dark:text-gray-400">
                        {group.exclusive ? "Exclusive" : "Shared"} · {" "}
                        {group.entries.length} reservation
                        {group.entries.length !== 1 ? "s" : ""}
                      </div>
                    </div>
                    <div className="flex flex-col items-end gap-1">
                      {highlight && (
                        <span className="px-2 py-0.5 rounded-full text-[10px] uppercase bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300">
                          Conflict
                        </span>
                      )}
                      {!highlight && multiHolder && (
                        <span className="px-2 py-0.5 rounded-full text-[10px] uppercase bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300">
                          Multi-holder
                        </span>
                      )}
                    </div>
                  </div>

                  <div className="text-xs text-gray-500 dark:text-gray-400">
                    Agents: {group.agents.join(", ") || "—"}
                  </div>

                  {group.earliestExpiry && (
                    <div className="text-xs text-gray-500 dark:text-gray-400">
                      Earliest expiry: {formatTimestamp(group.earliestExpiry)}
                    </div>
                  )}

                  {conflict && (
                    <div className="text-xs text-red-600 dark:text-red-300">
                      Conflict holders: {conflict.holders.join(", ")}
                    </div>
                  )}

                  <div className="text-xs text-gray-500 dark:text-gray-400">
                    Reasons: {group.entries
                      .map((entry) => entry.reason)
                      .filter(Boolean)
                      .join(" · ") || "—"}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </section>
    </div>
  );
}
