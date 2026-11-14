import {readFileSync,existsSync,mkdirSync,writeFileSync,renameSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';
import {join} from 'node:path';
const root=fileURLToPath(new URL('..',import.meta.url));
const manifest=JSON.parse(readFileSync(join(root,'delivery-manifest.json'),'utf8'));
const hash=data=>createHash('sha256').update(data).digest('hex');
const allowed=['pooltune-macos-arm64.tar.gz','pooltune-macos-arm64.tar.gz.sha256','managed-setup-guide.md','managed-mvp-results.md','managed-dashboard.png'];
if(Object.keys(manifest.files).sort().join()!==allowed.sort().join())throw new Error('Unexpected delivery asset');
if(manifest.base_url!==`https://github.com/jyshum/pooltune/releases/download/${manifest.release}/`)throw new Error('Unexpected release origin');
mkdirSync(join(root,'.delivery'),{recursive:true});
for(const [name,expected] of Object.entries(manifest.files)){
 const path=join(root,'.delivery',name);
 if(existsSync(path)&&hash(readFileSync(path))===expected)continue;
 const response=await fetch(manifest.base_url+name,{signal:AbortSignal.timeout(120000)});
 if(!response.ok)throw new Error(`Download unavailable: ${name} (${response.status})`);
 const bytes=Buffer.from(await response.arrayBuffer());
 if(bytes.length>100*1024*1024||hash(bytes)!==expected)throw new Error(`Delivery integrity check failed: ${name}`);
 writeFileSync(path+'.tmp',bytes);renameSync(path+'.tmp',path);
}
console.log('Delivery assets match the pinned release manifest.');
