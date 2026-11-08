// scd dashboard script. Intentionally tiny and dependency-free.
"use strict";

// Stack checkboxes save as soon as they change. Saving only changes the selection;
// nothing deploys until an Apply.
document.addEventListener("change", (e) => {
  const box = e.target.closest(".select input[type=checkbox]");
  if (box) box.form.submit();
});

// Follow a running Apply's output live, then reload to show the results.
const applyOutput = document.querySelector(".apply-output[data-stream]");
if (applyOutput) {
  const already = applyOutput.textContent.split("\n").filter((l, i, a) => i < a.length - 1).length;
  const es = new EventSource(`${applyOutput.dataset.stream}?from=${already}`);
  es.onmessage = (e) => {
    applyOutput.textContent += e.data + "\n";
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
      row.querySelector(".cpu").textContent = c.cpu;
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
