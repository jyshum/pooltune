import assert from 'node:assert/strict';
import {test} from 'node:test';
import {captureCronPeriod} from './cron-traffic.ts';
import {ownedCronFailureLine, waitOwnedCronActivation} from './owned-cron-traffic.ts';

test('the observer captures the same busy, control and quiet slots for each of six cycles', async () => {
  const first = Date.parse('2026-11-15T13:00:00Z');
  const seen: [string, number, boolean, number | undefined][] = [];
  let at = first;
  for (let index = 0; index < 6; index++) {
    const cycle = first + index * 120_000;
    const period = await captureCronPeriod(cycle, index, async next => { at = next; },
      async (name, seconds, targetOn, observedCycle) => {
        seen.push([name, at - first, targetOn, observedCycle]);
        return {start: new Date(at).toISOString(), end: new Date(at + seconds * 1000).toISOString(), blocks: [name]};
      });
    const group = index < 3 ? 'original' : 'future';
    assert.equal(period.cycle, new Date(cycle).toISOString());
    assert.deepEqual(period.blocks, [`${group}_busy`]);
    assert.equal('blocks' in period.control, false);
    assert.deepEqual(seen.slice(-3), [
      [`${group}_busy`, cycle - first, true, cycle],
      [`${group}_control`, cycle - first + 30_000, true, undefined],
      [`${group}_quiet`, cycle - first + 60_000, false, undefined],
    ]);
  }
});

test('a late slot cannot become an observed Cron period', async () => {
  const first = Date.parse('2026-11-15T13:00:00Z');
  let at = first;
  await assert.rejects(captureCronPeriod(first, 0, async next => { at = next; }, async (_name, seconds) => ({
    start: new Date(at + 3000).toISOString(), end: new Date(at + 3000 + seconds * 1000).toISOString(), blocks: [],
  })), /window/i);
});

test('the producer waits for exact activation without using the preceding slot', async () => {
  const first = Date.parse('2026-11-15T13:00:00Z');
  let read = 0;
  const phases = [-90_000, -55_000, -45_000];
  await waitOwnedCronActivation(first, async () => ({now: first + phases[read], witnessActive: read >= 1, active: ++read === 3}), async () => {});
  assert.equal(read, 3);
  await assert.rejects(waitOwnedCronActivation(first, async () => ({now: first - 60_000, active: true, witnessActive: true})), /before/);
  await assert.rejects(waitOwnedCronActivation(first, async () => ({now: first - 47_000, active: true, witnessActive: false})), /witness/i);
  await assert.rejects(waitOwnedCronActivation(first, async () => ({now: first, active: false, witnessActive: false})), /expired/);
});

test('the private observer identifies its failed phase without logging error details', () => {
  const failure = Object.assign(new Error('postgres://sensitive-login:secret@private-host/railway'), {code: 'ECONNREFUSED'});
  assert.equal(ownedCronFailureLine('target_connection', failure),
    'Owned Cron observation failed (ECONNREFUSED). Phase: target_connection.');
  const unknown = Object.assign(new Error('secret reason'), {code: 'SECRT'});
  assert.equal(ownedCronFailureLine('fixture_catalog', unknown),
    'Owned Cron observation failed (OBSERVER_ERROR). Phase: fixture_catalog.');
});
