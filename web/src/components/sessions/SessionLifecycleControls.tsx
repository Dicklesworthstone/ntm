"use client";

/**
 * SessionLifecycleControls
 *
 * Surfaces session-level health inspection, Ctrl+C interrupt, controller
 * agent launch, pane title renaming, and guarded session teardown on /sessions:
 *   - GET    /api/v1/sessions/{id}/health
 *   - POST   /api/v1/sessions/{id}/interrupt
 *   - POST   /api/v1/sessions/{id}/controller
 *   - PATCH  /api/v1/sessions/{sessionId}/panes/{paneIdx}/title
 *   - DELETE /api/v1/sessions/{id}
 */

import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface SessionAgentHealthItem {
  pane: number;
  pane_id?: string;
  agent_type: string;
  status: string;
  activity?: string;
  issues?: Array<{ type?: string; message?: string } | string>;
}

export interface SessionHealthResponse extends ApiEnvelope {
  session?: string;
  overall_status?: string;
  summary?: {
    total?: number;
    healthy?: number;
    warning?: number;
    error?: number;
  };
  agents?: SessionAgentHealthItem[];
}

interface ControllerSpawnResponse extends ApiEnvelope {
  session: string;
  pane_id: string;
  pane_index: number;
  agent_type: string;
}

export function SessionLifecycleControls({
  sessionId,
  onNotice,
}: {
  sessionId: string;
  onNotice?: (next: { type: "success" | "error"; message: string }) => void;
}) {
  const queryClient = useQueryClient();
  const encodedSession = encodeURIComponent(sessionId);

  const [healthRequested, setHealthRequested] = useState(false);
  const [controllerHarness, setControllerHarness] = useState<
    "cc" | "cod" | "agy"
  >("cc");
  const [paneIndexInput, setPaneIndexInput] = useState("1");
  const [paneTitleInput, setPaneTitleInput] = useState("");
  const [confirmKill, setConfirmKill] = useState(false);

  const healthQuery = useQuery({
    queryKey: ["session-health", sessionId],
    queryFn: () =>
      apiFetch<SessionHealthResponse>(
        `/api/v1/sessions/${encodedSession}/health`
      ),
    enabled: healthRequested && Boolean(sessionId),
    retry: false,
  });

  const interruptAllMutation = useMutation({
    mutationFn: () =>
      apiFetch<ApiEnvelope>(`/api/v1/sessions/${encodedSession}/interrupt`, {
        method: "POST",
        body: JSON.stringify({}),
      }),
    onSuccess: () => {
      onNotice?.({
        type: "success",
        message: `Sent Ctrl+C interrupt to agent panes in ${sessionId}.`,
      });
    },
    onError: (err) => {
      onNotice?.({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const launchControllerMutation = useMutation({
    mutationFn: () =>
      apiFetch<ControllerSpawnResponse>(
        `/api/v1/sessions/${encodedSession}/controller`,
        {
          method: "POST",
          body: JSON.stringify({ agent_type: controllerHarness }),
        }
      ),
    onSuccess: (res) => {
      onNotice?.({
        type: "success",
        message: `Launched ${res.agent_type || controllerHarness} controller in pane ${res.pane_id || res.pane_index}.`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["sessions", sessionId, "panes"],
      });
    },
    onError: (err) => {
      onNotice?.({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const renamePaneMutation = useMutation({
    mutationFn: ({ paneIdx, title }: { paneIdx: number; title: string }) =>
      apiFetch<ApiEnvelope>(
        `/api/v1/sessions/${encodedSession}/panes/${paneIdx}/title`,
        {
          method: "PATCH",
          body: JSON.stringify({ title }),
        }
      ),
    onSuccess: (_res, vars) => {
      setPaneTitleInput("");
      onNotice?.({
        type: "success",
        message: `Updated pane ${vars.paneIdx} title to "${vars.title}".`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["sessions", sessionId, "panes"],
      });
    },
    onError: (err) => {
      onNotice?.({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const killSessionMutation = useMutation({
    mutationFn: () =>
      apiFetch<ApiEnvelope>(
        `/api/v1/sessions/${encodedSession}?force=true`,
        {
          method: "DELETE",
        }
      ),
    onSuccess: () => {
      setConfirmKill(false);
      onNotice?.({
        type: "success",
        message: `Terminated session ${sessionId}.`,
      });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (err) => {
      onNotice?.({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const handleRenameSubmit = (e: FormEvent) => {
    e.preventDefault();
    const parsedIdx = Number.parseInt(paneIndexInput.trim(), 10);
    const trimmedTitle = paneTitleInput.trim();
    if (Number.isNaN(parsedIdx) || parsedIdx < 0 || !trimmedTitle) return;
    renamePaneMutation.mutate({ paneIdx: parsedIdx, title: trimmedTitle });
  };

  const healthAgents = healthQuery.data?.agents ?? [];

  return (
    <section
      aria-label="Session Lifecycle, Controller & Health Controls"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4"
    >
      <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Session Health, Controller &amp; Lifecycle Controls
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Check per-pane agent health, launch a dedicated coordinator pane, rename panes, interrupt agents, or tear down the session.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={!sessionId || healthQuery.isFetching}
            onClick={() => {
              if (healthRequested) {
                void healthQuery.refetch();
              } else {
                setHealthRequested(true);
              }
            }}
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
          >
            {healthQuery.isFetching ? "Checking…" : "Check Session Health"}
          </button>

          <div className="flex items-center gap-1">
            <select
              aria-label="Controller Harness"
              value={controllerHarness}
              onChange={(e) =>
                setControllerHarness(e.target.value as "cc" | "cod" | "agy")
              }
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1.5 text-xs text-gray-900 dark:text-white"
            >
              <option value="cc">cc</option>
              <option value="cod">cod</option>
              <option value="agy">agy</option>
            </select>
            <button
              type="button"
              disabled={!sessionId || launchControllerMutation.isPending}
              onClick={() => launchControllerMutation.mutate()}
              className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {launchControllerMutation.isPending
                ? "Launching…"
                : "Launch Controller"}
            </button>
          </div>

          <button
            type="button"
            disabled={!sessionId || interruptAllMutation.isPending}
            onClick={() => interruptAllMutation.mutate()}
            className="rounded-md border border-amber-300 dark:border-amber-700 bg-amber-50 dark:bg-amber-900/20 px-3 py-1.5 text-xs font-medium text-amber-800 dark:text-amber-200 hover:bg-amber-100 dark:hover:bg-amber-900/40 disabled:opacity-50"
          >
            Interrupt All Agents
          </button>

          {!confirmKill ? (
            <button
              type="button"
              disabled={!sessionId}
              onClick={() => setConfirmKill(true)}
              className="rounded-md border border-red-300 dark:border-red-800 px-3 py-1.5 text-xs font-medium text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
            >
              Kill Session…
            </button>
          ) : (
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                disabled={killSessionMutation.isPending}
                onClick={() => killSessionMutation.mutate()}
                className="rounded-md bg-red-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-red-700 disabled:opacity-50"
              >
                {killSessionMutation.isPending
                  ? "Killing…"
                  : "Confirm Kill Session"}
              </button>
              <button
                type="button"
                onClick={() => setConfirmKill(false)}
                className="text-xs text-gray-500 hover:underline"
              >
                Cancel
              </button>
            </div>
          )}
        </div>
      </div>

      {/* Rename Pane Title inline form */}
      <form
        onSubmit={handleRenameSubmit}
        className="flex flex-wrap items-center gap-2 pt-2 border-t border-gray-200 dark:border-gray-700"
      >
        <span className="text-xs text-gray-600 dark:text-gray-400">
          Rename Pane:
        </span>
        <input
          aria-label="Pane Index to Rename"
          value={paneIndexInput}
          onChange={(e) => setPaneIndexInput(e.target.value)}
          className="w-16 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs font-mono text-gray-900 dark:text-white"
        />
        <input
          aria-label="New Pane Title"
          value={paneTitleInput}
          onChange={(e) => setPaneTitleInput(e.target.value)}
          placeholder="e.g. backend-auditor"
          className="flex-1 min-w-[160px] rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1 text-xs font-mono text-gray-900 dark:text-white"
        />
        <button
          type="submit"
          disabled={!paneTitleInput.trim() || renamePaneMutation.isPending}
          className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
        >
          Set Pane Title
        </button>
      </form>

      {healthQuery.isError && (
        <div className="text-xs text-red-600 dark:text-red-400">
          {formatApiErrorMessage(healthQuery.error)}
        </div>
      )}

      {healthRequested && healthQuery.data && (
        <div
          data-testid="session-health-report"
          className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-3 space-y-2 text-xs"
        >
          <div className="flex flex-wrap items-center gap-3 font-medium">
            <span>
              Overall:{" "}
              <strong className="uppercase font-mono">
                {healthQuery.data.overall_status || "ok"}
              </strong>
            </span>
            {healthQuery.data.summary && (
              <span className="text-gray-600 dark:text-gray-300 font-mono">
                healthy={healthQuery.data.summary.healthy ?? 0} · warning=
                {healthQuery.data.summary.warning ?? 0} · error=
                {healthQuery.data.summary.error ?? 0}
              </span>
            )}
          </div>
          {healthAgents.length > 0 && (
            <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {healthAgents.map((ag) => (
                <div
                  key={`${ag.pane}-${ag.pane_id ?? ""}`}
                  className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-2.5 py-1.5 font-mono flex items-center justify-between"
                >
                  <span>
                    pane {ag.pane} ({ag.agent_type})
                  </span>
                  <span className="uppercase font-semibold">{ag.status}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
