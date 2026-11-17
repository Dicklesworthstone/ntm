import * as cp from 'child_process';
import * as fs from 'fs';
import * as vscode from 'vscode';
import {
    AssignStrategy,
    AssignWorkOptions,
    AssignWorkRecommendation,
    AssignWorkResult,
    AssignWorkSummary,
    DoctorCheckItem,
    DoctorContentionHolder,
    DoctorOptions,
    DoctorResult,
    normalizeAssignWorkResult,
    normalizeDoctorResult,
    normalizePreflightResult,
    PreflightFinding,
    PreflightOptions,
    PreflightResult,
    SUPPORTED_ASSIGN_STRATEGIES,
} from './orchestrationSupport';
import {
    normalizeRebalanceResult,
    normalizeReviewQueueResult,
    normalizeScaleResult,
    normalizeSummaryResult,
    parseScaleSpec,
    RebalanceSessionOptions,
    RebalanceSessionResult,
    RebalanceTransferItem,
    ReviewQueueOptions,
    ReviewQueueResult,
    ReviewQueueSuggestionItem,
    ScaleHarness,
    ScalePlanAction,
    ScaleSessionOptions,
    ScaleSessionResult,
    SessionSummaryOptions,
    SessionSummaryResult,
    SummaryAgentItem,
    SUPPORTED_SCALE_HARNESSES,
} from './swarmOpsSupport';

export {
    AssignStrategy,
    AssignWorkOptions,
    AssignWorkRecommendation,
    AssignWorkResult,
    AssignWorkSummary,
    DoctorCheckItem,
    DoctorContentionHolder,
    DoctorOptions,
    DoctorResult,
    normalizeAssignWorkResult,
    normalizeDoctorResult,
    normalizePreflightResult,
    normalizeRebalanceResult,
    normalizeReviewQueueResult,
    normalizeScaleResult,
    normalizeSummaryResult,
    parseScaleSpec,
    PreflightFinding,
    PreflightOptions,
    PreflightResult,
    RebalanceSessionOptions,
    RebalanceSessionResult,
    RebalanceTransferItem,
    ReviewQueueOptions,
    ReviewQueueResult,
    ReviewQueueSuggestionItem,
    ScaleHarness,
    ScalePlanAction,
    ScaleSessionOptions,
    ScaleSessionResult,
    SessionSummaryOptions,
    SessionSummaryResult,
    SummaryAgentItem,
    SUPPORTED_ASSIGN_STRATEGIES,
    SUPPORTED_SCALE_HARNESSES,
};

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

export type EnsembleSynthesisFormat = 'markdown' | 'json' | 'yaml';

export interface EnsembleSynthesizeOptions {
    session: string;
    strategy?: string;
    format?: EnsembleSynthesisFormat;
    output?: string;
    force?: boolean;
}

export interface EnsembleSynthesizeReport {
    summary: string;
    strategy: string;
    output_path?: string;
    format: string;
    findings_count: number;
    recommendations_count: number;
    risks_count: number;
    questions_count: number;
    confidence: number;
    generated_at?: string;
    synthesized_by?: string;
    fallback_reason?: string;
}

export interface EnsembleSynthesizeAudit {
    conflict_count: number;
    unresolved_count: number;
    high_conflict_pairs: string[];
}

export interface EnsembleSynthesizeResult {
    action: string;
    session: string;
    status: string;
    report?: EnsembleSynthesizeReport;
    audit?: EnsembleSynthesizeAudit;
    formatted_output?: string;
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

export type BackpressureLevel = 'normal' | 'elevated' | 'critical' | 'overload';

export interface BackpressureSurfaceReport {
    surface: string;
    session?: string;
    pane?: string;
    command?: string;
    queue_depth: number;
    queue_capacity?: number;
    dropped_count: number;
    latency_ms: number;
    client_lag_ms?: number;
    decision: string;
    reason_codes: string[];
}

export interface BackpressureHeatmapBucket {
    heatmap_bucket: string;
    surface: string;
    session?: string;
    pane?: string;
    severity: string;
    decision?: string;
    score: number;
    retry_after_ms?: number;
    reason_codes: string[];
    recommended_action: string;
}

export interface BackpressureLoadShedding {
    schema_version?: string;
    decision: string;
    error_code?: string;
    retry_after_ms?: number;
    degraded_surface?: string;
    recommended_action: string;
    reason_codes: string[];
}

export interface BackpressureSessionSummary {
    session: string;
    level: BackpressureLevel;
    decision: string;
    max_utilization: number;
    queue_depth: number;
    queue_capacity: number;
    dropped_count: number;
    hot_surfaces: string[];
    reason_codes: string[];
    recommended_action?: string;
}

export interface BackpressureResult {
    success: boolean;
    timestamp: string;
    session?: string;
    level: BackpressureLevel;
    regime: string;
    decision: string;
    utilization: number;
    queue_depth: number;
    queue_capacity: number;
    dropped_count: number;
    retry_after_ms?: number;
    reason_codes: string[];
    shed_policy: BackpressureLoadShedding;
    surfaces: BackpressureSurfaceReport[];
    buckets: BackpressureHeatmapBucket[];
    sessions: BackpressureSessionSummary[];
    recommended_actions: string[];
    hint?: string;
}

export interface WorktreeInfo {
    agent_name: string;
    branch_name: string;
    path: string;
    session_id: string;
    created: boolean;
    status: 'active' | 'pending' | 'error';
    error?: string;
}

export interface WorktreeListResult {
    session: string;
    total: number;
    worktrees: WorktreeInfo[];
}

export type IncidentAction = 'investigate' | 'resolve' | 'mute';

export const SUPPORTED_INCIDENT_ACTIONS: readonly IncidentAction[] = [
    'investigate',
    'resolve',
    'mute',
];

export interface IncidentItem {
    id: string;
    title: string;
    fingerprint: string;
    family: string;
    category: string;
    status: string;
    severity: string;
    session_names?: string;
    event_count: number;
    alert_count: number;
    started_at?: string;
    last_event_at?: string;
    notes?: string;
}

export interface IncidentListOptions {
    includeAll?: boolean;
    limit?: number;
    status?: string;
}

export interface IncidentMutationResult {
    success: boolean;
    action: IncidentAction;
    incident_id: string;
    previous_status: string;
    status: string;
    idempotent: boolean;
    note?: string;
}

export interface HandoffQualityReport {
    score: number;
    status: string;
    scored_at?: string;
    dimensions: Array<{ name: string; score: number }>;
    hints: string[];
}

export interface HandoffSummaryEntry {
    path: string;
    session: string;
    date: string;
    status?: string;
    goal?: string;
    is_auto: boolean;
}

export interface HandoffResult {
    success: boolean;
    action: string;
    session: string;
    created_path?: string;
    latest_path?: string;
    total_returned: number;
    handoffs: HandoffSummaryEntry[];
    latest?: {
        session: string;
        goal: string;
        now: string;
        status?: string;
        outcome?: string;
    };
    quality?: HandoffQualityReport;
    ledger?: {
        path: string;
        exists: boolean;
        bytes: number;
    };
}

export interface HandoffCreateOptions {
    session: string;
    auto?: boolean;
    goal?: string;
    now?: string;
    description?: string;
    projectDir?: string;
}

export interface AnalyticsOptions {
    days?: number;
    since?: string;
    sessions?: boolean;
}

export interface AnalyticsAgentStats {
    count: number;
    prompts: number;
    chars_sent: number;
    tokens_estimated: number;
}

export interface AnalyticsSessionSummary {
    name: string;
    created_at: string;
    agent_count: number;
    prompt_count: number;
}

export interface AnalyticsResult {
    success: boolean;
    schema_id?: string;
    schema_version?: string;
    period: string;
    since: string;
    days?: number;
    total_sessions: number;
    total_agents: number;
    total_prompts: number;
    total_chars_sent: number;
    total_tokens_estimated: number;
    agent_breakdown: Record<string, AnalyticsAgentStats>;
    sessions: AnalyticsSessionSummary[];
    error_count: number;
    error_types: Record<string, number>;
    agent_crashes: number;
    agent_restarts: number;
}

export type AuditModeAction = 'list' | 'show' | 'search' | 'verify' | 'sources';

export interface AuditOptions {
    session?: string;
    action?: AuditModeAction;
    search?: string;
    verify?: boolean;
    sources?: boolean;
    eventTypes?: string;
    actors?: string;
    since?: string;
    days?: number;
    limit?: number;
    offset?: number;
}

export interface AuditLogFileSummary {
    file: string;
    session?: string;
    size_bytes: number;
    modified_at: string;
}

export interface AuditEntrySummary {
    timestamp: string;
    session_id: string;
    event_type: string;
    actor: string;
    target: string;
    sequence_num: number;
    checksum: string;
    payload?: Record<string, unknown>;
}

export interface AuditVerifyFileResult {
    file: string;
    status: string;
    verified: boolean;
    error?: string;
}

export interface AuditSourceHealthSummary {
    source_name: string;
    available: boolean;
    healthy: boolean;
    reason?: string;
}

export interface AuditResult {
    success: boolean;
    action: string;
    session: string;
    count: number;
    total_matches: number;
    has_more: boolean;
    verified?: boolean;
    error?: string;
    logs: AuditLogFileSummary[];
    entries: AuditEntrySummary[];
    files: AuditVerifyFileResult[];
    sources: AuditSourceHealthSummary[];
}

export interface SupportBundleOptions {
    session?: string;
    all?: boolean;
    outputPath?: string;
    format?: 'zip' | 'tar.gz';
    since?: string;
    lines?: number;
    maxSizeMB?: number;
    redactMode?: 'redact' | 'block' | 'warn';
}

export interface SupportBundleResult {
    success: boolean;
    path: string;
    format: string;
    file_count: number;
    total_size: number;
    privacy_mode: boolean;
    content_suppressed: boolean;
    redaction_summary?: {
        mode: string;
        total_findings: number;
        files_scanned: number;
        files_redacted: number;
    };
    warnings: string[];
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
 * Normalize an `ntm --robot-ensemble-synthesize=<session>` payload into `EnsembleSynthesizeResult`.
 */
export function normalizeEnsembleSynthesize(raw: unknown): EnsembleSynthesizeResult {
    const obj = assertSuccessPayload(raw, 'ensemble synthesize');
    const rawReport = asRecord(obj.report);
    const report: EnsembleSynthesizeReport | undefined = rawReport
        ? {
            summary: typeof rawReport.summary === 'string' ? rawReport.summary : '',
            strategy: typeof rawReport.strategy === 'string' ? rawReport.strategy : '',
            output_path: typeof rawReport.output_path === 'string' && rawReport.output_path.length > 0
                ? rawReport.output_path
                : undefined,
            format: typeof rawReport.format === 'string' && rawReport.format.length > 0
                ? rawReport.format
                : 'markdown',
            findings_count: typeof rawReport.findings_count === 'number' ? rawReport.findings_count : 0,
            recommendations_count: typeof rawReport.recommendations_count === 'number'
                ? rawReport.recommendations_count
                : 0,
            risks_count: typeof rawReport.risks_count === 'number' ? rawReport.risks_count : 0,
            questions_count: typeof rawReport.questions_count === 'number' ? rawReport.questions_count : 0,
            confidence: typeof rawReport.confidence === 'number' ? rawReport.confidence : 0,
            generated_at: typeof rawReport.generated_at === 'string' ? rawReport.generated_at : undefined,
            synthesized_by: typeof rawReport.synthesized_by === 'string' && rawReport.synthesized_by.length > 0
                ? rawReport.synthesized_by
                : undefined,
            fallback_reason: typeof rawReport.fallback_reason === 'string' && rawReport.fallback_reason.length > 0
                ? rawReport.fallback_reason
                : undefined,
        }
        : undefined;

    const rawAudit = asRecord(obj.audit);
    const audit: EnsembleSynthesizeAudit | undefined = rawAudit
        ? {
            conflict_count: typeof rawAudit.conflict_count === 'number' ? rawAudit.conflict_count : 0,
            unresolved_count: typeof rawAudit.unresolved_count === 'number' ? rawAudit.unresolved_count : 0,
            high_conflict_pairs: toStringArray(rawAudit.high_conflict_pairs),
        }
        : undefined;

    return {
        action: typeof obj.action === 'string' ? obj.action : 'ensemble_synthesize',
        session: typeof obj.session === 'string' ? obj.session : '',
        status: typeof obj.status === 'string' ? obj.status : 'completed',
        report,
        audit,
        formatted_output: typeof obj.formatted_output === 'string' && obj.formatted_output.length > 0
            ? obj.formatted_output
            : undefined,
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

const BACKPRESSURE_LEVEL_ORDER: Record<BackpressureLevel, number> = {
    normal: 0,
    elevated: 1,
    critical: 2,
    overload: 3,
};

/**
 * Map a backpressure decision/severity/regime token to a normalized `BackpressureLevel`.
 */
export function mapBackpressureLevel(raw?: string): BackpressureLevel {
    switch ((raw ?? '').trim().toLowerCase()) {
        case 'degrade':
        case 'overload':
        case 'shed':
            return 'overload';
        case 'defer':
        case 'critical':
        case 'warning':
        case 'high':
            return 'critical';
        case 'coalesce':
        case 'elevated':
        case 'watch':
            return 'elevated';
        default:
            return 'normal';
    }
}

/**
 * Normalize an `ntm --robot-backpressure` JSON payload into `BackpressureResult`.
 */
export function normalizeBackpressure(raw: unknown): BackpressureResult {
    const obj = assertSuccessPayload(raw, 'backpressure');
    const bp = asRecord(obj.backpressure);
    const dash = asRecord(obj.dashboard);
    const heatmap = asRecord(obj.heatmap) ?? asRecord(bp?.heatmap);
    const shedRaw = asRecord(obj.load_shedding) ?? asRecord(obj.shed_policy);

    const decision = typeof bp?.decision === 'string' && bp.decision.length > 0
        ? bp.decision
        : (typeof obj.regime === 'string' && obj.regime.length > 0
            ? obj.regime
            : (typeof dash?.decision === 'string' && dash.decision.length > 0 ? dash.decision : 'ok'));

    const explicitLevel = typeof obj.level === 'string' && obj.level.length > 0 ? obj.level : decision;
    const level = mapBackpressureLevel(explicitLevel);

    const surfaces: BackpressureSurfaceReport[] = Array.isArray(bp?.surfaces)
        ? bp.surfaces
            .map(entry => {
                const s = asRecord(entry);
                if (!s || typeof s.surface !== 'string') {
                    return undefined;
                }
                const report: BackpressureSurfaceReport = {
                    surface: s.surface,
                    session: typeof s.session === 'string' && s.session.length > 0 ? s.session : undefined,
                    pane: typeof s.pane === 'string' && s.pane.length > 0 ? s.pane : undefined,
                    command: typeof s.command === 'string' && s.command.length > 0 ? s.command : undefined,
                    queue_depth: typeof s.queue_depth === 'number' && Number.isFinite(s.queue_depth) ? s.queue_depth : 0,
                    queue_capacity: typeof s.queue_capacity === 'number' && Number.isFinite(s.queue_capacity)
                        ? s.queue_capacity
                        : undefined,
                    dropped_count: typeof s.dropped_count === 'number' && Number.isFinite(s.dropped_count)
                        ? s.dropped_count
                        : 0,
                    latency_ms: typeof s.latency_ms === 'number' && Number.isFinite(s.latency_ms) ? s.latency_ms : 0,
                    client_lag_ms: typeof s.client_lag_ms === 'number' && Number.isFinite(s.client_lag_ms)
                        ? s.client_lag_ms
                        : undefined,
                    decision: typeof s.decision === 'string' && s.decision.length > 0 ? s.decision : 'ok',
                    reason_codes: toStringArray(s.reason_codes),
                };
                return report;
            })
            .filter((s): s is BackpressureSurfaceReport => s !== undefined)
        : [];

    const buckets: BackpressureHeatmapBucket[] = Array.isArray(heatmap?.buckets)
        ? heatmap.buckets
            .map(entry => {
                const b = asRecord(entry);
                if (!b) {
                    return undefined;
                }
                const bucketName = typeof b.heatmap_bucket === 'string' && b.heatmap_bucket.length > 0
                    ? b.heatmap_bucket
                    : (typeof b.surface === 'string' ? b.surface : 'host');
                const bucket: BackpressureHeatmapBucket = {
                    heatmap_bucket: bucketName,
                    surface: typeof b.surface === 'string' ? b.surface : 'host',
                    session: typeof b.session === 'string' && b.session.length > 0 ? b.session : undefined,
                    pane: typeof b.pane === 'string' && b.pane.length > 0 ? b.pane : undefined,
                    severity: typeof b.severity === 'string' && b.severity.length > 0 ? b.severity : 'info',
                    decision: typeof b.decision === 'string' ? b.decision : undefined,
                    score: typeof b.score === 'number' && Number.isFinite(b.score) ? b.score : 0,
                    retry_after_ms: typeof b.retry_after_ms === 'number' ? b.retry_after_ms : undefined,
                    reason_codes: toStringArray(b.reason_codes),
                    recommended_action: typeof b.recommended_action === 'string' ? b.recommended_action : '',
                };
                return bucket;
            })
            .filter((b): b is BackpressureHeatmapBucket => b !== undefined)
        : [];

    let totalDepth = 0;
    let totalCapacity = 0;
    let totalDropped = 0;
    let maxUtilization = typeof obj.utilization === 'number' && Number.isFinite(obj.utilization)
        ? Math.max(0, obj.utilization)
        : 0;

    for (const s of surfaces) {
        totalDepth += s.queue_depth;
        totalDropped += s.dropped_count;
        if (typeof s.queue_capacity === 'number' && s.queue_capacity > 0) {
            totalCapacity += s.queue_capacity;
            const ratio = s.queue_depth / s.queue_capacity;
            if (ratio > maxUtilization) {
                maxUtilization = ratio;
            }
        }
    }

    const shed_policy: BackpressureLoadShedding = {
        schema_version: typeof shedRaw?.schema_version === 'string' ? shedRaw.schema_version : undefined,
        decision: typeof shedRaw?.decision === 'string' && shedRaw.decision.length > 0 ? shedRaw.decision : decision,
        error_code: typeof shedRaw?.error_code === 'string' && shedRaw.error_code.length > 0
            ? shedRaw.error_code
            : undefined,
        retry_after_ms: typeof shedRaw?.retry_after_ms === 'number' ? shedRaw.retry_after_ms : undefined,
        degraded_surface: typeof shedRaw?.degraded_surface === 'string' && shedRaw.degraded_surface.length > 0
            ? shedRaw.degraded_surface
            : undefined,
        recommended_action: typeof shedRaw?.recommended_action === 'string' && shedRaw.recommended_action.length > 0
            ? shedRaw.recommended_action
            : 'none',
        reason_codes: toStringArray(shedRaw?.reason_codes),
    };

    // Aggregate per-session overload summaries from explicit sessions, surfaces, and heatmap buckets
    const sessionMap = new Map<string, BackpressureSessionSummary>();
    const ensureSession = (sessionName: string): BackpressureSessionSummary => {
        const existing = sessionMap.get(sessionName);
        if (existing) {
            return existing;
        }
        const created: BackpressureSessionSummary = {
            session: sessionName,
            level: 'normal',
            decision: 'ok',
            max_utilization: 0,
            queue_depth: 0,
            queue_capacity: 0,
            dropped_count: 0,
            hot_surfaces: [],
            reason_codes: [],
        };
        sessionMap.set(sessionName, created);
        return created;
    };

    const promoteSessionDecision = (target: BackpressureSessionSummary, candidateDecision: string) => {
        const candidateLevel = mapBackpressureLevel(candidateDecision);
        if (BACKPRESSURE_LEVEL_ORDER[candidateLevel] > BACKPRESSURE_LEVEL_ORDER[target.level]) {
            target.level = candidateLevel;
            target.decision = candidateDecision;
        }
    };

    if (Array.isArray(obj.sessions)) {
        for (const rawSess of obj.sessions) {
            const s = asRecord(rawSess);
            const name = typeof s?.session === 'string' ? s.session.trim() : (typeof s?.name === 'string' ? s.name.trim() : '');
            if (!name || !s) {
                continue;
            }
            const entry = ensureSession(name);
            const dec = typeof s.decision === 'string' ? s.decision : (typeof s.level === 'string' ? s.level : 'ok');
            promoteSessionDecision(entry, dec);
            if (typeof s.utilization === 'number' && s.utilization > entry.max_utilization) {
                entry.max_utilization = s.utilization;
            }
            for (const code of toStringArray(s.reason_codes)) {
                if (!entry.reason_codes.includes(code)) {
                    entry.reason_codes.push(code);
                }
            }
        }
    }

    for (const s of surfaces) {
        if (!s.session) {
            continue;
        }
        const entry = ensureSession(s.session);
        promoteSessionDecision(entry, s.decision);
        entry.queue_depth += s.queue_depth;
        entry.dropped_count += s.dropped_count;
        if (typeof s.queue_capacity === 'number' && s.queue_capacity > 0) {
            entry.queue_capacity += s.queue_capacity;
            const ratio = s.queue_depth / s.queue_capacity;
            if (ratio > entry.max_utilization) {
                entry.max_utilization = ratio;
            }
        }
        if (mapBackpressureLevel(s.decision) !== 'normal' && !entry.hot_surfaces.includes(s.surface)) {
            entry.hot_surfaces.push(s.surface);
        }
        for (const code of s.reason_codes) {
            if (!entry.reason_codes.includes(code)) {
                entry.reason_codes.push(code);
            }
        }
    }

    for (const b of buckets) {
        const bucketSession = b.session
            ?? (b.heatmap_bucket.startsWith('session:') ? b.heatmap_bucket.slice('session:'.length).trim() : undefined);
        if (!bucketSession) {
            continue;
        }
        const entry = ensureSession(bucketSession);
        promoteSessionDecision(entry, b.decision || b.severity);
        if (!entry.hot_surfaces.includes(b.surface)) {
            entry.hot_surfaces.push(b.surface);
        }
        for (const code of b.reason_codes) {
            if (!entry.reason_codes.includes(code)) {
                entry.reason_codes.push(code);
            }
        }
        if (!entry.recommended_action && b.recommended_action) {
            entry.recommended_action = b.recommended_action;
        }
    }

    const sessions = Array.from(sessionMap.values()).sort(
        (a, b) => BACKPRESSURE_LEVEL_ORDER[b.level] - BACKPRESSURE_LEVEL_ORDER[a.level]
            || b.max_utilization - a.max_utilization
            || a.session.localeCompare(b.session)
    );

    const reasonCodes = toStringArray(bp?.reason_codes);
    if (reasonCodes.length === 0) {
        for (const code of toStringArray(dash?.reason_codes)) {
            if (!reasonCodes.includes(code)) {
                reasonCodes.push(code);
            }
        }
    }

    return {
        success: true,
        timestamp: typeof obj.timestamp === 'string' ? obj.timestamp : new Date().toISOString(),
        session: typeof obj.session === 'string' && obj.session.length > 0 ? obj.session : undefined,
        level,
        regime: decision,
        decision,
        utilization: Math.round(maxUtilization * 10000) / 10000,
        queue_depth: totalDepth,
        queue_capacity: totalCapacity,
        dropped_count: totalDropped,
        retry_after_ms: typeof bp?.retry_after_ms === 'number'
            ? bp.retry_after_ms
            : shed_policy.retry_after_ms,
        reason_codes: reasonCodes,
        shed_policy,
        surfaces,
        buckets,
        sessions,
        recommended_actions: toStringArray(dash?.recommended_actions),
        hint: typeof obj.hint === 'string' ? obj.hint : undefined,
    };
}

/**
 * Normalize an `ntm worktrees list --json` payload into `WorktreeListResult`.
 */
export function normalizeWorktreeList(raw: unknown): WorktreeListResult {
    const obj = assertSuccessPayload(raw, 'worktrees list');
    const session = typeof obj.session === 'string' ? obj.session : '';
    const worktrees: WorktreeInfo[] = Array.isArray(obj.worktrees)
        ? obj.worktrees
            .map(entry => {
                const w = asRecord(entry);
                if (!w || typeof w.agent_name !== 'string' || w.agent_name.trim().length === 0) {
                    return undefined;
                }
                const errStr = typeof w.error === 'string' && w.error.trim().length > 0 ? w.error.trim() : undefined;
                const created = typeof w.created === 'boolean' ? w.created : true;
                const status: WorktreeInfo['status'] = errStr ? 'error' : (created ? 'active' : 'pending');
                const info: WorktreeInfo = {
                    agent_name: w.agent_name.trim(),
                    branch_name: typeof w.branch_name === 'string'
                        ? w.branch_name
                        : (typeof w.branch === 'string' ? w.branch : ''),
                    path: typeof w.path === 'string' ? w.path : '',
                    session_id: typeof w.session_id === 'string' && w.session_id.length > 0 ? w.session_id : session,
                    created,
                    status,
                    error: errStr,
                };
                return info;
            })
            .filter((w): w is WorktreeInfo => w !== undefined)
        : [];

    const total = typeof obj.total === 'number' && Number.isFinite(obj.total)
        ? obj.total
        : (typeof obj.count === 'number' && Number.isFinite(obj.count) ? obj.count : worktrees.length);

    return {
        session,
        total,
        worktrees,
    };
}

/**
 * Normalize an `ntm attention incidents list --json` payload into `IncidentItem[]`.
 */
export function normalizeIncidentList(raw: unknown): IncidentItem[] {
    const obj = assertSuccessPayload(raw, 'attention incidents list');
    if (!Array.isArray(obj.incidents)) {
        return [];
    }
    return obj.incidents
        .map(entry => {
            const inc = asRecord(entry);
            if (!inc || typeof inc.id !== 'string' || inc.id.trim().length === 0) {
                return undefined;
            }
            const item: IncidentItem = {
                id: inc.id.trim(),
                title: typeof inc.title === 'string' ? inc.title : inc.id.trim(),
                fingerprint: typeof inc.fingerprint === 'string' ? inc.fingerprint : '',
                family: typeof inc.family === 'string' ? inc.family : '',
                category: typeof inc.category === 'string' ? inc.category : '',
                status: typeof inc.status === 'string' && inc.status.length > 0 ? inc.status : 'open',
                severity: typeof inc.severity === 'string' && inc.severity.length > 0 ? inc.severity : 'info',
                session_names: typeof inc.session_names === 'string' && inc.session_names.length > 0 ? inc.session_names : undefined,
                event_count: typeof inc.event_count === 'number' && Number.isFinite(inc.event_count) ? inc.event_count : 0,
                alert_count: typeof inc.alert_count === 'number' && Number.isFinite(inc.alert_count) ? inc.alert_count : 0,
                started_at: typeof inc.started_at === 'string' ? inc.started_at : undefined,
                last_event_at: typeof inc.last_event_at === 'string' ? inc.last_event_at : undefined,
                notes: typeof inc.notes === 'string' && inc.notes.length > 0 ? inc.notes : undefined,
            };
            return item;
        })
        .filter((i): i is IncidentItem => i !== undefined);
}

/**
 * Normalize an `ntm attention incidents <action> <id> --json` payload.
 */
export function normalizeIncidentMutation(raw: unknown): IncidentMutationResult {
    const obj = assertSuccessPayload(raw, 'attention incidents mutation');
    const actionRaw = typeof obj.action === 'string' ? obj.action : 'investigate';
    const action: IncidentAction = SUPPORTED_INCIDENT_ACTIONS.includes(actionRaw as IncidentAction)
        ? (actionRaw as IncidentAction)
        : 'investigate';
    return {
        success: true,
        action,
        incident_id: typeof obj.incident_id === 'string' ? obj.incident_id : '',
        previous_status: typeof obj.previous_status === 'string' ? obj.previous_status : '',
        status: typeof obj.status === 'string' ? obj.status : '',
        idempotent: Boolean(obj.idempotent),
        note: typeof obj.note === 'string' && obj.note.length > 0 ? obj.note : undefined,
    };
}

/**
 * Normalize an `ntm --robot-handoff=<session>` payload into `HandoffResult`.
 */
export function normalizeHandoffResult(raw: unknown): HandoffResult {
    const obj = assertSuccessPayload(raw, 'robot-handoff');
    const handoffs: HandoffSummaryEntry[] = Array.isArray(obj.handoffs)
        ? obj.handoffs
            .map(entry => {
                const h = asRecord(entry);
                if (!h || typeof h.path !== 'string' || h.path.trim().length === 0) {
                    return undefined;
                }
                const item: HandoffSummaryEntry = {
                    path: h.path,
                    session: typeof h.session === 'string' ? h.session : '',
                    date: typeof h.date === 'string' ? h.date : '',
                    status: typeof h.status === 'string' && h.status.length > 0 ? h.status : undefined,
                    goal: typeof h.goal === 'string' && h.goal.length > 0 ? h.goal : undefined,
                    is_auto: Boolean(h.is_auto),
                };
                return item;
            })
            .filter((h): h is HandoffSummaryEntry => h !== undefined)
        : [];

    const latestRec = asRecord(obj.latest);
    const latest = latestRec
        ? {
            session: typeof latestRec.Session === 'string'
                ? latestRec.Session
                : (typeof latestRec.session === 'string' ? latestRec.session : ''),
            goal: typeof latestRec.Goal === 'string'
                ? latestRec.Goal
                : (typeof latestRec.goal === 'string' ? latestRec.goal : ''),
            now: typeof latestRec.Now === 'string'
                ? latestRec.Now
                : (typeof latestRec.now === 'string' ? latestRec.now : ''),
            status: typeof latestRec.Status === 'string'
                ? latestRec.Status
                : (typeof latestRec.status === 'string' ? latestRec.status : undefined),
            outcome: typeof latestRec.Outcome === 'string'
                ? latestRec.Outcome
                : (typeof latestRec.outcome === 'string' ? latestRec.outcome : undefined),
        }
        : undefined;

    const qualRec = asRecord(obj.quality) ?? (latestRec ? asRecord(latestRec.Quality) : undefined);
    const quality: HandoffQualityReport | undefined = qualRec && typeof qualRec.score === 'number'
        ? {
            score: qualRec.score,
            status: typeof qualRec.status === 'string' ? qualRec.status : 'unknown',
            scored_at: typeof qualRec.scored_at === 'string' ? qualRec.scored_at : undefined,
            dimensions: Array.isArray(qualRec.dimensions)
                ? qualRec.dimensions
                    .map(d => {
                        const dr = asRecord(d);
                        if (!dr || typeof dr.name !== 'string') {
                            return undefined;
                        }
                        return {
                            name: dr.name,
                            score: typeof dr.score === 'number' ? dr.score : 0,
                        };
                    })
                    .filter((d): d is { name: string; score: number } => d !== undefined)
                : [],
            hints: toStringArray(qualRec.hints),
        }
        : undefined;

    const ledgerRec = asRecord(obj.ledger);
    const ledger = ledgerRec && typeof ledgerRec.path === 'string'
        ? {
            path: ledgerRec.path,
            exists: Boolean(ledgerRec.exists),
            bytes: typeof ledgerRec.bytes === 'number' ? ledgerRec.bytes : 0,
        }
        : undefined;

    return {
        success: true,
        action: typeof obj.action === 'string' ? obj.action : 'list',
        session: typeof obj.session === 'string' ? obj.session : '',
        created_path: typeof obj.created_path === 'string' && obj.created_path.length > 0 ? obj.created_path : undefined,
        latest_path: typeof obj.latest_path === 'string' && obj.latest_path.length > 0 ? obj.latest_path : undefined,
        total_returned: typeof obj.total_returned === 'number' ? obj.total_returned : handoffs.length,
        handoffs,
        latest,
        quality,
        ledger,
    };
}

/**
 * Normalize `--robot-analytics` JSON output into a strongly typed `AnalyticsResult`.
 */
export function normalizeAnalyticsResult(raw: unknown): AnalyticsResult {
    const obj = asRecord(raw);
    if (!obj) {
        throw new Error('Invalid analytics response payload');
    }
    if (obj.success === false) {
        const msg = typeof obj.error === 'string' && obj.error.length > 0
            ? obj.error
            : 'Analytics command failed';
        throw new Error(msg);
    }

    const agentBreakdown: Record<string, AnalyticsAgentStats> = {};
    const rawBreakdown = asRecord(obj.agent_breakdown);
    if (rawBreakdown) {
        for (const [key, val] of Object.entries(rawBreakdown)) {
            const st = asRecord(val);
            if (!st) {
                continue;
            }
            agentBreakdown[key] = {
                count: typeof st.count === 'number' ? st.count : 0,
                prompts: typeof st.prompts === 'number' ? st.prompts : 0,
                chars_sent: typeof st.chars_sent === 'number' ? st.chars_sent : 0,
                tokens_estimated: typeof st.tokens_estimated === 'number' ? st.tokens_estimated : 0,
            };
        }
    }

    const sessions: AnalyticsSessionSummary[] = Array.isArray(obj.sessions)
        ? obj.sessions
            .map(entry => {
                const sr = asRecord(entry);
                if (!sr || typeof sr.name !== 'string') {
                    return undefined;
                }
                return {
                    name: sr.name,
                    created_at: typeof sr.created_at === 'string' ? sr.created_at : '',
                    agent_count: typeof sr.agent_count === 'number' ? sr.agent_count : 0,
                    prompt_count: typeof sr.prompt_count === 'number' ? sr.prompt_count : 0,
                };
            })
            .filter((entry): entry is AnalyticsSessionSummary => entry !== undefined)
        : [];

    const errorTypes: Record<string, number> = {};
    const rawErrors = asRecord(obj.error_types);
    if (rawErrors) {
        for (const [errKey, errVal] of Object.entries(rawErrors)) {
            if (typeof errVal === 'number') {
                errorTypes[errKey] = errVal;
            }
        }
    }

    return {
        success: true,
        schema_id: typeof obj.schema_id === 'string' ? obj.schema_id : undefined,
        schema_version: typeof obj.schema_version === 'string' ? obj.schema_version : undefined,
        period: typeof obj.period === 'string' ? obj.period : 'Last 30 days',
        since: typeof obj.since === 'string' ? obj.since : '',
        days: typeof obj.days === 'number' ? obj.days : undefined,
        total_sessions: typeof obj.total_sessions === 'number' ? obj.total_sessions : 0,
        total_agents: typeof obj.total_agents === 'number' ? obj.total_agents : 0,
        total_prompts: typeof obj.total_prompts === 'number' ? obj.total_prompts : 0,
        total_chars_sent: typeof obj.total_chars_sent === 'number' ? obj.total_chars_sent : 0,
        total_tokens_estimated: typeof obj.total_tokens_estimated === 'number' ? obj.total_tokens_estimated : 0,
        agent_breakdown: agentBreakdown,
        sessions,
        error_count: typeof obj.error_count === 'number' ? obj.error_count : 0,
        error_types: errorTypes,
        agent_crashes: typeof obj.agent_crashes === 'number' ? obj.agent_crashes : 0,
        agent_restarts: typeof obj.agent_restarts === 'number' ? obj.agent_restarts : 0,
    };
}

/**
 * Normalize `--robot-audit` JSON output into a strongly typed `AuditResult`.
 * Preserves verification diagnostics (`verified: false` + `files`) when hash-chain
 * tampering is detected.
 */
export function normalizeAuditResult(raw: unknown): AuditResult {
    const obj = asRecord(raw);
    if (!obj) {
        throw new Error('Invalid audit response payload');
    }
    const hasVerifiedField = typeof obj.verified === 'boolean';
    if (obj.success === false && !hasVerifiedField) {
        const msg = typeof obj.error === 'string' && obj.error.length > 0
            ? obj.error
            : 'Audit command failed';
        throw new Error(msg);
    }

    const logs: AuditLogFileSummary[] = Array.isArray(obj.logs)
        ? obj.logs
            .map(item => {
                const lr = asRecord(item);
                if (!lr || typeof lr.file !== 'string') {
                    return undefined;
                }
                const entry: AuditLogFileSummary = {
                    file: lr.file,
                    size_bytes: typeof lr.size_bytes === 'number' ? lr.size_bytes : 0,
                    modified_at: typeof lr.modified_at === 'string' ? lr.modified_at : '',
                };
                if (typeof lr.session === 'string' && lr.session.length > 0) {
                    entry.session = lr.session;
                }
                return entry;
            })
            .filter((item): item is AuditLogFileSummary => item !== undefined)
        : [];

    const entries: AuditEntrySummary[] = Array.isArray(obj.entries)
        ? obj.entries
            .map(item => {
                const er = asRecord(item);
                if (!er) {
                    return undefined;
                }
                const summary: AuditEntrySummary = {
                    timestamp: typeof er.timestamp === 'string' ? er.timestamp : '',
                    session_id: typeof er.session_id === 'string' ? er.session_id : '',
                    event_type: typeof er.event_type === 'string' ? er.event_type : '',
                    actor: typeof er.actor === 'string' ? er.actor : '',
                    target: typeof er.target === 'string' ? er.target : '',
                    sequence_num: typeof er.sequence_num === 'number' ? er.sequence_num : 0,
                    checksum: typeof er.checksum === 'string' ? er.checksum : '',
                };
                const payloadRec = asRecord(er.payload);
                if (payloadRec) {
                    summary.payload = payloadRec;
                }
                return summary;
            })
            .filter((item): item is AuditEntrySummary => item !== undefined)
        : [];

    const files: AuditVerifyFileResult[] = Array.isArray(obj.files)
        ? obj.files
            .map(item => {
                const fr = asRecord(item);
                if (!fr || typeof fr.file !== 'string') {
                    return undefined;
                }
                const vf: AuditVerifyFileResult = {
                    file: fr.file,
                    status: typeof fr.status === 'string' ? fr.status : 'unknown',
                    verified: Boolean(fr.verified),
                };
                if (typeof fr.error === 'string' && fr.error.length > 0) {
                    vf.error = fr.error;
                }
                return vf;
            })
            .filter((item): item is AuditVerifyFileResult => item !== undefined)
        : [];

    const sources: AuditSourceHealthSummary[] = Array.isArray(obj.sources)
        ? obj.sources
            .map(item => {
                const sr = asRecord(item);
                if (!sr || typeof sr.source_name !== 'string') {
                    return undefined;
                }
                const sh: AuditSourceHealthSummary = {
                    source_name: sr.source_name,
                    available: Boolean(sr.available),
                    healthy: Boolean(sr.healthy),
                };
                if (typeof sr.reason === 'string' && sr.reason.length > 0) {
                    sh.reason = sr.reason;
                }
                return sh;
            })
            .filter((item): item is AuditSourceHealthSummary => item !== undefined)
        : [];

    return {
        success: obj.success !== false,
        action: typeof obj.action === 'string' ? obj.action : 'list',
        session: typeof obj.session === 'string' ? obj.session : 'all',
        count: typeof obj.count === 'number' ? obj.count : 0,
        total_matches: typeof obj.total_matches === 'number' ? obj.total_matches : 0,
        has_more: Boolean(obj.has_more),
        verified: hasVerifiedField ? (obj.verified as boolean) : undefined,
        error: typeof obj.error === 'string' && obj.error.length > 0 ? obj.error : undefined,
        logs,
        entries,
        files,
        sources,
    };
}

/**
 * Normalize `--robot-support-bundle` JSON output into a strongly typed `SupportBundleResult`.
 */
export function normalizeSupportBundleResult(raw: unknown): SupportBundleResult {
    const obj = asRecord(raw);
    if (!obj) {
        throw new Error('Invalid support bundle response payload');
    }
    if (obj.success === false) {
        const msg = typeof obj.error === 'string' && obj.error.length > 0
            ? obj.error
            : 'Support bundle generation failed';
        throw new Error(msg);
    }

    const redRec = asRecord(obj.redaction_summary);
    const redactionSummary = redRec && typeof redRec.mode === 'string'
        ? {
            mode: redRec.mode,
            total_findings: typeof redRec.total_findings === 'number' ? redRec.total_findings : 0,
            files_scanned: typeof redRec.files_scanned === 'number' ? redRec.files_scanned : 0,
            files_redacted: typeof redRec.files_redacted === 'number' ? redRec.files_redacted : 0,
        }
        : undefined;

    return {
        success: true,
        path: typeof obj.path === 'string' ? obj.path : '',
        format: typeof obj.format === 'string' ? obj.format : 'zip',
        file_count: typeof obj.file_count === 'number' ? obj.file_count : 0,
        total_size: typeof obj.total_size === 'number' ? obj.total_size : 0,
        privacy_mode: Boolean(obj.privacy_mode),
        content_suppressed: Boolean(obj.content_suppressed),
        redaction_summary: redactionSummary,
        warnings: toStringArray(obj.warnings),
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
                    if (
                        parsed &&
                        typeof parsed === 'object' &&
                        ('available' in parsed ||
                            'error' in parsed ||
                            'overall_status' in parsed ||
                            'findings' in parsed)
                    ) {
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
     * Synthesize completed reasoning mode outputs for an ensemble session via
     * `ntm --robot-ensemble-synthesize=<session>`.
     */
    async synthesizeEnsemble(options: EnsembleSynthesizeOptions): Promise<EnsembleSynthesizeResult> {
        const session = (options?.session ?? '').trim();
        if (!session) {
            throw new Error('Session name is required to synthesize an ensemble');
        }

        const args: string[] = [`--robot-ensemble-synthesize=${session}`];
        const strategy = (options?.strategy ?? '').trim();
        if (strategy) {
            args.push(`--synth-strategy=${strategy}`);
        }
        const format = (options?.format ?? '').trim();
        if (format) {
            args.push(`--synth-format=${format}`);
        }
        const output = (options?.output ?? '').trim();
        if (output) {
            args.push(`--synth-output=${output}`);
        }
        if (options?.force) {
            args.push('--synth-force');
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeEnsembleSynthesize(JSON.parse(stdout));
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

    /**
     * Fetch live swarm backpressure, utilization, load-shedding contract, and overload heatmap
     * via `ntm --robot-backpressure [--session=<session>] --robot-format=json`.
     */
    async getBackpressure(session?: string): Promise<BackpressureResult> {
        const args: string[] = ['--robot-backpressure'];
        const trimmedSession = (session ?? '').trim();
        if (trimmedSession) {
            args.push(`--session=${trimmedSession}`);
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeBackpressure(JSON.parse(stdout));
    }

    /**
     * List isolated per-agent git worktrees via `ntm worktrees list [--session=<session>] --json`.
     */
    async listWorktrees(session?: string): Promise<WorktreeListResult> {
        const args: string[] = ['worktrees', 'list'];
        const trimmedSession = (session ?? '').trim();
        if (trimmedSession) {
            args.push(`--session=${trimmedSession}`);
        }
        args.push('--json');

        const stdout = await this.run(args);
        return normalizeWorktreeList(JSON.parse(stdout));
    }

    /**
     * List durable operator incidents via `ntm attention incidents list --json`.
     */
    async listIncidents(options?: IncidentListOptions): Promise<IncidentItem[]> {
        const args: string[] = ['attention', 'incidents', 'list'];
        if (options?.limit !== undefined) {
            if (!Number.isInteger(options.limit) || options.limit < 1) {
                throw new Error('Incident list limit must be a positive integer');
            }
            args.push(`--limit=${options.limit}`);
        }
        if (options?.includeAll) {
            args.push('--all');
        }
        const status = (options?.status ?? '').trim();
        if (status) {
            args.push(`--status=${status}`);
        }
        args.push('--json');

        const stdout = await this.run(args);
        return normalizeIncidentList(JSON.parse(stdout));
    }

    /**
     * Transition a durable operator incident via `ntm attention incidents <action> <id> [--reason=...] --json`.
     */
    async mutateIncident(
        action: IncidentAction,
        incidentId: string,
        reason?: string
    ): Promise<IncidentMutationResult> {
        if (!SUPPORTED_INCIDENT_ACTIONS.includes(action)) {
            throw new Error(`Unsupported incident action: ${String(action)}`);
        }
        const id = (incidentId ?? '').trim();
        if (!id) {
            throw new Error('Incident ID is required');
        }
        const args: string[] = ['attention', 'incidents', action, id];
        const trimmedReason = (reason ?? '').trim();
        if (trimmedReason) {
            args.push(`--reason=${trimmedReason}`);
        }
        args.push('--json');

        const stdout = await this.run(args);
        return normalizeIncidentMutation(JSON.parse(stdout));
    }

    /**
     * Inspect recent handoffs, latest Goal/Now handoff, quality report, and continuity ledger
     * via `ntm --robot-handoff=<session> --robot-format=json`.
     */
    async getHandoff(
        session: string,
        options?: { limit?: number; projectDir?: string }
    ): Promise<HandoffResult> {
        const sess = (session ?? '').trim();
        if (!sess) {
            throw new Error('Session name is required to inspect handoffs');
        }
        const args: string[] = [`--robot-handoff=${sess}`];
        if (options?.limit !== undefined) {
            if (!Number.isInteger(options.limit) || options.limit < 1) {
                throw new Error('Handoff limit must be a positive integer');
            }
            args.push(`--handoff-limit=${options.limit}`);
        }
        const projectDir = (options?.projectDir ?? '').trim();
        if (projectDir) {
            args.push(`--handoff-project-dir=${projectDir}`);
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeHandoffResult(JSON.parse(stdout));
    }

    /**
     * Create a manual or auto-generated session handoff via `ntm --robot-handoff=<session> --handoff-create`.
     */
    async createHandoff(options: HandoffCreateOptions): Promise<HandoffResult> {
        const sess = (options?.session ?? '').trim();
        if (!sess) {
            throw new Error('Session name is required to create a handoff');
        }
        const args: string[] = [`--robot-handoff=${sess}`, '--handoff-create'];
        if (options?.auto) {
            args.push('--handoff-auto');
        } else {
            const goal = (options?.goal ?? '').trim();
            const now = (options?.now ?? '').trim();
            if (!goal || !now) {
                throw new Error('Both goal and now are required for a manual handoff');
            }
            args.push(`--handoff-goal=${goal}`, `--handoff-now=${now}`);
            const desc = (options?.description ?? '').trim();
            if (desc) {
                args.push(`--handoff-description=${desc}`);
            }
        }
        const projectDir = (options?.projectDir ?? '').trim();
        if (projectDir) {
            args.push(`--handoff-project-dir=${projectDir}`);
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeHandoffResult(JSON.parse(stdout));
    }

    /**
     * Fetch multi-harness event analytics (spawns, prompts, token estimates, crashes,
     * restarts, error distribution, and per-session summaries) via `ntm --robot-analytics`.
     */
    async getAnalytics(options?: AnalyticsOptions): Promise<AnalyticsResult> {
        const args: string[] = ['--robot-analytics'];
        const since = (options?.since ?? '').trim();
        if (since) {
            args.push(`--analytics-since=${since}`);
        } else if (options?.days !== undefined) {
            if (!Number.isInteger(options.days) || options.days < 0) {
                throw new Error('Analytics days must be a non-negative integer');
            }
            args.push(`--analytics-days=${options.days}`);
        }
        if (options?.sessions) {
            args.push('--analytics-sessions');
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeAnalyticsResult(JSON.parse(stdout));
    }

    /**
     * Inspect, search, verify tamper-evident hash chains, or query projection source health
     * via `ntm --robot-audit[=<session>] ... --robot-format=json`.
     */
    async getAudit(options?: AuditOptions): Promise<AuditResult> {
        const sess = (options?.session ?? '').trim();
        const args: string[] = [
            sess && sess !== 'all' ? `--robot-audit=${sess}` : '--robot-audit',
        ];

        const action = (options?.action ?? '').trim();
        if (action) {
            args.push(`--audit-action=${action}`);
        }
        const search = (options?.search ?? '').trim();
        if (search) {
            args.push(`--audit-search=${search}`);
        }
        if (options?.verify) {
            args.push('--audit-verify');
        }
        if (options?.sources) {
            args.push('--audit-sources');
        }
        const eventTypes = (options?.eventTypes ?? '').trim();
        if (eventTypes) {
            args.push(`--audit-type=${eventTypes}`);
        }
        const actors = (options?.actors ?? '').trim();
        if (actors) {
            args.push(`--audit-actor=${actors}`);
        }
        const since = (options?.since ?? '').trim();
        if (since) {
            args.push(`--audit-since=${since}`);
        }
        if (options?.days !== undefined) {
            if (!Number.isInteger(options.days) || options.days < 0) {
                throw new Error('Audit days must be a non-negative integer');
            }
            args.push(`--audit-days=${options.days}`);
        }
        if (options?.limit !== undefined) {
            if (!Number.isInteger(options.limit) || options.limit < 1) {
                throw new Error('Audit limit must be a positive integer');
            }
            args.push(`--audit-limit=${options.limit}`);
        }
        if (options?.offset !== undefined) {
            if (!Number.isInteger(options.offset) || options.offset < 0) {
                throw new Error('Audit offset must be a non-negative integer');
            }
            args.push(`--audit-offset=${options.offset}`);
        }
        args.push('--robot-format=json');

        const allowVerifyEnvelope = Boolean(options?.verify || action === 'verify');
        const stdout = await this.run(args, { allowJsonErrorEnvelope: allowVerifyEnvelope });
        return normalizeAuditResult(JSON.parse(stdout));
    }

    /**
     * Generate a disclosure-redacted diagnostic support bundle archive via
     * `ntm --robot-support-bundle[=<session>] [--all] ... --robot-format=json`.
     */
    async generateSupportBundle(options?: SupportBundleOptions): Promise<SupportBundleResult> {
        const sess = (options?.session ?? '').trim();
        const args: string[] = [
            sess ? `--robot-support-bundle=${sess}` : '--robot-support-bundle',
        ];
        if (!sess && (options?.all ?? true)) {
            args.push('--all');
        }
        const outputPath = (options?.outputPath ?? '').trim();
        if (outputPath) {
            args.push(`--bundle-output=${outputPath}`);
        }
        const format = options?.format ?? 'zip';
        if (format !== 'zip' && format !== 'tar.gz') {
            throw new Error(`Unsupported support bundle format: ${String(format)}`);
        }
        args.push(`--bundle-format=${format}`);

        const since = (options?.since ?? '').trim();
        if (since) {
            args.push(`--bundle-since=${since}`);
        }
        if (options?.lines !== undefined) {
            if (!Number.isInteger(options.lines) || options.lines < 1) {
                throw new Error('Support bundle lines must be a positive integer');
            }
            args.push(`--bundle-lines=${options.lines}`);
        }
        if (options?.maxSizeMB !== undefined) {
            if (!Number.isInteger(options.maxSizeMB) || options.maxSizeMB < 1) {
                throw new Error('Support bundle maxSizeMB must be a positive integer');
            }
            args.push(`--bundle-max-size=${options.maxSizeMB}`);
        }
        const redactMode = options?.redactMode ?? 'redact';
        if (redactMode !== 'redact' && redactMode !== 'block' && redactMode !== 'warn') {
            throw new Error(`Unsupported support bundle redaction mode: ${String(redactMode)}`);
        }
        args.push(`--bundle-redact=${redactMode}`);
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeSupportBundleResult(JSON.parse(stdout));
    }

    /**
     * Run ecosystem diagnostics (dependencies, tools, daemons, invariants, safety defaults,
     * and optional SQLite/Beads lock contention scan) via `ntm --robot-doctor`.
     */
    async getDoctor(options?: DoctorOptions): Promise<DoctorResult> {
        const args: string[] = ['--robot-doctor'];
        if (options?.contention) {
            args.push('--doctor-contention');
        }
        if (options?.verbose) {
            args.push('--doctor-verbose');
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args, { allowJsonErrorEnvelope: true });
        return normalizeDoctorResult(JSON.parse(stdout));
    }

    /**
     * Lint and safety-check a prompt before dispatching to agents via
     * `ntm --robot-preflight=<prompt> [--preflight-strict] --robot-format=json`.
     */
    async preflightPrompt(options: PreflightOptions): Promise<PreflightResult> {
        const filePath = (options?.filePath ?? '').trim();
        const prompt = (options?.prompt ?? '').trim();
        if (!filePath && !prompt) {
            throw new Error('Either prompt text or filePath is required for preflight');
        }
        const args: string[] = filePath
            ? ['--robot-preflight', `--preflight-file=${filePath}`]
            : [`--robot-preflight=${prompt}`];
        if (options?.strict) {
            args.push('--preflight-strict');
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args, { allowJsonErrorEnvelope: true });
        return normalizePreflightResult(JSON.parse(stdout));
    }

    /**
     * Recommend ready-bead to idle-agent pairings via
     * `ntm --robot-assign=<session> [--strategy=...] [--beads=...] [--relax] --robot-format=json`.
     */
    async getAssign(options: AssignWorkOptions): Promise<AssignWorkResult> {
        const sess = (options?.session ?? '').trim();
        if (!sess) {
            throw new Error('Session name is required to recommend assignments');
        }
        const args: string[] = [`--robot-assign=${sess}`];
        const strategy = options?.strategy;
        if (strategy !== undefined) {
            if (!SUPPORTED_ASSIGN_STRATEGIES.includes(strategy)) {
                throw new Error(`Unsupported assignment strategy: ${String(strategy)}`);
            }
            args.push(`--strategy=${strategy}`);
        }
        const beads = (options?.beads ?? '').trim();
        if (beads) {
            args.push(`--beads=${beads}`);
        }
        if (options?.relax) {
            args.push('--relax');
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeAssignWorkResult(JSON.parse(stdout));
    }

    /**
     * Preview or execute multi-harness session scaling across all 11 supported harnesses via
     * `ntm --robot-scale=<session> [--scale-<harness>=N ...] [--dry-run] [--force] --robot-format=json`.
     */
    async scaleSession(options: ScaleSessionOptions): Promise<ScaleSessionResult> {
        const sess = (options?.session ?? '').trim();
        if (!sess) {
            throw new Error('Session name is required to scale agents');
        }
        const args: string[] = [`--robot-scale=${sess}`];
        let targetCount = 0;
        for (const harness of SUPPORTED_SCALE_HARNESSES) {
            const count = options?.targets?.[harness];
            if (count !== undefined) {
                if (!Number.isInteger(count) || count < 0) {
                    throw new Error(`Scale target count for ${harness} must be a non-negative integer`);
                }
                args.push(`--scale-${harness}=${count}`);
                targetCount += 1;
            }
        }
        if (targetCount === 0) {
            throw new Error('At least one harness target count is required to scale a session');
        }
        if (options?.dryRun) {
            args.push('--dry-run');
        }
        if (options?.force) {
            args.push('--force');
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeScaleResult(JSON.parse(stdout));
    }

    /**
     * Analyze or apply workload / geometry rebalancing across session panes via
     * `ntm --robot-rebalance=<session> [--rebalance-apply] [--rebalance-threshold=...] [--rebalance-geometry] [--dry-run] --robot-format=json`.
     */
    async rebalanceSession(options: RebalanceSessionOptions): Promise<RebalanceSessionResult> {
        const sess = (options?.session ?? '').trim();
        if (!sess) {
            throw new Error('Session name is required to rebalance workload');
        }
        const args: string[] = [`--robot-rebalance=${sess}`];
        if (options?.apply) {
            args.push('--rebalance-apply');
        }
        if (options?.threshold !== undefined) {
            if (!Number.isFinite(options.threshold) || options.threshold < 0) {
                throw new Error('Rebalance threshold must be a non-negative number');
            }
            args.push(`--rebalance-threshold=${options.threshold}`);
        }
        if (options?.geometry) {
            args.push('--rebalance-geometry');
        }
        if (options?.geometryTolerance !== undefined) {
            if (!Number.isFinite(options.geometryTolerance) || options.geometryTolerance <= 0) {
                throw new Error('Rebalance geometry tolerance must be a positive number');
            }
            args.push(`--rebalance-geometry-tolerance=${options.geometryTolerance}`);
        }
        const filterType = (options?.filterType ?? '').trim();
        if (filterType) {
            args.push(`--type=${filterType}`);
        }
        if (options?.dryRun) {
            args.push('--dry-run');
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeRebalanceResult(JSON.parse(stdout));
    }

    /**
     * Generate or dispatch cross-agent peer review prompts for idle panes via
     * `ntm --robot-review-queue=<session> [--review-dispatch] [--review-pane=N] [--review-project-dir=...] --robot-format=json`.
     */
    async getReviewQueue(options: ReviewQueueOptions): Promise<ReviewQueueResult> {
        const sess = (options?.session ?? '').trim();
        if (!sess) {
            throw new Error('Session name is required to build a peer review queue');
        }
        const args: string[] = [`--robot-review-queue=${sess}`];
        if (options?.dispatch) {
            args.push('--review-dispatch');
        }
        if (options?.pane !== undefined) {
            if (!Number.isInteger(options.pane) || options.pane < 0) {
                throw new Error('Review target pane must be a non-negative integer');
            }
            args.push(`--review-pane=${options.pane}`);
        }
        const projectDir = (options?.projectDir ?? '').trim();
        if (projectDir) {
            args.push(`--review-project-dir=${projectDir}`);
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeReviewQueueResult(JSON.parse(stdout));
    }

    /**
     * Summarize recent agent activity, modified files, commands run, and detected file conflicts via
     * `ntm --robot-summary=<session> [--since=...] --robot-format=json`.
     */
    async getSummary(options: SessionSummaryOptions): Promise<SessionSummaryResult> {
        const sess = (options?.session ?? '').trim();
        if (!sess) {
            throw new Error('Session name is required to summarize session activity');
        }
        const args: string[] = [`--robot-summary=${sess}`];
        const since = (options?.since ?? '').trim();
        if (since) {
            args.push(`--since=${since}`);
        }
        args.push('--robot-format=json');

        const stdout = await this.run(args);
        return normalizeSummaryResult(JSON.parse(stdout));
    }
}
