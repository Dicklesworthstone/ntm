"use client";

/**
 * Agents Page
 *
 * Lists all registered agents across sessions, aggregated from
 * GET /api/v1/sessions and GET /api/v1/sessions/{id}/agents.
 */

import Link from "next/link";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiRequestSignal, getAuthHeaders, getBaseUrl } from "@/lib/api/client";

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface SessionRecord {
  name: string;
  project_path?: string;
  status?: string;
}

interface SessionsResponse extends ApiEnvelope {
  sessions: SessionRecord[];
}

interface AgentRecord {
  id: string;
  session_id: string;
  name: string;
  type: string;
  model?: string;
  tmux_pane_id?: string;
  last_seen?: string;
  status?: string;
  current_task_id?: string;
}

interface SessionAgentsResponse extends ApiEnvelope {
  session_id: string;
  agents: AgentRecord[];
  count: number;
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

const AGENT_TYPE_LABELS: Record<string, string> = {
  cc: "Claude Code",
  cod: "Codex",
  gmi: "Gemini",
  agy: "Antigravity",
  grok: "Grok",
};

const STATUS_CLASSES: Record<string, string> = {
  active: "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300",
  working: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300",
  idle: "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300",
  stuck: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300",
};

export default function AgentsPage() {
  const [filter, setFilter] = useState("");

  const {
    data: agents,
    isLoading,
    error,
  } = useQuery({
    queryKey: ["all-agents"],
    queryFn: async () => {
      const sessionsResponse = await apiFetch<SessionsResponse>("/api/v1/sessions");
      const sessions = sessionsResponse.sessions ?? [];
      const perSession = await Promise.all(
        sessions.map(async (session) => {
          try {
            const response = await apiFetch<SessionAgentsResponse>(
              `/api/v1/sessions/${encodeURIComponent(session.name)}/agents`
            );
            return (response.agents ?? []).map((agent) => ({
              ...agent,
              session_id: agent.session_id || session.name,
            }));
          } catch {
            // A single unreachable session must not blank the whole roster.
            return [] as AgentRecord[];
          }
        })
      );
      return perSession.flat();
    },
    refetchInterval: 10000,
  });

  const agentList = useMemo(() => agents ?? [], [agents]);
  const filtered = useMemo(() => {
    if (!filter) return agentList;
    const q = filter.toLowerCase();
    return agentList.filter((agent) =>
      [agent.name, agent.type, agent.session_id, agent.status, agent.model]
        .filter(Boolean)
        .some((field) => String(field).toLowerCase().includes(q))
    );
  }, [agentList, filter]);

  const typeCounts = useMemo(() => {
    return agentList.reduce<Record<string, number>>((acc, agent) => {
      const type = agent.type || "unknown";
      acc[type] = (acc[type] || 0) + 1;
      return acc;
    }, {});
  }, [agentList]);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Agents
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            All registered agents across sessions.
          </p>
        </div>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {filtered.length} agent{filtered.length !== 1 ? "s" : ""}
        </span>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {Object.entries(typeCounts).map(([type, count]) => (
          <span
            key={type}
            className="rounded-full bg-gray-100 px-3 py-1 text-xs font-medium text-gray-700 dark:bg-gray-800 dark:text-gray-300"
          >
            {AGENT_TYPE_LABELS[type] || type}: {count}
          </span>
        ))}
        <input
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="Filter agents..."
          className="ml-auto w-full sm:w-64 rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white shadow-sm focus:border-blue-500 focus:outline-none"
        />
      </div>

      {isLoading && (
        <div className="flex items-center justify-center h-64">
          <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
        </div>
      )}

      {error && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md">
          <p className="text-red-700 dark:text-red-400">
            Error loading agents:{" "}
            {error instanceof Error ? error.message : "Unknown error"}
          </p>
        </div>
      )}

      {!isLoading && !error && filtered.length === 0 && (
        <div className="text-center py-12">
          <p className="text-gray-500 dark:text-gray-400">
            No agents registered. Spawn some with{" "}
            <code className="bg-gray-100 dark:bg-gray-800 px-1 rounded">
              ntm spawn
            </code>
          </p>
        </div>
      )}

      {!isLoading && !error && filtered.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-gray-200 dark:border-gray-700">
          <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700 text-sm">
            <thead className="bg-gray-50 dark:bg-gray-800">
              <tr>
                {["Agent", "Type", "Session", "Status", "Pane", "Last Seen"].map(
                  (heading) => (
                    <th
                      key={heading}
                      className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400"
                    >
                      {heading}
                    </th>
                  )
                )}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 dark:divide-gray-700 bg-white dark:bg-gray-900">
              {filtered.map((agent) => (
                <tr key={`${agent.session_id}-${agent.id}`}>
                  <td className="px-4 py-3">
                    <div className="font-medium text-gray-900 dark:text-white">
                      {agent.name || agent.id}
                    </div>
                    {agent.model && (
                      <div className="text-xs text-gray-500 dark:text-gray-400">
                        {agent.model}
                      </div>
                    )}
                  </td>
                  <td className="px-4 py-3 text-gray-700 dark:text-gray-300">
                    {AGENT_TYPE_LABELS[agent.type] || agent.type || "unknown"}
                  </td>
                  <td className="px-4 py-3">
                    <Link
                      href={`/sessions?session=${encodeURIComponent(agent.session_id)}`}
                      className="text-blue-600 dark:text-blue-400 hover:underline"
                    >
                      {agent.session_id}
                    </Link>
                  </td>
                  <td className="px-4 py-3">
                    <span
                      className={`rounded-full px-2 py-0.5 text-xs ${
                        STATUS_CLASSES[agent.status || ""] ||
                        "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300"
                      }`}
                    >
                      {agent.status || "unknown"}
                    </span>
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-gray-500 dark:text-gray-400">
                    {agent.tmux_pane_id || "—"}
                  </td>
                  <td className="px-4 py-3 text-xs text-gray-500 dark:text-gray-400">
                    {agent.last_seen
                      ? new Date(agent.last_seen).toLocaleString()
                      : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
