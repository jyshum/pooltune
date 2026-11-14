import {useEffect,useState} from 'react';
import {api} from './types';
type Bucket={at:string;provider:string;demo:boolean;rate:number|null;peak:number|null;active:number;queued:number;limit:number;warm:number;readings:number};
type History={enabled:boolean;status:string;last_write:string;items:Bucket[]};
const fmt=(n:number|null)=>n===null?'Unavailable':n.toLocaleString(undefined,{maximumFractionDigits:1});
const names:Record<string,string>={azure:'Azure PostgreSQL',google:'Google Cloud SQL',supabase:'Supabase',local:'Local PgBouncer'};
export default function TelemetryHistory(){
 const [data,setData]=useState<History|null>(null),[error,setError]=useState(false),[clock,setClock]=useState(Date.now);
 useEffect(()=>{let alive=true;let timer:ReturnType<typeof setTimeout>;const refresh=()=>api<History>('telemetry').then(v=>{if(alive){setData(v);setError(false);setClock(Date.now())}}).catch(()=>{if(alive)setError(true)}).finally(()=>{if(alive)timer=setTimeout(refresh,10000)});void refresh();return()=>{alive=false;clearTimeout(timer)}},[]);
 const rows=data?.items??[],peak=Math.max(1,...rows.map(r=>r.peak??0)),start=clock-3600000;
 return <section aria-label="Traffic history" data-testid="traffic-history">
 <div className="section-heading"><div><span className="eyebrow">Tiger Data</span><h2>Traffic history</h2></div><span className="badge">Last hour</span></div>
 {error?<p role="status">History storage is unavailable. Live tuning continues independently.</p>:!data?<p role="status">Loading saved readings…</p>:!data.enabled?<p>History is optional. Configure a private Tiger Data connection file and stream name on your server to retain readings.</p>:<>
 <p className="muted">Saved readings across dashboard visits. One-minute averages and peaks. Gaps are not filled.</p>
 <div className="capacity-chart"><svg viewBox="0 0 800 170" role="img" aria-label="Saved transaction throughput over the last hour">
 {[0,.5,1].map(f=><g key={f}><line x1="48" x2="792" y1={150-f*130} y2={150-f*130} className="chart-grid"/><text x="0" y={154-f*130}>{fmt(peak*f)}</text></g>)}
 {rows.filter(r=>r.peak!==null).map(r=><rect key={`${r.at}-${r.provider}-${r.demo}`} x={Math.max(48,48+(Date.parse(r.at)-start)/3600000*732)} y={150-(r.peak??0)/peak*130} width="8" height={Math.max(2,(r.peak??0)/peak*130)} rx="1" fill="#3fb5c4"/>)}
 </svg></div>
 <div className="traffic-trend__axis"><span>60 minutes ago</span><span>Now</span></div>
 {rows.some(r=>r.demo)&&<p className="notice-line">Simulated traffic is labeled below. These saved demo readings are not live cloud performance results.</p>}
 {!rows.length?<p>Waiting for the first saved readings.</p>:<div className="table-wrap"><table><thead><tr><th>Time</th><th>Source</th><th>Avg / peak tx/s</th><th>Limit / warm</th><th>Peak queued</th></tr></thead><tbody>{[...rows].reverse().slice(0,60).map(r=><tr key={`${r.at}-${r.provider}-${r.demo}`}><td><time dateTime={r.at}>{new Date(r.at).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'})}</time></td><td>{names[r.provider]??r.provider}{r.demo&&<small className="record-id">Simulated traffic</small>}</td><td>{fmt(r.rate)} / {fmt(r.peak)}</td><td>{r.limit} / {r.warm}</td><td>{r.queued}</td></tr>)}</tbody></table></div>}
 <p className="muted">{data.status==='recording'?'Recording to Tiger Data':data.status==='connecting'?'Connecting to Tiger Data':data.status==='waiting for fresh readings'?'Waiting for fresh transaction readings':'History storage is unavailable'}. {Date.parse(data.last_write)>0?`Last saved ${new Date(data.last_write).toLocaleTimeString()}.`:''} Capacity columns show the maximum observed in each minute.</p>
 </>}
 </section>
}
