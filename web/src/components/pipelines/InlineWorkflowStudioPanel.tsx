"use client";

/**
 * InlineWorkflowStudioPanel
 *
 * Surfaces inline pipeline authoring, validation, execution, and state cleanup:
 *   - POST /api/v1/pipelines/validate (YAML workflow validation + errors/warnings)
 *   - POST /api/v1/pipelines/exec (inline JSON Workflow execution without a file on disk)
 *   - POST /api/v1/pipelines/cleanup (prune completed/failed/cancelled run state files)
 */

import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  apiRequestSignal,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface ValidationIssue {
  field?: string;
  message: string;
  hint?: string;
}

export interface PipelineValidateResponse extends ApiEnvelope {
  valid: boolean;
  workflow_id: string;
  step_count: number;
  errors: ValidationIssue[];
  warnings: ValidationIssue[];
}

export interface InlineWorkflowStepDraft {
  id: string;
  name?: string;
  agent?: string;
  prompt?: string;
  command?: string;
}

export interface PipelineExecResponse extends ApiEnvelope {
  run_id: string;
  workflow_id: string;
  session: string;
  status: string;
}

export interface PipelineCleanupResponse extends ApiEnvelope {
  deleted: number;
  eligible: number;
  retained: number;
  older_than_hours: number;
}

const DEFAULT_STARTER_YAML = `schema_version: "2.0"
name: quick-review
steps:
  - id: inspect
    agent: claude
    prompt: Review staged changes for correctness and concurrency bugs
`;

const INLINE_AGENT_HARNESSES = [
  { value: "claude", label: "Claude Code (claude)" },
  { value: "codex", label: "OpenAI Codex (codex)" },
  { value: "antigravity", label: "Antigravity (antigravity)" },
  { value: "grok", label: "Grok (grok)" },
  { value: "oc", label: "OpenCode (oc)" },
  { value: "omp", label: "Oh My Pi (omp)" },
  { value: "gemini", label: "Gemini CLI (gemini)" },
  { value: "aider", label: "Aider (aider)" },
  { value: "ollama", label: "Ollama (ollama)" },
];

export function InlineWorkflowStudioPanel({
  onNotice,
}: {
  onNotice?: (notice: { type: "success" | "error"; message: string }) => void;
}) {
  const queryClient = useQueryClient();

  // 1. Validate Workflow YAML
  const [yamlContent, setYamlContent] = useState(DEFAULT_STARTER_YAML);
  const [validationResult, setValidationResult] =
    useState<PipelineValidateResponse | null>(null);
  const [validationError, setValidationError] = useState<string | null>(null);

  const validateMutation = useMutation({
    mutationFn: (content: string) =>
      apiFetch<PipelineValidateResponse>("/api/v1/pipelines/validate", {
        method: "POST",
        body: JSON.stringify({ workflow_content: content }),
      }),
    onSuccess: (res) => {
      setValidationResult(res);
      setValidationError(null);
    },
    onError: (err) => {
      setValidationResult(null);
      setValidationError(formatApiErrorMessage(err));
    },
  });

  // 2. Execute Inline Workflow
  const [execName, setExecName] = useState("inline-triage");
  const [execSession, setExecSession] = useState("");
  const [execStepId, setExecStepId] = useState("step-1");
  const [execMode, setExecMode] = useState<"prompt" | "command">("prompt");
  const [execAgent, setExecAgent] = useState("claude");
  const [execBody, setExecBody] = useState("");
  const [execBackground, setExecBackground] = useState(true);
  const [execResult, setExecResult] = useState<PipelineExecResponse | null>(
    null
  );
  const [execError, setExecError] = useState<string | null>(null);

  const execMutation = useMutation({
    mutationFn: async () => {
      const step: InlineWorkflowStepDraft = {
        id: execStepId.trim() || "step-1",
      };
      if (execMode === "prompt") {
        step.agent = execAgent;
        step.prompt = execBody.trim();
      } else {
        step.command = execBody.trim();
      }
      return apiFetch<PipelineExecResponse>("/api/v1/pipelines/exec", {
        method: "POST",
        signal: execBackground ? apiRequestSignal(30_000) : undefined,
        body: JSON.stringify({
          workflow: {
            schema_version: "2.0",
            name: execName.trim() || "inline-workflow",
            steps: [step],
          },
          session: execSession.trim(),
          background: execBackground,
        }),
      });
    },
    onSuccess: (res) => {
      setExecResult(res);
      setExecError(null);
      void queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      onNotice?.({
        type: "success",
        message: `Inline workflow "${res.workflow_id}" started (${res.run_id}).`,
      });
    },
    onError: (err) => {
      setExecResult(null);
      setExecError(formatApiErrorMessage(err));
    },
  });

  // 3. Prune Old Pipeline State
  const [cleanupHours, setCleanupHours] = useState("24");
  const [confirmCleanup, setConfirmCleanup] = useState(false);
  const [cleanupResult, setCleanupResult] =
    useState<PipelineCleanupResponse | null>(null);
  const [cleanupError, setCleanupError] = useState<string | null>(null);

  const cleanupMutation = useMutation({
    mutationFn: (olderThanHours: number) =>
      apiFetch<PipelineCleanupResponse>("/api/v1/pipelines/cleanup", {
        method: "POST",
        body: JSON.stringify({ older_than_hours: olderThanHours }),
      }),
    onSuccess: (res) => {
      setCleanupResult(res);
      setCleanupError(null);
      setConfirmCleanup(false);
      void queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      onNotice?.({
        type: "success",
        message: `Pruned ${res.deleted} completed/finished pipeline state file(s).`,
      });
    },
    onError: (err) => {
      setCleanupError(formatApiErrorMessage(err));
      setConfirmCleanup(false);
    },
  });

  const handleValidateSubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = yamlContent.trim();
    if (!trimmed) {
      setValidationError("Workflow YAML content is required.");
      return;
    }
    validateMutation.mutate(trimmed);
  };

  const handleExecSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!execSession.trim()) {
      setExecError("Target session name is required.");
      return;
    }
    if (!execBody.trim()) {
      setExecError(
        execMode === "prompt"
          ? "Step prompt is required."
          : "Shell command is required."
      );
      return;
    }
    execMutation.mutate();
  };

  const handleConfirmCleanup = () => {
    const parsed = Number.parseInt(cleanupHours, 10);
    const hours = Number.isFinite(parsed) && parsed > 0 ? parsed : 24;
    cleanupMutation.mutate(hours);
  };

  return (
    <section
      aria-label="Inline Workflow Studio & State Maintenance"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-5"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Inline Workflow Studio & State Maintenance
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Validate YAML workflows (<code className="font-mono">/api/v1/pipelines/validate</code>), launch inline workflows (<code className="font-mono">/api/v1/pipelines/exec</code>), and prune archived state (<code className="font-mono">/api/v1/pipelines/cleanup</code>).
          </p>
        </div>

        {/* Cleanup quick bar */}
        <div
          role="region"
          aria-label="Pipeline State Cleanup"
          className="flex flex-wrap items-center gap-2"
        >
          <label
            htmlFor="pipeline-cleanup-hours"
            className="text-xs text-gray-600 dark:text-gray-300"
          >
            Older than (h):
          </label>
          <input
            id="pipeline-cleanup-hours"
            type="number"
            min={1}
            max={8760}
            value={cleanupHours}
            onChange={(e) => setCleanupHours(e.target.value)}
            className="w-20 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
          />
          {!confirmCleanup ? (
            <button
              type="button"
              disabled={cleanupMutation.isPending}
              onClick={() => setConfirmCleanup(true)}
              className="rounded-md border border-amber-300 dark:border-amber-700 bg-amber-50 dark:bg-amber-900/20 px-2.5 py-1 text-xs font-medium text-amber-800 dark:text-amber-200 hover:bg-amber-100 dark:hover:bg-amber-900/40 disabled:opacity-50"
            >
              Prune Old Pipeline State
            </button>
          ) : (
            <span className="inline-flex items-center gap-1.5">
              <button
                type="button"
                disabled={cleanupMutation.isPending}
                onClick={handleConfirmCleanup}
                className="rounded bg-amber-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-amber-700 disabled:opacity-50"
              >
                {cleanupMutation.isPending ? "Pruning…" : "Confirm Prune"}
              </button>
              <button
                type="button"
                onClick={() => setConfirmCleanup(false)}
                className="text-xs text-gray-500 dark:text-gray-400 hover:underline"
              >
                Abort Prune
              </button>
            </span>
          )}
        </div>
      </div>

      {cleanupError && (
        <div className="rounded border border-red-200 bg-red-50 p-2.5 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          {cleanupError}
        </div>
      )}
      {cleanupResult && (
        <div
          data-testid="pipeline-cleanup-summary"
          className="rounded border border-emerald-200 bg-emerald-50 p-2.5 text-xs text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
        >
          Cleanup summary (&gt;{cleanupResult.older_than_hours}h): deleted{" "}
          <strong>{cleanupResult.deleted}</strong>, eligible{" "}
          {cleanupResult.eligible}, retained {cleanupResult.retained}.
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Left: YAML Validator */}
        <form
          onSubmit={handleValidateSubmit}
          aria-label="YAML Workflow Validator"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Validate Workflow YAML
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Lint workflow syntax, step dependencies, and routing settings before running.
            </p>
          </div>

          <div>
            <label
              htmlFor="pipeline-validate-yaml"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Workflow YAML Content
            </label>
            <textarea
              id="pipeline-validate-yaml"
              rows={6}
              value={yamlContent}
              onChange={(e) => setYamlContent(e.target.value)}
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>

          <button
            type="submit"
            disabled={validateMutation.isPending}
            className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {validateMutation.isPending
              ? "Validating…"
              : "Validate Workflow YAML"}
          </button>

          {validationError && (
            <div className="rounded border border-red-200 bg-red-50 p-2.5 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
              {validationError}
            </div>
          )}

          {validationResult && (
            <div
              data-testid="pipeline-validate-result"
              className={`rounded border p-3 text-xs space-y-1.5 ${
                validationResult.valid
                  ? "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
                  : "border-red-200 bg-red-50 text-red-900 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200"
              }`}
            >
              <div className="font-semibold">
                {validationResult.valid ? "Valid Workflow" : "Invalid Workflow"}
                {validationResult.workflow_id
                  ? ` · ${validationResult.workflow_id}`
                  : ""}{" "}
                ({validationResult.step_count} step
                {validationResult.step_count === 1 ? "" : "s"})
              </div>
              {validationResult.errors.length > 0 && (
                <ul className="list-disc pl-4 space-y-0.5">
                  {validationResult.errors.map((issue, idx) => (
                    <li key={`${idx}-${issue.message}`}>
                      {issue.field ? `${issue.field}: ` : ""}
                      {issue.message}
                    </li>
                  ))}
                </ul>
              )}
              {validationResult.warnings.length > 0 && (
                <ul className="list-disc pl-4 space-y-0.5 text-amber-700 dark:text-amber-300">
                  {validationResult.warnings.map((issue, idx) => (
                    <li key={`${idx}-${issue.message}`}>
                      Warning: {issue.message}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </form>

        {/* Right: Inline Workflow Launcher */}
        <form
          onSubmit={handleExecSubmit}
          aria-label="Execute Inline Workflow"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Execute Inline Workflow
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Dispatch an ad-hoc workflow directly to a tmux session without saving a file.
            </p>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
            <div>
              <label
                htmlFor="inline-exec-name"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Workflow Name
              </label>
              <input
                id="inline-exec-name"
                value={execName}
                onChange={(e) => setExecName(e.target.value)}
                placeholder="inline-triage"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
              />
            </div>
            <div>
              <label
                htmlFor="inline-exec-session"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Target Session
              </label>
              <input
                id="inline-exec-session"
                value={execSession}
                onChange={(e) => setExecSession(e.target.value)}
                placeholder="my-project"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
              />
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2.5">
            <div>
              <label
                htmlFor="inline-exec-step-id"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Step ID
              </label>
              <input
                id="inline-exec-step-id"
                value={execStepId}
                onChange={(e) => setExecStepId(e.target.value)}
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
              />
            </div>
            <div>
              <label
                htmlFor="inline-exec-mode"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Step Mode
              </label>
              <select
                id="inline-exec-mode"
                value={execMode}
                onChange={(e) =>
                  setExecMode(e.target.value as "prompt" | "command")
                }
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
              >
                <option value="prompt">Agent Prompt</option>
                <option value="command">Shell Command</option>
              </select>
            </div>
            <div>
              <label
                htmlFor="inline-exec-agent"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Target Harness
              </label>
              <select
                id="inline-exec-agent"
                disabled={execMode !== "prompt"}
                value={execAgent}
                onChange={(e) => setExecAgent(e.target.value)}
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white disabled:opacity-50"
              >
                {INLINE_AGENT_HARNESSES.map((h) => (
                  <option key={h.value} value={h.value}>
                    {h.label}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div>
            <label
              htmlFor="inline-exec-body"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              {execMode === "prompt" ? "Prompt Instruction" : "Shell Command"}
            </label>
            <textarea
              id="inline-exec-body"
              rows={2}
              value={execBody}
              onChange={(e) => setExecBody(e.target.value)}
              placeholder={
                execMode === "prompt"
                  ? "Inspect recent commits and summarize risk areas"
                  : "go test -short ./..."
              }
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>

          <div className="flex items-center justify-between gap-3">
            <label
              htmlFor="inline-exec-background"
              className="flex items-center gap-1.5 text-xs text-gray-700 dark:text-gray-300 cursor-pointer"
            >
              <input
                id="inline-exec-background"
                type="checkbox"
                checked={execBackground}
                onChange={(e) => setExecBackground(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              Detach in background
            </label>

            <button
              type="submit"
              disabled={execMutation.isPending}
              className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {execMutation.isPending
                ? "Launching…"
                : "Execute Inline Workflow"}
            </button>
          </div>

          {execError && (
            <div className="rounded border border-red-200 bg-red-50 p-2.5 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
              {execError}
            </div>
          )}

          {execResult && (
            <div
              data-testid="pipeline-inline-exec-result"
              className="rounded border border-emerald-200 bg-emerald-50 p-2.5 text-xs text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
            >
              Started inline run{" "}
              <span className="font-mono font-semibold">{execResult.run_id}</span>{" "}
              ({execResult.status}) on session{" "}
              <span className="font-mono">{execResult.session}</span>.
            </div>
          )}
        </form>
      </div>
    </section>
  );
}
