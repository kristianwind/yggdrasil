# 🌳 Yggdrasil

A self-hosted **game server management panel** for Debian/Ubuntu — think *AMP +
Pterodactyl, but radically easier to install, update and maintain*.

- **One binary.** A single static Go binary with the web UI embedded. No separate
  database, no Redis, no required reverse proxy.
- **One command to install.** The installer handles Docker and everything else.
- **Extensible.** A game = one declarative **gameskill** file (shown as a *Rune*
  in the UI). Ships with Minecraft Java, Minecraft Bedrock, Rust and DayZ.
- **Installable PWA.** Mobile-friendly, dark mode, works as an app on iOS/Android.

> ⚠️ Early development. See [PROGRESS.md](PROGRESS.md) for the phase-by-phase status.
>
> 🤖 **Built with [Claude Code](https://claude.com/claude-code).** Provided **as‑is**,
> with **no warranty and no liability whatsoever** — you use it entirely at your own
> risk. See the [Disclaimer](#disclaimer) below.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/kristianwind/yggdrasil/main/install.sh | sudo bash
```

The installer detects your distro, installs Docker if missing, creates a
dedicated service user, drops a systemd unit, and prints your login URL and a
generated admin password. Re-running it upgrades or repairs an existing install.

Updating later is just: swap the binary + restart the service (or re-run the
one-liner — it's idempotent).

## Key features

**Servers & runtime**
- Create / start / stop / restart / delete servers, each in its own Docker
  container with **per-server CPU & RAM limits** and clean isolation
- **Real-time console** (WebSocket) with command input, plus live **log streaming**
- One-time **install flow** that runs the gameskill's setup in an ephemeral
  container with live progress (e.g. downloads the right Paper/Vanilla/Fabric jar)
- **Restart-on-crash** + crash detection; conflict-free **port allocation**
- **Resource monitoring** (CPU / RAM) and **player count / status** via query
  protocols (A2S for Steam games, Minecraft Java SLP, Bedrock ping)
- **RCON** where the game has it — Source/Minecraft (TCP), Rust (WebSocket),
  DayZ (BattlEye)

**Games (gameskills / "Runes")**
- Ships with **Minecraft Java, Minecraft Bedrock, Rust, DayZ**
- Author your own in declarative YAML; auto-generated settings form
  (string/int/bool/**dropdown**) per game
- **Import** existing definitions: Pterodactyl **eggs** (JSON) and **XML**
- **Steam authorization**: anonymous by default; a one-time login flow for games
  that require an account (DayZ), with the SteamCMD cache persisted so Steam Guard
  isn't re-triggered

**Organize & control access**
- **Realms** to group servers; default grouping by game type, custom realms supported
- **Multiple admins with scoped permissions** — grant `view / control / console /
  files / create / delete / backup / schedule` at **global / realm / game-type /
  single-server** scope
- Built-in **file manager** + config editor (browse, edit, upload, download)
- Full **audit log** of admin actions; **API tokens** to drive everything from
  automation (a documented REST API)
- Optional **2FA (TOTP)** on login

**Automation & operations**
- **Backups** to local / mounted NFS-CIFS, **SFTP**, or **SMB** — on-demand or
  scheduled, with **restore** and **retention** (keep N / X days)
- **Scheduler** (cron) for backups, restarts, updates, start/stop, console
  commands, and **in-game messages** with editable templates (`{{minutes}}`,
  `{{server_name}}`) and a "skip if players online" guard
- **Notifications** via Telegram, Discord, generic webhook, or email (SMTP) —
  server up/down, backup done/failed, **low disk**
- **Disk dashboard** with a low-space alert

**Anti-cheat & moderation**
- Per-game **anti-cheat surface** (Paper anti-xray hints, BattlEye/EAC status,
  recommended plugins)
- **Centralized cross-server ban list** — ban a player on one server or everywhere
  at once, pushed via RCON/console
- **Violation auto-actions** — watch logs for a pattern and auto-kick/ban when it
  recurs past a threshold

**Security**
- Passwords hashed with **argon2id**; RCON / backup / Steam credentials
  **encrypted at rest** (AES-256-GCM) and never logged
- Login **rate-limiting**, secure headers, CSRF-safe token auth, path-traversal
  guards on file access

## Screenshots

| Dashboard | Server console & live stats |
|---|---|
| [![Dashboard](docs/screenshots/dashboard.png)](docs/screenshots/dashboard.png) | [![Server detail](docs/screenshots/server-console.png)](docs/screenshots/server-console.png) |
| **Servers** | **Anti-cheat** |
| [![Servers](docs/screenshots/servers.png)](docs/screenshots/servers.png) | [![Anti-cheat](docs/screenshots/server-anticheat.png)](docs/screenshots/server-anticheat.png) |
| **Schedules** | **Bans & auto-actions** |
| [![Schedules](docs/screenshots/schedules.png)](docs/screenshots/schedules.png) | [![Bans](docs/screenshots/bans.png)](docs/screenshots/bans.png) |
| **Runes (gameskills)** | **Settings** |
| [![Runes](docs/screenshots/runes.png)](docs/screenshots/runes.png) | [![Settings](docs/screenshots/settings.png)](docs/screenshots/settings.png) |

<sub>Dark mode by default; the UI is fully responsive and installable as a PWA.</sub>

## Concepts

| Term | Meaning |
|------|---------|
| **Yggdrasil** | the whole panel |
| **Gameskill** (UI: *Rune*) | a declarative game definition (YAML). You "carve new runes" to add games. |
| **Realm** | a group/category of servers. Default grouping is by game type; custom realms supported. |

Each game server runs as its own Docker container — giving per-server CPU/RAM
limits, clean isolation, and portable per-game runtimes (JRE, SteamCMD) without
polluting the host.

## Development

Requirements: Go 1.23+, and (optionally) Node 20+ for the frontend.

```bash
# Run the backend against a local config (Docker optional; it degrades gracefully)
go run ./cmd/yggdrasil --config ./dev-config.yaml

# Tests
go test ./...

# Build a static binary
CGO_ENABLED=0 go build -o yggdrasil ./cmd/yggdrasil
```

A minimal `dev-config.yaml`:

```yaml
server: { host: "127.0.0.1", port: 8080 }
database: { path: "./ygg.db" }
admin: { username: "admin", password: "changeme" }
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the design and
[docs/GAMESKILL_SCHEMA.md](docs/GAMESKILL_SCHEMA.md) for the gameskill format.

## Disclaimer

**Yggdrasil was built with [Claude Code](https://claude.com/claude-code), Anthropic's
agentic coding tool.** Much of the code, configuration, and documentation in this
repository was generated by an AI assistant.

**This software is provided "AS IS", without warranty of any kind**, express or
implied, including but not limited to the warranties of merchantability, fitness
for a particular purpose, and non‑infringement.

**There is absolutely no liability.** In no event shall the authors, contributors,
or anyone associated with this project be held liable for any claim, damages, data
loss, downtime, security incident, or other liability — whether in an action of
contract, tort, or otherwise — arising from, out of, or in connection with the
software or its use.

**You use Yggdrasil entirely at your own risk.** It manages game servers, runs
containers, executes install scripts, opens network ports, and stores credentials;
operate it only on systems and data you are willing to lose, keep your own backups,
and review what it does before running it in production. By installing or using this
software you accept full responsibility for any consequences.

## License

To be decided before public release (intended: open source).
