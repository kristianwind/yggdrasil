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
