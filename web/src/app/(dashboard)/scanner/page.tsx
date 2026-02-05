'use client';

import { useState, useEffect } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';

// Types based on scanner.go REST API
interface ScanOptions {
  path?: string;
  languages?: string[];
  exclude?: string[];
  staged_only?: boolean;
  diff_only?: boolean;
  ci?: boolean;
  fail_on_warning?: boolean;
  timeout_seconds?: number;
}

interface ScanRecord {
  id: string;
  state: 'pending' | 'running' | 'completed' | 'failed';
  path: string;
  options?: ScanOptions;
  started_at: string;
  completed_at?: string;
  result?: ScanResult;
  error?: string;
  finding_ids?: string[];
}

interface ScanResult {
  totals: {
    files: number;
    critical: number;
    warning: number;
    info: number;
  };
  findings: Finding[];
}

interface Finding {
  severity: 'critical' | 'warning' | 'info';
  file: string;
  line: number;
  column?: number;
  message: string;
  category: string;
  rule_id: string;
  suggestion?: string;
}

interface FindingRecord {
  id: string;
  scan_id: string;
  finding: Finding;
  dismissed: boolean;
  dismissed_at?: string;
  dismissed_by?: string;
  bead_id?: string;
  created_at: string;
}

interface ScannerStatus {
  available: boolean;
  version?: string;
  current_scan?: ScanRecord;
  last_scan?: ScanRecord;
  total_scans: number;
  total_findings: number;
}

interface BugSummary {
  total_findings: number;
  critical: number;
  warning: number;
  info: number;
  by_severity: Record<string, number>;
  by_category: Record<string, number>;
  by_file: Record<string, number>;
  dismissed_count: number;
  linked_beads: number;
}

const API_BASE = process.env.NEXT_PUBLIC_API_URL || 'http://localhost:8080';

async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${url}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
  });
  if (!res.ok) {
    const error = await res.json().catch(() => ({ message: res.statusText }));
    throw new Error(error.message || res.statusText);
  }
  const data = await res.json();
  return data.data || data;
}

// Severity color helpers
function getSeverityColor(severity: string): string {
  switch (severity) {
    case 'critical': return 'text-red-400';
    case 'warning': return 'text-yellow-400';
    case 'info': return 'text-blue-400';
    default: return 'text-gray-400';
  }
}

function getSeverityBg(severity: string): string {
  switch (severity) {
    case 'critical': return 'bg-red-500/20 border-red-500/30';
    case 'warning': return 'bg-yellow-500/20 border-yellow-500/30';
    case 'info': return 'bg-blue-500/20 border-blue-500/30';
    default: return 'bg-gray-500/20 border-gray-500/30';
  }
}

// Severity Chart Component
function SeverityChart({ critical, warning, info }: { critical: number; warning: number; info: number }) {
  const total = critical + warning + info;
  if (total === 0) {
    return (
      <div className="h-4 bg-gray-700 rounded-full overflow-hidden">
        <div className="h-full bg-green-500/50 w-full flex items-center justify-center text-xs text-green-300">
          No findings
        </div>
      </div>
    );
  }

  const critPct = (critical / total) * 100;
  const warnPct = (warning / total) * 100;
  const infoPct = (info / total) * 100;

  return (
    <div className="h-4 bg-gray-700 rounded-full overflow-hidden flex">
      {critical > 0 && (
        <div
          className="h-full bg-red-500 transition-all duration-300"
          style={{ width: `${critPct}%` }}
          title={`Critical: ${critical}`}
        />
      )}
      {warning > 0 && (
        <div
          className="h-full bg-yellow-500 transition-all duration-300"
          style={{ width: `${warnPct}%` }}
          title={`Warning: ${warning}`}
        />
      )}
      {info > 0 && (
        <div
          className="h-full bg-blue-500 transition-all duration-300"
          style={{ width: `${infoPct}%` }}
          title={`Info: ${info}`}
        />
      )}
    </div>
  );
}

// Scan State Badge
function ScanStateBadge({ state }: { state: ScanRecord['state'] }) {
  const styles: Record<string, string> = {
    pending: 'bg-gray-500/20 text-gray-300 border-gray-500/30',
    running: 'bg-blue-500/20 text-blue-300 border-blue-500/30 animate-pulse',
    completed: 'bg-green-500/20 text-green-300 border-green-500/30',
    failed: 'bg-red-500/20 text-red-300 border-red-500/30',
  };

  return (
    <span className={`px-2 py-0.5 text-xs rounded border ${styles[state] || styles.pending}`}>
      {state}
    </span>
  );
}

// Finding Card Component
function FindingCard({
  finding,
  onDismiss,
  onCreateBead,
  isDismissing,
  isCreatingBead,
}: {
  finding: FindingRecord;
  onDismiss: () => void;
  onCreateBead: () => void;
  isDismissing: boolean;
  isCreatingBead: boolean;
}) {
  const [expanded, setExpanded] = useState(false);

  return (
    <div className={`border rounded-lg p-3 ${getSeverityBg(finding.finding.severity)} ${finding.dismissed ? 'opacity-50' : ''}`}>
      <div className="flex items-start justify-between gap-2">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <span className={`font-medium ${getSeverityColor(finding.finding.severity)}`}>
              {finding.finding.severity.toUpperCase()}
            </span>
            <span className="text-gray-400 text-sm">{finding.finding.category}</span>
            {finding.dismissed && (
              <span className="text-xs text-gray-500 bg-gray-700 px-1.5 py-0.5 rounded">dismissed</span>
            )}
            {finding.bead_id && (
              <span className="text-xs text-purple-400 bg-purple-500/20 px-1.5 py-0.5 rounded">
                {finding.bead_id}
              </span>
            )}
          </div>
          <div className="text-sm text-gray-300 mb-1 truncate" title={finding.finding.message}>
            {finding.finding.message}
          </div>
          <div className="text-xs text-gray-500 font-mono">
            {finding.finding.file}:{finding.finding.line}
          </div>
        </div>
        <button
          onClick={() => setExpanded(!expanded)}
          className="text-gray-400 hover:text-gray-200 p-1"
        >
          {expanded ? '▲' : '▼'}
        </button>
      </div>

      {expanded && (
        <div className="mt-3 pt-3 border-t border-gray-600/50">
          <div className="text-xs text-gray-400 mb-2">
            <span className="text-gray-500">Rule:</span> {finding.finding.rule_id}
          </div>
          {finding.finding.suggestion && (
            <div className="text-sm text-gray-300 bg-gray-800/50 p-2 rounded mb-3">
              <span className="text-gray-500 text-xs block mb-1">Suggestion:</span>
              {finding.finding.suggestion}
            </div>
          )}
          <div className="flex gap-2">
            {!finding.dismissed && (
              <button
                onClick={onDismiss}
                disabled={isDismissing}
                className="px-3 py-1 text-xs bg-gray-600 hover:bg-gray-500 disabled:opacity-50 rounded"
              >
                {isDismissing ? 'Dismissing...' : 'Dismiss'}
              </button>
            )}
            {!finding.bead_id && !finding.dismissed && (
              <button
                onClick={onCreateBead}
                disabled={isCreatingBead}
                className="px-3 py-1 text-xs bg-purple-600 hover:bg-purple-500 disabled:opacity-50 rounded"
              >
                {isCreatingBead ? 'Creating...' : 'Create Bead'}
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// Main Scanner Page
export default function ScannerPage() {
  const queryClient = useQueryClient();
  const [severityFilter, setSeverityFilter] = useState<string>('');
  const [showDismissed, setShowDismissed] = useState(false);
  const [selectedFindingId, setSelectedFindingId] = useState<string | null>(null);

  // Scanner status
  const { data: status, isLoading: statusLoading } = useQuery({
    queryKey: ['scanner', 'status'],
    queryFn: () => fetchJSON<ScannerStatus>('/api/v1/scanner/status'),
    refetchInterval: 5000, // Poll while scan might be running
  });

  // Bug summary
  const { data: summary } = useQuery({
    queryKey: ['bugs', 'summary'],
    queryFn: () => fetchJSON<BugSummary>('/api/v1/bugs/summary'),
    refetchInterval: 10000,
  });

  // Findings list
  const { data: findingsData, isLoading: findingsLoading } = useQuery({
    queryKey: ['scanner', 'findings', severityFilter, showDismissed],
    queryFn: () => fetchJSON<{ findings: FindingRecord[]; count: number }>(`/api/v1/scanner/findings?severity=${severityFilter}&include_dismissed=${showDismissed}&limit=100`),
    refetchInterval: 10000,
  });

  // Scan history
  const { data: historyData } = useQuery({
    queryKey: ['scanner', 'history'],
    queryFn: () => fetchJSON<{ scans: ScanRecord[] }>('/api/v1/scanner/history?limit=10'),
    refetchInterval: 10000,
  });

  // Run scan mutation
  const runScanMutation = useMutation({
    mutationFn: (options: ScanOptions) => fetchJSON<{ scan_id: string }>('/api/v1/scanner/run', {
      method: 'POST',
      body: JSON.stringify(options),
    }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['scanner'] });
      if (process.env.NODE_ENV === 'development') {
        console.log('[Scanner] Scan started');
      }
    },
  });

  // Dismiss finding mutation
  const dismissMutation = useMutation({
    mutationFn: (findingId: string) => fetchJSON(`/api/v1/scanner/findings/${findingId}/dismiss`, {
      method: 'POST',
    }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['scanner', 'findings'] });
      queryClient.invalidateQueries({ queryKey: ['bugs', 'summary'] });
      if (process.env.NODE_ENV === 'development') {
        console.log('[Scanner] Finding dismissed');
      }
    },
  });

  // Create bead mutation
  const createBeadMutation = useMutation({
    mutationFn: (findingId: string) => fetchJSON<{ bead_id: string }>(`/api/v1/scanner/findings/${findingId}/create-bead`, {
      method: 'POST',
    }),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ['scanner', 'findings'] });
      queryClient.invalidateQueries({ queryKey: ['bugs', 'summary'] });
      if (process.env.NODE_ENV === 'development') {
        console.log('[Scanner] Bead created:', data.bead_id);
      }
    },
  });

  // WebSocket for real-time updates
  useEffect(() => {
    const wsUrl = process.env.NEXT_PUBLIC_WS_URL || 'ws://localhost:8080/ws';
    let ws: WebSocket | null = null;

    try {
      ws = new WebSocket(wsUrl);
      ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data);
          if (msg.topic === 'scanner') {
            queryClient.invalidateQueries({ queryKey: ['scanner'] });
            queryClient.invalidateQueries({ queryKey: ['bugs'] });
          }
        } catch {
          // Ignore parse errors
        }
      };
    } catch {
      // WebSocket not available
    }

    return () => {
      if (ws) ws.close();
    };
  }, [queryClient]);

  const isScanning = status?.current_scan?.state === 'running' || status?.current_scan?.state === 'pending';
  const findings = findingsData?.findings || [];
  const history = historyData?.scans || [];

  return (
    <div className="p-6 space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-100">Scanner</h1>
          <p className="text-sm text-gray-400">UBS code scanner and bug tracking</p>
        </div>
        <div className="flex items-center gap-3">
          {status?.available ? (
            <span className="text-xs text-green-400 bg-green-500/20 px-2 py-1 rounded">
              UBS {status.version || 'available'}
            </span>
          ) : (
            <span className="text-xs text-red-400 bg-red-500/20 px-2 py-1 rounded">
              UBS unavailable
            </span>
          )}
          <button
            onClick={() => runScanMutation.mutate({})}
            disabled={!status?.available || isScanning || runScanMutation.isPending}
            className="px-4 py-2 bg-blue-600 hover:bg-blue-500 disabled:bg-gray-600 disabled:opacity-50 rounded-lg font-medium transition-colors"
          >
            {isScanning ? 'Scanning...' : 'Scan Now'}
          </button>
        </div>
      </div>

      {/* Stats Cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <div className="text-3xl font-bold text-red-400">{summary?.critical || 0}</div>
          <div className="text-sm text-gray-400">Critical</div>
        </div>
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <div className="text-3xl font-bold text-yellow-400">{summary?.warning || 0}</div>
          <div className="text-sm text-gray-400">Warnings</div>
        </div>
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <div className="text-3xl font-bold text-blue-400">{summary?.info || 0}</div>
          <div className="text-sm text-gray-400">Info</div>
        </div>
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <div className="text-3xl font-bold text-purple-400">{summary?.linked_beads || 0}</div>
          <div className="text-sm text-gray-400">Linked Beads</div>
        </div>
      </div>

      {/* Severity Distribution */}
      <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
        <div className="text-sm text-gray-400 mb-2">Severity Distribution</div>
        <SeverityChart
          critical={summary?.critical || 0}
          warning={summary?.warning || 0}
          info={summary?.info || 0}
        />
        <div className="flex justify-between text-xs text-gray-500 mt-2">
          <span>Total: {summary?.total_findings || 0} findings</span>
          <span>Dismissed: {summary?.dismissed_count || 0}</span>
        </div>
      </div>

      {/* Current/Last Scan Status */}
      {(status?.current_scan || status?.last_scan) && (
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <div className="text-sm text-gray-400 mb-2">
            {status.current_scan ? 'Current Scan' : 'Last Scan'}
          </div>
          {(() => {
            const scan = status.current_scan || status.last_scan;
            if (!scan) return null;
            return (
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-3">
                  <ScanStateBadge state={scan.state} />
                  <span className="text-sm text-gray-300 font-mono">{scan.path}</span>
                </div>
                <div className="text-xs text-gray-500">
                  {scan.state === 'completed' && scan.result && (
                    <span>
                      {scan.result.totals.files} files, {scan.result.findings?.length || 0} findings
                    </span>
                  )}
                  {scan.state === 'failed' && (
                    <span className="text-red-400">{scan.error}</span>
                  )}
                  {(scan.state === 'running' || scan.state === 'pending') && (
                    <span className="text-blue-400">In progress...</span>
                  )}
                </div>
              </div>
            );
          })()}
        </div>
      )}

      {/* Findings Section */}
      <div className="bg-gray-800 rounded-lg border border-gray-700">
        <div className="p-4 border-b border-gray-700 flex items-center justify-between">
          <h2 className="text-lg font-semibold text-gray-100">Findings</h2>
          <div className="flex items-center gap-3">
            <select
              value={severityFilter}
              onChange={(e) => setSeverityFilter(e.target.value)}
              className="bg-gray-700 border border-gray-600 rounded px-2 py-1 text-sm"
            >
              <option value="">All Severities</option>
              <option value="critical">Critical</option>
              <option value="warning">Warning</option>
              <option value="info">Info</option>
            </select>
            <label className="flex items-center gap-2 text-sm text-gray-400">
              <input
                type="checkbox"
                checked={showDismissed}
                onChange={(e) => setShowDismissed(e.target.checked)}
                className="rounded bg-gray-700 border-gray-600"
              />
              Show dismissed
            </label>
          </div>
        </div>

        <div className="p-4 space-y-3 max-h-[500px] overflow-y-auto">
          {findingsLoading ? (
            <div className="text-center text-gray-500 py-8">Loading findings...</div>
          ) : findings.length === 0 ? (
            <div className="text-center text-gray-500 py-8">
              {severityFilter || !showDismissed ? 'No findings match filters' : 'No findings yet. Run a scan to get started.'}
            </div>
          ) : (
            findings.map((finding) => (
              <FindingCard
                key={finding.id}
                finding={finding}
                onDismiss={() => dismissMutation.mutate(finding.id)}
                onCreateBead={() => createBeadMutation.mutate(finding.id)}
                isDismissing={dismissMutation.isPending && dismissMutation.variables === finding.id}
                isCreatingBead={createBeadMutation.isPending && createBeadMutation.variables === finding.id}
              />
            ))
          )}
        </div>
      </div>

      {/* Category Breakdown */}
      {summary && Object.keys(summary.by_category || {}).length > 0 && (
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h2 className="text-lg font-semibold text-gray-100 mb-3">By Category</h2>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
            {Object.entries(summary.by_category)
              .sort(([,a], [,b]) => b - a)
              .slice(0, 8)
              .map(([category, count]) => (
                <div key={category} className="bg-gray-700/50 rounded p-2">
                  <div className="text-sm font-medium text-gray-300">{category}</div>
                  <div className="text-lg font-bold text-gray-100">{count}</div>
                </div>
              ))}
          </div>
        </div>
      )}

      {/* File Hotspots */}
      {summary && Object.keys(summary.by_file || {}).length > 0 && (
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h2 className="text-lg font-semibold text-gray-100 mb-3">File Hotspots</h2>
          <div className="space-y-2">
            {Object.entries(summary.by_file)
              .sort(([,a], [,b]) => b - a)
              .slice(0, 5)
              .map(([file, count]) => (
                <div key={file} className="flex items-center justify-between">
                  <span className="text-sm text-gray-300 font-mono truncate flex-1" title={file}>
                    {file}
                  </span>
                  <span className="text-sm font-medium text-gray-400 ml-2">{count}</span>
                </div>
              ))}
          </div>
        </div>
      )}

      {/* Scan History */}
      {history.length > 0 && (
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h2 className="text-lg font-semibold text-gray-100 mb-3">Scan History</h2>
          <div className="space-y-2">
            {history.map((scan) => (
              <div key={scan.id} className="flex items-center justify-between py-2 border-b border-gray-700 last:border-0">
                <div className="flex items-center gap-3">
                  <ScanStateBadge state={scan.state} />
                  <span className="text-xs text-gray-500 font-mono">{scan.id}</span>
                </div>
                <div className="flex items-center gap-4 text-xs text-gray-500">
                  {scan.state === 'completed' && scan.result && (
                    <span>
                      {scan.result.totals.critical}C / {scan.result.totals.warning}W / {scan.result.totals.info}I
                    </span>
                  )}
                  <span>{new Date(scan.started_at).toLocaleString()}</span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
