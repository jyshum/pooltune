import {test} from 'node:test';
import assert from 'node:assert/strict';
import {buildGraph} from './graph.ts';
import {buildDiagnosticGraph} from './diagnostic.ts';
import {scriptedInvestigator} from './model.ts';
function evidence():any {
 const now=Date.now();const earlier={Target:{DatabaseOID:1,TableOID:17000,Schema:'public',Relation:'orders'},At:new Date(now-3000).toISOString(),Boot:new Date(now-3600000).toISOString(),StatsReset:new Date(now-3600000).toISOString(),LastAutovacuum:'1970-01-01T00:00:00Z',ScaleOption:'',ScaleFactor:.2,EstimatedRows:4000,BaseThreshold:50,MaximumThreshold:-1,DeadTuples:700,Cleanups:0,RelationBytes:1000000,AutomaticCleanups:0,Updated:0,Deleted:0,AutovacuumNaptimeSeconds:1,AutovacuumEnabled:true,OnlyDeadTupleTrigger:true};
 const later={...earlier,At:new Date(now-1000).toISOString(),DeadTuples:750,Updated:750};
 return {run_id:'managed-cleanup',table_oid:17000,column_number:1,observed_at:new Date(now).toISOString(),decision:'investigate',lookup_column:'lookup',covering_index:true,plan:{node:'Index Only Scan',examined_rows:3000,returned_rows:3000,shared_blocks:2000},vacuum:{heap_fetches:2000,scan_rows:3000,conditions_clear:true,cleanup_trigger:{earlier,later,next_scale_factor:.15}}};
}
test('cleanup proposal sees real growing backlog and exact bounded threshold',async()=>{
 let calls=0;const graph=buildGraph(async(read,enrollment)=>{calls++;assert.equal(enrollment.vacuum?.cleanup_trigger?.next_scale_factor,.15);return scriptedInvestigator(read,enrollment)});
 assert.equal((await graph.invoke({evidence:evidence()})).proposal?.repair,'vacuum');
 for(const mutate of [(e:any)=>e.vacuum.cleanup_trigger.next_scale_factor=.01,(e:any)=>e.vacuum.cleanup_trigger.later.DeadTuples=700,(e:any)=>e.vacuum.cleanup_trigger.later.Updated=0,(e:any)=>e.vacuum.cleanup_trigger.later.OnlyDeadTupleTrigger=false,(e:any)=>e.table_oid++,(e:any)=>e.vacuum.cleanup_trigger.later.Boot=new Date().toISOString(),(e:any)=>e.vacuum.cleanup_trigger.later.AutovacuumNaptimeSeconds=121]) {
 const e=evidence();mutate(e);assert.equal((await graph.invoke({evidence:e})).proposal?.outcome,'no_change');
 }
 assert.equal(calls,1);
});
test('cleanup-trigger evidence survives the diagnostic bridge',async()=>{
 const e=evidence(),identity={run_id:e.run_id,table_oid:e.table_oid,column_number:e.column_number};
 const initial=[['plan',e.plan],['indexes',{covering_index:true}],['vacuum',e.vacuum]].map(([tool,data])=>({...identity,tool,observed_at:e.observed_at,status:'available',data}));
 const graph=buildDiagnosticGraph(async()=>({decision:{action:'propose',repair:'vacuum',tools:[],assessments:[{cause:'vacuum',assessment:'supported',blocks_decision:false,reason:'Growing backlog exceeds the supplied bounded threshold.'},{cause:'index',assessment:'rejected',blocks_decision:false,reason:'Usable index exists.'}],reason:'Trial the supplied automatic-cleanup threshold.'}}),async()=>{throw Error('unexpected read')});
 const result=await graph.invoke({input:{...identity,decision:'investigate',initial} as any});assert.equal(result.proposal?.outcome,'propose');assert.equal(result.proposal?.repair,'vacuum');
});
