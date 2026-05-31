// Thin REST client. Token is kept in localStorage and sent as a Bearer header.
const TOKEN_KEY = "ygg_token";

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || "";
}
export function setToken(t) {
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
}

async function request(method, path, body, opts = {}) {
  const headers = {};
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;

  let fetchBody;
  if (body instanceof FormData) {
    fetchBody = body;
  } else if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    fetchBody = JSON.stringify(body);
  }

  const res = await fetch(`/api${path}`, { method, headers, body: fetchBody });
  if (res.status === 401 && !opts.allow401) {
    setToken("");
    if (location.hash !== "#/login") location.hash = "#/login";
    throw new Error("unauthorized");
  }
  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }
  if (!res.ok) {
    throw new Error((data && data.error) || res.statusText);
  }
  return data;
}

export const api = {
  get: (p, opts) => request("GET", p, undefined, opts),
  post: (p, b, opts) => request("POST", p, b, opts),
  put: (p, b, opts) => request("PUT", p, b, opts),
  del: (p, opts) => request("DELETE", p, undefined, opts),
};

// Build a WebSocket URL for an authenticated stream (token passed as query
// param since browsers can't set headers on WebSocket handshakes).
export function wsURL(path) {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const token = encodeURIComponent(getToken());
  return `${proto}://${location.host}/api${path}?token=${token}`;
}
