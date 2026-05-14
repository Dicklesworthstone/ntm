/**
 * NTM API Client
 *
 * Type-safe REST client generated from OpenAPI spec using openapi-fetch.
 * Supports authentication and automatic error handling.
 */

import createClient from "openapi-fetch";
import type { paths } from "./schema";

// Connection configuration stored in localStorage
export interface ConnectionConfig {
  baseUrl: string;
  authToken?: string;
}

const CONNECTION_KEY = "ntm-connection";
export const DEFAULT_NTM_BASE_URL = "http://localhost:7337";
export const DEFAULT_API_TIMEOUT_MS = 15_000;

export function apiRequestSignal(timeoutMs = DEFAULT_API_TIMEOUT_MS): AbortSignal | undefined {
  if (typeof AbortSignal === "undefined" || typeof AbortSignal.timeout !== "function") {
    return undefined;
  }
  return AbortSignal.timeout(timeoutMs);
}

export function formatApiErrorMessage(error: unknown): string {
  if (typeof error === "string") {
    return error;
  }
  if (error instanceof Error) {
    return error.message;
  }
  if (error && typeof error === "object") {
    const record = error as Record<string, unknown>;
    const messageFields = ["error", "message", "detail", "title"];
    for (const field of messageFields) {
      const value = record[field];
      if (typeof value === "string" && value.trim()) {
        return value;
      }
    }
    try {
      return JSON.stringify(error);
    } catch {
      return "Unexpected error";
    }
  }
  return "Unexpected error";
}

function normalizeBaseUrl(baseUrl: string): string {
  const trimmed = baseUrl.trim();
  if (!trimmed) {
    return DEFAULT_NTM_BASE_URL;
  }
  const normalized = trimmed.replace(/\/+$/, "");
  return normalized || DEFAULT_NTM_BASE_URL;
}

/**
 * Get the current connection config from localStorage.
 */
export function getConnectionConfig(): ConnectionConfig | null {
  if (typeof window === "undefined") return null;
  const stored = localStorage.getItem(CONNECTION_KEY);
  if (!stored) return null;
  try {
    const parsed = JSON.parse(stored) as ConnectionConfig;
    if (!parsed?.baseUrl) {
      return null;
    }
    return {
      ...parsed,
      baseUrl: normalizeBaseUrl(parsed.baseUrl),
    };
  } catch {
    return null;
  }
}

/**
 * Resolve the active API base URL from saved config or environment.
 */
export function getBaseUrl(): string {
  const config = getConnectionConfig();
  return normalizeBaseUrl(
    config?.baseUrl || process.env.NEXT_PUBLIC_NTM_URL || DEFAULT_NTM_BASE_URL
  );
}

/**
 * Build auth headers for the saved connection config.
 */
export function getAuthHeaders(): Record<string, string> {
  const config = getConnectionConfig();
  if (!config?.authToken) return {};
  return { Authorization: `Bearer ${config.authToken}` };
}

/**
 * Save connection config to localStorage.
 */
export function saveConnectionConfig(config: ConnectionConfig): void {
  if (typeof window === "undefined") return;
  localStorage.setItem(
    CONNECTION_KEY,
    JSON.stringify({
      ...config,
      baseUrl: normalizeBaseUrl(config.baseUrl),
    })
  );
}

/**
 * Clear connection config from localStorage.
 */
export function clearConnectionConfig(): void {
  if (typeof window === "undefined") return;
  localStorage.removeItem(CONNECTION_KEY);
}

/**
 * Create the NTM API client with current connection config.
 */
export function createNtmClient() {
  const authHeaders = getAuthHeaders();

  const client = createClient<paths>({
    baseUrl: getBaseUrl(),
    headers: Object.keys(authHeaders).length > 0 ? authHeaders : undefined,
  });

  return client;
}

/**
 * Singleton client instance.
 * Re-created when connection config changes.
 */
let clientInstance: ReturnType<typeof createNtmClient> | null = null;

/**
 * Get the singleton API client.
 * Call resetClient() when connection config changes.
 */
export function getClient() {
  if (!clientInstance) {
    clientInstance = createNtmClient();
  }
  return clientInstance;
}

/**
 * Reset the client instance (call when connection config changes).
 */
export function resetClient(): void {
  clientInstance = null;
}

/**
 * API error with typed response.
 */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: unknown
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/**
 * Check if the server is reachable and authentication is valid.
 */
export async function checkConnection(): Promise<{ ok: boolean; error?: string }> {
  try {
    const client = getClient();
    const response = await client.GET("/api/v1/health");
    const responseError = (response as { error?: unknown }).error;

    if (responseError) {
      return {
        ok: false,
        error: `Server error: ${formatApiErrorMessage(responseError)}`,
      };
    }

    return { ok: true };
  } catch (err) {
    const message = formatApiErrorMessage(err);
    return { ok: false, error: message };
  }
}
