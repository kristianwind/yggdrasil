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

### ⏳ Pass 2 — planned (in priority order)
- **Auth**: session/token revocation (check `disabled` + live `role` per request; bump a
  `token_version` on logout/disable/role-change/password-reset). Per-account login lockout +
  don't trust `X-Forwarded-For` for the limiter when not behind a known proxy.
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
