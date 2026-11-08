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
    for (const c of containers) {
      const row = document.querySelector(`tr.container[data-id="${CSS.escape(c.id)}"]`);
      if (!row) continue;
      row.className = `container state-${c.state}${c.health === "unhealthy" ? " is-unhealthy" : ""}${c.crashLooping ? " is-crashloop" : ""}`;
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
      const fill = row.querySelector(".bar-fill");
      fill.style.width = `${Number.isFinite(pct) ? Math.min(pct, 100) : 0}%`;
      fill.className = `bar-fill${pct >= 85 ? " bar-bad" : pct >= 50 ? " bar-warn" : ""}`;
      row.querySelector(".mem").textContent = c.memory;
      row.querySelector(".status").textContent = c.status;
    }
    refreshSummaries(containers);
  } catch (_) {
    // Network hiccup: try again next tick.
  }
}

// Keep the overview tiles and each Stack's pill in step with the refreshed rows.
function needsAttention(c) {
  return c.crashLooping || c.health === "unhealthy" || c.state === "restarting" || c.state === "dead" ||
    (c.state === "exited" && !c.status.startsWith("Exited (0)"));
}

function refreshSummaries(containers) {
  const running = containers.filter((c) => c.state === "running").length;
  const runTile = document.querySelector('[data-tile="running"]');
  if (runTile) runTile.innerHTML = `${running}<small>/ ${containers.length}</small>`;
  const attTile = document.querySelector('[data-tile="attention"]');
  if (attTile) attTile.textContent = containers.filter(needsAttention).length;

  for (const card of document.querySelectorAll("section.stack.card")) {
    const pill = card.querySelector(".stack-pill");
    if (!pill) continue;
    const rows = [...card.querySelectorAll("tr.container")];
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
    // Trim in batches, so a busy log doesn't rewrite the whole view on every line.
    if (count > MAX_LINES + 2000) {
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
