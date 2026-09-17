import * as vscode from 'vscode';
import { NtmClient } from './ntmClient';
import { NtmDashboard } from './dashboard';
import { registerFileDecorations } from './fileDecorations';
import { registerSessionTreeView } from './sessionTreeView';

export function activate(context: vscode.ExtensionContext) {
	const client = new NtmClient();
    const statusBarItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    statusBarItem.command = 'ntm.openPalette';
    context.subscriptions.push(statusBarItem);

    let currentSession: string | undefined;

    // Register file decoration provider for showing lock/reservation status
    const { disposable: fileDecorationDisposable } = registerFileDecorations(context, client);
    context.subscriptions.push(fileDecorationDisposable);

    // Register session tree view in activity bar
    registerSessionTreeView(context, client);

    // Initial check
    client.checkAvailable().then(available => {
        if (available) {
            updateStatus(client, statusBarItem).then(name => {
                currentSession = name ?? currentSession;
            });
            // Poll every 5s
            const interval = setInterval(() => {
                updateStatus(client, statusBarItem).then(name => {
                    currentSession = name ?? currentSession;
                });
            }, 5000);
            context.subscriptions.push({ dispose: () => clearInterval(interval) });
        } else {
            statusBarItem.text = '$(error) NTM missing';
            statusBarItem.color = new vscode.ThemeColor('statusBarItem.errorForeground');
            statusBarItem.show();
        }
    });

	let dispStatus = vscode.commands.registerCommand('ntm.showStatus', async () => {
		try {
            const status = await client.getStatus();
            const items = status.sessions.map(s => ({
                label: s.name,
                description: `${s.agents?.length || 0} agents`,
                detail: s.attached ? 'Attached' : 'Detached'
            }));
            
            vscode.window.showQuickPick(items, { placeHolder: 'Active NTM Sessions' });
        } catch (e) {
            vscode.window.showErrorMessage(`NTM Error: ${e}`);
        }
	});
    
    let dispOpenPalette = vscode.commands.registerCommand('ntm.openPalette', async () => {
        try {
            const status = await client.getStatus();
            const primary = pickPrimarySession(status);
            const session = primary?.name ?? currentSession;

            const chosen = session ?? await vscode.window.showInputBox({ prompt: 'NTM session name for palette' });
            if (!chosen) { return; }

            currentSession = chosen;

            const terminal = vscode.window.createTerminal({ name: `NTM Palette: ${chosen}` });
            terminal.show(true);
            terminal.sendText(`ntm palette ${chosen}`, true);
        } catch (e) {
            vscode.window.showErrorMessage(`NTM palette error: ${e}`);
        }
    });

    let dispSpawn = vscode.commands.registerCommand('ntm.spawn', async () => {
        const session = await vscode.window.showInputBox({ prompt: 'Session Name' });
        if (!session) return;
        try {
            await client.spawn(session, ['--cc=2']);
            vscode.window.showInformationMessage(`Spawned session ${session}`);
            updateStatus(client, statusBarItem);
        } catch (e) {
            vscode.window.showErrorMessage(`Spawn failed: ${e}`);
        }
    });

    let dispDashboard = vscode.commands.registerCommand('ntm.dashboard', () => {
        NtmDashboard.createOrShow(context.extensionUri, client);
    });

    const handleSend = async (content: string) => {
        try {
            let session = currentSession;
            if (!session) {
                const status = await client.getStatus();
                const primary = pickPrimarySession(status);
                session = primary?.name;
            }
            
            const chosenSession = session ?? await vscode.window.showInputBox({ prompt: 'NTM Session' });
            if (!chosenSession) return;
            currentSession = chosenSession;

            const target = await vscode.window.showQuickPick(
                ['Claude (--cc)', 'Codex (--cod)', 'Gemini (--gmi)', 'Oh My Pi (--omp)', 'All (--all)'],
                { placeHolder: 'Select Target Agents' }
            );
            if (!target) return;

            let targets: string[] = [];
            if (target.includes('--all')) targets = ['all'];
            else if (target.includes('--cc')) targets = ['cc'];
            else if (target.includes('--cod')) targets = ['cod'];
            else if (target.includes('--gmi')) targets = ['gmi'];
            else if (target.includes('--omp')) targets = ['omp'];

            const instruction = await vscode.window.showInputBox({ prompt: 'Additional Instructions (optional)' });
            const fullPrompt = content + (instruction ? `\nInstructions: ${instruction}` : '');

            await client.send(chosenSession, fullPrompt, targets);
            vscode.window.showInformationMessage(`Sent to ${chosenSession} (${targets.join(',')})`);
        } catch (e) {
            vscode.window.showErrorMessage(`Send failed: ${e}`);
        }
    };

    let dispSendSelection = vscode.commands.registerCommand('ntm.sendSelection', async () => {
        const editor = vscode.window.activeTextEditor;
        if (!editor) return;

        const selection = editor.selection;
        const text = editor.document.getText(selection);
        if (!text) {
            vscode.window.showWarningMessage('No text selected');
            return;
        }

        const filePath = vscode.workspace.asRelativePath(editor.document.uri);
        const prompt = `File: ${filePath}\n\`\`\`\n${text}\n\`\`\`\n`;
        await handleSend(prompt);
    });

    let dispSendFile = vscode.commands.registerCommand('ntm.sendCurrentFile', async () => {
        const editor = vscode.window.activeTextEditor;
        if (!editor) return;

        const text = editor.document.getText();
        const filePath = vscode.workspace.asRelativePath(editor.document.uri);
        const prompt = `File: ${filePath}\n\`\`\`\n${text}\n\`\`\`\n`;
        await handleSend(prompt);
    });

    let dispOpenTerminal = vscode.commands.registerCommand('ntm.openTerminal', async () => {
        try {
            let session = currentSession;
            if (!session) {
                const status = await client.getStatus();
                const primary = pickPrimarySession(status);
                session = primary?.name;
            }

            const chosenSession = session ?? await vscode.window.showInputBox({ prompt: 'NTM Session to attach' });
            if (!chosenSession) return;
            currentSession = chosenSession;

            const terminal = vscode.window.createTerminal({ name: `NTM: ${chosenSession}` });
            terminal.show(true);
            terminal.sendText(`ntm attach ${chosenSession}`, true);
        } catch (e) {
            vscode.window.showErrorMessage(`Failed to open terminal: ${e}`);
        }
    });

	context.subscriptions.push(dispStatus, dispSpawn, dispOpenPalette, dispDashboard, dispSendSelection, dispSendFile, dispOpenTerminal);
}

function pickPrimarySession(status: ReturnType<NtmClient['getStatus']> extends Promise<infer T> ? T : never) {
    const sessions = status.sessions || [];
    
    // Prefer session matching workspace name
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
            const primaryAgents = primary.agents?.length ?? primary.panes ?? agentCount;
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
            item.text = `$(terminal) NTM`;
            item.tooltip = "No active sessions";
            item.color = new vscode.ThemeColor('statusBarItem.warningForeground');
            item.show();
        }
    } catch {
        item.text = `$(warning) NTM`;
        item.tooltip = "Error connecting to NTM";
        item.color = new vscode.ThemeColor('statusBarItem.errorForeground');
        item.show();
    }
    return undefined;
}

export function deactivate() {}
