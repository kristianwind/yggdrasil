package main

import (
	"bytes"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/ast"
)

// githubIDs generates heading anchors the way GitHub does.
//
// This matters because the docs render in two places. `docs/*.md` is read on
// GitHub and rendered onto the site, and a link like
// `[config_files](../reference/rune-schema.md#config_files)` has to land in both.
// One link, two sluggers — so they have to agree, and GitHub's is the one that
// can't be changed.
//
// goldmark's default replaces every non-alphanumeric run with a hyphen, so
// `## `config_files“ becomes `#config-files`. GitHub keeps the underscore and
// yields `#config_files`. Every doc link was written against GitHub's, so the
// site's copies quietly 404'd on the difference — one link did, and any future
// heading with an underscore would have too.
//
// The rules, from github-slugger:
//   - lowercase
//   - drop punctuation, keeping letters, digits, `-` and `_`
//   - spaces become hyphens
//   - a repeat gets `-1`, `-2`, … appended
type githubIDs struct {
	used map[string]int
}

func newGithubIDs() *githubIDs { return &githubIDs{used: map[string]int{}} }

func (g *githubIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	slug := githubSlug(string(value))
	if slug == "" {
		// GitHub emits an empty anchor here; a stable fallback is more useful than
		// nothing, and headings like this don't exist in these docs anyway.
		slug = "section"
	}
	if n, seen := g.used[slug]; seen {
		g.used[slug] = n + 1
		slug = slug + "-" + strconv.Itoa(n)
	} else {
		g.used[slug] = 1
	}
	return []byte(slug)
}

func (g *githubIDs) Put(value []byte) { g.used[string(value)] = 1 }

func githubSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('-')
		default:
			// Punctuation and emoji are dropped, not replaced — "Copy & paste"
			// becomes "copy--paste" on GitHub, two hyphens, because the spaces
			// around the ampersand each become one.
		}
	}
	return b.String()
}

// slugOf is githubSlug over a heading's rendered text, for tests and for any
// caller that needs to predict an anchor.
func slugOf(heading []byte) string { return githubSlug(string(bytes.TrimSpace(heading))) }
