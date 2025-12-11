import * as vscode from 'vscode';
import { MailAgent, MailLock, NtmClient, RobotMail, RobotStatus, SessionInfo } from './ntmClient';

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
            message => {
                switch (message.command) {
                    case 'refresh':
                        this.update();
                        break;
                    case 'spawn':
                        vscode.commands.executeCommand('ntm.spawn');
                        break;
                    case 'openPalette':
                        vscode.commands.executeCommand('ntm.openPalette');
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
            console.error(e);
        }
    }

    private getHtmlForWebview(status: RobotStatus, mail?: RobotMail) {
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
            }
            .status-dot {
                width: 8px;
                height: 8px;
                border-radius: 50%;
                margin-right: 8px;
            }
            .status-active { background-color: var(--vscode-testing-iconPassed); }
            .status-inactive { background-color: var(--vscode-testing-iconSkipped); }
            
            .refresh-btn {
                background-color: var(--vscode-button-background);
                color: var(--vscode-button-foreground);
                border: none;
                padding: 8px 12px;
                cursor: pointer;
                border-radius: 4px;
                margin-left: 8px;
            }
            .refresh-btn:hover {
                background-color: var(--vscode-button-hoverBackground);
            }
            .tag {
                font-size: 0.8em;
                padding: 2px 6px;
                border-radius: 10px;
                background-color: var(--vscode-badge-background);
                color: var(--vscode-badge-foreground);
                margin-left: 5px;
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
            table { width: 100%; border-collapse: collapse; }
            th, td { padding: 6px 4px; text-align: left; }
            th { color: var(--vscode-descriptionForeground); font-weight: 600; font-size: 12px; }
            tr + tr td { border-top: 1px solid var(--vscode-widget-border); }
            .muted { color: var(--vscode-descriptionForeground); }
            .mono { font-family: var(--vscode-editor-font-family); }
        `;

        const sessionsHtml = status.sessions.map(s => this.renderSessionCard(s)).join('');
        const primary = this.pickPrimarySession(status);
        const mailHtml = this.renderMail(mail);
        const lockHtml = this.renderLocks(mail?.locks || []);

        return `<!DOCTYPE html>
            <html lang="en">
            <head>
                <meta charset="UTF-8">
                <style>${style}</style>
            </head>
            <body>
                <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px;">
                    <div>
                        <h1 style="margin:0;">NTM Dashboard</h1>
                        <div class="muted" style="margin-top:4px;">${status.summary.total_sessions} sessions • ${status.summary.total_agents} agents${status.beads?.available ? ` • Beads: ${status.beads.open} open / ${status.beads.in_progress} active` : ''}${status.agent_mail?.available ? ' • Mail online' : ''}</div>
                    </div>
                    <div style="display:flex; align-items:center;">
                        <button class="refresh-btn" id="refreshBtn">Refresh</button>
                        <button class="refresh-btn" id="paletteBtn">${primary ? `Palette (${primary.name})` : 'Open Palette'}</button>
                        <button class="refresh-btn" id="spawnBtn">Spawn Session</button>
                    </div>
                </div>

                <div class="grid" style="margin-bottom:16px;">
                    <div class="card">
                        <div class="card-header">
                            <span>Sessions</span>
                            <span class="pill">${status.summary.total_sessions} total</span>
                        </div>
                        <div class="grid">
                            ${sessionsHtml}
                        </div>
                    </div>
                    <div class="card">
                        <div class="card-header">
                            <span>Agent Mail</span>
                            <span class="pill">${mail?.available ? 'Online' : 'Offline'}</span>
                        </div>
                        ${mailHtml}
                    </div>
                </div>

                <div class="card">
                    <div class="card-header">
                        <span>Locks</span>
                        <span class="pill">${mail?.locks?.length || 0} active</span>
                    </div>
                    ${lockHtml}
                </div>

                <script>
                    const vscodeApi = acquireVsCodeApi();
                    document.getElementById('refreshBtn')?.addEventListener('click', () => vscodeApi.postMessage({command: 'refresh'}));
                    document.getElementById('paletteBtn')?.addEventListener('click', () => vscodeApi.postMessage({command: 'openPalette'}));
                    document.getElementById('spawnBtn')?.addEventListener('click', () => vscodeApi.postMessage({command: 'spawn'}));
                </script>
            </body>
            </html>`;
    }

    private renderSessionCard(session: SessionInfo): string {
        const agentsHtml = session.agents?.map(a => {
            const statusClass = a.is_active ? 'status-active' : 'status-inactive';
            return `<div class="agent">
                <div style="display:flex; align-items:center;">
                    <div class="status-dot ${statusClass}"></div>
                    <span>${a.pane}</span>
                    <span class="tag">${a.type}</span>
                </div>
                <span class="muted">${a.variant ?? ''}</span>
            </div>`;
        }).join('') || 'No agents';

        const attachedTag = session.attached 
            ? `<span class="tag" style="background-color: var(--vscode-testing-iconPassed)">Attached</span>` 
            : `<span class="tag">Detached</span>`;

        return `
            <div class="card">
                <div class="card-header">
                    <span>${session.name}</span>
                    ${attachedTag}
                </div>
                <div class="muted" style="margin-bottom:8px;">${session.agents?.length ?? 0} agents · ${session.panes} panes</div>
                <div class="agent-list">
                    ${agentsHtml}
                </div>
            </div>
        `;
    }

    private renderMail(mail?: RobotMail): string {
        if (!mail) {
            return `<div class="muted">Mail status unavailable.</div>`;
        }
        if (!mail.available) {
            return `<div class="muted">Mail server offline.</div>`;
        }
        if (mail.error) {
            return `<div class="muted">Mail error: ${mail.error}</div>`;
        }
        const agents = mail.agents || [];
        if (!agents.length) {
            return `<div class="muted">No registered agents.</div>`;
        }
        return agents.map(a => this.renderMailAgent(a)).join('');
    }

    private renderMailAgent(agent: MailAgent): string {
        const urgent = agent.urgent_count || 0;
        const unread = agent.unread_count || 0;
        const dotClass = urgent > 0 ? 'status-inactive' : unread > 0 ? 'status-active' : '';
        return `
            <div class="agent">
                <div style="display:flex; align-items:center; gap:6px;">
                    <div class="status-dot ${dotClass}"></div>
                    <div>
                        <div>${agent.name}</div>
                        <div class="muted">${agent.program ?? ''} ${agent.model ? '· ' + agent.model : ''}</div>
                    </div>
                </div>
                <div class="muted">${unread} unread / ${urgent} urgent</div>
            </div>
        `;
    }

    private renderLocks(locks: MailLock[]): string {
        if (!locks.length) {
            return `<div class="muted">No active locks.</div>`;
        }
        const rows = locks.slice(0, 20).map(lock => `
            <tr>
                <td class="mono">${lock.path_pattern}</td>
                <td>${lock.agent_name}</td>
                <td class="muted">${lock.exclusive ? 'exclusive' : 'shared'}</td>
            </tr>
        `).join('');
        return `
            <table>
                <tr><th>Path</th><th>Agent</th><th>Mode</th></tr>
                ${rows}
            </table>
        `;
    }

    private pickPrimarySession(status: RobotStatus): SessionInfo | undefined {
        if (vscode.workspace.name) {
            const match = status.sessions.find(s => s.name === vscode.workspace.name);
            if (match) return match;
        }
        return status.sessions.find(s => s.attached) ?? status.sessions[0];
    }
}
