"use client";

/**
 * Connect Page
 *
 * Allows users to:
 * - Enter NTM server URL
 * - Optionally provide auth token
 * - Test connection before proceeding
 */

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import {
  DEFAULT_NTM_BASE_URL,
  getBrowserOriginBaseUrl,
  saveConnectionConfig,
  getConnectionConfig,
  checkConnection,
  resetClient,
} from "@/lib/api/client";
import { resetWsClient } from "@/lib/ws/client";
import { resetQueryClient } from "@/lib/hooks/use-query";

export default function ConnectPage() {
  const router = useRouter();

  // Keep initial render deterministic across SSR and hydration; hydrate saved
  // config or live window.location.origin post-mount so embedded `ntm serve --web`
  // on custom ports auto-fills its own origin without hydration warnings.
  const [baseUrl, setBaseUrl] = useState(DEFAULT_NTM_BASE_URL);
  const [authToken, setAuthToken] = useState("");
  const [isConnecting, setIsConnecting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const existingConfig = getConnectionConfig();
    if (existingConfig?.baseUrl) {
      setBaseUrl(existingConfig.baseUrl);
      setAuthToken(existingConfig.authToken || "");
      return;
    }
    const browserOrigin = getBrowserOriginBaseUrl();
    if (browserOrigin) {
      setBaseUrl(browserOrigin);
    }
  }, []);

  const handleConnect = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setIsConnecting(true);

    // Save config first so checkConnection uses it
    saveConnectionConfig({ baseUrl, authToken: authToken || undefined });
    resetClient();
    resetWsClient();
    resetQueryClient();

    // Test connection
    const result = await checkConnection();

    if (result.ok) {
      // Connection successful, redirect to dashboard
      router.push("/");
    } else {
      setError(result.error || "Connection failed");
    }

    setIsConnecting(false);
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50 dark:bg-gray-900 px-4">
      <div className="max-w-md w-full space-y-8">
        <div className="text-center">
          <h1 className="text-3xl font-bold text-gray-900 dark:text-white">
            NTM
          </h1>
          <p className="mt-2 text-gray-600 dark:text-gray-400">
            Named Tmux Manager
          </p>
        </div>

        <form onSubmit={handleConnect} className="mt-8 space-y-6">
          <div className="space-y-4">
            <div>
              <label
                htmlFor="baseUrl"
                className="block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                Server URL
              </label>
              <input
                id="baseUrl"
                name="baseUrl"
                type="url"
                required
                value={baseUrl}
                onChange={(e) => setBaseUrl(e.target.value)}
                className="mt-1 block w-full px-3 py-2 border border-gray-300 dark:border-gray-700 rounded-md shadow-sm
                         bg-white dark:bg-gray-800 text-gray-900 dark:text-white
                         placeholder-gray-400 dark:placeholder-gray-500
                         focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                placeholder={DEFAULT_NTM_BASE_URL}
              />
            </div>

            <div>
              <label
                htmlFor="authToken"
                className="block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                Auth Token{" "}
                <span className="text-gray-400 dark:text-gray-500">
                  (optional)
                </span>
              </label>
              <input
                id="authToken"
                name="authToken"
                type="password"
                value={authToken}
                onChange={(e) => setAuthToken(e.target.value)}
                className="mt-1 block w-full px-3 py-2 border border-gray-300 dark:border-gray-700 rounded-md shadow-sm
                         bg-white dark:bg-gray-800 text-gray-900 dark:text-white
                         placeholder-gray-400 dark:placeholder-gray-500
                         focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                placeholder="Bearer token or API key"
              />
              <p className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                Leave blank for local mode without authentication
              </p>
            </div>
          </div>

          {error && (
            <div className="rounded-md bg-red-50 dark:bg-red-900/20 p-4">
              <p className="text-sm text-red-700 dark:text-red-400">{error}</p>
            </div>
          )}

          <button
            type="submit"
            disabled={isConnecting}
            className="w-full flex justify-center py-2 px-4 border border-transparent rounded-md shadow-sm
                     text-sm font-medium text-white bg-blue-600 hover:bg-blue-700
                     focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-blue-500
                     disabled:opacity-50 disabled:cursor-not-allowed
                     dark:focus:ring-offset-gray-900"
          >
            {isConnecting ? (
              <span className="flex items-center">
                <svg
                  className="animate-spin -ml-1 mr-2 h-4 w-4 text-white"
                  fill="none"
                  viewBox="0 0 24 24"
                >
                  <circle
                    className="opacity-25"
                    cx="12"
                    cy="12"
                    r="10"
                    stroke="currentColor"
                    strokeWidth="4"
                  />
                  <path
                    className="opacity-75"
                    fill="currentColor"
                    d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                  />
                </svg>
                Connecting...
              </span>
            ) : (
              "Connect"
            )}
          </button>
        </form>

        <div className="text-center text-xs text-gray-500 dark:text-gray-400">
          <p>
            Make sure the NTM server is running with{" "}
            <code className="bg-gray-100 dark:bg-gray-800 px-1 rounded">
              ntm serve
            </code>
          </p>
        </div>
      </div>
    </div>
  );
}
