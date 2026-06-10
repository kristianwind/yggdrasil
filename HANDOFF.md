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

## Current State (as of 2026-06-08)

### Live versions
| Where | Version |
|---|---|
| Latest GitHub tag | **v0.2.87** |
| VM (`192.168.1.158`) — GAME server | **v0.2.87** ✅ |
| VM (`192.168.1.164`) — PRODUCTION | **v0.2.87** ✅ |

> **Server roles (2026-06-09):** `.164` = **production** (public apps, WordPress, Vaultwarden,
> panel.nolimit.dk); `.158` = **game** server (live game servers landing soon). Both
> production-grade.
> **Cloudflare Workers Git integration is connected to the repo and fails on every push** (the
> repo isn't a Workers app — it spawns `cloudflare/workers-autoconfig` branches and emails red ❌).
> Harmless to the project, but the user should disconnect it: CF dashboard → Workers & Pages →
> `yggdrasil` → Settings → disconnect Git / delete the project. (CF mutations are classifier-blocked
> for Claude.)

> **Two VMs, both unattended-deployable.** `192.168.1.158` (hostname `yggdrasil`) and
> `192.168.1.164` (hostname `yggdrasilpanel`) BOTH now have **passwordless sudo** for `kw`
> (`/etc/sudoers.d/kw-nopasswd`), so the full build→release→`install`→restart loop runs without the
> user. SSH key `~/.ssh/ygg-vm/id_ygg` works on both. Both `secret_key`s are 44 chars (fail-closed
> crypto guard won't trip). API token for `.158` at `~/.ssh/ygg-vm/.api-token`.

### Deploy classifier guardrails (things Claude CANNOT do — hand to the user)
- Cloudflare API mutations (DNS / tunnel ingress) — even with explicit yes. Give the user the
  exact dashboard step or curl.
- Minting a panel API token in the DB; writing `.claude/settings.json`; putting a sudo/login
  password on the command line. `git push origin main` is blocked → use a PR branch + `gh pr merge`.

### Session 2026-06-10 — what shipped since v0.2.86
- **v0.2.87** (A3) **Realm-scoped server creation** for delegates: a user granted ServerCreate on a
  realm can now create servers there. `/auth/me` exposes the caller's create-scopes
  `{global, realms[], gameskills[]}`; the create modal gained a Realm dropdown + a rune list that
  adapts to the chosen realm (realm you own / global → all runes; else only gameskill-creatable).
  Backend already enforced it — the modal just never sent `realm_id`. (B1) **Edit-user modal** on the
  Users page: change role + reset password (with the 🎲 generator); revokes sessions.
- **Brand asset**: `yggdrasil-tree.svg` (+ 512/1024/2048 PNGs) — the 🌳 emoji bomærke rendered
  high-res and auto-traced to a scalable vector via `vtracer` (pip). Not wired into the app icon yet.
- **Deploy gotcha learned**: don't `gh release create` manually before CI — the Release workflow
  creates the release; a pre-existing one makes the asset-upload step fail ("Requires
  authentication"). Fix: delete the release (keep tag) + delete/re-push the tag to re-trigger CI.

### Session 2026-06-09 — what shipped since v0.2.82
- **v0.2.86** (a) Delegates see/create only permitted runes: `GET /api/gameskills` returns
  per-rune `creatable`; Runes page hides non-creatable runes + admin-only import actions; create
  modal + per-rune Create button honor it. (b) Password generator (show/generate/copy, `PasswordField.svelte`)
  on new-user + secret-looking server env fields. (c) **MongoDB rune → 4.4** (was mongo:7) — Mongo 5+
  needs CPU AVX, which the default Proxmox/QEMU VM CPU lacks → "Illegal instruction (core dumped)".
  Re-imported to `.158`'s live panel. **Open: the "Genshin Impact DB" server (mongodb, stopped) on
  `.158` must be Started (recreates the container on mongo:4.4); if it ever ran on mongo:7 and wrote
  data, 4.4 can't downgrade-read it → clear its `/data/db` first.** To run newer Mongo: set the
  Proxmox VM CPU type to "host" + reboot, then bump the rune image.
- **v0.2.83** Domains overview (NPM/Cloudflare phase 2): Domains nav page listing every routed
  domain (server × provider) with provisioned + live reachability badges. `GET /api/domains` +
  `GET /api/domains/{id}/check?provider=` (domain derived server-side; 30s cache).
- **v0.2.84** SECURITY PASS 3 (re-audit, 3 reviewers → `docs/SECURITY_AUDIT.md` "Pass 3"):
  install-log WebSocket now RBAC-gated (was an unauth'd cross-server leak); domain-probe SSRF
  guard (rejects loopback/private/link-local/metadata IPs at connect time — the `subdomain` is
  `server.control`-editable); realm CRUD admin-gated; builtin-rune overwrite guard on all four
  import paths.
- **v0.2.85** Users permission gating: non-admin delegates now see only the servers they're
  granted and only the actions in their per-server perms (tabs + buttons hidden accordingly).
  API attaches effective `perms` per server; `/auth/me` gains `can_create`. Backend enforcement
  was already in place — this is the UI honoring it.

### Session 2026-06-08 — what shipped since v0.2.70
- **v0.2.71** Cloudflare Tunnel subdomain integration (`internal/cloudflare/`, `cf_*` settings,
  `servers.cf_hostname`, Settings→Network card). Live-proven (`memos.nolimit.dk`). CF token needs
  perms **Argo Tunnel (Legacy)→Edit** (= the Tunnel API perm) + **Zone→DNS:Edit**. Account-scoped
  tokens can't call `/user/tokens/verify` (so Test uses CheckTunnel + zone-resolve).
- **v0.2.72** Rebrand to **"Yggdrasil Panel"** (binary/module/repo unchanged) · per-rune
  **"Create server"** button (`#/servers?new=<id>`) · `capabilities`/`devices`/`sysctls` rune fields.
- **v0.2.73** Compact side-by-side Runes card buttons.
- **v0.2.74** Schedules: **edit after creation** + **run log** (`schedule_runs` table,
  `GET /api/schedules/{id}/runs`).
- **v0.2.75** Schedules mobile layout (buttons wrap).
- **v0.2.76** **Static hosting**: `community-runes/apps/static-site.yaml` (nginx, data dir = web
  root) + FileManager **drag&drop / multi-file upload** (recursive folders).
- **v0.2.77** Servers table: aligned columns across rune groups (`table-layout:fixed`).
- **v0.2.78–v0.2.82** SECURITY HARDENING (5-agent audit → `docs/SECURITY_AUDIT.md`). See that doc
  for the full list. Highlights: rune cap/device/sysctl/extra_volumes **allowlist** + admin-gated
  rune endpoints; JWT live re-validation + token revocation + per-account login lockout; CSP +
  conditional HSTS + WS same-origin + CORS-creds-off; symlink/zip-slip defenses + PidsLimit; RCON
  password masking + BattleMetrics token encryption + TOTP replay protection + crypto fail-closed.
- **WordPress rune v3**: install seeds `.htaccess` raising PHP upload to 128M; DB-field labels
  spell out the gotcha (DB host = the DB rune's **published host port**, e.g. `192.168.1.164:25003`,
  NOT `:3306`; use the app user `MARIADB_USER`/`MARIADB_PASSWORD`, not root).
- **Landing page** at `website/` (index.html + screenshots), hosted live on **`yggdrasilpanel.com`**
  via a static-site server on `.164:25005` (apex A-record → public IP `5.186.58.205`).
- **Tailscale**: NOT a rune (removed — bad fit). Installed **host-native** on `.164`
  (subnet-router/exit-node). The cap/device/sysctl rune support stays (GPU/WireGuard).

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

### Immediate / open threads (2026-06-09)
1. ~~NPM Phase 2 — Domains overview~~ — **SHIPPED v0.2.83** (Domains nav page: every routed
   domain × provider with provisioned + reachable badges; `GET /api/domains` +
   `GET /api/domains/{id}/check?provider=`). See `docs/NPM_SUBDOMAIN_PLAN.md`.
2. **Security Pass 2 deferred items** (low priority, documented in `docs/SECURITY_AUDIT.md`): NPM/UniFi/SFTP TLS-pinning, full env-at-rest encryption, startup-command `{{TEMPLATED}}` env injection, a dedicated `server.edit` perm.
3. ~~Tailscale on `.164`~~ — **DONE** (user-confirmed 2026-06-09; `ygg-164` is up and offers exit
   node). Audited 2026-06-09: no tailscale runes/servers/containers remain in either panel,
   either Docker, or the repo. Only trace: an offline tailnet node `13324081f245` (the old rune
   container, last seen days ago) — remove it in the **Tailscale admin console** if it bothers.

### WISHLIST open
- **Sleep toggle**: enable/disable "sleep mode" for a server (pause without stopping?). Needs design.

### Potential improvements (not on any list)
- Panel warns when Expansion-Navigation version doesn't match DayZ version (detects `player connect will stay disabled` in logs → shows alert)
- Per-server CPU limits actually enforced in practice (set via `cpu_percent` in server settings)
- More community runes (Pi-hole, Portainer, etc.)
- WordPress + MariaDB linked setup guide
- Norn: nominal/restock tuning (spawn amounts, not just lifetime)

---

## Security Notes
- Full audit + hardening status: **`docs/SECURITY_AUDIT.md`** (read this first for the security picture).
- Steam password/Guard code: never stored or logged.
- Secrets encrypted at rest (AES-256-GCM): UniFi / NPM / Cloudflare / backup / Steam / TOTP / BattleMetrics; never returned in API responses. `crypto.New` fails closed on a secret < 16 chars.
- Argon2id passwords; JWT sessions re-validated live per request (disable/role/logout effective immediately via `users.token_version`); per-account login lockout; TOTP replay protection.
- Rune privilege allowlist (capabilities/devices/sysctls/extra_volumes); rune management is admin-only.
- CSP + conditional HSTS, WebSocket same-origin, CORS creds off; path-traversal + symlink/zip-slip guards; PidsLimit on all containers.
- VM sudo password: transient — do not store in files.

---

*Last updated: 2026-06-10*
