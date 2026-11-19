(() => {
  const root = document.documentElement;
  const reduced = matchMedia("(prefers-reduced-motion: reduce)");
  const roomy = matchMedia("(min-height: 560px)");
  const walk = document.querySelector(".walk");
  const pin = document.querySelector(".pin");
  const steps = [...document.querySelectorAll(".steps button")];
  const blockValue = document.querySelector("#block-value");
  const timing = document.querySelector("#timing");
  const sampleStage = document.querySelector("#sample-stage");
  const exampleSql = document.querySelector("#example-sql");
  const lookupSql = exampleSql.textContent;
  const indexSql = "CREATE INDEX CONCURRENTLY ON workload\n  (customer, category);";
  const states = [
    ["866", "16.605 ms", "Before index", lookupSql],
    ["866", "16.605 ms", "Pre-build sample", indexSql],
    ["4", "0.070 ms", "After index", lookupSql],
  ];
  let stageStart = 0;
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
    const rgb = [45, 207, 255].map((c) => Math.round(150 + (c - 150) * blend));
    const alpha = 0.22 + f * 0.38 + blend * 0.31;
    return "rgba(" + rgb.join(",") + "," + alpha + ")";
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
        const path = paths[Math.round(crest * 13)];
        path.rect(
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
    // Tint only the existing dots. Each small circle follows a crest of the first swell.
    const radius = 34;
    const wavelength = (2 * Math.PI) / 0.014;
    ctx.save();
    ctx.globalCompositeOperation = "source-atop";
    for (let row = 0; row * 255 < h; row++) {
      const cy = row * 255 + 70 + 24 * Math.sin(t * 0.22 + row);
      const peakX = (Math.PI / 2 - cy * 0.006 + t * 0.8) / 0.014;
      const firstX = ((peakX + radius) % wavelength + wavelength) % wavelength - radius;
      for (let cx = firstX; cx - radius < w; cx += wavelength) {
        const secondSwell = Math.sin(-cx * 0.005 + cy * 0.017 - t * 0.55 + 1.3);
        ctx.globalAlpha = Math.max(0, secondSwell);
        if (!ctx.globalAlpha) continue;
        const glow = ctx.createRadialGradient(cx, cy, 0, cx, cy, radius);
        glow.addColorStop(0, "#ffb36e");
        glow.addColorStop(0.45, "#ff7158");
        glow.addColorStop(1, "rgba(255,100,82,0)");
        ctx.fillStyle = glow;
        ctx.fillRect(cx - radius, cy - radius, radius * 2, radius * 2);
      }
    }
    ctx.restore();
  });

  // Each entry represents one square along the illustrative index path.
  const corners = [[2, 6], [9, 6], [9, 3], [19, 3], [19, 8], [25, 8]];
  const route = [corners[0]];
  for (let i = 1; i < corners.length; i++) {
    let [x, y] = corners[i - 1];
    const [endX, endY] = corners[i];
    while (x !== endX || y !== endY) {
      x += Math.sign(endX - x);
      y += Math.sign(endY - y);
      route.push([x, y]);
    }
  }

  const redraw = surface(document.querySelector("#blocks"), (ctx, w, h, t) => {
    ctx.clearRect(0, 0, w, h);
    const cols = 28;
    const rows = 12;
    const pitch = Math.min((w * 0.78) / cols, (h * 0.77) / rows);
    const left = (w - cols * pitch) / 2;
    const top = (h - rows * pitch) / 2;
    const size = pitch * 0.67;
    const sweep = (t * 0.31 + progress) % 1;
    for (let row = 0; row < rows; row++) {
      for (let col = 0; col < cols; col++) {
        const order = (col + row * cols) / (cols * rows);
        const distance = (sweep - order + 1) % 1;
        ctx.fillStyle = "#204447";
        if (stage === 0) {
          ctx.fillStyle =
            distance < 0.09 ? "#ff7868" : distance < 0.47 ? "#71433f" : "#344f50";
        }
        ctx.fillRect(left + col * pitch, top + row * pitch, size, size);
      }
    }
    if (stage === 0) return;

    const cycle = Math.max(0, t - stageStart) % 4.6;
    const fraction = reduced.matches
      ? 1
      : Math.min(1, Math.max(0, (cycle - 0.45) / 3.35));
    const travel = fraction * (route.length - 1);
    const visited = Math.floor(travel);
    const [col, row] = route[visited];
    const [nextCol, nextRow] = route[Math.min(visited + 1, route.length - 1)];
    const x = col + (nextCol - col) * (travel - visited);
    const y = row + (nextRow - row) * (travel - visited);
    ctx.save();
    ctx.translate(left + size / 2, top + size / 2);
    ctx.scale(pitch, pitch);

    function trace(color, points, count, tipX, tipY) {
      ctx.beginPath();
      ctx.moveTo(points[0][0], points[0][1]);
      for (let i = 1; i < count; i++) ctx.lineTo(points[i][0], points[i][1]);
      ctx.lineTo(tipX, tipY);
      ctx.strokeStyle = color;
      ctx.stroke();
    }

    const last = route[route.length - 1];
    ctx.lineWidth = 0.24;
    trace("#ff6654", corners, corners.length, ...last);
    ctx.shadowColor = "#00b9ff";
    ctx.shadowBlur = 7;
    trace("#38d9ff", route, visited + 1, x, y);
    if (stage === 2) {
      // Turn each of the four block stops blue when the verification reaches it.
      let distance = 0;
      for (let i = 1; i < corners.length - 1; i++) {
        const [cx, cy] = corners[i];
        distance += Math.abs(cx - corners[i - 1][0]) + Math.abs(cy - corners[i - 1][1]);
        const reached = travel >= distance;
        ctx.shadowBlur = reached ? 7 : 0;
        ctx.fillStyle = reached ? "#76eaff" : "#ff6654";
        ctx.fillRect(cx - 0.34, cy - 0.34, 0.68, 0.68);
      }
    }
    ctx.shadowColor = "#00b9ff";
    ctx.shadowBlur = 8;
    ctx.fillStyle = "#deffff";
    ctx.fillRect(x - 0.42, y - 0.42, 0.84, 0.84);
    ctx.restore();
  });

  function select(index) {
    if (stage !== index) stageStart = performance.now() / 1000;
    stage = index;
    steps.forEach((step, i) => {
      if (i === index) step.setAttribute("aria-current", "step");
      else step.removeAttribute("aria-current");
    });
    [
      blockValue.textContent,
      timing.textContent,
      sampleStage.textContent,
      exampleSql.textContent,
    ] = states[index];
    redraw();
  }
  function range() {
    return walk.offsetHeight - pin.offsetHeight;
  }
  const canPin = () => roomy.matches && !reduced.matches;
  function follow() {
    if (!canPin()) return;
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
    steps[index].style.setProperty(
      "--step-progress",
      progress * steps.length - index,
    );
  }
  steps.forEach((button, i) =>
    button.addEventListener("click", () => {
      if (canPin()) {
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
  follow();
  roomy.addEventListener("change", follow);
  reduced.addEventListener("change", follow);
  window.addEventListener("resize", follow);
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
      ".walk .section-head, .steps button, .monitor, .repairs .section-head, .repairs li, .provider-heading, .providers > *, .setup > div, .setup li",
    )
    .forEach((section) => reveal.observe(section));
})();
