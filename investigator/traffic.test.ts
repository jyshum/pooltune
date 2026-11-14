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
