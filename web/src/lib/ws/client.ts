/**
 * NTM WebSocket Client
 *
 * Handles real-time subscriptions with:
 * - Automatic reconnection with exponential backoff
 * - Topic-based subscriptions
 * - Event cursor tracking for replay
 * - Integration with TanStack Query for cache updates
 */

import { getBaseUrl, getConnectionConfig } from "../api/client";

// WebSocket message types from server
export type WSMessageType =
  | "ack"
  | "error"
  | "pong"
  | "event"
  | "stream.reset"
  | "pane.output"
  | "pane.output.dropped";

// Base message envelope
export interface WSMessage {
  type: WSMessageType;
  ts: string;
  seq?: number;
  topic?: string;
  request_id?: string;
  data?: unknown;
}

// Event message with typed data
export interface WSEvent<T = unknown> extends WSMessage {
  type: "event";
  topic: string;
  event_type: string;
  data: T;
}

// Error message
export interface WSError extends WSMessage {
  type: "error";
  code: string;
  message: string;
  details?: unknown;
}

// Subscription options
export interface SubscribeOptions {
  since?: number;
  throttle_ms?: number;
  max_lines_per_msg?: number;
  mode?: "lines" | "raw";
}

// Connection state
export type ConnectionState = "disconnected" | "connecting" | "connected" | "reconnecting";

// Event handlers
export type EventHandler = (event: WSEvent) => void;
export type StateHandler = (state: ConnectionState) => void;
export type ErrorHandler = (error: WSError) => void;

/**
 * WebSocket client for NTM server.
 */
export class NtmWebSocket {
  private ws: WebSocket | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private pingInterval: ReturnType<typeof setInterval> | null = null;
  private reconnectAttempt = 0;
  private maxReconnectAttempt = 10;
  private baseReconnectDelay = 1000;
  private maxReconnectDelay = 30000;

  private state: ConnectionState = "disconnected";
  private subscriptions = new Map<string, SubscribeOptions>();
  private eventHandlers = new Set<EventHandler>();
  private stateHandlers = new Set<StateHandler>();
  private errorHandlers = new Set<ErrorHandler>();
  private pendingAcks = new Map<string, { resolve: () => void; reject: (err: Error) => void }>();
  private lastSeq: number | null = null;
  private refCounter = 0;

  /**
   * Connect to the WebSocket server.
   */
  connect(): void {
    if (this.state === "connected" || this.state === "connecting") {
      return;
    }

    this.setState("connecting");
    const config = getConnectionConfig();
    const baseUrl = getBaseUrl();

    // Convert HTTP URL to WebSocket URL
    const wsUrl = baseUrl.replace(/^http/, "ws") + "/api/v1/ws";
    const url = config?.authToken ? `${wsUrl}?token=${config.authToken}` : wsUrl;

    if (process.env.NODE_ENV === "development") {
      console.log("[WS] Connecting to", wsUrl);
    }

    try {
      this.ws = new WebSocket(url);
      this.setupEventListeners();
    } catch (err) {
      console.error("[WS] Connection error:", err);
      this.scheduleReconnect();
    }
  }

  /**
   * Disconnect from the server.
   */
  disconnect(): void {
    this.clearTimers();
    this.rejectPendingAcks("WebSocket disconnected");
    if (this.ws) {
      this.ws.close(1000, "Client disconnect");
      this.ws = null;
    }
    this.setState("disconnected");
    this.subscriptions.clear();
  }

  /**
   * Subscribe to a topic.
   */
  async subscribe(topic: string, options: SubscribeOptions = {}): Promise<void> {
    this.subscriptions.set(topic, options);

    if (this.state !== "connected" || !this.ws) {
      // Will subscribe when connected
      return;
    }

    const ref = this.nextRef();
    const payload: Record<string, unknown> = { topics: [topic], ...options };

    return new Promise((resolve, reject) => {
      this.pendingAcks.set(ref, { resolve, reject });
      this.sendMessage("subscribe", ref, payload);

      // Timeout for ack
      setTimeout(() => {
        if (this.pendingAcks.has(ref)) {
          this.pendingAcks.delete(ref);
          reject(new Error(`Subscribe timeout for topic: ${topic}`));
        }
      }, 10000);
    });
  }

  /**
   * Unsubscribe from a topic.
   */
  async unsubscribe(topic: string): Promise<void> {
    this.subscriptions.delete(topic);

    if (this.state !== "connected" || !this.ws) {
      return;
    }

    const ref = this.nextRef();
    return new Promise((resolve, reject) => {
      this.pendingAcks.set(ref, { resolve, reject });
      this.sendMessage("unsubscribe", ref, { topics: [topic] });

      setTimeout(() => {
        if (this.pendingAcks.has(ref)) {
          this.pendingAcks.delete(ref);
          reject(new Error(`Unsubscribe timeout for topic: ${topic}`));
        }
      }, 10000);
    });
  }

  /**
   * Add an event handler.
   */
  onEvent(handler: EventHandler): () => void {
    this.eventHandlers.add(handler);
    return () => this.eventHandlers.delete(handler);
  }

  /**
   * Add a state change handler.
   */
  onStateChange(handler: StateHandler): () => void {
    this.stateHandlers.add(handler);
    handler(this.state); // Call immediately with current state
    return () => this.stateHandlers.delete(handler);
  }

  /**
   * Add an error handler.
   */
  onError(handler: ErrorHandler): () => void {
    this.errorHandlers.add(handler);
    return () => this.errorHandlers.delete(handler);
  }

  /**
   * Get current connection state.
   */
  getState(): ConnectionState {
    return this.state;
  }

  /**
   * Get last received sequence number.
   */
  getLastSeq(): number | null {
    return this.lastSeq;
  }

  private setupEventListeners(): void {
    if (!this.ws) return;

    this.ws.onopen = () => {
      if (process.env.NODE_ENV === "development") {
        console.log("[WS] Connected");
      }
      this.setState("connected");
      this.reconnectAttempt = 0;
      this.startPing();
      this.resubscribeAll();
    };

    this.ws.onclose = (event) => {
      if (process.env.NODE_ENV === "development") {
        console.log("[WS] Disconnected:", event.code, event.reason);
      }
      this.clearTimers();
      this.ws = null;
      this.rejectPendingAcks("WebSocket disconnected");

      if (event.code !== 1000) {
        // Abnormal close, attempt reconnect
        this.scheduleReconnect();
      } else {
        this.setState("disconnected");
      }
    };

    this.ws.onerror = (event) => {
      console.error("[WS] Error:", event);
    };

    this.ws.onmessage = (event) => {
      this.handleMessage(event.data);
    };
  }

  private handleMessage(data: string): void {
    const frames = data
      .split("\n")
      .map((frame) => frame.trim())
      .filter((frame) => frame.length > 0);

    for (const frame of frames) {
      try {
        const message = JSON.parse(frame) as WSMessage;

        // Track sequence for resumption
        if (message.seq !== undefined) {
          this.lastSeq = message.seq;
        }

        switch (message.type) {
          case "ack": {
            const ref = message.request_id;
            if (ref && this.pendingAcks.has(ref)) {
              this.pendingAcks.get(ref)!.resolve();
              this.pendingAcks.delete(ref);
            }
            break;
          }

          case "error": {
            const error = message as WSError;
            const ref = error.request_id;
            if (ref && this.pendingAcks.has(ref)) {
              this.pendingAcks.get(ref)!.reject(new Error(error.message));
              this.pendingAcks.delete(ref);
            }
            this.errorHandlers.forEach((h) => h(error));
            break;
          }

          case "event":
          case "pane.output": {
            const event = message as WSEvent;
            this.eventHandlers.forEach((h) => h(event));
            break;
          }

          case "stream.reset": {
            // Server says our cursor is stale, reset subscriptions
            if (process.env.NODE_ENV === "development") {
              console.log("[WS] Stream reset, resubscribing");
            }
            this.lastSeq = null;
            this.resubscribeAll();
            break;
          }

          case "pong":
            // Expected response to ping
            break;

          default:
            if (process.env.NODE_ENV === "development") {
              console.log("[WS] Unknown message type:", message.type);
            }
          }
      } catch (err) {
        console.error("[WS] Failed to parse message:", err);
      }
    }
  }

  private setState(state: ConnectionState): void {
    if (this.state !== state) {
      this.state = state;
      this.stateHandlers.forEach((h) => h(state));
    }
  }

  private nextRef(): string {
    return `ref-${++this.refCounter}`;
  }

  private startPing(): void {
    this.pingInterval = setInterval(() => {
      if (this.ws?.readyState === WebSocket.OPEN) {
        this.sendMessage("ping", this.nextRef());
      }
    }, 30000);
  }

  private clearTimers(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.pingInterval) {
      clearInterval(this.pingInterval);
      this.pingInterval = null;
    }
  }

  private rejectPendingAcks(message: string): void {
    if (this.pendingAcks.size === 0) {
      return;
    }
    const pending = Array.from(this.pendingAcks.values());
    this.pendingAcks.clear();
    for (const entry of pending) {
      entry.reject(new Error(message));
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectAttempt >= this.maxReconnectAttempt) {
      console.error("[WS] Max reconnect attempts reached");
      this.setState("disconnected");
      return;
    }

    this.setState("reconnecting");
    const delay = Math.min(
      this.baseReconnectDelay * Math.pow(2, this.reconnectAttempt),
      this.maxReconnectDelay
    );

    if (process.env.NODE_ENV === "development") {
      console.log(`[WS] Reconnecting in ${delay}ms (attempt ${this.reconnectAttempt + 1})`);
    }

    this.reconnectTimer = setTimeout(() => {
      this.reconnectAttempt++;
      this.connect();
    }, delay);
  }

  private resubscribeAll(): void {
    // Resubscribe to all topics with since cursor for replay
    for (const [topic, options] of this.subscriptions) {
      const subscribeOptions = { ...options };
      if (this.lastSeq !== null) {
        subscribeOptions.since = this.lastSeq;
      }

      const ref = this.nextRef();
      this.sendMessage("subscribe", ref, {
        topics: [topic],
        ...subscribeOptions,
      });
    }
  }

  private sendMessage(
    type: "subscribe" | "unsubscribe" | "ping",
    requestId: string,
    data?: Record<string, unknown>
  ): void {
    if (this.ws?.readyState !== WebSocket.OPEN) {
      return;
    }

    this.ws.send(
      JSON.stringify({
        type,
        ts: new Date().toISOString(),
        request_id: requestId,
        data,
      })
    );
  }
}

// Singleton instance
let wsInstance: NtmWebSocket | null = null;

/**
 * Get the singleton WebSocket client.
 */
export function getWsClient(): NtmWebSocket {
  if (!wsInstance) {
    wsInstance = new NtmWebSocket();
  }
  return wsInstance;
}

/**
 * Reset the WebSocket client (call when connection config changes).
 */
export function resetWsClient(): void {
  if (wsInstance) {
    wsInstance.disconnect();
    wsInstance = null;
  }
}
