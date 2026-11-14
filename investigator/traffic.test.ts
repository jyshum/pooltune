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
