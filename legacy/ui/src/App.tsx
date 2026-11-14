import {useCallback,useEffect,useRef,useState,type FormEvent} from 'react';
import {api,APIError,type State,type CommandResult} from './types';
import Setup from './Setup';
import History from './History';
import Controls from './Controls';
import Tuning, {type TuningState} from './Tuning';
import Burst, {type BurstState} from './Burst';
import Wordmark from './Brand';
import PixelWave from './PixelWave';
import {Stat,Status} from './Status';
import {useEntrance} from './motion';
import About from './About';
type AppState=State|TuningState|BurstState;
const isBurst=(s:AppState):s is BurstState=>'mode' in s&&s.mode==='managed-burst';
const isTuning=(s:AppState):s is TuningState=>'mode' in s&&s.mode==='pool-tuning';
export default function App(){
 const [claimed,setClaimed]=useState<boolean|null>(null),[csrf,setCSRF]=useState(''),[password,setPassword]=useState(''),[claim,setClaim]=useState(''),[state,setState]=useState<AppState|null>(null),[error,setError]=useState(''),[notice,setNotice]=useState(''),[busy,setBusy]=useState(false),[tab,setTab]=useState('overview');
 const stateRequest=useRef(0),view=useRef<HTMLDivElement>(null);
 const [about,setAbout]=useState(()=>location.hash==='#about');
 useEffect(()=>{const sync=()=>{setAbout(location.hash==='#about');window.scrollTo(0,0)};addEventListener('hashchange',sync);return()=>removeEventListener('hashchange',sync)},[]);
 const refresh=useCallback(async()=>{const request=++stateRequest.current;const value=await api<AppState>('state');if(request===stateRequest.current)setState(value);},[]);
 useEffect(()=>{let alive=true;api<{claimed:boolean}>('setup/status').then(v=>{if(alive)setClaimed(v.claimed)}).catch(()=>{if(alive)setError('Private control service is unavailable.')});api<{csrf:string}>('session').then(v=>{if(alive)setCSRF(v.csrf)}).catch(()=>{});return()=>{alive=false}},[]);
 useEffect(()=>{if(!csrf)return;let alive=true;let timer:ReturnType<typeof setTimeout>;const poll=()=>refresh().catch(e=>{if(alive){setError(e.message);if(e instanceof APIError&&e.status===401){setCSRF('');setState(null)}}}).finally(()=>{if(alive)timer=setTimeout(poll,1000)});void poll();return()=>{alive=false;clearTimeout(timer);stateRequest.current++}},[csrf,refresh]);
 const viewKey=!csrf?'login':!state?'loading':about?'about':isBurst(state)?'burst':isTuning(state)?'tuning':`legacy-${tab}`;
 useEntrance(view,viewKey);
 async function signIn(e:FormEvent){e.preventDefault();setBusy(true);setError('');try{const s=await api<{csrf:string}>(claimed?'login':'setup/claim',{password,...(!claimed?{claim_token:claim}:{})});setCSRF(s.csrf);setClaimed(true);setPassword('');setClaim('')}catch(e){setError((e as Error).message)}finally{setBusy(false)}}
 async function mutate(action:string,fields:Record<string,unknown>={}){if(!state||busy)return;setBusy(true);setError('');setNotice('');try{const result=await api<CommandResult>(action,{...fields,expected_revision:state.revision,idempotency_key:crypto.randomUUID()},csrf);setNotice(result.status==='confirmed'?'Change confirmed.':`Change ${result.status}. Waiting for confirmed runtime state.`);await refresh()}catch(e){setError((e as Error).message);throw e}finally{setBusy(false)}}
 async function logout(){setBusy(true);try{await api('logout',{},csrf);setCSRF('');setState(null);setPassword('')}catch(e){if(e instanceof APIError&&e.status===401){setCSRF('');setState(null)}else setError((e as Error).message)}finally{setBusy(false)}}
 return <div className="shell"><header><a className="brand" href="/" aria-label="pooltune dashboard home"><Wordmark/><span className="brand__tag">Dashboard</span></a><div className="header-right"><span className="badge">Your environment</span>{csrf&&<button className="quiet" onClick={logout} disabled={busy}>Sign out</button>}</div></header>
 <main ref={view}><div role="alert" className={error?'alert':'empty'}>{error}</div><div role="status" className={notice?'notice':'empty'}>{notice}</div>
 {!csrf?<div className="login-stage"><PixelWave/><section className="login" data-animate><span className="eyebrow">Private dashboard</span><h1>{claimed===false?'Claim this installation':'Welcome back'}</h1><p>Sign in to control this installation. Database credentials stay on your server.</p><form onSubmit={signIn}>{claimed===false&&<label>One-time setup token<input autoComplete="off" type="password" value={claim} onChange={e=>setClaim(e.target.value)} required/><small>From the private claim file on your server. Never put it in a URL.</small></label>}<label>Administrator password<input type="password" minLength={12} maxLength={72} autoComplete={claimed?'current-password':'new-password'} value={password} onChange={e=>setPassword(e.target.value)} required/><small>12 to 72 characters. Stored only by your local control service.</small></label><button disabled={busy||claimed===null}>{busy?'Checking…':claimed?'Sign in':'Claim installation'}</button></form></section></div>:!state?<p role="status" className="loading">Loading…</p>:about?<About state={state}/>:isBurst(state)?<Burst state={state}/>:isTuning(state)?<Tuning state={state} mutate={mutate} busy={busy}/>:<>
 <div className="intro" data-animate><div><span className="eyebrow">Traffic control</span><h1>{state.target||'Set up your first target'}</h1><p>Protect important requests inside a fixed connection budget.</p></div><div className="intro__side"><a className="link-button" href="#about">What this does</a><Status tone={state.frozen?'off':state.ready?'live':'hold'} label={state.frozen?'Frozen':state.ready?'Traffic ready':'Setup or recovery required'}/></div></div>
 <nav aria-label="Control sections" data-animate>{['overview','setup','history'].map(x=><button key={x} aria-current={tab===x?'page':undefined} className={tab===x?'selected':'quiet'} onClick={()=>setTab(x)}>{x[0].toUpperCase()+x.slice(1)}</button>)}</nav>
 {tab==='setup'||(!state.setup.approved&&tab==='overview')?<Setup state={state} mutate={mutate} busy={busy}/>:tab==='history'?<History revision={state.revision}/>:<>
 <div className="cards"><Stat label="Admission limit" value={state.limits.effective} note={state.path==='postgres'?'PostgreSQL transactions':'REST requests in flight'}/><Stat label="Queued" value={state.metrics.queued} note="Waiting inside pooltune"/><Stat label="p95 latency" value={state.metrics.p95_ms?.toFixed(1)??'Unavailable'} unit=" ms" note="Upper bound. Read with errors"/><Stat label="Error rate" value={state.metrics.error_rate===null?'Unavailable':(state.metrics.error_rate*100).toFixed(2)} unit="%" note="Current window"/></div>
 <section data-animate><span className="eyebrow">Policy</span><h2>{state.experiment?.phase||'Stable policy'}</h2><p>{state.experiment?.reason||'Fixed rules protect traffic. AI is never in the request path.'}</p><div className="allocation"><span>Stable: {state.limits.stable} slots</span><span>Candidate: {state.limits.candidate} slots</span></div><p className="muted">Physical connections: {state.limits.physical??'unverified'}. REST requests and database connections are different units.</p></section>
 <Controls state={state} mutate={mutate} busy={busy}/></>}
 {state.gates.length>0&&<p className="notice-line" data-animate><strong>{state.gates.length} checks before more automation</strong> <a href="#about">See details</a></p>}
 <footer>Revision <code>{state.revision}</code> · {state.provider||'No provider'} · {state.path||'No traffic path'}</footer>
 </>}
 </main></div>
}
