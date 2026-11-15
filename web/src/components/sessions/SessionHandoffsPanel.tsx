"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type components,
} from "@/lib/api/client";

type HandoffsEnvelopeResponse =
  components["schemas"]["HandoffsEnvelopeResponse"];
type CreateHandoffRequest = components["schemas"]["CreateHandoffRequest"];
type QualityReport = components["schemas"]["QualityReport"];

type PanelNotice = { type: "success" | "error"; message: string };

function qualityBadgeClass(status?: string): string {
  switch (status?.toLowerCase()) {
    case "ready":
      return "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200";
    case "needs_attention":
      return "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200";
    case "blocked":
      return "bg-rose-100 text-rose-800 dark:bg-rose-900/40 dark:text-rose-200";
    default:
      return "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300";
  }
}

export function SessionHandoffsPanel({
  sessionId,
  defaultTargetSession = "",
  onNotice,
}: {
  sessionId: string;
  defaultTargetSession?: string;
  onNotice?: (next: PanelNotice) => void;
}) {
  const queryClient = useQueryClient();
  const isCrossSession = !sessionId || sessionId === "all";
  const querySession = isCrossSession ? "all" : sessionId;

  const [targetSession, setTargetSession] =
    useState<string>(defaultTargetSession);
  const [goal, setGoal] = useState<string>("");
  const [now, setNow] = useState<string>("");
  const [description, setDescription] = useState<string>("");
  const [localNotice, setLocalNotice] = useState<PanelNotice | null>(null);

  const emitNotice = (next: PanelNotice) => {
    setLocalNotice(next);
    onNotice?.(next);
  };

  const handoffsQuery = useQuery({
    queryKey: ["session-handoffs", querySession],
    queryFn: () =>
      apiFetch<HandoffsEnvelopeResponse>(
        `/api/v1/handoffs?session=${encodeURIComponent(querySession)}&limit=10`
      ),
    retry: false,
  });

  const createHandoffMutation = useMutation({
    mutationFn: async (payload: CreateHandoffRequest) => {
      return apiFetch<HandoffsEnvelopeResponse>("/api/v1/handoffs", {
        method: "POST",
        body: JSON.stringify(payload),
      });
    },
    onSuccess: (res) => {
      setGoal("");
      setNow("");
      setDescription("");
      const qScore = res.quality?.score;
      emitNotice({
        type: "success",
        message: `Saved handoff for "${res.session}"${typeof qScore === "number" ? ` (quality ${qScore}/100 · ${res.quality?.status ?? "scored"})` : ""}.`,
      });
      void queryClient.invalidateQueries({ queryKey: ["session-handoffs"] });
    },
    onError: (err) => {
      emitNotice({
        type: "error",
        message: `Handoff creation failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const resolveEffectiveSession = (): string => {
    if (!isCrossSession) return sessionId.trim();
    const typed = targetSession.trim();
    if (typed) return typed;
    if (defaultTargetSession.trim()) return defaultTargetSession.trim();
    const firstKnown = handoffsQuery.data?.sessions?.[0]?.trim() || "";
    return firstKnown;
  };

  const handleCreateManual = (e: React.FormEvent) => {
    e.preventDefault();
    const sess = resolveEffectiveSession();
    if (!sess) {
      emitNotice({
        type: "error",
        message: "Target session name is required to create a handoff.",
      });
      return;
    }
    if (!goal.trim() || !now.trim()) {
      emitNotice({
        type: "error",
        message: "Both Goal and Now fields are required for a manual handoff.",
      });
      return;
    }
    createHandoffMutation.mutate({
      session: sess,
      goal: goal.trim(),
      now: now.trim(),
      description: description.trim() || undefined,
    });
  };

  const handleAutoGenerate = () => {
    const sess = resolveEffectiveSession();
    if (!sess) {
      emitNotice({
        type: "error",
        message: "Target session name is required to auto-generate a handoff.",
      });
      return;
    }
    createHandoffMutation.mutate({
      session: sess,
      auto: true,
    });
  };

  const latest = handoffsQuery.data?.latest;
  const quality: QualityReport | undefined =
    handoffsQuery.data?.quality ?? latest?.Quality;
  const ledger = handoffsQuery.data?.ledger;
  const handoffs = handoffsQuery.data?.handoffs ?? [];

  return (
    <section
      aria-label="Session Handoffs"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Session Handoffs &amp; Continuity
          </h2>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Structured Goal/Now handoff records with automatic quality scoring
            and continuity ledger status (`/api/v1/handoffs`).
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          {ledger && (
            <span
              className={`rounded-full px-2.5 py-0.5 font-medium ${
                ledger.exists
                  ? "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200"
                  : "bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300"
              }`}
            >
              {ledger.exists
                ? `Continuity Ledger (${ledger.bytes} B)`
                : "No Continuity Ledger"}
            </span>
          )}
          <span className="font-medium text-gray-500 dark:text-gray-400">
            {handoffs.length} handoff{handoffs.length === 1 ? "" : "s"}
          </span>
        </div>
      </div>

      {localNotice && !onNotice && (
        <div
          role={localNotice.type === "error" ? "alert" : "status"}
          className={`rounded-md border px-3 py-2 text-xs flex items-center justify-between gap-2 ${
            localNotice.type === "error"
              ? "border-red-200 bg-red-50 text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300"
              : "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300"
          }`}
        >
          <span>{localNotice.message}</span>
          <button
            type="button"
            onClick={() => setLocalNotice(null)}
            className="underline opacity-80 hover:opacity-100"
          >
            Dismiss
          </button>
        </div>
      )}

      {/* Latest Handoff Summary + Quality Score */}
      {latest && (
        <div
          data-testid="latest-handoff-card"
          className="rounded-md border border-blue-200/80 dark:border-blue-800/60 bg-blue-50/40 dark:bg-blue-900/15 p-4 space-y-2.5"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs font-semibold uppercase tracking-wide text-blue-700 dark:text-blue-300">
                Latest Handoff
              </span>
              {latest.Session && (
                <span className="rounded bg-white/80 dark:bg-gray-900/60 px-2 py-0.5 text-xs font-mono text-gray-800 dark:text-gray-200">
                  {latest.Session}
                </span>
              )}
              {latest.Status && (
                <span className="rounded bg-gray-200/80 dark:bg-gray-700 px-2 py-0.5 text-xs text-gray-800 dark:text-gray-200">
                  {latest.Status}
                </span>
              )}
            </div>

            {quality && (
              <div className="flex flex-wrap items-center gap-2">
                <span
                  data-testid="handoff-quality-badge"
                  className={`rounded-full px-2.5 py-0.5 text-xs font-semibold ${qualityBadgeClass(
                    quality.status
                  )}`}
                >
                  Quality {quality.score}/100 · {quality.status}
                </span>
              </div>
            )}
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-sm">
            <div className="rounded border border-gray-200/80 dark:border-gray-700 bg-white dark:bg-gray-900/60 p-3">
              <div className="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">
                Goal (Accomplished)
              </div>
              <p className="mt-1 text-gray-900 dark:text-white">
                {latest.Goal || "—"}
              </p>
            </div>
            <div className="rounded border border-gray-200/80 dark:border-gray-700 bg-white dark:bg-gray-900/60 p-3">
              <div className="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">
                Now (Next First Step)
              </div>
              <p className="mt-1 text-gray-900 dark:text-white">
                {latest.Now || "—"}
              </p>
            </div>
          </div>

          {quality?.dimensions && quality.dimensions.length > 0 && (
            <div className="flex flex-wrap gap-2 pt-1">
              {quality.dimensions.map((dim) => (
                <span
                  key={dim.name}
                  className="rounded border border-gray-200 dark:border-gray-700 bg-white/80 dark:bg-gray-900/50 px-2 py-0.5 text-xs font-mono text-gray-700 dark:text-gray-300"
                >
                  {dim.name}: {dim.score}
                </span>
              ))}
            </div>
          )}

          {quality?.hints && quality.hints.length > 0 && (
            <div className="text-xs text-amber-800 dark:text-amber-300">
              Quality hints: {quality.hints.join(" · ")}
            </div>
          )}
        </div>
      )}

      {/* Create / Auto-Generate Handoff Form */}
      <form
        onSubmit={handleCreateManual}
        aria-label="Create session handoff"
        className="space-y-3 rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-3.5"
      >
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          {isCrossSession && (
            <div>
              <label
                htmlFor="handoff-session-input"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Target Session
              </label>
              <input
                id="handoff-session-input"
                value={targetSession}
                onChange={(e) => setTargetSession(e.target.value)}
                placeholder="e.g. ntm-dev"
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
              />
            </div>
          )}

          <div>
            <label
              htmlFor="handoff-goal-input"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Goal (What was accomplished)
            </label>
            <input
              id="handoff-goal-input"
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="Implemented REST endpoints and OpenAPI schemas"
              className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
            />
          </div>

          <div>
            <label
              htmlFor="handoff-now-input"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Now (Immediate next task)
            </label>
            <input
              id="handoff-now-input"
              value={now}
              onChange={(e) => setNow(e.target.value)}
              placeholder="Run integration suite and verify browser cockpit"
              className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
            />
          </div>

          {!isCrossSession && (
            <div>
              <label
                htmlFor="handoff-desc-input"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Slug / Description (optional)
              </label>
              <input
                id="handoff-desc-input"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="wave9-checkpoint"
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
              />
            </div>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="submit"
            disabled={createHandoffMutation.isPending}
            className="rounded-md bg-blue-600 px-4 py-2 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {createHandoffMutation.isPending ? "Saving…" : "Save Handoff"}
          </button>
          <button
            type="button"
            disabled={createHandoffMutation.isPending}
            onClick={handleAutoGenerate}
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3.5 py-2 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
          >
            Auto-Generate Handoff
          </button>
        </div>
      </form>

      {handoffsQuery.isError && (
        <div className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300">
          {formatApiErrorMessage(handoffsQuery.error)}
        </div>
      )}

      {!handoffsQuery.isLoading &&
        !handoffsQuery.isError &&
        handoffs.length === 0 && (
          <div className="py-4 text-center text-sm text-gray-400">
            No handoffs recorded yet.
          </div>
        )}

      {handoffs.length > 0 && (
        <div className="space-y-2">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Recent Handoff History
          </div>
          {handoffs.map((item) => (
            <div
              key={item.path}
              className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/60 dark:bg-gray-900/40 px-3.5 py-2.5 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2 text-xs"
            >
              <div className="space-y-0.5 min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono font-semibold text-gray-800 dark:text-gray-200">
                    {item.session}
                  </span>
                  {item.status && (
                    <span className="rounded bg-gray-200 dark:bg-gray-700 px-1.5 py-0.5 text-gray-700 dark:text-gray-200">
                      {item.status}
                    </span>
                  )}
                  {item.is_auto && (
                    <span className="rounded bg-indigo-100 text-indigo-800 dark:bg-indigo-900/40 dark:text-indigo-200 px-1.5 py-0.5">
                      auto
                    </span>
                  )}
                  <span className="text-gray-400">{item.date}</span>
                </div>
                {item.goal && (
                  <p className="text-gray-700 dark:text-gray-300 truncate">
                    {item.goal}
                  </p>
                )}
              </div>
              <code className="text-[11px] font-mono text-gray-500 dark:text-gray-400 truncate max-w-xs">
                {item.path}
              </code>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
