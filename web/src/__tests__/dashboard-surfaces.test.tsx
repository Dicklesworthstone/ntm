import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const navigationState = {
  pathname: "/",
  searchParams: new URLSearchParams(),
  replace: vi.fn(),
  push: vi.fn(),
};

vi.mock("next/navigation", () => ({
  usePathname: () => navigationState.pathname,
  useSearchParams: () => navigationState.searchParams,
  useRouter: () => ({
    replace: navigationState.replace,
    push: navigationState.push,
    prefetch: vi.fn(),
  }),
}));

// Mock localStorage
const localStorageMock = {
  store: {} as Record<string, string>,
  getItem(key: string) {
    return this.store[key] || null;
  },
  setItem(key: string, value: string) {
    this.store[key] = value;
  },
  removeItem(key: string) {
    delete this.store[key];
  },
  clear() {
    this.store = {};
  },
};

Object.defineProperty(global, "localStorage", { value: localStorageMock });

// Controllable MockWebSocket for UI integration tests
class UiMockWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances: UiMockWebSocket[] = [];
  static autoOpen = true;

  readyState = UiMockWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  sentFrames: Array<{
    type: string;
    request_id: string;
    data?: Record<string, unknown>;
  }> = [];

  constructor(public url: string) {
    UiMockWebSocket.instances.push(this);
    if (UiMockWebSocket.autoOpen) {
      queueMicrotask(() => {
        if (this.readyState === UiMockWebSocket.CONNECTING) {
          this.readyState = UiMockWebSocket.OPEN;
          this.onopen?.();
        }
      });
    }
  }

  send = vi.fn((raw: string) => {
    const parsed = JSON.parse(raw) as {
      type: string;
      request_id: string;
      data?: Record<string, unknown>;
    };
    this.sentFrames.push(parsed);
    if (parsed.type === "subscribe" || parsed.type === "unsubscribe") {
      queueMicrotask(() => {
        if (this.readyState === UiMockWebSocket.OPEN) {
          this.onmessage?.({
            data: JSON.stringify({
              type: "ack",
              ts: new Date().toISOString(),
              request_id: parsed.request_id,
            }),
          });
        }
      });
    }
  });

  close = vi.fn((code?: number, reason?: string) => {
    this.readyState = UiMockWebSocket.CLOSED;
    this.onclose?.({ code: code ?? 1000, reason: reason ?? "" });
  });
}

Object.defineProperty(global, "WebSocket", { value: UiMockWebSocket });

import { NavBar } from "../components/layout/nav-bar";
import DashboardLayout from "../app/(dashboard)/layout";
import OverviewPage from "../app/(dashboard)/page";
import AgentsPage from "../app/(dashboard)/agents/page";
import AttentionPage from "../app/(dashboard)/attention/page";
import EnsemblePage from "../app/(dashboard)/ensemble/page";
import MemoryPage from "../app/(dashboard)/memory/page";
import SessionsPage from "../app/(dashboard)/sessions/page";
import SwarmPage from "../app/(dashboard)/swarm/page";
import WorktreesPage from "../app/(dashboard)/worktrees/page";
import { resetClient } from "../lib/api/client";
import { handleWsEvent, resetQueryClient } from "../lib/hooks/use-query";
import { getWsClient, resetWsClient } from "../lib/ws/client";

const clients: QueryClient[] = [];

function createTestQueryClient(): QueryClient {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, refetchOnWindowFocus: false },
    },
  });
  clients.push(client);
  return client;
}

beforeEach(() => {
  localStorageMock.clear();
  localStorageMock.setItem(
    "ntm-connection",
    JSON.stringify({ baseUrl: "http://localhost:7337" })
  );
  navigationState.pathname = "/";
  navigationState.searchParams = new URLSearchParams();
  navigationState.replace.mockReset();
  navigationState.push.mockReset();
  UiMockWebSocket.instances = [];
  UiMockWebSocket.autoOpen = true;
  resetWsClient();
  resetClient();
  resetQueryClient();
});

afterEach(() => {
  cleanup();
  for (const client of clients) client.clear();
  clients.length = 0;
  resetWsClient();
  resetClient();
  resetQueryClient();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("NavBar route coverage and active state", () => {
  const expectedRoutes = [
    { href: "/", label: "Overview" },
    { href: "/sessions", label: "Sessions" },
    { href: "/agents", label: "Agents" },
    { href: "/ensemble", label: "Ensemble" },
    { href: "/swarm", label: "Swarm" },
    { href: "/worktrees", label: "Worktrees" },
    { href: "/attention", label: "Attention" },
    { href: "/pipelines", label: "Pipelines" },
    { href: "/beads", label: "Beads" },
    { href: "/mail", label: "Mail" },
    { href: "/scanner", label: "Scanner" },
    { href: "/analytics", label: "Analytics" },
    { href: "/accounts", label: "Accounts" },
    { href: "/safety", label: "Safety" },
    { href: "/memory", label: "Memory" },
  ];

  it("renders links to all 15 dashboard routes in desktop and mobile navigation", () => {
    navigationState.pathname = "/pipelines/";
    render(<NavBar wsState="connected" />);

    const desktopNav = screen.getByRole("navigation", { name: "Main navigation" });
    for (const route of expectedRoutes) {
      const link = within(desktopNav).getByRole("link", { name: route.label });
      expect(link).toHaveAttribute("href", route.href);
    }

    // Active highlighting handles trailingSlash: true without marking "/" active
    expect(within(desktopNav).getByRole("link", { name: "Pipelines" })).toHaveAttribute(
      "aria-current",
      "page"
    );
    expect(
      within(desktopNav).getByRole("link", { name: "Overview" })
    ).not.toHaveAttribute("aria-current");

    // Open mobile navigation drawer and verify all 15 routes are reachable there too
    fireEvent.click(screen.getByRole("button", { name: "Toggle navigation menu" }));
    const mobileNav = screen.getByRole("navigation", { name: "Mobile navigation" });
    for (const route of expectedRoutes) {
      const link = within(mobileNav).getByRole("link", { name: route.label });
      expect(link).toHaveAttribute("href", route.href);
    }
  });
});

describe("Ctrl+K Command Palette (/api/v1 resolution & {param} prompts)", () => {
  it("normalizes /sessions to /api/v1/sessions and prompts inline for {sessionId} with encodeURIComponent", async () => {
    const requestedPaths: string[] = [];
    const transport = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input));
      requestedPaths.push(url.pathname);
      const reply = (body: object) =>
        new Response(JSON.stringify({ success: true, ...body }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });

      if (url.pathname === "/api/v1/kernel/commands") {
        return reply({
          commands: [
            {
              name: "sessions.list",
              description: "List active sessions",
              category: "sessions",
              rest: { method: "GET", path: "/sessions" },
            },
            {
              name: "sessions.events",
              description: "Read session events",
              category: "sessions",
              rest: { method: "GET", path: "/sessions/{sessionId}/events" },
            },
          ],
        });
      }
      if (url.pathname === "/api/v1/sessions") {
        return reply({ sessions: [{ name: "alpha" }] });
      }
      if (url.pathname === "/api/v1/sessions/my%2Fproj%231/events") {
        return reply({ events: [{ type: "session.created" }] });
      }
      throw new Error(`Unexpected request path: ${url.pathname}`);
    });
    vi.stubGlobal("fetch", transport);

    render(
      <DashboardLayout>
        <div>Dashboard Child</div>
      </DashboardLayout>
    );

    // Open palette via Ctrl+K
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    const dialog = await screen.findByRole("dialog", { name: "Command Palette" });

    // 1. Parameterless command normalizes /sessions -> /api/v1/sessions
    fireEvent.click(await within(dialog).findByText("sessions.list"));
    await waitFor(() => {
      expect(requestedPaths).toContain("/api/v1/sessions");
    });

    // 2. Parameterized command renders inline input instead of firing literal {sessionId}
    fireEvent.click(within(dialog).getByText("sessions.events"));
    expect(
      requestedPaths.some((path) => path.includes("{sessionId}"))
    ).toBe(false);

    const paramForm = await within(dialog).findByRole("form", {
      name: "Parameters for sessions.events",
    });
    fireEvent.change(within(paramForm).getByLabelText("sessionId"), {
      target: { value: "my/proj#1" },
    });
    fireEvent.click(within(paramForm).getByRole("button", { name: "Run Command" }));

    await waitFor(() => {
      expect(requestedPaths).toContain("/api/v1/sessions/my%2Fproj%231/events");
    });
    expect(await within(dialog).findByText(/session\.created/)).toBeInTheDocument();
  });
});

describe("AgentsPage + WebSocket cache invalidation", () => {
  it("refetches /agents roster when handleWsEvent receives an agent.* event", async () => {
    let agentStatus = "idle";
    let agentsFetchCount = 0;

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input)).pathname;
        const reply = (body: object) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        if (path === "/api/v1/sessions") {
          return reply({ sessions: [{ name: "my/proj#1", status: "active" }] });
        }
        if (path === "/api/v1/sessions/my%2Fproj%231/agents") {
          agentsFetchCount += 1;
          return reply({
            session_id: "my/proj#1",
            count: 1,
            agents: [
              {
                id: "pane-1",
                session_id: "my/proj#1",
                name: "BlueFox",
                type: "cc",
                status: agentStatus,
              },
            ],
          });
        }
        throw new Error(`Unexpected path: ${path}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <AgentsPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("BlueFox")).toBeInTheDocument();
    expect(screen.getByText("idle")).toBeInTheDocument();
    expect(agentsFetchCount).toBe(1);

    agentStatus = "working";
    act(() => {
      handleWsEvent(client, {
        type: "event",
        topic: "sessions:my/proj#1",
        event_type: "agent.status_changed",
        data: { id: "pane-1", status: "working" },
        ts: new Date().toISOString(),
        seq: 10,
      });
    });

    expect(await screen.findByText("working")).toBeInTheDocument();
    expect(agentsFetchCount).toBe(2);
  });

  it("refetches /memory status when handleWsEvent receives a memory topic event", async () => {
    let daemonState = "running";
    let daemonFetchCount = 0;

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input)).pathname;
        const reply = (body: object) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        if (path === "/api/v1/memory/daemon/status") {
          daemonFetchCount += 1;
          return reply({ installed: true, state: daemonState });
        }
        if (path === "/api/v1/cass/status") {
          return reply({ installed: true, healthy: true, doc_count: 12 });
        }
        if (path === "/api/v1/memory/rules") {
          return reply({ rules: [], anti_patterns: [] });
        }
        throw new Error(`Unexpected path: ${path}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <MemoryPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("running")).toBeInTheDocument();
    expect(daemonFetchCount).toBe(1);

    daemonState = "stopped";
    act(() => {
      handleWsEvent(client, {
        type: "event",
        topic: "memory",
        event_type: "memory.daemon.stopped",
        data: { state: "stopped" },
        ts: new Date().toISOString(),
        seq: 11,
      });
    });

    expect(await screen.findByText("stopped")).toBeInTheDocument();
    expect(daemonFetchCount).toBe(2);
  });
});

describe("SessionsPage live WebSocket PaneStreamer & URL segment encoding", () => {
  it("encodes sessionId on REST URLs, streams live WS pane.output frames, and disables 2.5s polling while WS is connected", async () => {
    const sessionId = "my/proj#1";
    navigationState.searchParams = new URLSearchParams({ session: sessionId });

    const requestedPaths: string[] = [];
    let outputFetchCount = 0;

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        requestedPaths.push(`${options?.method ?? "GET"} ${url.pathname}${url.search}`);
        const reply = (body: object) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/sessions/my%2Fproj%231") {
          return reply({ session: { name: sessionId, status: "active" } });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes") {
          return reply({
            session_id: sessionId,
            count: 1,
            panes: [{ index: 0, title: "claude-0", type: "cc", active: true }],
          });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/0/output") {
          outputFetchCount += 1;
          return reply({
            pane: "0",
            output: "initial backfill line 1\ninitial backfill line 2",
            lines: 2,
          });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/checkpoints") {
          return reply({
            session_name: sessionId,
            checkpoints: [],
            count: 0,
          });
        }
        if (
          url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/0/input" &&
          options?.method === "POST"
        ) {
          return reply({ sent: true, pane: "0" });
        }
        if (
          url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/0/interrupt" &&
          options?.method === "POST"
        ) {
          return reply({});
        }
        throw new Error(`Unexpected request: ${options?.method ?? "GET"} ${url.pathname}`);
      })
    );

    // Connect singleton WebSocket before mounting SessionsPage
    const ws = getWsClient();
    ws.connect();
    await waitFor(() => expect(ws.getState()).toBe("connected"));
    const socket = UiMockWebSocket.instances[0];
    expect(socket).toBeDefined();

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <SessionsPage />
      </QueryClientProvider>
    );

    // Initial REST backfill renders with URL-encoded sessionId
    expect(await screen.findByText("initial backfill line 1")).toBeInTheDocument();
    expect(screen.getByText("initial backfill line 2")).toBeInTheDocument();
    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(outputFetchCount).toBe(1);

    expect(requestedPaths).toContain("GET /api/v1/sessions/my%2Fproj%231");
    expect(requestedPaths).toContain("GET /api/v1/sessions/my%2Fproj%231/panes");
    expect(requestedPaths).toContain(
      "GET /api/v1/sessions/my%2Fproj%231/panes/0/output?lines=200"
    );

    // Subscribed to exact PaneStreamer topic panes:my/proj#1:0
    await waitFor(() => {
      expect(
        socket.sentFrames.some(
          (frame) =>
            frame.type === "subscribe" &&
            Array.isArray(frame.data?.topics) &&
            (frame.data?.topics as string[]).includes(`panes:${sessionId}:0`)
        )
      ).toBe(true);
    });

    // Deliver live incremental pane.output event over WS
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: "event",
          topic: `panes:${sessionId}:0`,
          event_type: "pane.output",
          seq: 11,
          ts: new Date().toISOString(),
          data: {
            lines: ["streamed live line 3"],
            seq: 1,
            is_full: false,
          },
        }),
      });
    });

    expect(await screen.findByText("streamed live line 3")).toBeInTheDocument();
    expect(screen.getByText("initial backfill line 1")).toBeInTheDocument();

    // Deliver full snapshot pane.output event over WS
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: "event",
          topic: `panes:${sessionId}:0`,
          event_type: "pane.output",
          seq: 12,
          ts: new Date().toISOString(),
          data: {
            lines: ["snapshot replacement line"],
            seq: 2,
            is_full: true,
          },
        }),
      });
    });

    expect(await screen.findByText("snapshot replacement line")).toBeInTheDocument();
    expect(screen.queryByText("initial backfill line 1")).not.toBeInTheDocument();

    // Wait > 2.6s while WS remains connected: 2.5s REST polling MUST NOT fire!
    await new Promise((resolve) => setTimeout(resolve, 2700));
    expect(outputFetchCount).toBe(1);

    // Verify sendPrompt and interruptPane also encode sessionId
    fireEvent.change(screen.getByPlaceholderText("Send a prompt to this pane..."), {
      target: { value: "status check" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => {
      expect(requestedPaths).toContain(
        "POST /api/v1/sessions/my%2Fproj%231/panes/0/input"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Interrupt" }));
    await waitFor(() => {
      expect(requestedPaths).toContain(
        "POST /api/v1/sessions/my%2Fproj%231/panes/0/interrupt"
      );
    });
  }, 10000);

  it("polls REST every 2.5s as fallback when WebSocket is disconnected", async () => {
    UiMockWebSocket.autoOpen = false;
    navigationState.searchParams = new URLSearchParams({ session: "offline-proj" });

    let outputFetchCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input));
        const reply = (body: object) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        if (url.pathname === "/api/v1/sessions/offline-proj") {
          return reply({ session: { name: "offline-proj", status: "active" } });
        }
        if (url.pathname === "/api/v1/sessions/offline-proj/panes") {
          return reply({
            session_id: "offline-proj",
            count: 1,
            panes: [{ index: 0, title: "codex-0", type: "cod", active: true }],
          });
        }
        if (url.pathname === "/api/v1/sessions/offline-proj/panes/0/output") {
          outputFetchCount += 1;
          return reply({
            pane: "0",
            output: `polled output #${outputFetchCount}`,
            lines: 1,
          });
        }
        if (url.pathname === "/api/v1/sessions/offline-proj/checkpoints") {
          return reply({
            session_name: "offline-proj",
            checkpoints: [],
            count: 0,
          });
        }
        throw new Error(`Unexpected path: ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <SessionsPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("polled output #1")).toBeInTheDocument();
    expect(screen.getByText("Offline")).toBeInTheDocument();
    expect(outputFetchCount).toBe(1);

    // Disconnected fallback polls after 2.5s
    expect(
      await screen.findByText("polled output #2", {}, { timeout: 4000 })
    ).toBeInTheDocument();
    expect(outputFetchCount).toBeGreaterThanOrEqual(2);
  }, 10000);
});

describe("AttentionPage triage inbox & durable state mutations", () => {
  it("loads digest + events and dispatches ack/snooze mutations to /api/v1/attention/items/{cursor}/state", async () => {
    const mutations: Array<{ cursor: string; body: Record<string, unknown> }> = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const reply = (body: object) =>
          new Response(JSON.stringify(body), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/attention/digest") {
          return reply({
            cursor_start: 1,
            cursor_end: 42,
            event_count: 3,
            by_category: { agent: 2, bead: 1 },
            by_actionability: { action_required: 1, interesting: 2 },
            buckets: {
              action_required: [
                {
                  cursor_start: 42,
                  cursor_end: 42,
                  source_event_count: 1,
                  event: {
                    cursor: 42,
                    ts: "2026-11-13T20:00:00Z",
                    category: "agent",
                    type: "agent.stalled",
                    actionability: "action_required",
                    severity: "warning",
                    summary: "Agent cc_1 stalled awaiting approval",
                  },
                },
              ],
              interesting: [],
              background: [],
            },
            suppressed: { total: 4 },
            summary: "1 action_required from 3 events",
          });
        }

        if (url.pathname === "/api/v1/attention/events") {
          return reply({
            success: true,
            timestamp: "2026-11-13T20:00:01Z",
            version: "1.2.0",
            output_format: "json",
            since_cursor: 0,
            next_cursor: 42,
            newest_cursor: 42,
            oldest_cursor: 1,
            event_count: 1,
            truncated: false,
            events: [
              {
                cursor: 42,
                ts: "2026-11-13T20:00:00Z",
                session: "alpha",
                pane: 1,
                category: "agent",
                type: "agent.stalled",
                actionability: "action_required",
                severity: "warning",
                summary: "Agent cc_1 stalled awaiting approval",
                next_actions: [
                  {
                    action: "robot-tail",
                    args: "--robot-tail=alpha --panes=1",
                  },
                ],
              },
            ],
          });
        }

        if (
          url.pathname === "/api/v1/attention/items/42/state" &&
          options?.method === "POST"
        ) {
          const parsedBody = JSON.parse(String(options.body ?? "{}")) as Record<
            string,
            unknown
          >;
          mutations.push({ cursor: "42", body: parsedBody });
          const nextState =
            parsedBody.action === "ack" ? "acknowledged" : String(parsedBody.action);
          return reply({
            success: true,
            timestamp: "2026-11-13T20:00:02Z",
            target: {
              cursor: 42,
              item_key: "cursor:42",
              dedup_key: "dk-42",
              session: "alpha",
              pane: "1",
            },
            result: {
              action: String(parsedBody.action),
              previous_state: "new",
              new_state: nextState,
              attention_state: {
                item_key: "cursor:42",
                state: nextState,
                pinned: false,
                snoozed_until:
                  typeof parsedBody.until === "string"
                    ? parsedBody.until
                    : undefined,
              },
            },
            audit: {
              event_id: 1,
              decision_id: 1,
              actor_type: "api",
              actor_id: "anonymous",
            },
          });
        }

        throw new Error(`Unexpected request: ${options?.method ?? "GET"} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <AttentionPage />
      </QueryClientProvider>
    );

    expect(
      await screen.findByText("Agent cc_1 stalled awaiting approval")
    ).toBeInTheDocument();
    expect(
      screen.getByText("ntm --robot-tail=alpha --panes=1")
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Acknowledge item 42" }));
    await waitFor(() => {
      expect(mutations).toContainEqual({
        cursor: "42",
        body: { action: "ack" },
      });
    });
    expect(await screen.findByText("acknowledged")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Snooze item 42" }));
    await waitFor(() => {
      expect(
        mutations.some(
          (m) =>
            m.cursor === "42" &&
            m.body.action === "snooze" &&
            typeof m.body.until === "string"
        )
      ).toBe(true);
    });
  });
});

describe("EnsemblePage reasoning studio, recommender & synthesis", () => {
  it("loads modes/presets, recommends a preset, inspects a session, and runs synthesis", async () => {
    const calledEndpoints: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        calledEndpoints.push(`${options?.method ?? "GET"} ${url.pathname}`);
        const reply = (body: object) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/ensemble/modes") {
          return reply({
            action: "ensemble_modes",
            catalog_version: "1.0.0",
            default_tier: "core",
            total_modes: 82,
            core_modes: 28,
            advanced_modes: 42,
            experimental_modes: 12,
            categories: [{ code: "A", name: "Analytical", mode_count: 10 }],
            modes: [
              {
                id: "deductive",
                code: "A1",
                tier: "core",
                name: "Deductive Proof",
                category: { code: "A", name: "Analytical" },
                short_desc: "Derive necessary conclusions from invariants",
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/ensemble/presets") {
          return reply({
            action: "ensemble_presets",
            count: 1,
            presets: [
              {
                name: "bug-hunt",
                display: "Bug Hunt",
                description: "Adversarial fault isolation",
                modes: ["A1", "B2"],
                mode_count: 2,
                synthesis: { strategy: "adversarial" },
                budget: { total: 40000 },
                allow_advanced: false,
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/sessions") {
          return reply({ sessions: [{ name: "ens-demo", status: "active" }] });
        }

        if (
          url.pathname === "/api/v1/ensemble/suggest" &&
          options?.method === "POST"
        ) {
          return reply({
            question: "Find race conditions in server shutdown",
            top_pick: {
              preset_name: "bug-hunt",
              display_name: "Bug Hunt",
              description: "Adversarial fault isolation",
              score: 0.94,
              reasons: ["Matches concurrency bug keywords"],
              mode_count: 2,
              tags: ["bugs"],
            },
            suggestions: [
              {
                preset_name: "bug-hunt",
                display_name: "Bug Hunt",
                description: "Adversarial fault isolation",
                score: 0.94,
                reasons: ["Matches concurrency bug keywords"],
                mode_count: 2,
                tags: ["bugs"],
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/ensemble/sessions/ens-demo") {
          return reply({
            ensemble: {
              session: "ens-demo",
              question: "Find race conditions in server shutdown",
              preset: "bug-hunt",
              status: "ready",
              modes: [
                {
                  id: "deductive",
                  code: "A1",
                  name: "Deductive Proof",
                  agent_type: "cc",
                  status: "done",
                },
              ],
              synthesis: { strategy: "adversarial", status: "ready" },
              budget: { total_tokens: 40000 },
              metrics: { coverage_map: { A: 1 } },
            },
            summary: {
              total_modes: 1,
              completed: 1,
              working: 0,
              pending: 0,
              core_modes: 1,
              advanced_modes: 0,
            },
          });
        }

        if (
          url.pathname === "/api/v1/ensemble/sessions/ens-demo/synthesize" &&
          options?.method === "POST"
        ) {
          return reply({
            action: "ensemble_synthesize",
            session: "ens-demo",
            status: "completed",
            formatted_output: "## Consensus Findings\nNo lock inversion detected.",
          });
        }

        throw new Error(`Unexpected request: ${options?.method ?? "GET"} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <EnsemblePage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("Deductive Proof")).toBeInTheDocument();
    expect(screen.getByText("Bug Hunt")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Analysis Question"), {
      target: { value: "Find race conditions in server shutdown" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Recommend Preset" }));

    expect(await screen.findByText("Top Pick")).toBeInTheDocument();
    expect(calledEndpoints).toContain("POST /api/v1/ensemble/suggest");

    fireEvent.change(screen.getByLabelText("Inspect ensemble session"), {
      target: { value: "ens-demo" },
    });
    expect(
      await screen.findByText("[A1] Deductive Proof")
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Synthesize" }));
    expect(
      await screen.findByText(/No lock inversion detected\./)
    ).toBeInTheDocument();
    expect(calledEndpoints).toContain(
      "POST /api/v1/ensemble/sessions/ens-demo/synthesize"
    );
  });
});

describe("WorktreesPage isolation & merge cockpit", () => {
  it("lists isolated worktrees, creates a new worktree, merges back with inline feedback, and removes via inline confirmation", async () => {
    const worktrees = [
      {
        agent_name: "BlueLake",
        session_id: "alpha-swarm",
        branch_name: "ntm/alpha-swarm/BlueLake",
        path: "/data/projects/ntm/.ntm/worktrees/alpha-swarm/BlueLake",
        created: true,
      },
    ];
    const recordedCalls: Array<{
      method: string;
      path: string;
      idempotencyKey: string | null;
      body?: Record<string, unknown>;
    }> = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
        const headers = new Headers(options?.headers);
        const body =
          typeof options?.body === "string" && options.body.length > 0
            ? (JSON.parse(options.body) as Record<string, unknown>)
            : undefined;
        recordedCalls.push({
          method,
          path: `${url.pathname}${url.search}`,
          idempotencyKey: headers.get("Idempotency-Key"),
          body,
        });

        const reply = (payload: object, status = 200) =>
          new Response(JSON.stringify({ success: true, ...payload }), {
            status,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/worktrees" && method === "GET") {
          return reply({
            project_dir: "/data/projects/ntm",
            worktrees: [...worktrees],
            count: worktrees.length,
          });
        }

        if (url.pathname === "/api/v1/worktrees" && method === "POST") {
          const created = {
            agent_name: String(body?.agent ?? ""),
            session_id: String(body?.session ?? ""),
            branch_name: `ntm/${String(body?.session)}/${String(body?.agent)}`,
            path: `/data/projects/ntm/.ntm/worktrees/${String(body?.session)}/${String(body?.agent)}`,
            created: true,
          };
          worktrees.push(created);
          return reply(
            {
              project_dir: "/data/projects/ntm",
              session: String(body?.session ?? ""),
              worktree: created,
            },
            201
          );
        }

        if (
          url.pathname === "/api/v1/worktrees/BlueLake/merge" &&
          method === "POST"
        ) {
          return reply({
            project_dir: "/data/projects/ntm",
            session: String(body?.session ?? "alpha-swarm"),
            agent_name: "BlueLake",
            source_branch: "ntm/alpha-swarm/BlueLake",
            merged: true,
            preview: {
              source_branch: "ntm/alpha-swarm/BlueLake",
              target_branch: "main",
              merge_base: "794839c4",
              source_commits: 3,
              target_commits: 0,
              already_merged: false,
              can_merge: true,
              has_conflicts: false,
            },
          });
        }

        if (
          url.pathname === "/api/v1/worktrees/BlueLake" &&
          method === "DELETE"
        ) {
          const idx = worktrees.findIndex((w) => w.agent_name === "BlueLake");
          if (idx >= 0) worktrees.splice(idx, 1);
          return reply({
            project_dir: "/data/projects/ntm",
            session: url.searchParams.get("session") ?? "alpha-swarm",
            agent_name: "BlueLake",
            removed: true,
          });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}${url.search}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <WorktreesPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("BlueLake")).toBeInTheDocument();
    expect(screen.getByText(/Branch: ntm\/alpha-swarm\/BlueLake/)).toBeInTheDocument();

    // 1. Create a new isolated worktree
    fireEvent.change(screen.getByLabelText("Session Name"), {
      target: { value: "alpha-swarm" },
    });
    fireEvent.change(screen.getByLabelText("Agent Identifier"), {
      target: { value: "GreenStone" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create Worktree" }));

    expect(await screen.findByText("GreenStone")).toBeInTheDocument();
    const createCall = recordedCalls.find(
      (c) => c.method === "POST" && c.path === "/api/v1/worktrees"
    );
    expect(createCall).toBeDefined();
    expect(createCall?.body).toEqual({
      session: "alpha-swarm",
      agent: "GreenStone",
    });
    expect(createCall?.idempotencyKey).toMatch(/^wt-create-/);

    // 2. Merge Back BlueLake and verify inline merge result banner
    fireEvent.click(screen.getByRole("button", { name: "Merge Back BlueLake" }));
    const mergeBanners = await screen.findAllByText(
      /Merged "ntm\/alpha-swarm\/BlueLake" back into "main" \(3 commit\(s\)\)\./
    );
    expect(mergeBanners.length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText(/Can merge: yes/)).toBeInTheDocument();
    const mergeCall = recordedCalls.find(
      (c) => c.method === "POST" && c.path === "/api/v1/worktrees/BlueLake/merge"
    );
    expect(mergeCall?.body).toEqual({ session: "alpha-swarm" });
    expect(mergeCall?.idempotencyKey).toMatch(/^wt-merge-/);

    // 3. Remove BlueLake via inline confirmation toggle (no window.confirm)
    fireEvent.click(
      screen.getByRole("button", { name: "Remove Worktree BlueLake" })
    );
    const confirmButton = await screen.findByRole("button", {
      name: "Confirm remove BlueLake",
    });
    fireEvent.click(confirmButton);

    await waitFor(() => {
      expect(
        recordedCalls.some(
          (c) =>
            c.method === "DELETE" &&
            c.path === "/api/v1/worktrees/BlueLake?session=alpha-swarm"
        )
      ).toBe(true);
    });
    await waitFor(() => {
      expect(screen.queryByText("BlueLake")).not.toBeInTheDocument();
    });
  });

  it("filters worktrees by search query and surfaces merge conflict blockers on preview", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
        const reply = (payload: object) =>
          new Response(JSON.stringify({ success: true, ...payload }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/worktrees" && method === "GET") {
          return reply({
            project_dir: "/data/projects/ntm",
            count: 2,
            worktrees: [
              {
                agent_name: "AmberOwl",
                session_id: "s-one",
                branch_name: "ntm/s-one/AmberOwl",
                path: "/dp/.ntm/worktrees/s-one/AmberOwl",
                created: true,
              },
              {
                agent_name: "RedCreek",
                session_id: "s-two",
                branch_name: "ntm/s-two/RedCreek",
                path: "/dp/.ntm/worktrees/s-two/RedCreek",
                created: true,
              },
            ],
          });
        }

        if (
          url.pathname === "/api/v1/worktrees/AmberOwl/merge" &&
          method === "POST"
        ) {
          return reply({
            project_dir: "/data/projects/ntm",
            session: "s-one",
            agent_name: "AmberOwl",
            source_branch: "ntm/s-one/AmberOwl",
            merged: false,
            preview: {
              source_branch: "ntm/s-one/AmberOwl",
              target_branch: "main",
              merge_base: "794839c4",
              source_commits: 2,
              target_commits: 1,
              already_merged: false,
              can_merge: false,
              has_conflicts: true,
              conflicts: ["internal/serve/server.go"],
              blockers: ["3-way merge conflict in server.go"],
            },
          });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <WorktreesPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("AmberOwl")).toBeInTheDocument();
    expect(screen.getByText("RedCreek")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Search worktrees"), {
      target: { value: "amber" },
    });
    expect(screen.getByText("AmberOwl")).toBeInTheDocument();
    expect(screen.queryByText("RedCreek")).not.toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Preview merge for AmberOwl" })
    );
    expect(
      await screen.findByText(
        /Preview detected merge blockers or conflicts for "AmberOwl"\./
      )
    ).toBeInTheDocument();
    expect(screen.getByText("internal/serve/server.go")).toBeInTheDocument();
    expect(
      screen.getByText("3-way merge conflict in server.go")
    ).toBeInTheDocument();
  });
});

describe("SwarmPage backpressure heatmap & synthetic load lab", () => {
  it("renders backpressure telemetry + heatmap and executes a synthetic lab scenario with SLO gate breakdown", async () => {
    const labRunPayloads: Array<Record<string, unknown>> = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
        const reply = (payload: object) =>
          new Response(JSON.stringify({ success: true, ...payload }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/health/backpressure" && method === "GET") {
          return reply({
            timestamp: "2026-11-14T03:00:00Z",
            backpressure: {
              captured_at: "2026-11-14T03:00:00Z",
              decision: "coalesce",
              surfaces: [
                {
                  surface: "rest_handler",
                  decision: "ok",
                  queue_depth: 4,
                  queue_capacity: 64,
                  dropped_count: 0,
                  latency_ms: 12,
                },
                {
                  surface: "sse_stream",
                  decision: "coalesce",
                  queue_depth: 32,
                  queue_capacity: 64,
                  dropped_count: 0,
                },
                {
                  surface: "websocket",
                  session: "swarm-heavy",
                  decision: "coalesce",
                  queue_depth: 18,
                  queue_capacity: 128,
                  dropped_count: 2,
                },
              ],
            },
            load_shedding: {
              schema_version: "ntm.load_shedding.v1",
              generated_at: "2026-11-14T03:00:00Z",
              decision: "coalesce",
              recommended_action: "coalesce_updates",
              degraded_surface: "sse_stream",
              reason_codes: ["sse_buffer_high"],
              retry_after_ms: 250,
            },
            heatmap: {
              schema_version: "ntm.overload_heatmap.v1",
              generated_at: "2026-11-14T03:00:00Z",
              overall_decision: "coalesce",
              bucket_count: 1,
              buckets: [
                {
                  surface: "websocket",
                  session: "swarm-heavy",
                  pane: "3",
                  severity: "watch",
                  decision: "coalesce",
                  heatmap_bucket: "session:swarm-heavy",
                  recommended_action: "coalesce_deltas",
                  reason_codes: ["ws_lag_rising"],
                  score: 48,
                  retry_after_ms: 250,
                },
              ],
            },
          });
        }

        if (url.pathname === "/api/v1/swarm/lab/scenarios" && method === "GET") {
          return reply({
            timestamp: "2026-11-14T03:00:00Z",
            action: "swarm_lab_scenarios",
            count: 1,
            scenarios: [
              {
                id: "burst-16-agents",
                gate: "short",
                description: "16 concurrent agents streaming delta bursts",
                synthetic: {
                  name: "Burst 16 Agents",
                  pane_count: 16,
                  command_count: 320,
                  runtime_event_burst: 640,
                  snapshot_iterations: 8,
                  seed: 42,
                },
                budget: {
                  name: "short",
                  max_latency_p95_micros: 25000,
                  max_memory_growth_bytes: 16777216,
                  max_goroutine_leak: 0,
                  min_throughput_per_second: 100,
                },
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/swarm/lab/run" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          labRunPayloads.push(parsed);
          return reply({
            timestamp: "2026-11-14T03:00:02Z",
            action: "swarm_lab_run",
            count: 1,
            experiments: [
              {
                schema_version: "ntm.swarm_lab.v1",
                test_run_id: "run-burst-001",
                scenario_id: "burst-16-agents",
                scenario: "Burst 16 Agents",
                gate: String(parsed.gate ?? "short"),
                captured_at: "2026-11-14T03:00:02Z",
                metrics: {
                  pane_count: 16,
                  command_count: 320,
                  runtime_events_emitted: 640,
                  snapshot_iterations: 8,
                  synthetic_duration_micros: 18400,
                  event_throughput_per_second: 34782.6,
                  latency_p50_micros: 420,
                  latency_p95_micros: 1850,
                  latency_p50_ms: 0.42,
                  latency_p95_ms: 1.85,
                  memory_before_bytes: 10485760,
                  memory_after_bytes: 11534336,
                  memory_growth_bytes: 1048576,
                  goroutines_before: 24,
                  goroutines_after: 24,
                  goroutines_leaked: 0,
                },
                backpressure: {
                  captured_at: "2026-11-14T03:00:02Z",
                  decision: "ok",
                  surfaces: [],
                },
                comparison: {
                  budget: "short",
                  result: "pass",
                  checks: [
                    {
                      metric: "latency_p95_ms",
                      result: "pass",
                      current: "1.85ms",
                      limit: "25.00ms",
                    },
                    {
                      metric: "goroutines_leaked",
                      result: "pass",
                      current: "0",
                      limit: "0",
                    },
                  ],
                },
              },
            ],
          });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <SwarmPage />
      </QueryClientProvider>
    );

    // Backpressure badge, occupancy, load-shedding reason, and heatmap row
    await waitFor(() => {
      expect(screen.getByTestId("overall-pressure-badge")).toHaveTextContent(
        "elevated"
      );
    });
    expect(screen.getByText("32 / 64")).toBeInTheDocument();
    expect(screen.getByText(/· 2 dropped/)).toBeInTheDocument();
    expect(screen.getByText("sse_buffer_high")).toBeInTheDocument();
    expect(screen.getByText("swarm-heavy")).toBeInTheDocument();
    expect(screen.getByText("ws_lag_rising")).toBeInTheDocument();

    // Scenario catalog renders and Run Scenario dispatches POST /api/v1/swarm/lab/run
    expect(await screen.findByText("Burst 16 Agents")).toBeInTheDocument();
    const runBtn = screen.getByRole("button", { name: "Run Scenario" });
    await waitFor(() => expect(runBtn).not.toBeDisabled());
    fireEvent.click(runBtn);

    const verdictBadge = await screen.findByTestId("lab-gate-verdict");
    expect(verdictBadge).toHaveTextContent("Gate: pass");
    expect(screen.getByText("latency_p95_ms")).toBeInTheDocument();
    expect(screen.getByText("1.85ms")).toBeInTheDocument();
    expect(labRunPayloads).toContainEqual({
      scenario: "burst-16-agents",
      gate: "short",
    });
  });
});

describe("SessionsPage checkpoints & integrity verification", () => {
  it("lists session checkpoints, saves a new checkpoint, verifies SHA256/archive integrity, restores, and deletes", async () => {
    const sessionId = "my/proj#1";
    navigationState.searchParams = new URLSearchParams({ session: sessionId });

    const checkpoints: Array<{
      id: string;
      name: string;
      description?: string;
      session_name: string;
      pane_count: number;
      created_at: string;
      git?: { branch?: string; commit?: string };
    }> = [
      {
        id: "cp-20261113",
        name: "baseline-clean",
        description: "Before risky migration",
        session_name: sessionId,
        pane_count: 3,
        created_at: "2026-11-13T21:00:00Z",
        git: { branch: "main", commit: "794839c4abcdef" },
      },
    ];
    const checkpointCalls: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
        checkpointCalls.push(`${method} ${url.pathname}`);
        const reply = (body: object, status = 200) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/sessions/my%2Fproj%231") {
          return reply({ session: { name: sessionId, status: "active" } });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes") {
          return reply({
            session_id: sessionId,
            count: 1,
            panes: [{ index: 0, title: "claude-0", type: "cc", active: true }],
          });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/0/output") {
          return reply({ pane: "0", output: "ready", lines: 1 });
        }
        if (
          url.pathname === "/api/v1/sessions/my%2Fproj%231/checkpoints" &&
          method === "GET"
        ) {
          return reply({
            session_name: sessionId,
            checkpoints: [...checkpoints],
            count: checkpoints.length,
          });
        }
        if (
          url.pathname === "/api/v1/sessions/my%2Fproj%231/checkpoints" &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as {
            name?: string;
            description?: string;
          };
          const created = {
            id: "cp-20261114",
            name: parsed.name || "cp-20261114",
            description: parsed.description,
            session_name: sessionId,
            pane_count: 3,
            created_at: "2026-11-14T03:05:00Z",
          };
          checkpoints.unshift(created);
          return reply({ checkpoint: created }, 201);
        }
        if (
          url.pathname ===
            "/api/v1/sessions/my%2Fproj%231/checkpoints/cp-20261113/verify" &&
          method === "GET"
        ) {
          return reply({
            valid: true,
            schema_valid: true,
            files_present: true,
            consistency_valid: true,
            details: {
              sha256: "e3b0c44298fc1c149afbf4c8996fb924",
              scrollback_files: "3/3 verified",
            },
          });
        }
        if (
          url.pathname ===
            "/api/v1/sessions/my%2Fproj%231/checkpoints/cp-20261113/restore" &&
          method === "POST"
        ) {
          return reply({
            session_name: sessionId,
            checkpoint_id: "cp-20261113",
            panes_restored: 3,
            dry_run: false,
          });
        }
        if (
          url.pathname ===
            "/api/v1/sessions/my%2Fproj%231/checkpoints/cp-20261113" &&
          method === "DELETE"
        ) {
          const idx = checkpoints.findIndex((c) => c.id === "cp-20261113");
          if (idx >= 0) checkpoints.splice(idx, 1);
          return reply({ deleted: "cp-20261113", session_name: sessionId });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <SessionsPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("baseline-clean")).toBeInTheDocument();
    expect(screen.getByText("main@794839c")).toBeInTheDocument();

    // Save a new checkpoint
    fireEvent.change(screen.getByLabelText("Checkpoint Name"), {
      target: { value: "pre-wave-merge" },
    });
    fireEvent.change(screen.getByLabelText("Description (optional)"), {
      target: { value: "All green tests" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Checkpoint" }));

    expect(await screen.findByText("pre-wave-merge")).toBeInTheDocument();
    expect(checkpointCalls).toContain(
      "POST /api/v1/sessions/my%2Fproj%231/checkpoints"
    );

    // Verify integrity inline
    fireEvent.click(
      screen.getByRole("button", { name: "Verify Integrity cp-20261113" })
    );
    expect(
      await screen.findByText("Integrity Verified (SHA256 & Archive Valid)")
    ).toBeInTheDocument();
    expect(
      screen.getByText(/sha256: e3b0c44298fc1c149afbf4c8996fb924/)
    ).toBeInTheDocument();

    // Restore checkpoint
    fireEvent.click(screen.getByRole("button", { name: "Restore cp-20261113" }));
    expect(
      await screen.findByText(/Restored checkpoint "cp-20261113" \(3 pane\(s\) restored\)\./)
    ).toBeInTheDocument();

    // Delete checkpoint via inline confirmation
    fireEvent.click(screen.getByRole("button", { name: "Delete cp-20261113" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm delete cp-20261113" })
    );
    await waitFor(() => {
      expect(checkpointCalls).toContain(
        "DELETE /api/v1/sessions/my%2Fproj%231/checkpoints/cp-20261113"
      );
    });
    await waitFor(() => {
      expect(screen.queryByText("baseline-clean")).not.toBeInTheDocument();
    });
  });
});

describe("MemoryPage daemon lifecycle controls & CASS cross-agent search", () => {
  it("starts/stops the memory daemon and executes interactive CASS conversation search with expandable snippets", async () => {
    let daemonState = "stopped";
    const searchPayloads: Array<Record<string, unknown>> = [];
    const daemonActions: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
        const reply = (body: object) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/memory/daemon/status") {
          return reply({ installed: true, state: daemonState, port: 8201 });
        }
        if (url.pathname === "/api/v1/cass/status") {
          return reply({
            installed: true,
            healthy: true,
            doc_count: 148,
            database_size: 4194304,
          });
        }
        if (url.pathname === "/api/v1/memory/rules") {
          return reply({ rules: [], anti_patterns: [] });
        }
        if (
          url.pathname === "/api/v1/memory/daemon/start" &&
          method === "POST"
        ) {
          daemonActions.push("start");
          daemonState = "running";
          return reply({
            state: "starting",
            port: 8201,
            message: "Memory daemon starting on port 8201",
          });
        }
        if (
          url.pathname === "/api/v1/memory/daemon/stop" &&
          method === "POST"
        ) {
          daemonActions.push("stop");
          daemonState = "stopped";
          return reply({
            state: "stopped",
            message: "Memory daemon stopped",
          });
        }
        if (url.pathname === "/api/v1/cass/search" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          searchPayloads.push(parsed);
          return reply({
            query: String(parsed.query ?? ""),
            limit: Number(parsed.limit ?? 20),
            offset: 0,
            count: 1,
            total_matches: 1,
            meta: { took_ms: 9 },
            hits: [
              {
                source_path: "/home/ubuntu/.claude/sessions/sess-99.jsonl",
                agent: "claude",
                workspace: "/data/projects/ntm",
                session_id: "sess-99",
                title: "Wire idempotency middleware for worktrees",
                score: 0.97,
                line_number: 142,
                snippet: "Mounted idempotencyMiddleware on mutating /api/v1 routes.",
                content:
                  "Full transcript: Mounted idempotencyMiddleware on mutating /api/v1 routes and verified replay headers.",
              },
            ],
          });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <MemoryPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("stopped")).toBeInTheDocument();

    // Start Daemon -> transitions state to running, then Stop Daemon
    fireEvent.click(screen.getByRole("button", { name: "Start Daemon" }));
    expect(await screen.findByText("running")).toBeInTheDocument();
    expect(daemonActions).toContain("start");

    fireEvent.click(screen.getByRole("button", { name: "Stop Daemon" }));
    expect(await screen.findByText("Memory daemon stopped")).toBeInTheDocument();
    expect(daemonActions).toContain("stop");

    // Interactive CASS search
    fireEvent.change(screen.getByLabelText("Search Query"), {
      target: { value: "idempotency middleware" },
    });
    fireEvent.change(screen.getByLabelText("Agent Filter"), {
      target: { value: "claude" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Search CASS" }));

    expect(
      await screen.findByText("Wire idempotency middleware for worktrees")
    ).toBeInTheDocument();
    expect(screen.getByText(/1 shown of 1 match · 9ms/)).toBeInTheDocument();
    expect(screen.getByText("Workspace: /data/projects/ntm")).toBeInTheDocument();
    expect(screen.getByText("Session: sess-99")).toBeInTheDocument();
    expect(searchPayloads).toContainEqual({
      query: "idempotency middleware",
      agent: "claude",
      limit: 20,
      highlight: true,
    });

    // Expand hit snippet
    fireEvent.click(screen.getByRole("button", { name: "Expand snippet" }));
    expect(
      await screen.findByText(/Full transcript: Mounted idempotencyMiddleware/)
    ).toBeInTheDocument();
  });
});

describe("OverviewPage (/) apiFetch migration & non-blocking pulse cards", () => {
  it("renders pulse cards + quick cockpit links via apiFetch", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input));
        const reply = (body: object) =>
          new Response(JSON.stringify({ success: true, ...body }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/sessions") {
          return reply({
            sessions: [
              { id: "s1", name: "core-swarm", status: "active", project_path: "/dp/ntm" },
              { id: "s2", name: "idle-docs", status: "paused" },
            ],
          });
        }
        if (url.pathname === "/api/v1/health/backpressure") {
          return reply({
            timestamp: "2026-11-14T03:00:00Z",
            backpressure: {
              captured_at: "2026-11-14T03:00:00Z",
              decision: "coalesce",
              surfaces: [],
            },
            heatmap: {
              schema_version: "ntm.overload_heatmap.v1",
              generated_at: "2026-11-14T03:00:00Z",
              overall_decision: "coalesce",
              bucket_count: 2,
              buckets: [
                {
                  surface: "sse_stream",
                  severity: "watch",
                  decision: "coalesce",
                  heatmap_bucket: "host",
                  recommended_action: "coalesce",
                  score: 35,
                },
                {
                  surface: "websocket",
                  severity: "watch",
                  decision: "coalesce",
                  heatmap_bucket: "session:core-swarm",
                  recommended_action: "coalesce",
                  score: 30,
                },
              ],
            },
          });
        }
        if (url.pathname === "/api/v1/attention/digest") {
          return new Response(
            JSON.stringify({
              cursor_start: 1,
              cursor_end: 20,
              event_count: 14,
              by_category: { agent: 3 },
              by_actionability: { action_required: 3 },
              buckets: {
                action_required: [{}, {}, {}],
                interesting: [],
                background: [],
              },
              suppressed: { total: 0 },
              summary: "3 action_required",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/worktrees") {
          return reply({
            project_dir: "/dp/ntm",
            worktrees: [{ agent_name: "BlueLake", path: "/dp/ntm/.ntm/wt", created: true }],
            count: 4,
          });
        }

        throw new Error(`Unexpected path: ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <OverviewPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("core-swarm")).toBeInTheDocument();
    expect(screen.getByText("idle-docs")).toBeInTheDocument();

    const pulseRegion = screen.getByLabelText("Overview pulse");
    expect(await within(pulseRegion).findByText("elevated")).toBeInTheDocument();
    expect(within(pulseRegion).getByText("3")).toBeInTheDocument();
    expect(within(pulseRegion).getByText("4")).toBeInTheDocument();

    const quickLinks = screen.getByLabelText("Quick cockpit links");
    expect(within(quickLinks).getByRole("link", { name: /Swarm Lab/ })).toHaveAttribute(
      "href",
      "/swarm"
    );
    expect(
      within(quickLinks).getByRole("link", { name: /Attention Inbox/ })
    ).toHaveAttribute("href", "/attention");
    expect(
      within(quickLinks).getByRole("link", { name: /Ensemble Studio/ })
    ).toHaveAttribute("href", "/ensemble");
    expect(within(quickLinks).getByRole("link", { name: /Worktrees/ })).toHaveAttribute(
      "href",
      "/worktrees"
    );
  });

  it("renders sessions cleanly with graceful card fallback when secondary pulse endpoints fail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input));
        if (url.pathname === "/api/v1/sessions") {
          return new Response(
            JSON.stringify({
              success: true,
              sessions: [{ id: "s1", name: "resilient-session", status: "active" }],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        return new Response(
          JSON.stringify({
            success: false,
            error_code: "UNAVAILABLE",
            error: "Subsystem offline",
          }),
          { status: 503, headers: { "Content-Type": "application/json" } }
        );
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <OverviewPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("resilient-session")).toBeInTheDocument();
    expect(screen.queryByText(/Error loading sessions/)).not.toBeInTheDocument();

    const pulseRegion = screen.getByLabelText("Overview pulse");
    await waitFor(() => {
      expect(within(pulseRegion).getAllByText("Unavailable")).toHaveLength(3);
    });
  });
});
