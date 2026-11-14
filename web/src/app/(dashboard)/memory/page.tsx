"use client";

/**
 * Memory Page
 *
 * Surfaces the CASS Memory (cm) daemon status & lifecycle controls, the CASS
 * cross-agent conversation search engine, and the procedural rule catalog over:
 *   GET  /api/v1/memory/daemon/status
 *   POST /api/v1/memory/daemon/start
 *   POST /api/v1/memory/daemon/stop
 *   GET  /api/v1/cass/status
 *   POST /api/v1/cass/search
 *   GET  /api/v1/memory/rules
 */

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

interface MemoryDaemonStatusResponse extends ApiEnvelope {
  installed: boolean;
  state: string;
  message?: string;
  pid?: number;
  port?: number;
  started_at?: string;
  session_id?: string;
}

interface MemoryDaemonActionResponse extends ApiEnvelope {
  state: string;
  port?: number;
  session_id?: string;
  message?: string;
}

interface CASSStatusResponse extends ApiEnvelope {
  installed: boolean;
  healthy: boolean;
  version?: string;
  database_size?: number;
  index_size?: number;
  doc_count?: number;
  last_indexed?: string;
  needs_reindex?: boolean;
  reindex_reason?: string;
}

interface CASSSearchHit {
  source_path: string;
  agent: string;
  workspace: string;
  title: string;
  score: number;
  snippet: string;
  match_type?: string;
  line_number?: number;
  content?: string;
  session_id?: string;
  created_at?: string;
}

interface CASSSearchResponse extends ApiEnvelope {
  query: string;
  limit: number;
  offset: number;
  count: number;
  total_matches: number;
  hits: CASSSearchHit[];
  has_more?: boolean;
  meta?: {
    took_ms?: number;
    index_size?: number;
    version?: string;
  };
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

function createIdempotencyKey(prefix: string): string {
  const randomPart =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  return `${prefix}-${randomPart}`;
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
  return formatApiErrorMessage(error);
}

export default function MemoryPage() {
  const queryClient = useQueryClient();

  // CASS interactive search state
  const [searchInput, setSearchInput] = useState("");
  const [agentFilter, setAgentFilter] = useState("");
  const [searchLimit, setSearchLimit] = useState<number>(20);
  const [searchResult, setSearchResult] = useState<CASSSearchResponse | null>(
    null
  );
  const [expandedHitKeys, setExpandedHitKeys] = useState<
    Record<string, boolean>
  >({});
  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const daemonQuery = useQuery({
    queryKey: ["memory", "daemon-status"],
    queryFn: () =>
      apiFetch<MemoryDaemonStatusResponse>("/api/v1/memory/daemon/status"),
    refetchInterval: 15000,
  });

  const cassQuery = useQuery({
    queryKey: ["memory", "cass-status"],
    queryFn: () => apiFetch<CASSStatusResponse>("/api/v1/cass/status"),
    refetchInterval: 30000,
  });

  const rulesQuery = useQuery({
    queryKey: ["memory", "rules"],
    queryFn: () => apiFetch<MemoryRulesResponse>("/api/v1/memory/rules"),
    staleTime: 60000,
    retry: false,
  });

  const startDaemonMutation = useMutation({
    mutationFn: () =>
      apiFetch<MemoryDaemonActionResponse>("/api/v1/memory/daemon/start", {
        method: "POST",
        headers: {
          "Idempotency-Key": createIdempotencyKey("cm-daemon-start"),
        },
        body: JSON.stringify({}),
      }),
    onSuccess: (res) => {
      setNotice({
        kind: "success",
        text:
          res.message ||
          `Memory daemon ${res.state || "starting"}${res.port ? ` on port ${res.port}` : ""}.`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["memory", "daemon-status"],
      });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Failed to start daemon: ${errorMessage(err)}`,
      });
    },
  });

  const stopDaemonMutation = useMutation({
    mutationFn: () =>
      apiFetch<MemoryDaemonActionResponse>("/api/v1/memory/daemon/stop", {
        method: "POST",
        headers: {
          "Idempotency-Key": createIdempotencyKey("cm-daemon-stop"),
        },
        body: JSON.stringify({}),
      }),
    onSuccess: (res) => {
      setNotice({
        kind: "success",
        text: res.message || `Memory daemon ${res.state || "stopped"}.`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["memory", "daemon-status"],
      });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Failed to stop daemon: ${errorMessage(err)}`,
      });
    },
  });

  const cassSearchMutation = useMutation({
    mutationFn: (payload: { query: string; agent?: string; limit: number }) =>
      apiFetch<CASSSearchResponse>("/api/v1/cass/search", {
        method: "POST",
        body: JSON.stringify({
          query: payload.query,
          limit: payload.limit,
          ...(payload.agent ? { agent: payload.agent } : {}),
          highlight: true,
        }),
      }),
    onSuccess: (res) => {
      setSearchResult(res);
      setExpandedHitKeys({});
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `CASS search failed: ${errorMessage(err)}`,
      });
    },
  });

  const daemon = daemonQuery.data;
  const cass = cassQuery.data;
  const rules = rulesQuery.data?.rules ?? [];
  const antiPatterns = rulesQuery.data?.anti_patterns ?? [];
  const isDaemonRunning =
    daemon?.state === "running" || daemon?.state === "starting";

  const handleSearchSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    const trimmed = searchInput.trim();
    if (!trimmed) {
      setNotice({
        kind: "error",
        text: "Enter a search query to search past agent conversations.",
      });
      return;
    }
    cassSearchMutation.mutate({
      query: trimmed,
      agent: agentFilter.trim() || undefined,
      limit: searchLimit,
    });
  };

  const toggleHitExpanded = (key: string) => {
    setExpandedHitKeys((prev) => ({
      ...prev,
      [key]: !prev[key],
    }));
  };

  return (
    <div className="space-y-6">
      {/* Header & Daemon Controls */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Memory
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            CASS Memory daemon, cross-agent conversation search, and procedural
            rules.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={startDaemonMutation.isPending || isDaemonRunning}
            onClick={() => startDaemonMutation.mutate()}
            className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
          >
            {startDaemonMutation.isPending ? "Starting…" : "Start Daemon"}
          </button>
          <button
            type="button"
            disabled={stopDaemonMutation.isPending || daemon?.state === "stopped"}
            onClick={() => stopDaemonMutation.mutate()}
            className="rounded-md border border-red-300 dark:border-red-700 px-3 py-1.5 text-sm font-medium text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
          >
            {stopDaemonMutation.isPending ? "Stopping…" : "Stop Daemon"}
          </button>
        </div>
      </div>

      {/* Feedback Banner */}
      {notice && (
        <div
          role={notice.kind === "error" ? "alert" : "status"}
          className={`rounded-lg border px-4 py-3 text-sm flex items-center justify-between gap-3 ${
            notice.kind === "error"
              ? "bg-red-50 border-red-200 text-red-800 dark:bg-red-900/20 dark:border-red-800 dark:text-red-200"
              : "bg-emerald-50 border-emerald-200 text-emerald-800 dark:bg-emerald-900/20 dark:border-emerald-800 dark:text-emerald-200"
          }`}
        >
          <span>{notice.text}</span>
          <button
            type="button"
            onClick={() => setNotice(null)}
            className="text-xs underline opacity-80 hover:opacity-100"
          >
            Dismiss
          </button>
        </div>
      )}

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
          helper={`Index size ${formatBytes(cass?.database_size ?? cass?.index_size)}`}
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

      {/* Interactive CASS Cross-Agent Conversation Search */}
      <section className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4">
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              CASS Cross-Agent Conversation Search
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Search indexed sessions across Claude Code, Codex, Gemini, and
              other coding agents.
            </p>
          </div>
          {searchResult && (
            <span className="rounded-full bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200 px-2.5 py-0.5 text-xs font-medium">
              {searchResult.count} shown of {searchResult.total_matches} match
              {searchResult.total_matches === 1 ? "" : "es"}
              {searchResult.meta?.took_ms !== undefined
                ? ` · ${searchResult.meta.took_ms}ms`
                : ""}
            </span>
          )}
        </div>

        <form
          onSubmit={handleSearchSubmit}
          aria-label="CASS conversation search"
          className="grid grid-cols-1 sm:grid-cols-12 gap-3 items-end"
        >
          <div className="sm:col-span-6">
            <label
              htmlFor="cass-search-input"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Search Query
            </label>
            <input
              id="cass-search-input"
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
              placeholder="Search past sessions (e.g. idempotency middleware, deadlock)..."
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
            />
          </div>

          <div className="sm:col-span-3">
            <label
              htmlFor="cass-agent-filter"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Agent Filter
            </label>
            <select
              id="cass-agent-filter"
              value={agentFilter}
              onChange={(e) => setAgentFilter(e.target.value)}
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
            >
              <option value="">All agents</option>
              <option value="claude">Claude (claude)</option>
              <option value="codex">Codex (codex)</option>
              <option value="gemini">Gemini (gemini)</option>
              <option value="cursor">Cursor (cursor)</option>
            </select>
          </div>

          <div className="sm:col-span-1">
            <label
              htmlFor="cass-search-limit"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Limit
            </label>
            <select
              id="cass-search-limit"
              value={searchLimit}
              onChange={(e) => setSearchLimit(Number(e.target.value))}
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-2 text-sm text-gray-900 dark:text-white"
            >
              <option value={10}>10</option>
              <option value={20}>20</option>
              <option value={50}>50</option>
            </select>
          </div>

          <div className="sm:col-span-2">
            <button
              type="submit"
              disabled={cassSearchMutation.isPending}
              className="w-full rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {cassSearchMutation.isPending ? "Searching…" : "Search CASS"}
            </button>
          </div>
        </form>

        {searchResult && searchResult.hits.length === 0 && (
          <div className="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
            No matching conversations found for &ldquo;{searchResult.query}
            &rdquo;.
          </div>
        )}

        {searchResult && searchResult.hits.length > 0 && (
          <div className="space-y-3">
            {searchResult.hits.map((hit, idx) => {
              const hitKey = `${hit.source_path}-${hit.line_number ?? idx}`;
              const expanded = Boolean(expandedHitKeys[hitKey]);
              return (
                <div
                  key={hitKey}
                  className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/60 dark:bg-gray-900/40 p-3.5 space-y-2"
                >
                  <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
                    <div className="flex flex-wrap items-center gap-2 min-w-0">
                      <span className="font-semibold text-sm text-gray-900 dark:text-white truncate">
                        {hit.title || hit.source_path}
                      </span>
                      <span className="rounded-full bg-indigo-100 text-indigo-800 dark:bg-indigo-900/40 dark:text-indigo-200 px-2 py-0.5 text-xs font-medium">
                        {hit.agent || "unknown"}
                      </span>
                      <span className="rounded bg-gray-200 dark:bg-gray-700 px-2 py-0.5 text-xs font-mono text-gray-700 dark:text-gray-200">
                        score: {typeof hit.score === "number" ? hit.score.toFixed(2) : hit.score}
                      </span>
                    </div>

                    <button
                      type="button"
                      onClick={() => toggleHitExpanded(hitKey)}
                      className="self-start sm:self-auto text-xs font-medium text-blue-600 dark:text-blue-400 hover:underline shrink-0"
                    >
                      {expanded ? "Collapse snippet" : "Expand snippet"}
                    </button>
                  </div>

                  <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs font-mono text-gray-500 dark:text-gray-400">
                    {hit.workspace && <span>Workspace: {hit.workspace}</span>}
                    {hit.session_id && <span>Session: {hit.session_id}</span>}
                    {hit.line_number !== undefined && (
                      <span>Line: {hit.line_number}</span>
                    )}
                  </div>

                  <p className="text-xs text-gray-700 dark:text-gray-300 whitespace-pre-wrap">
                    {hit.snippet}
                  </p>

                  {expanded && (
                    <div className="mt-2 rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-950 p-3 text-xs font-mono text-gray-800 dark:text-gray-200 space-y-1.5">
                      <div className="text-[11px] text-gray-500 dark:text-gray-400 break-all">
                        Source: {hit.source_path}
                        {hit.created_at
                          ? ` · ${new Date(hit.created_at).toLocaleString()}`
                          : ""}
                      </div>
                      <div className="whitespace-pre-wrap">
                        {hit.content || hit.snippet}
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </section>

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
