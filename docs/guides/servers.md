# Managing servers

Everything about a single game or app instance in Yggdrasil Panel: creating it, the
states it moves through, keeping it alive, and the tools on its detail page. Read
[Runes](runes.md) first if you don't yet know what a rune is.

## Creating a server

A server is one Docker container plus one data directory on the host. You need three
things to make one:

- a **rune** — the definition of the game or app (see [Runes](runes.md)),
- a **realm** — the group it belongs to,
- values for the rune's **variables** — RAM, world seed, RCON password, and so on.

Open **Servers → + New server**. Pick the realm, pick the rune, name the server, and
fill in the variables the rune declares. Anything you leave alone takes the rune's
default. If you leave the realm empty, Yggdrasil files the server under a realm named
after the rune's category, creating that realm if it doesn't exist yet (or `Default`
when the rune declares no category).

Two more fields sit at the bottom of the dialog:

- **CPU limit (%)** and **RAM limit (MB)** — `0` means unlimited. See
  [Resource limits](#resource-limits).
- **Start automatically after a reboot** — on by default.

A **Subdomain** field appears only for runes that declare a port named `web`; it hands
the server to Nginx Proxy Manager or Cloudflare Tunnel. That's covered in
[Networking](networking.md).

Yggdrasil allocates the host ports, creates the data directory, saves the server, and
starts the install immediately — you land on the server's detail page with the install
log already streaming.

### Port allocation

Yggdrasil ignores the game's well-known port (2302, 25565, …) and allocates
sequentially from the configured range instead — `ports.range_min` to
`ports.range_max` in your config file, which default to **25000–30000**. Distinctive
ports get scanned and abused less, and every server ends up with its own.

A port is only handed out if it is free three ways over: not already in Yggdrasil's
allocation table, not published by any container Docker knows about (including
orphans), and bindable right now on the host. Each rune port becomes an environment
variable in two forms — `PORT_<name>` and `<NAME>_PORT` (so `GAME_PORT`,
`QUERY_PORT`) — which is how a rune's command line learns the real external port.

For Steam games the container publishes each port **1:1**: the bound port inside the
container equals the published host port. Steam servers register their bind port with
the Steam master server, so it has to match what the outside world connects to.
Non-Steam runes bind the rune's fixed default port inside the container and publish it
on the allocated host port.

## The lifecycle

Yggdrasil tracks two things separately: whether the server is **installed**, and what
its container is doing.

| Value | Meaning |
|-------|---------|
| `install_status: installing` | the rune's install script is running right now |
| `install_status: done` | files are on disk; the server can start |
| `install_status: error` | the install script failed; press **Retry install** |
| `status: starting` | the container is up; Yggdrasil is waiting for a readiness signal |
| `status: running` | the game answered — it's ready |
| `status: stopped` | no container, or the container exited |

A new server sits at `stopped` while its install runs, and cannot be started until the
install finishes. Start it and the status goes to `starting`. What promotes it to
`running` is the rune's `done_regex` — a regular expression matched against the
container's log output.

The watcher behind this polls every 3 seconds for up to **10 minutes**, scanning the
last 2000 log lines each pass (games are noisy while they load). Then:

- the regex matches → `running`,
- the rune declares no `done_regex` → the container being up is the only signal
  available, so it becomes `running` as soon as it's confirmed up,
- the container exits first → back to `stopped`, and start-failure detection takes
  over,
- 10 minutes pass and it's still up but silent → Yggdrasil shows it as `running`
  rather than leave you staring at `starting` forever.

If a rune has a `done_regex` and the server hasn't signalled after **5 minutes**, you
get a one-time "taking longer than usual" notification with the latest log lines
attached. It keeps waiting; that's a heads-up, not a failure.

Separately, a reconciler sweeps every **20 seconds** and flips any server whose
container has died to `stopped`, so a crash shows up in the UI without you refreshing.

## Installing, updating, reinstalling

The install runs the rune's install script inside an **ephemeral container** — pulled
fresh, run as root, removed when it exits — with the server's data directory mounted at
`/data`. Output streams live to the **Install log** tab over a WebSocket, and the last
500 lines are replayed if you open the tab late or reconnect. When the script succeeds,
Yggdrasil hands `/data` back to the panel's own user so both the server process and the
file manager can write there, and marks the server installed. A rune with no install
block is installed the moment it's created.

Steam games get some extra handling. The install container makes `/data` writable
before SteamCMD runs, and mounts a persistent SteamCMD cache so Steam Guard isn't
re-triggered on every update. Games that need a real Steam account (rather than
anonymous login) fail with a clear message until you authorize one under
**Settings → Steam**.

**Update / Reinstall** on the server page re-runs that same script. That's how you
update the game to the latest version and re-download mods. It regenerates whatever the
script writes, so config files can be overwritten — back up first. If the server was
running when you pressed it, Yggdrasil recreates the container afterwards so the new
files are actually in use.

## Start, stop, restart

**Start** and **Restart** both recreate the container from scratch: the old one is
removed and a new one is created from the rune, variables, and ports as they are *right
now*. This matters. A container bakes in its command and environment at creation time,
so changing a variable or a mod list and calling a plain `docker restart` would change
nothing. Restart in Yggdrasil is a recreate, so **env, mod, and rune changes apply on
restart** — no stop/start dance required. Restart does not update the game version;
that's **Update / Reinstall**.

Recreating also reconciles ports. If one of your allocated host ports has been taken by
something else while the server was down, Yggdrasil allocates a new one and records it
rather than failing to bind.

### Graceful stop

Stopping is not a kill. Yggdrasil asks the rune how to shut this game down cleanly:

1. send the rune's `save_command` over the console (Minecraft's `save-all`), wait 2
   seconds for the flush,
2. send the rune's `stop` command, wait 2 seconds,
3. `docker stop` with the rune's `stop_timeout` as the SIGTERM→SIGKILL grace period.

`stop_timeout` defaults to **30 seconds** and a rune may raise it to at most **300**.
The generous default exists because some games flush a lot of state on shutdown — DayZ
writes its entire Central Economy and sets `stop_timeout: 90`. A rune that declares
neither command still gets the full grace period.

The same graceful stop runs before every recreate, so a restart doesn't cut a save in
half either.

### Safe restart

**Safe restart** is the one to use on a populated server. It broadcasts the rune's
restart countdown in-game — each warning fires at its declared time before the restart,
largest first, so a `[15m, 5m, 1m]` set warns at fifteen, five, and one minute — then
restarts. Tick **Back up before each restart** and a backup runs first.

The API call returns immediately; the countdown runs in the background. A rune with no
warnings declared backs up (if asked) and restarts promptly — the button still works, it
has nothing to say to players.

### Auto-restart

**🔁 Auto-restart** schedules a recurring restart, using the same warned restart path.
It's a front door onto the scheduler: under the hood it manages one schedule row for you,
so you never hand-edit cron. The dialog pre-fills **6 hours**, warnings on, backup off.

Two fields decide the timing:

- **Restart every** — the interval, 1 to 24 hours. 24 means once a day.
- **Starting at** — the hour the cycle is anchored to.

The anchor is what makes the interval useful. "Every 6 hours" on its own always lands on
00:00, 06:00, 12:00 and 18:00 — which is no help if 18:00 is your busiest hour. Anchor it
at 03:00 and it fires at 03:00, 09:00, 15:00 and 21:00 instead. The dialog spells out the
exact times below the fields, so you can see what you picked.

### Restarting when nobody's on

The dialog shows a **quiet-hours** hint when there's data for it: Yggdrasil buckets the
last 14 days of sampled player counts by local hour and names the calmest one — "Quietest
around 05:00 (avg 0.4 players, last 14 days)". Next to it is a **Start there** link that
sets the anchor to that hour, so the recommendation is one click rather than arithmetic.

A brand-new server, or a game whose rune declares no query, has no player data. The hint
stays hidden rather than guess.

Both the hint and the schedule use the panel host's local time, so they agree with each
other. If your players are in a different timezone than your box, the "quietest hour" is
still the right hour — it's measured from real player counts, not from a clock.

One wrinkle when the interval doesn't divide 24 evenly: the cycle restarts at the anchor
each day rather than rolling past midnight. Every 5 hours from 03:00 fires at 03:00,
08:00, 13:00, 18:00 and 23:00 — then 03:00 again, a four-hour gap instead of five. Intervals
that divide 24 (1, 2, 3, 4, 6, 8, 12, 24) don't have this, which is why those are the ones
the dialog offers.

See [Backups and schedules](backups-and-schedules.md) for the general scheduler, and
[Monitoring and alerts](monitoring-and-alerts.md) for where the quiet-hours data comes from.

## Keeping servers alive

### Watchdog (auto-heal)

A container can be up while the game inside it is hung — a plain liveness check misses
that entirely. The **🩺 Watchdog** toggle catches it by health-checking the game through
its own query protocol, which is why the toggle only appears for runes that declare a
`query` block.

Every 20-second tick, each watchdog-enabled `running` server gets a query with a
3-second timeout. **3 consecutive failures** trigger an auto-restart and a
notification; any success resets the streak. After a heal, a **4-minute cooldown**
holds off further checks so the server has room to boot.

Auto-heal knows when to stop. If a server needs **5 heals within 30 minutes**, restarting
clearly isn't fixing it, so Yggdrasil **quarantines** it: auto-heal pauses, and you get
one alert saying so instead of an endless flapping loop. Starting the server manually
clears the quarantine and gives the watchdog a fresh chance.

### Start-failure detection

The watchdog only heals servers Yggdrasil already believes are running. A server whose
container comes up and then exits during startup is a different failure, and it gets its
own handling: Yggdrasil retries, **3 attempts total**, with a **15-second** backoff
between them. A transient blip — a slow image pull, a dependency still coming up — heals
itself and never bothers you; retries are logged, not notified.

When the budget runs out, the server is left stopped and you get exactly **one**
actionable alert, with the crashed container's last **40 log lines** attached (trimmed
to 1500 characters) so you can see why without opening the panel. Manually starting or
stopping the server resets the streak — a fresh intent earns a fresh retry budget.

If you take over during a backoff (start it yourself, or an install begins), the retry
chain bows out and leaves the server to you.

### Autostart

**Start automatically after a reboot** brings a server back up when the panel or host
restarts — but only if it was actually running when things went down. A server you
deliberately stopped stays stopped, and a plain panel restart or deploy is a no-op
because the game containers keep running through it.

## Clone

**⧉ Clone** stands up "another one like this": same rune, variables, resource limits,
host mounts, autostart, port-forward, and watchdog settings, with a fresh set of ports
and an empty data directory. It then installs.

It clones **configuration, not data** — no copying a live multi-gigabyte world. The
identity fields are deliberately left behind too: the clone gets no subdomain, no
BattleMetrics id, and is not published on the status page. Default name is the source
plus `(copy)`.

## Wipe

**🧹 Wipe** resets a server's persistence — loot, bases, progress — as the rune defines
it. The button only appears for runes with a `wipe` block, because only the rune knows
which paths mean "the world" for that game. Config, allowlist, and mods survive.

The order is: safety backup (if ticked), then a graceful stop and container removal,
then deletion of the rune's declared paths, then bring the server back up if it was
running. **The backup gates the wipe** — if it doesn't complete, the wipe aborts and
nothing is deleted. Path deletion is jailed to the data directory; anything resolving
outside it, or to the data directory itself, is refused.

Wipe runs as soon as you confirm. To wipe on a timer, use a scheduled wipe action in
[Backups and schedules](backups-and-schedules.md).

## Resource limits

Per-server **CPU limit (%)** and **RAM limit (MB)** are set at create time and editable
under **Settings** on the server page. `0` means unlimited for either.

The CPU number is a share of **one core**: `100` = one full core, `200` = two, `50` =
half. Note that the live CPU figure on the server page is measured differently — it's a
share of the whole host, matching the dashboard's host-CPU card.

Every container also gets a process cap of 8192 regardless of your settings, so one
runaway server can't fork-bomb the host's PID table and take the panel down with it.

## Console, logs, and RCON

The **Console** tab attaches to the container: you read its output live and type
commands back to the server. It needs the `server.console` permission.

Commands go over **RCON** when the game's rune declares an enabled `rcon` block, and to
the container's stdin otherwise. That split matters: Rust and DayZ read commands over
RCON only, and Bedrock reads them on stdin only, so the console picks the channel the
game actually listens on. Where RCON answers, its reply is printed in the console.

If a game has RCON but the panel can't reach it, the console says so and sends the
command to stdin as a fallback rather than failing outright. The usual cause on
Minecraft is a changed RCON password: `rcon.password` is written into
`server.properties` when the server is installed, so a password edited in the panel
afterwards no longer matches. Fix it in **Config files → server.properties**, or set the
variable back.

When the container isn't running, the console doesn't leave you with a blank "press
Start" — it prints the container's last 300 lines so a crash-on-start (a bad mod, a
missing jar) is right there. Same when the container dies between the check and the
attach.

The separate log stream replays the last 200 lines and then follows live.

### Copying and downloading a log

**Copy** takes what's on screen to the clipboard — the quick move when you want to paste
a few lines into a bug report.

**Download…** goes further back than the tab does. The tab holds what arrived since you
opened it; the download comes from the container itself, so it reaches output from before
you were looking. Pick a range — a line count (200, 2000, everything) or a window (last
15 minutes, hour, 24 hours) — and whether to include timestamps. You get a text file named
after the server and the time you took it.

The ranges are relative to now, and there's deliberately no calendar. Starting or
restarting a server builds a **new container**, and the log belongs to the container — so
it begins at the last (re)start and there is nothing older to fetch. On a server that
auto-restarts every six hours, "everything" is at most six hours.

The **Install log** tab has the same two buttons, without the range: that log is the last
500 lines of the most recent install, held in memory. A panel restart clears it.

Both need `server.view`, the same as reading the log at all.

> A server log can contain passwords and tokens — an RCON password echoed at startup, a
> credential in an install script. Read a log before you attach it to an issue.

**RCON** is available through `POST /api/servers/{id}/rcon` for automation and scripts,
for runes that declare an enabled `rcon` block; it needs `server.console` too. RCON
also powers the **Players** tab — the live roster, kick, broadcast, and lock — for runes
that declare a `players` block. Yggdrasil dials the server's allocated `rcon` port, or
the `game` port for games where the protocol shares it (BattlEye).

See [the API reference](../reference/api.md) for request shapes.

## Files

The **Files** tab is a full file manager over the server's data directory: browse, open
a text file in the editor, save, delete, upload, download.

**Config files** sit at the top, when the rune names any: shortcuts straight to the files
you actually edit. This is where a game's real settings live — the create form asks for a
handful of things, but a Minecraft `server.properties` has around fifty entries, and Rust
keeps its config four directories down. The shortcut skips the hunt.

Recognised formats (`.properties`, `.env`, `.cfg`, `.conf`) open as a form with one field
per setting, with a raw view a click away. A file that isn't there yet — most are written
by the game on first boot — says so instead of erroring.

Which files appear is the rune's decision, via
[`config_files`](../reference/rune-schema.md#config_files). A rune that names none simply
doesn't show the row.

- Drag and drop files — or a whole folder, recursively — anywhere over the listing to
  upload into the current directory.
- Files above 5 MB won't open in the editor. Download them instead.
- Every path is resolved and checked to stay inside the server's data directory,
  including through symlinks. There is no route out of it.
- If a file is owned by root because a SteamCMD install put it there, saving repairs
  ownership through a root container and retries once, rather than throwing a
  permission error at you.

### Config version history

Each time you save a text file, Yggdrasil snapshots the **previous** contents first. Hit
**⏱ History** in the editor to see the saved versions of that file, **Load into editor**
to pull one back, then **Save** to restore it. It keeps the last **10 versions per
file**; binaries and anything over 256 KB are skipped.

That's the undo for "I edited server.properties and now it won't boot".

## Organizing the Servers page

**Notes** live at the top of each server page — free text shared with the whole admin
team, for what the server is for, event schedules, gotchas. Up to 8000 characters, and
editing needs `server.control`.

Tick **Markdown** while editing and the note renders instead: headings, lists, bold,
links, code, tables. It's stored per server and **off by default**, so an existing note
keeps reading exactly as it does now and nobody's asterisks quietly turn into bullets.
Untick it and you get the plain text back, unchanged — the note itself is never rewritten,
only how it's displayed.

Markdown notes are rendered by the panel, not the browser, and raw HTML in a note is
**dropped rather than shown**: a `<script>` becomes nothing, and a link whose target is
`javascript:` loses that target. That's deliberate rather than incidental. A note is writable by anyone
with `server.control` and read by admins, so it crosses a privilege boundary — the
rendering has to be safe by construction, not by whoever happens to be typing.

**Tags** are comma-separated labels set under **Settings** on the server page
("survival, event, staging"). They're normalized to lowercase, deduplicated, and capped
at 20 tags of 30 characters. Tagged servers show their tags as chips on the Servers
page; click a chip — or one in the filter row — to narrow the list to that tag.

**Search** appears once you have more than three servers and matches on name, rune id,
and tags.

**Bulk actions**: tick servers, or **☑ Select all** (which respects an active tag
filter), and Start / Restart / Stop / Back up them together. Yggdrasil only acts where
it makes sense — Start applies to stopped servers, Stop and Restart to running ones —
and reports how many succeeded. Bulk backup asks for one target, and skips servers still
installing. You only ever see selection controls for servers you actually hold
`server.control` on.

## See also

- [Runes](runes.md) — the catalog servers are created from
- [Networking](networking.md) — port forwarding, subdomains, reverse proxies
- [Backups and schedules](backups-and-schedules.md) — backup targets, scheduled restarts and wipes
- [Monitoring and alerts](monitoring-and-alerts.md) — notifications, resource alarms, the status page
- [API reference](../reference/api.md)
- [Rune schema](../reference/rune-schema.md) — the fields quoted above (`done_regex`, `stop_timeout`, `wipe`, …)
