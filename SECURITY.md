# Security policy

## Reporting a vulnerability

**Please report privately, not in a public issue or in Discord.**

Use GitHub's private reporting: **[Security → Report a
vulnerability](https://github.com/kristianwind/yggdrasil/security/advisories/new)**. It's a private
channel between you and the maintainer, and it becomes a published advisory once there's a fix.

If that form isn't available to you, open a regular issue that says only *"security report, need a
private channel"* — no details — and you'll get one.

What helps: the panel version, what an attacker would need to already have (an account? which role?
network access?), and the smallest thing that demonstrates it. A proof of concept is welcome but not
required.

Yggdrasil Panel is a hobby project maintained by one person. There's no bounty and no SLA, but
reports get read and taken seriously.

## Supported versions

The **latest release** only. Fixes ship forward as a new version rather than being backported —
updating is a binary swap and a service restart, and panels can auto-update.

## What Yggdrasil already assumes

Some things look like findings but are the design. Knowing the boundary saves everyone time:

- **The panel runs as a privileged service.** It talks to the Docker daemon, so it can start
  containers, bind host paths and read any server's files. Anyone who is a panel admin is
  effectively root on that host. This is the same trust model as Portainer, Pterodactyl or AMP.
- **Admin-only means trusted.** A rune chooses the container image, the command and the user it runs
  as, so uploading one is equivalent to running code on the host. That's why rune management,
  host-mounts and the network integrations are all admin-gated rather than delegable.
- **Integrations tolerate self-signed TLS.** Nginx Proxy Manager and UniFi are typically reached over
  a LAN address with a self-signed certificate. The panel accepts them because the admin configuring
  them already owns the host — enforcing TLS there would break the normal setup without gaining
  anything an admin couldn't do anyway.
- **Delegates are the real security boundary.** The [permission
  model](docs/guides/users-and-permissions.md) exists to give a non-admin exactly one server, or one
  action on it. A way for a delegate to exceed their grants **is** a vulnerability — please report it.
  Two such bugs were found and fixed in `v0.2.143` and `v0.2.147`.

## Secrets

`auth.secret_key` in `/etc/yggdrasil/config.yaml` signs sessions and derives the AES-256-GCM key that
encrypts stored credentials — UniFi, Nginx Proxy Manager, Cloudflare and BattleMetrics tokens, the
Discord webhook, and backup-target credentials. It's the one value that protects everything else;
the file is `chmod 640`, `root:yggdrasil`.

Note that **rune variables are only encrypted when the rune marks them `secret: true`** (or names them
via `rcon.password_var`). A variable that merely *looks* like a secret is stored in plaintext — see
the [rune schema](docs/reference/rune-schema.md). If you write runes, flag your secrets.

Panel logs and config can contain all of the above. Read what you paste into an issue.

## Dependency alerts

Dependabot currently reports several **high** alerts against `github.com/docker/docker`. They are
open on purpose, and here's the reasoning so it reads as a decision rather than neglect:

- They are **daemon-side** vulnerabilities — `docker cp` races, the `PUT /containers/{id}/archive`
  handler, AuthZ plugin bypasses. Yggdrasil ships a *client*; it does not run a Docker daemon.
- `github.com/docker/docker` is one Go module containing both the daemon and the client, so an
  advisory against the daemon flags every consumer of the client. `govulncheck` reports the
  "reachable symbols" as `client.Ping`, `client.ContainerStats` and similar — generic client calls,
  not the vulnerable code. That's the advisory's granularity, not real reachability.
- The most serious of them has **no fixed module version** (`Fixed in: N/A`), because the fix ships
  in Docker Engine, which is versioned separately from the Go module. There is nothing to bump.
- We never call the archive endpoint the worst advisory concerns (`CopyToContainer`,
  `CopyFromContainer`, `ContainerArchive` appear nowhere in this codebase).

**The fix for those CVEs is to keep Docker itself updated on the host** (`apt upgrade docker-ce`),
which is where the vulnerable code actually runs. If you find a way to reach one of them *through*
the panel, that changes the analysis — please report it.

Alerts against anything else, including the Go standard library, are treated as real and fixed by
bumping. Released binaries are built with the latest Go 1.26 patch.
