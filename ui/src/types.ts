export type Group={workload_id:string;label:string;class:'critical'|'normal'|'background';locked:boolean;source:string;allowed_classes?:string[]};
export type State={revision:string;target:string;provider:string;path:string;ready:boolean;frozen:boolean;setup:{verified:boolean;observed:boolean;approved:boolean;groups:Group[];matched_percent:number|null;ai_available:boolean};safety?:{restricted?:boolean;stopped:boolean;resume_ready:boolean;start_capacity:number};limits:{effective:number;physical:number|null;stable:number;candidate:number};metrics:{p95_ms:number|null;error_rate:number|null;queued:number};experiment?:{phase:string;fraction:number;reason:string};gates:string[]};
export type CommandResult={revision:string;status:string};
export type Mutate=(action:string,fields?:Record<string,unknown>)=>Promise<void>;
export class APIError extends Error{constructor(message:string,public status:number){super(message)}}
export async function api<T>(path:string,body?:unknown,csrf?:string):Promise<T>{
 const signal=AbortSignal.timeout(body===undefined?5000:35000);
 let response:Response;
 try { response=await fetch(`/api/${path}`,{method:body===undefined?'GET':'POST',signal,credentials:'same-origin',cache:'no-store',headers:body===undefined?{}:{'Content-Type':'application/json',...(csrf?{'X-CSRF-Token':csrf}:{})},body:body===undefined?undefined:JSON.stringify(body)}); } catch(e) { if(signal.aborted)throw new Error('Private request timed out. Refresh before retrying a change.');throw e; }
 if(!response.ok){if(response.status===401)throw new APIError('Your session expired or credentials were rejected. Sign in again.',401);if(response.status===409)throw new Error('The configuration changed. Refresh and review before trying again.');throw new Error((await response.text()).slice(0,300)||'Request unavailable.');}
 return response.json() as Promise<T>;
}
