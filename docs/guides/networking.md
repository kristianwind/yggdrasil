# Networking and public access

How Yggdrasil Panel gives a server a port, works out the address players connect to, and — if you
want it — opens that address to the internet. Read this when a server runs fine locally but nobody
outside can reach it.

## Port allocation

Every rune declares the ports its game or app needs, each with a name (`game`, `query`, `rcon`,
`web`, …). When you create a server, Yggdrasil allocates one host port per declared port and stores
the mapping on the server row.

Ports come sequentially from a configured range — `ports.range_min` to `ports.range_max` in the
panel's config file, defaulting to **25000–30000**. The game's well-known default port (2302, 25565,
27016, …) is deliberately *not* used, even when the rune names it: distinctive ports attract less
scanning, and every server on the host gets a port of its own with no collisions.

A port is only handed out when all three of these hold:

- it isn't recorded in the panel's `port_allocations` table,
- Docker isn't already publishing it (including containers Yggdrasil didn't create), and
- Yggdrasil can bind it right now — each candidate is test-bound on `0.0.0.0` before it's accepted.

The per-server connect address is the **effective public host** (below) joined to the allocated port.
The server page lists one `host:port` line per named port, which is what you hand to players.

## Public hostname and the external-IP fallback

**Settings → Network** has a **Public hostname** field, stored as the `public_hostname` setting. Put
your domain or static IP there. If you paste a full URL, Yggdrasil strips the scheme and any trailing
slash — it wants a bare host.

Leave it blank and Yggdrasil falls back to a detected external IP: it fetches your public address
from `https://api.ipify.org` with a 3-second timeout and caches the result for one hour. If that
lookup fails, the cached (possibly stale) value is used. If there has never been one, the effective
host is empty and a server's **Connect address** falls back to the literal placeholder `your-host` —
`your-host:25565` — with a prompt to go and set a hostname. The address is always shown; only the host
part is missing.

`GET /api/settings/network` returns all three values, which is the clearest way to see what's
happening: `public_hostname` (what you set), `detected` (the external-IP fallback), and `effective`
(the one actually used — your hostname if set, otherwise the detected IP).

Set a real hostname if you have one. The fallback re-detects on a dynamic IP, but anything you have
already told players stays stale.

## "Online from outside" reachability

A running server's page carries a reachability badge — **reachable** or **not from outside**. This is
a genuine external probe, not a container health check: Yggdrasil connects to the *effective public
host*, never to `127.0.0.1`. A success proves both that the game is answering and that the port
reaches it from the outside world.

How it probes depends on the rune:

- If the rune declares a query protocol, Yggdrasil speaks that protocol (A2S, Minecraft SLP, Bedrock
  ping, …) against the query port.
- Otherwise it opens a plain TCP connection to the `game` port.

Either way the timeout is 4 seconds, and the result is cached for 30 seconds per server
(`GET /api/servers/{id}/reachability`). The badge only appears while a server is running or starting.

One caveat worth internalising: the probe leaves the panel host and comes back in through your
router, so it depends on **NAT loopback** (hairpinning). Routers that don't support it will make the
badge read "not from outside" for a server that players can reach perfectly well. If the badge
disagrees with reality, test from a phone on mobile data before you go changing port forwards.

## Per-server auto-forward

Each server has an **auto_forward** flag, **on by default**. When a server with auto-forward on
starts, Yggdrasil asks UPnP and UniFi (whichever you have enabled) to open its ports; when it stops
or is deleted, the mappings and rules come back down. Turn auto-forward off for a server that should
stay LAN-only, or one whose ports you forward by hand.

The **RCON port is never forwarded**, regardless of the setting. An admin console on the open
internet is not something Yggdrasil will do for you.

Auto-forward covers firewall/NAT forwarding only. The two subdomain integrations — Nginx Proxy
Manager and Cloudflare Tunnel — run independently of it and gate themselves on their own settings
plus a per-server subdomain.

## Four ways to expose a server

| Option | Needs a port forward | Carries | Best for |
| --- | --- | --- | --- |
| UPnP | Router opens it for you | Any TCP/UDP port | A home router you control, quick setup |
| UniFi | Rules created for you | Any TCP/UDP port | A UniFi gateway, game servers, reproducible rules |
| Nginx Proxy Manager | Yes (ports 80/443 to NPM) | HTTP(S) only | Web apps on a subdomain, with certificates |
| Cloudflare Tunnel | No | HTTP(S) only | No public IP, CGNAT, or no wish to open ports |

Game servers need UPnP or UniFi (or a manual forward). Web apps can use any of the four; the two
subdomain options give them a name instead of a port number.

### UPnP

**Off by default.** Enable it under **Settings → Network**. Yggdrasil discovers an Internet Gateway
Device on the LAN and asks it to map each of a server's non-admin ports to the same port on the panel
host, described as `Yggdrasil: <server name>`. The lease is permanent — mappings are removed when the
server stops rather than expiring on a timer.

`GET /api/upnp/status` reports whether a gateway answered, and if so the panel's LAN IP and the
gateway's WAN IP. If no gateway is found (no UPnP, or it's switched off in the router), discovery
returns an error and nothing else happens — you forward manually instead.

UPnP is convenient and blunt. Any device on your LAN can open ports through it. If you have a UniFi
gateway, prefer the UniFi integration: same result, explicit rules you can audit.

### UniFi

**Settings → Network → UniFi** takes the controller URL, a local username and password, and a site
(default `default`). The password is encrypted at rest. **Test connection** logs in and lists your
existing port-forward rules, which confirms both credentials and site.

UniFi OS speaks HTTPS only on its local API, and it presents a self-signed certificate — Yggdrasil
skips verification for that host and assumes `https://` when you omit the scheme from the URL. The
controller lives at your gateway's address (`https://192.168.1.1`), not the cloud portal.

When a server with auto-forward starts, Yggdrasil creates one WAN port-forward rule per non-admin
port, named:

```text
Yggdrasil: <server name> [ygg:<id8>]
```

The `[ygg:<id8>]` tag — the first 8 characters of the server's id — is how Yggdrasil finds its own
rules again. Every rule carrying a server's tag is deleted when that server stops, is deleted, or
starts again (stale rules are cleared before fresh ones go in). Leave the tag alone in the rule name
if you edit rules in the UniFi UI, or Yggdrasil will orphan them.

### Nginx Proxy Manager (subdomains)

For **HTTP apps only**. NPM terminates TLS and proxies a subdomain to an app's published port, so
`notes.example.com` reaches the app instead of `example.com:25004`.

Configure **Settings → Domains → Nginx Proxy Manager** with the NPM admin URL (assumed `http://` when
you omit the scheme), the admin email and password (encrypted at rest), a **base domain**, an optional
**internal host** (defaults to the panel host's LAN IP), and a Let's Encrypt account email. Then set a
**Subdomain** on each server you want exposed.

The per-server `subdomain` field takes a bare label (`notes`), which is joined to the base domain, or
a value containing a dot, which is treated as a complete custom domain and used verbatim. Empty means
the feature is off for that server.

Yggdrasil proxies to the host port named `web`, or the first TCP port if the rune doesn't name one.
A server with no TCP port — a UDP-only game — has nothing to proxy and is skipped entirely.

On start, Yggdrasil creates a proxy host routing the domain to `<internal host>:<port>` over plain
HTTP, requesting a fresh Let's Encrypt certificate with forced SSL, HTTP/2, websocket upgrades and
NPM's exploit blocking. If certificate issuance fails — HTTP-01 needs port 80 reachable for that
domain — it cleans up the partial host and retries **without SSL**, so routing works and you can
attach a wildcard certificate later. Changing a server's subdomain drops the old proxy host; the next
start creates one for the new domain.

You still need ports 80 and 443 forwarded to NPM, and a wildcard DNS record (`*.your-domain` → your
public IP) so each new subdomain resolves without another DNS edit. NPM is the right choice when you
already run it; Cloudflare Tunnel is the right choice when you would rather not open anything.

### Cloudflare Tunnel (subdomains)

Also **HTTP apps only**, and the option that needs **no port forward at all**. The `cloudflared`
connector makes an outbound connection to Cloudflare's edge, and traffic for your hostname comes back
down it.

Three pieces have to be in place:

1. A tunnel created in the Cloudflare dashboard under **Zero Trust → Networks → Tunnels**.
2. The `cloudflared` connector running against that tunnel. Yggdrasil ships a **cloudflared** rune for
   exactly this: create a server from it, paste the tunnel's connector token as `TUNNEL_TOKEN`, and
   start it. The connector is outbound-only and publishes no ports of its own.
3. **Settings → Domains → Cloudflare Tunnel**: an API token (encrypted at rest), your account ID,
   the tunnel ID, a base domain, an optional zone ID, and an optional internal host (defaults to the
   panel host's LAN IP).

Leave the zone ID blank and Yggdrasil resolves it from the base domain on first use and caches it.
**Test connection** always reads the tunnel's configuration, proving tunnel access. It resolves the
zone first — proving DNS access — only when no zone ID is set yet; once one is saved, or cached from
an earlier resolve, the test goes straight to the tunnel and stops exercising DNS. Either way it
tells you which half failed.

When a server with a subdomain starts, Yggdrasil adds a tunnel ingress rule mapping the hostname to
`http://<internal host>:<port>`, keeping every other rule and re-appending the mandatory trailing
`http_status:404` catch-all, then creates or updates a proxied `CNAME` from the hostname to
`<tunnel-id>.cfargotunnel.com`. The ingress rule is the critical half — if it fails, nothing is
recorded. A DNS failure is tolerated, since you may manage the record yourself.

Servers use the same per-server **Subdomain** field as NPM, and the same "port named `web`, else first
TCP port" rule applies.

## The Domains page

**Domains** is one flat list of every hostname the panel routes — one row per server per provider —
filtered by your permissions the same way the server list is. Each row shows the domain, the provider,
the internal port it routes to, and whether it is **provisioned** (a proxy host or ingress rule is
actually recorded). "Not provisioned" is normal for a server that hasn't started since you gave it a
subdomain.

Each row also carries a live check. Yggdrasil requests `https://<domain>/`, falling back to
`http://`, with a 6-second timeout, and reports whether anything answered and with what status.
Redirects are not followed — a 301 from the proxy already proves the route works — and a self-signed
certificate is retried without verification and flagged, since the route works either way. A 5xx gets
its own warning badge: the proxy answered, so routing is fine, but the app behind it may be down.
Results are cached for 30 seconds per server/provider/domain.

The domain is always recomputed server-side from the stored subdomain and never taken from the
request, and the probe refuses to connect to anything that resolves to a loopback, private,
link-local or metadata address.

## Two things that will catch you out

### A proxied Cloudflare record will not carry a game port

Cloudflare's **proxied** records — the orange cloud — carry HTTP(S) traffic and nothing else. They
are exactly right for tunnel-routed web apps, which is why Yggdrasil always creates its tunnel `CNAME`
proxied.

They are exactly wrong for a game. Point a proxied record at a Minecraft or DayZ hostname and players
cannot connect, no matter that the server is healthy, the port is forwarded, and every panel badge is
green — the raw TCP or UDP port never reaches your host. This one wastes hours because nothing is
broken; the traffic is being dropped at the edge by design.

For any hostname players use to join a game, use a **DNS-only** record (grey cloud), or hand out the
IP and port. Keep game hostnames and web-app hostnames separate so you never have to change one
record's mode back and forth.

### The Cloudflare token permission is called "Argo Tunnel (Legacy)"

Cloudflare's token UI does not offer a permission labelled "Cloudflare Tunnel". The permission
Yggdrasil needs is listed as **Argo Tunnel (Legacy) → Edit**, scoped to the whole **account** — that
is the tunnel API permission under its old name. Add **Zone → DNS: Edit** for the zone holding your
base domain and the token is complete.

Account-scoped tokens cannot call Cloudflare's `/user/tokens/verify` endpoint — it returns a
misleading "Invalid API Token" for a token that is entirely valid. Yggdrasil's **Test connection**
therefore never calls it, and reads the tunnel's configuration instead. If Test reports the tunnel
isn't reachable, check the account ID and tunnel ID first, then the Argo Tunnel permission.

## See also

- [Servers](servers.md)
- [Backups and schedules](backups-and-schedules.md)
- [Monitoring and alerts](monitoring-and-alerts.md)
- [Status page and beacon](status-page-and-beacon.md)
- [Notifications](notifications.md)
- [API reference](../reference/api.md)
