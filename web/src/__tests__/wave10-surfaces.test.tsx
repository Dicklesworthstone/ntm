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

class Wave10MockWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = Wave10MockWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  send = vi.fn();
  close = vi.fn();
}

Object.defineProperty(global, "WebSocket", { value: Wave10MockWebSocket });

import AnalyticsPage from "../app/(dashboard)/analytics/page";
import MemoryPage from "../app/(dashboard)/memory/page";
import SessionsPage from "../app/(dashboard)/sessions/page";
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
  localStorageMock.setItem(
    "ntm-connection",
    JSON.stringify({ baseUrl: "http://localhost:7337" })
  );
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

describe("Wave 10 Web Cockpit Surfaces", () => {
  it("renders multi-harness event analytics on AnalyticsPage, filters by window/since, and previews Prometheus metrics", async () => {
    const analyticsQueries: string[] = [];

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
            count: 1,
            sessions: [
              {
                id: "s-1",
                name: "wave10-swarm",
                project_path: "/data/projects/ntm",
                status: "active",
              },
            ],
          });
        }
        if (url.pathname === "/api/v1/reservations") {
          return reply({ reservations: [] });
        }
        if (url.pathname === "/api/v1/analytics") {
          analyticsQueries.push(`${url.pathname}${url.search}`);
          if (url.searchParams.get("format") === "prometheus") {
            return new Response(
              `# HELP ntm_sessions_total Total sessions created.\nntm_sessions_total{period="Since 2026-11-10"} 2\n`,
              {
                status: 200,
                headers: { "Content-Type": "text/plain; version=0.0.4" },
              }
            );
          }
          const since = url.searchParams.get("since") ?? "";
          const days = Number(url.searchParams.get("days") ?? "30");
          return reply({
            schema_id: "ntm:robot:analytics:v1",
            schema_version: "1.0.0",
            period: since ? `Since ${since}` : `Last ${days} days`,
            since: "2026-10-16T00:00:00Z",
            days: since ? undefined : days,
            total_sessions: 2,
            total_agents: 6,
            total_prompts: 14,
            total_chars_sent: 8400,
            total_tokens_estimated: 2450,
            agent_breakdown: {
              claude: {
                count: 2,
                prompts: 6,
                chars_sent: 3600,
                tokens_estimated: 1050,
              },
              antigravity: {
                count: 2,
                prompts: 5,
                chars_sent: 3000,
                tokens_estimated: 880,
              },
              oc: {
                count: 1,
                prompts: 2,
                chars_sent: 1200,
                tokens_estimated: 350,
              },
              ollama: {
                count: 1,
                prompts: 1,
                chars_sent: 600,
                tokens_estimated: 170,
              },
            },
            sessions: [
              {
                name: "wave10-swarm",
                created_at: "2026-11-15T09:00:00Z",
                agent_count: 6,
                prompt_count: 14,
              },
            ],
            error_count: 1,
            error_types: { rate_limit: 1 },
            agent_crashes: 1,
            agent_restarts: 1,
          });
        }

        throw new Error(`Unexpected request: ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <AnalyticsPage />
      </QueryClientProvider>
    );

    const panel = await screen.findByRole("region", {
      name: "Event Telemetry & Multi-Harness Breakdown",
    });

    await waitFor(() => {
      expect(
        within(panel).getByTestId("analytics-total-prompts")
      ).toHaveTextContent("14");
    });
    expect(within(panel).getByTestId("analytics-total-tokens")).toHaveTextContent(
      "2,450"
    );
    expect(within(panel).getByTestId("analytics-total-agents")).toHaveTextContent(
      "6"
    );
    expect(within(panel).getByTestId("analytics-error-count")).toHaveTextContent(
      "1"
    );
    expect(
      within(panel).getByText("1 crash(es) · 1 restart(s)")
    ).toBeInTheDocument();

    // Verify all multi-harness display rows render accurately
    expect(
      within(panel).getByText("Claude Code (claude)")
    ).toBeInTheDocument();
    expect(
      within(panel).getByText("Antigravity (antigravity)")
    ).toBeInTheDocument();
    expect(within(panel).getByText("OpenCode (oc)")).toBeInTheDocument();
    expect(within(panel).getByText("Ollama (ollama)")).toBeInTheDocument();
    expect(within(panel).getByText("rate_limit")).toBeInTheDocument();

    // Switch window to 7 days, then apply explicit Since cutoff
    fireEvent.change(within(panel).getByLabelText("Window"), {
      target: { value: "7" },
    });
    await waitFor(() => {
      expect(analyticsQueries).toContain(
        "/api/v1/analytics?days=7&sessions=true"
      );
    });

    fireEvent.change(within(panel).getByLabelText("Since (YYYY-MM-DD)"), {
      target: { value: "2026-11-10" },
    });
    fireEvent.click(within(panel).getByRole("button", { name: "Apply" }));
    await waitFor(() => {
      expect(analyticsQueries).toContain(
        "/api/v1/analytics?since=2026-11-10&sessions=true"
      );
    });

    // Export Prometheus exposition preview
    fireEvent.click(
      within(panel).getByRole("button", { name: "Prometheus Metrics" })
    );
    expect(
      await within(panel).findByTestId("analytics-prometheus-preview")
    ).toHaveTextContent('ntm_sessions_total{period="Since 2026-11-10"} 2');
  });

  it("synthesizes task context, records outcome feedback, loads CASS insights, and updates privacy on MemoryPage", async () => {
    let insightsCalls = 0;
    let privacyGetCalls = 0;
    const contextPayloads: Array<Record<string, unknown>> = [];
    const outcomePayloads: Array<Record<string, unknown>> = [];
    const privacyPutPayloads: Array<Record<string, unknown>> = [];

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
          return reply({ installed: true, state: "running", port: 8201 });
        }
        if (url.pathname === "/api/v1/cass/status") {
          return reply({ installed: true, healthy: true, doc_count: 85 });
        }
        if (url.pathname === "/api/v1/memory/rules") {
          return reply({ rules: [], anti_patterns: [] });
        }
        if (url.pathname === "/api/v1/memory/context" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          contextPayloads.push(parsed);
          return reply({
            task: String(parsed.task),
            source: "daemon",
            relevant_rules: [
              {
                id: "b-101",
                content: "Always regenerate OpenAPI and schema.d.ts together.",
                category: "openapi",
              },
            ],
            anti_patterns: [
              {
                id: "ap-9",
                content: "Never hand-edit generated schema.d.ts.",
                category: "drift",
              },
            ],
            history_snippets: [
              {
                source_path: "/sessions/wave9.jsonl",
                line_number: 44,
                agent: "antigravity",
                title: "OpenAPI 3.1 reflection wiring",
                snippet: "Ran bash scripts/gen_web_types.sh after adding route.",
                score: 0.94,
              },
            ],
            suggested_queries: ["openapi_drift_gate"],
          });
        }
        if (url.pathname === "/api/v1/memory/outcome" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          outcomePayloads.push(parsed);
          return reply({
            recorded: true,
            status: String(parsed.status),
          });
        }
        if (url.pathname === "/api/v1/cass/insights" && method === "GET") {
          insightsCalls += 1;
          return reply({
            total_documents: 85,
            agents: [
              { key: "claude", doc_count: 40 },
              { key: "antigravity", doc_count: 25 },
              { key: "codex", doc_count: 20 },
            ],
            workspaces: [{ key: "/data/projects/ntm", doc_count: 85 }],
          });
        }
        if (url.pathname === "/api/v1/memory/privacy" && method === "GET") {
          privacyGetCalls += 1;
          return reply({
            enabled: true,
            agents: ["claude", "codex", "antigravity"],
          });
        }
        if (url.pathname === "/api/v1/memory/privacy" && method === "PUT") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          privacyPutPayloads.push(parsed);
          return reply({
            enabled: parsed.enabled,
            agents: parsed.agents,
            updated: true,
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

    expect(await screen.findByText("running")).toBeInTheDocument();
    // On-demand endpoints never fire unprompted on mount
    expect(insightsCalls).toBe(0);
    expect(privacyGetCalls).toBe(0);

    // Multi-harness agent filter includes antigravity and opencode
    const agentFilter = screen.getByLabelText("Agent Filter") as HTMLSelectElement;
    expect(
      Array.from(agentFilter.options).map((o) => o.value)
    ).toEqual(expect.arrayContaining(["antigravity", "opencode", "ollama"]));

    // 1. Fetch Task Recovery Context
    const ctxRegion = screen.getByRole("region", {
      name: "Task Recovery Context Builder",
    });
    fireEvent.change(within(ctxRegion).getByLabelText("Task Description"), {
      target: { value: "Expose /api/v1/analytics OpenAPI schema" },
    });
    fireEvent.click(
      within(ctxRegion).getByRole("button", { name: "Fetch Recovery Context" })
    );

    expect(
      await within(ctxRegion).findByTestId("memory-context-source")
    ).toHaveTextContent("source: daemon");
    expect(
      within(ctxRegion).getByText(
        /Always regenerate OpenAPI and schema\.d\.ts together\./
      )
    ).toBeInTheDocument();
    expect(contextPayloads[0]).toMatchObject({
      task: "Expose /api/v1/analytics OpenAPI schema",
      max_rules: 10,
      max_snippets: 5,
    });

    // Click suggested query -> populates CASS Search input, and select rule b-101 for outcome
    fireEvent.click(
      within(ctxRegion).getByRole("button", { name: "openapi_drift_gate" })
    );
    expect(screen.getByLabelText("Search Query")).toHaveValue(
      "openapi_drift_gate"
    );
    fireEvent.click(
      within(ctxRegion).getByLabelText("Select rule b-101 for outcome")
    );

    // 2. Record Task Outcome
    const outcomeRegion = screen.getByRole("region", {
      name: "Task Outcome Feedback Recorder",
    });
    expect(
      within(outcomeRegion).getByLabelText("Rule IDs (comma-separated)")
    ).toHaveValue("b-101");
    fireEvent.change(
      within(outcomeRegion).getByLabelText("Outcome Notes (optional)"),
      { target: { value: "Zero drift across openapi.json and schema.d.ts" } }
    );
    fireEvent.click(
      within(outcomeRegion).getByRole("button", { name: "Record Outcome" })
    );

    expect(
      await within(outcomeRegion).findByTestId("memory-outcome-confirmation")
    ).toHaveTextContent("success");
    expect(outcomePayloads[0]).toEqual({
      status: "success",
      rule_ids: ["b-101"],
      notes: "Zero drift across openapi.json and schema.d.ts",
    });

    // 3. Load CASS Corpus Insights
    const insightsRegion = screen.getByRole("region", {
      name: "CASS Corpus Insights",
    });
    fireEvent.click(
      within(insightsRegion).getByRole("button", {
        name: "Load Corpus Insights",
      })
    );
    expect(
      await within(insightsRegion).findByTestId("cass-insights-total-docs")
    ).toHaveTextContent("85");
    expect(within(insightsRegion).getByText("antigravity")).toBeInTheDocument();
    expect(insightsCalls).toBe(1);

    // 4. Load & Save Cross-Agent Privacy Policy
    const privacyRegion = screen.getByRole("region", {
      name: "Cross-Agent Memory Privacy",
    });
    fireEvent.click(
      within(privacyRegion).getByRole("button", { name: "Load Privacy Status" })
    );
    expect(
      await within(privacyRegion).findByTestId("memory-privacy-current")
    ).toBeInTheDocument();
    fireEvent.change(
      within(privacyRegion).getByLabelText(
        "Allowed Agents (comma-separated, optional)"
      ),
      { target: { value: "claude, codex, antigravity, opencode" } }
    );
    fireEvent.click(
      within(privacyRegion).getByRole("button", { name: "Save Privacy Policy" })
    );
    expect(
      await within(privacyRegion).findByTestId("memory-privacy-saved")
    ).toHaveTextContent("enabled=true");
    expect(privacyPutPayloads[0]).toEqual({
      enabled: true,
      agents: ["claude", "codex", "antigravity", "opencode"],
    });
  });

  it("previews & executes Git rollback and exports & imports portable checkpoint archives on SessionsPage", async () => {
    const sessionId = "my/proj#1";
    navigationState.searchParams = new URLSearchParams({ session: sessionId });

    const checkpoints: Array<{
      id: string;
      name: string;
      session_name: string;
      pane_count: number;
      created_at: string;
      git?: { branch: string; commit: string };
    }> = [
      {
        id: "cp-20261115",
        name: "wave10-clean",
        session_name: sessionId,
        pane_count: 3,
        created_at: "2026-11-15T10:00:00Z",
        git: { branch: "main", commit: "025207cd123456" },
      },
    ];
    const rollbackPayloads: Array<Record<string, unknown>> = [];
    const exportPayloads: Array<Record<string, unknown>> = [];
    const importPayloads: Array<Record<string, unknown>> = [];

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = new URL(String(input));
        const method = options?.method ?? "GET";
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
            panes: [{ index: 0, title: "agy-0", type: "antigravity", active: true }],
          });
        }
        if (url.pathname === "/api/v1/sessions/my%2Fproj%231/panes/0/output") {
          return reply({ pane: "0", output: "ready", lines: 1 });
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
          url.pathname === "/api/v1/sessions/my%2Fproj%231/rollback" &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          rollbackPayloads.push(parsed);
          const dryRun = Boolean(parsed.dry_run);
          return reply({
            checkpoint_id: "cp-20261115",
            checkpoint_name: "wave10-clean",
            git_restored: !dryRun,
            dry_run: dryRun,
            stash_created: !dryRun,
            stash_ref: dryRun ? undefined : "stash@{0}",
            warnings: dryRun
              ? ["would rollback to commit 025207cd on branch main"]
              : [],
          });
        }
        if (
          url.pathname ===
            "/api/v1/sessions/my%2Fproj%231/checkpoints/cp-20261115/export" &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          exportPayloads.push(parsed);
          return reply({
            filename: "my_proj_1_cp-20261115.zip",
            size: 2048,
            content_type: "application/zip",
            data: btoa("fake-zip-bytes"),
          });
        }
        if (
          url.pathname ===
            "/api/v1/sessions/my%2Fproj%231/checkpoints/import" &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          importPayloads.push(parsed);
          const imported = {
            id: "cp-imported-77",
            name: "portable-bundle-77",
            session_name: sessionId,
            pane_count: 4,
            created_at: "2026-11-15T10:15:00Z",
          };
          checkpoints.unshift(imported);
          return reply({ imported: true, checkpoint: imported }, 201);
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

    expect(await screen.findByText("wave10-clean")).toBeInTheDocument();

    // 1. Preview dry-run rollback then execute live rollback
    const rollbackRegion = screen.getByRole("region", {
      name: "Git-Aware Session Rollback",
    });
    fireEvent.click(
      within(rollbackRegion).getByRole("button", { name: "Use cp-20261115" })
    );
    fireEvent.click(
      within(rollbackRegion).getByRole("button", {
        name: "Preview Rollback (Dry Run)",
      })
    );
    expect(
      await within(rollbackRegion).findByTestId("session-rollback-result")
    ).toHaveTextContent("would rollback to commit 025207cd on branch main");

    fireEvent.click(
      within(rollbackRegion).getByRole("button", { name: "Execute Rollback" })
    );
    fireEvent.click(
      within(rollbackRegion).getByRole("button", {
        name: "Confirm Git Rollback",
      })
    );
    await waitFor(() => {
      expect(
        within(rollbackRegion).getByTestId("session-rollback-result")
      ).toHaveTextContent("stash=stash@{0}");
    });
    expect(rollbackPayloads).toEqual([
      {
        checkpoint_ref: "cp-20261115",
        no_stash: false,
        no_git: false,
        dry_run: true,
      },
      {
        checkpoint_ref: "cp-20261115",
        no_stash: false,
        no_git: false,
        dry_run: false,
      },
    ]);

    // 2. Export Checkpoint Archive
    const archiveRegion = screen.getByRole("region", {
      name: "Portable Checkpoint Archives",
    });
    fireEvent.change(within(archiveRegion).getByLabelText("Archive Format"), {
      target: { value: "zip" },
    });
    fireEvent.click(
      within(archiveRegion).getByRole("button", { name: "Export Archive" })
    );
    expect(
      await within(archiveRegion).findByTestId("checkpoint-export-ready")
    ).toHaveTextContent("my_proj_1_cp-20261115.zip");
    expect(exportPayloads[0]).toEqual({
      format: "zip",
      redact_secrets: true,
      rewrite_paths: true,
    });

    // 3. Import Checkpoint Archive
    fireEvent.change(
      within(archiveRegion).getByLabelText("Base64 Archive Data"),
      { target: { value: btoa("archive-payload") } }
    );
    fireEvent.click(
      within(archiveRegion).getByRole("button", {
        name: "Import Checkpoint Archive",
      })
    );
    expect(
      await within(archiveRegion).findByTestId("checkpoint-import-confirmation")
    ).toHaveTextContent("portable-bundle-77");
    expect(
      await screen.findByText('Imported checkpoint "portable-bundle-77".')
    ).toBeInTheDocument();
    expect(
      (await screen.findAllByText("portable-bundle-77")).length
    ).toBeGreaterThanOrEqual(2);
    expect(importPayloads[0]).toEqual({
      data: btoa("archive-payload"),
      verify_checksums: true,
      allow_overwrite: false,
    });
  });
});
