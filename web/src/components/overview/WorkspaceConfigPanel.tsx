"use client";

/**
 * WorkspaceConfigPanel
 *
 * Surfaces live server configuration and runtime workspace switching on Overview:
 *   - GET   /api/v1/config (host, port, auth_mode, project_dir, startup_project_dir)
 *   - PATCH /api/v1/config (switch active project_dir or reset to startup_project_dir)
 */

import { useEffect, useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface ServerConfigSnapshot extends ApiEnvelope {
  host: string;
  port: number;
  auth_mode: string;
  allowed_origins?: string[];
  public_base_url?: string;
  project_dir: string;
  startup_project_dir?: string;
}

interface ServerConfigPatchResponse extends ApiEnvelope {
  updated: boolean;
  config: {
    allowed_origins?: string[];
    project_dir: string;
  };
}

export function WorkspaceConfigPanel() {
  const queryClient = useQueryClient();
  const [configRequested, setConfigRequested] = useState(false);
  const [draftProjectDir, setDraftProjectDir] = useState("");
  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const configQuery = useQuery({
    queryKey: ["overview", "server-config"],
    queryFn: () => apiFetch<ServerConfigSnapshot>("/api/v1/config"),
    enabled: configRequested,
    retry: false,
  });

  const initializedDirRef = useRef(false);
  useEffect(() => {
    if (configQuery.data && !initializedDirRef.current) {
      initializedDirRef.current = true;
      setDraftProjectDir(configQuery.data.project_dir || "");
    }
  }, [configQuery.data]);

  const patchConfigMutation = useMutation({
    mutationFn: (projectDir: string | null) =>
      apiFetch<ServerConfigPatchResponse>("/api/v1/config", {
        method: "PATCH",
        body: JSON.stringify({ project_dir: projectDir }),
      }),
    onSuccess: (res) => {
      const updatedDir = res.config?.project_dir || "";
      setDraftProjectDir(updatedDir);
      setNotice({
        kind: "success",
        text: updatedDir
          ? `Switched active workspace to ${updatedDir}`
          : "Reset workspace to unscoped startup state.",
      });
      void queryClient.invalidateQueries({ queryKey: ["overview", "server-config"] });
      void queryClient.invalidateQueries({ queryKey: ["beads"] });
      void queryClient.invalidateQueries({ queryKey: ["mail"] });
      void queryClient.invalidateQueries({ queryKey: ["worktrees"] });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: formatApiErrorMessage(err),
      });
    },
  });

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = draftProjectDir.trim();
    if (!trimmed) {
      setNotice({
        kind: "error",
        text: "Enter an existing project directory path (or click Reset Workspace).",
      });
      return;
    }
    patchConfigMutation.mutate(trimmed);
  };

  return (
    <section
      aria-label="Runtime Workspace & Server Config"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-gray-900 dark:text-white">
            Runtime Project Workspace &amp; Server Config
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Inspect or retarget the active project workspace (<code className="font-mono">GET/PATCH /api/v1/config</code>) used by Beads, Agent Mail, Worktrees, and Ensembles.
          </p>
        </div>

        <button
          type="button"
          disabled={configQuery.isFetching}
          onClick={() => {
            if (configRequested) {
              void configQuery.refetch();
            } else {
              setConfigRequested(true);
            }
          }}
          className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
        >
          {configQuery.isFetching
            ? "Loading Config…"
            : configRequested
              ? "Refresh Server Config"
              : "Inspect / Switch Workspace"}
        </button>
      </div>

      {configQuery.isError && (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          {formatApiErrorMessage(configQuery.error)}
        </div>
      )}

      {configRequested && (
        <div className="space-y-3 pt-2 border-t border-gray-200 dark:border-gray-700">
          {configQuery.data && (
            <div className="flex flex-wrap items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
              <span className="rounded bg-gray-100 dark:bg-gray-900 px-2 py-0.5 font-mono">
                {configQuery.data.host}:{configQuery.data.port}
              </span>
              <span className="rounded bg-blue-50 dark:bg-blue-900/30 px-2 py-0.5 font-medium text-blue-700 dark:text-blue-300">
                auth: {configQuery.data.auth_mode || "local"}
              </span>
              <span
                data-testid="active-workspace-badge"
                className="rounded bg-emerald-50 dark:bg-emerald-900/20 px-2 py-0.5 font-mono text-emerald-800 dark:text-emerald-300"
              >
                project_dir: {configQuery.data.project_dir || "(unscoped)"}
              </span>
              {configQuery.data.startup_project_dir && (
                <span className="text-[11px] text-gray-500 dark:text-gray-400 font-mono">
                  startup: {configQuery.data.startup_project_dir}
                </span>
              )}
            </div>
          )}

          <form
            onSubmit={handleSubmit}
            className="flex flex-col sm:flex-row items-stretch sm:items-center gap-2"
          >
            <label htmlFor="workspace-project-dir-input" className="sr-only">
              Active Project Directory
            </label>
            <input
              id="workspace-project-dir-input"
              aria-label="Active Project Directory"
              value={draftProjectDir}
              onChange={(e) => setDraftProjectDir(e.target.value)}
              placeholder="/data/projects/my-repo"
              className="flex-1 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <button
              type="submit"
              disabled={patchConfigMutation.isPending}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {patchConfigMutation.isPending
                ? "Switching…"
                : "Switch Workspace"}
            </button>
            <button
              type="button"
              disabled={patchConfigMutation.isPending}
              onClick={() => patchConfigMutation.mutate("")}
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              Reset Workspace
            </button>
          </form>

          {notice && (
            <div
              data-testid="workspace-config-notice"
              className={`rounded border p-2 text-xs ${
                notice.kind === "success"
                  ? "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
                  : "border-red-200 bg-red-50 text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
              }`}
            >
              {notice.text}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
