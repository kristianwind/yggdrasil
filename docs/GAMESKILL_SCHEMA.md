# Gameskill Schema

A **gameskill** (shown as a *Rune* in the UI) is a declarative YAML file that
teaches Yggdrasil how to install, run, query, and manage one game. Upload your
own under **Runes → Carve a rune**, or import a Pterodactyl egg.

A gameskill is validated on upload; clear errors are shown if it's invalid.

## Top-level

```yaml
gameskill:
  id: minecraft-java          # required, unique slug (a-z0-9-)
  name: "Minecraft (Java)"    # required, display name
  category: "Minecraft"       # default realm / group
  description: "..."          # shown in the UI
  author: "yggdrasil-core"
  version: 1
  icon: "minecraft"           # icon reference or URL
  docker: { ... }             # required
  variables: [ ... ]          # the auto-generated settings form
  install: { ... }            # one-time setup (optional)
  startup: { ... }            # required
  query: { ... }              # player count / status (optional)
  rcon: { ... }               # remote console (optional)
  steam: { ... }              # SteamCMD games (optional)
  config_files: [ ... ]       # files exposed in the editor (optional)
  ports: [ ... ]              # host ports to allocate
  anticheat: { ... }          # anti-cheat hooks (optional)
  bans: { ... }               # ban/unban console commands (optional)
  backup: { ... }             # what to include in backups (optional)
```

## `docker` (required)

```yaml
docker:
  image: "eclipse-temurin:{{JAVA_VERSION}}-jre"
```

`image` supports `{{VARIABLE}}` templating from the server's variables. The image
must contain the runtime the game needs (JRE, Steam libs, …).

## `variables`

Each variable becomes a form field when creating a server and an environment
variable in the install/startup containers.

```yaml
variables:
  - key: SERVER_TYPE        # required — the env var name
    name: "Server software" # field label
    type: select            # string | int | bool | select
    options: [vanilla, paper, purpur, fabric, forge]  # required for select
    default: paper
    required: true          # optional
```

| type | renders as | notes |
|------|-----------|-------|
| `string` | text input | |
| `int` | number input | |
| `bool` | checkbox | templated as `true`/`false` |
| `select` | dropdown | needs `options` |

Variables are referenced as `{{KEY}}` in `docker.image`, `install.script`,
`startup.command`, and pre-seeded files, and are exported as real env vars
(`KEY=value`) inside containers. Allocated host ports are also exported as
`PORT_<name>` env vars.

## `install` (optional)

Runs once, in an ephemeral container, into the server's data volume (`/data`).
Output streams live to the UI's install log.

```yaml
install:
  image: "eclipse-temurin:21-jre"   # defaults to docker.image
  script: |
    ./fetch-server.sh "{{SERVER_TYPE}}" "{{MC_VERSION}}"
    echo "eula={{EULA}}" > eula.txt
```

The working directory is `/data`; whatever the script writes there persists as
the server's files. The host filesystem is never exposed.

## `startup` (required)

```yaml
startup:
  command: "java -Xmx{{MEMORY_MB}}M -jar server.jar nogui"
  done_regex: 'Done \(.*\)! For help'   # marks the server as "running"
  stop: "stop"                          # graceful-stop console command (else SIGTERM)
```

The command becomes the container's command (run via `/bin/sh -lc`).

## `query` (optional)

```yaml
query:
  type: minecraft   # minecraft | minecraft-bedrock | a2s
```

Used for the dashboard's player count / liveness. Yggdrasil queries the `query`
port mapping if present, else `game`.

## `rcon` (optional)

```yaml
rcon:
  enabled: true
  type: minecraft        # minecraft/source | rust-websocket | battleye
  port_var: RCON_PORT
  password_var: RCON_PASSWORD
```

Drives the console-command box, scheduled commands/messages, and ban delivery.
The password is read from the named variable. For games where RCON must be
enabled in a config file (e.g. Minecraft's `server.properties`), do that in the
install script.

## `steam` (optional)

```yaml
steam:
  app_id: 258550
  anonymous: true     # false for games requiring an account that owns them
```

`anonymous: false` games (e.g. DayZ) require the one-time **Authorize Steam
account** flow; the SteamCMD sentry cache is then reused so Steam Guard isn't
re-triggered.

## `config_files` (optional)

```yaml
config_files:
  - "server.properties"
  - "config/paper-world-defaults.yml"
```

Surfaced in the built-in file/config editor.

## `ports`

```yaml
ports:
  - { name: game, default: 25565, protocol: tcp }
  - { name: rcon, default: 25575, protocol: tcp }
```

Yggdrasil allocates a conflict-free **host** port for each and maps it to the
container's `default` port. Protocol is `tcp` or `udp`.

## `anticheat` (optional)

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

Surfaced informationally on the server's Anti-cheat tab.

## `bans` (optional)

```yaml
bans:
  ban_command: "ban {{player}} {{reason}}"
  unban_command: "pardon {{player}}"
```

Used by centralized ban management. `{{player}}` and `{{reason}}` are substituted
and the command is delivered via RCON/console. Omit for games with no console ban.

## `backup` (optional)

```yaml
backup:
  include: ["world", "world_nether", "world_the_end", "server.properties", "plugins"]
```

Paths (relative to `/data`) included in archives. When omitted, the whole data
directory is archived.
