"use client";

/**
 * Sessions Dashboard
 *
 * Displays list of tmux sessions with their status and agents.
 */

import Link from "next/link";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { formatApiErrorMessage, getClient } from "@/lib/api/client";

interface SessionRecord {
  id: string;
  name: string;
  project_path?: string;
  created_at?: string;
  status?: string;
}

interface SessionsResponse {
  sessions: SessionRecord[];
}

export default function SessionsPage() {
  const [filter, setFilter] = useState("");
  const {
    data: sessions,
    isLoading,
    error,
  } = useQuery({
    queryKey: ["sessions"],
    queryFn: async () => {
      const client = getClient();
      const response = await client.GET("/api/v1/sessions");
      const responseError = (response as { error?: unknown }).error;
      if (responseError) {
        throw new Error(
          `Failed to fetch sessions: ${formatApiErrorMessage(responseError)}`
        );
      }
      return response.data as SessionsResponse | undefined;
    },
    refetchInterval: 10000, // Poll every 10 seconds as backup
  });

  const sessionList = useMemo(() => sessions?.sessions ?? [], [sessions?.sessions]);
  const filteredSessions = useMemo(() => {
    if (!filter) return sessionList;
    const query = filter.toLowerCase();
    return sessionList.filter((session) => {
      const name = session.name || "";
      const projectPath = session.project_path || "";
      const status = session.status || "";
      return (
        name.toLowerCase().includes(query) ||
        projectPath.toLowerCase().includes(query) ||
        status.toLowerCase().includes(query)
      );
    });
  }, [filter, sessionList]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
      </div>
    );
  }

  if (error) {
    const message = error instanceof Error ? error.message : "Unknown error";
    return (
      <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md">
        <p className="text-red-700 dark:text-red-400">
          Error loading sessions: {message}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
          Sessions
        </h1>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {filteredSessions.length} session
          {filteredSessions.length !== 1 ? "s" : ""}
        </span>
      </div>

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-sm text-gray-500 dark:text-gray-400">
          Filter by session name, project path, or status.
        </div>
        <input
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="Search sessions..."
          className="w-full sm:w-64 rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white shadow-sm focus:border-blue-500 focus:outline-none"
        />
      </div>

      {filteredSessions.length === 0 ? (
        <div className="text-center py-12">
          <p className="text-gray-500 dark:text-gray-400">
            No sessions found. Create one with{" "}
            <code className="bg-gray-100 dark:bg-gray-800 px-1 rounded">
              ntm spawn
            </code>
          </p>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filteredSessions.map((session) => (
            <SessionCard key={session.name} session={session} />
          ))}
        </div>
      )}
    </div>
  );
}

function SessionCard({ session }: { session: SessionRecord }) {
  const created = session.created_at;
  const sessionName = session.name;
  const status = session.status || "unknown";
  const statusClasses: Record<string, string> = {
    active: "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300",
    paused: "bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300",
    terminated: "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300",
  };

  return (
    <Link
      href={`/sessions/${encodeURIComponent(sessionName)}`}
      className="group bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-4 hover:border-blue-300 dark:hover:border-blue-500 transition-colors"
    >
      <div className="flex items-start justify-between">
        <h3 className="font-medium text-gray-900 dark:text-white truncate">
          {sessionName}
        </h3>
        <span
          className={`rounded-full px-2 py-0.5 text-xs ${statusClasses[status] || "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300"}`}
        >
          {status}
        </span>
      </div>

      {session.project_path && (
        <div className="mt-2 rounded-md bg-gray-50 px-2 py-1 text-xs font-mono text-gray-600 dark:bg-gray-900 dark:text-gray-400">
          {session.project_path}
        </div>
      )}

      <div className="mt-3 flex items-center justify-between text-xs text-gray-400 dark:text-gray-500">
        <span>{created && `Created ${new Date(created).toLocaleDateString()}`}</span>
        <span className="text-blue-600 dark:text-blue-400 group-hover:underline">
          View →
        </span>
      </div>
    </Link>
  );
}
