import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SwarmScaleRebalanceReviewPanel } from "../components/swarm/SwarmScaleRebalanceReviewPanel";
import { RequestLifecycleAndOutboxPanel } from "../components/overview/RequestLifecycleAndOutboxPanel";
import { SystemDiagnosticsToolbar } from "../components/overview/SystemDiagnosticsToolbar";
import { MetricsExportAndStreamingPanel } from "../components/analytics/MetricsExportAndStreamingPanel";
import { SessionSummaryAndEventsPanel } from "../components/sessions/SessionSummaryAndEventsPanel";
import { AuditAndPolicyPanel } from "../components/safety/AuditAndPolicyPanel";
import { MemoryWorkbenchPanel } from "../components/memory/MemoryWorkbenchPanel";
import { MailContactsPanel } from "../components/mail/MailContactsPanel";
import { ScannerTargetedAndBugsPanel } from "../components/scanner/ScannerTargetedAndBugsPanel";

function renderWithClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("Wave 13 Web Cockpit Surfaces", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
  });

  it("wires SwarmScaleRebalanceReviewPanel (scale 11-harness, rebalance, review-queue, coordinator status/digest/conflicts)", async () => {
    const calls: Array<{ method: string; path: string; body?: unknown }> = [];

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        const parsedBody = init?.body ? JSON.parse(String(init.body)) : undefined;
        calls.push({ method, path: url.pathname, body: parsedBody });

        if (url.pathname === "/api/v1/sessions/demo-swarm/scale") {
          return new Response(
            JSON.stringify({
              success: true,
              timestamp: "2026-11-16T09:30:00Z",
              session: "demo-swarm",
              dry_run: true,
              before: { cc: 1, cursor: 0 },
              after: { cc: 3, cursor: 1 },
              actions: [
                { type: "spawn", agent_type: "cc", count: 2, agents: [] },
                { type: "spawn", agent_type: "cursor", count: 1, agents: [] },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/demo-swarm/rebalance") {
          return new Response(
            JSON.stringify({
              success: true,
              timestamp: "2026-11-16T09:30:01Z",
              session: "demo-swarm",
              imbalance_score: 0.82,
              recommendation: "rebalance_recommended",
              workloads: [],
              before: { "%1": 3, "%2": 0 },
              after: { "%1": 2, "%2": 1 },
              transfers: [
                {
                  bead_id: "bd-y5y8h.1",
                  bead_title: "Wire ORI scale",
                  from_pane: 1,
                  from_target: "demo-swarm:0.1",
                  from_pane_id: "%1",
                  from_agent: "claude",
                  to_pane: 2,
                  to_target: "demo-swarm:0.2",
                  to_pane_id: "%2",
                  to_agent: "codex",
                  reason: "target_idle",
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/demo-swarm/review-queue") {
          return new Response(
            JSON.stringify({
              success: true,
              timestamp: "2026-11-16T09:30:02Z",
              session: "demo-swarm",
              idle_agents: [{ pane: 2, agent_type: "codex" }],
              suggestions: [
                {
                  agent: "codex_2",
                  pane: 2,
                  agent_type: "codex",
                  prompt: 'Review the changes for bead bd-y5y8h.1: "Wire ORI scale"',
                  source: "agent_work",
                  source_ref: "bd-y5y8h.1",
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        if (url.pathname === "/api/v1/sessions/demo-swarm/coordinator/digest") {
          return new Response(
            JSON.stringify({
              success: true,
              timestamp: "2026-11-16T09:30:03Z",
              session: "demo-swarm",
              action: "digest",
              digest: {
                session: "demo-swarm",
                agent_count: 4,
                active_count: 3,
                idle_count: 1,
                error_count: 0,
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        return new Response(JSON.stringify({ success: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
    );

    renderWithClient(<SwarmScaleRebalanceReviewPanel />);

    fireEvent.change(screen.getByLabelText("Target Swarm Session"), {
      target: { value: "demo-swarm" },
    });
    fireEvent.change(screen.getByLabelText("Scale target cc"), {
      target: { value: "3" },
    });
    fireEvent.change(screen.getByLabelText("Scale target cursor"), {
      target: { value: "1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview Scale Plan" }));

    await waitFor(() => {
      expect(screen.getByTestId("scale-plan-result")).toHaveTextContent(
        "+ spawn 2 cc"
      );
      expect(screen.getByTestId("scale-plan-result")).toHaveTextContent(
        "+ spawn 1 cursor"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Analyze Imbalance" }));
    await waitFor(() => {
      expect(screen.getByTestId("rebalance-plan-result")).toHaveTextContent(
        "0.82 (rebalance_recommended)"
      );
      expect(screen.getByTestId("rebalance-plan-result")).toHaveTextContent(
        "[bd-y5y8h.1]"
      );
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Preview Review Queue" })
    );
    await waitFor(() => {
      expect(screen.getByTestId("review-queue-result")).toHaveTextContent(
        "codex_2"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Generate Digest" }));
    await waitFor(() => {
      expect(screen.getByTestId("coordinator-ops-result")).toHaveTextContent(
        "Agents: 4"
      );
    });

    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      "POST /api/v1/sessions/demo-swarm/scale",
      "POST /api/v1/sessions/demo-swarm/rebalance",
      "POST /api/v1/sessions/demo-swarm/review-queue",
      "POST /api/v1/sessions/demo-swarm/coordinator/digest",
    ]);
  });

  it("wires RequestLifecycleAndOutboxPanel (/api/v1/requests/{id}, /api/v1/webhooks/outbox) and SystemDiagnosticsToolbar (/api/v1/version, /api/v1/capabilities)", async () => {
    const calls: string[] = [];

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        calls.push(`${method} ${url.pathname}`);

        if (url.pathname === "/api/v1/requests/op-42") {
          return new Response(
            JSON.stringify({
              success: true,
              request_id: "op-42",
              kind: "send",
              status: "completed",
              session: "alpha",
              completed: true,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/webhooks/outbox") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "list",
              snapshot: {
                entries: [
                  {
                    record: "rec-001",
                    revision: "rev-7",
                    status: "failed",
                    event_type: "agent.error",
                    last_error: "502 Bad Gateway",
                  },
                ],
                count: 1,
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/webhooks/outbox/rec-001/retry") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "retry",
              authorized: true,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/version") {
          return new Response(
            JSON.stringify({
              success: true,
              version: "1.39.0",
              go_version: "go1.26.11",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/capabilities") {
          return new Response(
            JSON.stringify({
              success: true,
              count: 165,
              commands: [],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        return new Response(JSON.stringify({ success: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
    );

    renderWithClient(
      <>
        <RequestLifecycleAndOutboxPanel />
        <SystemDiagnosticsToolbar />
      </>
    );

    fireEvent.change(screen.getByLabelText("Durable request ID"), {
      target: { value: "op-42" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Inspect Request" }));
    await waitFor(() => {
      expect(screen.getByTestId("durable-request-result")).toHaveTextContent(
        "op-42"
      );
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Inspect Webhook Outbox" })
    );
    await waitFor(() => {
      expect(screen.getByTestId("webhook-outbox-snapshot")).toHaveTextContent(
        "rec-001"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => {
      expect(calls).toContain("POST /api/v1/webhooks/outbox/rec-001/retry");
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Runtime & Capabilities" })
    );
    await waitFor(() => {
      expect(
        screen.getByTestId("overview-runtime-capabilities")
      ).toHaveTextContent("1.39.0");
      expect(
        screen.getByTestId("overview-runtime-capabilities")
      ).toHaveTextContent("165");
    });
  });

  it("wires MetricsExportAndStreamingPanel, SessionSummaryAndEventsPanel, AuditAndPolicyPanel (/api/v1/audit/sessions), MemoryWorkbenchPanel (/api/v1/cass/*), and MailContactsPanel", async () => {
    const calls: string[] = [];

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        calls.push(`${method} ${url.pathname}`);

        if (url.pathname === "/api/v1/metrics/export") {
          return new Response("ntm_session_agents 4\n", {
            status: 200,
            headers: { "Content-Type": "text/plain" },
          });
        }
        if (url.pathname === "/api/v1/streaming/stats") {
          return new Response(
            JSON.stringify({
              success: true,
              active_streams: 2,
              dropped_chunks: 0,
              active_targets: ["alpha:0.1", "alpha:0.2"],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/sessions/alpha/summary") {
          return new Response(
            JSON.stringify({
              success: true,
              session: "alpha",
              summary: {
                agents: [{ pane_id: "%1", agent_type: "claude" }],
                text: "Claude modified internal/serve/server_swarm_ops.go",
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/audit/sessions") {
          return new Response(
            JSON.stringify({
              success: true,
              action: "search",
              count: 1,
              entries: [
                {
                  sequence_num: 9,
                  event_type: "spawn",
                  actor: "operator",
                  target: "alpha",
                  timestamp: "2026-11-16T09:00:00Z",
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/cass/preview") {
          return new Response(
            JSON.stringify({
              success: true,
              content: '{"step":1,"text":"Previewed transcript"}',
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/mail/threads/bd-y5y8h/summary") {
          return new Response(
            JSON.stringify({
              success: true,
              summary: "All Wave 13 surfaces converged cleanly.",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        return new Response(JSON.stringify({ success: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
    );

    renderWithClient(
      <>
        <MetricsExportAndStreamingPanel selectedSession="alpha" />
        <SessionSummaryAndEventsPanel sessionId="alpha" />
        <AuditAndPolicyPanel />
        <MemoryWorkbenchPanel />
        <MailContactsPanel selectedAgentName="BlueLake" />
      </>
    );

    fireEvent.change(screen.getByLabelText("Metrics export format"), {
      target: { value: "prometheus" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Export Metrics" }));
    await waitFor(() => {
      expect(screen.getByTestId("metrics-export-preview")).toHaveTextContent(
        "ntm_session_agents 4"
      );
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Inspect Stream Stats" })
    );
    await waitFor(() => {
      expect(screen.getByTestId("streaming-stats-card")).toHaveTextContent(
        "alpha:0.1"
      );
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Generate Activity Summary" })
    );
    await waitFor(() => {
      expect(screen.getByTestId("session-activity-summary")).toHaveTextContent(
        "server_swarm_ops.go"
      );
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Query Session Audit" })
    );
    await waitFor(() => {
      expect(
        screen.getByTestId("session-jsonl-audit-results")
      ).toHaveTextContent("#9 spawn");
    });

    fireEvent.change(screen.getByLabelText("CASS preview path"), {
      target: { value: "/tmp/demo.jsonl" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview Transcript" }));
    await waitFor(() => {
      expect(screen.getByTestId("cass-transcript-preview")).toHaveTextContent(
        "Previewed transcript"
      );
    });

    fireEvent.change(screen.getByLabelText("Mail thread ID to summarize"), {
      target: { value: "bd-y5y8h" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Summarize Thread" }));
    await waitFor(() => {
      expect(
        screen.getByTestId("mail-thread-summary-result")
      ).toHaveTextContent("All Wave 13 surfaces converged cleanly.");
    });
  });

  it("wires ScannerTargetedAndBugsPanel (POST /api/v1/scanner/scan, GET /api/v1/bugs, GET /api/v1/scanner/findings/{id})", async () => {
    const calls: string[] = [];

    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(
          typeof input === "string" ? input : input.toString(),
          "http://127.0.0.1:7337"
        );
        const method = (init?.method || "GET").toUpperCase();
        calls.push(`${method} ${url.pathname}`);

        if (url.pathname === "/api/v1/scanner/scan") {
          return new Response(
            JSON.stringify({
              success: true,
              scan_id: "scan-wave13-1",
              state: "running",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/bugs") {
          return new Response(
            JSON.stringify({
              success: true,
              total: 1,
              count: 1,
              bugs: [
                {
                  id: "find-99",
                  scan_id: "scan-wave13-1",
                  finding: {
                    severity: "warning",
                    file: "internal/serve/server.go",
                    line: 142,
                    message: "Unchecked query param",
                    category: "security",
                    rule_id: "ubs-query-01",
                  },
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        if (url.pathname === "/api/v1/scanner/findings/find-99") {
          return new Response(
            JSON.stringify({
              success: true,
              id: "find-99",
              finding: {
                severity: "warning",
                file: "internal/serve/server.go",
                line: 142,
                message: "Unchecked query param",
                category: "security",
                rule_id: "ubs-query-01",
                suggestion: "Use firstQueryParam helper",
              },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        return new Response(JSON.stringify({ success: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
    );

    renderWithClient(<ScannerTargetedAndBugsPanel />);

    fireEvent.change(screen.getByLabelText("Targeted Scan File"), {
      target: { value: "internal/serve/server.go" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Run Targeted Scan" }));
    await waitFor(() => {
      expect(screen.getByTestId("targeted-scan-notice")).toHaveTextContent(
        "scan-wave13-1"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Load Active Bugs" }));
    await waitFor(() => {
      expect(screen.getByTestId("active-bugs-results")).toHaveTextContent(
        "internal/serve/server.go:142"
      );
    });

    fireEvent.click(screen.getByRole("button", { name: "Inspect" }));
    await waitFor(() => {
      expect(screen.getByTestId("finding-inspector-detail")).toHaveTextContent(
        "Use firstQueryParam helper"
      );
    });

    expect(calls).toEqual([
      "POST /api/v1/scanner/scan",
      "GET /api/v1/bugs",
      "GET /api/v1/scanner/findings/find-99",
    ]);
  });
});
