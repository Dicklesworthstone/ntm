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

    // 3. Pressing Ctrl+K a second time closes the palette cleanly (regression for double-listener bug)
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Command Palette" })).not.toBeInTheDocument();
    });
  });

  it("gates mutating POST commands behind explicit confirmation and sends JSON payload", async () => {
    const postCalls: Array<{ path: string; method: string; body?: string }> = [];
    const transport = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      const method = (init?.method || "GET").toUpperCase();
      if (method !== "GET") {
        postCalls.push({
          path: url.pathname,
          method,
          body: typeof init?.body === "string" ? init.body : undefined,
        });
      }
      const reply = (body: object) =>
        new Response(JSON.stringify({ success: true, ...body }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      if (url.pathname === "/api/v1/kernel/commands") {
        return reply({
          commands: [
            {
              name: "beads.sync",
              description: "Flush beads JSONL",
              category: "beads",
              safety_level: "mutating",
              rest: { method: "POST", path: "/beads/sync" },
            },
          ],
        });
      }
      if (url.pathname === "/api/v1/beads/sync" && method === "POST") {
        return reply({ synced: true });
      }
      throw new Error(`Unexpected request: ${method} ${url.pathname}`);
    });
    vi.stubGlobal("fetch", transport);

    render(
      <DashboardLayout>
        <div>Dashboard Child</div>
      </DashboardLayout>
    );

    fireEvent.keyDown(window, { key: "k", metaKey: true });
    const dialog = await screen.findByRole("dialog", { name: "Command Palette" });
    fireEvent.click(await within(dialog).findByText("beads.sync"));

    const executeBtn = await within(dialog).findByRole("button", { name: "Execute POST" });
    expect(executeBtn).toBeDisabled();
    expect(postCalls).toHaveLength(0);

    fireEvent.click(within(dialog).getByRole("checkbox"));
    expect(executeBtn).not.toBeDisabled();
    fireEvent.click(executeBtn);

    await waitFor(() => {
      expect(postCalls).toHaveLength(1);
    });
    expect(postCalls[0]).toEqual({
      path: "/api/v1/beads/sync",
      method: "POST",
      body: "{}",
    });
    expect(await within(dialog).findByText(/"synced": true/)).toBeInTheDocument();
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
    let backfillOutput = "initial backfill line 1\ninitial backfill line 2";

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
            count: 2,
            panes: [
              { index: 0, window_index: 1, id: "%7", title: "claude-0", type: "cc", active: true },
              { index: 0, window_index: 2, id: "%8", title: "codex-0", type: "cod" },
            ],
          });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/%257/output") {
          outputFetchCount += 1;
          return reply({
            pane: "0",
            output: backfillOutput,
            lines: 2,
          });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/%258/output") {
          return reply({ pane: "%8", output: "other window output", lines: 1 });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/%258/stream") {
          return reply({ target: "%8", topic: "panes:%8" });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/checkpoints") {
          return reply({
            session_name: sessionId,
            checkpoints: [],
            count: 0,
          });
        }
        if (url.pathname === "/api/v1/handoffs") {
          return reply({
            action: "list",
            session: sessionId,
            sessions: [],
            handoffs: [],
            total_returned: 0,
          });
        }
        if (
          url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/%257/stream" &&
          (options?.method === "POST" || options?.method === "DELETE")
        ) {
          return reply(
            options.method === "POST"
              ? { target: "%7", topic: "panes:%7", message: "streaming started" }
              : { target: "%7", message: "streaming stopped" }
          );
        }
        if (
          url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/%258/input" &&
          options?.method === "POST"
        ) {
          return reply({ sent: true, pane: "0" });
        }
        if (
          url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/%258/interrupt" &&
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
      "GET /api/v1/sessions/my%2Fproj%231/panes/%257/output?lines=200"
    );

    // Subscribe to the physical pane's topic before starting its stream.
    await waitFor(() => {
      expect(requestedPaths).toContainEqual(
        expect.stringMatching(/^POST .*\/%257\/stream\?lease=[0-9a-f]{32}$/)
      );
      expect(
        socket.sentFrames.some(
          (frame) =>
            frame.type === "subscribe" &&
            Array.isArray(frame.data?.topics) &&
            (frame.data?.topics as string[]).includes("panes:%7")
        )
      ).toBe(true);
    });

    // Deliver live incremental pane.output event over WS on server-returned panes:%7 topic
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: "event",
          topic: "panes:%7",
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

    // Deliver full snapshot pane.output event over WS on server-returned panes:%7 topic
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: "event",
          topic: "panes:%7",
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

    // Unterminated prompts use the existing full-snapshot wire contract, so
    // both this source UI and the shipped embedded UI replace the same line.
    act(() => {
      socket.onmessage?.({
        data: [
          JSON.stringify({
            type: "event", topic: "panes:%7", event_type: "pane.output", seq: 13,
            data: { lines: ["recent history", "ready> "], is_full: true },
          }),
          JSON.stringify({
            type: "event", topic: "panes:%7", event_type: "pane.output", seq: 14,
            data: { lines: ["recent history", "ready> working"], is_full: true },
          }),
          JSON.stringify({
            type: "event", topic: "panes:%7", event_type: "pane.output", seq: 15,
            data: { lines: ["ordinary complete line"], is_full: false },
          }),
        ].join("\n"),
      });
    });
    expect(await screen.findByText("ready> working")).toBeInTheDocument();
    expect(screen.getByText("ordinary complete line")).toBeInTheDocument();
    expect(screen.queryByText("ready>", { exact: true })).not.toBeInTheDocument();

    // The global reset emitted when ingress overflows must refresh the REST
    // snapshot even though it has no pane topic.
    backfillOutput = "fresh authoritative snapshot";
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({ type: "stream.reset", reason: "broadcast_overflow", current_seq: 15 }),
      });
    });
    expect(await screen.findByText("fresh authoritative snapshot")).toBeInTheDocument();
    expect(screen.queryByText("ready> working")).not.toBeInTheDocument();
    expect(outputFetchCount).toBe(2);
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: "event", topic: "panes:%7", event_type: "pane.output", seq: 16,
          data: { lines: ["fresh authoritative snapshot", "resumed prompt"], is_full: true },
        }),
      });
    });
    expect(await screen.findByText("resumed prompt")).toBeInTheDocument();
    expect(screen.getByText("fresh authoritative snapshot")).toBeInTheDocument();

    // Select the other window's pane zero. Output and both controls must
    // follow %8, even though its window-local index is identical to %7's.
    fireEvent.click(screen.getByRole("button", { name: /Pane 2\.0/ }));
    expect(await screen.findByText("other window output")).toBeInTheDocument();
    expect(screen.queryByText("resumed prompt")).not.toBeInTheDocument();
    await waitFor(() => {
      const first = requestedPaths.find((path) =>
        path.startsWith("POST /api/v1/sessions/my%2Fproj%231/panes/%257/stream?lease=")
      );
      const next = requestedPaths.find((path) =>
        path.startsWith("POST /api/v1/sessions/my%2Fproj%231/panes/%258/stream?lease=")
      );
      expect(first).toBeDefined();
      expect(next).toBeDefined();
      expect(requestedPaths).toContain(first!.replace("POST ", "DELETE "));
      expect(first!.split("lease=")[1]).not.toBe(next!.split("lease=")[1]);
    });

    fireEvent.change(screen.getByPlaceholderText("Send a prompt to this pane..."), {
      target: { value: "status check" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => {
      expect(requestedPaths).toContain(
        "POST /api/v1/sessions/my%2Fproj%231/panes/%258/input"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Interrupt" }));
    await waitFor(() => {
      expect(requestedPaths).toContain(
        "POST /api/v1/sessions/my%2Fproj%231/panes/%258/interrupt"
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
        if (url.pathname === "/api/v1/handoffs") {
          return reply({
            action: "list",
            session: "offline-proj",
            sessions: [],
            handoffs: [],
            total_returned: 0,
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

        if (url.pathname === "/api/v1/incidents") {
          return reply({
            success: true,
            incidents: [],
            count: 0,
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

        if (
          url.pathname === "/api/v1/ensemble/runs" &&
          (options?.method ?? "GET") === "GET"
        ) {
          return reply({
            runs: [],
            count: 0,
          });
        }

        if (
          url.pathname === "/api/v1/jobs/" &&
          (options?.method ?? "GET") === "GET"
        ) {
          return reply({
            count: 0,
            execution: {
              accepting: true,
              max_concurrent: 4,
              owned: 0,
              preparing: 0,
              queue_capacity: 16,
              queued: 0,
              running: 0,
              waiting_for_resources: 0,
            },
            jobs: [],
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

        if (url.pathname === "/api/v1/handoffs") {
          return reply({
            action: "list",
            session: sessionId,
            sessions: [],
            handoffs: [],
            total_returned: 0,
          });
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
        if (url.pathname === "/api/v1/jobs/") {
          return reply({
            count: 2,
            execution: {
              accepting: true,
              max_concurrent: 4,
              owned: 1,
              preparing: 0,
              queue_capacity: 16,
              queued: 0,
              running: 1,
              waiting_for_resources: 0,
            },
            jobs: [
              {
                id: "job-active-1",
                type: "ensemble_spawn",
                status: "running",
                progress: 40,
                created_at: "2026-11-14T03:00:00Z",
                updated_at: "2026-11-14T03:00:05Z",
              },
              {
                id: "job-done-2",
                type: "swarm_spawn",
                status: "completed",
                progress: 100,
                created_at: "2026-11-14T02:55:00Z",
                updated_at: "2026-11-14T02:56:00Z",
              },
            ],
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
    expect(await within(pulseRegion).findByText(/2 recent jobs/)).toBeInTheDocument();

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
    const requestedPaths: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input));
        requestedPaths.push(url.pathname);
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
      expect(within(pulseRegion).getAllByText("Unavailable")).toHaveLength(4);
    });
    expect(requestedPaths).not.toContain("/api/v1/doctor");
  });
});

describe("AgentsPage fleet spawn, broadcast send, and interrupt controls", () => {
  it("spawns multi-harness agents from an empty roster, renders OpenCode/Ollama labels, broadcasts prompts, and interrupts panes", async () => {
    const sessionId = "my/proj";
    const encodedSession = encodeURIComponent(sessionId);

    const sessions: Array<{ name: string; status: string }> = [];
    const roster: Array<{
      id: string;
      session_id: string;
      name: string;
      type: string;
      tmux_pane_id: string;
      status: string;
    }> = [];
    const recordedPosts: Array<{
      path: string;
      body: Record<string, unknown>;
    }> = [];

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

        if (url.pathname === "/api/v1/sessions" && method === "GET") {
          return reply({ sessions, count: sessions.length });
        }

        if (
          url.pathname === `/api/v1/sessions/${encodedSession}/agents` &&
          method === "GET"
        ) {
          return reply({
            session_id: sessionId,
            agents: roster,
            count: roster.length,
          });
        }

        if (
          url.pathname === `/api/v1/sessions/${encodedSession}/agents/spawn` &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          recordedPosts.push({ path: url.pathname, body: parsed });
          sessions.splice(0, sessions.length, { name: sessionId, status: "active" });
          roster.splice(
            0,
            roster.length,
            {
              id: "%11",
              session_id: sessionId,
              name: "JadeFox",
              type: "oc",
              tmux_pane_id: "%11",
              status: "active",
            },
            {
              id: "%12",
              session_id: sessionId,
              name: "LocalRunner",
              type: "ollama",
              tmux_pane_id: "%12",
              status: "idle",
            }
          );
          return reply({
            session: sessionId,
            agents: [
              { pane: "1", agent_type: "oc", title: "my/proj__oc_1" },
              { pane: "2", agent_type: "cc", title: "my/proj__cc_1" },
            ],
          });
        }

        if (
          url.pathname === `/api/v1/sessions/${encodedSession}/agents/send` &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          recordedPosts.push({ path: url.pathname, body: parsed });
          return reply({
            session: sessionId,
            targets: ["%11"],
            successful: ["%11"],
            failed: [],
          });
        }

        if (
          url.pathname === `/api/v1/sessions/${encodedSession}/agents/interrupt` &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          recordedPosts.push({ path: url.pathname, body: parsed });
          return reply({
            session: sessionId,
            interrupted: ["%11"],
            ready_for_input: ["%11"],
            failed: [],
          });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <AgentsPage />
      </QueryClientProvider>
    );

    // Empty state renders AND Fleet Operations remains accessible to bootstrap
    expect(await screen.findByText(/No agents registered/)).toBeInTheDocument();
    const fleetOps = screen.getByRole("region", { name: "Fleet Operations" });
    expect(fleetOps).toBeInTheDocument();

    // 1. Multi-harness spawn into URL-encoded session "my/proj"
    fireEvent.change(within(fleetOps).getByLabelText("Target Session"), {
      target: { value: sessionId },
    });
    fireEvent.change(within(fleetOps).getByLabelText("Claude Code (cc)"), {
      target: { value: "1" },
    });
    fireEvent.change(within(fleetOps).getByLabelText("OpenCode (oc)"), {
      target: { value: "1" },
    });
    fireEvent.change(within(fleetOps).getByLabelText("Preset (optional)"), {
      target: { value: "review-pair" },
    });
    fireEvent.change(within(fleetOps).getByLabelText("Goal Label (optional)"), {
      target: { value: "wave-1" },
    });
    fireEvent.click(
      within(fleetOps).getByLabelText("Wait until agents are ready")
    );
    fireEvent.click(within(fleetOps).getByRole("button", { name: "Spawn Agents" }));

    expect(
      await screen.findByText(/Spawned 2 agent panes in session "my\/proj"\./)
    ).toBeInTheDocument();
    expect(recordedPosts[0]).toEqual({
      path: "/api/v1/sessions/my%2Fproj/agents/spawn",
      body: {
        cc_count: 1,
        oc_count: 1,
        preset: "review-pair",
        label: "wave-1",
        wait_ready: true,
      },
    });

    // Roster refreshes automatically and renders OpenCode + Ollama labels
    expect(await screen.findByText("JadeFox")).toBeInTheDocument();
    expect(screen.getByText("LocalRunner")).toBeInTheDocument();
    expect(screen.getByText("OpenCode: 1")).toBeInTheDocument();
    expect(screen.getByText("Ollama: 1")).toBeInTheDocument();

    // 2. Broadcast prompt filtered to OpenCode ("oc")
    fireEvent.change(within(fleetOps).getByLabelText("Target Agent Type"), {
      target: { value: "oc" },
    });
    fireEvent.change(
      within(fleetOps).getByLabelText("Prompt / Follow-up Message"),
      {
        target: { value: "Audit error handling in server_agents.go" },
      }
    );
    fireEvent.click(within(fleetOps).getByRole("button", { name: "Send Prompt" }));

    expect(
      await screen.findByText(/Sent prompt to 1 pane in session "my\/proj"\./)
    ).toBeInTheDocument();
    expect(recordedPosts[1]).toEqual({
      path: "/api/v1/sessions/my%2Fproj/agents/send",
      body: {
        message: "Audit error handling in server_agents.go",
        agent_types: ["oc"],
      },
    });

    // 3. One-click Interrupt with force=true and optional follow-up message
    fireEvent.change(
      within(fleetOps).getByLabelText("Prompt / Follow-up Message"),
      {
        target: { value: "Pause and report status" },
      }
    );
    fireEvent.click(
      within(fleetOps).getByLabelText("Force interrupt idle panes")
    );
    fireEvent.click(
      within(fleetOps).getByRole("button", { name: "Interrupt Agents" })
    );

    expect(
      await screen.findByText(
        /Interrupted 1 pane in session "my\/proj" \(1 ready for input\)\./
      )
    ).toBeInTheDocument();
    expect(recordedPosts[2]).toEqual({
      path: "/api/v1/sessions/my%2Fproj/agents/interrupt",
      body: {
        agent_types: ["oc"],
        message: "Pause and report status",
        force: true,
      },
    });
  });

  it("validates required session and count/message fields before dispatching fleet mutations", async () => {
    const fetchSpy = vi.fn(
      async (input: RequestInfo | URL, _init?: RequestInit) => {
        const url = new URL(String(input));
        if (url.pathname === "/api/v1/sessions") {
          return new Response(
            JSON.stringify({ success: true, sessions: [], count: 0 }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        throw new Error(`Unexpected request: ${url.pathname}`);
      }
    );
    vi.stubGlobal("fetch", fetchSpy);

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <AgentsPage />
      </QueryClientProvider>
    );

    const fleetOps = await screen.findByRole("region", {
      name: "Fleet Operations",
    });

    fireEvent.click(within(fleetOps).getByRole("button", { name: "Spawn Agents" }));
    expect(
      within(fleetOps).getByText("Target session name is required to spawn agents.")
    ).toBeInTheDocument();

    fireEvent.change(within(fleetOps).getByLabelText("Target Session"), {
      target: { value: "alpha" },
    });
    fireEvent.click(within(fleetOps).getByRole("button", { name: "Spawn Agents" }));
    expect(
      within(fleetOps).getByText(
        "Specify at least one agent count greater than 0 or a preset name."
      )
    ).toBeInTheDocument();

    fireEvent.click(within(fleetOps).getByRole("button", { name: "Send Prompt" }));
    expect(
      within(fleetOps).getByText("Enter a prompt message to send.")
    ).toBeInTheDocument();

    expect(
      fetchSpy.mock.calls.every(([, init]) => (init?.method ?? "GET") === "GET")
    ).toBe(true);
  });
});

describe("EnsemblePage durable async background jobs (/api/v1/jobs/)", () => {
  it("queues ensemble_spawn and ensemble_synthesize background jobs and cancels active jobs", async () => {
    const jobs: Array<{
      id: string;
      type: string;
      status: string;
      progress?: number;
      result?: Record<string, unknown>;
      error?: string;
      created_at: string;
      updated_at: string;
    }> = [
      {
        id: "job-prev-failed",
        type: "swarm_spawn",
        status: "failed",
        progress: 0,
        result: { session: "old-swarm" },
        error: "tmux server unavailable",
        created_at: "2026-11-14T02:00:00Z",
        updated_at: "2026-11-14T02:00:05Z",
      },
    ];
    const jobCreateBodies: Array<Record<string, unknown>> = [];
    const cancelledJobIds: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
        const reply = (payload: object, status = 200) =>
          new Response(JSON.stringify({ success: true, ...payload }), {
            status,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/ensemble/modes") {
          return reply({
            total_modes: 1,
            core_modes: 1,
            advanced_modes: 0,
            experimental_modes: 0,
            filtered_count: 1,
            default_tier: "core",
            categories: [{ code: "A", name: "Formal", mode_count: 1 }],
            modes: [
              {
                id: "deductive",
                code: "A1",
                name: "Deductive Proof",
                category: { code: "A", name: "Formal" },
                tier: "core",
                short_desc: "Stepwise invariant verification",
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/ensemble/presets") {
          return reply({
            count: 1,
            presets: [
              {
                name: "project-diagnosis",
                display: "Project Diagnosis",
                description: "Broad architectural diagnosis",
                modes: ["A1"],
                mode_count: 1,
                synthesis: { strategy: "consensus" },
                budget: { total_tokens: 30000 },
                allow_advanced: false,
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/sessions") {
          return reply({ sessions: [{ name: "ens-async", status: "active" }] });
        }

        if (url.pathname === "/api/v1/ensemble/sessions/ens-async") {
          return reply({
            ensemble: {
              session: "ens-async",
              question: "Audit durability fences in jobs_dispatch.go",
              preset: "project-diagnosis",
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
              synthesis: { strategy: "consensus", status: "ready" },
              budget: { total_tokens: 30000 },
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

        if (url.pathname === "/api/v1/ensemble/runs" && method === "GET") {
          return reply({
            runs: [],
            count: 0,
          });
        }

        if (url.pathname === "/api/v1/jobs/" && method === "GET") {
          const running = jobs.filter((j) => j.status === "running").length;
          const queued = jobs.filter((j) => j.status === "pending").length;
          return reply({
            count: jobs.length,
            execution: {
              accepting: true,
              max_concurrent: 4,
              owned: running + queued,
              preparing: 0,
              queue_capacity: 16,
              queued,
              running,
              waiting_for_resources: 0,
            },
            jobs,
          });
        }

        if (url.pathname === "/api/v1/jobs/" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          jobCreateBodies.push(parsed);
          const newId =
            parsed.type === "ensemble_spawn"
              ? "job-spawn-101"
              : "job-synth-202";
          const created = {
            id: newId,
            type: String(parsed.type),
            status: "running",
            progress: 50,
            result: {
              session: String(parsed.session ?? ""),
              progress_msg: "Dispatching cognitive modes",
            },
            created_at: "2026-11-14T03:10:00Z",
            updated_at: "2026-11-14T03:10:01Z",
          };
          jobs.unshift(created);
          return reply({ job: created }, 202);
        }

        if (
          url.pathname.startsWith("/api/v1/jobs/") &&
          method === "DELETE"
        ) {
          const jobId = decodeURIComponent(
            url.pathname.slice("/api/v1/jobs/".length)
          );
          cancelledJobIds.push(jobId);
          const target = jobs.find((j) => j.id === jobId);
          if (target) {
            target.status = "cancelled";
          }
          return reply({ job: target });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <EnsemblePage />
      </QueryClientProvider>
    );

    const jobsCard = await screen.findByRole("region", {
      name: "Async Ensemble & Swarm Jobs",
    });
    expect(
      await within(jobsCard).findByText("job-prev-failed")
    ).toBeInTheDocument();
    expect(
      within(jobsCard).getByText("tmux server unavailable")
    ).toBeInTheDocument();

    // 1. Queue Async Spawn
    fireEvent.change(screen.getByLabelText("Analysis Question"), {
      target: { value: "Audit durability fences in jobs_dispatch.go" },
    });
    fireEvent.change(screen.getByLabelText("Session Name"), {
      target: { value: "ens-async" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Queue Async Spawn" }));

    expect(
      await screen.findByText(
        /Queued async ensemble_spawn job "job-spawn-101" for session "ens-async"\./
      )
    ).toBeInTheDocument();
    expect(jobCreateBodies[0]).toEqual({
      type: "ensemble_spawn",
      session: "ens-async",
      params: {
        session: "ens-async",
        preset: "project-diagnosis",
        question: "Audit durability fences in jobs_dispatch.go",
        agents: "cc=2,cod=1",
        allow_advanced: false,
      },
    });

    // 2. Queue Async Synthesis from Live Session Inspector
    expect(await screen.findByText("[A1] Deductive Proof")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Force partial synthesis"));
    fireEvent.click(
      screen.getByRole("button", { name: "Queue Async Synthesis" })
    );

    expect(
      await screen.findByText(
        /Queued async ensemble_synthesize job "job-synth-202" for session "ens-async"\./
      )
    ).toBeInTheDocument();
    expect(jobCreateBodies[1]).toEqual({
      type: "ensemble_synthesize",
      session: "ens-async",
      params: {
        session: "ens-async",
        force: true,
        output_format: "markdown",
      },
    });

    // 3. Cancel active running job
    const synthRow = await within(jobsCard).findByTestId(
      "job-row-job-synth-202"
    );
    expect(within(synthRow).getByText("50%")).toBeInTheDocument();
    fireEvent.click(
      within(synthRow).getByRole("button", { name: "Cancel Job" })
    );

    expect(
      await screen.findByText(/Cancelled async job "job-synth-202"\./)
    ).toBeInTheDocument();
    expect(cancelledJobIds).toEqual(["job-synth-202"]);
  });
});

describe("OverviewPage on-demand System Doctor & Async Jobs pulse", () => {
  it("never calls /api/v1/doctor on mount, displays active/recent jobs pulse, and renders doctor checks when Run Doctor Check is clicked", async () => {
    let doctorCalls = 0;

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
            sessions: [{ id: "s1", name: "prod-swarm", status: "active" }],
          });
        }
        if (url.pathname === "/api/v1/health/backpressure") {
          return reply({
            timestamp: "2026-11-14T03:00:00Z",
            backpressure: {
              captured_at: "2026-11-14T03:00:00Z",
              decision: "ok",
              surfaces: [],
            },
          });
        }
        if (url.pathname === "/api/v1/attention/digest") {
          return new Response(
            JSON.stringify({
              cursor_start: 1,
              cursor_end: 5,
              event_count: 2,
              by_category: {},
              by_actionability: { action_required: 0 },
              buckets: { action_required: [], interesting: [], background: [] },
              suppressed: { total: 0 },
              summary: "nominal",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/worktrees") {
          return reply({ project_dir: "/dp/ntm", worktrees: [], count: 0 });
        }
        if (url.pathname === "/api/v1/jobs/") {
          return reply({
            count: 3,
            execution: {
              accepting: true,
              max_concurrent: 4,
              owned: 2,
              preparing: 0,
              queue_capacity: 16,
              queued: 1,
              running: 1,
              waiting_for_resources: 0,
            },
            jobs: [
              {
                id: "j1",
                type: "ensemble_spawn",
                status: "running",
                created_at: "2026-11-14T03:00:00Z",
                updated_at: "2026-11-14T03:00:01Z",
              },
              {
                id: "j2",
                type: "ensemble_synthesize",
                status: "pending",
                created_at: "2026-11-14T03:00:02Z",
                updated_at: "2026-11-14T03:00:02Z",
              },
              {
                id: "j3",
                type: "swarm_spawn",
                status: "completed",
                progress: 100,
                created_at: "2026-11-14T02:50:00Z",
                updated_at: "2026-11-14T02:51:00Z",
              },
            ],
          });
        }
        if (url.pathname === "/api/v1/doctor") {
          doctorCalls += 1;
          return reply({
            timestamp: "2026-11-14T03:05:00Z",
            overall: "warning",
            warnings: 1,
            errors: 0,
            dependencies: [
              {
                name: "tmux",
                required: true,
                installed: true,
                status: "ok",
                version: "3.4",
                path: "/usr/bin/tmux",
              },
              {
                name: "cass",
                required: false,
                installed: false,
                status: "warning",
                message: "cass not installed (optional)",
              },
            ],
            daemons: [
              {
                name: "agent-mail",
                port: 8765,
                running: true,
                status: "ok",
                message: "listening on port 8765",
              },
            ],
            guard_hook: {
              name: "guard-hook",
              status: "ok",
              degraded_runs: 0,
            },
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

    expect(await screen.findByText("prod-swarm")).toBeInTheDocument();
    const pulseRegion = screen.getByLabelText("Overview pulse");
    expect(
      await within(pulseRegion).findByText(/3 recent jobs/)
    ).toBeInTheDocument();
    expect(within(pulseRegion).getByText("2")).toBeInTheDocument();

    // Doctor was NOT called automatically on mount
    expect(doctorCalls).toBe(0);

    // Click Run Doctor Check
    const doctorSection = screen.getByRole("region", { name: "System Doctor" });
    fireEvent.click(
      within(doctorSection).getByRole("button", { name: "Run Doctor Check" })
    );

    expect(await screen.findByTestId("doctor-overall-status")).toHaveTextContent(
      "warning"
    );
    expect(doctorCalls).toBe(1);
    expect(within(doctorSection).getByText("Passed: 3")).toBeInTheDocument();
    expect(within(doctorSection).getByText("Warnings: 1")).toBeInTheDocument();
    expect(within(doctorSection).getByText("Failed: 0")).toBeInTheDocument();
    expect(within(doctorSection).getByText("tmux")).toBeInTheDocument();
    expect(within(doctorSection).getByText(/v3\.4 · \/usr\/bin\/tmux/)).toBeInTheDocument();
    expect(
      within(doctorSection).getByText("cass not installed (optional)")
    ).toBeInTheDocument();
    expect(
      within(doctorSection).getByText("listening on port 8765")
    ).toBeInTheDocument();
    expect(within(doctorSection).getByText("guard-hook")).toBeInTheDocument();
  });
});

describe("EnsemblePage analysis workbench (estimate, runs, compare & provenance)", () => {
  it("estimates token budgets with substitution advice, compares saved runs side-by-side, and inspects finding provenance chains", async () => {
    const estimateBodies: Array<Record<string, unknown>> = [];
    const compareBodies: Array<Record<string, unknown>> = [];
    const provenanceUrls: string[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
        const reply = (payload: object, status = 200) =>
          new Response(JSON.stringify({ success: true, ...payload }), {
            status,
            headers: { "Content-Type": "application/json" },
          });

        if (url.pathname === "/api/v1/ensemble/modes") {
          return reply({
            total_modes: 2,
            core_modes: 2,
            advanced_modes: 0,
            experimental_modes: 0,
            default_tier: "core",
            categories: [{ code: "A", name: "Formal", mode_count: 2 }],
            modes: [
              {
                id: "deductive",
                code: "A1",
                name: "Deductive Proof",
                category: { code: "A", name: "Formal" },
                tier: "core",
                short_desc: "Stepwise invariant verification",
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/ensemble/presets") {
          return reply({
            count: 1,
            presets: [
              {
                name: "project-diagnosis",
                display: "Project Diagnosis",
                description: "Broad architectural diagnosis",
                modes: ["A1"],
                mode_count: 1,
                synthesis: { strategy: "consensus" },
                budget: { total_tokens: 30000 },
                allow_advanced: false,
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/sessions") {
          return reply({ sessions: [{ name: "ens-alpha", status: "active" }] });
        }

        if (url.pathname === "/api/v1/jobs/" && method === "GET") {
          return reply({
            count: 0,
            execution: {
              accepting: true,
              max_concurrent: 4,
              owned: 0,
              preparing: 0,
              queue_capacity: 16,
              queued: 0,
              running: 0,
              waiting_for_resources: 0,
            },
            jobs: [],
          });
        }

        if (url.pathname === "/api/v1/ensemble/runs" && method === "GET") {
          return reply({
            count: 2,
            runs: [
              {
                run_id: "ens-alpha",
                session_name: "ens-alpha",
                source: "session",
                question: "Audit durability fences",
                preset_used: "project-diagnosis",
                status: "ready",
                total_modes: 2,
                completed_modes: 2,
                pending_modes: 0,
                error_modes: 0,
                has_synthesis: true,
                created_at: "2026-11-15T01:00:00Z",
              },
              {
                run_id: "ckpt-beta-01",
                session_name: "ens-beta",
                source: "checkpoint",
                question: "Audit durability fences v2",
                status: "complete",
                total_modes: 3,
                completed_modes: 3,
                pending_modes: 0,
                error_modes: 0,
                has_synthesis: false,
                created_at: "2026-11-15T01:10:00Z",
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/ensemble/estimate" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          estimateBodies.push(parsed);
          return reply({
            generated_at: "2026-11-15T01:15:00Z",
            over_budget: true,
            modes: [
              {
                mode_id: "deductive",
                mode_code: "A1",
                mode_name: "Deductive Logic",
                tier: "core",
                token_estimate: 4200,
                value_score: 0.88,
                value_per_token: 0.00021,
              },
              {
                mode_id: "failure-mode",
                mode_code: "E1",
                mode_name: "Failure Mode Analysis",
                tier: "core",
                token_estimate: 4800,
                value_score: 0.79,
                value_per_token: 0.00016,
              },
            ],
            budget: {
              max_tokens_per_mode: 4000,
              max_total_tokens: 5000,
              estimated_mode_tokens: 9000,
              estimated_total_tokens: 12500,
              mode_count: 2,
              budget_override: 5000,
            },
            warnings: ["Estimated total 12500 exceeds budget cap 5000"],
            suggestions: [
              {
                replace_mode_id: "failure-mode",
                replace_mode_name: "Failure Mode Analysis",
                replace_tokens: 4800,
                suggested_mode_id: "edge-case",
                suggested_mode_name: "Edge Case Probe",
                suggested_tokens: 2600,
                savings_tokens: 2200,
              },
            ],
          });
        }

        if (url.pathname === "/api/v1/ensemble/compare" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          compareBodies.push(parsed);
          return reply({
            generated_at: "2026-11-15T01:16:00Z",
            run_a: String(parsed.run_a),
            run_b: String(parsed.run_b),
            summary: "+1 mode, +1 finding, -1 finding",
            unchanged: false,
            changed_sections: ["modes", "findings"],
            result: {
              run_a: String(parsed.run_a),
              run_b: String(parsed.run_b),
              generated_at: "2026-11-15T01:16:00Z",
              summary: "+1 mode, +1 finding, -1 finding",
              mode_diff: {
                added: ["systems-thinking"],
                removed: [],
                unchanged: ["deductive", "failure-mode"],
                added_count: 1,
                removed_count: 0,
                unchanged_count: 2,
              },
              findings_diff: {
                new: [
                  {
                    finding_id: "find-new-99",
                    mode_id: "systems-thinking",
                    text: "Queue backpressure propagates to SSE hub",
                    impact: "high",
                    confidence: 0.91,
                  },
                ],
                missing: [
                  {
                    finding_id: "find-old-01",
                    mode_id: "deductive",
                    text: "Stale lock timeout hypothesis",
                    impact: "low",
                    confidence: 0.55,
                  },
                ],
                changed: [],
                unchanged: [],
                new_count: 1,
                missing_count: 1,
                changed_count: 0,
                unchanged_count: 2,
              },
              conclusion_diff: {
                synthesis_changed: false,
              },
              contribution_diff: {
                score_deltas: [],
                rank_changes: [],
                overlap_delta: 0,
                diversity_delta: 0,
              },
            },
          });
        }

        if (
          url.pathname === "/api/v1/ensemble/sessions/ens-alpha/provenance" &&
          method === "GET"
        ) {
          provenanceUrls.push(`${url.pathname}${url.search}`);
          return reply({
            session: "ens-alpha",
            finding_filter: url.searchParams.get("finding") ?? undefined,
            count: 1,
            stats: {
              total_findings: 3,
              active_findings: 2,
              merged_findings: 1,
              filtered_count: 0,
              cited_count: 1,
              mode_breakdown: { deductive: 2, "failure-mode": 1 },
            },
            chains: [
              {
                finding_id: "f-ded-01",
                source_mode: "deductive",
                context_hash: "ctx-123",
                original_text: "Nested SQLite rows scan holds connection lock",
                current_text: "Nested SQLite rows scan holds connection lock",
                impact: "high",
                confidence: 0.93,
                synthesis_citations: ["synthesis:primary-thesis"],
                steps: [
                  {
                    stage: "discovery",
                    action: "recorded",
                    details: "Extracted from deductive mode output",
                    timestamp: "2026-11-15T01:01:00Z",
                  },
                  {
                    stage: "synthesis",
                    action: "cited",
                    details: "Referenced in consensus report",
                    timestamp: "2026-11-15T01:03:00Z",
                  },
                ],
                created_at: "2026-11-15T01:01:00Z",
                updated_at: "2026-11-15T01:03:00Z",
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
        <EnsemblePage />
      </QueryClientProvider>
    );

    const workbench = await screen.findByRole("region", {
      name: "Ensemble Analysis Workbench",
    });

    // Saved runs loaded from GET /api/v1/ensemble/runs
    const alphaRow = await within(workbench).findByTestId(
      "saved-run-ens-alpha"
    );
    const betaRow = await within(workbench).findByTestId(
      "saved-run-ckpt-beta-01"
    );
    expect(within(alphaRow).getByText("synthesized")).toBeInTheDocument();

    // 1. Token Budget Estimator (explicit modes + cap override)
    fireEvent.change(screen.getByLabelText("Analysis Question"), {
      target: { value: "Audit durability fences" },
    });
    const estimator = within(workbench).getByRole("region", {
      name: "Token Budget Estimator",
    });
    fireEvent.change(within(estimator).getByLabelText("Estimate Target"), {
      target: { value: "modes" },
    });
    fireEvent.change(
      within(estimator).getByLabelText("Explicit Mode IDs / Codes"),
      {
        target: { value: "deductive, failure-mode" },
      }
    );
    fireEvent.change(within(estimator).getByLabelText("Budget Cap Override"), {
      target: { value: "5000" },
    });
    fireEvent.click(
      within(estimator).getByRole("button", { name: "Estimate Budget" })
    );

    expect(
      await within(estimator).findByTestId("estimate-budget-status")
    ).toHaveTextContent("Over Budget");
    expect(estimateBodies[0]).toEqual({
      modes: ["deductive", "failure-mode"],
      budget_total: 5000,
      question: "Audit durability fences",
    });
    expect(
      within(estimator).getByText("Estimated total 12500 exceeds budget cap 5000")
    ).toBeInTheDocument();
    expect(
      within(estimator).getByText(/Edge Case Probe/)
    ).toBeInTheDocument();

    // 2. Compare Saved Runs side-by-side
    fireEvent.click(within(alphaRow).getByRole("button", { name: "Run A" }));
    fireEvent.click(within(betaRow).getByRole("button", { name: "Run B" }));
    const compareRegion = within(workbench).getByRole("region", {
      name: "Saved Runs & Comparison",
    });
    fireEvent.click(
      within(compareRegion).getByRole("button", { name: "Compare Runs" })
    );

    expect(
      await within(compareRegion).findByText(
        "+1 mode, +1 finding, -1 finding"
      )
    ).toBeInTheDocument();
    expect(compareBodies[0]).toEqual({
      run_a: "ens-alpha",
      run_b: "ckpt-beta-01",
    });
    expect(
      within(compareRegion).getByText(
        /\+ \[systems-thinking\] Queue backpressure propagates to SSE hub/
      )
    ).toBeInTheDocument();

    // 3. Finding Provenance Chain Inspector (with finding filter)
    const provRegion = within(workbench).getByRole("region", {
      name: "Finding Provenance Inspector",
    });
    fireEvent.change(within(provRegion).getByLabelText("Finding ID Filter"), {
      target: { value: "f-ded" },
    });
    fireEvent.click(
      within(alphaRow).getByRole("button", { name: "Provenance" })
    );

    const chainCard = await within(provRegion).findByTestId(
      "provenance-chain-f-ded-01"
    );
    expect(provenanceUrls).toEqual([
      "/api/v1/ensemble/sessions/ens-alpha/provenance?finding=f-ded",
    ]);
    expect(within(provRegion).getByText("Cited: 1")).toBeInTheDocument();
    expect(
      within(chainCard).getByText("Nested SQLite rows scan holds connection lock")
    ).toBeInTheDocument();
    expect(
      within(chainCard).getByText("synthesis:primary-thesis")
    ).toBeInTheDocument();
    expect(
      within(chainCard).getByText(/Extracted from deductive mode output/)
    ).toBeInTheDocument();
  });
});
