import {z} from 'zod';
const count=z.number().int().nonnegative();
const snapshot=z.strictObject({
 Target:z.strictObject({DatabaseOID:count.positive(),TableOID:count.positive(),Schema:z.string().min(1),Relation:z.string().min(1)}),
 At:z.iso.datetime(),Boot:z.iso.datetime(),StatsReset:z.iso.datetime(),LastAutovacuum:z.iso.datetime(),
 ScaleOption:z.string().max(32),ScaleFactor:z.number().nonnegative().max(100),EstimatedRows:z.number().nonnegative(),
 BaseThreshold:count,MaximumThreshold:z.number().int().min(-1),DeadTuples:count,Cleanups:count,RelationBytes:count,
 AutomaticCleanups:count,Updated:count,Deleted:count,AutovacuumNaptimeSeconds:count,
 AutovacuumEnabled:z.boolean(),OnlyDeadTupleTrigger:z.boolean(),
});
export const managedCleanupSchema=z.strictObject({earlier:snapshot,later:snapshot,next_scale_factor:z.number().nonnegative()});
export function managedCleanupCause(c:z.infer<typeof managedCleanupSchema>,table:number,now=Date.now()):boolean {
 const a=c.earlier,b=c.later,span=Date.parse(b.At)-Date.parse(a.At),age=now-Date.parse(b.At);
 if(b.Target.TableOID!==table || a.Target.DatabaseOID!==b.Target.DatabaseOID || a.Target.TableOID!==b.Target.TableOID || a.Target.Schema!==b.Target.Schema || a.Target.Relation!==b.Target.Relation ||
 span<1000 || span>30000 || age<0 || age>30000 || Date.parse(a.Boot)<=0 || Date.parse(a.Boot)!==Date.parse(b.Boot) || Date.parse(a.StatsReset)!==Date.parse(b.StatsReset) ||
 a.ScaleOption!==b.ScaleOption || a.ScaleFactor!==b.ScaleFactor || a.EstimatedRows!==b.EstimatedRows || a.BaseThreshold!==b.BaseThreshold || a.MaximumThreshold!==b.MaximumThreshold || a.Cleanups!==b.Cleanups ||
 a.AutomaticCleanups!==b.AutomaticCleanups || Date.parse(a.LastAutovacuum)!==Date.parse(b.LastAutovacuum) || !a.AutovacuumEnabled || !b.AutovacuumEnabled || !a.OnlyDeadTupleTrigger || !b.OnlyDeadTupleTrigger ||
 a.AutovacuumNaptimeSeconds!==b.AutovacuumNaptimeSeconds || b.AutovacuumNaptimeSeconds<=0 || b.AutovacuumNaptimeSeconds>120 || b.DeadTuples<=a.DeadTuples || b.Updated<a.Updated || b.Deleted<a.Deleted || (b.Updated===a.Updated&&b.Deleted===a.Deleted))return false;
 const next=Math.round(Math.max(.05,b.ScaleFactor-Math.min(.05,b.ScaleFactor/4))*1e6)/1e6;
 const threshold=(factor:number)=>Math.min(b.BaseThreshold+b.EstimatedRows*factor,b.MaximumThreshold>=0?b.MaximumThreshold:Infinity);
 const option=b.ScaleOption===''?0:Number(b.ScaleOption);
 return Number.isFinite(option)&&option>=0&&option<=100&&b.EstimatedRows>0&&next<b.ScaleFactor&&c.next_scale_factor===next&&threshold(next)<a.DeadTuples&&b.DeadTuples<threshold(b.ScaleFactor);
}
