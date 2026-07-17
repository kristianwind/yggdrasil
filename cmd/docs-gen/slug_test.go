package main

import "testing"

// The anchors this produces must match GitHub's, because docs/*.md renders in
// both places and one link has to land in both.
//
// Every expectation below was taken from GitHub's own renderer, not from memory:
//
//	curl -s https://api.github.com/markdown/raw -X POST \
//	  -H 'Content-Type: text/x-markdown' --data-binary $'## `config_files`\n'
//
// If you change a case, check it the same way. The surprising ones are real:
// punctuation is dropped rather than hyphenated ("variables[].min" →
// "variablesmin"), but the spaces around it still become hyphens, so "Copy &
// paste" yields two ("copy--paste").
func TestGithubSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		// The one that broke: goldmark's default gives "config-files".
		{"config_files", "config_files"},
		{"data_path and the working directory", "data_path-and-the-working-directory"},
		{"variables[].min", "variablesmin"}, // brackets and dots are dropped, not hyphenated

		{"Getting started", "getting-started"},
		{"ports", "ports"},
		{"Copy & paste", "copy--paste"}, // each space around the & becomes a hyphen
		{"What's in a backup?", "whats-in-a-backup"},
		{"Start, stop, restart", "start-stop-restart"},
		{"The /status.js split", "the-statusjs-split"},
		{"Kvasir — the AI assistant", "kvasir--the-ai-assistant"},
		{"HTTP API", "http-api"},
		{"  Trimmed  ", "trimmed"},
	}
	for _, c := range cases {
		if got := githubSlug(c.in); got != c.want {
			t.Errorf("githubSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A page with two identical headings gets -1, -2 … like GitHub.
func TestGithubIDsDeduplicates(t *testing.T) {
	g := newGithubIDs()
	want := []string{"see-also", "see-also-1", "see-also-2"}
	for i, w := range want {
		if got := string(g.Generate([]byte("See also"), 0)); got != w {
			t.Errorf("occurrence %d = %q, want %q", i+1, got, w)
		}
	}
}

// The table is per document, so page two starts clean rather than inheriting
// page one's "-1".
func TestGithubIDsAreScopedPerDocument(t *testing.T) {
	a, b := newGithubIDs(), newGithubIDs()
	a.Generate([]byte("See also"), 0)
	if got := string(b.Generate([]byte("See also"), 0)); got != "see-also" {
		t.Errorf("a fresh document produced %q, want an unsuffixed anchor", got)
	}
}

func TestGithubSlugEmptyHeading(t *testing.T) {
	g := newGithubIDs()
	if got := string(g.Generate([]byte("!!!"), 0)); got != "section" {
		t.Errorf("a heading of pure punctuation gave %q, want a stable fallback", got)
	}
}
