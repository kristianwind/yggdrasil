#!/usr/bin/env bash
# yggdrasil-update — runs as a ROOT systemd oneshot (yggdrasil-update.service),
# triggered by yggdrasil-update.path when the panel drops a request file into its
# state dir. Because it runs in its own UNsandboxed unit, it works even though
# the panel service itself is hardened (NoNewPrivileges + ProtectSystem, which
# make a panel-spawned `sudo` impossible and /usr read-only).
#
# It reads the requested tag, downloads the OFFICIAL release, verifies its
# SHA-256 against the release checksums, sanity-checks it runs, replaces the
# binary, and restarts the panel. The tag is the only input and is strictly
# validated — so this can only ever install a checksum-matched official release.
set -uo pipefail

STATE_DIR="/var/lib/yggdrasil"
REQ="$STATE_DIR/.update-request"
STATUS="$STATE_DIR/.update-status"
REPO="kristianwind/yggdrasil"
BIN="/usr/local/bin/yggdrasil"
TAG=""

write_status() { # <state> <message>
  printf '{"state":"%s","tag":"%s","message":"%s","at":"%s"}\n' \
    "$1" "$TAG" "$(printf '%s' "$2" | tr -d '"\n')" "$(date -u +%FT%TZ 2>/dev/null)" \
    > "$STATUS" 2>/dev/null || true
  chown yggdrasil:yggdrasil "$STATUS" 2>/dev/null || true
}

# Consume the request (sanitized to the tag charset) and remove it so the path
# unit re-arms and we never loop.
[ -f "$REQ" ] && TAG="$(head -n1 "$REQ" | tr -dc 'v0-9.')"
rm -f "$REQ"

if ! printf '%s' "$TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  write_status error "invalid or missing version tag"
  exit 2
fi

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) write_status error "unsupported architecture"; exit 3 ;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BASE="https://github.com/$REPO/releases/download/$TAG"
write_status running "downloading $TAG"

if ! curl -fsSL -o "$TMP/ygg" "$BASE/yggdrasil-linux-$ARCH" \
  || ! curl -fsSL -o "$TMP/sums" "$BASE/checksums.txt"; then
  write_status error "download failed"
  exit 4
fi

EXPECT="$(awk -v f="yggdrasil-linux-$ARCH" '$2==f || $2=="*"f {print $1}' "$TMP/sums" | head -n1)"
ACTUAL="$(sha256sum "$TMP/ygg" | awk '{print $1}')"
if [ -z "$EXPECT" ] || [ "$EXPECT" != "$ACTUAL" ]; then
  write_status error "checksum verification failed"
  exit 5
fi

chmod +x "$TMP/ygg"
if ! "$TMP/ygg" --version >/dev/null 2>&1; then
  write_status error "downloaded binary failed to run"
  exit 6
fi

install -m 0755 "$TMP/ygg" "$BIN"
write_status done "installed $TAG"
# Separate unit from yggdrasil.service, so restarting the panel doesn't kill us.
systemctl restart yggdrasil
