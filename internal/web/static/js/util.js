// Formatting and small DOM helpers shared across views.

export function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export function fmtRate(bps) {
  return `${fmtBytes(bps)}/s`;
}

export function fmtNum(n) {
  return (Number(n) || 0).toLocaleString();
}

export function fmtLatency(ms) {
  if (ms === undefined || ms === null || ms <= 0) return "—";
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)} s`;
  return `${ms} ms`;
}

export function fmtDuration(ms) {
  if (!ms || ms < 0) return "—";
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

export function fmtUptime(sec) {
  return fmtDuration((Number(sec) || 0) * 1000);
}

export function fmtTime(iso) {
  if (!iso || iso.startsWith("0001")) return "—";
  const d = new Date(iso);
  if (isNaN(d)) return "—";
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function timeAgo(iso) {
  if (!iso || iso.startsWith("0001")) return "never";
  const d = new Date(iso);
  if (isNaN(d)) return "never";
  const s = Math.floor((Date.now() - d.getTime()) / 1000);
  if (s < 2) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

// Map a relay/connectivity health status to a pill color class.
export function statusColor(status) {
  switch ((status || "").toLowerCase()) {
    case "healthy": case "alive": case "up": return "green";
    case "degraded": return "amber";
    case "down": case "dead": return "red";
    case "checking": case "pending": return "blue";
    default: return "gray";
  }
}

export function pill(text, color, plain = false) {
  return `<span class="pill ${color}${plain ? " plain" : ""}">${escapeHtml(text)}</span>`;
}

export function escapeHtml(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => (
    { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]
  ));
}

// Tiny hyperscript: el("div", {class:"x"}, ["text", el(...)])
export function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "class") node.className = v;
    else if (k === "html") node.innerHTML = v;
    else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined && v !== false) node.setAttribute(k, v);
  }
  for (const c of [].concat(children)) {
    if (c === null || c === undefined || c === false) continue;
    node.append(c.nodeType ? c : document.createTextNode(c));
  }
  return node;
}

export function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}
