package main

import (
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// parseDoc mirrors the real pipeline's parser context. Using goldmark's stock
// auto-IDs instead would slug "done_regex" to "done-regex" and quietly test
// anchors the site never emits.
func parseDoc(t *testing.T, src string) []sec {
	t.Helper()
	b := []byte(src)
	// Both halves are needed: WithAutoHeadingID turns IDs on at all, WithIDs
	// swaps in the slugger that matches GitHub. Either one alone silently yields
	// headings with no ID.
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Typographer),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	pctx := parser.NewContext(parser.WithIDs(newGithubIDs()))
	doc := md.Parser().Parse(text.NewReader(b), parser.WithContext(pctx))
	return sections(doc, b)
}

// No h1: rewrite drops it (the shell renders the page title), so sections()
// only ever sees the body from the intro down.
const schemaLike = `Intro prose about runes.

## docker

Prose under docker.

### data_path

Where the rune keeps its files.

### keep_entrypoint

Leave the image entrypoint alone.

## startup

Prose under startup.

### done_regex

The line that means the server finished booting.
`

func findSec(secs []sec, id string) *sec {
	for i := range secs {
		if secs[i].ID == id {
			return &secs[i]
		}
	}
	return nil
}

// An h3 is where the answers actually are — every rune variable is one. Folding
// them into the h2 above meant a search for "done_regex" deep-linked to whatever
// h2 span happened to contain the prose, dropping the reader somewhere else
// entirely.
func TestSectionsSplitAtH3(t *testing.T) {
	secs := parseDoc(t, schemaLike)

	s := findSec(secs, "done_regex")
	if s == nil {
		var got []string
		for _, x := range secs {
			got = append(got, x.ID)
		}
		t.Fatalf("no section anchored at the done_regex h3 — it got folded into an h2. Sections: %v", got)
	}
	if !strings.Contains(s.Text, "finished booting") {
		t.Errorf("the done_regex section doesn't carry its own prose: %q", s.Text)
	}
	// Its prose must not also sit in the h2 above, or a hit there still wins.
	if p := findSec(secs, "startup"); p != nil && strings.Contains(p.Text, "finished booting") {
		t.Errorf("startup h2 still holds the h3's prose: %q", p.Text)
	}
}

// A bare h3 title doesn't say where you'd land — "data_path" alone is meaningless
// in a result list. It has to carry the h2 it lives under.
func TestH3CarriesItsParentH2(t *testing.T) {
	secs := parseDoc(t, schemaLike)

	for _, tc := range []struct{ id, parent string }{
		{"data_path", "docker"},
		{"keep_entrypoint", "docker"},
		{"done_regex", "startup"}, // the second h2 must replace the first as parent
	} {
		s := findSec(secs, tc.id)
		if s == nil {
			t.Errorf("%s: no section", tc.id)
			continue
		}
		if s.Parent != tc.parent {
			t.Errorf("%s: parent %q, want %q", tc.id, s.Parent, tc.parent)
		}
	}

	// An h2 has no parent — it would render as "Page › docker › docker".
	if s := findSec(secs, "docker"); s == nil || s.Parent != "" {
		t.Errorf("h2 docker got parent %q, want none", s.Parent)
	}
}

// An h3 before any h2 has no parent to name — it must not inherit a stale one
// or borrow the page title, which would render as "Page › Page › Thing".
func TestH3WithNoH2AboveItHasNoParent(t *testing.T) {
	secs := parseDoc(t, "### Straight to an h3\n\nProse.\n")
	s := findSec(secs, "straight-to-an-h3")
	if s == nil {
		t.Fatal("no section for the h3")
	}
	if s.Parent != "" {
		t.Errorf("h3 with no h2 above it got parent %q, want none", s.Parent)
	}
}

// The intro before any heading stays searchable, anchored at the page itself.
func TestIntroSurvivesAsAPageLevelSection(t *testing.T) {
	secs := parseDoc(t, schemaLike)
	if len(secs) == 0 {
		t.Fatal("no sections at all")
	}
	if secs[0].ID != "" {
		t.Errorf("first section is %q, want the page-level one with an empty ID", secs[0].ID)
	}
	if !strings.Contains(secs[0].Text, "Intro prose") {
		t.Errorf("intro prose was dropped: %q", secs[0].Text)
	}
}
