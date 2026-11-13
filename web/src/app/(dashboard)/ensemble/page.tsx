"use client";

/**
 * Reasoning Ensemble Studio
 *
 * Operator cockpit for multi-agent reasoning ensembles backed by:
 *   - GET  /api/v1/ensemble/modes
 *   - GET  /api/v1/ensemble/presets
 *   - POST /api/v1/ensemble/suggest
 *   - POST /api/v1/ensemble/spawn
 *   - GET  /api/v1/ensemble/sessions/{session}
 *   - POST /api/v1/ensemble/sessions/{session}/synthesize
 *   - POST /api/v1/ensemble/sessions/{session}/stop
 */

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
  type components,
} from "@/lib/api/client";

type EnsembleModesResponse =
  components["schemas"]["EnsembleModesEnvelopeResponse"];
type EnsemblePresetsResponse =
  components["schemas"]["EnsemblePresetsEnvelopeResponse"];
type EnsembleSuggestRequest =
  components["schemas"]["EnsembleSuggestRequest"];
type EnsembleSuggestResponse =
  components["schemas"]["EnsembleSuggestEnvelopeResponse"];
type EnsembleSessionResponse =
  components["schemas"]["EnsembleSessionEnvelopeResponse"];
type EnsembleSpawnRequest = components["schemas"]["EnsembleSpawnRequest"];
type EnsembleSpawnResponse =
  components["schemas"]["EnsembleSpawnEnvelopeResponse"];
type EnsembleSynthesizeRequest =
  components["schemas"]["EnsembleSynthesizeRequest"];
type EnsembleSynthesizeResponse =
  components["schemas"]["EnsembleSynthesizeEnvelopeResponse"];
type EnsembleStopResponse =
  components["schemas"]["EnsembleStopEnvelopeResponse"];

interface SessionsListResponse extends ApiEnvelope {
  sessions?: Array<{ name: string; status?: string }>;
}

export default function EnsemblePage() {
  const queryClient = useQueryClient();

  // Recommender & Spawn state
  const [question, setQuestion] = useState("");
  const [spawnSession, setSpawnSession] = useState("ensemble-analysis");
  const [selectedPreset, setSelectedPreset] = useState("project-diagnosis");
  const [agentMix, setAgentMix] = useState("cc=2,cod=1");
  const [allowAdvanced, setAllowAdvanced] = useState(false);

  // Active Ensemble Session Inspector state
  const [inspectSession, setInspectSession] = useState("");
  const [forceSynthesize, setForceSynthesize] = useState(false);
  const [synthesisOutput, setSynthesisOutput] =
    useState<EnsembleSynthesizeResponse | null>(null);

  // Catalog filter state
  const [modeTier, setModeTier] = useState<string>("core");
  const [modeCategory, setModeCategory] = useState<string>("");
  const [modeSearch, setModeSearch] = useState<string>("");

  const [notice, setNotice] = useState<{
    kind: "success" | "error";
    text: string;
  } | null>(null);

  const modesPath = useMemo(() => {
    const params = new URLSearchParams();
    params.set("limit", "100");
    if (modeTier) params.set("tier", modeTier);
    if (modeCategory) params.set("category", modeCategory);
    if (modeSearch.trim()) params.set("search", modeSearch.trim());
    return `/api/v1/ensemble/modes?${params.toString()}`;
  }, [modeTier, modeCategory, modeSearch]);

  const modesQuery = useQuery({
    queryKey: ["ensemble", "modes", modesPath],
    queryFn: () => apiFetch<EnsembleModesResponse>(modesPath),
  });

  const presetsQuery = useQuery({
    queryKey: ["ensemble", "presets"],
    queryFn: () => apiFetch<EnsemblePresetsResponse>("/api/v1/ensemble/presets"),
  });

  const sessionsQuery = useQuery({
    queryKey: ["sessions", "list-for-ensemble"],
    queryFn: () => apiFetch<SessionsListResponse>("/api/v1/sessions"),
    refetchInterval: 15_000,
  });

  const trimmedInspectSession = inspectSession.trim();
  const ensembleSessionQuery = useQuery({
    queryKey: ["ensemble", "session", trimmedInspectSession],
    queryFn: () =>
      apiFetch<EnsembleSessionResponse>(
        `/api/v1/ensemble/sessions/${encodeURIComponent(trimmedInspectSession)}`
      ),
    enabled: trimmedInspectSession.length > 0,
    retry: false,
  });

  const suggestMutation = useMutation({
    mutationFn: (body: EnsembleSuggestRequest) =>
      apiFetch<EnsembleSuggestResponse>("/api/v1/ensemble/suggest", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: (res) => {
      if (res.top_pick?.preset_name) {
        setSelectedPreset(res.top_pick.preset_name);
      }
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Recommendation failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const spawnMutation = useMutation({
    mutationFn: (body: EnsembleSpawnRequest) =>
      apiFetch<EnsembleSpawnResponse>("/api/v1/ensemble/spawn", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: (res, vars) => {
      setInspectSession(res.session || vars.session);
      setNotice({
        kind: "success",
        text: `Spawned ensemble session "${res.session || vars.session}" with preset "${vars.preset || "custom"}".`,
      });
      void queryClient.invalidateQueries({ queryKey: ["ensemble"] });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Ensemble spawn failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const synthesizeMutation = useMutation({
    mutationFn: ({
      session,
      body,
    }: {
      session: string;
      body: EnsembleSynthesizeRequest;
    }) =>
      apiFetch<EnsembleSynthesizeResponse>(
        `/api/v1/ensemble/sessions/${encodeURIComponent(session)}/synthesize`,
        {
          method: "POST",
          body: JSON.stringify(body),
        }
      ),
    onSuccess: (res) => {
      setSynthesisOutput(res);
      setNotice({
        kind: "success",
        text: `Synthesized ensemble session "${res.session}" (${res.status}).`,
      });
      void queryClient.invalidateQueries({
        queryKey: ["ensemble", "session", res.session],
      });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Synthesis failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const stopMutation = useMutation({
    mutationFn: (session: string) =>
      apiFetch<EnsembleStopResponse>(
        `/api/v1/ensemble/sessions/${encodeURIComponent(session)}/stop`,
        {
          method: "POST",
          body: JSON.stringify({}),
        }
      ),
    onSuccess: (_res, session) => {
      setNotice({
        kind: "success",
        text: `Stopped ensemble session "${session}".`,
      });
      void queryClient.invalidateQueries({ queryKey: ["ensemble"] });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (err) => {
      setNotice({
        kind: "error",
        text: `Stop failed: ${formatApiErrorMessage(err)}`,
      });
    },
  });

  const presets = presetsQuery.data?.presets ?? [];
  const modes = modesQuery.data?.modes ?? [];
  const categories = modesQuery.data?.categories ?? [];
  const suggestions = suggestMutation.data?.suggestions ?? [];
  const activeSessions = sessionsQuery.data?.sessions ?? [];
  const inspected = ensembleSessionQuery.data?.ensemble;
  const inspectedSummary = ensembleSessionQuery.data?.summary;

  const handleSuggest = () => {
    const q = question.trim();
    if (!q) {
      setNotice({
        kind: "error",
        text: "Enter an analysis question to get preset recommendations.",
      });
      return;
    }
    suggestMutation.mutate({ question: q });
  };

  const handleSpawn = () => {
    const sess = spawnSession.trim();
    const q = question.trim();
    if (!sess || !q) {
      setNotice({
        kind: "error",
        text: "Both a session name and a question are required to spawn an ensemble.",
      });
      return;
    }
    spawnMutation.mutate({
      session: sess,
      preset: selectedPreset,
      question: q,
      agents: agentMix.trim() || undefined,
      allow_advanced: allowAdvanced,
    });
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-white">
            Ensemble
          </h1>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            Multi-agent reasoning studio: question-to-preset recommender,
            cognitive mode catalog, and live synthesis.
          </p>
        </div>
      </div>

      {/* Feedback Banner */}
      {notice && (
        <div
          role={notice.kind === "error" ? "alert" : "status"}
          className={`rounded-lg border px-4 py-3 text-sm flex items-center justify-between gap-3 ${
            notice.kind === "error"
              ? "bg-red-50 border-red-200 text-red-800 dark:bg-red-900/20 dark:border-red-800 dark:text-red-200"
              : "bg-emerald-50 border-emerald-200 text-emerald-800 dark:bg-emerald-900/20 dark:border-emerald-800 dark:text-emerald-200"
          }`}
        >
          <span>{notice.text}</span>
          <button
            type="button"
            onClick={() => setNotice(null)}
            className="text-xs underline opacity-80 hover:opacity-100"
          >
            Dismiss
          </button>
        </div>
      )}

      {/* Catalog KPI Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Total Cognitive Modes
          </div>
          <div className="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {modesQuery.data?.total_modes ?? 0}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Catalog {modesQuery.data?.catalog_version || "v1"}
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Core Reasoning Modes
          </div>
          <div className="mt-2 text-2xl font-bold text-blue-600 dark:text-blue-400">
            {modesQuery.data?.core_modes ?? 0}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Default high-orthogonality operators
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Advanced &amp; Experimental
          </div>
          <div className="mt-2 text-2xl font-bold text-purple-600 dark:text-purple-400">
            {(modesQuery.data?.advanced_modes ?? 0) +
              (modesQuery.data?.experimental_modes ?? 0)}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Domain-specialized reasoning lenses
          </div>
        </div>

        <div className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">
            Ensemble Presets
          </div>
          <div className="mt-2 text-2xl font-bold text-emerald-600 dark:text-emerald-400">
            {presetsQuery.data?.count ?? presets.length}
          </div>
          <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
            Curated multi-perspective bundles
          </div>
        </div>
      </div>

      {/* Question-to-Preset Recommender & Spawn Launcher */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4">
          <div>
            <h2 className="text-base font-semibold text-gray-900 dark:text-white">
              Question Recommender &amp; Launcher
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Describe your problem to rank presets, then spawn a multi-agent
              reasoning session.
            </p>
          </div>

          <div className="space-y-3">
            <div>
              <label
                htmlFor="ensemble-question-input"
                className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
              >
                Analysis Question
              </label>
              <textarea
                id="ensemble-question-input"
                rows={3}
                value={question}
                onChange={(e) => setQuestion(e.target.value)}
                placeholder="e.g., What concurrency hazards or architectural bottlenecks exist in internal/serve?"
                className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm text-gray-900 dark:text-white"
              />
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <label
                  htmlFor="ensemble-session-input"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Session Name
                </label>
                <input
                  id="ensemble-session-input"
                  type="text"
                  value={spawnSession}
                  onChange={(e) => setSpawnSession(e.target.value)}
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
                />
              </div>

              <div>
                <label
                  htmlFor="ensemble-preset-select"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Preset
                </label>
                <select
                  id="ensemble-preset-select"
                  value={selectedPreset}
                  onChange={(e) => setSelectedPreset(e.target.value)}
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
                >
                  {presets.length === 0 ? (
                    <option value={selectedPreset}>{selectedPreset}</option>
                  ) : (
                    presets.map((p) => (
                      <option key={p.name} value={p.name}>
                        {p.display || p.name} ({p.mode_count} modes)
                      </option>
                    ))
                  )}
                </select>
              </div>

              <div>
                <label
                  htmlFor="ensemble-agents-input"
                  className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
                >
                  Agent Mix
                </label>
                <input
                  id="ensemble-agents-input"
                  type="text"
                  value={agentMix}
                  onChange={(e) => setAgentMix(e.target.value)}
                  placeholder="cc=2,cod=1"
                  className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white font-mono"
                />
              </div>
            </div>

            <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
              <label className="inline-flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
                <input
                  type="checkbox"
                  checked={allowAdvanced}
                  onChange={(e) => setAllowAdvanced(e.target.checked)}
                  className="rounded border-gray-300 dark:border-gray-600"
                />
                Allow advanced/experimental modes
              </label>

              <div className="flex items-center gap-2">
                <button
                  type="button"
                  disabled={suggestMutation.isPending}
                  onClick={handleSuggest}
                  className="px-3 py-1.5 text-xs font-medium rounded-md border border-blue-300 dark:border-blue-700 text-blue-700 dark:text-blue-300 hover:bg-blue-50 dark:hover:bg-blue-900/30 disabled:opacity-50"
                >
                  {suggestMutation.isPending
                    ? "Ranking..."
                    : "Recommend Preset"}
                </button>
                <button
                  type="button"
                  disabled={spawnMutation.isPending}
                  onClick={handleSpawn}
                  className="px-3 py-1.5 text-xs font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50"
                >
                  {spawnMutation.isPending ? "Spawning..." : "Spawn Ensemble"}
                </button>
              </div>
            </div>
          </div>

          {suggestions.length > 0 && (
            <div className="pt-3 border-t border-gray-200 dark:border-gray-700 space-y-2">
              <div className="text-xs font-semibold text-gray-700 dark:text-gray-300">
                Recommended Presets
              </div>
              {suggestions.slice(0, 3).map((sug, idx) => (
                <div
                  key={sug.preset_name}
                  className="rounded border border-gray-200 dark:border-gray-700 p-2.5 flex items-start justify-between gap-2"
                >
                  <div className="space-y-0.5 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-semibold text-gray-900 dark:text-white">
                        {sug.display_name || sug.preset_name}
                      </span>
                      {idx === 0 && (
                        <span className="px-1.5 py-0.5 text-[11px] rounded bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200">
                          Top Pick
                        </span>
                      )}
                      <span className="text-[11px] text-gray-500 font-mono">
                        score {sug.score.toFixed(2)} &middot; {sug.mode_count}{" "}
                        modes
                      </span>
                    </div>
                    <p className="text-xs text-gray-600 dark:text-gray-400">
                      {sug.description}
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={() => setSelectedPreset(sug.preset_name)}
                    className="px-2 py-1 text-xs rounded border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-200 hover:bg-gray-100 dark:hover:bg-gray-700 shrink-0"
                  >
                    Use
                  </button>
                </div>
              ))}
            </div>
          )}
        </section>

        {/* Live Session Inspector & Synthesis */}
        <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4">
          <div>
            <h2 className="text-base font-semibold text-gray-900 dark:text-white">
              Live Session Inspector &amp; Synthesis
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Monitor mode completion, redundancy metrics, and synthesize
              findings across panes.
            </p>
          </div>

          <div className="flex flex-col sm:flex-row gap-2">
            <input
              aria-label="Inspect ensemble session"
              type="text"
              value={inspectSession}
              onChange={(e) => setInspectSession(e.target.value)}
              placeholder="Enter ensemble session name..."
              list="ensemble-active-sessions"
              className="flex-1 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-sm text-gray-900 dark:text-white"
            />
            <datalist id="ensemble-active-sessions">
              {activeSessions.map((s) => (
                <option key={s.name} value={s.name} />
              ))}
            </datalist>
            <button
              type="button"
              disabled={!trimmedInspectSession}
              onClick={() => void ensembleSessionQuery.refetch()}
              className="px-3 py-1.5 text-xs font-medium rounded-md border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
            >
              Inspect
            </button>
          </div>

          {ensembleSessionQuery.isError && (
            <div className="rounded border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-900/20 p-3 text-xs text-amber-800 dark:text-amber-200">
              {formatApiErrorMessage(ensembleSessionQuery.error)}
            </div>
          )}

          {inspected ? (
            <div className="space-y-3">
              <div className="rounded border border-gray-200 dark:border-gray-700 p-3 space-y-1">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-sm font-semibold text-gray-900 dark:text-white font-mono">
                    {inspected.session}
                  </span>
                  <span className="px-2 py-0.5 text-xs rounded bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-200">
                    {inspected.status || "active"}
                  </span>
                </div>
                {inspected.question && (
                  <p className="text-xs text-gray-700 dark:text-gray-300">
                    {inspected.question}
                  </p>
                )}
                <div className="text-xs text-gray-500 dark:text-gray-400">
                  Completed {inspectedSummary?.completed ?? 0}/
                  {inspectedSummary?.total_modes ?? inspected.modes.length}{" "}
                  &middot; Synthesis strategy:{" "}
                  {inspected.synthesis?.strategy || "default"}
                </div>
              </div>

              <div className="space-y-1.5 max-h-44 overflow-y-auto">
                {inspected.modes.map((m) => (
                  <div
                    key={`${m.id}-${m.pane || ""}`}
                    className="flex items-center justify-between text-xs rounded border border-gray-100 dark:border-gray-700 px-2.5 py-1.5"
                  >
                    <span className="font-medium text-gray-800 dark:text-gray-200">
                      {m.code ? `[${m.code}] ` : ""}
                      {m.name || m.id}
                    </span>
                    <span className="font-mono text-gray-500 dark:text-gray-400">
                      {m.agent_type || "agent"} &middot; {m.status || "pending"}
                    </span>
                  </div>
                ))}
              </div>

              <div className="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-gray-200 dark:border-gray-700">
                <label className="inline-flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
                  <input
                    type="checkbox"
                    checked={forceSynthesize}
                    onChange={(e) => setForceSynthesize(e.target.checked)}
                    className="rounded border-gray-300 dark:border-gray-600"
                  />
                  Force partial synthesis
                </label>

                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    disabled={synthesizeMutation.isPending}
                    onClick={() =>
                      synthesizeMutation.mutate({
                        session: inspected.session,
                        body: { force: forceSynthesize },
                      })
                    }
                    className="px-3 py-1.5 text-xs font-medium rounded-md bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50"
                  >
                    {synthesizeMutation.isPending
                      ? "Synthesizing..."
                      : "Synthesize"}
                  </button>
                  <button
                    type="button"
                    disabled={stopMutation.isPending}
                    onClick={() => stopMutation.mutate(inspected.session)}
                    className="px-3 py-1.5 text-xs font-medium rounded-md border border-red-300 dark:border-red-700 text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/30 disabled:opacity-50"
                  >
                    Stop Ensemble
                  </button>
                </div>
              </div>
            </div>
          ) : (
            <div className="rounded border border-dashed border-gray-200 dark:border-gray-700 p-6 text-center text-xs text-gray-500 dark:text-gray-400">
              Select or spawn an ensemble session above to inspect mode progress
              and trigger synthesis.
            </div>
          )}

          {synthesisOutput?.formatted_output && (
            <div className="pt-2 space-y-1">
              <div className="text-xs font-semibold text-gray-700 dark:text-gray-300">
                Synthesis Output
              </div>
              <pre className="rounded bg-gray-900 text-gray-100 p-3 text-xs overflow-x-auto max-h-60">
                {synthesisOutput.formatted_output}
              </pre>
            </div>
          )}
        </section>
      </div>

      {/* Curated Presets Grid */}
      <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold text-gray-900 dark:text-white">
            Curated Ensemble Presets
          </h2>
          <span className="text-xs text-gray-500 dark:text-gray-400">
            {presets.length} available
          </span>
        </div>

        {presetsQuery.isLoading ? (
          <div className="text-sm text-gray-500 dark:text-gray-400 py-4">
            Loading ensemble presets...
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {presets.map((preset) => (
              <div
                key={preset.name}
                className="rounded-lg border border-gray-200 dark:border-gray-700 p-3.5 flex flex-col justify-between gap-3"
              >
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm font-semibold text-gray-900 dark:text-white">
                      {preset.display || preset.name}
                    </span>
                    <span className="text-xs font-mono text-gray-500 dark:text-gray-400">
                      {preset.mode_count} modes
                    </span>
                  </div>
                  <p className="text-xs text-gray-600 dark:text-gray-400">
                    {preset.description}
                  </p>
                  <div className="flex flex-wrap gap-1 pt-1">
                    {preset.modes.slice(0, 6).map((code) => (
                      <span
                        key={`${preset.name}-${code}`}
                        className="px-1.5 py-0.5 text-[11px] font-mono rounded bg-gray-100 dark:bg-gray-700 text-gray-700 dark:text-gray-200"
                      >
                        {code}
                      </span>
                    ))}
                  </div>
                </div>

                <div className="flex items-center justify-between text-xs pt-2 border-t border-gray-100 dark:border-gray-700/70">
                  <span className="text-gray-500 dark:text-gray-400">
                    Strategy: {preset.synthesis.strategy}
                  </span>
                  <button
                    type="button"
                    onClick={() => setSelectedPreset(preset.name)}
                    className="font-medium text-blue-600 dark:text-blue-400 hover:underline"
                  >
                    Select {preset.name}
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Cognitive Mode Browser */}
      <section className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-4">
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-gray-900 dark:text-white">
              Cognitive Mode Catalog
            </h2>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Browse reasoning operators by tier, category, or keyword.
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <select
              aria-label="Filter modes by tier"
              value={modeTier}
              onChange={(e) => setModeTier(e.target.value)}
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            >
              <option value="core">Core tier</option>
              <option value="advanced">Advanced tier</option>
              <option value="experimental">Experimental tier</option>
              <option value="all">All tiers</option>
            </select>

            <select
              aria-label="Filter modes by category"
              value={modeCategory}
              onChange={(e) => setModeCategory(e.target.value)}
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            >
              <option value="">All categories</option>
              {categories.map((cat) => (
                <option key={cat.code} value={cat.code}>
                  [{cat.code}] {cat.name} ({cat.mode_count})
                </option>
              ))}
            </select>

            <input
              aria-label="Search cognitive modes"
              type="text"
              value={modeSearch}
              onChange={(e) => setModeSearch(e.target.value)}
              placeholder="Search modes..."
              className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            />
          </div>
        </div>

        {modesQuery.isLoading ? (
          <div className="text-sm text-gray-500 dark:text-gray-400 py-4">
            Loading cognitive modes...
          </div>
        ) : modes.length === 0 ? (
          <div className="text-sm text-gray-500 dark:text-gray-400 py-4">
            No cognitive modes match the current filters.
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {modes.map((mode) => (
              <div
                key={mode.id}
                className="rounded border border-gray-200 dark:border-gray-700 p-3 space-y-1"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-semibold text-gray-900 dark:text-white">
                    <span className="font-mono text-blue-600 dark:text-blue-400 mr-1.5">
                      {mode.code}
                    </span>
                    {mode.name}
                  </span>
                  <span className="px-1.5 py-0.5 text-[10px] uppercase rounded bg-gray-100 dark:bg-gray-700 text-gray-600 dark:text-gray-300">
                    {mode.tier}
                  </span>
                </div>
                <p className="text-xs text-gray-600 dark:text-gray-400">
                  {mode.short_desc}
                </p>
                <div className="text-[11px] text-gray-500 dark:text-gray-400">
                  Category: {mode.category.name}
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
