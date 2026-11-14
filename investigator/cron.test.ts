import {test} from 'node:test';
import assert from 'node:assert/strict';
import {buildGraph} from './graph.ts';
import {scriptedInvestigator} from './model.ts';

function evidence():any {
 const epoch='2026-11-14T12:00:00Z';
 const metric=(mean:number,n:number)=>({before:{Database:1,Role:2,Query:"3",Boot:epoch,Reset:epoch,Entry:epoch,Calls:0,Blocks:0,ExecutionMS:0},after:{Database:1,Role:2,Query:"3",Boot:epoch,Reset:epoch,Entry:epoch,Calls:n,Blocks:0,ExecutionMS:mean*n},offered:n,errors:0,missed:0});
 return {run_id:'cron',observed_at:epoch,decision:'investigate',table_oid:42,column_number:1,lookup_column:'lookup',covering_index:false,plan:{node:'Result',examined_rows:0,returned_rows:0,shared_blocks:0},cron:{job_id:9,original_schedule:'*/2 * * * *',alternate_schedule:'1-59/2 * * * *',identity_verified:true,required_runs_per_cycle:1,cycle_seconds:120,deadline_seconds:120,periods:[0,1,2].map(i=>({cycle:new Date(Date.parse(epoch)+120000*i).toISOString(),busy:{start:epoch,end:epoch,target:metric(42,40),healthy:metric(1,80),write:metric(1,40)},control:{start:epoch,end:epoch,target:metric(2,40),healthy:metric(1,80),write:metric(1,40)},quiet:{start:epoch,end:epoch,target:metric(0,0),healthy:metric(1,80),write:metric(1,40)},blocks:[{at:epoch,run_id:i+1,job_id:9,pid:42,backend_start:epoch,waiting_pid:99,waiting_start:epoch,query_id:"3"}]}))}};
}
test('Cron cause reaches one model request; incomplete or unapproved scheduling is declined',async()=>{
 let calls=0;const graph=buildGraph(async(read,e)=>{calls++;return scriptedInvestigator(read,e)});
 const result=await graph.invoke({evidence:evidence()});assert.equal(result.proposal?.repair,'cron');assert.equal(calls,1);
 for(const bad of ['few','overlap','window','frequency','small','healthy']){const e=evidence();if(bad==='few')e.cron.periods.pop();if(bad==='overlap')e.cron.periods[0].blocks=[];if(bad==='window')e.cron.alternate_schedule='* * * * *';if(bad==='frequency')e.cron.required_runs_per_cycle=2;if(bad==='small')e.cron.periods[0].busy.target.after.ExecutionMS=400;if(bad==='healthy')e.decision='no_change';assert.equal((await graph.invoke({evidence:e})).proposal?.outcome,'no_change')};assert.equal(calls,1);
});
test('Cron rejects a mixed cause and propagates real provider failure',async()=>{
 const e=evidence();e.vacuum={heap_fetches:1000,scan_rows:1000,conditions_clear:true};await assert.rejects(buildGraph(scriptedInvestigator).invoke({evidence:e}));await assert.rejects(buildGraph(async()=>{throw Error('provider unavailable')}).invoke({evidence:evidence()}),/provider unavailable/);
});

test('Cron target demand stays absent in odd minutes and unchanged after repair',async()=>{
 const {cronTargetDue}=await import('./cron-traffic.ts');
 for(const [seconds,want] of [[0,true],[19,true],[20,false],[29,false],[30,true],[49,true],[50,false],[60,false],[79,false],[119,false],[120,true]] as const)assert.equal(cronTargetDue(seconds*1000),want);
});

// Run in a separate Node process with fatal unhandled-rejection behavior. A
// deferred rejection handler must prevent the final report/cleanup assertions.
import {spawnSync} from 'node:child_process';
import {mkdtempSync,readFileSync,existsSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';

for(const phase of ['manual_spike','activation','record','traffic']){
 test(`early ${phase} rejection settles parallel work, cleans up, and saves failure`,()=>{
  const dir=mkdtempSync(join(tmpdir(),'cron-rejection-'));
  try{
   const moduleURL=new URL('./cron-traffic.ts',import.meta.url).href;
   const script=`
    import {runCronPhase,cleanupCronTraffic,saveCronTrafficReport} from ${JSON.stringify(moduleURL)};
    import {writeFileSync,existsSync} from 'node:fs';
    import {setTimeout as sleep} from 'node:timers/promises';
    const dir=${JSON.stringify(dir)},phase=${JSON.stringify(phase)};
    const output={passed:true,phase,worker_failures:[],traffic_errors:0,missed_requests:0};
    try {
      const late=async()=>{await sleep(40);writeFileSync(dir+'/traffic-settled','yes');return 'settled'};
      const failed=()=>Promise.reject(new Error(phase+' refused'));
      await runCronPhase(phase==='traffic'?late:failed,phase==='traffic'?failed:late);
    } catch(error) {output.passed=false;output.error=error.message}
    finally {
      await cleanupCronTraffic(output,'owned-fixture',performance.now()+1000,async()=>{
        if(!existsSync(dir+'/traffic-settled'))throw Error('cleanup raced live traffic');
        writeFileSync(dir+'/cleanup-attempted','yes');output.fixture_removed=true;
      });
      saveCronTrafficReport(output,dir+'/report.json',true);
    }`;
   const child=spawnSync(process.execPath,['--unhandled-rejections=strict','--input-type=module','-e',script],{encoding:'utf8',timeout:10000});
   assert.equal(child.status,1,child.stderr);
   assert(existsSync(join(dir,'report.json')),`failure report missing: ${child.stderr}`);
   const report=JSON.parse(readFileSync(join(dir,'report.json'),'utf8'));
   assert.equal(report.passed,false);assert.match(report.error,new RegExp(phase+' refused'));
   assert.equal(report.cleanup_verified,true);assert(existsSync(join(dir,'cleanup-attempted')));
  }finally{rmSync(dir,{recursive:true,force:true})}
 });
}

test('cleanup deadline after removal cannot credit incomplete preservation readback',()=>{
 const dir=mkdtempSync(join(tmpdir(),'cron-cleanup-'));
 try{
  const script=`
   import {cleanupCronTraffic,saveCronTrafficReport} from ${JSON.stringify(new URL('./cron-traffic.ts',import.meta.url).href)};
   import {setTimeout as sleep} from 'node:timers/promises';
   const output={passed:true,worker_failures:[],traffic_errors:0,missed_requests:0};let attempts=0;
   await cleanupCronTraffic(output,'owned-fixture',performance.now()+15,async()=>{
      attempts++;output.fixture_removed=true;output.owned_jobs_remaining=0;
      await sleep(25);throw Error('unrelated job readback unavailable '+ 'x'.repeat(600));
   });
   output.cleanup_attempts=attempts;
   saveCronTrafficReport(output,${JSON.stringify(join(dir,'report.json'))},true);`;
  const child=spawnSync(process.execPath,['--unhandled-rejections=strict','--input-type=module','-e',script],{encoding:'utf8',timeout:10000});
  assert.equal(child.status,1,child.stderr);assert(existsSync(join(dir,'report.json')),child.stderr);
  const report=JSON.parse(readFileSync(join(dir,'report.json'),'utf8'));
  assert.equal(report.fixture_removed,true);assert.equal(report.cleanup_verified,false);assert.equal(report.cleanup_failed,true);assert.equal(report.passed,false);
  assert.equal(report.recovery_fixture_id,'owned-fixture');assert.equal(report.cleanup_attempts,1);assert.match(report.cleanup_last_error,/unrelated job readback unavailable/);assert(report.cleanup_last_error.length<=400);
 }finally{rmSync(dir,{recursive:true,force:true})}
});

test('a latched client failure vetoes success even after verified cleanup',()=>{
 const dir=mkdtempSync(join(tmpdir(),'cron-late-failure-'));
 try{
  const script=`
   import {saveCronTrafficReport} from ${JSON.stringify(new URL('./cron-traffic.ts',import.meta.url).href)};
   saveCronTrafficReport({passed:true,cleanup_verified:true,client_failures:[{client:'state',code:'ECONNRESET'}]},${JSON.stringify(join(dir,'report.json'))},true);
  `;
  const child=spawnSync(process.execPath,['--unhandled-rejections=strict','--input-type=module','-e',script],{encoding:'utf8',timeout:10000});
  assert.equal(child.status,1,child.stderr);
  const report=JSON.parse(readFileSync(join(dir,'report.json'),'utf8'));
  assert.equal(report.passed,false);assert.equal(report.cleanup_verified,true);
 }finally{rmSync(dir,{recursive:true,force:true})}
});

// Exercise the complete driver with only external DB/command transport replaced.
// Real pg Client error dispatch, phase settlement, cleanup and report code remain.
for(const role of ['admin','observer','fault','state','traffic_0']){
 test(`idle ${role} client failure saves a sanitized failed run after settled cleanup`,()=>{
  const dir=mkdtempSync(join(tmpdir(),'cron-idle-'));
  try{
   const driverURL=new URL('./cron-traffic.ts',import.meta.url).href;
   const trafficURL=new URL('./traffic.ts',import.meta.url).href;
   const script=`
    import pg from 'pg';
    import {registerHooks} from 'node:module';
    import {writeFileSync} from 'node:fs';
    import {setTimeout as sleep} from 'node:timers/promises';
    import * as shared from ${JSON.stringify(trafficURL)};
    const dir=${JSON.stringify(dir)}, failedRole=${JSON.stringify(role)};
    const id='0123456789abcdef01234567',schema='pooltune_trial_'+id;
    const roles=['admin','observer','fault','state','traffic_0','traffic_1','traffic_2','traffic_3'];
    const clients=[],connected=[],closed=[];let armed=false,active=0,calls=0,cleaned=false,clockOffset=0;
    const realNow=performance.now.bind(performance);performance.now=()=>realNow()+clockOffset;
    const fixture={id,schema,enrollment:{project:'idle-error',queries:[{database_oid:1,role_oid:2,query_id:'11'},{database_oid:1,role_oid:2,query_id:'12'}]}};
    class Client extends pg.Client {
     constructor(options){super(options);this.role=roles[clients.length];clients.push(this)}
     async connect(){connected.push({role:this.role,listeners:this.listenerCount('error')})}
     async end(){if(active)writeFileSync(dir+'/cleanup-raced-traffic','yes');closed.push(this.role)}
     async query(sql,params=[]){
      if(!this._queryable){if(cleaned&&this.role==='admin')clockOffset=180001;throw Error('client is not queryable')}
      if(sql.includes('extract(epoch')&&!sql.includes('pg_stat_statements s'))return {rows:[{ms:118800}]};
      if(sql.includes('string_agg(indexdef'))return {rows:[{count:0,hash:'unchanged'}]};
      if(sql.includes('FROM cron.job ORDER BY'))return {rows:[]};
      if(sql.includes('FROM pg_extension'))return {rows:[{nspname:'public'}]};
      if(sql.includes('pg_stat_statements s'))return {rows:[['11','SELECT healthy'],['12','SELECT target'],['13','UPDATE ONLY "'+schema+'"."healthy"']].map(([queryid,query])=>({snapshot_ms:118800,queryid,query,dbid:1,userid:2,boot:'2026-11-14T12:00:00Z',reset:'2026-11-14T12:00:00Z',entry:'2026-11-14T12:00:00Z',calls:queryid==='12'?calls:10,total_exec_time:queryid==='12'?calls:10,blocks:0,temp_blocks:0}))};
      if(sql.includes('pooltune_worker.investigations'))return {rows:[]};
      if(sql.includes('to_regnamespace'))return {rows:[{gone:true}]};
      if(sql.includes('FROM cron.job WHERE'))return {rows:[{n:0}]};
      if(sql.startsWith('SET '))return {rows:[]};
      if(sql.includes('pooltune_cron_read'))calls++;
      if(armed){active++;try{await sleep(90)}finally{active--;writeFileSync(dir+'/traffic-settled','yes')}}
      return {rows:[{id:params[0]??43}]};
     }
    }
    const command=async args=>{
     if(args.includes('-create-consumer-fixture'))return JSON.stringify(fixture);
     if(args.includes('inspect'))return JSON.stringify({target:{id:1}});
     if(args.includes('witness')){
      armed=true;setTimeout(()=>{
       const client=clients.find(c=>c.role===failedRole);
       for(let i=0;i<50;i++)client._handleErrorEvent(Object.assign(new Error('secret postgresql://user:password@host/private '+ 'x'.repeat(1000)),{code:'ECONNRESET'}));
      },20);return '{}';
     }
     if(args.includes('-remove-consumer-fixture')){if(active)throw Error('cleanup raced traffic');cleaned=true;writeFileSync(dir+'/cleanup-attempted','yes');return '{}'}
     throw Error('unexpected command after disconnect');
    };
    globalThis.boundary={pg:{Client},shared:{...shared,command}};
    registerHooks({resolve(specifier,context,next){
     if(context.parentURL===${JSON.stringify(driverURL)}&&specifier==='pg')return {url:'test:pg',shortCircuit:true};
     if(context.parentURL===${JSON.stringify(driverURL)}&&specifier==='./traffic.ts')return {url:'test:traffic',shortCircuit:true};
     return next(specifier,context);
    },load(url,context,next){
     if(url==='test:pg')return {format:'module',source:'export default globalThis.boundary.pg',shortCircuit:true};
     if(url==='test:traffic')return {format:'module',source:'export const {command,counterDeltas,populateJobTimings,recordWorkerDiagnostic,summarizeTraffic,trafficRequest,vacuumObjective}=globalThis.boundary.shared',shortCircuit:true};
     return next(url,context);
    }});
    writeFileSync(dir+'/ca.pem','test boundary certificate');
    Object.assign(process.env,{POOLTUNE_STAGING_PRIVATE_STATE:'1',ANTHROPIC_API_KEY:'unused-test',POOLTUNE_ANTHROPIC_MODEL:'unused-test',POOLTUNE_LAB_TARGET_DSN:'postgresql://unused:unused@aws-0-us-west-2.pooler.supabase.com:5432/postgres',POOLTUNE_LAB_STATE_DSN:'postgresql://unused:unused@localhost:5432/test',POOLTUNE_LAB_ROOKERY_CA_FILE:dir+'/ca.pem',POOLTUNE_CRON_REPORT_PATH:dir+'/report.json'});
    const {runCronTraffic}=await import(${JSON.stringify(driverURL)});
    await runCronTraffic();
    writeFileSync(dir+'/lifecycle.json',JSON.stringify({connected,closed,cleaned}));
   `;
   const child=spawnSync(process.execPath,['--unhandled-rejections=strict','--input-type=module','-e',script],{encoding:'utf8',timeout:10000,cwd:new URL('.',import.meta.url)});
   assert.equal(child.status,1,child.stderr);
   assert(existsSync(join(dir,'report.json')),`failure report missing: ${child.stderr}`);
   const raw=readFileSync(join(dir,'report.json'),'utf8'),report=JSON.parse(raw);
   assert.equal(report.passed,false);assert.match(report.error,/client.*failed/);
   assert.deepEqual(report.client_failures,[{client:role,code:'ECONNRESET'}]);
   assert(!raw.includes('password')&&!raw.includes('secret postgresql'));
   assert.equal(report.fixture.id,'0123456789abcdef01234567');
   if(role==='admin'){
    assert.equal(report.cleanup_verified,false);assert.equal(report.cleanup_failed,true);
    assert.equal(report.recovery_fixture_id,'0123456789abcdef01234567');
   }else assert.equal(report.cleanup_verified,true);
   if(role==='state')assert.equal(report.job_readback_failed,true);
   assert(existsSync(join(dir,'cleanup-attempted')));assert(existsSync(join(dir,'traffic-settled')));
   assert(!existsSync(join(dir,'cleanup-raced-traffic')));
   const lifecycle=JSON.parse(readFileSync(join(dir,'lifecycle.json'),'utf8'));
   assert.equal(lifecycle.connected.length,8);assert(lifecycle.connected.every((c:any)=>c.listeners>0));assert.equal(lifecycle.closed.length,8);
  }finally{rmSync(dir,{recursive:true,force:true})}
 });
}

// Virtual transport time is explicit. Snapshots keep their actual server time;
// neither the scheduled boundary nor a client timestamp substitutes for it.
test('Cron boundary alignment absorbs transport overhead without shifting measured windows',async()=>{
 const {waitCronBoundary,assertCronWindowTiming}=await import('./cron-traffic.ts');
 let now=90000;const requests:number[]=[];
 const clock=async()=>{now+=150;const stamp=now;now+=150;return stamp};
 const snapshot=async()=>{now+=150;const time=now;now+=150;return time};
 const healthyUntil=async(deadline:number)=>{for(let at=now;at<deadline;at+=250)requests.push(at);now=Math.max(now,deadline)};
 for(const at of [120000,150000,180000]){
  await waitCronBoundary(at,clock,healthyUntil,()=>now,()=>{});
  const start=await snapshot();const trafficStart=now;
  // The fixed 20-second workload retains every cadence and offered count.
  const offered=[1000,1000,250,500].map((cadence,lane)=>Array.from({length:20000/cadence},(_,i)=>trafficStart+(lane===1?500:0)+i*cadence));
  assert.deepEqual(offered.map(slots=>slots.length),[20,20,80,40]);
  now=trafficStart+20000;const end=await snapshot();
  assertCronWindowTiming({start:new Date(start).toISOString(),end:new Date(end).toISOString()},at);
  assert.equal(start-at,300);assert.equal(end-at,20600);
 }
 assert(requests.length>0);
});

test('Cron alignment reports overshoot and never backdates slow snapshots',async()=>{
 const {waitCronBoundary,assertCronWindowTiming}=await import('./cron-traffic.ts');
 let now=119000;
 const clock=async()=>{now+=600;const stamp=now;now+=600;return stamp};
 await waitCronBoundary(120000,clock,async deadline=>{now=Math.max(now,deadline)},()=>now,()=>{});
 now+=600;const start=now;now+=600;now+=20000;now+=600;const end=now;
 assert.equal(start,121200);assert.equal(end,142400);
 assert.throws(()=>assertCronWindowTiming({start:new Date(start).toISOString(),end:new Date(end).toISOString()},120000),/outside required window/);
 // The two saved hosted starts independently violate the unchanged 1s gate.
 for(const [start,at] of [['2026-11-14T21:02:31.026Z','2026-11-14T21:02:30Z'],['2026-11-14T21:03:01.015Z','2026-11-14T21:03:00Z']]){
  assert.throws(()=>assertCronWindowTiming({start,end:new Date(Date.parse(at)+20500).toISOString()},Date.parse(at)),/outside required window/);
 }
});
