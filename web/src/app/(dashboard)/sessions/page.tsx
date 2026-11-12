"use client";

/**
 * Session Detail Page
 *
 * Shows session overview, panes list, and live output viewer.
 * The target session comes from the `?session=` query parameter so the
 * route stays statically exportable (output: "export" forbids dynamic
 * path segments without generateStaticParams).
 */

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch, type ApiEnvelope } from "@/lib/api/client";
import { useConnection } from "@/lib/hooks/use-query";
import {
  applyPaneOutputEvent,
  getWsClient,
  type ConnectionState,
} from "@/lib/ws/client";

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
const MAX_STREAM_BUFFER_LINES = 2000;

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "Unexpected error";
}

export default function SessionsPage() {
  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
        Session Detail
      </h1>
      <Suspense
        fallback={
          <div className="flex items-center justify-center h-64">
            <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
          </div>
        }
      >
        <SessionDetail />
      </Suspense>
    </div>
  );
}

function SessionDetail() {
  const searchParams = useSearchParams();
  const sessionId = searchParams.get("session") || "";

  if (!sessionId) {
    return (
      <div className="rounded-lg border border-gray-200 bg-white p-6 text-sm text-gray-600 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-300">
        No session selected. Pick one from the{" "}
        <Link href="/" className="text-blue-600 dark:text-blue-400 hover:underline">
          sessions list
        </Link>
        .
      </div>
    );
  }

  return <SessionDetailView sessionId={sessionId} />;
}

function SessionDetailView({ sessionId }: { sessionId: string }) {
  const queryClient = useQueryClient();
  const { isConnected: providerConnected } = useConnection();
  const [wsClientState, setWsClientState] = useState<ConnectionState>(() =>
    getWsClient().getState()
  );
  const isConnected = providerConnected || wsClientState === "connected";

  const outputRef = useRef<HTMLDivElement | null>(null);
  const noticeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const wsFullSnapshotKeyRef = useRef<string | null>(null);

  const [selectedPane, setSelectedPane] = useState<number | null>(null);
  const [outputLines, setOutputLines] = useState(DEFAULT_OUTPUT_LINES);
  const [autoScroll, setAutoScroll] = useState(true);
  const [prompt, setPrompt] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);
  const [liveOutputState, setLiveOutputState] = useState<{
    key: string | null;
    lines: string[];
  }>({ key: null, lines: [] });

  const encodedSessionId = useMemo(
    () => encodeURIComponent(sessionId),
    [sessionId]
  );

  useEffect(() => {
    const ws = getWsClient();
    return ws.onStateChange((state) => {
      setWsClientState(state);
    });
  }, []);

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
    queryFn: () =>
      apiFetch<SessionResponse>(`/api/v1/sessions/${encodedSessionId}`),
    refetchInterval: isConnected ? false : 15000,
  });

  const panesQuery = useQuery({
    queryKey: ["session-panes", sessionId],
    queryFn: () =>
      apiFetch<PaneListResponse>(`/api/v1/sessions/${encodedSessionId}/panes`),
    refetchInterval: isConnected ? false : 5000,
  });

  const paneIndex = selectedPane ?? panesQuery.data?.panes?.[0]?.index ?? null;
  const activePaneKey =
    paneIndex !== null ? `${sessionId}:${paneIndex}` : null;

  const outputQuery = useQuery({
    queryKey: ["pane-output-backfill", sessionId, paneIndex, outputLines],
    queryFn: () => {
      const encodedPaneIndex = encodeURIComponent(String(paneIndex));
      return apiFetch<PaneOutputResponse>(
        `/api/v1/sessions/${encodedSessionId}/panes/${encodedPaneIndex}/output?lines=${outputLines}`
      );
    },
    enabled: paneIndex !== null,
    refetchInterval: isConnected ? false : 2500,
    refetchOnWindowFocus: !isConnected,
  });
  const refetchOutputBackfill = outputQuery.refetch;

  // Seed or refresh the output buffer whenever REST backfill completes.
  useEffect(() => {
    if (!activePaneKey || !outputQuery.data) {
      return;
    }
    const rawOutput = outputQuery.data.output || "";
    const backfillLines = rawOutput ? rawOutput.split("\n") : [];
    const maxBuffer = Math.max(outputLines, MAX_STREAM_BUFFER_LINES);

    setLiveOutputState((prev) => {
      if (prev.key !== activePaneKey) {
        return {
          key: activePaneKey,
          lines: backfillLines.slice(-maxBuffer),
        };
      }
      if (!isConnected) {
        return {
          key: activePaneKey,
          lines: backfillLines.slice(-maxBuffer),
        };
      }
      // If a full WS snapshot already arrived for this pane, keep live stream state.
      if (wsFullSnapshotKeyRef.current === activePaneKey && prev.lines.length > 0) {
        return prev;
      }
      // Merge initial REST backfill with any early incremental WS lines.
      if (prev.lines.length > 0) {
        const merged = [...backfillLines, ...prev.lines].slice(-maxBuffer);
        return { key: activePaneKey, lines: merged };
      }
      return {
        key: activePaneKey,
        lines: backfillLines.slice(-maxBuffer),
      };
    });
  }, [activePaneKey, isConnected, outputLines, outputQuery.data, outputQuery.dataUpdatedAt]);

  // Subscribe to WebSocket PaneStreamer topic panes:${sessionId}:${paneIndex}
  // for real-time output frames without 2.5s REST polling.
  useEffect(() => {
    if (paneIndex === null || !activePaneKey) {
      return;
    }

    const ws = getWsClient();
    const paneTopic = `panes:${sessionId}:${paneIndex}`;
    const maxBuffer = Math.max(outputLines, MAX_STREAM_BUFFER_LINES);
    wsFullSnapshotKeyRef.current = null;

    const unsubEvent = ws.onEvent((event) => {
      if (event.topic !== paneTopic) {
        return;
      }

      if (event.event_type === "pane.output") {
        const record =
          event.data && typeof event.data === "object"
            ? (event.data as { is_full?: boolean; full?: boolean })
            : null;
        if (record?.is_full === true || record?.full === true) {
          wsFullSnapshotKeyRef.current = activePaneKey;
        }
        setLiveOutputState((prev) => {
          const base = prev.key === activePaneKey ? prev.lines : [];
          return {
            key: activePaneKey,
            lines: applyPaneOutputEvent(base, event.data, maxBuffer),
          };
        });
      } else if (event.event_type === "pane.output.dropped") {
        void refetchOutputBackfill();
      }
    });

    void ws.subscribe(paneTopic, { mode: "lines" }).catch((err) => {
      if (process.env.NODE_ENV === "development") {
        console.error("[Sessions] WS pane subscribe error", paneTopic, err);
      }
    });

    return () => {
      unsubEvent();
      void ws.unsubscribe(paneTopic).catch(() => {});
    };
  }, [activePaneKey, outputLines, paneIndex, refetchOutputBackfill, sessionId]);

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

  const outputSnapshot = useMemo(() => {
    const fallbackRaw = outputQuery.data?.output || "";
    const fallbackLines = fallbackRaw ? fallbackRaw.split("\n") : [];
    const allLines =
      activePaneKey && liveOutputState.key === activePaneKey
        ? liveOutputState.lines
        : fallbackLines;
    if (allLines.length === 0) {
      return { lines: [] as string[], total: 0, truncated: false };
    }
    const truncated = allLines.length > outputLines;
    const lines = truncated ? allLines.slice(-outputLines) : allLines;
    return { lines, total: allLines.length, truncated };
  }, [activePaneKey, liveOutputState, outputLines, outputQuery.data]);

  useEffect(() => {
    if (!autoScroll || !outputRef.current) return;
    outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [autoScroll, outputSnapshot.lines]);

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

    const encodedPaneIndex = encodeURIComponent(String(paneIndex));
    try {
      await apiFetch<PaneInputResponse>(
        `/api/v1/sessions/${encodedSessionId}/panes/${encodedPaneIndex}/input`,
        {
          method: "POST",
          body: JSON.stringify({ text: prompt.trim(), enter: true }),
        }
      );

      if (process.env.NODE_ENV === "development") {
        console.log("[Sessions] send prompt", {
          sessionId,
          paneIndex,
          length: prompt.length,
        });
      }

      setPrompt("");
      if (!isConnected) {
        void queryClient.invalidateQueries({
          queryKey: ["pane-output-backfill", sessionId, paneIndex],
        });
      }
    } catch (error) {
      if (process.env.NODE_ENV === "development") {
        console.error("[Sessions] send prompt error", error);
      }
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    }
  }, [encodedSessionId, isConnected, prompt, paneIndex, queryClient, sessionId, setStatusNotice]);

  const interruptPane = useCallback(async () => {
    if (paneIndex === null) return;
    const encodedPaneIndex = encodeURIComponent(String(paneIndex));
    try {
      await apiFetch(
        `/api/v1/sessions/${encodedSessionId}/panes/${encodedPaneIndex}/interrupt`,
        { method: "POST" }
      );
      setStatusNotice({ type: "success", message: "Interrupt sent." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    }
  }, [encodedSessionId, paneIndex, setStatusNotice]);

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
              className="mt-4 h-[min(40rem,70vh)] overflow-y-auto rounded-md border border-gray-200 bg-gray-50 p-3 text-xs font-mono text-gray-700 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200"
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
