# Feature plan: NPM subdomain integration (per-server domains)

Status: **approved, not yet built** (designed 2026-06-04). Build phase 1 next.

Give each HTTP app server an optional **subdomain** (e.g. `notes.yggdrasilpanel.com`);
Yggdrasil drives an **Nginx Proxy Manager** (via its API) to create/remove a proxy
host on server start/stop/delete. Additive to ports/UniFi — games (raw UDP) are
unaffected and don't get the field. **Mirror the existing UniFi integration almost
exactly** — same shape (encrypted settings, Test button, add-on-start /
remove-on-stop+delete hooks).

## How it routes
Yggdrasil's app containers publish a **host port** (e.g. WindzNotes → 25072). NPM
proxies `sub.domain → <internal_host>:<app web port>` (NOT the container directly —
they're not on NPM's Docker network). `internal_host` defaults to the VM's LAN IP
(a setting, since NPM may live on another machine). Route to the server's **`web`
port** (first tcp port if no port named `web`).

Out-of-band prereqs (document in UI help): wildcard DNS `*.domain → public IP`,
router 80/443 → NPM, certs handled by NPM (Let's Encrypt).

## NPM API (jc21/nginx-proxy-manager)
- Auth: `POST {npmURL}/api/tokens` `{identity:<email>, secret:<password>}` → `{token}`. Use `Authorization: Bearer <token>` (cache until expiry).
- Create: `POST /api/nginx/proxy-hosts` JSON:
  ```json
  {"domain_names":["notes.yggdrasilpanel.com"],"forward_scheme":"http",
   "forward_host":"192.168.1.158","forward_port":25072,
   "access_list_id":0,"certificate_id":"new","ssl_forced":true,
   "block_exploits":true,"allow_websocket_upgrade":true,"http2_support":true,
   "hsts_enabled":false,"caching_enabled":false,"locations":[],"advanced_config":"",
   "meta":{"letsencrypt_agree":true,"dns_challenge":false,"letsencrypt_email":"<email>"}}
  ```
  Returns `{id, ...}`. (`certificate_id:"new"` = request a fresh LE cert; if HTTP-01
  is flaky on a high-port-only setup, fall back to `certificate_id:0` = no SSL, or a
  pre-made wildcard cert id.)
- Delete: `DELETE /api/nginx/proxy-hosts/{id}`.
- List (for reconcile): `GET /api/nginx/proxy-hosts`.

## Implementation (mirror internal/unifi + handlers_unifi.go)
1. **`internal/npm/npm.go`** — client like `internal/unifi`: `New(url,email,password)`, `login()` (token cache), `CreateProxyHost(domain, fwdHost, fwdPort, opts) (id int, err)`, `DeleteProxyHost(id)`, `TestConnection()`. Plain `net/http` + JSON; honor `InsecureSkipVerify` if NPM is https-self-signed.
2. **DB (`internal/db/db.go`)** via `addColumnIfMissing`:
   - `servers.subdomain TEXT` (the chosen label or full domain; empty = off).
   - `servers.npm_host_id INTEGER` (the NPM proxy-host id we created — for precise delete/update).
   - `app_settings`: store NPM config like UniFi — `npm_url`, `npm_email`, `npm_password` (encrypted via `s.cipher`), `npm_enabled`, `npm_base_domain`, `npm_internal_host` (default LAN IP), `npm_le_email`.
3. **`internal/api/handlers_npm.go`** (mirror handlers_unifi.go):
   - `npmClient(ctx)` — load+decrypt settings, build client.
   - `npmAddServer(id, name)` — if enabled + server has a subdomain + an http/web port: resolve full domain (`<subdomain>.<base_domain>` or a full custom domain), CreateProxyHost(domain, internal_host, webPort), store `npm_host_id`.
   - `npmRemoveServer(id)` — if `npm_host_id` set: DeleteProxyHost(it); clear the column.
   - `GET/PUT /api/settings/npm` (+ `POST /api/settings/npm/test`) — like the UniFi settings handlers; never return the password (only a `configured` flag).
4. **Wire into the server lifecycle** (handlers_servers.go): call `go s.npmAddServer(id,name)` next to `s.upnpAddServer`/`s.unifiAddServer` in `recreateAndStart`; call `go s.npmRemoveServer(id)` next to the upnp/unifi removes in `handleStopServer` + the delete handler. (Search for `unifiAddServer` / `unifiRemoveServer` — add the npm calls beside every one.)
5. **Routes (server.go):** `r.Get/Put("/api/settings/npm", requireAdmin(...))`, `r.Post("/api/settings/npm/test", requireAdmin(...))`. Accept `subdomain` in create/update server payloads (handlers_servers.go: add to the req struct + persist to `servers.subdomain`).
6. **Which port:** helper to pick the server's web port: ports map → `web`, else first tcp port. Only offer/act when one exists (skip steam/UDP-only games).
7. **UI:**
   - Settings → Network: an **NPM** card mirroring the UniFi card (url/email/password/base-domain/internal-host/LE-email/enable + Test). See `web/src/views/Settings.svelte` UniFi card.
   - Server create modal + ServerDetail Settings: a **Subdomain** field (only when the rune has an http/web port). Show the resulting full URL (`<sub>.<base_domain>`). Persist via the existing server create/update calls (add `subdomain`).
   - (Phase 2) a **Domains** menu: list apps + their subdomain + reachable badge.

## Gotchas
- Re-import community rune ≠ DB rune (re-`POST /api/gameskills` after editing) — unrelated but a known footgun.
- NPM proxy-host create can fail if the LE cert (HTTP-01) can't validate (needs 80 reachable on the public IP for that domain). Handle the error gracefully (create the host with `certificate_id:0`/no SSL and surface a warning), or document using a wildcard cert (DNS-01) created once in NPM.
- Store `npm_host_id` so deletes are precise; on start, if a host for the domain already exists (reconcile via GET), update instead of duplicate.
- Encrypt the NPM password at rest (`s.cipher`), never return it in API responses — exactly like UniFi.

## Test plan
1. Settings → NPM: point at the user's existing NPM, Test → ok.
2. Create a memos server with subdomain `notes`; start → NPM gets a proxy host `notes.<base> → LAN-IP:port`; `https://notes.<base>` serves memos.
3. Stop/delete → proxy host removed (by stored id).
4. A DayZ server shows no subdomain field (UDP-only) and is untouched.
