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
