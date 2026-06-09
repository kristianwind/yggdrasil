<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { navigate } from "../lib/router.js";

  let domains = $state([]);
  let gameskills = $state([]);
  let loaded = $state(false);
  // Per-row reachability ("<server_id>|<provider>" -> {reachable, status, url}).
  let checks = $state({});

  async function load() {
    try {
      [domains, gameskills] = await Promise.all([
        api.get("/domains"),
        api.get("/gameskills").catch(() => []),
      ]);
      loaded = true;
      // Probe each domain in the background; rows fill in as results arrive.
      domains.forEach(async (d) => {
        const r = await api
          .get(`/domains/${d.server_id}/check?provider=${d.provider}`)
          .catch(() => null);
        if (r) checks = { ...checks, [key(d)]: r };
      });
    } catch (e) {
      toast(e.message, "error");
    }
  }
  onMount(load);

  function key(d) {
    return `${d.server_id}|${d.provider}`;
  }
  function runeName(id) {
    return gameskills.find((g) => g.id === id)?.name || id;
  }
  function checkBadge(c) {
    if (!c) return { icon: "…", cls: "bg-border text-muted", title: "Checking…" };
    if (!c.reachable) return { icon: "🚫", cls: "bg-danger/20 text-danger", title: "No answer from the public URL" };
    if (c.status >= 500)
      return { icon: "⚠️", cls: "bg-warn/20 text-warn", title: `Proxy answered ${c.status} — the app behind it may be down` };
    return {
      icon: "🌐",
      cls: "bg-accent2/20 text-accent",
      title: `Reachable (HTTP ${c.status}${c.self_signed ? ", self-signed cert" : ""})`,
    };
  }
</script>

<div class="flex items-center justify-between mb-2">
  <h1 class="text-2xl font-semibold">Domains</h1>
  <button class="btn-ghost" onclick={() => { checks = {}; load(); }}>↻ Refresh</button>
</div>
<p class="text-muted mb-6">
  Every public domain the panel routes — via Nginx Proxy Manager or a Cloudflare Tunnel — with a
  live check that the URL actually answers. Set a server's <b>Subdomain</b> in its settings, and
  configure a provider under Settings → Network.
</p>

<div class="card divide-y divide-border">
  {#if !loaded}
    <div class="p-4 text-muted text-sm">Loading…</div>
  {:else if domains.length === 0}
    <div class="p-4 text-muted text-sm">
      No domains yet. Give an HTTP app server a subdomain (server settings → Subdomain) and enable
      NPM or Cloudflare Tunnel under Settings → Network.
    </div>
  {/if}
  {#each domains as d (key(d))}
    {@const c = checks[key(d)]}
    {@const badge = checkBadge(c)}
    <div class="flex items-center gap-3 px-4 py-3">
      <span class="badge {badge.cls} shrink-0" title={badge.title}>{badge.icon}</span>
      <div class="flex-1 min-w-0">
        <div class="font-medium flex items-center gap-2 flex-wrap">
          <a
            href={`https://${d.domain}`}
            target="_blank"
            rel="noopener"
            class="hover:underline truncate"
            title="Open in a new tab"
          >{d.domain} ↗</a>
          <span class="badge {d.provider === 'cloudflare' ? 'bg-accent2/15 text-accent' : 'bg-panel2 border border-border text-muted'} text-[11px]">
            {d.provider === "cloudflare" ? "Cloudflare Tunnel" : "NPM"}
          </span>
          {#if !d.provisioned}
            <span class="badge bg-warn/20 text-warn text-[11px]" title="No proxy host / ingress rule recorded yet — it's created on server start">
              not provisioned
            </span>
          {/if}
        </div>
        <div class="text-xs text-muted truncate">
          <button class="hover:underline" onclick={() => navigate(`/servers/${d.server_id}`)}>
            {d.server_name}
          </button>
          · {runeName(d.gameskill_id)}
          {#if d.port}&nbsp;· → :{d.port}{/if}
          {#if c?.reachable}&nbsp;· HTTP {c.status}{/if}
        </div>
      </div>
      <span
        class="badge shrink-0 {d.status === 'running' ? 'bg-accent2/20 text-accent' : d.status === 'starting' ? 'bg-warn/20 text-warn' : 'bg-border text-muted'}"
      >{d.status}</span>
    </div>
  {/each}
</div>
