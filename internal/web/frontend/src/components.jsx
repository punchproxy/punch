import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";

export function Icon({ name }) {
  const common = { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 2, "aria-hidden": true };
  const icons = {
    overview: <><rect x="3" y="3" width="7" height="9"/><rect x="14" y="3" width="7" height="5"/><rect x="14" y="12" width="7" height="9"/><rect x="3" y="16" width="7" height="5"/></>,
    relays: <><circle cx="12" cy="5" r="2"/><circle cx="5" cy="19" r="2"/><circle cx="19" cy="19" r="2"/><path d="M12 7v4m0 0-5 6m5-6 5 6"/></>,
    sessions: <><path d="M4 7h16M4 12h16M4 17h10"/></>,
    dns: <><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.5 4 5.5 4 9s-1.5 6.5-4 9c-2.5-2.5-4-5.5-4-9s1.5-6.5 4-9z"/></>,
    config: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-2.7 1.1v.3a2 2 0 1 1-4 0v-.1A1.6 1.6 0 0 0 7 19.4a1.6 1.6 0 0 0-1.8.3l-.1.1A2 2 0 1 1 2.3 17l.1-.1a1.6 1.6 0 0 0-1.1-2.7H1a2 2 0 1 1 0-4h.1A1.6 1.6 0 0 0 4.6 7a1.6 1.6 0 0 0-.3-1.8l-.1-.1A2 2 0 1 1 7 2.3l.1.1a1.6 1.6 0 0 0 1.8.3H9A1.6 1.6 0 0 0 10 1.2V1a2 2 0 1 1 4 0v.1a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1A2 2 0 1 1 19.7 5l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H23a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1z"/></>,
    logs: <><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M9 13h6M9 17h6"/></>,
    theme: <><path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z"/></>,
    lock: <><rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/></>,
    menu: <><path d="M4 6h16M4 12h16M4 18h16"/></>,
  };
  return <svg {...common}>{icons[name]}</svg>;
}

export const Pill = ({ children, color = "gray", plain = false }) => <span className={`pill ${color}${plain ? " plain" : ""}`}>{children}</span>;
export const Tag = ({ children }) => <span className="tag">{children}</span>;
export const Empty = ({ children }) => <div className="empty">{children}</div>;
export const Card = ({ children, className = "" }) => <section className={`card ${className}`}>{children}</section>;
export function CardHeader({ title, sub, actions }) {
  return <div className="card-head"><h3>{title}</h3>{(sub || actions) && <div className="card-head-aside">{sub && <span className="sub">{sub}</span>}{actions}</div>}</div>;
}
export const StatTile = ({ label, value, detail }) => <Card className="pad stat"><span className="label">{label}</span><span className="value">{value}</span>{detail && <span className="delta">{detail}</span>}</Card>;

const ToastContext = createContext(null);
export function ToastProvider({ children }) {
  const [toasts, setToasts] = useState([]);
  const push = useCallback((message, type = "info", title = "") => {
    const id = crypto.randomUUID?.() || `${Date.now()}-${Math.random()}`;
    setToasts((current) => [...current, { id, message, type, title }]);
    window.setTimeout(() => setToasts((current) => current.filter((toast) => toast.id !== id)), 4500);
  }, []);
  return <ToastContext.Provider value={push}>{children}<div className="toast-stack" aria-live="polite">{toasts.map((toast) => <div className={`toast ${toast.type}`} key={toast.id}>{toast.title && <strong>{toast.title}</strong>}{toast.message}</div>)}</div></ToastContext.Provider>;
}
export const useToast = () => useContext(ToastContext);

const DialogContext = createContext(null);
export function DialogProvider({ children }) {
  const [dialog, setDialog] = useState(null);
  const open = useCallback((config) => new Promise((resolve) => setDialog({ ...config, resolve })), []);
  const close = (value) => {
    dialog?.resolve(value);
    setDialog(null);
  };
  return <DialogContext.Provider value={open}>{children}{dialog && <DialogState config={dialog} close={close} />}</DialogContext.Provider>;
}
export const useDialog = () => useContext(DialogContext);

function DialogState({ config, close }) {
  const [values, setValues] = useState(() => Object.fromEntries((config.fields || []).filter((field) => field.type !== "static").map((field) => [field.name, field.value || ""])));
  const first = useRef(null);
  useEffect(() => first.current?.focus(), []);
  const submit = (event) => {
    event.preventDefault();
    close(config.fields ? Object.fromEntries(Object.entries(values).map(([key, value]) => [key, String(value).trim()])) : true);
  };
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && close(config.fields ? null : false)}>
    <section className="modal" role="dialog" aria-modal="true" aria-labelledby="dialog-title">
      <div className="modal-head" id="dialog-title">{config.title}</div>
      <form onSubmit={submit}>
        <div className="modal-body">
          {config.message && <p className="dialog-message">{config.message}</p>}
          {(config.fields || []).map((field, index) => <label className="field" key={field.name}>
            <span>{field.label}</span>
            {field.type === "static" ? <span className="mono muted">{field.value || "—"}</span> : field.type === "select" ?
              <select ref={index === 0 ? first : null} value={values[field.name]} onChange={(event) => setValues((current) => ({ ...current, [field.name]: event.target.value }))}>{field.options.map((option) => { const value = typeof option === "string" ? option : option.value; return <option value={value} key={value}>{typeof option === "string" ? option : option.label}</option>; })}</select> :
              <input ref={index === 0 ? first : null} type={field.type || "text"} value={values[field.name]} placeholder={field.placeholder || ""} onChange={(event) => setValues((current) => ({ ...current, [field.name]: event.target.value }))} />}
          </label>)}
        </div>
        <div className="modal-foot"><button type="button" className="btn" onClick={() => close(config.fields ? null : false)}>Cancel</button><button className={`btn ${config.danger ? "btn-danger" : "btn-primary"}`} type="submit">{config.confirmLabel || config.submitLabel || "Save"}</button></div>
      </form>
    </section>
  </div>;
}

export function Modal({ title, children, onClose, maxWidth }) {
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
    <section className="modal" role="dialog" aria-modal="true" style={{ maxWidth }}><div className="modal-head">{title}</div><div className="modal-body">{children}</div><div className="modal-foot"><button className="btn" onClick={onClose}>Close</button></div></section>
  </div>;
}

export function usePolling(load, interval) {
  useEffect(() => {
    let active = true;
    let timer;
    let running = false;
    const tick = async () => {
      if (!active || running) return;
      running = true;
      try { await load(); } finally {
        running = false;
        if (active) timer = window.setTimeout(tick, interval);
      }
    };
    tick();
    return () => { active = false; window.clearTimeout(timer); };
  }, [load, interval]);
}

export function ErrorState({ error, fallback = "Loading…" }) {
  return <Empty>{error ? error.message : fallback}</Empty>;
}
