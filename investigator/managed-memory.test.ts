import {test} from 'node:test';
import assert from 'node:assert/strict';
import {buildGraph,evidenceSchema} from './graph.ts';
import {buildDiagnosticGraph} from './diagnostic.ts';
import {scriptedInvestigator} from './model.ts';

function evidence():any {
 return {run_id:'managed-memory',table_oid:42,column_number:2,observed_at:new Date().toISOString(),decision:'investigate',lookup_column:'lookup',covering_index:false,
 plan:{node:'Sort',examined_rows:50000,returned_rows:50000,shared_blocks:100},managed_memory:{function_oid:300,baseline_work_mem_bytes:1048576,candidate_bytes:33554432,concurrent_boost_slots:3,
 available_memory_bytes:700*1024*1024,total_memory_bytes:1024*1024*1024,memory_observed_at:new Date().toISOString(),outer_temp_bytes:16000000,inner_temp_bytes:16000000,sort_rows:50000,identity_verified:true,fallback_preserves_baseline:true}};
}
test('ordinary function memory uses real capacity and original-memory fallback',async()=>{
 let calls=0;const g=buildGraph(async(read,enrollment)=>{calls++;return scriptedInvestigator(read,enrollment)});
 assert.equal((await g.invoke({evidence:evidence()})).proposal?.repair,'memory');
 for(const mutate of [(e:any)=>{e.managed_memory.available_memory_bytes=300*1024*1024},(e:any)=>{e.managed_memory.fallback_preserves_baseline=false},(e:any)=>{e.managed_memory.inner_temp_bytes=0},(e:any)=>{e.managed_memory.memory_observed_at=new Date(Date.now()-31000).toISOString()},(e:any)=>{e.plan.returned_rows=100000}]){
  const e=evidence();mutate(e);assert.equal((await g.invoke({evidence:e})).proposal?.outcome,'no_change');
 }
 assert.equal(calls,1);
 const mixed=evidence();mixed.analyze={estimated_rows:1,actual_rows:100};assert.equal(evidenceSchema.safeParse(mixed).success,false);
});
test('managed memory crosses diagnostic bridge with exact function evidence',async()=>{
 const e=evidence(),identity={run_id:e.run_id,table_oid:e.table_oid,column_number:e.column_number};
 const initial=[['plan',e.plan],['indexes',{covering_index:false}],['memory',e.managed_memory]].map(([tool,data])=>({...identity,tool,observed_at:e.observed_at,status:'available',data}));
 const graph=buildDiagnosticGraph(async()=>({decision:{action:'propose',repair:'memory',tools:[],assessments:[{cause:'memory',assessment:'supported',blocks_decision:false,reason:'Exact function spill with real capacity and fallback.'},{cause:'index',assessment:'rejected',blocks_decision:false,reason:'Measured bounded sort.'}],reason:'Try bounded extra memory and compare actual traffic.'}}),async()=>{throw Error('unexpected read')});
 const result=await graph.invoke({input:{...identity,decision:'investigate',initial} as any});assert.equal(result.proposal?.repair,'memory');assert.equal(result.proposal?.outcome,'propose');
});
