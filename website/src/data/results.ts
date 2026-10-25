import raw from './recorded-results.json';

// Codex supplies runs in recorded-results.json. See website/README.md for the format.
// A run is shown only when its provenance is complete.

export type RecordedRun = {
  id: string;
  title: string;
  kind: 'measured' | 'replay';
  recordedAt: string;
  sourceCommit: string;
  environment: string;
  workload: string;
  summary: string;
  columns: string[];
  rows: (string | number)[][];
  rawDataUrl: string;
  notes?: string[];
};

export type DemoVideo = { url: string; title: string; recordedAt: string };

export type ResultsFile = { runs: RecordedRun[]; video: DemoVideo | null };

export function runProblems(run: Partial<RecordedRun>): string[] {
  const problems: string[] = [];
  for (const key of ['id', 'title', 'recordedAt', 'sourceCommit', 'environment', 'workload', 'summary', 'rawDataUrl'] as const) {
    if (typeof run[key] !== 'string' || !(run[key] as string).trim()) problems.push(`missing ${key}`);
  }
  if (run.kind !== 'measured' && run.kind !== 'replay') problems.push('kind must be measured or replay');
  if (!Array.isArray(run.columns) || run.columns.length === 0) problems.push('missing columns');
  if (!Array.isArray(run.rows) || run.rows.length === 0) problems.push('missing rows');
  else if (run.rows.some(r => !Array.isArray(r) || r.length !== run.columns?.length)) problems.push('row length does not match columns');
  return problems;
}

export const results = raw as ResultsFile;
export const validRuns = results.runs.filter(r => runProblems(r).length === 0);
export const rejectedRuns = results.runs.length - validRuns.length;
