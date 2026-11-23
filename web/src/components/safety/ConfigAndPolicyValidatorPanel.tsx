"use client";

/**
 * ConfigAndPolicyValidatorPanel
 *
 * Surfaces full NTM configuration, recipes, personas, policy, and 11-harness
 * executable validation with optional directory auto-fix:
 *   - GET  /api/v1/config/validate?all=true|false
 *   - POST /api/v1/config/validate
 */

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface ConfigValidationIssueItem {
  field?: string;
  message: string;
  fixable?: boolean;
}

export interface ConfigValidationFileResult {
  path: string;
  type: string;
  valid: boolean;
  errors?: ConfigValidationIssueItem[];
  warnings?: ConfigValidationIssueItem[];
  info?: string[];
}

export interface ConfigValidateResponse extends ApiEnvelope {
  valid: boolean;
  all?: boolean;
  fix_applied?: boolean;
  validation_error?: string;
  results: ConfigValidationFileResult[];
  summary: {
    files_checked: number;
    error_count: number;
    warning_count: number;
    fixable_count: number;
  };
}

export function ConfigAndPolicyValidatorPanel() {
  const queryClient = useQueryClient();
  const [checkAll, setCheckAll] = useState(false);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  const validateQuery = useQuery({
    queryKey: ["safety-config-validate", checkAll],
    queryFn: () =>
      apiFetch<ConfigValidateResponse>(
        `/api/v1/config/validate?all=${checkAll ? "true" : "false"}`
      ),
  });

  const fixMutation = useMutation({
    mutationFn: (applyFix: boolean) =>
      apiFetch<ConfigValidateResponse>("/api/v1/config/validate", {
        method: "POST",
        body: JSON.stringify({ all: checkAll, fix: applyFix }),
      }),
    onSuccess: (res, applyFix) => {
      queryClient.setQueryData(["safety-config-validate", checkAll], res);
      setStatusMessage(
        applyFix
          ? `Validation & auto-fix completed (${res.summary.files_checked} files checked).`
          : `Validation completed (${res.summary.files_checked} files checked).`
      );
    },
    onError: (err) => {
      setStatusMessage(`Validation request failed: ${formatApiErrorMessage(err)}`);
    },
  });

  const report = validateQuery.data;

  return (
    <section
      aria-label="Configuration, Manifest & 12-Harness Validator"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="text-base font-semibold text-gray-900 dark:text-white">
            Configuration, Recipes, Personas &amp; 12-Harness Validator
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Validate main config.toml, .ntm/config.toml, recipes.toml,
            personas.toml, policy.yaml, and configured agent launch commands.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <label className="inline-flex items-center gap-1.5 text-xs text-gray-600 dark:text-gray-300">
            <input
              type="checkbox"
              checked={checkAll}
              onChange={(e) => setCheckAll(e.target.checked)}
            />
            Check optional locations
          </label>
          <button
            type="button"
            onClick={() => fixMutation.mutate(false)}
            disabled={fixMutation.isPending}
            className="rounded-md border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50 dark:border-gray-600 dark:text-gray-200 dark:hover:bg-gray-700"
          >
            Re-Validate
          </button>
          <button
            type="button"
            onClick={() => fixMutation.mutate(true)}
            disabled={fixMutation.isPending}
            className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            Auto-Fix Directories (--fix)
          </button>
        </div>
      </div>

      {statusMessage && (
        <div className="rounded border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-800 dark:border-blue-800 dark:bg-blue-900/20 dark:text-blue-200">
          {statusMessage}
        </div>
      )}

      {report && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-3 text-xs">
            <span
              className={`rounded-full px-2.5 py-0.5 font-medium ${
                report.valid
                  ? "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-300"
                  : "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300"
              }`}
            >
              {report.valid ? "Valid" : "Errors Detected"}
            </span>
            <span className="text-gray-600 dark:text-gray-300">
              Files: {report.summary.files_checked} · Errors:{" "}
              {report.summary.error_count} · Warnings:{" "}
              {report.summary.warning_count} · Fixable:{" "}
              {report.summary.fixable_count}
            </span>
          </div>

          <div className="space-y-2">
            {report.results.map((res) => (
              <div
                key={`${res.type}-${res.path}`}
                className="rounded border border-gray-200 bg-gray-50 p-3 text-xs dark:border-gray-700 dark:bg-gray-900/50"
              >
                <div className="flex items-center justify-between font-medium">
                  <span className="font-mono text-gray-900 dark:text-white">
                    {res.path}
                  </span>
                  <span className="uppercase text-gray-500">{res.type}</span>
                </div>
                {res.errors?.map((e, idx) => (
                  <div
                    key={`err-${idx}`}
                    className="mt-1 text-red-700 dark:text-red-300"
                  >
                    ✗ {e.field ? `${e.field}: ` : ""}
                    {e.message}
                  </div>
                ))}
                {res.warnings?.map((w, idx) => (
                  <div
                    key={`warn-${idx}`}
                    className="mt-1 text-amber-700 dark:text-amber-300"
                  >
                    ⚠ {w.field ? `${w.field}: ` : ""}
                    {w.message}
                    {w.fixable ? " (fixable)" : ""}
                  </div>
                ))}
                {res.info?.map((infoItem, idx) => (
                  <div
                    key={`info-${idx}`}
                    className="mt-1 text-gray-500 dark:text-gray-400"
                  >
                    ℹ {infoItem}
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}
