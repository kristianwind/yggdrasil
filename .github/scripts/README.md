# CI scripts

## `discord_release.py`

Posts a release announcement to a Discord webhook. Run by the `announce` job in
[`release.yml`](../workflows/release.yml) after the binaries are published.

It reads the **tag's annotation** on stdin — not GitHub's generated notes — because the tag
message is a hand-written summary of what shipped, which is what a reader wants. A list of
merge commits isn't.

### Setting it up

1. In Discord: **the channel → Edit Channel → Integrations → Create Webhook**. Copy the URL.
2. In GitHub: **Settings → Secrets and variables → Actions → New repository secret**, named
   `DISCORD_RELEASE_WEBHOOK`. Paste the URL there.

That's the whole setup. The next `git tag -a vX.Y.Z && git push origin vX.Y.Z` announces itself.

**The webhook URL is a credential** — anyone holding it can post to that channel as the app.
It belongs in the repository secret and nowhere else: not in a commit, not in a chat message,
not pasted into an issue. Rotate it in Discord if it ever leaks; the webhook page has a delete
and a re-create.

### Behaviour worth knowing

- **No secret configured → it skips and exits 0.** Forks and a fresh clone don't get a red X
  on every release.
- **Discord unreachable or rejecting → it still exits 0.** The binaries are already published
  at that point; a missed chat message is not worth failing a release over. The error is
  printed to the log.
- The webhook is never echoed, including on the error paths.
- The embed clamps to Discord's limits (title 256, description 4096) rather than letting an
  over-long tag message turn into a 400.

### Why a webhook and not a bot

Nothing to host, no gateway connection to keep alive, and no token with server-wide reach. It
is the same shape as the panel's own Discord status board — see
[Status page and beacon](../../docs/guides/status-page-and-beacon.md).
