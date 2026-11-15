import assert from 'node:assert/strict';
import test from 'node:test';
import { buildDiagnosticGraph, diagnosticInputSchema } from './diagnostic.ts';

const identity = {run_id:'project-lookup-1',table_oid:45000,column_number:1};
const lookup = {objective_ms:1,recent_window_mean_ms:[4,4,4,4,4],recent_window_calls:[2,2,2,2,2],
 recent_window_shared_block_accesses:[100,100,100,100,100],table_pages:442,estimated_table_rows:100000,
 leading_btree:false,modified_since_analyze:0,estimated_dead_rows:0};
const input = () => diagnosticInputSchema.parse({...identity,mode:'enrolled_lookup',decision:'investigate',initial:[
 {...identity,tool:'lookup',observed_at:new Date().toISOString(),status:'available',data:lookup}
]});
const assessments = [
 {cause:'index',assessment:'supported',blocks_decision:false,reason:'No leading index on the approved column.'},
 {cause:'vacuum',assessment:'uncertain',blocks_decision:true,reason:'No measured plan or visibility data.'},
];

test('diagnoses an enrolled lookup in one model call without enabling evidence requests', async () => {
 const graph = buildDiagnosticGraph(async view => {
  assert.equal(view.mode,'enrolled_lookup');
  assert.equal(view.remaining_reads,0);
  assert.deepEqual(view.observations[0].data,lookup);
  return {decision:{action:'no_change',repair:null,tools:[],assessments,reason:'A missing leading index is the strongest clue.'}};
 },async () => { throw Error('Unexpected database read'); });
 const result = await graph.invoke({input:input()});
 assert.equal(result.proposal?.outcome,'no_change');
 assert.equal(result.model_calls,1);
 assert.equal(result.history.length,1);
});

test('refuses a repair proposal in the enrolled-lookup path', async () => {
 const graph = buildDiagnosticGraph(async () => ({decision:{action:'propose',repair:'index',tools:[],assessments,reason:'Try an index.'}}),
  async () => { throw Error('Unexpected database read'); });
 const result = await graph.invoke({input:input()});
 assert.equal(result.proposal?.outcome,'no_change');
 assert.equal(result.stop_reason,'invalid_model_output');
 assert.deepEqual(result.history,[]);
});
