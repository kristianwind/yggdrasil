// Minimal service worker: app-shell cache for offline launch + PWA installability.
// API calls and WebSockets are always network (never cached).
// Bump this on every icon/shell change so old caches (and the stale icons they
// hold) are purged on the next visit — otherwise installed PWAs keep serving the
// previously cached icon forever.
const CACHE = "ygg-shell-v3";

self.addEventListener("install", (e) => {
  self.skipWaiting();
  e.waitUntil(
    caches.open(CACHE).then((c) =>
      c.addAll([
        "./",
        "./index.html",
        "./manifest.webmanifest",
        "./icon-192.png?v=3",
        "./icon-512.png?v=3",
      ]),
    ),
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))),
  );
  self.clients.claim();
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (url.pathname.startsWith("/api/") || e.request.method !== "GET") return; // never cache API
  e.respondWith(
    fetch(e.request)
      .then((res) => {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(e.request, copy)).catch(() => {});
        return res;
      })
      .catch(() => caches.match(e.request).then((r) => r || caches.match("./index.html"))),
  );
});
