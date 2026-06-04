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

// GameskillsFS contains the four built-in rune (gameskill) definitions, embedded
// from the builtin-runes/ folder (community runes live in community-runes/ and
// are imported, not embedded).
//
//go:embed builtin-runes/*.yaml
var GameskillsFS embed.FS
