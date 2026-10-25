import { logos, type Logo } from './logos';

// Public claims live here. Sources: docs/mvp-scope.md, docs/pool-sizing-results.md,
// docs/provider-evidence.md, docs/release-gates.md and docs/recovery.md.

export const repoUrl = 'https://github.com/jyshum/pooltune';

// Order matters: hosted providers first, self-hosted PgBouncer last.
// PgBouncer has no logo of its own, so it uses the PostgreSQL elephant.
export const targets: { name: string; logo: Logo }[] = [
  { name: 'Azure', logo: logos.azure },
  { name: 'Google Cloud', logo: logos.googleCloud },
  { name: 'Supabase', logo: logos.supabase },
  { name: 'PgBouncer', logo: logos.postgresql },
];

export const setup = [
  { title: 'Install next to your pooler', body: 'Pooltune runs beside it. Your app keeps its connection string.' },
  { title: 'Set your bounds', body: 'Pick a minimum, a maximum and the CPU and memory levels to back off at.' },
  { title: 'Watch it in your dashboard', body: 'A private dashboard on your own server. Freeze it any time.' },
];

// Kept short on purpose. Each item maps to an open gap in docs/release-gates.md and docs/recovery.md.
export const limits =
  'Known limits: no backup routing if the Pooltune proxy stops, hosted providers are still in testing, and no speed claims until they are measured.';
