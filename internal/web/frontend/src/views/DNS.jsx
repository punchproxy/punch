import { useCallback, useEffect, useMemo, useState } from "react";
import { api, stream } from "../api.js";
import { Card, Empty, ErrorState, Pill, Tag, useDialog, usePolling, useToast } from "../components.jsx";
import { cacheStateColor, fmtLatency, fmtNum, fmtTime, timeAgo } from "../utils.js";

const tabs = [["live","Live queries"],["rules","Domain rules"],["routes","CIDR routes"],["upstreams","Upstreams"],["cache","Cache"],["fakeips","Fake IPs"]];

export default function DNS() {
  const [tab, setTab] = useState("live");
  return <><div className="tabs-scroll"><div className="btn-row dns-tabs" role="tablist">{tabs.map(([id,label]) => <button role="tab" aria-selected={tab===id} className={`btn btn-sm ${tab===id ? "btn-primary" : "btn-ghost"}`} onClick={() => setTab(id)} key={id}>{label}</button>)}</div></div><div className="mt">{tab === "live" ? <Live/> : tab === "rules" || tab === "routes" ? <Rules kind={tab}/> : tab === "upstreams" ? <Upstreams/> : tab === "cache" ? <Cache/> : <FakeIPs/>}</div></>;
}

function Live() {
  const [feed, setFeed] = useState([]), [error, setError] = useState(null);
  const toast = useToast();
  useEffect(() => {
    const controller = stream("/dns/queries/stream", (query) => setFeed((current) => [query, ...current].slice(0,300)), {parseJSON:true,onError:setError});
    return () => controller.abort();
  }, []);
  useEffect(() => { if (error) toast(error.message,"err","Stream error"); }, [error, toast]);
  return <Card><div className="card-head"><h3>Live DNS decisions</h3><div className="flex"><span className="sub">{feed.length ? `${feed.length} shown` : "streaming…"}</span><button className="btn btn-sm btn-ghost" onClick={() => setFeed([])}>Clear</button></div></div><div className="feed dns-feed">{feed.length ? feed.map((query,index) => <div className="feed-row" key={`${query.time}-${query.domain}-${index}`}><span className="t">{fmtTime(query.time)}</span><span className={`decision ${decisionClass(query.decision)}`}>{query.decision}</span><span className="d" title={query.domain}>{query.domain} <span className="faint">{query.qtype || ""}</span></span><span className="meta">{query.cached && <><Tag>cache</Tag>{" "}</>}{query.rule && <><Tag>{query.rule}</Tag>{" "}</>}{query.latency_ms ? fmtLatency(query.latency_ms) : ""}</span></div>) : <Empty>Waiting for queries…</Empty>}</div></Card>;
}

const decisionClass = (decision) => ({relay:"relay",direct:"direct",reject:"reject"})[String(decision).toLowerCase()] || "";

function Rules({ kind }) {
  const path = kind === "rules" ? "/dns/rules" : "/dns/routes", decisions = kind === "rules" ? ["relay","direct","reject"] : ["direct","reject"];
  const [rows, setRows] = useState([]), [search, setSearch] = useState(""), [error, setError] = useState(null);
  const toast = useToast(), dialog = useDialog();
  const load = useCallback(async () => { try { setRows(await api.get(path) || []); setError(null); } catch (next) { setError(next); } }, [path]);
  useEffect(() => { load(); }, [load]);
  const act = async (fn, message) => { try { await fn(); toast(message,"ok"); await load(); } catch (next) { toast(next.message,"err","Error"); } };
  const add = async () => {
    const values = await dialog({title:`Add ${kind === "rules" ? "domain rule" : "CIDR route"}`,submitLabel:"Add",fields:[{name:"decision",label:"Decision",type:"select",options:decisions,value:decisions[0]},{name:"source",label:kind === "rules" ? "Domain / suffix / URL / file" : "CIDR / URL / file",placeholder:kind === "rules" ? "example.com or +.example.com" : "10.0.0.0/8"}]});
    if (values?.source) act(() => api.post(path,values),"Added");
  };
  const remove = async (row) => { if (await dialog({title:"Delete entry?",message:row.source,confirmLabel:"Delete",danger:true})) act(() => api.del(`${path}?index=${row.index}`),"Deleted"); };
  const filtered = rows.filter((row) => !search.trim() || (row.source || "").toLowerCase().includes(search.trim().toLowerCase()));
  if (error && !rows.length) return <ErrorState error={error}/>;
  return <><div className="toolbar"><input className="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter…" aria-label={`Filter ${kind}`}/><div className="spacer"/><button className="btn btn-sm" onClick={() => act(() => api.post(`${path}/refresh?all=true`),"Remote lists refreshed")}>Refresh remote lists</button><button className="btn btn-sm btn-primary" onClick={add}>Add {kind === "rules" ? "rule" : "route"}</button></div>
    <Card><div className="table-wrap"><table className="data responsive-table rules-table"><thead><tr><th>#</th><th>Decision</th><th>Source</th><th>Type</th><th>Entries</th><th>Hits</th><th>Updated</th><th>Actions</th></tr></thead><tbody>{filtered.map((row) => <RuleRow key={`${row.index}-${row.source}`} row={row} path={path} act={act} remove={remove}/>)}{!filtered.length && <tr className="empty-row"><td colSpan="8"><Empty>No entries.</Empty></td></tr>}</tbody></table></div></Card></>;
}

function RuleRow({ row, path, act, remove }) {
  const color = {relay:"orange",direct:"blue",reject:"red"}[row.decision] || "gray", remote = /^https?:\/\//.test(row.source || "");
  return <tr><td data-label="#" className="mono faint">{row.index}</td><td data-label="Decision"><Pill color={color}>{row.decision}</Pill></td><td data-label="Source" className="mono source-cell">{row.source} {row.default && <Tag>default</Tag>}</td><td data-label="Type"><Tag>{row.type || ""}</Tag></td><td data-label="Entries" className="num">{row.count ? fmtNum(row.count) : "—"}</td><td data-label="Hits" className="num">{row.hits ? fmtNum(row.hits) : "—"}</td><td data-label="Updated" className="faint nowrap">{row.last_updated ? timeAgo(row.last_updated) : "—"}</td><td data-label="Actions"><div className="row-actions">{!row.default && <>{row.index > 0 && <button className="btn btn-sm btn-ghost" title="Move up" onClick={() => act(() => api.post(`${path}/move?index=${row.index}`,{index:row.index-1}),"Reordered")}>↑</button>}{remote && <button className="btn btn-sm btn-ghost" onClick={() => act(() => api.post(`${path}/refresh?index=${row.index}`),"Refreshed")}>Refresh</button>}<button className="btn btn-sm btn-danger-ghost" onClick={() => remove(row)}>Delete</button></>}</div></td></tr>;
}

function Upstreams() {
  const [rows, setRows] = useState([]), [error, setError] = useState(null);
  const toast = useToast(), dialog = useDialog();
  const load = useCallback(async () => { try { setRows(await api.get("/dns/upstreams") || []); setError(null); } catch (next) { setError(next); } }, []);
  useEffect(() => { load(); }, [load]);
  const act = async (fn,message) => { try { await fn(); toast(message,"ok"); await load(); } catch (next) { toast(next.message,"err","Error"); } };
  const edit = async (existing) => {
    const values = await dialog({title:existing ? "Edit upstream" : "Add upstream",fields:[{name:"url",label:"URL",value:existing?.url || "",placeholder:"https://dns.example/dns-query"},{name:"bootstrap",label:"Bootstrap IP (optional)",value:existing?.bootstrap || "",placeholder:"223.5.5.5"},{name:"domains",label:"Scoped domains (comma-separated, optional)",value:(existing?.domains || []).join(", ")}]});
    if (!values?.url) return;
    const payload = {...values,domains:values.domains ? values.domains.split(",").map((item) => item.trim()).filter(Boolean) : []};
    act(() => existing ? api.put("/dns/upstreams",payload) : api.post("/dns/upstreams",payload),"Saved");
  };
  const remove = async (row) => { if (await dialog({title:"Delete upstream?",message:row.url,confirmLabel:"Delete",danger:true})) act(() => api.del(`/dns/upstreams?url=${encodeURIComponent(row.url)}`),"Deleted"); };
  if (error && !rows.length) return <ErrorState error={error}/>;
  return <><div className="toolbar"><div className="spacer"/><button className="btn btn-sm btn-primary" onClick={() => edit(null)}>Add upstream</button></div><Card><div className="table-wrap"><table className="data responsive-table upstreams-table"><thead><tr><th>URL</th><th>Bootstrap</th><th>Domains</th><th>Queries</th><th>Avg latency</th><th>Last</th><th>Actions</th></tr></thead><tbody>{rows.map((row) => <tr key={row.url}><td data-label="URL" className="mono">{row.url}</td><td data-label="Bootstrap" className="mono faint">{row.bootstrap || "—"}</td><td data-label="Domains">{row.domains?.length ? row.domains.map((domain) => <Tag key={domain}>{domain}</Tag>) : <span className="faint">all</span>}</td><td data-label="Queries" className="num">{fmtNum(row.queries)}</td><td data-label="Avg latency" className="num">{fmtLatency(row.average_latency_ms)}</td><td data-label="Last" className="faint nowrap">{row.last_queried_at ? timeAgo(row.last_queried_at) : "—"}</td><td data-label="Actions"><div className="row-actions"><button className="btn btn-sm btn-ghost" onClick={() => edit(row)}>Edit</button><button className="btn btn-sm btn-danger-ghost" onClick={() => remove(row)}>Delete</button></div></td></tr>)}{!rows.length && <tr className="empty-row"><td colSpan="7"><Empty>No upstreams.</Empty></td></tr>}</tbody></table></div></Card></>;
}

function Cache() {
  const [rows, setRows] = useState([]), [search, setSearch] = useState(""), [error, setError] = useState(null);
  const toast = useToast(), dialog = useDialog();
  const load = useCallback(async () => { try { setRows(await api.get("/dns/cache") || []); setError(null); } catch (next) { setError(next); } }, []);
  usePolling(load,5000);
  const filtered = useMemo(() => rows.filter((row) => !search.trim() || `${row.name || ""} ${row.result || ""}`.toLowerCase().includes(search.trim().toLowerCase())).slice(0,500), [rows,search]);
  const flush = async () => { if (!await dialog({title:"Flush DNS cache?",message:"All cached answers will be dropped.",confirmLabel:"Flush",danger:true})) return; try { await api.del("/dns/cache"); toast("Cache flushed","ok"); load(); } catch (next) { toast(next.message,"err","Error"); } };
  if (error && !rows.length) return <ErrorState error={error}/>;
  return <><div className="toolbar"><input className="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter…" aria-label="Filter DNS cache"/><span className="muted">{fmtNum(rows.length)} entries</span><div className="spacer"/><button className="btn btn-sm btn-danger-ghost" onClick={flush}>Flush cache</button></div><Card><div className="table-wrap"><table className="data responsive-table cache-table"><thead><tr><th>Name</th><th>Type</th><th>Result</th><th>Upstream</th><th>State</th><th>Expires</th></tr></thead><tbody>{filtered.map((row,index) => <tr key={`${row.name}-${row.qtype}-${index}`}><td data-label="Name" className="mono">{row.name}</td><td data-label="Type"><Tag>{row.qtype}</Tag></td><td data-label="Result" className="mono result-cell">{row.result}</td><td data-label="Upstream" className="mono faint">{row.upstream || "—"}</td><td data-label="State"><Pill color={cacheStateColor(row.state)}>{row.state}</Pill></td><td data-label="Expires" className="faint nowrap">{timeAgo(row.expires_at)}</td></tr>)}{!filtered.length && <tr className="empty-row"><td colSpan="6"><Empty>Cache is empty.</Empty></td></tr>}</tbody></table></div></Card></>;
}

function FakeIPs() {
  const [rows,setRows] = useState([]), [search,setSearch] = useState(""), [error,setError] = useState(null);
  const load = useCallback(async () => { try { setRows(await api.get("/dns/fakeips") || []); setError(null); } catch (next) { setError(next); } }, []);
  usePolling(load,5000);
  const filtered = useMemo(() => rows.filter((row) => !search.trim() || `${row.domain || ""} ${row.fake_ip || ""}`.toLowerCase().includes(search.trim().toLowerCase())).slice(0,500),[rows,search]);
  if (error && !rows.length) return <ErrorState error={error}/>;
  return <><div className="toolbar"><input className="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter…" aria-label="Filter fake IPs"/><span className="muted">{fmtNum(rows.length)} mappings · {fmtNum(rows.filter((row) => row.state === "active").length)} active</span></div><Card><div className="table-wrap"><table className="data responsive-table fakeips-table"><thead><tr><th>Fake IP</th><th>Domain</th><th>State</th><th>Sessions</th><th>Expires</th></tr></thead><tbody>{filtered.map((row) => <tr key={row.fake_ip}><td data-label="Fake IP" className="mono">{row.fake_ip}</td><td data-label="Domain" className="mono">{row.domain}</td><td data-label="State"><Pill color={row.state === "active" ? "green" : "gray"}>{row.state}</Pill></td><td data-label="Sessions" className="num">{row.session_ids?.length || 0}</td><td data-label="Expires" className="faint nowrap">{timeAgo(row.expires_at)}</td></tr>)}{!filtered.length && <tr className="empty-row"><td colSpan="5"><Empty>No fake IPs allocated.</Empty></td></tr>}</tbody></table></div></Card></>;
}
