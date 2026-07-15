import { useEffect, useMemo, useRef, useState } from "react";
import { api, stream } from "../api.js";
import { Card, Empty, useToast } from "../components.jsx";

const MAX = 1000;
export default function Logs() {
  const [lines,setLines] = useState([]), [paused,setPaused] = useState(false), [level,setLevel] = useState("all");
  const pausedRef = useRef(paused), feedRef = useRef(null), toast = useToast();
  useEffect(() => { pausedRef.current = paused; },[paused]);
  useEffect(() => {
    let active = true, controller;
    const start = async () => {
      try { const snapshot = await api.get("/logs"); if (active) setLines((snapshot.entries || []).map((entry) => entry.line).slice(-MAX)); } catch { /* live stream can still work */ }
      if (!active) return;
      controller = stream("/logs/stream",(line) => { if (!pausedRef.current) setLines((current) => [...current,line].slice(-MAX)); },{onError:(error) => toast(error.message,"err","Log stream error")});
    };
    start();
    return () => { active = false; controller?.abort(); };
  },[toast]);
  const shown = useMemo(() => lines.filter((line) => passes(line,level)).reverse(),[lines,level]);
  useEffect(() => { if (!paused && feedRef.current) feedRef.current.scrollTop = 0; },[shown,paused]);
  return <><div className="toolbar"><select value={level} onChange={(event) => setLevel(event.target.value)} aria-label="Minimum log level"><option value="all">All levels</option><option value="error">Error</option><option value="warn">Warn+</option><option value="info">Info+</option></select><div className="spacer"/><button className={`btn btn-sm ${paused ? "btn-primary" : ""}`} onClick={() => setPaused((value) => !value)}>{paused ? "Resume" : "Pause"}</button><button className="btn btn-sm btn-ghost" onClick={() => setLines([])}>Clear</button></div><Card><div className="feed log-feed" ref={feedRef}>{shown.length ? shown.map((line,index) => <div className={`log-line level-${levelOf(line)}`} key={`${index}-${line}`}>{line}</div>) : <Empty>No log lines.</Empty>}</div></Card></>;
}

function levelOf(line) {
  if (/\b(ERROR|ERRO|level=error)\b/i.test(line)) return "error";
  if (/\b(WARN|WARNING|level=warn)\b/i.test(line)) return "warn";
  if (/\b(DEBUG|DEBU|level=debug)\b/i.test(line)) return "debug";
  return "info";
}
function passes(line, level) {
  if (level === "all") return true;
  const rank = {debug:0,info:1,warn:2,error:3}, need = {info:1,warn:2,error:3}[level] || 0;
  return rank[levelOf(line)] >= need;
}
