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
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
  type components,
} from "@/lib/api/client";
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

      <SessionCheckpointsSection
        sessionId={sessionId}
        encodedSessionId={encodedSessionId}
        onNotice={setStatusNotice}
      />
    </div>
  );
}

type CheckpointsListResponse =
  components["schemas"]["CheckpointsListEnvelopeResponse"];
type CheckpointEnvelopeResponse =
  components["schemas"]["CheckpointEnvelopeResponse"];
type VerifyCheckpointResponse =
  components["schemas"]["VerifyCheckpointResponse"];
type RestoreCheckpointResponse =
  components["schemas"]["RestoreCheckpointResponse"];
type DeleteCheckpointResponse =
  components["schemas"]["DeleteCheckpointEnvelopeResponse"];

function createCheckpointIdempotencyKey(prefix: string): string {
  const randomPart =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  return `${prefix}-${randomPart}`;
}

function SessionCheckpointsSection({
  sessionId,
  encodedSessionId,
  onNotice,
}: {
  sessionId: string;
  encodedSessionId: string;
  onNotice: (next: Notice) => void;
}) {
  const queryClient = useQueryClient();
  const [cpName, setCpName] = useState("");
  const [cpDescription, setCpDescription] = useState("");
  const [verifyById, setVerifyById] = useState<
    Record<string, VerifyCheckpointResponse>
  >({});
  const [verifyingId, setVerifyingId] = useState<string | null>(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);

  const checkpointsQuery = useQuery({
    queryKey: ["session-checkpoints", sessionId],
    queryFn: () =>
      apiFetch<CheckpointsListResponse>(
        `/api/v1/sessions/${encodedSessionId}/checkpoints`
      ),
  });

  const createCheckpointMutation = useMutation({
    mutationFn: (payload: { name?: string; description?: string }) =>
      apiFetch<CheckpointEnvelopeResponse>(
        `/api/v1/sessions/${encodedSessionId}/checkpoints`,
        {
          method: "POST",
          headers: {
            "Idempotency-Key": createCheckpointIdempotencyKey("cp-create"),
          },
          body: JSON.stringify(payload),
        }
      ),
    onSuccess: (res) => {
      setCpName("");
      setCpDescription("");
      onNotice({
        type: "success",
        message: `Saved checkpoint "${res.checkpoint.name || res.checkpoint.id}".`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["session-checkpoints", sessionId],
      });
    },
    onError: (err) => {
      onNotice({
        type: "error",
        message: `Save checkpoint failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const restoreCheckpointMutation = useMutation({
    mutationFn: (checkpointId: string) =>
      apiFetch<RestoreCheckpointResponse>(
        `/api/v1/sessions/${encodedSessionId}/checkpoints/${encodeURIComponent(checkpointId)}/restore`,
        {
          method: "POST",
          headers: {
            "Idempotency-Key": createCheckpointIdempotencyKey("cp-restore"),
          },
          body: JSON.stringify({ force: true }),
        }
      ),
    onSuccess: (res, cpId) => {
      onNotice({
        type: "success",
        message: `Restored checkpoint "${cpId}" (${res.panes_restored} pane(s) restored).`,
      });
      void queryClient.invalidateQueries({ queryKey: ["sessions", sessionId] });
      void queryClient.invalidateQueries({
        queryKey: ["session-panes", sessionId],
      });
    },
    onError: (err) => {
      onNotice({
        type: "error",
        message: `Restore failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const deleteCheckpointMutation = useMutation({
    mutationFn: (checkpointId: string) =>
      apiFetch<DeleteCheckpointResponse>(
        `/api/v1/sessions/${encodedSessionId}/checkpoints/${encodeURIComponent(checkpointId)}`,
        {
          method: "DELETE",
          headers: {
            "Idempotency-Key": createCheckpointIdempotencyKey("cp-delete"),
          },
        }
      ),
    onSuccess: (_res, cpId) => {
      setConfirmDeleteId((prev) => (prev === cpId ? null : prev));
      setVerifyById((prev) => {
        const next = { ...prev };
        delete next[cpId];
        return next;
      });
      onNotice({
        type: "success",
        message: `Deleted checkpoint "${cpId}".`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["session-checkpoints", sessionId],
      });
    },
    onError: (err) => {
      onNotice({
        type: "error",
        message: `Delete failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const handleSaveCheckpoint = (event: React.FormEvent) => {
    event.preventDefault();
    const trimmedName = cpName.trim();
    const trimmedDesc = cpDescription.trim();
    createCheckpointMutation.mutate({
      ...(trimmedName ? { name: trimmedName } : {}),
      ...(trimmedDesc ? { description: trimmedDesc } : {}),
    });
  };

  const handleVerifyIntegrity = async (checkpointId: string) => {
    setVerifyingId(checkpointId);
    try {
      const result = await apiFetch<VerifyCheckpointResponse>(
        `/api/v1/sessions/${encodedSessionId}/checkpoints/${encodeURIComponent(checkpointId)}/verify`
      );
      setVerifyById((prev) => ({ ...prev, [checkpointId]: result }));
    } catch (err) {
      onNotice({
        type: "error",
        message: `Integrity check failed: ${formatApiErrorMessage(err)}`,
      });
    } finally {
      setVerifyingId((prev) => (prev === checkpointId ? null : prev));
    }
  };

  const checkpoints = checkpointsQuery.data?.checkpoints ?? [];

  return (
    <section className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Session Checkpoints
          </h2>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Capture pane scrollback & Git state snapshots, verify archive
            integrity, or restore checkpoints.
          </p>
        </div>
        <span className="text-xs font-medium text-gray-500 dark:text-gray-400">
          {checkpoints.length} checkpoint{checkpoints.length === 1 ? "" : "s"}
        </span>
      </div>

      {/* Save Checkpoint Form */}
      <form
        onSubmit={handleSaveCheckpoint}
        aria-label="Save session checkpoint"
        className="grid grid-cols-1 sm:grid-cols-3 gap-3 items-end"
      >
        <div>
          <label
            htmlFor="cp-save-name"
            className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
          >
            Checkpoint Name
          </label>
          <input
            id="cp-save-name"
            value={cpName}
            onChange={(e) => setCpName(e.target.value)}
            placeholder="e.g. pre-refactor-snapshot"
            className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
          />
        </div>
        <div>
          <label
            htmlFor="cp-save-description"
            className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
          >
            Description (optional)
          </label>
          <input
            id="cp-save-description"
            value={cpDescription}
            onChange={(e) => setCpDescription(e.target.value)}
            placeholder="Notes about current swarm state..."
            className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
          />
        </div>
        <div>
          <button
            type="submit"
            disabled={createCheckpointMutation.isPending}
            className="w-full rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {createCheckpointMutation.isPending
              ? "Saving…"
              : "Save Checkpoint"}
          </button>
        </div>
      </form>

      {checkpointsQuery.error && (
        <div className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
          {formatApiErrorMessage(checkpointsQuery.error)}
        </div>
      )}

      {!checkpointsQuery.isLoading &&
        !checkpointsQuery.error &&
        checkpoints.length === 0 && (
          <div className="py-6 text-center text-sm text-gray-400">
            No checkpoints saved for this session yet.
          </div>
        )}

      {checkpoints.length > 0 && (
        <div className="space-y-3">
          {checkpoints.map((cp) => {
            const verification = verifyById[cp.id];
            const isVerifying = verifyingId === cp.id;
            const isConfirmingDelete = confirmDeleteId === cp.id;
            const isRestoring =
              restoreCheckpointMutation.isPending &&
              restoreCheckpointMutation.variables === cp.id;
            const isDeleting =
              deleteCheckpointMutation.isPending &&
              deleteCheckpointMutation.variables === cp.id;

            return (
              <div
                key={cp.id}
                className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/60 dark:bg-gray-900/40 p-3.5 space-y-2.5"
              >
                <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-semibold text-sm text-gray-900 dark:text-white">
                        {cp.name || cp.id}
                      </span>
                      <span className="text-xs font-mono text-gray-500 dark:text-gray-400">
                        {cp.id}
                      </span>
                      <span className="rounded-full bg-gray-200 dark:bg-gray-700 px-2 py-0.5 text-xs text-gray-700 dark:text-gray-200">
                        {cp.pane_count} pane{cp.pane_count === 1 ? "" : "s"}
                      </span>
                      {cp.git?.branch && (
                        <span className="rounded-full bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200 px-2 py-0.5 text-xs font-mono">
                          {cp.git.branch}
                          {cp.git.commit ? `@${cp.git.commit.slice(0, 7)}` : ""}
                        </span>
                      )}
                    </div>
                    {cp.description && (
                      <p className="mt-1 text-xs text-gray-600 dark:text-gray-300">
                        {cp.description}
                      </p>
                    )}
                    <div className="mt-1 text-xs text-gray-400 dark:text-gray-500">
                      {cp.age ||
                        (cp.created_at
                          ? new Date(cp.created_at).toLocaleString()
                          : "—")}
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center gap-2 shrink-0">
                    <button
                      type="button"
                      disabled={isVerifying}
                      aria-label={`Verify Integrity ${cp.id}`}
                      onClick={() => void handleVerifyIntegrity(cp.id)}
                      className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
                    >
                      {isVerifying ? "Verifying…" : "Verify Integrity"}
                    </button>

                    <button
                      type="button"
                      disabled={isRestoring}
                      aria-label={`Restore ${cp.id}`}
                      onClick={() => restoreCheckpointMutation.mutate(cp.id)}
                      className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
                    >
                      {isRestoring ? "Restoring…" : "Restore"}
                    </button>

                    {!isConfirmingDelete ? (
                      <button
                        type="button"
                        aria-label={`Delete ${cp.id}`}
                        onClick={() => setConfirmDeleteId(cp.id)}
                        className="rounded-md border border-red-300 dark:border-red-700 px-3 py-1.5 text-xs font-medium text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20"
                      >
                        Delete
                      </button>
                    ) : (
                      <div className="flex items-center gap-2 rounded-md border border-red-300 dark:border-red-700 bg-red-50 dark:bg-red-900/20 px-2.5 py-1">
                        <button
                          type="button"
                          disabled={isDeleting}
                          aria-label={`Confirm delete ${cp.id}`}
                          onClick={() => deleteCheckpointMutation.mutate(cp.id)}
                          className="rounded bg-red-600 px-2 py-0.5 text-xs font-medium text-white hover:bg-red-700 disabled:opacity-50"
                        >
                          {isDeleting ? "Deleting…" : "Confirm Delete"}
                        </button>
                        <button
                          type="button"
                          onClick={() => setConfirmDeleteId(null)}
                          className="text-xs text-gray-600 dark:text-gray-300 hover:underline"
                        >
                          Cancel
                        </button>
                      </div>
                    )}
                  </div>
                </div>

                {/* Inline Integrity Verification Status */}
                {verification && (
                  <div
                    className={`rounded-md border px-3 py-2 text-xs space-y-1.5 ${
                      verification.valid
                        ? "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
                        : "border-red-200 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200"
                    }`}
                  >
                    <div className="flex flex-wrap items-center gap-3 font-medium">
                      <span>
                        {verification.valid
                          ? "Integrity Verified (SHA256 & Archive Valid)"
                          : "Integrity Verification Failed"}
                      </span>
                      <span className="font-mono text-[11px] opacity-90">
                        schema={verification.schema_valid ? "ok" : "fail"} ·
                        files={verification.files_present ? "ok" : "fail"} ·
                        consistency=
                        {verification.consistency_valid ? "ok" : "fail"}
                      </span>
                    </div>
                    {verification.details &&
                      Object.keys(verification.details).length > 0 && (
                        <div className="flex flex-wrap gap-x-3 gap-y-1 font-mono text-[11px] opacity-90">
                          {Object.entries(verification.details).map(
                            ([k, v]) => (
                              <span key={k}>
                                {k}: {v}
                              </span>
                            )
                          )}
                        </div>
                      )}
                    {verification.errors && verification.errors.length > 0 && (
                      <div>Errors: {verification.errors.join("; ")}</div>
                    )}
                    {verification.warnings &&
                      verification.warnings.length > 0 && (
                        <div>Warnings: {verification.warnings.join("; ")}</div>
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
