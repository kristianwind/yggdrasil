package api

// statusPageHTML is the self-contained public status board served at /status.
// No SPA, no auth, no external assets — it fetches /api/status and renders a
// simple up/down grid, refreshing every 30s. Kept deliberately tiny and
// dependency-free so it loads instantly and works even if the panel UI doesn't.
const statusPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Server Status</title>
<style>
  :root {
    --bg:#0f1216; --card:#171b21; --border:#242a33; --text:#e6e9ef;
    --muted:#8b95a5; --online:#3fb950; --starting:#d29922; --offline:#6e7681;
  }
  * { box-sizing:border-box; }
  body {
    margin:0; background:var(--bg); color:var(--text);
    font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
    padding:2.5rem 1rem;
  }
  .wrap { max-width:760px; margin:0 auto; }
  h1 { font-size:1.7rem; font-weight:650; margin:0 0 .35rem; letter-spacing:-.01em; }
  .sub { color:var(--muted); font-size:.85rem; margin:0 0 1.75rem; }
  .grid { display:grid; gap:.7rem; }
  .card {
    display:flex; align-items:center; gap:.9rem;
    background:var(--card); border:1px solid var(--border); border-radius:12px;
    padding:.9rem 1.1rem;
  }
  .dot { width:11px; height:11px; border-radius:50%; flex:0 0 auto; box-shadow:0 0 0 4px rgba(255,255,255,.03); }
  .dot.online { background:var(--online); box-shadow:0 0 0 4px rgba(63,185,80,.15); }
  .dot.starting { background:var(--starting); box-shadow:0 0 0 4px rgba(210,153,34,.15); animation:pulse 1.4s ease-in-out infinite; }
  .dot.offline { background:var(--offline); }
  @keyframes pulse { 0%,100%{opacity:1} 50%{opacity:.35} }
  .name { font-weight:600; }
  .game { color:var(--muted); font-size:.8rem; }
  .meta { margin-left:auto; text-align:right; }
  .state { font-size:.8rem; font-weight:600; text-transform:capitalize; }
  .state.online { color:var(--online); }
  .state.starting { color:var(--starting); }
  .state.offline { color:var(--offline); }
  .players { color:var(--muted); font-size:.78rem; }
  .empty, .foot { color:var(--muted); font-size:.8rem; text-align:center; }
  .empty { padding:2.5rem 0; }
  .foot { margin-top:1.75rem; }
  .foot a { color:var(--muted); }
</style>
</head>
<body>
<div class="wrap">
  <h1 id="title">Server Status</h1>
  <p class="sub" id="sub">Loading…</p>
  <div class="grid" id="grid"></div>
  <p class="foot">Powered by <a href="https://github.com/kristianwind/yggdrasil" rel="noopener">Yggdrasil Panel</a></p>
</div>
<script src="/status.js"></script>
</body>
</html>`

// statusPageJS is served as an external asset (script-src 'self') because the
// panel's strict CSP forbids inline scripts. It fetches /api/status and renders.
const statusPageJS = `
  var esc = function (s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;" }[c];
    });
  };
  function render(data) {
    document.getElementById("title").textContent = data.title || "Server Status";
    document.title = data.title || "Server Status";
    var grid = document.getElementById("grid");
    var servers = data.servers || [];
    if (!servers.length) {
      grid.innerHTML = '<p class="empty">No servers are being shared right now.</p>';
    } else {
      grid.innerHTML = servers.map(function (s) {
        var st = s.status || "offline";
        var players = "";
        if (st === "online" && s.players != null) {
          players = '<div class="players">' + s.players + ' online</div>';
        }
        var game = s.game ? '<div class="game">' + esc(s.game) + '</div>' : '';
        return '<div class="card">' +
          '<span class="dot ' + st + '"></span>' +
          '<div><div class="name">' + esc(s.name) + '</div>' + game + '</div>' +
          '<div class="meta"><div class="state ' + st + '">' + st + '</div>' + players + '</div>' +
        '</div>';
      }).join("");
    }
    var up = servers.filter(function (s) { return s.status === "online"; }).length;
    document.getElementById("sub").textContent =
      servers.length ? (up + " of " + servers.length + " online · updated just now") : "";
  }
  function load() {
    fetch("/api/status", { cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(render)
      .catch(function () { document.getElementById("sub").textContent = "Status is currently unavailable."; });
  }
  load();
  setInterval(load, 30000);
`
