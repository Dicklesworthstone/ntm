"use client";

/**
 * Accounts Page
 *
 * CAAM account management with quota visualization and rotation.
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

interface AccountInfo {
  provider: string;
  name: string;
  current: boolean;
  index?: number;
  email?: string;
  tier?: string;
}

interface AccountsListResponse extends ApiEnvelope {
  accounts: AccountInfo[];
}

interface QuotaInfo {
  provider: string;
  current: string;
  usage_percent: number;
  limit_reset?: string;
  available_accounts: number;
  rate_limited: boolean;
}

interface QuotaResponse extends ApiEnvelope {
  quotas: QuotaInfo[];
}

interface ActiveAccountsResponse extends ApiEnvelope {
  active: Record<string, AccountInfo>;
}

interface AutoRotateConfig {
  auto_rotate_enabled: boolean;
  auto_rotate_cooldown_seconds: number;
  auto_rotate_on_rate_limit: boolean;
}

interface AutoRotateConfigResponse extends ApiEnvelope {
  config: AutoRotateConfig;
}

interface RotationEvent {
  timestamp: string;
  provider: string;
  previous_account?: string;
  new_account?: string;
  reason?: string;
  automatic: boolean;
  success: boolean;
  error?: string;
}

interface HistoryResponse extends ApiEnvelope {
  history: RotationEvent[];
  total: number;
}

type Notice = { type: "success" | "error"; message: string };

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

  if (diffMins <= 0) return "now";
  if (diffMins < 60) return `in ${diffMins}m`;
  const hours = Math.floor(diffMins / 60);
  if (hours < 24) return `in ${hours}h`;
  const days = Math.floor(hours / 24);
  return `in ${days}d`;
}

function usageColor(percent: number): string {
  if (percent >= 90) return "text-red-600 dark:text-red-400";
  if (percent >= 70) return "text-orange-600 dark:text-orange-400";
  return "text-green-600 dark:text-green-400";
}

function usageRingColor(percent: number): string {
  if (percent >= 90) return "stroke-red-500";
  if (percent >= 70) return "stroke-orange-500";
  return "stroke-green-500";
}

// Circular progress ring component
function QuotaRing({ percent, size = 80 }: { percent: number; size?: number }) {
  const strokeWidth = 8;
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (percent / 100) * circumference;

  return (
    <svg width={size} height={size} className="transform -rotate-90">
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        strokeWidth={strokeWidth}
        className="stroke-gray-200 dark:stroke-gray-700"
      />
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeDasharray={circumference}
        strokeDashoffset={offset}
        className={`transition-all duration-500 ${usageRingColor(percent)}`}
      />
    </svg>
  );
}

export default function AccountsPage() {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState<Notice | null>(null);
  const [selectedProvider, setSelectedProvider] = useState<string | null>(null);
  const noticeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [actionBusy, setActionBusy] = useState({
    rotate: false,
    config: false,
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

  // Accounts list query
  const accountsQuery = useQuery({
    queryKey: ["accounts"],
    queryFn: () => apiFetch<AccountsListResponse>("/api/v1/accounts"),
    refetchInterval: 10000,
  });

  // Quota query
  const quotaQuery = useQuery({
    queryKey: ["accounts", "quota"],
    queryFn: () => apiFetch<QuotaResponse>("/api/v1/accounts/quota"),
    refetchInterval: 10000,
  });

  // Active accounts query
  const activeQuery = useQuery({
    queryKey: ["accounts", "active"],
    queryFn: () => apiFetch<ActiveAccountsResponse>("/api/v1/accounts/active"),
    refetchInterval: 10000,
  });

  // Auto-rotate config query
  const configQuery = useQuery({
    queryKey: ["accounts", "config"],
    queryFn: () => apiFetch<AutoRotateConfigResponse>("/api/v1/accounts/auto-rotate"),
    refetchInterval: 30000,
  });

  // History query
  const historyQuery = useQuery({
    queryKey: ["accounts", "history"],
    queryFn: () => apiFetch<HistoryResponse>("/api/v1/accounts/history?limit=20"),
    refetchInterval: 15000,
  });

  const accountsList = useMemo(() => accountsQuery.data?.accounts ?? [], [accountsQuery.data?.accounts]);
  const quotasList = quotaQuery.data?.quotas ?? [];
  const activeAccounts = activeQuery.data?.active ?? {};
  const autoRotateConfig = configQuery.data?.config;
  const historyList = historyQuery.data?.history ?? [];

  // Group accounts by provider
  const accountsByProvider = useMemo(() => {
    const grouped = new Map<string, AccountInfo[]>();
    for (const account of accountsList) {
      const list = grouped.get(account.provider) ?? [];
      list.push(account);
      grouped.set(account.provider, list);
    }
    return grouped;
  }, [accountsList]);

  const providers = useMemo(() => {
    return Array.from(accountsByProvider.keys()).sort();
  }, [accountsByProvider]);

  useEffect(() => {
    if (providers.length === 0) {
      if (selectedProvider !== null) {
        setSelectedProvider(null);
      }
      return;
    }

    if (selectedProvider === null || !providers.includes(selectedProvider)) {
      setSelectedProvider(providers[0]);
    }
  }, [providers, selectedProvider]);

  const handleRotate = useCallback(
    async (provider: string, accountId?: string) => {
      setActionBusy((prev) => ({ ...prev, rotate: true }));
      try {
        if (process.env.NODE_ENV === "development") {
          console.log("[Accounts] Rotate", { provider, accountId });
        }
        await apiFetch<ApiEnvelope>("/api/v1/accounts/rotate", {
          method: "POST",
          body: JSON.stringify({
            provider,
            account_id: accountId,
            reason: "Manual rotation from UI",
          }),
        });
        queryClient.invalidateQueries({ queryKey: ["accounts"] });
        setStatusNotice({ type: "success", message: `Rotated ${provider} account.` });
      } catch (error) {
        setStatusNotice({ type: "error", message: getErrorMessage(error) });
      } finally {
        setActionBusy((prev) => ({ ...prev, rotate: false }));
      }
    },
    [queryClient, setStatusNotice]
  );

  const handleToggleAutoRotate = useCallback(async () => {
    if (!autoRotateConfig) return;

    setActionBusy((prev) => ({ ...prev, config: true }));
    try {
      await apiFetch<ApiEnvelope>("/api/v1/accounts/auto-rotate", {
        method: "PATCH",
        body: JSON.stringify({
          auto_rotate_enabled: !autoRotateConfig.auto_rotate_enabled,
        }),
      });
      queryClient.invalidateQueries({ queryKey: ["accounts", "config"] });
      setStatusNotice({
        type: "success",
        message: autoRotateConfig.auto_rotate_enabled
          ? "Auto-rotate disabled."
          : "Auto-rotate enabled.",
      });
    } catch (error) {
      setStatusNotice({ type: "error", message: getErrorMessage(error) });
    } finally {
      setActionBusy((prev) => ({ ...prev, config: false }));
    }
  }, [autoRotateConfig, queryClient, setStatusNotice]);

  const connectionError =
    accountsQuery.error ??
    quotaQuery.error ??
    activeQuery.error ??
    configQuery.error ??
    historyQuery.error;

  return (
    <div className="space-y-8">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Accounts
          </h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            CAAM account management and quota monitoring.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            onClick={handleToggleAutoRotate}
            disabled={actionBusy.config || !autoRotateConfig}
            className={`px-3 py-1.5 rounded-md text-xs font-medium border ${
              autoRotateConfig?.auto_rotate_enabled
                ? "border-green-200 text-green-600 dark:border-green-700 dark:text-green-300 hover:bg-green-50 dark:hover:bg-green-900/20"
                : "border-gray-200 text-gray-600 dark:border-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700"
            } disabled:opacity-50`}
          >
            Auto-rotate: {autoRotateConfig?.auto_rotate_enabled ? "On" : "Off"}
          </button>
        </div>
      </div>

      {connectionError && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-md">
          <p className="text-sm text-red-700 dark:text-red-400">
            Accounts error: {getErrorMessage(connectionError)}
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

      {/* Active Account Cards */}
      <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {quotasList.map((quota) => {
          const active = activeAccounts[quota.provider];
          return (
            <div
              key={quota.provider}
              className={`rounded-md border p-4 ${
                quota.rate_limited
                  ? "border-red-300 bg-red-50 dark:border-red-700 dark:bg-red-900/20"
                  : "border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800"
              }`}
            >
              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
                    {quota.provider}
                  </div>
                  <div className="mt-1 text-lg font-semibold text-gray-900 dark:text-white">
                    {active?.name || quota.current || "No account"}
                  </div>
                  {active?.email && (
                    <div className="text-xs text-gray-500 dark:text-gray-400">
                      {active.email}
                    </div>
                  )}
                </div>
                <div className="relative flex items-center justify-center">
                  <QuotaRing percent={quota.usage_percent} />
                  <div
                    className={`absolute text-lg font-bold ${usageColor(
                      quota.usage_percent
                    )}`}
                  >
                    {quota.usage_percent}%
                  </div>
                </div>
              </div>

              <div className="mt-3 text-xs text-gray-500 dark:text-gray-400 space-y-1">
                {quota.limit_reset && (
                  <div>Resets: {formatRelativeTime(quota.limit_reset)}</div>
                )}
                <div>Available: {quota.available_accounts} accounts</div>
                {quota.rate_limited && (
                  <div className="text-red-600 dark:text-red-400 font-medium">
                    Rate limited
                  </div>
                )}
              </div>

              <div className="mt-3">
                <button
                  type="button"
                  onClick={() => handleRotate(quota.provider)}
                  disabled={actionBusy.rotate || quota.available_accounts < 2}
                  className="w-full px-3 py-1.5 rounded-md text-xs font-medium border border-gray-200 dark:border-gray-700 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
                >
                  Rotate Account
                </button>
              </div>
            </div>
          );
        })}

        {quotasList.length === 0 && !quotaQuery.isLoading && (
          <div className="col-span-full p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
            No accounts configured. CAAM is not available.
          </div>
        )}

        {quotaQuery.isLoading && (
          <div className="col-span-full flex items-center justify-center h-40">
            <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
          </div>
        )}
      </section>

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Account List by Provider */}
        <section className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
                Accounts
              </h2>
              <p className="text-xs text-gray-500 dark:text-gray-400">
                All configured accounts by provider.
              </p>
            </div>
            {providers.length > 0 && (
              <select
                value={selectedProvider ?? ""}
                onChange={(e) => setSelectedProvider(e.target.value)}
                className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 py-1 text-xs text-gray-700 dark:text-gray-200"
              >
                {providers.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            )}
          </div>

          {selectedProvider && (
            <div className="space-y-2">
              {(accountsByProvider.get(selectedProvider) ?? []).map((account) => (
                <div
                  key={`${account.provider}-${account.name}`}
                  className={`rounded-md border px-3 py-3 ${
                    account.current
                      ? "border-green-300 bg-green-50 dark:border-green-700 dark:bg-green-900/20"
                      : "border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800"
                  }`}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <div className="text-sm font-medium text-gray-900 dark:text-white">
                        {account.name}
                        {account.current && (
                          <span className="ml-2 px-2 py-0.5 rounded-full text-xs bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300">
                            Active
                          </span>
                        )}
                      </div>
                      {account.email && (
                        <div className="text-xs text-gray-500 dark:text-gray-400">
                          {account.email}
                        </div>
                      )}
                      {account.tier && (
                        <div className="text-xs text-gray-500 dark:text-gray-400">
                          Tier: {account.tier}
                        </div>
                      )}
                    </div>
                    {!account.current && (
                      <button
                        type="button"
                        onClick={() => handleRotate(account.provider, account.name)}
                        disabled={actionBusy.rotate}
                        className="px-2 py-1 rounded text-xs font-medium text-blue-600 dark:text-blue-400 hover:bg-blue-50 dark:hover:bg-blue-900/20 disabled:opacity-50"
                      >
                        Activate
                      </button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}

          {!selectedProvider && !accountsQuery.isLoading && (
            <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              No accounts available.
            </div>
          )}
        </section>

        {/* Rotation History */}
        <section className="space-y-4">
          <div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
              Rotation History
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Recent account rotations.
            </p>
          </div>

          {historyQuery.isLoading && (
            <div className="flex items-center justify-center h-40">
              <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
            </div>
          )}

          {!historyQuery.isLoading && historyList.length === 0 && (
            <div className="p-4 rounded-md border border-gray-200 dark:border-gray-700 text-sm text-gray-500 dark:text-gray-400">
              No rotation history yet.
            </div>
          )}

          {historyList.length > 0 && (
            <div className="space-y-2 max-h-[400px] overflow-y-auto">
              {historyList.map((event, idx) => (
                <div
                  key={idx}
                  className={`rounded-md border px-3 py-2 ${
                    event.success
                      ? "border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800"
                      : "border-red-200 bg-red-50 dark:border-red-700 dark:bg-red-900/20"
                  }`}
                >
                  <div className="flex items-start justify-between gap-2">
                    <div>
                      <div className="text-xs font-medium text-gray-900 dark:text-white">
                        {event.provider}
                        {event.automatic && (
                          <span className="ml-1 text-gray-500 dark:text-gray-400">
                            (auto)
                          </span>
                        )}
                      </div>
                      <div className="text-xs text-gray-500 dark:text-gray-400">
                        {event.previous_account} → {event.new_account}
                      </div>
                    </div>
                    <div className="text-xs text-gray-500 dark:text-gray-400">
                      {formatTimestamp(event.timestamp)}
                    </div>
                  </div>
                  {event.reason && (
                    <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {event.reason}
                    </div>
                  )}
                  {event.error && (
                    <div className="mt-1 text-xs text-red-600 dark:text-red-400">
                      {event.error}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}

          {/* Auto-rotate Config */}
          {autoRotateConfig && (
            <div className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-2">
              <div className="text-sm font-semibold text-gray-900 dark:text-white">
                Auto-rotate Settings
              </div>
              <div className="text-xs text-gray-500 dark:text-gray-400 space-y-1">
                <div>
                  Status:{" "}
                  <span
                    className={
                      autoRotateConfig.auto_rotate_enabled
                        ? "text-green-600 dark:text-green-400"
                        : "text-gray-600 dark:text-gray-300"
                    }
                  >
                    {autoRotateConfig.auto_rotate_enabled ? "Enabled" : "Disabled"}
                  </span>
                </div>
                <div>
                  Cooldown: {Math.floor(autoRotateConfig.auto_rotate_cooldown_seconds / 60)}{" "}
                  minutes
                </div>
                <div>
                  On rate limit:{" "}
                  {autoRotateConfig.auto_rotate_on_rate_limit ? "Yes" : "No"}
                </div>
              </div>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
