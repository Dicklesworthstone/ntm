import * as cp from 'child_process';
import * as fs from 'fs';
import * as vscode from 'vscode';

export const SUPPORTED_SPAWN_HARNESSES = [
    'cc',
    'cod',
    'agy',
    'omp',
    'grok',
    'oc',
    'pi',
] as const;

export const SUPPORTED_SEND_HARNESSES = [
    'cc',
    'cod',
    'agy',
    'omp',
    'grok',
    'oc',
    'pi',
    'gmi',
] as const;

export type SpawnHarness = typeof SUPPORTED_SPAWN_HARNESSES[number];
export type SendHarness = typeof SUPPORTED_SEND_HARNESSES[number];

export interface RobotStatus {
    generated_at: string;
    schema_id?: string;
    schema_version?: string;
    degraded?: boolean;
    system: {
        version: string;
        os: string;
    };
    sessions: SessionInfo[];
    summary: StatusSummary;
    beads?: BeadsStatus;
    agent_mail?: AgentMailStatus;
    alerts?: AlertDetail[];
}

export interface BeadsPreviewItem {
    id: string;
    title: string;
    priority?: string | number;
    type?: string;
    assignee?: string;
    updated_at?: string;
}

export interface BeadsStatus {
    available: boolean;
    total: number;
    open: number;
    in_progress: number;
    blocked: number;
    ready: number;
    closed: number;
    project?: string;
    ready_preview?: BeadsPreviewItem[];
    in_progress_list?: BeadsPreviewItem[];
}

export interface AgentMailSessionStats {
    pane?: string;
    unread: number;
    pending_ack: number;
}

export interface AgentMailStatus {
    available: boolean;
    server_url?: string;
    reason?: string;
    project?: string;
    total_unread?: number;
    urgent_messages?: number;
    total_locks?: number;
    threads_known?: number;
    error?: string;
    agents?: Record<string, AgentMailSessionStats>;
}

export interface SessionHealth {
    status: string;
    reason?: string;
}

export interface ServiceInfo {
    pane: string;
    name: string;
    manager: string;
    command?: string;
}

export interface SessionInfo {
    name: string;
    exists: boolean;
    attached: boolean;
    windows: number;
    panes: number;
    agent_count: number;
    agents: AgentInfo[];
    services?: ServiceInfo[];
    health?: SessionHealth;
}

export interface AgentInfo {
    type: string;
    variant?: string;
    pane: string;
    state?: string;
    is_active: boolean;
    context_percent?: number;
    current_bead?: string | null;
    pending_mail?: number;
    last_output_age_sec?: number;
}

export interface StatusSummary {
    total_sessions: number;
    total_agents: number;
    attached_count: number;
    ready_work?: number;
    in_progress?: number;
    health_score?: number;
    health_status?: string;
    alerts_active?: number;
    mail_unread?: number;
    mail_urgent?: number;
}

export interface AlertDetail {
    id?: string;
    source?: string;
    type?: string;
    severity?: string;
    message: string;
    session?: string;
    pane?: string;
    bead_id?: string;
    created_at?: string;
}

export interface RobotMail {
    generated_at: string;
    session?: string;
    project_key?: string;
    available: boolean;
    server_url?: string;
    session_agent?: SessionAgentInfo;
    agents?: MailAgent[];
    unmapped_agents?: MailAgent[];
    messages?: MailMessageCounts;
    file_reservations?: MailReservation[];
    conflicts?: MailConflict[];
    warnings?: string[];
    error?: string;
}

export interface SessionAgentInfo {
    name: string;
    model?: string;
    program?: string;
}

export interface MailMessageCounts {
    total: number;
    unread: number;
    urgent: number;
    pending_ack: number;
}

export interface MailAgent {
    pane?: string;
    agent_name?: string;
    name?: string; // Alias for agent_name used in display
    program?: string;
    model?: string;
    unread_count?: number;
    urgent_count?: number;
    last_active_ts?: string;
}

export interface MailReservation {
    id: number;
    pattern: string;
    agent: string;
    exclusive: boolean;
    expires_in_seconds: number;
    expires_in?: string | number;
    reason?: string;
}

export interface MailConflict {
    pattern: string;
    holders: string[];
}

export const DEFAULT_WEB_PORT = 7337;

export const SUPPORTED_ATTENTION_ACTIONS = [
    'ack',
    'pin',
    'unpin',
    'snooze',
    'dismiss',
] as const;

export type AttentionMutationAction = typeof SUPPORTED_ATTENTION_ACTIONS[number];

export interface EnsemblePreset {
    name: string;
    display?: string;
    display_name: string;
    description: string;
    modes: string[];
    mode_count: number;
    agent_count: number;
    category?: string;
    tags: string[];
    source?: string;
    allow_advanced?: boolean;
    synthesis?: { strategy: string };
    budget?: { total: number };
}

export interface EnsembleSuggestion {
    preset_name: string;
    display_name: string;
    description: string;
    score: number;
    reasons: string[];
    mode_count: number;
    modes?: string[];
    tags: string[];
    spawn_cmd?: string;
}

export interface EnsembleSuggestResult {
    question: string;
    top_pick?: EnsembleSuggestion;
    suggestions: EnsembleSuggestion[];
    recommended_preset?: string;
    recommended_modes?: string[];
    summary?: string;
}

export interface EnsembleSpawnOptions {
    session: string;
    preset?: string;
    modes?: string;
    question: string;
    agents?: string;
    projectDir?: string;
    assignment?: string;
    allowAdvanced?: boolean;
}

export interface EnsembleSpawnMode {
    id: string;
    code?: string;
    tier?: string;
    pane?: string;
    agent_type?: string;
}

export interface EnsembleSpawnBudget {
    total_tokens?: number;
    per_mode_tokens?: number;
}

export interface EnsembleSpawnResult {
    action: string;
    session: string;
    preset?: string;
    question?: string;
    project_dir?: string;
    assignment?: string;
    agents?: Record<string, number>;
    modes: EnsembleSpawnMode[];
    budget: EnsembleSpawnBudget;
    status?: string;
    warnings?: string[];
}

export interface AttentionListOptions {
    actionRequired?: boolean;
    limit?: number;
    session?: string;
    includeAll?: boolean;
}

export interface AttentionItem {
    cursor: number;
    item_key: string;
    dedup_key?: string;
    session?: string;
    pane?: string;
    category: string;
    type: string;
    severity: string;
    actionability: string;
    action_required: boolean;
    summary: string;
    event_timestamp?: string;
    state: string;
    operator_state: string;
    pinned: boolean;
    muted: boolean;
    snoozed_until?: string;
    override_priority?: string;
    resurfacing_policy?: string;
}

export interface AttentionMutationResult {
    success: boolean;
    timestamp: string;
    action: string;
    ref: string;
    cursor?: number;
    item_key: string;
    previous_state: string;
    current_state: string;
    pinned?: boolean;
    snoozed_until?: string;
}

export type ExecFileRunner = (
    file: string,
    args: string[],
    options: cp.ExecFileOptions
) => Promise<{ stdout: string; stderr: string }>;

export interface NtmClientOptions {
    binaryPath?: string;
    workspaceRoot?: string;
    execFile?: ExecFileRunner;
}

const SAFE_BARE_SHELL_ARG = /^[A-Za-z0-9_./:@-][A-Za-z0-9_./:@=-]*$/;
const ACTIVE_AGENT_STATES = new Set(['active', 'working', 'busy', 'running']);
const ACTION_REQUIRED_LEVELS = new Set(['action_required', 'urgent']);
const MAX_EXEC_BUFFER_BYTES = 16 * 1024 * 1024;

/**
 * Quote a single argument for safe interpolation into a POSIX shell command line.
 * Strips NUL bytes and wraps any non-trivial or empty token in single quotes,
 * escaping embedded single quotes with `'\''`.
 */
export function shellQuote(arg: string): string {
    const clean = String(arg ?? '').replace(/\0/g, '');
    if (clean.length === 0) {
        return "''";
    }
    if (SAFE_BARE_SHELL_ARG.test(clean)) {
        return clean;
    }
    return `'${clean.replace(/'/g, "'\\''")}'`;
}

/**
 * Build a shell-safe integrated terminal command using the configured `ntm.binaryPath`.
 * Accepts either `(binaryPath, argsArray)` or `(binaryPath, subcommand, ...args)`.
 */
export function buildNtmTerminalCommand(binaryPath: string, args: readonly string[]): string;
export function buildNtmTerminalCommand(binaryPath: string, subcommand: string, ...args: string[]): string;
export function buildNtmTerminalCommand(
    binaryPath: string,
    subcommandOrArgs: string | readonly string[],
    ...restArgs: string[]
): string {
    const bin = (binaryPath ?? '').trim() || 'ntm';
    const argv = Array.isArray(subcommandOrArgs)
        ? [...subcommandOrArgs, ...restArgs]
        : [subcommandOrArgs as string, ...restArgs];
    return [bin, ...argv].map(shellQuote).join(' ');
}

/**
 * Normalize a configured web server port to a valid TCP port (1..65535), falling back to 7337.
 */
export function normalizeWebPort(value: unknown): number {
    const num = typeof value === 'number'
        ? value
        : (typeof value === 'string' && value.trim().length > 0 ? Number(value.trim()) : NaN);
    if (Number.isInteger(num) && num >= 1 && num <= 65535) {
        return num;
    }
    return DEFAULT_WEB_PORT;
}

/**
 * Build a shell-safe terminal command for launching `ntm web --port <port>`.
 */
export function buildWebCockpitCommand(binaryPath: string, port?: number): string {
    const resolvedPort = normalizeWebPort(port);
    return buildNtmTerminalCommand(binaryPath, ['web', '--port', String(resolvedPort)]);
}

/**
 * Resolve workspace folder path to pass as `cwd` to `cp.execFile` only if it exists on disk.
 */
export function resolveWorkspaceCwd(candidatePath?: string): string | undefined {
    const fsPath = candidatePath ?? vscode.workspace.workspaceFolders?.[0]?.uri?.fsPath;
    if (!fsPath || typeof fsPath !== 'string') {
        return undefined;
    }
    try {
        return fs.statSync(fsPath).isDirectory() ? fsPath : undefined;
    } catch {
        return undefined;
    }
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
        return value as Record<string, unknown>;
    }
    return undefined;
}

function normalizeAgent(raw: unknown): AgentInfo {
    const obj = asRecord(raw) ?? {};
    const state = typeof obj.state === 'string' && obj.state.length > 0 ? obj.state : undefined;
    const isActive = typeof obj.is_active === 'boolean'
        ? obj.is_active
        : (state ? ACTIVE_AGENT_STATES.has(state.toLowerCase()) : false);

    return {
        type: typeof obj.type === 'string' && obj.type.length > 0 ? obj.type : 'unknown',
        variant: typeof obj.variant === 'string' && obj.variant.length > 0 ? obj.variant : undefined,
        pane: typeof obj.pane === 'string' ? obj.pane : String(obj.pane ?? ''),
        state,
        is_active: isActive,
        context_percent: typeof obj.context_percent === 'number' ? obj.context_percent : undefined,
        current_bead: typeof obj.current_bead === 'string' ? obj.current_bead : (obj.current_bead === null ? null : undefined),
        pending_mail: typeof obj.pending_mail === 'number' ? obj.pending_mail : undefined,
        last_output_age_sec: typeof obj.last_output_age_sec === 'number' ? obj.last_output_age_sec : undefined,
    };
}

function normalizeService(raw: unknown): ServiceInfo {
    const obj = asRecord(raw) ?? {};
    return {
        pane: typeof obj.pane === 'string' ? obj.pane : String(obj.pane ?? ''),
        name: typeof obj.name === 'string' ? obj.name : '',
        manager: typeof obj.manager === 'string' ? obj.manager : '',
        command: typeof obj.command === 'string' ? obj.command : undefined,
    };
}

function normalizeSession(raw: unknown): SessionInfo {
    const obj = asRecord(raw) ?? {};
    const agents = Array.isArray(obj.agents) ? obj.agents.map(normalizeAgent) : [];
    const services = Array.isArray(obj.services) ? obj.services.map(normalizeService) : undefined;
    const agentCount = typeof obj.agent_count === 'number' ? obj.agent_count : agents.length;
    const panes = typeof obj.panes === 'number'
        ? obj.panes
        : (agents.length > 0 ? agents.length + (services?.length ?? 0) : agentCount);
    const windows = typeof obj.windows === 'number' ? obj.windows : (panes > 0 ? 1 : 0);

    const rawHealth = asRecord(obj.health);
    const health: SessionHealth | undefined = rawHealth && typeof rawHealth.status === 'string'
        ? {
            status: rawHealth.status,
            reason: typeof rawHealth.reason === 'string' ? rawHealth.reason : undefined,
        }
        : undefined;

    return {
        name: typeof obj.name === 'string' ? obj.name : String(obj.name ?? ''),
        exists: typeof obj.exists === 'boolean' ? obj.exists : true,
        attached: Boolean(obj.attached),
        windows,
        panes,
        agent_count: agentCount,
        agents,
        services,
        health,
    };
}

function normalizeBeadPreviewList(raw: unknown): BeadsPreviewItem[] | undefined {
    if (!Array.isArray(raw)) {
        return undefined;
    }
    return raw
        .map(item => {
            const obj = asRecord(item);
            if (!obj) {
                return undefined;
            }
            const preview: BeadsPreviewItem = {
                id: String(obj.id ?? ''),
                title: String(obj.title ?? ''),
            };
            if (typeof obj.priority === 'string' || typeof obj.priority === 'number') {
                preview.priority = obj.priority;
            }
            if (typeof obj.type === 'string') {
                preview.type = obj.type;
            }
            if (typeof obj.assignee === 'string') {
                preview.assignee = obj.assignee;
            }
            if (typeof obj.updated_at === 'string') {
                preview.updated_at = obj.updated_at;
            }
            return preview;
        })
        .filter((item): item is BeadsPreviewItem => item !== undefined);
}

function normalizeBeads(raw: Record<string, unknown>): BeadsStatus | undefined {
    const workObj = asRecord(raw.work);
    const workSummary = workObj ? asRecord(workObj.summary) : undefined;
    const rawBeads = asRecord(raw.beads_summary)
        ?? asRecord(raw.beads)
        ?? (workSummary ? { available: Boolean(workObj?.available ?? true), ...workSummary } : undefined);

    if (!rawBeads) {
        return undefined;
    }

    return {
        available: typeof rawBeads.available === 'boolean' ? rawBeads.available : true,
        total: Number(rawBeads.total ?? 0),
        open: Number(rawBeads.open ?? 0),
        in_progress: Number(rawBeads.in_progress ?? 0),
        blocked: Number(rawBeads.blocked ?? 0),
        ready: Number(rawBeads.ready ?? 0),
        closed: Number(rawBeads.closed ?? 0),
        project: typeof rawBeads.project === 'string' ? rawBeads.project : undefined,
        ready_preview: normalizeBeadPreviewList(rawBeads.ready_preview),
        in_progress_list: normalizeBeadPreviewList(rawBeads.in_progress_list),
    };
}

function normalizeAgentMailStatus(raw: Record<string, unknown>): AgentMailStatus | undefined {
    const rawMail = asRecord(raw.agent_mail);
    const rawCoord = asRecord(raw.coordination);
    if (!rawMail) {
        return undefined;
    }

    const totalUnread = typeof rawMail.total_unread === 'number'
        ? rawMail.total_unread
        : (typeof raw.mail_unread === 'number'
            ? raw.mail_unread
            : (typeof rawCoord?.mail_unread === 'number' ? rawCoord.mail_unread : undefined));

    const urgentMessages = typeof rawMail.urgent_messages === 'number'
        ? rawMail.urgent_messages
        : (typeof rawCoord?.mail_urgent === 'number' ? rawCoord.mail_urgent : undefined);

    let agentsMap: Record<string, AgentMailSessionStats> | undefined;
    const rawAgents = asRecord(rawMail.agents);
    if (rawAgents) {
        agentsMap = {};
        for (const [name, statsVal] of Object.entries(rawAgents)) {
            const stats = asRecord(statsVal);
            if (stats) {
                agentsMap[name] = {
                    pane: typeof stats.pane === 'string' ? stats.pane : undefined,
                    unread: Number(stats.unread ?? 0),
                    pending_ack: Number(stats.pending_ack ?? 0),
                };
            }
        }
    }

    return {
        available: Boolean(rawMail.available),
        server_url: typeof rawMail.server_url === 'string' ? rawMail.server_url : undefined,
        reason: typeof rawMail.reason === 'string' ? rawMail.reason : undefined,
        project: typeof rawMail.project === 'string' ? rawMail.project : undefined,
        total_unread: totalUnread,
        urgent_messages: urgentMessages,
        total_locks: typeof rawMail.total_locks === 'number' ? rawMail.total_locks : undefined,
        threads_known: typeof rawMail.threads_known === 'number' ? rawMail.threads_known : undefined,
        error: typeof rawMail.error === 'string' ? rawMail.error : undefined,
        agents: agentsMap,
    };
}

function normalizeAlerts(raw: Record<string, unknown>): AlertDetail[] | undefined {
    const detailed = Array.isArray(raw.alerts_detailed) ? raw.alerts_detailed : undefined;
    const sourceList = detailed && detailed.length > 0
        ? detailed
        : (Array.isArray(raw.alerts) ? raw.alerts : undefined);

    if (!sourceList) {
        return undefined;
    }

    return sourceList.map(entry => {
        if (typeof entry === 'string') {
            return { message: entry, severity: 'warning' };
        }
        const obj = asRecord(entry) ?? {};
        return {
            id: typeof obj.id === 'string' ? obj.id : undefined,
            source: typeof obj.source === 'string' ? obj.source : undefined,
            type: typeof obj.type === 'string' ? obj.type : undefined,
            severity: typeof obj.severity === 'string' ? obj.severity : undefined,
            message: typeof obj.message === 'string' ? obj.message : String(obj.message ?? ''),
            session: typeof obj.session === 'string' ? obj.session : undefined,
            pane: typeof obj.pane === 'string' ? obj.pane : undefined,
            bead_id: typeof obj.bead_id === 'string' ? obj.bead_id : undefined,
            created_at: typeof obj.created_at === 'string' ? obj.created_at : undefined,
        };
    });
}

function normalizeCommonPayload(raw: unknown, degraded: boolean): RobotStatus {
    const obj = asRecord(raw);
    if (!obj) {
        throw new Error('Invalid NTM robot JSON payload');
    }
    if (obj.success === false) {
        const errMsg = typeof obj.error === 'string' ? obj.error : 'robot command reported success=false';
        throw new Error(errMsg);
    }

    const sessions = Array.isArray(obj.sessions) ? obj.sessions.map(normalizeSession) : [];
    const rawSummary = asRecord(obj.summary);
    const computedAgents = sessions.reduce((sum, s) => sum + s.agent_count, 0);
    const computedAttached = sessions.filter(s => s.attached).length;

    const summary: StatusSummary = {
        total_sessions: typeof rawSummary?.total_sessions === 'number' ? rawSummary.total_sessions : sessions.length,
        total_agents: typeof rawSummary?.total_agents === 'number' ? rawSummary.total_agents : computedAgents,
        attached_count: typeof rawSummary?.attached_count === 'number' ? rawSummary.attached_count : computedAttached,
        ready_work: typeof rawSummary?.ready_work === 'number' ? rawSummary.ready_work : undefined,
        in_progress: typeof rawSummary?.in_progress === 'number' ? rawSummary.in_progress : undefined,
        health_score: typeof rawSummary?.health_score === 'number' ? rawSummary.health_score : undefined,
        health_status: typeof rawSummary?.health_status === 'string' ? rawSummary.health_status : undefined,
        alerts_active: typeof rawSummary?.alerts_active === 'number' ? rawSummary.alerts_active : undefined,
        mail_unread: typeof rawSummary?.mail_unread === 'number' ? rawSummary.mail_unread : undefined,
        mail_urgent: typeof rawSummary?.mail_urgent === 'number' ? rawSummary.mail_urgent : undefined,
    };

    const rawSystem = asRecord(obj.system);
    const generatedAt = (typeof obj.generated_at === 'string' && obj.generated_at)
        || (typeof obj.ts === 'string' && obj.ts)
        || (typeof obj.timestamp === 'string' && obj.timestamp)
        || new Date().toISOString();

    return {
        generated_at: generatedAt,
        schema_id: typeof obj.schema_id === 'string' ? obj.schema_id : undefined,
        schema_version: typeof obj.schema_version === 'string' ? obj.schema_version : undefined,
        degraded,
        system: {
            version: typeof rawSystem?.version === 'string'
                ? rawSystem.version
                : (typeof obj.version === 'string' ? obj.version : 'unknown'),
            os: typeof rawSystem?.os === 'string' ? rawSystem.os : process.platform,
        },
        sessions,
        summary,
        beads: normalizeBeads(obj),
        agent_mail: normalizeAgentMailStatus(obj),
        alerts: normalizeAlerts(obj),
    };
}

/**
 * Normalize a `--robot-snapshot` JSON payload into a rich `RobotStatus` model.
 */
export function normalizeSnapshot(raw: unknown): RobotStatus {
    return normalizeCommonPayload(raw, false);
}

/**
 * Normalize a lightweight `--robot-status` v2 header payload into `RobotStatus`.
 */
export function normalizeStatusHeader(raw: unknown): RobotStatus {
    return normalizeCommonPayload(raw, true);
}

function assertSuccessPayload(raw: unknown, label: string): Record<string, unknown> {
    const obj = asRecord(raw);
    if (!obj) {
        throw new Error(`Invalid NTM ${label} JSON payload`);
    }
    if (obj.success === false) {
        const errMsg = typeof obj.error === 'string' && obj.error.length > 0
            ? obj.error
            : `${label} reported success=false`;
        const hint = typeof obj.hint === 'string' && obj.hint.length > 0 ? ` (${obj.hint})` : '';
        throw new Error(`${errMsg}${hint}`);
    }
    return obj;
}

function toStringArray(raw: unknown): string[] {
    if (!Array.isArray(raw)) {
        return [];
    }
    return raw
        .filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
        .map(item => item.trim());
}

/**
 * Normalize a `--robot-ensemble-presets` JSON payload into `EnsemblePreset[]`.
 */
export function normalizeEnsemblePresets(raw: unknown): EnsemblePreset[] {
    const obj = assertSuccessPayload(raw, 'ensemble presets');
    if (!Array.isArray(obj.presets)) {
        return [];
    }

    return obj.presets
        .map(entry => {
            const p = asRecord(entry);
            if (!p) {
                return undefined;
            }
            const name = typeof p.name === 'string' ? p.name.trim() : '';
            if (!name) {
                return undefined;
            }
            const modes = toStringArray(p.modes);
            const tags = toStringArray(p.tags);
            const display = typeof p.display === 'string' && p.display.trim().length > 0
                ? p.display.trim()
                : undefined;
            const displayName = typeof p.display_name === 'string' && p.display_name.trim().length > 0
                ? p.display_name.trim()
                : (display ?? name);
            const modeCount = typeof p.mode_count === 'number'
                ? p.mode_count
                : (typeof p.agent_count === 'number' ? p.agent_count : modes.length);
            const agentCount = typeof p.agent_count === 'number' ? p.agent_count : modeCount;
            const category = typeof p.category === 'string' && p.category.trim().length > 0
                ? p.category.trim()
                : tags[0];

            const rawSynthesis = asRecord(p.synthesis);
            const rawBudget = asRecord(p.budget);

            const preset: EnsemblePreset = {
                name,
                display,
                display_name: displayName,
                description: typeof p.description === 'string' ? p.description : '',
                modes,
                mode_count: modeCount,
                agent_count: agentCount,
                category,
                tags,
                source: typeof p.source === 'string' ? p.source : undefined,
                allow_advanced: typeof p.allow_advanced === 'boolean' ? p.allow_advanced : undefined,
                synthesis: rawSynthesis && typeof rawSynthesis.strategy === 'string'
                    ? { strategy: rawSynthesis.strategy }
                    : undefined,
                budget: rawBudget && typeof rawBudget.total === 'number'
                    ? { total: rawBudget.total }
                    : undefined,
            };
            return preset;
        })
        .filter((item): item is EnsemblePreset => item !== undefined);
}

function normalizeSuggestionItem(raw: unknown): EnsembleSuggestion | undefined {
    const obj = asRecord(raw);
    if (!obj) {
        return undefined;
    }
    const presetName = typeof obj.preset_name === 'string'
        ? obj.preset_name.trim()
        : (typeof obj.name === 'string' ? obj.name.trim() : '');
    if (!presetName) {
        return undefined;
    }
    const modes = Array.isArray(obj.modes) ? toStringArray(obj.modes) : undefined;
    const modeCount = typeof obj.mode_count === 'number'
        ? obj.mode_count
        : (modes ? modes.length : 0);

    return {
        preset_name: presetName,
        display_name: typeof obj.display_name === 'string' && obj.display_name.trim().length > 0
            ? obj.display_name.trim()
            : presetName,
        description: typeof obj.description === 'string' ? obj.description : '',
        score: typeof obj.score === 'number' ? obj.score : 0,
        reasons: toStringArray(obj.reasons),
        mode_count: modeCount,
        modes,
        tags: toStringArray(obj.tags),
        spawn_cmd: typeof obj.spawn_cmd === 'string' ? obj.spawn_cmd : undefined,
    };
}

/**
 * Normalize a `--robot-ensemble-suggest` JSON payload into `EnsembleSuggestResult`.
 */
export function normalizeEnsembleSuggest(raw: unknown): EnsembleSuggestResult {
    const obj = assertSuccessPayload(raw, 'ensemble suggest');
    const suggestions = Array.isArray(obj.suggestions)
        ? obj.suggestions
            .map(normalizeSuggestionItem)
            .filter((s): s is EnsembleSuggestion => s !== undefined)
        : [];

    const topPick = normalizeSuggestionItem(obj.top_pick) ?? suggestions[0];
    const rawHints = asRecord(obj._agent_hints);
    const fallbackPreset = typeof obj.recommended_preset === 'string' && obj.recommended_preset.trim().length > 0
        ? obj.recommended_preset.trim()
        : (typeof obj.preset_name === 'string' && obj.preset_name.trim().length > 0 ? obj.preset_name.trim() : undefined);
    const recommendedModes = topPick?.modes
        ?? (Array.isArray(obj.recommended_modes) ? toStringArray(obj.recommended_modes) : undefined)
        ?? (Array.isArray(obj.modes) ? toStringArray(obj.modes) : undefined);

    return {
        question: typeof obj.question === 'string' ? obj.question : '',
        top_pick: topPick,
        suggestions,
        recommended_preset: topPick?.preset_name ?? fallbackPreset,
        recommended_modes: recommendedModes,
        summary: rawHints && typeof rawHints.summary === 'string' ? rawHints.summary : undefined,
    };
}

/**
 * Normalize a `--robot-ensemble-spawn` JSON payload into `EnsembleSpawnResult`.
 */
export function normalizeEnsembleSpawn(raw: unknown): EnsembleSpawnResult {
    const obj = assertSuccessPayload(raw, 'ensemble spawn');
    const modes: EnsembleSpawnMode[] = Array.isArray(obj.modes)
        ? obj.modes
            .map(entry => {
                const m = asRecord(entry);
                if (!m || typeof m.id !== 'string') {
                    return undefined;
                }
                const mode: EnsembleSpawnMode = {
                    id: m.id,
                    code: typeof m.code === 'string' ? m.code : undefined,
                    tier: typeof m.tier === 'string' ? m.tier : undefined,
                    pane: typeof m.pane === 'string' ? m.pane : undefined,
                    agent_type: typeof m.agent_type === 'string' ? m.agent_type : undefined,
                };
                return mode;
            })
            .filter((m): m is EnsembleSpawnMode => m !== undefined)
        : [];

    const rawBudget = asRecord(obj.budget);
    const budget: EnsembleSpawnBudget = {
        total_tokens: typeof rawBudget?.total_tokens === 'number' ? rawBudget.total_tokens : undefined,
        per_mode_tokens: typeof rawBudget?.per_mode_tokens === 'number' ? rawBudget.per_mode_tokens : undefined,
    };

    let agentsMap: Record<string, number> | undefined;
    const rawAgents = asRecord(obj.agents);
    if (rawAgents) {
        agentsMap = {};
        for (const [k, v] of Object.entries(rawAgents)) {
            if (typeof v === 'number') {
                agentsMap[k] = v;
            }
        }
    }

    return {
        action: typeof obj.action === 'string' ? obj.action : 'ensemble_spawn',
        session: typeof obj.session === 'string' ? obj.session : '',
        preset: typeof obj.preset === 'string' ? obj.preset : undefined,
        question: typeof obj.question === 'string' ? obj.question : undefined,
        project_dir: typeof obj.project_dir === 'string' ? obj.project_dir : undefined,
        assignment: typeof obj.assignment === 'string' ? obj.assignment : undefined,
        agents: agentsMap,
        modes,
        budget,
        status: typeof obj.status === 'string' ? obj.status : undefined,
        warnings: Array.isArray(obj.warnings) ? toStringArray(obj.warnings) : undefined,
    };
}

/**
 * Normalize an `ntm attention list --json` payload into `AttentionItem[]`.
 */
export function normalizeAttentionList(raw: unknown): AttentionItem[] {
    const obj = assertSuccessPayload(raw, 'attention list');
    if (!Array.isArray(obj.items)) {
        return [];
    }

    return obj.items
        .map(entry => {
            const it = asRecord(entry);
            if (!it) {
                return undefined;
            }
            const cursor = Number(it.cursor ?? 0);
            if (!Number.isFinite(cursor)) {
                return undefined;
            }
            const state = typeof it.state === 'string' && it.state.length > 0
                ? it.state
                : (typeof it.operator_state === 'string' && it.operator_state.length > 0 ? it.operator_state : 'new');
            const operatorState = typeof it.operator_state === 'string' && it.operator_state.length > 0
                ? it.operator_state
                : state;
            const actionability = typeof it.actionability === 'string' && it.actionability.length > 0
                ? it.actionability
                : (it.action_required === true ? 'action_required' : 'interesting');
            const actionRequired = typeof it.action_required === 'boolean'
                ? it.action_required
                : ACTION_REQUIRED_LEVELS.has(actionability.toLowerCase());

            const item: AttentionItem = {
                cursor,
                item_key: typeof it.item_key === 'string' && it.item_key.length > 0 ? it.item_key : `cursor:${cursor}`,
                dedup_key: typeof it.dedup_key === 'string' ? it.dedup_key : undefined,
                session: typeof it.session === 'string' && it.session.length > 0 ? it.session : undefined,
                pane: typeof it.pane === 'string' && it.pane.length > 0 ? it.pane : undefined,
                category: typeof it.category === 'string' ? it.category : 'general',
                type: typeof it.type === 'string' ? it.type : 'event',
                severity: typeof it.severity === 'string' && it.severity.length > 0 ? it.severity : 'info',
                actionability,
                action_required: actionRequired,
                summary: typeof it.summary === 'string' ? it.summary : '',
                event_timestamp: typeof it.event_timestamp === 'string' ? it.event_timestamp : undefined,
                state,
                operator_state: operatorState,
                pinned: Boolean(it.pinned),
                muted: Boolean(it.muted),
                snoozed_until: typeof it.snoozed_until === 'string' ? it.snoozed_until : undefined,
                override_priority: typeof it.override_priority === 'string' ? it.override_priority : undefined,
                resurfacing_policy: typeof it.resurfacing_policy === 'string' ? it.resurfacing_policy : undefined,
            };
            return item;
        })
        .filter((item): item is AttentionItem => item !== undefined);
}

/**
 * Normalize an `ntm attention <action> <cursor> --json` payload into `AttentionMutationResult`.
 */
export function normalizeAttentionMutation(raw: unknown): AttentionMutationResult {
    const obj = assertSuccessPayload(raw, 'attention mutation');
    const attState = asRecord(obj.attention_state);

    return {
        success: true,
        timestamp: typeof obj.timestamp === 'string' ? obj.timestamp : new Date().toISOString(),
        action: typeof obj.action === 'string' ? obj.action : '',
        ref: typeof obj.ref === 'string' ? obj.ref : String(obj.cursor ?? ''),
        cursor: typeof obj.cursor === 'number' ? obj.cursor : undefined,
        item_key: typeof obj.item_key === 'string' ? obj.item_key : '',
        previous_state: typeof obj.previous_state === 'string' ? obj.previous_state : 'new',
        current_state: typeof obj.current_state === 'string'
            ? obj.current_state
            : (typeof attState?.state === 'string' ? attState.state : 'new'),
        pinned: typeof attState?.pinned === 'boolean' ? attState.pinned : undefined,
        snoozed_until: typeof attState?.snoozed_until === 'string' ? attState.snoozed_until : undefined,
    };
}

function extractJsonErrorDetail(stdout?: string): string | undefined {
    if (typeof stdout !== 'string') {
        return undefined;
    }
    const trimmed = stdout.trim();
    if (!trimmed.startsWith('{')) {
        return undefined;
    }
    try {
        const parsed = asRecord(JSON.parse(trimmed));
        if (parsed && typeof parsed.error === 'string' && parsed.error.length > 0) {
            const hint = typeof parsed.hint === 'string' && parsed.hint.length > 0 ? ` (${parsed.hint})` : '';
            return `${parsed.error}${hint}`;
        }
    } catch {
        // Ignore non-JSON stdout
    }
    return undefined;
}

function defaultExecFile(
    file: string,
    args: string[],
    options: cp.ExecFileOptions
): Promise<{ stdout: string; stderr: string }> {
    return new Promise((resolve, reject) => {
        cp.execFile(file, args, options, (err, stdout, stderr) => {
            const outStr = typeof stdout === 'string' ? stdout : stdout.toString('utf8');
            const errStr = typeof stderr === 'string' ? stderr : stderr.toString('utf8');
            if (err) {
                const enriched = err as Error & { stdout?: string; stderr?: string };
                enriched.stdout = outStr;
                enriched.stderr = errStr;
                reject(enriched);
                return;
            }
            resolve({ stdout: outStr, stderr: errStr });
        });
    });
}

function ensureRobotJsonArgs(args: string[]): string[] {
    const hasRobotCmd = args.some(
        arg => arg.startsWith('--robot-') && !arg.startsWith('--robot-format') && !arg.startsWith('--robot-output-format')
    );
    const hasRobotFormat = args.some(
        arg => arg === '--robot-format' || arg.startsWith('--robot-format=') || arg.startsWith('--robot-output-format')
    );
    if (hasRobotCmd && !hasRobotFormat) {
        return [...args, '--robot-format=json'];
    }
    return [...args];
}

export class NtmClient {
    private readonly options: NtmClientOptions;
    private readonly execRunner: ExecFileRunner;
    private snapshotInFlight: Promise<RobotStatus> | null = null;
    private statusHeaderInFlight: Promise<RobotStatus> | null = null;
    private mailInFlight: Promise<RobotMail> | null = null;

    constructor(options: NtmClientOptions = {}) {
        this.options = options;
        this.execRunner = options.execFile ?? defaultExecFile;
    }

    public getBinaryPath(): string {
        if (this.options.binaryPath && this.options.binaryPath.trim().length > 0) {
            return this.options.binaryPath.trim();
        }
        const config = vscode.workspace.getConfiguration('ntm');
        const configured = config.get<string>('binaryPath', 'ntm');
        return (configured ?? '').trim() || 'ntm';
    }

    public getWorkspaceCwd(): string | undefined {
        return resolveWorkspaceCwd(this.options.workspaceRoot);
    }

    private async run(args: string[], opts?: { allowJsonErrorEnvelope?: boolean }): Promise<string> {
        const finalArgs = ensureRobotJsonArgs(args);
        const execOptions: cp.ExecFileOptions = {
            maxBuffer: MAX_EXEC_BUFFER_BYTES,
        };
        const cwd = this.getWorkspaceCwd();
        if (cwd) {
            execOptions.cwd = cwd;
        }

        try {
            const { stdout } = await this.execRunner(this.getBinaryPath(), finalArgs, execOptions);
            return stdout;
        } catch (err) {
            const e = err as Error & { stdout?: string; stderr?: string };
            if (opts?.allowJsonErrorEnvelope && typeof e.stdout === 'string' && e.stdout.trim().startsWith('{')) {
                try {
                    const parsed = JSON.parse(e.stdout);
                    if (parsed && typeof parsed === 'object' && ('available' in parsed || 'error' in parsed)) {
                        return e.stdout;
                    }
                } catch {
                    // Fall through to standard error reporting
                }
            }
            const jsonError = !e.stderr || e.stderr.trim().length === 0
                ? extractJsonErrorDetail(e.stdout)
                : undefined;
            const detail = e.stderr && e.stderr.trim().length > 0
                ? `: ${e.stderr}`
                : (jsonError ? `: ${jsonError}` : (e.message ? `: ${e.message}` : ''));
            throw new Error(`ntm ${finalArgs.join(' ')} failed${detail}`);
        }
    }

    /**
     * Tier-2 full state fetch via `--robot-snapshot --robot-format=json`, single-flight
     * deduplicated across concurrent callers, with automatic Tier-1 fallback to
     * `--robot-status --robot-format=json` if snapshot fails or degrades.
     */
    async getSnapshot(): Promise<RobotStatus> {
        if (this.snapshotInFlight) {
            return this.snapshotInFlight;
        }

        const flight = (async (): Promise<RobotStatus> => {
            try {
                const stdout = await this.run(['--robot-snapshot', '--robot-format=json']);
                return normalizeSnapshot(JSON.parse(stdout));
            } catch (snapshotErr) {
                try {
                    return await this.getStatusHeader();
                } catch {
                    throw new Error(`Failed to get status: ${snapshotErr}`);
                }
            }
        })();

        this.snapshotInFlight = flight;
        try {
            return await flight;
        } finally {
            if (this.snapshotInFlight === flight) {
                this.snapshotInFlight = null;
            }
        }
    }

    /**
     * Tier-1 lightweight header/health fetch via `--robot-status --robot-format=json`.
     */
    async getStatusHeader(): Promise<RobotStatus> {
        if (this.statusHeaderInFlight) {
            return this.statusHeaderInFlight;
        }

        const flight = (async (): Promise<RobotStatus> => {
            try {
                const stdout = await this.run(['--robot-status', '--robot-format=json']);
                return normalizeStatusHeader(JSON.parse(stdout));
            } catch (e) {
                throw new Error(`Failed to get status header: ${e}`);
            }
        })();

        this.statusHeaderInFlight = flight;
        try {
            return await flight;
        } finally {
            if (this.statusHeaderInFlight === flight) {
                this.statusHeaderInFlight = null;
            }
        }
    }

    /**
     * Unified status accessor backed by single-flight `getSnapshot()` + Tier-1 fallback.
     */
    async getStatus(): Promise<RobotStatus> {
        return this.getSnapshot();
    }

    async spawn(session: string, args: string[]): Promise<void> {
        const cleanArgs = args.filter(a => a.trim().length > 0);
        const finalArgs = ['spawn', session, ...cleanArgs];
        if (!finalArgs.includes('--json')) {
            finalArgs.push('--json');
        }
        await this.run(finalArgs);
    }

    async send(session: string, prompt: string, targets: string[] = []): Promise<void> {
        const args = ['send', session];

        if (targets.includes('all')) {
            args.push('--all');
        } else {
            for (const harness of SUPPORTED_SEND_HARNESSES) {
                if (targets.includes(harness)) {
                    args.push(`--${harness}`);
                }
            }
        }

        // Use -- to separate flags from positional arguments (the prompt)
        args.push('--', prompt);
        await this.run(args);
    }

    async checkAvailable(): Promise<boolean> {
        try {
            await this.run(['--version']);
            return true;
        } catch {
            return false;
        }
    }

    async getMail(): Promise<RobotMail> {
        if (this.mailInFlight) {
            return this.mailInFlight;
        }

        const flight = (async (): Promise<RobotMail> => {
            try {
                const stdout = await this.run(['--robot-mail', '--robot-format=json'], {
                    allowJsonErrorEnvelope: true,
                });
                return JSON.parse(stdout) as RobotMail;
            } catch (e) {
                throw new Error(`Failed to get mail state: ${e}`);
            }
        })();

        this.mailInFlight = flight;
        try {
            return await flight;
        } finally {
            if (this.mailInFlight === flight) {
                this.mailInFlight = null;
            }
        }
    }

    /**
     * List available reasoning ensemble presets via `ntm --robot-ensemble-presets`.
     */
    async getEnsemblePresets(): Promise<EnsemblePreset[]> {
        const stdout = await this.run(['--robot-ensemble-presets', '--robot-format=json']);
        return normalizeEnsemblePresets(JSON.parse(stdout));
    }

    /**
     * Suggest the best reasoning ensemble preset for a problem/question via
     * `ntm --robot-ensemble-suggest=<question>`.
     */
    async suggestEnsemble(question: string): Promise<EnsembleSuggestResult> {
        const trimmed = (question ?? '').trim();
        if (!trimmed) {
            throw new Error('Question is required for ensemble suggestion');
        }
        const stdout = await this.run([`--robot-ensemble-suggest=${trimmed}`, '--robot-format=json']);
        return normalizeEnsembleSuggest(JSON.parse(stdout));
    }

    /**
     * Spawn a multi-agent reasoning ensemble via `ntm --robot-ensemble-spawn=<session>`.
     */
    async spawnEnsemble(options: EnsembleSpawnOptions): Promise<EnsembleSpawnResult> {
        const session = (options?.session ?? '').trim();
        if (!session) {
            throw new Error('Session name is required to spawn an ensemble');
        }
        const question = (options?.question ?? '').trim();
        if (!question) {
            throw new Error('Question is required to spawn an ensemble');
        }
        const preset = (options?.preset ?? '').trim();
        const modes = (options?.modes ?? '').trim();
        if (!preset && !modes) {
            throw new Error('Either preset or modes is required to spawn an ensemble');
        }
        if (preset && modes) {
            throw new Error('preset and modes are mutually exclusive for ensemble spawn');
        }

        const args: string[] = [`--robot-ensemble-spawn=${session}`];
        if (preset) {
            args.push(`--preset=${preset}`);
        }
        if (modes) {
            args.push(`--modes=${modes}`);
        }
        args.push(`--question=${question}`);

        const agents = (options?.agents ?? '').trim();
        if (agents) {
            args.push(`--agents=${agents}`);
        }
        const assignment = (options?.assignment ?? '').trim();
        if (assignment) {
            args.push(`--assignment=${assignment}`);
        }
        if (options?.allowAdvanced) {
            args.push('--allow-advanced');
        }
        const projectDir = (options?.projectDir ?? '').trim();
        if (projectDir) {
            args.push(`--project=${projectDir}`);
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeEnsembleSpawn(JSON.parse(stdout));
    }

    /**
     * List surfaced operator attention items via `ntm attention list --json`.
     */
    async listAttentionItems(options?: AttentionListOptions): Promise<AttentionItem[]> {
        const args: string[] = ['attention', 'list'];
        if (options?.session && options.session.trim().length > 0) {
            args.push(`--session=${options.session.trim()}`);
        }
        if (options?.limit !== undefined) {
            if (!Number.isInteger(options.limit) || options.limit < 1) {
                throw new Error('Attention list limit must be a positive integer');
            }
            args.push(`--limit=${options.limit}`);
        }
        if (options?.includeAll) {
            args.push('--all');
        }
        args.push('--json');

        const stdout = await this.run(args);
        const items = normalizeAttentionList(JSON.parse(stdout));
        if (options?.actionRequired) {
            return items.filter(item => item.action_required);
        }
        return items;
    }

    /**
     * Transition operator attention state via `ntm attention <action> <cursor> --json`
     * (using `--until=<duration>` when `action === 'snooze'`).
     */
    async mutateAttentionItem(
        action: AttentionMutationAction,
        cursor: number | string,
        duration?: string
    ): Promise<AttentionMutationResult> {
        if (!SUPPORTED_ATTENTION_ACTIONS.includes(action)) {
            throw new Error(`Unsupported attention action: ${String(action)}`);
        }

        let ref: string;
        if (typeof cursor === 'number') {
            if (!Number.isInteger(cursor) || cursor < 1) {
                throw new Error('Attention cursor must be a positive integer');
            }
            ref = String(cursor);
        } else {
            ref = String(cursor ?? '').trim();
            if (!ref) {
                throw new Error('Attention cursor or item key is required');
            }
        }

        const args: string[] = ['attention', action, ref];
        if (action === 'snooze') {
            const trimmedDuration = (duration ?? '').trim();
            if (!trimmedDuration) {
                throw new Error('Duration is required when snoozing an attention item');
            }
            args.push(`--until=${trimmedDuration}`);
        }
        args.push('--json');

        const stdout = await this.run(args);
        return normalizeAttentionMutation(JSON.parse(stdout));
    }
}
