import { useEffect, useRef } from 'react';

// Background for the Works with section: a flat grid of grey pixels seen from above.
// Swells travel across it like a wave viewed from overhead. Crests are brighter and drawn
// a little larger, so the field reads as depth rather than a vertical wave.
// Random cyan pixels flash on and off on top.

const CELL = 14; // pixel pitch in CSS px
const SIZES = [2, 3, 4, 5]; // trough to crest
const SHADES = 6;
const SPARK = [95, 232, 245]; // neon cyan, brighter than --brand
const SPARK_LIFE = 0.9; // seconds
const SPARK_RATE = 0.005; // new sparks per cell per second

type Spark = { col: number; row: number; born: number };

export default function PixelField() {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    const ctx = canvas?.getContext('2d');
    if (!canvas || !ctx) return;
    const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false;
    let w = 0;
    let h = 0;
    let raf = 0;
    let visible = false;
    let last = 0;
    let sparks: Spark[] = [];
    const start = performance.now();

    const resize = () => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      w = canvas.clientWidth;
      h = canvas.clientHeight;
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    };

    // Height of the water surface at a point, 0 (trough) to 1 (crest).
    const swell = (x: number, y: number, t: number) => {
      const a = Math.sin(x * 0.011 + y * 0.005 - t * 0.8);
      const b = Math.sin(x * -0.004 + y * 0.013 - t * 0.55 + 1.3);
      const d = Math.hypot(x - w * 0.15, y + h * 0.6);
      const c = Math.sin(d * 0.018 - t * 1.05);
      const v = (a * 0.5 + b * 0.3 + c * 0.2 + 1) / 2;
      // Sharpen so crests are narrow bands and troughs stay dark.
      const s = Math.min(1, Math.max(0, (v - 0.3) / 0.7));
      return s * s * (3 - 2 * s);
    };

    const draw = (time: number) => {
      const t = time / 1000;
      const dt = last ? Math.min(0.1, t - last) : 0;
      last = t;
      ctx.clearRect(0, 0, w, h);
      const cols = Math.ceil(w / CELL);
      const rows = Math.ceil(h / CELL);
      // buckets[size][shade] holds x, y pairs so each frame is a handful of fills.
      const buckets = SIZES.map(() => Array.from({ length: SHADES }, () => [] as number[]));

      for (let row = 0; row < rows; row++) {
        for (let col = 0; col < cols; col++) {
          const cx = col * CELL + CELL / 2;
          const cy = row * CELL + CELL / 2;
          const v = swell(cx, cy, t);
          const si = Math.min(SIZES.length - 1, Math.floor(v * SIZES.length));
          const shade = Math.min(SHADES - 1, Math.floor(v * SHADES));
          buckets[si][shade].push(cx, cy);
        }
      }

      buckets.forEach((byShade, si) => {
        const size = SIZES[si];
        byShade.forEach((list, shade) => {
          if (!list.length) return;
          const a = 0.05 + (shade / (SHADES - 1)) * 0.17;
          ctx.fillStyle = `rgba(200, 200, 200, ${a.toFixed(3)})`;
          ctx.beginPath();
          for (let i = 0; i < list.length; i += 2) ctx.rect(list[i] - size / 2, list[i + 1] - size / 2, size, size);
          ctx.fill();
        });
      });

      // Sparks: spawn at random cells, fade in and out over their life.
      const spawn = cols * rows * SPARK_RATE * dt;
      for (let n = spawn + Math.random(); n >= 1; n--) {
        sparks.push({ col: Math.floor(Math.random() * cols), row: Math.floor(Math.random() * rows), born: t });
      }
      sparks = sparks.filter(s => t - s.born < SPARK_LIFE);
      const [r, g, b] = SPARK;
      for (const s of sparks) {
        const k = Math.sin((Math.PI * (t - s.born)) / SPARK_LIFE);
        const cx = s.col * CELL + CELL / 2;
        const cy = s.row * CELL + CELL / 2;
        ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${(0.18 * k).toFixed(3)})`;
        ctx.fillRect(cx - 7, cy - 7, 14, 14);
        ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${k.toFixed(3)})`;
        ctx.fillRect(cx - 3, cy - 3, 6, 6);
      }
    };

    const loop = (now: number) => {
      if (visible && !document.hidden) draw(now - start);
      else last = 0;
      raf = requestAnimationFrame(loop);
    };

    const still = () => {
      // Reduced motion: one frame with a few fixed sparks.
      draw(3000);
      const cols = Math.ceil(w / CELL);
      const rows = Math.ceil(h / CELL);
      sparks = Array.from({ length: Math.round(cols * rows * 0.0025) }, () => ({
        col: Math.floor(Math.random() * cols),
        row: Math.floor(Math.random() * rows),
        born: 3 - SPARK_LIFE / 2,
      }));
      draw(3000);
    };

    resize();
    const ro = new ResizeObserver(() => {
      resize();
      if (reduce) still();
    });
    ro.observe(canvas);
    const io = new IntersectionObserver(([e]) => (visible = e.isIntersecting));
    io.observe(canvas);
    if (reduce) still();
    else raf = requestAnimationFrame(loop);

    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      io.disconnect();
    };
  }, []);

  return <canvas ref={ref} className="pixel-field" aria-hidden="true" />;
}
