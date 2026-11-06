"use client";

import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiRequestSignal, formatApiErrorMessage } from "@/lib/api/client";

type RecoveryKind = "delivery" | "command" | "selector";

interface RecoveryReceipt {
  step_id: string;
  status: string;
  receipt_token: string;
  can_accept: boolean;
  refusal_reason?: string;
  pane_id?: string;
  pane_pid?: number;
  agent_type?: string;
  process_id?: number;
  resolution_action?: string;
  resolution_reason?: string;
  resolved_at?: string;
  exit_code?: number;
  output_bytes?: number;
}

interface RecoveryInspection {
  success: boolean;
  error?: string;
  run_id: string;
  deliveries: RecoveryReceipt[];
  commands: RecoveryReceipt[];
  selectors: RecoveryReceipt[];
}

interface RecoveryAcceptance {
  success: boolean;
  error?: string;
  run_id: string;
}

interface RecoveryEntry extends RecoveryReceipt {
  kind: RecoveryKind;
}

interface RecoveryDecision {
  reason: string;
  exitCode?: number;
  output?: string;
}

export interface PipelineRecoveryPanelProps {
  runId: string;
  request: <T>(path: string, options?: RequestInit) => Promise<T>;
  onAccepted?: (runId: string) => void;
}

const RECOVERY_TIMEOUT_MS = 45_000;
const KIND_LABELS: Record<RecoveryKind, string> = {
  delivery: "Agent delivery",
  command: "Command outcome",
  selector: "Selector outcome",
};

function receiptKey(entry: RecoveryEntry): string {
  return `${entry.kind}:${entry.step_id}:${entry.receipt_token}`;
}

// A different run gets a fresh query/form lifetime, including during an
// outstanding acceptance. No decision fields are carried to another run.
export default function PipelineRecoveryPanel(props: PipelineRecoveryPanelProps) {
  return <RunRecoveryPanel key={props.runId} {...props} />;
}

function RunRecoveryPanel({ runId, request, onAccepted }: PipelineRecoveryPanelProps) {
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [acceptedKeys, setAcceptedKeys] = useState<Set<string>>(() => new Set());
  const [notice, setNotice] = useState<{ error: boolean; message: string } | null>(null);
  const submitting = useRef(false);
  const mounted = useRef(true);
  const path = `/api/v1/pipelines/${encodeURIComponent(runId)}/recover`;

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  const inspection = useQuery({
    queryKey: ["pipelines", runId, "recovery"],
    queryFn: async () => {
      const response = await request<RecoveryInspection>(path, {
        signal: apiRequestSignal(RECOVERY_TIMEOUT_MS),
      });
      if (!response.success) {
        throw new Error(response.error || "Recovery inspection failed.");
      }
      if (response.run_id !== runId || !Array.isArray(response.deliveries) ||
          !Array.isArray(response.commands) || !Array.isArray(response.selectors)) {
        throw new Error("The recovery response does not identify this run and all its receipt collections.");
      }
      return response;
    },
    retry: false,
  });

  const entries: RecoveryEntry[] = [
    ...(inspection.data?.deliveries ?? []).map((entry) => ({ ...entry, kind: "delivery" as const })),
    ...(inspection.data?.commands ?? []).map((entry) => ({ ...entry, kind: "command" as const })),
    ...(inspection.data?.selectors ?? []).map((entry) => ({ ...entry, kind: "selector" as const })),
  ];

  const blocked = busyKey !== null || inspection.isFetching || inspection.isError;

  async function accept(entry: RecoveryEntry, decision: RecoveryDecision) {
    const key = receiptKey(entry);
    const current = entries.find((candidate) => receiptKey(candidate) === key);
    if (submitting.current || blocked || acceptedKeys.has(key) || !current?.can_accept) return;

    // The token is the one the operator reviewed, never a replacement fetched
    // after submission. The server independently verifies its exact identity.
    const body: Record<string, unknown> = {
      step_id: entry.step_id,
      receipt: entry.receipt_token,
      reason: decision.reason,
      [`accept_${entry.kind}`]: true,
    };
    if (entry.kind !== "delivery") {
      body.exit_code = decision.exitCode;
      body.output = decision.output;
    }

    submitting.current = true;
    setBusyKey(key);
    setNotice(null);
    let accepted = false;
    try {
      const response = await request<RecoveryAcceptance>(path, {
        method: "POST",
        body: JSON.stringify(body),
        signal: apiRequestSignal(RECOVERY_TIMEOUT_MS),
      });
      if (!response.success || response.run_id !== runId) {
        throw new Error(response.error || "The server did not confirm this run's recovery decision.");
      }
      accepted = true;
      if (mounted.current) {
        setAcceptedKeys((previous) => new Set(previous).add(key));
        setNotice({ error: false, message: "Decision recorded. Use Resume when you are ready to continue the pipeline." });
      }
    } catch (error) {
      if (mounted.current) {
        setNotice({
          error: true,
          message: `${formatApiErrorMessage(error)} Review the refreshed receipts before trying again.`,
        });
      }
    } finally {
      // A failed response may follow a committed decision, or a stale token
      // may now identify different work. Refresh before enabling any retry.
      if (mounted.current) {
        await inspection.refetch({ cancelRefetch: false, throwOnError: false });
        if (mounted.current) setBusyKey(null);
      }
      submitting.current = false;
    }
    if (accepted && mounted.current) onAccepted?.(runId);
  }

  return (
    <section aria-label="Pipeline recovery" aria-busy={inspection.isFetching || busyKey !== null}
      className="rounded-md border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4 space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white">Recovery decisions</h3>
        <button type="button" onClick={() => { void inspection.refetch(); }}
          disabled={inspection.isFetching || busyKey !== null}
          className="px-2 py-1 rounded border border-gray-200 dark:border-gray-600 text-xs disabled:opacity-50">
          Refresh receipts
        </button>
      </div>
      <p className="text-xs text-gray-600 dark:text-gray-300">
        Inspect the original agent or process before recording a verified result. Recording a decision
        leaves execution paused; use Resume separately to continue.
      </p>
      {notice && <p role={notice.error ? "alert" : "status"}
        className={notice.error ? "text-xs text-red-600 dark:text-red-300" : "text-xs text-green-700 dark:text-green-300"}>
        {notice.message}
      </p>}
      {inspection.isPending && <p role="status" className="text-xs text-gray-500">Loading recovery receipts…</p>}
      {inspection.isError && <p role="alert" className="text-xs text-red-600 dark:text-red-300">
        {formatApiErrorMessage(inspection.error)} Refresh receipts successfully before recording a decision.
      </p>}
      {inspection.isSuccess && entries.length === 0 && <p className="text-xs text-gray-500 dark:text-gray-400">
        No uncertain execution receipts or recorded recovery decisions were found for this run.
      </p>}
      <div className="space-y-3">
        {entries.map((entry) => <RecoveryReceiptCard key={receiptKey(entry)} entry={entry}
          blocked={blocked} saving={busyKey === receiptKey(entry)}
          accepted={acceptedKeys.has(receiptKey(entry))} onAccept={accept} />)}
      </div>
    </section>
  );
}

function RecoveryReceiptCard({ entry, blocked, saving, accepted, onAccept }: {
  entry: RecoveryEntry;
  blocked: boolean;
  saving: boolean;
  accepted: boolean;
  onAccept: (entry: RecoveryEntry, decision: RecoveryDecision) => Promise<void>;
}) {
  const id = useId();
  const [editing, setEditing] = useState(false);
  const [reason, setReason] = useState("");
  const [exitCode, setExitCode] = useState("");
  const [output, setOutput] = useState("");
  const [verified, setVerified] = useState(false);
  const outcome = entry.kind !== "delivery";
  const trimmedReason = reason.trim();
  const reasonBytes = new TextEncoder().encode(trimmedReason).length;
  const validReason = reasonBytes > 0 && reasonBytes <= 4096;
  const validExitCode = /^\d+$/.test(exitCode) && Number(exitCode) <= 255;
  const eligible = entry.can_accept && !accepted && !entry.resolution_action;
  const valid = validReason && verified && (!outcome || validExitCode);
  const label = KIND_LABELS[entry.kind];

  return (
    <article aria-label={`${label} ${entry.step_id}`}
      className="rounded border border-gray-200 dark:border-gray-700 p-3 space-y-2 text-xs">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="font-medium text-gray-900 dark:text-white break-all">{entry.step_id}</h4>
        <span className="text-gray-500 dark:text-gray-400">{label} · {entry.status}</span>
      </div>
      {entry.pane_id && <p className="text-gray-600 dark:text-gray-300">
        Original agent: {entry.agent_type || "agent"}, pane {entry.pane_id}
        {entry.pane_pid ? `, recorded PID ${entry.pane_pid}` : ""}.
      </p>}
      {entry.process_id ? <p className="text-gray-600 dark:text-gray-300">Recorded process PID: {entry.process_id}.</p> : null}
      {entry.resolution_action ? <div className="text-green-700 dark:text-green-300 space-y-1">
        <p>Recorded decision: {entry.resolution_action}.</p>
        {entry.resolution_reason && <p>{entry.resolution_reason}</p>}
        {entry.exit_code !== undefined && <p>Exit code: {entry.exit_code}. Recorded output: {entry.output_bytes ?? 0} bytes.</p>}
      </div> : accepted ? <p className="text-green-700 dark:text-green-300">Decision recorded. Refresh receipts to inspect the saved decision.</p>
        : !entry.can_accept ? <p className="text-gray-600 dark:text-gray-300">
          {entry.refusal_reason || (entry.kind === "delivery" && entry.status === "delivered"
            ? "Submission is already confirmed. Resume can observe the original agent's result."
            : "This receipt cannot accept an operator decision in its current state.")}
        </p> : null}
      {eligible && !editing && <button type="button" onClick={() => setEditing(true)} disabled={blocked}
        className="px-2 py-1 rounded border border-blue-300 text-blue-700 dark:text-blue-300 disabled:opacity-50">
        Review and record decision
      </button>}
      {eligible && editing && <form aria-label={`Record recovery for ${entry.step_id}`} className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (!valid || blocked) return;
          setVerified(false);
          void onAccept(entry, {
            reason: trimmedReason,
            ...(outcome ? { exitCode: Number(exitCode), output } : {}),
          });
        }}>
        <p className="text-gray-600 dark:text-gray-300">
          {entry.kind === "delivery"
            ? "Verify that the original agent received this prompt. Resume will observe that delivery."
            : entry.kind === "selector"
              ? "Verify the selector process and all child and descendant processes have stopped. Supply its original exit code and complete standard output, which determines the pipeline's selected work."
              : "Verify the original command and its child processes have stopped. Supply its original exit code and complete captured output."}
        </p>
        <div className="space-y-1">
          <label htmlFor={`${id}-reason`} className="block font-medium">Reason for the decision</label>
          <textarea id={`${id}-reason`} value={reason}
            onChange={(event) => { setReason(event.target.value); setVerified(false); }}
            disabled={blocked} rows={2} className="w-full rounded border border-gray-300 dark:border-gray-600 bg-transparent px-2 py-1"
            placeholder="Explain what you verified." />
          {reasonBytes > 4096 && <p role="alert" className="text-red-600">The reason exceeds the 4,096-byte limit.</p>}
        </div>
        {outcome && <>
          <div className="space-y-1">
            <label htmlFor={`${id}-exit`} className="block font-medium">Original exit code</label>
            <input id={`${id}-exit`} type="number" min={0} max={255} step={1} value={exitCode}
              onChange={(event) => { setExitCode(event.target.value); setVerified(false); }} disabled={blocked}
              className="w-28 rounded border border-gray-300 dark:border-gray-600 bg-transparent px-2 py-1" />
            <p className="text-gray-500 dark:text-gray-400">Enter a whole number from 0 to 255.</p>
          </div>
          <div className="space-y-1">
            <label htmlFor={`${id}-output`} className="block font-medium">
              {entry.kind === "selector" ? "Complete selector standard output" : "Complete command output"}
            </label>
            <textarea id={`${id}-output`} value={output}
              onChange={(event) => { setOutput(event.target.value); setVerified(false); }}
              disabled={blocked} rows={5} spellCheck={false}
              className="w-full rounded border border-gray-300 dark:border-gray-600 bg-transparent px-2 py-1 font-mono" />
            <p className="text-gray-500 dark:text-gray-400">Leave this blank only if you verified that the original output was empty.</p>
          </div>
        </>}
        <label className="flex items-start gap-2" htmlFor={`${id}-verified`}>
          <input id={`${id}-verified`} type="checkbox" checked={verified} disabled={blocked}
            onChange={(event) => setVerified(event.target.checked)} />
          <span>{outcome
            ? "I verified the original process and all child and descendant processes have stopped, and the exit code and complete output above are correct, including any intentionally empty output."
            : "I verified this prompt reached the original agent shown above."}</span>
        </label>
        <div className="flex gap-2">
          <button type="submit" disabled={blocked || !valid}
            className="px-3 py-1.5 rounded bg-blue-600 text-white disabled:opacity-50">
            {saving ? "Recording…" : "Record decision"}
          </button>
          <button type="button" onClick={() => { setEditing(false); setVerified(false); }} disabled={blocked}
            className="px-3 py-1.5 rounded border border-gray-300 dark:border-gray-600 disabled:opacity-50">Close</button>
        </div>
      </form>}
    </article>
  );
}
