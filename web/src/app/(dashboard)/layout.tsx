"use client";

/**
 * Dashboard Layout
 *
 * Wraps authenticated pages with:
 * - TanStack Query provider
 * - WebSocket connection
 * - Navigation
 * - Connection status indicator
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import {
  apiRequestSignal,
  getAuthHeaders,
  getBaseUrl,
  getConnectionConfig,
} from "@/lib/api/client";
import { NtmQueryProvider, useConnection } from "@/lib/hooks/use-query";
import { NavBar } from "@/components/layout/nav-bar";

interface DashboardLayoutProps {
  children: ReactNode;
}

function DashboardContent({ children }: DashboardLayoutProps) {
  const { wsState, isConnected } = useConnection();
  const [paletteOpen, setPaletteOpen] = useState(false);

  return (
    <div className="min-h-screen flex flex-col bg-gray-50 dark:bg-gray-900">
      <NavBar wsState={wsState} />
      <main className="flex-1 p-4 sm:p-6 lg:p-8">
        {!isConnected && wsState === "reconnecting" && (
          <div className="mb-4 p-3 bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 rounded-md">
            <p className="text-sm text-yellow-700 dark:text-yellow-400">
              Reconnecting to server...
            </p>
          </div>
        )}
        {children}
      </main>
      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        isConnected={isConnected}
      />
    </div>
  );
}

export default function DashboardLayout({ children }: DashboardLayoutProps) {
  const router = useRouter();
  const [isChecking, setIsChecking] = useState(true);

  useEffect(() => {
    // Check if we have connection config
    const config = getConnectionConfig();
    if (!config) {
      router.replace("/connect");
      return;
    }
    setIsChecking(false);
  }, [router]);

  if (isChecking) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-gray-50 dark:bg-gray-900">
        <div className="animate-spin h-8 w-8 border-4 border-blue-500 border-t-transparent rounded-full" />
      </div>
    );
  }

  return (
    <NtmQueryProvider>
      <DashboardContent>{children}</DashboardContent>
    </NtmQueryProvider>
  );
}

interface ApiEnvelope {
  success: boolean;
  timestamp: string;
  request_id?: string;
  error?: string;
  error_code?: string;
}

interface KernelCommand {
  name: string;
  description: string;
  category?: string;
  rest?: { method?: string; path?: string };
  safety_level?: string;
  idempotent?: boolean;
}

interface KernelListResponse extends ApiEnvelope {
  commands: KernelCommand[];
  count?: number;
}

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

function CommandPalette({
  open,
  onOpenChange,
  isConnected,
}: {
  open: boolean;
  onOpenChange: (next: boolean) => void;
  isConnected: boolean;
}) {
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [result, setResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        onOpenChange(!open);
      }
      if (event.key === "Escape") {
        onOpenChange(false);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, onOpenChange]);

  useEffect(() => {
    const handleGlobal = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        onOpenChange(true);
      }
    };
    window.addEventListener("keydown", handleGlobal);
    return () => window.removeEventListener("keydown", handleGlobal);
  }, [onOpenChange]);

  const commandsQuery = useQuery({
    queryKey: ["kernel-commands"],
    queryFn: () => apiFetch<KernelListResponse>("/api/kernel/commands"),
    enabled: open,
  });

  const commands = useMemo(() => commandsQuery.data?.commands ?? [], [commandsQuery.data?.commands]);
  const filtered = useMemo(() => {
    if (!query) return commands;
    const q = query.toLowerCase();
    return commands.filter((cmd) => {
      return (
        cmd.name.toLowerCase().includes(q) ||
        cmd.description.toLowerCase().includes(q) ||
        (cmd.category || "").toLowerCase().includes(q)
      );
    });
  }, [commands, query]);

  useEffect(() => {
    setSelectedIndex(0);
  }, [query, open]);

  const runCommand = async (command: KernelCommand) => {
    setError(null);
    setResult(null);
    if (!command.rest?.path || !command.rest?.method) {
      setError("Command has no REST binding.");
      return;
    }

    if (command.rest.method.toUpperCase() !== "GET") {
      setError("This command requires input; only GET commands are supported here.");
      return;
    }

    try {
      if (process.env.NODE_ENV === "development") {
        console.log("[Command Palette] run", command.name, command.rest);
      }
      const response = await apiFetch<Record<string, unknown>>(command.rest.path);
      setResult(JSON.stringify(response, null, 2));
    } catch (err) {
      if (process.env.NODE_ENV === "development") {
        console.error("[Command Palette] error", err);
      }
      setError(getErrorMessage(err));
    }
  };

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 p-4"
      onClick={() => onOpenChange(false)}
    >
      <div
        className="w-full max-w-2xl rounded-lg border border-gray-200 bg-white shadow-xl dark:border-gray-700 dark:bg-gray-900"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-gray-700">
          <div>
            <div className="text-sm font-semibold text-gray-900 dark:text-white">
              Command Palette
            </div>
            <div className="text-xs text-gray-500 dark:text-gray-400">
              {isConnected ? "Connected" : "Offline"} · Kernel commands via REST
            </div>
          </div>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-xs text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"
          >
            ESC
          </button>
        </div>

        <div className="p-4">
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search commands..."
            className="w-full rounded-md border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 shadow-sm focus:border-blue-500 focus:outline-none dark:border-gray-700 dark:bg-gray-950 dark:text-white"
          />

          <div className="mt-3 max-h-64 overflow-y-auto space-y-2">
            {commandsQuery.isLoading && (
              <div className="py-6 text-center text-sm text-gray-400">
                Loading commands...
              </div>
            )}
            {commandsQuery.error && (
              <div className="py-6 text-center text-sm text-red-500 dark:text-red-400">
                Failed to load commands: {getErrorMessage(commandsQuery.error)}
              </div>
            )}
            {!commandsQuery.isLoading && !commandsQuery.error && filtered.length === 0 && (
              <div className="py-6 text-center text-sm text-gray-400">
                No commands match your search.
              </div>
            )}
            {filtered.map((command, index) => (
              <button
                key={command.name}
                type="button"
                onClick={() => runCommand(command)}
                onMouseEnter={() => setSelectedIndex(index)}
                className={`w-full rounded-md border px-3 py-2 text-left text-sm ${
                  index === selectedIndex
                    ? "border-blue-400 bg-blue-50 text-blue-900 dark:border-blue-500 dark:bg-blue-900/20 dark:text-blue-200"
                    : "border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-gray-700 dark:bg-gray-950 dark:text-gray-300"
                }`}
              >
                <div className="flex items-center justify-between">
                  <span className="font-medium">{command.name}</span>
                  {command.category && (
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {command.category}
                    </span>
                  )}
                </div>
                <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {command.description}
                </div>
              </button>
            ))}
          </div>

          {(error || result) && (
            <div className="mt-4 rounded-md border border-gray-200 bg-gray-50 p-3 text-xs text-gray-700 dark:border-gray-700 dark:bg-gray-950 dark:text-gray-200">
              {error && <div className="text-red-600 dark:text-red-400">{error}</div>}
              {result && (
                <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap">
                  {result}
                </pre>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
