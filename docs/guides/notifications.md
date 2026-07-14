# Notifications

Yggdrasil pushes events — backups, crashes, alarms, auto-bans — to Telegram, Discord, a generic
webhook, or email. This covers the channel types, how to add and test one, and exactly which events
produce a message.

## Channels

A channel is a global admin setting, not a per-server one: every enabled channel receives every
event the panel emits. Add them under **Settings → Integrations → Notifications → + Add channel**.
Credentials are encrypted at rest and are never returned by the API — the channel list shows only
the type and whether it is enabled.

| Type | What you provide | How it delivers |
| --- | --- | --- |
| `telegram` | Bot token, chat ID | `sendMessage` to the Telegram bot API |
| `discord` | Webhook URL | `POST {"content": "…"}` |
| `webhook` | Any URL | `POST {"text": "…"}` |
| `email` | SMTP host, port, username, password, from, to | `smtp.SendMail`, subject `Yggdrasil notification` |

For email, the port defaults to `587` when you leave it blank, and PLAIN auth is used when you set a
username (leave it empty for a relay that doesn't authenticate). One `from` and one `to` per
channel — add a second channel if you want a second recipient.

Telegram, Discord and webhook sends get a 10-second timeout. Email does not: `smtp.SendMail` carries
no deadline, so an SMTP server that accepts the connection and then stalls holds that send open.
Sends happen in the background, so a dead channel never blocks a backup or a restart; failures are
written to the panel log rather than retried.

### Testing

**Test** on a channel sends "🌳 Yggdrasil test notification — channels are working." through the
stored config and reports the transport error verbatim if it fails. Do this once when you add a
channel — a wrong Telegram chat ID or an expired Discord webhook looks identical to "no events
happened" otherwise.

## What actually notifies

### Backups

- Backup finished, with the archive size.
- Backup failed, with the reason (server not found, target config, connect, upload).

Both fire for every backup, whether you started it by hand or a schedule did.

### Server lifecycle

- A server started, stopped, or restarted **from the panel or the API**.
- A server was wiped.

These come from the manual endpoints. A restart that a *schedule* performs does not send its own
message — see the schedule summary below.

### Watchdog and start failures

The watchdog (per-server, off unless you enable it) health-checks running servers whose rune
declares a query, and notifies on each step:

- The server stopped responding — the message carries the number of failed checks — and is being
  auto-restarted.
- The auto-restart itself failed.
- The auto-restart succeeded.
- Auto-heal gave up: five heals inside 30 minutes puts the server in quarantine, auto-heal pauses,
  and you get one message telling you to fix it and start it manually.

Separately, the start watchdog watches a server that was just started:

- It is taking longer than usual — still not ready after 5 minutes. Sent at most once per start
  attempt, with the latest log lines attached.
- It was still "starting" when the panel restarted and never became ready; it has been marked
  stopped. Includes the last log lines.
- It failed to start after 3 attempts and has been left stopped. Includes the last log lines.

### Resource and disk alarms

Per-server CPU and memory alarms fire only when you set a threshold above zero on that server. The
metrics sampler runs every 5 minutes, and an alarm needs two consecutive over-threshold samples
(roughly 10 minutes) before it speaks.

The per-server disk alarm compares the data directory's size against its threshold. Both alarms send
an all-clear message when the value comes back under the threshold.

The host also has a low-disk monitor that is always on: when the filesystem holding the Yggdrasil
database drops below 10% free, you get one message.

### Auto-moderation

When a violation rule crosses its threshold, Yggdrasil notifies the action it took: an auto-kick
(with the rule name and server) or an auto-ban.

### Scheduled runs

Each firing of a schedule produces one summary: how many servers succeeded, were skipped, and
failed, with the names under each heading. The icon reflects the worst outcome — ✅ if everything
worked, ❌ if anything failed, ⏰ if nothing ran.

### Nightly backup verification

When backup verification is enabled, the nightly pass notifies only when it finds a corrupt latest
backup, listing the affected servers. A transport problem reaching the target is not a corruption
verdict and is skipped quietly. A clean run says nothing.

### Daily AI digest

With Kvasir and its daily digest both switched on, the ops briefing arrives once a day at your
chosen hour. See [Kvasir](kvasir-ai.md).

## Which events are gated

Nothing per-event is toggleable — a channel gets everything the panel emits. What varies is whether
the *source* of the event is switched on for that server.

Always on once you have a channel:

- Backup done and failed
- Manual start / stop / restart / wipe
- Slow start, stalled start, and start-gave-up alerts
- Schedule run summaries
- Host low disk (under 10% free)

Only when you've configured the feature:

- Watchdog heal, heal-failed, and quarantine — per-server watchdog toggle
- CPU and memory alarms and all-clears — per-server threshold above 0
- Disk alarm and all-clear — per-server threshold above 0
- Auto-kick / auto-ban — you have a violation rule with that action
- Backup-verify failures — the nightly verification setting
- Daily ops digest — Kvasir enabled and the digest toggle on

## The anti-noise design

The volume is kept down on purpose, and it's worth knowing the rules so silence doesn't read as
breakage:

- **One message per schedule firing**, not one per server. Only state-changing schedule actions —
  start, stop, restart, update — produce it. Scheduled backups are excluded because a backup already
  emits its own ✅/❌, and scheduled message and command actions are excluded because they only ever
  act in-game, where the players already saw them.
- **A scheduled restart does not double-notify.** The individual "restarted" message comes from the
  manual restart endpoint only; a scheduled one is represented in the summary.
- **Alarms are edge-triggered.** An alarm fires once when it crosses, then stays quiet no matter how
  many samples stay over the threshold, and sends exactly one all-clear when it drops back. You get
  two messages per episode, not one every 5 minutes. The same applies to the host low-disk monitor,
  which re-arms only once free space recovers to 15%.
- **CPU and memory alarm state resets when a server stops**, so its next run is evaluated fresh.
  Disk alarm state deliberately persists: a stopped server's data directory still occupies the disk.
- **The start watchdog says "taking a long time" at most once per start attempt**, and the
  "gave up" alert is a single message after the retry budget is spent, not one per attempt.
- **Backup verification only speaks on bad news.**

## The Discord status board is a different thing

A Discord notification channel posts a new message for every event. The **Discord status board**
(**Settings → Integrations → Discord status board**) is separate: it keeps one embed in a channel
and edits it in place every 3 minutes with the up/down state and player counts of the servers you've
shared publicly. It's a live board for your community, not an event feed, and it has its own webhook
URL and enable switch. See [Status page and beacon](status-page-and-beacon.md).

## See also

- [Monitoring and alerts](monitoring-and-alerts.md) — where thresholds, the watchdog and verification are configured
- [Kvasir](kvasir-ai.md) — the daily ops digest
- [Status page and beacon](status-page-and-beacon.md) — the Discord status board
- [API reference](../reference/api.md)
