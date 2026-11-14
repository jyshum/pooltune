import { spawn, type ChildProcess } from "node:child_process";
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { setTimeout as sleep } from "node:timers/promises";
import assert from "node:assert/strict";
import pg from "pg";

export type RequestSample = { kind: string; duration_ms: number; ok: boolean; code?: string };
export function summarizeTraffic(samples: RequestSample[], offered: number, seconds: number) {
  const times = samples.map(s => s.duration_ms).sort((a, b) => a - b);
  const percentile = (p: number) => times.length ? times[Math.ceil(p * times.length) - 1] : null;
  const completed = samples.filter(s => s.ok).length;
  return { offered, completed, errors: samples.length - completed, missed: offered - samples.length,
    completed_per_second: completed / seconds, p50_ms: percentile(.5), p95_ms: percentile(.95),
    p99_ms: percentile(.99), max_ms: times.at(-1) ?? null,
    error_codes: [...new Set(samples.filter(s => !s.ok).map(s => s.code))] };
}

export function trafficCase(repair: string, candidate: string) {
  assert(["index", "analyze", "statistics", "vacuum", "memory"].includes(repair));
  return {
    sql: repair === "memory" ? `SELECT ${candidate.replace(/\."candidate"$/,'."pooltune_memory_sum"')}() AS sum` : repair === "vacuum" ? `SELECT sum(lookup) FROM ONLY ${candidate} WHERE lookup >= $1 AND lookup <= $2`
      : `SELECT id FROM ${repair === "index" ? "" : "ONLY "}${candidate} WHERE lookup=$1` + (repair === "statistics" ? " AND padding=$2" : ""),
    input: (tick: number) => repair === "memory" ? [] : repair === "vacuum" ? [1,50000] : repair === "statistics" ? [1 + tick % 2, tick % 2 ? "a" : "b"] : [43],
    check: (rows: any[], tick: number) => {
      if (repair === "memory") { assert.equal(rows.length,1); assert.equal(String(rows[0]?.sum),"5000050000"); }
      else if (repair === "vacuum") { assert.equal(rows.length,1); assert.equal(String(rows[0]?.sum),"1250025000"); }
      else if (repair === "statistics") {
        const first = tick % 2 ? 101 : 1; const length = tick % 2 ? 300 : 100;
        const ids = rows.map(r => r.id).sort((a,b) => a-b);
        assert.equal(ids.length,length); assert(ids.every((id,i) => id === first+i));
      } else { assert.equal(rows.length,1); assert.equal(rows[0]?.id,43); }
    },
  };
}
export function vacuumObjective(measured: number[]) {
  assert.equal(measured.length,10);
  assert(measured.every(v => Number.isFinite(v) && v > 0));
  return Math.max(3,1.5 * measured.reduce((n,v) => n+v,0)/measured.length);
}

export function memoryObjective(measured:number[]) {
  assert.equal(measured.length,10);
  assert(measured.every(v=>Number.isFinite(v) && v>0));
  return Math.max(3,1.25*measured.reduce((n,v)=>n+v,0)/measured.length);
}
export function trafficCadence(repair:string,lane:number) {
  return repair === "memory" && lane < 2 ? 2000 : lane === 3 ? 500 : 250;
}

// Only the Go diagnostic vocabulary enters reports. Configuration/parser errors
// and arbitrary child stderr can contain private connection details and are omitted.
export function recordWorkerDiagnostic(lines: string[], binary: string, raw: string) {
  if (lines.length >= 40 || !["pooltune-worker", "pooltune-consumer"].includes(binary)) return;
  const line = raw.trim();
  if (line.length > 160 || !/^(consumer attempt|connect worker target|verify dedicated detector identity|connect worker state|configure worker|prepare worker state|poll worker) (failed(?: \(SQLSTATE [A-Z0-9]{5}\))?|timed out)$/.test(line)) return;
  lines.push(`${binary}: ${line}`.slice(0, 200));
}

export async function command(args: string[], timeoutMS = 65000) {
  const action=args[args.indexOf("-cron-action")+1];
  const operation=args.includes("-cron-action")&&["activate","record","inspect","witness","recover"].includes(action)?`cron_${action}`
    :args.includes("-remove-consumer-fixture")?"remove_consumer_fixture":args.includes("-create-consumer-fixture")?"create_consumer_fixture":"fixture";
  return new Promise<string>((resolve, reject) => {
    const child = spawn("pooltune-query-lab", ["-target", "rookery", ...args]);
    let timedOut=false;
    const timeout=setTimeout(()=>{timedOut=true;child.kill("SIGKILL")},timeoutMS);
    let result = "",diagnostic="";
    child.stdout.on("data", b => { result += b; });
    child.stderr.on("data",b=>{diagnostic=(diagnostic+b).slice(-4096)});
    child.on("error", ()=>{clearTimeout(timeout);reject(Object.assign(new Error(`fixture command ${operation} could not start`),{command_failure:{operation,code:"spawn_failed"}}))});
    // Wait for close so the bounded stderr tail is complete. Never retain raw
    // stderr, arguments, paths or connection details in an error/report.
    child.on("close", (code,signal) => {
      clearTimeout(timeout);
      if(code===0){resolve(result);return}
      const sqlstate=diagnostic.match(/\(SQLSTATE ([A-Z0-9]{5})\)/)?.[1];
      const command_failure={operation,exit_code:code,signal,timed_out:timedOut,...(sqlstate?{sqlstate}:{})};
      reject(Object.assign(new Error(`fixture command ${operation} exited ${code}${signal?` (${signal})`:""}`),{command_failure}));
    });
  });
}

async function main() {
  const repair = process.argv[2];
  if(repair === "cron") {const {runCronTraffic}=await import("./cron-traffic.ts");await runCronTraffic();return;}
  assert(["index", "analyze", "statistics", "vacuum", "memory"].includes(repair), "choose a supported fixture repair");
  assert.equal(process.env.POOLTUNE_STAGING_PRIVATE_STATE, "1");
  assert(process.env.ANTHROPIC_API_KEY && process.env.POOLTUNE_ANTHROPIC_MODEL);
  const config = new URL(process.env.POOLTUNE_LAB_TARGET_DSN!);
  assert.equal(config.hostname, "aws-0-us-west-2.pooler.supabase.com");
  assert.equal(config.port, "5432");
  assert.equal(config.pathname, "/postgres");
  const ca = readFileSync(process.env.POOLTUNE_LAB_ROOKERY_CA_FILE!, "utf8");
  config.search = "";
  const targetOptions = { connectionString: config.href, ssl: { ca, rejectUnauthorized: true }, connectionTimeoutMillis: 5000,
    application_name: "pooltune-traffic", statement_timeout: 1500, query_timeout: 2500 };
  const state = new pg.Client({ connectionString: process.env.POOLTUNE_LAB_STATE_DSN, connectionTimeoutMillis: 5000, statement_timeout: 3000 });
  const admin = new pg.Client(targetOptions);
  const directory = mkdtempSync(join(tmpdir(), "pooltune-traffic-"));
  let fixture: any;
  let baselineIndexOID: number | null = null;
  let children: ChildProcess[] = [];
  const childResults: any[] = [];
  const childFailures: number[] = [];
  const workerDiagnostics: string[] = [];
  const clients: pg.Client[] = [];
  const phases: any[] = [];
  const output: any = { repair, phases };
  const publish = () => console.log(JSON.stringify(output));
  async function stopWorkers() {
    const stopping = children;
    children = [];
    await Promise.all(stopping.map(child => new Promise<void>(resolve => {
      if (!child.pid || child.exitCode !== null || child.signalCode !== null) return resolve();
      const kill = setTimeout(() => child.kill("SIGKILL"), 4000);
      child.once("exit", () => { clearTimeout(kill); resolve(); });
      child.kill("SIGTERM");
    })));
  }
  function launch(binary: string, args: string[]) {
    const child = spawn(binary, args, { env: process.env });
    let buffer = "";
    child.stdout.on("data", chunk => {
      buffer += chunk;
      let newline;
      while ((newline = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
        if (line.startsWith("{")) childResults.push(JSON.parse(line));
      }
    });
    let diagnosticBuffer = "";
    child.stderr.on("data", chunk => {
      diagnosticBuffer = (diagnosticBuffer + chunk.toString()).slice(-4096);
      let newline;
      while ((newline = diagnosticBuffer.indexOf("\n")) >= 0) {
        recordWorkerDiagnostic(workerDiagnostics, binary, diagnosticBuffer.slice(0, newline));
        diagnosticBuffer = diagnosticBuffer.slice(newline + 1);
      }
    });
    child.on("exit", () => recordWorkerDiagnostic(workerDiagnostics, binary, diagnosticBuffer));
    child.on("error", () => { childFailures.push(-1); });
    child.on("exit", code => { if (code) childFailures.push(code); });
    children.push(child);
  }
  try {
    await state.connect(); await admin.connect();
    const fingerprintSQL = "SELECT count(*)::int AS count,md5(coalesce(string_agg(indexdef,E'\\n' ORDER BY indexdef),'')) AS hash FROM pg_indexes WHERE schemaname='public'";
    output.public_indexes_before = (await admin.query(fingerprintSQL)).rows[0];
    const flags = repair === "index" ? [] : [`-${repair}-fixture`];
    fixture = JSON.parse(await command(["-create-consumer-fixture", ...flags]));
    assert.match(fixture.id, /^[a-f0-9]{24}$/);
    const schema = `pooltune_trial_${fixture.id}`;
    assert.equal(fixture.schema, schema);
    assert.equal(fixture.enrollment.project, `rookery/consumer/${fixture.id}`);
    output.fixture = fixture.id;
    output.target_query_id = String(fixture.enrollment.queries[1].query_id);
    const enrollment = join(directory, "enrollment.json");
    let objective = 3;
    const candidate = `"${schema}"."candidate"`;
    const healthy = `"${schema}"."healthy"`;
    const source = repair === "index" ? "" : "ONLY ";
    const workload = trafficCase(repair,candidate);
    const targetSQL = workload.sql;
    const healthySQL = `SELECT id FROM ${source}${healthy} WHERE id=$1`;
    const writeSQL = `UPDATE ONLY ${healthy} SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id`;
    const queryIDs = fixture.enrollment.queries.map((q: any) => q.query_id);
    const extension = (await admin.query("SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'")).rows[0].nspname;
    assert.match(extension, /^[a-z_]+$/);
    const counterSQL = `SELECT queryid::text,calls::float8,total_exec_time,temp_blks_written::float8 AS temp_blocks,shared_blks_hit::float8+shared_blks_read::float8 AS blocks FROM "${extension}".pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND userid=current_user::regrole::oid AND toplevel AND (queryid=ANY($1::bigint[]) OR query LIKE $2 OR query LIKE '%JOIN unnest($2::oid[], $3::bigint[]) AS enrolled%')`;
    const snapshot = async () => (await admin.query(counterSQL, [queryIDs, `%${schema}%`])).rows;
    const jobs = async () => (await state.query("SELECT id,created_at,finished_at,result,attempts FROM pooltune_worker.investigations WHERE project_id=$1 ORDER BY id", [fixture.enrollment.project])).rows;
    function startWorkers() {
      launch("pooltune-worker", ["-target", "rookery", "-enrollment", enrollment, "-interval", "2s", "-window", "4s"]);
      const flag = repair === "index" ? "repair" : repair;
      launch("pooltune-consumer", ["-target", "rookery", "-project", fixture.enrollment.project, "-fixture", fixture.id, "-mode", "anthropic", `-${flag}-staging-fixture`]);
    }
    // Two target sessions share the declared rate; healthy reads stay at four/s and writes at two/s.
    const kinds = ["target", "target", "healthy", "write"];
    for (let i = 0; i < kinds.length; i++) {
      const client = new pg.Client(targetOptions); client.on("error", () => {});
      await client.connect(); clients.push(client);
      await client.query(`SET lock_timeout='250ms'; SET statement_timeout='1500ms'; SET max_parallel_workers_per_gather=0; SET random_page_cost=${repair === "vacuum" ? 1 : 5}`);
      if (repair !== "vacuum" && repair !== "memory") await client.query("SET enable_bitmapscan=off");
    }
    await admin.query("SET lock_timeout='250ms'; SET statement_timeout='5s'");
    async function createBaselineIndex() {
      await admin.query(`CREATE INDEX pooltune_lookup_idx ON ${candidate} (lookup)`);
      baselineIndexOID = (await admin.query("SELECT to_regclass($1)::oid AS oid", [`${schema}.pooltune_lookup_idx`])).rows[0].oid;
    }
    if (repair === "index") await createBaselineIndex();
    async function setSlow(slow: boolean) {
      if (repair === "index") {
        if (slow) {
          await admin.query(`DROP INDEX "${schema}".pooltune_lookup_idx RESTRICT`);
          baselineIndexOID = null;
        } else await createBaselineIndex();
      } else if (repair === "memory") {
        await admin.query(`ALTER FUNCTION "${schema}"."pooltune_memory_sum"() SET work_mem='${slow ? "4MB" : "32MB"}'`);
      } else if (repair === "vacuum") {
        if (slow) await admin.query(`UPDATE ONLY ${candidate} SET padding=CASE padding WHEN repeat('p',64) THEN repeat('q',64) ELSE repeat('p',64) END`);
        else {
          await admin.query("SET statement_timeout='2s'; SET maintenance_work_mem='16MB'");
          await admin.query(`VACUUM (ANALYZE FALSE,FULL FALSE,TRUNCATE FALSE,PARALLEL 0) ${candidate}`);
          await admin.query("SET statement_timeout='5s'");
        }
      } else {
        await Promise.all(clients.slice(0, 2).map(c => c.query(`SET enable_seqscan=${slow ? "on" : "off"}`)));
      }
    }
    if (repair === "analyze" || repair === "statistics" || repair === "memory") await setSlow(false);
    if (repair === "vacuum" || repair === "memory") {
      const baseline: number[] = [];
      const warm = await clients[0].query(targetSQL,workload.input(0)); workload.check(warm.rows,0);
      for (let i=0;i<10;i++) {
        const result = await clients[0].query("EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) " + targetSQL,workload.input(0));
        const plan = result.rows[0]["QUERY PLAN"][0];
        if (repair === "memory") {
          assert.equal(plan.Plan["Node Type"],"Result");
          assert.equal(plan.Plan["Temp Written Blocks"],0);
        } else {
          assert.equal(plan.Plan.Plans[0]["Node Type"],"Index Only Scan");
          assert.equal(plan.Plan.Plans[0]["Heap Fetches"],0);
        }
        baseline.push(plan["Execution Time"]);
      }
      objective = repair === "memory" ? memoryObjective(baseline) : vacuumObjective(baseline);
      output.healthy_calibration_ms = baseline;
      output.benefit_gates = { minimum_ms:5, minimum_fraction:.2, noise_multiple:3, ...(repair === "memory" ? {minimum_temp_write_reduction:.5} : {minimum_heap_reduction:.5}) };
      if (repair === "memory") output.memory_budget={per_sort_bytes:33554432,sort_nodes:1,max_concurrent_calls:3,aggregate_bytes:100663296,production_headroom:"unknown"};
    }
    output.target_calls_per_second=repair === "memory" ? 1 : 8;
    output.objective_ms = objective;
    for (const [i,q] of fixture.enrollment.queries.entries()) q.mean_execution_ms = i === 1 ? objective : 3;
    writeFileSync(enrollment, JSON.stringify(fixture.enrollment), { mode: 0o600 });
    async function phase(name: string, seconds: number) {
      const before = await snapshot();
      const samples: RequestSample[] = [];
      const offered = [0, 0, 0, 0];
      const start = performance.now();
      const at = new Date().toISOString();
      await Promise.all(clients.map(async (client, lane) => {
        const cadence = trafficCadence(repair,lane);
        const offset = repair === "memory" && lane < 2 ? lane * 1000 : lane * 35;
        for (let tick = 0; offset + tick * cadence < seconds * 1000; tick++) {
          offered[lane]++;
          const scheduled = start + offset + tick * cadence;
          const wait = scheduled - performance.now();
          if (wait > 0) await sleep(wait);
          if (performance.now() - scheduled > cadence) continue;
          const kind = kinds[lane];
          const input=workload.input(tick);
          samples.push(await trafficRequest(client,kind === "target" ? targetSQL : kind === "healthy" ? healthySQL : writeSQL,
            kind === "target" ? input : [kind === "healthy" ? 43 : 90001 + tick % 10],
            rows=>{if(kind === "target")workload.check(rows,tick);else assert.equal(rows[0]?.id,kind === "write" ? 90001 + tick % 10 : 43)},kind,scheduled));
        }
      }));
      const remaining = seconds * 1000 - (performance.now() - start);
      if (remaining > 0) await sleep(remaining);
      const wall = (performance.now() - start) / 1000;
      const after = await snapshot();
      const delta = counterDeltas(before,after);
      const summary: any = { name, start: at, seconds: wall, counters: delta, requests: {} };
      for (const kind of new Set(kinds)) {
        const count = offered.reduce((n,value,lane) => n + (kinds[lane] === kind ? value : 0), 0);
        summary.requests[kind] = summarizeTraffic(samples.filter(s => s.kind === kind), count, wall);
      }
      phases.push(summary); console.error(`Finished ${repair} ${name}`);
      return summary;
    }
    // Reverse the on/off order in the second pair to expose warmup and time trends.
    for (const on of [false, true, true, false]) {
      if (on && children.length === 0) { startWorkers(); await sleep(1000); }
      if (!on) await stopWorkers();
      await phase(on ? "monitor_on" : "monitor_off", 12);
      assert.equal((await jobs()).length, 0, "healthy monitoring opened a job");
    }
    startWorkers();
    await phase("healthy_history", 24);
    await setSlow(true);
    const spike = await phase("brief_spike", 3);
    const spikeCounters = spike.counters.filter((c: any) => c.query_id === output.target_query_id);
    output.spike_database_mean_ms = spikeCounters.reduce((n: number, c: any) => n + c.execution_ms, 0) / spikeCounters.reduce((n: number, c: any) => n + c.calls, 0);
    assert(output.spike_database_mean_ms > objective, "spike did not exceed the enrolled database objective");
    await setSlow(false);
    await phase("spike_recovery", 24);
    output.spike_jobs = await jobs();
    assert.equal(output.spike_jobs.length, 0, "brief spike opened an investigation");
    await setSlow(true);
    output.slowdown_at = new Date().toISOString();
    for (let i = 0; i < 6; i++) {
      await phase("sustained", 10);
      const queued = await jobs();
      if (queued.some(j => j.finished_at)) break;
    }
    output.jobs = await jobs();
    populateJobTimings(output);
    const afterRepair = await phase("after_repair", 12);
    const meanFor = (phase: any) => {
      const rows = phase.counters.filter((c: any) => c.query_id === output.target_query_id);
      return rows.reduce((n: number, c: any) => n + c.execution_ms, 0) / rows.reduce((n: number, c: any) => n + c.calls, 0);
    };
    output.before_database_mean_ms = meanFor(phases.find(p => p.name === "sustained"));
    output.after_database_mean_ms = meanFor(afterRepair);
    assert(output.after_database_mean_ms < objective && output.after_database_mean_ms < output.before_database_mean_ms, "ongoing target traffic did not recover below its objective");
    output.worker_failures = childFailures;
    output.worker_diagnostics = workerDiagnostics;
    output.detector_windows = childResults.flatMap(r => r.windows ?? []);
    output.process_results = childResults.filter(r => r.job_id);
    assert.equal(output.jobs.length, 1, "expected one sustained-slowdown job");
    const job = output.jobs[0];
    output.detection_seconds = (new Date(job.created_at).getTime() - Date.parse(output.slowdown_at))/1000;
    output.repair_seconds = (new Date(job.finished_at).getTime() - new Date(job.created_at).getTime())/1000;
    assert.equal(job.result?.outcome, "improved_tested_inputs", "sustained slowdown did not get a verified repair");
    assert.equal(job.result.proposal.model_call.requests, 1);
    output.repair_verified = true;
    output.traffic_errors = phases.reduce((sum, phase) => sum + Object.values(phase.requests).reduce((n: number, r: any) => n + r.errors, 0), 0);
    output.missed_requests = phases.reduce((sum, phase) => sum + Object.values(phase.requests).reduce((n: number, r: any) => n + r.missed, 0), 0);
    output.passed = output.traffic_errors === 0 && output.missed_requests === 0 && childFailures.length === 0;
  } catch (error: any) {
    output.passed = false; output.error = String(error.message).slice(0, 400);
    process.exitCode = 1;
  } finally {
    await stopWorkers();
    output.worker_failures = childFailures;
    output.worker_diagnostics = workerDiagnostics;
    output.detector_windows = childResults.flatMap(r => r.windows ?? []);
    output.process_results = childResults.filter(r => r.job_id);
    output.traffic_errors = phases.reduce((sum, phase) => sum + Object.values(phase.requests).reduce((n: number, r: any) => n + r.errors, 0), 0);
    output.missed_requests = phases.reduce((sum, phase) => sum + Object.values(phase.requests).reduce((n: number, r: any) => n + r.missed, 0), 0);
    if (fixture) { try { output.jobs = (await state.query("SELECT id,created_at,finished_at,result,attempts FROM pooltune_worker.investigations WHERE project_id=$1 ORDER BY id", [fixture.enrollment.project])).rows; } catch { output.job_readback_failed = true; } }
    populateJobTimings(output);
    await Promise.all(clients.map(c => c.end().catch(() => {})));
    if (fixture) {
      try {
        if (baselineIndexOID !== null) {
          const current = (await admin.query("SELECT to_regclass($1)::oid AS oid", [`${fixture.schema}.pooltune_lookup_idx`])).rows[0].oid;
          assert.equal(current, baselineIndexOID, "baseline index identity changed");
          await admin.query(`DROP INDEX "${fixture.schema}".pooltune_lookup_idx RESTRICT`);
        }
        await command(["-remove-consumer-fixture", fixture.id]);
        output.fixture_removed = (await admin.query("SELECT to_regnamespace($1) IS NULL AS gone", [fixture.schema])).rows[0].gone;
        assert.equal(output.fixture_removed, true, "fixture was not removed");
        output.public_indexes_after = (await admin.query("SELECT count(*)::int AS count,md5(coalesce(string_agg(indexdef,E'\\n' ORDER BY indexdef),'')) AS hash FROM pg_indexes WHERE schemaname='public'")).rows[0];
        assert.deepEqual(output.public_indexes_after, output.public_indexes_before);
      } catch { output.cleanup_failed = true; output.passed = false; }
    }
    await state.end().catch(() => {}); await admin.end().catch(() => {});
    rmSync(directory, { recursive: true, force: true });
    if (!output.passed) process.exitCode = 1;
    publish();
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(() => { console.error("Traffic run could not start"); process.exitCode = 1; });
}

export function populateJobTimings(output:any) {
 const jobs=output.jobs??[];if(!jobs.length)return;
 const first=jobs[0];const last=jobs.filter((j:any)=>j.finished_at).at(-1);
 if(first.finished_at&&Number.isFinite(first.result?.elapsed_ms))output.initial_queue_delay_seconds=(new Date(first.finished_at).getTime()-new Date(first.created_at).getTime()-first.result.elapsed_ms)/1000;
 const model=jobs.find((j:any)=>j.result?.proposal?.model_call)?.result.proposal.model_call;if(model)output.model_seconds=model.duration_ms/1000;
 const action=jobs.find((j:any)=>j.result?.cron?.action_wall_ms>0)?.result.cron;if(action)output.action_seconds=action.action_wall_ms/1000;
 const cycle=jobs.findLast((j:any)=>j.result?.cron?.cycle_wait_ms>0)?.result.cron;if(cycle)output.future_cycle_wait_seconds=cycle.cycle_wait_ms/1000;
 const periods=jobs.find((j:any)=>j.result?.cron?.before?.length===3)?.result.cron.before;if(periods)output.evidence_accumulation_seconds=(Date.parse(periods[2].quiet.end)-Date.parse(periods[0].cycle))/1000;
 if(output.slowdown_at)output.detection_seconds=(new Date(first.created_at).getTime()-Date.parse(output.slowdown_at))/1000;
 if(last){output.repair_seconds=(new Date(last.finished_at).getTime()-new Date(first.created_at).getTime())/1000;output.final_outcome=last.result?.outcome;}
}
export function counterDeltas(before:any[],after:any[]){
 return after.map(row=>{const old=before.find(b=>b.queryid===row.queryid);return {query_id:row.queryid,calls:row.calls-(old?.calls??0),execution_ms:row.total_exec_time-(old?.total_exec_time??0),blocks:row.blocks-(old?.blocks??0),temp_bytes:8192*(row.temp_blocks-(old?.temp_blocks??0))}}).filter(row=>row.calls>0);
}
export async function trafficRequest(client:pg.Client,sql:string,params:any[],check:(rows:any[])=>void,kind:string,scheduled:number):Promise<RequestSample>{
 let ok=true,code:string|undefined;
 try{const r=await client.query(sql,params);check(r.rows)}catch(e:any){ok=false;code=typeof e.code==='string'?e.code:'client_or_result_error'}
 return {kind,duration_ms:performance.now()-scheduled,ok,code};
}
