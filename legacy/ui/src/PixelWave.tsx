import { useEffect, useRef } from 'react';

// Two side columns of square pixels that rise and fall in waves.
// Drawn on one canvas. Pixels are batched by brightness level so each frame is a few fills.

const CELL = 10; // pixel pitch in CSS px
const SIZE = 6; // pixel size, leaving a 4px gap
const LEVELS = 10;

const [r, g, b] = [63, 181, 196]; // --brand

function hash(x: number, y: number, t: number) {
  const n = Math.sin(x * 127.1 + y * 311.7 + t * 74.7) * 43758.5453;
  return n - Math.floor(n);
}

export default function PixelWave() {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    const ctx = canvas?.getContext('2d');
    if (!canvas || !ctx) return;
    const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false;
    let w = 0;
    let h = 0;
    let raf = 0;
    let visible = true;
    const start = performance.now();

    const resize = () => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      w = canvas.clientWidth;
      h = canvas.clientHeight;
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    };

    const draw = (time: number) => {
      const t = time / 1000;
      ctx.clearRect(0, 0, w, h);
      const cols = Math.ceil(w / CELL);
      const rows = Math.ceil(h / CELL);
      const narrow = w < 720;
      const band = (narrow ? 0.26 : 0.34) * w;
      const flicker = Math.floor(t * 6);
      const buckets: number[][] = Array.from({ length: LEVELS }, () => []);

      for (let c = 0; c < cols; c++) {
        const x = c * CELL;
        const fromEdge = Math.min(x, w - x - CELL);
        if (fromEdge > band) continue;
        // Strongest at the edge, gone at the inner edge of the band.
        const side = 1 - fromEdge / band;
        const mask = side * side * (3 - 2 * side);
        // Count from the outer edge on both sides, but shift the right side's phase so they never mirror.
        const right = x >= w / 2;
        const k = right ? cols - c + 23 : c;
        const shift = right ? 2.3 : 0;
        const crest =
          0.5 + 0.2 * Math.sin(k * 0.21 + t * 1.3 + shift) + 0.12 * Math.sin(k * 0.07 - t * 0.7 + shift) + 0.06 * Math.sin(k * 0.53 + t * 2.1);
        const top = h * (1 - crest * (0.55 + 0.45 * mask));
        for (let row = 0; row < rows; row++) {
          const y = row * CELL;
          const n = hash(c, row, flicker);
          let a: number;
          if (y >= top) {
            // Body of the wave: bright near the crest, dimmer toward the bottom.
            const depth = (y - top) / Math.max(1, h - top);
            a = 0.85 - depth * 0.55 + (n - 0.5) * 0.35;
          } else {
            // Sparse drifting pixels above the crest. The rest of the empty area stays black.
            const gap = (top - y) / h;
            a = n > 0.965 - gap * 0.2 ? 0.55 - gap : 0;
          }
          a *= mask;
          if (a < 0.03) continue;
          buckets[Math.min(LEVELS - 1, Math.floor(a * LEVELS))].push(x, y);
        }
      }

      for (let l = 0; l < LEVELS; l++) {
        const list = buckets[l];
        if (!list.length) continue;
        ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${((l + 0.5) / LEVELS).toFixed(2)})`;
        ctx.beginPath();
        for (let i = 0; i < list.length; i += 2) ctx.rect(list[i], list[i + 1], SIZE, SIZE);
        ctx.fill();
      }
    };

    const loop = (now: number) => {
      if (visible && !document.hidden) draw(now - start);
      raf = requestAnimationFrame(loop);
    };

    resize();
    const ro = new ResizeObserver(() => {
      resize();
      if (reduce) draw(4000);
    });
    ro.observe(canvas);
    const io = new IntersectionObserver(([e]) => (visible = e.isIntersecting));
    io.observe(canvas);
    if (reduce) draw(4000);
    else raf = requestAnimationFrame(loop);

    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      io.disconnect();
    };
  }, []);

  return <canvas ref={ref} className="pixel-wave" aria-hidden="true" />;
}
