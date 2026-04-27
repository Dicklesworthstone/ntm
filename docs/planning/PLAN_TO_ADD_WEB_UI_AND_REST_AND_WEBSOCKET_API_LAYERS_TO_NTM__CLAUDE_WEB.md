# NTM Web Platform Extension

## Implementation Plan for REST API, WebSocket Streaming, and World-Class Web UI

**Version:** 1.0.0  
**Date:** January 2026  
**Status:** Ready for Implementation

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Technology Stack](#technology-stack)
3. [Architecture Overview](#architecture-overview)
4. [REST API Design](#rest-api-design)
5. [WebSocket Protocol](#websocket-protocol)
6. [UI/UX Design System](#uiux-design-system)
7. [Desktop Experience](#desktop-experience)
8. [Mobile Experience](#mobile-experience)
9. [Component Library](#component-library)
10. [Real-Time Integration](#real-time-integration)
11. [Security Architecture](#security-architecture)
12. [Performance Optimizations](#performance-optimizations)
13. [Agent Protocol Integration](#agent-protocol-integration)
14. [Deployment](#deployment)
15. [Implementation Roadmap](#implementation-roadmap)

---

## Executive Summary

Transform NTM from a terminal-only tool into a multi-interface AI agent orchestration platform with:

- **REST API**: 100% CLI feature parity via Go backend
- **WebSocket Layer**: Real-time streaming for agent output, state changes, and notifications
- **Web UI**: Next.js 16 + React 19.2 + Bun, Stripe-level quality, mobile-first responsive design

### Goals

1. Every CLI command accessible via REST API
2. Real-time updates with <100ms latency
3. Production-ready web interface for desktop and mobile
4. WCAG 2.1 AA accessibility compliance
5. Seamless integration with Claude, Codex, and Gemini agents

---

## Technology Stack

### Backend (Go)

| Component | Technology | Purpose |
|-----------|------------|---------|
| HTTP Router | Chi | Lightweight, idiomatic routing with middleware support |
| WebSocket | gorilla/websocket | Battle-tested WebSocket implementation |
| OpenAPI | ogen | Type-safe code generation from OpenAPI specs |
| JSON | stdlib + sonic | Standard library for correctness, sonic for hot paths |

### Frontend (TypeScript/React)

| Component | Technology | Purpose |
|-----------|------------|---------|
| Runtime | Bun | Fast package management and bundling |
| Framework | Next.js 16 | Turbopack stable, Cache Components, React 19.2 |
| Data Fetching | TanStack Query v5 | Caching, background refetch, optimistic updates |
| Styling | Tailwind CSS v4 | Utility-first CSS with design tokens |
| Animation | Framer Motion | Production-ready animations |
| State | React Context + Zustand | Local state and settings persistence |
| Icons | Lucide React | Consistent, accessible icons |

### Agent Integration

| Component | Package | Purpose |
|-----------|---------|---------|
| Claude Agent SDK | `@anthropic-ai/claude-agent-sdk` | Direct Claude integration |
| ACP TypeScript | Agent Client Protocol | Multi-agent orchestration |
| Codex/Gemini | ACP adapters | Alternative agent support |

---

## Architecture Overview

### Data Flow

```
┌─────────────────────────────────────────────────────────┐
│                        Web UI                            │
│  (Next.js 16 + React 19.2 + TanStack Query)             │
└─────────────────┬───────────────────────┬───────────────┘
                  │                       │
                  ▼                       ▼
           REST API                 WebSocket
         (commands)               (streams)
                  │                       │
                  └───────────┬───────────┘
                              ▼
                    ┌─────────────────┐
                    │   Event Bus     │
                    │  (Go channels)  │
                    └────────┬────────┘
                             ▼
                    ┌─────────────────┐
                    │  NTM Core       │
                    │   Engine        │
                    └────────┬────────┘
                             ▼
                    ┌─────────────────┐
                    │     tmux        │
                    │   operations    │
                    └─────────────────┘
```

### Package Structure

```
ntm/
├── internal/
│   ├── api/
│   │   ├── server.go          # HTTP server setup
│   │   ├── router.go          # Chi router configuration
│   │   ├── middleware.go      # Auth, CORS, rate limiting
│   │   ├── websocket.go       # WebSocket hub
│   │   ├── pane_stream.go     # Pane output streaming
│   │   └── handlers/
│   │       ├── sessions.go
│   │       ├── agents.go
│   │       ├── robot.go
│   │       ├── work.go
│   │       └── config.go
│   └── acp/
│       ├── server.go          # ACP protocol implementation
│       └── tools.go           # Tool definitions
├── web/
│   ├── app/
│   │   ├── (dashboard)/
│   │   │   ├── sessions/
│   │   │   │   ├── page.tsx
│   │   │   │   └── [name]/
│   │   │   │       └── page.tsx
│   │   │   ├── work/
│   │   │   └── settings/
│   │   ├── layout.tsx
│   │   └── globals.css
│   ├── components/
│   │   ├── ui/
│   │   ├── session/
│   │   ├── agent/
│   │   ├── palette/
│   │   └── mobile/
│   └── lib/
│       ├── api/
│       │   ├── client.ts
│       │   └── queries.ts
│       └── hooks/
│           ├── useWebSocket.ts
│           └── useSession.ts
└── api/
    └── openapi.yaml
```

---

## REST API Design

### Design Principles

- **Resource-oriented**: URLs represent resources, HTTP methods represent actions
- **Consistent error envelope**: All errors return `{error: {code, message, details}}`
- **Cursor-based pagination**: Use `?cursor=` and `?limit=` for lists
- **Query parameter filtering**: Filter resources with query params
- **URL versioning**: All endpoints prefixed with `/api/v1/`

### Complete Endpoint Mapping

Every CLI command maps to an HTTP endpoint:

#### Sessions

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm create <name>` | `/api/v1/sessions` | POST | Create new session |
| `ntm list` | `/api/v1/sessions` | GET | List all sessions |
| `ntm status <name>` | `/api/v1/sessions/{name}` | GET | Get session status |
| `ntm kill <name>` | `/api/v1/sessions/{name}` | DELETE | Kill session |
| `ntm spawn <name> [agents]` | `/api/v1/sessions/{name}/spawn` | POST | Spawn agents in session |

#### Agents

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm add <name> <agent>` | `/api/v1/sessions/{name}/agents` | POST | Add agent to session |
| `ntm send <name> <prompt>` | `/api/v1/sessions/{name}/send` | POST | Send prompt to agents |
| `ntm int <name>` | `/api/v1/sessions/{name}/interrupt` | POST | Interrupt all agents |

#### Output

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm pane <name> <index>` | `/api/v1/sessions/{name}/panes/{index}/output` | GET | Get pane output |
| `ntm out <name>` | `/api/v1/sessions/{name}/output` | GET | Get all pane outputs |
| `ntm save <name>` | `/api/v1/sessions/{name}/save` | POST | Save outputs to files |

#### Monitoring

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm activity <name>` | `/api/v1/sessions/{name}/activity` | GET | Get agent activity |
| `ntm health <name>` | `/api/v1/sessions/{name}/health` | GET | Get health status |
| `ntm dash <name>` | `/api/v1/sessions/{name}/dashboard` | GET | Get dashboard data |
| `ntm extract <name>` | `/api/v1/sessions/{name}/extract` | GET | Extract code blocks |
| `ntm diff <name>` | `/api/v1/sessions/{name}/diff` | GET | Get git diffs |
| `ntm grep <name> <pattern>` | `/api/v1/sessions/{name}/grep` | GET | Search output |

#### Robot Mode

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm robot status` | `/api/v1/robot/status` | GET | Get robot mode status |
| `ntm robot context` | `/api/v1/robot/context/{session}` | GET | Get context for session |
| `ntm robot snapshot` | `/api/v1/robot/snapshot` | GET | Get full system snapshot |
| `ntm robot send` | `/api/v1/robot/send/{session}` | POST | Send as robot |
| `ntm robot assign` | `/api/v1/robot/assign/{session}` | POST | Assign work to agent |
| `ntm bead create` | `/api/v1/robot/beads` | POST | Create work bead |
| `ntm bead claim` | `/api/v1/robot/beads/{id}/claim` | POST | Claim bead |
| `ntm cass search` | `/api/v1/robot/cass/search` | GET | Search CASS |

#### Work Management

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm work triage` | `/api/v1/work/triage` | GET | Get triage view |
| `ntm work alerts` | `/api/v1/work/alerts` | GET | Get active alerts |
| `ntm work search` | `/api/v1/work/search` | GET | Search work items |
| `ntm work impact` | `/api/v1/work/impact` | POST | Calculate impact |
| `ntm work next` | `/api/v1/work/next` | GET | Get next work item |

#### Command Palette

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm palette` | `/api/v1/palette` | GET | Get available commands |
| N/A | `/api/v1/palette/execute` | POST | Execute palette command |

#### Configuration

| CLI Command | HTTP Endpoint | Method | Description |
|-------------|---------------|--------|-------------|
| `ntm config` | `/api/v1/config` | GET | Get configuration |
| `ntm config set` | `/api/v1/config` | POST | Update configuration |
| `ntm config get <key>` | `/api/v1/config/{key}` | GET | Get specific config |

#### WebSocket Endpoints

| Endpoint | Description |
|----------|-------------|
| `/api/v1/ws/sessions/{name}/watch` | Watch session events |
| `/api/v1/ws/global` | Global event stream |

### Request/Response Examples

#### Create Session

```http
POST /api/v1/sessions
Content-Type: application/json
Authorization: Bearer <token>

{
  "name": "feature-auth",
  "agents": ["cc", "cod"],
  "working_dir": "/home/user/project",
  "config": {
    "auto_compact": true,
    "checkpoint_interval": "5m"
  }
}
```

```json
{
  "data": {
    "name": "feature-auth",
    "created_at": "2026-01-07T12:00:00Z",
    "agents": [
      {"index": 0, "type": "claude", "state": "waiting"},
      {"index": 1, "type": "codex", "state": "waiting"}
    ],
    "working_dir": "/home/user/project"
  }
}
```

#### Send Prompt

```http
POST /api/v1/sessions/feature-auth/send
Content-Type: application/json
Authorization: Bearer <token>

{
  "prompt": "Implement user authentication with JWT",
  "targets": ["all"],
  "options": {
    "stream": true,
    "checkpoint_before": true
  }
}
```

```json
{
  "data": {
    "message_id": "msg_abc123",
    "sent_to": ["cc", "cod"],
    "timestamp": "2026-01-07T12:01:00Z"
  }
}
```

#### Error Response

```json
{
  "error": {
    "code": "SESSION_NOT_FOUND",
    "message": "Session 'feature-auth' does not exist",
    "details": {
      "requested_session": "feature-auth",
      "available_sessions": ["main", "testing"]
    }
  }
}
```

---

## WebSocket Protocol

### Message Envelope

JSON-RPC 2.0 inspired format:

```typescript
interface WebSocketMessage {
  type: 'event' | 'request' | 'response' | 'error';
  id?: string;           // UUID for request/response correlation
  event?: string;        // Event type for 'event' messages
  params?: Record<string, unknown>;
  result?: unknown;      // For 'response' messages
  error?: {              // For 'error' messages
    code: string;
    message: string;
    details?: unknown;
  };
}
```

### Event Types

#### Pane Events

| Event | Description | Payload |
|-------|-------------|---------|
| `pane.output` | New output from pane | `{session, index, content, timestamp}` |
| `pane.state` | Pane state changed | `{session, index, state, previous}` |
| `pane.context` | Context window update | `{session, index, used, total, percentage}` |

#### Agent Events

| Event | Description | Payload |
|-------|-------------|---------|
| `agent.state` | Agent state changed | `{session, agent, state, reason}` |
| `agent.compacted` | Agent compacted | `{session, agent, before, after}` |
| `agent.rate_limited` | Rate limit hit | `{session, agent, retry_after}` |
| `agent.crashed` | Agent crashed | `{session, agent, error}` |

#### Session Events

| Event | Description | Payload |
|-------|-------------|---------|
| `session.created` | Session created | `{name, agents, config}` |
| `session.killed` | Session terminated | `{name, reason}` |

#### System Events

| Event | Description | Payload |
|-------|-------------|---------|
| `alert.fired` | Alert triggered | `{id, type, message, severity}` |
| `notification.new` | New notification | `{id, title, body, action}` |
| `health.changed` | Health status changed | `{component, status, message}` |

#### Work Events

| Event | Description | Payload |
|-------|-------------|---------|
| `bead.created` | Work bead created | `{id, type, priority, assignee}` |
| `bead.updated` | Bead status changed | `{id, status, progress}` |
| `conflict.detected` | Work conflict | `{bead_id, agents, files}` |

#### Mail Events

| Event | Description | Payload |
|-------|-------------|---------|
| `mail.received` | New mail message | `{from, to, subject, body}` |
| `lock.acquired` | Lock acquired | `{resource, holder, expires}` |

### Client Connection Example

```typescript
// lib/hooks/useWebSocket.ts
import { useEffect, useRef, useCallback, useState } from 'react';

interface UseWebSocketOptions {
  url: string;
  onMessage?: (message: WebSocketMessage) => void;
  onConnect?: () => void;
  onDisconnect?: () => void;
  reconnect?: boolean;
  reconnectInterval?: number;
  maxReconnectAttempts?: number;
}

export function useWebSocket(options: UseWebSocketOptions) {
  const {
    url,
    onMessage,
    onConnect,
    onDisconnect,
    reconnect = true,
    reconnectInterval = 1000,
    maxReconnectAttempts = 10,
  } = options;

  const ws = useRef<WebSocket | null>(null);
  const reconnectAttempts = useRef(0);
  const [isConnected, setIsConnected] = useState(false);

  const connect = useCallback(() => {
    ws.current = new WebSocket(url);

    ws.current.onopen = () => {
      setIsConnected(true);
      reconnectAttempts.current = 0;
      onConnect?.();
    };

    ws.current.onmessage = (event) => {
      const message = JSON.parse(event.data) as WebSocketMessage;
      onMessage?.(message);
    };

    ws.current.onclose = () => {
      setIsConnected(false);
      onDisconnect?.();

      if (reconnect && reconnectAttempts.current < maxReconnectAttempts) {
        const delay = reconnectInterval * Math.pow(2, reconnectAttempts.current);
        reconnectAttempts.current++;
        setTimeout(connect, Math.min(delay, 30000));
      }
    };

    ws.current.onerror = (error) => {
      console.error('WebSocket error:', error);
    };
  }, [url, onMessage, onConnect, onDisconnect, reconnect, reconnectInterval, maxReconnectAttempts]);

  const send = useCallback((message: WebSocketMessage) => {
    if (ws.current?.readyState === WebSocket.OPEN) {
      ws.current.send(JSON.stringify(message));
    }
  }, []);

  const subscribe = useCallback((events: string[]) => {
    send({
      type: 'request',
      id: crypto.randomUUID(),
      params: { action: 'subscribe', events },
    });
  }, [send]);

  useEffect(() => {
    connect();
    return () => {
      ws.current?.close();
    };
  }, [connect]);

  return { isConnected, send, subscribe };
}
```

### Server Hub Implementation

```go
// internal/api/websocket.go
package api

import (
    "encoding/json"
    "sync"
    "time"

    "github.com/gorilla/websocket"
)

type Hub struct {
    clients    map[*Client]bool
    broadcast  chan []byte
    register   chan *Client
    unregister chan *Client
    sessions   map[string]map[*Client]bool
    mu         sync.RWMutex
}

type Client struct {
    hub           *Hub
    conn          *websocket.Conn
    send          chan []byte
    subscriptions map[string]bool
    session       string
}

type Message struct {
    Type   string          `json:"type"`
    ID     string          `json:"id,omitempty"`
    Event  string          `json:"event,omitempty"`
    Params json.RawMessage `json:"params,omitempty"`
    Result json.RawMessage `json:"result,omitempty"`
    Error  *ErrorPayload   `json:"error,omitempty"`
}

func NewHub() *Hub {
    return &Hub{
        clients:    make(map[*Client]bool),
        broadcast:  make(chan []byte, 256),
        register:   make(chan *Client),
        unregister: make(chan *Client),
        sessions:   make(map[string]map[*Client]bool),
    }
}

func (h *Hub) Run() {
    for {
        select {
        case client := <-h.register:
            h.mu.Lock()
            h.clients[client] = true
            h.mu.Unlock()

        case client := <-h.unregister:
            h.mu.Lock()
            if _, ok := h.clients[client]; ok {
                delete(h.clients, client)
                close(client.send)
                if client.session != "" {
                    delete(h.sessions[client.session], client)
                }
            }
            h.mu.Unlock()

        case message := <-h.broadcast:
            h.mu.RLock()
            for client := range h.clients {
                select {
                case client.send <- message:
                default:
                    close(client.send)
                    delete(h.clients, client)
                }
            }
            h.mu.RUnlock()
        }
    }
}

func (h *Hub) BroadcastToSession(session string, message []byte) {
    h.mu.RLock()
    defer h.mu.RUnlock()

    if clients, ok := h.sessions[session]; ok {
        for client := range clients {
            select {
            case client.send <- message:
            default:
                // Client buffer full, skip
            }
        }
    }
}

func (h *Hub) SubscribeToSession(client *Client, session string) {
    h.mu.Lock()
    defer h.mu.Unlock()

    if h.sessions[session] == nil {
        h.sessions[session] = make(map[*Client]bool)
    }
    h.sessions[session][client] = true
    client.session = session
}
```

---

## UI/UX Design System

### Catppuccin Color Palette

```css
/* Mocha (Dark Theme) */
:root[data-theme="dark"] {
  --base: #1e1e2e;
  --mantle: #181825;
  --crust: #11111b;
  --surface0: #313244;
  --surface1: #45475a;
  --surface2: #585b70;
  --overlay0: #6c7086;
  --overlay1: #7f849c;
  --overlay2: #9399b2;
  --subtext0: #a6adc8;
  --subtext1: #bac2de;
  --text: #cdd6f4;
  
  /* Accent Colors */
  --rosewater: #f5e0dc;
  --flamingo: #f2cdcd;
  --pink: #f5c2e7;
  --mauve: #cba6f7;
  --red: #f38ba8;
  --maroon: #eba0ac;
  --peach: #fab387;
  --yellow: #f9e2af;
  --green: #a6e3a1;
  --teal: #94e2d5;
  --sky: #89dceb;
  --sapphire: #74c7ec;
  --blue: #89b4fa;
  --lavender: #b4befe;
}

/* Latte (Light Theme) */
:root[data-theme="light"] {
  --base: #eff1f5;
  --mantle: #e6e9ef;
  --crust: #dce0e8;
  --surface0: #ccd0da;
  --surface1: #bcc0cc;
  --surface2: #acb0be;
  --overlay0: #9ca0b0;
  --overlay1: #8c8fa1;
  --overlay2: #7c7f93;
  --subtext0: #6c6f85;
  --subtext1: #5c5f77;
  --text: #4c4f69;
  
  /* Accent Colors (adjusted for light) */
  --mauve: #8839ef;
  --blue: #1e66f5;
  --green: #40a02b;
  --yellow: #df8e1d;
  --red: #d20f39;
}
```

### Agent Colors

```css
:root {
  /* Agent identification */
  --agent-claude: var(--mauve);      /* Purple for Claude */
  --agent-codex: var(--blue);        /* Blue for Codex */
  --agent-gemini: var(--yellow);     /* Yellow for Gemini */
  --agent-user: var(--green);        /* Green for User */
  
  /* Activity states */
  --state-waiting: var(--green);
  --state-generating: var(--blue);
  --state-thinking: var(--yellow);
  --state-stalled: var(--red);
  --state-error: var(--red);
}
```

### Typography

```css
:root {
  /* Font families */
  --font-sans: 'Inter var', -apple-system, BlinkMacSystemFont, sans-serif;
  --font-mono: 'JetBrains Mono', 'Fira Code', monospace;
  
  /* Font sizes */
  --text-xs: 0.75rem;    /* 12px */
  --text-sm: 0.875rem;   /* 14px */
  --text-base: 1rem;     /* 16px */
  --text-lg: 1.125rem;   /* 18px */
  --text-xl: 1.25rem;    /* 20px */
  --text-2xl: 1.5rem;    /* 24px */
  --text-3xl: 1.875rem;  /* 30px */
  
  /* Line heights */
  --leading-tight: 1.25;
  --leading-normal: 1.5;
  --leading-relaxed: 1.75;
}
```

### Spacing Scale

```css
:root {
  /* 4px base unit */
  --space-1: 0.25rem;   /* 4px */
  --space-2: 0.5rem;    /* 8px */
  --space-3: 0.75rem;   /* 12px */
  --space-4: 1rem;      /* 16px */
  --space-5: 1.25rem;   /* 20px */
  --space-6: 1.5rem;    /* 24px */
  --space-8: 2rem;      /* 32px */
  --space-10: 2.5rem;   /* 40px */
  --space-12: 3rem;     /* 48px */
  --space-16: 4rem;     /* 64px */
}
```

### Animation Tokens

```typescript
// lib/motion.ts
export const durations = {
  instant: 0,
  fast: 100,
  normal: 200,
  slow: 300,
  slower: 500,
} as const;

export const easings = {
  ease: [0.25, 0.1, 0.25, 1],
  easeIn: [0.4, 0, 1, 1],
  easeOut: [0, 0, 0.2, 1],
  easeInOut: [0.4, 0, 0.2, 1],
  spring: [0.175, 0.885, 0.32, 1.275],
} as const;

export const variants = {
  fadeIn: {
    hidden: { opacity: 0 },
    visible: { opacity: 1, transition: { duration: durations.normal / 1000 } },
  },
  slideUp: {
    hidden: { opacity: 0, y: 20 },
    visible: { opacity: 1, y: 0, transition: { duration: durations.normal / 1000 } },
  },
  scaleIn: {
    hidden: { opacity: 0, scale: 0.95 },
    visible: { opacity: 1, scale: 1, transition: { duration: durations.fast / 1000 } },
  },
  stagger: {
    visible: {
      transition: { staggerChildren: 0.05 },
    },
  },
} as const;
```

### Design Principles (Stripe-Inspired)

1. **Clarity over cleverness**: Every element serves a purpose
2. **Progressive disclosure**: Show complexity only when needed
3. **Responsive feedback**: Instant visual feedback for all interactions
4. **Consistent patterns**: Same problems solved the same way
5. **Accessibility first**: WCAG 2.1 AA compliance minimum
6. **Performance perception**: Skeleton states, optimistic updates

---

## Desktop Experience

### Layout Structure (≥1024px)

```
┌─────────┬──────────────────────────────────────┐
│ SIDEBAR │ HEADER (breadcrumb, search, user)    │
│  240px  ├──────────────────────────────────────┤
│         │                                      │
│  Nav    │ PRIMARY PANEL     │ SECONDARY PANEL  │
│  Items  │ (Sessions List)   │ (Details/Output) │
│         │                   │                  │
│  Quick  │                   │                  │
│  Actions│                   │                  │
│         │                   │                  │
│         │                   │                  │
│         │                   │                  │
└─────────┴───────────────────┴──────────────────┘
```

### Keyboard-First Navigation

| Shortcut | Action |
|----------|--------|
| `⌘K` | Open command palette |
| `⌘1-9` | Quick session switching |
| `⌘Enter` | Send to all agents |
| `⌘⇧Enter` | Send to Claude only |
| `⌘⇧C` | Send to Codex only |
| `⌘⇧G` | Send to Gemini only |
| `j/k` | Navigate lists (Vim-style) |
| `gg` | Jump to top |
| `G` | Jump to bottom |
| `Esc` | Close modals/palette |
| `/` | Focus search |

### Command Palette

```tsx
// components/palette/palette-dialog.tsx
'use client';

import { useState, useEffect, useMemo } from 'react';
import { motion, AnimatePresence } from 'framer-motion';
import { Command } from 'cmdk';
import { useHotkeys } from 'react-hotkeys-hook';
import { usePaletteCommands } from '@/lib/hooks/usePaletteCommands';

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [target, setTarget] = useState<'all' | 'cc' | 'cod' | 'gmi'>('all');
  const { commands, execute } = usePaletteCommands();

  useHotkeys('mod+k', (e) => {
    e.preventDefault();
    setOpen(true);
  });

  const filteredCommands = useMemo(() => {
    if (!search) return commands;
    const query = search.toLowerCase();
    return commands.filter(
      (cmd) =>
        cmd.label.toLowerCase().includes(query) ||
        cmd.keywords?.some((k) => k.includes(query))
    );
  }, [commands, search]);

  return (
    <AnimatePresence>
      {open && (
        <motion.div
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          className="fixed inset-0 z-50 bg-black/50 backdrop-blur-sm"
          onClick={() => setOpen(false)}
        >
          <motion.div
            initial={{ opacity: 0, scale: 0.95, y: -20 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.95, y: -20 }}
            className="fixed top-[20%] left-1/2 -translate-x-1/2 w-full max-w-xl"
            onClick={(e) => e.stopPropagation()}
          >
            <Command className="rounded-xl border border-surface1 bg-base shadow-2xl overflow-hidden">
              {/* Gradient header */}
              <div className="h-1 bg-gradient-to-r from-mauve via-blue to-green" />
              
              {/* Target selector */}
              <div className="flex gap-2 p-3 border-b border-surface0">
                {(['all', 'cc', 'cod', 'gmi'] as const).map((t) => (
                  <button
                    key={t}
                    onClick={() => setTarget(t)}
                    className={`px-3 py-1 rounded-full text-sm font-medium transition-colors ${
                      target === t
                        ? 'bg-mauve text-base'
                        : 'bg-surface0 text-subtext1 hover:bg-surface1'
                    }`}
                  >
                    {t === 'all' ? 'All' : t.toUpperCase()}
                  </button>
                ))}
              </div>

              {/* Search input */}
              <Command.Input
                value={search}
                onValueChange={setSearch}
                placeholder="Type a command or prompt..."
                className="w-full px-4 py-3 bg-transparent text-text placeholder:text-overlay0 focus:outline-none"
              />

              {/* Commands list */}
              <Command.List className="max-h-80 overflow-auto p-2">
                <Command.Empty className="py-6 text-center text-overlay0">
                  No commands found
                </Command.Empty>
                
                {filteredCommands.map((cmd) => (
                  <Command.Item
                    key={cmd.id}
                    value={cmd.label}
                    onSelect={() => {
                      execute(cmd, target);
                      setOpen(false);
                    }}
                    className="flex items-center gap-3 px-3 py-2 rounded-lg cursor-pointer data-[selected=true]:bg-surface0"
                  >
                    <span className="text-overlay1">{cmd.icon}</span>
                    <span className="flex-1 text-text">{cmd.label}</span>
                    {cmd.shortcut && (
                      <kbd className="px-2 py-0.5 rounded bg-surface1 text-xs text-subtext0">
                        {cmd.shortcut}
                      </kbd>
                    )}
                  </Command.Item>
                ))}
              </Command.List>

              {/* Footer */}
              <div className="flex items-center justify-between px-4 py-2 border-t border-surface0 text-xs text-overlay0">
                <span>↑↓ Navigate</span>
                <span>↵ Select</span>
                <span>esc Close</span>
              </div>
            </Command>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
```

---

## Mobile Experience

### Layout Structure (<768px)

```
┌─────────────────────┐
│ HEADER (☰ name ⚙️)  │  48px
├─────────────────────┤
│                     │
│  MAIN CONTENT       │
│  (scrollable)       │
│                     │
│                     │
├─────────────────────┤
│ FLOATING ACTION BAR │  56px
│ 💬 Send prompt...   │
├─────────────────────┤
│ BOTTOM NAV          │  64px
│ Home│Sess│Work│More │
└─────────────────────┘
```

### Touch-First Optimizations

| Pattern | Implementation |
|---------|----------------|
| Touch targets | Minimum 44x44px |
| Swipe gestures | Left: back, Right: actions |
| Pull-to-refresh | Native refresh pattern |
| Haptic feedback | On significant actions |
| Sheet modals | Instead of dialogs |
| Reduced payloads | Paginate, lazy load |
| Infinite scroll | For long lists |

### Mobile Prompt Sheet

```tsx
// components/mobile/prompt-sheet.tsx
'use client';

import { useState, useRef, useEffect } from 'react';
import { motion, AnimatePresence, useDragControls } from 'framer-motion';
import { Send, Loader2 } from 'lucide-react';

interface PromptSheetProps {
  sessionName: string;
  onSend: (prompt: string, target: string) => Promise<void>;
}

export function PromptSheet({ sessionName, onSend }: PromptSheetProps) {
  const [isExpanded, setIsExpanded] = useState(false);
  const [prompt, setPrompt] = useState('');
  const [target, setTarget] = useState<'all' | 'cc' | 'cod' | 'gmi'>('all');
  const [isSending, setIsSending] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const dragControls = useDragControls();

  useEffect(() => {
    if (isExpanded && textareaRef.current) {
      textareaRef.current.focus();
    }
  }, [isExpanded]);

  const handleSend = async () => {
    if (!prompt.trim() || isSending) return;
    
    setIsSending(true);
    try {
      await onSend(prompt, target);
      setPrompt('');
      setIsExpanded(false);
    } finally {
      setIsSending(false);
    }
  };

  return (
    <>
      {/* Collapsed bar */}
      <AnimatePresence>
        {!isExpanded && (
          <motion.button
            initial={{ y: 100 }}
            animate={{ y: 0 }}
            exit={{ y: 100 }}
            onClick={() => setIsExpanded(true)}
            className="fixed bottom-20 left-4 right-4 h-14 bg-surface0 rounded-2xl flex items-center gap-3 px-4 shadow-lg"
          >
            <span className="text-mauve">💬</span>
            <span className="text-overlay0">Send prompt to {sessionName}...</span>
          </motion.button>
        )}
      </AnimatePresence>

      {/* Expanded sheet */}
      <AnimatePresence>
        {isExpanded && (
          <>
            {/* Backdrop */}
            <motion.div
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              className="fixed inset-0 bg-black/50 z-40"
              onClick={() => setIsExpanded(false)}
            />
            
            {/* Sheet */}
            <motion.div
              initial={{ y: '100%' }}
              animate={{ y: 0 }}
              exit={{ y: '100%' }}
              transition={{ type: 'spring', damping: 25, stiffness: 300 }}
              drag="y"
              dragControls={dragControls}
              dragConstraints={{ top: 0, bottom: 0 }}
              dragElastic={{ top: 0, bottom: 0.5 }}
              onDragEnd={(_, info) => {
                if (info.offset.y > 100) setIsExpanded(false);
              }}
              className="fixed bottom-0 left-0 right-0 bg-base rounded-t-3xl z-50 pb-safe"
            >
              {/* Drag handle */}
              <div
                className="flex justify-center py-3 cursor-grab active:cursor-grabbing"
                onPointerDown={(e) => dragControls.start(e)}
              >
                <div className="w-10 h-1 rounded-full bg-surface2" />
              </div>

              <div className="px-4 pb-4 space-y-4">
                {/* Target pills */}
                <div className="flex gap-2">
                  {(['all', 'cc', 'cod', 'gmi'] as const).map((t) => (
                    <button
                      key={t}
                      onClick={() => setTarget(t)}
                      className={`px-4 py-2 rounded-full text-sm font-medium transition-all ${
                        target === t
                          ? t === 'cc'
                            ? 'bg-mauve text-base'
                            : t === 'cod'
                            ? 'bg-blue text-base'
                            : t === 'gmi'
                            ? 'bg-yellow text-base'
                            : 'bg-green text-base'
                          : 'bg-surface0 text-subtext1'
                      }`}
                    >
                      {t === 'all' ? 'All Agents' : t.toUpperCase()}
                    </button>
                  ))}
                </div>

                {/* Textarea */}
                <textarea
                  ref={textareaRef}
                  value={prompt}
                  onChange={(e) => setPrompt(e.target.value)}
                  placeholder={`Send to ${target === 'all' ? 'all agents' : target.toUpperCase()}...`}
                  className="w-full h-32 p-3 bg-surface0 rounded-xl text-text placeholder:text-overlay0 resize-none focus:outline-none focus:ring-2 focus:ring-mauve"
                />

                {/* Send button */}
                <button
                  onClick={handleSend}
                  disabled={!prompt.trim() || isSending}
                  className="w-full h-12 bg-mauve text-base rounded-xl font-medium flex items-center justify-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  {isSending ? (
                    <Loader2 className="w-5 h-5 animate-spin" />
                  ) : (
                    <>
                      <Send className="w-5 h-5" />
                      Send
                    </>
                  )}
                </button>
              </div>
            </motion.div>
          </>
        )}
      </AnimatePresence>
    </>
  );
}
```

### Bottom Navigation

```tsx
// components/mobile/bottom-nav.tsx
'use client';

import { usePathname } from 'next/navigation';
import Link from 'next/link';
import { Home, Layers, ListTodo, MoreHorizontal } from 'lucide-react';

const navItems = [
  { href: '/', icon: Home, label: 'Home' },
  { href: '/sessions', icon: Layers, label: 'Sessions' },
  { href: '/work', icon: ListTodo, label: 'Work' },
  { href: '/more', icon: MoreHorizontal, label: 'More' },
];

export function BottomNav() {
  const pathname = usePathname();

  return (
    <nav className="fixed bottom-0 left-0 right-0 h-16 bg-mantle border-t border-surface0 flex items-center justify-around pb-safe md:hidden">
      {navItems.map(({ href, icon: Icon, label }) => {
        const isActive = pathname === href || pathname.startsWith(`${href}/`);
        
        return (
          <Link
            key={href}
            href={href}
            className={`flex flex-col items-center gap-1 py-2 px-4 transition-colors ${
              isActive ? 'text-mauve' : 'text-overlay0'
            }`}
          >
            <Icon className="w-6 h-6" />
            <span className="text-xs">{label}</span>
          </Link>
        );
      })}
    </nav>
  );
}
```

---

## Component Library

### Base Components

#### Button

```tsx
// components/ui/button.tsx
import { forwardRef } from 'react';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '@/lib/utils';

const buttonVariants = cva(
  'inline-flex items-center justify-center rounded-lg font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-mauve disabled:pointer-events-none disabled:opacity-50',
  {
    variants: {
      variant: {
        default: 'bg-mauve text-base hover:bg-mauve/90',
        secondary: 'bg-surface0 text-text hover:bg-surface1',
        outline: 'border border-surface1 bg-transparent hover:bg-surface0',
        ghost: 'hover:bg-surface0',
        destructive: 'bg-red text-base hover:bg-red/90',
      },
      size: {
        sm: 'h-8 px-3 text-sm',
        default: 'h-10 px-4',
        lg: 'h-12 px-6 text-lg',
        icon: 'h-10 w-10',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  }
);

interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, ...props }, ref) => (
    <button
      ref={ref}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
);
Button.displayName = 'Button';
```

#### Badge

```tsx
// components/ui/badge.tsx
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '@/lib/utils';

const badgeVariants = cva(
  'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium transition-colors',
  {
    variants: {
      variant: {
        default: 'bg-surface0 text-text',
        claude: 'bg-mauve/20 text-mauve',
        codex: 'bg-blue/20 text-blue',
        gemini: 'bg-yellow/20 text-yellow',
        user: 'bg-green/20 text-green',
        success: 'bg-green/20 text-green',
        warning: 'bg-yellow/20 text-yellow',
        error: 'bg-red/20 text-red',
        info: 'bg-blue/20 text-blue',
      },
    },
    defaultVariants: {
      variant: 'default',
    },
  }
);

interface BadgeProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <div className={cn(badgeVariants({ variant }), className)} {...props} />;
}
```

### Agent Components

#### Agent State Indicator

```tsx
// components/agent/agent-state-indicator.tsx
'use client';

import { motion } from 'framer-motion';
import { cn } from '@/lib/utils';

type AgentState = 'waiting' | 'generating' | 'thinking' | 'error' | 'stalled';

interface AgentStateIndicatorProps {
  state: AgentState;
  className?: string;
}

const stateConfig: Record<AgentState, { color: string; label: string; pulse: boolean }> = {
  waiting: { color: 'bg-green', label: 'Waiting', pulse: false },
  generating: { color: 'bg-blue', label: 'Generating', pulse: true },
  thinking: { color: 'bg-yellow', label: 'Thinking', pulse: true },
  error: { color: 'bg-red', label: 'Error', pulse: false },
  stalled: { color: 'bg-red', label: 'Stalled', pulse: true },
};

export function AgentStateIndicator({ state, className }: AgentStateIndicatorProps) {
  const config = stateConfig[state];

  return (
    <div className={cn('flex items-center gap-2', className)}>
      <span className="relative flex h-2.5 w-2.5">
        {config.pulse && (
          <motion.span
            className={cn('absolute inline-flex h-full w-full rounded-full opacity-75', config.color)}
            animate={{ scale: [1, 1.5, 1], opacity: [0.75, 0, 0.75] }}
            transition={{ duration: 1.5, repeat: Infinity }}
          />
        )}
        <span className={cn('relative inline-flex h-2.5 w-2.5 rounded-full', config.color)} />
      </span>
      <span className="text-sm text-subtext0">{config.label}</span>
    </div>
  );
}
```

#### Context Meter

```tsx
// components/agent/context-meter.tsx
'use client';

import { useMemo } from 'react';
import { cn } from '@/lib/utils';

interface ContextMeterProps {
  used: number;
  total: number;
  showLabel?: boolean;
  className?: string;
}

export function ContextMeter({ used, total, showLabel = true, className }: ContextMeterProps) {
  const percentage = useMemo(() => Math.round((used / total) * 100), [used, total]);
  
  const colorClass = useMemo(() => {
    if (percentage < 40) return 'bg-green';
    if (percentage < 60) return 'bg-yellow';
    if (percentage < 80) return 'bg-peach';
    return 'bg-red';
  }, [percentage]);

  return (
    <div className={cn('space-y-1', className)}>
      {showLabel && (
        <div className="flex justify-between text-xs text-subtext0">
          <span>Context</span>
          <span>{percentage}%</span>
        </div>
      )}
      <div className="h-1.5 bg-surface0 rounded-full overflow-hidden">
        <div
          className={cn('h-full rounded-full transition-all duration-300', colorClass)}
          style={{ width: `${percentage}%` }}
        />
      </div>
    </div>
  );
}
```

#### Velocity Badge

```tsx
// components/agent/velocity-badge.tsx
import { TrendingUp, TrendingDown, Minus } from 'lucide-react';
import { cn } from '@/lib/utils';

interface VelocityBadgeProps {
  tokensPerMinute: number;
  trend?: 'up' | 'down' | 'stable';
  className?: string;
}

export function VelocityBadge({ tokensPerMinute, trend = 'stable', className }: VelocityBadgeProps) {
  const TrendIcon = trend === 'up' ? TrendingUp : trend === 'down' ? TrendingDown : Minus;
  const trendColor = trend === 'up' ? 'text-green' : trend === 'down' ? 'text-red' : 'text-overlay0';

  return (
    <div className={cn('inline-flex items-center gap-1 px-2 py-1 rounded bg-surface0', className)}>
      <span className="text-xs font-mono text-text">{tokensPerMinute.toLocaleString()}</span>
      <span className="text-xs text-subtext0">tok/m</span>
      <TrendIcon className={cn('w-3 h-3', trendColor)} />
    </div>
  );
}
```

### Session Components

#### Session Card

```tsx
// components/session/session-card.tsx
'use client';

import { formatDistanceToNow } from 'date-fns';
import { Layers, Clock, Activity } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { AgentStateIndicator } from '@/components/agent/agent-state-indicator';
import { cn } from '@/lib/utils';

interface Agent {
  type: 'claude' | 'codex' | 'gemini';
  state: 'waiting' | 'generating' | 'thinking' | 'error' | 'stalled';
}

interface SessionCardProps {
  name: string;
  agents: Agent[];
  createdAt: Date;
  lastActivity?: Date;
  isActive?: boolean;
  onClick?: () => void;
}

const agentVariant: Record<string, 'claude' | 'codex' | 'gemini'> = {
  claude: 'claude',
  codex: 'codex',
  gemini: 'gemini',
};

export function SessionCard({
  name,
  agents,
  createdAt,
  lastActivity,
  isActive,
  onClick,
}: SessionCardProps) {
  const activeAgents = agents.filter((a) => a.state === 'generating' || a.state === 'thinking');

  return (
    <button
      onClick={onClick}
      className={cn(
        'w-full p-4 rounded-xl border transition-all text-left',
        isActive
          ? 'border-mauve bg-mauve/5'
          : 'border-surface0 bg-surface0/50 hover:border-surface1 hover:bg-surface0'
      )}
    >
      {/* Header */}
      <div className="flex items-start justify-between mb-3">
        <div className="flex items-center gap-2">
          <Layers className="w-4 h-4 text-overlay0" />
          <span className="font-medium text-text">{name}</span>
        </div>
        {activeAgents.length > 0 && (
          <Badge variant="info">
            <Activity className="w-3 h-3 mr-1" />
            {activeAgents.length} active
          </Badge>
        )}
      </div>

      {/* Agents */}
      <div className="flex flex-wrap gap-2 mb-3">
        {agents.map((agent, i) => (
          <div
            key={i}
            className="flex items-center gap-2 px-2 py-1 rounded-lg bg-base"
          >
            <Badge variant={agentVariant[agent.type]} className="px-1.5">
              {agent.type === 'claude' ? 'CC' : agent.type === 'codex' ? 'COD' : 'GMI'}
            </Badge>
            <AgentStateIndicator state={agent.state} />
          </div>
        ))}
      </div>

      {/* Footer */}
      <div className="flex items-center gap-4 text-xs text-overlay0">
        <span className="flex items-center gap-1">
          <Clock className="w-3 h-3" />
          Created {formatDistanceToNow(createdAt, { addSuffix: true })}
        </span>
        {lastActivity && (
          <span>
            Last activity {formatDistanceToNow(lastActivity, { addSuffix: true })}
          </span>
        )}
      </div>
    </button>
  );
}
```

#### Pane Output Viewer

```tsx
// components/session/pane-output.tsx
'use client';

import { useRef, useEffect, useState, useCallback } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import AnsiToHtml from 'ansi-to-html';
import { cn } from '@/lib/utils';

interface PaneOutputProps {
  lines: string[];
  className?: string;
  autoScroll?: boolean;
}

const ansiConverter = new AnsiToHtml({
  fg: '#cdd6f4',
  bg: '#1e1e2e',
  colors: {
    0: '#45475a', 1: '#f38ba8', 2: '#a6e3a1', 3: '#f9e2af',
    4: '#89b4fa', 5: '#cba6f7', 6: '#94e2d5', 7: '#bac2de',
    8: '#585b70', 9: '#f38ba8', 10: '#a6e3a1', 11: '#f9e2af',
    12: '#89b4fa', 13: '#cba6f7', 14: '#94e2d5', 15: '#a6adc8',
  },
});

export function PaneOutput({ lines, className, autoScroll = true }: PaneOutputProps) {
  const parentRef = useRef<HTMLDivElement>(null);
  const [userScrolled, setUserScrolled] = useState(false);

  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 20,
    overscan: 20,
  });

  const scrollToBottom = useCallback(() => {
    if (parentRef.current && autoScroll && !userScrolled) {
      parentRef.current.scrollTop = parentRef.current.scrollHeight;
    }
  }, [autoScroll, userScrolled]);

  useEffect(() => {
    scrollToBottom();
  }, [lines.length, scrollToBottom]);

  const handleScroll = useCallback((e: React.UIEvent<HTMLDivElement>) => {
    const el = e.currentTarget;
    const isAtBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50;
    setUserScrolled(!isAtBottom);
  }, []);

  return (
    <div
      ref={parentRef}
      onScroll={handleScroll}
      className={cn(
        'h-full overflow-auto bg-crust rounded-lg font-mono text-sm',
        className
      )}
    >
      <div
        style={{
          height: `${virtualizer.getTotalSize()}px`,
          width: '100%',
          position: 'relative',
        }}
      >
        {virtualizer.getVirtualItems().map((virtualItem) => (
          <div
            key={virtualItem.key}
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              width: '100%',
              height: `${virtualItem.size}px`,
              transform: `translateY(${virtualItem.start}px)`,
            }}
            className="px-3 py-0.5 whitespace-pre-wrap break-all"
            dangerouslySetInnerHTML={{
              __html: ansiConverter.toHtml(lines[virtualItem.index]),
            }}
          />
        ))}
      </div>

      {/* Scroll to bottom indicator */}
      {userScrolled && (
        <button
          onClick={() => {
            setUserScrolled(false);
            scrollToBottom();
          }}
          className="fixed bottom-4 right-4 px-3 py-1.5 rounded-full bg-surface0 text-sm text-text shadow-lg hover:bg-surface1 transition-colors"
        >
          ↓ New output
        </button>
      )}
    </div>
  );
}
```

---

## Real-Time Integration

### TanStack Query + WebSocket Pattern

```typescript
// lib/api/queries.ts
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';
import { useWebSocket } from '@/lib/hooks/useWebSocket';
import { apiClient } from './client';

export function useSession(name: string) {
  const queryClient = useQueryClient();
  
  const query = useQuery({
    queryKey: ['sessions', name],
    queryFn: () => apiClient.getSession(name),
    staleTime: 30_000,
  });

  const { isConnected } = useWebSocket({
    url: `${process.env.NEXT_PUBLIC_WS_URL}/api/v1/ws/sessions/${name}/watch`,
    onMessage: (msg) => {
      if (msg.event === 'agent.state' || msg.event === 'pane.state') {
        queryClient.invalidateQueries({ queryKey: ['sessions', name] });
      }
      if (msg.event === 'pane.output') {
        queryClient.setQueryData(['sessions', name, 'output'], (old: any) => {
          if (!old) return old;
          const paneIndex = msg.params?.index;
          return {
            ...old,
            panes: old.panes.map((p: any, i: number) =>
              i === paneIndex
                ? { ...p, lines: [...p.lines, msg.params?.content] }
                : p
            ),
          };
        });
      }
    },
  });

  return { ...query, isConnected };
}

export function useSessionActivity(name: string) {
  return useQuery({
    queryKey: ['sessions', name, 'activity'],
    queryFn: () => apiClient.getSessionActivity(name),
    refetchInterval: 5_000, // Poll every 5s as backup
  });
}

export function useSessions() {
  const queryClient = useQueryClient();

  const query = useQuery({
    queryKey: ['sessions'],
    queryFn: () => apiClient.getSessions(),
    staleTime: 10_000,
  });

  useWebSocket({
    url: `${process.env.NEXT_PUBLIC_WS_URL}/api/v1/ws/global`,
    onMessage: (msg) => {
      if (msg.event === 'session.created' || msg.event === 'session.killed') {
        queryClient.invalidateQueries({ queryKey: ['sessions'] });
      }
    },
  });

  return query;
}
```

### Batched WebSocket Messages

```typescript
// lib/hooks/useBatchedWebSocket.ts
import { useRef, useCallback, useEffect, useState } from 'react';

interface UseBatchedWebSocketOptions {
  url: string;
  onBatch: (messages: WebSocketMessage[]) => void;
  maxBatchSize?: number;
  maxWaitMs?: number;
}

export function useBatchedWebSocket({
  url,
  onBatch,
  maxBatchSize = 100,
  maxWaitMs = 50,
}: UseBatchedWebSocketOptions) {
  const ws = useRef<WebSocket | null>(null);
  const batch = useRef<WebSocketMessage[]>([]);
  const timer = useRef<NodeJS.Timeout | null>(null);
  const [isConnected, setIsConnected] = useState(false);

  const flush = useCallback(() => {
    if (batch.current.length > 0) {
      onBatch([...batch.current]);
      batch.current = [];
    }
    if (timer.current) {
      clearTimeout(timer.current);
      timer.current = null;
    }
  }, [onBatch]);

  const addToBatch = useCallback(
    (message: WebSocketMessage) => {
      batch.current.push(message);

      if (batch.current.length >= maxBatchSize) {
        flush();
      } else if (!timer.current) {
        timer.current = setTimeout(flush, maxWaitMs);
      }
    },
    [flush, maxBatchSize, maxWaitMs]
  );

  useEffect(() => {
    ws.current = new WebSocket(url);

    ws.current.onopen = () => setIsConnected(true);
    ws.current.onclose = () => setIsConnected(false);
    ws.current.onmessage = (event) => {
      const message = JSON.parse(event.data);
      addToBatch(message);
    };

    return () => {
      flush();
      ws.current?.close();
    };
  }, [url, addToBatch, flush]);

  return { isConnected };
}
```

---

## Security Architecture

### Authentication Flow

```typescript
// lib/auth/auth-context.tsx
'use client';

import { createContext, useContext, useState, useEffect, useCallback } from 'react';

interface AuthState {
  token: string | null;
  isAuthenticated: boolean;
  isLoading: boolean;
}

interface AuthContextValue extends AuthState {
  login: (username: string, password: string) => Promise<void>;
  logout: () => void;
  refreshToken: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [state, setState] = useState<AuthState>({
    token: null,
    isAuthenticated: false,
    isLoading: true,
  });

  useEffect(() => {
    const stored = localStorage.getItem('ntm_token');
    if (stored) {
      const { token, expires } = JSON.parse(stored);
      if (new Date(expires) > new Date()) {
        setState({ token, isAuthenticated: true, isLoading: false });
      } else {
        localStorage.removeItem('ntm_token');
        setState({ token: null, isAuthenticated: false, isLoading: false });
      }
    } else {
      setState((s) => ({ ...s, isLoading: false }));
    }
  }, []);

  const login = useCallback(async (username: string, password: string) => {
    const res = await fetch('/api/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });

    if (!res.ok) throw new Error('Login failed');

    const { token, expires_at } = await res.json();
    localStorage.setItem('ntm_token', JSON.stringify({ token, expires: expires_at }));
    setState({ token, isAuthenticated: true, isLoading: false });
  }, []);

  const logout = useCallback(() => {
    localStorage.removeItem('ntm_token');
    setState({ token: null, isAuthenticated: false, isLoading: false });
  }, []);

  const refreshToken = useCallback(async () => {
    const res = await fetch('/api/v1/auth/refresh', {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${state.token}`,
      },
    });

    if (!res.ok) {
      logout();
      throw new Error('Refresh failed');
    }

    const { token, expires_at } = await res.json();
    localStorage.setItem('ntm_token', JSON.stringify({ token, expires: expires_at }));
    setState((s) => ({ ...s, token }));
  }, [state.token, logout]);

  return (
    <AuthContext.Provider value={{ ...state, login, logout, refreshToken }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within AuthProvider');
  return ctx;
}
```

### API Security Middleware (Go)

```go
// internal/api/middleware.go
package api

import (
    "context"
    "net/http"
    "strings"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "golang.org/x/time/rate"
)

type contextKey string

const userContextKey contextKey = "user"

// JWTMiddleware validates JWT tokens
func JWTMiddleware(secret []byte) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Skip auth for certain paths
            if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
                next.ServeHTTP(w, r)
                return
            }

            auth := r.Header.Get("Authorization")
            if !strings.HasPrefix(auth, "Bearer ") {
                http.Error(w, "Unauthorized", http.StatusUnauthorized)
                return
            }

            tokenStr := strings.TrimPrefix(auth, "Bearer ")
            token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
                return secret, nil
            })

            if err != nil || !token.Valid {
                http.Error(w, "Invalid token", http.StatusUnauthorized)
                return
            }

            claims := token.Claims.(jwt.MapClaims)
            ctx := context.WithValue(r.Context(), userContextKey, claims["sub"])
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// RateLimitMiddleware limits requests per client
func RateLimitMiddleware(rps float64, burst int) func(http.Handler) http.Handler {
    limiters := make(map[string]*rate.Limiter)
    var mu sync.Mutex

    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ip := r.RemoteAddr

            mu.Lock()
            limiter, exists := limiters[ip]
            if !exists {
                limiter = rate.NewLimiter(rate.Limit(rps), burst)
                limiters[ip] = limiter
            }
            mu.Unlock()

            if !limiter.Allow() {
                http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
                return
            }

            next.ServeHTTP(w, r)
        })
    }
}

// CORSMiddleware handles CORS headers
func CORSMiddleware(origins []string) func(http.Handler) http.Handler {
    allowed := make(map[string]bool)
    for _, o := range origins {
        allowed[o] = true
    }

    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            origin := r.Header.Get("Origin")
            
            if allowed[origin] || allowed["*"] {
                w.Header().Set("Access-Control-Allow-Origin", origin)
                w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
                w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
                w.Header().Set("Access-Control-Max-Age", "86400")
            }

            if r.Method == "OPTIONS" {
                w.WriteHeader(http.StatusNoContent)
                return
            }

            next.ServeHTTP(w, r)
        })
    }
}
```

---

## Performance Optimizations

### Server-Side

| Optimization | Implementation |
|--------------|----------------|
| Response compression | gzip/brotli middleware |
| Connection pooling | Pool tmux command connections |
| In-memory cache | Cache session state (30s TTL) |
| Rate limiting | 600 req/min, burst 50 |
| Batch pane captures | Aggregate multiple pane reads |

### Client-Side

| Optimization | Implementation |
|--------------|----------------|
| Route-based code splitting | Next.js dynamic imports |
| Virtual lists | @tanstack/react-virtual for output |
| Debounced WebSocket | Batch updates every 50ms |
| Service worker | Offline support, caching |
| Optimistic UI | Immediate feedback, reconcile later |
| Image optimization | Next.js Image component |

---

## Agent Protocol Integration

### ACP (Agent Client Protocol)

The Agent Client Protocol provides editor-agnostic agent control:

```typescript
// lib/acp/client.ts
interface ACPMessage {
  jsonrpc: '2.0';
  id: string | number;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

interface ACPTool {
  name: string;
  description: string;
  inputSchema: object;
}

const standardTools: ACPTool[] = [
  {
    name: 'read_text_file',
    description: 'Read contents of a text file',
    inputSchema: {
      type: 'object',
      properties: {
        path: { type: 'string' },
        start_line: { type: 'number' },
        end_line: { type: 'number' },
      },
      required: ['path'],
    },
  },
  {
    name: 'write_text_file',
    description: 'Write contents to a text file',
    inputSchema: {
      type: 'object',
      properties: {
        path: { type: 'string' },
        content: { type: 'string' },
      },
      required: ['path', 'content'],
    },
  },
  {
    name: 'edit_text_file',
    description: 'Edit a text file with search/replace',
    inputSchema: {
      type: 'object',
      properties: {
        path: { type: 'string' },
        edits: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              old_text: { type: 'string' },
              new_text: { type: 'string' },
            },
          },
        },
      },
      required: ['path', 'edits'],
    },
  },
];
```

### Claude Agent SDK Integration

```typescript
// lib/agents/claude.ts
import { query } from '@anthropic-ai/claude-agent-sdk';

interface ClaudeQueryOptions {
  prompt: string;
  cwd: string;
  model?: 'sonnet' | 'opus' | 'haiku';
  allowedTools?: string[];
  onMessage?: (message: StreamMessage) => void;
}

export async function runClaudeAgent({
  prompt,
  cwd,
  model = 'sonnet',
  allowedTools = ['Read', 'Write', 'Edit', 'Bash'],
  onMessage,
}: ClaudeQueryOptions) {
  const stream = query({
    prompt,
    options: {
      model,
      allowedTools,
      cwd,
    },
  });

  const messages: StreamMessage[] = [];

  for await (const message of stream) {
    messages.push(message);
    onMessage?.(message);
  }

  return messages;
}
```

---

## Deployment

### Docker Configuration

```dockerfile
# Dockerfile
FROM golang:1.23-alpine AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o ntm ./cmd/ntm

FROM oven/bun:1.1 AS web-builder
WORKDIR /app/web
COPY web/package.json web/bun.lockb ./
RUN bun install --frozen-lockfile
COPY web/ .
RUN bun run build

FROM alpine:3.19
RUN apk add --no-cache tmux nodejs npm
WORKDIR /app

COPY --from=go-builder /app/ntm /usr/local/bin/
COPY --from=web-builder /app/web/.next/standalone ./web/
COPY --from=web-builder /app/web/.next/static ./web/.next/static
COPY --from=web-builder /app/web/public ./web/public

EXPOSE 8765 3000

CMD ["ntm", "serve", "--with-web"]
```

### Configuration File

```toml
# ~/.config/ntm/config.toml

[api]
enabled = true
port = 8765
cors_origins = ["http://localhost:3000", "https://ntm.example.com"]

[api.auth]
enabled = false  # Set true for production
jwt_secret = ""  # Auto-generated if empty

[api.rate_limit]
requests_per_minute = 600
burst = 50

[api.websocket]
ping_interval = "30s"
pong_timeout = "10s"
max_message_size = 65536

[web]
enabled = true
port = 3000
```

---

## Implementation Roadmap

### Phase 1: Foundation (Weeks 1-3)

**Goal**: API server infrastructure and core endpoints

| Week | Deliverables |
|------|--------------|
| 1 | Chi router setup, middleware (CORS, rate limiting), OpenAPI spec |
| 2 | Session endpoints (create, list, get, delete), WebSocket hub |
| 3 | Agent endpoints (add, send, interrupt), output endpoints |

### Phase 2: Real-Time (Weeks 4-6)

**Goal**: WebSocket streaming and robot mode

| Week | Deliverables |
|------|--------------|
| 4 | Pane output streaming, agent state events |
| 5 | Robot mode endpoints (status, context, snapshot) |
| 6 | Bead management, CASS search, event filtering |

### Phase 3: Web UI Foundation (Weeks 7-10)

**Goal**: Next.js setup and core components

| Week | Deliverables |
|------|--------------|
| 7 | Next.js 16 setup, App Router, TanStack Query |
| 8 | Theme system (Catppuccin), WebSocket provider |
| 9 | Session list, session card, agent badges |
| 10 | Pane output viewer with virtual scrolling |

### Phase 4: Desktop Experience (Weeks 11-14)

**Goal**: Full desktop UI with command palette

| Week | Deliverables |
|------|--------------|
| 11 | Command palette with fuzzy search |
| 12 | Session detail view, agent grid |
| 13 | Work management views, notifications |
| 14 | Analytics charts, dashboard polish |

### Phase 5: Mobile Experience (Weeks 15-17)

**Goal**: Touch-optimized mobile UI

| Week | Deliverables |
|------|--------------|
| 15 | Responsive layout, bottom navigation |
| 16 | Mobile prompt sheet, swipe gestures |
| 17 | Offline indicators, reduced payloads |

### Phase 6: Polish & Launch (Weeks 18-20)

**Goal**: Production readiness

| Week | Deliverables |
|------|--------------|
| 18 | Virtual list optimization, bundle splitting |
| 19 | Service worker, load testing |
| 20 | Documentation, deployment guides |

---

## Success Criteria

| Metric | Target |
|--------|--------|
| Feature Parity | Every CLI command accessible via REST API |
| Real-Time Latency | <100ms for WebSocket events |
| Initial Page Load | <2s on 3G connection |
| Interaction Response | <200ms for all UI actions |
| Mobile Support | Fully functional on iOS Safari, Chrome Android |
| Accessibility | WCAG 2.1 AA compliance |
| Uptime | 99.9% availability |
| Error Rate | <0.1% of API requests |

---

## Appendix: Key File Paths

### Backend

```
internal/api/server.go          # HTTP server setup
internal/api/router.go          # Route definitions
internal/api/websocket.go       # WebSocket hub
internal/api/pane_stream.go     # Pane output streaming
internal/api/handlers/*.go      # Endpoint handlers
internal/acp/server.go          # ACP protocol
```

### Frontend

```
app/(dashboard)/layout.tsx      # Dashboard layout
app/(dashboard)/sessions/       # Session pages
components/ui/                  # Base components
components/agent/               # Agent components
components/session/             # Session components
components/palette/             # Command palette
components/mobile/              # Mobile components
lib/api/client.ts              # API client
lib/api/queries.ts             # TanStack Query hooks
lib/hooks/useWebSocket.ts      # WebSocket hook
```

### Configuration

```
~/.config/ntm/config.toml      # NTM configuration
web/next.config.ts             # Next.js config
web/tailwind.config.ts         # Tailwind + theme
api/openapi.yaml               # OpenAPI specification
```

---

**Document Version**: 1.0.0  
**Total Implementation Time**: 20 weeks  
**Status**: Ready for Implementation
