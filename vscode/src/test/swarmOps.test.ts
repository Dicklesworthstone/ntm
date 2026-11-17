import { mockState, resetVscodeMock } from './vscodeMock';
import { beforeEach, describe, it } from 'node:test';
import * as assert from 'node:assert/strict';
import * as fs from 'fs';
import * as path from 'path';
import * as vscode from 'vscode';

import {
    normalizeRebalanceResult,
    normalizeReviewQueueResult,
    normalizeScaleResult,
    normalizeSummaryResult,
    NtmClient,
    parseScaleSpec,
    SUPPORTED_SCALE_HARNESSES,
} from '../ntmClient';
import {
    activate,
    formatRebalanceMarkdown,
    formatReviewQueueMarkdown,
    formatScaleMarkdown,
    formatSummaryMarkdown,
    PALETTE_ACTION_OPTIONS,
    REBALANCE_MODE_OPTIONS,
    REVIEW_QUEUE_MODE_OPTIONS,
    SCALE_MODE_OPTIONS,
    SUMMARY_WINDOW_OPTIONS,
} from '../extension';

const WAVE13_SNAPSHOT = {
    success: true,
    timestamp: '2026-11-16T09:50:00Z',
    sessions: [
        {
            name: 'wave13-swarm',
            exists: true,
            attached: true,
            windows: 1,
            panes: 3,
            agent_count: 3,
            agents: [
                { type: 'claude', pane: '%1', state: 'working', is_active: true },
                { type: 'codex', pane: '%2', state: 'idle', is_active: false },
                { type: 'cursor', pane: '%3', state: 'idle', is_active: false },
            ],
        },
    ],
    summary: {
        total_sessions: 1,
        total_agents: 3,
        attached_count: 1,
    },
};

const REAL_SCALE_PAYLOAD = {
    success: true,
    timestamp: '2026-11-16T09:51:00Z',
    session: 'wave13-swarm',
    dry_run: true,
    before: { cc: 1, cursor: 0, ollama: 0 },
    after: { cc: 3, cursor: 1, ollama: 1 },
    actions: [
        { type: 'spawn', agent_type: 'cc', count: 2, agents: [] },
        { type: 'spawn', agent_type: 'cursor', count: 1, agents: [] },
        { type: 'spawn', agent_type: 'ollama', count: 1, agents: [] },
    ],
    errors: [],
};

const REAL_REBALANCE_PAYLOAD = {
    success: true,
    timestamp: '2026-11-16T09:52:00Z',
    session: 'wave13-swarm',
    imbalance_score: 0.85,
    recommendation: 'rebalance_recommended',
    dry_run: true,
    applied: false,
    before: { '%1': 3, '%2': 0 },
    after: { '%1': 2, '%2': 1 },
    transfers: [
        {
            bead_id: 'bd-y5y8h.2',
            bead_title: 'Expose Swarm Ops REST + OpenAPI 3.1',
            from_pane: 1,
            from_agent: 'claude',
            to_pane: 2,
            to_agent: 'codex',
            reason: 'target_idle',
        },
    ],
};

const REAL_REVIEW_QUEUE_PAYLOAD = {
    success: true,
    timestamp: '2026-11-16T09:53:00Z',
    session: 'wave13-swarm',
    dispatched: true,
    idle_agents: [{ pane: 2, agent_type: 'codex' }],
    suggestions: [
        {
            agent: 'codex_2',
            pane: 2,
            agent_type: 'codex',
            prompt: 'Review the changes for bead bd-y5y8h.1: "Wire 11-harness scale"',
            source: 'agent_work',
            source_ref: 'bd-y5y8h.1',
        },
    ],
};

const REAL_SUMMARY_PAYLOAD = {
    success: true,
    timestamp: '2026-11-16T09:54:00Z',
    summary: {
        session: 'wave13-swarm',
        total_files: 3,
        total_output_lines: 184,
        agents: [
            {
                pane_id: '%1',
                pane_title: 'wave13-swarm__cc_1',
                agent_type: 'claude',
                state: 'idle',
                output_lines: 184,
                commands_run: 5,
                files_modified: ['internal/serve/server_swarm_ops.go', 'docs/openapi.json'],
                errors: 0,
                key_actions: ['implemented server_swarm_ops.go'],
            },
        ],
        conflicts: [
            {
                path: 'docs/openapi.json',
                agents: ['claude', 'codex'],
            },
        ],
    },
};

describe('Wave 13: VSCode ntm.scaleSession, ntm.rebalanceSession, ntm.reviewQueue & ntm.showSummary', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('constructs exact CLI argv across all 11 harnesses and normalizes scale, rebalance, review-queue, and summary JSON', async () => {
        const calls: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                calls.push(args);
                if (args.some(a => a.startsWith('--robot-scale='))) {
                    return { stdout: JSON.stringify(REAL_SCALE_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-rebalance='))) {
                    return { stdout: JSON.stringify(REAL_REBALANCE_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-review-queue='))) {
                    return { stdout: JSON.stringify(REAL_REVIEW_QUEUE_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-summary='))) {
                    return { stdout: JSON.stringify(REAL_SUMMARY_PAYLOAD), stderr: '' };
                }
                throw new Error(`unexpected args: ${args.join(' ')}`);
            },
        });

        assert.equal(SUPPORTED_SCALE_HARNESSES.length, 11);
        const parsed = parseScaleSpec('cc=3, cursor=1 ollama=1 --scale-windsurf=2');
        assert.deepEqual(parsed, { cc: 3, cursor: 1, ollama: 1, windsurf: 2 });

        const scaleRes = await client.scaleSession({
            session: 'wave13-swarm',
            targets: { cc: 3, cursor: 1, ollama: 1 },
            dryRun: true,
        });
        assert.deepEqual(calls[0], [
            '--robot-scale=wave13-swarm',
            '--scale-cc=3',
            '--scale-ollama=1',
            '--scale-cursor=1',
            '--dry-run',
            '--robot-format=json',
        ]);
        assert.equal(scaleRes.actions.length, 3);
        assert.equal(scaleRes.dry_run, true);

        const rebRes = await client.rebalanceSession({
            session: 'wave13-swarm',
            apply: true,
            threshold: 0.75,
            geometry: true,
            geometryTolerance: 0.2,
            filterType: 'claude',
        });
        assert.deepEqual(calls[1], [
            '--robot-rebalance=wave13-swarm',
            '--rebalance-apply',
            '--rebalance-threshold=0.75',
            '--rebalance-geometry',
            '--rebalance-geometry-tolerance=0.2',
            '--type=claude',
            '--robot-format=json',
        ]);
        assert.equal(rebRes.imbalance_score, 0.85);
        assert.equal(rebRes.transfers[0].bead_id, 'bd-y5y8h.2');

        const rqRes = await client.getReviewQueue({
            session: 'wave13-swarm',
            dispatch: true,
            pane: 2,
            projectDir: '/data/projects/ntm',
        });
        assert.deepEqual(calls[2], [
            '--robot-review-queue=wave13-swarm',
            '--review-dispatch',
            '--review-pane=2',
            '--review-project-dir=/data/projects/ntm',
            '--robot-format=json',
        ]);
        assert.equal(rqRes.dispatched, true);
        assert.equal(rqRes.idle_count, 1);
        assert.equal(rqRes.suggestions[0].agent, 'codex_2');

        const sumRes = await client.getSummary({
            session: 'wave13-swarm',
            since: '30m',
        });
        assert.deepEqual(calls[3], [
            '--robot-summary=wave13-swarm',
            '--since=30m',
            '--robot-format=json',
        ]);
        assert.equal(sumRes.total_files, 3);
        assert.equal(sumRes.agents[0].commands_run, 5);
        assert.equal(sumRes.conflicts.length, 1);

        assert.ok(formatScaleMarkdown(scaleRes).includes('`cursor`'));
        assert.ok(formatRebalanceMarkdown(rebRes).includes('`bd-y5y8h.2`'));
        assert.ok(formatReviewQueueMarkdown(rqRes).includes('codex_2'));
        assert.ok(formatSummaryMarkdown(sumRes).includes('docs/openapi.json'));

        assert.equal(SCALE_MODE_OPTIONS.length, 3);
        assert.equal(REBALANCE_MODE_OPTIONS.length, 4);
        assert.equal(REVIEW_QUEUE_MODE_OPTIONS.length, 3);
        assert.equal(SUMMARY_WINDOW_OPTIONS.length, 3);

        assert.throws(() => parseScaleSpec('unknown=2'), /Unsupported scale harness/);
        assert.throws(() => normalizeScaleResult({ success: false, error: 'scale failed' }), /scale failed/);
        assert.throws(() => normalizeRebalanceResult({ success: false, error: 'rebalance failed' }), /rebalance failed/);
        assert.throws(() => normalizeReviewQueueResult({ success: false, error: 'review failed' }), /review failed/);
        assert.throws(() => normalizeSummaryResult({ success: false, error: 'summary failed' }), /summary failed/);

        const pkgPath = path.resolve(__dirname, '../../package.json');
        const pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
        const activationEvents: string[] = pkgJson.activationEvents ?? [];
        assert.ok(activationEvents.includes('onCommand:ntm.scaleSession'));
        assert.ok(activationEvents.includes('onCommand:ntm.rebalanceSession'));
        assert.ok(activationEvents.includes('onCommand:ntm.reviewQueue'));
        assert.ok(activationEvents.includes('onCommand:ntm.showSummary'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.scaleSession'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.rebalanceSession'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.reviewQueue'));
        assert.ok(PALETTE_ACTION_OPTIONS.some(p => p.command === 'ntm.showSummary'));
    });

    it('executes ntm.scaleSession, ntm.rebalanceSession, ntm.reviewQueue, and ntm.showSummary end-to-end with Web Cockpit routing', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;
        const seenArgs: string[][] = [];

        const client = new NtmClient({
            execFile: async (_file, args) => {
                seenArgs.push(args);
                if (args.some(a => a.startsWith('--robot-scale='))) {
                    return { stdout: JSON.stringify(REAL_SCALE_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-rebalance='))) {
                    return { stdout: JSON.stringify(REAL_REBALANCE_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-review-queue='))) {
                    return { stdout: JSON.stringify(REAL_REVIEW_QUEUE_PAYLOAD), stderr: '' };
                }
                if (args.some(a => a.startsWith('--robot-summary='))) {
                    return { stdout: JSON.stringify(REAL_SUMMARY_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify(WAVE13_SNAPSHOT), stderr: '' };
            },
        });

        const subscriptions: Array<{ dispose(): void }> = [];
        const context = {
            subscriptions,
            extensionUri: vscode.Uri.file('/ext'),
        } as unknown as vscode.ExtensionContext;

        activate(context, client);
        try {
            // 1. Dry-run scale across 11-harness spec
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'dryRun')
            );
            mockState.inputBoxQueue.push('cc=3,cursor=1,ollama=1');
            mockState.infoMessageActionQueue.push('Open /swarm in Web Cockpit');
            await vscode.commands.executeCommand('ntm.scaleSession', 'wave13-swarm');
            assert.ok(
                seenArgs.some(
                    a =>
                        a.includes('--robot-scale=wave13-swarm') &&
                        a.includes('--scale-cursor=1') &&
                        a.includes('--scale-ollama=1') &&
                        a.includes('--dry-run')
                )
            );

            // 2. Analyze workload rebalance
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'analyze')
            );
            await vscode.commands.executeCommand('ntm.rebalanceSession', 'wave13-swarm');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-rebalance=wave13-swarm') && a.includes('--dry-run')
                )
            );

            // 3. Dispatch peer review queue
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'dispatch')
            );
            await vscode.commands.executeCommand('ntm.reviewQueue', 'wave13-swarm');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-review-queue=wave13-swarm') && a.includes('--review-dispatch')
                )
            );

            // 4. Show session activity summary -> Open /sessions in Web Cockpit
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string; since?: string }>) =>
                    items.find(i => i.mode === 'since' && i.since === '30m')
            );
            mockState.infoMessageActionQueue.push('Open /sessions in Web Cockpit');
            await vscode.commands.executeCommand('ntm.showSummary', 'wave13-swarm');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-summary=wave13-swarm') && a.includes('--since=30m')
                )
            );
            assert.deepEqual(mockState.openedExternalUris, [
                'http://127.0.0.1:7337/swarm',
                'http://127.0.0.1:7337/sessions?session=wave13-swarm',
            ]);
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});
