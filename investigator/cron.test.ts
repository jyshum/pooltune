import {test} from 'node:test';
import assert from 'node:assert/strict';
import {buildGraph} from './graph.ts';
import {scriptedInvestigator} from './model.ts';
import {diagnosticDecider} from './diagnostic-model.ts';
import type {DiagnosticView} from './diagnostic.ts';

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

test('shortens only the complete scheduling request while preserving the measured evidence',async t=>{
 const oldKey=process.env.ANTHROPIC_API_KEY,oldModel=process.env.POOLTUNE_ANTHROPIC_MODEL;
 process.env.ANTHROPIC_API_KEY='test-key';process.env.POOLTUNE_ANTHROPIC_MODEL='claude-sonnet-5-5';
 t.after(()=>{
  if(oldKey===undefined)delete process.env.ANTHROPIC_API_KEY;else process.env.ANTHROPIC_API_KEY=oldKey;
  if(oldModel===undefined)delete process.env.POOLTUNE_ANTHROPIC_MODEL;else process.env.POOLTUNE_ANTHROPIC_MODEL=oldModel;
 });
 const decision={action:'propose',repair:'cron',tools:[],assessments:[
  {cause:'cron',assessment:'supported',blocks_decision:false,reason:'The enrolled job repeatedly blocked foreground reads.'},
  {cause:'index',assessment:'rejected',blocks_decision:false,reason:'The matched reads were fast without the competing job.'},
 ],reason:'Move the job within the approved schedule to reduce attributed blocking.'};
 const calls:{model:string;max_tokens:number;system:unknown;messages:{content:string|{text:string}[]}[];output_config:unknown}[]=[];
 t.mock.method(globalThis,'fetch',async(_input:unknown,init?:RequestInit)=>{
  calls.push(JSON.parse(String(init?.body)));
  return Response.json({id:'test',type:'message',role:'assistant',model:'claude-sonnet-5-5',
   content:[{type:'text',text:JSON.stringify(decision)}],stop_reason:'end_turn',stop_sequence:null,usage:{input_tokens:1,output_tokens:1}});
 });
 const e=evidence();
 const identity={run_id:e.run_id,table_oid:e.table_oid,column_number:e.column_number};
 const baseline={...identity,observed_at:e.observed_at,status:'available' as const};
 const view:DiagnosticView={identity,observations:[
  {...baseline,tool:'plan',data:e.plan},
  {...baseline,tool:'indexes',data:{covering_index:e.covering_index}},
  {...baseline,tool:'cron',data:e.cron},
 ],history:[],remaining_model_calls:3,remaining_reads:6};
 const signal=new AbortController().signal;
 const result=await diagnosticDecider(view,signal);
 await diagnosticDecider({...view,observations:[...view.observations.slice(0,2),
  {...baseline,tool:'cron',data:{...e.cron,periods:e.cron.periods.slice(0,2)}}]},signal);
 await diagnosticDecider({...view,observations:[...view.observations,
  {...baseline,tool:'analyze',data:{estimated_rows:10,actual_rows:1}}]},signal);
 const [compact,incomplete,mixed]=calls;
 assert.deepEqual(result.decision,decision);
 assert.equal(compact.model,'claude-sonnet-5-5');
 assert.equal(compact.max_tokens,600);
 assert.equal(incomplete.max_tokens,1200);
 assert.deepEqual(compact.output_config,incomplete.output_config);
 assert.deepEqual(incomplete.system,mixed.system);
 const prompt=JSON.stringify(compact.system);
 assert.ok(prompt.length<JSON.stringify(incomplete.system).length/2);
 assert.match(prompt,/plausible alternative/);
 assert.match(prompt,/never cancel/i);
 const content=compact.messages[0].content;
 assert.deepEqual(JSON.parse(typeof content==='string'?content:content[0].text).observations,view.observations);
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
     if(url==='test:traffic')return {format:'module',source:'export const {command,verifyCronModelAction,trafficInvestigatorMode,verifyTrafficProposal,counterDeltas,populateJobTimings,recordWorkerDiagnostic,summarizeTraffic,trafficRequest,vacuumObjective}=globalThis.boundary.shared',shortCircuit:true};
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

function acknowledgedCronJobs(cycle:number):any[]{
 const stamp=(ms:number)=>new Date(cycle+ms).toISOString();
 return [{id:1,finished_at:stamp(106000),result:{outcome:'awaiting_verification',cron:{commit_at:stamp(101000),post_witness:[{id:11,job_id:2,status:'succeeded',start:stamp(102000),end:stamp(102100)},{id:12,job_id:2,status:'succeeded',start:stamp(104000),end:stamp(104100)}]}}},{id:2,parent_id:1,finished_at:null,not_before:stamp(482000)}];
}

test('Cron waits for published on-time proof while healthy traffic continues before restart',async()=>{
 const {waitCronAcknowledgement}=await import('./cron-traffic.ts');
 let now=100000,healthy=0,stopped=false;
 const ready=acknowledgedCronJobs(0);
 const waiting=waitCronAcknowledgement(0,2,async()=>now>=107000?ready:[],async()=>now,async seconds=>{assert.equal(stopped,false);healthy++;now+=seconds*1000},()=>now);
 assert.equal(stopped,false);
 const jobs=await waiting;stopped=true;
 assert.equal(jobs,ready);assert(now>=107000&&now<110000);assert(healthy>=14);assert.equal(stopped,true);
});

for(const mode of ['missing_proof','late_commit','late_witness','duplicate_witness','foreign_witness','failed_witness','declined','failed','late_publication','clock_overshoot','clock_reversed']){
 test(`Cron acknowledgement refuses ${mode} without premature restart`,async()=>{
  const {waitCronAcknowledgement}=await import('./cron-traffic.ts');
  let now=106000,healthy=0,stopped=false;const ready=acknowledgedCronJobs(0),proof=ready[0].result.cron;
  if(mode==='missing_proof')proof.post_witness=[];
  if(mode==='late_commit')proof.commit_at=new Date(105000).toISOString();
  if(mode==='late_witness')proof.post_witness[1].end=new Date(105000).toISOString();
  if(mode==='duplicate_witness')proof.post_witness[1]=proof.post_witness[0];
  if(mode==='foreign_witness')proof.post_witness[1].job_id=3;
  if(mode==='failed_witness')proof.post_witness[1].status='failed';
  if(mode==='declined'||mode==='failed')ready[0].result.outcome=mode==='declined'?'no_change':'inconclusive';
  await assert.rejects(async()=>{
   await waitCronAcknowledgement(0,2,async()=>mode==='late_publication'&&now<110000?[]:ready,async()=>now,async seconds=>{healthy++;now+=seconds*1000;if(mode==='clock_overshoot')now=110001;if(mode==='clock_reversed')now=99999},()=>now);
   stopped=true;
  },/Cron/);
  assert.equal(stopped,false);assert(healthy>0);
  if(mode==='declined'||mode==='failed'||mode==='missing_proof')assert.equal(healthy,1);
 });
}

function pendingCronFinalJobs():any[]{return [{id:1,finished_at:new Date(106000).toISOString(),result:{outcome:'awaiting_verification'}},{id:2,parent_id:1,finished_at:null,not_before:new Date(722000).toISOString()}]}

test('Cron campaign permits final verification after alignment and all six actual periods',async()=>{
 const {CRON_CAMPAIGN_BUDGET_MS,checkCronCampaignBudget,waitCronBoundary,waitCronFinalResult}=await import('./cron-traffic.ts');
 // Worst calendar alignment (2 min) plus the existing controls (6 min) puts
 // firstCycle at minute 8. The real child is eligible 12 min and 2 s later.
 const firstCycle=480_000,eligible=1_202_000,observeAt=1_208_000;
 let now=1_180_000;const start=0;
 await waitCronBoundary(observeAt,async()=>now,async deadline=>{now=deadline},()=>now,()=>checkCronCampaignBudget(start,now));
 const parent={id:1,finished_at:new Date(850_000).toISOString(),result:{outcome:'awaiting_verification'}};
 const child={id:2,parent_id:1,finished_at:null,not_before:new Date(eligible).toISOString()};
 const final=await waitCronFinalResult(firstCycle,{id:2,parent_id:1},async()=>[parent,now<1_220_000?child:{...child,finished_at:new Date(now).toISOString(),result:{outcome:'improved_tested_inputs'}}],async()=>now,async seconds=>{now+=seconds*1000},start+CRON_CAMPAIGN_BUDGET_MS,()=>now);
 assert.equal(final[1].result?.outcome,'improved_tested_inputs');
 assert(now>=1_220_000&&now<1_235_000,'final observation must retain its original +35s bound');
});

test('Cron campaign deadline still stops boundary and final-result waits',async()=>{
 const {CRON_CAMPAIGN_BUDGET_MS,checkCronCampaignBudget,waitCronBoundary,waitCronFinalResult}=await import('./cron-traffic.ts');
 let now=CRON_CAMPAIGN_BUDGET_MS-1;
 await assert.rejects(waitCronBoundary(CRON_CAMPAIGN_BUDGET_MS+1,async()=>now,async deadline=>{now=deadline},()=>now,()=>checkCronCampaignBudget(0,now)),/wall-clock budget expired/);
 now=CRON_CAMPAIGN_BUDGET_MS;
 let reads=0;
 await assert.rejects(waitCronFinalResult(now-728_000,{id:2,parent_id:1},async()=>{reads++;return pendingCronFinalJobs()},async()=>now,async()=>{},CRON_CAMPAIGN_BUDGET_MS,()=>now),/work budget expired/);
 assert.equal(reads,0,'expired campaigns must not start another observation');
});

test('Cron final observation waits beyond the first check for one terminal saved result',async()=>{
 const {waitCronFinalResult}=await import('./cron-traffic.ts');let now=728000,healthy=0,terminalReads=0;
 const rows=pendingCronFinalJobs();const final=rows.map(j=>j.id===2?{...j,finished_at:new Date(740000).toISOString(),result:{outcome:'improved_tested_inputs'}}:j);
 const observed=await waitCronFinalResult(0,{id:2,parent_id:1},async()=>{if(now>=740000){terminalReads++;return final}return rows},async()=>now,async seconds=>{healthy++;now+=seconds*1000},1200000,()=>now);
 assert.equal(observed,final);assert.equal(terminalReads,1);assert(healthy>=24);assert(now<755000);
});

for(const mode of ['terminal_failure','missing_child','never_terminal','late_publication','clock_overshoot','clock_reversed','overall_budget','stalled_clock']){
 test(`Cron final observation refuses ${mode}`,async()=>{
  const {waitCronFinalResult}=await import('./cron-traffic.ts');let now=728000,mono=728000,healthy=0;
  const rows=pendingCronFinalJobs();
  if(mode==='terminal_failure'){rows[1].finished_at=new Date(now).toISOString();rows[1].result={outcome:'inconclusive'}}
  if(mode==='missing_child')rows.pop();
  await assert.rejects(waitCronFinalResult(0,{id:2,parent_id:1},async()=>{
   if(mode==='late_publication'&&now>=755000)return rows.map(j=>j.id===2?{...j,finished_at:new Date(now).toISOString(),result:{outcome:'improved_tested_inputs'}}:j);
   return rows;
  },async()=>now,async seconds=>{healthy++;mono+=seconds*1000;if(mode!=='stalled_clock')now+=seconds*1000;if(mode==='clock_overshoot')now=755001;if(mode==='clock_reversed')now=727999},mode==='overall_budget'?729000:1200000,()=>mono),/Cron/);
  assert(healthy>0);if(mode==='terminal_failure'||mode==='missing_child')assert.equal(healthy,1);
  if(mode==='overall_budget')assert(mono<=729000);
 });
}

for(const mode of ['replacement_pending_child','wrong_improved_child']){
 test(`Cron final observation refuses ${mode}`,async()=>{
  const {waitCronFinalResult}=await import('./cron-traffic.ts');let now=728000,reads=0;
  const rows=pendingCronFinalJobs();
  await assert.rejects(waitCronFinalResult(0,{id:2,parent_id:1},async()=>{
   reads++;
   // The child captured at acknowledgement was ID 2, parent 1. A replacement
   // sharing its parent must not become the awaited child or receive credit.
   return [rows[0],{...rows[1],id:3,...(mode==='wrong_improved_child'||reads>1?{finished_at:new Date(now).toISOString(),result:{outcome:'improved_tested_inputs'}}:{})}];
  },async()=>now,async seconds=>{now+=seconds*1000},1200000,()=>now),/Cron final verification child missing or changed/);
  assert.equal(reads,1);
 });
}

test('Cron delivers current and permitted candidate meanings while preserving model decline and schedule screening',async()=>{
 const {buildSingleCallInvestigator}=await import('./model.ts');let calls=0;
 const graph=buildGraph(buildSingleCallInvestigator(async messages=>{
  calls++;
  const instructions=String(messages[0].content),payload=JSON.parse(String(messages[1].content));
  assert.match(instructions,/original_schedule is the current schedule/);
  assert.match(instructions,/alternate_schedule is the owner-approved candidate for this enrolled job/);
  assert.match(instructions,/permission applies only to this exact schedule pair/);
  assert.match(instructions,/repair=cron proposes exactly alternate_schedule/);
  assert.equal(payload.enrollment.cron.original_schedule,'*/2 * * * *');
  assert.equal(payload.enrollment.cron.alternate_schedule,'1-59/2 * * * *');
  assert.equal(payload.enrollment.cron.required_runs_per_cycle,1);
  assert.equal(payload.enrollment.cron.deadline_seconds,120);
  return {outcome:'no_change',run_id:payload.enrollment.run_id,table_oid:payload.enrollment.table_oid,column_number:payload.enrollment.column_number,reason:'Declined after reviewing evidence.'};
 }));
 const declined=await graph.invoke({evidence:evidence()});
 assert.equal(declined.proposal?.reason,'Declined after reviewing evidence.');assert.equal(calls,1);
 for(const field of ['original_schedule','alternate_schedule']){
  const bad=evidence();bad.cron[field]='* * * * *';bad.cron.identity_verified=true;
  assert.equal((await graph.invoke({evidence:bad})).proposal?.outcome,'no_change');
 }
 assert.equal(calls,1,'identity verification must not admit a disallowed schedule to the model');
});
