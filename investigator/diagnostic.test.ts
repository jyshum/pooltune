import assert from 'node:assert/strict';
import test from 'node:test';
import { MemorySaver } from '@langchain/langgraph';
import { buildDiagnosticGraph, type Observation, type DiagnosticTool, type DiagnosticInput } from './diagnostic.ts';

const now=Date.now();
const identity={run_id:'diagnostic-test',table_oid:42,column_number:2};
const observation=(tool:DiagnosticTool,data:unknown):Observation=>({...identity,tool,observed_at:new Date(now).toISOString(),status:'available',data});
const input=(initial:Observation[]=[]):DiagnosticInput=>({...identity,decision:'investigate',initial});
const plan={node:'Seq Scan',examined_rows:100000,returned_rows:1,shared_blocks:1200};
const compare=[{cause:'index',assessment:'rejected',blocks_decision:false,reason:'A covering index exists.'},{cause:'analyze',assessment:'supported',blocks_decision:false,reason:'Stored frequencies contradict measured rows.'}];
const propose=(repair='analyze')=>({action:'propose',repair,tools:[],assessments:compare,reason:'Refresh stale lookup statistics.'});
const request=(tools:string[])=>({action:'request_evidence',repair:null,tools,assessments:[{cause:'analyze',assessment:'uncertain',blocks_decision:true,reason:'Need stored frequencies.'},{cause:'statistics',assessment:'uncertain',blocks_decision:true,reason:'Need evidence of relationships.'}],reason:'Distinguish stale statistics from missing relationship information.'});
const base=[observation('plan',plan),observation('indexes',{covering_index:true})];

test('requests a missing measurement, compares mixed causes, and returns the supported repair',async()=>{
 let calls=0;const read:string[]=[];
 const graph=buildDiagnosticGraph(async view=>({decision:++calls===1?request(['analyze','statistics']):propose()}),async tool=>{
  read.push(tool);return observation(tool,tool==='analyze'?{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1}:{correlated_columns:false,cases:[{estimated_rows:1,actual_rows:1},{estimated_rows:1,actual_rows:1}]});
 });
 const out=await graph.invoke({input:input(base)});
 assert.equal(out.proposal?.repair,'analyze');assert.equal(out.proposal?.outcome,'propose');
 assert.equal(out.model_calls,2);assert.deepEqual(read,['analyze','statistics']);assert.equal(out.history.length,2);
 assert.equal(out.observations.length,4);
});

test('reuses sufficient evidence in one model call without a reader call',async()=>{
 const graph=buildDiagnosticGraph(async()=>({decision:propose()}),async()=>{throw Error('Must reuse evidence');});
 const out=await graph.invoke({input:input([...base,observation('analyze',{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1})])});
 assert.equal(out.proposal?.outcome,'propose');assert.equal(out.model_calls,1);assert.equal(out.reads,0);
});

test('healthy evidence skips the model and missing support cannot become a repair',async()=>{
 const graph=buildDiagnosticGraph(async()=>({decision:propose()}),async()=>{throw Error('unexpected read');});
 const healthy=await graph.invoke({input:{...input(base),decision:'no_change'}});
 assert.equal(healthy.model_calls,0);assert.equal(healthy.proposal?.outcome,'no_change');
 assert.equal((await graph.invoke({input:input(base)})).proposal?.outcome,'no_change');
});

test('unavailable evidence is visible to Claude without being treated as negative evidence',async()=>{
 let calls=0;
 const graph=buildDiagnosticGraph(async view=>{
  if(++calls===1)return {decision:request(['analyze'])};
  assert.equal(view.observations.at(-1)?.status,'unavailable');
  return {decision:{...propose(),action:'no_change',repair:null,reason:'Cannot distinguish causes without statistics.'}};
 },async tool=>({...observation(tool,null),status:'unavailable'}));
 const out=await graph.invoke({input:input(base)});assert.equal(out.proposal?.outcome,'no_change');assert.equal(out.model_calls,2);
});

test('repeat requests stop without repeating database work',async()=>{
 let reads=0;
 const graph=buildDiagnosticGraph(async()=>({decision:request(['analyze'])}),async tool=>{reads++;return observation(tool,{estimated_rows:1,actual_rows:1});});
 const out=await graph.invoke({input:input(base)});assert.equal(reads,1);assert.equal(out.model_calls,2);assert.equal(out.proposal?.outcome,'no_change');assert.equal(out.stop_reason,'repeated_evidence_request');
});

test('three model turns bound a wandering investigation',async()=>{
 let calls=0;
 const graph=buildDiagnosticGraph(async()=>({decision:request([['analyze','statistics','vacuum'][calls++]])}),async tool=>({...observation(tool,null),status:'unavailable'}));
 const out=await graph.invoke({input:input(base)});assert.equal(out.model_calls,3);assert.equal(out.reads,2);assert.equal(out.stop_reason,'model_budget_exhausted');assert.equal(out.proposal?.outcome,'no_change');
});

for(const [name,mutate] of [
 ['wrong identity',(o:any)=>({...o,table_oid:99})],
 ['stale evidence',(o:any)=>({...o,observed_at:new Date(now-31000).toISOString()})],
 ['future evidence',(o:any)=>({...o,observed_at:new Date(now+60000).toISOString()})],
 ['invalid data',(o:any)=>({...o,data:{sql:'DROP TABLE x'}})],
] as const)test(`rejects ${name} from a reader`,async()=>{
 const graph=buildDiagnosticGraph(async()=>({decision:request(['analyze'])}),async tool=>mutate(observation(tool,{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1})),undefined,()=>now);
 const out=await graph.invoke({input:input(base)});assert.equal(out.proposal?.outcome,'no_change');assert.equal(out.stop_reason,'invalid_evidence');
});

test('unknown tool and malformed model output fail closed',async()=>{
 for(const decision of [request(['raw_sql']),{...propose(),sql:'CREATE INDEX'}, {...propose(),repair:'restart_database'}]){
  const graph=buildDiagnosticGraph(async()=>({decision}),async()=>{throw Error('No tool should run');});
  const out=await graph.invoke({input:input(base)});assert.equal(out.proposal?.outcome,'no_change');assert.equal(out.stop_reason,'invalid_model_output');
 }
});

test('model failures and reader failures end without a proposal',async()=>{
 for(const failure of ['model','reader']){
  const graph=buildDiagnosticGraph(async()=>{if(failure==='model')throw Error('secret');return {decision:request(['analyze'])};},async()=>{throw Error('secret');});
  const out=await graph.invoke({input:input(base)});assert.equal(out.proposal?.outcome,'no_change');assert.ok(!JSON.stringify(out).includes('secret'));
 }
});

test('checkpoint resume does not repeat completed evidence reads',async()=>{
 let reads=0;const saver=new MemorySaver();
 const graph=buildDiagnosticGraph(async view=>({decision:view.observations.some(o=>o.tool==='analyze')?propose():request(['analyze'])}),async tool=>{reads++;return observation(tool,{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1});},saver);
 const config={configurable:{thread_id:'resume'},interruptAfter:['request_evidence' as const]};
 await graph.invoke({input:input(base)},config);
 const out=await graph.invoke(null,{configurable:{thread_id:'resume'}});
 assert.equal(out.proposal?.outcome,'propose');assert.equal(reads,1);assert.equal(out.model_calls,2);
});

test('contradictory stale and freshly analyzed relationship evidence cannot authorize a repair',async()=>{
 const graph=buildDiagnosticGraph(async()=>({decision:{...propose('statistics'),assessments:[{cause:'statistics',assessment:'supported',blocks_decision:false,reason:'Relationship error.'},{cause:'analyze',assessment:'rejected',blocks_decision:false,reason:'Ignore stale frequencies.'}]}}),async()=>{throw Error('Unexpected read');});
 const out=await graph.invoke({input:input([...base,observation('analyze',{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1}),observation('statistics',{correlated_columns:true,cases:[{estimated_rows:25000,actual_rows:1},{estimated_rows:25000,actual_rows:1}]})])});
 assert.equal(out.proposal?.outcome,'no_change');assert.equal(out.stop_reason,'contradictory_evidence');
});

test('the hosted diagnostic accepts a column-target trial and declines contradictory freshness',async()=>{
 const graph=buildDiagnosticGraph(async()=>({decision:{
  action:'propose',repair:'statistics',tools:[],
  assessments:[{cause:'statistics',assessment:'supported',blocks_decision:false,reason:'A fresh, full sample omitted a frequent value.'},
   {cause:'analyze',assessment:'rejected',blocks_decision:false,reason:'The existing sample was already analyzed recently.'}],
  reason:'Trial the bounded column target increase.',
 }}),async()=>{throw Error('unexpected read');});
 const complete=[observation('plan',{node:'Index Only Scan',examined_rows:1000,returned_rows:1000,shared_blocks:200}),
  base[1],observation('statistics',{correlated_columns:false,cases:[{estimated_rows:20,actual_rows:1000}],
   column_target:{current_target:100,next_target:200}})];
 const result=await graph.invoke({input:input(complete)});
 assert.equal(result.proposal?.repair,'statistics');
 assert.equal(result.model_calls,1);
 const stale=observation('analyze',{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1});
 const contradictory=await graph.invoke({input:input([...complete,stale])});
 assert.equal(contradictory.proposal?.outcome,'no_change');
 assert.equal(contradictory.stop_reason,'contradictory_evidence');
});

test('the graph reports its model deadline and aborts the pending call at the existing limit',async t=>{
 t.mock.timers.enable({apis:['setTimeout']});
 const started=Promise.withResolvers<void>();
 let aborted=false;
 const graph=buildDiagnosticGraph(async (_view,signal)=>await new Promise((_resolve,reject)=>{
  signal.addEventListener('abort',()=>{aborted=true;reject(Error('private transport detail'));},{once:true});
  started.resolve();
 }),async()=>{throw Error('unexpected read');});
 const run=graph.invoke({input:input(base)});
 await started.promise;
 t.mock.timers.tick(8499);
 assert.equal(aborted,false);
 t.mock.timers.tick(1);
 const result=await run;
 assert.equal(aborted,true);
 assert.equal(result.model_calls,1);
 assert.equal(result.proposal?.outcome,'no_change');
 assert.equal(result.stop_reason,'model_timeout');
 assert.equal(result.proposal?.reason,'The model did not respond within its time budget.');
});

test('provider timeouts use a typed stop reason without exposing provider detail',async()=>{
 const providerError=new Error('private request status detail');
 providerError.name='APIConnectionTimeoutError';
 const graph=buildDiagnosticGraph(async()=>{throw providerError;},async()=>{throw Error('unexpected read');});
 const result=await graph.invoke({input:input(base)});
 assert.equal(result.proposal?.outcome,'no_change');
 assert.equal(result.stop_reason,'model_timeout');
 assert.doesNotMatch(result.proposal?.reason??'',/private request/);
});

test('expired initial evidence and elapsed investigation deadline stop without more work',async()=>{
 let clock=now,calls=0;
 const graph=buildDiagnosticGraph(async()=>{calls++;clock+=21000;return {decision:propose()};},async()=>{throw Error('Unexpected read');},undefined,()=>clock);
 const stale=await graph.invoke({input:input(base.map(o=>({...o,observed_at:new Date(now-31000).toISOString()})))});
 assert.equal(stale.proposal?.outcome,'no_change');assert.equal(calls,0);
 const out=await graph.invoke({input:input([...base,observation('analyze',{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1})])});
 assert.equal(out.proposal?.outcome,'no_change');assert.equal(calls,1);
});

test('an unresolved relevant alternative vetoes a proposed repair',async()=>{
 const graph=buildDiagnosticGraph(async()=>({decision:{...propose(),assessments:[{cause:'analyze',assessment:'supported',blocks_decision:false,reason:'Stale frequency.'},{cause:'statistics',assessment:'uncertain',blocks_decision:true,reason:'Missing evidence could change the repair.'}]}}),async()=>{throw Error('Unexpected read');});
 const out=await graph.invoke({input:input([...base,observation('analyze',{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1})])});
 assert.equal(out.proposal?.outcome,'no_change');
});

test('estimate error alone is not proof that statistics are stale',async()=>{
 const graph=buildDiagnosticGraph(async()=>({decision:propose()}),async()=>{throw Error('Unexpected read');});
 const out=await graph.invoke({input:input([...base,observation('analyze',{estimated_rows:100000,actual_rows:1})])});
 assert.equal(out.proposal?.outcome,'no_change');
});
