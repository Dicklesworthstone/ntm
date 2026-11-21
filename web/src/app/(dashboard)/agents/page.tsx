"use client";

/**
 * Agents Page
 *
 * Lists the live agent panes across sessions, aggregated from
 * GET /api/v1/sessions and GET /api/v1/sessions/{id}/agents. `name` is the
 * Agent Mail name when one is registered for the pane, else the pane title.
 */

import Link from "next/link";
import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
  type components,
} from "@/lib/api/client";
import { PersonasAndRecipesPanel } from "@/components/agents/PersonasAndRecipesPanel";
import { AgentMappingAndBadgesPanel } from "@/components/agents/AgentMappingAndBadgesPanel";
import { PluginsAndLocalModelsPanel } from "@/components/agents/PluginsAndLocalModelsPanel";
import { AGENT_TYPE_LABELS } from "@/lib/agent-labels";

type AgentSpawnRequest = components["schemas"]["AgentSpawnRequest"];
type SpawnOutput = components["schemas"]["SpawnOutput"];
type AgentSendRequest = components["schemas"]["AgentSendRequest"];
type SendOutput = components["schemas"]["SendOutput"];
type AgentInterruptRequest = components["schemas"]["AgentInterruptRequest"];
type InterruptOutput = components["schemas"]["InterruptOutput"];

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
  title?: string;
  agent_mail_name?: string;
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

const SPAWN_COUNT_FIELDS = [
  { key: "cc_count", label: "Claude Code (cc)" },
  { key: "cod_count", label: "Codex (cod)" },
  { key: "agy_count", label: "Antigravity (agy)" },
  { key: "grok_count", label: "Grok (grok)" },
  { key: "omp_count", label: "Oh My Pi (omp)" },
  { key: "oc_count", label: "OpenCode (oc)" },
  { key: "gmi_count", label: "Gemini (gmi)" },
] as const;

type SpawnCountKey = (typeof SPAWN_COUNT_FIELDS)[number]["key"];

const CONTROL_AGENT_TYPES = [
  { value: "all", label: "All Agents (all)" },
  { value: "cc", label: "Claude Code (cc)" },
  { value: "cod", label: "Codex (cod)" },
  { value: "agy", label: "Antigravity (agy)" },
  { value: "grok", label: "Grok (grok)" },
  { value: "omp", label: "Oh My Pi (omp)" },
  { value: "oc", label: "OpenCode (oc)" },
  { value: "gmi", label: "Gemini (gmi)" },
] as const;

const STATUS_CLASSES: Record<string, string> = {
  active: "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300",
  working: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300",
  idle: "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300",
  stuck: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300",
  dead: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300",
};

const INITIAL_SPAWN_COUNTS: Record<SpawnCountKey, number> = {
  cc_count: 0,
  cod_count: 0,
  agy_count: 0,
  grok_count: 0,
  omp_count: 0,
  oc_count: 0,
  gmi_count: 0,
};

export default function AgentsPage() {
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState("");

  // Fleet Operations state
  const [fleetOpen, setFleetOpen] = useState(true);
  const [targetSession, setTargetSession] = useState("");
  const [spawnCounts, setSpawnCounts] =
    useState<Record<SpawnCountKey, number>>(INITIAL_SPAWN_COUNTS);
  const [spawnPreset, setSpawnPreset] = useState("");
  const [spawnLabel, setSpawnLabel] = useState("");
  const [waitReady, setWaitReady] = useState(false);

  const [targetAgentType, setTargetAgentType] = useState<string>("all");
  const [promptMessage, setPromptMessage] = useState("");
  const [forceInterrupt, setForceInterrupt] = useState(false);

  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const {
    data: fleetData,
    isLoading,
    error,
  } = useQuery({
    queryKey: ["agents", "all"],
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
      return {
        sessions,
        agents: perSession.flat(),
      };
    },
    refetchInterval: 10000,
  });

  const sessionOptions = useMemo(
    () => fleetData?.sessions ?? [],
    [fleetData?.sessions]
  );
  const agentList = useMemo(() => fleetData?.agents ?? [], [fleetData?.agents]);

  const spawnMutation = useMutation({
    mutationFn: ({
      sessionId,
      body,
    }: {
      sessionId: string;
      body: AgentSpawnRequest;
    }) =>
      apiFetch<SpawnOutput>(
        `/api/v1/sessions/${encodeURIComponent(sessionId)}/agents/spawn`,
        {
          method: "POST",
          body: JSON.stringify(body),
        }
      ),
    onSuccess: (res, vars) => {
      const spawnedCount = res.agents?.length ?? 0;
      setNotice({
        kind: "success",
        text: `Spawned ${spawnedCount} agent pane${spawnedCount === 1 ? "" : "s"} in session "${res.session || vars.sessionId}".`,
      });
      void queryClient.invalidateQueries({ queryKey: ["agents"] });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Spawn failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const sendMutation = useMutation({
    mutationFn: ({
      sessionId,
      body,
    }: {
      sessionId: string;
      body: AgentSendRequest;
    }) =>
      apiFetch<SendOutput>(
        `/api/v1/sessions/${encodeURIComponent(sessionId)}/agents/send`,
        {
          method: "POST",
          body: JSON.stringify(body),
        }
      ),
    onSuccess: (res, vars) => {
      const delivered = res.successful?.length ?? res.targets?.length ?? 0;
      setNotice({
        kind: "success",
        text: `Sent prompt to ${delivered} pane${delivered === 1 ? "" : "s"} in session "${res.session || vars.sessionId}".`,
      });
      setPromptMessage("");
      void queryClient.invalidateQueries({ queryKey: ["agents"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Send failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const interruptMutation = useMutation({
    mutationFn: ({
      sessionId,
      body,
    }: {
      sessionId: string;
      body: AgentInterruptRequest;
    }) =>
      apiFetch<InterruptOutput>(
        `/api/v1/sessions/${encodeURIComponent(sessionId)}/agents/interrupt`,
        {
          method: "POST",
          body: JSON.stringify(body),
        }
      ),
    onSuccess: (res, vars) => {
      const interruptedCount = res.interrupted?.length ?? 0;
      const readyCount = res.ready_for_input?.length ?? 0;
      setNotice({
        kind: "success",
        text: `Interrupted ${interruptedCount} pane${interruptedCount === 1 ? "" : "s"} in session "${res.session || vars.sessionId}" (${readyCount} ready for input).`,
      });
      void queryClient.invalidateQueries({ queryKey: ["agents"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Interrupt failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const filtered = useMemo(() => {
    if (!filter) return agentList;
    const q = filter.toLowerCase();
    return agentList.filter((agent) =>
      [agent.name, agent.title, agent.type, agent.session_id, agent.status, agent.model]
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

  const handleSpawnSubmit = () => {
    const sessionId = targetSession.trim();
    if (!sessionId) {
      setNotice({
        kind: "error",
        text: "Target session name is required to spawn agents.",
      });
      return;
    }

    const body: AgentSpawnRequest = {};
    let totalCount = 0;
    for (const field of SPAWN_COUNT_FIELDS) {
      const raw = spawnCounts[field.key];
      const count = Number.isFinite(raw) ? Math.max(0, Math.floor(raw)) : 0;
      if (count > 0) {
        body[field.key] = count;
        totalCount += count;
      }
    }

    const trimmedPreset = spawnPreset.trim();
    const trimmedLabel = spawnLabel.trim();
    if (trimmedPreset) {
      body.preset = trimmedPreset;
    }
    if (trimmedLabel) {
      body.label = trimmedLabel;
    }
    if (waitReady) {
      body.wait_ready = true;
    }

    if (totalCount === 0 && !trimmedPreset) {
      setNotice({
        kind: "error",
        text: "Specify at least one agent count greater than 0 or a preset name.",
      });
      return;
    }

    spawnMutation.mutate({ sessionId, body });
  };

  const handleSendPrompt = () => {
    const sessionId = targetSession.trim();
    const msg = promptMessage.trim();
    if (!sessionId) {
      setNotice({
        kind: "error",
        text: "Target session name is required to send a prompt.",
      });
      return;
    }
    if (!msg) {
      setNotice({
        kind: "error",
        text: "Enter a prompt message to send.",
      });
      return;
    }

    const body: AgentSendRequest = {
      message: msg,
      ...(targetAgentType !== "all" ? { agent_types: [targetAgentType] } : {}),
    };
    sendMutation.mutate({ sessionId, body });
  };

  const handleInterruptAgents = () => {
    const sessionId = targetSession.trim();
    if (!sessionId) {
      setNotice({
        kind: "error",
        text: "Target session name is required to interrupt agents.",
      });
      return;
    }

    const msg = promptMessage.trim();
    const body: AgentInterruptRequest = {
      ...(targetAgentType !== "all" ? { agent_types: [targetAgentType] } : {}),
      ...(msg ? { message: msg } : {}),
      ...(forceInterrupt ? { force: true } : {}),
    };
    interruptMutation.mutate({ sessionId, body });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Agents
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Live agent panes across sessions.
          </p>
        </div>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {filtered.length} agent{filtered.length !== 1 ? "s" : ""}
        </span>
      </div>

      {/* Fleet Operations Panel (accessible even when 0 agents are registered) */}
      <section
        aria-label="Fleet Operations"
        className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold text-gray-900 dark:text-white">
              Fleet Operations
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Spawn multi-harness agent panes, broadcast prompts, or interrupt
              running panes in any session.
            </p>
          </div>
          <button
            type="button"
            onClick={() => setFleetOpen((prev) => !prev)}
            className="px-2.5 py-1 text-xs font-medium rounded border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
          >
            {fleetOpen ? "Collapse" : "Expand"}
          </button>
        </div>

        {notice && (
          <div
            role={notice.kind === "error" ? "alert" : "status"}
            className={`rounded-lg border px-4 py-2.5 text-sm flex items-center justify-between gap-3 ${
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

        {fleetOpen && (
          <div className="space-y-4">
            {/* Shared Session Selector / Input */}
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label
                  htmlFor="fleet-target-session"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Target Session
                </label>
                <input
                  id="fleet-target-session"
                  type="text"
                  list="fleet-session-datalist"
                  value={targetSession}
                  onChange={(e) => setTargetSession(e.target.value)}
                  placeholder="Select existing or type session name..."
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
                />
                <datalist id="fleet-session-datalist">
                  {sessionOptions.map((s) => (
                    <option key={s.name} value={s.name} />
                  ))}
                </datalist>
              </div>

              {sessionOptions.length > 0 && (
                <div>
                  <label
                    htmlFor="fleet-existing-session-select"
                    className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                  >
                    Quick Pick Existing Session
                  </label>
                  <select
                    id="fleet-existing-session-select"
                    value={
                      sessionOptions.some((s) => s.name === targetSession)
                        ? targetSession
                        : ""
                    }
                    onChange={(e) => {
                      if (e.target.value) setTargetSession(e.target.value);
                    }}
                    className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
                  >
                    <option value="">Choose active session...</option>
                    {sessionOptions.map((s) => (
                      <option key={s.name} value={s.name}>
                        {s.name}
                        {s.status ? ` (${s.status})` : ""}
                      </option>
                    ))}
                  </select>
                </div>
              )}
            </div>

            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              {/* Spawn Agents Card */}
              <div className="rounded-lg border border-gray-200 dark:border-gray-700 p-4 space-y-3">
                <div>
                  <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                    Spawn Agents
                  </h3>
                  <p className="text-xs text-gray-500 dark:text-gray-400">
                    Launch new agent panes by harness count or preset.
                  </p>
                </div>

                <div className="grid grid-cols-2 sm:grid-cols-4 gap-2.5">
                  {SPAWN_COUNT_FIELDS.map((field) => (
                    <div key={field.key}>
                      <label
                        htmlFor={`spawn-${field.key}`}
                        className="block text-[11px] font-medium text-gray-700 dark:text-gray-300 mb-1"
                      >
                        {field.label}
                      </label>
                      <input
                        id={`spawn-${field.key}`}
                        type="number"
                        min={0}
                        max={32}
                        value={spawnCounts[field.key]}
                        onChange={(e) => {
                          const parsed = Number.parseInt(e.target.value, 10);
                          setSpawnCounts((prev) => ({
                            ...prev,
                            [field.key]: Number.isNaN(parsed)
                              ? 0
                              : Math.max(0, parsed),
                          }));
                        }}
                        className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1 text-sm text-gray-900 dark:text-white font-mono"
                      />
                    </div>
                  ))}
                </div>

                <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
                  <div>
                    <label
                      htmlFor="fleet-spawn-preset"
                      className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                    >
                      Preset (optional)
                    </label>
                    <input
                      id="fleet-spawn-preset"
                      type="text"
                      value={spawnPreset}
                      onChange={(e) => setSpawnPreset(e.target.value)}
                      placeholder="e.g. review-pair"
                      className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
                    />
                  </div>
                  <div>
                    <label
                      htmlFor="fleet-spawn-label"
                      className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                    >
                      Goal Label (optional)
                    </label>
                    <input
                      id="fleet-spawn-label"
                      type="text"
                      value={spawnLabel}
                      onChange={(e) => setSpawnLabel(e.target.value)}
                      placeholder="e.g. backend-wave"
                      className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
                    />
                  </div>
                </div>

                <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
                  <label className="inline-flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
                    <input
                      id="fleet-spawn-wait-ready"
                      type="checkbox"
                      checked={waitReady}
                      onChange={(e) => setWaitReady(e.target.checked)}
                      className="rounded border-gray-300 dark:border-gray-600"
                    />
                    Wait until agents are ready
                  </label>

                  <button
                    type="button"
                    disabled={spawnMutation.isPending}
                    onClick={handleSpawnSubmit}
                    className="px-3 py-1.5 text-xs font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
                  >
                    {spawnMutation.isPending ? "Spawning..." : "Spawn Agents"}
                  </button>
                </div>
              </div>

              {/* Broadcast / Send Prompt & Interrupt Card */}
              <div className="rounded-lg border border-gray-200 dark:border-gray-700 p-4 space-y-3">
                <div>
                  <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                    Broadcast Prompt &amp; Interrupt
                  </h3>
                  <p className="text-xs text-gray-500 dark:text-gray-400">
                    Dispatch instructions or send Ctrl+C across matching session
                    panes.
                  </p>
                </div>

                <div>
                  <label
                    htmlFor="fleet-agent-type-filter"
                    className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                  >
                    Target Agent Type
                  </label>
                  <select
                    id="fleet-agent-type-filter"
                    value={targetAgentType}
                    onChange={(e) => setTargetAgentType(e.target.value)}
                    className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
                  >
                    {CONTROL_AGENT_TYPES.map((opt) => (
                      <option key={opt.value} value={opt.value}>
                        {opt.label}
                      </option>
                    ))}
                  </select>
                </div>

                <div>
                  <label
                    htmlFor="fleet-prompt-message"
                    className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                  >
                    Prompt / Follow-up Message
                  </label>
                  <textarea
                    id="fleet-prompt-message"
                    rows={3}
                    value={promptMessage}
                    onChange={(e) => setPromptMessage(e.target.value)}
                    placeholder="Enter prompt to broadcast (or optional post-interrupt steering message)..."
                    className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
                  />
                </div>

                <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
                  <label className="inline-flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
                    <input
                      id="fleet-interrupt-force"
                      type="checkbox"
                      checked={forceInterrupt}
                      onChange={(e) => setForceInterrupt(e.target.checked)}
                      className="rounded border-gray-300 dark:border-gray-600"
                    />
                    Force interrupt idle panes
                  </label>

                  <div className="flex items-center gap-2">
                    <button
                      type="button"
                      disabled={sendMutation.isPending}
                      onClick={handleSendPrompt}
                      className="px-3 py-1.5 text-xs font-medium rounded-md bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50"
                    >
                      {sendMutation.isPending ? "Sending..." : "Send Prompt"}
                    </button>
                    <button
                      type="button"
                      disabled={interruptMutation.isPending}
                      onClick={handleInterruptAgents}
                      className="px-3 py-1.5 text-xs font-medium rounded-md border border-red-300 dark:border-red-700 text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/30 disabled:opacity-50"
                    >
                      {interruptMutation.isPending
                        ? "Interrupting..."
                        : "Interrupt Agents"}
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </div>
        )}
      </section>

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
                    {agent.title && agent.title !== agent.name && (
                      <div className="text-xs text-gray-500 dark:text-gray-400">
                        {agent.title}
                      </div>
                    )}
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

      <AgentMappingAndBadgesPanel
        sessionOptions={(fleetData?.sessions ?? []).map((s) => s.name)}
      />

      <PersonasAndRecipesPanel
        onSelectRecipePreset={(recipeName) => setSpawnPreset(recipeName)}
      />

      <PluginsAndLocalModelsPanel />
    </div>
  );
}
