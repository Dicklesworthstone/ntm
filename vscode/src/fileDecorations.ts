import * as vscode from 'vscode';
import { MailReservation, NtmClient } from './ntmClient';

/**
 * Normalize a workspace URI's relative path to POSIX `/` separators.
 */
export function normalizeWorkspaceRelativePath(uri: vscode.Uri): string {
    return vscode.workspace.asRelativePath(uri, false)
        .replace(/\\/g, '/')
        .replace(/^\.\//, '');
}

/**
 * Match a workspace-relative path against an Agent Mail glob reservation pattern.
 * Normalizes both path and pattern to POSIX `/` separators and supports `*`, `**`, and `?`.
 */
export function matchesReservationPattern(rawPath: string, rawPattern: string): boolean {
    const normPath = rawPath.replace(/\\/g, '/').replace(/^\.\//, '').replace(/^\/+/, '');
    const normPattern = rawPattern.replace(/\\/g, '/').replace(/^\.\//, '').replace(/^\/+/, '');

    if (!normPath || !normPattern) {
        return false;
    }

    let regexPattern = normPattern
        .replace(/[.+^${}()|[\]\\]/g, '\\$&')
        .replace(/\/\*\*\//g, '<<<GLOBSTAR_MID>>>')
        .replace(/^\*\*\//, '<<<GLOBSTAR_START>>>')
        .replace(/\/\*\*$/, '<<<GLOBSTAR_END>>>')
        .replace(/\*\*/g, '<<<GLOBSTAR_ANY>>>')
        .replace(/\*/g, '[^/]*')
        .replace(/\?/g, '[^/]')
        .replace(/<<<GLOBSTAR_MID>>>/g, '(?:/|/.*/)')
        .replace(/<<<GLOBSTAR_START>>>/g, '(?:.*/)?')
        .replace(/<<<GLOBSTAR_END>>>/g, '/.+')
        .replace(/<<<GLOBSTAR_ANY>>>/g, '.*');

    regexPattern = '^' + regexPattern + '$';

    try {
        const regex = new RegExp(regexPattern);
        return regex.test(normPath);
    } catch {
        return normPath === normPattern;
    }
}

/**
 * FileDecorationProvider for showing NTM file reservation status in the explorer.
 * Files with active reservations show a lock badge indicating which agent holds them.
 */
export class NtmFileDecorationProvider implements vscode.FileDecorationProvider {
    private reservations: MailReservation[] = [];
    private workspaceRoot: string | undefined;

    private _onDidChangeFileDecorations = new vscode.EventEmitter<vscode.Uri | vscode.Uri[] | undefined>();
    readonly onDidChangeFileDecorations = this._onDidChangeFileDecorations.event;

    constructor(private readonly client: NtmClient) {
        this.workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    }

    /**
     * Update reservations from NTM and trigger decoration refresh.
     */
    async refresh(): Promise<void> {
        try {
            const mail = await this.client.getMail();
            this.reservations = mail.file_reservations || [];
            this._onDidChangeFileDecorations.fire(undefined);
        } catch {
            this.reservations = [];
            this._onDidChangeFileDecorations.fire(undefined);
        }
    }

    /**
     * Set reservations directly (useful for testing and immediate state sync).
     */
    public setReservations(reservations: MailReservation[]): void {
        this.reservations = reservations;
        this._onDidChangeFileDecorations.fire(undefined);
    }

    /**
     * Provide decoration for a URI if it matches a reservation pattern.
     */
    provideFileDecoration(uri: vscode.Uri): vscode.FileDecoration | undefined {
        const currentRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? this.workspaceRoot;
        if (!currentRoot || this.reservations.length === 0) {
            return undefined;
        }

        const relativePath = normalizeWorkspaceRelativePath(uri);
        const normalizedFull = (uri.fsPath || '').replace(/\\/g, '/');
        const normalizedRoot = currentRoot.replace(/\\/g, '/').replace(/\/+$/, '');
        if (
            normalizedFull &&
            relativePath === normalizedFull &&
            !normalizedFull.startsWith(normalizedRoot + '/')
        ) {
            return undefined;
        }

        for (const res of this.reservations) {
            if (matchesReservationPattern(relativePath, res.pattern)) {
                return {
                    badge: res.exclusive ? '🔒' : '👁',
                    tooltip: `${res.exclusive ? 'Locked' : 'Watched'} by ${res.agent}${res.reason ? ` (${res.reason})` : ''}`,
                    color: res.exclusive
                        ? new vscode.ThemeColor('gitDecoration.modifiedResourceForeground')
                        : new vscode.ThemeColor('gitDecoration.untrackedResourceForeground'),
                };
            }
        }

        return undefined;
    }

    dispose(): void {
        this._onDidChangeFileDecorations.dispose();
    }
}

/**
 * Create and register the file decoration provider.
 * Returns disposable for cleanup.
 */
export function registerFileDecorations(
    context: vscode.ExtensionContext,
    client: NtmClient
): { provider: NtmFileDecorationProvider; disposable: vscode.Disposable } {
    const provider = new NtmFileDecorationProvider(client);

    const disposable = vscode.window.registerFileDecorationProvider(provider);

    provider.refresh();

    const interval = setInterval(() => provider.refresh(), 10000);
    context.subscriptions.push({ dispose: () => clearInterval(interval) });

    return { provider, disposable };
}
