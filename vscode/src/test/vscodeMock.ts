import * as path from 'path';
import Module = require('module');

export interface RecordedTerminal {
    name?: string;
    shown: boolean;
    disposed?: boolean;
    sentCommands: Array<{ text: string; shouldExecute?: boolean }>;
    dispose?: () => void;
}

export interface RecordedWebviewPanel {
    viewType: string;
    title: string;
    revealed: boolean;
    disposed: boolean;
    webview: {
        html: string;
        onDidReceiveMessage: (listener: (msg: unknown) => void, thisArgs?: unknown, disposables?: Array<{ dispose(): void }>) => { dispose(): void };
        triggerMessage: (msg: unknown) => void;
    };
    onDidDispose: (listener: () => void, thisArgs?: unknown, disposables?: Array<{ dispose(): void }>) => { dispose(): void };
    reveal: () => void;
    dispose: () => void;
}

export interface MockTerminalInstance {
    readonly name: string;
    exitStatus: { code: number | undefined } | undefined;
    show(preserveFocus?: boolean): void;
    sendText(text: string, shouldExecute?: boolean): void;
    dispose(): void;
}

export interface VscodeMockState {
    binaryPath: string;
    webPort: number;
    workspaceName: string | undefined;
    workspaceFolders: Array<{ uri: { fsPath: string } }> | undefined;
    asRelativePathOverride: ((uri: { fsPath: string }, includeWorkspaceFolder?: boolean) => string) | undefined;
    createdTerminals: RecordedTerminal[];
    openTerminals: MockTerminalInstance[];
    createdWebviewPanels: RecordedWebviewPanel[];
    registeredCommands: Map<string, (...args: unknown[]) => unknown>;
    executedCommands: Array<{ command: string; args: unknown[] }>;
    inputBoxQueue: Array<string | undefined>;
    inputBoxOptionsHistory: Array<{ prompt?: string; value?: string; placeHolder?: string }>;
    quickPickQueue: unknown[];
    quickPickItemsHistory: unknown[][];
    infoMessages: string[];
    infoMessageActionQueue: Array<string | undefined>;
    warningMessages: string[];
    errorMessages: string[];
    openedExternalUris: string[];
    progressTitles: string[];
}

export const mockState: VscodeMockState = {
    binaryPath: 'ntm',
    webPort: 7337,
    workspaceName: undefined,
    workspaceFolders: undefined,
    asRelativePathOverride: undefined,
    createdTerminals: [],
    openTerminals: [],
    createdWebviewPanels: [],
    registeredCommands: new Map(),
    executedCommands: [],
    inputBoxQueue: [],
    inputBoxOptionsHistory: [],
    quickPickQueue: [],
    quickPickItemsHistory: [],
    infoMessages: [],
    infoMessageActionQueue: [],
    warningMessages: [],
    errorMessages: [],
    openedExternalUris: [],
    progressTitles: [],
};

export function resetVscodeMock(): void {
    mockState.binaryPath = 'ntm';
    mockState.webPort = 7337;
    mockState.workspaceName = undefined;
    mockState.workspaceFolders = undefined;
    mockState.asRelativePathOverride = undefined;
    mockState.createdTerminals.length = 0;
    mockState.openTerminals.length = 0;
    mockState.createdWebviewPanels.length = 0;
    mockState.registeredCommands.clear();
    mockState.executedCommands.length = 0;
    mockState.inputBoxQueue.length = 0;
    mockState.inputBoxOptionsHistory.length = 0;
    mockState.quickPickQueue.length = 0;
    mockState.quickPickItemsHistory.length = 0;
    mockState.infoMessages.length = 0;
    mockState.infoMessageActionQueue.length = 0;
    mockState.warningMessages.length = 0;
    mockState.errorMessages.length = 0;
    mockState.openedExternalUris.length = 0;
    mockState.progressTitles.length = 0;
}

class MockEventEmitter<T> {
    private listeners = new Set<(e: T) => void>();

    readonly event = (listener: (e: T) => void) => {
        this.listeners.add(listener);
        return {
            dispose: () => {
                this.listeners.delete(listener);
            },
        };
    };

    fire(data: T): void {
        for (const listener of this.listeners) {
            listener(data);
        }
    }

    dispose(): void {
        this.listeners.clear();
    }
}

class MockThemeIcon {
    constructor(public readonly id: string) {}
}

class MockThemeColor {
    constructor(public readonly id: string) {}
}

class MockTreeItem {
    public contextValue?: string;
    public iconPath?: unknown;
    public description?: string | boolean;
    public tooltip?: string;

    constructor(
        public readonly label: string,
        public readonly collapsibleState: number = 0
    ) {}
}

const MockUri = {
    file(fsPath: string) {
        return { fsPath, scheme: 'file', path: fsPath.replace(/\\/g, '/'), toString: () => `file://${fsPath}` };
    },
    joinPath(base: { fsPath: string }, ...segments: string[]) {
        const joined = path.join(base.fsPath, ...segments);
        return { fsPath: joined, scheme: 'file', path: joined.replace(/\\/g, '/'), toString: () => `file://${joined}` };
    },
    parse(value: string) {
        try {
            const parsed = new URL(value);
            return {
                scheme: parsed.protocol.replace(/:$/, ''),
                authority: parsed.host,
                path: parsed.pathname,
                query: parsed.search.replace(/^\?/, ''),
                fragment: parsed.hash.replace(/^#/, ''),
                fsPath: '',
                toString: () => value,
            };
        } catch {
            return {
                scheme: 'http',
                authority: '',
                path: value,
                query: '',
                fragment: '',
                fsPath: '',
                toString: () => value,
            };
        }
    },
};

const vscodeMockObject = {
    TreeItem: MockTreeItem,
    TreeItemCollapsibleState: {
        None: 0,
        Collapsed: 1,
        Expanded: 2,
    },
    ThemeIcon: MockThemeIcon,
    ThemeColor: MockThemeColor,
    EventEmitter: MockEventEmitter,
    Uri: MockUri,
    ViewColumn: {
        One: 1,
        Two: 2,
    },
    StatusBarAlignment: {
        Left: 1,
        Right: 2,
    },
    ProgressLocation: {
        SourceControl: 1,
        Window: 10,
        Notification: 15,
    },
    env: {
        async openExternal(target: { toString?: () => string } | string): Promise<boolean> {
            const str = typeof target === 'string'
                ? target
                : (typeof target?.toString === 'function' ? target.toString() : String(target));
            mockState.openedExternalUris.push(str);
            return true;
        },
    },
    workspace: {
        get workspaceFolders() {
            return mockState.workspaceFolders;
        },
        get name() {
            return mockState.workspaceName;
        },
        getConfiguration(section?: string) {
            return {
                get<T>(key: string, defaultValue?: T): T {
                    if (section === 'ntm' && key === 'binaryPath') {
                        return (mockState.binaryPath as unknown) as T;
                    }
                    if (section === 'ntm' && key === 'webPort') {
                        return (mockState.webPort as unknown) as T;
                    }
                    return defaultValue as T;
                },
            };
        },
        asRelativePath(pathOrUri: string | { fsPath: string }, includeWorkspaceFolder?: boolean): string {
            const uriObj = typeof pathOrUri === 'string' ? { fsPath: pathOrUri } : pathOrUri;
            if (mockState.asRelativePathOverride) {
                return mockState.asRelativePathOverride(uriObj, includeWorkspaceFolder);
            }
            const root = mockState.workspaceFolders?.[0]?.uri.fsPath;
            if (root && uriObj.fsPath.startsWith(root)) {
                const rel = uriObj.fsPath.slice(root.length).replace(/^[\\/]+/, '');
                return rel || uriObj.fsPath;
            }
            return uriObj.fsPath;
        },
    },
    window: {
        activeTextEditor: undefined as unknown,
        get terminals() {
            return mockState.openTerminals;
        },
        createStatusBarItem() {
            return {
                text: '',
                tooltip: '',
                color: undefined as unknown,
                command: undefined as string | undefined,
                shown: false,
                show() {
                    this.shown = true;
                },
                hide() {
                    this.shown = false;
                },
                dispose() {},
            };
        },
        createTerminal(options?: { name?: string }) {
            const term: RecordedTerminal = {
                name: options?.name,
                shown: false,
                disposed: false,
                sentCommands: [],
            };
            mockState.createdTerminals.push(term);
            const instance: MockTerminalInstance = {
                name: options?.name ?? '',
                exitStatus: undefined,
                show() {
                    term.shown = true;
                },
                sendText(text: string, shouldExecute?: boolean) {
                    term.sentCommands.push({ text, shouldExecute });
                },
                dispose() {
                    term.disposed = true;
                    const idx = mockState.openTerminals.indexOf(instance);
                    if (idx >= 0) {
                        mockState.openTerminals.splice(idx, 1);
                    }
                },
            };
            term.dispose = () => instance.dispose();
            mockState.openTerminals.push(instance);
            return instance;
        },
        async withProgress<R>(
            options: { title?: string },
            task: (
                progress: { report: (value: unknown) => void },
                token: { isCancellationRequested: boolean; onCancellationRequested: () => { dispose(): void } }
            ) => Promise<R> | R
        ): Promise<R> {
            if (options?.title) {
                mockState.progressTitles.push(options.title);
            }
            return task(
                { report: () => {} },
                { isCancellationRequested: false, onCancellationRequested: () => ({ dispose: () => {} }) }
            );
        },
        createTreeView(_viewId: string, _options: unknown) {
            return {
                dispose() {},
            };
        },
        registerFileDecorationProvider(_provider: unknown) {
            return {
                dispose() {},
            };
        },
        createWebviewPanel(viewType: string, title: string) {
            const msgListeners: Array<(msg: unknown) => void> = [];
            const disposeListeners: Array<() => void> = [];
            const panel: RecordedWebviewPanel = {
                viewType,
                title,
                revealed: false,
                disposed: false,
                webview: {
                    html: '',
                    onDidReceiveMessage(listener, _thisArgs, disposables) {
                        msgListeners.push(listener);
                        const disp = { dispose: () => {} };
                        if (Array.isArray(disposables)) {
                            disposables.push(disp);
                        }
                        return disp;
                    },
                    triggerMessage(msg: unknown) {
                        for (const l of msgListeners) {
                            l(msg);
                        }
                    },
                },
                onDidDispose(listener, _thisArgs, disposables) {
                    disposeListeners.push(listener);
                    const disp = { dispose: () => {} };
                    if (Array.isArray(disposables)) {
                        disposables.push(disp);
                    }
                    return disp;
                },
                reveal() {
                    panel.revealed = true;
                },
                dispose() {
                    panel.disposed = true;
                },
            };
            mockState.createdWebviewPanels.push(panel);
            return panel;
        },
        async showInputBox(options?: { prompt?: string; value?: string; placeHolder?: string }): Promise<string | undefined> {
            mockState.inputBoxOptionsHistory.push(options ?? {});
            return mockState.inputBoxQueue.shift();
        },
        async showQuickPick(items: unknown): Promise<unknown> {
            if (Array.isArray(items)) {
                mockState.quickPickItemsHistory.push([...items]);
            }
            if (mockState.quickPickQueue.length > 0) {
                const next = mockState.quickPickQueue.shift();
                if (typeof next === 'function' && Array.isArray(items)) {
                    return (next as (candidates: unknown[]) => unknown)(items);
                }
                return next;
            }
            return Array.isArray(items) ? items[0] : undefined;
        },
        showInformationMessage(msg: string, ..._items: string[]) {
            mockState.infoMessages.push(msg);
            const action = mockState.infoMessageActionQueue.length > 0
                ? mockState.infoMessageActionQueue.shift()
                : undefined;
            return Promise.resolve(action);
        },
        showWarningMessage(msg: string) {
            mockState.warningMessages.push(msg);
            return Promise.resolve(undefined);
        },
        showErrorMessage(msg: string) {
            mockState.errorMessages.push(msg);
            return Promise.resolve(undefined);
        },
    },
    commands: {
        registerCommand(command: string, callback: (...args: unknown[]) => unknown) {
            mockState.registeredCommands.set(command, callback);
            return {
                dispose: () => {
                    mockState.registeredCommands.delete(command);
                },
            };
        },
        async executeCommand(command: string, ...args: unknown[]) {
            mockState.executedCommands.push({ command, args });
            const handler = mockState.registeredCommands.get(command);
            if (handler) {
                return handler(...args);
            }
            return undefined;
        },
    },
};

let installed = false;

export function installVscodeMock(): void {
    if (installed) {
        return;
    }
    const moduleAny = Module as unknown as {
        _load: (request: string, parent: unknown, isMain: boolean) => unknown;
    };
    const originalLoad = moduleAny._load;
    moduleAny._load = function patchedLoad(request: string, parent: unknown, isMain: boolean): unknown {
        if (request === 'vscode') {
            return vscodeMockObject;
        }
        return originalLoad.call(this, request, parent, isMain);
    };
    installed = true;
}

installVscodeMock();
