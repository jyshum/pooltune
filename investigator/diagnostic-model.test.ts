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
