import {spawn,type ChildProcess} from 'node:child_process';
import {readFileSync,writeFileSync,mkdtempSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {setTimeout as sleep} from 'node:timers/promises';
import assert from 'node:assert/strict';
import pg from 'pg';
import {command,verifyCronModelAction,trafficInvestigatorMode,verifyTrafficProposal,counterDeltas,populateJobTimings,recordWorkerDiagnostic,summarizeTraffic,trafficRequest,vacuumObjective,type RequestSample} from './traffic.ts';

export function cronTargetDue(epochMS:number){const s=(epochMS/1000)%120;return s<20||(s>=30&&s<50)}
const iso=(n:number)=>new Date(n).toISOString();
const sqlISO=(value:string)=>`to_char(${value} AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`;

// 2 min calendar alignment + 6 min healthy/spike controls + 6 real 2 min
// periods + 4 min setup/calibration/final-observation allowance = 24 min.
// Cleanup has its separate existing 3 min deadline; the outer runner must
// allow both. This does not change worker, model, period or proof deadlines.
export const CRON_CAMPAIGN_BUDGET_MS=(2+6+6*2+4)*60_000;
export function checkCronCampaignBudget(start:number,now=performance.now()) {
 if(now-start>=CRON_CAMPAIGN_BUDGET_MS)throw Error('Cron wall-clock budget expired');
}

export async function runCronTraffic(){
 const investigatorMode=trafficInvestigatorMode(process.env.POOLTUNE_TRAFFIC_MODE);
 assert.equal(process.env.POOLTUNE_STAGING_PRIVATE_STATE,'1');
 assert(process.env.ANTHROPIC_API_KEY&&process.env.POOLTUNE_ANTHROPIC_MODEL);
 const config=new URL(process.env.POOLTUNE_LAB_TARGET_DSN!);assert.equal(config.hostname,'aws-0-us-west-2.pooler.supabase.com');assert.equal(config.port,'5432');assert.equal(config.pathname,'/postgres');config.search='';
 const options={connectionString:config.href,ssl:{ca:readFileSync(process.env.POOLTUNE_LAB_ROOKERY_CA_FILE!,'utf8'),rejectUnauthorized:true},connectionTimeoutMillis:5000,application_name:'pooltune-cron-traffic',statement_timeout:1500,query_timeout:2500};
 // Keep one sanitized record per owned client. The listener is installed before
 // connect and remains through cleanup; idle socket errors must not kill Node.
 const clientFailures:{client:string,code:string}[]=[];
 function client(name:string,config:pg.ClientConfig){
  const c=new pg.Client(config);let failed=false;
  c.on('error',(error:unknown)=>{
   if(failed)return;failed=true;
   const code=(error as {code?:unknown})?.code;
   clientFailures.push({client:name,code:typeof code==='string'&&/^[A-Z0-9_]{1,24}$/.test(code)?code:'client_error'});
  });return c;
 }
 function checkClients(){if(clientFailures.length)throw Error(`Cron client ${clientFailures[0].client} failed`)}
 async function phase<A,B>(operation:()=>Promise<A>,traffic:()=>Promise<B>):Promise<[A,B]>{
  checkClients();const result=await runCronPhase(operation,traffic);checkClients();return result;
 }
 const admin=client('admin',options),observer=client('observer',options),fault=client('fault',{...options,statement_timeout:22000,query_timeout:24000});
 const state=client('state',{connectionString:process.env.POOLTUNE_LAB_STATE_DSN,connectionTimeoutMillis:5000,statement_timeout:3000,query_timeout:5000});
 const clients:pg.Client[]=[];const directory=mkdtempSync(join(tmpdir(),'pooltune-cron-'));const enrollment=join(directory,'enrollment.json');
 const phases:any[]=[];const output:any={repair:'cron',phases,client_failures:clientFailures,limits:{procedure_holds:200,hold_ms:80,elapsed_guard_seconds:19,per_lock_timeout_ms:150,cycle_seconds:120,guard_margin_seconds:15,wall_budget_seconds:CRON_CAMPAIGN_BUDGET_MS/1000},target_calls_per_second:2,quiet_target_calls_per_second:0};
 let fixture:any,owned:any,worker:ChildProcess|undefined,consumer:ChildProcess|undefined;const allChildren:ChildProcess[]=[];const childResults:any[]=[],failures:number[]=[],diagnostics:string[]=[];
 const start=performance.now();const reportPath=process.env.POOLTUNE_CRON_REPORT_PATH??join(tmpdir(),`pooltune-cron-${Date.now()}.json`);output.report_path=reportPath;
 const progress=(s:string)=>console.error(`Cron: ${s}`);
 function launch(binary:string,args:string[]){const c=spawn(binary,args,{env:process.env});allChildren.push(c);let out='',err='';c.stdout.on('data',b=>{out+=b;let i;while((i=out.indexOf('\n'))>=0){const line=out.slice(0,i);out=out.slice(i+1);if(line.startsWith('{')){try{childResults.push(JSON.parse(line))}catch{failures.push(-2)}}}});c.stderr.on('data',b=>{err=(err+b).slice(-4096);let i;while((i=err.indexOf('\n'))>=0){recordWorkerDiagnostic(diagnostics,binary,err.slice(0,i));err=err.slice(i+1)}});c.on('error',()=>failures.push(-1));c.on('exit',code=>{if(code)failures.push(code);recordWorkerDiagnostic(diagnostics,binary,err)});return c}
 async function stop(c:ChildProcess|undefined){if(!c||c.exitCode!==null||c.signalCode!==null)return;await new Promise<void>(resolve=>{const kill=setTimeout(()=>c.kill('SIGKILL'),4000);c.once('exit',()=>{clearTimeout(kill);resolve()});c.kill('SIGTERM')})}
 const jobs=async()=>fixture?(await state.query('SELECT id,created_at,finished_at,result,attempts,not_before,parent_id FROM pooltune_worker.investigations WHERE project_id=$1 ORDER BY id',[fixture.enrollment.project])).rows:[];
 let dbNow=async()=>{checkClients();const now=Number((await admin.query('SELECT extract(epoch FROM clock_timestamp())*1000 AS ms')).rows[0].ms);checkClients();return now};
 let slice:((name:string,seconds:number,targetOn:boolean,cycle?:number)=>Promise<any>)|undefined;
 let traffic:((name:string,seconds:number,targetOn:boolean,cycle?:number)=>Promise<any>)|undefined;
 async function waitUntil(at:number){await waitCronBoundary(at,dbNow,async deadline=>{
  const seconds=Math.max(0,(deadline-performance.now())/1000);const result=await traffic!('healthy_between_periods',seconds,false);
  phases.push({name:'healthy_between_periods',seconds,counter_sampled:false,requests:result.requests});
 },()=>performance.now(),()=>{checkClients();checkCronCampaignBudget(start)})}
 function startWorkers(){checkClients();if(!worker||worker.exitCode!==null||worker.signalCode!==null)worker=launch('pooltune-worker',['-target','rookery','-enrollment',enrollment,'-interval','2s','-window','4s']);if(!consumer||consumer.exitCode!==null||consumer.signalCode!==null)consumer=launch('pooltune-consumer',['-target','rookery','-project',fixture.enrollment.project,'-fixture',fixture.id,'-mode',investigatorMode,'-cron-staging-fixture'])}
 try{
  await state.connect();await admin.connect();await observer.connect();await fault.connect();
  const fingerprint=`SELECT count(*)::int AS count,md5(coalesce(string_agg(indexdef,E'\n' ORDER BY indexdef),'')) AS hash FROM pg_indexes WHERE schemaname='public'`;
  output.public_indexes_before=(await admin.query(fingerprint)).rows[0];output.unrelated_jobs_before=(await admin.query('SELECT jobid,jobname,username,database,command,schedule,active FROM cron.job ORDER BY jobid')).rows;
  checkClients();fixture=JSON.parse(await command(['-create-consumer-fixture','-cron-fixture']));assert.match(fixture.id,/^[a-f0-9]{24}$/);assert.equal(fixture.schema,`pooltune_trial_${fixture.id}`);output.fixture=fixture;checkClients();
  owned=JSON.parse(await command(['-cron-action','inspect','-cron-id',fixture.id]));output.enrollment=owned;
  const schema=fixture.schema;const targetSQL=`SELECT "${schema}"."pooltune_cron_read"() AS id`;const healthySQL=`SELECT id FROM ONLY "${schema}"."healthy" WHERE id=$1`;const writeSQL=`UPDATE ONLY "${schema}"."healthy" SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id`;
  for(let i=0;i<4;i++){checkClients();const c=client(`traffic_${i}`,options);clients.push(c);await c.connect();await c.query("SET lock_timeout='250ms'")}
  await clients[0].query(targetSQL);await clients[2].query(healthySQL,[43]);await clients[3].query(writeSQL,[90001]);
  const ext=(await admin.query("SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'")).rows[0].nspname;assert.match(ext,/^[a-z_]+$/);
  const snapshot=async()=>{checkClients();const rows=(await admin.query(`SELECT extract(epoch FROM clock_timestamp())*1000 AS snapshot_ms,s.queryid::text AS queryid,s.dbid,s.userid,${sqlISO('pg_postmaster_start_time()')} AS boot,${sqlISO('i.stats_reset')} AS reset,${sqlISO('s.stats_since')} AS entry,s.calls::float8 AS calls,s.total_exec_time,s.shared_blks_hit::float8+s.shared_blks_read::float8 AS blocks,s.temp_blks_written::float8 AS temp_blocks,s.query FROM "${ext}".pg_stat_statements s CROSS JOIN "${ext}".pg_stat_statements_info i WHERE s.dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND s.userid=current_user::regrole::oid AND s.toplevel AND (s.query LIKE $1 OR s.query LIKE '%JOIN unnest($2::oid[], $3::bigint[]) AS enrolled%')`,[`%${schema}%`])).rows;checkClients();const time=Number(rows[0]?.snapshot_ms);assert(Number.isFinite(time),'snapshot clock/counters unavailable');return {time,rows}};
  const targetID=fixture.enrollment.queries[1].query_id,healthyID=fixture.enrollment.queries[0].query_id;output.target_query_id=targetID;
  const initial=await snapshot();const writeID=initial.rows.find(r=>r.query.startsWith(`UPDATE ONLY "${schema}"."healthy"`))?.queryid;assert(writeID,'write identity unavailable');
  const toCounter=(rows:any[],id:string)=>{const r=rows.find(r=>r.queryid===id);assert(r,'required statement counter missing');return {Database:r.dbid,Role:r.userid,Query:r.queryid,Boot:r.boot,Reset:r.reset,Entry:r.entry,Calls:r.calls,Blocks:r.blocks,ExecutionMS:r.total_exec_time}};
  const calibration:number[]=[];for(let i=0;i<10;i++){const b=await snapshot();const r=await clients[0].query(targetSQL);assert.equal(r.rows[0].id,43);const a=await snapshot();const d=counterDeltas(b.rows,a.rows).find(r=>r.query_id===targetID);assert.equal(d?.calls,1);calibration.push(d!.execution_ms)}
  output.healthy_calibration_ms=calibration;output.objective_ms=vacuumObjective(calibration);output.benefit_gates={minimum_fraction:.2,minimum_ms:5,noise_multiple:3,original_extra_fraction:.3,original_extra_ms:20};
  fixture.enrollment.queries[0].mean_execution_ms=3;fixture.enrollment.queries[1].mean_execution_ms=output.objective_ms;writeFileSync(enrollment,JSON.stringify(fixture.enrollment),{mode:0o600});
  const blocks=async()=>{const rows=(await observer.query(`SELECT ${sqlISO('clock_timestamp()')} AS at,r.runid::float8 AS run_id,r.jobid::float8 AS job_id,a.pid,${sqlISO('a.backend_start')} AS backend_start,w.pid AS waiting_pid,${sqlISO('w.backend_start')} AS waiting_start,w.query_id::text AS query_id FROM cron.job_run_details r JOIN pg_stat_activity a ON a.pid=r.job_pid JOIN pg_stat_activity w ON a.pid=ANY(pg_blocking_pids(w.pid)) WHERE r.jobid=$1 AND r.end_time IS NULL AND r.status='running' AND w.query_id=$2::bigint AND w.datid=$3 AND w.usesysid=$4`,[owned.target.id,targetID,fixture.enrollment.queries[1].database_oid,fixture.enrollment.queries[1].role_oid])).rows;return rows};
  traffic=async(name,seconds,targetOn,cycle)=>{
   output.current_phase=name;const samples:RequestSample[]=[];const offered=[0,0,0,0];const attributed:any[]=[];const anchor=performance.now();let collecting=true;
   await phase(async()=>{if(cycle!==undefined&&targetOn){while(collecting){checkClients();attributed.push(...await blocks());await sleep(50)}}},async()=>{try{
   const lanes=await Promise.allSettled(clients.map(async(c,lane)=>{const cadence=lane<2?1000:lane===2?250:500;const offset=lane===1?500:0;for(let tick=0;offset+tick*cadence<seconds*1000;tick++){checkClients();if(lane<2&&!targetOn)continue;offered[lane]++;const scheduled=anchor+offset+tick*cadence;const wait=scheduled-performance.now();if(wait>0)await sleep(wait);checkClients();if(performance.now()-scheduled>cadence)continue;const kind=lane<2?'target':lane===2?'healthy':'write';const id=kind==='write'?90001+tick%10:43;samples.push(await trafficRequest(c,lane<2?targetSQL:lane===2?healthySQL:writeSQL,lane<2?[]:[id],rows=>{assert.equal(rows.length,1);assert.equal(rows[0].id,id)},kind,scheduled))}}));
   const failed=lanes.find(r=>r.status==='rejected');if(failed?.status==='rejected')throw failed.reason;
   const remain=seconds*1000-(performance.now()-anchor);if(remain>0)await sleep(remain);
   }finally{collecting=false}});
   const requests:any={};const kinds=['target','healthy','write'];for(const [i,kind]of kinds.entries()){const n=i===0?offered[0]+offered[1]:offered[i+1];requests[kind]=summarizeTraffic(samples.filter(s=>s.kind===kind),n,seconds)}
   return {requests,attributed};
  };
  slice=async(name,seconds,targetOn,cycle)=>{
   output.current_phase=name;const before=await snapshot();const {requests,attributed}=await traffic!(name,seconds,targetOn,cycle);const after=await snapshot();
   phases.push({name,start:iso(before.time),end:iso(after.time),seconds,counters:counterDeltas(before.rows,after.rows),requests});
   const metric=(id:string,kind:string)=>({before:toCounter(before.rows,id),after:toCounter(after.rows,id),offered:requests[kind].offered,errors:requests[kind].errors,missed:requests[kind].missed});
   const window={start:iso(before.time),end:iso(after.time),target:metric(targetID,'target'),healthy:metric(healthyID,'healthy'),write:metric(writeID,'write'),blocks:attributed};
   if(name.startsWith('original_')||name.startsWith('future_'))(output.period_windows??=[]).push({phase:name,cycle:output.current_cycle,window});
   return window;
  };
  // Two matched healthy comparisons with reversed monitoring order. The witness
  // stays active for both, while the target remains inactive until the fault.
  checkClients();await command(['-cron-action','witness','-cron-id',fixture.id]);
  const firstClean=Math.ceil((await dbNow()+1000)/120000)*120000;
  for(const [i,on]of [false,true,true,false].entries()){const at=firstClean+Math.floor(i/2)*120000+(i%2)*30000;await waitUntil(at);if(on)startWorkers();else{await stop(worker);await stop(consumer);worker=consumer=undefined}await slice(on?'monitor_on':'monitor_off',20,true);assert.equal((await jobs()).length,0,'healthy monitoring opened a job')}
  startWorkers();
  const spikeAt=firstClean+240000;await waitUntil(spikeAt);progress('manual 60-hold brief spike, not a scheduled run');const [,sw]=await phase(()=>fault.query(`CALL "${schema}"."pooltune_cron_hold"(60)`),()=>slice!('manual_brief_spike',20,true));output.spike_database_mean_ms=(sw.target.after.ExecutionMS-sw.target.before.ExecutionMS)/(sw.target.after.Calls-sw.target.before.Calls);assert(output.spike_database_mean_ms>output.objective_ms,'manual spike did not breach fixed objective');
  await waitUntil(spikeAt+30000);await slice('spike_recovery',20,true);await waitUntil(spikeAt+65000);output.spike_jobs=await jobs();assert.equal(output.spike_jobs.length,0,'brief spike opened an investigation');
  // Activation accepts an already active identical witness, but target activation
  // remains inactive until this recorded odd-minute interval.
  const [activated]=await phase(()=>command(['-cron-action','activate','-cron-id',fixture.id]),()=>slice!('activation_healthy',5,false));owned=JSON.parse(activated);output.activation=owned;const firstCycle=Date.parse(owned.first_cycle);output.slowdown_at=iso(firstCycle);
  let restarted=false;
  for(let i=0;i<6;i++){
   const cycle=firstCycle+i*120000;output.current_cycle=iso(cycle);progress(`${i<3?'original':'future'} actual cycle ${i%3+1}/3`);
   const period=await captureCronPeriod(cycle,i,waitUntil,slice);(output.periods??=[]).push(period);output.current_phase='record_period';
   const file=join(directory,`period-${i}.json`);writeFileSync(file,JSON.stringify(period),{mode:0o600});
   await phase(()=>command(['-cron-action','record','-cron-id',fixture.id,'-cron-period',file]),async()=>slice!('cycle_end_healthy',Math.max(.1,(cycle+100000-await dbNow())/1000),false));
   let queued=await jobs();
   if(i===2){
    output.current_phase='schedule_acknowledgement';
    queued=await waitCronAcknowledgement(cycle,owned.witness.id,jobs,dbNow,async seconds=>{
     const observed=await traffic!('acknowledgement_healthy',seconds,false);
     phases.push({name:'acknowledgement_healthy',seconds,counter_sampled:false,requests:observed.requests});
    });
    output.current_phase='schedule_acknowledgement';
   }
   output.jobs=queued;populateJobTimings(output);
   if(i===2){const pending=queued.find(j=>j.result?.outcome==='awaiting_verification');assert(pending,'no acknowledged schedule action after full original periods');verifyTrafficProposal(pending.result.proposal,investigatorMode);output.model_seconds=pending.result.proposal.model_call.duration_ms/1000;output.action_seconds=pending.result.cron.action_wall_ms/1000;output.action_at=pending.result.cron.commit_at;const child=queued.find(j=>!j.finished_at);assert(child&&child.parent_id===pending.id&&new Date(child.not_before).getTime()>await dbNow(),'missing future verification child');await stop(consumer);consumer=undefined;startWorkers();const afterRestart=await jobs();assert(afterRestart.some(j=>j.id===child.id&&j.parent_id===child.parent_id&&!j.finished_at),'restart lost pending child');restarted=true;output.consumer_restart={job_id:child.id,parent_id:child.parent_id,not_before:child.not_before,preserved:true}}
  }
  await waitUntil(firstCycle+12*60000+8000);output.current_phase='final_verification';
  output.jobs=await waitCronFinalResult(firstCycle,{id:output.consumer_restart.job_id,parent_id:output.consumer_restart.parent_id},jobs,dbNow,async seconds=>{
   const observed=await traffic!('final_verification_healthy',seconds,false);
   phases.push({name:'final_verification_healthy',seconds,counter_sampled:false,requests:observed.requests});
  },start+CRON_CAMPAIGN_BUDGET_MS);output.current_phase='final_verification';
  populateJobTimings(output);const final=output.jobs.find((j:any)=>j.id===output.consumer_restart.job_id&&j.parent_id===output.consumer_restart.parent_id&&j.finished_at);output.future_cycle_wait_seconds=final?.result?.cron?.cycle_wait_ms/1000;assert(restarted);assert.equal(final?.result?.outcome,'improved_tested_inputs','Cron follow-up did not verify all future work and traffic');verifyCronModelAction(output.jobs,investigatorMode);output.repair_verified=true;output.passed=true;
 }catch(e:any){output.passed=false;output.failed_phase=e.command_failure?.operation??output.current_phase;output.command_failure=e.command_failure;output.error=clientFailures.length?`Cron client ${clientFailures[0].client} failed`:String(e.message).slice(0,400);process.exitCode=1}
 finally{
  await Promise.all(allChildren.map(stop));output.worker_failures=failures;output.worker_diagnostics=diagnostics;output.detector_windows=childResults.flatMap(r=>r.windows??[]);output.process_results=childResults.filter(r=>r.job_id);
  try{output.jobs=await jobs();populateJobTimings(output)}catch{output.job_readback_failed=true}
  output.traffic_errors=phases.reduce((n,p)=>n+Object.values(p.requests).reduce((v:number,r:any)=>v+r.errors,0),0);output.missed_requests=phases.reduce((n,p)=>n+Object.values(p.requests).reduce((v:number,r:any)=>v+r.missed,0),0);
  await Promise.all(clients.map(c=>c.end().catch(()=>{})));
  if(fixture){
   progress('waiting for safe target cleanup; stopping owned diagnostic witness separately');
   await cleanupCronTraffic(output,fixture.id,performance.now()+180000,async timeoutMS=>{
    await command(['-remove-consumer-fixture',fixture.id],timeoutMS);
    output.fixture_removed=(await admin.query('SELECT to_regnamespace($1) IS NULL AS gone',[fixture.schema])).rows[0].gone;
    output.owned_jobs_remaining=(await admin.query('SELECT count(*)::int AS n FROM cron.job WHERE jobname=ANY($1::text[])',[['pooltune_target_'+fixture.id,'pooltune_witness_'+fixture.id]])).rows[0].n;
    assert(output.fixture_removed&&output.owned_jobs_remaining===0);
    output.unrelated_jobs_after=(await admin.query('SELECT jobid,jobname,username,database,command,schedule,active FROM cron.job ORDER BY jobid')).rows;
    assert.deepEqual(output.unrelated_jobs_after,output.unrelated_jobs_before);
    output.public_indexes_after=(await admin.query("SELECT count(*)::int AS count,md5(coalesce(string_agg(indexdef,E'\\n' ORDER BY indexdef),'')) AS hash FROM pg_indexes WHERE schemaname='public'")).rows[0];
    assert.deepEqual(output.public_indexes_after,output.public_indexes_before);
   });
  }
  await Promise.all([state.end().catch(()=>{}),admin.end().catch(()=>{}),observer.end().catch(()=>{}),fault.end().catch(()=>{})]);rmSync(directory,{recursive:true,force:true});output.total_seconds=(performance.now()-start)/1000;saveCronTrafficReport(output,reportPath,!!fixture);console.log(JSON.stringify(output));
 }
}

// Start both operations with rejection handlers attached in the same turn, then
// wait for both to settle before any caller can close their shared clients.
export async function runCronPhase<A,B>(operation:()=>Promise<A>,traffic:()=>Promise<B>):Promise<[A,B]> {
 const results=await Promise.allSettled([Promise.resolve().then(operation),Promise.resolve().then(traffic)]);
 const [action,requests]=results;
 if(action.status==='rejected')throw action.reason;
 if(requests.status==='rejected')throw requests.reason;
 return [action.value,requests.value];
}

// Removal and preservation readback are separate facts. Only the complete
// callback can establish cleanup success, regardless of how the loop exits.
export async function cleanupCronTraffic(output:any,fixtureID:string,deadline:number,removeAndVerify:(timeoutMS:number)=>Promise<void>) {
 output.cleanup_verified=false;
 for(let attempt=0;attempt<35&&performance.now()<deadline;attempt++) {
  try {
   await removeAndVerify(Math.min(15000,deadline-performance.now()));
   output.cleanup_verified=true;
   break;
  } catch(error:unknown) {
   output.cleanup_last_error=String(error instanceof Error ? error.message : error).slice(0,400);
   const remaining=deadline-performance.now();
   if(remaining>0&&attempt<34)await sleep(Math.min(2000,remaining));
  }
 }
 if(!output.cleanup_verified) {
  output.cleanup_failed=true;
  output.recovery_fixture_id=fixtureID;
  output.passed=false;
 }
}

export function saveCronTrafficReport(output:any,reportPath:string,requiresCleanup:boolean) {
 output.passed=!!output.passed&&(!requiresCleanup||output.cleanup_verified===true)&&!output.cleanup_failed&&!output.traffic_errors&&!output.missed_requests&&!output.worker_failures?.length&&!output.client_failures?.length;
 if(!output.passed)process.exitCode=1;
 writeFileSync(reportPath,JSON.stringify(output,null,2),{mode:0o600});
}

// The database observation is a lower bound at receipt. Anchoring from receipt
// deliberately waits through reply latency, never backdates a server snapshot.
// Between-period traffic has no counter snapshots that could extend the wait.
export async function waitCronBoundary(at:number,clock:()=>Promise<number>,healthyUntil:(deadline:number)=>Promise<void>,now=()=>performance.now(),check=()=>{}) {
 check();const observed=await clock();assert(Number.isFinite(observed),'Cron clock unavailable');
 const deadline=now()+Math.max(0,at-observed);
 while(now()<deadline){check();await healthyUntil(Math.min(deadline,now()+10000))}
 check();
}

// Mirror the existing Go admission limits only to report the failing window
// locally. Actual database timestamps and full selected evidence stay intact.
export function assertCronWindowTiming(window:{start:string,end:string},expected:number) {
 const start=Date.parse(window.start)-expected,end=Date.parse(window.end)-expected;
 if(!Number.isFinite(start)||!Number.isFinite(end)||start<0||start>1000||end<20000||end>21000)
  throw Error(`Cron period outside required window: start_offset_ms=${start} end_offset_ms=${end}`);
}

export async function captureCronPeriod<W extends {start: string; end: string; blocks: unknown[]}>(
  cycle: number, index: number, waitUntil: (at: number) => Promise<void>,
  measure: (name: string, seconds: number, targetOn: boolean, observeCycle?: number) => Promise<W>,
) {
  if (!Number.isSafeInteger(cycle) || cycle <= 0 || cycle % 120_000 !== 0 || !Number.isInteger(index) || index < 0 || index > 5) {
    throw Error('Cron period requires one of the six aligned cycles');
  }
  const phase = index < 3 ? 'original' : 'future';
  await waitUntil(cycle);
  const {blocks, ...busy} = await measure(`${phase}_busy`, 20, true, cycle);
  assertCronWindowTiming(busy, cycle);
  await waitUntil(cycle + 30_000);
  const {blocks: _controlBlocks, ...control} = await measure(`${phase}_control`, 20, true);
  assertCronWindowTiming(control, cycle + 30_000);
  await waitUntil(cycle + 60_000);
  const {blocks: _quietBlocks, ...quiet} = await measure(`${phase}_quiet`, 20, false);
  assertCronWindowTiming(quiet, cycle + 60_000);
  return {cycle: iso(cycle), busy, control, quiet, blocks};
}

// Go's action/proof deadline stays at +105s. Only publication observation gets
// five additional seconds, leaving ten seconds before the next busy boundary.
export async function waitCronAcknowledgement(cycle:number,witnessID:number,readJobs:()=>Promise<any[]>,clock:()=>Promise<number>,healthy:(seconds:number)=>Promise<void>,monotonic=()=>performance.now()):Promise<any[]> {
 const actionDeadline=cycle+105000,observationDeadline=cycle+110000;
 let now=await clock();const localDeadline=monotonic()+Math.max(0,observationDeadline-now);
 for(;;){
  if(!Number.isFinite(now)||now>=observationDeadline||monotonic()>=localDeadline)throw Error('Cron acknowledgement publication deadline expired');
  const [queued]=await runCronPhase(readJobs,()=>healthy(Math.min(.5,(observationDeadline-now)/1000)));
  const observed=await clock();
  if(!Number.isFinite(observed)||observed<now)throw Error('Cron acknowledgement clock ambiguous');
  now=observed;
  if(now>=observationDeadline||monotonic()>=localDeadline)throw Error('Cron acknowledgement publication deadline expired');
  const latest=queued.filter(j=>j.finished_at).at(-1);
  if(latest&&!['awaiting_evidence','awaiting_verification'].includes(latest.result?.outcome))throw Error('Cron action declined or failed before acknowledgement');
  const pending=queued.find(j=>j.finished_at&&j.result?.outcome==='awaiting_verification');
  if(!pending)continue;
  const report=pending.result.cron,commit=Date.parse(report?.commit_at);
  if(!Number.isFinite(commit)||commit<=cycle+60000||commit>=actionDeadline||commit>now)throw Error('Cron acknowledged action has missing or late commit proof');
  const ids=new Set<number>(),starts=new Set<number>();
  for(const run of report?.post_witness??[]){
   const start=Date.parse(run.start),end=Date.parse(run.end);
   if(run.job_id===witnessID&&Number.isSafeInteger(run.id)&&run.id>0&&run.status==='succeeded'&&Number.isFinite(start)&&Number.isFinite(end)&&start>commit&&end>=start&&end<actionDeadline&&end<=now){ids.add(run.id);starts.add(start)}
  }
  if(ids.size<2||starts.size<2)throw Error('Cron acknowledged action lacks two on-time post-commit witnesses');
  return queued;
 }
}

// A pending verification claim is not a terminal outcome. Observation remains
// bounded by both the target clock and the original overall monotonic budget.
export async function waitCronFinalResult(firstCycle:number,expectedChild:{id:unknown,parent_id:unknown},readJobs:()=>Promise<any[]>,clock:()=>Promise<number>,healthy:(seconds:number)=>Promise<void>,workDeadline:number,monotonic=()=>performance.now()):Promise<any[]> {
 const observationDeadline=firstCycle+12*60000+35000;
 let now=await clock();const localDeadline=Math.min(workDeadline,monotonic()+Math.max(0,observationDeadline-now));
 for(;;){
  if(!Number.isFinite(now)||now>=observationDeadline||monotonic()>=localDeadline)throw Error('Cron final verification observation deadline or work budget expired');
  const seconds=Math.min(.5,(observationDeadline-now)/1000,(localDeadline-monotonic())/1000);
  const [queued]=await runCronPhase(readJobs,()=>healthy(seconds));
  const observed=await clock();
  if(!Number.isFinite(observed)||observed<now)throw Error('Cron final verification clock ambiguous');
  now=observed;
  if(now>=observationDeadline||monotonic()>=localDeadline)throw Error('Cron final verification observation deadline or work budget expired');
  const child=queued.find(j=>j.id===expectedChild.id&&j.parent_id===expectedChild.parent_id),pending=queued.filter(j=>!j.finished_at);
  if(!child)throw Error('Cron final verification child missing or changed');
  if(child.finished_at){
   if(child.result?.outcome==='improved_tested_inputs'&&pending.length===0)return queued;
   throw Error('Cron final verification has no improved terminal result');
  }
  const parent=queued.find(j=>j.id===expectedChild.parent_id&&j.finished_at);
  if(parent?.result?.outcome!=='awaiting_verification')throw Error('Cron final verification has no improved terminal result');
  if(pending.length!==1||pending[0].id!==expectedChild.id)throw Error('Cron final verification child missing or changed');
 }
}
