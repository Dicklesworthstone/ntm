import * as crypto from 'crypto';
import * as vscode from 'vscode';
import {
    AlertDetail,
    BeadsPreviewItem,
    BeadsStatus,
    MailAgent,
    MailReservation,
    NtmClient,
    RobotMail,
    RobotStatus,
    SessionInfo,
} from './ntmClient';

export interface DashboardRenderOptions {
    nonce?: string;
    workspaceName?: string;
    errorMessage?: string;
}

/**
 * Generate a cryptographic 128-bit random base64 nonce for per-render Webview CSP.
 */
export function generateNonce(): string {
    return crypto.randomBytes(16).toString('base64');
}

/**
 * Escape untrusted text for safe HTML element content interpolation.
 */
export function escapeHtml(value: unknown): string {
    if (value === null || value === undefined) {
        return '';
    }
    return String(value)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

/**
 * Escape untrusted text for safe HTML quoted attribute interpolation.
 */
export function escapeAttr(value: unknown): string {
    return escapeHtml(value)
        .replace(/`/g, '&#96;')
        .replace(/\r/g, '&#13;')
        .replace(/\n/g, '&#10;');
}

function pickPrimarySession(status: RobotStatus, workspaceName?: string): SessionInfo | undefined {
    const sessions = status.sessions || [];
    if (workspaceName) {
        const match = sessions.find(s => s.name === workspaceName);
        if (match) {
            return match;
        }
    }
    return sessions.find(s => s.attached) ?? sessions[0];
}

function renderSessionCard(session: SessionInfo): string {
    const agents = session.agents || [];
    const agentCount = agents.length || session.agent_count || 0;
    const paneCount = session.panes ?? agentCount;

    const agentsHtml = agents.length > 0
        ? agents.map(a => {
            const statusClass = a.is_active ? 'status-active' : 'status-inactive';
            const stateBadge = a.state ? `<span class="tag">${escapeHtml(a.state)}</span>` : '';
            const beadBadge = a.current_bead ? `<span class="tag mono">${escapeHtml(a.current_bead)}</span>` : '';
            return `<div class="agent">
                <div style="display:flex; align-items:center; flex-wrap:wrap; gap:4px;">
                    <div class="status-dot ${statusClass}"></div>
                    <span>${escapeHtml(a.pane)}</span>
                    <span class="tag">${escapeHtml(a.type)}</span>
                    ${stateBadge}
                    ${beadBadge}
                </div>
                <span class="muted">${escapeHtml(a.variant ?? '')}</span>
            </div>`;
        }).join('')
        : '<div class="muted">No agents</div>';

    const attachedTag = session.attached
        ? '<span class="tag" style="background-color: var(--vscode-testing-iconPassed)">Attached</span>'
        : '<span class="tag">Detached</span>';

    const healthTag = session.health?.status && session.health.status !== 'healthy'
        ? `<span class="tag" title="${escapeAttr(session.health.reason ?? '')}">${escapeHtml(session.health.status)}</span>`
        : '';

    const escapedSessionAttr = escapeAttr(session.name);

    return `
        <div class="card">
            <div class="card-header">
                <span>${escapeHtml(session.name)}</span>
                <div style="display:flex; align-items:center; gap:4px;">
                    ${healthTag}
                    ${attachedTag}
                </div>
            </div>
            <div class="muted" style="margin-bottom:8px;">${escapeHtml(agentCount)} agents · ${escapeHtml(paneCount)} panes</div>
            <div class="card-actions">
                <button type="button" class="action-btn" data-action="attach" data-session="${escapedSessionAttr}">Attach</button>
                <button type="button" class="action-btn" data-action="dashboard" data-session="${escapedSessionAttr}">Dashboard</button>
                <button type="button" class="action-btn" data-action="send" data-session="${escapedSessionAttr}">Send</button>
            </div>
            <div class="agent-list">
                ${agentsHtml}
            </div>
        </div>
    `;
}

function renderMailAgent(agent: MailAgent): string {
    const urgent = agent.urgent_count || 0;
    const unread = agent.unread_count || 0;
    const dotClass = urgent > 0 ? 'status-inactive' : unread > 0 ? 'status-active' : '';
    const displayName = agent.agent_name || agent.name || 'Unknown';
    const metaParts = [
        agent.program ? escapeHtml(agent.program) : '',
        agent.model ? escapeHtml(agent.model) : '',
        agent.pane ? escapeHtml(agent.pane) : '',
    ].filter(Boolean);

    return `
        <div class="agent">
            <div style="display:flex; align-items:center; gap:6px;">
                <div class="status-dot ${dotClass}"></div>
                <div>
                    <div>${escapeHtml(displayName)}</div>
                    <div class="muted">${metaParts.join(' · ')}</div>
                </div>
            </div>
            <div class="muted">${escapeHtml(unread)} unread / ${escapeHtml(urgent)} urgent</div>
        </div>
    `;
}

function renderMail(mail?: RobotMail): string {
    if (!mail) {
        return '<div class="muted">Mail status unavailable.</div>';
    }
    if (!mail.available) {
        return '<div class="muted">Mail server offline.</div>';
    }
    if (mail.error) {
        return `<div class="muted">Mail error: ${escapeHtml(mail.error)}</div>`;
    }
    const agents = mail.agents || [];
    if (!agents.length) {
        return '<div class="muted">No registered agents.</div>';
    }
    return agents.map(a => renderMailAgent(a)).join('');
}

function formatReservationExpiry(res: MailReservation): string {
    if (res.expires_in !== undefined && res.expires_in !== null && res.expires_in !== '') {
        return escapeHtml(res.expires_in);
    }
    if (typeof res.expires_in_seconds === 'number' && res.expires_in_seconds > 0) {
        return escapeHtml(`${Math.round(res.expires_in_seconds / 60)}m`);
    }
    return '';
}

function renderReservations(reservations: MailReservation[]): string {
    if (!reservations.length) {
        return '<div class="muted">No active file reservations.</div>';
    }
    const rows = reservations.slice(0, 20).map(res => {
        const mode = res.exclusive ? 'exclusive' : 'shared';
        const reasonAttr = res.reason ? ` title="${escapeAttr(res.reason)}"` : '';
        return `
            <tr${reasonAttr}>
                <td class="mono">${escapeHtml(res.pattern)}</td>
                <td>${escapeHtml(res.agent)}</td>
                <td class="muted">${mode}</td>
                <td class="muted">${formatReservationExpiry(res)}</td>
            </tr>
        `;
    }).join('');
    return `
        <table>
            <tr><th>Pattern</th><th>Agent</th><th>Mode</th><th>Expires</th></tr>
            ${rows}
        </table>
    `;
}

function renderBeadList(items: BeadsPreviewItem[] | undefined): string {
    if (!items || items.length === 0) {
        return '';
    }
    const rows = items.slice(0, 10).map(item => {
        const priorityTag = item.priority !== undefined && item.priority !== ''
            ? `<span class="tag">${escapeHtml(item.priority)}</span>`
            : '';
        return `<div class="agent">
            <div>
                <span class="mono">${escapeHtml(item.id)}</span>
                ${priorityTag}
                <span>${escapeHtml(item.title)}</span>
            </div>
            <span class="muted">${escapeHtml(item.assignee ?? item.type ?? '')}</span>
        </div>`;
    }).join('');
    return `<div class="agent-list">${rows}</div>`;
}

function renderBeadsCard(beads?: BeadsStatus): string {
    if (!beads || !beads.available) {
        return '';
    }
    const previewItems = [
        ...(beads.in_progress_list ?? []),
        ...(beads.ready_preview ?? []),
    ];
    return `
        <div class="card" style="margin-bottom:16px;">
            <div class="card-header">
                <span>Beads Work Queue</span>
                <span class="pill">${escapeHtml(beads.ready)} ready · ${escapeHtml(beads.in_progress)} active · ${escapeHtml(beads.open)} open</span>
            </div>
            ${renderBeadList(previewItems) || '<div class="muted">No active or ready beads listed.</div>'}
        </div>
    `;
}

function renderAlertsBanner(alerts?: AlertDetail[], errorMessage?: string): string {
    const blocks: string[] = [];
    if (errorMessage) {
        blocks.push(`<div class="alert-banner">${escapeHtml(errorMessage)}</div>`);
    }
    if (alerts && alerts.length > 0) {
        for (const alert of alerts.slice(0, 5)) {
            const prefix = alert.severity ? `[${escapeHtml(alert.severity)}] ` : '';
            const scope = alert.session ? ` (${escapeHtml(alert.session)}${alert.pane ? ':' + escapeHtml(alert.pane) : ''})` : '';
            blocks.push(`<div class="alert-banner">${prefix}${escapeHtml(alert.message)}${scope}</div>`);
        }
    }
    return blocks.join('');
}

/**
 * Pure, hermetic HTML renderer for the NTM Dashboard Webview with strict per-render nonce CSP.
 */
export function renderDashboardHtml(
    status: RobotStatus,
    mail?: RobotMail,
    options: DashboardRenderOptions = {}
): string {
    const nonce = options.nonce ?? generateNonce();
    const escapedNonce = escapeAttr(nonce);

    const style = `
        body {
            font-family: var(--vscode-font-family);
            padding: 18px;
            color: var(--vscode-editor-foreground);
            background-color: var(--vscode-editor-background);
        }
        .grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
            gap: 16px;
        }
        .card {
            background-color: var(--vscode-sideBar-background);
            border: 1px solid var(--vscode-widget-border);
            padding: 14px;
            border-radius: 6px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.15);
        }
        .card-header {
            font-weight: bold;
            margin-bottom: 8px;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        .card-actions {
            display: flex;
            gap: 6px;
            margin-bottom: 8px;
            flex-wrap: wrap;
        }
        .agent-list {
            margin-top: 8px;
        }
        .agent {
            display: flex;
            align-items: center;
            margin-bottom: 5px;
            padding: 4px;
            background-color: var(--vscode-editor-lineHighlightBackground);
            border-radius: 4px;
            justify-content: space-between;
            gap: 8px;
        }
        .status-dot {
            width: 8px;
            height: 8px;
            border-radius: 50%;
            margin-right: 4px;
            flex-shrink: 0;
        }
        .status-active { background-color: var(--vscode-testing-iconPassed); }
        .status-inactive { background-color: var(--vscode-testing-iconSkipped); }
        .refresh-btn, .action-btn {
            background-color: var(--vscode-button-background);
            color: var(--vscode-button-foreground);
            border: none;
            padding: 8px 12px;
            cursor: pointer;
            border-radius: 4px;
        }
        .refresh-btn {
            margin-left: 8px;
        }
        .action-btn {
            padding: 4px 8px;
            font-size: 12px;
        }
        .refresh-btn:hover, .action-btn:hover {
            background-color: var(--vscode-button-hoverBackground);
        }
        .tag {
            font-size: 0.8em;
            padding: 2px 6px;
            border-radius: 10px;
            background-color: var(--vscode-badge-background);
            color: var(--vscode-badge-foreground);
            margin-left: 4px;
        }
        .pill {
            display: inline-flex;
            align-items: center;
            gap: 6px;
            padding: 4px 8px;
            border-radius: 999px;
            background-color: var(--vscode-badge-background);
            color: var(--vscode-badge-foreground);
        }
        .alert-banner {
            margin-bottom: 12px;
            padding: 8px 12px;
            border-radius: 4px;
            border: 1px solid var(--vscode-inputValidation-warningBorder);
            background-color: var(--vscode-inputValidation-warningBackground);
        }
        table { width: 100%; border-collapse: collapse; }
        th, td { padding: 6px 4px; text-align: left; }
        th { color: var(--vscode-descriptionForeground); font-weight: 600; font-size: 12px; }
        tr + tr td { border-top: 1px solid var(--vscode-widget-border); }
        .muted { color: var(--vscode-descriptionForeground); }
        .mono { font-family: var(--vscode-editor-font-family); }
    `;

    const sessions = status.sessions || [];
    const sessionsHtml = sessions.length > 0
        ? sessions.map(s => renderSessionCard(s)).join('')
        : '<div class="muted">No active sessions.</div>';
    const primary = pickPrimarySession(status, options.workspaceName);
    const primarySessionAttr = escapeAttr(primary?.name ?? '');
    const mailOnline = mail?.available ?? status.agent_mail?.available ?? false;
    const mailHtml = renderMail(mail);
    const reservations = mail?.file_reservations || [];
    const lockHtml = renderReservations(reservations);
    const beadsCardHtml = renderBeadsCard(status.beads);
    const alertsHtml = renderAlertsBanner(status.alerts, options.errorMessage);

    const beadsInlineSummary = status.beads?.available
        ? ` • Beads: ${escapeHtml(status.beads.open)} open / ${escapeHtml(status.beads.in_progress)} active`
        : '';
    const mailInlineSummary = (status.agent_mail?.available || mailOnline) ? ' • Mail online' : '';

    return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-${escapedNonce}';">
    <style>${style}</style>
</head>
<body>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; flex-wrap: wrap; gap: 8px;">
        <div>
            <h1 style="margin:0;">NTM Dashboard</h1>
            <div class="muted" style="margin-top:4px;">${escapeHtml(status.summary.total_sessions)} sessions • ${escapeHtml(status.summary.total_agents)} agents${beadsInlineSummary}${mailInlineSummary}</div>
        </div>
        <div style="display:flex; align-items:center; flex-wrap:wrap; gap:4px;">
            <button type="button" class="refresh-btn" data-action="refresh">Refresh</button>
            <button type="button" class="refresh-btn" data-action="openPalette" data-session="${primarySessionAttr}">${primary ? `Palette (${escapeHtml(primary.name)})` : 'Open Palette'}</button>
            <button type="button" class="refresh-btn" data-action="dashboard" data-session="${primarySessionAttr}">TUI Dashboard</button>
            <button type="button" class="refresh-btn" data-action="send" data-session="${primarySessionAttr}">Send Prompt</button>
            <button type="button" class="refresh-btn" data-action="spawn">Spawn Session</button>
        </div>
    </div>

    ${alertsHtml}

    <div class="grid" style="margin-bottom:16px;">
        <div class="card">
            <div class="card-header">
                <span>Sessions</span>
                <span class="pill">${escapeHtml(status.summary.total_sessions)} total</span>
            </div>
            <div class="grid">
                ${sessionsHtml}
            </div>
        </div>
        <div class="card">
            <div class="card-header">
                <span>Agent Mail</span>
                <span class="pill">${mailOnline ? 'Online' : 'Offline'}</span>
            </div>
            ${mailHtml}
        </div>
    </div>

    ${beadsCardHtml}

    <div class="card">
        <div class="card-header">
            <span>File Reservations</span>
            <span class="pill">${escapeHtml(reservations.length)} active</span>
        </div>
        ${lockHtml}
    </div>

    <script nonce="${escapedNonce}">
        const vscodeApi = acquireVsCodeApi();
        document.addEventListener('click', (event) => {
            const origin = event.target instanceof Element ? event.target.closest('[data-action]') : null;
            if (!origin) {
                return;
            }
            const action = origin.getAttribute('data-action');
            const session = origin.getAttribute('data-session') || undefined;
            if (action) {
                vscodeApi.postMessage({ command: action, session });
            }
        });
    </script>
</body>
</html>`;
}

export class NtmDashboard {
    public static currentPanel: NtmDashboard | undefined;
    private readonly panel: vscode.WebviewPanel;
    private disposables: vscode.Disposable[] = [];
    private readonly client: NtmClient;

    public static createOrShow(extensionUri: vscode.Uri, client: NtmClient) {
        const column = vscode.window.activeTextEditor
            ? vscode.window.activeTextEditor.viewColumn
            : undefined;

        if (NtmDashboard.currentPanel) {
            NtmDashboard.currentPanel.panel.reveal(column);
            return;
        }

        const panel = vscode.window.createWebviewPanel(
            'ntmDashboard',
            'NTM Dashboard',
            column || vscode.ViewColumn.One,
            {
                enableScripts: true,
                localResourceRoots: [vscode.Uri.joinPath(extensionUri, 'media')],
                retainContextWhenHidden: true,
            }
        );

        NtmDashboard.currentPanel = new NtmDashboard(panel, client);
    }

    private constructor(panel: vscode.WebviewPanel, client: NtmClient) {
        this.panel = panel;
        this.client = client;

        this.update();

        this.panel.onDidDispose(() => this.dispose(), null, this.disposables);

        this.panel.webview.onDidReceiveMessage(
            (message: { command?: string; session?: string }) => {
                const sessionArg = typeof message?.session === 'string' && message.session.length > 0
                    ? message.session
                    : undefined;
                switch (message?.command) {
                    case 'refresh':
                        this.update();
                        break;
                    case 'spawn':
                        vscode.commands.executeCommand('ntm.spawn');
                        break;
                    case 'openPalette':
                        vscode.commands.executeCommand('ntm.openPalette', sessionArg);
                        break;
                    case 'attach':
                        vscode.commands.executeCommand('ntm.openTerminal', sessionArg);
                        break;
                    case 'dashboard':
                        vscode.commands.executeCommand('ntm.terminalDashboard', sessionArg);
                        break;
                    case 'send':
                        vscode.commands.executeCommand('ntm.sendPrompt', sessionArg);
                        break;
                    default:
                        break;
                }
            },
            null,
            this.disposables
        );

        const interval = setInterval(() => this.update(), 10000);
        this.disposables.push({ dispose: () => clearInterval(interval) });
    }

    public dispose() {
        NtmDashboard.currentPanel = undefined;
        this.panel.dispose();
        while (this.disposables.length) {
            const x = this.disposables.pop();
            if (x) {
                x.dispose();
            }
        }
    }

    private async update() {
        try {
            const [status, mail] = await Promise.all([
                this.client.getStatus(),
                this.client.getMail().catch(() => undefined),
            ]);
            this.panel.webview.html = this.getHtmlForWebview(status, mail);
        } catch (e) {
            const emptyStatus: RobotStatus = {
                generated_at: new Date().toISOString(),
                system: { version: 'unknown', os: process.platform },
                sessions: [],
                summary: { total_sessions: 0, total_agents: 0, attached_count: 0 },
            };
            const errMsg = e instanceof Error ? e.message : String(e);
            this.panel.webview.html = this.getHtmlForWebview(emptyStatus, undefined, errMsg);
        }
    }

    public getHtmlForWebview(status: RobotStatus, mail?: RobotMail, errorMessage?: string): string {
        return renderDashboardHtml(status, mail, {
            workspaceName: vscode.workspace.name,
            errorMessage,
        });
    }
}
