import * as vscode from 'vscode';

export type ScaleHarness =
    | 'cc'
    | 'cod'
    | 'gmi'
    | 'agy'
    | 'ollama'
    | 'cursor'
    | 'windsurf'
    | 'aider'
    | 'grok'
    | 'omp'
    | 'oc';

export const SUPPORTED_SCALE_HARNESSES: readonly ScaleHarness[] = [
    'cc',
    'cod',
    'gmi',
    'agy',
    'ollama',
    'cursor',
    'windsurf',
    'aider',
    'grok',
    'omp',
    'oc',
] as const;

export interface ScaleSessionOptions {
    session: string;
    targets: Partial<Record<ScaleHarness, number>>;
    dryRun?: boolean;
    force?: boolean;
}

export interface ScalePlanAction {
    type: string;
    agent_type: string;
    count: number;
    agents: string[];
}

export interface ScaleSessionResult {
    success: boolean;
    session: string;
    dry_run: boolean;
    before: Record<string, number>;
    after: Record<string, number>;
    actions: ScalePlanAction[];
    errors: string[];
}

export interface RebalanceSessionOptions {
    session: string;
    apply?: boolean;
    dryRun?: boolean;
    threshold?: number;
    geometry?: boolean;
    geometryTolerance?: number;
    filterType?: string;
}

export interface RebalanceTransferItem {
    bead_id: string;
    bead_title: string;
    from_pane: number;
    from_agent: string;
    to_pane: number;
    to_agent: string;
    reason?: string;
}

export interface RebalanceSessionResult {
    success: boolean;
    session: string;
    imbalance_score: number;
    recommendation: string;
    dry_run: boolean;
    applied: boolean;
    transfers: RebalanceTransferItem[];
    before: Record<string, number>;
    after: Record<string, number>;
}

export interface ReviewQueueOptions {
    session: string;
    dispatch?: boolean;
    pane?: number;
    projectDir?: string;
}

export interface ReviewQueueSuggestionItem {
    agent: string;
    pane: number;
    agent_type: string;
    prompt: string;
    source: string;
    source_ref?: string;
}

export interface ReviewQueueResult {
    success: boolean;
    session: string;
    dispatched: boolean;
    idle_count: number;
    suggestions: ReviewQueueSuggestionItem[];
}

export interface SessionSummaryOptions {
    session: string;
    since?: string;
}

export interface SummaryAgentItem {
    pane_id: string;
    pane_title?: string;
    agent_type: string;
    state: string;
    output_lines: number;
    commands_run: number;
    files_modified: string[];
    errors: number;
    key_actions: string[];
}

export interface SessionSummaryResult {
    success: boolean;
    session: string;
    total_files: number;
    total_output_lines: number;
    agents: SummaryAgentItem[];
    conflicts: Array<{ path: string; agents: string[] }>;
}

export interface ScaleModeQuickPickOption extends vscode.QuickPickItem {
    mode: 'dryRun' | 'apply' | 'openSwarmCockpit';
}

export const SCALE_MODE_OPTIONS: readonly ScaleModeQuickPickOption[] = [
    {
        label: '$(eye) Preview Scale Plan (Dry Run)',
        description: '--robot-scale=<session> --dry-run',
        detail: 'Compute multi-harness spawn/terminate delta across all 11 harnesses without mutating panes',
        mode: 'dryRun',
    },
    {
        label: '$(rocket) Execute Scale Plan',
        description: '--robot-scale=<session> --force',
        detail: 'Spawn or scale agent panes to match target harness counts',
        mode: 'apply',
    },
    {
        label: '$(globe) Open Swarm Scaler in Web Cockpit',
        description: 'Web Cockpit (/swarm)',
        detail: 'Open interactive 11-harness session scaler in browser',
        mode: 'openSwarmCockpit',
    },
];

export interface RebalanceModeQuickPickOption extends vscode.QuickPickItem {
    mode: 'analyze' | 'apply' | 'geometry' | 'openSwarmCockpit';
}

export const REBALANCE_MODE_OPTIONS: readonly RebalanceModeQuickPickOption[] = [
    {
        label: '$(graph-scatter) Analyze Workload Imbalance (Dry Run)',
        description: '--robot-rebalance=<session> --dry-run',
        detail: 'Compute workload imbalance score and recommended Bead transfers across agent panes',
        mode: 'analyze',
    },
    {
        label: '$(sync) Apply Workload Rebalance Transfers',
        description: '--robot-rebalance=<session> --rebalance-apply',
        detail: 'Execute recommended Bead transfers from overloaded panes to idle agents',
        mode: 'apply',
    },
    {
        label: '$(split-horizontal) Analyze Pane Geometry Balance',
        description: '--robot-rebalance=<session> --rebalance-geometry --dry-run',
        detail: 'Inspect tmux pane geometry skew and layout balance',
        mode: 'geometry',
    },
    {
        label: '$(globe) Open Workload Rebalancer in Web Cockpit',
        description: 'Web Cockpit (/swarm)',
        detail: 'Open interactive workload rebalancer and Coordinator workbench in browser',
        mode: 'openSwarmCockpit',
    },
];

export interface ReviewQueueModeQuickPickOption extends vscode.QuickPickItem {
    mode: 'preview' | 'dispatch' | 'openSwarmCockpit';
}

export const REVIEW_QUEUE_MODE_OPTIONS: readonly ReviewQueueModeQuickPickOption[] = [
    {
        label: '$(checklist) Preview Peer Review Queue',
        description: '--robot-review-queue=<session>',
        detail: 'Generate cross-agent peer review prompts for idle agents from completed beads and git commits',
        mode: 'preview',
    },
    {
        label: '$(send) Dispatch Peer Reviews to Idle Agents',
        description: '--robot-review-queue=<session> --review-dispatch',
        detail: 'Generate and send peer review prompts directly to idle agent panes',
        mode: 'dispatch',
    },
    {
        label: '$(globe) Open Peer Review Queue in Web Cockpit',
        description: 'Web Cockpit (/swarm)',
        detail: 'Open interactive peer review queue and swarm controls in browser',
        mode: 'openSwarmCockpit',
    },
];

export interface SummaryWindowQuickPickOption extends vscode.QuickPickItem {
    mode: 'since' | 'openSessionsCockpit';
    since?: string;
}

export const SUMMARY_WINDOW_OPTIONS: readonly SummaryWindowQuickPickOption[] = [
    {
        label: '$(Pulse) Last 30 Minutes',
        description: '--robot-summary=<session> --since=30m',
        detail: 'Summarize recent agent pane output, files modified, commands run, and conflicts',
        mode: 'since',
        since: '30m',
    },
    {
        label: '$(history) Last 2 Hours',
        description: '--robot-summary=<session> --since=2h',
        detail: 'Summarize multi-hour swarm activity, modified files, and errors',
        mode: 'since',
        since: '2h',
    },
    {
        label: '$(globe) Open Session Activity Summary in Web Cockpit',
        description: 'Web Cockpit (/sessions)',
        detail: 'Open interactive session summary, latest handoff, and events feed in browser',
        mode: 'openSessionsCockpit',
    },
];

function asObj(value: unknown): Record<string, unknown> | undefined {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
        return value as Record<string, unknown>;
    }
    return undefined;
}

function toNumMap(raw: unknown): Record<string, number> {
    const obj = asObj(raw);
    if (!obj) {
        return {};
    }
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(obj)) {
        if (typeof v === 'number' && Number.isFinite(v)) {
            out[k] = v;
        }
    }
    return out;
}

export function parseScaleSpec(spec: string): Partial<Record<ScaleHarness, number>> {
    const tokens = spec
        .split(/[,\s]+/)
        .map(s => s.trim())
        .filter(Boolean);
    if (tokens.length === 0) {
        throw new Error('At least one harness target (e.g. cc=2,cod=1) is required');
    }
    const targets: Partial<Record<ScaleHarness, number>> = {};
    for (const tok of tokens) {
        const clean = tok.replace(/^--(?:scale-)?/, '');
        const eqIdx = clean.indexOf('=');
        if (eqIdx <= 0) {
            throw new Error(`Invalid scale target "${tok}": expected <harness>=<count>`);
        }
        const harness = clean.slice(0, eqIdx).trim() as ScaleHarness;
        const countStr = clean.slice(eqIdx + 1).trim();
        if (!SUPPORTED_SCALE_HARNESSES.includes(harness)) {
            throw new Error(`Unsupported scale harness: ${harness}`);
        }
        const count = Number(countStr);
        if (!Number.isInteger(count) || count < 0) {
            throw new Error(`Scale target count for ${harness} must be a non-negative integer`);
        }
        targets[harness] = count;
    }
    return targets;
}

export function normalizeScaleResult(raw: unknown): ScaleSessionResult {
    const obj = asObj(raw);
    if (!obj) {
        throw new Error('Invalid scale response payload');
    }
    if (obj.success === false) {
        const msg = typeof obj.error === 'string' && obj.error ? obj.error : 'Session scale failed';
        throw new Error(msg);
    }
    const actions: ScalePlanAction[] = Array.isArray(obj.actions)
        ? obj.actions
              .map(entry => {
                  const a = asObj(entry);
                  if (!a || typeof a.type !== 'string') {
                      return undefined;
                  }
                  return {
                      type: a.type,
                      agent_type: typeof a.agent_type === 'string' ? a.agent_type : 'unknown',
                      count: typeof a.count === 'number' ? a.count : 0,
                      agents: Array.isArray(a.agents)
                          ? a.agents.filter((x): x is string => typeof x === 'string')
                          : [],
                  };
              })
              .filter((a): a is ScalePlanAction => a !== undefined)
        : [];
    const errors = Array.isArray(obj.errors)
        ? obj.errors.filter((x): x is string => typeof x === 'string')
        : [];

    return {
        success: true,
        session: typeof obj.session === 'string' ? obj.session : '',
        dry_run: Boolean(obj.dry_run),
        before: toNumMap(obj.before),
        after: toNumMap(obj.after),
        actions,
        errors,
    };
}

export function normalizeRebalanceResult(raw: unknown): RebalanceSessionResult {
    const obj = asObj(raw);
    if (!obj) {
        throw new Error('Invalid rebalance response payload');
    }
    if (obj.success === false) {
        const msg = typeof obj.error === 'string' && obj.error ? obj.error : 'Session rebalance failed';
        throw new Error(msg);
    }
    const transfers: RebalanceTransferItem[] = Array.isArray(obj.transfers)
        ? obj.transfers
              .map(entry => {
                  const tr = asObj(entry);
                  if (!tr || typeof tr.bead_id !== 'string') {
                      return undefined;
                  }
                  const item: RebalanceTransferItem = {
                      bead_id: tr.bead_id,
                      bead_title: typeof tr.bead_title === 'string' ? tr.bead_title : '',
                      from_pane: typeof tr.from_pane === 'number' ? tr.from_pane : 0,
                      from_agent: typeof tr.from_agent === 'string' ? tr.from_agent : 'unknown',
                      to_pane: typeof tr.to_pane === 'number' ? tr.to_pane : 0,
                      to_agent: typeof tr.to_agent === 'string' ? tr.to_agent : 'unknown',
                  };
                  if (typeof tr.reason === 'string' && tr.reason) {
                      item.reason = tr.reason;
                  }
                  return item;
              })
              .filter((tr): tr is RebalanceTransferItem => tr !== undefined)
        : [];

    return {
        success: true,
        session: typeof obj.session === 'string' ? obj.session : '',
        imbalance_score: typeof obj.imbalance_score === 'number' ? obj.imbalance_score : 0,
        recommendation: typeof obj.recommendation === 'string' ? obj.recommendation : 'balanced',
        dry_run: Boolean(obj.dry_run),
        applied: Boolean(obj.applied),
        transfers,
        before: toNumMap(obj.before),
        after: toNumMap(obj.after),
    };
}

export function normalizeReviewQueueResult(raw: unknown): ReviewQueueResult {
    const obj = asObj(raw);
    if (!obj) {
        throw new Error('Invalid review queue response payload');
    }
    if (obj.success === false) {
        const msg = typeof obj.error === 'string' && obj.error ? obj.error : 'Review queue generation failed';
        throw new Error(msg);
    }
    const suggestions: ReviewQueueSuggestionItem[] = Array.isArray(obj.suggestions)
        ? obj.suggestions
              .map(entry => {
                  const s = asObj(entry);
                  if (!s || typeof s.prompt !== 'string') {
                      return undefined;
                  }
                  const item: ReviewQueueSuggestionItem = {
                      agent: typeof s.agent === 'string' ? s.agent : 'agent',
                      pane: typeof s.pane === 'number' ? s.pane : 0,
                      agent_type: typeof s.agent_type === 'string' ? s.agent_type : 'unknown',
                      prompt: s.prompt,
                      source: typeof s.source === 'string' ? s.source : 'git',
                  };
                  if (typeof s.source_ref === 'string' && s.source_ref) {
                      item.source_ref = s.source_ref;
                  }
                  return item;
              })
              .filter((s): s is ReviewQueueSuggestionItem => s !== undefined)
        : [];

    const idleCount = Array.isArray(obj.idle_agents)
        ? obj.idle_agents.length
        : (typeof obj.idle_count === 'number' ? obj.idle_count : 0);

    return {
        success: true,
        session: typeof obj.session === 'string' ? obj.session : '',
        dispatched: Boolean(obj.dispatched),
        idle_count: idleCount,
        suggestions,
    };
}

export function normalizeSummaryResult(raw: unknown): SessionSummaryResult {
    const obj = asObj(raw);
    if (!obj) {
        throw new Error('Invalid session summary response payload');
    }
    if (obj.success === false) {
        const msg = typeof obj.error === 'string' && obj.error ? obj.error : 'Session summary failed';
        throw new Error(msg);
    }
    const sum = asObj(obj.summary) ?? obj;
    const agents: SummaryAgentItem[] = Array.isArray(sum.agents)
        ? sum.agents
              .map(entry => {
                  const a = asObj(entry);
                  if (!a || typeof a.pane_id !== 'string') {
                      return undefined;
                  }
                  const item: SummaryAgentItem = {
                      pane_id: a.pane_id,
                      agent_type: typeof a.agent_type === 'string' ? a.agent_type : 'unknown',
                      state: typeof a.state === 'string' ? a.state : 'idle',
                      output_lines: typeof a.output_lines === 'number' ? a.output_lines : 0,
                      commands_run: typeof a.commands_run === 'number' ? a.commands_run : 0,
                      files_modified: Array.isArray(a.files_modified)
                          ? a.files_modified.filter((x): x is string => typeof x === 'string')
                          : [],
                      errors: typeof a.errors === 'number' ? a.errors : 0,
                      key_actions: Array.isArray(a.key_actions)
                          ? a.key_actions.filter((x): x is string => typeof x === 'string')
                          : [],
                  };
                  if (typeof a.pane_title === 'string' && a.pane_title) {
                      item.pane_title = a.pane_title;
                  }
                  return item;
              })
              .filter((a): a is SummaryAgentItem => a !== undefined)
        : [];

    const conflicts = Array.isArray(sum.conflicts)
        ? sum.conflicts
              .map(entry => {
                  const c = asObj(entry);
                  if (!c || typeof c.path !== 'string') {
                      return undefined;
                  }
                  return {
                      path: c.path,
                      agents: Array.isArray(c.agents)
                          ? c.agents.filter((x): x is string => typeof x === 'string')
                          : [],
                  };
              })
              .filter((c): c is { path: string; agents: string[] } => c !== undefined)
        : [];

    return {
        success: true,
        session: typeof sum.session === 'string' ? sum.session : (typeof obj.session === 'string' ? obj.session : ''),
        total_files: typeof sum.total_files === 'number' ? sum.total_files : 0,
        total_output_lines: typeof sum.total_output_lines === 'number' ? sum.total_output_lines : 0,
        agents,
        conflicts,
    };
}

export function formatScaleMarkdown(res: ScaleSessionResult): string {
    const lines = [
        `# NTM Session Scale ${res.dry_run ? 'Plan (Dry Run)' : 'Execution'}: ${res.session}`,
        '',
        `- **Session:** \`${res.session}\``,
        `- **Mode:** ${res.dry_run ? 'Dry Run' : 'Executed'}`,
        `- **Actions:** ${res.actions.length}`,
    ];
    if (res.actions.length > 0) {
        lines.push('', '## Planned / Executed Actions', '');
        for (const a of res.actions) {
            lines.push(`- **${a.type}** ${a.count} \`${a.agent_type}\`${a.agents.length > 0 ? ` (${a.agents.join(', ')})` : ''}`);
        }
    }
    if (res.errors.length > 0) {
        lines.push('', '## Warnings / Errors', '');
        for (const e of res.errors) {
            lines.push(`- ${e}`);
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatRebalanceMarkdown(res: RebalanceSessionResult): string {
    const lines = [
        `# NTM Workload Rebalance (${res.session})`,
        '',
        `- **Imbalance Score:** ${res.imbalance_score.toFixed(2)} (\`${res.recommendation}\`)`,
        `- **Applied:** ${res.applied ? 'yes' : 'no (analysis)'}`,
        `- **Transfers:** ${res.transfers.length}`,
    ];
    if (res.transfers.length > 0) {
        lines.push('', '## Bead Transfers', '');
        for (const tr of res.transfers) {
            lines.push(
                `- \`${tr.bead_id}\` (${tr.bead_title || 'untitled'}): pane ${tr.from_pane} (\`${tr.from_agent}\`) -> pane ${tr.to_pane} (\`${tr.to_agent}\`)`
            );
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatReviewQueueMarkdown(res: ReviewQueueResult): string {
    const lines = [
        `# NTM Peer Review Queue (${res.session})`,
        '',
        `- **Idle Agents:** ${res.idle_count}`,
        `- **Suggestions:** ${res.suggestions.length} (${res.dispatched ? 'dispatched' : 'preview'})`,
    ];
    if (res.suggestions.length > 0) {
        lines.push('', '## Review Assignments', '');
        for (const s of res.suggestions) {
            lines.push(
                `- **${s.agent}** (pane ${s.pane} · \`${s.agent_type}\` · ${s.source}${s.source_ref ? ` \`${s.source_ref}\`` : ''}): ${s.prompt}`
            );
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatSummaryMarkdown(res: SessionSummaryResult): string {
    const lines = [
        `# NTM Session Activity Summary (${res.session})`,
        '',
        `- **Agents Summarized:** ${res.agents.length}`,
        `- **Files Modified:** ${res.total_files}`,
        `- **Output Lines Analyzed:** ${res.total_output_lines}`,
        `- **Detected File Conflicts:** ${res.conflicts.length}`,
    ];
    if (res.agents.length > 0) {
        lines.push('', '## Agent Activity', '', '| Pane | Harness | State | Lines | Commands | Modified Files | Errors |', '|---|---|---|---:|---:|---:|---:|');
        for (const a of res.agents) {
            lines.push(
                `| \`${a.pane_id}\` | \`${a.agent_type}\` | \`${a.state}\` | ${a.output_lines} | ${a.commands_run} | ${a.files_modified.length} | ${a.errors} |`
            );
        }
    }
    if (res.conflicts.length > 0) {
        lines.push('', '## Detected Conflicts', '');
        for (const c of res.conflicts) {
            lines.push(`- \`${c.path}\` — ${c.agents.join(', ')}`);
        }
    }
    lines.push('');
    return lines.join('\n');
}
