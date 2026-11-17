import * as path from 'path';
import * as vscode from 'vscode';
import {
    AgentInfo,
    AnalyticsResult,
    AttentionItem,
    AttentionMutationAction,
    AuditResult,
    BackpressureLevel,
    BackpressureResult,
    buildNtmTerminalCommand,
    buildWebCockpitCommand,
    DEFAULT_WEB_PORT,
    EnsemblePreset,
    EnsembleSuggestResult,
    HandoffResult,
    IncidentAction,
    IncidentItem,
    normalizeWebPort,
    NtmClient,
    RobotStatus,
    SendHarness,
    SessionInfo,
    shellQuote,
    SpawnHarness,
    SupportBundleResult,
    WorktreeInfo,
    WorktreeListResult,
} from './ntmClient';
import {
    ASSIGN_STRATEGY_OPTIONS,
    DOCTOR_MODE_OPTIONS,
    formatAssignWorkMarkdown,
    formatDoctorMarkdown,
    formatPreflightMarkdown,
    PREFLIGHT_MODE_OPTIONS,
} from './orchestrationSupport';
import {
    formatRebalanceMarkdown,
    formatReviewQueueMarkdown,
    formatScaleMarkdown,
    formatSummaryMarkdown,
    parseScaleSpec,
    REBALANCE_MODE_OPTIONS,
    REVIEW_QUEUE_MODE_OPTIONS,
    SCALE_MODE_OPTIONS,
    SUMMARY_WINDOW_OPTIONS,
} from './swarmOpsSupport';
import { NtmDashboard } from './dashboard';
import { normalizeWorkspaceRelativePath, registerFileDecorations } from './fileDecorations';
import { NtmTreeItem, registerSessionTreeView } from './sessionTreeView';

export {
    ASSIGN_STRATEGY_OPTIONS,
    buildNtmTerminalCommand,
    buildWebCockpitCommand,
    DOCTOR_MODE_OPTIONS,
    formatAssignWorkMarkdown,
    formatDoctorMarkdown,
    formatPreflightMarkdown,
    formatRebalanceMarkdown,
    formatReviewQueueMarkdown,
    formatScaleMarkdown,
    formatSummaryMarkdown,
    normalizeWebPort,
    parseScaleSpec,
    PREFLIGHT_MODE_OPTIONS,
    REBALANCE_MODE_OPTIONS,
    REVIEW_QUEUE_MODE_OPTIONS,
    SCALE_MODE_OPTIONS,
    shellQuote,
    SUMMARY_WINDOW_OPTIONS,
};

export const WEB_COCKPIT_TERMINAL_NAME = 'NTM Web Cockpit';

export interface SpawnHarnessQuickPickItem extends vscode.QuickPickItem {
    harness: SpawnHarness | 'custom';
}

export interface SendTargetQuickPickItem extends vscode.QuickPickItem {
    target: SendHarness | 'all';
}

export const SPAWN_HARNESS_OPTIONS: readonly SpawnHarnessQuickPickItem[] = [
    { label: 'Claude Code (cc)', harness: 'cc', description: '--cc=<count>' },
    { label: 'Codex (cod)', harness: 'cod', description: '--cod=<count>' },
    { label: 'Antigravity (agy)', harness: 'agy', description: '--agy=<count>' },
    { label: 'Oh My Pi (omp)', harness: 'omp', description: '--omp=<count>' },
    { label: 'Grok (grok)', harness: 'grok', description: '--grok=<count>' },
    { label: 'OpenCode (oc)', harness: 'oc', description: '--oc=<count>' },
    { label: 'Pi (pi)', harness: 'pi', description: '--pi=<count>' },
    {
        label: 'Custom Preset / Flags...',
        harness: 'custom',
        description: 'Enter custom spawn flags (e.g. --cc=2 --cod=1 or --recipe=default)',
    },
];

export const SEND_TARGET_OPTIONS: readonly SendTargetQuickPickItem[] = [
    { label: 'All Agents (--all)', target: 'all', description: 'Broadcast to all agent panes' },
    { label: 'Claude Code (--cc)', target: 'cc', description: 'Target Claude panes' },
    { label: 'Codex (--cod)', target: 'cod', description: 'Target Codex panes' },
    { label: 'Antigravity (--agy)', target: 'agy', description: 'Target Antigravity panes' },
    { label: 'Oh My Pi (--omp)', target: 'omp', description: 'Target Oh My Pi panes' },
    { label: 'Grok (--grok)', target: 'grok', description: 'Target Grok panes' },
    { label: 'OpenCode (--oc)', target: 'oc', description: 'Target OpenCode panes' },
    { label: 'Pi (--pi)', target: 'pi', description: 'Target Pi panes' },
    { label: 'Gemini (--gmi)', target: 'gmi', description: 'Target Gemini panes' },
];

export interface EnsemblePresetQuickPickItem extends vscode.QuickPickItem {
    presetName: string;
    recommended: boolean;
}

export interface EnsembleSynthesisQuickPickItem extends vscode.QuickPickItem {
    strategy: string;
    force: boolean;
}

export const ENSEMBLE_SYNTHESIS_STRATEGY_OPTIONS: readonly EnsembleSynthesisQuickPickItem[] = [
    {
        label: '$(combine) Preset Default Strategy',
        description: 'Automatic strategy from session/preset',
        detail: 'Synthesize completed mode outputs using the ensemble preset strategy',
        strategy: '',
        force: false,
    },
    {
        label: '$(check-all) Consensus',
        description: '--synth-strategy=consensus',
        detail: 'Merge convergent findings and surface shared high-confidence conclusions',
        strategy: 'consensus',
        force: false,
    },
    {
        label: '$(flame) Adversarial',
        description: '--synth-strategy=adversarial',
        detail: 'Prioritize contradictions, failure modes, and counter-evidence across modes',
        strategy: 'adversarial',
        force: false,
    },
    {
        label: '$(git-compare) Dialectical',
        description: '--synth-strategy=dialectical',
        detail: 'Synthesize thesis, antithesis, and resolved trade-offs across viewpoints',
        strategy: 'dialectical',
        force: false,
    },
    {
        label: '$(warning) Force Partial Synthesis',
        description: '--synth-force',
        detail: 'Synthesize available mode outputs even if some modes are still pending or working',
        strategy: '',
        force: true,
    },
];

export interface AttentionQuickPickItem extends vscode.QuickPickItem {
    item: AttentionItem;
}

export interface AttentionActionQuickPickItem extends vscode.QuickPickItem {
    action: AttentionMutationAction;
    duration?: string;
}

export interface PaletteActionQuickPickItem extends vscode.QuickPickItem {
    actionId:
        | 'terminalPalette'
        | 'spawnEnsemble'
        | 'synthesizeEnsemble'
        | 'triageAttention'
        | 'triageIncidents'
        | 'handoffSession'
        | 'showAnalytics'
        | 'showAudit'
        | 'generateSupportBundle'
        | 'runDoctor'
        | 'preflightPrompt'
        | 'assignWork'
        | 'scaleSession'
        | 'rebalanceSession'
        | 'reviewQueue'
        | 'showSummary'
        | 'openWebCockpit'
        | 'showBackpressure'
        | 'manageWorktrees'
        | 'dashboard'
        | 'spawn'
        | 'sendPrompt'
        | 'showStatus';
    command?: string;
}

export const PALETTE_ACTION_OPTIONS: readonly PaletteActionQuickPickItem[] = [
    {
        label: '$(terminal) Open Session Palette (ntm palette)',
        description: 'Interactive TUI command palette in terminal',
        actionId: 'terminalPalette',
    },
    {
        label: '$(sparkle) Spawn Reasoning Ensemble',
        description: 'NTM: Spawn Reasoning Ensemble',
        detail: 'Suggest preset for a question and launch a multi-agent reasoning ensemble',
        actionId: 'spawnEnsemble',
        command: 'ntm.spawnEnsemble',
    },
    {
        label: '$(combine) Synthesize Reasoning Ensemble',
        description: 'NTM: Synthesize Reasoning Ensemble',
        detail: 'Collect completed mode outputs and synthesize findings, audit conflicts, and recommendations',
        actionId: 'synthesizeEnsemble',
        command: 'ntm.synthesizeEnsemble',
    },
    {
        label: '$(bell) Triage Attention Feed',
        description: 'NTM: Triage Attention Feed',
        detail: 'Inspect pending attention items and acknowledge, pin, snooze, or dismiss',
        actionId: 'triageAttention',
        command: 'ntm.triageAttention',
    },
    {
        label: '$(flame) Triage Operator Incidents',
        description: 'NTM: Triage Operator Incidents',
        detail: 'Inspect durable escalated incidents and investigate, resolve, or mute',
        actionId: 'triageIncidents',
        command: 'ntm.triageIncidents',
    },
    {
        label: '$(note) Session Handoff (Inspect / Create)',
        description: 'NTM: Session Handoff (Inspect / Create)',
        detail: 'Inspect latest Goal/Now handoff quality report or persist manual/auto handoff',
        actionId: 'handoffSession',
        command: 'ntm.handoffSession',
    },
    {
        label: '$(graph) Show Swarm & Harness Analytics',
        description: 'NTM: Show Swarm & Harness Analytics',
        detail: 'Inspect multi-harness spawns, prompts, token estimates, crashes, and session activity',
        actionId: 'showAnalytics',
        command: 'ntm.showAnalytics',
    },
    {
        label: '$(shield) Inspect & Verify Audit Logs',
        description: 'NTM: Inspect & Verify Audit Logs',
        detail: 'Inspect session audit logs, search entries, verify tamper-evident hash chains, or check source health',
        actionId: 'showAudit',
        command: 'ntm.showAudit',
    },
    {
        label: '$(package) Generate Redacted Support Bundle',
        description: 'NTM: Generate Redacted Support Bundle',
        detail: 'Package disclosure-redacted diagnostic state, configs, and scrollback into a zip or tar.gz bundle',
        actionId: 'generateSupportBundle',
        command: 'ntm.generateSupportBundle',
    },
    {
        label: '$(stethoscope) Run Ecosystem Doctor',
        description: 'NTM: Run Ecosystem Doctor',
        detail: 'Inspect required CLI tools, daemons, invariants, safety defaults, and SQLite/Beads lock contention',
        actionId: 'runDoctor',
        command: 'ntm.runDoctor',
    },
    {
        label: '$(verified) Preflight Prompt / Selection',
        description: 'NTM: Preflight Prompt / Selection',
        detail: 'Lint editor selection or prompt text for token footprint, DCG guardrails, and safety risks',
        actionId: 'preflightPrompt',
        command: 'ntm.preflightPrompt',
    },
    {
        label: '$(tasklist) Plan & Assign Beads Work',
        description: 'NTM: Plan & Assign Beads Work',
        detail: 'Score ready Beads issues against idle agents by strategy and optionally dispatch assignments',
        actionId: 'assignWork',
        command: 'ntm.assignWork',
    },
    {
        label: '$(layers) Scale Session Agents (11 Harnesses)',
        description: 'NTM: Scale Session Agents',
        detail: 'Preview or execute multi-harness agent scaling across cc, cod, gmi, agy, ollama, cursor, windsurf, aider, grok, omp, and oc',
        actionId: 'scaleSession',
        command: 'ntm.scaleSession',
    },
    {
        label: '$(sync) Rebalance Session Workload',
        description: 'NTM: Rebalance Session Workload',
        detail: 'Compute imbalance score and rebalance assigned Beads or pane geometry across agents',
        actionId: 'rebalanceSession',
        command: 'ntm.rebalanceSession',
    },
    {
        label: '$(checklist) Peer Review Queue',
        description: 'NTM: Peer Review Queue',
        detail: 'Generate or dispatch cross-agent peer review prompts for idle session panes',
        actionId: 'reviewQueue',
        command: 'ntm.reviewQueue',
    },
    {
        label: '$(output) Show Session Activity Summary',
        description: 'NTM: Show Session Activity Summary',
        detail: 'Summarize recent agent output, modified files, commands run, and detected file conflicts',
        actionId: 'showSummary',
        command: 'ntm.showSummary',
    },
    {
        label: '$(pulse) Show Swarm Backpressure',
        description: 'NTM: Show Swarm Backpressure',
        detail: 'Inspect queue utilization, load-shedding regime, and per-session overload buckets',
        actionId: 'showBackpressure',
        command: 'ntm.showBackpressure',
    },
    {
        label: '$(git-branch) Manage Agent Worktrees',
        description: 'NTM: Manage Agent Worktrees',
        detail: 'Inspect isolated agent worktrees, create new checkouts, or merge back',
        actionId: 'manageWorktrees',
        command: 'ntm.manageWorktrees',
    },
    {
        label: '$(globe) Open Web Cockpit (ntm web)',
        description: 'NTM: Open Web Cockpit',
        detail: 'Start or reuse the local NTM web dashboard server and open it in browser',
        actionId: 'openWebCockpit',
        command: 'ntm.openWebCockpit',
    },
    {
        label: '$(dashboard) Open Dashboard',
        description: 'NTM: Open Dashboard',
        actionId: 'dashboard',
        command: 'ntm.dashboard',
    },
    {
        label: '$(add) Spawn Agents for Project',
        description: 'NTM: Spawn Agents for Project',
        actionId: 'spawn',
        command: 'ntm.spawn',
    },
    {
        label: '$(comment-discussion) Send Prompt to Agent',
        description: 'NTM: Send Prompt to Agent',
        actionId: 'sendPrompt',
        command: 'ntm.sendPrompt',
    },
    {
        label: '$(list-unordered) Show Session Status',
        description: 'NTM: Show Session Status',
        actionId: 'showStatus',
        command: 'ntm.showStatus',
    },
];

/**
 * Build QuickPick items for ensemble presets, promoting any suggested preset to index 0
 * with a `$(sparkle) Recommended: ...` badge followed by all remaining presets.
 */
export function buildEnsemblePresetQuickPickItems(
    presets: readonly EnsemblePreset[],
    suggestion?: EnsembleSuggestResult
): EnsemblePresetQuickPickItem[] {
    const items: EnsemblePresetQuickPickItem[] = [];
    const topPick = suggestion?.top_pick
        ?? (suggestion?.recommended_preset
            ? {
                preset_name: suggestion.recommended_preset,
                display_name: suggestion.recommended_preset,
                description: '',
                score: 0,
                reasons: [],
                mode_count: suggestion.recommended_modes?.length ?? 0,
                modes: suggestion.recommended_modes,
                tags: [],
            }
            : undefined);

    let recommendedName: string | undefined;
    if (topPick && topPick.preset_name) {
        recommendedName = topPick.preset_name;
        const matched = presets.find(p => p.name === topPick.preset_name);
        const displayName = topPick.display_name || matched?.display_name || topPick.preset_name;
        const modeCount = matched?.mode_count ?? topPick.mode_count;
        const descParts = [
            topPick.preset_name,
            modeCount > 0 ? `${modeCount} modes` : undefined,
            matched?.category,
        ].filter(Boolean);
        const detailParts = [
            topPick.description || matched?.description,
            topPick.reasons.length > 0 ? `Why: ${topPick.reasons.join(', ')}` : undefined,
        ].filter(Boolean);

        items.push({
            label: `$(sparkle) Recommended: ${displayName}`,
            description: descParts.join(' · '),
            detail: detailParts.length > 0 ? detailParts.join(' — ') : undefined,
            presetName: topPick.preset_name,
            recommended: true,
            picked: true,
        });
    }

    for (const preset of presets) {
        if (recommendedName && preset.name === recommendedName) {
            continue;
        }
        const descParts = [
            preset.name,
            preset.mode_count > 0 ? `${preset.mode_count} modes` : undefined,
            preset.category,
        ].filter(Boolean);
        items.push({
            label: preset.display_name || preset.name,
            description: descParts.join(' · '),
            detail: preset.description || undefined,
            presetName: preset.name,
            recommended: false,
        });
    }

    return items;
}

/**
 * Derive a clean tmux-safe default session name (`<workspace>-ensemble`).
 */
export function defaultEnsembleSessionName(workspaceName?: string, workspaceFolderPath?: string): string {
    const rawBase = (workspaceName ?? '').trim()
        || (workspaceFolderPath ? path.basename(workspaceFolderPath).trim() : '');
    const sanitized = rawBase
        .replace(/[:.\s/\\]+/g, '-')
        .replace(/[^A-Za-z0-9_-]/g, '-')
        .replace(/-+/g, '-')
        .replace(/^-|-$/g, '');
    const base = sanitized || 'ntm';
    return base.endsWith('-ensemble') ? base : `${base}-ensemble`;
}

/**
 * Map attention item severity to a VS Code codicon.
 */
export function attentionSeverityIcon(severity?: string): string {
    switch ((severity ?? '').toLowerCase()) {
        case 'critical':
        case 'error':
            return '$(error)';
        case 'warning':
        case 'warn':
        case 'high':
            return '$(warning)';
        default:
            return '$(info)';
    }
}

/**
 * Format surfaced attention items for QuickPick triage.
 */
export function buildAttentionQuickPickItems(items: readonly AttentionItem[]): AttentionQuickPickItem[] {
    return items.map(item => {
        const icon = attentionSeverityIcon(item.severity);
        const stateBadge = item.pinned ? `pinned · ${item.operator_state}` : item.operator_state;
        const descParts = [
            item.session ?? 'global',
            item.pane ? `pane ${item.pane}` : undefined,
        ].filter(Boolean);
        const detailParts = [
            item.category,
            item.type,
            item.action_required ? 'action-required' : item.actionability,
        ].filter(Boolean);

        return {
            label: `${icon} #${item.cursor} [${stateBadge}] ${item.summary}`,
            description: descParts.join(' · '),
            detail: detailParts.join(' • '),
            item,
        };
    });
}

/**
 * Build operator mutation actions for a selected attention item.
 */
export function buildAttentionActionQuickPickItems(item?: AttentionItem): AttentionActionQuickPickItem[] {
    const actions: AttentionActionQuickPickItem[] = [
        {
            label: '$(check) Acknowledge',
            description: 'ack',
            detail: 'Mark this attention item as acknowledged',
            action: 'ack',
        },
        {
            label: '$(pin) Pin',
            description: 'pin',
            detail: 'Keep this attention item elevated in the feed',
            action: 'pin',
        },
    ];

    if (item?.pinned) {
        actions.push({
            label: '$(pinned) Unpin',
            description: 'unpin',
            detail: 'Remove pin from this attention item',
            action: 'unpin',
        });
    }

    actions.push(
        {
            label: '$(clock) Snooze 30m',
            description: 'snooze (30m)',
            detail: 'Hide this attention item for 30 minutes',
            action: 'snooze',
            duration: '30m',
        },
        {
            label: '$(trash) Dismiss',
            description: 'dismiss',
            detail: 'Dismiss this attention item from the active feed',
            action: 'dismiss',
        }
    );

    return actions;
}

export interface BackpressureQuickPickItem extends vscode.QuickPickItem {
    itemAction:
        | 'summary'
        | 'shedPolicy'
        | 'session'
        | 'openSwarmCockpit'
        | 'runBackpressureHeatmap';
    sessionName?: string;
}

export interface WorktreeQuickPickItem extends vscode.QuickPickItem {
    itemAction:
        | 'worktree'
        | 'createWorktree'
        | 'mergeWorktree'
        | 'openWorktreesCockpit';
    worktree?: WorktreeInfo;
}

export interface IncidentQuickPickItem extends vscode.QuickPickItem {
    itemAction: 'incident' | 'openAttentionCockpit';
    incident?: IncidentItem;
}

export interface IncidentActionQuickPickItem extends vscode.QuickPickItem {
    action: IncidentAction | 'openAttentionCockpit';
}

export interface HandoffModeQuickPickItem extends vscode.QuickPickItem {
    mode: 'inspect' | 'createManual' | 'createAuto' | 'openSessionsCockpit';
}

export const HANDOFF_MODE_OPTIONS: readonly HandoffModeQuickPickItem[] = [
    {
        label: '$(eye) Inspect Latest Handoff & Quality Score',
        description: '--robot-handoff=<session>',
        detail: 'Open a Markdown report with Goal, Now, quality dimensions, and continuity ledger status',
        mode: 'inspect',
    },
    {
        label: '$(edit) Create Manual Goal/Now Handoff',
        description: '--handoff-create --handoff-goal=... --handoff-now=...',
        detail: 'Record accomplished Goal and next Now action with automatic quality scoring',
        mode: 'createManual',
    },
    {
        label: '$(sparkle) Auto-Generate Handoff Snapshot',
        description: '--handoff-create --handoff-auto',
        detail: 'Capture an automatic handoff snapshot from session output, git, and beads state',
        mode: 'createAuto',
    },
    {
        label: '$(globe) Open /sessions in Web Cockpit',
        description: 'Web Cockpit (/sessions)',
        detail: 'Inspect and manage session handoffs and checkpoints in browser',
        mode: 'openSessionsCockpit',
    },
];

export function buildIncidentQuickPickItems(incidents: readonly IncidentItem[]): IncidentQuickPickItem[] {
    const items: IncidentQuickPickItem[] = incidents.map(inc => {
        const icon = attentionSeverityIcon(inc.severity);
        const descParts = [
            inc.id,
            inc.session_names || 'global',
            `${inc.alert_count} alerts · ${inc.event_count} events`,
        ].filter(Boolean);
        const detailParts = [
            `status: ${inc.status}`,
            inc.category && inc.family ? `${inc.category}/${inc.family}` : (inc.category || inc.family),
            inc.notes,
        ].filter(Boolean);
        return {
            label: `${icon} [${inc.status}] ${inc.title}`,
            description: descParts.join(' · '),
            detail: detailParts.join(' • '),
            itemAction: 'incident',
            incident: inc,
        };
    });
    items.push({
        label: '$(globe) Open /attention in Web Cockpit',
        description: 'Web Cockpit (/attention)',
        detail: 'Inspect durable incident timelines and attention feed in browser',
        itemAction: 'openAttentionCockpit',
    });
    return items;
}

export function buildIncidentActionQuickPickItems(inc: IncidentItem): IncidentActionQuickPickItem[] {
    const actions: IncidentActionQuickPickItem[] = [];
    if (inc.status !== 'investigating' && inc.status !== 'resolved') {
        actions.push({
            label: '$(search) Investigate Incident',
            description: `ntm attention incidents investigate ${inc.id}`,
            detail: 'Mark this incident as actively under investigation',
            action: 'investigate',
        });
    }
    if (inc.status !== 'resolved') {
        actions.push({
            label: '$(check) Resolve Incident',
            description: `ntm attention incidents resolve ${inc.id}`,
            detail: 'Resolve this incident with an optional operator resolution note',
            action: 'resolve',
        });
    }
    if (inc.status !== 'muted' && inc.status !== 'resolved') {
        actions.push({
            label: '$(mute) Mute Incident',
            description: `ntm attention incidents mute ${inc.id}`,
            detail: 'Mute recurring alert escalation for this incident fingerprint',
            action: 'mute',
        });
    }
    actions.push({
        label: '$(globe) Open /attention in Web Cockpit',
        description: 'Web Cockpit (/attention)',
        detail: 'View linked incident events in browser',
        action: 'openAttentionCockpit',
    });
    return actions;
}

export function formatHandoffMarkdown(res: HandoffResult): string {
    const lines: string[] = [
        `# Session Handoff: ${res.session || 'all'}`,
        '',
    ];
    if (res.quality) {
        lines.push(`- **Quality Score:** ${res.quality.score}/100 (\`${res.quality.status}\`)`);
        if (res.quality.dimensions.length > 0) {
            lines.push(
                `- **Dimensions:** ${res.quality.dimensions.map(d => `${d.name}=${d.score}`).join(', ')}`
            );
        }
        if (res.quality.hints.length > 0) {
            lines.push(`- **Hints:** ${res.quality.hints.join('; ')}`);
        }
    }
    if (res.ledger) {
        lines.push(
            `- **Continuity Ledger:** ${res.ledger.exists ? `${res.ledger.path} (${res.ledger.bytes} bytes)` : 'none'}`
        );
    }
    if (res.latest) {
        lines.push('', '## Latest Handoff', '');
        lines.push(`- **Goal:** ${res.latest.goal || '—'}`);
        lines.push(`- **Now:** ${res.latest.now || '—'}`);
        if (res.latest.status) {
            lines.push(`- **Status:** ${res.latest.status}`);
        }
    }
    if (res.handoffs.length > 0) {
        lines.push('', '## Recent Handoff History', '');
        for (const h of res.handoffs) {
            const autoTag = h.is_auto ? ' [auto]' : '';
            lines.push(`- \`${h.date}\` **${h.session}**${autoTag}: ${h.goal || h.path}`);
        }
    }
    lines.push('');
    return lines.join('\n');
}

export interface AnalyticsWindowQuickPickItem extends vscode.QuickPickItem {
    mode: 'window' | 'customSince' | 'openAnalyticsCockpit';
    days?: number;
}

export const ANALYTICS_WINDOW_OPTIONS: readonly AnalyticsWindowQuickPickItem[] = [
    {
        label: '$(graph) Last 7 Days',
        description: '--analytics-days=7 --analytics-sessions',
        detail: 'Inspect recent multi-harness spawns, prompts, token volume, crashes, and sessions',
        mode: 'window',
        days: 7,
    },
    {
        label: '$(calendar) Last 30 Days',
        description: '--analytics-days=30 --analytics-sessions',
        detail: 'Monthly swarm & harness activity report across all coding agents',
        mode: 'window',
        days: 30,
    },
    {
        label: '$(history) Last 90 Days',
        description: '--analytics-days=90 --analytics-sessions',
        detail: 'Quarterly multi-harness utilization and error summary',
        mode: 'window',
        days: 90,
    },
    {
        label: '$(filter) Since Specific Date (YYYY-MM-DD)...',
        description: '--analytics-since=<date> --analytics-sessions',
        detail: 'Aggregate event telemetry from an explicit YYYY-MM-DD or RFC3339 cutoff',
        mode: 'customSince',
    },
    {
        label: '$(globe) Open /analytics in Web Cockpit',
        description: 'Web Cockpit (/analytics)',
        detail: 'Open interactive multi-harness charts and Prometheus preview in browser',
        mode: 'openAnalyticsCockpit',
    },
];

export function formatAnalyticsMarkdown(res: AnalyticsResult): string {
    const lines: string[] = [
        `# NTM Swarm & Harness Analytics (${res.period})`,
        '',
        `- **Since:** ${res.since || '—'}`,
        `- **Total Sessions:** ${res.total_sessions}`,
        `- **Total Agents Spawned:** ${res.total_agents}`,
        `- **Total Prompts Sent:** ${res.total_prompts} (${res.total_chars_sent} chars)`,
        `- **Estimated Tokens:** ${res.total_tokens_estimated}`,
        `- **Agent Crashes / Restarts:** ${res.agent_crashes} crashes · ${res.agent_restarts} restarts`,
        `- **Errors:** ${res.error_count}`,
    ];

    const harnessEntries = Object.entries(res.agent_breakdown).sort((a, b) => {
        if (b[1].tokens_estimated !== a[1].tokens_estimated) {
            return b[1].tokens_estimated - a[1].tokens_estimated;
        }
        if (b[1].prompts !== a[1].prompts) {
            return b[1].prompts - a[1].prompts;
        }
        if (b[1].count !== a[1].count) {
            return b[1].count - a[1].count;
        }
        return a[0].localeCompare(b[0]);
    });

    lines.push('', '## Multi-Harness Breakdown', '');
    if (harnessEntries.length === 0) {
        lines.push('No harness activity recorded in this window.');
    } else {
        lines.push('| Harness | Agents | Prompts | Chars Sent | Est. Tokens |');
        lines.push('|---|---:|---:|---:|---:|');
        for (const [harness, st] of harnessEntries) {
            lines.push(
                `| \`${harness}\` | ${st.count} | ${st.prompts} | ${st.chars_sent} | ${st.tokens_estimated} |`
            );
        }
    }

    const errorEntries = Object.entries(res.error_types).sort(
        (a, b) => b[1] - a[1] || a[0].localeCompare(b[0])
    );
    if (errorEntries.length > 0) {
        lines.push('', '## Error Breakdown', '');
        for (const [errType, count] of errorEntries) {
            lines.push(`- \`${errType}\`: ${count}`);
        }
    }

    if (res.sessions.length > 0) {
        lines.push('', '## Session Activity', '');
        for (const s of res.sessions) {
            lines.push(
                `- **${s.name}** — ${s.agent_count} agents · ${s.prompt_count} prompts${s.created_at ? ` (${s.created_at})` : ''}`
            );
        }
    }

    lines.push('');
    return lines.join('\n');
}

export interface AuditQuickPickOption extends vscode.QuickPickItem {
    mode: 'list' | 'showSession' | 'search' | 'verify' | 'sources' | 'openSafetyCockpit';
}

export const AUDIT_MODE_OPTIONS: readonly AuditQuickPickOption[] = [
    {
        label: '$(list-unordered) List Session Audit Logs',
        description: '--robot-audit',
        detail: 'List all tamper-evident JSONL audit log files across NTM sessions',
        mode: 'list',
    },
    {
        label: '$(eye) Inspect Session Audit Entries',
        description: '--robot-audit=<session>',
        detail: 'Inspect chronological audit entries (spawns, prompts, commands, errors) for a session',
        mode: 'showSession',
    },
    {
        label: '$(search) Search Audit Logs (Regex)...',
        description: '--robot-audit --audit-search=<pattern>',
        detail: 'Search across session audit logs with a regular expression',
        mode: 'search',
    },
    {
        label: '$(shield) Verify Session Hash-Chain Integrity',
        description: '--robot-audit=<session> --audit-verify',
        detail: 'Verify SHA-256 hash chains, sequence continuity, and entry checksums for tampering',
        mode: 'verify',
    },
    {
        label: '$(ServerProcess) Check Projection Source Health',
        description: '--robot-audit --audit-sources',
        detail: 'Inspect state.db runtime projection data-source availability and health',
        mode: 'sources',
    },
    {
        label: '$(globe) Open /safety Audit & Policy Cockpit',
        description: 'Web Cockpit (/safety)',
        detail: 'Open interactive HTTP/WS audit query, chain verifier, and automation policy cockpit in browser',
        mode: 'openSafetyCockpit',
    },
];

export interface SupportBundleFormatQuickPickOption extends vscode.QuickPickItem {
    format: 'zip' | 'tar.gz';
    redactMode: 'redact' | 'block';
}

export const SUPPORT_BUNDLE_FORMAT_OPTIONS: readonly SupportBundleFormatQuickPickOption[] = [
    {
        label: '$(package) ZIP Archive (Redact Secrets)',
        description: '--bundle-format=zip --bundle-redact=redact',
        detail: 'Recommended: scrub API keys, tokens, and PII into a portable .zip diagnostic archive',
        format: 'zip',
        redactMode: 'redact',
    },
    {
        label: '$(file-zip) tar.gz Archive (Redact Secrets)',
        description: '--bundle-format=tar.gz --bundle-redact=redact',
        detail: 'Scrub API keys, tokens, and PII into a gzipped tarball (.tar.gz)',
        format: 'tar.gz',
        redactMode: 'redact',
    },
    {
        label: '$(lock) ZIP Archive (Strict Block on Secrets)',
        description: '--bundle-format=zip --bundle-redact=block',
        detail: 'Fail closed if any secret pattern is detected instead of redacting in place',
        format: 'zip',
        redactMode: 'block',
    },
];

export function formatAuditMarkdown(res: AuditResult): string {
    const lines: string[] = [
        `# NTM Audit Report (${res.action} — session: ${res.session || 'all'})`,
        '',
        `- **Action:** \`${res.action}\``,
        `- **Session:** \`${res.session || 'all'}\``,
        `- **Matches:** ${res.count} shown of ${res.total_matches}${res.has_more ? ' (more available)' : ''}`,
    ];
    if (typeof res.verified === 'boolean') {
        lines.push(`- **Hash-Chain Integrity:** ${res.verified ? 'VERIFIED (INTACT)' : 'FAILED (TAMPERED / BROKEN)'}`);
    }
    if (res.error) {
        lines.push(`- **Diagnostic:** ${res.error}`);
    }

    if (res.files.length > 0) {
        lines.push('', '## Hash-Chain Verification Files', '');
        lines.push('| File | Status | Verified | Error |');
        lines.push('|---|---|---|---|');
        for (const f of res.files) {
            lines.push(
                `| \`${f.file}\` | \`${f.status}\` | ${f.verified ? 'yes' : 'no'} | ${f.error ?? '—'} |`
            );
        }
    }

    if (res.logs.length > 0) {
        lines.push('', '## Audit Log Files', '');
        lines.push('| File | Session | Size (bytes) | Modified |');
        lines.push('|---|---|---:|---|');
        for (const l of res.logs) {
            lines.push(
                `| \`${l.file}\` | \`${l.session ?? '—'}\` | ${l.size_bytes} | ${l.modified_at || '—'} |`
            );
        }
    }

    if (res.entries.length > 0) {
        lines.push('', '## Audit Entries', '');
        for (const e of res.entries) {
            lines.push(
                `- \`#${e.sequence_num}\` **${e.event_type}** (${e.actor} → \`${e.target}\`) at ${e.timestamp || '—'}`
            );
        }
    }

    if (res.sources.length > 0) {
        lines.push('', '## Projection Data Sources', '');
        lines.push('| Source | Available | Healthy | Reason |');
        lines.push('|---|---|---|---|');
        for (const s of res.sources) {
            lines.push(
                `| \`${s.source_name}\` | ${s.available ? 'yes' : 'no'} | ${s.healthy ? 'yes' : 'no'} | ${s.reason ?? '—'} |`
            );
        }
    }

    lines.push('');
    return lines.join('\n');
}

/**
 * Map normalized backpressure level to a VS Code codicon.
 */
export function backpressureLevelIcon(level: BackpressureLevel): string {
    switch (level) {
        case 'overload':
            return '$(error)';
        case 'critical':
            return '$(warning)';
        case 'elevated':
            return '$(pulse)';
        default:
            return '$(pass)';
    }
}

/**
 * Build QuickPick items summarizing overall swarm backpressure, queue utilization,
 * load-shedding contract, per-session overload buckets, and cockpit/CLI actions.
 */
export function buildBackpressureQuickPickItems(
    result: BackpressureResult
): BackpressureQuickPickItem[] {
    const utilPct = `${Math.round(result.utilization * 100)}%`;
    const queueLabel = result.queue_capacity > 0
        ? `${result.queue_depth}/${result.queue_capacity}`
        : String(result.queue_depth);
    const summaryDescParts = [
        `Utilization ${utilPct}`,
        `Queue ${queueLabel}`,
        result.dropped_count > 0 ? `${result.dropped_count} dropped` : undefined,
    ].filter(Boolean);

    const items: BackpressureQuickPickItem[] = [
        {
            label: `${backpressureLevelIcon(result.level)} Overall Pressure: ${result.level.toUpperCase()} (${result.regime})`,
            description: summaryDescParts.join(' · '),
            detail: result.reason_codes.length > 0
                ? `Reasons: ${result.reason_codes.join(', ')}`
                : 'All monitored transport surfaces nominal',
            itemAction: 'summary',
        },
        {
            label: `$(shield) Load Shedding: ${result.shed_policy.decision}`,
            description: [
                `Action: ${result.shed_policy.recommended_action}`,
                result.shed_policy.degraded_surface
                    ? `Surface: ${result.shed_policy.degraded_surface}`
                    : undefined,
            ]
                .filter(Boolean)
                .join(' · '),
            detail: [
                result.shed_policy.retry_after_ms !== undefined
                    ? `retry_after=${result.shed_policy.retry_after_ms}ms`
                    : undefined,
                result.shed_policy.reason_codes.length > 0
                    ? `Reasons: ${result.shed_policy.reason_codes.join(', ')}`
                    : undefined,
            ]
                .filter(Boolean)
                .join(' · ') || 'No active load shedding',
            itemAction: 'shedPolicy',
        },
    ];

    for (const sess of result.sessions) {
        const sessUtilPct = `${Math.round(sess.max_utilization * 100)}%`;
        const sessQueue = sess.queue_capacity > 0
            ? `${sess.queue_depth}/${sess.queue_capacity}`
            : String(sess.queue_depth);
        const sessDescParts = [
            `${sessUtilPct} util`,
            `queue ${sessQueue}`,
            sess.dropped_count > 0 ? `${sess.dropped_count} dropped` : undefined,
        ].filter(Boolean);
        const sessDetailParts = [
            sess.hot_surfaces.length > 0 ? `Surfaces: ${sess.hot_surfaces.join(', ')}` : undefined,
            sess.reason_codes.length > 0 ? `Reasons: ${sess.reason_codes.join(', ')}` : undefined,
            sess.recommended_action ? `Action: ${sess.recommended_action}` : undefined,
        ].filter(Boolean);

        items.push({
            label: `${backpressureLevelIcon(sess.level)} Session: ${sess.session} [${sess.level}]`,
            description: sessDescParts.join(' · '),
            detail: sessDetailParts.join(' · ') || `Decision: ${sess.decision}`,
            itemAction: 'session',
            sessionName: sess.session,
        });
    }

    items.push(
        {
            label: '$(globe) Open /swarm in Web Cockpit',
            description: 'Web Cockpit (/swarm)',
            detail: 'Inspect live overload heatmap and run synthetic scale scenarios',
            itemAction: 'openSwarmCockpit',
        },
        {
            label: '$(terminal) Run ntm health --backpressure --heatmap',
            description: 'Integrated Terminal',
            detail: 'Print live backpressure & heatmap report in terminal',
            itemAction: 'runBackpressureHeatmap',
        }
    );

    return items;
}

/**
 * Build QuickPick items displaying active agent worktrees alongside create, merge,
 * and Web Cockpit actions.
 */
export function buildWorktreeQuickPickItems(
    result: WorktreeListResult
): WorktreeQuickPickItem[] {
    const items: WorktreeQuickPickItem[] = result.worktrees.map(wt => {
        const icon = wt.status === 'error'
            ? '$(error)'
            : (wt.status === 'pending' ? '$(clock)' : '$(git-branch)');
        const detailParts = [
            wt.path,
            wt.session_id ? `session: ${wt.session_id}` : undefined,
            wt.error ? `error: ${wt.error}` : undefined,
        ].filter(Boolean);

        return {
            label: `${icon} ${wt.agent_name}`,
            description: `${wt.branch_name || 'detached'} · ${wt.status}`,
            detail: detailParts.join(' · '),
            itemAction: 'worktree',
            worktree: wt,
        };
    });

    items.push(
        {
            label: '$(add) Create Worktree for Agent',
            description: 'ntm worktrees create <agent>',
            detail: 'Provision a new isolated Git worktree for an agent',
            itemAction: 'createWorktree',
        },
        {
            label: '$(git-merge) Merge Worktree',
            description: 'ntm worktrees merge <agent>',
            detail: 'Merge an agent worktree branch back into main',
            itemAction: 'mergeWorktree',
        },
        {
            label: '$(globe) Open /worktrees in Web Cockpit',
            description: 'Web Cockpit (/worktrees)',
            detail: 'Preview 3-way merge conflicts and manage worktrees in browser',
            itemAction: 'openWorktreesCockpit',
        }
    );

    return items;
}

/**
 * Parse custom whitespace-separated CLI flags while honoring single/double quotes.
 */
export function parseCustomSpawnFlags(raw: string): string[] {
    const tokens: string[] = [];
    let current = '';
    let quote: "'" | '"' | null = null;

    for (let i = 0; i < raw.length; i++) {
        const ch = raw[i];
        if (quote) {
            if (ch === quote) {
                quote = null;
            } else {
                current += ch;
            }
        } else if (ch === "'" || ch === '"') {
            quote = ch;
        } else if (/\s/.test(ch)) {
            if (current.length > 0) {
                tokens.push(current);
                current = '';
            }
        } else {
            current += ch;
        }
    }
    if (current.length > 0) {
        tokens.push(current);
    }
    return tokens;
}

/**
 * Resolve selected QuickPick target(s) into canonical `NtmClient.send` target IDs.
 */
export function resolveSendTargetsFromSelection(
    selection: SendTargetQuickPickItem | SendTargetQuickPickItem[] | string | undefined
): Array<SendHarness | 'all'> {
    if (!selection) {
        return [];
    }
    if (Array.isArray(selection)) {
        if (selection.some(item => item.target === 'all')) {
            return ['all'];
        }
        return selection.map(item => item.target);
    }
    if (typeof selection !== 'string') {
        return [selection.target];
    }
    if (selection.includes('--all')) {
        return ['all'];
    }
    for (const option of SEND_TARGET_OPTIONS) {
        if (option.target !== 'all' && selection.includes(`--${option.target}`)) {
            return [option.target];
        }
    }
    return [];
}

function extractSessionName(arg?: string | NtmTreeItem): string | undefined {
    if (!arg) {
        return undefined;
    }
    if (typeof arg === 'string') {
        return arg.trim() || undefined;
    }
    if (arg.itemType === 'session') {
        return (arg.data as SessionInfo | undefined)?.name ?? arg.sessionName;
    }
    if (arg.itemType === 'agent') {
        return arg.sessionName;
    }
    return undefined;
}

export function activate(context: vscode.ExtensionContext, clientOverride?: NtmClient) {
    const client = clientOverride ?? new NtmClient();
    const statusBarItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    statusBarItem.command = 'ntm.openPalette';
    context.subscriptions.push(statusBarItem);

    let currentSession: string | undefined;

    const { provider: fileDecorationProvider, disposable: fileDecorationDisposable } = registerFileDecorations(context, client);
    context.subscriptions.push(fileDecorationDisposable);

    const { provider: treeProvider } = registerSessionTreeView(context, client);

    client.checkAvailable().then(available => {
        if (available) {
            updateStatus(client, statusBarItem).then(name => {
                currentSession = name ?? currentSession;
            }).catch(() => undefined);
            const interval = setInterval(() => {
                updateStatus(client, statusBarItem).then(name => {
                    currentSession = name ?? currentSession;
                }).catch(() => undefined);
            }, 5000);
            context.subscriptions.push({ dispose: () => clearInterval(interval) });
        } else {
            statusBarItem.text = '$(error) NTM missing';
            statusBarItem.color = new vscode.ThemeColor('statusBarItem.errorForeground');
            statusBarItem.show();
        }
    }).catch(() => undefined);

    const resolveSessionInteractive = async (
        explicitSession: string | undefined,
        promptText: string
    ): Promise<string | undefined> => {
        if (explicitSession) {
            currentSession = explicitSession;
            return explicitSession;
        }
        let candidate = currentSession;
        if (!candidate) {
            const status = await client.getStatus().catch(() => undefined);
            if (status) {
                candidate = pickPrimarySession(status)?.name;
            }
        }
        const chosen = candidate ?? await vscode.window.showInputBox({ prompt: promptText });
        if (!chosen) {
            return undefined;
        }
        currentSession = chosen;
        return chosen;
    };

    const dispStatus = vscode.commands.registerCommand('ntm.showStatus', async () => {
        try {
            const status = await client.getStatus();
            const items = status.sessions.map(s => {
                const agentCount = s.agents?.length || s.agent_count || 0;
                const paneCount = s.panes ?? agentCount;
                return {
                    label: s.name,
                    description: `${agentCount} agents · ${paneCount} panes`,
                    detail: s.attached ? 'Attached' : 'Detached',
                };
            });

            const picked = await vscode.window.showQuickPick(items, { placeHolder: 'Active NTM Sessions' });
            if (picked) {
                currentSession = picked.label;
            }
        } catch (e) {
            vscode.window.showErrorMessage(`NTM Error: ${e}`);
        }
    });

    const dispOpenPalette = vscode.commands.registerCommand('ntm.openPalette', async (sessionArg?: string | NtmTreeItem) => {
        try {
            const picked = await vscode.window.showQuickPick(PALETTE_ACTION_OPTIONS, {
                placeHolder: 'Select an NTM action',
            });
            if (!picked) {
                return;
            }

            if (picked.actionId !== 'terminalPalette' && picked.command) {
                if (
                    (picked.actionId === 'sendPrompt' ||
                        picked.actionId === 'handoffSession' ||
                        picked.actionId === 'showAudit' ||
                        picked.actionId === 'generateSupportBundle' ||
                        picked.actionId === 'assignWork' ||
                        picked.actionId === 'scaleSession' ||
                        picked.actionId === 'rebalanceSession' ||
                        picked.actionId === 'reviewQueue' ||
                        picked.actionId === 'showSummary') &&
                    sessionArg
                ) {
                    await vscode.commands.executeCommand(picked.command, sessionArg);
                } else {
                    await vscode.commands.executeCommand(picked.command);
                }
                return;
            }

            const chosen = await resolveSessionInteractive(
                extractSessionName(sessionArg),
                'NTM session name for palette'
            );
            if (!chosen) {
                return;
            }

            const terminal = vscode.window.createTerminal({ name: `NTM Palette: ${chosen}` });
            terminal.show(true);
            terminal.sendText(buildNtmTerminalCommand(client.getBinaryPath(), 'palette', chosen), true);
        } catch (e) {
            vscode.window.showErrorMessage(`NTM palette error: ${e}`);
        }
    });

    const dispSpawn = vscode.commands.registerCommand('ntm.spawn', async () => {
        const session = await vscode.window.showInputBox({
            prompt: 'Session Name',
            value: vscode.workspace.name || '',
        });
        if (!session) {
            return;
        }

        const rawPicked = await vscode.window.showQuickPick(SPAWN_HARNESS_OPTIONS, {
            canPickMany: true,
            placeHolder: 'Select agent harness(es) to spawn (or Custom Preset / Flags)',
        });
        const pickedList = Array.isArray(rawPicked)
            ? rawPicked
            : (rawPicked ? [rawPicked] : []);
        if (pickedList.length === 0) {
            return;
        }

        let spawnArgs: string[] = [];
        if (pickedList.some(item => item.harness === 'custom')) {
            const customInput = await vscode.window.showInputBox({
                prompt: 'Enter spawn flags or preset (e.g. --cc=2 --cod=1 or --recipe=default)',
                value: '--cc=2',
            });
            if (!customInput) {
                return;
            }
            spawnArgs = parseCustomSpawnFlags(customInput);
            if (spawnArgs.length === 0) {
                return;
            }
        } else {
            for (const item of pickedList) {
                if (item.harness === 'custom') {
                    continue;
                }
                const countStr = await vscode.window.showInputBox({
                    prompt: `Number of ${item.label} agents to spawn`,
                    value: '2',
                    validateInput: value => {
                        const num = Number(value.trim());
                        return Number.isInteger(num) && num >= 1 && num <= 64
                            ? null
                            : 'Enter an integer between 1 and 64';
                    },
                });
                if (!countStr) {
                    return;
                }
                spawnArgs.push(`--${item.harness}=${countStr.trim()}`);
            }
        }

        try {
            await client.spawn(session, spawnArgs);
            currentSession = session;
            vscode.window.showInformationMessage(`Spawned session ${session} (${spawnArgs.join(' ')})`);
            updateStatus(client, statusBarItem);
            treeProvider.refresh();
            fileDecorationProvider.refresh();
        } catch (e) {
            vscode.window.showErrorMessage(`Spawn failed: ${e}`);
        }
    });

    const dispDashboard = vscode.commands.registerCommand('ntm.dashboard', () => {
        NtmDashboard.createOrShow(context.extensionUri, client);
    });

    const handleSend = async (content: string, explicitSession?: string) => {
        try {
            const chosenSession = await resolveSessionInteractive(explicitSession, 'NTM Session');
            if (!chosenSession) {
                return;
            }

            const pickedTarget = await vscode.window.showQuickPick(SEND_TARGET_OPTIONS, {
                placeHolder: 'Select Target Agents',
            });
            const targets = resolveSendTargetsFromSelection(pickedTarget);
            if (targets.length === 0) {
                return;
            }

            const instruction = await vscode.window.showInputBox({ prompt: 'Additional Instructions (optional)' });
            const fullPrompt = content
                ? content + (instruction ? `\nInstructions: ${instruction}` : '')
                : (instruction ?? '');
            if (!fullPrompt.trim()) {
                return;
            }

            await client.send(chosenSession, fullPrompt, targets);
            vscode.window.showInformationMessage(`Sent to ${chosenSession} (${targets.join(',')})`);
        } catch (e) {
            vscode.window.showErrorMessage(`Send failed: ${e}`);
        }
    };

    const dispSendPrompt = vscode.commands.registerCommand('ntm.sendPrompt', async (sessionArg?: string) => {
        try {
            const chosenSession = await resolveSessionInteractive(
                extractSessionName(sessionArg),
                'NTM Session'
            );
            if (!chosenSession) {
                return;
            }

            const pickedTarget = await vscode.window.showQuickPick(SEND_TARGET_OPTIONS, {
                placeHolder: 'Select Target Agents',
            });
            const targets = resolveSendTargetsFromSelection(pickedTarget);
            if (targets.length === 0) {
                return;
            }

            const prompt = await vscode.window.showInputBox({ prompt: `Prompt to send to ${chosenSession}` });
            if (!prompt || !prompt.trim()) {
                return;
            }

            await client.send(chosenSession, prompt, targets);
            vscode.window.showInformationMessage(`Sent to ${chosenSession} (${targets.join(',')})`);
        } catch (e) {
            vscode.window.showErrorMessage(`Send failed: ${e}`);
        }
    });

    const dispSendSelection = vscode.commands.registerCommand('ntm.sendSelection', async () => {
        const editor = vscode.window.activeTextEditor;
        if (!editor) {
            return;
        }

        const selection = editor.selection;
        const text = editor.document.getText(selection);
        if (!text) {
            vscode.window.showWarningMessage('No text selected');
            return;
        }

        const filePath = normalizeWorkspaceRelativePath(editor.document.uri);
        const prompt = `File: ${filePath}\n\`\`\`\n${text}\n\`\`\`\n`;
        await handleSend(prompt);
    });

    const dispSendFile = vscode.commands.registerCommand('ntm.sendCurrentFile', async () => {
        const editor = vscode.window.activeTextEditor;
        if (!editor) {
            return;
        }

        const text = editor.document.getText();
        const filePath = normalizeWorkspaceRelativePath(editor.document.uri);
        const prompt = `File: ${filePath}\n\`\`\`\n${text}\n\`\`\`\n`;
        await handleSend(prompt);
    });

    const dispOpenTerminal = vscode.commands.registerCommand('ntm.openTerminal', async (sessionArg?: string | NtmTreeItem) => {
        try {
            const chosenSession = await resolveSessionInteractive(
                extractSessionName(sessionArg),
                'NTM Session to attach'
            );
            if (!chosenSession) {
                return;
            }

            const terminal = vscode.window.createTerminal({ name: `NTM: ${chosenSession}` });
            terminal.show(true);
            terminal.sendText(buildNtmTerminalCommand(client.getBinaryPath(), 'attach', chosenSession), true);
        } catch (e) {
            vscode.window.showErrorMessage(`Failed to open terminal: ${e}`);
        }
    });

    const dispViewSession = vscode.commands.registerCommand('ntm.viewSession', async (sessionArg?: string | NtmTreeItem) => {
        try {
            const chosenSession = await resolveSessionInteractive(
                extractSessionName(sessionArg),
                'NTM Session to view'
            );
            if (!chosenSession) {
                return;
            }

            const terminal = vscode.window.createTerminal({ name: `NTM View: ${chosenSession}` });
            terminal.show(true);
            terminal.sendText(buildNtmTerminalCommand(client.getBinaryPath(), 'view', chosenSession), true);
        } catch (e) {
            vscode.window.showErrorMessage(`Failed to view session: ${e}`);
        }
    });

    const dispZoomPane = vscode.commands.registerCommand(
        'ntm.zoomPane',
        async (itemOrSession?: string | NtmTreeItem, paneArg?: string) => {
            try {
                let explicitPane = paneArg;
                if (itemOrSession && typeof itemOrSession !== 'string' && itemOrSession.itemType === 'agent') {
                    explicitPane = explicitPane ?? (itemOrSession.data as AgentInfo | undefined)?.pane;
                }

                const chosenSession = await resolveSessionInteractive(
                    extractSessionName(itemOrSession),
                    'NTM Session to zoom'
                );
                if (!chosenSession) {
                    return;
                }

                let chosenPane = explicitPane;
                if (!chosenPane) {
                    const status = await client.getStatus().catch(() => undefined);
                    const matchingSession = status?.sessions.find(s => s.name === chosenSession);
                    if (matchingSession?.agents && matchingSession.agents.length > 0) {
                        const panePick = await vscode.window.showQuickPick(
                            matchingSession.agents.map(a => ({
                                label: a.pane,
                                description: [a.type, a.variant, a.state].filter(Boolean).join(' • '),
                            })),
                            { placeHolder: `Select pane in ${chosenSession} to zoom` }
                        );
                        chosenPane = panePick?.label;
                    } else {
                        chosenPane = await vscode.window.showInputBox({
                            prompt: `Pane index or ID in ${chosenSession} to zoom`,
                            value: '1',
                        });
                    }
                }
                if (!chosenPane) {
                    return;
                }

                const terminal = vscode.window.createTerminal({ name: `NTM Zoom: ${chosenSession}:${chosenPane}` });
                terminal.show(true);
                terminal.sendText(
                    buildNtmTerminalCommand(client.getBinaryPath(), 'zoom', chosenSession, chosenPane),
                    true
                );
            } catch (e) {
                vscode.window.showErrorMessage(`Failed to zoom pane: ${e}`);
            }
        }
    );

    const dispTerminalDashboard = vscode.commands.registerCommand(
        'ntm.terminalDashboard',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const chosenSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'NTM Session for TUI Dashboard'
                );
                const terminalName = chosenSession ? `NTM Dashboard: ${chosenSession}` : 'NTM Dashboard';
                const terminal = vscode.window.createTerminal({ name: terminalName });
                terminal.show(true);
                const cmd = chosenSession
                    ? buildNtmTerminalCommand(client.getBinaryPath(), 'dashboard', chosenSession)
                    : buildNtmTerminalCommand(client.getBinaryPath(), 'dashboard');
                terminal.sendText(cmd, true);
            } catch (e) {
                vscode.window.showErrorMessage(`Failed to open TUI dashboard: ${e}`);
            }
        }
    );

    const dispSpawnEnsemble = vscode.commands.registerCommand('ntm.spawnEnsemble', async () => {
        try {
            const rawQuestion = await vscode.window.showInputBox({
                prompt: 'Problem or question for the reasoning ensemble',
                placeHolder: 'e.g. What security or concurrency issues exist in the auth pipeline?',
            });
            const question = (rawQuestion ?? '').trim();
            if (!question) {
                return;
            }

            const [suggestion, presets] = await Promise.all([
                client.suggestEnsemble(question).catch(() => undefined),
                client.getEnsemblePresets(),
            ]);

            const quickPickItems = buildEnsemblePresetQuickPickItems(presets, suggestion);
            if (quickPickItems.length === 0) {
                vscode.window.showWarningMessage('No ensemble presets available from NTM');
                return;
            }

            const pickedPreset = await vscode.window.showQuickPick(quickPickItems, {
                placeHolder: 'Select reasoning ensemble preset',
            });
            if (!pickedPreset) {
                return;
            }

            const defaultSession = defaultEnsembleSessionName(
                vscode.workspace.name,
                vscode.workspace.workspaceFolders?.[0]?.uri?.fsPath
            );
            const rawSession = await vscode.window.showInputBox({
                prompt: 'Ensemble session name',
                value: defaultSession,
            });
            const sessionName = (rawSession ?? '').trim();
            if (!sessionName) {
                return;
            }

            const spawnResult = await vscode.window.withProgress(
                {
                    location: vscode.ProgressLocation.Notification,
                    title: `Spawning NTM reasoning ensemble "${sessionName}" (${pickedPreset.presetName})...`,
                    cancellable: false,
                },
                async () => client.spawnEnsemble({
                    session: sessionName,
                    preset: pickedPreset.presetName,
                    question,
                    projectDir: client.getWorkspaceCwd(),
                })
            );

            const resolvedSession = spawnResult.session || sessionName;
            currentSession = resolvedSession;
            updateStatus(client, statusBarItem).catch(() => undefined);
            treeProvider.refresh();
            fileDecorationProvider.refresh();

            const modeSuffix = spawnResult.modes.length > 0 ? ` (${spawnResult.modes.length} modes)` : '';
            const nextAction = await vscode.window.showInformationMessage(
                `Spawned ensemble "${resolvedSession}" using preset "${pickedPreset.presetName}"${modeSuffix}`,
                'View Session',
                'Attach Terminal'
            );
            if (nextAction === 'View Session') {
                await vscode.commands.executeCommand('ntm.viewSession', resolvedSession);
            } else if (nextAction === 'Attach Terminal') {
                await vscode.commands.executeCommand('ntm.openTerminal', resolvedSession);
            }
        } catch (e) {
            vscode.window.showErrorMessage(`Ensemble spawn failed: ${e}`);
        }
    });

    const dispSynthesizeEnsemble = vscode.commands.registerCommand(
        'ntm.synthesizeEnsemble',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const chosenSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'Ensemble session name to synthesize'
                );
                if (!chosenSession) {
                    return;
                }

                const pickedStrategy = await vscode.window.showQuickPick(
                    ENSEMBLE_SYNTHESIS_STRATEGY_OPTIONS,
                    {
                        placeHolder: `Select synthesis strategy for ensemble "${chosenSession}"`,
                    }
                );
                if (!pickedStrategy) {
                    return;
                }

                const synthResult = await vscode.window.withProgress(
                    {
                        location: vscode.ProgressLocation.Notification,
                        title: `Synthesizing NTM reasoning ensemble "${chosenSession}"...`,
                        cancellable: false,
                    },
                    async () => client.synthesizeEnsemble({
                        session: chosenSession,
                        strategy: pickedStrategy.strategy || undefined,
                        format: 'markdown',
                        force: pickedStrategy.force,
                    })
                );

                const resolvedSession = synthResult.session || chosenSession;
                currentSession = resolvedSession;
                const strategyLabel = synthResult.report?.strategy
                    || pickedStrategy.strategy
                    || 'preset';
                const findingsCount = synthResult.report?.findings_count ?? 0;
                const conflictCount = synthResult.audit?.conflict_count ?? 0;

                const nextAction = await vscode.window.showInformationMessage(
                    `Synthesized ensemble "${resolvedSession}" (${strategyLabel}: ${findingsCount} findings, ${conflictCount} conflicts)`,
                    'Open Synthesis Report',
                    'Open /ensemble in Web Cockpit'
                );
                if (nextAction === 'Open Synthesis Report') {
                    const markdownBody = synthResult.formatted_output
                        || synthResult.report?.summary
                        || `# Ensemble Synthesis: ${resolvedSession}\n\nStatus: ${synthResult.status}\n`;
                    const doc = await vscode.workspace.openTextDocument({
                        content: markdownBody,
                        language: 'markdown',
                    });
                    await vscode.window.showTextDocument(doc);
                } else if (nextAction === 'Open /ensemble in Web Cockpit') {
                    await openWebCockpitRoute('/ensemble');
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Ensemble synthesis failed: ${e}`);
            }
        }
    );

    const dispTriageAttention = vscode.commands.registerCommand('ntm.triageAttention', async () => {
        try {
            const items = await client.listAttentionItems({ limit: 30 });
            if (items.length === 0) {
                vscode.window.showInformationMessage('No attention items pending');
                return;
            }

            const itemPicks = buildAttentionQuickPickItems(items);
            const pickedItem = await vscode.window.showQuickPick(itemPicks, {
                placeHolder: 'Select an attention item to triage',
            });
            if (!pickedItem) {
                return;
            }

            const actionPicks = buildAttentionActionQuickPickItems(pickedItem.item);
            const pickedAction = await vscode.window.showQuickPick(actionPicks, {
                placeHolder: `Choose action for attention item #${pickedItem.item.cursor}`,
            });
            if (!pickedAction) {
                return;
            }

            const result = await client.mutateAttentionItem(
                pickedAction.action,
                pickedItem.item.cursor,
                pickedAction.duration
            );
            updateStatus(client, statusBarItem).catch(() => undefined);
            vscode.window.showInformationMessage(
                `Attention item #${pickedItem.item.cursor} updated (${pickedAction.action} -> ${result.current_state})`
            );
        } catch (e) {
            vscode.window.showErrorMessage(`Attention triage failed: ${e}`);
        }
    });

    const webCockpitCommands = new WeakMap<vscode.Terminal, string>();
    const openWebCockpitRoute = async (routePath = ''): Promise<void> => {
        const config = vscode.workspace.getConfiguration('ntm');
        const port = normalizeWebPort(config.get<number>('webPort', DEFAULT_WEB_PORT));
        const cmd = buildWebCockpitCommand(client.getBinaryPath(), port);

        const openTerminals = vscode.window.terminals ?? [];
        let terminal = openTerminals.find(
            t => t.name === WEB_COCKPIT_TERMINAL_NAME && t.exitStatus === undefined
        );
        const isNewTerminal = !terminal;
        if (!terminal) {
            terminal = vscode.window.createTerminal({ name: WEB_COCKPIT_TERMINAL_NAME });
        }

        terminal.show(true);
        if (isNewTerminal || webCockpitCommands.get(terminal) !== cmd) {
            terminal.sendText(cmd, true);
            webCockpitCommands.set(terminal, cmd);
        }

        const suffix = routePath
            ? (routePath.startsWith('/') ? routePath : `/${routePath}`)
            : '';
        await vscode.env.openExternal(vscode.Uri.parse(`http://127.0.0.1:${port}${suffix}`));
    };

    const dispOpenWebCockpit = vscode.commands.registerCommand('ntm.openWebCockpit', async () => {
        try {
            await openWebCockpitRoute();
        } catch (e) {
            vscode.window.showErrorMessage(`Failed to open NTM Web Cockpit: ${e}`);
        }
    });

    const dispShowBackpressure = vscode.commands.registerCommand(
        'ntm.showBackpressure',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const explicitSession = extractSessionName(sessionArg);
                const bp = await client.getBackpressure(explicitSession);
                const picks = buildBackpressureQuickPickItems(bp);
                const picked = await vscode.window.showQuickPick(picks, {
                    placeHolder: 'NTM Swarm Backpressure & Load Shedding',
                });
                if (!picked) {
                    return;
                }

                if (picked.itemAction === 'openSwarmCockpit') {
                    await openWebCockpitRoute('/swarm');
                } else if (picked.itemAction === 'runBackpressureHeatmap') {
                    const terminal = vscode.window.createTerminal({ name: 'NTM Backpressure' });
                    terminal.show(true);
                    terminal.sendText(
                        buildNtmTerminalCommand(client.getBinaryPath(), [
                            'health',
                            '--backpressure',
                            '--heatmap',
                        ]),
                        true
                    );
                } else if (picked.itemAction === 'session' && picked.sessionName) {
                    currentSession = picked.sessionName;
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Failed to fetch swarm backpressure: ${e}`);
            }
        }
    );

    const runWorktreeMergeInTerminal = (agentName: string): void => {
        const trimmed = agentName.trim();
        if (!trimmed) {
            return;
        }
        const terminal = vscode.window.createTerminal({ name: `NTM Worktree Merge: ${trimmed}` });
        terminal.show(true);
        terminal.sendText(
            buildNtmTerminalCommand(client.getBinaryPath(), 'worktrees', 'merge', trimmed),
            true
        );
    };

    const dispManageWorktrees = vscode.commands.registerCommand(
        'ntm.manageWorktrees',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const explicitSession = extractSessionName(sessionArg);
                const result = await client.listWorktrees(explicitSession);
                const picks = buildWorktreeQuickPickItems(result);
                const picked = await vscode.window.showQuickPick(picks, {
                    placeHolder: result.worktrees.length > 0
                        ? `NTM Agent Worktrees (${result.worktrees.length} active)`
                        : 'No active agent worktrees — select an action',
                });
                if (!picked) {
                    return;
                }

                if (picked.itemAction === 'createWorktree') {
                    const rawAgent = await vscode.window.showInputBox({
                        prompt: 'Agent name for new isolated worktree',
                        placeHolder: 'e.g. BlueLake or cc_1',
                    });
                    const agentName = (rawAgent ?? '').trim();
                    if (!agentName) {
                        return;
                    }
                    const terminal = vscode.window.createTerminal({
                        name: `NTM Worktree: ${agentName}`,
                    });
                    terminal.show(true);
                    terminal.sendText(
                        buildNtmTerminalCommand(
                            client.getBinaryPath(),
                            'worktrees',
                            'create',
                            agentName
                        ),
                        true
                    );
                } else if (picked.itemAction === 'mergeWorktree') {
                    let agentName: string | undefined;
                    if (result.worktrees.length > 0) {
                        const wtChoices = result.worktrees.map(wt => ({
                            label: wt.agent_name,
                            description: wt.branch_name || undefined,
                            detail: wt.path || undefined,
                            agentName: wt.agent_name,
                        }));
                        const chosen = await vscode.window.showQuickPick(wtChoices, {
                            placeHolder: 'Select agent worktree to merge into main',
                        });
                        agentName = chosen?.agentName;
                    } else {
                        const rawAgent = await vscode.window.showInputBox({
                            prompt: 'Agent name for worktree merge',
                            placeHolder: 'e.g. BlueLake',
                        });
                        agentName = (rawAgent ?? '').trim() || undefined;
                    }
                    if (agentName) {
                        runWorktreeMergeInTerminal(agentName);
                    }
                } else if (picked.itemAction === 'worktree' && picked.worktree) {
                    const wt = picked.worktree;
                    const subAction = await vscode.window.showQuickPick(
                        [
                            {
                                label: `$(git-merge) Merge Worktree (${wt.agent_name})`,
                                description: `ntm worktrees merge ${wt.agent_name}`,
                                action: 'merge' as const,
                            },
                            {
                                label: '$(globe) Open /worktrees in Web Cockpit',
                                description: 'Web Cockpit (/worktrees)',
                                action: 'web' as const,
                            },
                        ],
                        { placeHolder: `Worktree ${wt.agent_name} (${wt.branch_name || wt.path})` }
                    );
                    if (subAction?.action === 'merge') {
                        runWorktreeMergeInTerminal(wt.agent_name);
                    } else if (subAction?.action === 'web') {
                        await openWebCockpitRoute('/worktrees');
                    }
                } else if (picked.itemAction === 'openWorktreesCockpit') {
                    await openWebCockpitRoute('/worktrees');
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Failed to manage worktrees: ${e}`);
            }
        }
    );

    const dispTriageIncidents = vscode.commands.registerCommand('ntm.triageIncidents', async () => {
        try {
            const incidents = await client.listIncidents({ limit: 30 });
            const picks = buildIncidentQuickPickItems(incidents);
            const picked = await vscode.window.showQuickPick(picks, {
                placeHolder: incidents.length > 0
                    ? `Select an operator incident to triage (${incidents.length} active)`
                    : 'No active operator incidents — open /attention in Web Cockpit',
            });
            if (!picked) {
                return;
            }
            if (picked.itemAction === 'openAttentionCockpit' || !picked.incident) {
                await openWebCockpitRoute('/attention');
                return;
            }

            const actionPick = await vscode.window.showQuickPick(
                buildIncidentActionQuickPickItems(picked.incident),
                { placeHolder: `Choose transition for incident ${picked.incident.id}` }
            );
            if (!actionPick) {
                return;
            }
            if (actionPick.action === 'openAttentionCockpit') {
                await openWebCockpitRoute('/attention');
                return;
            }

            const rawReason = await vscode.window.showInputBox({
                prompt: `Optional operator note for "${actionPick.action}" on ${picked.incident.id}`,
                placeHolder: 'Leave blank to transition without a note',
            });
            if (rawReason === undefined) {
                return;
            }

            const res = await client.mutateIncident(
                actionPick.action,
                picked.incident.id,
                rawReason.trim() || undefined
            );
            updateStatus(client, statusBarItem).catch(() => undefined);
            vscode.window.showInformationMessage(
                `Incident ${res.incident_id} updated (${res.action} -> ${res.status})`
            );
        } catch (e) {
            vscode.window.showErrorMessage(`Incident triage failed: ${e}`);
        }
    });

    const dispHandoffSession = vscode.commands.registerCommand(
        'ntm.handoffSession',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const chosenSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'NTM Session for Handoff'
                );
                if (!chosenSession) {
                    return;
                }

                const modePick = await vscode.window.showQuickPick(HANDOFF_MODE_OPTIONS, {
                    placeHolder: `Session Handoff for "${chosenSession}"`,
                });
                if (!modePick) {
                    return;
                }

                if (modePick.mode === 'openSessionsCockpit') {
                    await openWebCockpitRoute(`/sessions?session=${encodeURIComponent(chosenSession)}`);
                    return;
                }

                if (modePick.mode === 'inspect') {
                    const res = await client.getHandoff(chosenSession, {
                        limit: 10,
                        projectDir: client.getWorkspaceCwd(),
                    });
                    const doc = await vscode.workspace.openTextDocument({
                        content: formatHandoffMarkdown(res),
                        language: 'markdown',
                    });
                    await vscode.window.showTextDocument(doc);
                    const qLabel = res.quality
                        ? ` (quality ${res.quality.score}/100 · ${res.quality.status})`
                        : '';
                    vscode.window.showInformationMessage(
                        `Loaded handoff summary for "${chosenSession}"${qLabel}`
                    );
                    return;
                }

                let handoffRes: HandoffResult;
                if (modePick.mode === 'createAuto') {
                    handoffRes = await client.createHandoff({
                        session: chosenSession,
                        auto: true,
                        projectDir: client.getWorkspaceCwd(),
                    });
                } else {
                    const rawGoal = await vscode.window.showInputBox({
                        prompt: `Goal accomplished in session "${chosenSession}"`,
                        placeHolder: 'e.g. Implemented REST endpoints and OpenAPI schemas',
                    });
                    const goal = (rawGoal ?? '').trim();
                    if (!goal) {
                        return;
                    }
                    const rawNow = await vscode.window.showInputBox({
                        prompt: `Immediate next step (Now) for "${chosenSession}"`,
                        placeHolder: 'e.g. Run browser qualification and push to main',
                    });
                    const now = (rawNow ?? '').trim();
                    if (!now) {
                        return;
                    }
                    handoffRes = await client.createHandoff({
                        session: chosenSession,
                        goal,
                        now,
                        projectDir: client.getWorkspaceCwd(),
                    });
                }

                currentSession = chosenSession;
                const qSuffix = handoffRes.quality
                    ? ` (quality ${handoffRes.quality.score}/100 · ${handoffRes.quality.status})`
                    : '';
                const nextBtn = await vscode.window.showInformationMessage(
                    `Created handoff for "${chosenSession}"${qSuffix}`,
                    'Open /sessions in Web Cockpit'
                );
                if (nextBtn === 'Open /sessions in Web Cockpit') {
                    await openWebCockpitRoute(`/sessions?session=${encodeURIComponent(chosenSession)}`);
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Session handoff failed: ${e}`);
            }
        }
    );

    const dispShowAnalytics = vscode.commands.registerCommand('ntm.showAnalytics', async () => {
        try {
            const windowPick = await vscode.window.showQuickPick(ANALYTICS_WINDOW_OPTIONS, {
                placeHolder: 'Select NTM analytics reporting window',
            });
            if (!windowPick) {
                return;
            }
            if (windowPick.mode === 'openAnalyticsCockpit') {
                await openWebCockpitRoute('/analytics');
                return;
            }

            let since: string | undefined;
            if (windowPick.mode === 'customSince') {
                const rawSince = await vscode.window.showInputBox({
                    prompt: 'Start date for NTM analytics (YYYY-MM-DD or RFC3339)',
                    placeHolder: 'e.g. 2026-11-01',
                });
                const trimmedSince = (rawSince ?? '').trim();
                if (!trimmedSince) {
                    return;
                }
                since = trimmedSince;
            }

            const res = await client.getAnalytics({
                days: since ? undefined : (windowPick.days ?? 30),
                since,
                sessions: true,
            });
            const doc = await vscode.workspace.openTextDocument({
                content: formatAnalyticsMarkdown(res),
                language: 'markdown',
            });
            await vscode.window.showTextDocument(doc);
            const nextBtn = await vscode.window.showInformationMessage(
                `Loaded NTM analytics (${res.period}: ${res.total_sessions} sessions, ${res.total_agents} agents, ${res.total_prompts} prompts)`,
                'Open /analytics in Web Cockpit'
            );
            if (nextBtn === 'Open /analytics in Web Cockpit') {
                await openWebCockpitRoute('/analytics');
            }
        } catch (e) {
            vscode.window.showErrorMessage(`Analytics query failed: ${e}`);
        }
    });

    const dispShowAudit = vscode.commands.registerCommand(
        'ntm.showAudit',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const modePick = await vscode.window.showQuickPick(AUDIT_MODE_OPTIONS, {
                    placeHolder: 'Select NTM audit action',
                });
                if (!modePick) {
                    return;
                }
                if (modePick.mode === 'openSafetyCockpit') {
                    await openWebCockpitRoute('/safety');
                    return;
                }

                const explicitSess = extractSessionName(sessionArg);
                let res: AuditResult;

                if (modePick.mode === 'list') {
                    res = await client.getAudit({ action: 'list', limit: 50 });
                } else if (modePick.mode === 'sources') {
                    res = await client.getAudit({ action: 'sources', sources: true, limit: 50 });
                } else if (modePick.mode === 'search') {
                    const pattern = await vscode.window.showInputBox({
                        prompt: 'Regular expression to search across NTM audit logs',
                        placeHolder: 'e.g. spawn|rate_limit|error',
                    });
                    const trimmedPattern = (pattern ?? '').trim();
                    if (!trimmedPattern) {
                        return;
                    }
                    res = await client.getAudit({
                        session: explicitSess ?? 'all',
                        action: 'search',
                        search: trimmedPattern,
                        days: 30,
                        limit: 50,
                    });
                } else {
                    const targetSession = await resolveSessionInteractive(
                        explicitSess,
                        modePick.mode === 'verify'
                            ? 'Session name to verify hash-chain audit integrity'
                            : 'Session name to inspect audit entries'
                    );
                    if (!targetSession) {
                        return;
                    }
                    if (modePick.mode === 'verify') {
                        res = await client.getAudit({
                            session: targetSession,
                            action: 'verify',
                            verify: true,
                        });
                    } else {
                        res = await client.getAudit({
                            session: targetSession,
                            action: 'show',
                            limit: 50,
                        });
                    }
                }

                const doc = await vscode.workspace.openTextDocument({
                    content: formatAuditMarkdown(res),
                    language: 'markdown',
                });
                await vscode.window.showTextDocument(doc);

                if (typeof res.verified === 'boolean' && !res.verified) {
                    vscode.window.showWarningMessage(
                        `NTM audit verification FAILED for "${res.session}" (${res.files.length} file(s) inspected)`
                    );
                } else {
                    const nextBtn = await vscode.window.showInformationMessage(
                        `Loaded NTM audit (${res.action}: ${res.count} of ${res.total_matches})`,
                        'Open /safety in Web Cockpit'
                    );
                    if (nextBtn === 'Open /safety in Web Cockpit') {
                        await openWebCockpitRoute('/safety');
                    }
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Audit query failed: ${e}`);
            }
        }
    );

    const dispGenerateSupportBundle = vscode.commands.registerCommand(
        'ntm.generateSupportBundle',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const formatPick = await vscode.window.showQuickPick(SUPPORT_BUNDLE_FORMAT_OPTIONS, {
                    placeHolder: 'Select support bundle archive format & redaction policy',
                });
                if (!formatPick) {
                    return;
                }

                const explicitSess = extractSessionName(sessionArg);
                let targetSession = explicitSess;
                if (!targetSession) {
                    const rawSession = await vscode.window.showInputBox({
                        prompt: 'Session name to bundle (leave blank to bundle all active sessions)',
                        value: currentSession ?? '',
                        placeHolder: 'Leave empty for --all sessions',
                    });
                    if (rawSession === undefined) {
                        return;
                    }
                    targetSession = rawSession.trim() || undefined;
                }

                const cwd = client.getWorkspaceCwd();
                const defaultFilename = `ntm-support-bundle.${formatPick.format}`;
                const suggestedOutput = cwd ? path.join(cwd, defaultFilename) : '';
                const rawOutput = await vscode.window.showInputBox({
                    prompt: 'Optional output path for support bundle (leave blank for NTM default)',
                    value: suggestedOutput,
                });
                if (rawOutput === undefined) {
                    return;
                }

                const res: SupportBundleResult = await client.generateSupportBundle({
                    session: targetSession,
                    all: !targetSession,
                    outputPath: rawOutput.trim() || undefined,
                    format: formatPick.format,
                    redactMode: formatPick.redactMode,
                    lines: 500,
                });

                const redactedNote = res.redaction_summary
                    ? `, ${res.redaction_summary.files_redacted} file(s) redacted`
                    : '';
                const nextBtn = await vscode.window.showInformationMessage(
                    `Generated support bundle: ${res.path} (${res.file_count} files, ${res.total_size} bytes${redactedNote})`,
                    'Open Overview in Web Cockpit'
                );
                if (nextBtn === 'Open Overview in Web Cockpit') {
                    await openWebCockpitRoute('/');
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Support bundle generation failed: ${e}`);
            }
        }
    );

    const dispRunDoctor = vscode.commands.registerCommand('ntm.runDoctor', async () => {
        try {
            const modePick = await vscode.window.showQuickPick(DOCTOR_MODE_OPTIONS, {
                placeHolder: 'Select NTM ecosystem doctor diagnostic mode',
            });
            if (!modePick) {
                return;
            }
            if (modePick.mode === 'openOverviewCockpit') {
                await openWebCockpitRoute('/');
                return;
            }

            const res = await client.getDoctor({
                contention: modePick.mode === 'contention',
                verbose: true,
            });
            const doc = await vscode.workspace.openTextDocument({
                content: formatDoctorMarkdown(res),
                language: 'markdown',
            });
            await vscode.window.showTextDocument(doc);

            if (res.errors > 0 || res.overall_status.toLowerCase() === 'unhealthy') {
                vscode.window.showWarningMessage(
                    `NTM Doctor: ${res.overall_status.toUpperCase()} (${res.errors} error(s), ${res.warnings} warning(s), ${res.contention.length} lock holder(s))`
                );
            } else {
                const nextBtn = await vscode.window.showInformationMessage(
                    `NTM Doctor: ${res.overall_status.toUpperCase()} (${res.passed} passed, ${res.warnings} warning(s))`,
                    'Open Overview in Web Cockpit'
                );
                if (nextBtn === 'Open Overview in Web Cockpit') {
                    await openWebCockpitRoute('/');
                }
            }
        } catch (e) {
            vscode.window.showErrorMessage(`Ecosystem doctor failed: ${e}`);
        }
    });

    const dispPreflightPrompt = vscode.commands.registerCommand('ntm.preflightPrompt', async () => {
        try {
            const modePick = await vscode.window.showQuickPick(PREFLIGHT_MODE_OPTIONS, {
                placeHolder: 'Select prompt preflight lint mode',
            });
            if (!modePick) {
                return;
            }
            if (modePick.mode === 'openSwarmCockpit') {
                await openWebCockpitRoute('/swarm');
                return;
            }

            const editor = vscode.window.activeTextEditor;
            const selectedText = editor ? editor.document.getText(editor.selection).trim() : '';
            let promptText = selectedText;
            if (!promptText) {
                const rawPrompt = await vscode.window.showInputBox({
                    prompt: 'Enter prompt text to preflight-lint before dispatching to agents',
                    placeHolder: 'e.g. Implement typed OpenAPI schemas and run go test ./internal/serve',
                });
                promptText = (rawPrompt ?? '').trim();
            }
            if (!promptText) {
                return;
            }

            const res = await client.preflightPrompt({
                prompt: promptText,
                strict: modePick.mode === 'strict',
            });
            const doc = await vscode.workspace.openTextDocument({
                content: formatPreflightMarkdown(res),
                language: 'markdown',
            });
            await vscode.window.showTextDocument(doc);

            if (!res.allowed) {
                vscode.window.showWarningMessage(
                    `Prompt preflight BLOCKED (${res.error_count} error(s), ${res.warning_count} warning(s), ~${res.estimated_tokens} tokens)`
                );
            } else {
                const nextBtn = await vscode.window.showInformationMessage(
                    `Prompt preflight PASSED (~${res.estimated_tokens} tokens, ${res.warning_count} warning(s))`,
                    'Open /swarm in Web Cockpit'
                );
                if (nextBtn === 'Open /swarm in Web Cockpit') {
                    await openWebCockpitRoute('/swarm');
                }
            }
        } catch (e) {
            vscode.window.showErrorMessage(`Prompt preflight failed: ${e}`);
        }
    });

    const dispAssignWork = vscode.commands.registerCommand(
        'ntm.assignWork',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const strategyPick = await vscode.window.showQuickPick(ASSIGN_STRATEGY_OPTIONS, {
                    placeHolder: 'Select Beads work assignment strategy',
                });
                if (!strategyPick) {
                    return;
                }
                if (strategyPick.strategy === 'openSwarmCockpit') {
                    await openWebCockpitRoute('/swarm');
                    return;
                }

                const targetSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'Session name to plan Beads work assignments'
                );
                if (!targetSession) {
                    return;
                }

                const rawBeads = await vscode.window.showInputBox({
                    prompt: 'Optional comma-separated Bead IDs to scope assignment (leave blank for all ready beads)',
                    placeHolder: 'Leave empty for all ready beads, or e.g. bd-101,bd-102',
                });
                if (rawBeads === undefined) {
                    return;
                }

                const res = await client.getAssign({
                    session: targetSession,
                    strategy: strategyPick.strategy,
                    beads: rawBeads.trim() || undefined,
                });
                const doc = await vscode.workspace.openTextDocument({
                    content: formatAssignWorkMarkdown(res),
                    language: 'markdown',
                });
                await vscode.window.showTextDocument(doc);

                const nextBtn = await vscode.window.showInformationMessage(
                    `Planned ${res.summary.recommendations} assignment(s) for "${res.session}" (${res.summary.idle_agents} idle agent(s), ${res.summary.ready_beads} ready bead(s))`,
                    'Open /swarm in Web Cockpit'
                );
                if (nextBtn === 'Open /swarm in Web Cockpit') {
                    await openWebCockpitRoute('/swarm');
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Work assignment planning failed: ${e}`);
            }
        }
    );

    const dispScaleSession = vscode.commands.registerCommand(
        'ntm.scaleSession',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const modePick = await vscode.window.showQuickPick(SCALE_MODE_OPTIONS, {
                    placeHolder: 'Select session scaling mode across all 11 harnesses',
                });
                if (!modePick) {
                    return;
                }
                if (modePick.mode === 'openSwarmCockpit') {
                    await openWebCockpitRoute('/swarm');
                    return;
                }

                const targetSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'Session name to scale'
                );
                if (!targetSession) {
                    return;
                }

                const rawSpec = await vscode.window.showInputBox({
                    prompt: 'Enter target harness counts (comma/space separated across cc, cod, gmi, agy, ollama, cursor, windsurf, aider, grok, omp, oc)',
                    value: 'cc=2,cod=1',
                    placeHolder: 'e.g. cc=3,cod=2,cursor=1,ollama=1',
                });
                if (!rawSpec || !rawSpec.trim()) {
                    return;
                }

                const targets = parseScaleSpec(rawSpec);
                const res = await client.scaleSession({
                    session: targetSession,
                    targets,
                    dryRun: modePick.mode === 'dryRun',
                    force: modePick.mode === 'apply',
                });
                const doc = await vscode.workspace.openTextDocument({
                    content: formatScaleMarkdown(res),
                    language: 'markdown',
                });
                await vscode.window.showTextDocument(doc);

                const nextBtn = await vscode.window.showInformationMessage(
                    `${res.dry_run ? 'Scale plan' : 'Scaled'} "${res.session}": ${res.actions.length} action(s)`,
                    'Open /swarm in Web Cockpit'
                );
                if (nextBtn === 'Open /swarm in Web Cockpit') {
                    await openWebCockpitRoute('/swarm');
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Session scale failed: ${e}`);
            }
        }
    );

    const dispRebalanceSession = vscode.commands.registerCommand(
        'ntm.rebalanceSession',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const modePick = await vscode.window.showQuickPick(REBALANCE_MODE_OPTIONS, {
                    placeHolder: 'Select workload or geometry rebalance mode',
                });
                if (!modePick) {
                    return;
                }
                if (modePick.mode === 'openSwarmCockpit') {
                    await openWebCockpitRoute('/swarm');
                    return;
                }

                const targetSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'Session name to rebalance'
                );
                if (!targetSession) {
                    return;
                }

                const res = await client.rebalanceSession({
                    session: targetSession,
                    apply: modePick.mode === 'apply',
                    dryRun: modePick.mode !== 'apply',
                    geometry: modePick.mode === 'geometry',
                });
                const doc = await vscode.workspace.openTextDocument({
                    content: formatRebalanceMarkdown(res),
                    language: 'markdown',
                });
                await vscode.window.showTextDocument(doc);

                const nextBtn = await vscode.window.showInformationMessage(
                    `Rebalance "${res.session}": score=${res.imbalance_score.toFixed(2)} (${res.recommendation}, ${res.transfers.length} transfer(s))`,
                    'Open /swarm in Web Cockpit'
                );
                if (nextBtn === 'Open /swarm in Web Cockpit') {
                    await openWebCockpitRoute('/swarm');
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Session rebalance failed: ${e}`);
            }
        }
    );

    const dispReviewQueue = vscode.commands.registerCommand(
        'ntm.reviewQueue',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const modePick = await vscode.window.showQuickPick(REVIEW_QUEUE_MODE_OPTIONS, {
                    placeHolder: 'Select peer review queue mode',
                });
                if (!modePick) {
                    return;
                }
                if (modePick.mode === 'openSwarmCockpit') {
                    await openWebCockpitRoute('/swarm');
                    return;
                }

                const targetSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'Session name for peer review queue'
                );
                if (!targetSession) {
                    return;
                }

                const res = await client.getReviewQueue({
                    session: targetSession,
                    dispatch: modePick.mode === 'dispatch',
                    projectDir: client.getWorkspaceCwd(),
                });
                const doc = await vscode.workspace.openTextDocument({
                    content: formatReviewQueueMarkdown(res),
                    language: 'markdown',
                });
                await vscode.window.showTextDocument(doc);

                const nextBtn = await vscode.window.showInformationMessage(
                    `Peer review queue for "${res.session}": ${res.suggestions.length} suggestion(s) across ${res.idle_count} idle agent(s)`,
                    'Open /swarm in Web Cockpit'
                );
                if (nextBtn === 'Open /swarm in Web Cockpit') {
                    await openWebCockpitRoute('/swarm');
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Peer review queue failed: ${e}`);
            }
        }
    );

    const dispShowSummary = vscode.commands.registerCommand(
        'ntm.showSummary',
        async (sessionArg?: string | NtmTreeItem) => {
            try {
                const modePick = await vscode.window.showQuickPick(SUMMARY_WINDOW_OPTIONS, {
                    placeHolder: 'Select session activity summary window',
                });
                if (!modePick) {
                    return;
                }

                const targetSession = await resolveSessionInteractive(
                    extractSessionName(sessionArg),
                    'Session name to summarize'
                );
                if (modePick.mode === 'openSessionsCockpit') {
                    await openWebCockpitRoute(
                        targetSession
                            ? `/sessions?session=${encodeURIComponent(targetSession)}`
                            : '/sessions'
                    );
                    return;
                }
                if (!targetSession) {
                    return;
                }

                const res = await client.getSummary({
                    session: targetSession,
                    since: modePick.since ?? '30m',
                });
                const doc = await vscode.workspace.openTextDocument({
                    content: formatSummaryMarkdown(res),
                    language: 'markdown',
                });
                await vscode.window.showTextDocument(doc);

                const nextBtn = await vscode.window.showInformationMessage(
                    `Session summary "${res.session}": ${res.agents.length} agent(s), ${res.total_files} file(s) modified, ${res.conflicts.length} conflict(s)`,
                    'Open /sessions in Web Cockpit'
                );
                if (nextBtn === 'Open /sessions in Web Cockpit') {
                    await openWebCockpitRoute(`/sessions?session=${encodeURIComponent(res.session)}`);
                }
            } catch (e) {
                vscode.window.showErrorMessage(`Session summary failed: ${e}`);
            }
        }
    );

    context.subscriptions.push(
        dispStatus,
        dispSpawn,
        dispSpawnEnsemble,
        dispSynthesizeEnsemble,
        dispTriageAttention,
        dispTriageIncidents,
        dispHandoffSession,
        dispShowAnalytics,
        dispShowAudit,
        dispGenerateSupportBundle,
        dispRunDoctor,
        dispPreflightPrompt,
        dispAssignWork,
        dispScaleSession,
        dispRebalanceSession,
        dispReviewQueue,
        dispShowSummary,
        dispOpenWebCockpit,
        dispShowBackpressure,
        dispManageWorktrees,
        dispOpenPalette,
        dispDashboard,
        dispSendPrompt,
        dispSendSelection,
        dispSendFile,
        dispOpenTerminal,
        dispViewSession,
        dispZoomPane,
        dispTerminalDashboard
    );
}

function pickPrimarySession(status: RobotStatus): SessionInfo | undefined {
    const sessions = status.sessions || [];

    if (vscode.workspace.name) {
        const match = sessions.find(s => s.name === vscode.workspace.name);
        if (match) {
            return match;
        }
    }

    const attached = sessions.find(s => s.attached);
    return attached ?? sessions[0];
}

async function updateStatus(client: NtmClient, item: vscode.StatusBarItem): Promise<string | undefined> {
    try {
        const status = await client.getStatus();
        const sessionCount = status.summary.total_sessions;
        const agentCount = status.summary.total_agents;
        const primary = pickPrimarySession(status);

        if (sessionCount > 0 && primary) {
            const primaryAgents = primary.agents?.length || primary.agent_count || primary.panes || agentCount;
            const isAttached = primary.attached;
            item.text = `$(terminal) ${primary.name} • ${primaryAgents}`;
            item.tooltip = `${sessionCount} sessions • ${agentCount} agents\n${primary.name}: ${isAttached ? 'Attached' : 'Detached'}`;

            if (isAttached) {
                item.color = new vscode.ThemeColor('statusBarItem.prominentForeground');
            } else {
                item.color = undefined;
            }
            item.show();
            return primary.name;
        } else {
            item.text = '$(terminal) NTM';
            item.tooltip = 'No active sessions';
            item.color = new vscode.ThemeColor('statusBarItem.warningForeground');
            item.show();
        }
    } catch {
        item.text = '$(warning) NTM';
        item.tooltip = 'Error connecting to NTM';
        item.color = new vscode.ThemeColor('statusBarItem.errorForeground');
        item.show();
    }
    return undefined;
}

export function deactivate() {}
