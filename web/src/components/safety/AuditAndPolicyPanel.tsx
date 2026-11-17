"use client";

/**
 * AuditAndPolicyPanel
 *
 * Surfaces live HTTP/WS audit trail, session hash-chain integrity verification,
 * automation policy guardrails, policy evaluation decisions, and approval history:
 *   - GET  /api/v1/audit (query HTTP/WS AuditStore records by session, resource, action, user)
 *   - POST /api/v1/audit/verify (verify tamper-evident session JSONL hash chains)
 *   - GET  & PUT /api/v1/policy/automation (auto_commit, auto_push, force_release)
 *   - GET  /api/v1/policy/decisions (durable policy decision evidence ledger)
 *   - GET  /api/v1/approvals/history (historical SLB approval audit log)
 */

import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface HTTPAuditRecord {
  id: number;
  timestamp: string;
  request_id: string;
  user_id: string;
  role: string;
  action: string;
  resource: string;
  resource_id?: string;
  method: string;
  path: string;
  status_code: number;
  duration_ms: number;
  session_id?: string;
  pane_id?: string;
  agent_id?: string;
  details?: string;
  approval_id?: string;
}

interface HTTPAuditListResponse extends ApiEnvelope {
  records: HTTPAuditRecord[];
  count: number;
  total_matches: number;
  has_more: boolean;
}

interface AuditVerifyFileResult {
  file: string;
  status: string;
  verified: boolean;
  error?: string;
}

interface SessionAuditVerifyResponse extends ApiEnvelope {
  session: string;
  verified: boolean;
  files: AuditVerifyFileResult[];
}

interface AutomationSettingsResponse extends ApiEnvelope {
  auto_commit: boolean;
  auto_push: boolean;
  force_release: string;
  modified?: boolean;
}

interface PolicyDecisionRecord {
  id?: string | number;
  entity_type?: string;
  entity_id?: string;
  action?: string;
  outcome?: string;
  decision?: string;
  reason?: string;
  actor?: string;
  created_at?: string;
  timestamp?: string;
}

interface PolicyDecisionsResponse extends ApiEnvelope {
  decisions?: PolicyDecisionRecord[];
  entries?: PolicyDecisionRecord[];
  count?: number;
}

interface HistoricalApproval {
  id: string;
  action: string;
  resource: string;
  requestor: string;
  reason: string;
  slb_required: boolean;
  status: string;
  created_at: string;
  approved_by?: string;
  approved_at?: string;
}

interface ApprovalsHistoryResponse extends ApiEnvelope {
  approvals: HistoricalApproval[];
  count: number;
}

export function AuditAndPolicyPanel({
  initialAutomation,
}: {
  initialAutomation?: {
    auto_commit?: boolean;
    auto_push?: boolean;
    force_release?: string;
  };
}) {
  const queryClient = useQueryClient();

  // 1. Automation Guardrails (GET + PUT /api/v1/policy/automation)
  const [automationRequested, setAutomationRequested] = useState(false);
  const [autoCommit, setAutoCommit] = useState<boolean>(
    Boolean(initialAutomation?.auto_commit ?? true)
  );
  const [autoPush, setAutoPush] = useState<boolean>(
    Boolean(initialAutomation?.auto_push ?? false)
  );
  const [forceRelease, setForceRelease] = useState<
    "never" | "approval" | "auto"
  >(
    (initialAutomation?.force_release as "never" | "approval" | "auto") ||
      "approval"
  );
  const [automationSavedMsg, setAutomationSavedMsg] = useState<string | null>(
    null
  );
  const [automationError, setAutomationError] = useState<string | null>(null);

  useEffect(() => {
    if (!initialAutomation) {
      return;
    }
    if (typeof initialAutomation.auto_commit === "boolean") {
      setAutoCommit(initialAutomation.auto_commit);
    }
    if (typeof initialAutomation.auto_push === "boolean") {
      setAutoPush(initialAutomation.auto_push);
    }
    if (
      initialAutomation.force_release === "never" ||
      initialAutomation.force_release === "approval" ||
      initialAutomation.force_release === "auto"
    ) {
      setForceRelease(initialAutomation.force_release);
    }
  }, [
    initialAutomation?.auto_commit,
    initialAutomation?.auto_push,
    initialAutomation?.force_release,
  ]);

  const automationQuery = useQuery({
    queryKey: ["safety", "policy-automation"],
    queryFn: () =>
      apiFetch<AutomationSettingsResponse>("/api/v1/policy/automation"),
    enabled: automationRequested,
    retry: false,
  });

  useEffect(() => {
    if (!automationQuery.data) {
      return;
    }
    setAutoCommit(Boolean(automationQuery.data.auto_commit));
    setAutoPush(Boolean(automationQuery.data.auto_push));
    if (
      automationQuery.data.force_release === "never" ||
      automationQuery.data.force_release === "approval" ||
      automationQuery.data.force_release === "auto"
    ) {
      setForceRelease(automationQuery.data.force_release);
    }
  }, [automationQuery.data]);

  const saveAutomationMutation = useMutation({
    mutationFn: () =>
      apiFetch<AutomationSettingsResponse>("/api/v1/policy/automation", {
        method: "PUT",
        body: JSON.stringify({
          auto_commit: autoCommit,
          auto_push: autoPush,
          force_release: forceRelease,
        }),
      }),
    onSuccess: (res) => {
      setAutomationSavedMsg(
        `Saved automation policy (auto_commit=${String(res.auto_commit)}, auto_push=${String(res.auto_push)}, force_release=${res.force_release}).`
      );
      setAutomationError(null);
      void queryClient.invalidateQueries({ queryKey: ["safety", "policy"] });
    },
    onError: (err) => {
      setAutomationError(formatApiErrorMessage(err));
    },
  });

  // 2. Live HTTP/WS Audit Trail (GET /api/v1/audit) + Session Chain Verify (POST /api/v1/audit/verify)
  const [auditRequested, setAuditRequested] = useState(false);
  const [auditSessionFilter, setAuditSessionFilter] = useState("");
  const [auditResourceFilter, setAuditResourceFilter] = useState("");
  const [auditActionFilter, setAuditActionFilter] = useState("");
  const [appliedAuditFilters, setAppliedAuditFilters] = useState({
    session: "",
    resource: "",
    action: "",
  });

  const auditQuery = useQuery({
    queryKey: ["safety", "http-audit", appliedAuditFilters],
    queryFn: () => {
      const params = new URLSearchParams();
      params.set("limit", "30");
      if (appliedAuditFilters.session) {
        params.set("session_id", appliedAuditFilters.session);
      }
      if (appliedAuditFilters.resource) {
        params.set("resource", appliedAuditFilters.resource);
      }
      if (appliedAuditFilters.action) {
        params.set("action", appliedAuditFilters.action);
      }
      return apiFetch<HTTPAuditListResponse>(
        `/api/v1/audit?${params.toString()}`
      );
    },
    enabled: auditRequested,
    retry: false,
  });

  const [verifySession, setVerifySession] = useState("");
  const [verifyResult, setVerifyResult] =
    useState<SessionAuditVerifyResponse | null>(null);
  const [verifyError, setVerifyError] = useState<string | null>(null);

  const verifyChainMutation = useMutation({
    mutationFn: (sessionName: string) =>
      apiFetch<SessionAuditVerifyResponse>("/api/v1/audit/verify", {
        method: "POST",
        body: JSON.stringify({ session: sessionName }),
      }),
    onSuccess: (res) => {
      setVerifyResult(res);
      setVerifyError(null);
    },
    onError: (err) => {
      setVerifyResult(null);
      setVerifyError(formatApiErrorMessage(err));
    },
  });

  // 3. Policy Decisions Ledger (GET /api/v1/policy/decisions), Approval History (GET /api/v1/approvals/history),
  //    and Session JSONL Audit Logs (GET /api/v1/audit/sessions)
  const [decisionsRequested, setDecisionsRequested] = useState(false);
  const [historyRequested, setHistoryRequested] = useState(false);
  const [sessionAuditQueryInput, setSessionAuditQueryInput] = useState("");
  const [sessionAuditRegexInput, setSessionAuditRegexInput] = useState("");
  const [sessionAuditRequested, setSessionAuditRequested] = useState(false);

  const sessionAuditQuery = useQuery({
    queryKey: [
      "safety",
      "session-audit-jsonl",
      sessionAuditQueryInput.trim(),
      sessionAuditRegexInput.trim(),
    ],
    queryFn: () => {
      const params = new URLSearchParams({ limit: "25" });
      if (sessionAuditQueryInput.trim()) {
        params.set("session", sessionAuditQueryInput.trim());
      }
      if (sessionAuditRegexInput.trim()) {
        params.set("search", sessionAuditRegexInput.trim());
      }
      return apiFetch<
        ApiEnvelope & {
          action?: string;
          count?: number;
          entries?: Array<{
            sequence_num: number;
            event_type: string;
            actor: string;
            target: string;
            timestamp?: string;
          }>;
          logs?: Array<{ file: string; session?: string; size_bytes: number }>;
        }
      >(`/api/v1/audit/sessions?${params.toString()}`);
    },
    enabled: sessionAuditRequested,
    retry: false,
  });

  const decisionsQuery = useQuery({
    queryKey: ["safety", "policy-decisions"],
    queryFn: () =>
      apiFetch<PolicyDecisionsResponse>("/api/v1/policy/decisions?limit=25"),
    enabled: decisionsRequested,
    retry: false,
  });

  const approvalHistoryQuery = useQuery({
    queryKey: ["safety", "approvals-history-panel"],
    queryFn: () =>
      apiFetch<ApprovalsHistoryResponse>("/api/v1/approvals/history?limit=25"),
    enabled: historyRequested,
    retry: false,
  });

  const handleAuditFilterSubmit = (e: FormEvent) => {
    e.preventDefault();
    const next = {
      session: auditSessionFilter.trim(),
      resource: auditResourceFilter.trim(),
      action: auditActionFilter.trim(),
    };
    setAppliedAuditFilters(next);
    if (
      auditRequested &&
      next.session === appliedAuditFilters.session &&
      next.resource === appliedAuditFilters.resource &&
      next.action === appliedAuditFilters.action
    ) {
      void auditQuery.refetch();
    } else {
      setAuditRequested(true);
    }
  };

  const handleVerifySubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = verifySession.trim();
    if (!trimmed) {
      setVerifyError("Session name is required to verify hash-chain logs.");
      return;
    }
    verifyChainMutation.mutate(trimmed);
  };

  const auditRecords = auditQuery.data?.records ?? [];
  const decisionRows =
    decisionsQuery.data?.decisions ?? decisionsQuery.data?.entries ?? [];
  const approvalHistoryRows = approvalHistoryQuery.data?.approvals ?? [];

  return (
    <section
      aria-label="Audit Trail, Automation Guardrails & Decision Ledger"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-6"
    >
      <div>
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
          Audit Trail, Automation Guardrails & Decision Ledger
        </h2>
        <p className="text-xs text-gray-500 dark:text-gray-400">
          Inspect mutating HTTP/WS audit records (<code className="font-mono">/api/v1/audit</code>), verify session hash chains (<code className="font-mono">/api/v1/audit/verify</code>), configure automation policy (<code className="font-mono">/api/v1/policy/automation</code>), and audit policy/approval history.
        </p>
      </div>

      {/* Top Grid: Automation Guardrails + Hash-Chain Verifier */}
      <div className="grid gap-4 lg:grid-cols-2">
        <div
          role="region"
          aria-label="Automation Guardrails"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Automation Guardrails
            </h3>
            <button
              type="button"
              disabled={automationQuery.isFetching}
              onClick={() => {
                if (automationRequested) {
                  void automationQuery.refetch();
                } else {
                  setAutomationRequested(true);
                }
              }}
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-2.5 py-1 text-xs text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {automationQuery.isFetching
                ? "Loading…"
                : "Refresh Automation Policy"}
            </button>
          </div>

          <div className="flex flex-wrap gap-4 text-xs text-gray-700 dark:text-gray-300">
            <label
              htmlFor="policy-auto-commit"
              className="flex items-center gap-1.5 cursor-pointer"
            >
              <input
                id="policy-auto-commit"
                type="checkbox"
                checked={autoCommit}
                onChange={(e) => setAutoCommit(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              Allow auto_commit
            </label>
            <label
              htmlFor="policy-auto-push"
              className="flex items-center gap-1.5 cursor-pointer"
            >
              <input
                id="policy-auto-push"
                type="checkbox"
                checked={autoPush}
                onChange={(e) => setAutoPush(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              Allow auto_push
            </label>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <div>
              <label
                htmlFor="policy-force-release"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Reservation Force-Release Gate
              </label>
              <select
                id="policy-force-release"
                value={forceRelease}
                onChange={(e) =>
                  setForceRelease(
                    e.target.value as "never" | "approval" | "auto"
                  )
                }
                className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
              >
                <option value="approval">approval (SLB two-person rule)</option>
                <option value="never">never (always block)</option>
                <option value="auto">auto (allow without approval)</option>
              </select>
            </div>

            <button
              type="button"
              disabled={saveAutomationMutation.isPending}
              onClick={() => saveAutomationMutation.mutate()}
              className="mt-5 rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {saveAutomationMutation.isPending
                ? "Saving…"
                : "Save Automation Policy"}
            </button>
          </div>

          {automationError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {automationError}
            </div>
          )}
          {automationSavedMsg && (
            <div
              data-testid="automation-policy-saved"
              className="text-xs text-emerald-700 dark:text-emerald-300"
            >
              {automationSavedMsg}
            </div>
          )}
        </div>

        {/* Hash-Chain Audit Verifier */}
        <form
          onSubmit={handleVerifySubmit}
          aria-label="Session Audit Chain Verifier"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Verify Session Hash-Chain Integrity
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Validate sequence numbers, SHA-256 chain links, and checksums for a session&apos;s JSONL audit logs.
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <label htmlFor="verify-audit-session" className="sr-only">
              Session to verify
            </label>
            <input
              id="verify-audit-session"
              aria-label="Session to verify"
              value={verifySession}
              onChange={(e) => setVerifySession(e.target.value)}
              placeholder="Session name (e.g. ntm-prod)"
              className="flex-1 min-w-[180px] rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
            <button
              type="submit"
              disabled={verifyChainMutation.isPending}
              className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {verifyChainMutation.isPending
                ? "Verifying…"
                : "Verify Audit Chain"}
            </button>
          </div>

          {verifyError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {verifyError}
            </div>
          )}

          {verifyResult && (
            <div
              data-testid="audit-chain-verify-result"
              className={`rounded border p-2.5 text-xs space-y-1 ${
                verifyResult.verified
                  ? "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
                  : "border-red-200 bg-red-50 text-red-900 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200"
              }`}
            >
              <div className="font-semibold">
                {verifyResult.verified ? "PASS" : "FAIL"} · Session{" "}
                <span className="font-mono">{verifyResult.session}</span> (
                {verifyResult.files.length} file
                {verifyResult.files.length === 1 ? "" : "s"})
              </div>
              {verifyResult.files.map((f) => (
                <div key={f.file} className="font-mono text-[11px]">
                  [{f.status}] {f.file}
                  {f.error ? ` — ${f.error}` : ""}
                </div>
              ))}
            </div>
          )}
        </form>
      </div>

      {/* Live HTTP/WS Audit Log */}
      <div
        role="region"
        aria-label="HTTP & WebSocket Audit Trail"
        className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
      >
        <form
          onSubmit={handleAuditFilterSubmit}
          className="flex flex-wrap items-end gap-2.5"
        >
          <div className="mr-auto">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              HTTP & WebSocket Mutation Audit Log
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Filter durable <code className="font-mono">audit_records</code> by session, resource, or action.
            </p>
          </div>

          <div>
            <label
              htmlFor="http-audit-session"
              className="block text-[11px] text-gray-600 dark:text-gray-300"
            >
              Session
            </label>
            <input
              id="http-audit-session"
              value={auditSessionFilter}
              onChange={(e) => setAuditSessionFilter(e.target.value)}
              placeholder="any session"
              className="w-32 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>

          <div>
            <label
              htmlFor="http-audit-resource"
              className="block text-[11px] text-gray-600 dark:text-gray-300"
            >
              Resource
            </label>
            <input
              id="http-audit-resource"
              value={auditResourceFilter}
              onChange={(e) => setAuditResourceFilter(e.target.value)}
              placeholder="sessions / approvals"
              className="w-36 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>

          <div>
            <label
              htmlFor="http-audit-action"
              className="block text-[11px] text-gray-600 dark:text-gray-300"
            >
              Action
            </label>
            <select
              id="http-audit-action"
              value={auditActionFilter}
              onChange={(e) => setAuditActionFilter(e.target.value)}
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-900 dark:text-white"
            >
              <option value="">all actions</option>
              <option value="create">create</option>
              <option value="update">update</option>
              <option value="delete">delete</option>
              <option value="approve">approve</option>
              <option value="deny">deny</option>
              <option value="execute">execute</option>
            </select>
          </div>

          <button
            type="submit"
            disabled={auditQuery.isFetching}
            className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {auditQuery.isFetching
              ? "Loading Audit…"
              : auditRequested
                ? "Refresh HTTP Audit"
                : "Load HTTP Audit Log"}
          </button>
        </form>

        {auditRequested && (
          <div data-testid="http-audit-log-table" className="space-y-2">
            {auditQuery.isFetching && !auditQuery.data ? (
              <div className="text-xs text-gray-500 dark:text-gray-400">
                Loading HTTP/WS audit records…
              </div>
            ) : auditQuery.error ? (
              <div className="text-xs text-red-600 dark:text-red-400">
                {formatApiErrorMessage(auditQuery.error)}
              </div>
            ) : auditRecords.length === 0 ? (
              <div className="text-xs text-gray-500 dark:text-gray-400">
                No matching HTTP/WS audit records found.
              </div>
            ) : (
              <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-700">
                <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700 text-xs">
                  <thead className="bg-gray-100 dark:bg-gray-900 text-gray-600 dark:text-gray-300">
                    <tr>
                      <th className="px-2.5 py-1.5 text-left">Time</th>
                      <th className="px-2.5 py-1.5 text-left">Principal</th>
                      <th className="px-2.5 py-1.5 text-left">Action</th>
                      <th className="px-2.5 py-1.5 text-left">Method / Path</th>
                      <th className="px-2.5 py-1.5 text-left">Session</th>
                      <th className="px-2.5 py-1.5 text-right">Status</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-gray-200 dark:divide-gray-700 bg-white dark:bg-gray-800">
                    {auditRecords.map((rec) => (
                      <tr key={rec.id}>
                        <td className="px-2.5 py-1.5 font-mono text-[11px] text-gray-500 dark:text-gray-400">
                          {new Date(rec.timestamp).toLocaleTimeString()}
                        </td>
                        <td className="px-2.5 py-1.5 font-mono text-gray-800 dark:text-gray-200">
                          {rec.user_id} ({rec.role})
                        </td>
                        <td className="px-2.5 py-1.5 font-medium text-gray-900 dark:text-white">
                          {rec.action} · {rec.resource}
                        </td>
                        <td className="px-2.5 py-1.5 font-mono text-[11px] text-gray-700 dark:text-gray-300">
                          {rec.method} {rec.path}
                        </td>
                        <td className="px-2.5 py-1.5 font-mono text-gray-600 dark:text-gray-300">
                          {rec.session_id || "—"}
                        </td>
                        <td className="px-2.5 py-1.5 text-right font-mono">
                          {rec.status_code} ({rec.duration_ms}ms)
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}
      </div>

      {/* Bottom Grid: Policy Decisions Ledger & Approval History */}
      <div className="grid gap-4 lg:grid-cols-2">
        <div
          role="region"
          aria-label="Policy Decision Ledger"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div className="flex items-center justify-between gap-2">
            <div>
              <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                Policy Decision Ledger
              </h3>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Recent durable policy & routing decisions (<code className="font-mono">/api/v1/policy/decisions</code>).
              </p>
            </div>
            <button
              type="button"
              disabled={decisionsQuery.isFetching}
              onClick={() => {
                if (decisionsRequested) {
                  void decisionsQuery.refetch();
                } else {
                  setDecisionsRequested(true);
                }
              }}
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-2.5 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {decisionsQuery.isFetching
                ? "Loading…"
                : decisionsRequested
                  ? "Refresh Decisions"
                  : "Load Policy Decisions"}
            </button>
          </div>

          {decisionsRequested && (
            <div data-testid="policy-decisions-list" className="text-xs space-y-1.5">
              {decisionsQuery.isFetching && !decisionsQuery.data ? (
                <div className="text-gray-500 dark:text-gray-400">
                  Loading policy decisions…
                </div>
              ) : decisionsQuery.error ? (
                <div className="text-red-600 dark:text-red-400">
                  {formatApiErrorMessage(decisionsQuery.error)}
                </div>
              ) : decisionRows.length === 0 ? (
                <div className="text-gray-500 dark:text-gray-400">
                  No recorded policy decisions yet.
                </div>
              ) : (
                <ul className="divide-y divide-gray-200 dark:divide-gray-700">
                  {decisionRows.map((d, idx) => (
                    <li
                      key={`${idx}-${String(d.id ?? d.entity_id ?? "")}`}
                      className="py-1.5 flex items-center justify-between gap-2"
                    >
                      <span className="font-mono text-gray-800 dark:text-gray-200">
                        {d.action || d.entity_type || "decision"}{" "}
                        {d.entity_id ? `(${d.entity_id})` : ""}
                      </span>
                      <span className="text-gray-600 dark:text-gray-300">
                        {d.outcome || d.decision || "recorded"}
                        {d.reason ? ` · ${d.reason}` : ""}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>

        <div
          role="region"
          aria-label="SLB Approval Audit History"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div className="flex items-center justify-between gap-2">
            <div>
              <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                SLB Approval Audit History
              </h3>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Complete approval trail from <code className="font-mono">/api/v1/approvals/history</code>.
              </p>
            </div>
            <button
              type="button"
              disabled={approvalHistoryQuery.isFetching}
              onClick={() => {
                if (historyRequested) {
                  void approvalHistoryQuery.refetch();
                } else {
                  setHistoryRequested(true);
                }
              }}
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-2.5 py-1 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {approvalHistoryQuery.isFetching
                ? "Loading…"
                : historyRequested
                  ? "Refresh History"
                  : "Load Approval History"}
            </button>
          </div>

          {historyRequested && (
            <div data-testid="approval-history-list" className="text-xs space-y-1.5">
              {approvalHistoryQuery.isFetching && !approvalHistoryQuery.data ? (
                <div className="text-gray-500 dark:text-gray-400">
                  Loading approval history…
                </div>
              ) : approvalHistoryQuery.error ? (
                <div className="text-red-600 dark:text-red-400">
                  {formatApiErrorMessage(approvalHistoryQuery.error)}
                </div>
              ) : approvalHistoryRows.length === 0 ? (
                <div className="text-gray-500 dark:text-gray-400">
                  No historical SLB approval records found.
                </div>
              ) : (
                <ul className="divide-y divide-gray-200 dark:divide-gray-700">
                  {approvalHistoryRows.map((a) => (
                    <li
                      key={a.id}
                      className="py-1.5 flex items-center justify-between gap-2"
                    >
                      <span className="font-mono text-gray-800 dark:text-gray-200">
                        {a.id} · {a.action} ({a.resource})
                      </span>
                      <span className="text-gray-600 dark:text-gray-300">
                        {a.status}
                        {a.approved_by ? ` by ${a.approved_by}` : ""}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Session JSONL Audit Log Browser (/api/v1/audit/sessions) */}
      <div
        role="region"
        aria-label="Session JSONL Audit Logs"
        className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
      >
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2">
          <div>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
              Session JSONL Audit Logs
            </h3>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Browse or regex-search tamper-evident per-session JSONL audit
              logs (<code className="font-mono">/api/v1/audit/sessions</code>).
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <input
              aria-label="Session JSONL audit session"
              value={sessionAuditQueryInput}
              onChange={(e) => setSessionAuditQueryInput(e.target.value)}
              placeholder="Session or all..."
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1 text-xs text-gray-900 dark:text-white"
            />
            <input
              aria-label="Session JSONL audit regex"
              value={sessionAuditRegexInput}
              onChange={(e) => setSessionAuditRegexInput(e.target.value)}
              placeholder="Regex search..."
              className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1 text-xs font-mono text-gray-900 dark:text-white"
            />
            <button
              type="button"
              disabled={sessionAuditQuery.isFetching}
              onClick={() =>
                sessionAuditRequested
                  ? void sessionAuditQuery.refetch()
                  : setSessionAuditRequested(true)
              }
              className="rounded bg-blue-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {sessionAuditQuery.isFetching
                ? "Searching…"
                : "Query Session Audit"}
            </button>
          </div>
        </div>

        {sessionAuditRequested && sessionAuditQuery.data && (
          <div
            data-testid="session-jsonl-audit-results"
            className="text-xs space-y-1.5"
          >
            <div className="font-mono text-gray-600 dark:text-gray-400">
              Action: {sessionAuditQuery.data.action || "list"} · Returned:{" "}
              {sessionAuditQuery.data.count ??
                (sessionAuditQuery.data.entries?.length ||
                  sessionAuditQuery.data.logs?.length ||
                  0)}
            </div>
            {(sessionAuditQuery.data.entries ?? []).map((entry) => (
              <div
                key={`${entry.sequence_num}-${entry.timestamp}`}
                className="font-mono text-gray-800 dark:text-gray-200"
              >
                #{entry.sequence_num} {entry.event_type} ({entry.actor} →{" "}
                {entry.target})
              </div>
            ))}
            {(sessionAuditQuery.data.logs ?? []).map((logFile) => (
              <div
                key={logFile.file}
                className="font-mono text-gray-700 dark:text-gray-300"
              >
                {logFile.file} ({logFile.size_bytes} B)
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}
