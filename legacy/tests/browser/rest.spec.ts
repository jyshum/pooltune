import { test, expect } from '@playwright/test';

test('real browser preserves REST, refresh, CORS, errors, streaming and abort', async ({page}) => {
 page.on('requestfailed', request => console.error('Browser request failure:', new URL(request.url()).pathname, request.failure()?.errorText));
 await page.goto('/');await page.waitForFunction(()=>Boolean((window as any).ready));
 const result=await page.evaluate(async()=>{
  const route:typeof fetch=(window as any).route;
  // Auth refresh must stay on the project, not the REST gateway.
  const refresh=await route('http://127.0.0.1:12161/auth/v1/token',{method:'POST'});
  const {access_token}=await refresh.json();
  const headers={Authorization:`Bearer ${access_token}`,apikey:'public-fixture-key',Prefer:'return=representation','Content-Type':'application/json'};
  const url='http://127.0.0.1:12161/rest/v1/items';
  const read=await route(url,{headers});const count=read.headers.get('Content-Range');await read.text();
  const write=await route(new Request(url,{method:'POST',headers,body:'{"id":1}'}));await write.text();
  const rpc=await route('http://127.0.0.1:12161/rest/v1/rpc/add',{method:'POST',headers,body:'{}'});await rpc.text();
  const denied=await route(url+'?tenant=bob',{headers});const deniedBody=await denied.json();
  const invalid=await route(url,{headers:{...headers,Authorization:'Bearer invalid'}});await invalid.text();
  const stream=await route('http://127.0.0.1:12161/rest/v1/stream',{headers});const reader=stream.body!.getReader();const first=await reader.read();let rest='';for(;;){const c=await reader.read();if(c.done)break;rest+=new TextDecoder().decode(c.value)}
  const overload=await Promise.all(Array.from({length:8},async(_,i)=>{const r=await route('http://127.0.0.1:12161/rest/v1/slow?request='+i,{headers});const code=r.headers.get('X-Pooltune-Error');await r.text();return {status:r.status,code}}));
  const abort=new AbortController();const hanging=await route('http://127.0.0.1:12161/rest/v1/hang',{headers,signal:abort.signal});const pending=hanging.body!.getReader();await pending.read();abort.abort();let aborted=false;try{await pending.read()}catch{aborted=true}
  return {read:read.status,count,write:write.status,rpc:rpc.status,denied:denied.status,deniedBody,invalid:invalid.status,first:new TextDecoder().decode(first.value),rest,overload,aborted};
 });
 expect(result).toMatchObject({read:200,count:'0-0/1',write:201,rpc:201,denied:403,deniedBody:{code:'42501'},invalid:401,first:'first',rest:'second',aborted:true});
 expect(result.overload.some(r=>r.status===503&&!!r.code)).toBe(true);
 await expect.poll(async()=>{const r=await page.request.get('/stats');return (await r.json()).scheduler.Uncertain}).toBe(1);
});
