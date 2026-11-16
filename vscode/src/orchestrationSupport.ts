import * as vscode from 'vscode';

export type AssignStrategy = 'simple' | 'balanced' | 'speed' | 'quality' | 'dependency';

export const SUPPORTED_ASSIGN_STRATEGIES: readonly AssignStrategy[] = [
    'simple',
    'balanced',
    'speed',
    'quality',
    'dependency',
] as const;

export interface DoctorOptions {
    contention?: boolean;
    verbose?: boolean;
}

export interface DoctorCheckItem {
    name: string;
    category: string;
    status: string;
    message?: string;
    details?: string;
}

export interface DoctorContentionHolder {
    pid: number;
    process: string;
    lock_file: string;
    cwd?: string;
    cmdline?: string;
}

export interface DoctorResult {
    success: boolean;
    overall_status: string;
    passed: number;
    warnings: number;
    errors: number;
    checks: DoctorCheckItem[];
    contention: DoctorContentionHolder[];
    error?: string;
}

export interface PreflightOptions {
    prompt?: string;
    filePath?: string;
    strict?: boolean;
}

export interface PreflightFinding {
    severity: string;
    code: string;
    message: string;
    remediation?: string;
}

export interface PreflightResult {
    success: boolean;
    allowed: boolean;
    preview_hash: string;
    preview_len: number;
    estimated_tokens: number;
    error_count: number;
    warning_count: number;
    info_count: number;
    dcg_available: boolean;
    findings: PreflightFinding[];
    error?: string;
}

export interface AssignWorkOptions {
    session: string;
    strategy?: AssignStrategy;
    beads?: string;
    relax?: boolean;
}

export interface AssignWorkRecommendation {
    pane_id: string;
    pane_target: string;
    agent_type: string;
    model?: string;
    assign_bead: string;
    bead_title: string;
    priority: string;
    confidence: number;
    reasoning: string;
}

export interface AssignWorkSummary {
    total_agents: number;
    idle_agents: number;
    working_agents: number;
    ready_beads: number;
    blocked_beads: number;
    recommendations: number;
    unassignable_beads: number;
}

export interface AssignWorkResult {
    success: boolean;
    session: string;
    strategy: string;
    relaxed: boolean;
    recommendations: AssignWorkRecommendation[];
    idle_agents: string[];
    summary: AssignWorkSummary;
}

export interface DoctorQuickPickOption extends vscode.QuickPickItem {
    mode: 'standard' | 'contention' | 'openOverviewCockpit';
}

export const DOCTOR_MODE_OPTIONS: readonly DoctorQuickPickOption[] = [
    {
        label: '$(stethoscope) Full Ecosystem Health Check',
        description: '--robot-doctor --doctor-verbose',
        detail: 'Verify required dependencies, optional tools, daemons, invariants, safety defaults, and guard hooks',
        mode: 'standard',
    },
    {
        label: '$(lock) Ecosystem Health + Lock Contention Scan',
        description: '--robot-doctor --doctor-contention --doctor-verbose',
        detail: 'Include live process inspection for SQLite/Beads lock contention holders',
        mode: 'contention',
    },
    {
        label: '$(globe) Open Overview Doctor in Web Cockpit',
        description: 'Web Cockpit (/)',
        detail: 'Open interactive System Doctor, Dependency Matrix, and Support Bundle cockpit in browser',
        mode: 'openOverviewCockpit',
    },
];

export interface PreflightQuickPickOption extends vscode.QuickPickItem {
    mode: 'selectionOrInput' | 'strict' | 'openSwarmCockpit';
}

export const PREFLIGHT_MODE_OPTIONS: readonly PreflightQuickPickOption[] = [
    {
        label: '$(check) Standard Prompt Preflight Lint',
        description: '--robot-preflight=<prompt>',
        detail: 'Estimate token footprint, inspect DCG destructive-command guardrails, and lint prompt risks',
        mode: 'selectionOrInput',
    },
    {
        label: '$(shield) Strict Prompt Preflight (Fail on Warnings)',
        description: '--robot-preflight=<prompt> --preflight-strict',
        detail: 'Treat warnings as blocking errors before dispatching prompts to agents',
        mode: 'strict',
    },
    {
        label: '$(globe) Open Swarm Orchestration Workbench in Web Cockpit',
        description: 'Web Cockpit (/swarm)',
        detail: 'Open interactive Prompt Preflight, Smart Router, and Assignment Planner in browser',
        mode: 'openSwarmCockpit',
    },
];

export interface AssignStrategyQuickPickOption extends vscode.QuickPickItem {
    strategy: AssignStrategy | 'openSwarmCockpit';
    relax?: boolean;
}

export const ASSIGN_STRATEGY_OPTIONS: readonly AssignStrategyQuickPickOption[] = [
    {
        label: '$(balance) Balanced Planner (Recommended)',
        description: '--strategy=balanced',
        detail: 'Balance workload across idle agents weighted by task-type affinity',
        strategy: 'balanced',
    },
    {
        label: '$(git-PullRequest) Dependency-Unblocking Planner',
        description: '--strategy=dependency',
        detail: 'Prioritize ready beads that unblock the largest downstream dependency subgraphs',
        strategy: 'dependency',
    },
    {
        label: '$(zap) Speed Planner',
        description: '--strategy=speed',
        detail: 'Favor fastest-turnaround agent pairings for rapid backlog burn-down',
        strategy: 'speed',
    },
    {
        label: '$(verified) Quality Planner',
        description: '--strategy=quality',
        detail: 'Favor highest capability-affinity pairings for subtle or critical beads',
        strategy: 'quality',
    },
    {
        label: '$(list-ordered) Simple Sequential Pairing',
        description: '--strategy=simple',
        detail: 'Pair ready beads sequentially to idle agents without affinity scoring',
        strategy: 'simple',
    },
    {
        label: '$(globe) Open Swarm Orchestration Workbench in Web Cockpit',
        description: 'Web Cockpit (/swarm)',
        detail: 'Open interactive Assignment Planner, Prompt Router, and Preflight in browser',
        strategy: 'openSwarmCockpit',
    },
];

function asObj(value: unknown): Record<string, unknown> | undefined {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
        return value as Record<string, unknown>;
    }
    return undefined;
}

export function normalizeDoctorResult(raw: unknown): DoctorResult {
    const obj = asObj(raw);
    if (!obj) {
        throw new Error('Invalid doctor response payload');
    }
    const overallRaw =
        (typeof obj.overall_status === 'string' && obj.overall_status) ||
        (typeof obj.overall === 'string' && obj.overall) ||
        '';
    if (obj.success === false && !overallRaw && !Array.isArray(obj.dependencies) && !Array.isArray(obj.tools)) {
        const msg = typeof obj.error === 'string' && obj.error.length > 0
            ? obj.error
            : 'Doctor command failed';
        throw new Error(msg);
    }

    const checks: DoctorCheckItem[] = [];
    const pushRows = (arr: unknown, defaultCat: string) => {
        if (!Array.isArray(arr)) {
            return;
        }
        for (const item of arr) {
            const r = asObj(item);
            if (!r || typeof r.name !== 'string') {
                continue;
            }
            const status =
                (typeof r.status === 'string' && r.status) ||
                (r.installed === true || r.running === true ? 'ok' : (r.required ? 'error' : 'warning'));
            const detailParts = [
                typeof r.version === 'string' && r.version ? r.version : '',
                typeof r.path === 'string' && r.path ? r.path : '',
                typeof r.port === 'number' ? `port ${r.port}` : '',
                typeof r.details === 'string' && r.details ? r.details : '',
            ].filter(Boolean);
            checks.push({
                name: r.name,
                category: typeof r.category === 'string' && r.category ? r.category : defaultCat,
                status,
                message: typeof r.message === 'string' ? r.message : undefined,
                details: detailParts.length > 0 ? detailParts.join(' · ') : undefined,
            });
        }
    };

    pushRows(obj.dependencies, 'Dependency');
    pushRows(obj.tools, 'Tool');
    pushRows(obj.daemons, 'Daemon');
    pushRows(obj.configuration, 'Configuration');
    pushRows(obj.invariants, 'Invariant');
    pushRows(obj.safety_defaults, 'Safety Default');
    pushRows(obj.checks, 'Diagnostic');

    const gh = asObj(obj.guard_hook);
    if (gh) {
        const degraded = typeof gh.degraded_runs === 'number' ? gh.degraded_runs : 0;
        checks.push({
            name: typeof gh.name === 'string' && gh.name ? gh.name : 'guard-hook',
            category: 'Safety Hook',
            status: typeof gh.status === 'string' && gh.status ? gh.status : (degraded > 0 ? 'warning' : 'ok'),
            message: typeof gh.message === 'string' ? gh.message : `degraded_runs=${degraded}`,
            details: typeof gh.since === 'string' && gh.since ? `since ${gh.since}` : undefined,
        });
    }

    const contention: DoctorContentionHolder[] = Array.isArray(obj.contention)
        ? obj.contention
            .map(entry => {
                const c = asObj(entry);
                if (!c || typeof c.pid !== 'number') {
                    return undefined;
                }
                const holder: DoctorContentionHolder = {
                    pid: c.pid,
                    process: typeof c.process === 'string'
                        ? c.process
                        : (typeof c.command === 'string'
                            ? c.command
                            : (typeof c.comm === 'string' ? c.comm : 'unknown')),
                    lock_file: typeof c.lock_file === 'string'
                        ? c.lock_file
                        : (typeof c.resource === 'string'
                            ? c.resource
                            : (typeof c.path === 'string' ? c.path : '')),
                };
                if (typeof c.cwd === 'string' && c.cwd) {
                    holder.cwd = c.cwd;
                }
                if (typeof c.cmdline === 'string' && c.cmdline) {
                    holder.cmdline = c.cmdline;
                }
                return holder;
            })
            .filter((c): c is DoctorContentionHolder => c !== undefined)
        : [];

    const passed = typeof obj.passed === 'number'
        ? obj.passed
        : checks.filter(c => ['ok', 'pass', 'passed', 'healthy'].includes(c.status.toLowerCase())).length;
    const warnings = typeof obj.warnings === 'number'
        ? obj.warnings
        : checks.filter(c => ['warn', 'warning'].includes(c.status.toLowerCase())).length;
    const errors = typeof obj.errors === 'number'
        ? obj.errors
        : checks.filter(c => ['error', 'fail', 'failed', 'unhealthy'].includes(c.status.toLowerCase())).length;
    const overallStatus = overallRaw || (errors > 0 ? 'unhealthy' : (warnings > 0 ? 'warning' : 'healthy'));

    return {
        success: obj.success !== false && errors === 0,
        overall_status: overallStatus,
        passed,
        warnings,
        errors,
        checks,
        contention,
        error: typeof obj.error === 'string' && obj.error ? obj.error : undefined,
    };
}

export function normalizePreflightResult(raw: unknown): PreflightResult {
    const obj = asObj(raw);
    if (!obj) {
        throw new Error('Invalid preflight response payload');
    }
    const hasPreflightFields =
        Array.isArray(obj.findings) ||
        typeof obj.error_count === 'number' ||
        typeof obj.estimated_tokens === 'number';
    if (obj.success === false && !hasPreflightFields) {
        const msg = typeof obj.error === 'string' && obj.error.length > 0
            ? obj.error
            : 'Prompt preflight failed';
        throw new Error(msg);
    }

    const findings: PreflightFinding[] = Array.isArray(obj.findings)
        ? obj.findings
            .map(entry => {
                const f = asObj(entry);
                if (!f || typeof f.message !== 'string') {
                    return undefined;
                }
                const finding: PreflightFinding = {
                    severity: typeof f.severity === 'string' ? f.severity : 'info',
                    code: typeof f.id === 'string'
                        ? f.id
                        : (typeof f.code === 'string'
                            ? f.code
                            : (typeof f.rule === 'string' ? f.rule : 'lint')),
                    message: f.message,
                };
                const rem = typeof f.help === 'string' && f.help
                    ? f.help
                    : (typeof f.remediation === 'string'
                        ? f.remediation
                        : (typeof f.suggestion === 'string' ? f.suggestion : ''));
                if (rem) {
                    finding.remediation = rem;
                }
                return finding;
            })
            .filter((f): f is PreflightFinding => f !== undefined)
        : [];

    const errorCount = typeof obj.error_count === 'number'
        ? obj.error_count
        : findings.filter(f => f.severity.toLowerCase() === 'error').length;
    const warningCount = typeof obj.warning_count === 'number'
        ? obj.warning_count
        : findings.filter(f => f.severity.toLowerCase().startsWith('warn')).length;
    const infoCount = typeof obj.info_count === 'number'
        ? obj.info_count
        : findings.filter(f => f.severity.toLowerCase() === 'info').length;
    const allowed = typeof obj.allowed === 'boolean'
        ? obj.allowed
        : (obj.success !== false && errorCount === 0);

    return {
        success: obj.success !== false,
        allowed,
        preview_hash: typeof obj.preview_hash === 'string' ? obj.preview_hash : '',
        preview_len: typeof obj.preview_len === 'number' ? obj.preview_len : 0,
        estimated_tokens: typeof obj.estimated_tokens === 'number' ? obj.estimated_tokens : 0,
        error_count: errorCount,
        warning_count: warningCount,
        info_count: infoCount,
        dcg_available: Boolean(obj.dcg_available),
        findings,
        error: typeof obj.error === 'string' && obj.error ? obj.error : undefined,
    };
}

export function normalizeAssignWorkResult(raw: unknown): AssignWorkResult {
    const obj = asObj(raw);
    if (!obj) {
        throw new Error('Invalid assign response payload');
    }
    if (obj.success === false) {
        const msg = typeof obj.error === 'string' && obj.error.length > 0
            ? obj.error
            : 'Work assignment planning failed';
        throw new Error(msg);
    }

    const recommendations: AssignWorkRecommendation[] = Array.isArray(obj.recommendations)
        ? obj.recommendations
            .map(entry => {
                const r = asObj(entry);
                if (!r || typeof r.assign_bead !== 'string') {
                    return undefined;
                }
                const rec: AssignWorkRecommendation = {
                    pane_id: typeof r.pane_id === 'string' ? r.pane_id : '',
                    pane_target: typeof r.pane_target === 'string' ? r.pane_target : '',
                    agent_type: typeof r.agent_type === 'string' ? r.agent_type : 'unknown',
                    assign_bead: r.assign_bead,
                    bead_title: typeof r.bead_title === 'string' ? r.bead_title : '',
                    priority: typeof r.priority === 'string' ? r.priority : '',
                    confidence: typeof r.confidence === 'number' ? r.confidence : 0,
                    reasoning: typeof r.reasoning === 'string' ? r.reasoning : '',
                };
                if (typeof r.model === 'string' && r.model) {
                    rec.model = r.model;
                }
                return rec;
            })
            .filter((r): r is AssignWorkRecommendation => r !== undefined)
        : [];

    const rawSum = asObj(obj.summary);
    const idleAgents = Array.isArray(obj.idle_agents)
        ? obj.idle_agents.filter((x): x is string => typeof x === 'string')
        : [];

    return {
        success: true,
        session: typeof obj.session === 'string' ? obj.session : '',
        strategy: typeof obj.strategy === 'string' ? obj.strategy : 'simple',
        relaxed: Boolean(obj.relaxed),
        recommendations,
        idle_agents: idleAgents,
        summary: {
            total_agents: typeof rawSum?.total_agents === 'number' ? rawSum.total_agents : 0,
            idle_agents: typeof rawSum?.idle_agents === 'number' ? rawSum.idle_agents : idleAgents.length,
            working_agents: typeof rawSum?.working_agents === 'number' ? rawSum.working_agents : 0,
            ready_beads: typeof rawSum?.ready_beads === 'number' ? rawSum.ready_beads : 0,
            blocked_beads: typeof rawSum?.blocked_beads === 'number' ? rawSum.blocked_beads : 0,
            recommendations: typeof rawSum?.recommendations === 'number' ? rawSum.recommendations : recommendations.length,
            unassignable_beads: typeof rawSum?.unassignable_beads === 'number' ? rawSum.unassignable_beads : 0,
        },
    };
}

export function formatDoctorMarkdown(res: DoctorResult): string {
    const lines: string[] = [
        `# NTM Ecosystem Doctor Report (${res.overall_status.toUpperCase()})`,
        '',
        `- **Overall Status:** \`${res.overall_status}\``,
        `- **Passed:** ${res.passed} · **Warnings:** ${res.warnings} · **Errors:** ${res.errors}`,
    ];
    if (res.checks.length > 0) {
        lines.push('', '## Diagnostic Checks', '', '| Category | Check | Status | Message | Details |', '|---|---|---|---|---|');
        for (const c of res.checks) {
            lines.push(`| ${c.category} | \`${c.name}\` | \`${c.status}\` | ${c.message ?? '—'} | ${c.details ?? '—'} |`);
        }
    }
    if (res.contention.length > 0) {
        lines.push('', '## Lock Contention Holders', '', '| PID | Process | Lock File | CWD |', '|---:|---|---|---|');
        for (const h of res.contention) {
            lines.push(`| ${h.pid} | \`${h.process}\` | \`${h.lock_file}\` | \`${h.cwd ?? '—'}\` |`);
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatPreflightMarkdown(res: PreflightResult): string {
    const lines: string[] = [
        `# NTM Prompt Preflight Report (${res.allowed ? 'PASSED' : 'BLOCKED'})`,
        '',
        `- **Verdict:** ${res.allowed ? 'PASSED' : 'BLOCKED'}`,
        `- **Estimated Tokens:** ${res.estimated_tokens} (${res.preview_len} chars · hash \`${res.preview_hash || 'n/a'}\`)`,
        `- **DCG Guard Available:** ${res.dcg_available ? 'yes' : 'no'}`,
        `- **Findings:** ${res.error_count} errors · ${res.warning_count} warnings · ${res.info_count} info`,
    ];
    if (res.findings.length > 0) {
        lines.push('', '## Lint Findings', '');
        for (const f of res.findings) {
            lines.push(
                `- **[${f.severity.toUpperCase()}] \`${f.code}\`**: ${f.message}${f.remediation ? ` _(Remediation: ${f.remediation})_` : ''}`
            );
        }
    }
    lines.push('');
    return lines.join('\n');
}

export function formatAssignWorkMarkdown(res: AssignWorkResult): string {
    const lines: string[] = [
        `# NTM Work Assignment Plan (${res.session} — strategy: ${res.strategy})`,
        '',
        `- **Session:** \`${res.session}\``,
        `- **Strategy:** \`${res.strategy}\`${res.relaxed ? ' (relaxed)' : ''}`,
        `- **Summary:** ${res.summary.recommendations} recommendation(s) · ${res.summary.idle_agents}/${res.summary.total_agents} idle agents · ${res.summary.ready_beads} ready beads`,
    ];
    if (res.recommendations.length === 0) {
        lines.push('', 'No ready bead-to-agent assignments produced.');
    } else {
        lines.push('', '## Recommended Pairings', '', '| Bead | Priority | Agent | Pane | Confidence | Reasoning |', '|---|---|---|---|---:|---|');
        for (const r of res.recommendations) {
            lines.push(
                `| \`${r.assign_bead}\` (${r.bead_title}) | ${r.priority || '—'} | \`${r.agent_type}\` | \`${r.pane_id}\` (${r.pane_target}) | ${Math.round(r.confidence * 100)}% | ${r.reasoning || '—'} |`
            );
        }
    }
    lines.push('');
    return lines.join('\n');
}
