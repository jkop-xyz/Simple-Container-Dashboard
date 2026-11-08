// scd dashboard script. Intentionally tiny and dependency-free.
"use strict";

// Stack checkboxes save as soon as they change. Saving only changes the selection;
// nothing deploys until an Apply.
// Saved in the background, then the page reloads in place (keeping the scroll position)
// because the change marks any Sync Plan stale.
document.addEventListener("change", async (e) => {
  const box = e.target.closest(".select input[type=checkbox]");
  if (!box) return;
  box.setAttribute("aria-busy", "true");
  try {
    await fetch(box.form.action, { method: "POST", body: new URLSearchParams(new FormData(box.form)) });
  } finally {
    try {
      sessionStorage.setItem("scd-scroll", String(window.scrollY));
      sessionStorage.setItem("scd-focus", box.dataset.stack || "");
    } catch (_) { /* storage unavailable */ }
    location.reload();
  }
});
// After that reload: back to the same scroll position, focus on the same switch.
try {
  const y = sessionStorage.getItem("scd-scroll");
  const stack = sessionStorage.getItem("scd-focus");
  sessionStorage.removeItem("scd-scroll");
  sessionStorage.removeItem("scd-focus");
  if (y !== null) window.scrollTo(0, Number(y));
  if (stack) {
    const box = document.querySelector(`.select input[data-stack="${CSS.escape(stack)}"]`);
    if (box) box.focus({ preventScroll: true });
  }
} catch (_) { /* storage unavailable: nothing to restore */ }

// Sync and Apply can take a moment: show that the click registered.
document.addEventListener("submit", (e) => {
  const btn = e.target.querySelector("button.sync-button, button.apply-button, button.apply-cta");
  if (!btn) return;
  if (btn.dataset.busy) { e.preventDefault(); return; } // already submitted once
  btn.dataset.busy = "1";
  btn.setAttribute("aria-disabled", "true");
  btn.classList.add("is-busy");
  const label = btn.matches(".sync-button") ? "Syncing…" : "Applying…";
  btn.innerHTML = `<span class="spinner" aria-hidden="true"></span>${label}`;
});

// Follow a running Apply's output live, then reload to show the results.
const applyOutput = document.querySelector(".apply-output[data-stream]");
if (applyOutput) {
  const already = applyOutput.textContent.split("\n").filter((l, i, a) => i < a.length - 1).length;
  const es = new EventSource(`${applyOutput.dataset.stream}?from=${already}`);
  es.onmessage = (e) => {
    const span = document.createElement("span");
    if (e.data.startsWith("==>")) span.className = "ln-step";
    else if (e.data.startsWith("!!!")) span.className = "ln-error";
    span.textContent = e.data;
    applyOutput.append(span, "\n");
    applyOutput.scrollTop = applyOutput.scrollHeight;
  };
  es.addEventListener("done", () => { es.close(); location.reload(); });
}

// Refresh container state, health and usage every 5 seconds while the tab is visible.
const REFRESH_MS = 5000;
let refreshTimer = null;

async function refreshStatus() {
  try {
    const res = await fetch("/api/status", { cache: "no-store" });
    if (!res.ok) return;
    const { containers } = await res.json();
    const peakMem = Math.max(0, ...containers.map((c) => c.memoryBytes || 0));
    for (const c of containers) {
      const row = document.querySelector(`tr.container[data-id="${CSS.escape(c.id)}"]`);
      if (!row) continue;
      row.className = `container state-${c.state} si-${statusIcon(c)}${c.health === "unhealthy" ? " is-unhealthy" : ""}${c.crashLooping ? " is-crashloop" : ""}`;
      const loop = row.querySelector(".crashloop-badge");
      if (loop) loop.hidden = !c.crashLooping;
      row.dataset.health = c.health;
      row.dataset.restarts = c.restarts;
      row.dataset.status = c.status;
      row.querySelector(".state").textContent = c.state;
      const health = row.querySelector(".health");
      health.textContent = c.health;
      health.className = `health health-${c.health === "—" ? "" : c.health}`;
      row.querySelector(".restarts").textContent = c.restarts;
      row.querySelector(".restarts-badge").classList.toggle("is-zero", !c.restarts);
      row.querySelector(".cpu").textContent = c.cpu;
      const pct = parseFloat(c.cpu);
      const load = pct >= 85 ? " bar-bad" : pct >= 50 ? " bar-warn" : "";
      const fill = row.querySelector(".bar-fill");
      fill.style.width = `${Number.isFinite(pct) ? Math.min(pct, 100) : 0}%`;
      fill.className = `bar-fill${load}`;
      const spark = row.querySelector(".spark-fill");
      if (spark) { spark.setAttribute("d", c.spark || ""); spark.setAttribute("class", `spark-fill${load}`); }
      const membar = row.querySelector(".membar-fill");
      if (membar) membar.style.width = `${peakMem ? (c.memoryBytes / peakMem) * 100 : 0}%`;
      row.querySelector(".mem").textContent = c.memory;
      row.querySelector(".status").textContent = c.status;
    }
    refreshSummaries(containers);
  } catch (_) {
    // Network hiccup: try again next tick.
  }
}

// The row's status icon: matches sicon() on the server.
function statusIcon(c) {
  if (needsAttention(c)) return "err";
  if ((c.state === "running" && c.health === "starting") || c.state === "restarting") return "pend";
  return c.state === "running" ? "ok" : "off";
}

// Keep the overview tiles and each Stack's pill in step with the refreshed rows.
function needsAttention(c) {
  return c.crashLooping || c.health === "unhealthy" || c.state === "restarting" || c.state === "dead" ||
    (c.state === "exited" && !c.status.startsWith("Exited (0)"));
}

function refreshSummaries(containers) {
  const set = (key, n) => {
    const el = document.querySelector(`[data-count="${key}"]`);
    if (!el) return;
    el.textContent = n;
    if (key === "attention") el.classList.toggle("nav-count-bad", n > 0);
  };
  set("total", containers.length);
  set("attention", containers.filter(needsAttention).length);
  set("stopped", containers.filter((c) => c.state !== "running").length);

  for (const group of document.querySelectorAll("tbody.stack")) {
    const pill = group.querySelector(".stack-pill");
    if (!pill) continue;
    const rows = [...group.querySelectorAll("tr.container")];
    const up = rows.filter((r) => r.classList.contains("state-running")).length;
    const sick = rows.filter((r) => r.classList.contains("is-unhealthy")).length;
    const looping = rows.filter((r) => r.classList.contains("is-crashloop")).length;
    const restarted = rows.some((r) => Number(r.dataset.restarts) > 0);
    pill.textContent = `${looping ? `${looping} crash-looping · ` : ""}${sick ? `${sick} unhealthy · ` : ""}${up}/${rows.length} running`;
    pill.className = `badge stack-pill ${sick || looping ? "badge-bad" : up < rows.length || restarted ? "badge-warn" : "badge-ok"}`;
  }
}

function scheduleRefresh(delay) {
  clearTimeout(refreshTimer);
  if (document.hidden) return;
  refreshTimer = setTimeout(async () => {
    await refreshStatus();
    scheduleRefresh(REFRESH_MS);
  }, delay);
}

document.addEventListener("visibilitychange", () => scheduleRefresh(document.hidden ? 0 : 100));
// The first CPU figure needs two samples, so refresh soon after load.
if (document.querySelector("tr.container")) scheduleRefresh(1500);

// Log views: tail, then follow live. Filter, line count, timestamps, pause and download.
const logView = document.querySelector("pre.log[data-stream]");
if (logView) {
  const MAX_LINES = 10000;
  const $ = (sel) => document.querySelector(sel);
  const pauseBtn = $(".log-pause"), status = $(".log-status"), live = $(".live");
  const filterBox = $(".log-filter"), countEl = $(".log-count");
  const linesSel = $(".log-lines"), stampsBox = $(".log-timestamps"), tailEl = $(".log-tail");
  let lines = [], held = [], paused = false, es = null, filter = "";

  const atBottom = () => logView.scrollHeight - logView.scrollTop - logView.clientHeight < 40;
  const matches = (l) => !filter || l.toLowerCase().includes(filter);

  // One line as text, with filter matches marked.
  function lineNode(l) {
    const el = document.createElement("span");
    if (!filter) { el.textContent = l + "\n"; return el; }
    const low = l.toLowerCase();
    let i = 0;
    for (let j = low.indexOf(filter); j >= 0; j = low.indexOf(filter, i)) {
      el.append(l.slice(i, j));
      const m = document.createElement("mark");
      m.textContent = l.slice(j, j + filter.length);
      el.append(m);
      i = j + filter.length;
    }
    el.append(l.slice(i) + "\n");
    return el;
  }

  function updateCount() {
    if (!countEl) return;
    countEl.textContent = filter ? `${lines.filter(matches).length} of ${lines.length} lines` : `${lines.length} lines`;
  }

  function render() {
    const stick = atBottom();
    const frag = document.createDocumentFragment();
    for (const l of lines) if (matches(l)) frag.append(lineNode(l));
    logView.replaceChildren(frag);
    updateCount();
    if (stick || !filter) logView.scrollTop = logView.scrollHeight;
  }

  function append(batch) {
    const stick = atBottom();
    lines.push(...batch);
    if (lines.length > MAX_LINES + 2000) { // trim in batches, not on every line
      lines = lines.slice(lines.length - MAX_LINES);
      render();
      return;
    }
    const frag = document.createDocumentFragment();
    for (const l of batch) if (matches(l)) frag.append(lineNode(l));
    logView.append(frag);
    updateCount();
    if (stick) logView.scrollTop = logView.scrollHeight;
  }

  function connect() {
    if (es) es.close();
    lines = []; held = [];
    logView.replaceChildren();
    status.textContent = "";
    const params = new URLSearchParams();
    if (linesSel) params.set("lines", linesSel.value);
    if (stampsBox && stampsBox.checked) params.set("timestamps", "1");
    if (tailEl && linesSel) tailEl.textContent = linesSel.value;
    es = new EventSource(`${logView.dataset.stream}?${params}`);
    es.onopen = () => { if (live) live.hidden = paused; };
    es.onmessage = (e) => (paused ? held.push(e.data) : append([e.data]));
    es.addEventListener("error", (e) => { if (e.data) status.textContent = "error: " + e.data; });
    es.addEventListener("done", () => {
      es.close();
      if (live) live.hidden = true;
      status.textContent = "end of log";
    });
  }

  pauseBtn.addEventListener("click", () => {
    paused = !paused;
    pauseBtn.textContent = paused ? "Resume" : "Pause";
    pauseBtn.setAttribute("aria-pressed", String(paused));
    if (!paused && held.length) { append(held); held = []; }
    status.textContent = paused ? "paused" : "";
    if (live && es && es.readyState !== EventSource.CLOSED) live.hidden = paused;
  });

  let filterTimer;
  filterBox?.addEventListener("input", () => {
    clearTimeout(filterTimer);
    filterTimer = setTimeout(() => { filter = filterBox.value.trim().toLowerCase(); render(); }, 120);
  });
  linesSel?.addEventListener("change", connect);
  stampsBox?.addEventListener("change", connect);

  $(".log-download")?.addEventListener("click", () => {
    const text = lines.filter(matches).join("\n") + "\n";
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    a.download = `${logView.dataset.name || "log"}-${new Date().toISOString().replace(/[:.]/g, "-")}.log`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  });

  connect();
}

// Side navigation drawer on narrow screens: Escape or a click outside closes it, and
// focus goes back to the toggle.
const navToggle = document.querySelector(".nav-toggle");
const scrim = document.querySelector(".scrim");
const appbar = document.querySelector(".appbar");
const syncAppbarHeight = () => {
  if (appbar) document.documentElement.style.setProperty("--appbar", `${appbar.offsetHeight}px`);
};
syncAppbarHeight();
window.addEventListener("resize", syncAppbarHeight);
function setNav(open, { focusToggle = false } = {}) {
  document.body.classList.toggle("nav-open", open);
  navToggle?.setAttribute("aria-expanded", String(open));
  if (scrim) scrim.hidden = !open;
  if (open) document.querySelector("#sidenav .nav-item")?.focus();
  else if (focusToggle) navToggle?.focus();
}
navToggle?.addEventListener("click", () => setNav(!document.body.classList.contains("nav-open")));
scrim?.addEventListener("click", () => setNav(false, { focusToggle: true }));
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && document.body.classList.contains("nav-open")) setNav(false, { focusToggle: true });
});

// Search: on the dashboard it narrows the table live (by container name, image or
// Stack); from other pages, or without JS, it submits to /?q=.
const search = document.querySelector(".global-search");
const table = document.querySelector("table.containers");
if (search && table) {
  const status = document.querySelector(".no-match");
  const apply = () => {
    const q = search.value.trim().toLowerCase();
    let shown = 0;
    for (const group of document.querySelectorAll("tbody.stack")) {
      const stackHit = q && (group.dataset.stack || "").toLowerCase().includes(q);
      let visible = 0;
      for (const row of group.querySelectorAll("tr.container")) {
        const hit = !q || stackHit || row.dataset.name.toLowerCase().includes(q) || row.dataset.image.toLowerCase().includes(q);
        row.hidden = !hit;
        if (hit) visible++;
      }
      group.hidden = q !== "" && !stackHit && visible === 0;
      shown += visible;
    }
    for (const row of document.querySelectorAll("#stacks .stack-row")) {
      row.hidden = q !== "" && !row.dataset.stack.toLowerCase().includes(q);
    }
    for (const sel of [".charts", "#attention", "#sync"]) {
      const el = document.querySelector(sel);
      if (el) el.hidden = q !== "";
    }
    if (status) {
      status.hidden = !q;
      status.textContent = shown ? `${shown} container${shown === 1 ? "" : "s"} match “${search.value.trim()}”.` : "No containers match your search.";
    }
  };
  search.form?.addEventListener("submit", (e) => { e.preventDefault(); apply(); });
  search.addEventListener("input", apply);
  search.addEventListener("keydown", (e) => { if (e.key === "Escape") { search.value = ""; apply(); } });
}
document.addEventListener("keydown", (e) => {
  if (e.key === "/" && search && document.activeElement === document.body) { e.preventDefault(); search.focus(); }
});

// Host CPU and memory charts, drawn as SVG from /api/metrics.
const charts = [...document.querySelectorAll(".chart[data-series]")];
if (charts.length) {
  const SVG = "http://www.w3.org/2000/svg";
  const el = (name, attrs) => {
    const n = document.createElementNS(SVG, name);
    for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
    return n;
  };
  const fmtBytes = (b) => b >= 1 << 30 ? `${(b / (1 << 30)).toFixed(1)} Gi` : `${Math.round(b / (1 << 20))} Mi`;
  const fmt = (v, unit) => unit === "bytes" ? fmtBytes(v) : (v === 0 ? "0" : v < 0.1 ? v.toFixed(2) : v.toFixed(v < 10 ? 2 : 1));
  const clock = (t) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  // A "nice" top for the y axis: 1, 2 or 5 × 10^n, above the peak.
  const niceTop = (v, unit) => {
    if (v <= 0) return unit === "bytes" ? 1 << 28 : 0.1;
    const p = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v * 1.1) return m * p;
    return 10 * p;
  };

  function draw(box, points) {
    if (points.length < 2) return;
    const unit = box.dataset.unit;
    const cs = getComputedStyle(box);
    const W = (box.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)) || 600;
    const H = (box.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom)) || 200;
    const L = 56, R = 12, T = 12, B = 30;
    const iw = W - L - R, ih = H - T - B;
    const t0 = new Date(points[0].t).getTime(), t1 = new Date(points[points.length - 1].t).getTime();
    const top = niceTop(Math.max(...points.map((p) => p.v)), unit);
    const x = (t) => L + ((new Date(t).getTime() - t0) / Math.max(1, t1 - t0)) * iw;
    const y = (v) => T + ih - (v / top) * ih;

    const svg = el("svg", { viewBox: `0 0 ${W} ${H}` });
    const axis = el("g", { class: "axis" });
    for (let i = 0; i <= 4; i++) {
      const v = (top / 4) * i, yy = y(v);
      svg.append(el("line", { class: "gridline", x1: L, x2: W - R, y1: yy, y2: yy }));
      const label = el("text", { x: L - 8, y: yy + 4, "text-anchor": "end" });
      label.textContent = fmt(v, unit);
      axis.append(label);
    }
    for (let i = 0; i <= 4; i++) {
      const t = t0 + ((t1 - t0) / 4) * i;
      const label = el("text", { x: L + (iw / 4) * i, y: H - 8, "text-anchor": i === 0 ? "start" : i === 4 ? "end" : "middle" });
      label.textContent = clock(t);
      axis.append(label);
    }
    const line = points.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)} ${y(p.v).toFixed(1)}`).join(" ");
    svg.append(el("path", { class: "area", d: `${line} L${x(points[points.length - 1].t).toFixed(1)} ${T + ih} L${L} ${T + ih} Z` }));
    svg.append(el("path", { class: "line", d: line }));
    svg.append(el("line", { class: "baseline", x1: L, x2: W - R, y1: T + ih, y2: T + ih }));
    svg.append(axis);
    box.replaceChildren(svg);

    const now = document.querySelector(`[data-now="${box.dataset.series}"]`);
    const latest = `${fmt(points[points.length - 1].v, unit)}${unit === "bytes" ? "B" : " cores"}`;
    if (now) now.textContent = `${latest} now`;
    box.setAttribute("aria-label", `${box.dataset.series === "cpu" ? "CPU" : "Memory"} usage over the last ${Math.max(1, Math.round((t1 - t0) / 60000))} minutes, now ${latest}`);
  }

  async function refreshCharts() {
    if (document.hidden) return;
    try {
      const res = await fetch("/api/metrics", { cache: "no-store" });
      if (!res.ok) return;
      const data = await res.json();
      for (const box of charts) draw(box, data[box.dataset.series] || []);
    } catch (_) { /* try again next time */ }
  }
  refreshCharts();
  setInterval(refreshCharts, 15000);
  let resizeTimer;
  window.addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(refreshCharts, 200); });
}
