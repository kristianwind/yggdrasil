# Rune Schema

A **rune** is a YAML file that teaches Yggdrasil Panel how to install, run, and manage one game or
app. This is the field-by-field reference for writing one, plus the container patterns that real
runes use.

## The file

Everything lives under a single top-level `gameskill:` key — `gameskill` is the code and API name
for a rune, and this is one of the few places you meet the spelling.

```yaml
gameskill:
  id: minecraft-java
  name: "Minecraft (Java)"
  category: "Minecraft"
  docker:
    image: "eclipse-temurin:21-jre"
  startup:
    command: "java -jar server.jar nogui"
```

That is a valid rune. Four things are required: `id`, `name`, `docker.image`, and a startup command
(`startup.command` or `startup.exec` — unless `docker.keep_entrypoint` is set, in which case the
image's own `CMD` is the command). Everything else is optional and adds a panel feature.

| Key | Type | What it does |
|-----|------|--------------|
| `id` | string | **Required.** The rune's primary key. Re-uploading the same `id` replaces the rune. You cannot overwrite a built-in rune — pick a different `id`. |
| `name` | string | **Required.** Display name in the Runes list. |
| `category` | string | When you create a server without picking a realm, Yggdrasil puts it in a realm with this name, creating the realm if needed. Also shown in the Runes list. |
| `description` | string | Shown on the rune's card in **Runes → Browse GitHub**. |
| `version` | int | Shown as `v<n>` in the Runes list. Bump it when you change the file. |
| `docker` | map | **Required.** The image and how the container is built. |
| `variables` | list | The settings form, and the env vars/`{{KEY}}` values. |
| `install` | map | One-time setup script. |
| `startup` | map | **Required.** How the server runs, stops, and reports readiness. |
| `ports` | list | Host ports to allocate and publish. |
| `query` | map | Player count and liveness polling. |
| `rcon` | map | Remote console, for the console box, schedules, bans and the Players tab. |
| `steam` | map | Marks the rune as a SteamCMD game. |
| `bans` | map | Ban/unban console commands. |
| `players` | map | Live Players tab: list, kick, broadcast, lock. |
| `admin_log` | map | Parsed admin/activity feed. |
| `wipe` | map | What "reset the world" deletes. |
| `restart` | map | In-game countdown before a safe restart. |
| `backup` | map | What goes into a backup archive. |
| `anticheat` | map | Informational anti-cheat hints. |

## Templating and injected values

Yggdrasil substitutes `{{KEY}}` placeholders in `docker.image`, `docker.user`, `install.image`,
`install.script`, `startup.command`, and every element of `startup.exec`. This is plain string
replacement — there are no conditionals, loops, or filters, and a `{{KEY}}` with no matching value
is left in the text as-is.

The values come from the server's variables, plus two things the panel injects:

| Injected key | Value |
|--------------|-------|
| `SERVER_NAME` | The server's name as typed in the panel. Don't declare a variable for it. |
| `<NAME>_PORT` | The allocated host port for each declared port, name uppercased — `game` becomes `GAME_PORT`. |

The same set is exported as real environment variables inside both the install and the runtime
container. The runtime container additionally gets `PORT_<name>` (the same port, keyed by the
declared name as written) and `HOME=/data`.

The `bans`, `players`, and `restart` blocks use their own placeholders (`{{player}}`, `{{reason}}`,
`{{id}}`, `{{name}}`, `{{message}}`). Those are filled from the action, not from variables.

## `docker`

```yaml
docker:
  image: "eclipse-temurin:{{JAVA_VERSION}}-jre"
```

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `image` | string | — | **Required.** The runtime image. Templated. |
| `data_path` | string | `/data` | Where the server's persistent directory mounts inside the container. |
| `user` | string | the panel's own uid:gid | Overrides the runtime user. Templated. |
| `keep_entrypoint` | bool | `false` | Run the image's own `ENTRYPOINT` instead of clearing it. |
| `extra_volumes` | list of strings | — | Extra container paths that each get their own persisted directory. |
| `capabilities` | list of strings | — | Linux capabilities to add (`cap_add`). Allowlisted. |
| `devices` | list of strings | — | Host devices to expose. Allowlisted. |
| `sysctls` | map | — | Kernel parameters set in the container's namespace. Allowlisted. |

### `data_path` and the working directory

Yggdrasil bind-mounts the server's data directory into the container. Games leave `data_path`
unset: the mount lands on `/data` and the container's working directory is set to `/data`, so a
startup command like `./DayZServer` or `-jar server.jar` resolves against the server's files.

Set `data_path` when the image stores its state somewhere else — `/app/data` for Uptime Kuma,
`/var/www/html` for WordPress, `/etc/pihole` for Pi-hole. Setting it has a second effect: Yggdrasil
then leaves the working directory alone, so the image's own `WORKDIR` applies. That matters for
images that resolve relative paths against their `WORKDIR`, and it's why `builtin-runes/vaultwarden.yaml`
sets `data_path: /data` even though `/data` is already the default — same mount, but the image keeps
its own working directory.

### `user` and the `/etc/passwd` shim

By default the container runs as the panel's uid:gid, which keeps every file the server writes
editable from the Files tab. Set `user: "0:0"` for images that must start as root in order to drop
to their own `PUID`/`PGID` (linuxserver.io images, Gitea's s6 init, Nginx Proxy Manager). The
install script always runs as root regardless of this field.

Running as a uid that doesn't exist in the image's `/etc/passwd` breaks any binary that calls
`getpwuid()` — Steam-based servers segfault on the NULL result, which surfaces as a misleading
`CrashReporter: not found`. So when a rune does **not** set `keep_entrypoint`, Yggdrasil mounts a
minimal read-only `/etc/passwd` (root, the run-as user with home `/data`, and nobody) into the
container. Runes with `keep_entrypoint` don't get the shim on purpose: those images run their own
init and need their own named users (`git`, `www-data`), and overwriting `/etc/passwd` would delete
them.

### `keep_entrypoint`

Yggdrasil clears the image's `ENTRYPOINT` by default so that your startup command is the actual
command. Without this, an image like `cm2network/steamcmd` would pass your startup command as
arguments to its own entrypoint.

Set `keep_entrypoint: true` to run an off-the-shelf image the way a plain `docker run` would. The
startup command then becomes optional: leave it empty and the image's default `CMD` runs, or use
`startup.exec` to pass arguments to the image's entrypoint.

### `extra_volumes`

For images that insist on more than one mount. Each listed container path gets its own directory
under the server's data dir, named after the path (`/etc/letsencrypt` becomes `_etc_letsencrypt`),
created if missing.

```yaml
docker:
  image: "jc21/nginx-proxy-manager:latest"
  data_path: /data
  extra_volumes:
    - /etc/letsencrypt
```

Each target must be an absolute path with no `..`. Yggdrasil refuses targets that would shadow
sensitive container directories: exactly `/`, `/etc`, `/var`, `/var/run`, `/run` or `/home`, and
anything at or under `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/proc`, `/sys`, `/dev`, `/boot` or
`/root`. Subpaths of the exact-match denies are fine — `/etc/letsencrypt` passes, `/etc` does not.

### `capabilities`, `devices`, `sysctls`

These three widen a container's blast radius, and runes are semi-trusted — they get uploaded or
imported from GitHub. So Yggdrasil validates them against a fixed allowlist, both when the rune is
uploaded and every time it's loaded to start a server. Anything outside the list is a validation
error, not a warning.

| Field | Permitted values |
|-------|------------------|
| `capabilities` | `NET_ADMIN`, `NET_RAW`, `NET_BIND_SERVICE`, `SYS_NICE` |
| `devices` | `/dev/net/tun`, `/dev/dri`, `/dev/fuse` |
| `sysctls` | `net.ipv4.ip_forward`, `net.ipv6.conf.all.forwarding`, `net.ipv4.conf.all.src_valid_mark` |

Capability names are matched case-insensitively. A device entry is `host[:container[:perms]]` — the
container path defaults to the host path and permissions default to `rwm`; only the host part is
checked against the allowlist.

```yaml
docker:
  image: "tailscale/tailscale:latest"
  capabilities: ["NET_ADMIN"]
  devices: ["/dev/net/tun"]
  sysctls:
    net.ipv4.ip_forward: "1"
```

A rune cannot mount host paths. Host binds exist, but only an admin can add them per server, and
they never come from rune YAML.

## Two container patterns

Nearly every working rune is one of these two.

**A — you own the command.** Yggdrasil clears the entrypoint, runs your command as the panel user,
and mounts `/etc/passwd`. Use it for game servers and for app images whose binary you can invoke
directly. Files stay editable from the Files tab because the process runs as the panel's uid. This
is `builtin-runes/uptime-kuma.yaml` in full:

```yaml
gameskill:
  id: uptime-kuma
  name: "Uptime Kuma"
  category: "Apps"
  description: "Self-hosted uptime monitoring with status pages and alerts"
  author: "yggdrasil-community"
  version: 1
  icon: "app"

  docker:
    image: "louislam/uptime-kuma:1"
    data_path: /app/data

  install:
    image: "louislam/uptime-kuma:1"
    script: |
      mkdir -p /data
      echo "Uptime Kuma data directory ready"

  startup:
    command: "node server/server.js"
    done_regex: 'Listening on'

  ports:
    - { name: web, default: 3001, protocol: tcp }

  backup:
    include: ["."]
```

**B — the image owns the command.** `keep_entrypoint: true` plus `user: "0:0"` runs the image
exactly as its author intended: its init starts as root and drops to whatever uid it wants. Use it
when the image has an s6/init layer, an entrypoint that chowns or generates config, or no shell at
all. The cost is that files land under the image's uid, not the panel's.

```yaml
docker:
  image: "lscr.io/linuxserver/freshrss:latest"
  data_path: /config
  keep_entrypoint: true
  user: "0:0"
```

For a shell-less (distroless, ko-built) image, pair `keep_entrypoint` with `startup.exec` — see
`builtin-runes/cloudflared.yaml` and `community-runes/apps/headscale.yaml`.

## `variables`

Each variable is one field in the create-server form and in **Settings** on the server, and one env
var in the install and runtime containers.

```yaml
variables:
  - key: SERVER_TYPE
    name: "Server software"
    type: select
    options: [vanilla, paper, purpur, fabric, forge]
    default: paper
  - key: RCON_PASSWORD
    name: "RCON password"
    type: string
    default: "change-me"
    secret: true
```

| Field | Type | What it does |
|-------|------|--------------|
| `key` | string | **Required.** The env var name and the `{{KEY}}` placeholder. |
| `name` | string | The form label. Falls back to `key`. |
| `type` | string | **Required.** One of `string`, `int`, `bool`, `select`. |
| `options` | list | **Required for `select`.** The dropdown entries. |
| `default` | any | Pre-filled value. Stringified when it reaches the container. |
| `required` | bool | Marks the field with an asterisk in the form. |
| `min`, `max` | int | Bounds for an `int`. Optional, and independent — you can set just one. |
| `secret` | bool | Treats the value as a secret. |

There is no default type — a variable with no `type`, or an unknown one, fails validation.

| type | renders as | accepted values |
|------|-----------|-----------------|
| `string` | text input, or a password field if it's a secret | anything |
| `int` | number input, bounded by `min`/`max` | a whole number, within bounds |
| `bool` | checkbox, templated as `true` / `false` | `true` or `false` |
| `select` | dropdown over `options` | one of `options` |

**The panel enforces that third column.** A value that doesn't match what the variable declares is
refused with a `400` naming the field — `Max RAM (MB): must be at least 512, got 256` — rather than
being passed to the container to fail there in some less obvious way. An empty value means "use the
default", so optional fields you leave alone are fine.

Two details worth knowing if you're writing a rune:

- Bounds are checked **on create against the whole form, including your defaults** — so a default
  that contradicts its own `type` or bounds surfaces the first time someone builds a server, not at
  boot. On update only the fields being changed are checked, so tightening a rune's bounds later
  doesn't strand servers that already exist.
- Only variables you declare are checked. The env a container sees has other sources (injected
  ports, `HOME`, `SERVER_NAME`), and those pass through untouched.

`secret: true` does three things: the form renders a password field with show/generate/copy
controls, the value is encrypted at rest in the database, and it's masked in API responses. The
variable named by `rcon.password_var` gets the same treatment, whether or not it says so.

**Set `secret: true` on every sensitive variable. Do not rely on the name.** The form independently
renders a password-style field for any string variable whose key or label matches `pass`,
`password`, `secret`, or `token` — but that heuristic lives entirely in the frontend and only
chooses the *input widget*. Encryption and API masking key off `secret: true` (or
`rcon.password_var`) and nothing else. A variable called `API_TOKEN` without the flag therefore
looks protected in the form while being stored in plaintext in `servers.env_json` and returned
unmasked by `GET /api/servers/{id}`.

## `install`

Runs once, before the server may start, in a throwaway container. Its output streams live to the
install log in the UI.

```yaml
install:
  image: "eclipse-temurin:21-jre"   # defaults to docker.image
  script: |
    curl -fsSL -o server.jar "$URL"
    echo "eula={{EULA}}" > eula.txt
```

The script runs as root via `/bin/sh -c`, with the server's data directory mounted at `/data` and
the working directory set to `/data` — always `/data`, regardless of `docker.data_path`. Whatever
the script writes there persists as the server's files. The host filesystem is never exposed. When
the script finishes, Yggdrasil chowns `/data` to the panel's user so the server and the Files tab
can both write to it. A non-zero exit fails the install.

A rune with no `install` block is marked installed immediately. A server cannot be started until its
install has finished, and re-running an install on a running server recreates the container
afterwards so the new files take effect.

For Steam runes, the install container gets extra help — see [`steam`](#steam).

## `startup`

```yaml
startup:
  command: "java -Xmx{{MEMORY_MB}}M -jar server.jar nogui"
  done_regex: 'Done \(.*\)! For help'
  save_command: "save-all"
  stop: "stop"
  stop_timeout: 90
```

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `command` | string | — | The command, run via `/bin/sh -c`. Templated. |
| `exec` | list of strings | — | Raw argv, no shell. Each element templated. Takes precedence over `command`. |
| `done_regex` | string | — | Log pattern that promotes `starting` → `running`. |
| `save_command` | string | — | Console command sent before `stop`, to flush state to disk. |
| `stop` | string | — | Console command sent before the container is signalled. |
| `stop_timeout` | int | `30` | SIGTERM→SIGKILL grace period, in seconds. Capped at `300`. |

### `command` vs `exec`

`command` is the normal choice: it goes to `/bin/sh -c`, so pipes, `export`, and multi-line scripts
all work. Start the real process with `exec` inside the script (`exec java -jar server.jar`) so it
becomes PID 1 and receives both stdin and SIGTERM — without that, `stop` and the graceful shutdown
never reach the game.

`startup.exec` is a raw argv list with no shell involved. Use it for images with no shell (distroless,
ko-built), or to pass arguments to an image's own entrypoint alongside `keep_entrypoint`. When
`exec` is set, `command` is ignored.

```yaml
docker:
  image: "cloudflare/cloudflared:latest"
  keep_entrypoint: true
startup:
  exec: ["tunnel", "--no-autoupdate", "run"]
  done_regex: 'Registered tunnel connection|Starting tunnel'
```

### `done_regex`

A freshly started server is `starting`, not `running`. Every three seconds Yggdrasil scans the last
2000 lines of the container log for `done_regex`; the first match flips the server to `running`.
Without a `done_regex`, a container that stays up is called `running` on the next poll.

Pick a line the game prints exactly once, when it's actually accepting players. Alternatives with
`|` are fine. If the container exits before the pattern matches, the server goes to `stopped` and
the start-failure path takes over.

A `done_regex` that never matches is not fatal, but it is a bad time: the server sits in `starting`
for five minutes, you get a "taking a long time" notification, and at ten minutes Yggdrasil gives up
waiting and marks it `running` anyway if the container is still alive. Ten minutes of a wrong status
badge, plus anything gated on `running` running late. Test the pattern against a real log.

### Stopping cleanly

On stop and on restart, Yggdrasil sends `save_command` to the console, waits two seconds, sends
`stop`, waits two more, then asks Docker to stop the container with `stop_timeout` seconds of grace
before SIGKILL.

**Both commands go to the container's stdin, always — never over RCON**, even on a rune with an
enabled `rcon` block. That's why the game must be PID 1 and must read commands on stdin. If your
game only takes commands over RCON, `save_command` and `stop` will silently do nothing: rely on
`stop_timeout` and a clean SIGTERM instead. (Other features — bans, restart warnings, scheduled
commands — *do* prefer RCON and fall back to stdin. Graceful stop is the exception.)

Games with no console save command need the timeout instead. DayZ flushes its whole Central Economy
state on a clean SIGTERM and uses `stop_timeout: 90` rather than being killed mid-save.

## `config_files`

The files an operator actually edits, out of everything in the server's data directory.

```yaml
config_files:
  - "server.properties"
  - "whitelist.json"
  - "config/paper-world-defaults.yml"
```

The Files tab turns each into a one-click shortcut. This matters more than it sounds: a rune's
`variables` are the handful of settings that get templated in at install, but a game's real
configuration lives in its own files — a `server.properties` has around fifty entries, and Rust
keeps its config four directories down at `server/yggdrasil/cfg/server.cfg`. Without a shortcut,
"change the MOTD" starts with knowing the layout.

Paths are relative to the server's data directory. Absolute paths and anything containing `..` are
rejected when the rune is uploaded.

A listed file that doesn't exist is not an error — most are written by the game on first boot, so a
freshly created server has none of them, and the panel says so rather than reporting a failure.

Files ending in `.properties`, `.env`, `.cfg` or `.conf` open in a generated key/value form, with a
raw-text view a click away. Anything else opens as raw text.

## `ports`

```yaml
ports:
  - { name: game, default: 25565, protocol: tcp }
  - { name: rcon, default: 25575, protocol: tcp }
```

| Field | Type | What it does |
|-------|------|--------------|
| `name` | string | **Required.** The port's role. Becomes `<NAME>_PORT` and `PORT_<name>`. |
| `default` | int | The container-side port. |
| `protocol` | string | **Required.** Exactly `tcp` or `udp`. |

For each entry, Yggdrasil allocates a free **host** port from the configured range (25000–30000 by
default, see [Configuration](configuration.md)) and publishes the container port to it. The
allocator walks the range sequentially and test-binds each candidate; it deliberately ignores
`default`, because well-known game ports are the ones that get scanned. `default` is only the
container side of the mapping.

Steam runes are the exception: they publish 1:1, so the container port equals the allocated host
port. A Steam server registers its bind port with the Steam master server, so bind, publish and
advertised port all have to be the same number. This is what makes `-port={{GAME_PORT}}` correct in
a Steam rune's startup command.

Three names are load-bearing. `game` is the fallback for both query and RCON. `query` is preferred
by the query poller if present. `rcon` is preferred by the console if present.

## `query`

```yaml
query:
  type: minecraft
```

`type` is one of `a2s` (or its alias `source`) for Steam games, `minecraft` (alias
`minecraft-java`), or `minecraft-bedrock`. Anything else is an error at query time.

Yggdrasil polls the `query` host port if the rune declares one, otherwise `game`, and uses the
result for the dashboard's player count and liveness. Declaring a `query` block is also what makes
a server eligible for the watchdog, which restarts a server that stops answering.

## `rcon`

```yaml
rcon:
  enabled: true
  type: minecraft
  password_var: RCON_PASSWORD
```

| Field | Type | What it does |
|-------|------|--------------|
| `enabled` | bool | Turns the console box and RCON delivery on. |
| `type` | string | `minecraft` or `source` (both Source RCON, and the default when empty), `rust-websocket`, or `battleye`. |
| `password_var` | string | The variable holding the password. Always stored encrypted. |

Yggdrasil dials `127.0.0.1` on the server's `rcon` host port, falling back to the `game` port —
BattlEye shares the game port. It does not read a port out of a variable.

An enabled `rcon` block is what the Console tab, schedules, bans, restart warnings and the Players
tab use to reach the game; without it they fall back to the container's stdin, or aren't offered at
all. If the game
needs RCON switched on in a config file, do that in the install script — Minecraft's rune writes
`enable-rcon=true` into `server.properties`.

## `steam`

```yaml
steam:
  anonymous: false
```

Declaring a `steam` block changes four things: host ports are published 1:1, the install script is
prefixed with a `chmod -R a+rwX /data` (SteamCMD drops privileges and can't otherwise rewrite
`steamapps/` on a reinstall), a persistent SteamCMD cache is mounted at `/steamcache` with `HOME`
pointing at it, and the data dir is made world-writable for the install.

`anonymous: true` games just install. With `anonymous: false`, Yggdrasil requires an authorized
Steam account — set one up once under **Settings → Steam** — and injects its username as
`STEAM_USER` for the install script to use. The install fails with a clear message if no account is
authorized. The sentry cache in `/steamcache` means Steam Guard isn't re-triggered on later updates.

Your install script issues the SteamCMD commands itself, including the app id.

## `bans`

```yaml
bans:
  ban_command: "ban {{player}} {{reason}}"
  unban_command: "pardon {{player}}"
```

Centralized ban management substitutes `{{player}}` and `{{reason}}` and delivers the command over
RCON, or the container's stdin if the rune has no enabled `rcon` block. Control characters in both
values are collapsed to spaces before substitution, so a crafted player name can't inject a second
command. Omit the block for games with no console ban.

## `players`

Adds the live Players tab. Requires an enabled `rcon` block — validation rejects the rune otherwise.

```yaml
players:
  list_command: "players"
  player_regex: '^(?P<id>\d+)\s+(?P<ip>[0-9.]+):\d+\s+(?P<ping>\d+)\s+(?P<guid>[0-9a-f]+)\s*\((?:OK|\?)\)\s+(?P<name>.+?)\s*$'
  kick_command: "kick {{id}} {{reason}}"
  broadcast_command: "say -1 {{message}}"
  lock_command: "#lock"
  unlock_command: "#unlock"
```

| Field | Required | What it does |
|-------|----------|--------------|
| `list_command` | yes | Run over RCON; its text response is parsed line by line. |
| `player_regex` | yes | Must compile and must have a `(?P<name>...)` group. |
| `kick_command` | no | Templated with `{{id}}`, `{{name}}`, `{{reason}}`. |
| `broadcast_command` | no | Templated with `{{message}}`. |
| `lock_command` | no | No template. |
| `unlock_command` | no | No template. |

`player_regex` runs against each line of the response; lines that don't match (headers, totals) are
skipped. `name` is required; `id`, `ping`, `guid` and `ip` are optional and shown when captured. Any
action command you leave out is not offered in the UI, so read-only listing is a valid rune.

Single-quote the regex in YAML so backslashes stay literal.

## `admin_log`

Turns a game's admin/activity log into a parsed feed of joins, leaves, deaths and kills.

```yaml
admin_log:
  path: "profiles/*.ADM"
  time_regex: '^(?P<time>\d{1,2}:\d{2}:\d{2})'
  events:
    - { type: "kill",  regex: 'Player "(?P<name>[^"]+)".*killed by' }
    - { type: "join",  regex: 'Player "(?P<name>[^"]+)" is connected' }
```

`path` is a glob relative to the server's data dir; the most recently modified match is read. It
can't contain `..`. `events` is required and each entry needs a `type` (your own label) and a
`regex` that compiles; an optional `(?P<name>...)` group names the player. The first rule that
matches a line wins, and unmatched lines are dropped. `time_regex` pulls a timestamp off the front
of the line — its `time` group if it has one, otherwise the whole match.

## `wipe`

```yaml
wipe:
  paths: ["mpmissions/*/storage_*"]
```

Gives the rune a Wipe button and a schedulable wipe action. `paths` is required when the block is
present: glob patterns relative to the server's data dir, deleted on wipe. Yggdrasil refuses an
empty entry, `/`, `.`, or anything containing `..` — at upload and again at wipe time. A wipe stops
the server, deletes the matches, and restarts it if it was running.

## `restart`

```yaml
restart:
  warnings:
    - { at: "15m", msg: "say ⚠ Server restart in 15 minutes." }
    - { at: "5m",  msg: "say ⚠ Server restart in 5 minutes." }
    - { at: "1m",  msg: "say ⚠ Restarting in 1 minute!" }
```

Enables warned restarts, manual and scheduled. Yggdrasil sorts the warnings by `at` descending and
sends each `msg` that long before the restart. `at` is a Go duration (`15m`, `60s`) and must parse
to something greater than zero. `msg` is a complete broadcast command for the game, delivered over
RCON or stdin — not just the text.

## `backup`

```yaml
backup:
  include: ["world", "world_nether", "server.properties", "plugins"]
```

Paths relative to the server's data dir, archived into the backup tarball. Files and directories
both work; there is no globbing, and a listed path that doesn't exist is skipped rather than failing
the backup. Omit `include`, or leave it empty, to archive the whole data dir.

## `anticheat`

```yaml
anticheat:
  antixray:
    supported: true
    config_hint: "Paper anti-xray (engine-mode 1/2) in config/paper-world-defaults.yml"
  battleye:
    supported: true
    config_hint: "BattlEye enabled via -BEpath=battleye"
  plugins_recommended: ["Grim", "Vulcan", "NoCheatPlus"]
```

Purely informational. `antixray` and `battleye` each take a `supported` bool and a `config_hint`
string; `plugins_recommended` is a list of names. All of it is rendered on the server's **Anti-cheat**
tab and nothing acts on it.

## Fields the panel parses but doesn't use

These appear in real runes and in the schema, and they validate fine, but no panel feature reads
them. Don't rely on them:

- `author`, `icon` — descriptive only.
- `steam.app_id` — your install script passes the app id to SteamCMD itself.
- `wipe.backup_first` — whether a wipe takes a safety backup first is chosen when you run or
  schedule the wipe.

`query.port` and `rcon.port_var` used to be here. They are gone from the schema: the port to
query or send RCON to comes from the [`ports`](#ports) block — a mapping named `query` or `rcon`,
falling back to `game`. That mapping is what actually gets allocated and published, so a second
place to say it could only ever disagree with it. A rune that still sets either is not an error;
the value is ignored, as it always was.

## A complete rune

This is `builtin-runes/minecraft-java.yaml`, the rune behind the panel's built-in Minecraft (Java)
servers. It uses pattern A, and every block above except `steam`, `players` and `admin_log`.

```yaml
gameskill:
  id: minecraft-java
  name: "Minecraft (Java)"
  category: "Minecraft"
  description: "Vanilla / Paper / Purpur / Fabric / Forge Java server"
  author: "yggdrasil-core"
  version: 7
  icon: "minecraft"

  docker:
    # The JRE image depends on the resolved Java version (see install step).
    image: "eclipse-temurin:{{JAVA_VERSION}}-jre"

  variables:
    - key: SERVER_TYPE
      name: "Server software"
      type: select
      options: [vanilla, paper, purpur, fabric, forge]
      default: paper
    - key: MC_VERSION
      name: "Minecraft version"
      type: string
      default: "latest"
    - key: JAVA_VERSION
      name: "Java runtime"
      type: select
      options: ["25", "21", "17"]
      default: "25"
    - key: MEMORY_MB
      name: "Max RAM (MB)"
      type: int
      default: 4096
    - key: LEVEL_SEED
      name: "World seed (blank = random)"
      type: string
      default: ""
    - key: LEVEL_TYPE
      name: "World type (only applies to a new world)"
      type: select
      options: [normal, flat, large_biomes, amplified]
      default: normal
    - key: DIFFICULTY
      name: "Difficulty"
      type: select
      options: [peaceful, easy, normal, hard]
      default: easy
    - key: RCON_PORT
      name: "RCON port (container-internal)"
      type: int
      default: 25575
    # Encrypted at rest and masked in the API because `rcon.password_var` below
    # names it — not because of its name. Any other sensitive variable would
    # need an explicit `secret: true`.
    - key: RCON_PASSWORD
      name: "RCON password"
      type: string
      default: "change-me"
    - key: ENABLE_WHITELIST
      name: "Enable whitelist (only listed players can join)"
      type: bool
      default: false
    - key: EULA
      name: "Accept the Minecraft EULA"
      type: bool
      default: false
      required: true

  # Runs once as root, with the data dir at /data. Downloads the jar the chosen
  # SERVER_TYPE needs and seeds the config files the panel depends on.
  install:
    image: "eclipse-temurin:21-jre"
    script: |
      set -eu
      # eclipse-temurin is Ubuntu-based; add curl + jq for robust JSON parsing.
      if ! command -v curl >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq curl ca-certificates jq >/dev/null
      fi

      TYPE="{{SERVER_TYPE}}"
      VER="{{MC_VERSION}}"
      MANIFEST=https://launchermeta.mojang.com/mc/game/version_manifest_v2.json

      latest_vanilla() { curl -fsSL "$MANIFEST" | jq -r '.latest.release'; }
      need() { [ -n "$1" ] && [ "$1" != "null" ] || { echo "$2"; exit 1; }; }

      echo "Installing $TYPE (requested version: $VER)"
      case "$TYPE" in
        vanilla)
          [ "$VER" = "latest" ] && VER="$(latest_vanilla)"
          need "$VER" "could not resolve latest Minecraft version"
          VER_URL=$(curl -fsSL "$MANIFEST" | jq -r --arg v "$VER" '.versions[] | select(.id==$v) | .url')
          need "$VER_URL" "version $VER not found"
          SRV_URL=$(curl -fsSL "$VER_URL" | jq -r '.downloads.server.url')
          need "$SRV_URL" "no server download for $VER"
          curl -fsSL -o server.jar "$SRV_URL"
          ;;
        paper)
          # Paper v3 (fill) API — v2 is frozen at 1.21.x. Newest version is the
          # first patch of the first version group.
          UA="yggdrasil-installer"
          if [ "$VER" = "latest" ]; then
            VER=$(curl -fsSL -A "$UA" https://fill.papermc.io/v3/projects/paper | jq -r '.versions | to_entries[0].value[0]')
          fi
          need "$VER" "could not resolve Paper version"
          URL=$(curl -fsSL -A "$UA" "https://fill.papermc.io/v3/projects/paper/versions/$VER/builds/latest" \
            | jq -r '.downloads["server:default"].url')
          need "$URL" "no Paper build for $VER"
          curl -fsSL -A "$UA" -o server.jar "$URL"
          ;;
        purpur)
          [ "$VER" = "latest" ] && VER=$(curl -fsSL https://api.purpurmc.org/v2/purpur | jq -r '.versions[-1]')
          need "$VER" "could not resolve Purpur version"
          curl -fsSL -o server.jar "https://api.purpurmc.org/v2/purpur/$VER/latest/download"
          ;;
        fabric)
          [ "$VER" = "latest" ] && VER="$(latest_vanilla)"
          need "$VER" "could not resolve Minecraft version for Fabric"
          LOADER=$(curl -fsSL "https://meta.fabricmc.net/v2/versions/loader/$VER" | jq -r '.[0].loader.version')
          INSTALLER=$(curl -fsSL https://meta.fabricmc.net/v2/versions/installer | jq -r '.[0].version')
          need "$LOADER" "no Fabric loader for $VER"
          need "$INSTALLER" "no Fabric installer"
          curl -fsSL -o server.jar \
            "https://meta.fabricmc.net/v2/versions/loader/$VER/$LOADER/$INSTALLER/server/jar"
          ;;
        forge)
          echo "Forge requires running its installer; install the Forge universal jar"
          echo "and adjust the startup command. Not yet automated."
          exit 1
          ;;
        *)
          echo "unknown SERVER_TYPE: $TYPE"; exit 1 ;;
      esac

      # Make sure we actually got a jar.
      [ -s server.jar ] || { echo "download produced no server.jar"; exit 1; }

      echo "eula={{EULA}}" > eula.txt

      # Append a trailing newline first if the file lacks one, so we never merge
      # onto the last existing line (e.g. a hand-edited server.properties).
      # Append a trailing newline only if the file exists and lacks one. Must
      # return 0 even when the file is absent (fresh install) — otherwise `set -e`
      # would abort the whole install.
      ensure_nl() { if [ -s server.properties ] && [ -n "$(tail -c1 server.properties)" ]; then echo >> server.properties; fi; }
      # Pre-seed RCON + whitelist settings so the panel (and schedules/bans) can
      # connect. Minecraft fills in any remaining server.properties on first run.
      if ! grep -q '^enable-rcon=' server.properties 2>/dev/null; then
        ensure_nl
        {
          echo "enable-rcon=true"
          echo "rcon.port={{RCON_PORT}}"
          echo "rcon.password={{RCON_PASSWORD}}"
          echo "white-list={{ENABLE_WHITELIST}}"
          echo "enforce-whitelist={{ENABLE_WHITELIST}}"
          echo "difficulty={{DIFFICULTY}}"
          # World type is fixed at generation — only meaningful on the first run.
          echo "level-type=minecraft:{{LEVEL_TYPE}}"
        } >> server.properties
      fi
      # World seed (only on first install, before the world is generated).
      if [ -n "{{LEVEL_SEED}}" ] && ! grep -q '^level-seed=' server.properties 2>/dev/null; then
        ensure_nl
        echo "level-seed={{LEVEL_SEED}}" >> server.properties
      fi
      # Ensure the whitelist/ops files exist so they're editable in the file manager.
      [ -f whitelist.json ] || echo "[]" > whitelist.json
      [ -f ops.json ] || echo "[]" > ops.json

      echo "Installed $TYPE $VER -> server.jar"
      ls -la server.jar

  startup:
    # Re-stamp eula.txt + the mutable server.properties toggles (difficulty,
    # whitelist) from the CURRENT settings on every start, so changing them in
    # Settings + restart actually takes effect — install only writes them once,
    # and the toggle otherwise looks applied while the server keeps the old value.
    # World type is omitted — it's fixed at world generation and can't change.
    # `exec` hands PID 1 + stdin to Java so the "stop" command and signals reach it.
    # --enable-native-access=ALL-UNNAMED silences the Java 24+ "restricted
    # method ... java.lang.System::load (JNA)" warnings; harmless either way.
    command: |
      setprop() { if grep -q "^$1=" server.properties 2>/dev/null; then sed -i "s/^$1=.*/$1=$2/" server.properties; else echo "$1=$2" >> server.properties; fi; }
      echo eula={{EULA}} > eula.txt
      setprop difficulty {{DIFFICULTY}}
      setprop white-list {{ENABLE_WHITELIST}}
      setprop enforce-whitelist {{ENABLE_WHITELIST}}
      exec java -Xmx{{MEMORY_MB}}M --enable-native-access=ALL-UNNAMED -jar server.jar nogui
    done_regex: 'Done \(.*\)! For help'
    # Flush chunks to disk before shutting down, then stop cleanly (the "stop"
    # command already saves, but save-all first makes a graceful restart robust).
    save_command: "save-all"
    stop: "stop"

  query:
    type: minecraft

  # RCON itself is switched on by the install script, in server.properties.
  rcon:
    enabled: true
    type: minecraft
    password_var: RCON_PASSWORD

  config_files:
    - "server.properties"
    - "whitelist.json"
    - "ops.json"
    - "config/paper-world-defaults.yml"

  # The host ports are allocated from the panel's range; 25565 and 25575 are only
  # the container side of each mapping.
  ports:
    - { name: game, default: 25565, protocol: tcp }
    - { name: rcon, default: 25575, protocol: tcp }

  anticheat:
    antixray:
      supported: true
      config_hint: "Paper anti-xray (engine-mode 1/2) in config/paper-world-defaults.yml"
    plugins_recommended: ["Grim", "Vulcan", "NoCheatPlus"]

  bans:
    ban_command: "ban {{player}} {{reason}}"
    unban_command: "pardon {{player}}"

  backup:
    include: ["world", "world_nether", "world_the_end", "server.properties", "whitelist.json", "ops.json", "plugins"]

  # "Wipe" deletes the world folders; a fresh world regenerates on next start
  # (same seed if level-seed is set, otherwise random). Keeps server.properties,
  # whitelist and ops.
  wipe:
    paths: ["world", "world_nether", "world_the_end"]
    backup_first: true

  # In-game countdown broadcast before a safe restart.
  restart:
    warnings:
      - { at: "15m", msg: "say ⚠ Server restart in 15 minutes." }
      - { at: "5m",  msg: "say ⚠ Server restart in 5 minutes." }
      - { at: "1m",  msg: "say ⚠ Restarting in 1 minute — find a safe spot!" }
```

## Authoring a new rune

Start from a rune that already works. `builtin-runes/` has the five that ship with the panel;
`community-runes/` has thirty-odd more, split into `games/`, `apps/` and `databases/`. Copy the
closest one and change the image, variables and startup command.

1. **Write the YAML.** Give it a new `id` — you cannot overwrite a built-in rune, and re-uploading
   an existing `id` replaces that rune in place.
2. **Get it into the panel.** Either **Runes → Carve a rune (upload)** and pick your `.yaml`
   (512 KB maximum), or **Runes → Browse GitHub** to install straight from a repo folder — it
   defaults to this project's `community-runes/`, and takes any `owner/repo`, path and branch.
   Pterodactyl eggs and XML definitions import under **Import egg** / **Import XML**. All of these
   need admin rights.
3. **Read the validation errors.** Yggdrasil parses and validates on upload and refuses anything
   invalid, naming the field: a missing `startup.command`, a variable with no `type`, a port with no
   protocol, a `player_regex` that doesn't compile or lacks its `name` group, a capability outside
   the allowlist. The same validation runs every time a server starts, so a rune that uploads is a
   rune that loads.
4. **Test it.** Create a server from the rune, watch the install log for the script's output, then
   start it and watch the console. The two things that usually need a second pass are the startup
   command's paths (working directory, library paths) and `done_regex` — if the server plays fine
   but the badge stays on `starting`, the regex is wrong.

Iterate by re-uploading the same `id`. Existing servers pick up the new YAML on their next start,
since the container is rebuilt from the rune every time.

## See also

- [Configuration](configuration.md) — the port range runes allocate from
- [API reference](api.md) — the `/api/gameskills` endpoints behind the Runes page
- [Networking](../guides/networking.md) — making an allocated port reachable
