import assert from 'node:assert/strict';
import test from 'node:test';
import {buildDiagnosticGraph,diagnosticInputSchema} from './diagnostic.ts';
import {evidenceSchema,proposalSchema} from './graph.ts';
const id={run_id:'compound-test',table_oid:42,column_number:2,column_numbers:[2,3]};
const plan={node:'Seq Scan',examined_rows:100000,returned_rows:1,shared_blocks:1300};
const now=new Date().toISOString();
const observations=[{...id,tool:'plan',observed_at:now,status:'available',data:plan},{...id,tool:'indexes',observed_at:now,status:'available',data:{covering_index:false}}];
test('compound diagnostic preserves ordered enrolled keys without model SQL',async()=>{
 const input=diagnosticInputSchema.parse({...id,decision:'investigate',initial:observations});
 const g=buildDiagnosticGraph(async()=>({decision:{action:'propose',repair:'index',tools:[],assessments:[{cause:'index',assessment:'supported',blocks_decision:false,reason:'Combined equality predicates have no covering index.'},{cause:'analyze',assessment:'rejected',blocks_decision:false,reason:'Refreshing statistics cannot create the missing access path.'}],reason:'Add the enrolled compound index.'}}),async()=>{throw Error('No additional evidence required');});
 const out=await g.invoke({input});assert.equal(out.proposal?.outcome,'propose');assert.deepEqual(out.proposal?.column_numbers,[2,3]);
});
test('compound schemas reject unsupported ordered keys and diagnostic evidence rebinding',async()=>{
 for(const keys of [[3,2],[2],[2,2],[2,4],[2,3,4]]) {
  assert.equal(evidenceSchema.safeParse({...id,column_numbers:keys,observed_at:now,decision:'investigate',lookup_column:'lookup',covering_index:false,plan}).success,false);
  assert.equal(proposalSchema.safeParse({...id,column_numbers:keys,outcome:'propose',reason:'test'}).success,false);
 }
 const g=buildDiagnosticGraph(async()=>{throw Error('Invalid evidence must stop before inference');},async()=>null);
 const out=await g.invoke({input:diagnosticInputSchema.parse({...id,decision:'investigate',initial:observations.map(o=>({...o,column_numbers:undefined}))})});
 assert.equal(out.stop_reason,'invalid_evidence');
});
