// Light/dark theme. Persisted in localStorage; defaults to the OS preference.
// The initial value is also applied by a tiny inline script in index.html so
// there's no flash of the wrong theme before the app boots.
const KEY = "ygg_theme";

export function getTheme() {
  return document.documentElement.getAttribute("data-theme") === "light" ? "light" : "dark";
}

export function applyTheme(theme) {
  document.documentElement.setAttribute("data-theme", theme);
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", theme === "light" ? "#ffffff" : "#0b0f14");
}

export function initTheme() {
  let t = null;
  try {
    t = localStorage.getItem(KEY);
  } catch {
    /* private mode */
  }
  if (t !== "light" && t !== "dark") {
    t = window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  }
  applyTheme(t);
  return t;
}

export function toggleTheme() {
  const next = getTheme() === "light" ? "dark" : "light";
  try {
    localStorage.setItem(KEY, next);
  } catch {
    /* ignore */
  }
  applyTheme(next);
  return next;
}
