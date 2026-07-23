// Package yggdrasil holds the embedded assets (built frontend + bundled
// gameskills). It lives at the module root because go:embed cannot reference
// paths outside the embedding file's directory.
package yggdrasil

import "embed"

// WebFS contains the built Svelte frontend under web/dist.
// `all:` ensures dotfiles (e.g. service worker assets) are included.
//
//go:embed all:web/dist
var WebFS embed.FS

// GameskillsFS contains the built-in rune (gameskill) definitions, embedded
// from the builtin-runes/ folder (community runes live in community-runes/ and
// are imported, not embedded).
//
//go:embed builtin-runes/*.yaml
var GameskillsFS embed.FS

// DocsFS carries the user documentation (getting started, guides, reference) so
// the Kvasir chat can ground its guidance in the real docs — retrieval happens
// per question, only excerpts ever reach the model. Design notes in docs/ are
// deliberately not embedded.
//
//go:embed docs/getting-started.md docs/guides/*.md docs/reference/*.md
var DocsFS embed.FS
