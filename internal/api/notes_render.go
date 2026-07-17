package api

import (
	"bytes"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// Server notes can be written as markdown, and are rendered here rather than in
// the browser.
//
// Two reasons for doing it server-side. The frontend has no runtime
// dependencies — deliberately — and a markdown library would be its first. And
// the escaping is the security boundary, so it belongs where it can be tested in
// Go rather than trusted to a bundle.
//
// SECURITY: this renderer must never be given html.WithUnsafe().
//
// Notes are writable with server.control and readable by admins, so they cross a
// privilege boundary: a delegate writes, an admin reads. Rendering their
// markdown to HTML is therefore a stored-XSS surface, and the panel's CSP
// (script-src 'self', no unsafe-inline) is a mitigation, not a licence to hand it
// unfiltered HTML.
//
// goldmark's defaults are what make this safe, and they were checked rather than
// assumed:
//
//	<script>alert(1)</script>          -> <!-- raw HTML omitted -->
//	<img src=x onerror=alert(1)>       -> <!-- raw HTML omitted -->
//	[click](javascript:alert(1))       -> <a href="">click</a>
//	<a href="javascript:alert(1)">x</a>-> <!-- raw HTML omitted -->x…
//	[ok](https://example.com)          -> <a href="https://example.com">ok</a>
//
// So raw HTML is dropped whole and dangerous URL schemes are emptied. Adding
// WithUnsafe() would turn every one of those back on. cmd/docs-gen does pass it,
// because it renders markdown that lives in the repo and went through review —
// the opposite situation.
var notesMD = sync.OnceValue(func() goldmark.Markdown {
	return goldmark.New(
		// GFM buys tables, strikethrough and autolinks. No WithUnsafe, no raw HTML.
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
})

// renderNotes turns a note's markdown into HTML safe to inject.
//
// On failure it returns "" rather than partial output: the caller falls back to
// showing the note as plain text, which is always safe and never wrong.
func renderNotes(src string) string {
	if src == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := notesMD().Convert([]byte(src), &buf); err != nil {
		return ""
	}
	return buf.String()
}
