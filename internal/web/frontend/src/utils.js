export function fmtBytes(value) {
  let n = Number(value) || 0;
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export const fmtRate = (value) => `${fmtBytes(value)}/s`;
export const fmtNum = (value) => (Number(value) || 0).toLocaleString();

export function fmtLatency(ms) {
  if (ms === undefined || ms === null || ms <= 0) return "—";
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms} ms`;
}

export function fmtDuration(ms) {
  if (!ms || ms < 0) return "—";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}

export const fmtUptime = (seconds) => fmtDuration((Number(seconds) || 0) * 1000);
export const connectLatencyWindowMS = 10 * 60 * 1000;

export function filterConnectLatencySamples(samples, now = Date.now()) {
  const cutoff = now - connectLatencyWindowMS;
  return (samples || []).filter((sample) => {
    const at = new Date(sample.at).getTime();
    return Number(sample.ms) > 1 && at >= cutoff && at <= now;
  });
}

export function fmtTime(iso) {
  if (!iso || iso.startsWith?.("0001")) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function timeAgo(iso) {
  if (!iso || iso.startsWith?.("0001")) return "never";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "never";
  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (seconds < 2) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function statusColor(status) {
  switch ((status || "").toLowerCase()) {
    case "healthy": case "alive": case "up": return "green";
    case "degraded": return "amber";
    case "down": case "dead": return "red";
    case "checking": case "pending": return "blue";
    default: return "gray";
  }
}

export const cacheStateColor = (state) => state === "live" ? "green" : state === "stale" ? "amber" : "gray";
export const isSessionActive = (session) => !session.closed_at || session.closed_at.startsWith("0001");

// Strips the port from a source address: "1.2.3.4:5678" → "1.2.3.4", "[::1]:5678" → "::1".
export function clientIP(source) {
  if (!source) return "";
  const v6 = source.match(/^\[(.+)\]:\d+$/);
  if (v6) return v6[1];
  const idx = source.lastIndexOf(":");
  if (idx > 0 && source.indexOf(":") === idx) return source.slice(0, idx);
  return source;
}

export function filterSessions(sessions, filter, search) {
  const needle = search.trim().toLowerCase();
  return sessions
    .filter((session) => filter === "all" || (filter === "active" ? isSessionActive(session) : !isSessionActive(session)))
    .filter((session) => !needle || `${session.destination || ""} ${session.source || ""} ${session.relay || ""}`.toLowerCase().includes(needle));
}

export const sumBy = (rows, key) => rows.reduce((total, row) => total + (Number(row[key]) || 0), 0);
export function shortName(name, group = "") {
  if (!name) return "";
  const prefix = group ? `${group} / ` : "";
  if (prefix && name.startsWith(prefix)) return name.slice(prefix.length);
  const parts = name.split(" / ");
  return parts.length > 1 ? parts.at(-1) : name;
}
