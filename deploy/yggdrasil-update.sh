#!/usr/bin/env bash
# yggdrasil-update — in-panel self-update helper.
#
# Installed root-owned (0755) by install.sh; the panel (running as the
# unprivileged `yggdrasil` user) invokes it through a narrowly-scoped NOPASSWD
# sudoers rule. It downloads the requested OFFICIAL release, verifies its
# SHA-256 against the release's checksums.txt, sanity-checks it runs, atomically
# replaces the binary, and schedules a service restart in an independent
# transient unit so the running panel being killed by the restart can't
# interrupt the restart itself. The only untrusted input is the version tag,
# which is strictly validated — so this can only ever install a checksum-matched
# official release, never arbitrary code.
set -euo pipefail

REPO="kristianwind/yggdrasil"
BIN="/usr/local/bin/yggdrasil"
TAG="${1:-}"

if ! printf '%s' "$TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "invalid version tag: '$TAG' (expected vMAJOR.MINOR.PATCH)" >&2
  exit 2
fi

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 3 ;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BASE="https://github.com/$REPO/releases/download/$TAG"

echo "Downloading $TAG ($ARCH)..."
curl -fsSL -o "$TMP/ygg" "$BASE/yggdrasil-linux-$ARCH"
curl -fsSL -o "$TMP/sums" "$BASE/checksums.txt"

EXPECT="$(awk -v f="yggdrasil-linux-$ARCH" '$2==f || $2=="*"f {print $1}' "$TMP/sums" | head -n1)"
ACTUAL="$(sha256sum "$TMP/ygg" | awk '{print $1}')"
if [ -z "$EXPECT" ] || [ "$EXPECT" != "$ACTUAL" ]; then
  echo "checksum verification failed (expected '$EXPECT', got '$ACTUAL')" >&2
  exit 4
fi

chmod +x "$TMP/ygg"
if ! "$TMP/ygg" --version >/dev/null 2>&1; then
  echo "downloaded binary failed to run — aborting" >&2
  exit 5
fi

install -m 0755 "$TMP/ygg" "$BIN"
echo "installed $TAG -> $BIN"

# Restart detached from this process (a child of the running service) so the
# restart survives our own cgroup being torn down.
if command -v systemd-run >/dev/null 2>&1; then
  systemd-run --on-active=2 --timer-property=AccuracySec=100ms \
    systemctl restart yggdrasil >/dev/null 2>&1 && echo "restart scheduled" && exit 0
fi
# Fallback: best-effort direct restart.
systemctl restart yggdrasil || true
echo "restart requested"
