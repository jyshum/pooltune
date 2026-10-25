import {useEffect,useState} from 'react';
import {api,type Mutate} from './types';
import Wordmark from './Brand';
import TrafficTrend from './TrafficTrend';
import type {TransactionCounter} from './traffic';
import {Stat,Status} from './Status';

export type PoolSample={transactions?:TransactionCounter|null;at:string;target:number;working:number;idle:number;waiting:number;cpu:number;memory:number;pending:boolean};
type Revision={id:string;actor:string;status:string;at:string;reason:string;before:number;after:number};
export type TuningState={mode:'pool-tuning';target:string;revision:string;frozen:boolean;ready:boolean;status:string;bounds:{minimum:number;maximum:number;cpu_grow:number;cpu_backoff:number;memory_grow:number;memory_backoff:number};observation:PoolSample|null;pending:Revision|null;samples:PoolSample[];gates:string[]};

function CapacityChart({samples,maximum}:{samples:PoolSample[];maximum:number}){
 const width=800,height=200,x=(i:number)=>32+i*(width-48)/Math.max(1,samples.length-1),y=(v:number)=>height-22-v/Math.max(1,maximum)*(height-40);
 const points=(field:'target'|'working')=>samples.map((s,i)=>`${x(i)},${y(s[field])}`).join(' ');
 const area=samples.length<2?'':`${x(0)},${y(0)} ${points('working')} ${x(samples.length-1)},${y(0)}`;
 return <div className="capacity-chart"><div className="chart-legend"><span><i className="ceiling-dot"/>Limit</span><span><i className="working-dot"/>Open connections</span><small>Last {samples.length} readings</small></div>
 {samples.length<2?<p className="muted">Collecting readings…</p>:<svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Pool limit and open connections over recent readings">
 <defs><linearGradient id="working-fill" x1="0" x2="0" y1="0" y2="1"><stop offset="0" stopColor="#3fb5c4" stopOpacity=".28"/><stop offset="1" stopColor="#3fb5c4" stopOpacity="0"/></linearGradient></defs>
 {[0,.5,1].map(f=><g key={f}><line x1="32" x2={width-16} y1={y(maximum*f)} y2={y(maximum*f)} className="chart-grid"/><text x="4" y={y(maximum*f)+4}>{Math.round(maximum*f)}</text></g>)}
 <polygon points={area} className="working-area"/><polyline points={points('working')} className="working-line"/><polyline points={points('target')} className="ceiling-line"/>
 </svg>}<small className="muted">Counts only. Not a measure of speed or cost.</small></div>
}

export default function Tuning({state,mutate,busy}:{state:TuningState;mutate:Mutate;busy:boolean}){
 const [tab,setTab]=useState('overview'),[history,setHistory]=useState<Revision[]>([]),[historyError,setHistoryError]=useState('');
 useEffect(()=>{if(tab!=='history')return;let active=true;api<{items:Revision[]}>('revisions').then(r=>{if(active){setHistory(r.items);setHistoryError('')}}).catch(()=>{if(active)setHistoryError('History unavailable. Nothing has been inferred.')});return()=>{active=false}},[tab,state.revision]);
 const [clock,setClock]=useState(Date.now);
 useEffect(()=>{const timer=setInterval(()=>setClock(Date.now()),250);return()=>clearInterval(timer)},[]);
 const m=state.observation;
 const fresh=!!m&&clock-Date.parse(m.at)<=3000&&Date.parse(m.at)<=clock;
 const measured=state.ready&&fresh;
 const call=(action:string,fields?:Record<string,unknown>)=>void mutate(action,fields).catch(()=>{});
 return <>
 <div className="intro" data-animate><div><span className="eyebrow">Pool tuning</span><h1>Pool capacity</h1><p>{state.target} <span className="muted">· Local PgBouncer</span></p></div><div className="intro__side"><a className="link-button" href="#about">What this does</a><Status tone={state.pending?'alert':!measured?'hold':state.frozen?'off':'live'} label={state.pending?'Reconciling':!measured?'Observation required':state.frozen?'Automation frozen':'Automation running'}/></div></div>
 <div className="route-strip" aria-label="Application route" data-animate><span>App</span><b/><span className="route-strip__pool">Pooler</span><b/><span>PostgreSQL</span><small><Wordmark/> adjusts the pooler in the background.</small></div>
 <nav aria-label="Tuning sections" data-animate>{['overview','history','support'].map(x=><button key={x} aria-current={tab===x?'page':undefined} className={tab===x?'selected':'quiet'} onClick={()=>setTab(x)}>{x[0].toUpperCase()+x.slice(1)}</button>)}</nav>
 {tab==='overview'?<>
 <TrafficTrend sample={m?.transactions} active={fresh&&m?m.working-m.idle:null} queued={fresh?m?.waiting:null} scope="Local PgBouncer · Pooled transactions, including rollbacks"/>
 <div className="cards"><Stat label="Connection ceiling" value={fresh?m?.target:'Unavailable'} note="Current pool limit"/><Stat label="Open connections" value={fresh?m?.working:'Unavailable'} note={fresh?`${m?.idle} idle`:'Waiting for a fresh reading'}/><Stat label="Waiting clients" value={fresh?m?.waiting:'Unavailable'} note="Queued in PgBouncer"/><Stat label="Your range" value={<>{state.bounds.minimum}<em> to </em>{state.bounds.maximum}</>} note="Minimum to maximum"/></div>
 <section data-animate><div className="section-heading"><div><span className="eyebrow">Live readings</span><h2>Limit and connections</h2></div><span className="badge">{fresh?'Live':'Stale'}</span></div><CapacityChart samples={state.samples||[]} maximum={state.bounds.maximum}/></section>
 <div className="tuning-columns"><section data-animate><span className="eyebrow">Database host</span><h2>CPU and memory</h2><div className="pressure-row"><span>CPU</span><strong>{fresh?`${(m!.cpu*100).toFixed(1)}%`:'Unavailable'}</strong></div><meter aria-label="CPU utilization" min={0} max={1} value={fresh?m!.cpu:0}/><div className="pressure-row"><span>Memory</span><strong>{fresh?`${(m!.memory*100).toFixed(1)}%`:'Unavailable'}</strong></div><meter aria-label="Memory pressure" min={0} max={1} value={fresh?m!.memory:0}/><p className="muted">Includes other processes on this machine.</p></section>
 <section data-animate><span className="eyebrow">Controls</span><h2>{state.frozen?'Automation is paused':'Automation is on'}</h2><p>Freeze pauses changes. Pressure backoff stays on. Lower limits let open transactions finish.</p><button disabled={busy||(state.frozen&&!measured)} onClick={()=>call(state.frozen?'resume':'freeze')}>{state.frozen?'Resume automation':'Freeze automation'}</button><p className="controller-reason">Controller: {state.status.replaceAll('_',' ')}</p>{state.pending&&<p role="status">Change {state.pending.before} → {state.pending.after} is waiting for confirmation. It will not be replayed.</p>}</section></div>
 <p className="notice-line" data-animate><strong>Cloud automation unverified</strong> This controls a local pooler only. <a href="#about">See details</a></p>
 </>:tab==='history'?<section data-animate><span className="eyebrow">History</span><h2>Every change, with its result</h2><p>Each change records what was planned and what was confirmed. Restoring creates a new change and pauses automation.</p>{historyError&&<p role="alert">{historyError}</p>}<div className="table-wrap"><table><thead><tr><th>When</th><th>Limit</th><th>Result</th><th>Reason</th><th></th></tr></thead><tbody>{[...history].reverse().map((r,i)=><tr key={`${r.id}-${r.status}-${i}`}><td><time dateTime={r.at}>{new Date(r.at).toLocaleTimeString()}</time><small className="record-id">{r.id.slice(0,17)}</small></td><td>{r.after>0?`${r.before} → ${r.after}`:'Owner state'}</td><td><span className={`result result--${r.status}`}>{r.status}</span></td><td>{r.reason}</td><td>{r.status==='confirmed'&&r.after>0&&<button className="quiet" disabled={busy||!measured||!!state.pending||r.after===m?.target} onClick={()=>call('revert',{revision:r.id})}>Restore {r.after}</button>}</td></tr>)}</tbody></table></div></section>:<>
 <section data-animate><span className="eyebrow">Support</span><h2>Verified here. Still to verify there.</h2><div className="table-wrap"><table><thead><tr><th>Target</th><th>Evidence so far</th><th>Still needed</th></tr></thead><tbody>
 <tr><td>Local PgBouncer</td><td>Local readings, real limit changes and process-kill tests</td><td>Final release review</td></tr>
 <tr><td>Azure PostgreSQL</td><td>Live config reader and SQL access</td><td>Demand and pressure readings, scoped changes, recovery</td></tr>
 <tr><td>Google Cloud SQL</td><td>Live config discovery. Test start blocked by regional capacity</td><td>Explicit settings, per-pooler budget, SQL access, live test</td></tr>
 <tr><td>Supabase</td><td>Live config reader and SQL access</td><td>Demand and pressure readings. Changes disabled. REST pool is separate from Supavisor</td></tr>
 </tbody></table></div></section><aside data-animate><h2>Safety and routing limits</h2><ul>{state.gates.map(g=><li key={g}>{g}</li>)}</ul><p>Poolers already close idle connections on their own. pooltune still has to show it adds value by adjusting their limits. The last comparison did not show that yet.</p></aside>
 </>}
 <footer>Private dashboard · AI and priority scheduling deferred · Revision <code>{state.revision}</code></footer>
 </>
}
