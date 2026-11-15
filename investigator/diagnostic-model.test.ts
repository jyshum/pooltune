import assert from 'node:assert/strict';
import test from 'node:test';
import { AIMessage } from '@langchain/core/messages';
import { parseDiagnosticModelOutput } from './diagnostic-model.ts';

test('classifies rejected model output without accepting or disclosing raw content',()=>{
 const input={action:'no_change',repair:null,tools:[],assessments:[{cause:'index',assessment:'rejected',reason:'x'.repeat(201)},{cause:'analyze',assessment:'uncertain',reason:'Unknown'}],reason:'Insufficient evidence'};
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
