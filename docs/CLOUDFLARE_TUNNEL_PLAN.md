# Feature: Cloudflare Tunnel subdomain integration

Status: **built (phase 1), on branch `cloudflare-tunnel`, dev-deployed to .158 for review**
(2026-06-04). Sibling to the NPM integration — same per-server `subdomain` field, same
lifecycle-hook shape, but routes through a **Cloudflare Tunnel** instead of a reverse proxy.

## Why it fits the same pattern (and is actually simpler than NPM)
A remotely-managed Cloudflare Tunnel has exactly two API-managed pieces per host:
1. an **ingress rule** in the tunnel config: `hostname → http://<internal_host>:<port>`
2. a **proxied CNAME** DNS record: `hostname → <tunnel-id>.cfargotunnel.com`

Auth is a single **API token** (Bearer) — stateless, no login/CSRF dance. And because the
tunnel is **outbound**, there's no port forwarding / public IP at all (pairs naturally with
`auto_forward = off`). On server start we upsert (1)+(2); on stop/delete we remove them.

## Implementation
- `internal/cloudflare/cloudflare.go` — client: `Verify`, `ResolveZoneID`, `UpsertHostname`/
  `RemoveHostname` (GET→modify→PUT the tunnel config map, preserving unmanaged keys + the
  mandatory trailing `http_status:404` catch-all), `EnsureDNS`/`RemoveDNS` (proxied CNAME).
- DB: `servers.cf_hostname TEXT` — the full hostname we provisioned (drives precise teardown,
  robust to subdomain changes; no need to recompute from the base domain at delete time).
- `internal/api/handlers_cloudflare.go` — `cfClient` (loads/decrypts settings, auto-resolves &
  caches `cf_zone_id` from the base domain), `cfAddServer`/`cfRemoveServer`, and
  `GET/PUT/test /api/settings/cloudflare`.
- Lifecycle: `cfAddServer` beside `npmAddServer` in `recreateAndStart`; `cfRemoveServer` beside
  `npmRemoveServer` in stop, delete (sync), `stoppedCleanup`, and the subdomain-change path.
- Settings (`app_settings`): `cf_enabled`, `cf_api_token` (encrypted), `cf_account_id`,
  `cf_zone_id`, `cf_tunnel_id`, `cf_base_domain`, `cf_internal_host` (default LAN IP).
- UI: Settings → Network **Cloudflare Tunnel** card; the existing per-server Subdomain field is
  shared (hint updated to "NPM or Cloudflare Tunnel").
- **`community-runes/apps/cloudflared.yaml`** — launch the connector from the panel: distroless
  image via `keep_entrypoint` + `startup.exec: [tunnel, --no-autoupdate, run]`, token in
  `TUNNEL_TOKEN`. Outbound-only, no ports.

## Out-of-band prereqs (document in UI help — already in the card text)
- A tunnel created in the Cloudflare dashboard (Zero Trust → Networks → Tunnels) with the
  `cloudflared` connector running (the rune above, or host-native).
- An API token with **Account → Cloudflare Tunnel: Edit** + **Zone → DNS: Edit**.
- The connector token (`TUNNEL_TOKEN`), the account id, the tunnel id, and the base domain.
  Zone id auto-resolves from the base domain.

## NPM vs Cloudflare (both can be enabled; you normally pick one)
| | NPM | Cloudflare Tunnel |
|---|---|---|
| Public exposure | reverse proxy + port forward (80/443 → NPM) | outbound tunnel, no forwarding |
| DNS | wildcard `*.domain → public IP` (manual) | per-host proxied CNAME (auto) |
| TLS | LE via NPM (HTTP-01) | Cloudflare edge (automatic) |
| Per-server field | `subdomain` (shared) | `subdomain` (shared) |

## Gotchas / notes
- The tunnel config is a single shared document → every add/remove is GET-modify-PUT; the
  catch-all `http_status:404` must stay last or Cloudflare rejects the PUT.
- DNS record **must** be `proxied: true` (orange cloud) to resolve to the tunnel.
- Configuring ingress/DNS works even when the connector is down — it just won't serve until
  `cloudflared` reconnects.
- If both NPM and Cloudflare are enabled, both will try to route the same `subdomain`; enable
  only the one you use (DNS can only point one way anyway).

## Test plan (for tomorrow)
1. Start the `cloudflared` rune with a real `TUNNEL_TOKEN` (or run cloudflared host-native).
2. Settings → Cloudflare: token + account id + tunnel id + base domain → **Test** (zone resolves).
3. Create a memos server, subdomain `notes`, start → tunnel gets an ingress rule + a proxied
   CNAME `notes.<base>`; `https://notes.<base>` serves memos with a valid CF cert.
4. Stop/delete → ingress rule + CNAME removed.
5. A DayZ server (UDP-only) shows no subdomain field and is untouched.

## Open
- Phase 2 (shared with NPM): a **Domains** overview menu listing apps + subdomain + provider +
  reachable badge.
- Optional: a per-server provider selector (NPM vs Cloudflare) instead of "both fire if enabled".
