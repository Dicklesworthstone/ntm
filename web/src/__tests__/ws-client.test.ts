/**
 * WebSocket Client Tests
 *
 * Tests for connection state and reconnection behavior.
 */

import { QueryClient } from "@tanstack/react-query";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { browserNavigation } from "../lib/api/client";
import { handleWsEvent } from "../lib/hooks/use-query";
import {
  applyPaneOutputEvent,
  NtmWebSocket,
  type ConnectionState,
} from "../lib/ws/client";

// Mock WebSocket
class MockWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances: MockWebSocket[] = [];

  readyState = MockWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;

  constructor(public url: string) {
    MockWebSocket.instances.push(this);
    // Simulate async connection
    setTimeout(() => {
      if (this.readyState === MockWebSocket.CONNECTING) {
        this.readyState = MockWebSocket.OPEN;
        this.onopen?.();
      }
    }, 10);
  }

  send = vi.fn();
  close = vi.fn((code?: number, reason?: string) => {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.({ code: code || 1000, reason: reason || "" });
  });
}

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
Object.defineProperty(global, "WebSocket", { value: MockWebSocket });

describe("NtmWebSocket", () => {
  let ws: NtmWebSocket;

  beforeEach(() => {
    MockWebSocket.instances = [];
    localStorageMock.clear();
    localStorageMock.setItem(
      "ntm-connection",
      JSON.stringify({ baseUrl: "http://localhost:8080" })
    );
    ws = new NtmWebSocket();
  });

  afterEach(() => {
    ws.disconnect();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  describe("connection state", () => {
    it("starts disconnected", () => {
      expect(ws.getState()).toBe("disconnected");
    });

    it("transitions to connecting on connect()", () => {
      ws.connect();
      expect(ws.getState()).toBe("connecting");
    });

    it("normalizes trailing slashes in the websocket URL", async () => {
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({ baseUrl: "http://localhost:8080///" })
      );

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      const mockWs = (ws as unknown as { ws: MockWebSocket }).ws;
      expect(mockWs.url).toBe("ws://localhost:8080/api/v1/ws");
    });

    it("notifies state handlers", async () => {
      const states: ConnectionState[] = [];
      ws.onStateChange((state) => states.push(state));

      ws.connect();

      // Wait for mock WebSocket to "connect"
      await new Promise((resolve) => setTimeout(resolve, 20));

      expect(states).toContain("disconnected");
      expect(states).toContain("connecting");
      expect(states).toContain("connected");
    });
  });

  describe("disconnect", () => {
    it("transitions to disconnected", async () => {
      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      ws.disconnect();
      expect(ws.getState()).toBe("disconnected");
    });

    it("clears subscriptions", async () => {
      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      const pending = ws.subscribe("sessions:*");
      const rejection = expect(pending).rejects.toThrow("WebSocket disconnected");
      ws.disconnect();

      // Internal state should be cleared
      expect(ws.getState()).toBe("disconnected");
      await rejection;
    });

    it("rejects pending subscription acknowledgements on disconnect", async () => {
      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      const pending = ws.subscribe("sessions:*");
      const rejection = expect(pending).rejects.toThrow("WebSocket disconnected");
      ws.disconnect();

      await rejection;
    });
  });

  describe("event handling", () => {
    it("calls event handlers on message", async () => {
      const events: unknown[] = [];
      ws.onEvent((event) => events.push(event));

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      // Simulate receiving an event
      const mockWs = (ws as unknown as { ws: MockWebSocket }).ws;
      mockWs.onmessage?.({
        data: JSON.stringify({
          type: "event",
          topic: "sessions:test",
          event_type: "session.created",
          data: { name: "test" },
          seq: 1,
        }),
      });

      expect(events.length).toBe(1);
      expect((events[0] as { topic: string }).topic).toBe("sessions:test");
    });

    it("tracks sequence numbers", async () => {
      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      const mockWs = (ws as unknown as { ws: MockWebSocket }).ws;
      mockWs.onmessage?.({
        data: JSON.stringify({
          type: "event",
          topic: "test",
          event_type: "test",
          data: {},
          seq: 42,
        }),
      });

      expect(ws.getLastSeq()).toBe(42);
    });

    it("parses newline-batched websocket frames", async () => {
      const events: unknown[] = [];
      ws.onEvent((event) => events.push(event));

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      const mockWs = (ws as unknown as { ws: MockWebSocket }).ws;
      mockWs.onmessage?.({
        data: [
          JSON.stringify({
            type: "event",
            topic: "sessions:test",
            event_type: "session.created",
            data: { name: "test" },
            seq: 7,
          }),
          JSON.stringify({
            type: "event",
            topic: "panes:test:0",
            event_type: "pane.output",
            data: { lines: ["hi"] },
            seq: 8,
          }),
        ].join("\n"),
      });

      expect(events).toHaveLength(2);
      expect((events[0] as { topic: string }).topic).toBe("sessions:test");
      expect((events[1] as { topic: string }).topic).toBe("panes:test:0");
      expect(ws.getLastSeq()).toBe(8);
    });
  });

  describe("error handling", () => {
    it("calls error handlers on error message", async () => {
      const errors: unknown[] = [];
      ws.onError((error) => errors.push(error));

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      const mockWs = (ws as unknown as { ws: MockWebSocket }).ws;
      mockWs.onmessage?.({
        data: JSON.stringify({
          type: "error",
          code: "AUTH_FAILED",
          message: "Invalid token",
        }),
      });

      expect(errors.length).toBe(1);
      expect((errors[0] as { code: string }).code).toBe("AUTH_FAILED");
    });
  });

  describe("one-time WS ticket auth & 401 redirect", () => {
    it("connects directly without ?token= or ?ticket= in local mode (empty authToken)", async () => {
      const fetchSpy = vi.fn();
      vi.stubGlobal("fetch", fetchSpy);

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      expect(fetchSpy).not.toHaveBeenCalled();
      expect(MockWebSocket.instances).toHaveLength(1);
      expect(MockWebSocket.instances[0].url).toBe("ws://localhost:8080/api/v1/ws");
      expect(MockWebSocket.instances[0].url).not.toContain("token=");
      expect(MockWebSocket.instances[0].url).not.toContain("ticket=");
    });

    it("exchanges POST /api/v1/auth/ws-ticket and connects with ?ticket= (never ?token=<secret>)", async () => {
      const rawSecret = "ntm-raw-secret-token-xyz";
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({
          baseUrl: "http://localhost:8080",
          authToken: rawSecret,
        })
      );

      const fetchSpy = vi.fn(async () =>
        new Response(
          JSON.stringify({
            success: true,
            ticket: "opaque/ticket+1",
            expires_in_sec: 30,
            timestamp: "2026-11-12T00:00:00Z",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        )
      );
      vi.stubGlobal("fetch", fetchSpy);

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 30));

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [ticketUrl, init] = fetchSpy.mock.calls[0] as unknown as [
        string,
        RequestInit,
      ];
      expect(ticketUrl).toBe("http://localhost:8080/api/v1/auth/ws-ticket");
      expect(init.method).toBe("POST");
      expect((init.headers as Record<string, string>).Authorization).toBe(
        `Bearer ${rawSecret}`
      );

      expect(MockWebSocket.instances).toHaveLength(1);
      expect(MockWebSocket.instances[0].url).toBe(
        "ws://localhost:8080/api/v1/ws?ticket=opaque%2Fticket%2B1"
      );
      expect(MockWebSocket.instances[0].url).not.toContain("token=");
      expect(MockWebSocket.instances[0].url).not.toContain(rawSecret);
    });

    it("obtains a fresh single-use ticket on every reconnect attempt", async () => {
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({
          baseUrl: "http://localhost:8080",
          authToken: "secret-reconnect",
        })
      );
      // Speed up reconnect delay for deterministic testing
      (ws as unknown as { baseReconnectDelay: number }).baseReconnectDelay = 15;

      let ticketCounter = 0;
      const fetchSpy = vi.fn(async () => {
        ticketCounter += 1;
        return new Response(
          JSON.stringify({
            success: true,
            ticket: `single-use-ticket-${ticketCounter}`,
            expires_in_sec: 30,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      });
      vi.stubGlobal("fetch", fetchSpy);

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 30));
      expect(MockWebSocket.instances).toHaveLength(1);
      expect(MockWebSocket.instances[0].url).toBe(
        "ws://localhost:8080/api/v1/ws?ticket=single-use-ticket-1"
      );

      // Simulate abnormal drop (1006) -> triggers reconnect with fresh ticket
      MockWebSocket.instances[0].close(1006, "Abnormal closure");
      await new Promise((resolve) => setTimeout(resolve, 50));

      expect(fetchSpy).toHaveBeenCalledTimes(2);
      expect(MockWebSocket.instances).toHaveLength(2);
      expect(MockWebSocket.instances[1].url).toBe(
        "ws://localhost:8080/api/v1/ws?ticket=single-use-ticket-2"
      );
    });

    it("guards against duplicate concurrent connect() and aborts cleanly if disconnect() runs mid-exchange", async () => {
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({
          baseUrl: "http://localhost:8080",
          authToken: "secret-race",
        })
      );

      let resolveFetch!: (res: Response) => void;
      const fetchSpy = vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            resolveFetch = resolve;
          })
      );
      vi.stubGlobal("fetch", fetchSpy);

      ws.connect();
      ws.connect(); // Duplicate concurrent call must be ignored
      expect(fetchSpy).toHaveBeenCalledTimes(1);

      // Disconnect while ticket exchange is still in flight
      ws.disconnect();
      resolveFetch(
        new Response(
          JSON.stringify({ success: true, ticket: "stale-ticket" }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        )
      );
      await new Promise((resolve) => setTimeout(resolve, 20));

      expect(ws.getState()).toBe("disconnected");
      expect(MockWebSocket.instances).toHaveLength(0);
    });

    it("redirects to /connect on 401 during ticket exchange without looping when already on /connect", async () => {
      localStorageMock.setItem(
        "ntm-connection",
        JSON.stringify({
          baseUrl: "http://localhost:8080",
          authToken: "invalid-secret",
        })
      );

      const pathnameSpy = vi
        .spyOn(browserNavigation, "getPathname")
        .mockReturnValue("/sessions");
      const redirectSpy = vi
        .spyOn(browserNavigation, "redirect")
        .mockImplementation(() => {});

      vi.stubGlobal(
        "fetch",
        vi.fn(async () =>
          new Response(
            JSON.stringify({ success: false, error: "Unauthorized" }),
            { status: 401, headers: { "Content-Type": "application/json" } }
          )
        )
      );

      const errors: Array<{ code: string }> = [];
      ws.onError((e) => errors.push(e));

      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));

      expect(ws.getState()).toBe("disconnected");
      expect(MockWebSocket.instances).toHaveLength(0);
      expect(errors.map((e) => e.code)).toContain("UNAUTHORIZED");
      expect(redirectSpy).toHaveBeenCalledTimes(1);
      expect(redirectSpy).toHaveBeenCalledWith("/connect");

      // Now already on /connect -> must NOT redirect again
      redirectSpy.mockClear();
      pathnameSpy.mockReturnValue("/connect");
      ws.connect();
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(redirectSpy).not.toHaveBeenCalled();
    });
  });
});

describe("applyPaneOutputEvent", () => {
  it("appends incremental lines and replaces on full snapshots within maxLines", () => {
    const initial = ["line 1", "line 2"];
    const appended = applyPaneOutputEvent(initial, {
      lines: ["line 3", "line 4"],
      is_full: false,
    });
    expect(appended).toEqual(["line 1", "line 2", "line 3", "line 4"]);

    const bounded = applyPaneOutputEvent(appended, { lines: ["line 5"] }, 3);
    expect(bounded).toEqual(["line 3", "line 4", "line 5"]);

    const replaced = applyPaneOutputEvent(bounded, {
      lines: ["fresh 1", "fresh 2"],
      is_full: true,
    });
    expect(replaced).toEqual(["fresh 1", "fresh 2"]);
  });
});

describe("handleWsEvent", () => {
  it("invalidates pane output queries for pane websocket events", () => {
    const queryClient = new QueryClient();
    const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");

    handleWsEvent(queryClient, {
      type: "event",
      topic: "panes:demo:2",
      event_type: "pane.output",
      data: { lines: ["hello"] },
      ts: new Date().toISOString(),
      seq: 17,
    });

    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["pane-output", "demo", 2],
    });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["panes", "demo", 2],
    });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["panes"],
    });
  });

  it("invalidates ['agents'] on agent events and ['memory']/['cm'] on memory topic events", () => {
    const queryClient = new QueryClient();
    const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");

    handleWsEvent(queryClient, {
      type: "event",
      topic: "sessions:alpha",
      event_type: "agent.spawned",
      data: { id: "pane-1" },
      ts: new Date().toISOString(),
      seq: 21,
    });

    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["agents"],
    });

    invalidateSpy.mockClear();

    handleWsEvent(queryClient, {
      type: "event",
      topic: "memory",
      event_type: "memory.daemon.stopped",
      data: { state: "stopped" },
      ts: new Date().toISOString(),
      seq: 22,
    });

    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["memory"],
    });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["cm"],
    });
  });
});
