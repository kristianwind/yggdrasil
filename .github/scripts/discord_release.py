#!/usr/bin/env python3
"""Post a release announcement to a Discord webhook.

Reads the tag's annotation on stdin and turns it into an embed. Called from
.github/workflows/release.yml; skips silently when no webhook is configured.

A webhook rather than a bot on purpose: nothing to host, no gateway to keep
alive, and no token with server-wide reach — the same shape as the panel's own
Discord status board.
"""

import json
import os
import sys
import urllib.error
import urllib.request

# Discord's documented ceilings. Exceeding one is a 400, not a truncation, so
# clamp rather than hope the notes are short.
MAX_TITLE = 256
MAX_DESC = 4096
BRAND_GREEN = 0x22C55E  # --accent, matching the site and the panel

INSTALL = (
    "curl -fsSL https://raw.githubusercontent.com/"
    "kristianwind/yggdrasil/main/install.sh | sudo bash"
)


def clamp(s: str, limit: int) -> str:
    s = s.strip()
    if len(s) <= limit:
        return s
    return s[: limit - 1].rstrip() + "…"


def build_embed(tag: str, repo: str, notes: str) -> dict:
    """Split the tag annotation into a headline and a body.

    Tag messages here are written as "v0.2.148 — what changed" followed by a blank
    line and the detail, so the first line is already a usable title.
    """
    lines = notes.strip().split("\n")
    subject = lines[0].strip() if lines else tag
    body = "\n".join(lines[1:]).strip()

    # If the subject just repeats the tag, don't say it twice.
    title = subject if subject and subject != tag else f"Yggdrasil Panel {tag}"
    if not title.lower().startswith(("yggdrasil", "v0", "v1")):
        title = f"{tag} — {title}"

    if not body:
        body = "See the release notes on GitHub."

    release_url = f"https://github.com/{repo}/releases/tag/{tag}"

    return {
        "title": clamp(f"🌳 {title}", MAX_TITLE),
        "url": release_url,
        "description": clamp(body, MAX_DESC),
        "color": BRAND_GREEN,
        "fields": [
            {
                "name": "Install or update",
                "value": f"```bash\n{INSTALL}\n```",
                "inline": False,
            },
            {
                "name": "Links",
                "value": (
                    f"[Release notes]({release_url}) · "
                    f"[Documentation](https://yggdrasilpanel.com/docs/) · "
                    f"[Report a bug](https://github.com/{repo}/issues)"
                ),
                "inline": False,
            },
        ],
        "footer": {"text": "yggdrasilpanel.com"},
    }


def main() -> int:
    webhook = os.environ.get("WEBHOOK", "").strip()
    if not webhook:
        print("No webhook configured — skipping.")
        return 0

    tag = os.environ.get("TAG", "").strip() or "dev"
    repo = os.environ.get("REPO", "kristianwind/yggdrasil").strip()
    notes = sys.stdin.read()

    payload = {
        "username": "Yggdrasil Panel",
        "embeds": [build_embed(tag, repo, notes)],
    }

    req = urllib.request.Request(
        webhook,
        data=json.dumps(payload).encode(),
        # A User-Agent is required. Discord sits behind Cloudflare, which blocks
        # urllib's default "Python-urllib/x" signature with HTTP 403 "error code:
        # 1010" — the request never reaches Discord. Any real UA gets through.
        headers={
            "Content-Type": "application/json",
            "User-Agent": "Yggdrasil-Panel-Release (+https://github.com/kristianwind/yggdrasil)",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            print(f"Announced {tag} to Discord (HTTP {resp.status}).")
        return 0
    except urllib.error.HTTPError as e:
        # Never echo the webhook itself — the URL is the credential.
        print(f"Discord rejected the announcement: HTTP {e.code} {e.reason}", file=sys.stderr)
        print(e.read().decode("utf-8", "replace")[:500], file=sys.stderr)
    except Exception as e:  # noqa: BLE001 — a failed announcement must not fail the release
        print(f"Could not reach Discord: {type(e).__name__}: {e}", file=sys.stderr)

    # The binaries are already published; a missed Discord post is not worth a
    # red release.
    print("Continuing — the release itself is unaffected.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
