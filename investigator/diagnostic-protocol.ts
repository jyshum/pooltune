import { isDeepStrictEqual } from 'node:util';
import { createInterface } from 'node:readline';
import type { Readable, Writable } from 'node:stream';
import type { BaseCheckpointSaver } from '@langchain/langgraph';
import { buildDiagnosticGraph, diagnosticInputSchema, type DiagnosticDecider } from './diagnostic.ts';

export async function runDiagnosticProtocol(decide:DiagnosticDecider,saver:BaseCheckpointSaver,input:Readable,output:Writable){
 const lines=createInterface({input});const iterator=lines[Symbol.asyncIterator]();
 const next=async()=>{const item=await iterator.next();if(item.done||Buffer.byteLength(item.value)>60000)throw Error('Invalid protocol input');return JSON.parse(item.value);};
 const send=(data:unknown)=>new Promise<void>((resolve,reject)=>output.write(JSON.stringify(data)+'\n',e=>e?reject(e):resolve()));
 try{
  const enrollment=diagnosticInputSchema.parse(await next());
  const read=async(tool:string,signal:AbortSignal)=>{
   if(signal.aborted)throw Error('Cancelled evidence');
   await send({type:'read',tool});
   const reply=await next();
   if(Object.keys(reply).length!==1||!('observation' in reply))throw Error('Invalid evidence frame');
   return reply.observation;
  };
  const graph=buildDiagnosticGraph(async(view,signal)=>{
   await send({type:'model_start'});
   const permit=await next();
   if(Object.keys(permit).length!==1||permit.permit!==true)throw Error('Model attempt budget exhausted');
   return decide(view,signal);
  },read,saver);
  const config={configurable:{thread_id:enrollment.run_id},durability:'sync' as const,recursionLimit:12};
  const saved=await graph.getState(config);
  const resume=Boolean(saved.values?.input);
  if(resume){
   if(!isDeepStrictEqual(saved.values.input,enrollment))throw Error('Changed checkpoint enrollment');
   // Restore only receipts Go already persisted. Go's journal returns the same
   // observation without repeating a customer query, and both sides recheck it.
   for(const observation of saved.values.observations??[]){
    if(enrollment.initial.some(o=>o.tool===observation.tool))continue;
    const restored=await read(observation.tool,new AbortController().signal);
    if(!isDeepStrictEqual(restored,observation))throw Error('Changed evidence receipt');
   }
  }
  const result=await graph.invoke(resume?null:{input:enrollment},config);
  await send({type:'result',result:{proposal:result.proposal,model_calls:result.model_calls,reads:result.reads,model_ms:result.model_ms,tool_ms:result.tool_ms,stop_reason:result.stop_reason,history:result.history}});
 }finally{lines.close();}
}
