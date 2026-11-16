import {test} from 'node:test';
import assert from 'node:assert/strict';
import {buildGraph,evidenceSchema} from './graph.ts';
import {buildDiagnosticGraph} from './diagnostic.ts';
import {scriptedInvestigator} from './model.ts';
import {managedCronSchema,managedCronCause} from './managed-cron-schema.ts';
import {summarizeDiagnosticEvidence} from './diagnostic-model.ts';

export function managedEvidence():any {
 const base=Math.floor(Date.now()/120000)*120000-360000;
 const stamp=(n:number)=>new Date(n).toISOString(),epoch=stamp(base-60000);
 const metric=(query:string,calls:number,latency:number,offset:number)=>{
  const before={Database:1,Role:2,Query:query,Boot:epoch,Reset:epoch,Entry:epoch,Calls:offset,Blocks:offset,ExecutionMS:offset*100};
  return {before,after:{...before,Calls:offset+calls,Blocks:offset+calls,ExecutionMS:before.ExecutionMS+calls*latency}};
 };
 const periods=[0,1,2].map(i=>{
  const cycle=base+i*120000;
  const window=(seconds:number,mean:number,n:number,offset:number)=>({start:stamp(cycle+seconds*1000),end:stamp(cycle+(seconds+20)*1000),target:metric('3',n,mean,offset),read:metric('4',80,1,offset),write:metric('5',40,1,offset)});
  return {cycle:stamp(cycle),run:{id:i+1,job_id:9,pid:100+i,start:stamp(cycle+100),end:stamp(cycle+10000),status:'succeeded'},busy:window(0,42,40,i*1000),control:window(30,2,40,i*1000+200),quiet:window(60,0,0,i*1000+400),blocks:[{at:stamp(cycle+1000),run_id:i+1,job_id:9,pid:100+i,backend_start:stamp(cycle),waiting_pid:200,waiting_role:2,waiting_start:stamp(cycle),query_id:'3'}]};
 });
 return {run_id:'managed-cron',observed_at:new Date().toISOString(),decision:'investigate',job_id:9,table_oid:0,column_number:0,lookup_column:'lookup',covering_index:false,plan:{node:'Result',examined_rows:0,returned_rows:0,shared_blocks:0},managed_cron:{job_id:9,original_schedule:'*/2 * * * *',alternate_schedule:'1-59/2 * * * *',window:{EveryMinutes:2,CurrentMinute:0,ProposedMinute:1,DeadlineSeconds:120,MaxRuntimeSeconds:20},generation:{pid:99,started:epoch,postmaster:epoch},periods,application_request_visibility:'unobserved'}};
}

test('managed scheduling reaches the model using completed work without invented app counts',async()=>{
 const e=managedEvidence();assert(managedCronCause(managedCronSchema.parse(e.managed_cron)));
 let calls=0;const g=buildGraph(async(read,enrollment)=>{calls++;return scriptedInvestigator(read,enrollment)});
 const result=await g.invoke({evidence:e});assert.equal(result.proposal?.repair,'cron');assert.equal(calls,1);
 for(const change of ['missing_blocks','reset','less_demand','changed_window','wrong_run','small_effect']){
  const e=managedEvidence(),c=e.managed_cron;
  if(change==='missing_blocks')c.periods[0].blocks=[];
  if(change==='reset')c.periods[0].busy.target.after.Reset=new Date().toISOString();
  if(change==='less_demand')c.periods[1].busy.target.after.Calls=c.periods[1].busy.target.before.Calls+21;
  if(change==='changed_window')c.window.ProposedMinute=0;
  if(change==='wrong_run')c.periods[1].blocks[0].run_id=1;
  if(change==='small_effect')c.periods[0].busy.target.after.ExecutionMS=80;
  assert.equal((await g.invoke({evidence:e})).proposal?.outcome,'no_change',change);
 }
 assert.equal(calls,1);
});

test('managed scheduling rejects made-up request metrics and mixed legacy evidence',()=>{
 const e=managedEvidence();e.managed_cron.periods[0].busy.target.errors=0;
 assert.equal(evidenceSchema.safeParse(e).success,false);
 const c=managedEvidence();c.managed_cron.application_request_visibility='verified';assert.equal(evidenceSchema.safeParse(c).success,false);
 const mixed=managedEvidence();mixed.vacuum={heap_fetches:1000,scan_rows:1000,conditions_clear:true};assert.equal(evidenceSchema.safeParse(mixed).success,false);
});

test('managed Cron crosses the actual diagnostic bridge with visibility preserved',async()=>{
 const e=managedEvidence(),identity={job_id:e.job_id,run_id:e.run_id,table_oid:e.table_oid,column_number:e.column_number};
 const initial=[['plan',e.plan],['indexes',{covering_index:false}],['cron',e.managed_cron]].map(([tool,data])=>({...identity,tool:tool as 'plan'|'indexes'|'cron',observed_at:e.observed_at,status:'available' as const,data}));
 const summary=summarizeDiagnosticEvidence(initial);assert.equal(summary.managed_cron_periods?.[0].application_request_visibility,'unobserved');assert.equal(summary.cron_periods,undefined);
 const graph=buildDiagnosticGraph(async()=>({decision:{action:'propose',repair:'cron',tools:[],assessments:[{cause:'cron',assessment:'supported',blocks_decision:false,reason:'Repeated attributable blocking and comparable completed database work.'},{cause:'index',assessment:'rejected',blocks_decision:false,reason:'Schedule-linked blocking is the observed mechanism.'}],reason:'Try the enrolled future window and verify database work.'}}),async()=>{throw Error('unexpected extra read')});
 const result=await graph.invoke({input:{...identity,decision:'investigate',initial}});assert.equal(result.proposal?.repair,'cron');assert.equal(result.proposal?.outcome,'propose');
});

 test('managed job identity cannot be substituted or mixed with a table identity',async()=>{
  for(const patch of [{job_id:10},{job_id:undefined},{table_oid:42},{column_number:1}]){
   const e={...managedEvidence(),...patch};
   let calls=0;const graph=buildGraph(async(read,enrollment)=>{calls++;return scriptedInvestigator(read,enrollment)});
   if(evidenceSchema.safeParse(e).success)assert.equal((await graph.invoke({evidence:e})).proposal?.outcome,'no_change');
   assert.equal(calls,0);
  }
 });
 test('managed schedule rejects reused protection identity and counter rewind between windows',()=>{
  const e=managedEvidence();e.managed_cron.periods[0].control.read=e.managed_cron.periods[0].control.target;
  assert.equal(managedCronCause(e.managed_cron),false);
  const rewind=managedEvidence();rewind.managed_cron.periods[0].control.target.before.Calls=0;
  assert.equal(managedCronCause(rewind.managed_cron),false);
 });
test('managed scheduling binds each blocker to the observed app role',()=>{
 const e=managedEvidence();e.managed_cron.periods[0].blocks[0].waiting_role=999;
 assert.equal(managedCronCause(e.managed_cron),false);
 const missing=managedEvidence();delete missing.managed_cron.periods[0].blocks[0].waiting_role;
 assert.equal(managedCronCause(missing.managed_cron),false);
});
