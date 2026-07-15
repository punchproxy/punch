import { useCallback, useMemo, useState } from "react";
import { api } from "../api.js";
import { BarList } from "../charts.jsx";
import { Card, Empty, ErrorState, Modal, Pill, StatTile, Tag, useDialog, usePolling, useToast } from "../components.jsx";
import { filterSessions, fmtBytes, fmtDuration, fmtNum, isSessionActive, shortName, sumBy } from "../utils.js";

export default function Sessions() {
  const [sessions, setSessions] = useState([]), [error, setError] = useState(null), [search, setSearch] = useState(""), [filter, setFilter] = useState("all"), [trace, setTrace] = useState(null);
  const toast = useToast(), dialog = useDialog();
  const refresh = useCallback(async () => { try { setSessions(await api.get("/sessions") || []); setError(null); } catch (next) { setError(next); } }, []);
  usePolling(refresh, 2000);
  const rows = useMemo(() => filterSessions(sessions, filter, search), [sessions, filter, search]);
  const activeCount = sessions.filter(isSessionActive).length;
  const talkers = useMemo(() => rows.map((session) => ({ label: shortName(session.destination) || session.dst_ip || "unknown", value: (session.upload_bytes || 0) + (session.download_bytes || 0), color: isSessionActive(session) ? "var(--orange)" : "var(--text-faint)" })).filter((item) => item.value > 0).sort((a,b) => b.value-a.value).slice(0,6), [rows]);
  const terminateAll = async () => {
    if (!await dialog({title:"Terminate all active sessions?",message:"Every active flow will be dropped immediately.",confirmLabel:"Terminate all",danger:true})) return;
    try { const result = await api.del("/sessions?all=true"); toast(`Terminated ${result.terminated} session(s)`, "ok"); refresh(); } catch (next) { toast(next.message,"err","Error"); }
  };
  const terminate = async (id) => { try { await api.del(`/sessions/${encodeURIComponent(id)}`); toast("Session terminated","ok"); refresh(); } catch (next) { toast(next.message,"err","Error"); } };
  const showTrace = async (id) => { try { setTrace(await api.get(`/sessions/${encodeURIComponent(id)}`)); } catch (next) { toast(next.message,"err","Error"); } };
  if (error && !sessions.length) return <ErrorState error={error}/>;
  return <>
    <div className="grid cols-4 stats-row"><StatTile label="Active flows" value={fmtNum(activeCount)}/><StatTile label="Total (history)" value={fmtNum(sessions.length)}/><StatTile label="Upload (visible)" value={fmtBytes(sumBy(rows,"upload_bytes"))}/><StatTile label="Download (visible)" value={fmtBytes(sumBy(rows,"download_bytes"))}/></div>
    {talkers.length > 0 && <Card className="talkers"><div className="card-head"><h3>Top visible talkers</h3><span className="sub">by total bytes</span></div><div className="card-body"><BarList items={talkers} formatValue={fmtBytes}/></div></Card>}
    <div className="toolbar"><input className="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter by host, relay…" aria-label="Filter sessions"/><select value={filter} onChange={(event) => setFilter(event.target.value)} aria-label="Session status"><option value="all">All</option><option value="active">Active only</option><option value="closed">Closed only</option></select><div className="spacer"/><button className="btn btn-sm btn-danger-ghost" disabled={!activeCount} onClick={terminateAll}>Terminate all active</button></div>
    <Card><div className="table-wrap"><table className="data responsive-table sessions-table"><thead><tr><th>Destination</th><th>Proto</th><th>Relay</th><th>↑ Up</th><th>↓ Down</th><th>Duration</th><th>Status</th><th>Actions</th></tr></thead><tbody>
      {rows.map((session) => <SessionRow key={session.id} session={session} terminate={terminate} showTrace={showTrace}/>)}
      {!rows.length && <tr className="empty-row"><td colSpan="8"><Empty>No sessions match.</Empty></td></tr>}
    </tbody></table></div></Card>
    {trace && <Trace session={trace} close={() => setTrace(null)}/>} 
  </>;
}

function SessionRow({ session, terminate, showTrace }) {
  const active = isSessionActive(session);
  return <tr><td data-label="Destination"><span className="mono">{session.destination || session.dst_ip || "—"}</span>{session.fake_ip && <small className="mono faint block">fake {session.fake_ip}</small>}</td><td data-label="Proto"><Tag>{session.protocol?.split(":")[0] || "?"}</Tag></td><td data-label="Relay" className="muted">{shortName(session.relay) || "direct"}</td><td data-label="↑ Up" className="num">{fmtBytes(session.upload_bytes)}</td><td data-label="↓ Down" className="num">{fmtBytes(session.download_bytes)}</td><td data-label="Duration" className="num">{fmtDuration(session.duration_ms)}</td><td data-label="Status"><Pill color={active ? "green" : "gray"}>{active ? "active" : "closed"}</Pill></td><td data-label="Actions"><div className="row-actions"><button className="btn btn-sm btn-ghost" onClick={() => showTrace(session.id)}>Trace</button>{active && <button className="btn btn-sm btn-danger-ghost" onClick={() => terminate(session.id)}>Kill</button>}</div></td></tr>;
}

function Trace({ session, close }) {
  return <Modal title={`Session ${shortName(session.destination) || session.id}`} onClose={close} maxWidth="560px"><dl className="kv"><dt>ID</dt><dd>{session.id}</dd><dt>Destination</dt><dd>{session.destination || "—"}</dd><dt>Relay</dt><dd>{session.relay || "direct"}</dd><dt>Protocol</dt><dd>{session.protocol || "—"}</dd><dt>Uploaded</dt><dd>{fmtBytes(session.upload_bytes)}</dd><dt>Downloaded</dt><dd>{fmtBytes(session.download_bytes)}</dd><dt>Duration</dt><dd>{fmtDuration(session.duration_ms)}</dd></dl><div className="section-title compact">Trace</div><div className="feed trace-feed">{session.trace?.length ? session.trace.map((item,index) => <div className="feed-row" key={index}><span className="t">+{item.offset_ms}ms</span><span className="d">{item.message}</span></div>) : <Empty>No trace recorded.</Empty>}</div></Modal>;
}
