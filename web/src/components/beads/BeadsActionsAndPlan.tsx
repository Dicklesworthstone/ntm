"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

interface CreatedBead {
  id: string;
  title?: string;
}

interface CreateBeadResponse extends ApiEnvelope {
  bead: CreatedBead;
}

interface SyncBeadsResponse extends ApiEnvelope {
  synced: boolean;
  output?: string;
}

interface PlanItem {
  id: string;
  title: string;
  priority: number;
  status: string;
  unblocks?: string[];
}

interface PlanTrack {
  track_id: string;
  items: PlanItem[];
  reason?: string;
}

interface BeadsPlanResponse extends ApiEnvelope {
  plan?: {
    generated_at?: string;
    plan?: {
      tracks?: PlanTrack[];
    };
  };
}

export interface BeadsActionsAndPlanProps {
  onNotice: (notice: { type: "success" | "error"; message: string }) => void;
  onSelectBead: (beadId: string) => void;
}

export function BeadsActionsAndPlan({
  onNotice,
  onSelectBead,
}: BeadsActionsAndPlanProps) {
  const queryClient = useQueryClient();
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [title, setTitle] = useState("");
  const [issueType, setIssueType] = useState("task");
  const [priority, setPriority] = useState("1");
  const [description, setDescription] = useState("");
  const [labelsInput, setLabelsInput] = useState("");

  const planQuery = useQuery({
    queryKey: ["beads", "plan"],
    queryFn: () => apiFetch<BeadsPlanResponse>("/api/v1/beads/plan"),
    refetchInterval: 120_000,
  });

  const syncMutation = useMutation({
    mutationFn: () =>
      apiFetch<SyncBeadsResponse>("/api/v1/beads/sync", {
        method: "POST",
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["beads"] });
      onNotice({
        type: "success",
        message: "Flushed SQLite bead state to .beads/issues.jsonl.",
      });
    },
    onError: (err) => {
      onNotice({
        type: "error",
        message: `Beads sync failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const trimmedTitle = title.trim();
      if (!trimmedTitle) {
        throw new Error("Bead title is required.");
      }
      if (trimmedTitle.startsWith("-")) {
        throw new Error("Bead title must not start with '-'.");
      }
      const labels = labelsInput
        .split(",")
        .map((l) => l.trim())
        .filter(Boolean);
      return apiFetch<CreateBeadResponse>("/api/v1/beads", {
        method: "POST",
        body: JSON.stringify({
          title: trimmedTitle,
          type: issueType,
          priority,
          ...(description.trim() ? { description: description.trim() } : {}),
          ...(labels.length > 0 ? { labels } : {}),
        }),
      });
    },
    onSuccess: (res) => {
      setTitle("");
      setDescription("");
      setLabelsInput("");
      setShowCreateForm(false);
      void queryClient.invalidateQueries({ queryKey: ["beads"] });
      onNotice({
        type: "success",
        message: `Created bead ${res.bead?.id ?? "new"}.`,
      });
    },
    onError: (err) => {
      onNotice({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const tracks = planQuery.data?.plan?.plan?.tracks ?? [];

  return (
    <div className="space-y-4">
      {/* Create Bead + Sync JSONL Controls */}
      <div className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800 space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-gray-900 dark:text-white">
              Bead Graph Actions
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Create tracked issues or flush SQLite changes to{" "}
              <code className="font-mono">.beads/issues.jsonl</code>.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => setShowCreateForm((prev) => !prev)}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700"
            >
              {showCreateForm ? "Cancel New Bead" : "New Bead"}
            </button>
            <button
              type="button"
              disabled={syncMutation.isPending}
              onClick={() => syncMutation.mutate()}
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {syncMutation.isPending ? "Syncing…" : "Sync JSONL"}
            </button>
          </div>
        </div>

        {showCreateForm && (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              createMutation.mutate();
            }}
            aria-label="Create new bead"
            className="grid gap-3 border-t border-gray-200 dark:border-gray-700 pt-3 sm:grid-cols-4"
          >
            <div className="sm:col-span-2">
              <label
                htmlFor="new-bead-title"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Title
              </label>
              <input
                id="new-bead-title"
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                placeholder="Concise actionable task summary"
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
              />
            </div>

            <div>
              <label
                htmlFor="new-bead-type"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Type
              </label>
              <select
                id="new-bead-type"
                value={issueType}
                onChange={(event) => setIssueType(event.target.value)}
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
              >
                <option value="task">task</option>
                <option value="bug">bug</option>
                <option value="feature">feature</option>
                <option value="epic">epic</option>
              </select>
            </div>

            <div>
              <label
                htmlFor="new-bead-priority"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Priority
              </label>
              <select
                id="new-bead-priority"
                value={priority}
                onChange={(event) => setPriority(event.target.value)}
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
              >
                <option value="0">P0 (Critical)</option>
                <option value="1">P1 (High)</option>
                <option value="2">P2 (Normal)</option>
                <option value="3">P3 (Low)</option>
              </select>
            </div>

            <div className="sm:col-span-2">
              <input
                aria-label="New bead description"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                placeholder="Description / acceptance notes (optional)"
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs text-gray-900 dark:text-white"
              />
            </div>

            <div>
              <input
                aria-label="New bead labels"
                value={labelsInput}
                onChange={(event) => setLabelsInput(event.target.value)}
                placeholder="Labels comma-separated (optional)"
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs text-gray-900 dark:text-white"
              />
            </div>

            <div className="flex items-end">
              <button
                type="submit"
                disabled={createMutation.isPending || !title.trim()}
                className="w-full rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
              >
                {createMutation.isPending ? "Creating…" : "Create Bead"}
              </button>
            </div>
          </form>
        )}
      </div>

      {/* BV Parallel Execution Plan Tracks */}
      <section className="rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800 space-y-3">
        <div className="flex items-center justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold text-gray-900 dark:text-white">
              Parallel Execution Plan Tracks
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Independent agent work tracks computed from the bead dependency DAG.
            </p>
          </div>
          <span className="text-xs text-gray-500 dark:text-gray-400">
            {tracks.length} track{tracks.length === 1 ? "" : "s"}
          </span>
        </div>

        {planQuery.error && (
          <div className="rounded border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
            {formatApiErrorMessage(planQuery.error)}
          </div>
        )}

        {!planQuery.isLoading && !planQuery.error && tracks.length === 0 && (
          <div className="text-xs text-gray-500 dark:text-gray-400">
            No parallel execution tracks currently queued.
          </div>
        )}

        {tracks.length > 0 && (
          <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
            {tracks.slice(0, 6).map((track) => (
              <div
                key={track.track_id}
                className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/70 dark:bg-gray-900/40 p-3 space-y-2"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-semibold uppercase tracking-wide text-blue-700 dark:text-blue-300">
                    Track {track.track_id}
                  </span>
                  <span className="text-[11px] text-gray-500 dark:text-gray-400">
                    {track.items.length} step{track.items.length === 1 ? "" : "s"}
                  </span>
                </div>
                {track.reason && (
                  <p className="text-xs text-gray-500 dark:text-gray-400">
                    {track.reason}
                  </p>
                )}
                <ol className="space-y-1.5">
                  {track.items.slice(0, 4).map((item) => (
                    <li key={item.id}>
                      <button
                        type="button"
                        onClick={() => onSelectBead(item.id)}
                        className="w-full text-left rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 px-2.5 py-1.5 text-xs hover:border-blue-400"
                      >
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-mono font-medium text-gray-700 dark:text-gray-200">
                            {item.id}
                          </span>
                          <span className="text-[11px] text-gray-500 dark:text-gray-400">
                            P{item.priority}
                            {item.unblocks && item.unblocks.length > 0
                              ? ` · unblocks ${item.unblocks.length}`
                              : ""}
                          </span>
                        </div>
                        <div className="mt-0.5 truncate text-gray-900 dark:text-white">
                          {item.title}
                        </div>
                      </button>
                    </li>
                  ))}
                </ol>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
