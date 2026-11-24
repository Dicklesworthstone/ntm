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
  DEFAULT_API_TIMEOUT_MS,
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

function isAttentionTopic(topic: string): boolean {
  return topic === "attention" || topic.startsWith("attention:");
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

// A line limit alone cannot bound a terminal prompt/progress line that never
// ends. Retain a bounded tail even when a snapshot contains one enormous line.
const MAX_PANE_OUTPUT_CHARS = 1024 * 1024;

function boundPaneOutput(lines: string[], maxLines: number): string[] {
  const tail = lines.slice(-maxLines);
  let remaining = MAX_PANE_OUTPUT_CHARS;
  for (let i = tail.length - 1; i >= 0; i--) {
    if (tail[i].length > remaining) {
      let start = tail[i].length - remaining;
      // Avoid retaining the low half of a surrogate pair at the tail boundary.
      const first = tail[i].charCodeAt(start);
      if (first >= 0xdc00 && first <= 0xdfff) start++;
      tail[i] = tail[i].slice(start);
      return tail.slice(i);
    }
    remaining -= tail[i].length;
    if (remaining === 0) return tail.slice(i);
  }
  return tail;
}

/**
 * Apply a `pane.output` WebSocket payload from `PaneStreamer` to the current
 * line buffer. Full snapshots (`is_full: true`) replace the buffer; incremental
 * deltas (`is_full: false` or omitted) append lines bounded by `maxLines` and
 * a character budget, including snapshots containing an unterminated prompt.
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
    return boundPaneOutput(incomingLines, limit);
  }

  if (incomingLines.length === 0) {
    return currentLines;
  }

  const combined = [...currentLines, ...incomingLines];
  return boundPaneOutput(combined, limit);
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
      ref: string;
      resolve: () => void;
      reject: (err: Error) => void;
      timer: ReturnType<typeof setTimeout>;
    }
  >();
  private lastSeq: number | null = null;
  private lastAttentionCursor: number | null = null;
  private attentionRequestId: string | null = null;
  // Persist across a disconnect until the replacement live subscription is
  // acknowledged and snapshot consumers can safely refresh.
  private attentionNeedsSnapshot = false;
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
    const timeoutId = controller
      ? setTimeout(() => controller.abort(), DEFAULT_API_TIMEOUT_MS)
      : null;

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
    } finally {
      if (timeoutId !== null) {
        clearTimeout(timeoutId);
      }
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
    this.attentionRequestId = null;
    this.attentionNeedsSnapshot = false;
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
    const ack = this.waitForAck(ref, `Subscribe timeout for topic: ${topic}`);
    if (isAttentionTopic(topic)) {
      this.sendAttentionSubscription(ref, options);
    } else {
      this.sendMessage("subscribe", ref, { topics: [topic], ...options });
    }
    return ack;
  }

  /**
   * Unsubscribe from a topic.
   */
  async unsubscribe(topic: string): Promise<void> {
    this.subscriptions.delete(topic);
    if (isAttentionTopic(topic) && !Array.from(this.subscriptions.keys()).some(isAttentionTopic)) {
      this.lastAttentionCursor = null;
      this.attentionRequestId = null;
      this.attentionNeedsSnapshot = false;
    }

    if (this.state !== "connected" || !this.ws) {
      return;
    }

    const ref = this.nextRef();
    const ack = this.waitForAck(ref, `Unsubscribe timeout for topic: ${topic}`);
    this.sendMessage("unsubscribe", ref, { topics: [topic] });
    return ack;
  }

  private waitForAck(ref: string, timeoutMessage: string): Promise<void> {
    return new Promise((resolve, reject) => {
      const entry = {
        ref, resolve, reject,
        timer: setTimeout(() => {
          if (this.pendingAcks.delete(entry.ref)) reject(new Error(timeoutMessage));
        }, 10000),
      };
      this.pendingAcks.set(ref, entry);
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
      this.attentionRequestId = null;
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
        if (message.seq !== undefined && !isAttentionTopic(message.topic || "")) {
          this.lastSeq = message.seq;
        }

        switch (message.type) {
          case "ack": {
            const ref = message.request_id;
            if (ref && ref === this.attentionRequestId) {
              this.attentionRequestId = null;
              if (this.attentionNeedsSnapshot) this.finishAttentionResync(message);
            }
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
            if (error.code === "CURSOR_EXPIRED" && ref &&
                ref !== this.attentionRequestId && !this.pendingAcks.has(ref)) {
              break; // A superseded request cannot reset the active membership.
            }
            if (ref && ref === this.attentionRequestId) {
              this.attentionRequestId = null;
              if (error.code === "CURSOR_EXPIRED" && !this.attentionNeedsSnapshot) {
                this.attentionNeedsSnapshot = true;
                this.lastAttentionCursor = null;
                const retry = this.nextRef();
                // Keep any explicit subscribe promise (and its deadline) tied
                // to the new request. Automatic reconnects have no promise.
                const pending = this.pendingAcks.get(ref);
                if (pending) {
                  this.pendingAcks.delete(ref);
                  pending.ref = retry;
                  this.pendingAcks.set(retry, pending);
                }
                this.sendAttentionSubscription(retry);
                break;
              }
            }
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
            if (isAttentionTopic(event.topic)) {
              // The envelope carries the durable cursor even if redaction
              // transforms the payload. Older servers may only set data.cursor.
              const cursor = event.seq ?? (event.data as { cursor?: unknown } | null)?.cursor;
              if (
                typeof cursor === "number" && Number.isSafeInteger(cursor) && cursor >= 0 &&
                (this.lastAttentionCursor === null || cursor > this.lastAttentionCursor)
              ) {
                this.lastAttentionCursor = cursor;
              }
            }
            this.eventHandlers.forEach((h) => h(event));
            break;
          }

          case "stream.reset": {
            // Hub resets leave memberships active. Refresh the snapshots and
            // resume future hub replay at the reset boundary. Attention uses a
            // separate journal and its cursor remains valid.
            const boundary = (message as WSMessage & { current_seq?: number }).current_seq;
            this.lastSeq = typeof boundary === "number" && Number.isSafeInteger(boundary) && boundary >= 0
              ? boundary : null;
            this.eventHandlers.forEach((h) => h({
              ...message,
              type: "event",
              topic: message.topic || "",
              event_type: "stream.reset",
              data: message.data ?? message,
            }));
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

  private finishAttentionResync(ack: WSMessage): void {
    const attention = (ack.data as { attention?: { newest_cursor?: unknown } } | null)?.attention;
    const boundary = attention?.newest_cursor;
    // Also remember the subscription boundary on quiet feeds. Events can
    // arrive before the ACK, in which case their newer cursor wins.
    if (typeof boundary === "number" && Number.isSafeInteger(boundary) && boundary >= 0) {
      this.lastAttentionCursor = Math.max(this.lastAttentionCursor ?? 0, boundary);
    }
    this.attentionNeedsSnapshot = false;
    const reset: WSEvent = {
      type: "event", event_type: "stream.reset", topic: "attention", ts: ack.ts,
      data: { reason: "cursor_expired" },
    };
    this.eventHandlers.forEach((handler) => handler(reset));
  }

  private sendAttentionSubscription(ref: string, options?: SubscribeOptions): void {
    const topics = Array.from(this.subscriptions.keys()).filter(isAttentionTopic);
    const request: Record<string, unknown> = {
      topics,
      ...(options ?? this.subscriptions.get(topics[topics.length - 1])),
    };
    if (this.attentionNeedsSnapshot) {
      request.since_cursor = -1;
    } else if (this.lastAttentionCursor !== null) {
      request.since_cursor = this.lastAttentionCursor;
    }
    this.attentionRequestId = ref;
    this.sendMessage("subscribe", ref, request);
  }

  private resubscribeAll(): void {
    const regularGroups = new Map<string, { topics: string[]; options: SubscribeOptions }>();
    let hasAttention = false;
    for (const [topic, options] of this.subscriptions) {
      if (isAttentionTopic(topic)) {
        hasAttention = true;
        continue;
      }
      // Replay matching hub topics together so a busy topic cannot advance
      // the shared seq past another topic's undelivered history on reconnect.
      const resume = { ...options };
      if (this.lastSeq !== null) resume.since = this.lastSeq;
      const key = JSON.stringify([resume.since, resume.throttle_ms, resume.max_lines_per_msg, resume.mode]);
      const group = regularGroups.get(key);
      if (group) {
        group.topics.push(topic);
      } else {
        regularGroups.set(key, { topics: [topic], options: resume });
      }
    }
    for (const { topics, options } of regularGroups.values()) {
      this.sendMessage("subscribe", this.nextRef(), { topics, ...options });
    }
    if (hasAttention) {
      this.sendAttentionSubscription(this.nextRef());
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
