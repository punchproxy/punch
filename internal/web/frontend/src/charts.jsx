import { useLayoutEffect, useRef } from "react";
import * as d3 from "d3";
import { gaugeGeometry } from "./math.js";

const css = (name) => `var(${name})`;

function useChart(draw, dependencies) {
  const containerRef = useRef(null), svgRef = useRef(null), tooltipRef = useRef(null);
  useLayoutEffect(() => {
    const container = containerRef.current, svg = svgRef.current;
    if (!container || !svg) return undefined;
    const track = (event) => { container._chartPointer = { x: event.clientX, y: event.clientY }; };
    const clear = () => { container._chartPointer = null; hideTooltip(tooltipRef.current); };
    container.addEventListener("pointermove", track);
    container.addEventListener("pointerleave", clear);
    container.addEventListener("pointercancel", clear);
    const render = () => {
      hideTooltip(tooltipRef.current);
      draw(d3.select(svg), Math.max(1, container.clientWidth), tooltipRef.current, container);
    };
    render();
    const observer = new ResizeObserver(render);
    observer.observe(container);
    return () => {
      observer.disconnect();
      container.removeEventListener("pointermove", track);
      container.removeEventListener("pointerleave", clear);
      container.removeEventListener("pointercancel", clear);
    };
  }, dependencies);
  return { containerRef, svgRef, tooltipRef };
}

function tooltipContent(tooltip, title, lines) {
  tooltip.replaceChildren();
  const heading = document.createElement("strong");
  heading.textContent = title;
  tooltip.append(heading);
  for (const line of lines) {
    const row = document.createElement("span");
    row.textContent = line;
    tooltip.append(row);
  }
}

function showTooltipAtMark(tooltip, mark, title, lines) {
  if (!tooltip || !mark) return;
  tooltipContent(tooltip, title, lines);
  tooltip.classList.add("visible");
  const markBox = mark.getBoundingClientRect(), width = tooltip.offsetWidth, height = tooltip.offsetHeight;
  const left = markBox.left + markBox.width / 2 - width / 2;
  let top = markBox.top - height - 8;
  if (top < 4) top = Math.min(window.innerHeight - height - 4, markBox.bottom + 8);
  tooltip.style.left = `${Math.max(4, Math.min(window.innerWidth - width - 4, left))}px`;
  tooltip.style.top = `${Math.max(4, top)}px`;
}

function showTooltipAtPointer(tooltip, x, y, title, lines) {
  if (!tooltip) return;
  tooltipContent(tooltip, title, lines);
  tooltip.classList.add("visible");
  const width = tooltip.offsetWidth, height = tooltip.offsetHeight, gap = 12;
  let left = x + gap, top = y + gap;
  if (left + width > window.innerWidth - 4) left = x - width - gap;
  if (top + height > window.innerHeight - 4) top = y - height - gap;
  tooltip.style.left = `${Math.max(4, Math.min(window.innerWidth - width - 4, left))}px`;
  tooltip.style.top = `${Math.max(4, Math.min(window.innerHeight - height - 4, top))}px`;
}

function hideTooltip(tooltip) {
  tooltip?.classList.remove("visible");
}

function bindTooltip(selection, tooltip, container, content, keyboard = true) {
  const show = (x, y, datum) => {
    const [title, lines] = content(datum);
    showTooltipAtPointer(tooltip, x, y, title, lines);
  };
  selection
    .on("pointerenter pointermove", (event, datum) => show(event.clientX, event.clientY, datum))
    .on("pointerleave", () => hideTooltip(tooltip));
  if (keyboard) {
    selection.attr("tabindex", 0).on("focus", function(event, datum) {
      const [title, lines] = content(datum);
      showTooltipAtMark(tooltip, this, title, lines);
    }).on("blur", () => hideTooltip(tooltip)).on("keydown", (event) => {
      if (event.key === "Escape") hideTooltip(tooltip);
    });
  }
  // Live redraws replace the marks under a stationary cursor; if the pointer is
  // still over one of the new marks, restore the tooltip so it keeps tracking.
  const pointer = container?._chartPointer;
  if (pointer) {
    const node = document.elementFromPoint(pointer.x, pointer.y);
    if (node && selection.nodes().includes(node)) show(pointer.x, pointer.y, d3.select(node).datum());
  }
}

function ChartShell({ refs, className, label }) {
  return <div className={`d3-chart ${className || ""}`} ref={refs.containerRef}>
    <svg ref={refs.svgRef} role="img" aria-label={label}/>
    <div className="chart-tooltip" ref={refs.tooltipRef} role="tooltip"/>
  </div>;
}

export function Sparkline({ values = [], times = [], color = css("--orange"), width = 120, height = 32, fill = true, max = 0, formatValue = (value) => `${value} ms`, label = "Latency history" }) {
  // Pair values with their optional timestamps before filtering so they stay aligned.
  // With a fixed max scale, non-positive values mark failed checks and pin to the top.
  const points = values.map((value, index) => {
    const num = Number(value);
    const failed = max > 0 && !(num > 0);
    return { value: failed ? max : num, time: times[index], failed };
  }).filter((point) => Number.isFinite(point.value));
  const clean = points.map((point) => point.value);
  const refs = useChart((svg, availableWidth, tooltip, container) => {
    const w = Math.min(width, availableWidth), h = height;
    svg.selectAll("*").remove();
    svg.attr("viewBox", `0 0 ${w} ${h}`).attr("width", w).attr("height", h);
    if (clean.length < 2) return;
    const x = d3.scaleLinear().domain([0, clean.length - 1]).range([2, w - 2]);
    let y;
    if (max > 0) {
      y = d3.scaleLinear().domain([0, max]).range([h - 3, 3]).clamp(true);
    } else {
      const extent = d3.extent(clean), padding = Math.max(1, (extent[1] - extent[0]) * .12);
      y = d3.scaleLinear().domain([Math.max(0, extent[0] - padding), extent[1] + padding]).range([h - 3, 3]);
    }
    const line = d3.line().x((_, index) => x(index)).y(y).curve(d3.curveMonotoneX);
    if (fill) {
      svg.append("path").datum(clean).attr("class", "spark-area").attr("d", d3.area().x((_, index) => x(index)).y0(h).y1(y).curve(d3.curveMonotoneX)).attr("fill", color).attr("opacity", .12);
    }
    svg.append("path").datum(clean).attr("class", "spark-line").attr("d", line).attr("fill", "none").attr("stroke", color).attr("stroke-width", 1.6);
    const marker = svg.append("circle").attr("class", "spark-marker").attr("r", 2.5).attr("fill", color).attr("visibility", "hidden");
    const overlay = svg.append("rect").attr("width", w).attr("height", h).attr("fill", "transparent");
    const showAt = (clientX, clientY) => {
      const box = overlay.node().getBoundingClientRect();
      const index = Math.max(0, Math.min(clean.length - 1, Math.round(x.invert((clientX - box.left) * (w / box.width)))));
      marker.attr("cx", x(index)).attr("cy", y(clean[index])).attr("visibility", "visible");
      const lines = [points[index].failed ? "failed" : formatValue(clean[index])];
      const time = points[index].time ? new Date(points[index].time) : null;
      if (time && !Number.isNaN(time.getTime())) lines.push(d3.timeFormat("%H:%M:%S")(time));
      showTooltipAtPointer(tooltip, clientX, clientY, label, lines);
    };
    overlay.on("pointermove", (event) => showAt(event.clientX, event.clientY))
      .on("pointerleave", () => { marker.attr("visibility", "hidden"); hideTooltip(tooltip); });
    const pointer = container._chartPointer;
    if (pointer) {
      const box = overlay.node().getBoundingClientRect();
      if (pointer.x >= box.left && pointer.x <= box.right && pointer.y >= box.top && pointer.y <= box.bottom) showAt(pointer.x, pointer.y);
    }
  }, [points.map((point) => `${point.value}:${point.failed ? "!" : ""}:${point.time || ""}`).join(","), color, width, height, fill, max, formatValue, label]);
  return <ChartShell refs={refs} className="sparkline" label={label}/>;
}

export function Donut({ segments, label, sub, size = 132, thickness = 18 }) {
  const valuesKey = segments.map((segment) => `${segment.label}:${segment.value}:${segment.color}`).join("|");
  const refs = useChart((svg, availableWidth, tooltip, container) => {
    const chartSize = Math.min(size, availableWidth), radius = chartSize / 2, total = d3.sum(segments, (segment) => Math.max(0, Number(segment.value) || 0));
    svg.selectAll("*").remove();
    svg.attr("viewBox", `0 0 ${chartSize} ${chartSize}`).attr("width", chartSize).attr("height", chartSize);
    const root = svg.append("g").attr("transform", `translate(${radius},${radius})`);
    root.append("circle").attr("r", radius - thickness / 2).attr("fill", "none").attr("stroke", css("--hover")).attr("stroke-width", thickness);
    if (total > 0) {
      const arcs = d3.pie().sort(null).value((segment) => Math.max(0, Number(segment.value) || 0))(segments);
      const paths = root.selectAll("path").data(arcs).join("path")
        .attr("d", d3.arc().innerRadius(radius - thickness).outerRadius(radius))
        .attr("fill", (datum) => datum.data.color)
        .attr("aria-label", (datum) => `${datum.data.label}: ${datum.data.value}`);
      bindTooltip(paths, tooltip, container, (datum) => {
        const percent = total ? Math.round(datum.data.value / total * 100) : 0;
        return [datum.data.label, [`${Number(datum.data.value).toLocaleString()} requests`, `${percent}% of routed queries`]];
      });
    }
    root.append("text").attr("class", "donut-value").attr("text-anchor", "middle").attr("y", -1).text(label);
    root.append("text").attr("class", "donut-label").attr("text-anchor", "middle").attr("y", 15).text(sub);
  }, [valuesKey, label, sub, size, thickness]);
  return <ChartShell refs={refs} className="donut" label={`${label} ${sub}`}/>;
}

export function Gauge({ value, max = 100, label, sub, color = css("--orange"), size = 150 }) {
  const refs = useChart((svg, availableWidth, tooltip, container) => {
    const chartSize = Math.min(size, availableWidth), geometry = gaugeGeometry(value, max, chartSize);
    const arc = d3.arc().innerRadius(geometry.radius - 12).outerRadius(geometry.radius).startAngle(-Math.PI / 2);
    svg.selectAll("*").remove();
    svg.attr("viewBox", `0 0 ${geometry.width} ${geometry.height + 24}`).attr("width", geometry.width).attr("height", geometry.height + 24);
    const root = svg.append("g").attr("transform", `translate(${geometry.centerX},${geometry.centerY})`);
    root.append("path").attr("d", arc({ endAngle: Math.PI / 2 })).attr("fill", css("--hover"));
    const valuePath = root.append("path").attr("d", arc({ endAngle: -Math.PI / 2 + Math.PI * geometry.fraction })).attr("fill", color);
    bindTooltip(valuePath, tooltip, container, () => [sub || "Value", [`${label}`, `${Math.round(geometry.fraction * 100)}% of scale`]]);
    svg.append("text").attr("class", "gauge-label").attr("x", geometry.centerX).attr("y", geometry.centerY - 4).attr("text-anchor", "middle").text(label);
    svg.append("text").attr("class", "gauge-sub").attr("x", geometry.centerX).attr("y", geometry.height + 16).attr("text-anchor", "middle").text(sub);
  }, [value, max, label, sub, color, size]);
  return <ChartShell refs={refs} className="gauge" label={`${label} ${sub}`}/>;
}

export function AreaChart({ series, formatY, height = 210, windowSeconds = 120 }) {
  const seriesKey = series.map((item) => `${item.label}:${item.values.map((point) => `${point.time}:${point.value}`).join(",")}`).join("|");
  const refs = useChart((svg, width, tooltip, container) => {
    // The container may be flex-stretched taller than the base height; fill it.
    const chartHeight = Math.max(height, container.clientHeight || 0);
    const margin = { top: 18, right: 10, bottom: 29, left: 54 }, innerWidth = Math.max(1, width - margin.left - margin.right), innerHeight = chartHeight - margin.top - margin.bottom;
    const all = series.flatMap((item) => item.values).map((point) => ({ ...point, date: new Date(point.time), value: Math.max(0, Number(point.value) || 0) })).filter((point) => !Number.isNaN(point.date.getTime()));
    svg.selectAll("*").remove();
    svg.attr("viewBox", `0 0 ${width} ${chartHeight}`).attr("width", width).attr("height", chartHeight);
    if (!all.length) return;
    const latest = d3.max(all, (point) => point.date), earliest = new Date(latest.getTime() - windowSeconds * 1000);
    const x = d3.scaleTime().domain([earliest, latest]).range([0, innerWidth]);
    const y = d3.scaleLinear().domain([0, Math.max(1, d3.max(all, (point) => point.value) * 1.1)]).nice(4).range([innerHeight, 0]);
    const root = svg.append("g").attr("transform", `translate(${margin.left},${margin.top})`);
    root.append("text").attr("class", "axis-unit").attr("x", 0).attr("y", -7).text("Bytes/s");
    root.append("g").attr("class", "chart-grid").call(d3.axisLeft(y).ticks(4).tickSize(-innerWidth).tickFormat(""));
    root.append("g").attr("class", "chart-axis y-axis").call(d3.axisLeft(y).ticks(4).tickFormat(formatY));
    root.append("g").attr("class", "chart-axis x-axis").attr("transform", `translate(0,${innerHeight})`).call(d3.axisBottom(x).ticks(width < 460 ? 3 : 5).tickFormat(d3.timeFormat("%H:%M:%S")));
    const area = d3.area().defined((point) => Number.isFinite(point.value)).x((point) => x(point.date)).y0(innerHeight).y1((point) => y(point.value)).curve(d3.curveMonotoneX);
    const line = d3.line().defined((point) => Number.isFinite(point.value)).x((point) => x(point.date)).y((point) => y(point.value)).curve(d3.curveMonotoneX);
    const normalized = series.map((item) => ({ ...item, values: item.values.map((point) => ({ ...point, date: new Date(point.time), value: Math.max(0, Number(point.value) || 0) })).filter((point) => !Number.isNaN(point.date.getTime())) }));
    for (const item of normalized) {
      root.append("path").datum(item.values).attr("class", "throughput-area").attr("d", area).attr("fill", item.color).attr("opacity", .09);
      root.append("path").datum(item.values).attr("class", "throughput-line").attr("d", line).attr("fill", "none").attr("stroke", item.color).attr("stroke-width", 1.8);
    }
    const crosshair = root.append("line").attr("class", "crosshair").attr("y1", 0).attr("y2", innerHeight).attr("visibility", "hidden");
    const dots = root.selectAll("circle.hover-dot").data(normalized).join("circle").attr("class", "hover-dot").attr("r", 3.5).attr("fill", (item) => item.color).attr("visibility", "hidden");
    const overlay = root.append("rect").attr("class", "chart-overlay").attr("width", innerWidth).attr("height", innerHeight).attr("fill", "transparent").attr("tabindex", 0);
    const samples = normalized[0]?.values || [];
    let selectedIndex = samples.length - 1;
    const selectPoint = (index, { mark = overlay.node(), pointer } = {}) => {
      if (!samples.length) return;
      selectedIndex = Math.max(0, Math.min(samples.length - 1, index));
      const point = samples[selectedIndex], px = x(point.date);
      crosshair.attr("x1", px).attr("x2", px).attr("visibility", "visible");
      dots.attr("cx", px).attr("cy", (item) => y(item.values[selectedIndex]?.value || 0)).attr("visibility", "visible");
      const title = d3.timeFormat("%H:%M:%S")(point.date), lines = normalized.map((item) => `${item.label}: ${formatY(item.values[selectedIndex]?.value || 0)}/s`);
      if (pointer) showTooltipAtPointer(tooltip, pointer.x, pointer.y, title, lines);
      else showTooltipAtMark(tooltip, mark, title, lines);
    };
    const selectAtPointer = (pointer) => {
      const date = x.invert(pointer.x - overlay.node().getBoundingClientRect().left);
      const index = d3.bisector((point) => point.date).center(samples, date);
      selectPoint(index, { pointer });
    };
    overlay.on("pointermove", (event) => selectAtPointer({ x: event.clientX, y: event.clientY }))
      .on("pointerleave blur", () => { crosshair.attr("visibility", "hidden"); dots.attr("visibility", "hidden"); hideTooltip(tooltip); })
      .on("focus", function() { selectPoint(selectedIndex, { mark: dots.nodes()[0] || this }); })
      .on("keydown", function(event) {
        if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
          event.preventDefault();
          selectPoint(selectedIndex + (event.key === "ArrowLeft" ? -1 : 1), { mark: dots.nodes()[0] || this });
        }
      });
    // Keep the crosshair and tooltip tracking the cursor across live redraws.
    const pointer = container._chartPointer;
    if (pointer) {
      const box = overlay.node().getBoundingClientRect();
      if (pointer.x >= box.left && pointer.x <= box.right && pointer.y >= box.top && pointer.y <= box.bottom) selectAtPointer(pointer);
    }
  }, [seriesKey, formatY, height, windowSeconds]);
  return <ChartShell refs={refs} className="area-chart" label="Throughput over the last 120 seconds"/>;
}

export function BarList({ items, formatValue, label = "Ranked values" }) {
  const key = items.map((item) => `${item.label}:${item.value}:${item.color}:${item.detail || ""}`).join("|");
  const height = Math.max(32, items.length * 32);
  const refs = useChart((svg, width, tooltip, container) => {
    const labelWidth = Math.min(150, Math.max(88, width * .28)), valueWidth = 78, barStart = labelWidth + 8, barWidth = Math.max(30, width - barStart - valueWidth - 8);
    const x = d3.scaleLinear().domain([0, Math.max(1, d3.max(items, (item) => Number(item.value) || 0))]).range([0, barWidth]);
    svg.selectAll("*").remove();
    svg.attr("viewBox", `0 0 ${width} ${height}`).attr("width", width).attr("height", height);
    const rows = svg.selectAll("g.bar-item").data(items).join("g").attr("class", "bar-item").attr("transform", (_, index) => `translate(0,${index * 32})`);
    const labelChars = Math.max(8, Math.floor(labelWidth / 7));
    rows.append("text").attr("class", "bar-label").attr("x", 0).attr("y", 20).text((item) => truncate(item.label, labelChars));
    rows.append("rect").attr("class", "bar-track").attr("x", barStart).attr("y", 11).attr("width", barWidth).attr("height", 8).attr("rx", 4);
    const bars = rows.append("rect").attr("class", "bar-mark").attr("x", barStart).attr("y", 11).attr("width", (item) => x(Math.max(0, Number(item.value) || 0))).attr("height", 8).attr("rx", 4).attr("fill", (item) => item.color || css("--orange"));
    rows.append("text").attr("class", "bar-value").attr("x", width).attr("y", 20).attr("text-anchor", "end").text((item) => formatValue(item.value));
    bindTooltip(bars, tooltip, container, (item) => [item.label, [formatValue(item.value), item.detail].filter(Boolean)]);
  }, [key, formatValue, label, height]);
  return <ChartShell refs={refs} className="bar-list" label={label}/>;
}

export function ConnectivityBars({ data = {}, formatValue }) {
  const slotCount = 60;
  const history = (data.history || []).slice(-slotCount);
  const key = history.map((item) => `${item.time}:${item.status}:${item.relay || ""}:${item.latency_ms}`).join("|");
  const height = 54;
  const refs = useChart((svg, width, tooltip, container) => {
    const labelWidth = 48, plotStart = labelWidth + 7, plotWidth = Math.max(80, width - plotStart - 2), top = 7;
    // Fixed scale: full bar height represents 1s, longer latencies clamp to full height.
    const y = d3.scaleLinear().domain([0, 1000]).range([2, 28]).clamp(true);
    const slots = Array.from({ length: slotCount }, (_, index) => history[index - (slotCount - history.length)] || null);
    const x = d3.scaleBand().domain(d3.range(slotCount)).range([plotStart, plotStart + plotWidth]).padding(.18);
    svg.selectAll("*").remove();
    svg.attr("viewBox", `0 0 ${width} ${height}`).attr("width", width).attr("height", height);
    svg.append("text").attr("class", "connectivity-label").attr("x", 0).attr("y", top + 13).text("RTT");
    svg.append("text").attr("class", "connectivity-value").attr("x", 0).attr("y", top + 27).text(formatValue(data.latency_ms));
    // Down slots render as full-height red bars so outages stand out.
    const barHeight = (item) => {
      if (!item) return 2;
      if (item.status === "down") return 28;
      return y(Number(item.latency_ms) || 0);
    };
    svg.append("g").selectAll("rect").data(slots).join("rect")
      .attr("x", (_, index) => x(index)).attr("width", x.bandwidth())
      .attr("y", (item) => top + 30 - barHeight(item))
      .attr("height", barHeight)
      .attr("rx", 1).attr("fill", (item) => connectivityColor(item));
    // Full-height invisible hit targets: the visible bars are only a few pixels tall.
    const hits = svg.append("g").selectAll("rect").data(slots).join("rect")
      .attr("x", (_, index) => x(index)).attr("width", x.bandwidth())
      .attr("y", top).attr("height", 32).attr("fill", "transparent");
    bindTooltip(hits.filter((item) => item), tooltip, container, (item) => ["Round trip", [formatValue(item.latency_ms), item.status || "unknown", item.relay, d3.timeFormat("%H:%M:%S")(new Date(item.time))].filter(Boolean)], false);
  }, [key, data.latency_ms, data.status, formatValue]);
  return <ChartShell refs={refs} className="connectivity-bars" label="Round-trip latency history"/>;
}

function connectivityColor(item) {
  if (!item) return css("--hover");
  if (item.status === "down") return css("--red");
  const value = Number(item.latency_ms) || 0;
  if (value <= 0) return css("--hover");
  if (item.status === "degraded" || value > 500) return css("--amber");
  return css("--green");
}

// LineChart plots discrete latency samples over real time: one dot per
// request, connected by a line, with time on the x axis.
export function LineChart({ points = [], formatY = (value) => `${value} ms`, height = 150, label = "Latency over time", unit = "ms", windowSeconds = 0, windowEnd = 0 }) {
  const key = points.map((point) => `${point.time}:${point.value}:${point.detail || ""}`).join("|");
  const refs = useChart((svg, width, tooltip, container) => {
    const margin = { top: 16, right: 10, bottom: 26, left: 46 }, innerWidth = Math.max(1, width - margin.left - margin.right), innerHeight = height - margin.top - margin.bottom;
    const end = windowSeconds > 0 ? new Date(windowEnd || Date.now()) : null;
    const start = end ? new Date(end.getTime() - windowSeconds * 1000) : null;
    const data = points.map((point) => ({ ...point, date: new Date(point.time), value: Math.max(0, Number(point.value) || 0) })).filter((point) => {
      if (Number.isNaN(point.date.getTime())) return false;
      return !start || (point.date >= start && point.date <= end);
    });
    svg.selectAll("*").remove();
    svg.attr("viewBox", `0 0 ${width} ${height}`).attr("width", width).attr("height", height);
    if (data.length < 2) return;
    const x = d3.scaleTime().domain(start ? [start, end] : d3.extent(data, (point) => point.date)).range([0, innerWidth]);
    const y = d3.scaleLinear().domain([0, Math.max(1, d3.max(data, (point) => point.value) * 1.1)]).nice(4).range([innerHeight, 0]);
    const root = svg.append("g").attr("transform", `translate(${margin.left},${margin.top})`);
    root.append("text").attr("class", "axis-unit").attr("x", 0).attr("y", -6).text(unit);
    root.append("g").attr("class", "chart-grid").call(d3.axisLeft(y).ticks(4).tickSize(-innerWidth).tickFormat(""));
    root.append("g").attr("class", "chart-axis y-axis").call(d3.axisLeft(y).ticks(4).tickFormat(formatY));
    root.append("g").attr("class", "chart-axis x-axis").attr("transform", `translate(0,${innerHeight})`).call(d3.axisBottom(x).ticks(width < 460 ? 3 : 5).tickFormat(d3.timeFormat("%H:%M:%S")));
    const line = d3.line().x((point) => x(point.date)).y((point) => y(point.value)).curve(d3.curveMonotoneX);
    root.append("path").datum(data).attr("d", line).attr("fill", "none").attr("stroke", css("--orange")).attr("stroke-width", 1.6);
    root.append("g").selectAll("circle").data(data).join("circle")
      .attr("cx", (point) => x(point.date)).attr("cy", (point) => y(point.value))
      .attr("r", 2).attr("fill", css("--orange"));
    const marker = root.append("circle").attr("r", 3.5).attr("fill", css("--orange")).attr("visibility", "hidden");
    const overlay = root.append("rect").attr("class", "chart-overlay").attr("width", innerWidth).attr("height", innerHeight).attr("fill", "transparent");
    const showAt = (clientX, clientY) => {
      const box = overlay.node().getBoundingClientRect();
      const date = x.invert(clientX - box.left);
      const index = d3.bisector((point) => point.date).center(data, date);
      const point = data[index];
      marker.attr("cx", x(point.date)).attr("cy", y(point.value)).attr("visibility", "visible");
      showTooltipAtPointer(tooltip, clientX, clientY, d3.timeFormat("%H:%M:%S")(point.date), [formatY(point.value), point.detail].filter(Boolean));
    };
    overlay.on("pointermove", (event) => showAt(event.clientX, event.clientY))
      .on("pointerleave", () => { marker.attr("visibility", "hidden"); hideTooltip(tooltip); });
    const pointer = container._chartPointer;
    if (pointer) {
      const box = overlay.node().getBoundingClientRect();
      if (pointer.x >= box.left && pointer.x <= box.right && pointer.y >= box.top && pointer.y <= box.bottom) showAt(pointer.x, pointer.y);
    }
  }, [key, formatY, height, label, unit, windowSeconds, windowEnd]);
  return <ChartShell refs={refs} className="line-chart" label={label}/>;
}

function truncate(value, maxLength) {
  const text = String(value || "");
  return text.length > maxLength ? `${text.slice(0, Math.max(1, maxLength - 1))}…` : text;
}
