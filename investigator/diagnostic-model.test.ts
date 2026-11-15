import assert from 'node:assert/strict';
import test from 'node:test';
import { AIMessage } from '@langchain/core/messages';
import { parseDiagnosticModelOutput } from './diagnostic-model.ts';

test('classifies rejected model output without accepting or disclosing raw content',()=>{
 const input={action:'no_change',repair:null,tools:[],assessments:[{cause:'index',assessment:'rejected',blocks_decision:false,reason:'x'.repeat(201)},{cause:'analyze',assessment:'uncertain',blocks_decision:true,reason:'Unknown'}],reason:'Insufficient evidence'};
 const raw=new AIMessage({content:[{type:'tool_use',id:'test',name:'diagnostic_step',input}]});
 assert.throws(()=>parseDiagnosticModelOutput({parsed:null,raw}),e=>e instanceof Error&&e.message==='model_output_length');
});

import { summarizeDiagnosticEvidence } from './diagnostic-model.ts';
test('presents the measured match fraction and estimate error without choosing a repair',()=>{
 const id={run_id:'s',table_oid:1,column_number:2,observed_at:new Date().toISOString(),status:'available' as const};
 const summary=summarizeDiagnosticEvidence([{...id,tool:'plan',data:{node:'Seq Scan',examined_rows:100000,returned_rows:1,shared_blocks:1000}},{...id,tool:'analyze',data:{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1}}]);
 assert.deepEqual(summary.lookup_distribution,{observed_match_fraction:0.00001,stored_match_fraction:1,estimate_error_ratio:100000});
 assert.equal('recommended_repair' in summary,false);
});

import { diagnosticDecider } from './diagnostic-model.ts';
test('uses native structured output without unsupported forced tool choice',async t=>{
 const previous={key:process.env.ANTHROPIC_API_KEY,model:process.env.POOLTUNE_ANTHROPIC_MODEL};
 process.env.ANTHROPIC_API_KEY='test-key';process.env.POOLTUNE_ANTHROPIC_MODEL='claude-sonnet-5-5';
 t.after(()=>{if(previous.key===undefined)delete process.env.ANTHROPIC_API_KEY;else process.env.ANTHROPIC_API_KEY=previous.key;if(previous.model===undefined)delete process.env.POOLTUNE_ANTHROPIC_MODEL;else process.env.POOLTUNE_ANTHROPIC_MODEL=previous.model;});
 t.mock.method(globalThis,'fetch',async(_input:unknown,init?:RequestInit)=>{
  const body=JSON.parse(String(init?.body));assert.equal(body.tool_choice,undefined);assert.equal(body.output_config.format.type,'json_schema');
  return Response.json({id:'test',type:'message',role:'assistant',model:'claude-sonnet-5-5',content:[{type:'text',text:JSON.stringify({action:'no_change',repair:null,tools:[],assessments:[{cause:'index',assessment:'uncertain',blocks_decision:true,reason:'Missing plan.'},{cause:'analyze',assessment:'uncertain',blocks_decision:true,reason:'Missing statistics.'}],reason:'Insufficient evidence.'})}],stop_reason:'end_turn',stop_sequence:null,usage:{input_tokens:1,output_tokens:1}});
 });
 const out=await diagnosticDecider({identity:{run_id:'test',table_oid:1,column_number:2},observations:[],history:[],remaining_model_calls:1,remaining_reads:0},new AbortController().signal);
 assert.equal((out.decision as any).action,'no_change');
});

test('sends the complete prequalified column-target case with shorter Sonnet instructions',async t=>{
 const previous={key:process.env.ANTHROPIC_API_KEY,model:process.env.POOLTUNE_ANTHROPIC_MODEL};
 process.env.ANTHROPIC_API_KEY='test-key';process.env.POOLTUNE_ANTHROPIC_MODEL='claude-sonnet-5-5';
 t.after(()=>{if(previous.key===undefined)delete process.env.ANTHROPIC_API_KEY;else process.env.ANTHROPIC_API_KEY=previous.key;if(previous.model===undefined)delete process.env.POOLTUNE_ANTHROPIC_MODEL;else process.env.POOLTUNE_ANTHROPIC_MODEL=previous.model;});
 const decision={action:'request_evidence',repair:null,tools:['analyze'],
  assessments:[{cause:'statistics',assessment:'uncertain',blocks_decision:true,reason:'Need to rule out stale data.'},
   {cause:'analyze',assessment:'uncertain',blocks_decision:true,reason:'Need a stored frequency check.'}],reason:'Check the stored distribution.'};
 const calls:{model:string;max_tokens:number;system:unknown;messages:unknown;output_config:unknown}[]=[];
 t.mock.method(globalThis,'fetch',async(_input:unknown,init?:RequestInit)=>{
  calls.push(JSON.parse(String(init?.body)));
  return Response.json({id:'test',type:'message',role:'assistant',model:'claude-sonnet-5-5',
   content:[{type:'text',text:JSON.stringify(decision)}],stop_reason:'end_turn',stop_sequence:null,usage:{input_tokens:1,output_tokens:1}});
 });
 const identity={run_id:'column-target',table_oid:28,column_number:2};
 const base={...identity,observed_at:new Date().toISOString(),status:'available' as const};
 const observations=[
  {...base,tool:'plan' as const,data:{node:'Index Only Scan',examined_rows:73600,returned_rows:73600,shared_blocks:2000}},
  {...base,tool:'indexes' as const,data:{covering_index:true}},
  {...base,tool:'statistics' as const,data:{correlated_columns:false,cases:[{estimated_rows:1500,actual_rows:73600}],
   column_target:{current_target:1,next_target:2}}},
 ];
 const view={identity,observations,history:[],remaining_model_calls:3,remaining_reads:6};
 const signal=new AbortController().signal;
 const exact=await diagnosticDecider(view,signal);
 await diagnosticDecider({...view,observations:[...observations,{...base,tool:'analyze' as const,
  data:{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1}}]},signal);
 await diagnosticDecider({...view,observations:observations.map(o=>o.tool==='statistics'?
  {...o,data:{correlated_columns:true,cases:[{estimated_rows:50,actual_rows:1000},{estimated_rows:60,actual_rows:1100}]}}:o)},signal);
 assert.deepEqual(exact.decision,decision);
 const [compact,mixed,joint]=calls;
 assert.equal(compact.model,'claude-sonnet-5-5');
 assert.equal(compact.max_tokens,600);
 assert.equal(mixed.max_tokens,1200);
 assert.equal(joint.max_tokens,1200);
 assert.deepEqual(compact.output_config,mixed.output_config);
 const short=JSON.stringify(compact.system), general=JSON.stringify(mixed.system);
 assert.ok(short.length<general.length/2,'the exact prompt must materially reduce input bytes');
 assert.match(short,/plausible alternative/);
 assert.match(short,/no_change/);
 assert.match(short,/Go owns/);
 assert.deepEqual(mixed.system,joint.system);
 const messages=compact.messages as {content:string|{text:string}[]}[];
 const body=typeof messages[0].content==='string'?messages[0].content:messages[0].content[0].text;
 assert.deepEqual(JSON.parse(body).observations,observations);
});
