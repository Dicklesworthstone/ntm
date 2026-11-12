"use client";

/**
 * TanStack Query Provider with WebSocket Bridge
 *
 * Provides:
 * - QueryClient with sensible defaults
 * - WebSocket integration that updates query cache
 * - Connection state management
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { getWsClient, type ConnectionState, type WSEvent } from "../ws/client";

// Create QueryClient with NTM-specific defaults
function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Stale time: how long before data is considered stale
        staleTime: 5000,
        // Cache time: how long to keep unused data
        gcTime: 10 * 60 * 1000, // 10 minutes
        // Retry configuration
        retry: 2,
        retryDelay: (attemptIndex) => Math.min(1000 * 2 ** attemptIndex, 10000),
        // Refetch settings
        refetchOnWindowFocus: true,
        refetchOnReconnect: true,
      },
      mutations: {
        retry: 1,
      },
    },
  });
}

// Connection context
interface ConnectionContextValue {
  wsState: ConnectionState;
  isConnected: boolean;
}

const ConnectionContext = createContext<ConnectionContextValue>({
  wsState: "disconnected",
  isConnected: false,
});

export function useConnection() {
  return useContext(ConnectionContext);
}

// Provider props
interface NtmQueryProviderProps {
  children: ReactNode;
}

// Singleton QueryClient
let queryClientInstance: QueryClient | null = null;

function getQueryClient(): QueryClient {
  if (!queryClientInstance) {
    queryClientInstance = createQueryClient();
  }
  return queryClientInstance;
}

/**
 * Provider component that sets up TanStack Query and WebSocket bridge.
 */
export function NtmQueryProvider({ children }: NtmQueryProviderProps) {
  const [wsState, setWsState] = useState<ConnectionState>("disconnected");
  const queryClient = getQueryClient();

  useEffect(() => {
    const ws = getWsClient();

    // Track connection state
    const unsubState = ws.onStateChange((state) => {
      setWsState(state);
    });

    // Bridge WebSocket events to query cache
    const unsubEvents = ws.onEvent((event) => {
      handleWsEvent(queryClient, event);
    });

    const topics = [
      "sessions:*",
      "panes:*",
      "beads:*",
      "mail:*",
      "reservations:*",
      "pipelines:*",
      "approvals:*",
      "accounts:*",
      "attention",
      "scanner",
      "memory",
    ];
    for (const topic of topics) {
      void ws.subscribe(topic).catch((error) => {
        if (process.env.NODE_ENV === "development") {
          console.error("[WS Bridge] subscribe failed", topic, error);
        }
      });
    }

    // Connect WebSocket
    ws.connect();

    return () => {
      unsubState();
      unsubEvents();
    };
  }, [queryClient]);

  const contextValue: ConnectionContextValue = {
    wsState,
    isConnected: wsState === "connected",
  };

  return (
    <QueryClientProvider client={queryClient}>
      <ConnectionContext.Provider value={contextValue}>
        {children}
      </ConnectionContext.Provider>
    </QueryClientProvider>
  );
}

/**
 * Handle WebSocket events by updating the query cache.
 * Maps event types to query keys for cache invalidation/updates.
 */
export function handleWsEvent(queryClient: QueryClient, event: WSEvent): void {
  const { topic, event_type } = event;

  // Log in development
  if (process.env.NODE_ENV === "development") {
    console.log("[WS Bridge] Event:", event_type, topic);
  }

  // Map events to query invalidations
  // Sessions events
  if (topic.startsWith("sessions:") || event_type.startsWith("session.")) {
    queryClient.invalidateQueries({ queryKey: ["sessions"] });

    // Extract session name from topic or event
    const sessionMatch = topic.match(/^sessions:(.+)$/);
    if (sessionMatch) {
      queryClient.invalidateQueries({
        queryKey: ["sessions", sessionMatch[1]],
      });
    }
  }

  // Pane events
  if (topic.startsWith("panes:") || event_type.startsWith("pane.")) {
    // panes:sessionName:paneIdx
    const match = topic.match(/^panes:(.+):(\d+)$/);
    if (match) {
      const [, sessionName, paneIdx] = match;
      const parsedPaneIdx = parseInt(paneIdx, 10);
      queryClient.invalidateQueries({
        queryKey: ["panes", sessionName, parsedPaneIdx],
      });
      queryClient.invalidateQueries({
        queryKey: ["pane-output", sessionName, parsedPaneIdx],
      });
      if (event_type !== "pane.output") {
        queryClient.invalidateQueries({
          queryKey: ["session-panes", sessionName],
        });
      }
    }
    queryClient.invalidateQueries({ queryKey: ["panes"] });
  }

  // Agent events
  if (
    topic === "agents" ||
    topic.startsWith("agents:") ||
    event_type.startsWith("agent.")
  ) {
    queryClient.invalidateQueries({ queryKey: ["agents"] });
  }

  // Memory / CASS Memory (cm) events
  if (
    topic === "memory" ||
    topic.startsWith("memory:") ||
    event_type.startsWith("memory.") ||
    event_type.startsWith("cm.")
  ) {
    queryClient.invalidateQueries({ queryKey: ["memory"] });
    queryClient.invalidateQueries({ queryKey: ["cm"] });
  }

  // Bead events
  if (topic.startsWith("beads:") || event_type.startsWith("bead.")) {
    queryClient.invalidateQueries({ queryKey: ["beads"] });

    const beadMatch = topic.match(/^beads:(.+)$/);
    if (beadMatch) {
      queryClient.invalidateQueries({
        queryKey: ["beads", beadMatch[1]],
      });
    }
  }

  // Mail events
  if (topic.startsWith("mail:") || event_type.startsWith("mail.")) {
    queryClient.invalidateQueries({ queryKey: ["mail"] });
  }

  // Reservation events
  if (topic.startsWith("reservations:") || event_type.startsWith("reservation.")) {
    queryClient.invalidateQueries({ queryKey: ["reservations"] });
  }

  // Scanner / bug tracker events
  if (topic === "scanner" || event_type.startsWith("scanner.")) {
    queryClient.invalidateQueries({ queryKey: ["scanner"] });
    queryClient.invalidateQueries({ queryKey: ["bugs"] });
  }

  // Pipeline events
  if (topic.startsWith("pipelines:") || event_type.startsWith("pipeline.")) {
    queryClient.invalidateQueries({ queryKey: ["pipelines"] });
  }

  // Safety / approvals events
  if (topic.startsWith("approvals:") || event_type.startsWith("approval.")) {
    queryClient.invalidateQueries({ queryKey: ["safety"] });
  }

  // Account rotation / quota events
  if (topic.startsWith("accounts:") || event_type.startsWith("account.")) {
    queryClient.invalidateQueries({ queryKey: ["accounts"] });
  }

  // For optimistic updates with setQueryData, handle specific events here
  // Example:
  // if (event_type === "session.created" && event.data) {
  //   queryClient.setQueryData(["sessions"], (old) => [...old, event.data]);
  // }
}

/**
 * Reset the query client (call when connection config changes).
 */
export function resetQueryClient(): void {
  if (queryClientInstance) {
    queryClientInstance.clear();
    queryClientInstance = null;
  }
}
