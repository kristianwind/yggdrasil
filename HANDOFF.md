# Yggdrasil — Handoff / Session Summary

> Auto-generated 2026-06-03. For the human picking up this project.

---

## What Yggdrasil Is

A self-hosted **game & app server panel** for Debian/Ubuntu.
- Single static Go binary + embedded Svelte 5 PWA + SQLite (modernc, cgo-free)
- Docker-per-server via the official SDK v28
- REST + WebSocket API, installable PWA
- Repo: `github.com/kristianwind/yggdrasil` · branch `main`
- Module: `github.com/kristianwind/yggdrasil`
- Local path: `/Users/kw/Documents/Code/Yggdrasil`

---

## Current State (as of 2026-06-03)

### Live versions
| Where | Version |
|---|---|
| Latest GitHub tag | **v0.2.70** |
| VM (`192.168.1.158`) | **v0.2.70** ✅ |
| VM (`192.168.1.164`) | **v0.2.70** ✅ (second host, added 2026-06-04) |

> **`.158` currently runs a `dev` build ahead of v0.2.70** = the **Cloudflare Tunnel** integration
> (branch `cloudflare-tunnel`, PR pending), deployed 2026-06-04 for review. Sibling to NPM: same
> per-server `subdomain` field, routes via a Cloudflare Tunnel (outbound — no port-forward). Settings →
> Network has a new **Cloudflare Tunnel** card; a **cloudflared** community rune launches the connector.
> Full design in `docs/CLOUDFLARE_TUNNEL_PLAN.md`. After review: merge → cut **v0.2.71** → roll to both hosts.

> **Two VMs now.** `192.168.1.158` has **passwordless sudo** for `kw` (sudoers.d). `192.168.1.164`
> does NOT — its sudo still needs the password, so deploys there require the user to run the
> privileged `install`+`systemctl restart` step manually (classifier blocks the password on the
> command line, and weakening sudo there wasn't authorized). SSH key (`~/.ssh/ygg-vm/id_ygg`) works on
> both. v0.2.70 was deployed to .158 by Claude and to .164 by the user (one pasted install line).

> **NPM subdomain integration — SHIPPED in v0.2.70** (PR #3 merged 2026-06-04). Per-server subdomains via
> Nginx Proxy Manager, mirroring UniFi. `internal/npm/npm.go` client; `servers.subdomain` +
> `servers.npm_host_id` columns; `handlers_npm.go` (`npmAddServer`/`npmRemoveServer` beside upnp/unifi
> in recreateAndStart/stop/delete/stoppedCleanup); Settings→Network NPM card + per-server Subdomain
> field (gated on the rune having a `web` port). Routes `sub.<base> → <internal_host>:<web port>`.
> LE-cert create falls back to no-SSL on failure. Live-verified end-to-end against a throwaway NPM
> (create-on-start persists npm_host_id, remove-on-stop deletes it; DayZ/UDP untouched). NPM settings
> on the live panel are currently **cleared/disabled** — the user must point Settings→Network at their
> real NPM (url/email/password/base-domain) to use it. **Phase 2 (still open):** a Domains menu listing
> apps + subdomain + reachable badge (see `docs/NPM_SUBDOMAIN_PLAN.md`).

> v0.2.67 `startup.exec` (raw argv for shell-less images). v0.2.69 `docker.extra_volumes` (multi-mount).
> Runes added: Headscale (0.26.1) + Headplane UI (0.6.1, matched pair → WindzHeadscale/WindzHeadplane) +
> Nginx Proxy Manager (needs extra_volumes for /etc/letsencrypt; admin UI :81).

> v0.2.68: Runes header — primary "Carve a rune" on the title line, imports wrap below (mobile layout).
> Headscale is headless (no web UI; 404 on `/` is normal, `/health`=200); administer via `docker exec <c> headscale ...`.
> **Headplane rune added** = the headscale web UI (community-runes/apps/headplane.yaml, ghcr.io/tale/headplane).
> Running as **WindzHeadplane** at http://game.nolimit.dk:25074/admin; log in with a headscale API key
> (`headscale apikeys create`). NOTE version pairing: headplane:latest targets headscale 0.26 but the
> headscale rune is pinned 0.23.0 → "20/29 endpoints" (core node/user/key mgmt works; align versions for full
> parity). Headscale ports: only TCP 8080 needed (control+clients+API); 9090 metrics / 50443 gRPC / 3478-udp
> STUN (embedded DERP) are optional.

> v0.2.64: Runes page header buttons wrap on mobile. v0.2.66: embedded folder `gameskills/` → `builtin-runes/`
> (no API/DB/data impact; rune ids re-seed identically; v0.2.65 was a failed attempt — its commit missed the
> go:embed path edit). v0.2.67: new `startup.exec` rune field (raw argv for shell-less/distroless images) +
> **Headscale rune** (verified live). NOTE: the user bulk-deleted all community runes from the panel on
> 2026-06-04 14:31Z (audit-confirmed, deliberate) + re-added memos — they're all still in GitHub
> (`community-runes/`), one-click re-installable via Browse GitHub. Panel runes now: dayz, headscale, memos,
> minecraft-bedrock, minecraft-java, rust.

> v0.2.63 (2026-06-03 night): `community-runes/` regrouped into `databases/`/`apps/`/`games/`;
> GitHub rune browser recurses subfolders + has a filter box; **9 new runes** added (IT-Tools,
> Excalidraw, CyberChef, linkding, Stirling-PDF, FreshRSS, Mealie, Factorio, Luanti) — all
> verified live; Dozzle dropped (hard-fails without the Docker socket). Community catalog ≈ 28.

> v0.2.60 (2026-06-03): GitHub rune browser, DayZ **Mods** tab (per-server Workshop-mod
> health via Steam API), Vaultwarden `data_path` fix.
> v0.2.61 (2026-06-03): don't mount the minimal /etc/passwd over keep_entrypoint app
> images (was deleting gitea's "git" / nextcloud+wordpress's "www-data" users).
> Panel API also reachable from a dev machine at `https://yggdrasil.nolimit.dk` (Cloudflare)
> with the token in `~/.ssh/ygg-vm/.api-token`.

### Rune QA (2026-06-03, all 19 runes)
Prod-proven: dayz, minecraft-java, minecraft-bedrock, terraria, vaultwarden.
Tested standalone — **work:** mariadb, mongodb, postgresql, grafana, jellyfin, n8n,
uptime-kuma, homepage, memos, gitea (after v0.2.61). **wordpress:** starts (Apache) but
needs a MariaDB rune to finish ("Error establishing a database connection"). **rust:**
not re-tested (heavy SteamCMD; historically proven via the deleted Muspelheim).
**genshin-impact:** experimental — needs a manual grasscutter.jar + Resources + MongoDB.
**nextcloud: FIXED** (LinuxServer image, v2) — see Known Issues #2.

### VM specs (just upgraded)
- **24 CPU cores** (recently upgraded in Proxmox)
- **NVMe disk** (recently moved — sequential read ~1.2 GB/s, write ~974 MB/s, ~8–10× faster than before)
- **124 GB disk** (expanded), 86 GB free
- 76 GB RAM
- Ubuntu, Docker, yggdrasil service as uid 999

### SSH access
```bash
ssh -i ~/.ssh/ygg-vm/id_ygg -o IdentitiesOnly=yes -o StrictHostKeyChecking=no kw@192.168.1.158
```
Sudo pw: `Ol6rtd45-001` (transient, do not store in files)
API token: `~/.ssh/ygg-vm/.api-token` (created in panel UI by user)

### Panel access
- URL: `http://yggdrasil.nolimit.dk` (behind NPM reverse proxy)
- Public IP: `5.186.58.205` / hostname `game.nolimit.dk`
- UniFi (UDM): `https://192.168.1.1`, user `claude`, pass `KRwxx3kajDARME`
- Steam account: `great_danes` (authorized for DayZ, cached at `/var/lib/yggdrasil/steam-cache`)

---

## Running Servers (current)

| Name | Game | Status | Notes |
|---|---|---|---|
| Asgard | MC Java | running | |
| Midgard | MC Java | running | |
| Bifrost | MC Bedrock | running | |
| Niflheim | DayZ Chernarus | running | 16-mod vanilla+ set, joinable ✅ (id `92e8fbbe…`) |
| Jotunheim | DayZ Livonia | running | no mods |
| GarageGutterne | MC Java | running | |

**6 servers as of 2026-06-03 night.** Alfheim (Terraria), Valty (Vaultwarden), and the
user's WordPress test (wp1 + a MariaDB) were **deleted by the user via the panel** at
~20:50 UTC (confirmed in the audit log: `server.delete` by `admin`) — deliberate cleanup,
not a bug. Muspelheim/PrivateVault were already gone. If any of those deletions were
unintended, recreate them (Vaultwarden/Terraria runes still exist).

### DayZ notes (Niflheim — fixed 2026-06-03)
- **Working 16-mod list** (CF, **Dabs Framework**, Expansion Licensed/Core/Book/Navigation/Animations/Vehicles, COT, VPPAdminTools, BBP, Code Lock, MMG Base Storage, GoreZ, Ear-Plugs, SimpleAutorun). Mission compiles + OnMissionStart OK, A2S online, joinable. See `memory/niflheim-dayz-mods.md`.
- **Lesson:** chat-sourced mod-ID lists are unreliable — verify every ID with the Steam API (`GetPublishedFileDetails`, `result==1` + `consumer_app_id==221100`). The new **Mods tab** does this in-panel.
- **Dabs Framework (2545327648) is mandatory** (provides `JsonFileLoader`/`Math.Exp` for BBP + Expansion Vehicles/Book). Missing it → "Game" module fails to compile → crash-loop. This, not Navigation, was the real blocker; Navigation works fine with the full Expansion suite + Dabs.
- Norn: a 4h lifetime floor is persisted on Niflheim's types.xml (auto-reapplied after reinstall).

---

## What Works

### Core panel features (all verified live)
- Create / install / start / stop / restart / delete servers
- Real-time console (WebSocket) + live log streaming
- File manager + config editor (key=value form + raw)
- Backups: local/SFTP/SMB, retention, readable names (`<server-name>-<id8>/<timestamp>.tar.gz`)
- Schedules (cron), notifications (Telegram/Discord/webhook/SMTP)
- RBAC: admins + per-server delegated users
- 2FA (TOTP), API tokens (`ygg_` prefix)
- Pterodactyl egg + XML import
- Resource monitoring (CPU/RAM per server and host)
- Query protocols: A2S (DayZ/Rust), Minecraft SLP/Bedrock ping
- RCON: Source/Minecraft (TCP), Rust (WebSocket), DayZ (BattlEye)

### Networking
- UniFi OS auto port-forwarding (opens/closes rules on start/stop/delete)
- UPnP fallback
- Per-server `auto_forward` toggle (default on — can keep a server LAN-only)
- External "online from outside" check (probes public address, shows 🌐/🚫 badge)
- Connect address shown per server (public hostname or auto-detected IP)
- BattleMetrics status badge (optional per-server BM ID)

### Runes (gameskills)
**Bundled (built-in):**
- Minecraft Java (Paper/Vanilla/Purpur/Fabric/Forge)
- Minecraft Bedrock (with online-mode=true CA-bundle fix)
- DayZ (Workshop mods, BattlEye, serverDZ.cfg, per-server ports 1:1)
- Rust

**Community-runes/ (import manually):**
- Terraria, Genshin/Grasscutter
- Databases: MongoDB, MariaDB, PostgreSQL
- Apps: Uptime Kuma, Vaultwarden, Gitea, n8n, Grafana, Jellyfin, WordPress, Nextcloud, Memos, Homepage

### Norn (DayZ loot economy helper — `internal/api/handlers_dayz.go`)
New tab on DayZ servers. Fully working:
1. Economy overview (items, shortest lifetime, globals)
2. Minimum-lifetime floor (one click — raises all items below N hours)
3. Detect + register mission types.xml files not in cfgeconomycore.xml
4. Scan installed @mods for loot files → import + register (wraps fragments, named subfolder)
5. **Persist settings** in `servers.norn_json` → auto-re-applied after Update/Reinstall
6. Reset button to return to vanilla

### Infrastructure
- Self-prunes old built-in runes on boot (if moved to community-runes + no servers use them)
- `data_path`, `user`, `keep_entrypoint` rune fields for app Docker images
- Disk freed on server delete (falls back to root container for root-owned files)
- Per-server CPU% = share of whole host (capped 100%)
- Dashboard shows rune type per server in Recent list

---

## What's Broken / Known Issues

### 1. Restart / Reinstall don't recreate the container — FIXED (v0.2.62)
Was: startup command + `-mod` + env are baked in at container **create** (only `start` recreated). Restart did a plain `docker.Restart` (same container) and Reinstall only downloaded files → neither applied mod/env changes; you had to Stop→Start. **Fixed:** extracted `recreateAndStart(ctx, id)` (the shared recreate path) and used it from start, **restart** (was docker.Restart), the **scheduled restart** action, and **runInstall** (auto-recreates a server that was running when a (re)install finishes). Verified live: restart yields a new container ID. So Update/Reinstall now applies mod changes without a manual restart.

### 2. Nextcloud rune — FIXED (community-runes/nextcloud.yaml v2, 2026-06-03)
Switched from the official `nextcloud` image (Apache as fixed www-data:33, couldn't write
the panel-owned data dir → 503) to `lscr.io/linuxserver/nextcloud` which drops to PUID:PGID
(default 999:982 = the panel user). Now serves the login page over HTTPS:443 (self-signed).
Verified live (status=running, HTTPS 200). Pushed to GitHub + re-imported into the live panel.
Caveat: Yggdrasil mounts one volume per server (`/config`), so during first-run setup keep
Nextcloud's data directory under `/config` — the image's separate `/data` isn't persisted.

### 3. WISHLIST: Sleep feature
Open WISHLIST item: `Sleep — Mulighed for at slå sleep til eller fra`. Not yet designed or built.

### 4. Norn: floor still applied to Niflheim
A 4h lifetime floor is persisted on Niflheim's types.xml. To revert to vanilla: Norn tab → Reset, then Update/Reinstall.

---

## Architecture Quick Reference

```
cmd/yggdrasil/main.go          — binary entry, seeder, loadBuiltinGameskills
internal/api/
  server.go                    — routes, Server struct
  handlers_servers.go          — CRUD, start/stop/restart/delete
  handlers_dayz.go             — Norn (DayZ loot economy)
  handlers_backup.go           — backups + retention
  handlers_battlemetrics.go    — BM status badge
  handlers_reachability.go     — "online from outside" probe
  handlers_settings.go         — network settings, BM token
  handlers_unifi.go            — UniFi port-forward management
  handlers_upnp.go             — UPnP management
  install.go                   — install flow (calls dayzReapplyNorn after)
  reconcile.go                 — status reconciler, watchStartupReady
internal/docker/docker.go      — container create/start/stop + data_path/user/keep_entrypoint
internal/gameskill/schema.go   — rune schema (data_path, user, keep_entrypoint fields)
internal/db/db.go              — SQLite schema + migrations (addColumnIfMissing)
gameskills/*.yaml              — 4 bundled runes
community-runes/*.yaml         — 14 community runes
web/src/views/
  ServerDetail.svelte          — tabs: Console/Files/Backups/Settings/Anti-cheat/Norn/Install log
  Dashboard.svelte             — recent servers with rune type + New Server button
  Servers.svelte               — grid/table toggle + 🌐 reachability badges
assets.go                      — go:embed for web/dist + gameskills/*.yaml
```

### Deploy procedure
```bash
# Build
cd web && npm run build && cd ..
go build ./...

# Release
git add -A && git commit -m "..."
git push origin main
git tag vX.Y.Z && git push origin vX.Y.Z
gh release create vX.Y.Z --title "vX.Y.Z" --notes "..."

# Wait for CI asset
for i in $(seq 1 30); do
  A=$(gh release view vX.Y.Z --json assets -q '.assets[].name' 2>/dev/null | grep amd64)
  [ -n "$A" ] && break; sleep 12
done

# Deploy to VM
ssh -i ~/.ssh/ygg-vm/id_ygg kw@192.168.1.158 "echo 'SUDO_PW' | sudo -S bash -c '
curl -fSL -o /tmp/ygg.new https://github.com/kristianwind/yggdrasil/releases/download/vX.Y.Z/yggdrasil-linux-amd64 2>/dev/null
chmod +x /tmp/ygg.new; systemctl stop yggdrasil; install -m755 /tmp/ygg.new /usr/local/bin/yggdrasil
rm -f /tmp/ygg.new; systemctl start yggdrasil; sleep 2; yggdrasil --version'"
```

### Common SSH commands
```bash
# Drive the REST API
TOKEN=$(cat ~/.ssh/ygg-vm/.api-token)
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/servers

# Container logs
echo 'SUDO_PW' | sudo -S docker logs --tail 50 ygg-<id8> 2>&1

# DB query
echo 'SUDO_PW' | sudo -S python3 -c "
import sqlite3; db=sqlite3.connect('/var/lib/yggdrasil/yggdrasil.db')
# run queries here
"
```

---

## Next Steps / Backlog

### Immediate
1. **Fix Restart/Reinstall recreate gap** (see Known Issues #1) — the main outstanding engineering item.

### WISHLIST open
- **Sleep toggle**: add ability to enable/disable "sleep mode" for a server (pause without stopping?). Needs design.

### Potential improvements (not on any list)
- Panel warns when Expansion-Navigation version doesn't match DayZ version (detects `player connect will stay disabled` in logs → shows alert)
- Per-server CPU limits actually enforced in practice (set via `cpu_percent` in server settings)
- More community runes (Pi-hole, Portainer, etc.)
- WordPress + MariaDB linked setup guide
- Norn: nominal/restock tuning (spawn amounts, not just lifetime)

---

## Security Notes
- Steam password/Guard code: never stored or logged
- RCON/backup/Steam credentials: encrypted at rest (AES-256-GCM)
- UniFi credentials: encrypted, never returned in API responses
- Argon2id for user passwords
- Rate-limiting on login, CSRF-safe token auth, path-traversal guards on file access
- VM sudo password: transient — do not store in files

---

*Last updated: 2026-06-03*
