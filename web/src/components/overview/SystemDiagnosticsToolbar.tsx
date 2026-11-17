"use client";

/**
 * SystemDiagnosticsToolbar
 *
 * On-demand runtime tool dependency matrix (GET /api/v1/deps) and
 * one-click disclosure-redacted diagnostic support bundle generator
 * (POST /api/v1/support-bundle) for the Overview page.
 */

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface DependencyCheckEntry {
  name: string;
  installed: boolean;
  required?: boolean;
  version?: string;
  path?: string;
}

interface DependenciesResponse extends ApiEnvelope {
  all_installed?: boolean;
  dependencies?: DependencyCheckEntry[];
}

export interface SupportBundleArchiveResponse extends ApiEnvelope {
  filename: string;
  format: string;
  file_count: number;
  size: number;
  content_type: string;
  data: string;
  privacy_mode: boolean;
  content_suppressed: boolean;
  redaction_summary?: {
    mode: string;
    total_findings: number;
    files_scanned: number;
    files_redacted: number;
  };
  warnings?: string[];
}

export function SystemDiagnosticsToolbar() {
  const [depsRequested, setDepsRequested] = useState(false);
  const [runtimeRequested, setRuntimeRequested] = useState(false);
  const [bundleFormat, setBundleFormat] = useState<"zip" | "tar.gz">("zip");
  const [bundleReady, setBundleReady] =
    useState<SupportBundleArchiveResponse | null>(null);
  const [bundleError, setBundleError] = useState<string | null>(null);

  const depsQuery = useQuery({
    queryKey: ["overview", "deps"],
    queryFn: () => apiFetch<DependenciesResponse>("/api/v1/deps"),
    enabled: depsRequested,
    retry: false,
  });

  const runtimeQuery = useQuery({
    queryKey: ["overview", "runtime-version-capabilities"],
    queryFn: async () => {
      const [ver, caps] = await Promise.all([
        apiFetch<ApiEnvelope & { version?: string; commit?: string; go_version?: string }>("/api/v1/version"),
        apiFetch<ApiEnvelope & { capabilities?: unknown; commands?: unknown[]; count?: number }>("/api/v1/capabilities"),
      ]);
      return { ver, caps };
    },
    enabled: runtimeRequested,
    retry: false,
  });

  const bundleMutation = useMutation({
    mutationFn: (format: "zip" | "tar.gz") =>
      apiFetch<SupportBundleArchiveResponse>("/api/v1/support-bundle", {
        method: "POST",
        body: JSON.stringify({
          format,
          redact_mode: "redact",
          lines: 500,
        }),
      }),
    onSuccess: (res) => {
      setBundleReady(res);
      setBundleError(null);
    },
    onError: (err) => {
      setBundleReady(null);
      setBundleError(formatApiErrorMessage(err));
    },
  });

  const depsList = depsQuery.data?.dependencies ?? [];
  const installedCount = depsList.filter((d) => d.installed).length;

  return (
    <div
      role="region"
      aria-label="Dependencies & Support Bundle"
      className="pt-3 border-t border-gray-200 dark:border-gray-700 space-y-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={depsQuery.isFetching}
            onClick={() => {
              if (depsRequested) {
                void depsQuery.refetch();
              } else {
                setDepsRequested(true);
              }
            }}
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
          >
            {depsQuery.isFetching
              ? "Checking Dependencies…"
              : depsRequested
                ? "Refresh Tool Dependencies"
                : "Check Tool Dependencies"}
          </button>

          <button
            type="button"
            disabled={runtimeQuery.isFetching}
            onClick={() => {
              if (runtimeRequested) {
                void runtimeQuery.refetch();
              } else {
                setRuntimeRequested(true);
              }
            }}
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
          >
            {runtimeQuery.isFetching
              ? "Loading Runtime…"
              : "Runtime & Capabilities"}
          </button>

          <a
            href="/api/v1/openapi.json"
            target="_blank"
            rel="noreferrer"
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-blue-600 dark:text-blue-400 hover:underline"
          >
            OpenAPI 3.1 Spec ↗
          </a>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <label htmlFor="support-bundle-format" className="sr-only">
            Support Bundle Format
          </label>
          <select
            id="support-bundle-format"
            aria-label="Support Bundle Format"
            value={bundleFormat}
            onChange={(e) =>
              setBundleFormat(e.target.value as "zip" | "tar.gz")
            }
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
          >
            <option value="zip">zip</option>
            <option value="tar.gz">tar.gz</option>
          </select>

          <button
            type="button"
            disabled={bundleMutation.isPending}
            onClick={() => bundleMutation.mutate(bundleFormat)}
            className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
          >
            {bundleMutation.isPending
              ? "Bundling…"
              : "Generate Support Bundle"}
          </button>
        </div>
      </div>

      {bundleError && (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          {bundleError}
        </div>
      )}

      {bundleReady && (
        <div
          data-testid="support-bundle-ready"
          className="rounded border border-emerald-200 bg-emerald-50 p-2.5 text-xs text-emerald-900 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200 flex flex-wrap items-center justify-between gap-2"
        >
          <span>
            Ready:{" "}
            <span className="font-mono font-semibold">
              {bundleReady.filename}
            </span>{" "}
            ({bundleReady.file_count} files, {bundleReady.size} B
            {bundleReady.redaction_summary
              ? ` · redacted ${bundleReady.redaction_summary.total_findings} findings`
              : ""}
            )
          </span>
          <a
            href={`data:${bundleReady.content_type};base64,${bundleReady.data}`}
            download={bundleReady.filename}
            className="rounded bg-emerald-700 px-2.5 py-1 text-xs font-medium text-white hover:bg-emerald-800"
          >
            Download Archive
          </a>
        </div>
      )}

      {depsRequested && (
        <div data-testid="overview-deps-matrix" className="space-y-2 text-xs">
          {depsQuery.isFetching && !depsQuery.data ? (
            <div className="text-gray-500 dark:text-gray-400">
              Checking runtime dependencies…
            </div>
          ) : depsQuery.error ? (
            <div className="text-red-600 dark:text-red-400">
              {formatApiErrorMessage(depsQuery.error)}
            </div>
          ) : (
            <>
              <div className="text-gray-600 dark:text-gray-300 font-medium">
                Tool Dependencies: {installedCount}/{depsList.length} installed
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-2">
                {depsList.map((dep) => (
                  <div
                    key={dep.name}
                    className={`rounded border px-2.5 py-1.5 flex items-center justify-between gap-2 ${
                      dep.installed
                        ? "border-emerald-200 bg-emerald-50/60 dark:border-emerald-800 dark:bg-emerald-900/15"
                        : "border-gray-200 bg-gray-50 dark:border-gray-700 dark:bg-gray-900/40"
                    }`}
                  >
                    <span className="font-mono truncate text-gray-900 dark:text-white">
                      {dep.name}
                    </span>
                    <span
                      className={`text-[11px] font-semibold ${
                        dep.installed
                          ? "text-emerald-700 dark:text-emerald-300"
                          : "text-gray-500 dark:text-gray-400"
                      }`}
                    >
                      {dep.installed ? dep.version || "ok" : "missing"}
                    </span>
                  </div>
                ))}
              </div>
            </>
          )}
        </div>
      )}
      {runtimeRequested && runtimeQuery.data && (
        <div
          data-testid="overview-runtime-capabilities"
          className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/40 p-2.5 text-xs font-mono text-gray-800 dark:text-gray-200 flex flex-wrap items-center gap-3"
        >
          <span>
            Version: <strong>{runtimeQuery.data.ver.version || "dev"}</strong>
          </span>
          {runtimeQuery.data.ver.go_version && (
            <span>Go: {runtimeQuery.data.ver.go_version}</span>
          )}
          <span>
            Robot Surfaces:{" "}
            <strong>
              {runtimeQuery.data.caps.count ??
                (Array.isArray(runtimeQuery.data.caps.commands)
                  ? runtimeQuery.data.caps.commands.length
                  : "active")}
            </strong>
          </span>
        </div>
      )}
    </div>
  );
}
