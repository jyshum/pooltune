// Public claims live here. Sources: docs/mvp-scope.md, docs/pool-sizing-results.md,
// docs/provider-evidence.md, docs/release-gates.md and docs/recovery.md.

export const repoUrl = 'https://github.com/jyshum/pooltune';

export type Level = 'local' | 'testing' | 'readonly';

export const levelLabel: Record<Level, string> = {
  local: 'Verified locally',
  testing: 'In testing',
  readonly: 'Read-only for now',
};

export const targets: { name: string; pooler: string; level: Level }[] = [
  { name: 'PgBouncer', pooler: 'Self-hosted, with PostgreSQL', level: 'local' },
  { name: 'Azure', pooler: 'Database for PostgreSQL, built-in PgBouncer', level: 'testing' },
  { name: 'Google Cloud', pooler: 'Cloud SQL, managed connection pooling', level: 'testing' },
  { name: 'Supabase', pooler: 'Supavisor transaction pool', level: 'readonly' },
];

export const setup = [
  { title: 'Install next to your pooler', body: 'Pooltune runs beside it. Your app keeps its connection string.' },
  { title: 'Set your bounds', body: 'Pick a minimum, a maximum and the CPU and memory levels to back off at.' },
  { title: 'Watch it in your dashboard', body: 'A private dashboard on your own server. Freeze it any time.' },
];

// Kept short on purpose. Each item maps to an open gap in docs/release-gates.md and docs/recovery.md.
export const limits =
  'Known limits: no backup routing if the Pooltune proxy stops, hosted providers are still in testing, and no speed claims until they are measured.';
