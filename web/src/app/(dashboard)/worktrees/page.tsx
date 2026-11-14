"use client";

/**
 * Worktrees Isolation & Merge Cockpit
 *
 * Operator cockpit for isolated per-agent git worktrees backed by:
 *   - GET    /api/v1/worktrees
 *   - POST   /api/v1/worktrees
 *   - POST   /api/v1/worktrees/{agent}/merge
 *   - DELETE /api/v1/worktrees/{agent}
 */

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  apiFetch,
  formatApiErrorMessage,
  type components,
} from "@/lib/api/client";

type WorktreesListResponse =
  components["schemas"]["WorktreesListEnvelopeResponse"];
type WorktreeInfo = components["schemas"]["WorktreeInfo"];
type WorktreeCreateResponse =
  components["schemas"]["WorktreeCreateEnvelopeResponse"];
type WorktreeMergeResponse =
  components["schemas"]["WorktreeMergeEnvelopeResponse"];
type WorktreeDeleteResponse =
  components["schemas"]["WorktreeDeleteEnvelopeResponse"];
type MergePreview = components["schemas"]["MergePreview"];

interface CreateWorktreePayload {
  session: string;
  agent: string;
  project_dir?: string;
}

interface MergeWorktreePayload {
  session?: string;
  preview?: boolean;
}

interface WorktreeMergeFeedback {
  kind: "success" | "warning" | "error";
  merged: boolean;
  message: string;
  sourceBranch?: string;
  preview?: MergePreview;
  details?: string[];
}

function createIdempotencyKey(prefix: string): string {
  const randomPart =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  return `${prefix}-${randomPart}`;
}

function worktreeRowKey(wt: WorktreeInfo): string {
  return `${wt.session_id || "default"}::${wt.agent_name}`;
}

function extractConflictDetails(err: unknown): string[] {
  if (!(err instanceof ApiError) || !err.details || typeof err.details !== "object") {
    return [];
  }
  const root = err.details as Record<string, unknown>;
  const nested =
    root.details && typeof root.details === "object"
      ? (root.details as Record<string, unknown>)
      : root;
  const items: string[] = [];
  if (typeof nested.uncommitted_files === "number" && nested.uncommitted_files > 0) {
    items.push(`${nested.uncommitted_files} uncommitted file(s)`);
  }
  if (typeof nested.unmerged_commits === "number" && nested.unmerged_commits > 0) {
    items.push(`${nested.unmerged_commits} unmerged commit(s)`);
  }
  if (typeof nested.hint === "string" && nested.hint.trim()) {
    items.push(nested.hint.trim());
  }
  return items;
}

export default function WorktreesPage() {
  const queryClient = useQueryClient();

  // Filter & search state
  const [sessionFilter, setSessionFilter] = useState("");
  const [searchQuery, setSearchQuery] = useState("");

  // Create form state
  const [newSession, setNewSession] = useState("");
  const [newAgent, setNewAgent] = useState("");
  const [newProjectDir, setNewProjectDir] = useState("");

  // Per-worktree inline state (merge feedback & delete confirmation)
  const [mergeFeedbackByKey, setMergeFeedbackByKey] = useState<
    Record<string, WorktreeMergeFeedback>
  >({});
  const [confirmDeleteKey, setConfirmDeleteKey] = useState<string | null>(null);
  const [forceDeleteByKey, setForceDeleteByKey] = useState<Record<string, boolean>>({});
  const [deleteErrorByKey, setDeleteErrorByKey] = useState<
    Record<string, { message: string; details: string[] }>
  >({});

  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const trimmedSessionFilter = sessionFilter.trim();
  const listPath = useMemo(() => {
    const params = new URLSearchParams();
    if (trimmedSessionFilter) {
      params.set("session", trimmedSessionFilter);
    }
    const qs = params.toString();
    return `/api/v1/worktrees${qs ? `?${qs}` : ""}`;
  }, [trimmedSessionFilter]);

  const worktreesQuery = useQuery({
    queryKey: ["worktrees", "list", listPath],
    queryFn: () => apiFetch<WorktreesListResponse>(listPath),
    refetchInterval: 15_000,
  });

  const createMutation = useMutation({
    mutationFn: (payload: CreateWorktreePayload) =>
      apiFetch<WorktreeCreateResponse>("/api/v1/worktrees", {
        method: "POST",
        headers: {
          "Idempotency-Key": createIdempotencyKey("wt-create"),
        },
        body: JSON.stringify(payload),
      }),
    onSuccess: (res) => {
      setNewAgent("");
      setNotice({
        kind: "success",
        text: `Provisioned isolated worktree for agent "${res.worktree.agent_name}" on branch "${res.worktree.branch_name}" (session "${res.session}").`,
      });
      void queryClient.invalidateQueries({ queryKey: ["worktrees"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Failed to create worktree: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const mergeMutation = useMutation({
    mutationFn: ({
      agentName,
      payload,
    }: {
      rowKey: string;
      agentName: string;
      payload: MergeWorktreePayload;
    }) =>
      apiFetch<WorktreeMergeResponse>(
        `/api/v1/worktrees/${encodeURIComponent(agentName)}/merge`,
        {
          method: "POST",
          headers: payload.preview
            ? undefined
            : { "Idempotency-Key": createIdempotencyKey("wt-merge") },
          body: JSON.stringify(payload),
        }
      ),
    onSuccess: (res, vars) => {
      const preview = res.preview;
      const hasConflicts = Boolean(
        preview?.has_conflicts ||
          (preview?.conflicts && preview.conflicts.length > 0) ||
          (preview?.blockers && preview.blockers.length > 0)
      );

      let kind: "success" | "warning" | "error" = "success";
      let message = "";

      if (vars.payload.preview) {
        if (preview?.already_merged) {
          message = `Preview: branch "${res.source_branch || preview.source_branch}" is already merged into "${preview.target_branch}".`;
        } else if (hasConflicts || preview?.can_merge === false) {
          kind = "warning";
          message = `Preview detected merge blockers or conflicts for "${res.agent_name}".`;
        } else {
          message = `Preview clean: ${preview?.source_commits ?? 0} commit(s) ready to merge into "${preview?.target_branch || "main"}".`;
        }
      } else {
        message = res.merged
          ? `Merged "${res.source_branch || res.agent_name}" back into "${preview?.target_branch || "main"}" (${preview?.source_commits ?? 0} commit(s)).`
          : `Merge completed with no branch changes for "${res.agent_name}".`;
      }

      setMergeFeedbackByKey((prev) => ({
        ...prev,
        [vars.rowKey]: {
          kind,
          merged: res.merged,
          message,
          sourceBranch: res.source_branch,
          preview,
        },
      }));

      if (!vars.payload.preview && res.merged) {
        setNotice({
          kind: "success",
          text: message,
        });
        void queryClient.invalidateQueries({ queryKey: ["worktrees"] });
      }
    },
    onError: (err, vars) => {
      const message = formatApiErrorMessage(err);
      const details = extractConflictDetails(err);
      setMergeFeedbackByKey((prev) => ({
        ...prev,
        [vars.rowKey]: {
          kind: "error",
          merged: false,
          message: `Merge failed: ${message}`,
          details,
        },
      }));
      setNotice({
        kind: "error",
        text: `Merge failed for "${vars.agentName}": ${message}`,
      });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: ({
      agentName,
      session,
      force,
    }: {
      rowKey: string;
      agentName: string;
      session?: string;
      force?: boolean;
    }) => {
      const params = new URLSearchParams();
      if (session && session.trim()) {
        params.set("session", session.trim());
      }
      if (force) {
        params.set("force", "true");
      }
      const qs = params.toString();
      return apiFetch<WorktreeDeleteResponse>(
        `/api/v1/worktrees/${encodeURIComponent(agentName)}${qs ? `?${qs}` : ""}`,
        {
          method: "DELETE",
          headers: {
            "Idempotency-Key": createIdempotencyKey("wt-delete"),
          },
        }
      );
    },
    onSuccess: (res, vars) => {
      setConfirmDeleteKey((prev) => (prev === vars.rowKey ? null : prev));
      setDeleteErrorByKey((prev) => {
        const next = { ...prev };
        delete next[vars.rowKey];
        return next;
      });
      setNotice({
        kind: "success",
        text: `Removed worktree for agent "${res.agent_name}" (session "${res.session}").`,
      });
      void queryClient.invalidateQueries({ queryKey: ["worktrees"] });
    },
    onError: (err, vars) => {
      const message = formatApiErrorMessage(err);
      const details = extractConflictDetails(err);
      setDeleteErrorByKey((prev) => ({
        ...prev,
        [vars.rowKey]: { message, details },
      }));
      setNotice({
        kind: "error",
        text: `Remove failed for "${vars.agentName}": ${message}`,
      });
    },
  });

  const allWorktrees = useMemo(
    () => worktreesQuery.data?.worktrees ?? [],
    [worktreesQuery.data?.worktrees]
  );

  const filteredWorktrees = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return allWorktrees;
    return allWorktrees.filter((wt) => {
      const agent = (wt.agent_name || "").toLowerCase();
      const branch = (wt.branch_name || "").toLowerCase();
      const path = (wt.path || "").toLowerCase();
      const session = (wt.session_id || "").toLowerCase();
      return (
        agent.includes(q) ||
        branch.includes(q) ||
        path.includes(q) ||
        session.includes(q)
      );
    });
  }, [allWorktrees, searchQuery]);

  const stats = useMemo(() => {
    let cleanCount = 0;
    let issueCount = 0;
    const branches = new Set<string>();
    const sessions = new Set<string>();
    let mergeableBranches = 0;

    for (const wt of allWorktrees) {
      const key = worktreeRowKey(wt);
      const fb = mergeFeedbackByKey[key];
      const hasPreviewConflict = Boolean(
        fb?.preview?.has_conflicts ||
          (fb?.preview?.blockers && fb.preview.blockers.length > 0) ||
          deleteErrorByKey[key]
      );

      if (wt.created && !wt.error && !hasPreviewConflict) {
        cleanCount += 1;
      } else {
        issueCount += 1;
      }
      if (wt.branch_name) {
        branches.add(wt.branch_name);
        if (wt.created && !wt.error && fb?.preview?.can_merge !== false) {
          mergeableBranches += 1;
        }
      }
      if (wt.session_id) {
        sessions.add(wt.session_id);
      }
    }

    return {
      total: allWorktrees.length,
      cleanCount,
      issueCount,
      activeBranches: branches.size,
      mergeableBranches,
      isolatedSessions: sessions.size,
    };
  }, [allWorktrees, deleteErrorByKey, mergeFeedbackByKey]);

  const handleCreateSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    const session = newSession.trim();
    const agent = newAgent.trim();
    const projectDir = newProjectDir.trim();

    if (!session || !agent) {
      setNotice({
        kind: "error",
        text: "Both Session and Agent identifiers are required to create a worktree.",
      });
      return;
    }

    createMutation.mutate({
      session,
      agent,
      ...(projectDir ? { project_dir: projectDir } : {}),
    });
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Worktrees
          </h1>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            Per-agent Git worktree isolation, 3-way merge previews, and safe
            branch reconciliation.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void worktreesQuery.refetch()}
          className="self-start sm:self-auto px-3 py-1.5 text-sm font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700"
        >
          Refresh Worktrees
        </button>
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

      {/* Summary KPI Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Total Worktrees
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {stats.total}
          </div>
          <div
            className="mt-1 text-xs font-mono truncate text-gray-500 dark:text-gray-400"
            title={worktreesQuery.data?.project_dir || ""}
          >
            {worktreesQuery.data?.project_dir || "Server project root"}
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Clean vs Dirty / Issues
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-emerald-600 dark:text-emerald-400">
              {stats.cleanCount}
            </span>
            <span className="text-sm text-gray-400">/</span>
            <span
              className={`text-lg font-semibold ${
                stats.issueCount > 0
                  ? "text-amber-600 dark:text-amber-400"
                  : "text-gray-500 dark:text-gray-400"
              }`}
            >
              {stats.issueCount} flagged
            </span>
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Healthy checkouts vs conflicts/dirty state
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Mergeable / Active Branches
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-2xl font-bold text-gray-900 dark:text-white">
              {stats.mergeableBranches}
            </span>
            <span className="text-xs text-gray-500 dark:text-gray-400">
              of {stats.activeBranches} branch{stats.activeBranches === 1 ? "" : "es"}
            </span>
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Isolated agent branches ready to reconcile
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Isolated Sessions
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {stats.isolatedSessions}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {trimmedSessionFilter
              ? `Filtered to "${trimmedSessionFilter}"`
              : "Across all active sessions"}
          </div>
        </div>
      </div>

      {/* Create Isolated Worktree Form */}
      <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5">
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
          Create Isolated Worktree
        </h2>
        <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
          Provision a dedicated <code className="font-mono text-xs">.ntm/worktrees/&lt;session&gt;/&lt;agent&gt;</code> checkout so concurrent agents edit without index contention.
        </p>

        <form
          onSubmit={handleCreateSubmit}
          aria-label="Create isolated worktree"
          className="mt-4 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 items-end"
        >
          <div>
            <label
              htmlFor="wt-create-session"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Session Name
            </label>
            <input
              id="wt-create-session"
              value={newSession}
              onChange={(e) => setNewSession(e.target.value)}
              placeholder="e.g. alpha-swarm"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
            />
          </div>

          <div>
            <label
              htmlFor="wt-create-agent"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Agent Identifier
            </label>
            <input
              id="wt-create-agent"
              value={newAgent}
              onChange={(e) => setNewAgent(e.target.value)}
              placeholder="e.g. BlueLake or cc_1"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
            />
          </div>

          <div>
            <label
              htmlFor="wt-create-project"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Project Directory (optional)
            </label>
            <input
              id="wt-create-project"
              value={newProjectDir}
              onChange={(e) => setNewProjectDir(e.target.value)}
              placeholder="/abs/path (defaults to server root)"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm font-mono text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
            />
          </div>

          <div>
            <button
              type="submit"
              disabled={createMutation.isPending}
              className="w-full rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {createMutation.isPending ? "Creating…" : "Create Worktree"}
            </button>
          </div>
        </form>
      </section>

      {/* Filter Bar & Worktrees Inventory */}
      <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4">
        <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-3">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Active Agent Worktrees
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Showing {filteredWorktrees.length} of {allWorktrees.length} isolated worktree
              {allWorktrees.length === 1 ? "" : "s"}.
            </p>
          </div>

          <div className="flex flex-col sm:flex-row gap-3">
            <div>
              <label htmlFor="wt-session-filter" className="sr-only">
                Filter by session
              </label>
              <input
                id="wt-session-filter"
                aria-label="Filter by session"
                value={sessionFilter}
                onChange={(e) => setSessionFilter(e.target.value)}
                placeholder="Session filter (optional)..."
                className="w-full sm:w-52 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
              />
            </div>

            <div>
              <label htmlFor="wt-search-query" className="sr-only">
                Search worktrees
              </label>
              <input
                id="wt-search-query"
                aria-label="Search worktrees"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search agent, branch, or path..."
                className="w-full sm:w-64 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white focus:border-blue-500 focus:outline-none"
              />
            </div>
          </div>
        </div>

        {worktreesQuery.isLoading && (
          <div className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
            Loading isolated worktrees…
          </div>
        )}

        {worktreesQuery.error && (
          <div
            role="alert"
            className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
          >
            Failed to load worktrees: {formatApiErrorMessage(worktreesQuery.error)}
          </div>
        )}

        {!worktreesQuery.isLoading &&
          !worktreesQuery.error &&
          filteredWorktrees.length === 0 && (
            <div className="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
              No isolated worktrees found. Create one above to isolate an agent on its own branch.
            </div>
          )}

        {filteredWorktrees.length > 0 && (
          <div className="space-y-3">
            {filteredWorktrees.map((wt) => {
              const rowKey = worktreeRowKey(wt);
              const feedback = mergeFeedbackByKey[rowKey];
              const isConfirmingDelete = confirmDeleteKey === rowKey;
              const forceDelete = Boolean(forceDeleteByKey[rowKey]);
              const deleteError = deleteErrorByKey[rowKey];
              const isMergingThis =
                mergeMutation.isPending &&
                mergeMutation.variables?.rowKey === rowKey;
              const isDeletingThis =
                deleteMutation.isPending &&
                deleteMutation.variables?.rowKey === rowKey;

              return (
                <div
                  key={rowKey}
                  className="rounded-lg border border-gray-200 dark:border-gray-700 bg-gray-50/60 dark:bg-gray-900/40 p-4 space-y-3"
                >
                  <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-3">
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-base font-semibold text-gray-900 dark:text-white">
                          {wt.agent_name}
                        </span>
                        {wt.session_id && (
                          <span className="rounded-full bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200 px-2 py-0.5 text-xs font-medium">
                            {wt.session_id}
                          </span>
                        )}
                        <span
                          className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                            wt.created && !wt.error
                              ? "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300"
                              : "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300"
                          }`}
                        >
                          {wt.created && !wt.error ? "Isolated" : "Error"}
                        </span>
                      </div>

                      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs font-mono text-gray-600 dark:text-gray-300">
                        <span>Branch: {wt.branch_name || "—"}</span>
                        <span className="truncate" title={wt.path}>
                          Path: {wt.path}
                        </span>
                      </div>

                      {wt.error && (
                        <div className="text-xs text-red-600 dark:text-red-400">
                          {wt.error}
                        </div>
                      )}
                    </div>

                    {/* Action Buttons */}
                    <div className="flex flex-wrap items-center gap-2 shrink-0">
                      <button
                        type="button"
                        disabled={isMergingThis}
                        aria-label={`Preview merge for ${wt.agent_name}`}
                        onClick={() =>
                          mergeMutation.mutate({
                            rowKey,
                            agentName: wt.agent_name,
                            payload: {
                              session: wt.session_id || undefined,
                              preview: true,
                            },
                          })
                        }
                        className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
                      >
                        Preview Merge
                      </button>

                      <button
                        type="button"
                        disabled={isMergingThis}
                        aria-label={`Merge Back ${wt.agent_name}`}
                        onClick={() =>
                          mergeMutation.mutate({
                            rowKey,
                            agentName: wt.agent_name,
                            payload: {
                              session: wt.session_id || undefined,
                            },
                          })
                        }
                        className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
                      >
                        {isMergingThis ? "Merging…" : "Merge Back"}
                      </button>

                      {!isConfirmingDelete ? (
                        <button
                          type="button"
                          aria-label={`Remove Worktree ${wt.agent_name}`}
                          onClick={() => setConfirmDeleteKey(rowKey)}
                          className="rounded-md border border-red-300 dark:border-red-700 px-3 py-1.5 text-xs font-medium text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20"
                        >
                          Remove Worktree
                        </button>
                      ) : (
                        <div className="flex flex-wrap items-center gap-2 rounded-md border border-red-300 dark:border-red-700 bg-red-50/70 dark:bg-red-900/20 px-2.5 py-1">
                          <label className="inline-flex items-center gap-1.5 text-xs text-red-800 dark:text-red-200">
                            <input
                              type="checkbox"
                              checked={forceDelete}
                              onChange={(e) =>
                                setForceDeleteByKey((prev) => ({
                                  ...prev,
                                  [rowKey]: e.target.checked,
                                }))
                              }
                              className="h-3.5 w-3.5"
                            />
                            Force
                          </label>
                          <button
                            type="button"
                            disabled={isDeletingThis}
                            aria-label={`Confirm remove ${wt.agent_name}`}
                            onClick={() =>
                              deleteMutation.mutate({
                                rowKey,
                                agentName: wt.agent_name,
                                session: wt.session_id || undefined,
                                force: forceDelete,
                              })
                            }
                            className="rounded bg-red-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-red-700 disabled:opacity-50"
                          >
                            {isDeletingThis ? "Removing…" : "Confirm Remove"}
                          </button>
                          <button
                            type="button"
                            onClick={() => setConfirmDeleteKey(null)}
                            className="text-xs text-gray-600 dark:text-gray-300 hover:underline"
                          >
                            Cancel
                          </button>
                        </div>
                      )}
                    </div>
                  </div>

                  {/* Inline Merge / Preview Feedback Banner */}
                  {feedback && (
                    <div
                      className={`rounded-md border px-3 py-2.5 text-xs space-y-1.5 ${
                        feedback.kind === "error"
                          ? "border-red-200 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200"
                          : feedback.kind === "warning"
                            ? "border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200"
                            : "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
                      }`}
                    >
                      <div className="font-medium">{feedback.message}</div>
                      {feedback.preview && (
                        <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-[11px] opacity-90">
                          <span>
                            {feedback.preview.source_branch} →{" "}
                            {feedback.preview.target_branch}
                          </span>
                          <span>Commits: {feedback.preview.source_commits}</span>
                          <span>
                            Can merge: {feedback.preview.can_merge ? "yes" : "no"}
                          </span>
                        </div>
                      )}
                      {feedback.preview?.conflicts &&
                        feedback.preview.conflicts.length > 0 && (
                          <div>
                            <span className="font-semibold">Conflicts: </span>
                            <span className="font-mono">
                              {feedback.preview.conflicts.join(", ")}
                            </span>
                          </div>
                        )}
                      {feedback.preview?.blockers &&
                        feedback.preview.blockers.length > 0 && (
                          <div>
                            <span className="font-semibold">Blockers: </span>
                            <span>{feedback.preview.blockers.join("; ")}</span>
                          </div>
                        )}
                      {feedback.details && feedback.details.length > 0 && (
                        <div>{feedback.details.join(" · ")}</div>
                      )}
                    </div>
                  )}

                  {/* Inline Delete Risk / Error Feedback */}
                  {deleteError && (
                    <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200 space-y-1">
                      <div className="font-medium">{deleteError.message}</div>
                      {deleteError.details.length > 0 && (
                        <div>{deleteError.details.join(" · ")}</div>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </section>
    </div>
  );
}
