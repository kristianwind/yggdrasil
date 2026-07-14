# Configuration

Yggdrasil Panel reads one YAML file at startup, `/etc/yggdrasil/config.yaml`. The installer
generates it on first install and never touches it again on re-runs. Everything else — servers,
users, integrations, backup targets — lives in the database and is configured in the web UI.

Restart the service after editing the file:

```bash
sudo systemctl restart yggdrasil
```

## Full file

This is what the installer writes, with the generated values replaced:

```yaml
server:
  host: "0.0.0.0"
  port: 8080
database:
  path: "/var/lib/yggdrasil/yggdrasil.db"
auth:
  secret_key: "<44-char random base64>"
  session_ttl_hours: 168
docker:
  socket: "unix:///var/run/docker.sock"
ports:
  range_min: 25000
  range_max: 30000
admin:
  username: "admin"
  password: "<generated on first install>"
```

Every key is optional — a missing file or a missing key falls back to the defaults below. The file
is written `chmod 640`, owned `root:yggdrasil`, because it holds `auth.secret_key`.

## Keys

| Key | Default | Meaning |
| --- | --- | --- |
| `server.host` | `0.0.0.0` | Listen address. Set to `127.0.0.1` if a reverse proxy on the same host is the only thing that should reach the panel. |
| `server.port` | `8080` | Listen port. Must be 1–65535. |
| `database.path` | `/var/lib/yggdrasil/yggdrasil.db` | SQLite file. Created on first boot. |
| `auth.secret_key` | *(none)* | Signs JWTs and derives the encryption key for secrets at rest. See below. |
| `auth.session_ttl_hours` | `168` | Login session lifetime (7 days). Also sets the `ygg_token` cookie's max age. |
| `docker.socket` | `unix:///var/run/docker.sock` | Docker endpoint. The panel talks to Docker over this; it does **not** mount it into any container. |
| `ports.range_min` | `25000` | Low end of the automatic port-allocation range. |
| `ports.range_max` | `30000` | High end. Must be greater than `range_min`. |
| `admin.username` | *(none)* | Bootstrap admin. See below. |
| `admin.password` | *(none)* | Bootstrap admin password. See below. |

## `auth.secret_key`

This one key protects everything. It signs session tokens and derives the AES-GCM key that encrypts
stored secrets — UniFi and Nginx Proxy Manager credentials, Cloudflare and BattleMetrics tokens, the
Discord webhook, and backup-target credentials.

It must be **at least 16 characters**; the panel fails closed and refuses to encrypt with a shorter
one rather than silently weakening it. The installer generates 44 characters of base64 from
`/dev/urandom`.

Changing it invalidates every existing session and makes every already-encrypted secret
undecryptable — you would have to re-enter each integration's credentials. Back it up with the
database; a restored database is useless without the matching key.

## `admin.username` / `admin.password`

These bootstrap the first admin account, and only that. On boot, if a user with the `admin` role
already exists, the block is ignored entirely — it will not reset a password or create a second
admin.

If `admin.username` is set with no password, the panel generates one and prints it to stdout once,
as a plain banner rather than through the logger (so it doesn't linger in `journalctl`):

```bash
sudo journalctl -u yggdrasil --since "10 minutes ago" | head -20
```

Because the account is created once and never re-checked, the right move after first login is to
change the password in the UI and then delete the whole `admin:` block from the config, so a
plaintext password isn't sitting on disk. Recovering a lost admin password means deleting the admin
user from the database and letting the block re-bootstrap it.

## `ports`

Yggdrasil allocates a free host port per server port by walking this range from `range_min` upward,
and tracks it so two servers never collide. A port counts as free only if it isn't already
allocated, isn't claimed earlier in the same request, and can actually be bound right now — which
also catches ports held by processes Yggdrasil doesn't know about.

The game's well-known port is **deliberately ignored**. A Minecraft server does not land on 25565
and DayZ does not land on 2302; both get a port from this range. Off-default ports attract
noticeably less scanning, and every server still gets a unique one. The rune's declared port is the
*container* side of the mapping only.

The exception is Steam games, which publish 1:1 (host port == container port) because the game binds,
publishes, and advertises the same number to the Steam master.

Steam games publish 1:1 (host port == container port) so the game binds, publishes, and advertises
the same number to the Steam master. See the [rune schema](rune-schema.md) for how ports are
declared and injected.

Whatever range you choose has to be reachable from the internet for players to connect — see
[Networking](../guides/networking.md).

## Environment variables

The config file has no environment-variable overrides. The installer reads a few for its own
behavior, not the panel's:

| Variable | Used by | Meaning |
| --- | --- | --- |
| `YGG_REPO` | `install.sh` | Source repo. Default `kristianwind/yggdrasil`. |
| `YGG_VERSION` | `install.sh` | Release tag to install. Default `latest`. |
| `YGG_BINARY_URL` | `install.sh` | Install a prebuilt binary from a URL instead of a GitHub release. |
| `YGG_BINARY_FILE` | `install.sh` | Install a prebuilt binary from a local path. |

The last two exist for testing a build before it's released.

## Paths

| Path | Contents |
| --- | --- |
| `/usr/local/bin/yggdrasil` | The binary. UI and builtin runes are embedded in it. |
| `/etc/yggdrasil/config.yaml` | This file. |
| `/var/lib/yggdrasil/yggdrasil.db` | SQLite database — everything not in the config file. |
| `/var/lib/yggdrasil/servers/<uuid>/` | One server's data directory, bind-mounted into its container. |
| `/var/lib/yggdrasil/steam-cache/` | Shared SteamCMD cache across Steam-game servers. |
| `/etc/systemd/system/yggdrasil.service` | The unit. Runs as the `yggdrasil` system user. |

Back up `config.yaml` and `yggdrasil.db` together. The server data directories are large and are
what the panel's own [backup system](../guides/backups-and-schedules.md) is for.

## See also

- [Getting started](../getting-started.md) — installing for the first time
- [Networking](../guides/networking.md) — making servers reachable
- [API reference](api.md) — the HTTP API
