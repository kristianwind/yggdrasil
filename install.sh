#!/usr/bin/env bash
#
# Yggdrasil installer — single-command install / upgrade / repair.
#
#   curl -fsSL https://raw.githubusercontent.com/yggdrasil-panel/yggdrasil/main/install.sh | sudo bash
#
# Idempotent: safe to re-run to upgrade the binary or repair the install.
set -euo pipefail

REPO="${YGG_REPO:-yggdrasil-panel/yggdrasil}"
VERSION="${YGG_VERSION:-latest}"
BIN_PATH="/usr/local/bin/yggdrasil"
CONFIG_DIR="/etc/yggdrasil"
CONFIG_FILE="$CONFIG_DIR/config.yaml"
DATA_DIR="/var/lib/yggdrasil"
SERVICE_USER="yggdrasil"
SERVICE_FILE="/etc/systemd/system/yggdrasil.service"

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!! \033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx \033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "Please run as root (use sudo)."

# --- Distro check ---------------------------------------------------------
. /etc/os-release 2>/dev/null || die "Cannot detect distribution (/etc/os-release missing)."
case "${ID:-}${ID_LIKE:-}" in
  *debian*|*ubuntu*) : ;;
  *) die "Unsupported distro '${PRETTY_NAME:-unknown}'. Yggdrasil supports Debian/Ubuntu." ;;
esac
log "Detected ${PRETTY_NAME}"

# --- Architecture ---------------------------------------------------------
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "Unsupported architecture: $(uname -m)" ;;
esac

# --- Base utilities -------------------------------------------------------
log "Installing base dependencies (curl, ca-certificates)..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl ca-certificates >/dev/null

# --- Docker ---------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  log "Installing Docker Engine via official convenience script..."
  curl -fsSL https://get.docker.com | sh
else
  log "Docker already installed: $(docker --version)"
fi
systemctl enable --now docker >/dev/null 2>&1 || warn "Could not enable docker service automatically."

# --- Service user ---------------------------------------------------------
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  log "Creating service user '$SERVICE_USER'..."
  useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi
usermod -aG docker "$SERVICE_USER"

# --- Directories ----------------------------------------------------------
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
install -d "$CONFIG_DIR"

# --- Binary ---------------------------------------------------------------
if [ "$VERSION" = "latest" ]; then
  DL_URL="https://github.com/$REPO/releases/latest/download/yggdrasil-linux-$ARCH"
else
  DL_URL="https://github.com/$REPO/releases/download/$VERSION/yggdrasil-linux-$ARCH"
fi
log "Downloading Yggdrasil binary ($ARCH)..."
if curl -fsSL -o "$BIN_PATH.new" "$DL_URL"; then
  chmod +x "$BIN_PATH.new"
  mv "$BIN_PATH.new" "$BIN_PATH"
  log "Installed $($BIN_PATH version 2>/dev/null || echo yggdrasil)"
else
  rm -f "$BIN_PATH.new"
  if [ -x "$BIN_PATH" ]; then
    warn "Could not download release; keeping existing binary."
  else
    die "Could not download binary from $DL_URL (no release published yet?)."
  fi
fi

# --- Config (first install only) -----------------------------------------
FIRST_PW=""
if [ ! -f "$CONFIG_FILE" ]; then
  log "Generating initial config..."
  FIRST_PW="$(head -c 12 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 16)"
  SECRET="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
  cat > "$CONFIG_FILE" <<EOF
server:
  host: "0.0.0.0"
  port: 8080
database:
  path: "$DATA_DIR/yggdrasil.db"
auth:
  secret_key: "$SECRET"
  session_ttl_hours: 168
docker:
  socket: "unix:///var/run/docker.sock"
ports:
  range_min: 25000
  range_max: 30000
admin:
  username: "admin"
  password: "$FIRST_PW"
EOF
  chmod 640 "$CONFIG_FILE"
  chown root:"$SERVICE_USER" "$CONFIG_FILE"
else
  log "Existing config found; leaving it untouched."
fi

# --- systemd unit ---------------------------------------------------------
log "Installing systemd service..."
curl -fsSL -o "$SERVICE_FILE" "https://raw.githubusercontent.com/$REPO/main/deploy/yggdrasil.service" 2>/dev/null || \
cat > "$SERVICE_FILE" <<'EOF'
[Unit]
Description=Yggdrasil Game Server Management Panel
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
[Service]
Type=simple
User=yggdrasil
Group=yggdrasil
ExecStart=/usr/local/bin/yggdrasil --config /etc/yggdrasil/config.yaml
Restart=on-failure
RestartSec=5
WorkingDirectory=/var/lib/yggdrasil
[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable yggdrasil >/dev/null 2>&1 || true
systemctl restart yggdrasil

# --- Done -----------------------------------------------------------------
IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
log "Yggdrasil is installed and running."
echo "   URL:   http://${IP:-localhost}:8080"
if [ -n "$FIRST_PW" ]; then
  echo "   Login: admin / $FIRST_PW"
  echo "   (Change this password after first login; then clear it from $CONFIG_FILE.)"
else
  echo "   Login: use your existing admin credentials."
fi
echo
echo "   Logs:  journalctl -u yggdrasil -f"
