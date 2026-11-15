import assert from 'node:assert/strict';
import test from 'node:test';
import {buildDiagnosticGraph,diagnosticInputSchema} from './diagnostic.ts';
import {buildGraph,evidenceSchema,proposalSchema} from './graph.ts';
const id={run_id:'compound-test',table_oid:42,column_number:2,column_numbers:[2,3]};
const plan={node:'Seq Scan',examined_rows:100000,returned_rows:1,shared_blocks:1300};
const now=new Date().toISOString();
const observations=[{...id,tool:'plan',observed_at:now,status:'available',data:plan},{...id,tool:'indexes',observed_at:now,status:'available',data:{covering_index:false}}];
for (const [anchor, ordered] of [[2, 3], [15, 4]]) {
 test(`compound diagnostic preserves ordered keys ${anchor}, ${ordered}`, async () => {
  const identity = {...id, column_number: anchor, column_numbers: [anchor, ordered]};
  const input = diagnosticInputSchema.parse({...identity, decision:'investigate', initial:observations.map(o => ({...o, ...identity}))});
  const g = buildDiagnosticGraph(async view => {
   assert.deepEqual(view.identity, identity);
   return {decision:{action:'propose',repair:'index',tools:[],assessments:[{cause:'index',assessment:'supported',blocks_decision:false,reason:'Combined equality predicates have no covering index.'},{cause:'analyze',assessment:'rejected',blocks_decision:false,reason:'Refreshing statistics cannot create the missing access path.'}],reason:'Add the enrolled compound index.'}};
  }, async () => {throw Error('No additional evidence required');});
  const out = await g.invoke({input});
  assert.equal(out.proposal?.outcome, 'propose');
  assert.deepEqual(out.proposal?.column_numbers, [anchor, ordered]);
 });
}
test('compound schemas reject unsupported ordered keys and diagnostic evidence rebinding',async()=>{
 for(const keys of [[3,2],[2],[2,2],[2,0],[2,-1],[2,32768],[2,3.5],[2,3,4]]) {
  assert.equal(evidenceSchema.safeParse({...id,column_numbers:keys,observed_at:now,decision:'investigate',lookup_column:'lookup',covering_index:false,plan}).success,false);
  assert.equal(proposalSchema.safeParse({...id,column_numbers:keys,outcome:'propose',reason:'test'}).success,false);
  assert.equal(diagnosticInputSchema.safeParse({...id,column_numbers:keys,decision:'investigate',initial:[]}).success,false);
 }
 const g=buildDiagnosticGraph(async()=>{throw Error('Invalid evidence must stop before inference');},async()=>null);
 for (const column_numbers of [undefined, [2, 4]]) {
  const out=await g.invoke({input:diagnosticInputSchema.parse({...id,decision:'investigate',initial:observations.map(o=>({...o,column_numbers}))})});
  assert.equal(out.stop_reason,'invalid_evidence');
 }
});

test('single-call graph rejects a different valid compound proposal', async () => {
 const evidence = evidenceSchema.parse({...id, observed_at:now, decision:'investigate', lookup_column:'lookup', covering_index:false, plan});
 const graph = buildGraph(async read => {
  read('plan');
  read('indexes');
  return {...id, column_numbers:[2,4], outcome:'propose', reason:'Selective lookup'};
 });
 await assert.rejects(graph.invoke({evidence}), /enrolled index identity/);
});
