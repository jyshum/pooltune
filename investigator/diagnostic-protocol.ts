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
  const graph=buildDiagnosticGraph(decide,async(tool,signal)=>{
   if(signal.aborted)throw Error('Cancelled evidence');
   await send({type:'read',tool});
   const reply=await next();
   if(Object.keys(reply).length!==1||!('observation' in reply))throw Error('Invalid evidence frame');
   return reply.observation;
  },saver);
  const result=await graph.invoke({input:enrollment},{configurable:{thread_id:enrollment.run_id},durability:'sync',recursionLimit:12});
  await send({type:'result',result:{proposal:result.proposal,model_calls:result.model_calls,reads:result.reads,model_ms:result.model_ms,tool_ms:result.tool_ms,stop_reason:result.stop_reason,history:result.history}});
 }finally{lines.close();}
}
