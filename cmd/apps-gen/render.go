package main

import (
	"fmt"
	"html"
	"strings"
)

// render builds the standalone /apps showcase, styled to match the marketing site.
func render(apps []app) string {
	var appCards, gameCards strings.Builder
	nApps, nGames := 0, 0
	for _, a := range apps {
		if a.IsApp {
			appCards.WriteString(card(a))
			nApps++
		} else {
			gameCards.WriteString(card(a))
			nGames++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, head, len(apps))
	fmt.Fprintf(&b, `<main class="wrap">
  <h1>Supported apps &amp; games</h1>
  <p class="lede">%d one-click runes and counting — self-hosted apps and dedicated game servers, each installed, configured and kept running by the panel. Multi-container apps (marked <span class="badge">stack</span>) bring their own database.</p>
`, len(apps))
	if nGames > 0 {
		fmt.Fprintf(&b, `<h2>Games <span class="count">%d</span></h2>
<div class="grid">%s</div>
`, nGames, gameCards.String())
	}
	if nApps > 0 {
		fmt.Fprintf(&b, `<h2>Apps <span class="count">%d</span></h2>
<div class="grid">%s</div>
`, nApps, appCards.String())
	}
	b.WriteString(builtOn)
	b.WriteString(foot)
	return b.String()
}

func card(a app) string {
	var icon string
	if a.Icon != "" {
		icon = fmt.Sprintf(`<img loading="lazy" src="%s" alt="" width="40" height="40">`, a.Icon)
	} else {
		glyph := "📦"
		if !a.IsApp {
			glyph = "🎮"
		}
		icon = fmt.Sprintf(`<span class="glyph">%s</span>`, glyph)
	}
	stack := ""
	if a.Stack {
		stack = `<span class="badge">stack</span>`
	}
	// title carries the FULL description. The visible text is deliberately left
	// as it was — firstSentence() trims it, and .desc then clamps that to two
	// lines — so a long entry is cut twice and there was no way to read the rest.
	// A native title needs no script, no CDN and no layout change; the cost is
	// that it does not exist on touch, where there is no hover to do it with.
	return fmt.Sprintf(`<div class="app" title="%s">
  <div class="ic">%s</div>
  <div class="meta"><div class="name">%s%s</div><div class="desc">%s</div></div>
</div>`, html.EscapeString(a.Description), icon, html.EscapeString(a.Name), stack, html.EscapeString(firstSentence(a.Description)))
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".!"); i > 0 && i < 160 {
		return s[:i+1]
	}
	if len(s) > 160 {
		return strings.TrimSpace(s[:160]) + "…"
	}
	return s
}

const head = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Supported apps &amp; games — Yggdrasil Panel</title>
<meta name="description" content="%[1]d self-hosted apps and game servers you can run in one click with Yggdrasil Panel.">
<link rel="canonical" href="https://yggdrasilpanel.com/apps/">
<meta property="og:title" content="Supported apps &amp; games — Yggdrasil Panel">
<meta property="og:description" content="%[1]d self-hosted apps and game servers you can run in one click with Yggdrasil Panel.">
<meta property="og:type" content="website">
<meta property="og:url" content="https://yggdrasilpanel.com/apps/">
<link rel="icon" href="data:image/svg+xml,%%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'%%3E%%3Ctext y='.9em' font-size='90'%%3E%%F0%%9F%%8C%%B3%%3C/text%%3E%%3C/svg%%3E">
<style>
:root{--bg:#0b0f14;--bg2:#10161e;--card:#141b24;--card2:#1b2530;--bd:#243040;--tx:#e6edf3;--mut:#9aa7b4;--grn:#22c55e;--grn2:#34d399}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--tx);font-family:system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;line-height:1.5}
a{color:var(--grn2);text-decoration:none}
.nav{position:sticky;top:0;z-index:20;background:rgba(11,15,20,.8);backdrop-filter:blur(8px);border-bottom:1px solid var(--bd)}
.navin{max-width:1080px;margin:0 auto;display:flex;align-items:center;gap:1rem;height:60px;padding:0 1.25rem}
.links{margin-left:auto;display:flex;gap:1.25rem;align-items:center}
.links a{color:var(--mut);font-size:.95rem}.links a:hover{color:var(--tx);text-decoration:none}
.links a.btn{background:transparent;color:var(--tx);border:1px solid var(--bd);border-radius:.6rem;padding:.6rem 1rem;font-weight:600}
.navtoggle{position:absolute;opacity:0;pointer-events:none}
.hamburger{display:none;margin-left:auto;font-size:1.6rem;line-height:1;cursor:pointer;color:var(--tx)}
@media (max-width:640px){
  .hamburger{display:block}
  .links{position:absolute;top:60px;left:0;right:0;margin-left:0;flex-direction:column;align-items:flex-start;gap:0;background:rgba(11,15,20,.97);border-bottom:1px solid var(--bd);padding:.25rem 1.25rem .75rem;display:none}
  .navtoggle:checked ~ .links{display:flex}
  .links a{padding:.7rem 0;font-size:1rem}
  .links a.btn{margin-top:.4rem;text-align:center;align-self:stretch}
}
.nav .brand{font-weight:700;color:var(--tx);font-size:1.05rem}
.nav a{color:var(--mut)}.nav a:hover{color:var(--tx)}.nav .sp{flex:1}
.wrap{max-width:1080px;margin:0 auto;padding:2.5rem 1.25rem 4rem}
h1{font-size:2rem;margin:0 0 .5rem}
.lede{color:var(--mut);max-width:60ch;margin:0 0 2rem}
h2{margin:2.5rem 0 1rem;font-size:1.15rem;display:flex;align-items:center;gap:.6rem}
.count{font-size:.8rem;color:var(--mut);font-weight:400;border:1px solid var(--bd);border-radius:999px;padding:.05rem .5rem}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:.75rem}
.app{display:flex;gap:.75rem;align-items:center;background:var(--card);border:1px solid var(--bd);border-radius:12px;padding:.85rem;transition:border-color .15s,background .15s}
.app:hover{border-color:var(--grn);background:var(--card2)}
.ic{width:40px;height:40px;flex:0 0 40px;display:grid;place-items:center}
.ic img{width:40px;height:40px;object-fit:contain;border-radius:8px}
.glyph{font-size:28px;line-height:1}
.meta{min-width:0}
.name{font-weight:600;display:flex;align-items:center;gap:.4rem}
.desc{color:var(--mut);font-size:.82rem;overflow:hidden;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical}
.badge{font-size:.62rem;text-transform:uppercase;letter-spacing:.04em;color:var(--grn2);background:rgba(34,197,94,.12);border:1px solid rgba(34,197,94,.35);border-radius:999px;padding:.02rem .4rem;font-weight:700}
footer{border-top:1px solid var(--bd);color:var(--mut);text-align:center;padding:2rem 1.25rem;font-size:.85rem}
</style></head>
<body>
<nav class="nav">
<div class="navin">
  <a class="brand" href="/">🌳 Yggdrasil&nbsp;Panel</a>
  <input type="checkbox" id="navtoggle" class="navtoggle" aria-hidden="true" />
  <label for="navtoggle" class="hamburger" aria-label="Toggle menu">☰</label>
  <div class="links">
    <a href="/#features">Features</a>
    <a href="/apps">Apps &amp; games</a>
    <a href="/docs/">Docs</a>
    <a href="/#install">Install</a>
    <a href="https://discord.gg/QM6TmJYvMS" target="_blank" rel="noopener">Discord</a>
    <a class="btn" href="https://github.com/kristianwind/yggdrasil" target="_blank" rel="noopener">GitHub ↗</a>
  </div>
</div>
</nav>
`

// builtOn lists applications that ship as a rune. It belongs on this page rather
// than the front page: these are apps you can install, which is what a reader is
// here for — and it does not earn its own nav entry.
//
// Hand-maintained on purpose. Everything above comes from the rune catalogue;
// these live in their own repositories, which the generator has no reason to read.
//
// 🔴 A card is only a link when the repository is PUBLIC. Checked anonymously,
// not with a signed-in gh — a private repo answers 200 to you and 404 to every
// visitor. Mimir is listed without one for exactly that reason (rechecked
// 2026-08-29: 404). Make the repo public and it becomes an <a> like the others.
//
// Kokkeri (Andreas Dinesen — recipe library, meal planner, shopping list) belongs
// here too and was listed until 2026-08-09, but its repository is still private
// (rechecked 2026-08-25: anonymous GitHub returns 404). A card linking somewhere
// nobody can go is an advert, not a reference, so it comes back the day the
// repository does — the card is written and commented out below, ready to
// uncomment, and the count goes to 7.
const builtOn = `<h2>Built on Yggdrasil <span class="count">8</span></h2>
<p class="lede">Applications that install as a rune — one entry in the catalogue, and the panel builds, runs and backs them up like anything else.</p>
<div class="grid">
  <a class="app" title="A personal book library. Scan the ISBN barcode and it fills in title, author, cover and series — and says at once if you already own it. Passkey login, several users, SQLite built in." href="https://github.com/andreasdinesen/bogreolen" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
    <div class="ic"><span class="glyph">📚</span></div>
    <div class="meta"><div class="name">Bogreolen <span class="badge">GitHub ↗</span></div><div class="desc">A personal book library. Scan the ISBN barcode and it fills in title, author, cover and series — and says at once if you already own it. Passkey login, several users, SQLite built in.</div></div>
  </a>
  <a class="app" title="Tasks and notes on GTD lines: one search field that both finds and creates, opening as soon as you type, and Todoist-style recurring syntax. No npm packages, no CDN." href="https://github.com/andreasdinesen/doda" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
    <div class="ic"><span class="glyph">✅</span></div>
    <div class="meta"><div class="name">doda <span class="badge">GitHub ↗</span></div><div class="desc">Tasks and notes on GTD lines: one search field that both finds and creates, opening as soon as you type, and Todoist-style recurring syntax. No npm packages, no CDN.</div></div>
  </a>
  <div class="app" title="Genshin Impact adviser: imports your account from Enka, GOOD or HoYoLAB, optimises your artifacts, and ranks every possible upgrade on the whole account by expected damage gained per resin — so it answers what to spend today's resin on, not what a perfect build would look like.">
    <div class="ic"><span class="glyph">🔮</span></div>
    <div class="meta"><div class="name">Mimir</div><div class="desc">Genshin Impact adviser: imports your account, optimises artifacts, and ranks every possible upgrade by damage gained per resin — what to spend today's resin on, not what a perfect build would be.</div></div>
  </div>
  <a class="app" title="Notes and wiki, written to replace Notion: notebooks nested to any depth, a hybrid live-markdown editor, full-text search, and publishing a page or a whole notebook as a public wiki with comments." href="https://github.com/andreasdinesen/sagu" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
    <div class="ic"><span class="glyph">📓</span></div>
    <div class="meta"><div class="name">Sagu <span class="badge">GitHub ↗</span></div><div class="desc">Notes and wiki, written to replace Notion: notebooks nested to any depth, a hybrid live-markdown editor, full-text search, and publishing a page or a whole notebook as a public wiki with comments.</div></div>
  </a>
  <a class="app" title="Event sign-ups with three levels — attendee, group admin and master admin — plus CSV export. Flask and SQLite; the database creates itself on first start." href="https://github.com/andreasdinesen/tilmeld" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
    <div class="ic"><span class="glyph">🎟️</span></div>
    <div class="meta"><div class="name">Tilmeld <span class="badge">GitHub ↗</span></div><div class="desc">Event sign-ups with three levels — attendee, group admin and master admin — plus CSV export. Flask and SQLite; the database creates itself on first start.</div></div>
  </a>
  <a class="app" title="Time tracking on tasks and projects: timers or manual entry, estimates, a customer view, and import from Microsoft Planner. doda's twin — same stack, its own database." href="https://github.com/andreasdinesen/tovo" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
    <div class="ic"><span class="glyph">⏱️</span></div>
    <div class="meta"><div class="name">tovo <span class="badge">GitHub ↗</span></div><div class="desc">Time tracking on tasks and projects: timers or manual entry, estimates, a customer view, and import from Microsoft Planner. doda's twin — same stack, its own database.</div></div>
  </a>
  <a class="app" title="Strength-training PWA with offline logging, honest statistics and an AI coach. Built for iPhone and web." href="https://github.com/kristianwind/uruz" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
    <div class="ic"><span class="glyph">ᚢ</span></div>
    <div class="meta"><div class="name">Uruz <span class="badge">GitHub ↗</span></div><div class="desc">Strength-training PWA with offline logging, honest statistics and an AI coach. Built for iPhone and web.</div></div>
  </a>
  <a class="app" title="Tasks and projects, shared with the people you share the work with — projects, filters and labels beside what&#39;s due today. One Go binary with SQLite inside it." href="https://github.com/kristianwind/verdande" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
    <div class="ic"><span class="glyph">🧵</span></div>
    <div class="meta"><div class="name">Verdande <span class="badge">GitHub ↗</span></div><div class="desc">Tasks and projects, shared with the people you share the work with — projects, filters and labels beside what&#39;s due today. One Go binary with SQLite inside it.</div></div>
  </a>
</div>
`

// Ready for the day andreasdinesen/kokkeri goes public — see the note above.
// Add it after doda (the grid is alphabetical) and bump the count to 7.
//
//	<a class="app" title="Recipe library, meal planner and shopping list — a self-hosted replacement for Paprika." href="https://github.com/andreasdinesen/kokkeri" target="_blank" rel="noopener" style="text-decoration:none;color:inherit">
//	  <div class="ic"><span class="glyph">🍳</span></div>
//	  <div class="meta"><div class="name">Kokkeri <span class="badge">GitHub ↗</span></div><div class="desc">Recipe library, meal planner and shopping list — a self-hosted replacement for Paprika.</div></div>
//	</a>

const foot = `</main>
<footer>Missing one? Runes are plain YAML — <a href="/docs/guides-runes.html">write your own</a> or open a PR. Icons by <a href="https://github.com/homarr-labs/dashboard-icons" target="_blank" rel="noopener">Dashboard Icons</a>.</footer>
</body></html>
`
