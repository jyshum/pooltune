import { describe, it, expect, vi } from 'vitest';
import { createPooltuneFetch } from './supabase-fetch';

describe('selective REST routing', () => {
 it('rewrites exact REST origin and preserves Request/init semantics', async () => {
  const original = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => new Response(await new Request(input, init).text()));
  const route = createPooltuneFetch('https://project.example', 'https://gateway.example', original);
  const controller = new AbortController();
  const request = new Request('https://project.example/rest/v1/items?select=id%2Cname', {method:'POST',headers:{Authorization:'Bearer old',Prefer:'return=representation'},body:'{"id":1}',signal:controller.signal});
  await route(request,{headers:{Authorization:'Bearer refreshed'},body:'{"id":2}'});
  const forwarded = original.mock.calls[0][0] as Request;
  expect(forwarded.url).toBe('https://gateway.example/rest/v1/items?select=id%2Cname');
  expect(forwarded.headers.get('Authorization')).toBe('Bearer refreshed');
  expect(forwarded.headers.has('Prefer')).toBe(false); // init headers replace Request headers
  expect(forwarded.method).toBe('POST');
  controller.abort(); expect(forwarded.signal.aborted).toBe(true);
 });
 it.each(['/auth/v1/token','/storage/v1/object/x','/functions/v1/x','/realtime/v1','/graphql/v1','/rest/v10/items'])('bypasses %s unchanged',async path=>{
  const original=vi.fn(async()=>new Response('ok'));
  const route=createPooltuneFetch('https://project.example','https://gateway.example',original);
  const url=new URL('https://project.example'+path); await route(url);
  expect(original).toHaveBeenCalledWith(url,undefined);
 });
 it('does not route another origin or bypass a failed gateway',async()=>{
  const original=vi.fn(async()=>{throw new Error('offline')});
  const route=createPooltuneFetch('https://project.example','https://gateway.example',original);
  await expect(route('https://project.example/rest/v1/x')).rejects.toThrow('offline');
  expect(original).toHaveBeenCalledTimes(1);
  original.mockClear();await expect(route('https://other.example/rest/v1/x')).rejects.toThrow();
  expect(original.mock.calls[0][0]).toBe('https://other.example/rest/v1/x');
 });
 it('rejects origins with credentials, paths or insecure remote HTTP',()=>{
  for(const bad of ['https://user:pass@x.example','https://x.example/prefix','http://x.example']) expect(()=>createPooltuneFetch('https://project.example',bad,fetch)).toThrow();
 });
});
