# Yggdrasil Panel — website

The public site for yggdrasilpanel.com: a landing page and the documentation.
Static HTML, inline or file CSS, no framework, no CDN, nothing that phones home.

| Path | What it is |
| --- | --- |
| `index.html` | The landing page. Hand-written, self-contained, edited directly. |
| `screenshots/` | Landing-page screenshots. |
| `docs/` | **Generated — do not edit by hand.** See below. |

## The docs are generated

`website/docs/` is built from the markdown in [`docs/`](../docs/) by
[`cmd/docs-gen`](../cmd/docs-gen). `docs/` is the single source of truth: it renders on
GitHub for contributors and here for everyone else, and nothing is written twice.

After editing anything under `docs/`, regenerate from the repo root:

```bash
go run ./cmd/docs-gen
```

Then commit the result. The output is committed on purpose, so deploying the site stays a
file copy with no build step. CI regenerates and fails if `website/docs/` has drifted, so a
markdown edit can't silently ship a stale page.

### Adding or moving a page

Edit the `pages` list in [`cmd/docs-gen/main.go`](../cmd/docs-gen/main.go). It fixes the
sidebar order, the labels, and the index blurbs.

Each page has an `OnSite` flag. `false` keeps a page GitHub-only: it isn't rendered, and
every link to it — from the sidebar, the index, and inside other pages — is rewritten to
the GitHub blob URL, so links stay live rather than 404. The deep reference (`api.md`,
`rune-schema.md`) ships that way today, because it changes with the code and its readers
are already on GitHub. Publishing it here is one flag, not a rewrite.

### What the generator does

- Renders GitHub-flavoured markdown, keeping the landing page's design tokens so the docs
  read as the same site.
- Rewrites relative markdown links to their `.html` equivalent, or to GitHub for
  unpublished pages.
- Builds a sidebar, a per-page table of contents with scroll-spy, and a search index of
  every `##` section — so a hit deep-links to the section that matched instead of dumping
  the reader at the top of a long page.
- Adds a copy button to every code block, and a lightbox to every screenshot.
- Search runs client-side over one small JSON file. There is no search service and no
  analytics: consistent with the project's [no-telemetry stance](../docs/guides/status-page-and-beacon.md).

## Host it with Yggdrasil itself

1. **Runes → Browse GitHub →** install **"Static site (nginx)"**.
2. **Create server** from that rune, then **Start** it.
3. Open the server's **Files** tab and **drag & drop** this whole `website/` folder
   (or its contents) into the web root.
4. Give the server a **Subdomain** (**Settings → Domains**: NPM or Cloudflare Tunnel), or
   point a Cloudflare Tunnel public hostname at the server's web port.

That's the panel hosting its own site. 🌳
