"use client";

/**
 * MemoryWorkbenchPanel
 *
 * Surfaces the four previously unwired CASS Memory (cm) & CASS Insights
 * endpoints on the /memory cockpit page:
 *   POST /api/v1/memory/context
 *   POST /api/v1/memory/outcome
 *   GET  /api/v1/memory/privacy
 *   PUT  /api/v1/memory/privacy
 *   GET  /api/v1/cass/insights
 */

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface MemoryWorkbenchRule {
  id: string;
  content: string;
  category?: string;
}

export interface MemoryHistorySnippet {
  source_path: string;
  line_number?: number;
  agent?: string;
  workspace?: string;
  title?: string;
  snippet: string;
  score?: number;
}

export interface MemoryContextResponse extends ApiEnvelope {
  task: string;
  relevant_rules: MemoryWorkbenchRule[];
  anti_patterns: MemoryWorkbenchRule[];
  history_snippets: MemoryHistorySnippet[];
  suggested_queries?: string[];
  source: string;
}

export interface MemoryOutcomeResponse extends ApiEnvelope {
  recorded: boolean;
  status: string;
}

export interface CASSInsightBucket {
  key: string;
  doc_count: number;
}

export interface CASSInsightsResponse extends ApiEnvelope {
  total_documents: number;
  agents: CASSInsightBucket[];
  workspaces: CASSInsightBucket[];
  warnings?: string[];
}

export type MemoryPrivacyResponse = ApiEnvelope & Record<string, unknown>;

function createWorkbenchIdempotencyKey(prefix: string): string {
  const randomPart =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  return `${prefix}-${randomPart}`;
}

export interface MemoryWorkbenchPanelProps {
  onApplySuggestedQuery?: (query: string) => void;
  onNotice?: (notice: { kind: "success" | "error"; text: string }) => void;
}

export function MemoryWorkbenchPanel({
  onApplySuggestedQuery,
  onNotice,
}: MemoryWorkbenchPanelProps) {
  // 1. Task Recovery Context state
  const [taskInput, setTaskInput] = useState("");
  const [workspaceInput, setWorkspaceInput] = useState("");
  const [maxRules, setMaxRules] = useState<number>(10);
  const [maxSnippets, setMaxSnippets] = useState<number>(5);
  const [contextResult, setContextResult] =
    useState<MemoryContextResponse | null>(null);
  const [contextError, setContextError] = useState<string | null>(null);

  // 2. Task Outcome Recorder state
  const [outcomeStatus, setOutcomeStatus] = useState<
    "success" | "partial" | "failure"
  >("success");
  const [outcomeRuleIdsInput, setOutcomeRuleIdsInput] = useState("");
  const [outcomeSentiment, setOutcomeSentiment] = useState("");
  const [outcomeNotes, setOutcomeNotes] = useState("");
  const [outcomeResult, setOutcomeResult] =
    useState<MemoryOutcomeResponse | null>(null);
  const [outcomeError, setOutcomeError] = useState<string | null>(null);

  // 3. On-demand CASS Insights, Capabilities, Timeline, Preview & CM Privacy triggers
  const [insightsRequested, setInsightsRequested] = useState(false);
  const [cassCapsRequested, setCassCapsRequested] = useState(false);
  const [cassTimelineSince, setCassTimelineSince] = useState("7d");
  const [cassTimelineRequested, setCassTimelineRequested] = useState(false);
  const [cassPreviewPath, setCassPreviewPath] = useState("");
  const [cassPreviewData, setCassPreviewData] = useState<string | null>(null);
  const [cassPreviewError, setCassPreviewError] = useState<string | null>(null);
  const [privacyRequested, setPrivacyRequested] = useState(false);
  const [privacyEnabled, setPrivacyEnabled] = useState(true);
  const [privacyAgentsInput, setPrivacyAgentsInput] = useState("");
  const [privacySaveMessage, setPrivacySaveMessage] = useState<string | null>(
    null
  );
  const [privacySaveError, setPrivacySaveError] = useState<string | null>(null);

  const insightsQuery = useQuery({
    queryKey: ["memory", "cass-insights"],
    queryFn: () => apiFetch<CASSInsightsResponse>("/api/v1/cass/insights"),
    enabled: insightsRequested,
    retry: false,
  });

  const cassCapsQuery = useQuery({
    queryKey: ["memory", "cass-capabilities"],
    queryFn: () =>
      apiFetch<ApiEnvelope & Record<string, unknown>>(
        "/api/v1/cass/capabilities"
      ),
    enabled: cassCapsRequested,
    retry: false,
  });

  const cassTimelineQuery = useQuery({
    queryKey: ["memory", "cass-timeline", cassTimelineSince.trim()],
    queryFn: () =>
      apiFetch<
        ApiEnvelope & {
          entries?: Array<{
            timestamp?: string;
            agent?: string;
            workspace?: string;
            summary?: string;
            source_path?: string;
          }>;
          count?: number;
        }
      >(
        `/api/v1/cass/timeline?since=${encodeURIComponent(
          cassTimelineSince.trim() || "7d"
        )}&limit=15`
      ),
    enabled: cassTimelineRequested,
    retry: false,
  });

  const cassPreviewMutation = useMutation({
    mutationFn: (docPath: string) =>
      apiFetch<
        ApiEnvelope & { path?: string; content?: string; preview?: string }
      >(`/api/v1/cass/preview?path=${encodeURIComponent(docPath)}`),
    onSuccess: (res) => {
      setCassPreviewData(
        res.content || res.preview || JSON.stringify(res, null, 2)
      );
      setCassPreviewError(null);
    },
    onError: (err) => {
      setCassPreviewData(null);
      setCassPreviewError(formatApiErrorMessage(err));
    },
  });

  const privacyQuery = useQuery({
    queryKey: ["memory", "privacy"],
    queryFn: async () => {
      const res = await apiFetch<MemoryPrivacyResponse>(
        "/api/v1/memory/privacy"
      );
      if (typeof res.enabled === "boolean") {
        setPrivacyEnabled(res.enabled);
      }
      if (Array.isArray(res.agents)) {
        setPrivacyAgentsInput(
          res.agents.filter((a): a is string => typeof a === "string").join(", ")
        );
      }
      return res;
    },
    enabled: privacyRequested,
    retry: false,
  });

  const contextMutation = useMutation({
    mutationFn: (payload: {
      task: string;
      workspace?: string;
      max_rules: number;
      max_snippets: number;
    }) =>
      apiFetch<MemoryContextResponse>("/api/v1/memory/context", {
        method: "POST",
        body: JSON.stringify(payload),
      }),
    onSuccess: (res) => {
      setContextError(null);
      setContextResult(res);
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err);
      setContextError(msg);
      onNotice?.({
        kind: "error",
        text: `Memory context lookup failed: ${msg}`,
      });
    },
  });

  const outcomeMutation = useMutation({
    mutationFn: (payload: {
      status: "success" | "partial" | "failure";
      rule_ids?: string[];
      sentiment?: string;
      notes?: string;
    }) =>
      apiFetch<MemoryOutcomeResponse>("/api/v1/memory/outcome", {
        method: "POST",
        headers: {
          "Idempotency-Key": createWorkbenchIdempotencyKey("cm-outcome"),
        },
        body: JSON.stringify(payload),
      }),
    onSuccess: (res) => {
      setOutcomeError(null);
      setOutcomeResult(res);
      setOutcomeNotes("");
      onNotice?.({
        kind: "success",
        text: `Recorded task outcome (${res.status}).`,
      });
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err);
      setOutcomeError(msg);
      onNotice?.({
        kind: "error",
        text: `Recording outcome failed: ${msg}`,
      });
    },
  });

  const updatePrivacyMutation = useMutation({
    mutationFn: (payload: { enabled: boolean; agents?: string[] }) =>
      apiFetch<MemoryPrivacyResponse>("/api/v1/memory/privacy", {
        method: "PUT",
        headers: {
          "Idempotency-Key": createWorkbenchIdempotencyKey("cm-privacy"),
        },
        body: JSON.stringify(payload),
      }),
    onSuccess: (res) => {
      setPrivacySaveError(null);
      const summary = `Updated cross-agent privacy policy (enabled=${String(
        res.enabled ?? privacyEnabled
      )}).`;
      setPrivacySaveMessage(summary);
      onNotice?.({ kind: "success", text: summary });
      if (privacyRequested) {
        void privacyQuery.refetch();
      }
    },
    onError: (err) => {
      const msg = formatApiErrorMessage(err);
      setPrivacySaveMessage(null);
      setPrivacySaveError(msg);
      onNotice?.({
        kind: "error",
        text: `Privacy policy update failed: ${msg}`,
      });
    },
  });

  const selectedRuleIds = outcomeRuleIdsInput
    .split(",")
    .map((id) => id.trim())
    .filter(Boolean);

  const toggleRuleForOutcome = (ruleId: string) => {
    const set = new Set(selectedRuleIds);
    if (set.has(ruleId)) {
      set.delete(ruleId);
    } else {
      set.add(ruleId);
    }
    setOutcomeRuleIdsInput(Array.from(set).join(", "));
  };

  const handleContextSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    const trimmedTask = taskInput.trim();
    if (!trimmedTask) {
      setContextError("Task description is required.");
      return;
    }
    contextMutation.mutate({
      task: trimmedTask,
      ...(workspaceInput.trim() ? { workspace: workspaceInput.trim() } : {}),
      max_rules: maxRules,
      max_snippets: maxSnippets,
    });
  };

  const handleOutcomeSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    outcomeMutation.mutate({
      status: outcomeStatus,
      ...(selectedRuleIds.length > 0 ? { rule_ids: selectedRuleIds } : {}),
      ...(outcomeSentiment.trim()
        ? { sentiment: outcomeSentiment.trim() }
        : {}),
      ...(outcomeNotes.trim() ? { notes: outcomeNotes.trim() } : {}),
    });
  };

  const handlePrivacySubmit = (event: React.FormEvent) => {
    event.preventDefault();
    const agents = privacyAgentsInput
      .split(",")
      .map((a) => a.trim())
      .filter(Boolean);
    updatePrivacyMutation.mutate({
      enabled: privacyEnabled,
      ...(agents.length > 0 ? { agents } : {}),
    });
  };

  const privacyMetadataEntries = privacyQuery.data
    ? Object.entries(privacyQuery.data).filter(
        ([key]) =>
          key !== "success" && key !== "timestamp" && key !== "request_id"
      )
    : [];

  return (
    <section
      aria-label="Memory Recovery & Governance Workbench"
      className="space-y-6"
    >
      {/* Row 1: Task Recovery Context Builder + Task Outcome Feedback */}
      <div className="grid gap-6 lg:grid-cols-2">
        {/* Task Recovery Context Builder */}
        <section
          aria-label="Task Recovery Context Builder"
          className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
        >
          <div className="flex items-start justify-between gap-2">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Task Recovery Context
              </h2>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                Synthesize relevant procedural rules, anti-patterns, and past
                session snippets for an upcoming task.
              </p>
            </div>
            {contextResult?.source && (
              <span
                data-testid="memory-context-source"
                className="rounded-full bg-indigo-100 text-indigo-800 dark:bg-indigo-900/40 dark:text-indigo-200 px-2.5 py-0.5 text-xs font-mono"
              >
                source: {contextResult.source}
              </span>
            )}
          </div>

          <form onSubmit={handleContextSubmit} className="space-y-3">
            <div>
              <label
                htmlFor="memory-context-task"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Task Description
              </label>
              <input
                id="memory-context-task"
                value={taskInput}
                onChange={(e) => setTaskInput(e.target.value)}
                placeholder="e.g. Wire OpenAPI 3.1 drift gate for /api/v1/analytics"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
              />
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <label
                  htmlFor="memory-context-workspace"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Workspace (optional)
                </label>
                <input
                  id="memory-context-workspace"
                  value={workspaceInput}
                  onChange={(e) => setWorkspaceInput(e.target.value)}
                  placeholder="/data/projects/ntm"
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
                />
              </div>
              <div>
                <label
                  htmlFor="memory-context-max-rules"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Max Rules
                </label>
                <input
                  id="memory-context-max-rules"
                  type="number"
                  min={1}
                  max={50}
                  value={maxRules}
                  onChange={(e) =>
                    setMaxRules(Math.max(1, Number(e.target.value) || 10))
                  }
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
                />
              </div>
              <div>
                <label
                  htmlFor="memory-context-max-snippets"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Max Snippets
                </label>
                <input
                  id="memory-context-max-snippets"
                  type="number"
                  min={1}
                  max={25}
                  value={maxSnippets}
                  onChange={(e) =>
                    setMaxSnippets(Math.max(1, Number(e.target.value) || 5))
                  }
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
                />
              </div>
            </div>

            <button
              type="submit"
              disabled={contextMutation.isPending}
              className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {contextMutation.isPending
                ? "Synthesizing Context…"
                : "Fetch Recovery Context"}
            </button>
          </form>

          {contextError && (
            <div className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
              {contextError}
            </div>
          )}

          {contextResult && (
            <div className="space-y-3 pt-2 border-t border-gray-200 dark:border-gray-700">
              <div className="space-y-1.5">
                <div className="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">
                  Relevant Rules ({contextResult.relevant_rules?.length ?? 0})
                </div>
                {(contextResult.relevant_rules ?? []).length === 0 ? (
                  <p className="text-xs text-gray-400">
                    No matching procedural rules returned.
                  </p>
                ) : (
                  <ul className="space-y-1.5">
                    {contextResult.relevant_rules.map((rule) => {
                      const checked = selectedRuleIds.includes(rule.id);
                      return (
                        <li
                          key={rule.id}
                          className="rounded border border-gray-200 dark:border-gray-700 p-2.5 text-xs text-gray-800 dark:text-gray-200 flex items-start justify-between gap-2"
                        >
                          <label className="flex items-start gap-2 cursor-pointer">
                            <input
                              type="checkbox"
                              checked={checked}
                              onChange={() => toggleRuleForOutcome(rule.id)}
                              aria-label={`Select rule ${rule.id} for outcome`}
                              className="mt-0.5 h-3.5 w-3.5"
                            />
                            <span>
                              <span className="font-mono font-semibold text-blue-600 dark:text-blue-400 mr-1.5">
                                [{rule.id}]
                              </span>
                              {rule.content}
                            </span>
                          </label>
                          {rule.category && (
                            <span className="shrink-0 rounded-full bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300 px-2 py-0.5 text-[11px]">
                              {rule.category}
                            </span>
                          )}
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>

              {(contextResult.anti_patterns ?? []).length > 0 && (
                <div className="space-y-1.5">
                  <div className="text-xs font-semibold uppercase tracking-wide text-red-600 dark:text-red-400">
                    Anti-Patterns ({contextResult.anti_patterns.length})
                  </div>
                  <ul className="space-y-1.5">
                    {contextResult.anti_patterns.map((ap) => (
                      <li
                        key={ap.id}
                        className="rounded border border-red-200 dark:border-red-800/60 bg-red-50/40 dark:bg-red-900/10 p-2 text-xs text-red-800 dark:text-red-200"
                      >
                        <span className="font-mono font-semibold mr-1.5">
                          [{ap.id}]
                        </span>
                        {ap.content}
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              {(contextResult.history_snippets ?? []).length > 0 && (
                <div className="space-y-1.5">
                  <div className="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    History Snippets ({contextResult.history_snippets.length})
                  </div>
                  <div className="space-y-1.5">
                    {contextResult.history_snippets.map((sn, idx) => (
                      <div
                        key={`${sn.source_path}-${sn.line_number ?? idx}`}
                        className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-2.5 text-xs space-y-1"
                      >
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <span className="font-medium text-gray-900 dark:text-white">
                            {sn.title || sn.source_path}
                          </span>
                          {sn.agent && (
                            <span className="rounded bg-gray-200 dark:bg-gray-700 px-1.5 py-0.5 font-mono text-[11px] text-gray-700 dark:text-gray-200">
                              {sn.agent}
                            </span>
                          )}
                        </div>
                        <p className="text-gray-600 dark:text-gray-300 whitespace-pre-wrap">
                          {sn.snippet}
                        </p>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {(contextResult.suggested_queries ?? []).length > 0 && (
                <div className="space-y-1">
                  <div className="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    Suggested CASS Queries
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {contextResult.suggested_queries?.map((q) => (
                      <button
                        key={q}
                        type="button"
                        onClick={() => onApplySuggestedQuery?.(q)}
                        className="rounded-full border border-blue-300 dark:border-blue-700 bg-blue-50 dark:bg-blue-900/20 px-2.5 py-0.5 text-xs text-blue-700 dark:text-blue-300 hover:bg-blue-100 dark:hover:bg-blue-900/40"
                      >
                        {q}
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </section>

        {/* Task Outcome Feedback Recorder */}
        <section
          aria-label="Task Outcome Feedback Recorder"
          className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
        >
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Record Task Outcome
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Close the procedural learning loop by scoring rule effectiveness
              against task outcomes via the running CASS Memory daemon.
            </p>
          </div>

          <form onSubmit={handleOutcomeSubmit} className="space-y-3">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label
                  htmlFor="memory-outcome-status"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Outcome Status
                </label>
                <select
                  id="memory-outcome-status"
                  value={outcomeStatus}
                  onChange={(e) =>
                    setOutcomeStatus(
                      e.target.value as "success" | "partial" | "failure"
                    )
                  }
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
                >
                  <option value="success">Success</option>
                  <option value="partial">Partial</option>
                  <option value="failure">Failure</option>
                </select>
              </div>
              <div>
                <label
                  htmlFor="memory-outcome-sentiment"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Sentiment (optional)
                </label>
                <input
                  id="memory-outcome-sentiment"
                  value={outcomeSentiment}
                  onChange={(e) => setOutcomeSentiment(e.target.value)}
                  placeholder="e.g. high-confidence, time-saver"
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
                />
              </div>
            </div>

            <div>
              <label
                htmlFor="memory-outcome-rule-ids"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Rule IDs (comma-separated)
              </label>
              <input
                id="memory-outcome-rule-ids"
                value={outcomeRuleIdsInput}
                onChange={(e) => setOutcomeRuleIdsInput(e.target.value)}
                placeholder="b-8f2a1, b-4c91e (or select rules on the left)"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm font-mono text-gray-900 dark:text-white"
              />
            </div>

            <div>
              <label
                htmlFor="memory-outcome-notes"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Outcome Notes (optional)
              </label>
              <textarea
                id="memory-outcome-notes"
                rows={2}
                value={outcomeNotes}
                onChange={(e) => setOutcomeNotes(e.target.value)}
                placeholder="What worked or failed during verification..."
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
              />
            </div>

            <button
              type="submit"
              disabled={outcomeMutation.isPending}
              className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
            >
              {outcomeMutation.isPending ? "Recording…" : "Record Outcome"}
            </button>
          </form>

          {outcomeError && (
            <div className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
              {outcomeError}
            </div>
          )}

          {outcomeResult?.recorded && (
            <div
              data-testid="memory-outcome-confirmation"
              className="rounded-md border border-emerald-200 bg-emerald-50 p-3 text-xs text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
            >
              Outcome feedback recorded (status:{" "}
              <span className="font-mono font-semibold">
                {outcomeResult.status}
              </span>
              ).
            </div>
          )}
        </section>
      </div>

      {/* Row 2: CASS Corpus Insights + Cross-Agent Privacy Policy */}
      <div className="grid gap-6 lg:grid-cols-2">
        {/* CASS Corpus Insights */}
        <section
          aria-label="CASS Corpus Insights"
          className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
        >
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                CASS Corpus Insights
              </h2>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                Inspect indexed conversation volume by coding agent harness and
                workspace.
              </p>
            </div>
            <button
              type="button"
              disabled={insightsQuery.isFetching}
              onClick={() => {
                if (!insightsRequested) {
                  setInsightsRequested(true);
                } else {
                  void insightsQuery.refetch();
                }
              }}
              className="self-start sm:self-auto rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {insightsQuery.isFetching
                ? "Loading Insights…"
                : insightsRequested
                  ? "Refresh Insights"
                  : "Load Corpus Insights"}
            </button>
          </div>

          {!insightsRequested && (
            <div className="py-4 text-xs text-gray-500 dark:text-gray-400">
              Click &ldquo;Load Corpus Insights&rdquo; to query{" "}
              <code className="font-mono">/api/v1/cass/insights</code> across
              indexed agent transcripts.
            </div>
          )}

          {insightsQuery.error && (
            <div className="rounded-md border border-yellow-200 bg-yellow-50 p-3 text-xs text-yellow-800 dark:border-yellow-800 dark:bg-yellow-900/20 dark:text-yellow-300">
              {formatApiErrorMessage(insightsQuery.error)}
            </div>
          )}

          {insightsQuery.data && (
            <div className="space-y-4">
              <div className="flex items-center justify-between rounded-md bg-gray-50 dark:bg-gray-900/50 px-3 py-2 text-xs">
                <span className="text-gray-600 dark:text-gray-300">
                  Total Indexed Documents
                </span>
                <span
                  data-testid="cass-insights-total-docs"
                  className="font-mono font-semibold text-gray-900 dark:text-white"
                >
                  {insightsQuery.data.total_documents}
                </span>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <div className="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400 mb-2">
                    By Agent ({insightsQuery.data.agents?.length ?? 0})
                  </div>
                  {(insightsQuery.data.agents ?? []).length === 0 ? (
                    <div className="text-xs text-gray-400">No agent stats.</div>
                  ) : (
                    <ul className="space-y-1 text-xs">
                      {insightsQuery.data.agents.map((bucket) => (
                        <li
                          key={bucket.key}
                          className="flex items-center justify-between rounded border border-gray-200 dark:border-gray-700 px-2.5 py-1.5"
                        >
                          <span className="font-mono text-gray-800 dark:text-gray-200">
                            {bucket.key}
                          </span>
                          <span className="font-semibold text-gray-900 dark:text-white">
                            {bucket.doc_count}
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>

                <div>
                  <div className="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400 mb-2">
                    Top Workspaces ({insightsQuery.data.workspaces?.length ?? 0}
                    )
                  </div>
                  {(insightsQuery.data.workspaces ?? []).length === 0 ? (
                    <div className="text-xs text-gray-400">
                      No workspace stats.
                    </div>
                  ) : (
                    <ul className="space-y-1 text-xs">
                      {insightsQuery.data.workspaces
                        .slice(0, 8)
                        .map((bucket) => (
                          <li
                            key={bucket.key}
                            className="flex items-center justify-between gap-2 rounded border border-gray-200 dark:border-gray-700 px-2.5 py-1.5"
                          >
                            <span
                              className="font-mono text-gray-700 dark:text-gray-300 truncate"
                              title={bucket.key}
                            >
                              {bucket.key}
                            </span>
                            <span className="shrink-0 font-semibold text-gray-900 dark:text-white">
                              {bucket.doc_count}
                            </span>
                          </li>
                        ))}
                    </ul>
                  )}
                </div>
              </div>

              {(insightsQuery.data.warnings ?? []).length > 0 && (
                <div className="rounded border border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-900/20 p-2.5 text-xs text-yellow-800 dark:text-yellow-300">
                  {insightsQuery.data.warnings?.join(" · ")}
                </div>
              )}
            </div>
          )}
        </section>

        {/* Cross-Agent Privacy Controls */}
        <section
          aria-label="Cross-Agent Memory Privacy"
          className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
        >
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Cross-Agent Privacy Policy
              </h2>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                Inspect or update which coding agent harnesses participate in
                cross-agent memory enrichment.
              </p>
            </div>
            <button
              type="button"
              disabled={privacyQuery.isFetching}
              onClick={() => {
                if (!privacyRequested) {
                  setPrivacyRequested(true);
                } else {
                  void privacyQuery.refetch();
                }
              }}
              className="self-start sm:self-auto rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {privacyQuery.isFetching
                ? "Checking…"
                : privacyRequested
                  ? "Refresh Privacy Status"
                  : "Load Privacy Status"}
            </button>
          </div>

          {privacyQuery.error && (
            <div className="rounded-md border border-yellow-200 bg-yellow-50 p-3 text-xs text-yellow-800 dark:border-yellow-800 dark:bg-yellow-900/20 dark:text-yellow-300">
              {formatApiErrorMessage(privacyQuery.error)}
            </div>
          )}

          {privacyMetadataEntries.length > 0 && (
            <div
              data-testid="memory-privacy-current"
              className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-3 text-xs font-mono space-y-1"
            >
              {privacyMetadataEntries.map(([key, val]) => (
                <div
                  key={key}
                  className="flex items-center justify-between gap-2"
                >
                  <span className="text-gray-500 dark:text-gray-400">
                    {key}
                  </span>
                  <span className="text-gray-900 dark:text-white truncate">
                    {typeof val === "object"
                      ? JSON.stringify(val)
                      : String(val)}
                  </span>
                </div>
              ))}
            </div>
          )}

          <form onSubmit={handlePrivacySubmit} className="space-y-3">
            <label
              htmlFor="memory-privacy-enabled"
              className="flex items-center gap-2 text-sm text-gray-800 dark:text-gray-200 cursor-pointer"
            >
              <input
                id="memory-privacy-enabled"
                type="checkbox"
                checked={privacyEnabled}
                onChange={(e) => setPrivacyEnabled(e.target.checked)}
                className="h-4 w-4"
              />
              Enable cross-agent memory enrichment
            </label>

            <div>
              <label
                htmlFor="memory-privacy-agents"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Allowed Agents (comma-separated, optional)
              </label>
              <input
                id="memory-privacy-agents"
                value={privacyAgentsInput}
                onChange={(e) => setPrivacyAgentsInput(e.target.value)}
                disabled={!privacyEnabled}
                placeholder="claude, codex, gemini, antigravity, cursor, opencode"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm font-mono text-gray-900 dark:text-white disabled:opacity-50"
              />
            </div>

            <button
              type="submit"
              disabled={updatePrivacyMutation.isPending}
              className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {updatePrivacyMutation.isPending
                ? "Saving Policy…"
                : "Save Privacy Policy"}
            </button>
          </form>

          {privacySaveError && (
            <div className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
              {privacySaveError}
            </div>
          )}

          {privacySaveMessage && (
            <div
              data-testid="memory-privacy-saved"
              className="rounded-md border border-emerald-200 bg-emerald-50 p-3 text-xs text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
            >
              {privacySaveMessage}
            </div>
          )}
        </section>
      </div>

      {/* CASS Timeline, Transcript Preview & Capabilities */}
      <section
        aria-label="CASS Timeline, Preview & Capabilities"
        className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4"
      >
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          <div>
            <h3 className="text-base font-semibold text-gray-900 dark:text-white">
              CASS Session Timeline, Transcript Preview &amp; Capabilities
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Inspect cross-agent session history timeline, preview archived
              JSONL transcripts, and query CASS adapter capabilities.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <input
              aria-label="CASS timeline since"
              value={cassTimelineSince}
              onChange={(e) => setCassTimelineSince(e.target.value)}
              placeholder="7d"
              className="w-20 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <button
              type="button"
              onClick={() =>
                cassTimelineRequested
                  ? void cassTimelineQuery.refetch()
                  : setCassTimelineRequested(true)
              }
              className="rounded bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700"
            >
              Load CASS Timeline
            </button>
            <button
              type="button"
              onClick={() =>
                cassCapsRequested
                  ? void cassCapsQuery.refetch()
                  : setCassCapsRequested(true)
              }
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
            >
              CASS Capabilities
            </button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <input
            aria-label="CASS preview path"
            value={cassPreviewPath}
            onChange={(e) => setCassPreviewPath(e.target.value)}
            placeholder="Transcript source path to preview (/path/to/session.jsonl)..."
            className="flex-1 min-w-[220px] rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
          />
          <button
            type="button"
            disabled={cassPreviewMutation.isPending}
            onClick={() => {
              if (!cassPreviewPath.trim()) {
                setCassPreviewError("Enter a transcript source path first.");
                return;
              }
              cassPreviewMutation.mutate(cassPreviewPath.trim());
            }}
            className="rounded bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
          >
            Preview Transcript
          </button>
        </div>

        {cassPreviewError && (
          <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
            {cassPreviewError}
          </div>
        )}

        {cassPreviewData && (
          <pre
            data-testid="cass-transcript-preview"
            className="max-h-48 overflow-auto rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900 p-3 text-xs font-mono text-gray-800 dark:text-gray-200 whitespace-pre-wrap"
          >
            {cassPreviewData}
          </pre>
        )}

        {cassTimelineRequested && cassTimelineQuery.data && (
          <div
            data-testid="cass-timeline-results"
            className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-3 text-xs space-y-1.5"
          >
            <div className="font-semibold text-gray-900 dark:text-white">
              CASS Timeline Entries (
              {cassTimelineQuery.data.count ??
                (cassTimelineQuery.data.entries?.length || 0)}
              )
            </div>
            {(cassTimelineQuery.data.entries ?? []).map((item, idx) => (
              <div
                key={`${item.source_path || idx}`}
                className="font-mono text-gray-700 dark:text-gray-300"
              >
                [{item.agent || "agent"}]{" "}
                {item.summary || item.source_path || item.timestamp || "entry"}
              </div>
            ))}
          </div>
        )}

        {cassCapsRequested && cassCapsQuery.data && (
          <div
            data-testid="cass-capabilities-card"
            className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-3 text-xs font-mono text-gray-800 dark:text-gray-200"
          >
            CASS Capabilities Loaded:{" "}
            {Object.keys(cassCapsQuery.data).slice(0, 6).join(", ")}
          </div>
        )}
      </section>
    </section>
  );
}
