const status = document.getElementById("copy-status");

for (const button of document.querySelectorAll("[data-copy]")) {
  const field = document.getElementById(button.dataset.copy);
  if (!field) continue;

  button.hidden = false;
  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(field.value);
      status.textContent = "Copied to clipboard.";
    } catch {
      field.focus();
      field.select();
      status.textContent = "Text selected. Use your device’s Copy command.";
    }
  });
}

const selectAll = document.getElementById("select-all-capabilities");

if (selectAll) {
  const options = Array.from(selectAll.form.querySelectorAll('input[name="capability"]'));

  function updateSelectAll() {
    const selected = options.filter(option => option.checked).length;
    selectAll.checked = selected === options.length;
    selectAll.indeterminate = selected > 0 && selected < options.length;
  }

  selectAll.addEventListener("change", () => {
    for (const option of options) option.checked = selectAll.checked;
    updateSelectAll();
  });
  for (const option of options) option.addEventListener("change", updateSelectAll);

  updateSelectAll();
  selectAll.closest("label").hidden = false;
}

const passwordToggle = document.getElementById("password-toggle");
if (passwordToggle) {
  const input = document.getElementById("password");
  passwordToggle.hidden = false;
  passwordToggle.addEventListener("click", () => {
    const visible = input.type === "password";
    input.type = visible ? "text" : "password";
    passwordToggle.textContent = visible ? "Hide" : "Show";
    passwordToggle.setAttribute("aria-label", visible ? "Hide password" : "Show password");
  });
}

const googleCode = document.getElementById("google-code");
if (googleCode) {
  const invite = document.getElementById("invite");
  googleCode.disabled = false;
  googleCode.form.addEventListener("submit", event => {
    if (!invite.reportValidity()) event.preventDefault();
    googleCode.value = invite.value;
  });
}

const cards = document.querySelectorAll(".directory .project-card");
if (cards.length && window.gsap) {
  gsap.matchMedia().add("(prefers-reduced-motion: no-preference)", () => {
    gsap.from(cards, {y: 6, opacity: 0.5, duration: 0.24, stagger: 0.035, clearProps: "opacity,transform"});
  });
}

const projectSearch = document.querySelector(".project-search");
if (projectSearch) {
  projectSearch.hidden = false;
  const input = projectSearch.querySelector("input");

  const filter = () => {
    const term = input.value.trim().toLocaleLowerCase();
    for (const card of cards) card.hidden = !card.dataset.projectName.toLocaleLowerCase().includes(term);
    document.getElementById("no-matches").hidden = Array.from(cards).some(card => !card.hidden);
  };
  input.addEventListener("input", filter);
  filter();
}

const rail = document.querySelector(".project-rail");
if (rail) {
  const toggle = rail.querySelector(".rail-toggle");
  const mobile = matchMedia("(max-width: 700px)");
  const motion = matchMedia("(prefers-reduced-motion: no-preference)");
  rail.dataset.enhanced = "true";
  toggle.hidden = false;
  function setOpen(open, animate = true) {
    rail.dataset.open = String(open);
    toggle.setAttribute("aria-expanded", String(open));
    const width = mobile.matches ? "100%" : open ? "188px" : "54px";
    if (window.gsap && animate && motion.matches && !mobile.matches) {
      gsap.to(rail, {width, duration: 0.2, ease: "power2.out", overwrite: true});
    } else {
      window.gsap?.killTweensOf(rail);
      rail.style.width = width;
    }
  }
  rail.addEventListener("pointerenter", event => {
    if (!mobile.matches && event.pointerType === "mouse") setOpen(true);
  });
  rail.addEventListener("pointerleave", () => {
    if (!mobile.matches && !rail.querySelector(":focus-visible")) setOpen(false);
  });
  rail.addEventListener("focusin", event => {
    if (!mobile.matches && event.target.matches("a:focus-visible")) setOpen(true);
  });
  rail.addEventListener("focusout", event => {
    if (!rail.contains(event.relatedTarget)) setOpen(false);
  });
  toggle.addEventListener("click", () => setOpen(rail.dataset.open !== "true"));
  rail.addEventListener("keydown", event => {
    if (event.key === "Escape") { toggle.focus(); setOpen(false); }
  });
  mobile.addEventListener("change", () => setOpen(false, false));
  setOpen(false, false);
}

const metrics = document.getElementById("workload");
if (metrics) {
  let busy = false;
  let lastAttempt = Date.now();
  async function refreshMetrics() {
    if (document.hidden || busy || Date.now() - lastAttempt < 59000) return;
    busy = true;
    lastAttempt = Date.now();
    try {
      const response = await fetch(metrics.dataset.source, {cache: "no-store"});
      if (response.redirected || response.status === 404) { location.reload(); return; }
      if (!response.ok) throw new Error("Activity unavailable");
      const doc = new DOMParser().parseFromString(await response.text(), "text/html");
      const next = doc.getElementById("workload");
      if (!next || next.dataset.source !== metrics.dataset.source) throw new Error("Unexpected activity response");
      metrics.replaceChildren(...next.children);
      if (next.dataset.poll) document.getElementById("latest-poll").textContent = next.dataset.poll;
    } catch {
      metrics.querySelector(".metrics-head span").textContent = "Refresh delayed · showing last update";
    } finally {
      busy = false;
    }
  }
  setInterval(refreshMetrics, 60000);
  document.addEventListener("visibilitychange", refreshMetrics);
}
