"use client";

/**
 * PluginsAndLocalModelsPanel
 *
 * Surfaces installed NTM agent/command plugins and local Ollama model
 * VRAM recommendations plus live daemon inventory:
 *   - GET /api/v1/plugins
 *   - GET /api/v1/models?action=recommend
 *   - GET /api/v1/models?action=list
 */

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface AgentPluginViewItem {
  name: string;
  alias?: string;
  command: string;
  probe_command?: string;
  description?: string;
  default_model?: string;
  tags?: string[];
  env_keys?: string[];
  has_readiness: boolean;
}

export interface CommandPluginViewItem {
  name: string;
  path: string;
  usage?: string;
  description?: string;
}

export interface PluginsListResponse extends ApiEnvelope {
  config_dir: string;
  agents_dir: string;
  commands_dir: string;
  agent_plugins: AgentPluginViewItem[];
  command_plugins: CommandPluginViewItem[];
  total_count: number;
}

export interface LocalModelViewItem {
  name: string;
  size_bytes: number;
  modified_at: string;
  family?: string;
  parameters?: string;
}

export interface LocalModelsResponse extends ApiEnvelope {
  action: string;
  host?: string;
  model_count?: number;
  total_size_bytes?: number;
  disk_free_bytes?: number;
  disk_low?: boolean;
  cleanup_suggestions?: string[];
  models: LocalModelViewItem[];
  detected_vram_gb?: number;
  vram_source?: string;
  recommendations: string[];
}

function formatGib(bytes?: number): string {
  if (!bytes || bytes <= 0) {
    return "0 B";
  }
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GiB`;
}

export function PluginsAndLocalModelsPanel() {
  const [inventoryError, setInventoryError] = useState<string | null>(null);

  const pluginsQuery = useQuery({
    queryKey: ["ecosystem-plugins"],
    queryFn: () => apiFetch<PluginsListResponse>("/api/v1/plugins"),
  });

  const recommendQuery = useQuery({
    queryKey: ["ecosystem-models-recommend"],
    queryFn: () =>
      apiFetch<LocalModelsResponse>("/api/v1/models?action=recommend"),
  });

  const liveInventoryMutation = useMutation({
    mutationFn: () =>
      apiFetch<LocalModelsResponse>("/api/v1/models?action=list"),
    onError: (err) => {
      setInventoryError(formatApiErrorMessage(err));
    },
    onSuccess: () => {
      setInventoryError(null);
    },
  });

  const pluginsData = pluginsQuery.data;
  const recData = recommendQuery.data;
  const invData = liveInventoryMutation.data;

  return (
    <section
      aria-label="Installed Plugins & Local Ollama Models"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-5"
    >
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="text-base font-semibold text-gray-900 dark:text-white">
            Installed Plugins &amp; Local Ollama Models
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Inspect custom TOML agent harnesses, executable command plugins, GPU
            VRAM model recommendations, and live local Ollama storage.
          </p>
        </div>
        <button
          type="button"
          onClick={() => liveInventoryMutation.mutate()}
          disabled={liveInventoryMutation.isPending}
          className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
        >
          {liveInventoryMutation.isPending
            ? "Probing Ollama..."
            : "Probe Live Ollama Inventory"}
        </button>
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div className="rounded-md border border-gray-200 p-4 dark:border-gray-700 space-y-2">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-medium text-gray-900 dark:text-white">
              Agent &amp; Command Plugins ({pluginsData?.total_count ?? 0})
            </h3>
            {pluginsData?.config_dir && (
              <span className="font-mono text-[11px] text-gray-500 dark:text-gray-400">
                {pluginsData.config_dir}
              </span>
            )}
          </div>

          {pluginsData && pluginsData.total_count === 0 && (
            <p className="text-xs text-gray-500 dark:text-gray-400">
              No custom plugins installed in agents/ or commands/.
            </p>
          )}

          {pluginsData?.agent_plugins.map((ap) => (
            <div
              key={ap.name}
              className="rounded bg-gray-50 p-2.5 text-xs dark:bg-gray-900/50"
            >
              <div className="font-semibold text-gray-900 dark:text-white">
                {ap.name}
                {ap.alias ? ` (${ap.alias})` : ""}
                {ap.has_readiness ? " · readiness patterns" : ""}
              </div>
              {ap.description && (
                <div className="text-gray-600 dark:text-gray-300">
                  {ap.description}
                </div>
              )}
              <div className="mt-1 font-mono text-[11px] text-gray-500">
                cmd: {ap.command}
                {ap.env_keys && ap.env_keys.length > 0
                  ? ` · env keys: ${ap.env_keys.join(", ")}`
                  : ""}
              </div>
            </div>
          ))}

          {pluginsData?.command_plugins.map((cp) => (
            <div
              key={cp.name}
              className="rounded bg-gray-50 p-2.5 text-xs dark:bg-gray-900/50"
            >
              <div className="font-semibold text-gray-900 dark:text-white">
                ntm {cp.name}
              </div>
              {cp.description && (
                <div className="text-gray-600 dark:text-gray-300">
                  {cp.description}
                </div>
              )}
            </div>
          ))}
        </div>

        <div className="rounded-md border border-gray-200 p-4 dark:border-gray-700 space-y-2">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-medium text-gray-900 dark:text-white">
              VRAM Advisor &amp; Ollama Models
            </h3>
            {recData?.detected_vram_gb !== undefined && (
              <span className="rounded bg-blue-50 px-2 py-0.5 text-xs font-medium text-blue-700 dark:bg-blue-900/30 dark:text-blue-300">
                {recData.detected_vram_gb.toFixed(1)} GB VRAM (
                {recData.vram_source || "default"})
              </span>
            )}
          </div>

          {recData && recData.recommendations.length > 0 && (
            <div className="text-xs text-gray-600 dark:text-gray-300">
              Recommended:{" "}
              <span className="font-mono">
                {recData.recommendations.join(", ")}
              </span>
            </div>
          )}

          {inventoryError && (
            <div className="rounded border border-amber-200 bg-amber-50 px-2.5 py-1.5 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
              {inventoryError}
            </div>
          )}

          {invData && (
            <div className="space-y-1.5 pt-1">
              <div className="text-xs text-gray-500 dark:text-gray-400">
                Host {invData.host} · {invData.model_count ?? 0} model(s) ·{" "}
                {formatGib(invData.total_size_bytes)}
                {invData.disk_low ? " · LOW DISK" : ""}
              </div>
              {invData.models.map((m) => (
                <div
                  key={m.name}
                  className="flex items-center justify-between rounded bg-gray-50 px-2.5 py-1.5 text-xs dark:bg-gray-900/50"
                >
                  <span className="font-mono text-gray-900 dark:text-white">
                    {m.name}
                  </span>
                  <span className="text-gray-500 dark:text-gray-400">
                    {formatGib(m.size_bytes)}
                    {m.parameters ? ` · ${m.parameters}` : ""}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
