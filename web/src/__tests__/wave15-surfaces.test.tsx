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

const dialogShowModal = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "showModal");
const dialogClose = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, "close");

beforeEach(() => {
  // JSDOM does not implement the browser's modal dialog lifecycle. Model its
  // open/closed state while exercising the real controls and HTTP client.
  Object.defineProperties(HTMLDialogElement.prototype, {
    showModal: { configurable: true, value: function (this: HTMLDialogElement) { this.open = true; } },
    close: { configurable: true, value: function (this: HTMLDialogElement) { this.open = false; } },
  });
});

afterEach(() => {
  for (const [name, descriptor] of [["showModal", dialogShowModal], ["close", dialogClose]] as const) {
    if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, name, descriptor);
    else Reflect.deleteProperty(HTMLDialogElement.prototype, name);
  }
});

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return { ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>), qc };
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
    await screen.findByText(/Restored session "ntm_live"/);

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

describe("Saved session restore controls", () => {
  const originalFetch = globalThis.fetch;
  type RestoreCall = { body: Record<string, unknown>; key: string | null };
  let restoreCalls: RestoreCall[];
  let replyToRestore: (call: RestoreCall) => Promise<Response>;

  function json(data: unknown, status = 200) {
    return new Response(JSON.stringify(data), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  }

  beforeEach(() => {
    vi.restoreAllMocks();
    restoreCalls = [];
    replyToRestore = async ({ body }) => json({
      success: true,
      action: "restore",
      name: "daily-snapshot",
      session: body.target_name || "project",
      mode: body.mode,
      launch: body.mode !== "topology",
    });
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://127.0.0.1:7337");
      if (url.pathname === "/api/v1/saved-sessions") {
        return json({ success: true, sessions: [{
          name: "daily-snapshot", saved_at: "2026-11-20T12:00:00Z", cwd: "/project",
          agents: 2, file_path: "/snapshots/daily.json", file_size: 1024,
        }] });
      }
      if (url.pathname === "/api/v1/saved-sessions/daily-snapshot") {
        return json({ success: true, resumable_panes: 1, state: {
          name: "project", saved_at: "2026-11-20T12:00:00Z", cwd: "/project",
          layout: "tiled", agents: { cc: 1 }, panes: [{
            title: "project__cc_1", index: 1, agent_type: "cc", session_id: "saved-uuid",
          }],
        } });
      }
      if (url.pathname === "/api/v1/saved-sessions/daily-snapshot/restore" && init?.method === "POST") {
        const call = {
          body: JSON.parse(String(init.body)),
          key: new Headers(init.headers).get("Idempotency-Key"),
        };
        restoreCalls.push(call);
        return replyToRestore(call);
      }
      throw new Error(`Unexpected request: ${init?.method ?? "GET"} ${url.pathname}`);
    }) as typeof fetch;
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  async function openRestore() {
    const rendered = renderWithClient(<SavedSessionsAndResumePanel sessionId="" />);
    await screen.findByText("daily-snapshot");
    fireEvent.click(screen.getByRole("button", { name: "Inspect" }));
    fireEvent.click(await screen.findByRole("button", { name: "Restore Session" }));
    await screen.findByRole("dialog", { name: "Restore saved session" });
    return rendered;
  }

  it("requires confirmation and defaults to restoring only the saved layout", async () => {
    const { qc } = await openRestore();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    expect(restoreCalls).toHaveLength(0);
    expect(screen.getByLabelText("Target session name")).toHaveValue("project");
    expect(screen.getByLabelText("Restore mode")).toHaveValue("topology");
    expect(screen.getByLabelText("Replace an existing target session")).not.toBeChecked();

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(restoreCalls).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Restore Session" }));
    fireEvent.change(screen.getByLabelText("Target session name"), { target: { value: " restored " } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await screen.findByText(/Restored session "restored"\. Pane layout restored/);
    expect(restoreCalls).toHaveLength(1);
    expect(restoreCalls[0].body).toEqual({
      target_name: "restored", force: false, mode: "topology", native: false,
    });
    expect(restoreCalls[0].key).toMatch(/^saved-restore-.+/);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["sessions"] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["session-panes", "restored"] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["saved-session-detail", "daily-snapshot"] });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("restores directly from the list using the snapshot's recorded name when target is blank", async () => {
    const { qc } = renderWithClient(<SavedSessionsAndResumePanel sessionId="" />);
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    await screen.findByText("daily-snapshot");
    fireEvent.click(screen.getByRole("button", { name: "Restore" }));
    await screen.findByRole("dialog", { name: "Restore saved session" });
    expect(screen.getByLabelText("Target session name")).toHaveValue("");
    fireEvent.change(screen.getByLabelText("Target session name"), { target: { value: "  " } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await screen.findByText(/Restored session "project"/);
    expect(restoreCalls[0].body).toEqual({ force: false, mode: "topology", native: false });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["session-panes", "project"] });
  });

  it.each(["launch", "resume"])("supports %s with explicit replacement consent", async (mode) => {
    await openRestore();
    fireEvent.change(screen.getByLabelText("Restore mode"), { target: { value: mode } });
    if (mode === "resume") {
      fireEvent.click(screen.getByLabelText("Use native resume commands (bypass casr)"));
    } else {
      expect(screen.queryByLabelText("Use native resume commands (bypass casr)")).not.toBeInTheDocument();
    }
    fireEvent.click(screen.getByLabelText("Replace an existing target session"));
    expect(screen.getByText(/This will stop the running session "project"/)).toBeInTheDocument();
    expect(restoreCalls).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await screen.findByText(/Restored session "project"/);
    expect(restoreCalls[0].body).toEqual({
      target_name: "project", force: true, mode, native: mode === "resume",
    });
  });

  it("clears native resume when switching to fresh launch", async () => {
    await openRestore();
    fireEvent.change(screen.getByLabelText("Restore mode"), { target: { value: "resume" } });
    fireEvent.click(screen.getByLabelText("Use native resume commands (bypass casr)"));
    fireEvent.change(screen.getByLabelText("Restore mode"), { target: { value: "launch" } });
    fireEvent.change(screen.getByLabelText("Target session name"), { target: { value: "fresh" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await screen.findByText(/Restored session "fresh"/);
    expect(restoreCalls[0].body).toEqual({
      target_name: "fresh", force: false, mode: "launch", native: false,
    });
  });

  it("keeps one key for unchanged retries and starts a new attempt after changes or reopening", async () => {
    replyToRestore = async () => { throw new TypeError("Connection lost"); };
    await openRestore();
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: "Retry Restore" }));
    await waitFor(() => expect(restoreCalls).toHaveLength(2));
    await screen.findByRole("button", { name: "Retry Restore" });
    expect(restoreCalls[1].key).toBe(restoreCalls[0].key);
    expect(restoreCalls[1].body).toEqual(restoreCalls[0].body);

    fireEvent.change(screen.getByLabelText("Target session name"), { target: { value: "another" } });
    fireEvent.click(screen.getByRole("button", { name: "Retry Restore" }));
    await waitFor(() => expect(restoreCalls).toHaveLength(3));
    expect(restoreCalls[2].key).not.toBe(restoreCalls[0].key);
    expect(restoreCalls[2].body.target_name).toBe("another");
    expect(screen.queryByText(/Restored session/)).not.toBeInTheDocument();

    await screen.findByRole("button", { name: "Retry Restore" });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    fireEvent.click(screen.getByRole("button", { name: "Restore Session" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await waitFor(() => expect(restoreCalls).toHaveLength(4));
    expect(restoreCalls[3].key).not.toBe(restoreCalls[0].key);
    expect(restoreCalls[3].body).toEqual(restoreCalls[0].body);
  });

  it("renders a successful restore with no agent outcomes when panes is null", async () => {
    replyToRestore = async ({ body }) => json({
      success: true, action: "restore", name: "daily-snapshot", session: body.target_name,
      mode: "launch", launch: true,
      resume: { session: body.target_name, resumed: 0, launched: 0, skipped: 0, failed: 0, panes: null },
    });
    await openRestore();
    fireEvent.change(screen.getByLabelText("Restore mode"), { target: { value: "launch" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    await screen.findByText(/Restored session "project"/);
    expect(screen.getByText(/0 resumed, 0 started, 0 skipped, 0 failed/)).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("reports partial agent failures and refreshes sessions even when restoration fails", async () => {
    replyToRestore = async () => json({
      success: false, error: "One agent could not resume", error_code: "RESTORE_FAILED",
      details: {
        session: "project", name: "daily-snapshot", mode: "resume",
        resume: { session: "project", resumed: 1, launched: 0, skipped: 1, failed: 1, panes: [
          { index: 2, window_index: 0, agent_type: "cod", action: "failed", error: "Codex unavailable" },
        ] },
      },
    }, 500);
    const { qc } = await openRestore();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    fireEvent.change(screen.getByLabelText("Restore mode"), { target: { value: "resume" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm Restore" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("One agent could not resume");
    expect(screen.queryByText(/Restored session/)).not.toBeInTheDocument();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["sessions"] });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByText(/1 resumed, 0 started, 1 skipped, 1 failed/)).toBeInTheDocument();
    expect(screen.getByText(/Pane 0:2 \(cod\): Codex unavailable/)).toBeInTheDocument();
  });
});
