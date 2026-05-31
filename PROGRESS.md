# Yggdrasil — Progress

Last updated: 2026-05-31

## Current state (read me first)

**Phase 0 is complete and verified** — the binary builds (`CGO_ENABLED=0`),
`go vet` and `go test` pass, and a live smoke test confirms: boot → SQLite
schema → builtin gameskill load → JWT login → authed API call → 401 on bad/missing
auth → graceful Docker-absent degradation.

Because the backend layers are cheap to write together, **substantial backend for
Phases 1, 2, 5 and 6 already exists** (auth, server lifecycle, log/console
WebSockets, stats, file manager, realms, users, audit log). The **big remaining
gap is the entire frontend** — only a placeholder `web/dist/index.html` is
embedded. The next milestone is the Phase 1 Svelte web shell, which turns this
into an actually usable panel.

Run locally: `go run ./cmd/yggdrasil --config dev-config.yaml` (see README).

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

### Phase 1 — Auth + web shell + login 🟡 BACKEND DONE, FRONTEND PENDING
- [x] internal/auth: argon2id, session JWT
- [x] API: POST /api/auth/login, POST /api/auth/logout, GET /api/auth/me
- [x] First-run admin creation (from config; generated password if none)
- [x] Rate limiting middleware (5/min/IP on login)
- [x] Secure headers + HttpOnly/SameSite=Strict cookie
- [ ] **Svelte frontend: login page, session store, app shell** ← NEXT
- [ ] CSRF double-submit token on state-changing calls (cookie path)

### Phase 2 — Docker integration 🟡 BACKEND DONE, FRONTEND PENDING
- [x] internal/docker: Create/Start/Stop/Restart/Delete (Docker SDK v28)
- [x] WebSocket log streaming (/api/servers/:id/logs, stdcopy demux)
- [x] WebSocket console (/api/servers/:id/console) — stdin passthrough
- [x] Resource stats (CPU/RAM) via /api/servers/:id/stats
- [x] API: CRUD for servers (gameskill-driven, with port allocation)
- [x] Restart-on-crash (container RestartPolicy unless-stopped)
- [ ] Crash detection → status reconciliation loop (poll container state)
- [ ] Frontend: server list, console panel, resource mini-graph
- [ ] Disk usage stat (currently CPU/RAM only)

### Phase 3 — Gameskill parser + variable form + install flow 🟡 PARSER DONE
- [x] internal/gameskill: YAML parser, schema validation, clear errors
- [x] Variable types: string, int, bool, select (validated)
- [x] Docker image templating ({{VAR}})
- [x] API: upload/validate gameskill, list, get, delete (builtins protected)
- [x] minecraft-java.yaml (schema-complete; install script is a stub)
- [ ] Install flow: ephemeral container exists (docker.RunEphemeral); not yet
      wired to server creation with progress streaming
- [ ] Real Minecraft install logic (Paper/Purpur/Mojang/Fabric/Forge + latest resolve)
- [ ] Frontend: "Carve a Rune" upload UI, auto-generated server creation form

### Phase 4 — Remaining gameskills + query/RCON + resource graphs
- [ ] minecraft-bedrock.yaml
- [ ] rust.yaml (SteamCMD, A2S query, WebSocket RCON)
- [ ] dayz.yaml (SteamCMD, BattlEye RCON, A2S query, caveats documented)
- [ ] Steam authorization flow (one-time UI, persisted SteamCMD cache)
- [ ] Query protocols: A2S (Steam games), Minecraft query
- [ ] RCON: Minecraft, Rust WebSocket RCON, BattlEye
- [ ] Resource graphs (time-series in SQLite, frontend charts)
- [ ] startup done_regex detection

### Phase 5 — Realms + file management + config editor 🟡 BACKEND MOSTLY DONE
- [x] Realms: CRUD API
- [x] Default realm = gameskill category (auto-created on server create)
- [x] File browser API (list/read/write/upload/download/delete) with ../ guard
- [ ] Move servers between realms (endpoint)
- [ ] Config editor surfacing gameskill-declared config_files specifically
- [ ] Frontend: realm sidebar, file manager panel

### Phase 6 — Multi-admin RBAC + audit log 🟡 ROLES + AUDIT DONE, SCOPES PENDING
- [x] Basic roles: global admin vs user; admin-only routes enforced
- [x] User CRUD API (last-admin / self-delete guards)
- [x] Audit log: written on state-changing actions; admin-only read API
- [ ] **Scoped permissions**: permission bits (start/stop, console, config,
      backup, create, schedule) over scope types (realm/gameskill/server).
      `permissions` table exists but is not yet enforced.
- [ ] Frontend: user management, permission editor

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
