import { mockState, resetVscodeMock } from './vscodeMock';
import { beforeEach, describe, it } from 'node:test';
import * as assert from 'node:assert/strict';
import * as vscode from 'vscode';

import {
    normalizeAdoptResult,
    normalizeContextResult,
    normalizeRedactPreviewResult,
    NtmClient,
    parseAdoptSpec,
    SUPPORTED_ADOPT_HARNESSES,
} from '../ntmClient';
import {
    activate,
    ADOPT_MODE_OPTIONS,
    CONTEXT_LINES_OPTIONS,
    formatAdoptMarkdown,
    formatContextMarkdown,
    formatRedactPreviewMarkdown,
    REDACT_MODE_OPTIONS,
} from '../extension';

const WAVE14_SNAPSHOT = {
    success: true,
    timestamp: '2026-11-18T18:00:00Z',
    sessions: [
        {
            name: 'wave14-sess',
            exists: true,
            attached: true,
            windows: 1,
            panes: 3,
            agent_count: 2,
            agents: [
                { type: 'agy', pane: '%1', state: 'working', is_active: true },
                { type: 'grok', pane: '%2', state: 'idle', is_active: false },
            ],
        },
    ],
    summary: {
        total_sessions: 1,
        total_agents: 2,
        attached_count: 1,
    },
};

const REAL_ADOPT_PAYLOAD = {
    success: true,
    session: 'wave14-sess',
    total_panes: 3,
    dry_run: true,
    auto_name: true,
    by_window: false,
    adopted_panes: [
        {
            pane_id: '%1',
            pane_index: 0,
            window_index: 0,
            agent_type: 'agy',
            old_title: 'zsh',
            new_title: 'wave14-sess__agy_1',
            ntm_index: 1,
        },
        {
            pane_id: '%2',
            pane_index: 1,
            window_index: 0,
            agent_type: 'grok',
            new_title: 'wave14-sess__grok_1',
            ntm_index: 1,
        },
    ],
    agents: { agy: 1, grok: 1 },
};

const REAL_REDACT_PAYLOAD = {
    success: true,
    source: 'text',
    input_len: 46,
    finding_count: 1,
    findings: [
        {
            category: 'OPENAI_KEY',
            redacted: '[REDACTED:OPENAI_KEY]',
            start: 22,
            end: 46,
            line: 1,
            column: 23,
        },
    ],
    output: 'Authorization: Bearer [REDACTED:OPENAI_KEY]',
};

const REAL_CONTEXT_PAYLOAD = {
    success: true,
    session: 'wave14-sess',
    agents: [
        {
            pane: '0.0',
            pane_id: '%1',
            pane_idx: 0,
            agent_type: 'agy',
            model: 'gemini-2.5-pro',
            state: 'working',
            estimated_tokens: 84000,
            context_limit: 200000,
            usage_percent: 42.0,
        },
    ],
    summary: {
        high_usage_count: 0,
        average_usage: 42.0,
    },
};

describe('Wave 14 Session Intel Support & Commands (adopt, redact, context)', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('supports all 12 adopt pane types, window.pane specs, normalizers, and markdown formatters', () => {
        assert.equal(SUPPORTED_ADOPT_HARNESSES.length, 12);
        assert.equal(ADOPT_MODE_OPTIONS.length, 4);
        assert.equal(REDACT_MODE_OPTIONS.length, 3);
        assert.equal(CONTEXT_LINES_OPTIONS.length, 3);

        const parsed = parseAdoptSpec('cc=0,1 agy=1.0,2.0 grok=3-4 user=5');
        assert.deepEqual(parsed, {
            cc: '0,1',
            agy: '1.0,2.0',
            grok: '3-4',
            user: '5',
        });
        assert.throws(() => parseAdoptSpec(''), /required/i);
        assert.throws(() => parseAdoptSpec('unknown=0'), /Unsupported adopt harness/);

        const adoptRes = normalizeAdoptResult(REAL_ADOPT_PAYLOAD);
        assert.equal(adoptRes.adopted_panes.length, 2);
        assert.ok(formatAdoptMarkdown(adoptRes).includes('wave14-sess__agy_1'));

        const redactRes = normalizeRedactPreviewResult(REAL_REDACT_PAYLOAD);
        assert.equal(redactRes.finding_count, 1);
        assert.ok(formatRedactPreviewMarkdown(redactRes).includes('[REDACTED:OPENAI_KEY]'));

        const ctxRes = normalizeContextResult(REAL_CONTEXT_PAYLOAD);
        assert.equal(ctxRes.average_usage, 42.0);
        assert.ok(formatContextMarkdown(ctxRes).includes('42.0%'));
    });

    it('invokes exact CLI flags in NtmClient.adoptSession, previewRedaction, and getContext', async () => {
        const seenArgs: string[][] = [];
        const client = new NtmClient({
            binaryPath: 'ntm',
            execFile: async (_file, args) => {
                seenArgs.push([...args]);
                if (args.some(a => a.startsWith('--robot-adopt='))) {
                    return { stdout: JSON.stringify(REAL_ADOPT_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-redact'))) {
                    return { stdout: JSON.stringify(REAL_REDACT_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-context='))) {
                    return { stdout: JSON.stringify(REAL_CONTEXT_PAYLOAD), stderr: '' };
                }
                return { stdout: '{}', stderr: '' };
            },
        });

        await client.adoptSession({
            session: 'wave14-sess',
            panes: { agy: '1.0', grok: '2.0' },
            autoName: false,
            dryRun: true,
        });
        assert.deepEqual(seenArgs[0], [
            '--robot-adopt=wave14-sess',
            '--adopt-agy=1.0',
            '--adopt-grok=2.0',
            '--adopt-auto-name=false',
            '--dry-run',
            '--robot-format=json',
        ]);

        await client.adoptSession({
            session: 'wave14-sess',
            byWindowHarness: 'cc',
        });
        assert.deepEqual(seenArgs[1], [
            '--robot-adopt=wave14-sess',
            '--adopt-by-window=cc',
            '--robot-format=json',
        ]);

        await client.previewRedaction({ text: 'Bearer secret' });
        assert.deepEqual(seenArgs[2], ['--robot-redact=Bearer secret', '--robot-format=json']);

        await client.previewRedaction({ filePath: '/tmp/notes.txt' });
        assert.deepEqual(seenArgs[3], ['--robot-redact', '--redact-file=/tmp/notes.txt', '--robot-format=json']);

        await client.getContext({ session: 'wave14-sess', lines: 1000 });
        assert.deepEqual(seenArgs[4], ['--robot-context=wave14-sess', '--lines=1000', '--robot-format=json']);
    });

    it('executes ntm.adoptSession, ntm.previewRedaction, and ntm.showContext end-to-end', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;
        const seenArgs: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file: string, args: readonly string[]) => {
                seenArgs.push([...args]);
                if (args.includes('--robot-snapshot') || args.includes('--robot-status')) {
                    return { stdout: JSON.stringify(WAVE14_SNAPSHOT), stderr: '' };
                }
                if (args.includes('--robot-mail')) {
                    return { stdout: JSON.stringify({ success: true, inboxes: {} }), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-adopt='))) {
                    return { stdout: JSON.stringify(REAL_ADOPT_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-redact'))) {
                    return { stdout: JSON.stringify(REAL_REDACT_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-context='))) {
                    return { stdout: JSON.stringify(REAL_CONTEXT_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify({ success: true }), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const ctx = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;
        activate(ctx, client);

        try {
            // 1. Adopt preview
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'dryRun')
            );
            mockState.inputBoxQueue.push('agy=0 grok=1');
            mockState.infoMessageActionQueue.push(undefined);
            await vscode.commands.executeCommand('ntm.adoptSession', 'wave14-sess');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-adopt=wave14-sess') && a.includes('--adopt-agy=0') && a.includes('--dry-run')
                )
            );

            // 2. Redaction preview -> warning message with finding count
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'selectionOrInput')
            );
            mockState.inputBoxQueue.push('Authorization: Bearer test-token');
            await vscode.commands.executeCommand('ntm.previewRedaction');
            assert.ok(
                mockState.warningMessages.some(m => m.includes('scrubbed 1 secret/PII finding'))
            );

            // 3. Context window inspection
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'lines200')
            );
            mockState.infoMessageActionQueue.push('Open /sessions in Web Cockpit');
            await vscode.commands.executeCommand('ntm.showContext', 'wave14-sess');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-context=wave14-sess') && a.includes('--lines=200')
                )
            );
            assert.ok(
                mockState.openedExternalUris.includes('http://127.0.0.1:7337/sessions?session=wave14-sess')
            );
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});
