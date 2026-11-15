import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {setTimeout as sleep} from 'node:timers/promises';
import pg from 'pg';
import {
  captureCronPeriod, checkCronCampaignBudget, CRON_CAMPAIGN_BUDGET_MS, runCronPhase,
  waitCronAcknowledgement, waitCronBoundary, waitCronFinalResult,
} from './cron-traffic.ts';
import {summarizeTraffic, trafficRequest, type RequestSample} from './traffic.ts';

const rookery = 'osdjqwpzgyzwfaxyufze';
const iso = (ms: number) => new Date(ms).toISOString();
const sqlISO = (value: string) => `to_char(${value} AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`;

type OwnedFixture = {
  Installation: string;
  Query?: number;
  Request: {
    Target: {Project: string; DatabaseOID: number; OwnerOID: number; JobID: number; WitnessJobID: number; Name: string; CommandDigest: string};
    Candidate: {Schema: string; DatabaseOID: number; Relation: string; TableOID: number};
    Runtime: {Schema: string; FunctionOID: number; OwnerOID: number; DefinitionDigest: string};
    ReadOID: number;
    ProcedureOID: number;
    ReadDigest: string;
    ProcedureDigest: string;
    FirstCycle: string;
    Expires: string;
  };
};

export function readOwnedCronObserverBinding(raw: string) {
  if (Buffer.byteLength(raw) > 16 * 1024) throw Error('invalid owned Cron fixture binding');
  const binding = JSON.parse(raw) as OwnedFixture;
  assert(binding && /^[0-9a-f]{16}$/.test(binding.Installation) && (binding.Query === undefined || binding.Query === 0),
    'Cron observation requires an automatic owned fixture');
  const project = `supabase/${rookery}/${binding.Installation}`;
  const schema = `pt_hosted_cr_${binding.Installation}`;
  const {Target: target, Candidate: candidate, Runtime: runtime, FirstCycle, Expires} = binding.Request;
  const first = Date.parse(FirstCycle);
  const expires = Date.parse(Expires);
  const ids = [target.DatabaseOID, target.OwnerOID, target.JobID, target.WitnessJobID, candidate.TableOID, binding.Request.ReadOID,
    binding.Request.ProcedureOID, runtime.FunctionOID, runtime.OwnerOID];
  const digest = /^[0-9a-f]{64}$/;
  assert(target.Project === project && target.Name === `${schema}_target` && target.JobID !== target.WitnessJobID &&
    target.CommandDigest === createHash('sha256').update(`CALL "${schema}"."pooltune_cron_hold"(200)`).digest('hex') &&
    candidate.Schema === schema && candidate.Relation === 'candidate' && candidate.DatabaseOID === target.DatabaseOID &&
    runtime.Schema === `pt_cron_guard_${binding.Installation}` && digest.test(runtime.DefinitionDigest) &&
    digest.test(binding.Request.ReadDigest) && digest.test(binding.Request.ProcedureDigest) &&
    ids.every(id => Number.isSafeInteger(id) && id > 0) && Number.isFinite(first) && first % 120_000 === 0 &&
    expires > first + 12 * 60_000 + 35_000, 'invalid owned Cron fixture binding');
  return {project, schema, target, candidate, first, expires, request: binding.Request};
}

type CounterRow = {
  query: string; queryid: string; dbid: number; userid: number; boot: string;
  reset: string; entry: string; calls: number; blocks: number; execution_ms: number;
};

export async function waitOwnedCronActivation(first: number, read: () => Promise<{now: number; active: boolean}>,
  wait: () => Promise<unknown> = () => sleep(700)) {
  let previous = 0;
  for (;;) {
    const {now, active} = await read();
    if (!Number.isFinite(now) || now <= previous || now >= first) throw Error('Cron activation observation expired or clock changed');
    if (active) {
      if (now < first - 55_000) throw Error('Cron target was active before its approved activation window');
      return;
    }
    previous = now;
    await wait();
  }
}

// Captures only the already prepared private fixture. PostgreSQL and the
// recorder still enforce job identity, evidence, and all action authority.
export async function runOwnedCronTraffic() {
  assert.equal(process.env.POOLTUNE_STAGING_PRIVATE_STATE, '1');
  const {project, schema, target, candidate, first, expires, request} = readOwnedCronObserverBinding(process.env.POOLTUNE_OWNED_CRON_FIXTURE ?? '');
  assert(process.env.POOLTUNE_OWNED_CRON_PASSWORD && process.env.POOLTUNE_OWNED_CRON_CA, 'Cron fixture login is missing');
  const stateURL = new URL(process.env.POOLTUNE_WORKER_STATE_DSN ?? '');
  assert(['postgres:', 'postgresql:'].includes(stateURL.protocol) &&
    stateURL.hostname === 'postgres.railway.internal' && stateURL.port === '5432' && stateURL.pathname === '/railway' &&
    !stateURL.search && !stateURL.hash && stateURL.username && stateURL.password, 'Cron observation requires the private Railway state');
  const clients: pg.Client[] = [];
  const failures: string[] = [];
  const begin = performance.now();
  function client(name: string, config: pg.ClientConfig) {
    const connection = new pg.Client({connectionTimeoutMillis: 5000, query_timeout: 2500, statement_timeout: 1500,
      application_name: 'pooltune-owned-cron-traffic', ...config});
    connection.on('error', () => { failures.push(name); });
    clients.push(connection);
    return connection;
  }
  function check() {
    checkCronCampaignBudget(begin);
    if (failures.length) throw Error(`Cron observer lost its ${failures[0]} connection`);
  }
  const writer: pg.ClientConfig = {host: `db.${rookery}.supabase.co`, port: 5432, database: 'postgres', user: schema,
    password: process.env.POOLTUNE_OWNED_CRON_PASSWORD,
    ssl: {ca: process.env.POOLTUNE_OWNED_CRON_CA, rejectUnauthorized: true}};
  const stats = client('counter', writer);
  const observer = client('block observer', writer);
  const state = client('worker state', {connectionString: stateURL.href});
  const trafficClients = [0, 1, 2, 3].map(index => client(`traffic ${index + 1}`, writer));
  const targetSQL = `SELECT "${schema}"."pooltune_cron_read"() AS id FROM (VALUES (1)) AS invocation(slot)`;
  const normalizedTarget = targetSQL.replace('VALUES (1)', 'VALUES ($1)');
  const healthySQL = `SELECT id FROM ONLY "${schema}"."healthy" WHERE id=$1`;
  const writeSQL = `UPDATE ONLY "${schema}"."healthy" SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id`;
  const normalizedWrite = writeSQL.replace("WHEN 'a' THEN 'b' ELSE 'a'", 'WHEN $2 THEN $3 ELSE $4');
  const result = {project, periods_recorded: 0, job_id: '', verification_job_id: '', outcome: ''};
  try {
    await stats.connect();
    const route = await stats.query<{eligible: boolean}>(`SELECT NOT pg_is_in_recovery()
      AND (SELECT current_user::regrole::oid=$1 AND oid=$2 FROM pg_database WHERE datname=current_database())
      AND current_user=session_user AND has_schema_privilege(current_user,'cron','USAGE')
      AND has_function_privilege(current_user,'cron.alter_job(bigint,text,text,text,text,boolean)','EXECUTE')
      AND NOT has_any_column_privilege(current_user,'cron.job','UPDATE')
      AND row_security_active('cron.job') AND row_security_active('cron.job_run_details')
      AND (SELECT extversion='1.6.4' FROM pg_extension WHERE extname='pg_cron')
      AND EXISTS(SELECT FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
        WHERE p.oid=$3 AND p.proname='scheduler_status' AND p.pronargs=0 AND p.prosecdef AND n.nspname=$4
          AND n.nspowner=$5 AND p.proowner=$5 AND p.proowner='postgres'::regrole::oid
          AND NOT has_schema_privilege(current_user,n.oid,'CREATE')
          AND encode(sha256(convert_to(pg_get_functiondef(p.oid),'UTF8')),'hex')=$6) AS eligible`,
    [target.OwnerOID, target.DatabaseOID, request.Runtime.FunctionOID, request.Runtime.Schema, request.Runtime.OwnerOID, request.Runtime.DefinitionDigest]);
    assert.equal(route.rows[0]?.eligible, true, 'exact scheduler provider must qualify before sending traffic');
    const scheduler = await stats.query<{ready: boolean}>(`SELECT ready FROM "${request.Runtime.Schema}".scheduler_status()`);
    assert(scheduler.rows.length === 1 && scheduler.rows[0].ready, 'the scoped scheduler clock is unavailable');
    const scope = await stats.query<{eligible: boolean}>(`SELECT EXISTS (
      SELECT FROM pg_namespace n JOIN pg_class c ON c.relnamespace=n.oid
      WHERE n.nspname=$1 AND n.nspowner=$2 AND c.oid=$3 AND c.relowner=$2 AND c.relname='candidate'
        AND obj_description(n.oid,'pg_namespace')=$4
        AND EXISTS(SELECT FROM pg_class h WHERE h.relnamespace=n.oid AND h.relname='healthy' AND h.relowner=$2
          AND h.relkind='r' AND NOT (h.relispartition OR h.relrowsecurity OR h.relhasrules OR h.relhastriggers))
      ) AND EXISTS (SELECT FROM cron.job WHERE jobid=$5 AND jobname=$6 AND schedule='*/2 * * * *'
        AND username::text=current_user AND database=current_database()
        AND encode(sha256(convert_to(command,'UTF8')),'hex')=$7)
      AND EXISTS (SELECT FROM cron.job WHERE jobid=$8 AND jobname=$1||'_witness' AND active AND schedule='2 seconds'
        AND username::text=current_user AND database=current_database() AND command='SELECT 1')
      AND NOT EXISTS(SELECT FROM (VALUES($9::oid,$10::text),($11::oid,$12::text)) v(oid,digest)
        WHERE NOT EXISTS (SELECT FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE p.oid=v.oid AND n.nspname=$1 AND p.proowner=$2 AND NOT p.prosecdef
            AND encode(sha256(convert_to(pg_get_functiondef(p.oid),'UTF8')),'hex')=v.digest)) AS eligible`,
      [schema, target.OwnerOID, candidate.TableOID, `Pooltune hosted cron fixture ${project}`, target.JobID, target.Name,
        target.CommandDigest, target.WitnessJobID, request.ReadOID, request.ReadDigest, request.ProcedureOID, request.ProcedureDigest]);
    assert.equal(scope.rows[0]?.eligible, true, 'the exact disposable Cron objects or active jobs did not qualify');
    const clock = async () => {
      check();
      const {rows} = await stats.query<{now: string}>(`SELECT extract(epoch FROM clock_timestamp())*1000 AS now`);
      return Number(rows[0]?.now);
    };
    const now = await clock();
    assert(Number.isFinite(now) && now < first && expires > now + 12 * 60_000, 'Cron observer started outside the fixture lifetime');
    await observer.connect();
    await state.connect();
    for (const connection of trafficClients) {
      await connection.connect();
      await connection.query("SET lock_timeout='250ms'");
    }
    await waitOwnedCronActivation(first, async () => {
      check();
      const {rows} = await stats.query<{now: string; active: boolean}>(`SELECT extract(epoch FROM clock_timestamp())*1000 AS now,j.active
        FROM cron.job j JOIN cron.job w ON w.jobid=$5 AND w.username=j.username AND w.database=j.database
        WHERE j.jobid=$1 AND j.jobname=$2 AND j.username::text=current_user AND j.database=current_database()
          AND j.schedule='*/2 * * * *' AND encode(sha256(convert_to(j.command,'UTF8')),'hex')=$3
          AND w.jobname=$4 AND w.active AND w.schedule='2 seconds' AND w.command='SELECT 1'`,
      [target.JobID, target.Name, target.CommandDigest, `${schema}_witness`, target.WitnessJobID]);
      assert.equal(rows.length, 1, 'Cron activation disappeared or the exact jobs changed');
      return {now: Number(rows[0].now), active: rows[0].active};
    });
    await trafficClients[0].query(targetSQL);
    await trafficClients[2].query(healthySQL, [43]);
    await trafficClients[3].query(writeSQL, [90001]);
    const namespace = (await stats.query<{nspname: string}>(`SELECT n.nspname FROM pg_extension e
      JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'`)).rows[0]?.nspname;
    assert(namespace && /^[a-z_]+$/.test(namespace), 'Cron statement counters are unavailable');
    async function snapshot() {
      check();
      const {rows} = await stats.query<CounterRow & {at: string}>(`SELECT ${sqlISO('clock_timestamp()')} AS at,
        s.queryid::text,s.query,s.dbid,s.userid,${sqlISO('pg_postmaster_start_time()')} AS boot,
        ${sqlISO('i.stats_reset')} AS reset,${sqlISO('s.stats_since')} AS entry,s.calls::float8 AS calls,
        s.total_exec_time AS execution_ms,s.shared_blks_hit::float8+s.shared_blks_read::float8 AS blocks
        FROM "${namespace}".pg_stat_statements s CROSS JOIN "${namespace}".pg_stat_statements_info i
        WHERE s.dbid=$1 AND s.userid=$2 AND s.toplevel AND s.query=ANY($3::text[])`,
      [target.DatabaseOID, target.OwnerOID, [targetSQL, normalizedTarget, healthySQL, writeSQL, normalizedWrite]]);
      assert.equal(rows.length, 3, 'all three exact observed query counters are required');
      return {at: rows[0].at, rows};
    }
    const initial = await snapshot();
    const id = (...statements: string[]) => {
      const row = initial.rows.find(item => statements.includes(item.query));
      assert(row && /^-?[0-9]+$/.test(row.queryid), 'observed Cron statement identity is missing');
      return row.queryid;
    };
    const targetID = id(targetSQL, normalizedTarget), healthyID = id(healthySQL), writeID = id(writeSQL, normalizedWrite);
    async function observeBlocks() {
      const {rows} = await observer.query(`SELECT ${sqlISO('clock_timestamp()')} AS at,r.runid::float8 AS run_id,r.jobid::float8 AS job_id,
        a.pid,${sqlISO('a.backend_start')} AS backend_start,w.pid AS waiting_pid,${sqlISO('w.backend_start')} AS waiting_start,w.query_id::text AS query_id
        FROM cron.job_run_details r JOIN pg_stat_activity a ON a.pid=r.job_pid
          JOIN pg_stat_activity w ON a.pid=ANY(pg_blocking_pids(w.pid))
        WHERE r.jobid=$1 AND r.end_time IS NULL AND r.status='running' AND w.query_id=$2::bigint AND w.datid=$3 AND w.usesysid=$4`,
      [target.JobID, targetID, target.DatabaseOID, target.OwnerOID]);
      assert(rows.every(row => Number.isSafeInteger(row.run_id) && row.backend_start && row.waiting_start), 'Cron blocking evidence is incomplete');
      return rows;
    }
    async function traffic(seconds: number, targetOn: boolean, observe: boolean) {
      const kinds = ['target', 'target', 'healthy', 'write'];
      const statements = [targetSQL, targetSQL, healthySQL, writeSQL];
      const cadence = [1000, 1000, 250, 500];
      const offsets = [0, 500, 0, 0];
      const offered = [0, 0, 0, 0];
      const requests: RequestSample[] = [];
      const blocks: unknown[] = [];
      const anchor = performance.now();
      let collecting = observe;
      await runCronPhase(async () => {
        while (collecting) {
          check();
          blocks.push(...await observeBlocks());
          await sleep(50);
        }
      }, async () => {
        try {
          const all = await Promise.allSettled(trafficClients.map(async (connection, lane) => {
            if (lane < 2 && !targetOn) return;
            for (let tick = 0; offsets[lane] + tick * cadence[lane] < seconds * 1000; tick++) {
              check();
              offered[lane]++;
              const scheduled = anchor + offsets[lane] + tick * cadence[lane];
              if (scheduled > performance.now()) await sleep(scheduled - performance.now());
              if (performance.now() - scheduled > cadence[lane]) continue;
              const rowID = lane === 3 ? 90001 + tick % 10 : 43;
              requests.push(await trafficRequest(connection, statements[lane], lane < 2 ? [] : [rowID], rows => {
                assert(rows.length === 1 && rows[0].id === rowID, 'owned Cron request did not return its fixture row');
              }, kinds[lane], scheduled));
            }
          }));
          for (const lane of all) if (lane.status === 'rejected') throw lane.reason;
          const remaining = seconds * 1000 - (performance.now() - anchor);
          if (remaining > 0) await sleep(remaining);
        } finally { collecting = false; }
      });
      const measure = (kind: string, n: number) => summarizeTraffic(requests.filter(item => item.kind === kind), n, seconds);
      const summary = {target: measure('target', offered[0] + offered[1]), healthy: measure('healthy', offered[2]), write: measure('write', offered[3])};
      assert(Object.values(summary).every(lane => lane.errors === 0 && lane.missed === 0), 'a Cron workload lane failed or missed demand');
      return {summary, blocks};
    }
    const healthy = async (seconds: number) => { await traffic(seconds, false, false); };
    const waitUntil = (at: number) => waitCronBoundary(at, clock, deadline => healthy(Math.max(0, (deadline - performance.now()) / 1000)), () => performance.now(), check);
    const boundedTraffic = async (_name: string, seconds: number, targetOn: boolean, cycle?: number) => {
      const before = await snapshot();
      const {summary, blocks} = await traffic(seconds, targetOn, cycle !== undefined);
      const after = await snapshot();
      const counter = (rows: CounterRow[], key: string) => {
        const row = rows.find(entry => entry.queryid === key);
        assert(row, 'Cron statement disappeared while collecting a period');
        return {Database: row.dbid, Role: row.userid, Query: row.queryid, Boot: row.boot, Reset: row.reset, Entry: row.entry,
          Calls: row.calls, Blocks: row.blocks, ExecutionMS: row.execution_ms};
      };
      const metric = (key: string, kind: keyof typeof summary) => ({before: counter(before.rows, key), after: counter(after.rows, key),
        offered: summary[kind].offered, errors: summary[kind].errors, missed: summary[kind].missed});
      return {start: before.at, end: after.at, target: metric(targetID, 'target'), healthy: metric(healthyID, 'healthy'), write: metric(writeID, 'write'), blocks};
    };
    const jobs = async () => (await state.query(`SELECT id,created_at,finished_at,result,attempts,not_before,parent_id
      FROM pooltune_worker.investigations WHERE project_id=$1 AND database_oid=$2 AND role_oid=$3 AND query_id=$4 ORDER BY id`,
    [project, target.DatabaseOID, target.OwnerOID, targetID])).rows;
    let attached: {id: string; parent_id: string} | undefined;
    for (let index = 0; index < 6; index++) {
      const cycle = first + index * 120_000;
      const period = await captureCronPeriod(cycle, index, waitUntil, boundedTraffic);
      await runCronPhase(() => archive(period), async () => healthy(Math.max(.1, (cycle + 100_000 - await clock()) / 1000)));
      result.periods_recorded++;
      console.error(`Cron period ${index + 1} of 6 archived.`);
      if (index === 2) {
        const queued = await waitCronAcknowledgement(cycle, target.WitnessJobID, jobs, clock, healthy);
        const parent = queued.find(job => job.finished_at && job.result?.outcome === 'awaiting_verification');
        attached = queued.find(job => !job.finished_at && job.parent_id === parent?.id);
        assert(attached && Date.parse(queued.find(job => job.id === attached?.id).not_before) > await clock(), 'Cron continuation is missing or early');
        result.job_id = attached.parent_id;
        result.verification_job_id = attached.id;
      }
    }
    assert(attached, 'Cron action did not leave an exact verification job');
    await waitUntil(first + 12 * 60_000 + 8000);
    const queued = await waitCronFinalResult(first, attached, jobs, clock, healthy, begin + CRON_CAMPAIGN_BUDGET_MS);
    result.outcome = queued.find(job => job.id === attached?.id).result.outcome;
    console.log(JSON.stringify(result));
  } finally {
    await Promise.allSettled(clients.map(connection => connection.end()));
  }
}

async function archive(period: object) {
  await new Promise<void>((resolve, reject) => {
    const failure = () => reject(Error('private Cron recorder rejected a measured period'));
    const child = spawn('pooltune-owned-cron-record', [], {stdio: ['pipe', 'ignore', 'ignore'], timeout: 13_000});
    child.on('error', failure);
    child.stdin.on('error', failure);
    child.on('close', code => { if (code === 0) resolve(); else failure(); });
    child.stdin.end(JSON.stringify(period));
  });
}

if (import.meta.main) {
  try {
    await runOwnedCronTraffic();
  } catch (error) {
    const code = (error as {code?: unknown})?.code;
    console.error(typeof code === 'string' && /^[0-9A-Z_]{1,24}$/.test(code) ?
      `Owned Cron observation failed (${code}).` : 'Owned Cron observation failed. Check the scoped fixture, recorder and worker queue.');
    process.exitCode = 1;
  }
}
