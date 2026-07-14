# Yggdrasil Panel documentation

A self-hosted control panel for game and app servers. One static binary, SQLite, one Docker
container per server.

**New here? Start with [Getting started](getting-started.md)** — install to a running game server in
about fifteen minutes.

## Guides

For people running a panel.

| Guide | Covers |
| --- | --- |
| [Servers](guides/servers.md) | Creating servers, the lifecycle, console, files, cloning, tags, bulk actions |
| [Runes](guides/runes.md) | The catalog: builtin runes, the community library, importing, Pterodactyl eggs |
| [Users and permissions](guides/users-and-permissions.md) | Realms, the eight permission bits, delegates, 2FA, passkeys, API tokens |
| [Backups and schedules](guides/backups-and-schedules.md) | Targets, restore, verification, retention, cron actions |
| [Networking](guides/networking.md) | Ports, reachability, UPnP, UniFi, Nginx Proxy Manager, Cloudflare Tunnel |
| [Monitoring and alerts](guides/monitoring-and-alerts.md) | Metrics, resource alarms, the watchdog, auto-restart, quiet hours |
| [Notifications](guides/notifications.md) | Telegram, Discord, webhooks, email — and what actually triggers them |
| [Status page and beacon](guides/status-page-and-beacon.md) | The public `/status` board, the Discord status embed, the opt-in beacon |
| [Kvasir (AI)](guides/kvasir-ai.md) | The optional AI assistant, its providers, and its safety model |
| [DayZ loot economy (Norn)](guides/dayz-norn.md) | Lifetimes, globals, mod loot, and surviving a reinstall |

## Reference

For people integrating with a panel or writing runes.

| Reference | Covers |
| --- | --- |
| [Configuration](reference/configuration.md) | Every key in `/etc/yggdrasil/config.yaml` |
| [API](reference/api.md) | The full HTTP API, authentication, and permission gates |
| [Rune schema](reference/rune-schema.md) | The YAML format for teaching Yggdrasil a new game or app |

## Design notes

Working documents, kept for context. They record intent at a point in time and are not maintained as
user documentation.

- [Security audit](SECURITY_AUDIT.md) — audit findings, what was fixed, what was deliberately deferred
- [AI roadmap](AI_ROADMAP.md)
- [NPM subdomain plan](NPM_SUBDOMAIN_PLAN.md)
- [Cloudflare Tunnel plan](CLOUDFLARE_TUNNEL_PLAN.md)

## A note on vocabulary

The UI says **rune**; the code and API say `gameskill`. They're the same thing — a declarative YAML
file describing one game or app. The docs use "rune" in prose and `gameskill` only where you'll
actually meet the word, such as `/api/gameskills`.

A **realm** is a group of servers. **Kvasir** is the AI assistant. **Norn** is the DayZ
loot-economy tool.
