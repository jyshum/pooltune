import { test } from "node:test";
import assert from "node:assert/strict";
import { summarizeTraffic } from "./traffic.ts";

test("traffic report includes errors and offered requests in its rate denominator", () => {
  const summary = summarizeTraffic([
    { kind: "target", duration_ms: 10, ok: true },
    { kind: "target", duration_ms: 50, ok: true },
    { kind: "target", duration_ms: 750, ok: false },
  ], 4, 2);
  assert.equal(summary.offered, 4);
  assert.equal(summary.completed, 2);
  assert.equal(summary.errors, 1);
  assert.equal(summary.missed, 1);
  assert.equal(summary.completed_per_second, 1);
  assert.equal(summary.p95_ms, 750);
});

test("VACUUM traffic checks the aggregate result and freezes a baseline-based objective", async () => {
  const { trafficCase, vacuumObjective } = await import("./traffic.ts");
  const workload = trafficCase("vacuum", '"owned"."candidate"');
  assert.equal(workload.sql, 'SELECT sum(lookup) FROM ONLY "owned"."candidate" WHERE lookup >= $1 AND lookup <= $2');
  assert.deepEqual(workload.input(0), [1,50000]);
  workload.check([{sum:"1250025000"}],0);
  assert.throws(() => workload.check([{sum:"0"}],0));
  assert.equal(vacuumObjective([4,4,4,4,4,4,4,4,4,4]),6);
  assert.throws(() => vacuumObjective([4]));
});

test("memory traffic pins result, one target call per second and healthy objective", async()=>{
 const {trafficCase,memoryObjective,trafficCadence}=await import("./traffic.ts");
 const workload=trafficCase("memory",'"owned"."candidate"');
 assert.equal(workload.sql,'SELECT "owned"."pooltune_memory_sum"() AS sum');
 assert.deepEqual(workload.input(10),[]);
 workload.check([{sum:"5000050000"}],0);
 assert.throws(()=>workload.check([{sum:"1"}],0));
 assert.equal(memoryObjective([20,20,20,20,20,20,20,20,20,20]),25);
 assert.equal(memoryObjective([1,1,1,1,1,1,1,1,1,1]),3);
 assert.throws(()=>memoryObjective([20]));
 assert.deepEqual([0,1,2,3].map(lane=>trafficCadence("memory",lane)),[2000,2000,250,500]);
});

test("worker diagnostics retain only bounded sanitized failure lines",async()=>{
 const {recordWorkerDiagnostic}=await import("./traffic.ts");
 const lines:string[]=[];
 recordWorkerDiagnostic(lines,"pooltune-consumer","consumer attempt failed (SQLSTATE 22003)");
 recordWorkerDiagnostic(lines,"pooltune-worker","poll worker timed out");
 recordWorkerDiagnostic(lines,"pooltune-consumer","postgres://name:secret@host/db failed");
 recordWorkerDiagnostic(lines,"pooltune-consumer","consumer attempt failed (SQLSTATE 22003) password=secret");
 recordWorkerDiagnostic(lines,"unknown","consumer attempt failed");
 assert.deepEqual(lines,["pooltune-consumer: consumer attempt failed (SQLSTATE 22003)","pooltune-worker: poll worker timed out"]);
 for(let i=0;i<100;i++) recordWorkerDiagnostic(lines,"pooltune-consumer","consumer attempt failed");
 assert.equal(lines.length,40);
 assert(lines.every(line=>line.length<=200 && !line.includes("secret")));
});

test('failed repair reports retain first queue and final-save timings',async()=>{
 const {populateJobTimings}=await import('./traffic.ts');
 const report:any={slowdown_at:'2026-11-14T12:00:00Z',jobs:[{created_at:'2026-11-14T12:00:10Z',finished_at:'2026-11-14T12:00:20Z',result:{outcome:'awaiting_verification'}},{created_at:'2026-11-14T12:00:20Z',finished_at:'2026-11-14T12:06:00Z',result:{outcome:'inconclusive'}}]};
 populateJobTimings(report);assert.equal(report.detection_seconds,10);assert.equal(report.repair_seconds,350);assert.equal(report.final_outcome,'inconclusive');
});

test('Cron timing preserves model, action, evidence and cycle waits separately on failure',async()=>{
 const {populateJobTimings}=await import('./traffic.ts');
 const report:any={jobs:[{created_at:'2026-11-14T12:00:00Z',finished_at:'2026-11-14T12:00:02Z',result:{elapsed_ms:500,outcome:'awaiting_evidence'}},{created_at:'2026-11-14T12:00:02Z',finished_at:'2026-11-14T12:12:00Z',result:{outcome:'inconclusive',proposal:{model_call:{duration_ms:3000}},cron:{action_wall_ms:6000,cycle_wait_ms:400000,before:[{cycle:'2026-11-14T12:00:00Z'},{},{quiet:{end:'2026-11-14T12:05:20Z'}}]}}}]};
 populateJobTimings(report);assert.equal(report.initial_queue_delay_seconds,1.5);assert.equal(report.model_seconds,3);assert.equal(report.action_seconds,6);assert.equal(report.future_cycle_wait_seconds,400);assert.equal(report.evidence_accumulation_seconds,320);
});

test('failed fixture commands retain bounded operation and SQLSTATE without stderr secrets',async()=>{
 const {command}=await import('./traffic.ts');
 const {mkdtempSync,writeFileSync,chmodSync,rmSync}=await import('node:fs');
 const {tmpdir}=await import('node:os');const {join}=await import('node:path');
 const dir=mkdtempSync(join(tmpdir(),'fixture-command-'));const prior=process.env.PATH;
 try{
  const binary=join(dir,'pooltune-query-lab');
  writeFileSync(binary,'#!/bin/sh\nprintf "%s\\n" "postgresql://user:password@host/private secret (SQLSTATE 42501)" >&2\nexit 1\n');chmodSync(binary,0o700);process.env.PATH=dir+':'+prior;
  await assert.rejects(command(['-cron-action','record','-cron-id','private-id','-cron-period','/private/path']),error=>{
   const e=error as Error&{command_failure?:unknown};
   assert.deepEqual(e.command_failure,{operation:'cron_record',exit_code:1,signal:null,timed_out:false,sqlstate:'42501'});
   assert.match(e.message,/cron_record/);assert(!JSON.stringify(e).includes('password'));assert(!e.message.includes('private'));return true;
  });
 }finally{process.env.PATH=prior;rmSync(dir,{recursive:true,force:true})}
});

test('diagnostic traffic requires recorded real model work and allows bounded follow-up calls',async()=>{
 const {trafficInvestigatorMode,verifyTrafficProposal}=await import('./traffic.ts');
 assert.equal(trafficInvestigatorMode(undefined),'anthropic');
 assert.equal(trafficInvestigatorMode('diagnostic'),'diagnostic');
 assert.throws(()=>trafficInvestigatorMode('scripted'));
 const p={model_call:{model:'test-model',requests:2,duration_ms:100},diagnostic:{model_calls:2,model_attempts:2,reads:1,model_ms:100,tool_ms:5,total_ms:110}};
 verifyTrafficProposal(p,'diagnostic');
 assert.throws(()=>verifyTrafficProposal({...p,diagnostic:undefined},'diagnostic'));
 assert.throws(()=>verifyTrafficProposal({...p,model_call:{...p.model_call,requests:4}},'diagnostic'));
 assert.throws(()=>verifyTrafficProposal({...p,diagnostic:{...p.diagnostic,model_calls:1}},'diagnostic'));
 assert.throws(()=>verifyTrafficProposal(p,'anthropic'));
 verifyTrafficProposal({model_call:{model:'test-model',requests:1,duration_ms:100}},'anthropic');
});

test('compound traffic keeps both enrolled predicates and verifies the exact result',async()=>{
 const {trafficCase}=await import('./traffic.ts');
 const c=trafficCase('compound','"owned"."candidate"');
 assert.equal(c.sql,'SELECT id FROM ONLY "owned"."candidate" WHERE lookup=$1 AND padding=$2');
 assert.deepEqual(c.input(0),[43,'43'.padStart(64,'0')]);
 c.check([{id:43}],0);
 assert.throws(()=>c.check([{id:43},{id:44}],0));
 assert.throws(()=>c.check([{id:44}],0));
});

test('Cron final verification accepts one acknowledged multi-call diagnostic action and rejects duplicates',async()=>{
 const {verifyCronModelAction}=await import('./traffic.ts');
 const proposal={model_call:{model:'test-model',requests:2,duration_ms:100},diagnostic:{model_calls:2,model_attempts:2,reads:1,model_ms:100,tool_ms:5,total_ms:110}};
 const jobs=[{result:{outcome:'awaiting_verification',proposal}},{result:{outcome:'improved_tested_inputs'}}];
 verifyCronModelAction(jobs,'diagnostic');
 assert.throws(()=>verifyCronModelAction([...jobs,jobs[0]],'diagnostic'));
 assert.throws(()=>verifyCronModelAction([jobs[1]],'diagnostic'));
 assert.throws(()=>verifyCronModelAction(jobs,'anthropic'));
});
