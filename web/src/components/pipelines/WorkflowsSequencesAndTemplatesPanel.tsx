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
  formatApiErrorMessage,
  typedGet,
  typedPost,
  type components,
} from "@/lib/api/client";

type SequencePaneState = NonNullable<
  components["schemas"]["SequenceEnvelopeResponse"]["pane"]
>;

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

  // Workflow runtime control state (/workflows/{name}/run|trigger|stop)
  const [wfSession, setWfSession] = useState("ntm_live");
  const [wfDryRun, setWfDryRun] = useState(false);
  const [wfGateToken, setWfGateToken] = useState("");
  const [wfTransitionIdx, setWfTransitionIdx] = useState("0");
  const [wfReason, setWfReason] = useState("Approved via Web Cockpit");
  const [wfControlMsg, setWfControlMsg] = useState<string | null>(null);

  const workflowsQuery = useQuery({
    queryKey: ["workflows-catalog", roleFilter],
    queryFn: () =>
      typedGet("/api/v1/workflows", {
        params: {
          query: roleFilter.trim() ? { role: roleFilter.trim() } : undefined,
        },
      }),
  });

  const workflowDetailQuery = useQuery({
    queryKey: ["workflow-detail", selectedWorkflow],
    queryFn: () =>
      typedGet("/api/v1/workflows/{name}", {
        params: { path: { name: selectedWorkflow } },
      }),
    enabled: Boolean(selectedWorkflow),
  });

  const templatesQuery = useQuery({
    queryKey: ["session-templates-catalog"],
    queryFn: () => typedGet("/api/v1/session-templates"),
  });

  const templateDetailQuery = useQuery({
    queryKey: ["session-template-detail", selectedTemplate],
    queryFn: () =>
      typedGet("/api/v1/session-templates/{name}", {
        params: { path: { name: selectedTemplate } },
      }),
    enabled: Boolean(selectedTemplate),
  });

  const createSeqMutation = useMutation({
    mutationFn: () => {
      const steps = seqStepsRaw
        .split("\n")
        .map((s) => s.trim())
        .filter(Boolean);
      return typedPost("/api/v1/sequences", {
        body: { name: seqName.trim(), steps },
      });
    },
    onSuccess: (res) => {
      setSeqMessage(
        `Created sequence "${res.sequence?.name || seqName}" (${res.sequence?.steps?.length ?? 0} steps).`
      );
    },
    onError: (err) => {
      setSeqMessage(`Create sequence failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const inspectNextMutation = useMutation({
    mutationFn: () =>
      typedGet("/api/v1/sequences/{name}/next", {
        params: {
          path: { name: seqName.trim() },
          query: { pane: seqPane.trim() },
        },
      }),
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
      typedPost("/api/v1/sequences/{name}/advance", {
        params: { path: { name: seqName.trim() } },
        body: {
          pane: seqPane.trim(),
          ...(paneState ? { expected_position: paneState.position } : {}),
        },
      }),
    onSuccess: (res) => {
      if (res.pane) {
        setPaneState(res.pane);
        setSeqMessage(
          `Advanced pane ${res.pane.pane} to step ${res.pane.position}${res.pane.complete ? " (completed)" : ""}.`
        );
      }
    },
    onError: (err) => {
      setSeqMessage(`Sequence advance failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const runWorkflowMutation = useMutation({
    mutationFn: (name: string) =>
      typedPost("/api/v1/workflows/{name}/run", {
        params: { path: { name } },
        body: {
          session: wfSession.trim(),
          dry_run: wfDryRun,
        },
      }),
    onSuccess: (res, name) => {
      if (res.manual_gate?.token) {
        setWfGateToken(res.manual_gate.token);
      }
      setWfControlMsg(
        `Workflow "${name}" ${wfDryRun ? "validated (dry-run)" : "started"} for session "${wfSession.trim()}" at stage "${res.stage || "initial"}".`
      );
    },
    onError: (err) => {
      setWfControlMsg(`Run workflow failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const triggerWorkflowMutation = useMutation({
    mutationFn: () =>
      typedPost("/api/v1/workflows/{name}/trigger", {
        params: { path: { name: wfSession.trim() } },
        body: {
          token: wfGateToken.trim(),
          transition: Number.parseInt(wfTransitionIdx, 10) || 0,
          reason: wfReason.trim(),
        },
      }),
    onSuccess: (res) => {
      setWfControlMsg(
        `Recorded manual transition for "${wfSession.trim()}" -> "${res.decision?.to || "next"}".`
      );
    },
    onError: (err) => {
      setWfControlMsg(`Trigger gate failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const stopWorkflowMutation = useMutation({
    mutationFn: () =>
      typedPost("/api/v1/workflows/{name}/stop", {
        params: { path: { name: wfSession.trim() } },
        body: { reason: wfReason.trim() || "Stopped via Web Cockpit" },
      }),
    onSuccess: () => {
      setWfControlMsg(`Paused/stopped workflow checkpoint for session "${wfSession.trim()}".`);
    },
    onError: (err) => {
      setWfControlMsg(`Stop workflow failed: ${formatApiErrorMessage(err)}`);
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
            Pane {paneState.pane} · Position {paneState.position} ·{" "}
            {paneState.complete ? "Completed" : `Prompt: ${paneState.prompt || "—"}`}
          </div>
        )}
        {seqMessage && (
          <div className="text-xs text-blue-700 dark:text-blue-300">{seqMessage}</div>
        )}
      </div>

      {/* 3. Multi-Agent Workflow Runtime Controls (Run / Trigger Gate / Stop) */}
      <div
        data-testid="workflow-runtime-controls"
        className="rounded-lg border border-gray-200 dark:border-gray-800 bg-gray-50/50 dark:bg-gray-950/30 p-4 space-y-3"
      >
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
          Workflow Runtime Control (Run, Trigger Manual Gate &amp; Stop)
        </h3>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-3">
          <div>
            <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
              Target Session
            </label>
            <input
              type="text"
              aria-label="Workflow Target Session"
              value={wfSession}
              onChange={(e) => setWfSession(e.target.value)}
              className="w-full rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
              Manual Gate Token
            </label>
            <input
              type="text"
              aria-label="Workflow Gate Token"
              value={wfGateToken}
              onChange={(e) => setWfGateToken(e.target.value)}
              placeholder="gate-token"
              className="w-full rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
              Transition Index
            </label>
            <input
              type="number"
              min={0}
              aria-label="Workflow Transition Index"
              value={wfTransitionIdx}
              onChange={(e) => setWfTransitionIdx(e.target.value)}
              className="w-full rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1">
              Operator Reason
            </label>
            <input
              type="text"
              aria-label="Workflow Operator Reason"
              value={wfReason}
              onChange={(e) => setWfReason(e.target.value)}
              className="w-full rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            />
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-1.5 text-xs text-gray-700 dark:text-gray-300 mr-2">
            <input
              type="checkbox"
              checked={wfDryRun}
              onChange={(e) => setWfDryRun(e.target.checked)}
            />
            Dry-run only
          </label>
          <button
            type="button"
            disabled={!(selectedWorkflow || workflows[0]?.name) || !wfSession.trim() || runWorkflowMutation.isPending}
            onClick={() => runWorkflowMutation.mutate(selectedWorkflow || workflows[0]?.name || "")}
            className="rounded bg-blue-600 hover:bg-blue-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
          >
            Run Workflow
          </button>
          <button
            type="button"
            disabled={!wfSession.trim() || !wfGateToken.trim() || !wfReason.trim() || triggerWorkflowMutation.isPending}
            onClick={() => triggerWorkflowMutation.mutate()}
            className="rounded bg-emerald-600 hover:bg-emerald-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
          >
            Trigger Manual Gate
          </button>
          <button
            type="button"
            disabled={!wfSession.trim() || stopWorkflowMutation.isPending}
            onClick={() => stopWorkflowMutation.mutate()}
            className="rounded bg-rose-600 hover:bg-rose-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
          >
            Stop Workflow
          </button>
        </div>
        {wfControlMsg && (
          <div className="text-xs text-blue-700 dark:text-blue-300">{wfControlMsg}</div>
        )}
      </div>
    </section>
  );
}
