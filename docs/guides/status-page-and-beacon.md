# Status page, Discord board, and beacon

Three ways Yggdrasil Panel talks to the outside world: a public up/down page for your players, a
self-updating status embed in Discord, and a voluntary install ping to the project. All three are off
until you turn them on.

## The public status page

`/status` is a read-only board showing whether your servers are online. No login, no panel account —
the thing players ask about most, answered without giving them access to anything.

### Double opt-in

Nothing is exposed until **both** switches are on:

1. The master switch, **Settings → Network → Status page → Enable the public status page**
   (the `status_page_enabled` setting).
2. **Show on the public status page** on each individual server's Settings tab (the per-server
   `status_public` flag, off by default).

With the master switch off, both `/status` and `/api/status` return **404** — not an empty page, not
"disabled", a plain not-found, so the panel's existence isn't advertised to anyone scanning. With the
master switch on but no server opted in, the page loads and says nothing is being shared.

Neither switch implies the other. Enabling the page shares no servers; opting a server in while the
page is off shares nothing.

### Exactly what it exposes

Per opted-in server, the JSON at `/api/status` carries four fields and no more:

| Field | Contents |
| --- | --- |
| `name` | the server's display name |
| `game` | the rune's name, omitted when empty |
| `status` | `online`, `starting`, or `offline` |
| `players` | current player count, omitted when unknown |

The response also carries the page `title` (from **Page title**, defaulting to "Server Status") and
an `updated_at` timestamp.

There is no id, no port, no address, no environment, no rune configuration, no operational detail.
`status` is `online` only for an installed server the panel has running, `starting` for one mid-start,
and `offline` for everything else, so a server you have opted in but never installed reads offline
rather than leaking that it exists in a half-built state.

### Player counts come from the sampler

The `players` value is **not** a live query. It is the most recent count the metrics sampler already
recorded, and only when that sample is **less than 15 minutes old** — older than that, or no sample at
all (a game with no query protocol), and the field is omitted entirely rather than showing a stale or
invented number.

That means rendering the page never touches a game server, no matter how many people load it. The
sampler runs every 5 minutes, so a count can be up to five minutes behind reality. See
[Monitoring and alerts](monitoring-and-alerts.md) for the sampler.

On top of that, the assembled JSON is cached for **15 seconds** and served with
`Cache-Control: public, max-age=15` — a cheap abuse guard on an unauthenticated endpoint. Changing the
master switch or the title drops the cache immediately, so those edits show up without waiting out the
TTL. A server's own opt-in does not: it writes the flag and leaves the cache alone, so adding a server
to the board — or taking one off it — can take up to 15 seconds to land. A server you just un-shared
can still appear on `/status` until the TTL expires.

### The `/status.js` split

The page itself is a single self-contained HTML document with no framework and no external assets. Its
JavaScript is served separately from `/status.js` rather than inlined, because the panel sends a
strict Content-Security-Policy with `script-src 'self'` — an inline `<script>` would be blocked by the
browser. Both `/status` and `/status.js` 404 when the master switch is off.

The script fetches `/api/status` and re-renders every 30 seconds. The page carries
`<meta name="robots" content="noindex">`.

## The Discord status board

The same set of servers, as a live embed in a Discord channel. It needs **no bot to host** — an
incoming webhook is enough.

Create the webhook in Discord under **Server Settings → Integrations → Webhooks → New Webhook**, pick
a channel, copy the URL, and paste it into **Settings → Integrations → Discord status board**. The URL
must start with `https://` and is **encrypted at rest**, like every other secret the panel stores.

The board shows the servers with `status_public` on — the exact set the `/status` page shows — so a
server you share appears in both places. The per-server opt-in governs both; the status page's master
switch does not gate Discord.

### One message, edited in place

Yggdrasil posts the embed once, records the message id, and from then on **edits that same message**.
Your channel gets one status board, not a running log of them.

If the edit comes back **404** — someone deleted the message in Discord — Yggdrasil clears the stored
id and posts a fresh one, which becomes the new board. Any other error is reported rather than
silently reposting. Changing or clearing the webhook also clears the stored id, so the next refresh
starts a new message in the new channel.

The board refreshes every **3 minutes**. Saving the settings pushes an immediate update, and **Post
now** forces a refresh on demand and surfaces any error rather than hiding it in the log.

### What the embed looks like

Each server becomes one inline field: a 🟢 / 🟡 / 🔴 dot with the server name, and the game plus its
player count, `starting…`, or `offline`. The embed's description is `**N of M online**`, and its colour
is green when everything shared is up, amber when some are, grey when none are.

Discord caps an embed at **25 fields**, so if you share more than 25 servers the board lists the first
25 by name and says so in the footer — "showing 25 of 30". The rest are on `/status`. The description
and the colour both count every shared server regardless of the cap, so a fleet of 30 that's fully up
still reads `**30 of 30 online**` in green.

## The beacon

Yggdrasil Panel ships **no telemetry**. Nothing phones home, nothing tracks you, and the download
count on GitHub is the only number the project gets for free. The beacon is the single deliberate
exception, and it is **off by default**.

When you switch it on under **Settings → System → Beacon**, the panel sends one small ping a day so
the project can gauge how many installs exist.

### The literal payload

```json
{ "instance_id": "…", "version": "…" }
```

That is the whole thing. Two fields. `instance_id` is a random UUID generated on first use and stored
locally — it identifies nothing about you, it exists only so the collector can de-duplicate repeat
pings into a unique-install count. `version` is the panel build.

No IP address is stored. No server names, no addresses, no player counts, no configuration, no usage
data. The settings page shows you the exact payload your install would send, filled in with your real
values, so there is nothing to take on trust.

### How it behaves

The loop wakes 30 seconds after startup and every 30 minutes after that, but sends at most **one ping
per calendar day (UTC)**, and only records the day as done when the collector actually accepts it. A
panel that isn't up around the clock still pings roughly daily. Opting in sends a ping right away
rather than waiting. The settings page shows the date of the last successful ping.

### Collector URL and Test

The ping goes to `https://beacon.yggdrasilpanel.com/api/beacon` unless you change the **Collector
URL** setting. Any value must start with `http://` or `https://`; clearing it restores the default.

**Test** sends a one-off ping to whatever URL is in the box — not the saved one — and tells you
whether it was accepted, so you can check a collector before committing to it. A 404 gets a specific
message, because that is what an unrouted or disabled collector looks like.

### Running your own collector

The same binary can be the collector. Turn on the `beacon_receiver_enabled` setting on the instance
that should gather counts (`PUT /api/settings/beacon` with `receiver_enabled`, admin only), and point
other installs' Collector URL at its `/api/beacon`. The receiver is off by default and, while off, the
endpoint returns **404** so it isn't advertised.

A receiving instance upserts by instance id — the first ping inserts, repeats bump `last_seen` and the
ping count and refresh the version — and stores no IP or other request metadata. When an instance is
acting as a collector, its Beacon settings card shows the tallies: total instances ever seen, active in
the last 7 and 30 days, and a version breakdown across the last 30 days (`GET /api/beacon/stats`,
admin only).

## See also

- [Servers](servers.md)
- [Monitoring and alerts](monitoring-and-alerts.md)
- [Notifications](notifications.md)
- [Networking and public access](networking.md)
- [API reference](../reference/api.md)
