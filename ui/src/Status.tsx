import {ThinkingOrb} from 'thinking-orbs';
import SpotlightCard from './reactbits/SpotlightCard';
import type {CSSProperties,ReactNode} from 'react';

export type Tone='live'|'hold'|'alert'|'off';
const orbState={live:'working',hold:'breathing',alert:'solving',off:'breathing'} as const;

// Status pill with a small Thinking Orb. The label sits in its own span so tests and
// screen readers read exactly the status text.
export function Status({tone,label}:{tone:Tone;label:string}){
 return <span className={`status status--${tone}`}><span className="status__orb" aria-hidden="true"><ThinkingOrb state={orbState[tone]} size={20} theme="dark" paused={tone==='off'}/></span><span>{label}</span></span>;
}

const surface={'--spotlight-card-surface':'var(--surface)','--spotlight-card-border':'var(--border)'} as CSSProperties;

// One headline number, on the same React Bits spotlight card the public site uses.
export function Stat({label,value,unit,note}:{label:string;value:ReactNode;unit?:string;note:ReactNode}){
 return <SpotlightCard className="stat" spotlightColor="#3fb5c4" intensity={0.08} borderGlow={0.35} theme="dark" style={surface} data-animate>
  <span className="stat__label">{label}</span><strong>{value}{unit&&<em>{unit}</em>}</strong><small>{note}</small>
 </SpotlightCard>;
}
