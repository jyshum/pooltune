(() => {
  const root = document.documentElement;
  const reduced = matchMedia("(prefers-reduced-motion: reduce)");
  const roomy = matchMedia("(min-height: 560px)");
  const walk = document.querySelector(".walk");
  const pin = document.querySelector(".pin");
  const steps = [...document.querySelectorAll(".steps button")];
  const blockValue = document.querySelector("#block-value");
  const timing = document.querySelector("#timing");
  const states = [
    ["866", "16.605 ms"],
    ["866", "16.605 ms"],
    ["4", "0.070 ms"],
  ];
  let stage = 0;
  let progress = 0;

  // The canvases share the same visibility and reduced-motion behavior.
  function surface(canvas, paint) {
    const context = canvas.getContext("2d");
    if (!context) return () => {};
    let frame = 0;
    let previous = -Infinity;
    let onscreen = false;
    let width = 0,
      height = 0;
    function tick(now) {
      frame = 0;
      if (!onscreen || document.hidden || !width || !height) return;
      if (now - previous >= 32 || reduced.matches) {
        paint(context, width, height, reduced.matches ? 0 : now / 1000);
        previous = now;
      }
      if (!reduced.matches) frame = requestAnimationFrame(tick);
    }
    function schedule() {
      if (!frame) frame = requestAnimationFrame(tick);
    }
    new ResizeObserver(() => {
      width = canvas.clientWidth;
      height = canvas.clientHeight;
      const scale = Math.min(devicePixelRatio || 1, 2);
      canvas.width = Math.round(width * scale);
      canvas.height = Math.round(height * scale);
      context.setTransform(scale, 0, 0, scale, 0, 0);
      previous = -Infinity;
      schedule();
    }).observe(canvas);
    new IntersectionObserver(([entry]) => {
      onscreen = entry.isIntersecting;
      schedule();
    }).observe(canvas);
    document.addEventListener("visibilitychange", schedule);
    reduced.addEventListener("change", schedule);
    return schedule;
  }

  const wave = document.querySelector("#wave");
  let pointer = null;
  let offsets = new Float32Array(0);
  let columns = 0;
  wave.addEventListener("pointermove", (event) => {
    pointer = { x: event.offsetX, y: event.offsetY, inside: true };
  });
  function releasePointer() {
    if (pointer) pointer.inside = false;
  }
  wave.addEventListener("pointerleave", releasePointer);
  wave.addEventListener("pointercancel", releasePointer);
  wave.addEventListener("pointerup", (event) => {
    if (event.pointerType !== "mouse") releasePointer();
  });

  const repairs = document.querySelector(".repairs ul");
  repairs.addEventListener("pointermove", (event) => {
    const tile = event.target.closest("li");
    if (!tile || event.pointerType === "touch" || reduced.matches) return;
    const bounds = tile.getBoundingClientRect();
    tile.style.setProperty("--spot-x", event.clientX - bounds.left + "px");
    tile.style.setProperty("--spot-y", event.clientY - bounds.top + "px");
  });

  const palette = [
    "#102b2d",
    "#174144",
    "#20595e",
    "#28777c",
    "#329298",
    "#43b3b9",
    "#64cbd0",
    "#88e2df",
    "#b4f4ec",
  ];
  // Only the bright crest picks up heat. Its darker wake stays teal.
  const emberCrest = ["#894247", "#cb444a", "#ef5052", "#ff645b", "#ff9073"];
  function crestHeight(u, t) {
    return (
      0.39 +
      0.17 * Math.sin(u * 6.4 + t * 0.72) +
      0.13 * Math.sin(u * 12.4 - t * 0.51) +
      0.045 * Math.sin(u * 24 + t * 0.81)
    );
  }
  surface(wave, (ctx, w, h, t) => {
    ctx.fillStyle = "#081b1c";
    ctx.fillRect(0, 0, w, h);
    const pitch = Math.max(8, Math.min(13, w / 59));
    const buckets = palette.map(() => new Path2D());
    const cols = Math.ceil(w / pitch);
    const rows = Math.ceil(h / pitch);
    if (columns !== cols || offsets.length !== cols * rows * 2) {
      offsets = new Float32Array(cols * rows * 2);
      columns = cols;
    }
    const radius = Math.min(54, w * 0.12);
    const repel = pointer?.inside && !reduced.matches;
    for (let col = 0; col * pitch < w; col++) {
      const x = col * pitch,
        u = x / w;
      const top = h * crestHeight(u, t);
      for (let row = 0; row * pitch < h; row++) {
        const y = row * pitch,
          depth = (y - top) / h;
        const grain = ((col * 37 + row * 53 + col * row * 7) % 69) / 69;
        const glow = Math.exp(-Math.abs(depth) * 10);
        const body =
          depth >= 0
            ? 0.35 +
              0.12 * Math.sin(depth * 11 + u * 9 - t * 0.6) -
              depth * 0.16
            : 0.025;
        let level = body + glow * 0.48 + grain * 0.13;
        if (depth < -0.03 && grain < 0.94) level *= 0.44;
        // Move each square from its resting position. The offset follows its
        // target over several frames, so passing the pointer leaves a short wake.
        const slot = (row * cols + col) * 2;
        let targetX = 0,
          targetY = 0;
        if (repel) {
          const dx = x + pitch / 2 - pointer.x;
          const dy = y + pitch / 2 - pointer.y;
          const distance = Math.hypot(dx, dy);
          if (distance < radius) {
            const push = radius * 0.5 * (1 - distance / radius) ** 2;
            targetX = distance ? (push * dx) / distance : push;
            targetY = distance ? (push * dy) / distance : 0;
          }
        }
        const ease = targetX || targetY ? 0.42 : 0.18;
        offsets[slot] = reduced.matches
          ? 0
          : offsets[slot] + (targetX - offsets[slot]) * ease;
        offsets[slot + 1] = reduced.matches
          ? 0
          : offsets[slot + 1] + (targetY - offsets[slot + 1]) * ease;
        if (level < 0.047) continue;
        const size = pitch * (0.22 + Math.min(1, level) * 0.57);
        const bucket = Math.min(8, Math.floor(level * 9));
        buckets[bucket].rect(
          x + (pitch - size) / 2 + offsets[slot],
          y + (pitch - size) / 2 + offsets[slot + 1],
          size,
          size,
        );
      }
    }
    const center = 0.62 + 0.09 * Math.sin(t * 0.32);
    const x = w * center;
    const y = h * crestHeight(center, t);
    const reach = Math.min(w * 0.34, h * 0.48);
    buckets.forEach((path, i) => {
      if (i >= 4) {
        const heat = ctx.createRadialGradient(x, y, 0, x, y, reach);
        heat.addColorStop(0, emberCrest[i - 4]);
        heat.addColorStop(0.49, emberCrest[i - 4]);
        heat.addColorStop(1, palette[i]);
        ctx.fillStyle = heat;
      } else {
        ctx.fillStyle = palette[i];
      }
      ctx.fill(path);
    });
  });

  // The overhead view uses the same three travelling swells as the original
  // Vercel provider section, with dark water between the crests.
  const swellShades = Array.from({ length: 14 }, (_, i) => {
    const f = i / 13;
    const mix = Math.max(0, Math.min(1, (f - 0.55) / 0.4));
    const blend = mix * mix * (3 - 2 * mix);
    const rgb = [63, 181, 196].map((c) => Math.round(150 + (c - 150) * blend));
    return "rgba(" + rgb.join(",") + "," + (0.22 + f * 0.38) + ")";
  });
  surface(document.querySelector("#swells"), (ctx, w, h, t) => {
    ctx.clearRect(0, 0, w, h);
    const paths = swellShades.map(() => new Path2D());
    for (let y = 5.5; y < h; y += 11) {
      for (let x = 5.5; x < w; x += 11) {
        const a = Math.sin(x * 0.014 + y * 0.006 - t * 0.8);
        const b = Math.sin(-x * 0.005 + y * 0.017 - t * 0.55 + 1.3);
        const c = Math.sin(
          Math.hypot(x - w * 0.15, y + h * 0.6) * 0.022 - t * 1.05,
        );
        const crest = Math.min(1, (a * 0.5 + b * 0.3 + c * 0.2) / 0.84);
        if (crest <= 0) continue;
        const size = 3 + 2 * crest;
        paths[Math.round(crest * 13)].rect(
          x - size / 2,
          y - size / 2,
          size,
          size,
        );
      }
    }
    paths.forEach((path, i) => {
      ctx.fillStyle = swellShades[i];
      ctx.fill(path);
    });
  });

  // Illustrative map of table pages. The readouts below it are measured endpoints.
  const redraw = surface(document.querySelector("#blocks"), (ctx, w, h, t) => {
    ctx.clearRect(0, 0, w, h);
    const cols = 28,
      rows = 12;
    const pitch = Math.min((w * 0.78) / cols, (h * 0.77) / rows);
    const left = (w - cols * pitch) / 2,
      top = (h - rows * pitch) / 2;
    const sweep = (t * 0.31 + progress) % 1;
    const route = [
      [2, 6],
      [9, 6],
      [9, 3],
      [19, 3],
      [19, 8],
      [25, 8],
    ];
    const routeLength = route
      .slice(1)
      .reduce(
        (sum, point, i) =>
          sum +
          Math.abs(point[0] - route[i][0]) +
          Math.abs(point[1] - route[i][1]),
        0,
      );
    const size = pitch * 0.67;
    for (let row = 0; row < rows; row++)
      for (let col = 0; col < cols; col++) {
        const order = (col + row * cols) / (cols * rows);
        const distance = (sweep - order + 1) % 1;
        const onRoute =
          (row === 6 && col >= 2 && col <= 9) ||
          (col === 9 && row >= 3 && row <= 6) ||
          (row === 3 && col >= 9 && col <= 19) ||
          (col === 19 && row >= 3 && row <= 8) ||
          (row === 8 && col >= 19 && col <= 25);
        const hit = route.some(
          ([x, y], i) => i > 0 && i < 5 && x === col && y === row,
        );
        let fill = "#204447";
        if (stage === 0)
          fill =
            distance < 0.09
              ? "#ef7866"
              : distance < 0.47
                ? "#408181"
                : "#285457";
        if (stage === 1 && onRoute)
          fill = order < 0.17 + 0.74 * sweep ? "#77d7ce" : "#385e61";
        if (stage === 2 && hit) fill = "#b0fae5";
        ctx.fillStyle = fill;
        ctx.fillRect(left + col * pitch, top + row * pitch, size, size);
      }
    if (stage > 0) {
      const points = route.map(([x, y]) => [
        left + x * pitch + size / 2,
        top + y * pitch + size / 2,
      ]);
      ctx.beginPath();
      points.forEach(([x, y], i) => (i ? ctx.lineTo(x, y) : ctx.moveTo(x, y)));
      ctx.strokeStyle = stage === 1 ? "#6bb5aa" : "#50857c";
      ctx.lineWidth = 1.5;
      ctx.stroke();
      let travel = sweep * routeLength;
      for (let i = 1; i < route.length; i++) {
        const length =
          Math.abs(route[i][0] - route[i - 1][0]) +
          Math.abs(route[i][1] - route[i - 1][1]);
        if (travel <= length) {
          const f = travel / length;
          const x = points[i - 1][0] + (points[i][0] - points[i - 1][0]) * f;
          const y = points[i - 1][1] + (points[i][1] - points[i - 1][1]) * f;
          ctx.fillStyle = "#d5fff0";
          ctx.fillRect(x - size * 0.4, y - size * 0.4, size * 0.8, size * 0.8);
          break;
        }
        travel -= length;
      }
    }
  });

  function select(index) {
    stage = index;
    steps.forEach((step, i) => {
      if (i === index) step.setAttribute("aria-current", "step");
      else step.removeAttribute("aria-current");
    });
    [blockValue.textContent, timing.textContent] = states[index];
    redraw();
  }
  function range() {
    return walk.offsetHeight - pin.offsetHeight;
  }
  function follow() {
    if (!root.classList.contains("pinned")) return;
    const navHeight = document.querySelector("header").offsetHeight;
    progress = Math.max(
      0,
      Math.min(
        1,
        (navHeight - walk.getBoundingClientRect().top) / Math.max(1, range()),
      ),
    );
    const index = Math.min(
      steps.length - 1,
      Math.floor(progress * steps.length),
    );
    if (index !== stage) select(index);
  }
  function layout() {
    root.classList.toggle("pinned", roomy.matches && !reduced.matches);
    follow();
  }
  steps.forEach((button, i) =>
    button.addEventListener("click", () => {
      if (root.classList.contains("pinned")) {
        const start =
          scrollY +
          walk.getBoundingClientRect().top -
          document.querySelector("header").offsetHeight;
        window.scrollTo({
          top: start + (range() * (i + 0.5)) / steps.length,
          behavior: "instant",
        });
      }
      select(i);
    }),
  );
  root.classList.add("enhanced");
  layout();
  roomy.addEventListener("change", layout);
  reduced.addEventListener("change", layout);
  window.addEventListener("resize", layout);
  window.addEventListener("scroll", follow, { passive: true });
  const reveal = new IntersectionObserver((entries) => {
    for (const entry of entries)
      if (entry.isIntersecting) {
        entry.target.classList.add("arrived");
        reveal.unobserve(entry.target);
      }
  });
  document
    .querySelectorAll(
      ".walk .section-head, .repairs .section-head, .repairs li, .provider-heading, .providers > *, .setup > div, .setup li",
    )
    .forEach((section) => reveal.observe(section));
})();
