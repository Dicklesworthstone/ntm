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

const SAFE_BARE_SHELL_ARG = /^[A-Za-z0-9_./:@][A-Za-z0-9_./:@=-]*$/;
const ACTIVE_AGENT_STATES = new Set(['active', 'working', 'busy', 'running']);
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
 */
export function buildNtmTerminalCommand(binaryPath: string, subcommand: string, ...args: string[]): string {
    const bin = (binaryPath ?? '').trim() || 'ntm';
    return [bin, subcommand, ...args].map(shellQuote).join(' ');
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
            const detail = e.stderr ? `: ${e.stderr}` : (e.message ? `: ${e.message}` : '');
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
}
