"use client";

/**
 * Pipelines Page
 *
 * Pipeline workflow management with run list and visual builder.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { apiRequestSignal, getAuthHeaders, getBaseUrl } from "@/lib/api/client";

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface PipelineProgress {
  completed: number;
  pending: number;
  failed: number;
  skipped: number;
  total: number;
  percent: number;
}

interface PipelineSummary {
  run_id: string;
  workflow_id: string;
  session: string;
  status: string;
  started_at: string;
  finished_at?: string;
  progress: PipelineProgress;
}

interface PipelinesListResponse extends ApiEnvelope {
  pipelines: PipelineSummary[];
  count: number;
}

interface StepExecution {
  id: string;
  name?: string;
  status: string;
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  error?: string;
}

interface PipelineDetail extends PipelineSummary {
  current_step?: string;
  steps?: StepExecution[];
  duration_ms?: number;
  error?: string;
}

interface PipelineDetailResponse extends ApiEnvelope, PipelineDetail {}

interface Template {
  name: string;
  path: string;
  description?: string;
}

interface TemplatesResponse extends ApiEnvelope {
  templates: Template[];
  count: number;
}

type Notice = { type: "success" | "error"; message: string };
type StatusFilter = "all" | "running" | "completed" | "failed" | "cancelled";

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

function formatTimestamp(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatDuration(ms?: number): string {
  if (!ms) return "—";
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  return `${minutes}m ${remainingSeconds}s`;
}

function statusBadge(status: string): string {
  switch (status) {
    case "running":
    case "pending":
      return "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300";
    case "completed":
      return "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300";
    case "failed":
      return "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300";
    case "cancelled":
      return "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400";
    default:
      return "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300";
  }
}

function stepStatusColor(status: string): string {
  switch (status) {
    case "running":
      return "border-blue-500 bg-blue-50 dark:bg-blue-900/20";
    case "completed":
      return "border-green-500 bg-green-50 dark:bg-green-900/20";
    case "failed":
      return "border-red-500 bg-red-50 dark:bg-red-900/20";
    case "pending":
      return "border-gray-300 bg-gray-50 dark:border-gray-600 dark:bg-gray-800";
    case "skipped":
      return "border-gray-300 bg-gray-100 dark:border-gray-600 dark:bg-gray-700";
    default:
      return "border-gray-300 dark:border-gray-600";
  }
}

export default function PipelinesPage() {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState<Notice | null>(null);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  const [showRunModal, setShowRunModal] = useState(false);
  const noticeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [runForm, setRunForm] = useState({
    workflowFile: "",
    session: "",
    background: true,
  });
  const [actionBusy, setActionBusy] = useState({
    run: false,
    cancel: false,
    resume: false,
    validate: false,
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

  // Pipelines list query
  const pipelinesQuery = useQuery({
    queryKey: ["pipelines"],
    queryFn: () => apiFetch<PipelinesListResponse>("/api/v1/pipelines"),
    refetchInterval: 3000,
  });

  // Templates query
  const templatesQuery = useQuery({
    queryKey: ["pipelines", "templates"],
    queryFn: () => apiFetch<TemplatesResponse>("/api/v1/pipelines/templates"),
    refetchInterval: 60000,
  });

  // Selected pipeline detail
  const detailQuery = useQuery({
    queryKey: ["pipelines", selectedRunId],
    queryFn: () =>
      apiFetch<PipelineDetailResponse>(`/api/v1/pipelines/${selectedRunId}`),
    enabled: selectedRunId !== null,
    refetchInterval: 2000,
  });

  const pipelinesList = useMemo(() => pipelinesQuery.data?.pipelines ?? [], [pipelinesQuery.data?.pipelines]);
  const templatesList = templatesQuery.data?.templates ?? [];

  const filteredPipelines = useMemo(() => {
    const sorted = [...pipelinesList].sort((a, b) => {
      const aTime = new Date(a.started_at).getTime();
      const bTime = new Date(b.started_at).getTime();
      return bTime - aTime;
    });

    if (statusFilter === "all") return sorted;
    return sorted.filter((p) => p.status === statusFilter);
  }, [pipelinesList, statusFilter]);

  // Auto-select first pipeline if none selected
  useEffect(() => {
    if (filteredPipelines.length === 0) {
      if (selectedRunId !== null) {
        setSelectedRunId(null);
      }
      return;
    }

    const hasSelectedPipeline = selectedRunId
      ? filteredPipelines.some((pipeline) => pipeline.run_id === selectedRunId)
      : false;

    if (!hasSelectedPipeline) {
      setSelectedRunId(filteredPipelines[0].run_id);
    }
  }, [filteredPipelines, selectedRunId]);

  const selectedPipeline = useMemo(() => {
    if (!selectedRunId) return null;
    return pipelinesList.find((p) => p.run_id === selectedRunId) ?? null;
  }, [pipelinesList, selectedRunId]);

  const pipelineDetail = detailQuery.data;

  const handleRun = useCallback(async () => {
    if (!runForm.workflowFile || !runForm.session) {
      setStatusNotice({ type: "error", message: "Workflow file and session are required." });
      return;
    }

    setActionBusy((prev) => ({ ...prev, run: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Pipelines] Run", runForm);
      }
      await apiFetch<ApiEnvelope>("/api/v1/pipelines/run", {
        method: "POST",
        body: JSON.stringify({
          workflow_file: runForm.workflowFile,
          session: runForm.session,
          background: runForm.background,
        }),
      });
      queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      setStatusNotice({ type: "success", message: "Pipeline started." });
      setShowRunModal(false);
      setRunForm({ workflowFile: "", session: "", background: true });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, run: false }));
    }
  }, [runForm, queryClient, setStatusNotice]);

  const handleCancel = useCallback(async () => {
    if (!selectedRunId) return;

    setActionBusy((prev) => ({ ...prev, cancel: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Pipelines] Cancel", selectedRunId);
      }
      await apiFetch<ApiEnvelope>(`/api/v1/pipelines/${selectedRunId}/cancel`, {
        method: "POST",
      });
      queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      setStatusNotice({ type: "success", message: "Pipeline cancelled." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, cancel: false }));
    }
  }, [selectedRunId, queryClient, setStatusNotice]);

  const handleResume = useCallback(async () => {
    if (!selectedRunId || !selectedPipeline) return;

    setActionBusy((prev) => ({ ...prev, resume: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Pipelines] Resume", selectedRunId);
      }
      await apiFetch<ApiEnvelope>(`/api/v1/pipelines/${selectedRunId}/resume`, {
        method: "POST",
        body: JSON.stringify({ session: selectedPipeline.session }),
      });
      queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      setStatusNotice({ type: "success", message: "Pipeline resumed." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, resume: false }));
    }
  }, [selectedRunId, selectedPipeline, queryClient, setStatusNotice]);

  const connectionError =
    pipelinesQuery.error ?? templatesQuery.error ?? detailQuery.error;

  const runningCount = pipelinesList.filter(
    (p) => p.status === "running" || p.status === "pending"
  ).length;

  return (
    <div className="space-y-8">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Pipelines
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Workflow automation and pipeline runs.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            onClick={() => setShowRunModal(true)}
            className="px-3 py-1.5 rounded-md text-xs font-medium bg-blue-600 text-white hover:bg-blue-700"
          >
            Run Pipeline
          </button>
        </div>
      </div>

      {connectionError && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md">
          <p className="text-sm text-red-700 dark:text-red-400">
            Pipeline error: {getErrorMessage(connectionError)}
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

      {/* Stats Cards */}
      <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Total Runs
          </div>
          <div className="mt-2 text-lg font-semibold text-gray-900 dark:text-white">
            {pipelinesList.length}
          </div>
        </div>
        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Running
          </div>
          <div className="mt-2 flex items-center gap-2">
            {runningCount > 0 && (
              <span className="h-2 w-2 rounded-full bg-blue-500 animate-pulse" />
            )}
            <span className="text-lg font-semibold text-gray-900 dark:text-white">
              {runningCount}
            </span>
          </div>
        </div>
        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Completed
          </div>
          <div className="mt-2 text-lg font-semibold text-green-600 dark:text-green-400">
            {pipelinesList.filter((p) => p.status === "completed").length}
          </div>
        </div>
        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Templates
          </div>
          <div className="mt-2 text-lg font-semibold text-gray-900 dark:text-white">
            {templatesList.length}
          </div>
        </div>
      </section>

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Pipeline List */}
        <section className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Runs
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Pipeline execution history.
              </p>
            </div>
            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
              className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-700 dark:text-gray-200"
            >
              <option value="all">All</option>
              <option value="running">Running</option>
              <option value="completed">Completed</option>
              <option value="failed">Failed</option>
              <option value="cancelled">Cancelled</option>
            </select>
          </div>

          {pipelinesQuery.isLoading && (
            <div className="flex items-center justify-center h-40">
              <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
            </div>
          )}

          {!pipelinesQuery.isLoading && filteredPipelines.length === 0 && (
            <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              No pipelines match the current filter.
            </div>
          )}

          {filteredPipelines.length > 0 && (
            <div className="space-y-2 max-h-[600px] overflow-y-auto">
              {filteredPipelines.map((pipeline) => {
                const isSelected = pipeline.run_id === selectedRunId;
                const isRunning = pipeline.status === "running" || pipeline.status === "pending";
                return (
                  <button
                    key={pipeline.run_id}
                    type="button"
                    onClick={() => setSelectedRunId(pipeline.run_id)}
                    className={`w-full text-left rounded-md border px-3 py-3 transition-colors ${
                      isSelected
                        ? "border-blue-400 bg-blue-50 dark:bg-blue-900/20"
                        : "border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 hover:border-blue-300"
                    }`}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <div className="text-sm font-medium text-gray-900 dark:text-white flex items-center gap-2">
                          {isRunning && (
                            <span className="h-2 w-2 rounded-full bg-blue-500 animate-pulse" />
                          )}
                          {pipeline.workflow_id}
                        </div>
                        <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                          {pipeline.session} · {formatTimestamp(pipeline.started_at)}
                        </div>
                      </div>
                      <span
                        className={`px-2 py-0.5 rounded-full text-xs ${statusBadge(
                          pipeline.status
                        )}`}
                      >
                        {pipeline.status}
                      </span>
                    </div>
                    {pipeline.progress && (
                      <div className="mt-2">
                        <div className="flex items-center justify-between text-xs text-gray-500 dark:text-gray-400 mb-1">
                          <span>
                            {pipeline.progress.completed}/{pipeline.progress.total} steps
                          </span>
                          <span>{pipeline.progress.percent}%</span>
                        </div>
                        <div className="h-1.5 w-full bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
                          <div
                            className={`h-full transition-all ${
                              pipeline.status === "failed"
                                ? "bg-red-500"
                                : pipeline.status === "completed"
                                ? "bg-green-500"
                                : "bg-blue-500"
                            }`}
                            style={{ width: `${pipeline.progress.percent}%` }}
                          />
                        </div>
                      </div>
                    )}
                  </button>
                );
              })}
            </div>
          )}
        </section>

        {/* Pipeline Detail */}
        <section className="space-y-4">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Detail
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Pipeline run details and step progress.
            </p>
          </div>

          {!selectedPipeline && (
            <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              Select a pipeline to view details.
            </div>
          )}

          {selectedPipeline && (
            <div className="space-y-4">
              {/* Info Card */}
              <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
                <div className="flex items-start justify-between gap-4">
                  <div>
                    <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                      {selectedPipeline.workflow_id}
                    </h3>
                    <p className="text-xs text-gray-500 dark:text-gray-400">
                      Run ID: {selectedPipeline.run_id}
                    </p>
                  </div>
                  <span
                    className={`px-2 py-0.5 rounded-full text-xs ${statusBadge(
                      selectedPipeline.status
                    )}`}
                  >
                    {selectedPipeline.status}
                  </span>
                </div>

                <div className="text-xs text-gray-500 dark:text-gray-400 space-y-1">
                  <div>Session: {selectedPipeline.session}</div>
                  <div>Started: {formatTimestamp(selectedPipeline.started_at)}</div>
                  {selectedPipeline.finished_at && (
                    <div>Finished: {formatTimestamp(selectedPipeline.finished_at)}</div>
                  )}
                  {pipelineDetail?.duration_ms && (
                    <div>Duration: {formatDuration(pipelineDetail.duration_ms)}</div>
                  )}
                  {pipelineDetail?.current_step && (
                    <div>Current Step: {pipelineDetail.current_step}</div>
                  )}
                </div>

                {pipelineDetail?.error && (
                  <div className="p-2 rounded bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-xs text-red-700 dark:text-red-300">
                    {pipelineDetail.error}
                  </div>
                )}

                <div className="flex gap-2">
                  {(selectedPipeline.status === "running" ||
                    selectedPipeline.status === "pending") && (
                    <button
                      type="button"
                      onClick={handleCancel}
                      disabled={actionBusy.cancel}
                      className="px-3 py-1.5 rounded-md text-xs font-medium border border-red-200 text-red-600 dark:border-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
                    >
                      Cancel
                    </button>
                  )}
                  {(selectedPipeline.status === "failed" ||
                    selectedPipeline.status === "cancelled") && (
                    <button
                      type="button"
                      onClick={handleResume}
                      disabled={actionBusy.resume}
                      className="px-3 py-1.5 rounded-md text-xs font-medium bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
                    >
                      Resume
                    </button>
                  )}
                </div>
              </div>

              {/* Steps */}
              {pipelineDetail?.steps && pipelineDetail.steps.length > 0 && (
                <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
                  <div className="text-sm font-semibold text-gray-900 dark:text-white">
                    Steps
                  </div>
                  <div className="space-y-2">
                    {pipelineDetail.steps.map((step, idx) => (
                      <div
                        key={step.id}
                        className={`rounded-md border-l-4 p-3 ${stepStatusColor(
                          step.status
                        )}`}
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div>
                            <div className="text-xs font-medium text-gray-900 dark:text-white">
                              {idx + 1}. {step.name || step.id}
                            </div>
                            {step.started_at && (
                              <div className="text-xs text-gray-500 dark:text-gray-400">
                                {formatTimestamp(step.started_at)}
                                {step.duration_ms
                                  ? ` (${formatDuration(step.duration_ms)})`
                                  : ""}
                              </div>
                            )}
                          </div>
                          <span
                            className={`px-2 py-0.5 rounded text-xs ${statusBadge(
                              step.status
                            )}`}
                          >
                            {step.status}
                          </span>
                        </div>
                        {step.error && (
                          <div className="mt-2 text-xs text-red-600 dark:text-red-400">
                            {step.error}
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}

          {/* Templates */}
          {templatesList.length > 0 && (
            <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
              <div className="text-sm font-semibold text-gray-900 dark:text-white">
                Templates
              </div>
              <div className="space-y-2">
                {templatesList.slice(0, 5).map((template) => (
                  <div
                    key={template.path}
                    className="text-xs text-gray-600 dark:text-gray-300"
                  >
                    <code className="bg-gray-100 dark:bg-gray-700 px-1 rounded">
                      {template.name}
                    </code>
                    {template.description && (
                      <span className="ml-2 text-gray-500 dark:text-gray-400">
                        {template.description}
                      </span>
                    )}
                  </div>
                ))}
                {templatesList.length > 5 && (
                  <div className="text-xs text-gray-500 dark:text-gray-400">
                    +{templatesList.length - 5} more
                  </div>
                )}
              </div>
            </div>
          )}
        </section>
      </div>

      {/* Run Modal */}
      {showRunModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-white dark:bg-gray-800 rounded-lg shadow-lg w-full max-w-md mx-4 p-6 space-y-4">
            <div className="flex items-center justify-between">
              <h3 className="text-lg font-semibold text-gray-900 dark:text-white">
                Run Pipeline
              </h3>
              <button
                type="button"
                onClick={() => setShowRunModal(false)}
                className="text-gray-400 hover:text-gray-600 dark:hover:text-gray-200"
              >
                <span className="sr-only">Close</span>
                <svg
                  className="h-5 w-5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M6 18L18 6M6 6l12 12"
                  />
                </svg>
              </button>
            </div>

            <div className="space-y-4">
              <div>
                <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
                  Workflow File
                </label>
                <input
                  type="text"
                  value={runForm.workflowFile}
                  onChange={(e) =>
                    setRunForm((prev) => ({ ...prev, workflowFile: e.target.value }))
                  }
                  placeholder="path/to/workflow.yaml"
                  className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
                />
                {templatesList.length > 0 && (
                  <div className="mt-1">
                    <select
                      value=""
                      onChange={(e) =>
                        setRunForm((prev) => ({
                          ...prev,
                          workflowFile: e.target.value,
                        }))
                      }
                      className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-700 dark:text-gray-200"
                    >
                      <option value="">Select template...</option>
                      {templatesList.map((t) => (
                        <option key={t.path} value={t.path}>
                          {t.name}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
              </div>

              <div>
                <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
                  Session
                </label>
                <input
                  type="text"
                  value={runForm.session}
                  onChange={(e) =>
                    setRunForm((prev) => ({ ...prev, session: e.target.value }))
                  }
                  placeholder="session-name"
                  className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
                />
              </div>

              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="background"
                  checked={runForm.background}
                  onChange={(e) =>
                    setRunForm((prev) => ({ ...prev, background: e.target.checked }))
                  }
                  className="rounded border-gray-300 dark:border-gray-600"
                />
                <label
                  htmlFor="background"
                  className="text-xs text-gray-600 dark:text-gray-300"
                >
                  Run in background
                </label>
              </div>
            </div>

            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setShowRunModal(false)}
                className="px-3 py-1.5 rounded-md text-xs font-medium border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={handleRun}
                disabled={actionBusy.run || !runForm.workflowFile || !runForm.session}
                className="px-3 py-1.5 rounded-md text-xs font-medium bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
              >
                Run
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
