# Yggdrasil — Progress

Last updated: 2026-05-31

## Current state (read me first)

**END-TO-END VERIFIED AGAINST REAL DOCKER (2026-05-31):** installed + ran an
actual Minecraft Paper server — install flow pulled the JRE image and downloaded
Paper (54 MB), the container started ("Done (28s)!"), the live Minecraft **query**
returned player count/version, and **RCON** executed `list` successfully. This
closes the long-standing "coded but not Docker-verified" caveat for Phases 3/4.
The test surfaced one real bug (RCON wasn't enabled in server.properties) — fixed
in the Minecraft gameskill install script.


**Phase 0 is complete and verified** — the binary builds (`CGO_ENABLED=0`),
`go vet` and `go test` pass, and a live smoke test confirms: boot → SQLite
schema → builtin gameskill load → JWT login → authed API call → 401 on bad/missing
auth → graceful Docker-absent degradation.

**Phase 1 (Svelte web shell) is complete and visually verified.** The frontend
is a Svelte 5 + Vite + Tailwind SPA (72 KB JS / 26 KB gzipped) embedded in the
binary. Verified in a real browser: login → JWT session → dashboard with live
stats → sidebar nav → Runes list. Backend for Phases 2/5/6 also has matching UI
(server list+create form, console/logs, file manager, users, audit log).

The next milestone is **Phase 3/4: the real gameskill install flow** (wiring
`docker.RunEphemeral` into server creation with progress streaming) and the
remaining three gameskills (Bedrock, Rust, DayZ) with query/RCON.

Run backend: `go run ./cmd/yggdrasil --config dev-config.yaml`.
Dev frontend (proxy to :8080): `cd web && npm run dev`.
Build embedded binary: `cd web && npm run build && cd .. && go build ./cmd/yggdrasil`.

## Phase Checklist

### Phase 0 — Repo skeleton ✅ DONE
- [x] ARCHITECTURE.md + PROGRESS.md
- [x] go.mod + go.sum (Docker SDK v28, modernc sqlite, chi, jwt, gorilla/ws)
- [x] cmd/yggdrasil/main.go (config, DB, admin, gameskill load, graceful shutdown, subcommands)
- [x] internal/config package (+ validation, gen-config)
- [x] internal/db package (full schema + migrations)
- [x] deploy/yggdrasil.service (hardened systemd unit)
- [x] deploy/config.yaml.example
- [x] install.sh (distro check, Docker install, user, binary, systemd — idempotent)
- [x] .github/workflows/release.yml (static amd64+arm64, checksums, test job)
- [x] .gitignore + README.md
- [x] Tests: gameskill parser, auth (hash + JWT)
- [x] Verified: builds, vets, tests, live boot smoke test
- [ ] Initial git commit + push (pending user confirmation for push)

### Phase 1 — Auth + web shell + login ✅ DONE
- [x] internal/auth: argon2id, session JWT
- [x] API: POST /api/auth/login, POST /api/auth/logout, GET /api/auth/me
- [x] First-run admin creation (from config; generated password if none)
- [x] Rate limiting middleware (5/min/IP on login)
- [x] Secure headers + HttpOnly/SameSite=Strict cookie + token query-param for WS
- [x] Svelte 5 + Vite + Tailwind SPA: login, session store, hash router, app shell
- [x] Responsive sidebar (mobile drawer), dark theme, toast notifications
- [x] PWA: manifest, SVG icon, service worker (app-shell cache, /api never cached)
- [x] SPA fallback in Go static handler (deep links work)
- [x] Verified in real browser (login → dashboard → nav → runes)
- [ ] CSRF double-submit token (cookie path; Bearer path is CSRF-immune)

### Phase 2 — Docker integration 🟡 BACKEND DONE, FRONTEND PENDING
- [x] internal/docker: Create/Start/Stop/Restart/Delete (Docker SDK v28)
- [x] WebSocket log streaming (/api/servers/:id/logs, stdcopy demux)
- [x] WebSocket console (/api/servers/:id/console) — stdin passthrough
- [x] Resource stats (CPU/RAM) via /api/servers/:id/stats
- [x] API: CRUD for servers (gameskill-driven, with port allocation)
- [x] Restart-on-crash (container RestartPolicy unless-stopped)
- [x] Frontend: server list (grouped by realm), create form, console + live stats
- [ ] Crash detection → status reconciliation loop (poll container state)
- [ ] Disk usage stat (currently CPU/RAM only)
- [ ] Resource mini-graph (currently numeric stats only)

### Phase 3 — Gameskill parser + variable form + install flow ✅ DONE (Docker-untested)
- [x] internal/gameskill: YAML parser, schema validation, clear errors
- [x] Variable types: string, int, bool, select (validated)
- [x] Docker image templating ({{VAR}})
- [x] API: upload/validate gameskill, list, get, delete (builtins protected)
- [x] minecraft-java.yaml with REAL install logic (Paper/Purpur/Mojang/Fabric
      APIs + "latest" resolution; Forge flagged as manual)
- [x] Install flow wired: create → background install (docker.RunEphemeral) with
      live progress over a WebSocket hub; install_status tracked in DB
- [x] Start gated on install completion (409 until installed)
- [x] Startup command from gameskill passed as the container command (was missing)
- [x] Per-server CPU/RAM caps applied at container create
- [x] Frontend: auto-generated creation form (select/int/bool/string), "Carve a
      Rune" upload UI, install-log streaming panel, install/reinstall buttons
- [x] Verified wiring end-to-end (create→install→error-without-docker→start 409)
- NOTE: actual container download/run needs a Docker daemon to verify; the path
  is coded + the control flow proven, but not run against real images here.

### Phase 4 — Remaining gameskills + query/RCON 🟡 GAMESKILLS + PROTOCOLS DONE
- [x] minecraft-bedrock.yaml (Mojang links API, no RCON, x86_64 caveat)
- [x] rust.yaml (SteamCMD 258550 anon, A2S query, WebSocket RCON)
- [x] dayz.yaml (SteamCMD 223350 owned-account, BattlEye RCON, A2S, caveats)
- [x] App IDs verified via web research (Rust 258550, DayZ 223350/221100)
- [x] Query protocols: A2S, Minecraft Java SLP, Bedrock ping (internal/query, tested)
- [x] RCON: Source/Minecraft, Rust WebSocket, BattlEye (internal/rcon, tested)
- [x] API: GET /servers/:id/query, POST /servers/:id/rcon
- [x] Frontend: live player-count on server detail
- [x] Steam authorization flow: one-time UI (username/password/Guard), interactive
      SteamCMD login in a container, persisted sentry cache (/steamcache) reused by
      all installs; password/code never stored or logged; non-anonymous installs
      gated on an authorized account + inject STEAM_USER + mount the cache.
      (Login itself not e2e-tested — needs real DayZ-owning Steam credentials.)
- [ ] Resource graphs (time-series in SQLite, frontend charts)
- [ ] startup done_regex → running-state detection at runtime
- [ ] Wire real install flow (docker.RunEphemeral) into server creation with
      progress streaming + real Minecraft/Steam download logic
      (NOTE: full install/run path needs a Docker daemon to verify; the dev
      machine here has none, so these are coded but not yet end-to-end tested)

### Phase 5 — Realms + file management + config editor 🟡 BACKEND MOSTLY DONE
- [x] Realms: CRUD API
- [x] Default realm = gameskill category (auto-created on server create)
- [x] File browser API (list/read/write/upload/download/delete) with ../ guard
- [x] Frontend: file manager panel (browse/edit/upload, realm-grouped servers)
- [ ] Move servers between realms (endpoint + UI)
- [ ] Config editor surfacing gameskill-declared config_files specifically

### Phase 6 — Multi-admin RBAC + audit log ✅ DONE
- [x] Basic roles: global admin vs user; admin-only routes enforced
- [x] User CRUD API (last-admin / self-delete guards)
- [x] Audit log: written on state-changing actions; admin-only read API
- [x] Frontend: user management (create/disable/delete), audit log viewer
- [x] internal/rbac: permission bits (view/control/console/files/create/delete/
      backup/schedule) × scopes (global/realm/gameskill/server), unit-tested
- [x] Enforcement across server handlers (list filtered to visible; get/stats/
      query=view; start/stop/restart/install=control; console/logs/rcon=console;
      files=files; create=create; delete=delete). Global admins bypass.
- [x] Permission management API: GET/PUT /users/:id/permissions + catalog
- [x] Frontend: scoped permission editor (grants per realm/gameskill/server)
- [x] Verified: rbac unit tests, API enforcement test (403/filter/persist),
      browser-verified editor + end-to-end scoped-user access checks

### Phase 7 — Backup + restore + schedules ✅ DONE
- [x] internal/crypto: AES-256-GCM, key from panel secret (unit-tested)
- [x] internal/backup: tar.gz archive honoring backup.include, restore (traversal-
      guarded), retention (keep-N / keep-days) — unit-tested
- [x] Backup targets: local (also covers mounted NFS/CIFS), SFTP (pkg/sftp),
      SMB/CIFS direct (go-smb2); credentials encrypted at rest
- [x] On-demand backups (async, status on backups row), list, delete, restore
      (stops container first); retention applied after each backup
- [x] Backup targets API (admin) + per-server backups API (RBAC server.backup)
- [x] Frontend: Settings→targets (type-conditional form, test/delete) + ServerDetail
      Backups tab (run/list/restore/delete)
- [x] Verified end-to-end without Docker (target create→test→backup done→file on
      disk; browser-verified UI)
- [x] Scheduler: cron task runner (robfig/cron, rebuilt on change), started on boot
- [x] Schedule types: backup, restart, start, stop, command, message, update
- [x] Scope: single server / realm / all servers; manual "run now" trigger
- [x] internal/scheduler: template render + action/cron validation (unit-tested)
- [x] Message templates: 5 defaults seeded, editable, {{minutes}}/{{server_name}}
- [x] Player-online check (skip_if_players) before restart/update via query
- [x] Command/message delivery via RCON with container-stdin fallback (Bedrock)
- [x] API: schedules CRUD + run; templates CRUD (RBAC: server.schedule / admin)
- [x] Frontend: Schedules page (action-conditional form) + templates editor
- [x] Verified: unit tests, API (seed/validate/CRUD/run/toggle), browser UI

### Phase 8 — Anti-cheat + ban management ✅ DONE
- [x] Per-rune anti-cheat surface in ServerDetail (anti-xray / BattlEye hints +
      recommended plugins, from the gameskill's anticheat block)
- [x] Paper anti-xray hint + link to the config file editor (Minecraft Java)
- [x] BattlEye config surface (DayZ); Rust EAC/VAC noted
- [x] gameskill `bans` block: ban/unban console commands ({{player}}/{{reason}})
      added to Minecraft Java, Rust, DayZ (Bedrock has none — allowlist-based)
- [x] Centralized cross-server ban list (admin): ban one server or all at once,
      reason + audit; pushes the ban command to running servers via RCON/console
- [x] Bans UI (list/ban/unban) + anti-cheat tab; API + browser verified
- [ ] (deferred to later) violation-driven auto-ban rules; live kick/ban event feed

### Phase 9 — Notifications + API tokens + importer ✅ MOSTLY DONE
- [x] internal/notify: Telegram, Discord webhook, generic webhook (httptest-tested)
- [x] Notification channels (admin): CRUD + test send; secrets encrypted at rest
- [x] Event hooks: backup done/failed, server start/stop → notifyAll
- [x] API tokens (per-user, inherit owner role): create (shown once)/list/delete,
      ygg_ prefix, SHA-256 stored; auth middleware accepts them (API-verified)
- [x] Pterodactyl egg importer (internal/gameskill/egg.go): maps image/startup/
      done_regex/install/config_files/variables (rules→type), unit-tested + UI
- [x] Frontend: egg import on Runes; tokens + notifications in Settings
- [x] Verified: notify unit tests, egg import test, API (token auth 200 / bad 401,
      egg import), bundle embeds all new UI
- [x] Email/SMTP notifications (net/smtp; host/port/user/pass/from/to + UI)
- [x] XML gameskill import (internal/gameskill/xml.go, unit-tested + UI button)
- [x] docs/API.md (done in Phase 10)

### Phase 10 — PWA polish + docs + end-to-end test 🟡 MOSTLY DONE
- [x] PWA manifest (SVG + 192/512 PNG icons), service worker (v2), installable
- [x] Manifest served as application/manifest+json; apple-touch-icon fixed (was 404)
- [x] Dark mode default, responsive sidebar/drawer, touch-friendly (done since P1)
- [x] install.sh full implementation (Docker install, user, binary, systemd, idempotent)
- [x] README.md (+ Claude Code / no-liability disclaimer)
- [x] docs/GAMESKILL_SCHEMA.md, docs/API.md
- [x] **End-to-end test against real Docker** (install + run a Paper server,
      query + RCON verified) — see top of file
- [ ] End-to-end oneliner install test on a clean Debian VM (needs a VM; install.sh
      is complete but not yet run on a fresh box from the published release)

### Optional / Later
- [x] Violation-driven auto-ban rules: per-server log-tailing watcher applies
      admin regex rules (group 1 = player); N hits in a window → auto-kick/ban
      (optionally cross-server) + notify. CRUD UI on the Bans page.
- [ ] Import running servers from Pterodactyl / AMP (out of scope — risks the
      gameskill model; skipped per the spec's own caution)
- [ ] Multi-node / wings-style (large; future)
- [x] 2FA on login (TOTP, RFC 6238, dependency-free): enroll (setup→confirm),
      required at login, disable with a code; secret encrypted at rest. Verified
      end-to-end against an independent TOTP implementation.
- [x] Disk dashboard with alerts: host disk free/total in system info + on the
      Dashboard; a background monitor notifies once when free drops below 10%
      (re-arms above 15%).

## Notes & Decisions

- **Svelte** chosen over React: smaller bundle (~50 KB gzipped), no virtual DOM at runtime.
- **Docker per server**: CPU/RAM cgroup limits, clean isolation, portable game runtimes.
- **cgo-free SQLite** (`modernc.org/sqlite`): enables true static cross-compilation in CI.
- **Port range**: default 25000–30000, tracked in SQLite to avoid conflicts.
- **DayZ Linux caveat**: DayZ dedicated server on Linux has historically been experimental / Wine-required. Research current (2025+) status at implementation time; document in gameskill if still restricted.
- **Gameskill install scripts** run in ephemeral Docker containers with only the server volume mounted — no host filesystem access.
