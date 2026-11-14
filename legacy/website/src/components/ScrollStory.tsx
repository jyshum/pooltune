import { useEffect, useRef, useState } from 'react';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import TunerOrb, { type OrbMood } from './TunerOrb';
import Wordmark, { withBrand } from './Wordmark';

gsap.registerPlugin(ScrollTrigger);

// Illustrative only. Hand-written states that explain the rules. Not measured data.

const MAX = 12;
const MIN = 2;

type Scene = {
  title: string;
  line: string;
  mood: OrbMood;
  action: string;
  // Each value is [at the start of the step, at the end of the step].
  limit: [number, number];
  busy: [number, number];
  waiting: [number, number];
  cpu: [number, number];
  tuner: boolean;
};

const scenes: Scene[] = [
  {
    title: 'Traffic arrives.',
    line: 'Your app reaches PostgreSQL through a connection pooler.',
    mood: 'idle',
    action: 'Not installed',
    limit: [4, 4], busy: [2, 3], waiting: [0, 0], cpu: [22, 28], tuner: false,
  },
  {
    title: 'A fixed limit makes requests wait.',
    line: 'Set once, it stays the same as requests rise and fall.',
    mood: 'idle',
    action: 'Not installed',
    limit: [4, 4], busy: [4, 4], waiting: [2, 7], cpu: [34, 40], tuner: false,
  },
  {
    title: 'Pooltune opens admission.',
    line: 'More work can run within the budget while safety checks pass.',
    mood: 'working',
    action: 'Growing the limit',
    limit: [4, 8], busy: [4, 8], waiting: [7, 1], cpu: [40, 58], tuner: true,
  },
  {
    title: 'It backs off under pressure.',
    line: 'Unsafe or expired health readings stop new admission.',
    mood: 'alert',
    action: 'Backing off',
    limit: [8, 6], busy: [8, 6], waiting: [1, 3], cpu: [88, 70], tuner: true,
  },
  {
    title: 'It lowers admission again.',
    line: 'When things go quiet, fewer transactions run. The reserve stays warm.',
    mood: 'idle',
    action: 'Shrinking slowly',
    limit: [6, MIN], busy: [0, 0], waiting: [0, 0], cpu: [14, 10], tuner: true,
  },
];

const lerp = ([a, b]: [number, number], p: number) => a + (b - a) * p;

export default function ScrollStory() {
  const rootRef = useRef<HTMLDivElement>(null);
  const [progress, setProgress] = useState(0);

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const st = ScrollTrigger.create({
      trigger: root,
      start: 'top top+=64',
      end: 'bottom bottom',
      onUpdate: self => setProgress(self.progress),
    });
    return () => st.kill();
  }, []);

  const raw = progress * scenes.length;
  const index = Math.min(scenes.length - 1, Math.floor(raw));
  // Hold each step still for its first and last fifth so the state reads clearly.
  const p = Math.min(1, Math.max(0, (raw - index - 0.2) / 0.6));
  const s = scenes[index];
  const limit = Math.round(lerp(s.limit, p));
  const busy = Math.min(limit, Math.round(lerp(s.busy, p)));
  const waiting = Math.round(lerp(s.waiting, p));
  const cpu = Math.round(lerp(s.cpu, p));

  const jump = (i: number) => {
    const root = rootRef.current;
    if (!root) return;
    const range = root.offsetHeight - window.innerHeight + 64;
    window.scrollTo({ top: root.offsetTop - 64 + range * ((i + 0.5) / scenes.length) });
  };

  return (
    <div className="story" ref={rootRef} style={{ height: `${scenes.length * 90 + 60}vh` }}>
      <div className="story__sticky">
        {/* The heading is pinned with the story so it stays in view while scrolling through the steps. */}
        <div className="story__head">
          <p className="kicker">How it works</p>
          <h2 id="how-title">Transaction capacity that adjusts to your requests.</h2>
        </div>
        <div className="story__body">
          <ol className="story__steps">
            {scenes.map((x, i) => (
              <li key={x.title} className={i === index ? 'is-active' : i < index ? 'is-done' : ''}>
                <button type="button" onClick={() => jump(i)} aria-current={i === index ? 'step' : undefined}>
                  <span className="story__n">{String(i + 1).padStart(2, '0')}</span>
                  <span className="story__title">{withBrand(x.title)}</span>
                  <span className="story__line">{withBrand(x.line)}</span>
                </button>
              </li>
            ))}
          </ol>

          <div className="panel" aria-hidden="true">
            <div className="panel__bar">
              <span className="dots"><i /><i /><i /></span>
              <span className="panel__tag">Illustrative</span>
            </div>

            <div className={`tuner${s.tuner ? ' is-on' : ''}`}>
              <TunerOrb mood={s.tuner ? s.mood : 'off'} />
              <div>
                <strong><Wordmark /></strong>
                <span className="tuner__action">{s.action}</span>
              </div>
            </div>
            <div className={`tuner__link${s.tuner ? ' is-on' : ''}`} />

            <div className="flow">
              <div className="node node--app">
                <span className="node__label">App</span>
                <div className="queue">
                  {Array.from({ length: 8 }, (_, i) => <i key={i} className={i < waiting ? 'is-on' : ''} />)}
                </div>
                <span className="node__meta">{waiting} waiting</span>
              </div>

              <div className="wire" />

              <div className="node node--pool">
                <span className="node__label">Admission</span>
                <div className="slots">
                  {Array.from({ length: MAX }, (_, i) => (
                    <i key={i} className={i >= limit ? 'off' : i < busy ? 'busy' : 'open'} />
                  ))}
                </div>
                <span className="node__meta">
                  limit <b>{limit}</b> <em>min {MIN} · max {MAX}</em>
                </span>
              </div>

              <div className="wire" />

              <div className="node node--db">
                <span className="node__label">PostgreSQL</span>
                <div className="cpu">
                  <div className={`cpu__fill${cpu >= 80 ? ' is-hot' : cpu >= 60 ? ' is-warm' : ''}`} style={{ height: `${cpu}%` }} />
                </div>
                <span className="node__meta">CPU {cpu}%</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
