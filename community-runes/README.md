# Community Runes

Extra Runes that are **not bundled** with Yggdrasil. Only a small default set
ships built-in (Minecraft Java/Bedrock, Uptime Kuma, Vaultwarden and the
Cloudflare Tunnel connector); everything else — the other games, databases and
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

- **`mongodb.yaml`** — MongoDB 4.4 (root user via `MONGO_INITDB_ROOT_*`). Pinned to
  4.4 deliberately: Mongo 5+ requires the AVX CPU instruction, which the default
  Proxmox/QEMU virtual CPU doesn't expose — 5+ dies with `Illegal instruction`.
  For a newer Mongo, set the VM's CPU type to `host` and bump the image.
- **`mariadb.yaml`** — MariaDB 11 (MySQL-compatible; root + app user/db).
- **`postgresql.yaml`** — PostgreSQL 16 (`POSTGRES_*`).

## `apps/`
These use the rune `docker.data_path` / `docker.user` / `docker.keep_entrypoint`
fields so off-the-shelf images run cleanly. Most expose a web UI on a high port —
forward it / reverse-proxy it as you like.

| Rune | Image | Port | Notes |
|------|-------|------|-------|
| `gitea.yaml` | gitea/gitea | 3000 + 2222 | self-hosted Git (set SSH port to the published one) |
| `n8n.yaml` | n8nio/n8n | 5678 | workflow automation |
| `grafana.yaml` | grafana/grafana | 3000 | dashboards (admin pw via env) |
| `jellyfin.yaml` | jellyfin/jellyfin | 8096 | media server (add a `/media` mount via Files/edit) |
| `wordpress.yaml` | wordpress | 80 | **app stack** — website/CMS; the panel bundles its MariaDB database, no separate DB rune needed |
| `nextcloud.yaml` | lscr.io/linuxserver/nextcloud | 443 | files/cloud over HTTPS (self-signed); SQLite by default. Set PUID/PGID to the data-dir owner (default 999:982) |
| `phpmyadmin.yaml` | phpmyadmin | 80 | web UI for MySQL/MariaDB — point it at a MariaDB rune (PMA_ARBITRARY=1 = type any host at login) |
| `adminer.yaml` | adminer | 8080 | lightweight single-file DB manager (MySQL/Postgres/SQLite); enter the DB host at login |
| `portainer.yaml` | portainer/portainer-ce | 9443 | Docker UI over HTTPS. Managing the LOCAL Docker needs `/var/run/docker.sock` mounted (not auto-mounted) — use remote endpoints/agents or add the socket manually |
| `pihole.yaml` | pihole/pihole | 80 + 53 | ad-blocker; admin UI at `/admin/`. DNS (:53) lands on a high panel-allocated port, so it's not usable as your real DNS without a fixed/host-network mapping |
| `adguardhome.yaml` | adguard/adguardhome | 3000 + 53 | ad-blocker / DNS with a nicer UI than Pi-hole; first launch is a setup wizard. Same DNS caveat as Pi-hole (:53 lands on a high port) |
| `home-assistant.yaml` | ghcr.io/home-assistant/home-assistant | 8123 | home automation hub. Runs bridged, so network integrations work but local mDNS auto-discovery and USB radios (Zigbee/Z-Wave) aren't available through the panel's per-server model |
| `paperless-ngx.yaml` | ghcr.io/paperless-ngx/paperless-ngx | 8000 | **app stack** — document manager with OCR + full-text search; the panel runs it with its required Redis sidecar. SQLite DB (fine for personal use) |
| `immich.yaml` | ghcr.io/immich-app/immich-server | 2283 | **app stack** — self-hosted photo/video backup (Google Photos alternative); the panel wires up server + machine-learning + Postgres (vector) + Redis. ML image + models are several GB |
| `memos.yaml` | neosmemo/memos | 5230 | lightweight notes / knowledge base |
| `homepage.yaml` | gethomepage/homepage | 3000 | homelab start page (set allowed hosts) |
| `it-tools.yaml` | corentinth/it-tools | 80 | big collection of dev/sysadmin tools (static) |
| `excalidraw.yaml` | excalidraw/excalidraw | 80 | hand-drawn-style whiteboard (static) |
| `cyberchef.yaml` | ghcr.io/gchq/cyberchef | 80 | the "cyber swiss-army knife" data tool (static) |
| `linkding.yaml` | sissbruecker/linkding | 9090 | minimal bookmark manager (SQLite) |
| `stirling-pdf.yaml` | stirlingtools/stirling-pdf | 8080 | local PDF toolbox (merge/split/OCR/convert) |
| `freshrss.yaml` | lscr.io/linuxserver/freshrss | 80 | RSS/Atom aggregator (SQLite; PUID/PGID) |
| `mealie.yaml` | ghcr.io/mealie-recipes/mealie | 9000 | recipe manager + meal planner (SQLite) |
| `static-site.yaml` | nginx:alpine | 80 | serve a folder of static files; upload via the Files tab |
| `nginx-proxy-manager.yaml` | jc21/nginx-proxy-manager | 81 + 80 + 443 | reverse proxy w/ UI (default `admin@example.com` / `changeme`). For real proxying, forward router 80/443 to the published ports |
| `headscale.yaml` | headscale/headscale:0.26.1 | 8080 | self-hosted Tailscale control server. Headless — no web signup; register nodes via CLI. Set `SERVER_URL` to the public address |
| `headplane.yaml` | ghcr.io/tale/headplane:0.6.1 | 3000 | web UI for Headscale; log in with a Headscale API key. Pin as a matched pair: headplane 0.6.x ↔ headscale 0.26.x |
| `hermes-agent.yaml` | nousresearch/hermes-agent | 9119 | **experimental** — self-improving AI agent; needs an LLM key + dashboard password. Not runtime-tested |

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
