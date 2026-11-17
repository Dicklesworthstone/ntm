'use client';

import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { apiFetch } from '@/lib/api/client';

interface TargetedScanResponse {
  scan_id?: string;
  id?: string;
  state?: string;
  path?: string;
}

interface BugItem {
  id: string;
  scan_id: string;
  finding: {
    severity: string;
    file: string;
    line: number;
    message: string;
    category: string;
    rule_id: string;
    suggestion?: string;
  };
  bead_id?: string;
}

interface BugsListResponse {
  bugs?: BugItem[];
  count?: number;
  total?: number;
}

interface FindingDetailResponse {
  id?: string;
  scan_id?: string;
  dismissed?: boolean;
  bead_id?: string;
  finding?: {
    severity?: string;
    file?: string;
    line?: number;
    message?: string;
    category?: string;
    rule_id?: string;
    suggestion?: string;
  };
}

function getErr(err: unknown): string {
  return err instanceof Error ? err.message : 'Request failed';
}

export function ScannerTargetedAndBugsPanel() {
  const queryClient = useQueryClient();
  const [targetFile, setTargetFile] = useState('');
  const [languages, setLanguages] = useState('');
  const [failOnWarning, setFailOnWarning] = useState(false);
  const [scanNotice, setScanNotice] = useState<string | null>(null);

  const [bugFileFilter, setBugFileFilter] = useState('');
  const [bugsResult, setBugsResult] = useState<BugsListResponse | null>(null);

  const [findingIdInput, setFindingIdInput] = useState('');
  const [findingDetail, setFindingDetail] = useState<FindingDetailResponse | null>(null);

  const targetedScanMut = useMutation({
    mutationFn: () => {
      const langs = languages
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
      return apiFetch<TargetedScanResponse>('/api/v1/scanner/scan', {
        method: 'POST',
        body: JSON.stringify({
          ...(targetFile.trim() ? { file: targetFile.trim() } : {}),
          ...(langs.length > 0 ? { languages: langs } : {}),
          ...(failOnWarning ? { fail_on_warning: true } : {}),
        }),
      });
    },
    onSuccess: (res) => {
      const sid = res.scan_id || res.id || 'queued';
      setScanNotice(`Started targeted UBS scan (${sid})`);
      queryClient.invalidateQueries({ queryKey: ['scanner'] });
      queryClient.invalidateQueries({ queryKey: ['bugs'] });
    },
  });

  const listBugsMut = useMutation({
    mutationFn: () => {
      const qs = new URLSearchParams({ limit: '25' });
      if (bugFileFilter.trim()) {
        qs.set('file', bugFileFilter.trim());
      }
      return apiFetch<BugsListResponse>(`/api/v1/bugs?${qs.toString()}`);
    },
    onSuccess: (res) => setBugsResult(res),
  });

  const inspectFindingMut = useMutation({
    mutationFn: (id: string) =>
      apiFetch<FindingDetailResponse>(
        `/api/v1/scanner/findings/${encodeURIComponent(id.trim())}`
      ),
    onSuccess: (res) => setFindingDetail(res),
  });

  return (
    <div
      role="region"
      aria-label="Targeted File Scan & Active Bug Triage"
      className="grid grid-cols-1 lg:grid-cols-3 gap-4"
    >
      {/* POST /api/v1/scanner/scan */}
      <div className="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div className="text-sm font-semibold text-gray-100">
          Targeted UBS Scan (<code className="font-mono text-xs">/api/v1/scanner/scan</code>)
        </div>
        <div className="space-y-2">
          <input
            type="text"
            aria-label="Targeted Scan File"
            value={targetFile}
            onChange={(e) => setTargetFile(e.target.value)}
            placeholder="Single file (e.g. internal/serve/server.go)"
            className="w-full bg-gray-900 border border-gray-600 rounded px-2.5 py-1.5 text-xs font-mono text-gray-200"
          />
          <div className="flex flex-wrap items-center gap-2">
            <input
              type="text"
              aria-label="Targeted Scan Languages"
              value={languages}
              onChange={(e) => setLanguages(e.target.value)}
              placeholder="Languages (e.g. go,ts)"
              className="flex-1 min-w-[120px] bg-gray-900 border border-gray-600 rounded px-2.5 py-1.5 text-xs font-mono text-gray-200"
            />
            <label className="flex items-center gap-1.5 text-xs text-gray-300 cursor-pointer">
              <input
                type="checkbox"
                checked={failOnWarning}
                onChange={(e) => setFailOnWarning(e.target.checked)}
                className="rounded bg-gray-700 border-gray-600"
              />
              Fail on warning
            </label>
          </div>
          <button
            type="button"
            disabled={targetedScanMut.isPending}
            onClick={() => targetedScanMut.mutate()}
            className="px-3 py-1.5 text-xs font-medium bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded text-white"
          >
            {targetedScanMut.isPending ? 'Launching...' : 'Run Targeted Scan'}
          </button>
        </div>
        {targetedScanMut.error && (
          <div className="text-xs text-red-400">{getErr(targetedScanMut.error)}</div>
        )}
        {scanNotice && (
          <div data-testid="targeted-scan-notice" className="text-xs text-emerald-400">
            {scanNotice}
          </div>
        )}
      </div>

      {/* GET /api/v1/bugs */}
      <div className="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div className="text-sm font-semibold text-gray-100">
          Active Bugs Feed (<code className="font-mono text-xs">/api/v1/bugs</code>)
        </div>
        <div className="flex items-center gap-2">
          <input
            type="text"
            aria-label="Bug File Substring Filter"
            value={bugFileFilter}
            onChange={(e) => setBugFileFilter(e.target.value)}
            placeholder="Filter file substring..."
            className="flex-1 bg-gray-900 border border-gray-600 rounded px-2.5 py-1.5 text-xs font-mono text-gray-200"
          />
          <button
            type="button"
            disabled={listBugsMut.isPending}
            onClick={() => listBugsMut.mutate()}
            className="px-3 py-1.5 text-xs font-medium bg-amber-600 hover:bg-amber-500 disabled:opacity-50 rounded text-white"
          >
            {listBugsMut.isPending ? 'Loading...' : 'Load Active Bugs'}
          </button>
        </div>
        {listBugsMut.error && (
          <div className="text-xs text-red-400">{getErr(listBugsMut.error)}</div>
        )}
        {bugsResult && (
          <div
            data-testid="active-bugs-results"
            className="space-y-1 max-h-32 overflow-y-auto text-xs text-gray-300"
          >
            <div className="text-gray-400">
              Active non-dismissed bugs: <strong className="text-gray-100">{bugsResult.total ?? bugsResult.count ?? 0}</strong>
            </div>
            {(bugsResult.bugs ?? []).map((b) => (
              <div key={b.id} className="flex items-center justify-between gap-2 bg-gray-900/70 rounded px-2 py-1">
                <span className="font-mono truncate">
                  [{b.finding.severity}] {b.finding.file}:{b.finding.line}
                </span>
                <button
                  type="button"
                  onClick={() => {
                    setFindingIdInput(b.id);
                    inspectFindingMut.mutate(b.id);
                  }}
                  className="text-[11px] text-blue-400 hover:underline shrink-0"
                >
                  Inspect
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* GET /api/v1/scanner/findings/{id} */}
      <div className="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div className="text-sm font-semibold text-gray-100">
          Finding Inspector (<code className="font-mono text-xs">/api/v1/scanner/findings/&#123;id&#125;</code>)
        </div>
        <div className="flex items-center gap-2">
          <input
            type="text"
            aria-label="Finding ID to Inspect"
            value={findingIdInput}
            onChange={(e) => setFindingIdInput(e.target.value)}
            placeholder="Finding ID..."
            className="flex-1 bg-gray-900 border border-gray-600 rounded px-2.5 py-1.5 text-xs font-mono text-gray-200"
          />
          <button
            type="button"
            disabled={!findingIdInput.trim() || inspectFindingMut.isPending}
            onClick={() => inspectFindingMut.mutate(findingIdInput)}
            className="px-3 py-1.5 text-xs font-medium bg-gray-700 hover:bg-gray-600 disabled:opacity-50 rounded text-gray-100"
          >
            {inspectFindingMut.isPending ? 'Loading...' : 'Lookup Finding'}
          </button>
        </div>
        {inspectFindingMut.error && (
          <div className="text-xs text-red-400">{getErr(inspectFindingMut.error)}</div>
        )}
        {findingDetail && (
          <div
            data-testid="finding-inspector-detail"
            className="bg-gray-900/70 rounded p-2 text-xs text-gray-300 space-y-1"
          >
            <div className="font-mono text-gray-200">
              {findingDetail.id} · {findingDetail.finding?.rule_id || 'rule'}
            </div>
            <div>{findingDetail.finding?.message}</div>
            {findingDetail.finding?.suggestion && (
              <div className="text-gray-400">Fix: {findingDetail.finding.suggestion}</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
