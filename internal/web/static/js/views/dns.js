// DNS: live routing decisions plus full management of rules, routes,
// upstreams, cache, and fake-IP allocations — parity with `punchctl dns`.

import { api, stream } from "../api.js";
import { toast, toastError, formModal, confirmModal } from "../ui.js";
import { fmtLatency, fmtNum, pill, timeAgo, escapeHtml, debounce } from "../util.js";

const TABS = [
  ["live", "Live queries"], ["rules", "Domain rules"], ["routes", "CIDR routes"],
  ["upstreams", "Upstreams"], ["cache", "Cache"], ["fakeips", "Fake IPs"],
];

let tab = "live";
let ctrl = null;      // active stream controller
let timer = null;     // active poll timer
let feed = [];        // live query buffer
const decisionClass = (d) => ({ relay: "relay", direct: "direct", reject: "reject" }[String(d).toLowerCase()] || "");

export function mount(container) {
  container.innerHTML = `
    <div class="toolbar" style="margin-bottom:0;">
      <div class="btn-row" id="dns-tabs">${TABS.map(([id, label]) =>
        `<button class="btn btn-sm ${id === tab ? "btn-primary" : "btn-ghost"}" data-tab="${id}">${label}</button>`).join("")}</div>
    </div>
    <div id="dns-body" class="mt"></div>`;
  document.querySelectorAll("[data-tab]").forEach((b) => b.onclick = () => switchTab(b.dataset.tab));
  switchTab(tab);
}

export function unmount() { teardown(); }
function teardown() { if (ctrl) { ctrl.abort(); ctrl = null; } clearInterval(timer); timer = null; }

function switchTab(next) {
  teardown();
  tab = next;
  document.querySelectorAll("[data-tab]").forEach((b) =>
    b.className = "btn btn-sm " + (b.dataset.tab === tab ? "btn-primary" : "btn-ghost"));
  const body = document.getElementById("dns-body");
  body.innerHTML = "";
  if (tab === "live") return mountLive(body);
  if (tab === "rules") return mountRules(body, "rules");
  if (tab === "routes") return mountRules(body, "routes");
  if (tab === "upstreams") return mountUpstreams(body);
  if (tab === "cache") return mountCache(body);
  if (tab === "fakeips") return mountFakeIPs(body);
}

// ---------- Live query stream ----------
function mountLive(body) {
  feed = [];
  body.innerHTML = `<div class="card">
    <div class="card-head"><h3>Live DNS decisions</h3>
      <div class="flex gap-sm"><span class="sub" id="live-count">streaming…</span>
      <button class="btn btn-sm btn-ghost" id="live-clear">Clear</button></div></div>
    <div class="feed" id="live-feed"><div class="empty">Waiting for queries…</div></div></div>`;
  document.getElementById("live-clear").onclick = () => { feed = []; paintLive(); };
  ctrl = stream("/dns/queries/stream", (ql) => {
    feed.unshift(ql);
    if (feed.length > 300) feed.pop();
    paintLive();
  }, { parseJSON: true, onError: (e) => toastError(e, "Stream error") });
}
function paintLive() {
  const el = document.getElementById("live-feed");
  if (!el) return;
  document.getElementById("live-count").textContent = `${feed.length} shown`;
  if (!feed.length) { el.innerHTML = `<div class="empty">Waiting for queries…</div>`; return; }
  el.innerHTML = feed.map((q) => `<div class="feed-row">
    <span class="t">${new Date(q.time).toLocaleTimeString()}</span>
    <span class="decision ${decisionClass(q.decision)}">${escapeHtml(q.decision)}</span>
    <span class="d" title="${escapeHtml(q.domain)}">${escapeHtml(q.domain)} <span class="faint">${escapeHtml(q.qtype || "")}</span></span>
    <span class="meta">${q.cached ? '<span class="tag">cache</span> ' : ""}${q.rule ? `<span class="tag">${escapeHtml(q.rule)}</span> ` : ""}${q.latency_ms ? fmtLatency(q.latency_ms) : ""}</span>
  </div>`).join("");
}

// ---------- Rules / routes ----------
function mountRules(body, kind) {
  const path = kind === "rules" ? "/dns/rules" : "/dns/routes";
  const decisions = kind === "rules" ? ["relay", "direct", "reject"] : ["direct", "reject"];
  let rows = [];
  let search = "";

  const load = async () => {
    try { rows = await api.get(path); paint(); }
    catch (e) { body.innerHTML = `<div class="empty">${escapeHtml(e.message)}</div>`; }
  };
  const doAct = async (fn, msg) => { try { await fn(); toast(msg, { type: "ok" }); await load(); } catch (e) { toastError(e); } };

  function paint() {
    const filtered = rows.filter((r) => !search || (r.source || "").toLowerCase().includes(search));
    body.innerHTML = `
      <div class="toolbar">
        <input class="search" id="r-search" placeholder="Filter…" value="${escapeHtml(search)}" />
        <div class="spacer"></div>
        <button class="btn btn-sm" id="r-refresh">Refresh remote lists</button>
        <button class="btn btn-sm btn-primary" id="r-add">Add ${kind === "rules" ? "rule" : "route"}</button>
      </div>
      <div class="card"><div class="table-wrap"><table class="data">
        <thead><tr><th style="width:40px;">#</th><th>Decision</th><th>Source</th><th>Type</th>
          <th class="right">Entries</th><th class="right">Hits</th><th>Updated</th><th></th></tr></thead>
        <tbody>${filtered.map((r) => ruleRow(r)).join("") || `<tr><td colspan="8"><div class="empty">No entries.</div></td></tr>`}</tbody>
      </table></div></div>`;
    document.getElementById("r-search").oninput = debounce((e) => { search = e.target.value.trim().toLowerCase(); paint(); }, 150);
    document.getElementById("r-refresh").onclick = () => doAct(() => api.post(`${path}/refresh?all=true`), "Remote lists refreshed");
    document.getElementById("r-add").onclick = async () => {
      const v = await formModal({ title: `Add ${kind === "rules" ? "domain rule" : "CIDR route"}`, submitLabel: "Add", fields: [
        { name: "decision", label: "Decision", type: "select", options: decisions, value: decisions[0] },
        { name: "source", label: kind === "rules" ? "Domain / suffix / URL / file" : "CIDR / URL / file", placeholder: kind === "rules" ? "example.com or +.example.com" : "10.0.0.0/8" },
      ]});
      if (!v || !v.source) return;
      doAct(() => api.post(path, { decision: v.decision, source: v.source }), "Added");
    };
    body.querySelectorAll("[data-del]").forEach((b) => b.onclick = async () => {
      const ok = await confirmModal({ title: "Delete entry?", message: b.dataset.src, confirmLabel: "Delete", danger: true });
      if (ok) doAct(() => api.del(`${path}?index=${b.dataset.del}`), "Deleted");
    });
    body.querySelectorAll("[data-rr]").forEach((b) => b.onclick = () =>
      doAct(() => api.post(`${path}/refresh?index=${b.dataset.rr}`), "Refreshed"));
    body.querySelectorAll("[data-move]").forEach((b) => b.onclick = () => {
      const [from, to] = b.dataset.move.split(":").map(Number);
      doAct(() => api.post(`${path}/move?index=${from}`, { index: to }), "Reordered");
    });
  }

  function ruleRow(r) {
    const color = { relay: "orange", direct: "teal", reject: "red" }[r.decision] || "gray";
    const remote = /^https?:\/\//.test(r.source || "");
    return `<tr>
      <td class="faint mono">${r.index}</td>
      <td>${pill(r.decision, color === "teal" ? "blue" : color)}</td>
      <td class="mono" style="max-width:360px;overflow:hidden;text-overflow:ellipsis;">${escapeHtml(r.source)}${r.default ? ' <span class="tag">default</span>' : ""}</td>
      <td><span class="tag">${escapeHtml(r.type || "")}</span></td>
      <td class="num">${r.count ? fmtNum(r.count) : "—"}</td>
      <td class="num">${r.hits ? fmtNum(r.hits) : "—"}</td>
      <td class="faint nowrap">${r.last_updated ? timeAgo(r.last_updated) : "—"}</td>
      <td><div class="row-actions">
        ${r.default ? "" : `${r.index > 0 ? `<button class="btn btn-sm btn-ghost" data-move="${r.index}:${r.index - 1}" title="Move up">↑</button>` : ""}
        ${remote ? `<button class="btn btn-sm btn-ghost" data-rr="${r.index}">Refresh</button>` : ""}
        <button class="btn btn-sm btn-danger-ghost" data-del="${r.index}" data-src="${escapeHtml(r.source)}">Delete</button>`}
      </div></td>
    </tr>`;
  }

  load();
}

// ---------- Upstreams ----------
function mountUpstreams(body) {
  let rows = [];
  const load = async () => { try { rows = await api.get("/dns/upstreams"); paint(); } catch (e) { body.innerHTML = `<div class="empty">${escapeHtml(e.message)}</div>`; } };
  const doAct = async (fn, msg) => { try { await fn(); toast(msg, { type: "ok" }); await load(); } catch (e) { toastError(e); } };

  function paint() {
    body.innerHTML = `
      <div class="toolbar"><div class="spacer"></div>
        <button class="btn btn-sm btn-primary" id="u-add">Add upstream</button></div>
      <div class="card"><div class="table-wrap"><table class="data">
        <thead><tr><th>URL</th><th>Bootstrap</th><th>Domains</th><th class="right">Queries</th>
          <th class="right">Avg latency</th><th class="right">Last</th><th></th></tr></thead>
        <tbody>${rows.map(uRow).join("") || `<tr><td colspan="7"><div class="empty">No upstreams.</div></td></tr>`}</tbody>
      </table></div></div>`;
    document.getElementById("u-add").onclick = () => editUpstream(null);
    body.querySelectorAll("[data-edit]").forEach((b) => b.onclick = () => editUpstream(rows[Number(b.dataset.edit)]));
    body.querySelectorAll("[data-del]").forEach((b) => b.onclick = async () => {
      const ok = await confirmModal({ title: "Delete upstream?", message: b.dataset.url, confirmLabel: "Delete", danger: true });
      if (ok) doAct(() => api.del(`/dns/upstreams?url=${encodeURIComponent(b.dataset.url)}`), "Deleted");
    });
  }
  function uRow(u, i) {
    return `<tr>
      <td class="mono">${escapeHtml(u.url)}</td>
      <td class="mono faint">${escapeHtml(u.bootstrap || "—")}</td>
      <td>${(u.domains || []).map((d) => `<span class="tag">${escapeHtml(d)}</span>`).join(" ") || '<span class="faint">all</span>'}</td>
      <td class="num">${fmtNum(u.queries)}</td>
      <td class="num">${fmtLatency(u.average_latency_ms)}</td>
      <td class="faint nowrap">${u.last_queried_at ? timeAgo(u.last_queried_at) : "—"}</td>
      <td><div class="row-actions"><button class="btn btn-sm btn-ghost" data-edit="${i}">Edit</button>
        <button class="btn btn-sm btn-danger-ghost" data-del="${escapeHtml(u.url)}" data-url="${escapeHtml(u.url)}">Delete</button></div></td>
    </tr>`;
  }
  async function editUpstream(existing) {
    const v = await formModal({ title: existing ? "Edit upstream" : "Add upstream", submitLabel: "Save", fields: [
      { name: "url", label: "URL", value: existing?.url || "", placeholder: "https://dns.example/dns-query" },
      { name: "bootstrap", label: "Bootstrap IP (optional)", value: existing?.bootstrap || "", placeholder: "223.5.5.5" },
      { name: "domains", label: "Scoped domains (comma-separated, optional)", value: (existing?.domains || []).join(", ") },
    ]});
    if (!v || !v.url) return;
    const payload = { url: v.url, bootstrap: v.bootstrap, domains: v.domains ? v.domains.split(",").map((s) => s.trim()).filter(Boolean) : [] };
    doAct(() => existing ? api.put("/dns/upstreams", payload) : api.post("/dns/upstreams", payload), "Saved");
  }
  load();
}

// ---------- Cache ----------
function mountCache(body) {
  let rows = [];
  let search = "";
  const load = async () => { try { rows = await api.get("/dns/cache"); paint(); } catch (e) { body.innerHTML = `<div class="empty">${escapeHtml(e.message)}</div>`; } };
  function paint() {
    const filtered = rows.filter((r) => !search || `${r.name} ${r.result}`.toLowerCase().includes(search));
    body.innerHTML = `
      <div class="toolbar">
        <input class="search" id="c-search" placeholder="Filter…" value="${escapeHtml(search)}" />
        <span class="muted">${fmtNum(rows.length)} entries</span>
        <div class="spacer"></div>
        <button class="btn btn-sm btn-danger-ghost" id="c-flush">Flush cache</button>
      </div>
      <div class="card"><div class="table-wrap"><table class="data">
        <thead><tr><th>Name</th><th>Type</th><th>Result</th><th>Upstream</th><th>State</th><th>Expires</th></tr></thead>
        <tbody>${filtered.slice(0, 500).map((c) => `<tr>
          <td class="mono">${escapeHtml(c.name)}</td><td><span class="tag">${escapeHtml(c.qtype)}</span></td>
          <td class="mono" style="max-width:280px;overflow:hidden;text-overflow:ellipsis;">${escapeHtml(c.result)}</td>
          <td class="faint mono">${escapeHtml(c.upstream || "—")}</td>
          <td>${pill(c.state, c.state === "fresh" ? "green" : c.state === "stale" ? "amber" : "gray")}</td>
          <td class="faint nowrap">${timeAgo(c.expires_at)}</td></tr>`).join("") || `<tr><td colspan="6"><div class="empty">Cache is empty.</div></td></tr>`}</tbody>
      </table></div></div>`;
    document.getElementById("c-search").oninput = debounce((e) => { search = e.target.value.trim().toLowerCase(); paint(); }, 150);
    document.getElementById("c-flush").onclick = async () => {
      const ok = await confirmModal({ title: "Flush DNS cache?", message: "All cached answers will be dropped.", confirmLabel: "Flush", danger: true });
      if (!ok) return;
      try { await api.del("/dns/cache"); toast("Cache flushed", { type: "ok" }); load(); } catch (e) { toastError(e); }
    };
  }
  load();
  timer = setInterval(load, 5000);
}

// ---------- Fake IPs ----------
function mountFakeIPs(body) {
  let rows = [];
  let search = "";
  const load = async () => { try { rows = await api.get("/dns/fakeips"); paint(); } catch (e) { body.innerHTML = `<div class="empty">${escapeHtml(e.message)}</div>`; } };
  function paint() {
    const filtered = rows.filter((r) => !search || `${r.domain} ${r.fake_ip}`.toLowerCase().includes(search));
    body.innerHTML = `
      <div class="toolbar">
        <input class="search" id="f-search" placeholder="Filter…" value="${escapeHtml(search)}" />
        <span class="muted">${fmtNum(rows.length)} mappings · ${fmtNum(rows.filter((r) => r.state === "active").length)} active</span>
      </div>
      <div class="card"><div class="table-wrap"><table class="data">
        <thead><tr><th>Fake IP</th><th>Domain</th><th>State</th><th class="right">Sessions</th><th>Expires</th></tr></thead>
        <tbody>${filtered.slice(0, 500).map((f) => `<tr>
          <td class="mono">${escapeHtml(f.fake_ip)}</td><td class="mono">${escapeHtml(f.domain)}</td>
          <td>${pill(f.state, f.state === "active" ? "green" : "gray")}</td>
          <td class="num">${(f.session_ids || []).length}</td>
          <td class="faint nowrap">${timeAgo(f.expires_at)}</td></tr>`).join("") || `<tr><td colspan="5"><div class="empty">No fake IPs allocated.</div></td></tr>`}</tbody>
      </table></div></div>`;
    document.getElementById("f-search").oninput = debounce((e) => { search = e.target.value.trim().toLowerCase(); paint(); }, 150);
  }
  load();
  timer = setInterval(load, 5000);
}
