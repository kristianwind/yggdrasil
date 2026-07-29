// Apply the saved/OS theme before first paint to avoid a flash of the wrong one.
//
// This lives in its own file rather than inline in index.html on purpose: the
// panel sends `script-src 'self'` with no 'unsafe-inline', so an inline block is
// refused by the browser and silently never runs — which is exactly what used to
// happen here. Loaded as a blocking script in <head> so it still beats first paint.
(function () {
  try {
    var t = localStorage.getItem("ygg_theme");
    if (t !== "light" && t !== "dark") {
      t = matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    }
    document.documentElement.setAttribute("data-theme", t);
  } catch (e) {}
})();
