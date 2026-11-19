(() => {
  const canvas = document.querySelector("#wave");
  const ctx = canvas.getContext("2d");
  const motion = matchMedia("(prefers-reduced-motion: reduce)");
  let visible = false,
    frame = 0,
    phase = 0,
    last = 0;
  let pointer = 0,
    tilt = 0;
  function draw(now) {
    frame = 0;
    if (!ctx) return;
    const w = canvas.clientWidth,
      h = canvas.clientHeight;
    const ratio = Math.min(devicePixelRatio || 1, 2);
    if (
      canvas.width !== Math.round(w * ratio) ||
      canvas.height !== Math.round(h * ratio)
    ) {
      canvas.width = Math.round(w * ratio);
      canvas.height = Math.round(h * ratio);
    }
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    ctx.fillStyle = "#101830";
    ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = "#31415e";
    for (let y = 12; y < h; y += 16)
      for (let x = 12; x < w; x += 16) ctx.fillRect(x, y, 1, 1);
    if (!motion.matches) phase += Math.min(now - last, 60) * 0.00037;
    last = now;
    tilt += (pointer - tilt) * 0.1;
    const size = Math.min(w / 56, h / 32);
    for (let row = 0; row < 27; row++) {
      for (let col = 0; col < 44; col++) {
        const u = col / 43,
          v = row / 26;
        const z =
          Math.sin(u * 7 + v * 2 + phase + tilt) *
          (0.72 + 0.27 * Math.cos(v * 4));
        const x = w * (0.07 + 0.66 * u + 0.195 * v);
        const y = h * (0.4 - 0.1 * u + 0.39 * v - 0.18 * z);
        ctx.fillStyle =
          "hsl(" +
          (221 + 7 * u) +
          " 96% " +
          (43 + (28 * (z + 1)) / 2 + 10 * (1 - v)) +
          "%)";
        ctx.fillRect(x, y, size * 0.84, size * 0.84);
      }
    }
    if (visible && !motion.matches && !document.hidden)
      frame = requestAnimationFrame(draw);
  }
  function repaint() {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(draw);
  }
  canvas.addEventListener("pointermove", (event) => {
    if (!motion.matches)
      pointer = (event.offsetX / canvas.clientWidth - 0.5) * 3;
  });
  canvas.addEventListener("pointerleave", () => {
    pointer = 0;
  });
  new ResizeObserver(repaint).observe(canvas);
  new IntersectionObserver(([entry]) => {
    visible = entry.isIntersecting;
    repaint();
  }).observe(canvas);
  motion.addEventListener("change", repaint);
  document.addEventListener("visibilitychange", repaint);

  const steps = [...document.querySelectorAll(".steps article")];
  const scenes = [...document.querySelectorAll(".scene")];
  const mobile = matchMedia("(max-width:760px)");
  function select(index) {
    steps.forEach((step, i) => step.classList.toggle("active", i === index));
    scenes.forEach((scene, i) => {
      scene.hidden = i !== index;
    });
  }
  function followScroll() {
    if (mobile.matches) return;
    let selected = 0;
    steps.forEach((step, i) => {
      if (step.getBoundingClientRect().top <= innerHeight * 0.42) selected = i;
    });
    select(selected);
  }
  document.querySelector(".story").classList.add("interactive");
  select(0);
  steps.forEach((step, index) =>
    step.querySelector("a").addEventListener("click", (event) => {
      if (mobile.matches) {
        event.preventDefault();
        document.querySelector(".record").scrollIntoView({ block: "start" });
      }
      select(index);
    }),
  );
  addEventListener("scroll", followScroll, { passive: true });
  followScroll();
})();
