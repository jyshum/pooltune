import {MemorySaver} from '../../../investigator/node_modules/@langchain/langgraph/dist/index.js';
import {runDiagnosticProtocol} from '../../../investigator/diagnostic-protocol.ts';
await runDiagnosticProtocol(async view=>({decision:view.observations.some(o=>o.tool==='analyze')?
 {action:'propose',repair:'analyze',tools:[],assessments:[{cause:'index',assessment:'rejected',reason:'An index exists.'},{cause:'analyze',assessment:'supported',reason:'Stored frequency differs from measured rows.'}],reason:'Refresh stale statistics.'}:
 {action:'request_evidence',repair:null,tools:['analyze'],assessments:[{cause:'index',assessment:'rejected',reason:'An index exists.'},{cause:'analyze',assessment:'uncertain',reason:'Need stored frequency.'}],reason:'Check stale statistics.'}
}),new MemorySaver(),process.stdin,process.stdout);
process.stdin.destroy();
