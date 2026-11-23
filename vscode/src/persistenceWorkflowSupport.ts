import * as vscode from 'vscode';

export type SavedSessionsAction = 'list' | 'show' | 'save' | 'archive' | 'unarchive';

export interface SavedSessionsOptions {
    action?: SavedSessionsAction;
    name?: string;
    session?: string;
    archived?: boolean;
    overwrite?: boolean;
}

export interface SavedSessionEntry {
    name: string;
    saved_at: string;
    cwd: string;
    agents: number;
    git_branch?: string;
    file_path: string;
    file_size: number;
}

export interface SavedSessionDetailPane {
    title: string;
    index: number;
    agent_type: string;
    session_id?: string;
    session_provider?: string;
    session_freshness?: string;
}

export interface SavedSessionsResult {
    success: boolean;
    action: string;
    archived: boolean;
    count: number;
    sessions: SavedSessionEntry[];
    saved_as?: string;
    resumable_panes: number;
    agents?: Record<string, number>;
    panes?: SavedSessionDetailPane[];
}

export interface AgentMappingOptions {
    session: string;
    projectDir?: string;
}

export interface AgentMappingEntryItem {
    name: string;
    session: string;
    pane_index: number;
    pane_id: string;
    agent_type?: string;
    badge_label?: string;
    observation_state?: string;
}

export interface AgentMappingResult {
    success: boolean;
    session: string;
    count: number;
    entries: AgentMappingEntryItem[];
    discrepancy_count: number;
    badges_enabled: boolean;
    badges_published: number;
}

export interface WorkflowsOptions {
    name?: string;
    role?: string;
    projectDir?: string;
}

export interface WorkflowSummaryItem {
    name: string;
    description: string;
    source: string;
    coordination: string;
    agent_count: number;
    roles: string[];
}

export interface WorkflowsResult {
    success: boolean;
    action: string;
    total: number;
    workflows: WorkflowSummaryItem[];
    workflow?: {
        name: string;
        description: string;
        source: string;
        coordination: string;
        agent_count: number;
        roles: string[];
        validated_trigger_count: number;
    };
}

export interface SessionTemplatesResult {
    success: boolean;
    action: string;
    total: number;
    templates: Array<{
        name: string;
        description: string;
        source: string;
        agent_count: number;
    }>;
}

export interface SavedSessionsModeQuickPickOption extends vscode.QuickPickItem {
    mode: 'listActive' | 'listArchived' | 'saveCurrent' | 'showSnapshot' | 'openSessionsCockpit';
}

export interface WorkflowsModeQuickPickOption extends vscode.QuickPickItem {
    mode: 'listWorkflows' | 'inspectWorkflow' | 'listSessionTemplates' | 'openPipelinesCockpit';
}

export const SAVED_SESSIONS_MODE_OPTIONS: readonly SavedSessionsModeQuickPickOption[] = [
    {
        label: '$(list-unordered) List Active Saved Sessions',
        description: '--robot-saved-sessions',
        detail: 'List saved session snapshots with 12-harness pane counts and git context',
        mode: 'listActive',
    },
    {
        label: '$(archive) List Archived Snapshots',
        description: '--robot-saved-sessions --saved-archived',
        detail: 'Inspect archived saved session snapshots',
        mode: 'listArchived',
    },
    {
        label: '$(save) Save Active Tmux Session Snapshot',
        description: '--robot-saved-sessions --saved-action=save',
        detail: 'Capture topology and per-pane casr/native resume bindings for an active session',
        mode: 'saveCurrent',
    },
    {
        label: '$(eye) Inspect Snapshot & Resume Readiness',
        description: '--robot-saved-sessions --saved-action=show',
        detail: 'Inspect full 12-harness breakdown and resumable pane bindings for a saved snapshot',
        mode: 'showSnapshot',
    },
    {
        label: '$(globe) Open /sessions in Web Cockpit',
        description: 'Interactive Saved Sessions & Resume Panel',
        detail: 'Manage saved and archived snapshots in the NTM Web Cockpit',
        mode: 'openSessionsCockpit',
    },
];

export const WORKFLOWS_MODE_OPTIONS: readonly WorkflowsModeQuickPickOption[] = [
    {
        label: '$(symbol-event) List Multi-Agent Workflow Templates',
        description: '--robot-workflows',
        detail: 'Browse ping-pong, pipeline, parallel, and review-gate workflow templates',
        mode: 'listWorkflows',
    },
    {
        label: '$(search) Inspect Workflow & Validate Triggers',
        description: '--robot-workflows --workflows-name=<name>',
        detail: 'Inspect roles, stages, and runtime trigger diagnostics for one workflow',
        mode: 'inspectWorkflow',
    },
    {
        label: '$(layers) List Multi-Agent Session Templates',
        description: '--robot-session-templates',
        detail: 'List built-in, user, and project session configuration templates',
        mode: 'listSessionTemplates',
    },
    {
        label: '$(globe) Open /pipelines in Web Cockpit',
        description: 'Workflows, Sequences & Session Templates',
        detail: 'Open the Pipelines & Workflows cockpit page in browser',
        mode: 'openPipelinesCockpit',
    },
];

function asRecord(value: unknown): Record<string, unknown> | undefined {
    if (value && typeof value === 'object' && !Array.isArray(value)) {
        return value as Record<string, unknown>;
    }
    return undefined;
}

export function normalizeSavedSessionsResult(raw: unknown): SavedSessionsResult {
    const obj = asRecord(raw) ?? {};
    const rawSessions = Array.isArray(obj.sessions) ? obj.sessions : [];
    const sessions: SavedSessionEntry[] = rawSessions.map(item => {
        const s = asRecord(item) ?? {};
        return {
            name: typeof s.name === 'string' ? s.name : '',
            saved_at: typeof s.saved_at === 'string' ? s.saved_at : '',
            cwd: typeof s.cwd === 'string' ? s.cwd : '',
            agents: typeof s.agents === 'number' ? s.agents : 0,
            git_branch: typeof s.git_branch === 'string' ? s.git_branch : undefined,
            file_path: typeof s.file_path === 'string' ? s.file_path : '',
            file_size: typeof s.file_size === 'number' ? s.file_size : 0,
        };
    });

    const stateObj = asRecord(obj.state);
    const rawAgents = asRecord(stateObj?.agents);
    const agents: Record<string, number> | undefined = rawAgents
        ? Object.fromEntries(
              Object.entries(rawAgents).filter(([, v]) => typeof v === 'number') as Array<[string, number]>
          )
        : undefined;
    const rawPanes = Array.isArray(stateObj?.panes) ? stateObj.panes : undefined;
    const panes: SavedSessionDetailPane[] | undefined = rawPanes?.map(item => {
        const p = asRecord(item) ?? {};
        return {
            title: typeof p.title === 'string' ? p.title : '',
            index: typeof p.index === 'number' ? p.index : 0,
            agent_type: typeof p.agent_type === 'string' ? p.agent_type : '',
            session_id: typeof p.session_id === 'string' ? p.session_id : undefined,
            session_provider: typeof p.session_provider === 'string' ? p.session_provider : undefined,
            session_freshness: typeof p.session_freshness === 'string' ? p.session_freshness : undefined,
        };
    });

    return {
        success: obj.success !== false,
        action: typeof obj.action === 'string' ? obj.action : 'list',
        archived: obj.archived === true,
        count: typeof obj.count === 'number' ? obj.count : sessions.length,
        sessions,
        saved_as: typeof obj.saved_as === 'string' ? obj.saved_as : undefined,
        resumable_panes: typeof obj.resumable_panes === 'number' ? obj.resumable_panes : 0,
        agents,
        panes,
    };
}

export function normalizeAgentMappingResult(raw: unknown): AgentMappingResult {
    const obj = asRecord(raw) ?? {};
    const rawEntries = Array.isArray(obj.entries) ? obj.entries : [];
    const entries: AgentMappingEntryItem[] = rawEntries.map(item => {
        const e = asRecord(item) ?? {};
        const idObj = asRecord(e.identity);
        return {
            name: typeof e.name === 'string' ? e.name : '',
            session: typeof e.session === 'string' ? e.session : '',
            pane_index: typeof e.pane_index === 'number' ? e.pane_index : 0,
            pane_id: typeof e.pane_id === 'string' ? e.pane_id : '',
            agent_type: typeof e.agent_type === 'string' ? e.agent_type : undefined,
            badge_label: typeof idObj?.label === 'string' ? idObj.label : undefined,
            observation_state: typeof idObj?.observation_state === 'string' ? idObj.observation_state : undefined,
        };
    });
    const rawDisc = Array.isArray(obj.discrepancies) ? obj.discrepancies : [];
    const badgesObj = asRecord(obj.badges);

    return {
        success: obj.success !== false,
        session: typeof obj.session === 'string' ? obj.session : '',
        count: typeof obj.count === 'number' ? obj.count : entries.length,
        entries,
        discrepancy_count: rawDisc.length,
        badges_enabled: badgesObj?.enabled === true,
        badges_published: typeof badgesObj?.published === 'number' ? badgesObj.published : 0,
    };
}

export function normalizeWorkflowsResult(raw: unknown): WorkflowsResult {
    const obj = asRecord(raw) ?? {};
    const rawWorkflows = Array.isArray(obj.workflows) ? obj.workflows : [];
    const workflows: WorkflowSummaryItem[] = rawWorkflows.map(item => {
        const w = asRecord(item) ?? {};
        const roles = Array.isArray(w.roles)
            ? w.roles.filter((r): r is string => typeof r === 'string')
            : [];
        return {
            name: typeof w.name === 'string' ? w.name : '',
            description: typeof w.description === 'string' ? w.description : '',
            source: typeof w.source === 'string' ? w.source : 'builtin',
            coordination: typeof w.coordination === 'string' ? w.coordination : '',
            agent_count: typeof w.agent_count === 'number' ? w.agent_count : 0,
            roles,
        };
    });
    const wfObj = asRecord(obj.workflow);
    const triggers = Array.isArray(wfObj?.validated_triggers) ? wfObj.validated_triggers : [];

    return {
        success: obj.success !== false,
        action: typeof obj.action === 'string' ? obj.action : 'list',
        total: typeof obj.total === 'number' ? obj.total : workflows.length,
        workflows,
        workflow: wfObj
            ? {
                  name: typeof wfObj.name === 'string' ? wfObj.name : '',
                  description: typeof wfObj.description === 'string' ? wfObj.description : '',
                  source: typeof wfObj.source === 'string' ? wfObj.source : 'builtin',
                  coordination: typeof wfObj.coordination === 'string' ? wfObj.coordination : '',
                  agent_count: typeof wfObj.agent_count === 'number' ? wfObj.agent_count : 0,
                  roles: Array.isArray(wfObj.roles)
                      ? wfObj.roles.filter((r): r is string => typeof r === 'string')
                      : [],
                  validated_trigger_count: triggers.length,
              }
            : undefined,
    };
}

export function normalizeSessionTemplatesResult(raw: unknown): SessionTemplatesResult {
    const obj = asRecord(raw) ?? {};
    const rawTemplates = Array.isArray(obj.templates) ? obj.templates : [];
    return {
        success: obj.success !== false,
        action: typeof obj.action === 'string' ? obj.action : 'list',
        total: typeof obj.total === 'number' ? obj.total : rawTemplates.length,
        templates: rawTemplates.map(item => {
            const t = asRecord(item) ?? {};
            return {
                name: typeof t.name === 'string' ? t.name : '',
                description: typeof t.description === 'string' ? t.description : '',
                source: typeof t.source === 'string' ? t.source : 'builtin',
                agent_count: typeof t.agent_count === 'number' ? t.agent_count : 0,
            };
        }),
    };
}

export function formatSavedSessionsMarkdown(res: SavedSessionsResult): string {
    const lines: string[] = [
        `# NTM Saved Sessions (${res.action})`,
        '',
    ];
    if (res.action === 'show' || res.action === 'save') {
        lines.push(
            `- **Snapshot:** \`${res.saved_as || 'default'}\``,
            `- **Resumable Panes (casr/native):** ${res.resumable_panes}`
        );
        if (res.agents) {
            const counts = Object.entries(res.agents)
                .filter(([, count]) => count > 0)
                .map(([k, count]) => `${k}=${count}`)
                .join(', ');
            lines.push(`- **Harnesses:** ${counts || 'none'}`);
        }
        if (res.panes && res.panes.length > 0) {
            lines.push('', '## Captured Panes', '', '| Index | Title | Harness | Resume Binding |', '|---:|---|---|---|');
            for (const p of res.panes) {
                const binding = p.session_id
                    ? `\`${p.session_provider || 'provider'}:${p.session_id}\` (${p.session_freshness || 'unknown'})`
                    : 'none';
                lines.push(`| ${p.index} | \`${p.title}\` | \`${p.agent_type}\` | ${binding} |`);
            }
        }
    } else {
        lines.push(`- **Scope:** ${res.archived ? 'Archived' : 'Active'} (${res.count} snapshot(s))`);
        if (res.sessions.length > 0) {
            lines.push('', '## Snapshots', '', '| Name | Agents | Branch | Directory |', '|---|---:|---|---|');
            for (const s of res.sessions) {
                lines.push(`| \`${s.name}\` | ${s.agents} | \`${s.git_branch || '-'}\` | \`${s.cwd}\` |`);
            }
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatAgentMappingMarkdown(res: AgentMappingResult): string {
    const lines: string[] = [
        `# NTM Agent Mail <-> Tmux Pane Mapping (${res.session})`,
        '',
        `- **Mapped Panes:** ${res.count}`,
        `- **Discrepancies:** ${res.discrepancy_count}`,
        `- **Border Badges:** ${res.badges_enabled ? `enabled (${res.badges_published} published)` : 'disabled'}`,
    ];
    if (res.entries.length > 0) {
        lines.push('', '## Mapped Agents', '', '| Agent Name | Pane | Harness | Badge | Observation |', '|---|---|---|---|---|');
        for (const e of res.entries) {
            lines.push(
                `| \`${e.name}\` | \`${e.pane_id}\` (#${e.pane_index}) | \`${e.agent_type || '-'}\` | \`${e.badge_label || '-'}\` | \`${e.observation_state || 'unverified'}\` |`
            );
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatWorkflowsMarkdown(res: WorkflowsResult): string {
    if (res.workflow) {
        return [
            `# NTM Workflow Template: ${res.workflow.name}`,
            '',
            `- **Coordination:** \`${res.workflow.coordination}\``,
            `- **Source:** \`${res.workflow.source}\``,
            `- **Agents:** ${res.workflow.agent_count}`,
            `- **Roles:** ${res.workflow.roles.join(', ') || 'none'}`,
            `- **Validated Transitions:** ${res.workflow.validated_trigger_count}`,
            '',
            res.workflow.description,
            '',
        ].join('\n');
    }
    const lines: string[] = [
        `# NTM Multi-Agent Workflow Catalog (${res.total})`,
        '',
        '| Workflow | Coordination | Agents | Source | Description |',
        '|---|---|---:|---|---|',
    ];
    for (const wf of res.workflows) {
        lines.push(
            `| \`${wf.name}\` | \`${wf.coordination}\` | ${wf.agent_count} | \`${wf.source}\` | ${wf.description} |`
        );
    }
    lines.push('');
    return lines.join('\n');
}

export function formatSessionTemplatesMarkdown(res: SessionTemplatesResult): string {
    const lines: string[] = [
        `# NTM Session Templates (${res.total})`,
        '',
        '| Template | Agents | Source | Description |',
        '|---|---:|---|---|',
    ];
    for (const t of res.templates) {
        lines.push(`| \`${t.name}\` | ${t.agent_count} | \`${t.source}\` | ${t.description} |`);
    }
    lines.push('');
    return lines.join('\n');
}
