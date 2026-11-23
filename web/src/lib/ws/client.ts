/**
 * NTM WebSocket Client
 *
 * Handles real-time subscriptions with:
 * - Automatic reconnection with exponential backoff
 * - Topic-based subscriptions
 * - Event cursor tracking for replay
 * - Integration with TanStack Query for cache updates
 */

import {
  apiRequestSignal,
  getBaseUrl,
  getConnectionConfig,
  handleUnauthorizedRedirect,
} from "../api/client";

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

export interface PaneOutputEventData {
  lines?: string[];
  output?: string;
  line?: string;
  seq?: number;
  ts?: string;
  is_full?: boolean;
  full?: boolean;
}

/**
 * Apply a `pane.output` WebSocket payload from `PaneStreamer` to the current
 * line buffer. Full snapshots (`is_full: true`) replace the buffer; incremental
 * deltas (`is_full: false` or omitted) append lines bounded by `maxLines`.
 */
export function applyPaneOutputEvent(
  currentLines: string[],
  data: unknown,
  maxLines = 2000
): string[] {
  const limit = Math.max(1, maxLines);
  let incomingLines: string[] = [];
  let isFull = false;

  if (typeof data === "string") {
    incomingLines = data.length > 0 ? data.split("\n") : [];
  } else if (data && typeof data === "object") {
    const record = data as PaneOutputEventData;
    isFull = record.is_full === true || record.full === true;
    if (Array.isArray(record.lines)) {
      incomingLines = record.lines.map((line) => String(line));
    } else if (typeof record.output === "string") {
      incomingLines = record.output.length > 0 ? record.output.split("\n") : [];
    } else if (typeof record.line === "string") {
      incomingLines = [record.line];
    }
  } else {
    return currentLines;
  }

  if (isFull) {
    return incomingLines.length > limit
      ? incomingLines.slice(-limit)
      : incomingLines;
  }

  if (incomingLines.length === 0) {
    return currentLines;
  }

  const combined = [...currentLines, ...incomingLines];
  return combined.length > limit ? combined.slice(-limit) : combined;
}

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
  private pendingAcks = new Map<
    string,
    {
      resolve: () => void;
      reject: (err: Error) => void;
      timer: ReturnType<typeof setTimeout>;
    }
  >();
  private lastSeq: number | null = null;
  private lastAttentionCursor: number | null = null;
  private refCounter = 0;
  private connectGeneration = 0;
  private ticketAbortController: AbortController | null = null;

  /**
   * Connect to the WebSocket server.
   * When an auth token is configured, exchanges it via POST /api/v1/auth/ws-ticket
   * for a single-use ?ticket= query parameter so the raw bearer secret never
   * appears in WebSocket URLs.
   */
  connect(): void {
    if (this.state === "connected" || this.state === "connecting") {
      return;
    }

    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }

    const generation = ++this.connectGeneration;
    this.setState("connecting");
    const config = getConnectionConfig();
    const baseUrl = getBaseUrl();

    // Convert HTTP URL to WebSocket URL
    const wsUrl = baseUrl.replace(/^http/, "ws") + "/api/v1/ws";
    const authToken = config?.authToken?.trim() || "";

    if (!authToken) {
      this.openSocket(wsUrl, wsUrl);
      return;
    }

    void this.connectWithTicket(generation, baseUrl, wsUrl, authToken);
  }

  private async connectWithTicket(
    generation: number,
    baseUrl: string,
    wsUrl: string,
    authToken: string
  ): Promise<void> {
    const controller =
      typeof AbortController !== "undefined" ? new AbortController() : null;
    this.ticketAbortController = controller;

    try {
      const response = await fetch(`${baseUrl}/api/v1/auth/ws-ticket`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${authToken}`,
        },
        signal: controller?.signal ?? apiRequestSignal(),
      });

      if (this.ticketAbortController === controller) {
        this.ticketAbortController = null;
      }
      if (generation !== this.connectGeneration) {
        return;
      }

      if (response.status === 401) {
        const authError: WSError = {
          type: "error",
          ts: new Date().toISOString(),
          code: "UNAUTHORIZED",
          message: "WebSocket authentication failed (401)",
        };
        this.errorHandlers.forEach((h) => h(authError));
        this.setState("disconnected");
        handleUnauthorizedRedirect();
        return;
      }

      if (!response.ok) {
        throw new Error(`WS ticket exchange failed (${response.status})`);
      }

      const payload = (await response.json()) as {
        success?: boolean;
        ticket?: string;
        error?: string;
      };

      if (generation !== this.connectGeneration) {
        return;
      }

      const ticket =
        typeof payload?.ticket === "string" ? payload.ticket.trim() : "";
      if (payload?.success === false || !ticket) {
        throw new Error(payload?.error || "WS ticket response missing ticket");
      }

      const url = `${wsUrl}?ticket=${encodeURIComponent(ticket)}`;
      this.openSocket(url, wsUrl);
    } catch (err) {
      if (this.ticketAbortController === controller) {
        this.ticketAbortController = null;
      }
      if (generation !== this.connectGeneration) {
        return;
      }
      console.error("[WS] Ticket exchange error:", err);
      this.scheduleReconnect();
    }
  }

  private openSocket(url: string, logUrl: string): void {
    if (process.env.NODE_ENV === "development") {
      console.log("[WS] Connecting to", logUrl);
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
    this.connectGeneration++;
    if (this.ticketAbortController) {
      this.ticketAbortController.abort();
      this.ticketAbortController = null;
    }
    this.clearTimers();
    this.rejectPendingAcks("WebSocket disconnected");
    if (this.ws) {
      const staleSocket = this.ws;
      staleSocket.onopen = null;
      staleSocket.onclose = null;
      staleSocket.onerror = null;
      staleSocket.onmessage = null;
      this.ws = null;
      staleSocket.close(1000, "Client disconnect");
    }
    this.setState("disconnected");
    this.subscriptions.clear();
    this.lastSeq = null;
    this.lastAttentionCursor = null;
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
      const timer = setTimeout(() => {
        if (this.pendingAcks.has(ref)) {
          this.pendingAcks.delete(ref);
          reject(new Error(`Subscribe timeout for topic: ${topic}`));
        }
      }, 10000);
      this.pendingAcks.set(ref, { resolve, reject, timer });
      this.sendMessage("subscribe", ref, payload);
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
      const timer = setTimeout(() => {
        if (this.pendingAcks.has(ref)) {
          this.pendingAcks.delete(ref);
          reject(new Error(`Unsubscribe timeout for topic: ${topic}`));
        }
      }, 10000);
      this.pendingAcks.set(ref, { resolve, reject, timer });
      this.sendMessage("unsubscribe", ref, { topics: [topic] });
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
    const socket = this.ws;
    if (!socket) return;

    socket.onopen = () => {
      if (this.ws !== socket) return;
      if (process.env.NODE_ENV === "development") {
        console.log("[WS] Connected");
      }
      this.setState("connected");
      this.reconnectAttempt = 0;
      this.startPing();
      this.resubscribeAll();
    };

    socket.onclose = (event) => {
      if (this.ws !== socket) return;
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

    socket.onerror = (event) => {
      if (this.ws !== socket) return;
      console.error("[WS] Error:", event);
    };

    socket.onmessage = (event) => {
      if (this.ws !== socket) return;
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

        // Track hub sequence for resumption on non-attention topics.
        // Attention frames carry the AttentionFeed cursor in `seq`, which is
        // tracked separately in `lastAttentionCursor` so it never corrupts
        // the hub's event-sequence cursor.
        const isAttentionTopic =
          message.topic === "attention" ||
          (typeof message.topic === "string" &&
            message.topic.startsWith("attention:"));
        if (message.seq !== undefined && !isAttentionTopic) {
          this.lastSeq = message.seq;
        }

        switch (message.type) {
          case "ack": {
            const ref = message.request_id;
            if (ref && this.pendingAcks.has(ref)) {
              const entry = this.pendingAcks.get(ref)!;
              clearTimeout(entry.timer);
              this.pendingAcks.delete(ref);
              entry.resolve();
            }
            break;
          }

          case "error": {
            const error = message as WSError;
            const ref = error.request_id;
            if (ref && this.pendingAcks.has(ref)) {
              const entry = this.pendingAcks.get(ref)!;
              clearTimeout(entry.timer);
              this.pendingAcks.delete(ref);
              entry.reject(new Error(error.message));
            }
            this.errorHandlers.forEach((h) => h(error));
            break;
          }

          case "event":
          case "pane.output":
          case "pane.output.dropped": {
            const event: WSEvent = {
              ...(message as WSEvent),
              event_type:
                (message as Partial<WSEvent>).event_type || message.type,
              topic: message.topic || "",
            };
            if (
              event.topic === "attention" ||
              event.topic.startsWith("attention:")
            ) {
              const cursor =
                event.data &&
                typeof event.data === "object" &&
                typeof (event.data as { cursor?: unknown }).cursor === "number"
                  ? (event.data as { cursor: number }).cursor
                  : null;
              if (
                cursor !== null &&
                (this.lastAttentionCursor === null ||
                  cursor > this.lastAttentionCursor)
              ) {
                this.lastAttentionCursor = cursor;
              }
            }
            this.eventHandlers.forEach((h) => h(event));
            break;
          }

          case "stream.reset": {
            // Server says our cursor is stale, reset subscriptions
            if (process.env.NODE_ENV === "development") {
              console.log("[WS] Stream reset, resubscribing");
            }
            this.lastSeq = null;
            this.lastAttentionCursor = null;
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
      clearTimeout(entry.timer);
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
    // Resubscribe to all topics with the appropriate cursor domain for replay:
    // attention topics use attention cursor (since_cursor), while other topics
    // use the hub's WS frame sequence (since).
    for (const [topic, options] of this.subscriptions) {
      const subscribeOptions: Record<string, unknown> = { ...options };
      if (topic === "attention" || topic.startsWith("attention:")) {
        if (this.lastAttentionCursor !== null) {
          subscribeOptions.since_cursor = this.lastAttentionCursor;
        }
      } else if (this.lastSeq !== null) {
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
