// Application shell: navigation, hash router, auth screen, theme, and the
// shared daemon-status poller that views subscribe to.

import { api, AuthError, setToken, getToken, clearToken } from "./api.js";
import { toastError } from "./ui.js";

const ICONS = {
  overview: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="9"/><rect x="14" y="3" width="7" height="5"/><rect x="14" y="12" width="7" height="9"/><rect x="3" y="16" width="7" height="5"/></svg>',
  relays: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="5" r="2"/><circle cx="5" cy="19" r="2"/><circle cx="19" cy="19" r="2"/><path d="M12 7v4m0 0l-5 6m5-6l5 6"/></svg>',
  sessions: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 7h16M4 12h16M4 17h10"/></svg>',
  dns: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.5 4 5.5 4 9s-1.5 6.5-4 9c-2.5-2.5-4-5.5-4-9s1.5-6.5 4-9z"/></svg>',
  config: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.6 1.6 0 00.3 1.8l.1.1a2 2 0 11-2.8 2.8l-.1-.1a1.6 1.6 0 00-2.7 1.1V21a2 2 0 11-4 0v-.1A1.6 1.6 0 007 19.4a1.6 1.6 0 00-1.8.3l-.1.1a2 2 0 11-2.8-2.8l.1-.1a1.6 1.6 0 00-1.1-2.7H1a2 2 0 110-4h.1A1.6 1.6 0 004.6 7a1.6 1.6 0 00-.3-1.8l-.1-.1a2 2 0 112.8-2.8l.1.1a1.6 1.6 0 001.8.3H9a1.6 1.6 0 001-1.5V1a2 2 0 114 0v.1a1.6 1.6 0 001 1.5 1.6 1.6 0 001.8-.3l.1-.1a2 2 0 112.8 2.8l-.1.1a1.6 1.6 0 00-.3 1.8V9a1.6 1.6 0 001.5 1H23a2 2 0 110 4h-.1a1.6 1.6 0 00-1.5 1z"/></svg>',
  logs: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><path d="M14 2v6h6M9 13h6M9 17h6"/></svg>',
  moon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.8A9 9 0 1111.2 3 7 7 0 0021 12.8z"/></svg>',
  lock: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 018 0v4"/></svg>',
};

const ROUTES = [
  { id: "overview", title: "Overview", subtitle: "Live daemon status and traffic" },
  { id: "relays", title: "Relays", subtitle: "Groups, health, and selection" },
  { id: "sessions", title: "Sessions", subtitle: "Live flows through the tunnel" },
  { id: "dns", title: "DNS", subtitle: "Routing decisions, rules, and cache" },
  { id: "config", title: "Configuration", subtitle: "Runtime settings" },
  { id: "logs", title: "Logs", subtitle: "Live daemon log stream" },
];

const authEl = document.getElementById("auth");
const appEl = document.getElementById("app");

// ---- Theme ----
function initTheme() {
  const saved = localStorage.getItem("punch_theme") || "auto";
  document.documentElement.setAttribute("data-theme", saved);
  const btn = document.getElementById("theme-toggle");
  btn.innerHTML = ICONS.moon;
  btn.onclick = () => {
    const cur = document.documentElement.getAttribute("data-theme");
    const next = cur === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    localStorage.setItem("punch_theme", next);
  };
}

// ---- Auth ----
function showAuth(errorMsg) {
  appEl.hidden = true;
  authEl.hidden = false;
  const err = document.getElementById("auth-error");
  if (errorMsg) { err.textContent = errorMsg; err.hidden = false; }
  const input = document.getElementById("auth-token");
  input.value = getToken();
  input.focus();
}

function hideAuth() {
  authEl.hidden = true;
  appEl.hidden = false;
}

document.getElementById("auth-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const token = document.getElementById("auth-token").value.trim();
  setToken(token);
  await boot();
});

document.getElementById("lock-btn").addEventListener("click", () => {
  clearToken();
  stopPolling();
  showAuth("Token cleared. Enter a token to reconnect.");
});

// ---- Shared status poller ----
const statusListeners = new Set();
let pollTimer = null;
let lastStatus = null;

export function onStatus(fn) {
  statusListeners.add(fn);
  if (lastStatus) fn(lastStatus);
  return () => statusListeners.delete(fn);
}
export function currentStatus() { return lastStatus; }

function setConn(ok, label) {
  const c = document.getElementById("conn-status");
  c.className = "conn-status " + (ok ? "ok" : "err");
  c.querySelector(".conn-label").textContent = label;
}

async function pollOnce() {
  try {
    const st = await api.get("/status");
    lastStatus = st;
    setConn(true, "connected");
    document.getElementById("brand-version").textContent = st.general?.version || "daemon";
    statusListeners.forEach((fn) => { try { fn(st); } catch (e) { console.error(e); } });
  } catch (e) {
    if (e instanceof AuthError) { stopPolling(); showAuth("Token rejected. Please re-enter it."); return; }
    setConn(false, "disconnected");
  }
}

function startPolling() {
  if (pollTimer) return;
  pollOnce();
  pollTimer = setInterval(pollOnce, 2000);
}
function stopPolling() {
  clearInterval(pollTimer);
  pollTimer = null;
}

// ---- Router ----
let currentView = null;

function buildNav() {
  const nav = document.getElementById("nav");
  nav.innerHTML = ROUTES.map((r) =>
    `<a href="#/${r.id}" data-route="${r.id}">${ICONS[r.id]}<span>${r.title}</span></a>`
  ).join("");
}

async function renderRoute() {
  const id = (location.hash.replace(/^#\//, "") || "overview").split("?")[0];
  const route = ROUTES.find((r) => r.id === id) || ROUTES[0];
  document.querySelectorAll("#nav a").forEach((a) =>
    a.classList.toggle("active", a.dataset.route === route.id));
  document.getElementById("view-title").textContent = route.title;
  document.getElementById("view-subtitle").textContent = route.subtitle;

  if (currentView && currentView.unmount) { try { currentView.unmount(); } catch {} }
  const container = document.getElementById("view");
  container.innerHTML = "";
  try {
    const mod = await import(`./views/${route.id}.js`);
    currentView = mod;
    await mod.mount(container);
  } catch (e) {
    console.error(e);
    container.innerHTML = `<div class="empty">Failed to load view: ${e.message}</div>`;
  }
}

// ---- Shutdown ----
document.getElementById("shutdown-btn").addEventListener("click", async () => {
  const { confirmModal } = await import("./ui.js");
  const ok = await confirmModal({
    title: "Shut down daemon?",
    message: "This stops punchd entirely. DNS, TUN routing, and this dashboard will go offline.",
    confirmLabel: "Shut down", danger: true,
  });
  if (!ok) return;
  try { await api.post("/shutdown"); } catch {}
  setConn(false, "shutting down…");
});

// ---- Boot ----
async function boot() {
  try {
    await api.get("/status");
  } catch (e) {
    if (e instanceof AuthError) { showAuth(getToken() ? "Token rejected. Please re-enter it." : ""); return; }
    // Reachable-but-error or unreachable: still show the app; poller will retry.
  }
  hideAuth();
  document.getElementById("lock-btn").hidden = !getToken();
  startPolling();
  if (!location.hash) location.hash = "#/overview";
  renderRoute();
}

window.addEventListener("hashchange", renderRoute);
initTheme();
buildNav();
boot();
