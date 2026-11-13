/**
 * NTM API Client
 *
 * Type-safe REST client generated from OpenAPI spec using openapi-fetch.
 * Supports authentication and automatic error handling.
 */

import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

export type { components, paths };

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

export interface ApiEnvelope {
  success?: boolean;
  timestamp?: string;
  request_id?: string;
  error?: string;
  error_code?: string;
  message?: string;
}

/**
 * Browser navigation adapter used for 401 redirects so tests can observe or
 * stub navigation cleanly without JSDOM Location descriptor issues.
 */
export const browserNavigation = {
  getPathname(): string {
    if (typeof window === "undefined") return "";
    return window.location?.pathname ?? "";
  },
  redirect(path: string): void {
    if (typeof window === "undefined") return;
    if (typeof window.location?.assign === "function") {
      window.location.assign(path);
    } else if (window.location) {
      window.location.href = path;
    }
  },
};

/**
 * Redirect cleanly to /connect when a 401 Unauthorized response is received in
 * the browser, unless already on /connect (preventing infinite reload loops).
 */
export function handleUnauthorizedRedirect(): boolean {
  if (typeof window === "undefined") {
    return false;
  }
  const pathname = browserNavigation.getPathname();
  if (pathname.startsWith("/connect")) {
    return false;
  }
  browserNavigation.redirect("/connect");
  return true;
}

/**
 * Create the NTM API client with current connection config.
 */
export function createNtmClient() {
  const authHeaders = getAuthHeaders();

  const client = createClient<paths>({
    baseUrl: getBaseUrl(),
    headers: Object.keys(authHeaders).length > 0 ? authHeaders : undefined,
    fetch: (input: Request) =>
      fetch(input, {
        signal: input.signal ?? apiRequestSignal(),
      }),
  });

  client.use({
    async onResponse({ response }) {
      if (response.status === 401) {
        handleUnauthorizedRedirect();
      }
      return response;
    },
  });

  return client;
}

/**
 * Shared JSON envelope fetch helper with Bearer auth, timeout signal, and
 * loop-safe /connect redirect on 401 Unauthorized.
 */
export async function apiFetch<T>(
  path: string,
  options: RequestInit = {}
): Promise<T> {
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

  if (response.status === 401) {
    handleUnauthorizedRedirect();
  }

  const raw = await response.text();
  let data: unknown = null;
  if (raw) {
    try {
      data = JSON.parse(raw);
    } catch {
      throw new ApiError(
        response.status,
        "INVALID_JSON",
        "Invalid response from server."
      );
    }
  }

  const envelope = data as ApiEnvelope | null;
  if (!response.ok || !envelope || envelope.success === false) {
    const message =
      envelope?.error ||
      envelope?.message ||
      `Request failed (${response.status})`;
    throw new ApiError(
      response.status,
      envelope?.error_code || `HTTP_${response.status}`,
      message,
      data
    );
  }

  return data as T;
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

const KERNEL_PATH_PARAM_REGEX = /\{([a-zA-Z0-9_]+)\}/g;

/**
 * Extract unique `{param}` placeholder names from a kernel REST path template
 * in order of appearance (e.g. `/sessions/{sessionId}/panes/{paneIdx}` ->
 * `["sessionId", "paneIdx"]`).
 */
export function extractKernelPathParams(pathTemplate: string): string[] {
  if (!pathTemplate) return [];
  const seen = new Set<string>();
  const params: string[] = [];
  for (const match of pathTemplate.matchAll(KERNEL_PATH_PARAM_REGEX)) {
    const name = match[1];
    if (name && !seen.has(name)) {
      seen.add(name);
      params.push(name);
    }
  }
  return params;
}

/**
 * Normalize a kernel `RESTBinding.Path` into a full `/api/...` request path and
 * substitute `{param}` segments using `encodeURIComponent`.
 *
 * Kernel command paths are registered relative to `/api/v1` (e.g. `/sessions`,
 * `/deps`, `/sessions/{sessionId}/events`), while paths already starting with
 * `/api/` (e.g. `/api/v1/sessions`, `/api/kernel/commands`) are preserved.
 */
export function resolveKernelRESTPath(
  rawPath: string,
  params: Record<string, string> = {}
): string {
  const trimmed = rawPath.trim();
  if (!trimmed) {
    throw new Error("Command REST path is empty.");
  }

  const withLeadingSlash = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  const normalizedTemplate =
    withLeadingSlash === "/api" || withLeadingSlash.startsWith("/api/")
      ? withLeadingSlash
      : withLeadingSlash === "/"
        ? "/api/v1"
        : `/api/v1${withLeadingSlash}`;

  return normalizedTemplate.replace(
    KERNEL_PATH_PARAM_REGEX,
    (_placeholder, paramName: string) => {
      const value = params[paramName];
      if (value === undefined || value === null || value.trim() === "") {
        throw new Error(`Missing required parameter: ${paramName}`);
      }
      return encodeURIComponent(value.trim());
    }
  );
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
