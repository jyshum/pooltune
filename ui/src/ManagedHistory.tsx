import {useEffect,useState} from 'react';
import {api,type Mutate} from './types';

type Revision={id:string;at:string;actor:string;status:string;reason:string;before:number;after:number;restored_from?:string};
type History={items:Revision[];total:number;limit:number};
export default function ManagedHistory({revision,effective,minimum,maximum,canRestore,busy,mutate}:{revision:string;effective:number;minimum:number;maximum:number;canRestore:boolean;busy:boolean;mutate:Mutate}){
 const [data,setData]=useState<History|null>(null),[error,setError]=useState('');
 const [review,setReview]=useState<{record:Revision;expected:string}|null>(null);
 useEffect(()=>{let active=true;api<History>('revisions').then(value=>{if(active){setData(value);setError('')}}).catch(()=>{if(active)setError('Change history is unavailable. Nothing has been inferred.')});return()=>{active=false}},[revision]);
 const staleReview=!!review&&review.expected!==revision;
 async function restore(){
  if(!review||staleReview||!canRestore||busy)return;
  try{await mutate('revert',{revision:review.record.id,expected_revision:review.expected});setReview(null)}catch{/* App displays the server response. No automatic retry. */}
 }
 return <section aria-label="Managed change history" data-animate>
  <span className="eyebrow">Change history</span><h2>Restore a reviewed admission limit</h2>
  <p>Restoring creates a new revision and holds that limit until you resume automation. Active transactions finish. Safety checks can still reduce or stop admission.</p>
  <p className="muted">This does not undo SQL, resize the hosted pooler or remove warm connections. Tiger Data traffic history is separate.</p>
  {error&&<p role="alert">{error}</p>}
  {review&&<aside aria-label="Review restore">
   <h3>Restore admission to {review.record.after}?</h3>
   <p>Current limit: {effective}. Approved range: {minimum} to {maximum}. Automatic adjustments will pause at the selected limit.</p>
   <p>Selected revision: <code>{review.record.id}</code></p>
   {staleReview&&<p role="status">The configuration changed. Cancel and review the latest state before restoring.</p>}
   {!canRestore&&<p role="status">Restore needs fresh, healthy readings and running admission without uncertain work.</p>}
   <button disabled={busy||!canRestore||staleReview} onClick={()=>void restore()}>Confirm restore</button>{' '}
   <button className="quiet" disabled={busy} onClick={()=>setReview(null)}>Cancel</button>
  </aside>}
  {data&&<>
   <p className="muted">Showing the latest {data.items.length} of {data.total} recorded changes.</p>
   {data.total>=data.limit*.9&&<p role="status">The local journal is nearing its {data.limit}-record limit. At that limit admission stops for operator maintenance; old history is never silently deleted.</p>}
   <div className="table-wrap"><table><thead><tr><th>When</th><th>Admission</th><th>Result</th><th>Reason</th><th>Action</th></tr></thead><tbody>
    {[...data.items].reverse().map(r=><tr key={r.id}>
     <td><time dateTime={r.at}>{new Date(r.at).toLocaleString()}</time><small className="record-id">{r.id.slice(0,17)}</small></td>
     <td>{r.before} → {r.after}</td><td><span className={`result result--${r.status}`}>{r.status}</span></td>
     <td>{r.actor}: {r.reason.replaceAll('_',' ')}{r.restored_from&&<small className="record-id">From {r.restored_from.slice(0,17)}</small>}</td>
     <td>{r.status==='confirmed'&&r.after>=minimum&&r.after<=maximum&&<button className="quiet" disabled={busy||!canRestore||!!review} onClick={()=>setReview({record:r,expected:revision})}>Restore {r.after}</button>}</td>
    </tr>)}
   </tbody></table></div>
   {data.items.length===0&&<p>No recorded changes yet. History begins with this version; earlier limits are not reconstructed.</p>}
  </>}
 </section>;
}
