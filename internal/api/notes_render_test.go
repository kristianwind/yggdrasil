package api

import (
	"strings"
	"testing"
)

// Notes are written with server.control and read by admins, so a rendered note
// crosses a privilege boundary: a delegate writes it, an admin's browser runs it.
// The panel's CSP is a mitigation, not a reason to hand the browser unfiltered
// HTML.
//
// These are the vectors that must not survive. If one starts failing, the cause
// is almost certainly html.WithUnsafe() having been added to the renderer.
func TestRenderNotesDropsHTMLAndScriptURLs(t *testing.T) {
	cases := []struct {
		name, in string
		mustNot  []string
	}{
		{"script tag", `<script>alert(1)</script>`, []string{"<script", "alert(1)"}},
		{"img onerror", `<img src=x onerror=alert(1)>`, []string{"<img src=x", "onerror"}},
		{"div handler", `<div onclick="alert(1)">hi</div>`, []string{"onclick"}},
		{"raw anchor", `<a href="javascript:alert(1)">x</a>`, []string{"javascript:"}},
		{"markdown link", `[click me](javascript:alert(1))`, []string{"javascript:"}},
		{"markdown image", `![img](javascript:alert(1))`, []string{"javascript:"}},
		{"data URI", `[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`, []string{"data:text/html"}},
		{"vbscript", `[x](vbscript:msgbox(1))`, []string{"vbscript:"}},
		{"iframe", `<iframe src="https://evil.example"></iframe>`, []string{"<iframe"}},
		{"svg onload", `<svg onload=alert(1)>`, []string{"<svg", "onload"}},
		{"style", `<style>body{display:none}</style>`, []string{"<style"}},
		{"mixed case script", `<ScRiPt>alert(1)</ScRiPt>`, []string{"<ScRiPt", "alert(1)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderNotes(c.in)
			for _, bad := range c.mustNot {
				if strings.Contains(got, bad) {
					t.Errorf("rendered note still contains %q:\n  in:  %s\n  out: %s", bad, c.in, got)
				}
			}
		})
	}
}

// The point of the feature: ordinary markdown has to actually work.
func TestRenderNotesRendersMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold**", "<strong>bold</strong>"},
		{"*italic*", "<em>italic</em>"},
		{"`code`", "<code>code</code>"},
		{"- one\n- two", "<li>one</li>"},
		{"# Heading", "<h1"},
		{"[ok](https://example.com)", `href="https://example.com"`},
		{"| a | b |\n|---|---|\n| 1 | 2 |", "<table>"}, // GFM
		{"~~gone~~", "<del>gone</del>"},                // GFM
		{"line1\n\nline2", "<p>line2</p>"},
	}
	for _, c := range cases {
		if got := renderNotes(c.in); !strings.Contains(got, c.want) {
			t.Errorf("renderNotes(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
	}
}

// A relative or mailto link is fine and shouldn't be stripped along with the
// dangerous schemes.
func TestRenderNotesKeepsOrdinaryLinks(t *testing.T) {
	for _, in := range []string{
		"[docs](/docs/getting-started.html)",
		"[mail](mailto:admin@example.com)",
		"[panel](http://192.168.1.50:8080)",
	} {
		got := renderNotes(in)
		if strings.Contains(got, `href=""`) {
			t.Errorf("renderNotes(%q) emptied a legitimate link: %s", in, got)
		}
	}
}

func TestRenderNotesEmptyInput(t *testing.T) {
	if got := renderNotes(""); got != "" {
		t.Errorf("empty note rendered to %q", got)
	}
}
