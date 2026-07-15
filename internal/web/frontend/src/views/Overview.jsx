import { useStatus } from "../App.jsx";
import { AreaChart, ConnectivityBars, Donut, Sparkline } from "../charts.jsx";
import { Card, CardHeader, Empty, Pill, StatTile } from "../components.jsx";
import { fmtBytes, fmtLatency, fmtNum, fmtRate, fmtUptime, shortName, statusColor } from "../utils.js";

const colors = { relay: "var(--orange)", direct: "var(--teal)", reject: "var(--red)", up: "var(--orange)", down: "var(--blue)" };
const statusColors = { green: "var(--green)", amber: "var(--amber)", red: "var(--red)", blue: "var(--blue)", gray: "var(--text-faint)" };

export default function Overview() {
  const status = useStatus();
  if (!status) return <Empty>Waiting for daemon status…</Empty>;
  const general = status.general || {}, relay = status.relay || {}, dns = status.dns || {}, connectivity = status.connectivity || {};
  const total = (dns.relay?.requests || 0) + (dns.direct?.requests || 0) + (dns.reject?.requests || 0);
  const hitRatio = dns.total_queries > 0 ? Math.round(dns.cache_hits / dns.total_queries * 100) : 0;
  const direct = !relay.active_relay || relay.active_relay === "DIRECT", relayColor = statusColor(relay.status);
  const history = relay.throughput_history || [];
  const throughputSeries = [
    { label: "Download", values: history.map((sample) => ({ time: sample.time, value: sample.download_bps || 0 })), color: colors.down },
    { label: "Upload", values: history.map((sample) => ({ time: sample.time, value: sample.upload_bps || 0 })), color: colors.up },
  ];
  const relayGroups = status.relay_groups || [];
  return <>
    <div className="grid cols-4">
      <StatTile label="Active relay" value={<span className="relay-value">{direct ? "DIRECT" : shortName(relay.active_relay)}</span>} detail={<Pill color={direct ? "gray" : relayColor}>{direct ? "direct" : relay.status || "unknown"}</Pill>}/>
      <StatTile label="Total traffic" value={<>{fmtBytes(relay.download_bytes)} <small>↓</small></>} detail={`${fmtBytes(relay.upload_bytes)} ↑`}/>
      <StatTile label="Active sessions" value={fmtNum(relay.active_sessions)} detail={`${fmtNum(relay.total_processed_sessions)} total`}/>
      <StatTile label="DNS queries" value={fmtNum(dns.total_queries)} detail={`${hitRatio}% cache hits`}/>
    </div>
    <div className="overview-primary mt">
      <Card className="throughput-card"><CardHeader title="Throughput"/><div className="card-body"><AreaChart series={throughputSeries} formatY={fmtBytes} height={240}/><div className="legend"><span><i style={{background: colors.down}}/>Download<span className="mono muted">{fmtRate(relay.download_bps)}</span></span><span><i style={{background: colors.up}}/>Upload<span className="mono muted">{fmtRate(relay.upload_bps)}</span></span></div></div></Card>
      <Card><CardHeader title="DNS decisions"/><div className="card-body dns-decision-wrap"><Donut segments={[{label:"Relay",value:dns.relay?.requests||0,color:colors.relay},{label:"Direct",value:dns.direct?.requests||0,color:colors.direct},{label:"Reject",value:dns.reject?.requests||0,color:colors.reject}]} label={fmtNum(total)} sub="routed"/><div className="decision-legend"><Decision label="Relay" stat={dns.relay} color={colors.relay} total={total}/><Decision label="Direct" stat={dns.direct} color={colors.direct} total={total}/><Decision label="Reject" stat={dns.reject} color={colors.reject} total={total}/></div></div></Card>
    </div>
    <div className="grid cols-3 mt">
      <Card><CardHeader title="Connectivity" sub={`every ${Math.round((connectivity.check_interval_ms || 0)/1000)}s`}/><div className="card-body"><Connection title="Internet (direct)" data={connectivity.domestic}/><hr/><Connection title="Relayed (outside)" data={connectivity.outside}/></div></Card>
      <Card><CardHeader title="Relay groups" sub={`${relayGroups.length} groups`}/><div className="card-body tight">{relayGroups.length ? relayGroups.map((group) => <GroupRow key={group.name} group={group}/>) : <Empty>No relay groups configured.</Empty>}</div></Card>
      <Card><CardHeader title="Daemon"/><div className="card-body"><dl className="kv"><dt>Version</dt><dd>{general.version || "—"}</dd><dt>Platform</dt><dd>{general.architecture || "—"}</dd><dt>Uptime</dt><dd>{fmtUptime(general.uptime_seconds)}</dd><dt>Memory</dt><dd>{fmtBytes(general.memory_bytes)}</dd><dt>Goroutines</dt><dd>{fmtNum(general.goroutines)}</dd><dt>Cache entries</dt><dd>{fmtNum(dns.cache_entries)}</dd><dt>UDP drops</dt><dd>{fmtNum(relay.udp?.packets_dropped)}</dd></dl></div></Card>
    </div>
  </>;
}

function Decision({ label, stat, color, total }) {
  const value = stat?.requests || 0, percent = total ? Math.round(value / total * 100) : 0;
  return <div className="decision-row"><div className="spread"><span><i className="swatch" style={{background: color}}/>{label}</span><span className="mono muted">{fmtNum(value)} · {percent}%</span></div><div className="last-domain"><span>Last routed</span><span className="mono" title={stat?.last_domain}>{stat?.last_domain || "—"}</span></div></div>;
}

function GroupRow({ group }) {
  const color = statusColors[statusColor(group.current_status)];
  const history = (group.history || []).map((record) => record.latency_ms || 0);
  return <div className="group-row">
    <div className="spread"><span className="flex"><strong>{group.name}</strong>{group.selected && <Pill color="orange">active</Pill>}</span><span className="mono muted">{fmtLatency(group.current_latency_ms)}</span></div>
    <div className="spread group-row-sub">
      <span className="mono">{shortName(group.current_relay, group.name) || "—"}{group.current_status ? ` · ${group.current_status}` : ""}</span>
      {history.some((value) => value > 0) ? <Sparkline values={history} color={color} width={110} height={22} fill={false} formatValue={fmtLatency} label={`${group.name} latency history`}/> : <span className="faint">—</span>}
    </div>
  </div>;
}

function Connection({ title, data = {} }) {
  const color = statusColor(data.status);
  return <div className="connection"><div className="spread"><strong>{title}</strong><Pill color={color}>{data.status || "unknown"}</Pill></div><ConnectivityBars data={data} formatValue={fmtLatency}/>{data.error && <div className="inline-error" title={data.error}>{data.error}</div>}</div>;
}
