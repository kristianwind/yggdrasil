# API reference

Every route Yggdrasil Panel serves, what it does, and what it takes to call it. The web UI talks to
this API and nothing else, so anything the UI can do, you can automate.

## Authentication

Yggdrasil accepts two kinds of credential, and one endpoint reads them in a fixed order:

1. `Authorization: Bearer <token>` — the header wins if present.
2. The `ygg_token` cookie.
3. The `?token=` query parameter.

### Session cookie

`POST /api/auth/login` with `{"username", "password"}` (plus `"code"` when the account has TOTP
enabled) returns a signed JWT and sets it as a `ygg_token` cookie. The cookie is `HttpOnly`,
`SameSite=Strict`, path `/`, and lives for the configured session TTL. The same JWT is also returned
in the response body.

Every request carrying a JWT is re-checked against the database: the panel reads the user's current
`role`, refuses the request if the account is disabled, and refuses it if the token's version no
longer matches the account's `token_version`. That last check is what makes `POST /api/auth/logout`
a real revocation — it bumps `token_version`, invalidating every JWT ever issued to that user.
Because the role is read live, a demotion takes effect on the next request rather than at token
expiry.

### API tokens

For automation, mint a token with `POST /api/tokens` and a body of `{"name": "..."}`. The response
contains the plaintext token exactly once:

```json
{"id": "…", "name": "home-automation", "token": "ygg_…"}
```

Only a hash is stored, so a lost token cannot be recovered — delete it and mint another. Tokens
start with `ygg_`, which is how the auth middleware tells them apart from JWTs. Send one as
`Authorization: Bearer ygg_…`. `GET /api/tokens` lists your own tokens with their last-used
timestamp; `DELETE /api/tokens/{id}` removes one, and you may only delete your own.

Read [Gotchas](#gotchas) before you hand an API token to anything. A token is not a scoped
credential.

### WebSocket handshakes

Browsers cannot set headers on a WebSocket handshake, so the three streaming endpoints also accept
`?token=<jwt-or-api-token>` in the query string. The access log redacts `token`, `access_token`, and
`api_key` query parameters, so a token in a URL does not land in journald — but it does travel in
the request line, so prefer the cookie from a browser and the query parameter only where you have
no alternative.

### Cross-origin requests and CSRF

The panel serves its own UI, so it never needs credentialed cross-origin access. CORS allows any
origin but never reflects credentials, and the allowed headers are `Accept`, `Authorization`,
`Content-Type`, and `X-CSRF-Token`.

On top of `SameSite=Strict`, the auth middleware rejects `POST`, `PUT`, `PATCH`, and `DELETE`
whenever the request carries an `Origin` header whose hostname differs from the request's host. The
response is `403 {"error": "cross-origin request blocked"}`. A browser on the panel's own origin
matches and passes. Bearer automation — curl, a script, a home assistant — sends no `Origin` at all,
so the check does not apply to it. The WebSocket upgrader applies the same rule: an empty `Origin`
or a same-host one is accepted, anything else is refused.

### Login rate limits

`POST /api/auth/login` is limited to 5 attempts per IP per minute; over that you get
`429 {"error": "too many login attempts"}`. The client IP comes from the real-IP middleware, so a
reverse proxy's `X-Forwarded-For` is honoured.

Because a spoofed `X-Forwarded-For` would sidestep a purely IP-based limit, there is a second,
per-account lockout: 10 failed attempts on one username within 15 minutes locks that username for 15
minutes, regardless of source IP, with `429 {"error": "account temporarily locked due to repeated
failed logins; try again later"}`. A successful login clears the counter. A wrong password, an
unknown username, a bad TOTP code, and a replayed TOTP code all count as failures.

## Permissions

A user is either a global admin or a delegate. Admins bypass every check in the panel — the
permission helpers return `true` for them before any grant is loaded, and `requireAdmin` routes
accept them unconditionally.

Everyone else holds *grants*. A grant is a set of permissions attached to one scope. There are eight
permissions:

| Permission | Covers |
| --- | --- |
| `server.view` | Seeing the server, its status, stats, metrics, queries, and logs |
| `server.control` | Start, stop, restart, install, update, wipe, watchdog, config edits |
| `server.console` | The console stream, RCON, and player moderation |
| `server.files` | Browsing, reading, editing, uploading, downloading, deleting files |
| `server.create` | Creating servers within the scope |
| `server.delete` | Deleting servers |
| `server.backup` | Listing, running, verifying, restoring, and deleting backups |
| `server.schedule` | Creating schedules and reading their run history |

And four scopes a grant can hang off:

| Scope | Grant applies to |
| --- | --- |
| `global` | Every server |
| `realm` | Every server in that realm |
| `gameskill` | Every server built from that rune, in any realm |
| `server` | That one server |

A check passes when any single grant both contains the permission and covers the target. So a
`server.control` grant at realm scope lets you restart every server in that realm; a `server.files`
grant at gameskill scope lets you edit files on every DayZ server you can see, wherever it lives.
List endpoints filter rather than refuse: `GET /api/servers` returns only servers you can view, and
`GET /api/domains` and `GET /api/schedules` filter the same way.

Denied requests return `403 {"error": "forbidden: insufficient permissions"}`. `requireAdmin`
returns `403 {"error": "forbidden"}`.

Rune management, realm mutations, user management, and every integration setting are admin-only —
a rune controls the Docker runtime (image, command, user, capabilities, devices, mounts), so
uploading one is equivalent to root on the host.

## Gotchas

Two behaviors here will not match what you assume from the shape of the API. Both are worth reading
before you build against it.

### API tokens carry no scope of their own

An `ygg_` token is not a scoped credential. It is a pointer to a user. When the middleware sees the
`ygg_` prefix it looks the token's hash up in `api_tokens`, joins to the owning user, and builds
claims from that user's id, username, and current role. There is no per-token permission subset and
no way to create one: a token minted by an admin is an admin token, and it passes every
`requireAdmin` route — user management, rune upload, system update, the lot. A token minted by a
delegate carries exactly that delegate's grants, no more and no less. If you want a narrow
automation credential, create a user with narrow grants and mint the token as that user.

The token lookup filters on `disabled=0`, so disabling an account does immediately kill its tokens.

### Logout does not revoke API tokens

The `token_version` mechanism that makes logout revoke JWTs does not reach API tokens. The claims
built for an `ygg_` token omit the version field entirely, and the API-token branch of the
middleware returns before the code that compares a token's version against the account's. Bumping
`token_version` — which is all `POST /api/auth/logout` does — therefore has no effect on any `ygg_`
token. Logging out everywhere leaves every API token live.

To revoke an API token, delete it: `DELETE /api/tokens/{id}`. To revoke all of a user's tokens at
once, disable or delete the user.

### A delegate can create a schedule they cannot then touch

`POST /api/schedules` is permission-checked. For a server-scoped schedule it requires
`server.schedule` on that server, plus the permission the scheduled action would need if you ran it
by hand: `server.console` for a command, `server.control` for start/stop/restart/update/wipe,
`server.backup` for a backup. A rendered player message needs nothing beyond `server.schedule`. The
table is exhaustive and fails closed — an action with no entry is rejected. Realm- and
global-scoped schedules are admin-only.

The other three mutations are not permission-checked. `PUT /api/schedules/{id}`,
`DELETE /api/schedules/{id}`, and `POST /api/schedules/{id}/run` are flat admin-only and return
`403 {"error": "admin required"}` for everyone else, with no consideration of grants.

So a delegate with `server.schedule` and `server.control` on a server can create a nightly restart,
see it in `GET /api/schedules`, and read its history at `GET /api/schedules/{id}/runs` — but cannot
edit its cron expression, cannot disable it, cannot delete it, and cannot trigger it early. Only an
admin can do those.

## Conventions

Everything is JSON in and JSON out, with `Content-Type: application/json`.

Errors are a flat object with a single key, returned with the matching HTTP status:

```json
{"error": "forbidden: insufficient permissions"}
```

JSON request bodies are read through a 1 MiB limit. The reader truncates at the cap rather than
returning a distinct error, so an oversized body surfaces as `400 {"error": "invalid request"}` from
the decode. This applies to the pre-auth login endpoint too, which is the point: an unauthenticated
client cannot exhaust memory. A few endpoints have their own limits — rune upload caps the body at
512 KB, file upload is multipart with a 64 MiB form buffer, and the file editor refuses to open
anything over 5 MB.

There is deliberately no global request timeout. The console, log, and install streams are
long-lived, and container operations like an image pull or a first start can run for minutes; a
blanket timeout dropped both. Individual operations carry their own contexts instead — the AI
planner, for instance, gives the model 60 seconds.

Responses set `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
`Referrer-Policy: strict-origin-when-cross-origin`, and a strict CSP. HSTS is asserted only when the
request arrived over TLS or through a proxy that set `X-Forwarded-Proto: https`, so plain-HTTP LAN
access does not lock you out.

## Worked example

Mint a token in the UI under **Settings → API tokens**, or over the API from an existing session.
Then list your servers:

```bash
curl -s -H "Authorization: Bearer ygg_XXXXXXXX" \
  https://panel.example.com/api/servers
```

Each entry carries the server's id, name, rune, status, allocated ports, tags, and more. Abridged:

```json
[
  {
    "id": "3f9c…",
    "name": "survival",
    "gameskill_id": "minecraft-java",
    "status": "stopped",
    "ports": {"game": 25565, "rcon": 25575},
    "perms": ["server.view", "server.control", "server.console"]
  }
]
```

The `perms` array on each server is the caller's effective permissions on it — useful for deciding
what to attempt. An admin always gets all eight. Start one:

```bash
curl -s -X POST -H "Authorization: Bearer ygg_XXXXXXXX" \
  https://panel.example.com/api/servers/3f9c…/start
```

No `Origin` header goes out, so the same-origin mutation check does not apply. Follow the console:

```bash
websocat "wss://panel.example.com/api/servers/3f9c…/console?token=ygg_XXXXXXXX"
```

## Route reference

The **Auth** column names the strictest requirement. "Session" means any authenticated caller,
JWT or API token. "Admin" means a global admin. A permission name means that permission on the
target server, which an admin also satisfies.

### Public

These need no credential at all. The status and beacon routes 404 rather than 403 when their feature
is switched off, so a disabled status page or beacon receiver is not advertised.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `POST` | `/api/auth/login` | none | Exchange username/password (+ TOTP code) for a JWT and session cookie |
| `POST` | `/api/auth/passkey/login/begin` | none | Start a WebAuthn login; returns the challenge |
| `POST` | `/api/auth/passkey/login/finish` | none | Complete a WebAuthn login; issues the session |
| `GET` | `/api/version` | none | Build version, repo URL, latest release, and whether an update exists |
| `GET` | `/api/status` | none | Public status board JSON; 404 when the status page is off |
| `GET` | `/status` | none | Public status page HTML; 404 when the status page is off |
| `GET` | `/status.js` | none | The status page's script, served same-origin for the CSP |
| `POST` | `/api/beacon` | none | Receive an install ping; 404 unless this instance is the collector |
| `GET` | `/api/beacon/count` | none | Installs seen in the last 30 days; 404 unless the collector opted into publishing. `count` is `null` below the threshold — "not saying", not zero |

### Session and account

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `POST` | `/api/auth/logout` | Session | Clear the cookie and bump `token_version`, revoking every JWT for the user |
| `GET` | `/api/auth/me` | Session | The caller's id, username, role, and the scopes they can create servers in |
| `GET` | `/api/auth/2fa` | Session | Whether TOTP is enabled on the caller's account |
| `POST` | `/api/auth/2fa/setup` | Session | Generate a pending secret and return its `otpauth://` URI |
| `POST` | `/api/auth/2fa/enable` | Session | Verify a code against the pending secret and turn TOTP on |
| `POST` | `/api/auth/2fa/disable` | Session | Turn TOTP off; requires a valid code |
| `GET` | `/api/auth/passkey/credentials` | Session | List the caller's registered passkeys |
| `POST` | `/api/auth/passkey/register/begin` | Session | Start registering a passkey |
| `POST` | `/api/auth/passkey/register/finish` | Session | Finish registering a passkey |
| `PUT` | `/api/auth/passkey/credentials/{id}` | Session | Rename one of the caller's passkeys |
| `DELETE` | `/api/auth/passkey/credentials/{id}` | Session | Delete one of the caller's passkeys |
| `GET` | `/api/tokens` | Session | List the caller's API tokens with last-used times |
| `POST` | `/api/tokens` | Session | Mint an API token; the plaintext is returned once |
| `DELETE` | `/api/tokens/{id}` | Session | Delete one of the caller's own API tokens |

### Runes

The API spells a rune `gameskill`. Reading the catalogue is open to any session because the
create-server form needs it; everything that changes the catalogue is admin-only.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/gameskills` | Session | List runes, each flagged with whether the caller can create servers from it |
| `GET` | `/api/gameskills/{id}` | Session | The parsed rune definition |
| `POST` | `/api/gameskills` | Admin | Upload a rune YAML (body capped at 512 KB) |
| `POST` | `/api/gameskills/import-egg` | Admin | Convert a Pterodactyl egg JSON into a rune |
| `POST` | `/api/gameskills/import-xml` | Admin | Import a rune expressed in XML |
| `GET` | `/api/gameskills/github` | Admin | List runes in a GitHub repo directory. Each entry carries the repo copy's `version`, plus `installed` / `installed_version` / `builtin` for the local one |
| `GET` | `/api/gameskills/updates` | Admin | Installed non-builtin runes the catalog has moved past: `{updates:[{id,name,installed_version,available_version,download_url}], checked_at, note?}`. Matched by rune id against the community catalog; a `note` means the check couldn't run, which is not the same as everything being current |
| `POST` | `/api/gameskills/install-from-github` | Admin | Fetch, validate, and store one rune from GitHub |
| `DELETE` | `/api/gameskills/{id}` | Admin | Delete a rune |

### Servers

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/servers` | Session | List servers, filtered to those the caller can view |
| `POST` | `/api/servers` | `server.create` | Create a server in a realm from a rune |
| `GET` | `/api/servers/{id}` | `server.view` | One server, with the caller's effective permissions |
| `PUT` | `/api/servers/{id}` | `server.control` | Edit name, variable values, resource caps, `notes`, `notes_markdown`. Changing `realm_id` or `host_mounts` requires admin — both are privilege-scoping fields |
| `DELETE` | `/api/servers/{id}` | `server.delete` | Delete the server |
| `POST` | `/api/servers/{id}/clone` | `server.view` + `server.create` | Copy a server's setup into a fresh server with new ports and an empty data dir |
| `POST` | `/api/servers/{id}/install` | `server.control` | Run or re-run the install in the background |
| `POST` | `/api/servers/{id}/start` | `server.control` | Start the container |
| `POST` | `/api/servers/{id}/stop` | `server.control` | Stop the container |
| `POST` | `/api/servers/{id}/restart` | `server.control` | Restart the container |
| `POST` | `/api/servers/{id}/safe-restart` | `server.control` | Restart after warning players on a countdown |
| `GET` | `/api/servers/{id}/auto-restart` | `server.control` | The auto-restart toggle's current state: `{enabled, every_hours, anchor_hour, warn, backup_first, target_id}` |
| `PUT` | `/api/servers/{id}/auto-restart` | `server.control` | Create, update, or remove the managed auto-restart schedule. `every_hours` 1–24, `anchor_hour` 0–23 is the hour the cycle starts from; `backup_first` requires `target_id` |
| `PUT` | `/api/servers/{id}/watchdog` | `server.control` | Toggle auto-heal for the server |
| `POST` | `/api/servers/{id}/wipe` | `server.control` | Delete the rune's declared wipe paths, optionally backing up first |
| `GET` | `/api/servers/{id}/stats` | `server.view` | Live CPU and memory from Docker |
| `GET` | `/api/servers/{id}/logs/export` | `server.view` | Download the container log as `text/plain`. `tail` (a count or `all`), `since`/`until` (a duration like `2h`, or RFC3339), `timestamps=true`. Streamed, not buffered. The log starts at the current container's creation — a restart makes a new one, so there is no older history to ask for |
| `GET` | `/api/servers/{id}/install/log/export` | `server.view` | Download the buffered install log as `text/plain`. No range: it is the last 500 lines of the most recent install, held in memory and cleared by a panel restart |
| `GET` | `/api/servers/{id}/metrics` | `server.view` | Sampled history over the last N hours (default 24, max 168) |
| `GET` | `/api/servers/{id}/quiet-hours` | `server.view` | The calmest hour of day from 14 days of player samples |
| `GET` | `/api/servers/{id}/query` | `server.view` | Live game-protocol query: players, map, version |
| `GET` | `/api/servers/{id}/battlemetrics` | `server.view` | Online-status summary from BattleMetrics, when an id is configured |
| `GET` | `/api/servers/{id}/reachability` | `server.view` | Probe the public connect address to prove the port is forwarded |
| `GET` | `/api/servers/{id}/admin-log` | `server.view` | Parsed admin-log events, when the rune supports one |
| `GET` | `/api/servers/{id}/delegates` | Admin | Every user holding a server-scoped grant on this server |
| `PUT` | `/api/servers/{id}/delegates` | Admin | Replace the full set of server-scoped grants on this server |

### Host migration

Move things between two running panels. Both bundle formats carry secrets in
recoverable form (the target re-encrypts them with its own key), so every route
is admin-only and the files should be treated like passwords.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/servers/{id}/export` | Admin | One server as a portable `.tar.gz`: data dir, rune, variables (secrets decrypted), limits, ports, group, schedules, watchers, notification routing, subdomain |
| `POST` | `/api/servers/import` | Admin | Create a server from an exported bundle. Keeps the source's host ports when free (reports `ports_changed` otherwise) and the subdomain unless taken (`subdomain_dropped`) |
| `GET` | `/api/panel/export` | Admin | Panel configuration as JSON. `?include=` any of `channels,ai,integrations,network,rune_repos,watchers,users` |
| `POST` | `/api/panel/import` | Admin | Merge a panel-settings bundle: existing rows are skipped, never overwritten; AI config and integration keys are applied; returns a per-category summary. API tokens, beacon identity and passkeys never transfer |
| `GET` | `/api/migration/export` | Admin | One archive of settings + servers: `?include=` settings groups, `?servers=` ids or `all` |
| `POST` | `/api/migration/import` | Admin | Merge a migration archive: settings first, then each server; `?skip_existing=1` skips name clashes; returns the settings summary plus a per-server report |

### Players and moderation

Player actions run over RCON, so they gate on `server.console` rather than `server.control`.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/servers/{id}/players` | `server.console` | The online player list, or why it is unavailable |
| `POST` | `/api/servers/{id}/players/kick` | `server.console` | Kick a player |
| `POST` | `/api/servers/{id}/players/broadcast` | `server.console` | Broadcast a message in-game |
| `POST` | `/api/servers/{id}/players/lock` | `server.console` | Lock or unlock the server against new joins |
| `POST` | `/api/servers/{id}/rcon` | `server.console` | Send a raw RCON command |
| `GET` | `/api/bans` | Admin | List centrally managed bans |
| `POST` | `/api/bans` | Admin | Ban a player on one server or all of them |
| `DELETE` | `/api/bans/{id}` | Admin | Lift a ban |
| `GET` | `/api/violations` | Admin | List violation auto-action rules |
| `POST` | `/api/violations` | Admin | Create a violation auto-action rule |
| `PUT` | `/api/violations/{id}` | Admin | Edit a violation auto-action rule |
| `DELETE` | `/api/violations/{id}` | Admin | Delete a violation auto-action rule |

### Files

All of these resolve the server's data directory and gate on `server.files`. Paths are jailed to
that directory, including through symlinks.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/servers/{id}/files` | `server.files` | List a directory (`?path=`) |
| `GET` | `/api/servers/{id}/files/content` | `server.files` | Read a file for editing; refuses anything over 5 MB |
| `PUT` | `/api/servers/{id}/files/content` | `server.files` | Write a file, snapshotting the previous contents first |
| `GET` | `/api/servers/{id}/files/versions` | `server.files` | List snapshots for a path (metadata only) |
| `GET` | `/api/servers/{id}/files/versions/{vid}` | `server.files` | One snapshot's full contents |
| `DELETE` | `/api/servers/{id}/files` | `server.files` | Delete a file or directory (`?path=`) |
| `POST` | `/api/servers/{id}/files/upload` | `server.files` | Multipart upload into a directory |
| `GET` | `/api/servers/{id}/files/download` | `server.files` | Download a single file |

The editor keeps 10 versions per file and only snapshots UTF-8 files up to 256 KB.

### Backups

Backup *targets* are global configuration and admin-only. Individual backups gate on
`server.backup` for the server they belong to — for `/api/backups/{id}/…` the panel resolves the
backup's server first, then checks.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/backup/targets` | Admin | List configured backup destinations |
| `POST` | `/api/backup/targets` | Admin | Add a backup destination |
| `DELETE` | `/api/backup/targets/{id}` | Admin | Remove a backup destination |
| `POST` | `/api/backup/targets/{id}/test` | Admin | Verify a destination's credentials and reachability |
| `GET` | `/api/servers/{id}/backups` | `server.backup` | List a server's backups |
| `POST` | `/api/servers/{id}/backup` | `server.backup` | Run a backup now |
| `POST` | `/api/backups/{id}/restore` | `server.backup` | Restore a backup over the server's data |
| `POST` | `/api/backups/{id}/verify` | `server.backup` | Check one backup's integrity on demand |
| `DELETE` | `/api/backups/{id}` | `server.backup` | Delete a backup |
| `GET` | `/api/settings/backup-verify` | Admin | The automatic backup-verification settings |
| `PUT` | `/api/settings/backup-verify` | Admin | Update the automatic backup-verification settings |
| `GET` | `/api/system/backup-coverage` | Admin | Installed servers with no recent successful backup (default window 7 days) |

### Schedules and templates

Read [Gotchas](#a-delegate-can-create-a-schedule-they-cannot-then-touch) — the gates here are not
symmetric.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/schedules` | Session | List schedules, filtered to those the caller has `server.schedule` on; realm/global rows are admin-only |
| `POST` | `/api/schedules` | `server.schedule` + the action's own permission | Create a schedule; realm/global scope is admin-only |
| `PUT` | `/api/schedules/{id}` | Admin | Edit a schedule, or toggle it on and off |
| `DELETE` | `/api/schedules/{id}` | Admin | Delete a schedule |
| `POST` | `/api/schedules/{id}/run` | Admin | Trigger a schedule immediately |
| `GET` | `/api/schedules/{id}/runs` | `server.schedule` | The last 50 executions; admin for realm/global schedules |
| `GET` | `/api/templates` | Session | List message templates, built-in and custom |
| `POST` | `/api/templates` | Admin | Create or update a message template |
| `DELETE` | `/api/templates/{id}` | Admin | Delete a message template |

Managed schedule rows — the ones behind the per-server auto-restart toggle — are hidden from
`GET /api/schedules` and driven through `/api/servers/{id}/auto-restart` instead.

### Users, realms, and permissions

Listing realms is open to any session because the create-server form needs it. Every other route
here is admin-only: a realm is a permission scope, so renaming or deleting one moves servers between
grant boundaries.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/realms` | Session | List realms. Admins also get `server_count` per realm; other callers don't, since the list exists for the create-server form |
| `POST` | `/api/realms` | Admin | Create a realm. `name` is required and unique — a clash is a `409`, not a merge |
| `PUT` | `/api/realms/{id}` | Admin | Rename or re-describe a realm. `name` is required here too: it writes both columns, so omitting it would blank the name servers are matched against |
| `DELETE` | `/api/realms/{id}` | Admin | Delete a realm. Its servers are detached, not deleted; grants scoped to it stop applying |
| `GET` | `/api/users` | Admin | List users |
| `POST` | `/api/users` | Admin | Create a user |
| `PUT` | `/api/users/{id}` | Admin | Update a user's password, role, or disabled flag |
| `DELETE` | `/api/users/{id}` | Admin | Delete a user |
| `GET` | `/api/users/{id}/permissions` | Admin | A user's grants |
| `PUT` | `/api/users/{id}/permissions` | Admin | Replace a user's grants |
| `GET` | `/api/permissions/catalog` | Admin | The assignable permissions and scope types, for building an editor |

### Kvasir

Kvasir's features are opt-in and off until an admin configures a provider. Configuration is
admin-only; the advisory endpoints gate on the permission that matches what they reveal.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/ai/config` | Admin | The AI provider configuration |
| `PUT` | `/api/ai/config` | Admin | Update the AI provider configuration |
| `POST` | `/api/ai/config/test` | Admin | Send a trivial prompt to verify credentials and endpoint |
| `POST` | `/api/ai/health-digest` | Admin | An advisory cross-server ops briefing |
| `POST` | `/api/servers/{id}/admin-log/digest` | `server.view` | A plain-language read of recent admin-log activity |
| `POST` | `/api/servers/{id}/explain` | `server.view` | Explain an error from log text the caller is looking at |
| `POST` | `/api/servers/{id}/config-advice` | `server.control` | An advisory review of the server's configuration |
| `POST` | `/api/ai/plan` | Session | Turn a natural-language request into a previewable plan; executes nothing |
| `POST` | `/api/ai/plan/execute` | Session | Run confirmed actions, re-checking each against RBAC |

`/api/ai/plan` and `/api/ai/plan/execute` need no explicit permission because they scope themselves:
both build the candidate set from the servers the caller holds `server.control` on, and the execute
step re-derives that set server-side rather than trusting the posted plan. Both refuse with `400`
unless an admin has enabled AI actions.

### Watchers

Log-pattern rules scanned against running servers' container logs (see the monitoring guide). All
admin-only — watchers read logs across servers.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/watchers` | Admin | List watchers; `?server_id=` narrows to one server plus the global rules |
| `POST` | `/api/watchers` | Admin | Create a watcher (the pattern must compile) |
| `PUT` | `/api/watchers/{id}` | Admin | Update a watcher |
| `DELETE` | `/api/watchers/{id}` | Admin | Delete a watcher |
| `POST` | `/api/servers/{id}/watchers/suggest` | Admin | Ask Kvasir for watcher rules from the server's rune type and recent log; returns validated proposals, creates nothing |

### Norn

Norn is the DayZ loot-economy tool. Reading the economy needs `server.view`; changing it needs
`server.control`, because these routes rewrite mission files.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/servers/{id}/dayz/economy` | `server.view` | Types files, item and lifetime stats, cleanup timers |
| `GET` | `/api/servers/{id}/dayz/mods` | `server.view` | Each Workshop mod's on-disk and upstream status, plus orphan folders |
| `GET` | `/api/servers/{id}/dayz/mod-loot` | `server.view` | Installed mods and the `types.xml` files each ships |
| `POST` | `/api/servers/{id}/dayz/min-lifetime` | `server.control` | Raise every item lifetime below a floor, in hours |
| `POST` | `/api/servers/{id}/dayz/globals` | `server.control` | Update allowlisted `globals.xml` cleanup timers |
| `POST` | `/api/servers/{id}/dayz/register-types` | `server.control` | Register detected types files in `cfgeconomycore.xml` |
| `POST` | `/api/servers/{id}/dayz/import-mod-types` | `server.control` | Copy a mod's `types.xml` into the mission and register it |
| `POST` | `/api/servers/{id}/dayz/reset` | `server.control` | Clear saved Norn settings so reinstalls return to vanilla |

### Networking and domains

The domain list is RBAC-filtered like the server list. Every integration setting is admin-only.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/domains` | Session | Per-server subdomain and proxy state, filtered to viewable servers |
| `GET` | `/api/domains/{id}/check` | `server.view` | Probe one server's public URL; the domain is recomputed server-side |
| `GET` | `/api/settings/network` | Session | Public hostname, detected address, and the effective connect address |
| `PUT` | `/api/settings/network` | Admin | Set the public hostname, UPnP toggle, and BattleMetrics token |
| `GET` | `/api/upnp/status` | Admin | Whether UPnP is on and a gateway is reachable |
| `GET` | `/api/settings/unifi` | Admin | The UniFi controller configuration |
| `PUT` | `/api/settings/unifi` | Admin | Update the UniFi controller configuration |
| `POST` | `/api/settings/unifi/test` | Admin | Log in and list rules to confirm it works |
| `GET` | `/api/settings/npm` | Admin | The Nginx Proxy Manager configuration |
| `PUT` | `/api/settings/npm` | Admin | Update the Nginx Proxy Manager configuration |
| `POST` | `/api/settings/npm/test` | Admin | Log in and list proxy hosts to confirm it works |
| `GET` | `/api/settings/cloudflare` | Admin | The Cloudflare tunnel configuration |
| `PUT` | `/api/settings/cloudflare` | Admin | Update the Cloudflare tunnel configuration |
| `POST` | `/api/settings/cloudflare/test` | Admin | Verify the token, resolve the zone, and check the tunnel config |

`GET /api/settings/network` is readable by any session because the connect address is what the UI
shows players; writing it is admin-only.

### Steam

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/steam/account` | Admin | The authorized Steam account, if any |
| `POST` | `/api/steam/send-code` | Admin | Attempt a login without a Guard code, prompting Steam to email one |
| `POST` | `/api/steam/authorize` | Admin | Complete authorization with the Guard code |
| `DELETE` | `/api/steam/account` | Admin | Remove the stored Steam credentials |

### Status page, beacon, and Discord

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/settings/status-page` | Admin | The public status page configuration |
| `PUT` | `/api/settings/status-page` | Admin | Update the public status page configuration |
| `GET` | `/api/settings/beacon` | Admin | The beacon configuration |
| `PUT` | `/api/settings/beacon` | Admin | Update the beacon configuration |
| `POST` | `/api/settings/beacon/test` | Admin | Send a one-off ping to confirm a collector is reachable |
| `GET` | `/api/beacon/stats` | Admin | Collected install counts, when this instance is a collector |
| `GET` | `/api/settings/discord-status` | Admin | The Discord status board configuration |
| `PUT` | `/api/settings/discord-status` | Admin | Update the Discord status board configuration |
| `POST` | `/api/settings/discord-status/post` | Admin | Force an immediate status board refresh |

The public status board caches for 15 seconds and is served with `Cache-Control: public, max-age=15`.

### Notifications

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/notifications` | Admin | List notification channels |
| `POST` | `/api/notifications` | Admin | Add a notification channel |
| `DELETE` | `/api/notifications/{id}` | Admin | Remove a notification channel |
| `POST` | `/api/notifications/{id}/test` | Admin | Send a test notification |

### Audit

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/audit` | Admin | The audit log, filterable |
| `GET` | `/api/audit/export` | Admin | The same filtered log as a CSV download |

### System

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/api/system/info` | Admin | Docker health, object counts, and host CPU/memory/disk figures |
| `GET` | `/api/system/os-updates` | Admin | Pending host OS updates: `{supported, total, security?, reboot_required, reboot_pkgs?, cache_age_hours?, source}`. Read-only — the panel reports, it never applies. `security` is **absent** when the host can't tell, which is not the same as zero. Cached 15 minutes |
| `POST` | `/api/system/update` | Admin | Update the panel to the latest official release |
| `POST` | `/api/system/check-update` | Admin | Force a fresh release check, bypassing the 6-hour cache |
| `GET` | `/api/system/update-status` | Admin | The last update result written by the helper, including failures |
| `GET` | `/api/system/auto-update` | Admin | The opt-in scheduled updater's settings |
| `POST` | `/api/system/auto-update` | Admin | Update the scheduled updater's settings |

Anything not matching `/api/` falls through to the embedded SPA, which serves `index.html` for
unknown paths so client-side routes deep-link correctly. Unknown `/api/` paths return `404`.

## WebSocket endpoints

Three endpoints upgrade to WebSocket. All three authenticate exactly like the rest of the API — the
`ygg_token` cookie is enough from a browser, and `?token=` carries a JWT or an `ygg_` API token from
anything that cannot set headers. The upgrader accepts a handshake with no `Origin` (automation) or
one whose hostname matches the request host, and refuses everything else.

| Path | Auth | Streams |
| --- | --- | --- |
| `/api/servers/{id}/install/log` | `server.view` | Install and build output — buffered history, then live |
| `/api/servers/{id}/logs` | `server.view` | Container logs, live |
| `/api/servers/{id}/console` | `server.console` | Container output, and input sent over RCON or the container's stdin |

`/console` is the one that writes, which is why it gates on `server.console` while `/logs` needs only
`server.view`. Each message you send it is one command: it goes over RCON when the server's rune
declares an enabled `rcon` block, and to the container's stdin otherwise. Start an install with `POST /api/servers/{id}/install` and follow `/install/log` for
progress; the install itself runs in the background and does not block the POST.

## See also

- [Rune schema](rune-schema.md)
