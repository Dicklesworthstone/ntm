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
  ApiError,
  apiFetch,
  extractKernelPathParams,
  getConnectionConfig,
  resolveKernelRESTPath,
  type ApiEnvelope,
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

  useEffect(() => {
    // Redirect to /connect when no connection config is saved. The content
    // renders unconditionally so the static export carries each route's real
    // markup (the F1 non-stub proof asserts on the exported HTML).
    const config = getConnectionConfig();
    if (!config) {
      router.replace("/connect");
    }
  }, [router]);

  return (
    <NtmQueryProvider>
      <DashboardContent>{children}</DashboardContent>
    </NtmQueryProvider>
  );
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

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "Unexpected error";
}

async function fetchKernelCommands(): Promise<KernelListResponse> {
  try {
    return await apiFetch<KernelListResponse>("/api/v1/kernel/commands");
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      return apiFetch<KernelListResponse>("/api/kernel/commands");
    }
    throw err;
  }
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
  const [pendingCommand, setPendingCommand] = useState<KernelCommand | null>(null);
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [isExecuting, setIsExecuting] = useState(false);
  const [result, setResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      setPendingCommand(null);
      setParamValues({});
      setError(null);
      setResult(null);
      setIsExecuting(false);
      return;
    }
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
    queryFn: fetchKernelCommands,
    enabled: open,
  });

  const commands = useMemo(
    () => commandsQuery.data?.commands ?? [],
    [commandsQuery.data?.commands]
  );
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

  const pendingParams = useMemo(
    () =>
      pendingCommand?.rest?.path
        ? extractKernelPathParams(pendingCommand.rest.path)
        : [],
    [pendingCommand]
  );

  const executeCommand = async (
    command: KernelCommand,
    params: Record<string, string>
  ) => {
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

    setIsExecuting(true);
    try {
      const resolvedPath = resolveKernelRESTPath(command.rest.path, params);
      if (process.env.NODE_ENV === "development") {
        console.log("[Command Palette] run", command.name, resolvedPath);
      }
      const response = await apiFetch<Record<string, unknown>>(resolvedPath);
      setResult(JSON.stringify(response, null, 2));
    } catch (err) {
      if (process.env.NODE_ENV === "development") {
        console.error("[Command Palette] error", err);
      }
      setError(getErrorMessage(err));
    } finally {
      setIsExecuting(false);
    }
  };

  const selectCommand = (command: KernelCommand, index: number) => {
    setSelectedIndex(index);
    setError(null);
    setResult(null);

    if (!command.rest?.path || !command.rest?.method) {
      setPendingCommand(null);
      setError("Command has no REST binding.");
      return;
    }

    if (command.rest.method.toUpperCase() !== "GET") {
      setPendingCommand(null);
      setError("This command requires input; only GET commands are supported here.");
      return;
    }

    const requiredParams = extractKernelPathParams(command.rest.path);
    if (requiredParams.length > 0) {
      setPendingCommand(command);
      setParamValues((prev) => {
        const next: Record<string, string> = {};
        for (const param of requiredParams) {
          next[param] = prev[param] ?? "";
        }
        return next;
      });
      return;
    }

    setPendingCommand(null);
    setParamValues({});
    void executeCommand(command, {});
  };

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 p-4"
      onClick={() => onOpenChange(false)}
    >
      <div
        role="dialog"
        aria-label="Command Palette"
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
            {filtered.map((command, index) => {
              const params = command.rest?.path
                ? extractKernelPathParams(command.rest.path)
                : [];
              return (
                <button
                  key={command.name}
                  type="button"
                  onClick={() => selectCommand(command, index)}
                  onMouseEnter={() => setSelectedIndex(index)}
                  className={`w-full rounded-md border px-3 py-2 text-left text-sm ${
                    index === selectedIndex
                      ? "border-blue-400 bg-blue-50 text-blue-900 dark:border-blue-500 dark:bg-blue-900/20 dark:text-blue-200"
                      : "border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-gray-700 dark:bg-gray-950 dark:text-gray-300"
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-medium">{command.name}</span>
                    <div className="flex items-center gap-2">
                      {params.length > 0 && (
                        <span className="rounded bg-gray-100 px-1.5 py-0.5 font-mono text-[11px] text-gray-600 dark:bg-gray-800 dark:text-gray-300">
                          {params.map((p) => `{${p}}`).join(" ")}
                        </span>
                      )}
                      {command.category && (
                        <span className="text-xs text-gray-500 dark:text-gray-400">
                          {command.category}
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {command.description}
                  </div>
                </button>
              );
            })}
          </div>

          {pendingCommand && pendingParams.length > 0 && (
            <form
              aria-label={`Parameters for ${pendingCommand.name}`}
              onSubmit={(event) => {
                event.preventDefault();
                void executeCommand(pendingCommand, paramValues);
              }}
              className="mt-4 rounded-md border border-blue-200 bg-blue-50/40 p-3 space-y-3 dark:border-blue-800 dark:bg-blue-950/30"
            >
              <div className="flex items-center justify-between gap-2">
                <div className="text-xs font-semibold text-gray-900 dark:text-white">
                  Parameters for {pendingCommand.name}
                </div>
                <code className="text-[11px] text-gray-500 dark:text-gray-400">
                  {pendingCommand.rest?.path}
                </code>
              </div>

              <div className="grid gap-2 sm:grid-cols-2">
                {pendingParams.map((paramName) => {
                  const inputId = `kernel-param-${paramName}`;
                  return (
                    <div key={paramName}>
                      <label
                        htmlFor={inputId}
                        className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                      >
                        {paramName}
                      </label>
                      <input
                        id={inputId}
                        name={paramName}
                        value={paramValues[paramName] ?? ""}
                        onChange={(event) =>
                          setParamValues((prev) => ({
                            ...prev,
                            [paramName]: event.target.value,
                          }))
                        }
                        placeholder={`Enter ${paramName}...`}
                        className="w-full rounded-md border border-gray-200 bg-white px-2.5 py-1.5 text-sm text-gray-900 focus:border-blue-500 focus:outline-none dark:border-gray-700 dark:bg-gray-900 dark:text-white"
                      />
                    </div>
                  );
                })}
              </div>

              <div className="flex items-center justify-end gap-2">
                <button
                  type="button"
                  onClick={() => {
                    setPendingCommand(null);
                    setParamValues({});
                  }}
                  className="rounded-md border border-gray-200 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-100 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-gray-800"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={isExecuting}
                  className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
                >
                  {isExecuting ? "Running..." : "Run Command"}
                </button>
              </div>
            </form>
          )}

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
