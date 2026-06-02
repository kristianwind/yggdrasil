# Community Runes

Extra Runes that are **not bundled** with Yggdrasil. The four core games (DayZ,
Rust, Minecraft Java/Bedrock) ship built-in; everything else — databases and
homelab apps — lives here so the default set stays lean.

To use one: open **Runes → Carve a rune (upload)** in the panel and upload the
`.yaml` file from this folder. It's then available when creating a server.

> Provided as-is, community-maintained. Apps run in Docker exactly like a normal
> `docker run`; you may need to tune ports/env for your setup. Put a reverse proxy
> (see the main README) in front of web apps to serve them on a domain with TLS.

## Databases
Backing stores for other runes/apps (e.g. WordPress/Nextcloud → MariaDB). All
verified live on a real VM (init + auth).

- **`mongodb.yaml`** — MongoDB 7 (root user via `MONGO_INITDB_ROOT_*`).
- **`mariadb.yaml`** — MariaDB 11 (MySQL-compatible; root + app user/db).
- **`postgresql.yaml`** — PostgreSQL 16 (`POSTGRES_*`).

## Homelab apps
These use the rune `docker.data_path` / `docker.user` / `docker.keep_entrypoint`
fields so off-the-shelf images run cleanly. Most expose a web UI on a high port —
forward it / reverse-proxy it as you like.

| Rune | Image | Port | Notes |
|------|-------|------|-------|
| `uptime-kuma.yaml` | louislam/uptime-kuma | 3001 | uptime monitoring + status pages |
| `vaultwarden.yaml` | vaultwarden/server | 8080 | Bitwarden-compatible password manager |
| `gitea.yaml` | gitea/gitea | 3000 + 2222 | self-hosted Git (set SSH port to the published one) |
| `n8n.yaml` | n8nio/n8n | 5678 | workflow automation |
| `grafana.yaml` | grafana/grafana | 3000 | dashboards (admin pw via env) |
| `jellyfin.yaml` | jellyfin/jellyfin | 8096 | media server (add a `/media` mount via Files/edit) |
| `wordpress.yaml` | wordpress | 80 | website/CMS — **needs a MariaDB rune** |
| `nextcloud.yaml` | nextcloud | 80 | files/cloud (SQLite by default; DB rune for production) |
| `memos.yaml` | neosmemo/memos | 5230 | lightweight notes / knowledge base |
| `homepage.yaml` | gethomepage/homepage | 3000 | homelab start page (set allowed hosts) |

WordPress/Nextcloud: create a **MariaDB** rune first, then point the app's
`*_DB_HOST` at that server's connect address (`host:port`).

## Games

### `terraria.yaml` — Terraria
Official terraria.org dedicated server (downloaded at install, bundled-Mono
binary). TCP 7777; world size/difficulty/password as variables.

## Available (legacy notes)

### `genshin-impact.yaml` — Genshin Impact (Grasscutter)

Genshin Impact has no official dedicated server. This rune runs
[Grasscutter](https://github.com/Grasscutters/Grasscutter), an open-source
private-server emulator. It is a **starter template** — you must provide:

- **MongoDB** reachable from the container (set `MONGO_URI` when creating the
  server). Run it as another server or on the host.
- **`grasscutter.jar`** — upload it into the server's files (Files tab).
- **`Resources/`** game data — upload via the Files tab. Not downloaded
  automatically.

After uploading those, press **Start**. See the Grasscutter wiki for client
redirection. The install step writes a `config.json` from your chosen ports +
`MONGO_URI`; edit it via the Files tab for anything deeper.
