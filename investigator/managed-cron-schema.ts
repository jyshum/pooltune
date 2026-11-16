import {z} from 'zod';

const count=z.number().int().nonnegative();
const positive=z.number().int().positive();
const stamp=z.iso.datetime();
const counter=z.strictObject({Database:positive,Role:positive,Query:z.string().regex(/^-?\d+$/),Boot:stamp,Reset:stamp,Entry:stamp,Calls:count,Blocks:count,ExecutionMS:z.number().nonnegative()});
const measure=z.strictObject({before:counter,after:counter});
const window=z.strictObject({start:stamp,end:stamp,target:measure,read:measure,write:measure});
export const managedCronSchema=z.strictObject({
 job_id:positive,original_schedule:z.string().max(80),alternate_schedule:z.string().max(80),
 application_request_visibility:z.literal('unobserved'),
 window:z.strictObject({EveryMinutes:positive.max(60),CurrentMinute:count.max(59),ProposedMinute:count.max(59),DeadlineSeconds:positive.max(3600),MaxRuntimeSeconds:positive.max(20)}),
 generation:z.strictObject({pid:positive,started:stamp,postmaster:stamp}),
 periods:z.array(z.strictObject({cycle:stamp,run:z.strictObject({id:positive,job_id:positive,pid:positive,start:stamp,end:stamp,status:z.string().max(30)}),busy:window,control:window,quiet:window,blocks:z.array(z.strictObject({at:stamp,run_id:positive,job_id:positive,pid:positive,backend_start:stamp,waiting_pid:positive,waiting_role:positive,waiting_start:stamp,query_id:z.string().regex(/^-?\d+$/)})).max(100)})).length(3),
});
type Counter=z.infer<typeof counter>;
type Measure=z.infer<typeof measure>;
const time=(s:string)=>Date.parse(s);
const same=(a:Counter,b:Counter)=>a.Database===b.Database&&a.Role===b.Role&&a.Query===b.Query&&a.Boot===b.Boot&&a.Reset===b.Reset&&a.Entry===b.Entry;
const calls=(m:Measure)=>m.after.Calls-m.before.Calls;
const mean=(m:Measure)=>calls(m)>0?(m.after.ExecutionMS-m.before.ExecutionMS)/calls(m):0;
const monotonic=(a:Counter,b:Counter)=>same(a,b)&&b.Calls>=a.Calls&&b.Blocks>=a.Blocks&&b.ExecutionMS>=a.ExecutionMS;

// The model sees completed database work only. Go independently validates the
// exact current job, grant, collected evidence and scheduler acknowledgement.
export function managedCronCause(e:z.infer<typeof managedCronSchema>):boolean {
 const w=e.window,cycle=w.EveryMinutes*60000;
 if(w.EveryMinutes<2||60%w.EveryMinutes!==0||w.CurrentMinute===w.ProposedMinute||w.CurrentMinute>=w.EveryMinutes||w.ProposedMinute>=w.EveryMinutes||w.DeadlineSeconds>w.EveryMinutes*60||w.DeadlineSeconds<Math.max(w.CurrentMinute,w.ProposedMinute)*60+w.MaxRuntimeSeconds)return false;
 const schedule=(offset:number)=>w.EveryMinutes===60?`${offset} * * * *`:offset===0?`*/${w.EveryMinutes} * * * *`:`${offset}-59/${w.EveryMinutes} * * * *`;
 if(e.original_schedule!==schedule(w.CurrentMinute)||e.alternate_schedule!==schedule(w.ProposedMinute)||e.application_request_visibility!=='unobserved')return false;
 if(new Set(e.periods.map(p=>p.run.id)).size!==3)return false;
 const chronological=e.periods.flatMap(p=>[p.busy,p.control,p.quiet]).sort((a,b)=>time(a.start)-time(b.start));
 for(const [i,v] of chronological.entries()){
  const c=[v.target.before,v.read.before,v.write.before];
  if(c.some(x=>x.Database!==c[0].Database)||same(c[0],c[1])||same(c[0],c[2])||same(c[1],c[2]))return false;
  if(i>0){const prev=chronological[i-1];for(const [j,m] of [v.target,v.read,v.write].entries())if(!monotonic([prev.target,prev.read,prev.write][j].after,m.before))return false;}
 }
 return e.periods.every((p,i)=>{
  const start=time(p.cycle),due=start+w.CurrentMinute*60000,newDue=start+w.ProposedMinute*60000;
  const r=p.run;
  if(start%cycle!==0||(i>0&&start!==time(e.periods[i-1].cycle)+cycle)||r.job_id!==e.job_id||r.status!=='succeeded'||time(r.start)<due||time(r.end)<=time(r.start)||time(r.end)>due+20000||time(r.end)-time(r.start)>w.MaxRuntimeSeconds*1000||time(r.end)>start+w.DeadlineSeconds*1000)return false;
  for(const [n,v] of [p.busy,p.control,p.quiet].entries()){
   const expected=[due,due+30000,newDue][n];
   if(time(v.start)<expected||time(v.start)>expected+1000||time(v.end)<expected+20000||time(v.end)>expected+21000)return false;
   const base=[e.periods[0].busy,e.periods[0].control,e.periods[0].quiet][n];
   for(const [j,m] of [v.target,v.read,v.write].entries()){
    const ref=[base.target,base.read,base.write][j];const completed=calls(m);
    if(!monotonic(m.before,m.after)||!same(m.before,ref.before)||m.before.Boot!==e.generation.postmaster||!Number.isFinite(mean(m))||(completed<20&&!(n===2&&j===0&&completed===0))||completed<calls(ref)*.75||completed>calls(ref)*1.25)return false;
   }
  }
  if(mean(p.busy.target)-mean(p.control.target)<20||mean(p.busy.target)<mean(p.control.target)*1.3||p.blocks.length===0)return false;
  return p.blocks.every(b=>b.run_id===r.id&&b.job_id===e.job_id&&b.pid===r.pid&&b.waiting_pid!==b.pid&&b.waiting_role===p.busy.target.before.Role&&b.query_id===p.busy.target.before.Query&&time(b.backend_start)<=time(b.at)&&time(b.waiting_start)<=time(b.at)&&time(b.at)>=time(r.start)&&time(b.at)<=time(r.end)&&time(b.at)>=time(p.busy.start)&&time(b.at)<=time(p.busy.end));
 });
}
