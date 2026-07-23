// Package docskb is a tiny lexical retrieval index over the panel's embedded
// user documentation, so the Kvasir chat can ground how-to guidance in the real
// docs. Deliberately boring: no embeddings, no external calls — sections are
// split at markdown headings and scored by term overlap, which is plenty for
// "how do I set up backups?" against a 200 KB corpus, and it works identically
// on every install.
package docskb

import (
	"io/fs"
	"sort"
	"strings"
)

// Section is one retrievable chunk: an H2 section of a docs page (or the page's
// preamble before its first H2).
type Section struct {
	Page  string // the page's H1, e.g. "Monitoring & alerts"
	Title string // the section heading, e.g. "Kvasir Watchers" (page title for preambles)
	Body  string // the section's markdown text
}

type KB struct {
	sections []Section
}

// Load walks every embedded .md file and splits it into sections.
func Load(fsys fs.FS) *KB {
	kb := &KB{}
	fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return nil
		}
		kb.sections = append(kb.sections, splitSections(string(raw))...)
		return nil
	})
	return kb
}

// splitSections chunks one markdown document at its H2 headings.
func splitSections(md string) []Section {
	page := ""
	var out []Section
	cur := Section{}
	flush := func() {
		body := strings.TrimSpace(cur.Body)
		if body != "" && cur.Title != "" {
			cur.Body = body
			cur.Page = page
			out = append(out, cur)
		}
		cur = Section{}
	}
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "# ") && page == "":
			page = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			cur.Title = page // preamble section carries the page title
		case strings.HasPrefix(line, "## "):
			flush()
			cur.Title = strings.TrimSpace(strings.TrimPrefix(line, "## "))
		default:
			cur.Body += line + "\n"
		}
	}
	flush()
	return out
}

// stopwords that would otherwise dominate matching. Both English (the docs'
// language) and the operator's likely fillers.
var stop = map[string]bool{
	"the": true, "and": true, "for": true, "how": true, "can": true, "you": true,
	"what": true, "with": true, "that": true, "this": true, "are": true, "does": true,
	"del": true, "til": true, "der": true, "det": true, "den": true, "har": true,
	"hvordan": true, "kan": true, "jeg": true, "med": true, "min": true, "mit": true,
}

func tokens(s string) []string {
	f := func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-')
	}
	var out []string
	for _, t := range strings.FieldsFunc(strings.ToLower(s), f) {
		if len(t) >= 3 && !stop[t] {
			out = append(out, t)
		}
	}
	return out
}

// Retrieve returns the best-matching sections for a question, at most
// maxSections and jointly capped at maxChars of body text (a long section is
// truncated rather than dropped). Empty when nothing scores — better no context
// than misleading context.
func (kb *KB) Retrieve(query string, maxSections, maxChars int) []Section {
	terms := map[string]bool{}
	for _, t := range tokens(query) {
		terms[t] = true
	}
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		s     Section
		score int
	}
	var hits []scored
	for _, sec := range kb.sections {
		titleLower := strings.ToLower(sec.Page + " " + sec.Title)
		bodyLower := strings.ToLower(sec.Body)
		score := 0
		for t := range terms {
			if strings.Contains(titleLower, t) {
				score += 4
			}
			if n := strings.Count(bodyLower, t); n > 0 {
				if n > 5 {
					n = 5
				}
				score += n
			}
		}
		if score >= 3 { // a lone glancing body hit isn't relevance
			hits = append(hits, scored{sec, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > maxSections {
		hits = hits[:maxSections]
	}
	var out []Section
	budget := maxChars
	for _, h := range hits {
		if budget <= 200 {
			break
		}
		s := h.s
		if len(s.Body) > budget {
			s.Body = s.Body[:budget] + "…"
		}
		budget -= len(s.Body)
		out = append(out, s)
	}
	return out
}
