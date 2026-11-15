import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

class Wave9MockWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = Wave9MockWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  send = vi.fn();
  close = vi.fn();
}

Object.defineProperty(global, "WebSocket", { value: Wave9MockWebSocket });

import AttentionPage from "../app/(dashboard)/attention/page";
import SessionsPage from "../app/(dashboard)/sessions/page";
import SafetyPage from "../app/(dashboard)/safety/page";
import MailPage from "../app/(dashboard)/mail/page";
import { BeadsActionsAndPlan } from "../components/beads/BeadsActionsAndPlan";
import { resetClient } from "../lib/api/client";
import { resetQueryClient } from "../lib/hooks/use-query";
import { resetWsClient } from "../lib/ws/client";

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
  navigationState.pathname = "/";
  navigationState.searchParams = new URLSearchParams();
  navigationState.replace.mockReset();
  navigationState.push.mockReset();
  localStorageMock.clear();
  resetClient();
  resetQueryClient();
  resetWsClient();
});

afterEach(() => {
  cleanup();
  while (clients.length > 0) {
    clients.pop()?.clear();
  }
  vi.unstubAllGlobals();
});

describe("Wave 9 Web Cockpit Surfaces", () => {
  it("triages durable operator incidents on AttentionPage (list, inspect timeline, investigate, resolve)", async () => {
    const incidentState = {
      id: "inc-stuck-99",
      title: "Agent cod_2 context window exhausted repeatedly",
      fingerprint: "fp:context_exhausted:cod_2",
      family: "context_exhausted",
      category: "agent",
      status: "open",
      severity: "critical",
      session_names: "alpha-swarm",
      event_count: 4,
      alert_count: 2,
      first_event_cursor: 12,
      last_event_cursor: 19,
      started_at: "2026-11-15T10:00:00Z",
      last_event_at: "2026-11-15T10:05:00Z",
    };
    const statusCalls: Array<Record<string, unknown>> = [];
    const resolveCalls: Array<Record<string, unknown>> = [];

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

        if (url.pathname === "/api/v1/attention/digest") {
          return reply({ event_count: 0, cursor_end: 19 });
        }
        if (url.pathname === "/api/v1/attention/events") {
          return reply({ events: [], event_count: 0, oldest_cursor: 0, newest_cursor: 19 });
        }
        if (url.pathname === "/api/v1/incidents" && method === "GET") {
          const includeResolved = url.searchParams.get("include_resolved") === "true";
          const list =
            incidentState.status === "resolved" && !includeResolved
              ? []
              : [incidentState];
          return reply({ incidents: list, count: list.length });
        }
        if (url.pathname === "/api/v1/incidents/inc-stuck-99" && method === "GET") {
          return reply({
            incident: incidentState,
            event_count: 1,
            events: [
              {
                cursor: 19,
                ts: "2026-11-15T10:05:00Z",
                session_name: "alpha-swarm",
                category: "agent",
                event_type: "agent.context_exhausted",
                source: "scanner",
                actionability: "action_required",
                severity: "critical",
                summary: "Pane %2 context hit 98% token threshold",
                dedup_count: 1,
              },
            ],
          });
        }
        if (url.pathname === "/api/v1/incidents/inc-stuck-99/status" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>;
          statusCalls.push(parsed);
          incidentState.status = String(parsed.status ?? "investigating");
          return reply({
            incident_id: "inc-stuck-99",
            action: "investigate",
            previous_status: "open",
            status: incidentState.status,
            idempotent: false,
            incident: incidentState,
          });
        }
        if (url.pathname === "/api/v1/incidents/inc-stuck-99/resolve" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>;
          resolveCalls.push(parsed);
          incidentState.status = "resolved";
          return reply({
            incident_id: "inc-stuck-99",
            action: "resolve",
            previous_status: "investigating",
            status: "resolved",
            idempotent: false,
            incident: incidentState,
          });
        }
        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <AttentionPage />
      </QueryClientProvider>
    );

    const panel = screen.getByRole("region", { name: "Operator Incidents Triage" });
    expect(
      await within(panel).findByText("Agent cod_2 context window exhausted repeatedly")
    ).toBeInTheDocument();

    // Inspect linked timeline events
    fireEvent.click(within(panel).getByRole("button", { name: "Inspect incident inc-stuck-99" }));
    expect(
      await within(panel).findByText("Pane %2 context hit 98% token threshold")
    ).toBeInTheDocument();

    // Add triage note, mark investigating, then resolve
    fireEvent.change(within(panel).getByLabelText("Incident triage note"), {
      target: { value: "Rotated agent via handoff" },
    });
    fireEvent.click(
      within(panel).getByRole("button", { name: "Investigate incident inc-stuck-99" })
    );
    await waitFor(() => {
      expect(statusCalls).toContainEqual({
        status: "investigating",
        reason: "Rotated agent via handoff",
      });
    });

    fireEvent.click(
      within(panel).getByRole("button", { name: "Resolve incident inc-stuck-99" })
    );
    await waitFor(() => {
      expect(resolveCalls).toContainEqual({
        note: "Rotated agent via handoff",
      });
    });
  });

  it("renders session picker when no ?session= query param is set and creates manual + auto handoffs with quality scores", async () => {
    const handoffBodies: Array<Record<string, unknown>> = [];
    let latestGoal = "Completed Wave 8 ensemble workbench";

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

        if (url.pathname === "/api/v1/sessions" && method === "GET") {
          return reply({
            sessions: [
              { name: "ntm-main", status: "running", project_path: "/data/projects/ntm" },
            ],
            count: 1,
          });
        }
        if (url.pathname === "/api/v1/handoffs" && method === "GET") {
          return reply({
            action: "list",
            session: url.searchParams.get("session") ?? "all",
            sessions: ["ntm-main"],
            total_returned: 1,
            handoffs: [
              {
                path: ".ntm/handoffs/ntm-main/2026-11-15_handoff.yaml",
                session: "ntm-main",
                date: "2026-11-15",
                status: "complete",
                goal: latestGoal,
                is_auto: false,
              },
            ],
            latest: {
              Session: "ntm-main",
              Goal: latestGoal,
              Now: "Start Wave 9 surfaces",
              Status: "complete",
            },
            quality: {
              score: 92,
              status: "ready",
              scored_at: "2026-11-15T10:00:00Z",
              dimensions: [{ name: "completeness", score: 95 }],
            },
            ledger: {
              path: ".ntm/continuity/CONTINUITY_ntm-main.md",
              exists: true,
              bytes: 1840,
            },
          });
        }
        if (url.pathname === "/api/v1/handoffs" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>;
          handoffBodies.push(parsed);
          latestGoal =
            typeof parsed.goal === "string" && parsed.goal
              ? parsed.goal
              : "Auto-captured session snapshot";
          return reply({
            action: parsed.auto ? "create_auto" : "create",
            session: String(parsed.session ?? "ntm-main"),
            created_path: ".ntm/handoffs/ntm-main/new.yaml",
            sessions: ["ntm-main"],
            handoffs: [],
            total_returned: 1,
            quality: {
              score: 88,
              status: "ready",
              scored_at: "2026-11-15T10:10:00Z",
              dimensions: [{ name: "completeness", score: 88 }],
            },
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

    // Picker renders active tmux session link
    expect(await screen.findByText("Select a Tmux Session")).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /ntm-main/i })).toHaveAttribute(
      "href",
      "/sessions?session=ntm-main"
    );

    // Handoff quality badge + ledger status render
    expect(await screen.findByTestId("handoff-quality-badge")).toHaveTextContent(
      "Quality 92/100 · ready"
    );
    expect(screen.getByText("Continuity Ledger (1840 B)")).toBeInTheDocument();

    // Create manual handoff
    fireEvent.change(screen.getByLabelText("Goal (What was accomplished)"), {
      target: { value: "Wired Wave 9 incidents and handoffs" },
    });
    fireEvent.change(screen.getByLabelText("Now (Immediate next task)"), {
      target: { value: "Run headless Chrome qualification" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Handoff" }));

    await waitFor(() => {
      expect(handoffBodies).toContainEqual({
        session: "ntm-main",
        goal: "Wired Wave 9 incidents and handoffs",
        now: "Run headless Chrome qualification",
      });
    });

    // Auto-generate handoff
    fireEvent.click(screen.getByRole("button", { name: "Auto-Generate Handoff" }));
    await waitFor(() => {
      expect(handoffBodies).toContainEqual({
        session: "ntm-main",
        auto: true,
      });
    });
  });

  it("evaluates commands via Command Safety Simulator and submits SLB approval requests on SafetyPage", async () => {
    const checkCalls: Array<Record<string, unknown>> = [];
    const approvalReqCalls: Array<Record<string, unknown>> = [];

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

        if (url.pathname === "/api/v1/safety/status") {
          return reply({ installed: true, policy_path: ".ntm/policy.yaml", blocked_rules: 12, approval_rules: 4, allowed_rules: 8 });
        }
        if (url.pathname === "/api/v1/safety/blocked") {
          return reply({ entries: [], count: 0 });
        }
        if (url.pathname === "/api/v1/policy") {
          return reply({ version: 1, stats: { blocked: 12, approval: 4, allowed: 8 } });
        }
        if (url.pathname === "/api/v1/approvals") {
          return reply({ approvals: [], count: 0 });
        }
        if (url.pathname === "/api/v1/approvals/history") {
          return reply({ approvals: [], count: 0 });
        }
        if (url.pathname === "/api/v1/safety/check" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>;
          checkCalls.push(parsed);
          return reply({
            command: String(parsed.command ?? ""),
            action: "block",
            pattern: "git\\s+reset\\s+--hard",
            reason: "Destructive working-tree wipe blocked by policy",
            slb: true,
          });
        }
        if (url.pathname === "/api/v1/approvals/request" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>;
          approvalReqCalls.push(parsed);
          return reply({
            id: "appr-wave9-1",
            action: String(parsed.action ?? ""),
            status: "pending",
            requested_by: "operator",
            requested_at: "2026-11-15T10:00:00Z",
            expires_at: "2026-11-15T11:00:00Z",
          });
        }
        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <SafetyPage />
      </QueryClientProvider>
    );

    fireEvent.change(await screen.findByLabelText("Command to simulate"), {
      target: { value: "git reset --hard HEAD~1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Evaluate Command" }));

    expect(
      await screen.findByText(/Verdict: BLOCK \(SLB Two-Person Rule\)/)
    ).toBeInTheDocument();
    expect(checkCalls).toEqual([{ command: "git reset --hard HEAD~1" }]);

    fireEvent.change(screen.getByLabelText("Approval action"), {
      target: { value: "deploy production migration" },
    });
    fireEvent.change(screen.getByLabelText("Approval resource"), {
      target: { value: "ntm-prod" },
    });
    fireEvent.change(screen.getByLabelText("Approval reason"), {
      target: { value: "Verified dry-run preflight" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit Request" }));

    await waitFor(() => {
      expect(approvalReqCalls).toContainEqual({
        action: "deploy production migration",
        resource: "ntm-prod",
        reason: "Verified dry-run preflight",
        ttl_seconds: 3600,
      });
    });
  });

  it("searches Agent Mail across threads and renews/releases/force-releases file reservations on MailPage", async () => {
    const reservationMutations: string[] = [];
    const searchUrls: string[] = [];

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

        if (url.pathname === "/api/v1/mail/agents") {
          return reply({
            agents: [{ id: 1, name: "BlueLake", program: "claude-code", model: "opus" }],
            count: 1,
          });
        }
        if (url.pathname === "/api/v1/mail/inbox") {
          return reply({ messages: [], count: 0 });
        }
        if (url.pathname === "/api/v1/reservations") {
          return reply({
            reservations: [
              {
                id: 77,
                path_pattern: "internal/serve/**",
                agent_name: "BlueLake",
                exclusive: true,
                reason: "bd-8pjf5.3",
                created_ts: "2026-11-15T09:00:00Z",
                expires_ts: "2026-11-15T10:00:00Z",
              },
            ],
            count: 1,
          });
        }
        if (url.pathname === "/api/v1/reservations/conflicts") {
          return reply({ conflicts: [], has_conflicts: false });
        }
        if (url.pathname === "/api/v1/mail/search" && method === "GET") {
          searchUrls.push(`${url.pathname}${url.search}`);
          return reply({
            query: url.searchParams.get("q") ?? "",
            count: 1,
            results: [
              {
                id: 501,
                subject: "Handoff ready for bd-8pjf5",
                from: "GreenCastle",
                created_ts: "2026-11-15T09:30:00Z",
                thread_id: "bd-8pjf5",
                importance: "high",
                ack_required: false,
              },
            ],
          });
        }
        if (url.pathname.startsWith("/api/v1/reservations/77/") && method === "POST") {
          reservationMutations.push(`${url.pathname}${url.search}`);
          return reply({});
        }
        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <MailPage />
      </QueryClientProvider>
    );

    // Search cross-thread mail
    fireEvent.change(await screen.findByLabelText("Search Agent Mail"), {
      target: { value: "bd-8pjf5" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Search Mail" }));
    expect(
      await screen.findByText("Handoff ready for bd-8pjf5")
    ).toBeInTheDocument();
    expect(searchUrls).toEqual(["/api/v1/mail/search?q=bd-8pjf5&limit=25"]);

    // Renew, release, and two-step force-release reservation #77
    fireEvent.click(await screen.findByRole("button", { name: "Renew reservation 77" }));
    await waitFor(() => {
      expect(reservationMutations).toContain("/api/v1/reservations/77/renew");
    });

    fireEvent.click(screen.getByRole("button", { name: "Release reservation 77" }));
    await waitFor(() => {
      expect(reservationMutations).toContain(
        "/api/v1/reservations/77/release?agent_name=BlueLake"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Force release reservation 77" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm force release reservation 77" })
    );
    await waitFor(() => {
      expect(reservationMutations).toContain("/api/v1/reservations/77/force-release");
    });
  });

  it("renders BV parallel execution plan tracks, flushes JSONL sync, and creates beads via BeadsActionsAndPlan", async () => {
    const createCalls: Array<Record<string, unknown>> = [];
    let syncCount = 0;
    const notices: Array<{ type: string; message: string }> = [];
    const selectedBeads: string[] = [];

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

        if (url.pathname === "/api/v1/beads/plan" && method === "GET") {
          return reply({
            plan: {
              generated_at: "2026-11-15T10:00:00Z",
              plan: {
                tracks: [
                  {
                    track_id: "track-alpha",
                    reason: "Independent backend surface",
                    items: [
                      {
                        id: "bd-8pjf5.4",
                        title: "Wire Web Cockpit Wave 9 surfaces",
                        priority: 1,
                        status: "in_progress",
                        unblocks: ["bd-8pjf5"],
                      },
                    ],
                  },
                ],
              },
            },
          });
        }
        if (url.pathname === "/api/v1/beads/sync" && method === "POST") {
          syncCount += 1;
          return reply({ synced: true });
        }
        if (url.pathname === "/api/v1/beads" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>;
          createCalls.push(parsed);
          return reply({ bead: { id: "bd-new-101", title: String(parsed.title ?? "") } });
        }
        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <BeadsActionsAndPlan
          onNotice={(n) => notices.push(n)}
          onSelectBead={(id) => selectedBeads.push(id)}
        />
      </QueryClientProvider>
    );

    // Track renders and clicking item selects bead
    const trackBtn = await screen.findByText(/Wire Web Cockpit Wave 9 surfaces/);
    fireEvent.click(trackBtn);
    expect(selectedBeads).toEqual(["bd-8pjf5.4"]);

    // Sync JSONL
    fireEvent.click(screen.getByRole("button", { name: "Sync JSONL" }));
    await waitFor(() => expect(syncCount).toBe(1));

    // Open New Bead form and create bead
    fireEvent.click(screen.getByRole("button", { name: "New Bead" }));
    fireEvent.change(screen.getByLabelText("Title"), {
      target: { value: "Verify VSCode handoff command" },
    });
    fireEvent.change(screen.getByLabelText("New bead labels"), {
      target: { value: "vscode, wave9" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create Bead" }));

    await waitFor(() => {
      expect(createCalls).toContainEqual({
        title: "Verify VSCode handoff command",
        type: "task",
        priority: "1",
        labels: ["vscode", "wave9"],
      });
    });
  });
});
