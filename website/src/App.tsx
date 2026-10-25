import { useEffect } from 'react';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import SpotlightCard from './reactbits/SpotlightCard';
import PixelWave from './components/PixelWave';
import ScrollStory from './components/ScrollStory';
import RecordedResults from './components/RecordedResults';
import { limits, repoUrl, setup, targets } from './content';
import PixelField from './components/PixelField';
import Wordmark, { withBrand } from './components/Wordmark';
import DashboardPreview from './components/DashboardPreview';
import { results, validRuns } from './data/results';
import tigerDataLogo from './assets/partners/tiger-data.svg';
import manaLogo from './assets/partners/mana.png';
import architecture from '../../docs/architecture/pooltune.svg';

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
            <Wordmark className="wordmark--nav" />
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
              <Wordmark /> adjusts how many PostgreSQL transactions can run when requests pile up, using a budgeted warm reserve and database health checks.
            </p>
            <div className="hero__cta">
              <a className="btn" href="#get-started">Start your project</a>
              {results.video ? (
                <a className="btn btn--dark" href={results.video.url}>Watch the demo</a>
              ) : (
                <a className="btn btn--dark" href="#how">See how it works</a>
              )}
            </div>
            <div className="hero__built-with" aria-label="Built with Tiger Data and Mana">
              <span>Built with</span>
              <a href="https://www.tigerdata.com" title="Tiger Data powers optional traffic history">
                <img src={tigerDataLogo} width="109" height="24" alt="Tiger Data" />
              </a>
              <a href="https://www.mana.space/a/pooltune" title="Explore the demo built in Mana">
                <img src={manaLogo} width="24" height="24" alt="" />
                <span className="hero__mana-name">Mana</span>
              </a>
            </div>
          </div>
        </section>

        <section className="section section--story" id="how" aria-labelledby="how-title">
          <div className="wrap">
            <ScrollStory />
          </div>
        </section>

        <section className="section" id="architecture" aria-labelledby="architecture-title">
          <div className="wrap">
            <div className="head reveal">
              <p className="kicker">Under the hood</p>
              <h2 id="architecture-title">One clear path to your database.</h2>
            </div>
            <figure className="architecture reveal">
              <a href={architecture} aria-label="Open the full Pooltune architecture diagram">
                <img src={architecture} width="1200" height="700" loading="lazy" alt="Application SQL over TLS flows through the Go admission gateway and owned PgBouncer to Azure, Google Cloud or Supabase. Background health and inventory checks gate admission; SQLite stores state, and the private dashboard can save history to Tiger Data." />
              </a>
              <figcaption><p><strong>Application → Go gateway → owned PgBouncer → managed PostgreSQL.</strong></p>Go + PgBouncer for the managed request path. SQLite for local state. React for the private dashboard. Tiger Data for optional history. Mana for the separate demo. <a href={architecture}>Open full diagram ↗</a></figcaption>
            </figure>
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

        <section className="section section--works" id="works-with" aria-labelledby="works-title">
          <PixelField />
          <div className="wrap">
            <div className="head reveal">
              <p className="kicker">Works with</p>
              <h2 id="works-title">Built for the poolers you already run.</h2>
            </div>
            <div className="targets">
              {targets.map(t => (
                <SpotlightCard key={t.name} className="target reveal" spotlightColor="#3fb5c4" intensity={0.08} borderGlow={0.4} theme="dark">
                  <svg className="target__logo" viewBox={t.logo.viewBox} aria-hidden="true" dangerouslySetInnerHTML={{ __html: t.logo.body }} />
                  <h3>{t.name}</h3>
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
            <div className="setup-layout">
              <div className="setup__start">
                <ol className="setup">
                  {setup.slice(0, 2).map((s, i) => (
                    <li key={s.title} className="setup__step reveal">
                      <span className="setup__n">{i + 1}</span>
                      <h3>{s.title}</h3>
                      <p>{withBrand(s.body)}</p>
                    </li>
                  ))}
                </ol>
                <div className="code reveal">
                  <div className="code__bar"><span className="dots"><i /><i /><i /></span><span>Terminal</span></div>
                  <pre><code><span className="code__dim">$</span> bin/pooltune-burst -config /absolute/private/profile.json -resume</code></pre>
                </div>
                <div className="deployment-note reveal">
                  <p><strong>Docker learning preview</strong></p>
                  <p>Build the Go gateway, PgBouncer and dashboard in one non-root image. ARM64 packaging and real SQL checks are verified; deployment networking and persistent mounts are still being completed.</p>
                  <a className="guide__text-link" href="./setup.html#docker">Build &amp; verify with Docker ↗</a>
                </div>
                <div className="setup__cta reveal">
                  <a className="btn" href="./setup.html">Setup &amp; Download</a>
                  <a className="btn btn--dark" href={repoUrl}>View on GitHub</a>
                </div>
              </div>
              <ol className="setup setup--preview" start={3}>
                <li className="setup__step setup__step--dashboard reveal">
                  <div className="setup__dashboard-bar">
                    <span className="setup__n">3</span>
                    <div><h3>{setup[2].title}</h3><p>{withBrand(setup[2].body)}</p></div>
                  </div>
                  <figure className="setup__preview">
                    <DashboardPreview />
                    <figcaption>Dashboard preview <span aria-hidden="true">·</span> Sample data</figcaption>
                  </figure>
                </li>
              </ol>
            </div>
          </div>
        </section>
      </main>

      <footer className="footer">
        <div className="wrap">
          <p className="footer__limits">{withBrand(limits)}</p>
          <div className="footer__row">
            <a className="brand" href="#top" aria-label="Pooltune home"><Wordmark className="wordmark--footer" /></a>
            <a href={repoUrl}>GitHub</a>
          </div>
        </div>
      </footer>
    </>
  );
}
