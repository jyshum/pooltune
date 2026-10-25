import type {State} from './types';
import type {TuningState} from './Tuning';
import type {BurstState} from './Burst';

type AppState=State|TuningState|BurstState;

// The longer explanations, kept off the main dashboard. Opened with the "What this does" link (#about).
export default function About({state}:{state:AppState}){
 const mode='mode' in state?state.mode:'legacy';
 return <div className="about">
  <a className="back-link" href="#" data-animate>← Back to dashboard</a>
  <div className="intro" data-animate><div><span className="eyebrow">About this installation</span><h1>What this does</h1></div></div>
  {mode==='managed-burst'?<Burst/>:mode==='pool-tuning'?<Tuning gates={(state as TuningState).gates}/>:<Legacy gates={(state as State).gates}/>}
 </div>;
}

function Burst(){
 return <>
  <section data-animate><h2>The short version</h2><p>pooltune sits between your app and your database. It limits how many transactions run at once, inside the connection budget you approved, and keeps some connections warm so bursts start fast.</p><p>It does not make individual queries faster.</p></section>
  <section data-animate><h2>Start, restart and stop</h2>
   <p>You start the gateway process from a terminal on its server. When durable control history is available, this dashboard can freeze admission, resume automation and restore a confirmed historical admission limit.</p>
   <div className="command"><span>Run in a terminal on your server</span><pre><code>bin/pooltune-burst -config /absolute/path/to/profile.json -resume</code></pre></div>
   <ul><li><code>-config</code> is the full path to your private profile file.</li><li><code>-resume</code> lets traffic in once the warm connections are verified. Leave it off to start in a safety hold and review first.</li><li>To stop, press Ctrl+C in that terminal. New work stops and active transactions finish.</li></ul>
  </section>
  <section data-animate><h2>Restore a change</h2><p>Open Change history, choose a confirmed limit, and review the restore before confirming. It creates a new revision and holds admission at that limit until Resume automation. Safety checks remain active and may lower or stop admission. Active transactions finish; this never undoes database writes or changes hosted pooler settings.</p><p>Stale views, uncertain work and unsafe readings prevent restore. Storage uncertainty stops admission and requires operator review. Clean restarts retain history but start frozen; they never replay a previous restore.</p></section>
  <section data-animate><h2>Limits</h2><ul><li>Runs on a single host. There is no automatic failover or backup route.</li><li>It never replays SQL. If a transaction outcome is unknown, check it by hand.</li><li>Provider CPU and memory readings lag, so they are not instant overload protection.</li><li>AI and priority scheduling are not part of this release.</li></ul></section>
 </>;
}

function Tuning({gates}:{gates:string[]}){
 return <>
  <section data-animate><h2>The short version</h2><p>pooltune runs beside your existing pooler. It raises the pooler's connection limit when requests pile up, lowers it when things go quiet, and backs off when the database is under pressure. Your app keeps connecting to the pooler as usual.</p></section>
  <section data-animate><h2>Controls</h2><ul><li>Freeze pauses automatic changes. Pressure backoff stays on.</li><li>Lowering the limit lets open transactions finish first.</li><li>Restoring a past limit creates a new change and pauses automation.</li></ul></section>
  <section data-animate><h2>Cloud providers</h2><p>This dashboard controls a local PgBouncer. Azure, Google Cloud and Supabase each need their own verified readings, permissions and change checks before pooltune can manage them. See the Support tab for details.</p></section>
  {gates.length>0&&<section data-animate><h2>Safety and routing limits</h2><ul>{gates.map(g=><li key={g}>{g}</li>)}</ul></section>}
 </>;
}

function Legacy({gates}:{gates:string[]}){
 return <>
  <section data-animate><h2>The short version</h2><p>pooltune controls how many requests reach your database at once and protects important work inside a fixed connection budget. AI is never in the request path.</p></section>
  {gates.length>0&&<section data-animate><h2>Before enabling more automation</h2><ul>{gates.map(g=><li key={g}>{g}</li>)}</ul></section>}
 </>;
}
