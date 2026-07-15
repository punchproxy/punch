import { useCallback, useMemo, useState } from "react";
import { api } from "../api.js";
import { Sparkline } from "../charts.jsx";
import { Card, Empty, ErrorState, Pill, Tag, usePolling, useToast } from "../components.jsx";
import { fmtLatency, shortName, statusColor, timeAgo } from "../utils.js";

export default function Relays() {
  const [groups, setGroups] = useState([]), [relays, setRelays] = useState([]), [error, setError] = useState(null);
  const [search, setSearch] = useState(""), [sort, setSort] = useState("latency"), [busy, setBusy] = useState(new Set());
  const toast = useToast();
  const refresh = useCallback(async () => {
    try {
      const [nextGroups, nextRelays] = await Promise.all([api.get("/relaygroups"), api.get("/relays")]);
      setGroups(nextGroups || []); setRelays(nextRelays || []); setError(null);
    } catch (nextError) { setError(nextError); }
  }, []);
  usePolling(refresh, 3000);

  const act = async (key, fn, message) => {
    if (busy.has(key)) return;
    setBusy((current) => new Set(current).add(key));
    try { await fn(); toast(message, "ok"); await refresh(); }
    catch (nextError) { toast(nextError.message, "err", "Error"); }
    finally { setBusy((current) => { const next = new Set(current); next.delete(key); return next; }); }
  };

  const shown = useMemo(() => relays.filter((relay) => !search.trim() || `${relay.name || ""} ${relay.group || ""} ${relay.addr || ""}`.toLowerCase().includes(search.trim().toLowerCase())).sort((a, b) => compareRelays(a, b, sort)), [relays, search, sort]);
  if (error && !groups.length && !relays.length) return <ErrorState error={error}/>;
  return <>
    <div className="toolbar">
      <input className="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter relays…" aria-label="Filter relays"/>
      <select value={sort} onChange={(event) => setSort(event.target.value)} aria-label="Sort relays"><option value="latency">Sort: latency</option><option value="name">Sort: name</option><option value="group">Sort: group</option><option value="status">Sort: status</option></select>
      <div className="spacer"/>
      <button className="btn btn-sm" disabled={busy.has("check-all")} onClick={() => act("check-all", () => api.post("/relaygroups/check?all=true"), "Health check started for all groups")}>{busy.has("check-all") ? "Checking…" : "Check all"}</button>
      <button className="btn btn-sm" disabled={busy.has("refresh-all")} onClick={() => act("refresh-all", () => api.post("/relaygroups/refresh?all=true"), "Subscriptions refreshed")}>Refresh subscriptions</button>
    </div>
    <div className="section-title">Groups</div>
    <div className="grid cols-3">{groups.length ? groups.map((group) => <GroupCard key={group.name} group={group} busy={busy} act={act}/>) : <Empty>No relay groups configured.</Empty>}</div>
    <div className="section-title">Relays <span>({shown.length})</span></div>
    <Card><div className="table-wrap"><table className="data responsive-table relays-table"><thead><tr><th>Relay</th><th>Group</th><th>Type</th><th>Status</th><th>TCP connect</th><th>Roundtrip</th><th>Checked</th><th>Actions</th></tr></thead><tbody>
      {shown.map((relay) => <RelayRow key={`${relay.group}-${relay.name}`} relay={relay} busy={busy} act={act}/>)}
      {!shown.length && <tr className="empty-row"><td colSpan="8"><Empty>No relays.</Empty></td></tr>}
    </tbody></table></div></Card>
  </>;
}

function GroupCard({ group, busy, act }) {
  const selectKey = `gsel-${group.name}`, checkKey = `gcheck-${group.name}`, refreshKey = `grefresh-${group.name}`;
  return <Card className={group.selected ? "selected-card" : ""}><div className="card-head"><div className="flex"><h3>{group.name}</h3>{group.selected && <Pill color="orange">active</Pill>}</div><Pill plain>{group.type}</Pill></div><div className="card-body tight">
    <div className="spread info-row"><span className="muted">Select mode</span><Pill color={group.select === "auto" ? "blue" : "gray"} plain>{group.select}</Pill></div>
    <div className="spread info-row"><span className="muted">Relays</span><span className="mono">{group.relay_count}</span></div>
    <div className="spread info-row"><span className="muted">Current</span><span className="mono">{shortName(group.current_relay, group.name) || "—"}</span></div>
    <div className="spread info-row"><span className="muted">Status</span>{group.current_status ? <Pill color={statusColor(group.current_status)}>{group.current_status}</Pill> : <span className="faint">—</span>}</div>
    <div className="spread info-row"><span className="muted">Latency</span><span className="mono">{fmtLatency(group.current_latency_ms)}</span></div>
    {group.error && <div className="inline-error">{group.error}</div>}
    <div className="btn-row">
      {!group.selected && group.relay_count > 0 && group.type !== "direct" && <button className="btn btn-sm btn-primary" disabled={busy.has(selectKey)} onClick={() => act(selectKey, () => api.post(`/relaygroups/${encodeURIComponent(group.name)}/select`), `Group ${group.name} selected`)}>Select</button>}
      <button className="btn btn-sm" disabled={busy.has(checkKey)} onClick={() => act(checkKey, () => api.post(`/relaygroups/${encodeURIComponent(group.name)}/check`), `Checking ${group.name}`)}>{busy.has(checkKey) ? "Checking…" : "Check"}</button>
      {group.type === "remote" && <button className="btn btn-sm" disabled={busy.has(refreshKey)} onClick={() => act(refreshKey, () => api.post(`/relaygroups/${encodeURIComponent(group.name)}/refresh`), `Refreshed ${group.name}`)}>Refresh</button>}
    </div>
  </div></Card>;
}

function LatencyCell({ history, metric, current, label }) {
  return <div className="latency-cell">
    {history?.length > 1 ? <Sparkline values={history.map((item) => item[metric] || 0)} times={history.map((item) => item.time)} max={1000} color="var(--text-faint)" width={90} height={24} fill={false} formatValue={fmtLatency} label={label}/> : <span className="faint">—</span>}
    <span className="mono muted nowrap">{fmtLatency(current)}</span>
  </div>;
}

function RelayRow({ relay, busy, act }) {
  const latency = relay.url_test_latency_ms || relay.latency_ms || 0, color = statusColor(relay.status), name = shortName(relay.name, relay.group);
  const selectKey = `sel-${relay.name}`, checkKey = `check-${relay.name}`;
  return <tr className={relay.selected ? "selected-row" : ""}>
    <td data-label="Relay"><div className="flex"><span className="mono">{name}</span>{relay.selected && <Pill color="orange">active</Pill>}</div><small className="mono faint block">{relay.addr}</small></td>
    <td data-label="Group" className="muted">{relay.group}</td><td data-label="Type"><Tag>{relay.type || "?"}</Tag></td><td data-label="Status"><Pill color={color}>{relay.status || "unknown"}</Pill></td>
    <td data-label="TCP connect"><LatencyCell history={relay.history} metric="tcp_connect_latency_ms" current={relay.tcp_connect_latency_ms} label={`${name} TCP connect latency history`}/></td>
    <td data-label="Roundtrip"><LatencyCell history={relay.history} metric="latency_ms" current={latency} label={`${name} roundtrip latency history`}/></td>
    <td data-label="Checked" className="faint nowrap">{timeAgo(relay.last_checked_at)}</td>
    <td data-label="Actions"><div className="row-actions">{!relay.selected && <button className="btn btn-sm" disabled={busy.has(selectKey)} onClick={() => act(selectKey, () => api.post(`/relays/${encodeURIComponent(name)}/select?group=${encodeURIComponent(relay.group)}`), `Selected ${name}`)}>Select</button>}<button className="btn btn-sm btn-ghost" disabled={busy.has(checkKey)} onClick={() => act(checkKey, () => api.post(`/relays/${encodeURIComponent(name)}/check?group=${encodeURIComponent(relay.group)}`), `Checking ${name}`)}>Check</button></div></td>
  </tr>;
}

function compareRelays(a, b, sort) {
  if (sort === "name") return shortName(a.name, a.group).localeCompare(shortName(b.name, b.group));
  if (sort === "group") return (a.group || "").localeCompare(b.group || "");
  if (sort === "status") return (a.status || "").localeCompare(b.status || "");
  const aLatency = a.url_test_latency_ms || a.latency_ms || Infinity, bLatency = b.url_test_latency_ms || b.latency_ms || Infinity;
  return aLatency - bLatency;
}
