// Command docs-gen renders the markdown in docs/ into the static documentation
// site under website/docs/.
//
// docs/ is the single source of truth: it renders on GitHub for contributors and
// here for everyone else. Nothing is written twice.
//
// Pages marked OnSite=false are not rendered; links to them are rewritten to
// GitHub instead, so the deep reference material stays one click away while the
// site leads with the starter path. Publishing the rest is a flag flip, not a
// rewrite — see pages below.
//
// The output is self-contained static HTML: no CDN, no runtime dependency, no
// build step for the reader. It is served by the static-site rune like the
// landing page beside it.
//
// Usage: go run ./cmd/docs-gen
package main

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

const (
	repoURL    = "https://github.com/kristianwind/yggdrasil"
	discordURL = "https://discord.gg/murNphXV3"
	docsOnGH   = repoURL + "/blob/main/docs/"
	outDir     = "website/docs"
	srcDir     = "docs"
	siteTitle  = "Yggdrasil Panel docs"
)

// Page is one markdown file in docs/.
//
// OnSite=false keeps a page GitHub-only: it isn't rendered, and every link to it
// is rewritten to the GitHub blob URL. That's how the site ships the starter path
// first while staying ready to host everything — set OnSite=true and it appears,
// links and search included, with no other change.
type Page struct {
	Src     string // path under docs/, e.g. "guides/servers.md"
	Section string // sidebar group; "" pins it above the groups
	Nav     string // short sidebar label
	Blurb   string // one line, shown on the docs index
	OnSite  bool
}

var pages = []Page{
	{Src: "getting-started.md", Nav: "Getting started", Blurb: "From a bare Debian box to a running game server.", OnSite: true},

	{Src: "guides/servers.md", Section: "Guides", Nav: "Servers", Blurb: "Lifecycle, console, files, cloning, tags, bulk actions.", OnSite: true},
	{Src: "guides/runes.md", Section: "Guides", Nav: "Runes", Blurb: "The catalog: what ships, and how to add more.", OnSite: true},
	{Src: "guides/backups-and-schedules.md", Section: "Guides", Nav: "Backups & schedules", Blurb: "Targets, restore, verification, retention, cron.", OnSite: true},
	{Src: "guides/networking.md", Section: "Guides", Nav: "Networking", Blurb: "Ports, reachability, UniFi, NPM, Cloudflare Tunnel.", OnSite: true},
	{Src: "guides/monitoring-and-alerts.md", Section: "Guides", Nav: "Monitoring & alerts", Blurb: "Metrics, alarms, the watchdog, auto-restart.", OnSite: true},
	{Src: "guides/notifications.md", Section: "Guides", Nav: "Notifications", Blurb: "Telegram, Discord, webhooks — and what triggers them.", OnSite: true},
	{Src: "guides/users-and-permissions.md", Section: "Guides", Nav: "Users & permissions", Blurb: "Realms, permission bits, delegates, 2FA, tokens.", OnSite: true},
	{Src: "guides/status-page-and-beacon.md", Section: "Guides", Nav: "Status page & beacon", Blurb: "The public board, the Discord embed, the opt-in beacon.", OnSite: true},
	{Src: "guides/kvasir-ai.md", Section: "Guides", Nav: "Kvasir (AI)", Blurb: "The optional AI assistant and its safety model.", OnSite: true},
	{Src: "guides/dayz-norn.md", Section: "Guides", Nav: "DayZ loot (Norn)", Blurb: "Lifetimes, globals, mod loot, surviving a reinstall.", OnSite: true},

	{Src: "reference/configuration.md", Section: "Reference", Nav: "Configuration", Blurb: "Every key in config.yaml.", OnSite: true},

	// Deep reference: integrators and rune authors are already on GitHub, and
	// these move with the code. Flip OnSite to publish them here.
	{Src: "reference/api.md", Section: "Reference", Nav: "HTTP API", Blurb: "All 165 routes, auth, and permission gates.", OnSite: false},
	{Src: "reference/rune-schema.md", Section: "Reference", Nav: "Rune schema", Blurb: "The YAML format for teaching Yggdrasil a new game.", OnSite: false},
}

// sectionOrder fixes the sidebar order; a map alone would render randomly.
var sectionOrder = []string{"Guides", "Reference"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "docs-gen:", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat(srcDir); err != nil {
		return fmt.Errorf("run me from the repo root: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Typographer),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(ghtml.WithUnsafe()),
	)

	var built []built
	for _, p := range pages {
		if !p.OnSite {
			continue
		}
		b, err := renderPage(md, p)
		if err != nil {
			return fmt.Errorf("%s: %w", p.Src, err)
		}
		built = append(built, b)
	}

	for _, b := range built {
		dst := filepath.Join(outDir, b.page.slug()+".html")
		if err := os.WriteFile(dst, []byte(shell(b)), 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte(indexPage()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "docs.css"), []byte(css), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "docs.js"), []byte(js), 0o644); err != nil {
		return err
	}
	if err := writeSearchIndex(built); err != nil {
		return err
	}
	if err := copyScreenshots(); err != nil {
		return err
	}

	fmt.Printf("docs-gen: %d pages + index → %s/\n", len(built), outDir)
	for _, p := range pages {
		if !p.OnSite {
			fmt.Printf("  (GitHub only: %s)\n", p.Src)
		}
	}
	return nil
}

type built struct {
	page  Page
	title string
	body  string
	toc   []head
	text  string // plain text of the whole page, for the meta description
	secs  []sec  // per-section text, for the search index
}

// sec is one h2 span of a page. Search indexes these rather than whole pages, so
// a hit can deep-link to the section that actually matched — and so a long page's
// later sections are searchable at all.
type sec struct {
	ID    string
	Title string
	Text  string
}

type head struct {
	Level int
	ID    string
	Text  string
}

// headingID returns the auto-generated anchor for a heading.
//
// AttributeString yields the raw []byte goldmark stored, so it must be converted
// explicitly: fmt.Sprint on a []byte renders it as "[104 105]", which produces an
// anchor that looks plausible in source and matches nothing in the page.
func headingID(h *ast.Heading) string {
	v, ok := h.AttributeString("id")
	if !ok {
		return ""
	}
	switch id := v.(type) {
	case []byte:
		return string(id)
	case string:
		return id
	default:
		return ""
	}
}

func (p Page) slug() string {
	s := strings.TrimSuffix(p.Src, ".md")
	return strings.ReplaceAll(s, "/", "-")
}

// pageBySrc resolves a docs-relative path (as written in a markdown link) to its
// Page, so links can be rewritten to the right destination.
func pageBySrc(src string) (Page, bool) {
	for _, p := range pages {
		if p.Src == src {
			return p, true
		}
	}
	return Page{}, false
}

func renderPage(md goldmark.Markdown, p Page) (built, error) {
	raw, err := os.ReadFile(filepath.Join(srcDir, p.Src))
	if err != nil {
		return built{}, err
	}
	doc := md.Parser().Parse(text.NewReader(raw))

	b := built{page: p}
	if err := rewrite(doc, raw, p, &b); err != nil {
		return built{}, err
	}

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, raw, doc); err != nil {
		return built{}, err
	}
	b.body = buf.String()
	b.text = plainText(b.body)
	b.secs = sections(doc, raw)
	return b, nil
}

// sections splits a page at its h2 boundaries. Everything before the first h2
// (the intro paragraph) is kept as a section anchored at the page itself, so the
// opening lines are searchable too.
func sections(doc ast.Node, src []byte) []sec {
	out := []sec{{ID: "", Title: ""}}
	var sb strings.Builder

	flush := func() {
		out[len(out)-1].Text = strings.TrimSpace(wsRE.ReplaceAllString(sb.String(), " "))
		sb.Reset()
	}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok && h.Level <= 2 {
			flush()
			out = append(out, sec{ID: headingID(h), Title: string(h.Text(src))})
			continue
		}
		sb.WriteString(string(n.Text(src)))
		sb.WriteByte(' ')
		// Node.Text skips fenced code, which is where half the answers live
		// (commands, YAML keys, env vars). Pull those in explicitly.
		_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch c := c.(type) {
			case *ast.FencedCodeBlock:
				for i := 0; i < c.Lines().Len(); i++ {
					l := c.Lines().At(i)
					sb.Write(l.Value(src))
					sb.WriteByte(' ')
				}
			case *ast.CodeBlock:
				for i := 0; i < c.Lines().Len(); i++ {
					l := c.Lines().At(i)
					sb.Write(l.Value(src))
					sb.WriteByte(' ')
				}
			}
			return ast.WalkContinue, nil
		})
	}
	flush()

	var keep []sec
	for _, s := range out {
		if s.Text != "" || s.Title != "" {
			keep = append(keep, s)
		}
	}
	return keep
}

// rewrite walks the AST to do three things at once: lift the title out of the
// first h1 (and drop it, since the shell renders it), collect the h2/h3 TOC, and
// repoint every relative link at either the generated page or GitHub.
func rewrite(doc ast.Node, src []byte, p Page, b *built) error {
	var drop []ast.Node
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			txt := string(n.Text(src))
			switch {
			case n.Level == 1 && b.title == "":
				b.title = txt
				drop = append(drop, n) // the shell renders the title
			case n.Level == 2 || n.Level == 3:
				b.toc = append(b.toc, head{Level: n.Level, ID: headingID(n), Text: txt})
			}
		case *ast.Link:
			n.Destination = []byte(resolveLink(string(n.Destination), p))
		case *ast.Image:
			n.Destination = []byte(resolveImage(string(n.Destination)))
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return err
	}
	for _, n := range drop {
		n.Parent().RemoveChild(n.Parent(), n)
	}
	if b.title == "" {
		b.title = p.Nav
	}
	return nil
}

// resolveLink maps a markdown link to where it lives on the site.
//
// Same-page anchors and external URLs pass through. A link to a published page
// becomes its .html; a link to a GitHub-only page becomes the GitHub blob URL, so
// the reader still lands somewhere real rather than a 404.
func resolveLink(dest string, from Page) string {
	if dest == "" || strings.HasPrefix(dest, "#") ||
		strings.HasPrefix(dest, "http://") || strings.HasPrefix(dest, "https://") ||
		strings.HasPrefix(dest, "mailto:") {
		return dest
	}

	target, anchor, _ := strings.Cut(dest, "#")
	if anchor != "" {
		anchor = "#" + anchor
	}
	if target == "" {
		return dest
	}

	// Resolve relative to the linking page's directory, into a docs/-relative path.
	abs := path.Clean(path.Join(path.Dir(from.Src), target))

	if !strings.HasSuffix(abs, ".md") {
		// Screenshots and other assets are copied alongside the pages.
		if strings.Contains(abs, "screenshots/") {
			return path.Base(path.Dir(abs)) + "/" + path.Base(abs) + anchor
		}
		return docsOnGH + abs + anchor
	}
	if p, ok := pageBySrc(abs); ok {
		if p.OnSite {
			return p.slug() + ".html" + anchor
		}
		return docsOnGH + abs + anchor
	}
	// A doc that exists but isn't in the manifest (design notes) — send them to GitHub.
	return docsOnGH + abs + anchor
}

func resolveImage(dest string) string {
	if strings.HasPrefix(dest, "http") {
		return dest
	}
	return "screenshots/" + path.Base(dest)
}

var tagRE = regexp.MustCompile(`<[^>]*>`)
var wsRE = regexp.MustCompile(`\s+`)

func plainText(h string) string {
	s := tagRE.ReplaceAllString(h, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(wsRE.ReplaceAllString(s, " "))
}

func copyScreenshots() error {
	dst := filepath.Join(outDir, "screenshots")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(filepath.Join(srcDir, "screenshots"))
	if err != nil {
		return err
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		in, err := os.Open(filepath.Join(srcDir, "screenshots", e.Name()))
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(dst, e.Name()))
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			return err
		}
		n++
	}
	return nil
}

// writeSearchIndex emits one record per section, so a hit deep-links to the
// heading that matched instead of dumping the reader at the top of a long page.
//
// It is deliberately not a search service: the whole index is one small file the
// page filters client-side, so the docs call nothing at runtime and — like the
// rest of the project — report nothing about who read what.
func writeSearchIndex(bs []built) error {
	var sb strings.Builder
	sb.WriteString("[\n")
	first := true
	for _, b := range bs {
		for _, s := range b.secs {
			url := b.page.slug() + ".html"
			title := b.title
			if s.ID != "" {
				url += "#" + s.ID
				title = b.title + " › " + s.Title
			}
			if !first {
				sb.WriteString(",\n")
			}
			first = false
			fmt.Fprintf(&sb, `{"u":%s,"t":%s,"s":%s,"b":%s}`,
				jsonStr(url), jsonStr(title), jsonStr(sectionOf(b.page)), jsonStr(s.Text))
		}
	}
	sb.WriteString("\n]\n")
	return os.WriteFile(filepath.Join(outDir, "search.json"), []byte(sb.String()), 0o644)
}

func sectionOf(p Page) string {
	if p.Section == "" {
		return "Start here"
	}
	return p.Section
}

func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			if r < 0x20 {
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ---- templating ----

func sidebar(current string) string {
	var b strings.Builder
	// The toggle is CSS-only and only surfaces on mobile, where an always-open
	// sidebar would push the article a full screen down on every page.
	b.WriteString(`<input type="checkbox" id="sidetoggle" class="sidetoggle" aria-hidden="true" />` +
		`<label for="sidetoggle" class="sidebtn">📖 All pages</label>` +
		`<nav class="side" aria-label="Documentation">`)

	link := func(p Page) {
		cls := ""
		if p.slug() == current {
			cls = ` class="on"`
		}
		if p.OnSite {
			fmt.Fprintf(&b, `<a href="%s.html"%s>%s</a>`, p.slug(), cls, html.EscapeString(p.Nav))
			return
		}
		fmt.Fprintf(&b, `<a href="%s%s" class="ext" target="_blank" rel="noopener">%s <span class="gh">GitHub&nbsp;↗</span></a>`,
			docsOnGH, p.Src, html.EscapeString(p.Nav))
	}

	b.WriteString(`<a href="index.html"` + onIf(current == "") + `>Overview</a>`)
	for _, p := range pages {
		if p.Section == "" && p.OnSite {
			link(p)
		}
	}
	for _, sec := range sectionOrder {
		fmt.Fprintf(&b, `<div class="sec">%s</div>`, html.EscapeString(sec))
		for _, p := range pages {
			if p.Section == sec {
				link(p)
			}
		}
	}
	b.WriteString(`</nav>`)
	return b.String()
}

func onIf(cond bool) string {
	if cond {
		return ` class="on"`
	}
	return ""
}

func tocHTML(hs []head) string {
	if len(hs) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<aside class="toc"><div class="toch">On this page</div>`)
	for _, h := range hs {
		cls := "t2"
		if h.Level == 3 {
			cls = "t3"
		}
		fmt.Fprintf(&b, `<a class="%s" href="#%s">%s</a>`, cls, h.ID, html.EscapeString(h.Text))
	}
	b.WriteString(`</aside>`)
	return b.String()
}

func topNav() string {
	return `<header class="nav">
  <div class="navwrap">
    <a class="brand" href="/">🌳 Yggdrasil&nbsp;Panel</a>
    <input type="checkbox" id="navtoggle" class="navtoggle" aria-hidden="true" />
    <label for="navtoggle" class="hamburger" aria-label="Toggle menu">☰</label>
    <nav>
      <a href="/#features">Features</a>
      <a href="/#screens">Screenshots</a>
      <a href="/docs/" class="on">Docs</a>
      <a href="/#install">Install</a>
      <a href="` + discordURL + `" target="_blank" rel="noopener">Discord</a>
      <a class="btn ghost" href="` + repoURL + `" target="_blank" rel="noopener">GitHub ↗</a>
    </nav>
  </div>
</header>`
}

func searchBox() string {
	return `<div class="search">
  <input id="q" type="search" placeholder="Search the docs…" autocomplete="off" aria-label="Search the docs" />
  <div id="res" class="res" hidden></div>
</div>`
}

func shell(b built) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>` + html.EscapeString(b.title) + ` — ` + siteTitle + `</title>
<meta name="description" content="` + html.EscapeString(firstSentence(b.text)) + `" />
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'%3E%3Ctext y='.9em' font-size='90'%3E%F0%9F%8C%B3%3C/text%3E%3C/svg%3E" />
<link rel="stylesheet" href="docs.css" />
</head>
<body>
` + topNav() + `
<div class="shell">
` + sidebar(b.page.slug()) + `
<main>
  ` + searchBox() + `
  <article class="doc">
    <h1>` + html.EscapeString(b.title) + `</h1>
    ` + b.body + `
    <div class="editrow">
      <a href="` + docsOnGH + b.page.Src + `" target="_blank" rel="noopener">Edit this page on GitHub ↗</a>
    </div>
  </article>
</main>
` + tocHTML(b.toc) + `
</div>
<script src="docs.js"></script>
</body>
</html>
`
}

func firstSentence(s string) string {
	if i := strings.IndexAny(s, "."); i > 0 && i < 180 {
		return s[:i+1]
	}
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

func indexPage() string {
	var cards strings.Builder

	var start []Page
	for _, p := range pages {
		if p.Section == "" && p.OnSite {
			start = append(start, p)
		}
	}
	sort.SliceStable(start, func(i, j int) bool { return false })

	card := func(p Page) {
		href := p.slug() + ".html"
		ext := ""
		if !p.OnSite {
			href = docsOnGH + p.Src
			ext = ` target="_blank" rel="noopener"`
		}
		badge := ""
		if !p.OnSite {
			badge = `<span class="gh">GitHub ↗</span>`
		}
		fmt.Fprintf(&cards, `<a class="card" href="%s"%s><h3>%s %s</h3><p>%s</p></a>`,
			href, ext, html.EscapeString(p.Nav), badge, html.EscapeString(p.Blurb))
	}

	cards.WriteString(`<div class="hero-doc">
    <div class="tree">🌳</div>
    <h1>Documentation</h1>
    <p class="tag">Everything you need to run Yggdrasil Panel — from a bare Debian box
    to a fleet of game servers, backups and all.</p>
  </div>`)

	cards.WriteString(`<div class="startbox">`)
	for _, p := range start {
		fmt.Fprintf(&cards, `<a class="startcard" href="%s.html">
      <div class="k">Start here</div>
      <h2>%s</h2>
      <p>%s</p>
      <span class="go">Read the guide →</span>
    </a>`, p.slug(), html.EscapeString(p.Nav), html.EscapeString(p.Blurb))
	}
	cards.WriteString(`</div>`)

	for _, sec := range sectionOrder {
		fmt.Fprintf(&cards, `<h2 class="sech">%s</h2>`, html.EscapeString(sec))
		if sec == "Reference" {
			cards.WriteString(`<p class="secsub">For integrating with a panel or writing your own runes. The deep reference lives on GitHub, next to the code it describes.</p>`)
		}
		cards.WriteString(`<div class="grid">`)
		for _, p := range pages {
			if p.Section == sec {
				card(p)
			}
		}
		cards.WriteString(`</div>`)
	}

	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>` + siteTitle + `</title>
<meta name="description" content="Documentation for Yggdrasil Panel — install, run and manage game and app servers on your own Debian/Ubuntu box." />
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'%3E%3Ctext y='.9em' font-size='90'%3E%F0%9F%8C%B3%3C/text%3E%3C/svg%3E" />
<link rel="stylesheet" href="docs.css" />
</head>
<body>
` + topNav() + `
<div class="shell">
` + sidebar("") + `
<main>
  ` + searchBox() + `
  <article class="doc idx">
    ` + cards.String() + `
  </article>
</main>
</div>
<script src="docs.js"></script>
</body>
</html>
`
}
