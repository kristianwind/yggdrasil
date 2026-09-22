package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The analytics tag lives in four places — the hand-written front page, two
// docs-gen templates and apps-gen's own — because the site has no shared layout.
// Four copies of one line is exactly the shape that drifts: someone adds a page,
// or edits one template, and a page quietly stops being counted. Nothing about
// the site looks wrong when that happens; the numbers just get smaller.
//
// So assert the property rather than the copies: every page the site publishes
// carries it.
const siteAnalyticsTag = "plausible.yggdrasilpanel.com/js/script.js"

func TestEveryPublishedPageCarriesTheAnalyticsTag(t *testing.T) {
	root := filepath.Join("..", "..", "website")
	var missing []string
	n := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		n++
		if !strings.Contains(string(b), siteAnalyticsTag) {
			rel, _ := filepath.Rel(root, path)
			missing = append(missing, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if n == 0 {
		t.Fatalf("no HTML found under %s — this test would pass on an empty site", root)
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d pages have no analytics tag:\n  %s\n\n"+
			"Generated pages come from cmd/docs-gen and cmd/apps-gen; regenerate and commit. "+
			"A hand-written page needs the tag added to its <head>.",
			len(missing), n, strings.Join(missing, "\n  "))
	}
}

// The favicon has the same four-copies problem as the analytics tag, and it bit
// in exactly the way the comment above predicts. On 2026-09-21 the emoji
// data-URI was replaced with real icon files in the hand-written pages and in
// both docs-gen templates — and missed in apps-gen's. Worse, the fix was applied
// to apps-gen's *generated output* by hand, so the page looked right while the
// generator still produced the old line: CI's staleness check went red on a
// change that had, to any reader of the site, already worked.
//
// Assert the property on every published page, both halves. "Has a favicon link"
// alone would pass on the emoji one, which is the state being moved away from.
const (
	siteFaviconLink = `<link rel="icon" href="/favicon.svg"`
	siteEmojiIcon   = "data:image/svg+xml,%3Csvg"
)

func TestEveryPublishedPageCarriesTheRealFavicon(t *testing.T) {
	root := filepath.Join("..", "..", "website")
	var missing, stillEmoji []string
	n := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		n++
		rel, _ := filepath.Rel(root, path)
		body := string(b)
		if !strings.Contains(body, siteFaviconLink) {
			missing = append(missing, rel)
		}
		if strings.Contains(body, siteEmojiIcon) {
			stillEmoji = append(stillEmoji, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if n == 0 {
		t.Fatalf("no HTML found under %s — this test would pass on an empty site", root)
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d pages have no favicon link:\n  %s\n\n"+
			"Generated pages come from cmd/docs-gen and cmd/apps-gen — fix the GENERATOR, "+
			"not its output, then regenerate and commit.",
			len(missing), n, strings.Join(missing, "\n  "))
	}
	if len(stillEmoji) > 0 {
		t.Errorf("%d of %d pages still carry the emoji data-URI icon:\n  %s",
			len(stillEmoji), n, strings.Join(stillEmoji, "\n  "))
	}
}
