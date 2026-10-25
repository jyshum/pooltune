import {describe,it,expect} from 'vitest';
import {advanceTraffic,emptyTraffic,trafficFresh} from './traffic';
const start=Date.parse('2026-10-25T12:00:00Z');
const sample=(seconds:number,total:string,epoch='one')=>({at:new Date(start+seconds*1000).toISOString(),total,epoch});
describe('measured transaction trend',()=>{
 it('uses counter deltas and actual elapsed seconds without rounding uint64',()=>{
  let s=advanceTraffic(emptyTraffic(),sample(0,'9007199254740993'),start);
  expect(s.points).toHaveLength(0);
  s=advanceTraffic(s,sample(2,'9007199254741000'),start+2000);
  expect(s.points.at(-1)?.rate).toBe(3.5);
 });
 it('distinguishes measured zero from missing and stale data',()=>{
  let s=advanceTraffic(emptyTraffic(),sample(0,'4'),start);
  s=advanceTraffic(s,sample(1,'4'),start+1000);expect(s.points.at(-1)?.rate).toBe(0);
  expect(trafficFresh(s.anchor,start+5000)).toBe(false);
  s=advanceTraffic(s,null,start+2000);expect(s.anchor).toBeNull();
  s=advanceTraffic(s,sample(3,'20'),start+3000);expect(s.points.at(-1)?.rate).toBeNull();
 });
 it('rebaselines counter resets, process changes and gaps',()=>{
  for(const next of [sample(1,'2'),sample(1,'15','two'),sample(5,'20'),sample(-1,'15')]) {
   const s=advanceTraffic(advanceTraffic(emptyTraffic(),sample(0,'10'),start),next,Date.parse(next.at));
   expect(s.points.filter(p=>p.rate!==null)).toHaveLength(0);
  }
 });
 it('ignores duplicate readings and rejects invalid counters',()=>{
  const s=advanceTraffic(emptyTraffic(),sample(0,'1'),start);
  expect(advanceTraffic(s,sample(0,'1'),start)).toBe(s);
  for(const total of ['-1','1.5','NaN','18446744073709551616']) expect(advanceTraffic(s,sample(1,total),start+1000).anchor).toBeNull();
 });
 it('colors only measured spikes and bounds history to 60 seconds',()=>{
  let s=advanceTraffic(emptyTraffic(),sample(0,'0'),start);
  for(let i=1;i<=6;i++) s=advanceTraffic(s,sample(i,String(i*10)),start+i*1000);
  expect(s.points.at(-1)?.spike).toBe(false);
  s=advanceTraffic(s,sample(7,'100'),start+7000);expect(s.points.at(-1)?.spike).toBe(true);
  for(let i=8;i<=90;i++) s=advanceTraffic(s,sample(i,String(100+(i-7)*10)),start+i*1000);
  expect(s.points.every(p=>p.at>=start+30000)).toBe(true);
  expect(s.points.length).toBeLessThanOrEqual(61);
 });
});
