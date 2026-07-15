import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { api, AuthError, clearToken, getToken, setToken } from "./api.js";
import { DialogProvider, Icon, ToastProvider, useDialog } from "./components.jsx";
import Overview from "./views/Overview.jsx";
import Relays from "./views/Relays.jsx";
import Sessions from "./views/Sessions.jsx";
import DNS from "./views/DNS.jsx";
import Config from "./views/Config.jsx";
import Logs from "./views/Logs.jsx";

const routes = [
  { id: "overview", title: "Overview", subtitle: "Live daemon status and traffic", component: Overview },
  { id: "relays", title: "Relays", subtitle: "Groups, health, and selection", component: Relays },
  { id: "sessions", title: "Sessions", subtitle: "Live flows through the tunnel", component: Sessions },
  { id: "dns", title: "DNS", subtitle: "Routing decisions, rules, and cache", component: DNS },
  { id: "config", title: "Configuration", subtitle: "Runtime settings", component: Config },
  { id: "logs", title: "Logs", subtitle: "Live daemon log stream", component: Logs },
];

const StatusContext = createContext(null);
export const useStatus = () => useContext(StatusContext);

function currentRoute() {
  const id = window.location.hash.replace(/^#\//, "").split("?")[0] || "overview";
  return routes.find((route) => route.id === id) || routes[0];
}

function Dashboard() {
  const [route, setRoute] = useState(currentRoute);
  const [status, setStatus] = useState(null);
  const [connected, setConnected] = useState(false);
  const [authVisible, setAuthVisible] = useState(false);
  const [authError, setAuthError] = useState("");
  const [theme, setTheme] = useState(() => localStorage.getItem("punch_theme") || "auto");
  const dialog = useDialog();

  const showAuth = useCallback((message = "") => {
    setAuthError(message);
    setAuthVisible(true);
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("punch_theme", theme);
  }, [theme]);

  useEffect(() => {
    const onHash = () => setRoute(currentRoute());
    if (!window.location.hash) window.location.hash = "#/overview";
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    const unauthorized = () => showAuth(getToken() ? "Token rejected. Please re-enter it." : "");
    window.addEventListener("punch:unauthorized", unauthorized);
    return () => window.removeEventListener("punch:unauthorized", unauthorized);
  }, [showAuth]);

  useEffect(() => {
    if (authVisible) return;
    let active = true, timer;
    const poll = async () => {
      try {
        const next = await api.get("/status");
        if (active) { setStatus(next); setConnected(true); }
      } catch (error) {
        if (error instanceof AuthError) return;
        if (active) setConnected(false);
      } finally {
        if (active) timer = window.setTimeout(poll, 2000);
      }
    };
    poll();
    return () => { active = false; window.clearTimeout(timer); };
  }, [authVisible]);

  const submitToken = async (event) => {
    event.preventDefault();
    const token = new FormData(event.currentTarget).get("token").trim();
    setToken(token);
    try {
      const next = await api.get("/status");
      setStatus(next); setConnected(true); setAuthVisible(false); setAuthError("");
    } catch (error) {
      if (!(error instanceof AuthError)) setAuthError(error.message);
    }
  };

  const lock = () => {
    clearToken();
    setConnected(false);
    showAuth("Token cleared. Enter a token to reconnect.");
  };

  const shutdown = async () => {
    const confirmed = await dialog({ title: "Shut down daemon?", message: "This stops punchd entirely. DNS, TUN routing, and this dashboard will go offline.", confirmLabel: "Shut down", danger: true });
    if (!confirmed) return;
    try { await api.post("/shutdown"); } catch { /* the daemon may close the connection */ }
    setConnected(false);
  };

  const RouteComponent = route.component;
  const contextValue = useMemo(() => status, [status]);
  const cycleTheme = () => setTheme((current) => current === "auto" ? "light" : current === "light" ? "dark" : "auto");

  return <StatusContext.Provider value={contextValue}>
    {authVisible && <div className="auth-overlay">
      <form className="auth-card" onSubmit={submitToken}>
        <Brand version="secure daemon" />
        <h1>Authentication required</h1>
        <p>This daemon is protected by an API token. It stays only in this browser.</p>
        <label className="field"><span>API token</span><input autoFocus name="token" type="password" autoComplete="off" spellCheck="false" placeholder="api.secret value" defaultValue={getToken()} /></label>
        {authError && <p className="auth-error" role="alert">{authError}</p>}
        <button type="submit" className="btn btn-primary btn-block">Unlock dashboard</button>
      </form>
    </div>}

    <div className="app-shell">
      <aside className="sidebar">
        <Brand version={status?.general?.version || "daemon"} />
        <Navigation route={route} />
        <div className="sidebar-foot">
          <button className="icon-btn" onClick={cycleTheme} title={`Theme: ${theme}`} aria-label={`Change theme, currently ${theme}`}><Icon name="theme" /></button>
          {getToken() && <button className="icon-btn" onClick={lock} title="Clear token" aria-label="Clear API token"><Icon name="lock" /></button>}
        </div>
      </aside>
      <main className="main">
        <header className="topbar">
          <div className="mobile-brand"><span className="logo" aria-hidden="true"/><strong>Punch</strong></div>
          <div className="topbar-title"><h1>{route.title}</h1><span>{route.subtitle}</span></div>
          <div className="topbar-actions">
            <span className={`conn-status ${connected ? "ok" : "err"}`} title="Daemon connection"><span className="dot"/><span className="conn-label">{connected ? "connected" : "disconnected"}</span></span>
            <button className="btn btn-sm btn-danger-ghost" onClick={shutdown}>Shutdown</button>
          </div>
        </header>
        <div className="view"><RouteComponent /></div>
      </main>
      <nav className="mobile-nav" aria-label="Dashboard navigation"><Navigation route={route} /></nav>
    </div>
  </StatusContext.Provider>;
}

function Brand({ version }) {
  return <div className="sidebar-brand"><span className="logo" aria-hidden="true"/><div className="sidebar-brand-text"><strong>Punch</strong><small>{version}</small></div></div>;
}

function Navigation({ route }) {
  return <div className="nav">{routes.map((item) => <a href={`#/${item.id}`} className={route.id === item.id ? "active" : ""} key={item.id} aria-current={route.id === item.id ? "page" : undefined}><Icon name={item.id}/><span>{item.title === "Configuration" ? "Config" : item.title}</span></a>)}</div>;
}

export default function App() {
  return <ToastProvider><DialogProvider><Dashboard /></DialogProvider></ToastProvider>;
}
