"use client";

/**
 * Overview & Sessions Dashboard
 *
 * Displays live swarm pulse cards (Active Sessions, Backpressure Status,
 * Action-Required Attention items, Isolated Worktrees), quick cockpit links,
 * and the list of tmux sessions over the shared apiFetch client.
 */

import Link from "next/link";
import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
  type components,
} from "@/lib/api/client";

interface SessionRecord {
  id?: string;
  name: string;
  project_path?: string;
  created_at?: string;
  status?: string;
}

interface SessionsResponse extends ApiEnvelope {
  sessions?: SessionRecord[];
  count?: number;
}

type HealthBackpressureResponse =
  components["schemas"]["HealthBackpressureEnvelopeResponse"];
type AttentionDigest = components["schemas"]["AttentionDigest"];
type WorktreesListResponse =
  components["schemas"]["WorktreesListEnvelopeResponse"];
type JobsListResponse = components["schemas"]["JobsListEnvelopeResponse"];
type ServerDoctorResponse =
  components["schemas"]["ServerDoctorEnvelopeResponse"];

interface DoctorCheckRow {
  id: string;
  category: string;
  name: string;
  status: string;
  message: string;
  details: string;
}

interface NormalizedDoctorReport {
  overallStatus: string;
  passed: number;
  warnings: number;
  failed: number;
  checks: DoctorCheckRow[];
}

function normalizeDoctorReport(
  payload: ServerDoctorResponse
): NormalizedDoctorReport {
  const raw = payload as unknown as Record<string, unknown>;
  const checks: DoctorCheckRow[] = [];

  for (const dep of payload.dependencies ?? []) {
    if (!dep || typeof dep.name !== "string") continue;
    const status =
      dep.status || (dep.installed ? "ok" : dep.required ? "error" : "warning");
    const detailParts = [
      dep.version ? `v${dep.version.replace(/^v/, "")}` : "",
      dep.path || "",
    ].filter(Boolean);
    checks.push({
      id: `dep-${dep.name}`,
      category: dep.required ? "Required Dependency" : "Optional Dependency",
      name: dep.name,
      status,
      message: dep.message || (dep.installed ? "Installed" : "Not installed"),
      details: detailParts.join(" · "),
    });
  }

  for (const daemon of payload.daemons ?? []) {
    if (!daemon || typeof daemon.name !== "string") continue;
    checks.push({
      id: `daemon-${daemon.name}`,
      category: "Daemon",
      name: daemon.name,
      status: daemon.status || "ok",
      message: daemon.message || (daemon.running ? "Running" : "Available"),
      details:
        typeof daemon.port === "number"
          ? `port ${daemon.port} · ${daemon.running ? "running" : "idle"}`
          : "",
    });
  }

  if (payload.guard_hook && typeof payload.guard_hook === "object") {
    const gh = payload.guard_hook;
    const degradedRuns =
      typeof gh.degraded_runs === "number" ? gh.degraded_runs : 0;
    const detailParts = [
      `degraded_runs=${degradedRuns}`,
      gh.since ? `since ${gh.since}` : "",
    ].filter(Boolean);
    checks.push({
      id: `guard-${gh.name || "guard-hook"}`,
      category: "Safety Hook",
      name: gh.name || "guard-hook",
      status: gh.status || "ok",
      message:
        gh.message ||
        (degradedRuns > 0
          ? `Degraded ${degradedRuns} run(s)`
          : "Reservation guard nominal"),
      details: detailParts.join(" · "),
    });
  }

  if (Array.isArray(raw.checks)) {
    raw.checks.forEach((item, idx) => {
      if (!item || typeof item !== "object") return;
      const entry = item as Record<string, unknown>;
      const name = typeof entry.name === "string" ? entry.name : `check-${idx}`;
      checks.push({
        id: `check-${idx}-${name}`,
        category:
          typeof entry.category === "string" ? entry.category : "Diagnostic",
        name,
        status: typeof entry.status === "string" ? entry.status : "ok",
        message: typeof entry.message === "string" ? entry.message : "",
        details: typeof entry.details === "string" ? entry.details : "",
      });
    });
  }

  const passed =
    typeof raw.passed === "number"
      ? raw.passed
      : checks.filter((c) => {
          const s = c.status.toLowerCase();
          return s === "ok" || s === "pass" || s === "passed" || s === "healthy";
        }).length;

  const warnings =
    typeof payload.warnings === "number"
      ? payload.warnings
      : checks.filter((c) => {
          const s = c.status.toLowerCase();
          return s === "warning" || s === "warn";
        }).length;

  const failed =
    typeof payload.errors === "number"
      ? payload.errors
      : typeof raw.failed === "number"
        ? raw.failed
        : checks.filter((c) => {
            const s = c.status.toLowerCase();
            return s === "error" || s === "fail" || s === "failed";
          }).length;

  const overallStatus =
    (typeof payload.overall === "string" && payload.overall) ||
    (typeof raw.overall_status === "string" && raw.overall_status) ||
    (failed > 0 ? "unhealthy" : warnings > 0 ? "warning" : "healthy");

  return {
    overallStatus,
    passed,
    warnings,
    failed,
    checks,
  };
}

function doctorStatusBadgeClass(status: string): string {
  const normalized = status.trim().toLowerCase();
  switch (normalized) {
    case "unhealthy":
    case "error":
    case "fail":
    case "failed":
      return "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-200";
    case "warning":
    case "warn":
      return "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200";
    default:
      return "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300";
  }
}

type PressureLevel = "normal" | "elevated" | "critical" | "overload";

function mapDecisionToPressure(decision?: string): PressureLevel {
  const normalized = (decision || "").trim().toLowerCase();
  switch (normalized) {
    case "degrade":
    case "overload":
      return "overload";
    case "defer":
    case "critical":
    case "warning":
      return "critical";
    case "coalesce":
    case "elevated":
    case "watch":
      return "elevated";
    default:
      return "normal";
  }
}

function pressurePillClass(level: PressureLevel): string {
  switch (level) {
    case "overload":
      return "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-200";
    case "critical":
      return "bg-orange-100 text-orange-800 dark:bg-orange-900/40 dark:text-orange-200";
    case "elevated":
      return "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200";
    default:
      return "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300";
  }
}

export default function SessionsPage() {
  const [filter, setFilter] = useState("");
  const {
    data: sessions,
    isLoading,
    error,
  } = useQuery({
    queryKey: ["sessions"],
    queryFn: () => apiFetch<SessionsResponse>("/api/v1/sessions"),
    refetchInterval: 10000, // Poll every 10 seconds as backup
  });

  // Secondary pulse queries are strictly non-blocking: failures fall back in-card
  const backpressureQuery = useQuery({
    queryKey: ["overview", "backpressure"],
    queryFn: () =>
      apiFetch<HealthBackpressureResponse>("/api/v1/health/backpressure"),
    refetchInterval: 15000,
    retry: false,
  });

  const attentionQuery = useQuery({
    queryKey: ["overview", "attention-digest"],
    queryFn: () => apiFetch<AttentionDigest>("/api/v1/attention/digest"),
    refetchInterval: 15000,
    retry: false,
  });

  const worktreesQuery = useQuery({
    queryKey: ["overview", "worktrees"],
    queryFn: () => apiFetch<WorktreesListResponse>("/api/v1/worktrees"),
    refetchInterval: 20000,
    retry: false,
  });

  const jobsQuery = useQuery({
    queryKey: ["overview", "jobs"],
    queryFn: () => apiFetch<JobsListResponse>("/api/v1/jobs/"),
    refetchInterval: 15000,
    retry: false,
  });

  // On-demand ONLY: /api/v1/doctor is never polled on mount.
  const doctorMutation = useMutation({
    mutationFn: () => apiFetch<ServerDoctorResponse>("/api/v1/doctor"),
  });

  const doctorReport = useMemo(
    () =>
      doctorMutation.data ? normalizeDoctorReport(doctorMutation.data) : null,
    [doctorMutation.data]
  );

  const sessionList = useMemo(
    () => sessions?.sessions ?? [],
    [sessions?.sessions]
  );

  const activeSessionCount = useMemo(() => {
    return sessionList.filter(
      (s) => !s.status || s.status === "active" || s.status === "running"
    ).length;
  }, [sessionList]);

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

  const bpDecision = backpressureQuery.data?.backpressure?.decision;
  const bpLevel = mapDecisionToPressure(bpDecision);
  const bpOverloadedBuckets =
    backpressureQuery.data?.heatmap?.buckets?.length ??
    backpressureQuery.data?.backpressure?.heatmap?.buckets?.length ??
    0;

  const actionRequiredCount =
    attentionQuery.data?.buckets?.action_required?.length ??
    attentionQuery.data?.by_actionability?.action_required ??
    0;

  const worktreeCount =
    worktreesQuery.data?.count ?? worktreesQuery.data?.worktrees?.length ?? 0;

  const jobItems = jobsQuery.data?.jobs ?? [];
  const activeJobsCount = jobItems.filter(
    (j) => j.status === "running" || j.status === "pending"
  ).length;
  const totalJobsCount = jobsQuery.data?.count ?? jobItems.length;

  return (
    <div className="space-y-6">
      {/* Header & Quick Cockpit Links */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-baseline gap-3">
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Sessions
          </h1>
          <span className="text-sm text-gray-500 dark:text-gray-400">
            {filteredSessions.length} session
            {filteredSessions.length !== 1 ? "s" : ""}
          </span>
        </div>

        <div
          aria-label="Quick cockpit links"
          className="flex flex-wrap items-center gap-2 text-xs font-medium"
        >
          <Link
            href="/swarm"
            className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-2.5 py-1.5 text-gray-700 dark:text-gray-200 hover:border-blue-400 dark:hover:border-blue-500 transition-colors"
          >
            Swarm Lab →
          </Link>
          <Link
            href="/attention"
            className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-2.5 py-1.5 text-gray-700 dark:text-gray-200 hover:border-blue-400 dark:hover:border-blue-500 transition-colors"
          >
            Attention Inbox →
          </Link>
          <Link
            href="/ensemble"
            className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-2.5 py-1.5 text-gray-700 dark:text-gray-200 hover:border-blue-400 dark:hover:border-blue-500 transition-colors"
          >
            Ensemble Studio →
          </Link>
          <Link
            href="/worktrees"
            className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-2.5 py-1.5 text-gray-700 dark:text-gray-200 hover:border-blue-400 dark:hover:border-blue-500 transition-colors"
          >
            Worktrees →
          </Link>
        </div>
      </div>

      {/* Overview Pulse Cards (Non-blocking secondary telemetry) */}
      <div
        aria-label="Overview pulse"
        className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-4"
      >
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Active Sessions
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {isLoading ? "…" : activeSessionCount}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {sessionList.length} total managed session
            {sessionList.length === 1 ? "" : "s"}
          </div>
        </div>

        <Link
          href="/swarm"
          className="group rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 hover:border-blue-300 dark:hover:border-blue-500 transition-colors"
        >
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Backpressure Status
          </div>
          <div className="mt-2 flex items-center gap-2">
            {backpressureQuery.isLoading ? (
              <span className="text-xl font-bold text-gray-400">…</span>
            ) : backpressureQuery.isError ? (
              <span className="text-sm font-medium text-gray-500 dark:text-gray-400">
                Unavailable
              </span>
            ) : (
              <>
                <span
                  className={`rounded-full px-2.5 py-0.5 text-xs font-bold uppercase ${pressurePillClass(bpLevel)}`}
                >
                  {bpLevel}
                </span>
                {bpDecision && (
                  <span className="text-xs font-mono text-gray-500 dark:text-gray-400">
                    ({bpDecision})
                  </span>
                )}
              </>
            )}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 group-hover:text-blue-600 dark:group-hover:text-blue-400">
            {backpressureQuery.isError
              ? "Telemetry offline · Open Swarm →"
              : `${bpOverloadedBuckets} hot bucket${bpOverloadedBuckets === 1 ? "" : "s"} · Open Swarm →`}
          </div>
        </Link>

        <Link
          href="/attention"
          className="group rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 hover:border-blue-300 dark:hover:border-blue-500 transition-colors"
        >
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Action-Required Attention
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {attentionQuery.isLoading ? (
              "…"
            ) : attentionQuery.isError ? (
              <span className="text-sm font-medium text-gray-500 dark:text-gray-400">
                Unavailable
              </span>
            ) : (
              actionRequiredCount
            )}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 group-hover:text-blue-600 dark:group-hover:text-blue-400">
            {attentionQuery.isError
              ? "Digest unavailable · Open Attention →"
              : `${attentionQuery.data?.event_count ?? 0} feed events · Triage →`}
          </div>
        </Link>

        <Link
          href="/worktrees"
          className="group rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 hover:border-blue-300 dark:hover:border-blue-500 transition-colors"
        >
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Isolated Worktrees
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {worktreesQuery.isLoading ? (
              "…"
            ) : worktreesQuery.isError ? (
              <span className="text-sm font-medium text-gray-500 dark:text-gray-400">
                Unavailable
              </span>
            ) : (
              worktreeCount
            )}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 group-hover:text-blue-600 dark:group-hover:text-blue-400">
            {worktreesQuery.isError
              ? "Worktrees unavailable · Manage →"
              : "Per-agent Git checkouts · Manage →"}
          </div>
        </Link>

        <Link
          href="/ensemble"
          className="group rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 hover:border-blue-300 dark:hover:border-blue-500 transition-colors"
        >
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Async Jobs
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {jobsQuery.isLoading ? (
              "…"
            ) : jobsQuery.isError ? (
              <span className="text-sm font-medium text-gray-500 dark:text-gray-400">
                Unavailable
              </span>
            ) : (
              activeJobsCount
            )}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400 group-hover:text-blue-600 dark:group-hover:text-blue-400">
            {jobsQuery.isError
              ? "Job queue unavailable · Open Ensemble →"
              : `${totalJobsCount} recent job${totalJobsCount === 1 ? "" : "s"} · Open Ensemble →`}
          </div>
        </Link>
      </div>

      {/* On-Demand System Doctor Section */}
      <section
        aria-label="System Doctor"
        className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3"
      >
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold text-gray-900 dark:text-white">
              On-Demand System Doctor
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Verify required CLI dependencies, daemon ports, and reservation
              guard hooks on demand.
            </p>
          </div>
          <button
            type="button"
            disabled={doctorMutation.isPending}
            onClick={() => doctorMutation.mutate()}
            className="px-3 py-1.5 text-xs font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50 self-start sm:self-center"
          >
            {doctorMutation.isPending
              ? "Running Doctor..."
              : "Run Doctor Check"}
          </button>
        </div>

        {doctorMutation.isError && (
          <div
            role="alert"
            className="rounded border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 px-3 py-2 text-xs text-red-700 dark:text-red-300"
          >
            Doctor check failed: {formatApiErrorMessage(doctorMutation.error)}
          </div>
        )}

        {doctorReport && (
          <div className="space-y-3 pt-2 border-t border-gray-200 dark:border-gray-700">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span
                data-testid="doctor-overall-status"
                className={`rounded-full px-2.5 py-0.5 font-bold uppercase ${doctorStatusBadgeClass(
                  doctorReport.overallStatus
                )}`}
              >
                {doctorReport.overallStatus}
              </span>
              <span className="rounded bg-emerald-50 dark:bg-emerald-900/20 px-2 py-0.5 font-medium text-emerald-700 dark:text-emerald-300">
                Passed: {doctorReport.passed}
              </span>
              <span className="rounded bg-amber-50 dark:bg-amber-900/20 px-2 py-0.5 font-medium text-amber-700 dark:text-amber-300">
                Warnings: {doctorReport.warnings}
              </span>
              <span className="rounded bg-red-50 dark:bg-red-900/20 px-2 py-0.5 font-medium text-red-700 dark:text-red-300">
                Failed: {doctorReport.failed}
              </span>
            </div>

            {doctorReport.checks.length > 0 && (
              <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-700">
                <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700 text-xs">
                  <thead className="bg-gray-50 dark:bg-gray-900">
                    <tr>
                      <th className="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-400">
                        Check
                      </th>
                      <th className="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-400">
                        Status
                      </th>
                      <th className="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-400">
                        Message
                      </th>
                      <th className="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-400">
                        Details
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-gray-200 dark:divide-gray-700 bg-white dark:bg-gray-800">
                    {doctorReport.checks.map((check) => (
                      <tr key={check.id}>
                        <td className="px-3 py-2">
                          <div className="font-mono font-medium text-gray-900 dark:text-white">
                            {check.name}
                          </div>
                          <div className="text-[11px] text-gray-500 dark:text-gray-400">
                            {check.category}
                          </div>
                        </td>
                        <td className="px-3 py-2">
                          <span
                            className={`rounded-full px-2 py-0.5 font-medium uppercase ${doctorStatusBadgeClass(
                              check.status
                            )}`}
                          >
                            {check.status}
                          </span>
                        </td>
                        <td className="px-3 py-2 text-gray-700 dark:text-gray-300">
                          {check.message || "—"}
                        </td>
                        <td className="px-3 py-2 font-mono text-gray-500 dark:text-gray-400">
                          {check.details || "—"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}
      </section>

      {isLoading && (
        <div className="flex items-center justify-center h-64">
          <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
        </div>
      )}

      {error && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md">
          <p className="text-red-700 dark:text-red-400">
            Error loading sessions: {formatApiErrorMessage(error)}
          </p>
        </div>
      )}

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

      {!isLoading && !error && filteredSessions.length === 0 ? (
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
      href={`/sessions?session=${encodeURIComponent(sessionName)}`}
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
