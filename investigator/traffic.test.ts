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
