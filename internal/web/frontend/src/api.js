const TOKEN_KEY = "punch_token";
const BASE = "/api";

export class AuthError extends Error {}
export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

export const getToken = () => localStorage.getItem(TOKEN_KEY) || "";
export function setToken(token) {
  if (token) localStorage.setItem(TOKEN_KEY, token);
  else localStorage.removeItem(TOKEN_KEY);
}
export const clearToken = () => setToken("");

function authHeaders() {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

function unauthorized() {
  window.dispatchEvent(new Event("punch:unauthorized"));
  return new AuthError("unauthorized");
}

async function request(method, path, body, signal) {
  const options = { method, signal, headers: authHeaders() };
  if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  let response;
  try {
    response = await fetch(BASE + path, options);
  } catch (error) {
    if (error.name === "AbortError") throw error;
    throw new ApiError(`cannot reach daemon: ${error.message}`, 0);
  }
  if (response.status === 401) throw unauthorized();
  const text = await response.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = text; }
  }
  if (!response.ok) {
    const message = data?.error || (typeof data === "string" && data) || `HTTP ${response.status}`;
    throw new ApiError(message, response.status);
  }
  return data;
}

export const api = {
  get: (path, signal) => request("GET", path, undefined, signal),
  post: (path, body) => request("POST", path, body),
  put: (path, body) => request("PUT", path, body),
  del: (path, body) => request("DELETE", path, body),
};

export function stream(path, onLine, { parseJSON = false, onError } = {}) {
  const controller = new AbortController();
  (async () => {
    let response;
    try {
      response = await fetch(BASE + path, { headers: authHeaders(), signal: controller.signal });
      if (response.status === 401) throw unauthorized();
      if (!response.ok || !response.body) throw new ApiError(`HTTP ${response.status}`, response.status);
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      const emit = (line) => {
        line = line.trim();
        if (!line) return;
        if (!parseJSON) return onLine(line);
        try { onLine(JSON.parse(line)); } catch { /* ignore malformed stream records */ }
      };
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let newline;
        while ((newline = buffer.indexOf("\n")) >= 0) {
          emit(buffer.slice(0, newline));
          buffer = buffer.slice(newline + 1);
        }
      }
      buffer += decoder.decode();
      emit(buffer);
    } catch (error) {
      if (!controller.signal.aborted) onError?.(error);
    }
  })();
  return controller;
}
