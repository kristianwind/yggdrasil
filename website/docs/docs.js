(function () {
  // ---- search ----
  var q = document.getElementById("q"), res = document.getElementById("res"), idx = null, sel = -1;

  function load() {
    if (idx) return Promise.resolve(idx);
    return fetch("search.json").then(function (r) { return r.json(); }).then(function (j) { idx = j; return j; });
  }
  function esc(s) { return s.replace(/[&<>"]/g, function (c) { return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c]; }); }
  function mark(text, term) {
    var i = text.toLowerCase().indexOf(term);
    if (i < 0) return esc(text.slice(0, 140));
    var from = Math.max(0, i - 45), s = (from ? "…" : "") + text.slice(from, i);
    return esc(s) + "<mark>" + esc(text.substr(i, term.length)) + "</mark>" + esc(text.substr(i + term.length, 90)) + "…";
  }
  function render(hits, term) {
    if (!hits.length) { res.innerHTML = '<div class="none">Nothing matches “' + esc(term) + '”.</div>'; res.hidden = false; return; }
    res.innerHTML = hits.map(function (h) {
      // Show the section as the headline and the page as the breadcrumb, since
      // the section is what the reader actually wants to land on.
      var parts = h.t.split(" › "), page = parts[0], section = parts[1] || "";
      return '<a href="' + h.u + '"><div class="rs">' + esc(h.s) + " · " + esc(page) + '</div>' +
             '<div class="rt">' + esc(section || page) + '</div>' +
             '<div class="rb">' + mark(h.b, term) + '</div></a>';
    }).join("");
    res.hidden = false; sel = -1;
  }
  function search(term) {
    term = term.trim().toLowerCase();
    if (term.length < 2) { res.hidden = true; return; }
    load().then(function (all) {
      var hits = [];
      all.forEach(function (p) {
        // The record's title is "Page › Section"; a match in the section name is
        // a much stronger signal than one buried in its prose, and a whole-word
        // hit beats a substring ("ports" shouldn't rank on "passports").
        var t = p.t.toLowerCase(), b = p.b.toLowerCase(), score = 0;
        var word = new RegExp("\\b" + term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
        if (t.indexOf(term) >= 0) score += word.test(t) ? 24 : 12;
        if (b.indexOf(term) >= 0) score += word.test(b) ? 4 : 1;
        // A short section that mentions the term is usually more on-topic than a
        // long one that mentions it once in passing.
        if (score && b.length < 900) score += 1;
        if (score) hits.push({ p: p, score: score });
      });
      hits.sort(function (a, b) { return b.score - a.score; });
      render(hits.slice(0, 8).map(function (h) { return h.p; }), term);
    });
  }
  if (q) {
    q.addEventListener("input", function () { search(q.value); });
    q.addEventListener("keydown", function (e) {
      var items = res.querySelectorAll("a");
      if (e.key === "Escape") { res.hidden = true; q.blur(); return; }
      if (!items.length) return;
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        sel += e.key === "ArrowDown" ? 1 : -1;
        if (sel < 0) sel = items.length - 1;
        if (sel >= items.length) sel = 0;
        items.forEach(function (a) { a.classList.remove("sel"); });
        items[sel].classList.add("sel");
        items[sel].scrollIntoView({ block: "nearest" });
      }
      if (e.key === "Enter" && sel >= 0) { e.preventDefault(); items[sel].click(); }
    });
    document.addEventListener("click", function (e) {
      if (!res.contains(e.target) && e.target !== q) res.hidden = true;
    });
    // "/" focuses search, the way every docs site the reader already uses does.
    document.addEventListener("keydown", function (e) {
      if (e.key === "/" && document.activeElement !== q && !/^(INPUT|TEXTAREA)$/.test(document.activeElement.tagName)) {
        e.preventDefault(); q.focus();
      }
    });
  }

  // ---- copy button on code blocks ----
  // The install one-liner is the most important line on the site and it scrolls
  // out of view on a narrow screen; copying beats selecting it by hand.
  document.querySelectorAll(".doc pre").forEach(function (pre) {
    var b = document.createElement("button");
    b.className = "copy"; b.type = "button"; b.textContent = "Copy";
    b.setAttribute("aria-label", "Copy code to clipboard");
    b.addEventListener("click", function () {
      var code = pre.querySelector("code");
      var text = (code ? code.innerText : pre.innerText).replace(/\s+$/, "");
      function done() { b.textContent = "Copied!"; b.classList.add("ok"); setTimeout(function () { b.textContent = "Copy"; b.classList.remove("ok"); }, 1400); }
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(done);
      } else {
        // The panel is commonly reached over plain http on a LAN, where the
        // async clipboard API is unavailable; fall back rather than do nothing.
        var ta = document.createElement("textarea");
        ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0";
        document.body.appendChild(ta); ta.select();
        try { document.execCommand("copy"); done(); } catch (e) { /* nothing sensible left */ }
        document.body.removeChild(ta);
      }
    });
    pre.appendChild(b);
  });

  // ---- screenshot lightbox ----
  var imgs = document.querySelectorAll(".doc img");
  if (imgs.length) {
    var lb = document.createElement("div");
    lb.id = "lb"; lb.innerHTML = '<button class="x" aria-label="Close">×</button><img alt="" />';
    document.body.appendChild(lb);
    var big = lb.querySelector("img");
    imgs.forEach(function (el) {
      el.addEventListener("click", function () {
        big.src = el.currentSrc || el.src; big.alt = el.alt || "";
        lb.classList.add("open");
      });
    });
    lb.addEventListener("click", function () { lb.classList.remove("open"); big.removeAttribute("src"); });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape") lb.classList.remove("open"); });
  }

  // ---- TOC scroll-spy ----
  var links = document.querySelectorAll(".toc a");
  if (links.length && "IntersectionObserver" in window) {
    var map = {};
    links.forEach(function (a) { map[a.getAttribute("href").slice(1)] = a; });
    var seen = [];
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        var i = seen.indexOf(en.target.id);
        if (en.isIntersecting && i < 0) seen.push(en.target.id);
        if (!en.isIntersecting && i >= 0) seen.splice(i, 1);
      });
      if (!seen.length) return;
      links.forEach(function (a) { a.classList.remove("on"); });
      var top = seen[0];
      if (map[top]) map[top].classList.add("on");
    }, { rootMargin: "-60px 0px -70% 0px" });
    Object.keys(map).forEach(function (id) {
      var h = document.getElementById(id);
      if (h) io.observe(h);
    });
  }

  // ---- close the mobile menu after tapping a link ----
  var nt = document.getElementById("navtoggle");
  document.querySelectorAll(".nav nav a").forEach(function (a) {
    a.addEventListener("click", function () { if (nt) nt.checked = false; });
  });
})();
