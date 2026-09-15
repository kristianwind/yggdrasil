<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { toast } from "../lib/toast.js";
  import { confirmDialog } from "../lib/dialog.js";
  import { navigate } from "../lib/router.js";

  // One list across this panel and every linked one.
  //
  // Each host keeps running its own full panel — this is not a controller that
  // owns them. So anything that touches a server's files (console, backups, the
  // file browser, install) links out to the panel that actually has the disk,
  // rather than being proxied badly from here.
  let panels = $state(null);
  let busy = $state("");

  async function load() {
    try {
      panels = await api.get("/panel/fleet");
    } catch (e) {
      toast(e.message, "error");
      panels = [];
    }
  }
  onMount(load);

  const CONFIRM = {
    stop: "Stops it now, disconnecting anyone connected.",
    restart: "Restarts it now, with no warning to anyone connected.",
  };

  async function act(panel, srv, action) {
    const ask = CONFIRM[action];
    if (ask && !(await confirmDialog({ title: `${action} ${srv.name}`, body: `${srv.name} on ${panel.name} — ${ask}`, confirmText: action }))) return;
    busy = panel.id + srv.id;
    try {
      if (panel.local) {
        await api.post(`/servers/${srv.id}/${action}`);
      } else {
        await api.post(`/panel/fleet/${panel.id}/servers/${srv.id}/${action}`);
      }
      toast(`${srv.name}: ${action}`, "success");
      await load();
    } catch (e) {
      toast(e.message, "error", 9000);
    } finally {
      busy = "";
    }
  }

  function statusClass(s) {
    return s === "running"
      ? "bg-accent2/20 text-accent"
      : s === "starting"
        ? "bg-warn/20 text-warn"
        : "bg-border text-muted";
  }
</script>

<div class="flex items-center justify-between mb-1">
  <h1 class="text-2xl font-semibold">Fleet</h1>
  <button class="btn-ghost" onclick={load}>↻ Refresh</button>
</div>
<p class="text-muted mb-6">
  Every server on this panel and on the panels you have linked. Each host still runs its own panel —
  anything that touches a server&rsquo;s files opens there, where the disk is.
</p>

{#if panels === null}
  <div class="text-muted">Loading…</div>
{:else}
  {#each panels as p}
    <div class="mb-6">
      <div class="flex items-center gap-2 mb-2">
        <h2 class="text-lg font-semibold">{p.name}</h2>
        {#if p.local}
          <span class="badge bg-accent2/15 text-accent text-[11px]">this panel</span>
        {/if}
        <span class="text-xs text-muted">{p.servers.length} server{p.servers.length === 1 ? "" : "s"}</span>
        {#if !p.local && p.url}
          <a class="text-xs hover:underline ml-auto" href={p.url} target="_blank" rel="noopener">open ↗</a>
        {/if}
      </div>

      {#if p.error}
        <!-- A panel being down costs its own rows and nothing else. The message
             says which kind of problem it is, because the fix differs: a machine
             that is off is not a token that was deleted over there. -->
        <div class="card p-4 text-sm text-warn">⚠ {p.error}</div>
      {:else if p.servers.length === 0}
        <div class="card p-4 text-sm text-muted">No servers.</div>
      {:else}
        <div class="card divide-y divide-border">
          {#each p.servers as s}
            <div class="flex items-center gap-3 px-4 py-3">
              <span class="badge shrink-0 {statusClass(s.status)}">{s.status}</span>
              <div class="flex-1 min-w-0">
                <div class="font-medium truncate">{s.name}</div>
                <div class="text-xs text-muted truncate">{s.gameskill_id}</div>
              </div>
              {#if s.status === "running"}
                <button class="btn-ghost px-2 py-1 shrink-0" disabled={busy === p.id + s.id}
                  onclick={() => act(p, s, "restart")}>Restart</button>
                <button class="btn-ghost px-2 py-1 shrink-0" disabled={busy === p.id + s.id}
                  onclick={() => act(p, s, "stop")}>Stop</button>
              {:else}
                <button class="btn-ghost px-2 py-1 shrink-0" disabled={busy === p.id + s.id}
                  onclick={() => act(p, s, "start")}>Start</button>
              {/if}
              {#if p.local}
                <button class="btn-ghost px-2 py-1 shrink-0" onclick={() => navigate(`/servers/${s.id}`)}>Open</button>
              {:else}
                <a class="btn-ghost px-2 py-1 shrink-0" href={`${p.url}/#/servers/${s.id}`} target="_blank" rel="noopener">Open ↗</a>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/each}

  {#if panels.length <= 1}
    <div class="card p-4 text-sm text-muted">
      No other panels linked yet. Add one under <b>Servers → Pull from another panel</b> by ticking
      <b>Remember this panel</b>. Create its token on that panel as a <b>Link</b> token, so this one
      can show and operate its servers without being able to reconfigure or export them.
    </div>
  {/if}
{/if}
