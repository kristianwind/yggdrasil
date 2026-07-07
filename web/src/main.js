import "./app.css";
import { mount } from "svelte";
import App from "./App.svelte";
import { initTheme } from "./lib/theme.js";

initTheme(); // reconcile the inline data-theme + set the matching meta theme-color

const app = mount(App, { target: document.getElementById("app") });

// Register the PWA service worker. The browser only honors this in a secure
// context (HTTPS or localhost); it fails silently otherwise.
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("./sw.js").catch(() => {});
  });
}

export default app;
