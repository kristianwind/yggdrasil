# Migrating a panel to another host

Moving a whole Yggdrasil panel — every server, its data, its runes and its
settings — from one machine to another. Think "the old box is dying" or "I set up
a fresh Ubuntu server and want everything over there."

This is a **whole-panel** move. Moving a single server between two panels that
both keep running is a different job — see [Exporting one server](#exporting-one-server).

## What actually makes up a panel

Almost everything lives in one place and one file:

- **The database** (`/var/lib/yggdrasil/yggdrasil.db`) — your servers, their
  variables, ports and resource limits, **your runes** (community runes are
  stored here, not just built-in ones), backup targets, users and permissions.
- **The server data directories** (`/var/lib/yggdrasil/servers/<id>/`) — worlds,
  configs, plugins and mods.
- **The `secret_key`** in your config (`/etc/yggdrasil/config.yaml`). This one is
  easy to forget and **critical**: variable secrets like RCON passwords and API
  keys are encrypted with it. Move the database without the key and those secrets
  are unrecoverable on the new host.

Docker images aren't part of the move — the new host re-pulls them on first
start.

## The easy way: `migrate export` / `migrate import`

The binary bundles all three into a single file for you.

**On the old host:**

```bash
sudo systemctl stop yggdrasil          # so nothing is mid-write
sudo yggdrasil migrate export -o ygg-migration.tar.gz
```

The bundle contains the database snapshot, every server's data directory, and the
`secret_key`. **It is a full-panel credential** — it holds password hashes and
every encrypted secret. Treat it like a private key and transfer it over
something you trust (`scp`, a USB drive), not a public share.

**On the new host** — install Yggdrasil the usual way first, then:

```bash
sudo systemctl stop yggdrasil
sudo yggdrasil migrate import ygg-migration.tar.gz
```

Import writes the database, restores each server's data directory to the same
path it had before, and then prints the original `secret_key`. Put that key into
the new host's `/etc/yggdrasil/config.yaml`:

```yaml
auth:
  secret_key: "…the key it printed…"
```

Then start it:

```bash
sudo systemctl start yggdrasil
```

The panel reads the database, re-pulls images, and recreates the containers. Your
servers, runes, backups, users and settings are all there.

> **Same layout on both hosts.** Import restores data directories to the absolute
> paths recorded in the database (`/var/lib/yggdrasil/servers/<id>/`). Use the
> default install layout on the new host — which a normal install already does —
> and the paths line up. A server that had no data directory yet (never installed)
> is skipped on export; its configuration still moves with the database.

## The manual way: `rsync`

If the two hosts can reach each other directly, you don't need the bundle — it's
all just files:

```bash
# on the old host
sudo systemctl stop yggdrasil
sudo rsync -aH /var/lib/yggdrasil/ newhost:/var/lib/yggdrasil/

# copy the secret_key line from the old /etc/yggdrasil/config.yaml
# into the new host's config, then on the new host:
sudo systemctl start yggdrasil
```

Same rules apply: the `secret_key` must come across, and both hosts should use the
default layout.

## After the move

- Check **Dashboard → Servers**: everything should be listed. Start one and watch
  the console to confirm the image pulled and the world loaded.
- **Ports and networking** move with the database, but the new host needs the same
  ports free and forwarded. See [Networking](networking.md).
- If a game says it can't reach its RCON or a password is wrong, the `secret_key`
  probably didn't come across — the secrets decrypted to garbage. Fix the key and
  restart.
- Once the new host is confirmed healthy, retire the old one. Don't run both
  against the same game servers or backup targets at once.

## Exporting one server

Moving a single server between two panels that both stay running isn't covered by
the whole-panel tools above — the server's configuration (its rune, variables and
ports) lives in the source panel's database, and a plain data backup doesn't carry
it. A dedicated per-server export/import is the right tool for that; until it
lands, the manual route is: recreate the server on the target panel with the same
rune and variables, then restore its data from a shared backup target (see
[Backups](backups-and-schedules.md)).
