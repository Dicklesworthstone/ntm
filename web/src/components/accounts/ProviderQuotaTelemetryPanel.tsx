"use client";

/**
 * ProviderQuotaTelemetryPanel
 *
 * Surfaces live caut provider token/cost quota, adaptive rate-limit cooldowns,
 * per-provider CAAM seat status, and provider-scoped account rotation:
 *   - GET  /api/v1/quota
 *   - GET  /api/v1/accounts/status
 *   - POST /api/v1/accounts/{provider}/rotate
 */

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface ProviderQuotaEntry {
  usage_percent: number;
  requests_used?: number;
  requests_limit?: number;
  tokens_used?: number;
  tokens_limit?: number;
  cost_usd: number;
  reset_at?: string;
  account_email?: string;
  rate_limited?: boolean;
  status: string;
  rate_limit?: {
    in_cooldown: boolean;
    cooldown_remaining_sec?: number;
    current_delay_ms: number;
    total_rate_limits: number;
    total_successes: number;
  };
}

export interface QuotaEnvelopeResponse extends ApiEnvelope {
  quota: {
    last_updated: string;
    caut_available: boolean;
    providers: Record<string, ProviderQuotaEntry>;
    total_cost_today_usd: number;
    total_cost_month_usd?: number;
    has_warning: boolean;
    has_critical: boolean;
  };
}

interface ProviderAccountStatus {
  current: string;
  usage_percent: number;
  limit_reset?: string;
  available_accounts: number;
  rate_limited: boolean;
}

interface AccountStatusResponse extends ApiEnvelope {
  accounts: Record<string, ProviderAccountStatus>;
}

interface RotateProviderResponse extends ApiEnvelope {
  switch?: {
    success: boolean;
    provider: string;
    previous_account?: string;
    new_account?: string;
    error?: string;
  };
}

export function ProviderQuotaTelemetryPanel({
  onNotice,
}: {
  onNotice?: (next: { type: "success" | "error"; message: string }) => void;
}) {
  const queryClient = useQueryClient();
  const [telemetryRequested, setTelemetryRequested] = useState(false);
  const [rotateReason, setRotateReason] = useState("Rate-limit relief");

  const quotaTelemetryQuery = useQuery({
    queryKey: ["accounts", "caut-quota"],
    queryFn: () => apiFetch<QuotaEnvelopeResponse>("/api/v1/quota"),
    enabled: telemetryRequested,
    retry: false,
  });

  const statusQuery = useQuery({
    queryKey: ["accounts", "provider-status"],
    queryFn: () => apiFetch<AccountStatusResponse>("/api/v1/accounts/status"),
    enabled: telemetryRequested,
    retry: false,
  });

  const rotateProviderMutation = useMutation({
    mutationFn: (provider: string) =>
      apiFetch<RotateProviderResponse>(
        `/api/v1/accounts/${encodeURIComponent(provider)}/rotate`,
        {
          method: "POST",
          body: JSON.stringify({
            reason: rotateReason.trim() || "Manual provider rotation",
          }),
        }
      ),
    onSuccess: (res, provider) => {
      const nextAcct = res.switch?.new_account
        ? ` → ${res.switch.new_account}`
        : "";
      onNotice?.({
        type: "success",
        message: `Rotated ${provider} seat${nextAcct}.`,
      });
      void queryClient.invalidateQueries({ queryKey: ["accounts"] });
    },
    onError: (err) => {
      onNotice?.({
        type: "error",
        message: formatApiErrorMessage(err),
      });
    },
  });

  const providerQuotaEntries = Object.entries(
    quotaTelemetryQuery.data?.quota?.providers ?? {}
  ).sort((a, b) => a[0].localeCompare(b[0]));

  const providerStatuses = statusQuery.data?.accounts ?? {};

  return (
    <section
      aria-label="Live Provider Quota & Fast Seat Rotation"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Live Provider Quota, Rate-Limit Cooldowns &amp; Seat Rotation
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Inspect <code className="font-mono">caut</code> token/spend telemetry (<code className="font-mono">/api/v1/quota</code>), CAAM provider seat availability (<code className="font-mono">/api/v1/accounts/status</code>), and trigger instant provider seat rotation.
          </p>
        </div>

        <button
          type="button"
          disabled={quotaTelemetryQuery.isFetching || statusQuery.isFetching}
          onClick={() => {
            if (telemetryRequested) {
              void quotaTelemetryQuery.refetch();
              void statusQuery.refetch();
            } else {
              setTelemetryRequested(true);
            }
          }}
          className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {quotaTelemetryQuery.isFetching || statusQuery.isFetching
            ? "Loading Quota…"
            : telemetryRequested
              ? "Refresh Quota & Seat Status"
              : "Load Provider Quota & Cooldowns"}
        </button>
      </div>

      {quotaTelemetryQuery.isError && (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          {formatApiErrorMessage(quotaTelemetryQuery.error)}
        </div>
      )}

      {telemetryRequested && quotaTelemetryQuery.data?.quota && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-3 text-xs">
            <span className="rounded bg-gray-100 dark:bg-gray-900 px-2.5 py-1 font-mono text-gray-800 dark:text-gray-200">
              Today: $
              {quotaTelemetryQuery.data.quota.total_cost_today_usd.toFixed(2)}
            </span>
            {typeof quotaTelemetryQuery.data.quota.total_cost_month_usd ===
              "number" && (
              <span className="rounded bg-gray-100 dark:bg-gray-900 px-2.5 py-1 font-mono text-gray-800 dark:text-gray-200">
                Month: $
                {quotaTelemetryQuery.data.quota.total_cost_month_usd.toFixed(2)}
              </span>
            )}
            <span className="rounded bg-indigo-50 dark:bg-indigo-900/30 px-2.5 py-1 font-medium text-indigo-700 dark:text-indigo-300">
              caut:{" "}
              {quotaTelemetryQuery.data.quota.caut_available
                ? "connected"
                : "offline"}
            </span>

            <div className="ml-auto flex items-center gap-2">
              <label htmlFor="provider-rotate-reason" className="sr-only">
                Rotation Reason
              </label>
              <input
                id="provider-rotate-reason"
                aria-label="Rotation Reason"
                value={rotateReason}
                onChange={(e) => setRotateReason(e.target.value)}
                placeholder="Reason for rotation"
                className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1 text-xs text-gray-900 dark:text-white"
              />
            </div>
          </div>

          {providerQuotaEntries.length === 0 ? (
            <p className="text-xs text-gray-500 dark:text-gray-400">
              No provider quota telemetry recorded yet.
            </p>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {providerQuotaEntries.map(([provider, q]) => {
                const seat = providerStatuses[provider];
                return (
                  <div
                    key={provider}
                    data-testid={`provider-quota-card-${provider}`}
                    className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/60 dark:bg-gray-900/40 p-3.5 space-y-2 text-xs"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono font-semibold text-sm text-gray-900 dark:text-white">
                        {provider}
                      </span>
                      <span
                        className={`rounded-full px-2 py-0.5 font-semibold uppercase ${
                          q.status === "critical"
                            ? "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300"
                            : q.status === "warning"
                              ? "bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300"
                              : "bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300"
                        }`}
                      >
                        {q.status}
                      </span>
                    </div>

                    <div className="text-gray-600 dark:text-gray-300 space-y-0.5 font-mono">
                      <div>
                        Usage: {q.usage_percent.toFixed(1)}% · $
                        {q.cost_usd.toFixed(2)}
                      </div>
                      {typeof q.tokens_used === "number" && (
                        <div>
                          Tokens: {q.tokens_used.toLocaleString()}
                          {q.tokens_limit
                            ? ` / ${q.tokens_limit.toLocaleString()}`
                            : ""}
                        </div>
                      )}
                      {seat && (
                        <div>
                          Active: {seat.current || "—"} ({seat.available_accounts}{" "}
                          seat{seat.available_accounts === 1 ? "" : "s"})
                        </div>
                      )}
                      {q.rate_limit && (
                        <div>
                          Pacing: {q.rate_limit.current_delay_ms}ms · 429s:{" "}
                          {q.rate_limit.total_rate_limits}
                        </div>
                      )}
                    </div>

                    <button
                      type="button"
                      disabled={rotateProviderMutation.isPending}
                      onClick={() => rotateProviderMutation.mutate(provider)}
                      className="w-full rounded border border-indigo-300 dark:border-indigo-700 bg-indigo-50 dark:bg-indigo-900/30 px-2.5 py-1 font-medium text-indigo-700 dark:text-indigo-200 hover:bg-indigo-100 dark:hover:bg-indigo-900/50 disabled:opacity-50"
                    >
                      Rotate {provider} Seat
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
