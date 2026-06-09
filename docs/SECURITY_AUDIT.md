# Security audit & hardening

Audit performed 2026-06-08 (5 parallel reviewers: auth/RBAC, injection/files, secrets/crypto,
container privilege, web/network). Trust model: single-admin homelab, but runes are imported
from GitHub (semi-trusted) and apps are exposed publicly (Cloudflare/NPM), so privilege and
injection findings matter.

## Status

### ✅ Pass 1A — shipped v0.2.78
- **Rune privilege allowlist** (`internal/gameskill/schema.go` `validate()`): `capabilities`,
  `devices`, `sysctls`, and `extra_volumes` targets restricted to a conservative allowlist
  (caps: NET_ADMIN/NET_RAW/NET_BIND_SERVICE/SYS_NICE; devices: /dev/net/tun,/dev/dri,/dev/fuse;
  sysctls: a few net.*; extra_volumes may not shadow /usr,/bin,/etc,…). Enforced at the single
  Parse chokepoint (upload **and** runtime load), so a malicious rune is refused both ways.
- **Admin-gate rune endpoints** (`server.go`): upload/import-egg/import-xml/github-browse/
  install-from-github/delete now `requireAdmin` (were any-authenticated → priv-esc path).
- **Block built-in rune overwrite** (`handleUploadGameskill`): refuse upserting over a
  `builtin=1` rune (anti-backdoor).
- **Root command injection fixed** (`repairDataPerms`): user path was interpolated via `%q`
  (double quotes leave `$()`/backticks active) into a root `/bin/sh -c`; now shell-single-quoted.

### ✅ Pass 2 #1 (Auth) — shipped v0.2.79
- **Live session re-validation**: `authMiddleware` now re-checks the JWT against the DB every
  request — `disabled` users and role changes take effect immediately (the live role is used,
  not the token's). `users.token_version` column + `ver` JWT claim; a mismatch = revoked.
- **Real logout / revocation**: logout, and any password/role/disabled change, bump
  `token_version` → all of that user's existing tokens are invalidated.
- **Per-account login lockout**: 10 failed attempts/15 min locks a username for 15 min,
  independent of source IP (defeats `X-Forwarded-For` rotation). The per-IP limiter map is now
  swept to bound memory.

### ✅ Pass 2 #4 (Web) — shipped v0.2.80
- **CSP** (`default-src 'self'`, `script-src 'self'`, `frame-ancestors 'none'`, …) + **HSTS**
  (only when served over HTTPS, so plain-HTTP LAN access isn't force-upgraded and locked out).
- **WebSocket same-origin check** (`CheckOrigin` now compares Origin host to the request host)
  — blocks cross-site WebSocket hijacking of the console; non-browser clients (no Origin) pass.
- **CORS**: `AllowCredentials` off (no cookie reflection cross-origin); tokenless Bearer API use
  still allowed.
- **Cross-origin mutation block**: state-changing requests with a mismatched Origin are rejected
  (defense-in-depth behind SameSite=Strict; Bearer automation sends no Origin and passes).

### ✅ Pass 2 #5 + #6 (Files + Runtime) — shipped v0.2.81
- **safeJoin** now resolves symlinks (nearest existing ancestor) and re-checks the result stays
  in the data dir — a symlink planted inside can't be used to read/write host files.
- **Backup restore** rejects symlink entries whose target escapes the destination (zip-slip via
  symlink) and opens files with `O_NOFOLLOW`.
- **PidsLimit (8192)** on all runtime + install containers — caps process count so a fork bomb in
  one server can't exhaust the host PID table.

### ⏳ Pass 2 — remaining (in priority order)
- **Startup-command env injection**: `{{TEMPLATED}}` env values flow into `/bin/sh -c`. Naive
  shell-quoting breaks runes that word-split (e.g. `JAVA_OPTS`); needs a per-rune-tested fix
  (prefer `startup.exec`; validate env keys; quote only where safe).
- **Transport**: NPM/UniFi clients use `InsecureSkipVerify` (MITM of admin creds) → TOFU cert
  pinning or explicit per-integration opt-in. SFTP backup uses `InsecureIgnoreHostKey` → pin.
- **Secrets at rest**: server `env_json` (RCON/admin passwords) stored plaintext and returned in
  GET; encrypt secret-typed vars + mask in responses. BattleMetrics token → encrypt.
- **Files**: backup restore follows symlinks (zip-slip); `safeJoin` should `EvalSymlinks`.
- **Web**: WS `CheckOrigin` allowlist (today saved only by `SameSite=Strict`); CSP + HSTS
  headers; lock CORS to same-origin; Origin/Referer check on mutations; drop `?token=` fallback.
- **Runtime**: default `PidsLimit` + resource caps (anti fork-bomb); harden install containers
  (no-new-privileges, restricted network) without breaking root chown.
- **TOTP**: narrow window + reject replayed counter.
- **Low**: `crypto.New` error ignored; default bind `0.0.0.0` plain HTTP (document TLS-proxy req);
  `server.control` can edit env/subdomain (consider a stronger perm).

## Confirmed good
argon2id password hashing; API tokens stored only as SHA-256 hash; parameterized SQL throughout;
no docker.sock mount and runes can't request it; `extra_volumes` host source confined to the
data dir; no privileged/host-net/host-pid; GitHub rune fetch host-restricted; RBAC otherwise
consistent (server/file/backup/schedule handlers resolve the real target, no IDOR); login cookie
is HttpOnly + **SameSite=Strict** (load-bearing — do not relax without Origin checks).
