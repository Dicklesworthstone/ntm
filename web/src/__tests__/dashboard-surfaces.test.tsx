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
import AgentsPage from "../app/(dashboard)/agents/page";
import AttentionPage from "../app/(dashboard)/attention/page";
import EnsemblePage from "../app/(dashboard)/ensemble/page";
import MemoryPage from "../app/(dashboard)/memory/page";
import SessionsPage from "../app/(dashboard)/sessions/page";
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

  it("renders links to all 13 dashboard routes in desktop and mobile navigation", () => {
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

    // Open mobile navigation drawer and verify all 10 routes are reachable there too
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
