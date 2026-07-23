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

## Moving one server

Moving a single server between two panels that both stay running is built in:
every server page has **Export**, and the Servers page has **Import server**.

The bundle carries the whole habitat, not just the animal: the server's data
directory, its rune (the target doesn't need it pre-installed), variables with
their secrets, resource limits, host mounts, its group, its host ports — plus
the server's **schedules, watchers, notification routing and subdomain**. On
import the target re-encrypts the secrets with its own key, keeps the source's
host ports so NPM/tunnel/DNS forwarding survives (a port is reallocated only on
a real collision, and the response tells you which), and keeps the subdomain
unless another server on the target already claims it.

Because the bundle contains the server's secrets in recoverable form, both ends
are admin-only — treat the file like a password.

## Moving settings and servers together

The same Host migration section can bundle **settings plus any selection of
servers** into one archive: tick the groups, tick the servers (or **All**), and
Export produces a single `.tar.gz` holding the settings bundle and one server
bundle per selection. Import it on the target in one upload: settings merge
first, then each server lands with its full habitat, and the result is a
per-server report — imported, skipped, ports moved, subdomain dropped.

Two knobs matter on import: **skip servers that already exist here** (on by
default) makes re-imports idempotent — a server whose name is already present
is reported and left alone, so you can re-run an archive after a partial
transfer without duplicating anything. With it off, a name clash imports the
copy as "<name> (imported)". An imported server always arrives **stopped**.

Selecting every server plus every settings group is a whole-panel **copy** onto
a target that keeps its own identity. It differs from `migrate import` (below)
on exactly one axis: the target's own database survives — nothing is
overwritten, and the target keeps its own secret key, so API tokens still need
recreating there.

## Moving your settings

**Settings → System → Host migration** moves the panel's configuration — not
its servers — to another running panel. Pick the groups to export (notification
channels, Kvasir/AI configuration, integrations such as the Steam and Discord
keys, network integrations, rune repositories, global watchers, users and
permissions), download the bundle, and import it on the target.

The import **merges**: nothing on the target is deleted, and an existing user,
channel, repository or watcher is skipped rather than overwritten. Two things
are applied rather than merged, because moving them is the point: the Kvasir/AI
configuration and the selected integration keys. Every secret in the bundle is
re-encrypted with the target's key on arrival.

Not carried, deliberately: **API tokens** (each panel signs its own — recreate
them on the target), the **beacon identity** (per-install by design), and
**passkeys** (re-register them on the target in seconds). The bundle contains
decrypted secrets — same rule as above: treat the file like a password, delete
it after the move.
