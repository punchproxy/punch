// Logs: live tail of the daemon log stream with a level filter and pause.

import { api, stream } from "../api.js";
import { toastError } from "../ui.js";
import { escapeHtml } from "../util.js";

let ctrl = null;
let lines = [];
let paused = false;
let level = "all";
const MAX = 1000;

export function mount(container) {
  container.innerHTML = `
    <div class="toolbar">
      <select id="log-level" style="max-width:150px;">
        <option value="all">All levels</option>
        <option value="error">Error</option>
        <option value="warn">Warn+</option>
        <option value="info">Info+</option>
      </select>
      <div class="spacer"></div>
      <button class="btn btn-sm" id="log-pause">Pause</button>
      <button class="btn btn-sm btn-ghost" id="log-clear">Clear</button>
    </div>
    <div class="card"><div class="feed" id="log-feed" style="max-height:calc(100vh - 220px);"><div class="empty">Connecting to log stream…</div></div></div>`;

  document.getElementById("log-level").onchange = (e) => { level = e.target.value; paint(); };
  document.getElementById("log-pause").onclick = (e) => {
    paused = !paused;
    e.target.textContent = paused ? "Resume" : "Pause";
    e.target.classList.toggle("btn-primary", paused);
  };
  document.getElementById("log-clear").onclick = () => { lines = []; paint(); };

  loadHistory();
}

export function unmount() { if (ctrl) ctrl.abort(); ctrl = null; }

async function loadHistory() {
  try {
    const snap = await api.get("/logs");
    lines = (snap.entries || []).map((e) => e.line).slice(-MAX);
  } catch { /* stream still provides live lines */ }
  paint();
  ctrl = stream("/logs/stream", (line) => {
    if (paused) return;
    lines.push(line);
    if (lines.length > MAX) lines.shift();
    paint();
  }, { onError: (e) => toastError(e, "Log stream error") });
}

function levelOf(line) {
  if (/\b(ERROR|ERRO|level=error)\b/i.test(line)) return "error";
  if (/\b(WARN|WARNING|level=warn)\b/i.test(line)) return "warn";
  if (/\b(DEBUG|DEBU|level=debug)\b/i.test(line)) return "debug";
  return "info";
}
function pass(l) {
  if (level === "all") return true;
  const rank = { debug: 0, info: 1, warn: 2, error: 3 };
  const need = { error: 3, warn: 2, info: 1 }[level] ?? 0;
  return rank[levelOf(l)] >= need;
}

function paint() {
  const el = document.getElementById("log-feed");
  if (!el) return;
  const shown = lines.filter(pass);
  if (!shown.length) { el.innerHTML = `<div class="empty">No log lines.</div>`; return; }
  const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  el.innerHTML = shown.slice(-MAX).map((l) =>
    `<div class="log-line level-${levelOf(l)}">${escapeHtml(l)}</div>`).join("");
  if (!paused && atBottom) el.scrollTop = el.scrollHeight;
}
