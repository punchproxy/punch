import test from "node:test";
import assert from "node:assert/strict";
import { gaugeGeometry } from "./math.js";
import { cacheStateColor, filterSessions, fmtTime, sumBy } from "./utils.js";

test("semicircular gauges never request a major arc", () => {
  assert.equal(gaugeGeometry(300, 500).largeArcFlag, 0);
  assert.equal(gaugeGeometry(500, 500).largeArcFlag, 0);
});

test("live cache records are green and stale records are amber", () => {
  assert.equal(cacheStateColor("live"), "green");
  assert.equal(cacheStateColor("stale"), "amber");
});

test("visible session totals use the filtered rows", () => {
  const sessions = [
    { destination: "one.test", upload_bytes: 10 },
    { destination: "two.test", upload_bytes: 20, closed_at: "2026-01-01T00:00:00Z" },
  ];
  const visible = filterSessions(sessions, "active", "one");
  assert.equal(sumBy(visible, "upload_bytes"), 10);
});

test("invalid and zero timestamps render a placeholder", () => {
  assert.equal(fmtTime(), "—");
  assert.equal(fmtTime("0001-01-01T00:00:00Z"), "—");
  assert.equal(fmtTime("not-a-date"), "—");
});
