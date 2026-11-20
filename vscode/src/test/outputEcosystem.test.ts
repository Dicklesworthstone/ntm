import { mockState, resetVscodeMock } from './vscodeMock';
import { beforeEach, describe, it } from 'node:test';
import * as assert from 'node:assert/strict';
import * as vscode from 'vscode';

import { NtmClient } from '../ntmClient';
import {
    formatConfigValidateMarkdown,
    formatExtractCodeBlocksMarkdown,
    formatGrepPanesMarkdown,
    formatLocalModelsMarkdown,
    formatPluginsMarkdown,
} from '../outputEcosystemSupport';

describe('Wave 16 VS Code Output, Validation, Plugins & Local Models Support', () => {
    beforeEach(() => {
        resetVscodeMock();
    });

    it('invokes --robot-grep and --robot-extract via NtmClient and formats markdown', async () => {
        const seenArgs: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                seenArgs.push([...args]);
                if (args.some(a => a.startsWith('--robot-grep='))) {
                    return {
                        stdout: JSON.stringify({
                            success: true,
                            pattern: 'panic:',
                            session: 'proj_w16',
                            matches: [
                                {
                                    session: 'proj_w16',
                                    pane: 'proj_w16__agy_1',
                                    pane_id: '%3',
                                    pane_index: 1,
                                    agent_type: 'antigravity',
                                    line: 42,
                                    content: 'panic: index out of range',
                                },
                            ],
                            matching_panes: ['proj_w16/proj_w16__agy_1'],
                            total_lines_searched: 200,
                            match_count: 1,
                            panes_searched: 2,
                        }),
                        stderr: '',
                    };
                }
                return {
                    stdout: JSON.stringify({
                        success: true,
                        session: 'proj_w16',
                        source: 'proj_w16',
                        blocks: [
                            {
                                language: 'go',
                                file_path: 'internal/app/run.go',
                                content: 'package app\nfunc Run() {}',
                                source_pane: 'proj_w16:.1',
                            },
                        ],
                        total_found: 1,
                        panes_scanned: 1,
                    }),
                    stderr: '',
                };
            },
        });

        const grepRes = await client.grepPanes({
            pattern: 'panic:',
            session: 'proj_w16',
            agentType: 'agy',
            ignoreCase: true,
            contextLines: 2,
        });
        assert.equal(grepRes.match_count, 1);
        assert.ok(seenArgs[0].includes('--robot-grep=panic:'));
        assert.ok(seenArgs[0].includes('--grep-session=proj_w16'));
        assert.ok(seenArgs[0].includes('--type=agy'));
        assert.ok(seenArgs[0].includes('--grep-ignore-case'));
        assert.ok(seenArgs[0].includes('--grep-context=2'));
        assert.match(formatGrepPanesMarkdown(grepRes), /panic: index out of range/);

        const extRes = await client.extractCodeBlocks({
            session: 'proj_w16',
            language: 'go',
            lines: 250,
        });
        assert.equal(extRes.total_found, 1);
        assert.ok(seenArgs[1].includes('--robot-extract=proj_w16'));
        assert.ok(seenArgs[1].includes('--extract-lang=go'));
        assert.ok(seenArgs[1].includes('--lines=250'));
        assert.match(formatExtractCodeBlocksMarkdown(extRes), /internal\/app\/run\.go/);
    });

    it('invokes --robot-validate, --robot-plugins, and --robot-models via NtmClient', async () => {
        const seenArgs: string[][] = [];
        const client = new NtmClient({
            execFile: async (_file, args) => {
                seenArgs.push([...args]);
                if (args.includes('--robot-validate')) {
                    return {
                        stdout: JSON.stringify({
                            success: true,
                            valid: true,
                            results: [
                                {
                                    path: '/home/dev/.config/ntm/config.toml',
                                    type: 'main',
                                    valid: true,
                                    warnings: [{ field: 'projects_base', message: 'missing dir', fixable: true }],
                                },
                            ],
                            summary: { files_checked: 1, error_count: 0, warning_count: 1, fixable_count: 1 },
                        }),
                        stderr: '',
                    };
                }
                if (args.includes('--robot-plugins')) {
                    return {
                        stdout: JSON.stringify({
                            success: true,
                            config_dir: '/home/dev/.config/ntm',
                            total_count: 1,
                            agent_plugins: [
                                { name: 'hermes', alias: 'hm', command: 'hermes', env_keys: [], has_readiness: true },
                            ],
                            command_plugins: [],
                        }),
                        stderr: '',
                    };
                }
                return {
                    stdout: JSON.stringify({
                        success: true,
                        action: 'recommend',
                        detected_vram_gb: 16,
                        vram_source: 'nvidia-smi',
                        models: [],
                        recommendations: ['mixtral:8x7b'],
                    }),
                    stderr: '',
                };
            },
        });

        const valRes = await client.validateConfig({ all: true, fix: true, projectDir: '/repo' });
        assert.equal(valRes.valid, true);
        assert.ok(seenArgs[0].includes('--validate-all'));
        assert.ok(seenArgs[0].includes('--validate-fix'));
        assert.ok(seenArgs[0].includes('--project=/repo'));
        assert.match(formatConfigValidateMarkdown(valRes), /VALID/);

        const plugRes = await client.getPlugins();
        assert.equal(plugRes.total_count, 1);
        assert.match(formatPluginsMarkdown(plugRes), /hermes/);

        const modRes = await client.getLocalModels({ action: 'recommend' });
        assert.equal(modRes.detected_vram_gb, 16);
        assert.match(formatLocalModelsMarkdown(modRes), /mixtral:8x7b/);
    });

    it('executes ntm.validateConfig and ntm.showLocalModels commands end-to-end without leaking timers', async () => {
        const { activate, deactivate } = await import('../extension');
        const subscriptions: vscode.Disposable[] = [];
        const ctx = { subscriptions } as unknown as vscode.ExtensionContext;
        const mockClient = new NtmClient({
            execFile: async () => ({
                stdout: JSON.stringify({ success: true, sessions: [], summary: { total_sessions: 0, total_agents: 0 } }),
                stderr: '',
            }),
        });
        activate(ctx, mockClient);

        try {
            mockState.quickPickQueue.push({ label: 'Safety Cockpit', mode: 'openSafetyCockpit' });
            await vscode.commands.executeCommand('ntm.validateConfig');
            assert.ok(mockState.openedExternalUris.includes('http://127.0.0.1:7337/safety'));

            mockState.quickPickQueue.push({ label: 'Agents Cockpit', mode: 'openAgentsCockpit' });
            await vscode.commands.executeCommand('ntm.showLocalModels');
            assert.ok(mockState.openedExternalUris.includes('http://127.0.0.1:7337/agents'));
        } finally {
            for (const sub of subscriptions) {
                sub.dispose();
            }
            deactivate();
        }
    });

    it('does not leak status poll setInterval when checkAvailable resolves after disposal', async () => {
        const { activate, deactivate } = await import('../extension');
        const subscriptions: vscode.Disposable[] = [];
        const ctx = { subscriptions } as unknown as vscode.ExtensionContext;
        let resolveCheck!: (val: boolean) => void;
        let getStatusCalls = 0;

        const deferredClient = new NtmClient({
            execFile: async () => ({
                stdout: JSON.stringify({ success: true, sessions: [], file_reservations: [] }),
                stderr: '',
            }),
        });
        deferredClient.checkAvailable = () => new Promise<boolean>(resolve => {
            resolveCheck = resolve;
        });
        deferredClient.getStatus = async () => {
            getStatusCalls++;
            return {
                generated_at: new Date().toISOString(),
                system: {
                    version: '1.38.0',
                    commit: 'test',
                    build_date: '2026-11-20',
                    go_version: 'go1.26.10',
                    os: 'linux',
                    arch: 'amd64',
                    tmux_available: true,
                },
                sessions: [],
                summary: { total_sessions: 0, total_agents: 0, attached_count: 0, claude_count: 0, codex_count: 0, gemini_count: 0 },
            };
        };

        activate(ctx, deferredClient);
        for (const sub of subscriptions) {
            sub.dispose();
        }
        deactivate();

        // Reset after initial activation refresh from SessionTreeView; resolving
        // checkAvailable AFTER disposal must NOT trigger updateStatus or start polling.
        getStatusCalls = 0;
        resolveCheck(true);
        await new Promise(resolve => setImmediate(resolve));
        assert.equal(getStatusCalls, 0);
    });
});
