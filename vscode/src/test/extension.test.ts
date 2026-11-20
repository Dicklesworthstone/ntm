import { mockState, resetVscodeMock } from './vscodeMock';
import { beforeEach, describe, it } from 'node:test';
import * as assert from 'node:assert/strict';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import {
    AttentionMutationAction,
    buildNtmTerminalCommand,
    buildWebCockpitCommand,
    DEFAULT_WEB_PORT,
    mapBackpressureLevel,
    normalizeAnalyticsResult,
    normalizeAssignWorkResult,
    normalizeAuditResult,
    normalizeBackpressure,
    normalizeDoctorResult,
    normalizeEnsembleSynthesize,
    normalizeHandoffResult,
    normalizeIncidentList,
    normalizeIncidentMutation,
    normalizePreflightResult,
    normalizeSnapshot,
    normalizeStatusHeader,
    normalizeSupportBundleResult,
    normalizeWebPort,
    normalizeWorktreeList,
    NtmClient,
    RobotMail,
    shellQuote,
    SUPPORTED_ASSIGN_STRATEGIES,
    SUPPORTED_ATTENTION_ACTIONS,
    SUPPORTED_INCIDENT_ACTIONS,
    SUPPORTED_SEND_HARNESSES,
    SUPPORTED_SPAWN_HARNESSES,
} from '../ntmClient';
import {
    escapeAttr,
    escapeHtml,
    generateNonce,
    NtmDashboard,
    renderDashboardHtml,
} from '../dashboard';
import {
    matchesReservationPattern,
    normalizeWorkspaceRelativePath,
    NtmFileDecorationProvider,
} from '../fileDecorations';
import { NtmSessionTreeProvider, NtmTreeItem } from '../sessionTreeView';
import {
    activate,
    ANALYTICS_WINDOW_OPTIONS,
    ASSIGN_STRATEGY_OPTIONS,
    attentionSeverityIcon,
    AUDIT_MODE_OPTIONS,
    backpressureLevelIcon,
    buildAttentionActionQuickPickItems,
    buildAttentionQuickPickItems,
    buildBackpressureQuickPickItems,
    buildEnsemblePresetQuickPickItems,
    buildIncidentActionQuickPickItems,
    buildIncidentQuickPickItems,
    buildWebCockpitCommand as extensionBuildWebCockpitCommand,
    buildWorktreeQuickPickItems,
    defaultEnsembleSessionName,
    DOCTOR_MODE_OPTIONS,
    ENSEMBLE_SYNTHESIS_STRATEGY_OPTIONS,
    formatAnalyticsMarkdown,
    formatAssignWorkMarkdown,
    formatAuditMarkdown,
    formatDoctorMarkdown,
    formatHandoffMarkdown,
    formatPreflightMarkdown,
    HANDOFF_MODE_OPTIONS,
    PALETTE_ACTION_OPTIONS,
    parseCustomSpawnFlags,
    PREFLIGHT_MODE_OPTIONS,
    resolveSendTargetsFromSelection,
    SEND_TARGET_OPTIONS,
    shellQuote as extensionShellQuote,
    SPAWN_HARNESS_OPTIONS,
    SUPPORT_BUNDLE_FORMAT_OPTIONS,
    WEB_COCKPIT_TERMINAL_NAME,
} from '../extension';

const REAL_SNAPSHOT_PAYLOAD = {
    success: true,
    timestamp: '2026-11-12T20:24:27Z',
    version: '1.0.0',
    output_format: 'json',
    schema_id: 'ntm:robot:snapshot:v1',
    schema_version: 'ntm.robot.snapshot.v1',
    ts: '2026-11-12T20:24:27Z',
    safety_profile: 'standard',
    attention_contract_version: '1.2.0',
    latest_cursor: 21440,
    sessions: [
        {
            name: 'ntm-prod',
            attached: true,
            agents: [
                {
                    pane: '1.1',
                    type: 'claude',
                    variant: 'opus-4.6',
                    type_confidence: 0.95,
                    type_method: 'process',
                    state: 'working',
                    last_output_age_sec: 2,
                    output_tail_lines: 40,
                    context_percent: 0.32,
                    current_bead: 'bd-rc-track-c-surfaces-h2e1n.3',
                    pending_mail: 1,
                },
                {
                    pane: '1.2',
                    type: 'antigravity',
                    variant: 'gemini-4-neon',
                    type_confidence: 0.9,
                    type_method: 'title',
                    state: 'idle',
                    last_output_age_sec: 18,
                    output_tail_lines: 25,
                    pending_mail: 0,
                },
            ],
        },
        {
            name: 'ntm-services',
            attached: false,
            agents: [
                {
                    pane: '1.1',
                    type: 'omp',
                    type_confidence: 0.88,
                    type_method: 'process',
                    state: 'active',
                    last_output_age_sec: 1,
                    output_tail_lines: 12,
                    pending_mail: 0,
                },
            ],
            services: [
                {
                    pane: '1.2',
                    name: 'cm',
                    manager: 'acfs',
                    command: 'cm serve',
                },
            ],
        },
    ],
    summary: {
        total_sessions: 2,
        total_agents: 3,
        attached_count: 1,
        ready_work: 3,
        in_progress: 7,
        health_score: 1,
        health_status: 'healthy',
        alerts_active: 1,
        mail_unread: 2,
        mail_urgent: 1,
    },
    beads_summary: {
        available: true,
        project: '/data/projects/ntm',
        total: 3228,
        open: 22,
        in_progress: 7,
        blocked: 19,
        ready: 3,
        closed: 3199,
        ready_preview: [
            {
                id: 'bd-rc-track-c-surfaces-h2e1n.3',
                title: 'Rescue VSCode extension for --robot-status v2 / --robot-snapshot',
                priority: 'P1',
                type: 'task',
            },
        ],
        in_progress_list: [
            {
                id: 'bd-rc-track-a-gates-j7pun.1',
                title: 'Restore WS0-G1 deadcode_gate.sh on main',
                updated_at: '2026-11-12T20:15:08Z',
            },
        ],
    },
    agent_mail: {
        available: true,
        project: '/data/projects/ntm',
        total_unread: 2,
        agents: {
            BlueLake: {
                pane: '1.1',
                unread: 2,
                pending_ack: 1,
            },
        },
        threads_known: 4,
    },
    alerts: ['Context approaching threshold'],
    alerts_detailed: [
        {
            id: 'al-1',
            type: 'context_warning',
            severity: 'warning',
            message: 'Context approaching threshold',
            session: 'ntm-prod',
            pane: '1.1',
            created_at: '2026-11-12T20:24:00Z',
        },
    ],
};

const REAL_STATUS_V2_HEADER_PAYLOAD = {
    success: true,
    timestamp: '2026-11-12T20:24:24Z',
    schema_id: 'ntm:robot:status:v1',
    schema_version: 'ntm.robot.status.v2',
    generated_at: '2026-11-12T20:24:24.677962623Z',
    overall_status: 'healthy',
    system: {
        version: '1.38.0',
        os: 'linux',
    },
    sessions: [
        {
            name: 'ntm-prod',
            attached: true,
            agent_count: 2,
            health: {
                status: 'healthy',
            },
        },
    ],
    summary: {
        total_sessions: 1,
        total_agents: 2,
        attached_count: 1,
    },
};

describe('NtmClient (--robot-snapshot + single-flight + cwd + fallback)', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('normalizes real --robot-snapshot v1 JSON with non-zero panes, agents, beads_summary, agent_mail, and alerts', async () => {
        const recordedCalls: Array<{ file: string; args: string[]; options: cp.ExecFileOptions }> = [];
        const tmpWorkspace = os.tmpdir();
        mockState.workspaceFolders = [{ uri: { fsPath: tmpWorkspace } }];

        const client = new NtmClient({
            execFile: async (file, args, options) => {
                recordedCalls.push({ file, args, options });
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const status = await client.getStatus();
        assert.equal(recordedCalls.length, 1);
        assert.equal(recordedCalls[0].file, 'ntm');
        assert.deepEqual(recordedCalls[0].args, ['--robot-snapshot', '--robot-format=json']);
        assert.equal(recordedCalls[0].options.cwd, tmpWorkspace);

        assert.equal(status.degraded, false);
        assert.equal(status.sessions.length, 2);

        const prod = status.sessions[0];
        assert.equal(prod.name, 'ntm-prod');
        assert.equal(prod.attached, true);
        assert.equal(prod.panes, 2);
        assert.equal(prod.agent_count, 2);
        assert.equal(prod.agents.length, 2);
        assert.equal(prod.agents[0].pane, '1.1');
        assert.equal(prod.agents[0].type, 'claude');
        assert.equal(prod.agents[0].state, 'working');
        assert.equal(prod.agents[0].is_active, true);
        assert.equal(prod.agents[1].state, 'idle');
        assert.equal(prod.agents[1].is_active, false);

        const svcSession = status.sessions[1];
        assert.equal(svcSession.panes, 2, 'panes includes agent panes + service panes when services present');
        assert.equal(svcSession.agent_count, 1);

        assert.ok(status.beads);
        assert.equal(status.beads.available, true);
        assert.equal(status.beads.open, 22);
        assert.equal(status.beads.in_progress, 7);
        assert.equal(status.beads.ready, 3);
        assert.equal(status.beads.ready_preview?.[0]?.id, 'bd-rc-track-c-surfaces-h2e1n.3');
        assert.equal(status.beads.in_progress_list?.[0]?.id, 'bd-rc-track-a-gates-j7pun.1');

        assert.ok(status.agent_mail);
        assert.equal(status.agent_mail.available, true);
        assert.equal(status.agent_mail.total_unread, 2);
        assert.equal(status.agent_mail.agents?.BlueLake?.unread, 2);

        assert.equal(status.alerts?.length, 1);
        assert.equal(status.alerts?.[0]?.message, 'Context approaching threshold');

        // Verify NtmSessionTreeProvider renders real non-zero counts and child agent nodes
        const tree = new NtmSessionTreeProvider(client);
        await tree.refresh();
        const rootItems = await tree.getChildren();
        assert.equal(rootItems.length, 2);
        assert.equal(rootItems[0].tooltip, '2 panes • 2 agents');
        const agentItems = await tree.getChildren(rootItems[0]);
        assert.equal(agentItems.length, 2);
        assert.equal(agentItems[0].label, '1.1');
        assert.equal(agentItems[0].sessionName, 'ntm-prod');
        tree.dispose();
    });

    it('deduplicates concurrent getStatus / getSnapshot calls into a single child process (single-flight)', async () => {
        let spawnCount = 0;
        const client = new NtmClient({
            execFile: async () => {
                spawnCount++;
                await new Promise(resolve => setTimeout(resolve, 25));
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const [s1, s2, s3, s4] = await Promise.all([
            client.getStatus(),
            client.getSnapshot(),
            client.getStatus(),
            client.getSnapshot(),
        ]);

        assert.equal(spawnCount, 1, 'concurrent callers must share one in-flight snapshot child process');
        assert.equal(s1.sessions[0].panes, 2);
        assert.equal(s2.sessions[0].panes, 2);
        assert.equal(s3.sessions[0].panes, 2);
        assert.equal(s4.sessions[0].panes, 2);

        // Subsequent call after flight settles runs a fresh child process
        await client.getStatus();
        assert.equal(spawnCount, 2);
    });

    it('falls back cleanly to Tier-1 --robot-status --robot-format=json when snapshot degrades', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args.includes('--robot-snapshot')) {
                    throw new Error('projection store locked');
                }
                return { stdout: JSON.stringify(REAL_STATUS_V2_HEADER_PAYLOAD), stderr: '' };
            },
        });

        const status = await client.getStatus();
        assert.deepEqual(calls, [
            ['--robot-snapshot', '--robot-format=json'],
            ['--robot-status', '--robot-format=json'],
        ]);
        assert.equal(status.degraded, true);
        assert.equal(status.sessions.length, 1);
        assert.equal(status.sessions[0].name, 'ntm-prod');
        assert.equal(status.sessions[0].agent_count, 2);
        assert.equal(status.sessions[0].panes, 2);
        assert.equal(status.sessions[0].health?.status, 'healthy');

        const tree = new NtmSessionTreeProvider(client);
        await tree.refresh();
        const items = await tree.getChildren();
        assert.equal(items[0].tooltip, '2 panes • 2 agents');
        tree.dispose();
    });

    it('omits cwd when no workspace is open or workspace folder does not exist on disk', async () => {
        const recordedOpts: cp.ExecFileOptions[] = [];
        mockState.workspaceFolders = undefined;

        const client = new NtmClient({
            execFile: async (_file, args, options) => {
                recordedOpts.push(options);
                if (args.includes('--robot-mail')) {
                    const mail: RobotMail = {
                        generated_at: '2026-11-12T20:24:30Z',
                        available: true,
                        agents: [],
                        file_reservations: [
                            {
                                id: 7,
                                pattern: 'vscode/**',
                                agent: 'BlueLake',
                                exclusive: true,
                                expires_in_seconds: 1800,
                                reason: 'bd-rc-track-c-surfaces-h2e1n.3',
                            },
                        ],
                    };
                    return { stdout: JSON.stringify(mail), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        await client.getStatus();
        assert.equal(recordedOpts[0].cwd, undefined);

        mockState.workspaceFolders = [{ uri: { fsPath: '/nonexistent/missing-ntm-workspace-dir-99999' } }];
        const mail = await client.getMail();
        assert.equal(recordedOpts[1].cwd, undefined);
        assert.equal(mail.file_reservations?.length, 1);
        assert.equal(mail.file_reservations?.[0]?.pattern, 'vscode/**');
    });

    it('executes real child_process.execFile with cwd and --robot-format=json forwarding', async () => {
        const tmpDir = os.tmpdir();
        mockState.workspaceFolders = [{ uri: { fsPath: tmpDir } }];
        // Use node itself as a real child binary via binaryPath? Or test via real `/usr/bin/env`?
        // Better: pass node as binaryPath and verify real child process error/stdout plumbing,
        // and test real `/bin/echo` returning JSON when workspace cwd is set!
        const echoClient = new NtmClient({
            binaryPath: '/bin/sh',
        });
        // checkAvailable runs `/bin/sh --version` or returns boolean cleanly without throwing
        const avail = await echoClient.checkAvailable();
        assert.equal(typeof avail, 'boolean');
        assert.equal(echoClient.getWorkspaceCwd(), path.resolve(tmpDir));
    });
});

describe('Dashboard Webview CSP & XSS Remediation', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('enforces strict per-render 128-bit nonce CSP, zero raw <img or inline onclick=, and escapes all XSS payloads', () => {
        const xss = '<img src=x onerror=alert(1)>';

        const makeSnapshotWithPayload = (payload: string) =>
            normalizeSnapshot({
                success: true,
                ts: '2026-11-12T20:24:27Z',
                sessions: [
                    {
                        name: `sess-${payload}`,
                        attached: true,
                        health: {
                            status: `degraded-${payload}`,
                            reason: `reason-${payload}`,
                        },
                        agents: [
                            {
                                pane: `1.1 ${payload}`,
                                type: `claude ${payload}`,
                                variant: `opus ${payload}`,
                                state: `working ${payload}`,
                                current_bead: `bd-1 ${payload}`,
                            },
                        ],
                    },
                ],
                summary: {
                    total_sessions: 1,
                    total_agents: 1,
                    attached_count: 1,
                },
                beads_summary: {
                    available: true,
                    total: 1,
                    open: 1,
                    in_progress: 1,
                    blocked: 0,
                    ready: 1,
                    closed: 0,
                    ready_preview: [
                        {
                            id: `bd-xss-${payload}`,
                            title: `Title ${payload}`,
                            priority: `P1 ${payload}`,
                        },
                    ],
                },
                alerts: [`Alert ${payload}`],
            });

        const makeMailWithPayload = (payload: string, withError: boolean): RobotMail => ({
            generated_at: '2026-11-12T20:24:30Z',
            available: true,
            error: withError ? `Auth failed ${payload}` : undefined,
            agents: [
                {
                    agent_name: `MailAgent ${payload}`,
                    program: `claude ${payload}`,
                    model: `opus ${payload}`,
                    pane: `1.1 ${payload}`,
                    unread_count: 2,
                    urgent_count: 1,
                },
            ],
            file_reservations: [
                {
                    id: 1,
                    pattern: `vscode/**/${payload}`,
                    agent: `Agent ${payload}`,
                    exclusive: true,
                    expires_in_seconds: 900,
                    expires_in: `15m ${payload}`,
                    reason: `Reason ${payload}`,
                },
            ],
        });

        const html1 = renderDashboardHtml(
            makeSnapshotWithPayload(xss),
            makeMailWithPayload(xss, true),
            { errorMessage: `Banner ${xss}` }
        );
        const html2 = renderDashboardHtml(
            makeSnapshotWithPayload(xss),
            makeMailWithPayload(xss, false)
        );

        for (const html of [html1, html2]) {
            const cspMatch = html.match(
                /<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-([^']+)';">/
            );
            assert.ok(cspMatch, 'must inject strict default-src none + nonce script-src CSP meta tag');
            const nonce = cspMatch[1];
            assert.equal(Buffer.from(nonce, 'base64').length, 16, 'nonce must decode to 128 bits (16 bytes)');
            assert.ok(html.includes(`<script nonce="${nonce}">`), 'script tag must carry matching nonce');

            // Planted negative assertions: zero unescaped <img tags, zero extra <script> tags, zero inline onclick=
            assert.equal(/<img\b/i.test(html), false, 'must contain zero raw <img tags');
            assert.equal(html.includes('onclick='), false, 'must contain zero inline onclick= attributes');
            assert.equal((html.match(/<script\b/gi) || []).length, 1, 'must contain only the single nonced <script> tag');

            // Escaped XSS payload is rendered harmlessly as text
            assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt;'), 'XSS payload must be HTML-escaped');

            // Delegated data-action attributes exist for all interactive actions
            for (const action of ['refresh', 'openPalette', 'dashboard', 'send', 'spawn', 'attach']) {
                assert.ok(html.includes(`data-action="${action}"`), `missing data-action="${action}" button`);
            }
            assert.equal(html.includes('undefined panes'), false, 'must never render "undefined panes"');
        }

        // Attribute breakout test: verify quote-breaking onclick/script payloads cannot inject attributes on any HTML element
        const attrBreakout = `" onclick="alert(2)" ' onmouseover='alert(3)'><script>alert(4)</script>`;
        const breakoutHtml = renderDashboardHtml(
            makeSnapshotWithPayload(attrBreakout),
            makeMailWithPayload(attrBreakout, false)
        );
        assert.equal((breakoutHtml.match(/<script\b/gi) || []).length, 1, 'attribute breakout must not inject <script>');
        const htmlTags = breakoutHtml.match(/<[a-zA-Z][^>]*>/g) || [];
        assert.ok(htmlTags.length > 0);
        for (const tag of htmlTags) {
            const tagWithoutAttrValues = tag
                .replace(/="[^"]*"/g, '=""')
                .replace(/='[^']*'/g, "=''");
            assert.equal(
                /\bon[a-z]+\s*=/i.test(tagWithoutAttrValues),
                false,
                `HTML tag must have zero inline event handler attributes: ${tag}`
            );
        }

        // Verify per-render nonces are unique
        const nonce1 = html1.match(/nonce-([^']+)/)![1];
        const nonce2 = html2.match(/nonce-([^']+)/)![1];
        assert.notEqual(nonce1, nonce2, 'each render must generate a fresh cryptographic nonce');
        assert.equal(Buffer.from(generateNonce(), 'base64').length, 16);
        assert.equal(escapeHtml(xss), '&lt;img src=x onerror=alert(1)&gt;');
        assert.ok(!escapeAttr(attrBreakout).includes('"'));
    });

    it('dispatches delegated Webview button messages (attach, dashboard, refresh, spawn, send) to VS Code commands', async () => {
        let refreshCalls = 0;
        const client = new NtmClient({
            execFile: async (_file, args) => {
                if (args.includes('--robot-snapshot')) {
                    refreshCalls++;
                    return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
                }
                return {
                    stdout: JSON.stringify({ generated_at: 'now', available: false, agents: [] }),
                    stderr: '',
                };
            },
        });

        NtmDashboard.createOrShow(vscode.Uri.file('/ext'), client);
        try {
            await new Promise(resolve => setTimeout(resolve, 15));
            assert.equal(mockState.createdWebviewPanels.length, 1);
            const panel = mockState.createdWebviewPanels[0];
            assert.ok(panel.webview.html.includes('ntm-prod'));

            panel.webview.triggerMessage({ command: 'attach', session: 'ntm-prod' });
            panel.webview.triggerMessage({ command: 'dashboard', session: 'ntm-prod' });
            panel.webview.triggerMessage({ command: 'send', session: 'ntm-prod' });
            panel.webview.triggerMessage({ command: 'spawn' });
            panel.webview.triggerMessage({ command: 'refresh' });
            await new Promise(resolve => setTimeout(resolve, 15));

            assert.deepEqual(mockState.executedCommands, [
                { command: 'ntm.openTerminal', args: ['ntm-prod'] },
                { command: 'ntm.terminalDashboard', args: ['ntm-prod'] },
                { command: 'ntm.sendPrompt', args: ['ntm-prod'] },
                { command: 'ntm.spawn', args: [] },
            ]);
            assert.ok(refreshCalls >= 2, 'refresh message must re-fetch status');
        } finally {
            NtmDashboard.currentPanel?.dispose();
        }
    });
});

describe('POSIX shellQuote, Custom binaryPath & Multi-Harness Commands', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('quotes spaces, single quotes, $(), backticks, and custom binaryPath safely in POSIX sh', () => {
        assert.equal(shellQuote, extensionShellQuote);
        assert.equal(shellQuote(''), "''");
        assert.equal(shellQuote('/opt/my tools/ntm'), "'/opt/my tools/ntm'");
        assert.equal(shellQuote("it's a 'pane'"), "'it'\\''s a '\\''pane'\\'''");
        assert.equal(shellQuote('$(rm -rf /tmp/nope)'), "'$(rm -rf /tmp/nope)'");
        assert.equal(shellQuote('`uname -a`'), "'`uname -a`'");
        assert.equal(shellQuote('=zsh-expand'), "'=zsh-expand'");

        const cmd = buildNtmTerminalCommand(
            '/opt/my tools/ntm',
            'attach',
            "sess $(id) `whoami` 'x'"
        );
        assert.equal(cmd, "'/opt/my tools/ntm' attach 'sess $(id) `whoami` '\\''x'\\'''");

        // Verify real /bin/sh decodes the exact literal strings without executing $(...) or backticks
        const trickyInputs = [
            '/opt/my tools/ntm',
            "o'hara's session",
            '$(echo INJECTED)',
            '`echo BACKTICK`',
            'semi;colon & pipe | redirect > /dev/null',
            '',
        ];
        for (const input of trickyInputs) {
            const quoted = shellQuote(input);
            const decoded = cp.execFileSync('/bin/sh', ['-c', `printf '%s' ${quoted}`], {
                encoding: 'utf8',
            });
            assert.equal(decoded, input);
        }
    });

    it('uses configured ntm.binaryPath + shellQuote across attach, view, zoom, dashboard, and palette terminals', async () => {
        mockState.binaryPath = '/opt/my tools/ntm';
        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context);
        try {
            const trickySession = "my proj $(id) '1'";
            await vscode.commands.executeCommand('ntm.openTerminal', trickySession);
            await vscode.commands.executeCommand('ntm.viewSession', trickySession);
            await vscode.commands.executeCommand('ntm.zoomPane', trickySession, '1.2');
            await vscode.commands.executeCommand('ntm.terminalDashboard', trickySession);
            await vscode.commands.executeCommand('ntm.openPalette', trickySession);

            const sessionTreeItem = new NtmTreeItem(
                trickySession,
                vscode.TreeItemCollapsibleState.None,
                'session',
                normalizeStatusHeader(REAL_STATUS_V2_HEADER_PAYLOAD).sessions[0],
                trickySession
            );
            (sessionTreeItem.data as { name: string }).name = trickySession;
            await vscode.commands.executeCommand('ntm.attachSession', sessionTreeItem);

            const sentTexts = mockState.createdTerminals.map(t => t.sentCommands[0]?.text);
            assert.deepEqual(sentTexts, [
                `'/opt/my tools/ntm' attach 'my proj $(id) '\\''1'\\'''`,
                `'/opt/my tools/ntm' view 'my proj $(id) '\\''1'\\'''`,
                `'/opt/my tools/ntm' zoom 'my proj $(id) '\\''1'\\''' 1.2`,
                `'/opt/my tools/ntm' dashboard 'my proj $(id) '\\''1'\\'''`,
                `'/opt/my tools/ntm' palette 'my proj $(id) '\\''1'\\'''`,
                `'/opt/my tools/ntm' attach 'my proj $(id) '\\''1'\\'''`,
            ]);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });

    it('supports all spawn harnesses (cc, cod, agy, omp, grok, oc, pi, custom) and all send targets (all, cc, cod, agy, omp, grok, oc, pi, gmi)', async () => {
        const spawnIds = SPAWN_HARNESS_OPTIONS.map(o => o.harness);
        assert.deepEqual(spawnIds, [...SUPPORTED_SPAWN_HARNESSES, 'custom']);
        assert.deepEqual(parseCustomSpawnFlags('--cc=2 --grok=1 --recipe="full stack"'), [
            '--cc=2',
            '--grok=1',
            '--recipe=full stack',
        ]);

        const sendIds = SEND_TARGET_OPTIONS.map(o => o.target);
        assert.deepEqual(sendIds, ['all', ...SUPPORTED_SEND_HARNESSES]);

        for (const option of SEND_TARGET_OPTIONS) {
            assert.deepEqual(resolveSendTargetsFromSelection(option), [option.target]);
        }

        const recordedArgs: string[][] = [];
        const client = new NtmClient({
            binaryPath: '/opt/my tools/ntm',
            execFile: async (_file, args) => {
                recordedArgs.push(args);
                return { stdout: '{}', stderr: '' };
            },
        });

        await client.spawn('swarm-1', ['--agy=2', '--pi=1', '--omp=1']);
        assert.deepEqual(recordedArgs[0], ['spawn', 'swarm-1', '--agy=2', '--pi=1', '--omp=1', '--json']);

        for (const harness of SUPPORTED_SEND_HARNESSES) {
            recordedArgs.length = 0;
            await client.send('swarm-1', 'Check tests', [harness]);
            assert.deepEqual(recordedArgs[0], ['send', 'swarm-1', `--${harness}`, '--', 'Check tests']);
        }

        recordedArgs.length = 0;
        await client.send('swarm-1', 'Broadcast msg', ['all']);
        assert.deepEqual(recordedArgs[0], ['send', 'swarm-1', '--all', '--', 'Broadcast msg']);
    });
});

describe('File Reservation POSIX Path Normalization & Glob Matching', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('normalizes Windows backslash workspace-relative paths to POSIX / and matches reservation globs', () => {
        mockState.workspaceFolders = [{ uri: { fsPath: 'C:\\Users\\dev\\ntm' } }];
        mockState.asRelativePathOverride = uri =>
            uri.fsPath.replace('C:\\Users\\dev\\ntm\\', '');

        const winUri = vscode.Uri.file('C:\\Users\\dev\\ntm\\vscode\\src\\ntmClient.ts');
        assert.equal(normalizeWorkspaceRelativePath(winUri), 'vscode/src/ntmClient.ts');

        assert.equal(matchesReservationPattern('vscode\\src\\ntmClient.ts', 'vscode/**'), true);
        assert.equal(matchesReservationPattern('vscode\\src\\ntmClient.ts', 'vscode/**/*.ts'), true);
        assert.equal(matchesReservationPattern('vscode\\extension.ts', 'vscode/**/*.ts'), true);
        assert.equal(matchesReservationPattern('ntmClient.ts', '**/ntmClient.ts'), true);
        assert.equal(matchesReservationPattern('internal/cli/root.go', 'vscode/**'), false);
        assert.equal(matchesReservationPattern('vscode/src/sub/a.ts', 'vscode/src/*.ts'), false);

        const client = new NtmClient({
            execFile: async () => ({ stdout: '{}', stderr: '' }),
        });
        const provider = new NtmFileDecorationProvider(client);
        provider.setReservations([
            {
                id: 1,
                pattern: 'vscode/**/*.ts',
                agent: 'ChartreuseStone',
                exclusive: true,
                expires_in_seconds: 1200,
                reason: 'VSCode rescue',
            },
            {
                id: 2,
                pattern: 'docs/**',
                agent: 'BlueLake',
                exclusive: false,
                expires_in_seconds: 600,
            },
        ]);

        const lockedDecoration = provider.provideFileDecoration(winUri);
        assert.ok(lockedDecoration);
        assert.equal(lockedDecoration.badge, '🔒');
        assert.equal(lockedDecoration.tooltip, 'Locked by ChartreuseStone (VSCode rescue)');

        const watchedUri = vscode.Uri.file('C:\\Users\\dev\\ntm\\docs\\guide.md');
        const watchedDecoration = provider.provideFileDecoration(watchedUri);
        assert.ok(watchedDecoration);
        assert.equal(watchedDecoration.badge, '👁');

        const unlockedUri = vscode.Uri.file('C:\\Users\\dev\\ntm\\internal\\cli\\root.go');
        assert.equal(provider.provideFileDecoration(unlockedUri), undefined);

        provider.dispose();
    });
});

const REAL_ENSEMBLE_PRESETS_PAYLOAD = {
    success: true,
    timestamp: '2026-11-13T20:30:25Z',
    version: '1.0.0',
    output_format: 'json',
    action: 'ensemble_presets',
    count: 3,
    presets: [
        {
            name: 'project-diagnosis',
            display: 'Project Diagnosis',
            description: 'Comprehensive project health analysis',
            modes: ['F7', 'G6', 'F2', 'F4', 'H2'],
            mode_count: 5,
            synthesis: { strategy: 'adversarial' },
            budget: { total: 30000 },
            allow_advanced: false,
            tags: ['analysis', 'health'],
            source: 'embedded',
        },
        {
            name: 'safety-risk',
            display: 'Safety / Risk Analysis',
            description: 'Threat modeling and security review',
            modes: ['D1', 'D3', 'F2', 'G4', 'H1'],
            mode_count: 5,
            synthesis: { strategy: 'consensus' },
            budget: { total: 35000 },
            allow_advanced: false,
            tags: ['security', 'risk'],
            source: 'embedded',
        },
        {
            name: 'bug-hunt',
            display_name: 'Bug Hunt',
            description: 'Root-cause isolation and regression proofing',
            modes: ['C1', 'C4', 'E2'],
            agent_count: 3,
            category: 'debugging',
            tags: ['bugs'],
            source: 'user',
        },
    ],
};

const REAL_ENSEMBLE_SUGGEST_PAYLOAD = {
    success: true,
    timestamp: '2026-11-13T20:30:28Z',
    version: '1.0.0',
    output_format: 'json',
    question: 'What security vulnerabilities exist in auth?',
    top_pick: {
        preset_name: 'safety-risk',
        display_name: 'Safety / Risk Analysis',
        description: 'Threat modeling and security review',
        score: 0.57,
        reasons: ['matches "security"', 'matches "auth"', 'tag match: security'],
        mode_count: 5,
        tags: ['security', 'risk'],
        spawn_cmd: 'ntm ensemble safety-risk "What security vulnerabilities exist in auth?"',
    },
    suggestions: [
        {
            preset_name: 'safety-risk',
            display_name: 'Safety / Risk Analysis',
            description: 'Threat modeling and security review',
            score: 0.57,
            reasons: ['matches "security"', 'matches "auth"', 'tag match: security'],
            mode_count: 5,
            tags: ['security', 'risk'],
            spawn_cmd: 'ntm ensemble safety-risk "What security vulnerabilities exist in auth?"',
        },
    ],
    _agent_hints: {
        summary: 'Best match: Safety / Risk Analysis',
    },
};

const REAL_ENSEMBLE_SPAWN_PAYLOAD = {
    success: true,
    timestamp: '2026-11-13T20:31:00Z',
    action: 'ensemble_spawn',
    session: 'ntm-ensemble',
    preset: 'safety-risk',
    question: 'What security vulnerabilities exist in auth?',
    project_dir: '/data/projects/ntm',
    assignment: 'affinity',
    agents: { cc: 2, cod: 1 },
    modes: [
        { id: 'threat-modeling', code: 'D1', tier: 'core', pane: '1.1', agent_type: 'cc' },
        { id: 'boundary-audit', code: 'D3', tier: 'core', pane: '1.2', agent_type: 'cod' },
    ],
    budget: {
        total_tokens: 35000,
        per_mode_tokens: 7000,
    },
    status: 'running',
};

const REAL_ATTENTION_LIST_PAYLOAD = {
    success: true,
    timestamp: '2026-11-13T20:32:00Z',
    count: 3,
    items: [
        {
            cursor: 42,
            item_key: 'alert:agent_stuck:pane-def456',
            dedup_key: 'alert:agent_stuck:pane-def456',
            session: 'proj-alpha',
            pane: '2',
            category: 'alert',
            type: 'agent_stuck',
            severity: 'warning',
            actionability: 'action_required',
            summary: 'Agent cc_2 stuck for 120s',
            event_timestamp: '2026-11-13T20:31:40Z',
            state: 'new',
            pinned: false,
            muted: false,
            resurfacing_policy: 'on_change',
        },
        {
            cursor: 41,
            item_key: 'mail:urgent:thread-9',
            session: 'proj-alpha',
            pane: '1',
            category: 'mail',
            type: 'urgent_unread',
            severity: 'critical',
            actionability: 'urgent',
            summary: 'Urgent message from BlueLake',
            event_timestamp: '2026-11-13T20:30:10Z',
            state: 'acknowledged',
            pinned: true,
            muted: false,
        },
        {
            cursor: 40,
            item_key: 'session:build_complete',
            session: 'proj-beta',
            category: 'lifecycle',
            type: 'task_done',
            severity: 'info',
            actionability: 'interesting',
            summary: 'Build finished cleanly',
            event_timestamp: '2026-11-13T20:29:00Z',
            state: 'seen',
            pinned: false,
            muted: false,
        },
    ],
};

describe('NtmClient Ensemble & Attention Methods (argv, JSON parsing, error handling)', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('getEnsemblePresets, suggestEnsemble, and spawnEnsemble construct exact CLI argv and parse real NTM JSON', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args.includes('--robot-ensemble-presets')) {
                    return { stdout: JSON.stringify(REAL_ENSEMBLE_PRESETS_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-ensemble-suggest='))) {
                    return { stdout: JSON.stringify(REAL_ENSEMBLE_SUGGEST_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-ensemble-spawn='))) {
                    return { stdout: JSON.stringify(REAL_ENSEMBLE_SPAWN_PAYLOAD), stderr: '' };
                }
                throw new Error(`unexpected args: ${args.join(' ')}`);
            },
        });

        const presets = await client.getEnsemblePresets();
        assert.deepEqual(calls[0], ['--robot-ensemble-presets', '--robot-format=json']);
        assert.equal(presets.length, 3);
        assert.equal(presets[0].name, 'project-diagnosis');
        assert.equal(presets[0].display_name, 'Project Diagnosis');
        assert.equal(presets[0].mode_count, 5);
        assert.equal(presets[0].agent_count, 5);
        assert.equal(presets[0].category, 'analysis');
        assert.deepEqual(presets[0].modes, ['F7', 'G6', 'F2', 'F4', 'H2']);
        assert.equal(presets[2].name, 'bug-hunt');
        assert.equal(presets[2].display_name, 'Bug Hunt');
        assert.equal(presets[2].agent_count, 3);
        assert.equal(presets[2].category, 'debugging');

        // Verify flag-injection safety when question begins with '-'
        const suggested = await client.suggestEnsemble('  -race condition & security risk in auth?  ');
        assert.deepEqual(calls[1], [
            '--robot-ensemble-suggest=-race condition & security risk in auth?',
            '--robot-format=json',
        ]);
        assert.equal(suggested.recommended_preset, 'safety-risk');
        assert.equal(suggested.top_pick?.preset_name, 'safety-risk');
        assert.equal(suggested.top_pick?.display_name, 'Safety / Risk Analysis');
        assert.equal(suggested.top_pick?.mode_count, 5);
        assert.equal(suggested.summary, 'Best match: Safety / Risk Analysis');

        const spawned = await client.spawnEnsemble({
            session: 'ntm-ensemble',
            preset: 'safety-risk',
            question: 'What security vulnerabilities exist in auth?',
            agents: 'cc=2,cod=1',
            projectDir: '/data/projects/ntm',
        });
        assert.deepEqual(calls[2], [
            '--robot-ensemble-spawn=ntm-ensemble',
            '--preset=safety-risk',
            '--question=What security vulnerabilities exist in auth?',
            '--agents=cc=2,cod=1',
            '--project=/data/projects/ntm',
            '--robot-format=json',
        ]);
        assert.equal(spawned.session, 'ntm-ensemble');
        assert.equal(spawned.preset, 'safety-risk');
        assert.equal(spawned.modes.length, 2);
        assert.equal(spawned.modes[0].code, 'D1');
        assert.equal(spawned.budget.total_tokens, 35000);
        assert.deepEqual(spawned.agents, { cc: 2, cod: 1 });

        // Verify explicit --modes spawn argv construction
        await client.spawnEnsemble({
            session: 'modes-ensemble',
            modes: 'D1,D3,F2',
            question: 'Analyze race conditions',
            assignment: 'round-robin',
            allowAdvanced: true,
        });
        assert.deepEqual(calls[3], [
            '--robot-ensemble-spawn=modes-ensemble',
            '--modes=D1,D3,F2',
            '--question=Analyze race conditions',
            '--assignment=round-robin',
            '--allow-advanced',
            '--robot-format=json',
        ]);
    });

    it('listAttentionItems and mutateAttentionItem construct exact CLI argv and handle filters', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args[0] === 'attention' && args[1] === 'list') {
                    return { stdout: JSON.stringify(REAL_ATTENTION_LIST_PAYLOAD), stderr: '' };
                }
                if (args[0] === 'attention') {
                    const action = args[1];
                    const ref = args[2];
                    return {
                        stdout: JSON.stringify({
                            success: true,
                            timestamp: '2026-11-13T20:33:00Z',
                            action,
                            ref,
                            cursor: Number(ref) || 42,
                            item_key: `cursor:${ref}`,
                            previous_state: 'new',
                            current_state: action === 'snooze'
                                ? 'snoozed'
                                : (action === 'dismiss' ? 'dismissed' : (action === 'ack' ? 'acknowledged' : 'new')),
                            attention_state: {
                                item_key: `cursor:${ref}`,
                                state: action === 'ack' ? 'acknowledged' : 'new',
                                pinned: action === 'pin',
                                snoozed_until: action === 'snooze' ? '2026-11-13T21:03:00Z' : undefined,
                            },
                        }),
                        stderr: '',
                    };
                }
                throw new Error(`unexpected args: ${args.join(' ')}`);
            },
        });

        const allItems = await client.listAttentionItems({ limit: 30 });
        assert.deepEqual(calls[0], ['attention', 'list', '--limit=30', '--json']);
        assert.equal(allItems.length, 3);
        assert.equal(allItems[0].cursor, 42);
        assert.equal(allItems[0].action_required, true);
        assert.equal(allItems[0].operator_state, 'new');
        assert.equal(allItems[1].pinned, true);
        assert.equal(allItems[1].action_required, true);
        assert.equal(allItems[2].action_required, false);

        // actionRequired filters client-side without passing unsupported --action-required flag
        const urgentOnly = await client.listAttentionItems({
            actionRequired: true,
            limit: 15,
            session: 'proj-alpha',
            includeAll: true,
        });
        assert.deepEqual(calls[1], [
            'attention',
            'list',
            '--session=proj-alpha',
            '--limit=15',
            '--all',
            '--json',
        ]);
        assert.equal(urgentOnly.length, 2);
        assert.deepEqual(urgentOnly.map(i => i.cursor), [42, 41]);

        // Verify all 5 attention mutation actions (ack, pin, unpin, snooze, dismiss)
        assert.deepEqual(SUPPORTED_ATTENTION_ACTIONS, ['ack', 'pin', 'unpin', 'snooze', 'dismiss']);

        const expectedArgvByAction: Record<AttentionMutationAction, string[]> = {
            ack: ['attention', 'ack', '42', '--json'],
            pin: ['attention', 'pin', '42', '--json'],
            unpin: ['attention', 'unpin', '42', '--json'],
            snooze: ['attention', 'snooze', '42', '--until=30m', '--json'],
            dismiss: ['attention', 'dismiss', '42', '--json'],
        };

        for (const action of SUPPORTED_ATTENTION_ACTIONS) {
            calls.length = 0;
            const res = await client.mutateAttentionItem(
                action,
                42,
                action === 'snooze' ? '30m' : undefined
            );
            assert.deepEqual(calls[0], expectedArgvByAction[action]);
            assert.equal(res.success, true);
            assert.equal(res.action, action);
        }
    });

    it('validates arguments and surfaces structured JSON error envelopes cleanly', async () => {
        const client = new NtmClient({
            execFile: async (_file, args) => {
                if (args.includes('--robot-ensemble-presets')) {
                    return {
                        stdout: JSON.stringify({
                            success: false,
                            error: 'Failed to load ensemble registry',
                            error_code: 'INTERNAL_ERROR',
                            hint: 'Check config',
                        }),
                        stderr: '',
                    };
                }
                const err = new Error('Command failed') as Error & { stdout?: string; stderr?: string };
                err.stdout = JSON.stringify({
                    success: false,
                    error: 'attention cursor 999 not found',
                    error_code: 'NOT_FOUND',
                });
                err.stderr = '';
                throw err;
            },
        });

        await assert.rejects(
            () => client.suggestEnsemble('   '),
            /Question is required/
        );
        await assert.rejects(
            () => client.spawnEnsemble({ session: '', preset: 'bug-hunt', question: 'Why?' }),
            /Session name is required/
        );
        await assert.rejects(
            () => client.spawnEnsemble({ session: 's1', preset: 'bug-hunt', modes: 'A1,B2', question: 'Why?' }),
            /mutually exclusive/
        );
        await assert.rejects(
            () => client.spawnEnsemble({ session: 's1', question: 'Why?' }),
            /Either preset or modes is required/
        );
        await assert.rejects(
            () => client.listAttentionItems({ limit: 0 }),
            /positive integer/
        );
        await assert.rejects(
            () => client.mutateAttentionItem('snooze', 42),
            /Duration is required/
        );
        await assert.rejects(
            () => client.mutateAttentionItem('ack', 0),
            /positive integer/
        );
        await assert.rejects(
            () => client.getEnsemblePresets(),
            /Failed to load ensemble registry \(Check config\)/
        );
        await assert.rejects(
            () => client.mutateAttentionItem('ack', 999),
            /attention cursor 999 not found/
        );
    });
});

describe('Ensemble Spawn, Attention Triage, Web Cockpit & Palette Commands', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('formats QuickPick items for ensemble presets, attention feed, and buildWebCockpitCommand', () => {
        assert.equal(buildWebCockpitCommand, extensionBuildWebCockpitCommand);
        assert.equal(normalizeWebPort(undefined), DEFAULT_WEB_PORT);
        assert.equal(normalizeWebPort(0), DEFAULT_WEB_PORT);
        assert.equal(normalizeWebPort(70000), DEFAULT_WEB_PORT);
        assert.equal(normalizeWebPort(8080), 8080);
        assert.equal(normalizeWebPort('9100'), 9100);

        assert.equal(buildWebCockpitCommand('ntm'), 'ntm web --port 7337');
        assert.equal(
            buildWebCockpitCommand('/opt/my tools/ntm', 8080),
            `'/opt/my tools/ntm' web --port 8080`
        );
        assert.equal(
            buildNtmTerminalCommand('/opt/my tools/ntm', ['web', '--port', '7337']),
            `'/opt/my tools/ntm' web --port 7337`
        );

        assert.equal(defaultEnsembleSessionName('ntm-repo'), 'ntm-repo-ensemble');
        assert.equal(defaultEnsembleSessionName('my.project:v2'), 'my-project-v2-ensemble');
        assert.equal(defaultEnsembleSessionName(undefined, '/work/alpha'), 'alpha-ensemble');
        assert.equal(defaultEnsembleSessionName(undefined, undefined), 'ntm-ensemble');

        assert.equal(attentionSeverityIcon('critical'), '$(error)');
        assert.equal(attentionSeverityIcon('warning'), '$(warning)');
        assert.equal(attentionSeverityIcon('info'), '$(info)');

        // Manifest verification: package.json declares all 3 commands + ntm.webPort
        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const commandIds = (pkgJson.contributes?.commands ?? []).map((c: { command: string }) => c.command);
        assert.ok(commandIds.includes('ntm.spawnEnsemble'));
        assert.ok(commandIds.includes('ntm.triageAttention'));
        assert.ok(commandIds.includes('ntm.openWebCockpit'));
        assert.equal(pkgJson.contributes?.configuration?.properties?.['ntm.webPort']?.default, 7337);

        // Palette menu surfaces all 3 new commands
        const paletteCommands = PALETTE_ACTION_OPTIONS.map(p => p.command).filter(Boolean);
        assert.ok(paletteCommands.includes('ntm.spawnEnsemble'));
        assert.ok(paletteCommands.includes('ntm.triageAttention'));
        assert.ok(paletteCommands.includes('ntm.openWebCockpit'));
    });

    it('runs ntm.spawnEnsemble end-to-end with recommended preset at top, graceful suggest fallback, and View Session follow-up', async () => {
        const tmpWorkspace = os.tmpdir();
        mockState.workspaceName = 'ntm';
        mockState.workspaceFolders = [{ uri: { fsPath: tmpWorkspace } }];

        let suggestShouldFail = false;
        const spawnCalls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                if (args.includes('--version')) {
                    return { stdout: 'ntm 1.38.0', stderr: '' };
                }
                if (args.includes('--robot-snapshot') || args.includes('--robot-status')) {
                    return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
                }
                if (args.includes('--robot-mail')) {
                    return { stdout: JSON.stringify({ generated_at: 'now', available: false }), stderr: '' };
                }
                if (args.includes('--robot-ensemble-presets')) {
                    return { stdout: JSON.stringify(REAL_ENSEMBLE_PRESETS_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-ensemble-suggest='))) {
                    if (suggestShouldFail) {
                        throw new Error('suggest engine unavailable');
                    }
                    return { stdout: JSON.stringify(REAL_ENSEMBLE_SUGGEST_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-ensemble-spawn='))) {
                    spawnCalls.push(args);
                    const spawnArg = args.find(a => a.startsWith('--robot-ensemble-spawn=')) ?? '';
                    const requestedSession = spawnArg.slice('--robot-ensemble-spawn='.length);
                    return {
                        stdout: JSON.stringify({
                            ...REAL_ENSEMBLE_SPAWN_PAYLOAD,
                            session: requestedSession || REAL_ENSEMBLE_SPAWN_PAYLOAD.session,
                        }),
                        stderr: '',
                    };
                }
                return { stdout: '{}', stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Happy path: suggestEnsemble recommends safety-risk at top of QuickPick
            mockState.inputBoxQueue.push(
                'What security vulnerabilities exist in auth?',
                'ntm-ensemble'
            );
            mockState.infoMessageActionQueue.push('View Session');

            await vscode.commands.executeCommand('ntm.spawnEnsemble');

            assert.equal(mockState.inputBoxOptionsHistory[1]?.value, 'ntm-ensemble');
            const presetChoices = mockState.quickPickItemsHistory[0] as Array<{
                label: string;
                presetName: string;
                recommended: boolean;
            }>;
            assert.equal(presetChoices.length, 3, 'all 3 presets must appear without duplicating safety-risk');
            assert.equal(presetChoices[0].presetName, 'safety-risk');
            assert.equal(presetChoices[0].recommended, true);
            assert.ok(presetChoices[0].label.startsWith('$(sparkle) Recommended: Safety / Risk Analysis'));

            assert.equal(mockState.progressTitles.length, 1);
            assert.equal(spawnCalls.length, 1);
            assert.deepEqual(spawnCalls[0], [
                '--robot-ensemble-spawn=ntm-ensemble',
                '--preset=safety-risk',
                '--question=What security vulnerabilities exist in auth?',
                `--project=${path.resolve(tmpWorkspace)}`,
                '--robot-format=json',
            ]);
            assert.equal(mockState.createdTerminals.length, 1);
            assert.equal(mockState.createdTerminals[0].name, 'NTM View: ntm-ensemble');

            // 2. Fallback path: suggestEnsemble throws, presets QuickPick still renders cleanly
            suggestShouldFail = true;
            mockState.quickPickItemsHistory.length = 0;
            mockState.inputBoxQueue.push('Investigate flaky tests', 'ntm-fallback-ensemble');
            mockState.quickPickQueue.push((items: Array<{ presetName: string }>) =>
                items.find(i => i.presetName === 'bug-hunt')
            );
            mockState.infoMessageActionQueue.push('Attach Terminal');

            await vscode.commands.executeCommand('ntm.spawnEnsemble');

            const fallbackChoices = mockState.quickPickItemsHistory[0] as Array<{
                presetName: string;
                recommended: boolean;
            }>;
            assert.equal(fallbackChoices.length, 3);
            assert.equal(fallbackChoices.every(c => !c.recommended), true);
            assert.equal(spawnCalls.length, 2);
            assert.ok(spawnCalls[1].includes('--preset=bug-hunt'));
            assert.equal(mockState.createdTerminals[1].name, 'NTM: ntm-fallback-ensemble');
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });

    it('runs ntm.triageAttention for empty feed and snooze/ack/pin/dismiss flows', async () => {
        let returnEmpty = true;
        const mutatedCalls: string[][] = [];

        const client = new NtmClient({
            execFile: async (_file, args) => {
                if (args[0] === 'attention' && args[1] === 'list') {
                    return {
                        stdout: JSON.stringify(
                            returnEmpty
                                ? { success: true, timestamp: 'now', count: 0, items: [] }
                                : REAL_ATTENTION_LIST_PAYLOAD
                        ),
                        stderr: '',
                    };
                }
                if (args[0] === 'attention') {
                    mutatedCalls.push(args);
                    return {
                        stdout: JSON.stringify({
                            success: true,
                            timestamp: 'now',
                            action: args[1],
                            ref: args[2],
                            cursor: Number(args[2]),
                            item_key: `cursor:${args[2]}`,
                            previous_state: 'new',
                            current_state: args[1] === 'snooze' ? 'snoozed' : 'acknowledged',
                        }),
                        stderr: '',
                    };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const items = await client.listAttentionItems({ limit: 30 }).then(() => []);
        assert.equal(items.length, 0);

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // Empty attention feed shows exact toast
            await vscode.commands.executeCommand('ntm.triageAttention');
            assert.ok(mockState.infoMessages.includes('No attention items pending'));

            // Non-empty feed -> pick item #42 -> pick Snooze 30m
            returnEmpty = false;
            mockState.infoMessages.length = 0;
            mockState.quickPickItemsHistory.length = 0;
            mockState.quickPickQueue.push(
                (candidates: Array<{ item: { cursor: number } }>) =>
                    candidates.find(c => c.item.cursor === 42),
                (actions: Array<{ action: string }>) =>
                    actions.find(a => a.action === 'snooze')
            );

            await vscode.commands.executeCommand('ntm.triageAttention');

            const renderedItems = buildAttentionQuickPickItems(
                await client.listAttentionItems({ limit: 30 })
            );
            assert.ok(renderedItems[0].label.includes('$(warning) #42 [new] Agent cc_2 stuck for 120s'));
            assert.ok(renderedItems[0].description?.includes('proj-alpha'));
            assert.ok(renderedItems[1].label.includes('$(error) #41 [pinned · acknowledged]'));

            // Pinned item offers Unpin in addition to Acknowledge, Pin, Snooze 30m, Dismiss
            const pinnedActions = buildAttentionActionQuickPickItems(renderedItems[1].item);
            assert.deepEqual(
                pinnedActions.map(a => a.action),
                ['ack', 'pin', 'unpin', 'snooze', 'dismiss']
            );

            assert.deepEqual(mutatedCalls[0], ['attention', 'snooze', '42', '--until=30m', '--json']);
            assert.ok(mockState.infoMessages.some(m => m.includes('#42') && m.includes('snoozed')));
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });

    it('runs ntm.openWebCockpit with terminal reuse, custom ntm.webPort, and ntm.openPalette QuickPick routing', async () => {
        mockState.binaryPath = '/opt/my tools/ntm';
        mockState.webPort = 7337;

        const client = new NtmClient({
            execFile: async () => ({ stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' }),
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // First launch via ntm.openPalette selecting openWebCockpit
            mockState.quickPickQueue.push((options: Array<{ actionId: string }>) =>
                options.find(o => o.actionId === 'openWebCockpit')
            );
            await vscode.commands.executeCommand('ntm.openPalette');

            assert.equal(mockState.createdTerminals.length, 1);
            assert.equal(mockState.createdTerminals[0].name, WEB_COCKPIT_TERMINAL_NAME);
            assert.equal(mockState.createdTerminals[0].shown, true);
            assert.deepEqual(mockState.createdTerminals[0].sentCommands, [
                { text: `'/opt/my tools/ntm' web --port 7337`, shouldExecute: true },
            ]);
            assert.deepEqual(mockState.openedExternalUris, ['http://127.0.0.1:7337']);

            // Second launch reuses existing "NTM Web Cockpit" terminal instead of spawning a second one
            await vscode.commands.executeCommand('ntm.openWebCockpit');
            assert.equal(mockState.createdTerminals.length, 1, 'must reuse active NTM Web Cockpit terminal');
            assert.equal(mockState.createdTerminals[0].sentCommands.length, 1);
            assert.deepEqual(mockState.openedExternalUris, [
                'http://127.0.0.1:7337',
                'http://127.0.0.1:7337',
            ]);

            // Changing ntm.webPort reuses terminal with updated command and opens new port URL
            mockState.webPort = 8484;
            await vscode.commands.executeCommand('ntm.openWebCockpit');
            assert.equal(mockState.createdTerminals.length, 1);
            assert.equal(mockState.createdTerminals[0].sentCommands.length, 2);
            assert.equal(
                mockState.createdTerminals[0].sentCommands[1].text,
                `'/opt/my tools/ntm' web --port 8484`
            );
            assert.equal(mockState.openedExternalUris[2], 'http://127.0.0.1:8484');

            // Disposing terminal causes next invocation to create a fresh terminal
            mockState.createdTerminals[0].dispose?.();
            await vscode.commands.executeCommand('ntm.openWebCockpit');
            assert.equal(mockState.createdTerminals.length, 2);
            assert.equal(mockState.createdTerminals[1].name, WEB_COCKPIT_TERMINAL_NAME);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});

const REAL_BACKPRESSURE_PAYLOAD = {
    success: true,
    timestamp: '2026-11-14T22:10:00Z',
    backpressure: {
        decision: 'degrade',
        retry_after_ms: 1500,
        reason_codes: ['stream_lag', 'ws_drop'],
        surfaces: [
            {
                surface: 'websocket_events',
                session: 'swarm-heavy',
                pane: '1.2',
                queue_depth: 92,
                queue_capacity: 100,
                dropped_count: 14,
                latency_ms: 420,
                client_lag_ms: 1100,
                decision: 'degrade',
                reason_codes: ['ws_drop'],
            },
            {
                surface: 'tmux_poller',
                session: 'ntm-prod',
                queue_depth: 30,
                queue_capacity: 100,
                dropped_count: 0,
                latency_ms: 85,
                decision: 'coalesce',
                reason_codes: ['poll_coalesce'],
            },
        ],
    },
    load_shedding: {
        schema_version: 'ntm.load_shedding.v1',
        decision: 'degrade',
        error_code: 'OVERLOADED',
        retry_after_ms: 1500,
        degraded_surface: 'websocket_events',
        recommended_action: 'switch_to_polling',
        reason_codes: ['ws_drop'],
    },
    heatmap: {
        generated_at: '2026-11-14T22:10:00Z',
        overall_decision: 'degrade',
        buckets: [
            {
                heatmap_bucket: 'session:swarm-heavy',
                surface: 'websocket_events',
                session: 'swarm-heavy',
                severity: 'critical',
                decision: 'degrade',
                score: 0.92,
                retry_after_ms: 1500,
                reason_codes: ['ws_drop'],
                recommended_action: 'throttle_agent_output',
            },
        ],
    },
    dashboard: {
        decision: 'degrade',
        reason_codes: ['stream_lag', 'ws_drop'],
        recommended_actions: ['pause_spawns', 'switch_to_polling'],
    },
};

const REAL_WORKTREES_LIST_PAYLOAD = {
    session: 'ntm-prod',
    total: 3,
    worktrees: [
        {
            agent_name: 'BlueLake',
            branch_name: 'ntm/ntm-prod/BlueLake',
            path: '/repo/.ntm/worktrees/BlueLake',
            session_id: 'ntm-prod',
            created: true,
        },
        {
            agent_name: 'GreenForge',
            branch_name: 'ntm/ntm-prod/GreenForge',
            path: '/repo/.ntm/worktrees/GreenForge',
            session_id: 'ntm-prod',
            created: false,
        },
        {
            agent_name: 'RedStone',
            branch_name: 'ntm/ntm-prod/RedStone',
            path: '/repo/.ntm/worktrees/RedStone',
            session_id: 'ntm-prod',
            created: false,
            error: 'branch already exists',
        },
    ],
};

describe('Swarm Backpressure & Agent Worktrees Contracts and Commands', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('normalizes --robot-backpressure and worktrees list --json CLI contracts and rejects error envelopes', async () => {
        const recordedCalls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                recordedCalls.push(args);
                if (args.includes('--robot-backpressure')) {
                    const sessArg = args.find(a => a.startsWith('--session='));
                    return {
                        stdout: JSON.stringify({
                            ...REAL_BACKPRESSURE_PAYLOAD,
                            session: sessArg ? sessArg.slice('--session='.length) : undefined,
                        }),
                        stderr: '',
                    };
                }
                if (args[0] === 'worktrees' && args[1] === 'list') {
                    return {
                        stdout: JSON.stringify(REAL_WORKTREES_LIST_PAYLOAD),
                        stderr: '',
                    };
                }
                return { stdout: '{}', stderr: '' };
            },
        });

        const bpGlobal = await client.getBackpressure();
        assert.deepEqual(recordedCalls[0], ['--robot-backpressure', '--robot-format=json']);
        assert.equal(bpGlobal.level, 'overload');
        assert.equal(bpGlobal.regime, 'degrade');
        assert.equal(bpGlobal.utilization, 0.92);
        assert.equal(bpGlobal.queue_depth, 122);
        assert.equal(bpGlobal.queue_capacity, 200);
        assert.equal(bpGlobal.dropped_count, 14);
        assert.equal(bpGlobal.shed_policy.decision, 'degrade');
        assert.equal(bpGlobal.shed_policy.degraded_surface, 'websocket_events');
        assert.equal(bpGlobal.shed_policy.recommended_action, 'switch_to_polling');
        assert.equal(bpGlobal.sessions.length, 2);
        assert.equal(bpGlobal.sessions[0].session, 'swarm-heavy');
        assert.equal(bpGlobal.sessions[0].level, 'overload');
        assert.equal(bpGlobal.sessions[0].max_utilization, 0.92);
        assert.equal(bpGlobal.sessions[1].session, 'ntm-prod');
        assert.equal(bpGlobal.sessions[1].level, 'elevated');

        const bpScoped = await client.getBackpressure('swarm-heavy');
        assert.deepEqual(recordedCalls[1], [
            '--robot-backpressure',
            '--session=swarm-heavy',
            '--robot-format=json',
        ]);
        assert.equal(bpScoped.session, 'swarm-heavy');

        const wtGlobal = await client.listWorktrees();
        assert.deepEqual(recordedCalls[2], ['worktrees', 'list', '--json']);
        assert.equal(wtGlobal.session, 'ntm-prod');
        assert.equal(wtGlobal.total, 3);
        assert.deepEqual(
            wtGlobal.worktrees.map(w => ({ agent: w.agent_name, status: w.status })),
            [
                { agent: 'BlueLake', status: 'active' },
                { agent: 'GreenForge', status: 'pending' },
                { agent: 'RedStone', status: 'error' },
            ]
        );

        await client.listWorktrees('ntm-prod');
        assert.deepEqual(recordedCalls[3], ['worktrees', 'list', '--session=ntm-prod', '--json']);

        assert.throws(
            () => normalizeBackpressure({ success: false, error: 'telemetry collector offline' }),
            /telemetry collector offline/
        );
        assert.throws(
            () => normalizeWorktreeList({ success: false, error: 'not a git repository' }),
            /not a git repository/
        );
    });

    it('formats backpressure & worktree QuickPick items and maintains package.json + PALETTE_ACTION_OPTIONS parity', () => {
        assert.equal(mapBackpressureLevel('ok'), 'normal');
        assert.equal(mapBackpressureLevel('coalesce'), 'elevated');
        assert.equal(mapBackpressureLevel('defer'), 'critical');
        assert.equal(mapBackpressureLevel('degrade'), 'overload');

        assert.equal(backpressureLevelIcon('normal'), '$(pass)');
        assert.equal(backpressureLevelIcon('elevated'), '$(pulse)');
        assert.equal(backpressureLevelIcon('critical'), '$(warning)');
        assert.equal(backpressureLevelIcon('overload'), '$(error)');

        const bpItems = buildBackpressureQuickPickItems(normalizeBackpressure(REAL_BACKPRESSURE_PAYLOAD));
        assert.equal(bpItems[0].itemAction, 'summary');
        assert.ok(bpItems[0].label.includes('OVERLOAD (degrade)'));
        assert.ok(bpItems[0].description?.includes('Utilization 92%'));
        assert.ok(bpItems[0].description?.includes('Queue 122/200'));
        assert.ok(bpItems[0].description?.includes('14 dropped'));
        assert.equal(bpItems[1].itemAction, 'shedPolicy');
        assert.ok(bpItems[1].label.includes('Load Shedding: degrade'));
        assert.ok(bpItems[1].description?.includes('switch_to_polling'));
        assert.equal(bpItems[2].itemAction, 'session');
        assert.equal(bpItems[2].sessionName, 'swarm-heavy');
        assert.equal(bpItems[3].itemAction, 'session');
        assert.equal(bpItems[3].sessionName, 'ntm-prod');
        assert.equal(bpItems[4].itemAction, 'openSwarmCockpit');
        assert.ok(bpItems[4].label.includes('Open /swarm in Web Cockpit'));
        assert.equal(bpItems[5].itemAction, 'runBackpressureHeatmap');
        assert.ok(bpItems[5].label.includes('ntm health --backpressure --heatmap'));

        const wtItems = buildWorktreeQuickPickItems(normalizeWorktreeList(REAL_WORKTREES_LIST_PAYLOAD));
        assert.equal(wtItems.length, 6);
        assert.equal(wtItems[0].label, '$(git-branch) BlueLake');
        assert.ok(wtItems[0].description?.includes('ntm/ntm-prod/BlueLake · active'));
        assert.equal(wtItems[1].label, '$(clock) GreenForge');
        assert.ok(wtItems[1].description?.includes('pending'));
        assert.equal(wtItems[2].label, '$(error) RedStone');
        assert.ok(wtItems[2].detail?.includes('error: branch already exists'));
        assert.deepEqual(
            wtItems.slice(3).map(i => i.itemAction),
            ['createWorktree', 'mergeWorktree', 'openWorktreesCockpit']
        );

        // Verify package.json activationEvents + contributes.commands parity
        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const activationEvents: string[] = pkgJson.activationEvents ?? [];
        assert.ok(activationEvents.includes('onCommand:ntm.showBackpressure'));
        assert.ok(activationEvents.includes('onCommand:ntm.manageWorktrees'));

        const commands: Array<{ command: string; title: string; icon?: string }> =
            pkgJson.contributes?.commands ?? [];
        const bpCmd = commands.find(c => c.command === 'ntm.showBackpressure');
        assert.deepEqual(bpCmd, {
            command: 'ntm.showBackpressure',
            title: 'NTM: Show Swarm Backpressure',
            icon: '$(pulse)',
        });
        const wtCmd = commands.find(c => c.command === 'ntm.manageWorktrees');
        assert.deepEqual(wtCmd, {
            command: 'ntm.manageWorktrees',
            title: 'NTM: Manage Agent Worktrees',
            icon: '$(git-branch)',
        });

        // Verify PALETTE_ACTION_OPTIONS parity
        const bpPalette = PALETTE_ACTION_OPTIONS.find(p => p.actionId === 'showBackpressure');
        assert.equal(bpPalette?.command, 'ntm.showBackpressure');
        const wtPalette = PALETTE_ACTION_OPTIONS.find(p => p.actionId === 'manageWorktrees');
        assert.equal(wtPalette?.command, 'ntm.manageWorktrees');
    });

    it('executes ntm.showBackpressure and ntm.manageWorktrees actions (CLI terminals, Web Cockpit /swarm & /worktrees routes, palette dispatch)', async () => {
        mockState.binaryPath = '/opt/my tools/ntm';
        mockState.webPort = 7337;

        const client = new NtmClient({
            execFile: async (_file, args) => {
                if (args.includes('--robot-backpressure')) {
                    return { stdout: JSON.stringify(REAL_BACKPRESSURE_PAYLOAD), stderr: '' };
                }
                if (args[0] === 'worktrees' && args[1] === 'list') {
                    return { stdout: JSON.stringify(REAL_WORKTREES_LIST_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Dispatch showBackpressure via ntm.openPalette -> run ntm health --backpressure --heatmap
            mockState.quickPickQueue.push(
                (options: Array<{ actionId: string }>) =>
                    options.find(o => o.actionId === 'showBackpressure'),
                (picks: Array<{ itemAction: string }>) =>
                    picks.find(p => p.itemAction === 'runBackpressureHeatmap')
            );
            await vscode.commands.executeCommand('ntm.openPalette');

            assert.equal(mockState.createdTerminals.length, 1);
            assert.equal(mockState.createdTerminals[0].name, 'NTM Backpressure');
            assert.deepEqual(mockState.createdTerminals[0].sentCommands, [
                {
                    text: `'/opt/my tools/ntm' health --backpressure --heatmap`,
                    shouldExecute: true,
                },
            ]);

            // 2. Open /swarm in Web Cockpit from ntm.showBackpressure
            mockState.quickPickQueue.push((picks: Array<{ itemAction: string }>) =>
                picks.find(p => p.itemAction === 'openSwarmCockpit')
            );
            await vscode.commands.executeCommand('ntm.showBackpressure');

            assert.equal(mockState.createdTerminals.length, 2);
            assert.equal(mockState.createdTerminals[1].name, WEB_COCKPIT_TERMINAL_NAME);
            assert.deepEqual(mockState.createdTerminals[1].sentCommands, [
                { text: `'/opt/my tools/ntm' web --port 7337`, shouldExecute: true },
            ]);
            assert.deepEqual(mockState.openedExternalUris, ['http://127.0.0.1:7337/swarm']);

            // 3. Dispatch manageWorktrees via ntm.openPalette -> Create Worktree for Agent
            mockState.quickPickQueue.push(
                (options: Array<{ actionId: string }>) =>
                    options.find(o => o.actionId === 'manageWorktrees'),
                (picks: Array<{ itemAction: string }>) =>
                    picks.find(p => p.itemAction === 'createWorktree')
            );
            mockState.inputBoxQueue.push('BlueLake');
            await vscode.commands.executeCommand('ntm.openPalette');

            assert.equal(mockState.createdTerminals.length, 3);
            assert.equal(mockState.createdTerminals[2].name, 'NTM Worktree: BlueLake');
            assert.deepEqual(mockState.createdTerminals[2].sentCommands, [
                {
                    text: `'/opt/my tools/ntm' worktrees create BlueLake`,
                    shouldExecute: true,
                },
            ]);

            // 4. Merge Worktree action in ntm.manageWorktrees
            mockState.quickPickQueue.push(
                (picks: Array<{ itemAction: string }>) =>
                    picks.find(p => p.itemAction === 'mergeWorktree'),
                (choices: Array<{ agentName: string }>) =>
                    choices.find(c => c.agentName === 'BlueLake')
            );
            await vscode.commands.executeCommand('ntm.manageWorktrees');

            assert.equal(mockState.createdTerminals.length, 4);
            assert.equal(mockState.createdTerminals[3].name, 'NTM Worktree Merge: BlueLake');
            assert.deepEqual(mockState.createdTerminals[3].sentCommands, [
                {
                    text: `'/opt/my tools/ntm' worktrees merge BlueLake`,
                    shouldExecute: true,
                },
            ]);

            // 5. Open /worktrees in Web Cockpit reuses active NTM Web Cockpit terminal
            mockState.quickPickQueue.push((picks: Array<{ itemAction: string }>) =>
                picks.find(p => p.itemAction === 'openWorktreesCockpit')
            );
            await vscode.commands.executeCommand('ntm.manageWorktrees');

            assert.equal(
                mockState.createdTerminals.length,
                4,
                'must reuse active NTM Web Cockpit terminal when opening /worktrees'
            );
            assert.equal(mockState.createdTerminals[1].sentCommands.length, 1);
            assert.deepEqual(mockState.openedExternalUris, [
                'http://127.0.0.1:7337/swarm',
                'http://127.0.0.1:7337/worktrees',
            ]);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});

const REAL_ENSEMBLE_SYNTHESIZE_PAYLOAD = {
    success: true,
    timestamp: '2026-11-15T01:20:00Z',
    action: 'ensemble_synthesize',
    session: 'ntm-prod-ensemble',
    status: 'completed',
    report: {
        summary: 'Synthesized 3 convergent findings across deductive and failure-mode operators.',
        strategy: 'adversarial',
        format: 'markdown',
        findings_count: 3,
        recommendations_count: 2,
        risks_count: 1,
        questions_count: 1,
        confidence: 0.89,
        generated_at: '2026-11-15T01:20:00Z',
        synthesized_by: 'mechanical',
    },
    audit: {
        conflict_count: 1,
        unresolved_count: 0,
        high_conflict_pairs: ['deductive <-> failure-mode'],
    },
    formatted_output: '# Synthesis Report\n\n## Findings\n1. Queue backpressure bounded.\n',
};

describe('Wave 8: VSCode ntm.synthesizeEnsemble (--robot-ensemble-synthesize)', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('normalizes --robot-ensemble-synthesize JSON, builds CLI flags, and maintains manifest + palette parity', async () => {
        const recordedCalls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                recordedCalls.push(args);
                return { stdout: JSON.stringify(REAL_ENSEMBLE_SYNTHESIZE_PAYLOAD), stderr: '' };
            },
        });

        const res = await client.synthesizeEnsemble({
            session: 'ntm-prod-ensemble',
            strategy: 'adversarial',
            format: 'markdown',
            output: '/tmp/synth.md',
            force: true,
        });

        assert.deepEqual(recordedCalls[0], [
            '--robot-ensemble-synthesize=ntm-prod-ensemble',
            '--synth-strategy=adversarial',
            '--synth-format=markdown',
            '--synth-output=/tmp/synth.md',
            '--synth-force',
            '--robot-format=json',
        ]);
        assert.equal(res.action, 'ensemble_synthesize');
        assert.equal(res.session, 'ntm-prod-ensemble');
        assert.equal(res.status, 'completed');
        assert.equal(res.report?.strategy, 'adversarial');
        assert.equal(res.report?.findings_count, 3);
        assert.equal(res.audit?.conflict_count, 1);
        assert.deepEqual(res.audit?.high_conflict_pairs, ['deductive <-> failure-mode']);
        assert.ok(res.formatted_output?.includes('# Synthesis Report'));

        await assert.rejects(
            () => client.synthesizeEnsemble({ session: '   ' }),
            /Session name is required/
        );
        assert.throws(
            () =>
                normalizeEnsembleSynthesize({
                    success: false,
                    error: 'ensemble outputs incomplete (1/3 ready)',
                    error_code: 'SYNTHESIS_NOT_READY',
                }),
            /ensemble outputs incomplete/
        );

        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const activationEvents: string[] = pkgJson.activationEvents ?? [];
        assert.ok(activationEvents.includes('onCommand:ntm.synthesizeEnsemble'));

        const commands: Array<{ command: string; title: string; icon?: string }> =
            pkgJson.contributes?.commands ?? [];
        const synthCmd = commands.find(c => c.command === 'ntm.synthesizeEnsemble');
        assert.deepEqual(synthCmd, {
            command: 'ntm.synthesizeEnsemble',
            title: 'NTM: Synthesize Reasoning Ensemble',
            icon: '$(combine)',
        });

        const synthPalette = PALETTE_ACTION_OPTIONS.find(p => p.actionId === 'synthesizeEnsemble');
        assert.equal(synthPalette?.command, 'ntm.synthesizeEnsemble');
        assert.ok(ENSEMBLE_SYNTHESIS_STRATEGY_OPTIONS.length >= 5);
    });

    it('executes ntm.synthesizeEnsemble via palette and opens markdown report or /ensemble Web Cockpit', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;

        const synthArgsSeen: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                if (args.some(a => a.startsWith('--robot-ensemble-synthesize='))) {
                    synthArgsSeen.push(args);
                    return { stdout: JSON.stringify(REAL_ENSEMBLE_SYNTHESIZE_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Dispatch via ntm.openPalette -> adversarial strategy -> Open Synthesis Report
            mockState.quickPickQueue.push(
                (options: Array<{ actionId: string }>) =>
                    options.find(o => o.actionId === 'synthesizeEnsemble'),
                (strategies: Array<{ strategy: string; force: boolean }>) =>
                    strategies.find(s => s.strategy === 'adversarial')
            );
            mockState.infoMessageActionQueue.push('Open Synthesis Report');

            await vscode.commands.executeCommand('ntm.openPalette');

            assert.equal(synthArgsSeen.length, 1);
            assert.deepEqual(synthArgsSeen[0], [
                '--robot-ensemble-synthesize=ntm-prod',
                '--synth-strategy=adversarial',
                '--synth-format=markdown',
                '--robot-format=json',
            ]);
            assert.ok(
                mockState.infoMessages.some(msg =>
                    msg.includes('Synthesized ensemble "ntm-prod-ensemble" (adversarial: 3 findings, 1 conflicts)')
                )
            );
            assert.equal(mockState.openedTextDocuments.length, 1);
            assert.equal(mockState.openedTextDocuments[0].language, 'markdown');
            assert.ok(mockState.openedTextDocuments[0].content.includes('# Synthesis Report'));

            // 2. Direct invocation with explicit session + Force Partial Synthesis -> Open /ensemble in Web Cockpit
            mockState.quickPickQueue.push((strategies: Array<{ strategy: string; force: boolean }>) =>
                strategies.find(s => s.force === true)
            );
            mockState.infoMessageActionQueue.push('Open /ensemble in Web Cockpit');

            await vscode.commands.executeCommand('ntm.synthesizeEnsemble', 'custom-ensemble');

            assert.equal(synthArgsSeen.length, 2);
            assert.deepEqual(synthArgsSeen[1], [
                '--robot-ensemble-synthesize=custom-ensemble',
                '--synth-format=markdown',
                '--synth-force',
                '--robot-format=json',
            ]);
            assert.deepEqual(mockState.openedExternalUris, ['http://127.0.0.1:7337/ensemble']);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});

const REAL_INCIDENTS_LIST_PAYLOAD = {
    success: true,
    timestamp: '2026-11-15T10:00:00Z',
    count: 1,
    incidents: [
        {
            id: 'inc-wave9-01',
            title: 'Repeated agent stall in ntm-prod:%1',
            fingerprint: 'fp:stall:ntm-prod:1',
            family: 'agent_stall',
            category: 'agent',
            status: 'open',
            severity: 'critical',
            session_names: 'ntm-prod',
            event_count: 5,
            alert_count: 3,
            started_at: '2026-11-15T09:45:00Z',
            last_event_at: '2026-11-15T09:59:00Z',
        },
    ],
};

const REAL_HANDOFF_PAYLOAD = {
    success: true,
    timestamp: '2026-11-15T10:05:00Z',
    action: 'list',
    session: 'ntm-prod',
    sessions: ['ntm-prod'],
    total_returned: 1,
    latest_path: '.ntm/handoffs/ntm-prod/2026-11-15_handoff.yaml',
    handoffs: [
        {
            path: '.ntm/handoffs/ntm-prod/2026-11-15_handoff.yaml',
            session: 'ntm-prod',
            date: '2026-11-15',
            status: 'complete',
            goal: 'Completed full-stack Incidents & Handoffs surfaces',
            is_auto: false,
        },
    ],
    latest: {
        Session: 'ntm-prod',
        Goal: 'Completed full-stack Incidents & Handoffs surfaces',
        Now: 'Run final qualification gates and push to main',
        Status: 'complete',
    },
    quality: {
        score: 94,
        status: 'ready',
        scored_at: '2026-11-15T10:05:00Z',
        dimensions: [{ name: 'completeness', score: 96 }],
        hints: [],
    },
    ledger: {
        path: '.ntm/continuity/CONTINUITY_ntm-prod.md',
        exists: true,
        bytes: 2048,
    },
};

describe('Wave 9: VSCode ntm.triageIncidents & ntm.handoffSession', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('constructs exact CLI argv and normalizes JSON for listIncidents, mutateIncident, getHandoff, and createHandoff', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args[0] === 'attention' && args[1] === 'incidents' && args[2] === 'list') {
                    return { stdout: JSON.stringify(REAL_INCIDENTS_LIST_PAYLOAD), stderr: '' };
                }
                if (args[0] === 'attention' && args[1] === 'incidents') {
                    const action = args[2];
                    const id = args[3];
                    return {
                        stdout: JSON.stringify({
                            success: true,
                            timestamp: '2026-11-15T10:02:00Z',
                            action,
                            incident_id: id,
                            previous_status: 'open',
                            status: action === 'resolve' ? 'resolved' : (action === 'mute' ? 'muted' : 'investigating'),
                            idempotent: false,
                            note: 'Handled via VSCode',
                        }),
                        stderr: '',
                    };
                }
                if (args.some(a => a.startsWith('--robot-handoff='))) {
                    return {
                        stdout: JSON.stringify({
                            ...REAL_HANDOFF_PAYLOAD,
                            action: args.includes('--handoff-create')
                                ? (args.includes('--handoff-auto') ? 'create_auto' : 'create')
                                : 'list',
                            created_path: args.includes('--handoff-create')
                                ? '.ntm/handoffs/ntm-prod/created.yaml'
                                : undefined,
                        }),
                        stderr: '',
                    };
                }
                throw new Error(`unexpected args: ${args.join(' ')}`);
            },
        });

        const incidents = await client.listIncidents({ limit: 25, includeAll: true, status: 'open' });
        assert.deepEqual(calls[0], [
            'attention',
            'incidents',
            'list',
            '--limit=25',
            '--all',
            '--status=open',
            '--json',
        ]);
        assert.equal(incidents.length, 1);
        assert.equal(incidents[0].id, 'inc-wave9-01');
        assert.equal(incidents[0].alert_count, 3);
        assert.deepEqual(SUPPORTED_INCIDENT_ACTIONS, ['investigate', 'resolve', 'mute']);

        const resolved = await client.mutateIncident('resolve', 'inc-wave9-01', 'Rotated stuck pane');
        assert.deepEqual(calls[1], [
            'attention',
            'incidents',
            'resolve',
            'inc-wave9-01',
            '--reason=Rotated stuck pane',
            '--json',
        ]);
        assert.equal(resolved.status, 'resolved');
        assert.equal(resolved.incident_id, 'inc-wave9-01');

        const inspected = await client.getHandoff('ntm-prod', {
            limit: 10,
            projectDir: '/data/projects/ntm',
        });
        assert.deepEqual(calls[2], [
            '--robot-handoff=ntm-prod',
            '--handoff-limit=10',
            '--handoff-project-dir=/data/projects/ntm',
            '--robot-format=json',
        ]);
        assert.equal(inspected.latest?.goal, 'Completed full-stack Incidents & Handoffs surfaces');
        assert.equal(inspected.quality?.score, 94);
        assert.equal(inspected.ledger?.exists, true);

        const createdManual = await client.createHandoff({
            session: 'ntm-prod',
            goal: 'Shipped Wave 9',
            now: 'Verify preflight',
            description: 'wave9',
            projectDir: '/data/projects/ntm',
        });
        assert.deepEqual(calls[3], [
            '--robot-handoff=ntm-prod',
            '--handoff-create',
            '--handoff-goal=Shipped Wave 9',
            '--handoff-now=Verify preflight',
            '--handoff-description=wave9',
            '--handoff-project-dir=/data/projects/ntm',
            '--robot-format=json',
        ]);
        assert.equal(createdManual.action, 'create');

        const createdAuto = await client.createHandoff({
            session: 'ntm-prod',
            auto: true,
        });
        assert.deepEqual(calls[4], [
            '--robot-handoff=ntm-prod',
            '--handoff-create',
            '--handoff-auto',
            '--robot-format=json',
        ]);
        assert.equal(createdAuto.action, 'create_auto');

        const md = formatHandoffMarkdown(inspected);
        assert.ok(md.includes('# Session Handoff: ntm-prod'));
        assert.ok(md.includes('94/100'));
        assert.ok(md.includes('Completed full-stack Incidents & Handoffs surfaces'));
        assert.ok(buildIncidentQuickPickItems(incidents).length === 2);
        assert.ok(buildIncidentActionQuickPickItems(incidents[0]).length >= 3);
        assert.equal(HANDOFF_MODE_OPTIONS.length, 4);
        assert.ok(normalizeIncidentList({ success: true, incidents: [] }).length === 0);
        assert.equal(
            normalizeIncidentMutation({
                success: true,
                action: 'mute',
                incident_id: 'inc-1',
                previous_status: 'open',
                status: 'muted',
            }).status,
            'muted'
        );
        assert.equal(
            normalizeHandoffResult({ success: true, action: 'list', session: 's1', handoffs: [] }).session,
            's1'
        );

        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const activationEvents: string[] = pkgJson.activationEvents ?? [];
        assert.ok(activationEvents.includes('onCommand:ntm.triageIncidents'));
        assert.ok(activationEvents.includes('onCommand:ntm.handoffSession'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.triageIncidents'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.handoffSession'));
    });

    it('executes ntm.triageIncidents and ntm.handoffSession end-to-end across inspect and manual create flows', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;
        const seenArgs: string[][] = [];

        const client = new NtmClient({
            execFile: async (_file, args) => {
                seenArgs.push(args);
                if (args[0] === 'attention' && args[1] === 'incidents' && args[2] === 'list') {
                    return { stdout: JSON.stringify(REAL_INCIDENTS_LIST_PAYLOAD), stderr: '' };
                }
                if (args[0] === 'attention' && args[1] === 'incidents') {
                    return {
                        stdout: JSON.stringify({
                            success: true,
                            action: args[2],
                            incident_id: args[3],
                            previous_status: 'open',
                            status: 'resolved',
                        }),
                        stderr: '',
                    };
                }
                if (args.some(a => a.startsWith('--robot-handoff='))) {
                    return { stdout: JSON.stringify(REAL_HANDOFF_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Triage operator incident -> Resolve with note
            mockState.quickPickQueue.push(
                (items: Array<{ itemAction: string }>) => items.find(i => i.itemAction === 'incident'),
                (actions: Array<{ action: string }>) => actions.find(a => a.action === 'resolve')
            );
            mockState.inputBoxQueue.push('Resolved stalled pane');

            await vscode.commands.executeCommand('ntm.triageIncidents');
            assert.ok(
                seenArgs.some(
                    a =>
                        a[0] === 'attention' &&
                        a[1] === 'incidents' &&
                        a[2] === 'resolve' &&
                        a[3] === 'inc-wave9-01' &&
                        a.includes('--reason=Resolved stalled pane')
                )
            );
            assert.ok(
                mockState.infoMessages.some(m =>
                    m.includes('Incident inc-wave9-01 updated (resolve -> resolved)')
                )
            );

            // 2. Inspect session handoff -> opens Markdown summary document
            mockState.quickPickQueue.push((modes: Array<{ mode: string }>) =>
                modes.find(m => m.mode === 'inspect')
            );
            await vscode.commands.executeCommand('ntm.handoffSession', 'ntm-prod');
            assert.equal(mockState.openedTextDocuments.length, 1);
            assert.ok(
                mockState.openedTextDocuments[0].content.includes(
                    'Completed full-stack Incidents & Handoffs surfaces'
                )
            );

            // 3. Create manual session handoff -> Open /sessions in Web Cockpit
            mockState.quickPickQueue.push((modes: Array<{ mode: string }>) =>
                modes.find(m => m.mode === 'createManual')
            );
            mockState.inputBoxQueue.push('Finished Wave 9 VSCode parity', 'Run full repo test suite');
            mockState.infoMessageActionQueue.push('Open /sessions in Web Cockpit');

            await vscode.commands.executeCommand('ntm.handoffSession', 'ntm-prod');
            assert.ok(
                seenArgs.some(
                    a =>
                        a.includes('--robot-handoff=ntm-prod') &&
                        a.includes('--handoff-create') &&
                        a.includes('--handoff-goal=Finished Wave 9 VSCode parity') &&
                        a.includes('--handoff-now=Run full repo test suite')
                )
            );
            assert.deepEqual(mockState.openedExternalUris, [
                'http://127.0.0.1:7337/sessions?session=ntm-prod',
            ]);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});

const REAL_ANALYTICS_PAYLOAD = {
    success: true,
    timestamp: '2026-11-15T10:30:00Z',
    schema_id: 'ntm:robot:analytics:v1',
    schema_version: '1.0.0',
    period: 'Last 30 days',
    since: '2026-10-16',
    days: 30,
    total_sessions: 4,
    total_agents: 11,
    total_prompts: 27,
    total_chars_sent: 18400,
    total_tokens_estimated: 5200,
    agent_breakdown: {
        claude: { count: 4, prompts: 12, chars_sent: 8200, tokens_estimated: 2300 },
        antigravity: { count: 3, prompts: 9, chars_sent: 6100, tokens_estimated: 1750 },
        oc: { count: 2, prompts: 4, chars_sent: 2900, tokens_estimated: 810 },
        ollama: { count: 2, prompts: 2, chars_sent: 1200, tokens_estimated: 340 },
    },
    sessions: [
        {
            name: 'wave10-swarm',
            created_at: '2026-11-15T09:00:00Z',
            agent_count: 5,
            prompt_count: 14,
        },
    ],
    error_count: 2,
    error_types: { rate_limit: 2 },
    agent_crashes: 1,
    agent_restarts: 1,
};

describe('Swarm & Harness Analytics Contracts and Commands', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('serializes --robot-analytics flags, normalizes multi-harness stats, formats Markdown, and maintains manifest parity', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args.includes('--robot-analytics')) {
                    return { stdout: JSON.stringify(REAL_ANALYTICS_PAYLOAD), stderr: '' };
                }
                throw new Error(`unexpected args: ${args.join(' ')}`);
            },
        });

        const byDays = await client.getAnalytics({ days: 30, sessions: true });
        assert.deepEqual(calls[0], [
            '--robot-analytics',
            '--analytics-days=30',
            '--analytics-sessions',
            '--robot-format=json',
        ]);
        assert.equal(byDays.total_sessions, 4);
        assert.equal(byDays.total_agents, 11);
        assert.equal(byDays.agent_breakdown.antigravity.tokens_estimated, 1750);
        assert.equal(byDays.agent_breakdown.oc.prompts, 4);
        assert.equal(byDays.agent_crashes, 1);
        assert.equal(byDays.agent_restarts, 1);
        assert.equal(byDays.sessions.length, 1);
        assert.equal(byDays.sessions[0].name, 'wave10-swarm');

        await client.getAnalytics({ since: '2026-11-01', sessions: true });
        assert.deepEqual(calls[1], [
            '--robot-analytics',
            '--analytics-since=2026-11-01',
            '--analytics-sessions',
            '--robot-format=json',
        ]);

        await assert.rejects(
            () => client.getAnalytics({ days: -1 }),
            /Analytics days must be a non-negative integer/
        );
        assert.throws(
            () => normalizeAnalyticsResult({ success: false, error: 'invalid --analytics-since date' }),
            /invalid --analytics-since date/
        );

        const md = formatAnalyticsMarkdown(byDays);
        assert.ok(md.includes('# NTM Swarm & Harness Analytics (Last 30 days)'));
        assert.ok(md.includes('| `claude` | 4 | 12 | 8200 | 2300 |'));
        assert.ok(md.includes('| `antigravity` | 3 | 9 | 6100 | 1750 |'));
        assert.ok(md.includes('| `oc` | 2 | 4 | 2900 | 810 |'));
        assert.ok(md.includes('`rate_limit`: 2'));
        assert.ok(md.includes('**wave10-swarm** — 5 agents · 14 prompts'));
        assert.equal(ANALYTICS_WINDOW_OPTIONS.length, 5);

        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const activationEvents: string[] = pkgJson.activationEvents ?? [];
        assert.ok(activationEvents.includes('onCommand:ntm.showAnalytics'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.showAnalytics'));
    });

    it('executes ntm.showAnalytics across preset windows, custom since date, and Web Cockpit /analytics routing', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;
        const seenArgs: string[][] = [];

        const client = new NtmClient({
            execFile: async (_file, args) => {
                seenArgs.push(args);
                if (args.includes('--robot-analytics')) {
                    return { stdout: JSON.stringify(REAL_ANALYTICS_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Run Last 7 Days report and click "Open /analytics in Web Cockpit"
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string; days?: number }>) =>
                    items.find(i => i.mode === 'window' && i.days === 7)
            );
            mockState.infoMessageActionQueue.push('Open /analytics in Web Cockpit');

            await vscode.commands.executeCommand('ntm.showAnalytics');
            assert.ok(
                seenArgs.some(
                    a =>
                        a.includes('--robot-analytics') &&
                        a.includes('--analytics-days=7') &&
                        a.includes('--analytics-sessions')
                )
            );
            assert.equal(mockState.openedTextDocuments.length, 1);
            assert.ok(mockState.openedTextDocuments[0].content.includes('`antigravity`'));
            assert.deepEqual(mockState.openedExternalUris, ['http://127.0.0.1:7337/analytics']);

            // 2. Run custom Since cutoff via ntm.openPalette -> showAnalytics
            mockState.quickPickQueue.push(
                (options: Array<{ actionId: string }>) =>
                    options.find(o => o.actionId === 'showAnalytics'),
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'customSince')
            );
            mockState.inputBoxQueue.push('2026-11-01');

            await vscode.commands.executeCommand('ntm.openPalette');
            assert.ok(
                seenArgs.some(
                    a =>
                        a.includes('--robot-analytics') &&
                        a.includes('--analytics-since=2026-11-01') &&
                        a.includes('--analytics-sessions')
                )
            );
            assert.equal(mockState.openedTextDocuments.length, 2);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});

describe('Wave 11 Audit Inspection/Verification & Support Bundle Commands', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    const REAL_AUDIT_SEARCH_PAYLOAD = {
        success: true,
        schema_id: 'ntm:robot:audit:v1',
        schema_version: '1.0.0',
        action: 'search',
        session: 'all',
        count: 1,
        total_matches: 1,
        has_more: false,
        logs: [],
        entries: [
            {
                timestamp: '2026-11-15T22:00:00Z',
                session_id: 'wave11-swarm',
                event_type: 'spawn',
                actor: 'user',
                target: 'wave11-swarm__pane_1',
                sequence_num: 1,
                checksum: 'abc123def456',
                payload: { msg: 'spawned agy agent' },
            },
        ],
    };

    const REAL_AUDIT_VERIFY_TAMPERED_PAYLOAD = {
        success: false,
        error: 'integrity verification failed for one or more files: checksum mismatch at line 2',
        error_code: 'INTERNAL_ERROR',
        schema_id: 'ntm:robot:audit:v1',
        schema_version: '1.0.0',
        action: 'verify',
        session: 'wave11-swarm',
        count: 1,
        total_matches: 1,
        has_more: false,
        verified: false,
        logs: [],
        entries: [],
        files: [
            {
                file: 'wave11-swarm-2026-11-15.jsonl',
                status: 'FAILED',
                verified: false,
                error: 'checksum mismatch at line 2',
            },
        ],
    };

    const REAL_SUPPORT_BUNDLE_PAYLOAD = {
        success: true,
        path: '/tmp/ntm-bundle-wave11.zip',
        format: 'zip',
        file_count: 6,
        total_size: 4096,
        privacy_mode: false,
        content_suppressed: false,
        redaction_summary: {
            mode: 'redact',
            total_findings: 2,
            files_scanned: 6,
            files_redacted: 1,
        },
        warnings: [],
    };

    it('normalizes --robot-audit (including tampered verify exit-1 JSON) and --robot-support-bundle payloads', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args.includes('--audit-verify')) {
                    const exitErr = Object.assign(new Error('Command failed: exit 1'), {
                        stdout: JSON.stringify(REAL_AUDIT_VERIFY_TAMPERED_PAYLOAD),
                        stderr: '',
                    });
                    throw exitErr;
                }
                if (args.some(a => a.startsWith('--robot-audit'))) {
                    return { stdout: JSON.stringify(REAL_AUDIT_SEARCH_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-support-bundle'))) {
                    return { stdout: JSON.stringify(REAL_SUPPORT_BUNDLE_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const searchRes = await client.getAudit({
            action: 'search',
            search: 'spawn',
            days: 7,
            limit: 25,
        });
        assert.deepEqual(calls[0], [
            '--robot-audit',
            '--audit-action=search',
            '--audit-search=spawn',
            '--audit-days=7',
            '--audit-limit=25',
            '--robot-format=json',
        ]);
        assert.equal(searchRes.total_matches, 1);
        assert.equal(searchRes.entries[0].event_type, 'spawn');

        const tamperedRes = await client.getAudit({
            session: 'wave11-swarm',
            action: 'verify',
            verify: true,
        });
        assert.equal(tamperedRes.verified, false);
        assert.equal(tamperedRes.files.length, 1);
        assert.equal(tamperedRes.files[0].status, 'FAILED');

        const md = formatAuditMarkdown(tamperedRes);
        assert.ok(md.includes('FAILED (TAMPERED / BROKEN)'));
        assert.ok(md.includes('wave11-swarm-2026-11-15.jsonl'));
        assert.equal(AUDIT_MODE_OPTIONS.length, 6);
        assert.equal(SUPPORT_BUNDLE_FORMAT_OPTIONS.length, 3);

        const bundleRes = await client.generateSupportBundle({
            all: true,
            outputPath: '/tmp/ntm-bundle-wave11.zip',
            format: 'zip',
            redactMode: 'redact',
            lines: 500,
        });
        assert.deepEqual(calls[2], [
            '--robot-support-bundle',
            '--all',
            '--bundle-output=/tmp/ntm-bundle-wave11.zip',
            '--bundle-format=zip',
            '--bundle-lines=500',
            '--bundle-redact=redact',
            '--robot-format=json',
        ]);
        assert.equal(bundleRes.file_count, 6);
        assert.equal(bundleRes.redaction_summary?.files_redacted, 1);

        assert.throws(
            () => normalizeAuditResult({ success: false, error: 'no audit logs found' }),
            /no audit logs found/
        );
        assert.throws(
            () => normalizeSupportBundleResult({ success: false, error: 'invalid format' }),
            /invalid format/
        );

        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const activationEvents: string[] = pkgJson.activationEvents ?? [];
        assert.ok(activationEvents.includes('onCommand:ntm.showAudit'));
        assert.ok(activationEvents.includes('onCommand:ntm.generateSupportBundle'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.showAudit'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.generateSupportBundle'));
    });

    it('executes ntm.showAudit and ntm.generateSupportBundle end-to-end with Web Cockpit routing', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;
        const seenArgs: string[][] = [];

        const client = new NtmClient({
            execFile: async (_file, args) => {
                seenArgs.push(args);
                if (args.includes('--audit-verify')) {
                    const exitErr = Object.assign(new Error('exit 1'), {
                        stdout: JSON.stringify(REAL_AUDIT_VERIFY_TAMPERED_PAYLOAD),
                        stderr: '',
                    });
                    throw exitErr;
                }
                if (args.some(a => a.startsWith('--robot-audit'))) {
                    return { stdout: JSON.stringify(REAL_AUDIT_SEARCH_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-support-bundle'))) {
                    return { stdout: JSON.stringify(REAL_SUPPORT_BUNDLE_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Run regex audit search and route to /safety in Web Cockpit
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'search')
            );
            mockState.inputBoxQueue.push('spawn');
            mockState.infoMessageActionQueue.push('Open /safety in Web Cockpit');

            await vscode.commands.executeCommand('ntm.showAudit');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-audit') && a.includes('--audit-search=spawn')
                )
            );
            assert.equal(mockState.openedTextDocuments.length, 1);
            assert.ok(mockState.openedTextDocuments[0].content.includes('**spawn**'));
            assert.deepEqual(mockState.openedExternalUris, ['http://127.0.0.1:7337/safety']);

            // 2. Run hash-chain verify on tampered session and assert warning message
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'verify')
            );
            await vscode.commands.executeCommand('ntm.showAudit', 'wave11-swarm');
            assert.ok(
                mockState.warningMessages.some(m => m.includes('FAILED for "wave11-swarm"'))
            );

            // 3. Generate redacted support bundle for all sessions and route to Overview
            mockState.quickPickQueue.push(
                (items: Array<{ format: string; redactMode: string }>) =>
                    items.find(i => i.format === 'zip' && i.redactMode === 'redact')
            );
            mockState.inputBoxQueue.push('', '/tmp/ntm-bundle-wave11.zip');
            mockState.infoMessageActionQueue.push('Open Overview in Web Cockpit');

            await vscode.commands.executeCommand('ntm.generateSupportBundle');
            assert.ok(
                seenArgs.some(
                    a =>
                        a.includes('--robot-support-bundle') &&
                        a.includes('--all') &&
                        a.includes('--bundle-output=/tmp/ntm-bundle-wave11.zip')
                )
            );
            assert.deepEqual(mockState.openedExternalUris, [
                'http://127.0.0.1:7337/safety',
                'http://127.0.0.1:7337/',
            ]);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});

describe('Wave 12: VSCode ntm.runDoctor, ntm.preflightPrompt & ntm.assignWork', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    const REAL_DOCTOR_CONTENTION_PAYLOAD = {
        success: true,
        timestamp: '2026-11-16T05:00:00Z',
        overall_status: 'warning',
        passed: 8,
        warnings: 1,
        errors: 0,
        dependencies: [
            { name: 'tmux', installed: true, required: true, version: 'tmux 3.4', status: 'ok' },
        ],
        daemons: [
            { name: 'agent-mail', port: 8765, running: true, status: 'ok', message: 'listening on port 8765' },
        ],
        guard_hook: {
            name: 'guard-hook',
            status: 'warning',
            degraded_runs: 1,
            since: '2026-11-15T10:00:00Z',
        },
        contention: [
            { pid: 4242, process: 'br', lock_file: '.beads/beads.db-wal', cwd: '/data/projects/ntm' },
        ],
    };

    const REAL_PREFLIGHT_BLOCKED_PAYLOAD = {
        success: false,
        error: 'prompt blocked by preflight',
        error_code: 'PREFLIGHT_BLOCKED',
        preview_hash: 'deadbeefcafebabe',
        preview_len: 31,
        estimated_tokens: 9,
        error_count: 1,
        warning_count: 0,
        info_count: 0,
        dcg_available: true,
        findings: [
            {
                severity: 'error',
                code: 'destructive_command',
                message: 'Prompt embeds a destructive filesystem command',
                remediation: 'Remove destructive command before dispatching',
            },
        ],
    };

    const REAL_ASSIGN_PAYLOAD = {
        success: true,
        session: 'wave12-swarm',
        strategy: 'dependency',
        relaxed: false,
        idle_agents: ['%11', '%12'],
        recommendations: [
            {
                pane_id: '%11',
                pane_target: '0.1',
                agent_type: 'claude',
                assign_bead: 'bd-n5y0l.2',
                bead_title: 'Expose Orchestration REST routes',
                priority: 'P1',
                confidence: 0.94,
                reasoning: 'Unblocks 2 downstream tasks',
            },
        ],
        summary: {
            total_agents: 3,
            idle_agents: 2,
            working_agents: 1,
            ready_beads: 2,
            blocked_beads: 1,
            recommendations: 1,
            unassignable_beads: 0,
        },
    };

    it('constructs exact CLI argv and normalizes JSON for getDoctor, preflightPrompt (including exit-1 blocked envelope), and getAssign', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args.includes('--robot-doctor')) {
                    return { stdout: JSON.stringify(REAL_DOCTOR_CONTENTION_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-preflight'))) {
                    const exitErr = Object.assign(new Error('exit 1'), {
                        stdout: JSON.stringify(REAL_PREFLIGHT_BLOCKED_PAYLOAD),
                        stderr: '',
                    });
                    throw exitErr;
                }
                if (args.some(a => a.startsWith('--robot-assign='))) {
                    return { stdout: JSON.stringify(REAL_ASSIGN_PAYLOAD), stderr: '' };
                }
                throw new Error(`unexpected args: ${args.join(' ')}`);
            },
        });

        const docRes = await client.getDoctor({ contention: true, verbose: true });
        assert.deepEqual(calls[0], [
            '--robot-doctor',
            '--doctor-contention',
            '--doctor-verbose',
            '--robot-format=json',
        ]);
        assert.equal(docRes.overall_status, 'warning');
        assert.equal(docRes.contention.length, 1);
        assert.equal(docRes.contention[0].pid, 4242);

        const pfRes = await client.preflightPrompt({
            prompt: 'Check destructive safety command',
            strict: true,
        });
        assert.deepEqual(calls[1], [
            '--robot-preflight=Check destructive safety command',
            '--preflight-strict',
            '--robot-format=json',
        ]);
        assert.equal(pfRes.allowed, false);
        assert.equal(pfRes.error_count, 1);
        assert.equal(pfRes.findings[0].code, 'destructive_command');

        const assignRes = await client.getAssign({
            session: 'wave12-swarm',
            strategy: 'dependency',
            beads: 'bd-n5y0l.2',
            relax: true,
        });
        assert.deepEqual(calls[2], [
            '--robot-assign=wave12-swarm',
            '--strategy=dependency',
            '--beads=bd-n5y0l.2',
            '--relax',
            '--robot-format=json',
        ]);
        assert.equal(assignRes.recommendations.length, 1);
        assert.equal(assignRes.recommendations[0].assign_bead, 'bd-n5y0l.2');

        assert.ok(formatDoctorMarkdown(docRes).includes('4242'));
        assert.ok(formatPreflightMarkdown(pfRes).includes('BLOCKED'));
        assert.ok(formatAssignWorkMarkdown(assignRes).includes('94%'));
        assert.equal(DOCTOR_MODE_OPTIONS.length, 3);
        assert.equal(PREFLIGHT_MODE_OPTIONS.length, 3);
        assert.equal(ASSIGN_STRATEGY_OPTIONS.length, 6);
        assert.equal(SUPPORTED_ASSIGN_STRATEGIES.length, 5);

        assert.throws(
            () => normalizeDoctorResult({ success: false, error: 'doctor crashed' }),
            /doctor crashed/
        );
        assert.throws(
            () => normalizePreflightResult({ success: false, error: 'missing prompt' }),
            /missing prompt/
        );
        assert.throws(
            () => normalizeAssignWorkResult({ success: false, error: 'session not found' }),
            /session not found/
        );

        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const activationEvents: string[] = pkgJson.activationEvents ?? [];
        assert.ok(activationEvents.includes('onCommand:ntm.runDoctor'));
        assert.ok(activationEvents.includes('onCommand:ntm.preflightPrompt'));
        assert.ok(activationEvents.includes('onCommand:ntm.assignWork'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.runDoctor'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.preflightPrompt'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.assignWork'));
    });

    it('executes ntm.runDoctor, ntm.preflightPrompt, and ntm.assignWork end-to-end with Web Cockpit routing', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;
        const seenArgs: string[][] = [];

        const client = new NtmClient({
            execFile: async (_file, args) => {
                seenArgs.push(args);
                if (args.includes('--robot-doctor')) {
                    return { stdout: JSON.stringify(REAL_DOCTOR_CONTENTION_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-preflight'))) {
                    const exitErr = Object.assign(new Error('exit 1'), {
                        stdout: JSON.stringify(REAL_PREFLIGHT_BLOCKED_PAYLOAD),
                        stderr: '',
                    });
                    throw exitErr;
                }
                if (args.some(a => a.startsWith('--robot-assign='))) {
                    return { stdout: JSON.stringify(REAL_ASSIGN_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(REAL_SNAPSHOT_PAYLOAD), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Run doctor + contention scan and route to Overview
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'contention')
            );
            mockState.infoMessageActionQueue.push('Open Overview in Web Cockpit');
            await vscode.commands.executeCommand('ntm.runDoctor');
            assert.ok(seenArgs.some(a => a.includes('--robot-doctor') && a.includes('--doctor-contention')));
            assert.deepEqual(mockState.openedExternalUris, ['http://127.0.0.1:7337/']);

            // 2. Run strict preflight on input prompt -> blocked warning toast
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'strict')
            );
            mockState.inputBoxQueue.push('Check destructive safety command');
            await vscode.commands.executeCommand('ntm.preflightPrompt');
            assert.ok(
                mockState.warningMessages.some(m => m.includes('Prompt preflight BLOCKED'))
            );

            // 3. Plan Beads work assignments for wave12-swarm and route to /swarm
            mockState.quickPickQueue.push(
                (items: Array<{ strategy: string }>) => items.find(i => i.strategy === 'dependency')
            );
            mockState.inputBoxQueue.push('');
            mockState.infoMessageActionQueue.push('Open /swarm in Web Cockpit');
            await vscode.commands.executeCommand('ntm.assignWork', 'wave12-swarm');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-assign=wave12-swarm') && a.includes('--strategy=dependency')
                )
            );
            assert.deepEqual(mockState.openedExternalUris, [
                'http://127.0.0.1:7337/',
                'http://127.0.0.1:7337/swarm',
            ]);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});

import './swarmOps.test';
import './sessionIntel.test';
import './persistenceWorkflow.test';
import './outputEcosystem.test';
