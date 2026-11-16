"use client";

/**
 * MailContactsPanel
 *
 * Surfaces Agent Mail server health/projects and cross-agent contact handshakes:
 *   - GET  /api/v1/mail/health & GET /api/v1/mail/projects (on-demand)
 *   - GET  /api/v1/mail/contacts?agent_name=... (on-demand)
 *   - PUT  /api/v1/mail/contacts/policy (open | auto | contacts_only | block_all)
 *   - POST /api/v1/mail/contacts/request (cross-agent / cross-project handshake)
 *   - POST /api/v1/mail/contacts/respond (accept or reject contact request)
 */

import { useState, type FormEvent } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  apiFetch,
  formatApiErrorMessage,
  type ApiEnvelope,
} from "@/lib/api/client";

export interface MailContactEntry {
  to_agent?: string;
  agent_name?: string;
  from_agent?: string;
  status?: string;
  reason?: string;
  expires_ts?: string;
}

interface MailContactsListResponse extends ApiEnvelope {
  contacts: MailContactEntry[];
  count: number;
}

interface MailHealthResponse extends ApiEnvelope {
  available?: boolean;
  healthy?: boolean;
  status?: string;
  server_url?: string;
}

interface MailProjectRecord {
  id?: number;
  slug?: string;
  human_key?: string;
}

interface MailProjectsResponse extends ApiEnvelope {
  projects?: MailProjectRecord[];
  count?: number;
}

interface ContactPolicyUpdateResponse extends ApiEnvelope {
  agent_name: string;
  policy: string;
}

interface ContactRequestResponse extends ApiEnvelope {
  from_agent: string;
  to_agent: string;
  status: string;
}

interface ContactRespondResponse extends ApiEnvelope {
  to_agent: string;
  from_agent: string;
  accepted: boolean;
  status?: string;
}

export function MailContactsPanel({
  selectedAgentName,
}: {
  selectedAgentName: string;
}) {
  const [contactsRequested, setContactsRequested] = useState(false);
  const [healthRequested, setHealthRequested] = useState(false);

  // Contact Policy state
  const [policyValue, setPolicyValue] = useState<
    "open" | "auto" | "contacts_only" | "block_all"
  >("auto");
  const [policyConfirmation, setPolicyConfirmation] = useState<string | null>(
    null
  );
  const [policyError, setPolicyError] = useState<string | null>(null);

  // Request Contact state
  const [targetAgent, setTargetAgent] = useState("");
  const [targetProject, setTargetProject] = useState("");
  const [requestReason, setRequestReason] = useState("");
  const [requestStatusMsg, setRequestStatusMsg] = useState<string | null>(null);
  const [requestError, setRequestError] = useState<string | null>(null);

  // Respond Contact state
  const [requesterAgent, setRequesterAgent] = useState("");
  const [respondStatusMsg, setRespondStatusMsg] = useState<string | null>(null);
  const [respondError, setRespondError] = useState<string | null>(null);

  const contactsQuery = useQuery({
    queryKey: ["mail", "contacts", selectedAgentName],
    queryFn: () =>
      apiFetch<MailContactsListResponse>(
        `/api/v1/mail/contacts?agent_name=${encodeURIComponent(selectedAgentName)}`
      ),
    enabled: contactsRequested && Boolean(selectedAgentName),
    retry: false,
  });

  const healthQuery = useQuery({
    queryKey: ["mail", "health"],
    queryFn: () => apiFetch<MailHealthResponse>("/api/v1/mail/health"),
    enabled: healthRequested,
    retry: false,
  });

  const projectsQuery = useQuery({
    queryKey: ["mail", "projects"],
    queryFn: () => apiFetch<MailProjectsResponse>("/api/v1/mail/projects"),
    enabled: healthRequested,
    retry: false,
  });

  const setPolicyMutation = useMutation({
    mutationFn: () =>
      apiFetch<ContactPolicyUpdateResponse>("/api/v1/mail/contacts/policy", {
        method: "PUT",
        body: JSON.stringify({
          agent_name: selectedAgentName,
          policy: policyValue,
        }),
      }),
    onSuccess: (res) => {
      setPolicyConfirmation(
        `Updated contact policy for ${res.agent_name} to "${res.policy}".`
      );
      setPolicyError(null);
    },
    onError: (err) => {
      setPolicyError(formatApiErrorMessage(err));
    },
  });

  const requestContactMutation = useMutation({
    mutationFn: () =>
      apiFetch<ContactRequestResponse>("/api/v1/mail/contacts/request", {
        method: "POST",
        body: JSON.stringify({
          from_agent: selectedAgentName,
          to_agent: targetAgent.trim(),
          ...(targetProject.trim() ? { to_project: targetProject.trim() } : {}),
          ...(requestReason.trim() ? { reason: requestReason.trim() } : {}),
        }),
      }),
    onSuccess: (res) => {
      setRequestStatusMsg(
        `Contact handshake ${res.from_agent} → ${res.to_agent}: ${res.status || "requested"}.`
      );
      setRequestError(null);
      setTargetAgent("");
      setRequestReason("");
      if (contactsRequested) {
        void contactsQuery.refetch();
      }
    },
    onError: (err) => {
      setRequestError(formatApiErrorMessage(err));
    },
  });

  const respondContactMutation = useMutation({
    mutationFn: (accept: boolean) =>
      apiFetch<ContactRespondResponse>("/api/v1/mail/contacts/respond", {
        method: "POST",
        body: JSON.stringify({
          to_agent: selectedAgentName,
          from_agent: requesterAgent.trim(),
          accept,
        }),
      }),
    onSuccess: (res) => {
      setRespondStatusMsg(
        `${res.accepted ? "Accepted" : "Rejected"} contact request from ${res.from_agent}.`
      );
      setRespondError(null);
      setRequesterAgent("");
      if (contactsRequested) {
        void contactsQuery.refetch();
      }
    },
    onError: (err) => {
      setRespondError(formatApiErrorMessage(err));
    },
  });

  const handlePolicySubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!selectedAgentName) {
      setPolicyError("Select an active agent first.");
      return;
    }
    setPolicyMutation.mutate();
  };

  const handleRequestSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!selectedAgentName) {
      setRequestError("Select an active agent first.");
      return;
    }
    if (!targetAgent.trim()) {
      setRequestError("Target agent name is required.");
      return;
    }
    requestContactMutation.mutate();
  };

  const contacts = contactsQuery.data?.contacts ?? [];
  const projects = projectsQuery.data?.projects ?? [];

  return (
    <section
      aria-label="Agent Contacts & Handshake Policy"
      className="rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800 p-5 space-y-5"
    >
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">
            Agent Contacts & Handshake Policy
          </h2>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            Manage cross-agent contact links, contact policy (<code className="font-mono">/api/v1/mail/contacts/*</code>), and Agent Mail server projects.
          </p>
        </div>

        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            disabled={!selectedAgentName || contactsQuery.isFetching}
            onClick={() => {
              if (contactsRequested) {
                void contactsQuery.refetch();
              } else {
                setContactsRequested(true);
              }
            }}
            className="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-1.5 text-xs font-medium text-gray-700 dark:text-gray-200 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
          >
            {contactsQuery.isFetching
              ? "Loading Contacts…"
              : contactsRequested
                ? "Refresh Contacts"
                : "Load Contacts"}
          </button>

          <button
            type="button"
            disabled={healthQuery.isFetching || projectsQuery.isFetching}
            onClick={() => {
              if (healthRequested) {
                void healthQuery.refetch();
                void projectsQuery.refetch();
              } else {
                setHealthRequested(true);
              }
            }}
            className="rounded-md border border-blue-300 dark:border-blue-700 bg-blue-50 dark:bg-blue-900/20 px-3 py-1.5 text-xs font-medium text-blue-800 dark:text-blue-200 hover:bg-blue-100 dark:hover:bg-blue-900/40 disabled:opacity-50"
          >
            {healthQuery.isFetching
              ? "Checking…"
              : "Check Mail Health & Projects"}
          </button>
        </div>
      </div>

      {healthRequested && (healthQuery.data || projectsQuery.data) && (
        <div
          data-testid="mail-health-projects-banner"
          className="rounded-md border border-blue-200 dark:border-blue-800 bg-blue-50/70 dark:bg-blue-900/20 p-3 text-xs text-blue-900 dark:text-blue-200 flex flex-wrap items-center justify-between gap-2"
        >
          <span>
            Mail Server:{" "}
            <strong>
              {healthQuery.data?.status ||
                (healthQuery.data?.healthy !== false ? "healthy" : "degraded")}
            </strong>
            {healthQuery.data?.server_url
              ? ` (${healthQuery.data.server_url})`
              : ""}
          </span>
          <span>
            Registered Projects:{" "}
            <strong>{projectsQuery.data?.count ?? projects.length}</strong>
            {projects.length > 0
              ? ` (${projects
                  .slice(0, 4)
                  .map((p) => p.slug || p.human_key || String(p.id ?? ""))
                  .filter(Boolean)
                  .join(", ")})`
              : ""}
          </span>
        </div>
      )}

      {contactsRequested && (
        <div
          data-testid="mail-contacts-list"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/60 dark:bg-gray-900/30 p-3 text-xs space-y-2"
        >
          <div className="font-semibold text-gray-800 dark:text-gray-200">
            Contacts for <span className="font-mono">{selectedAgentName}</span> (
            {contacts.length})
          </div>
          {contactsQuery.isFetching && !contactsQuery.data ? (
            <div className="text-gray-500 dark:text-gray-400">
              Loading contacts…
            </div>
          ) : contactsQuery.error ? (
            <div className="text-red-600 dark:text-red-400">
              {formatApiErrorMessage(contactsQuery.error)}
            </div>
          ) : contacts.length === 0 ? (
            <div className="text-gray-500 dark:text-gray-400">
              No active contact links recorded for this agent.
            </div>
          ) : (
            <ul className="divide-y divide-gray-200 dark:divide-gray-700">
              {contacts.map((c, idx) => {
                const peer =
                  c.to_agent || c.agent_name || c.from_agent || `contact-${idx}`;
                return (
                  <li
                    key={`${idx}-${peer}`}
                    className="py-1.5 flex items-center justify-between gap-2"
                  >
                    <span className="font-mono font-medium text-gray-900 dark:text-white">
                      {peer}
                    </span>
                    <span className="text-gray-500 dark:text-gray-400">
                      {c.status || "linked"}
                      {c.reason ? ` · ${c.reason}` : ""}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-3">
        {/* 1. Contact Policy */}
        <form
          onSubmit={handlePolicySubmit}
          aria-label="Contact Policy Form"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-3"
        >
          <div className="text-sm font-semibold text-gray-900 dark:text-white">
            Contact Policy
          </div>
          <div>
            <label
              htmlFor="mail-contact-policy"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Inbound Policy ({selectedAgentName || "no agent selected"})
            </label>
            <select
              id="mail-contact-policy"
              value={policyValue}
              onChange={(e) =>
                setPolicyValue(
                  e.target.value as
                    | "open"
                    | "auto"
                    | "contacts_only"
                    | "block_all"
                )
              }
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            >
              <option value="open">open (accept all)</option>
              <option value="auto">auto (approve project peers)</option>
              <option value="contacts_only">contacts_only (strict)</option>
              <option value="block_all">block_all</option>
            </select>
          </div>
          <button
            type="submit"
            disabled={!selectedAgentName || setPolicyMutation.isPending}
            className="rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {setPolicyMutation.isPending
              ? "Saving…"
              : "Save Contact Policy"}
          </button>
          {policyError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {policyError}
            </div>
          )}
          {policyConfirmation && (
            <div
              data-testid="mail-contact-policy-confirmation"
              className="text-xs text-emerald-700 dark:text-emerald-300"
            >
              {policyConfirmation}
            </div>
          )}
        </form>

        {/* 2. Request Contact Handshake */}
        <form
          onSubmit={handleRequestSubmit}
          aria-label="Request Contact Form"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-2.5"
        >
          <div className="text-sm font-semibold text-gray-900 dark:text-white">
            Request Contact Handshake
          </div>
          <div>
            <label
              htmlFor="mail-contact-to-agent"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Target Agent
            </label>
            <input
              id="mail-contact-to-agent"
              value={targetAgent}
              onChange={(e) => setTargetAgent(e.target.value)}
              placeholder="GreenCastle"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            />
          </div>
          <div>
            <label
              htmlFor="mail-contact-to-project"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Target Project (optional)
            </label>
            <input
              id="mail-contact-to-project"
              value={targetProject}
              onChange={(e) => setTargetProject(e.target.value)}
              placeholder="/data/projects/other"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs font-mono text-gray-900 dark:text-white"
            />
          </div>
          <div>
            <label
              htmlFor="mail-contact-reason"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Reason (optional)
            </label>
            <input
              id="mail-contact-reason"
              value={requestReason}
              onChange={(e) => setRequestReason(e.target.value)}
              placeholder="Coordinate API schema sync"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            />
          </div>
          <button
            type="submit"
            disabled={!selectedAgentName || requestContactMutation.isPending}
            className="rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
          >
            {requestContactMutation.isPending
              ? "Requesting…"
              : "Send Contact Request"}
          </button>
          {requestError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {requestError}
            </div>
          )}
          {requestStatusMsg && (
            <div
              data-testid="mail-contact-request-confirmation"
              className="text-xs text-emerald-700 dark:text-emerald-300"
            >
              {requestStatusMsg}
            </div>
          )}
        </form>

        {/* 3. Respond to Contact Request */}
        <div
          role="region"
          aria-label="Respond to Contact Request"
          className="rounded-md border border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-900/30 p-4 space-y-2.5"
        >
          <div className="text-sm font-semibold text-gray-900 dark:text-white">
            Respond to Contact Request
          </div>
          <div>
            <label
              htmlFor="mail-contact-from-agent"
              className="block text-xs font-medium text-gray-700 dark:text-gray-300 mb-1"
            >
              Requester Agent
            </label>
            <input
              id="mail-contact-from-agent"
              value={requesterAgent}
              onChange={(e) => setRequesterAgent(e.target.value)}
              placeholder="RedStone"
              className="w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-2.5 py-1.5 text-xs text-gray-900 dark:text-white"
            />
          </div>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              disabled={
                !selectedAgentName ||
                !requesterAgent.trim() ||
                respondContactMutation.isPending
              }
              onClick={() => respondContactMutation.mutate(true)}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-700 disabled:opacity-50"
            >
              Accept Contact
            </button>
            <button
              type="button"
              disabled={
                !selectedAgentName ||
                !requesterAgent.trim() ||
                respondContactMutation.isPending
              }
              onClick={() => respondContactMutation.mutate(false)}
              className="rounded-md border border-red-300 dark:border-red-700 px-3 py-1.5 text-xs font-medium text-red-700 dark:text-red-300 hover:bg-red-50 dark:hover:bg-red-900/20 disabled:opacity-50"
            >
              Reject Contact
            </button>
          </div>
          {respondError && (
            <div className="text-xs text-red-600 dark:text-red-400">
              {respondError}
            </div>
          )}
          {respondStatusMsg && (
            <div
              data-testid="mail-contact-respond-confirmation"
              className="text-xs text-emerald-700 dark:text-emerald-300"
            >
              {respondStatusMsg}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
