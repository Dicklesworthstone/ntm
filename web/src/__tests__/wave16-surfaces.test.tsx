import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SessionGrepExtractErrorsPanel } from "../components/sessions/SessionGrepExtractErrorsPanel";
import { PluginsAndLocalModelsPanel } from "../components/agents/PluginsAndLocalModelsPanel";
import { ConfigAndPolicyValidatorPanel } from "../components/safety/ConfigAndPolicyValidatorPanel";

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("Wave 16 Web Cockpit Surfaces", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
  });

  it("wires SessionGrepExtractErrorsPanel (grep, extract, errors)", async () => {
    const calls: Array<{ method: string; path: string }> = [];

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        calls.push({ method, path: `${url.pathname}${url.search}` });

        if (url.pathname === "/api/v1/sessions/proj_w16/grep") {
          return new Response(
            JSON.stringify({
              success: true,
              pattern: "panic:",
              session: "proj_w16",
              matches: [
                {
                  session: "proj_w16",
                  pane: "proj_w16__agy_1",
                  pane_id: "%4",
                  pane_index: 1,
                  agent_type: "antigravity",
                  line: 17,
                  content: "panic: nil pointer dereference",
                },
              ],
              matching_panes: ["proj_w16/proj_w16__agy_1"],
              total_lines_searched: 120,
              match_count: 1,
              panes_searched: 2,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/proj_w16/extract") {
          return new Response(
            JSON.stringify({
              success: true,
              session: "proj_w16",
              source: "proj_w16",
              blocks: [
                {
                  language: "go",
                  file_path: "internal/demo/fix.go",
                  content: "package demo\nfunc Fixed() bool { return true }",
                  source_pane: "proj_w16:.1",
                },
              ],
              total_found: 1,
              panes_scanned: 2,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/proj_w16/errors") {
          return new Response(
            JSON.stringify({
              success: true,
              session: "proj_w16",
              errors: [
                {
                  timestamp: "2026-11-18T21:00:00Z",
                  pane_id: "%4",
                  pane_name: "proj_w16__agy_1",
                  pane_index: 1,
                  agent_type: "antigravity",
                  line_number: 17,
                  content: "panic: nil pointer dereference",
                  match_type: "panic",
                },
              ],
              summary: {
                total_errors: 1,
                total_lines_searched: 120,
                panes_searched: 2,
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        return new Response(JSON.stringify({ success: false }), { status: 404 });
      }
    ) as unknown as typeof fetch;

    renderWithClient(<SessionGrepExtractErrorsPanel sessionId="proj_w16" />);

    fireEvent.change(screen.getByLabelText("11-Harness pane filter"), {
      target: { value: "agy" },
    });
    fireEvent.change(screen.getByLabelText("Pane grep regex pattern"), {
      target: { value: "panic:" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Run Pane Grep" }));

    await waitFor(() => {
      expect(
        screen.getByText(/panic: nil pointer dereference/)
      ).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("button", { name: "Code Blocks" }));
    await waitFor(() => {
      expect(screen.getByText(/internal\/demo\/fix\.go/)).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("button", { name: "Error Signatures" }));
    await waitFor(() => {
      expect(screen.getByText(/\[panic\]/)).toBeInTheDocument();
    });

    expect(
      calls.some((c) =>
        c.path.startsWith("/api/v1/sessions/proj_w16/grep?pattern=panic%3A&type=agy")
      )
    ).toBe(true);
  });

  it("wires PluginsAndLocalModelsPanel (plugins, VRAM recommend, live Ollama list)", async () => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(
        typeof input === "string" ? input : input.toString(),
        "http://127.0.0.1:7337"
      );

      if (url.pathname === "/api/v1/plugins") {
        return new Response(
          JSON.stringify({
            success: true,
            config_dir: "/home/user/.config/ntm",
            agents_dir: "/home/user/.config/ntm/agents",
            commands_dir: "/home/user/.config/ntm/commands",
            agent_plugins: [
              {
                name: "hermes",
                alias: "hm",
                command: "hermes-cli",
                description: "Custom Hermes harness",
                env_keys: ["HERMES_KEY"],
                has_readiness: true,
              },
            ],
            command_plugins: [],
            total_count: 1,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }

      if (
        url.pathname === "/api/v1/models" &&
        url.searchParams.get("action") === "recommend"
      ) {
        return new Response(
          JSON.stringify({
            success: true,
            action: "recommend",
            detected_vram_gb: 24,
            vram_source: "nvidia-smi",
            models: [],
            recommendations: ["mixtral:8x7b", "codellama:70b"],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }

      if (
        url.pathname === "/api/v1/models" &&
        url.searchParams.get("action") === "list"
      ) {
        return new Response(
          JSON.stringify({
            success: true,
            action: "list",
            host: "http://127.0.0.1:11434",
            model_count: 1,
            total_size_bytes: 21474836480,
            models: [
              {
                name: "deepseek-coder:33b",
                size_bytes: 21474836480,
                modified_at: "2026-11-18T20:00:00Z",
                parameters: "33B",
              },
            ],
            recommendations: ["mixtral:8x7b"],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }

      return new Response(JSON.stringify({ success: false }), { status: 404 });
    }) as unknown as typeof fetch;

    renderWithClient(<PluginsAndLocalModelsPanel />);

    await waitFor(() => {
      expect(screen.getByText(/Custom Hermes harness/)).toBeInTheDocument();
      expect(screen.getByText(/24\.0 GB VRAM/)).toBeInTheDocument();
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Probe Live Ollama Inventory" })
    );
    await waitFor(() => {
      expect(screen.getByText("deepseek-coder:33b")).toBeInTheDocument();
    });
  });

  it("wires ConfigAndPolicyValidatorPanel (GET + POST --fix)", async () => {
    let postBody: unknown = null;

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        if (url.pathname === "/api/v1/config/validate" && method === "GET") {
          return new Response(
            JSON.stringify({
              success: true,
              valid: true,
              results: [
                {
                  path: "/home/user/.config/ntm/config.toml",
                  type: "main",
                  valid: true,
                  warnings: [
                    {
                      field: "projects_base",
                      message: "directory does not exist: /tmp/projects",
                      fixable: true,
                    },
                  ],
                },
              ],
              summary: {
                files_checked: 1,
                error_count: 0,
                warning_count: 1,
                fixable_count: 1,
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/config/validate" && method === "POST") {
          postBody = init?.body ? JSON.parse(String(init.body)) : null;
          return new Response(
            JSON.stringify({
              success: true,
              valid: true,
              fix_applied: true,
              results: [
                {
                  path: "/home/user/.config/ntm/config.toml",
                  type: "main",
                  valid: true,
                  info: ["created projects_base: /tmp/projects"],
                },
              ],
              summary: {
                files_checked: 1,
                error_count: 0,
                warning_count: 0,
                fixable_count: 0,
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        return new Response(JSON.stringify({ success: false }), { status: 404 });
      }
    ) as unknown as typeof fetch;

    renderWithClient(<ConfigAndPolicyValidatorPanel />);

    await waitFor(() => {
      expect(screen.getByText(/directory does not exist/)).toBeInTheDocument();
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Auto-Fix Directories (--fix)" })
    );
    await waitFor(() => {
      expect(screen.getByText(/created projects_base/)).toBeInTheDocument();
    });
    expect(postBody).toEqual({ all: false, fix: true });
  });
});
