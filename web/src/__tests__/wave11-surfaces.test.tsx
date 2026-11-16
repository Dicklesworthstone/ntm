import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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
  }),
}));

vi.mock("next/link", () => ({
  default: ({
    href,
    children,
    ...props
  }: {
    href: string;
    children: React.ReactNode;
    [key: string]: unknown;
  }) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
}));

const localStorageStore = new Map<string, string>();
const localStorageMock = {
  getItem: (key: string) => localStorageStore.get(key) ?? null,
  setItem: (key: string, value: string) => {
    localStorageStore.set(key, value);
  },
  removeItem: (key: string) => {
    localStorageStore.delete(key);
  },
  clear: () => {
    localStorageStore.clear();
  },
};

Object.defineProperty(globalThis, "localStorage", {
  value: localStorageMock,
  writable: true,
});

import OverviewPage from "../app/(dashboard)/page";
import MailPage from "../app/(dashboard)/mail/page";
import PipelinesPage from "../app/(dashboard)/pipelines/page";
import SafetyPage from "../app/(dashboard)/safety/page";
import ScannerPage from "../app/(dashboard)/scanner/page";
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

describe("Wave 11 Web Cockpit Surfaces", () => {
  it("validates YAML workflows, launches inline workflows, and prunes old pipeline state on PipelinesPage", async () => {
    const validateBodies: Array<Record<string, unknown>> = [];
    const execBodies: Array<Record<string, unknown>> = [];
    const cleanupBodies: Array<Record<string, unknown>> = [];

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

        if (url.pathname === "/api/v1/pipelines" && method === "GET") {
          return reply({ pipelines: [], count: 0 });
        }
        if (url.pathname === "/api/v1/pipelines/templates" && method === "GET") {
          return reply({ templates: [], count: 0 });
        }
        if (url.pathname === "/api/v1/pipelines/validate" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          validateBodies.push(parsed);
          return reply({
            valid: true,
            workflow_id: "quick-review",
            step_count: 1,
            errors: [],
            warnings: [{ message: "Consider explicit step timeout" }],
          });
        }
        if (url.pathname === "/api/v1/pipelines/exec" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          execBodies.push(parsed);
          return reply({
            run_id: "run-inline-42",
            workflow_id: "inline-triage",
            session: "wave11-swarm",
            status: "running",
          });
        }
        if (url.pathname === "/api/v1/pipelines/cleanup" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          cleanupBodies.push(parsed);
          return reply({
            deleted: 3,
            eligible: 3,
            retained: 1,
            older_than_hours: Number(parsed.older_than_hours ?? 24),
          });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    const client = createTestQueryClient();
    render(
      <QueryClientProvider client={client}>
        <PipelinesPage />
      </QueryClientProvider>
    );

    const studio = await screen.findByRole("region", {
      name: "Inline Workflow Studio & State Maintenance",
    });

    // 1. Validate YAML
    fireEvent.click(
      within(studio).getByRole("button", { name: "Validate Workflow YAML" })
    );
    expect(
      await within(studio).findByTestId("pipeline-validate-result")
    ).toHaveTextContent("Valid Workflow · quick-review (1 step)");
    expect(validateBodies).toHaveLength(1);
    expect(String(validateBodies[0].workflow_content)).toContain(
      "name: quick-review"
    );

    // 2. Execute Inline Workflow
    fireEvent.change(within(studio).getByLabelText("Target Session"), {
      target: { value: "wave11-swarm" },
    });
    fireEvent.change(within(studio).getByLabelText("Target Harness"), {
      target: { value: "antigravity" },
    });
    fireEvent.change(within(studio).getByLabelText("Prompt Instruction"), {
      target: { value: "Audit concurrency invariants across internal/serve" },
    });
    fireEvent.click(
      within(studio).getByRole("button", { name: "Execute Inline Workflow" })
    );
    expect(
      await within(studio).findByTestId("pipeline-inline-exec-result")
    ).toHaveTextContent("run-inline-42");
    expect(execBodies).toEqual([
      {
        workflow: {
          schema_version: "2.0",
          name: "inline-triage",
          steps: [
            {
              id: "step-1",
              agent: "antigravity",
              prompt: "Audit concurrency invariants across internal/serve",
            },
          ],
        },
        session: "wave11-swarm",
        background: true,
      },
    ]);

    // 3. Prune Old Pipeline State
    fireEvent.change(within(studio).getByLabelText("Older than (h):"), {
      target: { value: "48" },
    });
    fireEvent.click(
      within(studio).getByRole("button", { name: "Prune Old Pipeline State" })
    );
    fireEvent.click(
      within(studio).getByRole("button", { name: "Confirm Prune" })
    );
    expect(
      await within(studio).findByTestId("pipeline-cleanup-summary")
    ).toHaveTextContent("deleted 3");
    expect(cleanupBodies).toEqual([{ older_than_hours: 48 }]);
  });

  it("queries HTTP audit logs, verifies session hash chains, updates automation policy, and loads policy decisions & approval history on SafetyPage", async () => {
    const automationPuts: Array<Record<string, unknown>> = [];
    const verifyBodies: Array<Record<string, unknown>> = [];
    const auditUrls: string[] = [];

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

        if (url.pathname === "/api/v1/safety/status") {
          return reply({
            installed: true,
            policy_path: ".ntm/policy.yaml",
            blocked_rules: 12,
            approval_rules: 4,
            allowed_rules: 3,
          });
        }
        if (url.pathname === "/api/v1/safety/blocked") {
          return reply({ entries: [], count: 0 });
        }
        if (url.pathname === "/api/v1/policy") {
          return reply({
            version: 1,
            is_default: false,
            stats: { blocked: 12, approval: 4, allowed: 3, slb_rules: 1 },
            automation: {
              auto_commit: true,
              auto_push: false,
              force_release: "approval",
            },
          });
        }
        if (url.pathname === "/api/v1/approvals") {
          return reply({ approvals: [], count: 0 });
        }
        if (
          url.pathname === "/api/v1/policy/automation" &&
          method === "PUT"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          automationPuts.push(parsed);
          return reply({
            auto_commit: Boolean(parsed.auto_commit),
            auto_push: Boolean(parsed.auto_push),
            force_release: String(parsed.force_release ?? "approval"),
            modified: true,
          });
        }
        if (url.pathname === "/api/v1/audit/verify" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          verifyBodies.push(parsed);
          return reply({
            session: String(parsed.session ?? ""),
            verified: true,
            files: [
              {
                file: "wave11-prod-20261115.jsonl",
                status: "PASS",
                verified: true,
              },
            ],
          });
        }
        if (url.pathname === "/api/v1/audit" && method === "GET") {
          auditUrls.push(`${url.pathname}${url.search}`);
          return reply({
            records: [
              {
                id: 99,
                timestamp: "2026-11-15T22:10:00Z",
                request_id: "req-w11",
                user_id: "operator-1",
                role: "admin",
                action: "approve",
                resource: "approvals",
                method: "POST",
                path: "/api/v1/approvals/appr-7/approve",
                status_code: 200,
                duration_ms: 4,
                session_id: "wave11-prod",
              },
            ],
            count: 1,
            total_matches: 1,
            has_more: false,
          });
        }
        if (url.pathname === "/api/v1/policy/decisions" && method === "GET") {
          return reply({
            decisions: [
              {
                id: "dec-1",
                action: "force_release",
                entity_id: "res-42",
                outcome: "approval_required",
                reason: "SLB two-person rule",
              },
            ],
            count: 1,
          });
        }
        if (url.pathname === "/api/v1/approvals/history" && method === "GET") {
          return reply({
            approvals: [
              {
                id: "appr-7",
                action: "force_release",
                resource: "internal/serve/**",
                requestor: "BlueLake",
                reason: "Stale lock",
                slb_required: true,
                status: "approved",
                created_at: "2026-11-15T22:00:00Z",
                approved_by: "GreenCastle",
              },
            ],
            count: 1,
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

    const panel = await screen.findByRole("region", {
      name: "Audit Trail, Automation Guardrails & Decision Ledger",
    });

    // 1. Update Automation Guardrails
    fireEvent.change(
      within(panel).getByLabelText("Reservation Force-Release Gate"),
      { target: { value: "never" } }
    );
    fireEvent.click(
      within(panel).getByRole("button", { name: "Save Automation Policy" })
    );
    expect(
      await within(panel).findByTestId("automation-policy-saved")
    ).toHaveTextContent("force_release=never");
    expect(automationPuts).toEqual([
      { auto_commit: true, auto_push: false, force_release: "never" },
    ]);

    // 2. Verify Session Audit Chain
    fireEvent.change(within(panel).getByLabelText("Session to verify"), {
      target: { value: "wave11-prod" },
    });
    fireEvent.click(
      within(panel).getByRole("button", { name: "Verify Audit Chain" })
    );
    expect(
      await within(panel).findByTestId("audit-chain-verify-result")
    ).toHaveTextContent("wave11-prod-20261115.jsonl");
    expect(verifyBodies).toEqual([{ session: "wave11-prod" }]);

    // 3. Query HTTP/WS Audit Log
    fireEvent.change(within(panel).getByLabelText("Session"), {
      target: { value: "wave11-prod" },
    });
    fireEvent.click(
      within(panel).getByRole("button", { name: "Load HTTP Audit Log" })
    );
    await waitFor(() => {
      expect(
        within(panel).getByTestId("http-audit-log-table")
      ).toHaveTextContent("/api/v1/approvals/appr-7/approve");
    });
    expect(auditUrls[0]).toContain("session_id=wave11-prod");

    // 4. Load Policy Decisions & Approval History
    fireEvent.click(
      within(panel).getByRole("button", { name: "Load Policy Decisions" })
    );
    await waitFor(() => {
      expect(
        within(panel).getByTestId("policy-decisions-list")
      ).toHaveTextContent("SLB two-person rule");
    });

    fireEvent.click(
      within(panel).getByRole("button", { name: "Load Approval History" })
    );
    await waitFor(() => {
      expect(
        within(panel).getByTestId("approval-history-list")
      ).toHaveTextContent("approved by GreenCastle");
    });
  });

  it("dispatches scoped UBS scans & bug notifications on ScannerPage, manages Mail contacts, and loads Timelines & Support Bundles", async () => {
    const scanBodies: Array<Record<string, unknown>> = [];
    const notifyBodies: Array<Record<string, unknown>> = [];
    const contactPolicyPuts: Array<Record<string, unknown>> = [];
    const contactReqs: Array<Record<string, unknown>> = [];
    const bundlePosts: Array<Record<string, unknown>> = [];

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

        // ScannerPage routes
        if (url.pathname === "/api/v1/scanner/status") {
          return reply({
            available: true,
            version: "5.5.26",
            total_scans: 2,
            total_findings: 1,
          });
        }
        if (url.pathname === "/api/v1/bugs/summary") {
          return reply({
            total_findings: 1,
            critical: 1,
            warning: 0,
            info: 0,
            by_severity: { critical: 1 },
            by_category: { concurrency: 1 },
            by_file: { "internal/serve/server.go": 1 },
            dismissed_count: 0,
            linked_beads: 0,
          });
        }
        if (url.pathname === "/api/v1/scanner/findings") {
          return reply({ findings: [], count: 0 });
        }
        if (url.pathname === "/api/v1/scanner/history") {
          return reply({ scans: [] });
        }
        if (url.pathname === "/api/v1/scanner/run" && method === "POST") {
          scanBodies.push(
            JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>
          );
          return reply({ scan_id: "scan-w11" });
        }
        if (url.pathname === "/api/v1/bugs/notify" && method === "POST") {
          notifyBodies.push(
            JSON.parse(String(options?.body ?? "{}")) as Record<string, unknown>
          );
          return reply({
            notified: true,
            channel: "webhook",
            findings_count: 1,
          });
        }

        // MailPage routes
        if (url.pathname === "/api/v1/mail/agents") {
          return reply({
            agents: [{ id: 1, name: "BlueLake", program: "claude-code" }],
            count: 1,
          });
        }
        if (url.pathname === "/api/v1/mail/inbox") {
          return reply({ messages: [], count: 0 });
        }
        if (url.pathname === "/api/v1/reservations") {
          return reply({ reservations: [], count: 0 });
        }
        if (url.pathname === "/api/v1/reservations/conflicts") {
          return reply({ conflicts: [], count: 0 });
        }
        if (url.pathname === "/api/v1/mail/health") {
          return reply({ status: "healthy", server_url: "http://127.0.0.1:8765" });
        }
        if (url.pathname === "/api/v1/mail/projects") {
          return reply({
            projects: [{ id: 1, slug: "data-projects-ntm" }],
            count: 1,
          });
        }
        if (url.pathname === "/api/v1/mail/contacts") {
          return reply({
            contacts: [{ to_agent: "GreenCastle", status: "approved" }],
            count: 1,
          });
        }
        if (url.pathname === "/api/v1/mail/contacts/policy" && method === "PUT") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          contactPolicyPuts.push(parsed);
          return reply({
            agent_name: String(parsed.agent_name ?? ""),
            policy: String(parsed.policy ?? ""),
          });
        }
        if (
          url.pathname === "/api/v1/mail/contacts/request" &&
          method === "POST"
        ) {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          contactReqs.push(parsed);
          return reply({
            from_agent: String(parsed.from_agent ?? ""),
            to_agent: String(parsed.to_agent ?? ""),
            status: "pending",
          });
        }

        // SessionsPage timeline + OverviewPage deps/bundle routes
        if (url.pathname === "/api/v1/sessions") {
          return reply({
            sessions: [{ id: "s1", name: "wave11-sess", status: "active" }],
          });
        }
        if (url.pathname === "/api/v1/sessions/wave11-sess") {
          return reply({ session: { name: "wave11-sess", status: "active" } });
        }
        if (url.pathname === "/api/v1/sessions/wave11-sess/panes") {
          return reply({ session_id: "wave11-sess", count: 0, panes: [] });
        }
        if (url.pathname === "/api/v1/sessions/wave11-sess/checkpoints") {
          return reply({ session_name: "wave11-sess", checkpoints: [], count: 0 });
        }
        if (url.pathname === "/api/v1/handoffs") {
          return reply({
            action: "list",
            session: "wave11-sess",
            handoffs: [],
            total_returned: 0,
          });
        }
        if (url.pathname === "/api/v1/timelines/wave11-sess") {
          return reply({
            action: "show",
            session: "wave11-sess",
            info: {
              session_id: "wave11-sess",
              event_count: 3,
              agent_count: 2,
              size_bytes: 512,
              compressed: false,
            },
            session_stats: {
              total_events: 3,
              unique_agents: 2,
              agent_breakdown: { cc_1: 2, agy_1: 1 },
              state_breakdown: { working: 2, waiting: 1 },
            },
            events: [
              {
                agent_id: "cc_1",
                state: "working",
                timestamp: "2026-11-15T22:00:00Z",
              },
            ],
          });
        }
        if (url.pathname === "/api/v1/health/backpressure") {
          return reply({ backpressure: { decision: "ok", surfaces: [] } });
        }
        if (url.pathname === "/api/v1/attention/digest") {
          return reply({ event_count: 0 });
        }
        if (url.pathname === "/api/v1/worktrees") {
          return reply({ worktrees: [], count: 0 });
        }
        if (url.pathname === "/api/v1/jobs/") {
          return reply({ jobs: [], count: 0 });
        }
        if (url.pathname === "/api/v1/deps") {
          return reply({
            all_installed: true,
            dependencies: [
              { name: "tmux", installed: true, version: "3.4" },
              { name: "br", installed: true, version: "0.4.2" },
            ],
          });
        }
        if (url.pathname === "/api/v1/support-bundle" && method === "POST") {
          const parsed = JSON.parse(String(options?.body ?? "{}")) as Record<
            string,
            unknown
          >;
          bundlePosts.push(parsed);
          return reply({
            filename: "ntm-support-bundle-20261115.zip",
            format: "zip",
            file_count: 9,
            size: 4096,
            content_type: "application/zip",
            data: btoa("bundle-bytes"),
            privacy_mode: false,
            content_suppressed: false,
            redaction_summary: {
              mode: "redact",
              total_findings: 2,
              files_scanned: 9,
              files_redacted: 1,
            },
          });
        }

        throw new Error(`Unexpected request: ${method} ${url.pathname}`);
      })
    );

    // A. ScannerPage scoped scan + bug notification
    const scannerClient = createTestQueryClient();
    const { unmount: unmountScanner } = render(
      <QueryClientProvider client={scannerClient}>
        <ScannerPage />
      </QueryClientProvider>
    );
    expect(await screen.findByText("UBS 5.5.26")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Scan Path"), {
      target: { value: "internal/serve" },
    });
    fireEvent.click(screen.getByLabelText("Diff only"));
    fireEvent.click(screen.getByRole("button", { name: "Scan Now" }));
    await waitFor(() => {
      expect(scanBodies).toEqual([{ path: "internal/serve", diff_only: true }]);
    });

    fireEvent.change(screen.getByLabelText("Notification Endpoint"), {
      target: { value: "https://hooks.example.com/ubs" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Notify Bugs" }));
    expect(
      await screen.findByTestId("scanner-notify-confirmation")
    ).toHaveTextContent("Sent bug notification via webhook (1 findings)");
    expect(notifyBodies).toEqual([
      {
        channel: "webhook",
        endpoint: "https://hooks.example.com/ubs",
        min_severity: "critical",
      },
    ]);
    unmountScanner();

    // B. MailPage contacts policy, handshake request, and server health/projects
    const mailClient = createTestQueryClient();
    const { unmount: unmountMail } = render(
      <QueryClientProvider client={mailClient}>
        <MailPage />
      </QueryClientProvider>
    );
    const contactsPanel = await screen.findByRole("region", {
      name: "Agent Contacts & Handshake Policy",
    });
    await waitFor(() => {
      expect(
        within(contactsPanel).getByRole("button", { name: "Load Contacts" })
      ).toBeEnabled();
    });
    fireEvent.click(
      within(contactsPanel).getByRole("button", {
        name: "Check Mail Health & Projects",
      })
    );
    expect(
      await within(contactsPanel).findByTestId("mail-health-projects-banner")
    ).toHaveTextContent("data-projects-ntm");

    fireEvent.click(
      within(contactsPanel).getByRole("button", { name: "Load Contacts" })
    );
    await waitFor(() => {
      expect(
        within(contactsPanel).getByTestId("mail-contacts-list")
      ).toHaveTextContent("GreenCastle");
    });

    fireEvent.change(
      within(contactsPanel).getByLabelText(/Inbound Policy/),
      { target: { value: "contacts_only" } }
    );
    fireEvent.click(
      within(contactsPanel).getByRole("button", { name: "Save Contact Policy" })
    );
    expect(
      await within(contactsPanel).findByTestId("mail-contact-policy-confirmation")
    ).toHaveTextContent('Updated contact policy for BlueLake to "contacts_only"');
    expect(contactPolicyPuts).toEqual([
      { agent_name: "BlueLake", policy: "contacts_only" },
    ]);

    fireEvent.change(within(contactsPanel).getByLabelText("Target Agent"), {
      target: { value: "PurpleBear" },
    });
    fireEvent.click(
      within(contactsPanel).getByRole("button", {
        name: "Send Contact Request",
      })
    );
    expect(
      await within(contactsPanel).findByTestId(
        "mail-contact-request-confirmation"
      )
    ).toHaveTextContent("BlueLake → PurpleBear: pending");
    expect(contactReqs).toEqual([
      { from_agent: "BlueLake", to_agent: "PurpleBear" },
    ]);
    unmountMail();

    // C. SessionsPage timeline inspector
    navigationState.searchParams = new URLSearchParams({
      session: "wave11-sess",
    });
    const sessionsClient = createTestQueryClient();
    const { unmount: unmountSessions } = render(
      <QueryClientProvider client={sessionsClient}>
        <SessionsPage />
      </QueryClientProvider>
    );
    const timelineRegion = await screen.findByRole("region", {
      name: "Session State Timeline",
    });
    fireEvent.click(
      within(timelineRegion).getByRole("button", {
        name: "Load Session Timeline",
      })
    );
    await waitFor(() => {
      expect(
        within(timelineRegion).getByTestId("session-timeline-detail")
      ).toHaveTextContent("working=2");
    });
    unmountSessions();

    // D. OverviewPage tool dependencies + support bundle archive
    navigationState.searchParams = new URLSearchParams();
    const overviewClient = createTestQueryClient();
    render(
      <QueryClientProvider client={overviewClient}>
        <OverviewPage />
      </QueryClientProvider>
    );
    const diagToolbar = await screen.findByRole("region", {
      name: "Dependencies & Support Bundle",
    });
    fireEvent.click(
      within(diagToolbar).getByRole("button", {
        name: "Check Tool Dependencies",
      })
    );
    await waitFor(() => {
      expect(
        within(diagToolbar).getByTestId("overview-deps-matrix")
      ).toHaveTextContent("Tool Dependencies: 2/2 installed");
    });

    fireEvent.click(
      within(diagToolbar).getByRole("button", {
        name: "Generate Support Bundle",
      })
    );
    expect(
      await within(diagToolbar).findByTestId("support-bundle-ready")
    ).toHaveTextContent("ntm-support-bundle-20261115.zip");
    expect(bundlePosts).toEqual([
      { format: "zip", redact_mode: "redact", lines: 500 },
    ]);
  });
});
