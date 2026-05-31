# Yggdrasil REST API

Base path: `/api`. All responses are JSON. Errors are `{"error": "message"}` with
an appropriate HTTP status.

## Authentication

Two token types, both sent as `Authorization: Bearer <token>`:

- **Session JWT** — from `POST /api/auth/login`. Also set as an HttpOnly cookie.
- **API token** — created under Settings (prefix `ygg_`). Acts as its owning user
  with that user's role/permissions. Ideal for automation.

WebSocket endpoints accept the token as a `?token=` query parameter (browsers
can't set headers on the WS handshake); the login cookie also works.

```bash
# Log in and capture a token
TOKEN=$(curl -s -X POST http://host:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"…"}' | jq -r .token)

# Use it
curl -H "Authorization: Bearer $TOKEN" http://host:8080/api/servers
```

## Access control

Global admins bypass all checks. Other users are evaluated against scoped grants
(see [RBAC](#permissions)); endpoints return `403` when not permitted. List
endpoints are filtered to what the caller may see.

## Endpoints

### Auth
| Method | Path | Notes |
|--------|------|-------|
| POST | `/api/auth/login` | `{username, password}` → `{token, role}` |
| POST | `/api/auth/logout` | clears the cookie |
| GET | `/api/auth/me` | current user |

### Gameskills (Runes)
| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/gameskills` | list |
| POST | `/api/gameskills` | upload YAML (raw body) |
| POST | `/api/gameskills/import-egg` | import a Pterodactyl egg (raw JSON) |
| GET | `/api/gameskills/{id}` | full parsed gameskill |
| DELETE | `/api/gameskills/{id}` | (built-ins protected) |

### Servers
| Method | Path | Perm | Notes |
|--------|------|------|-------|
| GET | `/api/servers` | view | filtered list |
| POST | `/api/servers` | create | `{name, gameskill_id, env, cpu_percent, memory_mb}` → installs in background |
| GET | `/api/servers/{id}` | view | |
| DELETE | `/api/servers/{id}` | delete | removes the container |
| POST | `/api/servers/{id}/install` | control | (re)run install |
| GET | `/api/servers/{id}/install/log` | control | **WebSocket** install output |
| POST | `/api/servers/{id}/start` | control | gated on install |
| POST | `/api/servers/{id}/stop` | control | |
| POST | `/api/servers/{id}/restart` | control | |
| GET | `/api/servers/{id}/stats` | view | CPU/RAM |
| GET | `/api/servers/{id}/query` | view | player count/status |
| POST | `/api/servers/{id}/rcon` | console | `{command}` → `{response}` |
| GET | `/api/servers/{id}/logs` | view | **WebSocket** log stream |
| GET | `/api/servers/{id}/console` | console | **WebSocket** console (stdin/stdout) |

### Files (perm: files)
| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/servers/{id}/files?path=` | list a directory |
| GET | `/api/servers/{id}/files/content?path=` | read a file |
| PUT | `/api/servers/{id}/files/content` | `{path, content}` |
| DELETE | `/api/servers/{id}/files?path=` | |
| POST | `/api/servers/{id}/files/upload` | multipart `path`, `file` |
| GET | `/api/servers/{id}/files/download?path=` | |

### Backups
| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/backup/targets` | admin; list (no secrets) |
| POST | `/api/backup/targets` | admin; `{name, type, path, host, port, username, password, share, keep_n, keep_days}` |
| DELETE | `/api/backup/targets/{id}` | admin |
| POST | `/api/backup/targets/{id}/test` | admin; connectivity check |
| GET | `/api/servers/{id}/backups` | perm: backup |
| POST | `/api/servers/{id}/backup` | perm: backup; `{target_id}` (async) |
| POST | `/api/backups/{id}/restore` | perm: backup; stops container first |
| DELETE | `/api/backups/{id}` | perm: backup |

### Schedules & templates
| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/schedules` | list (filtered) |
| POST | `/api/schedules` | `{name, cron_expr, action, server_id|realm_id, args}` |
| PUT | `/api/schedules/{id}` | `{enabled?, cron_expr?}` (admin) |
| DELETE | `/api/schedules/{id}` | admin |
| POST | `/api/schedules/{id}/run` | trigger now (admin) |
| GET | `/api/templates` | message templates |
| POST | `/api/templates` | admin; `{id?, name, body}` |
| DELETE | `/api/templates/{id}` | admin |

Schedule actions: `backup`, `restart`, `start`, `stop`, `command`, `message`,
`update`. Cron is 5 or 6 fields. `args` may include `target_id`, `command`,
`template_id`, `minutes`, `seconds`, `skip_if_players`.

### Realms
`GET/POST /api/realms`, `PUT/DELETE /api/realms/{id}`.

### Users & permissions (admin)
| Method | Path | Notes |
|--------|------|-------|
| GET/POST | `/api/users` | |
| PUT/DELETE | `/api/users/{id}` | |
| GET | `/api/users/{id}/permissions` | grants |
| PUT | `/api/users/{id}/permissions` | replace grants (array of `{scope_type, scope_id, perms}`) |
| GET | `/api/permissions/catalog` | assignable perms + scope types |

<a id="permissions"></a>
**Permissions:** `server.view`, `server.control`, `server.console`,
`server.files`, `server.create`, `server.delete`, `server.backup`,
`server.schedule`. **Scopes:** `global`, `realm`, `gameskill`, `server`.

### Bans (admin)
`GET /api/bans`, `POST /api/bans` (`{player_name, server_id?, reason}` — empty
`server_id` = all servers), `DELETE /api/bans/{id}`.

### Notifications (admin)
`GET/POST /api/notifications`, `DELETE /api/notifications/{id}`,
`POST /api/notifications/{id}/test`. Types: `telegram`, `discord`, `webhook`.

### API tokens
`GET /api/tokens`, `POST /api/tokens` (`{name}` → `{token}` shown once),
`DELETE /api/tokens/{id}`.

### Audit & system (admin)
`GET /api/audit?limit=`, `GET /api/system/info`.
