// scd dashboard script. Intentionally tiny and dependency-free.
"use strict";

// Stack checkboxes save as soon as they change. Saving only changes the selection;
// nothing deploys until an Apply.
// Saved in the background, then the page reloads in place (keeping the scroll position)
// because the change marks any Sync Plan stale.
document.addEventListener("change", async (e) => {
  const box = e.target.closest(".select input[type=checkbox]");
  if (!box) return;
  box.disabled = true;
  try {
    await fetch(box.form.action, { method: "POST", body: new URLSearchParams(new FormData(box.form)) });
  } finally {
    sessionStorage.setItem("scd-scroll", String(window.scrollY));
    location.reload();
  }
});
try {
  const y = sessionStorage.getItem("scd-scroll");
  if (y !== null) { sessionStorage.removeItem("scd-scroll"); window.scrollTo(0, Number(y)); }
} catch (_) { /* storage unavailable: nothing to restore */ }

// Sync and Apply can take a moment: show that the click registered.
document.addEventListener("submit", (e) => {
  const btn = e.target.querySelector("button.sync-button, button.apply-button, button.apply-cta");
  if (!btn) return;
  btn.disabled = true;
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
    for (const c of containers) {
      const row = document.querySelector(`tr.container[data-id="${CSS.escape(c.id)}"]`);
      if (!row) continue;
      row.className = `container state-${c.state}`;
      row.querySelector(".state").textContent = c.state;
      const health = row.querySelector(".health");
      health.textContent = c.health;
      health.className = `health health-${c.health === "—" ? "" : c.health}`;
      row.querySelector(".restarts").textContent = c.restarts;
      row.querySelector(".restarts-badge").classList.toggle("is-zero", !c.restarts);
      row.querySelector(".cpu").textContent = c.cpu;
      const pct = parseFloat(c.cpu);
      row.querySelector(".bar-fill").style.width = `${Number.isFinite(pct) ? Math.min(pct, 100) : 0}%`;
      row.querySelector(".mem").textContent = c.memory;
      row.querySelector(".status").textContent = c.status;
    }
  } catch (_) {
    // Network hiccup: try again next tick.
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

// Container logs: tail, then follow live. Pause stops appending without losing lines.
const logView = document.querySelector("pre.log[data-stream]");
if (logView) {
  const MAX_LINES = 10000;
  const pauseBtn = document.querySelector(".log-pause");
  const status = document.querySelector(".log-status");
  let paused = false;
  let held = [];
  let count = 0;

  const append = (lines) => {
    const atBottom = logView.scrollHeight - logView.scrollTop - logView.clientHeight < 40;
    logView.append(document.createTextNode(lines.join("\n") + "\n"));
    count += lines.length;
    if (count > MAX_LINES) {
      const text = logView.textContent.split("\n");
      logView.textContent = text.slice(text.length - MAX_LINES - 1).join("\n");
      count = MAX_LINES;
    }
    if (atBottom) logView.scrollTop = logView.scrollHeight;
  };

  const live = document.querySelector(".live");
  const es = new EventSource(logView.dataset.stream);
  es.onopen = () => { if (live) live.hidden = paused; };
  es.onmessage = (e) => (paused ? held.push(e.data) : append([e.data]));
  es.addEventListener("error", (e) => { if (e.data) status.textContent = "error: " + e.data; });
  es.addEventListener("done", () => {
    es.close();
    if (live) live.hidden = true;
    status.textContent = "end of log";
  });

  pauseBtn.addEventListener("click", () => {
    paused = !paused;
    pauseBtn.textContent = paused ? "Resume" : "Pause";
    pauseBtn.setAttribute("aria-pressed", String(paused));
    if (!paused && held.length) { append(held); held = []; }
    status.textContent = paused ? "paused" : "";
    if (live && es.readyState !== EventSource.CLOSED) live.hidden = paused;
  });
}
