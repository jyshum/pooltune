import { test } from "node:test";
import assert from "node:assert/strict";
import { buildGraph, type Evidence } from "./graph.ts";

const evidence: Evidence = {
  run_id: "fixture", observed_at: "2026-11-13T07:00:00Z", decision: "investigate",
  table_oid: 42, column_number: 2, lookup_column: "lookup", covering_index: false,
  plan: { node: "Seq Scan", examined_rows: 100000, returned_rows: 1, shared_blocks: 1334 },
};

test("investigator reads plan and catalog before returning a typed proposal", async () => {
  const graph = buildGraph(async (read) => {
    const plan = read("plan");
    const catalog = read("indexes");
    assert.deepEqual(plan, evidence.plan);
    assert.deepEqual(catalog, { covering_index: false });
    return { outcome: "propose", run_id: "fixture", table_oid: 42, column_number: 2, reason: "Selective scan" };
  });
  const result = await graph.invoke({ evidence });
  assert.equal(result.proposal?.outcome, "propose");
  assert.deepEqual(result.tools, ["plan", "indexes"]);
});

test("healthy and misleading cases end without a model call", async () => {
  const graph = buildGraph(async () => { throw new Error("model should not be called"); });
  assert.equal((await graph.invoke({ evidence: { ...evidence, decision: "no_change" } })).proposal?.outcome, "no_change");
  assert.equal((await graph.invoke({ evidence: { ...evidence, plan: { ...evidence.plan, returned_rows: 50000 } } })).proposal?.outcome, "no_change");
});
