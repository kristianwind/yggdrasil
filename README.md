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

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/kristianwind/yggdrasil/main/install.sh | sudo bash
```

The installer detects your distro, installs Docker if missing, creates a
dedicated service user, drops a systemd unit, and prints your login URL and a
generated admin password. Re-running it upgrades or repairs an existing install.

Updating later is just: swap the binary + restart the service (or re-run the
one-liner — it's idempotent).

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

## License

To be decided before public release (intended: open source).
