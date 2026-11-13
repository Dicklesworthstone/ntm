import * as path from 'path';
import * as vscode from 'vscode';
import {
    AgentInfo,
    AttentionItem,
    AttentionMutationAction,
    buildNtmTerminalCommand,
    buildWebCockpitCommand,
    DEFAULT_WEB_PORT,
    EnsemblePreset,
    EnsembleSuggestResult,
    normalizeWebPort,
    NtmClient,
    RobotStatus,
    SendHarness,
    SessionInfo,
    shellQuote,
    SpawnHarness,
} from './ntmClient';
import { NtmDashboard } from './dashboard';
import { normalizeWorkspaceRelativePath, registerFileDecorations } from './fileDecorations';
import { NtmTreeItem, registerSessionTreeView } from './sessionTreeView';

export { shellQuote, buildNtmTerminalCommand, buildWebCockpitCommand, normalizeWebPort };

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
        | 'triageAttention'
        | 'openWebCockpit'
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
        label: '$(bell) Triage Attention Feed',
        description: 'NTM: Triage Attention Feed',
        detail: 'Inspect pending attention items and acknowledge, pin, snooze, or dismiss',
        actionId: 'triageAttention',
        command: 'ntm.triageAttention',
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
                if (picked.actionId === 'sendPrompt' && sessionArg) {
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
    const dispOpenWebCockpit = vscode.commands.registerCommand('ntm.openWebCockpit', async () => {
        try {
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

            await vscode.env.openExternal(vscode.Uri.parse('http://127.0.0.1:' + port));
        } catch (e) {
            vscode.window.showErrorMessage(`Failed to open NTM Web Cockpit: ${e}`);
        }
    });

    context.subscriptions.push(
        dispStatus,
        dispSpawn,
        dispSpawnEnsemble,
        dispTriageAttention,
        dispOpenWebCockpit,
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
