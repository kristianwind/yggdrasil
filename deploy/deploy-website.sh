#!/usr/bin/env bash
#
# Deploy yggdrasilpanel.com.
#
# The site has no build step — website/ is committed whole, docs/ and apps/ are
# generated into it and committed too. That makes deploying a file copy, which is
# why it kept being done by hand, and why it kept drifting: on 2026-08-28 the live
# apps catalogue was missing two runes that had been on main for days. CI checks
# the generated pages match the repo; nothing checked the repo matched the box.
#
# So this script regenerates first and REFUSES to deploy if that produced a diff.
# A dirty tree there means someone edited a generated page by hand or forgot to
# commit a regen, and copying it to the box would bake the mistake into the site.
#
# Usage:  deploy/deploy-website.sh [--dry-run]
set -euo pipefail

HOST="${YGG_SITE_HOST:-kw@100.80.130.8}"
DIR="${YGG_SITE_DIR:-/var/lib/yggdrasil/servers/1324053b-ac82-4ac0-a44a-d625a085175a}"
OWNER="${YGG_SITE_OWNER:-yggdrasil:yggdrasil}"
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY=1

cd "$(dirname "$0")/.."

echo "==> Regenerating docs and apps pages"
go run ./cmd/docs-gen >/dev/null
go run ./cmd/apps-gen >/dev/null

if ! git diff --quiet -- website/ docs/; then
  echo "ERROR: website/ or docs/ differs from HEAD:" >&2
  git diff --stat -- website/ docs/ >&2
  echo >&2
  echo "Either you have uncommitted local edits, or a source changed and its" >&2
  echo "generated pages were never committed. Deploying now would put something" >&2
  echo "live that is in no commit, so nothing could tell you later what is on the" >&2
  echo "box. Commit it (or revert it) and run again." >&2
  exit 1
fi
echo "    clean — repo matches its generators"

if [ -n "$DRY" ]; then
  echo "==> Dry run; would deploy $(du -sh website | cut -f1) to $HOST:$DIR"
  exit 0
fi

STAMP=$(date +%Y-%m-%d-%H%M)
echo "==> Backing up the live site"
ssh -o ConnectTimeout=20 "$HOST" \
  "sudo tar -czf /var/backups/yggdrasilpanel-site-$STAMP.tgz -C '$DIR' ."

# README.md is a developer note and must not be published. ._* are macOS
# AppleDouble resource forks — 106 of them were being served publicly before this
# script existed, because the site had once been copied from the Mac by hand.
echo "==> Copying"
tar -czf - --exclude='README.md' --exclude='._*' --exclude='.DS_Store' -C website . \
  | ssh -o ConnectTimeout=20 "$HOST" "sudo tar -xzf - -C '$DIR'"

echo "==> Removing anything the repo no longer has"
ssh -o ConnectTimeout=20 "$HOST" \
  "sudo find '$DIR' -name '._*' -type f -delete; sudo rm -f '$DIR/README.md'"

echo "==> Normalising ownership"
ssh -o ConnectTimeout=20 "$HOST" "
  sudo chown -R $OWNER '$DIR'
  sudo find '$DIR' -type d -exec chmod 755 {} +
  sudo find '$DIR' -type f -exec chmod 644 {} +"

# Verify against the origin, not through Cloudflare: a CDN hit would report the
# old file as success and this check exists precisely to catch a copy that didn't
# land.
echo "==> Verifying"
LOCAL=$(md5sum website/apps/index.html | cut -d' ' -f1)
REMOTE=$(ssh -o ConnectTimeout=20 "$HOST" "sudo md5sum '$DIR/apps/index.html'" | cut -d' ' -f1)
if [ "$LOCAL" != "$REMOTE" ]; then
  echo "ERROR: apps/index.html differs after deploy ($LOCAL != $REMOTE)" >&2
  exit 1
fi
echo "    apps/index.html matches ($LOCAL)"
echo
echo "Deployed. Backup: /var/backups/yggdrasilpanel-site-$STAMP.tgz"
echo "Note: HTML is served with cf-cache-status: DYNAMIC, so this is live at once."
echo "If a Cloudflare cache rule is ever added, purge the cache after deploying."
