import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WorkspaceConfigPanel } from "../components/overview/WorkspaceConfigPanel";
import { ProviderQuotaTelemetryPanel } from "../components/accounts/ProviderQuotaTelemetryPanel";
import { SwarmOrchestrationWorkbench } from "../components/swarm/SwarmOrchestrationWorkbench";
import { BeadsGraphAndDepsPanel } from "../components/beads/BeadsGraphAndDepsPanel";
import { SessionLifecycleControls } from "../components/sessions/SessionLifecycleControls";

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("Wave 12 Web Cockpit Surfaces", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
  });

  it("wires WorkspaceConfigPanel (GET/PATCH /api/v1/config) and ProviderQuotaTelemetryPanel (/api/v1/quota, /api/v1/accounts/status, /api/v1/accounts/{provider}/rotate)", async () => {
    const calls: Array<{ method: string; path: string; body?: unknown }> = [];
    let currentDir = "/data/projects/ntm";

    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === "string" ? input : input.toString(), "http://127.0.0.1:7337");
      const method = (init?.method || "GET").toUpperCase();
      const parsedBody = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path: url.pathname, body: parsedBody });

      if (url.pathname === "/api/v1/config" && method === "GET") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            host: "127.0.0.1",
            port: 7337,
            auth_mode: "local",
            project_dir: currentDir,
            startup_project_dir: "/data/projects/ntm",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/config" && method === "PATCH") {
        currentDir = String((parsedBody as { project_dir?: string })?.project_dir || "/data/projects/ntm");
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:01Z",
            updated: true,
            config: { project_dir: currentDir },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/quota") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:02Z",
            quota: {
              last_updated: "2026-11-16T05:00:02Z",
              caut_available: true,
              total_cost_today_usd: 14.25,
              total_cost_month_usd: 210.5,
              has_warning: true,
              has_critical: false,
              providers: {
                claude: {
                  usage_percent: 78.4,
                  tokens_used: 480000,
                  tokens_limit: 1000000,
                  cost_usd: 9.5,
                  status: "warning",
                  rate_limit: {
                    in_cooldown: false,
                    current_delay_ms: 250,
                    total_rate_limits: 1,
                    total_successes: 42,
                  },
                },
              },
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/accounts/status") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:02Z",
            accounts: {
              claude: {
                current: "max-seat-1",
                usage_percent: 78,
                available_accounts: 3,
                rate_limited: false,
              },
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/accounts/claude/rotate" && method === "POST") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:03Z",
            switch: {
              success: true,
              provider: "claude",
              previous_account: "max-seat-1",
              new_account: "max-seat-2",
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      throw new Error(`Unhandled request ${method} ${url.pathname}`);
    }) as unknown as typeof fetch;

    const notices: string[] = [];
    renderWithClient(
      <div>
        <WorkspaceConfigPanel />
        <ProviderQuotaTelemetryPanel onNotice={(n) => notices.push(n.message)} />
      </div>
    );

    fireEvent.click(screen.getByRole("button", { name: /Inspect \/ Switch Workspace/i }));
    await waitFor(() => {
      expect(screen.getByTestId("active-workspace-badge").textContent).toContain("/data/projects/ntm");
    });

    fireEvent.change(screen.getByLabelText("Active Project Directory"), {
      target: { value: "/data/projects/other-repo" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Switch Workspace$/i }));
    await waitFor(() => {
      expect(screen.getByTestId("workspace-config-notice").textContent).toContain(
        "/data/projects/other-repo"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: /Load Provider Quota & Cooldowns/i }));
    await waitFor(() => {
      expect(screen.getByTestId("provider-quota-card-claude").textContent).toContain("max-seat-1");
    });

    fireEvent.click(screen.getByRole("button", { name: /Rotate claude Seat/i }));
    await waitFor(() => {
      expect(notices.some((m) => m.includes("max-seat-2"))).toBe(true);
    });
    expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/accounts/claude/rotate")).toBe(
      true
    );
  });

  it("wires SwarmOrchestrationWorkbench (/api/v1/assign, /api/v1/prompt/preflight, /api/v1/route)", async () => {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(typeof input === "string" ? input : input.toString(), "http://127.0.0.1:7337");
      if (url.pathname === "/api/v1/assign") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            session: "wave12-swarm",
            strategy: "balanced",
            relaxed: false,
            recommendations: [
              {
                pane_id: "%11",
                pane_target: "0.1",
                agent_type: "claude",
                assign_bead: "bd-n5y0l.2",
                bead_title: "Expose Orchestration REST routes",
                priority: "P1",
                confidence: 0.92,
                reasoning: "Strong Go backend + OpenAPI affinity",
              },
            ],
            summary: {
              total_agents: 3,
              idle_agents: 2,
              working_agents: 1,
              ready_beads: 1,
              blocked_beads: 0,
              recommendations: 1,
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/prompt/preflight") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            preview_hash: "a1b2c3d4e5f60718",
            preview_len: 42,
            estimated_tokens: 12,
            findings: [],
            error_count: 0,
            warning_count: 0,
            info_count: 0,
            dcg_available: true,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/route") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            session: "wave12-swarm",
            strategy: "least-loaded",
            recommendation: {
              pane_id: "%12",
              pane_index: 2,
              agent_type: "antigravity",
              state: "WAITING",
              score: 91.5,
            },
            candidates: [],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      throw new Error(`Unexpected path ${url.pathname}`);
    }) as unknown as typeof fetch;

    renderWithClient(<SwarmOrchestrationWorkbench defaultSession="wave12-swarm" />);

    fireEvent.click(screen.getByRole("button", { name: /Recommend Assignments/i }));
    await waitFor(() => {
      expect(screen.getByTestId("assign-plan-results").textContent).toContain("bd-n5y0l.2");
    });

    fireEvent.change(screen.getByLabelText("Candidate Prompt"), {
      target: { value: "Implement typed OpenAPI reflection for assign and route." },
    });
    fireEvent.click(screen.getByRole("button", { name: /Run Prompt Preflight/i }));
    await waitFor(() => {
      expect(screen.getByTestId("prompt-preflight-result").textContent).toContain("PASSED");
    });

    fireEvent.click(screen.getByRole("button", { name: /^Route$/i }));
    await waitFor(() => {
      expect(screen.getByTestId("prompt-route-result").textContent).toContain("antigravity");
    });
  });

  it("wires BeadsGraphAndDepsPanel and SessionLifecycleControls across priority, deps, health, controller, rename, interrupt, and kill", async () => {
    const seenRequests: string[] = [];

    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === "string" ? input : input.toString(), "http://127.0.0.1:7337");
      const method = (init?.method || "GET").toUpperCase();
      seenRequests.push(`${method} ${url.pathname}`);

      if (url.pathname === "/api/v1/beads/priority") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            priority: {
              recommendations: [
                {
                  issue_id: "bd-n5y0l.1",
                  title: "Fix kernel MustRegister bindings",
                  current_priority: 2,
                  suggested_priority: 1,
                },
              ],
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/beads/bd-n5y0l.2/deps" && method === "GET") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            bead_id: "bd-n5y0l.2",
            dependencies: [{ id: "bd-n5y0l.1", title: "Kernel fixes" }],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/beads/bd-n5y0l.2/deps" && method === "POST") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            bead_id: "bd-n5y0l.2",
            blocked_by: "bd-suaow",
            linked: true,
          }),
          { status: 201, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/beads/bd-n5y0l.2/deps/bd-n5y0l.1" && method === "DELETE") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            unlinked: true,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/sessions/wave12-sess/health") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            overall_status: "healthy",
            summary: { total: 2, healthy: 2, warning: 0, error: 0 },
            agents: [{ pane: 1, agent_type: "claude", status: "ok" }],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/beads/recipes" && method === "GET") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            recipes: {
              recipes: [
                {
                  name: "actionable",
                  description: "Ready-to-work open issues",
                  source: "builtin",
                },
              ],
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/sessions/wave12-sess/controller" && method === "POST") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            session: "wave12-sess",
            pane_id: "%21",
            pane_index: 1,
            agent_type: "cc",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/sessions/wave12-sess/interrupt" && method === "POST") {
        return new Response(
          JSON.stringify({ success: true, timestamp: "2026-11-16T05:00:00Z" }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/sessions/wave12-sess/panes/1/title" && method === "PATCH") {
        return new Response(
          JSON.stringify({
            success: true,
            timestamp: "2026-11-16T05:00:00Z",
            pane: "%21",
            title: "coordinator-lead",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      if (url.pathname === "/api/v1/sessions/wave12-sess" && method === "DELETE") {
        return new Response(
          JSON.stringify({ success: true, timestamp: "2026-11-16T05:00:00Z" }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      throw new Error(`Unexpected ${method} ${url.pathname}`);
    }) as unknown as typeof fetch;

    renderWithClient(
      <div>
        <BeadsGraphAndDepsPanel />
        <SessionLifecycleControls sessionId="wave12-sess" />
      </div>
    );

    fireEvent.click(screen.getByRole("button", { name: /Audit Graph Priorities/i }));
    await waitFor(() => {
      expect(screen.getByText("Fix kernel MustRegister bindings")).toBeTruthy();
    });

    fireEvent.click(screen.getByRole("button", { name: /Load Recipes/i }));
    await waitFor(() => {
      expect(screen.getByTestId("beads-recipes-list").textContent).toContain("actionable");
    });
    expect(seenRequests).toContain("GET /api/v1/beads/recipes");

    fireEvent.change(screen.getByLabelText("Target Bead ID"), {
      target: { value: "bd-n5y0l.2" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Inspect Deps/i }));
    await waitFor(() => {
      expect(screen.getByTestId("bead-deps-list").textContent).toContain("bd-n5y0l.1");
    });

    fireEvent.change(screen.getByLabelText("Blocked By Bead ID"), {
      target: { value: "bd-suaow" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Add Blocker/i }));
    await waitFor(() => {
      expect(seenRequests).toContain("POST /api/v1/beads/bd-n5y0l.2/deps");
    });

    fireEvent.click(screen.getByRole("button", { name: /Unlink/i }));
    await waitFor(() => {
      expect(seenRequests).toContain("DELETE /api/v1/beads/bd-n5y0l.2/deps/bd-n5y0l.1");
    });

    fireEvent.click(screen.getByRole("button", { name: /Check Session Health/i }));
    await waitFor(() => {
      expect(screen.getByTestId("session-health-report").textContent).toContain("pane 1 (claude)");
    });

    fireEvent.click(screen.getByRole("button", { name: /Launch Controller/i }));
    fireEvent.click(screen.getByRole("button", { name: /Interrupt All Agents/i }));

    fireEvent.change(screen.getByLabelText("New Pane Title"), {
      target: { value: "coordinator-lead" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Set Pane Title/i }));

    fireEvent.click(screen.getByRole("button", { name: /Kill Session…/i }));
    fireEvent.click(screen.getByRole("button", { name: /Confirm Kill Session/i }));

    await waitFor(() => {
      expect(seenRequests).toContain("POST /api/v1/sessions/wave12-sess/controller");
      expect(seenRequests).toContain("POST /api/v1/sessions/wave12-sess/interrupt");
      expect(seenRequests).toContain("PATCH /api/v1/sessions/wave12-sess/panes/1/title");
      expect(seenRequests).toContain("DELETE /api/v1/sessions/wave12-sess");
    });
  });
});
