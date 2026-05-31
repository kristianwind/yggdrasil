<script>
  import { onMount } from "svelte";
  import { api } from "../lib/api.js";
  import { user } from "../lib/auth.js";

  let info = $state(null);
  let servers = $state([]);
  let error = $state("");

  onMount(async () => {
    try {
      servers = await api.get("/servers");
      if ($user.role === "admin") info = await api.get("/system/info");
    } catch (e) {
      error = e.message;
    }
  });

  const stat = (label, value, sub) => ({ label, value, sub });
  let cards = $derived(
    info
      ? [
          stat("Servers", info.servers, `${info.servers_running} running`),
          stat("Runes", info.gameskills, "game definitions"),
          stat("Users", info.users, ""),
          stat("Docker", info.docker_ok ? "OK" : "Down", info.arch),
        ]
      : [stat("Servers", servers.length, `${servers.filter((s) => s.status === "running").length} running`)],
  );
</script>

<h1 class="text-2xl font-semibold mb-1">Dashboard</h1>
<p class="text-muted mb-6">Welcome back, {$user.username}.</p>

{#if error}
  <div class="card border-danger p-3 text-danger text-sm mb-4">{error}</div>
{/if}

<div class="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-8">
  {#each cards as c}
    <div class="card p-4">
      <div class="text-muted text-xs uppercase tracking-wide">{c.label}</div>
      <div class="text-2xl font-semibold mt-1">{c.value}</div>
      {#if c.sub}<div class="text-muted text-xs mt-0.5">{c.sub}</div>{/if}
    </div>
  {/each}
</div>

<h2 class="text-lg font-semibold mb-3">Recent servers</h2>
<div class="card divide-y divide-border">
  {#if servers.length === 0}
    <div class="p-4 text-muted text-sm">No servers yet. Create one from the Servers page.</div>
  {/if}
  {#each servers.slice(0, 8) as s}
    <a href={`#/servers/${s.id}`} class="flex items-center justify-between px-4 py-3 hover:bg-panel2/50">
      <span class="font-medium">{s.name}</span>
      <span
        class="badge {s.status === 'running' ? 'bg-accent2/20 text-accent' : 'bg-border text-muted'}"
        >{s.status}</span
      >
    </a>
  {/each}
</div>
