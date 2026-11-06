import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import PipelinesPage from "../app/(dashboard)/pipelines/page";

const clients: QueryClient[] = [];
const receipt = "c".repeat(64);

function mountPage(initialStatus = "failed") {
  let status = initialStatus;
  let recorded = false;
  const summary = () => ({
    run_id: "run-review", workflow_id: "review-workflow", session: "review-session",
    status, started_at: "2026-11-05T12:00:00Z",
    progress: { completed: 1, pending: 0, failed: 1, skipped: 0, total: 2, percent: 50 },
  });
  const transport = vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
    const path = new URL(String(input)).pathname;
    const reply = (body: object, code = 200) => new Response(JSON.stringify({ success: true, ...body }), {
      status: code, headers: { "Content-Type": "application/json" },
    });
    if (path === "/api/v1/pipelines") return reply({ pipelines: [summary()], count: 1 });
    if (path === "/api/v1/pipelines/templates") return reply({ templates: [], count: 0 });
    if (path === "/api/v1/pipelines/run-review") return reply({
      ...summary(), current_step: "review",
      // The HTTP API returns a map of PipelineStep values, not an array or
      // the raw checkpoint's StepResult/error objects. Keep reverse insertion
      // order to exercise chronological rendering of the actual wire shape.
      steps: {
        review: { id: "review", status: "failed", started_at: "2026-11-05T12:01:00Z", error: "Delivery outcome uncertain" },
        prepare: { id: "prepare", status: "completed", started_at: "2026-11-05T12:00:00Z", duration_ms: 1000 },
      },
    });
    if (path === "/api/v1/pipelines/run-review/recover") {
      if (options?.method === "POST") {
        recorded = true;
        return reply({ run_id: "run-review", replayed: false });
      }
      return reply({
        run_id: "run-review", commands: [], selectors: [],
        deliveries: [{
          step_id: "review", status: recorded ? "delivered" : "sending",
          pane_id: "%4", pane_pid: 123, agent_type: "claude",
          receipt_token: receipt, can_accept: !recorded,
          ...(recorded ? { resolution_action: "accept_delivery" } : {}),
        }],
      });
    }
    if (path === "/api/v1/pipelines/run-review/resume" && options?.method === "POST") {
      status = "running";
      return reply({ ...summary(), status: "pending", resumed: true, background: true }, 202);
    }
    if (path === "/api/v1/pipelines/run-review/cancel" && options?.method === "POST") {
      return reply({ run_id: "run-review", status: "cancellation_requested" }, 202);
    }
    throw new Error(`Unexpected request: ${options?.method ?? "GET"} ${path}`);
  });
  vi.stubGlobal("fetch", transport);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, refetchOnWindowFocus: false } } });
  clients.push(client);
  render(<QueryClientProvider client={client}><PipelinesPage /></QueryClientProvider>);
  return transport;
}

afterEach(() => {
  cleanup();
  for (const client of clients) client.clear();
  clients.length = 0;
  vi.unstubAllGlobals();
});

describe("pipeline dashboard integration", () => {
  it("renders the API's step map and exposes recovery for the selected failed run", async () => {
    const transport = mountPage();
    expect(await screen.findByText("1. prepare")).toBeInTheDocument();
    expect(screen.getByText("2. review")).toBeInTheDocument();
    expect(screen.getByText("Delivery outcome uncertain")).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Pipeline recovery" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Review and record decision" })).toBeEnabled();
    expect(transport.mock.calls.some(([, options]) => options?.method === "POST")).toBe(false);
  });

  it("records the inspected delivery and resumes only after a separate explicit action", async () => {
    const transport = mountPage();
    fireEvent.click(await screen.findByRole("button", { name: "Review and record decision" }));
    const form = screen.getByRole("form", { name: "Record recovery for review" });
    fireEvent.change(within(form).getByLabelText("Reason for the decision"), {
      target: { value: "Verified the original agent received the review." },
    });
    fireEvent.click(within(form).getByRole("checkbox"));
    fireEvent.click(within(form).getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText("Recorded decision: accept_delivery.")).toBeInTheDocument();

    const posts = () => transport.mock.calls.filter(([, options]) => options?.method === "POST");
    expect(posts()).toHaveLength(1);
    expect(JSON.parse(String(posts()[0][1]?.body))).toEqual({
      step_id: "review", receipt, accept_delivery: true,
      reason: "Verified the original agent received the review.",
    });

    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(new URL(String(posts()[1][0])).pathname).toBe("/api/v1/pipelines/run-review/resume");
    expect(JSON.parse(String(posts()[1][1]?.body))).toEqual({ background: true });
    expect(await screen.findByText("Pipeline resume started.")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("region", { name: "Pipeline recovery" })).not.toBeInTheDocument());
  });

  it("keeps running workflows out of recovery and acknowledges cancellation as a request", async () => {
    const transport = mountPage("running");
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(await screen.findByText("Cancellation requested.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Pipeline recovery" })).not.toBeInTheDocument();
    expect(transport.mock.calls.some(([input]) => new URL(String(input)).pathname.endsWith("/recover"))).toBe(false);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
  });
});
