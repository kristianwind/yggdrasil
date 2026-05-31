# Yggdrasil — Progress

Last updated: 2026-05-31

## Current state (read me first)

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
- [ ] Steam authorization flow (one-time UI, persisted SteamCMD sentry cache)
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

### Phase 6 — Multi-admin RBAC + audit log 🟡 ROLES + AUDIT DONE, SCOPES PENDING
- [x] Basic roles: global admin vs user; admin-only routes enforced
- [x] User CRUD API (last-admin / self-delete guards)
- [x] Audit log: written on state-changing actions; admin-only read API
- [x] Frontend: user management (create/disable/delete), audit log viewer
- [ ] **Scoped permissions**: permission bits (start/stop, console, config,
      backup, create, schedule) over scope types (realm/gameskill/server).
      `permissions` table exists but is not yet enforced.
- [ ] Frontend: permission editor (after scopes are enforced)

### Phase 7 — Backup + restore + schedules
- [ ] Backup targets: NFS mount, CIFS/SMB mount, SFTP
- [ ] Encrypted credential storage (AES-256-GCM)
- [ ] On-demand + scheduled backups
- [ ] Restore flow
- [ ] Retention policy (keep N / keep X days)
- [ ] Backup status UI
- [ ] Scheduler: cron parser, task runner
- [ ] Schedule types: backup, update, restart, in-game message, command
- [ ] Message templates with variables
- [ ] Player-online check before disruptive operations

### Phase 8 — Anti-cheat + ban management
- [ ] Paper anti-xray config surface (Minecraft Java)
- [ ] BattlEye/EAC/VAC config surface (DayZ/Rust)
- [ ] Centralized ban list (cross-server)
- [ ] Ban via RCON / console
- [ ] Kick/ban events surfaced in UI

### Phase 9 — Notifications + API tokens + importer
- [ ] Telegram notifications
- [ ] Discord webhook notifications
- [ ] Email notifications (SMTP)
- [ ] API tokens (per-user, scoped)
- [ ] REST API documentation (docs/API.md)
- [ ] Pterodactyl egg importer
- [ ] XML gameskill import

### Phase 10 — PWA polish + docs + end-to-end test
- [ ] PWA manifest, service worker, installability
- [ ] Dark mode default, responsive/touch-friendly
- [ ] install.sh full implementation + idempotency test
- [ ] README.md
- [ ] docs/GAMESKILL_SCHEMA.md
- [ ] End-to-end oneliner install test on clean Debian VM

### Optional / Later
- [ ] Import running servers from Pterodactyl / AMP
- [ ] Violation-driven auto-ban rules
- [ ] Multi-node support
- [ ] 2FA on login
- [ ] Disk dashboard with alerts

## Notes & Decisions

- **Svelte** chosen over React: smaller bundle (~50 KB gzipped), no virtual DOM at runtime.
- **Docker per server**: CPU/RAM cgroup limits, clean isolation, portable game runtimes.
- **cgo-free SQLite** (`modernc.org/sqlite`): enables true static cross-compilation in CI.
- **Port range**: default 25000–30000, tracked in SQLite to avoid conflicts.
- **DayZ Linux caveat**: DayZ dedicated server on Linux has historically been experimental / Wine-required. Research current (2025+) status at implementation time; document in gameskill if still restricted.
- **Gameskill install scripts** run in ephemeral Docker containers with only the server volume mounted — no host filesystem access.
