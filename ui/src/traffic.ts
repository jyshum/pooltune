export type TransactionCounter={at:string;total:string;epoch:string;demo?:boolean};
export type TrafficPoint={at:number;rate:number|null;spike:boolean};
export type TrafficHistory={anchor:TransactionCounter|null;points:TrafficPoint[]};
export const emptyTraffic=():TrafficHistory=>({anchor:null,points:[]});
export function trafficFresh(s:TransactionCounter|null|undefined,now:number):boolean {
 if(!s||!s.epoch||!/^\d{1,20}$/.test(s.total)||BigInt(s.total)>18446744073709551615n)return false;
 const age=now-Date.parse(s.at);
 return Number.isFinite(age)&&age>=-1000&&age<=3000;
}
export function advanceTraffic(old:TrafficHistory,s:TransactionCounter|null|undefined,now:number):TrafficHistory {
 const points=old.points.filter(p=>p.at>=now-60000).slice(-120);
 if(!trafficFresh(s,now)) return {anchor:null,points};
 const next=s!,at=Date.parse(next.at),prior=old.anchor;
 if(prior?.epoch===next.epoch&&prior.at===next.at&&prior.total===next.total)return old;
 if(!prior)return {anchor:next,points:points.length?[...points,{at,rate:null,spike:false}]:points};
 if(prior.epoch!==next.epoch||at<=Date.parse(prior.at))return {anchor:next,points:[]};
 const dt=(at-Date.parse(prior.at))/1000,delta=BigInt(next.total)-BigInt(prior.total);
 if(dt>3||delta<0n) return {anchor:next,points:[...points,{at,rate:null,spike:false}]};
 const rate=Number(delta)/dt;
 const baseline=points.filter(p=>p.rate!==null).slice(-10);
 const mean=baseline.reduce((n,p)=>n+p.rate!,0)/Math.max(1,baseline.length);
 const spike=baseline.length>=5&&rate>0&&rate>mean*1.5;
 return {anchor:next,points:[...points,{at,rate,spike}]};
}
