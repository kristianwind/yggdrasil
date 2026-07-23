# Monitoring and alerts

What Yggdrasil Panel measures, what it keeps, and what it will wake you up about. Read this before
you set your first alarm threshold, and again when you want to know why a number looks the way it
does.

## Live CPU and memory

A running server's page shows current CPU and memory read straight from Docker
(`GET /api/servers/{id}/stats`): CPU percent, memory used, and the container's memory limit.

The CPU number needs one sentence of explanation, because it is not the number `docker stats` prints.
Yggdrasil reports a server's CPU as **a share of the whole host, from 0 to 100**, and caps it at 100.
Docker's usual formula multiplies by the core count, so a container burning 1.2 of your 8 cores reads
`120%`. On a per-server gauge that looks alarming and means nothing. Yggdrasil divides the container's
CPU delta by the system-wide delta, which already spans every core, so the same container reads `15%`
— fifteen percent of the machine. That is the same scale as the host CPU figure on the dashboard, so
the two can be compared directly, and a server's CPU can never exceed 100.

The consequence for thresholds: a CPU alarm at `80` means "this one server is using 80% of the entire
host", not "80% of one core". Set them accordingly.

## Metrics history

A sampler runs every **5 minutes**. For each running server it records one row — CPU percent, memory
in MB, and player count — into the metrics table. Player count comes from the rune's query protocol,
polled on `127.0.0.1`; a game with no query, or one that doesn't answer, records `-1`.

Samples are pruned to a rolling **7-day** window, swept roughly hourly.

`GET /api/servers/{id}/metrics` returns the samples for the last N hours — default 24, maximum 168
(7 days). The server page's **History** section draws the three series as sparklines with 24h / 3d /
7d range buttons. Every sample also feeds the resource alarms below.

## Quiet hours

`GET /api/servers/{id}/quiet-hours` mines the sampled player counts to answer "when is nobody
playing?". It takes the last **14 days** of samples, ignores the `-1` no-data rows, buckets the rest
by **server-local hour** (0–23), and returns the average players per hour along with the quietest
hour and its average.

The server page shows the result as a hint next to auto-restart — "Quietest around 05:00 (avg 0.4
players, last 14 days)" — with a **Start there** link that anchors auto-restart to that hour. So the
recommendation is actionable, not just informative: you schedule a disruptive job on evidence rather
than a guess. See [auto-restart](servers.md#auto-restart).

The bucketing uses the panel host's local time, and so does the scheduler, so the hour the hint names
is the hour a restart anchored there actually fires.

When there is no usable data — a brand-new server, or a game with no query protocol — the response
says `has_data: false` and the UI stays silent rather than recommending an hour it invented.

Auto-restart itself is a per-server "restart every N hours" toggle backed by an ordinary managed
schedule row, so the restart runs through the same machinery as any other scheduled job, including
the rune's countdown warnings and an optional backup first. See
[Backups and schedules](backups-and-schedules.md).

## Resource alarms

Each server has three optional thresholds, all **off until you set them** (zero or blank disables):

| Threshold | Field | Unit | Measured |
| --- | --- | --- | --- |
| CPU | `cpu_alarm_pct` | percent of the whole host | every metrics sample (5 min) |
| Memory | `mem_alarm_mb` | megabytes | every metrics sample (5 min) |
| Disk | `disk_alarm_mb` | megabytes of the data directory | hourly |

All three are **edge-triggered**. Crossing into breach fires exactly one notification. Nothing
repeats while the alarm stays up. When the metric recovers, you get one all-clear. There is no
reminder loop and no alert spam, which means an alarm you dismiss is an alarm you will not hear about
again until it clears and re-fires.

CPU and memory require a **sustained** breach: the value has to sit at or above the threshold for
**2 consecutive samples** before the alarm fires. With a 5-minute sampler, that is roughly 10 minutes
— long enough to ignore a one-off spike during world generation, short enough to catch a real leak.
A single sample below the threshold resets the streak. The notification text spells the arithmetic
out, for example `⚠️ Skyrealm CPU high: 91% (≥ 85% for ~10 min)`.

Disk is different in two ways. It's checked on its own **hourly** timer, because measuring means
walking the whole data directory, and the first sweep waits 2 minutes after the panel starts so boot
isn't competing with a tree walk. It also fires on a **single** measurement at or above the threshold
— no sustained-breach requirement, since a directory doesn't spike.

### What clears when a server stops

CPU and memory alarm state is **dropped when a server stops**. A fresh run starts from zero rather
than staying latched on a value from the last one.

Disk alarm state **persists**. This is deliberate and it is the behaviour that surprises people: a
stopped server's data directory still occupies exactly as much disk as it did while running. Worlds,
in-place backups and logs don't shrink because you pressed Stop. So stopping a server does not silence
its disk alarm and does not produce an all-clear — you get the all-clear when the directory actually
falls back under the threshold, which means deleting something.

Alarms go out through your configured notification channels — see [Notifications](notifications.md).

## Watchdog

The **watchdog** is per-server auto-heal, off by default, and available only on runes that declare a
query protocol (there's nothing to health-check otherwise, so the UI gates the toggle).

The reconciler's 20-second tick health-checks every watchdog-enabled running server by speaking its
query protocol on `127.0.0.1` with a 3-second timeout. This catches a specific and nasty failure: the
container is up, Docker is happy, and the game inside is hung. A container-liveness check sees nothing
wrong; the watchdog sees no answer.

- **3 consecutive failed checks** trigger a heal. Yggdrasil recreates and restarts the server and
  notifies you before and after.
- After a heal, a **4-minute cooldown** suspends checks so the server has room to boot without being
  restarted again mid-start.
- Any successful check resets the failure streak.

If a server needs **5 heals within 30 minutes**, it is crash-looping and restarting it is not helping.
The watchdog **quarantines** it: auto-heal pauses, you get one alert saying so, and Yggdrasil leaves
it alone. Starting the server manually clears the quarantine and gives it a fresh chance. Turning the
toggle off, or stopping or deleting the server, also clears its state.

## Start-failure detection

The watchdog only heals servers the panel already believes are *running*. A container that comes up
and whose game process crashes straight back out is a different failure, and it has its own handling.

When a start attempt fails — the container exits before it ever signals readiness — Yggdrasil grabs
the container's log tail immediately, while the crashed container still exists, then retries. There
are **3 total attempts** (the original plus two automatic retries), each after a **15-second** backoff
so a slow image pull or a dependency still coming up can settle. Retries are logged, not notified,
which keeps a transient blip quiet.

After the third failure Yggdrasil gives up, leaves the server stopped, and sends **one** actionable
alert with the last **40 log lines** attached (trimmed to 1500 characters so the notification stays
sendable). That is the whole point of the feature: the alert tells you *why*, not just *that*.

The failure count resets the moment a start succeeds, or whenever you start or stop the server
yourself — a fresh intent gets a fresh retry budget. If you take over during a backoff, the retry
stands down.

Two adjacent cases are covered by the same machinery:

- **Slow start.** A server that is still `starting` after **5 minutes** without crashing gets one
  heads-up with its latest log tail, then Yggdrasil keeps waiting. The readiness window is 10 minutes;
  a server still up at the end of it is marked running regardless.
- **Stalled start across a panel restart.** If the panel restarts while a server is `starting`,
  Yggdrasil re-attaches to it. A container that has been up well past the readiness window and never
  signalled ready — the game process died but its wrapper stayed alive — is marked stopped, and you
  get an alert with the log tail.

## Kvasir Watchers

A **watcher** is a log rule: *if this pattern matches at least N lines within the last W seconds of
a server's log, act*. It reads the container's own stdout/stderr, so the same mechanism covers a
game server's crash spam, a WordPress failed-login burst, a database's error storm or an HTTP 5xx
spike. Watchers live under **Settings → Kvasir Watchers** (admin only).

- Every running server is scanned about every **30 seconds**; a watcher fires at most **once per
  10 minutes** so a sustained condition doesn't flood you.
- A watcher is scoped to **one server or to every server**.
- Action **Notify** sends the matched lines to your notification channels. Action **Notify +
  Kvasir** also hands them to the AI to explain what's happening and propose a fix — it needs a
  configured provider and proactive monitoring on (see the Kvasir guide).

You rarely have to start from nothing:

- **Runes ship defaults.** A rune can declare `watchers:` — the author's knowledge of what its log
  looks like when things go wrong. They're seeded per server at create and (re)install as ordinary
  rules (marked <em>ᚱ rune</em> in the list) that you can edit, disable or delete. Your changes
  stick across reinstalls; deleting one and reinstalling restores the default.
- **Kvasir can suggest rules.** Pick a server under Settings → Kvasir Watchers and press
  **Suggest**: the AI reads the server's app type and a sample of its recent log and proposes up to
  five rules, each with its reasoning. Every proposal is validated (the pattern must be a working
  regex, bounds are clamped) and nothing is created until you add it.

Patterns are Go/RE2 regular expressions matched per line. Prefer patterns anchored in how *your*
log actually formats trouble — the suggestion flow exists precisely because a generic pattern
either misses or matches routine lines.

## Host system info

`GET /api/system/info` backs the dashboard's host panel (admin only):

- Docker reachability
- server count, running count, user count, rune count
- Go version, architecture, CPU count
- host CPU percent, sampled from `/proc/stat` over a 150 ms window (`-1` when unavailable, such as on
  a non-Linux dev box)
- total and used RAM, read from `/proc/meminfo` (`MemTotal` minus `MemAvailable`); zero on a host
  without `/proc`, and the dashboard omits the card
- free and total bytes on the filesystem holding the Yggdrasil data directory

### Low-disk alert

Separately from the per-server disk alarms, Yggdrasil watches free space on the data filesystem every
**5 minutes**. When free space drops **below 10%** it sends one notification and stops. It re-arms
once free space recovers to **15% or more**, so a filesystem hovering at the line doesn't produce a
stream of alerts.

## Updates

`GET /api/version` reports the running build, the latest GitHub release tag, whether an update is
available, and whether the panel can update itself. The GitHub release lookup is cached for **6 hours**
so the version endpoint — hit on every page load — doesn't hammer the API. **Check now**
(`POST /api/system/check-update`) invalidates that cache and refetches, so a just-published release
shows up immediately.

Self-update works only for a released `vX.Y.Z` build with the updater installed by `install.sh`. The
panel runs sandboxed and cannot escalate its own privileges: instead it writes the target tag to a
request file in its state directory, and a systemd path unit picks that up and runs a root oneshot
updater. If the helper isn't present, the panel says so and points you at the manual steps rather than
failing obscurely. `GET /api/system/update-status` surfaces the last result the helper wrote, which is
where a checksum or download failure shows up after a restart poll times out.

**Auto-update** is opt-in, with a chosen hour (default **4**, server-local). A loop wakes every 20
minutes and acts at most once a day: it runs at the first check at or after your chosen hour that
hasn't run today. The catch-up window matters — gating on the exact hour meant a panel that was
rebooting, asleep or offline during that one hour silently skipped the whole day. A self-update
restarts only the panel binary; your game and app containers keep running, so the impact is a few
seconds of UI downtime.

## Host operating system

**Settings → System → Operating system** reports what the host has pending: how many updates, how
many of those are security, and whether something has asked for a reboot.

The numbers come from the host's own tooling, so they agree with what the box tells you when you log
into it over SSH — `apt-check` where it exists (Ubuntu's, the one behind the login banner), falling
back to counting `apt list --upgradable`. That fallback has no security breakdown, and the card says
so rather than showing a zero it can't stand behind: Ubuntu publishes security updates into both
`-security` and `-updates`, so counting suites would under-report them.

If apt's package list hasn't been refreshed in a couple of days the card says that too. A count of
zero from a stale list means nothing has been *checked*, not that nothing is pending.

**Yggdrasil does not install them.** That's deliberate, and worth understanding:

- Upgrading Docker restarts `docker.service`, which stops **every running server**. As a side effect
  of "update the OS", with no warning, that's the most disruptive thing the panel could do.
- A kernel update needs a reboot, which is a decision about your players, not a button.
- `unattended-upgrades` already does unattended patching properly, and it ships with the distro.

So the panel tells you, and you choose when:

```bash
sudo apt update && sudo apt upgrade
```

Pick a quiet hour — a server's **Auto-restart** dialog names its calmest one, mined from real player
counts. See [auto-restart](servers.md#auto-restart).

This exists because [the security policy](../../SECURITY.md) says the fix for the open Docker
advisories is keeping Docker updated on the host. Until now nothing in the panel would tell you
whether you had.

## See also

- [Servers](servers.md)
- [Notifications](notifications.md)
- [Backups and schedules](backups-and-schedules.md)
- [Networking and public access](networking.md)
- [Status page and beacon](status-page-and-beacon.md)
- [API reference](../reference/api.md)
