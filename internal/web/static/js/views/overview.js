// Overview: the visual heart of the dashboard. Live throughput, DNS decision
// mix, connectivity health, and the active relay — refreshed from the shared
// status poller.

import { onStatus } from "../app.js";
import { sparkline, donut, gauge, legend } from "../charts.js";
import { areaChart } from "../charts.js";
import { fmtBytes, fmtRate, fmtNum, fmtLatency, fmtUptime, statusColor, pill, timeAgo, escapeHtml } from "../util.js";

const MAX_POINTS = 60;
let up = [];
let down = [];
let unsub = null;

const C = { relay: "var(--cf-orange)", direct: "var(--teal)", reject: "var(--red)", up: "var(--cf-orange)", down: "var(--blue)" };

export function mount(container) {
  up = []; down = [];
  container.innerHTML = `<div id="ov"></div>`;
  const root = document.getElementById("ov");
  unsub = onStatus((st) => render(root, st));
  root.innerHTML = `<div class="empty">Waiting for daemon status…</div>`;
}

export function unmount() { if (unsub) unsub(); unsub = null; }

function render(root, st) {
  const g = st.general || {};
  const r = st.relay || {};
  const d = st.dns || {};
  const conn = st.connectivity || {};

  up.push(Math.max(0, r.upload_bps || 0));
  down.push(Math.max(0, r.download_bps || 0));
  if (up.length > MAX_POINTS) up.shift();
  if (down.length > MAX_POINTS) down.shift();

  const totalDecisions = (d.relay?.requests || 0) + (d.direct?.requests || 0) + (d.reject?.requests || 0);
  const hitRatio = d.total_queries > 0 ? Math.round((d.cache_hits / d.total_queries) * 100) : 0;
  const activeRelayColor = statusColor(r.status);
  const isDirect = !r.active_relay || r.active_relay === "DIRECT";

  root.innerHTML = `
    <div class="grid cols-4">
      ${statTile("Active relay", isDirect ? "DIRECT" : shortRelay(r.active_relay),
        isDirect ? pill("direct", "gray") : pill(r.status || "unknown", activeRelayColor), true)}
      ${statTile("Live throughput", `${fmtRate(r.download_bps)} <small>↓</small>`,
        `<span class="faint">${fmtRate(r.upload_bps)} ↑</span>`)}
      ${statTile("Active sessions", fmtNum(r.active_sessions),
        `<span class="faint">${fmtNum(r.total_processed_sessions)} total</span>`)}
      ${statTile("DNS queries", fmtNum(d.total_queries),
        `<span class="faint">${hitRatio}% cache hits</span>`)}
    </div>

    <div class="grid cols-3 mt" style="grid-template-columns: 2fr 1fr;">
      <div class="card">
        <div class="card-head"><h3>Throughput</h3>
          <span class="sub">${fmtRate(r.download_bps)} down · ${fmtRate(r.upload_bps)} up</span></div>
        <div class="card-body">
          ${areaChart([{ values: down, color: C.down }, { values: up, color: C.up }], { height: 190, formatY: (v) => fmtBytes(v) })}
          ${legend([{ label: "Download", color: C.down }, { label: "Upload", color: C.up }])}
          <div class="faint" style="font-size:11px;margin-top:6px;">last ${up.length * 2}s · updates every 2s</div>
        </div>
      </div>
      <div class="card">
        <div class="card-head"><h3>DNS decisions</h3></div>
        <div class="card-body">
          <div class="donut-wrap">
            ${donut([
              { label: "relay", value: d.relay?.requests || 0, color: C.relay },
              { label: "direct", value: d.direct?.requests || 0, color: C.direct },
              { label: "reject", value: d.reject?.requests || 0, color: C.reject },
            ], { centerLabel: fmtNum(totalDecisions), centerSub: "routed" })}
            <div style="flex:1">
              ${decisionRow("Relay", d.relay, C.relay, totalDecisions)}
              ${decisionRow("Direct", d.direct, C.direct, totalDecisions)}
              ${decisionRow("Reject", d.reject, C.reject, totalDecisions)}
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="grid cols-3 mt">
      <div class="card"><div class="card-head"><h3>Connectivity</h3>
        <span class="sub">every ${Math.round((conn.check_interval_ms || 0) / 1000)}s</span></div>
        <div class="card-body">
          ${connBlock("Internet (direct)", conn.domestic)}
          <div style="height:1px;background:var(--border);margin:14px 0;"></div>
          ${connBlock("Relayed (outside)", conn.outside)}
        </div>
      </div>

      <div class="card"><div class="card-head"><h3>Selected relay latency</h3></div>
        <div class="card-body" style="display:flex;flex-direction:column;align-items:center;justify-content:center;">
          ${gauge(Math.min(r.url_test_latency_ms || r.latency_ms || 0, 500), {
            max: 500, label: fmtLatency(r.url_test_latency_ms || r.latency_ms),
            sub: isDirect ? "direct routing" : shortRelay(r.active_relay),
            color: activeRelayColor === "green" ? "var(--green)" : activeRelayColor === "amber" ? "var(--amber)" : activeRelayColor === "red" ? "var(--red)" : "var(--cf-orange)",
          })}
          <div class="flex gap-sm" style="margin-top:6px;">
            <span class="tag">TCP ${fmtLatency(r.tcp_connect_latency_ms)}</span>
            <span class="tag">checked ${timeAgo(r.last_checked_at)}</span>
          </div>
        </div>
      </div>

      <div class="card"><div class="card-head"><h3>Daemon</h3></div>
        <div class="card-body">
          <dl class="kv">
            <dt>Version</dt><dd>${escapeHtml(g.version || "—")}</dd>
            <dt>Platform</dt><dd>${escapeHtml(g.architecture || "—")}</dd>
            <dt>Uptime</dt><dd>${fmtUptime(g.uptime_seconds)}</dd>
            <dt>Memory</dt><dd>${fmtBytes(g.memory_bytes)}</dd>
            <dt>Goroutines</dt><dd>${fmtNum(g.goroutines)}</dd>
            <dt>Cache entries</dt><dd>${fmtNum(d.cache_entries)}</dd>
            <dt>UDP drops</dt><dd>${fmtNum((r.udp?.packets_dropped) || 0)}</dd>
          </dl>
        </div>
      </div>
    </div>`;
}

function statTile(label, value, extra, isRelay = false) {
  return `<div class="card pad stat">
    <span class="label">${label}</span>
    <span class="value" ${isRelay ? 'style="font-size:18px;word-break:break-all;"' : ""}>${value}</span>
    <span class="delta">${extra || ""}</span>
  </div>`;
}

function decisionRow(label, stat, color, total) {
  const v = stat?.requests || 0;
  const pct = total > 0 ? Math.round((v / total) * 100) : 0;
  return `<div style="margin:6px 0;">
    <div class="spread" style="font-size:12px;margin-bottom:3px;">
      <span><span class="swatch" style="display:inline-block;width:8px;height:8px;border-radius:2px;background:${color};margin-right:6px;"></span>${label}</span>
      <span class="mono muted">${fmtNum(v)} · ${pct}%</span>
    </div>
    <div class="meter" style="height:5px;"><span style="width:${pct}%;background:${color};"></span></div>
    ${stat?.last_domain ? `<div class="faint mono" style="font-size:10.5px;margin-top:2px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${escapeHtml(stat.last_domain)}</div>` : ""}
  </div>`;
}

function connBlock(title, c) {
  c = c || {};
  const color = statusColor(c.status);
  const hist = (c.history || []).map((h) => h.latency_ms || 0);
  return `<div>
    <div class="spread" style="margin-bottom:6px;">
      <strong style="font-size:13px;">${title}</strong>
      ${pill(c.status || "unknown", color)}
    </div>
    <div class="flex" style="gap:14px;">
      <div style="flex:1;">${sparkline(hist, { color: color === "green" ? "var(--green)" : color === "red" ? "var(--red)" : "var(--amber)", height: 34 })}</div>
      <div class="right"><div class="mono" style="font-size:15px;">${fmtLatency(c.latency_ms)}</div>
      <div class="faint" style="font-size:11px;">${timeAgo(c.last_checked_at)}</div></div>
    </div>
    ${c.error ? `<div class="faint mono" style="font-size:11px;color:var(--red);margin-top:4px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${escapeHtml(c.error)}">${escapeHtml(c.error)}</div>` : ""}
  </div>`;
}

function shortRelay(name) {
  if (!name) return "—";
  const parts = name.split(" / ");
  return parts.length > 1 ? parts[parts.length - 1] : name;
}
