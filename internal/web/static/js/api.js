// API client for the punchd HTTP API. Handles the optional bearer token
// (stored in localStorage) and surfaces 401s so the shell can show the
// authentication screen.

const TOKEN_KEY = "punch_token";
const BASE = "/api";

export class AuthError extends Error {}
export class ApiError extends Error {
  constructor(message, status) { super(message); this.status = status; }
}

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || "";
}
export function setToken(t) {
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
}
export function clearToken() { setToken(""); }

function authHeaders() {
  const t = getToken();
  return t ? { Authorization: "Bearer " + t } : {};
}

async function request(method, path, body) {
  const opts = { method, headers: { ...authHeaders() } };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let resp;
  try {
    resp = await fetch(BASE + path, opts);
  } catch (e) {
    throw new ApiError("cannot reach daemon: " + e.message, 0);
  }
  if (resp.status === 401) throw new AuthError("unauthorized");
  const text = await resp.text();
  let data = null;
  if (text) { try { data = JSON.parse(text); } catch { data = text; } }
  if (!resp.ok) {
    const msg = (data && data.error) || (typeof data === "string" && data) || `HTTP ${resp.status}`;
    throw new ApiError(msg, resp.status);
  }
  return data;
}

export const api = {
  get: (p) => request("GET", p),
  post: (p, b) => request("POST", p, b),
  put: (p, b) => request("PUT", p, b),
  del: (p, b) => request("DELETE", p, b),
};

// Stream newline-delimited responses (NDJSON or plain lines). Returns an
// AbortController; onLine is called for each non-empty line. parseJSON=true
// parses each line as JSON.
export function stream(path, onLine, { parseJSON = false, onError } = {}) {
  const controller = new AbortController();
  (async () => {
    let resp;
    try {
      resp = await fetch(BASE + path, { headers: authHeaders(), signal: controller.signal });
    } catch (e) {
      if (!controller.signal.aborted && onError) onError(new ApiError(e.message, 0));
      return;
    }
    if (resp.status === 401) { if (onError) onError(new AuthError("unauthorized")); return; }
    if (!resp.ok || !resp.body) { if (onError) onError(new ApiError(`HTTP ${resp.status}`, resp.status)); return; }
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let nl;
        while ((nl = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, nl).trim();
          buf = buf.slice(nl + 1);
          if (!line) continue;
          if (parseJSON) { try { onLine(JSON.parse(line)); } catch {} }
          else onLine(line);
        }
      }
    } catch (e) {
      if (!controller.signal.aborted && onError) onError(new ApiError(e.message, 0));
    }
  })();
  return controller;
}
