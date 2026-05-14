"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiRequestSignal, getAuthHeaders, getBaseUrl } from "@/lib/api/client";

interface ApiEnvelope {
  success?: boolean;
  error?: string;
  message?: string;
}

// Types based on REST API
interface SessionMetrics {
  session: string;
  project_path?: string;
  created_at?: string;
  status: string;
}

interface SessionRecord {
  id: string;
  name: string;
  project_path?: string;
  created_at?: string;
  status: string;
}

interface SessionsResponse {
  sessions: SessionRecord[];
  count: number;
}

interface Reservation {
  id: number;
  path_pattern: string;
  agent_name: string;
  exclusive: boolean;
  reason?: string;
  expires_ts: string;
  created_ts?: string;
}

async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${getBaseUrl()}${url}`, {
    ...options,
    signal: options?.signal ?? apiRequestSignal(),
    headers: {
      "Content-Type": "application/json",
      ...getAuthHeaders(),
      ...options?.headers,
    },
  });

  const raw = await res.text();
  let data: unknown = null;
  if (raw) {
    try {
      data = JSON.parse(raw);
    } catch {
      throw new Error("Invalid response from server.");
    }
  }

  const envelope = data as ApiEnvelope | null;
  if (!res.ok || envelope?.success === false) {
    throw new Error(
      envelope?.error ||
        envelope?.message ||
        res.statusText ||
        `Request failed (${res.status})`
    );
  }

  return ((data as { data?: T } | null)?.data ?? data) as T;
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "Unexpected error";
}

// Conflict Heatmap Component
function ConflictHeatmap({ reservations }: { reservations: Reservation[] }) {
  // Build a matrix of files × agents
  const { files, agents, matrix } = useMemo(() => {
    const fileSet = new Set<string>();
    const agentSet = new Set<string>();
    const conflicts = new Map<string, Map<string, Reservation[]>>();

    for (const r of reservations) {
      // Extract base path for grouping
      const basePath = r.path_pattern.split('/').slice(0, 3).join('/') || r.path_pattern;
      fileSet.add(basePath);
      agentSet.add(r.agent_name);

      if (!conflicts.has(basePath)) {
        conflicts.set(basePath, new Map());
      }
      const fileConflicts = conflicts.get(basePath)!;
      if (!fileConflicts.has(r.agent_name)) {
        fileConflicts.set(r.agent_name, []);
      }
      fileConflicts.get(r.agent_name)!.push(r);
    }

    return {
      files: Array.from(fileSet).sort(),
      agents: Array.from(agentSet).sort(),
      matrix: conflicts,
    };
  }, [reservations]);

  if (files.length === 0 || agents.length === 0) {
    return (
      <div className="text-center text-gray-500 py-8">
        No active reservations to display
      </div>
    );
  }

  // Color cell based on exclusive status and count
  const getCellStyle = (reservations: Reservation[] | undefined) => {
    if (!reservations || reservations.length === 0) {
      return 'bg-gray-800';
    }
    const hasExclusive = reservations.some((r) => r.exclusive);
    if (hasExclusive) {
      return 'bg-red-500/60 hover:bg-red-500/80';
    }
    return 'bg-yellow-500/40 hover:bg-yellow-500/60';
  };

  return (
    <div className="overflow-x-auto">
      <table className="min-w-full text-sm">
        <thead>
          <tr>
            <th className="p-2 text-left text-gray-400 font-normal">Path</th>
            {agents.map((agent) => (
              <th key={agent} className="p-2 text-center text-gray-400 font-normal min-w-[80px]">
                <span className="truncate block max-w-[80px]" title={agent}>
                  {agent}
                </span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {files.map((file) => (
            <tr key={file} className="border-t border-gray-700">
              <td className="p-2 font-mono text-xs text-gray-300 max-w-[200px] truncate" title={file}>
                {file}
              </td>
              {agents.map((agent) => {
                const cellReservations = matrix.get(file)?.get(agent);
                return (
                  <td
                    key={`${file}-${agent}`}
                    className={`p-2 text-center ${getCellStyle(cellReservations)} transition-colors cursor-pointer`}
                    title={
                      cellReservations
                        ? `${cellReservations.length} reservation(s)\n${cellReservations.map((r) => `${r.exclusive ? 'Exclusive' : 'Shared'}: ${r.reason || 'No reason'}`).join('\n')}`
                        : 'No reservation'
                    }
                  >
                    {cellReservations && (
                      <span className="text-xs font-medium">
                        {cellReservations.some((r) => r.exclusive) ? '✕' : '○'}
                      </span>
                    )}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="flex items-center gap-4 mt-4 text-xs text-gray-500">
        <div className="flex items-center gap-1">
          <span className="w-4 h-4 bg-red-500/60 rounded"></span>
          <span>Exclusive</span>
        </div>
        <div className="flex items-center gap-1">
          <span className="w-4 h-4 bg-yellow-500/40 rounded"></span>
          <span>Shared</span>
        </div>
        <div className="flex items-center gap-1">
          <span className="w-4 h-4 bg-gray-800 rounded border border-gray-600"></span>
          <span>None</span>
        </div>
      </div>
    </div>
  );
}

// Metric Card Component
function MetricCard({ label, value, subtext }: { label: string; value: string | number; subtext?: string }) {
  return (
    <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
      <div className="text-3xl font-bold text-gray-100">{value}</div>
      <div className="text-sm text-gray-400">{label}</div>
      {subtext && <div className="text-xs text-gray-500 mt-1">{subtext}</div>}
    </div>
  );
}

// Session Row Component
function SessionRow({ session }: { session: SessionMetrics }) {
  const statusColors: Record<string, string> = {
    active: 'text-green-400',
    paused: 'text-yellow-400',
    terminated: 'text-gray-400',
  };

  return (
    <tr className="border-t border-gray-700 hover:bg-gray-700/30">
      <td className="p-3 font-medium text-gray-200">{session.session}</td>
      <td className="p-3 text-gray-400 font-mono text-xs">
        {session.project_path || '-'}
      </td>
      <td className="p-3 text-gray-400">
        {session.created_at ? new Date(session.created_at).toLocaleString() : '-'}
      </td>
      <td className="p-3">
        <span className={statusColors[session.status] || 'text-gray-400'}>
          {session.status}
        </span>
      </td>
    </tr>
  );
}

// Main Analytics Page
export default function AnalyticsPage() {
  const [selectedSession, setSelectedSession] = useState<string>("");

  const {
    data: sessionsData,
    isLoading: sessionsLoading,
    error: sessionsError,
  } = useQuery({
    queryKey: ["sessions"],
    queryFn: () => fetchJSON<SessionsResponse>("/api/v1/sessions"),
    refetchInterval: 30000,
  });

  // Reservations query for heatmap
  const {
    data: reservationsData,
    isLoading: reservationsLoading,
    error: reservationsError,
  } = useQuery({
    queryKey: ["reservations"],
    queryFn: () => fetchJSON<{ reservations: Reservation[] }>("/api/v1/reservations"),
    refetchInterval: 15000,
  });

  const reservations = reservationsData?.reservations || [];
  const sessions = useMemo<SessionMetrics[]>(
    () =>
      (sessionsData?.sessions || []).map((session) => ({
        session: session.name,
        project_path: session.project_path,
        created_at: session.created_at,
        status: session.status,
      })),
    [sessionsData]
  );

  useEffect(() => {
    if (!selectedSession) {
      return;
    }

    if (!sessions.some((session) => session.session === selectedSession)) {
      setSelectedSession("");
    }
  }, [selectedSession, sessions]);

  const connectionError = sessionsError ?? reservationsError;
  const visibleSessions = sessions.filter(
    (session) => !selectedSession || session.session === selectedSession
  );
  const activeSessions = sessions.filter((session) => session.status === "active").length;
  const uniqueProjects = new Set(
    sessions
      .map((session) => session.project_path)
      .filter((projectPath): projectPath is string => Boolean(projectPath))
  ).size;
  const exclusiveReservations = reservations.filter((reservation) => reservation.exclusive).length;
  const reservationAgents = new Set(
    reservations
      .map((reservation) => reservation.agent_name)
      .filter(Boolean)
  ).size;

  return (
    <div className="p-6 space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-100">Analytics</h1>
          <p className="text-sm text-gray-400">Live session inventory and reservation pressure</p>
        </div>
      </div>

      {connectionError && (
        <div className="rounded-lg border border-red-800 bg-red-900/20 p-4">
          <p className="text-sm text-red-300">
            Analytics error: {getErrorMessage(connectionError)}
          </p>
        </div>
      )}

      {/* Stats Cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <MetricCard
          label="Sessions"
          value={sessions.length}
          subtext="Tracked in state"
        />
        <MetricCard
          label="Active Sessions"
          value={activeSessions}
          subtext="Currently active"
        />
        <MetricCard
          label="Projects"
          value={uniqueProjects}
          subtext="Unique project roots"
        />
        <MetricCard
          label="Active Reservations"
          value={reservations.length}
          subtext={`${exclusiveReservations} exclusive · ${reservationAgents} agents`}
        />
      </div>

      {/* Session Filter */}
      {sessions.length > 0 && (
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <label className="text-sm text-gray-400 block mb-2">Filter by Session</label>
          <select
            value={selectedSession}
            onChange={(e) => setSelectedSession(e.target.value)}
            className="bg-gray-700 border border-gray-600 rounded px-3 py-2 w-full md:w-auto"
          >
            <option value="">All Sessions</option>
            {sessions.map((s) => (
              <option key={s.session} value={s.session}>{s.session}</option>
            ))}
          </select>
        </div>
      )}

      {/* Sessions Table */}
      <div className="bg-gray-800 rounded-lg border border-gray-700">
        <div className="p-4 border-b border-gray-700">
          <h2 className="text-lg font-semibold text-gray-100">Session Metrics</h2>
        </div>
        <div className="overflow-x-auto">
          {sessionsLoading ? (
            <div className="text-center text-gray-500 py-8">Loading sessions...</div>
          ) : sessions.length === 0 ? (
            <div className="text-center text-gray-500 py-8">No session data available.</div>
          ) : (
            <table className="min-w-full">
              <thead className="bg-gray-700/50">
                <tr>
                  <th className="p-3 text-left text-gray-400 font-medium">Session</th>
                  <th className="p-3 text-left text-gray-400 font-medium">Project</th>
                  <th className="p-3 text-left text-gray-400 font-medium">Created</th>
                  <th className="p-3 text-left text-gray-400 font-medium">Status</th>
                </tr>
              </thead>
              <tbody>
                {visibleSessions.map((session) => (
                  <SessionRow key={session.session} session={session} />
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {/* Conflict Heatmap */}
      <div className="bg-gray-800 rounded-lg border border-gray-700">
          <div className="p-4 border-b border-gray-700">
            <h2 className="text-lg font-semibold text-gray-100">Conflict Heatmap</h2>
            <p className="text-sm text-gray-400 mt-1">Active file reservations by agent and path group</p>
          </div>
          <div className="p-4">
            {reservationsLoading ? (
            <div className="text-center text-gray-500 py-8">Loading reservations...</div>
          ) : (
            <ConflictHeatmap reservations={reservations} />
          )}
        </div>
      </div>

      {/* Reservations List */}
      {reservations.length > 0 && (
        <div className="bg-gray-800 rounded-lg border border-gray-700">
          <div className="p-4 border-b border-gray-700">
            <h2 className="text-lg font-semibold text-gray-100">Active Reservations</h2>
          </div>
          <div className="p-4 space-y-2 max-h-[300px] overflow-y-auto">
            {reservations.map((r) => (
              <div
                key={r.id}
                className={`p-3 rounded-lg border ${r.exclusive ? 'bg-red-500/10 border-red-500/30' : 'bg-yellow-500/10 border-yellow-500/30'}`}
              >
                <div className="flex items-center justify-between">
                  <div>
                    <span className="font-mono text-sm text-gray-300">{r.path_pattern}</span>
                    <span className="ml-2 text-xs text-gray-500">by {r.agent_name}</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <span className={`text-xs px-2 py-0.5 rounded ${r.exclusive ? 'bg-red-500/20 text-red-300' : 'bg-yellow-500/20 text-yellow-300'}`}>
                      {r.exclusive ? 'Exclusive' : 'Shared'}
                    </span>
                    <span className="text-xs text-gray-500">
                      expires {new Date(r.expires_ts).toLocaleTimeString()}
                    </span>
                  </div>
                </div>
                {r.reason && (
                  <div className="text-xs text-gray-500 mt-1">{r.reason}</div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
