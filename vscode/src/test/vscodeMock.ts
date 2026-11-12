import * as path from 'path';
import Module = require('module');

export interface RecordedTerminal {
    name?: string;
    shown: boolean;
    sentCommands: Array<{ text: string; shouldExecute?: boolean }>;
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

export interface VscodeMockState {
    binaryPath: string;
    workspaceName: string | undefined;
    workspaceFolders: Array<{ uri: { fsPath: string } }> | undefined;
    asRelativePathOverride: ((uri: { fsPath: string }, includeWorkspaceFolder?: boolean) => string) | undefined;
    createdTerminals: RecordedTerminal[];
    createdWebviewPanels: RecordedWebviewPanel[];
    registeredCommands: Map<string, (...args: unknown[]) => unknown>;
    executedCommands: Array<{ command: string; args: unknown[] }>;
    inputBoxQueue: Array<string | undefined>;
    quickPickQueue: unknown[];
    infoMessages: string[];
    warningMessages: string[];
    errorMessages: string[];
}

export const mockState: VscodeMockState = {
    binaryPath: 'ntm',
    workspaceName: undefined,
    workspaceFolders: undefined,
    asRelativePathOverride: undefined,
    createdTerminals: [],
    createdWebviewPanels: [],
    registeredCommands: new Map(),
    executedCommands: [],
    inputBoxQueue: [],
    quickPickQueue: [],
    infoMessages: [],
    warningMessages: [],
    errorMessages: [],
};

export function resetVscodeMock(): void {
    mockState.binaryPath = 'ntm';
    mockState.workspaceName = undefined;
    mockState.workspaceFolders = undefined;
    mockState.asRelativePathOverride = undefined;
    mockState.createdTerminals.length = 0;
    mockState.createdWebviewPanels.length = 0;
    mockState.registeredCommands.clear();
    mockState.executedCommands.length = 0;
    mockState.inputBoxQueue.length = 0;
    mockState.quickPickQueue.length = 0;
    mockState.infoMessages.length = 0;
    mockState.warningMessages.length = 0;
    mockState.errorMessages.length = 0;
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
        return { fsPath, scheme: 'file', path: fsPath.replace(/\\/g, '/') };
    },
    joinPath(base: { fsPath: string }, ...segments: string[]) {
        const joined = path.join(base.fsPath, ...segments);
        return { fsPath: joined, scheme: 'file', path: joined.replace(/\\/g, '/') };
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
                sentCommands: [],
            };
            mockState.createdTerminals.push(term);
            return {
                show() {
                    term.shown = true;
                },
                sendText(text: string, shouldExecute?: boolean) {
                    term.sentCommands.push({ text, shouldExecute });
                },
                dispose() {},
            };
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
        async showInputBox(): Promise<string | undefined> {
            return mockState.inputBoxQueue.shift();
        },
        async showQuickPick(items: unknown): Promise<unknown> {
            if (mockState.quickPickQueue.length > 0) {
                return mockState.quickPickQueue.shift();
            }
            return Array.isArray(items) ? items[0] : undefined;
        },
        showInformationMessage(msg: string) {
            mockState.infoMessages.push(msg);
            return Promise.resolve(undefined);
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
