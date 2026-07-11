// Sessions: live view of flows through the tunnel with per-flow byte counts,
// top-talkers, and termination controls.

import { api } from "../api.js";
import { barList } from "../charts.js";
import { toast, toastError, confirmModal } from "../ui.js";
import { fmtBytes, fmtNum, fmtDuration, statusColor, pill, timeAgo, escapeHtml, debounce } from "../util.js";

let timer = null;
let sessions = [];
let search = "";
let filter = "all";

export function mount(container) {
  container.innerHTML = `<div id="ss"></div>`;
  refresh(true);
  timer = setInterval(() => refresh(false), 2000);
}
export function unmount() { clearInterval(timer); timer = null; }

async function refresh(showLoading) {
  const root = document.getElementById("ss");
  if (!root) return;
  if (showLoading) root.innerHTML = `<div class="empty">Loading sessions…</div>`;
  try {
    sessions = await api.get("/sessions");
    render();
  } catch (e) {
    if (showLoading) root.innerHTML = `<div class="empty">${escapeHtml(e.message)}</div>`;
  }
}

function isActive(s) { return !s.closed_at || s.closed_at.startsWith("0001"); }

function render() {
  const root = document.getElementById("ss");
  if (!root) return;
  const activeCount = sessions.filter(isActive).length;
  const rows = sessions
    .filter((s) => filter === "all" || (filter === "active" ? isActive(s) : !isActive(s)))
    .filter((s) => !search || `${s.destination} ${s.relay} ${s.rule} ${s.process}`.toLowerCase().includes(search));

  const talkers = [...sessions]
    .map((s) => ({ label: shortDest(s.destination), value: (s.upload_bytes || 0) + (s.download_bytes || 0), color: isActive(s) ? "var(--cf-orange)" : "var(--text-faint)" }))
    .filter((t) => t.value > 0)
    .sort((a, b) => b.value - a.value)
    .slice(0, 6);

  root.innerHTML = `
    <div class="grid cols-4" style="margin-bottom:18px;">
      <div class="card pad stat"><span class="label">Active flows</span><span class="value">${fmtNum(activeCount)}</span></div>
      <div class="card pad stat"><span class="label">Total (history)</span><span class="value">${fmtNum(sessions.length)}</span></div>
      <div class="card pad stat"><span class="label">Upload (visible)</span><span class="value" style="font-size:20px;">${fmtBytes(sum(sessions, "upload_bytes"))}</span></div>
      <div class="card pad stat"><span class="label">Download (visible)</span><span class="value" style="font-size:20px;">${fmtBytes(sum(sessions, "download_bytes"))}</span></div>
    </div>

    ${talkers.length ? `<div class="card" style="margin-bottom:18px;"><div class="card-head"><h3>Top talkers</h3><span class="sub">by total bytes</span></div>
      <div class="card-body">${barList(talkers, { formatValue: fmtBytes })}</div></div>` : ""}

    <div class="toolbar">
      <input class="search" id="ss-search" placeholder="Filter by host, relay, rule, process…" value="${escapeHtml(search)}" />
      <select id="ss-filter" style="max-width:150px;">
        <option value="all" ${filter === "all" ? "selected" : ""}>All</option>
        <option value="active" ${filter === "active" ? "selected" : ""}>Active only</option>
        <option value="closed" ${filter === "closed" ? "selected" : ""}>Closed only</option>
      </select>
      <div class="spacer"></div>
      <button class="btn btn-sm btn-danger-ghost" id="ss-kill-all" ${activeCount ? "" : "disabled"}>Terminate all active</button>
    </div>

    <div class="card"><div class="table-wrap"><table class="data">
      <thead><tr>
        <th>Destination</th><th>Proto</th><th>Relay</th><th>Rule</th><th>Process</th>
        <th class="right">↑ Up</th><th class="right">↓ Down</th><th class="right">Duration</th><th>Status</th><th></th>
      </tr></thead>
      <tbody>${rows.map(row).join("") || `<tr><td colspan="10"><div class="empty">No sessions match.</div></td></tr>`}</tbody>
    </table></div></div>`;

  wire();
}

function row(s) {
  const active = isActive(s);
  return `<tr>
    <td><span class="mono">${escapeHtml(s.destination || s.dst_ip || "—")}</span>
      ${s.fake_ip ? `<div class="faint mono" style="font-size:11px;">fake ${escapeHtml(s.fake_ip)}</div>` : ""}</td>
    <td><span class="tag">${escapeHtml((s.protocol || "").split(":")[0] || "?")}</span></td>
    <td class="muted">${escapeHtml(shortDest(s.relay) || "direct")}</td>
    <td>${s.rule ? `<span class="tag">${escapeHtml(s.rule)}</span>` : '<span class="faint">—</span>'}</td>
    <td class="muted nowrap">${escapeHtml(s.process || "—")}</td>
    <td class="num">${fmtBytes(s.upload_bytes)}</td>
    <td class="num">${fmtBytes(s.download_bytes)}</td>
    <td class="num">${fmtDuration(s.duration_ms)}</td>
    <td>${active ? pill("active", "green") : pill("closed", "gray")}</td>
    <td><div class="row-actions">
      <button class="btn btn-sm btn-ghost" data-trace="${escapeHtml(s.id)}">Trace</button>
      ${active ? `<button class="btn btn-sm btn-danger-ghost" data-kill="${escapeHtml(s.id)}">Kill</button>` : ""}
    </div></td>
  </tr>`;
}

function wire() {
  document.getElementById("ss-search").oninput = debounce((e) => { search = e.target.value.trim().toLowerCase(); render(); }, 150);
  document.getElementById("ss-filter").onchange = (e) => { filter = e.target.value; render(); };
  document.getElementById("ss-kill-all").onclick = async () => {
    const ok = await confirmModal({ title: "Terminate all active sessions?", message: "Every active flow will be dropped immediately.", confirmLabel: "Terminate all", danger: true });
    if (!ok) return;
    try { const r = await api.del("/sessions?all=true"); toast(`Terminated ${r.terminated} session(s)`, { type: "ok" }); refresh(false); }
    catch (e) { toastError(e); }
  };
  document.querySelectorAll("[data-kill]").forEach((b) => b.onclick = async () => {
    try { await api.del(`/sessions/${encodeURIComponent(b.dataset.kill)}`); toast("Session terminated", { type: "ok" }); refresh(false); }
    catch (e) { toastError(e); }
  });
  document.querySelectorAll("[data-trace]").forEach((b) => b.onclick = () => showTrace(b.dataset.trace));
}

async function showTrace(id) {
  let s;
  try { s = await api.get(`/sessions/${encodeURIComponent(id)}`); }
  catch (e) { toastError(e); return; }
  const backdrop = document.createElement("div");
  backdrop.className = "modal-backdrop";
  const trace = (s.trace || []).map((t) =>
    `<div class="feed-row"><span class="t">+${t.offset_ms}ms</span><span class="d">${escapeHtml(t.message)}</span></div>`).join("")
    || `<div class="empty">No trace recorded.</div>`;
  backdrop.innerHTML = `<div class="modal" style="max-width:560px;">
    <div class="modal-head">Session ${escapeHtml(shortDest(s.destination) || s.id)}</div>
    <div class="modal-body">
      <dl class="kv">
        <dt>ID</dt><dd>${escapeHtml(s.id)}</dd>
        <dt>Destination</dt><dd>${escapeHtml(s.destination || "—")}</dd>
        <dt>Relay</dt><dd>${escapeHtml(s.relay || "direct")}</dd>
        <dt>Rule</dt><dd>${escapeHtml(s.rule || "—")}</dd>
        <dt>Protocol</dt><dd>${escapeHtml(s.protocol || "—")}</dd>
        <dt>Uploaded</dt><dd>${fmtBytes(s.upload_bytes)}</dd>
        <dt>Downloaded</dt><dd>${fmtBytes(s.download_bytes)}</dd>
        <dt>Duration</dt><dd>${fmtDuration(s.duration_ms)}</dd>
      </dl>
      <div class="section-title" style="margin:6px 0 4px;">Trace</div>
      <div class="feed" style="max-height:220px;">${trace}</div>
    </div>
    <div class="modal-foot"><button class="btn" data-close>Close</button></div>
  </div>`;
  document.body.appendChild(backdrop);
  const close = () => backdrop.remove();
  backdrop.querySelector("[data-close]").onclick = close;
  backdrop.onclick = (e) => { if (e.target === backdrop) close(); };
}

function sum(arr, key) { return arr.reduce((a, s) => a + (s[key] || 0), 0); }
function shortDest(name) {
  if (!name) return "";
  const parts = name.split(" / ");
  return parts.length > 1 ? parts[parts.length - 1] : name;
}
