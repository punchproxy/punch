import test from "node:test";
import assert from "node:assert/strict";
import { gaugeGeometry } from "./math.js";
import { cacheStateColor, clientIP, filterSessions, fmtTime, sumBy } from "./utils.js";

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

test("client IP strips the port from v4 and v6 sources", () => {
  assert.equal(clientIP("192.168.1.5:52344"), "192.168.1.5");
  assert.equal(clientIP("[fe80::1]:52344"), "fe80::1");
  assert.equal(clientIP("192.168.1.5"), "192.168.1.5");
  assert.equal(clientIP(""), "");
});

test("session search matches the client source address", () => {
  const sessions = [
    { destination: "one.test", source: "192.168.1.5:1000" },
    { destination: "two.test", source: "10.0.0.9:2000" },
  ];
  assert.equal(filterSessions(sessions, "all", "192.168").length, 1);
});

test("invalid and zero timestamps render a placeholder", () => {
  assert.equal(fmtTime(), "—");
  assert.equal(fmtTime("0001-01-01T00:00:00Z"), "—");
  assert.equal(fmtTime("not-a-date"), "—");
});
