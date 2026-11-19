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

  // Both animations share the same visibility and reduced-motion behavior.
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
  let radius = 0;
  wave.addEventListener("pointermove", (event) => {
    pointer = { x: event.offsetX, y: event.offsetY, inside: true };
  });
  wave.addEventListener("pointerleave", () => {
    if (pointer) pointer.inside = false;
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
  surface(wave, (ctx, w, h, t) => {
    ctx.fillStyle = "#081b1c";
    ctx.fillRect(0, 0, w, h);
    const pitch = Math.max(8, Math.min(13, w / 59));
    const buckets = palette.map(() => new Path2D());
    radius +=
      ((pointer?.inside && !reduced.matches ? Math.min(95, w * 0.16) : 0) -
        radius) *
      0.22;
    for (let col = 0; col * pitch < w; col++) {
      const x = col * pitch,
        u = x / w;
      const top =
        h *
        (0.39 +
          0.17 * Math.sin(u * 6.4 + t * 0.72) +
          0.13 * Math.sin(u * 12.4 - t * 0.51) +
          0.045 * Math.sin(u * 24 + t * 0.81));
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
        if (pointer && radius > 1)
          level *= Math.max(
            0,
            Math.min(
              1,
              (Math.hypot(x - pointer.x, y - pointer.y) - radius * 0.55) /
                (radius * 0.45),
            ),
          );
        if (level < 0.047) continue;
        const size = pitch * (0.22 + Math.min(1, level) * 0.57);
        const bucket = Math.min(8, Math.floor(level * 9));
        buckets[bucket].rect(
          x + (pitch - size) / 2,
          y + (pitch - size) / 2,
          size,
          size,
        );
      }
    }
    buckets.forEach((path, i) => {
      ctx.fillStyle = palette[i];
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
              ? "#9ce2d7"
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
    .querySelectorAll("#integrations,.setup,.repairs")
    .forEach((section) => reveal.observe(section));
})();
