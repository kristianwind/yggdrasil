// Minimal hash router — dependency-free, robust for an embedded PWA.
import { readable } from "svelte/store";

function parse() {
  const hash = location.hash.replace(/^#/, "") || "/";
  const [path, query] = hash.split("?");
  const parts = path.split("/").filter(Boolean);
  return { path, parts, query: new URLSearchParams(query || "") };
}

export const route = readable(parse(), (set) => {
  const handler = () => set(parse());
  window.addEventListener("hashchange", handler);
  return () => window.removeEventListener("hashchange", handler);
});

export function navigate(path) {
  location.hash = path;
}
