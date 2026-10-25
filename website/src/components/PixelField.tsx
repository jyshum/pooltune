import { useEffect, useRef } from 'react';

// Background for the Works with section: a flat grid of pixels seen from above.
// Swells travel across it like a wave viewed from overhead. Troughs are dim grey and
// crests fade into cyan, drawn a little larger, so the field reads as depth rather
// than a vertical wave.

const CELL = 14; // pixel pitch in CSS px
const SIZES = [2, 3, 4, 5]; // trough to crest
const SHADES = 8;
const GREY = [150, 150, 150];
const CYAN = [63, 181, 196]; // --brand

// Colour and alpha for each shade, from grey trough to cyan crest.
const shadeStyle = Array.from({ length: SHADES }, (_, i) => {
  const f = i / (SHADES - 1);
  const mix = f * f; // stay grey through most of the swell, turn cyan near the crest
  const [r, g, b] = GREY.map((c, k) => Math.round(c + (CYAN[k] - c) * mix));
  const a = 0.06 + f * 0.5;
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
