# Runes: the server catalog

A rune tells Yggdrasil Panel how to run one game or app. Everything you can deploy comes
from one, so this is where you look when the game you want isn't in the list yet. For
authoring your own, see the [rune schema](../reference/rune-schema.md).

## What a rune is

A rune is a single declarative YAML file: which Docker image to use, how to install the
game, the command that starts it, the ports it needs, the variables an operator gets to
fill in, and the optional extras that light up features in the panel — a query protocol,
RCON, restart warnings, a wipe definition, a players list, an admin log.

The model exists so that adding a game is a *data* change, not a code change. Yggdrasil
has no per-game logic compiled into it. When you create a server, the panel reads that
YAML and nothing else: it templates the image name and command with your variable
values, allocates the ports the rune declares, and runs the install script in a
throwaway container. When you press **Wipe**, the paths it deletes come from the rune.
When the watchdog health-checks a server, it uses the query protocol the rune named.
Which is also why a rune with no `query` block gets no watchdog toggle, and one with no
`wipe` block gets no Wipe button — the panel offers exactly what the rune declares.

The code and the API call this a `gameskill`. You'll meet that spelling in API paths
(`/api/gameskills`), in the YAML's top-level `gameskill:` key, and in the `gameskill_id`
field on a server. In the panel it's a rune.

Runes are versioned by an integer `version` field, shown in the list. It's metadata for
you, not a dependency system.

## Keeping runes up to date

A rune declares a `version`. It's the rune author's number, bumped when the file changes — and for
a rune you installed from the catalog it's the only signal that your copy has drifted from its
source.

The **Runes** page compares the two. A rune the catalog has moved past shows a badge next to its
version — `v1 ↑ v2` — and clicking it re-imports the newer file. Admin-only, like everything else
that touches a rune.

Built-in runes never show one: they're embedded in the panel binary and re-seeded on every boot, so
they move when the panel does.

The check matches by rune **id** against the community catalog. Runes carry no record of where they
were installed from, so a rune you wrote yourself, or took from another repo, simply isn't reported
rather than being compared against something it isn't. If GitHub can't be reached the page stays
quiet rather than claiming everything is current.

**What updating a rune changes.** It replaces the definition, not your servers. Existing servers keep
their own names, variables, ports and data. But a server is *built* from its rune, so the new
definition — image, startup command, ports — applies the next time that server is started, restarted
or reinstalled. If a rune changed its image, that's when you'll get it.

## Built-in runes

Five runes are embedded in the Yggdrasil binary and seeded into the catalog on every
boot:

| Rune | id | Category |
|------|----|----------|
| Minecraft (Java) | `minecraft-java` | Minecraft |
| Minecraft (Bedrock) | `minecraft-bedrock` | Minecraft |
| Uptime Kuma | `uptime-kuma` | Apps |
| Vaultwarden | `vaultwarden` | Apps |
| Cloudflare Tunnel (cloudflared) | `cloudflared` | Apps |

They carry a **built-in** badge in the panel. Seeding is an upsert, so an upgraded
Yggdrasil ships you the newer definitions without disturbing the servers already built
from them.

Everything else — including DayZ and Rust — lives in the community catalog and is one
click away.

## The community catalog

The [`community-runes/`](../../community-runes/) directory in this repository holds runes
that aren't bundled, so the default set stays lean. They're grouped into three folders,
and the panel's GitHub browser descends into all of them automatically.

`databases/` — backing stores for the app runes:
`mariadb`, `mongodb`, `postgresql`.

`games/`:
`dayz`, `factorio`, `genshin-impact`, `luanti`, `rust`, `terraria`.

`apps/`:
`adminer`, `cyberchef`, `excalidraw`, `freshrss`, `gitea`, `grafana`, `headplane`,
`headscale`, `hermes-agent`, `homepage`, `it-tools`, `jellyfin`, `linkding`, `mealie`,
`memos`, `n8n`, `nextcloud`, `nginx-proxy-manager`, `phpmyadmin`, `pihole`, `portainer`,
`static-site`, `stirling-pdf`, `wordpress`.

These are community-maintained and provided as-is. The app runes run their images much
like a plain `docker run`, so you may need to tune ports or variables for your setup, and
some have prerequisites — WordPress and Nextcloud want a MariaDB rune to point at; the
catalog's own [README](../../community-runes/README.md) has the per-rune notes.

## Adding a rune

Four routes, all on the **Runes** page.

### Browse GitHub

**Runes → Browse GitHub** lists the YAML files in a GitHub repository folder and installs
any of them with one click. It defaults to this project's `community-runes` folder on
`main`, but the repo and path are editable — point it at your own fork or a private
collection of runes and it works the same.

The browser reads each file's id, name, category, and description, flags the ones you
already have installed, and lets you filter the list. It descends one level into
subfolders, which is how `databases/`, `games/`, and `apps/` show up together. Listings
are cached for 10 minutes; **Reload** forces a refresh.

Installing an already-installed rune **updates it** to the repository's latest version.
Existing servers built from that rune pick the change up the next time their container is
recreated — a restart is enough, no recreating the server.

Fetches are restricted to GitHub's own hosts, so this endpoint can't be turned into a
fetch-anything proxy, and each file is capped at 512 KB.

### Upload a YAML

**Carve a rune (upload)** takes a `.yaml` file directly — the same thing the GitHub
browser does, when you already have the file. Also capped at 512 KB.

### Import a Pterodactyl egg

**Import egg** converts a Pterodactyl egg `.json` into a rune, mapping the subset that
translates cleanly: image, startup command, readiness signal, install script, and
variables. That makes the large existing egg library reusable. Up to 1 MB. Expect to
review the result — an egg's conventions don't line up with Yggdrasil's on every field.

### Import XML

**Import XML** takes a rune expressed as XML, with elements mirroring the YAML keys. Up
to 1 MB.

Whichever route you use, the YAML is parsed and validated before it's stored. An invalid
rune is rejected with the reason, not saved and left to fail at runtime.

## Rune management is admin-only

Listing runes and reading one is open to anyone who can log in. **Every route that adds,
imports, or deletes a rune requires an admin.** This is deliberate, and it's a security
boundary rather than a formality.

A rune fully controls the Docker runtime: the image, the command, the user it runs as,
Linux capabilities, device access, and mount targets. Anyone who can install a rune can
therefore choose what code runs on your host and with which privileges — a
privilege-escalation path straight out of the panel. So a delegate can create servers
from the runes you've approved, and cannot add new ones.

Runes are still only semi-trusted after that, so the parser enforces allowlists on the
privilege-bearing fields at import *and* at load time. Capabilities are limited to
`NET_ADMIN`, `NET_RAW`, `NET_BIND_SERVICE`, and `SYS_NICE`; devices to `/dev/net/tun`,
`/dev/dri`, and `/dev/fuse`; sysctls to the three forwarding-related keys a VPN rune
needs. Extra mount targets that would shadow system paths are refused. A rune cannot
bind a host path at all — host mounts are set per server, by an admin, in the panel.

### The built-in overwrite guard

No import path may overwrite a built-in rune. Upload, egg, XML, and GitHub install all
refuse with a conflict if the incoming id matches a rune marked built-in — otherwise
anyone with admin could backdoor a definition that running servers already depend on.
Use a different id if you want your own variant.

## Deleting runes

Any rune can be deleted, including a built-in one — but not while servers still use it.
Yggdrasil refuses and tells you how many are in the way; delete those servers first, and
the rune goes.

Deleting a built-in rune sticks. Yggdrasil records the deletion, and the boot-time seeder
respects it rather than re-adding the rune on the next restart.

## What the seeder does on boot

Beyond upserting the embedded runes, the seeder prunes. Any rune still marked built-in in
your database that is no longer shipped with this version gets cleaned up — this is what
makes slimming the bundled set actually take effect:

- **not used by any server** → removed entirely. It's still installable from the
  community catalog.
- **still used by a server** → demoted to a normal community rune (it loses the built-in
  badge) so your servers keep working. The prune only ever considers runes that are still
  marked built-in, so a demoted one is never picked up again — deleting it once those
  servers are gone is a manual job.

Nothing is ever orphaned, and nothing you deleted comes back.

## Editing a rune's YAML does not change the panel's copy

The one that catches people out.

When a rune is imported, Yggdrasil stores the YAML **in its database**. That stored copy
is the only thing servers ever read. Editing the file you uploaded, or the file in a
checkout of `community-runes/`, changes nothing at all — there's no watched directory
and no reload.

To apply a change: **re-import the rune**. Upload the edited YAML again, or reinstall it
from **Browse GitHub** after pushing. Either overwrites the stored copy in place, keeping
the same id, so existing servers stay attached to it. They then pick up the new
definition the next time their container is recreated — press **Restart**, which rebuilds
the container from the rune as it is right now. Changes to the install script need
**Update / Reinstall** instead, since that's what re-runs it.

## See also

- [Managing servers](servers.md) — creating servers from runes, and the features runes light up
- [Rune schema](../reference/rune-schema.md) — every field, for writing your own
- [Networking](networking.md) — ports, subdomains, and reverse proxies for app runes
- [Backups and schedules](backups-and-schedules.md)
- [Monitoring and alerts](monitoring-and-alerts.md)
- [API reference](../reference/api.md) — the `/api/gameskills` routes
