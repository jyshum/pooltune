/** Route only the configured project's REST API. There is no automatic bypass. */
export function createPooltuneFetch(projectURL: string, gatewayURL: string, originalFetch: typeof fetch = globalThis.fetch.bind(globalThis)): typeof fetch {
 const origin = (value: string): URL => {
  const u = new URL(value);
  const local = u.hostname === '127.0.0.1' || u.hostname === '[::1]' || u.hostname === 'localhost';
  if (u.username || u.password || u.search || u.hash || u.pathname !== '/' || (u.protocol !== 'https:' && !(local && u.protocol === 'http:'))) throw new Error('Expected an HTTPS origin (HTTP loopback is test-only)');
  return u;
 };
 const project = origin(projectURL), gateway = origin(gatewayURL);
 return async (input, init) => {
  const u = new URL(input instanceof Request ? input.url : String(input));
  if (u.origin !== project.origin || !(u.pathname === '/rest/v1' || u.pathname.startsWith('/rest/v1/'))) return originalFetch(input, init);
  const request = new Request(input, init);
  const target = new URL(request.url); target.protocol = gateway.protocol; target.host = gateway.host;
  // Fetch defaults must not introduce gateway cookies or follow redirects off target.
  const rewritten: RequestInit = {
   method: request.method, headers: request.headers, body: await boundedBody(request),
   signal: request.signal, credentials: 'omit', redirect: 'manual',
   cache: request.cache, integrity: request.integrity, keepalive: request.keepalive,
   mode: request.mode, referrerPolicy: request.referrerPolicy
  };
  return originalFetch(new Request(target, rewritten));
 };
}

// Browser fetch cannot send a ReadableStream body over the HTTP/1 local profile.
// Buffer bounded REST payloads; large object uploads stay on Supabase Storage.
async function boundedBody(request: Request): Promise<ArrayBuffer | null> {
 if (request.signal.aborted) throw request.signal.reason ?? new DOMException('Aborted', 'AbortError');
 if (!request.body) return null;
 const reader = request.body.getReader(), chunks: Uint8Array[] = [];
 let total = 0;
 const abort = () => { void reader.cancel().catch(() => {}); };
 request.signal.addEventListener('abort', abort, {once:true});
 try {
  for (;;) {
   const {done, value} = await reader.read();
   if (request.signal.aborted) throw request.signal.reason ?? new DOMException('Aborted', 'AbortError');
   if (done) break;
   total += value.byteLength;
   if (total > 16 * 1024 * 1024) { await reader.cancel(); throw new RangeError('REST body exceeds 16 MiB'); }
   chunks.push(value);
  }
  const body = new Uint8Array(total); let offset = 0;
  for (const chunk of chunks) {body.set(chunk, offset);offset += chunk.byteLength;}
  return body.buffer;
 } finally {request.signal.removeEventListener('abort', abort);reader.releaseLock();}
}
