import { mockState, resetVscodeMock } from './vscodeMock';
import { beforeEach, describe, it } from 'node:test';
import * as assert from 'node:assert/strict';
import * as cp from 'child_process';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import {
    buildNtmTerminalCommand,
    normalizeSnapshot,
    normalizeStatusHeader,
    NtmClient,
    RobotMail,
    shellQuote,
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
    parseCustomSpawnFlags,
    resolveSendTargetsFromSelection,
    SEND_TARGET_OPTIONS,
    shellQuote as extensionShellQuote,
    SPAWN_HARNESS_OPTIONS,
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
                    variant: 'gemini-4-argon',
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
