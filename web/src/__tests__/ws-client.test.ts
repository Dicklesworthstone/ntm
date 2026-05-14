/**
 * WebSocket Client Tests
 *
 * Tests for connection state and reconnection behavior.
 */

import { QueryClient } from "@tanstack/react-query";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { handleWsEvent } from "../lib/hooks/use-query";
import { NtmWebSocket, type ConnectionState } from "../lib/ws/client";

// Mock WebSocket
class MockWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  readyState = MockWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;

  constructor(public url: string) {
    // Simulate async connection
    setTimeout(() => {
      this.readyState = MockWebSocket.OPEN;
      this.onopen?.();
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
    localStorageMock.clear();
    localStorageMock.setItem(
      "ntm-connection",
      JSON.stringify({ baseUrl: "http://localhost:8080" })
    );
    ws = new NtmWebSocket();
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
});
