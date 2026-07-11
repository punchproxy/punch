// Relays: inspect relay groups and individual relays, select the active
// path, and trigger health checks / subscription refreshes.

import { api } from "../api.js";
import { sparkline } from "../charts.js";
import { toast, toastError, confirmModal } from "../ui.js";
import { fmtLatency, statusColor, pill, timeAgo, escapeHtml, debounce } from "../util.js";

let timer = null;
let groups = [];
let relays = [];
let search = "";
let sortKey = "latency";
let busy = new Set();

export function mount(container) {
  container.innerHTML = `<div id="rl"></div>`;
  refresh(true);
  timer = setInterval(() => refresh(false), 3000);
}
export function unmount() { clearInterval(timer); timer = null; busy.clear(); }

async function refresh(showLoading) {
  const root = document.getElementById("rl");
  if (!root) return;
  if (showLoading) root.innerHTML = `<div class="empty">Loading relays…</div>`;
  try {
    [groups, relays] = await Promise.all([api.get("/relaygroups"), api.get("/relays")]);
    render();
  } catch (e) {
    if (showLoading) root.innerHTML = `<div class="empty">${escapeHtml(e.message)}</div>`;
  }
}

async function act(key, fn, okMsg) {
  if (busy.has(key)) return;
  busy.add(key);
  render();
  try {
    await fn();
    if (okMsg) toast(okMsg, { type: "ok" });
    await refresh(false);
  } catch (e) {
    toastError(e);
  } finally {
    busy.delete(key);
    render();
  }
}

function render() {
  const root = document.getElementById("rl");
  if (!root) return;
  const maxLat = Math.max(100, ...relays.map((r) => r.url_test_latency_ms || r.latency_ms || 0));
  const filtered = relays
    .filter((r) => !search || (r.name + r.group + (r.addr || "")).toLowerCase().includes(search))
    .sort(sortFn);

  root.innerHTML = `
    <div class="toolbar">
      <input class="search" id="rl-search" placeholder="Filter relays…" value="${escapeHtml(search)}" />
      <select id="rl-sort" style="max-width:170px;">
        <option value="latency" ${sortKey === "latency" ? "selected" : ""}>Sort: latency</option>
        <option value="name" ${sortKey === "name" ? "selected" : ""}>Sort: name</option>
        <option value="group" ${sortKey === "group" ? "selected" : ""}>Sort: group</option>
        <option value="status" ${sortKey === "status" ? "selected" : ""}>Sort: status</option>
      </select>
      <div class="spacer"></div>
      <button class="btn btn-sm" id="rl-check-all" ${busy.has("check-all") ? "disabled" : ""}>${busy.has("check-all") ? "Checking…" : "Check all"}</button>
      <button class="btn btn-sm" id="rl-refresh-all" ${busy.has("refresh-all") ? "disabled" : ""}>Refresh subscriptions</button>
    </div>

    <div class="section-title">Groups</div>
    <div class="grid cols-3">${groups.map(groupCard).join("") || `<div class="empty">No relay groups configured.</div>`}</div>

    <div class="section-title">Relays <span class="faint" style="text-transform:none;font-weight:400;">(${filtered.length})</span></div>
    <div class="card"><div class="table-wrap"><table class="data">
      <thead><tr>
        <th>Relay</th><th>Group</th><th>Type</th><th>Status</th>
        <th style="width:220px;">Latency</th><th>Trend</th><th>Checked</th><th></th>
      </tr></thead>
      <tbody>${filtered.map((r) => relayRow(r, maxLat)).join("") || `<tr><td colspan="8"><div class="empty">No relays.</div></td></tr>`}</tbody>
    </table></div></div>`;

  wire();
}

function groupCard(g) {
  const selectable = !g.selected && g.relay_count > 0 && g.type !== "direct";
  const checkKey = "gcheck-" + g.name, refreshKey = "grefresh-" + g.name, selKey = "gsel-" + g.name;
  return `<div class="card ${g.selected ? "" : ""}" style="${g.selected ? "border-color:var(--cf-orange);" : ""}">
    <div class="card-head">
      <div class="flex gap-sm"><h3>${escapeHtml(g.name)}</h3>${g.selected ? pill("active", "orange") : ""}</div>
      <span class="pill gray plain">${escapeHtml(g.type)}</span>
    </div>
    <div class="card-body tight">
      <div class="spread" style="margin-bottom:8px;">
        <span class="muted">Select mode</span>${pill(g.select, g.select === "auto" ? "blue" : "gray", true)}
      </div>
      <div class="spread" style="margin-bottom:8px;">
        <span class="muted">Relays</span><span class="mono">${g.relay_count}</span>
      </div>
      <div class="spread" style="margin-bottom:10px;">
        <span class="muted">Current</span>
        <span class="right"><span class="mono">${escapeHtml(shortName(g.current_relay, g.name) || "—")}</span>
        ${g.current_status ? " " + pill(g.current_status, statusColor(g.current_status)) : ""}
        <div class="faint mono" style="font-size:11px;">${fmtLatency(g.current_latency_ms)}</div></span>
      </div>
      ${g.error ? `<div class="faint" style="color:var(--red);font-size:11px;margin-bottom:8px;">${escapeHtml(g.error)}</div>` : ""}
      <div class="btn-row">
        ${selectable ? `<button class="btn btn-sm btn-primary" data-gsel="${escapeHtml(g.name)}" ${busy.has(selKey) ? "disabled" : ""}>Select</button>` : ""}
        <button class="btn btn-sm" data-gcheck="${escapeHtml(g.name)}" ${busy.has(checkKey) ? "disabled" : ""}>${busy.has(checkKey) ? "Checking…" : "Check"}</button>
        ${g.type === "remote" ? `<button class="btn btn-sm" data-grefresh="${escapeHtml(g.name)}" ${busy.has(refreshKey) ? "disabled" : ""}>Refresh</button>` : ""}
      </div>
    </div>
  </div>`;
}

function relayRow(r, maxLat) {
  const lat = r.url_test_latency_ms || r.latency_ms || 0;
  const color = statusColor(r.status);
  const w = Math.min(100, (lat / maxLat) * 100);
  const short = shortName(r.name, r.group);
  const hist = (r.history || []).map((h) => h.latency_ms || 0);
  const selKey = "sel-" + r.name, checkKey = "check-" + r.name;
  return `<tr class="${r.selected ? "selected-row" : ""}">
    <td><div class="flex gap-sm"><span class="mono">${escapeHtml(short)}</span>${r.selected ? pill("active", "orange") : ""}</div>
      <div class="faint mono" style="font-size:11px;">${escapeHtml(r.addr || "")}</div></td>
    <td class="muted">${escapeHtml(r.group)}</td>
    <td><span class="tag">${escapeHtml(r.type || "?")}</span></td>
    <td>${pill(r.status, color)}</td>
    <td><div class="latency-cell"><div class="meter ${color}"><span style="width:${w}%"></span></div>
      <span class="val">${fmtLatency(lat)}</span></div></td>
    <td style="width:110px;">${hist.length > 1 ? sparkline(hist, { color: "var(--text-faint)", width: 90, height: 24, fill: false }) : '<span class="faint">—</span>'}</td>
    <td class="faint nowrap">${timeAgo(r.last_checked_at)}</td>
    <td><div class="row-actions">
      ${r.selected ? "" : `<button class="btn btn-sm" data-sel="${escapeHtml(r.name)}" data-group="${escapeHtml(r.group)}" ${busy.has(selKey) ? "disabled" : ""}>Select</button>`}
      <button class="btn btn-sm btn-ghost" data-check="${escapeHtml(r.name)}" data-group="${escapeHtml(r.group)}" ${busy.has(checkKey) ? "disabled" : ""}>Check</button>
    </div></td>
  </tr>`;
}

function wire() {
  const s = document.getElementById("rl-search");
  s.oninput = debounce((e) => { search = e.target.value.trim().toLowerCase(); render(); }, 150);
  document.getElementById("rl-sort").onchange = (e) => { sortKey = e.target.value; render(); };
  document.getElementById("rl-check-all").onclick = () =>
    act("check-all", () => api.post("/relaygroups/check?all=true"), "Health check started for all groups");
  document.getElementById("rl-refresh-all").onclick = () =>
    act("refresh-all", () => api.post("/relaygroups/refresh?all=true"), "Subscriptions refreshed");

  document.querySelectorAll("[data-gsel]").forEach((b) => b.onclick = () =>
    act("gsel-" + b.dataset.gsel, () => api.post(`/relaygroups/${enc(b.dataset.gsel)}/select`), `Group ${b.dataset.gsel} selected`));
  document.querySelectorAll("[data-gcheck]").forEach((b) => b.onclick = () =>
    act("gcheck-" + b.dataset.gcheck, () => api.post(`/relaygroups/${enc(b.dataset.gcheck)}/check`), `Checking ${b.dataset.gcheck}`));
  document.querySelectorAll("[data-grefresh]").forEach((b) => b.onclick = () =>
    act("grefresh-" + b.dataset.grefresh, () => api.post(`/relaygroups/${enc(b.dataset.grefresh)}/refresh`), `Refreshed ${b.dataset.grefresh}`));

  document.querySelectorAll("[data-sel]").forEach((b) => b.onclick = () => {
    const short = shortName(b.dataset.sel, b.dataset.group);
    act("sel-" + b.dataset.sel, () => api.post(`/relays/${enc(short)}/select?group=${enc(b.dataset.group)}`), `Selected ${short}`);
  });
  document.querySelectorAll("[data-check]").forEach((b) => b.onclick = () => {
    const short = shortName(b.dataset.check, b.dataset.group);
    act("check-" + b.dataset.check, () => api.post(`/relays/${enc(short)}/check?group=${enc(b.dataset.group)}`), `Checking ${short}`);
  });
}

function sortFn(a, b) {
  if (sortKey === "name") return shortName(a.name, a.group).localeCompare(shortName(b.name, b.group));
  if (sortKey === "group") return (a.group || "").localeCompare(b.group || "");
  if (sortKey === "status") return (a.status || "").localeCompare(b.status || "");
  // latency: alive first, ascending; dead/zero last
  const la = a.url_test_latency_ms || a.latency_ms || 0;
  const lb = b.url_test_latency_ms || b.latency_ms || 0;
  const va = la > 0 ? la : Infinity, vb = lb > 0 ? lb : Infinity;
  return va - vb;
}

function shortName(name, group) {
  if (!name) return "";
  const prefix = group + " / ";
  return name.startsWith(prefix) ? name.slice(prefix.length) : name;
}
function enc(s) { return encodeURIComponent(s); }
