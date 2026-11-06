import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import PipelineRecoveryPanel, { type PipelineRecoveryPanelProps } from "../components/pipelines/PipelineRecoveryPanel";
import { ApiError } from "../lib/api/client";

const TOKEN_A = "a".repeat(64);
const TOKEN_B = "b".repeat(64);
const clients: QueryClient[] = [];
type Kind = "delivery" | "command" | "selector";

function inspection(runId = "run-a", kind: Kind = "delivery", overrides: Record<string, unknown> = {}) {
  const receipt = {
    step_id: "work", status: "sending", receipt_token: TOKEN_A, can_accept: true,
    ...(kind === "delivery" ? { pane_id: "%4", pane_pid: 123, agent_type: "claude" } : { process_id: 456 }),
    ...overrides,
  };
  return {
    success: true, run_id: runId,
    deliveries: kind === "delivery" ? [receipt] : [],
    commands: kind === "command" ? [receipt] : [],
    selectors: kind === "selector" ? [receipt] : [],
  };
}

function mountPanel(handler: (path: string, options?: RequestInit) => Promise<unknown>) {
  const transport = vi.fn(handler);
  const request: PipelineRecoveryPanelProps["request"] = async <T,>(path: string, options?: RequestInit) =>
    await transport(path, options) as T;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, refetchOnWindowFocus: false } } });
  clients.push(client);
  const onAccepted = vi.fn();
  const panel = (runId: string) => <QueryClientProvider client={client}>
    <PipelineRecoveryPanel runId={runId} request={request} onAccepted={onAccepted} />
  </QueryClientProvider>;
  const view = render(panel("run-a"));
  return { transport, onAccepted, rerender: (runId: string) => view.rerender(panel(runId)) };
}

async function openDecision() {
  fireEvent.click(await screen.findByRole("button", { name: "Review and record decision" }));
  return screen.getByRole("form", { name: "Record recovery for work" });
}

function verify(form: HTMLElement, reason = "Observed the original execution.") {
  fireEvent.change(within(form).getByLabelText("Reason for the decision"), { target: { value: reason } });
  fireEvent.click(within(form).getByRole("checkbox"));
}

function postedBodies(transport: ReturnType<typeof mountPanel>["transport"]) {
  return transport.mock.calls.filter(([, options]) => options?.method === "POST")
    .map(([, options]) => JSON.parse(String(options?.body)) as Record<string, unknown>);
}

afterEach(() => {
  cleanup();
  for (const client of clients) client.clear();
  clients.length = 0;
  vi.restoreAllMocks();
});

describe("pipeline recovery decisions", () => {
  it("records only an explicitly verified delivery with the exact receipt and prevents duplicate submission", async () => {
    let finish!: (value: unknown) => void;
    const pending = new Promise<unknown>((resolve) => { finish = resolve; });
    let recorded = false;
    const { transport, onAccepted } = mountPanel(async (_path, options) => {
      if (options?.method === "POST") {
        const result = await pending;
        recorded = true;
        return result;
      }
      return inspection("run-a", "delivery", recorded ? { can_accept: false, resolution_action: "accept_delivery" } : {});
    });
    const form = await openDecision();
    const submit = within(form).getByRole("button", { name: "Record decision" });
    expect(submit).toBeDisabled();
    expect(within(form).queryByLabelText("Original exit code")).not.toBeInTheDocument();
    expect(within(form).queryByLabelText("Complete command output")).not.toBeInTheDocument();
    verify(form, "  Verified the prompt in the original agent transcript.  ");
    fireEvent.click(submit);
    fireEvent.submit(form);
    expect(postedBodies(transport)).toEqual([{
      step_id: "work", receipt: TOKEN_A, accept_delivery: true,
      reason: "Verified the prompt in the original agent transcript.",
    }]);
    expect(onAccepted).not.toHaveBeenCalled();
    await act(async () => { finish({ success: true, run_id: "run-a" }); });
    await waitFor(() => expect(onAccepted).toHaveBeenCalledWith("run-a"));
    expect(screen.getByText(/Use Resume when you are ready/)).toBeInTheDocument();
    expect(transport.mock.calls.every(([path]) => path === "/api/v1/pipelines/run-a/recover")).toBe(true);
    expect(postedBodies(transport)).toHaveLength(1);
  });

  it.each(["command", "selector"] as const)("requires an explicit %s exit code and preserves the verified output exactly", async (kind) => {
    let recorded = false;
    const { transport } = mountPanel(async (_path, options) => {
      if (options?.method === "POST") {
        recorded = true;
        return { success: true, run_id: "run-a" };
      }
      return inspection("run-a", kind, recorded ? { can_accept: false, resolution_action: `accept_${kind}` } : {});
    });
    const form = await openDecision();
    verify(form);
    const submit = within(form).getByRole("button", { name: "Record decision" });
    const exit = within(form).getByLabelText("Original exit code");
    expect(submit).toBeDisabled();
    for (const invalid of ["256", "-1", "1.5"]) {
      fireEvent.change(exit, { target: { value: invalid } });
      expect(submit).toBeDisabled();
    }
    expect(postedBodies(transport)).toEqual([]);
    fireEvent.change(exit, { target: { value: "0" } });
    const output = kind === "selector" ? "  [\"first\", \"second\"]\n\n" : "";
    if (kind === "selector") {
      fireEvent.change(within(form).getByLabelText("Complete selector standard output"), { target: { value: output } });
    }
    expect(within(form).getByRole("checkbox")).not.toBeChecked();
    fireEvent.click(within(form).getByRole("checkbox"));
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    await waitFor(() => expect(postedBodies(transport)).toHaveLength(1));
    expect(postedBodies(transport)[0]).toEqual({
      step_id: "work", receipt: TOKEN_A, [`accept_${kind}`]: true,
      reason: "Observed the original execution.", exit_code: 0, output,
    });
    expect(await screen.findByText(`Recorded decision: accept_${kind}.`)).toBeInTheDocument();
  });

  it("shows the refusal reason and offers no acceptance for an ineligible receipt", async () => {
    const { transport } = mountPanel(async () => inspection("run-a", "command", {
      can_accept: false, refusal_reason: "The original command process is still running.",
    }));
    expect(await screen.findByText("The original command process is still running.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Review and record decision" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record decision" })).not.toBeInTheDocument();
    expect(postedBodies(transport)).toEqual([]);
  });

  it("refreshes a stale receipt after a conflict and requires a new review before retrying", async () => {
    let posts = 0;
    const { transport } = mountPanel(async (_path, options) => {
      if (options?.method === "POST") {
        posts++;
        if (posts === 1) throw new ApiError(409, "RECOVERY_REFUSED", "The receipt changed.");
        return { success: true, run_id: "run-a" };
      }
      return inspection("run-a", "delivery", posts > 0 ? { receipt_token: TOKEN_B,
        ...(posts > 1 ? { can_accept: false, resolution_action: "accept_delivery" } : {}) } : {});
    });
    let form = await openDecision();
    verify(form, "Old receipt observation.");
    fireEvent.click(within(form).getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText(/The receipt changed/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Review and record decision" })).toBeEnabled());
    expect(postedBodies(transport)).toHaveLength(1);
    form = await openDecision();
    expect(within(form).getByLabelText("Reason for the decision")).toHaveValue("");
    expect(within(form).getByRole("checkbox")).not.toBeChecked();
    expect(within(form).getByRole("button", { name: "Record decision" })).toBeDisabled();
    verify(form, "Verified the replacement receipt independently.");
    fireEvent.click(within(form).getByRole("button", { name: "Record decision" }));
    await waitFor(() => expect(postedBodies(transport)).toHaveLength(2));
    expect(postedBodies(transport).map((body) => body.receipt)).toEqual([TOKEN_A, TOKEN_B]);
    expect(postedBodies(transport)[1].reason).toBe("Verified the replacement receipt independently.");
    expect(await screen.findByText("Recorded decision: accept_delivery.")).toBeInTheDocument();
  });

  it("keeps retry disabled after a failed refresh and never automatically repeats an uncertain POST", async () => {
    let posts = 0;
    let failedRefresh = false;
    const { transport } = mountPanel(async (_path, options) => {
      if (options?.method === "POST") {
        posts++;
        if (posts === 1) throw new Error("The response was lost.");
        return { success: true, run_id: "run-a" };
      }
      if (posts === 1 && !failedRefresh) {
        failedRefresh = true;
        throw new Error("Inspection is offline.");
      }
      return inspection("run-a", "delivery", posts > 1 ? { can_accept: false, resolution_action: "accept_delivery" } : {});
    });
    const form = await openDecision();
    verify(form);
    fireEvent.click(within(form).getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText(/Inspection is offline/)).toBeInTheDocument();
    expect(within(form).getByRole("button", { name: "Record decision" })).toBeDisabled();
    expect(within(form).getByRole("checkbox")).not.toBeChecked();
    expect(postedBodies(transport)).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Refresh receipts" }));
    await waitFor(() => expect(within(form).getByRole("checkbox")).toBeEnabled());
    expect(postedBodies(transport)).toHaveLength(1);
    fireEvent.click(within(form).getByRole("checkbox"));
    fireEvent.click(within(form).getByRole("button", { name: "Record decision" }));
    await waitFor(() => expect(postedBodies(transport)).toHaveLength(2));
    expect(await screen.findByText("Recorded decision: accept_delivery.")).toBeInTheDocument();
  });

  it("clears reviewed fields when the selected run changes, even with matching step and receipt identifiers", async () => {
    const { transport, rerender } = mountPanel(async (path) => inspection(path.includes("run-b") ? "run-b" : "run-a"));
    const originalForm = await openDecision();
    verify(originalForm, "Observation for run A only.");
    rerender("run-b");
    await waitFor(() => expect(screen.getByRole("button", { name: "Review and record decision" })).toBeEnabled());
    const form = await openDecision();
    expect(within(form).getByLabelText("Reason for the decision")).toHaveValue("");
    expect(within(form).getByRole("checkbox")).not.toBeChecked();
    expect(within(form).getByRole("button", { name: "Record decision" })).toBeDisabled();
    expect(postedBodies(transport)).toEqual([]);
    expect(transport.mock.calls.at(-1)?.[0]).toBe("/api/v1/pipelines/run-b/recover");
  });

  it("does not report an old run's pending acceptance as a decision for the newly selected run", async () => {
    let finish!: (value: unknown) => void;
    const pending = new Promise<unknown>((resolve) => { finish = resolve; });
    const { onAccepted, rerender, transport } = mountPanel(async (path, options) => {
      if (options?.method === "POST") return pending;
      return inspection(path.includes("run-b") ? "run-b" : "run-a");
    });
    const form = await openDecision();
    verify(form);
    fireEvent.click(within(form).getByRole("button", { name: "Record decision" }));
    expect(postedBodies(transport)).toHaveLength(1);
    rerender("run-b");
    await waitFor(() => expect(screen.getByRole("button", { name: "Review and record decision" })).toBeEnabled());
    await act(async () => { finish({ success: true, run_id: "run-a" }); });
    expect(onAccepted).not.toHaveBeenCalled();
    expect(screen.queryByText(/Decision recorded/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review and record decision" })).toBeEnabled();
    expect(postedBodies(transport)).toHaveLength(1);
  });

  it("requires a nonempty reason and enforces its UTF-8 byte limit", async () => {
    const { transport } = mountPanel(async () => inspection());
    const form = await openDecision();
    verify(form, "   \n  ");
    const submit = within(form).getByRole("button", { name: "Record decision" });
    expect(submit).toBeDisabled();
    fireEvent.change(within(form).getByLabelText("Reason for the decision"), { target: { value: "😀".repeat(1025) } });
    expect(submit).toBeDisabled();
    expect(within(form).getByRole("alert")).toHaveTextContent("4,096-byte limit");
    expect(postedBodies(transport)).toEqual([]);
  });
});
