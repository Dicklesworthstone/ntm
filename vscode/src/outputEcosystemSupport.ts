import * as vscode from 'vscode';

export interface GrepPanesOptions {
    pattern: string;
    session?: string;
    allSessions?: boolean;
    agentType?: string;
    ignoreCase?: boolean;
    contextLines?: number;
    lines?: number;
}

export interface GrepMatchEntry {
    session: string;
    pane: string;
    pane_id: string;
    pane_index: number;
    agent_type?: string;
    line: number;
    content: string;
    context?: string[];
}

export interface GrepPanesResult {
    success: boolean;
    pattern: string;
    session: string;
    matches: GrepMatchEntry[];
    matching_panes: string[];
    total_lines_searched: number;
    match_count: number;
    panes_searched: number;
    warnings?: string[];
}

export interface ExtractCodeBlocksOptions {
    session: string;
    pane?: string;
    language?: string;
    lastPane?: boolean;
    lines?: number;
}

export interface ExtractedCodeBlockEntry {
    language: string;
    content: string;
    file_path?: string;
    is_new?: boolean;
    source_pane?: string;
}

export interface ExtractCodeBlocksResult {
    success: boolean;
    session: string;
    source: string;
    language?: string;
    blocks: ExtractedCodeBlockEntry[];
    total_found: number;
    panes_scanned: number;
}

export interface ConfigValidateOptions {
    all?: boolean;
    fix?: boolean;
    projectDir?: string;
}

export interface ConfigValidationIssue {
    field?: string;
    message: string;
    fixable?: boolean;
}

export interface ConfigValidationFile {
    path: string;
    type: string;
    valid: boolean;
    errors: ConfigValidationIssue[];
    warnings: ConfigValidationIssue[];
    info: string[];
}

export interface ConfigValidateResult {
    success: boolean;
    valid: boolean;
    results: ConfigValidationFile[];
    summary: {
        files_checked: number;
        error_count: number;
        warning_count: number;
        fixable_count: number;
    };
}

export interface PluginsListResult {
    success: boolean;
    config_dir: string;
    total_count: number;
    agent_plugins: Array<{
        name: string;
        alias?: string;
        command: string;
        description?: string;
        env_keys: string[];
        has_readiness: boolean;
    }>;
    command_plugins: Array<{
        name: string;
        path: string;
        usage?: string;
        description?: string;
    }>;
}

export interface LocalModelsOptions {
    action?: 'recommend' | 'list';
    host?: string;
}

export interface LocalModelsResult {
    success: boolean;
    action: string;
    host?: string;
    error?: string;
    hint?: string;
    model_count: number;
    total_size_bytes: number;
    disk_low: boolean;
    models: Array<{
        name: string;
        size_bytes: number;
        parameters?: string;
    }>;
    detected_vram_gb: number;
    vram_source?: string;
    recommendations: string[];
}

export interface ConfigValidateQuickPickOption extends vscode.QuickPickItem {
    mode: 'validateActive' | 'validateAll' | 'validateAndFix' | 'openSafetyCockpit';
}

export const CONFIG_VALIDATE_MODE_OPTIONS: readonly ConfigValidateQuickPickOption[] = [
    {
        label: '$(check) Validate Active Configs & Policy',
        description: '--robot-validate',
        detail: 'Validate main config.toml, .ntm/* manifests, and 12-harness executables',
        mode: 'validateActive',
    },
    {
        label: '$(checklist) Validate All Config Locations',
        description: '--robot-validate --validate-all',
        detail: 'Include absent optional user and project configuration files',
        mode: 'validateAll',
    },
    {
        label: '$(wrench) Validate & Auto-Fix Missing Directories',
        description: '--robot-validate --validate-fix',
        detail: 'Create missing fixable directories such as projects_base and templates.dir',
        mode: 'validateAndFix',
    },
    {
        label: '$(globe) Open /safety in Web Cockpit',
        description: 'Web Cockpit (/safety)',
        detail: 'Open interactive Config & Policy Validator in browser',
        mode: 'openSafetyCockpit',
    },
];

export interface LocalModelsQuickPickOption extends vscode.QuickPickItem {
    mode: 'recommend' | 'listModels' | 'listPlugins' | 'openAgentsCockpit';
}

export const LOCAL_MODELS_MODE_OPTIONS: readonly LocalModelsQuickPickOption[] = [
    {
        label: '$(sparkle) Recommend Local Models (GPU VRAM Advisor)',
        description: '--robot-models --models-action=recommend',
        detail: 'Detect GPU VRAM and suggest matching local coding models',
        mode: 'recommend',
    },
    {
        label: '$(database) List Installed Ollama Models & Disk Usage',
        description: '--robot-models --models-action=list',
        detail: 'Query the local Ollama daemon for installed models and LRU cleanup hints',
        mode: 'listModels',
    },
    {
        label: '$(extensions) List Installed NTM Agent & Command Plugins',
        description: '--robot-plugins',
        detail: 'Inspect custom TOML agent plugins and executable command plugins',
        mode: 'listPlugins',
    },
    {
        label: '$(globe) Open /agents in Web Cockpit',
        description: 'Web Cockpit (/agents)',
        detail: 'Open interactive Plugins & Local Models panel in browser',
        mode: 'openAgentsCockpit',
    },
];

function asRecord(raw: unknown): Record<string, unknown> {
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
        return raw as Record<string, unknown>;
    }
    return {};
}

export function normalizeGrepPanesResult(raw: unknown): GrepPanesResult {
    const obj = asRecord(raw);
    const rawMatches = Array.isArray(obj.matches) ? obj.matches : [];
    const matches: GrepMatchEntry[] = rawMatches.map(item => {
        const m = asRecord(item);
        return {
            session: String(m.session ?? ''),
            pane: String(m.pane ?? ''),
            pane_id: String(m.pane_id ?? ''),
            pane_index: Number(m.pane_index ?? 0),
            agent_type: m.agent_type ? String(m.agent_type) : undefined,
            line: Number(m.line ?? 0),
            content: String(m.content ?? ''),
            context: Array.isArray(m.context) ? m.context.map(String) : undefined,
        };
    });
    const matchingPanes = Array.isArray(obj.matching_panes)
        ? obj.matching_panes.map(String)
        : [];
    const warnings = Array.isArray(obj.warnings)
        ? obj.warnings.map(String).filter(w => w.length > 0)
        : undefined;
    return {
        success: Boolean(obj.success ?? true),
        pattern: String(obj.pattern ?? ''),
        session: String(obj.session ?? ''),
        matches,
        matching_panes: matchingPanes,
        total_lines_searched: Number(obj.total_lines_searched ?? 0),
        match_count: Number(obj.match_count ?? matches.length),
        panes_searched: Number(obj.panes_searched ?? 0),
        warnings: warnings && warnings.length > 0 ? warnings : undefined,
    };
}

export function normalizeExtractCodeBlocksResult(raw: unknown): ExtractCodeBlocksResult {
    const obj = asRecord(raw);
    const rawBlocks = Array.isArray(obj.blocks) ? obj.blocks : [];
    const blocks: ExtractedCodeBlockEntry[] = rawBlocks.map(item => {
        const b = asRecord(item);
        return {
            language: String(b.language ?? ''),
            content: String(b.content ?? ''),
            file_path: b.file_path ? String(b.file_path) : undefined,
            is_new: Boolean(b.is_new),
            source_pane: b.source_pane ? String(b.source_pane) : undefined,
        };
    });
    return {
        success: Boolean(obj.success ?? true),
        session: String(obj.session ?? ''),
        source: String(obj.source ?? ''),
        language: obj.language ? String(obj.language) : undefined,
        blocks,
        total_found: Number(obj.total_found ?? blocks.length),
        panes_scanned: Number(obj.panes_scanned ?? 0),
    };
}

export function normalizeConfigValidateResult(raw: unknown): ConfigValidateResult {
    const obj = asRecord(raw);
    const summaryObj = asRecord(obj.summary);
    const rawResults = Array.isArray(obj.results) ? obj.results : [];
    const results: ConfigValidationFile[] = rawResults.map(item => {
        const r = asRecord(item);
        const mapIssues = (arr: unknown): ConfigValidationIssue[] =>
            Array.isArray(arr)
                ? arr.map(entry => {
                      const e = asRecord(entry);
                      return {
                          field: e.field ? String(e.field) : undefined,
                          message: String(e.message ?? ''),
                          fixable: Boolean(e.fixable),
                      };
                  })
                : [];
        return {
            path: String(r.path ?? ''),
            type: String(r.type ?? ''),
            valid: Boolean(r.valid ?? true),
            errors: mapIssues(r.errors),
            warnings: mapIssues(r.warnings),
            info: Array.isArray(r.info) ? r.info.map(String) : [],
        };
    });
    return {
        success: Boolean(obj.success ?? true),
        valid: Boolean(obj.valid ?? true),
        results,
        summary: {
            files_checked: Number(summaryObj.files_checked ?? results.length),
            error_count: Number(summaryObj.error_count ?? 0),
            warning_count: Number(summaryObj.warning_count ?? 0),
            fixable_count: Number(summaryObj.fixable_count ?? 0),
        },
    };
}

export function normalizePluginsResult(raw: unknown): PluginsListResult {
    const obj = asRecord(raw);
    const agentPlugins = Array.isArray(obj.agent_plugins)
        ? obj.agent_plugins.map(item => {
              const p = asRecord(item);
              return {
                  name: String(p.name ?? ''),
                  alias: p.alias ? String(p.alias) : undefined,
                  command: String(p.command ?? ''),
                  description: p.description ? String(p.description) : undefined,
                  env_keys: Array.isArray(p.env_keys) ? p.env_keys.map(String) : [],
                  has_readiness: Boolean(p.has_readiness),
              };
          })
        : [];
    const commandPlugins = Array.isArray(obj.command_plugins)
        ? obj.command_plugins.map(item => {
              const p = asRecord(item);
              return {
                  name: String(p.name ?? ''),
                  path: String(p.path ?? ''),
                  usage: p.usage ? String(p.usage) : undefined,
                  description: p.description ? String(p.description) : undefined,
              };
          })
        : [];
    return {
        success: Boolean(obj.success ?? true),
        config_dir: String(obj.config_dir ?? ''),
        total_count: Number(obj.total_count ?? agentPlugins.length + commandPlugins.length),
        agent_plugins: agentPlugins,
        command_plugins: commandPlugins,
    };
}

export function normalizeLocalModelsResult(raw: unknown): LocalModelsResult {
    const obj = asRecord(raw);
    const models = Array.isArray(obj.models)
        ? obj.models.map(item => {
              const m = asRecord(item);
              return {
                  name: String(m.name ?? ''),
                  size_bytes: Number(m.size_bytes ?? 0),
                  parameters: m.parameters ? String(m.parameters) : undefined,
              };
          })
        : [];
    return {
        success: Boolean(obj.success ?? true),
        action: String(obj.action ?? 'recommend'),
        host: obj.host ? String(obj.host) : undefined,
        error: obj.error ? String(obj.error) : undefined,
        hint: obj.hint ? String(obj.hint) : undefined,
        model_count: Number(obj.model_count ?? models.length),
        total_size_bytes: Number(obj.total_size_bytes ?? 0),
        disk_low: Boolean(obj.disk_low),
        models,
        detected_vram_gb: Number(obj.detected_vram_gb ?? 0),
        vram_source: obj.vram_source ? String(obj.vram_source) : undefined,
        recommendations: Array.isArray(obj.recommendations)
            ? obj.recommendations.map(String)
            : [],
    };
}

export function formatGrepPanesMarkdown(res: GrepPanesResult): string {
    const lines: string[] = [
        `# NTM Pane Scrollback Grep (\`${res.pattern}\`)`,
        '',
        `- **Session:** ${res.session}`,
        `- **Matches:** ${res.match_count} across ${res.panes_searched} pane(s) (${res.total_lines_searched} lines scanned)`,
    ];
    if (res.warnings && res.warnings.length > 0) {
        for (const warning of res.warnings) {
            lines.push(`- **Warning:** ${warning}`);
        }
    }
    lines.push('');
    if (res.matches.length === 0) {
        lines.push('No matching lines found.');
        return lines.join('\n') + '\n';
    }
    for (const m of res.matches) {
        lines.push(
            `### ${m.session}/${m.pane}:${m.line}${m.agent_type ? ` (\`${m.agent_type}\`)` : ''}`,
            '```text',
            m.content,
            '```',
            ''
        );
    }
    return lines.join('\n');
}

export function formatExtractCodeBlocksMarkdown(res: ExtractCodeBlocksResult): string {
    const lines: string[] = [
        `# NTM Extracted Code Blocks (${res.source})`,
        '',
        `- **Blocks Found:** ${res.total_found} (${res.panes_scanned} pane(s) scanned)`,
        '',
    ];
    if (res.blocks.length === 0) {
        lines.push('No fenced code blocks found in captured scrollback.');
        return lines.join('\n') + '\n';
    }
    res.blocks.forEach((b, idx) => {
        const lang = b.language || 'text';
        lines.push(
            `## [${idx + 1}] ${lang}${b.file_path ? ` — \`${b.file_path}\`` : ''}${b.source_pane ? ` (${b.source_pane})` : ''}`,
            `\`\`\`${lang}`,
            b.content,
            '```',
            ''
        );
    });
    return lines.join('\n');
}

export function formatConfigValidateMarkdown(res: ConfigValidateResult): string {
    const lines: string[] = [
        `# NTM Configuration & Policy Validation (${res.valid ? 'VALID' : 'ERRORS DETECTED'})`,
        '',
        `- **Files Checked:** ${res.summary.files_checked}`,
        `- **Errors:** ${res.summary.error_count} · **Warnings:** ${res.summary.warning_count} · **Fixable:** ${res.summary.fixable_count}`,
        '',
    ];
    for (const r of res.results) {
        lines.push(`## \`${r.path}\` (${r.type}) — ${r.valid ? 'OK' : 'INVALID'}`);
        for (const err of r.errors) {
            lines.push(`- **Error**${err.field ? ` (\`${err.field}\`)` : ''}: ${err.message}`);
        }
        for (const warn of r.warnings) {
            lines.push(
                `- **Warning**${warn.field ? ` (\`${warn.field}\`)` : ''}: ${warn.message}${warn.fixable ? ' *(fixable)*' : ''}`
            );
        }
        for (const info of r.info) {
            lines.push(`- *Info*: ${info}`);
        }
        lines.push('');
    }
    return lines.join('\n');
}

export function formatPluginsMarkdown(res: PluginsListResult): string {
    const lines: string[] = [
        `# NTM Installed Plugins (${res.total_count})`,
        '',
        `- **Config Directory:** \`${res.config_dir}\``,
        '',
    ];
    if (res.total_count === 0) {
        lines.push('No custom agent or command plugins installed.');
        return lines.join('\n') + '\n';
    }
    if (res.agent_plugins.length > 0) {
        lines.push('## Agent Plugins', '');
        for (const ap of res.agent_plugins) {
            lines.push(
                `- **${ap.name}**${ap.alias ? ` (\`${ap.alias}\`)` : ''} — \`${ap.command}\`${ap.description ? `: ${ap.description}` : ''}`
            );
        }
        lines.push('');
    }
    if (res.command_plugins.length > 0) {
        lines.push('## Command Plugins', '');
        for (const cp of res.command_plugins) {
            lines.push(`- **ntm ${cp.name}** — \`${cp.path}\`${cp.description ? `: ${cp.description}` : ''}`);
        }
        lines.push('');
    }
    return lines.join('\n');
}

export function formatLocalModelsMarkdown(res: LocalModelsResult): string {
    const lines: string[] = [
        `# NTM Local Ollama Models (${res.action})`,
        '',
        `- **Detected VRAM:** ${res.detected_vram_gb.toFixed(1)} GB (${res.vram_source || 'default'})`,
        `- **Recommended Models:** ${res.recommendations.map(m => `\`${m}\``).join(', ') || '—'}`,
    ];
    if (res.host) {
        lines.push(`- **Ollama Host:** \`${res.host}\``);
    }
    if (!res.success && res.error) {
        lines.push(`- **Status:** Unavailable — ${res.error}`);
        if (res.hint) {
            lines.push(`- **Hint:** ${res.hint}`);
        }
    }
    if (res.action === 'list') {
        lines.push('', `## Installed Models (${res.model_count})`, '');
        if (res.models.length === 0) {
            lines.push('No local Ollama models installed.');
        } else {
            for (const m of res.models) {
                const gib = (m.size_bytes / (1024 * 1024 * 1024)).toFixed(1);
                lines.push(`- \`${m.name}\` — ${gib} GiB${m.parameters ? ` (${m.parameters})` : ''}`);
            }
        }
    }
    lines.push('');
    return lines.join('\n');
}
