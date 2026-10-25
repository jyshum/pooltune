import { useEffect } from 'react';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import SpotlightCard from './reactbits/SpotlightCard';
import PixelWave from './components/PixelWave';
import ScrollStory from './components/ScrollStory';
import RecordedResults from './components/RecordedResults';
import { levelLabel, limits, repoUrl, setup, targets } from './content';
import { results, validRuns } from './data/results';

gsap.registerPlugin(ScrollTrigger);

function useReveal() {
  useEffect(() => {
    const mm = gsap.matchMedia();
    mm.add('(prefers-reduced-motion: no-preference)', () => {
      gsap.from('.hero__inner > *', { opacity: 0, y: 18, duration: 0.8, ease: 'power3.out', stagger: 0.08 });
      gsap.utils.toArray<HTMLElement>('.reveal').forEach(el => {
        gsap.from(el, { opacity: 0, y: 24, duration: 0.7, ease: 'power2.out', scrollTrigger: { trigger: el, start: 'top 90%', once: true } });
      });
    });
    return () => mm.revert();
  }, []);
}

export default function App() {
  useReveal();
  return (
    <>
      <a className="skip" href="#main">Skip to content</a>
      <header className="nav">
        <div className="wrap nav__inner">
          <a className="brand" href="#top" aria-label="Pooltune home">
            <span className="brand__mark" aria-hidden="true"><i /><i /><i /></span>
            Pooltune
          </a>
          <nav aria-label="Sections">
            <a href="#how">How it works</a>
            <a href="#works-with">Works with</a>
            <a href="#get-started">Get started</a>
            <a href={repoUrl}>GitHub</a>
          </nav>
          <a className="btn btn--sm nav__cta" href="#get-started">Start your project</a>
        </div>
      </header>

      <main id="main">
        <section className="hero" id="top">
          <PixelWave />
          <div className="wrap hero__inner">
            <h1>
              Scale your traffic.
              <span>Tune your pool automatically.</span>
            </h1>
            <p className="hero__sub">
              Pooltune grows your PostgreSQL pooler when work piles up and shrinks it when things go quiet. Save costs on database scaling and provisioning.
            </p>
            <div className="hero__cta">
              <a className="btn" href="#get-started">Start your project</a>
              {results.video ? (
                <a className="btn btn--dark" href={results.video.url}>Watch the demo</a>
              ) : (
                <a className="btn btn--dark" href="#how">See how it works</a>
              )}
            </div>
          </div>
        </section>

        <section className="section section--story" id="how" aria-labelledby="how-title">
          <div className="wrap">
            <ScrollStory />
          </div>
        </section>

        {validRuns.length > 0 && (
          <section className="section" id="results" aria-labelledby="results-title">
            <div className="wrap">
              <div className="head reveal">
                <p className="kicker">Results</p>
                <h2 id="results-title">Recorded runs.</h2>
              </div>
              <RecordedResults />
            </div>
          </section>
        )}

        <section className="section" id="works-with" aria-labelledby="works-title">
          <div className="wrap">
            <div className="head reveal">
              <p className="kicker">Works with</p>
              <h2 id="works-title">Built for the poolers you already run.</h2>
            </div>
            <div className="targets">
              {targets.map(t => (
                <SpotlightCard key={t.name} className="target reveal" spotlightColor="#3fb5c4" intensity={0.08} borderGlow={0.4} theme="dark">
                  <h3>{t.name}</h3>
                  <p>{t.pooler}</p>
                  <span className={`status status--${t.level}`}><i />{levelLabel[t.level]}</span>
                </SpotlightCard>
              ))}
            </div>
          </div>
        </section>

        <section className="section section--setup" id="get-started" aria-labelledby="setup-title">
          <div className="wrap">
            <div className="head reveal">
              <p className="kicker">Get started</p>
              <h2 id="setup-title">Start your project.</h2>
            </div>
            <ol className="setup">
              {setup.map((s, i) => (
                <li key={s.title} className="setup__step reveal">
                  <span className="setup__n">{i + 1}</span>
                  <h3>{s.title}</h3>
                  <p>{s.body}</p>
                </li>
              ))}
            </ol>
            <div className="code reveal">
              <div className="code__bar"><span className="dots"><i /><i /><i /></span><span>Terminal</span></div>
              <pre><code><span className="code__dim">$</span> Install guide coming soon</code></pre>
            </div>
            <div className="setup__cta reveal">
              <a className="btn btn--dark" href={repoUrl}>View on GitHub</a>
            </div>
          </div>
        </section>
      </main>

      <footer className="footer">
        <div className="wrap">
          <p className="footer__limits">{limits}</p>
          <div className="footer__row">
            <a className="brand brand--small" href="#top"><span className="brand__mark" aria-hidden="true"><i /><i /><i /></span>Pooltune</a>
            <a href={repoUrl}>GitHub</a>
          </div>
        </div>
      </footer>
    </>
  );
}
