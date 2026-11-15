import {MemorySaver} from '../../../investigator/node_modules/@langchain/langgraph/dist/index.js';
import {PostgresSaver} from '../../../investigator/node_modules/@langchain/langgraph-checkpoint-postgres/dist/index.js';
import pg from '../../../investigator/node_modules/pg/lib/index.js';
import {runDiagnosticProtocol} from '../../../investigator/diagnostic-protocol.ts';
const pool=process.env.DIAGNOSTIC_TEST_STATE_DSN?new pg.Pool({connectionString:process.env.DIAGNOSTIC_TEST_STATE_DSN,max:1}):undefined;
const saver=pool?new PostgresSaver(pool,undefined,{schema:'pooltune_diagnostic'}):new MemorySaver();
if(pool)await saver.setup();
await runDiagnosticProtocol(async view=>{
 if(process.env.DIAGNOSTIC_TEST_EXIT==='1'&&view.observations.some(o=>o.tool==='analyze'))process.exit(75);
 return {decision:view.observations.some(o=>o.tool==='analyze')?
 {action:'propose',repair:'analyze',tools:[],assessments:[{cause:'index',assessment:'rejected',blocks_decision:false,reason:'An index exists.'},{cause:'analyze',assessment:'supported',blocks_decision:false,reason:'Stored frequency differs from measured rows.'}],reason:'Refresh stale statistics.'}:
 {action:'request_evidence',repair:null,tools:['analyze'],assessments:[{cause:'index',assessment:'rejected',blocks_decision:false,reason:'An index exists.'},{cause:'analyze',assessment:'uncertain',blocks_decision:true,reason:'Need stored frequency.'}],reason:'Check stale statistics.'}
};},saver,process.stdin,process.stdout);
await pool?.end();
process.stdin.destroy();
