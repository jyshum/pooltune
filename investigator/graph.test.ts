import { test } from "node:test";
import assert from "node:assert/strict";
import { buildGraph, type Evidence } from "./graph.ts";
import { buildSingleCallInvestigator, anthropicInvestigator, scriptedInvestigator } from "./model.ts";

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

test("single-call path sends both evidence fields together and skips healthy queries", async () => {
  const calls: unknown[] = [];
  const graph = buildGraph(buildSingleCallInvestigator(async (messages) => {
    assert.equal(messages.length, 2);
    const payload = JSON.parse(String(messages[1].content));
    calls.push(payload);
    return { outcome: "propose", run_id: "fixture", table_oid: 42, column_number: 2, reason: "Selective scan" };
  }));
  const result = await graph.invoke({ evidence });
  assert.deepEqual(calls, [{
    enrollment: { run_id: "fixture", observed_at: evidence.observed_at, decision: "investigate",
      table_oid: 42, column_number: 2, lookup_column: "lookup" },
    plan: evidence.plan, indexes: { covering_index: false },
  }]);
  assert.equal(result.proposal?.outcome, "propose");
  assert.equal(result.proposal?.table_oid, 42);
  assert.equal(result.proposal?.column_number, 2);
  assert.deepEqual(result.tools, ["plan", "indexes"]);

  for (const healthy of [
    { ...evidence, decision: "no_change" as const, plan: { ...evidence.plan, node: "Index Scan", examined_rows: 1 } },
    { ...evidence, covering_index: true },
  ]) {
    const skipped = await graph.invoke({ evidence: healthy });
    assert.equal(skipped.proposal?.outcome, "no_change");
    assert.deepEqual(skipped.tools, []);
  }
  assert.equal(calls.length, 1);
});

test("the default graph makes one request containing both pieces of evidence", async (t) => {
  const previousKey = process.env.ANTHROPIC_API_KEY;
  const previousModel = process.env.POOLTUNE_ANTHROPIC_MODEL;
  process.env.ANTHROPIC_API_KEY = "fixture-key";
  process.env.POOLTUNE_ANTHROPIC_MODEL = "claude-haiku-5-5";
  t.after(() => {
    if (previousKey === undefined) delete process.env.ANTHROPIC_API_KEY;
    else process.env.ANTHROPIC_API_KEY = previousKey;
    if (previousModel === undefined) delete process.env.POOLTUNE_ANTHROPIC_MODEL;
    else process.env.POOLTUNE_ANTHROPIC_MODEL = previousModel;
  });
  const requests: string[] = [];
  t.mock.method(globalThis, "fetch", async (_input: unknown, init?: RequestInit) => {
    requests.push(String(init?.body));
    const body = JSON.parse(requests.at(-1)!);
    const toolName = body.tools[0].name;
    assert.equal(toolName, "repair_recommendation");
    return Response.json({
      id: "msg_fixture", type: "message", role: "assistant", model: "claude-haiku-5-5",
      content: [{ type: "tool_use", id: "toolu_fixture", name: toolName,
        input: { outcome: "propose", run_id: "fixture", table_oid: 42, column_number: 2, reason: "Selective scan" } }],
      stop_reason: "tool_use", stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 },
    });
  });

  const result = await buildGraph(anthropicInvestigator).invoke({ evidence });
  assert.equal(result.proposal?.outcome, "propose");
  assert.equal(requests.length, 1);
  assert.equal(result.proposal?.model_call?.requests, 1);
  assert.equal(result.proposal?.model_call?.model, "claude-haiku-5-5");
  assert.equal(result.proposal?.model_call?.input_tokens, 1);
  assert.equal(result.proposal?.model_call?.output_tokens, 1);
  assert.ok(result.proposal!.model_call!.duration_ms >= 0);
  const request = JSON.parse(requests[0]);
  const content = request.messages[0].content;
  const payload = JSON.parse(typeof content === "string" ? content : content[0].text);
  assert.deepEqual(payload.plan, evidence.plan);
  assert.deepEqual(payload.indexes, { covering_index: false });
});

test("eligible ANALYZE evidence reaches the investigator and weak evidence closes early", async () => {
  let calls = 0;
  const graph = buildGraph(async (read, input) => { calls++; return scriptedInvestigator(read, input); });
  const stale = { ...evidence, covering_index: true, analyze: { estimated_rows: 100000, actual_rows: 1 } };
  const proposed = await graph.invoke({ evidence: stale });
  assert.equal(proposed.proposal?.outcome, "propose");
  assert.equal(proposed.proposal?.repair, "analyze");
  assert.equal(proposed.proposal?.table_oid, 42);
  assert.equal(calls, 1);
  const current = await graph.invoke({ evidence: { ...stale, analyze: { estimated_rows: 2, actual_rows: 1 } } });
  assert.equal(current.proposal?.outcome, "no_change");
});

test("eligible joint statistics reaches the investigator and unsupported evidence closes early", async () => {
  let calls = 0;
  const graph = buildGraph(async (read, input) => { calls++; return scriptedInvestigator(read, input); });
  const statistics = { correlated_columns: true, cases: [{ estimated_rows: 25000, actual_rows: 1 }, { estimated_rows: 24999, actual_rows: 1 }] };
  const result = await graph.invoke({ evidence: { ...evidence, statistics } });
  assert.equal(result.proposal?.repair, "statistics");
  assert.equal(calls, 1);
  const unsupported = await graph.invoke({ evidence: { ...evidence, statistics: { ...statistics, correlated_columns: false } } });
  assert.equal(unsupported.proposal?.outcome, "no_change");
});

test("the model can decline either statistics repair and errors propagate without a proposal", async () => {
  for (const details of [
    { analyze: { estimated_rows: 100000, actual_rows: 1 } },
    { statistics: { correlated_columns: true, cases: [{ estimated_rows: 25000, actual_rows: 100 }, { estimated_rows: 25000, actual_rows: 300 }] } },
  ]) {
    const input = { ...evidence, covering_index: true, ...details };
    const graph = buildGraph(async (read) => {
      read("plan"); read("indexes");
      return { outcome: "no_change", run_id: "fixture", table_oid: 42, column_number: 2, reason: "Insufficient causal evidence" };
    });
    assert.equal((await graph.invoke({ evidence: input })).proposal?.outcome, "no_change");
    await assert.rejects(buildGraph(async () => { throw new Error("provider timeout"); }).invoke({ evidence: input }), /provider timeout/);
  }
});

test("VACUUM visibility evidence reaches the shared model while weak and healthy cases stop", async () => {
  const dirty = { ...evidence, covering_index: true,
    plan: { node: "Index Only Scan", examined_rows: 2000, returned_rows: 2000, shared_blocks: 2000 },
    vacuum: { heap_fetches: 1000, scan_rows: 2000, conditions_clear: true } };
  const result = await buildGraph(scriptedInvestigator).invoke({ evidence: dirty });
  assert.equal(result.proposal?.repair, "vacuum");
  for (const changes of [ { decision: "no_change" as const }, { vacuum: { ...dirty.vacuum, heap_fetches: 999 } },
    { vacuum: { ...dirty.vacuum, scan_rows: 2001 } }, { vacuum: { ...dirty.vacuum, conditions_clear: false } } ]) {
    const skipped = await buildGraph(async () => { throw new Error("weak evidence reached model"); }).invoke({ evidence: { ...dirty, ...changes } });
    assert.equal(skipped.proposal?.outcome, "no_change");
  }
  await assert.rejects(buildGraph(async () => { throw new Error("provider timeout"); }).invoke({ evidence: dirty }), /provider timeout/);
  const declined = await buildGraph(buildSingleCallInvestigator(async () => ({ outcome: "no_change",run_id:"fixture",table_oid:42,column_number:2,reason:"uncertain cause" }))).invoke({evidence:dirty});
  assert.equal(declined.proposal?.outcome,"no_change");
});


test("ambiguous VACUUM and statistics causes do not reach the model", async () => {
 const input = { ...evidence, vacuum:{heap_fetches:1000,scan_rows:1000,conditions_clear:true},analyze:{estimated_rows:100000,actual_rows:1} };
 await assert.rejects(buildGraph(async()=>{ throw new Error("ambiguous evidence reached model"); }).invoke({evidence:input}),/one repair cause/);
});

test("memory screening requires attributed spill and bounded calls", async () => {
 const input = { ...evidence, plan:{node:"Sort",examined_rows:100000,returned_rows:100000,shared_blocks:2000},memory:{function_oid:43,temp_bytes:14688256,internal_temp_bytes:14688256,sort_nodes:1,max_concurrent_calls:3,candidate_bytes:33554432,aggregate_bytes:100663296,current_work_mem:"4MB",candidate_work_mem:"32MB",identity_verified:true} };
 const result=await buildGraph(scriptedInvestigator).invoke({evidence:input});
 assert.equal(result.proposal?.repair,"memory");
 for(const change of [{temp_bytes:0},{internal_temp_bytes:0},{sort_nodes:2},{max_concurrent_calls:4},{aggregate_bytes:33554432},{identity_verified:false},{current_work_mem:"32MB"}]) {
  const r=await buildGraph(async()=>{throw new Error("unsafe memory reached investigator")}).invoke({evidence:{...input,memory:{...input.memory,...change}}});
  assert.equal(r.proposal?.outcome,"no_change");
 }
 await assert.rejects(()=>buildGraph(scriptedInvestigator).invoke({evidence:{...input,vacuum:{heap_fetches:1000,scan_rows:1000,conditions_clear:true}}}));
});
