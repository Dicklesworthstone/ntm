import * as vscode from 'vscode';

export type AdoptHarness =
    | 'cc'
    | 'cod'
    | 'gmi'
    | 'agy'
    | 'grok'
    | 'cursor'
    | 'windsurf'
    | 'aider'
    | 'oc'
    | 'omp'
    | 'pi'
    | 'ollama'
    | 'user';

export const SUPPORTED_ADOPT_HARNESSES: readonly AdoptHarness[] = [
    'cc',
    'cod',
    'gmi',
    'agy',
    'grok',
    'cursor',
    'windsurf',
    'aider',
    'oc',
    'omp',
    'pi',
    'ollama',
    'user',
] as const;

export interface AdoptSessionOptions {
    session: string;
    panes?: Partial<Record<AdoptHarness, string>>;
    byWindowHarness?: AdoptHarness;
    autoName?: boolean;
    dryRun?: boolean;
}

export interface AdoptedPaneItem {
    pane_id: string;
    pane_index: number;
    window_index: number;
    agent_type: string;
    old_title?: string;
    new_title: string;
    ntm_index: number;
}

export interface AdoptSessionResult {
    success: boolean;
    session: string;
    total_panes: number;
    dry_run: boolean;
    auto_name: boolean;
    by_window: boolean;
    adopted_panes: AdoptedPaneItem[];
    agents: Record<string, number>;
}

export interface RedactPreviewOptions {
    text?: string;
    filePath?: string;
}

export interface RedactPreviewFindingItem {
    category: string;
    redacted: string;
    start: number;
    end: number;
    line?: number;
    column?: number;
}

export interface RedactPreviewResult {
    success: boolean;
    source: string;
    path?: string;
    input_len: number;
    finding_count: number;
    findings: RedactPreviewFindingItem[];
    output: string;
}

export interface SessionContextOptions {
    session: string;
    lines?: number;
}

export interface ContextAgentItem {
    pane: string;
    pane_id: string;
    pane_idx: number;
    agent_type: string;
    model: string;
    state: string;
    estimated_tokens: number;
    context_limit: number;
    usage_percent: number;
}

export interface SessionContextResult {
    success: boolean;
    session: string;
    agents: ContextAgentItem[];
    high_usage_count: number;
    average_usage: number;
}

export interface AdoptModeQuickPickOption extends vscode.QuickPickItem {
    mode: 'dryRun' | 'apply' | 'byWindow' | 'openSessionsCockpit';
}

export const ADOPT_MODE_OPTIONS: readonly AdoptModeQuickPickOption[] = [
    {
        label: '$(eye) Preview Session Adoption (Dry Run)',
        description: '--robot-adopt=<session> --dry-run',
        detail: 'Validate 2D window.pane or bare indices across all 13 pane types without mutating tmux',
        mode: 'dryRun',
    },
    {
        label: '$(plug) Adopt External Session Panes',
        description: '--robot-adopt=<session>',
        detail: 'Rename panes to NTM convention, stamp @ntm_agent_type, and register Agent Mail identities',
        mode: 'apply',
    },
    {
        label: '$(window) Adopt Window-Per-Agent Session (--adopt-by-window)',
        description: '--robot-adopt=<session> --adopt-by-window=<harness>',
        detail: 'Map every window sole pane in the session to a single harness type',
        mode: 'byWindow',
    },
    {
        label: '$(globe) Open /sessions Adoption Panel in Web Cockpit',
        description: 'Web Cockpit (/sessions)',
        detail: 'Open interactive 12-type external session adoption and context inspector in browser',
        mode: 'openSessionsCockpit',
    },
];

export interface RedactModeQuickPickOption extends vscode.QuickPickItem {
    mode: 'selectionOrInput' | 'currentFile' | 'openSafetyCockpit';
}

export const REDACT_MODE_OPTIONS: readonly RedactModeQuickPickOption[] = [
    {
        label: '$(shield) Scan Selection or Input Text',
        description: '--robot-redact=<text>',
        detail: 'Preview secret/PII redaction findings and safe-redacted text without exposing raw secrets',
        mode: 'selectionOrInput',
    },
    {
        label: '$(file-code) Scan Current Editor File',
        description: '--robot-redact --redact-file=<path>',
        detail: 'Scan the active saved file for secrets/PII with line & column offsets',
        mode: 'currentFile',
    },
    {
        label: '$(globe) Open Secret Redaction Sandbox in Web Cockpit',
        description: 'Web Cockpit (/safety)',
        detail: 'Open interactive Git Hooks & Secret Redaction Sandbox in browser',
        mode: 'openSafetyCockpit',
    },
];

export interface ContextLinesQuickPickOption extends vscode.QuickPickItem {
    mode: 'lines200' | 'lines1000' | 'openSessionsCockpit';
    lines?: number;
}

export const CONTEXT_LINES_OPTIONS: readonly ContextLinesQuickPickOption[] = [
    {
        label: '$(dashboard) Inspect Agent Context Gauges (200 lines)',
        description: '--robot-context=<session> --lines=200',
        detail: 'Estimate per-pane token consumption and context window fill percentage',
        mode: 'lines200',
        lines: 200,
    },
    {
        label: '$(telescope) Deep Context Scan (1000 lines)',
        description: '--robot-context=<session> --lines=1000',
        detail: 'Capture deeper scrollback per pane for transcript/status-bar context estimation',
        mode: 'lines1000',
        lines: 1000,
    },
    {
        label: '$(globe) Open Context & Diff Inspector in Web Cockpit',
        description: 'Web Cockpit (/sessions)',
        detail: 'Open live session context gauges, activity diff, and file attribution in browser',
        mode: 'openSessionsCockpit',
    },
];

function asRecord(value: unknown): Record<string, unknown> | undefined {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
        return value as Record<string, unknown>;
    }
    return undefined;
}

/**
 * Parse space-separated `harness=paneSpec` assignments (e.g. `cc=0,1 agy=2.0,3.0 user=4`).
 */
export function parseAdoptSpec(spec: string): Partial<Record<AdoptHarness, string>> {
    const trimmed = (spec ?? '').trim();
    if (!trimmed) {
        throw new Error('At least one harness=panes assignment is required (e.g. cc=0,1 cod=2)');
    }
    const allowed = new Set<string>(SUPPORTED_ADOPT_HARNESSES);
    const out: Partial<Record<AdoptHarness, string>> = {};
    const tokens = trimmed.split(/\s+/).filter(Boolean);
    for (const tok of tokens) {
        const eqIdx = tok.indexOf('=');
        if (eqIdx <= 0 || eqIdx === tok.length - 1) {
            throw new Error(`Invalid adopt token "${tok}": expected <harness>=<panes> (e.g. cc=0,1 or agy=1.0,2.0)`);
        }
        const harness = tok.slice(0, eqIdx).trim().toLowerCase();
        const paneSpec = tok.slice(eqIdx + 1).trim();
        if (!allowed.has(harness)) {
            throw new Error(
                `Unsupported adopt harness "${harness}". Supported: ${SUPPORTED_ADOPT_HARNESSES.join(', ')}`
            );
        }
        if (!/^[0-9.,-]+$/.test(paneSpec)) {
            throw new Error(
                `Invalid pane specification "${paneSpec}" for ${harness}: expected indices, ranges, or window.pane`
            );
        }
        out[harness as AdoptHarness] = paneSpec;
    }
    if (Object.keys(out).length === 0) {
        throw new Error('At least one harness=panes assignment is required');
    }
    return out;
}

export function normalizeAdoptResult(raw: unknown): AdoptSessionResult {
    const obj = asRecord(raw) ?? {};
    const panesRaw = Array.isArray(obj.adopted_panes) ? obj.adopted_panes : [];
    const adoptedPanes: AdoptedPaneItem[] = panesRaw.map(entry => {
        const p = asRecord(entry) ?? {};
        const item: AdoptedPaneItem = {
            pane_id: typeof p.pane_id === 'string' ? p.pane_id : '',
            pane_index: typeof p.pane_index === 'number' ? p.pane_index : 0,
            window_index: typeof p.window_index === 'number' ? p.window_index : 0,
            agent_type: typeof p.agent_type === 'string' ? p.agent_type : 'unknown',
            new_title: typeof p.new_title === 'string' ? p.new_title : '',
            ntm_index: typeof p.ntm_index === 'number' ? p.ntm_index : 1,
        };
        if (typeof p.old_title === 'string' && p.old_title.length > 0) {
            item.old_title = p.old_title;
        }
        return item;
    });

    const agentsRecord = asRecord(obj.agents) ?? {};
    const agents: Record<string, number> = {};
    for (const [k, v] of Object.entries(agentsRecord)) {
        if (typeof v === 'number' && v > 0) {
            agents[k] = v;
        }
    }

    return {
        success: obj.success !== false,
        session: typeof obj.session === 'string' ? obj.session : '',
        total_panes: typeof obj.total_panes === 'number' ? obj.total_panes : adoptedPanes.length,
        dry_run: Boolean(obj.dry_run),
        auto_name: obj.auto_name !== false,
        by_window: Boolean(obj.by_window),
        adopted_panes: adoptedPanes,
        agents,
    };
}

export function normalizeRedactPreviewResult(raw: unknown): RedactPreviewResult {
    const obj = asRecord(raw) ?? {};
    const findingsRaw = Array.isArray(obj.findings) ? obj.findings : [];
    const findings: RedactPreviewFindingItem[] = findingsRaw.map(entry => {
        const f = asRecord(entry) ?? {};
        const item: RedactPreviewFindingItem = {
            category: typeof f.category === 'string' ? f.category : 'SECRET',
            redacted: typeof f.redacted === 'string' ? f.redacted : '[REDACTED]',
            start: typeof f.start === 'number' ? f.start : 0,
            end: typeof f.end === 'number' ? f.end : 0,
        };
        if (typeof f.line === 'number' && f.line > 0) {
            item.line = f.line;
        }
        if (typeof f.column === 'number' && f.column > 0) {
            item.column = f.column;
        }
        return item;
    });

    const res: RedactPreviewResult = {
        success: obj.success !== false,
        source: typeof obj.source === 'string' ? obj.source : 'text',
        input_len: typeof obj.input_len === 'number' ? obj.input_len : 0,
        finding_count: typeof obj.finding_count === 'number' ? obj.finding_count : findings.length,
        findings,
        output: typeof obj.output === 'string' ? obj.output : '',
    };
    if (typeof obj.path === 'string' && obj.path.length > 0) {
        res.path = obj.path;
    }
    return res;
}

export function normalizeContextResult(raw: unknown): SessionContextResult {
    const obj = asRecord(raw) ?? {};
    const agentsRaw = Array.isArray(obj.agents) ? obj.agents : [];
    const agents: ContextAgentItem[] = agentsRaw.map(entry => {
        const a = asRecord(entry) ?? {};
        return {
            pane: typeof a.pane === 'string' ? a.pane : String(a.pane ?? ''),
            pane_id: typeof a.pane_id === 'string' ? a.pane_id : '',
            pane_idx: typeof a.pane_idx === 'number' ? a.pane_idx : 0,
            agent_type: typeof a.agent_type === 'string' ? a.agent_type : 'unknown',
            model: typeof a.model === 'string' ? a.model : '',
            state: typeof a.state === 'string' ? a.state : 'unknown',
            estimated_tokens: typeof a.estimated_tokens === 'number' ? a.estimated_tokens : 0,
            context_limit: typeof a.context_limit === 'number' ? a.context_limit : 0,
            usage_percent: typeof a.usage_percent === 'number' ? a.usage_percent : 0,
        };
    });

    const summaryObj = asRecord(obj.summary) ?? {};
    const highUsage = typeof summaryObj.high_usage_count === 'number'
        ? summaryObj.high_usage_count
        : agents.filter(a => a.usage_percent >= 80).length;
    const avgUsage = typeof summaryObj.avg_usage === 'number'
        ? summaryObj.avg_usage
        : typeof summaryObj.average_usage === 'number'
            ? summaryObj.average_usage
            : (agents.length > 0
                ? agents.reduce((acc, a) => acc + a.usage_percent, 0) / agents.length
                : 0);

    return {
        success: obj.success !== false,
        session: typeof obj.session === 'string' ? obj.session : '',
        agents,
        high_usage_count: highUsage,
        average_usage: avgUsage,
    };
}

export function formatAdoptMarkdown(res: AdoptSessionResult): string {
    const lines: string[] = [
        `# NTM Session Adoption (${res.session})`,
        '',
        `- **Mode:** ${res.dry_run ? 'Dry Run Preview' : 'Applied'}`,
        `- **Adopted Panes:** ${res.adopted_panes.length} of ${res.total_panes}`,
        `- **Auto-Name Titles:** ${res.auto_name ? 'yes' : 'no'}`,
    ];
    if (res.adopted_panes.length > 0) {
        lines.push('', '## Adopted Panes', '', '| Pane ID | Win.Pane | Harness | New Title |', '|---|---|---|---|');
        for (const p of res.adopted_panes) {
            lines.push(
                `| \`${p.pane_id}\` | \`${p.window_index}.${p.pane_index}\` | \`${p.agent_type}\` | \`${p.new_title}\` |`
            );
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatRedactPreviewMarkdown(res: RedactPreviewResult): string {
    const lines: string[] = [
        `# NTM Secret & PII Redaction Preview`,
        '',
        `- **Source:** \`${res.path || res.source}\``,
        `- **Input Length:** ${res.input_len} bytes`,
        `- **Findings:** ${res.finding_count}`,
    ];
    if (res.findings.length > 0) {
        lines.push('', '## Detected Secret / PII Placeholders', '');
        for (const f of res.findings) {
            const loc = f.line && f.column ? `line ${f.line}:${f.column}` : `bytes ${f.start}-${f.end}`;
            lines.push(`- **${f.category}** at ${loc}: \`${f.redacted}\``);
        }
    }
    lines.push('', '## Safe-Redacted Output', '', '```text', res.output, '```', '');
    return lines.join('\n');
}

export function formatContextMarkdown(res: SessionContextResult): string {
    const lines: string[] = [
        `# NTM Session Context Window Report (${res.session})`,
        '',
        `- **Measured Agent Panes:** ${res.agents.length}`,
        `- **Average Usage:** ${res.average_usage.toFixed(1)}%`,
        `- **High Usage (>=80%):** ${res.high_usage_count}`,
    ];
    if (res.agents.length > 0) {
        lines.push('', '## Per-Agent Context Gauges', '', '| Pane | Harness | Model | State | Usage | Tokens / Limit |', '|---|---|---|---|---:|---:|');
        for (const a of res.agents) {
            lines.push(
                `| \`${a.pane}\` (\`${a.pane_id}\`) | \`${a.agent_type}\` | \`${a.model || 'default'}\` | \`${a.state}\` | ${a.usage_percent.toFixed(1)}% | ${a.estimated_tokens} / ${a.context_limit} |`
            );
        }
    }
    lines.push('');
    return lines.join('\n');
}
