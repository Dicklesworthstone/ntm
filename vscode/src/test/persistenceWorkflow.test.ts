import { mockState, resetVscodeMock } from './vscodeMock';
import { beforeEach, describe, it } from 'node:test';
import * as assert from 'node:assert/strict';
import * as vscode from 'vscode';

import {
    normalizeAgentMappingResult,
    normalizeSavedSessionsResult,
    normalizeSessionTemplatesResult,
    normalizeWorkflowsResult,
    NtmClient,
} from '../ntmClient';
import {
    activate,
    formatAgentMappingMarkdown,
    formatSavedSessionsMarkdown,
    formatSessionTemplatesMarkdown,
    formatWorkflowsMarkdown,
    SAVED_SESSIONS_MODE_OPTIONS,
    WORKFLOWS_MODE_OPTIONS,
} from '../extension';

const WAVE15_STATUS = {
    success: true,
    timestamp: '2026-11-18T19:30:00Z',
    sessions: [
        {
            name: 'wave15-sess',
            exists: true,
            attached: true,
            windows: 1,
            panes: 3,
            agent_count: 2,
            agents: [
                { type: 'agy', pane: '%1', state: 'working', is_active: true },
                { type: 'cc', pane: '%2', state: 'idle', is_active: false },
            ],
        },
    ],
    summary: {
        total_sessions: 1,
        total_agents: 2,
        attached_count: 1,
    },
};

const REAL_SAVED_SHOW_PAYLOAD = {
    success: true,
    action: 'show',
    saved_as: 'wave15-snap',
    resumable_panes: 1,
    state: {
        name: 'wave15-sess',
        agents: { cc: 2, agy: 1 },
        panes: [
            {
                title: 'wave15-sess__agy_1',
                index: 0,
                agent_type: 'agy',
                session_id: 'agy-uuid-77',
                session_provider: 'antigravity',
                session_freshness: 'fresh',
            },
        ],
    },
};

const REAL_MAPPING_PAYLOAD = {
    success: true,
    session: 'wave15-sess',
    count: 1,
    entries: [
        {
            name: 'SilverWolf',
            session: 'wave15-sess',
            pane_index: 1,
            pane_id: '%2',
            agent_type: 'agy',
            identity: {
                label: '[SilverWolf]',
                observation_state: 'matched',
            },
        },
    ],
    discrepancies: [],
    badges: {
        enabled: true,
        published: 1,
    },
};

const REAL_WORKFLOW_PAYLOAD = {
    success: true,
    action: 'show',
    workflow: {
        name: 'red-green',
        description: 'Ping-pong TDD workflow',
        source: 'builtin',
        coordination: 'ping-pong',
        agent_count: 2,
        roles: ['red', 'green'],
        validated_triggers: [{ from: 'red', to: 'green', runtime_type: 'file', valid: true }],
    },
};

const REAL_SESSION_TEMPLATES_PAYLOAD = {
    success: true,
    action: 'list',
    total: 1,
    templates: [
        {
            name: 'refactor',
            description: 'Large refactor squad',
            source: 'builtin',
            agent_count: 3,
        },
    ],
};

describe('Wave 15 VS Code Persistence, Mapping & Workflows Support', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('normalizes and formats saved-sessions, mapping, workflows, and session-templates outputs', () => {
        const saved = normalizeSavedSessionsResult(REAL_SAVED_SHOW_PAYLOAD);
        assert.equal(saved.resumable_panes, 1);
        const savedMd = formatSavedSessionsMarkdown(saved);
        assert.match(savedMd, /antigravity:agy-uuid-77/);
        assert.match(savedMd, /agy=1/);

        const mapped = normalizeAgentMappingResult(REAL_MAPPING_PAYLOAD);
        assert.equal(mapped.count, 1);
        assert.equal(mapped.badges_published, 1);
        const mappedMd = formatAgentMappingMarkdown(mapped);
        assert.match(mappedMd, /SilverWolf/);
        assert.match(mappedMd, /\[SilverWolf\]/);

        const wf = normalizeWorkflowsResult(REAL_WORKFLOW_PAYLOAD);
        assert.equal(wf.workflow?.validated_trigger_count, 1);
        const wfMd = formatWorkflowsMarkdown(wf);
        assert.match(wfMd, /red-green/);

        const tmpls = normalizeSessionTemplatesResult(REAL_SESSION_TEMPLATES_PAYLOAD);
        assert.equal(tmpls.total, 1);
        const tmplsMd = formatSessionTemplatesMarkdown(tmpls);
        assert.match(tmplsMd, /refactor/);

        assert.equal(SAVED_SESSIONS_MODE_OPTIONS.length, 5);
        assert.equal(WORKFLOWS_MODE_OPTIONS.length, 4);
    });

    it('executes ntm.savedSessions, ntm.showAgentMapping, and ntm.showWorkflows end-to-end', async () => {
        mockState.binaryPath = '/opt/ntm/bin/ntm';
        mockState.webPort = 7337;
        const seenArgs: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file: string, args: readonly string[]) => {
                const argv = [...args];
                seenArgs.push(argv);
                if (argv.includes('--robot-status') || argv.includes('--robot-snapshot')) {
                    return { stdout: JSON.stringify(WAVE15_STATUS), stderr: '' };
                }
                if (argv.includes('--robot-saved-sessions')) {
                    return { stdout: JSON.stringify(REAL_SAVED_SHOW_PAYLOAD), stderr: '' };
                }
                if (argv.some(a => a.startsWith('--robot-mapping='))) {
                    return { stdout: JSON.stringify(REAL_MAPPING_PAYLOAD), stderr: '' };
                }
                if (argv.includes('--robot-workflows')) {
                    return { stdout: JSON.stringify(REAL_WORKFLOW_PAYLOAD), stderr: '' };
                }
                if (argv.includes('--robot-session-templates')) {
                    return { stdout: JSON.stringify(REAL_SESSION_TEMPLATES_PAYLOAD), stderr: '' };
                }
                return { stdout: JSON.stringify({ success: true }), stderr: '' };
            },
        });
        const subscriptions: vscode.Disposable[] = [];
        const ctx = { subscriptions } as unknown as vscode.ExtensionContext;
        await activate(ctx, client);

        try {
            // 1. ntm.savedSessions -> inspect snapshot 'wave15-snap'
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'showSnapshot')
            );
            mockState.inputBoxQueue.push('wave15-snap');
            await vscode.commands.executeCommand('ntm.savedSessions');
            assert.ok(
                seenArgs.some(
                    a =>
                        a.includes('--robot-saved-sessions') &&
                        a.includes('--saved-action=show') &&
                        a.includes('--saved-name=wave15-snap')
                )
            );

            // 2. ntm.showAgentMapping -> reconciles 'wave15-sess' and routes to /agents
            mockState.infoMessageActionQueue.push('Open /agents in Web Cockpit');
            await vscode.commands.executeCommand('ntm.showAgentMapping', 'wave15-sess');
            assert.ok(seenArgs.some(a => a.includes('--robot-mapping=wave15-sess')));
            assert.ok(mockState.openedExternalUris.includes('http://127.0.0.1:7337/agents'));

            // 3. ntm.showWorkflows -> inspect 'red-green'
            mockState.quickPickQueue.push(
                (items: Array<{ mode: string }>) => items.find(i => i.mode === 'inspectWorkflow')
            );
            mockState.inputBoxQueue.push('red-green');
            await vscode.commands.executeCommand('ntm.showWorkflows');
            assert.ok(
                seenArgs.some(
                    a => a.includes('--robot-workflows') && a.includes('--workflows-name=red-green')
                )
            );
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
        }
    });
});
