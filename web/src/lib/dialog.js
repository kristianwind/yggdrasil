// Themed confirm/prompt dialogs — a promise-based replacement for the native
// window.confirm()/prompt(), which render OS chrome ("site says…") that clashes
// with the app theme and looks broken in a home-screen PWA. A single
// <ConfirmDialog/> mounted in the shell subscribes to this store.
import { writable } from "svelte/store";

export const dialogState = writable(null);

// confirmDialog({ title, body, confirmText?, cancelText?, danger? }) → Promise<boolean>
export function confirmDialog(opts) {
  return new Promise((resolve) => {
    dialogState.set({ kind: "confirm", danger: false, confirmText: "Confirm", cancelText: "Cancel", ...opts, resolve });
  });
}

// promptDialog({ title, body?, label?, value?, placeholder?, confirmText? }) → Promise<string|null>
// Resolves to the entered string, or null if cancelled.
export function promptDialog(opts) {
  return new Promise((resolve) => {
    dialogState.set({ kind: "prompt", confirmText: "OK", cancelText: "Cancel", value: "", ...opts, resolve });
  });
}
