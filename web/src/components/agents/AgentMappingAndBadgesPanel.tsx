"use client";

/**
 * AgentMappingAndBadgesPanel
 *
 * Resolves Agent Mail identities to live tmux panes, inspects border badge
 * publication status, and surfaces identity discrepancies without relabelling:
 *   - GET /api/v1/sessions/{id}/mapping
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  formatApiErrorMessage,
  typedGet,
  type components,
} from "@/lib/api/client";

export type SessionMappingEnvelope =
  components["schemas"]["SessionMappingEnvelopeResponse"];
export type AgentMappingEntryItem = NonNullable<
  SessionMappingEnvelope["entries"]
>[number];
export type PaneBadgeIdentityRecord = NonNullable<
  SessionMappingEnvelope["discrepancies"]
>[number];

export function AgentMappingAndBadgesPanel({
  sessionOptions = [],
}: {
  sessionOptions?: string[];
}) {
  const [sessionInput, setSessionInput] = useState(sessionOptions[0] ?? "");
  const [queriedSession, setQueriedSession] = useState("");

  const mappingQuery = useQuery({
    queryKey: ["session-agent-mapping", queriedSession],
    queryFn: () =>
      typedGet("/api/v1/sessions/{id}/mapping", {
        params: { path: { id: queriedSession } },
      }),
    enabled: Boolean(queriedSession),
  });

  const data = mappingQuery.data;

  return (
    <section
      data-testid="agent-mapping-badges-panel"
      className="rounded-xl border border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-900 p-5 space-y-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-gray-900 dark:text-white">
            Agent Mail &lt;-&gt; Tmux Pane Mapping &amp; Border Badges
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Reconcile NTM-assigned Agent Mail names against canonical pane identity files, verify tmux border badges, and surface identity drift.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <input
            type="text"
            aria-label="Mapping Session Name"
            list="agent-mapping-session-datalist"
            value={sessionInput}
            onChange={(e) => setSessionInput(e.target.value)}
            placeholder="session-name"
            className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
          />
          <datalist id="agent-mapping-session-datalist">
            {sessionOptions.map((name) => (
              <option key={name} value={name} />
            ))}
          </datalist>
          <button
            type="button"
            disabled={!sessionInput.trim() || mappingQuery.isFetching}
            onClick={() => {
              const target = sessionInput.trim();
              if (target === queriedSession) {
                void mappingQuery.refetch();
              } else {
                setQueriedSession(target);
              }
            }}
            className="rounded-md bg-blue-600 hover:bg-blue-700 disabled:opacity-50 px-3 py-1.5 text-xs font-medium text-white"
          >
            {mappingQuery.isFetching ? "Reconciling…" : "Reconcile Mapping"}
          </button>
        </div>
      </div>

      {mappingQuery.isError && (
        <div className="rounded-md border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-950/30 px-3 py-2 text-xs text-red-700 dark:text-red-300">
          {formatApiErrorMessage(mappingQuery.error)}
        </div>
      )}

      {data && (
        <div className="space-y-3">
          {data.badges && (
            <div className="flex flex-wrap items-center gap-3 rounded-lg bg-gray-50 dark:bg-gray-950/50 border border-gray-200 dark:border-gray-800 px-3 py-2 text-xs text-gray-700 dark:text-gray-300">
              <span>
                Pane Badges:{" "}
                <strong>{data.badges.enabled ? "Enabled" : "Disabled"}</strong>
              </span>
              <span>Published: {data.badges.published}</span>
              <span>Cleared: {data.badges.cleared}</span>
              <span>Discrepancies: {data.discrepancies.length}</span>
            </div>
          )}

          {data.entries.length === 0 ? (
            <p className="text-xs text-gray-500 dark:text-gray-400">
              No mapped Agent Mail identities in session &ldquo;{data.session}&rdquo;.
            </p>
          ) : (
            <div className="overflow-x-auto">
              <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-800 text-xs">
                <thead>
                  <tr className="text-left text-gray-500 dark:text-gray-400">
                    <th className="py-2 pr-4">Agent Name</th>
                    <th className="py-2 pr-4">Pane</th>
                    <th className="py-2 pr-4">Harness</th>
                    <th className="py-2 pr-4">Badge Label</th>
                    <th className="py-2">Observation</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                  {data.entries.map((entry) => (
                    <tr key={`${entry.pane_id}-${entry.name}`}>
                      <td className="py-2 pr-4 font-mono font-semibold text-gray-900 dark:text-white">
                        {entry.name}
                      </td>
                      <td className="py-2 pr-4 font-mono text-gray-600 dark:text-gray-300">
                        {entry.pane_id} (#{entry.pane_index})
                      </td>
                      <td className="py-2 pr-4 text-gray-700 dark:text-gray-300">
                        {entry.agent_type || "—"}
                      </td>
                      <td className="py-2 pr-4 font-mono text-indigo-700 dark:text-indigo-300">
                        {entry.identity?.label || "—"}
                      </td>
                      <td className="py-2 text-gray-600 dark:text-gray-300">
                        {entry.identity?.observation_state || "unverified"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {data.discrepancies.length > 0 && (
            <div className="rounded-lg border border-amber-300 dark:border-amber-800 bg-amber-50/70 dark:bg-amber-950/30 p-3 space-y-1.5">
              <div className="text-xs font-semibold text-amber-900 dark:text-amber-200">
                Identity Drift Discrepancies ({data.discrepancies.length})
              </div>
              {data.discrepancies.map((disc, idx) => (
                <div
                  key={`${disc.pane_id}-${idx}`}
                  className="text-xs text-amber-800 dark:text-amber-300 font-mono"
                >
                  {disc.pane_id}: assigned={disc.assigned_name || "none"} resolved=
                  {disc.resolved_name || "none"} {(disc.problems ?? []).join("; ")}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
