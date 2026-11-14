import {useEffect,useRef,useState} from 'react';
import TelemetryHistory from './TelemetryHistory';
import {advanceTraffic,emptyTraffic,trafficFresh,type TransactionCounter} from './traffic';

// One pixel column per observed interval. No invented wave or interpolation.
export default function TrafficTrend({sample,active,queued,scope}:{sample?:TransactionCounter|null;active?:number|null;queued?:number|null;scope:string}) {
 const [showHistory,setShowHistory]=useState(false);
 const plot=useRef<HTMLDivElement>(null);
 const [plotWidth,setPlotWidth]=useState(960);
 useEffect(()=>{if(!plot.current)return;const observer=new ResizeObserver(([entry])=>setPlotWidth(entry.contentRect.width));observer.observe(plot.current);return()=>observer.disconnect()},[]);
 const [history,setHistory]=useState(emptyTraffic),[clock,setClock]=useState(Date.now);
 useEffect(()=>{const id=setInterval(()=>setClock(Date.now()),1000);return()=>clearInterval(id)},[]);
 useEffect(()=>{setHistory(old=>advanceTraffic(old,sample,Date.now()))},[sample?.at,sample?.total,sample?.epoch]);
 const fresh=trafficFresh(sample,clock),latest=history.points.at(-1);
 const current=fresh&&latest?.at===Date.parse(sample!.at)?latest:null;
 const rate=current?.rate??null;
 const points=history.points.filter(p=>p.at>=clock-60000);
 const peak=Math.max(0,...points.map(p=>p.rate??0));
 const max=Math.max(1,peak),width=Math.max(240,plotWidth),height=160;
 // On small screens keep each square legible by showing the measured peak
 // within each ten-pixel time bucket. Never interpolate missing intervals.
 const columns=new Map<number,(typeof points)[number]>();
 for(const point of points){if(point.rate===null)continue;const x=Math.floor((point.at-(clock-60000))/60000*(width-10)/10)*10;const prior=columns.get(x);if(!prior||point.rate>(prior.rate??0))columns.set(x,point)}
 const label=!fresh?'Data unavailable':rate===null?'Collecting readings':current?.spike?'Traffic spike':rate===0?'No transactions':'Normal traffic';
 const fmt=(n:number)=>n.toLocaleString(undefined,{maximumFractionDigits:1});
 return <><section className={`traffic-trend${fresh?'':' traffic-trend--stale'}`} aria-label="Transaction throughput" data-animate>
  <div className="traffic-trend__head"><div><span className="eyebrow">Transaction throughput</span><div className="traffic-trend__number"><strong data-testid="transaction-rate">{rate===null?'Unavailable':fmt(rate)}</strong><span>transactions/sec</span></div></div><div className="traffic-trend__summary"><span className={`badge${current?.spike?' traffic-trend__spike':''}`} data-testid="traffic-status">{label}</span>{sample?.demo&&<span className="badge">Simulated traffic</span>}<div><span>60s peak <strong>{points.some(p=>p.rate!==null)?fmt(peak):'Unavailable'}</strong></span><span>Active <strong>{active??'Unavailable'}</strong></span><span>Queued <strong>{queued??'Unavailable'}</strong></span></div></div></div>
  <div className="traffic-trend__plot" ref={plot}><svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Last 60 seconds of measured transactions per second, using peak readings per pixel column. Grey is normal traffic; cyan marks spikes.">
   {[0,1,2].map(i=><line key={i} x1="0" x2={width} y1={8+i*70} y2={8+i*70} className="chart-grid"/>)}
   {[...columns].map(([x,p])=>{if(p.rate===null)return null;const rows=p.rate===0?1:Math.max(1,Math.ceil(p.rate/max*15));return <g key={x} className={p.spike?'traffic-pixel traffic-pixel--spike':'traffic-pixel'}>{Array.from({length:rows},(_,r)=><rect key={r} x={x} y={height-8-r*10} width="6" height="6" rx="1" opacity={p.rate===0?.35:.4+.6*(r+1)/rows}/>)}</g>})}
  </svg>{!points.some(p=>p.rate!==null)&&<span className="traffic-trend__empty">{fresh?'Waiting for two counter readings':'Waiting for transaction data'}</span>}</div>
  <div className="traffic-trend__axis"><span>60 seconds ago</span><span>Now</span></div>
  <div className="traffic-trend__foot"><span>{scope}</span><button className="quiet" aria-expanded={showHistory} onClick={()=>setShowHistory(v=>!v)}>{showHistory?'Hide history':'View history'}</button><span title="Cyan indicates more than 1.5 times the mean of the previous 10 readings, after at least 5 readings."><i/> Normal <i className="cyan"/> Spike</span></div>
 </section>{showHistory&&<TelemetryHistory/>}</>
}
