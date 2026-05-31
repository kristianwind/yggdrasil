import { writable } from "svelte/store";
import { api, setToken } from "./api.js";

export const user = writable(null); // { id, username, role } or null

export async function loadUser() {
  try {
    const me = await api.get("/auth/me", { allow401: true });
    user.set(me);
    return me;
  } catch {
    user.set(null);
    return null;
  }
}

export async function login(username, password, code) {
  const res = await api.post("/auth/login", { username, password, code }, { allow401: true });
  setToken(res.token);
  await loadUser();
  return res;
}

export async function logout() {
  try {
    await api.post("/auth/logout");
  } catch {
    /* ignore */
  }
  setToken("");
  user.set(null);
  location.hash = "#/login";
}
