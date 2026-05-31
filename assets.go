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

// GameskillsFS contains the four bundled gameskill definitions.
//
//go:embed gameskills/*.yaml
var GameskillsFS embed.FS
