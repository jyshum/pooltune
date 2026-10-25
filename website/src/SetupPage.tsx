import { useEffect, useRef, useState, type ReactNode } from 'react';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import Wordmark from './components/Wordmark';
import PixelWave from './components/PixelWave';
import SpotlightCard from './reactbits/SpotlightCard';
import { logos, type Logo } from './logos';

gsap.registerPlugin(ScrollTrigger);

// Setup guide (setup.html). Copy is Codex's; this file only sets layout and motion to match the homepage.

const archive = './downloads/pooltune-macos-arm64.tar.gz';
const launch = 'bin/pooltune-burst -config /absolute/private/profile.json -resume \\\n  -dashboard-listen 127.0.0.1:8081 -assets /absolute/pooltune/ui';

const steps = [
  { id: 'docker', label: 'Docker preview' },
  { id: 'install', label: 'Install' },
  { id: 'connect', label: 'Connect' },
  { id: 'launch', label: 'Launch' },
  { id: 'dashboard', label: 'Your dashboard' },
];

const providers: { name: string; logo: Logo; body: string }[] = [
  { name: 'Azure', logo: logos.azure, body: 'Direct PostgreSQL endpoint, subscription, resource group and instance. Use an Azure management token for host metrics.' },
  { name: 'Google Cloud', logo: logos.googleCloud, body: 'Direct Cloud SQL endpoint, project and instance. Use the certificate DNS name and a Google access token.' },
  { name: 'Supabase', logo: logos.supabase, body: 'Stable session endpoint, project reference and tenant-qualified SQL login. Use a management token for metrics.' },
];

function Command({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
    }
  }
  return (
    <div className="code">
      <div className="code__bar"><span className="dots"><i /><i /><i /></span><span>Terminal</span><button className="guide__copy" onClick={copy}>{copied ? 'Copied' : 'Copy'}</button></div>
      <pre><code>{text}</code></pre>
    </div>
  );
}

function Step({ n, id, title, children }: { n: number; id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="guide__step" aria-labelledby={`${id}-title`}>
      <span className="guide__node" aria-hidden="true">{n}</span>
      <div className="guide__card reveal">
        <h2 id={`${id}-title`}>{title}</h2>
        {children}
      </div>
    </section>
  );
}

// Highlights the step currently in view in the side menu.
function useActiveStep() {
  const [active, setActive] = useState(steps[0].id);
  useEffect(() => {
    const io = new IntersectionObserver(
      entries => entries.forEach(e => e.isIntersecting && setActive(e.target.id)),
      { rootMargin: '-35% 0px -55% 0px' },
    );
    steps.forEach(s => {
      const el = document.getElementById(s.id);
      if (el) io.observe(el);
    });
    return () => io.disconnect();
  }, []);
  return active;
}

export default function SetupPage() {
  const active = useActiveStep();
  const railRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const mm = gsap.matchMedia();
    mm.add('(prefers-reduced-motion: no-preference)', () => {
      gsap.from('.hero__inner > *', { opacity: 0, y: 18, duration: 0.8, ease: 'power3.out', stagger: 0.08 });
      gsap.utils.toArray<HTMLElement>('.reveal').forEach(el => {
        gsap.from(el, { opacity: 0, y: 24, duration: 0.7, ease: 'power2.out', scrollTrigger: { trigger: el, start: 'top 90%', once: true } });
      });
      // The cyan rail fills as you scroll through the steps.
      gsap.fromTo('.guide__rail-fill', { scaleY: 0 }, { scaleY: 1, ease: 'none', scrollTrigger: { trigger: railRef.current, start: 'top 60%', end: 'bottom 60%', scrub: true } });
    });
    return () => mm.revert();
  }, []);

  return (
    <>
      <a className="skip" href="#main">Skip to content</a>
      <header className="nav">
        <div className="wrap nav__inner">
          <a className="brand" href="./" aria-label="Pooltune home"><Wordmark className="wordmark--nav" /></a>
          <nav aria-label="Setup navigation"><a href="./">Back to Pooltune</a><a href="#dashboard">Service UI</a></nav>
          <a className="btn btn--sm nav__cta" href={archive} download>Download</a>
        </div>
      </header>

      <main id="main">
        <section className="hero hero--guide">
          <PixelWave />
          <div className="wrap hero__inner">
            <p className="kicker">Get started</p>
            <h1>Your pool.<span>Your dashboard.</span></h1>
            <p className="hero__sub">Run Pooltune next to your application, connect your database, and open your private service UI.</p>
            <div className="hero__cta">
              <a className="btn" href={archive} download>Download for Mac</a>
              <a className="btn btn--dark" href="./downloads/managed-setup-guide.md">Full configuration guide</a>
            </div>
            <p className="guide__meta">macOS · Apple Silicon · PgBouncer required</p>
          </div>
        </section>

        <div className="wrap guide">
          <aside className="guide__contents" aria-label="On this page">
            <p className="kicker">On this page</p>
            {steps.map((s, i) => (
              <a key={s.id} href={`#${s.id}`} className={active === s.id ? 'is-active' : ''} aria-current={active === s.id ? 'location' : undefined}>
                <span>{i === 0 ? '◇' : String(i).padStart(2, '0')}</span> · {s.label}
              </a>
            ))}
          </aside>

          <div className="guide__steps" ref={railRef}>
            <div className="guide__rail" aria-hidden="true"><div className="guide__rail-fill" /></div>

            <section id="docker" className="guide__card deployment-note reveal" aria-labelledby="docker-title">
              <p className="kicker">Docker learning preview</p>
              <h2 id="docker-title">Build the container.</h2>
              <p>The non-root runtime includes the Go gateway, PgBouncer 1.26.1 and dashboard assets. Linux ARM64 packaging and a real PostgreSQL check are verified. No prebuilt registry image is published yet.</p>
              <p>Install Docker Engine, Buildx and Compose first. On a Mac, use <a href="https://github.com/jyshum/pooltune/blob/main/deploy/docker/README.md#docker-cli--colima-on-macos">Docker CLI + Colima</a>, then select that engine in your terminal.</p>
              <Command text={'git clone https://github.com/jyshum/pooltune.git\ncd pooltune\ndocker build --target runtime -t pooltune:learning .\ndocker compose -f compose.verify.yaml run --build --rm verify\ndocker run --rm --network none pooltune:learning -help'} />
              <p>This builds and checks the package without cloud credentials. It does not start a configured managed service. Application/dashboard networking, persistent state and container crash/restart verification are still open. The four steps below remain the native macOS setup.</p>
              <a className="guide__text-link" href="https://github.com/jyshum/pooltune/blob/main/deploy/docker/README.md">Docker guide and current limits ↗</a>
            </section>

            <Step n={1} id="install" title="Install the package.">
              <p>Download the archive into a new folder. Extract it, verify the included checksums, and install PgBouncer with Homebrew.</p>
              <Command text={'tar -xzf pooltune-macos-arm64.tar.gz\ncd pooltune\nshasum -a 256 -c SHA256SUMS\nbrew install pgbouncer'} />
              <a className="guide__text-link" href="./downloads/pooltune-macos-arm64.tar.gz.sha256">Archive checksum</a>
            </Step>

            <Step n={2} id="connect" title="Connect your database.">
              <p>Create separate private state and credential folders. Put your database connection strings and provider token in private files, then create a <code>burst-gateway-1</code> profile with your approved bounds.</p>
              <div className="guide__providers">
                {providers.map(p => (
                  <SpotlightCard key={p.name} className="guide__provider" spotlightColor="#3fb5c4" intensity={0.08} borderGlow={0.4} theme="dark">
                    <div className="guide__provider-head">
                      <svg className="guide__provider-logo" viewBox={p.logo.viewBox} aria-hidden="true" dangerouslySetInnerHTML={{ __html: p.logo.body }} />
                      <h3>{p.name}</h3>
                    </div>
                    <p>{p.body}</p>
                  </SpotlightCard>
                ))}
              </div>
              <p>The <a href="./downloads/managed-setup-guide.md">configuration guide</a> includes the full profile, TLS setup requirements, permissions and provider-specific fields. Keep passwords and tokens in server files.</p>
            </Step>

            <Step n={3} id="launch" title="Start your service.">
              <p>Use absolute paths for your profile and the extracted UI folder. The gateway checks its connections and budget before accepting traffic.</p>
              <Command text={launch} />
              <p>Point your application's PostgreSQL connection at <code>127.0.0.1:6439</code>, keep its database login, and trust the gateway certificate. The application and gateway run on the same host.</p>
            </Step>

            <Step n={4} id="dashboard" title="Open your dashboard.">
              <p>Visit <code>http://127.0.0.1:8081</code>. Read <code>claim.token</code> from your private state folder, claim the installation, and choose your administrator password.</p>
              <figure className="guide__preview">
                <div className="guide__preview-bar"><span className="dots"><i /><i /><i /></span><span>127.0.0.1:8081</span></div>
                <img src="./downloads/managed-dashboard.png" alt="Pooltune service dashboard with capacity, queue and provider resource readings" loading="lazy" />
                <figcaption>Dashboard preview with sample data. Your installation shows its own live readings.</figcaption>
              </figure>
              <p>Watch admission, established connections, queued work and resource pressure. The dashboard is read-only. Press Ctrl+C in the service terminal to stop new admission and let active work drain.</p>
            </Step>

            <div className="guide__resources reveal">
              <h3>Keep these nearby.</h3>
              <div className="guide__resource-list">
                <a href="./downloads/managed-setup-guide.md">Configuration guide ↗</a>
                <a href="./downloads/managed-mvp-results.md">Measured results and operating limits ↗</a>
                <a href="https://github.com/jyshum/pooltune">Source on GitHub ↗</a>
              </div>
            </div>
          </div>
        </div>
      </main>

      <footer className="footer">
        <div className="wrap footer__row">
          <a className="brand" href="./" aria-label="Pooltune home"><Wordmark className="wordmark--footer" /></a>
          <a href="./">Back to Pooltune</a>
        </div>
      </footer>
    </>
  );
}
