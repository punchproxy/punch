import { useCallback, useMemo, useState } from "react";
import { api } from "../api.js";
import { Sparkline } from "../charts.jsx";
import RelayPath from "../RelayPath.jsx";
import { Card, Empty, ErrorState, Pill, Tag, usePolling, useToast } from "../components.jsx";
import { filterRelays, fmtDuration, fmtLatency, formatRelayAddress, nextRelayGroupSelectMode, shortName, statusColor, timeAgo } from "../utils.js";

export default function Relays() {
  const [groups, setGroups] = useState([]), [relays, setRelays] = useState([]), [error, setError] = useState(null);
  const [search, setSearch] = useState(""), [sort, setSort] = useState("latency"), [busy, setBusy] = useState(new Set());
  const [showAllRelays, setShowAllRelays] = useState(false);
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

  const activeGroup = groups.find((group) => group.selected);
  const selectedGroup = activeGroup?.name || "";
  const shown = useMemo(() => filterRelays(relays, selectedGroup, search, showAllRelays).sort((a, b) => compareRelays(a, b, sort)), [relays, selectedGroup, search, showAllRelays, sort]);
  if (error && !groups.length && !relays.length) return <ErrorState error={error}/>;
  return <>
    {activeGroup?.path?.length > 0 && <Card className="active-path-card"><RelayPath path={activeGroup.path} label="Active path"/></Card>}
    <div className="toolbar">
      <input className="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter relays…" aria-label="Filter relays"/>
      <select value={sort} onChange={(event) => setSort(event.target.value)} aria-label="Sort relays"><option value="latency">Sort: latency</option><option value="name">Sort: name</option><option value="group">Sort: group</option><option value="status">Sort: status</option></select>
      <div className="spacer"/>
      <button className="btn btn-sm" disabled={busy.has("check-all")} onClick={() => act("check-all", () => api.post("/relaygroups/check?all=true"), "Health check started for all groups")}>{busy.has("check-all") ? "Checking…" : "Check all"}</button>
      <button className="btn btn-sm" disabled={busy.has("refresh-all")} onClick={() => act("refresh-all", () => api.post("/relaygroups/refresh?all=true"), "Subscriptions refreshed")}>Refresh subscriptions</button>
    </div>
    <div className="section-title">Groups</div>
    <div className="grid cols-3">{groups.length ? groups.map((group) => <GroupCard key={group.name} group={group} busy={busy} act={act}/>) : <Empty>No relay groups configured.</Empty>}</div>
    <div className="section-title relay-section-title"><div>Relays <span>({shown.length})</span>{!showAllRelays && selectedGroup && <small className="faint block">Exit group and groups in use</small>}</div><label className="relay-scope-toggle"><input type="checkbox" role="switch" checked={showAllRelays} onChange={(event) => setShowAllRelays(event.target.checked)}/><span>Show all relays</span></label></div>
    <Card><div className="table-wrap"><table className="data responsive-table relays-table"><thead><tr><th>Relay</th><th>Group</th><th>Type</th><th>Status</th><th>Roundtrip</th><th>Checked</th><th>Actions</th></tr></thead><tbody>
      {shown.map((relay) => <RelayRow key={`${relay.group}-${relay.name}`} relay={relay} group={groups.find((group) => group.name === relay.group)} busy={busy} act={act}/>)}
      {!shown.length && <tr className="empty-row"><td colSpan="7"><Empty>No relays.</Empty></td></tr>}
    </tbody></table></div></Card>
  </>;
}

function GroupCard({ group, busy, act }) {
  const selectKey = `gsel-${group.name}`, modeKey = `gmode-${group.name}`, roleKey = `grole-${group.name}`, checkKey = `gcheck-${group.name}`, refreshKey = `grefresh-${group.name}`;
  const nextMode = nextRelayGroupSelectMode(group.select);
  const exitEligible = group.config?.exit_eligible ?? group.exit_eligible ?? true;
  return <Card className={group.selected || group.in_use ? "selected-card" : ""}><div className="card-head"><div className="flex relay-group-title"><h3>{group.name}</h3>{group.selected ? <Pill color="orange">exit</Pill> : group.in_use && <Pill color="blue">transit in use</Pill>}{!exitEligible && <Pill plain>transit only</Pill>}</div><Pill plain>{group.type}</Pill></div><div className="card-body tight">
    <div className="spread info-row"><span className="muted">Select mode</span>{group.type === "direct" ? <Pill color="gray" plain>{group.select}</Pill> : <button type="button" className={`pill ${group.select === "auto" ? "blue" : "gray"} plain select-mode-toggle`} disabled={busy.has(modeKey)} aria-label={`Switch ${group.name} relay selection to ${nextMode}`} aria-pressed={group.select === "auto"} title={`Switch to ${nextMode}`} onClick={() => act(modeKey, () => api.put(`/relaygroups/${encodeURIComponent(group.name)}`, { ...group.config, select: nextMode }), `${group.name} relay selection switched to ${nextMode}`)}>{group.select}</button>}</div>
    <div className="spread info-row"><span className="muted">Relays</span><span className="mono">{group.relay_count}</span></div>
    {group.type !== "direct" && <div className="spread info-row"><label htmlFor={`exit-eligible-${group.name}`} className="muted">Allow as exit</label><input id={`exit-eligible-${group.name}`} className="relay-exit-toggle" type="checkbox" role="switch" checked={exitEligible} disabled={busy.has(roleKey)} title="When disabled, this group can only carry another relay's connections" onChange={(event) => { const eligible = event.target.checked; act(roleKey, () => api.put(`/relaygroups/${encodeURIComponent(group.name)}`, { ...group.config, exit_eligible: eligible }), `${group.name} ${eligible ? "can be used as exit" : "is transit only"}`); }}/></div>}
    <div className="spread info-row"><span className="muted">Current</span><span className="mono">{shortName(group.current_relay, group.name) || "—"}</span></div>
    <div className="spread info-row"><span className="muted">Status</span>{group.current_status ? <Pill color={statusColor(group.current_status)}>{group.current_status}</Pill> : <span className="faint">—</span>}</div>
    <div className="spread info-row"><span className="muted">Latency</span><span className="mono">{fmtLatency(group.current_latency_ms)}</span></div>
    {group.dialer_proxy && <div className="spread info-row"><span className="muted">Dialer proxy</span><span className="mono">{group.dialer_proxy}</span></div>}
    {group.check_interval > 0 && <div className="spread info-row"><span className="muted">Health check</span><span>every {fmtDuration(group.check_interval * 1000)}</span></div>}
    {group.error && <div className="inline-error">{group.error}</div>}
    <div className="btn-row">
      {!group.selected && exitEligible && group.relay_count > 0 && group.type !== "direct" && <button className="btn btn-sm btn-primary" disabled={busy.has(selectKey)} onClick={() => act(selectKey, () => api.post(`/relaygroups/${encodeURIComponent(group.name)}/select`), `Group ${group.name} selected as exit`)}>Use as exit</button>}
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

function RelayRow({ relay, group, busy, act }) {
  const latency = relay.url_test_latency_ms || relay.latency_ms || 0, color = statusColor(relay.status), name = shortName(relay.name, relay.group);
  const selectKey = `sel-${relay.group}-${relay.name}`, exitKey = `exit-${relay.group}-${relay.name}`, checkKey = `check-${relay.group}-${relay.name}`;
  const exitEligible = group?.config?.exit_eligible ?? group?.exit_eligible ?? true;
  const manual = relay.group_mode === "manual", groupSelected = relay.group_selected || relay.selected;
  const selectURL = `/relays/${encodeURIComponent(name)}/select?group=${encodeURIComponent(relay.group)}`;
  return <tr className={relay.selected || relay.in_use ? "selected-row" : ""}>
    <td data-label="Relay"><div className="flex relay-name"><span className="mono">{name}</span>{relay.selected ? <Pill color="orange">exit</Pill> : relay.in_use ? <Pill color="blue">transit in use</Pill> : groupSelected && <Pill plain>group choice</Pill>}</div><small className="mono faint block">{formatRelayAddress(relay.addr, relay.resolved_addr)}</small>{relay.dialer_proxy && <small className="muted block">Dialer proxy: <span className="mono">{relay.dialer_proxy}</span></small>}</td>
    <td data-label="Group" className="muted">{relay.group}</td><td data-label="Type"><Tag>{relay.type || "?"}</Tag></td><td data-label="Status"><div className="flex"><Pill color={color}>{relay.status || "unknown"}</Pill>{relay.recent_stream_aborts > 0 && <span title={`${relay.recent_stream_aborts} relay-side stream abort(s) in the last minute (${relay.stream_aborts} total this run). The relay accepts new connections but is killing live streams.`}><Pill color="amber">{relay.recent_stream_aborts} aborts/1m</Pill></span>}</div></td>
    <td data-label="Roundtrip"><LatencyCell history={relay.history} metric="latency_ms" current={latency} label={`${name} roundtrip latency history`}/></td>
    <td data-label="Checked" className="faint nowrap">{timeAgo(relay.last_checked_at)}{relay.check_interval > 0 && <small className="block">every {fmtDuration(relay.check_interval * 1000)}</small>}</td>
    <td data-label="Actions"><div className="row-actions">{manual && !groupSelected && <button className="btn btn-sm" disabled={busy.has(selectKey)} title={`Change the selected relay within ${relay.group}`} onClick={() => act(selectKey, () => api.post(`${selectURL}&activate=false`), `Set ${name} in ${relay.group}`)}>Set relay</button>}{manual && exitEligible && !group?.selected && !relay.selected && <button className="btn btn-sm" disabled={busy.has(exitKey)} onClick={() => act(exitKey, () => api.post(selectURL), `Selected ${name} as exit`)}>Use as exit</button>}<button className="btn btn-sm btn-ghost" disabled={busy.has(checkKey)} onClick={() => act(checkKey, () => api.post(`/relays/${encodeURIComponent(name)}/check?group=${encodeURIComponent(relay.group)}`), `Checking ${name}`)}>Check</button></div></td>
  </tr>;
}

function compareRelays(a, b, sort) {
  if (sort === "name") return shortName(a.name, a.group).localeCompare(shortName(b.name, b.group));
  if (sort === "group") return (a.group || "").localeCompare(b.group || "");
  if (sort === "status") return (a.status || "").localeCompare(b.status || "");
  const aLatency = a.url_test_latency_ms || a.latency_ms || Infinity, bLatency = b.url_test_latency_ms || b.latency_ms || Infinity;
  return aLatency - bLatency;
}
