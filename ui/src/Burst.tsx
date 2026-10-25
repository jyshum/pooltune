import {useEffect,useState} from 'react';
import ManagedHistory from './ManagedHistory';
import type {Mutate} from './types';
import Wordmark from './Brand';
import TrafficTrend from './TrafficTrend';
import type {TransactionCounter} from './traffic';
import {Stat,Status} from './Status';
export type BurstState={controls_available?:boolean;can_resume?:boolean;can_restore?:boolean;manual_limit?:number;audit_healthy?:boolean;transactions?:TransactionCounter|null;mode:'managed-burst';revision:string;target:string;provider:string;observed_at:string;health_at:string;pressure_at:string;pressure_age_seconds:number;pressure_max_age_seconds:number;cpu:number;memory:number;base:number;ceiling:number;warm:number;effective:number;active:number;queued:number;uncertain:number;ready:boolean;frozen:boolean;health_verified:boolean;reason:string};
const providers:Record<string,string>={azure:'Azure PostgreSQL',google:'Google Cloud SQL',supabase:'Supabase',local:'Local fixture'};
export default function Burst({state:s,mutate,busy}:{state:BurstState;mutate:Mutate;busy:boolean}){
 const [tab,setTab]=useState('overview');
 const [controlReview,setControlReview]=useState<{action:'freeze'|'resume';expected:string}|null>(null);
 async function confirmControl(){
  if(!controlReview||busy||controlReview.expected!==s.revision)return;
  try{await mutate(controlReview.action,{expected_revision:controlReview.expected});setControlReview(null)}catch{/* App shows refusal; never retry automatically. */}
 }
 const [clock,setClock]=useState(Date.now);
 useEffect(()=>{const id=setInterval(()=>setClock(Date.now()),250);return()=>clearInterval(id)},[]);
 const elapsed=clock-Date.parse(s.observed_at),fresh=elapsed>=-1000&&elapsed<=3000;
 const age=s.pressure_age_seconds+Math.max(0,elapsed)/1000;
 const health=fresh&&s.health_verified&&clock-Date.parse(s.health_at)<=2000&&age<=s.pressure_max_age_seconds;
 const ready=health&&s.ready;
 const count=(n:number)=>fresh?n:'Unavailable';
 return <>
 <div className="intro" data-animate><div><span className="eyebrow">Managed gateway</span><h1>{s.target}</h1><p>{providers[s.provider]??s.provider} <span className="muted">· Private installation</span></p></div><div className="intro__side"><a className="link-button" href="#about">What this does</a><Status tone={!fresh?'alert':s.frozen?'off':ready?'live':'hold'} label={!fresh?'Connection lost':s.frozen?'Admission frozen':ready?'Accepting traffic':'Safety hold'}/></div></div>
 <div className="route-strip" aria-label="Application route" data-animate><span>App</span><b/><span className="route-strip__pool"><Wordmark/></span><b/><span>PgBouncer</span><b/><span>{s.provider==='supabase'?'Supabase session pool':'Managed PostgreSQL'}</span></div>
 <nav aria-label="Managed gateway sections" data-animate>{['overview','history'].map(x=><button key={x} aria-current={tab===x?'page':undefined} className={tab===x?'selected':'quiet'} onClick={()=>setTab(x)}>{x==='history'?'Change history':'Overview'}</button>)}</nav>
 {tab==='history'?<ManagedHistory revision={s.revision} effective={s.effective} minimum={s.base} maximum={s.ceiling} canRestore={fresh&&!!s.can_restore} mutate={mutate} busy={busy}/>:<>
 <TrafficTrend sample={s.transactions} active={fresh?s.active:null} queued={fresh?s.queued:null} scope={`${providers[s.provider]??s.provider} via Pooltune · Pooled transactions, including rollbacks`}/>
 <div className="cards"><Stat label="Admission limit" value={count(s.effective)} note="Transactions allowed now"/><Stat label="Established connections" value={health?s.warm:'Unverified'} note="Includes warm reserve"/><Stat label="Active / queued" value={<>{count(s.active)}<em> / </em>{count(s.queued)}</>} note="Inside pooltune"/><Stat label="Your range" value={<>{s.base}<em> to </em>{s.ceiling}</>} note="Ceiling stays warm"/></div>
 <div className="tuning-columns"><section data-animate><span className="eyebrow">Provider metrics</span><h2>CPU and memory</h2><div className="pressure-row"><span>CPU</span><strong>{health?`${(s.cpu*100).toFixed(1)}%`:'Unavailable'}</strong></div><meter aria-label="Provider CPU" min={0} max={1} value={health?s.cpu:0}/><div className="pressure-row"><span>Memory</span><strong>{health?`${(s.memory*100).toFixed(1)}%`:'Unavailable'}</strong></div><meter aria-label="Provider memory" min={0} max={1} value={health?s.memory:0}/><p className="muted">Reading age: {s.pressure_age_seconds<0?'unavailable':`${Math.max(0,age).toFixed(0)}s`} of {s.pressure_max_age_seconds}s allowed.</p></section>
 <section data-animate><span className="eyebrow">Controller</span><h2>{s.reason.replaceAll('_',' ')}</h2><p>Unknown transaction outcomes: <strong>{count(s.uncertain)}</strong></p><p>{health?'Live readings and provider metrics are verified.':'Waiting for fresh readings before admitting new work.'}</p>{s.controls_available?<>
 <p>{s.manual_limit?`Automatic adjustments are paused. Reviewed limit: ${s.manual_limit}. Safety backoff remains on.`:'Automation adjusts admission within your approved range.'}</p>
 <button className="quiet" disabled={busy||!fresh||s.frozen} onClick={()=>setControlReview({action:'freeze',expected:s.revision})}>Freeze admission</button>{' '}
 <button disabled={busy||!fresh||!s.can_resume||(!s.frozen&&!s.manual_limit)} onClick={()=>setControlReview({action:'resume',expected:s.revision})}>Resume automation</button>
 {controlReview&&<aside aria-label="Review admission control">
  <h3>{controlReview.action==='freeze'?'Freeze admission?':'Resume automatic admission?'}</h3>
  <p>{controlReview.action==='freeze'?'The admission limit becomes zero. Active transactions finish; queued work remains subject to its deadline.':`Admission returns to the approved base of ${s.base}, then may adjust up to ${s.ceiling}. This clears a restored hold, subject to current safety checks.`}</p>
  {controlReview.expected!==s.revision&&<p role="status">The configuration changed. Cancel and review the latest state.</p>}
  <button disabled={busy||!fresh||controlReview.expected!==s.revision||(controlReview.action==='resume'&&!s.can_resume)} onClick={()=>void confirmControl()}>{controlReview.action==='freeze'?'Confirm freeze':'Confirm resume'}</button>{' '}
  <button className="quiet" disabled={busy} onClick={()=>setControlReview(null)}>Cancel</button>
 </aside>}
 <p className="muted">Freeze stops new transactions; active work drains. <a href="#about">How controls work</a></p>
 </>:<p className="muted">{s.audit_healthy===false?'History storage needs operator review. Admission is stopped.':'Read-only. Durable control history is unavailable.'} <a href="#about">How</a></p>}</section></div>
 </>}
 <footer>{providers[s.provider]??s.provider} · Revision <code>{s.revision}</code> · {fresh?'Updating every second':'Last update is stale'}</footer>
 </>
}
