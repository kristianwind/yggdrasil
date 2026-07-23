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
	return fmt.Sprintf(`<div class="app">
  <div class="ic">%s</div>
  <div class="meta"><div class="name">%s%s</div><div class="desc">%s</div></div>
</div>`, icon, html.EscapeString(a.Name), stack, html.EscapeString(firstSentence(a.Description)))
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
<meta name="description" content="%d self-hosted apps and game servers you can run in one click with Yggdrasil Panel.">
<link rel="icon" href="data:image/svg+xml,%%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'%%3E%%3Ctext y='.9em' font-size='90'%%3E%%F0%%9F%%8C%%B3%%3C/text%%3E%%3C/svg%%3E">
<style>
:root{--bg:#0b0f14;--bg2:#10161e;--card:#141b24;--card2:#1b2530;--bd:#243040;--tx:#e6edf3;--mut:#9aa7b4;--grn:#22c55e;--grn2:#34d399}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--tx);font-family:system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;line-height:1.5}
a{color:var(--grn2);text-decoration:none}
.nav{display:flex;align-items:center;gap:1.5rem;padding:1rem 1.25rem;border-bottom:1px solid var(--bd);flex-wrap:wrap}
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
  <a class="brand" href="/">🌳 Yggdrasil&nbsp;Panel</a>
  <a href="/#features">Features</a>
  <a href="/apps">Apps &amp; games</a>
  <a href="/docs/">Docs</a>
  <a href="https://discord.gg/QM6TmJYvMS" target="_blank" rel="noopener">Discord</a>
  <span class="sp"></span>
  <a href="https://github.com/kristianwind/yggdrasil" target="_blank" rel="noopener">GitHub</a>
</nav>
`

const foot = `</main>
<footer>Missing one? Runes are plain YAML — <a href="/docs/guides-runes.html">write your own</a> or open a PR. Icons by <a href="https://github.com/homarr-labs/dashboard-icons" target="_blank" rel="noopener">Dashboard Icons</a>.</footer>
</body></html>
`
