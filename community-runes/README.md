# Community Runes

Extra Runes that are **not bundled** with Yggdrasil. The four core games (DayZ,
Rust, Minecraft Java/Bedrock) ship built-in; everything else — databases and
homelab apps — lives here so the default set stays lean.

Runes are grouped into folders: [`databases/`](databases/), [`apps/`](apps/),
[`games/`](games/).

To use one: open **Runes → Browse GitHub** in the panel and one-click install any
rune (the browser descends into these subfolders automatically), or **Carve a
rune (upload)** and upload the `.yaml` file directly. It's then available when
creating a server.

> Provided as-is, community-maintained. Apps run in Docker exactly like a normal
> `docker run`; you may need to tune ports/env for your setup. Put a reverse proxy
> (see the main README) in front of web apps to serve them on a domain with TLS.

## `databases/`
Backing stores for other runes/apps (e.g. WordPress/Nextcloud → MariaDB). All
verified live on a real VM (init + auth).

- **`mongodb.yaml`** — MongoDB 7 (root user via `MONGO_INITDB_ROOT_*`).
- **`mariadb.yaml`** — MariaDB 11 (MySQL-compatible; root + app user/db).
- **`postgresql.yaml`** — PostgreSQL 16 (`POSTGRES_*`).

## `apps/`
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
| `nextcloud.yaml` | lscr.io/linuxserver/nextcloud | 443 | files/cloud over HTTPS (self-signed); SQLite by default. Set PUID/PGID to the data-dir owner (default 999:982) |
| `phpmyadmin.yaml` | phpmyadmin | 80 | web UI for MySQL/MariaDB — point it at a MariaDB rune (PMA_ARBITRARY=1 = type any host at login) |
| `adminer.yaml` | adminer | 8080 | lightweight single-file DB manager (MySQL/Postgres/SQLite); enter the DB host at login |
| `portainer.yaml` | portainer/portainer-ce | 9443 | Docker UI over HTTPS. Managing the LOCAL Docker needs `/var/run/docker.sock` mounted (not auto-mounted) — use remote endpoints/agents or add the socket manually |
| `pihole.yaml` | pihole/pihole | 80 + 53 | ad-blocker; admin UI at `/admin/`. DNS (:53) lands on a high panel-allocated port, so it's not usable as your real DNS without a fixed/host-network mapping |
| `memos.yaml` | neosmemo/memos | 5230 | lightweight notes / knowledge base |
| `homepage.yaml` | gethomepage/homepage | 3000 | homelab start page (set allowed hosts) |
| `it-tools.yaml` | corentinth/it-tools | 80 | big collection of dev/sysadmin tools (static) |
| `excalidraw.yaml` | excalidraw/excalidraw | 80 | hand-drawn-style whiteboard (static) |
| `cyberchef.yaml` | ghcr.io/gchq/cyberchef | 80 | the "cyber swiss-army knife" data tool (static) |
| `linkding.yaml` | sissbruecker/linkding | 9090 | minimal bookmark manager (SQLite) |
| `stirling-pdf.yaml` | stirlingtools/stirling-pdf | 8080 | local PDF toolbox (merge/split/OCR/convert) |
| `dozzle.yaml` | amir20/dozzle | 8080 | live Docker log viewer — **needs the Docker socket** (not auto-mounted) |
| `freshrss.yaml` | lscr.io/linuxserver/freshrss | 80 | RSS/Atom aggregator (SQLite; PUID/PGID) |
| `mealie.yaml` | ghcr.io/mealie-recipes/mealie | 9000 | recipe manager + meal planner (SQLite) |

WordPress/Nextcloud: create a **MariaDB** rune first, then point the app's
`*_DB_HOST` at that server's connect address (`host:port`).

## `games/`

| Rune | Image | Port | Notes |
|------|-------|------|-------|
| `terraria.yaml` | ubuntu (terraria.org build) | 7777/tcp | official server, bundled-Mono; size/difficulty/password as vars |
| `factorio.yaml` | factoriotools/factorio | 34197/udp + 27015/tcp | auto-creates a save; no Steam account; PUID/PGID |
| `luanti.yaml` | lscr.io/linuxserver/luanti | 30000/udp | Luanti (Minetest) voxel engine — free, tiny, mod-friendly; PUID/PGID |
| `genshin-impact.yaml` | eclipse-temurin (Grasscutter) | 443 + 22102 | **experimental** — needs a manual `grasscutter.jar` + Resources + MongoDB (see below) |

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
