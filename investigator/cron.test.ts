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
