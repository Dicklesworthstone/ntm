"use client";

/**
 * PersonasAndRecipesPanel
 *
 * Surfaces the 11-harness Agent Personas catalog, Profile Sets, and Spawn Recipes
 * on /agents:
 *   - GET /api/v1/personas?agent=...&tag=...
 *   - GET /api/v1/recipes
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

const PERSONA_HARNESS_FILTERS = [
  { value: "", label: "All 12 Harnesses" },
  { value: "cc", label: "Claude (cc)" },
  { value: "cod", label: "Codex (cod)" },
  { value: "gmi", label: "Gemini (gmi)" },
  { value: "agy", label: "Antigravity (agy)" },
  { value: "grok", label: "Grok (grok)" },
  { value: "cursor", label: "Cursor" },
  { value: "windsurf", label: "Windsurf" },
  { value: "aider", label: "Aider" },
  { value: "oc", label: "OpenCode (oc)" },
  { value: "omp", label: "Oh My Pi (omp)" },
  { value: "pi", label: "Pi Agent (pi)" },
  { value: "ollama", label: "Ollama" },
] as const;

export interface PersonasCatalogResponse extends ApiEnvelope {
  count: number;
  builtin_count: number;
  personas: Array<{
    name: string;
    agent_type: string;
    model?: string;
    description?: string;
    tags?: string[];
  }>;
  profile_sets: Array<{
    name: string;
    description?: string;
    personas?: string[];
  }>;
}

export interface RecipesCatalogResponse extends ApiEnvelope {
  count: number;
  recipes: Array<{
    name: string;
    description?: string;
    source?: string;
    total_agents: number;
    agents: Array<{
      type: string;
      count: number;
      model?: string;
      persona?: string;
    }>;
  }>;
}

export function PersonasAndRecipesPanel({
  onSelectRecipePreset,
}: {
  onSelectRecipePreset?: (recipeName: string) => void;
}) {
  const [agentFilter, setAgentFilter] = useState("");
  const [tagFilter, setTagFilter] = useState("");

  const personasQuery = useQuery({
    queryKey: ["personas", agentFilter, tagFilter],
    queryFn: () => {
      const params = new URLSearchParams();
      if (agentFilter) params.set("agent", agentFilter);
      if (tagFilter.trim()) params.set("tag", tagFilter.trim());
      const qs = params.toString();
      return apiFetch<PersonasCatalogResponse>(
        qs ? `/api/v1/personas?${qs}` : "/api/v1/personas"
      );
    },
    retry: false,
  });

  const recipesQuery = useQuery({
    queryKey: ["recipes"],
    queryFn: () => apiFetch<RecipesCatalogResponse>("/api/v1/recipes"),
    retry: false,
  });

  const personas = personasQuery.data?.personas ?? [];
  const profileSets = personasQuery.data?.profile_sets ?? [];
  const recipes = recipesQuery.data?.recipes ?? [];

  return (
    <section
      aria-label="Personas and Spawn Recipes Catalog"
      className="rounded-lg border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800 space-y-5"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            12-Harness Agent Personas &amp; Spawn Recipes
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Browse built-in, user, and project personas, profile sets, and multi-agent spawn recipes.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <select
            aria-label="Filter Personas by Harness"
            value={agentFilter}
            onChange={(e) => setAgentFilter(e.target.value)}
            className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
          >
            {PERSONA_HARNESS_FILTERS.map((opt) => (
              <option key={opt.value || "all"} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
          <input
            aria-label="Filter Personas by Tag"
            type="text"
            value={tagFilter}
            onChange={(e) => setTagFilter(e.target.value)}
            placeholder="Filter tag (e.g. review)"
            className="rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
          />
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {/* Personas & Profile Sets */}
        <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-gray-900 dark:text-white">
              Personas ({personas.length})
            </span>
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {profileSets.length} profile set(s)
            </span>
          </div>
          {personasQuery.isError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {formatApiErrorMessage(personasQuery.error)}
            </div>
          )}
          <div className="max-h-60 overflow-y-auto space-y-2">
            {personas.map((p) => (
              <div
                key={p.name}
                className="rounded border border-gray-100 dark:border-gray-700/70 bg-gray-50/70 dark:bg-gray-900/40 p-2.5 text-xs space-y-1"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold text-gray-900 dark:text-white">
                    {p.name}
                  </span>
                  <span className="rounded bg-blue-100 dark:bg-blue-900/40 px-1.5 py-0.5 font-mono text-[11px] text-blue-700 dark:text-blue-300">
                    {p.agent_type}
                  </span>
                </div>
                {p.description && (
                  <div className="text-gray-600 dark:text-gray-400">
                    {p.description}
                  </div>
                )}
              </div>
            ))}
            {!personasQuery.isLoading && personas.length === 0 && (
              <div className="text-xs text-gray-500">
                No personas match the current filter.
              </div>
            )}
          </div>
        </div>

        {/* Spawn Recipes */}
        <div className="rounded-md border border-gray-200 dark:border-gray-700 p-4 space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-gray-900 dark:text-white">
              Spawn Recipes ({recipes.length})
            </span>
          </div>
          {recipesQuery.isError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {formatApiErrorMessage(recipesQuery.error)}
            </div>
          )}
          <div className="max-h-60 overflow-y-auto space-y-2">
            {recipes.map((r) => (
              <div
                key={r.name}
                className="rounded border border-gray-100 dark:border-gray-700/70 bg-gray-50/70 dark:bg-gray-900/40 p-2.5 text-xs space-y-1"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold text-gray-900 dark:text-white">
                    {r.name} ({r.total_agents} agents)
                  </span>
                  {onSelectRecipePreset && (
                    <button
                      type="button"
                      onClick={() => onSelectRecipePreset(r.name)}
                      className="rounded border border-blue-300 dark:border-blue-700 px-2 py-0.5 text-[11px] text-blue-700 dark:text-blue-300 hover:bg-blue-50 dark:hover:bg-blue-900/20"
                    >
                      Use Recipe
                    </button>
                  )}
                </div>
                {r.description && (
                  <div className="text-gray-600 dark:text-gray-400">
                    {r.description}
                  </div>
                )}
              </div>
            ))}
            {!recipesQuery.isLoading && recipes.length === 0 && (
              <div className="text-xs text-gray-500">
                No spawn recipes configured.
              </div>
            )}
          </div>
        </div>
      </div>
    </section>
  );
}
