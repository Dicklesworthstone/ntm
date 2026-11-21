import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SavedSessionsAndResumePanel } from "../components/sessions/SavedSessionsAndResumePanel";
import { AgentMappingAndBadgesPanel } from "../components/agents/AgentMappingAndBadgesPanel";
import { WorkflowsSequencesAndTemplatesPanel } from "../components/pipelines/WorkflowsSequencesAndTemplatesPanel";

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("Wave 15 Web Cockpit Surfaces", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
  });

  it("wires SavedSessionsAndResumePanel (list, save, show, archive, unarchive)", async () => {
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

        if (method === "GET" && url.pathname === "/api/v1/saved-sessions") {
          const isArchived = url.searchParams.get("archived") === "true";
          return new Response(
            JSON.stringify({
              success: true,
              action: "list",
              archived: isArchived,
              count: 1,
              sessions: [
                {
                  name: isArchived ? "archived-snap" : "active-snap",
                  saved_at: "2026-11-18T19:00:00Z",
                  cwd: "/data/projects/ntm",
                  agents: 4,
                  git_branch: "main",
                  file_path: "/tmp/snap.json",
                  file_size: 1024,
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (method === "GET" && url.pathname === "/api/v1/saved-sessions/active-snap") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "show",
              saved_as: "active-snap",
              resumable_panes: 1,
              state: {
                name: "ntm_live",
                saved_at: "2026-11-18T19:00:00Z",
                cwd: "/data/projects/ntm",
                layout: "tiled",
                agents: { cc: 2, agy: 1, oc: 1 },
                panes: [
                  {
                    title: "ntm_live__agy_1",
                    index: 0,
                    agent_type: "agy",
                    session_id: "agy-uuid-99",
                    session_provider: "antigravity",
                    session_freshness: "fresh",
                  },
                ],
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (method === "POST" && url.pathname === "/api/v1/saved-sessions") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "save",
              saved_as: "active-snap",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (
          method === "POST" &&
          (url.pathname.endsWith("/archive") || url.pathname.endsWith("/unarchive"))
        ) {
          return new Response(
            JSON.stringify({ success: true, action: "archive" }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (method === "POST" && url.pathname === "/api/v1/saved-sessions/active-snap/restore") {
          return new Response(
            JSON.stringify({ success: true, action: "restore", name: "active-snap", session: "ntm_live" }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        return new Response(JSON.stringify({ success: true }), { status: 200 });
      }
    ) as unknown as typeof fetch;

    renderWithClient(<SavedSessionsAndResumePanel sessionId="ntm_live" />);

    await screen.findByText("active-snap");
    fireEvent.click(screen.getByRole("button", { name: "Inspect" }));
    await screen.findByText(/Antigravity \(agy\): 1/);
    expect(screen.getByText(/antigravity:agy-uuid-99/)).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Restore" }));
    await screen.findByTestId("saved-session-restore-confirm");
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.path === "/api/v1/saved-sessions/active-snap/restore"
        )
      ).toBe(true)
    );

    fireEvent.click(screen.getByRole("button", { name: "Capture Snapshot" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/saved-sessions")).toBe(
        true
      )
    );

    fireEvent.click(screen.getByRole("button", { name: "Archive" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.path === "/api/v1/saved-sessions/active-snap/archive"
        )
      ).toBe(true)
    );

    fireEvent.click(screen.getByRole("button", { name: "Archived Snapshots" }));
    await screen.findByText("archived-snap");
    fireEvent.click(screen.getByRole("button", { name: "Unarchive" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.path === "/api/v1/saved-sessions/archived-snap/unarchive"
        )
      ).toBe(true)
    );
  });

  it("wires AgentMappingAndBadgesPanel (/api/v1/sessions/{id}/mapping)", async () => {
    globalThis.fetch = vi.fn(async () => {
      return new Response(
        JSON.stringify({
          success: true,
          session: "swarm-alpha",
          count: 1,
          entries: [
            {
              name: "BlueLake",
              session: "swarm-alpha",
              pane_index: 1,
              pane_id: "%2",
              agent_type: "agy",
              identity: {
                pane_id: "%2",
                pane_index: 1,
                label: "[BlueLake!]",
                observation_state: "name_disagreement",
              },
            },
          ],
          discrepancies: [
            {
              pane_id: "%2",
              pane_index: 1,
              assigned_name: "BlueLake",
              resolved_name: "RedFox",
              problems: ["identity file disagrees with NTM assignment"],
            },
          ],
          badges: {
            enabled: true,
            published: 1,
            cleared: 0,
            windows_prepared: 1,
            windows_restored: 0,
          },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      );
    }) as unknown as typeof fetch;

    renderWithClient(<AgentMappingAndBadgesPanel sessionOptions={["swarm-alpha"]} />);
    fireEvent.click(screen.getByRole("button", { name: "Reconcile Mapping" }));

    await screen.findByText("BlueLake");
    expect(screen.getByText("[BlueLake!]")).toBeDefined();
    expect(screen.getByText(/Identity Drift Discrepancies/)).toBeDefined();
  });

  it("wires WorkflowsSequencesAndTemplatesPanel (workflows, session-templates, sequences)", async () => {
    const calls: Array<{ method: string; path: string }> = [];

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        calls.push({ method, path: url.pathname + url.search });

        if (url.pathname === "/api/v1/workflows") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "list",
              workflows: [
                {
                  name: "red-green",
                  description: "TDD ping-pong",
                  source: "builtin",
                  coordination: "ping-pong",
                  agent_count: 2,
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/workflows/red-green/run" && method === "POST") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "run",
              workflow: "red-green",
              session: "ntm_live",
              stage: "red",
              manual_gate: { token: "gate-tok-1" },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/workflows/ntm_live/trigger" && method === "POST") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "trigger",
              session: "ntm_live",
              decision: { to: "green" },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/workflows/ntm_live/stop" && method === "POST") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "stop",
              session: "ntm_live",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/workflows/red-green") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "show",
              workflow: {
                name: "red-green",
                description: "TDD ping-pong",
                source: "builtin",
                coordination: "ping-pong",
                agent_count: 2,
                roles: ["red", "green"],
                validated_triggers: [{ from: "red", to: "green", runtime_type: "file", valid: true }],
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/session-templates") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "list",
              templates: [
                {
                  name: "refactor",
                  description: "Large refactor squad",
                  source: "builtin",
                  agent_count: 3,
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/session-templates/refactor") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "show",
              template: {
                name: "refactor",
                description: "Large refactor squad",
                source: "builtin",
                tags: ["architecture"],
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sequences") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "create",
              sequence: { name: "review-loop", steps: ["inspect", "challenge"] },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sequences/review-loop/next") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "next",
              pane: {
                sequence: "review-loop",
                pane: "%1",
                position: 0,
                prompt: "inspect",
                complete: false,
                advanced: false,
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sequences/review-loop/advance") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "advance",
              pane: {
                sequence: "review-loop",
                pane: "%1",
                position: 1,
                prompt: "challenge",
                complete: false,
                advanced: true,
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        return new Response(JSON.stringify({ success: true }), { status: 200 });
      }
    ) as unknown as typeof fetch;

    renderWithClient(<WorkflowsSequencesAndTemplatesPanel />);

    await screen.findByText("red-green");
    await screen.findByText("refactor");

    fireEvent.click(screen.getByRole("button", { name: "Inspect Workflow" }));
    await screen.findByTestId("workflow-detail-box");

    fireEvent.click(screen.getByRole("button", { name: "Inspect Template" }));
    await screen.findByTestId("session-template-detail-box");

    fireEvent.click(screen.getByRole("button", { name: "Create Sequence" }));
    fireEvent.click(screen.getByRole("button", { name: "Inspect Next Prompt" }));
    await screen.findByText(/Prompt: inspect/);

    fireEvent.click(screen.getByRole("button", { name: "Advance Pane Step" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path === "/api/v1/sequences/review-loop/advance")
      ).toBe(true)
    );

    fireEvent.click(screen.getByRole("button", { name: "Run Workflow" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path === "/api/v1/workflows/red-green/run")
      ).toBe(true)
    );

    await waitFor(() =>
      expect((screen.getByLabelText("Workflow Gate Token") as HTMLInputElement).value).toBe(
        "gate-tok-1"
      )
    );
    fireEvent.click(screen.getByRole("button", { name: "Trigger Manual Gate" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path === "/api/v1/workflows/ntm_live/trigger")
      ).toBe(true)
    );

    fireEvent.click(screen.getByRole("button", { name: "Stop Workflow" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.path === "/api/v1/workflows/ntm_live/stop")
      ).toBe(true)
    );
  });
});
