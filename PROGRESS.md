# Yggdrasil — Progress

Last updated: 2026-08-24 · current release: **v0.3.0**

## Where the project is

Yggdrasil is **built, in daily use, and past the point where "early development" described it**.
The panel has been running real game and app servers in production since June 2026 — Minecraft,
DayZ, Rust, WordPress sites, the *arr media stack, Immich, Vaultwarden, Gitea and more — on
amd64 boxes and on arm64.

What that means concretely:

- **Every phase of the original plan is shipped and verified against real Docker**, not just
  compiled. The install flow, query/RCON, backups, schedules, RBAC, bans and the PWA all work
  end-to-end on a clean Debian/Ubuntu box installed from the published one-liner.
- **~220 releases** since v0.1.0 (2026-05-31), each built, released and auto-deployed.
- **343 Go tests** across 103 test files; CI runs `go vet` + `go test` + a full frontend build on
  every PR, and the release workflow ships static amd64 + arm64 binaries with checksums.
- **The documentation is written from the code**: 22 pages under [`docs/`](docs/), plus a full
  [HTTP API reference](docs/reference/api.md) and [rune schema](docs/reference/rune-schema.md).

**The releases are the changelog.** Every version has an annotated tag and a GitHub release
describing what changed, so that history is not duplicated here — see
[Releases](https://github.com/kristianwind/yggdrasil/releases). This file covers the current
state and what is *not* done.

## Support and stability

- **The latest release is the supported one.** Fixes ship forward as a new version rather than
  being backported; updating is a binary swap and a service restart, and the panel can do it
  itself. See [SECURITY.md](SECURITY.md).
- **Database migrations run automatically on boot** and are forward-only. Upgrading in place is
  the normal path and is exercised on every deploy.
- **The rune schema is still allowed to grow.** New fields are added regularly; existing fields
  are treated as a contract, because community runes depend on them. Anything that would break
  an existing rune gets a migration or a fallback, not a silent change.
- Version numbers stay in the **0.x** range while that remains true. See *Toward 1.0* below.

## What's built

**Core** — one static Go binary with the Svelte PWA embedded, SQLite, no Redis, no external
database, no required reverse proxy. Runs on amd64 and arm64 (Raspberry Pi 4/5 included).

| Area | State |
| --- | --- |
| Server lifecycle | Install / start / stop / restart / delete per Docker container, CPU + RAM limits, `installing → starting → running → stopped` with rune-declared readiness, crash detection, auto-restart, watchdog auto-heal, graceful stop with pre-stop save |
| Runes | 5 built in, 60+ community runes in [`community-runes/`](community-runes/), one-click install from a GitHub folder, app stacks with sidecars, Pterodactyl egg / XML / Docker Compose import |
| Console & files | WebSocket console with command input, live log streaming, file manager, config editor with generated web forms |
| Live admin | Safe restart with in-game countdowns, wipe with backup-first, live players roster with kick / broadcast / lock, activity feed, per-server history that survives log rotation |
| Backups | Local / SFTP / SMB targets, on-demand or scheduled, restore, retention, verification |
| Scheduler | Cron for backups, restarts, updates, start/stop, console commands, in-game messages, wipes, with a run log and a skip-if-players-online guard |
| Networking | Automatic port forwarding (UPnP + UniFi), per-server firewall toggle, public reachability probe, connect addresses, per-server subdomains via Nginx Proxy Manager or Cloudflare Tunnel |
| Access | Scoped multi-admin RBAC, realms, delegation, 2FA (TOTP), passkeys, self-service email password reset, `yggdrasil reset-password` break-glass CLI, API tokens, full audit log |
| Monitoring | Host and per-server CPU / RAM / disk with history and sparklines, low-disk alerts, anomaly watchers, rune-declared app events, security-event tracking, public status page + Discord board |
| Notifications | Telegram, Discord, generic webhook, email (bring your own SMTP) |
| Kvasir (AI) | Optional, off until configured. Streaming chat grounded in panel data and docs, read-only lookups scoped by permission, admin-tunable data level with a local-vs-cloud warning, proactive crash/anomaly explanation, propose-then-confirm actions |
| Integrations | Claude connector over MCP, BattleMetrics status, Steam authorization for games that need an account |
| Operations | Host-to-host panel migration with secrets, app-data import (including WordPress `.wpress`), self-update, opt-out beacon |

## Not done

Honest gaps, so nobody discovers them the hard way:

- **Multi-node.** One panel manages one host. There is no wings-style agent and no scheduling
  across machines. This is the largest thing on the "someday" list, not a near-term plan.
- **Importing servers that already run outside Yggdrasil** (a live Pterodactyl or AMP instance).
  Rune *definitions* import; running servers do not. Adopting a foreign container would mean
  guessing at its lifecycle, and guessing wrong takes someone's world down.
- **No high availability.** A single binary on a single box, with SQLite. Back up the data
  directory; that is the recovery story.
- **Windows and macOS hosts.** Debian/Ubuntu with Docker only. Other distros mostly work but are
  not what the installer is tested against.
- **A community.** The code is mature; the project is not yet widely used. Expect to be early,
  and expect the maintainer to have hit your bug before you do only if it happened on his boxes.

## Backlog

Next up, roughly in order:

- Auto-ban rules driven by a live kick/ban event feed (the rules exist; the live feed does not).
- Broader rune coverage for games — the app catalogue has outgrown the game catalogue.
- More end-to-end coverage of the upgrade path in CI, not just the install path.

## Toward 1.0

1.0 is a promise about compatibility, and it should be made when it can be kept. The bar:

1. Real external installs, not just the maintainer's — enough that a breaking change would
   actually hurt someone.
2. Several consecutive releases with no rune-schema break and no manual migration step.
3. The upgrade path covered by CI on a clean box, not only by hand.

Until then 0.x is the honest number, and it does not mean unfinished.

## Notes & decisions

- **Svelte** over React: smaller bundle (~50 KB gzipped), no virtual DOM at runtime.
- **Docker per server**: CPU/RAM cgroup limits, clean isolation, portable game runtimes.
- **cgo-free SQLite** (`modernc.org/sqlite`): enables true static cross-compilation in CI.
- **Port range**: default 25000–30000, tracked in SQLite to avoid conflicts. Steam games publish
  1:1 (host port == container port) so they advertise the port players actually connect to.
- **Rune install scripts** run in ephemeral containers with only the server volume mounted — no
  host filesystem access.
- **Containers run as the panel's service account**, not the image's `USER`. A rune with a data
  path must default PUID/PGID to that account; defaulting to 1000 takes the files away from the
  panel. This is the single most common bug when writing a rune — see
  [the rune guide](docs/guides/runes.md).
- **`web/dist` is git-ignored** and built in CI; a local `go build` needs `npm run build` first.
- **The beacon is on by default and says so on first login**, sends only a random install id and
  a version, stores no IP, and stays off permanently once turned off. It exists so the project
  can count installs without tracking anyone.

## How it got here

The build followed a ten-phase plan — repo skeleton, auth and web shell, Docker integration, the
rune parser and install flow, realms and file management, RBAC and audit log, backups and
schedules, anti-cheat and bans, notifications and API tokens, PWA polish and docs. All ten are
complete; the phase-by-phase checklist that used to live here was retired once every box in it
was ticked and the release notes took over as the record.
