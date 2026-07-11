// Lightweight, dependency-free SVG charts. Each function returns an SVG
// string sized to fill its container via viewBox, so they scale responsively.

const NS = "http://www.w3.org/2000/svg";

// Sparkline: small trend line. values are numbers (nulls become gaps).
export function sparkline(values, { color = "var(--cf-orange)", width = 120, height = 32, fill = true } = {}) {
  const pts = (values || []).filter((v) => v !== null && v !== undefined && !isNaN(v));
  if (pts.length < 2) return `<svg class="sparkline" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" width="${width}" height="${height}"></svg>`;
  const max = Math.max(...pts, 1);
  const min = Math.min(...pts, 0);
  const range = max - min || 1;
  const step = width / (pts.length - 1);
  const coords = pts.map((v, i) => [i * step, height - 3 - ((v - min) / range) * (height - 6)]);
  const line = coords.map((c, i) => `${i ? "L" : "M"}${c[0].toFixed(1)},${c[1].toFixed(1)}`).join(" ");
  const area = fill ? `<path d="${line} L${width},${height} L0,${height} Z" fill="${color}" opacity="0.12"/>` : "";
  return `<svg class="sparkline" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" width="100%" height="${height}">
    ${area}<path d="${line}" fill="none" stroke="${color}" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"/>
  </svg>`;
}

// Donut chart. segments: [{label, value, color}]. Renders center total.
export function donut(segments, { size = 132, thickness = 18, centerLabel = "", centerSub = "" } = {}) {
  const total = segments.reduce((a, s) => a + (s.value || 0), 0);
  const r = (size - thickness) / 2;
  const cx = size / 2, cy = size / 2;
  const circ = 2 * Math.PI * r;
  let offset = 0;
  let arcs = "";
  if (total > 0) {
    for (const s of segments) {
      const frac = (s.value || 0) / total;
      if (frac <= 0) continue;
      const len = frac * circ;
      arcs += `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="${s.color}" stroke-width="${thickness}"
        stroke-dasharray="${len.toFixed(2)} ${(circ - len).toFixed(2)}" stroke-dashoffset="${(-offset).toFixed(2)}"
        transform="rotate(-90 ${cx} ${cy})" stroke-linecap="butt"/>`;
      offset += len;
    }
  } else {
    arcs = `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="var(--hover)" stroke-width="${thickness}"/>`;
  }
  const label = centerLabel !== "" ? centerLabel : String(total);
  return `<svg class="chart-svg" viewBox="0 0 ${size} ${size}" width="${size}" height="${size}">
    <circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="var(--hover)" stroke-width="${thickness}"/>
    ${arcs}
    <g class="donut-center"><text x="${cx}" y="${cy - 1}" class="big">${label}</text>
    <text x="${cx}" y="${cy + 15}" class="small">${centerSub}</text></g>
  </svg>`;
}

// Semicircular gauge for a bounded value (e.g. latency or a ratio).
export function gauge(value, { max = 100, size = 150, label = "", sub = "", color = "var(--cf-orange)", invert = false } = {}) {
  const w = size, h = size * 0.62;
  const cx = w / 2, cy = h - 4, r = w / 2 - 12, thickness = 12;
  const frac = Math.max(0, Math.min(1, (value || 0) / max));
  const arc = (f) => {
    const a = Math.PI * (1 - f);
    return [cx + r * Math.cos(a), cy - r * Math.sin(a)];
  };
  const [sx, sy] = arc(0), [ex, ey] = arc(1), [vx, vy] = arc(frac);
  const large = frac > 0.5 ? 1 : 0;
  return `<svg class="chart-svg" viewBox="0 0 ${w} ${h + 24}" width="${size}" height="${(h + 24)}">
    <path d="M${sx},${sy} A${r},${r} 0 0 1 ${ex},${ey}" fill="none" stroke="var(--hover)" stroke-width="${thickness}" stroke-linecap="round"/>
    <path d="M${sx},${sy} A${r},${r} 0 ${large} 1 ${vx},${vy}" fill="none" stroke="${color}" stroke-width="${thickness}" stroke-linecap="round"/>
    <text x="${cx}" y="${cy - 4}" text-anchor="middle" class="gauge-label" font-size="20">${label}</text>
    <text x="${cx}" y="${h + 16}" text-anchor="middle" class="gauge-sub">${sub}</text>
  </svg>`;
}

// Dual-series realtime area chart (upload/download). series is
// [{values:[], color}]. values share the x-axis (time). Renders grid + axis.
export function areaChart(series, { width = 640, height = 180, formatY = (v) => v } = {}) {
  const n = Math.max(...series.map((s) => s.values.length), 0);
  const allVals = series.flatMap((s) => s.values);
  const max = Math.max(...allVals, 1) * 1.15;
  const padL = 52, padB = 4, padT = 8, padR = 6;
  const iw = width - padL - padR, ih = height - padT - padB;
  const x = (i) => padL + (n <= 1 ? iw : (i / (n - 1)) * iw);
  const y = (v) => padT + ih - (v / max) * ih;
  let grid = "";
  const ticks = 4;
  for (let t = 0; t <= ticks; t++) {
    const gv = (max / ticks) * t;
    const gy = y(gv);
    grid += `<line x1="${padL}" y1="${gy}" x2="${width - padR}" y2="${gy}" stroke="var(--grid-line)" stroke-width="1"/>
      <text x="${padL - 8}" y="${gy + 3}" text-anchor="end" font-size="10" fill="var(--text-faint)">${formatY(gv)}</text>`;
  }
  let paths = "";
  for (const s of series) {
    if (s.values.length < 2) continue;
    const line = s.values.map((v, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
    paths += `<path d="${line} L${x(s.values.length - 1).toFixed(1)},${y(0)} L${x(0).toFixed(1)},${y(0)} Z" fill="${s.color}" opacity="0.10"/>
      <path d="${line}" fill="none" stroke="${s.color}" stroke-width="1.8" stroke-linejoin="round"/>`;
  }
  return `<svg class="chart-svg" viewBox="0 0 ${width} ${height}" width="100%" height="${height}" preserveAspectRatio="none">${grid}${paths}</svg>`;
}

// Horizontal bar list. items: [{label, value, color, sub}]
export function barList(items, { formatValue = (v) => v } = {}) {
  const max = Math.max(...items.map((i) => i.value), 1);
  return items.map((it) => `
    <div class="flex" style="gap:12px;margin:8px 0;">
      <div class="mono" style="width:150px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px;" title="${it.label}">${it.label}</div>
      <div class="meter" style="flex:1;height:10px;"><span style="width:${(it.value / max) * 100}%;background:${it.color || "var(--cf-orange)"};"></span></div>
      <div class="mono faint" style="width:70px;text-align:right;font-size:12px;">${formatValue(it.value)}</div>
    </div>`).join("");
}

export function legend(items) {
  return `<div class="legend">${items.map((i) =>
    `<span class="item"><span class="swatch" style="background:${i.color}"></span>${i.label}${i.value !== undefined ? ` <strong style="color:var(--text)">${i.value}</strong>` : ""}</span>`
  ).join("")}</div>`;
}
