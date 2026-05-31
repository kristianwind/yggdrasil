import { writable } from "svelte/store";

export const toasts = writable([]);
let id = 0;

export function toast(message, type = "info", ms = 4000) {
  const t = { id: ++id, message, type };
  toasts.update((list) => [...list, t]);
  setTimeout(() => {
    toasts.update((list) => list.filter((x) => x.id !== t.id));
  }, ms);
}
