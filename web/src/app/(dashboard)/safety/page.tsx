"use client";

/**
 * Safety Page
 *
 * Approvals, policy management, and hook/guard status.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { apiRequestSignal, getAuthHeaders, getBaseUrl } from "@/lib/api/client";

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface SafetyStatus {
  installed: boolean;
  policy_path?: string;
  blocked_rules: number;
  approval_rules: number;
  allowed_rules: number;
  wrapper_path?: string;
  git_wrapper_installed: boolean;
  rm_wrapper_installed: boolean;
  hook_installed: boolean;
}

interface SafetyStatusResponse extends ApiEnvelope, SafetyStatus {}

interface PolicyStats {
  blocked: number;
  approval: number;
  allowed: number;
  slb_rules: number;
}

interface PolicyRuleSummary {
  pattern: string;
  reason?: string;
  slb?: boolean;
}

interface PolicyRules {
  blocked?: PolicyRuleSummary[];
  approval_required?: PolicyRuleSummary[];
  allowed?: PolicyRuleSummary[];
}

interface AutomationConfig {
  auto_commit?: boolean;
  auto_push?: boolean;
  force_release?: string;
}

interface PolicyGetResponse extends ApiEnvelope {
  version: number;
  policy_path?: string;
  content?: string;
  is_default: boolean;
  stats: PolicyStats;
  automation: AutomationConfig;
  rules?: PolicyRules;
}

interface PolicyValidateResponse extends ApiEnvelope {
  valid: boolean;
  policy_path?: string;
  errors?: string[];
  warnings?: string[];
}

interface Approval {
  id: string;
  action: string;
  resource: string;
  requestor: string;
  reason: string;
  slb_required: boolean;
  status: string;
  created_at: string;
  expires_at: string;
  approved_by?: string;
  approved_at?: string;
}

interface ApprovalsListResponse extends ApiEnvelope {
  approvals: Approval[];
  count: number;
}

interface BlockedEntry {
  time: string;
  command: string;
  reason: string;
  pattern?: string;
}

interface BlockedResponse extends ApiEnvelope {
  entries: BlockedEntry[];
  count: number;
}

type Notice = { type: "success" | "error"; message: string };

type ApprovalStatusFilter = "all" | "pending" | "approved" | "denied" | "expired";

async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const baseUrl = getBaseUrl();
  const headers: HeadersInit = {
    "Content-Type": "application/json",
    ...getAuthHeaders(),
    ...(options.headers || {}),
  };

  const response = await fetch(`${baseUrl}${path}`, {
    ...options,
    signal: options.signal ?? apiRequestSignal(),
    headers,
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
    const message = envelope?.error || `Request failed (${response.status})`;
    throw new Error(message);
  }

  return data as T;
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "Unexpected error";
}

function formatTimestamp(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatRelativeTime(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const now = new Date();
  const diffMs = date.getTime() - now.getTime();
  const diffMins = Math.round(diffMs / 60000);

  if (diffMins < 0) {
    const absMins = Math.abs(diffMins);
    if (absMins < 60) return `${absMins}m ago`;
    const hours = Math.floor(absMins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    return `${days}d ago`;
  } else {
    if (diffMins < 60) return `in ${diffMins}m`;
    const hours = Math.floor(diffMins / 60);
    if (hours < 24) return `in ${hours}h`;
    const days = Math.floor(hours / 24);
    return `in ${days}d`;
  }
}

function statusBadge(status: string): string {
  switch (status) {
    case "pending":
      return "bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300";
    case "approved":
      return "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300";
    case "denied":
      return "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300";
    case "expired":
      return "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400";
    default:
      return "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300";
  }
}

export default function SafetyPage() {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState<Notice | null>(null);
  const [statusFilter, setStatusFilter] = useState<ApprovalStatusFilter>("pending");
  const [selectedApprovalId, setSelectedApprovalId] = useState<string | null>(null);
  const [policyYaml, setPolicyYaml] = useState("");
  const [showPolicyEditor, setShowPolicyEditor] = useState(false);
  const noticeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [actionBusy, setActionBusy] = useState({
    approve: false,
    deny: false,
    validate: false,
    save: false,
    reset: false,
    install: false,
    uninstall: false,
  });

  useEffect(() => {
    return () => {
      if (noticeTimeoutRef.current !== null) {
        clearTimeout(noticeTimeoutRef.current);
      }
    };
  }, []);

  const setStatusNotice = useCallback((next: Notice) => {
    if (noticeTimeoutRef.current !== null) {
      clearTimeout(noticeTimeoutRef.current);
    }
    setNotice(next);
    noticeTimeoutRef.current = setTimeout(() => {
      setNotice(null);
      noticeTimeoutRef.current = null;
    }, 5000);
  }, []);

  // Safety status query
  const statusQuery = useQuery({
    queryKey: ["safety", "status"],
    queryFn: () => apiFetch<SafetyStatusResponse>("/api/v1/safety/status"),
    refetchInterval: 30000,
  });

  // Policy query
  const policyQuery = useQuery({
    queryKey: ["safety", "policy"],
    queryFn: () => apiFetch<PolicyGetResponse>("/api/v1/policy?rules=true&content=true"),
    refetchInterval: 30000,
  });

  // Approvals query
  const approvalsQuery = useQuery({
    queryKey: ["safety", "approvals", statusFilter],
    queryFn: () => {
      const params = statusFilter !== "all" ? `?status=${statusFilter}` : "";
      return apiFetch<ApprovalsListResponse>(`/api/v1/approvals${params}`);
    },
    refetchInterval: 5000,
  });

  const pendingApprovalsQuery = useQuery({
    queryKey: ["safety", "approvals", "pending-card"],
    queryFn: () => apiFetch<ApprovalsListResponse>("/api/v1/approvals?status=pending"),
    refetchInterval: 5000,
  });

  // Blocked commands query
  const blockedQuery = useQuery({
    queryKey: ["safety", "blocked"],
    queryFn: () => apiFetch<BlockedResponse>("/api/v1/safety/blocked?hours=24&limit=20"),
    refetchInterval: 15000,
  });

  // Policy validation state
  const [validationResult, setValidationResult] = useState<PolicyValidateResponse | null>(null);

  const approvalsList = useMemo(() => approvalsQuery.data?.approvals ?? [], [approvalsQuery.data?.approvals]);
  const blockedList = blockedQuery.data?.entries ?? [];

  const selectedApproval = useMemo(() => {
    if (!selectedApprovalId) return null;
    return approvalsList.find((a) => a.id === selectedApprovalId) ?? null;
  }, [approvalsList, selectedApprovalId]);

  // Auto-select the first approval visible in the current filter.
  useEffect(() => {
    if (approvalsList.length === 0) {
      if (selectedApprovalId !== null) {
        setSelectedApprovalId(null);
      }
      return;
    }

    const hasSelectedApproval = selectedApprovalId
      ? approvalsList.some((approval) => approval.id === selectedApprovalId)
      : false;

    if (!hasSelectedApproval) {
      setSelectedApprovalId(approvalsList[0].id);
    }
  }, [approvalsList, selectedApprovalId]);

  const handleApprove = useCallback(async () => {
    if (!selectedApprovalId) return;
    setActionBusy((prev) => ({ ...prev, approve: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Safety] Approve", { selectedApprovalId });
      }
      await apiFetch<ApiEnvelope>(
        `/api/v1/approvals/${selectedApprovalId}/approve`,
        { method: "POST" }
      );
      queryClient.invalidateQueries({ queryKey: ["safety", "approvals"] });
      setStatusNotice({ type: "success", message: "Approval granted." });
      setSelectedApprovalId(null);
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, approve: false }));
    }
  }, [queryClient, selectedApprovalId, setStatusNotice]);

  const handleDeny = useCallback(async () => {
    if (!selectedApprovalId) return;
    setActionBusy((prev) => ({ ...prev, deny: true }));
    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Safety] Deny", { selectedApprovalId });
      }
      await apiFetch<ApiEnvelope>(
        `/api/v1/approvals/${selectedApprovalId}/deny`,
        { method: "POST" }
      );
      queryClient.invalidateQueries({ queryKey: ["safety", "approvals"] });
      setStatusNotice({ type: "success", message: "Approval denied." });
      setSelectedApprovalId(null);
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, deny: false }));
    }
  }, [queryClient, selectedApprovalId, setStatusNotice]);

  const handleValidatePolicy = useCallback(async () => {
    setActionBusy((prev) => ({ ...prev, validate: true }));
    try {
      const result = await apiFetch<PolicyValidateResponse>(
        "/api/v1/policy/validate",
        {
          method: "POST",
          body: JSON.stringify({ content: policyYaml }),
        }
      );
      setValidationResult(result);
      if (result.valid) {
        setStatusNotice({ type: "success", message: "Policy is valid." });
      } else {
        setStatusNotice({ type: "error", message: "Policy validation failed." });
      }
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, validate: false }));
    }
  }, [policyYaml, setStatusNotice]);

  const handleSavePolicy = useCallback(async () => {
    setActionBusy((prev) => ({ ...prev, save: true }));
    try {
      await apiFetch<ApiEnvelope>("/api/v1/policy", {
        method: "PUT",
        body: JSON.stringify({ content: policyYaml }),
      });
      queryClient.invalidateQueries({ queryKey: ["safety", "policy"] });
      setStatusNotice({ type: "success", message: "Policy saved." });
      setShowPolicyEditor(false);
      setValidationResult(null);
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, save: false }));
    }
  }, [policyYaml, queryClient, setStatusNotice]);

  const handleResetPolicy = useCallback(async () => {
    setActionBusy((prev) => ({ ...prev, reset: true }));
    try {
      await apiFetch<ApiEnvelope>("/api/v1/policy/reset", { method: "POST" });
      queryClient.invalidateQueries({ queryKey: ["safety", "policy"] });
      setStatusNotice({ type: "success", message: "Policy reset to default." });
      setShowPolicyEditor(false);
      setValidationResult(null);
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, reset: false }));
    }
  }, [queryClient, setStatusNotice]);

  const handleInstall = useCallback(async () => {
    setActionBusy((prev) => ({ ...prev, install: true }));
    try {
      await apiFetch<ApiEnvelope>("/api/v1/safety/install", { method: "POST" });
      queryClient.invalidateQueries({ queryKey: ["safety", "status"] });
      setStatusNotice({ type: "success", message: "Safety guards installed." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, install: false }));
    }
  }, [queryClient, setStatusNotice]);

  const handleUninstall = useCallback(async () => {
    setActionBusy((prev) => ({ ...prev, uninstall: true }));
    try {
      await apiFetch<ApiEnvelope>("/api/v1/safety/uninstall", { method: "POST" });
      queryClient.invalidateQueries({ queryKey: ["safety", "status"] });
      setStatusNotice({ type: "success", message: "Safety guards uninstalled." });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, uninstall: false }));
    }
  }, [queryClient, setStatusNotice]);

  const connectionError =
    statusQuery.error ??
    policyQuery.error ??
    pendingApprovalsQuery.error ??
    approvalsQuery.error ??
    blockedQuery.error;

  const safetyStatus = statusQuery.data;
  const policyData = policyQuery.data;
  const wrappersInstalled =
    safetyStatus?.git_wrapper_installed && safetyStatus?.rm_wrapper_installed;
  const wrapperStatusText = wrappersInstalled
    ? "Installed"
    : safetyStatus?.git_wrapper_installed || safetyStatus?.rm_wrapper_installed
      ? "Partial"
      : "Not installed";

  useEffect(() => {
    if (showPolicyEditor) {
      return;
    }
    setPolicyYaml(policyData?.content || "");
    setValidationResult(null);
  }, [policyData?.content, showPolicyEditor]);

  return (
    <div className="space-y-8">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Safety
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            Approvals, policy management, and destructive command protection.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          {safetyStatus?.installed ? (
            <button
              type="button"
              onClick={handleUninstall}
              disabled={actionBusy.uninstall}
              className="px-3 py-1.5 rounded-md text-xs font-medium border border-red-200 text-red-600 dark:border-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
            >
              Uninstall Guards
            </button>
          ) : (
            <button
              type="button"
              onClick={handleInstall}
              disabled={actionBusy.install}
              className="px-3 py-1.5 rounded-md text-xs font-medium bg-green-600 text-white hover:bg-green-700 disabled:opacity-50"
            >
              Install Guards
            </button>
          )}
        </div>
      </div>

      {connectionError && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md">
          <p className="text-sm text-red-700 dark:text-red-400">
            Safety error: {getErrorMessage(connectionError)}
          </p>
        </div>
      )}

      {notice && (
        <div
          className={`p-3 rounded-md border text-sm ${
            notice.type === "success"
              ? "bg-green-50 border-green-200 text-green-700 dark:bg-green-900/20 dark:border-green-800 dark:text-green-300"
              : "bg-red-50 border-red-200 text-red-700 dark:bg-red-900/20 dark:border-red-800 dark:text-red-300"
          }`}
        >
          {notice.message}
        </div>
      )}

      {/* Status Cards */}
      <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Guard Status
          </div>
          <div className="mt-2 flex items-center gap-2">
            <span
              className={`h-3 w-3 rounded-full ${
                safetyStatus?.installed
                  ? "bg-green-500"
                  : "bg-gray-300 dark:bg-gray-600"
              }`}
            />
            <span className="text-lg font-semibold text-gray-900 dark:text-white">
              {safetyStatus?.installed ? "Active" : "Inactive"}
            </span>
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Wrappers: {wrapperStatusText} · Hook:{" "}
            {safetyStatus?.hook_installed ? "Installed" : "Not installed"}
          </div>
        </div>

        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Policy Rules
          </div>
          <div className="mt-2 text-lg font-semibold text-gray-900 dark:text-white">
            {(policyData?.stats?.blocked ?? 0) +
              (policyData?.stats?.approval ?? 0) +
              (policyData?.stats?.allowed ?? 0)}{" "}
            rules
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {policyData?.stats?.blocked ?? 0} blocked · {policyData?.stats?.approval ?? 0} approval · {policyData?.stats?.allowed ?? 0} allowed
          </div>
        </div>

        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Pending Approvals
          </div>
          <div className="mt-2 text-lg font-semibold text-gray-900 dark:text-white">
            {pendingApprovalsQuery.data?.count ?? 0}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {(policyData?.stats?.slb_rules ?? 0) > 0
              ? `${policyData?.stats?.slb_rules} require SLB`
              : "No SLB rules"}
          </div>
        </div>

        <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Blocked (24h)
          </div>
          <div className="mt-2 text-lg font-semibold text-gray-900 dark:text-white">
            {blockedQuery.data?.count ?? 0}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Dangerous commands intercepted
          </div>
        </div>
      </section>

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Pending Approvals */}
        <section className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Approvals
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Review and approve or deny pending requests.
              </p>
            </div>
            <select
              value={statusFilter}
              onChange={(event) =>
                setStatusFilter(event.target.value as ApprovalStatusFilter)
              }
              className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-700 dark:text-gray-200"
            >
              <option value="pending">Pending</option>
              <option value="approved">Approved</option>
              <option value="denied">Denied</option>
              <option value="expired">Expired</option>
              <option value="all">All</option>
            </select>
          </div>

          {approvalsQuery.isLoading && (
            <div className="flex items-center justify-center h-40">
              <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
            </div>
          )}

          {!approvalsQuery.isLoading && approvalsList.length === 0 && (
            <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              No approvals match the current filter.
            </div>
          )}

          {approvalsList.length > 0 && (
            <div className="space-y-2">
              {approvalsList.map((approval) => {
                const isSelected = approval.id === selectedApprovalId;
                const isPending = approval.status === "pending";
                return (
                  <button
                    key={approval.id}
                    type="button"
                    onClick={() => setSelectedApprovalId(approval.id)}
                    className={`w-full text-left rounded-md border px-3 py-3 transition-colors ${
                      isSelected
                        ? "border-blue-400 bg-blue-50 dark:bg-blue-900/20"
                        : "border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 hover:border-blue-300"
                    }`}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <div
                          className={`text-sm ${
                            isPending
                              ? "font-semibold text-gray-900 dark:text-white"
                              : "text-gray-700 dark:text-gray-200"
                          }`}
                        >
                          {approval.action}
                        </div>
                        <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                          {approval.resource || "No resource"} · {approval.requestor}
                        </div>
                      </div>
                      <div className="flex flex-col items-end gap-1">
                        <span
                          className={`px-2 py-0.5 rounded-full text-xs ${statusBadge(
                            approval.status
                          )}`}
                        >
                          {approval.status}
                        </span>
                        {approval.slb_required && (
                          <span className="text-[10px] uppercase text-orange-500">
                            SLB Required
                          </span>
                        )}
                      </div>
                    </div>
                    <div className="mt-2 text-xs text-gray-500 dark:text-gray-400">
                      {isPending
                        ? `Expires ${formatRelativeTime(approval.expires_at)}`
                        : formatTimestamp(approval.created_at)}
                    </div>
                  </button>
                );
              })}
            </div>
          )}

          {/* Approval Detail */}
          {selectedApproval && (
            <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <h3 className="text-sm font-semibold text-gray-900 dark:text-white">
                    {selectedApproval.action}
                  </h3>
                  <p className="text-xs text-gray-500 dark:text-gray-400">
                    ID: {selectedApproval.id}
                  </p>
                </div>
                <span
                  className={`px-2 py-0.5 rounded-full text-xs ${statusBadge(
                    selectedApproval.status
                  )}`}
                >
                  {selectedApproval.status}
                </span>
              </div>

              <div className="text-xs text-gray-500 dark:text-gray-400 space-y-1">
                <div>Resource: {selectedApproval.resource || "—"}</div>
                <div>Requestor: {selectedApproval.requestor}</div>
                <div>Reason: {selectedApproval.reason || "—"}</div>
                <div>Created: {formatTimestamp(selectedApproval.created_at)}</div>
                <div>Expires: {formatTimestamp(selectedApproval.expires_at)}</div>
                {selectedApproval.approved_by && (
                  <div>
                    {selectedApproval.status === "approved" ? "Approved" : "Denied"} by:{" "}
                    {selectedApproval.approved_by} at{" "}
                    {formatTimestamp(selectedApproval.approved_at)}
                  </div>
                )}
              </div>

              {selectedApproval.slb_required && (
                <div className="p-2 rounded bg-orange-50 dark:bg-orange-900/20 border border-orange-200 dark:border-orange-800 text-xs text-orange-700 dark:text-orange-300">
                  SLB (Simultaneous Launch Button) required: approver cannot be the
                  requestor.
                </div>
              )}

              {selectedApproval.status === "pending" && (
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={handleApprove}
                    disabled={actionBusy.approve}
                    className="flex-1 px-3 py-1.5 rounded-md text-xs font-medium bg-green-600 text-white hover:bg-green-700 disabled:opacity-50"
                  >
                    Approve
                  </button>
                  <button
                    type="button"
                    onClick={handleDeny}
                    disabled={actionBusy.deny}
                    className="flex-1 px-3 py-1.5 rounded-md text-xs font-medium border border-red-200 text-red-600 dark:border-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
                  >
                    Deny
                  </button>
                </div>
              )}
            </div>
          )}
        </section>

        {/* Policy & Blocked Commands */}
        <section className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Policy
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                {policyData?.is_default ? "Using default policy" : "Custom policy"}
              </p>
            </div>
            <button
              type="button"
              onClick={() => setShowPolicyEditor(!showPolicyEditor)}
              className="px-3 py-1.5 rounded-md text-xs font-medium border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700"
            >
              {showPolicyEditor ? "Hide Editor" : "Edit Policy"}
            </button>
          </div>

          {/* Automation Settings */}
          <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-2">
            <div className="text-sm font-semibold text-gray-900 dark:text-white">
              Automation
            </div>
            <div className="grid grid-cols-3 gap-3 text-xs">
              <div>
                <span className="text-gray-500 dark:text-gray-400">Auto Commit</span>
                <div className="font-medium text-gray-900 dark:text-white">
                  {policyData?.automation?.auto_commit ? "Enabled" : "Disabled"}
                </div>
              </div>
              <div>
                <span className="text-gray-500 dark:text-gray-400">Auto Push</span>
                <div className="font-medium text-gray-900 dark:text-white">
                  {policyData?.automation?.auto_push ? "Enabled" : "Disabled"}
                </div>
              </div>
              <div>
                <span className="text-gray-500 dark:text-gray-400">Force Release</span>
                <div className="font-medium text-gray-900 dark:text-white">
                  {policyData?.automation?.force_release || "approval"}
                </div>
              </div>
            </div>
          </div>

          {/* Policy Editor */}
          {showPolicyEditor && (
            <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
              <div className="text-sm font-semibold text-gray-900 dark:text-white">
                Policy YAML Editor
              </div>
              <textarea
                value={policyYaml}
                onChange={(event) => setPolicyYaml(event.target.value)}
                rows={12}
                placeholder="Enter YAML policy content..."
                className="w-full rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm font-mono text-gray-900 dark:text-white"
              />

              {validationResult && (
                <div
                  className={`p-2 rounded text-xs ${
                    validationResult.valid
                      ? "bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 text-green-700 dark:text-green-300"
                      : "bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-red-700 dark:text-red-300"
                  }`}
                >
                  <div className="font-semibold">
                    {validationResult.valid ? "Valid" : "Invalid"}
                  </div>
                  {validationResult.errors?.map((err, idx) => (
                    <div key={idx}>Error: {err}</div>
                  ))}
                  {validationResult.warnings?.map((warn, idx) => (
                    <div key={idx} className="text-yellow-600 dark:text-yellow-400">
                      Warning: {warn}
                    </div>
                  ))}
                </div>
              )}

              <div className="flex flex-wrap gap-2">
                <button
                  type="button"
                  onClick={handleValidatePolicy}
                  disabled={actionBusy.validate || !policyYaml.trim()}
                  className="px-3 py-1.5 rounded-md text-xs font-medium border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
                >
                  Validate
                </button>
                <button
                  type="button"
                  onClick={handleSavePolicy}
                  disabled={actionBusy.save || !policyYaml.trim()}
                  className="px-3 py-1.5 rounded-md text-xs font-medium bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
                >
                  Save Policy
                </button>
                <button
                  type="button"
                  onClick={handleResetPolicy}
                  disabled={actionBusy.reset}
                  className="px-3 py-1.5 rounded-md text-xs font-medium border border-red-200 text-red-600 dark:border-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
                >
                  Reset to Default
                </button>
              </div>
            </div>
          )}

          {/* Policy Rules Summary */}
          {policyData?.rules && !showPolicyEditor && (
            <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
              <div className="text-sm font-semibold text-gray-900 dark:text-white">
                Rules
              </div>

              {(policyData.rules.blocked?.length ?? 0) > 0 && (
                <div>
                  <div className="text-xs font-medium text-red-600 dark:text-red-400 mb-1">
                    Blocked ({policyData.rules.blocked?.length})
                  </div>
                  <div className="space-y-1">
                    {policyData.rules.blocked?.slice(0, 5).map((rule, idx) => (
                      <div
                        key={idx}
                        className="text-xs text-gray-600 dark:text-gray-300 truncate"
                        title={rule.pattern}
                      >
                        <code className="bg-gray-100 dark:bg-gray-700 px-1 rounded">
                          {rule.pattern}
                        </code>
                      </div>
                    ))}
                    {(policyData.rules.blocked?.length ?? 0) > 5 && (
                      <div className="text-xs text-gray-500 dark:text-gray-400">
                        +{(policyData.rules.blocked?.length ?? 0) - 5} more
                      </div>
                    )}
                  </div>
                </div>
              )}

              {(policyData.rules.approval_required?.length ?? 0) > 0 && (
                <div>
                  <div className="text-xs font-medium text-yellow-600 dark:text-yellow-400 mb-1">
                    Approval Required ({policyData.rules.approval_required?.length})
                  </div>
                  <div className="space-y-1">
                    {policyData.rules.approval_required?.slice(0, 5).map((rule, idx) => (
                      <div
                        key={idx}
                        className="text-xs text-gray-600 dark:text-gray-300 truncate"
                        title={rule.pattern}
                      >
                        <code className="bg-gray-100 dark:bg-gray-700 px-1 rounded">
                          {rule.pattern}
                        </code>
                        {rule.slb && (
                          <span className="ml-1 text-orange-500">(SLB)</span>
                        )}
                      </div>
                    ))}
                    {(policyData.rules.approval_required?.length ?? 0) > 5 && (
                      <div className="text-xs text-gray-500 dark:text-gray-400">
                        +{(policyData.rules.approval_required?.length ?? 0) - 5} more
                      </div>
                    )}
                  </div>
                </div>
              )}

              {(policyData.rules.allowed?.length ?? 0) > 0 && (
                <div>
                  <div className="text-xs font-medium text-green-600 dark:text-green-400 mb-1">
                    Allowed ({policyData.rules.allowed?.length})
                  </div>
                  <div className="space-y-1">
                    {policyData.rules.allowed?.slice(0, 3).map((rule, idx) => (
                      <div
                        key={idx}
                        className="text-xs text-gray-600 dark:text-gray-300 truncate"
                        title={rule.pattern}
                      >
                        <code className="bg-gray-100 dark:bg-gray-700 px-1 rounded">
                          {rule.pattern}
                        </code>
                      </div>
                    ))}
                    {(policyData.rules.allowed?.length ?? 0) > 3 && (
                      <div className="text-xs text-gray-500 dark:text-gray-400">
                        +{(policyData.rules.allowed?.length ?? 0) - 3} more
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}

          {/* Recently Blocked */}
          <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
            <div className="text-sm font-semibold text-gray-900 dark:text-white">
              Recently Blocked
            </div>

            {blockedQuery.isLoading && (
              <div className="text-xs text-gray-500 dark:text-gray-400">
                Loading...
              </div>
            )}

            {!blockedQuery.isLoading && blockedList.length === 0 && (
              <div className="text-xs text-gray-500 dark:text-gray-400">
                No blocked commands in the last 24 hours.
              </div>
            )}

            {blockedList.length > 0 && (
              <div className="space-y-2 max-h-48 overflow-y-auto">
                {blockedList.map((entry, idx) => (
                  <div
                    key={idx}
                    className="p-2 rounded bg-red-50 dark:bg-red-900/20 border border-red-100 dark:border-red-800"
                  >
                    <div className="text-xs font-mono text-red-700 dark:text-red-300 truncate">
                      {entry.command}
                    </div>
                    <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {entry.reason} · {formatTimestamp(entry.time)}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
