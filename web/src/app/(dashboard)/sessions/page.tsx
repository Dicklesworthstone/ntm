"use client";

/**
 * Session Detail Page
 *
 * Shows session overview, panes list, and live output viewer.
 */

import Link from "next/link";
import { useParams } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { apiRequestSignal, getAuthHeaders, getBaseUrl } from "@/lib/api/client";
import { useConnection } from "@/lib/hooks/use-query";

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface Session {
  name: string;
  created_at?: string;
  project_path?: string;
  status?: string;
}

interface SessionResponse extends ApiEnvelope {
  session: Session;
}

interface Pane {
  index: number;
  id?: string;
  title?: string;
  type?: string;
  variant?: string;
  active?: boolean;
  width?: number;
  height?: number;
  command?: string;
}

interface PaneListResponse extends ApiEnvelope {
  session_id: string;
  panes: Pane[];
  count: number;
}

interface PaneOutputResponse extends ApiEnvelope {
  pane: string;
  output: string;
  lines: number;
}

interface PaneInputResponse extends ApiEnvelope {
  sent: boolean;
  pane: string;
}

type Notice = { type: "success" | "error"; message: string };

const DEFAULT_OUTPUT_LINES = 200;

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
    const message = envelope?.error || `Request failed (${response.status})`;
    throw new Error(message);
  }

  return data as T;
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "Unexpected error";
}

export default function SessionDetailPage() {
  const params = useParams();
  const sessionId = decodeURIComponent(String(params.sessionId || ""));
  const queryClient = useQueryClient();
  const { isConnected } = useConnection();
  const outputRef = useRef<HTMLDivElement | null>(null);
  const noticeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const [selectedPane, setSelectedPane] = useState<number | null>(null);
  const [outputLines, setOutputLines] = useState(DEFAULT_OUTPUT_LINES);
  const [autoScroll, setAutoScroll] = useState(true);
  const [prompt, setPrompt] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);

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

  const sessionQuery = useQuery({
    queryKey: ["sessions", sessionId],
    queryFn: () => apiFetch<SessionResponse>(`/api/v1/sessions/${sessionId}`),
    refetchInterval: 15000,
  });

  const panesQuery = useQuery({
    queryKey: ["panes", sessionId],
    queryFn: () => apiFetch<PaneListResponse>(`/api/v1/sessions/${sessionId}/panes`),
    refetchInterval: 5000,
  });

  const paneIndex = selectedPane ?? panesQuery.data?.panes?.[0]?.index ?? null;

  const outputQuery = useQuery({
    queryKey: ["pane-output", sessionId, paneIndex, outputLines],
    queryFn: () =>
      apiFetch<PaneOutputResponse>(
        `/api/v1/sessions/${sessionId}/panes/${paneIndex}/output?lines=${outputLines}`
      ),
    enabled: paneIndex !== null,
    refetchInterval: 2500,
  });

  useEffect(() => {
    const panes = panesQuery.data?.panes ?? [];
    if (panes.length === 0) {
      if (selectedPane !== null) {
        setSelectedPane(null);
      }
      return;
    }

    const hasSelectedPane = selectedPane !== null
      ? panes.some((pane) => pane.index === selectedPane)
      : false;

    if (!hasSelectedPane) {
      setSelectedPane(panes[0].index);
    }
  }, [panesQuery.data, selectedPane]);

  useEffect(() => {
    if (!autoScroll || !outputRef.current) return;
    outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [autoScroll, outputQuery.data]);

  const outputSnapshot = useMemo(() => {
    const output = outputQuery.data?.output || "";
    if (!output) {
      return { lines: [], total: 0, truncated: false };
    }
    const list = output.split("\n");
    const truncated = list.length > outputLines;
    const lines = truncated ? list.slice(-outputLines) : list;
    return { lines, total: list.length, truncated };
  }, [outputLines, outputQuery.data]);

  const agentCounts = useMemo(() => {
    const panes = panesQuery.data?.panes || [];
    return panes.reduce<Record<string, number>>((acc, pane) => {
      const type = pane.type || "unknown";
      acc[type] = (acc[type] || 0) + 1;
      return acc;
    }, {});
  }, [panesQuery.data]);

  const sendPrompt = useCallback(async () => {
    if (!prompt.trim()) return;
    if (paneIndex === null) {
      setStatusNotice({ type: "error", message: "Select a pane first." });
      return;
    }

    try {
      await apiFetch<PaneInputResponse>(
        `/api/v1/sessions/${sessionId}/panes/${paneIndex}/input`,
        {
          method: "POST",
          body: JSON.stringify({ text: prompt.trim(), enter: true }),
        }
      );

      if (process.env.NODE_ENV === "development") {
        console.log("[Command Palette] send prompt", {
          sessionId,
          paneIndex,
          length: prompt.length,
        });
      }

      setPrompt("");
      queryClient.invalidateQueries({
        queryKey: ["pane-output", sessionId, paneIndex],
      });
    } catch (error) {
      if (process.env.NODE_ENV === "development") {
        console.error("[Command Palette] send prompt error", error);
      }
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    }
  }, [prompt, paneIndex, queryClient, sessionId, setStatusNotice]);

  const interruptPane = useCallback(async () => {
    if (paneIndex === null) return;
    try {
      await apiFetch(
        `/api/v1/sessions/${sessionId}/panes/${paneIndex}/interrupt`,
        { method: "POST" }
      );
      setStatusNotice({ type: "success", message: "Interrupt sent." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    }
  }, [paneIndex, sessionId, setStatusNotice]);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-3">
            <Link
              href="/"
              className="text-sm text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"
            >
              ← Back to sessions
            </Link>
            <span
              className={`rounded-full px-2 py-0.5 text-xs ${
                isConnected
                  ? "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300"
                  : "bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300"
              }`}
            >
              {isConnected ? "Live" : "Offline"}
            </span>
          </div>
          <h1 className="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">
            {sessionId}
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Session overview, panes, and live output.
          </p>
        </div>
        <div className="text-sm text-gray-500 dark:text-gray-400">
          {sessionQuery.data?.session?.created_at
            ? `Created ${new Date(
                sessionQuery.data.session.created_at
              ).toLocaleString()}`
            : "Created date unknown"}
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
        <StatCard
          title="Panes"
          value={panesQuery.data?.count ?? 0}
          helper="Active panes"
        />
        <StatCard
          title="Agents"
          value={Object.values(agentCounts).reduce((sum, count) => sum + count, 0)}
          helper="Agent count"
        />
        <StatCard
          title="Status"
          value={sessionQuery.data?.session?.status || "—"}
          helper="Current session state"
        />
        <StatCard
          title="Output Lines"
          value={outputLines}
          helper="Lines in view"
        />
      </div>

      {(sessionQuery.error || panesQuery.error) && (
        <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
          {getErrorMessage(sessionQuery.error || panesQuery.error)}
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-3">
        <section className="space-y-4">
          <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Panes
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Select a pane to view output.
            </p>

            {panesQuery.isLoading && (
              <div className="py-6 text-center text-sm text-gray-400">
                Loading panes...
              </div>
            )}

            {!panesQuery.isLoading && panesQuery.data?.panes?.length === 0 && (
              <div className="py-6 text-center text-sm text-gray-400">
                No panes detected.
              </div>
            )}

            <div className="mt-4 space-y-2">
              {(panesQuery.data?.panes || []).map((pane) => {
                const isSelected = pane.index === paneIndex;
                return (
                  <button
                    key={pane.index}
                    type="button"
                    onClick={() => setSelectedPane(pane.index)}
                    className={`w-full rounded-md border px-3 py-2 text-left text-sm transition ${
                      isSelected
                        ? "border-blue-400 bg-blue-50 text-blue-900 dark:border-blue-500 dark:bg-blue-900/20 dark:text-blue-200"
                        : "border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-300"
                    }`}
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-medium">
                        Pane {pane.index}
                      </span>
                      {pane.active && (
                        <span className="text-xs text-green-600 dark:text-green-400">
                          active
                        </span>
                      )}
                    </div>
                    <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {pane.title || pane.command || pane.type || "Untitled"}
                    </div>
                  </button>
                );
              })}
            </div>
          </div>

          <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
            <h3 className="text-base font-semibold text-gray-900 dark:text-white">
              Agent Mix
            </h3>
            <div className="mt-3 space-y-2 text-sm text-gray-600 dark:text-gray-300">
              {Object.keys(agentCounts).length === 0 && (
                <div className="text-sm text-gray-400">No agent metadata.</div>
              )}
              {Object.entries(agentCounts).map(([type, count]) => (
                <div key={type} className="flex items-center justify-between">
                  <span className="capitalize">{type}</span>
                  <span className="font-medium">{count}</span>
                </div>
              ))}
            </div>
          </div>
        </section>

        <section className="lg:col-span-2 space-y-4">
          <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                  Pane Viewer
                </h2>
                <p className="text-sm text-gray-500 dark:text-gray-400">
                  Live output with auto-scroll and send input.
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={autoScroll}
                    onChange={(event) => setAutoScroll(event.target.checked)}
                    className="h-4 w-4"
                  />
                  Auto-scroll
                </label>
                <select
                  value={outputLines}
                  onChange={(event) => setOutputLines(Number(event.target.value))}
                  className="rounded-md border border-gray-200 bg-white px-2 py-1 text-xs text-gray-700 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200"
                >
                  {[100, 200, 500, 1000].map((value) => (
                    <option key={value} value={value}>
                      Last {value} lines
                    </option>
                  ))}
                </select>
              </div>
            </div>

            <div className="mt-4 flex flex-col gap-3 sm:flex-row sm:items-center">
              <input
                value={prompt}
                onChange={(event) => setPrompt(event.target.value)}
                placeholder="Send a prompt to this pane..."
                className="flex-1 rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white shadow-sm focus:border-blue-500 focus:outline-none"
              />
              <button
                type="button"
                onClick={sendPrompt}
                className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
              >
                Send
              </button>
              <button
                type="button"
                onClick={interruptPane}
                className="rounded-md border border-gray-200 px-3 py-2 text-sm text-gray-700 hover:border-gray-300 dark:border-gray-700 dark:text-gray-200"
              >
                Interrupt
              </button>
            </div>

            {outputQuery.isLoading && (
              <div className="mt-6 text-center text-sm text-gray-400">
                Loading output...
              </div>
            )}

            {outputQuery.error && (
              <div className="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
                {getErrorMessage(outputQuery.error)}
              </div>
            )}

            <div
              ref={outputRef}
              className="mt-4 h-80 overflow-y-auto rounded-md border border-gray-200 bg-gray-50 p-3 text-xs font-mono text-gray-700 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200"
            >
              {outputSnapshot.lines.length === 0 && (
                <div className="text-gray-400">No output yet.</div>
              )}
              {outputSnapshot.lines.map((line, index) => (
                <div key={`${index}-${line}`} className="whitespace-pre-wrap">
                  {line}
                </div>
              ))}
              {outputSnapshot.truncated && (
                <div className="mt-2 text-gray-400">
                  Showing last {outputLines} lines of {outputSnapshot.total}.
                </div>
              )}
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}

function StatCard({ title, value, helper }: { title: string; value: string | number; helper: string }) {
  return (
    <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
      <div className="text-xs uppercase tracking-wide text-gray-500 dark:text-gray-400">
        {title}
      </div>
      <div className="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">
        {value}
      </div>
      <div className="mt-1 text-xs text-gray-400 dark:text-gray-500">{helper}</div>
    </div>
  );
}
