"use client";

/**
 * WorkflowsSequencesAndTemplatesPanel
 *
 * Surfaces multi-agent workflow coordination templates (with runtime trigger
 * validation), session configuration templates, and retry-safe per-pane prompt
 * sequences on /pipelines:
 *   - GET  /api/v1/workflows
 *   - GET  /api/v1/workflows/{name}
 *   - GET  /api/v1/session-templates
 *   - GET  /api/v1/session-templates/{name}
 *   - POST /api/v1/sequences
 *   - GET  /api/v1/sequences/{name}/next?pane=...
 *   - POST /api/v1/sequences/{name}/advance
 */

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

interface WorkflowSummaryItem {
  name: string;
  description: string;
  source: string;
  coordination: string;
  agent_count: number;
  roles?: string[];
}

interface WorkflowDetailPayload {
  name: string;
  description: string;
  source: string;
  coordination: string;
  agent_count: number;
  roles?: string[];
  validated_triggers?: Array<{
    from: string;
    to: string;
    runtime_type: string;
    valid: boolean;
    error?: string;
  }>;
}

interface WorkflowsEnvelope extends ApiEnvelope {
  action: string;
  workflows?: WorkflowSummaryItem[];
  total?: number;
  workflow?: WorkflowDetailPayload;
}

interface SessionTemplateSummaryItem {
  name: string;
  description: string;
  source: string;
  tags?: string[];
  agent_count: number;
}

interface SessionTemplatesEnvelope extends ApiEnvelope {
  action: string;
  templates?: SessionTemplateSummaryItem[];
  total?: number;
  template?: {
    name: string;
    description: string;
    source: string;
    tags?: string[];
    agents?: Record<string, unknown>;
  };
}

interface SequencePaneState {
  sequence: string;
  pane: string;
  position: number;
  total: number;
  prompt?: string;
  done: boolean;
  advanced?: boolean;
}

interface SequenceEnvelope extends ApiEnvelope {
  action: string;
  sequence?: {
    name: string;
    steps: string[];
  };
  pane?: SequencePaneState;
}

export function WorkflowsSequencesAndTemplatesPanel() {
  const [roleFilter, setRoleFilter] = useState("");
  const [selectedWorkflow, setSelectedWorkflow] = useState("");
  const [selectedTemplate, setSelectedTemplate] = useState("");

  // Durable per-pane prompt sequence state
  const [seqName, setSeqName] = useState("review-loop");
  const [seqStepsRaw, setSeqStepsRaw] = useState("inspect\nchallenge\nsummarize");
  const [seqPane, setSeqPane] = useState("%1");
  const [paneState, setPaneState] = useState<SequencePaneState | null>(null);
  const [seqMessage, setSeqMessage] = useState<string | null>(null);

  const workflowsQuery = useQuery({
    queryKey: ["workflows-catalog", roleFilter],
    queryFn: () =>
      apiFetch<WorkflowsEnvelope>(
        roleFilter.trim()
          ? `/api/v1/workflows?role=${encodeURIComponent(roleFilter.trim())}`
          : "/api/v1/workflows"
      ),
  });

  const workflowDetailQuery = useQuery({
    queryKey: ["workflow-detail", selectedWorkflow],
    queryFn: () =>
      apiFetch<WorkflowsEnvelope>(
        `/api/v1/workflows/${encodeURIComponent(selectedWorkflow)}`
      ),
    enabled: Boolean(selectedWorkflow),
  });

  const templatesQuery = useQuery({
    queryKey: ["session-templates-catalog"],
    queryFn: () => apiFetch<SessionTemplatesEnvelope>("/api/v1/session-templates"),
  });

  const templateDetailQuery = useQuery({
    queryKey: ["session-template-detail", selectedTemplate],
    queryFn: () =>
      apiFetch<SessionTemplatesEnvelope>(
        `/api/v1/session-templates/${encodeURIComponent(selectedTemplate)}`
      ),
    enabled: Boolean(selectedTemplate),
  });

  const createSeqMutation = useMutation({
    mutationFn: () => {
      const steps = seqStepsRaw
        .split("\n")
        .map((s) => s.trim())
        .filter(Boolean);
      return apiFetch<SequenceEnvelope>("/api/v1/sequences", {
        method: "POST",
        body: JSON.stringify({ name: seqName.trim(), steps }),
      });
    },
    onSuccess: (res) => {
      setSeqMessage(
        `Created sequence "${res.sequence?.name || seqName}" (${res.sequence?.steps.length ?? 0} steps).`
      );
    },
    onError: (err) => {
      setSeqMessage(`Create sequence failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const inspectNextMutation = useMutation({
    mutationFn: () =>
      apiFetch<SequenceEnvelope>(
        `/api/v1/sequences/${encodeURIComponent(seqName.trim())}/next?pane=${encodeURIComponent(seqPane.trim())}`
      ),
    onSuccess: (res) => {
      if (res.pane) {
        setPaneState(res.pane);
      }
      setSeqMessage(null);
    },
    onError: (err) => {
      setSeqMessage(`Sequence next failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const advanceSeqMutation = useMutation({
    mutationFn: () =>
      apiFetch<SequenceEnvelope>(
        `/api/v1/sequences/${encodeURIComponent(seqName.trim())}/advance`,
        {
          method: "POST",
          body: JSON.stringify({
            pane: seqPane.trim(),
            ...(paneState ? { expected_position: paneState.position } : {}),
          }),
        }
      ),
    onSuccess: (res) => {
      if (res.pane) {
        setPaneState(res.pane);
        setSeqMessage(
          `Advanced pane ${res.pane.pane} to step ${res.pane.position}/${res.pane.total}.`
        );
      }
    },
    onError: (err) => {
      setSeqMessage(`Sequence advance failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const workflows = workflowsQuery.data?.workflows ?? [];
  const workflowDetail = workflowDetailQuery.data?.workflow;
  const templates = templatesQuery.data?.templates ?? [];
  const templateDetail = templateDetailQuery.data?.template;

  return (
    <section
      data-testid="workflows-sequences-templates-panel"
      className="rounded-xl border border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-900 p-5 space-y-5"
    >
      <div>
        <h2 className="text-base font-semibold text-gray-900 dark:text-white">
          Multi-Agent Workflows, Session Templates &amp; Per-Pane Prompt Sequences
        </h2>
        <p className="text-xs text-gray-500 dark:text-gray-400">
          Inspect coordination patterns with trigger diagnostics, browse session templates, and drive retry-safe per-pane prompt sequences.
        </p>
      </div>

      {/* 1. Workflows + Session Templates Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <div className="rounded-lg border border-gray-200 dark:border-gray-800 p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Workflow Templates ({workflows.length})
            </h3>
            <input
              type="text"
              aria-label="Filter Workflows by Role"
              value={roleFilter}
              onChange={(e) => setRoleFilter(e.target.value)}
              placeholder="Filter role…"
              className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
            />
          </div>

          <div className="space-y-2 max-h-52 overflow-y-auto">
            {workflows.map((wf) => (
              <div
                key={wf.name}
                className="flex items-center justify-between gap-2 rounded border border-gray-100 dark:border-gray-800 p-2 text-xs"
              >
                <div>
                  <div className="font-mono font-semibold text-gray-900 dark:text-white">
                    {wf.name}{" "}
                    <span className="font-sans font-normal text-gray-500">
                      ({wf.coordination}, {wf.agent_count} agents)
                    </span>
                  </div>
                  <div className="text-gray-500 dark:text-gray-400">
                    {wf.description}
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => setSelectedWorkflow(wf.name)}
                  className="rounded border border-gray-200 dark:border-gray-700 px-2 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-800"
                >
                  Inspect Workflow
                </button>
              </div>
            ))}
          </div>

          {workflowDetail && (
            <div
              data-testid="workflow-detail-box"
              className="rounded bg-blue-50/50 dark:bg-blue-950/20 border border-blue-200 dark:border-blue-800 p-3 text-xs space-y-1"
            >
              <div className="font-mono font-semibold text-gray-900 dark:text-white">
                {workflowDetail.name} [{workflowDetail.source}]
              </div>
              <div className="text-gray-600 dark:text-gray-300">
                Roles: {(workflowDetail.roles ?? []).join(", ") || "none"} · Validated transitions:{" "}
                {(workflowDetail.validated_triggers ?? []).length}
              </div>
            </div>
          )}
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-800 p-4 space-y-3">
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
            Session Templates ({templates.length})
          </h3>

          <div className="space-y-2 max-h-52 overflow-y-auto">
            {templates.map((tmpl) => (
              <div
                key={tmpl.name}
                className="flex items-center justify-between gap-2 rounded border border-gray-100 dark:border-gray-800 p-2 text-xs"
              >
                <div>
                  <div className="font-mono font-semibold text-gray-900 dark:text-white">
                    {tmpl.name}{" "}
                    <span className="font-sans font-normal text-gray-500">
                      ({tmpl.agent_count} agents, {tmpl.source})
                    </span>
                  </div>
                  <div className="text-gray-500 dark:text-gray-400">
                    {tmpl.description}
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => setSelectedTemplate(tmpl.name)}
                  className="rounded border border-gray-200 dark:border-gray-700 px-2 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-800"
                >
                  Inspect Template
                </button>
              </div>
            ))}
          </div>

          {templateDetail && (
            <div
              data-testid="session-template-detail-box"
              className="rounded bg-emerald-50/50 dark:bg-emerald-950/20 border border-emerald-200 dark:border-emerald-800 p-3 text-xs space-y-1"
            >
              <div className="font-mono font-semibold text-gray-900 dark:text-white">
                {templateDetail.name} [{templateDetail.source}]
              </div>
              <div className="text-gray-600 dark:text-gray-300">
                Tags: {(templateDetail.tags ?? []).join(", ") || "none"}
              </div>
            </div>
          )}
        </div>
      </div>

      {/* 2. Durable Per-Pane Prompt Sequence Controls */}
      <div className="rounded-lg border border-gray-200 dark:border-gray-800 bg-gray-50/50 dark:bg-gray-950/30 p-4 space-y-3">
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
          Durable Per-Pane Prompt Sequence
        </h3>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
              Sequence Name
            </label>
            <input
              type="text"
              aria-label="Sequence Name"
              value={seqName}
              onChange={(e) => setSeqName(e.target.value)}
              className="w-full rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
              Pane ID
            </label>
            <input
              type="text"
              aria-label="Sequence Pane ID"
              value={seqPane}
              onChange={(e) => setSeqPane(e.target.value)}
              className="w-full rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
              Prompt Steps (one per line)
            </label>
            <textarea
              rows={2}
              aria-label="Sequence Prompt Steps"
              value={seqStepsRaw}
              onChange={(e) => setSeqStepsRaw(e.target.value)}
              className="w-full rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={!seqName.trim() || createSeqMutation.isPending}
            onClick={() => createSeqMutation.mutate()}
            className="rounded bg-blue-600 hover:bg-blue-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
          >
            Create Sequence
          </button>
          <button
            type="button"
            disabled={!seqName.trim() || !seqPane.trim() || inspectNextMutation.isPending}
            onClick={() => inspectNextMutation.mutate()}
            className="rounded border border-gray-300 dark:border-gray-700 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-white dark:hover:bg-gray-800"
          >
            Inspect Next Prompt
          </button>
          <button
            type="button"
            disabled={!seqName.trim() || !seqPane.trim() || advanceSeqMutation.isPending}
            onClick={() => advanceSeqMutation.mutate()}
            className="rounded bg-emerald-600 hover:bg-emerald-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
          >
            Advance Pane Step
          </button>
        </div>

        {paneState && (
          <div className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-xs font-mono text-gray-800 dark:text-gray-200">
            Pane {paneState.pane} · Position {paneState.position}/{paneState.total} ·{" "}
            {paneState.done ? "Completed" : `Prompt: ${paneState.prompt || "—"}`}
          </div>
        )}
        {seqMessage && (
          <div className="text-xs text-blue-700 dark:text-blue-300">{seqMessage}</div>
        )}
      </div>
    </section>
  );
}
