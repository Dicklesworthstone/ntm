"use client";

/**
 * SessionGrepExtractErrorsPanel
 *
 * Surfaces multi-harness pane scrollback regex grep, fenced Markdown code-block
 * extraction, and structured error-signature filtering:
 *   - GET /api/v1/sessions/{id}/grep
 *   - GET /api/v1/grep
 *   - GET /api/v1/sessions/{id}/extract
 *   - GET /api/v1/sessions/{id}/errors
 */

import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

const HARNESS_FILTER_OPTIONS = [
  { value: "", label: "All 12 Harnesses" },
  { value: "cc", label: "Claude (cc)" },
  { value: "cod", label: "Codex (cod)" },
  { value: "gmi", label: "Gemini (gmi)" },
  { value: "agy", label: "Antigravity (agy)" },
  { value: "grok", label: "Grok Build (grok)" },
  { value: "cursor", label: "Cursor" },
  { value: "windsurf", label: "Windsurf" },
  { value: "aider", label: "Aider" },
  { value: "oc", label: "OpenCode (oc)" },
  { value: "omp", label: "Oh My Pi (omp)" },
  { value: "pi", label: "Pi Agent (pi)" },
  { value: "ollama", label: "Ollama" },
] as const;

export interface GrepMatchItem {
  session: string;
  pane: string;
  pane_id: string;
  pane_index: number;
  agent_type?: string;
  line: number;
  content: string;
  context?: string[];
  context_before?: number;
  context_after?: number;
}

export interface SessionGrepResponse extends ApiEnvelope {
  pattern: string;
  session: string;
  matches: GrepMatchItem[];
  matching_panes: string[];
  total_lines_searched: number;
  match_count: number;
  panes_searched: number;
  warnings?: string[];
}

export interface ExtractedCodeBlockItem {
  language: string;
  content: string;
  file_path?: string;
  is_new?: boolean;
  source_pane?: string;
  start_line?: number;
  end_line?: number;
}

export interface SessionExtractResponse extends ApiEnvelope {
  session: string;
  source: string;
  language?: string;
  blocks: ExtractedCodeBlockItem[];
  total_found: number;
  panes_scanned: number;
}

export interface SessionErrorEntryItem {
  timestamp: string;
  pane_id: string;
  pane_name: string;
  pane_index: number;
  agent_type: string;
  line_number: number;
  content: string;
  match_type: string;
  context?: string[];
}

export interface SessionErrorsResponse extends ApiEnvelope {
  session: string;
  errors: SessionErrorEntryItem[];
  summary: {
    total_errors: number;
    total_lines_searched: number;
    panes_searched: number;
    by_type?: Record<string, number>;
    by_agent?: Record<string, number>;
  };
  truncated?: boolean;
}

export function SessionGrepExtractErrorsPanel({
  sessionId,
}: {
  sessionId: string;
}) {
  const [targetSession, setTargetSession] = useState(sessionId);
  const [searchAllSessions, setSearchAllSessions] = useState(!sessionId);
  const [grepPattern, setGrepPattern] = useState("");
  const [harnessType, setHarnessType] = useState("");
  const [ignoreCase, setIgnoreCase] = useState(false);
  const [contextLines, setContextLines] = useState("1");
  const [extractLang, setExtractLang] = useState("");
  const [activeTab, setActiveTab] = useState<"grep" | "extract" | "errors">(
    "grep"
  );
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    setTargetSession(sessionId);
    setSearchAllSessions(!sessionId);
  }, [sessionId]);

  const trimmedSession = targetSession.trim();

  const grepMutation = useMutation({
    mutationFn: async () => {
      const pat = grepPattern.trim();
      if (!pat) {
        throw new Error("Enter a regular expression pattern to search");
      }
      const params = new URLSearchParams();
      params.set("pattern", pat);
      if (harnessType) {
        params.set("type", harnessType);
      }
      if (ignoreCase) {
        params.set("ignore_case", "true");
      }
      const ctxNum = Number.parseInt(contextLines, 10);
      if (Number.isFinite(ctxNum) && ctxNum > 0) {
        params.set("context", String(ctxNum));
      }
      if (searchAllSessions || !trimmedSession) {
        params.set("all", "true");
        return apiFetch<SessionGrepResponse>(
          `/api/v1/grep?${params.toString()}`
        );
      }
      return apiFetch<SessionGrepResponse>(
        `/api/v1/sessions/${encodeURIComponent(trimmedSession)}/grep?${params.toString()}`
      );
    },
    onError: (err) => {
      setNotice(`Grep failed: ${formatApiErrorMessage(err)}`);
    },
    onSuccess: () => {
      setNotice(null);
    },
  });

  const extractQuery = useQuery({
    queryKey: ["session-extract", trimmedSession, extractLang],
    enabled: activeTab === "extract" && Boolean(trimmedSession),
    queryFn: () => {
      const params = new URLSearchParams();
      if (extractLang.trim()) {
        params.set("lang", extractLang.trim());
      }
      params.set("lines", "500");
      return apiFetch<SessionExtractResponse>(
        `/api/v1/sessions/${encodeURIComponent(trimmedSession)}/extract?${params.toString()}`
      );
    },
  });

  const errorsQuery = useQuery({
    queryKey: ["session-errors", trimmedSession, harnessType],
    enabled: activeTab === "errors" && Boolean(trimmedSession),
    queryFn: () => {
      const params = new URLSearchParams();
      if (harnessType) {
        params.set("agent_type", harnessType);
      }
      params.set("context", "2");
      return apiFetch<SessionErrorsResponse>(
        `/api/v1/sessions/${encodeURIComponent(trimmedSession)}/errors?${params.toString()}`
      );
    },
  });

  return (
    <section
      aria-label="Pane Grep, Code-Block Extract & Error Signatures"
      className="rounded-lg border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800 space-y-4"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="text-base font-semibold text-gray-900 dark:text-white">
            Pane Scrollback Grep, Code-Block Extract &amp; Error Signatures
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Search pane scrollback with regex across 12 agent harnesses, extract
            fenced Markdown code blocks, or isolate panics and stack traces.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          {(["grep", "extract", "errors"] as const).map((tab) => (
            <button
              key={tab}
              type="button"
              onClick={() => setActiveTab(tab)}
              className={`rounded-md px-3 py-1.5 text-xs font-medium ${
                activeTab === tab
                  ? "bg-blue-600 text-white"
                  : "border border-gray-200 text-gray-700 hover:bg-gray-50 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-gray-700"
              }`}
            >
              {tab === "grep"
                ? "Regex Grep"
                : tab === "extract"
                  ? "Code Blocks"
                  : "Error Signatures"}
            </button>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <label className="block text-xs text-gray-600 dark:text-gray-300">
          Session Name
          <input
            type="text"
            aria-label="Grep or extract target session"
            value={targetSession}
            onChange={(e) => setTargetSession(e.target.value)}
            className="mt-1 w-full rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 dark:border-gray-600 dark:bg-gray-900 dark:text-white"
          />
        </label>
        <label className="block text-xs text-gray-600 dark:text-gray-300">
          12-Harness Filter
          <select
            aria-label="12-Harness pane filter"
            value={harnessType}
            onChange={(e) => setHarnessType(e.target.value)}
            className="mt-1 w-full rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 dark:border-gray-600 dark:bg-gray-900 dark:text-white"
          >
            {HARNESS_FILTER_OPTIONS.map((opt) => (
              <option key={opt.value || "all"} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
        </label>
        <div className="flex items-end gap-3">
          <label className="inline-flex items-center gap-1.5 text-xs text-gray-600 dark:text-gray-300">
            <input
              type="checkbox"
              checked={searchAllSessions}
              onChange={(e) => setSearchAllSessions(e.target.checked)}
            />
            Search all sessions (grep)
          </label>
        </div>
      </div>

      {notice && (
        <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          {notice}
        </div>
      )}

      {activeTab === "grep" && (
        <div className="space-y-3">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
            <label className="flex-1 text-xs text-gray-600 dark:text-gray-300">
              Regular Expression Pattern
              <input
                type="text"
                aria-label="Pane grep regex pattern"
                value={grepPattern}
                onChange={(e) => setGrepPattern(e.target.value)}
                className="mt-1 w-full rounded-md border border-gray-300 bg-white px-2.5 py-1.5 font-mono text-xs text-gray-900 dark:border-gray-600 dark:bg-gray-900 dark:text-white"
              />
            </label>
            <label className="w-24 text-xs text-gray-600 dark:text-gray-300">
              Context
              <input
                type="number"
                min={0}
                max={10}
                aria-label="Grep context lines"
                value={contextLines}
                onChange={(e) => setContextLines(e.target.value)}
                className="mt-1 w-full rounded-md border border-gray-300 bg-white px-2 py-1.5 text-xs text-gray-900 dark:border-gray-600 dark:bg-gray-900 dark:text-white"
              />
            </label>
            <label className="inline-flex items-center gap-1.5 pb-2 text-xs text-gray-600 dark:text-gray-300">
              <input
                type="checkbox"
                checked={ignoreCase}
                onChange={(e) => setIgnoreCase(e.target.checked)}
              />
              Ignore case
            </label>
            <button
              type="button"
              onClick={() => grepMutation.mutate()}
              disabled={grepMutation.isPending}
              className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
            >
              {grepMutation.isPending ? "Searching..." : "Run Pane Grep"}
            </button>
          </div>

          {grepMutation.data && (
            <div className="space-y-2">
              <div className="text-xs text-gray-500 dark:text-gray-400">
                Found {grepMutation.data.match_count} match(es) across{" "}
                {grepMutation.data.panes_searched} pane(s) (
                {grepMutation.data.total_lines_searched} lines scanned).
              </div>
              {Array.isArray(grepMutation.data.warnings) &&
                grepMutation.data.warnings.length > 0 && (
                  <div className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
                    {grepMutation.data.warnings.join(" · ")}
                  </div>
                )}
              {grepMutation.data.matches.length > 0 && (
                <div className="max-h-64 space-y-2 overflow-y-auto">
                  {grepMutation.data.matches.map((m, idx) => (
                    <div
                      key={`${m.pane_id}-${m.line}-${idx}`}
                      className="rounded border border-gray-200 bg-gray-50 p-2.5 text-xs dark:border-gray-700 dark:bg-gray-900/50"
                    >
                      <div className="font-semibold text-gray-800 dark:text-gray-200">
                        {m.session}/{m.pane}:{m.line}
                        {m.agent_type ? ` (${m.agent_type})` : ""}
                      </div>
                      <pre className="mt-1 overflow-x-auto font-mono text-xs text-blue-700 dark:text-blue-300">
                        {m.content}
                      </pre>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {activeTab === "extract" && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-end gap-2">
            <label className="text-xs text-gray-600 dark:text-gray-300">
              Language Filter
              <input
                type="text"
                aria-label="Extract language filter"
                value={extractLang}
                onChange={(e) => setExtractLang(e.target.value)}
                className="mt-1 block rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 dark:border-gray-600 dark:bg-gray-900 dark:text-white"
              />
            </label>
            <button
              type="button"
              disabled={!trimmedSession || extractQuery.isFetching}
              onClick={() => void extractQuery.refetch()}
              className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {extractQuery.isFetching ? "Extracting..." : "Extract Code Blocks"}
            </button>
          </div>
          {!trimmedSession && (
            <div className="text-xs text-gray-500 dark:text-gray-400">
              Enter a session name above to extract fenced code blocks.
            </div>
          )}
          {extractQuery.data && (
            <div className="space-y-2">
              <div className="text-xs text-gray-500 dark:text-gray-400">
                Extracted {extractQuery.data.total_found} block(s) across{" "}
                {extractQuery.data.panes_scanned} pane(s).
              </div>
              {extractQuery.data.blocks.map((block, idx) => (
                <div
                  key={`${block.source_pane || "pane"}-${idx}`}
                  className="rounded border border-gray-200 bg-gray-50 p-3 dark:border-gray-700 dark:bg-gray-900/50"
                >
                  <div className="flex items-center justify-between text-xs font-medium text-gray-700 dark:text-gray-300">
                    <span>
                      [{idx + 1}] {block.language || "text"}
                      {block.file_path ? ` · ${block.file_path}` : ""}
                    </span>
                    <span className="font-mono text-gray-500">
                      {block.source_pane || extractQuery.data.source}
                    </span>
                  </div>
                  <pre className="mt-2 max-h-40 overflow-auto rounded bg-gray-900 p-2 font-mono text-xs text-gray-100">
                    {block.content}
                  </pre>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {activeTab === "errors" && (
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {trimmedSession
                ? `Scanning error signatures in "${trimmedSession}"`
                : "Enter a session name above to scan pane error signatures."}
            </span>
            <button
              type="button"
              disabled={!trimmedSession || errorsQuery.isFetching}
              onClick={() => void errorsQuery.refetch()}
              className="rounded-md border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50 dark:border-gray-600 dark:text-gray-200 dark:hover:bg-gray-700"
            >
              {errorsQuery.isFetching ? "Scanning..." : "Refresh Errors"}
            </button>
          </div>
          {errorsQuery.data && (
            <div className="space-y-2">
              <div className="text-xs text-gray-500 dark:text-gray-400">
                {errorsQuery.data.summary.total_errors} error(s) in{" "}
                {errorsQuery.data.summary.panes_searched} pane(s) (
                {errorsQuery.data.summary.total_lines_searched} lines).
              </div>
              {errorsQuery.data.truncated && (
                <div className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
                  Results truncated at the maximum error limit; narrow pane, agent, or line filters to inspect earlier errors.
                </div>
              )}
              {errorsQuery.data.errors.map((errItem, idx) => (
                <div
                  key={`${errItem.pane_id}-${errItem.line_number}-${idx}`}
                  className="rounded border border-red-200 bg-red-50 p-2.5 text-xs dark:border-red-800 dark:bg-red-900/20"
                >
                  <div className="font-semibold text-red-800 dark:text-red-300">
                    {errItem.pane_name} ({errItem.agent_type}) · line{" "}
                    {errItem.line_number} · [{errItem.match_type}]
                  </div>
                  <div className="mt-1 font-mono text-red-700 dark:text-red-200">
                    {errItem.content}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
