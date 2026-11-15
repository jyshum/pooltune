import assert from 'node:assert/strict';
import test from 'node:test';
import {PassThrough,Writable} from 'node:stream';
import {MemorySaver} from '@langchain/langgraph';
import {buildDiagnosticGraph,type DiagnosticInput,type Observation,type DiagnosticDecider} from './diagnostic.ts';
import {runDiagnosticProtocol} from './diagnostic-protocol.ts';
const id={run_id:'resume-protocol',table_oid:42,column_number:2};
const obs=(tool:Observation['tool'],data:unknown):Observation=>({...id,tool,data,status:'available',observed_at:new Date().toISOString()});
const initial=[obs('plan',{node:'Seq Scan',examined_rows:100000,returned_rows:1,shared_blocks:1000}),obs('indexes',{covering_index:true})];
const input:DiagnosticInput={...id,decision:'investigate',initial};
const analyze=obs('analyze',{estimated_rows:100000,actual_rows:1,sampled_lookup_fraction:1});
const decide:DiagnosticDecider=async view=>({decision:{action:view.observations.some(o=>o.tool==='analyze')?'propose':'request_evidence',repair:view.observations.some(o=>o.tool==='analyze')?'analyze':null,tools:view.observations.some(o=>o.tool==='analyze')?[]:['analyze'],assessments:[{cause:'analyze',assessment:view.observations.some(o=>o.tool==='analyze')?'supported':'uncertain',blocks_decision:!view.observations.some(o=>o.tool==='analyze'),reason:'Check distribution.'},{cause:'index',assessment:'rejected',blocks_decision:false,reason:'Index exists.'}],reason:'Check stale distribution.'}});

test('public protocol resumes the same checkpoint and restores Go evidence receipts',async()=>{
 const saver=new MemorySaver();const graph=buildDiagnosticGraph(decide,async()=>analyze,saver);
 await graph.invoke({input},{configurable:{thread_id:id.run_id},interruptAfter:['request_evidence']});
 const stream=new PassThrough();stream.write(JSON.stringify(input)+'\n');
 const frames:any[]=[];
 const output=new Writable({write(chunk,_encoding,done){const frame=JSON.parse(String(chunk));frames.push(frame);if(frame.type==='model_start')stream.write(JSON.stringify({permit:true})+'\n');if(frame.type==='read')stream.write(JSON.stringify({observation:analyze})+'\n');done();}});
 await runDiagnosticProtocol(decide,saver,stream,output);
 const result=frames.at(-1).result;
 assert.equal(result.proposal.outcome,'propose');assert.equal(result.reads,1);assert.equal(result.model_calls,2);
 assert.equal(frames.filter(f=>f.type==='read').length,1);
});

test('public resume rejects changed enrollment before requesting evidence',async()=>{
 const saver=new MemorySaver();const graph=buildDiagnosticGraph(decide,async()=>analyze,saver);
 await graph.invoke({input},{configurable:{thread_id:id.run_id},interruptAfter:['request_evidence']});
 const stream=new PassThrough();stream.end(JSON.stringify({...input,table_oid:99})+'\n');
 let writes=0;const output=new Writable({write(_c,_e,done){writes++;done();}});
 await assert.rejects(runDiagnosticProtocol(decide,saver,stream,output));assert.equal(writes,0);
});
