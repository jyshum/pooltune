import { MemorySaver } from '@langchain/langgraph';
import { PostgresSaver } from '@langchain/langgraph-checkpoint-postgres';
import pg from 'pg';
import { diagnosticDecider } from './diagnostic-model.ts';
import { runDiagnosticProtocol } from './diagnostic-protocol.ts';

let pool:pg.Pool|undefined;
try{
 if(process.env.POOLTUNE_DIAGNOSTIC_REPLAY==='1'){
  await runDiagnosticProtocol(diagnosticDecider,new MemorySaver(),process.stdin,process.stdout);
 }else{
  const url=new URL(process.env.POOLTUNE_LAB_STATE_DSN??'');
  const staging=process.env.POOLTUNE_STAGING_PRIVATE_STATE==='1';
  const route=staging?url.hostname==='postgres.railway.internal'&&url.port==='5432'&&url.pathname==='/railway'&&url.search==='':url.hostname==='127.0.0.1'&&!!url.port&&/^\/[^/]+$/.test(url.pathname)&&url.search==='?sslmode=disable';
  if(!['postgres:','postgresql:'].includes(url.protocol)||!url.username||!url.password||url.hash||!route)throw Error('Invalid state route');
  pool=new pg.Pool({connectionString:url.href,max:1,connectionTimeoutMillis:2500,statement_timeout:3000});
  const saver=new PostgresSaver(pool,undefined,{schema:'pooltune_diagnostic'});await saver.setup();
  await runDiagnosticProtocol(diagnosticDecider,saver,process.stdin,process.stdout);
 }
}catch{process.stderr.write('Pooltune diagnostic process failed.\n');process.exitCode=1;}
finally{await pool?.end();process.stdin.destroy();}
