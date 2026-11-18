"use client";

/**
 * GitHooksAndRedactionPanel
 *
 * Surfaces repository git hook inspection & execution plus zero-leak secret/PII
 * redaction preview on /safety:
 *   - GET  /api/v1/hooks
 *   - POST /api/v1/hooks/run
 *   - POST /api/v1/redact/preview
 */

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface HooksStatusResponse extends ApiEnvelope {
  action: string;
  repo_root?: string;
  hooks_dir?: string;
  installed_count?: number;
  hooks?: Array<{
    type: string;
    path: string;
    installed: boolean;
    is_ntm: boolean;
    has_backup: boolean;
  }>;
  hook_type?: string;
  passed?: boolean;
  pre_commit_result?: {
    passed: boolean;
    staged_files?: string[];
    block_reason?: string;
  };
  check_result?: {
    hook_type: string;
    passed: boolean;
    warnings?: string[];
    block_reason?: string;
  };
}

export interface RedactPreviewResponse extends ApiEnvelope {
  input_len: number;
  finding_count: number;
  output: string;
  findings: Array<{
    category: string;
    redacted: string;
    line?: number;
    column?: number;
    start: number;
    end: number;
  }>;
}

export function GitHooksAndRedactionPanel() {
  const [selectedHookType, setSelectedHookType] = useState("pre-commit");
  const [failOnWarning, setFailOnWarning] = useState(true);
  const [hookRunResult, setHookRunResult] =
    useState<HooksStatusResponse | null>(null);

  const [sampleText, setSampleText] = useState("");
  const [redactResult, setRedactResult] =
    useState<RedactPreviewResponse | null>(null);

  const hooksQuery = useQuery({
    queryKey: ["safety", "hooks"],
    queryFn: () => apiFetch<HooksStatusResponse>("/api/v1/hooks"),
    retry: false,
  });

  const runHookMutation = useMutation({
    mutationFn: () =>
      apiFetch<HooksStatusResponse>("/api/v1/hooks/run", {
        method: "POST",
        body: JSON.stringify({
          hook_type: selectedHookType,
          fail_on_warning: failOnWarning,
        }),
      }),
    onSuccess: (data) => setHookRunResult(data),
  });

  const redactMutation = useMutation({
    mutationFn: () =>
      apiFetch<RedactPreviewResponse>("/api/v1/redact/preview", {
        method: "POST",
        body: JSON.stringify({ text: sampleText }),
      }),
    onSuccess: (data) => setRedactResult(data),
  });

  const hooks = hooksQuery.data?.hooks ?? [];

  return (
    <section
      aria-label="Git Hooks and Secret Redaction Preview"
      className="rounded-lg border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800 space-y-5"
    >
      <div>
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
          Repository Git Hooks &amp; Secret Redaction Sandbox
        </h2>
        <p className="text-xs text-gray-500 dark:text-gray-400">
          Inspect NTM-managed git hooks, execute quality/guard checks, and preview secret/PII scrubbing without ever exposing raw tokens.
        </p>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-5">
        {/* Git Hooks Status & Runner */}
        <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <span className="text-sm font-medium text-gray-900 dark:text-white">
              Git Hooks ({hooksQuery.data?.installed_count ?? 0}/{hooks.length}{" "}
              installed)
            </span>
            <button
              type="button"
              onClick={() => void hooksQuery.refetch()}
              className="rounded border border-gray-300 dark:border-gray-600 px-2.5 py-1 text-xs text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
            >
              Refresh Hooks
            </button>
          </div>
          {hooksQuery.isError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {formatApiErrorMessage(hooksQuery.error)}
            </div>
          )}
          <div className="space-y-1.5 text-xs">
            {hooks.map((h) => (
              <div
                key={h.type}
                className="flex items-center justify-between rounded bg-gray-50 dark:bg-gray-900/50 px-2.5 py-1.5"
              >
                <span className="font-mono">{h.type}</span>
                <span
                  className={
                    h.installed
                      ? "text-green-700 dark:text-green-400"
                      : "text-gray-500"
                  }
                >
                  {h.installed
                    ? h.is_ntm
                      ? "installed (ntm)"
                      : "installed (external)"
                    : "not installed"}
                </span>
              </div>
            ))}
          </div>

          <div className="flex flex-wrap items-center gap-2 pt-2 border-t border-gray-100 dark:border-gray-700">
            <select
              aria-label="Hook Type to Run"
              value={selectedHookType}
              onChange={(e) => setSelectedHookType(e.target.value)}
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            >
              <option value="pre-commit">pre-commit</option>
              <option value="pre-push">pre-push</option>
              <option value="post-commit">post-commit</option>
              <option value="post-checkout">post-checkout</option>
            </select>
            <label className="inline-flex items-center gap-1.5 text-xs text-gray-700 dark:text-gray-300">
              <input
                type="checkbox"
                checked={failOnWarning}
                onChange={(e) => setFailOnWarning(e.target.checked)}
              />
              Fail on warning
            </label>
            <button
              type="button"
              disabled={runHookMutation.isPending}
              onClick={() => runHookMutation.mutate()}
              className="rounded bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              Run Hook Check
            </button>
          </div>
          {runHookMutation.isError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {formatApiErrorMessage(runHookMutation.error)}
            </div>
          )}
          {hookRunResult && (
            <div className="rounded bg-gray-50 dark:bg-gray-900/50 p-2.5 text-xs text-gray-800 dark:text-gray-200">
              Hook <span className="font-mono">{hookRunResult.hook_type}</span>:{" "}
              <span className="font-semibold">
                {hookRunResult.passed ? "PASSED" : "BLOCKED"}
              </span>
              {(hookRunResult.pre_commit_result?.block_reason ||
                hookRunResult.check_result?.block_reason) && (
                <div className="mt-1 text-red-600 dark:text-red-400">
                  {hookRunResult.pre_commit_result?.block_reason ||
                    hookRunResult.check_result?.block_reason}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Secret Redaction Sandbox */}
        <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-3">
          <div className="text-sm font-medium text-gray-900 dark:text-white">
            Safe Secret &amp; PII Redaction Preview
          </div>
          <textarea
            aria-label="Redaction Input Text"
            rows={4}
            value={sampleText}
            onChange={(e) => setSampleText(e.target.value)}
            placeholder="Paste prompt, log excerpt, or config snippet to test secret redaction..."
            className="w-full rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 p-2.5 font-mono text-xs text-gray-900 dark:text-white"
          />
          <button
            type="button"
            disabled={!sampleText.trim() || redactMutation.isPending}
            onClick={() => redactMutation.mutate()}
            className="rounded bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
          >
            Preview Redaction
          </button>
          {redactMutation.isError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {formatApiErrorMessage(redactMutation.error)}
            </div>
          )}
          {redactResult && (
            <div className="space-y-2 rounded bg-gray-50 dark:bg-gray-900/50 p-3 text-xs">
              <div className="font-medium text-gray-900 dark:text-white">
                Findings: {redactResult.finding_count} ({redactResult.input_len}{" "}
                bytes)
              </div>
              {(redactResult.findings ?? []).map((f, idx) => (
                <div key={idx} className="font-mono text-[11px]">
                  {f.line ? `${f.line}:${f.column}` : `${f.start}-${f.end}`}{" "}
                  [{f.category}] {f.redacted}
                </div>
              ))}
              <pre className="overflow-x-auto rounded bg-gray-900 p-2 font-mono text-[11px] text-gray-100">
                {redactResult.output}
              </pre>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
