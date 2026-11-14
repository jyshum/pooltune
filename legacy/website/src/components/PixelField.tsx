import { useEffect, useRef } from 'react';

// Background for the Works with section: a flat grid of pixels seen from above.
// Swells travel across it like a wave viewed from overhead. Between swells the field is
// empty and dark. Inside a swell, pixels are grey toward its edges and fade to cyan at its
// crest, and crest pixels are drawn a little larger, so the field reads as depth rather
// than a vertical wave.

const CELL = 11; // pixel pitch in CSS px
const SIZES = [3, 3.5, 4, 4.5, 5]; // trough to crest
const SHADES = 14;
const GAP = 0.5; // surface height below which nothing is drawn, leaving dark space between swells
const GREY = [150, 150, 150];
const CYAN = [63, 181, 196]; // --brand

// Colour and alpha across a swell, from its edge (0) to its crest (1). The outer part of each
// swell is grey. Cyan blends in only over the inner part, so every swell is a grey to cyan gradient.
const shadeStyle = Array.from({ length: SHADES }, (_, i) => {
  const f = i / (SHADES - 1);
  const t = Math.min(1, Math.max(0, (f - 0.55) / 0.4));
  const mix = t * t * (3 - 2 * t);
  const [r, g, b] = GREY.map((c, k) => Math.round(c + (CYAN[k] - c) * mix));
  const a = 0.22 + f * 0.38;
  return `rgba(${r}, ${g}, ${b}, ${a.toFixed(3)})`;
});

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
      const a = Math.sin(x * 0.014 + y * 0.006 - t * 0.8);
      const b = Math.sin(x * -0.005 + y * 0.017 - t * 0.55 + 1.3);
      const d = Math.hypot(x - w * 0.15, y + h * 0.6);
      const c = Math.sin(d * 0.022 - t * 1.05);
      return (a * 0.5 + b * 0.3 + c * 0.2 + 1) / 2;
    };

    const draw = (time: number) => {
      const t = time / 1000;
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
          if (v < GAP) continue;
          // Position inside the swell, 0 at its edge and 1 at its crest.
          const u = Math.min(1, (v - GAP) / (0.92 - GAP));
          const si = Math.min(SIZES.length - 1, Math.floor(u * SIZES.length));
          const shade = Math.min(SHADES - 1, Math.floor(u * SHADES));
          buckets[si][shade].push(cx, cy);
        }
      }

      buckets.forEach((byShade, si) => {
        const size = SIZES[si];
        byShade.forEach((list, shade) => {
          if (!list.length) return;
          ctx.fillStyle = shadeStyle[shade];
          ctx.beginPath();
          for (let i = 0; i < list.length; i += 2) ctx.rect(list[i] - size / 2, list[i + 1] - size / 2, size, size);
          ctx.fill();
        });
      });
    };

    const loop = (now: number) => {
      if (visible && !document.hidden) draw(now - start);
      raf = requestAnimationFrame(loop);
    };

    resize();
    const ro = new ResizeObserver(() => {
      resize();
      if (reduce) draw(3000);
    });
    ro.observe(canvas);
    const io = new IntersectionObserver(([e]) => (visible = e.isIntersecting));
    io.observe(canvas);
    if (reduce) draw(3000);
    else raf = requestAnimationFrame(loop);

    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      io.disconnect();
    };
  }, []);

  return <canvas ref={ref} className="pixel-field" aria-hidden="true" />;
}
