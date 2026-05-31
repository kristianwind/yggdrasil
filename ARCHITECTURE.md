# Yggdrasil — Architecture

## Overview

Yggdrasil is a self-hosted game server management panel for Debian/Ubuntu.
Single static Go binary, zero external runtime dependencies beyond Docker.

## Stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Backend | Go (static binary) | Single-file deploy, excellent concurrency, small binary |
| Database | SQLite via `modernc.org/sqlite` | Pure-Go (no cgo), WAL mode, embedded — no extra service |
| Frontend | Svelte 5 + Tailwind CSS | Smallest bundle of major frameworks (~50 KB gzipped), excellent PWA support |
| Build | Vite | Fast HMR in dev; outputs a static bundle embedded via `go:embed` |
| Container runtime | Docker Engine (via official Go SDK) | Per-server isolation, CPU/RAM limits, portable game runtimes |
| Service | systemd | Standard on Debian/Ubuntu, handles restart/logging |
| Config | `config.yaml` (single file) | Human-editable, loaded at startup |

## Repository Layout

```
yggdrasil/
├── cmd/yggdrasil/          # main package — flags, wiring
├── internal/
│   ├── config/             # config.yaml load + defaults
│   ├── db/                 # SQLite schema, migrations, queries
│   ├── auth/               # argon2id hashing, session/JWT, RBAC
│   ├── api/                # HTTP handlers (REST + WebSocket)
│   ├── docker/             # Docker SDK wrapper (create/start/stop/logs/stats)
│   ├── gameskill/          # YAML parser, variable validation, egg importer
│   ├── backup/             # NFS/CIFS/SFTP backup targets + restore
│   ├── scheduler/          # cron-like task runner
│   └── notify/             # Telegram / Discord / webhook / email
├── web/                    # Svelte SPA (embedded via go:embed)
│   ├── src/
│   └── dist/               # built output (gitignored; built in CI)
├── gameskills/             # bundled gameskill YAML files
│   ├── minecraft-java.yaml
│   ├── minecraft-bedrock.yaml
│   ├── rust.yaml
│   └── dayz.yaml
├── deploy/
│   ├── yggdrasil.service   # systemd unit
│   └── config.yaml.example
├── install.sh              # single-command installer
├── docs/
│   ├── API.md
│   └── GAMESKILL_SCHEMA.md
├── ARCHITECTURE.md         # this file
├── PROGRESS.md             # phase checklist
├── go.mod
└── .github/workflows/
    └── release.yml         # build + publish static binaries
```

## Data Model (SQLite)

```
users           — id, username, password_hash, role, created_at, disabled
sessions        — id, user_id, token_hash, expires_at
servers         — id, name, gameskill_id, realm_id, status, ports, env, created_at
realms          — id, name, description
gameskills      — id, yaml blob, parsed metadata, builtin bool
backups         — id, server_id, target_id, path, size, status, created_at
backup_targets  — id, name, type (nfs/cifs/sftp), config_encrypted
schedules       — id, server_id, cron, action, args, enabled
audit_log       — id, user_id, action, resource, detail, ts
bans            — id, player_id, server_scope, reason, banned_by, ts
permissions     — user_id, scope_type, scope_id, permission_bits
```

## Key Design Decisions

### Docker per server (vs native processes)
Docker provides CPU/RAM cgroups, network namespacing, and portable per-game runtimes (JRE versions, SteamCMD base images). Native processes would require manually managing Java installations, SteamCMD versions, and cgroup files — replicating a container runtime badly. Docker is the clear winner for "easy operations at scale."

### No separate database server
SQLite in WAL mode handles hundreds of concurrent reads and the write concurrency of a game server panel comfortably. Adding PostgreSQL/MySQL would be a deployment burden incompatible with the "single binary" goal.

### Svelte over React
Svelte compiles away the framework at build time; there is no virtual DOM shipped to the browser. A typical Svelte SPA is 40–80 KB gzipped vs 120–150 KB for a minimal React app. For an embedded PWA where binary size matters, Svelte wins.

### cgo-free SQLite
`modernc.org/sqlite` is a pure-Go port of SQLite. This allows true cross-compilation (GOOS=linux GOARCH=amd64 CGO_ENABLED=0) without a C toolchain or musl/glibc concerns.

### Gameskill (Rune) isolation
Install scripts run in ephemeral Docker containers with the server's data volume mounted. The host filesystem is never exposed to gameskill scripts. Resource limits apply from creation.

## Security Model

- Passwords: argon2id (memory=64MB, iterations=3, parallelism=4)
- Sessions: signed JWT (HS256) or secure random cookie in HttpOnly + SameSite=Strict cookie
- CSRF: double-submit cookie on all state-changing requests
- Login rate-limit: 5 attempts/minute per IP
- Gameskill uploads: parsed and validated; scripts only reach the Docker API, not the host shell
- Credentials (Steam, backup, RCON) encrypted at rest with AES-256-GCM, key derived from a machine secret in config

## Network / Port Model

Yggdrasil allocates host ports from a configurable range (default 25000–30000) and tracks them in SQLite to avoid conflicts. Each server's allocated ports are injected as env vars and passed to `docker run -p`.
