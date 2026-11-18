import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SessionAdoptContextDiffPanel } from "../components/sessions/SessionAdoptContextDiffPanel";
import { PersonasAndRecipesPanel } from "../components/agents/PersonasAndRecipesPanel";
import { GitHooksAndRedactionPanel } from "../components/safety/GitHooksAndRedactionPanel";

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("Wave 14 Web Cockpit Surfaces", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
  });

  it("wires SessionAdoptContextDiffPanel (adopt, context, diff, files)", async () => {
    const calls: Array<{ method: string; path: string; body?: unknown }> = [];

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        const parsedBody = init?.body ? JSON.parse(String(init.body)) : undefined;
        calls.push({ method, path: url.pathname + url.search, body: parsedBody });

        if (url.pathname === "/api/v1/sessions/ext-proj/adopt") {
          return new Response(
            JSON.stringify({
              success: true,
              session: "ext-proj",
              dry_run: true,
              total_panes: 2,
              adopted_panes: [
                {
                  pane_id: "%1",
                  pane_index: 0,
                  window_index: 0,
                  agent_type: "agy",
                  new_title: "ext-proj__agy_1",
                },
              ],
              agents: { agy: 1 },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/ext-proj/context") {
          return new Response(
            JSON.stringify({
              success: true,
              session: "ext-proj",
              agents: [
                {
                  pane: "0.0",
                  pane_id: "%1",
                  agent_type: "agy",
                  usage_percent: 42.4,
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/ext-proj/diff") {
          return new Response(
            JSON.stringify({
              success: true,
              session: "ext-proj",
              files: { modified: ["internal/cli/adopt.go"], potential_conflicts: [] },
              agent_activity: [
                { pane: "0.0", agent_type: "agy", state: "working", output_lines: 88 },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/ext-proj/files") {
          return new Response(
            JSON.stringify({
              success: true,
              session: "ext-proj",
              time_window: "15m",
              count: 1,
              changes: [
                {
                  timestamp: "2026-11-18T18:00:00Z",
                  path: "internal/cli/adopt.go",
                  operation: "modify",
                  agents: ["agy_1"],
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        return new Response(JSON.stringify({ success: false }), { status: 404 });
      }
    ) as unknown as typeof fetch;

    renderWithClient(<SessionAdoptContextDiffPanel sessionId="ext-proj" />);

    fireEvent.change(screen.getByLabelText("Adopt Harness"), {
      target: { value: "agy" },
    });
    fireEvent.click(screen.getByText("Preview Adopt"));
    await waitFor(() =>
      expect(screen.getByText(/ext-proj__agy_1/)).toBeDefined()
    );

    fireEvent.click(screen.getByText("Load Context"));
    await waitFor(() => expect(screen.getByText("42%")).toBeDefined());

    fireEvent.click(screen.getByText("Load Diff"));
    await waitFor(() => expect(screen.getByText(/88 lines/)).toBeDefined());

    fireEvent.click(screen.getByText("Load Files"));
    await waitFor(() =>
      expect(screen.getByText(/1 tracked file change/)).toBeDefined()
    );

    expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/sessions/ext-proj/adopt")).toBe(true);
  });

  it("wires PersonasAndRecipesPanel (11-harness personas + recipes)", async () => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(
        typeof input === "string" ? input : input.toString(),
        "http://127.0.0.1:7337"
      );
      if (url.pathname === "/api/v1/personas") {
        return new Response(
          JSON.stringify({
            success: true,
            count: 1,
            builtin_count: 6,
            personas: [
              {
                name: "agy-planner",
                agent_type: "agy",
                description: "Antigravity planner persona",
              },
            ],
            profile_sets: [{ name: "core-review", personas: ["agy-planner"] }],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/recipes") {
        return new Response(
          JSON.stringify({
            success: true,
            count: 1,
            recipes: [
              {
                name: "balanced-swarm",
                description: "2 Claude + 1 Codex",
                total_agents: 3,
                agents: [{ type: "cc", count: 2 }, { type: "cod", count: 1 }],
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      return new Response(JSON.stringify({ success: false }), { status: 404 });
    }) as unknown as typeof fetch;

    const picked: string[] = [];
    renderWithClient(
      <PersonasAndRecipesPanel onSelectRecipePreset={(r) => picked.push(r)} />
    );

    await waitFor(() => expect(screen.getByText("agy-planner")).toBeDefined());
    await waitFor(() =>
      expect(screen.getByText(/balanced-swarm/)).toBeDefined()
    );
    fireEvent.click(screen.getByText("Use Recipe"));
    expect(picked).toEqual(["balanced-swarm"]);
  });

  it("wires GitHooksAndRedactionPanel (hooks status/run + redact preview)", async () => {
    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        if (url.pathname === "/api/v1/hooks" && method === "GET") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "status",
              installed_count: 1,
              hooks: [
                {
                  type: "pre-commit",
                  path: ".git/hooks/pre-commit",
                  installed: true,
                  is_ntm: true,
                  has_backup: false,
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/hooks/run" && method === "POST") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "run",
              hook_type: "pre-commit",
              passed: true,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/redact/preview" && method === "POST") {
          return new Response(
            JSON.stringify({
              success: true,
              input_len: 44,
              finding_count: 1,
              output: "Authorization: Bearer [REDACTED:OPENAI_KEY]",
              findings: [
                {
                  category: "OPENAI_KEY",
                  redacted: "[REDACTED:OPENAI_KEY]",
                  line: 1,
                  column: 23,
                  start: 22,
                  end: 44,
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        return new Response(JSON.stringify({ success: false }), { status: 404 });
      }
    ) as unknown as typeof fetch;

    renderWithClient(<GitHooksAndRedactionPanel />);

    await waitFor(() =>
      expect(screen.getByText("installed (ntm)")).toBeDefined()
    );

    fireEvent.click(screen.getByText("Run Hook Check"));
    await waitFor(() => expect(screen.getByText("PASSED")).toBeDefined());

    fireEvent.change(screen.getByLabelText("Redaction Input Text"), {
      target: { value: "Authorization: Bearer sk-placeholder-test" },
    });
    fireEvent.click(screen.getByText("Preview Redaction"));
    await waitFor(() =>
      expect(
        screen.getByText("Authorization: Bearer [REDACTED:OPENAI_KEY]")
      ).toBeDefined()
    );
  });
});
