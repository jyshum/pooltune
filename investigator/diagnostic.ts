import { END, START, StateGraph, StateSchema } from '@langchain/langgraph';
import type { BaseCheckpointSaver } from '@langchain/langgraph';
import { z } from 'zod';
import { evidenceFields, orderedColumnsSchema, screenEvidence, type Evidence, type Proposal } from './graph.ts';

export const toolSchema=z.enum(['plan','indexes','analyze','statistics','vacuum','memory','cron']);
export type DiagnosticTool=z.infer<typeof toolSchema>;
const repairSchema=z.enum(['index','analyze','statistics','vacuum','memory','cron']);
const identityShape={run_id:z.string().min(1).max(64),table_oid:z.number().int().positive().max(4294967295),column_number:z.number().int().positive().max(32767),column_numbers:orderedColumnsSchema.optional()};
export const observationSchema=z.strictObject({...identityShape,tool:toolSchema,observed_at:z.iso.datetime(),status:z.enum(['available','unavailable']),data:z.unknown().optional()});
export type Observation=z.infer<typeof observationSchema>;
export const diagnosticInputSchema=z.strictObject({...identityShape,decision:z.enum(['investigate','no_change','insufficient_evidence']),initial:z.array(observationSchema).max(7)});
export type DiagnosticInput=z.infer<typeof diagnosticInputSchema>;
export const diagnosticDecisionSchema=z.strictObject({
 action:z.enum(['request_evidence','propose','no_change']),repair:repairSchema.nullable(),tools:z.array(toolSchema).max(3),
 assessments:z.array(z.strictObject({cause:z.enum([...repairSchema.options,'unsupported']),assessment:z.enum(['supported','rejected','uncertain']),blocks_decision:z.boolean(),reason:z.string().min(1).max(200)})).min(2).max(7),
 reason:z.string().min(1).max(300),
});
export type DiagnosticDecision=z.infer<typeof diagnosticDecisionSchema>;
export type DiagnosticView={identity:{run_id:string;table_oid:number;column_number:number;column_numbers?:[2,3]};observations:Observation[];history:DiagnosticDecision[];remaining_model_calls:number;remaining_reads:number};
export type DiagnosticDecider=(view:DiagnosticView,signal:AbortSignal)=>Promise<{decision:unknown;model?:string;input_tokens?:number;output_tokens?:number}>;
export type DiagnosticReader=(tool:DiagnosticTool,signal:AbortSignal)=>Promise<unknown>;
const stateSchema=new StateSchema({input:diagnosticInputSchema,observations:z.array(observationSchema).default([]),history:z.array(diagnosticDecisionSchema).default([]),
 model_calls:z.number().default(0),reads:z.number().default(0),model_ms:z.number().default(0),tool_ms:z.number().default(0),
 started_at:z.number().default(0),current:diagnosticDecisionSchema.optional(),stop_reason:z.string().default(''),
 proposal:z.custom<Proposal>().optional(),model:z.string().default(''),input_tokens:z.number().default(0),output_tokens:z.number().default(0)});
const schemas={plan:evidenceFields.shape.plan,indexes:z.strictObject({covering_index:z.boolean()}),analyze:evidenceFields.shape.analyze.unwrap(),statistics:evidenceFields.shape.statistics.unwrap(),vacuum:evidenceFields.shape.vacuum.unwrap(),memory:evidenceFields.shape.memory.unwrap(),cron:evidenceFields.shape.cron.unwrap()};
function identity(input:DiagnosticInput){return {run_id:input.run_id,table_oid:input.table_oid,column_number:input.column_number,...(input.column_numbers?{column_numbers:input.column_numbers}:{})};}
function checkedObservation(raw:unknown,input:DiagnosticInput,now:number,expected?:DiagnosticTool):Observation {
 const o=observationSchema.parse(raw);
 if(o.run_id!==input.run_id||o.table_oid!==input.table_oid||o.column_number!==input.column_number||JSON.stringify(o.column_numbers)!==JSON.stringify(input.column_numbers)||(expected&&o.tool!==expected))throw Error('identity');
 const age=now-Date.parse(o.observed_at);
 if(age<0||age>30000)throw Error('expired');
 if(o.status==='available')schemas[o.tool].parse(o.data);
 else if(o.data!==undefined&&o.data!==null)throw Error('unavailable data');
 return o;
}
async function bounded<T>(action:(signal:AbortSignal)=>Promise<T>,ms:number):Promise<T>{
 const controller=new AbortController();let timer:ReturnType<typeof setTimeout>;
 try{return await Promise.race([action(controller.signal),new Promise<never>((_,reject)=>{timer=setTimeout(()=>{controller.abort();reject(Error('deadline'));},Math.max(1,ms));})]);}
 finally{clearTimeout(timer!);}
}

export function buildDiagnosticGraph(decide:DiagnosticDecider,read:DiagnosticReader,checkpointer?:BaseCheckpointSaver,now:()=>number=Date.now){
 return new StateGraph(stateSchema)
 .addNode('screen_evidence',s=>{
  const started_at=s.started_at||now();
  if(s.input.decision!=='investigate')return {started_at,stop_reason:s.input.decision};
  try{
   const observations=s.input.initial.map(o=>checkedObservation(o,s.input,now()));
   if(new Set(observations.map(o=>o.tool)).size!==observations.length)throw Error('duplicate');
   return {started_at,observations};
  }catch{return {started_at,stop_reason:'invalid_evidence'};}
 })
 .addNode('compare_causes',async s=>{
  if(now()-s.started_at>=20000)return {stop_reason:'time_budget_exhausted'};
  if(s.model_calls>=3)return {stop_reason:'model_budget_exhausted'};
  try{for(const o of s.observations)checkedObservation(o,s.input,now());}catch{return {stop_reason:'invalid_evidence'};}
  const start=performance.now();
  try{
   const result=await bounded(signal=>decide({identity:identity(s.input),observations:s.observations,history:s.history,remaining_model_calls:3-s.model_calls,remaining_reads:6-s.reads},signal),Math.min(8500,20000-(now()-s.started_at)));
   const meta={model_calls:s.model_calls+1,model_ms:s.model_ms+performance.now()-start,model:result.model||s.model,input_tokens:s.input_tokens+(result.input_tokens||0),output_tokens:s.output_tokens+(result.output_tokens||0)};
   const parsed=diagnosticDecisionSchema.safeParse(result.decision);
   if(!parsed.success)return {...meta,stop_reason:'invalid_model_output'};
   const d=parsed.data;
   if(new Set(d.assessments.map(a=>a.cause)).size!==d.assessments.length||
    (d.action==='request_evidence'&&(d.repair!==null||d.tools.length===0||new Set(d.tools).size!==d.tools.length))||
    (d.action!=='request_evidence'&&d.tools.length!==0)||
    (d.action==='propose'&&d.repair===null)||(d.action==='no_change'&&d.repair!==null))return {...meta,stop_reason:'invalid_model_output'};
   if(d.action==='request_evidence'){
    if(meta.model_calls>=3)return {...meta,current:d,history:[...s.history,d],stop_reason:'model_budget_exhausted'};
    if(s.reads+d.tools.length>6)return {...meta,stop_reason:'evidence_budget_exhausted'};
    if(d.tools.some(t=>s.observations.some(o=>o.tool===t)))return {...meta,stop_reason:'repeated_evidence_request'};
   }
   return {...meta,current:d,history:[...s.history,d]};
  }catch(error){return {model_calls:s.model_calls+1,model_ms:s.model_ms+performance.now()-start,stop_reason:error instanceof Error&&error.name==='DiagnosticModelOutputError'&&['model_output_length','model_output_invalid'].includes(error.message)?error.message:'model_failed'};}
 })
 .addNode('request_evidence',async s=>{
  const observations=[...s.observations];let reads=s.reads;const start=performance.now();
  for(const tool of s.current!.tools){
   if(now()-s.started_at>=20000)return {observations,reads,tool_ms:s.tool_ms+performance.now()-start,stop_reason:'time_budget_exhausted'};
   reads++;
   let raw:unknown;
   try{raw=await bounded(signal=>read(tool,signal),Math.min(3000,20000-(now()-s.started_at)));}
   catch{return {observations,reads,tool_ms:s.tool_ms+performance.now()-start,stop_reason:'evidence_failed'};}
   try{observations.push(checkedObservation(raw,s.input,now(),tool));}
   catch{return {observations,reads,tool_ms:s.tool_ms+performance.now()-start,stop_reason:'invalid_evidence'};}
  }
  return {observations,reads,tool_ms:s.tool_ms+performance.now()-start};
 })
 .addNode('finish',s=>{
  let stop_reason=s.stop_reason;
  let proposal:Proposal={...identity(s.input),outcome:'no_change',reason:stop_reason||s.current?.reason||'Insufficient evidence.'};
  if(!stop_reason&&s.current?.action==='propose'){
   try{
    if(now()-s.started_at>=20000)throw Error('deadline');
    const observations=s.observations.map(o=>checkedObservation(o,s.input,now()));
    const get=(tool:DiagnosticTool)=>observations.find(o=>o.tool===tool&&o.status==='available');
    const p=get('plan'),i=get('indexes'),repair=s.current.repair!;
    const a=get('analyze'),stats=get('statistics');
    if(a&&stats){
     const av=schemas.analyze.parse(a.data),sv=schemas.statistics.parse(stats.data);
     if(av.sampled_lookup_fraction!==undefined&&av.sampled_lookup_fraction>=.9&&av.actual_rows*10<av.estimated_rows&&sv.correlated_columns&&sv.cases.every(c=>Math.max(c.actual_rows,c.estimated_rows)>=10*Math.min(c.actual_rows,c.estimated_rows))){
      return {proposal:{...identity(s.input),outcome:'no_change' as const,reason:'Stale distribution evidence conflicts with the fresh-statistics measurement contract.'},stop_reason:'contradictory_evidence'};
     }
    }
    if(!p||!i||(repair!=='index'&&!get(repair)))throw Error('missing support');
    const supported=s.current.assessments.filter(a=>a.assessment==='supported');
    if(supported.length!==1||supported[0].cause!==repair||s.current.assessments.some(a=>a.blocks_decision))throw Error('ambiguous');
    const raw={...identity(s.input),observed_at:p.observed_at,decision:'investigate',lookup_column:'lookup',plan:p.data,...i.data as object,...(repair==='index'?{}:{[repair]:get(repair)!.data})};
    const e=evidenceFields.parse(raw) as Evidence;
    if(repair==='analyze'){
     const a=e.analyze!;
     const observed=e.plan.examined_rows>0?a.actual_rows/e.plan.examined_rows:0;
     if(e.plan.node!=='Seq Scan'||a.actual_rows!==e.plan.returned_rows||a.sampled_lookup_fraction===undefined||observed<=0||a.sampled_lookup_fraction/observed<10)throw Error('staleness not established');
    }

    if(screenEvidence(e).proposal)throw Error('ineligible');
    proposal={...identity(s.input),outcome:'propose',reason:s.current.reason,...(repair==='index'?{}:{repair})};
   }catch{stop_reason='unsupported_proposal';proposal.reason='Available evidence does not justify this repair.';}
  }
  // These metrics are measured by the runner, never supplied in model output.
  if(s.model_calls&&s.model)proposal.model_call={model:s.model,duration_ms:s.model_ms,requests:s.model_calls,input_tokens:s.input_tokens,output_tokens:s.output_tokens};
  return {proposal,stop_reason};
 })
 .addEdge(START,'screen_evidence')
 .addConditionalEdges('screen_evidence',s=>s.stop_reason?'finish':'compare_causes',['finish','compare_causes'])
 .addConditionalEdges('compare_causes',s=>s.stop_reason||s.current?.action!=='request_evidence'?'finish':'request_evidence',['finish','request_evidence'])
 .addConditionalEdges('request_evidence',s=>s.stop_reason?'finish':'compare_causes',['finish','compare_causes'])
 .addEdge('finish',END).compile({checkpointer});
}
