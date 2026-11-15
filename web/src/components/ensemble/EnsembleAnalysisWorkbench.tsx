"use client";

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type components,
} from "@/lib/api/client";

type PresetInfo = components["schemas"]["PresetInfo"];
type EnsembleEstimateRequest =
  components["schemas"]["EnsembleEstimateRequest"];
type EnsembleEstimateResponse =
  components["schemas"]["EnsembleEstimateEnvelopeResponse"];
type EnsembleRunsResponse =
  components["schemas"]["EnsembleRunsEnvelopeResponse"];
type EnsembleCompareRequest =
  components["schemas"]["EnsembleCompareRequest"];
type EnsembleCompareResponse =
  components["schemas"]["EnsembleCompareEnvelopeResponse"];
type EnsembleProvenanceResponse =
  components["schemas"]["EnsembleProvenanceEnvelopeResponse"];
type ProvenanceChain = components["schemas"]["ProvenanceChain"];

export interface EnsembleAnalysisWorkbenchProps {
  presets: PresetInfo[];
  selectedPreset: string;
  question: string;
  inspectSession: string;
}

function parseModeTokens(raw: string): string[] {
  return raw
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part.length > 0);
}

export function EnsembleAnalysisWorkbench({
  presets,
  selectedPreset,
  question,
  inspectSession,
}: EnsembleAnalysisWorkbenchProps) {
  // 1. Token Budget Estimator state
  const [estimateSource, setEstimateSource] = useState<"preset" | "modes">(
    "preset"
  );
  const [customPreset, setCustomPreset] = useState("");
  const [modesInput, setModesInput] = useState("deductive,failure-mode");
  const [budgetOverrideInput, setBudgetOverrideInput] = useState("");
  const [estimateAllowAdvanced, setEstimateAllowAdvanced] = useState(false);
  const [estimateError, setEstimateError] = useState<string | null>(null);

  // 2. Saved Runs & Compare state
  const [runA, setRunA] = useState("");
  const [runB, setRunB] = useState("");
  const [compareError, setCompareError] = useState<string | null>(null);

  // 3. Provenance Inspector state
  const [provenanceTarget, setProvenanceTarget] = useState("");
  const [findingFilter, setFindingFilter] = useState("");
  const [provenanceError, setProvenanceError] = useState<string | null>(null);

  const runsQuery = useQuery({
    queryKey: ["ensemble", "runs"],
    queryFn: () => apiFetch<EnsembleRunsResponse>("/api/v1/ensemble/runs"),
    retry: false,
  });

  const estimateMutation = useMutation({
    mutationFn: (body: EnsembleEstimateRequest) =>
      apiFetch<EnsembleEstimateResponse>("/api/v1/ensemble/estimate", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      setEstimateError(null);
    },
    onError: (err) => {
      setEstimateError(formatApiErrorMessage(err));
    },
  });

  const compareMutation = useMutation({
    mutationFn: (body: EnsembleCompareRequest) =>
      apiFetch<EnsembleCompareResponse>("/api/v1/ensemble/compare", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      setCompareError(null);
    },
    onError: (err) => {
      setCompareError(formatApiErrorMessage(err));
    },
  });

  const provenanceMutation = useMutation({
    mutationFn: ({
      target,
      finding,
    }: {
      target: string;
      finding?: string;
    }) => {
      const qs = finding ? `?finding=${encodeURIComponent(finding)}` : "";
      return apiFetch<EnsembleProvenanceResponse>(
        `/api/v1/ensemble/sessions/${encodeURIComponent(target)}/provenance${qs}`
      );
    },
    onSuccess: () => {
      setProvenanceError(null);
    },
    onError: (err) => {
      setProvenanceError(formatApiErrorMessage(err));
    },
  });

  const effectivePreset = customPreset.trim() || selectedPreset.trim();
  const effectiveProvenanceTarget =
    provenanceTarget.trim() || inspectSession.trim();

  const handleEstimate = () => {
    setEstimateError(null);
    const body: EnsembleEstimateRequest = {};
    if (estimateSource === "preset") {
      if (!effectivePreset) {
        setEstimateError("Select or enter a preset name to estimate.");
        return;
      }
      body.preset = effectivePreset;
    } else {
      const modes = parseModeTokens(modesInput);
      if (modes.length === 0) {
        setEstimateError(
          "Enter at least one cognitive mode ID or code (comma-separated)."
        );
        return;
      }
      body.modes = modes;
    }

    if (budgetOverrideInput.trim().length > 0) {
      const parsedBudget = Number(budgetOverrideInput.trim());
      if (!Number.isInteger(parsedBudget) || parsedBudget <= 0) {
        setEstimateError("Budget override must be a positive integer.");
        return;
      }
      body.budget_total = parsedBudget;
    }

    if (question.trim()) {
      body.question = question.trim();
    }
    if (estimateAllowAdvanced) {
      body.allow_advanced = true;
    }

    estimateMutation.mutate(body);
  };

  const handleCompare = () => {
    setCompareError(null);
    const a = runA.trim();
    const b = runB.trim();
    if (!a || !b) {
      setCompareError(
        "Both Baseline Run (Run A) and Candidate Run (Run B) are required."
      );
      return;
    }
    compareMutation.mutate({ run_a: a, run_b: b });
  };

  const handleLoadProvenance = (explicitTarget?: string) => {
    setProvenanceError(null);
    const target = (explicitTarget ?? effectiveProvenanceTarget).trim();
    if (!target) {
      setProvenanceError(
        "Enter an ensemble session name or checkpoint run ID to inspect provenance."
      );
      return;
    }
    if (explicitTarget) {
      setProvenanceTarget(explicitTarget);
    }
    const filter = findingFilter.trim();
    provenanceMutation.mutate({
      target,
      finding: filter.length > 0 ? filter : undefined,
    });
  };

  const runs = runsQuery.data?.runs ?? [];
  const estimateData = estimateMutation.data;
  const compareData = compareMutation.data;
  const provenanceData = provenanceMutation.data;
  const provenanceChains: ProvenanceChain[] = provenanceData
    ? provenanceData.chains?.length
      ? provenanceData.chains
      : provenanceData.chain
        ? [provenanceData.chain]
        : []
    : [];

  return (
    <section
      aria-label="Ensemble Analysis Workbench"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-6"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold text-gray-900 dark:text-white">
            Ensemble Analysis Workbench
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Pre-flight token budget estimation, saved session &amp; checkpoint
            comparison, and end-to-end finding provenance chains.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void runsQuery.refetch()}
          className="px-2.5 py-1 text-xs rounded border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
        >
          Refresh Saved Runs
        </button>
      </div>

      {/* 1. Token Budget Estimator + 2. Saved Runs & Compare */}
      <div className="grid grid-cols-1 xl:grid-cols-2 gap-5">
        {/* Token Budget Estimator */}
        <div
          role="region"
          aria-label="Token Budget Estimator"
          className="rounded-lg border border-gray-200 dark:border-gray-700 p-4 space-y-3"
        >
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Token Budget Estimator
            </h3>
            <span className="text-[11px] font-mono text-gray-500 dark:text-gray-400">
              POST /api/v1/ensemble/estimate
            </span>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label
                htmlFor="estimate-source-select"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Estimate Target
              </label>
              <select
                id="estimate-source-select"
                value={estimateSource}
                onChange={(e) =>
                  setEstimateSource(e.target.value as "preset" | "modes")
                }
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
              >
                <option value="preset">Preset</option>
                <option value="modes">Explicit Modes</option>
              </select>
            </div>

            {estimateSource === "preset" ? (
              <div>
                <label
                  htmlFor="estimate-preset-select"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Estimate Preset
                </label>
                <select
                  id="estimate-preset-select"
                  value={effectivePreset}
                  onChange={(e) => setCustomPreset(e.target.value)}
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
                >
                  {presets.length === 0 && (
                    <option value={effectivePreset}>
                      {effectivePreset || "project-diagnosis"}
                    </option>
                  )}
                  {presets.map((p) => (
                    <option key={p.name} value={p.name}>
                      {p.display || p.name} ({p.mode_count} modes)
                    </option>
                  ))}
                </select>
              </div>
            ) : (
              <div>
                <label
                  htmlFor="estimate-modes-input"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Explicit Mode IDs / Codes
                </label>
                <input
                  id="estimate-modes-input"
                  type="text"
                  value={modesInput}
                  onChange={(e) => setModesInput(e.target.value)}
                  placeholder="deductive,failure-mode"
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
                />
              </div>
            )}
          </div>

          <div className="flex flex-wrap items-end justify-between gap-3">
            <div className="w-44">
              <label
                htmlFor="estimate-budget-override"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Budget Cap Override
              </label>
              <input
                id="estimate-budget-override"
                type="number"
                min={1}
                value={budgetOverrideInput}
                onChange={(e) => setBudgetOverrideInput(e.target.value)}
                placeholder="Default cap"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
              />
            </div>

            <label className="inline-flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300 pb-1">
              <input
                type="checkbox"
                checked={estimateAllowAdvanced}
                onChange={(e) => setEstimateAllowAdvanced(e.target.checked)}
                className="rounded border-gray-300 dark:border-gray-600"
              />
              Allow non-core modes
            </label>

            <button
              type="button"
              onClick={handleEstimate}
              disabled={estimateMutation.isPending}
              className="px-3 py-1.5 text-xs font-medium rounded-md bg-indigo-600 text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {estimateMutation.isPending ? "Estimating..." : "Estimate Budget"}
            </button>
          </div>

          {estimateError && (
            <div className="rounded border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 p-2.5 text-xs text-red-700 dark:text-red-300">
              {estimateError}
            </div>
          )}

          {estimateData && (
            <div className="space-y-2.5 pt-2 border-t border-gray-100 dark:border-gray-700">
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
                <div className="flex items-center gap-2">
                  <span
                    data-testid="estimate-budget-status"
                    className={`rounded-full px-2.5 py-0.5 font-medium ${
                      estimateData.over_budget
                        ? "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-200"
                        : "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200"
                    }`}
                  >
                    {estimateData.over_budget ? "Over Budget" : "Within Budget"}
                  </span>
                  {estimateData.preset_label && (
                    <span className="font-medium text-gray-700 dark:text-gray-200">
                      {estimateData.preset_label}
                    </span>
                  )}
                </div>
                <span className="font-mono text-gray-700 dark:text-gray-200">
                  {estimateData.budget.estimated_total_tokens.toLocaleString()}{" "}
                  / {estimateData.budget.max_total_tokens.toLocaleString()}{" "}
                  tokens ({estimateData.budget.mode_count} modes)
                </span>
              </div>

              {estimateData.warnings && estimateData.warnings.length > 0 && (
                <div className="rounded border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-900/20 p-2 text-xs text-amber-800 dark:text-amber-200 space-y-1">
                  {estimateData.warnings.map((w, idx) => (
                    <div key={`warn-${idx}`}>{w}</div>
                  ))}
                </div>
              )}

              <div className="max-h-40 overflow-y-auto space-y-1">
                {estimateData.modes.map((m) => (
                  <div
                    key={m.mode_id}
                    className="flex items-center justify-between rounded bg-gray-50 dark:bg-gray-900/60 px-2.5 py-1 text-xs"
                  >
                    <span className="text-gray-800 dark:text-gray-200">
                      {m.mode_code ? `[${m.mode_code}] ` : ""}
                      {m.mode_name || m.mode_id}
                    </span>
                    <span className="font-mono text-gray-600 dark:text-gray-300">
                      {m.token_estimate.toLocaleString()} tok
                      {typeof m.value_score === "number" && m.value_score > 0
                        ? ` · val ${m.value_score.toFixed(2)}`
                        : ""}
                    </span>
                  </div>
                ))}
              </div>

              {estimateData.suggestions &&
                estimateData.suggestions.length > 0 && (
                  <div className="rounded border border-blue-200 dark:border-blue-800 bg-blue-50/70 dark:bg-blue-900/20 p-2.5 text-xs space-y-1">
                    <div className="font-semibold text-blue-900 dark:text-blue-200">
                      Lower-Token Mode Substitutions
                    </div>
                    {estimateData.suggestions.map((sug, idx) => (
                      <div
                        key={`sug-${idx}`}
                        className="text-blue-800 dark:text-blue-200"
                      >
                        Replace{" "}
                        <span className="font-mono">
                          {sug.replace_mode_name || sug.replace_mode_id}
                        </span>{" "}
                        ({sug.replace_tokens} tok) with{" "}
                        <span className="font-mono">
                          {sug.suggested_mode_name || sug.suggested_mode_id}
                        </span>{" "}
                        ({sug.suggested_tokens} tok) &mdash; saves{" "}
                        <span className="font-mono">{sug.savings_tokens}</span>{" "}
                        tokens
                      </div>
                    ))}
                  </div>
                )}
            </div>
          )}
        </div>

        {/* Saved Runs & Side-by-Side Comparison */}
        <div
          role="region"
          aria-label="Saved Runs & Comparison"
          className="rounded-lg border border-gray-200 dark:border-gray-700 p-4 space-y-3"
        >
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Saved Runs &amp; Differential Comparison
            </h3>
            <span className="text-xs font-mono text-gray-500 dark:text-gray-400">
              {runs.length} runs
            </span>
          </div>

          {runsQuery.isError ? (
            <div className="rounded border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-900/20 p-2.5 text-xs text-amber-800 dark:text-amber-200">
              Saved runs unavailable: {formatApiErrorMessage(runsQuery.error)}
            </div>
          ) : runs.length === 0 ? (
            <div className="rounded border border-dashed border-gray-200 dark:border-gray-700 p-3 text-center text-xs text-gray-500 dark:text-gray-400">
              No persisted ensemble sessions or checkpoints yet. Enter run IDs
              below or spawn an ensemble to populate saved runs.
            </div>
          ) : (
            <div className="max-h-36 overflow-y-auto space-y-1.5">
              {runs.map((run) => (
                <div
                  key={`${run.source}-${run.run_id}`}
                  data-testid={`saved-run-${run.run_id}`}
                  className="rounded border border-gray-200 dark:border-gray-700 px-2.5 py-1.5 flex flex-wrap items-center justify-between gap-2 text-xs"
                >
                  <div className="flex flex-wrap items-center gap-1.5 min-w-0">
                    <span className="font-mono font-semibold text-gray-900 dark:text-white">
                      {run.run_id}
                    </span>
                    <span className="rounded bg-gray-100 dark:bg-gray-700 px-1.5 py-0.5 font-mono text-[11px] text-gray-600 dark:text-gray-300">
                      {run.source}
                    </span>
                    <span className="text-gray-500 dark:text-gray-400">
                      {run.completed_modes}/{run.total_modes} modes &middot;{" "}
                      {run.status}
                    </span>
                    {run.has_synthesis && (
                      <span className="rounded bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200 px-1.5 py-0.5 text-[11px]">
                        synthesized
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-1">
                    <button
                      type="button"
                      onClick={() => setRunA(run.run_id)}
                      className="px-1.5 py-0.5 rounded border border-gray-300 dark:border-gray-600 text-[11px] text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
                    >
                      Run A
                    </button>
                    <button
                      type="button"
                      onClick={() => setRunB(run.run_id)}
                      className="px-1.5 py-0.5 rounded border border-gray-300 dark:border-gray-600 text-[11px] text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
                    >
                      Run B
                    </button>
                    <button
                      type="button"
                      onClick={() => handleLoadProvenance(run.run_id)}
                      className="px-1.5 py-0.5 rounded border border-blue-300 dark:border-blue-700 text-[11px] text-blue-700 dark:text-blue-300 hover:bg-blue-50 dark:hover:bg-blue-900/30"
                    >
                      Provenance
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}

          <datalist id="ensemble-saved-runs-datalist">
            {runs.map((r) => (
              <option key={`dl-${r.source}-${r.run_id}`} value={r.run_id} />
            ))}
          </datalist>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5 pt-1">
            <div>
              <label
                htmlFor="compare-run-a"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Baseline Run (Run A)
              </label>
              <input
                id="compare-run-a"
                list="ensemble-saved-runs-datalist"
                type="text"
                value={runA}
                onChange={(e) => setRunA(e.target.value)}
                placeholder="Session or checkpoint ID"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
              />
            </div>

            <div>
              <label
                htmlFor="compare-run-b"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Candidate Run (Run B)
              </label>
              <input
                id="compare-run-b"
                list="ensemble-saved-runs-datalist"
                type="text"
                value={runB}
                onChange={(e) => setRunB(e.target.value)}
                placeholder="Session or checkpoint ID"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
              />
            </div>
          </div>

          <div className="flex justify-end">
            <button
              type="button"
              onClick={handleCompare}
              disabled={compareMutation.isPending}
              className="px-3 py-1.5 text-xs font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {compareMutation.isPending ? "Comparing..." : "Compare Runs"}
            </button>
          </div>

          {compareError && (
            <div className="rounded border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 p-2.5 text-xs text-red-700 dark:text-red-300">
              {compareError}
            </div>
          )}

          {compareData && (
            <div className="space-y-2 pt-2 border-t border-gray-100 dark:border-gray-700 text-xs">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="font-mono font-semibold text-gray-900 dark:text-white">
                  {compareData.run_a} &rarr; {compareData.run_b}
                </span>
                <span
                  className={`rounded-full px-2 py-0.5 font-medium ${
                    compareData.unchanged
                      ? "bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-200"
                      : "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200"
                  }`}
                >
                  {compareData.unchanged
                    ? "Identical"
                    : `Changed: ${(compareData.changed_sections ?? []).join(", ") || "diff"}`}
                </span>
              </div>

              <div className="rounded bg-gray-50 dark:bg-gray-900/60 px-2.5 py-1.5 font-mono text-gray-700 dark:text-gray-200">
                {compareData.summary}
              </div>

              {compareData.result && (
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                  <div className="rounded border border-gray-200 dark:border-gray-700 p-2 space-y-1">
                    <div className="font-semibold text-gray-800 dark:text-gray-200">
                      Modes Delta
                    </div>
                    <div className="text-gray-600 dark:text-gray-300">
                      +{compareData.result.mode_diff.added_count} added &middot;
                      -{compareData.result.mode_diff.removed_count} removed
                      &middot; ={compareData.result.mode_diff.unchanged_count}{" "}
                      unchanged
                    </div>
                    {(compareData.result.mode_diff.added ?? []).length > 0 && (
                      <div className="font-mono text-emerald-700 dark:text-emerald-300">
                        + {(compareData.result.mode_diff.added ?? []).join(", ")}
                      </div>
                    )}
                    {(compareData.result.mode_diff.removed ?? []).length >
                      0 && (
                      <div className="font-mono text-red-700 dark:text-red-300">
                        -{" "}
                        {(compareData.result.mode_diff.removed ?? []).join(
                          ", "
                        )}
                      </div>
                    )}
                  </div>

                  <div className="rounded border border-gray-200 dark:border-gray-700 p-2 space-y-1">
                    <div className="font-semibold text-gray-800 dark:text-gray-200">
                      Findings Delta
                    </div>
                    <div className="text-gray-600 dark:text-gray-300">
                      +{compareData.result.findings_diff.new_count} new &middot;
                      -{compareData.result.findings_diff.missing_count} missing
                      &middot; ~{compareData.result.findings_diff.changed_count}{" "}
                      changed
                    </div>
                    {(compareData.result.findings_diff.new ?? [])
                      .slice(0, 3)
                      .map((entry) => (
                        <div
                          key={`new-${entry.finding_id}`}
                          className="text-emerald-700 dark:text-emerald-300 truncate"
                        >
                          + [{entry.mode_id}] {entry.text}
                        </div>
                      ))}
                    {(compareData.result.findings_diff.missing ?? [])
                      .slice(0, 3)
                      .map((entry) => (
                        <div
                          key={`miss-${entry.finding_id}`}
                          className="text-red-700 dark:text-red-300 truncate"
                        >
                          - [{entry.mode_id}] {entry.text}
                        </div>
                      ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      {/* 3. Finding Provenance Chain Inspector */}
      <div
        role="region"
        aria-label="Finding Provenance Inspector"
        className="rounded-lg border border-gray-200 dark:border-gray-700 p-4 space-y-3"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Finding Provenance Chain Inspector
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Trace discovery, deduplication merges, and synthesis citations for
              every finding in a session or checkpoint run.
            </p>
          </div>
          <span className="text-[11px] font-mono text-gray-500 dark:text-gray-400">
            GET /api/v1/ensemble/sessions/&#123;session&#125;/provenance
          </span>
        </div>

        <div className="flex flex-wrap items-end gap-3">
          <div className="flex-1 min-w-[180px]">
            <label
              htmlFor="provenance-session-input"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Provenance Session or Run ID
            </label>
            <input
              id="provenance-session-input"
              list="ensemble-saved-runs-datalist"
              type="text"
              value={effectiveProvenanceTarget}
              onChange={(e) => setProvenanceTarget(e.target.value)}
              placeholder="Session name or checkpoint run_id"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>

          <div className="w-52">
            <label
              htmlFor="provenance-finding-filter"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Finding ID Filter
            </label>
            <input
              id="provenance-finding-filter"
              type="text"
              value={findingFilter}
              onChange={(e) => setFindingFilter(e.target.value)}
              placeholder="Optional finding ID/prefix"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>

          <button
            type="button"
            onClick={() => handleLoadProvenance()}
            disabled={provenanceMutation.isPending}
            className="px-3 py-1.5 text-xs font-medium rounded-md bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50"
          >
            {provenanceMutation.isPending
              ? "Loading Provenance..."
              : "Load Provenance"}
          </button>
        </div>

        {provenanceError && (
          <div className="rounded border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 p-2.5 text-xs text-red-700 dark:text-red-300">
            {provenanceError}
          </div>
        )}

        {provenanceData && (
          <div className="space-y-3 pt-2 border-t border-gray-100 dark:border-gray-700">
            <div className="flex flex-wrap items-center gap-3 text-xs">
              <span className="rounded bg-gray-100 dark:bg-gray-700 px-2 py-0.5 font-mono text-gray-800 dark:text-gray-200">
                Total: {provenanceData.stats.total_findings}
              </span>
              <span className="rounded bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200 px-2 py-0.5 font-mono">
                Active: {provenanceData.stats.active_findings}
              </span>
              <span className="rounded bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200 px-2 py-0.5 font-mono">
                Merged: {provenanceData.stats.merged_findings}
              </span>
              <span className="rounded bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-200 px-2 py-0.5 font-mono">
                Cited: {provenanceData.stats.cited_count}
              </span>
            </div>

            {provenanceChains.length === 0 ? (
              <div className="text-xs text-gray-500 dark:text-gray-400">
                No findings matched the current provenance query.
              </div>
            ) : (
              <div className="space-y-2 max-h-64 overflow-y-auto">
                {provenanceChains.map((chain) => (
                  <div
                    key={chain.finding_id}
                    data-testid={`provenance-chain-${chain.finding_id}`}
                    className="rounded border border-gray-200 dark:border-gray-700 p-3 space-y-1.5 text-xs"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-mono font-semibold text-gray-900 dark:text-white">
                          {chain.finding_id}
                        </span>
                        <span className="rounded bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200 px-1.5 py-0.5 font-mono">
                          {chain.source_mode}
                        </span>
                        <span className="uppercase text-[11px] font-medium text-gray-600 dark:text-gray-300">
                          {chain.impact}
                        </span>
                      </div>
                      <span className="font-mono text-gray-600 dark:text-gray-300">
                        {Math.round(chain.confidence * 100)}% confidence
                      </span>
                    </div>

                    <div className="text-gray-800 dark:text-gray-200">
                      {chain.current_text}
                    </div>

                    {(chain.synthesis_citations ?? []).length > 0 && (
                      <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-purple-700 dark:text-purple-300">
                        <span className="font-medium">Citations:</span>
                        {(chain.synthesis_citations ?? []).map((cite) => (
                          <span
                            key={`${chain.finding_id}-${cite}`}
                            className="rounded bg-purple-50 dark:bg-purple-900/30 px-1.5 py-0.5 font-mono"
                          >
                            {cite}
                          </span>
                        ))}
                      </div>
                    )}

                    {chain.steps.length > 0 && (
                      <div className="space-y-0.5 pt-1 border-t border-gray-100 dark:border-gray-700/70 text-[11px] text-gray-600 dark:text-gray-400">
                        {chain.steps.map((step, idx) => (
                          <div key={`${chain.finding_id}-step-${idx}`}>
                            <span className="font-mono">[{step.stage}]</span>{" "}
                            <span className="font-medium">{step.action}</span>
                            {step.details ? ` — ${step.details}` : ""}
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
