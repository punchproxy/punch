import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "../api.js";
import { Card, Empty, ErrorState, useDialog, useToast } from "../components.jsx";

export default function Config() {
  const [entries,setEntries] = useState([]), [search,setSearch] = useState(""), [error,setError] = useState(null);
  const toast = useToast(), dialog = useDialog();
  const load = useCallback(async () => { try { setEntries(await api.get("/config") || []); setError(null); } catch (next) { setError(next); } }, []);
  useEffect(() => { load(); }, [load]);
  const groups = useMemo(() => entries.reduce((result,entry) => {
    if (search.trim() && !`${entry.key || ""} ${entry.value || ""}`.toLowerCase().includes(search.trim().toLowerCase())) return result;
    const section = entry.key.includes(".") ? entry.key.split(".")[0] : "general";
    (result[section] ||= []).push(entry); return result;
  },{}),[entries,search]);
  const edit = async (entry) => {
    const secret = /secret|token|password/i.test(entry.key);
    const values = await dialog({title:`Set ${entry.key}`,fields:[{name:"key",label:"Key",type:"static",value:entry.key},{name:"value",label:"Value",type:secret ? "password" : "text",value:secret ? "" : entry.value || ""}]});
    if (!values) return;
    try { await api.put(`/config/${encodeURIComponent(entry.key)}`,{value:values.value}); toast(`Updated ${entry.key}`,"ok"); load(); } catch (next) { toast(next.message,"err","Error"); }
  };
  if (error && !entries.length) return <ErrorState error={error}/>;
  const sections = Object.keys(groups).sort();
  return <><div className="toolbar"><input className="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter keys…" aria-label="Filter configuration"/><div className="spacer"/><span className="faint">{entries.length} keys</span></div>{sections.map((section) => <section key={section}><div className="section-title">{section}</div><Card><div className="table-wrap"><table className="data responsive-table config-table"><tbody>{groups[section].map((entry) => { const secret = /secret|token|password/i.test(entry.key); return <tr key={entry.key}><td data-label="Key" className="mono config-key">{entry.key}</td><td data-label="Value" className="mono muted">{secret && entry.value ? "••••••••" : entry.value || <span className="faint">—</span>}</td><td data-label="Actions"><div className="row-actions"><button className="btn btn-sm btn-ghost" onClick={() => edit(entry)}>Edit</button></div></td></tr>; })}</tbody></table></div></Card></section>)}{!sections.length && <Empty>No keys match.</Empty>}</>;
}
