"use client";

/**
 * Memory Page
 *
 * Surfaces the CASS Memory (cm) daemon status, the CASS index status, and
 * the procedural rule catalog over the existing /api/v1 memory endpoints:
 *   GET /api/v1/memory/daemon/status
 *   GET /api/v1/cass/status
 *   GET /api/v1/memory/rules
 */

import { useQuery } from "@tanstack/react-query";
import { apiRequestSignal, getAuthHeaders, getBaseUrl } from "@/lib/api/client";

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface MemoryDaemonStatusResponse extends ApiEnvelope {
  installed: boolean;
  state: string;
  message?: string;
  pid?: number;
  port?: number;
  started_at?: string;
  session_id?: string;
}

interface CASSStatusResponse extends ApiEnvelope {
  installed: boolean;
  healthy: boolean;
  version?: string;
  index_size?: number;
  doc_count?: number;
  last_indexed?: string;
  needs_reindex?: boolean;
  reindex_reason?: string;
}

interface MemoryRule {
  id: string;
  content: string;
  category?: string;
}

interface MemoryRulesResponse extends ApiEnvelope {
  rules: MemoryRule[];
  anti_patterns: MemoryRule[];
}

async function apiFetch<T>(path: string): Promise<T> {
  const response = await fetch(`${getBaseUrl()}${path}`, {
    signal: apiRequestSignal(),
    headers: { "Content-Type": "application/json", ...getAuthHeaders() },
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
    throw new Error(envelope?.error || `Request failed (${response.status})`);
  }
  return data as T;
}

function formatBytes(bytes?: number): string {
  if (!bytes || bytes <= 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Unexpected error";
}

export default function MemoryPage() {
  const daemonQuery = useQuery({
    queryKey: ["memory-daemon-status"],
    queryFn: () =>
      apiFetch<MemoryDaemonStatusResponse>("/api/v1/memory/daemon/status"),
    refetchInterval: 15000,
  });

  const cassQuery = useQuery({
    queryKey: ["cass-status"],
    queryFn: () => apiFetch<CASSStatusResponse>("/api/v1/cass/status"),
    refetchInterval: 30000,
  });

  const rulesQuery = useQuery({
    queryKey: ["memory-rules"],
    queryFn: () => apiFetch<MemoryRulesResponse>("/api/v1/memory/rules"),
    staleTime: 60000,
    retry: false,
  });

  const daemon = daemonQuery.data;
  const cass = cassQuery.data;
  const rules = rulesQuery.data?.rules ?? [];
  const antiPatterns = rulesQuery.data?.anti_patterns ?? [];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
          Memory
        </h1>
        <p className="text-sm text-gray-500 dark:text-gray-400">
          CASS Memory daemon, search index, and procedural rules.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title="Daemon"
          value={
            daemonQuery.isLoading
              ? "…"
              : daemon?.installed
                ? daemon.state || "unknown"
                : "not installed"
          }
          helper={
            daemon?.port
              ? `Port ${daemon.port}${daemon.pid ? ` · PID ${daemon.pid}` : ""}`
              : daemon?.message || "cm daemon state"
          }
        />
        <StatCard
          title="Index Health"
          value={
            cassQuery.isLoading
              ? "…"
              : !cass?.installed
                ? "not installed"
                : cass.healthy
                  ? "healthy"
                  : "unhealthy"
          }
          helper={cass?.version ? `CASS ${cass.version}` : "CASS index status"}
        />
        <StatCard
          title="Documents"
          value={cass?.doc_count ?? "—"}
          helper={`Index size ${formatBytes(cass?.index_size)}`}
        />
        <StatCard
          title="Last Indexed"
          value={
            cass?.last_indexed
              ? new Date(cass.last_indexed).toLocaleDateString()
              : "—"
          }
          helper={
            cass?.needs_reindex
              ? `Reindex needed: ${cass.reindex_reason || "stale"}`
              : "Index is up to date"
          }
        />
      </div>

      {(daemonQuery.error || cassQuery.error) && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md text-sm text-red-700 dark:text-red-400">
          {errorMessage(daemonQuery.error || cassQuery.error)}
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-2">
        <RuleList
          title="Procedural Rules"
          emptyText="No rules recorded yet."
          rules={rules}
          loading={rulesQuery.isLoading}
          error={rulesQuery.error ? errorMessage(rulesQuery.error) : null}
          badgeClass="bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300"
        />
        <RuleList
          title="Anti-Patterns"
          emptyText="No anti-patterns recorded."
          rules={antiPatterns}
          loading={rulesQuery.isLoading}
          error={rulesQuery.error ? errorMessage(rulesQuery.error) : null}
          badgeClass="bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300"
        />
      </div>
    </div>
  );
}

function StatCard({
  title,
  value,
  helper,
}: {
  title: string;
  value: string | number;
  helper: string;
}) {
  return (
    <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
      <div className="text-xs uppercase tracking-wide text-gray-500 dark:text-gray-400">
        {title}
      </div>
      <div className="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">
        {value}
      </div>
      <div className="mt-1 text-xs text-gray-400 dark:text-gray-500">{helper}</div>
    </div>
  );
}

function RuleList({
  title,
  emptyText,
  rules,
  loading,
  error,
  badgeClass,
}: {
  title: string;
  emptyText: string;
  rules: MemoryRule[];
  loading: boolean;
  error: string | null;
  badgeClass: string;
}) {
  return (
    <section className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
          {title}
        </h2>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {rules.length}
        </span>
      </div>

      {loading && (
        <div className="py-6 text-center text-sm text-gray-400">Loading…</div>
      )}

      {error && (
        <div className="mt-3 rounded-md border border-yellow-200 bg-yellow-50 p-3 text-xs text-yellow-800 dark:border-yellow-800 dark:bg-yellow-900/20 dark:text-yellow-300">
          {error}
        </div>
      )}

      {!loading && !error && rules.length === 0 && (
        <div className="py-6 text-center text-sm text-gray-400">{emptyText}</div>
      )}

      <ul className="mt-3 space-y-2">
        {rules.map((rule) => (
          <li
            key={rule.id}
            className="rounded-md border border-gray-200 p-3 text-sm text-gray-700 dark:border-gray-700 dark:text-gray-300"
          >
            <div className="flex items-start justify-between gap-3">
              <span className="whitespace-pre-wrap">{rule.content}</span>
              {rule.category && (
                <span
                  className={`shrink-0 rounded-full px-2 py-0.5 text-xs ${badgeClass}`}
                >
                  {rule.category}
                </span>
              )}
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
