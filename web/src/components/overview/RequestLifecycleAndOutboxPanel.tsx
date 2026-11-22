"use client";

/**
 * RequestLifecycleAndOutboxPanel
 *
 * Overview operator workbench for:
 *   - Durable Request Identity Lifecycle:
 *       GET  /api/v1/requests/{id}
 *       POST /api/v1/requests/{id}/wait?timeout=5s
 *       POST /api/v1/requests/{id}/cancel
 *   - Durable Project Webhook Outbox Inspection & Recovery:
 *       GET  /api/v1/webhooks/outbox
 *       POST /api/v1/webhooks/outbox/{record}/retry
 *       POST /api/v1/webhooks/outbox/{record}/archive
 */

import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface DurableRequestStatus extends ApiEnvelope {
  request_id?: string;
  id?: string;
  kind?: string;
  status?: string;
  state?: string;
  session?: string;
  terminal?: boolean;
  completed?: boolean;
  canceled?: boolean;
  cancelled?: boolean;
  timed_out?: boolean;
  outcome?: Record<string, unknown> | string;
  error_message?: string;
  updated_at?: string;
}

export interface WebhookOutboxEntry {
  record: string;
  revision: string;
  delivery_id?: string;
  webhook_id?: string;
  session?: string;
  event_type?: string;
  state?: string;
  status?: string;
  blocked_by?: string;
  attempt?: number;
  attempts?: number;
  recoveries?: number;
  last_status?: number;
  last_error?: string;
  retry_allowed?: boolean;
  archive_allowed?: boolean;
  updated_at?: string;
}

export interface WebhookOutboxSnapshot {
  entries?: WebhookOutboxEntry[];
  count?: number;
  failed_count?: number;
  pending_count?: number;
}

export interface WebhookOutboxResponse extends ApiEnvelope {
  action: string;
  project_dir?: string;
  snapshot?: WebhookOutboxSnapshot;
  authorized?: boolean;
  entry?: WebhookOutboxEntry;
}

export function RequestLifecycleAndOutboxPanel() {
  const [requestIdInput, setRequestIdInput] = useState("");
  const [requestSessionInput, setRequestSessionInput] = useState("");
  const [waitTimeout, setWaitTimeout] = useState("5s");
  const [requestResult, setRequestResult] =
    useState<DurableRequestStatus | null>(null);
  const [requestError, setRequestError] = useState<string | null>(null);

  const [outboxSnapshot, setOutboxSnapshot] =
    useState<WebhookOutboxSnapshot | null>(null);
  const [outboxNotice, setOutboxNotice] = useState<string | null>(null);
  const [outboxError, setOutboxError] = useState<string | null>(null);

  const trimmedReqId = requestIdInput.trim();
  const trimmedSession = requestSessionInput.trim();

  const requestActionMutation = useMutation({
    mutationFn: (mode: "inspect" | "wait" | "cancel") => {
      const encoded = encodeURIComponent(trimmedReqId);
      const params = new URLSearchParams();
      if (trimmedSession) {
        params.set("session", trimmedSession);
      }
      if (mode === "inspect") {
        const qs = params.toString();
        return apiFetch<DurableRequestStatus>(
          `/api/v1/requests/${encoded}${qs ? `?${qs}` : ""}`
        );
      }
      if (mode === "wait") {
        const timeoutVal = waitTimeout.trim() || "5s";
        params.set("timeout", timeoutVal);
        return apiFetch<DurableRequestStatus>(
          `/api/v1/requests/${encoded}/wait?${params.toString()}`,
          {
            method: "POST",
            body: JSON.stringify({
              timeout: timeoutVal,
              ...(trimmedSession ? { session: trimmedSession } : {}),
            }),
          }
        );
      }
      const qs = params.toString();
      return apiFetch<DurableRequestStatus>(
        `/api/v1/requests/${encoded}/cancel${qs ? `?${qs}` : ""}`,
        {
          method: "POST",
          body: JSON.stringify(trimmedSession ? { session: trimmedSession } : {}),
        }
      );
    },
    onSuccess: (res) => {
      setRequestResult(res);
      setRequestError(null);
    },
    onError: (err) => {
      setRequestError(formatApiErrorMessage(err));
    },
  });

  const outboxListMutation = useMutation({
    mutationFn: () =>
      apiFetch<WebhookOutboxResponse>("/api/v1/webhooks/outbox"),
    onSuccess: (res) => {
      setOutboxSnapshot(res.snapshot ?? { entries: [], count: 0 });
      setOutboxError(null);
    },
    onError: (err) => {
      setOutboxError(formatApiErrorMessage(err));
    },
  });

  const outboxRecoverMutation = useMutation({
    mutationFn: ({
      action,
      record,
      revision,
    }: {
      action: "retry" | "archive";
      record: string;
      revision: string;
    }) =>
      apiFetch<WebhookOutboxResponse>(
        `/api/v1/webhooks/outbox/${encodeURIComponent(record)}/${action}`,
        {
          method: "POST",
          body: JSON.stringify({ revision }),
        }
      ),
    onSuccess: (_res, vars) => {
      setOutboxNotice(
        `${vars.action === "retry" ? "Authorized retry" : "Archived receipt"} for record "${vars.record}".`
      );
      setOutboxError(null);
      outboxListMutation.mutate();
    },
    onError: (err) => {
      setOutboxError(formatApiErrorMessage(err));
    },
  });

  const handleInspectSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!trimmedReqId) {
      setRequestError(
        "Enter an op-id, Idempotency-Key, or wait handle to inspect."
      );
      return;
    }
    requestActionMutation.mutate("inspect");
  };

  const outboxEntries = outboxSnapshot?.entries ?? [];

  return (
    <section
      aria-label="Durable Request Lifecycle & Webhook Outbox"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-4"
    >
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-5">
        {/* 1. Durable Request Identity Inspector */}
        <form onSubmit={handleInspectSubmit} className="space-y-3">
          <div>
            <h2 className="text-sm font-semibold text-gray-900 dark:text-white">
              Durable Request Lifecycle Inspector
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Inspect, await, or cancel any durable{" "}
              <code className="font-mono">--op-id</code>,{" "}
              <code className="font-mono">Idempotency-Key</code>, or{" "}
              <code className="font-mono">--wait-id</code> handle (
              <code className="font-mono">/api/v1/requests/&#123;id&#125;</code>
              ).
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <input
              aria-label="Durable request ID"
              value={requestIdInput}
              onChange={(e) => setRequestIdInput(e.target.value)}
              placeholder="Request or operation ID (e.g. deploy-42)..."
              className="flex-1 min-w-[180px] rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs text-gray-900 dark:text-white"
            />
            <input
              aria-label="Request session scope"
              value={requestSessionInput}
              onChange={(e) => setRequestSessionInput(e.target.value)}
              placeholder="Session (optional)"
              className="w-36 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            />
            <input
              aria-label="Wait timeout"
              value={waitTimeout}
              onChange={(e) => setWaitTimeout(e.target.value)}
              placeholder="5s"
              className="w-20 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="submit"
              disabled={requestActionMutation.isPending}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              Inspect Request
            </button>
            <button
              type="button"
              disabled={requestActionMutation.isPending}
              onClick={() => {
                if (!trimmedReqId) {
                  setRequestError(
                    "Enter an op-id, Idempotency-Key, or wait handle first."
                  );
                  return;
                }
                requestActionMutation.mutate("wait");
              }}
              className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              Wait Request
            </button>
            <button
              type="button"
              disabled={requestActionMutation.isPending}
              onClick={() => {
                if (!trimmedReqId) {
                  setRequestError(
                    "Enter an op-id, Idempotency-Key, or wait handle first."
                  );
                  return;
                }
                requestActionMutation.mutate("cancel");
              }}
              className="rounded-md bg-rose-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-rose-700 disabled:opacity-50"
            >
              Cancel Request
            </button>
          </div>

          {requestError && (
            <div
              role="alert"
              className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
            >
              {requestError}
            </div>
          )}

          {requestResult && (
            <div
              data-testid="durable-request-result"
              className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/50 p-2.5 text-xs space-y-1"
            >
              <div className="font-mono font-semibold text-gray-900 dark:text-white">
                {requestResult.request_id || requestResult.id || trimmedReqId} —{" "}
                <span className="uppercase">
                  {requestResult.status || requestResult.state || "ok"}
                </span>
              </div>
              <div className="text-gray-600 dark:text-gray-400">
                {requestResult.kind ? `Kind: ${requestResult.kind}` : ""}
                {requestResult.session
                  ? ` · Session: ${requestResult.session}`
                  : ""}
                {requestResult.completed || requestResult.terminal
                  ? " · terminal"
                  : ""}
                {requestResult.canceled || requestResult.cancelled
                  ? " · canceled"
                  : ""}
              </div>
            </div>
          )}
        </form>

        {/* 2. Durable Webhook Outbox Recovery */}
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-2">
            <div>
              <h2 className="text-sm font-semibold text-gray-900 dark:text-white">
                Webhook Outbox Recovery
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                Inspect retained project webhook receipts and authorize
                revision-locked retries or archive failed entries.
              </p>
            </div>
            <button
              type="button"
              disabled={outboxListMutation.isPending}
              onClick={() => outboxListMutation.mutate()}
              className="shrink-0 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-800 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              {outboxListMutation.isPending
                ? "Loading Outbox…"
                : "Inspect Webhook Outbox"}
            </button>
          </div>

          {outboxNotice && (
            <div className="rounded border border-emerald-200 bg-emerald-50 p-2 text-xs text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200">
              {outboxNotice}
            </div>
          )}

          {outboxError && (
            <div
              role="alert"
              className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
            >
              {outboxError}
            </div>
          )}

          {outboxSnapshot && (
            <div
              data-testid="webhook-outbox-snapshot"
              className="rounded border border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-900/50 p-2.5 text-xs space-y-2"
            >
              <div className="font-medium text-gray-800 dark:text-gray-200">
                Retained Receipts: {outboxEntries.length}
              </div>
              {outboxEntries.length === 0 ? (
                <div className="text-gray-500 dark:text-gray-400">
                  Outbox is clean — no retained failed or pending receipts.
                </div>
              ) : (
                <div className="space-y-2">
                  {outboxEntries.map((entry) => {
                    const entryState = entry.state || entry.status || "unknown";
                    const attemptCount = entry.attempt ?? entry.attempts;
                    const retryDisabled =
                      outboxRecoverMutation.isPending ||
                      entry.retry_allowed === false;
                    const archiveDisabled =
                      outboxRecoverMutation.isPending ||
                      entry.archive_allowed === false;
                    return (
                    <div
                      key={entry.record}
                      className="rounded border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-2 flex flex-wrap items-center justify-between gap-2"
                    >
                      <div className="min-w-0">
                        <div className="font-mono font-semibold text-gray-900 dark:text-white truncate">
                          {entry.record} ({entryState})
                        </div>
                        <div className="text-[11px] text-gray-500 dark:text-gray-400">
                          rev {entry.revision}
                          {entry.event_type ? ` · ${entry.event_type}` : ""}
                          {typeof attemptCount === "number"
                            ? ` · attempt ${attemptCount}`
                            : ""}
                          {typeof entry.last_status === "number" &&
                          entry.last_status > 0
                            ? ` · HTTP ${entry.last_status}`
                            : ""}
                          {entry.blocked_by
                            ? ` · blocked: ${entry.blocked_by}`
                            : ""}
                          {entry.last_error ? ` · ${entry.last_error}` : ""}
                        </div>
                      </div>
                      <div className="flex items-center gap-1.5">
                        <button
                          type="button"
                          disabled={retryDisabled}
                          onClick={() =>
                            outboxRecoverMutation.mutate({
                              action: "retry",
                              record: entry.record,
                              revision: entry.revision,
                            })
                          }
                          className="rounded bg-blue-600 px-2 py-1 text-[11px] font-medium text-white hover:bg-blue-700 disabled:opacity-50"
                        >
                          Retry
                        </button>
                        <button
                          type="button"
                          disabled={archiveDisabled}
                          onClick={() =>
                            outboxRecoverMutation.mutate({
                              action: "archive",
                              record: entry.record,
                              revision: entry.revision,
                            })
                          }
                          className="rounded bg-gray-600 px-2 py-1 text-[11px] font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                        >
                          Archive
                        </button>
                      </div>
                    </div>
                    );
                  })}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
