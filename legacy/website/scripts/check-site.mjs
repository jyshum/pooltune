// Copy and data checks for the public site. Run before and after the Vite build.
// Fails on em or en dashes in our own files, invalid recorded results, or a broken build output.
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const errors = [];

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

// 1. No em or en dashes in authored copy. Vendored React Bits code is included too.
const authored = [...walk(join(root, 'src')), join(root, 'index.html'), join(root, 'README.md')].filter(existsSync);
for (const file of authored) {
  readFileSync(file, 'utf8').split('\n').forEach((line, i) => {
    if (/[–—]/.test(line)) errors.push(`${relative(root, file)}:${i + 1} contains an em or en dash`);
  });
}

// 2. Recorded results must carry full provenance.
const data = JSON.parse(readFileSync(join(root, 'src/data/recorded-results.json'), 'utf8'));
if (!Array.isArray(data.runs)) errors.push('recorded-results.json: runs must be an array');
for (const [i, run] of (data.runs ?? []).entries()) {
  const where = `recorded-results.json runs[${i}]`;
  for (const key of ['id', 'title', 'recordedAt', 'sourceCommit', 'environment', 'workload', 'summary', 'rawDataUrl']) {
    if (typeof run[key] !== 'string' || !run[key].trim()) errors.push(`${where}: missing ${key}`);
  }
  if (run.kind !== 'measured' && run.kind !== 'replay') errors.push(`${where}: kind must be "measured" or "replay"`);
  if (!Array.isArray(run.columns) || !run.columns.length) errors.push(`${where}: missing columns`);
  if (!Array.isArray(run.rows) || !run.rows.length) errors.push(`${where}: missing rows`);
  else if (run.rows.some(r => !Array.isArray(r) || r.length !== run.columns?.length)) errors.push(`${where}: row length does not match columns`);
}
if (data.video !== null && data.video !== undefined) {
  for (const key of ['url', 'title', 'recordedAt']) {
    if (typeof data.video[key] !== 'string' || !data.video[key].trim()) errors.push(`recorded-results.json video: missing ${key}`);
  }
}

// 3. After the build: relative asset paths and the illustrative label survive bundling.
if (process.argv.includes('--dist')) {
  const dist = join(root, 'dist');
  const html = existsSync(join(dist, 'index.html')) ? readFileSync(join(dist, 'index.html'), 'utf8') : '';
  if (!html) errors.push('dist/index.html missing');
  if (/(src|href)="\/(?!\/)/.test(html)) errors.push('dist/index.html uses root-absolute paths');
  const js = walk(join(dist, 'assets')).filter(f => f.endsWith('.js')).map(f => readFileSync(f, 'utf8')).join('\n');
  if (!js.includes('Illustrative')) errors.push('bundle is missing the illustrative label');
}

if (errors.length) {
  console.error(errors.join('\n'));
  process.exit(1);
}
console.log(`check-site: ok (${authored.length} files, ${data.runs.length} recorded runs${process.argv.includes('--dist') ? ', dist' : ''})`);
